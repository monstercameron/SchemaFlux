package telemetry

import (
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
)

// MetricEvent represents a single metric observation before aggregation.
type MetricEvent struct {
	Name      string
	Value     float64
	Tags      map[string]string
	Timestamp time.Time
}

// MetricSnapshot is an aggregated view of all observations for a metric/tag set.
type MetricSnapshot struct {
	Name      string
	Tags      map[string]string
	Count     int64
	Sum       float64
	Min       float64
	Max       float64
	LastValue float64
	UpdatedAt time.Time
}

// MetricSink receives individual metric events for external export.
//
// RecordMetric runs on the goroutine that recorded the metric, which is
// usually the one making an LLM call, so a sink that blocks stalls that call.
// A sink that has slow work to do (a network export) should hand the event to
// its own queue and return. Sinks are called one after another in the order
// they were registered, and a sink that panics is recovered and logged without
// the event, so it cannot stop the sinks after it or unwind into the caller.
type MetricSink interface {
	RecordMetric(event MetricEvent)
}

type metricKey struct {
	name string
	tags string
}

// MetricSeriesLimit is the most series the registry holds. Each distinct
// metric name and tag set is a series kept for the life of the process, and a
// tag whose values come from outside the program, a model name a provider
// answers with, would otherwise grow the registry without limit. A series that
// would pass the limit is not created; see OverflowMetricName.
const MetricSeriesLimit = 2048

// OverflowMetricName is the one series that counts the observations the
// registry turned away because it was at MetricSeriesLimit. It has no tags,
// its Count and Sum are the number of observations, and it exists only once
// the limit has been reached. A sink is still handed every observation.
const OverflowMetricName = "schemaflux_metric_series_overflow"

// MetricTagValueLimit is the most distinct values the registry keeps for any one
// tag key, across every metric. The series limit is first come, first kept, so a
// tag whose values come from outside the program, a model name that a gateway
// makes up for every request, would fill it in a few hundred requests, and no
// series of any metric that came after, the library's own for a provider or an
// outcome not yet seen included, could be created. Each tag is bound on its own
// instead: once a key has held this many values, a value it has not held is
// recorded as OverflowTagValue, so one tag can multiply the series of a metric
// by at most this many and one more, whatever it is given. The values the
// library gives a tag from its own vocabulary, outcomes, modes and tiers, are
// far fewer. A sink is handed the value as it was, not the one the registry
// keeps.
const MetricTagValueLimit = 64

// OverflowTagValue is the value the registry records for a tag value it turned
// away because the tag already held MetricTagValueLimit others.
const OverflowTagValue = "other"

type registeredSink struct {
	id   uint64
	sink MetricSink
}

// metricRegistry holds the aggregates and the sinks. sinks is replaced rather
// than edited in place, so RecordMetricValue can take the slice it is about to
// walk under the lock and release the lock before calling into any sink.
var metricRegistry = struct {
	mu      sync.RWMutex
	metrics map[metricKey]*MetricSnapshot
	// overflow is the series named OverflowMetricName, nil until an
	// observation has been turned away. It is kept apart from metrics so that
	// it does not count toward the limit it reports on.
	overflow *MetricSnapshot
	sinks    []registeredSink
	nextID   uint64
	// tagValues is, for each tag key, the values the registry has kept for it, at
	// most MetricTagValueLimit.
	tagValues map[string]map[string]struct{}
}{
	metrics:   make(map[metricKey]*MetricSnapshot),
	tagValues: make(map[string]map[string]struct{}),
}

// sinkCount is how many sinks are registered, kept apart from the registry so
// that asking whether to record is one atomic load and takes no lock.
var sinkCount atomic.Int32

// HasMetricSink reports whether a metric sink is registered.
func HasMetricSink() bool { return sinkCount.Load() > 0 }

// MetricsEnabled reports whether the library should record its metrics: when a
// sink is registered, which is a host asking to be given them, or when the
// process turned collection on with config.SetMetricsEnabled or
// SCHEMAFLUX_METRICS. Every site that records asks it first, so with neither
// holding it costs an atomic load and the two of config.IsMetricsEnabled, and
// nothing is built for a metric nobody reads. A sink registered while the
// variable is unset used to receive nothing, because the switch was the only
// gate and registering a sink did not turn it on.
func MetricsEnabled() bool { return HasMetricSink() || config.IsMetricsEnabled() }

// RecordMetric records an integer-valued metric observation.
func RecordMetric(name string, value int64, tags map[string]string) {
	RecordMetricValue(name, float64(value), tags)
}

// RecordMetricValue records a metric observation and updates the in-process
// aggregate registry. A NaN or infinite value is dropped: it would make the
// series' sum, minimum and maximum meaningless for every observation after it.
// An observation that would start a series past MetricSeriesLimit is counted
// in the overflow series instead, and one recorded under the overflow series'
// own name is dropped, since that series is the registry's to write.
func RecordMetricValue(name string, value float64, tags map[string]string) {
	if !MetricsEnabled() || strings.TrimSpace(name) == "" || name == OverflowMetricName {
		return
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}

	now := time.Now().UTC()
	event := MetricEvent{
		Name:      name,
		Value:     value,
		Tags:      cloneTags(tags),
		Timestamp: now,
	}

	metricRegistry.mu.Lock()
	// The registry's series are keyed by the tags it keeps, with a tag value past
	// that tag's limit recorded as OverflowTagValue; the event a sink is handed
	// keeps the value as it was.
	registryTags := boundTagValues(event.Tags)
	key := metricKey{
		name: name,
		tags: canonicalTags(registryTags),
	}
	aggregate, ok := metricRegistry.metrics[key]
	if !ok {
		if len(metricRegistry.metrics) >= MetricSeriesLimit {
			// No room for a new series. What is counted is that an
			// observation was turned away, not its value: values of
			// different metrics do not add up to anything.
			if metricRegistry.overflow == nil {
				metricRegistry.overflow = &MetricSnapshot{Name: OverflowMetricName, Tags: map[string]string{}}
			}
			aggregate, value = metricRegistry.overflow, 1
		} else {
			aggregate = &MetricSnapshot{
				Name: name,
				Tags: cloneTags(registryTags),
			}
			metricRegistry.metrics[key] = aggregate
		}
	}

	aggregate.Count++
	aggregate.Sum += value
	if aggregate.Count == 1 || value < aggregate.Min {
		aggregate.Min = value
	}
	if aggregate.Count == 1 || value > aggregate.Max {
		aggregate.Max = value
	}
	aggregate.LastValue = value
	aggregate.UpdatedAt = now

	sinks := metricRegistry.sinks
	metricRegistry.mu.Unlock()

	if config.GetDebugMode() {
		logger.GetLogger().Debug("Metric recorded",
			"name", name,
			"value", event.Value,
			"tags", event.Tags,
		)
	}

	for _, registered := range sinks {
		delivered := event
		if len(sinks) > 1 {
			// Each sink has a map of its own: one that keeps it, or writes to it,
			// cannot be seen by or race with the next.
			delivered.Tags = cloneTags(event.Tags)
		}
		deliverToSink(registered.sink, delivered)
	}
}

// deliverToSink hands one event to one sink. The sink is the caller's code, so
// a panic in it is contained here and reported by metric name only: the
// recovered value and the event's tags can both hold the caller's data.
func deliverToSink(sink MetricSink, event MetricEvent) {
	defer func() {
		if recover() != nil {
			logger.GetLogger().Error(
				"A metric sink panicked; the observation was not delivered to it",
				"metric", event.Name,
			)
		}
	}()
	sink.RecordMetric(event)
}

// GetMetricSnapshot returns the aggregate for a specific metric/tag set.
func GetMetricSnapshot(name string, tags map[string]string) (MetricSnapshot, bool) {
	metricRegistry.mu.RLock()
	defer metricRegistry.mu.RUnlock()

	if name == OverflowMetricName {
		if metricRegistry.overflow == nil || len(tags) != 0 {
			return MetricSnapshot{}, false
		}
		return cloneSnapshot(*metricRegistry.overflow), true
	}

	snapshot, ok := metricRegistry.metrics[metricKey{
		name: name,
		tags: canonicalTags(tags),
	}]
	if !ok {
		return MetricSnapshot{}, false
	}

	return cloneSnapshot(*snapshot), true
}

// SnapshotMetrics returns all metric aggregates sorted by name and tags, the
// overflow series among them once there is one.
func SnapshotMetrics() []MetricSnapshot {
	metricRegistry.mu.RLock()
	defer metricRegistry.mu.RUnlock()

	snapshots := make([]MetricSnapshot, 0, len(metricRegistry.metrics)+1)
	for _, aggregate := range metricRegistry.metrics {
		snapshots = append(snapshots, cloneSnapshot(*aggregate))
	}
	if metricRegistry.overflow != nil {
		snapshots = append(snapshots, cloneSnapshot(*metricRegistry.overflow))
	}

	slices.SortFunc(snapshots, func(a, b MetricSnapshot) int {
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(canonicalTags(a.Tags), canonicalTags(b.Tags))
	})

	return snapshots
}

// ResetMetrics clears all recorded metric aggregates, the overflow series
// included, and the values each tag has held.
func ResetMetrics() {
	metricRegistry.mu.Lock()
	defer metricRegistry.mu.Unlock()
	metricRegistry.metrics = make(map[metricKey]*MetricSnapshot)
	metricRegistry.overflow = nil
	metricRegistry.tagValues = make(map[string]map[string]struct{})
}

// boundTagValues returns the tags the registry keys a series by: tags itself
// when every value is one its key already holds, or can hold, and otherwise a
// copy in which each value past its key's MetricTagValueLimit is
// OverflowTagValue. A value the key can still hold is kept, so the first values
// of a tag are the ones that keep their own series. The caller holds the
// registry's lock.
func boundTagValues(tags map[string]string) map[string]string {
	var bounded map[string]string
	for key, value := range tags {
		held := metricRegistry.tagValues[key]
		if _, known := held[value]; known {
			continue
		}
		if len(held) < MetricTagValueLimit {
			if held == nil {
				held = make(map[string]struct{}, 4)
				metricRegistry.tagValues[key] = held
			}
			held[value] = struct{}{}
			continue
		}
		if bounded == nil {
			bounded = cloneTags(tags)
		}
		bounded[key] = OverflowTagValue
	}
	if bounded == nil {
		return tags
	}
	return bounded
}

// RegisterMetricSink registers an external sink and returns a function that
// removes it. Sinks receive events in the order they were registered.
func RegisterMetricSink(sink MetricSink) func() {
	if sink == nil {
		return func() {}
	}

	metricRegistry.mu.Lock()
	id := metricRegistry.nextID
	metricRegistry.nextID++
	metricRegistry.sinks = append(metricRegistry.sinks, registeredSink{id: id, sink: sink})
	sinkCount.Store(int32(len(metricRegistry.sinks)))
	metricRegistry.mu.Unlock()

	return func() {
		metricRegistry.mu.Lock()
		defer metricRegistry.mu.Unlock()

		kept := make([]registeredSink, 0, len(metricRegistry.sinks))
		for _, registered := range metricRegistry.sinks {
			if registered.id != id {
				kept = append(kept, registered)
			}
		}
		metricRegistry.sinks = kept
		sinkCount.Store(int32(len(kept)))
	}
}

// Average returns the arithmetic mean of the observations in the snapshot.
func (snapshot MetricSnapshot) Average() float64 {
	if snapshot.Count == 0 {
		return 0
	}
	return snapshot.Sum / float64(snapshot.Count)
}

func cloneSnapshot(snapshot MetricSnapshot) MetricSnapshot {
	return MetricSnapshot{
		Name:      snapshot.Name,
		Tags:      cloneTags(snapshot.Tags),
		Count:     snapshot.Count,
		Sum:       snapshot.Sum,
		Min:       snapshot.Min,
		Max:       snapshot.Max,
		LastValue: snapshot.LastValue,
		UpdatedAt: snapshot.UpdatedAt,
	}
}

func cloneTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(tags))
	for key, value := range tags {
		cloned[key] = value
	}
	return cloned
}

// writeEscapedTag escapes the three bytes canonicalTags uses as syntax, so a
// key or value that contains them cannot be read as a boundary.
func writeEscapedTag(encoded *strings.Builder, value string) {
	start := 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\', ',', '=':
			encoded.WriteString(value[start:i])
			encoded.WriteByte('\\')
			encoded.WriteByte(value[i])
			start = i + 1
		}
	}
	encoded.WriteString(value[start:])
}

// canonicalTags renders a tag set as one string that is the same for equal
// sets and different for different ones, independent of map order. Without the
// escaping, {"a": "1,b=2"} and {"a": "1", "b": "2"} would render alike and
// share a series.
func canonicalTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}

	var smallKeys [8]string
	keys := smallKeys[:0]
	if len(tags) > len(smallKeys) {
		keys = make([]string, 0, len(tags))
	}
	for key := range tags {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	// Most tag sets have no characters to escape. Reserve their exact encoded
	// length; escaped sets may grow the builder, but never need per-tag strings.
	length := len(keys) - 1
	for _, key := range keys {
		length += len(key) + 1 + len(tags[key])
	}
	var encoded strings.Builder
	encoded.Grow(length)
	for i, key := range keys {
		if i > 0 {
			encoded.WriteByte(',')
		}
		writeEscapedTag(&encoded, key)
		encoded.WriteByte('=')
		writeEscapedTag(&encoded, tags[key])
	}
	return encoded.String()
}

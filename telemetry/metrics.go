package telemetry

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
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
type MetricSink interface {
	RecordMetric(event MetricEvent)
}

type metricKey struct {
	name string
	tags string
}

type metricAggregate struct {
	snapshot MetricSnapshot
}

var metricRegistry = struct {
	mu      sync.RWMutex
	metrics map[metricKey]*metricAggregate
	sinks   map[uint64]MetricSink
	nextID  uint64
}{
	metrics: make(map[metricKey]*metricAggregate),
	sinks:   make(map[uint64]MetricSink),
}

// RecordMetric records an integer-valued metric observation.
func RecordMetric(name string, value int64, tags map[string]string) {
	RecordMetricValue(name, float64(value), tags)
}

// RecordMetricValue records a metric observation and updates the in-process aggregate registry.
func RecordMetricValue(name string, value float64, tags map[string]string) {
	if !config.IsMetricsEnabled() || strings.TrimSpace(name) == "" {
		return
	}

	now := time.Now().UTC()
	event := MetricEvent{
		Name:      name,
		Value:     value,
		Tags:      cloneTags(tags),
		Timestamp: now,
	}

	key := metricKey{
		name: name,
		tags: canonicalTags(event.Tags),
	}

	var sinks []MetricSink

	metricRegistry.mu.Lock()
	aggregate, ok := metricRegistry.metrics[key]
	if !ok {
		aggregate = &metricAggregate{
			snapshot: MetricSnapshot{
				Name:      name,
				Tags:      cloneTags(event.Tags),
				Min:       value,
				Max:       value,
				LastValue: value,
				UpdatedAt: now,
			},
		}
		metricRegistry.metrics[key] = aggregate
	}

	aggregate.snapshot.Count++
	aggregate.snapshot.Sum += value
	if aggregate.snapshot.Count == 1 || value < aggregate.snapshot.Min {
		aggregate.snapshot.Min = value
	}
	if aggregate.snapshot.Count == 1 || value > aggregate.snapshot.Max {
		aggregate.snapshot.Max = value
	}
	aggregate.snapshot.LastValue = value
	aggregate.snapshot.UpdatedAt = now

	for _, sink := range metricRegistry.sinks {
		sinks = append(sinks, sink)
	}
	metricRegistry.mu.Unlock()

	if config.GetDebugMode() {
		logger.GetLogger().Debug("Metric recorded",
			"name", name,
			"value", value,
			"tags", event.Tags,
		)
	}

	for _, sink := range sinks {
		sink.RecordMetric(event)
	}
}

// GetMetricSnapshot returns the aggregate for a specific metric/tag set.
func GetMetricSnapshot(name string, tags map[string]string) (MetricSnapshot, bool) {
	metricRegistry.mu.RLock()
	defer metricRegistry.mu.RUnlock()

	snapshot, ok := metricRegistry.metrics[metricKey{
		name: name,
		tags: canonicalTags(tags),
	}]
	if !ok {
		return MetricSnapshot{}, false
	}

	return cloneSnapshot(snapshot.snapshot), true
}

// SnapshotMetrics returns all metric aggregates sorted by name and tags.
func SnapshotMetrics() []MetricSnapshot {
	metricRegistry.mu.RLock()
	defer metricRegistry.mu.RUnlock()

	snapshots := make([]MetricSnapshot, 0, len(metricRegistry.metrics))
	for _, aggregate := range metricRegistry.metrics {
		snapshots = append(snapshots, cloneSnapshot(aggregate.snapshot))
	}

	slices.SortFunc(snapshots, func(a, b MetricSnapshot) int {
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(canonicalTags(a.Tags), canonicalTags(b.Tags))
	})

	return snapshots
}

// ResetMetrics clears all recorded metric aggregates.
func ResetMetrics() {
	metricRegistry.mu.Lock()
	defer metricRegistry.mu.Unlock()
	metricRegistry.metrics = make(map[metricKey]*metricAggregate)
}

// RegisterMetricSink registers an external sink and returns a function that removes it.
func RegisterMetricSink(sink MetricSink) func() {
	if sink == nil {
		return func() {}
	}

	metricRegistry.mu.Lock()
	id := metricRegistry.nextID
	metricRegistry.nextID++
	metricRegistry.sinks[id] = sink
	metricRegistry.mu.Unlock()

	return func() {
		metricRegistry.mu.Lock()
		delete(metricRegistry.sinks, id)
		metricRegistry.mu.Unlock()
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

func canonicalTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}

	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+tags[key])
	}
	return strings.Join(parts, ",")
}

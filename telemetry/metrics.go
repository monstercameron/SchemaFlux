package telemetry

import (
	"sync"
	"time"

	wrapped "github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
)

// A Metric is one observation of one of the library's own metrics, as it is
// recorded, before it is aggregated.
type Metric struct {
	// Name is the metric's name: "llm_requests", "llm_request_duration_ms".
	Name string

	// Value is the observation. It is always a finite number: the library does
	// not record one that is not.
	Value float64

	// Tags describe what the observation is about: the provider, the model, the
	// mode, the tier, the outcome. Every value of a tag that came from outside
	// the program is at most 64 bytes of printable ASCII. A nil or empty Tags
	// means the observation has none. The map is the Sink's own: each sink is
	// handed a copy, so one that keeps it or changes it is not seen by another.
	Tags map[string]string

	// Timestamp is when the observation was recorded, in UTC.
	Timestamp time.Time
}

// A Snapshot is the aggregate of every observation of one metric with one set
// of tags.
type Snapshot struct {
	// Name and Tags identify the series. A series with no tags has an empty,
	// non-nil Tags.
	Name string
	Tags map[string]string

	// Count is the number of observations, and Sum, Min and Max aggregate
	// their values. A series is created by its first observation, so Count is
	// at least 1.
	Count int64
	Sum   float64
	// Min is the smallest observed value in this series.
	Min float64
	// Max is the largest observed value in this series.
	Max float64

	// LastValue is the most recent observation's value.
	LastValue float64

	// UpdatedAt is when the most recent observation was recorded, in UTC.
	UpdatedAt time.Time
}

// Average returns the arithmetic mean of the observations of the snapshot, or
// zero when it holds none.
func (s Snapshot) Average() float64 {
	if s.Count == 0 {
		return 0
	}
	return s.Sum / float64(s.Count)
}

// A Sink receives each observation of the library's metrics as it is recorded,
// for export.
//
// Record runs on the goroutine that recorded the metric, which is usually the
// one making a call, so a Sink that blocks stalls that call. A Sink with slow
// work to do, a network export, hands the observation to its own queue and
// returns. Sinks are called one after another in the order they were
// registered, and one that panics is recovered and logged, by the metric's name
// alone, without the observation, so it cannot stop the sinks after it or
// unwind into the caller.
type Sink interface {
	Record(m Metric)
}

// RegisterSink registers s beside the sinks already registered and returns the
// function that removes this registration and no other. The removals may run in
// any order and more than once. A nil sink registers nothing, and its remove does
// nothing.
//
// The library records its metrics while at least one sink is registered, or
// while the process has turned recording on (SCHEMAFLUX_METRICS), and Snapshots
// aggregates what is recorded. When the last sink is removed recording stops,
// unless the process turned it on, and the aggregates are left as they were.
func RegisterSink(s Sink) (remove func()) {
	if s == nil {
		return func() {}
	}
	removeBase := wrapped.RegisterMetricSink(sink{s})
	var once sync.Once
	return func() { once.Do(removeBase) }
}

// sink is a registered Sink as the package it wraps sees it.
type sink struct{ sink Sink }

func (s sink) RecordMetric(event wrapped.MetricEvent) {
	s.sink.Record(Metric{Name: event.Name, Value: event.Value, Tags: event.Tags, Timestamp: event.Timestamp})
}

// Snapshots returns the aggregate of every series recorded so far, sorted by
// name and then by tags. Each snapshot is a copy: changing it changes nothing the
// registry holds. A series is aggregated only while a sink is registered, so
// with none ever registered the result is empty.
//
// The registry keeps at most 64 distinct values for each tag, and records a value
// past that as "other", so that one tag cannot fill the registry for everything
// else; and at most 2048 series, one for each distinct name and set of tags, for
// the life of the process, because a tag whose values come from outside the
// program would otherwise grow it without limit. An observation that
// would start a series past the limit is counted in one series of its own, named
// "schemaflux_metric_series_overflow", which has no tags and whose Count and Sum
// are the number of observations turned away. It exists only once the limit has
// been reached, and it is among the snapshots when it does.
func Snapshots() []Snapshot {
	recorded := wrapped.SnapshotMetrics()
	snapshots := make([]Snapshot, len(recorded))
	for i, s := range recorded {
		snapshots[i] = Snapshot{
			Name: s.Name, Tags: s.Tags,
			Count: s.Count, Sum: s.Sum, Min: s.Min, Max: s.Max,
			LastValue: s.LastValue, UpdatedAt: s.UpdatedAt,
		}
	}
	return snapshots
}

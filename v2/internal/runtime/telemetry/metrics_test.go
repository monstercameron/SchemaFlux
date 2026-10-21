package telemetry

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type testMetricSink struct {
	mu     sync.Mutex
	events []MetricEvent
}

func (s *testMetricSink) RecordMetric(event MetricEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *testMetricSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func TestRecordMetricAggregatesSnapshots(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetric("extract_duration", 120, map[string]string{"mode": "strict", "type": "Person"})
	RecordMetric("extract_duration", 80, map[string]string{"type": "Person", "mode": "strict"})

	snapshot, ok := GetMetricSnapshot("extract_duration", map[string]string{"mode": "strict", "type": "Person"})
	if !ok {
		t.Fatal("expected snapshot to exist")
	}

	if snapshot.Count != 2 {
		t.Fatalf("expected count 2, got %d", snapshot.Count)
	}
	if snapshot.Sum != 200 {
		t.Fatalf("expected sum 200, got %v", snapshot.Sum)
	}
	if snapshot.Min != 80 {
		t.Fatalf("expected min 80, got %v", snapshot.Min)
	}
	if snapshot.Max != 120 {
		t.Fatalf("expected max 120, got %v", snapshot.Max)
	}
	if snapshot.LastValue != 80 {
		t.Fatalf("expected last value 80, got %v", snapshot.LastValue)
	}
	if snapshot.Average() != 100 {
		t.Fatalf("expected average 100, got %v", snapshot.Average())
	}
	if snapshot.Tags["mode"] != "strict" || snapshot.Tags["type"] != "Person" {
		t.Fatalf("unexpected tags: %#v", snapshot.Tags)
	}
}

func TestRecordMetricValueSupportsDecimals(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetricValue("llm_cost_total_usd", 0.0125, map[string]string{"model": "gpt-5-mini"})
	RecordMetricValue("llm_cost_total_usd", 0.0375, map[string]string{"model": "gpt-5-mini"})

	snapshot, ok := GetMetricSnapshot("llm_cost_total_usd", map[string]string{"model": "gpt-5-mini"})
	if !ok {
		t.Fatal("expected cost snapshot to exist")
	}
	if snapshot.Sum != 0.05 {
		t.Fatalf("expected sum 0.05, got %v", snapshot.Sum)
	}
	if snapshot.Average() != 0.025 {
		t.Fatalf("expected average 0.025, got %v", snapshot.Average())
	}
}

func TestRecordMetricDisabledSkipsStorage(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(false)

	RecordMetric("generate_duration", 42, nil)

	if snapshots := SnapshotMetrics(); len(snapshots) != 0 {
		t.Fatalf("expected no snapshots, got %d", len(snapshots))
	}
}

// GetMetricSnapshot for a metric/tag combination nobody recorded must report
// "not found" rather than a zero-valued snapshot that reads as "this metric
// really was observed, and it was zero every time."
func TestGetMetricSnapshotUnknownMetricReportsNotFound(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)

	snapshot, ok := GetMetricSnapshot("no_such_metric_was_ever_recorded", nil)
	if ok {
		t.Fatalf("expected ok=false for an unrecorded metric, got snapshot %+v", snapshot)
	}
	if snapshot.Name != "" || snapshot.Count != 0 || len(snapshot.Tags) != 0 {
		t.Fatalf("expected the zero MetricSnapshot on a miss, got %+v", snapshot)
	}
}

// Average on a snapshot nothing has recorded into (Count == 0) must return 0
// rather than dividing by zero.
func TestMetricSnapshotAverageWithNoObservations(t *testing.T) {
	var snapshot MetricSnapshot
	if got := snapshot.Average(); got != 0 {
		t.Fatalf("Average() on an empty snapshot = %v, want 0", got)
	}
}

// RecordMetricValue with a blank (or whitespace-only) name must be silently
// skipped -- there is no metric to attach a series to.
func TestRecordMetricValueBlankNameIsSkipped(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetricValue("", 1, nil)
	RecordMetricValue("   ", 1, nil)

	if snapshots := SnapshotMetrics(); len(snapshots) != 0 {
		t.Fatalf("expected a blank metric name to record nothing, got %d snapshots", len(snapshots))
	}
}

// RegisterMetricSink(nil) must return a harmless no-op unregister rather than
// panicking later when the registry is walked with a nil sink inside it.
func TestRegisterMetricSinkNilIsHarmless(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	unregister := RegisterMetricSink(nil)
	if unregister == nil {
		t.Fatal("RegisterMetricSink(nil) returned a nil unregister func")
	}
	unregister() // must not panic

	// A real recording must still work: no nil sink was left behind to crash on.
	RecordMetric("after_nil_sink", 1, nil)
	if _, ok := GetMetricSnapshot("after_nil_sink", nil); !ok {
		t.Fatal("expected the metric to be recorded even after registering/unregistering a nil sink")
	}
}

// cloneTags(nil) and cloneTags({}) both return a non-nil empty map -- the
// snapshot's Tags field is never nil, so a caller can range over it
// unconditionally.
func TestCloneTagsNormalizesEmptyAndNil(t *testing.T) {
	if got := cloneTags(nil); got == nil || len(got) != 0 {
		t.Fatalf("cloneTags(nil) = %#v, want a non-nil empty map", got)
	}
	if got := cloneTags(map[string]string{}); got == nil || len(got) != 0 {
		t.Fatalf("cloneTags({}) = %#v, want a non-nil empty map", got)
	}

	original := map[string]string{"a": "1"}
	cloned := cloneTags(original)
	cloned["a"] = "mutated"
	if original["a"] != "1" {
		t.Fatal("cloneTags did not deep-copy; mutating the clone changed the source map")
	}
}

// canonicalTags(nil)/({}) must both be "" so an untagged metric and an
// explicitly-empty-tagged one collapse to the same aggregate key.
func TestCanonicalTagsEmptyIsBlank(t *testing.T) {
	if got := canonicalTags(nil); got != "" {
		t.Fatalf("canonicalTags(nil) = %q, want empty", got)
	}
	if got := canonicalTags(map[string]string{}); got != "" {
		t.Fatalf("canonicalTags({}) = %q, want empty", got)
	}
}

func TestRegisterMetricSinkReceivesEvents(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	sink := &testMetricSink{}
	unregister := RegisterMetricSink(sink)
	t.Cleanup(unregister)

	RecordMetric("transform_duration", 33, map[string]string{"mode": "transform"})

	if sink.count() != 1 {
		t.Fatalf("expected sink to receive one event, got %d", sink.count())
	}

	unregister()
	RecordMetric("transform_duration", 44, map[string]string{"mode": "transform"})

	if sink.count() != 1 {
		t.Fatalf("expected sink count to remain 1 after unregister, got %d", sink.count())
	}
}

// lockedBuffer is a log destination safe to write from the goroutines a test
// starts.
type lockedBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// captureLogs sends the library's log to a buffer for the length of a test and
// returns a function reading what has been written so far.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	buffer := &lockedBuffer{}
	previous := logger.GetLogger()
	logger.ConfigureLogger(logger.LoggerConfig{Level: logger.DebugLevel, Format: "text", Output: buffer})
	t.Cleanup(func() { logger.SetLogger(previous) })
	return buffer.String
}

type funcMetricSink func(MetricEvent)

func (f funcMetricSink) RecordMetric(event MetricEvent) { f(event) }

// A value that is not a finite number would make Sum, Min and Max meaningless
// for every later observation of the series, so it is not recorded at all.
func TestRecordMetricValueDropsNonFiniteValues(t *testing.T) {
	cases := []struct {
		name     string
		value    float64
		recorded bool
	}{
		{"NaN", math.NaN(), false},
		{"positive infinity", math.Inf(1), false},
		{"negative infinity", math.Inf(-1), false},
		{"zero", 0, true},
		{"negative zero", math.Copysign(0, -1), true},
		{"one", 1, true},
		{"negative", -2.5, true},
		{"largest finite", math.MaxFloat64, true},
		{"most negative finite", -math.MaxFloat64, true},
		{"smallest subnormal", math.SmallestNonzeroFloat64, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enableMetrics(t)
			sink := &testMetricSink{}
			t.Cleanup(RegisterMetricSink(sink))

			RecordMetricValue("series", tc.value, nil)

			_, ok := GetMetricSnapshot("series", nil)
			if ok != tc.recorded {
				t.Errorf("series exists = %v, want %v", ok, tc.recorded)
			}
			if (sink.count() == 1) != tc.recorded {
				t.Errorf("sink saw %d events, want recorded=%v", sink.count(), tc.recorded)
			}
		})
	}
}

// One bad observation must not poison the aggregate the good ones built.
func TestANonFiniteValueDoesNotPoisonAnExistingSeries(t *testing.T) {
	enableMetrics(t)

	RecordMetricValue("latency", 10, nil)
	RecordMetricValue("latency", math.NaN(), nil)
	RecordMetricValue("latency", math.Inf(1), nil)
	RecordMetricValue("latency", 30, nil)

	snapshot, ok := GetMetricSnapshot("latency", nil)
	if !ok {
		t.Fatal("series missing")
	}
	if snapshot.Count != 2 || snapshot.Sum != 40 || snapshot.Min != 10 || snapshot.Max != 30 || snapshot.LastValue != 30 {
		t.Errorf("snapshot = %+v, want count 2, sum 40, min 10, max 30, last 30", snapshot)
	}
}

// Two different tag sets must never share a series, whatever characters their
// keys and values hold.
func TestCanonicalTagsKeepsDistinctTagSetsDistinct(t *testing.T) {
	distinct := []struct {
		name string
		a, b map[string]string
	}{
		{"comma in a value against a second tag", map[string]string{"a": "1,b=2"}, map[string]string{"a": "1", "b": "2"}},
		{"equals in a key against the value", map[string]string{"a=b": "c"}, map[string]string{"a": "b=c"}},
		{"equals in a value against a second tag", map[string]string{"a": "b=c"}, map[string]string{"a": "b", "c": ""}},
		{"backslash before a comma", map[string]string{"a": `x\`, "b": "y"}, map[string]string{"a": `x\,b=y`}},
		{"escaped comma against a literal one", map[string]string{"a": `1\,b=2`}, map[string]string{"a": `1`, "b": "2"}},
		{"empty value against an absent key", map[string]string{"a": ""}, map[string]string{}},
		{"empty key against an empty value", map[string]string{"": "a"}, map[string]string{"a": ""}},
		{"key moved into the value", map[string]string{"a": "b"}, map[string]string{"": "a=b"}},
		{"three tags against two", map[string]string{"a": "1", "b": "2", "c": "3"}, map[string]string{"a": "1", "b": "2,c=3"}},
		{"trailing backslash", map[string]string{"a": `\`}, map[string]string{"a": `\\`}},
		{"comma in a value against a comma in a key", map[string]string{"a": "1,b", "c": "3"}, map[string]string{"a": "1", "b,c": "3"}},
		{"backslash in a key against an equals in a key", map[string]string{`\`: "="}, map[string]string{`=\`: ""}},
		{"newline in a value", map[string]string{"a": "1\nb=2"}, map[string]string{"a": "1", "b": "2"}},
	}
	for _, tc := range distinct {
		t.Run(tc.name, func(t *testing.T) {
			if canonicalTags(tc.a) == canonicalTags(tc.b) {
				t.Errorf("%v and %v share the canonical form %q", tc.a, tc.b, canonicalTags(tc.a))
			}
		})
	}

	same := []struct {
		name string
		a, b map[string]string
	}{
		{"insertion order does not matter", map[string]string{"a": "1", "b": "2"}, map[string]string{"b": "2", "a": "1"}},
		{"nil against empty", nil, map[string]string{}},
	}
	for _, tc := range same {
		t.Run(tc.name, func(t *testing.T) {
			if canonicalTags(tc.a) != canonicalTags(tc.b) {
				t.Errorf("%v and %v differ: %q against %q", tc.a, tc.b, canonicalTags(tc.a), canonicalTags(tc.b))
			}
		})
	}
}

func TestCanonicalTagsStableEncoding(t *testing.T) {
	cases := []struct {
		name string
		tags map[string]string
		want string
	}{
		{"nil", nil, ""},
		{"empty", map[string]string{}, ""},
		{"one", map[string]string{"a": "1"}, "a=1"},
		{"sorted", map[string]string{"b": "2", "a": "1"}, "a=1,b=2"},
		{"empty key", map[string]string{"": "x"}, "=x"},
		{"empty value", map[string]string{"a": ""}, "a="},
		{"comma in key", map[string]string{"a,b": "c"}, `a\,b=c`},
		{"equals in key", map[string]string{"a=b": "c"}, `a\=b=c`},
		{"comma in value", map[string]string{"a": "b,c"}, `a=b\,c`},
		{"equals in value", map[string]string{"a": "b=c"}, `a=b\=c`},
		{"backslashes", map[string]string{`a\b`: `c\d`}, `a\\b=c\\d`},
		{"empty pair", map[string]string{"": "", "x": "y"}, "=,x=y"},
		{"newline", map[string]string{"a": "b\nc"}, "a=b\nc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalTags(tc.tags); got != tc.want {
				t.Errorf("canonicalTags(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestCanonicalTagsAllocationBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags map[string]string
		max  float64
	}{
		{"four", map[string]string{"provider": "openai", "model": "gpt-5.4", "mode": "transform", "intelligence": "fast"}, 1},
		{"twelve", map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8", "i": "9", "j": "10", "k": "11", "l": "12"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(100, func() {
				benchmarkCanonicalTagsResult = canonicalTags(tc.tags)
			})
			if allocs > tc.max {
				t.Errorf("canonicalTags allocated %.0f times, want at most %.0f", allocs, tc.max)
			}
		})
	}
}

func TestTagSetsThatLookAlikeAreSeparateSeries(t *testing.T) {
	enableMetrics(t)

	RecordMetric("m", 1, map[string]string{"a": "1,b=2"})
	RecordMetric("m", 1, map[string]string{"a": "1", "b": "2"})

	if got := len(SnapshotMetrics()); got != 2 {
		t.Fatalf("%d series, want 2", got)
	}
	snapshot, ok := GetMetricSnapshot("m", map[string]string{"a": "1", "b": "2"})
	if !ok || snapshot.Count != 1 {
		t.Errorf("snapshot = %+v ok=%v, want one observation of its own", snapshot, ok)
	}
}

// A sink is the caller's function and runs on the goroutine that recorded the
// metric. A panic in one must not unwind into the operation, must not stop the
// other sinks, and must not put the metric's tags in the log.
func TestAPanickingMetricSinkIsContained(t *testing.T) {
	enableMetrics(t)
	logs := captureLogs(t)

	var order []string
	t.Cleanup(RegisterMetricSink(funcMetricSink(func(MetricEvent) { order = append(order, "first") })))
	t.Cleanup(RegisterMetricSink(funcMetricSink(func(MetricEvent) { panic("secret tag value acme-invoice-77") })))
	t.Cleanup(RegisterMetricSink(funcMetricSink(func(MetricEvent) { order = append(order, "third") })))

	RecordMetric("m", 1, map[string]string{"customer": "acme-invoice-77"})

	if len(order) != 2 || order[0] != "first" || order[1] != "third" {
		t.Errorf("sinks that ran = %v, want first and third around the panic", order)
	}
	if _, ok := GetMetricSnapshot("m", map[string]string{"customer": "acme-invoice-77"}); !ok {
		t.Error("the aggregate was not recorded")
	}
	text := logs()
	if !strings.Contains(text, "panicked") {
		t.Errorf("the panic was not reported in the log: %q", text)
	}
	if strings.Contains(text, "acme-invoice-77") {
		t.Errorf("the log carries the panic value or a tag value: %q", text)
	}
}

// A sink is called after the registry's lock is released, so it may read the
// registry it is called from. With the lock held across the call, the sink's
// read waits for the recorder that is calling it. The read is made on a
// goroutine of its own and waited for with a bound, so that a sink held up by
// the lock fails the test instead of hanging the package: once the recorder
// returns and unlocks, the read finishes.
func TestAMetricSinkMayReadTheRegistryItIsCalledFrom(t *testing.T) {
	enableMetrics(t)
	var held atomic.Bool
	t.Cleanup(RegisterMetricSink(funcMetricSink(func(MetricEvent) {
		read := make(chan struct{})
		go func() {
			GetMetricSnapshot("any", nil)
			SnapshotMetrics()
			close(read)
		}()
		select {
		case <-read:
		case <-time.After(5 * time.Second):
			held.Store(true)
		}
	})))

	RecordMetric("any", 1, nil)

	if held.Load() {
		t.Fatal("a sink that reads the registry waited on a lock the recorder still held")
	}
}

// A sink is handed the tags of the event as its own copy: it may edit them, and
// the map the caller passed to RecordMetric stays as it was.
func TestASinkEditingItsEventsTagsDoesNotChangeTheCallersMap(t *testing.T) {
	enableMetrics(t)
	t.Cleanup(RegisterMetricSink(funcMetricSink(func(event MetricEvent) {
		event.Tags["added-by-sink"] = "x"
		delete(event.Tags, "kept")
	})))

	tags := map[string]string{"kept": "1"}
	RecordMetric("m", 1, tags)

	if len(tags) != 1 || tags["kept"] != "1" {
		t.Fatalf("the caller's tags are now %v, want map[kept:1]", tags)
	}
}

// Sinks are called in the order they were registered, so an exporter that
// depends on another having seen the event first can say so by registering
// after it.
func TestMetricSinksRunInRegistrationOrder(t *testing.T) {
	enableMetrics(t)

	var order []int
	for i := 0; i < 12; i++ {
		i := i
		t.Cleanup(RegisterMetricSink(funcMetricSink(func(MetricEvent) { order = append(order, i) })))
	}

	RecordMetric("m", 1, nil)

	if len(order) != 12 {
		t.Fatalf("%d sinks ran, want 12", len(order))
	}
	for i, got := range order {
		if got != i {
			t.Fatalf("order = %v, want 0..11", order)
		}
	}
}

// Unregistering one sink leaves the others in place and in order, and a
// removal function called twice removes nothing more.
func TestUnregisteringASinkLeavesTheOthers(t *testing.T) {
	enableMetrics(t)

	var order []string
	removeA := RegisterMetricSink(funcMetricSink(func(MetricEvent) { order = append(order, "a") }))
	t.Cleanup(RegisterMetricSink(funcMetricSink(func(MetricEvent) { order = append(order, "b") })))
	t.Cleanup(RegisterMetricSink(funcMetricSink(func(MetricEvent) { order = append(order, "c") })))

	removeA()
	removeA()
	RecordMetric("m", 1, nil)

	if got := strings.Join(order, ""); got != "bc" {
		t.Errorf("sinks that ran = %q, want bc", got)
	}
}

// Registering and removing sinks in every combination leaves exactly the
// registered-and-not-removed ones, in registration order.
func TestRegisterAndRemoveMetricSinks(t *testing.T) {
	cases := []struct {
		name   string
		count  int
		remove []int
		want   string
	}{
		{"one sink", 1, nil, "0"},
		{"one sink removed", 1, []int{0}, ""},
		{"three sinks", 3, nil, "012"},
		{"first of three removed", 3, []int{0}, "12"},
		{"middle of three removed", 3, []int{1}, "02"},
		{"last of three removed", 3, []int{2}, "01"},
		{"first and last removed", 3, []int{0, 2}, "1"},
		{"all removed forwards", 3, []int{0, 1, 2}, ""},
		{"all removed backwards", 3, []int{2, 1, 0}, ""},
		{"the same removal twice", 4, []int{1, 1}, "023"},
		{"five with two removed", 5, []int{1, 3}, "024"},
		{"none registered", 0, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enableMetrics(t)

			var got strings.Builder
			var removers []func()
			for i := 0; i < tc.count; i++ {
				i := i
				remove := RegisterMetricSink(funcMetricSink(func(MetricEvent) { got.WriteString(string(rune('0' + i))) }))
				t.Cleanup(remove)
				removers = append(removers, remove)
			}
			for _, index := range tc.remove {
				removers[index]()
			}

			RecordMetric("m", 1, nil)

			if got.String() != tc.want {
				t.Errorf("sinks that ran = %q, want %q", got.String(), tc.want)
			}
		})
	}
}

// Whatever a sink panics with, delivery returns normally and logs neither the
// value nor the event's tags.
func TestDeliverToSinkContainsEveryKindOfPanic(t *testing.T) {
	type custom struct{ secret string }
	var nilMap map[string]int
	var nilPointer *testMetricSink

	cases := []struct {
		name string
		fire func()
	}{
		{"a string", func() { panic("secret-value") }},
		{"an error", func() { panic(errors.New("secret-value")) }},
		{"a formatted error", func() { panic(fmt.Errorf("wrapped: %w", errors.New("secret-value"))) }},
		{"a struct", func() { panic(custom{secret: "secret-value"}) }},
		{"a pointer", func() { panic(&custom{secret: "secret-value"}) }},
		{"an integer", func() { panic(42) }},
		{"a nil map write", func() { nilMap["secret-value"] = 1 }},
		{"a nil pointer dereference", func() { _ = nilPointer.events }},
		{"an index out of range", func() { _ = []int{}[len("secret-value")] }},
		{"a closed channel", func() { c := make(chan int); close(c); close(c) }},
		{"a nil panic value", func() { panic(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			event := MetricEvent{Name: "series", Tags: map[string]string{"customer": "secret-value"}}

			deliverToSink(funcMetricSink(func(MetricEvent) { tc.fire() }), event)

			text := logs()
			if !strings.Contains(text, "panicked") {
				t.Errorf("not reported: %q", text)
			}
			if strings.Contains(text, "secret-value") {
				t.Errorf("the log carries the panic value or a tag: %q", text)
			}
			if !strings.Contains(text, "series") {
				t.Errorf("the log does not name the metric: %q", text)
			}
		})
	}
}

// Recording while sinks come and go is safe: the sink list a recording walks is
// never edited under it.
func TestRecordingWhileSinksComeAndGo(t *testing.T) {
	enableMetrics(t)

	var delivered sync.Map
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				RecordMetric("busy", int64(i), map[string]string{"worker": "w"})
			}
		}()
	}
	for i := 0; i < 50; i++ {
		i := i
		remove := RegisterMetricSink(funcMetricSink(func(MetricEvent) { delivered.Store(i, true) }))
		remove()
	}
	wg.Wait()

	snapshot, ok := GetMetricSnapshot("busy", map[string]string{"worker": "w"})
	if !ok || snapshot.Count != 800 {
		t.Errorf("snapshot = %+v ok=%v, want 800 observations", snapshot, ok)
	}
}

// distinctSeriesTags returns a tag set that is different for each index, spread
// over three tags so that no one tag takes more values than the registry keeps
// for it (MetricTagValueLimit): the index in base 64, one digit for each tag.
func distinctSeriesTags(index int) map[string]string {
	return map[string]string{
		"d0": fmt.Sprint(index % MetricTagValueLimit),
		"d1": fmt.Sprint(index / MetricTagValueLimit % MetricTagValueLimit),
		"d2": fmt.Sprint(index / (MetricTagValueLimit * MetricTagValueLimit)),
	}
}

// The registry is bounded. A series that would take it past the bound is not
// created; the observation is counted in one overflow series instead, so the
// registry's memory does not grow with the number of distinct tag values, and
// a reader can see that series were turned away and how many observations.
func TestTheRegistryIsBoundedAndCountsWhatItTurnsAwayInOneSeries(t *testing.T) {
	enableMetrics(t)
	sink := &testMetricSink{}
	defer RegisterMetricSink(sink)()

	extra := 37
	for index := range MetricSeriesLimit + extra {
		RecordMetric("requests_by_model", 1, distinctSeriesTags(index))
	}

	snapshots := SnapshotMetrics()
	if len(snapshots) != MetricSeriesLimit+1 {
		t.Fatalf("the registry holds %d series, want the bound of %d and the overflow series", len(snapshots), MetricSeriesLimit)
	}
	overflow, ok := GetMetricSnapshot(OverflowMetricName, nil)
	if !ok {
		t.Fatal("no overflow series was recorded")
	}
	if overflow.Count != int64(extra) || overflow.Sum != float64(extra) || len(overflow.Tags) != 0 {
		t.Errorf("overflow series = count %d sum %v tags %v, want %d observations and no tags", overflow.Count, overflow.Sum, overflow.Tags, extra)
	}
	if _, ok := GetMetricSnapshot("requests_by_model", distinctSeriesTags(MetricSeriesLimit)); ok {
		t.Error("a series past the bound was created")
	}

	// A series that already exists is still updated at the bound, and so is the
	// overflow series, which never counts against it.
	RecordMetric("requests_by_model", 1, distinctSeriesTags(0))
	if existing, _ := GetMetricSnapshot("requests_by_model", distinctSeriesTags(0)); existing.Count != 2 {
		t.Errorf("an existing series was not updated at the bound: count %d", existing.Count)
	}
	RecordMetric("another_metric", 5, nil)
	if overflow, _ := GetMetricSnapshot(OverflowMetricName, nil); overflow.Count != int64(extra)+1 {
		t.Errorf("overflow count = %d after one more new series, want %d", overflow.Count, extra+1)
	}
	if len(SnapshotMetrics()) != MetricSeriesLimit+1 {
		t.Errorf("the registry grew past its bound to %d series", len(SnapshotMetrics()))
	}

	// A sink is an exporter with its own aggregation: it is still handed every
	// observation.
	if got := sink.count(); got != MetricSeriesLimit+extra+2 {
		t.Errorf("the sink received %d events, want every one of %d", got, MetricSeriesLimit+extra+2)
	}

	// Clearing the registry clears the overflow count with it.
	ResetMetrics()
	if _, ok := GetMetricSnapshot(OverflowMetricName, nil); ok || len(SnapshotMetrics()) != 0 {
		t.Error("ResetMetrics left the overflow series behind")
	}
}

// What the overflow series counts is observations turned away, not their
// values: values of different metrics do not add up to anything. Observations
// of any value, including ones that are not 1, add exactly 1 each.
func TestTheOverflowSeriesCountsObservationsWhateverTheirValues(t *testing.T) {
	values := []float64{7.5, 0, -3, 1e9, 0.0001, 42, 1, 2, 3, 250}
	enableMetrics(t)
	for index := range MetricSeriesLimit {
		RecordMetric("filler", 1, distinctSeriesTags(index))
	}
	for index, value := range values {
		RecordMetricValue("turned_away", value, map[string]string{"k": fmt.Sprintf("%d", index)})
	}

	overflow, ok := GetMetricSnapshot(OverflowMetricName, nil)
	if !ok {
		t.Fatal("no overflow series")
	}
	if want := float64(len(values)); overflow.Count != int64(len(values)) || overflow.Sum != want {
		t.Errorf("overflow = count %d sum %v, want %d observations summing to %v whatever their values", overflow.Count, overflow.Sum, len(values), want)
	}
	if overflow.Min != 1 || overflow.Max != 1 || overflow.LastValue != 1 {
		t.Errorf("overflow min %v max %v last %v, want 1 for each: every observation counts as one", overflow.Min, overflow.Max, overflow.LastValue)
	}
}

// Below the bound there is no overflow series at all.
func TestBelowItsBoundTheRegistryHasNoOverflowSeries(t *testing.T) {
	for _, series := range []int{0, 1, 2, 10, 100, 500, MetricSeriesLimit - 1, MetricSeriesLimit} {
		t.Run(fmt.Sprintf("%d series", series), func(t *testing.T) {
			enableMetrics(t)
			for index := range series {
				RecordMetric("m", 1, distinctSeriesTags(index))
				RecordMetric("m", 1, distinctSeriesTags(index))
			}
			if _, ok := GetMetricSnapshot(OverflowMetricName, nil); ok {
				t.Error("an overflow series exists with the registry within its bound")
			}
			if got := len(SnapshotMetrics()); got != series {
				t.Errorf("%d series, want %d", got, series)
			}
		})
	}
}

// A metric recorded under the overflow series' own name does not become a
// second series beside it, and cannot be used to reset its count.
func TestTheOverflowNameIsNotAnOrdinarySeries(t *testing.T) {
	enableMetrics(t)
	RecordMetric(OverflowMetricName, 99, map[string]string{"forged": "yes"})
	for _, snapshot := range SnapshotMetrics() {
		if snapshot.Name == OverflowMetricName {
			t.Errorf("a caller recorded a series under the overflow name: %+v", snapshot)
		}
	}
}

// With neither a sink nor the process's own switch, the metric calls of the call
// path record nothing and allocate nothing, which is what lets the library ask
// on every request.
func TestMetricCallsAllocateNothingWhenNoSinkAndNoSwitch(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	previous := config.IsMetricsEnabled()
	config.SetMetricsEnabled(false)
	ResetMetrics()
	t.Cleanup(func() { config.SetMetricsEnabled(previous); ResetMetrics() })
	if MetricsEnabled() || HasMetricSink() {
		t.Fatal("recording is on before the test began")
	}

	metadata := benchResultMetadata()
	calls := map[string]func(){
		"RecordLLMMetrics": func() { RecordLLMMetrics(metadata) },
		"RecordLLMFailure": func() { RecordLLMFailure("openai", "gpt-5.4", types.TransformMode, types.Fast, "timeout") },
		"RecordMetric":     func() { RecordMetric("extract_duration", 42, nil) },
		"RecordMetricValue": func() {
			RecordMetricValue("llm_cost_total_usd", 0.5, nil)
		},
	}
	for name, call := range calls {
		if allocs := testing.AllocsPerRun(100, call); allocs != 0 {
			t.Errorf("%s allocates %v times with recording off, want 0", name, allocs)
		}
	}
	if got := SnapshotMetrics(); len(got) != 0 {
		t.Errorf("%d series were recorded with recording off", len(got))
	}
}

func TestHasMetricSinkFollowsRegistrationsAndTheirRemovals(t *testing.T) {
	if HasMetricSink() {
		t.Fatal("a sink is registered before the test began")
	}
	first, second := RegisterMetricSink(&testMetricSink{}), RegisterMetricSink(&testMetricSink{})
	if !HasMetricSink() {
		t.Error("HasMetricSink is false with two registered")
	}
	first()
	first()
	if !HasMetricSink() {
		t.Error("removing one sink twice removed the other from the count")
	}
	second()
	second()
	if HasMetricSink() {
		t.Error("HasMetricSink is true with none registered")
	}
	if nilRemove := RegisterMetricSink(nil); HasMetricSink() {
		nilRemove()
		t.Error("a nil sink counted as registered")
	}
}

// A tag that takes a new value for every request, a model name a gateway makes
// up, used to fill the whole series budget first come first kept, after which no
// new series of any metric could be created, the library's own for a provider
// not yet seen among them. Each tag is bound on its own now, so the budget is
// left for everything else.
func TestOneTagWithAValueForEveryRequestCannotExhaustTheSeriesBudget(t *testing.T) {
	enableMetrics(t)
	sink := &testMetricSink{}
	defer RegisterMetricSink(sink)()

	const requests = 2300
	for index := range requests {
		metadata := benchResultMetadata()
		metadata.Model = fmt.Sprintf("gateway-snapshot-%d", index)
		RecordLLMMetrics(metadata)
	}

	if _, turnedAway := GetMetricSnapshot(OverflowMetricName, nil); turnedAway {
		t.Fatal("the series budget was reached by one tag")
	}
	models := map[string]bool{}
	for _, snapshot := range SnapshotMetrics() {
		if model, ok := snapshot.Tags["model"]; ok {
			models[model] = true
		}
	}
	if len(models) != MetricTagValueLimit+1 || !models[OverflowTagValue] {
		t.Errorf("the registry kept %d model values, want the %d it holds and %q", len(models), MetricTagValueLimit, OverflowTagValue)
	}
	for index := range MetricTagValueLimit {
		if !models[fmt.Sprintf("gateway-snapshot-%d", index)] {
			t.Fatalf("the model seen as number %d has no series of its own", index)
		}
	}
	other, ok := GetMetricSnapshot("llm_requests", map[string]string{
		"provider": "openai", "model": OverflowTagValue, "mode": "transform", "intelligence": "fast", "outcome": OutcomeOK})
	if !ok || other.Count != requests-MetricTagValueLimit {
		t.Errorf("the overflow value counts %d requests, want the %d past the limit (found %v)", other.Count, requests-MetricTagValueLimit, ok)
	}

	// What the budget was kept for: a provider and an outcome not seen before.
	RecordLLMFailure("a-provider-seen-for-the-first-time", "gpt-5.4", types.TransformMode, types.Fast, "timeout")
	if _, ok := GetMetricSnapshot("llm_requests", map[string]string{
		"provider": "a-provider-seen-for-the-first-time", "model": OverflowTagValue, "mode": "transform", "intelligence": "fast", "outcome": "timeout"}); !ok {
		t.Error("a new provider's series was not recorded: the budget was spent")
	}

	// A sink is handed each observation as it was, with the model's own name.
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var seen bool
	for _, event := range sink.events {
		if event.Tags["model"] == fmt.Sprintf("gateway-snapshot-%d", requests-1) {
			seen = true
		}
	}
	if !seen {
		t.Error("the sink was not handed the last model's own name")
	}
}

func TestEachTagIsBoundOnItsOwn(t *testing.T) {
	enableMetrics(t)
	// Fill the model tag.
	for index := range MetricTagValueLimit + 10 {
		RecordMetric("m", 1, map[string]string{"model": fmt.Sprint("model-", index), "provider": "p"})
	}
	// Another tag is not affected by it, and neither is a metric of another name.
	RecordMetric("m", 1, map[string]string{"model": "model-0", "provider": "another"})
	RecordMetric("other_metric", 1, map[string]string{"model": "model-0"})
	for _, tags := range []map[string]string{
		{"model": "model-0", "provider": "another"},
		{"model": OverflowTagValue, "provider": "p"},
	} {
		if _, ok := GetMetricSnapshot("m", tags); !ok {
			t.Errorf("no series for %v", tags)
		}
	}
	if _, ok := GetMetricSnapshot("other_metric", map[string]string{"model": "model-0"}); !ok {
		t.Error("a value the tag holds was not kept for a metric of another name")
	}
	// A value the tag did not hold before the limit is not kept after it.
	if _, ok := GetMetricSnapshot("m", map[string]string{"model": fmt.Sprint("model-", MetricTagValueLimit), "provider": "p"}); ok {
		t.Error("a value past the limit has a series of its own")
	}
}

func TestBoundTagValues(t *testing.T) {
	filled := func(key string, n int) {
		metricRegistry.mu.Lock()
		defer metricRegistry.mu.Unlock()
		for index := range n {
			boundTagValues(map[string]string{key: fmt.Sprint("v", index)})
		}
	}
	cases := []struct {
		name  string
		setup func()
		tags  map[string]string
		want  map[string]string
	}{
		{"no tags", func() {}, nil, nil},
		{"empty tags", func() {}, map[string]string{}, map[string]string{}},
		{"a first value", func() {}, map[string]string{"model": "a"}, map[string]string{"model": "a"}},
		{"values under the limit are kept", func() { filled("model", MetricTagValueLimit-2) }, map[string]string{"model": "new"}, map[string]string{"model": "new"}},
		{"the value that reaches the limit is kept", func() { filled("model", MetricTagValueLimit-1) }, map[string]string{"model": "last"}, map[string]string{"model": "last"}},
		{"a value past the limit is the overflow value", func() { filled("model", MetricTagValueLimit) }, map[string]string{"model": "late"}, map[string]string{"model": OverflowTagValue}},
		{"a value held is kept at the limit", func() { filled("model", MetricTagValueLimit) }, map[string]string{"model": "v3"}, map[string]string{"model": "v3"}},
		{"only the full tag is bound", func() { filled("model", MetricTagValueLimit) }, map[string]string{"model": "late", "mode": "strict"}, map[string]string{"model": OverflowTagValue, "mode": "strict"}},
		{"another key has its own budget", func() { filled("model", MetricTagValueLimit) }, map[string]string{"provider": "late"}, map[string]string{"provider": "late"}},
		{"two full tags are both bound", func() { filled("model", MetricTagValueLimit); filled("provider", MetricTagValueLimit) },
			map[string]string{"model": "late", "provider": "late"}, map[string]string{"model": OverflowTagValue, "provider": OverflowTagValue}},
		{"the overflow value is not a held value", func() { filled("model", MetricTagValueLimit) }, map[string]string{"model": OverflowTagValue}, map[string]string{"model": OverflowTagValue}},
		{"an empty value is a value", func() { filled("model", MetricTagValueLimit) }, map[string]string{"model": ""}, map[string]string{"model": OverflowTagValue}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetMetrics()
			t.Cleanup(ResetMetrics)
			tc.setup()
			before := fmt.Sprint(tc.tags)
			metricRegistry.mu.Lock()
			got := boundTagValues(tc.tags)
			metricRegistry.mu.Unlock()
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("boundTagValues(%v) = %v, want %v", tc.tags, got, tc.want)
			}
			if fmt.Sprint(tc.tags) != before {
				t.Errorf("the caller's tags were changed to %v", tc.tags)
			}
		})
	}
}

func TestResettingTheMetricsForgetsTheValuesATagHeld(t *testing.T) {
	enableMetrics(t)
	for index := range MetricTagValueLimit + 5 {
		RecordMetric("m", 1, map[string]string{"model": fmt.Sprint("model-", index)})
	}
	ResetMetrics()
	RecordMetric("m", 1, map[string]string{"model": "after-the-reset"})
	if _, ok := GetMetricSnapshot("m", map[string]string{"model": "after-the-reset"}); !ok {
		t.Error("a tag that had reached its limit before the reset still turns values away")
	}
}

func TestATagIsBoundWhenManyGoroutinesRecordAtOnce(t *testing.T) {
	enableMetrics(t)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range 200 {
				RecordMetric("m", 1, map[string]string{"model": fmt.Sprint("w", worker, "-", index)})
			}
		}()
	}
	wg.Wait()
	models := map[string]bool{}
	for _, snapshot := range SnapshotMetrics() {
		models[snapshot.Tags["model"]] = true
	}
	if len(models) != MetricTagValueLimit+1 {
		t.Errorf("%d model values were kept, want %d and the overflow value", len(models), MetricTagValueLimit)
	}
}

type tagWritingSink struct {
	mu   sync.Mutex
	seen map[string]string
}

func (s *tagWritingSink) RecordMetric(event MetricEvent) {
	s.mu.Lock()
	s.seen = event.Tags
	s.mu.Unlock()
	event.Tags["written-by-a-sink"] = "yes"
	delete(event.Tags, "model")
}

// Every sink is handed a map of its own. One that writes to the map, or keeps it
// for later, was seen by the sinks after it and raced with them.
func TestEachSinkIsHandedATagMapOfItsOwn(t *testing.T) {
	enableMetrics(t)
	writer := &tagWritingSink{}
	reader := &testMetricSink{}
	defer RegisterMetricSink(writer)()
	defer RegisterMetricSink(reader)()

	RecordMetric("m", 1, map[string]string{"model": "gpt-5.4", "provider": "openai"})

	if len(reader.events) != 1 {
		t.Fatalf("the second sink received %d events, want 1", len(reader.events))
	}
	got := reader.events[0].Tags
	if got["model"] != "gpt-5.4" || got["provider"] != "openai" || len(got) != 2 {
		t.Errorf("the second sink was handed %v, which the first sink wrote to", got)
	}
	if _, ok := GetMetricSnapshot("m", map[string]string{"model": "gpt-5.4", "provider": "openai"}); !ok {
		t.Error("the registry's series was keyed by tags a sink changed")
	}
	if len(writer.seen) == 0 {
		t.Error("the first sink was handed no tags")
	}
}

func TestSinksThatWriteToTheirTagsDoNotRaceWithEachOther(t *testing.T) {
	enableMetrics(t)
	for range 4 {
		defer RegisterMetricSink(&tagWritingSink{})()
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				RecordMetric("m", 1, map[string]string{"model": "gpt-5.4"})
			}
		}()
	}
	wg.Wait()
	if snapshot, ok := GetMetricSnapshot("m", map[string]string{"model": "gpt-5.4"}); !ok || snapshot.Count != 200 {
		t.Errorf("snapshot = %+v ok=%v, want 200 observations", snapshot, ok)
	}
}

package telemetry_test

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	wrapped "github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// What the library records goes through the package wrapped here: its call
// path calls wrapped.RecordMetric, RecordLLMMetrics and RecordLLMFailure. The
// tests record through them, as the call path does, and read what a Sink and
// Snapshots return.

// collector is a Sink that keeps the observations it is given.
type collector struct {
	mu      sync.Mutex
	name    string
	metrics []telemetry.Metric
	log     *orderLog
	panics  bool
}

func (c *collector) Record(m telemetry.Metric) {
	if c.log != nil {
		c.log.add(c.name)
	}
	if c.panics {
		panic("MARK_PANIC the caller's invoice text")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics = append(c.metrics, m)
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.metrics)
}

// cleanMetrics gives the test an empty registry and metrics off, and holds it to
// leaving both as it found them.
func cleanMetrics(t *testing.T) {
	t.Helper()
	if wrapped.MetricsEnabled() {
		t.Fatal("metrics are on before the test began; an earlier test did not restore")
	}
	wrapped.ResetMetrics()
	t.Cleanup(func() {
		wrapped.ResetMetrics()
		if wrapped.MetricsEnabled() {
			t.Error("the test left metrics on")
		}
	})
}

func TestASinkReceivesWhatTheLibraryRecords(t *testing.T) {
	cleanMetrics(t)
	sink := &collector{}
	defer telemetry.RegisterSink(sink)()

	before := time.Now().UTC().Add(-time.Second)
	wrapped.RecordMetric("extract_duration", 42, map[string]string{"type": "invoice", "mode": "strict"})
	wrapped.RecordMetricValue("llm_cost_total_usd", 0.0042, map[string]string{"model": "gpt-5"})

	if sink.count() != 2 {
		t.Fatalf("the sink received %d observations, want 2", sink.count())
	}
	first, second := sink.metrics[0], sink.metrics[1]
	if first.Name != "extract_duration" || first.Value != 42 || first.Tags["type"] != "invoice" || first.Tags["mode"] != "strict" {
		t.Errorf("first observation = %+v", first)
	}
	if second.Name != "llm_cost_total_usd" || second.Value != 0.0042 || second.Tags["model"] != "gpt-5" {
		t.Errorf("second observation = %+v", second)
	}
	if first.Timestamp.Before(before) || first.Timestamp.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("timestamp %v is not the time of the observation", first.Timestamp)
	}
}

func TestAnLLMRequestIsRecordedAsTheLibraryNamesItsMetrics(t *testing.T) {
	cleanMetrics(t)
	sink := &collector{}
	defer telemetry.RegisterSink(sink)()

	wrapped.RecordLLMMetrics(&types.ResultMetadata{
		Provider: "openai", Model: "gpt-5", Mode: types.Strict, Intelligence: types.Smart, Duration: 1500 * time.Millisecond,
		TokenUsage: &types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130, CachedTokens: 40},
		CostInfo:   &types.CostInfo{TotalCost: 0.5, PromptCost: 0.3, CompletionCost: 0.2, Priced: true},
	})
	wrapped.RecordLLMFailure("openai", "gpt-5", types.Strict, types.Smart, types.KindRateLimited.String())

	var names []string
	for _, m := range sink.metrics {
		names = append(names, m.Name)
	}
	for _, want := range []string{"llm_requests", "llm_request_duration_ms", "llm_tokens_prompt", "llm_tokens_completion", "llm_tokens_total", "llm_tokens_cached", "llm_cost_total_usd"} {
		if !slices.Contains(names, want) {
			t.Errorf("no observation named %s among %v", want, names)
		}
	}
	if slices.Contains(names, "llm_tokens_reasoning") {
		t.Error("a series for reasoning tokens was recorded with none reported")
	}
	var outcomes []string
	for _, m := range sink.metrics {
		if m.Name == "llm_requests" {
			outcomes = append(outcomes, m.Tags["outcome"])
		}
	}
	if !slices.Equal(outcomes, []string{"ok", "rate limited"}) {
		t.Errorf("llm_requests outcomes = %v, want ok and the failure's kind", outcomes)
	}
}

func TestMetricsAreRecordedOnlyWhileASinkIsRegistered(t *testing.T) {
	cleanMetrics(t)

	wrapped.RecordMetric("before", 1, nil)
	if got := telemetry.Snapshots(); len(got) != 0 {
		t.Fatalf("%d series were aggregated with no sink registered, want none", len(got))
	}

	first, second := &collector{}, &collector{}
	restoreFirst := telemetry.RegisterSink(first)
	restoreSecond := telemetry.RegisterSink(second)
	wrapped.RecordMetric("both", 1, nil)

	restoreFirst()
	wrapped.RecordMetric("one", 1, nil)
	if first.count() != 1 || second.count() != 2 {
		t.Errorf("the first sink saw %d observations and the second %d, want 1 and 2", first.count(), second.count())
	}

	restoreSecond()
	wrapped.RecordMetric("after", 1, nil)
	if second.count() != 2 {
		t.Errorf("a sink saw an observation after its restore: %d", second.count())
	}

	var names []string
	for _, s := range telemetry.Snapshots() {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"both", "one"}) {
		t.Errorf("series = %v, want those recorded while a sink was registered: the last restore turns recording off", names)
	}
}

func TestRestoringTheLastSinkLeavesMetricsAsTheHostHadThem(t *testing.T) {
	cases := []struct {
		name       string
		hostEnable bool
	}{
		{"metrics off before", false},
		{"metrics already on", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped.ResetMetrics()
			config.SetMetricsEnabled(tc.hostEnable)
			defer func() {
				config.SetMetricsEnabled(false)
				wrapped.ResetMetrics()
			}()

			restore := telemetry.RegisterSink(&collector{})
			defer restore()
			if !wrapped.MetricsEnabled() {
				t.Fatal("registering a sink did not turn recording on")
			}
			if got := config.IsMetricsEnabled(); got != tc.hostEnable {
				t.Errorf("registering a sink changed the process's own switch to %v, want it left at %v", got, tc.hostEnable)
			}
			restore()
			restore()
			if got := wrapped.MetricsEnabled(); got != tc.hostEnable {
				t.Errorf("recording is on = %v after the last restore, want %v as before", got, tc.hostEnable)
			}
		})
	}
}

func TestSinksAreCalledInTheOrderTheyWereRegistered(t *testing.T) {
	cleanMetrics(t)
	log := &orderLog{}
	for _, name := range []string{"a", "b", "c"} {
		defer telemetry.RegisterSink(&collector{name: name, log: log})()
	}
	wrapped.RecordMetric("m", 1, nil)
	wrapped.RecordMetric("m", 2, nil)
	if got := log.all(); !slices.Equal(got, []string{"a", "b", "c", "a", "b", "c"}) {
		t.Errorf("order = %v", got)
	}
}

func TestAPanickingSinkReachesNeitherTheCallerNorAnotherSink(t *testing.T) {
	cleanMetrics(t)
	before, after := &collector{}, &collector{}
	defer telemetry.RegisterSink(before)()
	defer telemetry.RegisterSink(&collector{panics: true})()
	defer telemetry.RegisterSink(after)()

	wrapped.RecordMetric("m", 1, nil)
	wrapped.RecordMetric("m", 2, nil)
	if before.count() != 2 || after.count() != 2 {
		t.Errorf("sinks around the one that panicked saw %d and %d observations, want 2 and 2", before.count(), after.count())
	}
}

func TestRegisteringANilSinkRegistersNothing(t *testing.T) {
	cleanMetrics(t)
	restore := telemetry.RegisterSink(nil)
	if restore == nil {
		t.Fatal("RegisterSink(nil) returned a nil remove")
	}
	restore()
	wrapped.RecordMetric("m", 1, nil)
	if len(telemetry.Snapshots()) != 0 {
		t.Error("a nil sink turned metrics on")
	}
}

func TestRestoresOfSinksRunInAnyOrderAndAnyNumberOfTimes(t *testing.T) {
	orders := [][]int{{0, 1, 2}, {2, 1, 0}, {1, 0, 2}, {0, 0, 1, 2}, {2, 2, 2, 1, 0, 1}}
	for _, order := range orders {
		t.Run(fmtOrder(order), func(t *testing.T) {
			cleanMetrics(t)
			sinks := []*collector{{}, {}, {}}
			var restores []func()
			for _, s := range sinks {
				restores = append(restores, telemetry.RegisterSink(s))
			}
			for _, index := range order {
				restores[index]()
				wrapped.RecordMetric("m", 1, nil)
			}
			for position, s := range sinks {
				if want := slices.Index(order, position); s.count() != want {
					t.Errorf("sink %d saw %d observations, want %d", position, s.count(), want)
				}
			}
			if config.IsMetricsEnabled() {
				t.Error("metrics are still on after every sink was restored")
			}
		})
	}
}

func TestSnapshotsAggregateEachSeriesAndAreSortedByNameAndTags(t *testing.T) {
	cleanMetrics(t)
	defer telemetry.RegisterSink(&collector{})()

	for _, v := range []float64{4, 1, 9, 2} {
		wrapped.RecordMetricValue("latency", v, map[string]string{"model": "b"})
	}
	wrapped.RecordMetricValue("latency", 100, map[string]string{"model": "a"})
	wrapped.RecordMetricValue("alpha", 7, nil)

	got := telemetry.Snapshots()
	if len(got) != 3 {
		t.Fatalf("%d series, want 3: %+v", len(got), got)
	}
	var order []string
	for _, s := range got {
		order = append(order, s.Name+" "+s.Tags["model"])
	}
	if !slices.Equal(order, []string{"alpha ", "latency a", "latency b"}) {
		t.Errorf("order = %v, want by name and then by tags", order)
	}
	b := got[2]
	if b.Count != 4 || b.Sum != 16 || b.Min != 1 || b.Max != 9 || b.LastValue != 2 || b.Average() != 4 {
		t.Errorf("latency/b = %+v average %v, want count 4 sum 16 min 1 max 9 last 2 average 4", b, b.Average())
	}
	if b.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
	if (telemetry.Snapshot{}).Average() != 0 {
		t.Error("the average of no observations is not 0")
	}

	// A snapshot is a copy: writing to it changes nothing the registry holds.
	got[2].Tags["model"] = "changed"
	got[2].Count = 99
	again := telemetry.Snapshots()
	if again[2].Tags["model"] != "b" || again[2].Count != 4 {
		t.Error("changing a returned snapshot changed the registry")
	}
}

func TestNonFiniteValuesAreNotRecordedOrDelivered(t *testing.T) {
	cleanMetrics(t)
	sink := &collector{}
	defer telemetry.RegisterSink(sink)()

	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		wrapped.RecordMetricValue("m", v, nil)
	}
	wrapped.RecordMetricValue("   ", 1, nil)
	if sink.count() != 0 || len(telemetry.Snapshots()) != 0 {
		t.Errorf("%d observations delivered and %d series kept for values that carry no meaning", sink.count(), len(telemetry.Snapshots()))
	}
}

func TestTheTagsOfAnObservationAreBoundedAndPrintable(t *testing.T) {
	cleanMetrics(t)
	sink := &collector{}
	defer telemetry.RegisterSink(sink)()

	hostile := "m\n" + strings.Repeat("x", 5000) + "\x00" + "é"
	wrapped.RecordLLMMetrics(&types.ResultMetadata{Provider: hostile, Model: hostile, Mode: types.Strict, Intelligence: types.Fast, Duration: time.Second})
	wrapped.RecordLLMFailure(hostile, hostile, types.Strict, types.Fast, hostile)

	if sink.count() == 0 {
		t.Fatal("nothing was recorded")
	}
	for _, m := range sink.metrics {
		for key, value := range m.Tags {
			if len(value) > 64 {
				t.Errorf("%s tag %s is %d bytes, over 64", m.Name, key, len(value))
			}
			for index := 0; index < len(value); index++ {
				if value[index] < 0x20 || value[index] > 0x7e {
					t.Errorf("%s tag %s holds the unprintable byte %#x", m.Name, key, value[index])
					break
				}
			}
		}
	}
}

// The registry keeps one series for each distinct name and tag set, for the life
// of the process. A tag that takes its values from outside the program would
// grow it without limit, so past a limit new series are counted in one series
// of their own and not kept, while a sink still receives every observation.
func TestTheRegistryHoldsAtMostTheSeriesLimitAndCountsWhatItTurnedAway(t *testing.T) {
	cleanMetrics(t)
	sink := &collector{}
	defer telemetry.RegisterSink(sink)()

	const limit, extra = 2048, 25
	for i := range limit + extra {
		// Spread over three tags: no one tag keeps more than wrapped.MetricTagValueLimit values.
		wrapped.RecordMetric("per_model", 1, map[string]string{
			"d0": strconv.Itoa(i % wrapped.MetricTagValueLimit),
			"d1": strconv.Itoa(i / wrapped.MetricTagValueLimit % wrapped.MetricTagValueLimit),
			"d2": strconv.Itoa(i / (wrapped.MetricTagValueLimit * wrapped.MetricTagValueLimit)),
		})
	}

	if sink.count() != limit+extra {
		t.Errorf("the sink received %d observations, want all %d", sink.count(), limit+extra)
	}
	snapshots := telemetry.Snapshots()
	if len(snapshots) != limit+1 {
		t.Fatalf("%d series are kept, want the limit of %d and the overflow series", len(snapshots), limit)
	}
	var overflow *telemetry.Snapshot
	for i := range snapshots {
		if snapshots[i].Name == "schemaflux_metric_series_overflow" {
			overflow = &snapshots[i]
		}
	}
	if overflow == nil || overflow.Count != extra || overflow.Sum != extra || len(overflow.Tags) != 0 {
		t.Errorf("overflow series = %+v, want a count and a sum of %d and no tags", overflow, extra)
	}
	if wrapped.MetricSeriesLimit != limit || wrapped.OverflowMetricName != "schemaflux_metric_series_overflow" {
		t.Errorf("the registry's limit is %d and its overflow series %q; the documentation of Snapshots says 2048 and schemaflux_metric_series_overflow",
			wrapped.MetricSeriesLimit, wrapped.OverflowMetricName)
	}
	// A series that exists keeps counting past the limit.
	wrapped.RecordMetric("per_model", 1, map[string]string{"model": "m0"})
	for _, s := range telemetry.Snapshots() {
		if s.Name == "per_model" && s.Tags["model"] == "m0" && s.Count != 2 {
			t.Errorf("an existing series counted %d, want 2", s.Count)
		}
	}
}

func TestEveryObservationOfFiftyGoroutinesReachesEverySinkOnce(t *testing.T) {
	cleanMetrics(t)
	first, second := &collector{}, &collector{}
	defer telemetry.RegisterSink(first)()
	defer telemetry.RegisterSink(second)()

	var wg sync.WaitGroup
	for worker := range 50 {
		wg.Go(func() {
			for range 100 {
				wrapped.RecordMetric("m", 1, map[string]string{"worker": strconv.Itoa(worker % 5)})
			}
		})
	}
	wg.Wait()
	if first.count() != 5000 || second.count() != 5000 {
		t.Errorf("the sinks received %d and %d observations, want 5000 each", first.count(), second.count())
	}
	var total int64
	for _, s := range telemetry.Snapshots() {
		total += s.Count
	}
	if total != 5000 {
		t.Errorf("the registry counted %d observations, want 5000", total)
	}
}

// A tag that takes a new value for every request cannot fill the registry for
// everything else: the documentation says each tag keeps 64 values and records
// the rest as "other", and a sink is still given the value as it was.
func TestOneTagWithAValueForEveryRequestLeavesRoomForTheRest(t *testing.T) {
	cleanMetrics(t)
	if wrapped.MetricTagValueLimit != 64 || wrapped.OverflowTagValue != "other" {
		t.Fatalf("the limit is %d and the overflow value %q; the documentation says 64 and \"other\"", wrapped.MetricTagValueLimit, wrapped.OverflowTagValue)
	}
	sink := &collector{}
	defer telemetry.RegisterSink(sink)()

	for i := range 1000 {
		wrapped.RecordMetric("per_model", 1, map[string]string{"model": "snapshot-" + strconv.Itoa(i)})
	}
	wrapped.RecordMetric("a_metric_seen_for_the_first_time", 1, nil)

	models := map[string]bool{}
	var names []string
	for _, snapshot := range telemetry.Snapshots() {
		names = append(names, snapshot.Name)
		if model, ok := snapshot.Tags["model"]; ok {
			models[model] = true
		}
	}
	if len(models) != 65 || !models["other"] {
		t.Errorf("%d model values are kept, want 64 and \"other\"", len(models))
	}
	if !slices.Contains(names, "a_metric_seen_for_the_first_time") || slices.Contains(names, "schemaflux_metric_series_overflow") {
		t.Errorf("series = %v, want the new metric kept and no overflow series", names)
	}
	if sink.count() != 1001 {
		t.Errorf("the sink was handed %d observations, want all 1001", sink.count())
	}
}

// Registering a sink through this package turns recording on without the
// process's own switch, and each sink is handed a map of its own.
func TestASinkRegisteredHereReceivesWithTheProcessSwitchOffAndOwnsItsTags(t *testing.T) {
	cleanMetrics(t)
	first, second := &collector{}, &collector{}
	defer telemetry.RegisterSink(first)()
	defer telemetry.RegisterSink(second)()

	wrapped.RecordMetric("m", 1, map[string]string{"k": "v"})
	first.mu.Lock()
	first.metrics[0].Tags["k"] = "changed by the first sink"
	first.mu.Unlock()
	if got := second.metrics[0].Tags["k"]; got != "v" {
		t.Errorf("the second sink was handed %q, which the first changed", got)
	}
}

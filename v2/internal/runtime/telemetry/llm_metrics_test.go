package telemetry

import (
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// RecordLLMMetrics tags every series with provider/model/mode/intelligence --
// a bounded, declared vocabulary, per RecordBatchMetrics's own §15.1 rule
// (opsmetrics.go). These tests exercise it directly since nothing else in
// this package's test suite calls it.

// A nil metadata pointer must be a safe no-op: RecordLLMMetrics is called
// from a completion path that may not always have metadata to report.
func TestRecordLLMMetricsNilMetadataIsSafe(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordLLMMetrics(nil) // must not panic

	if snapshots := SnapshotMetrics(); len(snapshots) != 0 {
		t.Fatalf("expected nil metadata to record nothing, got %d snapshots", len(snapshots))
	}
}

// The full path: requests, duration, every non-zero token bucket, and every
// cost bucket, each carrying the same fixed provider/model/mode/intelligence
// tag set.
func TestRecordLLMMetricsRecordsRequestsTokensAndCost(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	metadata := &types.ResultMetadata{
		Provider:     "openai",
		Model:        "gpt-5.6-luna",
		Mode:         types.Strict,
		Intelligence: types.Smart,
		Duration:     250 * time.Millisecond,
		TokenUsage: &types.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			InputTokens:      90,
			OutputTokens:     45,
			CachedTokens:     10,
			ReasoningTokens:  5,
		},
		CostInfo: &types.CostInfo{
			Priced:         true,
			TotalCost:      0.03,
			PromptCost:     0.02,
			CompletionCost: 0.01,
			CachedCost:     0.001,
			ReasoningCost:  0.002,
		},
	}

	RecordLLMMetrics(metadata)

	tags := map[string]string{
		"provider": "openai", "model": "gpt-5.6-luna", "mode": "strict", "intelligence": "smart",
	}

	requests, ok := GetMetricSnapshot("llm_requests", map[string]string{
		"provider": "openai", "model": "gpt-5.6-luna", "mode": "strict", "intelligence": "smart", "outcome": OutcomeOK,
	})
	if !ok || requests.Count != 1 {
		t.Errorf("llm_requests with the outcome ok counted %d (found %v), want 1", requests.Count, ok)
	}

	wantCounters := map[string]float64{
		"llm_request_duration_ms": 250,
		"llm_tokens_prompt":       100,
		"llm_tokens_completion":   50,
		"llm_tokens_total":        150,
		"llm_tokens_cached":       10,
		"llm_tokens_reasoning":    5,
		"llm_cost_total_usd":      0.03,
		"llm_cost_prompt_usd":     0.02,
		"llm_cost_completion_usd": 0.01,
		"llm_cost_cached_usd":     0.001,
		"llm_cost_reasoning_usd":  0.002,
	}
	for name, want := range wantCounters {
		snapshot, ok := GetMetricSnapshot(name, tags)
		if !ok {
			t.Errorf("metric %s was not recorded", name)
			continue
		}
		if snapshot.LastValue != want {
			t.Errorf("metric %s LastValue = %v, want %v", name, snapshot.LastValue, want)
		}
	}
}

// Zero-valued optional buckets (no cache/reasoning tokens or cost) must not
// emit a series at all -- RecordLLMMetrics only records what actually
// happened, not a full fixed schema padded with zeros.
func TestRecordLLMMetricsOmitsZeroOptionalBuckets(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	metadata := &types.ResultMetadata{
		Provider:     "openai",
		Model:        "gpt-5-mini",
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
		TokenUsage: &types.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
			// InputTokens/OutputTokens/CachedTokens/ReasoningTokens left zero.
		},
		// CostInfo left nil entirely.
	}

	RecordLLMMetrics(metadata)

	forbidden := []string{
		"llm_tokens_input", "llm_tokens_output", "llm_tokens_cached", "llm_tokens_reasoning",
		"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd",
		"llm_cost_cached_usd", "llm_cost_reasoning_usd",
	}
	recorded := map[string]bool{}
	for _, s := range SnapshotMetrics() {
		recorded[s.Name] = true
	}
	for _, name := range forbidden {
		if recorded[name] {
			t.Errorf("metric %s should not be emitted when its source value is zero/absent", name)
		}
	}
}

// The privacy bar this task names explicitly, applied here: RecordLLMMetrics
// builds its tag map from exactly provider/model/mode/intelligence -- a
// fixed, small, declared vocabulary (opsmetrics.go's §15.1 rule). A caller's
// payload never has a route into a tag key at all, because nothing in
// ResultMetadata's request/response body fields (there are none on this
// type) is ever read here. This walks the emitted tag set and asserts it is
// exactly that closed set, on every series RecordLLMMetrics produced,
// proving the vocabulary stayed closed rather than just reading the source
// once and trusting it never grows a field.
func TestRecordLLMMetricsTagVocabularyIsClosed(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	// A marker planted in the one string field that becomes a tag value at
	// all (Model) still must not smuggle in a NEW tag *key* -- the closed
	// vocabulary is a property of the key set, independent of what any one
	// value contains.
	const marker = "MARKER-should-stay-a-value-never-a-key-9f2c"

	metadata := &types.ResultMetadata{
		Provider:     "openai",
		Model:        marker,
		Mode:         types.Strict,
		Intelligence: types.Smart,
		TokenUsage:   &types.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		CostInfo:     &types.CostInfo{Priced: true, TotalCost: 1, PromptCost: 1, CompletionCost: 0},
	}
	RecordLLMMetrics(metadata)

	allowedKeys := map[string]bool{"provider": true, "model": true, "mode": true, "intelligence": true}
	// The request counter alone says how the request ended.
	requestKeys := map[string]bool{"provider": true, "model": true, "mode": true, "intelligence": true, "outcome": true}
	snapshots := SnapshotMetrics()
	if len(snapshots) == 0 {
		t.Fatal("expected RecordLLMMetrics to emit at least one series")
	}
	for _, snapshot := range snapshots {
		if strings.Contains(snapshot.Name, marker) {
			t.Fatalf("metric name %q contains the payload marker", snapshot.Name)
		}
		allowed := allowedKeys
		if snapshot.Name == "llm_requests" {
			allowed = requestKeys
		}
		for key := range snapshot.Tags {
			if !allowed[key] {
				t.Fatalf("metric %s carries an undeclared tag key %q outside {provider,model,mode,intelligence} and, on llm_requests, outcome", snapshot.Name, key)
			}
		}
	}
}

// A call nobody could price reports a zero cost because nothing is known, not
// because it was free. Recording that zero would pull llm_cost_total_usd's
// average below any real request, so an unpriced call emits no cost series at
// all, the way RecordBatchMetrics and the span adapter treat it.
func TestRecordLLMMetricsCostSeriesFollowThePricedFlag(t *testing.T) {
	allCost := []string{
		"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd",
		"llm_cost_cached_usd", "llm_cost_reasoning_usd",
	}
	cases := []struct {
		name string
		cost *types.CostInfo
		want []string
	}{
		{"no cost info", nil, nil},
		{"unpriced and empty", &types.CostInfo{}, nil},
		{"unpriced with a stray total", &types.CostInfo{TotalCost: 0.5}, nil},
		{"unpriced with every field set", &types.CostInfo{TotalCost: 1, PromptCost: 1, CompletionCost: 1, CachedCost: 1, ReasoningCost: 1}, nil},
		{"unpriced with a source named", &types.CostInfo{PricingSource: "gpt-5.6-luna"}, nil},
		{"unpriced with currency only", &types.CostInfo{Currency: "USD"}, nil},
		{"priced and free", &types.CostInfo{Priced: true}, []string{"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd"}},
		{"priced total only", &types.CostInfo{Priced: true, TotalCost: 0.2}, []string{"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd"}},
		{"priced with cache cost", &types.CostInfo{Priced: true, TotalCost: 0.2, CachedCost: 0.01}, []string{"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd", "llm_cost_cached_usd"}},
		{"priced with reasoning cost", &types.CostInfo{Priced: true, TotalCost: 0.2, ReasoningCost: 0.01}, []string{"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd", "llm_cost_reasoning_usd"}},
		{"priced with every field", &types.CostInfo{Priced: true, TotalCost: 1, PromptCost: 1, CompletionCost: 1, CachedCost: 1, ReasoningCost: 1}, allCost},
		{"priced with negative cache cost", &types.CostInfo{Priced: true, TotalCost: 1, CachedCost: -1}, []string{"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enableMetrics(t)

			RecordLLMMetrics(&types.ResultMetadata{Provider: "openai", Model: "m", CostInfo: tc.cost})

			got := map[string]bool{}
			for _, snapshot := range SnapshotMetrics() {
				got[snapshot.Name] = true
			}
			want := map[string]bool{}
			for _, name := range tc.want {
				want[name] = true
			}
			for _, name := range allCost {
				if got[name] != want[name] {
					t.Errorf("%s recorded = %v, want %v", name, got[name], want[name])
				}
			}
			if !got["llm_requests"] {
				t.Error("the request itself must be counted whether or not it was priced")
			}
		})
	}
}

// An unpriced call must not move the average of a series other calls feed.
func TestAnUnpricedCallLeavesTheCostAverageAlone(t *testing.T) {
	enableMetrics(t)
	tags := map[string]string{"provider": "openai", "model": "gpt-5.6-luna", "mode": "strict", "intelligence": "smart"}
	base := types.ResultMetadata{Provider: "openai", Model: "gpt-5.6-luna", Mode: types.Strict, Intelligence: types.Smart}

	priced := base
	priced.CostInfo = &types.CostInfo{Priced: true, TotalCost: 0.4}
	unpriced := base
	unpriced.CostInfo = &types.CostInfo{}

	RecordLLMMetrics(&priced)
	RecordLLMMetrics(&unpriced)
	RecordLLMMetrics(&priced)

	snapshot, ok := GetMetricSnapshot("llm_cost_total_usd", tags)
	if !ok {
		t.Fatal("llm_cost_total_usd was not recorded for the priced calls")
	}
	if snapshot.Count != 2 || snapshot.Average() != 0.4 {
		t.Errorf("count=%d average=%v, want 2 observations averaging 0.4", snapshot.Count, snapshot.Average())
	}
}

// Token series follow the same rule as the cost series: a bucket is recorded
// when the provider reported it, and the three that every response carries are
// recorded even at zero.
func TestRecordLLMMetricsTokenSeries(t *testing.T) {
	always := []string{"llm_tokens_prompt", "llm_tokens_completion", "llm_tokens_total"}
	optional := []string{"llm_tokens_cached", "llm_tokens_reasoning"}
	// The input and output counts are the prompt and completion counts under
	// another name and have no series of their own.
	never := []string{"llm_tokens_input", "llm_tokens_output"}
	cases := []struct {
		name  string
		usage *types.TokenUsage
		want  []string
	}{
		{"no usage", nil, nil},
		{"zero usage", &types.TokenUsage{}, always},
		{"input only", &types.TokenUsage{InputTokens: 1}, nil},
		{"output only", &types.TokenUsage{OutputTokens: 1}, nil},
		{"cached only", &types.TokenUsage{CachedTokens: 1}, []string{"llm_tokens_cached"}},
		{"reasoning only", &types.TokenUsage{ReasoningTokens: 1}, []string{"llm_tokens_reasoning"}},
		{"input and output", &types.TokenUsage{InputTokens: 1, OutputTokens: 1}, nil},
		{"cached and reasoning", &types.TokenUsage{CachedTokens: 2, ReasoningTokens: 3}, []string{"llm_tokens_cached", "llm_tokens_reasoning"}},
		{"negative input is not reported", &types.TokenUsage{InputTokens: -1}, nil},
		{"negative cached is not reported", &types.TokenUsage{CachedTokens: -4}, nil},
		{"every bucket", &types.TokenUsage{InputTokens: 1, OutputTokens: 1, CachedTokens: 1, ReasoningTokens: 1}, optional},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enableMetrics(t)

			RecordLLMMetrics(&types.ResultMetadata{Provider: "p", Model: "m", TokenUsage: tc.usage})

			got := map[string]bool{}
			for _, snapshot := range SnapshotMetrics() {
				got[snapshot.Name] = true
			}
			want := map[string]bool{}
			if tc.usage != nil {
				for _, name := range always {
					want[name] = true
				}
			}
			for _, name := range tc.want {
				want[name] = true
			}
			for _, name := range append(append([]string{}, always...), optional...) {
				if got[name] != want[name] {
					t.Errorf("%s recorded = %v, want %v", name, got[name], want[name])
				}
			}
			for _, name := range never {
				if got[name] {
					t.Errorf("%s was recorded", name)
				}
			}
		})
	}
}

// With metrics off, which is the default, a recorded call allocates nothing. It
// allocated sixteen times: every one of the eight series it tried to record read
// the environment to find out that metrics were off. What this does not show is
// whether RecordLLMMetrics returns before it builds its tags: the tag map stays on
// the stack and RecordMetric returns at once, so without the early return the
// count is still zero and only the time differs.
func TestRecordLLMMetricsWithMetricsOffAllocatesNothing(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(false)

	priced := &types.CostInfo{TotalCost: 0.0042, PromptCost: 0.003, CompletionCost: 0.0012, CachedCost: 0.0001, ReasoningCost: 0.0002, Priced: true}
	cases := map[string]*types.ResultMetadata{
		"nil":                       nil,
		"empty":                     {},
		"no usage":                  {Provider: "openai", Model: "gpt-5.6-luna"},
		"usage only":                {Provider: "openai", Model: "gpt-5.6-luna", TokenUsage: &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
		"every token bucket":        {Provider: "openai", Model: "gpt-5.6-luna", TokenUsage: &types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, InputTokens: 90, OutputTokens: 45, CachedTokens: 10, ReasoningTokens: 5}},
		"priced":                    {Provider: "openai", Model: "gpt-5.6-luna", CostInfo: priced},
		"unpriced":                  {Provider: "openai", Model: "gpt-5.6-luna", CostInfo: &types.CostInfo{Priced: false}},
		"priced with usage":         {Provider: "openai", Model: "gpt-5.6-luna", Duration: time.Second, TokenUsage: &types.TokenUsage{PromptTokens: 10, TotalTokens: 10}, CostInfo: priced},
		"strict smart":              {Provider: "anthropic", Model: "m", Mode: types.Strict, Intelligence: types.Smart, Duration: time.Millisecond},
		"long names":                {Provider: strings.Repeat("p", 200), Model: strings.Repeat("m", 200)},
		"a cost with a zero amount": {Provider: "openai", Model: "m", CostInfo: &types.CostInfo{Priced: true}},
	}
	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(100, func() { RecordLLMMetrics(metadata) }); got != 0 {
				t.Errorf("RecordLLMMetrics with metrics off allocates %v times, want 0", got)
			}
			if snapshots := SnapshotMetrics(); len(snapshots) != 0 {
				t.Errorf("metrics off recorded %d series", len(snapshots))
			}
		})
	}

	// The count can see the cost it is guarding against: the same call with
	// metrics on builds the tags.
	config.SetMetricsEnabled(true)
	if got := testing.AllocsPerRun(20, func() { RecordLLMMetrics(cases["priced with usage"]) }); got == 0 {
		t.Error("RecordLLMMetrics with metrics on allocates nothing, so the count above could not show a missing early return")
	}
}

// A model's name arrives in a provider's response, so it is whatever the
// provider sent. As a tag it is cut and made printable.
func TestBoundedTagValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"an ordinary model name", "gpt-5.6-luna-2026-08-01", "gpt-5.6-luna-2026-08-01"},
		{"empty", "", ""},
		{"exactly the bound", strings.Repeat("m", maxTagValueLength), strings.Repeat("m", maxTagValueLength)},
		{"one over the bound", strings.Repeat("m", maxTagValueLength+1), strings.Repeat("m", maxTagValueLength)},
		{"ten thousand bytes", strings.Repeat("m", 10000), strings.Repeat("m", maxTagValueLength)},
		{"a newline", "model\ninjected=1", "model?injected=1"},
		{"a NUL", "model\x00", "model?"},
		{"an escape sequence", "\x1b[31mmodel", "?[31mmodel"},
		{"non-ASCII text", "mod\xc3\xa8le", "mod??le"},
		{"invalid UTF-8", "model\xff", "model?"},
		{"a path with a slash and a colon", "accounts/fireworks/models/llama-v3:free", "accounts/fireworks/models/llama-v3:free"},
		{"a space is kept", "my model", "my model"},
		{"long and unprintable", strings.Repeat("\t", 500), strings.Repeat("?", maxTagValueLength)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BoundedTagValue(tc.value)
			if got != tc.want {
				t.Errorf("BoundedTagValue = %q, want %q", got, tc.want)
			}
			if len(got) > maxTagValueLength {
				t.Errorf("%d bytes, over the bound of %d", len(got), maxTagValueLength)
			}
		})
	}
}

func TestAModelNameFromAProviderIsCutAndMadePrintableAsATag(t *testing.T) {
	enableMetrics(t)

	RecordLLMMetrics(&types.ResultMetadata{
		Provider:   "router\nwith a newline",
		Model:      strings.Repeat("snapshot-", 2000) + "\x00",
		TokenUsage: &types.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	})
	RecordLLMFailure("router\nwith a newline", strings.Repeat("snapshot-", 2000), types.Strict, types.Smart, "timeout")

	snapshots := SnapshotMetrics()
	if len(snapshots) == 0 {
		t.Fatal("nothing was recorded")
	}
	for _, snapshot := range snapshots {
		for _, key := range []string{"provider", "model"} {
			value := snapshot.Tags[key]
			if len(value) > maxTagValueLength {
				t.Errorf("%s: tag %s is %d bytes long", snapshot.Name, key, len(value))
			}
			for index := 0; index < len(value); index++ {
				if value[index] < 0x20 || value[index] > 0x7e {
					t.Fatalf("%s: tag %s holds the byte %#x", snapshot.Name, key, value[index])
				}
			}
		}
	}
}

// One number is one series. The input and output token counts are the prompt
// and completion counts under a second name, and a dashboard that summed the
// token series counted them twice.
func TestNoTokenCountIsRecordedUnderTwoNames(t *testing.T) {
	enableMetrics(t)

	RecordLLMMetrics(&types.ResultMetadata{
		Provider: "codex", Model: "m",
		TokenUsage: &types.TokenUsage{PromptTokens: 90, CompletionTokens: 45, TotalTokens: 135, InputTokens: 90, OutputTokens: 45},
	})

	seen := map[float64][]string{}
	for _, snapshot := range SnapshotMetrics() {
		if strings.HasPrefix(snapshot.Name, "llm_tokens_") && snapshot.Name != "llm_tokens_total" {
			seen[snapshot.Sum] = append(seen[snapshot.Sum], snapshot.Name)
		}
		if snapshot.Name == "llm_tokens_input" || snapshot.Name == "llm_tokens_output" {
			t.Errorf("%s is recorded; it repeats the prompt or completion count", snapshot.Name)
		}
	}
	for value, names := range seen {
		if len(names) > 1 {
			t.Errorf("the count %v is recorded under %v", value, names)
		}
	}
}

// The request counter counts every request, split by how it ended: "ok" or the
// library's error kind. It used to count only the requests that were answered,
// so the failures were missing from the series called requests.
func TestTheRequestCounterCountsFailuresAndSuccessesByOutcome(t *testing.T) {
	enableMetrics(t)
	tags := func(outcome string) map[string]string {
		return map[string]string{"provider": "openai", "model": "m", "mode": "strict", "intelligence": "smart", "outcome": outcome}
	}

	for range 3 {
		RecordLLMMetrics(&types.ResultMetadata{Provider: "openai", Model: "m", Mode: types.Strict, Intelligence: types.Smart})
	}
	failures := map[string]int{"rate limited": 2, "timeout": 1, "canceled": 4, "provider unavailable": 1, "internal": 1}
	for kind, count := range failures {
		for range count {
			RecordLLMFailure("openai", "m", types.Strict, types.Smart, kind)
		}
	}

	if ok, _ := GetMetricSnapshot("llm_requests", tags(OutcomeOK)); ok.Count != 3 {
		t.Errorf("llm_requests{outcome=ok} counted %d, want 3", ok.Count)
	}
	total := int64(3)
	for kind, count := range failures {
		snapshot, found := GetMetricSnapshot("llm_requests", tags(kind))
		if !found || snapshot.Count != int64(count) {
			t.Errorf("llm_requests{outcome=%s} counted %d (found %v), want %d", kind, snapshot.Count, found, count)
		}
		total += snapshot.Count
	}
	if total != 12 {
		t.Errorf("the request series add up to %d, want all 12 requests", total)
	}

	// A failure records the count and nothing else: no duration, tokens or cost
	// were measured for it.
	for _, snapshot := range SnapshotMetrics() {
		if snapshot.Name != "llm_requests" && snapshot.Name != "llm_request_duration_ms" {
			t.Errorf("unexpected series %s %v", snapshot.Name, snapshot.Tags)
		}
		if _, hasOutcome := snapshot.Tags["outcome"]; hasOutcome != (snapshot.Name == "llm_requests") {
			t.Errorf("%s: outcome tag present = %v", snapshot.Name, hasOutcome)
		}
	}
}

// A failure with no kind is counted as "unknown" rather than under an empty
// tag or as a success, and with metrics off nothing is recorded or allocated.
func TestRecordLLMFailureOutcomes(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{"rate limited", "rate limited"},
		{"provider unavailable", "provider unavailable"},
		{"timeout", "timeout"},
		{"canceled", "canceled"},
		{"schema violation", "schema violation"},
		{"internal", "internal"},
		{"unknown", "unknown"},
		{"", "unknown"},
		{"ok", "unknown"},
		{strings.Repeat("k", 500), strings.Repeat("k", maxTagValueLength)},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			enableMetrics(t)
			RecordLLMFailure("openai", "m", types.Strict, types.Smart, tc.kind)

			snapshots := SnapshotMetrics()
			if len(snapshots) != 1 || snapshots[0].Name != "llm_requests" || snapshots[0].Tags["outcome"] != tc.want {
				t.Errorf("recorded %+v, want one llm_requests series with outcome %q", snapshots, tc.want)
			}
		})
	}

	t.Run("metrics off", func(t *testing.T) {
		enableMetrics(t)
		config.SetMetricsEnabled(false)
		if got := testing.AllocsPerRun(100, func() { RecordLLMFailure("openai", "m", types.Strict, types.Smart, "timeout") }); got != 0 {
			t.Errorf("RecordLLMFailure with metrics off allocates %v times", got)
		}
		if len(SnapshotMetrics()) != 0 {
			t.Error("a failure was recorded with metrics off")
		}
	})
}

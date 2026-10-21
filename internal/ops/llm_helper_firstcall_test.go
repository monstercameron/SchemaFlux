package ops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// What the first CallLLM of a process is made of.
//
// The first provider call of a process costs milliseconds more than the
// hundredth. This takes it apart: in a fresh process it runs, one after another
// and each for the first time, the pieces CallLLM is made of, in the order it
// reaches them, and times each with a clock that resolves tens of nanoseconds.
// Then it makes the whole call, and a second. A piece that shares a cold cost
// with an earlier one (the first use of the local time zone is one) is charged to
// the earlier, so the order is the one the call makes them in.
//
// The measurement starts 20 processes and asserts nothing, so it is skipped
// unless asked for:
//
//	SCHEMAFLUX_MEASURE=1 go test ./internal/ops/ -run TestFirstCallLLMStages -v
const firstCallStagesEnv = "SCHEMAFLUX_FIRST_CALL_LLM_STAGES_CHILD"

func TestFirstCallLLMStagesChild(t *testing.T) {
	if os.Getenv(firstCallStagesEnv) == "" {
		t.Skip("run by TestFirstCallLLMStages")
	}
	logger.ConfigureLogger(logger.LoggerConfig{Level: logger.ErrorLevel, DisableStderr: true})
	provider := benchInstantProvider{answer: `{"order_id":"A-2207"}`}
	ctx := context.Background()
	opts := quietCallOptions()
	usage := types.TokenUsage{PromptTokens: 120, CompletionTokens: 24, TotalTokens: 144}
	var cost *types.CostInfo
	metadata := &types.ResultMetadata{
		RequestID: "req_0123456789abcdef", Model: "gpt-6-luna", Provider: "openai", EndTime: time.Now(),
		TokenUsage: &usage, Custom: map[string]any{"response_format": "json", "schema_id": "x"},
	}
	var stable string

	type stage struct {
		name string
		run  func()
	}
	stages := []stage{
		{"logger.GetLogger", func() { _ = logger.GetLogger() }},
		{"resolveModel", func() { _ = resolveModel(ctx, opts, benchInstantProvider{}) }},
		{"checkBudget", func() { _ = checkBudget(ctx) }},
		{"strengthenSystemPrompt", func() { stable = dec12StrengthenSystemPrompt(ctx, "Return only valid JSON.", "json") }},
		{"types.Mode.String", func() { _ = opts.Mode.String() }},
		{"digestOf (sha256)", func() { _ = digestOf(stable) }},
		{"promptCacheKeyFor", func() { _ = promptCacheKeyFor(opts, "gpt-6-luna", "json", stable) }},
		{"requesttracking.Ensure", func() { _, _ = requesttracking.Ensure(ctx, "", "") }},
		{"observeStart and observeEnd", func() { c := observeStart(ctx, telemetry.OperationEvent{}); observeEnd(c, telemetry.OperationResult{}) }},
		{"pricing.CalculateCost", func() { cost = pricing.CalculateCost(&usage, "gpt-6-luna", "openai") }},
		{"time.Now().Date() (the local zone)", func() { _, _, _ = time.Now().Date() }},
		{"pricing.TrackCost", func() { metadata.CostInfo = cost; pricing.TrackCost(cost, metadata) }},
		{"telemetry.RecordLLMMetrics", func() { telemetry.RecordLLMMetrics(metadata) }},
		{"provider.Complete", func() { _, _ = provider.Complete(ctx, llm.CompletionRequest{Model: "gpt-6-luna"}) }},
		{"then the first CallLLM", func() { _, _ = CallLLM(ctx, provider, "Return only valid JSON.", "user", opts) }},
		{"then the second CallLLM", func() { _, _ = CallLLM(ctx, provider, "Return only valid JSON.", "user", opts) }},
	}
	times := map[string]int64{}
	order := make([]string, 0, len(stages))
	for _, s := range stages {
		start := firstCallClockNanos()
		s.run()
		times[s.name] = firstCallClockNanos() - start
		order = append(order, s.name)
	}
	encoded, _ := json.Marshal(map[string]any{"order": order, "ns": times})
	os.Stdout.WriteString("FIRSTCALLSTAGES " + string(encoded) + "\n")
}

func TestFirstCallLLMStages(t *testing.T) {
	if os.Getenv("SCHEMAFLUX_MEASURE") != "1" {
		t.Skip("starts many processes and asserts nothing; set SCHEMAFLUX_MEASURE=1 to run it")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const samples = 20
	collected := map[string][]int64{}
	var order []string
	for i := 0; i < samples; i++ {
		command := exec.Command(executable, "-test.run=^TestFirstCallLLMStagesChild$", "-test.count=1")
		command.Env = append(os.Environ(), firstCallStagesEnv+"=1")
		var stdout strings.Builder
		command.Stdout = &stdout
		if err := command.Run(); err != nil {
			t.Fatalf("the child process failed: %v\n%s", err, stdout.String())
		}
		for _, line := range strings.Split(stdout.String(), "\n") {
			rest, ok := strings.CutPrefix(line, "FIRSTCALLSTAGES ")
			if !ok {
				continue
			}
			var result struct {
				Order []string         `json:"order"`
				NS    map[string]int64 `json:"ns"`
			}
			if err := json.Unmarshal([]byte(rest), &result); err != nil {
				t.Fatal(err)
			}
			order = result.Order
			for name, ns := range result.NS {
				collected[name] = append(collected[name], ns)
			}
		}
	}
	var total int64
	for _, name := range order {
		values := append([]int64(nil), collected[name]...)
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		median := values[len(values)/2]
		if !strings.HasPrefix(name, "then the") {
			total += median
		}
		t.Logf("%-40s %9.0f us   (min %.0f, max %.0f)", name, float64(median)/1e3, float64(values[0])/1e3, float64(values[len(values)-1])/1e3)
	}
	t.Logf("%-40s %9.0f us   (the stages before the first CallLLM)", "total", float64(total)/1e3)
}

package tests

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// What the first call's extra cost is made of.
//
// TestFirstCallCost says how long the first Extract of a process takes beyond
// a later one. This takes the first call apart by running, in a fresh process,
// the pieces an Extract is made of one after another, each for the first time,
// and timing each with the fine clock. The pieces are called by their exported
// names in the order an Extract reaches them; what the first Extract costs
// after all of them have run is the last line, which is what the pieces did not
// account for (the options, the repair loop, the provider wrapper, the decode
// closures).
//
// It is an attribution, not a partition: two pieces that share a cold cost
// (the reflection of a type's fields, a map of keys the first call sizes) are
// charged to whichever comes first. The order is the one a call makes them in,
// so the first of two such pieces is the one the cost belongs to in the sense a
// later change to it would show.
//
//	SCHEMAFLUX_MEASURE=1 go test ./tests/ -run TestFirstCallPieces -v

const firstCallPiecesEnv = "SCHEMAFLUX_FIRST_CALL_PIECES_CHILD"

func TestFirstCallPiecesChild(t *testing.T) {
	if os.Getenv(firstCallPiecesEnv) == "" {
		t.Skip("run by TestFirstCallPieces")
	}
	const answer = `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`
	schemaflux.ConfigureLogging(telemetry.LoggerConfig{Level: telemetry.ErrorLevel, DisableStderr: true})
	provider := benchProvider{answer}
	ctx := schemaflux.NewClient("").WithProviderInstance(provider).Context(context.Background())
	typ := reflect.TypeFor[benchInvoice]()

	type stage struct {
		name string
		run  func()
	}
	var prose, system string
	stages := []stage{
		{"json.Marshal of the target type", func() { _, _ = json.Marshal(benchInvoice{}) }},
		{"json.Unmarshal into the target type", func() { _ = json.Unmarshal([]byte(answer), new(benchInvoice)) }},
		{"PreflightType", func() { _ = ops.PreflightType(typ) }},
		{"GenerateTypeSchema (prose)", func() { prose = ops.GenerateTypeSchema(typ) }},
		{"GenerateJSONSchema", func() { _ = ops.GenerateJSONSchema(typ) }},
		{"DescribeSchema", func() { _ = ops.DescribeSchema(typ) }},
		{"BuildExtractSystemPrompt", func() { system = ops.BuildExtractSystemPrompt(prose, types.TransformMode) }},
		{"requesttracking.Ensure", func() { _, _ = requesttracking.Ensure(ctx, "", "") }},
		{"config.GetModel", func() { _ = config.GetModel(types.Fast, "openai") }},
		{"pricing.CalculateCost", func() {
			_ = pricing.CalculateCost(&types.TokenUsage{PromptTokens: 120, CompletionTokens: 24, TotalTokens: 144}, "gpt-6-luna", "openai")
		}},
		// Extract reads the answer with an internal reader that has no exported name:
		// the decode of ParseJSON, and a walk over the text that allocates nothing.
		{"ParseJSON (the decode Extract's answer reader makes)", func() { _ = ops.ParseJSON(answer, new(benchInvoice)) }},
		{"ops.CallLLM (stub provider)", func() {
			_, _ = ops.CallLLM(ctx, provider, system, benchText, types.OpOptions{Context: ctx, Mode: types.TransformMode, Intelligence: types.Fast, ResponseFormat: "json"})
		}},
	}
	extract := func() {
		opts := schemaflux.NewExtractOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		_, _ = schemaflux.Extract[benchInvoice](benchText, opts)
	}

	times := map[string]int64{}
	order := []string{}
	for _, s := range stages {
		start := benchClockNanos()
		s.run()
		times[s.name] = benchClockNanos() - start
		order = append(order, s.name)
	}
	start := benchClockNanos()
	extract()
	times["then the first Extract"] = benchClockNanos() - start
	order = append(order, "then the first Extract")
	start = benchClockNanos()
	extract()
	times["then the second Extract"] = benchClockNanos() - start
	order = append(order, "then the second Extract")

	encoded, _ := json.Marshal(map[string]any{"order": order, "ns": times})
	os.Stdout.WriteString("FIRSTCALLPIECES " + string(encoded) + "\n")
}

func TestFirstCallPieces(t *testing.T) {
	measureProcesses(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	samples := map[string][]int64{}
	var order []string
	for i := 0; i < firstCallSamples; i++ {
		command := exec.Command(executable, "-test.run=^TestFirstCallPiecesChild$", "-test.count=1")
		command.Env = append(os.Environ(), firstCallPiecesEnv+"=1")
		var stdout strings.Builder
		command.Stdout = &stdout
		if err := command.Run(); err != nil {
			t.Fatalf("the child process failed: %v\n%s", err, stdout.String())
		}
		for _, line := range strings.Split(stdout.String(), "\n") {
			rest, ok := strings.CutPrefix(line, "FIRSTCALLPIECES ")
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
				samples[name] = append(samples[name], ns)
			}
		}
	}
	var total int64
	for _, name := range order {
		values := append([]int64(nil), samples[name]...)
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		median := values[len(values)/2]
		if !strings.HasPrefix(name, "then the second") {
			total += median
		}
		t.Logf("%-40s %9.0f us   (min %.0f, max %.0f)", name, float64(median)/1e3, float64(values[0])/1e3, float64(values[len(values)-1])/1e3)
	}
	t.Logf("%-40s %9.0f us   (sum of medians, second Extract excluded)", "total", float64(total)/1e3)
}

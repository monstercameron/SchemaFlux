package exp_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

const arbitrateAnswer = `{"options":{"o1":{"r1":{"passed":true,"score":0.8,"reasoning":"good"}},"o2":{"r1":{"passed":true,"score":0.6,"reasoning":"less good"}}},"reasoning":"compared","confidence":0.7}`

func TestArbitrateOptionsAndOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		options []exp.ArbitrateOptions
	}{
		{"defaults", nil}, {"one option value", []exp.ArbitrateOptions{{Tiebreaker: "first"}}},
		{"multiple values", []exp.ArbitrateOptions{{Weights: []float64{1}}, {IncludeReasoning: true}}},
		{"weights", []exp.ArbitrateOptions{{Weights: []float64{2}}}}, {"require all", []exp.ArbitrateOptions{{RequireAllRules: true}}},
		{"reasoning", []exp.ArbitrateOptions{{IncludeReasoning: true}}}, {"first tiebreak", []exp.ArbitrateOptions{{Tiebreaker: "first"}}},
		{"random tiebreak", []exp.ArbitrateOptions{{Tiebreaker: "random"}}}, {"common model", []exp.ArbitrateOptions{{Options: schemaflux.Options{Model: "fast-model"}}}},
		{"web search", []exp.ArbitrateOptions{{Options: schemaflux.Options{WebSearch: true}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(arbitrateAnswer)
			schemafluxtest.Install(t, p)
			got, err := exp.Arbitrate(context.Background(), []string{"a", "b"}, []string{"good"}, tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			if got.Winner != "a" || !got.Evaluated || got.WinnerIndex != 0 {
				t.Fatalf("result=%+v", got)
			}
		})
	}
}

func TestArbitrateRecordsFloorAndPayloadCanaries(t *testing.T) {
	const canary = "arb-private-payload-9a7c"
	model := &schemafluxtest.Provider{}
	model.Reply(arbitrateAnswer)
	schemafluxtest.Install(t, model)
	recorder := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(context.Background(), recorder)
	_, err := exp.Arbitrate(ctx, []string{canary, "b"}, []string{"qualified"}, exp.ArbitrateOptions{Options: schemaflux.Options{Steering: canary}})
	if err != nil {
		t.Fatal(err)
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "exp.arbitrate" {
		t.Fatalf("records=%+v", records)
	}
	blob, _ := json.Marshal(records)
	if strings.Contains(string(blob), canary) {
		t.Fatalf("record contains input canary: %s", blob)
	}
}

func TestArbitrateFloorRefusesWithoutProviderCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(arbitrateAnswer)
	schemafluxtest.Install(t, model)
	client := schemaflux.Client{Provider: model, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	_, err := exp.Arbitrate(ctx, []string{"a", "b"}, []string{"r"})
	if !errors.Is(err, schemaflux.ErrPolicy) || model.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, model.Calls())
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "exp.arbitrate" || records[0].Requests != 0 {
		t.Fatalf("records=%+v", records)
	}
}

func TestArbitrateForeignErrorCanaryIsRedacted(t *testing.T) {
	const canary = "arb-vendor-canary-71d3"
	model := &schemafluxtest.Provider{}
	model.Fail(errors.New(canary))
	var logs strings.Builder
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	recorder := &schemaflux.Recorder{}
	ctx = schemaflux.WithRecorder(ctx, recorder)
	_, err := exp.Arbitrate(ctx, []string{canary, "b"}, []string{"r"}, exp.ArbitrateOptions{Options: schemaflux.Options{Steering: canary}})
	if err == nil || !errors.Is(err, schemaflux.ErrUnavailable) || strings.Contains(err.Error(), canary) {
		t.Fatalf("error=%v", err)
	}
	if rs := recorder.Records(); len(rs) != 1 || rs[0].Op != "exp.arbitrate" || rs[0].Err == nil {
		t.Fatalf("failure records=%+v", rs)
	}
	blob, _ := json.Marshal(recorder.Records())
	if strings.Contains(string(blob), canary) {
		t.Fatalf("record exposed canary: %s", blob)
	}
	if strings.Contains(logs.String(), canary) {
		t.Fatalf("log exposed provider text: %s", logs.String())
	}
}

func TestArbitrateMalformedAnswerCanaryIsRedacted(t *testing.T) {
	const canary = "arb-answer-canary-41ce"
	model := &schemafluxtest.Provider{}
	model.Reply(canary)
	schemafluxtest.Install(t, model)
	recorder := &schemaflux.Recorder{Bodies: true}
	_, err := exp.Arbitrate(schemaflux.WithRecorder(context.Background(), recorder), []string{canary, "b"}, []string{"r"}, exp.ArbitrateOptions{Options: schemaflux.Options{Steering: canary}})
	if err == nil || !errors.Is(err, schemaflux.ErrInvalidAnswer) || strings.Contains(err.Error(), canary) {
		t.Fatalf("error=%v", err)
	}
	if rs := recorder.Records(); len(rs) != 1 || rs[0].Op != "exp.arbitrate" {
		t.Fatalf("failure records=%+v", rs)
	}
	blob, _ := json.Marshal(recorder.Records())
	if strings.Contains(string(blob), canary) {
		t.Fatalf("record exposed canary: %s", blob)
	}
}

func TestArbitrateObserverFinishesOneScope(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(arbitrateAnswer)
	schemafluxtest.Install(t, model)
	obs := &arbitrateObserver{}
	remove := telemetry.RegisterObserver(obs)
	defer remove()
	if _, err := exp.Arbitrate(context.Background(), []string{"a", "b"}, []string{"r"}); err != nil {
		t.Fatal(err)
	}
	if obs.count != 1 {
		t.Fatalf("finished operation scopes=%d", obs.count)
	}
}

type arbitrateObserver struct{ count int }

func (o *arbitrateObserver) Started(ctx context.Context, _ telemetry.Event) context.Context {
	return ctx
}
func (o *arbitrateObserver) Finished(_ context.Context, e telemetry.Result) {
	if e.Scope == telemetry.ScopeOperation {
		o.count++
	}
}

func TestArbitrateRejectsInvalidInputsAndOptions(t *testing.T) {
	tests := []struct {
		name           string
		ctx            context.Context
		opts           []exp.ArbitrateOptions
		options, rules []string
		kind           error
	}{
		{"nil context", nil, nil, []string{"a", "b"}, []string{"r"}, schemaflux.ErrInvalidRequest},
		{"empty options", context.Background(), nil, nil, []string{"r"}, schemaflux.ErrInvalidRequest},
		{"empty rules", context.Background(), nil, []string{"a", "b"}, nil, schemaflux.ErrInvalidRequest},
		{"blank rule", context.Background(), nil, []string{"a", "b"}, []string{" "}, schemaflux.ErrInvalidRequest},
		{"bad tiebreaker", context.Background(), []exp.ArbitrateOptions{{Tiebreaker: "bad"}}, []string{"a", "b"}, []string{"r"}, schemaflux.ErrConfiguration},
		{"too many weights", context.Background(), []exp.ArbitrateOptions{{Weights: []float64{1, 2}}}, []string{"a", "b"}, []string{"r"}, schemaflux.ErrConfiguration},
		{"negative weight", context.Background(), []exp.ArbitrateOptions{{Weights: []float64{-1}}}, []string{"a", "b"}, []string{"r"}, schemaflux.ErrConfiguration},
		{"zero weights", context.Background(), []exp.ArbitrateOptions{{Weights: []float64{0}}}, []string{"a", "b"}, []string{"r"}, schemaflux.ErrConfiguration},
		{"bad max output", context.Background(), []exp.ArbitrateOptions{{Options: schemaflux.Options{MaxOutputTokens: -1}}}, []string{"a", "b"}, []string{"r"}, schemaflux.ErrConfiguration},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := exp.Arbitrate[string](tc.ctx, tc.options, tc.rules, tc.opts...)
			if !errors.Is(err, tc.kind) {
				t.Fatalf("error=%v, want %v", err, tc.kind)
			}
		})
	}
}

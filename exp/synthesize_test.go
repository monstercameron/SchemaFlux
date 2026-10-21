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

type synthesisTarget struct {
	Summary string `json:"summary"`
}

const synthesisAnswer = `{"synthesized":{"summary":"combined"},"facts":[],"insights":[],"conflicts":[],"source_coverage":{}}`

func TestSynthesizeRequiresSources(t *testing.T) {
	_, err := exp.Synthesize[synthesisTarget](context.Background(), nil)
	if !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("error = %v, want invalid request", err)
	}
}

func TestSynthesizeOptionsAndResult(t *testing.T) {
	cases := []struct {
		name    string
		options []exp.SynthesizeOptions
		want    string
	}{
		{"zero", nil, "combined"},
		{"single", []exp.SynthesizeOptions{{Strategy: "compare"}}, "combined"},
		{"several", []exp.SynthesizeOptions{{Strategy: "merge"}, {ConflictResolution: "majority"}}, "combined"},
		{"strategy", []exp.SynthesizeOptions{{Strategy: "reconcile"}}, "combined"},
		{"conflict resolution", []exp.SynthesizeOptions{{ConflictResolution: "latest-wins"}}, "combined"},
		{"priorities", []exp.SynthesizeOptions{{ConflictResolution: "source-priority", SourcePriorities: []int{1, 0}}}, "combined"},
		{"omit citations", []exp.SynthesizeOptions{{OmitCitations: true}}, "combined"},
		{"omit insights", []exp.SynthesizeOptions{{OmitInsights: true}}, "combined"},
		{"output structure", []exp.SynthesizeOptions{{OutputStructure: "brief"}}, "combined"},
		{"focus areas", []exp.SynthesizeOptions{{FocusAreas: []string{"risks"}}}, "combined"},
		{"exclude aspects", []exp.SynthesizeOptions{{ExcludeAspects: []string{"names"}}}, "combined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &schemafluxtest.Provider{}
			model.Reply(synthesisAnswer)
			schemafluxtest.Install(t, model)
			got, err := exp.Synthesize[synthesisTarget](context.Background(), []any{"source", "second"}, tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			if got.Synthesized.Summary != tc.want {
				t.Fatalf("result = %+v", got)
			}
			if len(model.Requests()) != 1 {
				t.Fatalf("requests = %d", len(model.Requests()))
			}
		})
	}
}

func TestSynthesizeConvertsReportedDetails(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(`{"synthesized":{"summary":"combined"},"facts":[{"fact":"revenue rose","sources":[0],"conflicts":[]}],"insights":[{"insight":"growth continued","supporting_sources":[0,1],"type":"trend"}],"conflicts":[{"topic":"revenue","positions":{"0":"up","1":"flat"},"resolution":"up","chosen_source":0}],"source_coverage":{"0":0.8,"1":0.6}}`)
	schemafluxtest.Install(t, model)
	got, err := exp.Synthesize[synthesisTarget](context.Background(), []any{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Synthesized.Summary != "combined" || len(got.Facts) != 1 || got.Facts[0].Fact != "revenue rose" ||
		len(got.Insights) != 1 || got.Insights[0].Type != "trend" || len(got.Conflicts) != 1 ||
		got.Conflicts[0].Positions[1] != "flat" || got.SourceCoverage[0] != 0.8 {
		t.Fatalf("result details were not converted: %+v", got)
	}
}

func TestSynthesizeRejectsInvalidCommonSettings(t *testing.T) {
	_, err := exp.Synthesize[synthesisTarget](context.Background(), []any{"source"}, exp.SynthesizeOptions{Options: schemaflux.Options{MaxOutputTokens: -1}})
	if !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func TestSynthesizeRejectsInvalidStrategyAndPriorities(t *testing.T) {
	cases := []exp.SynthesizeOptions{
		{Strategy: "unknown"},
		{ConflictResolution: "source-priority"},
		{ConflictResolution: "source-priority", SourcePriorities: []int{2}},
		{ConflictResolution: "source-priority", SourcePriorities: []int{0, 0}},
	}
	for _, options := range cases {
		_, err := exp.Synthesize[synthesisTarget](context.Background(), []any{"source"}, options)
		if !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Errorf("options %+v: error = %v, want configuration", options, err)
		}
	}
}

func TestSynthesizeRejectsNilContext(t *testing.T) {
	_, err := exp.Synthesize[synthesisTarget](nil, []any{"source"})
	if !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("error = %v, want invalid request", err)
	}
}

func TestSynthesizeRecordsSuccessAndFailure(t *testing.T) {
	for _, reply := range []string{synthesisAnswer, `{}`} {
		t.Run(reply, func(t *testing.T) {
			model := &schemafluxtest.Provider{}
			model.Reply(reply)
			schemafluxtest.Install(t, model)
			recorder := &schemaflux.Recorder{}
			ctx := schemaflux.WithRecorder(context.Background(), recorder)
			_, err := exp.Synthesize[synthesisTarget](ctx, []any{"source"})
			records := recorder.Records()
			if len(records) != 1 || records[0].Op != "exp.synthesize" {
				t.Fatalf("records = %+v", records)
			}
			if reply == synthesisAnswer && records[0].Delivered != schemaflux.ContractJSONWellFormed {
				t.Fatalf("delivered = %v, want JSON well formed", records[0].Delivered)
			}
			if reply != synthesisAnswer {
				if !errors.Is(err, schemaflux.ErrInvalidAnswer) {
					t.Fatalf("error = %v, want invalid answer", err)
				}
				if records[0].Err == nil {
					t.Fatal("failed call record has no error")
				}
			}
		})
	}
}

func TestSynthesizeMalformedAnswerCanaryDoesNotReachErrorOrRecord(t *testing.T) {
	const canary = "synthesis-answer-private-canary"
	model := &schemafluxtest.Provider{}
	model.Reply(canary)
	schemafluxtest.Install(t, model)
	recorder := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(context.Background(), recorder)
	_, err := exp.Synthesize[synthesisTarget](ctx, []any{canary}, exp.SynthesizeOptions{
		Options: schemaflux.Options{Steering: canary}, OutputStructure: canary,
	})
	if err == nil {
		t.Fatal("malformed answer succeeded")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error exposed response: %v", err)
	}
	encoded, marshalErr := json.Marshal(recorder.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("record exposed response: %s", encoded)
	}
}

func TestSynthesizeForeignProviderErrorCanary(t *testing.T) {
	const canary = "synthesis-vendor-private-canary"
	model := &schemafluxtest.Provider{}
	model.Fail(errors.New(canary))
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model, Logger: logger})
	recorder := &schemaflux.Recorder{}
	ctx = schemaflux.WithRecorder(ctx, recorder)
	_, err := exp.Synthesize[synthesisTarget](ctx, []any{"private source"})
	if err == nil {
		t.Fatal("provider failure succeeded")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("returned error exposed provider message: %v", err)
	}
	encoded, marshalErr := json.Marshal(recorder.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("record exposed provider message: %s", encoded)
	}
	if strings.Contains(logs.String(), canary) {
		t.Fatalf("log exposed provider text: %s", logs.String())
	}
}

func TestSynthesizeFloorRefusesBeforeSendingAndRecordsOnce(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(synthesisAnswer)
	schemafluxtest.Install(t, model)
	client := schemaflux.Client{Provider: model, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	_, err := exp.Synthesize[synthesisTarget](ctx, []any{"source"})
	if !errors.Is(err, schemaflux.ErrPolicy) {
		t.Fatalf("error = %v, want policy", err)
	}
	if model.Calls() != 0 {
		t.Fatalf("provider calls = %d, want 0", model.Calls())
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "exp.synthesize" || records[0].Requests != 0 {
		t.Fatalf("records = %+v", records)
	}
}

// TestSynthesizeUnderAnObserverFinishesOneOperationScope guards the non-plain
// call path without relying on the observer's operation label.
func TestSynthesizeUnderAnObserverFinishesOneOperationScope(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(synthesisAnswer)
	schemafluxtest.Install(t, model)
	observer := &expSynthesizeObserver{}
	restore := telemetry.RegisterObserver(observer)
	defer restore()
	if _, err := exp.Synthesize[synthesisTarget](context.Background(), []any{"source"}); err != nil {
		t.Fatal(err)
	}
	if observer.operations != 1 {
		t.Fatalf("finished operation scopes = %d, want 1", observer.operations)
	}
}

type expSynthesizeObserver struct{ operations int }

func (o *expSynthesizeObserver) Started(ctx context.Context, _ telemetry.Event) context.Context {
	return ctx
}

func (o *expSynthesizeObserver) Finished(_ context.Context, result telemetry.Result) {
	if result.Scope == telemetry.ScopeOperation {
		o.operations++
	}
}

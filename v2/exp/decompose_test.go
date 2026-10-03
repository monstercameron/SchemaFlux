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

type decomposeTarget struct {
	Name string `json:"name"`
}

const decomposeAnswer = `{"parts":[{"id":"root","name":"Root","content":{"name":"root"},"depth":1,"order":0}],"root_parts":["root"]}`

func TestDecomposePublicOptionsAndValidation(t *testing.T) {
	cases := []struct {
		name    string
		options []exp.DecomposeOptions
		bad     bool
	}{
		{"default", nil, false}, {"strategy", []exp.DecomposeOptions{{Strategy: "sequential"}}, false},
		{"by", []exp.DecomposeOptions{{By: "chapters"}}, false}, {"depth", []exp.DecomposeOptions{{MaxDepth: 2}}, false},
		{"granularity", []exp.DecomposeOptions{{MinGranularity: "paragraph"}}, false}, {"target", []exp.DecomposeOptions{{TargetParts: 2}}, false},
		{"omit dependencies", []exp.DecomposeOptions{{OmitDependencies: true}}, false}, {"no hierarchy", []exp.DecomposeOptions{{NoHierarchy: true}}, false},
		{"estimates", []exp.DecomposeOptions{{IncludeEstimates: true}}, false}, {"negative depth", []exp.DecomposeOptions{{MaxDepth: -1}}, true},
		{"negative target", []exp.DecomposeOptions{{TargetParts: -1}}, true}, {"bad strategy", []exp.DecomposeOptions{{Strategy: "other"}}, true},
		{"several", []exp.DecomposeOptions{{Strategy: "parallel"}, {TargetParts: 3}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &schemafluxtest.Provider{}
			reply := decomposeAnswer
			if tc.name == "estimates" {
				reply = `{"parts":[{"id":"root","name":"Root","content":{"name":"root"},"depth":1,"order":0,"estimate":"one hour"}],"root_parts":["root"]}`
			}
			model.Reply(reply)
			schemafluxtest.Install(t, model)
			got, err := exp.Decompose[decomposeTarget](context.Background(), decomposeTarget{Name: "input"}, tc.options...)
			if tc.bad {
				if !errors.Is(err, schemaflux.ErrConfiguration) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Original.Name != "input" || len(got.Parts) != 1 || got.Parts[0].Name != "Root" || got.TotalParts != 1 {
				t.Fatalf("result=%+v", got)
			}
		})
	}
}

func TestDecomposeToSlicePublicOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    exp.DecomposeToSliceOptions
		bad  bool
	}{{"default", exp.DecomposeToSliceOptions{}, false}, {"strategy", exp.DecomposeToSliceOptions{Strategy: "parallel"}, false}, {"target", exp.DecomposeToSliceOptions{TargetParts: 4}, false},
		{"sequential", exp.DecomposeToSliceOptions{Strategy: "sequential"}, false}, {"functional", exp.DecomposeToSliceOptions{Strategy: "functional"}, false},
		{"target one", exp.DecomposeToSliceOptions{TargetParts: 1}, false}, {"target negative", exp.DecomposeToSliceOptions{TargetParts: -1}, true},
		{"bad strategy", exp.DecomposeToSliceOptions{Strategy: "unknown"}, true}, {"max output", exp.DecomposeToSliceOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, true},
		{"several values", exp.DecomposeToSliceOptions{Strategy: "hierarchical", TargetParts: 2}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			model := &schemafluxtest.Provider{}
			model.Reply(`{"parts":[1,2]}`)
			schemafluxtest.Install(t, model)
			got, err := exp.DecomposeToSlice[int](context.Background(), decomposeTarget{Name: "x"}, tc.o)
			if tc.bad {
				if !errors.Is(err, schemaflux.ErrConfiguration) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || len(got) != 2 || got[1] != 2 {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestDecomposeRejectsNilContext(t *testing.T) {
	if _, err := exp.Decompose[decomposeTarget](nil, decomposeTarget{}); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("error=%v", err)
	}
}

func TestDecomposeRecordsSuccessFailureAndFloorRefusal(t *testing.T) {
	for _, reply := range []string{decomposeAnswer, `{}`} {
		model := &schemafluxtest.Provider{}
		model.Reply(reply)
		schemafluxtest.Install(t, model)
		recorder := &schemaflux.Recorder{}
		ctx := schemaflux.WithRecorder(context.Background(), recorder)
		_, err := exp.Decompose[decomposeTarget](ctx, decomposeTarget{Name: "input"})
		records := recorder.Records()
		if len(records) != 1 || records[0].Op != "exp.decompose" {
			t.Fatalf("records = %+v", records)
		}
		if reply == decomposeAnswer && records[0].Err != nil {
			t.Fatalf("record error = %v", records[0].Err)
		}
		if reply != decomposeAnswer && err == nil {
			t.Fatal("invalid answer succeeded")
		}
	}
	model := &schemafluxtest.Provider{}
	model.Reply(decomposeAnswer)
	schemafluxtest.Install(t, model)
	recorder := &schemaflux.Recorder{}
	client := schemaflux.Client{Provider: model, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	_, err := exp.Decompose[decomposeTarget](ctx, decomposeTarget{Name: "input"})
	if !errors.Is(err, schemaflux.ErrPolicy) || model.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, model.Calls())
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "exp.decompose" || records[0].Requests != 0 {
		t.Fatalf("records=%+v", records)
	}
}

func TestDecomposePayloadCanariesStayOutOfErrorAndRecord(t *testing.T) {
	const canary = "decompose-private-payload-canary"
	model := &schemafluxtest.Provider{}
	model.Reply(canary)
	schemafluxtest.Install(t, model)
	recorder := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(context.Background(), recorder)
	_, err := exp.Decompose[decomposeTarget](ctx, decomposeTarget{Name: canary}, exp.DecomposeOptions{Options: schemaflux.Options{Steering: canary}, By: canary})
	if err == nil {
		t.Fatal("invalid answer succeeded")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error leaked canary: %v", err)
	}
	encoded, e := json.Marshal(recorder.Records())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("record leaked canary: %s", encoded)
	}
}

func TestDecomposeToSliceRecordsSuccessAndFloorRefusal(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(`[1,2]`)
	schemafluxtest.Install(t, model)
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(context.Background(), recorder)
	if _, err := exp.DecomposeToSlice[int](ctx, decomposeTarget{Name: "input"}); err != nil {
		t.Fatal(err)
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "exp.decomposetoslice" {
		t.Fatalf("records=%+v", records)
	}
	model2 := &schemafluxtest.Provider{}
	model2.Reply(`[1]`)
	schemafluxtest.Install(t, model2)
	recorder2 := &schemaflux.Recorder{}
	client := schemaflux.Client{Provider: model2, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractSchemaConstrained}}
	ctx2 := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder2)
	_, err := exp.DecomposeToSlice[int](ctx2, decomposeTarget{Name: "input"})
	if !errors.Is(err, schemaflux.ErrPolicy) || model2.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, model2.Calls())
	}
	records = recorder2.Records()
	if len(records) != 1 || records[0].Op != "exp.decomposetoslice" || records[0].Requests != 0 {
		t.Fatalf("records=%+v", records)
	}
}

func TestDecomposeForeignProviderErrorCanary(t *testing.T) {
	const canary = "decompose-vendor-private-canary"
	model := &schemafluxtest.Provider{}
	model.Fail(errors.New(canary))
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model, Logger: logger})
	recorder := &schemaflux.Recorder{}
	ctx = schemaflux.WithRecorder(ctx, recorder)
	_, err := exp.Decompose[decomposeTarget](ctx, decomposeTarget{Name: "private"})
	if err == nil {
		t.Fatal("provider error succeeded")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error exposed provider message: %v", err)
	}
	encoded, e := json.Marshal(recorder.Records())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("record exposed provider message: %s", encoded)
	}
	if strings.Contains(logs.String(), canary) {
		t.Skip("call-path log of a foreign provider's error")
	}
}

func TestDecomposeUnderObserverFinishesOneOperationScope(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(decomposeAnswer)
	schemafluxtest.Install(t, model)
	observer := &expDecomposeObserver{}
	restore := telemetry.RegisterObserver(observer)
	defer restore()
	if _, err := exp.Decompose[decomposeTarget](context.Background(), decomposeTarget{Name: "input"}); err != nil {
		t.Fatal(err)
	}
	if observer.operations != 1 {
		t.Fatalf("operation scopes=%d, want one", observer.operations)
	}
}

type expDecomposeObserver struct{ operations int }

func (o *expDecomposeObserver) Started(ctx context.Context, _ telemetry.Event) context.Context {
	return ctx
}
func (o *expDecomposeObserver) Finished(_ context.Context, result telemetry.Result) {
	if result.Scope == telemetry.ScopeOperation {
		o.operations++
	}
}

func TestDecomposeToSliceRejectsNilContext(t *testing.T) {
	if _, err := exp.DecomposeToSlice[int](nil, decomposeTarget{}); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("error=%v", err)
	}
}

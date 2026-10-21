package op_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func stageDescriptor(name string) op.Descriptor[string, string] {
	return op.Descriptor[string, string]{ID: op.ID{Name: name}, BuildPrompt: func(s string, _ op.Options) (string, string) { return "pass", s }, Contract: op.Contract[string]{Decode: func(s string) (string, error) { return s, nil }}}
}

func TestRunStagesPassesEachOutputToTheNextStage(t *testing.T) {
	t.Parallel()
	ctx, _ := exampleContext("first", "second")
	got, err := op.RunStages(ctx, "source", []op.Stage[string]{{Descriptor: stageDescriptor("one")}, {Descriptor: stageDescriptor("two")}})
	if err != nil || got != "second" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestRunStagesNilContextIsInvalidRequest(t *testing.T) {
	t.Parallel()
	_, err := op.RunStages[string](nil, "datum", []op.Stage[string]{{Descriptor: stageDescriptor("one")}})
	wantError(t, err, schemaflux.ErrInvalidRequest, "op.runstages")
}

func TestPreflightStagesReturnsSplitUpperBoundPlanWithoutRequests(t *testing.T) {
	t.Parallel()
	ctx, model := exampleContext()
	stages := []op.Stage[string]{{Name: "first", Descriptor: stageDescriptor("first")}, {Name: "later", Descriptor: stageDescriptor("later")}}
	plan, perStage, err := op.PreflightStages(ctx, "datum", stages)
	if err != nil || !plan.Eligible || plan.Shape != op.ShapeSplit || plan.MaxCalls != 2 || model.Calls() != 0 {
		t.Fatalf("plan %+v, err %v, calls %d", plan, err, model.Calls())
	}
	if len(perStage) != 2 || perStage[0].ShapeReason != "" || perStage[1].ShapeReason != "upper bound from the initial datum" {
		t.Fatalf("per-stage plans = %+v", perStage)
	}
}

func TestPreflightStagesNilContextIsInvalidRequest(t *testing.T) {
	t.Parallel()
	_, _, err := op.PreflightStages[string](nil, "datum", []op.Stage[string]{{Descriptor: stageDescriptor("one")}})
	wantError(t, err, schemaflux.ErrInvalidRequest, "op.preflightstages")
}

func TestPreflightStagesReturnsOnePerStagePlanForEachStageCount(t *testing.T) {
	t.Parallel()
	for count := 1; count <= 10; count++ {
		count := count
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Parallel()
			ctx, model := exampleContext()
			stages := make([]op.Stage[string], count)
			for i := range stages {
				stages[i] = op.Stage[string]{Descriptor: stageDescriptor(fmt.Sprintf("stage-%d", i))}
			}
			plan, perStage, err := op.PreflightStages(ctx, "datum", stages, op.Options{}, op.Options{Options: schemaflux.Options{Steering: ""}})
			if err != nil || !plan.Eligible || len(perStage) != count || plan.MaxCalls != count || model.Calls() != 0 {
				t.Fatalf("plan has %d calls, %d stage plans, err %v, provider calls %d", plan.MaxCalls, len(perStage), err, model.Calls())
			}
		})
	}
}

func TestRunStagesFailurePreservesCompletedStageRecords(t *testing.T) {
	t.Parallel()
	ctx, _ := exampleContext(`{"number":"A"}`, `{"total":1}`)
	var rec schemaflux.Recorder
	ctx = schemaflux.WithRecorder(ctx, &rec)
	stages := []op.Stage[string]{
		{Descriptor: stageDescriptor("one")},
		{Descriptor: op.Descriptor[string, string]{ID: op.ID{Name: "fails"}, BuildPrompt: func(s string, _ op.Options) (string, string) { return "pass", s }, Contract: op.Contract[string]{Decode: func(string) (string, error) { return "", errors.New("bad answer") }}}},
		{Descriptor: stageDescriptor("never")},
	}
	got, err := op.RunStages(ctx, "source", stages)
	if err == nil || got != "" {
		t.Fatalf("got %q, err %v; want zero and error", got, err)
	}
	records := rec.Records()
	if len(records) != 2 || records[0].Op != "one" || records[0].Requests != 1 || records[1].Op != "fails" || records[1].Requests != 2 || records[1].Err == nil {
		t.Fatalf("stage records = %+v", records)
	}
}

func TestRunStagesAddsOneRecordForEveryCompletedStage(t *testing.T) {
	t.Parallel()
	ctx, _ := exampleContext("first", "second")
	var rec schemaflux.Recorder
	ctx = schemaflux.WithRecorder(ctx, &rec)
	stages := []op.Stage[string]{{Descriptor: stageDescriptor("first-stage")}, {Descriptor: stageDescriptor("second-stage")}}
	if _, err := op.RunStages(ctx, "source", stages); err != nil {
		t.Fatal(err)
	}
	records := rec.Records()
	if len(records) != 2 || records[0].Op != "first-stage" || records[1].Op != "second-stage" || records[0].Requests != 1 || records[1].Requests != 1 {
		t.Fatalf("stage records = %+v", records)
	}
}

func TestRunStagesRunsEveryStageCountInOrder(t *testing.T) {
	t.Parallel()
	for count := 1; count <= 10; count++ {
		count := count
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Parallel()
			ctx, model := exampleContext("answer")
			stages := make([]op.Stage[string], count)
			for i := range stages {
				stages[i] = op.Stage[string]{Descriptor: stageDescriptor(fmt.Sprintf("stage-%d", i))}
			}
			got, err := op.RunStages(ctx, "source", stages, op.Options{}, op.Options{Options: schemaflux.Options{Steering: ""}})
			if err != nil || got != "answer" || model.Calls() != count {
				t.Fatalf("got %q after %d requests, err %v; want %d requests", got, model.Calls(), err, count)
			}
		})
	}
}

// A call refused before its first stage sends nothing and still adds one Record,
// named for the call, as every function that asks a model does.
func TestRunStagesRefusedBeforeItsFirstStageAddsOneRecordWithNoRequests(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	one := []op.Stage[string]{{Descriptor: stageDescriptor("one")}}
	cases := []struct {
		name   string
		ctx    func(model *schemafluxtest.Provider, rec *schemaflux.Recorder) context.Context
		stages []op.Stage[string]
		opts   []op.Options
		kind   schemaflux.Kind
	}{
		{"a cancelled context", func(m *schemafluxtest.Provider, r *schemaflux.Recorder) context.Context {
			return schemaflux.WithClient(cancelled, schemaflux.Client{Provider: m, Recorder: r})
		}, one, nil, schemaflux.ErrCanceled},
		{"a negative option", func(m *schemafluxtest.Provider, r *schemaflux.Recorder) context.Context {
			return clientOn(m, recordingTo(r))
		}, one, []op.Options{{Options: schemaflux.Options{MaxOutputTokens: -1}}}, schemaflux.ErrConfiguration},
		{"no stages", func(m *schemafluxtest.Provider, r *schemaflux.Recorder) context.Context {
			return clientOn(m, recordingTo(r))
		}, nil, nil, schemaflux.ErrInvalidRequest},
		{"a stage with no prompt builder", func(m *schemafluxtest.Provider, r *schemaflux.Recorder) context.Context {
			return clientOn(m, recordingTo(r))
		}, []op.Stage[string]{{Descriptor: op.Descriptor[string, string]{ID: op.ID{Name: "bare"}}}}, nil, schemaflux.ErrConfiguration},
		{"a floor above the level", func(m *schemafluxtest.Provider, r *schemaflux.Recorder) context.Context {
			m.SetName("op-floor")
			return clientOn(m, recordingTo(r), underFloor(schemaflux.ContractFullyGoverned))
		}, one, nil, schemaflux.ErrPolicy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var model schemafluxtest.Provider
			model.Reply(`{"text":"x"}`)
			var rec schemaflux.Recorder
			got, err := op.RunStages(tc.ctx(&model, &rec), "datum", tc.stages, tc.opts...)
			if got != "" {
				t.Errorf("the call returned %q beside an error", got)
			}
			wantError(t, err, tc.kind, "op.runstages")
			record := onlyRecord(t, &rec)
			if record.Op != "op.runstages" || record.Requests != 0 || record.Err == nil {
				t.Errorf("record = op %q, %d requests, error %v; want op.runstages, 0 requests and the call's error", record.Op, record.Requests, record.Err)
			}
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent by a refused call", model.Calls())
			}
		})
	}
}

// A call that sent requests keeps one Record per stage and adds none for itself.
func TestRunStagesThatFailsInItsSecondStageAddsNoRecordBesideTheStagesRecords(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	ctx := clientOn(answeringWith("first"), recordingTo(&rec))
	stages := []op.Stage[string]{{Descriptor: stageDescriptor("one")}, {Descriptor: op.Descriptor[string, string]{ID: op.ID{Name: "two"}, BuildPrompt: func(s string, _ op.Options) (string, string) { return "pass", s },
		Contract: op.Contract[string]{Decode: func(string) (string, error) { return "", errors.New("no") }}}}}
	if _, err := op.RunStages(ctx, "datum", stages); err == nil {
		t.Fatal("the call did not fail")
	}
	records := rec.Records()
	if len(records) != 2 || records[0].Op != "one" || records[1].Op != "two" {
		t.Fatalf("records = %+v, want one for each stage and none for the call", records)
	}
}

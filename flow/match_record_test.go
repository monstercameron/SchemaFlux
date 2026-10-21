package flow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestMatchAddsOneRecordForTheCallWithTheOperationOfTheAmendments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		model    func() schemaflux.Provider
		cases    func() []flow.Case
		err      schemaflux.Kind // zero: the call succeeds
		requests int
	}{
		{"a string condition that holds", func() schemaflux.Provider { return modelThatSays("true") }, func() []flow.Case { return []flow.Case{flow.When("a", func() {})} }, 0, 1},
		{"a string condition that does not", func() schemaflux.Provider { return modelThatSays("false") }, func() []flow.Case { return []flow.Case{flow.When("a", func() {})} }, 0, 1},
		{"two string conditions", func() schemaflux.Provider { return modelThatSays("false", "true") }, func() []flow.Case {
			return []flow.Case{flow.When("a", func() {}), flow.When("b", func() {})}
		}, 0, 2},
		{"a Go condition", func() schemaflux.Provider { return modelThatSays("true") }, func() []flow.Case {
			return []flow.Case{flow.When(reflect.TypeFor[string](), func() {})}
		}, 0, 0},
		{"the default", func() schemaflux.Provider { return modelThatSays("true") }, func() []flow.Case { return []flow.Case{flow.Otherwise(func() {})} }, 0, 0},
		{"no cases", func() schemaflux.Provider { return modelThatSays("true") }, func() []flow.Case { return nil }, 0, 0},
		{"an answer that is neither", func() schemaflux.Provider { return modelThatSays("perhaps") }, func() []flow.Case { return []flow.Case{flow.When("a", func() {})} }, schemaflux.ErrInvalidAnswer, 1},
		{"a failure of the provider", func() schemaflux.Provider {
			return modelThatFails(&schemaflux.Error{Kind: schemaflux.ErrRateLimited})
		}, func() []flow.Case { return []flow.Case{flow.When("a", func() {})} }, schemaflux.ErrRateLimited, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			_, err := flow.Match(clientOn(tc.model(), recordedBy(&recorder)), "x", tc.cases()...)
			r := onlyOneRecord(t, &recorder)
			if r.Op != "flow.match" {
				t.Errorf("Op = %q, want flow.match", r.Op)
			}
			if r.Requests != tc.requests {
				t.Errorf("Requests = %d, want %d", r.Requests, tc.requests)
			}
			if r.Requested != schemaflux.ContractPromptOnly {
				t.Errorf("Requested = %v, want prompt only", r.Requested)
			}
			if len(r.Asked) != 0 {
				t.Errorf("Asked = %+v: Match builds no decision record", r.Asked)
			}
			if tc.err == 0 {
				if err != nil || r.Err != nil {
					t.Errorf("err = %v, record error = %v", err, r.Err)
				}
				return
			}
			wantKind(t, err, tc.err)
			wantKind(t, r.Err, tc.err)
			var e *schemaflux.Error
			if errors.As(r.Err, &e) && e.Op != "flow.match" {
				t.Errorf("the Record's error is for %q", e.Op)
			}
		})
	}
}

func TestMatchAddsARecordForACallThatIsRefusedBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	var recorder schemaflux.Recorder
	model := modelThatSays("true")
	_, err := flow.Match(clientOn(model, recordedBy(&recorder)), make(chan int), flow.When("a", func() {}))
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	r := onlyOneRecord(t, &recorder)
	wantKind(t, r.Err, schemaflux.ErrInvalidRequest)
	if r.Op != "flow.match" || r.Requests != 0 || model.Calls() != 0 {
		t.Errorf("record = op %q requests %d after %d requests", r.Op, r.Requests, model.Calls())
	}

	var done schemaflux.Recorder
	cancelled := doneContexts()[0].ctx
	_, err = flow.Match(schemaflux.WithRecorder(schemaflux.WithClient(cancelled, schemaflux.Client{Provider: model}), &done), "x", flow.Otherwise(func() {}))
	wantKind(t, err, schemaflux.ErrCanceled)
	if r := onlyOneRecord(t, &done); r.Op != "flow.match" || r.Requests != 0 {
		t.Errorf("record = op %q requests %d", r.Op, r.Requests)
	}
}

func TestMatchUnderAnyFloorIsRefusedBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	levels := []schemaflux.ContractLevel{
		schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaConstrained, schemaflux.ContractSchemaAndInvariantChecked,
		schemaflux.ContractEvidenceChecked, schemaflux.ContractFullyGoverned,
	}
	for _, level := range levels {
		for _, withRecorder := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v, recorder %v", level, withRecorder), func(t *testing.T) {
				t.Parallel()
				model := floorModel("true")
				var recorder schemaflux.Recorder
				settings := []func(*schemaflux.Client){heldToFloor(level)}
				if withRecorder {
					settings = append(settings, recordedBy(&recorder))
				}
				action, n := ran()
				// A text answer is the weakest level there is; a Go condition sends
				// nothing, and the call is refused all the same.
				for _, c := range []flow.Case{flow.When("a string", action), flow.When(reflect.TypeFor[string](), action), flow.Otherwise(action)} {
					got, err := flow.Match(clientOn(model, settings...), "x", c)
					wantKind(t, err, schemaflux.ErrPolicy)
					if got || n.Load() != 0 || model.Calls() != 0 {
						t.Errorf("Match = %v, ran %d, %d requests: a floor above prompt only refuses before anything is sent", got, n.Load(), model.Calls())
					}
				}
				if withRecorder {
					for _, r := range recorder.Records() {
						wantKind(t, r.Err, schemaflux.ErrPolicy)
						if r.Op != "flow.match" || r.Requests != 0 {
							t.Errorf("record = op %q requests %d", r.Op, r.Requests)
						}
					}
					if len(recorder.Records()) != 3 {
						t.Errorf("%d Records for three refused calls", len(recorder.Records()))
					}
				}
			})
		}
	}
	t.Run("a floor of prompt only holds nothing back", func(t *testing.T) {
		t.Parallel()
		model := floorModel("true")
		var recorder schemaflux.Recorder
		action, n := ran()
		got, err := flow.Match(clientOn(model, heldToFloor(schemaflux.ContractPromptOnly), recordedBy(&recorder)), "x", flow.When("a", action))
		if err != nil || !got || n.Load() != 1 {
			t.Fatalf("Match = %v, %v", got, err)
		}
		if r := onlyOneRecord(t, &recorder); r.Delivered != schemaflux.ContractPromptOnly || r.Requests != 1 {
			t.Errorf("record = delivered %v requests %d", r.Delivered, r.Requests)
		}
	})
}

func TestMatchUnderAnObserverIsOneOperationAndAsksWhatAPlainCallAsks(t *testing.T) {
	// Not parallel: an observer is process state.
	for _, tc := range []struct {
		name  string
		model schemaflux.Provider
	}{
		{"an answer", modelThatSays("true")},
		{"a failure", modelThatFails(nil)},
		{"an answer that is neither", modelThatSays("perhaps")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var recorder schemaflux.Recorder
			seen := operationsSeenDuring(func() {
				_, _ = flow.Match(clientOn(tc.model, recordedBy(&recorder)), "x", flow.When("a", func() {}))
			})
			if len(seen) != 1 {
				t.Errorf("the observer saw %v finish, want exactly one operation", seen)
			}
			if onlyOneRecord(t, &recorder).Op != "flow.match" {
				t.Error("the Record under an observer has the wrong operation")
			}
		})
	}
	t.Run("the request is the one a plain call sends", func(t *testing.T) {
		plain, observed := modelThatSays("true"), modelThatSays("true")
		if _, err := flow.Match(clientOn(plain), "x", flow.When("a", func() {})); err != nil {
			t.Fatal(err)
		}
		operationsSeenDuring(func() {
			if _, err := flow.Match(clientOn(observed), "x", flow.When("a", func() {})); err != nil {
				t.Error(err)
			}
		})
		if plain.Last().System != observed.Last().System || plain.Last().User != observed.Last().User || plain.Last().Model != observed.Last().Model {
			t.Error("an observer changed what the model is asked")
		}
	})
}

func TestMatchAnActionThatMakesACallOfItsOwnAddsItsOwnRecord(t *testing.T) {
	t.Parallel()
	var recorder schemaflux.Recorder
	model := modelThatSays("true", choice(1))
	ctx := clientOn(model, recordedBy(&recorder))
	var decided flow.DecideResult[string]
	got, err := flow.Match(ctx, "x", flow.When("a", func() {
		decided, _ = flow.Decide(ctx, "s", triage())
	}))
	if err != nil || !got || decided.Value != "technical" {
		t.Fatalf("Match = %v, %v, decided %+v", got, err, decided)
	}
	var ops []string
	for _, r := range recorder.Records() {
		ops = append(ops, r.Op)
		if r.Requests != 1 || r.Err != nil {
			t.Errorf("record %q = %d requests, error %v: each call is counted for its own requests", r.Op, r.Requests, r.Err)
		}
	}
	slices.Sort(ops)
	if !slices.Equal(ops, []string{"flow.decide", "flow.match"}) {
		t.Errorf("the recorder holds %v, want one Record for each call", ops)
	}
}

func TestMatchKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-77c0de"
	input := map[string]string{"key " + canary: "text " + canary}
	run := func(ctx context.Context) error {
		_, err := flow.Match(ctx, input, flow.When("condition "+canary, func() {}), flow.Otherwise(func() {}))
		return err
	}
	t.Run("the model's answer is refused and says the canary", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays("I say "+canary), true, run)
	})
	t.Run("the provider fails and says the canary beneath its error", func(t *testing.T) {
		t.Parallel()
		failure := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider said no", Err: errors.New(canary)}
		requireNoPayload(t, canary, modelThatFails(failure), false, run)
	})
	t.Run("the provider fails with a plain error that says the canary", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatFails(errors.New(canary)), false, run)
	})
	t.Run("the provider panics with the canary", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { panic(canary) })
		requireNoPayload(t, canary, &p, true, run)
	})
	t.Run("the call succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays("true"), true, run)
	})
	t.Run("the input cannot be written", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays("true"), true, func(ctx context.Context) error {
			_, err := flow.Match(ctx, map[string]any{"key " + canary: make(chan int)}, flow.When("condition "+canary, func() {}))
			return err
		})
	})
}

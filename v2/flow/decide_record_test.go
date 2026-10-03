package flow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// onlyOneRecord returns the one Record the recorder holds, or fails the test.
func onlyOneRecord(t *testing.T, r *schemaflux.Recorder) schemaflux.Record {
	t.Helper()
	records := r.Records()
	if len(records) != 1 {
		t.Fatalf("the recorder holds %d Records, want exactly 1: %+v", len(records), records)
	}
	return records[0]
}

// finishWatcher is a telemetry observer that tells a function what finished.
type finishWatcher struct{ finished func(telemetry.Result) }

func (finishWatcher) Started(ctx context.Context, _ telemetry.Event) context.Context { return ctx }
func (w finishWatcher) Finished(_ context.Context, r telemetry.Result)               { w.finished(r) }

// operationsSeenDuring runs f with an observer registered and returns the
// operations that finished. It is process state: the test that calls it is not
// parallel.
func operationsSeenDuring(f func()) []string {
	var mu sync.Mutex
	var seen []string
	restore := telemetry.RegisterObserver(finishWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			mu.Lock()
			seen = append(seen, r.Operation)
			mu.Unlock()
		}
	}})
	defer restore()
	f()
	return seen
}

func TestDecideAddsOneRecordForTheCallWithTheOperationOfTheAmendments(t *testing.T) {
	t.Parallel()
	asked := func(r schemaflux.Record) bool { return r.Requests == 1 }
	cases := []struct {
		name      string
		model     func() schemaflux.Provider
		decisions func() []flow.Decision[string]
		opts      []schemaflux.Options
		err       schemaflux.Kind // zero: the call succeeds
		check     func(t *testing.T, r schemaflux.Record)
	}{
		{"an answer", func() schemaflux.Provider { return modelThatSays(choice(1)) }, triage, nil, 0, func(t *testing.T, r schemaflux.Record) {
			if !asked(r) || r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked || r.Strategy != "" {
				t.Errorf("record = %+v", r)
			}
			if len(r.Asked) != 0 {
				t.Errorf("Asked = %+v: Decide builds no decision record", r.Asked)
			}
		}},
		{"a condition that holds", func() schemaflux.Provider { return modelThatSays(choice(1)) }, func() []flow.Decision[string] {
			d := triage()
			d[1].Condition = func(any) bool { return true }
			return d
		}, nil, 0, func(t *testing.T, r schemaflux.Record) {
			if r.Requests != 0 {
				t.Errorf("Requests = %d: no model was asked", r.Requests)
			}
		}},
		{"a failure that is returned", func() schemaflux.Provider {
			return modelThatFails(&schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded})
		}, triage, nil, schemaflux.ErrBudgetExceeded, func(t *testing.T, r schemaflux.Record) {
			if r.Requests != 1 || r.Delivered >= r.Requested {
				t.Errorf("record = %+v", r)
			}
		}},
		{"an answer that selects nothing", func() schemaflux.Provider { return modelThatSays(choice(9)) }, func() []flow.Decision[string] {
			d := triage()
			d[2].Fallback = false
			return d
		}, nil, schemaflux.ErrInvalidAnswer, func(t *testing.T, r schemaflux.Record) {
			if r.Requests < 1 {
				t.Errorf("record = %+v", r)
			}
		}},
		{"a bad option", func() schemaflux.Provider { return modelThatSays(choice(1)) }, triage, []schemaflux.Options{{MaxOutputTokens: -1}}, schemaflux.ErrConfiguration, func(t *testing.T, r schemaflux.Record) {
			if r.Requests != 0 {
				t.Errorf("Requests = %d: nothing was sent", r.Requests)
			}
		}},
		{"two fallbacks", func() schemaflux.Provider { return modelThatSays(choice(1)) }, func() []flow.Decision[string] {
			return []flow.Decision[string]{{Fallback: true}, {Fallback: true}}
		}, nil, schemaflux.ErrConfiguration, func(t *testing.T, r schemaflux.Record) {
			if r.Requests != 0 {
				t.Errorf("Requests = %d: nothing was sent", r.Requests)
			}
		}},
		{"no decisions", func() schemaflux.Provider { return modelThatSays(choice(1)) }, func() []flow.Decision[string] { return nil }, nil, schemaflux.ErrInvalidRequest, func(t *testing.T, r schemaflux.Record) {
			if r.Requests != 0 {
				t.Errorf("Requests = %d: nothing was sent", r.Requests)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			_, err := flow.Decide(clientOn(tc.model(), recordedBy(&recorder)), "s", tc.decisions(), tc.opts...)
			r := onlyOneRecord(t, &recorder)
			if r.Op != "flow.decide" {
				t.Errorf("Op = %q, want flow.decide", r.Op)
			}
			if tc.err == 0 {
				if err != nil || r.Err != nil {
					t.Errorf("err = %v, record error = %v", err, r.Err)
				}
			} else {
				wantKind(t, err, tc.err)
				wantKind(t, r.Err, tc.err)
				var e *schemaflux.Error
				if errors.As(r.Err, &e) && e.Op != "flow.decide" {
					t.Errorf("the Record's error is for %q", e.Op)
				}
			}
			tc.check(t, r)
		})
	}
}

func TestDecideRecordSaysTheModelFailedAndTheFallbackWasTaken(t *testing.T) {
	t.Parallel()
	for _, kind := range []schemaflux.Kind{schemaflux.ErrUnavailable, schemaflux.ErrRateLimited, schemaflux.ErrTimeout} {
		t.Run(kind.Error(), func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			got, err := flow.Decide(clientOn(modelThatFails(&schemaflux.Error{Kind: kind}), recordedBy(&recorder)), "s", triage())
			if err != nil || !got.Fallback {
				t.Fatalf("Decide = %+v, %v", got, err)
			}
			r := onlyOneRecord(t, &recorder)
			wantKind(t, r.Err, kind)
			if r.Op != "flow.decide" || r.Strategy != "fallback" || r.Requests < 1 {
				t.Errorf("record = op %q strategy %q requests %d", r.Op, r.Strategy, r.Requests)
			}
			if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractPromptOnly {
				t.Errorf("record = requested %v delivered %v: no answer of the model was delivered", r.Requested, r.Delivered)
			}
		})
	}
	t.Run("an answer that cannot be used", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		got, err := flow.Decide(clientOn(modelThatSays(`{}`), recordedBy(&recorder)), "s", triage())
		if err != nil || !got.Fallback {
			t.Fatalf("Decide = %+v, %v", got, err)
		}
		r := onlyOneRecord(t, &recorder)
		wantKind(t, r.Err, schemaflux.ErrInvalidAnswer)
		if r.Strategy != "fallback" || r.Delivered != schemaflux.ContractPromptOnly {
			t.Errorf("record = strategy %q delivered %v", r.Strategy, r.Delivered)
		}
	})
}

func TestDecideRecordOfACallThatTookNoFallbackIsNotMarkedAsOne(t *testing.T) {
	t.Parallel()
	var recorder schemaflux.Recorder
	if _, err := flow.Decide(clientOn(modelThatSays(choice(2)), recordedBy(&recorder)), "s", triage()); err != nil {
		t.Fatal(err)
	}
	if r := onlyOneRecord(t, &recorder); r.Strategy != "" || r.Err != nil {
		t.Errorf("record = strategy %q error %v: the model chose the marked decision, which is no fallback", r.Strategy, r.Err)
	}
}

func TestDecideGivesEachRecorderItsRecordOnce(t *testing.T) {
	t.Parallel()
	var onClient, onContext schemaflux.Recorder
	ctx := schemaflux.WithRecorder(clientOn(modelThatSays(choice(0)), recordedBy(&onClient)), &onContext)
	if _, err := flow.Decide(ctx, "s", triage()); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]*schemaflux.Recorder{"client": &onClient, "context": &onContext} {
		if got := onlyOneRecord(t, r); got.Op != "flow.decide" {
			t.Errorf("the %s recorder's Record is for %q", name, got.Op)
		}
	}
}

func TestDecideUnderAFloorDeliversAtTheLevelOfTheTableOrIsRefusedBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		level   schemaflux.ContractLevel
		refused bool
	}{
		{schemaflux.ContractPromptOnly, false},
		{schemaflux.ContractJSONWellFormed, false},
		{schemaflux.ContractSchemaConstrained, false},
		{schemaflux.ContractSchemaAndInvariantChecked, false},
		{schemaflux.ContractEvidenceChecked, true},
		{schemaflux.ContractFullyGoverned, true},
	} {
		for _, withRecorder := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v, recorder %v", tc.level, withRecorder), func(t *testing.T) {
				t.Parallel()
				model := floorModel(choice(1))
				var recorder schemaflux.Recorder
				settings := []func(*schemaflux.Client){heldToFloor(tc.level)}
				if withRecorder {
					settings = append(settings, recordedBy(&recorder))
				}
				got, err := flow.Decide(clientOn(model, settings...), "s", triage())
				if tc.refused {
					wantKind(t, err, schemaflux.ErrPolicy)
					if !zeroResult(got) || model.Calls() != 0 {
						t.Errorf("Decide = %+v after %d requests: a floor above the level refuses before anything is sent", got, model.Calls())
					}
					if withRecorder {
						r := onlyOneRecord(t, &recorder)
						wantKind(t, r.Err, schemaflux.ErrPolicy)
						if r.Op != "flow.decide" || r.Requests != 0 {
							t.Errorf("record = op %q requests %d", r.Op, r.Requests)
						}
					}
					return
				}
				if err != nil || got.Value != "technical" {
					t.Fatalf("Decide = %+v, %v", got, err)
				}
				if withRecorder {
					if r := onlyOneRecord(t, &recorder); r.Delivered != schemaflux.ContractSchemaAndInvariantChecked || r.Requested != schemaflux.ContractSchemaAndInvariantChecked {
						t.Errorf("record = requested %v delivered %v", r.Requested, r.Delivered)
					}
				}
			})
		}
	}
}

func TestDecideUnderAFloorTheModelCannotMeetNeverTakesTheFallback(t *testing.T) {
	t.Parallel()
	model := floorModel(choice(1))
	got, err := flow.Decide(clientOn(model, heldToFloor(schemaflux.ContractFullyGoverned)), "s", triage())
	wantKind(t, err, schemaflux.ErrPolicy)
	if !zeroResult(got) {
		t.Errorf("Decide returned %+v: a policy refusal is not a failure to reach the model", got)
	}
}

func TestDecideUnderAnObserverIsOneOperationWhatEverItReturns(t *testing.T) {
	// Not parallel: an observer is process state.
	for _, tc := range []struct {
		name  string
		model schemaflux.Provider
	}{
		{"an answer", modelThatSays(choice(0))},
		{"a failure that falls back", modelThatFails(nil)},
		{"a failure that is returned", modelThatFails(&schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var recorder schemaflux.Recorder
			seen := operationsSeenDuring(func() {
				_, _ = flow.Decide(clientOn(tc.model, recordedBy(&recorder)), "s", triage())
			})
			if len(seen) != 1 {
				t.Errorf("the observer saw %v finish, want exactly one operation", seen)
			}
			if onlyOneRecord(t, &recorder).Op != "flow.decide" {
				t.Error("the Record under an observer has the wrong operation")
			}
		})
	}
	t.Run("the request is the one a plain call sends", func(t *testing.T) {
		plain, observed := modelThatSays(choice(0)), modelThatSays(choice(0))
		if _, err := flow.Decide(clientOn(plain), "s", triage()); err != nil {
			t.Fatal(err)
		}
		operationsSeenDuring(func() {
			if _, err := flow.Decide(clientOn(observed), "s", triage()); err != nil {
				t.Error(err)
			}
		})
		if plain.Last().System != observed.Last().System || plain.Last().User != observed.Last().User || plain.Last().Model != observed.Last().Model {
			t.Error("an observer changed what the model is asked")
		}
	})
}

// requireNoPayload runs call on a client whose provider setup has scripted, with
// a recorder and a logger, and fails if the canary is in anything the call leaves
// behind: a Record (as a value and as JSON), a log line, or what the error says
// (as a string, as JSON and through slog's two handlers), and, when wholeChain
// is true, in any link of the error's chain. A call whose provider returned an
// error of its own leaves that error in the chain with the text the provider
// gave it, so for such a call only the error the caller receives is read.
func requireNoPayload(t *testing.T, canary string, model schemaflux.Provider, wholeChain bool, call func(ctx context.Context) error) {
	t.Helper()
	var recorder schemaflux.Recorder
	var logs schemafluxtest.Logs
	err := call(clientOn(model, recordedBy(&recorder), func(c *schemaflux.Client) { c.Logger = logs.Logger() }))

	var seen []string
	if err != nil {
		if wholeChain {
			seen = append(seen, chainTexts(err)...)
		} else {
			seen = append(seen, err.Error())
		}
		if encoded, jsonErr := json.Marshal(err); jsonErr == nil {
			seen = append(seen, string(encoded))
		}
		var text, structured bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Error("the call failed", "err", err)
		slog.New(slog.NewJSONHandler(&structured, nil)).Error("the call failed", "err", err)
		seen = append(seen, text.String(), structured.String())
	}
	for _, record := range recorder.Records() {
		seen = append(seen, fmt.Sprintf("%+v", record))
		if encoded, jsonErr := json.Marshal(record); jsonErr == nil {
			seen = append(seen, string(encoded))
		}
		if wholeChain {
			seen = append(seen, chainTexts(record.Err)...)
		}
	}
	for _, entry := range logs.Entries() {
		line := entry.Message
		entry.Attrs(func(a slog.Attr) bool {
			line += " " + a.Key + "=" + a.Value.String()
			return true
		})
		seen = append(seen, line)
	}
	for _, text := range seen {
		if strings.Contains(text, canary) {
			t.Errorf("the canary reached %q", shortened(text))
		}
	}
	if len(recorder.Records()) != 1 {
		t.Errorf("the recorder holds %d Records, want 1", len(recorder.Records()))
	}
}

// chainTexts is what Error says for err and for every error beneath it.
func chainTexts(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		out = append(out, e.Error())
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		}
	}
	walk(err)
	return out
}

// shortened cuts text for a failure message, so that a test never prints a whole
// prompt.
func shortened(text string) string {
	if runes := []rune(text); len(runes) > 160 {
		return string(runes[:160]) + "..."
	}
	return text
}

func TestDecideKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-5d1e88"
	// The caller's text is in the situation, a description, a value and the
	// steering; the model's text is in its explanation; a provider's is in its
	// error. None of it may reach a Record, an error or a log line.
	decisions := func() []flow.Decision[string] {
		return []flow.Decision[string]{
			{Value: "value " + canary, Description: "description " + canary},
			{Value: "other", Description: "other", Fallback: true},
		}
	}
	options := schemaflux.Options{Steering: "steer " + canary}
	situation := map[string]string{"key " + canary: "text " + canary}
	run := func(ctx context.Context) error {
		_, err := flow.Decide(ctx, situation, decisions(), options)
		return err
	}
	noFallback := func(ctx context.Context) error {
		d := decisions()
		d[1].Fallback = false
		_, err := flow.Decide(ctx, situation, d, options)
		return err
	}

	t.Run("the model's answer is refused and says the canary", func(t *testing.T) {
		t.Parallel()
		bad := `{"selected":9,"explanation":"` + canary + `","confidence":0.5,"alternatives":[]}`
		requireNoPayload(t, canary, modelThatSays(bad), true, noFallback)
	})
	t.Run("the model's answer is not JSON and says the canary", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays("I pick "+canary), true, noFallback)
	})
	t.Run("the model's answer is used and says the canary", func(t *testing.T) {
		t.Parallel()
		good := `{"selected":1,"explanation":"` + canary + `","confidence":0.5,"alternatives":[0]}`
		requireNoPayload(t, canary, modelThatSays(good), true, func(ctx context.Context) error {
			got, err := flow.Decide(ctx, situation, decisions(), options)
			if err == nil && got.Explanation != canary {
				t.Errorf("the model's words were not returned to the caller: %q", got.Explanation)
			}
			return err
		})
	})
	t.Run("the provider fails and says the canary, and no fallback is marked", func(t *testing.T) {
		t.Parallel()
		failure := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider said no", Err: errors.New(canary)}
		requireNoPayload(t, canary, modelThatFails(failure), false, noFallback)
	})
	t.Run("the provider fails with a kind that is returned", func(t *testing.T) {
		t.Parallel()
		failure := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Message: "the provider said no", Err: errors.New(canary)}
		requireNoPayload(t, canary, modelThatFails(failure), false, run)
	})
	t.Run("the provider fails and the fallback is taken", func(t *testing.T) {
		t.Parallel()
		failure := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider said no", Err: errors.New(canary)}
		requireNoPayload(t, canary, modelThatFails(failure), false, func(ctx context.Context) error {
			return run(ctx)
		})
	})
	t.Run("a condition holds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays(choice(0)), true, func(ctx context.Context) error {
			d := decisions()
			d[0].Condition = func(any) bool { return true }
			got, err := flow.Decide(ctx, situation, d, options)
			if err == nil && !strings.Contains(got.Explanation, canary) {
				// The explanation of a condition names the description, which the
				// caller wrote: it is the caller's own result and not a Record.
				t.Errorf("Explanation = %q", got.Explanation)
			}
			return err
		})
	})
	t.Run("the options are refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays(choice(0)), true, func(ctx context.Context) error {
			_, err := flow.Decide(ctx, situation, decisions(), schemaflux.Options{Steering: "steer " + canary, MaxOutputTokens: -1})
			return err
		})
	})
	t.Run("two decisions are marked", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays(choice(0)), true, func(ctx context.Context) error {
			d := decisions()
			d[0].Fallback = true
			_, err := flow.Decide(ctx, situation, d, options)
			return err
		})
	})
	t.Run("the situation cannot be written", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, modelThatSays(choice(0)), true, func(ctx context.Context) error {
			_, err := flow.Decide(ctx, map[string]any{"key " + canary: make(chan int)}, decisions(), options)
			return err
		})
	})
	t.Run("the provider panics with the canary", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { panic(canary) })
		requireNoPayload(t, canary, &p, true, run)
	})
	t.Run("the provider fails with a plain error that says the canary", func(t *testing.T) {
		t.Parallel()
		// A known open defect: the call path logs the raw text of a foreign
		// provider's error to the client's logger.
		requireNoPayload(t, canary, modelThatFails(errors.New(canary)), false, noFallback)
	})
}

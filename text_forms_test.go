package schemaflux_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// The text functions that are not Summarize (Rewrite, Translate, Expand and
// their Detail forms, Compress, Normalize, Format and Merge) are held to one set
// of rules, and the tests of those rules are written once, over a table. Each
// operation's own test file adds its rows to textForms from an init function,
// and keeps the tests of what is particular to it (its options, its refusals,
// its result type) in the same file.

// textForm is one function of the text files as the shared tests call it.
type textForm struct {
	// op is the Op of the Record the function adds, and of its errors.
	op string

	// level is the contract level the function's answer is delivered at.
	level schemaflux.ContractLevel

	// plain is true for a function whose second form asks another question (the
	// Detail forms): it sends the plain request in every state of the call.
	plain bool

	// body is a reply the call succeeds on. badBody is one it is refused on, with
	// ErrInvalidAnswer; it is empty for a function that takes any reply.
	body, badBody string

	// call makes the call on ctx. A payload that is not empty is put into every
	// text the caller supplies: the input, the steering, each option that is a
	// word or a list of them, a map key.
	call func(ctx context.Context, payload string) error

	// refused makes a call with an option that is refused as a configuration
	// error, or, when refusedKind is set, with an argument refused as that kind.
	refused     func(ctx context.Context, payload string) error
	refusedKind schemaflux.Kind
}

// textForms is filled by the init functions of the operation test files.
var textForms []textForm

func (f textForm) refusal() schemaflux.Kind {
	if f.refusedKind != 0 {
		return f.refusedKind
	}
	return schemaflux.ErrConfiguration
}

func TestTextFormsAddOneRecordWithTheOpOfTheTableOnEveryOutcome(t *testing.T) {
	t.Parallel()
	for _, f := range textForms {
		t.Run(f.op, func(t *testing.T) {
			t.Parallel()
			t.Run("a call that succeeds", func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				if err := f.call(operationClient(answeringWith(f.body), recordingTo(&recorder)), ""); err != nil {
					t.Fatal(err)
				}
				r := onlyRecord(t, &recorder)
				if r.Op != f.op || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
					t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
				}
				if r.Requested != f.level || r.Delivered != f.level {
					t.Errorf("levels = %v and %v, want %v", r.Requested, r.Delivered, f.level)
				}
			})
			if f.badBody != "" {
				t.Run("a call that fails on its answer", func(t *testing.T) {
					t.Parallel()
					var recorder schemaflux.Recorder
					err := f.call(operationClient(answeringWith(f.badBody), recordingTo(&recorder)), "")
					wantError(t, err, schemaflux.ErrInvalidAnswer, f.op)
					r := onlyRecord(t, &recorder)
					if r.Op != f.op || r.Err != err || r.Requests != 1 {
						t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
					}
				})
			}
			t.Run("a call the provider fails", func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				var model schemafluxtest.Provider
				model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited})
				err := f.call(operationClient(&model, recordingTo(&recorder)), "")
				wantError(t, err, schemaflux.ErrRateLimited, f.op)
				if r := onlyRecord(t, &recorder); r.Op != f.op || r.Err != err {
					t.Errorf("record = %q err %v", r.Op, r.Err)
				}
			})
			t.Run("a call refused before anything is sent", func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := answeringWith(f.body)
				err := f.refused(operationClient(model, recordingTo(&recorder)), "")
				wantError(t, err, f.refusal(), f.op)
				r := onlyRecord(t, &recorder)
				if r.Op != f.op || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
					t.Errorf("record = %q err %v requests %d, %d sent", r.Op, r.Err, r.Requests, model.Calls())
				}
			})
			t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
				t.Parallel()
				var onClient, onContext schemaflux.Recorder
				ctx := schemaflux.WithRecorder(operationClient(answeringWith(f.body), recordingTo(&onClient)), &onContext)
				if err := f.call(ctx, ""); err != nil {
					t.Fatal(err)
				}
				if onlyRecord(t, &onClient).Op != f.op || onlyRecord(t, &onContext).Op != f.op {
					t.Error("a recorder was not given the Record")
				}
			})
		})
	}
}

func TestTextFormsUnderAFloorAreRefusedBeforeAnythingIsSentOrDeliveredAtTheirLevel(t *testing.T) {
	t.Parallel()
	floors := []schemaflux.ContractLevel{
		schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaConstrained,
		schemaflux.ContractSchemaAndInvariantChecked, schemaflux.ContractEvidenceChecked,
	}
	for _, f := range textForms {
		for _, floor := range floors {
			for _, withRecorder := range []bool{false, true} {
				name := f.op + " under " + floor.String()
				if withRecorder {
					name += ", with a recorder"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					var recorder schemaflux.Recorder
					model := namedModel(f.body)
					settings := []func(*schemaflux.Client){underFloor(floor)}
					if withRecorder {
						settings = append(settings, recordingTo(&recorder))
					}
					err := f.call(operationClient(model, settings...), "")
					refused := floor > f.level
					if refused {
						wantError(t, err, schemaflux.ErrPolicy, f.op)
						if model.Calls() != 0 {
							t.Errorf("%d requests were sent under a floor above the level", model.Calls())
						}
					} else if err != nil {
						t.Fatalf("a floor at the level refused the call: %v", err)
					}
					if withRecorder {
						r := onlyRecord(t, &recorder)
						if r.Op != f.op || (r.Err != nil) != refused || (refused && r.Requests != 0) {
							t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
						}
					}
				})
			}
		}
	}
}

func TestTextFormsUnderAnObserverAreOneOperationAndOneRecord(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()
	for _, f := range textForms {
		t.Run(f.op, func(t *testing.T) {
			seen = nil
			var recorder schemaflux.Recorder
			if err := f.call(operationClient(answeringWith(f.body), recordingTo(&recorder)), ""); err != nil {
				t.Fatal(err)
			}
			// A Detail twin names its operation scope after the plain form; that
			// there is exactly one scope for the call is what is held.
			if len(seen) != 1 {
				t.Errorf("the observer saw %v, want one operation", seen)
			}
			if onlyRecord(t, &recorder).Op != f.op {
				t.Error("the Record under an observer has the wrong operation")
			}
		})
	}
}

func TestTextFormsRefuseANilContextWithoutSendingAnything(t *testing.T) {
	t.Parallel()
	for _, f := range textForms {
		t.Run(f.op, func(t *testing.T) {
			t.Parallel()
			var nilContext context.Context
			e := wantError(t, f.call(nilContext, ""), schemaflux.ErrInvalidRequest, f.op)
			if e.Message != "the context is nil" {
				t.Errorf("message = %q", e.Message)
			}
		})
	}
}

func TestTextFormsWithACancelledContextReturnCanceledAndSendNothing(t *testing.T) {
	t.Parallel()
	for _, f := range textForms {
		t.Run(f.op, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(f.body)
			ctx, cancel := context.WithCancel(operationClient(model))
			cancel()
			err := f.call(ctx, "")
			wantError(t, err, schemaflux.ErrCanceled, f.op)
			if !errors.Is(err, context.Canceled) || model.Calls() != 0 {
				t.Errorf("err %v after %d requests", err, model.Calls())
			}
		})
	}
}

func TestTextFormsReturnAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	for _, f := range textForms {
		for name, kind := range map[string]schemaflux.Kind{
			"unavailable":  schemaflux.ErrUnavailable,
			"unauthorized": schemaflux.ErrUnauthorized,
			"timed out":    schemaflux.ErrTimeout,
			"refused":      schemaflux.ErrRefused,
		} {
			t.Run(f.op+" "+name, func(t *testing.T) {
				t.Parallel()
				var model schemafluxtest.Provider
				model.Fail(&schemaflux.Error{Kind: kind})
				wantError(t, f.call(operationClient(&model), ""), kind, f.op)
			})
		}
	}
}

func TestTextFormsKeepThePayloadOutOfEverythingTheyLeaveBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-5b1f27"
	for _, f := range textForms {
		run := func(ctx context.Context) error { return f.call(ctx, canary) }
		t.Run(f.op+" a call that succeeds", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(f.body) }, true, run)
		})
		t.Run(f.op+" an answer carrying the payload", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(canary + " " + canary) }, true, run)
		})
		t.Run(f.op+" a refused argument", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(f.body) }, true,
				func(ctx context.Context) error { return f.refused(ctx, canary) })
		})
		t.Run(f.op+" a provider that fails with a vendor message beneath its error", func(t *testing.T) {
			t.Parallel()
			vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
			requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
		})
		t.Run(f.op+" a foreign provider that fails with a plain error", func(t *testing.T) {
			t.Parallel()
			plain := errors.New("vendor said " + canary)
			var logs schemafluxtest.Logs
			var probe schemafluxtest.Provider
			probe.Fail(plain)
			_ = run(operationClient(&probe, loggingTo(&logs)))
			for _, entry := range logs.Entries() {
				line := entry.Message
				entry.Attrs(func(a slog.Attr) bool {
					line += " " + a.Value.String()
					return true
				})
				if strings.Contains(line, canary) {
					t.Skip("call-path log of a foreign provider's error: the internal call logs the provider's raw error text")
				}
			}
			requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(plain) }, false, run)
		})
	}
}

// A plain form sends the plain request in every state of the call: a recorder,
// an observer and a client with a policy and no floor never turn it into the
// request of its Detail form, which asks for JSON and for the model's confidence.
func TestPlainTextFormsSendThePlainRequestInEveryState(t *testing.T) {
	// Not parallel: an observer is process state.
	for _, f := range textForms {
		if !f.plain {
			continue
		}
		t.Run(f.op, func(t *testing.T) {
			reference := answeringWith(f.body)
			if err := f.call(operationClient(reference), ""); err != nil {
				t.Fatal(err)
			}
			want := reference.Last()
			if want.Format != "text" || want.Schema != nil {
				t.Fatalf("the plain request has format %q and a schema %v", want.Format, want.Schema != nil)
			}
			wantNoText(t, want, "confidence")

			sameRequest := func(state string, got schemaflux.Request) {
				t.Helper()
				if requestText(got) != requestText(want) || got.Format != want.Format || got.Schema != nil ||
					got.Temperature != want.Temperature || got.MaxTokens != want.MaxTokens {
					t.Errorf("%s changed what the model is asked", state)
				}
			}
			var recorder schemaflux.Recorder
			recorded := answeringWith(f.body)
			if err := f.call(operationClient(recorded, recordingTo(&recorder)), ""); err != nil {
				t.Fatal(err)
			}
			sameRequest("a recorder", recorded.Last())

			floorFree := namedModel(f.body)
			if err := f.call(operationClient(floorFree, underFloor(schemaflux.ContractPromptOnly)), ""); err != nil {
				t.Fatal(err)
			}
			sameRequest("a client with a policy and no floor", floorFree.Last())

			restore := telemetry.RegisterObserver(operationWatcher{finished: func(telemetry.Result) {}})
			defer restore()
			observed := answeringWith(f.body)
			if err := f.call(operationClient(observed), ""); err != nil {
				t.Fatal(err)
			}
			sameRequest("an observer", observed.Last())
		})
	}
}

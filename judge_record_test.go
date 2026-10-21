package schemaflux_test

import (
	"context"
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// The four judgments are held to one table, as the operations on lists are
// (collection_record_test.go): each adds one Record with the name of the
// operation, on success and on failure; each is delivered at the level of the
// contract table and is refused above it under a floor; each leaves the caller's
// text out of everything it leaves behind.

// judgeCase is one operation of the table.
type judgeCase struct {
	// op is the Op of the Record, level the contract level it is delivered at and
	// above the first level over it.
	op    string
	level schemaflux.ContractLevel
	above schemaflux.ContractLevel

	// reply is an answer the call accepts, and requests the number of requests a
	// call that is answered with it sends.
	reply    string
	requests int

	// run is the call, with the plain input.
	run func(ctx context.Context) error

	// refusals are calls refused before anything is sent, with the kind of each.
	refusals []collectionRefusal

	// leaky is an answer the call refuses, with the canary in it. carrying is the
	// call with the canary in every place the caller supplies text, and refused a
	// call with the canary in an option that is refused.
	leaky    func(canary string) string
	carrying func(canary string) func(ctx context.Context) error
	refused  func(canary string) func(ctx context.Context) error
}

// auditOnePolicyAnswer is a clean answer to an audit of one policy.
const auditOnePolicyAnswer = `{"findings":[],"policies":{"p1":[]}}`

func judgeCases() []judgeCase {
	return []judgeCase{
		{
			op: "validate", level: schemaflux.ContractSchemaConstrained, above: schemaflux.ContractSchemaAndInvariantChecked,
			reply: validateClean, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Validate(ctx, validateBadSignup, validateRules)
				return err
			},
			refusals: []collectionRefusal{
				{"nothing to validate against", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Validate(ctx, validateBadSignup)
					return err
				}},
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Validate(ctx, validateBadSignup, schemaflux.ValidateOptions{Rules: "x", FailOn: "fatal"})
					return err
				}},
			},
			leaky: func(canary string) string {
				return `{"errors":[{"field":"` + canary + `","message":"  "}],"warnings":[],"info":[],"confidence":2,"summary":"` + canary + `"}`
			},
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Validate(ctx, map[string]string{canary + "-key": "value " + canary}, schemaflux.ValidateOptions{
						Rules:       "rules " + canary,
						FieldRules:  map[string]string{canary + "-field": "plausible " + canary},
						SchemaHints: map[string]string{canary + "-hint": "hint " + canary},
						Options:     schemaflux.Options{Steering: "steer " + canary},
					})
					return err
				}
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Validate(ctx, map[string]string{canary: canary}, schemaflux.ValidateOptions{
						Deterministic: true, FailOn: canary, Rules: canary, FieldRules: map[string]string{canary: canary},
					})
					return err
				}
			},
		},
		{
			op: "verify", level: schemaflux.ContractSchemaAndInvariantChecked, above: schemaflux.ContractEvidenceChecked,
			reply: verifyClean, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Verify(ctx, "the text")
				return err
			},
			refusals: []collectionRefusal{
				{"every check turned off", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Verify(ctx, "the text", schemaflux.VerifyOptions{SkipFacts: true, SkipLogic: true, SkipConsistency: true})
					return err
				}},
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Verify(ctx, "the text", schemaflux.VerifyOptions{MinConfidence: 2})
					return err
				}},
			},
			leaky: func(canary string) string {
				return `{"summary":"` + canary + `","overall_confidence":0.9,"trust_score":0.8,"claims":[{"claim":"` + canary + `","verdict":"` + canary + `","confidence":0.9}],"logic_issues":[],"consistency_issues":[]}`
			},
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Verify(ctx, "the text "+canary, schemaflux.VerifyOptions{
						Sources:        []any{"source " + canary, map[string]string{canary + "-key": canary}},
						TrustedSources: []string{"trusted " + canary},
						Domain:         "domain " + canary,
						Options:        schemaflux.Options{Steering: "steer " + canary},
					})
					return err
				}
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Verify(ctx, canary, schemaflux.VerifyOptions{Strictness: canary, Domain: canary, TrustedSources: []string{canary}})
					return err
				}
			},
		},
		{
			op: "audit", level: schemaflux.ContractSchemaAndInvariantChecked, above: schemaflux.ContractEvidenceChecked,
			reply: auditOnePolicyAnswer, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Audit(ctx, auditCustomer, schemaflux.AuditOptions{Policies: []string{"no plain text passwords"}})
				return err
			},
			refusals: []collectionRefusal{
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Audit(ctx, auditCustomer, schemaflux.AuditOptions{Threshold: 2})
					return err
				}},
				{"a negative threshold", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Audit(ctx, auditCustomer, schemaflux.AuditOptions{Threshold: -1})
					return err
				}},
			},
			leaky: func(canary string) string {
				return `{"findings":[{"category":"` + canary + `","severity":0.5,"issue":"` + canary + `","evidence":"` + canary + `"}],"policies":{"p1":[]}}`
			},
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Audit(ctx, map[string]string{canary + "-key": "value " + canary}, schemaflux.AuditOptions{
						Policies: []string{"policy " + canary},
						Options:  schemaflux.Options{Steering: "steer " + canary},
					})
					return err
				}
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Audit(ctx, map[string]string{canary: canary}, schemaflux.AuditOptions{
						Policies: []string{canary}, Categories: []string{canary}, Threshold: 5,
					})
					return err
				}
			},
		},
		{
			op: "critique", level: schemaflux.ContractJSONWellFormed, above: schemaflux.ContractSchemaConstrained,
			reply: critiqueReport("[]"), requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Critique(ctx, "the essay", critiqueCriteria)
				return err
			},
			refusals: []collectionRefusal{
				{"no criteria and no rubric", schemaflux.ErrInvalidRequest, func(ctx context.Context) error {
					_, err := schemaflux.Critique(ctx, "the essay", nil)
					return err
				}},
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Critique(ctx, "the essay", critiqueCriteria, schemaflux.CritiqueOptions{Style: "savage"})
					return err
				}},
			},
			leaky: func(canary string) string {
				return `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6},"issues":[` +
					`{"criterion":"` + canary + `","severity":"` + canary + `","description":"` + canary + `"}],"summary":"` + canary + `","top_priorities":["` + canary + `"]}`
			},
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Critique(ctx, map[string]string{canary + "-key": "value " + canary}, critiqueCriteria, schemaflux.CritiqueOptions{
						Domain:   "domain " + canary,
						Audience: "audience " + canary,
						Options:  schemaflux.Options{Steering: "steer " + canary},
					})
					return err
				}
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Critique(ctx, canary, []string{canary}, schemaflux.CritiqueOptions{
						Style: canary, SeverityFilter: canary, Rubric: map[string]string{canary: canary}, Domain: canary,
					})
					return err
				}
			},
		},
	}
}

func TestJudgmentsAddOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	for _, tc := range judgeCases() {
		t.Run(tc.op+" succeeds", func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			if err := tc.run(operationClient(answeringWith(tc.reply), recordingTo(&recorder))); err != nil {
				t.Fatal(err)
			}
			r := onlyRecord(t, &recorder)
			if r.Op != tc.op || r.Err != nil || r.Requests != tc.requests || r.Provider != "test" {
				t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
			}
			if r.Requested != tc.level {
				t.Errorf("requested level = %v, want %v", r.Requested, tc.level)
			}
		})
		t.Run(tc.op+" fails on its answer", func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			err := tc.run(operationClient(answeringWith("not an answer"), recordingTo(&recorder)))
			wantError(t, err, schemaflux.ErrInvalidAnswer, tc.op)
			r := onlyRecord(t, &recorder)
			if r.Op != tc.op || r.Err != err || r.Requests < 1 {
				t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
			}
		})
		t.Run(tc.op+" fails at the provider", func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
			err := tc.run(operationClient(&model, recordingTo(&recorder)))
			wantError(t, err, schemaflux.ErrUnavailable, tc.op)
			if r := onlyRecord(t, &recorder); r.Op != tc.op || r.Err != err {
				t.Errorf("record = %q err %v", r.Op, r.Err)
			}
		})
		for _, refusal := range tc.refusals {
			t.Run(tc.op+" is refused for "+refusal.name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := answeringWith(tc.reply)
				err := refusal.run(operationClient(model, recordingTo(&recorder)))
				wantError(t, err, refusal.kind, tc.op)
				r := onlyRecord(t, &recorder)
				if r.Op != tc.op || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
					t.Errorf("record = %q err %v requests %d, %d requests sent", r.Op, r.Err, r.Requests, model.Calls())
				}
			})
		}
		t.Run(tc.op+" gives a recorder on the context and one on the client each the Record", func(t *testing.T) {
			t.Parallel()
			var onClient, onContext schemaflux.Recorder
			ctx := schemaflux.WithRecorder(operationClient(answeringWith(tc.reply), recordingTo(&onClient)), &onContext)
			if err := tc.run(ctx); err != nil {
				t.Fatal(err)
			}
			if onlyRecord(t, &onClient).Op != tc.op || onlyRecord(t, &onContext).Op != tc.op {
				t.Error("a recorder was not given the Record")
			}
		})
	}
}

func TestJudgmentsUnderAFloorAreHeldToTheLevelOfTheContractTable(t *testing.T) {
	t.Parallel()
	for _, op := range judgeCases() {
		floors := []struct {
			name    string
			floor   schemaflux.ContractLevel
			refused bool
		}{
			{"the level itself is met", op.level, false},
			{"the lowest floor above prompt only is met", schemaflux.ContractJSONWellFormed, false},
			{"the level above is not", op.above, true},
		}
		for _, tc := range floors {
			for _, withRecorder := range []bool{false, true} {
				name := op.op + ": " + tc.name
				if withRecorder {
					name += ", with a recorder"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					var recorder schemaflux.Recorder
					model := namedModel(op.reply)
					settings := []func(*schemaflux.Client){underFloor(tc.floor)}
					if withRecorder {
						settings = append(settings, recordingTo(&recorder))
					}
					err := op.run(operationClient(model, settings...))
					if tc.refused {
						wantError(t, err, schemaflux.ErrPolicy, op.op)
						if model.Calls() != 0 {
							t.Errorf("%d requests were sent", model.Calls())
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if withRecorder {
						r := onlyRecord(t, &recorder)
						if r.Op != op.op || (r.Err != nil) != tc.refused || r.Requested != op.level {
							t.Errorf("record = %q err %v requested %v, want %v", r.Op, r.Err, r.Requested, op.level)
						}
						if tc.refused && r.Requests != 0 {
							t.Errorf("a refused call recorded %d requests", r.Requests)
						}
					}
				})
			}
		}
	}
}

func TestValidateDeterministicUnderAFloorIsHeldToTheLevelOfValidateAndSendsNothing(t *testing.T) {
	t.Parallel()
	opts := schemaflux.ValidateOptions{Deterministic: true, FieldRules: map[string]string{"email": "email"}}
	for _, tc := range []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor of schema constrained is met", schemaflux.ContractSchemaConstrained, false},
		{"a floor of schema and invariants is not", schemaflux.ContractSchemaAndInvariantChecked, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := namedModel(validateClean)
			res, err := schemaflux.Validate(operationClient(model, underFloor(tc.floor), recordingTo(&recorder)), validateBadSignup, opts)
			if tc.refused {
				wantError(t, err, schemaflux.ErrPolicy, "validate")
			} else if err != nil || !res.DeterministicOnly {
				t.Fatalf("got %+v, %v", res, err)
			}
			if r := onlyRecord(t, &recorder); r.Op != "validate" || r.Requests != 0 || model.Calls() != 0 {
				t.Errorf("record = %q requests %d, %d sent", r.Op, r.Requests, model.Calls())
			}
		})
	}
}

func TestValidateDeterministicAddsOneRecordWithNoRequests(t *testing.T) {
	t.Parallel()
	var recorder schemaflux.Recorder
	res, err := schemaflux.Validate(operationClient(answeringWith(validateClean), recordingTo(&recorder)), validateBadSignup,
		schemaflux.ValidateOptions{Deterministic: true, FieldRules: map[string]string{"email": "email"}})
	if err != nil || !res.DeterministicOnly {
		t.Fatalf("got %+v, %v", res, err)
	}
	if r := onlyRecord(t, &recorder); r.Op != "validate" || r.Requests != 0 || r.Err != nil {
		t.Errorf("record = %q requests %d err %v", r.Op, r.Requests, r.Err)
	}
}

func TestJudgmentsUnderAnObserverSendTheSameRequestAndRecordOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	for _, tc := range judgeCases() {
		seen = nil
		plain := answeringWith(tc.reply)
		if err := tc.run(operationClient(plain)); err != nil {
			t.Fatalf("%s: %v", tc.op, err)
		}
		var recorder schemaflux.Recorder
		watched := answeringWith(tc.reply)
		if err := tc.run(operationClient(watched, recordingTo(&recorder))); err != nil {
			t.Fatalf("%s: %v", tc.op, err)
		}
		if requestText(plain.Last()) != requestText(watched.Last()) {
			t.Errorf("%s: an observer changed what the model is asked", tc.op)
		}
		if onlyRecord(t, &recorder).Op != tc.op {
			t.Errorf("%s: the Record under an observer has the wrong operation", tc.op)
		}
		if len(seen) != 2 {
			t.Errorf("%s: the observer saw %v, want one operation for each call", tc.op, seen)
		}
	}
}

func TestJudgmentsKeepThePayloadOutOfEverythingTheyLeaveBehind(t *testing.T) {
	t.Parallel()
	const payload = "CANARY-b83d27"
	for _, tc := range judgeCases() {
		run := tc.carrying(payload)
		t.Run(tc.op+": a call that succeeds", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, payload, func(p *schemafluxtest.Provider) { p.Reply(tc.reply) }, true, run)
		})
		t.Run(tc.op+": an answer that is refused", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, payload, func(p *schemafluxtest.Provider) { p.Reply(tc.leaky(payload)) }, true, run)
		})
		t.Run(tc.op+": an answer that is text", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, payload, func(p *schemafluxtest.Provider) { p.Reply("the answer is " + payload) }, true, run)
		})
		t.Run(tc.op+": a provider that fails with a vendor message beneath its error", func(t *testing.T) {
			t.Parallel()
			vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + payload)}
			requireNoPayload(t, payload, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
		})
		t.Run(tc.op+": a provider that fails with a plain error", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, payload, func(p *schemafluxtest.Provider) { p.Fail(errors.New("vendor said " + payload)) }, false, run)
		})
		t.Run(tc.op+": a call refused for an option holding the canary", func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, payload, func(p *schemafluxtest.Provider) { p.Reply(tc.reply) }, true, tc.refused(payload))
		})
	}
}

func TestValidateOfAMapIsDeliveredAsWellFormedWhenACorrectionIsAskedFor(t *testing.T) {
	t.Parallel()
	rules := schemaflux.ValidateOptions{Rules: "every value is set"}
	wantLevelFollowsTheType(t, levelCall{
		op: "validate", mapReply: validateClean, structReply: validateClean,
		intoMap: func(ctx context.Context) error {
			_, err := schemaflux.Validate(ctx, map[string]string{"email": "ada@example.com"}, rules)
			return err
		},
		intoStruct: func(ctx context.Context) error {
			_, err := schemaflux.Validate(ctx, validateBadSignup, rules)
			return err
		},
	})
	// Without the correction the answer holds no value of the type, so the
	// schema is the same whatever the type is and a map has it.
	t.Run("a map with no correction asked for is schema constrained", func(t *testing.T) {
		var recorder schemaflux.Recorder
		model := namedModel(validateClean)
		_, err := schemaflux.Validate(operationClient(model, recordingTo(&recorder), underFloor(schemaflux.ContractSchemaConstrained)),
			map[string]string{"email": "ada@example.com"}, schemaflux.ValidateOptions{Rules: "every value is set", OmitCorrection: true})
		if err != nil {
			t.Fatal(err)
		}
		if model.Last().Schema == nil {
			t.Error("no schema was sent")
		}
		if r := onlyRecord(t, &recorder); r.Requested != schemaflux.ContractSchemaConstrained || r.Delivered != schemaflux.ContractSchemaConstrained {
			t.Errorf("requested %v delivered %v", r.Requested, r.Delivered)
		}
	})
	t.Run("a deterministic call into a map is held to the level of Validate", func(t *testing.T) {
		model := namedModel(validateClean)
		res, err := schemaflux.Validate(operationClient(model, underFloor(schemaflux.ContractSchemaConstrained)),
			map[string]string{"email": "ada@example.com"}, schemaflux.ValidateOptions{Deterministic: true, FieldRules: map[string]string{"email": "email"}})
		if err != nil || !res.DeterministicOnly || model.Calls() != 0 {
			t.Errorf("got %+v, %v, %d sent", res, err, model.Calls())
		}
	})
}

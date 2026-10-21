package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// The five operations that work on lists the caller gave are held to one table:
// each adds one Record with the name of the operation, on success and on failure;
// each is delivered at the level the contract table gives it and is refused above
// it under a floor; each leaves the caller's text out of everything it leaves
// behind.

// collectionJSON writes v as JSON, for a test that holds a result's tags.
func collectionJSON(v any) (string, error) {
	out, err := json.Marshal(v)
	return string(out), err
}

// collectionCase is one operation of the table.
type collectionCase struct {
	// op is the Op of the Record.
	op string

	// reply is an answer the call accepts, and requests the number of requests a
	// call that is answered with it sends.
	reply    string
	requests int

	// run is the call, with the plain input.
	run func(ctx context.Context) error

	// refusals are calls refused before anything is sent: an argument that cannot be
	// used and an option that cannot, with the kind of each.
	refusals []collectionRefusal

	// leaky is an answer the call refuses, with the canary in it.
	leaky func(canary string) string

	// carrying is the call with the canary in every place the caller supplies text,
	// and refused is a call with the canary in an option that is refused.
	carrying func(canary string) func(ctx context.Context) error
	refused  func(canary string) func(ctx context.Context) error
}

type collectionRefusal struct {
	name string
	kind schemaflux.Kind
	run  func(ctx context.Context) error
}

func collectionCases() []collectionCase {
	return []collectionCase{
		{
			op: "choose", reply: chooseSecondAnswer, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Choose(ctx, chooseOptions)
				return err
			},
			refusals: []collectionRefusal{
				{"no options", schemaflux.ErrInvalidRequest, func(ctx context.Context) error {
					_, err := schemaflux.Choose(ctx, []string(nil))
					return err
				}},
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Choose(ctx, chooseOptions, schemaflux.ChooseOptions{Strategy: "random"})
					return err
				}},
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Choose(ctx, []string{canary, "b"}, schemaflux.ChooseOptions{Strategy: canary, Criteria: []string{canary}})
					return err
				}
			},
			leaky: func(canary string) string { return `{"id":"` + canary + `"}` },
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Choose(ctx, []map[string]string{{canary + "-key": "value " + canary}, {"k": canary}},
						schemaflux.ChooseOptions{Criteria: []string{"criterion " + canary}, Options: schemaflux.Options{Steering: "steer " + canary}})
					return err
				}
			},
		},
		{
			op: "filter", reply: filterKeepsFirstAndThird, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Filter(ctx, filterItems, "short")
				return err
			},
			refusals: []collectionRefusal{
				{"blank criteria", schemaflux.ErrInvalidRequest, func(ctx context.Context) error {
					_, err := schemaflux.Filter(ctx, filterItems, " ")
					return err
				}},
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Filter(ctx, filterItems, "short", schemaflux.FilterOptions{MinConfidence: 2})
					return err
				}},
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Filter(ctx, []string{canary, "b"}, canary, schemaflux.FilterOptions{MinConfidence: 2})
					return err
				}
			},
			leaky: func(canary string) string { return `{"ids":["` + canary + `"]}` },
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Filter(ctx, []map[string]string{{canary + "-key": "value " + canary}, {"k": canary}}, "criteria "+canary,
						schemaflux.FilterOptions{Options: schemaflux.Options{Steering: "steer " + canary}})
					return err
				}
			},
		},
		{
			op: "sort", reply: sortThirdFirstSecond, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Sort(ctx, sortItems, "urgent")
				return err
			},
			refusals: []collectionRefusal{
				{"blank criteria", schemaflux.ErrInvalidRequest, func(ctx context.Context) error {
					_, err := schemaflux.Sort(ctx, sortItems, "")
					return err
				}},
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Sort(ctx, sortItems, "urgent", schemaflux.SortOptions{Direction: "sideways"})
					return err
				}},
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Sort(ctx, []string{canary, "b"}, canary, schemaflux.SortOptions{Direction: canary, SecondaryCriteria: []string{canary}})
					return err
				}
			},
			leaky: func(canary string) string { return `{"ids":["` + canary + `"],"rank_score":"` + canary + `"}` },
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Sort(ctx, []map[string]string{{canary + "-key": "value " + canary}, {"k": canary}}, "criteria "+canary,
						schemaflux.SortOptions{SecondaryCriteria: []string{"second " + canary}, ComparisonLogic: "logic " + canary, Options: schemaflux.Options{Steering: "steer " + canary}})
					return err
				}
			},
		},
		{
			op: "rank", reply: rankScored, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.Rank(ctx, rankItems, "pooling")
				return err
			},
			refusals: []collectionRefusal{
				{"a blank query", schemaflux.ErrInvalidRequest, func(ctx context.Context) error {
					_, err := schemaflux.Rank(ctx, rankItems, "")
					return err
				}},
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.Rank(ctx, rankItems, "pooling", schemaflux.RankOptions{TopK: -1})
					return err
				}},
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Rank(ctx, []string{canary, "b"}, canary, schemaflux.RankOptions{
						Factors: []string{"a"}, FactorWeights: map[string]float64{canary: 1}, BoostFields: map[string]float64{canary: 1},
					})
					return err
				}
			},
			leaky: func(canary string) string {
				return `{"rankings":[{"index":0,"score":0.5,"explanation":"` + canary + `"},{"index":"` + canary + `","score":1}]}`
			},
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.Rank(ctx, []map[string]string{{canary + "-key": "value " + canary}, {"k": canary}}, "query "+canary, schemaflux.RankOptions{
						Factors:        []string{canary + "-factor"},
						FactorWeights:  map[string]float64{canary + "-factor": 1},
						BoostFields:    map[string]float64{canary + "-boost": 2},
						PenalizeFields: map[string]float64{canary + "-penalty": 2},
						Options:        schemaflux.Options{Steering: "steer " + canary},
					})
					return err
				}
			},
		},
		{
			op: "semanticmatch", reply: matchTwoPairs, requests: 1,
			run: func(ctx context.Context) error {
				_, err := schemaflux.SemanticMatch(ctx, matchSources, matchTargets)
				return err
			},
			refusals: []collectionRefusal{
				{"a bad option", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.SemanticMatch(ctx, matchSources, matchTargets, schemaflux.SemanticMatchOptions{Strategy: "closest"})
					return err
				}},
				{"a bad option beside an empty list", schemaflux.ErrConfiguration, func(ctx context.Context) error {
					_, err := schemaflux.SemanticMatch(ctx, []string(nil), matchTargets, schemaflux.SemanticMatchOptions{Threshold: 2})
					return err
				}},
			},
			refused: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.SemanticMatch(ctx, []string{canary}, []string{canary}, schemaflux.SemanticMatchOptions{
						Strategy: canary, Criteria: canary, Fields: []string{canary}, FieldWeights: map[string]float64{canary: 1},
					})
					return err
				}
			},
			leaky: func(canary string) string {
				return `{"matches":[{"source_index":0,"target_index":9,"score":0.9,"explanation":"` + canary + `"}],"unmatched_sources":["` + canary + `"],"unmatched_targets":[]}`
			},
			carrying: func(canary string) func(ctx context.Context) error {
				return func(ctx context.Context) error {
					_, err := schemaflux.SemanticMatch(ctx, []map[string]string{{canary + "-key": "value " + canary}}, []string{"target " + canary, "other"},
						schemaflux.SemanticMatchOptions{
							Criteria:     "criteria " + canary,
							Fields:       []string{canary + "-field"},
							FieldWeights: map[string]float64{canary + "-field": 2},
							Options:      schemaflux.Options{Steering: "steer " + canary},
						})
					return err
				}
			},
		},
	}
}

func TestCollectionOperationsAddOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	for _, tc := range collectionCases() {
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
			if r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed {
				t.Errorf("levels = %v and %v, want json well-formed", r.Requested, r.Delivered)
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

func TestCollectionOperationsUnderAFloorAreHeldToWellFormedJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor of well-formed JSON is met", schemaflux.ContractJSONWellFormed, false},
		{"a floor of schema constrained is not: no schema is declared", schemaflux.ContractSchemaConstrained, true},
		{"a floor of schema and invariants is not", schemaflux.ContractSchemaAndInvariantChecked, true},
	}
	for _, op := range collectionCases() {
		for _, tc := range cases {
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
						if r.Op != op.op || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractJSONWellFormed {
							t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
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

func TestCollectionOperationsUnderAnObserverSendTheSameRequestAndRecordOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	for _, tc := range collectionCases() {
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

func TestCollectionOperationsKeepThePayloadOutOfEverythingTheyLeaveBehind(t *testing.T) {
	t.Parallel()
	const payload = "CANARY-5e1c80"
	for _, tc := range collectionCases() {
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

package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// levelCase is one call of a function of extract.go, complete.go or describe.go
// with its defaults, for the tests of what every one of them delivers: the
// Record it adds, the level it is held to under a floor, and that an observer
// changes nothing of what the model is asked. The functions have different
// options types, so a case builds its own from the common settings it is given.
type levelCase struct {
	// name names the case in a test. op is the Op of the Record the call adds
	// (A3) and level the level of its row in the table of contract levels (R31).
	name  string
	op    string
	level schemaflux.ContractLevel

	// reply is the body the scripted model answers with, and requests is how
	// many requests a call that succeeds sends.
	reply    string
	requests int

	// call makes the call with the settings given and returns its error.
	call func(ctx context.Context, common schemaflux.Options) error
}

// levelCases are the cases of every function of the three files. A function
// whose level depends on its type argument has a case for each level.
func levelCases() []levelCase {
	return []levelCase{
		{
			name: "Transform", op: "transform", level: schemaflux.ContractSchemaConstrained, reply: transformAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Transform[transformTarget](ctx, transformSource{First: "Ada", Last: "Lovelace", ID: 7}, schemaflux.TransformOptions{Options: common})
				return err
			},
		},
		{
			name: "Generate into a string", op: "generate", level: schemaflux.ContractPromptOnly, reply: "A short bio.", requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Generate[string](ctx, "Write a two-line bio", schemaflux.GenerateOptions{Options: common})
				return err
			},
		},
		{
			name: "Generate into a struct", op: "generate", level: schemaflux.ContractSchemaConstrained, reply: generateAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Generate[generatedMeal](ctx, "A vegetarian dinner", schemaflux.GenerateOptions{Options: common})
				return err
			},
		},
		{
			name: "Infer", op: "infer", level: schemaflux.ContractJSONWellFormed, reply: inferAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Infer(ctx, inferredContact{Email: "ada@example.com"}, schemaflux.InferOptions{Options: common})
				return err
			},
		},
		{
			name: "Parse through the model", op: "parse", level: schemaflux.ContractJSONWellFormed, reply: parseAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Parse[parsedPerson](ctx, parseUnreadable, schemaflux.ParseOptions{Options: common, AllowLLMFallback: true})
				return err
			},
		},
		{
			name: "Parse that the Go parsers read", op: "parse", level: schemaflux.ContractJSONWellFormed, reply: "{}", requests: 0,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Parse[parsedPerson](ctx, `{"name":"Ada","age":36}`, schemaflux.ParseOptions{Options: common})
				return err
			},
		},
		{
			name: "Complete", op: "complete", level: schemaflux.ContractPromptOnly, reply: "jumps over", requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Complete(ctx, "The quick brown fox", schemaflux.CompleteOptions{Options: common})
				return err
			},
		},
		{
			name: "CompleteField", op: "completefield", level: schemaflux.ContractPromptOnly, reply: "how doctors read scans.", requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.CompleteField(ctx, samplePost(), "Body", schemaflux.CompleteOptions{Options: common})
				return err
			},
		},
		{
			name: "Suggest", op: "suggest", level: schemaflux.ContractJSONWellFormed, reply: suggestAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Suggest[string](ctx, "My invoice is wrong", schemaflux.SuggestOptions{Options: common})
				return err
			},
		},
		{
			name: "Annotate", op: "annotate", level: schemaflux.ContractJSONWellFormed, reply: annotateAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Annotate(ctx, annotateInput, schemaflux.AnnotateOptions{Options: common})
				return err
			},
		},
		{
			name: "Explain", op: "explain", level: schemaflux.ContractJSONWellFormed, reply: explainAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Explain(ctx, queryPlan{Table: "orders"}, schemaflux.ExplainOptions{Options: common})
				return err
			},
		},
		{
			name: "Question", op: "question", level: schemaflux.ContractSchemaConstrained, reply: questionAnswer, requests: 1,
			call: func(ctx context.Context, common schemaflux.Options) error {
				_, err := schemaflux.Question[string](ctx, salesReport{Region: "EU"}, questionAsked, schemaflux.QuestionOptions{Options: common})
				return err
			},
		},
	}
}

func TestEveryOperationOfTheThreeFilesIsHeldToTheLevelOfTheTable(t *testing.T) {
	t.Parallel()
	for _, tc := range levelCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rule, known := ops.ContractLevelOf(tc.op)
			if !known {
				t.Fatalf("the internal table has no row for %q", tc.op)
			}
			if got := schemaflux.ContractLevel(rule.Level); got != tc.level && !rule.Computed {
				t.Errorf("the internal table holds level %v for %q, the case expects %v", got, tc.op, tc.level)
			}
		})
	}
}

func TestEveryOperationOfTheThreeFilesAddsOneRecordWithItsNameAndLevel(t *testing.T) {
	t.Parallel()
	for _, tc := range levelCases() {
		t.Run(tc.name+", on success", func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := answeringWith(tc.reply)
			if err := tc.call(operationClient(model, recordingTo(&recorder)), schemaflux.Options{}); err != nil {
				t.Fatal(err)
			}
			r := onlyRecord(t, &recorder)
			if r.Op != tc.op || r.Err != nil || r.Requests != tc.requests || model.Calls() != tc.requests {
				t.Errorf("record = %q err %v requests %d (%d sent), want %q, no error, %d", r.Op, r.Err, r.Requests, model.Calls(), tc.op, tc.requests)
			}
			if r.Delivered != tc.level {
				t.Errorf("delivered %v, want %v", r.Delivered, tc.level)
			}
		})
		if tc.requests == 0 {
			// A call that sends nothing cannot be failed by the provider.
			continue
		}
		t.Run(tc.name+", when the provider fails", func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited})
			err := tc.call(operationClient(&model, recordingTo(&recorder)), schemaflux.Options{})
			wantError(t, err, schemaflux.ErrRateLimited, tc.op)
			r := onlyRecord(t, &recorder)
			if r.Op != tc.op || r.Err != err || r.Requests == 0 {
				t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
			}
		})
		t.Run(tc.name+", when a setting is refused", func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := answeringWith(tc.reply)
			err := tc.call(operationClient(model, recordingTo(&recorder)), schemaflux.Options{MaxOutputTokens: -1})
			wantError(t, err, schemaflux.ErrConfiguration, tc.op)
			r := onlyRecord(t, &recorder)
			if r.Op != tc.op || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
				t.Errorf("record = %q err %v requests %d (%d sent)", r.Op, r.Err, r.Requests, model.Calls())
			}
		})
		t.Run(tc.name+", with a recorder on the context and one on the client", func(t *testing.T) {
			t.Parallel()
			var onClient, onContext schemaflux.Recorder
			ctx := schemaflux.WithRecorder(operationClient(answeringWith(tc.reply), recordingTo(&onClient)), &onContext)
			if err := tc.call(ctx, schemaflux.Options{}); err != nil {
				t.Fatal(err)
			}
			if onlyRecord(t, &onClient).Op != tc.op || onlyRecord(t, &onContext).Op != tc.op {
				t.Error("a recorder was not given the Record")
			}
		})
	}
}

func TestEveryOperationOfTheThreeFilesIsRefusedUnderAFloorAboveItsLevelAndOnlyThen(t *testing.T) {
	t.Parallel()
	floors := []schemaflux.ContractLevel{
		schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaConstrained,
		schemaflux.ContractSchemaAndInvariantChecked, schemaflux.ContractFullyGoverned,
	}
	for _, tc := range levelCases() {
		for _, floor := range floors {
			for _, withRecorder := range []bool{false, true} {
				name := tc.name + ", floor " + floor.String()
				if withRecorder {
					name += ", with a recorder"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					var recorder schemaflux.Recorder
					model := namedModel(tc.reply)
					settings := []func(*schemaflux.Client){underFloor(floor)}
					if withRecorder {
						settings = append(settings, recordingTo(&recorder))
					}
					err := tc.call(operationClient(model, settings...), schemaflux.Options{})
					if floor > tc.level {
						wantError(t, err, schemaflux.ErrPolicy, tc.op)
						if model.Calls() != 0 {
							t.Errorf("%d requests were sent to a call the floor refuses", model.Calls())
						}
					} else if err != nil {
						t.Fatalf("a call at level %v was refused under a floor of %v: %v", tc.level, floor, err)
					}
					if withRecorder {
						r := onlyRecord(t, &recorder)
						if r.Op != tc.op || (r.Err != nil) != (floor > tc.level) {
							t.Errorf("record = %q err %v", r.Op, r.Err)
						}
						if floor > tc.level && r.Requests != 0 {
							t.Errorf("a refused call recorded %d requests", r.Requests)
						}
					}
				})
			}
		}
	}
}

func TestEveryOperationOfTheThreeFilesRefusesANilContext(t *testing.T) {
	t.Parallel()
	for _, tc := range levelCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var nilContext context.Context
			err := tc.call(nilContext, schemaflux.Options{})
			e := wantError(t, err, schemaflux.ErrInvalidRequest, tc.op)
			if e.Message != "the context is nil" {
				t.Errorf("message = %q", e.Message)
			}
		})
	}
}

func TestEveryOperationOfTheThreeFilesUnderAnObserverAsksTheSameAndFinishesOneScope(t *testing.T) {
	// Not parallel: an observer is process state, and it changes the path of
	// every call that runs while it is installed.
	var scopes int
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			scopes++
		}
	}})
	defer restore()

	for _, tc := range levelCases() {
		t.Run(tc.name, func(t *testing.T) {
			scopes = 0
			plain := answeringWith(tc.reply)
			if err := tc.call(operationClient(plain), schemaflux.Options{}); err != nil {
				t.Fatal(err)
			}
			if scopes != 1 {
				t.Errorf("%d operation scopes finished for one call, want 1", scopes)
			}
			if tc.requests == 0 {
				return
			}
			var recorder schemaflux.Recorder
			watched := answeringWith(tc.reply)
			if err := tc.call(operationClient(watched, recordingTo(&recorder)), schemaflux.Options{}); err != nil {
				t.Fatal(err)
			}
			if requestText(plain.Last()) != requestText(watched.Last()) || plain.Last().Schema == nil != (watched.Last().Schema == nil) {
				t.Error("an observer or a recorder changed what the model is asked")
			}
			if onlyRecord(t, &recorder).Op != tc.op {
				t.Error("the Record under an observer has the wrong operation")
			}
		})
	}
}

// sentTo returns the text a request shows the model, for a failure message that
// must not print a whole prompt.
func sentTo(model *schemafluxtest.Provider) string {
	return excerpt(strings.ReplaceAll(requestText(model.Last()), "\n", " "))
}

// marshaledText is v as the JSON a caller would log or store.
func marshaledText(v any) (string, error) {
	encoded, err := json.Marshal(v)
	return string(encoded), err
}

func TestAnOptionNoCallCanUseIsRefusedBeforeTheFloorIsLookedAt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		op   string
		call func(ctx context.Context) error
	}{
		{"a negative output ceiling", "transform", func(ctx context.Context) error {
			_, err := schemaflux.Transform[transformTarget](ctx, transformSource{}, schemaflux.TransformOptions{Options: schemaflux.Options{MaxOutputTokens: -1}})
			return err
		}},
		{"a merge strategy of no kind", "transform", func(ctx context.Context) error {
			_, err := schemaflux.Transform[transformTarget](ctx, transformSource{}, schemaflux.TransformOptions{MergeStrategy: "upsert"})
			return err
		}},
		{"an empty custom delimiter", "parse", func(ctx context.Context) error {
			_, err := schemaflux.Parse[parsedPerson](ctx, `{"name":"Ada","age":36}`, schemaflux.ParseOptions{CustomDelimiters: []string{""}})
			return err
		}},
		{"a negative MaxLength", "complete", func(ctx context.Context) error {
			_, err := schemaflux.Complete(ctx, "x", schemaflux.CompleteOptions{MaxLength: -1})
			return err
		}},
		{"a negative MaxLength of a field", "completefield", func(ctx context.Context) error {
			_, err := schemaflux.CompleteField(ctx, samplePost(), "Body", schemaflux.CompleteOptions{MaxLength: -1})
			return err
		}},
		{"a negative TopN", "suggest", func(ctx context.Context) error {
			_, err := schemaflux.Suggest[string](ctx, "x", schemaflux.SuggestOptions{TopN: -1})
			return err
		}},
		{"a strategy of no kind", "suggest", func(ctx context.Context) error {
			_, err := schemaflux.Suggest[string](ctx, "x", schemaflux.SuggestOptions{Strategy: "bogus"})
			return err
		}},
		{"a MinConfidence above 1", "annotate", func(ctx context.Context) error {
			_, err := schemaflux.Annotate(ctx, "x", schemaflux.AnnotateOptions{MinConfidence: 2})
			return err
		}},
		{"a depth above 4", "explain", func(ctx context.Context) error {
			_, err := schemaflux.Explain(ctx, queryPlan{}, schemaflux.ExplainOptions{Depth: 5})
			return err
		}},
		{"an audience of no kind", "explain", func(ctx context.Context) error {
			_, err := schemaflux.Explain(ctx, queryPlan{}, schemaflux.ExplainOptions{Audience: "pirates"})
			return err
		}},
		{"a tier that is not one", "question", func(ctx context.Context) error {
			_, err := schemaflux.Question[string](ctx, 1, "Why?", schemaflux.QuestionOptions{Options: schemaflux.Options{Tier: 9}})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := namedModel("{}")
			err := tc.call(operationClient(model, underFloor(schemaflux.ContractFullyGoverned), recordingTo(&recorder)))
			wantError(t, err, schemaflux.ErrConfiguration, tc.op)
			if r := onlyRecord(t, &recorder); r.Op != tc.op || r.Requests != 0 || model.Calls() != 0 {
				t.Errorf("record = %q requests %d (%d sent)", r.Op, r.Requests, model.Calls())
			}
		})
	}
}

// foreignProviderError is the error of a provider written outside the library
// that returns a bare error holding its vendor's text.
type foreignProviderError struct{ text string }

func (e foreignProviderError) Error() string { return e.text }

func TestEveryOperationOfTheThreeFilesKeepsAForeignProvidersErrorTextOutOfWhatItReturnsAndRecords(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-vendor-text-4f19"
	for _, tc := range levelCases() {
		if tc.requests == 0 {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			var model schemafluxtest.Provider
			model.Fail(errors.New("vendor said " + canary))
			err := tc.call(operationClient(&model, recordingTo(&recorder)), schemaflux.Options{})
			if err == nil {
				t.Fatal("the call succeeded")
			}
			var seen []string
			seen = append(seen, err.Error())
			if encoded, jsonErr := marshaledText(err); jsonErr == nil {
				seen = append(seen, encoded)
			}
			for _, record := range recorder.Records() {
				seen = append(seen, fmt.Sprintf("%+v", record))
			}
			for _, text := range seen {
				if strings.Contains(text, canary) {
					t.Errorf("the vendor's text reached %q", excerpt(text))
				}
			}
		})
		t.Run(tc.name+", in the logs", func(t *testing.T) {
			t.Parallel()
			var logs schemafluxtest.Logs
			var model schemafluxtest.Provider
			model.Fail(errors.New("vendor said " + canary))
			_ = tc.call(operationClient(&model, loggingTo(&logs)), schemaflux.Options{})
			for _, entry := range logs.Entries() {
				line := entry.Message
				entry.Attrs(func(a slog.Attr) bool {
					line += " " + a.Key + "=" + a.Value.String()
					return true
				})
				if strings.Contains(line, canary) {
					t.Errorf("a log line holds the vendor's text: %s", excerpt(line))
				}
			}
		})
	}
}

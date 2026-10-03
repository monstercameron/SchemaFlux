package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// A decision costs a generating provider's caller nothing: the context that
// carries it is the one wrapper a decision already made to decline the
// reinforcement block, and no second one.
func TestADecisionCallIsOneAllocationAsTheWrapperItReplacedWas(t *testing.T) {
	ctx := context.Background()
	one := []assessQuestion{{kind: questionYesNo, ask: "Is it?"}}
	which := []assessQuestion{{kind: questionChoice, ask: "Which?", labels: []string{"a", "b", "c"}}}
	planned := planFor(reflect.TypeOf(dec12Look{}))
	if planned.err != nil || len(planned.ask.typed) != 13 {
		t.Fatalf("the plan keeps %d typed questions (%v), want 13", len(planned.ask.typed), planned.err)
	}

	var kept context.Context
	before := testing.AllocsPerRun(100, func() { kept = dec12WithoutReinforcement(ctx) })
	cases := []struct {
		name string
		make func() context.Context
	}{
		{"a yes or no", func() context.Context { return newDecisionCall(ctx, `"x"`, "", one, nil) }},
		{"a choice shares its labels", func() context.Context { return newDecisionCall(ctx, `"x"`, "steer", which, nil) }},
		{"a type's thirteen questions are its plan's", func() context.Context {
			return newDecisionCall(ctx, `"x"`, "", planned.questions, planned.ask.typed)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testing.AllocsPerRun(100, func() { kept = tc.make() })
			if got != before {
				t.Errorf("the decision call allocates %v times, and the wrapper it replaced %v", got, before)
			}
		})
	}
	_ = kept
}

type pointerMarshaled struct{ name string }

func (p *pointerMarshaled) MarshalJSON() ([]byte, error) {
	return json.Marshal("marshaled:" + p.name)
}

type refusesToMarshal struct{}

func (refusesToMarshal) MarshalJSON() ([]byte, error) { return nil, errors.New("will not") }

// taggedOptionsAgree checks taggedOptionsJSON against the encoding it replaced.
func taggedOptionsAgree[T any](t *testing.T, name string, options []T) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		want, wantErr := json.Marshal(tagItems(options))
		ids := itemIDs(len(options))
		got, items, err := taggedOptionsJSON(options, ids)
		if (err != nil) != (wantErr != nil) {
			t.Fatalf("error %v, and json.Marshal's is %v", err, wantErr)
		}
		if err != nil {
			if err.Error() != wantErr.Error() {
				t.Errorf("error %q, and json.Marshal's is %q", err, wantErr)
			}
			return
		}
		if got != string(want) {
			t.Errorf("the options are written differently\n got %s\nwant %s", got, want)
		}
		if len(items) != len(options) {
			t.Fatalf("%d items described, want %d", len(items), len(options))
		}
		for i := range options {
			alone, _ := json.Marshal(&options[i])
			if items[i] != string(alone) {
				t.Errorf("item %d is described as %s, want %s", i+1, items[i], alone)
			}
			if !strings.Contains(got, `{"id":"`+ids[i]+`","item":`+items[i]+`}`) {
				t.Errorf("item %d is not under its id in the list", i+1)
			}
		}
	})
}

func TestTaggedOptionsJSONIsWhatJSONMarshalWrote(t *testing.T) {
	type ticket struct {
		ID      string  `json:"id"`
		Subject string  `json:"subject"`
		Amount  float64 `json:"amount,omitempty"`
		private int
	}
	label := "pointed"
	taggedOptionsAgree(t, "structs", []ticket{{ID: "T-1", Subject: "charged twice", Amount: 42.5}, {ID: "T-2", Subject: "cannot log in"}})
	taggedOptionsAgree(t, "text with characters JSON and HTML escape", []string{`a "quoted" <b>&</b>`, "line\nbreak", "tab\tand \\ slash", "ünïcödé  "})
	taggedOptionsAgree(t, "numbers", []float64{1, 2.5, -3e9})
	taggedOptionsAgree(t, "booleans", []bool{true, false})
	taggedOptionsAgree(t, "maps", []map[string]any{{"b": 1, "a": []int{1, 2}}, {}, nil})
	taggedOptionsAgree(t, "any values", []any{nil, "text", 3, ticket{ID: "T-9"}, []string{"x"}})
	taggedOptionsAgree(t, "pointers, one nil", []*string{&label, nil})
	taggedOptionsAgree(t, "a marshaler on the pointer", []pointerMarshaled{{name: "a"}, {name: "b"}})
	taggedOptionsAgree(t, "pointers to a marshaler", []*pointerMarshaled{{name: "a"}, nil})
	taggedOptionsAgree(t, "raw JSON", []json.RawMessage{json.RawMessage(`{"k": [1, 2]}`), json.RawMessage(`"s"`)})
	taggedOptionsAgree(t, "one option", []string{"only"})
	taggedOptionsAgree(t, "no options", []string{})
	taggedOptionsAgree(t, "an item that cannot be written", []chan int{make(chan int)})
	taggedOptionsAgree(t, "an item that refuses", []refusesToMarshal{{}, {}})
	taggedOptionsAgree(t, "nested slices", [][]ticket{{{ID: "a"}}, nil, {}})
}

func TestChooseGuidance(t *testing.T) {
	base := NewChooseOptions()
	tournament := base
	tournament.Strategy = "tournament"
	cases := []struct {
		name string
		opts ChooseOptions
		want string
	}{
		{"nothing said", base, ""},
		{"steering alone", base.WithSteering("the most urgent"), "the most urgent"},
		{"criteria alone", base.WithCriteria([]string{"quality", "price"}), "Selection criteria: quality, price"},
		{"one criterion", base.WithCriteria([]string{"price"}), "Selection criteria: price"},
		{"both", base.WithSteering("the most urgent").WithCriteria([]string{"quality"}), "the most urgent. Selection criteria: quality"},
		{"steering with space around it", base.WithSteering("  prefer the cheapest \n"), "prefer the cheapest"},
		{"steering that is only space", base.WithSteering("   "), ""},
		{"the reasoning instruction is not guidance", base.WithRequireReasoning(true), ""},
		{"nor is the strategy", tournament, ""},
		{"nor the number wanted", base.WithTopN(3).WithCriteria([]string{"speed"}), "Selection criteria: speed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseGuidance(tc.opts); got != tc.want {
				t.Errorf("chooseGuidance = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDistinctLabels(t *testing.T) {
	many := make([]string, 40)
	for i := range many {
		many[i] = "label " + string(rune('A'+i))
	}
	repeated := append(append([]string{}, many...), "label A")
	blankAmongMany := append(append([]string{}, many...), "  ")
	cases := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"two different", []string{"billing", "technical"}, true},
		{"three different", []string{"a", "b", "c"}, true},
		{"none", nil, true},
		{"one", []string{"only"}, true},
		{"a repeat", []string{"a", "b", "a"}, false},
		{"a repeat but for space", []string{"a", " a "}, false},
		{"different letter case is different", []string{"a", "A"}, true},
		{"a blank", []string{"a", ""}, false},
		{"only space", []string{"a", "   "}, false},
		{"many different", many, true},
		{"many with a repeat", repeated, false},
		{"many with a blank", blankAmongMany, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := distinctLabels(tc.labels); got != tc.want {
				t.Errorf("distinctLabels = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyDecision(t *testing.T) {
	two := []string{"billing", "technical"}
	base := NewClassifyOptions().WithCategories(two)
	described := base
	described.CategoryDescriptions = map[string]string{"billing": "Payments and refunds"}
	withExamples := base
	withExamples.CategoryExamples = map[string][]string{"technical": {"the app crashes", "login fails"}}
	both := described
	both.CategoryExamples = map[string][]string{"billing": {"charged twice"}}

	cases := []struct {
		name     string
		opts     ClassifyOptions
		carried  bool
		criteria []string
		guidance string
	}{
		{"two categories", base, true, nil, ""},
		{"the default confidence floor does not stop it", base.WithMinConfidence(0.9), true, nil, ""},
		{"the caller's steering is the guidance", base.WithSteering("  a refund request is billing "), true, nil, "a refund request is billing"},
		{"descriptions are the criteria", described, true, []string{"Payments and refunds", ""}, ""},
		{"examples are criteria", withExamples, true, []string{"", "Examples: the app crashes; login fails"}, ""},
		{"a description and its examples", both, true, []string{"Payments and refunds. Examples: charged twice", ""}, ""},
		{"several labels are not a choice", base.WithMultiLabel(true), false, nil, ""},
		{"one category is not a choice", NewClassifyOptions().WithCategories([]string{"only"}), false, nil, ""},
		{"a repeated category is not a choice", NewClassifyOptions().WithCategories([]string{"a", "b", " a"}), false, nil, ""},
		{"a blank category is not a choice", NewClassifyOptions().WithCategories([]string{"a", " "}), false, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := classifyDecision(context.Background(), tc.opts, `"charged twice"`)
			if (call != nil) != tc.carried {
				t.Fatalf("a decision is carried: %v, want %v", call != nil, tc.carried)
			}
			if call == nil {
				return
			}
			decision := requestDecision(call)
			if decision == nil || len(decision.Questions) != 1 {
				t.Fatalf("the decision carried is %+v", decision)
			}
			question := decision.Questions[0]
			if question.Kind != llm.DecisionChoice || question.Ask != classifyAsk || !reflect.DeepEqual(question.Options, tc.opts.Categories) {
				t.Errorf("the question is %+v", question)
			}
			if !reflect.DeepEqual(question.Criteria, tc.criteria) {
				t.Errorf("criteria %q, want %q", question.Criteria, tc.criteria)
			}
			if decision.State != `"charged twice"` || decision.Guidance != tc.guidance {
				t.Errorf("state %q and guidance %q", decision.State, decision.Guidance)
			}
			if dec12ReinforcementDeclined(call) {
				t.Error("the classification's request would lose its reinforcement block")
			}
			if err := (llm.DecisionLimits{MaxOptions: 255}).Check("jev", "jev-latest", decision); err != nil {
				t.Errorf("the decision is not one a decision provider takes: %v", err)
			}
		})
	}
}

func TestClassifyTypedAnswer(t *testing.T) {
	categories := []string{"billing", "technical", "shipping"}
	cases := []struct {
		name       string
		response   string
		category   string
		confidence float64
		refused    types.ErrorKind
	}{
		{"the first category", `{"q1":1,"confidence":0.91}`, "billing", 0.91, types.KindUnknown},
		{"the last category", `{"q1":3,"confidence":0.5}`, "shipping", 0.5, types.KindUnknown},
		{"a confidence of zero is a confidence", `{"q1":2,"confidence":0}`, "technical", 0, types.KindUnknown},
		{"a confidence of one", `{"q1":2,"confidence":1}`, "technical", 1, types.KindUnknown},
		{"no probability was reported", `{"q1":2}`, "", 0, types.KindSchemaViolation},
		{"a number that is no category", `{"q1":4,"confidence":0.9}`, "", 0, types.KindSchemaViolation},
		{"zero is no category", `{"q1":0,"confidence":0.9}`, "", 0, types.KindSchemaViolation},
		{"no answer", `{"confidence":0.9}`, "", 0, types.KindSchemaViolation},
		{"a confidence past one", `{"q1":1,"confidence":1.5}`, "", 0, types.KindInvariantViolation},
		{"not the form at all", `the answer is billing`, "", 0, types.KindMalformedOutput},
		{"a generating model's classification is not this form", `{"category":"billing","confidence":0.9,"reasoning":"x"}`, "", 0, types.KindSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifyTypedAnswer(tc.response, categories)
			if tc.refused != types.KindUnknown {
				if types.KindOf(err) != tc.refused {
					t.Fatalf("error %v (kind %v), want kind %v", err, types.KindOf(err), tc.refused)
				}
				if got.Category != "" || strings.Contains(err.Error(), "billing") {
					t.Errorf("a refusal came with %+v beside it, or repeats the answer: %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got.Category != tc.category || got.ModelConfidence != tc.confidence || got.Reasoning != "" || len(got.Alternatives) != 0 {
				t.Errorf("read %+v", got)
			}
		})
	}
}

func TestDecisionAnsweredDescribesTheRecord(t *testing.T) {
	below := &llm.DecisionEscalation{Provider: "jev", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: 0.55, ConfidenceReported: true}
	down := &llm.DecisionEscalation{Provider: "jev", Reason: llm.DecisionEscalatedUnavailable}
	failed := errors.New("the generator failed")
	confidence := func(value float64) *float64 { return &value }

	cases := []struct {
		name     string
		answered decisionAnswered
		err      error
		want     DecisionRecord
	}{
		{"a generating provider answered", decisionAnswered{by: "openai"}, nil, DecisionRecord{AnsweredBy: "openai"}},
		{"a decision provider answered typed", decisionAnswered{by: "jev", typed: true}, nil, DecisionRecord{AnsweredBy: "jev", Typed: true}},
		{"asked first and below the floor", decisionAnswered{by: "openai", escalation: below}, nil,
			DecisionRecord{AnsweredBy: "openai", AskedFirst: "jev", Escalated: llm.DecisionEscalatedBelowFloor, AskedFirstModelConfidence: confidence(0.55)}},
		{"asked first and down", decisionAnswered{by: "openai", escalation: down}, nil,
			DecisionRecord{AnsweredBy: "openai", AskedFirst: "jev", Escalated: llm.DecisionEscalatedUnavailable}},
		{"nothing known", decisionAnswered{}, nil, DecisionRecord{}},
		{"a failed decision names nobody as having answered", decisionAnswered{by: "openai", typed: true}, failed, DecisionRecord{Err: failed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := DecisionRecord{Err: tc.err}
			tc.answered.describe(&record)
			if !reflect.DeepEqual(record, tc.want) {
				t.Errorf("the record says %+v, want %+v", record, tc.want)
			}
		})
	}

	t.Run("a call reports what CallLLM noted on it", func(t *testing.T) {
		call := newDecisionCall(context.Background(), `"x"`, "", []assessQuestion{{kind: questionYesNo, ask: "?"}}, nil)
		if got := call.answered(); got != (decisionAnswered{}) {
			t.Errorf("a call nobody answered reports %+v", got)
		}
		call.typed, call.answeredBy, call.escalation = true, "jev", below
		if got := call.answered(); got != (decisionAnswered{by: "jev", typed: true, escalation: below}) {
			t.Errorf("the call reports %+v", got)
		}
	})
}

// ledger is a client budget that keeps what it was charged.
type ledger struct {
	costs  []float64
	priced []bool
}

func (l *ledger) Check() error { return nil }
func (l *ledger) Record(cost float64, priced bool) {
	l.costs = append(l.costs, cost)
	l.priced = append(l.priced, priced)
}

func TestChargeAskedFirst(t *testing.T) {
	decider := benchInstantProvider{}
	usage := types.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 50, TotalTokens: 1_000_050}
	started := time.Now().Add(-time.Second)

	cases := []struct {
		name       string
		asked      llm.Provider
		model      string
		resp       llm.CompletionResponse
		charged    int
		priced     bool
		cost       float64
		recorded   int
		provider   string
		wantModel  string
		usageKnown bool
	}{
		{"a priced decision model is charged at its own rate", decider, "gpt-6-luna",
			llm.CompletionResponse{Provider: "jev", Model: "jev-latest", Usage: usage}, 1, true, 0.042, 1, "jev", "jev-latest", true},
		{"the version the API names is priced too", decider, "gpt-6-luna",
			llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", Usage: usage}, 1, true, 0.042, 1, "jev", "jev-1.13.0", true},
		{"a model with no price is charged as unpriced", decider, "gpt-6-luna",
			llm.CompletionResponse{Provider: "other", Model: "decide-9", Usage: usage}, 1, false, 0, 1, "other", "decide-9", true},
		{"a response that names nobody is the provider and model asked", decider, "jev-latest",
			llm.CompletionResponse{Usage: usage}, 1, true, 0.042, 1, decider.Name(), "jev-latest", true},
		{"a response with no usage is spend of unknown size", decider, "jev-latest",
			llm.CompletionResponse{Provider: "jev", Model: "jev-latest"}, 1, false, 0, 1, "jev", "jev-latest", false},
		{"a cached response was not bought", decider, "jev-latest",
			llm.CompletionResponse{Provider: "jev", Model: "jev-latest", Usage: usage, Cached: true}, 0, false, 0, 0, "", "", false},
		{"no provider, nothing to charge", nil, "jev-latest",
			llm.CompletionResponse{Provider: "jev", Model: "jev-latest", Usage: usage}, 0, false, 0, 0, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := &ledger{}
			ctx, records := withCallRecording(WithExecConfig(context.Background(), &ExecConfig{Budget: budget}))
			ChargeAskedFirst(ctx, tc.asked, tc.model, tc.resp, started)

			if len(budget.costs) != tc.charged {
				t.Fatalf("the budget was charged %d times, want %d", len(budget.costs), tc.charged)
			}
			if tc.charged == 1 && (budget.priced[0] != tc.priced || budget.costs[0] < tc.cost-1e-9 || budget.costs[0] > tc.cost+1e-9) {
				t.Errorf("charged %v (priced %v), want %v (priced %v)", budget.costs[0], budget.priced[0], tc.cost, tc.priced)
			}
			kept := records.collect()
			if len(kept) != tc.recorded {
				t.Fatalf("%d records, want %d", len(kept), tc.recorded)
			}
			if tc.recorded == 0 {
				return
			}
			record := kept[0]
			if record.Provider != tc.provider || record.Model != tc.wantModel || record.RequestedModel != tc.model {
				t.Errorf("the record names %s/%s (asked %s)", record.Provider, record.Model, record.RequestedModel)
			}
			if (record.TokenUsage != nil) != tc.usageKnown || (record.CostInfo != nil) != tc.usageKnown {
				t.Errorf("usage %v and cost %v on the record", record.TokenUsage, record.CostInfo)
			}
			if tc.usageKnown && (record.CostInfo.Priced != tc.priced || record.TokenUsage.PromptTokens != usage.PromptTokens) {
				t.Errorf("the record's cost is %+v", record.CostInfo)
			}
			if record.Custom[customDecisionAskedFirst] != tc.provider || record.Duration < time.Second {
				t.Errorf("the record does not say it was asked first, or lost when it started: %+v", record)
			}
			// It is one attempt of the call it belongs to.
			if meta := envelopeFrom(kept, "is"); meta.Attempts != 1 {
				t.Errorf("the record counts as %d attempts", meta.Attempts)
			}
		})
	}

	t.Run("a context with no budget and no recorder", func(t *testing.T) {
		ChargeAskedFirst(context.Background(), decider, "jev-latest", llm.CompletionResponse{Provider: "jev", Model: "jev-latest", Usage: usage}, started)
		ChargeAskedFirst(nil, decider, "jev-latest", llm.CompletionResponse{Provider: "jev", Model: "jev-latest", Usage: usage}, started)
	})
}

package ops

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 11: the functions that had fewer than ten cases of their own.

func TestArbitrateAnswerOnItsOwn(t *testing.T) {
	rules := []string{"cheap", "fast"}
	one := func(passed, score, reasoning string) string {
		return `{"passed":` + passed + `,"score":` + score + `,"reasoning":"` + reasoning + `"}`
	}
	whole := func(o1r1, o1r2, o2r1, o2r2, rest string) string {
		return `{"options":{"o1":{"r1":` + o1r1 + `,"r2":` + o1r2 + `},"o2":{"r1":` + o2r1 + `,"r2":` + o2r2 + `}}` + rest + `}`
	}
	good := one("true", "0.9", "why")
	const tail = `,"reasoning":"overall","confidence":0.8`

	cases := []struct {
		name      string
		response  string
		reasoning bool
		kind      types.ErrorKind // KindUnknown for an answer that is accepted
	}{
		{"a whole answer", whole(good, good, good, good, tail), true, types.KindUnknown},
		{"a whole answer, reasons not asked for", whole(good, good, good, good, tail), false, types.KindUnknown},
		{"no reasons, and none asked for", whole(`{"passed":true,"score":1}`, good, good, good, tail), false, types.KindUnknown},
		{"no reason for one evaluation, and reasons asked for", whole(`{"passed":true,"score":1}`, good, good, good, tail), true, types.KindSchemaViolation},
		{"a blank reason, and reasons asked for", whole(one("true", "1", "  "), good, good, good, tail), true, types.KindSchemaViolation},
		{"no overall reasoning", whole(good, good, good, good, `,"confidence":0.8`), true, types.KindSchemaViolation},
		{"no confidence", whole(good, good, good, good, `,"reasoning":"overall"`), true, types.KindSchemaViolation},
		{"a confidence off the scale", whole(good, good, good, good, `,"reasoning":"x","confidence":1.5`), true, types.KindInvariantViolation},
		{"a score off the scale", whole(one("true", "1.2", "why"), good, good, good, tail), true, types.KindInvariantViolation},
		{"a negative score", whole(one("true", "-0.1", "why"), good, good, good, tail), true, types.KindInvariantViolation},
		{"no score", whole(`{"passed":true,"reasoning":"why"}`, good, good, good, tail), true, types.KindSchemaViolation},
		{"no verdict on a rule", whole(`{"score":0.5,"reasoning":"why"}`, good, good, good, tail), true, types.KindSchemaViolation},
		{"a rule reported as null", whole(`null`, good, good, good, tail), true, types.KindSchemaViolation},
		{"a score that is text", whole(`{"passed":true,"score":"high","reasoning":"why"}`, good, good, good, tail), true, types.KindSchemaViolation},
		{"no options", `{"reasoning":"overall","confidence":0.8}`, true, types.KindSchemaViolation},
		{"options null", `{"options":null,"reasoning":"overall","confidence":0.8}`, true, types.KindSchemaViolation},
		{"options a list", `{"options":[` + good + `],"reasoning":"overall","confidence":0.8}`, true, types.KindSchemaViolation},
		{"an option missing", `{"options":{"o1":{"r1":` + good + `,"r2":` + good + `}},"reasoning":"x","confidence":0.8}`, true, types.KindSchemaViolation},
		{"an option too many", `{"options":{"o1":{"r1":` + good + `,"r2":` + good + `},"o2":{"r1":` + good + `,"r2":` + good + `},"o3":{"r1":` + good + `,"r2":` + good + `}},"reasoning":"x","confidence":0.8}`, true, types.KindSchemaViolation},
		{"a rule missing", `{"options":{"o1":{"r1":` + good + `},"o2":{"r1":` + good + `,"r2":` + good + `}},"reasoning":"x","confidence":0.8}`, true, types.KindSchemaViolation},
		{"keys in other capitals", `{"options":{"O1":{"r1":` + good + `,"r2":` + good + `},"o2":{"r1":` + good + `,"r2":` + good + `}},"reasoning":"x","confidence":0.8}`, true, types.KindSchemaViolation},
		{"not JSON", `the first option is better`, true, types.KindMalformedOutput},
		{"an empty object", `{}`, true, types.KindSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evaluations, overall, confidence, err := arbitrateAnswer(tc.response, 2, rules, tc.reasoning)
			if tc.kind != types.KindUnknown {
				if err == nil {
					t.Fatalf("the answer was accepted: %+v", evaluations)
				}
				if kind := types.KindOf(err); kind != tc.kind && !(tc.kind == types.KindMalformedOutput && kind == types.KindSchemaViolation) {
					t.Errorf("err = %v (%v), want %v", err, kind, tc.kind)
				}
				if evaluations != nil || overall != "" || confidence != 0 {
					t.Errorf("something came back beside the error: %v, %q, %v", evaluations, overall, confidence)
				}
				if strings.Contains(err.Error(), "why") || strings.Contains(err.Error(), "better") || strings.Contains(err.Error(), "high") {
					t.Errorf("the error repeats what the model wrote: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("arbitrateAnswer: %v", err)
			}
			if len(evaluations) != 2 || confidence != 0.8 {
				t.Fatalf("%d evaluations at %v", len(evaluations), confidence)
			}
			for i, evaluation := range evaluations {
				if evaluation.Index != i || len(evaluation.RuleResults) != 2 {
					t.Errorf("evaluation %d: %+v", i, evaluation)
				}
				for j, result := range evaluation.RuleResults {
					if result.Rule != rules[j] {
						t.Errorf("option %d, rule %d carries the rule %q", i, j, result.Rule)
					}
					if kept := result.Reasoning != ""; kept && !tc.reasoning {
						t.Errorf("a reason nobody asked for was kept: %q", result.Reasoning)
					}
				}
				// Nothing is worked out here.
				if evaluation.TotalScore != 0 || evaluation.Disqualified {
					t.Errorf("evaluation %d has a total or a disqualification before the caller's rules were applied", i)
				}
			}
		})
	}
}

func TestMergeArbitrateOptions(t *testing.T) {
	defaults := ArbitrateOptions{Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}
	ctx := context.WithValue(context.Background(), struct{}{}, "x")
	cases := []struct {
		name string
		user ArbitrateOptions
		want ArbitrateOptions
	}{
		{"nothing set", ArbitrateOptions{}, defaults},
		{"rules", ArbitrateOptions{Rules: []string{"a"}},
			ArbitrateOptions{Rules: []string{"a"}, Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"weights", ArbitrateOptions{Weights: []float64{2}},
			ArbitrateOptions{Weights: []float64{2}, Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"every rule required", ArbitrateOptions{RequireAllRules: true},
			ArbitrateOptions{RequireAllRules: true, Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"reasons", ArbitrateOptions{IncludeReasoning: true},
			ArbitrateOptions{IncludeReasoning: true, Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"a tiebreaker", ArbitrateOptions{Tiebreaker: "first"},
			ArbitrateOptions{Tiebreaker: "first", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"guidance", ArbitrateOptions{Steering: "prices exclude tax"},
			ArbitrateOptions{Steering: "prices exclude tax", Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"a mode", ArbitrateOptions{Mode: types.Strict},
			ArbitrateOptions{Tiebreaker: "most-confident", Mode: types.Strict, Intelligence: types.Fast}},
		{"a tier", ArbitrateOptions{Intelligence: types.Smart},
			ArbitrateOptions{Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Smart}},
		{"a model", ArbitrateOptions{Model: "gpt-6-luna"},
			ArbitrateOptions{Model: "gpt-6-luna", Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"an effort", ArbitrateOptions{Effort: types.EffortNone},
			ArbitrateOptions{Effort: types.EffortNone, Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"a context", ArbitrateOptions{Context: ctx},
			ArbitrateOptions{Context: ctx, Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
		{"the two identifiers", ArbitrateOptions{RequestID: "r-1", CorrelationID: "c-1"},
			ArbitrateOptions{RequestID: "r-1", CorrelationID: "c-1", Tiebreaker: "most-confident", Mode: types.TransformMode, Intelligence: types.Fast}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeArbitrateOptions(defaults, tc.user); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("merged = %+v, want %+v", got, tc.want)
			}
		})
	}
	// The defaults hold no reasons: a struct that does not ask for them does
	// not get them.
	if mergeArbitrateOptions(defaults, ArbitrateOptions{Rules: []string{"a"}}).IncludeReasoning {
		t.Error("reasons are asked for by default")
	}
}

func TestArbitrateFailures(t *testing.T) {
	option := func(passed ...bool) OptionEvaluation {
		results := make([]RuleEvaluation, len(passed))
		for j, ok := range passed {
			results[j] = RuleEvaluation{Rule: "the caller's own rule " + strconv.Itoa(j), Passed: ok, Reasoning: "the model's words"}
		}
		return OptionEvaluation{RuleResults: results}
	}
	many := make([]OptionEvaluation, 12)
	for i := range many {
		many[i] = option(false)
	}
	cases := []struct {
		name        string
		evaluations []OptionEvaluation
		want        string
	}{
		{"one option, one rule", []OptionEvaluation{option(false)}, "option 1 failed rule 1"},
		{"one option, the second rule", []OptionEvaluation{option(true, false)}, "option 1 failed rule 2"},
		{"one option, two rules", []OptionEvaluation{option(false, false)}, "option 1 failed rules 1, 2"},
		{"one option, the first and third", []OptionEvaluation{option(false, true, false)}, "option 1 failed rules 1, 3"},
		{"two options", []OptionEvaluation{option(false, true), option(true, false)}, "option 1 failed rule 1; option 2 failed rule 2"},
		{"three options", []OptionEvaluation{option(false), option(false), option(false)},
			"option 1 failed rule 1; option 2 failed rule 1; option 3 failed rule 1"},
		{"ten options are all listed", many[:10], strings.TrimSuffix(strings.Repeat("option N failed rule 1; ", 10), "; ")},
		{"eleven: ten and one more", many[:11], strings.Repeat("option N failed rule 1; ", 10) + "and 1 more"},
		{"twelve: ten and two more", many, strings.Repeat("option N failed rule 1; ", 10) + "and 2 more"},
		{"none", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := arbitrateFailures(tc.evaluations)
			want := tc.want
			for i := 1; strings.Contains(want, "option N"); i++ {
				want = strings.Replace(want, "option N", "option "+strconv.Itoa(i), 1)
			}
			if got != want {
				t.Errorf("arbitrateFailures = %q, want %q", got, want)
			}
			if strings.Contains(got, "caller's") || strings.Contains(got, "model's") {
				t.Errorf("the account carries text of the caller's or the model's: %q", got)
			}
		})
	}
}

type lx11Embeds struct{ lx11Named }
type lx11HiddenEmbed struct{ lx11Opaque }

// lx11Loop embeds a pointer to itself and has nothing exported.
type lx11Loop struct {
	*lx11Loop
	depth int
}

// lx11LoopNamed embeds a pointer to itself and has an exported field.
type lx11LoopNamed struct {
	*lx11LoopNamed
	Depth int
}

func TestDecideSaysNothing(t *testing.T) {
	var nilPointer *lx11Opaque
	var nilNamed *lx11Named
	cases := []struct {
		name      string
		situation any
		nothing   bool
	}{
		{"nil", nil, false},
		{"a string", "text", false},
		{"an empty string", "", false},
		{"a number", 3, false},
		{"a map", map[string]int{"a": 1}, false},
		{"an empty map", map[string]int{}, false},
		{"a slice", []string{"a"}, false},
		{"a struct with an exported field", lx11Named{Subject: "x"}, false},
		{"the same, empty", lx11Named{}, false},
		{"a pointer to one", &lx11Named{}, false},
		{"a nil pointer to one", nilNamed, false},
		{"a struct with none exported", lx11Opaque{}, true},
		{"a pointer to one with none exported", &lx11Opaque{}, true},
		{"a nil pointer to one with none exported", nilPointer, true},
		{"an empty struct", struct{}{}, false},
		{"an error", errors.New("x"), true},
		{"an error that formats", fmtError("x"), true},
		{"a classified error", &types.OperationError{Kind: types.KindTimeout}, false},
		{"a type that writes itself as JSON", lx11Marshals{text: "x"}, false},
		{"a struct that embeds one with exported fields", lx11Embeds{}, false},
		{"a struct that embeds only one with none", lx11HiddenEmbed{}, true},
		{"a struct that embeds a pointer to itself, nothing exported", lx11Loop{depth: 1}, true},
		{"a struct that embeds a pointer to itself, with an exported field", lx11LoopNamed{Depth: 1}, false},
	}
	for _, tc := range cases {
		if got := decideSaysNothing(tc.situation); got != tc.nothing {
			t.Errorf("%s: decideSaysNothing = %v, want %v", tc.name, got, tc.nothing)
		}
	}
}

func fmtError(text string) error { return errors.Join(errors.New(text)) }

// send, finish and whyStopped: the cases the table for them did not have.
func TestStreamSendFinishAndWhyStopped(t *testing.T) {
	newStream := func(buffer int) *TextStream {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		return &TextStream{ctx: ctx, cancel: cancel, items: make(chan textStreamItem, buffer)}
	}
	open := context.Background()
	ended, end := context.WithCancel(context.Background())
	end()
	failure := errors.New("the provider failed")
	answer := llm.CompletionResponse{Content: "the answer"}

	sends := []struct {
		name   string
		buffer int
		fill   int
		stop   bool
		run    context.Context
		sent   bool
	}{
		{"room for one", 1, 0, false, open, true},
		{"room for many", 4, 2, false, open, true},
		{"the last place", 2, 1, false, open, true},
		{"full, and the stream stopped", 1, 1, true, open, false},
		{"full, and the run over", 1, 1, false, ended, false},
		{"full, and both over", 2, 2, true, ended, false},
		{"room, and the stream stopped", 2, 1, true, open, true},
		{"room, and the run over", 2, 1, false, ended, true},
		{"room, and both over", 1, 0, true, ended, true},
		{"no buffer at all, and the run over", 0, 0, false, ended, false},
	}
	for _, tc := range sends {
		t.Run("send: "+tc.name, func(t *testing.T) {
			s := newStream(tc.buffer)
			for range tc.fill {
				s.items <- textStreamItem{delta: "x"}
			}
			if tc.stop {
				s.cancel()
			}
			if got := s.send(tc.run, textStreamItem{delta: "a"}); got != tc.sent {
				t.Errorf("sent = %v, want %v", got, tc.sent)
			}
			if want := tc.fill + boolCount(tc.sent); len(s.items) != want {
				t.Errorf("%d items are waiting, want %d", len(s.items), want)
			}
		})
	}

	finishes := []struct {
		name     string
		buffer   int
		run      context.Context
		items    []textStreamItem
		wantErr  error
		wantText string
		waiting  int
	}{
		{"a result, delivered", 1, open, []textStreamItem{{final: true, resp: answer}}, nil, "the answer", 1},
		{"a result nobody can take", 0, ended, []textStreamItem{{final: true, resp: answer}}, nil, "the answer", 0},
		{"an error, delivered", 1, open, []textStreamItem{{final: true, err: failure}}, failure, "", 1},
		{"an error nobody can take", 0, ended, []textStreamItem{{final: true, err: failure}}, failure, "", 0},
		{"a result, then an error: the result stands", 2, open, []textStreamItem{{final: true, resp: answer}, {final: true, err: failure}}, nil, "the answer", 2},
		{"an error, then a result: the error stands", 2, open, []textStreamItem{{final: true, err: failure}, {final: true, resp: answer}}, failure, "", 2},
		{"two results: the first stands", 2, open, []textStreamItem{{final: true, resp: answer}, {final: true, resp: llm.CompletionResponse{Content: "another"}}}, nil, "the answer", 2},
		{"an empty result", 1, open, []textStreamItem{{final: true}}, nil, "", 1},
		{"a result into a full buffer with the run over", 1, ended, []textStreamItem{{final: true, resp: answer}, {final: true, err: failure}}, nil, "the answer", 1},
		{"an error into a full buffer with the run over", 1, ended, []textStreamItem{{final: true, err: failure}, {final: true, resp: answer}}, failure, "", 1},
	}
	for _, tc := range finishes {
		t.Run("finish: "+tc.name, func(t *testing.T) {
			s := newStream(tc.buffer)
			for _, item := range tc.items {
				s.finish(tc.run, item)
			}
			if len(s.items) != tc.waiting {
				t.Errorf("%d items are waiting, want %d", len(s.items), tc.waiting)
			}
			// The record is there whether or not anything was delivered: a
			// reader that finds the channel closed gets the outcome from it.
			close(s.items)
			got := s.endWithoutFinal()
			if tc.wantErr == nil && got != nil || tc.wantErr != nil && !errors.Is(got, tc.wantErr) {
				t.Errorf("the outcome is %v, want %v", got, tc.wantErr)
			}
			s.resultMu.Lock()
			text := s.finalText
			s.resultMu.Unlock()
			if text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
		})
	}

	whys := []struct {
		name    string
		prepare func(s *TextStream)
		want    error // nil: an error of this library's own
	}{
		{"nothing known", func(*TextStream) {}, nil},
		{"the stream's context over", func(s *TextStream) { s.cancel() }, types.ErrCanceled},
		{"a reason left by the producer", func(s *TextStream) { s.stopped = failure }, failure},
		{"a reason, and the context over", func(s *TextStream) { s.stopped = failure; s.cancel() }, failure},
		{"a final error recorded", func(s *TextStream) { s.final = &textStreamItem{final: true, err: failure} }, failure},
		{"a final error over a reason", func(s *TextStream) {
			s.stopped = errors.New("another")
			s.final = &textStreamItem{final: true, err: failure}
		}, failure},
		{"a final result recorded, and nothing else", func(s *TextStream) { s.final = &textStreamItem{final: true, resp: answer} }, nil},
		{"a final result, and a reason", func(s *TextStream) {
			s.final = &textStreamItem{final: true, resp: answer}
			s.stopped = failure
		}, failure},
		{"a final result, and the context over", func(s *TextStream) {
			s.final = &textStreamItem{final: true, resp: answer}
			s.cancel()
		}, types.ErrCanceled},
		{"a classified reason", func(s *TextStream) { s.stopped = types.Newf(types.KindShutdown, "x", "closed") }, types.ErrShutdown},
	}
	for _, tc := range whys {
		t.Run("whyStopped: "+tc.name, func(t *testing.T) {
			s := newStream(0)
			tc.prepare(s)
			got := s.whyStopped()
			if got == nil {
				t.Fatal("whyStopped gave no reason")
			}
			if tc.want != nil && !errors.Is(got, tc.want) {
				t.Errorf("whyStopped = %v, want %v", got, tc.want)
			}
			if again := s.whyStopped(); !errors.Is(again, got) && again.Error() != got.Error() {
				t.Errorf("asked again, whyStopped = %v", again)
			}
		})
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestAuditResponseSchemaShapes(t *testing.T) {
	cases := []struct {
		name       string
		policies   []string
		categories []string
		policyKeys []string
		findingsOf []string // the categories a finding outside any policy may carry
	}{
		{"nothing supplied", nil, nil, nil, auditCategories},
		{"one policy", []string{"a"}, nil, []string{"p1"}, auditCategories},
		{"three policies", []string{"a", "b", "c"}, nil, []string{"p1", "p2", "p3"}, auditCategories},
		{"categories only", nil, []string{"quality"}, nil, []string{"quality"}},
		{"both", []string{"a"}, []string{"quality", "planning"}, []string{"p1"}, []string{"quality", "planning"}},
		{"ten policies", make([]string, 10), nil, []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10"}, auditCategories},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := auditResponseSchema(tc.policies, tc.categories)
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema breaks a strict-mode rule: %s", problem)
			}
			properties := schema["properties"].(map[string]any)
			policies, declared := properties["policies"].(map[string]any)
			if declared != (len(tc.policyKeys) > 0) {
				t.Fatalf("policies declared = %v", declared)
			}
			if declared {
				keys := policies["properties"].(map[string]any)
				if len(keys) != len(tc.policyKeys) {
					t.Errorf("%d policy keys, want %d", len(keys), len(tc.policyKeys))
				}
				for _, key := range tc.policyKeys {
					if _, has := keys[key]; !has {
						t.Errorf("no key %s", key)
					}
				}
			}
			finding := properties["findings"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			enum, _ := finding["category"].(map[string]any)["enum"].([]any)
			if len(enum) != len(tc.findingsOf) {
				t.Errorf("a finding may carry %v, want %v", enum, tc.findingsOf)
			}
		})
	}
}

package schemaflux

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
)

func TestDecisionKindsAreTheInternalNumbers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		kind     DecisionKind
		internal llm.DecisionKind
		value    uint8
	}{
		{"yes or no", DecisionYesNo, llm.DecisionYesNo, 1},
		{"choice", DecisionChoice, llm.DecisionChoice, 2},
		{"scale", DecisionScale, llm.DecisionScale, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if uint8(c.kind) != c.value || uint8(c.internal) != c.value {
				t.Errorf("kind %d, internal %d, want %d", c.kind, c.internal, c.value)
			}
		})
	}
}

// decisionTypes pairs each declared decision type with the internal type it
// converts to, so that one reflection test covers them all.
func TestEveryFieldOfTheInternalDecisionTypesIsCarried(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		internal reflect.Type
		carries  func(t *testing.T, set reflect.Value, field string)
	}{
		{"Decision", reflect.TypeOf(llm.Decision{}), func(t *testing.T, set reflect.Value, field string) {
			internal := set.Interface().(llm.Decision)
			converted := decisionFromInternal(&internal)
			if reflect.DeepEqual(*converted, Decision{}) {
				t.Fatalf("llm.Decision.%s is not carried by decisionFromInternal", field)
			}
			if back := converted.internal(); !reflect.DeepEqual(*back, internal) {
				t.Errorf("llm.Decision.%s does not come back: got %+v, want %+v", field, *back, internal)
			}
		}},
		{"DecisionQuestion", reflect.TypeOf(llm.DecisionQuestion{}), func(t *testing.T, set reflect.Value, field string) {
			internal := llm.Decision{Questions: []llm.DecisionQuestion{set.Interface().(llm.DecisionQuestion)}}
			converted := decisionFromInternal(&internal)
			if reflect.DeepEqual(converted.Questions[0], DecisionQuestion{}) {
				t.Fatalf("llm.DecisionQuestion.%s is not carried by decisionFromInternal", field)
			}
			if back := converted.internal(); !reflect.DeepEqual(*back, internal) {
				t.Errorf("llm.DecisionQuestion.%s does not come back", field)
			}
		}},
		{"DecisionAnswer", reflect.TypeOf(llm.DecisionAnswer{}), func(t *testing.T, set reflect.Value, field string) {
			internal := []llm.DecisionAnswer{set.Interface().(llm.DecisionAnswer)}
			converted := answersFromInternal(internal)
			if reflect.DeepEqual(converted[0], DecisionAnswer{}) {
				t.Fatalf("llm.DecisionAnswer.%s is not carried by answersFromInternal", field)
			}
			if back := answersToInternal(converted); !reflect.DeepEqual(back, internal) {
				t.Errorf("llm.DecisionAnswer.%s does not come back", field)
			}
		}},
		{"DecisionEscalation", reflect.TypeOf(llm.DecisionEscalation{}), func(t *testing.T, set reflect.Value, field string) {
			internal := set.Interface().(llm.DecisionEscalation)
			converted := escalationFromInternal(&internal)
			if reflect.DeepEqual(*converted, DecisionEscalation{}) {
				t.Fatalf("llm.DecisionEscalation.%s is not carried by escalationFromInternal", field)
			}
			if back := escalationToInternal(converted); !reflect.DeepEqual(*back, internal) {
				t.Errorf("llm.DecisionEscalation.%s does not come back", field)
			}
		}},
		{"DecisionLimits", reflect.TypeOf(llm.DecisionLimits{}), func(t *testing.T, set reflect.Value, field string) {
			internal := set.Interface().(llm.DecisionLimits)
			converted := decisionLimitsFromInternal(internal)
			if converted == (DecisionLimits{}) {
				t.Fatalf("llm.DecisionLimits.%s is not carried by decisionLimitsFromInternal", field)
			}
			if back := converted.internal(); back != internal {
				t.Errorf("llm.DecisionLimits.%s does not come back", field)
			}
		}},
	}
	for _, c := range cases {
		declared := map[string]reflect.Type{
			"Decision":           reflect.TypeOf(Decision{}),
			"DecisionQuestion":   reflect.TypeOf(DecisionQuestion{}),
			"DecisionAnswer":     reflect.TypeOf(DecisionAnswer{}),
			"DecisionEscalation": reflect.TypeOf(DecisionEscalation{}),
			"DecisionLimits":     reflect.TypeOf(DecisionLimits{}),
		}[c.name]
		t.Run(c.name+" has a field for each of the internal one", func(t *testing.T) {
			if declared.NumField() != c.internal.NumField() {
				t.Errorf("%s has %d fields and the internal type has %d", c.name, declared.NumField(), c.internal.NumField())
			}
		})
		for _, field := range fieldNames(c.internal) {
			t.Run(c.name+"."+field, func(t *testing.T) {
				c.carries(t, onlyField(t, c.internal, field), field)
			})
		}
	}
}

func TestDecisionConversionsOfNilAndEmpty(t *testing.T) {
	t.Parallel()
	t.Run("a nil decision is nil both ways", func(t *testing.T) {
		if (*Decision)(nil).internal() != nil || decisionFromInternal(nil) != nil {
			t.Error("nil did not stay nil")
		}
	})
	t.Run("a decision with no questions keeps nil questions", func(t *testing.T) {
		if got := (&Decision{State: "s"}).internal(); got.Questions != nil || got.State != "s" {
			t.Errorf("got %+v", got)
		}
		if got := decisionFromInternal(&llm.Decision{State: "s"}); got.Questions != nil {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("a decision with an empty list of questions keeps an empty list", func(t *testing.T) {
		if got := (&Decision{Questions: []DecisionQuestion{}}).internal(); got.Questions == nil || len(got.Questions) != 0 {
			t.Errorf("got %#v", got.Questions)
		}
	})
	t.Run("nil answers and a nil escalation stay nil", func(t *testing.T) {
		if answersToInternal(nil) != nil || answersFromInternal(nil) != nil || escalationToInternal(nil) != nil || escalationFromInternal(nil) != nil {
			t.Error("nil did not stay nil")
		}
	})
	t.Run("answers are copied", func(t *testing.T) {
		in := []DecisionAnswer{{Kind: DecisionYesNo, Yes: true}}
		out := answersToInternal(in)
		out[0].Yes = false
		if !in[0].Yes {
			t.Error("the slice is shared")
		}
	})
	t.Run("a question of every kind converts in order", func(t *testing.T) {
		d := &Decision{Questions: []DecisionQuestion{
			{Kind: DecisionYesNo, Ask: "a"},
			{Kind: DecisionChoice, Ask: "b", Options: []string{"x", "y", "z"}, Criteria: []string{"cx", "", "cz"}},
			{Kind: DecisionScale, Ask: "c", Low: -2, High: 2},
		}}
		back := decisionFromInternal(d.internal())
		if !reflect.DeepEqual(back, d) {
			t.Errorf("got %+v", back)
		}
	})
}

func TestDecisionLimitsCheck(t *testing.T) {
	t.Parallel()
	yesNo := DecisionQuestion{Kind: DecisionYesNo, Ask: "q"}
	choice := DecisionQuestion{Kind: DecisionChoice, Ask: "q", Options: []string{"a", "b", "c"}}
	scale := DecisionQuestion{Kind: DecisionScale, Ask: "q", Low: 1, High: 5}
	limits := DecisionLimits{MaxQuestions: 2, MaxOptions: 3, MaxLevels: 5, MaxStateTokens: 10}
	cases := []struct {
		name     string
		limits   DecisionLimits
		decision *Decision
		want     Kind // 0 means no error
	}{
		{"a decision within every limit", limits, &Decision{State: `"s"`, Questions: []DecisionQuestion{yesNo, choice}}, 0},
		{"a decision with no limits at all", DecisionLimits{}, &Decision{State: strings.Repeat("x", 10000), Questions: []DecisionQuestion{yesNo, choice, scale, yesNo}}, 0},
		{"no decision", limits, nil, ErrUnsupported},
		{"a decision with no questions", limits, &Decision{State: "s"}, ErrUnsupported},
		{"more questions than the limit", limits, &Decision{Questions: []DecisionQuestion{yesNo, yesNo, yesNo}}, ErrUnsupported},
		{"a blank question", limits, &Decision{Questions: []DecisionQuestion{{Kind: DecisionYesNo, Ask: "  "}}}, ErrInvalidRequest},
		{"a choice of one option", limits, &Decision{Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: "q", Options: []string{"a"}}}}, ErrInvalidRequest},
		{"criteria of another length than the options", limits, &Decision{Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: "q", Options: []string{"a", "b"}, Criteria: []string{"x"}}}}, ErrInvalidRequest},
		{"more options than the limit", limits, &Decision{Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: "q", Options: []string{"a", "b", "c", "d"}}}}, ErrUnsupported},
		{"a scale that leaves nothing to choose", limits, &Decision{Questions: []DecisionQuestion{{Kind: DecisionScale, Ask: "q", Low: 3, High: 3}}}, ErrInvalidRequest},
		{"a scale with more levels than the limit", limits, &Decision{Questions: []DecisionQuestion{{Kind: DecisionScale, Ask: "q", Low: 1, High: 6}}}, ErrUnsupported},
		{"a scale of exactly the limit", limits, &Decision{Questions: []DecisionQuestion{{Kind: DecisionScale, Ask: "q", Low: 1, High: 5}}}, 0},
		{"a question with no kind", limits, &Decision{Questions: []DecisionQuestion{{Ask: "q"}}}, ErrInvalidRequest},
		{"a state longer than the limit", limits, &Decision{State: strings.Repeat("x", 41), Questions: []DecisionQuestion{yesNo}}, ErrUnsupported},
		{"a state of exactly the limit", limits, &Decision{State: strings.Repeat("x", 40), Questions: []DecisionQuestion{yesNo}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.limits.Check("jev", "j1", c.decision)
			if c.want == 0 {
				if err != nil {
					t.Fatalf("got %v", err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("got %T %v, want an *Error", err, err)
			}
			if e.Kind != c.want {
				t.Errorf("Kind = %v, want %v", e.Kind, c.want)
			}
			if e.Op != "decision" || e.Message == "" {
				t.Errorf("got %+v", e)
			}
			if !errors.Is(err, c.want) {
				t.Errorf("errors.Is(err, %v) = false", c.want)
			}
		})
	}
	t.Run("an error repeats nothing of the state, the questions or the options", func(t *testing.T) {
		decisions := []*Decision{
			{State: canary, Questions: []DecisionQuestion{yesNo, yesNo, yesNo}},
			{State: canary, Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: canary, Options: []string{canary}}}},
			{State: strings.Repeat(canary, 20), Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: "q", Options: []string{canary, "b", "c", canary + "d"}}}},
			{State: canary, Questions: []DecisionQuestion{{Kind: DecisionYesNo, Ask: " "}}},
		}
		for i, d := range decisions {
			err := limits.Check("jev", "j1", d)
			if err == nil {
				t.Errorf("case %d: no error", i)
				continue
			}
			if strings.Contains(err.Error(), canary) || strings.Contains(err.(*Error).Detail(), canary) {
				t.Errorf("case %d: the error repeats the caller's text: %v", i, err)
			}
		}
	})
}

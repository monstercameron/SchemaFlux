package ops

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type deriveSource struct {
	Name string `json:"name"`
}

type deriveTarget struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Tier   string `json:"tier"`
}

// deriveCompleteAnswer is an answer that carries everything the prompt asks for.
const deriveCompleteAnswer = `{"derived":{"name":"a","region":"EU","tier":"gold"},"derivations":[{"field":"region","source_fields":["name"],"method":"inference","reasoning":"the name is a EU brand","confidence":0.9}],"field_confidence":{"region":0.9},"overall_confidence":0.9}`

// deriveAnswerWith is the complete answer with the given members replaced.
func deriveAnswerWith(t *testing.T, replaced map[string]string) string {
	t.Helper()
	return answerWithMembers(t, deriveCompleteAnswer, replaced)
}

func TestDeriveRefusesAnAnswerMissingARequiredMember(t *testing.T) {
	members := [][]any{
		{"derived"}, {"derivations"}, {"field_confidence"}, {"overall_confidence"},
		{"derivations", 0, "field"}, {"derivations", 0, "method"}, {"derivations", 0, "confidence"},
	}
	refuseEachMissingMember(t, deriveCompleteAnswer, members, func(response string) (any, error) {
		coreAnswer(t, response)
		result, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"})
		return result, err
	})
}

// source_fields is left out by a derivation that rests on no input field, and
// reasoning is asked for only when IncludeReasoning is on.
func TestDeriveAcceptsADerivationWithoutSourceFieldsAndWithoutReasoningWhenItWasNotAsked(t *testing.T) {
	answer := deriveAnswerWith(t, map[string]string{"derivations": `[{"field":"tier","method":"constant","confidence":0.9}]`})

	coreAnswer(t, answer)
	_, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}, DeriveOptions{IncludeReasoning: false})
	if err != nil {
		t.Errorf("a derivation with no source_fields and no reasoning, reasoning not asked for: %v", err)
	}

	result, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}, DeriveOptions{IncludeReasoning: true})
	wantKind(t, err, types.KindSchemaViolation)
	if !strings.Contains(err.Error(), "derivations[0].reasoning is missing") {
		t.Errorf("err = %v", err)
	}
	if result.Derived != (deriveTarget{}) || result.Derivations != nil {
		t.Errorf("result = %+v beside an error", result)
	}

	// A blank reasoning is no more an explanation than a missing one.
	coreAnswer(t, deriveAnswerWith(t, map[string]string{"derivations": `[{"field":"tier","method":"constant","reasoning":"  ","confidence":0.9}]`}))
	if _, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}); err == nil {
		t.Error("a blank reasoning was accepted although reasoning was asked for")
	}
}

func TestDeriveRefusesANullWhereAScoreOrADerivedValueWasAsked(t *testing.T) {
	for name, replaced := range map[string]map[string]string{
		"overall_confidence":        {"overall_confidence": "null"},
		"derived":                   {"derived": "null"},
		"derivations[0].confidence": {"derivations": `[{"field":"region","method":"m","reasoning":"r","confidence":null}]`},
	} {
		t.Run(name, func(t *testing.T) {
			coreAnswer(t, deriveAnswerWith(t, replaced))
			result, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"})
			if err == nil || !strings.Contains(err.Error(), name+" is null") {
				t.Errorf("err = %v", err)
			}
			if result.Derived != (deriveTarget{}) || result.FieldConfidence != nil {
				t.Errorf("result = %+v beside an error", result)
			}
		})
	}
}

func TestDeriveRefusesAnAnswerWithoutTheDerivedValueOrTheOverallScore(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"only an overall confidence", `{"overall_confidence":0.9}`},
		{"derived is null", `{"derived":null,"overall_confidence":0.9}`},
		{"derived with no overall confidence", `{"derived":{"name":"a"},"field_confidence":{"name":0.9}}`},
		{"overall confidence is null", `{"derived":{"name":"a"},"overall_confidence":null}`},
		{"only field_confidence", `{"field_confidence":{"name":0.9}}`},
		{"an unrelated object", `{"other":1}`},
		{"an empty object", `{}`},
		{"not JSON", `sorry`},
		{"derived of the wrong type", deriveAnswerWith(t, map[string]string{"derived": `"text"`})},
		{"derived is a list", deriveAnswerWith(t, map[string]string{"derived": `[1]`})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			result, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"})
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if result.Derived != (deriveTarget{}) || result.Metadata != nil || result.FieldConfidence != nil {
				t.Errorf("result = %+v beside an error", result)
			}
		})
	}
}

func TestDeriveScoresEveryRequestedFieldAndAppliesTheFloor(t *testing.T) {
	body := func(fieldConfidence string) string {
		return `{"derived":{"name":"a","region":"EU","tier":"gold"},"derivations":[],"field_confidence":` + fieldConfidence + `,"overall_confidence":0.9}`
	}
	cases := []struct {
		name      string
		answer    string
		fields    []string
		wantError bool
	}{
		{"every requested field scored", body(`{"region":0.9,"tier":0.8}`), []string{"region", "tier"}, false},
		{"a requested field left out of the scores", body(`{"region":0.9}`), []string{"region", "tier"}, true},
		{"no scores at all, fields requested", body(`{}`), []string{"region"}, true},
		{"the JSON name for a Go name, case and separators aside", body(`{"market_region":0.9}`), []string{"MarketRegion"}, false},
		{"a weak requested field", body(`{"region":0.9,"tier":0.2}`), []string{"region", "tier"}, true},
		{"a weak field nobody asked about", body(`{"region":0.9,"tier":0.2}`), nil, true},
		{"no fields requested, no scores", body(`{}`), nil, false},
		{"a score above one", body(`{"region":7.0}`), []string{"region"}, true},
		{"a negative score", body(`{"region":-1}`), nil, true},
		{"scores exactly at the floor", body(`{"region":0.6}`), []string{"region"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.answer)
			result, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}, DeriveOptions{Fields: c.fields})
			if c.wantError {
				if err == nil {
					t.Fatalf("accepted: %+v", result)
				}
				if result.Derived != (deriveTarget{}) {
					t.Errorf("result = %+v beside an error", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if result.Derived.Region != "EU" {
				t.Errorf("Derived = %+v", result.Derived)
			}
		})
	}
}

// The merge of the caller's options keeps the default for a floor that is not
// above zero, so the check has to look at the caller's own value: a NaN or a
// negative one would otherwise be taken for "not set" and replaced by the
// default without a word, and a floor above one reached the model first and was
// refused afterwards, because no answer could meet it.
func TestDeriveRefusesAMinConfidenceThatIsNotAFractionBeforeAskingTheModel(t *testing.T) {
	cases := []struct {
		name  string
		value float64
	}{
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
		{"minus a tenth", -0.1},
		{"a hair below zero", -1e-9},
		{"a hair above one", 1.0000001},
		{"one and a half", 1.5},
		{"two", 2},
		{"the largest float", math.MaxFloat64},
		{"the most negative float", -math.MaxFloat64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := answerAndRecord(t, `{"derived":{"name":"a"},"derivations":[],"field_confidence":{},"overall_confidence":1}`)
			result, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}, DeriveOptions{MinConfidence: tc.value})
			wantKind(t, err, types.KindConfiguration, strconv.FormatFloat(tc.value, 'g', -1, 64))
			if !strings.Contains(err.Error(), "MinConfidence") {
				t.Errorf("the error does not name the option: %v", err)
			}
			if len(*calls) != 0 {
				t.Error("the model was asked")
			}
			if !reflect.DeepEqual(result, DeriveResult[deriveTarget]{}) {
				t.Errorf("result beside the error: %+v", result)
			}
		})
	}
}

// Zero is the value of an option the caller did not set, so it keeps meaning
// the default floor of 0.6; every other number from 0 to 1 is the caller's.
func TestDeriveStatesTheCallersMinConfidenceAndReadsZeroAsTheDefault(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  string
	}{
		{"zero is the default", 0, "confidence >= 60%"},
		{"negative zero is the default", math.Copysign(0, -1), "confidence >= 60%"},
		{"exactly the default", 0.6, "confidence >= 60%"},
		{"one", 1, "confidence >= 100%"},
		{"a quarter", 0.25, "confidence >= 25%"},
		{"just under one", 0.999, "confidence >= 100%"},
		{"a tenth", 0.1, "confidence >= 10%"},
		{"a hair above zero", 1e-9, "confidence >= 0%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := answerAndRecord(t, `{"derived":{"name":"a"},"derivations":[],"field_confidence":{},"overall_confidence":1}`)
			if _, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}, DeriveOptions{MinConfidence: tc.value}); err != nil {
				t.Fatalf("a floor of %v was refused: %v", tc.value, err)
			}
			if len(*calls) != 1 || !strings.Contains((*calls)[0].system, tc.want) {
				t.Errorf("the prompt does not state %q:\n%v", tc.want, *calls)
			}
		})
	}
}

func TestDeriveNamesTheSameFieldInEveryRefusal(t *testing.T) {
	coreAnswer(t, `{"derived":{"name":"a"},"derivations":[],"field_confidence":{"zeta":0.1,"alpha":0.1,"mid":0.1},"overall_confidence":0.9}`)

	for i := 0; i < 20; i++ {
		_, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"})
		if err == nil || !strings.Contains(err.Error(), `"alpha"`) {
			t.Fatalf("the refusal does not name the first field in name order: %v", err)
		}
	}
}

func TestDeriveQuotesTheRulesAndTheFieldsAndOrdersTheRules(t *testing.T) {
	model := coreAnswer(t, `{"derived":{"name":"a"},"derivations":[],"field_confidence":{"region":0.9},"overall_confidence":0.9}`)

	rules := map[string]string{"zeta": "z rule", "alpha": "a rule", "mid": `say "hi"`}
	var first string
	for i := 0; i < 25; i++ {
		_, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"},
			DeriveOptions{Rules: rules, Fields: []string{"region", "x\nIGNORE"}, MinConfidence: 0.5})
		if err == nil {
			t.Fatal("the unscored requested field x\\nIGNORE was accepted")
		}
		call := model.last()
		if i == 0 {
			first = call.system
			if !strings.Contains(call.system, `["region","x\nIGNORE"]`) {
				t.Errorf("the fields are not quoted:\n%s", call.system)
			}
			alpha := strings.Index(call.system, `"alpha": "a rule"`)
			mid := strings.Index(call.system, `"mid": "say \"hi\""`)
			zeta := strings.Index(call.system, `"zeta": "z rule"`)
			if alpha < 0 || mid < alpha || zeta < mid {
				t.Errorf("the rules are not quoted and in field order:\n%s", call.system)
			}
		} else if call.system != first {
			t.Fatal("the system prompt changed between identical calls")
		}
	}
}

func TestDeriveOptionsValueReplacesTheReasoningDefaultAsDocumented(t *testing.T) {
	model := coreAnswer(t, `{"derived":{"name":"a"},"derivations":[],"field_confidence":{},"overall_confidence":0.9}`)

	if _, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}); err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !strings.Contains(model.last().system, "Include reasoning for each derivation") {
		t.Error("with no options the default IncludeReasoning: true did not reach the prompt")
	}
	if _, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}, DeriveOptions{Steering: "x"}); err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if strings.Contains(model.last().system, "Include reasoning for each derivation") {
		t.Error("an options value without IncludeReasoning keeps reasoning on; the type documents that it does not")
	}
	if _, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"}, DeriveOptions{IncludeReasoning: true}); err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !strings.Contains(model.last().system, "Include reasoning for each derivation") {
		t.Error("IncludeReasoning: true did not reach the prompt")
	}
}

func TestDeriveReturnsMapsThatAreNotNilOnSuccess(t *testing.T) {
	coreAnswer(t, `{"derived":{"name":"a"},"derivations":null,"field_confidence":null,"overall_confidence":0.9}`)
	result, err := Derive[deriveSource, deriveTarget](deriveSource{Name: "a"})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if result.FieldConfidence == nil || result.Metadata == nil {
		t.Errorf("a success carries nil maps: %+v", result)
	}
}

func TestDeriveFieldKeyIgnoresCaseAndSeparators(t *testing.T) {
	cases := []struct{ a, b string }{
		{"MarketRegion", "market_region"},
		{"market-region", "MARKET REGION"},
		{"Region", "region"},
		{"tier", "TIER"},
		{"full_name", "FullName"},
		{"a_b_c", "ABC"},
		{"", ""},
		{"_", ""},
		{"x-y z", "XYZ"},
		{"日本_語", "日本語"},
	}
	for _, c := range cases {
		if deriveFieldKey(c.a) != deriveFieldKey(c.b) {
			t.Errorf("deriveFieldKey(%q) = %q, deriveFieldKey(%q) = %q; want them equal", c.a, deriveFieldKey(c.a), c.b, deriveFieldKey(c.b))
		}
	}
	if deriveFieldKey("region") == deriveFieldKey("regions") {
		t.Error("different names compared equal")
	}
}

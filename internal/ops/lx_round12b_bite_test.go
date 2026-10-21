package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 12, the tests that passed with the code they guard taken out,
// and the functions with fewer than ten direct cases.

type bite12Exported struct{ A int }

type bite12Unexported struct{ a int }

type bite12Mixed struct {
	a int
	B int
}

type bite12Dash struct {
	A int `json:"-"`
}

type bite12DashName struct {
	A int `json:"-,"`
}

type bite12Inner struct{ X int }

type bite12Hidden struct{ y int }

// An unexported type embedded in a struct has its exported fields promoted.
type bite12EmbedsInner struct{ bite12Inner }

type bite12EmbedsInnerPointer struct{ *bite12Inner }

type bite12EmbedsHidden struct{ bite12Hidden }

type bite12EmbedsInnerLeftOut struct {
	bite12Inner `json:"-"`
}

type bite12EmbedsInterface struct{ fmt.Stringer }

type bite12Count int

type bite12EmbedsUnexportedNumber struct{ bite12Count }

type bite12EmbedsDuration struct{ time.Duration }

type bite12Loop struct{ *bite12Loop }

type bite12PingEmbedsPong struct{ *bite12PongEmbedsPing }

type bite12PongEmbedsPing struct{ *bite12PingEmbedsPong }

// bite12Named writes itself as text, by a value receiver, and shows nothing JSON
// could otherwise see.
type bite12Named struct{ name string }

func (n bite12Named) MarshalText() ([]byte, error) { return []byte(n.name), nil }

// bite12Stamp writes itself as JSON, by a value receiver.
type bite12Stamp struct{ at string }

func (s bite12Stamp) MarshalJSON() ([]byte, error) { return json.Marshal(s.at) }

// bite12PointerNamed writes itself as text by a pointer receiver, which
// encoding/json calls only for a value it can address.
type bite12PointerNamed struct{ name string }

func (n *bite12PointerNamed) MarshalText() ([]byte, error) { return []byte(n.name), nil }

// decideSaysNothing judges the type of the situation, and so a type that says
// how it is written as text or JSON is not one that says nothing, whatever its
// fields. Decide's own path checks for both before it asks, so the question is
// asked here directly: nothing else reaches the branch for text.
func TestBite12DecideSaysNothing(t *testing.T) {
	pointerNamed := &bite12PointerNamed{name: "premium"}
	var pointerToPointer = &pointerNamed
	cases := []struct {
		name      string
		situation any
		want      bool
	}{
		{"nil", nil, false},
		{"text", "a ticket", false},
		{"a number", 42, false},
		{"a list", []int{}, false},
		{"a map", map[string]int{}, false},
		{"a struct with no fields at all", struct{}{}, false},
		{"a struct with an exported field", bite12Exported{}, false},
		{"a struct with nothing exported", bite12Unexported{a: 1}, true},
		{"a pointer to one", &bite12Unexported{a: 1}, true},
		{"a pointer to a pointer to one", func() any { p := &bite12Unexported{}; return &p }(), true},
		{"a struct with one exported field among others", bite12Mixed{a: 1}, false},
		{"a struct whose only field is left out", bite12Dash{A: 1}, true},
		{"a field whose name is a dash", bite12DashName{A: 1}, false},
		{"an embedded type with exported fields", bite12EmbedsInner{}, false},
		{"an embedded type with nothing exported", bite12EmbedsHidden{}, true},
		{"nothing exported, written as text by value", bite12Named{name: "premium"}, false},
		{"a pointer to one", &bite12Named{name: "premium"}, false},
		{"nothing exported, written as JSON by value", bite12Stamp{at: "today"}, false},
		{"nothing exported, written as text by pointer, given as a value", bite12PointerNamed{name: "premium"}, true},
		{"the same, given as a pointer", pointerNamed, false},
		{"a pointer to a pointer to one that writes itself by pointer", pointerToPointer, true},
		{"an error with nothing exported", errors.New("declined"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideSaysNothing(tc.situation); got != tc.want {
				t.Errorf("decideSaysNothing(%T) = %v, want %v", tc.situation, got, tc.want)
			}
		})
	}
}

// The members of a type follow encoding/json: an exported field is written, one
// tagged "-" is not, and the fields of an embedded struct are written as the
// embedding struct's own, whether or not the embedded type's name is exported.
// decideSaysNothing asks whether there is one.
func TestBite12HasWrittenField(t *testing.T) {
	cases := []struct {
		name string
		kind reflect.Type
		want bool
	}{
		{"an exported field", reflect.TypeOf(bite12Exported{}), true},
		{"an unexported field", reflect.TypeOf(bite12Unexported{}), false},
		{"one of each", reflect.TypeOf(bite12Mixed{}), true},
		{"an exported field left out", reflect.TypeOf(bite12Dash{}), false},
		{"a field named dash", reflect.TypeOf(bite12DashName{}), true},
		{"no fields", reflect.TypeOf(struct{}{}), false},
		{"an embedded struct with an exported field", reflect.TypeOf(bite12EmbedsInner{}), true},
		{"a pointer to one embedded", reflect.TypeOf(bite12EmbedsInnerPointer{}), true},
		{"an embedded struct with nothing exported", reflect.TypeOf(bite12EmbedsHidden{}), false},
		{"an embedded struct left out", reflect.TypeOf(bite12EmbedsInnerLeftOut{}), false},
		{"an embedded interface", reflect.TypeOf(bite12EmbedsInterface{}), true},
		{"an embedded number of an unexported type", reflect.TypeOf(bite12EmbedsUnexportedNumber{}), false},
		{"an embedded number of an exported type", reflect.TypeOf(bite12EmbedsDuration{}), true},
		{"a struct that embeds a pointer to itself", reflect.TypeOf(bite12Loop{}), false},
		{"two that embed each other", reflect.TypeOf(bite12PingEmbedsPong{}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(jsonMembers(tc.kind)) > 0; got != tc.want {
				t.Errorf("%s has members: %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestBite12ValidateCount(t *testing.T) {
	cases := []struct {
		rules int
		want  string
	}{
		{0, "checked 0 field rules deterministically"},
		{1, "checked 1 field rules deterministically"},
		{2, "checked 2 field rules deterministically"},
		{9, "checked 9 field rules deterministically"},
		{10, "checked 10 field rules deterministically"},
		{11, "checked 11 field rules deterministically"},
		{99, "checked 99 field rules deterministically"},
		{100, "checked 100 field rules deterministically"},
		{1000, "checked 1000 field rules deterministically"},
		{123456, "checked 123456 field rules deterministically"},
	}
	for _, tc := range cases {
		if got := validateCount(tc.rules); got != tc.want {
			t.Errorf("validateCount(%d) = %q, want %q", tc.rules, got, tc.want)
		}
	}
	// It is the count that was given and nothing else.
	for _, rules := range []int{3, 7, 42} {
		if got := validateCount(rules); !strings.Contains(got, strconv.Itoa(rules)) || strings.Count(got, strconv.Itoa(rules)) != 1 {
			t.Errorf("validateCount(%d) = %q", rules, got)
		}
	}
}

type bite12Price struct {
	Amount float32 `json:"amount"`
}

type bite12Chan struct {
	Name string   `json:"name"`
	Feed chan int `json:"feed"`
}

type bite12Nested struct {
	Order lx025Order `json:"order"`
	Tags  []string   `json:"tags"`
}

type bite12NestedMarshaled struct {
	Order lx025Order `json:"order"`
	Who   lx12ID     `json:"who"`
}

// A correction is declared as the schema of the data only when that schema
// describes it. The assertions are on what a schema says, where they were
// inside an "if schema != nil" and held either way.
func TestBite12ValidateCorrectedSchemaDescribesThePlainTypesOnly(t *testing.T) {
	asJSON := func(schema map[string]any) string {
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("a schema that cannot be written: %v", err)
		}
		return string(encoded)
	}
	described := []struct {
		name   string
		schema map[string]any
		holds  []string
	}{
		{"a plain struct", validateCorrectedSchema[lx025Order](), []string{`"id"`, `"total"`, `"note"`, `"object"`}},
		{"a pointer to one", validateCorrectedSchema[*lx025Order](), []string{`"id"`, `"total"`, `"note"`}},
		{"a string", validateCorrectedSchema[string](), []string{`"string"`}},
		{"a whole number", validateCorrectedSchema[int](), []string{`"integer"`}},
		{"a truth value", validateCorrectedSchema[bool](), []string{`"boolean"`}},
		{"a list of text", validateCorrectedSchema[[]string](), []string{`"array"`, `"string"`}},
		{"a struct holding a struct and a list", validateCorrectedSchema[bite12Nested](), []string{`"order"`, `"tags"`, `"total"`}},
		{"a time", validateCorrectedSchema[time.Time](), []string{`"string"`}},
	}
	for _, tc := range described {
		t.Run(tc.name, func(t *testing.T) {
			if tc.schema == nil {
				t.Fatal("no schema was declared for a type that a schema describes")
			}
			encoded := asJSON(tc.schema)
			for _, want := range tc.holds {
				if !strings.Contains(encoded, want) {
					t.Errorf("the schema does not hold %s: %s", want, encoded)
				}
			}
		})
	}

	declinedFor := []struct {
		name   string
		schema map[string]any
	}{
		{"a struct with a field that writes itself as text", validateCorrectedSchema[bite12NestedMarshaled]()},
		{"a type that writes itself as text", validateCorrectedSchema[lx12ID]()},
		{"a type that writes itself as JSON", validateCorrectedSchema[lx12Stamped]()},
		{"raw JSON", validateCorrectedSchema[json.RawMessage]()},
		{"any", validateCorrectedSchema[any]()},
		{"an interface with methods", validateCorrectedSchema[fmt.Stringer]()},
		{"a map of text", validateCorrectedSchema[map[string]string]()},
		{"a map of anything", validateCorrectedSchema[map[string]any]()},
		{"a struct with nothing exported", validateCorrectedSchema[bite12Unexported]()},
		{"a struct with a float32", validateCorrectedSchema[bite12Price]()},
		{"a struct with a channel", validateCorrectedSchema[bite12Chan]()},
		{"a list of types that write themselves", validateCorrectedSchema[[]lx12ID]()},
		{"a pointer to one", validateCorrectedSchema[*lx12Stamped]()},
	}
	for _, tc := range declinedFor {
		if tc.schema != nil {
			t.Errorf("%s: a schema was declared for a type it cannot describe: %s", tc.name, asJSON(tc.schema))
		}
	}
}

// With a correction asked for and a type no schema describes, the answer goes
// out unconstrained and the result says the correction was not held to a
// schema. It says the opposite when it was, and says nothing about corrections
// when none was asked for.
func TestBite12ValidateSaysWhenTheCorrectionWasNotConstrained(t *testing.T) {
	const reply = `{"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"s","corrected":null}`
	rules := NewValidateOptions().WithRules("the email has to be on example.com")

	key := "correction_schema_constrained"
	cases := []struct {
		name    string
		run     func() (map[string]any, error)
		present bool
		want    bool
	}{
		{"a plain type, corrected", func() (map[string]any, error) {
			answering(t, reply)
			result, err := Validate(lx025Subject, rules.WithAutoCorrect(true))
			return result.Metadata, err
		}, true, true},
		{"a plain type, not corrected", func() (map[string]any, error) {
			answering(t, lx025Clean)
			result, err := Validate(lx025Subject, rules.WithAutoCorrect(false))
			return result.Metadata, err
		}, false, false},
		{"a type that writes itself as text, corrected", func() (map[string]any, error) {
			answering(t, reply)
			result, err := Validate(lx12User{Email: "x"}, rules.WithAutoCorrect(true))
			return result.Metadata, err
		}, true, false},
		{"a type that writes itself as text, not corrected", func() (map[string]any, error) {
			answering(t, lx025Clean)
			result, err := Validate(lx12User{Email: "x"}, rules.WithAutoCorrect(false))
			return result.Metadata, err
		}, false, false},
		{"a map, corrected", func() (map[string]any, error) {
			answering(t, reply)
			result, err := Validate(map[string]any{"email": "x"}, rules.WithAutoCorrect(true))
			return result.Metadata, err
		}, true, false},
		{"a map, not corrected", func() (map[string]any, error) {
			answering(t, lx025Clean)
			result, err := Validate(map[string]any{"email": "x"}, rules.WithAutoCorrect(false))
			return result.Metadata, err
		}, false, false},
		{"raw JSON, corrected", func() (map[string]any, error) {
			answering(t, reply)
			result, err := Validate(json.RawMessage(`{"email":"x"}`), rules.WithAutoCorrect(true))
			return result.Metadata, err
		}, true, false},
		{"a list, corrected", func() (map[string]any, error) {
			answering(t, reply)
			result, err := Validate([]string{"x"}, rules.WithAutoCorrect(true))
			return result.Metadata, err
		}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata, err := tc.run()
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			got, present := metadata[key].(bool)
			if present != tc.present || (present && got != tc.want) {
				t.Errorf("%s = %v (present %v), want %v (present %v)", key, got, present, tc.want, tc.present)
			}
			if tc.present && tc.want != (metadata["schema_declared"] == true) {
				t.Errorf("the correction is said to be constrained = %v, and a schema was declared = %v", tc.want, metadata["schema_declared"])
			}
		})
	}
}

// The model's answer is not written to the log by Arbitrate, whether it is
// accepted or refused. The log carried "response" for every call.
func TestBite12ArbitrateDoesNotLogTheAnswer(t *testing.T) {
	const marker = "ZX-SECRET-IN-THE-ANSWER"
	accepted := func() string {
		body := lx024Answer("0.8", []float64{0.9}, []float64{0.5}, []float64{0.2})
		body = strings.Replace(body, `"reasoning":"overall"`, `"reasoning":"`+marker+`"`, 1)
		return strings.ReplaceAll(body, `"reasoning":"because"`, `"reasoning":"`+marker+`"`)
	}
	cases := []struct {
		name      string
		response  string
		reasoning bool
		wantErr   bool
	}{
		{"accepted", accepted(), false, false},
		{"accepted, with a reason for every evaluation", accepted(), true, false},
		{"a score off the scale", strings.Replace(accepted(), `"score":0.9`, `"score":9`, 1), false, true},
		{"a rule not reported on", `{"options":{"o1":{},"o2":{},"o3":{}},"reasoning":"` + marker + `","confidence":0.8}`, false, true},
		{"not JSON", "I think " + marker + " is the better one.", false, true},
		{"a confidence that is missing", `{"options":{"o1":{"r1":{"passed":true,"score":0.9}}},"reasoning":"` + marker + `"}`, false, true},
		{"a winner named in the answer", `{"winner":"` + marker + `"}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			_, _, err := arbitrateWith(t, tc.response, lx024Vendors, ArbitrateOptions{Rules: []string{"cheap"}, IncludeReasoning: tc.reasoning, Tiebreaker: "first"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error = %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), marker) {
				t.Errorf("the error carries the answer: %v", err)
			}
			if logs() == "" {
				t.Fatal("the operation logged nothing, so this witnesses nothing")
			}
			if strings.Contains(logs(), marker) {
				t.Errorf("the log carries the answer:\n%s", logs())
			}
		})
	}
}

// An answer that reports on a rule that was not given is refused, whichever
// option it is on and however the extra is named.
func TestBite12ArbitrateAnswerHoldsEveryOptionToTheRulesGiven(t *testing.T) {
	rule := `{"passed":true,"score":0.9,"reasoning":"x"}`
	option := func(keys ...string) string {
		parts := make([]string, len(keys))
		for i, key := range keys {
			parts[i] = `"` + key + `":` + rule
		}
		return `{` + strings.Join(parts, ",") + `}`
	}
	answer := func(first, second string) string {
		return `{"options":{"o1":` + first + `,"o2":` + second + `},"reasoning":"x","confidence":0.8}`
	}
	cases := []struct {
		name      string
		response  string
		reasoning bool
		wantErr   bool
	}{
		{"exactly the rules given", answer(option("r1", "r2"), option("r1", "r2")), false, false},
		{"a rule too many on the first option", answer(option("r1", "r2", "r3"), option("r1", "r2")), false, true},
		{"a rule too many on the second option", answer(option("r1", "r2"), option("r1", "r2", "r3")), false, true},
		{"a key that is no rule's, in place of a third", answer(option("r1", "r2", "x"), option("r1", "r2")), false, true},
		{"a rule too few on the first option", answer(option("r1"), option("r1", "r2")), false, true},
		{"a rule too few on the second option", answer(option("r1", "r2"), option("r2")), false, true},
		{"the right number under the wrong keys", answer(option("r1", "r3"), option("r1", "r2")), false, true},
		{"no rules at all for an option", answer(`{}`, option("r1", "r2")), false, true},
		{"an option too many", `{"options":{"o1":` + option("r1", "r2") + `,"o2":` + option("r1", "r2") + `,"o3":` + option("r1", "r2") + `},"reasoning":"x","confidence":0.8}`, false, true},
		{"an option too few", `{"options":{"o1":` + option("r1", "r2") + `},"reasoning":"x","confidence":0.8}`, false, true},
		{"a rule reported as null", answer(`{"r1":`+rule+`,"r2":null}`, option("r1", "r2")), false, true},
		{"reasons asked for and given", answer(option("r1", "r2"), option("r1", "r2")), true, false},
		{"reasons asked for and missing", answer(`{"r1":{"passed":true,"score":0.9},"r2":`+rule+`}`, option("r1", "r2")), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evaluations, _, _, err := arbitrateAnswer(tc.response, 2, []string{"a", "b"}, tc.reasoning)
			if tc.wantErr {
				if types.KindOf(err) != types.KindSchemaViolation || evaluations != nil {
					t.Fatalf("evaluations %+v, err %v; want a refusal and nothing with it", evaluations, err)
				}
				return
			}
			if err != nil || len(evaluations) != 2 || len(evaluations[0].RuleResults) != 2 {
				t.Fatalf("evaluations %+v, err %v", evaluations, err)
			}
		})
	}
}

// The size of what an arbitration declares is worked out the same way for every
// count of options and rules, and a call is refused when it is over the budget
// and not when it is at it.
func TestBite12ArbitrateSizeBoundary(t *testing.T) {
	// 3 + options + options*rules*(1 + fields): fields is 2, or 3 with reasons.
	cases := []struct {
		name      string
		options   int
		rules     int
		reasoning bool
		declared  int
	}{
		{"nothing", 0, 0, false, 3},
		{"one by one", 1, 1, false, 7},
		{"one by one, with reasons", 1, 1, true, 8},
		{"three by two", 3, 2, false, 3 + 3 + 3*2*3},
		{"three by two, with reasons", 3, 2, true, 3 + 3 + 3*2*4},
		{"one under, without reasons", 4, 416, false, 4999},
		{"exactly at, without reasons", 263, 6, false, 5000},
		{"one over, without reasons", 3, 555, false, 5001},
		{"one under, with reasons", 4, 312, true, 4999},
		{"exactly at, with reasons", 1, 1249, true, 5000},
		{"one over, with reasons", 6, 208, true, 5001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The formula, written out from the schema it stands for.
			if got := arbitrateSchemaProperties(tc.options, tc.rules, tc.reasoning); got != tc.declared {
				t.Fatalf("arbitrateSchemaProperties(%d, %d, %v) = %d, want %d", tc.options, tc.rules, tc.reasoning, got, tc.declared)
			}
			if tc.options == 0 {
				return
			}

			over := tc.declared > strictPropertyBudget
			if over != (tc.declared == 5001) {
				t.Fatalf("the case is mislabelled: %d against %d", tc.declared, strictPropertyBudget)
			}
			options := make([]lx024Vendor, tc.options)
			rules := make([]string, tc.rules)
			scores := make([]float64, tc.rules)
			for j := range rules {
				rules[j] = "rule " + strconv.Itoa(j+1)
				scores[j] = 0.5
			}
			answers := make([][]float64, tc.options)
			for i := range answers {
				answers[i] = scores
			}
			result, sent, err := arbitrateWith(t, lx024Answer("0.8", answers...), options,
				ArbitrateOptions{Rules: rules, IncludeReasoning: tc.reasoning, RequireAllRules: true, Tiebreaker: "first"})
			if over {
				if types.KindOf(err) != types.KindConfiguration || sent.calls != 0 {
					t.Errorf("err %v after %d calls; want a configuration error before any", err, sent.calls)
				}
				if len(result.Evaluations) != 0 {
					t.Errorf("evaluations came back beside the refusal")
				}
				return
			}
			if err != nil {
				t.Errorf("an arbitration within the budget was refused: %v", err)
			}
		})
	}
}

// RunOpBatch on its own: encode, call, merge, hold every output to the
// invariants and normalize it, then the whole-set check.
func TestBite12RunOpBatchDirectly(t *testing.T) {
	type call struct{ system, user string }
	var seen []call
	reply := func(body string, err error) {
		seen = nil
		swapLLMCaller(t, func(_ context.Context, system, user string, _ types.OpOptions) (string, error) {
			seen = append(seen, call{system, user})
			return answerWithOfferedIDs(user, body), err
		})
	}
	lower := func(word string) string { return strings.ToLower(word) }
	trimmed := OutputContract[string]{Normalize: func(word string) string { return strings.TrimSpace(word) }}

	t.Run("a batch with nothing wired", func(t *testing.T) {
		reply("a,b", nil)
		op := Op[string, string]{ID: types.OperationID{Name: "words", Version: "v1"}}
		op.Batch.Class = types.BatchItemwise
		outputs, err := RunOpBatch(context.Background(), op, []string{"x", "y"}, types.OpOptions{})
		if err == nil || outputs != nil || len(seen) != 0 {
			t.Errorf("outputs %v, err %v, %d calls", outputs, err, len(seen))
		}
	})
	t.Run("no items", func(t *testing.T) {
		reply("a,b", nil)
		outputs, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{}), nil, types.OpOptions{})
		if err != nil || outputs != nil || len(seen) != 0 {
			t.Errorf("outputs %v, err %v, %d calls", outputs, err, len(seen))
		}
	})
	t.Run("a batch that cannot be encoded", func(t *testing.T) {
		reply("a,b", nil)
		op := lx11BatchOp(OutputContract[string]{})
		op.Batch.Encode = func([]string, types.OpOptions) (string, string, error) { return "", "", errors.New("cannot encode") }
		outputs, err := RunOpBatch(context.Background(), op, []string{"x", "y"}, types.OpOptions{})
		if err == nil || outputs != nil || len(seen) != 0 {
			t.Errorf("outputs %v, err %v, %d calls", outputs, err, len(seen))
		}
	})
	t.Run("a call that fails", func(t *testing.T) {
		reply("", errors.New("the provider is down"))
		outputs, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{}), []string{"x", "y"}, types.OpOptions{})
		if err == nil || outputs != nil || len(seen) != 1 {
			t.Errorf("outputs %v, err %v, %d calls", outputs, err, len(seen))
		}
	})
	t.Run("an answer the merge cannot read", func(t *testing.T) {
		reply("a,b,c", nil)
		outputs, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{}), []string{"x", "y"}, types.OpOptions{})
		if err == nil || outputs != nil {
			t.Errorf("outputs %v, err %v", outputs, err)
		}
		if err != nil && strings.Contains(err.Error(), "a,b,c") {
			t.Errorf("the error carries the answer: %v", err)
		}
	})
	t.Run("a merge that returns the wrong number of outputs", func(t *testing.T) {
		reply("a,b", nil)
		op := lx11BatchOp(OutputContract[string]{})
		op.Batch.Merge = func(string, []string) ([]string, error) { return []string{"only one"}, nil }
		outputs, err := RunOpBatch(context.Background(), op, []string{"x", "y"}, types.OpOptions{})
		if types.KindOf(err) != types.KindInvariantViolation || outputs != nil {
			t.Errorf("outputs %v, err %v", outputs, err)
		}
	})
	t.Run("an aggregate may merge into any number", func(t *testing.T) {
		reply("a,b", nil)
		op := lx11BatchOp(OutputContract[string]{})
		op.Batch.Class = types.BatchAggregate
		op.Batch.Merge = func(string, []string) ([]string, error) { return []string{"one"}, nil }
		outputs, err := RunOpBatch(context.Background(), op, []string{"x", "y"}, types.OpOptions{})
		if err != nil || len(outputs) != 1 {
			t.Errorf("outputs %v, err %v", outputs, err)
		}
	})
	t.Run("invariants that hold", func(t *testing.T) {
		reply("a,b", nil)
		outputs, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{Invariants: []func(string) error{lx11NotShouting}}), []string{"x", "y"}, types.OpOptions{})
		if err != nil || !reflect.DeepEqual(outputs, []string{"a", "b"}) {
			t.Errorf("outputs %v, err %v", outputs, err)
		}
	})
	for position, body := range map[string]string{"first": "A,b,c", "middle": "a,B,c", "last": "a,b,C"} {
		t.Run("an invariant broken by the "+position+" output", func(t *testing.T) {
			reply(body, nil)
			outputs, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{Invariants: []func(string) error{lx11NotShouting}}), []string{"x", "y", "z"}, types.OpOptions{})
			if types.KindOf(err) != types.KindInvariantViolation || outputs != nil {
				t.Errorf("outputs %v, err %v", outputs, err)
			}
		})
	}
	t.Run("every invariant is run on every output", func(t *testing.T) {
		reply("a,b,c", nil)
		ran := 0
		counting := func(string) error { ran++; return nil }
		_, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{Invariants: []func(string) error{counting, counting}}), []string{"x", "y", "z"}, types.OpOptions{})
		if err != nil || ran != 6 {
			t.Errorf("err %v, %d invariant runs, want 6", err, ran)
		}
	})
	t.Run("outputs are normalized", func(t *testing.T) {
		reply(" a , b ", nil)
		outputs, err := RunOpBatch(context.Background(), lx11BatchOp(trimmed), []string{"x", "y"}, types.OpOptions{})
		if err != nil || !reflect.DeepEqual(outputs, []string{"a", "b"}) {
			t.Errorf("outputs %q, err %v", outputs, err)
		}
	})
	t.Run("an invariant sees the output as it was merged", func(t *testing.T) {
		reply("A,B", nil)
		contract := OutputContract[string]{Normalize: lower, Invariants: []func(string) error{lx11NotShouting}}
		outputs, err := RunOpBatch(context.Background(), lx11BatchOp(contract), []string{"x", "y"}, types.OpOptions{})
		if types.KindOf(err) != types.KindInvariantViolation || outputs != nil {
			t.Errorf("outputs %v, err %v", outputs, err)
		}
	})
	t.Run("a whole-set check that holds, and one that does not", func(t *testing.T) {
		for _, passes := range []bool{true, false} {
			reply("a,b", nil)
			op := lx11BatchOp(OutputContract[string]{})
			op.Batch.Class = types.BatchAggregate
			op.Batch.GlobalValidate = func([]string, []string) error {
				if passes {
					return nil
				}
				return errors.New("the set is wrong")
			}
			outputs, err := RunOpBatch(context.Background(), op, []string{"x", "y"}, types.OpOptions{})
			if passes && (err != nil || len(outputs) != 2) {
				t.Errorf("a set that holds: outputs %v, err %v", outputs, err)
			}
			if !passes && (err == nil || outputs != nil) {
				t.Errorf("a set that does not: outputs %v, err %v", outputs, err)
			}
		}
	})
	t.Run("the whole-set check is given the outputs as they will be returned", func(t *testing.T) {
		reply(" a , b ", nil)
		op := lx11BatchOp(trimmed)
		op.Batch.Class = types.BatchAggregate
		var given []string
		op.Batch.GlobalValidate = func(_ []string, outputs []string) error { given = append([]string(nil), outputs...); return nil }
		if _, err := RunOpBatch(context.Background(), op, []string{"x", "y"}, types.OpOptions{}); err != nil || !reflect.DeepEqual(given, []string{"a", "b"}) {
			t.Errorf("err %v, the check was given %q", err, given)
		}
	})
	t.Run("the batch is sent as the operation encodes it", func(t *testing.T) {
		reply("a,b", nil)
		if _, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{}), []string{"x", "y"}, types.OpOptions{}); err != nil {
			t.Fatalf("RunOpBatch: %v", err)
		}
		if len(seen) != 1 || seen[0].system != "one word for each item" || seen[0].user != "x,y" {
			t.Errorf("sent %+v", seen)
		}
	})
	t.Run("a batch on the id protocol", func(t *testing.T) {
		reply(lx12Words("a", "b"), nil)
		outputs, err := RunOpBatch(context.Background(), lx12WordsOp(OutputContract[string]{}), []string{"x", "y"}, types.OpOptions{})
		if err != nil || !reflect.DeepEqual(outputs, []string{"a", "b"}) {
			t.Errorf("outputs %v, err %v", outputs, err)
		}
	})
	t.Run("a batch on the id protocol that drops an item", func(t *testing.T) {
		reply(lx12Words("a"), nil)
		outputs, err := RunOpBatch(context.Background(), lx12WordsOp(OutputContract[string]{}), []string{"x", "y"}, types.OpOptions{})
		if err == nil || outputs != nil {
			t.Errorf("outputs %v, err %v", outputs, err)
		}
	})
}

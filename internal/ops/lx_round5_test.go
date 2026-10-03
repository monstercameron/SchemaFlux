package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// promptJSON writes a value as it will read in a prompt. encoding/json escapes
// "<", ">" and "&" for the benefit of HTML, which a model is not: "R&D" went
// into a prompt as "R\u0026D", four tokens where one would do, and not the
// text the caller wrote.
func TestPromptJSON(t *testing.T) {
	type record struct {
		Name string `json:"name"`
		Note string `json:"note,omitempty"`
	}
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"an ampersand", "R&D", `"R&D"`},
		{"angle brackets", "<b>bold</b>", `"<b>bold</b>"`},
		{"a comparison", "a < b && b > c", `"a < b && b > c"`},
		{"a quote is still escaped", `say "hi"`, `"say \"hi\""`},
		{"a newline is still escaped", "one\ntwo", `"one\ntwo"`},
		{"a backslash is still escaped", `C:\dir`, `"C:\\dir"`},
		{"non-ASCII is left as it is", "café 東京", `"café 東京"`},
		{"a struct", record{Name: "Q&A", Note: "<draft>"}, `{"name":"Q&A","note":"<draft>"}`},
		{"a map, keys sorted", map[string]any{"b": 1, "a": "x&y"}, `{"a":"x&y","b":1}`},
		{"a list", []string{"a&b", "c"}, `["a&b","c"]`},
		{"a number", 42, `42`},
		{"nil", nil, `null`},
		{"no trailing newline", "x", `"x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := promptJSON(tc.value)
			if err != nil {
				t.Fatalf("promptJSON: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("promptJSON = %s, want %s", got, tc.want)
			}
			// It is still JSON, and reads back as the same value.
			var back, original any
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatalf("the output is not JSON: %v", err)
			}
			plain, _ := json.Marshal(tc.value)
			_ = json.Unmarshal(plain, &original)
			if !reflect.DeepEqual(back, original) {
				t.Fatalf("read back as %v, want %v", back, original)
			}
		})
	}

	if _, err := promptJSON(make(chan int)); err == nil {
		t.Error("a value that cannot be written was accepted")
	}
}

// The text a caller wrote reaches the model as they wrote it.
func TestPromptsCarryPunctuationAsWritten(t *testing.T) {
	t.Run("a Similar aspect", func(t *testing.T) {
		_, err, sent := similarWith(t, `{"score":0.8,"aspects":{"a1":{"score":0.9},"a2":{"score":0.9}},"explanation":"x"}`,
			NewSimilarOptions().WithAspects([]string{"R&D spend", "tone <formal>\nor casual"}))
		if err != nil {
			t.Fatalf("Similar: %v", err)
		}
		for _, line := range []string{`a1: "R&D spend"` + "\n", `a2: "tone <formal>\nor casual"` + "\n"} {
			if !strings.Contains(sent.user, line) {
				t.Errorf("the request has no line %q:\n%s", line, sent.user)
			}
		}
		// The schema carries no aspect text: it is in the request, under its key.
		entry, _ := schemaProperties(t, sent.opts.JSONSchema)["aspects"].(map[string]any)["properties"].(map[string]any)["a1"].(map[string]any)
		if _, described := entry["description"]; described {
			t.Errorf("the schema describes the aspect as %v", entry["description"])
		}
	})

	t.Run("Deduplicate items", func(t *testing.T) {
		_, err, sent := dedupWith(t, []string{"AT&T <billing>", "Q&A"}, 0.8, `{"duplicates":[]}`)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		for _, line := range []string{`0: "AT&T <billing>"`, `1: "Q&A"`} {
			if !strings.Contains(sent.user, line) {
				t.Errorf("the request has no line %q:\n%s", line, sent.user)
			}
		}
	})

	t.Run("Verify sources", func(t *testing.T) {
		_, err, sent := verifyWith(t, "content", verifyAnswer("[]", "[]", "[]"),
			NewVerifyOptions().WithMinConfidence(0).WithSources([]any{"Smith & Sons <1999>"}))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if !strings.Contains(sent.user, `"Smith & Sons <1999>"`) {
			t.Errorf("the source is not in the request as written:\n%s", sent.user)
		}
	})
}

// severityAnswer decodes itself and accepts two words only.
type severityAnswer string

func (s *severityAnswer) UnmarshalJSON(raw []byte) error {
	var word string
	if err := json.Unmarshal(raw, &word); err != nil {
		return err
	}
	if word != "low" && word != "high" {
		return errors.New("severity must be low or high")
	}
	*s = severityAnswer(word)
	return nil
}

// levelAnswer decodes itself from text.
type levelAnswer string

func (l *levelAnswer) UnmarshalText(raw []byte) error {
	if string(raw) != "one" && string(raw) != "two" {
		return errors.New("level must be one or two")
	}
	*l = levelAnswer(raw)
	return nil
}

// A text answer type that decodes itself has the last word on what it accepts.
// The fallback that reads a number as its text ran after such a type had
// refused the answer, and set it anyway.
func TestQuestionLeavesSelfDecodingTypesToDecodeThemselves(t *testing.T) {
	type plain string
	type verdict struct {
		Eligible bool `json:"eligible"`
	}

	ask := func(run func() (any, error), body string) (any, error) {
		installAnalysisResponse(t, body)
		return run()
	}
	// What these cases are about is the answer, so nothing else is asked for.
	answerOnly := NewQuestionOptions("q?").WithIncludeConfidence(false).WithIncludeReasoning(false).WithIncludeEvidence(false)
	severityQ := func() (any, error) {
		r, err := Question[string, severityAnswer]("data", answerOnly)
		return r.Answer, err
	}
	levelQ := func() (any, error) {
		r, err := Question[string, levelAnswer]("data", answerOnly)
		return r.Answer, err
	}
	plainQ := func() (any, error) {
		r, err := Question[string, plain]("data", answerOnly)
		return r.Answer, err
	}
	stringQ := func() (any, error) {
		r, err := Question[string, string]("data", answerOnly)
		return r.Answer, err
	}
	structQ := func() (any, error) {
		r, err := Question[string, verdict]("data", answerOnly)
		return r.Answer, err
	}
	intQ := func() (any, error) {
		r, err := Question[string, int]("data", answerOnly)
		return r.Answer, err
	}

	cases := []struct {
		name    string
		run     func() (any, error)
		body    string
		want    any
		wantErr bool
	}{
		{"a JSON decoder accepts its word", severityQ, `{"answer":"high"}`, severityAnswer("high"), false},
		{"a JSON decoder refuses another word", severityQ, `{"answer":"medium"}`, severityAnswer(""), true},
		{"a JSON decoder refuses a number", severityQ, `{"answer":7}`, severityAnswer(""), true},
		{"a text decoder accepts its word", levelQ, `{"answer":"two"}`, levelAnswer("two"), false},
		{"a text decoder refuses another word", levelQ, `{"answer":"nine"}`, levelAnswer(""), true},
		{"a text decoder refuses a number", levelQ, `{"answer":2}`, levelAnswer(""), true},
		{"a plain named string takes a number as text", plainQ, `{"answer":7}`, plain("7"), false},
		{"a string takes an exponent as written", stringQ, `{"answer":1e3}`, "1e3", false},
		{"a string takes a negative number", stringQ, `{"answer":-0.5}`, "-0.5", false},
		{"a struct given a number", structQ, `{"answer":7}`, verdict{}, true},
		{"a struct given text", structQ, `{"answer":"yes"}`, verdict{}, true},
		{"an int given text", intQ, `{"answer":"seven"}`, 0, true},
		{"an int given a fraction", intQ, `{"answer":7.5}`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ask(tc.run, tc.body)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			// On an error the answer is the zero value, never a half-decoded one.
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Answer = %#v, want %#v", got, tc.want)
			}
			if tc.wantErr && types.KindOf(err) != types.KindSchemaViolation {
				t.Errorf("kind = %v, want a schema violation", types.KindOf(err))
			}
		})
	}
}

func TestIsJSONScalar(t *testing.T) {
	cases := map[string]bool{
		"true": true, "false": true, "0": true, "42": true, "-1": true, "3.14": true, "1e3": true, "-0": true,
		`"text"`: false, "{}": false, "[]": false, "null": false, "": false, "NaN": false, "tru": false, " 1": false,
	}
	for raw, want := range cases {
		if got := isJSONScalar(raw); got != want {
			t.Errorf("isJSONScalar(%q) = %v, want %v", raw, got, want)
		}
	}
}

// Two items that are written the same are the same item, and Go can see that
// without asking. Only the distinct items are sent, the answer is in their
// numbers, and when at most one is distinct nothing is sent at all. An answer
// of "no duplicates" for ["a","b","a"] used to return three unique items.
func TestDeduplicateCollapsesIdenticalItemsItself(t *testing.T) {
	type customer struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	t.Run("identical strings the model says nothing about", func(t *testing.T) {
		result, err, sent := dedupWith(t, []string{"a", "b", "a"}, 0.8, `{"duplicates":[]}`)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if !reflect.DeepEqual(result.UniqueIndices, []int{0, 1}) || !reflect.DeepEqual(result.DuplicateIndices, [][]int{{0, 2}}) {
			t.Errorf("UniqueIndices = %v, DuplicateIndices = %v", result.UniqueIndices, result.DuplicateIndices)
		}
		if !strings.Contains(sent.user, `0: "a"`) || !strings.Contains(sent.user, `1: "b"`) || strings.Contains(sent.user, "\n2: ") {
			t.Errorf("the request does not list the two distinct items alone:\n%s", sent.user)
		}
	})

	t.Run("the answer is in the numbers the model was shown", func(t *testing.T) {
		// Distinct items: 0 "x", 1 "y", 2 "Y" (the caller's item 3).
		result, err, _ := dedupWith(t, []string{"x", "y", "x", "Y"}, 0.8, `{"duplicates":[{"index":2,"duplicate_of":1,"similarity":0.95}]}`)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if !reflect.DeepEqual(result.UniqueIndices, []int{0, 1}) || !reflect.DeepEqual(result.DuplicateIndices, [][]int{{0, 2}, {1, 3}}) {
			t.Errorf("UniqueIndices = %v, DuplicateIndices = %v", result.UniqueIndices, result.DuplicateIndices)
		}
		if result.TotalRemoved != 2 {
			t.Errorf("TotalRemoved = %d, want 2", result.TotalRemoved)
		}
	})

	t.Run("an index past the distinct items is refused", func(t *testing.T) {
		// Four items, three distinct: 3 is not a number the model was shown.
		_, err, _ := dedupWith(t, []string{"x", "y", "x", "Y"}, 0.8, `{"duplicates":[{"index":3,"duplicate_of":1,"similarity":0.95}]}`)
		if types.KindOf(err) != types.KindInvariantViolation {
			t.Errorf("err = %v, want an invariant violation", err)
		}
	})

	t.Run("an identical copy joins the group its original is in", func(t *testing.T) {
		// Distinct: 0 "Dana Kim", 1 "D. Kim". Item 2 is identical to item 1.
		result, err, _ := dedupWith(t, []string{"Dana Kim", "D. Kim", "D. Kim"}, 0.8, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9}]}`)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if !reflect.DeepEqual(result.DuplicateIndices, [][]int{{0, 1, 2}}) {
			t.Errorf("DuplicateIndices = %v, want one group of all three", result.DuplicateIndices)
		}
	})

	t.Run("identical copies are kept together under any threshold", func(t *testing.T) {
		result, err, _ := dedupWith(t, []string{"a", "b", "a"}, 1, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.6}]}`)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if !reflect.DeepEqual(result.DuplicateIndices, [][]int{{0, 2}}) {
			t.Errorf("DuplicateIndices = %v", result.DuplicateIndices)
		}
	})

	t.Run("nothing is sent when every item is the same", func(t *testing.T) {
		for _, items := range [][]string{{"a", "a"}, {"a", "a", "a", "a"}} {
			result, err, sent := dedupWith(t, items, 0.8, `not json`)
			if err != nil {
				t.Fatalf("Deduplicate: %v", err)
			}
			if sent.calls != 0 {
				t.Errorf("%d identical items: the provider was called", len(items))
			}
			if !reflect.DeepEqual(result.UniqueIndices, []int{0}) || len(result.DuplicateIndices) != 1 || len(result.DuplicateIndices[0]) != len(items) {
				t.Errorf("%d identical items: UniqueIndices = %v, DuplicateIndices = %v", len(items), result.UniqueIndices, result.DuplicateIndices)
			}
			if result.TotalRemoved != len(items)-1 || !reflect.DeepEqual(result.Unique, []string{"a"}) {
				t.Errorf("%d identical items: Unique = %v, TotalRemoved = %d", len(items), result.Unique, result.TotalRemoved)
			}
		}
	})

	t.Run("records that are written the same", func(t *testing.T) {
		items := []customer{{"Dana", "d@example.com"}, {"Omar", "o@example.com"}, {"Dana", "d@example.com"}}
		previousCaller, previousProvider := currentHooks()
		setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
			return `{"duplicates":[]}`, nil
		})
		t.Cleanup(func() {
			setLLMCaller(previousCaller)
			SetDefaultProvider(previousProvider)
		})
		result, err := Deduplicate(items, 0.8)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if !reflect.DeepEqual(result.DuplicateIndices, [][]int{{0, 2}}) || len(result.Unique) != 2 {
			t.Errorf("DuplicateIndices = %v, Unique = %v", result.DuplicateIndices, result.Unique)
		}
	})

	t.Run("items that differ only in case are not identical", func(t *testing.T) {
		result, err, sent := dedupWith(t, []string{"a", "A"}, 0.8, `{"duplicates":[]}`)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		if sent.calls != 1 || len(result.Unique) != 2 {
			t.Errorf("calls = %d, Unique = %v; want the model asked and both kept", sent.calls, result.Unique)
		}
	})
}

// An index has to be a whole number. A fraction, or a whole number written as
// one, is not a number the model was shown.
func TestDeduplicateRefusesAnIndexThatIsNotAWholeNumber(t *testing.T) {
	for _, response := range []string{
		`{"duplicates":[{"index":2.5,"duplicate_of":0,"similarity":0.9}]}`,
		`{"duplicates":[{"index":2,"duplicate_of":0.5,"similarity":0.9}]}`,
		`{"duplicates":[{"index":"2","duplicate_of":0,"similarity":0.9}]}`,
		`{"duplicates":[{"index":99999999999999999999,"duplicate_of":0,"similarity":0.9}]}`,
	} {
		result, err, _ := dedupWith(t, dedupFive, 0.8, response)
		if err == nil || len(result.Unique) != 0 {
			t.Errorf("%s: err = %v, Unique = %v", response, err, result.Unique)
		}
	}
}

// What comes back is the caller's to change: it does not share memory with the
// list that went in.
func TestDeduplicateResultDoesNotAliasTheInput(t *testing.T) {
	for _, items := range [][]string{{"only"}, {"a", "b"}} {
		original := append([]string(nil), items...)
		result, err, _ := dedupWith(t, items, 0.8, `{"duplicates":[]}`)
		if err != nil {
			t.Fatalf("Deduplicate: %v", err)
		}
		for i := range result.Unique {
			result.Unique[i] = "changed"
		}
		if !reflect.DeepEqual(items, original) {
			t.Errorf("changing the result changed the input: %v", items)
		}
	}

	result, err, _ := dedupWith(t, nil, 0.8, `{"duplicates":[]}`)
	if err != nil || result.Unique == nil || result.Duplicates == nil || len(result.Unique) != 0 {
		t.Errorf("no items: Unique = %#v, Duplicates = %#v, err = %v", result.Unique, result.Duplicates, err)
	}
	one, err, _ := dedupWith(t, []string{"only"}, 0.8, `{"duplicates":[]}`)
	if err != nil || !reflect.DeepEqual(one.Unique, []string{"only"}) || !reflect.DeepEqual(one.UniqueIndices, []int{0}) {
		t.Errorf("one item: Unique = %v, UniqueIndices = %v, err = %v", one.Unique, one.UniqueIndices, err)
	}
}

// deduplicateLinks is the check between the model's answer and the grouping. A
// member an entry leaves out never gets this far: the reader refuses the answer
// first (TestDeduplicateRefusesAnAnswerMissingAMemberItAsksFor).
func TestDeduplicateLinks(t *testing.T) {
	entry := func(index, target int, similarity float64) deduplicateEntry {
		return deduplicateEntry{Index: index, DuplicateOf: target, Similarity: similarity}
	}
	cases := []struct {
		name      string
		entries   []deduplicateEntry
		total     int
		threshold float64
		want      []int
		wantKind  types.ErrorKind
	}{
		{"no entries", nil, 3, 0.8, []int{-1, -1, -1}, types.KindUnknown},
		{"one link kept", []deduplicateEntry{entry(2, 0, 0.9)}, 3, 0.8, []int{-1, -1, 0}, types.KindUnknown},
		{"one link below the threshold", []deduplicateEntry{entry(2, 0, 0.7)}, 3, 0.8, []int{-1, -1, -1}, types.KindUnknown},
		{"a link at the threshold", []deduplicateEntry{entry(1, 0, 0.8)}, 2, 0.8, []int{-1, 0}, types.KindUnknown},
		{"two links", []deduplicateEntry{entry(1, 0, 1), entry(2, 1, 0.9)}, 3, 0.8, []int{-1, 0, 1}, types.KindUnknown},
		{"the same entry twice", []deduplicateEntry{entry(1, 0, 1), entry(1, 0, 1)}, 2, 0.8, []int{-1, 0}, types.KindUnknown},
		{"an item twice with two targets", []deduplicateEntry{entry(2, 0, 1), entry(2, 1, 1)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"an item twice with two scores", []deduplicateEntry{entry(2, 0, 1), entry(2, 0, 0.6)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"an index past the end", []deduplicateEntry{entry(3, 0, 1)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"a negative index", []deduplicateEntry{entry(-1, 0, 1)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"a target past the end", []deduplicateEntry{entry(1, 5, 1)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"a negative target", []deduplicateEntry{entry(1, -1, 1)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"itself", []deduplicateEntry{entry(1, 1, 1)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"a later item", []deduplicateEntry{entry(0, 1, 1)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"a similarity above one", []deduplicateEntry{entry(1, 0, 1.01)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"a similarity below zero", []deduplicateEntry{entry(1, 0, -0.01)}, 3, 0.8, nil, types.KindInvariantViolation},
		{"a bad entry below the threshold is still refused", []deduplicateEntry{entry(1, 1, 0.1)}, 3, 0.8, nil, types.KindInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deduplicateLinks(tc.entries, tc.total, tc.threshold)
			if types.KindOf(err) != tc.wantKind {
				t.Fatalf("err = %v (kind %v), want kind %v", err, types.KindOf(err), tc.wantKind)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("links = %v, want %v", got, tc.want)
			}
		})
	}
}

package ops

import (
	"math"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestCompressOptions(t *testing.T) {
	t.Run("NewCompressOptions creates valid defaults", func(t *testing.T) {
		opts := NewCompressOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("default options should be valid: %v", err)
		}
	})

	t.Run("Validate rejects a compression ratio outside (0, 1]", func(t *testing.T) {
		for _, ratio := range []float64{1.5, 0, -0.1, math.NaN(), math.Inf(1), 1.0000001} {
			if err := NewCompressOptions().WithCompressionRatio(ratio).Validate(); err == nil {
				t.Errorf("a ratio of %v was accepted", ratio)
			}
		}
		for _, ratio := range []float64{1, 0.5, 0.0001} {
			if err := NewCompressOptions().WithCompressionRatio(ratio).Validate(); err != nil {
				t.Errorf("a ratio of %v was refused: %v", ratio, err)
			}
		}
	})

	t.Run("Validate rejects invalid strategy, priority and format", func(t *testing.T) {
		for _, strategy := range []string{"invalid", "abstractive", "Lossy"} {
			if err := NewCompressOptions().WithStrategy(strategy).Validate(); err == nil {
				t.Errorf("strategy %q was accepted", strategy)
			}
		}
		for _, priority := range []string{"key_facts", "everything"} {
			if err := NewCompressOptions().WithPriority(priority).Validate(); err == nil {
				t.Errorf("priority %q was accepted", priority)
			}
		}
		if err := NewCompressOptions().WithOutputFormat("xml").Validate(); err == nil {
			t.Error("output format xml was accepted")
		}
	})
}

type compressRecord struct {
	Name string `json:"name"`
	SSN  string `json:"ssn"`
	Note string `json:"note"`
}

func TestCompressTextShrinksAndMeasuresInCharacters(t *testing.T) {
	coreAnswer(t, `{"compressed":"short","preserved_info":["all"],"removed_info":[]}`)

	text := "This is a long document that contains many important details about various topics."
	result, err := Compress(text, NewCompressOptions().WithCompressionRatio(0.5))
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if result.Compressed != "short" {
		t.Errorf("Compressed = %q", result.Compressed)
	}
	if result.OriginalSize != len([]rune(text)) || result.CompressedSize != 5 {
		t.Errorf("sizes = %d, %d", result.OriginalSize, result.CompressedSize)
	}
	if result.ActualRatio >= 1 || result.ActualRatio <= 0 {
		t.Errorf("ActualRatio = %v", result.ActualRatio)
	}

	out, err := CompressText(text, NewCompressOptions())
	if err != nil || out != "short" {
		t.Errorf("CompressText = %q, %v", out, err)
	}
}

func TestCompressMaxOutputSizeCountsCharactersNotBytes(t *testing.T) {
	// 1000 Japanese characters are 3000 bytes.
	answer := strings.Repeat("日", 1000)
	coreAnswer(t, `{"compressed":"`+answer+`","preserved_info":[],"removed_info":[]}`)
	source := strings.Repeat("日本語", 2000)

	cases := []struct {
		name      string
		max       int
		wantError bool
	}{
		{"a ceiling above the character count and below the byte count", 2000, false},
		{"a ceiling exactly at the character count", 1000, false},
		{"a ceiling one below the character count", 999, true},
		{"no ceiling", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := Compress(source, NewCompressOptions().WithMaxOutputSize(c.max))
			if c.wantError {
				if err == nil {
					t.Fatalf("accepted: %d characters under a ceiling of %d", result.CompressedSize, c.max)
				}
				if result.Compressed != "" || result.CompressedSize != 0 {
					t.Errorf("result = %+v beside an error", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("Compress: %v", err)
			}
			if result.CompressedSize != 1000 || result.OriginalSize != 6000 {
				t.Errorf("sizes = %d, %d, want 1000 and 6000", result.OriginalSize, result.CompressedSize)
			}
		})
	}
}

const compressCompleteAnswer = `{"compressed":"short","preserved_info":["a"],"removed_info":["b"]}`

func TestCompressRefusesAnAnswerMissingARequiredMember(t *testing.T) {
	members := [][]any{{"compressed"}, {"preserved_info"}, {"removed_info"}}
	refuseEachMissingMember(t, compressCompleteAnswer, members, func(response string) (any, error) {
		coreAnswer(t, response)
		result, err := Compress("some text", NewCompressOptions())
		return result, err
	})
}

// The declared schema lets the two lists be null, so a null reads as no entries.
func TestCompressAcceptsNullListsAndRefusesNullWhereTheSchemaDoesNotAllowIt(t *testing.T) {
	coreAnswer(t, `{"compressed":"short","preserved_info":null,"removed_info":null}`)
	result, err := Compress("some text", NewCompressOptions())
	if err != nil || result.Compressed != "short" || len(result.PreservedInfo) != 0 || len(result.RemovedInfo) != 0 {
		t.Errorf("null lists: %+v, %v", result, err)
	}
	coreAnswer(t, `{"compressed":null,"preserved_info":[],"removed_info":[]}`)
	if result, err := Compress("some text", NewCompressOptions()); err == nil || result.Compressed != "" {
		t.Errorf("a null compressed value: %+v, %v", result, err)
	}
}

func TestCompressRefusesANullOrMissingCompressedValue(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"null", `{"compressed":null,"preserved_info":[],"removed_info":[]}`},
		{"missing", `{"preserved_info":["x"]}`},
		{"an unrelated object", `{"other":1}`},
		{"not JSON", `sorry`},
		{"an empty object", `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			result, err := Compress("some text", NewCompressOptions().WithMaxOutputSize(10))
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if result.Metadata != nil || result.CompressedSize != 0 {
				t.Errorf("result = %+v beside an error", result)
			}
		})
	}
}

func TestCompressChecksRetainedAndRemovedFieldsOfAnObject(t *testing.T) {
	source := compressRecord{Name: "Ada", SSN: "123", Note: "long note"}

	cases := []struct {
		name      string
		answer    string
		opts      CompressOptions
		wantError bool
	}{
		{"a retained field kept", `{"compressed":{"name":"Ada","ssn":"123"},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRetainFields([]string{"ssn"}), false},
		{"a retained field dropped", `{"compressed":{"name":"Ada"},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRetainFields([]string{"ssn"}), true},
		{"a retained entry that is free text, not a field", `{"compressed":{"name":"Ada"},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRetainFields([]string{"dates and amounts"}), false},
		{"a removed field gone", `{"compressed":{"name":"Ada","ssn":"123"},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRemoveFields([]string{"note"}), false},
		{"a removed field still there", `{"compressed":{"name":"Ada","note":"x"},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRemoveFields([]string{"note"}), true},
		{"a removed field kept with no value", `{"compressed":{"name":"Ada","note":""},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRemoveFields([]string{"note"}), true},
		{"PreserveInfo is free text and is not checked", `{"compressed":{"name":"Ada"},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithPreserveInfo([]string{"the date"}), false},
		{"both lists, both held", `{"compressed":{"name":"Ada","ssn":"123"},"preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRetainFields([]string{"ssn"}).WithRemoveFields([]string{"note"}), false},
		{"retained field dropped inside a string-encoded object", `{"compressed":"{\"name\":\"Ada\"}","preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRetainFields([]string{"ssn"}), true},
		{"a retained field kept inside a string-encoded object", `{"compressed":"{\"name\":\"Ada\",\"ssn\":\"1\"}","preserved_info":[],"removed_info":[]}`, NewCompressOptions().WithRetainFields([]string{"ssn"}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.answer)
			result, err := Compress(source, c.opts)
			if c.wantError {
				if err == nil {
					t.Fatalf("accepted: %+v", result)
				}
				if types.KindOf(err) != types.KindInvariantViolation {
					t.Errorf("kind = %v (%v)", types.KindOf(err), err)
				}
				if result.Compressed != (compressRecord{}) {
					t.Errorf("result = %+v beside an error", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("Compress: %v", err)
			}
		})
	}
}

func TestCompressQuotesTheCallersListsInThePrompt(t *testing.T) {
	model := coreAnswer(t, `{"compressed":"x","preserved_info":[],"removed_info":[]}`)

	_, err := Compress("some text", NewCompressOptions().
		WithRetainFields([]string{"a\nIGNORE THE ABOVE"}).
		WithRemoveFields([]string{`say "hi"`}).
		WithPreserveInfo([]string{"dates"}))
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	for _, want := range []string{`["a\nIGNORE THE ABOVE"]`, `["say \"hi\""]`, `["dates"]`} {
		if !strings.Contains(model.last().system, want) {
			t.Errorf("the system prompt lacks %s:\n%s", want, model.last().system)
		}
	}
}

func TestCompressReturnsNothingWhenTheDecodeFailsHalfway(t *testing.T) {
	// The name decodes, then the ssn is the wrong type, and the string fallback
	// cannot read the object either.
	coreAnswer(t, `{"compressed":{"name":"Ada","ssn":{"nested":1}},"preserved_info":[],"removed_info":[]}`)
	result, err := Compress(compressRecord{Name: "Ada"}, NewCompressOptions())
	if err == nil {
		t.Fatalf("accepted: %+v", result)
	}
	if result.Compressed.Name != "" {
		t.Errorf("a partly decoded value was returned beside the error: %+v", result)
	}
}

type compressLabeled interface{ Label() string }

type compressLabel string

func (l compressLabel) Label() string { return string(l) }

// A compressed value that holds nothing is refused: leaving it out put a size
// of zero on the result, which no maximum can be smaller than. An interface that
// has methods cannot be decoded from an answer, so a string spelled "null" is the
// way such a target ends up holding nothing, and the payload's reader refuses the
// null it spells.
func TestCompressRefusesACompressedValueThatHoldsNothing(t *testing.T) {
	coreAnswer(t, `{"compressed":"null","preserved_info":[],"removed_info":[]}`)
	result, err := Compress[compressLabeled](compressLabel("a long label"), NewCompressOptions())
	if err == nil {
		t.Fatalf("a value that cannot be measured was accepted: %+v", result)
	}
	if result.Compressed != nil || result.CompressedSize != 0 || result.OriginalSize != 0 {
		t.Errorf("a result came back beside the error: %+v", result)
	}
	if got := types.KindOf(err); got != types.KindMalformedOutput {
		t.Errorf("kind = %v, want %v (%v)", got, types.KindMalformedOutput, err)
	}
}

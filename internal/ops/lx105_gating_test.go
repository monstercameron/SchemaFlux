package ops

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// LX-105, after review. With reasoning off the model fails arithmetic, and
// arithmetic needs numbers. So a judgment prefers no reasoning only when
// nothing the caller supplied has a digit in it: not the input, not the
// categories, descriptions, examples or aspects, not the steering.
func TestJudgmentEffort(t *testing.T) {
	cases := []struct {
		name  string
		texts []string
		want  types.Effort
	}{
		{"nothing at all", nil, types.EffortNone},
		{"plain words", []string{"My invoice is wrong", "billing", "technical"}, types.EffortNone},
		{"empty strings", []string{"", ""}, types.EffortNone},
		{"punctuation", []string{"refund? yes: now!"}, types.EffortNone},
		{"a digit in the input", []string{"3 units at a low price"}, types.EffortDefault},
		{"a digit in a later text", []string{"plain", "plain", "over $500"}, types.EffortDefault},
		{"a price", []string{"$129.99"}, types.EffortDefault},
		{"a date", []string{"due on 2 November"}, types.EffortDefault},
		{"a digit inside a word", []string{"error E404"}, types.EffortDefault},
		{"a fullwidth digit", []string{"番号 ３"}, types.EffortDefault},
		{"an arabic-indic digit", []string{"رقم ٣"}, types.EffortDefault},
		{"a number in JSON", []string{`{"qty":12,"unit":"dozen"}`}, types.EffortDefault},
		{"accented letters are not digits", []string{"café déjà vu"}, types.EffortNone},

		// Numerals that are not decimal digits. The first gate asked
		// unicode.IsDigit, which is one category of three.
		{"a vulgar fraction", []string{"½ off, then a further ¼"}, types.EffortDefault},
		{"a roman numeral sign", []string{"Ⅻ units"}, types.EffortDefault},
		{"a superscript", []string{"x² over the limit"}, types.EffortDefault},
		{"a circled number", []string{"step ③"}, types.EffortDefault},
		{"han numerals", []string{"三十個"}, types.EffortDefault},
		{"a han numeral for ten thousand", []string{"五万円を超える"}, types.EffortDefault},
		{"the han zero", []string{"二〇二六年"}, types.EffortDefault},
		// Accepted cost: the han numeral for one is also an everyday word.
		{"a han numeral used as a word", []string{"一般的な質問"}, types.EffortDefault},
		{"han text with no numeral", []string{"請求書が間違っています"}, types.EffortNone},

		// English quantity words, as whole words in any case.
		{"number words", []string{"twelve units, half off"}, types.EffortDefault},
		{"a small number word", []string{"the two of them"}, types.EffortDefault},
		{"a capitalised number word", []string{"Seven complaints"}, types.EffortDefault},
		{"an upper-case number word", []string{"TWICE THE PRICE"}, types.EffortDefault},
		{"a dozen", []string{"a dozen eggs"}, types.EffortDefault},
		{"a plural", []string{"thousands of orders"}, types.EffortDefault},
		{"a tens word", []string{"over forty items"}, types.EffortDefault},
		{"a hyphenated number", []string{"twenty-five units"}, types.EffortDefault},
		{"percent", []string{"ten percent off"}, types.EffortDefault},
		{"double", []string{"charged double"}, types.EffortDefault},
		{"a number word at the very end", []string{"they sent three"}, types.EffortDefault},
		{"a number word in a later text", []string{"plain", "a hundred refunds"}, types.EffortDefault},
		// A word that only contains one is not one.
		{"a word containing two", []string{"network outage"}, types.EffortNone},
		{"a word containing ten", []string{"it often fails"}, types.EffortNone},
		{"a word containing nine", []string{"canine unit"}, types.EffortNone},
		{"a word containing half", []string{"halfway there"}, types.EffortNone},
		{"a word joined by an apostrophe", []string{"the tenant's"}, types.EffortNone},
		// Stated limits. "One" is an article more often than a quantity, and a
		// number written as a word in another language is not seen.
		{"one is not counted", []string{"one more thing, no one answered"}, types.EffortNone},
		{"a number word in German", []string{"zwölf Einheiten"}, types.EffortNone},
		{"a number word in Spanish", []string{"doce unidades"}, types.EffortNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := judgmentEffort(tc.texts...); got != tc.want {
				t.Errorf("judgmentEffort(%q) = %q, want %q", tc.texts, got, tc.want)
			}
		})
	}

	// The gate reads every call's whole input, so it does not allocate.
	long := strings.Repeat("The customer wrote about an invoice that looked wrong to them. ", 200)
	if allocs := testing.AllocsPerRun(20, func() { _ = judgmentEffort(long, "billing", "technical") }); allocs != 0 {
		t.Errorf("judgmentEffort allocated %v times on a call with no number in it", allocs)
	}
}

// The three operations that prefer no reasoning drop the preference as soon as
// a number is in play. Each case is one a reviewer gave as a wrong answer
// waiting to happen, and its plain twin.
func TestThePreferenceIsDroppedWhenANumberIsInPlay(t *testing.T) {
	type call func(ctx context.Context) error
	classify := func(input string, set func(*ClassifyOptions)) call {
		return func(ctx context.Context) error {
			opts := NewClassifyOptions().WithCategories([]string{"large", "small"})
			opts.OpOptions.Context = ctx
			set(&opts)
			_, err := Classify[string, string](input, opts)
			return err
		}
	}
	similar := func(a, b any, set func(*SimilarOptions)) call {
		return func(ctx context.Context) error {
			opts := NewSimilarOptions()
			opts.OpOptions.Context = ctx
			set(&opts)
			_, err := Similar(a, b, opts)
			return err
		}
	}
	deduplicate := func(items []any, steering string) call {
		return func(ctx context.Context) error {
			_, err := Deduplicate(items, 0.8, types.OpOptions{Context: ctx, Steering: steering})
			return err
		}
	}
	const (
		classified = `{"category":"large","confidence":0.9,"reasoning":"r"}`
		scored     = `{"score":0.9,"explanation":"x"}`
		aspects    = `{"score":0.9,"aspects":{"a1":{"score":0.9}},"explanation":"x"}`
		noDups     = `{"duplicates":[]}`
	)

	cases := []struct {
		name  string
		reply string
		run   call
		want  string
	}{
		{"Classify: words only", classified, classify("a big order of widgets", func(*ClassifyOptions) {}), "none"},
		{"Classify: a number in the input", classified, classify("3 x $129.99 and 2 x $55.02", func(*ClassifyOptions) {}), ""},
		{"Classify: a number in a description", classified, classify("a big order", func(o *ClassifyOptions) {
			o.CategoryDescriptions = map[string]string{"large": "line items total more than $500"}
		}), ""},
		{"Classify: a description in words", classified, classify("a big order", func(o *ClassifyOptions) {
			o.CategoryDescriptions = map[string]string{"large": "bulk orders from business customers"}
		}), "none"},
		{"Classify: a number in an example", classified, classify("a big order", func(o *ClassifyOptions) {
			o.CategoryExamples = map[string][]string{"large": {"an order of 40 pallets"}}
		}), ""},
		{"Classify: a number in the steering", classified, classify("a big order", func(o *ClassifyOptions) {
			o.OpOptions.Steering = "Large means over 500 dollars"
		}), ""},
		{"Classify: steering in words", classified, classify("a big order", func(o *ClassifyOptions) {
			o.OpOptions.Steering = "Judge by the customer's tone"
		}), "none"},

		{"Similar: words only", scored, similar("The flight was cancelled.", "Fog grounded the flight.", func(*SimilarOptions) {}), "none"},
		{"Similar: numbers in the items", scored, similar(map[string]any{"qty": 12, "unit_price": 18.5}, map[string]any{"total": 222}, func(*SimilarOptions) {}), ""},
		{"Similar: an aspect, words only", aspects, similar("a", "b", func(o *SimilarOptions) { o.Aspects = []string{"tone"} }), "none"},
		{"Similar: a number in an aspect", aspects, similar("a", "b", func(o *SimilarOptions) { o.Aspects = []string{"price within 5%"} }), ""},
		{"Similar: a number in the steering", scored, similar("a", "b", func(o *SimilarOptions) { o.OpOptions.Steering = "Ignore differences under 10 units" }), ""},

		{"Deduplicate: words only", noDups, deduplicate([]any{"refund request", "login failure"}, ""), "none"},
		{"Deduplicate: numbers in the items", noDups, deduplicate([]any{map[string]any{"qty": 12, "unit": "dozen"}, map[string]any{"qty": 144, "unit": "each"}}, ""), ""},
		{"Deduplicate: a number in the steering", noDups, deduplicate([]any{"a", "b"}, "Prices within 2 dollars are the same"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")
			provider := &captureProvider{resp: llm.CompletionResponse{Content: tc.reply}}
			if err := tc.run(WithProvider(context.Background(), provider)); err != nil {
				t.Fatalf("the operation failed: %v", err)
			}
			if provider.req.ReasoningEffort != tc.want {
				t.Errorf("the request carried effort %q, want %q", provider.req.ReasoningEffort, tc.want)
			}
			if tc.want != "" && !provider.req.ReasoningEffortPreferred {
				t.Error("the effort is not marked as a preference")
			}
		})
	}
}

// The ten operations whose options spell their fields out, with no embedded
// half, had a model pin and no effort. Every request these build names both or
// neither, and the option merges carry both.
func TestFlatOptionsCarryTheEffortWhereverTheyCarryTheModel(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}
	literal := regexp.MustCompile(`(?s)types\.OpOptions\{[^}]*\}`)
	merge := regexp.MustCompile(`if user\.Model != "" \{\n\t\tdefaults\.Model = user\.Model\n\t\}\n`)

	literals, merges := 0, 0
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("reading %s: %v", source, err)
		}
		text := string(raw)
		for _, found := range literal.FindAllString(text, -1) {
			if !strings.Contains(found, "Model:") {
				continue
			}
			literals++
			if !strings.Contains(found, "Effort:") {
				t.Errorf("%s builds a request with the model pin and no effort:\n%s", source, found)
			}
		}
		for _, at := range merge.FindAllStringIndex(text, -1) {
			merges++
			if !strings.HasPrefix(text[at[1]:], "\tif user.Effort != types.EffortDefault {\n\t\tdefaults.Effort = user.Effort\n\t}\n") {
				t.Errorf("%s merges the model pin and not the effort", source)
			}
		}
	}
	if literals < 11 || merges < 10 {
		t.Errorf("the scan found %d request literals and %d merges; it should find at least 11 and 10", literals, merges)
	}
}

// One of the ten, end to end: the effort set on flat options reaches the
// request.
func TestFlatOptionsEffortReachesTheRequest(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")
	for _, effort := range []types.Effort{types.EffortDefault, types.EffortLow, types.EffortHigh} {
		provider := &captureProvider{resp: llm.CompletionResponse{Content: `{}`}}
		// The answer is not the point and is refused; the request is.
		_, _ = Settle[salaryTerms](map[string]any{"max_budget": "low"}, NegotiateOptions{
			Context: WithProvider(context.Background(), provider), Effort: effort,
		})
		if provider.attempts == 0 {
			t.Fatal("Settle made no provider call")
		}
		if provider.req.ReasoningEffort != string(effort) || provider.req.ReasoningEffortPreferred {
			t.Errorf("effort %q: the request carried %q (preferred %v)", effort, provider.req.ReasoningEffort, provider.req.ReasoningEffortPreferred)
		}
	}
}

// ConvertOpOptions built a CommonOptions from six fields of the options it was
// given, and left out the model pin and the effort.
func TestConvertOpOptionsCarriesTheModelAndTheEffort(t *testing.T) {
	for _, operation := range []string{"extract", "transform", "generate", "summarize", "an operation it does not know"} {
		converted := ConvertOpOptions(types.OpOptions{
			Steering: "s", Model: "a-pinned-model", Effort: types.EffortLow, CorrelationID: "c",
		}, operation)
		carrier, ok := converted.(interface{ toOpOptions() types.OpOptions })
		if !ok {
			t.Fatalf("%s: %T has no conversion", operation, converted)
		}
		got := carrier.toOpOptions()
		if got.Model != "a-pinned-model" || got.Effort != types.EffortLow || got.Steering != "s" {
			t.Errorf("%s: Model = %q, Effort = %q, Steering = %q", operation, got.Model, got.Effort, got.Steering)
		}
	}
}

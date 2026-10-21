package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Every place a caller can put text is read by the number gate, one place at a
// time. The test before this one put a number in several of them, and left out
// a category's own name and the steering set on CommonOptions, so either could
// be dropped from what the gate reads with every test still passing.
func TestTheNumberGateReadsEveryTextACallerSupplies(t *testing.T) {
	type call func(ctx context.Context) error
	classify := func(input string, categories []string, set func(*ClassifyOptions)) call {
		return func(ctx context.Context) error {
			opts := NewClassifyOptions().WithCategories(categories)
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
	deduplicate := func(items []string, steering string) call {
		return func(ctx context.Context) error {
			_, err := Deduplicate(items, 0.8, types.OpOptions{Context: ctx, Steering: steering})
			return err
		}
	}
	plain := []string{"large", "small"}
	nothing := func(*ClassifyOptions) {}
	const (
		large   = `{"category":"large","confidence":0.9,"reasoning":"r"}`
		tiered  = `{"category":"tier 2","confidence":0.9,"reasoning":"r"}`
		worded  = `{"category":"two or more","confidence":0.9,"reasoning":"r"}`
		scored  = `{"score":0.9,"explanation":"x"}`
		aspects = `{"score":0.9,"aspects":{"a1":{"score":0.9}},"explanation":"x"}`
		noDups  = `{"duplicates":[]}`
	)

	cases := []struct {
		name  string
		reply string
		run   call
		off   bool // whether the call runs with reasoning off
	}{
		{"Classify: nothing but words", large, classify("a big order", plain, nothing), true},
		{"Classify: a digit in the input", large, classify("order 7", plain, nothing), false},
		{"Classify: a number word in the input", large, classify("twelve pallets", plain, nothing), false},
		{"Classify: a digit in a category's name", tiered, classify("a big order", []string{"tier 2", "tier 3"}, nothing), false},
		{"Classify: a number word in a category's name", worded, classify("a big order", []string{"two or more", "fewer"}, nothing), false},
		{"Classify: a digit in a description", large, classify("a big order", plain, func(o *ClassifyOptions) {
			o.CategoryDescriptions = map[string]string{"small": "under 5 items"}
		}), false},
		{"Classify: a number word in a description", large, classify("a big order", plain, func(o *ClassifyOptions) {
			o.CategoryDescriptions = map[string]string{"small": "fewer than a dozen items"}
		}), false},
		{"Classify: a digit in an example", large, classify("a big order", plain, func(o *ClassifyOptions) {
			o.CategoryExamples = map[string][]string{"small": {"plain", "3 boxes"}}
		}), false},
		{"Classify: a digit in the steering on CommonOptions", large, classify("a big order", plain, func(o *ClassifyOptions) {
			o.CommonOptions.Steering = "large means over 500"
		}), false},
		{"Classify: a digit in the steering on the embedded half", large, classify("a big order", plain, func(o *ClassifyOptions) {
			o.OpOptions.Steering = "large means over 500"
		}), false},
		{"Classify: steering in words on CommonOptions", large, classify("a big order", plain, func(o *ClassifyOptions) {
			o.CommonOptions.Steering = "judge by the tone"
		}), true},

		{"Similar: nothing but words", scored, similar("the flight was cancelled", "fog grounded it", func(*SimilarOptions) {}), true},
		{"Similar: a digit in the first item", scored, similar("flight 204 was cancelled", "fog grounded it", func(*SimilarOptions) {}), false},
		{"Similar: a digit in the second item", scored, similar("the flight was cancelled", "fog at 6am", func(*SimilarOptions) {}), false},
		{"Similar: a number word in the second item", scored, similar("the flight was cancelled", "half the flights left", func(*SimilarOptions) {}), false},
		{"Similar: a digit in an aspect", aspects, similar("a", "b", func(o *SimilarOptions) { o.Aspects = []string{"within 5 percent"} }), false},
		{"Similar: a digit in the steering", scored, similar("a", "b", func(o *SimilarOptions) {
			o.OpOptions.Steering = "ignore differences under 10"
		}), false},
		{"Similar: a number word in the steering", scored, similar("a", "b", func(o *SimilarOptions) {
			o.OpOptions.Steering = "ignore the first two lines"
		}), false},
		{"Similar: steering in words", scored, similar("a", "b", func(o *SimilarOptions) {
			o.OpOptions.Steering = "ignore the greeting"
		}), true},

		{"Deduplicate: nothing but words", noDups, deduplicate([]string{"refund request", "login failure"}, ""), true},
		{"Deduplicate: a digit in the first item", noDups, deduplicate([]string{"refund of 40", "login failure"}, ""), false},
		{"Deduplicate: a digit in the last item", noDups, deduplicate([]string{"refund request", "login failure", "error 502"}, ""), false},
		{"Deduplicate: a number word in an item", noDups, deduplicate([]string{"charged twice", "login failure"}, ""), false},
		{"Deduplicate: a digit in the steering", noDups, deduplicate([]string{"refund request", "login failure"}, "amounts within 2 are the same"), false},
		{"Deduplicate: steering in words", noDups, deduplicate([]string{"refund request", "login failure"}, "ignore the greeting"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")
			provider := &captureProvider{resp: llm.CompletionResponse{Content: tc.reply}}
			if err := tc.run(WithProvider(context.Background(), provider)); err != nil {
				t.Fatalf("the operation failed: %v", err)
			}
			if provider.attempts == 0 {
				t.Fatal("no request was made")
			}
			off := provider.req.ReasoningEffort == "none" && provider.req.ReasoningEffortPreferred
			if off != tc.off {
				t.Errorf("reasoning off = %v (effort %q, preferred %v), want %v",
					off, provider.req.ReasoningEffort, provider.req.ReasoningEffortPreferred, tc.off)
			}
		})
	}
}

package tests

import (
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
	"github.com/monstercameron/schemaflux/internal/types"
)

// An operation that turns a call away before it asks a model says why with a
// kind, at the public boundary: a consumer branches on errors.Is against the
// exported sentinel and never reads the message, and the message is what the
// library has always written. The provider is asked nothing in any case.
func TestOperationsRefuseABadCallWithAKindAndAskNothing(t *testing.T) {
	type thing struct {
		Name string `json:"name"`
	}
	things := []thing{{Name: "a"}, {Name: "b"}, {Name: "c"}}

	cases := []struct {
		name string
		call func() error
		text string
	}{
		{"Classify with no categories", func() error {
			_, err := schemaflux.Classify[string, string]("x", schemaflux.NewClassifyOptions())
			return err
		}, "invalid options: at least one category is required"},
		{"Cluster with a naming strategy nobody knows", func() error {
			options := schemaflux.NewClusterOptions()
			options.NamingStrategy = "poetic"
			_, err := schemaflux.Cluster(things, options)
			return err
		}, "invalid options: invalid naming strategy: poetic"},
		{"Choose with a strategy nobody knows", func() error {
			options := schemaflux.NewChooseOptions()
			options.Strategy = "lottery"
			_, err := schemaflux.Choose(things, options)
			return err
		}, "invalid options: invalid strategy: lottery"},
		{"Filter with no criteria", func() error {
			_, err := schemaflux.Filter(things, schemaflux.NewFilterOptions())
			return err
		}, "invalid options: filter criteria is required"},
		{"Rank with no query", func() error {
			options := schemaflux.NewRankOptions()
			options.Query = ""
			_, err := schemaflux.Rank(things, options)
			return err
		}, "invalid options: query is required for ranking"},
		{"SemanticMatch with a negative match limit", func() error {
			options := schemaflux.NewMatchOptions()
			options.MaxMatches = -1
			_, err := schemaflux.SemanticMatch(things, things, options)
			return err
		}, "invalid options: max matches cannot be negative, got -1"},
		{"Critique with no criteria", func() error {
			_, err := schemaflux.Critique("an essay", schemaflux.NewCritiqueOptions())
			return err
		}, "invalid options: at least one criterion or rubric entry is required"},
		{"Verify with every check off", func() error {
			options := schemaflux.NewVerifyOptions()
			options.CheckFacts, options.CheckLogic, options.CheckConsistency = false, false, false
			_, err := schemaflux.Verify("a statement", options)
			return err
		}, "verify: configuration: invalid options: CheckFacts, CheckLogic and CheckConsistency are all off, so there is nothing to verify"},
		{"Diff of two numbers", func() error {
			_, err := schemaflux.Diff(1, 2, schemaflux.NewDiffOptions())
			return err
		}, "comparison failed: diff operation requires struct types"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := testfixtures.WithScriptedProvider(t, `{}`, nil)
			err := tc.call()
			if err == nil {
				t.Fatal("the call was accepted")
			}
			if err.Error() != tc.text {
				t.Errorf("the message changed:\n got %q\nwant %q", err.Error(), tc.text)
			}
			if got := schemaflux.KindOf(err); got != types.KindConfiguration {
				t.Errorf("the error has kind %v, want configuration", got)
			}
			if !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Error("errors.Is does not reach schemaflux.ErrConfiguration")
			}
			if asked := len(provider.Requests()); asked != 0 {
				t.Errorf("the provider was asked %d times", asked)
			}
		})
	}
}

// A strict extraction whose answer leaves a required field empty is refused by
// the completeness check, and the refusal is classified as the model's answer
// being unusable. Classifying it must not change what the stack does with it:
// the answer is still sent back for repair, and the call still ends as repair
// exhausted with the field named in the message.
func TestStrictExtractionStillRepairsAnIncompleteAnswer(t *testing.T) {
	type record struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	provider := testfixtures.WithScriptedProvider(t, `{"name":"Ada","email":""}`, nil)
	options := schemaflux.NewExtractOptions()
	options.CommonOptions.Mode = types.Strict
	_, err := schemaflux.Extract[record]("Ada", options)
	if err == nil {
		t.Fatal("an answer with a required field empty was accepted")
	}
	if got := schemaflux.KindOf(err); got != types.KindRepairExhausted {
		t.Errorf("the call ended with kind %v, want repair exhausted", got)
	}
	if !strings.Contains(err.Error(), "required field email is empty") {
		t.Errorf("the message no longer names the field: %v", err)
	}
	if asked := len(provider.Requests()); asked != 2 {
		t.Errorf("the provider was asked %d times, want 2: the answer was not sent back for repair", asked)
	}
}

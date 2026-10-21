package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LX-022. Similar had two ways of reporting a similarity nobody measured.
//
// A bare `true` or `false` body was accepted and given a score of the
// threshold plus or minus 0.1: a number the model never produced, which could
// be above 1.0 or below 0. And on the ordinary path IsSimilar was copied from
// the model and never compared with the score, so `{"is_similar": true,
// "score": 0.2}` at a threshold of 0.7 came back as similar.
//
// IsSimilar is now score >= threshold, computed in Go, and the model is not
// asked for a verdict at all. A response that still carries one is not
// consulted.
func TestSimilarIsDecidedFromTheScore(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		threshold float64
		wantErr   bool
		similar   bool
		score     float64
	}{
		{"above the threshold", `{"score":0.92,"explanation":"same idea"}`, 0.7, false, true, 0.92},
		{"below the threshold", `{"score":0.3,"explanation":"unrelated"}`, 0.7, false, false, 0.3},
		{"exactly at the threshold", `{"score":0.7,"explanation":"x"}`, 0.7, false, true, 0.7},
		{"a threshold of zero makes everything similar", `{"score":0,"explanation":"x"}`, 0, false, true, 0},
		{"a threshold of one needs a score of one", `{"score":0.99,"explanation":"x"}`, 1, false, false, 0.99},
		{"optional parts may be null", `{"score":0.9,"matched_aspects":null,"differing_aspects":null,"explanation":"x"}`, 0.7, false, true, 0.9},
		// A three-decimal threshold used to be shown to the model as "0.72"
		// and compared as 0.725.
		{"a three-decimal threshold, score just below", `{"score":0.72,"explanation":"x"}`, 0.725, false, false, 0.72},
		{"a three-decimal threshold, score at it", `{"score":0.725,"explanation":"x"}`, 0.725, false, true, 0.725},

		// A verdict the model volunteers is ignored: the score decides.
		{"claims similar with a low score", `{"is_similar":true,"score":0.2,"explanation":"x"}`, 0.7, false, false, 0.2},
		{"claims different with a high score", `{"is_similar":false,"score":0.95,"explanation":"x"}`, 0.7, false, true, 0.95},

		{"a bare true is not an answer", `true`, 0.5, true, false, 0},
		{"a bare false is not an answer", `false`, 0.5, true, false, 0},
		{"no score", `{"is_similar":true,"explanation":"x"}`, 0.7, true, false, 0},
		{"a null score", `{"score":null,"explanation":"x"}`, 0.7, true, false, 0},
		{"score above one", `{"score":1.5,"explanation":"x"}`, 0.7, true, false, 0},
		{"score below zero", `{"score":-0.2,"explanation":"x"}`, 0.7, true, false, 0},
		{"an aspect score off the scale", `{"score":0.9,"matched_aspects":[{"aspect":"tone","score":7}],"explanation":"x"}`, 0.7, true, false, 0},
		{"an aspect with no score", `{"score":0.9,"matched_aspects":[{"aspect":"tone"}],"explanation":"x"}`, 0.7, true, false, 0},
		{"an aspect with no name", `{"score":0.9,"differing_aspects":[{"aspect":" ","score":0.2}],"explanation":"x"}`, 0.7, true, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installAnalysisResponse(t, tc.body)

			result, err := Similar[string]("a", "b", NewSimilarOptions().WithSimilarityThreshold(tc.threshold))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted the response; IsSimilar=%v Score=%v", result.IsSimilar, result.Score)
				}
				// The result of a failed call must not carry a verdict or a
				// number a caller who ignores the error could act on.
				if result.IsSimilar || result.Score != 0 {
					t.Fatalf("a failed call returned IsSimilar=%v Score=%v", result.IsSimilar, result.Score)
				}
				return
			}
			if err != nil {
				t.Fatalf("Similar: %v", err)
			}
			if result.IsSimilar != tc.similar || result.Score != tc.score {
				t.Fatalf("IsSimilar=%v Score=%v, want %v and %v", result.IsSimilar, result.Score, tc.similar, tc.score)
			}
		})
	}
}

// The failures are classified, so a caller can tell a malformed answer from a
// provider outage without reading the message.
func TestSimilarFailuresAreClassified(t *testing.T) {
	cases := []struct {
		name, body string
		want       types.ErrorKind
	}{
		{"no score", `{"explanation":"x"}`, types.KindSchemaViolation},
		{"score off the scale", `{"score":1.5,"explanation":"x"}`, types.KindInvariantViolation},
		{"aspect score off the scale", `{"score":0.5,"matched_aspects":[{"aspect":"a","score":2}],"explanation":"x"}`, types.KindInvariantViolation},
		{"aspect with no score", `{"score":0.5,"matched_aspects":[{"aspect":"a"}],"explanation":"x"}`, types.KindSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installAnalysisResponse(t, tc.body)
			_, err := Similar[string]("a", "b", NewSimilarOptions())
			if got := types.KindOf(err); got != tc.want {
				t.Fatalf("error kind = %v, want %v (err: %v)", got, tc.want, err)
			}
		})
	}
}

// Aspects were rendered into the prompt and checked nowhere. A caller who names
// aspects gets each of them assessed, or an error.
func TestSimilarAssessesEveryRequestedAspect(t *testing.T) {
	cases := []struct {
		name    string
		aspects []string
		body    string
		wantErr bool
	}{
		{"none requested, none returned", nil, `{"score":0.9,"explanation":"x"}`, false},
		{"both requested, one in each list", []string{"meaning", "tone"},
			`{"score":0.9,"matched_aspects":[{"aspect":"meaning","score":0.95}],"differing_aspects":[{"aspect":"tone","score":0.4}],"explanation":"x"}`, false},
		{"both in matched", []string{"meaning", "tone"},
			`{"score":0.9,"matched_aspects":[{"aspect":"meaning","score":0.95},{"aspect":"tone","score":0.9}],"differing_aspects":[],"explanation":"x"}`, false},
		{"case and spacing differ", []string{"Tone"},
			`{"score":0.9,"matched_aspects":[{"aspect":" tone ","score":0.9}],"differing_aspects":[],"explanation":"x"}`, false},
		{"an extra aspect is allowed", []string{"tone"},
			`{"score":0.9,"matched_aspects":[{"aspect":"tone","score":0.9},{"aspect":"length","score":0.8}],"differing_aspects":[],"explanation":"x"}`, false},
		{"one requested aspect is missing", []string{"meaning", "tone"},
			`{"score":0.9,"matched_aspects":[{"aspect":"meaning","score":0.95}],"differing_aspects":[],"explanation":"x"}`, true},
		{"requested, and both lists are null", []string{"tone"}, `{"score":0.9,"matched_aspects":null,"differing_aspects":null,"explanation":"x"}`, true},
		{"requested, and both lists are empty", []string{"tone"}, `{"score":0.9,"matched_aspects":[],"differing_aspects":[],"explanation":"x"}`, true},
		{"requested, and the lists are absent", []string{"tone"}, `{"score":0.9,"explanation":"x"}`, true},
		{"a different aspect was assessed instead", []string{"tone"},
			`{"score":0.9,"matched_aspects":[{"aspect":"length","score":0.9}],"differing_aspects":[],"explanation":"x"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installAnalysisResponse(t, tc.body)
			opts := NewSimilarOptions()
			if tc.aspects != nil {
				opts = opts.WithAspects(tc.aspects)
			}
			_, err := Similar[string]("a", "b", opts)
			if tc.wantErr && err == nil {
				t.Fatal("a response that skipped a requested aspect was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Similar: %v", err)
			}
		})
	}
}

// The shape is declared to the provider, not only described in the prompt, so
// a provider with structured output cannot return the bare boolean at all.
func TestSimilarDeclaresAStrictSchema(t *testing.T) {
	var seen types.OpOptions
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, _ string, opt types.OpOptions) (string, error) {
		seen = opt
		return `{"score":0.9,"matched_aspects":[{"aspect":"tone","score":0.9}],"differing_aspects":[],"explanation":"x"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	for _, aspects := range [][]string{nil, {"tone"}} {
		opts := NewSimilarOptions()
		if aspects != nil {
			opts = opts.WithAspects(aspects)
		}
		if _, err := Similar[string]("a", "b", opts); err != nil {
			t.Fatalf("Similar: %v", err)
		}
		if problem := llm.StrictSchemaProblem(seen.JSONSchema); problem != "" {
			t.Fatalf("the schema breaks a strict-mode rule, or is missing: %s", problem)
		}
		properties := schemaProperties(t, seen.JSONSchema)
		if _, asked := properties["is_similar"]; asked {
			t.Fatal("the model is still asked for a verdict Go computes")
		}
		// Aspects are asked for exactly when they were named, and then they
		// must be a real value. The two lists the model used to fill in are
		// gone: Go sorts the aspects by score.
		if _, asked := properties["aspects"]; asked != (aspects != nil) {
			t.Fatalf("aspects=%v: aspects asked for = %v", aspects, asked)
		}
		if aspects != nil && !isRequiredNonNull(seen.JSONSchema, "aspects") {
			t.Fatalf("aspects=%v: aspects may be null", aspects)
		}
		for _, list := range []string{"matched_aspects", "differing_aspects"} {
			if _, asked := properties[list]; asked {
				t.Fatalf("the model is still asked to sort aspects into %s", list)
			}
		}
		for _, always := range []string{"score", "explanation"} {
			if !isRequiredNonNull(seen.JSONSchema, always) {
				t.Fatalf("%s may be null", always)
			}
		}
	}
}

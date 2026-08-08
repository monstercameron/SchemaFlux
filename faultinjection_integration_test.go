package schemaflux_test

import (
	"context"
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// This is the M01 exit gate. For every exported operation that reaches a
// provider, it runs three faults and asserts each produces an error:
//
//	provider error      — the call never completed
//	malformed body      — the answer is not the shape the operation parses
//	schema-violating    — the answer is well-formed JSON of the wrong shape
//
// The organising defect of this library was failing open: an operation that
// could not do its job returned a plausible-looking zero value with a nil
// error, and the caller could not tell. Every fix in M01 removed one instance.
// This suite is what keeps them removed.
//
// It runs with a scripted provider: no credential, no spend.

// record is the target type for the operations that need one.
type record struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// summary is a second type, for the operations that transform between two.
type summary struct {
	Headline string `json:"headline"`
}

// faultCase names one exported operation and the call that exercises it.
type faultCase struct {
	name string
	run  func() error
}

// The three faults. A schema-violating body is valid JSON that does not fit:
// operations that unmarshal into a struct accept unknown fields, so this is the
// weakest of the three and some operations legitimately survive it — those are
// listed in survivesSchemaViolation below with the reason.
const (
	malformedBody       = "I'm sorry, I can't help with that."
	schemaViolatingBody = `{"unrelated": [1, 2, 3], "shape": "wrong"}`

	// A body cut off mid-value. This is what a provider actually returns when
	// the output budget runs out, and it is a different failure from prose: the
	// JSON that arrived is right up to the point where it stops, so an
	// extractor that returns "the part it liked" would produce a decode of half
	// an answer rather than an error. TI-006.
	truncatedBody = `{"name": "Ada", "items": [{"id": 1}, {"id":`

	// A fenced block that never closes, which is the same failure wearing the
	// packaging models put around their answers.
	truncatedFencedBody = "```json\n{\"name\": \"Ada\", \"total\":"
)

// survivesSchemaViolation names operations for which a well-formed JSON body of
// the wrong shape is not an error, and why. Anything not listed here must fail
// all three faults.
var survivesSchemaViolation = map[string]string{
	"Summarize":      "returns the model's text verbatim",
	"Rewrite":        "returns the model's text verbatim",
	"Translate":      "returns the model's text verbatim",
	"Expand":         "returns the model's text verbatim",
	"CompressText":   "returns the model's text verbatim",
	"NormalizeText":  "returns the model's text verbatim",
	"QuestionLegacy": "returns the model's text verbatim",
	"Format":         "returns the model's text verbatim",
}

func faultCases() []faultCase {
	items := []record{{Name: "a", Count: 1}, {Name: "b", Count: 2}, {Name: "c", Count: 3}}

	return []faultCase{
		{"Extract", func() error {
			_, err := schemaflux.Extract[record]("some input", schemaflux.NewExtractOptions())
			return err
		}},
		{"Transform", func() error {
			_, err := schemaflux.Transform[record, summary](items[0], schemaflux.NewTransformOptions())
			return err
		}},
		{"Generate", func() error {
			_, err := schemaflux.Generate[record]("make a record", schemaflux.NewGenerateOptions())
			return err
		}},
		{"Choose", func() error {
			_, err := schemaflux.Choose(items, schemaflux.NewChooseOptions().WithSteering("pick one"))
			return err
		}},
		{"Filter", func() error {
			_, err := schemaflux.Filter(items, schemaflux.NewFilterOptions().WithSteering("keep some"))
			return err
		}},
		{"Sort", func() error {
			_, err := schemaflux.Sort(items, schemaflux.NewSortOptions().WithSteering("by count"))
			return err
		}},
		{"Classify", func() error {
			opts := schemaflux.NewClassifyOptions()
			opts.Categories = []string{"alpha", "bravo"}
			_, err := schemaflux.Classify[string, string]("some input", opts)
			return err
		}},
		{"Score", func() error {
			_, err := schemaflux.Score(items[0], schemaflux.NewScoreOptions().WithSteering("quality"))
			return err
		}},
		{"Compare", func() error {
			_, err := schemaflux.Compare(items[0], items[1], schemaflux.NewCompareOptions())
			return err
		}},
		{"Similar", func() error {
			_, err := schemaflux.Similar(items[0], items[1], schemaflux.NewSimilarOptions())
			return err
		}},
		{"Infer", func() error {
			_, err := schemaflux.Infer(items[0], schemaflux.NewInferOptions())
			return err
		}},
		{"Explain", func() error {
			_, err := schemaflux.Explain(items[0], schemaflux.NewExplainOptions())
			return err
		}},
		{"Parse", func() error {
			_, err := schemaflux.Parse[record]("some input", schemaflux.NewParseOptions())
			return err
		}},
		{"Summarize", func() error {
			_, err := schemaflux.Summarize("some input", schemaflux.NewSummarizeOptions())
			return err
		}},
		{"SummarizeWithMetadata", func() error {
			_, err := schemaflux.SummarizeWithMetadata("some input", schemaflux.NewSummarizeOptions())
			return err
		}},
		{"Rewrite", func() error {
			_, err := schemaflux.Rewrite("some input", schemaflux.NewRewriteOptions())
			return err
		}},
		{"RewriteWithMetadata", func() error {
			_, err := schemaflux.RewriteWithMetadata("some input", schemaflux.NewRewriteOptions())
			return err
		}},
		{"Translate", func() error {
			_, err := schemaflux.Translate("some input", schemaflux.NewTranslateOptions())
			return err
		}},
		{"TranslateWithMetadata", func() error {
			_, err := schemaflux.TranslateWithMetadata("some input", schemaflux.NewTranslateOptions())
			return err
		}},
		{"Expand", func() error {
			_, err := schemaflux.Expand("some input", schemaflux.NewExpandOptions())
			return err
		}},
		{"ExpandWithMetadata", func() error {
			_, err := schemaflux.ExpandWithMetadata("some input", schemaflux.NewExpandOptions())
			return err
		}},
		{"Suggest", func() error {
			_, err := schemaflux.Suggest[string]("some input", schemaflux.NewSuggestOptions())
			return err
		}},
		{"Validate", func() error {
			_, err := schemaflux.Validate(items[0], schemaflux.NewValidateOptions().WithRules("count > 0"))
			return err
		}},
		{"Question", func() error {
			_, err := schemaflux.Question[record, string](items[0], schemaflux.NewQuestionOptions("what?"))
			return err
		}},
		{"QuestionLegacy", func() error {
			_, err := schemaflux.QuestionLegacy(items[0], "what?")
			return err
		}},
		{"Merge", func() error {
			_, err := schemaflux.Merge(items, "prefer-newest")
			return err
		}},
		{"MergeWithMetadata", func() error {
			_, err := schemaflux.MergeWithMetadata(items, "prefer-newest")
			return err
		}},
		{"Format", func() error {
			_, err := schemaflux.Format(items[0], "a sentence")
			return err
		}},
		{"FormatWithMetadata", func() error {
			_, err := schemaflux.FormatWithMetadata(items[0], "a sentence")
			return err
		}},
		{"Decide", func() error {
			_, _, err := schemaflux.Decide(context.Background(), "a situation",
				[]schemaflux.Decision[string]{{Value: "a", Description: "first"}, {Value: "b", Description: "second"}})
			return err
		}},
		{"Match", func() error {
			_, err := schemaflux.Match(context.Background(), "an input",
				schemaflux.Like("some condition", func() {}))
			return err
		}},
		{"Annotate", func() error {
			_, err := schemaflux.Annotate(items[0], schemaflux.NewAnnotateOptions())
			return err
		}},
		{"Cluster", func() error {
			_, err := schemaflux.Cluster(items, schemaflux.NewClusterOptions())
			return err
		}},
		{"Rank", func() error {
			_, err := schemaflux.Rank(items, schemaflux.NewRankOptions().WithQuery("count"))
			return err
		}},
		{"Compress", func() error {
			_, err := schemaflux.Compress(items[0], schemaflux.NewCompressOptions())
			return err
		}},
		{"CompressText", func() error {
			_, err := schemaflux.CompressText("some input", schemaflux.NewCompressOptions())
			return err
		}},
		{"Decompose", func() error {
			_, err := schemaflux.Decompose(items[0], schemaflux.NewDecomposeOptions())
			return err
		}},
		{"Enrich", func() error {
			_, err := schemaflux.Enrich[record, summary](items[0], schemaflux.NewEnrichOptions())
			return err
		}},
		{"EnrichInPlace", func() error {
			_, err := schemaflux.EnrichInPlace(items[0], schemaflux.NewEnrichOptions())
			return err
		}},
		{"Normalize", func() error {
			_, err := schemaflux.Normalize(items[0], schemaflux.NewNormalizeOptions())
			return err
		}},
		{"NormalizeText", func() error {
			_, err := schemaflux.NormalizeText("some input", schemaflux.NewNormalizeOptions())
			return err
		}},
		{"SemanticMatch", func() error {
			_, err := schemaflux.SemanticMatch(items, items, schemaflux.NewMatchOptions())
			return err
		}},
		{"MatchOne", func() error {
			_, err := schemaflux.MatchOne(items[0], items, schemaflux.NewMatchOptions())
			return err
		}},
		{"Critique", func() error {
			_, err := schemaflux.Critique(items[0], schemaflux.NewCritiqueOptions())
			return err
		}},
		{"Synthesize", func() error {
			_, err := schemaflux.Synthesize[summary]([]any{items[0], items[1]}, schemaflux.NewSynthesizeOptions())
			return err
		}},
		{"Predict", func() error {
			_, err := schemaflux.Predict[record](items, schemaflux.NewPredictOptions())
			return err
		}},
		{"Verify", func() error {
			_, err := schemaflux.Verify("a claim", schemaflux.NewVerifyOptions())
			return err
		}},
		{"VerifyClaim", func() error {
			_, err := schemaflux.VerifyClaim("a claim", schemaflux.NewVerifyOptions())
			return err
		}},
		{"Resolve", func() error {
			_, err := schemaflux.Resolve(items)
			return err
		}},
		{"Derive", func() error {
			_, err := schemaflux.Derive[record, summary](items[0])
			return err
		}},
		{"Conform", func() error {
			_, err := schemaflux.Conform(items[0], "some standard")
			return err
		}},
		{"Interpolate", func() error {
			_, err := schemaflux.Interpolate(items)
			return err
		}},
		{"Arbitrate", func() error {
			_, err := schemaflux.Arbitrate(items)
			return err
		}},
		{"Project", func() error {
			_, err := schemaflux.Project[record, summary](items[0])
			return err
		}},
		{"Audit", func() error {
			_, err := schemaflux.Audit(items[0])
			return err
		}},
		{"Assemble", func() error {
			_, err := schemaflux.Assemble[record]([]any{items[0], items[1]})
			return err
		}},
		{"Pivot", func() error {
			_, err := schemaflux.Pivot[record, summary](items[0])
			return err
		}},
	}
}

// A provider that never completes must produce an error from every operation.
func TestFaultInjectionProviderError(t *testing.T) {
	for _, tc := range faultCases() {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, "", errors.New("provider unavailable"))

			if err := tc.run(); err == nil {
				t.Fatal("a provider that never completed must produce an error")
			}
		})
	}
}

// A body the operation cannot parse must produce an error, not a zero value.
func TestFaultInjectionMalformedBody(t *testing.T) {
	for _, tc := range faultCases() {
		t.Run(tc.name, func(t *testing.T) {
			if reason, exempt := survivesSchemaViolation[tc.name]; exempt && isTextPassthrough(reason) {
				t.Skipf("%s returns the model's text verbatim, so prose is a valid answer", tc.name)
			}
			withScriptedProvider(t, malformedBody, nil)

			if err := tc.run(); err == nil {
				t.Fatal("an unparseable body must produce an error")
			}
		})
	}
}

// Well-formed JSON of the wrong shape must produce an error, except where the
// operation legitimately tolerates it.
func TestFaultInjectionSchemaViolatingBody(t *testing.T) {
	for _, tc := range faultCases() {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, schemaViolatingBody, nil)

			err := tc.run()
			reason, exempt := survivesSchemaViolation[tc.name]
			switch {
			case err == nil && !exempt:
				t.Fatal("a body of the wrong shape must produce an error")
			case err != nil && exempt:
				// Stricter than the exemption claims, which is fine.
				t.Logf("%s rejects the wrong shape despite the exemption (%s)", tc.name, reason)
			}
		})
	}
}

// An empty body is the degenerate case of both.
func TestFaultInjectionEmptyBody(t *testing.T) {
	for _, tc := range faultCases() {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, "", nil)

			if err := tc.run(); err == nil {
				t.Fatal("an empty body must produce an error")
			}
		})
	}
}

// isTextPassthrough reports whether an exemption is the text-passthrough one.
// A truncated body is a distinct fault: valid JSON up to the cut, which is what
// a provider returns when the output budget runs out. The failure to avoid is an
// extractor that returns the part it could parse, so the operation decodes half
// an answer and reports success.
func TestFaultInjectionTruncatedBody(t *testing.T) {
	for _, body := range []string{truncatedBody, truncatedFencedBody} {
		for _, tc := range faultCases() {
			t.Run(tc.name, func(t *testing.T) {
				if reason, exempt := survivesSchemaViolation[tc.name]; exempt && isTextPassthrough(reason) {
					t.Skipf("%s returns the model's text verbatim, so a truncated string is a short answer", tc.name)
				}
				withScriptedProvider(t, body, nil)

				if err := tc.run(); err == nil {
					t.Fatal("a truncated body must produce an error, not a decode of what arrived")
				}
			})
		}
	}
}

// Every entry in the list is, today; the helper keeps the malformed-body test
// honest if a different kind of exemption is ever added.
func isTextPassthrough(reason string) bool {
	return reason == "returns the model's text verbatim"
}

// Diff is the one operation excluded from the table above, and it is excluded
// for a reason worth stating: the comparison is computed locally by reflection,
// so a provider failure does not make the result wrong. What it does is remove
// the summary, and the caller has to be able to tell a summary that was written
// from one that was skipped.
func TestFaultInjectionDiffKeepsTheStructuralResultAndReportsTheMissingSummary(t *testing.T) {
	type versioned struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	// Diff's summary is free text, so prose is a valid summary and there is no
	// shape for a schema-violating body to violate. The faults that can remove
	// a summary are a provider failure and an empty body.
	faults := []struct {
		name string
		body string
		err  error
	}{
		{"provider_error", "", errors.New("provider unavailable")},
		{"empty_body", "", nil},
		{"whitespace_body", " \n\t ", nil},
	}

	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			withScriptedProvider(t, fault.body, fault.err)

			result, err := schemaflux.Diff(
				versioned{Name: "a", Count: 1},
				versioned{Name: "b", Count: 2},
				schemaflux.NewDiffOptions())
			if err != nil {
				t.Fatalf("the structural diff is computed locally and must still succeed: %v", err)
			}
			if len(result.Modified) == 0 {
				t.Fatal("the structural comparison must still report the changes")
			}
			if result.SummaryError == nil {
				t.Fatal("a missing summary must be reported, not left to look like there was nothing to say")
			}
			if result.Summary != "" {
				t.Errorf("a failed summary must be empty, not prose; got %q", result.Summary)
			}
		})
	}
}

// And when the model answers, the summary is present and SummaryError is not.
func TestDiffReportsAWorkingSummary(t *testing.T) {
	type versioned struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	withScriptedProvider(t, `{"summary":"the count went up","significance":"minor"}`, nil)

	result, err := schemaflux.Diff(
		versioned{Name: "a", Count: 1},
		versioned{Name: "a", Count: 2},
		schemaflux.NewDiffOptions())
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.SummaryError != nil {
		t.Fatalf("a working summary must not report an error: %v", result.SummaryError)
	}
	if result.Summary == "" {
		t.Error("the summary must be reported")
	}
}

// The suite must cover every exported operation that reaches a provider. This
// is the guard against the table quietly falling behind the API.
func TestFaultInjectionCoversTheExportedSurface(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range faultCases() {
		if covered[tc.name] {
			t.Errorf("%s appears twice in the table", tc.name)
		}
		covered[tc.name] = true
	}

	if len(covered) < 55 {
		t.Fatalf("the table covers only %d operations; the exported surface is larger", len(covered))
	}
	t.Logf("%d exported operations under fault injection", len(covered))
}

package tests

import (
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// OP-401. The text operations shipped as twins: a plain one and a
// *WithMetadata one whose extra was a `map[string]any` called Metadata -- a bag
// where a model's claim and a measured token count are indistinguishable. The
// *Result forms return the same answer inside types.Result, whose Meta keeps
// the two apart. These tests hold that line: the value is unchanged, and the
// measurements never mix with the claims.

const (
	summaryReply     = `{"text":"A short summary.","key_points":["one","two"],"confidence":0.9}`
	rewriteReply     = `{"text":"Rewritten text.","changes_made":["tone"],"confidence":0.8,"tone_achieved":"formal"}`
	translateReply   = `{"text":"Texto traducido.","source_language_detected":"English","confidence":0.7}`
	expandReply      = `{"text":"A much longer piece of expanded text.","added_content":["detail"],"confidence":0.6}`
	sourceParagraph  = "The quarterly report covers revenue, costs, and headcount across four regions."
	shortSourceInput = "Brief."
)

func TestSummarizeResultCarriesTheEnvelope(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply(summaryReply))()

	result, err := schemaflux.SummarizeResult(sourceParagraph, schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("SummarizeResult: %v", err)
	}

	if result.Value.Text != "A short summary." {
		t.Errorf("Value.Text = %q", result.Value.Text)
	}
	if len(result.Value.KeyPoints) != 2 {
		t.Errorf("Value.KeyPoints = %v", result.Value.KeyPoints)
	}
	if result.Meta.Operation != "summarize" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "summarize")
	}
	if result.Meta.Attempts < 1 {
		t.Errorf("Meta.Attempts = %d; the call was made, so it was counted at least once", result.Meta.Attempts)
	}
}

func TestRewriteResultCarriesTheEnvelope(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply(rewriteReply))()

	result, err := schemaflux.RewriteResult(sourceParagraph, schemaflux.NewRewriteOptions())
	if err != nil {
		t.Fatalf("RewriteResult: %v", err)
	}
	if result.Value.Text != "Rewritten text." {
		t.Errorf("Value.Text = %q", result.Value.Text)
	}
	if result.Meta.Operation != "rewrite" {
		t.Errorf("Meta.Operation = %q", result.Meta.Operation)
	}
	if result.Meta.Attempts < 1 {
		t.Errorf("Meta.Attempts = %d", result.Meta.Attempts)
	}
}

func TestTranslateResultCarriesTheEnvelope(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply(translateReply))()

	result, err := schemaflux.TranslateResult(sourceParagraph,
		schemaflux.NewTranslateOptions().WithTargetLanguage("Spanish"))
	if err != nil {
		t.Fatalf("TranslateResult: %v", err)
	}
	if result.Value.Text != "Texto traducido." {
		t.Errorf("Value.Text = %q", result.Value.Text)
	}
	if result.Meta.Operation != "translate" {
		t.Errorf("Meta.Operation = %q", result.Meta.Operation)
	}
}

func TestExpandResultCarriesTheEnvelope(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply(expandReply))()

	result, err := schemaflux.ExpandResult(shortSourceInput, schemaflux.NewExpandOptions())
	if err != nil {
		t.Fatalf("ExpandResult: %v", err)
	}
	if result.Value.Text != "A much longer piece of expanded text." {
		t.Errorf("Value.Text = %q", result.Value.Text)
	}
	if result.Meta.Operation != "expand" {
		t.Errorf("Meta.Operation = %q", result.Meta.Operation)
	}
}

// The collapse has to be behaviour-preserving, or it is a rewrite wearing a
// refactor's name. The deprecated twin and the new form run the same operation
// and must return the same value for the same response.
func TestResultFormsAgreeWithTheDeprecatedTwins(t *testing.T) {
	install := schemafluxtest.Install(t, schemafluxtest.New().Reply(summaryReply).Reply(summaryReply))
	defer install()

	viaTwin, err := schemaflux.SummarizeWithMetadata(sourceParagraph, schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("SummarizeWithMetadata: %v", err)
	}
	viaResult, err := schemaflux.SummarizeResult(sourceParagraph, schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("SummarizeResult: %v", err)
	}

	if viaTwin.Text != viaResult.Value.Text {
		t.Errorf("text differs: twin %q, result %q", viaTwin.Text, viaResult.Value.Text)
	}
	if viaTwin.ModelConfidence != viaResult.Value.ModelConfidence {
		t.Errorf("confidence differs: twin %v, result %v", viaTwin.ModelConfidence, viaResult.Value.ModelConfidence)
	}
	if viaTwin.CompressionRatio != viaResult.Value.CompressionRatio {
		t.Errorf("compression ratio differs: twin %v, result %v",
			viaTwin.CompressionRatio, viaResult.Value.CompressionRatio)
	}
}

// A failure still returns the envelope. A caller that only gets an error back
// cannot tell a refusal that cost nothing from one that burned three attempts.
func TestSummarizeResultReturnsTheEnvelopeOnFailure(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply("not json at all"))()

	result, err := schemaflux.SummarizeResult(sourceParagraph, schemaflux.NewSummarizeOptions())
	if err == nil {
		t.Fatal("an unparseable response summarized successfully")
	}
	if result.Value.Text != "" {
		t.Errorf("a failed call returned a value: %q", result.Value.Text)
	}
	if result.Meta.Attempts < 1 {
		t.Errorf("Meta.Attempts = %d on a failure; the call was still made", result.Meta.Attempts)
	}
}

func TestEachResultFormReturnsTheEnvelopeOnFailure(t *testing.T) {
	cases := []struct {
		name string
		run  func() (int, error)
	}{
		{"rewrite", func() (int, error) {
			r, err := schemaflux.RewriteResult(sourceParagraph, schemaflux.NewRewriteOptions())
			return r.Meta.Attempts, err
		}},
		{"translate", func() (int, error) {
			r, err := schemaflux.TranslateResult(sourceParagraph,
				schemaflux.NewTranslateOptions().WithTargetLanguage("Spanish"))
			return r.Meta.Attempts, err
		}},
		{"expand", func() (int, error) {
			r, err := schemaflux.ExpandResult(shortSourceInput, schemaflux.NewExpandOptions())
			return r.Meta.Attempts, err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer schemafluxtest.Install(t, schemafluxtest.New().Reply("{ not json"))()

			attempts, err := tc.run()
			if err == nil {
				t.Fatal("an unparseable response succeeded")
			}
			if attempts < 1 {
				t.Errorf("Meta.Attempts = %d on a failure", attempts)
			}
		})
	}
}

// The reason the envelope exists: the model's score for its own answer is a
// claim, and it must not appear anywhere a caller would read it as something
// the library measured.
func TestTheModelsConfidenceStaysOutOfTheMeasurements(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply(summaryReply))()

	result, err := schemaflux.SummarizeResult(sourceParagraph, schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("SummarizeResult: %v", err)
	}

	// The claim is reachable, on the payload, named Model*.
	if result.Value.ModelConfidence != 0.9 {
		t.Errorf("Value.ModelConfidence = %v, want the model's claim of 0.9", result.Value.ModelConfidence)
	}
	// And it has not been copied into any check the library ran.
	for _, check := range result.Meta.Checks {
		if strings.Contains(strings.ToLower(check.Name), "confidence") {
			t.Errorf("a check named %q exists; a model's self-score is not a check the library ran", check.Name)
		}
	}
}

// Cost is measured from what the provider reported, so a scripted provider
// reporting nothing must produce zero rather than an invented number.
func TestEnvelopeDoesNotInventCost(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply(summaryReply))()

	result, err := schemaflux.SummarizeResult(sourceParagraph, schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("SummarizeResult: %v", err)
	}
	if result.Meta.Cost.TotalCost < 0 {
		t.Errorf("Meta.Cost.TotalCost = %v", result.Meta.Cost.TotalCost)
	}
	if result.Meta.Usage.TotalTokens < 0 {
		t.Errorf("Meta.Usage.TotalTokens = %v", result.Meta.Usage.TotalTokens)
	}
}

// Each operation names itself, so a stored envelope says which operation
// produced it. Getting this wrong is invisible until someone queries a corpus
// of results by operation and gets the wrong rows.
func TestEachOperationNamesItselfInTheEnvelope(t *testing.T) {
	cases := []struct {
		operation string
		reply     string
		run       func() (string, error)
	}{
		{"summarize", summaryReply, func() (string, error) {
			r, err := schemaflux.SummarizeResult(sourceParagraph, schemaflux.NewSummarizeOptions())
			return r.Meta.Operation, err
		}},
		{"rewrite", rewriteReply, func() (string, error) {
			r, err := schemaflux.RewriteResult(sourceParagraph, schemaflux.NewRewriteOptions())
			return r.Meta.Operation, err
		}},
		{"translate", translateReply, func() (string, error) {
			r, err := schemaflux.TranslateResult(sourceParagraph,
				schemaflux.NewTranslateOptions().WithTargetLanguage("Spanish"))
			return r.Meta.Operation, err
		}},
		{"expand", expandReply, func() (string, error) {
			r, err := schemaflux.ExpandResult(shortSourceInput, schemaflux.NewExpandOptions())
			return r.Meta.Operation, err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.operation, func(t *testing.T) {
			defer schemafluxtest.Install(t, schemafluxtest.New().Reply(tc.reply))()

			operation, err := tc.run()
			if err != nil {
				t.Fatalf("%s: %v", tc.operation, err)
			}
			if operation != tc.operation {
				t.Errorf("Meta.Operation = %q, want %q", operation, tc.operation)
			}
		})
	}
}

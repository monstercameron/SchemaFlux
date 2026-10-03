package tests

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// LX-028, VerifyClaim through the exported API and the real OpenAI provider.
// What it reports for a claim is never better than the review the claim came
// from; a verdict it lowers says what the model called the claim and why that
// was not kept; and what could not be checked stays that.
func TestIntegrationVerifyClaim(t *testing.T) {
	claim := func(verdict, confidence string) string {
		return `{"claim":"c","verdict":"` + verdict + `","confidence":` + confidence + `,"evidence":["the filing"],"reasoning":"the filing says so","corrections":null}`
	}
	answer := func(overall, claims, logic, consistency string) string {
		return `{"summary":"All claims are accurate","overall_confidence":` + overall + `,"trust_score":0.5,"claims":[` + claims + `],"logic_issues":` + logic + `,"consistency_issues":` + consistency + `}`
	}
	const contradiction = `[{"type":"contradiction","description":"d","conflicting_items":["a","b"],"suggestion":null}]`
	const critical = `[{"type":"non_sequitur","description":"d","location":null,"severity":"critical"}]`

	cases := []struct {
		name         string
		opts         schemaflux.VerifyOptions
		body         string
		wantErr      error
		sent         int
		verdict      string
		modelVerdict string
		note         string
	}{
		{"a verified claim", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("verified", "0.9"), "[]", "[]"), nil, 1, "verified", "", ""},
		{"a false claim", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("false", "0.9"), "[]", "[]"), nil, 1, "false", "", ""},
		{"a verified claim in content that contradicts itself", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("verified", "0.9"), "[]", contradiction), nil, 1, "partially_true", "verified", "1 consistency issue"},
		{"a verified claim behind a critical flaw", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("verified", "0.9"), critical, "[]"), nil, 1, "false", "verified", "1 reasoning issue"},
		{"a verified claim from a review unsure of itself", schemaflux.NewVerifyOptions(),
			answer("0.3", claim("verified", "0.9"), "[]", "[]"), nil, 1, "unverifiable", "verified", "below the confidence floor"},
		{"a verified claim the model was unsure of", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("verified", "0.3"), "[]", "[]"), nil, 1, "unverifiable", "verified", "its confidence is below the floor"},
		{"an unverifiable claim in content that contradicts itself", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("unverifiable", "0.9"), "[]", contradiction), nil, 1, "unverifiable", "", ""},
		{"an unverifiable claim behind a critical flaw", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("unverifiable", "0.9"), critical, "[]"), nil, 1, "unverifiable", "", ""},
		{"a verified claim beside a false one", schemaflux.NewVerifyOptions(),
			answer("0.9", claim("verified", "0.9")+","+claim("false", "0.8"), "[]", "[]"), nil, 1, "false", "", ""},
		{"no claim assessed", schemaflux.NewVerifyOptions(),
			answer("0.9", "", "[]", "[]"), schemaflux.ErrInvariantViolation, 1, "", "", ""},
		// Nothing would assess the claim, so nothing is sent.
		{"the fact check is off", schemaflux.NewVerifyOptions().WithCheckFacts(false),
			answer("0.9", claim("verified", "0.9"), "[]", "[]"), schemaflux.ErrConfiguration, 0, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			result, err := schemaflux.VerifyClaim("Revenue rose last year.", tc.opts)
			if len(*captured) != tc.sent {
				t.Fatalf("%d requests were sent, want %d", len(*captured), tc.sent)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if result.Verdict != "" || result.Claim != "" || len(result.Evidence) != 0 {
					t.Fatalf("a failed call returned a usable result: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyClaim: %v", err)
			}
			if result.Verdict != tc.verdict || result.ModelVerdict != tc.modelVerdict {
				t.Errorf("Verdict = %q, ModelVerdict = %q; want %q and %q", result.Verdict, result.ModelVerdict, tc.verdict, tc.modelVerdict)
			}
			if !strings.Contains(result.Note, tc.note) || (tc.note == "") != (result.Note == "") {
				t.Errorf("Note = %q, want it to say %q", result.Note, tc.note)
			}
			if result.Claim != "Revenue rose last year." {
				t.Errorf("Claim = %q, want the caller's own", result.Claim)
			}
			// The model's words for the claim stay the model's.
			if result.Reasoning != "the filing says so" {
				t.Errorf("Reasoning = %q, want the model's own for the claim", result.Reasoning)
			}
			if strings.Contains(result.Note, "All claims are accurate") {
				t.Errorf("the review's summary is in the note: %q", result.Note)
			}
		})
	}
}

// ExampleVerifyClaim checks one claim. Verdict is this library's: the model
// called the claim verified, and the content it sits in contradicts itself, so
// it is not reported as verified. What the model said and why it was not kept
// are both there to read. It runs under go test with a scripted provider: no
// credential, no spend.
func ExampleVerifyClaim() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"summary": "The figures are in the filing.", "overall_confidence": 0.9, "trust_score": 0.6,
		  "claims": [{"claim": "Revenue rose 10%", "verdict": "verified", "confidence": 0.9, "evidence": ["the annual filing"], "reasoning": "The filing reports a 10% rise.", "corrections": null}],
		  "logic_issues": [],
		  "consistency_issues": [{"type": "contradiction", "description": "Revenue is said to have risen and to have fallen.", "conflicting_items": ["rose 10%", "fell"], "suggestion": null}]}`))

	result, err := schemaflux.VerifyClaim("Revenue rose 10% last year, so revenue fell.", schemaflux.NewVerifyOptions())
	if err != nil {
		fmt.Println("the claim could not be checked:", err)
		return
	}

	fmt.Println("verdict:", result.Verdict)
	fmt.Println("the model said:", result.ModelVerdict)
	fmt.Println("why:", result.Note)

	// Output:
	// verdict: partially_true
	// the model said: verified
	// why: reported as partially_true, not verified: the review of the content as a whole came out mixed (1 consistency issue)
}

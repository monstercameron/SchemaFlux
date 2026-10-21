package ops

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

const verifyMockResponse = `{
	"overall_confidence": 0.6,
	"claims": [
		{"claim": "Water boils at 100C", "verdict": "verified", "confidence": 0.95, "reasoning": "a standard physical constant", "evidence": ["standard reference"]},
		{"claim": "The capital of France is London", "verdict": "false", "confidence": 0.99, "reasoning": "Paris is the capital", "evidence": ["geography reference"], "corrections": "The capital of France is Paris"}
	],
	"logic_issues": [
		{"type": "non_sequitur", "description": "conclusion does not follow", "location": "paragraph 2", "severity": "major"}
	],
	"consistency_issues": [
		{"type": "contradiction", "description": "two dates disagree", "conflicting_items": ["date A", "date B"], "suggestion": "reconcile the dates"}
	],
	"summary": "Mostly accurate with one false claim",
	"trust_score": 0.5
}`

func installVerifyResponse(t *testing.T) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return verifyMockResponse, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

// TestVerifyWithModelMatchesLegacyVerify pins the mapping between the two views
// of one result: for the same response, VerifyWithModel's Verdict and Issues
// describe the same findings as the deprecated Verify's OverallVerdict and
// Claims. Both come from one verifyCore, so this compares the views with each
// other and not either with the model's answer.
func TestVerifyWithModelMatchesLegacyVerify(t *testing.T) {
	installVerifyResponse(t)

	opts := NewVerifyOptions().WithMinConfidence(0)

	legacy, err := Verify("some claims", opts)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	judged, err := VerifyWithModel("some claims", opts)
	if err != nil {
		t.Fatalf("VerifyWithModel failed: %v", err)
	}

	if legacy.OverallVerdict != "mixed" {
		t.Fatalf("expected legacy overall_verdict mixed, got %s", legacy.OverallVerdict)
	}
	if judged.Verdict != types.VerdictMixed {
		t.Errorf("expected VerdictMixed, got %v", judged.Verdict)
	}

	// Only the false claim, plus the logic and consistency issues, should
	// become JudgmentIssues -- the verified claim is not a finding.
	wantIssues := 1 /* false claim */ + len(legacy.LogicIssues) + len(legacy.ConsistencyIssues)
	if len(judged.Issues) != wantIssues {
		t.Fatalf("expected %d issues, got %d: %+v", wantIssues, len(judged.Issues), judged.Issues)
	}

	var foundClaim, foundLogic, foundConsistency bool
	for _, issue := range judged.Issues {
		switch {
		case issue.Subject == "The capital of France is London":
			foundClaim = true
			if issue.Severity != "error" {
				t.Errorf("expected false claim severity 'error', got %q", issue.Severity)
			}
			if issue.ModelConfidence != 0.99 {
				t.Errorf("expected per-claim ModelConfidence 0.99, got %v", issue.ModelConfidence)
			}
		case issue.Category == "logic:non_sequitur":
			foundLogic = true
		case issue.Category == "consistency:contradiction":
			foundConsistency = true
		}
	}
	if !foundClaim || !foundLogic || !foundConsistency {
		t.Fatalf("expected claim, logic, and consistency issues, got %+v", judged.Issues)
	}

	// The model's overall confidence and trust score are claims, reachable
	// but segregated from Verdict and Issues.
	if judged.ModelConfidence != legacy.ModelOverallConfidence {
		t.Errorf("expected ModelConfidence %v, got %v", legacy.ModelOverallConfidence, judged.ModelConfidence)
	}
	if judged.ModelClaims["trust_score"] != legacy.ModelTrustScore {
		t.Errorf("expected trust_score claim %v, got %v", legacy.ModelTrustScore, judged.ModelClaims["trust_score"])
	}
}

// TestVerifyWithModelVerdictMapping proves every overall_verdict value maps
// onto the reduced Verdict vocabulary without silently defaulting a value
// this operation does not recognize to VerdictPass.
func TestVerifyWithModelVerdictMapping(t *testing.T) {
	cases := map[string]types.Verdict{
		"verified":       types.VerdictPass,
		"false":          types.VerdictFail,
		"partially_true": types.VerdictMixed,
		"misleading":     types.VerdictMixed,
		"unverifiable":   types.VerdictUnknown,
		"nonsense":       types.VerdictUnknown,
	}
	for overall, want := range cases {
		if got := verifyVerdict(overall); got != want {
			t.Errorf("verifyVerdict(%q) = %v, want %v", overall, got, want)
		}
	}
}

func TestVerifyOptions(t *testing.T) {
	t.Run("NewVerifyOptions creates valid defaults", func(t *testing.T) {
		opts := NewVerifyOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("default options should be valid: %v", err)
		}
	})

	t.Run("WithSources sets sources", func(t *testing.T) {
		sources := []any{"source1", "source2"}
		opts := NewVerifyOptions().WithSources(sources)
		if len(opts.Sources) != 2 {
			t.Errorf("expected 2 sources, got %d", len(opts.Sources))
		}
	})

	t.Run("WithStrictness sets strictness", func(t *testing.T) {
		opts := NewVerifyOptions().WithStrictness("strict")
		if opts.Strictness != "strict" {
			t.Errorf("expected strict, got %s", opts.Strictness)
		}
	})

	t.Run("WithIncludeEvidence sets evidence flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithIncludeEvidence(false)
		if opts.IncludeEvidence {
			t.Error("expected IncludeEvidence to be false")
		}
	})

	t.Run("WithExplainReasoning sets reasoning flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithExplainReasoning(false)
		if opts.ExplainReasoning {
			t.Error("expected ExplainReasoning to be false")
		}
	})

	t.Run("WithCheckLogic sets logic flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithCheckLogic(false)
		if opts.CheckLogic {
			t.Error("expected CheckLogic to be false")
		}
	})

	t.Run("WithCheckFacts sets facts flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithCheckFacts(false)
		if opts.CheckFacts {
			t.Error("expected CheckFacts to be false")
		}
	})

	t.Run("WithCheckConsistency sets consistency flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithCheckConsistency(false)
		if opts.CheckConsistency {
			t.Error("expected CheckConsistency to be false")
		}
	})

	t.Run("WithDomain sets domain", func(t *testing.T) {
		opts := NewVerifyOptions().WithDomain("science")
		if opts.Domain != "science" {
			t.Errorf("expected science, got %s", opts.Domain)
		}
	})

	t.Run("WithTrustedSources sets trusted sources", func(t *testing.T) {
		opts := NewVerifyOptions().WithTrustedSources([]string{"PubMed", "WHO"})
		if len(opts.TrustedSources) != 2 {
			t.Errorf("expected 2 trusted sources, got %d", len(opts.TrustedSources))
		}
	})

	t.Run("WithMinConfidence sets min confidence", func(t *testing.T) {
		opts := NewVerifyOptions().WithMinConfidence(0.9)
		if opts.MinConfidence != 0.9 {
			t.Errorf("expected 0.9, got %f", opts.MinConfidence)
		}
	})

	t.Run("Validate rejects invalid strictness", func(t *testing.T) {
		opts := NewVerifyOptions()
		opts.Strictness = "invalid"
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid strictness")
		}
	})

	t.Run("Validate rejects invalid min confidence", func(t *testing.T) {
		opts := NewVerifyOptions().WithMinConfidence(1.5)
		if err := opts.Validate(); err == nil {
			t.Error("expected error for confidence > 1")
		}
	})
}

// Verify fact-checks, checks logic and checks consistency against a scripted
// model. These replace two tests that skipped themselves for want of a live
// provider and so asserted nothing.
func TestVerify(t *testing.T) {
	t.Run("fact-checks claims and finds the false one", func(t *testing.T) {
		answer := verifyAnswer(`[
			{"claim":"The Earth is about 4.5 billion years old","verdict":"verified","confidence":0.95,"evidence":["geology"],"reasoning":"dated meteorites","sources":[],"corrections":null},
			{"claim":"The capital of France is London","verdict":"false","confidence":0.99,"evidence":["atlas"],"reasoning":"it is Paris","sources":[],"corrections":"Paris"}
		]`, `[]`, `[]`)

		result, err, _ := verifyWith(t, "claims", answer, NewVerifyOptions().WithMinConfidence(0))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if len(result.Claims) != 2 {
			t.Fatalf("claims = %+v", result.Claims)
		}
		if result.Claims[1].Verdict != "false" || result.Claims[1].Corrections != "Paris" {
			t.Errorf("the false claim came back as %+v", result.Claims[1])
		}
		if result.OverallVerdict != verdictMixed {
			t.Errorf("OverallVerdict = %q, want %q: one claim verified and one false", result.OverallVerdict, verdictMixed)
		}
	})

	t.Run("checks logic", func(t *testing.T) {
		answer := verifyAnswer(claimsJSON("verified"),
			`[{"type":"false_premise","description":"not all birds fly","location":"line 1","severity":"major"}]`, `[]`)

		result, err, _ := verifyWith(t, "All birds fly. Penguins are birds. So penguins fly.", answer, NewVerifyOptions().WithMinConfidence(0))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if len(result.LogicIssues) != 1 || result.LogicIssues[0].Severity != "major" {
			t.Errorf("LogicIssues = %+v", result.LogicIssues)
		}
		if result.OverallVerdict == verdictVerified {
			t.Errorf("a major logic issue left the verdict %q", result.OverallVerdict)
		}
	})
}

// A claim is checked on its own, through the same core Verify calls.
func TestVerifyClaim(t *testing.T) {
	answer := verifyAnswer(`[{"claim":"c","verdict":"verified","confidence":0.9,"evidence":["e"],"reasoning":"r","sources":[],"corrections":null}]`, `[]`, `[]`)
	swapLLMResponse(t, answer)

	got, err := VerifyClaim("The speed of light is about 300,000 km/s", NewVerifyOptions().WithMinConfidence(0.5))
	if err != nil {
		t.Fatalf("VerifyClaim: %v", err)
	}
	if got.Verdict != "verified" || got.Claim != "The speed of light is about 300,000 km/s" {
		t.Errorf("VerifyClaim = %+v", got)
	}
}

// A consistency issue names what conflicts. The schema makes the list required,
// and a provider that does not hold the model to the schema could leave it out
// without anything noticing.
func TestVerifyRefusesAConsistencyIssueWithNoItems(t *testing.T) {
	cases := []struct {
		name  string
		issue string
		ok    bool
	}{
		{"items listed", `{"type":"contradiction","description":"d","conflicting_items":["a","b"]}`, true},
		{"an empty list is a list", `{"type":"inconsistency","description":"d","conflicting_items":[]}`, true},
		{"no items key", `{"type":"contradiction","description":"d"}`, false},
		{"null items", `{"type":"contradiction","description":"d","conflicting_items":null}`, false},
		{"no items on the second issue", `{"type":"ambiguity","description":"d","conflicting_items":["a"]},{"type":"contradiction","description":"e"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer := verifyAnswer(claimsJSON("verified"), `[]`, `[`+tc.issue+`]`)
			result, err, _ := verifyWith(t, "content", answer, NewVerifyOptions().WithMinConfidence(0))
			if tc.ok {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("an issue with no list of conflicting items was accepted: %+v", result.ConsistencyIssues)
			}
			if kind := types.KindOf(err); kind != types.KindSchemaViolation {
				t.Errorf("kind = %v, want a schema violation", kind)
			}
			if !reflect.DeepEqual(result, VerifyResult{}) {
				t.Errorf("a result came back beside the error: %+v", result)
			}
		})
	}
}

// A claim the model called verified and the library lowered is reported with
// the reason it was lowered. It used to be reported with the model's
// justification of the verdict it had lost.
func TestVerifyWithModelReportsWhyAVerdictWasLowered(t *testing.T) {
	answer := verifyAnswer(`[{"claim":"c","verdict":"verified","confidence":0.3,"evidence":["e"],"reasoning":"the model's case for verified","sources":[],"corrections":null}]`, `[]`, `[]`)
	swapLLMResponse(t, answer)

	judged, err := VerifyWithModel("content", NewVerifyOptions().WithMinConfidence(0.8))
	if err != nil {
		t.Fatalf("VerifyWithModel: %v", err)
	}
	if len(judged.Issues) != 1 {
		t.Fatalf("issues = %+v, want the lowered claim", judged.Issues)
	}
	issue := judged.Issues[0]
	if issue.Category != "fact:unverifiable" {
		t.Errorf("Category = %q", issue.Category)
	}
	if strings.Contains(issue.Message, "the model's case for verified") {
		t.Errorf("the message is the model's justification of the verdict it lost: %q", issue.Message)
	}
	if !strings.Contains(issue.Message, "below the floor") {
		t.Errorf("the message does not give the reason the verdict was lowered: %q", issue.Message)
	}

	// A claim the model itself called false keeps its own reasoning.
	answer = verifyAnswer(`[{"claim":"c","verdict":"false","confidence":0.9,"evidence":["e"],"reasoning":"it is Paris","sources":[],"corrections":null}]`, `[]`, `[]`)
	swapLLMResponse(t, answer)
	judged, err = VerifyWithModel("content", NewVerifyOptions().WithMinConfidence(0.5))
	if err != nil || len(judged.Issues) != 1 || judged.Issues[0].Message != "it is Paris" {
		t.Errorf("a claim the model refuted: %+v, %v", judged.Issues, err)
	}
}

func TestVerifyRefusalsBeforeACallHaveAKind(t *testing.T) {
	refuse := func(change func(*VerifyOptions)) func() error {
		return func() error {
			options := NewVerifyOptions()
			change(&options)
			_, err := Verify("a statement", options)
			return err
		}
	}
	checkRefusalsBeforeACall(t, []refusalBeforeACall{
		{"a strictness nobody knows", refuse(func(o *VerifyOptions) { o.Strictness = "severe" }),
			"verify: configuration: invalid options: verification strictness must be strict, moderate, or lenient", types.KindConfiguration},
		{"a minimum confidence above one", refuse(func(o *VerifyOptions) { o.MinConfidence = 1.5 }),
			"verify: configuration: invalid options: min confidence must be between 0 and 1, got 1.5", types.KindConfiguration},
		{"a negative minimum confidence", refuse(func(o *VerifyOptions) { o.MinConfidence = -0.1 }),
			"verify: configuration: invalid options: min confidence must be between 0 and 1, got -0.1", types.KindConfiguration},
		{"a minimum confidence that is not a number", refuse(func(o *VerifyOptions) { o.MinConfidence = math.NaN() }),
			"verify: configuration: invalid options: min confidence must be between 0 and 1, got NaN", types.KindConfiguration},
		{"every check off", refuse(func(o *VerifyOptions) { o.CheckFacts, o.CheckLogic, o.CheckConsistency = false, false, false }),
			"verify: configuration: invalid options: CheckFacts, CheckLogic and CheckConsistency are all off, so there is nothing to verify", types.KindConfiguration},
		{"a threshold above one on the common half", refuse(func(o *VerifyOptions) { o.CommonOptions.Threshold = 6 }),
			"verify: configuration: invalid options: threshold must be between 0 and 1, got 6.000000", types.KindConfiguration},
		{"a nil input", func() error {
			_, err := Verify(nil, NewVerifyOptions())
			return err
		}, "failed to normalize input: input is nil", types.KindInvalidRequest},
		{"VerifyWithModel with every check off", func() error {
			options := NewVerifyOptions()
			options.CheckFacts, options.CheckLogic, options.CheckConsistency = false, false, false
			_, err := VerifyWithModel("a statement", options)
			return err
		}, "verify: configuration: invalid options: CheckFacts, CheckLogic and CheckConsistency are all off, so there is nothing to verify", types.KindConfiguration},
		{"VerifyWithModel with a nil input", func() error {
			_, err := VerifyWithModel(nil, NewVerifyOptions())
			return err
		}, "failed to normalize input: input is nil", types.KindInvalidRequest},
		{"VerifyClaim with the fact check off", func() error {
			options := NewVerifyOptions()
			options.CheckFacts = false
			_, err := VerifyClaim("a claim", options)
			return err
		}, "verify: configuration: VerifyClaim reports whether a claim is true, and CheckFacts is off, so nothing would assess it", types.KindConfiguration},
		{"VerifyClaim with a bad option", func() error {
			options := NewVerifyOptions()
			options.Strictness = "severe"
			_, err := VerifyClaim("a claim", options)
			return err
		}, "verify: configuration: invalid options: verification strictness must be strict, moderate, or lenient", types.KindConfiguration},
	})
}

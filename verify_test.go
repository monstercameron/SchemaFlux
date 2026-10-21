package schemaflux_test

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// verifyReport writes an answer to Verify with the claims, logic issues and
// consistency issues given as JSON lists, and overall confidence 0.9.
func verifyReport(claims, logic, consistency string) string {
	return `{"summary":"s","overall_confidence":0.9,"trust_score":0.85,"claims":` + claims +
		`,"logic_issues":` + logic + `,"consistency_issues":` + consistency + `}`
}

const (
	verifyVerifiedClaim = `{"claim":"c1","verdict":"verified","confidence":0.9,"corrections":"","evidence":["e1"],"reasoning":"r1"}`
	verifyFalseClaim    = `{"claim":"c2","verdict":"false","confidence":0.8,"corrections":"fix","evidence":["e2"],"reasoning":"r2"}`
)

var verifyClean = verifyReport("["+verifyVerifiedClaim+"]", "[]", "[]")

func claimOf(verdict, confidence string) string {
	return `{"claim":"c","verdict":"` + verdict + `","confidence":` + confidence + `,"corrections":"","evidence":["e"],"reasoning":"r"}`
}

func TestVerifyReturnsTheJudgmentTheClaimsAndTheTrustScore(t *testing.T) {
	t.Parallel()
	model := answeringWith(verifyClean)
	res, err := schemaflux.Verify(operationClient(model), "the text")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != schemaflux.VerdictPass || res.Subject != "the text" || res.Summary != "s" || res.ModelConfidence != 0.9 || res.ModelTrustScore != 0.85 {
		t.Errorf("got %+v", res)
	}
	if res.Issues != nil || !reflect.DeepEqual(res.Evidence, []string{"e1"}) {
		t.Errorf("issues %+v evidence %v", res.Issues, res.Evidence)
	}
	wantClaims := []schemaflux.ClaimVerification{{Claim: "c1", Verdict: "verified", ModelConfidence: 0.9, Evidence: []string{"e1"}, Reasoning: "r1"}}
	if !reflect.DeepEqual(res.Claims, wantClaims) {
		t.Errorf("claims = %+v", res.Claims)
	}
	req := model.Last()
	wantText(t, req, `Verify this content:`)
	wantText(t, req, `"the text"`)
	if req.Schema == nil || req.SchemaName != "verification" {
		t.Errorf("schema %v named %q: the request has to declare one", req.Schema, req.SchemaName)
	}
}

func TestVerifyReturnsEveryFindingAsAnIssueWithTheCategoryAndSeverityOfItsKind(t *testing.T) {
	t.Parallel()
	body := verifyReport(
		"["+verifyVerifiedClaim+","+verifyFalseClaim+"]",
		`[{"type":"circular","description":"loop","location":"p2","severity":"major"}]`,
		`[{"type":"contradiction","description":"clash","conflicting_items":["a","b"],"suggestion":"pick"}]`)
	res, err := schemaflux.Verify(operationClient(answeringWith(body)), "the text")
	if err != nil {
		t.Fatal(err)
	}
	want := []schemaflux.JudgmentIssue{
		{Subject: "c2", Category: "fact:false", Severity: "error", Message: "r2", Suggestion: "fix", Evidence: []string{"e2"}, ModelConfidence: 0.8},
		{Subject: "p2", Category: "logic:circular", Severity: "error", Message: "loop"},
		{Category: "consistency:contradiction", Severity: "warning", Message: "clash", Suggestion: "pick", Evidence: []string{"a", "b"}},
	}
	if !reflect.DeepEqual(res.Issues, want) {
		t.Errorf("issues = %+v\n want %+v", res.Issues, want)
	}
	if len(res.Claims) != 2 || !reflect.DeepEqual(res.Evidence, []string{"e1", "e2"}) {
		t.Errorf("claims %d evidence %v: every claim is in Claims and its evidence in Evidence", len(res.Claims), res.Evidence)
	}
}

func TestVerifyWorksTheVerdictOutAsTheWorstOfTheChecksThatRan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want schemaflux.Verdict
	}{
		{"every claim verified", verifyReport("["+claimOf("verified", "0.9")+"]", "[]", "[]"), schemaflux.VerdictPass},
		{"every claim false", verifyReport("["+claimOf("false", "0.9")+"]", "[]", "[]"), schemaflux.VerdictFail},
		{"one false and one verified", verifyReport("["+claimOf("false", "0.9")+","+claimOf("verified", "0.9")+"]", "[]", "[]"), schemaflux.VerdictMixed},
		{"a partly true claim", verifyReport("["+claimOf("partially_true", "0.9")+"]", "[]", "[]"), schemaflux.VerdictMixed},
		{"a misleading claim", verifyReport("["+claimOf("misleading", "0.9")+"]", "[]", "[]"), schemaflux.VerdictMixed},
		{"a claim that cannot be checked", verifyReport("["+claimOf("unverifiable", "0.9")+"]", "[]", "[]"), schemaflux.VerdictUnknown},
		{"no claims at all", verifyReport("[]", "[]", "[]"), schemaflux.VerdictUnknown},
		{"a critical flaw in the reasoning", verifyReport("["+claimOf("verified", "0.9")+"]", `[{"type":"other","description":"d","severity":"critical"}]`, "[]"), schemaflux.VerdictFail},
		{"a major flaw in the reasoning", verifyReport("["+claimOf("verified", "0.9")+"]", `[{"type":"other","description":"d","severity":"major"}]`, "[]"), schemaflux.VerdictMixed},
		{"a minor flaw the conclusion survives", verifyReport("["+claimOf("verified", "0.9")+"]", `[{"type":"other","description":"d","severity":"minor"}]`, "[]"), schemaflux.VerdictPass},
		{"a minor contradiction", verifyReport("["+claimOf("verified", "0.9")+"]", `[{"type":"contradiction","description":"d","severity":"minor"}]`, "[]"), schemaflux.VerdictMixed},
		{"an inconsistency", verifyReport("["+claimOf("verified", "0.9")+"]", "[]", `[{"type":"ambiguity","description":"d","conflicting_items":["x"]}]`), schemaflux.VerdictMixed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Verify(operationClient(answeringWith(tc.body)), "t")
			if err != nil || res.Verdict != tc.want {
				t.Errorf("verdict = %q, %v, want %q", res.Verdict, err, tc.want)
			}
		})
	}
}

func TestVerifyLowersAVerifiedClaimTheConfidenceFloorDoesNotHold(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		opts      schemaflux.VerifyOptions
		confident string
		lowered   bool
	}{
		{"the default floor is 0.7", schemaflux.VerifyOptions{}, "0.5", true},
		{"a claim at the floor stands", schemaflux.VerifyOptions{}, "0.7", false},
		{"a lower floor lets it stand", schemaflux.VerifyOptions{MinConfidence: 0.4}, "0.5", false},
		{"a negative floor is no floor", schemaflux.VerifyOptions{MinConfidence: -1}, "0.01", false},
		{"a floor of one holds anything less than certain", schemaflux.VerifyOptions{MinConfidence: 1}, "0.99", true},
		{"a higher floor holds it", schemaflux.VerifyOptions{MinConfidence: 0.95}, "0.9", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Verify(operationClient(answeringWith(verifyReport("["+claimOf("verified", tc.confident)+"]", "[]", "[]"))), "t", tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			claim := res.Claims[0]
			if tc.lowered {
				if claim.Verdict != "unverifiable" || claim.ModelVerdict != "verified" || claim.Note == "" || res.Verdict != schemaflux.VerdictUnknown || len(res.Issues) != 1 {
					t.Errorf("claim = %+v verdict %q issues %d", claim, res.Verdict, len(res.Issues))
				}
			} else if claim.Verdict != "verified" || claim.ModelVerdict != "" || claim.Note != "" || res.Verdict != schemaflux.VerdictPass {
				t.Errorf("claim = %+v verdict %q", claim, res.Verdict)
			}
		})
	}
}

func TestVerifyDoesNotGiveACleanVerdictToAReviewThatWasUnsureOfItself(t *testing.T) {
	t.Parallel()
	body := `{"summary":"s","overall_confidence":0.4,"trust_score":0.85,"claims":[` + claimOf("verified", "0.9") + `],"logic_issues":[],"consistency_issues":[]}`
	res, err := schemaflux.Verify(operationClient(answeringWith(body)), "t")
	if err != nil || res.Verdict != schemaflux.VerdictUnknown {
		t.Errorf("verdict = %q, %v", res.Verdict, err)
	}
	res, err = schemaflux.Verify(operationClient(answeringWith(body)), "t", schemaflux.VerifyOptions{MinConfidence: -1})
	if err != nil || res.Verdict != schemaflux.VerdictPass {
		t.Errorf("with no floor: verdict = %q, %v", res.Verdict, err)
	}
}

func TestVerifyLowersAVerifiedClaimWithNoEvidenceWhenEvidenceWasAskedFor(t *testing.T) {
	t.Parallel()
	bare := `{"claim":"c","verdict":"verified","confidence":0.9,"corrections":"","evidence":[],"reasoning":"r"}`
	res, err := schemaflux.Verify(operationClient(answeringWith(verifyReport("["+bare+"]", "[]", "[]"))), "t")
	if err != nil || res.Claims[0].Verdict != "unverifiable" || !strings.Contains(res.Claims[0].Note, "no evidence") {
		t.Errorf("got %+v, %v", res.Claims, err)
	}
	notAsked := `{"claim":"c","verdict":"verified","confidence":0.9,"corrections":"","reasoning":"r"}`
	res, err = schemaflux.Verify(operationClient(answeringWith(verifyReport("["+notAsked+"]", "[]", "[]"))), "t", schemaflux.VerifyOptions{OmitEvidence: true})
	if err != nil || res.Claims[0].Verdict != "verified" || res.Verdict != schemaflux.VerdictPass {
		t.Errorf("with OmitEvidence: got %+v, %v", res.Claims, err)
	}
}

func TestVerifyHoldsClaimsToTheSourcesItWasGiven(t *testing.T) {
	t.Parallel()
	sources := schemaflux.VerifyOptions{Sources: []any{"the filing", map[string]string{"k": "v"}}}
	withSources := func(sourcesList string) string {
		claim := `{"claim":"c","verdict":"verified","confidence":0.9,"corrections":"","evidence":["e"],"reasoning":"r","sources":` + sourcesList + `}`
		return verifyReport("["+claim+"]", "[]", "[]")
	}
	t.Run("the sources are numbered in the message", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(withSources("[0]"))
		if _, err := schemaflux.Verify(operationClient(model), "t", sources); err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "[Source 0]")
		wantText(t, model.Last(), `"the filing"`)
		wantText(t, model.Last(), "[Source 1]")
	})
	t.Run("a claim that cites a source stands", func(t *testing.T) {
		t.Parallel()
		res, err := schemaflux.Verify(operationClient(answeringWith(withSources("[1]"))), "t", sources)
		if err != nil || res.Verdict != schemaflux.VerdictPass || !reflect.DeepEqual(res.Claims[0].Sources, []int{1}) {
			t.Errorf("got %+v, %v", res, err)
		}
	})
	t.Run("a verified claim that cites none is lowered", func(t *testing.T) {
		t.Parallel()
		res, err := schemaflux.Verify(operationClient(answeringWith(withSources("[]"))), "t", sources)
		if err != nil || res.Claims[0].Verdict != "unverifiable" || !strings.Contains(res.Claims[0].Note, "none of the sources") {
			t.Errorf("got %+v, %v", res.Claims, err)
		}
	})
	t.Run("a claim that cites a source that was not given is refused", func(t *testing.T) {
		t.Parallel()
		res, err := schemaflux.Verify(operationClient(answeringWith(withSources("[7]"))), "t", sources)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "verify")
		if res.Verdict != "" || res.Claims != nil {
			t.Errorf("a refused answer left %+v beside the error", res)
		}
	})
}

func TestVerifyWithNoOptionsRunsEveryCheckAtTheSmartTierInStrictMode(t *testing.T) {
	t.Parallel()
	model := answeringWith(verifyClean)
	if _, err := schemaflux.Verify(operationClient(model), "t"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, `"claims": each factual claim`)
	wantText(t, req, `"logic_issues": each flaw`)
	wantText(t, req, `"consistency_issues": each place`)
	wantText(t, req, `"evidence": the facts`)
	wantText(t, req, `"reasoning": why you reached the verdict.`)
	wantText(t, req, "Use a moderate standard")
	wantNoText(t, req, "Domain")
	if math.Abs(req.Temperature-0.1) > 1e-6 || req.MaxTokens != 4000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestVerifyEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.VerifyOptions
		body  string
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Strictness strict", schemaflux.VerifyOptions{Strictness: "strict"}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Be strict: call a claim verified only on strong evidence.")
		}},
		{"Strictness lenient", schemaflux.VerifyOptions{Strictness: "lenient"}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Be lenient:")
		}},
		{"Domain", schemaflux.VerifyOptions{Domain: "oncology"}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Domain: "oncology"`)
		}},
		{"TrustedSources", schemaflux.VerifyOptions{TrustedSources: []string{"the registry"}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Trusted sources: ["the registry"]`)
		}},
		{"SkipFacts", schemaflux.VerifyOptions{SkipFacts: true}, verifyReport("null", "[]", "[]"), func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"claims": each factual claim`)
			wantText(t, req, `"logic_issues": each flaw`)
		}},
		{"SkipLogic", schemaflux.VerifyOptions{SkipLogic: true}, verifyReport("["+verifyVerifiedClaim+"]", "null", "[]"), func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"logic_issues": each flaw`)
		}},
		{"SkipConsistency", schemaflux.VerifyOptions{SkipConsistency: true}, verifyReport("["+verifyVerifiedClaim+"]", "[]", "null"), func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"consistency_issues": each place`)
		}},
		{"OmitEvidence", schemaflux.VerifyOptions{OmitEvidence: true}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"evidence": the facts`)
		}},
		{"OmitReasoning", schemaflux.VerifyOptions{OmitReasoning: true}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"reasoning": why you reached the verdict.`)
		}},
		{"Steering", schemaflux.VerifyOptions{Options: schemaflux.Options{Steering: "be exact"}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "be exact")
		}},
		{"Model", schemaflux.VerifyOptions{Options: schemaflux.Options{Model: "pinned-model"}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"TierFast", schemaflux.VerifyOptions{Options: schemaflux.Options{Tier: schemaflux.TierFast}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 2000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"ModeCreative", schemaflux.VerifyOptions{Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"MaxOutputTokens", schemaflux.VerifyOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.VerifyOptions{Options: schemaflux.Options{WebSearch: true}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.VerifyOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, verifyClean, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.body)
			if _, err := schemaflux.Verify(operationClient(model), "t", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestVerifyRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.VerifyOptions{
		"every check turned off":       {SkipFacts: true, SkipLogic: true, SkipConsistency: true},
		"a strictness that is not one": {Strictness: "severe"},
		"a floor above one":            {MinConfidence: 1.01},
		"a floor that is no number":    {MinConfidence: math.NaN()},
		"a floor of infinity":          {MinConfidence: math.Inf(1)},
		"a floor of negative infinity": {MinConfidence: math.Inf(-1)},
		"a negative output ceiling":    {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":       {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":       {Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(verifyClean)
			_, err := schemaflux.Verify(operationClient(model), "t", opts)
			wantError(t, err, schemaflux.ErrConfiguration, "verify")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestVerifyTurnsOffTwoChecksAndRunsTheThird(t *testing.T) {
	t.Parallel()
	model := answeringWith(verifyReport("["+verifyVerifiedClaim+"]", "null", "null"))
	res, err := schemaflux.Verify(operationClient(model), "t", schemaflux.VerifyOptions{SkipLogic: true, SkipConsistency: true})
	if err != nil || res.Verdict != schemaflux.VerdictPass {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestVerifyNamesTheFieldOfABadOptionAndNotItsValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		opts schemaflux.VerifyOptions
		want string
	}{
		{schemaflux.VerifyOptions{Strictness: "CANARY-strictness"}, "Strictness is not one of strict, moderate and lenient"},
		{schemaflux.VerifyOptions{MinConfidence: 2}, "MinConfidence must be a number up to 1"},
		{schemaflux.VerifyOptions{SkipFacts: true, SkipLogic: true, SkipConsistency: true}, "SkipFacts, SkipLogic and SkipConsistency are all set, so there is nothing to verify"},
	}
	for _, tc := range cases {
		_, err := schemaflux.Verify(operationClient(answeringWith(verifyClean)), "t", tc.opts)
		e := wantError(t, err, schemaflux.ErrConfiguration, "verify")
		if e.Message != tc.want {
			t.Errorf("message = %q, want %q", e.Message, tc.want)
		}
		for _, text := range chainText(err) {
			if strings.Contains(text, "CANARY") {
				t.Errorf("the value reached %q", text)
			}
		}
	}
}

func TestVerifyRefusesAnAnswerThatDoesNotReportOnACheckThatIsOn(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no claims":                            `{"summary":"s","overall_confidence":0.9,"trust_score":0.8,"logic_issues":[],"consistency_issues":[]}`,
		"a null claims list":                   verifyReport("null", "[]", "[]"),
		"no logic issues":                      `{"summary":"s","overall_confidence":0.9,"trust_score":0.8,"claims":[],"consistency_issues":[]}`,
		"no consistency issues":                `{"summary":"s","overall_confidence":0.9,"trust_score":0.8,"claims":[],"logic_issues":[]}`,
		"no summary":                           `{"overall_confidence":0.9,"trust_score":0.8,"claims":[],"logic_issues":[],"consistency_issues":[]}`,
		"no trust score":                       `{"summary":"s","overall_confidence":0.9,"claims":[],"logic_issues":[],"consistency_issues":[]}`,
		"a trust score off the scale":          `{"summary":"s","overall_confidence":0.9,"trust_score":1.4,"claims":[],"logic_issues":[],"consistency_issues":[]}`,
		"a verdict that is not one":            verifyReport(`[{"claim":"c","verdict":"true","confidence":0.9,"evidence":["e"],"reasoning":"r"}]`, "[]", "[]"),
		"a claim with no text":                 verifyReport(`[{"claim":" ","verdict":"verified","confidence":0.9,"evidence":["e"],"reasoning":"r"}]`, "[]", "[]"),
		"a claim with no confidence":           verifyReport(`[{"claim":"c","verdict":"verified","evidence":["e"],"reasoning":"r"}]`, "[]", "[]"),
		"a claim with no evidence list":        verifyReport(`[{"claim":"c","verdict":"verified","confidence":0.9,"reasoning":"r"}]`, "[]", "[]"),
		"a claim with no reasoning":            verifyReport(`[{"claim":"c","verdict":"verified","confidence":0.9,"evidence":["e"]}]`, "[]", "[]"),
		"a logic issue of an unknown type":     verifyReport("[]", `[{"type":"vague","description":"d","severity":"minor"}]`, "[]"),
		"a logic issue of an unknown severity": verifyReport("[]", `[{"type":"other","description":"d","severity":"huge"}]`, "[]"),
		"a consistency issue with no items":    verifyReport("[]", "[]", `[{"type":"ambiguity","description":"d"}]`),
		"text":                                 `The text is accurate.`,
		"a bare list":                          `[]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Verify(operationClient(answeringWith(body)), "t")
			wantError(t, err, schemaflux.ErrInvalidAnswer, "verify")
			if res.Verdict != "" || res.Subject != "" || res.Claims != nil || res.ModelTrustScore != 0 {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestVerifyWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Verify(nilContext, "t")
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "verify"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestVerifyMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(verifyClean)
		_, err := schemaflux.Verify(operationClient(model), "t",
			schemaflux.VerifyOptions{Strictness: "strict", Domain: "first", Options: schemaflux.Options{Model: "first-model"}},
			schemaflux.VerifyOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.VerifyOptions{Strictness: "lenient"})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Be lenient:")
		wantText(t, req, `Domain: "first"`)
		wantText(t, req, "later")
		if req.Model != "first-model" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(verifyReport("["+verifyVerifiedClaim+"]", "null", "[]"))
		_, err := schemaflux.Verify(operationClient(model), "t", schemaflux.VerifyOptions{SkipLogic: true}, schemaflux.VerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), `"logic_issues": each flaw`)
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Verify(operationClient(answeringWith(verifyClean)), "t", schemaflux.VerifyOptions{}, schemaflux.VerifyOptions{MinConfidence: 5})
		wantError(t, err, schemaflux.ErrConfiguration, "verify")
	})
}

func TestVerifyReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  *schemaflux.Error
	}{
		{"unavailable", &schemaflux.Error{Kind: schemaflux.ErrUnavailable}},
		{"rate limited", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429}},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401}},
		{"timed out", &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
		{"refused", &schemaflux.Error{Kind: schemaflux.ErrRefused}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(tc.err)
			res, err := schemaflux.Verify(operationClient(&model), "t")
			wantError(t, err, tc.err.Kind, "verify")
			if res.Verdict != "" || res.Subject != "" {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

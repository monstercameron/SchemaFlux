package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LX-028, after review round 8. When this library reports a verdict other than
// the one the model gave a claim, three things have to stay true.
//
// The model's own words stay the model's. Reasoning was replaced with the
// review's summary, which the model wrote without knowing the rule that
// lowered the verdict: a review below the confidence floor came back
// "unverifiable" with the reasoning "All claims are accurate".
//
// What the model said is still there to read, in ModelVerdict, and why it is
// not what was reported is in Note, in this library's words.
//
// And a claim nobody could check is never turned into a statement about truth.
// An unverifiable claim in content that contradicts itself was reported as
// "partially_true", which says part of it was found true.
func TestVerifyClaimSaysWhatItLoweredAndWhy(t *testing.T) {
	const reasoning = "the ledger shows it"
	const summary = "All claims are accurate"
	claim := func(verdict, confidence string) string {
		return `{"claim":"c","verdict":"` + verdict + `","confidence":` + confidence + `,"evidence":["e"],"reasoning":"` + reasoning + `","corrections":null}`
	}
	answer := func(overall, claims, logic, consistency string) string {
		return `{"summary":"` + summary + `","overall_confidence":` + overall + `,"trust_score":0.5,"claims":[` + claims + `],"logic_issues":` + logic + `,"consistency_issues":` + consistency + `}`
	}
	const contradiction = `[{"type":"contradiction","description":"d","conflicting_items":["a","b"]}]`
	const twoContradictions = `[{"type":"contradiction","description":"d","conflicting_items":["a","b"]},{"type":"inconsistency","description":"d","conflicting_items":["c","d"]}]`
	const critical = `[{"type":"non_sequitur","description":"d","severity":"critical"}]`
	const major = `[{"type":"non_sequitur","description":"d","severity":"major"}]`
	const minor = `[{"type":"other","description":"d","severity":"minor"}]`

	cases := []struct {
		name         string
		response     string
		floor        float64
		verdict      string
		modelVerdict string
		note         []string // what the note has to say; none means no note
	}{
		{"verified, nothing else found", answer("0.9", claim("verified", "0.9"), "[]", "[]"), 0, "verified", "", nil},
		{"verified and a minor flaw", answer("0.9", claim("verified", "0.9"), minor, "[]"), 0, "verified", "", nil},
		{"verified in content that contradicts itself", answer("0.9", claim("verified", "0.9"), "[]", contradiction), 0,
			"partially_true", "verified", []string{"partially_true", "verified", "mixed", "1 consistency issue"}},
		{"verified and two consistency issues", answer("0.9", claim("verified", "0.9"), "[]", twoContradictions), 0,
			"partially_true", "verified", []string{"2 consistency issues"}},
		{"verified behind a critical flaw", answer("0.9", claim("verified", "0.9"), critical, "[]"), 0,
			"false", "verified", []string{"false", "verified", "1 reasoning issue"}},
		{"verified behind a major flaw", answer("0.9", claim("verified", "0.9"), major, "[]"), 0,
			"partially_true", "verified", []string{"mixed", "1 reasoning issue"}},
		{"verified with both kinds of issue", answer("0.9", claim("verified", "0.9"), major, contradiction), 0,
			"partially_true", "verified", []string{"1 reasoning issue", "1 consistency issue"}},
		{"verified from a review below the floor", answer("0.3", claim("verified", "0.9"), "[]", "[]"), 0.7,
			"unverifiable", "verified", []string{"unverifiable", "verified", "below the confidence floor"}},
		{"verified, the claim itself below the floor", answer("0.9", claim("verified", "0.3"), "[]", "[]"), 0.7,
			"unverifiable", "verified", []string{"its confidence is below the floor"}},
		{"partly true behind a critical flaw", answer("0.9", claim("partially_true", "0.9"), critical, "[]"), 0,
			"false", "partially_true", []string{"false", "partially_true"}},
		{"misleading behind a critical flaw", answer("0.9", claim("misleading", "0.9"), critical, "[]"), 0,
			"false", "misleading", []string{"false", "misleading"}},
		// What nobody could check stays that.
		{"unverifiable in content that contradicts itself", answer("0.9", claim("unverifiable", "0.9"), "[]", contradiction), 0, "unverifiable", "", nil},
		{"unverifiable behind a critical flaw", answer("0.9", claim("unverifiable", "0.9"), critical, "[]"), 0, "unverifiable", "", nil},
		{"unverifiable behind a major flaw", answer("0.9", claim("unverifiable", "0.9"), major, "[]"), 0, "unverifiable", "", nil},
		// What is already no better than the review is left as the model gave it.
		{"false in content that contradicts itself", answer("0.9", claim("false", "0.9"), "[]", contradiction), 0, "false", "", nil},
		{"misleading in content that contradicts itself", answer("0.9", claim("misleading", "0.9"), "[]", contradiction), 0, "misleading", "", nil},
		{"two verified claims and a contradiction", answer("0.9", claim("verified", "0.9")+","+claim("verified", "0.8"), "[]", contradiction), 0,
			"partially_true", "verified", []string{"1 consistency issue"}},
		{"a verified claim beside a false one", answer("0.9", claim("verified", "0.9")+","+claim("false", "0.8"), "[]", "[]"), 0, "false", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previousCaller, previousProvider := currentHooks()
			setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) { return tc.response, nil })
			t.Cleanup(func() {
				setLLMCaller(previousCaller)
				SetDefaultProvider(previousProvider)
			})

			result, err := VerifyClaim("Sales rose, so sales fell.", NewVerifyOptions().WithMinConfidence(tc.floor))
			if err != nil {
				t.Fatalf("VerifyClaim: %v", err)
			}
			if result.Verdict != tc.verdict {
				t.Errorf("Verdict = %q, want %q", result.Verdict, tc.verdict)
			}
			if result.ModelVerdict != tc.modelVerdict {
				t.Errorf("ModelVerdict = %q, want %q", result.ModelVerdict, tc.modelVerdict)
			}
			if result.Reasoning != reasoning {
				t.Errorf("Reasoning = %q, want the model's own for the claim, %q", result.Reasoning, reasoning)
			}
			if strings.Contains(result.Note, summary) || strings.Contains(result.Reasoning, summary) {
				t.Errorf("the review's summary was passed off as the claim's: note %q, reasoning %q", result.Note, result.Reasoning)
			}
			if len(tc.note) == 0 && result.Note != "" {
				t.Errorf("Note = %q on a verdict nothing changed", result.Note)
			}
			for _, want := range tc.note {
				if !strings.Contains(result.Note, want) {
					t.Errorf("Note = %q, want it to say %q", result.Note, want)
				}
			}
			// The two fields go together: a verdict that was changed says what
			// it was and why, and one that was not says neither.
			if (result.ModelVerdict == "") != (result.Note == "") {
				t.Errorf("ModelVerdict %q and Note %q do not go together", result.ModelVerdict, result.Note)
			}
		})
	}
}

// The same in Verify's own list of claims: a claim it does not let stand as
// verified says what the model called it and which rule took the word back.
// The three counts are of claims each rule caught, so a claim that fails two
// rules is in two counts. They were first-match, and a claim below the floor
// with no evidence was reported as zero claims without evidence.
func TestVerifyRelabelsSayWhatAndWhyAndAreEachCounted(t *testing.T) {
	claim := func(confidence, evidence, sources string) string {
		return `{"claim":"c","verdict":"verified","confidence":` + confidence + `,"evidence":` + evidence + `,"reasoning":"r","sources":` + sources + `,"corrections":null}`
	}
	answer := func(claims string) string {
		return `{"summary":"s","overall_confidence":0.9,"trust_score":0.5,"claims":[` + claims + `],"logic_issues":[],"consistency_issues":[]}`
	}
	withSources := NewVerifyOptions().WithMinConfidence(0.7).WithSources([]any{"the annual report"})
	plain := NewVerifyOptions().WithMinConfidence(0.7)

	cases := []struct {
		name                          string
		response                      string
		opts                          VerifyOptions
		verdict, modelVerdict         string
		floor, unevidenced, unsourced int
		note                          []string
	}{
		{"nothing wrong", answer(claim("0.9", `["e"]`, `[]`)), plain, "verified", "", 0, 0, 0, nil},
		{"below the floor", answer(claim("0.3", `["e"]`, `[]`)), plain, "unverifiable", "verified", 1, 0, 0,
			[]string{"its confidence is below the floor"}},
		{"no evidence", answer(claim("0.9", `[]`, `[]`)), plain, "unverifiable", "verified", 0, 1, 0,
			[]string{"no evidence"}},
		{"below the floor and no evidence", answer(claim("0.3", `[]`, `[]`)), plain, "unverifiable", "verified", 1, 1, 0,
			[]string{"its confidence is below the floor", "no evidence"}},
		{"cites no source", answer(claim("0.9", `["e"]`, `[]`)), withSources, "unverifiable", "verified", 0, 0, 1,
			[]string{"none of the sources"}},
		{"cites a source", answer(claim("0.9", `["e"]`, `[0]`)), withSources, "verified", "", 0, 0, 0, nil},
		{"no evidence and no source", answer(claim("0.9", `[]`, `[]`)), withSources, "unverifiable", "verified", 0, 1, 1,
			[]string{"no evidence", "none of the sources"}},
		{"all three", answer(claim("0.3", `[]`, `[]`)), withSources, "unverifiable", "verified", 1, 1, 1,
			[]string{"its confidence is below the floor", "no evidence", "none of the sources"}},
		{"below the floor with a source", answer(claim("0.3", `["e"]`, `[0]`)), withSources, "unverifiable", "verified", 1, 0, 0,
			[]string{"its confidence is below the floor"}},
		{"blank evidence", answer(claim("0.9", `["  "]`, `[]`)), plain, "unverifiable", "verified", 0, 1, 0,
			[]string{"no evidence"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := verifyWith(t, "content", tc.response, tc.opts)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			got := result.Claims[0]
			if got.Verdict != tc.verdict || got.ModelVerdict != tc.modelVerdict {
				t.Errorf("Verdict = %q, ModelVerdict = %q; want %q and %q", got.Verdict, got.ModelVerdict, tc.verdict, tc.modelVerdict)
			}
			if got.Reasoning != "r" {
				t.Errorf("Reasoning = %q, want the model's own", got.Reasoning)
			}
			if len(tc.note) == 0 && got.Note != "" {
				t.Errorf("Note = %q on a verdict nothing changed", got.Note)
			}
			for _, want := range tc.note {
				if !strings.Contains(got.Note, want) {
					t.Errorf("Note = %q, want it to say %q", got.Note, want)
				}
			}
			counts := []any{result.Metadata["claims_below_min_confidence"], result.Metadata["claims_without_evidence"], result.Metadata["claims_without_a_source"]}
			if counts[0] != tc.floor || counts[1] != tc.unevidenced || counts[2] != tc.unsourced {
				t.Errorf("counts (floor, evidence, source) = %v, want %d, %d, %d", counts, tc.floor, tc.unevidenced, tc.unsourced)
			}
		})
	}
}

// A model's answer cannot fill in the two fields this library writes.
func TestVerifyDoesNotTakeTheLibrarysFieldsFromTheModel(t *testing.T) {
	response := `{"summary":"s","overall_confidence":0.9,"trust_score":0.5,"claims":[{"claim":"c","verdict":"verified","confidence":0.9,"evidence":["e"],"reasoning":"r","corrections":null,"model_verdict":"false","note":"trust me"}],"logic_issues":[],"consistency_issues":[]}`
	result, err, _ := verifyWith(t, "content", response, NewVerifyOptions().WithMinConfidence(0))
	if err != nil {
		// Refusing the answer outright is as good.
		return
	}
	if got := result.Claims[0]; got.ModelVerdict != "" || got.Note != "" {
		t.Errorf("ModelVerdict = %q, Note = %q: taken from the model's answer", got.ModelVerdict, got.Note)
	}
}

// Review round 9. Two things about what VerifyClaim returns.
//
// A stricter confidence floor must not give a better-looking verdict. A claim
// the model verified at low confidence, behind a critical flaw, came back
// "false" with no floor and "unverifiable" with one: the floor had relabelled
// it first, and what nobody could check is left alone by the cap. That
// exemption is for a claim the model itself could not check.
//
// And when the model splits the claim in several, the confidence and the
// evidence returned are those of the claims that gave the verdict. They were
// the lowest confidence and all of the evidence, so a claim found false at
// 0.95 beside one verified at 0.2 came back "false" at 0.2, carrying the
// verified claim's evidence.
func TestVerifyClaimReturnsTheVerdictsOwnConfidenceAndEvidence(t *testing.T) {
	claim := func(verdict, confidence, evidence string) string {
		return `{"claim":"c","verdict":"` + verdict + `","confidence":` + confidence + `,"evidence":["` + evidence + `"],"reasoning":"because ` + evidence + `","corrections":null}`
	}
	answer := func(claims, logic string) string {
		return `{"summary":"s","overall_confidence":0.9,"trust_score":0.5,"claims":[` + claims + `],"logic_issues":` + logic + `,"consistency_issues":[]}`
	}
	const critical = `[{"type":"non_sequitur","description":"d","severity":"critical"}]`
	const major = `[{"type":"non_sequitur","description":"d","severity":"major"}]`

	cases := []struct {
		name         string
		response     string
		floor        float64
		verdict      string
		modelVerdict string
		confidence   float64
		evidence     []string
		reasoning    string
		note         []string
	}{
		{"one verified claim", answer(claim("verified", "0.9", "A"), "[]"), 0, "verified", "", 0.9, []string{"A"}, "because A", nil},
		{"false at 0.95 beside verified at 0.2", answer(claim("false", "0.95", "A")+","+claim("verified", "0.2", "B"), "[]"), 0,
			"false", "", 0.95, []string{"A"}, "because A", nil},
		{"verified at 0.2 beside false at 0.95, in the other order", answer(claim("verified", "0.2", "B")+","+claim("false", "0.95", "A"), "[]"), 0,
			"false", "", 0.95, []string{"A"}, "because A", nil},
		{"two verified claims", answer(claim("verified", "0.9", "A")+","+claim("verified", "0.6", "B"), "[]"), 0,
			"verified", "", 0.6, []string{"A", "B"}, "because B", nil},
		// Review round 12: a refutation is as strong as its strongest claim.
		{"two false claims", answer(claim("false", "0.9", "A")+","+claim("false", "0.5", "B"), "[]"), 0,
			"false", "", 0.9, []string{"A", "B"}, "because A", nil},
		{"misleading beside partly true beside verified", answer(claim("verified", "0.9", "A")+","+claim("partially_true", "0.8", "B")+","+claim("misleading", "0.7", "C"), "[]"), 0,
			"misleading", "", 0.7, []string{"C"}, "because C", nil},
		{"unverifiable beside verified", answer(claim("verified", "0.9", "A")+","+claim("unverifiable", "0.4", "B"), "[]"), 0,
			"unverifiable", "", 0.4, []string{"B"}, "because B", nil},

		// The floor and the cap together.
		{"verified at low confidence, a critical flaw, no floor", answer(claim("verified", "0.3", "A"), critical), 0,
			"false", "verified", 0.3, []string{"A"}, "because A", []string{"reported as false, not verified"}},
		{"verified at low confidence, a critical flaw, a floor", answer(claim("verified", "0.3", "A"), critical), 0.7,
			"false", "verified", 0.3, []string{"A"}, "because A", []string{"its confidence is below the floor", "; then reported as false, not unverifiable"}},
		{"verified at low confidence, a major flaw, no floor", answer(claim("verified", "0.3", "A"), major), 0,
			"partially_true", "verified", 0.3, []string{"A"}, "because A", []string{"reported as partially_true, not verified"}},
		{"verified at low confidence, a major flaw, a floor", answer(claim("verified", "0.3", "A"), major), 0.7,
			"partially_true", "verified", 0.3, []string{"A"}, "because A", []string{"its confidence is below the floor", "; then reported as partially_true, not unverifiable"}},
		{"the model's own unverifiable, a critical flaw, a floor", answer(claim("unverifiable", "0.3", "A"), critical), 0.7,
			"unverifiable", "", 0.3, []string{"A"}, "because A", nil},
		{"the model's own unverifiable, a critical flaw, no floor", answer(claim("unverifiable", "0.3", "A"), critical), 0,
			"unverifiable", "", 0.3, []string{"A"}, "because A", nil},

		// Review round 10. Each claim is capped and then the worst is taken,
		// so a claim the model could not check does not shelter the one beside
		// it, and the order the claims come in does not change the verdict.
		{"verified beside the model's own unverifiable, a critical flaw", answer(claim("verified", "0.9", "A")+","+claim("unverifiable", "0.4", "B"), critical), 0,
			"false", "verified", 0.9, []string{"A"}, "because A", []string{"reported as false, not verified"}},
		{"the same two, the other way round", answer(claim("unverifiable", "0.4", "B")+","+claim("verified", "0.9", "A"), critical), 0,
			"false", "verified", 0.9, []string{"A"}, "because A", []string{"reported as false, not verified"}},
		{"verified beside the model's own unverifiable, a major flaw", answer(claim("verified", "0.9", "A")+","+claim("unverifiable", "0.4", "B"), major), 0,
			"partially_true", "verified", 0.9, []string{"A"}, "because A", []string{"reported as partially_true, not verified"}},
		{"unverifiable then a verified claim below the floor, a critical flaw", answer(claim("unverifiable", "0.9", "B")+","+claim("verified", "0.3", "A"), critical), 0.7,
			"false", "verified", 0.3, []string{"A"}, "because A", []string{"; then reported as false, not unverifiable"}},
		{"a verified claim below the floor then unverifiable, a critical flaw", answer(claim("verified", "0.3", "A")+","+claim("unverifiable", "0.9", "B"), critical), 0.7,
			"false", "verified", 0.3, []string{"A"}, "because A", []string{"; then reported as false, not unverifiable"}},
		{"two verified claims, a critical flaw: both are lowered and both are returned", answer(claim("verified", "0.9", "A")+","+claim("verified", "0.6", "B"), critical), 0,
			"false", "verified", 0.6, []string{"A", "B"}, "because B", []string{"reported as false, not verified"}},
		{"only claims the model could not check, a critical flaw", answer(claim("unverifiable", "0.9", "A")+","+claim("unverifiable", "0.4", "B"), critical), 0,
			"unverifiable", "", 0.4, []string{"A", "B"}, "because B", nil},

		// Review round 11. The confidence, the reasoning and the note come
		// from one claim: of the claims that gave the verdict, the one the
		// model was least sure of. They used to be the first such claim's
		// reasoning and note beside the lowest confidence, which is another
		// claim's as often as not. And a claim lowered by the review only
		// shares the verdict of one that has it for a reason of its own, a
		// confidence below the floor included: with a floor of 0.7, a claim
		// verified at 0.95 beside one verified at 0.3 came back with the first
		// one's reasoning, a note about the review, and 0.3.
		{"verified above the floor beside verified below it", answer(claim("verified", "0.95", "A")+","+claim("verified", "0.3", "B"), "[]"), 0.7,
			"unverifiable", "verified", 0.3, []string{"B"}, "because B", []string{"its confidence is below the floor"}},
		{"the same two, the other way round", answer(claim("verified", "0.3", "B")+","+claim("verified", "0.95", "A"), "[]"), 0.7,
			"unverifiable", "verified", 0.3, []string{"B"}, "because B", []string{"its confidence is below the floor"}},
		{"two below the floor: the less sure one speaks", answer(claim("verified", "0.5", "A")+","+claim("verified", "0.3", "B"), "[]"), 0.7,
			"unverifiable", "verified", 0.3, []string{"A", "B"}, "because B", []string{"its confidence is below the floor"}},
		{"the same two, the other way round again", answer(claim("verified", "0.3", "B")+","+claim("verified", "0.5", "A"), "[]"), 0.7,
			"unverifiable", "verified", 0.3, []string{"B", "A"}, "because B", []string{"its confidence is below the floor"}},
		{"three false claims, the first one surest", answer(claim("false", "0.9", "A")+","+claim("false", "0.2", "B")+","+claim("false", "0.6", "C"), "[]"), 0,
			"false", "", 0.9, []string{"A", "B", "C"}, "because A", nil},
		{"two equally sure: the earlier one speaks", answer(claim("false", "0.5", "A")+","+claim("false", "0.5", "B"), "[]"), 0,
			"false", "", 0.5, []string{"A", "B"}, "because A", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previousCaller, previousProvider := currentHooks()
			setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) { return tc.response, nil })
			t.Cleanup(func() {
				setLLMCaller(previousCaller)
				SetDefaultProvider(previousProvider)
			})

			got, err := VerifyClaim("the claim", NewVerifyOptions().WithMinConfidence(tc.floor))
			if err != nil {
				t.Fatalf("VerifyClaim: %v", err)
			}
			if got.Verdict != tc.verdict || got.ModelVerdict != tc.modelVerdict {
				t.Errorf("Verdict = %q, ModelVerdict = %q; want %q and %q", got.Verdict, got.ModelVerdict, tc.verdict, tc.modelVerdict)
			}
			if got.ModelConfidence != tc.confidence {
				t.Errorf("ModelConfidence = %v, want %v", got.ModelConfidence, tc.confidence)
			}
			if len(got.Evidence) != len(tc.evidence) {
				t.Fatalf("Evidence = %v, want %v", got.Evidence, tc.evidence)
			}
			for i, want := range tc.evidence {
				if got.Evidence[i] != want {
					t.Errorf("Evidence = %v, want %v", got.Evidence, tc.evidence)
				}
			}
			if got.Reasoning != tc.reasoning {
				t.Errorf("Reasoning = %q, want %q", got.Reasoning, tc.reasoning)
			}
			for _, want := range tc.note {
				if !strings.Contains(got.Note, want) {
					t.Errorf("Note = %q, want it to say %q", got.Note, want)
				}
			}
			if len(tc.note) == 0 && got.Note != "" {
				t.Errorf("Note = %q on a verdict nothing changed", got.Note)
			}
			if got.Claim != "the claim" {
				t.Errorf("Claim = %q, want the caller's own", got.Claim)
			}
		})
	}
}

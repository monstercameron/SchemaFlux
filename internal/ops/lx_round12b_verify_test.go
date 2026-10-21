package ops

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// scriptedVerifyClaim is one claim as the scripted model reports it. Corrections left
// empty are sent as null, which is what a model with nothing to correct sends.
type scriptedVerifyClaim struct {
	verdict     string
	confidence  string
	evidence    []string
	corrections string
}

func (c scriptedVerifyClaim) json() string {
	evidence, _ := json.Marshal(c.evidence)
	corrections := "null"
	if c.corrections != "" {
		raw, _ := json.Marshal(c.corrections)
		corrections = string(raw)
	}
	reasoning := "because " + strings.Join(c.evidence, "+")
	return `{"claim":"c","verdict":"` + c.verdict + `","confidence":` + c.confidence +
		`,"evidence":` + string(evidence) + `,"reasoning":"` + reasoning + `","corrections":` + corrections + `}`
}

func scriptedVerifyAnswer(logic string, claims ...scriptedVerifyClaim) string {
	parts := make([]string, len(claims))
	for i, c := range claims {
		parts[i] = c.json()
	}
	return `{"summary":"s","overall_confidence":0.9,"trust_score":0.5,"claims":[` + strings.Join(parts, ",") +
		`],"logic_issues":` + logic + `,"consistency_issues":[]}`
}

// Review round 12, VerifyClaim. Two findings.
//
// Only the speaking claim's corrections survived, so two false claims, one at
// 0.9 with a correction and one at 0.5 with none, came back false at 0.5 with
// no correction. The corrections of every claim that gives the verdict are
// kept now, in claim order and without repeats.
//
// And the least sure claim spoke for a refutation too, so a claim found false
// at 0.95 beside another found false at 0.2 came back at 0.2, which under-reads
// a strong refutation. A refutation is as strong as its strongest claim; a
// verification is as strong as its weakest, which is unchanged. The reasoning
// is always the speaking claim's own.
func TestVerifyClaimReadsARefutationByItsStrongestClaim(t *testing.T) {
	const critical = `[{"type":"non_sequitur","description":"d","severity":"critical"}]`
	a := func(verdict, confidence, corrections string) scriptedVerifyClaim {
		return scriptedVerifyClaim{verdict, confidence, []string{"A"}, corrections}
	}
	b := func(verdict, confidence, corrections string) scriptedVerifyClaim {
		return scriptedVerifyClaim{verdict, confidence, []string{"B"}, corrections}
	}
	c := func(verdict, confidence, corrections string) scriptedVerifyClaim {
		return scriptedVerifyClaim{verdict, confidence, []string{"C"}, corrections}
	}

	cases := []struct {
		name        string
		response    string
		floor       float64
		verdict     string
		confidence  float64
		reasoning   string
		evidence    []string
		corrections string
	}{
		{"a correction on the surer false claim is kept", scriptedVerifyAnswer("[]", a("false", "0.9", "fix A"), b("false", "0.5", "")), 0,
			"false", 0.9, "because A", []string{"A", "B"}, "fix A"},
		{"the same two, the other way round", scriptedVerifyAnswer("[]", b("false", "0.5", ""), a("false", "0.9", "fix A")), 0,
			"false", 0.9, "because A", []string{"B", "A"}, "fix A"},
		{"a correction on the less sure false claim is kept too", scriptedVerifyAnswer("[]", a("false", "0.9", ""), b("false", "0.5", "fix B")), 0,
			"false", 0.9, "because A", []string{"A", "B"}, "fix B"},
		{"two corrections are merged in claim order", scriptedVerifyAnswer("[]", a("false", "0.4", "fix A"), b("false", "0.8", "fix B")), 0,
			"false", 0.8, "because B", []string{"A", "B"}, "fix A\nfix B"},
		{"a correction both claims give is kept once", scriptedVerifyAnswer("[]", a("false", "0.4", "fix it"), b("false", "0.8", "  fix it ")), 0,
			"false", 0.8, "because B", []string{"A", "B"}, "fix it"},
		{"false at 0.95 beside false at 0.2", scriptedVerifyAnswer("[]", a("false", "0.95", ""), b("false", "0.2", "")), 0,
			"false", 0.95, "because A", []string{"A", "B"}, ""},
		{"the same two, the other way round", scriptedVerifyAnswer("[]", b("false", "0.2", ""), a("false", "0.95", "")), 0,
			"false", 0.95, "because A", []string{"B", "A"}, ""},
		{"three false claims, two equally sure: the earlier speaks", scriptedVerifyAnswer("[]", a("false", "0.6", ""), b("false", "0.9", ""), c("false", "0.9", "")), 0,
			"false", 0.9, "because B", []string{"A", "B", "C"}, ""},
		{"two misleading claims", scriptedVerifyAnswer("[]", a("misleading", "0.3", ""), b("misleading", "0.7", "")), 0,
			"misleading", 0.7, "because B", []string{"A", "B"}, ""},
		{"two partly true claims", scriptedVerifyAnswer("[]", a("partially_true", "0.8", ""), b("partially_true", "0.1", "")), 0,
			"partially_true", 0.8, "because A", []string{"A", "B"}, ""},
		{"false beside misleading: only the false claim gives, and speaks", scriptedVerifyAnswer("[]", a("misleading", "0.99", "fix A"), b("false", "0.3", "fix B")), 0,
			"false", 0.3, "because B", []string{"B"}, "fix B"},
		{"false beside verified: the verified claim gives nothing", scriptedVerifyAnswer("[]", a("verified", "0.9", "not this"), b("false", "0.6", "fix B")), 0,
			"false", 0.6, "because B", []string{"B"}, "fix B"},
		{"evidence two claims share is kept once", scriptedVerifyAnswer("[]",
			scriptedVerifyClaim{"false", "0.9", []string{"X", "Y"}, ""}, scriptedVerifyClaim{"false", "0.2", []string{"Y", "Z"}, ""}), 0,
			"false", 0.9, "because X+Y", []string{"X", "Y", "Z"}, ""},

		// Verification and "unverifiable" are read as they were: the least sure
		// claim speaks, and what the claims offer is merged.
		{"two verified claims: the weaker speaks", scriptedVerifyAnswer("[]", a("verified", "0.9", ""), b("verified", "0.6", "")), 0,
			"verified", 0.6, "because B", []string{"A", "B"}, ""},
		{"two verified claims with corrections", scriptedVerifyAnswer("[]", a("verified", "0.9", "fix A"), b("verified", "0.6", "fix B")), 0,
			"verified", 0.6, "because B", []string{"A", "B"}, "fix A\nfix B"},
		{"two unverifiable claims: the weaker speaks", scriptedVerifyAnswer("[]", a("unverifiable", "0.9", ""), b("unverifiable", "0.4", "")), 0,
			"unverifiable", 0.4, "because B", []string{"A", "B"}, ""},
		{"two claims under the floor: the weaker speaks", scriptedVerifyAnswer("[]", a("verified", "0.5", ""), b("verified", "0.3", "")), 0.7,
			"unverifiable", 0.3, "because B", []string{"A", "B"}, ""},
		// What only the review lowered is not a refutation by the model. The
		// confidence is the model's in a claim it called verified.
		{"two verified claims lowered by a critical flaw: the weaker speaks", scriptedVerifyAnswer(critical, a("verified", "0.9", ""), b("verified", "0.6", "")), 0,
			"false", 0.6, "because B", []string{"A", "B"}, ""},
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
			if got.Verdict != tc.verdict {
				t.Errorf("Verdict = %q, want %q", got.Verdict, tc.verdict)
			}
			if got.ModelConfidence != tc.confidence {
				t.Errorf("ModelConfidence = %v, want %v", got.ModelConfidence, tc.confidence)
			}
			if got.Reasoning != tc.reasoning {
				t.Errorf("Reasoning = %q, want %q: the confidence and the reasoning are one claim's", got.Reasoning, tc.reasoning)
			}
			if !reflect.DeepEqual(got.Evidence, tc.evidence) {
				t.Errorf("Evidence = %q, want %q", got.Evidence, tc.evidence)
			}
			if got.Corrections != tc.corrections {
				t.Errorf("Corrections = %q, want %q", got.Corrections, tc.corrections)
			}
			if got.Claim != "the claim" {
				t.Errorf("Claim = %q, want the caller's own", got.Claim)
			}
		})
	}
}

func TestClaimSpeaks(t *testing.T) {
	cases := []struct {
		name      string
		candidate float64
		held      float64
		strongest bool
		want      bool
	}{
		{"surer, asked for the surest", 0.9, 0.5, true, true},
		{"less sure, asked for the surest", 0.2, 0.5, true, false},
		{"a tie, asked for the surest", 0.5, 0.5, true, false},
		{"surer, asked for the least sure", 0.9, 0.5, false, false},
		{"less sure, asked for the least sure", 0.2, 0.5, false, true},
		{"a tie, asked for the least sure", 0.5, 0.5, false, false},
		{"zero against something, surest", 0, 0.1, true, false},
		{"zero against something, least sure", 0, 0.1, false, true},
		{"one against something, surest", 1, 0.99, true, true},
		{"one against something, least sure", 1, 0.99, false, false},
		{"zero against zero, surest", 0, 0, true, false},
		{"one against one, least sure", 1, 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimSpeaks(tc.candidate, tc.held, tc.strongest); got != tc.want {
				t.Errorf("claimSpeaks(%v, %v, %v) = %v, want %v", tc.candidate, tc.held, tc.strongest, got, tc.want)
			}
		})
	}
}

func TestAppendDistinct(t *testing.T) {
	cases := []struct {
		name string
		list []string
		more []string
		want []string
	}{
		{"nothing to add to nothing", nil, nil, nil},
		{"one entry onto nothing", nil, []string{"a"}, []string{"a"}},
		{"a blank is not kept", nil, []string{""}, nil},
		{"space is blank", nil, []string{"  \n"}, nil},
		{"a repeat is not kept", []string{"a"}, []string{"a"}, []string{"a"}},
		{"a repeat that differs in space is not kept", []string{"a"}, []string{" a "}, []string{"a"}},
		{"a repeat inside one call is not kept", nil, []string{"a", "a"}, []string{"a"}},
		{"order is the order of arrival", []string{"b"}, []string{"a", "c"}, []string{"b", "a", "c"}},
		{"case counts", []string{"a"}, []string{"A"}, []string{"a", "A"}},
		{"blanks between entries are skipped", nil, []string{"a", "", "b"}, []string{"a", "b"}},
		{"an empty list stays as it is when nothing is added", []string{}, []string{""}, []string{}},
		{"the first spelling is the one kept", nil, []string{"fix it", "  fix it "}, []string{"fix it"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendDistinct(tc.list, tc.more...); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("appendDistinct(%q, %q) = %#v, want %#v", tc.list, tc.more, got, tc.want)
			}
		})
	}
}

package ops

import (
	"strings"
	"testing"
)

// Review round 11, Audit.

// The severity scale offered a level for something that is not a problem: "an
// observation; nothing is wrong". Since a violation of a supplied policy fails
// the audit at any severity, one such entry under a policy failed a record
// that complied with it. Every level of the scale is a problem now, and what
// is not a problem is not reported.
func TestAuditScaleHasNoLevelForWhatIsNotAProblem(t *testing.T) {
	for _, phrase := range []string{"nothing is wrong", "an observation", "observation"} {
		if strings.Contains(auditSystemPrompt, phrase) {
			t.Errorf("the scale still offers %q", phrase)
		}
	}
	for _, band := range []string{"0.0 to 0.2", "0.3 to 0.4", "0.5 to 0.6", "0.7 to 0.8", "0.9 to 1.0"} {
		at := strings.Index(auditSystemPrompt, band)
		if at < 0 {
			t.Errorf("the scale has no band %s", band)
			continue
		}
		line := auditSystemPrompt[at:]
		if end := strings.Index(line, "\n"); end >= 0 {
			line = line[:end]
		}
		if !strings.Contains(line, "problem") {
			t.Errorf("the band %q is not described as a problem", line)
		}
	}
	if !strings.Contains(auditSystemPrompt, "is not a violation") {
		t.Error("the instructions do not say that something which is not a problem is left out of a policy's list")
	}
}

// The constructor asks for the same audit a struct does, and Shallow can be
// added to it. It still set Deep, which Shallow is refused beside, so
// NewAuditOptions could not be made shallow at all.
func TestNewAuditOptionsCanBeMadeShallow(t *testing.T) {
	clean := `{"findings":[]}`
	if NewAuditOptions().Deep {
		t.Error("NewAuditOptions sets Deep, which is deprecated and is refused beside Shallow")
	}

	cases := []struct {
		name    string
		opts    func() AuditOptions
		shallow bool
		refused bool
	}{
		{"the constructor", NewAuditOptions, false, false},
		{"the constructor, made shallow", func() AuditOptions {
			opts := NewAuditOptions()
			opts.Shallow = true
			return opts
		}, true, false},
		{"a struct", func() AuditOptions { return AuditOptions{} }, false, false},
		{"a struct, shallow", func() AuditOptions { return AuditOptions{Shallow: true} }, true, false},
		{"a struct, deep", func() AuditOptions { return AuditOptions{Deep: true} }, false, false},
		{"a struct, both", func() AuditOptions { return AuditOptions{Deep: true, Shallow: true} }, false, true},
		{"the constructor, with both set by hand", func() AuditOptions {
			opts := NewAuditOptions()
			opts.Deep, opts.Shallow = true, true
			return opts
		}, false, true},
		{"the constructor, with a threshold", func() AuditOptions {
			opts := NewAuditOptions()
			opts.Threshold = 0.5
			return opts
		}, false, false},
		{"the constructor, shallow, with a threshold", func() AuditOptions {
			opts := NewAuditOptions()
			opts.Threshold, opts.Shallow = 0.5, true
			return opts
		}, true, false},
		{"the constructor, with categories", func() AuditOptions {
			opts := NewAuditOptions()
			opts.Categories = []string{"quality"}
			return opts
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err, sent := auditWith(t, clean, tc.opts())
			if tc.refused {
				if err == nil || sent.calls != 0 {
					t.Fatalf("err %v after %d calls; want a refusal before any", err, sent.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			if shallow := strings.Contains(sent.user, "top level only"); shallow != tc.shallow {
				t.Errorf("asked for the top level only = %v, want %v:\n%s", shallow, tc.shallow, sent.user)
			}
		})
	}
}

// Review round 11, VerifyClaim. A claim under the caller's confidence floor is
// the reason an answer is unverifiable, and what comes back says so. Beside a
// claim the review then lowered, it came back as that other claim, with a
// note blaming the review.
func TestVerifyClaimDoesNotBlameTheReviewForAClaimBelowTheFloor(t *testing.T) {
	sure := `{"claim":"c","verdict":"verified","confidence":0.95,"evidence":["A"],"reasoning":"because A","corrections":null}`
	unsure := `{"claim":"c","verdict":"verified","confidence":0.3,"evidence":["B"],"reasoning":"because B","corrections":null}`
	for _, order := range [][]string{{sure, unsure}, {unsure, sure}} {
		response := `{"summary":"s","overall_confidence":0.9,"trust_score":0.5,"claims":[` + order[0] + `,` + order[1] + `],"logic_issues":[],"consistency_issues":[]}`
		restore := stubLLM(response)
		got, err := VerifyClaim("the claim", NewVerifyOptions().WithMinConfidence(0.7))
		restore()
		if err != nil {
			t.Fatalf("VerifyClaim: %v", err)
		}
		if got.Reasoning != "because B" || got.ModelConfidence != 0.3 {
			t.Errorf("reasoning %q at %v: the confidence and the reasoning are not one claim's", got.Reasoning, got.ModelConfidence)
		}
		if strings.Contains(got.Note, "the review of the content as a whole") {
			t.Errorf("the note blames the review: %q", got.Note)
		}
		if !strings.Contains(got.Note, "below the floor") {
			t.Errorf("the note does not give the reason: %q", got.Note)
		}
	}
}

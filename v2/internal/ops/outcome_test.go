package ops

import "testing"

func confidenceOf(p float64) *float64 { return &p }

func TestOutcomeNoteDecisionReportsWhoAnswered(t *testing.T) {
	typed := DecisionRecord{AnsweredBy: "jev", Typed: true}
	generated := DecisionRecord{AnsweredBy: "openai"}
	escalated := DecisionRecord{AnsweredBy: "openai", AskedFirst: "jev", Escalated: "below the floor", AskedFirstModelConfidence: confidenceOf(0.4)}
	unavailable := DecisionRecord{AnsweredBy: "openai", AskedFirst: "jev", Escalated: "unavailable"}
	failed := DecisionRecord{Err: errSample}

	cases := []struct {
		name       string
		records    []DecisionRecord
		keepAsked  bool
		wantBy     string
		wantTyped  bool
		wantFirst  string
		wantWhy    string
		wantFirstP *float64
		wantAskedN int
	}{
		{"no decision", nil, true, "", false, "", "", nil, 0},
		{"one typed answer", []DecisionRecord{typed}, true, "jev", true, "", "", nil, 0},
		{"one generated answer", []DecisionRecord{generated}, true, "openai", false, "", "", nil, 0},
		{"one escalated answer", []DecisionRecord{escalated}, true, "openai", false, "jev", "below the floor", confidenceOf(0.4), 0},
		{"one that found the decision provider unavailable", []DecisionRecord{unavailable}, true, "openai", false, "jev", "unavailable", nil, 0},
		{"a failed decision says nothing of who answered", []DecisionRecord{failed}, true, "", false, "", "", nil, 0},
		{"two that agree", []DecisionRecord{typed, typed}, true, "jev", true, "", "", nil, 0},
		{"two escalations that agree", []DecisionRecord{escalated, escalated}, true, "openai", false, "jev", "below the floor", confidenceOf(0.4), 0},
		{"two that disagree say nothing", []DecisionRecord{typed, generated}, true, "", false, "", "", nil, 0},
		{"disagreement stays after a later one that agrees with the last", []DecisionRecord{typed, generated, generated}, true, "", false, "", "", nil, 0},
		{"two escalations of different confidence disagree", []DecisionRecord{escalated, {AnsweredBy: "openai", AskedFirst: "jev", Escalated: "below the floor", AskedFirstModelConfidence: confidenceOf(0.2)}}, true, "", false, "", "", nil, 0},
		{"questions are kept when wanted", []DecisionRecord{{AnsweredBy: "jev", Typed: true, Asked: []AnsweredQuestion{{Question: "a"}, {Question: "b"}}}}, true, "jev", true, "", "", nil, 2},
		{"questions are not kept when not wanted", []DecisionRecord{{AnsweredBy: "jev", Typed: true, Asked: []AnsweredQuestion{{Question: "a"}}}}, false, "jev", true, "", "", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var outcome Outcome
			for _, record := range tc.records {
				outcome.NoteDecision(record, tc.keepAsked)
			}
			if outcome.AnsweredBy != tc.wantBy || outcome.Typed != tc.wantTyped || outcome.AskedFirst != tc.wantFirst || outcome.Escalated != tc.wantWhy {
				t.Errorf("answered by %q typed %v, asked first %q (%q); want %q %v %q (%q)",
					outcome.AnsweredBy, outcome.Typed, outcome.AskedFirst, outcome.Escalated, tc.wantBy, tc.wantTyped, tc.wantFirst, tc.wantWhy)
			}
			if (outcome.AskedFirstModelConfidence == nil) != (tc.wantFirstP == nil) ||
				(tc.wantFirstP != nil && *outcome.AskedFirstModelConfidence != *tc.wantFirstP) {
				t.Errorf("asked-first confidence = %v, want %v", outcome.AskedFirstModelConfidence, tc.wantFirstP)
			}
			if len(outcome.Asked) != tc.wantAskedN {
				t.Errorf("%d questions kept, want %d", len(outcome.Asked), tc.wantAskedN)
			}
		})
	}

	t.Run("the confidence is copied and not shared", func(t *testing.T) {
		source := escalated
		var outcome Outcome
		outcome.NoteDecision(source, false)
		*source.AskedFirstModelConfidence = 0.99
		if *outcome.AskedFirstModelConfidence != 0.4 {
			t.Errorf("the outcome shares the record's number: %v", *outcome.AskedFirstModelConfidence)
		}
	})
	t.Run("a nil outcome is left alone", func(t *testing.T) {
		var outcome *Outcome
		outcome.NoteDecision(typed, true)
	})
}

var errSample = &sampleError{}

type sampleError struct{}

func (*sampleError) Error() string { return "the decision failed" }

package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Every error that leaves a decision has a kind, with the text it has always
// had, and the sentinel its kind stands for.
func TestDecisionErrorsKeepTheirTextAndHaveTheirKind(t *testing.T) {
	ctx := context.Background()
	type triage struct {
		Team string `ask:"Which team?" oneof:"billing,technical"`
	}
	type unreadable struct {
		hidden bool `ask:"Is it hidden?"`
	}
	cases := []struct {
		name     string
		model    string
		run      func() error
		text     string
		kind     types.ErrorKind
		sentinel error
	}{
		{"Is with a blank question", "", func() error { _, err := Is(ctx, "s", " "); return err },
			"assess: configuration: question 1 is blank", types.KindConfiguration, types.ErrConfiguration},
		{"Which with one label", "", func() error { _, err := Which(ctx, "s", "q?", []string{"a"}); return err },
			"assess: configuration: question 1 offers 1 options, and a choice needs two", types.KindConfiguration, types.ErrConfiguration},
		{"Which with a blank label", "", func() error { _, err := Which(ctx, "s", "q?", []string{"a", " "}); return err },
			"assess: configuration: option 2 of question 1 is blank", types.KindConfiguration, types.ErrConfiguration},
		{"Which with a repeated label", "", func() error { _, err := Which(ctx, "s", "q?", []string{"a", "a "}); return err },
			"assess: configuration: option 2 of question 1 repeats an earlier one", types.KindConfiguration, types.ErrConfiguration},
		{"Rate with no room on the scale", "", func() error { _, err := Rate(ctx, "s", "q?", 3, 3); return err },
			"assess: configuration: the scale of question 1 runs from 3 to 3, which leaves nothing to choose", types.KindConfiguration, types.ErrConfiguration},
		{"WhichOrNone with a blank question and no labels", "", func() error { _, _, err := WhichOrNone(ctx, "s", "", nil); return err },
			"assess: configuration: question 1 is blank", types.KindConfiguration, types.ErrConfiguration},
		{"WhichOrNone with the label it adds for itself", "", func() error { _, _, err := WhichOrNone(ctx, "s", "q?", []string{"a", "None of these"}); return err },
			`assess: configuration: option 2 is "none of these", which is the answer WhichOrNone adds for itself`, types.KindConfiguration, types.ErrConfiguration},
		{"Assess into something that is not a struct", "", func() error { _, err := Assess[int](ctx, "s"); return err },
			"assess: configuration: Assess fills a struct, one field for each question", types.KindConfiguration, types.ErrConfiguration},
		{"Assess into a struct with a question on a field that is not exported", "", func() error { _, err := Assess[unreadable](ctx, "s"); return err },
			"assess: configuration: the field hidden asks a question and is not exported, so its answer could not be set", types.KindConfiguration, types.ErrConfiguration},
		{"Is about a subject that says nothing", "", func() error { _, err := Is(ctx, struct{ hidden int }{1}, "q?"); return err },
			"assess: invalid request: the subject is a struct { hidden int }, which has no field JSON can see and would reach the model as an empty object; pass text, or a value with exported fields",
			types.KindInvalidRequest, types.ErrInvalidRequest},
		{"Is about a subject that cannot be written", "", func() error { _, err := Is(ctx, make(chan int), "q?"); return err },
			"assess: invalid request: the subject could not be written as JSON", types.KindInvalidRequest, types.ErrInvalidRequest},
		{"Is answered with prose", "yes, probably", func() error { _, err := Is(ctx, "s", "q?"); return err },
			"assess: malformed output: the response is not the JSON object that was asked for", types.KindMalformedOutput, types.ErrMalformedOutput},
		{"Is answered with an answer missing", "{}", func() error { _, err := Is(ctx, "s", "q?"); return err },
			"assess: schema violation: the response does not answer question 1", types.KindSchemaViolation, types.ErrSchemaViolation},
		{"Which answered with an option that was not offered", `{"q1": 9}`, func() error { _, err := Which(ctx, "s", "q?", []string{"a", "b"}); return err },
			"assess: schema violation: the answer to question 1 is not the number of one of its 2 options", types.KindSchemaViolation, types.ErrSchemaViolation},
		{"Rate answered off the scale", `{"q1": 9}`, func() error { _, err := Rate(ctx, "s", "q?", 1, 5); return err },
			"assess: schema violation: the answer to question 1 is not a whole number on its scale", types.KindSchemaViolation, types.ErrSchemaViolation},
		{"Assess answered with a question twice", `{"q1": "billing", "q1": "technical"}`, func() error { _, err := Assess[triage](ctx, "s"); return err },
			"assess: schema violation: the response answers a question, or gives its confidence, more than once", types.KindSchemaViolation, types.ErrSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.model != "" {
				defer stubLLM(tc.model)()
			} else {
				defer stubLLM(`{"q1": true}`)()
			}
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if err.Error() != tc.text {
				t.Errorf("text = %q, want %q", err.Error(), tc.text)
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v (%v)", kind, tc.kind, err)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
		})
	}
}

// A failure of the call comes back as the very value the call returned: the
// decision adds nothing to it, and does not give it a kind it had none of.
func TestDecisionReturnsTheCallsErrorAsItIs(t *testing.T) {
	budget := &types.OperationError{Kind: types.KindBudgetExceeded, Op: "theirs", Message: "spent"}
	plain := errors.New("a failure of the caller's own")
	for _, failure := range []error{budget, plain} {
		t.Run(failure.Error(), func(t *testing.T) {
			defer stubLLMError(failure)()
			ctx := context.Background()
			if _, err := Is(ctx, "s", "q?"); err != failure {
				t.Errorf("Is: err = %#v", err)
			}
			if _, err := Which(ctx, "s", "q?", []string{"a", "b"}); err != failure {
				t.Errorf("Which: err = %#v", err)
			}
			if _, err := Rate(ctx, "s", "q?", 1, 5); err != failure {
				t.Errorf("Rate: err = %#v", err)
			}
			if _, err := Assess[struct {
				Yes bool `ask:"Is it?"`
			}](ctx, "s"); err != failure {
				t.Errorf("Assess: err = %#v", err)
			}
		})
	}
}

// The lines a decision writes go to the logger of the client on its context,
// and with no client to the process logger.
func TestDecisionLinesGoToTheCallsLogger(t *testing.T) {
	cases := []struct {
		name  string
		model func() func()
		run   func(ctx context.Context)
		lines []string
	}{
		{"a call that fails", func() func() { return stubLLMError(errors.New("an error nobody classified")) },
			func(ctx context.Context) { _, _ = Is(ctx, "s", "q?") },
			[]string{"Assess operation LLM call failed"}},
		{"an answer that is refused", func() func() { return stubLLM("maybe") },
			func(ctx context.Context) { _, _ = Is(ctx, "s", "q?") },
			[]string{"Assess operation failed: the answer was refused"}},
		{"an observer that panics", func() func() { return stubLLM(`{"q1": true}`) },
			func(ctx context.Context) {
				ctx = WithDecisionObserver(ctx, func(context.Context, DecisionRecord) { panic("secret") })
				if got, err := Is(ctx, "s", "q?"); err != nil || !got {
					t.Errorf("a panicking observer changed the decision: %v, %v", got, err)
				}
			},
			[]string{"A decision observer panicked; the decision is not affected"}},
	}
	for _, tc := range cases {
		t.Run(tc.name+", with a snapshot", func(t *testing.T) {
			defer tc.model()()
			ctx, snapshot, process := controlFlowSnapshotLog(t)
			tc.run(ctx)
			for _, line := range tc.lines {
				if !strings.Contains(snapshot.String(), line) {
					t.Errorf("the snapshot's logger did not get %q: %q", line, snapshot.String())
				}
			}
			if got := process(); got != "" {
				t.Errorf("the process logger got lines: %q", got)
			}
			if strings.Contains(snapshot.String(), "secret") {
				t.Errorf("the panic's value is in the log: %q", snapshot.String())
			}
		})
		t.Run(tc.name+", with no snapshot", func(t *testing.T) {
			defer tc.model()()
			process := captureLogs(t)
			tc.run(context.Background())
			for _, line := range tc.lines {
				if !strings.Contains(process(), line) {
					t.Errorf("the process logger did not get %q: %q", line, process())
				}
			}
		})
	}
}

// A request that cannot be written is refused with its kind, and the reason a
// text could not be quoted keeps the encoder's own message.
func TestDecisionRequestFailuresHaveKinds(t *testing.T) {
	yesNo := func(text string) dec12Ask {
		return dec12Ask{questions: []assessQuestion{{kind: questionYesNo, ask: text}}}
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{"a subject that is not JSON", func() error {
			_, err := dec12BuildRequest(context.Background(), make(chan int), yesNo("Is it?"), false, nil)
			return err
		}},
		{"a subject that says nothing", func() error {
			_, err := dec12BuildRequest(context.Background(), struct{ hidden int }{1}, yesNo("Is it?"), false, nil)
			return err
		}},
		{"no questions", func() error {
			_, err := dec12BuildRequest(context.Background(), "s", dec12Ask{}, false, nil)
			return err
		}},
		{"a blank question", func() error {
			_, err := dec12BuildRequest(context.Background(), "s", dec12Ask{questions: []assessQuestion{{kind: questionYesNo, ask: " "}}}, false, nil)
			return err
		}},
		{"a nil context", func() error {
			//lint:ignore SA1012 a nil context is a case the function has to take
			_, err := dec12BuildRequest(nil, make(chan int), yesNo("Is it?"), false, nil)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if types.KindOf(err) == types.KindUnknown {
				t.Errorf("the error has no kind: %v", err)
			}
		})
	}
}

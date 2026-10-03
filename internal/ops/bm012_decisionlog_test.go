package ops

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// BM-012. The record of a decision.

func dec12Float(value float64) *float64 { return &value }

// The record is built from what was asked and what was read, and from nothing
// else: an answer that is not there is not filled in, and nothing of the model's
// reaches it but the answers.
func TestDec12DecisionRecordIsBuiltFromWhatWasAskedAndRead(t *testing.T) {
	yes := assessQuestion{kind: questionYesNo, ask: "Is it spam?"}
	team := assessQuestion{kind: questionChoice, ask: "Which team?", labels: []string{"billing", "technical", "shipping"}}
	urgency := assessQuestion{kind: questionRating, ask: "How urgent?", low: 1, high: 5}
	failure := types.Newf(types.KindSchemaViolation, "assess", "the response does not answer question 1")
	elapsed := 1500 * time.Microsecond

	cases := []struct {
		name            string
		operation       string
		questions       []assessQuestion
		answers         []answer
		confidence      bool
		modelConfidence float64
		err             error
		want            DecisionRecord
	}{
		{"a yes", "is", []assessQuestion{yes}, []answer{{yes: true}}, false, 0, nil,
			DecisionRecord{Operation: "is", Asked: []AnsweredQuestion{{Question: "Is it spam?", Answer: true}}, Elapsed: elapsed}},
		{"a no is an answer", "is", []assessQuestion{yes}, []answer{{yes: false}}, false, 0, nil,
			DecisionRecord{Operation: "is", Asked: []AnsweredQuestion{{Question: "Is it spam?", Answer: false}}, Elapsed: elapsed}},
		{"a choice names its label and its options", "which", []assessQuestion{team}, []answer{{choice: 1}}, false, 0, nil,
			DecisionRecord{Operation: "which", Asked: []AnsweredQuestion{{Question: "Which team?", Options: []string{"billing", "technical", "shipping"}, Answer: "technical"}}, Elapsed: elapsed}},
		{"the first and the last label", "which", []assessQuestion{team, team}, []answer{{choice: 0}, {choice: 2}}, false, 0, nil,
			DecisionRecord{Operation: "which", Asked: []AnsweredQuestion{
				{Question: "Which team?", Options: []string{"billing", "technical", "shipping"}, Answer: "billing"},
				{Question: "Which team?", Options: []string{"billing", "technical", "shipping"}, Answer: "shipping"}}, Elapsed: elapsed}},
		{"a rating names its level and its scale", "rate", []assessQuestion{urgency}, []answer{{rating: 4}}, false, 0, nil,
			DecisionRecord{Operation: "rate", Asked: []AnsweredQuestion{{Question: "How urgent?", Low: 1, High: 5, Answer: 4}}, Elapsed: elapsed}},
		{"a rating of zero is an answer", "rate", []assessQuestion{{kind: questionRating, ask: "How?", low: 0, high: 3}}, []answer{{rating: 0}}, false, 0, nil,
			DecisionRecord{Operation: "rate", Asked: []AnsweredQuestion{{Question: "How?", Low: 0, High: 3, Answer: 0}}, Elapsed: elapsed}},
		{"questions in the order they were asked", "assess", []assessQuestion{yes, team, urgency}, []answer{{yes: true}, {choice: 2}, {rating: 5}}, false, 0, nil,
			DecisionRecord{Operation: "assess", Asked: []AnsweredQuestion{
				{Question: "Is it spam?", Answer: true},
				{Question: "Which team?", Options: []string{"billing", "technical", "shipping"}, Answer: "shipping"},
				{Question: "How urgent?", Low: 1, High: 5, Answer: 5}}, Elapsed: elapsed}},
		{"the confidence, when it was asked for", "assess", []assessQuestion{yes}, []answer{{yes: true}}, true, 0.75, nil,
			DecisionRecord{Operation: "assess", Asked: []AnsweredQuestion{{Question: "Is it spam?", Answer: true}}, ModelConfidence: dec12Float(0.75), Elapsed: elapsed}},
		{"a confidence of zero, when it was asked for", "is", []assessQuestion{yes}, []answer{{yes: true}}, true, 0, nil,
			DecisionRecord{Operation: "is", Asked: []AnsweredQuestion{{Question: "Is it spam?", Answer: true}}, ModelConfidence: dec12Float(0), Elapsed: elapsed}},
		{"no confidence when it was not asked for", "is", []assessQuestion{yes}, []answer{{yes: true}}, false, 0.9, nil,
			DecisionRecord{Operation: "is", Asked: []AnsweredQuestion{{Question: "Is it spam?", Answer: true}}, Elapsed: elapsed}},
		{"a failure lists its questions and no answers", "assess", []assessQuestion{yes, team, urgency}, nil, false, 0, failure,
			DecisionRecord{Operation: "assess", Asked: []AnsweredQuestion{
				{Question: "Is it spam?"},
				{Question: "Which team?", Options: []string{"billing", "technical", "shipping"}},
				{Question: "How urgent?", Low: 1, High: 5}}, Elapsed: elapsed, Err: failure}},
		{"a failure has no answer even when some were read", "is", []assessQuestion{yes}, []answer{{yes: true}}, true, 0.5, failure,
			DecisionRecord{Operation: "is", Asked: []AnsweredQuestion{{Question: "Is it spam?"}}, Elapsed: elapsed, Err: failure}},
		{"a failure with no question", "assess", nil, nil, false, 0, failure,
			DecisionRecord{Operation: "assess", Elapsed: elapsed, Err: failure}},
		{"an answer that is not there is not filled in", "assess", []assessQuestion{yes, urgency}, []answer{{yes: true}}, false, 0, nil,
			DecisionRecord{Operation: "assess", Asked: []AnsweredQuestion{{Question: "Is it spam?", Answer: true}, {Question: "How urgent?", Low: 1, High: 5}}, Elapsed: elapsed}},
		{"a label that is out of range is not invented", "which", []assessQuestion{team}, []answer{{choice: 7}}, false, 0, nil,
			DecisionRecord{Operation: "which", Asked: []AnsweredQuestion{{Question: "Which team?", Options: []string{"billing", "technical", "shipping"}}}, Elapsed: elapsed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dec12DecisionRecord(tc.operation, tc.questions, tc.answers, tc.confidence, tc.modelConfidence, tc.err, elapsed)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("record:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}

	t.Run("the options are a copy of the caller's labels", func(t *testing.T) {
		labels := []string{"a", "b"}
		got := dec12DecisionRecord("which", []assessQuestion{{kind: questionChoice, ask: "Which?", labels: labels}}, []answer{{choice: 0}}, false, 0, nil, 0)
		got.Asked[0].Options[0] = "changed by the observer"
		if labels[0] != "a" {
			t.Error("the record shares the caller's labels")
		}
	})
	t.Run("the confidence is a copy, not the caller's variable", func(t *testing.T) {
		held := 0.4
		got := dec12DecisionRecord("is", []assessQuestion{yes}, []answer{{yes: true}}, true, held, nil, 0)
		*got.ModelConfidence = 0.9
		if held != 0.4 {
			t.Error("the record points at the caller's variable")
		}
	})
}

// dec12Observed is what an observer was told, in order.
type dec12Observed struct {
	mu      sync.Mutex
	records []DecisionRecord
}

func (o *dec12Observed) observe(_ context.Context, record DecisionRecord) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
}

func (o *dec12Observed) taken() []DecisionRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]DecisionRecord(nil), o.records...)
}

// dec12Observing returns a context whose client has an observer, answering with
// response.
func dec12Observing(response string) (context.Context, *dec12Recorder, *dec12Observed) {
	ctx, recorder := dec12WithRecorder(response)
	observed := &dec12Observed{}
	return WithExecConfig(ctx, &ExecConfig{DecisionObserver: observed.observe}), recorder, observed
}

// Every form of a decision tells the observer, with the kind of decision it is.
func TestEveryDecisionTellsTheObserver(t *testing.T) {
	type triage struct {
		Spam bool   `ask:"Is this spam?"`
		Team string `ask:"Which team?" oneof:"billing,technical"`
	}
	cases := []struct {
		name      string
		response  string
		run       func(ctx context.Context) error
		operation string
		asked     int
		answer    any
		wantConf  bool
	}{
		{"Is", `{"q1":true}`, func(ctx context.Context) error { _, err := Is(ctx, "t", "Is it?"); return err }, "is", 1, true, false},
		{"IsResult", `{"q1":false,"confidence":0.25}`, func(ctx context.Context) error { _, err := IsResult(ctx, "t", "Is it?"); return err }, "is", 1, false, true},
		{"Which", `{"q1":2}`, func(ctx context.Context) error { _, err := Which(ctx, "t", "Which?", []string{"a", "b"}); return err }, "which", 1, "b", false},
		{"WhichResult", `{"q1":1,"confidence":0.25}`, func(ctx context.Context) error {
			_, err := WhichResult(ctx, "t", "Which?", []string{"a", "b"})
			return err
		}, "which", 1, "a", true},
		{"WhichOrNone, a label", `{"q1":1}`, func(ctx context.Context) error {
			_, _, err := WhichOrNone(ctx, "t", "Which?", []string{"a", "b"})
			return err
		}, "which", 1, "a", false},
		{"WhichOrNone, none", `{"q1":3}`, func(ctx context.Context) error {
			_, _, err := WhichOrNone(ctx, "t", "Which?", []string{"a", "b"})
			return err
		}, "which", 1, whichNone, false},
		{"WhichOrNoneResult", `{"q1":2,"confidence":0.25}`, func(ctx context.Context) error {
			_, err := WhichOrNoneResult(ctx, "t", "Which?", []string{"a", "b"})
			return err
		}, "which", 1, "b", true},
		{"Rate", `{"q1":4}`, func(ctx context.Context) error { _, err := Rate(ctx, "t", "How?", 1, 5); return err }, "rate", 1, 4, false},
		{"RateResult", `{"q1":4,"confidence":0.25}`, func(ctx context.Context) error {
			_, err := RateResult(ctx, "t", "How?", 1, 5)
			return err
		}, "rate", 1, 4, true},
		{"Assess", `{"q1":true,"q2":2}`, func(ctx context.Context) error { _, err := Assess[triage](ctx, "t"); return err }, "assess", 2, true, false},
		{"AssessResult", `{"q1":true,"q2":2,"confidence":0.25}`, func(ctx context.Context) error {
			_, err := AssessResult[triage](ctx, "t")
			return err
		}, "assess", 2, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, observed := dec12Observing(tc.response)
			if err := tc.run(ctx); err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			records := observed.taken()
			if len(records) != 1 {
				t.Fatalf("the observer was told %d times", len(records))
			}
			record := records[0]
			if record.Operation != tc.operation || len(record.Asked) != tc.asked || record.Err != nil {
				t.Fatalf("record = %+v", record)
			}
			if record.Asked[0].Answer != tc.answer {
				t.Errorf("the first answer is %#v, want %#v", record.Asked[0].Answer, tc.answer)
			}
			if (record.ModelConfidence != nil) != tc.wantConf || (tc.wantConf && *record.ModelConfidence != 0.25) {
				t.Errorf("the confidence is %v, wanted one: %v", record.ModelConfidence, tc.wantConf)
			}
			if record.Elapsed < 0 {
				t.Errorf("the elapsed time is %v", record.Elapsed)
			}
		})
	}
}

// A decision that fails tells the observer too, with its questions, its error
// and no answers.
func TestADecisionThatFailsTellsTheObserver(t *testing.T) {
	providerDown := errors.New("the provider is down")
	cases := []struct {
		name     string
		response string
		down     error
		run      func(ctx context.Context) error
		kind     types.ErrorKind
		asked    int
		sent     bool
	}{
		{"an answer that leaves a question out", `{}`, nil, func(ctx context.Context) error { _, err := Is(ctx, "t", "Is it?"); return err }, types.KindSchemaViolation, 1, true},
		{"an answer off the scale", `{"q1":9}`, nil, func(ctx context.Context) error { _, err := Rate(ctx, "t", "How?", 1, 5); return err }, types.KindSchemaViolation, 1, true},
		{"a response that is not JSON", `no`, nil, func(ctx context.Context) error { _, err := Is(ctx, "t", "Is it?"); return err }, types.KindMalformedOutput, 1, true},
		{"a provider that fails", ``, providerDown, func(ctx context.Context) error { _, err := Is(ctx, "t", "Is it?"); return err }, types.KindUnknown, 1, true},
		{"a question that cannot be asked", `{"q1":true}`, nil, func(ctx context.Context) error { _, err := Is(ctx, "t", "  "); return err }, types.KindConfiguration, 1, false},
		{"labels that cannot be offered", `{"q1":1}`, nil, func(ctx context.Context) error {
			_, err := Which(ctx, "t", "Which?", []string{"only"})
			return err
		}, types.KindConfiguration, 1, false},
		{"a subject that says nothing", `{"q1":true}`, nil, func(ctx context.Context) error {
			_, err := Is(ctx, struct{ hidden int }{1}, "Is it?")
			return err
		}, types.KindInvalidRequest, 1, false},
		{"a Result form", `{}`, nil, func(ctx context.Context) error { _, err := IsResult(ctx, "t", "Is it?"); return err }, types.KindSchemaViolation, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder, observed := dec12Observing(tc.response)
			recorder.err = tc.down
			err := tc.run(ctx)
			if err == nil {
				t.Fatal("the call did not fail")
			}
			records := observed.taken()
			if len(records) != 1 {
				t.Fatalf("the observer was told %d times", len(records))
			}
			record := records[0]
			if record.Err != err {
				t.Errorf("the record's error is %v, the caller's is %v", record.Err, err)
			}
			if tc.kind != types.KindUnknown && types.KindOf(err) != tc.kind {
				t.Errorf("the error is classified %v, want %v", types.KindOf(err), tc.kind)
			}
			if len(record.Asked) != tc.asked {
				t.Fatalf("the record lists %d questions, want %d", len(record.Asked), tc.asked)
			}
			for _, asked := range record.Asked {
				if asked.Answer != nil {
					t.Errorf("a failed decision has an answer: %#v", asked.Answer)
				}
			}
			if record.ModelConfidence != nil {
				t.Error("a failed decision has a confidence")
			}
			if sent := len(recorder.taken()) > 0; sent != tc.sent {
				t.Errorf("a request was sent: %v, want %v", sent, tc.sent)
			}
		})
	}
}

// With no observer nothing is built and nothing is called, and the decision is
// what it was.
func TestADecisionWithNoObserverBuildsNoRecord(t *testing.T) {
	ctx, _ := dec12WithRecorder(`{"q1":true}`)
	if ExecDecisionObserver(ctx) != nil {
		t.Fatal("the context has an observer")
	}
	// An exec config with no observer is the same.
	withConfig := WithExecConfig(ctx, &ExecConfig{})
	if ExecDecisionObserver(withConfig) != nil || ExecDecisionObserver(nil) != nil {
		t.Fatal("an observer came from nowhere")
	}
	for _, c := range []context.Context{ctx, withConfig} {
		if got, err := Is(c, "t", "Is it?"); err != nil || !got {
			t.Errorf("Is = %v, %v", got, err)
		}
	}

	// The allocations of a decision are the same with no observer as before one
	// existed: nothing is done for an observer that is not there.
	dec12StubProvider(t, `{"q1":true}`)
	plain := testing.AllocsPerRun(50, func() { _, _ = Is(context.Background(), dec12Subject, "Is it?") })
	registered := WithExecConfig(context.Background(), &ExecConfig{DecisionObserver: func(context.Context, DecisionRecord) {}})
	observed := testing.AllocsPerRun(50, func() { _, _ = Is(registered, dec12Subject, "Is it?") })
	if observed <= plain {
		t.Errorf("a decision with an observer makes %v allocations and one without makes %v: the record costs nothing, or the count is wrong", observed, plain)
	}
}

// A panic in the observer is the observer's: the decision returns what it
// would have, and what is logged is not the record.
func TestAPanicInTheObserverDoesNotTakeTheDecisionDown(t *testing.T) {
	logger.GetLogger().ResetEntries()
	ctx, recorder := dec12WithRecorder("")
	recorder.replies = []string{`{"q1":true}`, `{"q1":true,"q2":2}`}
	panicking := func(_ context.Context, record DecisionRecord) {
		panic(fmt.Sprintf("the observer broke on %q", record.Asked[0].Question))
	}
	ctx = WithExecConfig(ctx, &ExecConfig{DecisionObserver: panicking})

	got, err := Is(ctx, "t", "Is the secret code 1234 right?")
	if err != nil || !got {
		t.Fatalf("Is = %v, %v", got, err)
	}
	type triage struct {
		Spam bool   `ask:"Is this spam?"`
		Team string `ask:"Which team?" oneof:"billing,technical"`
	}
	if filled, err := Assess[triage](ctx, "t"); err != nil || !filled.Spam || filled.Team != "technical" {
		t.Fatalf("Assess = %+v, %v", filled, err)
	}

	var logged int
	for _, entry := range logger.GetLogger().Entries() {
		if strings.Contains(entry.Message, "decision observer panicked") {
			logged++
			if entry.Level != "ERROR" && entry.Level != "error" {
				t.Errorf("the panic was logged at level %q", entry.Level)
			}
		}
		flat := fmt.Sprint(entry.Message, entry.Attributes)
		if strings.Contains(flat, "secret code") || strings.Contains(flat, "1234") || strings.Contains(flat, "the observer broke") {
			t.Errorf("a log entry carries the record's text or the panic's: %s", flat)
		}
	}
	if logged != 2 {
		t.Errorf("%d panics were logged, want 2", logged)
	}
}

// The observer is told when no lock is held: a decision made from inside it, on
// the same client, completes, and so does one from a goroutine it waits for.
func TestTheObserverIsToldWithNoLockHeld(t *testing.T) {
	ctx, _ := dec12WithRecorder(`{"q1":true}`)
	var nested []DecisionRecord
	var mu sync.Mutex
	var depth int
	var observer func(context.Context, DecisionRecord)
	observer = func(c context.Context, record DecisionRecord) {
		mu.Lock()
		nested = append(nested, record)
		first := depth == 0
		depth++
		mu.Unlock()
		if !first {
			return
		}
		done := make(chan error, 1)
		go func() {
			_, err := Is(c, "inner", "Is the inner one fine?")
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the decision made from the observer failed: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("a decision made from the observer did not finish: the observer was told with a lock held")
		}
	}
	ctx = WithExecConfig(ctx, &ExecConfig{DecisionObserver: observer})
	if _, err := Is(ctx, "outer", "Is the outer one fine?"); err != nil {
		t.Fatal(err)
	}
	if len(nested) != 2 {
		t.Errorf("the observer was told %d times, want 2", len(nested))
	}
}

// Decisions that run at once each tell the observer once, with their own
// question and their own answer.
func TestDecisionsRunningAtOnceEachTellTheObserver(t *testing.T) {
	ctx, _, observed := dec12Observing(`{"q1":true}`)
	var wait sync.WaitGroup
	const workers = 24
	for n := 0; n < workers; n++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := Is(ctx, "t", fmt.Sprintf("Is number %d fine?", n)); err != nil {
				t.Errorf("Is: %v", err)
			}
		}()
	}
	wait.Wait()
	seen := map[string]bool{}
	for _, record := range observed.taken() {
		if len(record.Asked) != 1 || record.Asked[0].Answer != true {
			t.Fatalf("a record is wrong: %+v", record)
		}
		seen[record.Asked[0].Question] = true
	}
	if len(seen) != workers {
		t.Errorf("%d different questions were reported, want %d", len(seen), workers)
	}
}

// The record goes to the observer and nowhere else: not to the log, and not into
// an error.
func TestTheRecordIsNotLoggedAndIsInNoError(t *testing.T) {
	// Read at debug, the most talkative level: the process logger's own level is
	// info, and a payload logged below it would not be seen.
	logs := captureLogs(t)
	ctx, _, observed := dec12Observing(`{}`)
	_, err := Is(ctx, "a subject with a private word: periwinkle", "Is the private question answered?")
	if err == nil {
		t.Fatal("the call did not fail")
	}
	if len(observed.taken()) != 1 {
		t.Fatal("the observer was not told")
	}
	if logs() == "" {
		t.Fatal("the operation logged nothing, so this witnesses nothing")
	}
	for _, text := range []string{"periwinkle", "private question"} {
		if strings.Contains(err.Error(), text) {
			t.Errorf("the error carries %q: %v", text, err)
		}
		if strings.Contains(logs(), text) {
			t.Errorf("a log entry carries %q:\n%s", text, logs())
		}
	}
}

// The questions a type asks reach the observer through Assess, in the order
// they were written, with each kind of answer.
func TestTheObserverIsToldWhatAnAssessAnswered(t *testing.T) {
	ctx, _, observed := dec12Observing(dec12LookAnswer)
	if _, err := Assess[dec12Look](ctx, dec12Subject); err != nil {
		t.Fatal(err)
	}
	record := observed.taken()[0]
	if len(record.Asked) != 13 {
		t.Fatalf("%d questions were reported, want 13", len(record.Asked))
	}
	want := []any{false, false, "English", false, "billing", "making a complaint", 4, true, true, true, true, "escalate to a manager", false}
	for i, asked := range record.Asked {
		if asked.Answer != want[i] {
			t.Errorf("question %d (%q): answer %#v, want %#v", i+1, asked.Question, asked.Answer, want[i])
		}
	}
	if record.Asked[2].Options[0] != "English" || len(record.Asked[2].Options) != 6 {
		t.Errorf("the options of the language question are %v", record.Asked[2].Options)
	}
	if record.Asked[6].Low != 1 || record.Asked[6].High != 5 {
		t.Errorf("the scale of the urgency question is %d to %d", record.Asked[6].Low, record.Asked[6].High)
	}
}

package schemafluxtest_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func yn(ask string) schemaflux.DecisionQuestion {
	return schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo, Ask: ask}
}

func choice(options ...string) schemaflux.DecisionQuestion {
	return schemaflux.DecisionQuestion{Kind: schemaflux.DecisionChoice, Ask: "which?", Options: options}
}

func scale(low, high int) schemaflux.DecisionQuestion {
	return schemaflux.DecisionQuestion{Kind: schemaflux.DecisionScale, Ask: "how much?", Low: low, High: high}
}

// decide is a request that carries a decision of the questions given.
func decide(questions ...schemaflux.DecisionQuestion) schemaflux.Request {
	return schemaflux.Request{Model: "m", Decision: &schemaflux.Decision{State: `"the subject"`, Questions: questions}}
}

func TestTheAnswerConstructorsBuildTypedAnswers(t *testing.T) {
	t.Parallel()
	type build func(p ...float64) schemaflux.DecisionAnswer
	constructors := []struct {
		name  string
		build build
		check func(a schemaflux.DecisionAnswer) bool
	}{
		{"Yes", func(p ...float64) schemaflux.DecisionAnswer { return schemafluxtest.Yes(p...) },
			func(a schemaflux.DecisionAnswer) bool { return a.Kind == schemaflux.DecisionYesNo && a.Yes }},
		{"No", func(p ...float64) schemaflux.DecisionAnswer { return schemafluxtest.No(p...) },
			func(a schemaflux.DecisionAnswer) bool { return a.Kind == schemaflux.DecisionYesNo && !a.Yes }},
		{"Chose", func(p ...float64) schemaflux.DecisionAnswer { return schemafluxtest.Chose("billing", p...) },
			func(a schemaflux.DecisionAnswer) bool {
				return a.Kind == schemaflux.DecisionChoice && a.Label == "billing"
			}},
		{"Level", func(p ...float64) schemaflux.DecisionAnswer { return schemafluxtest.Level(4, p...) },
			func(a schemaflux.DecisionAnswer) bool { return a.Kind == schemaflux.DecisionScale && a.Level == 4 }},
	}
	probabilities := []struct {
		name     string
		given    []float64
		reported bool
		value    float64
		panics   bool
	}{
		{"none reports none", nil, false, 0, false},
		{"0.97", []float64{0.97}, true, 0.97, false},
		{"zero is a reported probability of zero", []float64{0}, true, 0, false},
		{"one", []float64{1}, true, 1, false},
		{"one half", []float64{0.5}, true, 0.5, false},
		{"a tiny probability", []float64{1e-12}, true, 1e-12, false},
		{"negative panics", []float64{-0.1}, false, 0, true},
		{"above one panics", []float64{1.01}, false, 0, true},
		{"not a number panics", []float64{math.NaN()}, false, 0, true},
		{"infinity panics", []float64{math.Inf(1)}, false, 0, true},
		{"two probabilities panic", []float64{0.5, 0.6}, false, 0, true},
	}
	for _, c := range constructors {
		for _, p := range probabilities {
			t.Run(c.name+" with "+p.name, func(t *testing.T) {
				t.Parallel()
				if p.panics {
					defer func() {
						if recover() == nil {
							t.Error("did not panic")
						}
					}()
					c.build(p.given...)
					return
				}
				a := c.build(p.given...)
				if !c.check(a) {
					t.Fatalf("answer = %+v", a)
				}
				if a.ConfidenceReported != p.reported || a.ModelConfidence != p.value {
					t.Fatalf("probability = %v reported %v, want %v reported %v", a.ModelConfidence, a.ConfidenceReported, p.value, p.reported)
				}
			})
		}
	}
}

func TestNewDeciderAnswersEachQuestionInOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("one answer to one question", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(0.9))
		resp, err := d.Complete(ctx, decide(yn("q")))
		if err != nil || len(resp.Answers) != 1 || !resp.Answers[0].Yes || resp.Answers[0].ModelConfidence != 0.9 {
			t.Fatalf("resp = %+v, err = %v", resp, err)
		}
	})
	t.Run("several answers to several questions, in order", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.Chose("b"), schemafluxtest.Level(3))
		resp, err := d.Complete(ctx, decide(yn("a"), choice("a", "b"), scale(1, 5)))
		if err != nil || len(resp.Answers) != 3 || resp.Answers[1].Label != "b" || resp.Answers[2].Level != 3 {
			t.Fatalf("resp = %+v, err = %v", resp, err)
		}
	})
	t.Run("the answers are handed out across requests", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.No(), schemafluxtest.Yes())
		first, _ := d.Complete(ctx, decide(yn("a")))
		second, _ := d.Complete(ctx, decide(yn("a"), yn("b")))
		if !first.Answers[0].Yes || second.Answers[0].Yes || !second.Answers[1].Yes {
			t.Fatalf("answers = %+v, %+v", first.Answers, second.Answers)
		}
	})
	t.Run("a question with no answer left fails the request", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes())
		_, _ = d.Complete(ctx, decide(yn("a")))
		_, err := d.Complete(ctx, decide(yn("b")))
		if !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Fatalf("err = %v, want ErrConfiguration", err)
		}
	})
	t.Run("a decider with no answers fails the first question", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider()
		if _, err := d.Complete(ctx, decide(yn("a"))); !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a request is answered whole or not at all", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.No())
		if _, err := d.Complete(ctx, decide(yn("a"), yn("b"), yn("c"))); err == nil {
			t.Fatal("a request of three questions was answered with two answers")
		}
		resp, err := d.Complete(ctx, decide(yn("a"), yn("b")))
		if err != nil || len(resp.Answers) != 2 {
			t.Fatalf("the two answers were taken by the refused request: %+v, %v", resp, err)
		}
	})
	t.Run("the answers given are copied", func(t *testing.T) {
		t.Parallel()
		answers := []schemaflux.DecisionAnswer{schemafluxtest.Yes()}
		d := schemafluxtest.NewDecider(answers...)
		answers[0] = schemafluxtest.No()
		resp, _ := d.Complete(ctx, decide(yn("a")))
		if !resp.Answers[0].Yes {
			t.Fatal("changing the slice changed the script")
		}
	})
	t.Run("the answers returned are copies", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.Yes())
		first, _ := d.Complete(ctx, decide(yn("a")))
		first.Answers[0] = schemafluxtest.No()
		d.Reset()
		again, _ := d.Complete(ctx, decide(yn("a")))
		if !again.Answers[0].Yes {
			t.Fatal("changing an answer returned changed the script")
		}
	})
	t.Run("the kind of an answer is not checked against its question", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Level(2))
		resp, err := d.Complete(ctx, decide(yn("a")))
		if err != nil || resp.Answers[0].Kind != schemaflux.DecisionScale {
			t.Fatalf("resp = %+v, err = %v", resp, err)
		}
	})
	t.Run("the zero decider is ready to be scripted with Reply", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.Reply(schemafluxtest.No())
		resp, err := d.Complete(ctx, decide(yn("a")))
		if err != nil || resp.Answers[0].Yes {
			t.Fatalf("resp = %+v, err = %v", resp, err)
		}
	})
	t.Run("a response carries no text", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes())
		resp, _ := d.Complete(ctx, decide(yn("a")))
		if resp.Content != "" || resp.Provider != "" || resp.FinishReason != "" {
			t.Fatalf("resp = %+v", resp)
		}
	})
	t.Run("the response names the model asked", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes())
		resp, _ := d.Complete(ctx, decide(yn("a")))
		if resp.Model != "m" {
			t.Fatalf("Model = %q", resp.Model)
		}
	})
	t.Run("the response names test-model when none was asked for", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes())
		req := decide(yn("a"))
		req.Model = ""
		if resp, _ := d.Complete(ctx, req); resp.Model != "test-model" {
			t.Fatalf("Model = %q", resp.Model)
		}
	})
}

func TestADeciderRefusesWhatADecisionModelCannotTake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name   string
		limits schemaflux.DecisionLimits
		req    schemaflux.Request
		kind   schemaflux.Kind
	}{
		{"a request with no decision is unsupported", schemaflux.DecisionLimits{}, schemaflux.Request{User: "text"}, schemaflux.ErrUnsupported},
		{"a decision with no questions is unsupported", schemaflux.DecisionLimits{}, decide(), schemaflux.ErrUnsupported},
		{"more questions than the limit are unsupported", schemaflux.DecisionLimits{MaxQuestions: 1}, decide(yn("a"), yn("b")), schemaflux.ErrUnsupported},
		{"more options than the limit are unsupported", schemaflux.DecisionLimits{MaxOptions: 2}, decide(choice("a", "b", "c")), schemaflux.ErrUnsupported},
		{"more levels than the limit are unsupported", schemaflux.DecisionLimits{MaxLevels: 3}, decide(scale(1, 9)), schemaflux.ErrUnsupported},
		{"a longer state than the limit is unsupported", schemaflux.DecisionLimits{MaxStateTokens: 2}, schemaflux.Request{Decision: &schemaflux.Decision{State: strings.Repeat("x", 100), Questions: []schemaflux.DecisionQuestion{yn("a")}}}, schemaflux.ErrUnsupported},
		{"a blank question is an invalid request", schemaflux.DecisionLimits{}, decide(yn(" ")), schemaflux.ErrInvalidRequest},
		{"a choice of one option is an invalid request", schemaflux.DecisionLimits{}, decide(choice("only")), schemaflux.ErrInvalidRequest},
		{"a scale with nothing to choose is an invalid request", schemaflux.DecisionLimits{}, decide(scale(3, 3)), schemaflux.ErrInvalidRequest},
		{"a question with no kind is an invalid request", schemaflux.DecisionLimits{}, decide(schemaflux.DecisionQuestion{Ask: "a"}), schemaflux.ErrInvalidRequest},
		{"the limit is on the request and not the answers", schemaflux.DecisionLimits{MaxQuestions: 2}, decide(yn("a"), yn("b"), yn("c")), schemaflux.ErrUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.Yes(), schemafluxtest.Yes())
			d.SetLimits(c.limits)
			_, err := d.Complete(ctx, c.req)
			if !errors.Is(err, c.kind) {
				t.Fatalf("err = %v, want kind %q", err, c.kind)
			}
			if d.Calls() != 1 {
				t.Fatalf("a refused request was not recorded: Calls() = %d", d.Calls())
			}
			d.SetLimits(schemaflux.DecisionLimits{})
			if resp, err := d.Complete(ctx, decide(yn("fine"))); err != nil || len(resp.Answers) != 1 {
				t.Fatalf("a refused request used up an answer: %+v, %v", resp, err)
			}
		})
	}
}

func TestAFailedScriptOfADeciderSaysWhatWasAskedAndWhatWasScripted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("it names the question, the request, its kind and what is scripted", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(0.97), schemafluxtest.No())
		_, err := d.Complete(ctx, decide(yn("a"), yn("b"), choice("w", "x", "y", "z")))
		msg := messageOf(t, err)
		for _, want := range []string{"question 3 of request 1", "a choice among 4 options", "asks 3 questions", "2 answers (yes 0.97, no)", "0 already used"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message lacks %q: %s", want, msg)
			}
		}
	})
	t.Run("a scale is described by its ends", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider()
		_, err := d.Complete(ctx, decide(scale(1, 7)))
		if msg := messageOf(t, err); !strings.Contains(msg, "a scale from 1 to 7") || !strings.Contains(msg, "no answers") {
			t.Fatalf("message = %s", msg)
		}
	})
	t.Run("a yes or no is described as one", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider()
		_, err := d.Complete(ctx, decide(yn("a")))
		if msg := messageOf(t, err); !strings.Contains(msg, "a yes or no") {
			t.Fatalf("message = %s", msg)
		}
	})
	t.Run("it never repeats the caller's wording or state", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider()
		req := decide(yn("CANARY-QUESTION"), choice("CANARY-A", "CANARY-B"))
		req.Decision.State = `"CANARY-STATE"`
		_, err := d.Complete(ctx, req)
		if strings.Contains(err.Error(), "CANARY") {
			t.Fatalf("error repeats the caller's data: %v", err)
		}
	})
	t.Run("it names the answers already used", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Chose("billing", 0.5), schemafluxtest.Level(2))
		_, _ = d.Complete(ctx, decide(choice("billing", "x")))
		_, err := d.Complete(ctx, decide(scale(1, 3), yn("a")))
		if msg := messageOf(t, err); !strings.Contains(msg, `chose "billing" 0.5`) || !strings.Contains(msg, "level 2") || !strings.Contains(msg, "1 already used") {
			t.Fatalf("message = %s", msg)
		}
	})
	t.Run("it is bounded however many answers are scripted", func(t *testing.T) {
		t.Parallel()
		answers := make([]schemaflux.DecisionAnswer, 200)
		for i := range answers {
			answers[i] = schemafluxtest.Chose(strings.Repeat("label", 50))
		}
		d := schemafluxtest.NewDecider(answers...)
		questions := make([]schemaflux.DecisionQuestion, 201)
		for i := range questions {
			questions[i] = yn("a")
		}
		_, err := d.Complete(ctx, decide(questions...))
		msg := messageOf(t, err)
		if n := len([]rune(msg)); n > 600 {
			t.Fatalf("message is %d runes, want at most 600", n)
		}
		if !strings.Contains(msg, "question 201 of request 1") {
			t.Fatalf("a bounded message lost what was asked: %s", msg)
		}
	})
	t.Run("it lists at most six answers and counts the rest", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.Yes(), schemafluxtest.Yes(), schemafluxtest.Yes(),
			schemafluxtest.Yes(), schemafluxtest.Yes(), schemafluxtest.Yes(), schemafluxtest.Yes())
		qs := make([]schemaflux.DecisionQuestion, 9)
		for i := range qs {
			qs[i] = yn("a")
		}
		_, err := d.Complete(ctx, decide(qs...))
		if msg := messageOf(t, err); !strings.Contains(msg, "8 answers") || !strings.Contains(msg, "and 2 more") {
			t.Fatalf("message = %s", msg)
		}
	})
	t.Run("the kind is configuration and the message is lower case", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider()
		_, err := d.Complete(ctx, decide(yn("a")))
		var e *schemaflux.Error
		if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || !strings.HasPrefix(e.Message, "no answer is scripted") {
			t.Fatalf("err = %#v", err)
		}
	})
}

func TestADeciderRespondsFromTheDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("the function is given the decision", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		var got schemaflux.Decision
		d.Respond(func(_ int, decision schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			got = decision
			return []schemaflux.DecisionAnswer{schemafluxtest.Yes()}, nil
		})
		_, _ = d.Complete(ctx, decide(yn("a")))
		if got.State != `"the subject"` || len(got.Questions) != 1 {
			t.Fatalf("decision = %+v", got)
		}
	})
	t.Run("the function is given the zero-based position of the request", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		var seen []int
		d.Respond(func(call int, _ schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			seen = append(seen, call)
			return nil, nil
		})
		for range 3 {
			_, _ = d.Complete(ctx, decide(yn("a")))
		}
		if len(seen) != 3 || seen[0] != 0 || seen[2] != 2 {
			t.Fatalf("positions = %v", seen)
		}
	})
	t.Run("the answers come back as they are, whatever their number", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) { return nil, nil })
		resp, err := d.Complete(ctx, decide(yn("a")))
		if err != nil || len(resp.Answers) != 0 {
			t.Fatalf("resp = %+v, err = %v", resp, err)
		}
	})
	t.Run("an answer chosen by the question", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.Respond(func(_ int, decision schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			var out []schemaflux.DecisionAnswer
			for _, q := range decision.Questions {
				out = append(out, schemafluxtest.Level(q.High))
			}
			return out, nil
		})
		resp, _ := d.Complete(ctx, decide(scale(1, 4), scale(1, 9)))
		if resp.Answers[0].Level != 4 || resp.Answers[1].Level != 9 {
			t.Fatalf("answers = %+v", resp.Answers)
		}
	})
	t.Run("its error is returned as it is", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		sentinel := errors.New("chosen by the test")
		d.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) { return nil, sentinel })
		if _, err := d.Complete(ctx, decide(yn("a"))); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("it is not called for a request that is refused", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		called := false
		d.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) { called = true; return nil, nil })
		_, _ = d.Complete(ctx, schemaflux.Request{User: "no decision"})
		if called {
			t.Fatal("the function was called for a request with no decision")
		}
	})
	t.Run("a nil function is refused as it is for a Provider", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.No())
		d.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			return []schemaflux.DecisionAnswer{schemafluxtest.Yes()}, nil
		})
		defer func() {
			if recovered := recover(); recovered == nil || !strings.Contains(fmt.Sprint(recovered), "Respond needs a function") {
				t.Errorf("recovered %v, want a panic that says Respond needs a function", recovered)
			}
			if resp, _ := d.Complete(ctx, decide(yn("a"))); !resp.Answers[0].Yes {
				t.Error("the function that was there stopped answering")
			}
		}()
		d.Respond(nil)
	})
	t.Run("Reply replaces it", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			return []schemaflux.DecisionAnswer{schemafluxtest.Yes()}, nil
		})
		d.Reply(schemafluxtest.No())
		if resp, _ := d.Complete(ctx, decide(yn("a"))); resp.Answers[0].Yes {
			t.Fatal("the function answered after Reply")
		}
	})
	t.Run("a scripted failure comes before it", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		called := false
		d.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) { called = true; return nil, nil })
		d.Fail(schemaflux.ErrBusy)
		_, err := d.Complete(ctx, decide(yn("a")))
		if called || !errors.Is(err, schemaflux.ErrBusy) {
			t.Fatalf("called = %v, err = %v", called, err)
		}
	})
	t.Run("Reset starts the positions again", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		var seen []int
		d.Respond(func(call int, _ schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			seen = append(seen, call)
			return nil, nil
		})
		_, _ = d.Complete(ctx, decide(yn("a")))
		d.Reset()
		_, _ = d.Complete(ctx, decide(yn("a")))
		if len(seen) != 2 || seen[1] != 0 {
			t.Fatalf("positions = %v", seen)
		}
	})
	t.Run("it may read the fake without a deadlock", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.Respond(func(int, schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			_ = d.Calls()
			_ = d.Last()
			return []schemaflux.DecisionAnswer{schemafluxtest.Yes()}, nil
		})
		if _, err := d.Complete(ctx, decide(yn("a"))); err != nil {
			t.Fatal(err)
		}
	})
}

func TestADeciderFailsAsScripted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	down := schemaflux.ErrUnavailable

	cases := []struct {
		name  string
		setup func(d *schemafluxtest.Decider)
		want  []schemaflux.Kind // 0 is an answer
	}{
		{"Fail fails every request", func(d *schemafluxtest.Decider) { d.Fail(down) }, []schemaflux.Kind{down, down, down}},
		{"Fail keeps the kind of the error", func(d *schemafluxtest.Decider) { d.Fail(schemaflux.ErrRateLimited) }, []schemaflux.Kind{schemaflux.ErrRateLimited}},
		{"Fail with nil is the provider being unavailable", func(d *schemafluxtest.Decider) { d.Fail(nil) }, []schemaflux.Kind{down}},
		{"FailThen fails the first requests", func(d *schemafluxtest.Decider) { d.FailThen(2, down) }, []schemaflux.Kind{down, down, 0, 0}},
		{"FailThen of none fails nothing", func(d *schemafluxtest.Decider) { d.FailThen(0, down) }, []schemaflux.Kind{0, 0}},
		{"FailThen of a negative number fails nothing", func(d *schemafluxtest.Decider) { d.FailThen(-3, down) }, []schemaflux.Kind{0}},
		{"FailThen with nil is the provider being unavailable", func(d *schemafluxtest.Decider) { d.FailThen(1, nil) }, []schemaflux.Kind{down, 0}},
		{"FailThen after Fail ends the outage after its count", func(d *schemafluxtest.Decider) { d.Fail(down); d.FailThen(1, schemaflux.ErrBusy) }, []schemaflux.Kind{schemaflux.ErrBusy, 0}},
		{"Fail after FailThen is a total outage", func(d *schemafluxtest.Decider) { d.FailThen(1, down); d.Fail(schemaflux.ErrClosed) }, []schemaflux.Kind{schemaflux.ErrClosed, schemaflux.ErrClosed}},
		{"a failed request does not take an answer", func(d *schemafluxtest.Decider) { d.FailThen(1, down) }, []schemaflux.Kind{down, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.Yes(), schemafluxtest.Yes(), schemafluxtest.Yes())
			c.setup(d)
			for i, kind := range c.want {
				resp, err := d.Complete(ctx, decide(yn("a")))
				switch {
				case kind == 0 && (err != nil || len(resp.Answers) != 1):
					t.Fatalf("request %d: resp = %+v, err = %v", i+1, resp, err)
				case kind != 0 && !errors.Is(err, kind):
					t.Fatalf("request %d: err = %v, want kind %q", i+1, err, kind)
				}
			}
			if d.Calls() != len(c.want) {
				t.Fatalf("Calls() = %d, want %d", d.Calls(), len(c.want))
			}
		})
	}

	t.Run("a limit refusal comes before a scripted failure", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes())
		d.Fail(schemaflux.ErrBusy)
		if _, err := d.Complete(ctx, schemaflux.Request{}); !errors.Is(err, schemaflux.ErrUnsupported) {
			t.Fatalf("err = %v, want the refusal of a request with no decision", err)
		}
	})
	t.Run("the error given is the error returned", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider()
		sentinel := errors.New("down")
		d.Fail(sentinel)
		if _, err := d.Complete(ctx, decide(yn("a"))); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a context that has ended fails the request first", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes())
		d.Fail(schemaflux.ErrBusy)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := d.Complete(cctx, decide(yn("a"))); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if d.Calls() != 1 {
			t.Fatalf("Calls() = %d", d.Calls())
		}
	})
}

func TestADeciderDeclaresItsLimitsAndKeepsItsState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	limits := []schemaflux.DecisionLimits{
		{},
		{MaxQuestions: 1},
		{MaxOptions: 4},
		{MaxLevels: 10},
		{MaxStateTokens: 500},
		{MaxQuestions: 8, MaxOptions: 6, MaxLevels: 5, MaxStateTokens: 4096},
		{MaxQuestions: 1, MaxOptions: 2, MaxLevels: 3, MaxStateTokens: 4},
		{MaxQuestions: math.MaxInt},
		{MaxStateTokens: -1},
		{MaxLevels: 2, MaxOptions: 2},
	}
	for _, l := range limits {
		t.Run(fmt.Sprintf("limits %+v", l), func(t *testing.T) {
			t.Parallel()
			var d schemafluxtest.Decider
			if got := d.DecisionLimits(); got != (schemaflux.DecisionLimits{}) {
				t.Fatalf("a new decider declares %+v", got)
			}
			d.SetLimits(l)
			if got := d.DecisionLimits(); got != l {
				t.Fatalf("DecisionLimits() = %+v, want %+v", got, l)
			}
		})
	}

	t.Run("a later call replaces the limits", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.SetLimits(schemaflux.DecisionLimits{MaxQuestions: 1})
		d.SetLimits(schemaflux.DecisionLimits{})
		d.Reply(schemafluxtest.Yes(), schemafluxtest.Yes())
		if _, err := d.Complete(ctx, decide(yn("a"), yn("b"))); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the limits stay through Reset", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.SetLimits(schemaflux.DecisionLimits{MaxQuestions: 1})
		d.Reset()
		if d.DecisionLimits().MaxQuestions != 1 {
			t.Fatal("Reset removed the limits")
		}
	})
	t.Run("the name is test-decider until it is set", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		if d.Name() != "test-decider" {
			t.Fatalf("Name() = %q", d.Name())
		}
		d.SetName("mine")
		if d.Name() != "mine" {
			t.Fatalf("Name() = %q", d.Name())
		}
		d.SetName("")
		if d.Name() != "test-decider" {
			t.Fatalf("Name() = %q after an empty name", d.Name())
		}
	})
	t.Run("the name is the one a limit refusal speaks of", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		d.SetName("jev-stand-in")
		d.SetLimits(schemaflux.DecisionLimits{MaxQuestions: 1})
		_, err := d.Complete(ctx, decide(yn("a"), yn("b")))
		if err == nil || !strings.Contains(err.Error(), "jev-stand-in") {
			t.Fatalf("err = %v, want the decider's name in it", err)
		}
	})
	t.Run("the usage is reported with every answer", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(), schemafluxtest.Yes())
		d.SetUsage(schemaflux.Usage{Input: 40, Output: 2})
		for range 2 {
			resp, _ := d.Complete(ctx, decide(yn("a")))
			if resp.Usage.Input != 40 || resp.Usage.Output != 2 {
				t.Fatalf("Usage = %+v", resp.Usage)
			}
		}
	})
	t.Run("requests are recorded, refused ones too, in order", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes())
		_, _ = d.Complete(ctx, schemaflux.Request{User: "no decision"})
		_, _ = d.Complete(ctx, decide(yn("a")))
		got := d.Requests()
		if len(got) != 2 || got[0].User != "no decision" || got[1].Decision == nil || d.Calls() != 2 {
			t.Fatalf("Requests() = %+v", got)
		}
		got[0].User = "changed"
		if d.Requests()[0].User != "no decision" {
			t.Fatal("the slice is not a copy")
		}
		if d.Last().Decision == nil {
			t.Fatal("Last() is not the most recent request")
		}
	})
	t.Run("Last is the zero request when there has been none", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		if got := d.Last(); got.Decision != nil || got.User != "" {
			t.Fatalf("Last() = %+v", got)
		}
	})
	t.Run("Reset hands the answers out from the first and forgets the requests", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Level(1), schemafluxtest.Level(2))
		_, _ = d.Complete(ctx, decide(scale(1, 5)))
		d.Reset()
		resp, _ := d.Complete(ctx, decide(scale(1, 5)))
		if resp.Answers[0].Level != 1 || d.Calls() != 1 {
			t.Fatalf("answer = %+v, Calls() = %d", resp.Answers, d.Calls())
		}
	})
	t.Run("it tells the library to never retry", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		if retries, backoff := d.RetryPolicy(); retries != -1 || backoff != 0 {
			t.Fatalf("RetryPolicy() = %d, %v", retries, backoff)
		}
	})
	t.Run("it names the one model for every tier", func(t *testing.T) {
		t.Parallel()
		var d schemafluxtest.Decider
		for _, tier := range []schemaflux.Tier{0, schemaflux.TierSmart, schemaflux.TierFast, schemaflux.TierQuick, 9, -1} {
			if got := d.ModelFor(tier); got != "test-model" {
				t.Errorf("ModelFor(%d) = %q", tier, got)
			}
		}
	})
}

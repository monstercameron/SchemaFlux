package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// LAT-007 through the exported API: the decision and the branch are real
// operations against a provider that takes a set time to answer, so the waits
// and the cancellations are those of calls, not of sleeping functions.

const aheadMessage = "I was charged twice for order 7731 and I want the second charge back."

// aheadAnswer is what the provider says to a question: after how long, and
// either a body or an error.
type aheadAnswer struct {
	delay time.Duration
	body  string
	err   error
}

// aheadProvider answers each question after a set delay, and records, for each
// question it was asked, whether the call was cancelled before it answered.
type aheadProvider struct {
	answers map[string]aheadAnswer

	mu        sync.Mutex
	asked     []string
	cancelled map[string]bool
	answered  map[string]bool
}

func newAheadProvider(answers map[string]aheadAnswer) *aheadProvider {
	return &aheadProvider{answers: answers, cancelled: map[string]bool{}, answered: map[string]bool{}}
}

func (p *aheadProvider) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	var question string
	var answer aheadAnswer
	for text, candidate := range p.answers {
		if strings.Contains(req.UserPrompt, `"`+text+`"`) {
			question, answer = text, candidate
		}
	}
	p.mu.Lock()
	p.asked = append(p.asked, question)
	p.mu.Unlock()
	if question == "" {
		return schemaflux.CompletionResponse{}, errors.New("a question nobody scripted")
	}

	timer := time.NewTimer(answer.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		p.mu.Lock()
		p.cancelled[question] = true
		p.mu.Unlock()
		return schemaflux.CompletionResponse{}, ctx.Err()
	case <-timer.C:
	}
	p.mu.Lock()
	p.answered[question] = true
	p.mu.Unlock()
	if answer.err != nil {
		return schemaflux.CompletionResponse{}, answer.err
	}
	return schemaflux.CompletionResponse{Content: answer.body, Provider: "local", Model: req.Model, FinishReason: "stop"}, nil
}

func (p *aheadProvider) Name() string                                      { return "local" }
func (p *aheadProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *aheadProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func (p *aheadProvider) wasAsked(question string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, asked := range p.asked {
		if asked == question {
			return true
		}
	}
	return false
}

func (p *aheadProvider) wasCancelled(question string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancelled[question]
}

func (p *aheadProvider) wasAnswered(question string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answered[question]
}

const (
	aheadDecision = "Is the customer asking for money back?"
	aheadRefund   = "Does the order qualify for a refund?"
	aheadReply    = "Is the customer angry?"
	aheadTeam     = "Which team should handle this?"
	aheadWaitCall = 120 * time.Millisecond
)

func aheadIs(question string, text string) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		ok, err := schemaflux.Is(ctx, aheadMessage, question)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s=%v", text, ok), nil
	}
}

func aheadDecideIs(question string) schemaflux.Step[bool] {
	return func(ctx context.Context) (bool, error) { return schemaflux.Is(ctx, aheadMessage, question) }
}

// Every test below runs in a synctest bubble, where the provider's delays are
// waits on a fake clock that advances when every goroutine of the test is
// blocked. A call that takes 120 ms therefore takes exactly 120 ms, two that
// overlap take exactly one wait, and nothing depends on how busy the machine is.
func TestIntegrationBranchAheadYesIsOneWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		provider := newAheadProvider(map[string]aheadAnswer{
			aheadDecision: {delay: aheadWaitCall, body: `{"q1":true}`},
			aheadRefund:   {delay: aheadWaitCall, body: `{"q1":true}`},
			aheadReply:    {delay: aheadWaitCall, body: `{"q1":true}`},
		})
		schemaflux.NewClient("test-key").WithProviderInstance(provider)

		start := time.Now()
		got, err := schemaflux.BranchAhead(ctx, aheadDecideIs(aheadDecision), aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply"))
		ahead := time.Since(start)
		if err != nil || got != "refund=true" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		if provider.wasAsked(aheadReply) {
			t.Error("a yes asked the question of the no branch")
		}
		if provider.wasCancelled(aheadRefund) || !provider.wasAnswered(aheadRefund) {
			t.Error("the yes branch was cancelled and not kept")
		}

		// Deciding and then branching, with the same two calls, waits for both.
		start = time.Now()
		yes, err := schemaflux.Is(ctx, aheadMessage, aheadDecision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := schemaflux.Branch(ctx, yes, aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply")); err != nil {
			t.Fatal(err)
		}
		sequential := time.Since(start)
		if sequential != 2*aheadWaitCall {
			t.Fatalf("two calls in a row took %v, want %v", sequential, 2*aheadWaitCall)
		}
		if ahead != aheadWaitCall {
			t.Errorf("BranchAhead took %v, want one wait of %v (after a decision: %v)", ahead, aheadWaitCall, sequential)
		}
	})
}

func TestIntegrationBranchAheadNoCancelsTheCallInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := newAheadProvider(map[string]aheadAnswer{
			aheadDecision: {delay: 50 * time.Millisecond, body: `{"q1":false}`},
			aheadRefund:   {delay: 5 * time.Second, body: `{"q1":true}`},
			aheadReply:    {delay: 25 * time.Millisecond, body: `{"q1":false}`},
		})
		schemaflux.NewClient("test-key").WithProviderInstance(provider)

		start := time.Now()
		got, err := schemaflux.BranchAhead(context.Background(), aheadDecideIs(aheadDecision), aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply"))
		if err != nil || got != "reply=false" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		// The decision's 50 ms and then the no branch's 25 ms; the speculative
		// call, which would have taken 5 s, was cancelled and not waited for.
		if elapsed := time.Since(start); elapsed != 75*time.Millisecond {
			t.Errorf("took %v, want 75ms: the speculative call was waited for", elapsed)
		}
		if !provider.wasCancelled(aheadRefund) || provider.wasAnswered(aheadRefund) {
			t.Error("the speculative call was not cancelled while the provider held it")
		}
		if !provider.wasAnswered(aheadReply) {
			t.Error("the no branch did not run")
		}
	})
}

func TestIntegrationBranchAheadFailedDecisionReturnsItsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := newAheadProvider(map[string]aheadAnswer{
			aheadDecision: {delay: 50 * time.Millisecond, err: errors.New("provider unavailable")},
			aheadRefund:   {delay: 5 * time.Second, body: `{"q1":true}`},
			aheadReply:    {delay: 25 * time.Millisecond, body: `{"q1":false}`},
		})
		schemaflux.NewClient("test-key").WithProviderInstance(provider)

		got, err := schemaflux.BranchAhead(context.Background(), aheadDecideIs(aheadDecision), aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply"))
		if err == nil || got != "" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		if !provider.wasCancelled(aheadRefund) {
			t.Error("the speculative call was left running")
		}
		if provider.wasAsked(aheadReply) {
			t.Error("a failed decision was read as a no")
		}
	})
}

func TestIntegrationBranchAheadAnAnswerThatIsNoAnswerIsNotANo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := newAheadProvider(map[string]aheadAnswer{
			aheadDecision: {delay: 25 * time.Millisecond, body: `{}`},
			aheadRefund:   {delay: 5 * time.Second, body: `{"q1":true}`},
			aheadReply:    {delay: 25 * time.Millisecond, body: `{"q1":false}`},
		})
		schemaflux.NewClient("test-key").WithProviderInstance(provider)

		got, err := schemaflux.BranchAhead(context.Background(), aheadDecideIs(aheadDecision), aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply"))
		if err == nil || got != "" || provider.wasAsked(aheadReply) {
			t.Errorf("BranchAhead = %q, %v; no branch should run on an answer that was not given", got, err)
		}
		if !provider.wasCancelled(aheadRefund) {
			t.Error("the speculative call was left running")
		}
	})
}

func TestIntegrationBranchBothKeepsTheOneTheDecisionPicks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		decision    string
		want        string
		loser       string
		winnerAsked string
	}{
		{"yes", `{"q1":true}`, "refund=true", aheadReply, aheadRefund},
		{"no", `{"q1":false}`, "reply=true", aheadRefund, aheadReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// The loser is slow enough that it can only have been cut short.
				answers := map[string]aheadAnswer{
					aheadDecision: {delay: aheadWaitCall, body: tc.decision},
					aheadRefund:   {delay: 60 * time.Millisecond, body: `{"q1":true}`},
					aheadReply:    {delay: 60 * time.Millisecond, body: `{"q1":true}`},
				}
				answers[tc.loser] = aheadAnswer{delay: 5 * time.Second, body: `{"q1":true}`}
				provider := newAheadProvider(answers)
				schemaflux.NewClient("test-key").WithProviderInstance(provider)

				start := time.Now()
				got, err := schemaflux.BranchBoth(context.Background(), aheadDecideIs(aheadDecision), aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply"))
				if err != nil || got != tc.want {
					t.Fatalf("BranchBoth = %q, %v", got, err)
				}
				// Both branches ran during the decision's wait; the one kept was ready
				// before it and the other, which would have taken 5 s, was cancelled.
				if elapsed := time.Since(start); elapsed != aheadWaitCall {
					t.Errorf("took %v, want %v: the branch not taken was waited for", elapsed, aheadWaitCall)
				}
				if !provider.wasAsked(aheadRefund) || !provider.wasAsked(aheadReply) {
					t.Error("both branches should have been started")
				}
				if !provider.wasCancelled(tc.loser) || !provider.wasAnswered(tc.winnerAsked) {
					t.Errorf("the loser was not cancelled or the winner not answered")
				}
			})
		})
	}
}

func TestIntegrationRouteAheadStartsTheLikelyRoute(t *testing.T) {
	labels := []string{"billing", "technical", "shipping"}
	which := func(ctx context.Context) (string, error) {
		return schemaflux.Which(ctx, aheadMessage, aheadTeam, labels)
	}
	route := func(label, question string) schemaflux.Route[string] {
		return schemaflux.To(label, aheadIs(question, label))
	}
	billing, technical := "Is this a refund?", "Is this a defect?"
	routes := []schemaflux.Route[string]{route("billing", billing), route("technical", technical)}

	t.Run("the decision names it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := newAheadProvider(map[string]aheadAnswer{
				aheadTeam: {delay: aheadWaitCall, body: `{"q1":1}`},
				billing:   {delay: aheadWaitCall, body: `{"q1":true}`},
				technical: {delay: 5 * time.Second, body: `{"q1":true}`},
			})
			schemaflux.NewClient("test-key").WithProviderInstance(provider)
			start := time.Now()
			got, err := schemaflux.RouteAhead(context.Background(), which, "billing", routes...)
			if err != nil || got != "billing=true" {
				t.Fatalf("RouteAhead = %q, %v", got, err)
			}
			if elapsed := time.Since(start); elapsed != aheadWaitCall {
				t.Errorf("took %v, want one wait of %v", elapsed, aheadWaitCall)
			}
			if provider.wasAsked(technical) {
				t.Error("a route that was not likely was started")
			}
		})
	})

	t.Run("the decision names another", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := newAheadProvider(map[string]aheadAnswer{
				aheadTeam: {delay: 50 * time.Millisecond, body: `{"q1":2}`},
				billing:   {delay: 5 * time.Second, body: `{"q1":true}`},
				technical: {delay: 25 * time.Millisecond, body: `{"q1":true}`},
			})
			schemaflux.NewClient("test-key").WithProviderInstance(provider)
			got, err := schemaflux.RouteAhead(context.Background(), which, "billing", routes...)
			if err != nil || got != "technical=true" {
				t.Fatalf("RouteAhead = %q, %v", got, err)
			}
			if !provider.wasCancelled(billing) {
				t.Error("the likely route was not cancelled")
			}
		})
	})

	t.Run("the decision names a label that is no route", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := newAheadProvider(map[string]aheadAnswer{
				aheadTeam: {delay: 25 * time.Millisecond, body: `{"q1":3}`},
				billing:   {delay: 5 * time.Second, body: `{"q1":true}`},
				technical: {delay: 25 * time.Millisecond, body: `{"q1":true}`},
			})
			schemaflux.NewClient("test-key").WithProviderInstance(provider)
			got, err := schemaflux.RouteAhead(context.Background(), which, "billing", routes...)
			if err == nil || got != "" {
				t.Fatalf("RouteAhead = %q, %v", got, err)
			}
			if !provider.wasCancelled(billing) || provider.wasAsked(technical) {
				t.Error("an answer that is no route ran a route, or left the likely one running")
			}
		})
	})

	t.Run("a likely that is no label asks nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := newAheadProvider(map[string]aheadAnswer{
				aheadTeam: {delay: 25 * time.Millisecond, body: `{"q1":1}`},
			})
			schemaflux.NewClient("test-key").WithProviderInstance(provider)
			_, err := schemaflux.RouteAhead(context.Background(), which, "returns", routes...)
			if err == nil || provider.wasAsked(aheadTeam) {
				t.Errorf("err = %v, asked = %v", err, provider.wasAsked(aheadTeam))
			}
		})
	})
}

// A caller that goes away stops both calls at the provider.
func TestIntegrationBranchAheadCallerCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := newAheadProvider(map[string]aheadAnswer{
			aheadDecision: {delay: 5 * time.Second, body: `{"q1":true}`},
			aheadRefund:   {delay: 5 * time.Second, body: `{"q1":true}`},
			aheadReply:    {delay: 5 * time.Second, body: `{"q1":true}`},
		})
		schemaflux.NewClient("test-key").WithProviderInstance(provider)

		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()
		time.AfterFunc(60*time.Millisecond, cancel)
		got, err := schemaflux.BranchAhead(ctx, aheadDecideIs(aheadDecision), aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply"))
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		if elapsed := time.Since(start); elapsed != 60*time.Millisecond {
			t.Errorf("BranchAhead returned after %v, want the moment the caller went away, 60ms", elapsed)
		}
		if !provider.wasCancelled(aheadDecision) || !provider.wasCancelled(aheadRefund) {
			t.Error("a call was left with the provider after the caller went away")
		}
	})
}

// Speculation is off unless it is asked for: the ordinary combinators ask the
// questions of one branch only, after the decision.
func TestIntegrationWhetherDoesNotSpeculate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := newAheadProvider(map[string]aheadAnswer{
			aheadDecision: {delay: 25 * time.Millisecond, body: `{"q1":false}`},
			aheadRefund:   {delay: 25 * time.Millisecond, body: `{"q1":true}`},
			aheadReply:    {delay: 25 * time.Millisecond, body: `{"q1":false}`},
		})
		schemaflux.NewClient("test-key").WithProviderInstance(provider)

		start := time.Now()
		got, err := schemaflux.Whether(context.Background(), aheadMessage, aheadDecision, aheadIs(aheadRefund, "refund"), aheadIs(aheadReply, "reply"))
		if err != nil || got != "reply=false" {
			t.Fatalf("Whether = %q, %v", got, err)
		}
		// The decision and then the one branch, one after the other: 25 ms each.
		if elapsed := time.Since(start); elapsed != 50*time.Millisecond {
			t.Errorf("Whether took %v, want 50ms: the decision and then the one branch", elapsed)
		}
		if provider.wasAsked(aheadRefund) {
			t.Error("Whether started a branch before the decision")
		}
	})
}

// ExampleBranchAhead starts the likely branch while the decision is still being
// made, and keeps it when the decision agrees.
func ExampleBranchAhead() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"q1":true}`))

	message := "I was charged twice for order 7731. Please send the second charge back."
	ask := func(question string) schemaflux.Step[bool] {
		return func(ctx context.Context) (bool, error) { return schemaflux.Is(ctx, message, question) }
	}
	say := func(text string) schemaflux.Step[string] {
		return func(context.Context) (string, error) { return text, nil }
	}
	outcome, err := schemaflux.BranchAhead(context.Background(),
		ask("Is the customer asking for money back?"),
		say("refund approved"),
		say("no refund"))
	if err != nil {
		fmt.Println("could not decide:", err)
		return
	}
	fmt.Println(outcome)

	// Output:
	// refund approved
}

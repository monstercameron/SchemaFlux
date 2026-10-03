package ops

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// A deadline provider records what the context of each request looks like and
// then either blocks until it ends or answers at once.
type deadlineProvider struct {
	mu        sync.Mutex
	block     bool
	requests  int
	deadlines []time.Duration // time left on each request's context, 0 when it has none
	bounded   []bool
}

func (p *deadlineProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.requests++
	deadline, bounded := ctx.Deadline()
	p.bounded = append(p.bounded, bounded)
	if bounded {
		p.deadlines = append(p.deadlines, time.Until(deadline))
	} else {
		p.deadlines = append(p.deadlines, 0)
	}
	block := p.block
	p.mu.Unlock()
	if block {
		<-ctx.Done()
		return llm.CompletionResponse{}, ctx.Err()
	}
	return llm.CompletionResponse{}, errors.New("the test provider answers nothing")
}
func (p *deadlineProvider) Name() string                               { return "deadline" }
func (p *deadlineProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *deadlineProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// CompleteStream is a stream that blocks like Complete does.
func (p *deadlineProvider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		_, err := p.Complete(ctx, req)
		yield(llm.StreamChunk{}, err)
	}
}

func (p *deadlineProvider) first() (requests int, bounded bool, left time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.deadlines) == 0 {
		return p.requests, false, 0
	}
	return p.requests, p.bounded[0], p.deadlines[0]
}

func TestClientTimeoutBindsEveryOperation(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "timeout-model")
	for _, call := range operationCalls() {
		t.Run(call.row, func(t *testing.T) {
			provider := &deadlineProvider{block: true}
			withDefaultExec(t, &ExecConfig{Provider: provider, Timeout: 250 * time.Millisecond})

			started := time.Now()
			err := call.run(context.Background())
			if requests, _, _ := provider.first(); requests == 0 {
				t.Fatalf("the provider was never asked (%v)", err)
			}
			if err == nil && call.row == "diff" {
				// Diff compares in Go and asks the model only for a summary
				// of the difference, which it does without when the model
				// does not answer. The deadline still bound the request.
				return
			}
			if err == nil {
				t.Fatal("the call succeeded against a provider that never answers")
			}
			if kind := types.KindOf(err); kind != types.KindTimeout {
				t.Errorf("kind = %v, want a timeout (%v)", kind, err)
			}
			if elapsed := time.Since(started); elapsed > 10*time.Second {
				t.Errorf("the call took %v, the client's deadline was 250ms", elapsed)
			}
		})
	}
}

// What deadline reaches the provider when the client names none: Extract none,
// Complete and RedactLLM their own thirty seconds, and the rest what
// config.GetTimeout says. Pinned from the tree before the client's Timeout
// existed.
func TestNoClientTimeoutIsToday(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "timeout-model")
	configtest.Setenv(t, "SCHEMAFLUX_TIMEOUT", "17s")
	const configured = 17 * time.Second
	today := map[string]time.Duration{
		"extract":       0,
		"complete":      30 * time.Second,
		"completefield": 30 * time.Second,
		"redact.text":   30 * time.Second,
	}
	for _, call := range operationCalls() {
		t.Run(call.row, func(t *testing.T) {
			provider := &deadlineProvider{}
			withDefaultExec(t, &ExecConfig{Provider: provider})
			_ = call.run(context.Background())

			requests, bounded, left := provider.first()
			if requests == 0 {
				t.Skip("the call sends nothing on its way to the provider")
			}
			want, named := today[call.row]
			if !named {
				want = configured
			}
			if want == 0 {
				if bounded {
					t.Errorf("a deadline of %v reached the provider, want none", left)
				}
				return
			}
			if !bounded || left > want || left < want-5*time.Second {
				t.Errorf("deadline left at the provider = %v (bounded %v), want about %v", left, bounded, want)
			}
		})
	}
}

// A deadline the caller's context already has is the budget: the client's
// Timeout is for a call whose context has none.
func TestCallerDeadlineStillWins(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "timeout-model")
	cases := []struct {
		name   string
		caller time.Duration
		client time.Duration
		want   time.Duration // about
	}{
		{"a longer client Timeout is not applied over a shorter deadline", 2 * time.Minute, 10 * time.Second, 2 * time.Minute},
		{"a shorter client Timeout is not applied over a longer deadline", 2 * time.Minute, 1 * time.Second, 2 * time.Minute},
		{"the same", time.Minute, time.Minute, time.Minute},
		{"a short caller deadline", 3 * time.Second, 30 * time.Second, 3 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &deadlineProvider{}
			withDefaultExec(t, &ExecConfig{Provider: provider, Timeout: tc.client})
			ctx, cancel := context.WithTimeout(context.Background(), tc.caller)
			defer cancel()
			_, _ = callWithOperationContext(ctx)
			_, bounded, left := provider.first()
			if !bounded || left > tc.want || left < tc.want-5*time.Second {
				t.Errorf("deadline left = %v (bounded %v), want about %v", left, bounded, tc.want)
			}
		})
	}
}

// callWithOperationContext is what an operation does at its start and then at
// its request: derive the context, and call.
func callWithOperationContext(ctx context.Context) (string, error) {
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()
	return callLLM(ctx, "system", "user", types.OpOptions{})
}

func TestOperationContextDeadline(t *testing.T) {
	cases := []struct {
		name    string
		client  time.Duration
		site    time.Duration
		caller  time.Duration
		want    time.Duration
		unbound bool
	}{
		{"the call site's own", 0, 12 * time.Second, 0, 12 * time.Second, false},
		{"no call site and no client is the configured default", 0, 0, 0, 17 * time.Second, false},
		{"the client's beats the call site's", 5 * time.Second, 12 * time.Second, 0, 5 * time.Second, false},
		{"the client's beats a default", 5 * time.Second, 0, 0, 5 * time.Second, false},
		{"a longer client Timeout beats a shorter site", 50 * time.Second, 12 * time.Second, 0, 50 * time.Second, false},
		{"a negative client Timeout is none", -3 * time.Second, 12 * time.Second, 0, 12 * time.Second, false},
		{"the caller's deadline over the client's", 5 * time.Second, 12 * time.Second, 90 * time.Second, 90 * time.Second, false},
		{"the caller's deadline over the call site's", 0, 12 * time.Second, 90 * time.Second, 90 * time.Second, false},
		{"a negative call site is the default", 0, -time.Second, 0, 17 * time.Second, false},
		{"a tiny client Timeout", 20 * time.Millisecond, 12 * time.Second, 0, 20 * time.Millisecond, false},
	}
	configtest.Setenv(t, "SCHEMAFLUX_TIMEOUT", "17s")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDefaultExec(t, nil)
			ctx := context.Background()
			if tc.client != 0 {
				ctx = WithSharedExecConfig(ctx, &ExecConfig{Timeout: tc.client})
			}
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.caller)
				defer cancel()
			}
			derived, cancel := operationContext(ctx, tc.site)
			defer cancel()
			deadline, bounded := derived.Deadline()
			if !bounded {
				t.Fatal("the derived context has no deadline")
			}
			left := time.Until(deadline)
			slack := 2 * time.Second
			if tc.want < slack {
				slack = tc.want
			}
			if left > tc.want || left < tc.want-slack {
				t.Errorf("deadline left = %v, want about %v", left, tc.want)
			}
		})
	}
}

func TestClientDeadlineAddsNothingWhenThereIsNothingToAdd(t *testing.T) {
	withDefaultExec(t, nil)
	bare := context.Background()
	if got := testing.AllocsPerRun(200, func() {
		_, cancel := clientDeadline(bare)
		cancel()
	}); got != 0 {
		t.Errorf("clientDeadline allocates %v times with no Timeout, want 0", got)
	}

	bounded, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	withClient := WithSharedExecConfig(bounded, &ExecConfig{Timeout: time.Second})
	if got := testing.AllocsPerRun(200, func() {
		_, cancel := clientDeadline(withClient)
		cancel()
	}); got != 0 {
		t.Errorf("clientDeadline allocates %v times when the context has a deadline, want 0", got)
	}
	derived, cancelDerived := clientDeadline(withClient)
	defer cancelDerived()
	if derived != withClient {
		t.Error("a bounded context was replaced")
	}

	t.Run("with a Timeout and no deadline it makes one", func(t *testing.T) {
		ctx, cancel := clientDeadline(WithSharedExecConfig(context.Background(), &ExecConfig{Timeout: 50 * time.Millisecond}))
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 50*time.Millisecond {
			t.Errorf("deadline %v, %v", deadline, ok)
		}
	})
	t.Run("a nil context is returned", func(t *testing.T) {
		//lint:ignore SA1012 the nil context is what is tested.
		ctx, cancel := clientDeadline(nil)
		defer cancel()
		if ctx != nil {
			t.Error("a nil context became something")
		}
	})
}

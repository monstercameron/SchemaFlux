package ops

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The client's settings on its snapshot reach the call: how many times a
// request that failed in transit is sent again, how many times a refused answer
// is sent back, and whether the fixed reinforcement block is left off the
// system prompt.

// failingProvider fails every request in transit and counts them.
type failingProvider struct {
	mu     sync.Mutex
	policy int
	calls  int
}

func (p *failingProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return llm.CompletionResponse{}, fmt.Errorf("rate limit exceeded: status 429")
}
func (p *failingProvider) Name() string                               { return "failing" }
func (p *failingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *failingProvider) RetryPolicy() (int, time.Duration)          { return p.policy, time.Microsecond }
func (p *failingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestClientRetriesOverrideTheProvider(t *testing.T) {
	cases := []struct {
		name     string
		provider int // the provider's own retries
		client   int // the snapshot's Retries
		wantSent int
	}{
		{"zero keeps the provider's two", 2, 0, 3},
		{"zero keeps the provider's none", 0, 0, 1},
		{"two on a provider that has none gives three requests", 0, 2, 3},
		{"two on a provider that has two gives three requests", 2, 2, 3},
		{"one beats the provider's three", 3, 1, 2},
		{"five beats the provider's one", 1, 5, 6},
		{"minus one is never, over the provider's two", 2, -1, 1},
		{"minus one is never, over the provider's none", 0, -1, 1},
		{"minus nine is never as well", 3, -9, 1},
		{"a snapshot of three on a provider of three", 3, 3, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "retry-model")
			provider := &failingProvider{policy: tc.provider}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Retries: tc.client})
			if _, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{}); err == nil {
				t.Fatal("the call succeeded against a provider that always fails")
			}
			if got := provider.count(); got != tc.wantSent {
				t.Errorf("%d requests sent, want %d", got, tc.wantSent)
			}
		})
	}

	t.Run("no snapshot is the provider's policy", func(t *testing.T) {
		configtest.Setenv(t, "SCHEMAFLUX_MODEL", "retry-model")
		provider := &failingProvider{policy: 2}
		_, _ = CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})
		if got := provider.count(); got != 3 {
			t.Errorf("%d requests sent, want 3", got)
		}
	})
}

func TestRepairBudgetPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		descriptor int
		client     int
		want       int
	}{
		{"the descriptor's own beats the client's", 3, 2, 3},
		{"the descriptor's own beats a client that says never", 3, -1, 3},
		{"the descriptor's own beats no client setting", 3, 0, 3},
		{"a descriptor that says never beats the client's", -1, 2, 0},
		{"a descriptor that says never beats the default", -1, 0, 0},
		{"the client's when the descriptor has none", 0, 2, 2},
		{"the client's never when the descriptor has none", 0, -1, 0},
		{"the default when neither has an opinion", 0, 0, DefaultRepairAttempts},
		{"a descriptor of one over a client of five", 1, 5, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDefaultExec(t, nil)
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Repairs: tc.client})
			if got := repairAttempts(ctx, RepairPolicy{Attempts: tc.descriptor}); got != tc.want {
				t.Errorf("repairAttempts = %d, want %d", got, tc.want)
			}
		})
	}
	t.Run("a context with no snapshot falls to the default", func(t *testing.T) {
		withDefaultExec(t, nil)
		if got := repairAttempts(context.Background(), RepairPolicy{}); got != DefaultRepairAttempts {
			t.Errorf("repairAttempts = %d, want %d", got, DefaultRepairAttempts)
		}
	})
	t.Run("the default snapshot's setting binds a bare context", func(t *testing.T) {
		withDefaultExec(t, &ExecConfig{Repairs: 4})
		if got := repairAttempts(context.Background(), RepairPolicy{}); got != 4 {
			t.Errorf("repairAttempts = %d, want 4", got)
		}
	})
}

// A client's Repairs reach the loop that makes the request: with one repair
// and then a good answer the call is three requests when the client says two,
// and a refused answer is final when it says never.
func TestClientRepairsBoundTheRepairLoop(t *testing.T) {
	cases := []struct {
		name    string
		repairs int
		wantErr bool
		wantReq int
	}{
		{"never", -1, true, 1},
		{"one", 1, true, 2},
		{"two lets the third answer be the good one", 2, false, 3},
		{"five is not used up by a third answer that is good", 5, false, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "repair-model")
			sent := 0
			provider := &answerScript{answer: func(int) string {
				sent++
				if sent < 3 {
					return "not json"
				}
				return `{"ok":true}`
			}}
			ctx := WithSharedExecConfig(WithProvider(context.Background(), provider), &ExecConfig{Repairs: tc.repairs})
			_, _, err := withRepair(ctx, "system", "user", types.OpOptions{}, RepairPolicy{}, func(body string) error {
				if !strings.Contains(body, "ok") {
					return types.Newf(types.KindMalformedOutput, "test", "not an object")
				}
				return nil
			})
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want error %v", err, tc.wantErr)
			}
			if sent != tc.wantReq {
				t.Errorf("%d requests, want %d", sent, tc.wantReq)
			}
		})
	}
}

// answerScript answers each request from a function of its number and
// remembers the last request, whole.
type answerScript struct {
	mu     sync.Mutex
	n      int
	last   llm.CompletionRequest
	answer func(n int) string
}

func (p *answerScript) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	p.last = req
	return llm.CompletionResponse{Content: p.answer(p.n), Provider: "scripted", Model: req.Model, FinishReason: "stop"}, nil
}
func (p *answerScript) Name() string                               { return "scripted" }
func (p *answerScript) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *answerScript) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// CompleteStream answers a stream with the same text, in one piece.
func (p *answerScript) CompleteStream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	p.mu.Lock()
	p.n++
	p.last = req
	text := p.answer(p.n)
	p.mu.Unlock()
	return func(yield func(llm.StreamChunk, error) bool) {
		if !yield(llm.StreamChunk{Delta: text}, nil) {
			return
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: text, Provider: "scripted", Model: req.Model, FinishReason: "stop"}}, nil)
	}
}

func (p *answerScript) request() llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func TestOmitReinforcementOnTheSnapshot(t *testing.T) {
	const operationPrompt = "  Summarize the text.  "
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "reinforcement-model")
	withDefaultExec(t, nil)

	cases := []struct {
		name       string
		omit       bool
		format     string
		wantPrefix bool
	}{
		{"on by default, text", false, "text", true},
		{"on by default, json", false, "json", true},
		{"omitted, text", true, "text", false},
		{"omitted, json", true, "json", false},
	}
	for _, tc := range cases {
		t.Run("function: "+tc.name, func(t *testing.T) {
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{OmitReinforcement: tc.omit})
			got := strengthenSystemPrompt(ctx, operationPrompt, tc.format)
			if has := strings.Contains(got, "Perform the semantic task faithfully"); has != tc.wantPrefix {
				t.Errorf("block present = %v, want %v", has, tc.wantPrefix)
			}
			if !tc.wantPrefix && got != strings.TrimSpace(operationPrompt) {
				t.Errorf("system prompt = %q, want the operation's own, trimmed", got)
			}
		})
		t.Run("buffered: "+tc.name, func(t *testing.T) {
			provider := &answerScript{answer: func(int) string { return `{"ok":true}` }}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{OmitReinforcement: tc.omit})
			system := operationPrompt
			if tc.format == "json" {
				system = "Return only valid JSON."
			}
			if _, err := CallLLM(ctx, provider, system, "user", types.OpOptions{}); err != nil {
				t.Fatal(err)
			}
			sent := provider.request().SystemPrompt
			if has := strings.Contains(sent, "Perform the semantic task faithfully"); has != tc.wantPrefix {
				t.Errorf("block present in the request = %v, want %v", has, tc.wantPrefix)
			}
			if !tc.wantPrefix && sent != strings.TrimSpace(system) {
				t.Errorf("request system prompt = %q, want the operation's own", sent)
			}
		})
	}

	for _, omit := range []bool{false, true} {
		t.Run(fmt.Sprintf("streamed, omitted=%v", omit), func(t *testing.T) {
			provider := &answerScript{answer: func(int) string { return "a summary" }}
			ctx := WithSharedExecConfig(WithProvider(context.Background(), provider), &ExecConfig{OmitReinforcement: omit})
			options := NewSummarizeOptions()
			options.CommonOptions.Context = ctx
			stream, err := StreamSummarize("some text", options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Text(); err != nil {
				t.Fatal(err)
			}
			sent := provider.request().SystemPrompt
			if has := strings.Contains(sent, "Perform the semantic task faithfully"); has == omit {
				t.Errorf("block present in the streamed request = %v with omit=%v", has, omit)
			}
			if omit && sent != strings.TrimSpace(summarizeSystemPrompt) {
				t.Errorf("streamed system prompt = %q, want the operation's own text", sent)
			}
		})
	}

	t.Run("the default snapshot's setting binds a bare context", func(t *testing.T) {
		withDefaultExec(t, &ExecConfig{OmitReinforcement: true})
		if got := strengthenSystemPrompt(context.Background(), "plain", "text"); got != "plain" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("the environment still turns it off beside a client that does not", func(t *testing.T) {
		configtest.Setenv(t, promptReinforcementEnvVar, "0")
		ctx := WithSharedExecConfig(context.Background(), &ExecConfig{})
		if got := strengthenSystemPrompt(ctx, "plain", "text"); got != "plain" {
			t.Errorf("got %q", got)
		}
	})
}

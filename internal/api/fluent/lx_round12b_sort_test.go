package fluent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Review round 12, the envelope a builder's sort returns.

// bite12Provider answers a sort without a network: a whole-list request gets
// ids, a per-item scoring request gets score, and every call takes delay.
type bite12Provider struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	ids   string
	score string
	err   error
}

func (p *bite12Provider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
	if p.err != nil {
		return llm.CompletionResponse{}, p.err
	}
	content := p.ids
	if strings.Contains(req.SystemPrompt, "rank_score") {
		content = p.score
	}
	return llm.CompletionResponse{Content: content, Model: req.Model, Provider: "local", FinishReason: "stop"}, nil
}

func (p *bite12Provider) Name() string                               { return "local" }
func (p *bite12Provider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *bite12Provider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func (p *bite12Provider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func bite12Items(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("item-%03d", i)
	}
	return items
}

// A builder's sort returns the sort's own envelope, and every path through it
// carries the ids the caller set. Options that do not validate are refused in
// the builder before the operation is reached and came back with an empty
// envelope; a floor refusal, a sort of fewer than two items and a sort that
// scored every item lost the ids, or the time, in the operation.
func TestBite12SortRunResultEnvelope(t *testing.T) {
	const orderTwo = `{"ids":["i-000002","i-000001"]}`
	cases := []struct {
		name         string
		items        []string
		provider     *bite12Provider
		build        func(SortRequest[string]) SortRequest[string]
		floor        types.ContractLevel
		wantErr      bool
		wantStrategy string
		wantCalls    bool
		wantFirst    string
	}{
		{"a whole-list answer", bite12Items(2), &bite12Provider{ids: orderTwo}, nil, 0, false, "whole-list", true, "item-001"},
		{"descending", bite12Items(2), &bite12Provider{ids: orderTwo}, func(r SortRequest[string]) SortRequest[string] { return r.Desc() }, 0, false, "whole-list", true, "item-001"},
		{"a floor the sort cannot meet", bite12Items(2), &bite12Provider{ids: orderTwo}, nil, types.ContractFullyGoverned, true, "", false, ""},
		{"no criteria", bite12Items(2), &bite12Provider{ids: orderTwo}, func(r SortRequest[string]) SortRequest[string] { return r.By("") }, 0, true, "", false, ""},
		{"a direction that is not one", bite12Items(2), &bite12Provider{ids: orderTwo}, func(r SortRequest[string]) SortRequest[string] {
			return r.Configure(func(o SortOptions) SortOptions { o.Direction = "sideways"; return o })
		}, 0, true, "", false, ""},
		{"no items", nil, &bite12Provider{ids: orderTwo}, nil, 0, false, "trivial", false, ""},
		{"one item", bite12Items(1), &bite12Provider{ids: orderTwo}, nil, 0, false, "trivial", false, "item-000"},
		{"a provider that fails", bite12Items(2), &bite12Provider{err: errors.New("the provider is down")}, nil, 0, true, "", true, ""},
		{"every item scored", bite12Items(51), &bite12Provider{score: `{"rank_score":0.5}`}, nil, 0, false, "scoring", true, "item-000"},
		{"a fallback to scoring", bite12Items(3), &bite12Provider{ids: `{"ids":["i-000001","i-000001","i-000003"]}`, score: `{"rank_score":0.5}`}, nil, 0, false, "scoring-fallback", true, "item-000"},
		{"an answer that is neither", bite12Items(3), &bite12Provider{ids: "not JSON", score: "not JSON"}, nil, 0, true, "scoring-fallback", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ops.WithProvider(context.Background(), tc.provider)
			if tc.floor != 0 {
				ctx = ops.WithContractPolicy(ctx, types.DataPolicy{MinimumContract: tc.floor})
			}
			request := Sorting(tc.items).By("alphabetically").Model("gpt-6-luna").RequestID("req-1").CorrelationID("corr-1")
			if tc.build != nil {
				request = tc.build(request)
			}

			result, err := request.RunResult(ctx)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error = %v", err, tc.wantErr)
			}
			meta := result.Meta
			if meta.RequestID != "req-1" || meta.CorrelationID != "corr-1" || meta.Operation != "sort" {
				t.Errorf("the envelope does not carry the request: %+v", meta)
			}
			if tc.wantStrategy != "" && meta.Strategy != tc.wantStrategy {
				t.Errorf("Strategy = %q, want %q", meta.Strategy, tc.wantStrategy)
			}
			if made := tc.provider.callCount() > 0; made != tc.wantCalls {
				t.Errorf("the provider was called = %v, want %v", made, tc.wantCalls)
			}
			if tc.wantErr {
				if result.Value != nil {
					t.Errorf("a value came back beside the error: %v", result.Value)
				}
				return
			}
			if len(tc.items) > 0 && (len(result.Value) != len(tc.items) || result.Value[0] != tc.wantFirst) {
				t.Errorf("Value starts with %v, want %q first", result.Value, tc.wantFirst)
			}
		})
	}
}

// A sort that sends nothing has made no attempt, and a sort that scores every
// item took as long as all of its calls, not as long as the last of them.
func TestBite12SortRunResultCountsAndTimesTheWholeSort(t *testing.T) {
	trivial, err := Sorting(bite12Items(1)).By("alphabetically").RunResult(ops.WithProvider(context.Background(), &bite12Provider{}))
	if err != nil || trivial.Meta.Attempts != 0 {
		t.Errorf("err %v, Attempts = %d for a sort that sent nothing", err, trivial.Meta.Attempts)
	}

	const delay = 10 * time.Millisecond
	items := bite12Items(51)
	rounds := (len(items) + ops.DefaultConcurrency - 1) / ops.DefaultConcurrency
	provider := &bite12Provider{score: `{"rank_score":0.5}`, delay: delay}
	result, err := Sorting(items).By("alphabetically").Model("gpt-6-luna").RunResult(ops.WithProvider(context.Background(), provider))
	if err != nil {
		t.Fatalf("RunResult: %v", err)
	}
	if want := time.Duration(rounds) * delay; result.Meta.Elapsed < want {
		t.Errorf("Elapsed = %v, want at least %v for %d calls of %v, %d at a time", result.Meta.Elapsed, want, len(items), delay, ops.DefaultConcurrency)
	}
	if result.Meta.Attempts < len(items) {
		t.Errorf("Attempts = %d, want at least %d", result.Meta.Attempts, len(items))
	}
}

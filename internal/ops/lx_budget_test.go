package ops

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// answersProvider returns a fixed sequence of responses, one per call, and
// then repeats the last.
type answersProvider struct {
	responses []llm.CompletionResponse
	calls     int
	retries   int
}

func (p *answersProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	index := p.calls
	if index >= len(p.responses) {
		index = len(p.responses) - 1
	}
	p.calls++
	return p.responses[index], nil
}
func (p *answersProvider) Name() string                               { return "openai" }
func (p *answersProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *answersProvider) RetryPolicy() (int, time.Duration)          { return p.retries, time.Nanosecond }

// A client's budget is charged for what its calls produced, whether or not the
// answer could be used. A truncated answer is tokens generated and billed; the
// buffered path recorded nothing for it, while a truncated stream was charged.
// And an answer that reports no usage is spend of unknown size, not a call
// that cost nothing: it was recorded as priced at zero.
func TestTheClientBudgetIsChargedForWhatACallProduced(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	good := func(u types.TokenUsage) llm.CompletionResponse {
		return llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai", Usage: u}
	}
	truncated := func(u types.TokenUsage) llm.CompletionResponse {
		return llm.CompletionResponse{Content: `{"ok":tr`, FinishReason: "length", Model: "gpt-6-luna", Provider: "openai", Usage: u}
	}
	empty := func(u types.TokenUsage) llm.CompletionResponse {
		return llm.CompletionResponse{Content: "", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai", Usage: u}
	}

	cases := []struct {
		name      string
		responses []llm.CompletionResponse
		retries   int
		wantErr   bool
		want      []bool // the priced flag of each record, in order
	}{
		{"an answer with usage", []llm.CompletionResponse{good(usage)}, 0, false, []bool{true}},
		{"an answer that reports no usage", []llm.CompletionResponse{good(types.TokenUsage{})}, 0, false, []bool{false}},
		{"a truncated answer with usage", []llm.CompletionResponse{truncated(usage)}, 0, true, []bool{true}},
		{"a truncated answer that reports no usage", []llm.CompletionResponse{truncated(types.TokenUsage{})}, 0, true, []bool{false}},
		{"an empty answer with usage", []llm.CompletionResponse{empty(usage)}, 0, true, []bool{true}},
		{"an unusable answer and then a good one", []llm.CompletionResponse{empty(usage), good(usage)}, 2, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &answersProvider{responses: tc.responses, retries: tc.retries}
			budget := &recordingBudget{}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Budget: budget})

			_, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}

			got := budget.seen()
			if tc.want == nil {
				// One record per answer the provider produced, all priced.
				if len(got) != provider.calls {
					t.Fatalf("%d records for %d provider calls: %v", len(got), provider.calls, got)
				}
				for _, priced := range got {
					if !priced {
						t.Fatalf("a call that reported usage was recorded unpriced: %v", got)
					}
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the budget was told %v (priced flags), want %v", got, tc.want)
			}
		})
	}

	// With no client budget there is nothing to record, and nothing breaks.
	provider := &answersProvider{responses: []llm.CompletionResponse{truncated(usage)}}
	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json"}); err == nil {
		t.Error("a truncated answer was accepted")
	}
}

// The same rule on a stream that ends cleanly and reports no usage.
func TestACleanStreamThatReportsNoUsageIsRecordedAsUnknownSpend(t *testing.T) {
	withStreamingProvider(t, &namedStreamProvider{name: "local", seq: func(yield func(llm.StreamChunk, error) bool) {
		if yield(llm.StreamChunk{Delta: "all of it"}, nil) {
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "all of it", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai"}}, nil)
		}
	}})
	budget := &recordingBudget{}

	opts := NewSummarizeOptions()
	opts.OpOptions.Context = WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
	stream, err := StreamSummarize("some input", opts)
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	for range stream.All() {
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(budget.seen()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := budget.seen(); !reflect.DeepEqual(got, []bool{false}) {
		t.Errorf("the budget was told %v, want one record of unknown size", got)
	}
}

package ops

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// floodProvider streams deltas for as long as its context lasts, which is what
// fills a stream nobody is reading.
type floodProvider struct{ started chan struct{} }

func (p *floodProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errors.New("Complete must not be called by the streaming path")
}
func (p *floodProvider) Name() string                               { return "openai" }
func (p *floodProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *floodProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *floodProvider) CompleteStream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		close(p.started)
		for ctx.Err() == nil {
			if !yield(llm.StreamChunk{Delta: "more "}, nil) {
				return
			}
		}
		yield(llm.StreamChunk{}, ctx.Err())
	}
}

var lx126TextOptions = types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "text"}

// Review round 9. A stream nobody reads gives its slot back when its scheduler
// is closed. The producer waited on the stream's own context, which Close does
// not touch, so with the buffer full it held the slot until the caller's
// deadline, and Close waited that long with it.
func TestAnUnreadStreamIsStoppedWhenItsSchedulerCloses(t *testing.T) {
	provider := &floodProvider{started: make(chan struct{})}
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	ctx, cancel := context.WithTimeout(WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler}), time.Hour)
	defer cancel()

	stream, err := streamWith(ctx, provider, lx126TextOptions)
	if err != nil {
		t.Fatalf("the stream: %v", err)
	}
	<-provider.started
	// Nobody reads. The buffer fills and the producer is stuck sending.
	waitFor(t, func() bool { return len(stream.items) == cap(stream.items) })

	closed := make(chan error, 1)
	go func() { closed <- scheduler.Close(context.Background()) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited on a stream nobody is reading")
	}
	if stats := scheduler.Stats(); stats.InFlight != 0 {
		t.Errorf("the stream still holds its slot: %+v", stats)
	}

	// A reader who comes back is told what happened, and not that the stream
	// finished.
	text, err := stream.Text()
	if !errors.Is(err, types.ErrShutdown) {
		t.Errorf("Text = %q, %v; want a shutdown", text, err)
	}
}

// A stream that was stopped before it finished says so. Text returned an empty
// string and no error for one, which is what an answer of nothing looks like.
func TestAStreamThatWasStoppedDoesNotReadAsFinished(t *testing.T) {
	cases := []struct {
		name string
		stop func(cancel context.CancelFunc, stream *TextStream)
		want error
	}{
		{"cancelled through the stream", func(_ context.CancelFunc, stream *TextStream) { stream.Cancel() }, types.ErrCanceled},
		{"the caller's context cancelled", func(cancel context.CancelFunc, _ *TextStream) { cancel() }, types.ErrCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &floodProvider{started: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stream, err := streamWith(ctx, provider, lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			<-provider.started
			tc.stop(cancel, stream)

			text, err := stream.Text()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Text = %d characters, err %v; want %v", len(text), err, tc.want)
			}
			if text != "" {
				t.Errorf("a stopped stream returned %d characters of text with its error", len(text))
			}
			if !errors.Is(stream.Err(), tc.want) {
				t.Errorf("Err = %v after the stream was read, want %v", stream.Err(), tc.want)
			}
		})
	}

	t.Run("a deadline that passes", func(t *testing.T) {
		// The deadline passes when the test says so, after the stream is open,
		// and not after a length of time that opening it had to beat.
		provider := &floodProvider{started: make(chan struct{})}
		ctx := strm12NewDeadline()
		stream, err := streamWith(ctx, provider, lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		t.Cleanup(stream.Cancel)
		<-provider.started
		ctx.pass()
		if _, err := stream.Text(); !errors.Is(err, types.ErrTimeout) {
			t.Fatalf("err = %v, want a timeout", err)
		}
	})
}

func oneChunkStream(resp llm.CompletionResponse) *namedStreamProvider {
	return &namedStreamProvider{name: "openai", seq: func(yield func(llm.StreamChunk, error) bool) {
		if yield(llm.StreamChunk{Delta: resp.Content}, nil) {
			yield(llm.StreamChunk{Done: true, Response: resp}, nil)
		}
	}}
}

// The buffered path's accounting rules, on a stream: an answer that was
// produced and refused is on the process-wide ledger, an answer from a cache is
// on neither ledger, and an answer from a provider the client's policy forbids
// is refused.
func TestAStreamFollowsTheBufferedPathsAccounting(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	good := llm.CompletionResponse{Content: "all of it", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai", Usage: usage}
	truncated := good
	truncated.FinishReason = "length"
	cached := good
	cached.Cached = true
	noUsage := truncated
	noUsage.Usage = types.TokenUsage{}

	run := func(t *testing.T, resp llm.CompletionResponse) (grew float64, budget *recordingBudget, err error) {
		t.Helper()
		budget = &recordingBudget{}
		ctx := WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
		since := time.Now().Add(-time.Millisecond)
		before := pricing.GetTotalCost(since, nil)
		stream, openErr := streamWith(ctx, oneChunkStream(resp), lx126TextOptions)
		if openErr != nil {
			t.Fatalf("the stream: %v", openErr)
		}
		_, err = stream.Text()
		// The producer records what the stream cost after it has delivered the
		// last item, so reading the answer is not the end of its accounting. Wait
		// for it to return.
		strm12AwaitProducer(t, stream)
		return pricing.GetTotalCost(since, nil) - before, budget, err
	}

	one, _, err := run(t, good)
	if err != nil || one <= 0 {
		t.Fatalf("a clean stream: err %v, process ledger grew by %v", err, one)
	}

	cases := []struct {
		name    string
		resp    llm.CompletionResponse
		wantErr bool
		grew    float64
		records int
	}{
		{"a clean stream", good, false, one, 1},
		{"a truncated stream", truncated, true, one, 1},
		{"a truncated stream that reports no usage", noUsage, true, 0, 1},
		{"a stream served from a cache", cached, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grew, budget, err := run(t, tc.resp)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if diff := grew - tc.grew; diff > one/1000 || diff < -one/1000 {
				t.Errorf("the process ledger grew by %v, want %v", grew, tc.grew)
			}
			if got := len(budget.seen()); got != tc.records {
				t.Errorf("the client's budget was told %d times, want %d", got, tc.records)
			}
		})
	}

	t.Run("an answer from a provider the policy forbids", func(t *testing.T) {
		llm.ResetCapabilityRegistryForTest()
		t.Cleanup(llm.ResetCapabilityRegistryForTest)
		llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna", Regions: []string{"eu"}})
		ctx := declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}})

		elsewhere := good
		elsewhere.Provider = "anthropic"
		stream, err := streamWith(ctx, oneChunkStream(elsewhere), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		text, err := stream.Text()
		if !errors.Is(err, types.ErrPolicyViolation) || text != "" {
			t.Fatalf("Text = %q, %v; want a policy refusal and no text", text, err)
		}
		if strings.Contains(err.Error(), "all of it") {
			t.Errorf("the refusal carries the answer: %v", err)
		}
	})
}

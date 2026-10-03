package tests

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// endingStreamProvider streams "one ", "two " and then waits for hold to close
// before it finishes, or for its context to end.
type endingStreamProvider struct {
	hold chan struct{}
	err  error // the failure after the second delta, when set
}

func (p *endingStreamProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errors.New("Complete is not the streaming path")
}
func (*endingStreamProvider) Name() string                               { return "ending-stream" }
func (*endingStreamProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (*endingStreamProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *endingStreamProvider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		for _, delta := range []string{"one ", "two "} {
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
		}
		if p.err != nil {
			yield(llm.StreamChunk{}, p.err)
			return
		}
		select {
		case <-p.hold:
		case <-ctx.Done():
			yield(llm.StreamChunk{}, ctx.Err())
			return
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
			Content: "one two ", Model: req.Model, Provider: "ending-stream", FinishReason: "stop",
			Usage: schemaflux.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
		}}, nil)
	}
}

// openEndingStream opens StreamSummarize on a client of its own, as the old
// root's callers do.
func openEndingStream(t *testing.T, provider *endingStreamProvider, ctx context.Context) *schemaflux.TextStream {
	t.Helper()
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	previous := schemaflux.GetDefaultClient()
	t.Cleanup(func() { schemaflux.SetDefaultClient(previous) })
	client := schemaflux.NewClient("k").WithProviderInstance(provider).WithModel("ending-model")
	opts := schemaflux.NewSummarizeOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(client.Context(ctx))
	stream, err := schemaflux.StreamSummarize("some text to summarise", opts)
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	return stream
}

// The end of a stream is observable the way it always was, through the old
// root's TextStream: what a reader is told, and what Outcome, Ended and Err say
// beside it, for every way the stream can end. The end callback that version 2
// installs is not reachable from here, and a stream opened through the old root
// carries none.
func TestIntegrationStreamOutcomeThroughTheOldRoot(t *testing.T) {
	t.Run("read to the end", func(t *testing.T) {
		hold := make(chan struct{})
		close(hold)
		stream := openEndingStream(t, &endingStreamProvider{hold: hold}, context.Background())
		text, err := stream.Text()
		if err != nil || text != "one two " {
			t.Fatalf("Text = %q, %v", text, err)
		}
		if ended, err := stream.Outcome(); !ended || err != nil {
			t.Errorf("Outcome = %v, %v, want an ended, clean stream", ended, err)
		}
	})

	t.Run("the provider fails midway", func(t *testing.T) {
		failure := types.Newf(types.KindProviderUnavailable, "ending-stream", "the connection dropped")
		stream := openEndingStream(t, &endingStreamProvider{err: failure}, context.Background())
		text, err := stream.Text()
		if text != "" || !errors.Is(err, failure) {
			t.Fatalf("Text = %q, %v", text, err)
		}
		if ended, got := stream.Outcome(); !ended || !errors.Is(got, failure) || stream.Err() != got {
			t.Errorf("Outcome = %v, %v", ended, got)
		}
	})

	t.Run("the reader stops early", func(t *testing.T) {
		stream := openEndingStream(t, &endingStreamProvider{hold: make(chan struct{})}, context.Background())
		for range stream.All() {
			break
		}
		ended, err := stream.Outcome()
		if !ended || !errors.Is(err, context.Canceled) {
			t.Errorf("Outcome = %v, %v, want an ended, cancelled stream", ended, err)
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		stream := openEndingStream(t, &endingStreamProvider{hold: make(chan struct{})}, context.Background())
		stream.Cancel()
		if ended, err := stream.Outcome(); !ended || !errors.Is(err, context.Canceled) {
			t.Errorf("Outcome = %v, %v", ended, err)
		}
	})

	t.Run("the context ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		stream := openEndingStream(t, &endingStreamProvider{hold: make(chan struct{})}, ctx)
		cancel()
		_, err := stream.Text()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Text = %v, want a cancellation", err)
		}
		if ended, got := stream.Outcome(); !ended || !errors.Is(got, context.Canceled) {
			t.Errorf("Outcome = %v, %v", ended, got)
		}
	})
}

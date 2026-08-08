package tests

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// This file covers the streaming wrappers: StreamSummarize, StreamRewrite,
// StreamTranslate, and StreamExpand.

// fakeStreamingProvider implements llm.StreamingProvider so the streaming
// wrappers can be exercised on a real success path with no network and no
// credential -- schemafluxtest's and testfixtures' doubles only implement the
// buffered llm.Provider, which is enough to prove StreamSummarize et al.
// return the "provider does not support streaming" capability error, but not
// enough to prove a stream actually delivers text.
type fakeStreamingProvider struct {
	deltas []string
}

func (p *fakeStreamingProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: strings.Join(p.deltas, ""), Model: req.Model, Provider: p.Name(), FinishReason: "stop"}, nil
}
func (p *fakeStreamingProvider) Name() string                               { return "local" }
func (p *fakeStreamingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *fakeStreamingProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *fakeStreamingProvider) CompleteStream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		var full strings.Builder
		for _, d := range p.deltas {
			full.WriteString(d)
			if !yield(llm.StreamChunk{Delta: d}, nil) {
				return
			}
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
			Content: full.String(), Model: req.Model, Provider: p.Name(), FinishReason: "stop",
		}}, nil)
	}
}

func installFakeStreaming(t *testing.T, deltas ...string) {
	t.Helper()
	t.Setenv("SCHEMAFLUX_API_KEY", "schemafluxtest-not-a-real-key")
	previous := schemaflux.GetDefaultClient()
	client := schemaflux.NewClient("schemafluxtest-not-a-real-key").WithProviderInstance(&fakeStreamingProvider{deltas: deltas})
	schemaflux.SetDefaultClient(client)
	t.Cleanup(func() {
		if previous != nil {
			schemaflux.SetDefaultClient(previous)
		} else {
			schemaflux.SetDefaultClient(nil)
		}
	})
}

func drainStream(t *testing.T, stream *schemaflux.TextStream) string {
	t.Helper()
	var got strings.Builder
	for delta, err := range stream.All() {
		if err != nil {
			t.Fatalf("stream delta error: %v", err)
		}
		got.WriteString(delta)
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream.Err(): %v", err)
	}
	return got.String()
}

func TestStreamSummarizeDeliversDeltasFromTheProvider(t *testing.T) {
	installFakeStreaming(t, "a summary ", "of the text.")

	stream, err := schemaflux.StreamSummarize("a long passage", schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	got := drainStream(t, stream)
	if got != "a summary of the text." {
		t.Errorf("got %q, want the concatenated scripted deltas", got)
	}
}

func TestStreamRewriteDeliversDeltasFromTheProvider(t *testing.T) {
	installFakeStreaming(t, "a rewritten ", "sentence.")

	stream, err := schemaflux.StreamRewrite("a sentence", schemaflux.NewRewriteOptions())
	if err != nil {
		t.Fatalf("StreamRewrite: %v", err)
	}
	got := drainStream(t, stream)
	if got != "a rewritten sentence." {
		t.Errorf("got %q, want the concatenated scripted deltas", got)
	}
}

func TestStreamTranslateDeliversDeltasFromTheProvider(t *testing.T) {
	installFakeStreaming(t, "bonjour ", "le monde")

	stream, err := schemaflux.StreamTranslate("hello world", schemaflux.NewTranslateOptions().WithTargetLanguage("fr"))
	if err != nil {
		t.Fatalf("StreamTranslate: %v", err)
	}
	got := drainStream(t, stream)
	if got != "bonjour le monde" {
		t.Errorf("got %q, want the concatenated scripted deltas", got)
	}
}

func TestStreamExpandDeliversDeltasFromTheProvider(t *testing.T) {
	installFakeStreaming(t, "a much ", "longer passage.")

	stream, err := schemaflux.StreamExpand("brief text", schemaflux.NewExpandOptions())
	if err != nil {
		t.Fatalf("StreamExpand: %v", err)
	}
	got := drainStream(t, stream)
	if got != "a much longer passage." {
		t.Errorf("got %q, want the concatenated scripted deltas", got)
	}
}

// A provider that cannot stream must fail with an unsupported-capability
// error, not silently fall back to a one-shot buffered call dressed up as a
// stream.
func TestStreamSummarizeOnANonStreamingProviderIsUnsupported(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "irrelevant", nil)

	_, err := schemaflux.StreamSummarize("a long passage", schemaflux.NewSummarizeOptions())
	if err == nil {
		t.Fatal("expected an unsupported-capability error for a non-streaming provider")
	}
	var opErr *schemaflux.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("err = %v (%T), want an *OperationError", err, err)
	}
	if opErr.Kind != schemaflux.KindOf(err) {
		t.Fatalf("KindOf did not recover the same kind carried by the error")
	}
}

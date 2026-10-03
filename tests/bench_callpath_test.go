package tests

import (
	"context"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// What one call costs between the caller and the network.
//
// The benchmarks that existed before these stub the library's own caller, so
// what they measure stops short of the logging, the environment, the options,
// the pricing and the provider's request. These go through the exported
// functions with a provider that answers at once: everything this library does
// around a call is in the figure, and nothing else is.
//
// Run with standard error discarded, since at its default settings the library
// writes a line or more to it for every call:
//
//	go test ./tests/ -run xxx -bench CallPath -benchmem 2>/dev/null

// benchProvider answers every request at once with a canned answer.
type benchProvider struct{ answer string }

func (p benchProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	return schemaflux.CompletionResponse{
		Content: p.answer, Model: req.Model, Provider: "openai", FinishReason: "stop",
		Usage: schemaflux.TokenUsage{PromptTokens: 120, CompletionTokens: 24, TotalTokens: 144},
	}, nil
}
func (benchProvider) Name() string                                      { return "openai" }
func (benchProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (benchProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

type benchInvoice struct {
	OrderID string `json:"order_id"`
	Product string `json:"product"`
	Amount  string `json:"amount"`
}

const benchText = "Why is my October invoice for order A-2207 $49 and not $39? I am on the Team plan and nobody told me about a change."

func benchContext(answer string) context.Context {
	return schemaflux.NewClient("").WithProviderInstance(benchProvider{answer}).Context(context.Background())
}

// quiet turns the library's logging down for the benchmarks that are not
// measuring it, and puts it back.
func quiet(b *testing.B) {
	b.Helper()
	schemaflux.ConfigureLogging(schemaflux.LoggerConfig{Level: schemaflux.LogError, DisableStderr: true})
	b.Cleanup(func() { schemaflux.ConfigureLogging(schemaflux.LoggerConfig{Level: schemaflux.LogInfo}) })
}

func benchExtract(b *testing.B, ctx context.Context) {
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	b.ReportAllocs()
	for b.Loop() {
		got, err := schemaflux.Extract[benchInvoice](benchText, opts)
		if err != nil || got.OrderID != "A-2207" {
			b.Fatalf("got %+v, err %v", got, err)
		}
	}
}

func BenchmarkCallPathExtractAtDefaultLogging(b *testing.B) {
	benchExtract(b, benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`))
}

func BenchmarkCallPathExtractQuiet(b *testing.B) {
	quiet(b)
	benchExtract(b, benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`))
}

func BenchmarkCallPathExtractQuietParallel(b *testing.B) {
	quiet(b)
	ctx := benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := schemaflux.Extract[benchInvoice](benchText, opts); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkCallPathIsQuiet(b *testing.B) {
	quiet(b)
	ctx := benchContext(`{"q1":false}`)
	b.ReportAllocs()
	for b.Loop() {
		spam, err := schemaflux.Is(ctx, benchText, "Is this message spam?")
		if err != nil || spam {
			b.Fatalf("got %t, err %v", spam, err)
		}
	}
}

func BenchmarkCallPathIsAtDefaultLogging(b *testing.B) {
	ctx := benchContext(`{"q1":false}`)
	b.ReportAllocs()
	for b.Loop() {
		spam, err := schemaflux.Is(ctx, benchText, "Is this message spam?")
		if err != nil || spam {
			b.Fatalf("got %t, err %v", spam, err)
		}
	}
}

func BenchmarkCallPathSummarizeQuiet(b *testing.B) {
	quiet(b)
	ctx := benchContext("The customer asks why the October invoice for order A-2207 is $49 and not $39.")
	opts := schemaflux.NewSummarizeOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := schemaflux.Summarize(benchText, opts); err != nil {
			b.Fatal(err)
		}
	}
}

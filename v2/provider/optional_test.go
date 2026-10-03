package provider

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
)

// capabilities says which optional interfaces a provider has, by looking each up
// through find.
type capabilities struct{ streamer, warmer, retrier, decider bool }

func capabilitiesOf(p schemaflux.Provider) capabilities {
	var c capabilities
	_, c.streamer = bind.Optional[Streamer](p)
	_, c.warmer = bind.Optional[Warmer](p)
	_, c.retrier = bind.Optional[Retrier](p)
	_, c.decider = bind.Optional[Decider](p)
	return c
}

func TestEveryBuiltInProviderHasTheOptionalInterfacesItCanHonour(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want capabilities
	}{
		{"openai", capabilities{streamer: true, warmer: true, retrier: true}},
		{"anthropic", capabilities{warmer: true, retrier: true}},
		{"openrouter", capabilities{warmer: true, retrier: true}},
		{"cerebras", capabilities{warmer: true, retrier: true}},
		{"deepseek", capabilities{warmer: true, retrier: true}},
		{"qwen", capabilities{warmer: true, retrier: true}},
		{"zai", capabilities{warmer: true, retrier: true}},
		{"jev", capabilities{warmer: true, retrier: true, decider: true}},
		{"jev-openrouter", capabilities{warmer: true, retrier: true, decider: true}},
		{"jev-litellm", capabilities{warmer: true, retrier: true, decider: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := mustNew(t, tt.name, buildable(tt.name, Config{}))
			if got := capabilitiesOf(p); got != tt.want {
				t.Errorf("%s has %+v, want %+v", tt.name, got, tt.want)
			}
		})
	}
}

func TestOptionalInterfacesSurviveWrapping(t *testing.T) {
	t.Parallel()
	layers := []struct {
		name string
		wrap func(schemaflux.Provider) schemaflux.Provider
		seen bool // whether the wrapped provider is found through it
	}{
		{"no layer", func(p schemaflux.Provider) schemaflux.Provider { return p }, true},
		{"one layer with Unwrap", func(p schemaflux.Provider) schemaflux.Provider { return transparent{p} }, true},
		{"two layers with Unwrap", func(p schemaflux.Provider) schemaflux.Provider { return transparent{transparent{p}} }, true},
		{"three layers with Unwrap", func(p schemaflux.Provider) schemaflux.Provider { return transparent{transparent{transparent{p}}} }, true},
		{"one layer without Unwrap", func(p schemaflux.Provider) schemaflux.Provider { return wrapper{p} }, false},
		{"two layers, the outer one without Unwrap", func(p schemaflux.Provider) schemaflux.Provider { return wrapper{transparent{p}} }, false},
		{"two layers, the inner one without Unwrap", func(p schemaflux.Provider) schemaflux.Provider { return transparent{wrapper{p}} }, false},
	}
	for _, name := range []string{"openai", "anthropic", "jev"} {
		base := mustNew(t, name, buildable(name, Config{}))
		for _, layer := range layers {
			t.Run(name+" through "+layer.name, func(t *testing.T) {
				t.Parallel()
				got := capabilitiesOf(layer.wrap(base))
				want := capabilitiesOf(base)
				if !layer.seen {
					want = capabilities{}
				}
				if got != want {
					t.Errorf("found %+v through %s, want %+v", got, layer.name, want)
				}
			})
		}
	}
}

// everything is a Provider that is a Warmer, a Streamer, a Retrier and a Decider.
type everything struct {
	scripted
	warmed int
}

func (e *everything) Warm(context.Context) error { e.warmed++; return nil }
func (e *everything) Stream(context.Context, schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) { yield(schemaflux.Chunk{Done: true}, nil) }
}
func (e *everything) RetryPolicy() (int, time.Duration) { return 7, time.Minute }
func (e *everything) DecisionLimits() schemaflux.DecisionLimits {
	return schemaflux.DecisionLimits{MaxQuestions: 3}
}

func TestFindLooksThroughUnwrapForEveryOptionalInterface(t *testing.T) {
	t.Parallel()
	inner := &everything{scripted: scripted{name: "inner"}}
	outer := &everything{scripted: scripted{name: "outer"}}
	tests := []struct {
		name      string
		p         schemaflux.Provider
		wantFound bool
		wantInner bool // the one found is inner
	}{
		{"nil", nil, false, false},
		{"a provider with none", &scripted{name: "plain"}, false, false},
		{"the provider itself", inner, true, true},
		{"one layer", transparent{inner}, true, true},
		{"two layers", transparent{transparent{inner}}, true, true},
		{"a layer that hides", wrapper{inner}, false, false},
		{"a hiding layer under a transparent one", transparent{wrapper{inner}}, false, false},
		{"a wrapper that is one itself is found first", &wrapping{Provider: inner, own: outer}, true, false},
		{"a wrapper that wraps nothing", transparent{nil}, false, false},
		{"a wrapper that unwraps to itself", &looping{Provider: &scripted{name: "x"}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Run("Warmer", func(t *testing.T) {
				got, ok := bind.Optional[Warmer](tt.p)
				checkFound(t, ok, tt.wantFound, got == Warmer(inner), tt.wantInner)
			})
			t.Run("Streamer", func(t *testing.T) {
				got, ok := bind.Optional[Streamer](tt.p)
				checkFound(t, ok, tt.wantFound, got == Streamer(inner), tt.wantInner)
			})
			t.Run("Retrier", func(t *testing.T) {
				got, ok := bind.Optional[Retrier](tt.p)
				checkFound(t, ok, tt.wantFound, got == Retrier(inner), tt.wantInner)
			})
			t.Run("Decider", func(t *testing.T) {
				got, ok := bind.Optional[Decider](tt.p)
				checkFound(t, ok, tt.wantFound, got == Decider(inner), tt.wantInner)
			})
			t.Run("Unwrapper", func(t *testing.T) {
				_, ok := bind.Optional[Unwrapper](tt.p)
				_, isOne := tt.p.(Unwrapper)
				if ok != isOne {
					t.Errorf("found = %v, but the provider's own Unwrapper-ness is %v", ok, isOne)
				}
			})
		})
	}
}

func checkFound(t *testing.T, ok, wantFound, isInner, wantInner bool) {
	t.Helper()
	if ok != wantFound {
		t.Errorf("found = %v, want %v", ok, wantFound)
	}
	if ok && isInner != wantInner {
		t.Errorf("the provider found is the inner one = %v, want %v", isInner, wantInner)
	}
}

// wrapping is a wrapper that is itself a Warmer, a Streamer, a Retrier and a
// Decider, by embedding the one in own.
type wrapping struct {
	schemaflux.Provider
	own *everything
}

func (w *wrapping) Unwrap() schemaflux.Provider               { return w.Provider }
func (w *wrapping) Warm(ctx context.Context) error            { return w.own.Warm(ctx) }
func (w *wrapping) RetryPolicy() (int, time.Duration)         { return w.own.RetryPolicy() }
func (w *wrapping) DecisionLimits() schemaflux.DecisionLimits { return w.own.DecisionLimits() }
func (w *wrapping) Stream(ctx context.Context, r schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return w.own.Stream(ctx, r)
}

func TestFindStopsAtTheDepthLimit(t *testing.T) {
	t.Parallel()
	nest := func(layers int) schemaflux.Provider {
		var p schemaflux.Provider = &everything{scripted: scripted{name: "bottom"}}
		for range layers {
			p = transparent{p}
		}
		return p
	}
	tests := []struct {
		layers int
		found  bool
	}{
		{0, true}, {1, true}, {10, true}, {62, true}, {63, true}, {64, false}, {65, false}, {200, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d layers", tt.layers), func(t *testing.T) {
			t.Parallel()
			if _, ok := bind.Optional[Warmer](nest(tt.layers)); ok != tt.found {
				t.Errorf("found = %v with %d layers, want %v", ok, tt.layers, tt.found)
			}
		})
	}
}

func TestTheRetrySettingsOfAWrappedProviderAreFoundThroughItsWrapper(t *testing.T) {
	t.Parallel()
	base := mustNew(t, "openai", buildable("openai", Config{Retries: 9, RetryBackoff: 2 * time.Second}))
	for _, layers := range []int{0, 1, 2} {
		p := base
		for range layers {
			p = transparent{p}
		}
		retrier, ok := bind.Optional[Retrier](p)
		if !ok {
			t.Fatalf("no Retrier through %d layers", layers)
		}
		if retries, backoff := retrier.RetryPolicy(); retries != 9 || backoff != 2*time.Second {
			t.Errorf("through %d layers RetryPolicy() = (%d, %v), want (9, 2s)", layers, retries, backoff)
		}
	}
}

func TestWarmOpensTheConnectionOfABuiltProvider(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"openai", "anthropic", "openrouter", "jev"} {
		t.Run(name+" through no layer and two", func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, ""))
			p := providerAt(t, name, srv.URL, Config{})
			for _, candidate := range []schemaflux.Provider{p, transparent{transparent{p}}} {
				warmer, ok := bind.Optional[Warmer](candidate)
				if !ok {
					t.Fatal("not a Warmer")
				}
				if err := warmer.Warm(t.Context()); err != nil {
					t.Fatalf("Warm: %v", err)
				}
			}
			if got := srv.last(t).method; got != http.MethodHead {
				t.Errorf("warming sent %s, want HEAD", got)
			}
		})
	}
	t.Run("a refused connection is an unavailable error", func(t *testing.T) {
		t.Parallel()
		srv := serve(t, replying(200, ""))
		url := srv.URL
		srv.Close()
		p := providerAt(t, "openai", url, Config{})
		err := p.(Warmer).Warm(t.Context())
		if e := errorOf(t, err); e.Kind != schemaflux.ErrUnavailable {
			t.Errorf("Kind = %v, want unavailable", e.Kind)
		}
		if strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("the error prints the address: %v", err)
		}
	})
	t.Run("a canceled context is a canceled error", func(t *testing.T) {
		t.Parallel()
		srv := serve(t, replying(200, ""))
		p := providerAt(t, "openai", srv.URL, Config{})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := p.(Warmer).Warm(ctx)
		if e := errorOf(t, err); e.Kind != schemaflux.ErrCanceled || !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want a canceled error", err)
		}
	})
}

// sse is the body of a stream of the Responses API: deltas, then the whole.
const sseBody = `data: {"type":"response.output_text.delta","delta":"Hel"}

data: {"type":"response.output_text.delta","delta":"lo"}

data: {"type":"response.completed","response":{"id":"r","status":"completed","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}

`

func TestStreamYieldsTheDeltasAndThenTheWholeAnswer(t *testing.T) {
	t.Parallel()
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	})
	p := providerAt(t, "openai", srv.URL, Config{})
	for _, candidate := range []schemaflux.Provider{p, transparent{p}, transparent{transparent{p}}} {
		streamer, ok := bind.Optional[Streamer](candidate)
		if !ok {
			t.Fatal("not a Streamer")
		}
		var deltas []string
		var done schemaflux.Chunk
		for chunk, err := range streamer.Stream(t.Context(), schemaflux.Request{Model: "m", User: "hi"}) {
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if chunk.Done {
				done = chunk
				break
			}
			if chunk.Response.Content != "" {
				t.Errorf("a chunk before the last carries a Response: %+v", chunk)
			}
			deltas = append(deltas, chunk.Delta)
		}
		if strings.Join(deltas, "|") != "Hel|lo" {
			t.Errorf("deltas = %v, want [Hel lo]", deltas)
		}
		if done.Response.Content != "Hello" || done.Response.Usage.Total != 3 || done.Response.Model != "gpt-test" {
			t.Errorf("the last chunk's Response = %+v", done.Response)
		}
	}
}

func TestStreamReportsAFailureAsAnErrorAndEnds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		serve http.HandlerFunc
		kind  schemaflux.Kind
	}{
		{"a 401", replying(401, failingWith("bad_key")), schemaflux.ErrUnauthorized},
		{"a 429", replying(429, failingWith("slow"), "Retry-After", "2"), schemaflux.ErrRateLimited},
		{"a 503", replying(503, failingWith("down")), schemaflux.ErrUnavailable},
		{"a 400", replying(400, failingWith("bad")), schemaflux.ErrInvalidRequest},
		{"a stream that stops before its end", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hel\"}\n\n"))
		}, schemaflux.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := serve(t, tt.serve)
			p := providerAt(t, "openai", srv.URL, Config{})
			var sawDone bool
			var failure error
			for chunk, err := range p.(Streamer).Stream(t.Context(), schemaflux.Request{Model: "m", User: "hi"}) {
				if err != nil {
					failure = err
					continue
				}
				sawDone = sawDone || chunk.Done
			}
			if sawDone {
				t.Error("the stream yielded a Done chunk beside its failure")
			}
			if e := errorOf(t, failure); e.Kind != tt.kind {
				t.Errorf("Kind = %v, want %v", e.Kind, tt.kind)
			}
			for how, text := range theTextOf(t, failure) {
				if strings.Contains(text, testMarker) || strings.Contains(text, testKey) {
					t.Errorf("%s prints the provider's text or the key: %s", how, text)
				}
			}
		})
	}
	t.Run("stopping early abandons the request", func(t *testing.T) {
		t.Parallel()
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(sseBody))
		})
		p := providerAt(t, "openai", srv.URL, Config{})
		count := 0
		for range p.(Streamer).Stream(t.Context(), schemaflux.Request{Model: "m", User: "hi"}) {
			count++
			break
		}
		if count != 1 {
			t.Errorf("saw %d chunks, want 1", count)
		}
	})
}

func TestACanceledContextEndsAStream(t *testing.T) {
	t.Parallel()
	srv := serve(t, replying(200, ""))
	p := providerAt(t, "openai", srv.URL, Config{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var got error
	for _, err := range p.(Streamer).Stream(ctx, schemaflux.Request{Model: "m", User: "hi"}) {
		got = err
	}
	if e := errorOf(t, got); e.Kind != schemaflux.ErrCanceled || !errors.Is(got, context.Canceled) {
		t.Errorf("got %v, want a canceled error", got)
	}
}

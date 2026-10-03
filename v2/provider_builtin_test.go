//go:build !js && !wasip1

package schemaflux_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
)

// These tests use the provider package, which imports the root, and so are in
// the root's external test package.

// internalSeam is what the provider package gives the adapter of a built-in
// provider: the internal provider it was made from.
type internalSeam interface{ InternalProvider() llm.Provider }

// layer is a middleware: it passes every call on and says what it wraps.
type layer struct{ inner schemaflux.Provider }

func (l layer) Name() string { return "layer" }
func (l layer) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	return l.inner.Complete(ctx, req)
}
func (l layer) Unwrap() schemaflux.Provider { return l.inner }

// refusing is a loopback server that answers 401 to everything and counts what
// it was sent.
func refusing(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"no"}}`))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func builtIn(t *testing.T, name, baseURL string) schemaflux.Provider {
	t.Helper()
	p, err := provider.New(name, provider.Config{APIKey: "test-key", BaseURL: baseURL})
	if err != nil {
		t.Fatalf("provider.New(%q) = %v", name, err)
	}
	return p
}

func TestAProviderFromTheProviderPackageGoesToTheCallPathAsTheVeryInternalProviderItWasMadeFrom(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := builtIn(t, name, "")
			seam, ok := p.(internalSeam)
			if !ok {
				t.Fatalf("%T has no InternalProvider: the provider package changed what the root looks for", p)
			}
			inner := seam.InternalProvider()
			if inner == nil {
				t.Fatal("the seam is empty")
			}
			if got := bind.ToInternal(p); got != inner {
				t.Errorf("bind.ToInternal gave %T, want the internal provider the adapter was made from: a built-in is wrapped a second time", got)
			}
			if bind.ToInternal(p).Name() != name {
				t.Errorf("Name = %q", bind.ToInternal(p).Name())
			}
		})
	}
}

func TestTheCallPathKeepsEverythingABuiltInProviderCanDoWhenItGoesThroughTheRoot(t *testing.T) {
	t.Parallel()
	p := builtIn(t, "openai", "")
	internal := bind.ToInternal(p)
	if _, ok := internal.(llm.StreamingProvider); !ok {
		t.Error("streaming was lost")
	}
	if model, ok := llm.ModelFor(internal, types.Fast); !ok || model == "" {
		t.Errorf("the model of a tier was lost: %q, %v", model, ok)
	}
	if _, ok := bind.ToInternal(builtIn(t, "anthropic", "")).(llm.ModelSender); !ok {
		t.Error("the model sent for a request (what a data policy reads) was lost")
	}
	if _, ok := internal.(llm.Warmer); !ok {
		t.Error("warming was lost")
	}
}

func TestAMiddlewareAroundABuiltInIsRunAndTheBuiltInIsStillReachedThroughIt(t *testing.T) {
	t.Parallel()
	server, hits := refusing(t)
	built := builtIn(t, "openai", server.URL)
	inner := built.(internalSeam).InternalProvider()
	for depth, wrapped := range map[int]schemaflux.Provider{1: layer{built}, 2: layer{layer{built}}, 3: layer{layer{layer{built}}}} {
		adapted := bind.ToInternal(wrapped)
		if adapted == inner {
			t.Fatalf("depth %d: the middleware was dropped: its Complete would never run", depth)
		}
		unwrapper, ok := adapted.(interface{ Unwrap() llm.Provider })
		if !ok || unwrapper.Unwrap() != inner {
			t.Errorf("depth %d: the built-in is not reachable through the adapter", depth)
		}
		if model, ok := llm.ModelFor(adapted, types.Fast); !ok || model == "" {
			t.Errorf("depth %d: the model of a tier is not reached: %q, %v", depth, model, ok)
		}
		// A call goes through the middleware to the built-in provider and its
		// server, and its refusal is an authorization failure, as for any call.
		before := hits.Load()
		_, err := adapted.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "u"})
		if err == nil || hits.Load() != before+1 {
			t.Errorf("depth %d: Complete = %v, server hits %d", depth, err, hits.Load()-before)
		}
		// And a stream goes the same way.
		streamer, ok := adapted.(llm.StreamingProvider)
		if !ok {
			t.Fatalf("depth %d: not streaming", depth)
		}
		var streamErr error
		for _, e := range streamer.CompleteStream(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "u"}) {
			if e != nil {
				streamErr = e
				break
			}
		}
		if streamErr == nil || types.KindOf(streamErr) == types.KindUnsupportedCapability {
			t.Errorf("depth %d: the stream through a middleware is %v: the built-in's streaming was hidden", depth, streamErr)
		}
	}
}

func TestACallOnAClientWhoseProviderIsABuiltInReachesItsServer(t *testing.T) {
	t.Parallel()
	server, hits := refusing(t)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: builtIn(t, "openai", server.URL), Timeout: 30 * time.Second})
	_, err := ops.Is(ctx, "s", "q?", types.OpOptions{})
	if hits.Load() != 1 {
		t.Fatalf("the server received %d requests (err = %v)", hits.Load(), err)
	}
	var e *schemaflux.Error
	if !errors.As(bind.ConvertError("is", err), &e) || e.Kind != schemaflux.ErrUnauthorized {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestAClientWarmsAMiddlewareAroundABuiltInThroughTheMiddleware(t *testing.T) {
	t.Parallel()
	server, hits := refusing(t)
	client := schemaflux.Client{Provider: layer{layer{builtIn(t, "openai", server.URL)}}}
	if err := client.Warm(context.Background()); err != nil {
		t.Fatalf("Warm = %v", err)
	}
	_ = hits
}

func TestACallersOwnProviderIsStillWrapped(t *testing.T) {
	t.Parallel()
	own := layer{inner: nil}
	if _, ok := any(own).(internalSeam); ok {
		t.Fatal("the stand-in has the seam")
	}
	adapted := bind.ToInternal(own)
	if adapted == nil || adapted.Name() != "layer" {
		t.Errorf("ToInternal = %v", adapted)
	}
	if bind.FromInternal(adapted) != any(own) {
		t.Error("the provider did not come back")
	}
}

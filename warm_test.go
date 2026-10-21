//go:build !js && !wasip1

package schemaflux

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// warmable is a Provider that can open its connection ahead of a request.
type warmable struct {
	answerer
	warmed atomic.Int32
	failed error
}

func (w *warmable) Warm(context.Context) error {
	w.warmed.Add(1)
	return w.failed
}

// wrapper is a Provider that wraps another and does nothing else.
type wrapper struct{ inner Provider }

func (w wrapper) Name() string { return "wrapper" }
func (w wrapper) Complete(ctx context.Context, req Request) (Response, error) {
	return w.inner.Complete(ctx, req)
}
func (w wrapper) Unwrap() Provider { return w.inner }

// withTiers is a Provider that means a model for each tier.
type withTiers struct{ answerer }

func (*withTiers) ModelFor(Tier) string { return "tier-model" }

func TestWarmOfAClientWithAProviderAndAModelIsNilAndSendsNothing(t *testing.T) {
	t.Parallel()
	provider := &answerer{name: "custom"}
	if err := (Client{Provider: provider, Model: "m"}).Warm(context.Background()); err != nil {
		t.Fatalf("Warm = %v", err)
	}
	if provider.count() != 0 {
		t.Errorf("Warm sent %d requests, want none", provider.count())
	}
}

func TestWarmOpensTheConnectionOfTheProviderOnceAndLooksThroughWrappers(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(*warmable) Provider{
		"the provider itself":       func(w *warmable) Provider { return w },
		"a wrapper around it":       func(w *warmable) Provider { return wrapper{w} },
		"three wrappers around it":  func(w *warmable) Provider { return wrapper{wrapper{wrapper{w}}} },
		"a wrapper that is nothing": func(w *warmable) Provider { return wrapper{w} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := &warmable{answerer: answerer{name: "custom"}}
			if err := (Client{Provider: build(w), Model: "m"}).Warm(context.Background()); err != nil {
				t.Fatalf("Warm = %v", err)
			}
			if w.warmed.Load() != 1 {
				t.Errorf("the provider was warmed %d times, want once", w.warmed.Load())
			}
			if w.count() != 0 {
				t.Errorf("Warm sent %d requests", w.count())
			}
		})
	}
}

func TestWarmOfAProviderThatCannotWarmIsNotAnError(t *testing.T) {
	t.Parallel()
	for name, p := range map[string]Provider{
		"a provider with no Warm":     &answerer{name: "custom"},
		"a wrapper around such":       wrapper{&answerer{name: "custom"}},
		"a provider with only a tier": &withTiers{answerer{name: "custom"}},
	} {
		if err := (Client{Provider: p, Model: "m"}).Warm(context.Background()); err != nil {
			t.Errorf("%s: Warm = %v", name, err)
		}
	}
}

func TestWarmReturnsAFailureToOpenTheConnectionAsAnErrorOfTheKindItHas(t *testing.T) {
	t.Parallel()
	w := &warmable{answerer: answerer{name: "custom"}, failed: &Error{Kind: ErrUnavailable, Message: "no route"}}
	err := (Client{Provider: w, Model: "m"}).Warm(context.Background())
	if err == nil {
		t.Fatal("Warm succeeded although the connection could not be opened")
	}
	e := asError(t, "warm", err)
	if e.Kind != ErrUnavailable || e.Op != "warm" {
		t.Errorf("Warm = {%v %q}, want an unavailable provider named for warm", e.Kind, e.Op)
	}
	if e.Provider != "custom" {
		t.Errorf("Provider = %q", e.Provider)
	}
}

func TestWarmOfAClientThatKnowsNoModelIsAConfigurationError(t *testing.T) {
	t.Parallel()
	err := (Client{Provider: &answerer{name: "custom"}}).Warm(context.Background())
	if err == nil {
		t.Fatal("Warm succeeded for a provider that means no model and a client that names none")
	}
	e := asError(t, "warm", err)
	if e.Kind != ErrConfiguration || !strings.Contains(e.Message, `"custom"`) || !strings.Contains(e.Message, "Client.Model") {
		t.Errorf("Warm = %v", e)
	}
	if e.Op != "warm" {
		t.Errorf("Op = %q", e.Op)
	}
}

func TestWarmKnowsAModelFromTheClientTheProviderOrTheLibrariesTable(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]Client{
		"the client's model":                         {Provider: &answerer{name: "custom"}, Model: "m"},
		"a provider with a model for each tier":      {Provider: &withTiers{answerer{name: "custom"}}},
		"a wrapper around such a provider":           {Provider: wrapper{&withTiers{answerer{name: "custom"}}}},
		"a provider the library has a table for":     {Provider: &answerer{name: "openai"}},
		"a provider with a table, named in capitals": {Provider: &answerer{name: " Anthropic "}},
	} {
		if err := c.Warm(context.Background()); err != nil {
			t.Errorf("%s: Warm = %v", name, err)
		}
	}
}

func TestWarmOfAContextThatEndedIsTheContextsError(t *testing.T) {
	t.Parallel()
	w := &warmable{answerer: answerer{name: "custom"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (Client{Provider: w, Model: "m"}).Warm(ctx)
	if err == nil || asError(t, "warm", err).Kind != ErrCanceled {
		t.Errorf("Warm on a cancelled context = %v, want ErrCanceled", err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if err := (Client{Provider: w, Model: "m"}).Warm(expired); err == nil || asError(t, "warm", err).Kind != ErrTimeout {
		t.Errorf("Warm past the deadline = %v, want ErrTimeout", err)
	}
	if w.warmed.Load() != 0 {
		t.Error("the connection was opened for a context that had ended")
	}
}

func TestWarmOfANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	//lint:ignore SA1012 a nil context is what is tested
	err := (Client{Provider: &answerer{name: "custom"}, Model: "m"}).Warm(nil)
	if err == nil {
		t.Fatal("Warm(nil) succeeded")
	}
	if e := asError(t, "warm", err); e.Kind != ErrInvalidRequest || e.Op != "warm" {
		t.Errorf("Warm(nil) = {%v %q}", e.Kind, e.Op)
	}
}

func TestWarmOfAClientWithABadSettingIsAConfigurationErrorThatNamesIt(t *testing.T) {
	t.Parallel()
	w := &warmable{answerer: answerer{name: "custom"}}
	err := (Client{Provider: w, Model: "m", Timeout: -time.Second}).Warm(context.Background())
	if err == nil || asError(t, "warm", err).Kind != ErrConfiguration || !strings.Contains(err.Error(), "Client.Timeout") {
		t.Errorf("Warm = %v", err)
	}
	if w.warmed.Load() != 0 {
		t.Error("a client that cannot make a call was warmed")
	}
}

func TestWarmWithNoKeyInTheEnvironmentIsTheConfigurationErrorThatNamesTheVariables(t *testing.T) {
	useEnvironment(t, nil)
	err := (Client{}).Warm(context.Background())
	if err == nil {
		t.Fatal("Warm succeeded with no key")
	}
	e := asError(t, "warm", err)
	if e.Kind != ErrConfiguration || !strings.Contains(e.Message, "OPENAI_API_KEY") || e.Op != "warm" {
		t.Errorf("Warm = %v", e)
	}
}

func TestWarmWithAKeyInTheEnvironmentBuildsTheProviderOnceAndSendsNoPrompt(t *testing.T) {
	server := newLoopback(t)
	cell := useEnvironment(t, map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": server.URL})
	for range 3 {
		if err := (Client{}).Warm(context.Background()); err != nil {
			t.Fatalf("Warm = %v", err)
		}
	}
	if cell.builds.Load() != 1 {
		t.Errorf("the provider was built %d times for three warms", cell.builds.Load())
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	for _, body := range server.bodies {
		if strings.TrimSpace(body) != "" {
			t.Errorf("Warm sent a body: %q", body)
		}
	}
}

func TestWarmOfTheDefaultIsTheCheckToRunAtStartUp(t *testing.T) {
	useEnvironment(t, nil)
	useDefault(t, Client{})
	if err := Default().Warm(context.Background()); err == nil || asError(t, "warm", err).Kind != ErrConfiguration {
		t.Errorf("Default().Warm with no key = %v", err)
	}
	w := &warmable{answerer: answerer{name: "custom"}}
	SetDefault(Client{Provider: w, Model: "m"})
	if err := Default().Warm(context.Background()); err != nil || w.warmed.Load() != 1 {
		t.Errorf("Default().Warm = %v, warmed %d times", err, w.warmed.Load())
	}
}

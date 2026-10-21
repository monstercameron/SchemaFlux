//go:build !js && !wasip1

package schemaflux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A cell of the tests reads the variables of a map and not those of the
// process, so none of these tests sets a variable and each runs in parallel with
// the others. Those that go through a Client use useEnvironment, which swaps the
// environment every client reads, and so are not parallel.

// cellOf is an environment of the given variables, read once on first use.
func cellOf(variables map[string]string) *environmentCell {
	return &environmentCell{lookup: func(name string) (string, bool) {
		value, ok := variables[name]
		return value, ok
	}}
}

// loopback is a server on the loopback interface that answers every request
// with an HTTP 401 and records what it was sent. A 401 is not retried, so a test
// that reaches it does not wait for a backoff.
type loopback struct {
	*httptest.Server
	mu      sync.Mutex
	headers []http.Header
	bodies  []string
	paths   []string
}

func newLoopback(t *testing.T) *loopback {
	t.Helper()
	l := &loopback{}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		l.mu.Lock()
		l.headers = append(l.headers, r.Header.Clone())
		l.bodies = append(l.bodies, string(body))
		l.paths = append(l.paths, r.URL.Path)
		l.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"no","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(l.Close)
	return l
}

func (l *loopback) requests() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func TestTheTableOfProviderVariablesIsWhatTheParserReads(t *testing.T) {
	t.Parallel()
	for provider, row := range providerVariables {
		for _, name := range row.keys {
			t.Run(provider+" key "+name, func(t *testing.T) {
				t.Parallel()
				env := config.Read(func(n string) (string, bool) { return "the-key", n == name })
				if got := env.Keys[provider]; got != "the-key" {
					t.Errorf("with only %s set, the key of %s is %q: the table names a variable the parser does not read for it", name, provider, got)
				}
			})
		}
		for _, name := range row.baseURLs {
			t.Run(provider+" base URL "+name, func(t *testing.T) {
				t.Parallel()
				env := config.Read(func(n string) (string, bool) { return "http://base.test", n == name })
				if got := env.BaseURLs[provider]; got != "http://base.test" {
					t.Errorf("with only %s set, the base URL of %s is %q", name, provider, got)
				}
			})
		}
		t.Run(provider+" has no variable the table does not list", func(t *testing.T) {
			t.Parallel()
			var read []string
			config.Read(func(n string) (string, bool) { read = append(read, n); return "", false })
			listed := append(append([]string{}, row.keys...), row.baseURLs...)
			for _, name := range listed {
				if !slices.Contains(read, name) {
					t.Errorf("the table lists %s, which the parser never looks at", name)
				}
			}
		})
	}
}

func TestAnEnvironmentWithNoKeyIsAConfigurationErrorThatNamesTheVariables(t *testing.T) {
	t.Parallel()
	for provider, row := range providerVariables {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			cell := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": provider})
			built, err := cell.resolve()
			if err == nil || built != nil {
				t.Fatalf("resolve = %v, %v, want no provider and an error", built, err)
			}
			e := asError(t, "extract", err)
			if e.Kind != ErrConfiguration {
				t.Errorf("kind = %v", e.Kind)
			}
			for _, name := range append(append([]string{}, row.keys...), sharedKeyVariable) {
				if !strings.Contains(e.Message, name) {
					t.Errorf("the message %q does not name %s", e.Message, name)
				}
			}
			if !strings.Contains(e.Message, `"`+provider+`"`) || !strings.Contains(e.Message, "give the client a Provider") {
				t.Errorf("the message does not say which provider and the way out: %q", e.Message)
			}
			if got := e.Error(); !strings.HasPrefix(got, "extract: configuration: no provider") {
				t.Errorf("Error() = %q", got)
			}
		})
	}
	t.Run("a provider the library has no variables for is told the shared key", func(t *testing.T) {
		t.Parallel()
		_, err := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": "nonesuch"}).resolve()
		if err == nil || !strings.Contains(err.Error(), sharedKeyVariable) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the default provider is openai", func(t *testing.T) {
		t.Parallel()
		_, err := cellOf(nil).resolve()
		if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestAnEnvironmentWithAKeyBuildsTheProviderItNames(t *testing.T) {
	t.Parallel()
	for provider := range providerVariables {
		if strings.HasPrefix(provider, "jev") {
			continue // the decision providers are not built to answer text
		}
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			built, err := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": provider, "SCHEMAFLUX_API_KEY": "a-key"}).resolve()
			if err != nil {
				t.Fatalf("resolve = %v", err)
			}
			if built == nil || built.Name() != provider {
				t.Errorf("the provider built is %v, want one named %s", built, provider)
			}
			if retries, backoff := built.RetryPolicy(); retries != llm.DefaultRetries || backoff != llm.DefaultRetryBackoff {
				t.Errorf("RetryPolicy = %d, %v, want the defaults of a provider that is built, %d and %v", retries, backoff, llm.DefaultRetries, llm.DefaultRetryBackoff)
			}
		})
	}
	t.Run("a provider that answers with a placeholder is refused and the offline provider is named", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"local", "mock", " LOCAL ", "Mock"} {
			for _, key := range []string{"", "a-key"} {
				variables := map[string]string{"SCHEMAFLUX_PROVIDER": name}
				if key != "" {
					variables["SCHEMAFLUX_API_KEY"] = key
				}
				built, err := cellOf(variables).resolve()
				if built != nil || err == nil {
					t.Fatalf("%q: resolve = %v, %v, want a refusal", name, built, err)
				}
				e := asError(t, "extract", err)
				if e.Kind != ErrConfiguration || !strings.Contains(e.Message, "schemafluxtest") {
					t.Errorf("%q: got %q (%v), want a configuration error that names schemafluxtest", name, e.Message, e.Kind)
				}
			}
		}
		_, err := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": "openai"}).resolve()
		if err == nil {
			t.Error("a missing key selected the offline provider")
		}
	})
	t.Run("the name is read without its case or its space", func(t *testing.T) {
		t.Parallel()
		built, err := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": "  OpenAI ", "OPENAI_API_KEY": "k"}).resolve()
		if err != nil || built.Name() != "openai" {
			t.Errorf("resolve = %v, %v", built, err)
		}
	})
	t.Run("a name no provider has is a configuration error", func(t *testing.T) {
		t.Parallel()
		built, err := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": "nonesuch", "SCHEMAFLUX_API_KEY": "k"}).resolve()
		if err == nil || built != nil {
			t.Fatalf("resolve = %v, %v", built, err)
		}
		if kind := asError(t, "extract", err).Kind; kind != ErrConfiguration {
			t.Errorf("kind = %v", kind)
		}
	})
}

func TestTheKeyOfTheProviderTheEnvironmentNamesIsTheOneUsed(t *testing.T) {
	t.Parallel()
	server := newLoopback(t)
	cases := []struct {
		name      string
		variables map[string]string
		wantKey   string
	}{
		{"the provider's own variable", map[string]string{"OPENAI_API_KEY": "own"}, "own"},
		{"the library's form beats it", map[string]string{"OPENAI_API_KEY": "own", "SCHEMAFLUX_OPENAI_API_KEY": "library"}, "library"},
		{"the shared key when the provider's own are unset", map[string]string{"SCHEMAFLUX_API_KEY": "shared"}, "shared"},
		{"the provider's own beats the shared key", map[string]string{"OPENAI_API_KEY": "own", "SCHEMAFLUX_API_KEY": "shared"}, "own"},
		{"another provider's key is not this one's", map[string]string{"ANTHROPIC_API_KEY": "wrong", "OPENAI_API_KEY": "right"}, "right"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vars := map[string]string{"OPENAI_BASE_URL": server.URL}
			for k, v := range c.variables {
				vars[k] = v
			}
			built, err := cellOf(vars).resolve()
			if err != nil {
				t.Fatal(err)
			}
			before := server.requests()
			_, _ = built.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "hello"})
			if server.requests() != before+1 {
				t.Fatal("the provider did not call the loopback server named by OPENAI_BASE_URL")
			}
			server.mu.Lock()
			auth := server.headers[len(server.headers)-1].Get("Authorization")
			server.mu.Unlock()
			if auth != "Bearer "+c.wantKey {
				t.Errorf("the request carried %q, want the key %q", auth, c.wantKey)
			}
		})
	}
	t.Run("another provider's key alone is no key", func(t *testing.T) {
		t.Parallel()
		if _, err := cellOf(map[string]string{"ANTHROPIC_API_KEY": "wrong"}).resolve(); err == nil {
			t.Error("openai was built with anthropic's key")
		}
	})
}

func TestTheModelVariablesBecomeTheModelsOfTheEnvironmentsProvider(t *testing.T) {
	t.Parallel()
	smart, fast, quick := types.Smart, types.Fast, types.Quick
	cases := []struct {
		name      string
		variables map[string]string
		want      map[types.Speed]string
	}{
		{"none: the provider's own", nil, map[types.Speed]string{smart: "gpt-6-luna", fast: "gpt-6-luna", quick: "gpt-6-luna"}},
		{"one model for every tier", map[string]string{"SCHEMAFLUX_MODEL": "one"}, map[types.Speed]string{smart: "one", fast: "one", quick: "one"}},
		{"a model for each tier", map[string]string{"SCHEMAFLUX_MODEL_SMART": "s", "SCHEMAFLUX_MODEL_FAST": "f", "SCHEMAFLUX_MODEL_QUICK": "q"}, map[types.Speed]string{smart: "s", fast: "f", quick: "q"}},
		{"the one model beats the per-tier ones", map[string]string{"SCHEMAFLUX_MODEL": "one", "SCHEMAFLUX_MODEL_SMART": "s"}, map[types.Speed]string{smart: "one", fast: "one", quick: "one"}},
		{"one tier set: the others are the provider's own", map[string]string{"SCHEMAFLUX_MODEL_SMART": "s"}, map[types.Speed]string{smart: "s", fast: "gpt-6-luna", quick: "gpt-6-luna"}},
		{"a model of space is not set", map[string]string{"SCHEMAFLUX_MODEL": "   "}, map[types.Speed]string{smart: "gpt-6-luna", fast: "gpt-6-luna", quick: "gpt-6-luna"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			vars := map[string]string{"OPENAI_API_KEY": "k"}
			for k, v := range c.variables {
				vars[k] = v
			}
			built, err := cellOf(vars).resolve()
			if err != nil {
				t.Fatal(err)
			}
			for tier, want := range c.want {
				if got, ok := llm.ModelFor(built, tier); !ok || got != want {
					t.Errorf("the model for %v is %q, %v, want %q", tier, got, ok, want)
				}
			}
		})
	}
}

func TestAMalformedVariableIsAConfigurationErrorThatNamesItAndNeverItsValue(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-VALUE-9c1f"
	cases := []struct {
		name      string
		variables map[string]string
		variable  string
	}{
		{"a key with a control character", map[string]string{"OPENAI_API_KEY": secret + "\nX-Injected: 1"}, "OPENAI_API_KEY"},
		{"the library's key with a control character", map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": secret + "\x00"}, "SCHEMAFLUX_OPENAI_API_KEY"},
		{"the shared key with a control character", map[string]string{"SCHEMAFLUX_API_KEY": secret + "\x01x"}, "SCHEMAFLUX_API_KEY"},
		{"a base URL that is not a URL", map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": secret + " ://nonsense"}, "OPENAI_BASE_URL"},
		{"a base URL with no host", map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "https:///" + secret}, "OPENAI_BASE_URL"},
		{"a base URL of another scheme", map[string]string{"OPENAI_API_KEY": "k", "SCHEMAFLUX_OPENAI_BASE_URL": "ftp://" + secret}, "SCHEMAFLUX_OPENAI_BASE_URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			built, err := cellOf(c.variables).resolve()
			if err == nil || built != nil {
				t.Fatalf("resolve = %v, %v", built, err)
			}
			e := asError(t, "extract", err)
			if e.Kind != ErrConfiguration {
				t.Errorf("kind = %v", e.Kind)
			}
			if !strings.Contains(e.Message, c.variable) {
				t.Errorf("the message %q does not name %s", e.Message, c.variable)
			}
			text, _ := json.Marshal(e)
			for _, shown := range []string{e.Error(), e.Message, e.Detail(), string(text), fmt.Sprintf("%+v", e)} {
				if strings.Contains(shown, secret) {
					t.Errorf("the value of the variable is in %q", shown)
				}
			}
		})
	}
	t.Run("a variable of another provider does not matter", func(t *testing.T) {
		t.Parallel()
		built, err := cellOf(map[string]string{
			"OPENAI_API_KEY":    "k",
			"ANTHROPIC_API_KEY": "bad\nkey", "ANTHROPIC_BASE_URL": "not a url",
			"QWEN_BASE_URL": "::",
		}).resolve()
		if err != nil || built == nil {
			t.Errorf("a malformed variable of a provider that is not in use failed the one that is: %v", err)
		}
	})
	t.Run("a malformed variable is not skipped for the next of its row", func(t *testing.T) {
		t.Parallel()
		_, err := cellOf(map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": "bad\nkey", "OPENAI_API_KEY": "good"}).resolve()
		if err == nil || !strings.Contains(err.Error(), "SCHEMAFLUX_OPENAI_API_KEY") {
			t.Errorf("err = %v: a setting nobody chose was used instead", err)
		}
	})
}

func TestTheTimeoutOfTheEnvironmentIsReadOnceAndAMalformedOneIsAnError(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		value string
		set   bool
		want  time.Duration
		bad   bool
	}{
		{"unset", "", false, 0, false},
		{"empty", "", true, 0, false},
		{"seconds", "45s", true, 45 * time.Second, false},
		{"minutes", "2m", true, 2 * time.Minute, false},
		{"with space", " 10s ", true, 10 * time.Second, false},
		{"not a duration", "soon", true, 0, true},
		{"a bare number", "30", true, 0, true},
		{"zero", "0s", true, 0, true},
		{"negative", "-5s", true, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			vars := map[string]string{}
			if c.set {
				vars["SCHEMAFLUX_TIMEOUT"] = c.value
			}
			got, err := cellOf(vars).timeout()
			if c.bad {
				if err == nil || !strings.Contains(err.Error(), "SCHEMAFLUX_TIMEOUT") {
					t.Fatalf("timeout = %v, %v, want an error naming SCHEMAFLUX_TIMEOUT", got, err)
				}
				if strings.Contains(err.Error(), c.value) && c.value != "" && len(c.value) > 2 {
					t.Errorf("the error shows the value: %v", err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("timeout = %v, %v, want %v", got, err, c.want)
			}
		})
	}
}

func TestTheEnvironmentIsReadOnceAndItsProviderBuiltOnceWhateverTheNumberOfCallers(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	vars := map[string]string{"OPENAI_API_KEY": "k"}
	var mu sync.Mutex
	cell := &environmentCell{lookup: func(name string) (string, bool) {
		reads.Add(1)
		mu.Lock()
		defer mu.Unlock()
		value, ok := vars[name]
		return value, ok
	}}
	var wg sync.WaitGroup
	results := make([]llm.Provider, 32)
	for i := range results {
		wg.Go(func() {
			results[i], _ = cell.resolve()
			_ = cell.environment()
			_, _ = cell.timeout()
		})
	}
	wg.Wait()
	for i, p := range results {
		if p == nil || p != results[0] {
			t.Fatalf("caller %d got %v, want the one provider every caller got", i, p)
		}
	}
	if cell.builds.Load() != 1 {
		t.Errorf("the provider was built %d times", cell.builds.Load())
	}
	first := reads.Load()
	mu.Lock()
	vars["OPENAI_API_KEY"] = "changed"
	vars["SCHEMAFLUX_TIMEOUT"] = "5s"
	mu.Unlock()
	again, _ := cell.resolve()
	_ = cell.environment()
	if again != results[0] || reads.Load() != first {
		t.Error("a change to the variables after the first read reached the environment")
	}
}

// variablesThatChange is an environment a test changes while a cell reads it.
type variablesThatChange struct {
	mu   sync.Mutex
	vars map[string]string
}

func (v *variablesThatChange) set(name, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.vars[name] = value
}

func (v *variablesThatChange) cell() *environmentCell {
	return &environmentCell{lookup: func(name string) (string, bool) {
		v.mu.Lock()
		defer v.mu.Unlock()
		value, ok := v.vars[name]
		return value, ok
	}}
}

func TestAFailureToBuildIsNotKeptAndTheEnvironmentIsReadAgain(t *testing.T) {
	t.Parallel()
	env := &variablesThatChange{vars: map[string]string{}}
	cell := env.cell()
	built, first := cell.resolve()
	if first == nil || built != nil {
		t.Fatalf("the first resolve = %v, %v, want no provider and an error", built, first)
	}
	if again, second := cell.resolve(); second == nil || again != nil || second.Error() != first.Error() {
		t.Errorf("with nothing changed the second resolve = %v, %v, want the same error", again, second)
	}

	env.set("OPENAI_API_KEY", "now-there-is-one")
	built, second := cell.resolve()
	if second != nil || built == nil || built.Name() != "openai" {
		t.Fatalf("after the key was set, resolve = %v, %v, want the provider", built, second)
	}
	if cell.builds.Load() != 3 {
		t.Errorf("built %d times, want one try for each of the three calls", cell.builds.Load())
	}

	// A success is kept: a later change does not reach it.
	env.set("OPENAI_API_KEY", "changed")
	env.set("SCHEMAFLUX_PROVIDER", "anthropic")
	again, err := cell.resolve()
	if err != nil || again != built || cell.builds.Load() != 3 {
		t.Errorf("after a success resolve = %v, %v with %d builds, want the same provider and no new build", again, err, cell.builds.Load())
	}
}

func TestAFailureThatIsFixedLaterReachesTheDefaultClientWithoutReplacingIt(t *testing.T) {
	env := &variablesThatChange{vars: map[string]string{}}
	previous := environmentOverride.Swap(env.cell())
	t.Cleanup(func() { environmentOverride.Store(previous) })
	useDefault(t, Client{})

	call := func() error {
		_, err := ops.Is(context.Background(), "a subject", "Is this a question?", types.OpOptions{Model: "named-model"})
		return err
	}
	if err := call(); !errors.Is(err, ErrConfiguration) && kindOfErr(convertError("is", err)) != ErrConfiguration {
		t.Fatalf("the first call returned %v, want a configuration error", err)
	}
	if err := (Client{}).Warm(context.Background()); err == nil {
		t.Fatal("Warm succeeded with no key")
	}

	server := newLoopback(t)
	env.set("OPENAI_API_KEY", "loaded-after-linking")
	env.set("OPENAI_BASE_URL", server.URL)
	before := server.requests()
	_ = call()
	if server.requests() != before+1 {
		t.Errorf("after the key was set the loopback server received %d requests, want 1", server.requests()-before)
	}
	for name, ctx := range map[string]context.Context{
		"WithClient(Client{})": WithClient(context.Background(), Client{}),
		"SetDefault copy":      WithClient(context.Background(), Default()),
	} {
		before = server.requests()
		_, _ = ops.Is(ctx, "a subject", "Is this a question?", types.OpOptions{Model: "named-model"})
		if server.requests() != before+1 {
			t.Errorf("%s: %d requests, want 1", name, server.requests()-before)
		}
	}
}

func TestCallsThatArriveTogetherBuildOneProviderAndSeeOneOutcome(t *testing.T) {
	t.Parallel()
	for _, withKey := range []bool{true, false} {
		env := &variablesThatChange{vars: map[string]string{}}
		if withKey {
			env.set("OPENAI_API_KEY", "k")
		}
		cell := env.cell()
		var wg sync.WaitGroup
		providers := make([]llm.Provider, 32)
		errs := make([]error, 32)
		for i := range providers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				providers[i], errs[i] = cell.resolve()
			}()
		}
		wg.Wait()
		for i := range providers {
			if withKey && (errs[i] != nil || providers[i] != providers[0]) {
				t.Fatalf("caller %d got %v, %v, want the one provider", i, providers[i], errs[i])
			}
			if !withKey && (errs[i] == nil || providers[i] != nil) {
				t.Fatalf("caller %d got %v, %v, want the error", i, providers[i], errs[i])
			}
		}
		if withKey && cell.builds.Load() != 1 {
			t.Errorf("%d builds for callers that arrived together, want 1", cell.builds.Load())
		}
	}
}

func TestAZeroClientWithAKeyInTheEnvironmentCallsTheProviderTheEnvironmentNames(t *testing.T) {
	server := newLoopback(t)
	useEnvironment(t, map[string]string{"OPENAI_API_KEY": "key-from-the-environment", "OPENAI_BASE_URL": server.URL})

	call := func(ctx context.Context) error {
		_, err := ops.Is(ctx, "a subject", "Is this a question?", types.OpOptions{Model: "named-model"})
		return err
	}
	for name, ctx := range map[string]context.Context{
		"through WithClient":      WithClient(context.Background(), Client{}),
		"through the default":     context.Background(),
		"through a copy of it":    WithClient(context.Background(), Default()),
		"with a model of its own": WithClient(context.Background(), Client{Model: "client-model"}),
	} {
		before := server.requests()
		err := call(ctx)
		if server.requests() != before+1 {
			t.Errorf("%s: the loopback server received %d requests, want 1 (err = %v)", name, server.requests()-before, err)
			continue
		}
		server.mu.Lock()
		auth := server.headers[len(server.headers)-1].Get("Authorization")
		server.mu.Unlock()
		if auth != "Bearer key-from-the-environment" {
			t.Errorf("%s: the request carried %q", name, auth)
		}
		if err == nil || asError(t, "is", err).Kind != ErrUnauthorized {
			t.Errorf("%s: the server's 401 came back as %v, want ErrUnauthorized", name, err)
		}
	}
}

func TestAClientsModelBeatsTheEnvironmentsAndTheEnvironmentsReachesAZeroClient(t *testing.T) {
	server := newLoopback(t)
	useEnvironment(t, map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": server.URL, "SCHEMAFLUX_MODEL": "environment-model"})
	modelOf := func(ctx context.Context) string {
		before := server.requests()
		_, _ = ops.Is(ctx, "s", "q?", types.OpOptions{})
		if server.requests() != before+1 {
			t.Fatal("no request reached the server")
		}
		server.mu.Lock()
		defer server.mu.Unlock()
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal([]byte(server.bodies[len(server.bodies)-1]), &body)
		return body.Model
	}
	if got := modelOf(WithClient(context.Background(), Client{})); got != "environment-model" {
		t.Errorf("a zero client asked for %q, want the environment's model", got)
	}
	if got := modelOf(WithClient(context.Background(), Client{Model: "client-model"})); got != "client-model" {
		t.Errorf("a client with a model asked for %q, want its own", got)
	}
}

func TestAZeroClientWithNoKeyReturnsAConfigurationErrorAndSendsNothing(t *testing.T) {
	server := newLoopback(t)
	// A base URL and every other variable, but no key.
	useEnvironment(t, map[string]string{"OPENAI_BASE_URL": server.URL, "SCHEMAFLUX_MODEL": "m"})

	for name, ctx := range map[string]context.Context{
		"through WithClient":  WithClient(context.Background(), Client{}),
		"through the default": context.Background(),
	} {
		_, err := ops.Is(ctx, "s", "q?", types.OpOptions{})
		if err == nil {
			t.Errorf("%s: a call with no key succeeded", name)
			continue
		}
		e := asError(t, "is", err)
		if e.Kind != ErrConfiguration {
			t.Errorf("%s: kind = %v, want ErrConfiguration", name, e.Kind)
		}
		for _, want := range []string{"OPENAI_API_KEY", sharedKeyVariable} {
			if !strings.Contains(e.Message, want) {
				t.Errorf("%s: the message %q does not name %s", name, e.Message, want)
			}
		}
		if e.Op != "is" {
			t.Errorf("%s: Op = %q", name, e.Op)
		}
	}
	if server.requests() != 0 {
		t.Errorf("the server received %d requests, want none", server.requests())
	}
	if err := (Client{}).Warm(context.Background()); err == nil || asError(t, "warm", err).Kind != ErrConfiguration {
		t.Errorf("Warm = %v, want the same configuration error", err)
	}
}

func TestAClientWithAProviderInCodeIsNeverAffectedByTheEnvironment(t *testing.T) {
	server := newLoopback(t)
	useEnvironment(t, map[string]string{
		"SCHEMAFLUX_PROVIDER": "openai", "OPENAI_API_KEY": "environment-key", "OPENAI_BASE_URL": server.URL,
		"SCHEMAFLUX_API_KEY": "shared", "SCHEMAFLUX_MODEL": "environment-model", "SCHEMAFLUX_MODEL_FAST": "fast-model",
	})
	provider := &answerer{name: "custom"}
	c := Client{Provider: provider, Timeout: time.Minute}
	for name, ctx := range map[string]context.Context{
		"on a context": WithClient(context.Background(), c),
		"as the default": func() context.Context {
			useDefault(t, c)
			return context.Background()
		}(),
	} {
		if _, err := ops.Is(ctx, "s", "q?", types.OpOptions{Model: "named"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if server.requests() != 0 {
		t.Errorf("the environment's server received %d requests from a client that has a provider of its own", server.requests())
	}
	for _, req := range provider.sent() {
		if req.Model != "named" {
			t.Errorf("the request asked for %q", req.Model)
		}
	}
	if len(provider.sent()) != 2 {
		t.Errorf("the provider in code received %d requests, want 2", len(provider.sent()))
	}
}

func TestAnEnvironmentTimeoutThatCannotBeUsedFailsEveryClientThatReadsItNamingTheVariable(t *testing.T) {
	const secret = "VALUE-NOT-TO-SHOW"
	useEnvironment(t, map[string]string{"SCHEMAFLUX_TIMEOUT": secret})
	provider := &answerer{name: "custom"}
	for name, c := range map[string]Client{
		"a client with a provider and no timeout": {Provider: provider},
		"a zero client": {},
	} {
		if len(c.Model) == 0 && c.Provider == nil {
			// A key is set, so that the timeout is what is wrong.
			useEnvironment(t, map[string]string{"SCHEMAFLUX_TIMEOUT": secret, "OPENAI_API_KEY": "k"})
		}
		_, err := callIs(WithClient(context.Background(), c))
		if err == nil {
			t.Errorf("%s: succeeded", name)
			continue
		}
		e := asError(t, "is", err)
		if e.Kind != ErrConfiguration || !strings.Contains(e.Message, "SCHEMAFLUX_TIMEOUT") || strings.Contains(e.Error(), secret) {
			t.Errorf("%s: %v", name, e)
		}
	}
	if provider.count() != 0 {
		t.Errorf("%d requests were sent", provider.count())
	}
	t.Run("a client that names a timeout of its own is not affected", func(t *testing.T) {
		if _, err := callIs(WithClient(context.Background(), Client{Provider: provider, Timeout: time.Minute})); err != nil {
			t.Errorf("err = %v", err)
		}
	})
}

func TestTheOnlyPlaceTheRootAsksForTheProcessEnvironmentIsOneFile(t *testing.T) {
	t.Parallel()
	// config.Process is the one reader of the process's variables the root has.
	// A second caller would be a second place a variable is read.
	cell := &environmentCell{}
	if cell.lookup != nil {
		t.Fatal("the process cell has a lookup of its own")
	}
	if cell.environment() != config.Process() {
		t.Error("the process cell does not read the environment config.Process reads")
	}
	if currentEnvironment() == nil {
		t.Error("there is no current environment")
	}
}

func TestEnvironmentProviderAnswersAsTheProviderUnderItAndNamesAFailureBeforeSending(t *testing.T) {
	useEnvironment(t, map[string]string{"SCHEMAFLUX_PROVIDER": "anthropic"})
	p := &environmentProvider{}
	if p.Name() != "anthropic" {
		t.Errorf("Name = %q", p.Name())
	}
	if got := p.ModelFor(types.Fast); got == "" {
		t.Error("with no key the provider has no model for a tier: a call would fail on the model and not on the key")
	}
	if retries, _ := p.RetryPolicy(); retries >= 0 {
		t.Errorf("RetryPolicy retries = %d before there is a provider, want not configured", retries)
	}
	if p.Unwrap() != nil {
		t.Error("Unwrap is not nil with no provider")
	}
	if got := p.ModelSentFor("x"); got != "x" {
		t.Errorf("ModelSentFor = %q", got)
	}
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{}); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("Complete = %v", err)
	}
	for _, err := range p.CompleteStream(context.Background(), llm.CompletionRequest{}) {
		if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
			t.Errorf("CompleteStream = %v", err)
		}
	}
	if p.EstimateCost(llm.CompletionRequest{}) != 0 {
		t.Error("EstimateCost is not zero")
	}
}

func TestEnvironmentProviderWithAKeyIsTheBuiltInProviderBehindIt(t *testing.T) {
	server := newLoopback(t)
	useEnvironment(t, map[string]string{"OPENAI_API_KEY": "k", "OPENAI_BASE_URL": server.URL, "SCHEMAFLUX_MODEL_FAST": "fast-model"})
	p := &environmentProvider{}
	if got := p.ModelFor(types.Fast); got != "fast-model" {
		t.Errorf("ModelFor(Fast) = %q", got)
	}
	if retries, backoff := p.RetryPolicy(); retries != llm.DefaultRetries || backoff != llm.DefaultRetryBackoff {
		t.Errorf("RetryPolicy = %d, %v", retries, backoff)
	}
	if p.Unwrap() == nil || p.Unwrap().Name() != "openai" {
		t.Errorf("Unwrap = %v", p.Unwrap())
	}
	if _, ok := any(p).(llm.StreamingProvider); !ok {
		t.Error("the environment provider is not a streaming provider")
	}
	if _, err := llm.Warm(context.Background(), p); err != nil {
		t.Errorf("Warm = %v", err)
	}
	if got := llm.ModelSentBy(p, "gpt-6-luna"); got == "" {
		t.Error("the model sent is empty")
	}
}

// TestTheTableOfProviderVariablesIsInTheOrderTheParserTriesThem sets every
// variable of a row at once, each to its own value, and holds the parser's
// answer to the first the table lists: the order of a row is a statement about
// which variable wins.
func TestTheTableOfProviderVariablesIsInTheOrderTheParserTriesThem(t *testing.T) {
	t.Parallel()
	for provider, row := range providerVariables {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			values := map[string]string{}
			for i, name := range append(slices.Clone(row.keys), row.baseURLs...) {
				if slices.Contains(row.keys, name) {
					values[name] = fmt.Sprintf("key-%d", i)
				} else {
					values[name] = fmt.Sprintf("http://base-%d.test", i)
				}
			}
			env := config.Read(func(n string) (string, bool) { v, ok := values[n]; return v, ok })
			if got, want := env.Keys[provider], values[row.keys[0]]; got != want {
				t.Errorf("with every key variable set the key is %q, want the first listed, %s (%q)", got, row.keys[0], want)
			}
			if got, want := env.BaseURLs[provider], values[row.baseURLs[0]]; got != want {
				t.Errorf("with every base URL variable set the address is %q, want the first listed, %s (%q)", got, row.baseURLs[0], want)
			}
		})
	}
	t.Run("the table the provider package reads is the same one", func(t *testing.T) {
		t.Parallel()
		for provider, row := range providerVariables {
			keys, baseURLs, ok := bind.ProviderVariables(provider)
			if !ok || !slices.Equal(keys, row.keys) || !slices.Equal(baseURLs, row.baseURLs) {
				t.Errorf("%s: bind.ProviderVariables = %v, %v, %v", provider, keys, baseURLs, ok)
			}
		}
		if _, _, ok := bind.ProviderVariables("nonesuch"); ok {
			t.Error("bind.ProviderVariables knows a provider the table does not")
		}
	})
}

func TestTheMissingKeyMessageSuggestsTheVendorsOwnVariableFirst(t *testing.T) {
	t.Parallel()
	_, err := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": "openai"}).resolve()
	want := `set OPENAI_API_KEY (or SCHEMAFLUX_OPENAI_API_KEY, SCHEMAFLUX_API_KEY), or give the client a Provider`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("got %v, want it to say %q", err, want)
	}
}

// TestARegisteredProviderThatNeedsNoKeyIsBuiltWithNoneAndOneThatDoesIsNot is the
// environment of a program that selects a provider added with
// provider.Register: whether it is built without a key is the provider's own
// declaration.
func TestARegisteredProviderThatNeedsNoKeyIsBuiltWithNoneAndOneThatDoesIsNot(t *testing.T) {
	t.Parallel()
	built := func(config llm.ProviderConfig) (llm.Provider, error) {
		return &answererInternal{name: "built-without-a-key", gotKey: config.APIKey}, nil
	}
	keyless, needsKey := "env-keyless-provider", "env-key-provider"
	for _, name := range []string{keyless, needsKey} {
		if err := llm.RegisterProviderFactory(name, built); err != nil {
			t.Fatal(err)
		}
	}
	bind.DeclareKeyless(keyless)

	got, err := cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": keyless}).resolve()
	if err != nil || got == nil || got.Name() != "built-without-a-key" {
		t.Fatalf("a keyless provider: %v, %v", got, err)
	}
	_, err = cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": needsKey}).resolve()
	if e := asError(t, "extract", err); e.Kind != ErrConfiguration || !strings.Contains(e.Message, sharedKeyVariable) {
		t.Errorf("a provider that needs a key with none set: %v", err)
	}
	got, err = cellOf(map[string]string{"SCHEMAFLUX_PROVIDER": needsKey, sharedKeyVariable: "k"}).resolve()
	if err != nil || got.(*answererInternal).gotKey != "k" {
		t.Errorf("a provider that needs a key with one set: %v, %v", got, err)
	}
}

// answererInternal is an internal provider that remembers the key it was built
// with.
type answererInternal struct {
	name   string
	gotKey string
}

func (p *answererInternal) Name() string { return p.name }

func (p *answererInternal) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}

func (p *answererInternal) EstimateCost(llm.CompletionRequest) float64 { return 0 }

func (p *answererInternal) RetryPolicy() (int, time.Duration) { return -1, 0 }

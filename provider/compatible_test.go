package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func TestOpenAICompatibleBuildsAProviderForAnyServerOfTheChatDialect(t *testing.T) {
	t.Parallel()
	srv := serve(t, replying(200, chatOK))
	p, err := OpenAICompatible("Together", srv.URL+"/v1", Config{APIKey: testKey, Headers: map[string]string{"X-Team": "blue"}})
	if err != nil {
		t.Fatalf("OpenAICompatible: %v", err)
	}
	if p.Name() != "together" {
		t.Errorf("Name() = %q, want the name in lower case", p.Name())
	}
	resp, err := p.Complete(t.Context(), schemaflux.Request{Model: "some-model", System: "SYSTEM", User: "USER", MaxTokens: 77})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "hello" || resp.Model != "chat-test" || resp.Usage.Input != 4 || resp.Usage.Output != 5 || resp.Usage.Total != 9 || resp.FinishReason != "stop" {
		t.Errorf("response = %+v", resp)
	}
	got := srv.last(t)
	if got.method != http.MethodPost || got.path != "/v1/chat/completions" {
		t.Errorf("request went to %s %s, want POST /v1/chat/completions", got.method, got.path)
	}
	if got.header.Get("Authorization") != "Bearer "+testKey || got.header.Get("X-Team") != "blue" {
		t.Errorf("headers = %v", got.header)
	}
	var body struct {
		Model    string `json:"model"`
		Messages []struct{ Role, Content string }
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	if body.Model != "some-model" || len(body.Messages) != 2 || body.Messages[0].Content != "SYSTEM" || body.Messages[1].Content != "USER" {
		t.Errorf("body = %s", got.body)
	}
}

func TestOpenAICompatibleRefusesWhatItCannotBuildFrom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pname   string
		baseURL string
		cfg     Config
		wantMsg string
	}{
		{"an empty name", "", "http://127.0.0.1:1/v1", Config{APIKey: "k"}, "name is empty"},
		{"a name of only space", "  ", "http://127.0.0.1:1/v1", Config{APIKey: "k"}, "name is empty"},
		{"an empty address", "x", "", Config{APIKey: "k"}, "base URL is empty"},
		{"an address of only space", "x", "   ", Config{APIKey: "k"}, "base URL is empty"},
		{"no key", "x", "http://127.0.0.1:1/v1", Config{}, "Config.APIKey is empty"},
		{"a key of only space", "x", "http://127.0.0.1:1/v1", Config{APIKey: " \n"}, "Config.APIKey is empty"},
		{"a key with a control character", "x", "http://127.0.0.1:1/v1", Config{APIKey: "a\x00b"}, "control character"},
		{"a negative timeout", "x", "http://127.0.0.1:1/v1", Config{APIKey: "k", Timeout: -1}, "Config.Timeout is negative"},
		{"a negative backoff", "x", "http://127.0.0.1:1/v1", Config{APIKey: "k", RetryBackoff: -time.Second}, "Config.RetryBackoff is negative"},
		{"a service tier", "x", "http://127.0.0.1:1/v1", Config{APIKey: "k", ServiceTier: "priority"}, ""},
		{"an endpoint policy that refuses the address", "x", "http://127.0.0.1:1/v1", Config{APIKey: "k", Endpoint: &EndpointPolicy{Schemes: []string{"https"}, Hosts: []string{"a.test"}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := OpenAICompatible(tt.pname, tt.baseURL, tt.cfg)
			if err == nil {
				t.Fatalf("got %v, want an error", p)
			}
			if p != nil {
				t.Errorf("returned %v beside its error", p)
			}
			e := errorOf(t, err)
			if e.Kind != schemaflux.ErrConfiguration || e.Op != opCompatible {
				t.Errorf("got %+v, want a configuration error of %s", e, opCompatible)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not say %q", err, tt.wantMsg)
			}
		})
	}
}

func TestOpenAICompatibleReadsNoEnvironmentVariable(t *testing.T) {
	// Not parallel: it sets variables of the process.
	for _, variable := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", sharedKeyVariable, "SCHEMAFLUX_OPENAI_API_KEY"} {
		t.Setenv(variable, "VALUE-from-the-environment")
	}
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/wrong")
	srv := serve(t, replying(200, chatOK))
	p, err := OpenAICompatible("proxy", srv.URL, Config{APIKey: "code-key"})
	if err != nil {
		t.Fatalf("OpenAICompatible: %v", err)
	}
	if _, err := ask(p); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := srv.last(t).header.Get("Authorization"); got != "Bearer code-key" {
		t.Errorf("Authorization = %q, want the key of the Config", got)
	}
	if _, err := OpenAICompatible("proxy", srv.URL, Config{}); err == nil {
		t.Error("a key in the environment stood in for a key that was not given")
	}
}

func TestOpenAICompatibleIgnoresTheBaseURLOfTheConfig(t *testing.T) {
	t.Parallel()
	srv := serve(t, replying(200, chatOK))
	p, err := OpenAICompatible("proxy", srv.URL, Config{APIKey: "k", BaseURL: "http://127.0.0.1:1/wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ask(p); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestOpenAICompatibleHasTheDefaultsOfABuiltProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         Config
		wantRetries int
		wantBackoff time.Duration
		wantTimeout time.Duration
	}{
		{"nothing set", Config{}, 3, time.Second, 30 * time.Second},
		{"retries", Config{Retries: 6}, 6, time.Second, 30 * time.Second},
		{"none", Config{Retries: -1}, -1, time.Second, 30 * time.Second},
		{"a backoff", Config{RetryBackoff: time.Minute}, 3, time.Minute, 30 * time.Second},
		{"a timeout", Config{Timeout: 5 * time.Second}, 3, time.Second, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			probe := newDeadlineProbe()
			cfg := tt.cfg
			cfg.APIKey, cfg.HTTPClient = "k", probe.client
			p, err := OpenAICompatible("proxy", "http://127.0.0.1:1/v1", cfg)
			if err != nil {
				t.Fatal(err)
			}
			retries, backoff := p.(Retrier).RetryPolicy()
			if retries != tt.wantRetries || backoff != tt.wantBackoff {
				t.Errorf("RetryPolicy() = (%d, %v), want (%d, %v)", retries, backoff, tt.wantRetries, tt.wantBackoff)
			}
			_, _ = ask(p)
			left, bounded := probe.remaining()
			if !bounded || left > tt.wantTimeout || left < tt.wantTimeout-5*time.Second {
				t.Errorf("the request had %v left (%v), want a little under %v", left, bounded, tt.wantTimeout)
			}
		})
	}
}

func TestAProviderOfTheChatDialectIsWarmableAndNotADecider(t *testing.T) {
	t.Parallel()
	srv := serve(t, replying(200, chatOK))
	p, err := OpenAICompatible("proxy", srv.URL, Config{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := capabilitiesOf(p), (capabilities{warmer: true, retrier: true}); got != want {
		t.Errorf("capabilities = %+v, want %+v", got, want)
	}
	if err := p.(Warmer).Warm(t.Context()); err != nil {
		t.Errorf("Warm: %v", err)
	}
}

func TestAnErrorOfACompatibleServerIsTheRootsError(t *testing.T) {
	t.Parallel()
	srv := serve(t, replying(429, failingWith("slow"), "Retry-After", "3"))
	p, err := OpenAICompatible("proxy", srv.URL, Config{APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ask(p)
	e := errorOf(t, err)
	if e.Kind != schemaflux.ErrRateLimited || e.RetryAfter != 3*time.Second || e.Provider != "proxy" || !errors.Is(err, schemaflux.ErrRateLimited) {
		t.Errorf("got %+v", e)
	}
	for how, text := range theTextOf(t, err) {
		if strings.Contains(text, testMarker) || strings.Contains(text, testKey) {
			t.Errorf("%s prints the provider's text or the key: %s", how, text)
		}
	}
}

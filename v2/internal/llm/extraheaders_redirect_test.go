package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtraHeadersDoNotCrossOriginsOnRedirect(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"api key", map[string]string{"X-API-Key": "secret"}},
		{"tenant", map[string]string{"X-Tenant": "north"}},
		{"authorization", map[string]string{"Authorization": "Bearer secret"}},
		{"custom auth", map[string]string{"X-Auth-Token": "secret"}},
		{"two headers", map[string]string{"X-API-Key": "secret", "X-Tenant": "north"}},
		{"case insensitive name", map[string]string{"x-api-key": "secret"}},
		{"empty value", map[string]string{"X-Empty": ""}},
		{"custom vendor key", map[string]string{"api-key": "secret"}},
		{"multiple credentials", map[string]string{"X-Key": "one", "X-Secret": "two"}},
		{"trace and credential", map[string]string{"X-Trace": "trace", "X-Credential": "secret"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var received http.Header
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received = r.Header.Clone()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer destination.Close()

			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination.URL+"/landed", http.StatusTemporaryRedirect)
			}))
			defer source.Close()

			client := resolveHTTPClient(ProviderConfig{HTTPClient: &http.Client{}, ExtraHeaders: tc.headers})
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Caller-Trace", "keep-me")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			resp.Body.Close()
			for name := range tc.headers {
				if values, exists := received[http.CanonicalHeaderKey(name)]; exists {
					t.Errorf("cross-origin destination received configured header %s=%q", name, values)
				}
			}
			if got := received.Get("X-Caller-Trace"); got != "keep-me" {
				t.Errorf("unrelated caller header = %q, want keep-me", got)
			}
		})
	}
}

func TestExtraHeadersRedirectRemovesConfiguredNamesFromCallerRequest(t *testing.T) {
	var received http.Header
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client := resolveHTTPClient(ProviderConfig{HTTPClient: &http.Client{}, ExtraHeaders: map[string]string{"X-Tenant": "configured"}})
	req, err := http.NewRequest(http.MethodGet, source.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Tenant", "caller-value")
	req.Header.Set("X-Trace", "keep-me")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if _, exists := received[http.CanonicalHeaderKey("X-Tenant")]; exists {
		t.Errorf("cross-origin destination received a configured ExtraHeader name from the original request: %v", received.Values("X-Tenant"))
	}
	if got := received.Get("X-Trace"); got != "keep-me" {
		t.Errorf("unrelated caller header = %q, want keep-me", got)
	}
}

func TestExtraHeadersRemainOnSameOriginRedirectAndCallerPolicyRuns(t *testing.T) {
	var landings int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/landed", http.StatusTemporaryRedirect)
			return
		}
		landings++
		if got := r.Header.Get("X-Tenant"); got != "acme" {
			t.Errorf("same-origin redirected request header = %q, want acme", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	called := 0
	baseClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			called++
			return nil
		},
	}
	client := resolveHTTPClient(ProviderConfig{HTTPClient: baseClient, ExtraHeaders: map[string]string{"X-Tenant": "acme"}})
	resp, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if landings != 1 || called != 1 {
		t.Errorf("same-origin redirect landings=%d CheckRedirect calls=%d, want 1 each", landings, called)
	}
}

func TestExtraHeadersRedirectPolicyPreservesCallerRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	baseClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return context.Canceled },
	}
	client := resolveHTTPClient(ProviderConfig{HTTPClient: baseClient, ExtraHeaders: map[string]string{"X-Tenant": "acme"}})
	resp, err := client.Get(server.URL)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v, want caller's redirect rejection", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

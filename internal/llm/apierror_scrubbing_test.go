package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// The key is removed where the error is built, in every form a server may write
// it in, so that neither Message nor Body ever holds it.
func TestNewAPIErrorRemovesTheSecretsItIsGiven(t *testing.T) {
	cases := []struct {
		name    string
		secrets []string
		body    string
		// gone are the strings no field may hold afterwards.
		gone []string
		// kept is a string that must survive in Message, so that the removal is
		// shown not to have taken the whole message with it.
		kept string
	}{
		{"in the message", []string{"gw-secret-111"}, `{"error":{"message":"bad key gw-secret-111 given"}}`, []string{"gw-secret-111"}, "bad key"},
		{"in a top-level message", []string{"gw-secret-111"}, `{"message":"bad key gw-secret-111 given"}`, []string{"gw-secret-111"}, "bad key"},
		{"escaped as JSON in the body", []string{`ab"cd\ef`}, `{"error":{"message":"bad key ab\"cd\\ef given"}}`, []string{`ab"cd\ef`, `ab\"cd\\ef`}, "bad key"},
		{"with markup characters", []string{"ab<cd>&ef"}, `{"error":{"message":"bad key ab<cd>&ef given"}}`, []string{"ab<cd>&ef", `ab<cd>&ef`}, "bad key"},
		{"padded with spaces", []string{"  gw-secret-222  "}, `{"error":{"message":"bad key gw-secret-222 given"}}`, []string{"gw-secret-222"}, "bad key"},
		{"twice in one message", []string{"gw-secret-333"}, `{"error":{"message":"gw-secret-333 and again gw-secret-333"}}`, []string{"gw-secret-333"}, "and again"},
		{"two secrets", []string{"gw-secret-444", "org-secret-555"}, `{"error":{"message":"gw-secret-444 org-secret-555"}}`, []string{"gw-secret-444", "org-secret-555"}, "[redacted]"},
		{"one secret the start of another", []string{"gw-secret", "gw-secret-666"}, `{"error":{"message":"key gw-secret-666 given"}}`, []string{"gw-secret-666", "-666"}, "key"},
		{"empty secrets are ignored", []string{"", "  "}, `{"error":{"message":"nothing to remove"}}`, nil, "nothing to remove"},
		{"in the type", []string{"gw-secret-777"}, `{"error":{"message":"m","type":"gw-secret-777"}}`, []string{"gw-secret-777"}, "m"},
		{"in the code", []string{"gw-secret-888"}, `{"error":{"message":"m","code":"gw-secret-888"}}`, []string{"gw-secret-888"}, "m"},
		{"in the param", []string{"gw-secret-999"}, `{"error":{"message":"m","param":"gw-secret-999"}}`, []string{"gw-secret-999"}, "m"},
		{"a key of a shape RedactCredentials knows", []string{"sk-proj-abcdefghijklmnopqrstuvwxyz0123"}, `{"error":{"message":"bad sk-proj-abcdefghijklmnopqrstuvwxyz0123 given"}}`, []string{"sk-proj-abcdefghij"}, "bad"},
		{"a credential of a known shape that is not the configured key", nil, `{"error":{"message":"bad sk-proj-abcdefghijklmnopqrstuvwxyz0123 given"}}`, []string{"sk-proj-abcdefghij"}, "bad"},
		{"the body is not JSON", []string{"gw-secret-000"}, `<html>gw-secret-000</html>`, []string{"gw-secret-000"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewAPIError("acme", "m", 401, tc.body, tc.secrets...)
			for _, field := range []string{err.Message, err.Body, err.Type, err.Code, err.Param, err.Detail(), err.Error()} {
				for _, gone := range tc.gone {
					if strings.Contains(field, gone) {
						t.Errorf("%q is still held: %q", gone, field)
					}
				}
			}
			if !strings.Contains(err.Message, tc.kept) {
				t.Errorf("Message = %q, want it to keep %q", err.Message, tc.kept)
			}
		})
	}
}

// The message is scrubbed before it is cut, so a cut cannot leave the front of
// a key behind.
func TestAKeyAcrossTheMessageCutIsStillRemoved(t *testing.T) {
	key := "gw-secret-ABCDEFGHIJ"
	for offset := 0; offset < len(key); offset++ {
		message := strings.Repeat("x", maxMessageLength-offset) + key + " tail"
		quoted, _ := json.Marshal(message)
		err := NewAPIError("acme", "m", 401, `{"error":{"message":`+string(quoted)+`}}`, key)
		if strings.Contains(err.Message, "gw-secret") || strings.Contains(err.Message, "ABCDEFGHIJ") {
			t.Fatalf("offset %d left part of the key: %q", offset, err.Message[len(err.Message)-40:])
		}
	}
}

func TestTheClassifiersAreBoundedAndPrintable(t *testing.T) {
	long := strings.Repeat("a", 500)
	cases := []struct {
		name      string
		body      string
		wantType  string
		wantCode  string
		wantParam string
	}{
		{"short values are kept", `{"error":{"type":"invalid_request_error","code":"invalid_api_key","param":"messages[0].content"}}`, "invalid_request_error", "invalid_api_key", "messages[0].content"},
		{"a line break is dropped", "{\"error\":{\"type\":\"a\\nb\",\"code\":\"c\\r\\nd\",\"param\":\"p\\nq\"}}", "ab", "cd", "pq"},
		{"control characters are dropped", `{"error":{"type":"a\u0000b\u001bc","code":"x\u0007y","param":"\u0000"}}`, "abc", "xy", ""},
		{"a long type is cut", `{"error":{"type":"` + long + `"}}`, strings.Repeat("a", 64) + "…", "", ""},
		{"a long code is cut", `{"error":{"code":"` + long + `"}}`, "", strings.Repeat("a", 64) + "…", ""},
		{"a long param is cut", `{"error":{"param":"` + long + `"}}`, "", "", strings.Repeat("a", 128) + "…"},
		{"exactly the limit is not cut", `{"error":{"type":"` + strings.Repeat("b", 64) + `"}}`, strings.Repeat("b", 64), "", ""},
		{"multi-byte text is counted in runes", `{"error":{"type":"` + strings.Repeat("é", 70) + `"}}`, strings.Repeat("é", 64) + "…", "", ""},
		{"a numeric code is kept", `{"error":{"code":429}}`, "", "429", ""},
		{"spaces at the ends are trimmed", `{"error":{"type":"  spaced  "}}`, "spaced", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewAPIError("acme", "m", 400, tc.body)
			if err.Type != tc.wantType || err.Code != tc.wantCode || err.Param != tc.wantParam {
				t.Errorf("Type, Code, Param = %q, %q, %q; want %q, %q, %q", err.Type, err.Code, err.Param, tc.wantType, tc.wantCode, tc.wantParam)
			}
		})
	}

	// An APIError built by hand is held to the same bounds when it is printed.
	byHand := &APIError{Provider: "acme", StatusCode: 400, Type: "a\nb" + long, Code: "c\x00d" + long, Param: "p\nq" + long}
	text := byHand.Error()
	if strings.Contains(text, "\n") || strings.Contains(text, "\x00") {
		t.Errorf("a control character reached the text: %q", text)
	}
	if strings.Contains(text, long) {
		t.Errorf("an unbounded classifier reached the text: %q", text)
	}
}

func TestPrintableBoundedCases(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"", 5, ""},
		{"abc", 5, "abc"},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcde…"},
		{"a\tb", 5, "ab"},
		{"a\nb", 5, "ab"},
		{"\x00\x01", 5, ""},
		{" a b ", 5, "a b"},
		{"éééééé", 5, "ééééé…"},
		{"👍👍👍", 2, "👍👍…"},
		{"a​b", 5, "ab"},
		{"­", 5, ""},
	}
	for _, tc := range cases {
		if got := printableBounded(tc.in, tc.limit); got != tc.want {
			t.Errorf("printableBounded(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
}

func TestConfigSecretsCases(t *testing.T) {
	cases := []struct {
		name   string
		config ProviderConfig
		want   []string
	}{
		{"just the key", ProviderConfig{APIKey: "k1"}, []string{"k1"}},
		{"no key", ProviderConfig{}, []string{""}},
		{"an authorization header", ProviderConfig{APIKey: "k1", ExtraHeaders: map[string]string{"Authorization": "Bearer t"}}, []string{"k1", "Bearer t"}},
		{"an api key header", ProviderConfig{APIKey: "k1", ExtraHeaders: map[string]string{"X-Api-Key": "k2"}}, []string{"k1", "k2"}},
		{"a token header", ProviderConfig{APIKey: "k1", ExtraHeaders: map[string]string{"X-Auth-Token": "k3"}}, []string{"k1", "k3"}},
		{"a secret header", ProviderConfig{APIKey: "k1", ExtraHeaders: map[string]string{"x-client-secret": "k4"}}, []string{"k1", "k4"}},
		{"a header that is not a credential", ProviderConfig{APIKey: "k1", ExtraHeaders: map[string]string{"X-Title": "SchemaFlux", "HTTP-Referer": "https://example.test"}}, []string{"k1"}},
		{"the organisation is not a credential", ProviderConfig{APIKey: "k1", OrgID: "org-1"}, []string{"k1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := configSecrets(tc.config)
			sort.Strings(got)
			sort.Strings(tc.want)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("configSecrets = %q, want %q", got, tc.want)
			}
		})
	}
}

// An extra header that carries a credential is removed from the error as the
// key is.
func TestAnExtraCredentialHeaderIsRemovedFromTheError(t *testing.T) {
	url := disclosureServer(t, http.StatusUnauthorized, nil,
		`{"error":{"message":"bad token proxy-token-0a1b2c3d and key `+disclosureKey+`"}}`)
	provider, err := NewOpenAICompatibleProvider("acme", ProviderConfig{
		APIKey: disclosureKey, BaseURL: url,
		ExtraHeaders: map[string]string{"X-Proxy-Token": "proxy-token-0a1b2c3d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := provider.Complete(context.Background(), disclosureRequest())
	var apiErr *APIError
	if !errors.As(callErr, &apiErr) {
		t.Fatalf("not an *APIError: %v", callErr)
	}
	for _, text := range []string{apiErr.Message, apiErr.Body, apiErr.Detail()} {
		if strings.Contains(text, "proxy-token-0a1b2c3d") || strings.Contains(text, disclosureKey) {
			t.Errorf("a credential is still held: %q", text)
		}
	}
}

func TestTheGateIsOneSwitchWithOneMeaning(t *testing.T) {
	cases := []struct {
		value string
		open  bool
	}{
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {"yes", false}, {"2", false},
		{"1", true}, {"true", true}, {"TRUE", true}, {" 1 ", true}, {"True", true},
	}
	for _, tc := range cases {
		t.Run("value "+tc.value, func(t *testing.T) {
			t.Setenv(errorDetailEnvVar, tc.value)
			err := &APIError{Provider: "acme", StatusCode: 400, Message: "the message", Body: `{"body":1}`}
			text := err.Error()
			if got := strings.Contains(text, "the message") && strings.Contains(text, `{"body":1}`); got != tc.open {
				t.Errorf("open = %v, want %v: %s", got, tc.open, text)
			}
			// Neither half opens alone: closed names both as withheld.
			if !tc.open && (!strings.Contains(text, "message withheld (11 bytes)") || !strings.Contains(text, "body withheld (10 bytes)")) {
				t.Errorf("the closed text does not name what it withheld: %s", text)
			}
			// Detail() does not depend on the gate.
			if detail := err.Detail(); !strings.Contains(detail, "message: the message") || !strings.Contains(detail, `body: {"body":1}`) {
				t.Errorf("Detail() = %q", detail)
			}
		})
	}
}

// The exact text, for each status the library reports, with the gate closed and open.
func TestTheTextOfAnAPIErrorIsExact(t *testing.T) {
	body := func(typ, code, param string) string {
		return `{"error":{"message":"the message","type":"` + typ + `","code":"` + code + `","param":"` + param + `"}}`
	}
	cases := []struct {
		name   string
		err    error
		closed string
		open   string
	}{
		{
			name:   "401",
			err:    NewAPIError("openai", "gpt-x", 401, body("invalid_request_error", "invalid_api_key", "")),
			closed: "openai API error (status 401, model gpt-x, type invalid_request_error, code invalid_api_key): message withheld (11 bytes), body withheld (102 bytes); set SCHEMAFLUX_ERROR_DETAIL=1 or call Detail() to read what was withheld",
			open:   "openai API error (status 401, model gpt-x, type invalid_request_error, code invalid_api_key): message: the message; body: " + body("invalid_request_error", "invalid_api_key", ""),
		},
		{
			name:   "400",
			err:    NewAPIError("anthropic", "claude-x", 400, body("invalid_request_error", "", "messages.0.content")),
			closed: "anthropic API error (status 400, model claude-x, type invalid_request_error, param messages.0.content): message withheld (11 bytes), body withheld (105 bytes); set SCHEMAFLUX_ERROR_DETAIL=1 or call Detail() to read what was withheld",
			open:   "anthropic API error (status 400, model claude-x, type invalid_request_error, param messages.0.content): message: the message; body: " + body("invalid_request_error", "", "messages.0.content"),
		},
		{
			name:   "429",
			err:    &RateLimitError{APIError: NewAPIError("cerebras", "m", 429, body("rate_limit_error", "rate_limit_exceeded", "")), RetryAfter: 53 * time.Second},
			closed: "cerebras API error (status 429, retry after 53s, model m, type rate_limit_error, code rate_limit_exceeded): message withheld (11 bytes), body withheld (101 bytes); set SCHEMAFLUX_ERROR_DETAIL=1 or call Detail() to read what was withheld",
			open:   "cerebras API error (status 429, retry after 53s, model m, type rate_limit_error, code rate_limit_exceeded): message: the message; body: " + body("rate_limit_error", "rate_limit_exceeded", ""),
		},
		{
			name:   "500 that is not JSON",
			err:    NewAPIError("openai", "m", 500, "<html>oops</html>"),
			closed: "openai API error (status 500, model m): body withheld (17 bytes); set SCHEMAFLUX_ERROR_DETAIL=1 or call Detail() to read what was withheld",
			open:   "openai API error (status 500, model m): body: <html>oops</html>",
		},
		{
			name:   "500 with nothing to say",
			err:    NewAPIError("openai", "m", 500, ""),
			closed: "openai API error (status 500, model m)",
			open:   "openai API error (status 500, model m)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(errorDetailEnvVar, "")
			if got := tc.err.Error(); got != tc.closed {
				t.Errorf("closed:\n got %s\nwant %s", got, tc.closed)
			}
			t.Setenv(errorDetailEnvVar, "1")
			if got := tc.err.Error(); got != tc.open {
				t.Errorf("open:\n got %s\nwant %s", got, tc.open)
			}
		})
	}
}

func TestMarshalJSONFollowsTheGateAndNamesOnlySafeFields(t *testing.T) {
	err := &RateLimitError{
		APIError:   NewAPIError("cerebras", "m", 429, `{"error":{"message":"the message","type":"rate_limit_error","code":"slow"}}`),
		RetryAfter: 9 * time.Second,
	}
	t.Setenv(errorDetailEnvVar, "")
	closed, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	const wantClosed = `{"provider":"cerebras","model":"m","status":429,"type":"rate_limit_error","code":"slow","retry_after":"9s","message_bytes":11,"body_bytes":75}`
	if string(closed) != wantClosed {
		t.Errorf("closed:\n got %s\nwant %s", closed, wantClosed)
	}
	if plain, _ := json.Marshal(err.APIError); strings.Contains(string(plain), "the message") || strings.Contains(string(plain), "retry_after") {
		t.Errorf("an APIError marshals as %s", plain)
	}

	t.Setenv(errorDetailEnvVar, "1")
	open, _ := json.Marshal(err)
	if !strings.Contains(string(open), `"message":"the message"`) || !strings.Contains(string(open), `"body":`) {
		t.Errorf("open: %s", open)
	}
}

func TestLogValueIsTheSafeText(t *testing.T) {
	t.Setenv(errorDetailEnvVar, "")
	apiErr := NewAPIError("acme", "m", 400, `{"error":{"message":"the message"}}`)
	rateLimit := &RateLimitError{APIError: apiErr, RetryAfter: time.Second}
	if got := apiErr.LogValue().String(); got != apiErr.Error() {
		t.Errorf("APIError.LogValue = %q, want %q", got, apiErr.Error())
	}
	if got := rateLimit.LogValue().String(); got != rateLimit.Error() || !strings.Contains(got, "retry after 1s") {
		t.Errorf("RateLimitError.LogValue = %q, want %q", got, rateLimit.Error())
	}
	if strings.Contains(apiErr.LogValue().String(), "the message") {
		t.Errorf("LogValue carries the message")
	}
}

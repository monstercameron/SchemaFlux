package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// P-007. A non-200 was a fmt.Errorf with the raw body pasted in, so a caller
// who wanted to branch on "429 or 400" had to substring-match an English
// sentence, and the provider's body -- which can quote the caller's own input
// back -- went straight into their logs.

// The recoverability half: the status code is a field, not a substring.
func TestStatusCodeIsRecoverable(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		want   int
		wantOK bool
	}{
		{"direct", &APIError{Provider: "openai", StatusCode: 400}, 400, true},
		{"wrapped", fmt.Errorf("extract failed: %w", &APIError{StatusCode: 422}), 422, true},
		{"double_wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", &APIError{StatusCode: 404})), 404, true},
		{"rate_limit_is_an_api_error_too",
			&RateLimitError{APIError: &APIError{StatusCode: 429}, RetryAfter: time.Second}, 429, true},
		{"wrapped_rate_limit",
			fmt.Errorf("x: %w", &RateLimitError{APIError: &APIError{StatusCode: 503}}), 503, true},
		{"unrelated", errors.New("connection reset"), 0, false},
		{"nil", nil, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := StatusCodeFrom(tc.err)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("StatusCodeFrom = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// A rate limit must satisfy errors.As for BOTH types: a caller branching on
// the status should not have to know this one carries an extra field.
func TestRateLimitSatisfiesBothErrorTypes(t *testing.T) {
	err := error(&RateLimitError{
		APIError:   &APIError{Provider: "cerebras", StatusCode: 429},
		RetryAfter: 53 * time.Second,
	})

	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) {
		t.Error("errors.As did not recover *RateLimitError")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("errors.As did not recover *APIError; a caller branching on status is stuck")
	}
	if apiErr.StatusCode != 429 {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
}

// The disclosure half. The body is not ours: a provider's error can quote the
// input back, and this library runs invoices and medical notes through models.
func TestTheRawBodyIsNotPrintedByDefault(t *testing.T) {
	secret := "patient Jane Okonkwo, DOB 1984-02-11, policy #55512"
	err := NewAPIError("openai", "gpt-5.6-luna", 400,
		`{"error":{"message":"Invalid value for 'input'","type":"invalid_request_error"},`+
			`"echoed":"`+secret+`"}`)

	message := err.Error()
	if strings.Contains(message, secret) {
		t.Errorf("the raw body reached the error string, and from there the caller's logs:\n%s", message)
	}
	if strings.Contains(message, "echoed") {
		t.Errorf("an unrecognised body field was printed:\n%s", message)
	}

	// But the value still HAS it, for a caller who needs it.
	if !strings.Contains(err.Body, secret) {
		t.Error("the body was discarded entirely; it should be retained, just not printed")
	}
	if !strings.Contains(err.Detail(), secret) {
		t.Error("Detail() must include the body; it is the escape hatch")
	}
}

// Withholding the vendor's own explanation would just move the debugging cost
// onto the caller. The split is by shape: what the provider wrote ABOUT the
// request is kept, the request itself is not.
func TestTheVendorsExplanationIsKept(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		message string
		errType string
		code    string
		param   string
	}{
		{
			"openai_nested",
			`{"error":{"message":"Unsupported parameter: 'temperature'","type":"invalid_request_error","param":"temperature","code":null}}`,
			"Unsupported parameter: 'temperature'", "invalid_request_error", "", "temperature",
		},
		{
			"cerebras_top_level",
			`{"message":"Requests per minute limit exceeded","type":"too_many_requests_error","param":"quota","code":"request_quota_exceeded"}`,
			"Requests per minute limit exceeded", "too_many_requests_error", "request_quota_exceeded", "quota",
		},
		{
			"numeric_code",
			`{"error":{"message":"nope","code":429}}`,
			"nope", "", "429", "",
		},
		{
			"not_an_error_shape",
			`{"unrelated":true}`,
			"", "", "", "",
		},
		{
			"not_json_at_all",
			`<html>502 Bad Gateway</html>`,
			"", "", "", "",
		},
		{
			"empty",
			``,
			"", "", "", "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewAPIError("openai", "m", 400, tc.body)
			if err.Message != tc.message {
				t.Errorf("Message = %q, want %q", err.Message, tc.message)
			}
			if err.Type != tc.errType {
				t.Errorf("Type = %q, want %q", err.Type, tc.errType)
			}
			if err.Code != tc.code {
				t.Errorf("Code = %q, want %q", err.Code, tc.code)
			}
			if err.Param != tc.param {
				t.Errorf("Param = %q, want %q", err.Param, tc.param)
			}
		})
	}
}

// A provider that echoes a large input into its message field defeats the
// split above, so the retained message is capped.
func TestALongVendorMessageIsTruncated(t *testing.T) {
	long := strings.Repeat("x", maxMessageLength*3)
	err := NewAPIError("openai", "m", 400, `{"error":{"message":"`+long+`"}}`)

	if len(err.Message) > maxMessageLength+len("… (truncated)") {
		t.Errorf("the message is %d chars; an unbounded quote defeats the redaction", len(err.Message))
	}
	if !strings.Contains(err.Message, "truncated") {
		t.Error("truncation was silent; a reader would think that was the whole message")
	}
}

// A body with nothing to extract must say it was withheld rather than say
// nothing, which reads as "the provider explained nothing".
func TestAnUnparseableBodyIsAnnouncedAsWithheld(t *testing.T) {
	err := NewAPIError("openai", "m", 502, "<html>502 Bad Gateway</html>")

	message := err.Error()
	if strings.Contains(message, "Bad Gateway") {
		t.Errorf("the raw body was printed: %s", message)
	}
	if !strings.Contains(message, "withheld") {
		t.Errorf("the error does not say a body was withheld: %s", message)
	}
	if !strings.Contains(message, errorDetailEnvVar) {
		t.Errorf("the error does not say how to see the body: %s", message)
	}
}

// A developer debugging against their own data can turn it back on.
func TestDetailCanBeEnabledByEnvironment(t *testing.T) {
	t.Setenv(errorDetailEnvVar, "1")

	err := NewAPIError("openai", "m", 502, "<html>502 Bad Gateway</html>")
	if !strings.Contains(err.Error(), "Bad Gateway") {
		t.Errorf("%s=1 did not include the body: %s", errorDetailEnvVar, err.Error())
	}
}

// The parts that identify the request must always be there, or the error names
// no subject.
func TestTheErrorNamesTheRequest(t *testing.T) {
	err := NewAPIError("cerebras", "gemma-4-31b", 404,
		`{"message":"Model does not exist","type":"not_found_error","param":"model"}`)

	message := err.Error()
	for _, want := range []string{"cerebras", "404", "gemma-4-31b", "not_found_error", "model", "Model does not exist"} {
		if !strings.Contains(message, want) {
			t.Errorf("the error does not mention %q: %s", want, message)
		}
	}
}

// The message must be stable across runs; ranging a map to build it is not.
func TestTheErrorStringIsDeterministic(t *testing.T) {
	err := NewAPIError("openai", "m", 400,
		`{"error":{"message":"bad","type":"invalid_request_error","code":"c","param":"p"}}`)

	first := err.Error()
	for i := 0; i < 50; i++ {
		if got := err.Error(); got != first {
			t.Fatalf("the error string varies between calls:\n%s\n%s", first, got)
		}
	}
}

// Retryability stated on the type, so it does not depend on reading prose.
func TestAPIErrorRetryable(t *testing.T) {
	retryable := []int{408, 409, 429, 500, 502, 503, 504}
	for _, status := range retryable {
		if !(&APIError{StatusCode: status}).Retryable() {
			t.Errorf("status %d should be retryable", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 405, 422, 501} {
		if (&APIError{StatusCode: status}).Retryable() {
			t.Errorf("status %d must not be retryable; retrying it wastes the whole budget", status)
		}
	}
}

// End to end, through both transports.
func TestBothTransportsProduceTypedErrors(t *testing.T) {
	body := `{"error":{"message":"Invalid schema: exceeds maximum nesting depth","type":"invalid_request_error"}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	compatible, _ := NewOpenAICompatibleProvider("cerebras", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})
	openai, _ := NewOpenAIProvider(ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})

	for name, provider := range map[string]Provider{"compatible": compatible, "responses": openai} {
		t.Run(name, func(t *testing.T) {
			_, err := provider.Complete(context.Background(),
				CompletionRequest{Model: "gemma-4-31b", UserPrompt: "x"})
			if err == nil {
				t.Fatal("a 400 must be an error")
			}

			status, ok := StatusCodeFrom(err)
			if !ok || status != 400 {
				t.Fatalf("the status is not recoverable from %T: %v", err, err)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatal("not an *APIError")
			}
			if apiErr.Model != "gemma-4-31b" {
				t.Errorf("Model = %q; the error does not say which model was refused", apiErr.Model)
			}
			if !strings.Contains(err.Error(), "nesting depth") {
				t.Errorf("the vendor's explanation was lost: %v", err)
			}
		})
	}
}

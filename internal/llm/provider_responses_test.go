package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/types"
)

// responsesServer stands up a fake Responses API endpoint returning body verbatim.
func responsesServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected the Responses API path, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestOpenAIProvider(t *testing.T, serverURL string) *OpenAIProvider {
	t.Helper()
	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey:  "test-key",
		BaseURL: serverURL,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	return provider
}

// A reasoning model returns reasoning items alongside the message item. Only the
// message text is the answer; concatenating every item corrupts a JSON payload.
func TestOpenAIResponsesIgnoresReasoningOutputItems(t *testing.T) {
	body := `{
	  "id": "resp_1",
	  "status": "completed",
	  "model": "gpt-5.6-luna",
	  "output": [
	    {"type": "reasoning", "content": [{"type": "reasoning_text", "text": "First I identify the name, then the age."}]},
	    {"type": "message", "content": [{"type": "output_text", "text": "{\"name\":\"John\",\"age\":30}"}]}
	  ],
	  "usage": {"input_tokens": 11, "output_tokens": 7, "total_tokens": 18}
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "John Smith is 30 years old.",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	const want = `{"name":"John","age":30}`
	if resp.Content != want {
		t.Fatalf("content must contain only the message text.\n got: %q\nwant: %q", resp.Content, want)
	}

	// The decisive assertion: the result must be parseable. Concatenating the
	// reasoning item leaves leading prose and json.Unmarshal fails.
	var parsed struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &parsed); err != nil {
		t.Fatalf("content is not valid JSON, reasoning text leaked in: %v", err)
	}
	if parsed.Name != "John" || parsed.Age != 30 {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

// Reasoning tokens are billed. Not reading them makes every cost figure on a
// reasoning model wrong, and cached tokens are reported by CostSummary but never
// populated by any provider.
func TestOpenAIResponsesParsesTokenDetails(t *testing.T) {
	body := `{
	  "id": "resp_2",
	  "status": "completed",
	  "model": "gpt-5.6-luna",
	  "output": [{"type": "message", "content": [{"type": "output_text", "text": "ok"}]}],
	  "usage": {
	    "input_tokens": 1200,
	    "output_tokens": 340,
	    "total_tokens": 1540,
	    "input_tokens_details": {"cached_tokens": 1024},
	    "output_tokens_details": {"reasoning_tokens": 256}
	  }
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Usage.CachedTokens != 1024 {
		t.Errorf("CachedTokens = %d, want 1024", resp.Usage.CachedTokens)
	}
	if resp.Usage.ReasoningTokens != 256 {
		t.Errorf("ReasoningTokens = %d, want 256", resp.Usage.ReasoningTokens)
	}
	if resp.Usage.PromptTokens != 1200 || resp.Usage.CompletionTokens != 340 {
		t.Errorf("base token counts wrong: %+v", resp.Usage)
	}
}

// A truncated response must be distinguishable from a complete one. Hardcoding
// "stop" hides the one failure a caller can actually act on.
func TestOpenAIResponsesReportsTruncationFinishReason(t *testing.T) {
	body := `{
	  "id": "resp_3",
	  "status": "incomplete",
	  "incomplete_details": {"reason": "max_output_tokens"},
	  "model": "gpt-5.6-luna",
	  "output": [{"type": "message", "content": [{"type": "output_text", "text": "{\"name\":\"Jo"}]}],
	  "usage": {"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason == "stop" {
		t.Fatalf("FinishReason must reflect truncation, got %q", resp.FinishReason)
	}
	if resp.FinishReason != "max_output_tokens" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "max_output_tokens")
	}
}

// Output items with no message content must not be reported as a successful
// empty completion.
func TestOpenAIResponsesReasoningOnlyIsAnError(t *testing.T) {
	body := `{
	  "id": "resp_4",
	  "status": "completed",
	  "model": "gpt-5.6-luna",
	  "output": [{"type": "reasoning", "content": [{"type": "reasoning_text", "text": "thinking"}]}],
	  "usage": {"input_tokens": 5, "output_tokens": 0, "total_tokens": 5}
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}); err == nil {
		t.Fatal("a response carrying no message item must return an error, not empty content")
	}
}

// One http.Client per provider, not one per request: a fresh client per call
// defeats connection reuse. What shows it is the server's side: three calls,
// one connection. A client built per call would open three.
func TestOpenAIProviderReusesItsConnection(t *testing.T) {
	body := `{"id":"r","status":"completed","model":"gpt-5.6-luna",
	  "output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
	  "usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	for i := 0; i < 3; i++ {
		if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	if got := connections.Load(); got != 1 {
		t.Errorf("three calls opened %d connections; the client is not reused", got)
	}
}

// Reasoning controls must be omitted for the gpt-5.6 family until the accepted
// effort values are verified live: an unrecognised enum fails the whole request,
// while omitting the block is always valid.
func TestGPT56OmitsUnverifiedReasoningControls(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		if supportsReasoningControls(model) {
			t.Errorf("%s: reasoning controls must be omitted until TODOS.md P-013 verifies them", model)
		}
		if supportsTemperature(model, true) {
			t.Errorf("%s: gpt-5 family does not accept temperature", model)
		}
	}
	// Older gpt-5 models keep the controls they were verified with.
	if !supportsReasoningControls("gpt-5.4") {
		t.Error("gpt-5.4 should still send reasoning controls")
	}
}

// A Responses answer whose status is not "completed" or "incomplete" is not a
// finished answer. A missing or unfamiliar status was read as "stop", so a
// failed or cancelled response that carried partial text was returned as a
// complete one, and the error object the API nests in a failed response was
// never decoded.
func TestOpenAIResponsesRefusesAStatusThatIsNotAFinishedAnswer(t *testing.T) {
	const text = `"output":[{"type":"message","content":[{"type":"output_text","text":"partial text"}]}]`
	cases := []struct {
		name      string
		body      string
		kind      types.ErrorKind
		retryable bool
		status    int // the status the failure stands for, 0 when it is not an APIError
	}{
		{"failed with a server error", `{"status":"failed","error":{"code":"server_error","message":"the model failed"},` + text + `}`,
			types.KindProviderUnavailable, true, 500},
		{"failed with a rate limit", `{"status":"failed","error":{"code":"rate_limit_exceeded","message":"slow down"},` + text + `}`,
			types.KindRateLimited, true, 429},
		{"failed with an invalid prompt", `{"status":"failed","error":{"code":"invalid_prompt","message":"bad prompt"},` + text + `}`,
			types.KindInvalidRequest, false, 400},
		{"failed with an image error", `{"status":"failed","error":{"code":"image_too_large","message":"too big"}}`,
			types.KindInvalidRequest, false, 400},
		{"failed with a vector store timeout", `{"status":"failed","error":{"code":"vector_store_timeout","message":"slow"}}`,
			types.KindTimeout, true, 504},
		{"failed with a numeric code", `{"status":"failed","error":{"code":429,"message":"busy"}}`,
			types.KindRateLimited, true, 429},
		{"failed with an unrecognised code", `{"status":"failed","error":{"code":"brand_new","message":"?"}}`,
			types.KindProviderUnavailable, true, 502},
		{"failed with no error object", `{"status":"failed",` + text + `}`,
			types.KindProviderUnavailable, true, 502},
		{"cancelled", `{"status":"cancelled",` + text + `}`, types.KindProviderUnavailable, true, 0},
		{"queued", `{"status":"queued",` + text + `}`, types.KindProviderUnavailable, true, 0},
		{"in progress", `{"status":"in_progress",` + text + `}`, types.KindProviderUnavailable, true, 0},
		{"no status", `{` + text + `}`, types.KindMalformedOutput, false, 0},
		{"empty status", `{"status":"",` + text + `}`, types.KindMalformedOutput, false, 0},
		{"unknown status", `{"status":"banana",` + text + `}`, types.KindMalformedOutput, false, 0},
		{"wrong case", `{"status":"Completed",` + text + `}`, types.KindMalformedOutput, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newTestOpenAIProvider(t, responsesServer(t, tc.body).URL)
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
			if err == nil {
				t.Fatalf("an answer with status %q was returned: %+v", tc.name, resp)
			}
			if !isEmptyResponse(resp) {
				t.Errorf("a result came back beside the error: %+v", resp)
			}
			if got := Classify(err); got != tc.kind {
				t.Errorf("Classify = %v, want %v (%v)", got, tc.kind, err)
			}
			if got := (&types.OperationError{Kind: Classify(err)}).Retryable(); got != tc.retryable {
				t.Errorf("retryable = %v, want %v", got, tc.retryable)
			}
			status, isAPI := StatusCodeFrom(err)
			if isAPI != (tc.status != 0) || status != tc.status {
				t.Errorf("StatusCodeFrom = %d, %v; want %d", status, isAPI, tc.status)
			}
			if strings.Contains(err.Error(), "partial text") {
				t.Errorf("the model's text is in the error: %v", err)
			}
		})
	}
}

// The vendor's own message and code are kept on a failed response, bounded.
func TestOpenAIResponsesFailedKeepsTheVendorsMessageAndCode(t *testing.T) {
	body := `{"status":"failed","error":{"code":"server_error","message":"` + strings.Repeat("ü", maxMessageLength*2) + `"}}`
	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	_, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want an *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "server_error" {
		t.Errorf("Code = %q, want server_error", apiErr.Code)
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(apiErr.Message, "… (truncated)")); n != maxMessageLength {
		t.Errorf("the message holds %d runes, want it cut at %d", n, maxMessageLength)
	}
	if !utf8.ValidString(err.Error()) {
		t.Error("the error string is not valid UTF-8")
	}
}

// A reasoning model given a small ceiling can spend all of it on reasoning and
// end "incomplete" with no text. That is an answer cut off or withheld, it is
// classified, and the same request is not sent again: it was billed for the
// reasoning and would be billed again for the identical outcome.
func TestOpenAIResponsesAnIncompleteAnswerWithNoTextIsClassified(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		kind   types.ErrorKind
		phrase string
	}{
		{"token ceiling", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning"}]}`,
			types.KindOutputTruncated, "cut off"},
		{"token ceiling, no output items at all", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`,
			types.KindOutputTruncated, "cut off"},
		{"content filter", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[{"type":"message","content":[]}]}`,
			types.KindPolicyViolation, "withheld"},
		{"unknown reason", `{"status":"incomplete","incomplete_details":{"reason":"something_new"},"output":[{"type":"reasoning"}]}`,
			types.KindMalformedOutput, "does not recognise"},
		{"no reason", `{"status":"incomplete","output":[{"type":"reasoning"}]}`,
			types.KindMalformedOutput, "does not recognise"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newTestOpenAIProvider(t, responsesServer(t, tc.body).URL)
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
			if err == nil || !isEmptyResponse(resp) {
				t.Fatalf("got %+v, %v; want an empty result and an error", resp, err)
			}
			if got := Classify(err); got != tc.kind {
				t.Errorf("Classify = %v, want %v (%v)", got, tc.kind, err)
			}
			if (&types.OperationError{Kind: Classify(err)}).Retryable() {
				t.Error("the identical request would be stopped the same way and must not be retried")
			}
			if !strings.Contains(err.Error(), tc.phrase) {
				t.Errorf("the error does not say what happened (%q): %v", tc.phrase, err)
			}
			if strings.Contains(err.Error(), "max_output_tokens") || strings.Contains(err.Error(), "something_new") {
				t.Errorf("the vendor's text is in the error: %v", err)
			}
		})
	}
}

// The same classification on the streamed path, which shares the function.
func TestOpenAIStreamAnIncompleteAnswerWithNoTextIsClassified(t *testing.T) {
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		frame := map[string]any{"type": "response.incomplete", "response": map[string]any{
			"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"output": []map[string]any{{"type": "reasoning"}},
		}}
		_, _ = w.Write([]byte(sseFrame(t, frame)))
		f.Flush()
	})
	provider := newTestOpenAIProvider(t, server.URL)
	_, _, err := collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if got := Classify(err); got != types.KindOutputTruncated {
		t.Errorf("Classify = %v, want %v (%v)", got, types.KindOutputTruncated, err)
	}
}

// Timing is a measurement of a call that produced a result. A call that failed
// returns no result, and the library's rule is an empty result beside an error.
func TestOpenAIResponsesTimingTravelsOnlyWithAResult(t *testing.T) {
	ok := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`
	provider := newTestOpenAIProvider(t, responsesServer(t, ok).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Timing.Calls != 1 {
		t.Errorf("Timing.Calls = %d, want 1 on a result", resp.Timing.Calls)
	}

	for name, body := range map[string]string{
		"empty output":        `{"status":"completed","output":[]}`,
		"reasoning only":      `{"status":"completed","output":[{"type":"reasoning"}]}`,
		"incomplete, no text": `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning"}]}`,
		"unrequested tool":    `{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"nope","arguments":"{}"}]}`,
		"failed":              `{"status":"failed","error":{"code":"server_error","message":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !isEmptyResponse(resp) {
				t.Errorf("a result, Timing among it, came back beside the error: %+v", resp)
			}
		})
	}
}

// A body past the ceiling is refused, not held in memory and decoded.
func TestOpenAIResponsesBodyIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"`))
		chunk := []byte(strings.Repeat("a", 1<<20))
		for written := 0; written <= maxResponseBodyBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`"}]}]}`))
	}))
	defer server.Close()
	provider := newTestOpenAIProvider(t, server.URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
	if err == nil {
		t.Fatalf("a body past the ceiling was accepted: %d bytes of content", len(resp.Content))
	}
	if got := Classify(err); got != types.KindMalformedOutput {
		t.Errorf("Classify = %v, want %v (%v)", got, types.KindMalformedOutput, err)
	}
	if !isEmptyResponse(resp) {
		t.Errorf("a result came back beside the error")
	}
}

// A body the size of a large answer is read whole.
func TestOpenAIResponsesALargeAnswerWithinTheCeilingIsRead(t *testing.T) {
	text := strings.Repeat("a", 2<<20)
	body := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"` + text + `"}]}]}`
	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Content) != len(text) {
		t.Errorf("read %d bytes of content, want %d", len(resp.Content), len(text))
	}
}

// A "refusal" content part is the model declining. It is classified as a policy
// violation, is not retried, and carries none of the model's words, whether or not
// text came beside it.
func TestOpenAIResponsesAClassifiedRefusal(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"alone", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"I cannot help with PATIENT-9921"}]}]}`},
		{"beside text", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"partial"},{"type":"refusal","refusal":"I cannot help with PATIENT-9921"}]}]}`},
		{"before text", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"PATIENT-9921"},{"type":"output_text","text":"partial"}]}]}`},
		{"in an untyped item", `{"status":"completed","output":[{"content":[{"type":"refusal","refusal":"PATIENT-9921"}]}]}`},
		{"after reasoning", `{"status":"completed","output":[{"type":"reasoning"},{"type":"message","content":[{"type":"refusal","refusal":"PATIENT-9921"}]}]}`},
		{"with a tool call", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"PATIENT-9921"}]},{"type":"function_call","call_id":"c","name":"t","arguments":"{}"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newTestOpenAIProvider(t, responsesServer(t, tc.body).URL)
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x", Tools: []Tool{{Name: "t"}}})
			if err == nil || !isEmptyResponse(resp) {
				t.Fatalf("got %+v, %v; want an empty result and an error", resp, err)
			}
			if got := Classify(err); got != types.KindPolicyViolation {
				t.Errorf("Classify = %v, want %v (%v)", got, types.KindPolicyViolation, err)
			}
			if (&types.OperationError{Kind: Classify(err)}).Retryable() {
				t.Error("a refusal is the same on retry")
			}
			if strings.Contains(err.Error(), "PATIENT-9921") || strings.Contains(err.Error(), "partial") {
				t.Errorf("the model's words are in the error: %v", err)
			}
		})
	}
}

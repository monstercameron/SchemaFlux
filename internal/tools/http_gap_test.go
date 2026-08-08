package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// executeFetch and executePost read timeout and headers from params when
// present, rather than only from the zero-value defaults the other tests
// exercise.
func TestExecuteFetchHonoursTimeoutAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom") != "yes" {
			t.Errorf("custom header did not reach the server")
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	result, err := FetchTool.Execute(context.Background(), map[string]any{
		"url":     server.URL,
		"timeout": float64(5),
		"headers": `{"X-Custom":"yes"}`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
}

func TestExecuteFetchReportsATransportError(t *testing.T) {
	result, err := FetchTool.Execute(context.Background(), map[string]any{
		"url": "http://127.0.0.1:1", // nothing listens here
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("a request to a closed port must fail")
	}
}

func TestExecutePostRequiresAURL(t *testing.T) {
	result, err := PostTool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for a missing url")
	}
}

func TestExecutePostHonoursContentTypeTimeoutAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "text/plain" {
			t.Errorf("Content-Type = %q, want text/plain", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Extra") != "yes" {
			t.Errorf("extra header did not merge into the request")
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	result, err := PostTool.Execute(context.Background(), map[string]any{
		"url":          server.URL,
		"body":         "plain text",
		"content_type": "text/plain",
		"timeout":      float64(5),
		"headers":      `{"X-Extra":"yes"}`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
}

func TestExecutePostReportsATransportError(t *testing.T) {
	result, err := PostTool.Execute(context.Background(), map[string]any{
		"url": "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("a request to a closed port must fail")
	}
}

// doRequest validates the URL before dialing anything -- a malformed URL is
// refused rather than handed to the transport.
func TestDoRequestRefusesAMalformedURL(t *testing.T) {
	_, err := doRequest(context.Background(), "GET", "http://%zz", "", nil, 0)
	if err == nil {
		t.Fatal("a malformed URL must be refused before any network activity")
	}
}

func TestDoRequestRefusesAnInvalidMethod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// A method containing a space is not a valid HTTP token, and
	// http.NewRequestWithContext refuses to build the request.
	_, err := doRequest(context.Background(), "G ET", server.URL, "", nil, 0)
	if err == nil {
		t.Fatal("an invalid HTTP method must be refused")
	}
}

// executeWebhook refuses a payload that is not valid JSON before it ever
// reaches the network, and it honours the method parameter.
func TestExecuteWebhookRefusesInvalidJSONPayload(t *testing.T) {
	result, err := WebhookTool.Execute(context.Background(), map[string]any{
		"url":     "http://127.0.0.1:1",
		"payload": `{not json`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an invalid JSON payload must be refused")
	}
}

func TestExecuteWebhookHonoursMethodAndReportsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Method = %q, want PUT", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result, err := WebhookTool.Execute(context.Background(), map[string]any{
		"url":     server.URL,
		"payload": `{"event":"test"}`,
		"method":  "PUT",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	// And the transport-error path.
	result, err = WebhookTool.Execute(context.Background(), map[string]any{
		"url":     "http://127.0.0.1:1",
		"payload": `{"event":"test"}`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("a webhook to a closed port must fail")
	}
}

// EncodeURLTool's decode action reports a failure for a malformed escape
// rather than silently returning something else, and an unknown action is
// refused outright.
func TestExecuteEncodeURLDecodeError(t *testing.T) {
	result, err := EncodeURLTool.Execute(context.Background(), map[string]any{
		"action": "decode",
		"text":   "%zz",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("a malformed percent-escape must fail to decode")
	}
}

func TestExecuteEncodeURLRefusesAnUnknownAction(t *testing.T) {
	result, err := EncodeURLTool.Execute(context.Background(), map[string]any{
		"action": "rot13",
		"text":   "hello",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an unrecognised action must be refused, not guessed at")
	}
}

// BuildURLTool refuses an unparsable base URL and an unparsable params
// object, and it is a no-op on the query string when params is empty.
func TestExecuteBuildURLRefusesAnInvalidBase(t *testing.T) {
	result, err := BuildURLTool.Execute(context.Background(), map[string]any{
		"base": "http://%zz",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an invalid base URL must be refused")
	}
}

func TestExecuteBuildURLRefusesInvalidParamsJSON(t *testing.T) {
	result, err := BuildURLTool.Execute(context.Background(), map[string]any{
		"base":   "https://api.example.com/search",
		"params": `{not json`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("invalid params JSON must be refused")
	}
}

func TestExecuteBuildURLWithNoParamsLeavesTheURLUnchanged(t *testing.T) {
	result, err := BuildURLTool.Execute(context.Background(), map[string]any{
		"base": "https://api.example.com/search",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.Data.(string) != "https://api.example.com/search" {
		t.Errorf("got %q", result.Data)
	}
}

// JSONRequest reports a marshal failure rather than sending a broken body --
// a channel cannot be encoded as JSON.
func TestJSONRequestReportsAMarshalFailure(t *testing.T) {
	_, err := JSONRequest(context.Background(), "POST", "http://127.0.0.1:1", make(chan int), nil)
	if err == nil {
		t.Fatal("a value that cannot be marshalled to JSON must fail before any request is sent")
	}
}

func TestJSONRequestReportsATransportFailure(t *testing.T) {
	_, err := JSONRequest(context.Background(), "POST", "http://127.0.0.1:1", map[string]string{"a": "b"}, nil)
	if err == nil {
		t.Fatal("a request to a closed port must fail")
	}
}

// DownloadFile refuses a non-200 response rather than returning whatever
// error body the server sent as if it were the file.
func TestDownloadFileRefusesANonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer server.Close()

	_, _, err := DownloadFile(context.Background(), server.URL, 0)
	if err == nil {
		t.Fatal("a 404 response must not be treated as a successful download")
	}
}

func TestDownloadFileReportsATransportFailure(t *testing.T) {
	_, _, err := DownloadFile(context.Background(), "http://127.0.0.1:1", 0)
	if err == nil {
		t.Fatal("a request to a closed port must fail")
	}
}

func TestDownloadFileRefusesAMalformedURL(t *testing.T) {
	_, _, err := DownloadFile(context.Background(), "http://%zz", 0)
	if err == nil {
		t.Fatal("a malformed URL must be refused before any network activity")
	}
}

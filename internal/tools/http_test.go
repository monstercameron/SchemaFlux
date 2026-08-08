package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetch(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET, got %s", r.Method)
		}
		w.Header().Set("X-Test", "value")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "hello"}`))
	}))
	defer server.Close()

	resp, err := Fetch(context.Background(), server.URL, nil, 10*1000*1000*1000)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}
	if resp.Headers["X-Test"] != "value" {
		t.Errorf("Missing X-Test header")
	}
	if resp.BodyJSON == nil {
		t.Error("Expected JSON body")
	}
}

func TestPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected application/json content type")
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id": 1}`))
	}))
	defer server.Close()

	resp, err := Post(context.Background(), server.URL, `{"name": "test"}`, map[string]string{
		"Content-Type": "application/json",
	}, 10*1000*1000*1000)
	if err != nil {
		t.Fatalf("Post error: %v", err)
	}

	if resp.StatusCode != 201 {
		t.Errorf("Expected 201, got %d", resp.StatusCode)
	}
}

func TestFetchTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`ok`))
	}))
	defer server.Close()

	result, err := FetchTool.Execute(context.Background(), map[string]any{
		"url": server.URL,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	resp := result.Data.(*HTTPResponse)
	if resp.Body != "ok" {
		t.Errorf("Expected 'ok', got %q", resp.Body)
	}
}

func TestPostTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`created`))
	}))
	defer server.Close()

	result, err := PostTool.Execute(context.Background(), map[string]any{
		"url":  server.URL,
		"body": `{"test": true}`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}
}

func TestEncodeURL(t *testing.T) {
	// Test encode
	result, _ := EncodeURLTool.Execute(context.Background(), map[string]any{
		"action": "encode",
		"text":   "hello world",
	})
	if result.Data != "hello+world" {
		t.Errorf("Expected 'hello+world', got %q", result.Data)
	}

	// Test decode
	result, _ = EncodeURLTool.Execute(context.Background(), map[string]any{
		"action": "decode",
		"text":   "hello%20world",
	})
	if result.Data != "hello world" {
		t.Errorf("Expected 'hello world', got %q", result.Data)
	}
}

func TestBuildURL(t *testing.T) {
	result, _ := BuildURLTool.Execute(context.Background(), map[string]any{
		"base":   "https://api.example.com/search",
		"params": `{"q": "test", "page": "1"}`,
	})

	if !result.Success {
		t.Fatalf("Expected success, got error: %s", result.Error)
	}

	urlStr := result.Data.(string)
	if urlStr != "https://api.example.com/search?page=1&q=test" &&
		urlStr != "https://api.example.com/search?q=test&page=1" {
		t.Errorf("Unexpected URL: %s", urlStr)
	}
}

func TestWebhookTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"received": true}`))
	}))
	defer server.Close()

	result, err := WebhookTool.Execute(context.Background(), map[string]any{
		"url":     server.URL,
		"payload": `{"event": "test"}`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}
}

func TestFetchInvalidURL(t *testing.T) {
	result, _ := FetchTool.Execute(context.Background(), map[string]any{
		"url": "",
	})
	if result.Success {
		t.Error("Expected failure for empty URL")
	}
}

func TestJSONRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("Expected JSON content type")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Error("Expected JSON accept header")
		}
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	resp, err := JSONRequest(context.Background(), "POST", server.URL, map[string]string{"key": "value"}, nil)
	if err != nil {
		t.Fatalf("JSONRequest error: %v", err)
	}
	if resp.BodyJSON == nil {
		t.Error("Expected JSON body")
	}
}

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("file content"))
	}))
	defer server.Close()

	data, contentType, err := DownloadFile(context.Background(), server.URL, 10*1000*1000*1000)
	if err != nil {
		t.Fatalf("DownloadFile error: %v", err)
	}
	if string(data) != "file content" {
		t.Errorf("Expected 'file content', got %q", string(data))
	}
	if contentType != "text/plain" {
		t.Errorf("Expected 'text/plain', got %q", contentType)
	}
}

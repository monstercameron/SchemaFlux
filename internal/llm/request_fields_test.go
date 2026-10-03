package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Anthropic sends a system prompt, a user prompt and a few settings, and nothing
// else, and the chat-completions dialect carries tools and a transcript but has no
// search. A request that set a field the provider cannot send was sent without it:
// a caller who offered tools got a plain answer with no calls in it, and one who
// passed a transcript had an empty user prompt sent in its place. Such a request
// is refused before anything is sent, and the error names the field and never its
// value.

func TestRequestsThatSetFieldsAProviderCannotSendAreRefused(t *testing.T) {
	const secret = "CALLER-SECRET-8841"

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	anthropic := newTestAnthropicProvider(t, server.URL)
	chat, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	providers := map[string]Provider{"anthropic": anthropic, "chat dialect": chat}

	cases := []struct {
		name   string
		mutate func(*CompletionRequest)
		fields []string
		// chatFields are the fields the chat dialect cannot send, which are fewer:
		// it carries tools, a tool choice and a transcript.
		chatFields []string
	}{
		{"tools", func(r *CompletionRequest) { r.Tools = []Tool{{Name: secret}} }, []string{"Tools"}, nil},
		{"tool choice", func(r *CompletionRequest) { r.ToolChoice = secret }, []string{"ToolChoice"}, nil},
		{"web search", func(r *CompletionRequest) { r.WebSearch = true }, []string{"WebSearch"}, []string{"WebSearch"}},
		{"messages", func(r *CompletionRequest) { r.Messages = []Message{{Role: "user", Content: secret}} }, []string{"Messages"}, nil},
		{"tools and tool choice", func(r *CompletionRequest) {
			r.Tools = []Tool{{Name: "t"}}
			r.ToolChoice = "required"
		}, []string{"Tools", "ToolChoice"}, nil},
		{"all four", func(r *CompletionRequest) {
			r.Tools = []Tool{{Name: "t"}}
			r.ToolChoice = "auto"
			r.WebSearch = true
			r.Messages = []Message{{Role: "user", Content: "hi"}}
		}, []string{"Tools", "ToolChoice", "WebSearch", "Messages"}, []string{"WebSearch"}},
		{"messages with an empty user prompt", func(r *CompletionRequest) {
			r.UserPrompt = ""
			r.Messages = []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
		}, []string{"Messages"}, nil},
	}
	for providerName, provider := range providers {
		for _, tc := range cases {
			fields := tc.fields
			if providerName == "chat dialect" {
				fields = tc.chatFields
			}
			if len(fields) == 0 {
				continue
			}
			t.Run(providerName+"/"+tc.name, func(t *testing.T) {
				before := calls.Load()
				req := CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u"}
				tc.mutate(&req)
				resp, err := provider.Complete(context.Background(), req)
				if err == nil {
					t.Fatalf("a request setting %v was sent: %+v", fields, resp)
				}
				if !isEmptyResponse(resp) {
					t.Errorf("a result came back beside the error")
				}
				if got := Classify(err); got != types.KindUnsupportedCapability {
					t.Errorf("Classify = %v, want %v (%v)", got, types.KindUnsupportedCapability, err)
				}
				for _, field := range fields {
					if !strings.Contains(err.Error(), field) {
						t.Errorf("the error does not name %s: %v", field, err)
					}
				}
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error carries the caller's value: %v", err)
				}
				if calls.Load() != before {
					t.Error("the request was sent before it was refused")
				}
				if (&types.OperationError{Kind: Classify(err)}).Retryable() {
					t.Error("a request that cannot be sent is the same on retry")
				}
			})
		}
	}
}

// Fields these providers can carry, and fields that are only hints, are not
// refused.
func TestRequestsThatSetOnlyFieldsAProviderCanSendAreNotRefused(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","model":"m"}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	anthropic := newTestAnthropicProvider(t, server.URL)
	chat, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second})

	cases := []struct {
		name string
		req  CompletionRequest
	}{
		{"plain", CompletionRequest{Model: "m", UserPrompt: "u"}},
		{"system prompt and ceiling", CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u", MaxTokens: 50}},
		{"temperature", CompletionRequest{Model: "m", UserPrompt: "u", Temperature: 0.4}},
		{"a cache key is a hint", CompletionRequest{Model: "m", UserPrompt: "u", PromptCacheKey: "k"}},
		{"an operation's effort preference", CompletionRequest{Model: "m", UserPrompt: "u", ReasoningEffort: "none", ReasoningEffortPreferred: true}},
		{"an empty tools list", CompletionRequest{Model: "m", UserPrompt: "u", Tools: []Tool{}}},
		{"an empty transcript", CompletionRequest{Model: "m", UserPrompt: "u", Messages: []Message{}}},
	}
	for _, tc := range cases {
		for name, provider := range map[string]Provider{"anthropic": anthropic, "chat dialect": chat} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				if _, err := provider.Complete(context.Background(), tc.req); err != nil {
					t.Fatalf("Complete: %v", err)
				}
			})
		}
	}
	if calls.Load() == 0 {
		t.Fatal("no request reached the server")
	}
}

func TestUnsupportedRequestFieldsReportsOnlyTheFieldsSet(t *testing.T) {
	cases := []struct {
		name   string
		req    CompletionRequest
		fields []string
		want   string // "" for no error
	}{
		{"nothing set", CompletionRequest{}, []string{"Tools", "ToolChoice", "WebSearch", "Messages"}, ""},
		{"tools set but not checked", CompletionRequest{Tools: []Tool{{Name: "a"}}}, []string{"WebSearch"}, ""},
		{"tools checked", CompletionRequest{Tools: []Tool{{Name: "a"}}}, []string{"Tools"}, "Tools"},
		{"tool choice", CompletionRequest{ToolChoice: "none"}, []string{"ToolChoice"}, "ToolChoice"},
		{"web search", CompletionRequest{WebSearch: true}, []string{"WebSearch"}, "WebSearch"},
		{"messages", CompletionRequest{Messages: []Message{{}}}, []string{"Messages"}, "Messages"},
		{"two of three", CompletionRequest{Tools: []Tool{{}}, WebSearch: true}, []string{"Tools", "ToolChoice", "WebSearch"}, "Tools, WebSearch"},
		{"an unknown field name is never reported", CompletionRequest{Tools: []Tool{{}}}, []string{"Banana"}, ""},
		{"no fields checked", CompletionRequest{Tools: []Tool{{}}, WebSearch: true}, nil, ""},
		{"provider is named", CompletionRequest{WebSearch: true}, []string{"WebSearch"}, "myprovider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := unsupportedRequestFields(tc.req, "myprovider", tc.fields...)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The local provider answers from a handler, or from the shape the request
// declares, and the handler is given the request and returns text. It has no way
// to return a tool call and no conversation to read: a transcript in Messages was
// answered as the empty user prompt, and tools were ignored. It refuses such a
// request the way the providers that speak a wire do, before the handler runs.
func TestLocalProviderRefusesWhatItCannotAnswer(t *testing.T) {
	const secret = "CALLER-SECRET-3392"

	cases := []struct {
		name   string
		mutate func(*CompletionRequest)
		fields []string
	}{
		{"tools", func(r *CompletionRequest) { r.Tools = []Tool{{Name: secret}} }, []string{"Tools"}},
		{"tool choice", func(r *CompletionRequest) { r.ToolChoice = secret }, []string{"ToolChoice"}},
		{"web search", func(r *CompletionRequest) { r.WebSearch = true }, []string{"WebSearch"}},
		{"messages", func(r *CompletionRequest) { r.Messages = []Message{{Role: "user", Content: secret}} }, []string{"Messages"}},
		{"messages with an empty user prompt", func(r *CompletionRequest) {
			r.UserPrompt = ""
			r.Messages = []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
		}, []string{"Messages"}},
		{"a tool result", func(r *CompletionRequest) {
			r.Messages = []Message{{Role: "tool", Content: secret, ToolCallID: "call_1"}}
		}, []string{"Messages"}},
		{"tools and tool choice", func(r *CompletionRequest) {
			r.Tools = []Tool{{Name: "t"}}
			r.ToolChoice = "required"
		}, []string{"Tools", "ToolChoice"}},
		{"tools and messages", func(r *CompletionRequest) {
			r.Tools = []Tool{{Name: "t"}}
			r.Messages = []Message{{Role: "user", Content: "hi"}}
		}, []string{"Tools", "Messages"}},
		{"all four", func(r *CompletionRequest) {
			r.Tools = []Tool{{Name: "t"}}
			r.ToolChoice = "auto"
			r.WebSearch = true
			r.Messages = []Message{{Role: "user", Content: "hi"}}
		}, []string{"Tools", "ToolChoice", "WebSearch", "Messages"}},
	}
	for _, scripted := range []bool{false, true} {
		for _, tc := range cases {
			name := "default mock/"
			if scripted {
				name = "handler/"
			}
			t.Run(name+tc.name, func(t *testing.T) {
				var handled atomic.Int32
				provider, err := NewLocalProvider(ProviderConfig{})
				if err != nil {
					t.Fatal(err)
				}
				if scripted {
					provider.WithHandler(func(context.Context, CompletionRequest) (string, error) {
						handled.Add(1)
						return "answer", nil
					})
				}
				req := CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u"}
				tc.mutate(&req)

				resp, err := provider.Complete(context.Background(), req)
				if err == nil {
					t.Fatalf("a request setting %v was answered: %+v", tc.fields, resp)
				}
				if !isEmptyResponse(resp) {
					t.Errorf("a result came back beside the error")
				}
				if got := Classify(err); got != types.KindUnsupportedCapability {
					t.Errorf("Classify = %v, want %v (%v)", got, types.KindUnsupportedCapability, err)
				}
				for _, field := range tc.fields {
					if !strings.Contains(err.Error(), field) {
						t.Errorf("the error does not name %s: %v", field, err)
					}
				}
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error carries the caller's value: %v", err)
				}
				if handled.Load() != 0 {
					t.Error("the handler ran for a request the provider cannot answer")
				}
			})
		}
	}
}

// A request that sets none of those fields is answered as it always was.
func TestLocalProviderStillAnswersARequestThatSetsNoneOfThem(t *testing.T) {
	cases := []CompletionRequest{
		{Model: "m", UserPrompt: "u"},
		{Model: "m", SystemPrompt: "s", UserPrompt: "u"},
		{Model: "m", UserPrompt: "u", Tools: []Tool{}},
		{Model: "m", UserPrompt: "u", Messages: []Message{}},
		{Model: "m", UserPrompt: "u", ResponseFormat: "json"},
	}
	for i, req := range cases {
		provider, _ := NewLocalProvider(ProviderConfig{})
		provider.WithHandler(func(context.Context, CompletionRequest) (string, error) { return "answer", nil })
		resp, err := provider.Complete(context.Background(), req)
		if err != nil || resp.Content != "answer" {
			t.Errorf("case %d: %q, %v", i, resp.Content, err)
		}
	}
}

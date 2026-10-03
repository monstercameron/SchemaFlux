package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// What a turn sends is built from the turn and the options, and from nothing
// else: the instructions as they were written, the conversation as its messages,
// text as the format, and none of what an operation's request carries.
func TestATurnRequestIsBuiltFromTheTurn(t *testing.T) {
	weather := llm.Tool{
		Name: "get_weather", Description: "Look up the weather.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
	}
	history := []llm.Message{
		{Role: "user", Content: "How warm is Oslo?"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{weatherCall("call_1")}},
		{Role: "tool", ToolCallID: "call_1", Content: "7C"},
	}
	cases := []struct {
		name  string
		turn  Turn
		opts  func(*types.OpOptions)
		check func(t *testing.T, req llm.CompletionRequest)
	}{
		{"the instructions are the system prompt, as written", Turn{Instructions: "You are terse. Return JSON if asked.", Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.SystemPrompt != "You are terse. Return JSON if asked." {
					t.Errorf("system = %q", req.SystemPrompt)
				}
			}},
		{"no instructions is no system prompt", Turn{Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.SystemPrompt != "" {
					t.Errorf("system = %q, want none", req.SystemPrompt)
				}
			}},
		{"no reinforcement block is added", Turn{Instructions: "Answer in one word.", Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.SystemPrompt != "Answer in one word." {
					t.Errorf("the instructions were changed to %q", req.SystemPrompt)
				}
			}},
		{"the conversation is the messages and nothing is in the user prompt", Turn{Messages: history}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.UserPrompt != "" {
					t.Errorf("user prompt = %q, want none", req.UserPrompt)
				}
				if len(req.Messages) != 3 || req.Messages[1].ToolCalls[0].ID != "call_1" || req.Messages[2].ToolCallID != "call_1" {
					t.Errorf("messages = %+v", req.Messages)
				}
			}},
		{"the format is text with no schema, tool choice or decision", Turn{Messages: history[:1], Tools: []llm.Tool{weather}}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.ResponseFormat != "text" || req.JSONSchema != nil || req.SchemaName != "" || req.ToolChoice != "" || req.Decision != nil {
					t.Errorf("format %q, schema %v %q, tool choice %q, decision %v", req.ResponseFormat, req.JSONSchema, req.SchemaName, req.ToolChoice, req.Decision)
				}
			}},
		{"an instruction that mentions JSON does not make the turn a JSON request", Turn{Instructions: "Reply with a JSON object of the tool arguments.", Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.ResponseFormat != "text" {
					t.Errorf("format = %q", req.ResponseFormat)
				}
			}},
		{"the tools are the tools", Turn{Messages: history[:1], Tools: []llm.Tool{weather}}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" || req.Tools[0].Parameters["type"] != "object" {
					t.Errorf("tools = %+v", req.Tools)
				}
			}},
		{"no tools is no tools", Turn{Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.Tools != nil {
					t.Errorf("tools = %+v", req.Tools)
				}
			}},
		{"the model is the one on the options", Turn{Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.Model != "chat-model" {
					t.Errorf("model = %q", req.Model)
				}
			}},
		{"the tier's ceiling is the default ceiling", Turn{Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if want := config.GetMaxTokens(types.Smart); req.MaxTokens != want {
					t.Errorf("max tokens = %d, want the smart tier's %d", req.MaxTokens, want)
				}
			}},
		{"a ceiling on the options wins", Turn{Messages: history[:1]}, func(o *types.OpOptions) { o.MaxOutputTokens = 321 },
			func(t *testing.T, req llm.CompletionRequest) {
				if req.MaxTokens != 321 {
					t.Errorf("max tokens = %d, want 321", req.MaxTokens)
				}
			}},
		{"the mode sets the temperature", Turn{Messages: history[:1]}, func(o *types.OpOptions) { o.Mode = types.Creative },
			func(t *testing.T, req llm.CompletionRequest) {
				if want := float64(config.GetTemperature(types.Creative)); req.Temperature != want {
					t.Errorf("temperature = %v, want %v", req.Temperature, want)
				}
			}},
		{"web search is declared when asked for", Turn{Messages: history[:1]}, func(o *types.OpOptions) { o.WebSearch = true },
			func(t *testing.T, req llm.CompletionRequest) {
				if !req.WebSearch {
					t.Error("web search is not declared")
				}
			}},
		{"an effort on the options is a requirement", Turn{Messages: history[:1]}, func(o *types.OpOptions) { o.Effort = types.EffortLow },
			func(t *testing.T, req llm.CompletionRequest) {
				if req.ReasoningEffort != "low" || req.ReasoningEffortPreferred {
					t.Errorf("effort %q preferred %v, want low as a requirement", req.ReasoningEffort, req.ReasoningEffortPreferred)
				}
			}},
		{"no effort is none asked for", Turn{Messages: history[:1]}, nil,
			func(t *testing.T, req llm.CompletionRequest) {
				if req.ReasoningEffort != "" {
					t.Errorf("effort = %q", req.ReasoningEffort)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &chatScript{steps: []chatStep{{resp: llm.CompletionResponse{Content: "ok"}}}}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})
			opts := callOptions()
			if tc.opts != nil {
				tc.opts(&opts)
			}
			if _, err := CallTurn(ctx, tc.turn, opts); err != nil {
				t.Fatalf("CallTurn: %v", err)
			}
			sent := provider.sent()
			if len(sent) != 1 {
				t.Fatalf("%d requests", len(sent))
			}
			tc.check(t, sent[0])
		})
	}
}

// The cache key is what the turns of a conversation share: it is the same for
// every turn of one conversation and different for another, and it never holds
// the history.
func TestATurnCacheKeyIsWhatTheTurnsOfAConversationShare(t *testing.T) {
	weather := llm.Tool{Name: "get_weather", Description: "d", Parameters: map[string]any{"b": 1, "a": map[string]any{"y": 2, "x": 1}}}
	reordered := llm.Tool{Name: "get_weather", Description: "d", Parameters: map[string]any{"a": map[string]any{"x": 1, "y": 2}, "b": 1}}
	user := func(text string) []llm.Message { return []llm.Message{{Role: "user", Content: text}} }

	base := turnCacheKey("m", "be terse", []llm.Tool{weather})
	same := []struct {
		name string
		key  string
	}{
		{"the same declaration again", turnCacheKey("m", "be terse", []llm.Tool{weather})},
		{"a schema whose keys were written in another order", turnCacheKey("m", "be terse", []llm.Tool{reordered})},
	}
	for _, tc := range same {
		if tc.key != base {
			t.Errorf("%s: %q != %q", tc.name, tc.key, base)
		}
	}
	different := []struct {
		name string
		key  string
	}{
		{"another model", turnCacheKey("n", "be terse", []llm.Tool{weather})},
		{"other instructions", turnCacheKey("m", "be wordy", []llm.Tool{weather})},
		{"no tools", turnCacheKey("m", "be terse", nil)},
		{"a tool with another name", turnCacheKey("m", "be terse", []llm.Tool{{Name: "get_time", Description: "d", Parameters: weather.Parameters}})},
		{"a tool with another description", turnCacheKey("m", "be terse", []llm.Tool{{Name: "get_weather", Description: "e", Parameters: weather.Parameters}})},
		{"a tool with another schema", turnCacheKey("m", "be terse", []llm.Tool{{Name: "get_weather", Description: "d", Parameters: map[string]any{"a": 1}}})},
		{"the text moved between instructions and a tool", turnCacheKey("m", "be ters", []llm.Tool{{Name: "e", Description: "d", Parameters: weather.Parameters}})},
	}
	seen := map[string]string{base: "the base"}
	for _, tc := range different {
		if previous, clash := seen[tc.key]; clash {
			t.Errorf("%s has the key of %s", tc.name, previous)
		}
		seen[tc.key] = tc.name
	}

	// Through the request: two turns of one conversation carry one key however
	// long the history is.
	provider := &chatScript{steps: []chatStep{{resp: llm.CompletionResponse{Content: "ok"}}}}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})
	for _, turn := range []Turn{
		{Instructions: "be terse", Tools: []llm.Tool{weather}, Messages: user("one")},
		{Instructions: "be terse", Tools: []llm.Tool{weather}, Messages: append(user("one"), llm.Message{Role: "assistant", Content: "two"}, llm.Message{Role: "user", Content: "three"})},
	} {
		if _, err := CallTurn(ctx, turn, callOptions()); err != nil {
			t.Fatal(err)
		}
	}
	sent := provider.sent()
	if len(sent) != 2 || sent[0].PromptCacheKey == "" || sent[0].PromptCacheKey != sent[1].PromptCacheKey {
		t.Fatalf("%d requests, cache keys %+v: the turns of one conversation have to share one", len(sent), sent)
	}
	if strings.Contains(sent[1].PromptCacheKey, "three") {
		t.Error("the key holds the history")
	}
}

// A conversation is refused before it is sent when it cannot fit the model's
// context window, if the window is declared, and not otherwise.
func TestATurnThatCannotFitTheModelsWindowIsRefusedBeforeItIsSent(t *testing.T) {
	long := strings.Repeat("word ", 4000) // 20,000 bytes, about 5,000 tokens
	cases := []struct {
		name    string
		window  int
		turn    Turn
		maxOut  int
		refused bool
	}{
		{"a long message in a small window", 4000, Turn{Messages: []llm.Message{{Role: "user", Content: long}}}, 100, true},
		{"the reply's ceiling is what tips it over", 5100, Turn{Messages: []llm.Message{{Role: "user", Content: long}}}, 1000, true},
		{"it fits with room to spare", 20000, Turn{Messages: []llm.Message{{Role: "user", Content: long}}}, 1000, false},
		{"instructions count", 5100, Turn{Instructions: long, Messages: []llm.Message{{Role: "user", Content: "hi"}}}, 1000, true},
		{"a tool's schema counts", 1200, Turn{Tools: []llm.Tool{{Name: "t", Parameters: map[string]any{"description": long}}}, Messages: []llm.Message{{Role: "user", Content: "hi"}}}, 100, true},
		{"a tool call's arguments count", 1200, Turn{Messages: []llm.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c", Name: "t", Arguments: json.RawMessage(`{"q":"` + long + `"}`)}}},
			{Role: "tool", ToolCallID: "c", Content: "ok"},
		}}, 100, true},
		{"a tool's result counts", 1200, Turn{Messages: []llm.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{weatherCall("c")}},
			{Role: "tool", ToolCallID: "c", Content: long},
		}}, 100, true},
		{"exactly the window is allowed", 1000, Turn{Messages: []llm.Message{{Role: "user", Content: strings.Repeat("x", 3600)}}}, 100, false},
		{"one token over is not", 999, Turn{Messages: []llm.Message{{Role: "user", Content: strings.Repeat("x", 3600)}}}, 100, true},
		{"no window declared is no refusal", 0, Turn{Messages: []llm.Message{{Role: "user", Content: strings.Repeat("word ", 400000)}}}, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			if tc.window > 0 {
				llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "local", Model: "chat-model", ContextWindow: tc.window})
			}
			provider := &chatScript{steps: []chatStep{{resp: llm.CompletionResponse{Content: "ok"}}}}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})
			opts := callOptions()
			opts.MaxOutputTokens = tc.maxOut

			_, err := CallTurn(ctx, tc.turn, opts)
			if !tc.refused {
				if err != nil {
					t.Fatalf("the turn was refused: %v", err)
				}
				return
			}
			if kind := types.KindOf(err); kind != types.KindContextTooLarge || !errors.Is(err, types.ErrContextTooLarge) {
				t.Fatalf("err = %v, want the context-too-large kind", err)
			}
			if provider.count() != 0 {
				t.Errorf("%d requests were sent for a conversation that cannot fit", provider.count())
			}
			if strings.Contains(err.Error(), "word") || strings.Contains(err.Error(), "get_weather") {
				t.Errorf("the error carries the conversation: %q", err.Error())
			}
		})
	}
}

// A route that has declared it takes no tools, or no conversation, is refused
// before anything is sent. A route that has declared nothing is asked.
func TestATurnForARouteDeclaredToTakeNoToolsIsRefusedBeforeItIsSent(t *testing.T) {
	two := []llm.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}
	tool := []llm.Tool{{Name: "get_weather"}}
	cases := []struct {
		name     string
		declared map[llm.Capability]bool
		turn     Turn
		refused  bool
	}{
		{"tools declared false", map[llm.Capability]bool{llm.CapToolCalling: false}, Turn{Tools: tool, Messages: two[:1]}, true},
		{"tools declared true", map[llm.Capability]bool{llm.CapToolCalling: true}, Turn{Tools: tool, Messages: two[:1]}, false},
		{"tools never declared", map[llm.Capability]bool{llm.CapStreaming: true}, Turn{Tools: tool, Messages: two[:1]}, false},
		{"a conversation declared false", map[llm.Capability]bool{llm.CapMultiTurn: false}, Turn{Messages: two}, true},
		{"a conversation declared true", map[llm.Capability]bool{llm.CapMultiTurn: true}, Turn{Messages: two}, false},
		{"one message needs no conversation", map[llm.Capability]bool{llm.CapMultiTurn: false}, Turn{Messages: two[:1]}, false},
		{"no tools asked of a route that takes none", map[llm.Capability]bool{llm.CapToolCalling: false}, Turn{Messages: two[:1]}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "local", Model: "chat-model", Supports: tc.declared})
			provider := &chatScript{steps: []chatStep{{resp: llm.CompletionResponse{Content: "ok"}}}}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})

			_, err := CallTurn(ctx, tc.turn, callOptions())
			if !tc.refused {
				if err != nil {
					t.Fatalf("the turn was refused: %v", err)
				}
				return
			}
			if !errors.Is(err, types.ErrUnsupportedCapability) {
				t.Fatalf("err = %v, want the unsupported kind", err)
			}
			if provider.count() != 0 {
				t.Errorf("%d requests were sent to a route that takes no tools", provider.count())
			}
		})
	}
}

func TestATurnRefusesWhatCannotBeSent(t *testing.T) {
	provider := &chatScript{steps: []chatStep{{resp: llm.CompletionResponse{Content: "ok"}}}}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		turn Turn
		opts types.OpOptions
		kind types.ErrorKind
	}{
		{"a nil context", nil, oneTurn("hi"), callOptions(), types.KindInvalidRequest},
		{"no messages", ctx, Turn{}, callOptions(), types.KindInvalidRequest},
		{"a context that is over", cancelled, oneTurn("hi"), callOptions(), types.KindCanceled},
		{"a threshold that is not a number", ctx, oneTurn("hi"), types.OpOptions{Model: "m", Threshold: math.NaN()}, types.KindConfiguration},
		{"no provider at all", context.Background(), oneTurn("hi"), callOptions(), types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapNoProvider(t)
			before := provider.count()
			_, err := CallTurn(tc.ctx, tc.turn, tc.opts)
			if got := types.KindOf(err); got != tc.kind {
				t.Fatalf("kind = %v (%v), want %v", got, err, tc.kind)
			}
			if provider.count() != before {
				t.Error("a request was sent")
			}
		})
	}
}

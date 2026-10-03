package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ModelConformanceChecks implements CP-003's checks that can only be
// answered by a real vendor route: does it actually honour a strict JSON
// Schema, does its usage block report real numbers, does it round-trip
// Unicode and a large payload without mangling either. Every check here
// requires a non-nil *LiveTarget -- RunConformanceSuite records one as
// Skipped rather than calling it when live is nil, so this file compiles
// and its checks are inert in a normal `go test ./...`. Only
// conformance_live_test.go, gated behind SCHEMAFLUX_LIVE_TESTS=1, ever
// supplies a live target that makes these actually call out.
func ModelConformanceChecks() []ConformanceCheck {
	return []ConformanceCheck{
		{ID: "schema_keyword_support", Category: CategoryModel, Capability: CapNativeJSONSchema,
			Description: "the route accepts a strict JSON Schema and returns a value the schema actually shaped",
			Run:         checkSchemaKeywordSupport},
		{ID: "usage_reporting", Category: CategoryModel, Capability: CapExactUsageReporting,
			Description: "the route's usage block reports non-zero prompt and completion token counts",
			Run:         checkUsageReporting},
		{ID: "unicode_roundtrip", Category: CategoryModel,
			Description: "a prompt containing multi-byte scripts and emoji is repeated back intact, as valid UTF-8",
			Run:         checkUnicodeRoundtrip},
		{ID: "large_payload", Category: CategoryModel,
			Description: "a large prompt is accepted and answered rather than rejected or silently truncated",
			Run:         checkLargePayload},
		{ID: "reasoning_cached_token_normalization", Category: CategoryModel,
			Description: "reasoning and cached-token fields, when the route has nothing to report, come back zero rather than erroring",
			Run:         checkReasoningCachedTokenNormalization},
	}
}

// modelCheckPreconditions returns a failedOutcome if live is missing what
// every model check needs, or nil if the caller may proceed. Centralizing
// this means a check body never has to guard against a nil Provider itself.
func modelCheckPreconditions(live *LiveTarget) *ConformanceOutcome {
	if live == nil || live.Provider == nil {
		outcome := failedOutcome("no live target supplied")
		return &outcome
	}
	return nil
}

func checkSchemaKeywordSupport(ctx context.Context, live *LiveTarget) ConformanceOutcome {
	if bad := modelCheckPreconditions(live); bad != nil {
		return *bad
	}

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"greeting"},
		"properties": map[string]any{
			"greeting": map[string]any{"type": "string", "enum": []string{"hello"}},
		},
	}

	resp, err := live.Provider.Complete(ctx, CompletionRequest{
		Model:          live.Model,
		SystemPrompt:   "Answer using the required JSON shape only.",
		UserPrompt:     "Reply with the greeting field set to \"hello\".",
		ResponseFormat: "json",
		JSONSchema:     schema,
		SchemaName:     "conformance_greeting",
	})
	if err != nil {
		// Only the route saying the request was wrong is evidence that it does not
		// take the schema. A throttle, an outage, a dropped connection, an expired
		// key or a cancelled context is a request that was not answered, and what
		// the route does with a schema is not known from it.
		if Classify(err) == types.KindInvalidRequest {
			return ConformanceOutcome{Passed: true, Supported: false,
				Detail: fmt.Sprintf("route rejected the schema-constrained request: %v", classifyDetail(err))}
		}
		return failedOutcome(fmt.Sprintf("could not be determined: the request failed (%v)", classifyDetail(err)))
	}
	if strings.TrimSpace(resp.Content) == "" {
		return ConformanceOutcome{Passed: true, Supported: false, Detail: "schema-constrained request returned empty content"}
	}

	// Supported is the route having been held to the schema: the answer decodes to
	// the one shape the schema allows, and carries the one value its enum allows.
	// Any non-empty reply used to count, so a route that ignored the schema and
	// answered in prose was published as one that enforces it.
	if !conformsToGreetingSchema(resp.Content) {
		return ConformanceOutcome{Passed: true, Supported: false,
			Detail: "schema-constrained request returned content that does not conform to the schema"}
	}
	return ConformanceOutcome{Passed: true, Supported: true, Detail: "schema-constrained request returned content that conforms to the schema"}
}

// conformsToGreetingSchema reports whether content is a JSON object with exactly
// the one property the conformance schema declares, holding the one value its
// enum allows.
func conformsToGreetingSchema(content string) bool {
	var answer map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &answer); err != nil {
		return false
	}
	greeting, isString := answer["greeting"].(string)
	return len(answer) == 1 && isString && greeting == "hello"
}

func checkUsageReporting(ctx context.Context, live *LiveTarget) ConformanceOutcome {
	if bad := modelCheckPreconditions(live); bad != nil {
		return *bad
	}

	resp, err := live.Provider.Complete(ctx, CompletionRequest{
		Model:      live.Model,
		UserPrompt: "Reply with the single word: ready",
	})
	if err != nil {
		return failedOutcome(fmt.Sprintf("request failed: %v", classifyDetail(err)))
	}

	supported := resp.Usage.PromptTokens > 0 && resp.Usage.CompletionTokens > 0
	detail := fmt.Sprintf("prompt_tokens=%d completion_tokens=%d", resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	if !supported {
		detail += " (zero usage reported -- this route's usage-reporting quality is not exact)"
	}
	return ConformanceOutcome{Passed: true, Supported: supported, Detail: detail}
}

func checkUnicodeRoundtrip(ctx context.Context, live *LiveTarget) ConformanceOutcome {
	if bad := modelCheckPreconditions(live); bad != nil {
		return *bad
	}

	// A hand-picked, hand-checked mix: CJK, Arabic (RTL), and an emoji with
	// a combining modifier -- the three shapes most likely to break a
	// byte-oriented decoder (multi-byte runes, right-to-left script,
	// combining sequences).
	const probe = "你好 مرحبا 👍🏽"

	resp, err := live.Provider.Complete(ctx, CompletionRequest{
		Model:      live.Model,
		UserPrompt: fmt.Sprintf("Repeat exactly this text and nothing else: %s", probe),
	})
	if err != nil {
		return failedOutcome(fmt.Sprintf("request failed: %v", classifyDetail(err)))
	}
	if !utf8.ValidString(resp.Content) {
		return failedOutcome("response content is not valid UTF-8")
	}
	if strings.TrimSpace(resp.Content) == "" {
		return failedOutcome("response content is empty")
	}
	// Content decoded from JSON is always valid UTF-8, so validity alone says
	// nothing: any non-empty answer passed it. What is checked is that the text
	// that was asked to be repeated came back whole.
	if !strings.Contains(resp.Content, probe) {
		return failedOutcome("the multi-script text did not come back intact")
	}
	return passedOutcome(fmt.Sprintf("the multi-script text came back intact in %d valid UTF-8 bytes", len(resp.Content)))
}

func checkLargePayload(ctx context.Context, live *LiveTarget) ConformanceOutcome {
	if bad := modelCheckPreconditions(live); bad != nil {
		return *bad
	}

	// ~50KB of filler plus a marker sentence, so a truncating route is
	// caught by the marker going missing rather than by guessing at a
	// length limit.
	const marker = "CONFORMANCE-MARKER-7f3a"
	filler := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 1100)
	prompt := filler + " The secret word is " + marker + ". What is the secret word?"

	resp, err := live.Provider.Complete(ctx, CompletionRequest{
		Model:      live.Model,
		UserPrompt: prompt,
	})
	if err != nil {
		return failedOutcome(fmt.Sprintf("a ~%dKB prompt failed: %v", len(prompt)/1024, classifyDetail(err)))
	}
	if !strings.Contains(resp.Content, marker) {
		return failedOutcome(fmt.Sprintf("a ~%dKB prompt's marker did not survive to the answer", len(prompt)/1024))
	}
	return passedOutcome(fmt.Sprintf("a ~%dKB prompt was accepted and its marker was recovered", len(prompt)/1024))
}

func checkReasoningCachedTokenNormalization(ctx context.Context, live *LiveTarget) ConformanceOutcome {
	if bad := modelCheckPreconditions(live); bad != nil {
		return *bad
	}

	resp, err := live.Provider.Complete(ctx, CompletionRequest{
		Model:      live.Model,
		UserPrompt: "Reply with the single word: ready",
	})
	if err != nil {
		return failedOutcome(fmt.Sprintf("request failed: %v", classifyDetail(err)))
	}
	if resp.Usage.ReasoningTokens < 0 || resp.Usage.CachedTokens < 0 {
		return failedOutcome(fmt.Sprintf("negative usage field: reasoning=%d cached=%d", resp.Usage.ReasoningTokens, resp.Usage.CachedTokens))
	}
	supported := resp.Usage.ReasoningTokens > 0 || resp.Usage.CachedTokens > 0
	return ConformanceOutcome{Passed: true, Supported: supported,
		Detail: fmt.Sprintf("reasoning_tokens=%d cached_tokens=%d (zero is a valid, normalized absence)", resp.Usage.ReasoningTokens, resp.Usage.CachedTokens)}
}

// classifyDetail describes an error by its classification only -- never by
// its message, which could carry a fragment of the synthetic prompt this
// suite itself sent. See AGENTS.md "never log or embed the caller's
// payload": these checks build their own fixtures, but the discipline is
// the same regardless of whose data it is.
func classifyDetail(err error) string {
	return Classify(err).String()
}

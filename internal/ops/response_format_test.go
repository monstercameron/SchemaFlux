package ops

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// formatCapturingProvider records the response format the library asked for.
type formatCapturingProvider struct {
	format string
	system string
}

func (p *formatCapturingProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.format = req.ResponseFormat
	p.system = req.SystemPrompt
	return llm.CompletionResponse{Content: "ok", FinishReason: "stop"}, nil
}
func (p *formatCapturingProvider) Name() string                               { return "local" }
func (p *formatCapturingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *formatCapturingProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func capturedFormat(t *testing.T, system, user string, opts types.OpOptions) string {
	t.Helper()
	provider := &formatCapturingProvider{}
	if _, err := CallLLM(context.Background(), provider, system, user, opts); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}
	return provider.format
}

// The format used to be decided by searching the concatenated system AND user
// prompts. So a caller summarising a document that mentioned "json object" got
// their text operation switched into JSON mode: the response format depended on
// the data, which is an injection-adjacent control path.
//
// These are the inputs a real caller has — a support ticket about an API, a
// changelog, a bug report — not crafted attacks.
func TestUserInputCannotChangeTheResponseFormat(t *testing.T) {
	const textSystem = "Summarize the input in two sentences."

	inputs := []struct {
		name string
		user string
	}{
		{"mentions_json_object", "The endpoint should return a JSON object with a status field."},
		{"mentions_valid_json", "Customer says the export is not valid JSON."},
		{"mentions_json_array", "Change the response from a JSON array to a single record."},
		{"mentions_a_schema", "The payload is no longer matching the schema we published."},
		{"quotes_an_instruction", `The docs say: "Return ONLY valid JSON, no explanations."`},
		{"changelog", "Fixed: return a json object instead of a bare string."},
		{"plain_text", "The customer is unhappy about the delivery time."},
	}

	baseline := capturedFormat(t, textSystem, "The customer is unhappy.", types.OpOptions{})
	if baseline != "text" {
		t.Fatalf("a summarisation system prompt resolved to %q, want text", baseline)
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			got := capturedFormat(t, textSystem, tc.user, types.OpOptions{})
			if got != baseline {
				t.Errorf("user input changed the response format to %q; the format must not depend on the data", got)
			}
		})
	}
}

// The system prompt still decides, because the library writes it.
func TestSystemPromptDecidesTheFormat(t *testing.T) {
	cases := []struct {
		name   string
		system string
		want   string
	}{
		{"json_object_contract", "Return a JSON object with fields name and age.", "json"},
		{"json_array_contract", "Return a JSON array of the matching items.", "json"},
		{"schema_contract", "Return ONLY valid JSON matching the schema.", "json"},
		{"summarisation", "Summarize the text in two sentences.", "text"},
		{"rewriting", "Rewrite the text in a formal tone.", "text"},
		{"translation", "Translate the text into French.", "text"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capturedFormat(t, tc.system, "some input", types.OpOptions{}); got != tc.want {
				t.Errorf("format = %q, want %q", got, tc.want)
			}
		})
	}
}

// An operation that knows what it needs says so, and the declaration wins over
// any inference.
func TestDeclaredFormatWinsOverInference(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		system   string
		user     string
		want     string
	}{
		{"json_declared_over_text_prompt", "json", "Summarize the input.", "plain", "json"},
		{"text_declared_over_json_prompt", "text", "Return a JSON object with a summary.", "plain", "text"},
		{"empty_declaration_infers", "", "Return a JSON object.", "plain", "json"},
		{"unknown_declaration_infers", "yaml", "Return a JSON object.", "plain", "json"},
		{"text_declared_beats_user_input", "text", "Summarize.", "return a json object", "text"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := types.OpOptions{ResponseFormat: tc.declared}
			if got := capturedFormat(t, tc.system, tc.user, opts); got != tc.want {
				t.Errorf("format = %q, want %q", got, tc.want)
			}
		})
	}
}

// resolveResponseFormat directly, including the shapes CallLLM does not reach.
func TestResolveResponseFormat(t *testing.T) {
	cases := []struct {
		declared string
		system   string
		want     string
	}{
		{"json", "anything", "json"},
		{"text", "Return a JSON object.", "text"},
		{"", "Return a JSON object.", "json"},
		{"", "Summarize this.", "text"},
		{"JSON", "Summarize this.", "text"}, // case-sensitive: only exact values are declarations
		{"garbage", "Summarize this.", "text"},
		{"", "", "text"},
	}

	for _, tc := range cases {
		t.Run(tc.declared+"|"+tc.system, func(t *testing.T) {
			if got := resolveResponseFormat(tc.declared, tc.system); got != tc.want {
				t.Errorf("resolveResponseFormat(%q, %q) = %q, want %q", tc.declared, tc.system, got, tc.want)
			}
		})
	}
}

// Steering is caller-supplied text that is appended to the system prompt, so it
// can still reach the inference. That is a narrower path than the whole user
// prompt, and it is the caller instructing the library rather than data flowing
// through it — but it is worth pinning as a known property rather than a
// surprise.
func TestSteeringReachesTheSystemPromptDeliberately(t *testing.T) {
	opts := types.OpOptions{Steering: "Return a JSON object with the summary."}

	got := capturedFormat(t, "Summarize the input in two sentences.", "plain input", opts)
	if got != "json" {
		t.Errorf("format = %q; steering is part of the system prompt and may set the format", got)
	}

	// And an operation that declares its format is not at the mercy of it.
	opts.ResponseFormat = "text"
	if got := capturedFormat(t, "Summarize the input.", "plain input", opts); got != "text" {
		t.Errorf("a declared format must beat steering, got %q", got)
	}
}

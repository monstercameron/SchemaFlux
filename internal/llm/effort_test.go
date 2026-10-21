package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LX-105. resolveEffort decides what reasoning effort a request carries. A
// caller's own value is a requirement: it is sent, or the request is refused
// here with the values the model takes. An operation's preference is a
// suggestion: it is sent when the model is known to take it and dropped when
// not. With neither, the request is what it was before the option existed.
func TestResolveEffort(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		requested string
		preferred bool
		want      string
		refused   bool
	}{
		{"unset on gpt-6 sends nothing", "gpt-6-luna", "", false, "", false},
		{"unset on gpt-5 sends the family default", "gpt-5-mini", "", false, "minimal", false},
		{"unset on gpt-5.4 sends the family default", "gpt-5.4-mini", "", false, "none", false},
		{"unset on gpt-5.6 sends nothing", "gpt-5.6-luna", "", false, "", false},
		{"unset on gpt-4 sends nothing", "gpt-4o", "", false, "", false},
		{"unset on an unknown model sends nothing", "some-new-model", "", false, "", false},

		{"a caller's none on gpt-6", "gpt-6-luna", "none", false, "none", false},
		{"a caller's low on gpt-6", "gpt-6-luna", "low", false, "low", false},
		{"a caller's max on gpt-6.1", "gpt-6.1-sol", "max", false, "max", false},
		{"a caller's value gpt-6 does not take", "gpt-6-luna", "minimal", false, "", true},
		{"a caller's misspelling", "gpt-6-luna", "hi", false, "", true},
		{"a caller's value on a model that does not reason", "gpt-4o", "low", false, "", true},
		{"a caller's value on gpt-3.5", "gpt-3.5-turbo", "none", false, "", true},
		{"a caller's value where nothing is known is sent", "gpt-5-mini", "high", false, "high", false},
		{"a caller's value on an unknown model is sent", "some-new-model", "low", false, "low", false},
		{"a caller's value through a gateway name", "openai/gpt-6-luna", "low", false, "low", false},

		// A preference is taken only where it was measured to be safe: none,
		// on gpt-6-luna. A family that accepts the word is not enough. The 5.6
		// family accepts none, and this table records it taking two of its
		// models from four right in four to none right; the other gpt-6 models
		// have not been measured at all.
		{"a preference on the model it was measured on", "gpt-6-luna", "none", true, "none", false},
		{"a preference on a dated snapshot of it", "gpt-6-luna-2026-09-15", "none", true, "none", false},
		{"a preference through a gateway name", "openai/gpt-6-luna", "none", true, "none", false},
		{"a preference that was not measured there", "gpt-6-luna", "low", true, "", false},
		{"a preference gpt-6 does not take is dropped", "gpt-6-luna", "minimal", true, "", false},
		{"a preference on an unmeasured gpt-6 model is dropped", "gpt-6-sol", "none", true, "", false},
		{"a preference on gpt-6.1 is dropped", "gpt-6.1-sol", "none", true, "", false},
		{"a preference on gpt-5.6 is dropped", "gpt-5.6-luna", "none", true, "", false},
		{"a preference on a model that does not reason is dropped", "gpt-4o", "none", true, "", false},
		{"a preference where nothing is known keeps the family default", "gpt-5-mini", "none", true, "minimal", false},
		{"a preference on an unknown model is dropped", "some-new-model", "none", true, "", false},
		// The model it was measured on is that name and its dated snapshots,
		// and nothing that only begins with it. The row was matched by prefix,
		// so a smaller sibling and a fine-tune inherited a measurement that
		// was never made on them.
		{"a preference in other capitals", "GPT-6-Luna", "none", true, "none", false},
		{"a preference with space around the name", "  gpt-6-luna ", "none", true, "none", false},
		{"a preference on a smaller sibling is dropped", "gpt-6-luna-mini", "none", true, "", false},
		{"a preference on a longer name is dropped", "gpt-6-lunar-x", "none", true, "", false},
		{"a preference on a fine-tune is dropped", "ft:gpt-6-luna:org::id", "none", true, "", false},
		{"a preference on a fine-tune of a snapshot is dropped", "ft:gpt-6-luna-2026-09-15:org::id", "none", true, "", false},
		{"a preference on an alias is dropped", "gpt-6-luna-latest", "none", true, "", false},
		{"a preference on a suffix that is not a date is dropped", "gpt-6-luna-2026-9-15", "none", true, "", false},
		{"a preference on a date with letters is dropped", "gpt-6-luna-2026-0a-15", "none", true, "", false},
		{"a preference on a snapshot of a sibling is dropped", "gpt-6-luna-mini-2026-09-15", "none", true, "", false},
		{"a preference on a gateway's snapshot name", "openai/gpt-6-luna-2026-09-15", "none", true, "none", false},
		// A sibling is still a gpt-6 model for what it accepts from a caller.
		{"a caller's none on a smaller sibling", "gpt-6-luna-mini", "none", false, "none", false},
		{"a caller's value a sibling does not take", "gpt-6-luna-mini", "minimal", false, "", true},
		// The caller's own value is not held to that: it is theirs to choose.
		{"a caller's none on gpt-5.6", "gpt-5.6-luna", "none", false, "none", false},
		{"a caller's none on an unmeasured gpt-6 model", "gpt-6-sol", "none", false, "none", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, problem := resolveEffort(tc.model, true, tc.requested, tc.preferred)
			if (problem != "") != tc.refused {
				t.Fatalf("problem = %q, want a refusal: %v", problem, tc.refused)
			}
			if got != tc.want {
				t.Fatalf("effort = %q, want %q", got, tc.want)
			}
			if tc.refused && !strings.Contains(problem, tc.requested) {
				t.Errorf("the refusal does not name the value: %q", problem)
			}
		})
	}
}

func effortOnTheWire(body map[string]any) (string, bool) {
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		return "", false
	}
	effort, _ := reasoning["effort"].(string)
	return effort, true
}

// The effort reaches the request body, and a value the model does not take is
// refused before anything is sent: these parameters are not negotiated, so the
// API would have failed the whole request.
func TestReasoningEffortOnTheWire(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		requested string
		preferred bool
		want      string
		sent      bool
		wantKind  types.ErrorKind
	}{
		{"unset on gpt-6: the body is what it was", "gpt-6-luna", "", false, "", false, types.KindUnknown},
		{"none on gpt-6", "gpt-6-luna", "none", false, "none", true, types.KindUnknown},
		{"low on gpt-6", "gpt-6-luna", "low", false, "low", true, types.KindUnknown},
		{"high on gpt-6.1", "gpt-6.1-sol", "high", false, "high", true, types.KindUnknown},
		{"a preference on gpt-6", "gpt-6-luna", "none", true, "none", true, types.KindUnknown},
		{"a preference gpt-4o cannot take", "gpt-4o", "none", true, "", false, types.KindUnknown},
		{"a preference on an unknown model", "a-new-model", "none", true, "", false, types.KindUnknown},
		{"unset on gpt-5 keeps its default", "gpt-5-mini", "", false, "minimal", true, types.KindUnknown},
		{"a value gpt-6 does not take", "gpt-6-luna", "minimal", false, "", false, types.KindInvalidRequest},
		{"a value on a model that does not reason", "gpt-4o", "low", false, "", false, types.KindInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, transport := openAIProviderOn(t, "")
			_, err := provider.Complete(context.Background(), CompletionRequest{
				Model: tc.model, SystemPrompt: "s", UserPrompt: "u",
				ReasoningEffort: tc.requested, ReasoningEffortPreferred: tc.preferred,
			})
			if types.KindOf(err) != tc.wantKind {
				t.Fatalf("err = %v (kind %v), want kind %v", err, types.KindOf(err), tc.wantKind)
			}
			if tc.wantKind != types.KindUnknown {
				if len(transport.bodies) != 0 {
					t.Fatalf("a refused request was sent")
				}
				return
			}
			if len(transport.bodies) != 1 {
				t.Fatalf("%d requests were sent, want 1", len(transport.bodies))
			}
			got, sent := effortOnTheWire(transport.bodies[0])
			if sent != tc.sent || got != tc.want {
				t.Fatalf("reasoning on the wire = %q (present %v), want %q (present %v)", got, sent, tc.want, tc.sent)
			}
		})
	}

	// The streaming request is built by the same code.
	provider, transport := openAIProviderOn(t, "")
	transport.stream = true
	for _, err := range provider.CompleteStream(context.Background(), CompletionRequest{
		Model: "gpt-6-luna", SystemPrompt: "s", UserPrompt: "u", ReasoningEffort: "low",
	}) {
		if err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
	}
	if got, _ := effortOnTheWire(transport.bodies[0]); got != "low" {
		t.Errorf("the streaming request carries effort %q, want low", got)
	}
}

// A provider path that cannot set an effort refuses a caller who asked for one
// and ignores an operation's preference. It never drops a caller's request in
// silence.
func TestProvidersThatCannotSetAnEffort(t *testing.T) {
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gemma-4-31b","choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer chat.Close()
	cerebras, err := NewCerebrasProvider(ProviderConfig{APIKey: "k", BaseURL: chat.URL})
	if err != nil {
		t.Fatalf("NewCerebrasProvider: %v", err)
	}

	server, _ := captureAnthropicRequest(t)
	anthropic := newTestAnthropicProvider(t, server.URL)

	providers := []struct {
		name     string
		complete func(CompletionRequest) error
	}{
		{"chat completions", func(req CompletionRequest) error {
			_, err := cerebras.Complete(context.Background(), req)
			return err
		}},
		{"anthropic", func(req CompletionRequest) error {
			_, err := anthropic.Complete(context.Background(), req)
			return err
		}},
	}
	for _, p := range providers {
		t.Run(p.name+"/a caller's effort is refused", func(t *testing.T) {
			err := p.complete(CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u", ReasoningEffort: "low"})
			if types.KindOf(err) != types.KindUnsupportedCapability {
				t.Fatalf("err = %v (kind %v), want an unsupported capability", err, types.KindOf(err))
			}
		})
		t.Run(p.name+"/a preference is ignored", func(t *testing.T) {
			err := p.complete(CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u", ReasoningEffort: "none", ReasoningEffortPreferred: true})
			if err != nil {
				t.Fatalf("a preference failed the request: %v", err)
			}
		})
		t.Run(p.name+"/unset is as before", func(t *testing.T) {
			if err := p.complete(CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u"}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
		})
	}
}

// The endpoint decides only what temperature an unknown model is sent. An
// effort is the same on a gateway as on OpenAI's own host.
func TestResolveEffortDoesNotDependOnTheEndpoint(t *testing.T) {
	for _, tc := range []struct {
		model, requested string
		preferred        bool
	}{
		{"gpt-6-luna", "none", true}, {"gpt-6-luna", "low", false}, {"gpt-6-luna", "minimal", false},
		{"gpt-4o", "low", false}, {"gpt-4o", "none", true}, {"gemma-4-31b", "low", false},
		{"gemma-4-31b", "none", true}, {"gpt-5-mini", "", false}, {"gpt-5.6-luna", "none", true}, {"a-new-model", "", false},
	} {
		onOpenAI, problemOnOpenAI := resolveEffort(tc.model, true, tc.requested, tc.preferred)
		elsewhere, problemElsewhere := resolveEffort(tc.model, false, tc.requested, tc.preferred)
		if onOpenAI != elsewhere || (problemOnOpenAI == "") != (problemElsewhere == "") {
			t.Errorf("%s %q preferred=%v: %q on OpenAI's endpoint and %q elsewhere", tc.model, tc.requested, tc.preferred, onOpenAI, elsewhere)
		}
	}
}

func TestEffortNotSupported(t *testing.T) {
	cases := []struct {
		name      string
		effort    string
		preferred bool
		refused   bool
	}{
		{"nothing asked for", "", false, false},
		{"nothing asked for, marked preferred", "", true, false},
		{"a caller's none", "none", false, true},
		{"a caller's low", "low", false, true},
		{"a caller's high", "high", false, true},
		{"a caller's unknown word", "plenty", false, true},
		{"a preference for none", "none", true, false},
		{"a preference for low", "low", true, false},
		{"a preference for an unknown word", "plenty", true, false},
		{"a caller's value with spaces", " low ", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := effortNotSupported(CompletionRequest{Model: "m", ReasoningEffort: tc.effort, ReasoningEffortPreferred: tc.preferred}, "a-provider")
			if (err != nil) != tc.refused {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if err != nil && types.KindOf(err) != types.KindUnsupportedCapability {
				t.Errorf("kind = %v, want an unsupported capability", types.KindOf(err))
			}
		})
	}
}

func TestWithoutSnapshotDate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gpt-6-luna", "gpt-6-luna"},
		{"gpt-6-luna-2026-09-15", "gpt-6-luna"},
		{"gpt-6-luna-2026-9-15", "gpt-6-luna-2026-9-15"},
		{"gpt-6-luna-20260915", "gpt-6-luna-20260915"},
		{"gpt-6-luna-2026-09-1x", "gpt-6-luna-2026-09-1x"},
		{"gpt-6-luna_2026-09-15", "gpt-6-luna_2026-09-15"},
		{"gpt-6-luna-2026/09/15", "gpt-6-luna-2026/09/15"},
		{"-2026-09-15", "-2026-09-15"},
		{"2026-09-15", "2026-09-15"},
		{"", ""},
		{"x-2026-09-15", "x"},
		{"a-2026-09-15-2026-09-15", "a-2026-09-15"},
		{"gpt-6-luna-0000-00-00", "gpt-6-luna"},
		{"gpt-6-luna-2026-09-15 ", "gpt-6-luna-2026-09-15 "},
	}
	for _, tc := range cases {
		if got := withoutSnapshotDate(tc.in); got != tc.want {
			t.Errorf("withoutSnapshotDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPreferenceMeasured(t *testing.T) {
	cases := []struct {
		model, effort string
		want          bool
	}{
		{"gpt-6-luna", "none", true},
		{"GPT-6-LUNA", "none", true},
		{" gpt-6-luna ", "none", true},
		{"gpt-6-luna-2026-09-15", "none", true},
		{"openai/gpt-6-luna", "none", true},
		{"a/b/gpt-6-luna", "none", true},
		{"gpt-6-luna", "low", false},
		{"gpt-6-luna", "", false},
		{"gpt-6-luna", "NONE", false},
		{"gpt-6-luna-mini", "none", false},
		{"gpt-6-sol", "none", false},
		{"gpt-6", "none", false},
		{"ft:gpt-6-luna:org::id", "none", false},
		{"FT:gpt-6-luna", "none", false},
		{"", "none", false},
		{"gpt-5.6-luna", "none", false},
	}
	for _, tc := range cases {
		if got := preferenceMeasured(tc.model, tc.effort); got != tc.want {
			t.Errorf("preferenceMeasured(%q, %q) = %v, want %v", tc.model, tc.effort, got, tc.want)
		}
	}
}

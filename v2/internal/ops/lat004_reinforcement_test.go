package ops

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// dec12Recorder is a provider that answers every request with response, at once,
// and keeps the requests it was sent. Unlike the test caller hook it is reached
// through CallLLM, so what it records is what the library would send.
type dec12Recorder struct {
	mu       sync.Mutex
	response string
	// replies, when set, answer the calls in turn, the last one repeating.
	replies  []string
	err      error
	requests []llm.CompletionRequest
}

func (r *dec12Recorder) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	call := len(r.requests) - 1
	r.mu.Unlock()
	if r.err != nil {
		return llm.CompletionResponse{}, r.err
	}
	response := r.response
	if len(r.replies) > 0 {
		response = r.replies[min(call, len(r.replies)-1)]
	}
	return llm.CompletionResponse{Content: response, Provider: "local", Model: req.Model, FinishReason: "stop"}, nil
}

func (r *dec12Recorder) Name() string                               { return "local" }
func (r *dec12Recorder) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (r *dec12Recorder) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (r *dec12Recorder) taken() []llm.CompletionRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.CompletionRequest(nil), r.requests...)
}

// dec12WithRecorder returns a context that reaches a recorder answering with
// response.
func dec12WithRecorder(response string) (context.Context, *dec12Recorder) {
	recorder := &dec12Recorder{response: response}
	return WithProvider(context.Background(), recorder), recorder
}

const dec12Preamble = "Perform the semantic task faithfully"

func dec12JSONOptions() types.OpOptions {
	return applyDefaults(types.OpOptions{ResponseFormat: "json"})
}
func dec12TextOptions() types.OpOptions {
	return applyDefaults(types.OpOptions{ResponseFormat: "text"})
}

// A decision's request carries its own instructions and no reinforcement block,
// whichever form of it is called.
func TestADecisionIsSentWithoutTheReinforcementBlock(t *testing.T) {
	type triage struct {
		Spam bool   `ask:"Is this spam?"`
		Team string `ask:"Which team?" oneof:"billing,technical"`
	}
	cases := []struct {
		name     string
		response string
		run      func(ctx context.Context) error
	}{
		{"Is", `{"q1":true}`, func(ctx context.Context) error { _, err := Is(ctx, "text", "Is it spam?"); return err }},
		{"IsResult", `{"q1":true,"confidence":0.5}`, func(ctx context.Context) error { _, err := IsResult(ctx, "text", "Is it spam?"); return err }},
		{"Which", `{"q1":1}`, func(ctx context.Context) error {
			_, err := Which(ctx, "text", "Which?", []string{"a", "b"})
			return err
		}},
		{"WhichResult", `{"q1":1,"confidence":0.5}`, func(ctx context.Context) error {
			_, err := WhichResult(ctx, "text", "Which?", []string{"a", "b"})
			return err
		}},
		{"WhichOrNone", `{"q1":3}`, func(ctx context.Context) error {
			_, _, err := WhichOrNone(ctx, "text", "Which?", []string{"a", "b"})
			return err
		}},
		{"WhichOrNoneResult", `{"q1":1,"confidence":0.5}`, func(ctx context.Context) error {
			_, err := WhichOrNoneResult(ctx, "text", "Which?", []string{"a", "b"})
			return err
		}},
		{"Rate", `{"q1":3}`, func(ctx context.Context) error { _, err := Rate(ctx, "text", "How?", 1, 5); return err }},
		{"RateResult", `{"q1":3,"confidence":0.5}`, func(ctx context.Context) error {
			_, err := RateResult(ctx, "text", "How?", 1, 5)
			return err
		}},
		{"Assess", `{"q1":true,"q2":1}`, func(ctx context.Context) error { _, err := Assess[triage](ctx, "text"); return err }},
		{"AssessResult", `{"q1":true,"q2":1,"confidence":0.5}`, func(ctx context.Context) error {
			_, err := AssessResult[triage](ctx, "text")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder := dec12WithRecorder(tc.response)
			if err := tc.run(ctx); err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			requests := recorder.taken()
			if len(requests) != 1 {
				t.Fatalf("%d requests were sent", len(requests))
			}
			if got := requests[0].SystemPrompt; got != strings.TrimSpace(assessSystemPrompt) {
				t.Errorf("the instructions are not the decision's own:\n%s", got)
			}
			if strings.Contains(requests[0].SystemPrompt, dec12Preamble) {
				t.Error("the reinforcement block was sent with a decision")
			}
		})
	}
}

// A decision leaves the block off by its own choice, so the context the caller
// handed it is not marked, and an operation called with that context afterwards
// still gets it.
func TestADecisionDoesNotMarkTheCallersContext(t *testing.T) {
	ctx, recorder := dec12WithRecorder(`{"q1":true}`)
	if _, err := Is(ctx, "text", "Is it spam?"); err != nil {
		t.Fatal(err)
	}
	if dec12ReinforcementDeclined(ctx) {
		t.Fatal("the caller's context was marked")
	}
	if _, err := callLLM(ctx, "Return only valid JSON.", "user", dec12JSONOptions()); err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	requests := recorder.taken()
	if len(requests) != 2 || !strings.Contains(requests[1].SystemPrompt, dec12Preamble) {
		t.Errorf("the operation after the decision was sent without the block: %d requests", len(requests))
	}
}

// Every other operation is sent the block, whatever the format of its request.
func TestOtherOperationsAreStillSentTheReinforcementBlock(t *testing.T) {
	t.Run("a request for JSON", func(t *testing.T) {
		ctx, recorder := dec12WithRecorder(`{}`)
		if _, err := callLLM(ctx, "Return only valid JSON.", "user", dec12JSONOptions()); err != nil {
			t.Fatal(err)
		}
		got := recorder.taken()[0].SystemPrompt
		if !strings.Contains(got, dec12Preamble) || !strings.Contains(got, "return only the final JSON answer") || !strings.HasSuffix(got, "Return only valid JSON.") {
			t.Errorf("the block is missing or in the wrong place:\n%s", got)
		}
	})
	t.Run("a request for text", func(t *testing.T) {
		ctx, recorder := dec12WithRecorder(`text`)
		if _, err := callLLM(ctx, "Write a sentence.", "user", dec12TextOptions()); err != nil {
			t.Fatal(err)
		}
		got := recorder.taken()[0].SystemPrompt
		if !strings.Contains(got, dec12Preamble) || strings.Contains(got, "return only the final JSON answer") || !strings.HasSuffix(got, "Write a sentence.") {
			t.Errorf("the text request's block is wrong:\n%s", got)
		}
	})
	t.Run("Summarize, a real operation", func(t *testing.T) {
		recorder := &dec12Recorder{response: "A summary."}
		previousCaller, previousProvider := currentHooks()
		setLLMCaller(nil)
		SetDefaultProvider(recorder)
		t.Cleanup(func() {
			setLLMCaller(previousCaller)
			SetDefaultProvider(previousProvider)
		})
		_, _ = Summarize("The invoice is due on the 30th and the customer disputes it.", NewSummarizeOptions())
		requests := recorder.taken()
		if len(requests) == 0 || !strings.Contains(requests[0].SystemPrompt, dec12Preamble) {
			t.Errorf("Summarize was sent without the block: %d requests", len(requests))
		}
	})
}

// The environment switch turns the block off for the operations that have it
// and changes nothing for a decision, which has none to turn off.
func TestTheEnvironmentSwitchStillTurnsTheBlockOffForOtherOperations(t *testing.T) {
	for _, value := range []string{"0", "off", "false", "no"} {
		t.Run(value, func(t *testing.T) {
			configtest.Setenv(t, promptReinforcementEnvVar, value)
			ctx, recorder := dec12WithRecorder(`{"q1":true}`)
			if _, err := callLLM(ctx, "Return only valid JSON.", "user", dec12JSONOptions()); err != nil {
				t.Fatal(err)
			}
			if got := recorder.taken()[0].SystemPrompt; got != "Return only valid JSON." {
				t.Errorf("the switch did not turn the block off:\n%s", got)
			}
			if _, err := Is(ctx, "text", "Is it spam?"); err != nil {
				t.Fatal(err)
			}
			if got := recorder.taken()[1].SystemPrompt; got != strings.TrimSpace(assessSystemPrompt) {
				t.Errorf("a decision with the switch off is not its own instructions:\n%s", got)
			}
		})
	}
	t.Run("a value that means nothing leaves the block on", func(t *testing.T) {
		configtest.Setenv(t, promptReinforcementEnvVar, "maybe")
		ctx, recorder := dec12WithRecorder(`{}`)
		if _, err := callLLM(ctx, "Return only valid JSON.", "user", dec12JSONOptions()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(recorder.taken()[0].SystemPrompt, dec12Preamble) {
			t.Error("the block was turned off by a value that means nothing")
		}
	})
}

// The system prompt is the operation's own when it declined the block, and the
// block's otherwise, for each way of switching and each format.
func TestDec12StrengthenSystemPromptDeclinesOnlyWhenTheContextSaysSo(t *testing.T) {
	declined := dec12WithoutReinforcement(context.Background())
	cases := []struct {
		name    string
		ctx     context.Context
		env     string
		format  string
		wantOwn bool
	}{
		{"plain, json", context.Background(), "", "json", false},
		{"plain, text", context.Background(), "", "text", false},
		{"declined, json", declined, "", "json", true},
		{"declined, text", declined, "", "text", true},
		{"declined, switch off", declined, "0", "json", true},
		{"plain, switch off", context.Background(), "0", "json", true},
		{"plain, switch on", context.Background(), "1", "json", false},
		{"nil context", nil, "", "json", false},
		{"declined under another value", context.WithValue(declined, struct{}{}, 1), "", "json", true},
		{"a context derived from a declined one", func() context.Context { c, cancel := context.WithCancel(declined); _ = cancel; return c }(), "", "text", true},
		{"the key holds false", context.WithValue(context.Background(), dec12WithoutReinforcementKey{}, false), "", "json", false},
	}
	const own = "Own instructions."
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, promptReinforcementEnvVar, tc.env)
			got := dec12StrengthenSystemPrompt(tc.ctx, "  "+own+"\n", tc.format)
			if tc.wantOwn != (got == own) {
				t.Errorf("got %q, want the operation's own prompt: %v", got, tc.wantOwn)
			}
			if !tc.wantOwn && !strings.HasSuffix(got, own) {
				t.Errorf("the operation's own prompt was lost: %q", got)
			}
		})
	}
}

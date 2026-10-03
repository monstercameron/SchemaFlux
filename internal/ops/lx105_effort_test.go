package ops

import (
	"context"
	"iter"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// LX-105. requestEffort is the rule for what a request carries.
func TestRequestEffort(t *testing.T) {
	background := context.Background()
	cases := []struct {
		name      string
		ctx       context.Context
		caller    types.Effort
		want      string
		preferred bool
	}{
		{"nothing set", background, types.EffortDefault, "", false},
		{"no context", nil, types.EffortDefault, "", false},
		{"the caller's value", background, types.EffortLow, "low", false},
		{"the caller's none", background, types.EffortNone, "none", false},
		{"the caller's value with no context", nil, types.EffortHigh, "high", false},
		{"the operation's preference", preferEffort(background, types.EffortNone), types.EffortDefault, "none", true},
		{"the caller's value wins over the preference", preferEffort(background, types.EffortNone), types.EffortMedium, "medium", false},
		{"the caller's none over a higher preference", preferEffort(background, types.EffortHigh), types.EffortNone, "none", false},
		{"a default preference is no preference", preferEffort(background, types.EffortDefault), types.EffortDefault, "", false},
		{"a preference on a nil context", preferEffort(nil, types.EffortLow), types.EffortDefault, "low", true},
		{"a later preference replaces an earlier one", preferEffort(preferEffort(background, types.EffortNone), types.EffortLow), types.EffortDefault, "low", true},
		{"a preference survives a derived context", context.WithValue(preferEffort(background, types.EffortNone), struct{}{}, 1), types.EffortDefault, "none", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effort, preferred := requestEffort(tc.ctx, types.OpOptions{Effort: tc.caller})
			if effort != tc.want || preferred != tc.preferred {
				t.Fatalf("requestEffort = %q (preferred %v), want %q (preferred %v)", effort, preferred, tc.want, tc.preferred)
			}
		})
	}
}

// effortSeen runs an operation against a provider that records the request,
// and returns the effort it carried.
func effortSeen(t *testing.T, reply string, run func(ctx context.Context) error) (string, bool) {
	t.Helper()
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")
	provider := &captureProvider{resp: llm.CompletionResponse{Content: reply}}
	if err := run(WithProvider(context.Background(), provider)); err != nil {
		t.Fatalf("the operation failed: %v", err)
	}
	if provider.attempts == 0 {
		t.Fatal("the operation made no provider call")
	}
	return provider.req.ReasoningEffort, provider.req.ReasoningEffortPreferred
}

type effortTicket struct {
	Title string `json:"title"`
}

// The operations whose answer is a semantic judgment, with no criteria of the
// caller's that could call for computing, prefer no reasoning. It is a
// preference: the caller's own value replaces it, and it is marked so a model
// that has no such setting is not sent it.
func TestJudgmentOperationsPreferNoReasoning(t *testing.T) {
	ops := []struct {
		name  string
		reply string
		run   func(ctx context.Context, effort types.Effort) error
	}{
		{"Classify", `{"category":"billing","confidence":0.9,"reasoning":"r"}`, func(ctx context.Context, effort types.Effort) error {
			opts := NewClassifyOptions().WithCategories([]string{"billing", "technical"})
			opts.OpOptions.Context, opts.OpOptions.Effort = ctx, effort
			_, err := Classify[string, string]("My invoice is wrong", opts)
			return err
		}},
		{"Similar", `{"score":0.9,"explanation":"x"}`, func(ctx context.Context, effort types.Effort) error {
			opts := NewSimilarOptions()
			opts.OpOptions.Context, opts.OpOptions.Effort = ctx, effort
			_, err := Similar("a", "b", opts)
			return err
		}},
		{"Deduplicate", `{"duplicates":[]}`, func(ctx context.Context, effort types.Effort) error {
			_, err := Deduplicate([]string{"a", "b"}, 0.8, types.OpOptions{Context: ctx, Effort: effort})
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name+"/prefers none", func(t *testing.T) {
			effort, preferred := effortSeen(t, op.reply, func(ctx context.Context) error { return op.run(ctx, types.EffortDefault) })
			if effort != "none" || !preferred {
				t.Errorf("the request carried effort %q (preferred %v), want none as a preference", effort, preferred)
			}
		})
		t.Run(op.name+"/the caller's value replaces it", func(t *testing.T) {
			effort, preferred := effortSeen(t, op.reply, func(ctx context.Context) error { return op.run(ctx, types.EffortLow) })
			if effort != "low" || preferred {
				t.Errorf("the request carried effort %q (preferred %v), want the caller's low", effort, preferred)
			}
		})
	}
}

// An operation that takes the caller's own criteria prefers nothing: "the
// cheapest per unit" is arithmetic, and with reasoning off the model got
// arithmetic right 13 times in 24.
func TestOperationsThatMayComputePreferNothing(t *testing.T) {
	ops := []struct {
		name  string
		reply string
		run   func(ctx context.Context) error
	}{
		{"Question", `{"answer":"42","confidence":0.9,"reasoning":"r","evidence":[]}`, func(ctx context.Context) error {
			opts := NewQuestionOptions("What is the total?")
			opts.CommonOptions.Context = ctx
			_, err := Question[string, string]("12 units at $3.50", opts)
			return err
		}},
		{"Choose", `{"id":"i-000001"}`, func(ctx context.Context) error {
			opts := NewChooseOptions().WithCriteria([]string{"cheapest per unit"})
			opts.OpOptions.Context = ctx
			_, err := Choose([]effortTicket{{"a"}, {"b"}}, opts)
			return err
		}},
		{"Verify", verifyAnswer("[]", "[]", "[]"), func(ctx context.Context) error {
			opts := NewVerifyOptions().WithMinConfidence(0)
			opts.CommonOptions.Context = ctx
			_, err := Verify("content", opts)
			return err
		}},
		// A condition is the caller's own words, and "the total exceeds $500"
		// is arithmetic.
		{"Match", `true`, func(ctx context.Context) error {
			_, err := Match(ctx, "a refund request", Like("asks for money back", func() {}))
			return err
		}},
		{"Complete", "the index had been rebuilt.", func(ctx context.Context) error {
			_, err := Complete(ctx, "The migration succeeded because", NewCompleteOptions())
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			effort, preferred := effortSeen(t, op.reply, op.run)
			if effort != "" || preferred {
				t.Errorf("the request carried effort %q (preferred %v), want none set", effort, preferred)
			}
		})
	}
}

// The option has two homes, like the model pin: the embedded OpOptions and
// CommonOptions. Either reaches the request, and CommonOptions wins.
func TestEffortReachesTheRequestFromEitherHome(t *testing.T) {
	cases := []struct {
		name             string
		common, embedded types.Effort
		want             string
	}{
		{"neither", types.EffortDefault, types.EffortDefault, ""},
		{"the embedded options", types.EffortDefault, types.EffortLow, "low"},
		{"CommonOptions", types.EffortHigh, types.EffortDefault, "high"},
		{"both: CommonOptions wins", types.EffortHigh, types.EffortLow, "high"},
		{"none is a value, not unset", types.EffortNone, types.EffortLow, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effort, preferred := effortSeen(t, "a short summary", func(ctx context.Context) error {
				opts := NewSummarizeOptions()
				opts.CommonOptions = opts.CommonOptions.WithEffort(tc.common)
				opts.CommonOptions.Context = ctx
				opts.OpOptions.Effort = tc.embedded
				_, err := Summarize("a long report", opts)
				return err
			})
			if effort != tc.want || preferred {
				t.Fatalf("the request carried effort %q (preferred %v), want %q from the caller", effort, preferred, tc.want)
			}
		})
	}
}

// A stream carries the caller's effort too.
func TestAStreamCarriesTheCallersEffort(t *testing.T) {
	var effort string
	var preferred bool
	provider := &effortRecordingStream{
		namedStreamProvider: namedStreamProvider{name: "local", seq: func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "ok", FinishReason: "stop"}}, nil)
		}},
		effort: &effort, preferred: &preferred,
	}
	withStreamingProvider(t, provider)

	opts := NewSummarizeOptions()
	opts.OpOptions.Effort = types.EffortLow
	stream, err := StreamSummarize("some input", opts)
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	for range stream.All() {
	}
	if effort != "low" || preferred {
		t.Errorf("the stream request carried effort %q (preferred %v), want the caller's low", effort, preferred)
	}
}

type effortRecordingStream struct {
	namedStreamProvider
	effort    *string
	preferred *bool
}

func (p *effortRecordingStream) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	*p.effort, *p.preferred = req.ReasoningEffort, req.ReasoningEffortPreferred
	return p.namedStreamProvider.CompleteStream(ctx, req)
}

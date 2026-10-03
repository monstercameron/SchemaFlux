package otel_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/mw"
)

// What the span of a request says when something other than the one provider it
// names answered it: a decision provider asked first, a response cache, a second
// copy of a hedge. The call's record counts what was paid for, and the span has
// to say the same.

// decisionHalf is one half of a decision pair: a provider that answers from a
// function.
type decisionHalf struct {
	name   string
	model  string
	answer func(req llm.CompletionRequest) (llm.CompletionResponse, error)
}

func (p *decisionHalf) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return p.answer(req)
}
func (p *decisionHalf) Name() string                               { return p.name }
func (p *decisionHalf) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *decisionHalf) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }
func (p *decisionHalf) ModelSentFor(requested string) string {
	if p.model != "" {
		return p.model
	}
	return requested
}

// generatorHalf answers in the form a generating model writes, with 400 tokens
// in and 20 out.
func generatorHalf(fail error) *decisionHalf {
	return &decisionHalf{name: "openai", answer: func(req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if fail != nil {
			return llm.CompletionResponse{}, fail
		}
		return llm.CompletionResponse{Content: `{"q1":true,"confidence":0.8}`, Provider: "openai", Model: req.Model, FinishReason: "stop",
			Usage: types.TokenUsage{PromptTokens: 400, CompletionTokens: 20, TotalTokens: 420}}, nil
	}}
}

// deciderHalf answers typed, with 300 tokens in and 40 out, at the confidence
// it is given, or fails with fail.
func deciderHalf(confidence float64, fail error) *decisionHalf {
	return &decisionHalf{name: "jev", model: "jev-latest", answer: func(llm.CompletionRequest) (llm.CompletionResponse, error) {
		if fail != nil {
			return llm.CompletionResponse{}, fail
		}
		return llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", FinishReason: "stop",
			DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: confidence, ConfidenceReported: true}},
			Usage:           types.TokenUsage{PromptTokens: 300, CompletionTokens: 40, TotalTokens: 340}}, nil
	}}
}

// stringAttribute reads a string attribute of a span, "" when it is absent.
func stringAttribute(span tracetest.SpanStub, key string) string {
	value, _ := attributeOf(span, key)
	return value.AsString()
}

func TestADecisionThatEscalatedIsOneRequestSpanThatSaysWhatWasPaidAndWhoWasAskedFirst(t *testing.T) {
	cases := []struct {
		name         string
		decider      *decisionHalf
		generator    *decisionHalf
		floor        float64
		wantFailure  bool
		wantAsked    string // the provider asked first, "" when nobody was
		wantModel    string
		wantReason   string
		wantAnswered string
		wantAttempts int
		wantUsage    [2]int // input, output tokens; both 0 means none are written
	}{
		{"the decider answers", deciderHalf(0.93, nil), generatorHalf(nil), 0, false, "", "", "", "jev", 1, [2]int{300, 40}},
		{"below the floor: both were paid for", deciderHalf(0.4, nil), generatorHalf(nil), 0.7, false, "jev", "jev-1.13.0", "below the floor", "openai", 2, [2]int{700, 60}},
		{"the decider failed in transit", deciderHalf(0, llm.NewAPIError("jev", "m", 503, "down")), generatorHalf(nil), 0, false, "jev", "jev-latest", "unavailable", "openai", 1, [2]int{400, 20}},
		{"the decider refused the decision", deciderHalf(0, types.Newf(types.KindUnsupportedCapability, "jev", "cannot take it")), generatorHalf(nil), 0, false, "jev", "jev-latest", "unsupported", "openai", 1, [2]int{400, 20}},
		{"below the floor and the generator then fails", deciderHalf(0.4, nil), generatorHalf(llm.NewAPIError("openai", "m", 401, "no")), 0.7, true, "jev", "jev-1.13.0", "below the floor", "", 2, [2]int{300, 40}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, exporter := traced(t)
			useProvider(t, mw.Decisions(tc.generator, tc.decider, mw.DecisionOptions{Floor: tc.floor}))
			var result types.Result[ops.Judged[bool]]
			var err error
			spans := underCaller(provider, exporter, func(ctx context.Context) {
				result, err = ops.IsResult(ctx, "the subject "+markInput, "is it so? "+markInput)
			})
			if (err != nil) != tc.wantFailure {
				t.Fatalf("the call returned %v, want a failure: %v", err, tc.wantFailure)
			}
			made := requests(spans)
			if len(made) != 1 {
				t.Fatalf("%d request spans, want one:\n%s", len(made), describe(spans))
			}
			request := made[0]

			if got := stringAttribute(request, "schemaflux.escalation.provider"); got != tc.wantAsked {
				t.Errorf("schemaflux.escalation.provider = %q, want %q", got, tc.wantAsked)
			}
			if got := stringAttribute(request, "schemaflux.escalation.model"); got != tc.wantModel {
				t.Errorf("schemaflux.escalation.model = %q, want %q", got, tc.wantModel)
			}
			if got := stringAttribute(request, "schemaflux.escalation.reason"); got != tc.wantReason {
				t.Errorf("schemaflux.escalation.reason = %q, want %q", got, tc.wantReason)
			}
			if got := stringAttribute(request, "schemaflux.response.provider"); got != tc.wantAnswered {
				t.Errorf("schemaflux.response.provider = %q, want %q", got, tc.wantAnswered)
			}
			if got := intAttribute(t, request, "schemaflux.attempts"); got != tc.wantAttempts {
				t.Errorf("schemaflux.attempts = %d, want %d", got, tc.wantAttempts)
			}
			input, _ := attributeOf(request, "gen_ai.usage.input_tokens")
			output, _ := attributeOf(request, "gen_ai.usage.output_tokens")
			if got := [2]int{int(input.AsInt64()), int(output.AsInt64())}; got != tc.wantUsage {
				t.Errorf("usage = %v, want %v", got, tc.wantUsage)
			}
			if (request.Status.Code == codes.Error) != tc.wantFailure {
				t.Errorf("status = %v, want an error: %v", request.Status.Code, tc.wantFailure)
			}

			// The span and the call's record are two accounts of one call.
			if !tc.wantFailure {
				if result.Meta.Attempts != tc.wantAttempts {
					t.Errorf("the record counts %d attempts, the test expected %d", result.Meta.Attempts, tc.wantAttempts)
				}
				if got := [2]int{result.Meta.Usage.PromptTokens, result.Meta.Usage.CompletionTokens}; got != tc.wantUsage {
					t.Errorf("the record's usage is %v, the span's %v", got, tc.wantUsage)
				}
			}
			if leaked := leaks(spans); len(leaked) != 0 {
				t.Errorf("the question reached a span: %v", leaked)
			}
		})
	}
}

func TestACacheHitIsMarkedAsOneAndSaysNothingWasPaid(t *testing.T) {
	provider, exporter := traced(t)
	useProvider(t, mw.Cache()(&scriptedProvider{answer: answering(invoiceAnswer)}))

	var misses, hits tracetest.SpanStubs
	for call := 0; call < 2; call++ {
		spans := underCaller(provider, exporter, func(ctx context.Context) {
			if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err != nil {
				t.Fatalf("Extract: %v", err)
			}
		})
		if call == 0 {
			misses = requests(spans)
		} else {
			hits = requests(spans)
		}
	}
	if len(misses) != 1 || len(hits) != 1 {
		t.Fatalf("%d and %d request spans, want one each", len(misses), len(hits))
	}
	miss, hit := misses[0], hits[0]

	if _, ok := attributeOf(miss, "schemaflux.cache.hit"); ok {
		t.Error("a miss is marked as a hit")
	}
	if intAttribute(t, miss, "schemaflux.attempts") != 1 || intAttribute(t, miss, "gen_ai.usage.input_tokens") != 11 {
		t.Errorf("the miss does not describe a provider call:\n%s", describe(misses))
	}

	if value, ok := attributeOf(hit, "schemaflux.cache.hit"); !ok || !value.AsBool() {
		t.Errorf("a hit is not marked:\n%s", describe(hits))
	}
	if got := intAttribute(t, hit, "schemaflux.attempts"); got != 0 {
		t.Errorf("a hit made %d provider calls, want 0", got)
	}
	if intAttribute(t, hit, "gen_ai.usage.input_tokens") != 0 || intAttribute(t, hit, "gen_ai.usage.output_tokens") != 0 {
		t.Errorf("a hit does not say that nothing was used:\n%s", describe(hits))
	}
	for _, key := range []string{"schemaflux.response.provider", "gen_ai.response.model", "schemaflux.cost.priced", "schemaflux.cost.total"} {
		if _, ok := attributeOf(hit, key); ok {
			t.Errorf("a hit carries %s, which describes a provider's answer", key)
		}
	}
}

func TestAHedgedRequestIsMarkedAsOne(t *testing.T) {
	provider, exporter := traced(t)
	slowFirst := &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if call == 0 {
			select {
			case <-ctx.Done():
				return llm.CompletionResponse{}, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		return answering(invoiceAnswer)(ctx, call, req)
	}}
	useProvider(t, mw.Hedge(20*time.Millisecond)(slowFirst))
	spans := underCaller(provider, exporter, func(ctx context.Context) {
		if _, err := ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx)); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	})
	made := requests(spans)
	if len(made) != 1 {
		t.Fatalf("%d request spans, want 1", len(made))
	}
	if value, ok := attributeOf(made[0], "schemaflux.hedged"); !ok || !value.AsBool() {
		t.Errorf("a hedged request is not marked:\n%s", describe(spans))
	}

	useProvider(t, mw.Hedge(5*time.Second)(&scriptedProvider{answer: answering(invoiceAnswer)}))
	spans = underCaller(provider, exporter, func(ctx context.Context) {
		_, _ = ops.Extract[invoice]("Invoice INV-1 from Acme", extractOptions(ctx))
	})
	if made := requests(spans); len(made) != 1 {
		t.Fatal("no request span")
	} else if _, ok := attributeOf(made[0], "schemaflux.hedged"); ok {
		t.Error("a request that was not hedged is marked as one")
	}
}

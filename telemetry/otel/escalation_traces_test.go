package otel

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// What the span of a request says when something other than the one provider it
// names answered it: a decision provider asked first, a response cache, a second
// copy of a hedge. The call's record counts what was paid for, and the span has
// to say the same.

func TestADecisionThatEscalatedIsOneRequestSpanThatSaysWhatWasPaidAndWhoWasAskedFirst(t *testing.T) {
	cases := []struct {
		name         string
		answer       schemaflux.DecisionAnswer
		fail         error
		generatorErr error
		floor        float64
		wantFailure  bool
		wantAsked    string
		wantReason   string
		wantAnswered string
		wantAttempts int
		wantUsage    [2]int
	}{
		{"the decider answers", schemafluxtest.Yes(0.93), nil, nil, 0, false, "", "", "decider", 1, [2]int{20, 2}},
		{"below the floor: both were paid for", schemafluxtest.Yes(0.4), nil, nil, 0.7, false, "decider", "below the floor", "generator", 2, [2]int{120, 12}},
		{"the decider failed in transit", schemafluxtest.Yes(), &schemaflux.Error{Kind: schemaflux.ErrUnavailable}, nil, 0, false, "decider", "unavailable", "generator", 1, [2]int{100, 10}},
		{"below the floor and the generator then fails", schemafluxtest.Yes(0.4), nil, &schemaflux.Error{Kind: schemaflux.ErrUnauthorized}, 0.7, true, "decider", "below the floor", "", 2, [2]int{20, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator := new(schemafluxtest.Provider)
			generator.SetName("generator")
			generator.Reply(`{"q1":true,"confidence":0.8}`)
			generator.SetUsage(schemaflux.Usage{Input: 100, Output: 10, Total: 110})
			if tc.generatorErr != nil {
				generator.Fail(tc.generatorErr)
			}
			decider := schemafluxtest.NewDecider(tc.answer)
			decider.SetName("decider")
			decider.SetUsage(schemaflux.Usage{Input: 20, Output: 2, Total: 22})
			if tc.fail != nil {
				decider.Fail(tc.fail)
			}
			pair := mw.Decisions(generator, decider, mw.DecisionOptions{Floor: tc.floor})

			var record schemaflux.Usage
			var attempts int
			var err error
			spans := withHostSpan(t, pair, func(ctx context.Context) {
				result, callErr := ops.IsResult(ctx, "the subject "+markInput, "is it so? "+markInput)
				err = callErr
				attempts = result.Meta.Attempts
				record = schemaflux.Usage{Input: result.Meta.Usage.PromptTokens, Output: result.Meta.Usage.CompletionTokens}
			})
			if (err != nil) != tc.wantFailure {
				t.Fatalf("the call returned %v, want a failure: %v", err, tc.wantFailure)
			}
			made := requestSpans(spans)
			if len(made) != 1 {
				t.Fatalf("%d request spans, want one", len(made))
			}
			request := made[0]

			if got := stringAttr(request, "schemaflux.escalation.provider"); got != tc.wantAsked {
				t.Errorf("schemaflux.escalation.provider = %q, want %q", got, tc.wantAsked)
			}
			if got := stringAttr(request, "schemaflux.escalation.reason"); got != tc.wantReason {
				t.Errorf("schemaflux.escalation.reason = %q, want %q", got, tc.wantReason)
			}
			if tc.wantAsked != "" && stringAttr(request, "schemaflux.escalation.model") == "" {
				t.Error("an escalation names no model it asked first")
			}
			if got := stringAttr(request, "schemaflux.response.provider"); got != tc.wantAnswered {
				t.Errorf("schemaflux.response.provider = %q, want %q", got, tc.wantAnswered)
			}
			if got := intAttr(t, request, "schemaflux.attempts"); got != tc.wantAttempts {
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
			if !tc.wantFailure {
				if attempts != tc.wantAttempts || [2]int{record.Input, record.Output} != tc.wantUsage {
					t.Errorf("the record counts %d attempts and %+v, the span %d and %v", attempts, record, tc.wantAttempts, tc.wantUsage)
				}
			}
			if found := leaks(spans); len(found) != 0 {
				t.Errorf("the question reached a span: %v", found)
			}
		})
	}
}

func TestACacheHitIsMarkedAsOneAndSaysNothingWasPaid(t *testing.T) {
	tracerProvider, exporter := installed(t)
	cache := mw.Cache()(&scripted{answer: func(int) (schemaflux.Response, error) {
		return schemaflux.Response{Content: `{"number":"A1","vendor":"Acme"}`}, nil
	}})
	extract := func(ctx context.Context) {
		options := ops.NewExtractOptions()
		options.CommonOptions.Context = ctx
		if _, err := ops.ExtractResult[invoice]("Invoice from Acme", options); err != nil {
			t.Fatalf("ExtractResult: %v", err)
		}
	}
	misses := requestSpans(underHostSpan(tracerProvider, exporter, cache, extract))
	hits := requestSpans(underHostSpan(tracerProvider, exporter, cache, extract))
	if len(misses) != 1 || len(hits) != 1 {
		t.Fatalf("%d and %d request spans, want one each", len(misses), len(hits))
	}
	miss, hit := misses[0], hits[0]

	if _, ok := attributeOf(miss, "schemaflux.cache.hit"); ok {
		t.Error("a miss is marked as a hit")
	}
	if intAttr(t, miss, "schemaflux.attempts") != 1 || intAttr(t, miss, "gen_ai.usage.input_tokens") != 11 {
		t.Errorf("the miss does not describe a provider call: %v", typed(miss.Attributes))
	}
	if value, ok := attributeOf(hit, "schemaflux.cache.hit"); !ok || !value.AsBool() {
		t.Errorf("a hit is not marked: %v", typed(hit.Attributes))
	}
	if got := intAttr(t, hit, "schemaflux.attempts"); got != 0 {
		t.Errorf("a hit made %d provider calls, want 0", got)
	}
	if intAttr(t, hit, "gen_ai.usage.input_tokens") != 0 || intAttr(t, hit, "gen_ai.usage.output_tokens") != 0 {
		t.Errorf("a hit does not say that nothing was used: %v", typed(hit.Attributes))
	}
	for _, key := range []string{"schemaflux.response.provider", "gen_ai.response.model", "schemaflux.cost.priced", "schemaflux.cost.total"} {
		if _, ok := attributeOf(hit, key); ok {
			t.Errorf("a hit carries %s, which describes a provider's answer", key)
		}
	}
}

// slowFirstCopy is a provider whose first call is slow enough to be hedged.
type slowFirstCopy struct{ calls chan struct{} }

func (p *slowFirstCopy) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	select {
	case p.calls <- struct{}{}:
		// The first call: it waits to be overtaken.
		select {
		case <-ctx.Done():
			return schemaflux.Response{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	default:
	}
	return schemaflux.Response{Content: `{"number":"A1","vendor":"Acme"}`, Model: req.Model, FinishReason: "stop",
		Usage: schemaflux.Usage{Input: 5, Output: 5, Total: 10}}, nil
}
func (*slowFirstCopy) Name() string                      { return "local" }
func (*slowFirstCopy) RetryPolicy() (int, time.Duration) { return -1, time.Millisecond }

func TestAHedgedRequestIsMarkedAsOne(t *testing.T) {
	tracerProvider, exporter := installed(t)
	extract := func(ctx context.Context) {
		options := ops.NewExtractOptions()
		options.CommonOptions.Context = ctx
		if _, err := ops.ExtractResult[invoice]("Invoice from Acme", options); err != nil {
			t.Fatalf("ExtractResult: %v", err)
		}
	}

	hedged := requestSpans(underHostSpan(tracerProvider, exporter, mw.Hedge(20*time.Millisecond)(&slowFirstCopy{calls: make(chan struct{}, 1)}), extract))
	if len(hedged) != 1 {
		t.Fatalf("%d request spans, want 1", len(hedged))
	}
	if value, ok := attributeOf(hedged[0], "schemaflux.hedged"); !ok || !value.AsBool() {
		t.Errorf("a hedged request is not marked: %v", typed(hedged[0].Attributes))
	}

	plain := requestSpans(underHostSpan(tracerProvider, exporter, mw.Hedge(5*time.Second)(&scripted{answer: func(int) (schemaflux.Response, error) {
		return schemaflux.Response{Content: `{"number":"A1","vendor":"Acme"}`}, nil
	}}), extract))
	if len(plain) != 1 {
		t.Fatal("no request span")
	}
	if _, ok := attributeOf(plain[0], "schemaflux.hedged"); ok {
		t.Error("a request that was not hedged is marked as one")
	}
}

// The strings of an escalation come from a provider and a middleware, so they
// reach a span cut and printable like every other string that does.
func TestTheEscalationOfARequestReachesTheSpanBoundedAndPrintable(t *testing.T) {
	long := strings.Repeat("x", 5000)
	observer, exporter := observerOn(t)
	ctx := observer.Started(context.Background(), telemetry.Event{Operation: "is", Provider: "p", Model: "m"})
	observer.Finished(ctx, telemetry.Result{
		Attempts: 2, Provider: "p", Model: "m",
		AskedFirst: long, AskedFirstModel: "model\n" + long, Escalated: "below\x00the floor" + long,
	})
	span := exporter.GetSpans()[0]
	for key, limit := range map[string]int{"schemaflux.escalation.provider": 64, "schemaflux.escalation.model": 128, "schemaflux.escalation.reason": 64} {
		value, ok := attributeOf(span, key)
		if !ok || len(value.AsString()) != limit {
			t.Errorf("%s has %d bytes (set: %v), want %d", key, len(value.AsString()), ok, limit)
		}
		for index := 0; index < len(value.AsString()); index++ {
			if c := value.AsString()[index]; c < 0x20 || c > 0x7e {
				t.Errorf("%s holds the byte %#x", key, c)
				break
			}
		}
	}
	if value, _ := attributeOf(span, "schemaflux.escalation.model"); !strings.HasPrefix(value.AsString(), "model?") {
		t.Errorf("the newline in the model was not replaced: %q", value.AsString()[:8])
	}
}

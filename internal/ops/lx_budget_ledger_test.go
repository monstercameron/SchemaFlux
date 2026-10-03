package ops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// The process-wide ledger is told about an answer that was produced and then
// refused, as a client's own ledger is. It was told only about the answer a
// call accepted, so for a caller with no client budget a truncated answer, and
// every attempt thrown away before a retry, cost nothing on any ledger.
func TestTheProcessLedgerIsToldAboutAnswersThatWereRefused(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	response := func(content, finish string, u types.TokenUsage) llm.CompletionResponse {
		return llm.CompletionResponse{Content: content, FinishReason: finish, Model: "gpt-6-luna", Provider: "openai", Usage: u}
	}
	good := response(`{"ok":true}`, "stop", usage)
	truncated := response(`{"ok":tr`, "length", usage)
	empty := response("", "stop", usage)

	spentBy := func(t *testing.T, ctx context.Context, responses []llm.CompletionResponse, retries int) float64 {
		t.Helper()
		since := time.Now().Add(-time.Millisecond)
		before := pricing.GetTotalCost(since, nil)
		provider := &answersProvider{responses: responses, retries: retries}
		_, _ = CallLLM(ctx, provider, "system", "user", types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json"})
		return pricing.GetTotalCost(since, nil) - before
	}
	one := spentBy(t, context.Background(), []llm.CompletionResponse{good}, 0)
	if one <= 0 {
		t.Fatalf("an accepted answer cost %v on the process ledger; the model is not priced and this test cannot measure", one)
	}

	withBudget := WithExecConfig(context.Background(), &ExecConfig{Budget: &recordingBudget{}})
	cases := []struct {
		name      string
		ctx       context.Context
		responses []llm.CompletionResponse
		retries   int
		answers   float64 // how many answers' worth of spend
	}{
		{"an accepted answer", context.Background(), []llm.CompletionResponse{good}, 0, 1},
		{"a truncated answer", context.Background(), []llm.CompletionResponse{truncated}, 0, 1},
		{"an empty answer", context.Background(), []llm.CompletionResponse{empty}, 0, 1},
		{"an empty answer and then a good one", context.Background(), []llm.CompletionResponse{empty, good}, 2, 2},
		{"two empty answers and then a good one", context.Background(), []llm.CompletionResponse{empty, empty, good}, 2, 3},
		{"empty answers until the attempts run out", context.Background(), []llm.CompletionResponse{empty}, 2, 3},
		{"a truncated answer that reports no usage", context.Background(), []llm.CompletionResponse{response(`{"ok":tr`, "length", types.TokenUsage{})}, 0, 0},
		{"a truncated answer, with a client budget too", withBudget, []llm.CompletionResponse{truncated}, 0, 1},
		{"an empty answer and then a good one, with a client budget", withBudget, []llm.CompletionResponse{empty, good}, 2, 2},
		{"an accepted answer, with a client budget", withBudget, []llm.CompletionResponse{good}, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spentBy(t, tc.ctx, tc.responses, tc.retries)
			want := tc.answers * one
			if diff := got - want; diff > one/1000 || diff < -one/1000 {
				t.Errorf("the process ledger grew by %v, want %v (%v answers)", got, want, tc.answers)
			}
		})
	}
}

// An answer served from a response cache was not paid for. The cache zeroes the
// usage on a hit, and zero usage is recorded as spend of unknown size, so one
// hit turned a client's total into a floor for a call that cost nothing.
func TestACacheHitIsNotRecordedAsSpend(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	answer := func(cached bool, u types.TokenUsage) llm.CompletionResponse {
		return llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai", Usage: u, Cached: cached}
	}
	cases := []struct {
		name string
		resp llm.CompletionResponse
		want []bool
	}{
		{"a hit", answer(true, types.TokenUsage{}), nil},
		{"a miss with usage", answer(false, usage), []bool{true}},
		{"a miss that reports no usage", answer(false, types.TokenUsage{}), []bool{false}},
		// A cache that replays the usage is still not a second purchase.
		{"a hit that carries usage", answer(true, usage), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := &recordingBudget{}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
			since := time.Now().Add(-time.Millisecond)
			before := pricing.GetTotalCost(since, nil)

			provider := &answersProvider{responses: []llm.CompletionResponse{tc.resp}}
			if _, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json"}); err != nil {
				t.Fatalf("CallLLM: %v", err)
			}
			if got := budget.seen(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the budget was told %v, want %v", got, tc.want)
			}
			if tc.resp.Cached {
				if grew := pricing.GetTotalCost(since, nil) - before; grew != 0 {
					t.Errorf("the process ledger grew by %v for a cache hit", grew)
				}
			}
		})
	}
}

// allowingBudget lets a number of checks through and refuses the rest.
type allowingBudget struct {
	allow   int
	checked int
}

func (b *allowingBudget) Check() error {
	b.checked++
	if b.checked > b.allow {
		return errors.New("this client's budget is spent")
	}
	return nil
}
func (b *allowingBudget) Record(float64, bool) {}

// The budget is asked before every attempt and not only before the first. An
// attempt that is thrown away is charged, so the budget can run out between
// attempts, and the retry was sent all the same.
func TestTheBudgetIsCheckedBeforeEveryAttempt(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	empty := llm.CompletionResponse{Content: "", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai", Usage: usage}
	good := llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai", Usage: usage}

	cases := []struct {
		name      string
		allow     int
		responses []llm.CompletionResponse
		retries   int
		calls     int
		refused   bool
	}{
		{"spent before the first attempt", 0, []llm.CompletionResponse{good}, 3, 0, true},
		{"spent after the first attempt", 1, []llm.CompletionResponse{empty, good}, 3, 1, true},
		{"spent after the second attempt", 2, []llm.CompletionResponse{empty, empty, good}, 3, 2, true},
		{"enough for every attempt", 10, []llm.CompletionResponse{empty, empty, good}, 3, 3, false},
		{"one attempt, one check", 1, []llm.CompletionResponse{good}, 3, 1, false},
		{"enough for the retry that succeeds", 2, []llm.CompletionResponse{empty, good}, 3, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := &allowingBudget{allow: tc.allow}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
			provider := &answersProvider{responses: tc.responses, retries: tc.retries}

			_, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json"})
			if provider.calls != tc.calls {
				t.Errorf("the provider was called %d times, want %d", provider.calls, tc.calls)
			}
			if refused := err != nil && strings.Contains(err.Error(), "budget is spent"); refused != tc.refused {
				t.Errorf("err = %v, want a budget refusal: %v", err, tc.refused)
			}
		})
	}
}

type ledgerEntry struct {
	cost   float64
	priced bool
}

// ledgerBudget records what it is told, figure and flag.
type ledgerBudget struct{ entries []ledgerEntry }

func (b *ledgerBudget) Check() error { return nil }
func (b *ledgerBudget) Record(cost float64, priced bool) {
	b.entries = append(b.entries, ledgerEntry{cost, priced})
}

// chargeBudget on its own: what one response is recorded as.
func TestChargeBudget(t *testing.T) {
	provider := &answersProvider{}
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	priced := func(resp llm.CompletionResponse) float64 {
		u := resp.Usage
		return pricing.CalculateCost(&u, "gpt-6-luna", "openai").TotalCost
	}
	full := llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: usage}
	if priced(full) <= 0 {
		t.Fatal("gpt-6-luna is not priced, and this test cannot tell a figure from none")
	}

	cases := []struct {
		name  string
		model string // the model asked for, used when the response names none
		resp  llm.CompletionResponse
		want  []ledgerEntry
	}{
		{"usage on a priced model", "gpt-6-luna", full, []ledgerEntry{{priced(full), true}}},
		{"no usage at all", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai"}, []ledgerEntry{{0, false}}},
		{"served from a cache", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Cached: true}, nil},
		{"served from a cache that kept the usage", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: usage, Cached: true}, nil},
		{"the response names no model", "gpt-6-luna", llm.CompletionResponse{Provider: "openai", Usage: usage}, []ledgerEntry{{priced(full), true}}},
		{"the response names no provider", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Usage: usage}, []ledgerEntry{{priced(full), true}}},
		{"the response names neither", "gpt-6-luna", llm.CompletionResponse{Usage: usage}, []ledgerEntry{{priced(full), true}}},
		{"the model that answered is not the one asked for", "some-other-model", full, []ledgerEntry{{priced(full), true}}},
		{"usage on a model with no price", "a-model-with-no-price", llm.CompletionResponse{Model: "a-model-with-no-price", Provider: "openai", Usage: usage}, []ledgerEntry{{0, false}}},
		{"prompt tokens only", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: types.TokenUsage{PromptTokens: 1000}},
			[]ledgerEntry{{priced(llm.CompletionResponse{Usage: types.TokenUsage{PromptTokens: 1000}}), true}}},
		{"completion tokens only", "gpt-6-luna", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai", Usage: types.TokenUsage{CompletionTokens: 500}},
			[]ledgerEntry{{priced(llm.CompletionResponse{Usage: types.TokenUsage{CompletionTokens: 500}}), true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := &ledgerBudget{}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
			chargeBudget(ctx, provider, tc.model, tc.resp)
			if !reflect.DeepEqual(budget.entries, tc.want) {
				t.Errorf("recorded %+v, want %+v", budget.entries, tc.want)
			}
		})
	}

	// With no client budget on the context there is nothing to tell.
	chargeBudget(context.Background(), provider, "gpt-6-luna", full)
}

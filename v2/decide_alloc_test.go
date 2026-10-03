package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

func TestIsAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string][]Options{
		"no options":           nil,
		"one options value":    {{Model: "alloc-model", Steering: "be strict", MaxOutputTokens: 100}},
		"two options values":   {{Model: "alloc-model"}, {Steering: "be strict"}},
		"every common setting": {{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be strict", MaxOutputTokens: 100, WebSearch: true}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			merged := mergeOptions(opts)
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: `{"q1":true}`,
				call: func(ctx context.Context) error {
					_, err := Is(ctx, "Please refund my order.", "Is the customer asking for a refund?", opts...)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Is(ctx, "Please refund my order.", "Is the customer asking for a refund?", decisionOptions(merged))
					return err
				},
			})
		})
	}
}

func TestIsDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":true,"confidence":0.8}`,
		call: func(ctx context.Context) error {
			_, err := IsDetail(ctx, "Please refund my order.", "Is the customer asking for a refund?")
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.IsResult(ctx, "Please refund my order.", "Is the customer asking for a refund?", decisionOptions(Options{}))
			return err
		},
	})
}

// allocLabels and allocTriage are what the allocation tests of the other
// decision functions ask about.
var allocLabels = []string{"billing", "technical", "shipping"}

type allocTriage struct {
	Spam    bool   `ask:"Is this message spam?"`
	Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
}

const (
	allocSubject  = "I was charged twice for my order."
	allocQuestion = "Which team should handle this?"
)

func TestWhichAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string][]Options{
		"no options":           nil,
		"one options value":    {{Model: "alloc-model", Steering: "be strict", MaxOutputTokens: 100}},
		"two options values":   {{Model: "alloc-model"}, {Steering: "be strict"}},
		"every common setting": {{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be strict", MaxOutputTokens: 100, WebSearch: true}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			merged := mergeOptions(opts)
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: `{"q1":2}`,
				call: func(ctx context.Context) error {
					_, err := Which(ctx, allocSubject, allocQuestion, allocLabels, opts...)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Which(ctx, allocSubject, allocQuestion, allocLabels, decisionOptions(merged))
					return err
				},
			})
		})
	}
}

func TestWhichDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":2,"confidence":0.8}`,
		call: func(ctx context.Context) error {
			_, err := WhichDetail(ctx, allocSubject, allocQuestion, allocLabels)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.WhichResult(ctx, allocSubject, allocQuestion, allocLabels, decisionOptions(Options{}))
			return err
		},
	})
}

func TestWhichOrNoneAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":2}`,
		call: func(ctx context.Context) error {
			_, _, err := WhichOrNone(ctx, allocSubject, allocQuestion, allocLabels)
			return err
		},
		internal: func(ctx context.Context) error {
			_, _, err := ops.WhichOrNone(ctx, allocSubject, allocQuestion, allocLabels, decisionOptions(Options{}))
			return err
		},
	})
}

// A call with no labels asks nothing, so the internal call never reads its
// context and does not pin the client: the function allocates what the internal
// call does under a client, and the pin and nothing else on the default client.
// requireAllocsLikeTheInternalCall holds both to the internal call's allocations
// on a context that pins, which a call that reads none does not.
func TestWhichOrNoneWithNoLabelsAllocatesAsTheInternalCallAndTheDefaultClientsPin(t *testing.T) {
	model := &allocProvider{reply: `{"q1":1}`}
	call := func(ctx context.Context) error {
		_, _, err := WhichOrNone(ctx, allocSubject, allocQuestion, nil)
		return err
	}
	internal := func(ctx context.Context) error {
		_, _, err := ops.WhichOrNone(ctx, allocSubject, allocQuestion, nil, decisionOptions(Options{}))
		return err
	}
	client := WithClient(context.Background(), Client{Provider: model})
	wrapped := testing.AllocsPerRun(allocRuns, func() { _ = call(client) })
	direct := testing.AllocsPerRun(allocRuns, func() { _ = internal(client) })
	if wrapped != direct {
		t.Errorf("under a client the function allocates %v times and the internal call %v", wrapped, direct)
	}
	previous := Default()
	SetDefault(Client{Provider: model})
	t.Cleanup(func() { SetDefault(previous) })
	background := context.Background()
	onDefault := testing.AllocsPerRun(allocRuns, func() { _ = call(background) })
	if onDefault != direct+1 {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin", onDefault, direct)
	}
}

func TestWhichOrNoneDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":2,"confidence":0.8}`,
		call: func(ctx context.Context) error {
			_, _, err := WhichOrNoneDetail(ctx, allocSubject, allocQuestion, allocLabels)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.WhichOrNoneResult(ctx, allocSubject, allocQuestion, allocLabels, decisionOptions(Options{}))
			return err
		},
	})
}

func TestRateAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":4}`,
		call: func(ctx context.Context) error {
			_, err := Rate(ctx, allocSubject, "How urgent is this?", 1, 5)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Rate(ctx, allocSubject, "How urgent is this?", 1, 5, decisionOptions(Options{}))
			return err
		},
	})
}

func TestRateDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":4,"confidence":0.8}`,
		call: func(ctx context.Context) error {
			_, err := RateDetail(ctx, allocSubject, "How urgent is this?", 1, 5)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.RateResult(ctx, allocSubject, "How urgent is this?", 1, 5, decisionOptions(Options{}))
			return err
		},
	})
}

func TestAssessAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":true,"q2":2,"q3":4}`,
		call: func(ctx context.Context) error {
			_, err := Assess[allocTriage](ctx, allocSubject)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Assess[allocTriage](ctx, allocSubject, decisionOptions(Options{}))
			return err
		},
	})
}

func TestAssessDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"q1":true,"q2":2,"q3":4,"confidence":0.8}`,
		call: func(ctx context.Context) error {
			_, err := AssessDetail[allocTriage](ctx, allocSubject)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.AssessResult[allocTriage](ctx, allocSubject, decisionOptions(Options{}))
			return err
		},
	})
}

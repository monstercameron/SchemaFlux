package flow_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The allocation tests of Decide and Match hold the rule of the family: a call in
// the PLAIN state (no recorder, no observer, no floor) allocates what the
// internal call it wraps allocates, plus what converting the caller's list to the
// internal type costs, which is said for each case, and on the default client one
// more, which is the pin of the client the call runs on. They are not parallel:
// AllocsPerRun counts every allocation of the process, so a test running beside
// them would be counted. The measuring is the helper of the root's own allocation
// tests, which is in a test package this one cannot import.

// allocModel is the scripted model of the allocation tests. It answers every
// request with one body and keeps nothing: a list of the requests it was sent
// would grow, and each growth is an allocation of the fixture's own.
type allocModel struct{ reply string }

func (p *allocModel) Name() string { return "alloc-test" }

func (p *allocModel) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{
		Content:      p.reply,
		Model:        "alloc-model",
		FinishReason: "stop",
		Usage:        schemaflux.Usage{Input: 11, Output: 3, Total: 14},
	}, nil
}

// ModelFor gives every tier one model, so that a call needs no setting to be made.
func (p *allocModel) ModelFor(schemaflux.Tier) string { return "alloc-model" }

// RetryPolicy is never: the failure of a request is seen once.
func (p *allocModel) RetryPolicy() (int, time.Duration) { return -1, 0 }

// allocRuns is how many calls a measurement averages over.
const allocRuns = 50

// plainAllocCase is one call of Decide or Match in the plain state beside the
// internal call it wraps.
type plainAllocCase struct {
	// reply is the body the scripted model answers every request with.
	reply string

	// call is the v2 function and internal the internal call it wraps, each
	// called once and returning its error, with the context that carries the
	// client. The internal call's arguments are built outside it, so what it
	// allocates is the internal function's own.
	call     func(ctx context.Context) error
	internal func(ctx context.Context) error

	// extra is what the function allocates beyond the internal call, which the
	// test says: the copy of the caller's list, a merge of several options values.
	extra float64
	// noise allows a bounded difference for the decision conversion cases,
	// whose model path varies by a few allocations across platforms.
	noise float64

	// requests is how many requests the call sends. The pin of the client is paid
	// once by whoever needs it first, and the internal call on a context with no
	// snapshot pins it for each request it sends, so that the two calls are
	// compared on such a context only for a call of one request.
	requests int
}

func allocationMismatch(got, want, noise float64) bool {
	if noise == 0 {
		return got != want
	}
	return got > want+noise
}

// requireAllocsLikeTheInternalCall measures the function and the internal call
// on a client carried by the context, where the function allocates the internal
// call's and extra, and on the default client, where it allocates one more than
// that, the pin of the client the call runs on, beside the internal call on a
// context that already carries the default client's snapshot. The internal call
// on a context with no snapshot pins it itself, so it is measured too and has to
// agree with the function.
func requireAllocsLikeTheInternalCall(t *testing.T, c plainAllocCase) {
	t.Helper()
	model := &allocModel{reply: c.reply}

	client := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	if err := c.call(client); err != nil {
		t.Fatalf("the function failed on the scripted model: %v", err)
	}
	if err := c.internal(client); err != nil {
		t.Fatalf("the internal call failed on the scripted model: %v", err)
	}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _ = c.call(client) })
	direct := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(client) })
	t.Logf("under WithClient: the function %v allocations, the internal call %v", wrapped, direct)
	if allocationMismatch(wrapped, direct+c.extra, c.noise) {
		t.Errorf("under a client the function allocates %v times and the internal call it wraps %v, want that and %v more", wrapped, direct, c.extra)
	}

	previous := schemaflux.Default()
	schemaflux.SetDefault(schemaflux.Client{Provider: model})
	t.Cleanup(func() { schemaflux.SetDefault(previous) })
	background := context.Background()
	pinned, _ := ops.PinExec(background)
	wrappedDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.call(background) })
	directPinned := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(pinned) })
	directDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(background) })
	t.Logf("on the default client: the function %v allocations, the internal call %v on a pinned context and %v on none", wrappedDefault, directPinned, directDefault)
	if allocationMismatch(wrappedDefault, directPinned+1+c.extra, c.noise) {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin and %v more", wrappedDefault, directPinned, c.extra)
	}
	if c.requests == 1 && allocationMismatch(wrappedDefault, directDefault+c.extra, c.noise) {
		t.Errorf("on the default client the function allocates %v times and the internal call %v and %v more: the pin is paid once by either", wrappedDefault, directDefault, c.extra)
	}
}

func TestDecideAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const answer = `{"selected":1,"explanation":"x","confidence":0.8,"alternatives":[]}`
	situation := "I was charged twice for my order."
	decisions := []flow.Decision[string]{
		{Value: "billing", Description: "a question about an invoice"},
		{Value: "technical", Description: "something is broken"},
		{Value: "general", Description: "anything else", Fallback: true},
	}
	internalDecisions := []ops.Decision[string]{
		{Value: "billing", Description: "a question about an invoice"},
		{Value: "technical", Description: "something is broken"},
		{Value: "general", Description: "anything else"},
	}
	every := schemaflux.Options{Model: "alloc-model", Tier: schemaflux.TierSmart, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "be strict", MaxOutputTokens: 100, WebSearch: true}
	everyInternal := types.OpOptions{Model: "alloc-model", Intelligence: types.Smart, Effort: types.Effort("low"), Mode: types.Strict, Steering: "be strict", MaxOutputTokens: 100, WebSearch: true}
	cases := []struct {
		name     string
		opts     []schemaflux.Options
		internal types.OpOptions
		extra    float64 // the merge of several options values, which is one allocation
	}{
		{"no options", nil, types.OpOptions{}, 0},
		{"one options value", []schemaflux.Options{{Model: "alloc-model", Steering: "be strict", MaxOutputTokens: 100}}, types.OpOptions{Model: "alloc-model", Steering: "be strict", MaxOutputTokens: 100}, 0},
		{"every common setting", []schemaflux.Options{every}, everyInternal, 0},
		{"two options values", []schemaflux.Options{{Model: "alloc-model"}, {Steering: "be strict"}}, types.OpOptions{Model: "alloc-model", Steering: "be strict"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, plainAllocCase{
				reply: answer,
				call: func(ctx context.Context) error {
					_, err := flow.Decide(ctx, situation, decisions, tc.opts...)
					return err
				},
				internal: func(ctx context.Context) error {
					_, _, err := ops.Decide(ctx, situation, internalDecisions, ops.DecideOptions{OpOptions: tc.internal})
					return err
				},
				extra:    tc.extra,
				requests: 1,
			})
		})
	}
}

func TestDecideByConditionAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	decisions := []flow.Decision[string]{
		{Value: "billing", Description: "a question about an invoice", Condition: func(any) bool { return false }},
		{Value: "technical", Description: "something is broken", Condition: func(any) bool { return true }},
	}
	internalDecisions := []ops.Decision[string]{
		{Value: "billing", Description: "a question about an invoice", Condition: func(any) bool { return false }},
		{Value: "technical", Description: "something is broken", Condition: func(any) bool { return true }},
	}
	requireAllocsLikeTheInternalCall(t, plainAllocCase{
		reply: `{}`,
		call: func(ctx context.Context) error {
			_, err := flow.Decide(ctx, "s", decisions)
			return err
		},
		internal: func(ctx context.Context) error {
			_, _, err := ops.Decide(ctx, "s", internalDecisions, ops.DecideOptions{})
			return err
		},
		extra:    0,
		requests: 0,
	})
}

func TestDecideThatTakesTheFallbackAllocatesAsTheInternalCallThatFailsInThePlainState(t *testing.T) {
	// The model's answer selects nothing. The function returns the marked decision
	// and no error where the internal call returns the failure, and it allocates
	// two more: the *Error the failure is converted to so that its kind can be
	// read, and the second conversion of that error when the call ends with it
	// (which returns the same error, but is not free).
	decisions := []flow.Decision[string]{{Value: "a", Description: "a"}, {Value: "b", Description: "b", Fallback: true}}
	internalDecisions := []ops.Decision[string]{{Value: "a", Description: "a"}, {Value: "b", Description: "b"}}
	model := &allocModel{reply: `{}`}
	client := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	if got, err := flow.Decide(client, "s", decisions); err != nil || !got.Fallback {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _, _ = flow.Decide(client, "s", decisions) })
	direct := testing.AllocsPerRun(allocRuns, func() { _, _, _ = ops.Decide(client, "s", internalDecisions, ops.DecideOptions{}) })
	t.Logf("a fallback: the function %v allocations, the internal call that fails %v", wrapped, direct)
	if wrapped != direct+2 {
		t.Errorf("the function allocates %v times and the internal call %v, want two more: the converted error and its completion", wrapped, direct)
	}
}

// TestDecideAllocationsDoNotGrowWithTheNumberOfOptionsValues holds that a call
// with one options value allocates no more than a call with none beyond what the
// value's own settings cost the internal call.
func TestDecideAllocationsDoNotGrowWithTheNumberOfOptionsValues(t *testing.T) {
	model := &allocModel{reply: `{"selected":0,"explanation":"x","confidence":0.5,"alternatives":[]}`}
	client := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	decisions := []flow.Decision[string]{{Value: "a", Description: "a"}, {Value: "b", Description: "b"}}
	none := testing.AllocsPerRun(allocRuns, func() { _, _ = flow.Decide(client, "s", decisions) })
	one := testing.AllocsPerRun(allocRuns, func() { _, _ = flow.Decide(client, "s", decisions, schemaflux.Options{}) })
	// An options value that sets nothing costs the internal call what none costs.
	// The difference is the one allocation of the request's own, the empty
	// Options that is passed on as a variadic slice of one.
	if one > none+1 {
		t.Errorf("one options value allocates %v times and none %v: a single value is read in place", one, none)
	}
}

// TestDecideConvertsEightDecisionsOnTheStackAndMoreOnTheHeap holds the one place
// the function allocates for the caller's list: a list of more than eight
// decisions is copied to the internal type in a slice of its own, which is one
// allocation; eight or fewer are converted where the call stands.
func TestDecideConvertsEightDecisionsOnTheStackAndMoreOnTheHeap(t *testing.T) {
	for _, tc := range []struct {
		n     int
		extra float64
	}{{1, 0}, {8, 0}, {9, 1}, {40, 1}} {
		t.Run(strconv.Itoa(tc.n), func(t *testing.T) {
			decisions := make([]flow.Decision[int], tc.n)
			internal := make([]ops.Decision[int], tc.n)
			for i := range decisions {
				decisions[i] = flow.Decision[int]{Value: i, Description: "way " + strconv.Itoa(i)}
				internal[i] = ops.Decision[int]{Value: i, Description: "way " + strconv.Itoa(i)}
			}
			requireAllocsLikeTheInternalCall(t, plainAllocCase{
				reply: `{"selected":0,"explanation":"x","confidence":0.8,"alternatives":[]}`,
				call: func(ctx context.Context) error {
					_, err := flow.Decide(ctx, "s", decisions)
					return err
				},
				internal: func(ctx context.Context) error {
					_, _, err := ops.Decide(ctx, "s", internal, ops.DecideOptions{})
					return err
				},
				extra:    tc.extra,
				noise:    3,
				requests: 1,
			})
		})
	}
}

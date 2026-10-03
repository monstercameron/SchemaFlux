package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

func TestDeduplicateAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95}]}`
	items := []string{"a@work.test", "A at work", "b@home.test"}
	cases := map[string]Options{
		"no options":           {},
		"one options value":    {Model: "alloc-model", Steering: "find repeats"},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "find repeats", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Deduplicate(ctx, items, 0.9, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Deduplicate(items, 0.9, deduplicateInternal(ctx, o))
					return err
				},
			})
		})
	}
}

// The result of the internal call is returned as it is, converted to the root's
// type with no copy, so a result with groups, a result with none and a list that
// asks nothing all allocate what the internal call does.
func TestDeduplicateAllocatesAsTheInternalCallWhateverTheResult(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		items []string
	}{
		{"no duplicates", `{"duplicates":[]}`, []string{"a", "b", "c"}},
		{"several groups", `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9},{"index":3,"duplicate_of":2,"similarity":0.9}]}`, []string{"a", "b", "c", "d"}},
		{"items written the same", `{"duplicates":[]}`, []string{"a", "a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: tc.reply,
				call: func(ctx context.Context) error {
					_, err := Deduplicate(ctx, tc.items, 0.8)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Deduplicate(tc.items, 0.8, deduplicateInternal(ctx, Options{}))
					return err
				},
			})
		})
	}
}

// A list that asks nothing sends no request, so the internal call never reads
// the client and never pins it, and the function pays for the pin alone: under a
// client the two allocate the same, and on the default client the function
// allocates the internal call's count on a pinned context plus the pin. The helper
// above also compares the function with the internal call on a context with no
// snapshot, which pins the client itself only when it sends something.
func TestDeduplicateThatAsksNothingAllocatesAsTheInternalCallPlusThePinOnTheDefaultClient(t *testing.T) {
	for name, items := range map[string][]string{"all written the same": {"a", "a"}, "one item": {"a"}, "no items": nil} {
		t.Run(name, func(t *testing.T) {
			model := &allocProvider{reply: `{"duplicates":[]}`}
			call := func(ctx context.Context) { _, _ = Deduplicate(ctx, items, 0.8) }
			internal := func(ctx context.Context) { _, _ = ops.Deduplicate(items, 0.8, deduplicateInternal(ctx, Options{})) }
			client := WithClient(context.Background(), Client{Provider: model})
			call(client)
			internal(client)
			wrapped := testing.AllocsPerRun(allocRuns, func() { call(client) })
			direct := testing.AllocsPerRun(allocRuns, func() { internal(client) })
			t.Logf("under WithClient: the function %v allocations, the internal call %v", wrapped, direct)
			if wrapped != direct {
				t.Errorf("under a client the function allocates %v times and the internal call %v", wrapped, direct)
			}

			previous := Default()
			SetDefault(Client{Provider: model})
			t.Cleanup(func() { SetDefault(previous) })
			background := context.Background()
			pinned, _ := ops.PinExec(background)
			wrappedDefault := testing.AllocsPerRun(allocRuns, func() { call(background) })
			directPinned := testing.AllocsPerRun(allocRuns, func() { internal(pinned) })
			t.Logf("on the default client: the function %v allocations, the internal call %v on a pinned context", wrappedDefault, directPinned)
			if wrappedDefault != directPinned+1 {
				t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin", wrappedDefault, directPinned)
			}
		})
	}
}

func TestDeduplicateWithSeveralOptionsValuesAllocatesAsTheInternalCallWithTheirMerge(t *testing.T) {
	const reply = `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95}]}`
	items := []string{"a@work.test", "A at work"}
	first, second := Options{Model: "alloc-model"}, Options{Steering: "find repeats"}
	merged := mergeOptions([]Options{first, second})
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: reply,
		call: func(ctx context.Context) error {
			_, err := Deduplicate(ctx, items, 0.9, first, second)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Deduplicate(items, 0.9, deduplicateInternal(ctx, merged))
			return err
		},
	})
}

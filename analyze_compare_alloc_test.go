package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// Compare, Diff and Cluster are held to the allocation rule of the family (C3): a
// call in the plain state allocates what the internal call it wraps allocates,
// and on the default client one more, the pin. Each of them returns a list in
// the root's own type instead of the internal one, and a list that is not empty
// is copied once, which is one allocation more per list. So each is measured
// twice: with nothing to convert, where the function and the internal call are
// equal, and with lists, where the function is exactly one more for each list
// that is not empty, and no more.

// requireAllocsPlusConversions is requireAllocsLikeTheInternalCall for a call
// whose result converts lists: under a client the function allocates exactly
// extra more than the internal call, and on the default client extra more than
// the internal call on a pinned context plus the pin. extra is the number of
// allocations the function makes that the internal call does not: one for each
// list it copies.
//
// asksModel says whether the call reaches a model. One that does not never needs
// the client, so the internal call never pins it, while the function pins it
// first thing, whether or not the call goes on to ask anything, because its
// Record and its floor are the client's: on the default client that call is the
// internal call's allocations plus the pin plus extra, and there is no pinned
// context to measure the internal call on.
func requireAllocsPlusConversions(t *testing.T, c allocCase, extra float64, asksModel bool) {
	t.Helper()
	model := &allocProvider{reply: c.reply}

	client := WithClient(context.Background(), Client{Provider: model})
	if err := c.call(client); err != nil {
		t.Fatalf("the function failed on the scripted model: %v", err)
	}
	if err := c.internal(client); err != nil {
		t.Fatalf("the internal call failed on the scripted model: %v", err)
	}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _ = c.call(client) })
	direct := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(client) })
	t.Logf("under WithClient: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct+extra {
		t.Errorf("under a client the function allocates %v times and the internal call %v: want %v more, one for each list it copies", wrapped, direct, extra)
	}

	previous := Default()
	SetDefault(Client{Provider: model})
	t.Cleanup(func() { SetDefault(previous) })
	background := context.Background()
	wrappedDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.call(background) })
	directDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(background) })
	if !asksModel {
		t.Logf("on the default client: the function %v allocations, the internal call %v", wrappedDefault, directDefault)
		if wrappedDefault != directDefault+1+extra {
			t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin and %v for the lists", wrappedDefault, directDefault, extra)
		}
		return
	}
	pinned, _ := ops.PinExec(background)
	directPinned := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(pinned) })
	t.Logf("on the default client: the function %v allocations, the internal call %v on a pinned context and %v on none", wrappedDefault, directPinned, directDefault)
	if wrappedDefault != directPinned+1+extra {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin and %v for the lists", wrappedDefault, directPinned, extra)
	}
	if wrappedDefault != directDefault+extra {
		t.Errorf("on the default client the function allocates %v times and the internal call %v on no pin: the pin is paid once by either, so they differ by the %v for the lists", wrappedDefault, directDefault, extra)
	}
}

const (
	// allocCompareBare has no point to convert; allocCompareFull has similarities
	// and differences, which are two lists to copy. Both score "x", which the
	// calls that name that aspect need.
	allocCompareBare = `{"similarity_score":0.8,"similarities":[],"differences":[],"verdict":"similar","aspect_scores":{"x":0.5}}`
	allocCompareFull = `{"similarity_score":0.8,"similarities":[{"aspect":"a","description":"d"},{"aspect":"b","description":"e"}],` +
		`"differences":[{"aspect":"c","description":"f","severity":"minor"},{"aspect":"d","description":"g","severity":"major"},{"aspect":"e","description":"h","severity":"minor"}],` +
		`"verdict":"similar","aspect_scores":{"x":0.5}}`
)

func TestMergeCompareOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeCompareOptions)
}

func TestCompareAllocatesAsTheInternalCallWhenThereIsNoPointToConvert(t *testing.T) {
	cases := map[string]CompareOptions{
		"no options":           {},
		"one options value":    {Aspects: []string{"x"}, FocusOn: "differences", Depth: 7, OmitSimilarity: true},
		"every common setting": {Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocCompareBare,
				call: func(ctx context.Context) error {
					_, err := Compare(ctx, "a", "b", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Compare("a", "b", compareInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestCompareWithPointsAllocatesOneMoreThanTheInternalCallForEachListItCopies(t *testing.T) {
	o := CompareOptions{Aspects: []string{"x"}}
	// The two lists, similarities and differences, are each copied in one
	// allocation however many points they hold: ComparisonPoint is the root's own
	// type and the internal one cannot be handed on in its place.
	requireAllocsPlusConversions(t, allocCase{
		reply: allocCompareFull,
		call: func(ctx context.Context) error {
			_, err := Compare(ctx, "a", "b", o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Compare("a", "b", compareInternal(ctx, o))
			return err
		},
	}, 2, true)
}

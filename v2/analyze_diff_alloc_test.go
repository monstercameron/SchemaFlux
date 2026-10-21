package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// allocDiffAccount is the value the allocation tests compare: a field that
// changes, one that does not and one JSON leaves out.
type allocDiffAccount struct {
	Name   string `json:"name"`
	Seats  int    `json:"seats"`
	Secret string `json:"-"`
}

// allocDiffWider has a field allocDiffAccount lacks, so that comparing the two
// through an interface turns up an added field and no modified one.
type allocDiffWider struct {
	Name   string `json:"name"`
	Seats  int    `json:"seats"`
	Region string `json:"region"`
}

func TestMergeDiffOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeDiffOptions)
}

func TestDiffAllocatesAsTheInternalCallWhenNoModifiedFieldIsReturned(t *testing.T) {
	var narrow, wide any = allocDiffAccount{Name: "a", Seats: 1}, allocDiffWider{Name: "a", Seats: 1, Region: "eu"}
	same := allocDiffAccount{Name: "a", Seats: 1}
	// Neither of these asks a model, so the internal call never pins the client.
	for _, tc := range []struct {
		name     string
		call     func(ctx context.Context) error
		internal func(ctx context.Context) error
	}{
		{"identical values",
			func(ctx context.Context) error { _, err := Diff(ctx, same, same); return err },
			func(ctx context.Context) error {
				_, err := ops.Diff(same, same, diffInternal(ctx, DiffOptions{}))
				return err
			}},
		{"an added field at the fast tier",
			func(ctx context.Context) error { _, err := Diff(ctx, narrow, wide); return err },
			func(ctx context.Context) error {
				_, err := ops.Diff(narrow, wide, diffInternal(ctx, DiffOptions{}))
				return err
			}},
	} {
		t.Run(tc.name+", which asks no model", func(t *testing.T) {
			requireAllocsPlusConversions(t, allocCase{reply: "unused", call: tc.call, internal: tc.internal}, 0, false)
		})
	}
	t.Run("an added field at the smart tier, which asks a model", func(t *testing.T) {
		o := DiffOptions{Options: allocEveryCommonSetting}
		requireAllocsLikeTheInternalCall(t, allocCase{reply: "The region was added.",
			call:     func(ctx context.Context) error { _, err := Diff(ctx, narrow, wide, o); return err },
			internal: func(ctx context.Context) error { _, err := ops.Diff(narrow, wide, diffInternal(ctx, o)); return err }})
	})
}

func TestDiffWithAModifiedFieldAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	before, after := allocDiffAccount{Name: "a", Seats: 1}, allocDiffAccount{Name: "b", Seats: 2}
	o := DiffOptions{Background: "billing", IgnoreFields: []string{"Nope"}, Options: allocEveryCommonSetting}
	// The function is one more, and not more: Modified is converted into the
	// root's own DiffChange list in one allocation, however many fields changed.
	// What the function does before the call (reading the two values' types to
	// refuse what cannot be compared), and ending the call, allocate nothing.
	requireAllocsPlusConversions(t, allocCase{
		reply: "The name and the seats changed.",
		call:  func(ctx context.Context) error { _, err := Diff(ctx, before, after, o); return err },
		internal: func(ctx context.Context) error {
			_, err := ops.Diff(before, after, diffInternal(ctx, o))
			return err
		},
	}, 1, true)
}

func TestDiffWithAChangeThatIsNotSentAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	before, after := allocDiffAccount{Secret: "a"}, allocDiffAccount{Secret: "b"}
	// The change is in a field JSON leaves out: no model is asked, and Modified
	// still holds the change and is converted.
	requireAllocsPlusConversions(t, allocCase{
		reply: "unused",
		call:  func(ctx context.Context) error { _, err := Diff(ctx, before, after); return err },
		internal: func(ctx context.Context) error {
			_, err := ops.Diff(before, after, diffInternal(ctx, DiffOptions{}))
			return err
		},
	}, 1, false)
}

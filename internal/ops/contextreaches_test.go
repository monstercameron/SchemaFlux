package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Found while wiring A-013: every options type embeds both CommonOptions and
// types.OpOptions, the fluent builders' .Context(ctx) sets the CommonOptions
// one, and the operations read opts.OpOptions.Context directly. So a caller
// could hand Choose, Filter, or Sort a cancellable context and watch it be
// ignored — cancellation reached Extract and silently did nothing for the
// collection operations.
//
// These assert on cancellation rather than on a context value, because
// cancellation is the property a caller actually loses.

func alreadyCancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestResolvedContextPrefersTheCommonOptionsSide(t *testing.T) {
	type key struct{}

	common := context.WithValue(context.Background(), key{}, "common")
	embedded := context.WithValue(context.Background(), key{}, "embedded")

	got := resolvedContext(CommonOptions{Context: common}, types.OpOptions{Context: embedded})
	if got.Value(key{}) != "common" {
		t.Errorf("the CommonOptions context lost to the embedded one")
	}

	got = resolvedContext(CommonOptions{}, types.OpOptions{Context: embedded})
	if got.Value(key{}) != "embedded" {
		t.Errorf("the embedded context was ignored when CommonOptions had none")
	}

	if resolvedContext(CommonOptions{}, types.OpOptions{}) == nil {
		t.Error("resolvedContext returned nil rather than a background context")
	}
}

// resolvedContext must not mint request identifiers as a side effect the way
// toOpOptions does: resolving a context is not an act of identification.
func TestResolvedContextDoesNotMintIdentifiers(t *testing.T) {
	common := CommonOptions{}
	embedded := types.OpOptions{}

	resolvedContext(common, embedded)

	if common.RequestID != "" || embedded.RequestID != "" {
		t.Error("resolving a context assigned a request ID")
	}
}

// The regression itself, one operation per collection entry point.
func TestACancelledContextReachesTheCollectionOperations(t *testing.T) {
	items := []map[string]any{{"id": 1}, {"id": 2}, {"id": 3}}

	cases := []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"choose", func(ctx context.Context) error {
			opts := NewChooseOptions()
			opts.CommonOptions.Context = ctx
			opts.Criteria = []string{"anything"}
			_, err := Choose(items, opts)
			return err
		}},
		{"filter", func(ctx context.Context) error {
			opts := NewFilterOptions()
			opts.CommonOptions.Context = ctx
			opts.Criteria = "anything"
			_, err := Filter(items, opts)
			return err
		}},
		{"sort", func(ctx context.Context) error {
			opts := NewSortOptions()
			opts.CommonOptions.Context = ctx
			opts.Criteria = "anything"
			_, err := Sort(items, opts)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A caller that never returns, so the only way the operation can
			// finish is by honouring the cancellation.
			setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			})
			t.Cleanup(func() { setLLMCaller(nil) })

			err := tc.run(alreadyCancelled())
			if err == nil {
				t.Fatalf("%s ignored a cancelled context and reported success", tc.name)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s failed with %v, which is not the cancellation", tc.name, err)
			}
		})
	}
}

// And the same for a text operation, which reads its context the same way.
func TestACancelledContextReachesTheTextOperations(t *testing.T) {
	setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	opts := NewSummarizeOptions()
	opts.CommonOptions.Context = alreadyCancelled()

	if _, err := Summarize("some text to summarise", opts); err == nil {
		t.Fatal("Summarize ignored a cancelled context")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("Summarize failed with %v, which is not the cancellation", err)
	}
}

// The embedded side still works, so the fix did not swap one ignored field for
// another.
func TestTheEmbeddedContextStillReachesTheOperations(t *testing.T) {
	setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	opts := NewFilterOptions()
	opts.OpOptions.Context = alreadyCancelled()
	opts.Criteria = "anything"

	if _, err := Filter([]map[string]any{{"id": 1}, {"id": 2}}, opts); err == nil {
		t.Fatal("the embedded context stopped being honoured")
	}
}

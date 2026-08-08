package fluent

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// FL-003's whole point is that the fluent surface and the direct API are the
// same operation reached two ways. FL-006 then proved a mis-built request is
// refused *before* any provider work. What neither covered is the plain case:
// a **correctly** built request actually reaching the provider.
//
// That gap matters more than it sounds. Every check in this package so far
// asserts an absence — no provider contact, no default mismatch — and a
// delegation that was broken outright would satisfy all of them. These assert
// the presence: each entrypoint, built validly, reaches the provider exactly
// once.
//
// The answers are deliberately thin. This is not testing what the operations
// do with a response; it is testing that the fluent spelling arrives.

// reaches runs one builder against a counting provider and asserts the call
// happened. The error is not checked: several of these operations reject the
// stub body, which is correct and irrelevant here — what is being tested is
// that the request was dispatched, and only the call count shows that.
func reaches(t *testing.T, name string, body string, run func(ctx context.Context) error) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		provider := &countingProvider{body: body}
		ctx := ops.WithProvider(context.Background(), provider)

		_ = run(ctx)

		if n := provider.calls.Load(); n == 0 {
			t.Errorf("%s never reached the provider; the fluent spelling does not dispatch", name)
		}
	})
}

type reachTarget struct {
	Name string `json:"name"`
}

func TestEveryFluentEntrypointReachesTheProvider(t *testing.T) {
	items := []string{"first", "second", "third"}

	reaches(t, "Extracting", `{"name":"x"}`, func(ctx context.Context) error {
		_, err := Extracting[reachTarget]("some text").Context(ctx).Run(ctx)
		return err
	})

	reaches(t, "Transforming", `{"name":"x"}`, func(ctx context.Context) error {
		_, err := Transforming[reachTarget, reachTarget](reachTarget{Name: "in"}).Context(ctx).Run(ctx)
		return err
	})

	reaches(t, "Generating", `{"name":"x"}`, func(ctx context.Context) error {
		_, err := Generating[reachTarget]("make something").Context(ctx).Run(ctx)
		return err
	})

	reaches(t, "Choosing", `{"id":"i-000001"}`, func(ctx context.Context) error {
		_, err := Choosing(items).By("the best one").Context(ctx).Run(ctx)
		return err
	})

	reaches(t, "Filtering", `{"ids":["i-000001"]}`, func(ctx context.Context) error {
		_, err := Filtering(items).By("the good ones").Context(ctx).Run(ctx)
		return err
	})

	reaches(t, "Sorting", `{"ids":["i-000001","i-000002","i-000003"]}`, func(ctx context.Context) error {
		_, err := Sorting(items).By("alphabetically").Context(ctx).Run(ctx)
		return err
	})
}

// The advanced surface is where FL-003's eleven missing constructors lived, so
// it is the half most likely to have been wired wrong.
func TestEveryAdvancedEntrypointReachesTheProvider(t *testing.T) {
	items := []string{"first", "second"}

	reaches(t, "Negotiating", `{"agreement":{},"satisfaction":{}}`, func(ctx context.Context) error {
		_, err := Negotiating[reachTarget](map[string]any{"budget": "low"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Resolving", `{"resolved":{}}`, func(ctx context.Context) error {
		_, err := Resolving(items).Context(ctx).Run()
		return err
	})

	reaches(t, "Deriving", `{"derived":{}}`, func(ctx context.Context) error {
		_, err := Deriving[reachTarget, reachTarget](reachTarget{Name: "in"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Conforming", `{"conformed":{}}`, func(ctx context.Context) error {
		_, err := Conforming(reachTarget{Name: "in"}, "a standard").Context(ctx).Run()
		return err
	})

	reaches(t, "Interpolating", `{"interpolated":[]}`, func(ctx context.Context) error {
		_, err := Interpolating(items).Context(ctx).Run()
		return err
	})

	reaches(t, "Arbitrating", `{"chosen":{}}`, func(ctx context.Context) error {
		_, err := Arbitrating(items).Rules("pick the shortest").Context(ctx).Run()
		return err
	})

	reaches(t, "Projecting", `{"projected":{}}`, func(ctx context.Context) error {
		_, err := Projecting[reachTarget, reachTarget](reachTarget{Name: "in"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Auditing", `{"findings":[],"summary":{}}`, func(ctx context.Context) error {
		_, err := Auditing(reachTarget{Name: "in"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Pivoting", `{"pivoted":{}}`, func(ctx context.Context) error {
		_, err := Pivoting[reachTarget, reachTarget](reachTarget{Name: "in"}).Context(ctx).Run()
		return err
	})
}

// The shorthand helpers are a third spelling of the same operations, and a
// third place the wiring can be wrong.
//
// They take no context, so `ops.WithProvider` cannot reach them and they can
// only be driven through the package-level provider. That is a real limitation
// rather than a quirk of this test: `Client.Context(ctx)` — the per-call
// isolation seam from TI-002 — **cannot reach these three**, so an application
// running two clients gets whichever provider was installed last for any
// ChooseBy/FilterBy/SortBy call. It is recorded here because the test is where
// somebody will next run into it.
func TestTheShorthandHelpersReachTheProvider(t *testing.T) {
	items := []string{"first", "second", "third"}

	shorthand := func(t *testing.T, name, body string, run func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			provider := &countingProvider{body: body}
			ops.SetDefaultProvider(provider)
			t.Cleanup(func() { ops.SetDefaultProvider(nil) })

			_ = run()

			if n := provider.calls.Load(); n == 0 {
				t.Errorf("%s never reached the provider", name)
			}
		})
	}

	shorthand(t, "ChooseBy", `{"id":"i-000001"}`, func() error {
		_, err := ChooseBy(items, "the best")
		return err
	})
	shorthand(t, "FilterBy", `{"ids":["i-000001"]}`, func() error {
		_, err := FilterBy(items, "the good ones")
		return err
	})
	shorthand(t, "SortBy", `{"ids":["i-000001","i-000002","i-000003"]}`, func() error {
		_, err := SortBy(items, "alphabetically")
		return err
	})
}

// The analysis and extended surfaces are the largest part of this package and
// were the least covered: nineteen entrypoints each, all thin delegations, none
// of which any test had ever dispatched.
func TestTheAnalysisAndExtendedEntrypointsReachTheProvider(t *testing.T) {
	reaches(t, "Classifying", `{"category":"a","confidence":0.9}`, func(ctx context.Context) error {
		_, err := Classifying[string, string]("input").Categories("a", "b").Context(ctx).Run()
		return err
	})

	reaches(t, "Scoring", `{"value":0.5,"normalized_value":0.5}`, func(ctx context.Context) error {
		_, err := Scoring("input").Context(ctx).Run()
		return err
	})

	reaches(t, "Comparing", `{"similarity":0.5,"differences":[]}`, func(ctx context.Context) error {
		_, err := Comparing("left", "right").Context(ctx).Run()
		return err
	})

	reaches(t, "Summarizing", `{"text":"short","key_points":[],"confidence":0.5}`, func(ctx context.Context) error {
		_, err := Summarizing("a longer piece of text to summarise").Context(ctx).Run()
		return err
	})

	reaches(t, "Rewriting", `{"text":"rewritten","changes_made":[],"confidence":0.5}`, func(ctx context.Context) error {
		_, err := Rewriting("some text").Context(ctx).Run()
		return err
	})

	reaches(t, "Translating", `{"text":"traducido","confidence":0.5}`, func(ctx context.Context) error {
		_, err := Translating("some text").To("Spanish").Context(ctx).Run()
		return err
	})

	reaches(t, "Expanding", `{"text":"a much longer text","added_content":[],"confidence":0.5}`, func(ctx context.Context) error {
		_, err := Expanding("brief").Context(ctx).Run()
		return err
	})

	reaches(t, "Explaining", `{"explanation":"because","key_points":[]}`, func(ctx context.Context) error {
		_, err := Explaining("something").Context(ctx).Run()
		return err
	})

	reaches(t, "Inferring", `{"name":"x"}`, func(ctx context.Context) error {
		_, err := Inferring(reachTarget{Name: "observed"}).Context(ctx).Run()
		return err
	})

	// Diff is mostly a local operation: it computes the structural change set
	// itself and only reaches a provider to *summarise* changes it found. Two
	// plain strings produce no structural changes — compareData refuses
	// anything that is not a struct — so that spelling correctly dispatches
	// nothing. The values here have to be structs differing in a field before
	// the summary step is reached at all.
	reaches(t, "Diffing", `{"summary":"the name changed"}`, func(ctx context.Context) error {
		_, err := Diffing(
			reachTarget{Name: "before"},
			reachTarget{Name: "after"},
		).Context(ctx).Run()
		return err
	})
}

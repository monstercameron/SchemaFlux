package schemaflux_test

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Choose returns one of the options it was given, never a copy of one. The model
// is scripted here, so the example runs offline.
func ExampleChoose() {
	var model schemafluxtest.Provider
	model.Reply(`{"id":"i-000002"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	laptops := []string{"A: 1.9 kg, $1200", "B: 1.2 kg, $1400", "C: 2.4 kg, $900"}
	best, err := schemaflux.Choose(ctx, laptops, schemaflux.ChooseOptions{Criteria: []string{"under $1500", "lightest"}})
	fmt.Println(best, err)
	// Output: B: 1.2 kg, $1400 <nil>
}

// An answer that names an option that was not offered is an error of kind
// ErrInvalidAnswer.
func ExampleChoose_invalidAnswer() {
	var model schemafluxtest.Provider
	model.Reply(`{"id":"i-000009"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	_, err := schemaflux.Choose(ctx, []string{"a", "b"})
	fmt.Println(errors.Is(err, schemaflux.ErrInvalidAnswer))
	// Output: true
}

// Filter returns the items that meet the criteria, in their original order.
func ExampleFilter() {
	var model schemafluxtest.Provider
	model.Reply(`{"ids":["i-000003","i-000001"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	tasks := []string{"write the report", "book the venue", "send the invoice", "water the plants"}
	open, err := schemaflux.Filter(ctx, tasks, "not yet completed")
	fmt.Println(open, err)
	// Output: [write the report send the invoice] <nil>
}

// Exclude keeps the items that do not meet the criteria.
func ExampleFilter_exclude() {
	var model schemafluxtest.Provider
	model.Reply(`{"ids":["i-000002"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	emails := []string{"weekly digest", "invoice 4411", "newsletter"}
	rest, err := schemaflux.Filter(ctx, emails, "is a newsletter", schemaflux.FilterOptions{Exclude: true})
	fmt.Println(rest, err)
	// Output: [invoice 4411] <nil>
}

// Sort returns a permutation of the items it was given.
func ExampleSort() {
	var model schemafluxtest.Provider
	model.Reply(`{"ids":["i-000003","i-000001","i-000002"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	issues := []string{"typo in footer", "login page is down", "slow search"}
	ranked, err := schemaflux.Sort(ctx, issues, "most urgent first")
	fmt.Println(ranked, err)
	// Output: [slow search typo in footer login page is down] <nil>
}

// The Record of a Sort says which path ran.
func ExampleSort_strategy() {
	var model schemafluxtest.Provider
	model.Reply(`{"ids":["i-000002","i-000001"]}`)
	var recorder schemaflux.Recorder
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Recorder: &recorder})

	_, err := schemaflux.Sort(ctx, []string{"a", "b"}, "alphabetical, descending")
	records := recorder.Records()
	fmt.Println(err, records[0].Op, records[0].Strategy)
	// Output: <nil> sort whole-list
}

// Rank scores every item against a query and returns them best first, ordered
// here by the scores.
func ExampleRank() {
	var model schemafluxtest.Provider
	model.Reply(`{"rankings":[{"index":0,"score":0.4},{"index":1,"score":0.95},{"index":2,"score":0.1}]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	docs := []string{"Tuning autovacuum", "Connection pooling with pgbouncer", "A history of SQL"}
	res, err := schemaflux.Rank(ctx, docs, "postgres connection pooling", schemaflux.RankOptions{TopK: 2})
	for _, item := range res.Items {
		fmt.Println(item.Rank, item.Score, item.Item)
	}
	fmt.Println(res.ReturnedItems, "of", res.TotalItems, err)
	// Output:
	// 1 0.95 Connection pooling with pgbouncer
	// 2 0.4 Tuning autovacuum
	// 2 of 3 <nil>
}

// SemanticMatch pairs items of one list with items of another by meaning, and
// says which items were left without a pair.
func ExampleSemanticMatch() {
	var model schemafluxtest.Provider
	model.Reply(`{"matches":[{"source_index":0,"target_index":1,"score":0.9,"explanation":"same role","is_partial":false}],"unmatched_sources":[1],"unmatched_targets":[0]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	resumes := []string{"ten years of Go", "a chef"}
	jobs := []string{"line cook", "backend engineer"}
	res, err := schemaflux.SemanticMatch(ctx, resumes, jobs)
	for _, m := range res.Matches {
		fmt.Println(m.Source, "->", m.Target, m.Score)
	}
	fmt.Println(res.UnmatchedSources, res.UnmatchedTargets, err)
	// Output:
	// ten years of Go -> backend engineer 0.9
	// [1] [0] <nil>
}

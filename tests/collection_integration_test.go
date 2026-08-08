package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

type lineItem struct {
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	UnitCost float64 `json:"unit_cost"`
}

func invoiceLines() []lineItem {
	return []lineItem{
		{SKU: "A-100", Name: "Widget", UnitCost: 12.50},
		{SKU: "A-200", Name: "Gadget", UnitCost: 87.00},
		{SKU: "A-300", Name: "Doohickey", UnitCost: 4.25},
	}
}

func encode(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// OP-101: Choose no longer asks the model to echo the option back, so there
// is no field for it to alter. The model answers with an id assigned at
// encode time, and what these cases exercise is that an id naming something
// that was never assigned -- including a line's own SKU, a plausible mistake
// for a model that forgets the protocol -- is refused.
func TestIntegrationChooseRefusesAnAlteredEcho(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"domain_value_not_a_protocol_id", `{"id":"A-200"}`},
		{"unassigned_protocol_id", `{"id":"i-000009"}`},
		{"zero_based_off_by_one", `{"id":"i-000000"}`},
		{"empty", `{"id":""}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, nil)

			chosen, err := schemaflux.Choose(invoiceLines(),
				schemaflux.NewChooseOptions().WithSteering("the most expensive line"))
			if err == nil {
				t.Fatalf("an id that was not offered must be refused; got %+v", chosen)
			}
			if chosen.SKU != "" {
				t.Errorf("a refused choice must return nothing, got %+v", chosen)
			}
		})
	}
}

// A faithful answer is accepted, and the value returned is the caller's own
// -- named by id, never reproduced by the model.
func TestIntegrationChooseReturnsTheCallersRecord(t *testing.T) {
	lines := invoiceLines()
	testfixtures.WithScriptedProvider(t, `{"id":"i-000002"}`, nil)

	chosen, err := schemaflux.Choose(lines, schemaflux.NewChooseOptions().WithSteering("most expensive"))
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if chosen != lines[1] {
		t.Errorf("chosen = %+v, want the input's own %+v", chosen, lines[1])
	}
}

// Filter must return a subset of what it was given, never an authored list.
// OP-101: the model answers with the ids to keep, so what is left to test is
// that an id that was never assigned, or one repeated, is refused.
func TestIntegrationFilterReturnsASubset(t *testing.T) {
	lines := invoiceLines()

	t.Run("faithful_subset", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"ids":["i-000003","i-000001"]}`, nil)

		kept, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("under $20"))
		if err != nil {
			t.Fatalf("Filter: %v", err)
		}
		if len(kept) != 2 || kept[0] != lines[0] || kept[1] != lines[2] {
			t.Errorf("kept = %+v, want the input's own items in input order", kept)
		}
	})

	t.Run("unassigned_id_is_refused", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"ids":["i-000009"]}`, nil)

		kept, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("under $20"))
		if err == nil {
			t.Fatalf("an unassigned id must be refused; got %+v", kept)
		}
	})

	t.Run("domain_value_is_refused", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"ids":["A-300"]}`, nil)

		if _, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("cheap")); err == nil {
			t.Fatal("a domain value standing in for an id must be refused")
		}
	})

	t.Run("longer_than_input_is_refused", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"ids":["i-000001","i-000002","i-000003","i-000001"]}`, nil)

		if _, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("all")); err == nil {
			t.Fatal("a subset larger than the set must be refused")
		}
	})
}

// Example_chooseRefusesAnAlteredRecord shows the guarantee that matters: what
// comes back is the record you supplied, or an error. The model answers with
// an id, and an id naming something that was never offered does not get to
// pass as your data. It runs under go test with a scripted provider: no
// credential, no spend.
func Example_chooseRefusesAnAlteredRecord() {
	lines := []lineItem{
		{SKU: "A-100", Name: "Widget", UnitCost: 12.50},
		{SKU: "A-200", Name: "Gadget", UnitCost: 87.00},
	}

	// The model answers with an id it was never assigned.
	schemaflux.NewClient("example-key").WithProviderInstance(
		testfixtures.NewScripted(`{"id":"i-000009"}`))

	chosen, err := schemaflux.Choose(lines,
		schemaflux.NewChooseOptions().WithSteering("the most expensive line"))

	fmt.Println("error is nil:", err == nil)
	fmt.Println("returned SKU:", chosen.SKU == "")

	// Output:
	// error is nil: false
	// returned SKU: true
}

// CF-04 / OP-109: a collection too large to echo back within the output budget
// used to be refused. Filter splits it instead, because the merge for a filter
// is a concatenation — each item's fate depends only on the criteria.
//
// OP-101 changed what "too large" means: the answer is now a list of ids, one
// per item, and an id costs the same handful of bytes regardless of item
// content -- so the boundary is item *count* against a fixed per-id cost, not
// item size. 200 items with long names used to be enough to force chunking
// under the old echo protocol; 1500 items, of any size, is what it takes now.
func TestIntegrationFilterChunksLargeCollections(t *testing.T) {
	lines := make([]lineItem, 1500)
	for i := range lines {
		lines[i] = lineItem{
			SKU:      fmt.Sprintf("A-%04d", i),
			Name:     fmt.Sprintf("Item %d", i),
			UnitCost: float64(i) + 0.5,
		}
	}

	// Each chunk keeps its first line, whatever the boundaries turn out to be.
	provider := &chunkEchoProvider{}
	schemaflux.NewClient("test-key").WithProviderInstance(provider)

	opts := schemaflux.NewFilterOptions().WithCriteria("anything")
	opts.CommonOptions.Intelligence = schemaflux.Quick

	kept, err := schemaflux.Filter(lines, opts)
	if err != nil {
		t.Fatalf("Filter must chunk rather than refuse: %v", err)
	}
	if provider.calls < 2 {
		t.Fatalf("made %d calls; a 200-item collection should have been split", provider.calls)
	}
	if len(kept) == 0 {
		t.Fatal("chunking produced nothing")
	}
	for i := 1; i < len(kept); i++ {
		if kept[i-1].SKU >= kept[i].SKU {
			t.Fatalf("results are not in input order: %q then %q", kept[i-1].SKU, kept[i].SKU)
		}
	}
}

// Example_mapReduce shows the primitive for the case the library cannot decide
// for you: chunking a Sort needs a merge that knows how to interleave two
// sorted runs, and that is your domain's knowledge, not the library's.
func Example_mapReduce() {
	candidates := []lineItem{
		{SKU: "A-1", Name: "cheap", UnitCost: 1},
		{SKU: "A-2", Name: "mid", UnitCost: 5},
		{SKU: "A-3", Name: "dear", UnitCost: 9},
		{SKU: "A-4", Name: "dearest", UnitCost: 20},
	}

	// Keeps the first line of whatever chunk it is handed, so it is a valid
	// answer for every chunk rather than only the first.
	schemaflux.NewClient("example-key").WithProviderInstance(&chunkEchoProvider{})

	perChunk, summary, err := schemaflux.MapReduce(context.Background(), candidates,
		schemaflux.MapReduceOptions{ChunkSize: 2, Concurrency: 1},
		func(ctx context.Context, chunk []lineItem) ([]lineItem, error) {
			opts := schemaflux.NewFilterOptions().WithCriteria("under $3")
			opts.CommonOptions.Context = ctx
			return schemaflux.Filter(chunk, opts)
		})
	if err != nil {
		fmt.Println("map-reduce failed:", err)
		return
	}

	fmt.Println("chunks:", summary.Chunks)
	fmt.Println("complete:", summary.Complete())
	fmt.Println("results from chunk one:", len(perChunk[0]))

	// Output:
	// chunks: 2
	// complete: true
	// results from chunk one: 1
}

// chunkEchoProvider keeps the id of the first item of whatever chunk it is
// given. OP-101 changed what a chunk's request and answer look like: the
// request tags each item with an id ({"id": "...", "item": ...}), and the
// answer names ids ({"ids": [...]}) rather than echoing items.
type chunkEchoProvider struct {
	calls int
}

func (p *chunkEchoProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.calls++

	start := strings.Index(req.UserPrompt, "[")
	if start < 0 {
		return schemaflux.CompletionResponse{Content: `{"ids":[]}`, FinishReason: "stop"}, nil
	}

	var chunk []struct {
		ID   string   `json:"id"`
		Item lineItem `json:"item"`
	}
	if err := json.Unmarshal([]byte(req.UserPrompt[start:]), &chunk); err != nil || len(chunk) == 0 {
		return schemaflux.CompletionResponse{Content: `{"ids":[]}`, FinishReason: "stop"}, nil
	}

	kept, err := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{IDs: []string{chunk[0].ID}})
	if err != nil {
		return schemaflux.CompletionResponse{Content: `{"ids":[]}`, FinishReason: "stop"}, nil
	}
	return schemaflux.CompletionResponse{Content: string(kept), FinishReason: "stop"}, nil
}

func (p *chunkEchoProvider) Name() string                                      { return "local" }
func (p *chunkEchoProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *chunkEchoProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

// A sort returns a permutation. The check was a count, which is satisfied by a
// model that returned one line twice and dropped another — and that corrupts a
// result quietly, where a short answer at least breaks obviously.
//
// Sort falls back to per-item scoring when the whole-list answer is rejected,
// so these cases use a provider that fails the fallback too; what is being
// asserted is that the whole-list answer is not accepted.
func TestIntegrationSortRefusesAResultThatIsNotAPermutation(t *testing.T) {
	lines := invoiceLines()

	cases := []struct {
		name string
		body string
	}{
		{
			"one line duplicated, one dropped — same length",
			encode(t, []lineItem{lines[0], lines[0], lines[2]}),
		},
		{
			"a line edited in place",
			encode(t, []lineItem{lines[1], {SKU: "A-100", Name: "Widget", UnitCost: 12.00}, lines[2]}),
		},
		{
			"an invented line",
			encode(t, []lineItem{lines[1], lines[0], {SKU: "Z-999", Name: "Invented", UnitCost: 1}}),
		},
		{
			"a line dropped",
			encode(t, []lineItem{lines[1], lines[0]}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The scoring fallback asks for {"rank_score": ...}; a body shaped
			// like the sorted list answers neither, so both paths refuse and
			// the operation reports rather than silently degrading.
			testfixtures.WithScriptedProvider(t, tc.body, nil)

			sorted, err := schemaflux.Sort(lines,
				schemaflux.NewSortOptions().WithCriteria("most expensive first"))
			if err == nil {
				t.Fatalf("a result that is not a permutation must be refused; got %+v", sorted)
			}
		})
	}
}

// The faithful case still works, and the values come back as the caller's
// own -- named by id, never reproduced by the model.
func TestIntegrationSortAcceptsAPermutation(t *testing.T) {
	lines := invoiceLines()
	testfixtures.WithScriptedProvider(t, `{"ids":["i-000002","i-000001","i-000003"]}`, nil)

	sorted, err := schemaflux.Sort(lines, schemaflux.NewSortOptions().WithCriteria("most expensive first"))
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	if len(sorted) != 3 || sorted[0] != lines[1] || sorted[1] != lines[0] || sorted[2] != lines[2] {
		t.Errorf("sorted = %+v", sorted)
	}
}

// Sort picks between ordering the whole list in one call and scoring items in
// parallel, and which one ran changes what the answer means. Before SortResult
// was exported that choice was invisible to a caller: two runs of the same
// input could come back ordered by different mechanisms with nothing in the
// result to say so.
func TestIntegrationSortResultReportsItsStrategy(t *testing.T) {
	lines := invoiceLines()
	testfixtures.WithScriptedProvider(t, `{"ids":["i-000002","i-000001","i-000003"]}`, nil)

	result, err := schemaflux.SortResult(lines, schemaflux.NewSortOptions().WithCriteria("most expensive first"))
	if err != nil {
		t.Fatalf("SortResult: %v", err)
	}
	if len(result.Value) != 3 || result.Value[0] != lines[1] {
		t.Errorf("value = %+v", result.Value)
	}
	if result.Meta.Strategy != "whole-list" {
		t.Errorf("Meta.Strategy = %q, want %q for a three-item sort", result.Meta.Strategy, "whole-list")
	}
}

// A sort of a list too short to have an order does not call a provider at all,
// and says so rather than reporting the strategy it would have used.
func TestIntegrationSortResultReportsTheTrivialCase(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"ids":["i-000001"]}`, nil)

	result, err := schemaflux.SortResult([]lineItem{invoiceLines()[0]},
		schemaflux.NewSortOptions().WithCriteria("most expensive first"))
	if err != nil {
		t.Fatalf("SortResult: %v", err)
	}
	if result.Meta.Strategy != "trivial" {
		t.Errorf("Meta.Strategy = %q, want %q for a one-item sort", result.Meta.Strategy, "trivial")
	}
}

// A clustering is a partition. Overlapping groups make a caller iterating the
// clusters process an item twice; missing indices drop items silently; and Size
// used to count the raw indices, so it disagreed with Items exactly when the
// model misbehaved.
func TestIntegrationClusterRequiresAPartition(t *testing.T) {
	lines := invoiceLines()

	cases := []struct {
		name string
		body string
	}{
		{
			"an item in two clusters",
			`{"clusters":[{"name":"a","indices":[0,1]},{"name":"b","indices":[1,2]}],"outlier_indices":[],"quality":0.9}`,
		},
		{
			"an item in no cluster",
			`{"clusters":[{"name":"a","indices":[0]},{"name":"b","indices":[1]}],"outlier_indices":[],"quality":0.9}`,
		},
		{
			"an index past the end",
			`{"clusters":[{"name":"a","indices":[0,1,2,7]}],"outlier_indices":[],"quality":0.9}`,
		},
		{
			"a negative index",
			`{"clusters":[{"name":"a","indices":[0,1,2,-1]}],"outlier_indices":[],"quality":0.9}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, nil)

			result, err := schemaflux.Cluster(lines, schemaflux.NewClusterOptions())
			if err == nil {
				t.Fatalf("a clustering that is not a partition must be refused; got %d clusters", result.NumClusters)
			}
		})
	}
}

// A real partition is accepted, outliers count as placed, and Size agrees with
// Items because it is derived from them.
func TestIntegrationClusterAcceptsAPartitionAndSizeAgrees(t *testing.T) {
	lines := invoiceLines()
	testfixtures.WithScriptedProvider(t,
		`{"clusters":[{"name":"cheap","indices":[0,2]}],"outlier_indices":[1],"quality":0.8}`, nil)

	result, err := schemaflux.Cluster(lines, schemaflux.NewClusterOptions())
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if result.NumClusters != 1 {
		t.Fatalf("NumClusters = %d, want 1", result.NumClusters)
	}
	cluster := result.Clusters[0]
	if cluster.Size != len(cluster.Items) {
		t.Errorf("Size = %d but Items has %d; the two must agree", cluster.Size, len(cluster.Items))
	}
	if len(cluster.Items) != 2 {
		t.Errorf("Items = %+v, want two", cluster.Items)
	}
	if len(result.Outliers) != 1 || result.Outliers[0] != lines[1] {
		t.Errorf("Outliers = %+v, want the caller's own second line", result.Outliers)
	}
}

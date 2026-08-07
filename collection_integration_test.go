package schemaflux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
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

// The failure this guards against is not an invented record — it is an echoed
// one with a changed number. At the public API a caller has no way to notice.
func TestIntegrationChooseRefusesAnAlteredEcho(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"cost_changed", `{"sku":"A-200","name":"Gadget","unit_cost":78.00}`},
		{"sku_changed", `{"sku":"A-999","name":"Gadget","unit_cost":87.00}`},
		{"name_changed", `{"sku":"A-200","name":"Gadget Pro","unit_cost":87.00}`},
		{"cost_rounded", `{"sku":"A-100","name":"Widget","unit_cost":12.00}`},
		{"invented", `{"sku":"B-100","name":"Nothing","unit_cost":0.01}`},
		{"empty", `{"sku":"","name":"","unit_cost":0}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, tc.body, nil)

			chosen, err := schemaflux.Choose(invoiceLines(),
				schemaflux.NewChooseOptions().WithSteering("the most expensive line"))
			if err == nil {
				t.Fatalf("an altered echo must be refused; got %+v", chosen)
			}
			if chosen.SKU != "" {
				t.Errorf("a refused choice must return nothing, got %+v", chosen)
			}
		})
	}
}

// A faithful echo is accepted, and the value returned is the caller's own.
func TestIntegrationChooseReturnsTheCallersRecord(t *testing.T) {
	lines := invoiceLines()
	withScriptedProvider(t, encode(t, lines[1]), nil)

	chosen, err := schemaflux.Choose(lines, schemaflux.NewChooseOptions().WithSteering("most expensive"))
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if chosen != lines[1] {
		t.Errorf("chosen = %+v, want the input's own %+v", chosen, lines[1])
	}
}

// Filter must return a subset of what it was given, never an authored list.
func TestIntegrationFilterReturnsASubset(t *testing.T) {
	lines := invoiceLines()

	t.Run("faithful_subset", func(t *testing.T) {
		withScriptedProvider(t, encode(t, []lineItem{lines[2], lines[0]}), nil)

		kept, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("under $20"))
		if err != nil {
			t.Fatalf("Filter: %v", err)
		}
		if len(kept) != 2 || kept[0] != lines[0] || kept[1] != lines[2] {
			t.Errorf("kept = %+v, want the input's own items in input order", kept)
		}
	})

	t.Run("edited_item_is_refused", func(t *testing.T) {
		withScriptedProvider(t, `[{"sku":"A-300","name":"Doohickey","unit_cost":3.25}]`, nil)

		kept, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("under $20"))
		if err == nil {
			t.Fatalf("an edited item must be refused; got %+v", kept)
		}
	})

	t.Run("invented_item_is_refused", func(t *testing.T) {
		withScriptedProvider(t, `[{"sku":"Z-000","name":"Phantom","unit_cost":1.00}]`, nil)

		if _, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("cheap")); err == nil {
			t.Fatal("an invented item must be refused")
		}
	})

	t.Run("longer_than_input_is_refused", func(t *testing.T) {
		tooMany := append(append([]lineItem{}, lines...), lines[0])
		withScriptedProvider(t, encode(t, tooMany), nil)

		if _, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("all")); err == nil {
			t.Fatal("a subset larger than the set must be refused")
		}
	})
}

// Example_chooseRefusesAnAlteredRecord shows the guarantee that matters: what
// comes back is the record you supplied, or an error. A model that echoes an
// option with a changed price does not get to pass it off as your data. It runs
// under go test with a scripted provider: no credential, no spend.
func Example_chooseRefusesAnAlteredRecord() {
	lines := []lineItem{
		{SKU: "A-100", Name: "Widget", UnitCost: 12.50},
		{SKU: "A-200", Name: "Gadget", UnitCost: 87.00},
	}

	// The model returns the right product at the wrong price.
	schemaflux.NewClient("example-key").WithProviderInstance(&scriptedProvider{
		body: `{"sku":"A-200","name":"Gadget","unit_cost":78.00}`,
	})

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
func TestIntegrationFilterChunksLargeCollections(t *testing.T) {
	lines := make([]lineItem, 200)
	for i := range lines {
		lines[i] = lineItem{
			SKU:      fmt.Sprintf("A-%04d", i),
			Name:     fmt.Sprintf("Item %d with a name long enough to be realistic", i),
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

// chunkEchoProvider returns the first item of whatever chunk it is given.
type chunkEchoProvider struct {
	calls int
}

func (p *chunkEchoProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.calls++

	start := strings.Index(req.UserPrompt, "[")
	if start < 0 {
		return schemaflux.CompletionResponse{Content: "[]", FinishReason: "stop"}, nil
	}

	var chunk []lineItem
	if err := json.Unmarshal([]byte(req.UserPrompt[start:]), &chunk); err != nil || len(chunk) == 0 {
		return schemaflux.CompletionResponse{Content: "[]", FinishReason: "stop"}, nil
	}

	kept, err := json.Marshal(chunk[:1])
	if err != nil {
		return schemaflux.CompletionResponse{Content: "[]", FinishReason: "stop"}, nil
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
			withScriptedProvider(t, tc.body, nil)

			sorted, err := schemaflux.Sort(lines,
				schemaflux.NewSortOptions().WithCriteria("most expensive first"))
			if err == nil {
				t.Fatalf("a result that is not a permutation must be refused; got %+v", sorted)
			}
		})
	}
}

// The faithful case still works, and the values come back as the caller's own.
func TestIntegrationSortAcceptsAPermutation(t *testing.T) {
	lines := invoiceLines()
	withScriptedProvider(t, encode(t, []lineItem{lines[1], lines[0], lines[2]}), nil)

	sorted, err := schemaflux.Sort(lines, schemaflux.NewSortOptions().WithCriteria("most expensive first"))
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	if len(sorted) != 3 || sorted[0] != lines[1] || sorted[1] != lines[0] || sorted[2] != lines[2] {
		t.Errorf("sorted = %+v", sorted)
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
			withScriptedProvider(t, tc.body, nil)

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
	withScriptedProvider(t,
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

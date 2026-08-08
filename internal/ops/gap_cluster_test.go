package ops

import (
	"strings"
	"testing"
)

// gap_cluster_test.go exercises Cluster's actual behaviour rather than its
// plumbing: the partition contract (CoversExactlyOnce), the option-validation
// refusals, and the paths callLLM's caller never reached before (empty input,
// a single item, a marshal failure, and a malformed or off-topic response).
//
// It deliberately does not assert on the prompt text -- only on what the
// function returns for a given response body.

// unmarshalableClusterItem carries a channel field, which encoding/json
// refuses to marshal. It exists to drive Cluster's own marshal-error path
// (cluster.go, the per-item json.Marshal check before building indexedItems),
// which nothing exercised before this file: every existing example uses a
// plain string or struct of JSON-safe fields.
type unmarshalableClusterItem struct {
	Ch chan int `json:"ch"`
}

// TestCluster_EmptyItemsReturnsZeroResultNoCall checks the len==0 early
// return. No LLM caller is installed at all, so if Cluster reached callLLM
// anyway this would panic (nil customLLMCaller, no defaultProvider) instead
// of quietly returning the wrong thing.
func TestCluster_EmptyItemsReturnsZeroResultNoCall(t *testing.T) {
	result, err := Cluster([]string{}, NewClusterOptions())
	if err != nil {
		t.Fatalf("Cluster on empty input returned an error: %v", err)
	}
	if result.TotalItems != 0 || len(result.Clusters) != 0 || result.NumClusters != 0 {
		t.Fatalf("expected a zero-value result for empty input, got %+v", result)
	}
}

func TestCluster_SingleItemFormsItsOwnClusterWithoutACall(t *testing.T) {
	result, err := Cluster([]string{"lonely"}, NewClusterOptions())
	if err != nil {
		t.Fatalf("Cluster on a single item returned an error: %v", err)
	}
	if result.NumClusters != 1 || len(result.Clusters) != 1 {
		t.Fatalf("expected exactly one cluster, got %+v", result)
	}
	if len(result.Clusters[0].Items) != 1 || result.Clusters[0].Items[0] != "lonely" {
		t.Fatalf("the single item was not preserved: %+v", result.Clusters[0])
	}
	if result.Clusters[0].Size != 1 {
		t.Fatalf("Size = %d, want 1", result.Clusters[0].Size)
	}
}

func TestCluster_InvalidOptionsAreRefusedBeforeAnyCall(t *testing.T) {
	cases := []struct {
		name string
		opts ClusterOptions
	}{
		{"negative num clusters", NewClusterOptions().WithNumClusters(-1)},
		{"zero min cluster size", func() ClusterOptions { o := NewClusterOptions(); o.MinClusterSize = 0; return o }()},
		{"threshold above one", NewClusterOptions().WithSimilarityThreshold(1.5)},
		{"threshold below zero", NewClusterOptions().WithSimilarityThreshold(-0.1)},
		{"invalid naming strategy", NewClusterOptions().WithNamingStrategy("weird")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No LLM caller installed at all: if Cluster reached callLLM despite
			// the invalid options, this would panic on a nil customLLMCaller and
			// no defaultProvider, which is its own proof the validation ran first.
			_, err := Cluster([]string{"a", "b"}, tc.opts)
			if err == nil {
				t.Fatalf("expected %s to be refused by Validate", tc.name)
			}
		})
	}
}

func TestCluster_MarshalErrorOnAnUnmarshalableItem(t *testing.T) {
	items := []unmarshalableClusterItem{{Ch: make(chan int)}, {Ch: make(chan int)}}
	_, err := Cluster(items, NewClusterOptions())
	if err == nil {
		t.Fatal("expected a marshal error for a channel-bearing item")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("error does not name the marshal failure: %v", err)
	}
}

func TestCluster_HappyPathBuildsClustersAndOutliersFromIndices(t *testing.T) {
	withResponse(t, `{
		"clusters": [
			{"name": "Fruits", "description": "fruit items", "indices": [0, 2], "keywords": ["fruit"]},
			{"name": "Veggies", "description": "vegetable items", "indices": [1], "keywords": ["veg"]}
		],
		"outlier_indices": [3],
		"quality": 0.82
	}`)

	items := []string{"apple", "carrot", "banana", "wrench"}
	result, err := Cluster(items, NewClusterOptions())
	if err != nil {
		t.Fatalf("Cluster failed: %v", err)
	}

	if result.NumClusters != 2 {
		t.Fatalf("NumClusters = %d, want 2", result.NumClusters)
	}
	if result.Quality != 0.82 {
		t.Fatalf("Quality = %v, want 0.82 (forwarded from the model's answer)", result.Quality)
	}
	if len(result.Outliers) != 1 || result.Outliers[0] != "wrench" {
		t.Fatalf("Outliers = %+v, want [wrench]", result.Outliers)
	}

	var fruitsCluster *ClusterInfo[string]
	for i := range result.Clusters {
		if result.Clusters[i].Name == "Fruits" {
			fruitsCluster = &result.Clusters[i]
		}
	}
	if fruitsCluster == nil {
		t.Fatal("the Fruits cluster is missing")
	}
	if fruitsCluster.Size != 2 || len(fruitsCluster.Items) != 2 {
		t.Fatalf("Fruits cluster = %+v, want Size 2 and 2 items", fruitsCluster)
	}
	if fruitsCluster.Items[0] != "apple" || fruitsCluster.Items[1] != "banana" {
		t.Fatalf("Fruits cluster items = %v, want [apple banana] resolved from indices [0 2]", fruitsCluster.Items)
	}
}

// TestCluster_OutliersCountAsAGroup is the contract the CI-008 comment in
// cluster.go states directly: "Outliers count as a group. An item the model
// called an outlier is placed, not missing." A response with no clusters at
// all, only outlier_indices covering every item, must still be accepted as a
// complete partition.
func TestCluster_OutliersCountAsAGroup(t *testing.T) {
	withResponse(t, `{"clusters": [], "outlier_indices": [0, 1, 2], "quality": 0.1}`)

	items := []string{"x", "y", "z"}
	result, err := Cluster(items, NewClusterOptions())
	if err != nil {
		t.Fatalf("an all-outlier partition should be accepted, got: %v", err)
	}
	if result.NumClusters != 0 {
		t.Fatalf("NumClusters = %d, want 0", result.NumClusters)
	}
	if len(result.Outliers) != 3 {
		t.Fatalf("Outliers = %+v, want all 3 items", result.Outliers)
	}
}

// --- Refusals: the partition contract (CoversExactlyOnce), driven through Cluster.

func TestCluster_RefusesAnIndexUsedTwice(t *testing.T) {
	withResponse(t, `{
		"clusters": [
			{"name": "A", "indices": [0, 1]},
			{"name": "B", "indices": [1, 2]}
		],
		"outlier_indices": []
	}`)

	_, err := Cluster([]string{"a", "b", "c"}, NewClusterOptions())
	if err == nil {
		t.Fatal("expected an error: index 1 was placed in two clusters")
	}
	if !strings.Contains(err.Error(), "partition") {
		t.Fatalf("error does not describe a partition failure: %v", err)
	}
}

func TestCluster_RefusesAnOutOfRangeIndex(t *testing.T) {
	withResponse(t, `{
		"clusters": [
			{"name": "A", "indices": [0, 1, 99]}
		],
		"outlier_indices": []
	}`)

	_, err := Cluster([]string{"a", "b", "c"}, NewClusterOptions())
	if err == nil {
		t.Fatal("expected an error: index 99 is out of range for 3 items")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("error does not name the out-of-range problem: %v", err)
	}
}

func TestCluster_RefusesAnItemLeftInNoGroup(t *testing.T) {
	withResponse(t, `{
		"clusters": [
			{"name": "A", "indices": [0]}
		],
		"outlier_indices": []
	}`)

	// 3 items offered, only index 0 placed anywhere -- 1 and 2 are in no
	// group and not claimed as outliers either.
	_, err := Cluster([]string{"a", "b", "c"}, NewClusterOptions())
	if err == nil {
		t.Fatal("expected an error: items 1 and 2 are in no group")
	}
	if !strings.Contains(err.Error(), "no group") {
		t.Fatalf("error does not describe the missing items: %v", err)
	}
}

func TestCluster_RefusesMalformedJSON(t *testing.T) {
	withResponse(t, `{"clusters": [`) // truncated
	_, err := Cluster([]string{"a", "b"}, NewClusterOptions())
	if err == nil {
		t.Fatal("expected a parse error for truncated JSON")
	}
}

// TestCluster_RefusesAnOffTopicResponse pins ParseJSONStrict's field check as
// reached through Cluster: a well-formed JSON object that shares none of
// "clusters", "outlier_indices", or "quality" is a response about something
// else, not an empty clustering.
func TestCluster_RefusesAnOffTopicResponse(t *testing.T) {
	withResponse(t, `{"unrelated_field": "some other answer entirely"}`)
	_, err := Cluster([]string{"a", "b"}, NewClusterOptions())
	if err == nil {
		t.Fatal("expected a schema-violation error for an off-topic response")
	}
}

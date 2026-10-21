package schemaflux_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// clusteredTicket is a value of the caller's own to group.
type clusteredTicket struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

const (
	// clusterTwoAndOne puts the first two of three items in one group and sets the
	// third aside.
	clusterTwoAndOne = `{"clusters":[{"name":"greetings","description":"people saying hello","indices":[0,1],"keywords":["hello","hi"]}],` +
		`"outlier_indices":[2],"quality":0.8}`

	// clusterAllPlaced puts all three in groups and sets none aside.
	clusterAllPlaced = `{"clusters":[{"name":"first","description":"d1","indices":[0,1],"keywords":["a"]},` +
		`{"name":"second","description":"d2","indices":[2],"keywords":[]}],"outlier_indices":[],"quality":0.9}`

	// clusterAllSetAside sets every one of three items aside.
	clusterAllSetAside = `{"clusters":[],"outlier_indices":[0,1,2],"quality":0.2}`
)

var clusterItems = []string{"hello", "hi there", "invoice 4"}

// clusterRequest makes one call with the options given on a model that gives the
// answer, and returns the request it sent.
func clusterRequest(t *testing.T, answer string, opts ...schemaflux.ClusterOptions) schemaflux.Request {
	t.Helper()
	model := answeringWith(answer)
	if _, err := schemaflux.Cluster(operationClient(model), clusterItems, opts...); err != nil {
		t.Fatalf("the call failed: %v", err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	return model.Last()
}

func TestClusterReturnsTheGroupsTheOutliersAndTheirPositions(t *testing.T) {
	t.Parallel()
	model := answeringWith(clusterTwoAndOne)
	res, err := schemaflux.Cluster(operationClient(model), clusterItems)
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.ClusterResult[string]{
		Clusters: []schemaflux.ClusterInfo[string]{{
			Name: "greetings", Description: "people saying hello", Items: []string{"hello", "hi there"},
			Indices: []int{0, 1}, Keywords: []string{"hello", "hi"}, Size: 2,
		}},
		Outliers: []string{"invoice 4"}, OutlierIndices: []int{2}, TotalItems: 3, NumClusters: 1, Quality: 0.8,
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("got %+v, want %+v", res, want)
	}
	req := model.Last()
	wantText(t, req, `[{"index":0,"item":"hello"},{"index":1,"item":"hi there"},{"index":2,"item":"invoice 4"}]`)
	if req.Format != "json" {
		t.Errorf("format = %q, want json", req.Format)
	}
}

func TestClusterReturnsTheCallersOwnValuesInTheirGroups(t *testing.T) {
	t.Parallel()
	tickets := []clusteredTicket{{1, "refund"}, {2, "refund again"}, {3, "login"}}
	res, err := schemaflux.Cluster(operationClient(answeringWith(clusterTwoAndOne)), tickets)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clusters) != 1 || !reflect.DeepEqual(res.Clusters[0].Items, tickets[:2]) || !reflect.DeepEqual(res.Outliers, tickets[2:]) {
		t.Errorf("got %+v", res)
	}
	res.Clusters[0].Items[0].Title = "changed"
	res.Outliers[0].Title = "changed"
	if tickets[0].Title != "refund" || tickets[2].Title != "login" {
		t.Errorf("changing the result changed the caller's tickets: %+v", tickets)
	}
}

func TestClusterWithNoOptionsAsksForGroupsOfMeaningWithDescriptionsAndAnOutlierGroup(t *testing.T) {
	t.Parallel()
	req := clusterRequest(t, clusterTwoAndOne)
	wantText(t, req, "Group the items based on semantic similarity.")
	wantText(t, req, "Automatically determine the optimal number of clusters (minimum cluster size: 1).")
	wantText(t, req, "Similarity threshold: 0.70")
	wantText(t, req, "Naming strategy: auto")
	wantText(t, req, "Give every cluster a description")
	wantText(t, req, "'outliers' group")
	wantNoText(t, req, "No cluster may contain more than")
	if req.Model != "test-model" || req.WebSearch {
		t.Errorf("model %q web search %v", req.Model, req.WebSearch)
	}
}

func TestClusterEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	base := clusterRequest(t, clusterTwoAndOne)
	cases := []struct {
		name   string
		answer string
		opts   schemaflux.ClusterOptions
		check  func(t *testing.T, req schemaflux.Request)
	}{
		{"By is quoted", clusterTwoAndOne, schemaflux.ClusterOptions{By: `root "cause"`}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `based on "root \"cause\"".`)
			wantNoText(t, req, "semantic similarity")
		}},
		{"NumClusters", clusterTwoAndOne, schemaflux.ClusterOptions{NumClusters: 4}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Create exactly 4 clusters.")
			wantNoText(t, req, "Automatically determine")
		}},
		{"MinClusterSize", clusterTwoAndOne, schemaflux.ClusterOptions{MinClusterSize: 2}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "minimum cluster size: 2")
		}},
		{"MaxClusterSize", clusterTwoAndOne, schemaflux.ClusterOptions{MaxClusterSize: 5}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "No cluster may contain more than 5 items")
		}},
		{"NamingStrategy descriptive", clusterTwoAndOne, schemaflux.ClusterOptions{NamingStrategy: "descriptive"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Naming strategy: descriptive")
		}},
		{"NamingStrategy numbered", clusterTwoAndOne, schemaflux.ClusterOptions{NamingStrategy: "numbered"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Naming strategy: numbered")
		}},
		{"NamingStrategy auto is the default", clusterTwoAndOne, schemaflux.ClusterOptions{NamingStrategy: "auto"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Naming strategy: auto")
		}},
		{"SimilarityThreshold", clusterTwoAndOne, schemaflux.ClusterOptions{SimilarityThreshold: 0.4}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Similarity threshold: 0.40")
		}},
		{"SimilarityThreshold of one", clusterTwoAndOne, schemaflux.ClusterOptions{SimilarityThreshold: 1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Similarity threshold: 1.00")
		}},
		{"SimilarityThreshold negative means 0", clusterTwoAndOne, schemaflux.ClusterOptions{SimilarityThreshold: -0.5}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Similarity threshold: 0.00")
		}},
		{"NoOutlierGroup", clusterAllPlaced, schemaflux.ClusterOptions{NoOutlierGroup: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Force all items into the nearest cluster")
			wantNoText(t, req, "'outliers' group")
		}},
		{"OmitDescriptions", clusterTwoAndOne, schemaflux.ClusterOptions{OmitDescriptions: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Leave the description empty")
			wantNoText(t, req, "Give every cluster a description")
		}},
		{"Steering", clusterTwoAndOne, schemaflux.ClusterOptions{Options: schemaflux.Options{Steering: "split by tone"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "split by tone")
		}},
		{"Model", clusterTwoAndOne, schemaflux.ClusterOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"Tier", clusterTwoAndOne, schemaflux.ClusterOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens == base.MaxTokens {
				t.Errorf("the output ceiling is %d at both tiers", req.MaxTokens)
			}
		}},
		{"Mode", clusterTwoAndOne, schemaflux.ClusterOptions{Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, func(t *testing.T, req schemaflux.Request) {
			if req.Temperature == base.Temperature {
				t.Errorf("the temperature is %v in both modes", req.Temperature)
			}
		}},
		{"Effort", clusterTwoAndOne, schemaflux.ClusterOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
		{"MaxOutputTokens", clusterTwoAndOne, schemaflux.ClusterOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", clusterTwoAndOne, schemaflux.ClusterOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.check(t, clusterRequest(t, tc.answer, tc.opts))
		})
	}
}

func TestClusterRefusesASettingNoCallCanUseBeforeSendingAnythingWhateverTheNumberOfItems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts schemaflux.ClusterOptions
	}{
		{"a negative number of groups", schemaflux.ClusterOptions{NumClusters: -1}},
		{"a negative minimum", schemaflux.ClusterOptions{MinClusterSize: -1}},
		{"a negative maximum", schemaflux.ClusterOptions{MaxClusterSize: -1}},
		{"the lowest maximum there is", schemaflux.ClusterOptions{MaxClusterSize: math.MinInt}},
		{"a threshold that is not a number", schemaflux.ClusterOptions{SimilarityThreshold: math.NaN()}},
		{"an infinite threshold", schemaflux.ClusterOptions{SimilarityThreshold: math.Inf(1)}},
		{"an infinitely negative threshold", schemaflux.ClusterOptions{SimilarityThreshold: math.Inf(-1)}},
		{"a threshold just above one", schemaflux.ClusterOptions{SimilarityThreshold: math.Nextafter(1, 2)}},
		{"the largest threshold there is", schemaflux.ClusterOptions{SimilarityThreshold: math.MaxFloat64}},
		{"a naming strategy no one knows", schemaflux.ClusterOptions{NamingStrategy: "fancy"}},
		{"a naming strategy in another case", schemaflux.ClusterOptions{NamingStrategy: "Auto"}},
		{"a negative output ceiling", schemaflux.ClusterOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}},
		{"a tier that is not one", schemaflux.ClusterOptions{Options: schemaflux.Options{Tier: schemaflux.Tier(99)}}},
		{"a mode that is not one", schemaflux.ClusterOptions{Options: schemaflux.Options{Mode: schemaflux.Mode(99)}}},
	}
	for _, tc := range cases {
		for _, items := range [][]string{nil, {"only"}, clusterItems} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				model := answeringWith(clusterTwoAndOne)
				res, err := schemaflux.Cluster(operationClient(model), items, tc.opts)
				wantError(t, err, schemaflux.ErrConfiguration, "cluster")
				if model.Calls() != 0 || !reflect.DeepEqual(res, schemaflux.ClusterResult[string]{}) {
					t.Errorf("%d requests, and a refusal left %+v beside the error", model.Calls(), res)
				}
			})
		}
	}
	t.Run("settings at the edge of their range are fine", func(t *testing.T) {
		t.Parallel()
		for _, o := range []schemaflux.ClusterOptions{{SimilarityThreshold: 1}, {SimilarityThreshold: math.SmallestNonzeroFloat64}, {MinClusterSize: 1}, {MaxClusterSize: math.MaxInt}, {NumClusters: math.MaxInt}} {
			if _, err := schemaflux.Cluster(operationClient(answeringWith(clusterTwoAndOne)), clusterItems, o); err != nil {
				t.Errorf("%+v: %v", o, err)
			}
		}
	})
}

func TestClusterRefusesANilContext(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Cluster[string](nil, clusterItems)
	wantError(t, err, schemaflux.ErrInvalidRequest, "cluster")
	if !reflect.DeepEqual(res, schemaflux.ClusterResult[string]{}) {
		t.Errorf("a refusal left %+v beside the error", res)
	}
}

func TestClusterRefusesAnAnswerThatIsNotAPartitionOfTheItems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		answer string
		opts   schemaflux.ClusterOptions
	}{
		{"not JSON", `the first two belong together`, schemaflux.ClusterOptions{}},
		{"an item in two groups", `{"clusters":[{"name":"a","indices":[0,1],"keywords":[]},{"name":"b","indices":[1,2],"keywords":[]}],"outlier_indices":[],"quality":0.5}`, schemaflux.ClusterOptions{}},
		{"an item in a group and among the outliers", `{"clusters":[{"name":"a","indices":[0,1,2],"keywords":[]}],"outlier_indices":[2],"quality":0.5}`, schemaflux.ClusterOptions{}},
		{"an item repeated in one group", `{"clusters":[{"name":"a","indices":[0,0,1,2],"keywords":[]}],"outlier_indices":[],"quality":0.5}`, schemaflux.ClusterOptions{}},
		{"an item left out", `{"clusters":[{"name":"a","indices":[0,1],"keywords":[]}],"outlier_indices":[],"quality":0.5}`, schemaflux.ClusterOptions{}},
		{"a position past the end", `{"clusters":[{"name":"a","indices":[0,1,2,3],"keywords":[]}],"outlier_indices":[],"quality":0.5}`, schemaflux.ClusterOptions{}},
		{"a negative position", `{"clusters":[{"name":"a","indices":[-1,0,1,2],"keywords":[]}],"outlier_indices":[],"quality":0.5}`, schemaflux.ClusterOptions{}},
		{"nothing placed at all", `{"clusters":[],"outlier_indices":[],"quality":0.5}`, schemaflux.ClusterOptions{}},
		{"a group below the minimum", clusterAllPlaced, schemaflux.ClusterOptions{MinClusterSize: 2}},
		{"a group above the maximum", clusterAllPlaced, schemaflux.ClusterOptions{MaxClusterSize: 1}},
		{"outliers when none are allowed", clusterTwoAndOne, schemaflux.ClusterOptions{NoOutlierGroup: true}},
		{"a quality above one", `{"clusters":[{"name":"a","indices":[0,1,2],"keywords":[]}],"outlier_indices":[],"quality":1.2}`, schemaflux.ClusterOptions{}},
		{"a quality below zero", `{"clusters":[{"name":"a","indices":[0,1,2],"keywords":[]}],"outlier_indices":[],"quality":-0.2}`, schemaflux.ClusterOptions{}},
		{"no quality", `{"clusters":[{"name":"a","indices":[0,1,2],"keywords":[]}],"outlier_indices":[]}`, schemaflux.ClusterOptions{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.answer)
			res, err := schemaflux.Cluster(operationClient(model), clusterItems, tc.opts)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "cluster")
			if model.Calls() != 1 || !reflect.DeepEqual(res, schemaflux.ClusterResult[string]{}) {
				t.Errorf("%d requests, and a refused answer left %+v beside the error", model.Calls(), res)
			}
		})
	}
}

func TestClusterDoesNotEnforceWhatItOnlyAsksFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		answer string
		opts   schemaflux.ClusterOptions
		groups int
	}{
		{"a number of groups other than the target", clusterAllPlaced, schemaflux.ClusterOptions{NumClusters: 3}, 2},
		{"one group where the target is two", `{"clusters":[{"name":"a","indices":[0,1,2],"keywords":[]}],"outlier_indices":[],"quality":0.5}`, schemaflux.ClusterOptions{NumClusters: 2}, 1},
		{"a single outlier below the minimum size, which the outlier group is exempt from", clusterTwoAndOne, schemaflux.ClusterOptions{MinClusterSize: 2}, 1},
		{"every item set aside, which no minimum applies to", clusterAllSetAside, schemaflux.ClusterOptions{MinClusterSize: 3}, 0},
		{"a group exactly at the maximum", clusterTwoAndOne, schemaflux.ClusterOptions{MaxClusterSize: 2}, 1},
		{"a description from a call that asked for none", clusterTwoAndOne, schemaflux.ClusterOptions{OmitDescriptions: true}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Cluster(operationClient(answeringWith(tc.answer)), clusterItems, tc.opts)
			if err != nil || len(res.Clusters) != tc.groups || res.NumClusters != tc.groups || res.TotalItems != 3 {
				t.Errorf("got %+v, %v", res, err)
			}
		})
	}
}

func TestClusterSizeIsCountedFromTheItemsAndNotReportedByTheModel(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Cluster(operationClient(answeringWith(clusterAllPlaced)), clusterItems)
	if err != nil {
		t.Fatal(err)
	}
	if res.Clusters[0].Size != 2 || res.Clusters[1].Size != 1 || len(res.Clusters[1].Items) != 1 {
		t.Errorf("got %+v", res.Clusters)
	}
	if res.Outliers != nil || res.OutlierIndices != nil {
		t.Errorf("outliers %#v at %#v, want none", res.Outliers, res.OutlierIndices)
	}
}

func TestClusterAnswersNoItemsAndOneItemWithoutAModel(t *testing.T) {
	t.Parallel()
	t.Run("no items", func(t *testing.T) {
		t.Parallel()
		for _, items := range [][]string{nil, {}} {
			model := answeringWith(clusterTwoAndOne)
			res, err := schemaflux.Cluster(operationClient(model), items)
			if err != nil || model.Calls() != 0 || res.TotalItems != 0 || res.NumClusters != 0 || len(res.Clusters) != 0 || len(res.Outliers) != 0 || res.Quality != 0 {
				t.Errorf("got %+v, %v with %d requests", res, err, model.Calls())
			}
		}
	})
	t.Run("one item is a group of its own, and the group does not share the caller's slice", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(clusterTwoAndOne)
		items := []string{"alone"}
		res, err := schemaflux.Cluster(operationClient(model), items)
		if err != nil || model.Calls() != 0 {
			t.Fatalf("got %+v, %v with %d requests", res, err, model.Calls())
		}
		want := schemaflux.ClusterInfo[string]{Name: "Single Item", Items: []string{"alone"}, Indices: []int{0}, Size: 1}
		if res.TotalItems != 1 || res.NumClusters != 1 || res.Quality != 0 || len(res.Clusters) != 1 || !reflect.DeepEqual(res.Clusters[0], want) {
			t.Errorf("got %+v", res)
		}
		res.Clusters[0].Items[0] = "changed"
		if items[0] != "alone" {
			t.Errorf("changing the result changed the caller's items: %v", items)
		}
	})
}

func TestClusterMergesSeveralOptionsValuesLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value sets what it names and leaves the rest", func(t *testing.T) {
		t.Parallel()
		req := clusterRequest(t, clusterTwoAndOne,
			schemaflux.ClusterOptions{By: "topic", MaxClusterSize: 5, NamingStrategy: "numbered", SimilarityThreshold: 0.3, Options: schemaflux.Options{Steering: "kept"}},
			schemaflux.ClusterOptions{NumClusters: 2, Options: schemaflux.Options{Model: "later-model"}},
			schemaflux.ClusterOptions{})
		wantText(t, req, `"topic"`)
		wantText(t, req, "No cluster may contain more than 5 items")
		wantText(t, req, "Naming strategy: numbered")
		wantText(t, req, "Similarity threshold: 0.30")
		wantText(t, req, "Create exactly 2 clusters.")
		wantText(t, req, "kept")
		if req.Model != "later-model" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("the later of two values for one field wins", func(t *testing.T) {
		t.Parallel()
		wantText(t, clusterRequest(t, clusterTwoAndOne, schemaflux.ClusterOptions{By: "first"}, schemaflux.ClusterOptions{By: "later"}), `"later"`)
	})
	t.Run("a negative threshold of a later value wins over a positive one", func(t *testing.T) {
		t.Parallel()
		wantText(t, clusterRequest(t, clusterTwoAndOne, schemaflux.ClusterOptions{SimilarityThreshold: 0.9}, schemaflux.ClusterOptions{SimilarityThreshold: -1}), "Similarity threshold: 0.00")
	})
	t.Run("true bools are not cleared by a later false", func(t *testing.T) {
		t.Parallel()
		req := clusterRequest(t, clusterAllPlaced,
			schemaflux.ClusterOptions{NoOutlierGroup: true, OmitDescriptions: true}, schemaflux.ClusterOptions{})
		wantText(t, req, "Force all items into the nearest cluster")
		wantText(t, req, "Leave the description empty")
	})
	t.Run("a later bad value is refused whatever came before it", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Cluster(operationClient(answeringWith(clusterTwoAndOne)), clusterItems, schemaflux.ClusterOptions{NumClusters: 2}, schemaflux.ClusterOptions{MinClusterSize: -3})
		wantError(t, err, schemaflux.ErrConfiguration, "cluster")
	})
}

func TestClusterReportsAProviderFailureWithItsKindAndNoResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		kind schemaflux.Kind
	}{
		{"a kind the provider gave", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Err: errors.New("slow down")}, schemaflux.ErrRateLimited},
		{"a plain error", errors.New("the connection broke"), schemaflux.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(clusterTwoAndOne)
			model.Fail(tc.err)
			res, err := schemaflux.Cluster(operationClient(model), clusterItems)
			wantError(t, err, tc.kind, "cluster")
			if !reflect.DeepEqual(res, schemaflux.ClusterResult[string]{}) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
	t.Run("a context that is already cancelled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(operationClient(answeringWith(clusterTwoAndOne)))
		cancel()
		_, err := schemaflux.Cluster(ctx, clusterItems)
		wantError(t, err, schemaflux.ErrCanceled, "cluster")
	})
}

// clusterCalled is the call the Record and payload tests make.
func clusterCalled(ctx context.Context) error {
	_, err := schemaflux.Cluster(ctx, clusterItems)
	return err
}

func TestClusterAddsOneRecordNamedClusterForEveryCall(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		call     func(ctx context.Context) error
		requests int
	}{
		{"a call that asks a model", clusterCalled, 1},
		{"a call with no items", func(ctx context.Context) error { _, err := schemaflux.Cluster(ctx, []string{}); return err }, 0},
		{"a call with one item", func(ctx context.Context) error { _, err := schemaflux.Cluster(ctx, []string{"x"}); return err }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			if err := tc.call(operationClient(answeringWith(clusterTwoAndOne), recordingTo(&recorder))); err != nil {
				t.Fatal(err)
			}
			r := onlyRecord(t, &recorder)
			if r.Op != "cluster" || r.Err != nil || r.Requests != tc.requests {
				t.Errorf("record = %q err %v requests %d, want %d", r.Op, r.Err, r.Requests, tc.requests)
			}
			if tc.requests == 1 && (r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed || r.Provider != "test") {
				t.Errorf("levels = %v and %v, provider %q, want json well-formed on test", r.Requested, r.Delivered, r.Provider)
			}
		})
	}
	t.Run("a call whose answer is refused", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		err := clusterCalled(operationClient(answeringWith("not an answer"), recordingTo(&recorder)))
		wantError(t, err, schemaflux.ErrInvalidAnswer, "cluster")
		if r := onlyRecord(t, &recorder); r.Op != "cluster" || r.Err != err || r.Requests != 1 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused for its options, before anything was sent", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Cluster(operationClient(answeringWith(clusterTwoAndOne), recordingTo(&recorder)), clusterItems, schemaflux.ClusterOptions{NumClusters: -1})
		wantError(t, err, schemaflux.ErrConfiguration, "cluster")
		if r := onlyRecord(t, &recorder); r.Op != "cluster" || r.Err != err || r.Requests != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call whose provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(clusterTwoAndOne)
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Err: errors.New("down")})
		err := clusterCalled(operationClient(model, recordingTo(&recorder)))
		wantError(t, err, schemaflux.ErrUnavailable, "cluster")
		if r := onlyRecord(t, &recorder); r.Op != "cluster" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
}

func TestClusterUnderAFloorIsRefusedAboveJSONWellFormedAndDeliveredAtIt(t *testing.T) {
	t.Parallel()
	for _, recorded := range []bool{false, true} {
		for _, floor := range []schemaflux.ContractLevel{schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaConstrained, schemaflux.ContractSchemaAndInvariantChecked} {
			for _, items := range [][]string{clusterItems, nil} {
				name := "no recorder"
				if recorded {
					name = "a recorder"
				}
				t.Run(name+", a floor of "+floor.String()+", "+string(rune('0'+len(items)))+" items", func(t *testing.T) {
					t.Parallel()
					model := namedModel(clusterTwoAndOne)
					var recorder schemaflux.Recorder
					settings := []func(*schemaflux.Client){underFloor(floor)}
					if recorded {
						settings = append(settings, recordingTo(&recorder))
					}
					res, err := schemaflux.Cluster(operationClient(model, settings...), items)
					refused := floor > schemaflux.ContractJSONWellFormed
					wantCalls := 0
					if len(items) > 1 {
						wantCalls = 1
					}
					if refused {
						// A call that would send nothing is refused as well: the level
						// is a property of the operation and not of the call.
						wantError(t, err, schemaflux.ErrPolicy, "cluster")
						if model.Calls() != 0 || !reflect.DeepEqual(res, schemaflux.ClusterResult[string]{}) {
							t.Errorf("a refusal sent %d requests and returned %+v", model.Calls(), res)
						}
					} else if err != nil || model.Calls() != wantCalls {
						t.Fatalf("at its own level: %+v, %v, %d requests", res, err, model.Calls())
					}
					if !recorded {
						return
					}
					r := onlyRecord(t, &recorder)
					if r.Op != "cluster" || (r.Err != nil) != refused || r.Requested != schemaflux.ContractJSONWellFormed {
						t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
					}
					if refused && r.Requests != 0 {
						t.Errorf("a refusal's Record counts %d requests", r.Requests)
					}
				})
			}
		}
	}
}

func TestClusterRunsAsOneOperationScopeForOneCallWhetherOrNotItAsksAModel(t *testing.T) {
	var scopes []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			scopes = append(scopes, r.Operation)
		}
	}})
	defer restore()
	for name, items := range map[string][]string{"a call that asks a model": clusterItems, "a call with no items": nil} {
		scopes = nil
		if _, err := schemaflux.Cluster(operationClient(answeringWith(clusterTwoAndOne)), items); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(scopes) != 1 {
			t.Errorf("%s: %d operation scopes finished (%v), want 1", name, len(scopes), scopes)
		}
	}
}

func TestClusterLeavesTheCallersTextOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "cluster-private-canary-b6d9"
	carrying := func(ctx context.Context) error {
		_, err := schemaflux.Cluster(ctx,
			[]map[string]string{{canary + "-key": "value " + canary}, {"k": canary}},
			schemaflux.ClusterOptions{By: "by " + canary, Options: schemaflux.Options{Steering: "steer " + canary}})
		return err
	}
	for name, answer := range map[string]string{
		"an answer that is not JSON":        "not json " + canary,
		"a name and keywords with the text": `{"clusters":[{"name":"` + canary + `","description":"` + canary + `","indices":[0,1],"keywords":["` + canary + `"]}],"outlier_indices":[],"quality":0.5}`,
		"an answer that is not a partition": `{"clusters":[{"name":"` + canary + `","indices":[0,0],"keywords":[]}],"outlier_indices":[],"quality":0.5}`,
		"a quality off the scale":           `{"clusters":[{"name":"` + canary + `","indices":[0,1],"keywords":[]}],"outlier_indices":[],"quality":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(answer) }, true, carrying)
		})
	}
	t.Run("a provider that fails with a plain error", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(errors.New("vendor said " + canary)) }, false, carrying)
	})
	t.Run("a provider that fails with an Error holding a vendor message", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Err: errors.New("vendor said " + canary)})
		}, false, carrying)
	})
	t.Run("a call refused for an option holding the canary", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(clusterTwoAndOne) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Cluster(ctx, []string{canary, "b"}, schemaflux.ClusterOptions{NamingStrategy: canary, By: canary, Options: schemaflux.Options{Steering: canary}})
			return err
		})
	})
	t.Run("items that cannot be written as JSON", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(clusterTwoAndOne) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Cluster(ctx, []map[string]any{{canary: make(chan int)}, {"b": canary}})
			return err
		})
	})
}

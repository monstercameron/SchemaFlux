package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func TestClusterOptions(t *testing.T) {
	t.Run("NewClusterOptions creates valid defaults", func(t *testing.T) {
		opts := NewClusterOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("default options should be valid: %v", err)
		}
	})

	t.Run("WithNumClusters sets cluster count", func(t *testing.T) {
		opts := NewClusterOptions().WithNumClusters(5)
		if opts.NumClusters != 5 {
			t.Errorf("expected 5 clusters, got %d", opts.NumClusters)
		}
	})

	t.Run("WithNamingStrategy sets strategy", func(t *testing.T) {
		opts := NewClusterOptions().WithNamingStrategy("descriptive")
		if opts.NamingStrategy != "descriptive" {
			t.Errorf("expected descriptive, got %s", opts.NamingStrategy)
		}
	})

	t.Run("WithSimilarityThreshold sets threshold", func(t *testing.T) {
		opts := NewClusterOptions().WithSimilarityThreshold(0.8)
		if opts.SimilarityThreshold != 0.8 {
			t.Errorf("expected 0.8, got %f", opts.SimilarityThreshold)
		}
	})

	t.Run("WithIncludeOutliers enables outliers", func(t *testing.T) {
		opts := NewClusterOptions().WithIncludeOutliers(true)
		if !opts.IncludeOutliers {
			t.Error("expected IncludeOutliers to be true")
		}
	})

	t.Run("Validate rejects invalid naming strategy", func(t *testing.T) {
		opts := NewClusterOptions()
		opts.NamingStrategy = "invalid"
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid naming strategy")
		}
	})

	t.Run("Validate rejects invalid similarity threshold", func(t *testing.T) {
		opts := NewClusterOptions().WithSimilarityThreshold(1.5)
		if err := opts.Validate(); err == nil {
			t.Error("expected error for threshold > 1")
		}
	})
}

func TestClusterOptionsValidateSizeBounds(t *testing.T) {
	cases := []struct {
		name    string
		min     int
		max     int
		wantErr string
	}{
		{"defaults", 1, 0, ""},
		{"zero max means unlimited", 4, 0, ""},
		{"matching bounds", 3, 3, ""},
		{"min below max", 2, 5, ""},
		{"minimum one", 1, 1, ""},
		{"negative max one", 1, -1, "max cluster size cannot be negative, got -1"},
		{"negative max several", 1, -7, "max cluster size cannot be negative, got -7"},
		{"zero min", 0, 4, "min cluster size must be at least 1, got 0"},
		{"negative min", -2, 4, "min cluster size must be at least 1, got -2"},
		{"min exceeds max by one", 4, 3, "min cluster size 4 cannot exceed max cluster size 3"},
		{"min exceeds max by several", 8, 2, "min cluster size 8 cannot exceed max cluster size 2"},
		{"impossible minimum against max one", 2, 1, "min cluster size 2 cannot exceed max cluster size 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewClusterOptions().WithMinClusterSize(tc.min).WithMaxClusterSize(tc.max)
			err := opts.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("Validate() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCluster(t *testing.T) {
	// A scripted answer, so this runs without a provider.
	withResponse(t, `{
		"clusters": [
			{"name": "Python and ML", "indices": [0, 2, 3],"keywords":[]},
			{"name": "Web", "indices": [1, 4, 5],"keywords":[]}
		],
		"outlier_indices": [],
		"quality": 0.8
	}`)

	t.Run("Cluster groups similar items", func(t *testing.T) {
		items := []string{
			"Python programming tutorial",
			"JavaScript for beginners",
			"Machine learning basics",
			"Deep learning with PyTorch",
			"React web development",
			"Vue.js framework guide",
		}

		opts := NewClusterOptions().
			WithNumClusters(2).
			WithNamingStrategy("descriptive")

		result, err := Cluster(items, opts)
		if err != nil {
			t.Fatalf("Cluster failed: %v", err)
		}

		if len(result.Clusters) != 2 {
			t.Fatalf("got %d clusters, want the 2 that were scripted", len(result.Clusters))
		}
		if result.Clusters[0].Items[1] != "Machine learning basics" || result.Clusters[1].Items[0] != "JavaScript for beginners" {
			t.Errorf("clusters = %+v, want the items at the scripted indices", result.Clusters)
		}
	})
}

const clusterCompleteAnswer = `{"clusters":[{"name":"A","description":"d","indices":[0,1],"keywords":["k"]},{"name":"B","description":"d","indices":[2],"keywords":[]}],"outlier_indices":[],"quality":0.5}`

func TestClusterRefusesAnAnswerMissingARequiredMember(t *testing.T) {
	members := [][]any{
		{"clusters"},
		{"clusters", 0, "name"},
		{"clusters", 0, "indices"},
		{"clusters", 1, "keywords"},
		{"outlier_indices"},
		{"quality"},
	}
	refuseEachMissingMember(t, clusterCompleteAnswer, members, func(response string) (any, error) {
		answerAndRecord(t, response)
		return Cluster([]string{"a", "b", "c"}, NewClusterOptions())
	})
}

func TestClusterReadsAnAnswerWhoseClustersHaveNoDescription(t *testing.T) {
	// The description is asked for only when GenerateDescriptions is set, and the
	// prompt tells the model to leave it empty otherwise.
	answerAndRecord(t, `{"clusters":[{"name":"A","indices":[0,1,2],"keywords":[]}],"outlier_indices":[],"quality":0.5}`)
	result, err := Cluster([]string{"a", "b", "c"}, NewClusterOptions().WithGenerateDescriptions(false))
	if err != nil || len(result.Clusters) != 1 || result.Clusters[0].Description != "" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestClusterRefusesAQualityThatIsNull(t *testing.T) {
	answerAndRecord(t, strings.Replace(clusterCompleteAnswer, `"quality":0.5`, `"quality":null`, 1))
	result, err := Cluster([]string{"a", "b", "c"}, NewClusterOptions())
	wantKind(t, err, types.KindSchemaViolation)
	if !strings.Contains(err.Error(), "quality is null") || len(result.Clusters) != 0 {
		t.Errorf("result = %+v, err = %v", result, err)
	}
}

func TestClusterRefusalsBeforeACallHaveAKind(t *testing.T) {
	items := []string{"a", "b", "c"}
	refuse := func(change func(*ClusterOptions)) func() error {
		return func() error {
			options := NewClusterOptions()
			change(&options)
			_, err := Cluster(items, options)
			return err
		}
	}
	checkRefusalsBeforeACall(t, []refusalBeforeACall{
		{"a negative number of clusters", refuse(func(o *ClusterOptions) { o.NumClusters = -1 }),
			"invalid options: num clusters cannot be negative, got -1", types.KindConfiguration},
		{"a minimum size of zero", refuse(func(o *ClusterOptions) { o.MinClusterSize = 0 }),
			"invalid options: min cluster size must be at least 1, got 0", types.KindConfiguration},
		{"a negative minimum size", refuse(func(o *ClusterOptions) { o.MinClusterSize = -4 }),
			"invalid options: min cluster size must be at least 1, got -4", types.KindConfiguration},
		{"a negative maximum size", refuse(func(o *ClusterOptions) { o.MaxClusterSize = -2 }),
			"invalid options: max cluster size cannot be negative, got -2", types.KindConfiguration},
		{"a minimum larger than its maximum", refuse(func(o *ClusterOptions) {
			o.MinClusterSize, o.MaxClusterSize = 4, 3
		}), "invalid options: min cluster size 4 cannot exceed max cluster size 3", types.KindConfiguration},
		{"a similarity threshold above one", refuse(func(o *ClusterOptions) { o.SimilarityThreshold = 1.25 }),
			"invalid options: similarity threshold must be between 0 and 1, got 1.250000", types.KindConfiguration},
		{"a negative similarity threshold", refuse(func(o *ClusterOptions) { o.SimilarityThreshold = -0.5 }),
			"invalid options: similarity threshold must be between 0 and 1, got -0.500000", types.KindConfiguration},
		{"a naming strategy nobody knows", refuse(func(o *ClusterOptions) { o.NamingStrategy = "poetic" }),
			"invalid options: naming strategy must be auto, descriptive, or numbered", types.KindConfiguration},
		{"a threshold above one on the common half", refuse(func(o *ClusterOptions) { o.CommonOptions.Threshold = 3 }),
			"invalid options: threshold must be between 0 and 1, got 3.000000", types.KindConfiguration},
		{"a bad option beside one item", func() error {
			options := NewClusterOptions()
			options.NumClusters = -2
			_, err := Cluster([]string{"only"}, options)
			return err
		}, "invalid options: num clusters cannot be negative, got -2", types.KindConfiguration},
		{"a bad option beside no items", func() error {
			options := NewClusterOptions()
			options.NamingStrategy = "x"
			_, err := Cluster([]string(nil), options)
			return err
		}, "invalid options: naming strategy must be auto, descriptive, or numbered", types.KindConfiguration},
		{"a bad option beside a typed slice of structs", func() error {
			options := NewClusterOptions()
			options.MinClusterSize = 0
			_, err := Cluster([]Person{{Name: "a"}, {Name: "b"}}, options)
			return err
		}, "invalid options: min cluster size must be at least 1, got 0", types.KindConfiguration},
	})
}

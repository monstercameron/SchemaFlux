package ops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ClusterOptions configures the Cluster operation
type ClusterOptions struct {
	CommonOptions
	types.OpOptions

	// Target number of clusters (0 for auto-detection)
	NumClusters int

	// Minimum items per cluster
	MinClusterSize int

	// Maximum items per cluster (0 for unlimited)
	MaxClusterSize int

	// Naming strategy for clusters ("auto", "descriptive", "numbered")
	NamingStrategy string

	// Similarity threshold for clustering (0.0-1.0)
	SimilarityThreshold float64

	// Clustering criteria (natural language description)
	ClusterBy string

	// Include outliers in a separate cluster
	IncludeOutliers bool

	// Generate cluster descriptions
	GenerateDescriptions bool
}

// NewClusterOptions creates ClusterOptions with defaults
func NewClusterOptions() ClusterOptions {
	return ClusterOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		NumClusters:          0, // Auto-detect
		MinClusterSize:       1,
		NamingStrategy:       "auto",
		SimilarityThreshold:  0.7,
		IncludeOutliers:      true,
		GenerateDescriptions: true,
	}
}

// Validate validates ClusterOptions
func (c ClusterOptions) Validate() error {
	if err := c.CommonOptions.Validate(); err != nil {
		return err
	}
	if c.NumClusters < 0 {
		return fmt.Errorf("num clusters cannot be negative, got %d", c.NumClusters)
	}
	if c.MinClusterSize < 1 {
		return fmt.Errorf("min cluster size must be at least 1, got %d", c.MinClusterSize)
	}
	if c.SimilarityThreshold < 0 || c.SimilarityThreshold > 1 {
		return fmt.Errorf("similarity threshold must be between 0 and 1, got %f", c.SimilarityThreshold)
	}
	validStrategies := map[string]bool{"auto": true, "descriptive": true, "numbered": true}
	if c.NamingStrategy != "" && !validStrategies[c.NamingStrategy] {
		return fmt.Errorf("naming strategy must be auto, descriptive, or numbered")
	}
	return nil
}

// WithNumClusters sets the target number of clusters
func (c ClusterOptions) WithNumClusters(n int) ClusterOptions {
	c.NumClusters = n
	return c
}

// WithMinClusterSize sets the minimum cluster size
func (c ClusterOptions) WithMinClusterSize(size int) ClusterOptions {
	c.MinClusterSize = size
	return c
}

// WithMaxClusterSize sets the maximum cluster size
func (c ClusterOptions) WithMaxClusterSize(size int) ClusterOptions {
	c.MaxClusterSize = size
	return c
}

// WithNamingStrategy sets the naming strategy
func (c ClusterOptions) WithNamingStrategy(strategy string) ClusterOptions {
	c.NamingStrategy = strategy
	return c
}

// WithSimilarityThreshold sets the similarity threshold
func (c ClusterOptions) WithSimilarityThreshold(threshold float64) ClusterOptions {
	c.SimilarityThreshold = threshold
	return c
}

// WithClusterBy sets the clustering criteria
func (c ClusterOptions) WithClusterBy(criteria string) ClusterOptions {
	c.ClusterBy = criteria
	return c
}

// WithIncludeOutliers includes outliers in a separate cluster
func (c ClusterOptions) WithIncludeOutliers(include bool) ClusterOptions {
	c.IncludeOutliers = include
	return c
}

// WithGenerateDescriptions enables cluster descriptions
func (c ClusterOptions) WithGenerateDescriptions(generate bool) ClusterOptions {
	c.GenerateDescriptions = generate
	return c
}

// WithSteering sets the steering prompt
func (c ClusterOptions) WithSteering(steering string) ClusterOptions {
	c.CommonOptions = c.CommonOptions.WithSteering(steering)
	return c
}

// WithMode sets the mode
func (c ClusterOptions) WithMode(mode types.Mode) ClusterOptions {
	c.CommonOptions = c.CommonOptions.WithMode(mode)
	return c
}

// WithIntelligence sets the intelligence level
func (c ClusterOptions) WithIntelligence(intelligence types.Speed) ClusterOptions {
	c.CommonOptions = c.CommonOptions.WithIntelligence(intelligence)
	return c
}

func (c ClusterOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(c.CommonOptions, c.OpOptions)
}

func (c ClusterOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return c.CommonOptions, c.OpOptions
}

// ClusterInfo contains information about a single cluster
type ClusterInfo[T any] struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Items       []T    `json:"items"`
	Indices     []int  `json:"indices"`
	// Centroid is never filled in: nothing computes one.
	Centroid string   `json:"centroid,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
	Size     int      `json:"size"`
}

// ClusterResult contains the results of clustering
type ClusterResult[T any] struct {
	Clusters       []ClusterInfo[T] `json:"clusters"`
	Outliers       []T              `json:"outliers,omitempty"`
	OutlierIndices []int            `json:"outlier_indices,omitempty"`
	TotalItems     int              `json:"total_items"`
	NumClusters    int              `json:"num_clusters"`

	// Quality is the model's own claim about how good its grouping is, from 0
	// to 1. It is not a measurement and is not calibrated; the answer is
	// refused when it is missing or off that scale. It is zero when no model was
	// asked: for an input of fewer than two items.
	Quality float64 `json:"quality,omitempty"`

	// Metadata is an empty map today: nothing is recorded in it.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Cluster groups similar items semantically without predefined categories.
// Uses LLM to understand semantic similarity and create meaningful groupings.
//
// The options are checked first, whatever the number of items. The answer is
// held to the partition and to MinClusterSize, MaxClusterSize and, when
// IncludeOutliers is false, to having no outliers. NumClusters,
// SimilarityThreshold and NamingStrategy are hints to the model and are not
// checked against the answer.
//
// Type parameter T specifies the type of items to cluster.
//
// Examples:
//
//	// Auto-detect clusters
//	result, err := Cluster(documents, NewClusterOptions())
//
//	// Specify number of clusters
//	result, err := Cluster(products, NewClusterOptions().
//	    WithNumClusters(5).
//	    WithNamingStrategy("descriptive"))
//
//	// Cluster by specific criteria
//	result, err := Cluster(customers, NewClusterOptions().
//	    WithClusterBy("purchasing behavior and preferences"))
func Cluster[T any](items []T, opts ClusterOptions) (ClusterResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting cluster operation", "itemCount", len(items))

	// What was asked for is checked whether or not anything will be asked of a
	// model. The shortcuts below came first, so a negative NumClusters and a
	// naming strategy nobody knows were accepted with one item and refused with
	// two.
	if err := opts.Validate(); err != nil {
		return ClusterResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	if len(items) == 0 {
		return ClusterResult[T]{Metadata: make(map[string]any)}, nil
	}

	// One item is its own cluster and no model is asked. The cluster is named
	// "Single Item", Quality stays zero because nothing measured or claimed one,
	// and NumClusters is not consulted: there is nothing to divide.
	if len(items) == 1 {
		return ClusterResult[T]{
			Clusters: []ClusterInfo[T]{{
				Name:    "Single Item",
				Items:   items,
				Indices: []int{0},
				Size:    1,
			}},
			TotalItems:  1,
			NumClusters: 1,
			Metadata:    make(map[string]any),
		}, nil
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// refuse turns an answer down in this library's words. It names counts and
	// positions and repeats nothing the model wrote.
	refuse := func(kind types.ErrorKind, format string, args ...any) (ClusterResult[T], error) {
		return ClusterResult[T]{}, types.Newf(kind, "cluster", format, args...)
	}

	// CI-008. The items used to be sent as `[0] {...}` lines joined by
	// newlines, which is prose: the indices the answer has to refer to were
	// readable only by a reader willing to parse a bracket prefix out of free
	// text. Everything downstream -- CoversExactlyOnce, the outlier accounting,
	// the whole partition contract -- is stated in those indices, so the one
	// thing the prompt most needed to be unambiguous about was the one thing it
	// left to inference.
	//
	// They go as a JSON array of {index, item} instead, which says the same
	// thing in a form that parses. This is not only about the local double: a
	// numbering a provider has to recover from formatting is a numbering that
	// can be recovered wrongly, and a wrong index here does not fail loudly --
	// it silently clusters the wrong item.
	indexedItems := make([]map[string]any, len(items))
	for i, item := range items {
		indexedItems[i] = map[string]any{"index": i, "item": item}
	}

	itemsJSON, err := json.Marshal(indexedItems)
	if err != nil {
		log.Error("Cluster operation failed: marshal error", "error", loggedError(err))
		return refuse(types.KindInvalidRequest, "the items could not be written as JSON")
	}

	var clusterConstraint string
	if opts.NumClusters > 0 {
		clusterConstraint = fmt.Sprintf("Create exactly %d clusters.", opts.NumClusters)
	} else {
		clusterConstraint = fmt.Sprintf("Automatically determine the optimal number of clusters (minimum cluster size: %d).", opts.MinClusterSize)
	}

	// The caller's own words are written as a JSON string, so that a ClusterBy
	// that reads like an instruction of its own stays a quoted value inside the
	// sentence.
	clusterCriteria := "semantic similarity"
	if opts.ClusterBy != "" {
		quoted, quoteErr := promptJSON(opts.ClusterBy)
		if quoteErr != nil {
			return refuse(types.KindInvalidRequest, "ClusterBy could not be written as JSON")
		}
		clusterCriteria = string(quoted)
	}

	sizeLimit := ""
	if opts.MaxClusterSize > 0 {
		sizeLimit = fmt.Sprintf("\nNo cluster may contain more than %d items; split a cluster that would exceed it.", opts.MaxClusterSize)
	}

	descriptionNote := "\nLeave the description empty; the cluster name is enough."
	if opts.GenerateDescriptions {
		descriptionNote = "\nGive every cluster a description saying what its members have in common."
	}

	outlierHandling := "Force all items into the nearest cluster, even if not a perfect fit."
	if opts.IncludeOutliers {
		outlierHandling = "Place items that don't fit well into any cluster in an 'outliers' group."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at semantic clustering. Group the items based on %s.%s%s

%s

%s

Naming strategy: %s
Similarity threshold: %.2f

Return a JSON object with:
{
  "clusters": [
    {
      "name": "Cluster Name",
      "description": "What this cluster represents",
      "indices": [0, 3, 7],
      "keywords": ["keyword1", "keyword2"]
    }
  ],
  "outlier_indices": [2, 5],
  "quality": 0.85
}`, clusterCriteria, sizeLimit, descriptionNote, clusterConstraint, outlierHandling, opts.NamingStrategy, opts.SimilarityThreshold)

	userPrompt := fmt.Sprintf("Cluster these items. Each entry carries the index the answer must refer to:\n\n%s", string(itemsJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Cluster operation LLM call failed", "error", loggedError(err))
		return ClusterResult[T]{}, fmt.Errorf("clustering failed: %w", err)
	}

	// Parse the response. Every member the prompt shows is required, so an
	// answer that leaves one out is refused by name. The description is the one
	// exception: it is asked for only when GenerateDescriptions is set, and
	// otherwise the prompt tells the model to leave it empty.
	var parsed struct {
		Clusters []struct {
			Name        string   `json:"name"`
			Description string   `json:"description,omitempty"`
			Indices     []int    `json:"indices"`
			Keywords    []string `json:"keywords"`
		} `json:"clusters"`
		OutlierIndices []int   `json:"outlier_indices"`
		Quality        float64 `json:"quality"`
	}

	if err := decodeAnswer("cluster", response, &parsed); err != nil {
		log.Error("Cluster operation failed: parse error", "error", loggedError(err))
		return ClusterResult[T]{}, err
	}

	// IncludeOutliers=false tells the model to place every item in a cluster, so
	// an answer that sets some aside has not done what was asked. It used to be
	// accepted, and an answer that set every item aside came back as a success
	// with no clusters at all.
	if !opts.IncludeOutliers && len(parsed.OutlierIndices) > 0 {
		return refuse(types.KindInvariantViolation,
			"the response sets %d items aside as outliers, and outliers were not allowed", len(parsed.OutlierIndices))
	}

	// A clustering is a partition: every input in exactly one group, no index
	// out of range, none in two groups. The check runs before anything is built,
	// because a caller iterating clusters that overlap processes an item twice,
	// and one that leaves items out drops them silently.
	//
	// Outliers count as a group. An item the model called an outlier is placed,
	// not missing.
	groups := make([][]int, 0, len(parsed.Clusters)+1)
	for _, c := range parsed.Clusters {
		groups = append(groups, c.Indices)
	}
	groups = append(groups, parsed.OutlierIndices)

	if err := CoversExactlyOnce(len(items), groups); err != nil {
		log.Error("Cluster operation failed: the clusters do not partition the input", "error", loggedError(err))
		return refuse(types.KindInvariantViolation, "%s", err.Error())
	}

	// The size limits, checked against the answer rather than only asked for.
	//
	// MinClusterSize and MaxClusterSize appeared in the prompt and nowhere
	// else: a model was told the bounds and free to ignore them, and a caller
	// who set MaxClusterSize(2) could receive a single cluster of everything
	// with a nil error. That is the same shape as Conform returning nothing and
	// Interpolate changing a sequence's length -- an option the library states
	// and never verifies.
	//
	// Outliers are deliberately exempt. They are the leftover group rather than
	// a cluster the model chose to form, and a single outlier is the normal
	// case; applying a minimum to them would reject correct answers.
	//
	// NumClusters, SimilarityThreshold and NamingStrategy are deliberately NOT
	// enforced. They are hints to the model: NumClusters is documented as a
	// "Target number of clusters (0 for auto-detection)", and refusing an answer
	// that found four natural groups when three were suggested would be the
	// library overriding the judgement it asked for; the threshold has nothing
	// in the answer to be measured against; a naming strategy is a style of name.
	// Recorded here so the next reader knows the omission was a decision.
	for _, c := range parsed.Clusters {
		size := len(c.Indices)
		if size < opts.MinClusterSize {
			log.Error("Cluster operation failed: a cluster is below the minimum size",
				"size", size, "minimum", opts.MinClusterSize)
			return refuse(types.KindInvariantViolation,
				"a cluster holds %d item(s) and MinClusterSize is %d", size, opts.MinClusterSize)
		}
		if opts.MaxClusterSize > 0 && size > opts.MaxClusterSize {
			log.Error("Cluster operation failed: a cluster is above the maximum size",
				"size", size, "maximum", opts.MaxClusterSize)
			return refuse(types.KindInvariantViolation,
				"a cluster holds %d item(s) and MaxClusterSize is %d", size, opts.MaxClusterSize)
		}
	}

	if scaleErr := onScale("cluster", "the quality", parsed.Quality); scaleErr != nil {
		return ClusterResult[T]{}, scaleErr
	}

	// Build cluster result. The partition check above has shown every index is
	// in range, and Size is derived from the items placed rather than reported.
	result := ClusterResult[T]{
		TotalItems: len(items),
		Quality:    parsed.Quality,
		Metadata:   make(map[string]any),
	}
	for _, c := range parsed.Clusters {
		cluster := ClusterInfo[T]{
			Name:        c.Name,
			Description: c.Description,
			Indices:     c.Indices,
			Keywords:    c.Keywords,
			Items:       make([]T, 0, len(c.Indices)),
		}
		for _, idx := range c.Indices {
			cluster.Items = append(cluster.Items, items[idx])
		}
		cluster.Size = len(cluster.Items)

		result.Clusters = append(result.Clusters, cluster)
	}

	// Handle outliers
	for _, idx := range parsed.OutlierIndices {
		result.Outliers = append(result.Outliers, items[idx])
		result.OutlierIndices = append(result.OutlierIndices, idx)
	}

	result.NumClusters = len(result.Clusters)

	log.Debug("Cluster operation succeeded", "numClusters", result.NumClusters, "outlierCount", len(result.Outliers))
	return result, nil
}

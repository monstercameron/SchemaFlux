// package ops - Cluster operation for grouping similar items semantically
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
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
		return fmt.Errorf("invalid naming strategy: %s", c.NamingStrategy)
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
	return c.CommonOptions.toOpOptions()
}

// ClusterInfo contains information about a single cluster
type ClusterInfo[T any] struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Items       []T      `json:"items"`
	Indices     []int    `json:"indices"`
	Centroid    string   `json:"centroid,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Size        int      `json:"size"`
}

// ClusterResult contains the results of clustering
type ClusterResult[T any] struct {
	Clusters       []ClusterInfo[T] `json:"clusters"`
	Outliers       []T              `json:"outliers,omitempty"`
	OutlierIndices []int            `json:"outlier_indices,omitempty"`
	TotalItems     int              `json:"total_items"`
	NumClusters    int              `json:"num_clusters"`
	Quality        float64          `json:"quality,omitempty"`
	Metadata       map[string]any   `json:"metadata,omitempty"`
}

// Cluster groups similar items semantically without predefined categories.
// Uses LLM to understand semantic similarity and create meaningful groupings.
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
	log := logger.GetLogger()
	log.Debug("Starting cluster operation", "itemCount", len(items))

	var result ClusterResult[T]
	result.TotalItems = len(items)
	result.Metadata = make(map[string]any)

	if len(items) == 0 {
		return result, nil
	}

	if len(items) == 1 {
		result.Clusters = []ClusterInfo[T]{{
			Name:    "Single Item",
			Items:   items,
			Indices: []int{0},
			Size:    1,
		}}
		result.NumClusters = 1
		return result, nil
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert items to JSON
	itemsJSON := make([]string, len(items))
	for i, item := range items {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			log.Error("Cluster operation failed: marshal error", "itemIndex", i, "error", err)
			return result, fmt.Errorf("failed to marshal item %d: %w", i, err)
		}
		itemsJSON[i] = fmt.Sprintf("[%d] %s", i, string(itemJSON))
	}

	clusterConstraint := ""
	if opts.NumClusters > 0 {
		clusterConstraint = fmt.Sprintf("Create exactly %d clusters.", opts.NumClusters)
	} else {
		clusterConstraint = fmt.Sprintf("Automatically determine the optimal number of clusters (minimum cluster size: %d).", opts.MinClusterSize)
	}

	clusterCriteria := "semantic similarity"
	if opts.ClusterBy != "" {
		clusterCriteria = opts.ClusterBy
	}

	sizeLimit := ""
	if opts.MaxClusterSize > 0 {
		sizeLimit = fmt.Sprintf("\nNo cluster may contain more than %d items; split a cluster that would exceed it.", opts.MaxClusterSize)
	}

	descriptionNote := "\nLeave the description empty; the cluster name is enough."
	if opts.GenerateDescriptions {
		descriptionNote = "\nGive every cluster a description saying what its members have in common."
	}

	outlierHandling := ""
	if opts.IncludeOutliers {
		outlierHandling = "Place items that don't fit well into any cluster in an 'outliers' group."
	} else {
		outlierHandling = "Force all items into the nearest cluster, even if not a perfect fit."
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

	userPrompt := fmt.Sprintf("Cluster these items:\n\n%s", strings.Join(itemsJSON, "\n"))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Cluster operation LLM call failed", "error", err)
		return result, fmt.Errorf("clustering failed: %w", err)
	}

	// Parse the response
	var parsed struct {
		Clusters []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Indices     []int    `json:"indices"`
			Keywords    []string `json:"keywords"`
		} `json:"clusters"`
		OutlierIndices []int   `json:"outlier_indices"`
		Quality        float64 `json:"quality"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Cluster operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse clustering result: %w", err)
	}

	// Build cluster result
	for _, c := range parsed.Clusters {
		cluster := ClusterInfo[T]{
			Name:        c.Name,
			Description: c.Description,
			Indices:     c.Indices,
			Keywords:    c.Keywords,
			Items:       make([]T, 0, len(c.Indices)),
			Size:        len(c.Indices),
		}

		for _, idx := range c.Indices {
			if idx >= 0 && idx < len(items) {
				cluster.Items = append(cluster.Items, items[idx])
			}
		}

		result.Clusters = append(result.Clusters, cluster)
	}

	// Handle outliers
	for _, idx := range parsed.OutlierIndices {
		if idx >= 0 && idx < len(items) {
			result.Outliers = append(result.Outliers, items[idx])
			result.OutlierIndices = append(result.OutlierIndices, idx)
		}
	}

	result.NumClusters = len(result.Clusters)
	result.Quality = parsed.Quality

	log.Debug("Cluster operation succeeded", "numClusters", result.NumClusters, "outlierCount", len(result.Outliers))
	return result, nil
}

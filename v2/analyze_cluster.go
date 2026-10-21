package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ClusterOptions are Cluster's settings. The zero value is the default: the model
// groups by similarity of meaning and decides how many groups there are, a group
// may hold one item, there is no upper limit, names are chosen by the model,
// items have to be 0.7 alike to share a group, items that fit nowhere go in a
// separate outlier group, and each group gets a description.
//
// The common settings of Options come first and are set without naming the
// embedded type: ClusterOptions{By: "root cause", MaxClusterSize: 20}.
type ClusterOptions struct {
	Options

	// By says what to group on, in plain language. It is sent as a quoted value.
	// Empty means similarity of meaning.
	By string

	// NumClusters is the number of groups aimed at. It is a target: the model is
	// asked for it and Go does not check the answer against it. Zero means the
	// model decides. A negative value is a configuration error.
	NumClusters int

	// MinClusterSize is the fewest items a group may hold. It is enforced: an
	// answer with a smaller group is an error of kind ErrInvalidAnswer, except for
	// the outlier group, which is the leftover and is exempt. Zero means 1. A
	// negative value is a configuration error.
	MinClusterSize int

	// MaxClusterSize is the most items a group may hold. It is enforced on every
	// group. Zero means no limit. A negative value is a configuration error.
	MaxClusterSize int

	// NamingStrategy is "auto", "descriptive" or "numbered". Empty means "auto".
	// Any other value is a configuration error. It is a request for a style of
	// name, and Go does not check the names.
	NamingStrategy string

	// SimilarityThreshold is how alike items must be to share a group, from 0 to
	// 1. It is a request in the prompt, and Go does not check the answer against
	// it. Zero means 0.7, and a negative value means 0. A value above 1, or one
	// that is not a number, is a configuration error.
	SimilarityThreshold float64

	// NoOutlierGroup withholds the separate group for items that fit nowhere, and
	// an answer that sets any item aside is an error of kind ErrInvalidAnswer.
	// False allows one.
	NoOutlierGroup bool

	// OmitDescriptions asks the model to leave the group descriptions empty
	// instead of writing them, and each Description comes back empty unless the
	// model wrote one anyway. False asks for them.
	OmitDescriptions bool
}

// ClusterResult is the grouping of the items given to Cluster. Every item is in
// exactly one of Clusters and Outliers.
type ClusterResult[T any] struct {
	// Clusters are the groups, in the order the model gave them. Nil for no items.
	Clusters []ClusterInfo[T] `json:"clusters"`

	// Outliers are the items in no group, as the caller gave them. Nil when there
	// are none.
	Outliers []T `json:"outliers,omitempty"`

	// OutlierIndices are the positions of Outliers in the input, in the same
	// order.
	OutlierIndices []int `json:"outlier_indices,omitempty"`

	// TotalItems is the number of items given.
	TotalItems int `json:"total_items"`

	// NumClusters is the number of groups, len(Clusters). Outliers are not
	// counted: they are not a group.
	NumClusters int `json:"num_clusters"`

	// Quality is the model's own claim about its grouping, from 0 to 1. It is not
	// a measurement and is not calibrated; an answer without one, or off that
	// scale, is refused. It is zero when no model was asked, which is the case for
	// fewer than two items.
	Quality float64 `json:"quality,omitempty"`
}

// ClusterInfo is one group of items.
type ClusterInfo[T any] struct {
	// Name is the model's name for the group. One item on its own is a group
	// named "Single Item", written by the library.
	Name string `json:"name"`

	// Description says what the members have in common. Empty when the model gave
	// none, as it is asked to when OmitDescriptions is set.
	Description string `json:"description,omitempty"`

	// Items are the members, as the caller gave them, in the order of Indices.
	Items []T `json:"items"`

	// Indices are the positions of Items in the input.
	Indices []int `json:"indices"`

	// Keywords are words the model gave for the group. Nil when it gave none.
	Keywords []string `json:"keywords,omitempty"`

	// Size is the number of members, len(Items), counted here and not reported by
	// the model.
	Size int `json:"size"`
}

// mergeClusterOptions merges the values of a call (M0).
func mergeClusterOptions(opts []ClusterOptions) ClusterOptions {
	return mergeValues(opts, func(o, later ClusterOptions) ClusterOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.By, later.By)
		lay(&o.NumClusters, later.NumClusters)
		lay(&o.MinClusterSize, later.MinClusterSize)
		lay(&o.MaxClusterSize, later.MaxClusterSize)
		lay(&o.NamingStrategy, later.NamingStrategy)
		lay(&o.SimilarityThreshold, later.SimilarityThreshold)
		lay(&o.NoOutlierGroup, later.NoOutlierGroup)
		lay(&o.OmitDescriptions, later.OmitDescriptions)
		return o
	})
}

// checkClusterOptions returns the internal error of a setting no call can use,
// or nil. Each number is checked on the caller's own value, before the meaning
// of its zero and of a negative threshold is applied.
func checkClusterOptions(o ClusterOptions) error {
	if err := o.common().Check("cluster"); err != nil {
		return err
	}
	if err := checkCount("cluster", "NumClusters", o.NumClusters); err != nil {
		return err
	}
	if err := checkCount("cluster", "MinClusterSize", o.MinClusterSize); err != nil {
		return err
	}
	if err := checkCount("cluster", "MaxClusterSize", o.MaxClusterSize); err != nil {
		return err
	}
	if err := checkFloat("cluster", "SimilarityThreshold", o.SimilarityThreshold, math.Inf(-1), 1); err != nil {
		return err
	}
	switch o.NamingStrategy {
	case "", "auto", "descriptive", "numbered":
		return nil
	}
	return types.Newf(types.KindConfiguration, "cluster", "NamingStrategy must be auto, descriptive or numbered")
}

// clusterInternal builds the internal options of a Cluster call on ctx. It starts
// from the constructor's value, so a field left at its zero keeps the default.
// A negative threshold is the caller's way of saying 0, which the internal call
// takes as a number and never sees as a negative.
func clusterInternal(ctx context.Context, o ClusterOptions) ops.ClusterOptions {
	opts := ops.NewClusterOptions()
	lay(&opts.ClusterBy, o.By)
	lay(&opts.NumClusters, o.NumClusters)
	lay(&opts.MinClusterSize, o.MinClusterSize)
	lay(&opts.MaxClusterSize, o.MaxClusterSize)
	lay(&opts.NamingStrategy, o.NamingStrategy)
	switch {
	case o.SimilarityThreshold > 0:
		opts.SimilarityThreshold = o.SimilarityThreshold
	case o.SimilarityThreshold < 0:
		opts.SimilarityThreshold = 0
	}
	turnOff(&opts.IncludeOutliers, o.NoOutlierGroup)
	turnOff(&opts.GenerateDescriptions, o.OmitDescriptions)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// clusterResult is the result of the internal call as the root declares it, for
// the items the call was given. The zero result, which the internal call returns
// beside an error, converts to the zero result.
//
// The lists the internal call built for the answer are handed on as they are,
// except one: for a single item it answers with the caller's own slice as the
// group's Items, and a result that shared storage with the input would let a
// caller who changes it change the input, so that one is copied.
func clusterResult[T any](res ops.ClusterResult[T], items []T) ClusterResult[T] {
	out := ClusterResult[T]{
		Outliers: res.Outliers, OutlierIndices: res.OutlierIndices,
		TotalItems: res.TotalItems, NumClusters: res.NumClusters, Quality: res.Quality,
	}
	if len(res.Clusters) == 0 {
		return out
	}
	out.Clusters = make([]ClusterInfo[T], len(res.Clusters))
	for i, c := range res.Clusters {
		members := c.Items
		if len(members) > 0 && len(items) > 0 && &members[0] == &items[0] {
			members = append([]T(nil), members...)
		}
		out.Clusters[i] = ClusterInfo[T]{
			Name: c.Name, Description: c.Description, Items: members,
			Indices: c.Indices, Keywords: c.Keywords, Size: c.Size,
		}
	}
	return out
}

// Cluster groups items that belong together. Every item lands in exactly one
// group or among the outliers: an answer that loses an item, repeats one, names a
// position that is not in the list, or breaks MinClusterSize or MaxClusterSize is
// an error of kind ErrInvalidAnswer. NumClusters, NamingStrategy and
// SimilarityThreshold are requests to the model that Go does not check. Default
// tier Fast, mode Transform.
//
//	groups, err := schemaflux.Cluster(ctx, tickets)
//	groups, err := schemaflux.Cluster(ctx, tickets, schemaflux.ClusterOptions{By: "root cause", MaxClusterSize: 20})
//	for _, g := range groups.Clusters { fmt.Println(g.Name, g.Size) }
//
// No items, and one item, are answered without a model: no items is an empty
// result and one item is one group of its own. Both still add a Record, with no
// request. The options are checked first, whatever the number of items.
//
// The Record of the call is "cluster", delivered at ContractJSONWellFormed: the
// answer is parsed and checked as a partition, but no schema is declared to the
// provider.
func Cluster[T any](ctx context.Context, items []T, opts ...ClusterOptions) (ClusterResult[T], error) {
	var zero ClusterResult[T]
	call, err := bind.Begin(ctx, "cluster")
	if err != nil {
		return zero, err
	}
	o := mergeClusterOptions(opts)
	if err := checkClusterOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Cluster(items, clusterInternal(call.Context(), o))
		return clusterResult(res, items), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ClusterResult[T], error) {
		return ops.Cluster(items, clusterInternal(ctx, o))
	})
	return clusterResult(res, items), err
}

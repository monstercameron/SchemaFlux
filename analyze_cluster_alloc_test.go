package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

var allocClusterItems = []string{"hello", "hi there", "invoice 4"}

const (
	// allocClusterGroups places two items in one group and sets one aside, so the
	// result has a group list to convert and an outlier list that is handed on.
	allocClusterGroups = `{"clusters":[{"name":"greetings","description":"d","indices":[0,1],"keywords":["hello"]}],"outlier_indices":[2],"quality":0.8}`

	// allocClusterOutliersOnly sets every item aside: there is no group list.
	allocClusterOutliersOnly = `{"clusters":[],"outlier_indices":[0,1,2],"quality":0.2}`
)

func TestMergeClusterOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeClusterOptions)
}

func TestClusterAllocatesAsTheInternalCallWhenThereIsNoGroupToConvert(t *testing.T) {
	cases := map[string]ClusterOptions{
		"no options":           {},
		"one options value":    {By: "topic", NumClusters: 2, MinClusterSize: 2, MaxClusterSize: 5, NamingStrategy: "numbered", SimilarityThreshold: -1, NoOutlierGroup: false, OmitDescriptions: true},
		"every common setting": {Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocClusterOutliersOnly,
				call: func(ctx context.Context) error {
					_, err := Cluster(ctx, allocClusterItems, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Cluster(allocClusterItems, clusterInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestClusterAllocatesAsTheInternalCallForNoItems(t *testing.T) {
	requireAllocsPlusConversions(t, allocCase{
		reply: allocClusterGroups,
		call: func(ctx context.Context) error {
			_, err := Cluster[string](ctx, nil)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Cluster[string](nil, clusterInternal(ctx, ClusterOptions{}))
			return err
		},
	}, 0, false)
}

func TestClusterWithGroupsAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	o := ClusterOptions{By: "greeting", Options: allocEveryCommonSetting}
	// The group list is converted into the root's own ClusterInfo, which has no
	// Centroid, in one allocation however many groups there are. That is the one
	// allocation more, and it cannot be avoided: the internal list is of another
	// type. The outliers and their positions, and each group's items, indices and
	// keywords, are handed on as the internal call built them, and so allocate
	// nothing here.
	requireAllocsPlusConversions(t, allocCase{
		reply: allocClusterGroups,
		call: func(ctx context.Context) error {
			_, err := Cluster(ctx, allocClusterItems, o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Cluster(allocClusterItems, clusterInternal(ctx, o))
			return err
		},
	}, 1, true)
}

func TestClusterOfOneItemAllocatesTwoMoreThanTheInternalCall(t *testing.T) {
	one := []string{"alone"}
	// The group list is converted (one allocation), and the group's Items, which
	// the internal call answers with the caller's own slice, are copied so that the
	// result does not share storage with the input (one more).
	requireAllocsPlusConversions(t, allocCase{
		reply: allocClusterGroups,
		call: func(ctx context.Context) error {
			_, err := Cluster(ctx, one)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Cluster(one, clusterInternal(ctx, ClusterOptions{}))
			return err
		},
	}, 2, false)
}

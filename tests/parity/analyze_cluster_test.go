package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

const (
	// clusterParityGroups puts two of three items in a group and sets one aside.
	clusterParityGroups = `{"clusters":[{"name":"a","description":"first","indices":[0,1],"keywords":["x"]}],"outlier_indices":[2],"quality":0.8}`

	// clusterParityAllPlaced sets none aside, which a call that allows no outliers needs.
	clusterParityAllPlaced = `{"clusters":[{"name":"a","description":"first","indices":[0,1,2],"keywords":["x"]}],"outlier_indices":[],"quality":0.8}`
)

// clusterCase is one Cluster pair on the items given, answered with the answer
// given: the old root's options and v2's.
func clusterCase(name, answer string, items []string, old oldroot.ClusterOptions, v2 schemaflux.ClusterOptions) parityCase {
	return parityCase{name: "Cluster/" + name, op: "cluster", replies: []string{answer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Cluster(items, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Cluster(ctx, items, v2)
			return err
		}}
}

func clusterCases() []parityCase {
	three := []string{"one", "two", "three"}
	base := oldroot.NewClusterOptions
	groups := func(name string, old oldroot.ClusterOptions, v2 schemaflux.ClusterOptions) parityCase {
		return clusterCase(name, clusterParityGroups, three, old, v2)
	}
	cases := []parityCase{
		groups("the default", base(), schemaflux.ClusterOptions{}),
		groups("By", base().WithClusterBy("topic"), schemaflux.ClusterOptions{By: "topic"}),
		groups("NumClusters", base().WithNumClusters(2), schemaflux.ClusterOptions{NumClusters: 2}),
		groups("MinClusterSize", base().WithMinClusterSize(2), schemaflux.ClusterOptions{MinClusterSize: 2}),
		groups("MinClusterSize 1, the default written out", base().WithMinClusterSize(1), schemaflux.ClusterOptions{MinClusterSize: 1}),
		groups("MaxClusterSize", base().WithMaxClusterSize(4), schemaflux.ClusterOptions{MaxClusterSize: 4}),
		groups("NamingStrategy descriptive", base().WithNamingStrategy("descriptive"), schemaflux.ClusterOptions{NamingStrategy: "descriptive"}),
		groups("NamingStrategy numbered", base().WithNamingStrategy("numbered"), schemaflux.ClusterOptions{NamingStrategy: "numbered"}),
		groups("NamingStrategy auto, the default written out", base().WithNamingStrategy("auto"), schemaflux.ClusterOptions{NamingStrategy: "auto"}),
		groups("SimilarityThreshold", base().WithSimilarityThreshold(.4), schemaflux.ClusterOptions{SimilarityThreshold: .4}),
		groups("SimilarityThreshold 0.7, the default written out", base().WithSimilarityThreshold(.7), schemaflux.ClusterOptions{SimilarityThreshold: .7}),
		groups("SimilarityThreshold 1", base().WithSimilarityThreshold(1), schemaflux.ClusterOptions{SimilarityThreshold: 1}),
		// A negative threshold means none, which the old root says as 0, a number it takes as it is.
		groups("SimilarityThreshold negative is 0", base().WithSimilarityThreshold(0), schemaflux.ClusterOptions{SimilarityThreshold: -1}),
		groups("SimilarityThreshold barely negative is 0", base().WithSimilarityThreshold(0), schemaflux.ClusterOptions{SimilarityThreshold: -0.001}),
		clusterCase("NoOutlierGroup", clusterParityAllPlaced, three, base().WithIncludeOutliers(false), schemaflux.ClusterOptions{NoOutlierGroup: true}),
		groups("OmitDescriptions", base().WithGenerateDescriptions(false), schemaflux.ClusterOptions{OmitDescriptions: true}),
		clusterCase("two items", `{"clusters":[{"name":"a","indices":[0,1],"keywords":[]}],"outlier_indices":[],"quality":0.5}`, three[:2],
			base(), schemaflux.ClusterOptions{}),
		clusterCase("every own setting", clusterParityAllPlaced, three,
			base().WithClusterBy("topic").WithNumClusters(2).WithMinClusterSize(2).WithMaxClusterSize(4).WithNamingStrategy("descriptive").
				WithSimilarityThreshold(.4).WithIncludeOutliers(false).WithGenerateDescriptions(false),
			schemaflux.ClusterOptions{By: "topic", NumClusters: 2, MinClusterSize: 2, MaxClusterSize: 4, NamingStrategy: "descriptive",
				SimilarityThreshold: .4, NoOutlierGroup: true, OmitDescriptions: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, groups(c.name, old, schemaflux.ClusterOptions{Options: c.v2}))
	}
	return cases
}

func TestClusterSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, clusterCases()) }

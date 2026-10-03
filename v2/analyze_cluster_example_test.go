package schemaflux_test

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleCluster() {
	tickets := []string{"refund please", "charged twice", "cannot log in", "password reset loop", "what are your hours"}
	var model schemafluxtest.Provider
	model.Reply(`{"clusters":[` +
		`{"name":"billing","description":"money that moved the wrong way","indices":[0,1],"keywords":["refund","charge"]},` +
		`{"name":"access","description":"people locked out","indices":[2,3],"keywords":["login","password"]}],` +
		`"outlier_indices":[4],"quality":0.85}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	groups, err := schemaflux.Cluster(ctx, tickets, schemaflux.ClusterOptions{By: "what the customer needs", MaxClusterSize: 20})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, g := range groups.Clusters {
		fmt.Println(g.Name, g.Size, g.Items)
	}
	fmt.Println("outliers:", groups.Outliers, groups.OutlierIndices)

	// An answer that loses an item is an error and not a grouping.
	model.Reply(`{"clusters":[{"name":"billing","description":"d","indices":[0,1],"keywords":[]}],"outlier_indices":[],"quality":0.5}`)
	_, err = schemaflux.Cluster(ctx, tickets)
	fmt.Println(errors.Is(err, schemaflux.ErrInvalidAnswer))
	// Output:
	// billing 2 [refund please charged twice]
	// access 2 [cannot log in password reset loop]
	// outliers: [what are your hours] [4]
	// true
}

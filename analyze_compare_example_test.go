package schemaflux_test

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleCompare() {
	type plan struct {
		Name  string
		Price int
	}
	var model schemafluxtest.Provider
	model.Reply(`{"similarity_score":0.7,"similarities":[{"aspect":"coverage","description":"both cover the basics"}],` +
		`"differences":[{"aspect":"price","description":"the pro plan costs more","severity":"moderate"}],` +
		`"verdict":"alike in coverage, apart in price","aspect_scores":{"price":0.3,"risk":0.9}}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Compare(ctx, plan{"basic", 10}, plan{"pro", 25},
		schemaflux.CompareOptions{Aspects: []string{"price", "risk"}, FocusOn: "both"})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("%.1f %s\n", res.SimilarityScore, res.Verdict)
	for _, d := range res.Differences {
		fmt.Println(d.Severity, d.Aspect+":", d.Description)
	}
	fmt.Println(res.AspectScores["price"], res.AspectScores["risk"])

	// An answer that leaves out an aspect that was asked for is refused.
	model.Reply(`{"similarity_score":0.7,"similarities":[],"differences":[],"verdict":"alike","aspect_scores":{"price":0.3}}`)
	_, err = schemaflux.Compare(ctx, plan{"basic", 10}, plan{"pro", 25}, schemaflux.CompareOptions{Aspects: []string{"price", "risk"}})
	fmt.Println(errors.Is(err, schemaflux.ErrInvalidAnswer))
	// Output:
	// 0.7 alike in coverage, apart in price
	// moderate price: the pro plan costs more
	// 0.3 0.9
	// true
}

package schemaflux_test

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func ExampleDiff() {
	type account struct {
		Name      string
		Seats     int
		UpdatedAt string
	}
	var model schemafluxtest.Provider
	model.Reply("The account was renamed and gained four seats.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	before := account{Name: "acme", Seats: 5, UpdatedAt: "monday"}
	after := account{Name: "acme corp", Seats: 9, UpdatedAt: "friday"}

	// The comparison is made in Go; the model only writes the summary of it.
	d, err := schemaflux.Diff(ctx, before, after, schemaflux.DiffOptions{IgnoreFields: []string{"UpdatedAt"}})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, c := range d.Modified {
		fmt.Println(c.Field, c.OldValue, "->", c.NewValue)
	}
	fmt.Println(d.Summary)

	// When the model cannot be reached the comparison is still returned, and
	// SummaryError says why there is no summary.
	var down schemafluxtest.Provider
	down.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Err: errors.New("no route")})
	d, err = schemaflux.Diff(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &down}), before, after)
	fmt.Println(len(d.Modified), d.Summary == "", errors.Is(d.SummaryError, schemaflux.ErrUnavailable), err)
	// Output:
	// Name acme -> acme corp
	// Seats 5 -> 9
	// The account was renamed and gained four seats.
	// 3 true true <nil>
}

package schemaflux

import (
	"context"
	"testing"
)

// Whether and RouteBy are their two halves and nothing else, so what they
// allocate is what the halves allocate: Whether Is and Branch, and RouteBy Which
// and Select with the two slices it makes before it asks anything: the labels and
// the routes as the internal check reads them. The measurement is of the plain
// state, under a client and on the default client.

func TestWhetherAllocatesAsIsAndBranchTogether(t *testing.T) {
	then := func(context.Context) (int, error) { return 1, nil }
	otherwise := func(context.Context) (int, error) { return 2, nil }
	model := &allocProvider{reply: `{"q1":true}`}
	whether := func(ctx context.Context) error {
		_, err := Whether(ctx, allocSubject, "Is this message spam?", then, otherwise)
		return err
	}
	halves := func(ctx context.Context) error {
		yes, err := Is(ctx, allocSubject, "Is this message spam?")
		if err != nil {
			return err
		}
		_, err = Branch(ctx, yes, then, otherwise)
		return err
	}
	requireSameAllocations(t, model, whether, halves, 0)
}

func TestRouteByAllocatesAsWhichAndSelectTogetherAndItsTwoSlices(t *testing.T) {
	run := func(context.Context) (int, error) { return 1, nil }
	routes := []Route[int]{To("billing", run), To("technical", run), To("shipping", run)}
	labels := []string{"billing", "technical", "shipping"}
	model := &allocProvider{reply: `{"q1":2}`}
	routeBy := func(ctx context.Context) error {
		_, err := RouteBy(ctx, allocSubject, allocQuestion, routes)
		return err
	}
	halves := func(ctx context.Context) error {
		label, err := Which(ctx, allocSubject, allocQuestion, labels)
		if err != nil {
			return err
		}
		_, err = Select(ctx, label, routes...)
		return err
	}
	requireSameAllocations(t, model, routeBy, halves, 2)
}

// requireSameAllocations holds the allocations of combined, under a client and on
// the default client, to those of the two halves it is made of plus extra, which
// is what the combined function allocates itself.
func requireSameAllocations(t *testing.T, model *allocProvider, combined, halves func(ctx context.Context) error, extra float64) {
	t.Helper()
	client := WithClient(context.Background(), Client{Provider: model})
	if err := combined(client); err != nil {
		t.Fatal(err)
	}
	if err := halves(client); err != nil {
		t.Fatal(err)
	}
	got := testing.AllocsPerRun(allocRuns, func() { _ = combined(client) })
	want := testing.AllocsPerRun(allocRuns, func() { _ = halves(client) })
	t.Logf("under WithClient: %v allocations, the halves %v", got, want)
	if got != want+extra {
		t.Errorf("under a client the function allocates %v times, want the halves' %v plus %v", got, want, extra)
	}

	previous := Default()
	SetDefault(Client{Provider: model})
	t.Cleanup(func() { SetDefault(previous) })
	background := context.Background()
	got = testing.AllocsPerRun(allocRuns, func() { _ = combined(background) })
	want = testing.AllocsPerRun(allocRuns, func() { _ = halves(background) })
	t.Logf("on the default client: %v allocations, the halves %v", got, want)
	if got != want+extra {
		t.Errorf("on the default client the function allocates %v times, want the halves' %v plus %v", got, want, extra)
	}
}

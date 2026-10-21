package flow_test

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// wrappers are the combinators of this package and of the root, each as a step
// that runs the step it is given inside it and hands back that step's failure.
// Whatever order they are nested in, the error that comes out at the top is the
// value the innermost step returned.
var wrappers = []struct {
	name string
	wrap func(inner schemaflux.Step[string]) schemaflux.Step[string]
}{
	{"Branch", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) { return schemaflux.Branch(ctx, true, inner, returns("")) }
	}},
	{"Select", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			return schemaflux.Select(ctx, "a", schemaflux.To("a", inner), schemaflux.To("b", returns("b")))
		}
	}},
	{"Gather", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			got, err := schemaflux.Gather(ctx, inner, wait(time.Hour, "slow"))
			if err != nil {
				return "", err
			}
			return got[0], nil
		}
	}},
	{"Gather2", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			got, _, err := schemaflux.Gather2(ctx, inner, wait(time.Hour, 1))
			return got, err
		}
	}},
	{"Each", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			got, err := schemaflux.Each(ctx, []int{1}, func(ctx context.Context, _ int) (string, error) { return inner(ctx) })
			if err != nil {
				return "", err
			}
			return got[0], nil
		}
	}},
	{"BranchAhead", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			return flow.BranchAhead(ctx, returns(true), inner, returns(""))
		}
	}},
	{"BranchBoth", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			return flow.BranchBoth(ctx, returns(false), returns(""), inner)
		}
	}},
	{"RouteAhead", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			return flow.RouteAhead(ctx, returns("b"), "a", schemaflux.To("a", returns("a")), schemaflux.To("b", inner))
		}
	}},
	{"Escalate", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			got, _, err := flow.Escalate(ctx, inner, inner, nil)
			return got, err
		}
	}},
	{"Until", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			got, _, err := flow.Until(ctx, inner, func(string) bool { return true }, 2)
			return got, err
		}
	}},
	{"Vote", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			got, _, err := flow.Vote(ctx, 2, inner, flow.ExactAgreement[string](1), flow.VoteOptions{Concurrency: 1})
			return got, err
		}
	}},
	{"Checkpoint", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			var store flow.MemoryStore
			got, _, err := flow.Checkpoint(ctx, &store, "run", "step", 1, inner)
			return got, err
		}
	}},
	{"Approve", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			return flow.Approve(ctx, inner, func(context.Context, string) (flow.ApprovalOutcome, error) {
				return flow.ApprovalOutcome{Approved: true}, nil
			}, nil)
		}
	}},
	{"MapReduce", func(inner schemaflux.Step[string]) schemaflux.Step[string] {
		return func(ctx context.Context) (string, error) {
			got, _, err := flow.MapReduce(ctx, []int{1}, func(ctx context.Context, _ []int) (string, error) { return inner(ctx) })
			if err != nil {
				return "", err
			}
			return got[0], nil
		}
	}},
}

func TestEveryCombinatorReturnsACallersErrorAsItWasGivenWhateverItIsNestedIn(t *testing.T) {
	budget := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Op: "extract"}
	for _, failure := range []error{errStep, budget} {
		for _, inner := range wrappers {
			for _, outer := range wrappers {
				t.Run(fmt.Sprintf("%v inside %s inside %s", failure, inner.name, outer.name), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						step := outer.wrap(inner.wrap(fails[string](failure)))
						got, err := step(context.Background())
						if err != failure || got != "" {
							t.Errorf("err = %v (%T), got = %q; want the innermost step's error, the very value", err, err, got)
						}
					})
				})
			}
		}
	}
}

func TestEveryCombinatorPassesTheValueOfTheInnermostStepUp(t *testing.T) {
	for _, inner := range wrappers {
		for _, outer := range wrappers {
			t.Run(fmt.Sprintf("%s inside %s", inner.name, outer.name), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					step := outer.wrap(inner.wrap(returns("the value")))
					got, err := step(context.Background())
					if err != nil || got != "the value" {
						t.Errorf("got = %q, err = %v", got, err)
					}
				})
			})
		}
	}
}

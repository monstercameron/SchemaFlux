package flow_test

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/monstercameron/schemaflux/v2/flow"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func TestMatchAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	act := func() {}
	cases := []struct {
		name  string
		cases []flow.Case
		reply string
		asks  int     // how many requests the model is sent
		extra float64 // the copy of the list of cases, which the first eight need none for
	}{
		{"one string condition", []flow.Case{flow.When("a billing question", act)}, "true", 1, 0},
		{"a string condition that is false", []flow.Case{flow.When("a billing question", act)}, "false", 1, 0},
		{"three string conditions", []flow.Case{flow.When("a", act), flow.When("b", act), flow.Otherwise(act)}, "false", 2, 0},
		{"a Go condition", []flow.Case{flow.When(reflect.TypeFor[string](), act)}, "true", 0, 0},
		{"the default alone", []flow.Case{flow.Otherwise(act)}, "true", 0, 0},
		{"no cases", nil, "true", 0, 0},
		{"eight cases", manyCases(8, act), "false", 8, 0},
		{"nine cases", manyCases(9, act), "false", 9, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			internal := make([]types.Case, len(tc.cases))
			for i, c := range tc.cases {
				internal[i] = types.Case(c)
			}
			requireAllocsLikeTheInternalCall(t, plainAllocCase{
				reply: tc.reply,
				call: func(ctx context.Context) error {
					_, err := flow.Match(ctx, "an invoice", tc.cases...)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Match(ctx, "an invoice", internal...)
					return err
				},
				extra:    tc.extra,
				requests: tc.asks,
			})
		})
	}
}

// manyCases is n cases that are judged by a model and never hold.
func manyCases(n int, act func()) []flow.Case {
	list := make([]flow.Case, n)
	for i := range list {
		list[i] = flow.When("a string condition "+strconv.Itoa(i), act)
	}
	return list
}

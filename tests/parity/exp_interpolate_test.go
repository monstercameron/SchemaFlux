package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type interpolateParityPoint struct {
	Value int `json:"value"`
}

const expInterpolateAnswer = `{"complete":[{"value":1},{"value":2},{"value":3}],"filled":[{"index":1,"method":"linear","based_on":[0,2],"reasoning":"between","confidence":0.8}],"gap_count":1,"method":"linear","average_confidence":0.8}`

func expInterpolateCase(name string, old oldroot.InterpolateOptions, current exp.InterpolateOptions) parityCase {
	items := []interpolateParityPoint{{1}, {0}, {3}}
	return parityCase{name: "Interpolate/" + name, op: "exp.interpolate", replies: []string{expInterpolateAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.Interpolate[interpolateParityPoint](items, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := exp.Interpolate[interpolateParityPoint](ctx, items, current)
			return err
		},
	}
}

func TestExpInterpolateSendsTheRequestTheOldRootSends(t *testing.T) {
	base := oldroot.InterpolateOptions{}
	cases := []parityCase{
		expInterpolateCase("default", base, exp.InterpolateOptions{}),
		expInterpolateCase("Method", oldroot.InterpolateOptions{Method: "linear"}, exp.InterpolateOptions{Method: "linear"}),
		expInterpolateCase("GapIndices", oldroot.InterpolateOptions{GapIndices: []int{1}}, exp.InterpolateOptions{GapIndices: []int{1}}),
		expInterpolateCase("SequenceField", oldroot.InterpolateOptions{SequenceField: "day"}, exp.InterpolateOptions{SequenceField: "day"}),
		expInterpolateCase("ContextWindow", oldroot.InterpolateOptions{ContextWindow: 2}, exp.InterpolateOptions{ContextWindow: 2}),
		expInterpolateCase("Constraints", oldroot.InterpolateOptions{Constraints: []string{"positive"}}, exp.InterpolateOptions{Constraints: []string{"positive"}}),
	}
	for _, common := range commonCases {
		old := base
		if common.v2.Model != "" {
			old.Model = common.v2.Model
		}
		if common.v2.Tier != 0 {
			old.Intelligence = types.Speed(common.v2.Tier)
		}
		if common.v2.Effort != "" {
			old.Effort = types.Effort(common.v2.Effort)
		}
		if common.v2.Mode != 0 {
			old.Mode = types.Mode(common.v2.Mode)
		}
		if common.v2.Steering != "" {
			old.Steering = common.v2.Steering
		}
		old.MaxOutputTokens = common.v2.MaxOutputTokens
		old.WebSearch = common.v2.WebSearch
		cases = append(cases, expInterpolateCase(common.name, old, exp.InterpolateOptions{Options: common.v2}))
	}
	runParity(t, cases)
}

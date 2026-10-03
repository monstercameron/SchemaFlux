package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type resolveParityTarget struct {
	Name string `json:"name"`
}

const expResolveAnswer = `{"resolved":{"name":"merged"},"conflicts":[],"source_contributions":{"0":["name"],"1":[]},"confidence":0.75}`

func expResolveCase(name string, old oldroot.ResolveOptions, v2 exp.ResolveOptions) parityCase {
	sources := []resolveParityTarget{{"first"}, {"second"}}
	return parityCase{name: "Resolve/" + name, op: "exp.resolve", replies: []string{expResolveAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.Resolve(sources, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := exp.Resolve(ctx, sources, v2)
			return err
		},
	}
}

func TestExpResolveSendsTheRequestTheOldRootSends(t *testing.T) {
	base := func() oldroot.ResolveOptions {
		return oldroot.ResolveOptions{Strategy: "most-complete", ConflictThreshold: 0.8, Mode: types.TransformMode, Intelligence: types.Fast}
	}
	priorities := map[string]int{"name": 1}
	majority := base()
	majority.Strategy = "majority"
	authoritative := base()
	authoritative.Strategy, authoritative.AuthoritativeSource = "authoritative", 1
	fieldPriorities := base()
	fieldPriorities.FieldPriorities = priorities
	threshold := base()
	threshold.ConflictThreshold = 0.4
	cases := []parityCase{
		expResolveCase("default", base(), exp.ResolveOptions{}),
		expResolveCase("Strategy", majority, exp.ResolveOptions{Strategy: "majority"}),
		expResolveCase("AuthoritativeSource", authoritative, exp.ResolveOptions{Strategy: "authoritative", AuthoritativeSource: 1}),
		expResolveCase("FieldPriorities", fieldPriorities, exp.ResolveOptions{FieldPriorities: priorities}),
		expResolveCase("ConflictThreshold", threshold, exp.ResolveOptions{ConflictThreshold: 0.4}),
	}
	for _, common := range commonCases {
		old := base()
		var settings ops.CommonOptions
		var embedded types.OpOptions
		common.flat(&settings, &embedded)
		old.Model, old.Intelligence, old.Effort, old.Mode, old.Steering = settings.Model, settings.Intelligence, settings.Effort, settings.Mode, settings.Steering
		if embedded.Model != "" {
			old.Model = embedded.Model
		}
		if embedded.Intelligence != 0 {
			old.Intelligence = embedded.Intelligence
		}
		if embedded.Effort != "" {
			old.Effort = embedded.Effort
		}
		if embedded.Mode != 0 {
			old.Mode = embedded.Mode
		}
		if embedded.Steering != "" {
			old.Steering = embedded.Steering
		}
		if embedded.MaxOutputTokens != 0 {
			old.MaxOutputTokens = embedded.MaxOutputTokens
		}
		if embedded.WebSearch {
			old.WebSearch = true
		}
		cases = append(cases, expResolveCase(common.name, old, exp.ResolveOptions{Options: common.v2}))
	}
	runParity(t, cases)
}

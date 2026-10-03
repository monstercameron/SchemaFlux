package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type enrichParityTarget struct {
	Name    string `json:"name"`
	Segment string `json:"segment"`
}

const expEnrichAnswer = `{"enriched":{"name":"Ada","segment":"research"},"added_fields":["segment"],"confidence":{},"derivations":{}}`

func enrichParityCase(name string, old oldroot.EnrichOptions, v2 exp.EnrichOptions) parityCase {
	in := map[string]string{"name": "Ada"}
	return parityCase{name: "Enrich/" + name, op: "exp.enrich", replies: []string{expEnrichAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Enrich[map[string]string, enrichParityTarget](in, old)
			return err
		},
		v2: func(ctx context.Context) error { _, err := exp.Enrich[enrichParityTarget](ctx, in, v2); return err }}
}

func TestExpEnrichParity(t *testing.T) {
	b := oldroot.NewEnrichOptions
	cases := []parityCase{
		enrichParityCase("default", b(), exp.EnrichOptions{}),
		enrichParityCase("fields", b().WithDeriveFields([]string{"segment"}), exp.EnrichOptions{Fields: []string{"segment"}}),
		enrichParityCase("rules", b().WithDerivationRules(map[string]string{"segment": "role"}), exp.EnrichOptions{Rules: map[string]string{"segment": "role"}}),
		enrichParityCase("background", b().WithContext(map[string]any{"team": "x"}), exp.EnrichOptions{Background: map[string]any{"team": "x"}}),
		enrichParityCase("confidence", b().WithIncludeConfidence(true), exp.EnrichOptions{IncludeConfidence: true}),
		enrichParityCase("allow changes", b().WithAddOnly(false), exp.EnrichOptions{AllowChanges: true}),
		enrichParityCase("excluded", b().WithExcludeFields([]string{"email"}), exp.EnrichOptions{ExcludeFields: []string{"email"}}),
		enrichParityCase("domain", b().WithDomain("research"), exp.EnrichOptions{Domain: "research"}),
	}
	for _, c := range commonCases {
		old := b()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, enrichParityCase(c.name, old, exp.EnrichOptions{Options: c.v2}))
	}
	runParity(t, cases)
}

package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type round8Record struct {
	Name  string `json:"name"`
	Total int    `json:"total"`
}

// Every operation whose options are one flat struct is called here with a
// pinned model and an effort, and what reaches the call is checked.
//
// The test this replaces as the guard read the source: it counted request
// literals that carry the model and merges that copy it, and passed with
// SettleAdversarial building a request from options its own merge had never
// copied the model or the effort into. Reading the request is the check.
func TestFlatOptionsCarryTheModelAndTheEffortToTheCall(t *testing.T) {
	const model = "a-pinned-model"
	effort := types.EffortHigh

	record := round8Record{Name: "a", Total: 1}
	cases := []struct {
		name string
		run  func(ctx context.Context)
	}{
		{"Arbitrate", func(ctx context.Context) {
			_, _ = Arbitrate([]round8Record{record, {Name: "b"}}, ArbitrateOptions{Context: ctx, Rules: []string{"prefer the larger total"}, Model: model, Effort: effort})
		}},
		{"Audit", func(ctx context.Context) {
			_, _ = Audit(record, AuditOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"AuditWithModel", func(ctx context.Context) {
			_, _ = AuditWithModel(record, AuditOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Assemble", func(ctx context.Context) {
			_, _ = Assemble[round8Record]([]any{record, "more"}, ComposeOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Conform", func(ctx context.Context) {
			_, _ = Conform(record, "ISO", ConformOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Derive", func(ctx context.Context) {
			_, _ = Derive[round8Record, round8Record](record, DeriveOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Interpolate", func(ctx context.Context) {
			_, _ = Interpolate([]round8Record{record, {}, {Name: "c", Total: 3}}, InterpolateOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Pivot", func(ctx context.Context) {
			_, _ = Pivot[[]round8Record, round8Record]([]round8Record{record}, PivotOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Project", func(ctx context.Context) {
			_, _ = Project[round8Record, round8Record](record, ProjectOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Resolve", func(ctx context.Context) {
			_, _ = Resolve([]round8Record{record, {Name: "b", Total: 2}}, ResolveOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"Settle", func(ctx context.Context) {
			_, _ = Settle[salaryTerms](map[string]any{"max_budget": "low"}, NegotiateOptions{Context: ctx, Model: model, Effort: effort})
		}},
		{"SettleAdversarial", func(ctx context.Context) {
			_, _ = SettleAdversarial[salaryTerms](AdversarialContext[salaryTerms]{
				Ours:   AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 160000}},
				Theirs: AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000}},
			}, AdversarialOptions{Context: ctx, Model: model, Effort: effort})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []types.OpOptions
			previousCaller, previousProvider := currentHooks()
			setLLMCaller(func(_ context.Context, _, _ string, opts types.OpOptions) (string, error) {
				seen = append(seen, opts)
				return "", errors.New("stop here")
			})
			t.Cleanup(func() {
				setLLMCaller(previousCaller)
				SetDefaultProvider(previousProvider)
			})

			tc.run(context.Background())
			if len(seen) == 0 {
				t.Fatal("the operation made no call")
			}
			for _, opts := range seen {
				if opts.Model != model {
					t.Errorf("the call named model %q, want the pinned %q", opts.Model, model)
				}
				if opts.Effort != effort {
					t.Errorf("the call carried effort %q, want %q", opts.Effort, effort)
				}
			}
		})
	}
}

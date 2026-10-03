package parity

import (
	"context"
	"testing"

	legacy "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type conformParityValue struct {
	Name string `json:"name"`
}

const conformParityAnswer = `{"conformed":{"name":"A"},"adjustments":[],"violations":[],"compliance":1,"standard":"USPS"}`

func TestExpConformParity(t *testing.T) {
	replies := []string{conformParityAnswer}
	cases := []parityCase{
		{name: "default", op: "exp.conform", replies: replies, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				_, err := legacy.Conform[conformParityValue](conformParityValue{}, "USPS", legacy.ConformOptions{Context: ctx})
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := exp.Conform[conformParityValue](ctx, conformParityValue{}, "USPS")
				return err
			}},
		{name: "strict", op: "exp.conform", replies: replies, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				_, err := legacy.Conform[conformParityValue](conformParityValue{}, "USPS", legacy.ConformOptions{Context: ctx, Strict: true})
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := exp.Conform[conformParityValue](ctx, conformParityValue{}, "USPS", exp.ConformOptions{Strict: true})
				return err
			}},
		{name: "drop unknown", op: "exp.conform", replies: replies, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				_, err := legacy.Conform[conformParityValue](conformParityValue{}, "USPS", legacy.ConformOptions{Context: ctx, PreserveUnknown: false})
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := exp.Conform[conformParityValue](ctx, conformParityValue{}, "USPS", exp.ConformOptions{DropUnknown: true})
				return err
			}},
		{name: "custom rules", op: "exp.conform", replies: replies, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				_, err := legacy.Conform[conformParityValue](conformParityValue{}, "USPS", legacy.ConformOptions{Context: ctx, CustomRules: map[string]string{"zip": "five digits"}})
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := exp.Conform[conformParityValue](ctx, conformParityValue{}, "USPS", exp.ConformOptions{CustomRules: map[string]string{"zip": "five digits"}})
				return err
			}},
	}
	common := []struct {
		name   string
		old    legacy.ConformOptions
		modern schemaflux.Options
	}{
		{"model", legacy.ConformOptions{Model: pinnedModel}, schemaflux.Options{Model: pinnedModel}},
		{"tier", legacy.ConformOptions{Intelligence: types.Quick}, schemaflux.Options{Tier: schemaflux.TierQuick}},
		{"effort", legacy.ConformOptions{Effort: types.EffortLow}, schemaflux.Options{Effort: schemaflux.EffortLow}},
		{"mode", legacy.ConformOptions{Mode: types.Creative}, schemaflux.Options{Mode: schemaflux.ModeCreative}},
		{"steering", legacy.ConformOptions{Steering: "concise"}, schemaflux.Options{Steering: "concise"}},
		{"max output tokens", legacy.ConformOptions{MaxOutputTokens: 222}, schemaflux.Options{MaxOutputTokens: 222}},
		{"web search", legacy.ConformOptions{WebSearch: true}, schemaflux.Options{WebSearch: true}},
	}
	for _, setting := range common {
		setting := setting
		cases = append(cases, parityCase{name: setting.name, op: "exp.conform", replies: replies, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				options := setting.old
				options.Context = ctx
				_, err := legacy.Conform[conformParityValue](conformParityValue{}, "USPS", options)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := exp.Conform[conformParityValue](ctx, conformParityValue{}, "USPS", exp.ConformOptions{Options: setting.modern})
				return err
			}})
	}
	runParity(t, cases)
}

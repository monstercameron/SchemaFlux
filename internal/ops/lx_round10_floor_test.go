package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 10. Every function that returns a Result states the contract
// level it establishes, and a floor it cannot reach is refused before anything
// is sent.
//
// The round before put the check in all seven and left the level unset in six,
// so those six were refused under any floor at all, after the provider had
// answered and been paid.
func TestEveryResultFormIsHeldToTheContractFloor(t *testing.T) {
	const textReply = `{"text": "a short text", "key_points": [], "changes_made": [], "tone_achieved": "neutral", "source_language_detected": "English", "added_content": [], "confidence": 0.9}`
	forms := []struct {
		name        string
		reply       string
		establishes types.ContractLevel
		run         func(ctx context.Context) (types.Meta, error)
	}{
		{"ExtractResult", `{"name":"a","total":1}`, requestedContractFor(NewExtractOptions()), func(ctx context.Context) (types.Meta, error) {
			opts := NewExtractOptions()
			opts.CommonOptions.Context = ctx
			result, err := ExtractResult[round8Record]("a, 1", opts)
			return result.Meta, err
		}},
		{"SummarizeResult", textReply, types.ContractJSONWellFormed, func(ctx context.Context) (types.Meta, error) {
			opts := NewSummarizeOptions()
			opts.CommonOptions.Context = ctx
			result, err := SummarizeResult("a long report", opts)
			return result.Meta, err
		}},
		{"RewriteResult", textReply, types.ContractJSONWellFormed, func(ctx context.Context) (types.Meta, error) {
			opts := NewRewriteOptions()
			opts.CommonOptions.Context = ctx
			result, err := RewriteResult("some text", opts)
			return result.Meta, err
		}},
		{"TranslateResult", textReply, types.ContractJSONWellFormed, func(ctx context.Context) (types.Meta, error) {
			opts := NewTranslateOptions().WithTargetLanguage("French")
			opts.CommonOptions.Context = ctx
			result, err := TranslateResult("translated", opts)
			return result.Meta, err
		}},
		{"ExpandResult", textReply, types.ContractJSONWellFormed, func(ctx context.Context) (types.Meta, error) {
			opts := NewExpandOptions()
			opts.CommonOptions.Context = ctx
			result, err := ExpandResult("text", opts)
			return result.Meta, err
		}},
		{"SortResult", `{"ids":["i-000002","i-000001"]}`, types.ContractJSONWellFormed, func(ctx context.Context) (types.Meta, error) {
			opts := NewSortOptions().WithCriteria("alphabetical")
			opts.CommonOptions.Context = ctx
			result, err := SortResult([]string{"b", "a"}, opts)
			return result.Meta, err
		}},
	}
	floors := []types.ContractLevel{
		types.ContractPromptOnly,
		types.ContractJSONWellFormed,
		types.ContractSchemaConstrained,
		types.ContractSchemaAndInvariantChecked,
		types.ContractEvidenceChecked,
		types.ContractFullyGoverned,
	}
	homes := []struct {
		name string
		with func(ctx context.Context, floor types.ContractLevel) context.Context
	}{
		{"on the client", func(ctx context.Context, floor types.ContractLevel) context.Context {
			return WithExecConfig(ctx, &ExecConfig{DataPolicyDeclared: true, DataPolicy: types.DataPolicy{
				AllowedProviders: []string{"local"}, AllowedRegions: []string{"eu"}, MinimumContract: floor}})
		}},
		{"on the call", func(ctx context.Context, floor types.ContractLevel) context.Context {
			return WithContractPolicy(ctx, types.DataPolicy{MinimumContract: floor})
		}},
	}

	for _, form := range forms {
		for _, floor := range floors {
			for _, home := range homes {
				t.Run(form.name+", a floor of "+floor.String()+" "+home.name, func(t *testing.T) {
					llm.ResetCapabilityRegistryForTest()
					t.Cleanup(llm.ResetCapabilityRegistryForTest)
					provider := &captureProvider{resp: llm.CompletionResponse{Content: form.reply, FinishReason: "stop", Provider: "local"}}
					llm.DeclareRoute(provider.Name(), "gpt-6-luna", []string{"eu"}, 0)
					configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")

					meta, err := form.run(home.with(WithProvider(context.Background(), provider), floor))
					if form.establishes < floor {
						if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrContractDegraded) {
							t.Fatalf("err = %v, want the floor's refusal", err)
						}
						if provider.attempts != 0 {
							t.Errorf("the provider was called %d times for a call that could never meet the floor", provider.attempts)
						}
						return
					}
					if err != nil {
						t.Fatalf("the call was refused under a floor it meets: %v", err)
					}
					if meta.DeliveredContract != form.establishes || meta.RequestedContract != form.establishes {
						t.Errorf("the envelope says requested %s, delivered %s; this form establishes %s",
							meta.RequestedContract, meta.DeliveredContract, form.establishes)
					}
				})
			}
		}
	}

	// With no policy at all, nothing is refused and the level is still stated.
	for _, form := range forms {
		t.Run(form.name+", no policy", func(t *testing.T) {
			provider := &captureProvider{resp: llm.CompletionResponse{Content: form.reply, FinishReason: "stop"}}
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-6-luna")
			meta, err := form.run(WithProvider(context.Background(), provider))
			if err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			if meta.DeliveredContract != form.establishes {
				t.Errorf("delivered %s, want %s", meta.DeliveredContract, form.establishes)
			}
		})
	}
}

// checkContractFloor on its own.
func TestCheckContractFloor(t *testing.T) {
	client := func(floor types.ContractLevel) context.Context {
		return WithExecConfig(context.Background(), &ExecConfig{DataPolicyDeclared: true, DataPolicy: types.DataPolicy{MinimumContract: floor}})
	}
	cases := []struct {
		name        string
		ctx         context.Context
		establishes types.ContractLevel
		refused     bool
	}{
		{"no policy", context.Background(), types.ContractPromptOnly, false},
		{"a client that declared no policy", WithExecConfig(context.Background(), &ExecConfig{DataPolicy: types.DataPolicy{MinimumContract: types.ContractFullyGoverned}}), types.ContractPromptOnly, false},
		{"no floor", client(types.ContractPromptOnly), types.ContractPromptOnly, false},
		{"below the floor", client(types.ContractSchemaConstrained), types.ContractJSONWellFormed, true},
		{"at the floor", client(types.ContractSchemaConstrained), types.ContractSchemaConstrained, false},
		{"above the floor", client(types.ContractSchemaConstrained), types.ContractFullyGoverned, false},
		{"one below the floor", client(types.ContractFullyGoverned), types.ContractEvidenceChecked, true},
		{"prompt only under the lowest real floor", client(types.ContractJSONWellFormed), types.ContractPromptOnly, true},
		{"a floor on the call", WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: types.ContractSchemaConstrained}), types.ContractJSONWellFormed, true},
		{"a looser floor on the call than on the client", WithContractPolicy(client(types.ContractSchemaConstrained), types.DataPolicy{MinimumContract: types.ContractJSONWellFormed}), types.ContractJSONWellFormed, true},
		{"a stricter floor on the call than on the client", WithContractPolicy(client(types.ContractJSONWellFormed), types.DataPolicy{MinimumContract: types.ContractSchemaConstrained}), types.ContractJSONWellFormed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkContractFloor(tc.ctx, tc.establishes)
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if err != nil && (!errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrContractDegraded)) {
				t.Errorf("the refusal is %v", err)
			}
			if exported := CheckContractFloor(tc.ctx, tc.establishes); (exported != nil) != tc.refused {
				t.Errorf("the exported form disagrees: %v", exported)
			}
		})
	}
}

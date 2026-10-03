package fluent

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Review round 10. The builders' RunResult builds its envelope here and not in
// the operations package, and it asked nothing about a contract floor: a
// client's floor held for ExtractResult and for nothing reached this way.
//
// This path establishes no contract level it can show, so under any floor
// above prompt-only the call is refused before it is made.
//
// Round 11: a builder's sort is the exception. It built this same best-effort
// envelope and so was refused under any floor, for a sort that ops.SortResult,
// the same sort, was allowed. It returns ops.SortResult's envelope now, and
// meets the floors a sort meets.
func TestRunResultIsHeldToTheContractFloor(t *testing.T) {
	builders := []struct {
		name        string
		body        string
		establishes types.ContractLevel
		run         func(ctx context.Context) error
	}{
		{"Transforming", `{"name":"x"}`, types.ContractPromptOnly, func(ctx context.Context) error {
			_, err := Transforming[reachTarget, reachTarget](reachTarget{Name: "in"}).RunResult(ctx)
			return err
		}},
		{"Generating", `{"name":"x"}`, types.ContractPromptOnly, func(ctx context.Context) error {
			_, err := Generating[reachTarget]("make something").RunResult(ctx)
			return err
		}},
		{"Choosing", `{"selected_index":0,"confidence":0.9}`, types.ContractPromptOnly, func(ctx context.Context) error {
			_, err := Choosing([]string{"a", "b"}).By("the better one").RunResult(ctx)
			return err
		}},
		{"Filtering", `{"ids":["i-000001"]}`, types.ContractPromptOnly, func(ctx context.Context) error {
			_, err := Filtering([]string{"a", "b"}).By("the good ones").RunResult(ctx)
			return err
		}},
		// A sort sends no schema, so it establishes well-formed JSON and no
		// more: a floor above that refuses it before anything is sent.
		{"Sorting", `{"ids":["i-000001","i-000002"]}`, types.ContractJSONWellFormed, func(ctx context.Context) error {
			_, err := Sorting([]string{"a", "b"}).By("alphabetically").RunResult(ctx)
			return err
		}},
	}
	onClient := func(level types.ContractLevel) func() context.Context {
		return func() context.Context {
			return ops.WithExecConfig(context.Background(), &ops.ExecConfig{DataPolicyDeclared: true, DataPolicy: types.DataPolicy{
				AllowedProviders: []string{"local"}, AllowedRegions: []string{"eu"}, MinimumContract: level}})
		}
	}
	on := func(level types.ContractLevel) func() context.Context {
		return func() context.Context {
			return ops.WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: level})
		}
	}
	floors := []struct {
		name  string
		ctx   func() context.Context
		level types.ContractLevel
	}{
		{"no policy", context.Background, types.ContractPromptOnly},
		{"a floor of prompt-only on the call", on(types.ContractPromptOnly), types.ContractPromptOnly},
		{"a floor of well-formed JSON on the call", on(types.ContractJSONWellFormed), types.ContractJSONWellFormed},
		{"a floor of schema-constrained on the call", on(types.ContractSchemaConstrained), types.ContractSchemaConstrained},
		{"a floor of invariants checked on the call", on(types.ContractSchemaAndInvariantChecked), types.ContractSchemaAndInvariantChecked},
		{"a floor of evidence checked on the call", on(types.ContractEvidenceChecked), types.ContractEvidenceChecked},
		{"the highest floor on the call", on(types.ContractFullyGoverned), types.ContractFullyGoverned},
		// The same floors set on a client, which is where a floor is most
		// often set.
		{"no floor on the client", onClient(types.ContractPromptOnly), types.ContractPromptOnly},
		{"a floor of well-formed JSON on the client", onClient(types.ContractJSONWellFormed), types.ContractJSONWellFormed},
		{"a floor of invariants checked on the client", onClient(types.ContractSchemaAndInvariantChecked), types.ContractSchemaAndInvariantChecked},
		{"a floor of evidence checked on the client", onClient(types.ContractEvidenceChecked), types.ContractEvidenceChecked},
	}
	for _, builder := range builders {
		for _, floor := range floors {
			t.Run(builder.name+", "+floor.name, func(t *testing.T) {
				llm.ResetCapabilityRegistryForTest()
				t.Cleanup(llm.ResetCapabilityRegistryForTest)
				llm.RegisterCapabilityFamily("local", "", llm.ProviderCapabilities{Regions: []string{"eu"}})
				provider := &countingProvider{body: builder.body}
				restore := installStubProvider(t, provider)
				defer restore()

				err := builder.run(floor.ctx())
				if builder.establishes >= floor.level {
					if errors.Is(err, types.ErrPolicyViolation) {
						t.Fatalf("refused with no floor to meet: %v", err)
					}
					if provider.calls.Load() == 0 {
						t.Fatal("the provider was not reached")
					}
					return
				}
				if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ops.ErrContractDegraded) {
					t.Fatalf("err = %v, want the floor's refusal", err)
				}
				if n := provider.calls.Load(); n != 0 {
					t.Errorf("the provider was called %d times for a call that was refused", n)
				}
			})
		}
	}
}

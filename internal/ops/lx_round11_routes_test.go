package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 11, the contract floor and the route an answer came by.

// lx11BatchOp is an operation with a batch form: the body is a list of words
// separated by commas, one for each item, and each word is an output.
func lx11BatchOp(contract OutputContract[string]) Op[string, string] {
	contract.Decode = func(body string) (string, error) { return body, nil }
	return Op[string, string]{
		ID:        types.OperationID{Name: "words", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract:  contract,
		Batch: BatchAlgebra[string, string]{
			Class: types.BatchItemwise,
			Encode: func(items []string, _ types.OpOptions) (string, string, error) {
				return "one word for each item", strings.Join(items, ","), nil
			},
			Merge: func(body string, items []string) ([]string, error) {
				words := strings.Split(body, ",")
				if len(words) != len(items) {
					return nil, errors.New("one word is wanted for each item")
				}
				return words, nil
			},
		},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) { return "one word", input },
	}
}

func lx11NotShouting(word string) error {
	if word != strings.ToLower(word) {
		return &types.OperationError{Kind: types.KindInvariantViolation, Op: "words", Message: "an output is in capitals"}
	}
	return nil
}

// A batch reported the level its operation declares, and the batch path ran
// neither the invariants nor the evidence check that level stands for. Under a
// floor of "invariants checked", outputs nobody had checked were returned.
//
// The invariants run on every output now. The evidence check needs one input
// beside one output and a batch has neither in that form, so a batch never
// reports that level, and a floor that asks for it is refused before anything
// is sent.
func TestABatchIsHeldToWhatItsLevelStandsFor(t *testing.T) {
	invariants := []func(string) error{lx11NotShouting}
	cases := []struct {
		name      string
		contract  OutputContract[string]
		reply     string
		floor     types.ContractLevel
		refusedAs error // the floor's refusal, before anything is sent
		failsAs   types.ErrorKind
		delivered types.ContractLevel
	}{
		{"no schema, no invariants", OutputContract[string]{}, lx12Words("a", "b"), types.ContractPromptOnly,
			nil, types.KindUnknown, types.ContractJSONWellFormed},
		{"a schema", OutputContract[string]{SchemaName: "words"}, lx12Words("a", "b"), types.ContractSchemaConstrained,
			nil, types.KindUnknown, types.ContractSchemaConstrained},
		{"invariants that hold", OutputContract[string]{SchemaName: "words", Invariants: invariants}, lx12Words("a", "b"), types.ContractSchemaAndInvariantChecked,
			nil, types.KindUnknown, types.ContractSchemaAndInvariantChecked},
		{"an invariant the second output breaks", OutputContract[string]{SchemaName: "words", Invariants: invariants}, lx12Words("a", "B"), types.ContractSchemaAndInvariantChecked,
			nil, types.KindInvariantViolation, 0},
		{"an invariant the first output breaks", OutputContract[string]{SchemaName: "words", Invariants: invariants}, lx12Words("A", "b"), types.ContractSchemaAndInvariantChecked,
			nil, types.KindInvariantViolation, 0},
		{"an invariant broken, and no floor", OutputContract[string]{SchemaName: "words", Invariants: invariants}, lx12Words("a", "B"), types.ContractPromptOnly,
			nil, types.KindInvariantViolation, 0},
		{"a floor of invariants checked, and none declared", OutputContract[string]{SchemaName: "words"}, lx12Words("a", "b"), types.ContractSchemaAndInvariantChecked,
			ErrContractDegraded, types.KindUnknown, 0},
		{"evidence required, a floor of evidence checked", OutputContract[string]{SchemaName: "words", EvidenceRequired: true}, lx12Words("a", "b"), types.ContractEvidenceChecked,
			ErrContractDegraded, types.KindUnknown, 0},
		{"an evidence policy, a floor of evidence checked", OutputContract[string]{SchemaName: "words", Invariants: invariants, EvidencePolicy: types.EvidenceAllModelDerived}, lx12Words("a", "b"), types.ContractEvidenceChecked,
			ErrContractDegraded, types.KindUnknown, 0},
		// A batch does not carry the evidence an operation requires, so it is refused
		// before anything is sent and the operation is asked one item at a time.
		{"evidence required, a floor a batch can meet", OutputContract[string]{SchemaName: "words", Invariants: invariants, EvidenceRequired: true}, lx12Words("a", "b"), types.ContractSchemaAndInvariantChecked,
			nil, types.KindConfiguration, 0},
		{"evidence required and no invariants, no floor", OutputContract[string]{SchemaName: "words", EvidenceRequired: true}, lx12Words("a", "b"), types.ContractPromptOnly,
			nil, types.KindConfiguration, 0},
		{"a floor of fully governed", OutputContract[string]{SchemaName: "words", Invariants: invariants}, lx12Words("a", "b"), types.ContractFullyGoverned,
			ErrContractDegraded, types.KindUnknown, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &captureProvider{resp: llm.CompletionResponse{Content: tc.reply, FinishReason: "stop"}}
			ctx := WithContractPolicy(WithProvider(context.Background(), offeringIDsProvider{provider}), types.DataPolicy{MinimumContract: tc.floor})

			// The levels above well formed are delivered on a route that enforces
			// the schema the request sends.
			opt := routeThatEnforcesSchemas(t, provider, "gpt-6-luna", types.OpOptions{})
			result, err := RunOpBatchResult(ctx, lx12WordsOp(tc.contract), []string{"x", "y"}, opt)
			switch {
			case tc.refusedAs != nil:
				if !errors.Is(err, tc.refusedAs) || !errors.Is(err, types.ErrPolicyViolation) {
					t.Fatalf("err = %v, want the floor's refusal", err)
				}
				if provider.attempts != 0 {
					t.Errorf("the provider was called %d times for a batch that could never meet the floor", provider.attempts)
				}
			case tc.failsAs != types.KindUnknown:
				if types.KindOf(err) != tc.failsAs {
					t.Fatalf("err = %v, want %v", err, tc.failsAs)
				}
			default:
				if err != nil {
					t.Fatalf("RunOpBatchResult: %v", err)
				}
				if result.Meta.DeliveredContract != tc.delivered {
					t.Errorf("delivered %s, want %s", result.Meta.DeliveredContract, tc.delivered)
				}
				if len(result.Value) != 2 {
					t.Errorf("Value = %v", result.Value)
				}
			}
			if err != nil && result.Value != nil {
				t.Errorf("outputs came back beside an error: %v", result.Value)
			}
		})
	}

	// RunOpBatch on its own holds every output to the invariants too.
	previousCaller, _ := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) { return "a,b,C", nil })
	t.Cleanup(func() { setLLMCaller(previousCaller) })
	outputs, err := RunOpBatch(context.Background(), lx11BatchOp(OutputContract[string]{Invariants: invariants}),
		[]string{"x", "y", "z"}, types.OpOptions{})
	if types.KindOf(err) != types.KindInvariantViolation || outputs != nil {
		t.Errorf("outputs %v, err %v", outputs, err)
	}
	if err != nil && strings.Contains(err.Error(), "a,b,C") {
		t.Errorf("the error carries the model's answer: %v", err)
	}
}

func TestBatchContractLevel(t *testing.T) {
	pass := []func(string) error{func(string) error { return nil }}
	cases := []struct {
		name     string
		contract OutputContract[string]
		want     types.ContractLevel
	}{
		{"nothing", OutputContract[string]{}, types.ContractJSONWellFormed},
		{"a schema", OutputContract[string]{SchemaName: "s"}, types.ContractSchemaConstrained},
		{"invariants and no schema", OutputContract[string]{Invariants: pass}, types.ContractJSONWellFormed},
		{"a schema and invariants", OutputContract[string]{SchemaName: "s", Invariants: pass}, types.ContractSchemaAndInvariantChecked},
		{"evidence required", OutputContract[string]{SchemaName: "s", EvidenceRequired: true}, types.ContractSchemaConstrained},
		{"evidence required, with invariants", OutputContract[string]{SchemaName: "s", Invariants: pass, EvidenceRequired: true}, types.ContractSchemaAndInvariantChecked},
		{"an evidence policy", OutputContract[string]{SchemaName: "s", EvidencePolicy: types.EvidenceMaterialFields}, types.ContractSchemaConstrained},
		{"an evidence policy, with invariants", OutputContract[string]{SchemaName: "s", Invariants: pass, EvidencePolicy: types.EvidenceNoInference}, types.ContractSchemaAndInvariantChecked},
		{"an evidence policy and no schema", OutputContract[string]{EvidencePolicy: types.EvidenceAllModelDerived}, types.ContractJSONWellFormed},
		{"evidence and invariants and no schema", OutputContract[string]{Invariants: pass, EvidenceRequired: true}, types.ContractJSONWellFormed},
	}
	for _, tc := range cases {
		// Round 12: the level is the operation's, since it depends on how the
		// batch is merged. These are batches on the id protocol.
		if got := batchContractLevel(lx12WordsOp(tc.contract)); got != tc.want {
			t.Errorf("%s: batchContractLevel = %s, want %s", tc.name, got, tc.want)
		}
		if got := batchContractLevel(lx12WordsOp(tc.contract)); got > declaredContractLevel(tc.contract) {
			t.Errorf("%s: a batch reports %s, above the %s its operation declares", tc.name, got, declaredContractLevel(tc.contract))
		}
	}
}

// RunOpResult asked about the floor only after the provider had answered and
// been paid. A floor its operation cannot reach is refused before anything is
// sent, as the other Result forms already were.
func TestRunOpResultIsRefusedBeforeItIsPaidFor(t *testing.T) {
	op := echoOp(func(string) error { return nil })
	declared := declaredContractLevel(op.Contract)
	floors := []types.ContractLevel{
		types.ContractPromptOnly, types.ContractJSONWellFormed, types.ContractSchemaConstrained,
		types.ContractSchemaAndInvariantChecked, types.ContractEvidenceChecked, types.ContractFullyGoverned,
	}
	homes := map[string]func(ctx context.Context, floor types.ContractLevel) context.Context{
		"on the call": func(ctx context.Context, floor types.ContractLevel) context.Context {
			return WithContractPolicy(ctx, types.DataPolicy{MinimumContract: floor})
		},
		"on the client": func(ctx context.Context, floor types.ContractLevel) context.Context {
			return WithExecConfig(ctx, &ExecConfig{DataPolicyDeclared: true, DataPolicy: types.DataPolicy{
				AllowedProviders: []string{"local"}, AllowedRegions: []string{"eu"}, MinimumContract: floor}})
		},
	}
	for _, floor := range floors {
		for home, with := range homes {
			t.Run("a floor of "+floor.String()+" "+home, func(t *testing.T) {
				llm.ResetCapabilityRegistryForTest()
				t.Cleanup(llm.ResetCapabilityRegistryForTest)
				provider := &captureProvider{resp: llm.CompletionResponse{Content: "word", FinishReason: "stop", Provider: "local"}}
				llm.DeclareRoute(provider.Name(), "gpt-6-luna", []string{"eu"}, 0)

				result, err := RunOpResult(with(WithProvider(context.Background(), provider), floor), op, "say a word", types.OpOptions{Model: "gpt-6-luna"})
				if declared < floor {
					if !errors.Is(err, ErrContractDegraded) || !errors.Is(err, types.ErrPolicyViolation) {
						t.Fatalf("err = %v, want the floor's refusal", err)
					}
					if provider.attempts != 0 {
						t.Errorf("the provider was called %d times for a call that could never meet the floor", provider.attempts)
					}
					if result.Value != "" {
						t.Errorf("a value came back beside the refusal: %q", result.Value)
					}
					return
				}
				if err != nil {
					t.Fatalf("refused under a floor it meets: %v", err)
				}
				if result.Meta.DeliveredContract != declared || result.Value != "word" {
					t.Errorf("delivered %s, value %q", result.Meta.DeliveredContract, result.Value)
				}
			})
		}
	}
}

// An answer is held to the client's data policy as the route it names.
//
// Two things were wrong. An answer under the provider that was asked was
// accepted whatever model it named, so a gateway that keeps one name and sends
// the request on to a model the policy forbids was never noticed. And an
// answer from another provider was checked under exactly the model it named,
// which is usually a dated snapshot nobody declares, so an alternate that had
// been checked, allowed and paid was then refused.
func TestAnAnswerIsHeldToTheRouteItNames(t *testing.T) {
	eu := []string{"eu"}
	policy := declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai", "anthropic"}, AllowedRegions: eu})
	cases := []struct {
		name     string
		ctx      context.Context
		provider string
		model    string
		refused  bool
	}{
		{"nothing named", policy, "", "", false},
		{"the route that was asked", policy, "openai", "gpt-6-luna", false},
		{"the same in other capitals", policy, "OpenAI", "GPT-6-Luna", false},
		{"a dated snapshot of the model asked", policy, "openai", "gpt-6-luna-2026-09-15", false},
		{"a snapshot dated without dashes, and no provider named", policy, "", "gpt-6-luna-20260915", false},
		{"a numbered version of the model asked", policy, "openai", "gpt-6-luna-001", false},
		{"the same provider, a model declared elsewhere", policy, "openai", "gpt-us", true},
		{"the same provider, a model nobody declared", policy, "openai", "gpt-undeclared", true},
		{"no provider named, a model declared elsewhere", policy, "", "gpt-us", true},
		{"the same provider, a longer name that is another model", policy, "openai", "gpt-6-luna-mini", true},
		{"the same provider, another model that is allowed", policy, "openai", "gpt-6-terra", false},
		{"another provider, a route that is allowed", policy, "anthropic", "claude-x", false},
		{"another provider, a snapshot of an allowed route", policy, "anthropic", "claude-x-20250929", false},
		{"another provider, a dated snapshot of an allowed route", policy, "anthropic", "claude-x-2025-09-29", false},
		{"another provider, and no model named", policy, "anthropic", "", true},
		{"another provider, a model nobody declared", policy, "anthropic", "claude-y", true},
		{"another provider, a snapshot of a model declared elsewhere", policy, "anthropic", "claude-us-20250929", true},
		{"a provider the policy does not allow", policy, "mistral", "claude-x", true},
		{"no policy: a route nobody declared", context.Background(), "mistral", "anything", false},
		{"no policy: another model", context.Background(), "openai", "gpt-us", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			llm.DeclareRoute("openai", "gpt-6-luna", eu, 0)
			llm.DeclareRoute("openai", "gpt-6-terra", eu, 0)
			llm.DeclareRoute("openai", "gpt-us", []string{"us"}, 0)
			llm.DeclareRoute("anthropic", "claude-x", eu, 0)
			llm.DeclareRoute("anthropic", "claude-us", []string{"us"}, 0)

			err := checkAnswerRoute(tc.ctx, &answersProvider{}, "gpt-6-luna",
				llm.CompletionResponse{Provider: tc.provider, Model: tc.model})
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if err == nil {
				return
			}
			if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrDataPolicyRefusal) {
				t.Errorf("the refusal is not classified as the policy's: %v", err)
			}
			var classified *types.OperationError
			if !errors.As(err, &classified) || classified.Provider == "" {
				t.Errorf("the refusal does not say where the request went: %v", err)
			}
			// A provider the policy does not allow is refused as that, and the
			// caller is not told to declare a route for it: no declaration
			// would make it allowed.
			if tc.provider == "mistral" && strings.Contains(err.Error(), "DeclareRoute") {
				t.Errorf("the refusal of a provider that is not allowed suggests declaring its route: %v", err)
			}
		})
	}

	// Through a call: the answer names a model of the same provider that the
	// policy forbids, and is refused once, without a retry.
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	llm.DeclareRoute("openai", "gpt-6-luna", eu, 0)
	llm.DeclareRoute("openai", "gpt-us", []string{"us"}, 0)
	provider := &answersProvider{responses: []llm.CompletionResponse{{
		Content: `{"ok":true}`, FinishReason: "stop", Model: "gpt-us", Provider: "openai",
	}}, retries: 3}
	if _, err := CallLLM(policy, provider, "system", "user", lx126Options); !errors.Is(err, types.ErrPolicyViolation) || provider.calls != 1 {
		t.Errorf("err %v after %d calls; want one call and a policy refusal", err, provider.calls)
	}
}

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 12, contract levels and the recovery cascade.

// lx12WordsOp is a batch operation built on the id protocol: the batch is
// answered as {"items":[{"id":...,"output":"word"}]}, every id has to be
// answered once, and every output is decoded on its own.
func lx12WordsOp(contract OutputContract[string]) Op[string, string] {
	contract.Decode = func(body string) (string, error) {
		var word string
		err := json.Unmarshal([]byte(body), &word)
		return word, err
	}
	codec := BatchItemCodec[string, string]{DecodeItem: func(raw json.RawMessage) (string, error) {
		var word string
		err := json.Unmarshal(raw, &word)
		return word, err
	}}
	return Op[string, string]{
		ID:          types.OperationID{Name: "words", Version: "v1"},
		Semantics:   types.Semantics{Stability: types.StabilityExperimental},
		Contract:    contract,
		Batch:       NewIDBatchAlgebra[string, string](types.BatchItemwise, types.AlgebraIndependent, "one word for each item", codec),
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) { return "one word", input },
	}
}

func lx12Words(words ...string) string {
	items := make([]string, len(words))
	for i, word := range words {
		encoded, _ := json.Marshal(word)
		items[i] = fmt.Sprintf(`{"id":%q,"output":%s}`, itemID(i), encoded)
	}
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

// A batch reports the level it checks, and no more. It reported "schema
// constrained" for an operation whose contract names a schema, whatever its
// merge did with the answer: a merge that splits the body on commas was
// delivered as well-formed JSON, schema constrained, and invariant checked.
// Only a batch built on the id protocol, which reads the answer as JSON,
// requires every id once and decodes each output, vouches for more than that
// the provider answered.
func TestABatchReportsOnlyTheLevelItChecks(t *testing.T) {
	invariants := []func(string) error{lx11NotShouting}
	levels := []struct {
		name string
		op   Op[string, string]
		want types.ContractLevel
	}{
		{"a merge of its own, no schema", lx11BatchOp(OutputContract[string]{}), types.ContractPromptOnly},
		{"a merge of its own, a schema named", lx11BatchOp(OutputContract[string]{SchemaName: "words"}), types.ContractPromptOnly},
		{"a merge of its own, with invariants", lx11BatchOp(OutputContract[string]{SchemaName: "words", Invariants: invariants}), types.ContractPromptOnly},
		{"a merge of its own, evidence required", lx11BatchOp(OutputContract[string]{SchemaName: "words", EvidenceRequired: true}), types.ContractPromptOnly},
		{"the id protocol, no schema", lx12WordsOp(OutputContract[string]{}), types.ContractJSONWellFormed},
		{"the id protocol, a schema named", lx12WordsOp(OutputContract[string]{SchemaName: "words"}), types.ContractSchemaConstrained},
		{"the id protocol, with invariants", lx12WordsOp(OutputContract[string]{SchemaName: "words", Invariants: invariants}), types.ContractSchemaAndInvariantChecked},
		{"the id protocol, invariants and no schema", lx12WordsOp(OutputContract[string]{Invariants: invariants}), types.ContractJSONWellFormed},
		{"the id protocol, evidence required", lx12WordsOp(OutputContract[string]{SchemaName: "words", EvidenceRequired: true}), types.ContractSchemaConstrained},
		{"the id protocol, evidence and invariants", lx12WordsOp(OutputContract[string]{SchemaName: "words", Invariants: invariants, EvidencePolicy: types.EvidenceAllModelDerived}), types.ContractSchemaAndInvariantChecked},
	}
	for _, tc := range levels {
		if got := batchContractLevel(tc.op); got != tc.want {
			t.Errorf("%s: batchContractLevel = %s, want %s", tc.name, got, tc.want)
		}
	}

	runs := []struct {
		name      string
		op        Op[string, string]
		reply     string
		floor     types.ContractLevel
		refused   bool
		fails     bool
		failsAs   types.ErrorKind
		delivered types.ContractLevel
	}{
		{"a merge of its own, no floor", lx11BatchOp(OutputContract[string]{SchemaName: "words", Invariants: invariants}), "a,b", types.ContractPromptOnly, false, false, types.KindUnknown, types.ContractPromptOnly},
		{"a merge of its own, a floor of well-formed JSON", lx11BatchOp(OutputContract[string]{SchemaName: "words"}), "a,b", types.ContractJSONWellFormed, true, false, types.KindUnknown, 0},
		{"a merge of its own, an invariant broken", lx11BatchOp(OutputContract[string]{Invariants: invariants}), "a,B", types.ContractPromptOnly, false, true, types.KindInvariantViolation, 0},
		{"the id protocol, a floor it meets", lx12WordsOp(OutputContract[string]{SchemaName: "words", Invariants: invariants}), lx12Words("a", "b"), types.ContractSchemaAndInvariantChecked, false, false, types.KindUnknown, types.ContractSchemaAndInvariantChecked},
		{"the id protocol, an invariant broken", lx12WordsOp(OutputContract[string]{SchemaName: "words", Invariants: invariants}), lx12Words("a", "B"), types.ContractSchemaAndInvariantChecked, false, true, types.KindInvariantViolation, 0},
		{"the id protocol, a floor of evidence checked", lx12WordsOp(OutputContract[string]{SchemaName: "words", EvidenceRequired: true}), lx12Words("a", "b"), types.ContractEvidenceChecked, true, false, types.KindUnknown, 0},
		{"the id protocol, an answer that is not JSON", lx12WordsOp(OutputContract[string]{SchemaName: "words"}), "a,b", types.ContractPromptOnly, false, true, types.KindUnknown, 0},
		{"the id protocol, an id missing", lx12WordsOp(OutputContract[string]{SchemaName: "words"}), lx12Words("a"), types.ContractPromptOnly, false, true, types.KindUnknown, 0},
		{"the id protocol, no schema, a floor of schema constrained", lx12WordsOp(OutputContract[string]{}), lx12Words("a", "b"), types.ContractSchemaConstrained, true, false, types.KindUnknown, 0},
		{"the id protocol, no schema, no floor", lx12WordsOp(OutputContract[string]{}), lx12Words("a", "b"), types.ContractPromptOnly, false, false, types.KindUnknown, types.ContractJSONWellFormed},
	}
	for _, tc := range runs {
		t.Run(tc.name, func(t *testing.T) {
			provider := &captureProvider{resp: llm.CompletionResponse{Content: tc.reply, FinishReason: "stop"}}
			ctx := WithContractPolicy(WithProvider(context.Background(), offeringIDsProvider{provider}), types.DataPolicy{MinimumContract: tc.floor})
			opt := routeThatEnforcesSchemas(t, provider, "gpt-6-luna", types.OpOptions{})
			result, err := RunOpBatchResult(ctx, tc.op, []string{"x", "y"}, opt)
			switch {
			case tc.refused:
				if !errors.Is(err, ErrContractDegraded) || provider.attempts != 0 {
					t.Fatalf("err %v after %d calls; want the floor's refusal before any", err, provider.attempts)
				}
			case tc.fails:
				if err == nil {
					t.Fatalf("the batch was accepted: %v", result.Value)
				}
				if tc.failsAs != types.KindUnknown && types.KindOf(err) != tc.failsAs {
					t.Errorf("err = %v, want %v", err, tc.failsAs)
				}
			default:
				if err != nil {
					t.Fatalf("RunOpBatchResult: %v", err)
				}
				if result.Meta.DeliveredContract != tc.delivered || result.Meta.RequestedContract != tc.delivered {
					t.Errorf("requested %s, delivered %s; want %s", result.Meta.RequestedContract, result.Meta.DeliveredContract, tc.delivered)
				}
			}
			if err != nil && result.Value != nil {
				t.Errorf("outputs came back beside an error: %v", result.Value)
			}
		})
	}
}

// A batch applies the contract's Normalize to each output, as a single call
// does, and an itemwise merge that returns the wrong number of outputs is
// caught here and not by whoever indexes them later.
func TestABatchNormalizesAndCountsItsOutputs(t *testing.T) {
	trimmed := OutputContract[string]{Normalize: strings.TrimSpace}
	provider := &captureProvider{resp: llm.CompletionResponse{Content: lx12Words(" a ", "b  "), FinishReason: "stop"}}
	outputs, err := RunOpBatch(WithProvider(context.Background(), offeringIDsProvider{provider}), lx12WordsOp(trimmed), []string{"x", "y"}, types.OpOptions{Model: "gpt-6-luna"})
	if err != nil || len(outputs) != 2 || outputs[0] != "a" || outputs[1] != "b" {
		t.Errorf("outputs %q, err %v; want them normalized", outputs, err)
	}

	short := lx11BatchOp(OutputContract[string]{})
	short.Batch.Merge = func(string, []string) ([]string, error) { return []string{"only one"}, nil }
	provider = &captureProvider{resp: llm.CompletionResponse{Content: "x", FinishReason: "stop"}}
	outputs, err = RunOpBatch(WithProvider(context.Background(), provider), short, []string{"x", "y"}, types.OpOptions{Model: "gpt-6-luna"})
	if err == nil || outputs != nil {
		t.Errorf("an itemwise batch of two came back with %d outputs and no error", len(outputs))
	}

	// An aggregate batch may return a different number: that is what its
	// validation of the whole is for.
	aggregate := short
	aggregate.Batch.Class = types.BatchAggregate
	if outputs, err := RunOpBatch(WithProvider(context.Background(), provider), aggregate, []string{"x", "y"}, types.OpOptions{Model: "gpt-6-luna"}); err != nil || len(outputs) != 1 {
		t.Errorf("an aggregate batch: %q, %v", outputs, err)
	}
}

// A call that declares a schema is delivered at "schema constrained" only on
// a route that enforces schemas. That was worked out after the provider had
// answered, so a floor the route could not meet was refused after it had been
// paid. The route is known before the call, and the floor is asked about it
// then.
func TestAFloorTheRouteCannotMeetIsRefusedBeforeTheCall(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
	op := echoOp(func(string) error { return nil })
	op.Contract.SchemaName = "echo"

	cases := []struct {
		name     string
		schema   map[string]any
		enforces bool // the route is registered as enforcing schemas
		floor    types.ContractLevel
		refused  bool
	}{
		{"no schema sent, a floor of schema constrained is refused: nothing enforced one", nil, false, types.ContractSchemaConstrained, true},
		{"no schema sent, a route that enforces, a floor of schema constrained is refused all the same", nil, true, types.ContractSchemaConstrained, true},
		{"no schema sent, a floor of well-formed JSON", nil, false, types.ContractJSONWellFormed, false},
		{"a schema, a route that enforces, a floor of schema constrained", schema, true, types.ContractSchemaConstrained, false},
		{"a schema, a route that enforces, a floor of invariants checked", schema, true, types.ContractSchemaAndInvariantChecked, false},
		{"a schema, a route that does not, a floor of schema constrained", schema, false, types.ContractSchemaConstrained, true},
		{"a schema, a route that does not, a floor of invariants checked", schema, false, types.ContractSchemaAndInvariantChecked, true},
		{"a schema, a route that does not, a floor of well-formed JSON", schema, false, types.ContractJSONWellFormed, false},
		{"a schema, a route that does not, no floor", schema, false, types.ContractPromptOnly, false},
		{"a schema, a route that enforces, a floor of evidence checked", schema, true, types.ContractEvidenceChecked, true},
		{"a schema, a route nobody registered, a floor of schema constrained", schema, false, types.ContractSchemaConstrained, true},
		{"no schema sent, a floor of evidence checked", nil, true, types.ContractEvidenceChecked, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			provider := &captureProvider{resp: llm.CompletionResponse{Content: "word", FinishReason: "stop", Provider: "local", Model: "gpt-6-luna"}}
			if tc.enforces {
				llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: provider.Name(), Model: "gpt-6-luna",
					Supports: map[llm.Capability]bool{llm.CapNativeJSONSchema: true}})
			}
			ctx := WithContractPolicy(WithProvider(context.Background(), offeringIDsProvider{provider}), types.DataPolicy{MinimumContract: tc.floor})
			result, err := RunOpResult(ctx, op, "say a word", types.OpOptions{Model: "gpt-6-luna", JSONSchema: tc.schema, ResponseFormat: "json"})
			if tc.refused {
				if !errors.Is(err, ErrContractDegraded) {
					t.Fatalf("err = %v, want the floor's refusal", err)
				}
				if provider.attempts != 0 {
					t.Errorf("the provider was called %d times for a call the route could never deliver", provider.attempts)
				}
				return
			}
			if err != nil {
				t.Fatalf("RunOpResult: %v", err)
			}
			if result.Meta.DeliveredContract < tc.floor {
				t.Errorf("delivered %s under a floor of %s", result.Meta.DeliveredContract, tc.floor)
			}
		})
	}
}

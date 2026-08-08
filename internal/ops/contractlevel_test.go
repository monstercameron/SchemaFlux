package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-004. Meta.RequestedContract/DeliveredContract (result.go) existed and
// nothing in this package ever set them. declaredContractLevel is the pure
// function; these tests exercise it directly across every combination its
// doc comment describes, then prove RunOpResult actually calls it.

func TestDeclaredContractLevel(t *testing.T) {
	cases := []struct {
		name     string
		contract OutputContract[string]
		want     types.ContractLevel
	}{
		{
			"no schema, no invariants, no evidence",
			OutputContract[string]{},
			types.ContractJSONWellFormed,
		},
		{
			"schema name declared",
			OutputContract[string]{SchemaName: "widget"},
			types.ContractSchemaConstrained,
		},
		{
			"schema plus one invariant",
			OutputContract[string]{SchemaName: "widget", Invariants: []func(string) error{func(string) error { return nil }}},
			types.ContractSchemaAndInvariantChecked,
		},
		{
			"invariants with no declared schema stay at JSON well-formed",
			OutputContract[string]{Invariants: []func(string) error{func(string) error { return nil }}},
			types.ContractJSONWellFormed,
		},
		{
			"schema plus multiple invariants",
			OutputContract[string]{SchemaName: "widget", Invariants: []func(string) error{
				func(string) error { return nil }, func(string) error { return nil },
			}},
			types.ContractSchemaAndInvariantChecked,
		},
		{
			"EvidenceRequired alone (no explicit policy) reaches evidence checked",
			OutputContract[string]{SchemaName: "widget", EvidenceRequired: true},
			types.ContractEvidenceChecked,
		},
		{
			"explicit EvidenceMaterialFields policy",
			OutputContract[string]{SchemaName: "widget", EvidencePolicy: types.EvidenceMaterialFields},
			types.ContractEvidenceChecked,
		},
		{
			"explicit EvidenceAllModelDerived policy",
			OutputContract[string]{SchemaName: "widget", EvidencePolicy: types.EvidenceAllModelDerived},
			types.ContractEvidenceChecked,
		},
		{
			"explicit EvidenceNoInference policy",
			OutputContract[string]{SchemaName: "widget", EvidencePolicy: types.EvidenceNoInference},
			types.ContractEvidenceChecked,
		},
		{
			"evidence policy set but EvidenceRequired false -- policy still governs",
			OutputContract[string]{SchemaName: "widget", EvidencePolicy: types.EvidenceAllModelDerived, EvidenceRequired: false},
			types.ContractEvidenceChecked,
		},
		{
			"schema, invariants, and evidence together reach the ceiling this path supports",
			OutputContract[string]{
				SchemaName:     "widget",
				Invariants:     []func(string) error{func(string) error { return nil }},
				EvidencePolicy: types.EvidenceAllModelDerived,
			},
			types.ContractEvidenceChecked,
		},
		{
			"evidence policy with no schema still reports evidence checked",
			OutputContract[string]{EvidencePolicy: types.EvidenceAllModelDerived},
			types.ContractEvidenceChecked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := declaredContractLevel(tc.contract)
			if got != tc.want {
				t.Errorf("declaredContractLevel() = %v, want %v", got, tc.want)
			}
		})
	}
}

// declaredContractLevel never returns ContractFullyGoverned: that level
// additionally requires capability negotiation and data-policy enforcement
// this Op-level path does not perform, and claiming it here would be
// exactly the "stronger than what was actually enforced" failure the whole
// milestone exists to close.
func TestDeclaredContractLevelNeverClaimsFullyGoverned(t *testing.T) {
	contract := OutputContract[string]{
		SchemaName:     "widget",
		Invariants:     []func(string) error{func(string) error { return nil }},
		EvidencePolicy: types.EvidenceNoInference,
	}
	if got := declaredContractLevel(contract); got == types.ContractFullyGoverned {
		t.Fatal("declaredContractLevel claimed FullyGoverned, which this path cannot verify")
	}
}

// RunOpResult wires declaredContractLevel into Meta on a success, and
// reports DeliveredContract at its zero value (ContractPromptOnly) on a
// failure -- nothing was actually delivered.
func TestRunOpResultReportsContractLevels(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "even", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := echoOp(func(value string) error {
		if value != "even" {
			return errFor(value)
		}
		return nil
	})
	op.Contract.SchemaName = "echo"

	result, err := RunOpResult(context.Background(), op, "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Meta.DeliveredContract != types.ContractSchemaAndInvariantChecked {
		t.Fatalf("DeliveredContract = %v, want ContractSchemaAndInvariantChecked", result.Meta.DeliveredContract)
	}
	if result.Meta.RequestedContract != result.Meta.DeliveredContract {
		t.Fatalf("RequestedContract = %v, DeliveredContract = %v; a success should not degrade at this layer",
			result.Meta.RequestedContract, result.Meta.DeliveredContract)
	}
	if result.Meta.Degraded() {
		t.Fatal("a successful call should not report itself as degraded")
	}
}

func TestRunOpResultReportsPromptOnlyDeliveredOnFailure(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "never-even", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := echoOp(func(string) error { return errFor("always fails") })
	op.Contract.SchemaName = "echo"

	result, err := RunOpResult(context.Background(), op, "x", types.OpOptions{})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if result.Meta.DeliveredContract != types.ContractPromptOnly {
		t.Fatalf("DeliveredContract = %v, want ContractPromptOnly on failure", result.Meta.DeliveredContract)
	}
	// The requested level is still worth reporting on a failure: it says
	// what the operation was engineered to deliver, which is exactly the
	// baseline Degraded() needs to be meaningful.
	if result.Meta.RequestedContract == types.ContractPromptOnly {
		t.Fatal("RequestedContract should still reflect the operation's own declared contract on a failure")
	}
	if !result.Meta.Degraded() {
		t.Fatal("a failed call delivered less than requested and should report itself as degraded")
	}
}

// errFor builds a plain error without pulling in fmt at the top of every
// test file that needs one.
func errFor(msg string) error { return &simpleErr{msg} }

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

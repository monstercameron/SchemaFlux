package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
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

// --- TC-004, the negotiation half: negotiatedContractLevel.

func TestNegotiatedContractLevel(t *testing.T) {
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)

	llm.RegisterCapabilities(llm.ProviderCapabilities{
		Provider: "confirmed", Model: "model-a",
		Supports: map[llm.Capability]bool{llm.CapNativeJSONSchema: true},
	})
	llm.RegisterCapabilities(llm.ProviderCapabilities{
		Provider: "declined", Model: "model-b",
		Supports: map[llm.Capability]bool{llm.CapNativeJSONSchema: false},
	})
	llm.RegisterCapabilities(llm.ProviderCapabilities{
		Provider: "declined", Model: "model-c",
		// Registered, but never says anything about native schema support --
		// HasDeclared is false for it, and Has (what this function reads)
		// reports false the same as an explicit "no".
		Supports: map[llm.Capability]bool{llm.CapStreaming: true},
	})

	schema := map[string]any{"type": "object"}

	cases := []struct {
		name     string
		level    types.ContractLevel
		opt      types.OpOptions
		provider string
		model    string
		want     types.ContractLevel
	}{
		{
			"no schema requested: PromptOnly untouched regardless of route",
			types.ContractPromptOnly, types.OpOptions{}, "unknown", "unknown",
			types.ContractPromptOnly,
		},
		{
			"no schema requested: JSONWellFormed untouched",
			types.ContractJSONWellFormed, types.OpOptions{}, "unknown", "unknown",
			types.ContractJSONWellFormed,
		},
		{
			"no JSONSchema in opt: SchemaConstrained claim passes through unexamined",
			types.ContractSchemaConstrained, types.OpOptions{}, "unknown", "unknown",
			types.ContractSchemaConstrained,
		},
		{
			"JSONSchema requested, route confirmed to support native schema: kept",
			types.ContractSchemaConstrained,
			types.OpOptions{JSONSchema: schema},
			"confirmed", "model-a",
			types.ContractSchemaConstrained,
		},
		{
			"JSONSchema requested, route confirmed, higher level kept too",
			types.ContractSchemaAndInvariantChecked,
			types.OpOptions{JSONSchema: schema},
			"confirmed", "model-a",
			types.ContractSchemaAndInvariantChecked,
		},
		{
			"JSONSchema requested, route explicitly declared false: demoted",
			types.ContractSchemaConstrained,
			types.OpOptions{JSONSchema: schema},
			"declined", "model-b",
			types.ContractJSONWellFormed,
		},
		{
			"JSONSchema requested, route known but silent on this capability: demoted",
			types.ContractSchemaConstrained,
			types.OpOptions{JSONSchema: schema},
			"declined", "model-c",
			types.ContractJSONWellFormed,
		},
		{
			"JSONSchema requested, route never registered at all: demoted (never fail open)",
			types.ContractSchemaConstrained,
			types.OpOptions{JSONSchema: schema},
			"nobody-registered-this", "mystery-model",
			types.ContractJSONWellFormed,
		},
		{
			"JSONSchema requested, route unregistered, EvidenceChecked also demoted",
			types.ContractEvidenceChecked,
			types.OpOptions{JSONSchema: schema},
			"nobody-registered-this", "mystery-model",
			types.ContractJSONWellFormed,
		},
		{
			"empty (non-nil-but-zero-length) JSONSchema map is still 'nothing requested'",
			types.ContractSchemaConstrained,
			types.OpOptions{JSONSchema: map[string]any{}},
			"nobody-registered-this", "mystery-model",
			types.ContractSchemaConstrained,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := negotiatedContractLevel(tc.level, tc.opt, tc.provider, tc.model)
			if got != tc.want {
				t.Errorf("negotiatedContractLevel() = %v, want %v", got, tc.want)
			}
		})
	}
}

// scriptedCapabilityProvider is a minimal llm.Provider that reports a fixed
// Provider/Model on its response, the way scriptedProvider (op_kernel_seam_
// test.go) does, but always answers with the same body -- these tests only
// need a real provider round trip to make CallLLM populate meta.Provider and
// meta.Model through the ordinary publishCallRecord path, not a repair
// sequence.
type scriptedCapabilityProvider struct {
	provider string
	model    string
	body     string
}

func (p *scriptedCapabilityProvider) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: p.body, Provider: p.provider, Model: p.model, FinishReason: "stop"}, nil
}
func (p *scriptedCapabilityProvider) Name() string                               { return p.provider }
func (p *scriptedCapabilityProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *scriptedCapabilityProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// TestRunOpResultNegotiatesDeliveredContractDown is TC-004's own verify line:
// a call that requested native schema enforcement (opt.JSONSchema set) from
// a route this registry does not know to support it is reported as having
// delivered less than it asked for, and Degraded() says so -- not silently,
// which is what happened before negotiatedContractLevel existed.
func TestRunOpResultNegotiatesDeliveredContractDown(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)

	provider := &scriptedCapabilityProvider{provider: "unregistered-route", model: "some-model", body: "even"}
	ctx := WithProvider(context.Background(), provider)

	op := echoOp(func(value string) error {
		if value != "even" {
			return errFor(value)
		}
		return nil
	})
	op.Contract.SchemaName = "echo"

	opt := types.OpOptions{JSONSchema: map[string]any{"type": "object"}}
	result, err := RunOpResult(ctx, op, "x", opt)
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Meta.RequestedContract != types.ContractSchemaAndInvariantChecked {
		t.Fatalf("RequestedContract = %v, want ContractSchemaAndInvariantChecked", result.Meta.RequestedContract)
	}
	if result.Meta.DeliveredContract != types.ContractJSONWellFormed {
		t.Fatalf("DeliveredContract = %v, want ContractJSONWellFormed (native schema unconfirmed for this route)", result.Meta.DeliveredContract)
	}
	if !result.Meta.Degraded() {
		t.Fatal("Meta.Degraded() should report true when the route cannot back the requested schema enforcement")
	}
}

// TestRunOpResultKeepsDeliveredContractWhenRouteConfirmsNativeSchema is the
// other side of the same verify line: a route this registry knows DOES
// support native json_schema enforcement is not penalized -- the caller
// asked for it, and it was actually available.
func TestRunOpResultKeepsDeliveredContractWhenRouteConfirmsNativeSchema(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)

	llm.RegisterCapabilities(llm.ProviderCapabilities{
		Provider: "confirmed-route", Model: "good-model",
		Supports: map[llm.Capability]bool{llm.CapNativeJSONSchema: true},
	})

	provider := &scriptedCapabilityProvider{provider: "confirmed-route", model: "good-model", body: "even"}
	ctx := WithProvider(context.Background(), provider)

	op := echoOp(func(value string) error {
		if value != "even" {
			return errFor(value)
		}
		return nil
	})
	op.Contract.SchemaName = "echo"

	opt := types.OpOptions{JSONSchema: map[string]any{"type": "object"}}
	result, err := RunOpResult(ctx, op, "x", opt)
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Meta.Degraded() {
		t.Fatalf("a route confirmed to support native schema enforcement should not report a degradation, got delivered=%v requested=%v",
			result.Meta.DeliveredContract, result.Meta.RequestedContract)
	}
}

// --- TC-004, the policy half: enforceContractPolicy / WithContractPolicy.

func TestContractPolicyFromReportsAbsenceDistinctFromZeroValue(t *testing.T) {
	if _, ok := contractPolicyFrom(context.Background()); ok {
		t.Fatal("contractPolicyFrom reported a policy attached to a plain context")
	}

	ctx := WithContractPolicy(context.Background(), types.DataPolicy{})
	policy, ok := contractPolicyFrom(ctx)
	if !ok {
		t.Fatal("contractPolicyFrom did not find the policy WithContractPolicy attached")
	}
	if policy.MinimumContract != types.ContractPromptOnly {
		t.Fatalf("MinimumContract = %v, want the zero value ContractPromptOnly", policy.MinimumContract)
	}
}

func TestEnforceContractPolicyNoPolicyAttachedIsANoOp(t *testing.T) {
	if err := enforceContractPolicy(context.Background(), types.ContractPromptOnly); err != nil {
		t.Fatalf("enforceContractPolicy with no attached policy returned an error: %v", err)
	}
}

func TestEnforceContractPolicyRefusesBelowMinimum(t *testing.T) {
	ctx := WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: types.ContractSchemaAndInvariantChecked})
	err := enforceContractPolicy(ctx, types.ContractJSONWellFormed)
	if err == nil {
		t.Fatal("enforceContractPolicy accepted a delivered level below the policy's minimum")
	}
	if !errors.Is(err, ErrContractDegraded) {
		t.Fatalf("err = %v, want it to wrap ErrContractDegraded", err)
	}
}

func TestEnforceContractPolicyAcceptsAtOrAboveMinimum(t *testing.T) {
	ctx := WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: types.ContractSchemaConstrained})
	if err := enforceContractPolicy(ctx, types.ContractSchemaConstrained); err != nil {
		t.Fatalf("enforceContractPolicy refused a delivered level exactly at the minimum: %v", err)
	}
	if err := enforceContractPolicy(ctx, types.ContractEvidenceChecked); err != nil {
		t.Fatalf("enforceContractPolicy refused a delivered level above the minimum: %v", err)
	}
}

// TestRunOpResultRefusesDisallowedDegradation is TC-004's verify line's other
// half: "with degradation disallowed, it returns an error instead." A caller
// who attached a policy floor via WithContractPolicy, and whose call
// negotiates down below that floor, gets ErrContractDegraded -- not a
// Result[T] carrying the weaker answer.
func TestRunOpResultRefusesDisallowedDegradation(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)

	provider := &scriptedCapabilityProvider{provider: "unregistered-route", model: "some-model", body: "even"}
	ctx := WithProvider(context.Background(), provider)
	ctx = WithContractPolicy(ctx, types.DataPolicy{MinimumContract: types.ContractSchemaConstrained})

	op := echoOp(func(value string) error {
		if value != "even" {
			return errFor(value)
		}
		return nil
	})
	op.Contract.SchemaName = "echo"

	opt := types.OpOptions{JSONSchema: map[string]any{"type": "object"}}
	result, err := RunOpResult(ctx, op, "x", opt)
	if err == nil {
		t.Fatal("RunOpResult should have refused a call that negotiated below the attached policy's minimum")
	}
	if !errors.Is(err, ErrContractDegraded) {
		t.Fatalf("err = %v, want it to wrap ErrContractDegraded", err)
	}
	if result.Value != "" {
		t.Fatalf("a refused call should not carry a value, got %q", result.Value)
	}
}

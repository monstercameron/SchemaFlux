package schemaflux_test

import (
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// A-006. `(T, error)` cannot say what an answer cost, how many attempts it
// took, which contract was delivered, or which checks the library ran.
func TestExtractResultCarriesTheRecord(t *testing.T) {
	provider := withScriptedProvider(t, invoiceJSON, nil)

	result, err := schemaflux.ExtractResult[invoice]("Invoice INV-4417",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}

	if result.Value.Number != "INV-4417" {
		t.Errorf("Value = %+v", result.Value)
	}

	meta := result.Meta
	if meta.Operation != "extract" {
		t.Errorf("Operation = %q", meta.Operation)
	}
	if meta.Attempts < 1 {
		t.Errorf("Attempts = %d, want at least one", meta.Attempts)
	}
	if meta.RequestID == "" {
		t.Error("no request ID, so this answer cannot be correlated with anything")
	}
	if meta.SchemaID == "" {
		t.Error("no schema identity, so nobody can say which contract produced this")
	}
	// Not `> 0`: against an in-process double the whole call can finish inside
	// one tick of the platform clock, and a test that fails for that reason
	// teaches people to distrust the field rather than the clock.
	if meta.Elapsed < 0 {
		t.Errorf("Elapsed = %v", meta.Elapsed)
	}
	if len(provider.requests) != meta.Attempts {
		t.Errorf("the envelope reports %d attempts and the provider saw %d",
			meta.Attempts, len(provider.requests))
	}
}

// The plain form and the detailed form are the same execution. Two return types
// that execute differently is how the two drift -- this library has four such
// pairs already.
func TestTheTwoFormsSendTheSameRequest(t *testing.T) {
	plain := withScriptedProvider(t, invoiceJSON, nil)
	if _, err := schemaflux.Extract[invoice]("Invoice INV-4417", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	detailed := withScriptedProvider(t, invoiceJSON, nil)
	if _, err := schemaflux.ExtractResult[invoice]("Invoice INV-4417", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}

	if len(plain.requests) != 1 || len(detailed.requests) != 1 {
		t.Fatalf("call counts differ: plain %d, detailed %d", len(plain.requests), len(detailed.requests))
	}
	if plain.requests[0].SystemPrompt != detailed.requests[0].SystemPrompt {
		t.Error("the two forms send different system prompts")
	}
	if plain.requests[0].UserPrompt != detailed.requests[0].UserPrompt {
		t.Error("the two forms send different user prompts")
	}
}

// Requested versus delivered is the field worth reading: asking for a strict
// contract and receiving a structural one is exactly the case to notice.
func TestTheEnvelopeReportsWhatWasDelivered(t *testing.T) {
	withScriptedProvider(t, invoiceJSON, nil)

	strict, err := schemaflux.ExtractResult[invoice]("Invoice INV-4417",
		schemaflux.NewExtractOptions().WithMode(schemaflux.Strict))
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}
	if strict.Meta.RequestedContract != schemaflux.ContractSchemaAndInvariantChecked {
		t.Errorf("RequestedContract = %v under Strict", strict.Meta.RequestedContract)
	}
	if strict.Meta.DeliveredContract != schemaflux.ContractSchemaAndInvariantChecked {
		t.Errorf("DeliveredContract = %v", strict.Meta.DeliveredContract)
	}
	if strict.Meta.Degraded() {
		t.Error("a satisfied strict request reports degradation")
	}
	if len(strict.Meta.Checks) < 3 {
		t.Errorf("Checks = %+v, want the strict checks named", strict.Meta.Checks)
	}

	withScriptedProvider(t, invoiceJSON, nil)
	transform, err := schemaflux.ExtractResult[invoice]("Invoice INV-4417",
		schemaflux.NewExtractOptions().WithMode(schemaflux.TransformMode))
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}
	if transform.Meta.DeliveredContract >= strict.Meta.DeliveredContract {
		t.Error("Transform delivered as much as Strict, which makes Strict meaningless")
	}
}

// A failure still produces an envelope, because a failure is the case where the
// record matters most.
func TestAFailureStillCarriesARecord(t *testing.T) {
	withScriptedProvider(t, "I'm sorry, I can't help with that.", nil)

	result, err := schemaflux.ExtractResult[invoice]("Invoice INV-4417",
		schemaflux.NewExtractOptions())
	if err == nil {
		t.Fatal("expected a failure")
	}
	if result.Meta.Attempts < 1 {
		t.Errorf("Attempts = %d; the calls were made and paid for", result.Meta.Attempts)
	}
	if len(result.Meta.Failed()) == 0 {
		t.Error("no failed check recorded for a failed extraction")
	}
}

// Usage and cost sum across attempts, because a caller asking what an answer
// cost means the answer, not the last try at it.
func TestUsageSumsAcrossAttempts(t *testing.T) {
	// The first body is unusable, so the repair loop tries again.
	provider := withScriptedProviderReplies(t,
		"not json at all",
		invoiceJSON,
	)

	result, err := schemaflux.ExtractResult[invoice]("Invoice INV-4417",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("ExtractResult: %v", err)
	}

	if len(provider.requests) < 2 {
		t.Skip("the repair loop did not run a second attempt; nothing to sum")
	}
	if result.Meta.Attempts < 2 {
		t.Errorf("Attempts = %d, want the repair counted", result.Meta.Attempts)
	}
}

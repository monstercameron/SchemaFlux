package types

import (
	"strings"
	"testing"
	"time"
)

func TestContractLevelString(t *testing.T) {
	cases := []struct {
		c    ContractLevel
		want string
	}{
		{ContractPromptOnly, "prompt only"},
		{ContractJSONWellFormed, "json well-formed"},
		{ContractSchemaConstrained, "schema constrained"},
		{ContractSchemaAndInvariantChecked, "schema and invariants checked"},
		{ContractEvidenceChecked, "evidence checked"},
		{ContractFullyGoverned, "fully governed"},
		{ContractLevel(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.c.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Degraded is the one field a caller who checks nothing else should check:
// it must be true exactly when what was delivered is weaker than what was
// asked for, never the reverse and never on an exact match.
func TestMetaDegraded(t *testing.T) {
	cases := []struct {
		name      string
		requested ContractLevel
		delivered ContractLevel
		want      bool
	}{
		{"delivered weaker", ContractEvidenceChecked, ContractSchemaConstrained, true},
		{"delivered exact match", ContractSchemaConstrained, ContractSchemaConstrained, false},
		{"delivered stronger", ContractSchemaConstrained, ContractFullyGoverned, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Meta{RequestedContract: tc.requested, DeliveredContract: tc.delivered}
			if got := m.Degraded(); got != tc.want {
				t.Errorf("Degraded() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMetaFailedReturnsOnlyTheFailedChecks(t *testing.T) {
	m := Meta{Checks: []Check{
		{Name: "schema", Passed: true},
		{Name: "subset", Passed: false, Detail: "extra item"},
		{Name: "permutation", Passed: false, Detail: "missing item"},
	}}
	failed := m.Failed()
	if len(failed) != 2 {
		t.Fatalf("Failed() returned %d checks, want 2", len(failed))
	}
	for _, f := range failed {
		if f.Passed {
			t.Errorf("Failed() included a passed check: %+v", f)
		}
	}

	allPassed := Meta{Checks: []Check{{Name: "schema", Passed: true}}}
	if got := allPassed.Failed(); got != nil {
		t.Errorf("Failed() = %v, want nil when every check passed", got)
	}
}

func TestMetaStringCoversPricedAndDegradedBranches(t *testing.T) {
	priced := Meta{
		Operation: "extract", Provider: "openai", Model: "gpt-5.6-luna",
		Attempts: 2, Repairs: 1, Elapsed: 1500 * time.Millisecond,
		Usage:             TokenUsage{TotalTokens: 120},
		Cost:              CostInfo{Priced: true, Currency: "USD", TotalCost: 0.004},
		RequestedContract: ContractFullyGoverned,
		DeliveredContract: ContractSchemaConstrained,
	}
	s := priced.String()
	for _, want := range []string{"extract", "openai/gpt-5.6-luna", "2 attempt(s)", "1 repair(s)", "USD 0.004000", "asked for fully governed"} {
		if !strings.Contains(s, want) {
			t.Errorf("Meta.String() = %q, missing %q", s, want)
		}
	}

	unpriced := Meta{Operation: "extract", Provider: "openai", Model: "gpt-5.6-luna"}
	if got := unpriced.String(); !strings.Contains(got, "cost unknown") {
		t.Errorf("Meta.String() = %q, want \"cost unknown\" for an unpriced call", got)
	}
	if strings.Contains(unpriced.String(), "asked for") {
		t.Errorf("Meta.String() = %q, an undegraded result must not explain a degradation", unpriced.String())
	}
}

func TestMetaFromNilMetadataReturnsZeroMeta(t *testing.T) {
	m := MetaFrom(nil)
	if m.Operation != "" || m.Provider != "" || m.Model != "" || m.Attempts != 0 ||
		m.RequestID != "" || m.Checks != nil || m.ModelClaims != nil {
		t.Errorf("MetaFrom(nil) = %+v, want the zero Meta", m)
	}
}

func TestMetaFromDerivesModelDriftNeverClaimsIt(t *testing.T) {
	// Both sides present and different: drift observed.
	drifted := MetaFrom(&ResultMetadata{
		RequestedModel: "gpt-5.6-luna",
		ObservedModel:  "gpt-5.6-sol",
	})
	if !drifted.ModelDrifted || drifted.ModelDriftUnknown {
		t.Errorf("drifted case: ModelDrifted=%v ModelDriftUnknown=%v, want true/false", drifted.ModelDrifted, drifted.ModelDriftUnknown)
	}

	// Both sides present and equal: no drift, and known to be so.
	agreed := MetaFrom(&ResultMetadata{
		RequestedModel: "gpt-5.6-luna",
		ObservedModel:  "gpt-5.6-luna",
	})
	if agreed.ModelDrifted || agreed.ModelDriftUnknown {
		t.Errorf("agreed case: ModelDrifted=%v ModelDriftUnknown=%v, want false/false", agreed.ModelDrifted, agreed.ModelDriftUnknown)
	}

	// The refusal: a provider that echoes nothing back must read as
	// "unknown", never as "no drift" -- silence is not agreement.
	silent := MetaFrom(&ResultMetadata{RequestedModel: "gpt-5.6-luna", ObservedModel: ""})
	if silent.ModelDrifted {
		t.Error("silent case: ModelDrifted = true, but drift cannot be observed from silence")
	}
	if !silent.ModelDriftUnknown {
		t.Error("silent case: ModelDriftUnknown = false, an unobserved substitution must not read as an observed absence of one")
	}

	neitherSet := MetaFrom(&ResultMetadata{})
	if !neitherSet.ModelDriftUnknown {
		t.Error("neither model named: ModelDriftUnknown = false, want true")
	}
}

func TestMetaFromCopiesUsageCostAndSchemaID(t *testing.T) {
	usage := TokenUsage{TotalTokens: 42}
	cost := CostInfo{Priced: true, Currency: "USD", TotalCost: 0.01}
	m := MetaFrom(&ResultMetadata{
		RequestID:      "req-1",
		CorrelationID:  "corr-1",
		Operation:      "extract",
		Provider:       "openai",
		Model:          "gpt-5.6-luna",
		RequestedModel: "gpt-5.6-luna",
		RetryCount:     2,
		Duration:       time.Second,
		TokenUsage:     &usage,
		CostInfo:       &cost,
		Custom:         map[string]any{"schema_id": "invoice@v2"},
	})
	if m.RequestID != "req-1" || m.CorrelationID != "corr-1" || m.Operation != "extract" {
		t.Fatalf("m = %+v, runtime facts not copied", m)
	}
	if m.Attempts != 3 {
		t.Errorf("Attempts = %d, want RetryCount+1 = 3", m.Attempts)
	}
	if m.Usage != usage {
		t.Errorf("Usage = %+v, want %+v", m.Usage, usage)
	}
	if m.Cost != cost {
		t.Errorf("Cost = %+v, want %+v", m.Cost, cost)
	}
	if m.SchemaID != "invoice@v2" {
		t.Errorf("SchemaID = %q, want invoice@v2", m.SchemaID)
	}
}

// A schema_id of the wrong Go type must not populate SchemaID with a
// type-asserted garbage value -- the `, ok` guard is the refusal under test.
func TestMetaFromIgnoresWrongTypedSchemaID(t *testing.T) {
	m := MetaFrom(&ResultMetadata{Custom: map[string]any{"schema_id": 42}})
	if m.SchemaID != "" {
		t.Errorf("SchemaID = %q, want empty when custom[schema_id] is not a string", m.SchemaID)
	}
}

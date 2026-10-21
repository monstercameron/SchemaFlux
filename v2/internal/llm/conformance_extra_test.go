package llm

import "testing"

// TestConflictsWithRegistrySkipsCapabilitiesTheRegistryNeverDeclared proves
// an observed capability that the registry entry simply never mentioned
// (HasDeclared false) is not reported as a conflict -- only a capability the
// registry explicitly declared one way while the suite measured the other
// counts. TestConformanceReportLabelDropsToLiveVerifiedOnRegistryConflict
// (conformance_test.go) only exercises the "declared and disagreeing"
// branch.
func TestConflictsWithRegistrySkipsCapabilitiesTheRegistryNeverDeclared(t *testing.T) {
	defer ResetCapabilityRegistryForTest()
	ResetCapabilityRegistryForTest()

	RegisterCapabilities(ProviderCapabilities{
		Provider: "conflict-skip-vendor",
		Model:    "conflict-skip-model",
		Supports: map[Capability]bool{CapNativeJSONSchema: true}, // says nothing about CapStreaming
	})

	modelCheck := ConformanceCheck{ID: "m1", Category: CategoryModel, Capability: CapStreaming}
	report := &ConformanceReport{
		ProviderName: "conflict-skip-vendor",
		Model:        "conflict-skip-model",
		checks:       []ConformanceCheck{modelCheck},
		Results: map[string]ConformanceOutcome{
			"m1": {Passed: true, Supported: true, Detail: "measured streaming support"},
		},
	}

	if conflicts := report.ConflictsWithRegistry(); len(conflicts) != 0 {
		t.Errorf("ConflictsWithRegistry() = %v, want none -- the registry never declared CapStreaming either way", conflicts)
	}
}

// TestConformanceCategoryString pins ConformanceCategory.String() for both
// named values and the default fallback for a value outside the declared
// range.
func TestConformanceCategoryString(t *testing.T) {
	cases := []struct {
		category ConformanceCategory
		want     string
	}{
		{CategoryProtocol, "protocol"},
		{CategoryModel, "model"},
		{ConformanceCategory(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.category.String(); got != tc.want {
			t.Errorf("ConformanceCategory(%d).String() = %q, want %q", tc.category, got, tc.want)
		}
	}
}

// TestConformanceLabelString pins every named ConformanceLabel's String() and
// the default fallback for a value outside the five declared constants.
// TestConformanceReportLabelHandCheckedMatrix (conformance_test.go) calls
// Label() and formats it with %s only inside a t.Errorf that never fires
// when the case passes, so String() itself is otherwise never actually
// invoked for those values -- these calls are direct and unconditional.
func TestConformanceLabelString(t *testing.T) {
	cases := []struct {
		label ConformanceLabel
		want  string
	}{
		{ConformanceLabelUnrated, "unrated"},
		{ConformanceLabelIntegrated, "integrated"},
		{ConformanceLabelConformant, "conformant"},
		{ConformanceLabelLiveVerified, "live-verified"},
		{ConformanceLabelProductionSupported, "production-supported"},
		{ConformanceLabel(999), "ConformanceLabel(999)"},
	}
	for _, tc := range cases {
		if got := tc.label.String(); got != tc.want {
			t.Errorf("ConformanceLabel(%d).String() = %q, want %q", tc.label, got, tc.want)
		}
	}
}

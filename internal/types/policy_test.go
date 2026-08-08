package types

import (
	"errors"
	"testing"
)

// CP-002. TRU-11's property under test throughout: Tighten(boundary,
// override) must never produce a policy more permissive than either input,
// on any field, regardless of which side happened to declare the stricter
// value.

func TestTightenClassificationDefersToWhicheverSideDeclaredIt(t *testing.T) {
	base := DataPolicy{}
	override := DataPolicy{Classification: "restricted"}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.Classification != "restricted" {
		t.Fatalf("Classification = %q, want %q", got.Classification, "restricted")
	}

	base = DataPolicy{Classification: "restricted"}
	got, err = base.Tighten(DataPolicy{})
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.Classification != "restricted" {
		t.Fatalf("Classification = %q, want %q", got.Classification, "restricted")
	}
}

func TestTightenRefusesConflictingClassification(t *testing.T) {
	base := DataPolicy{Classification: "public"}
	override := DataPolicy{Classification: "restricted"}
	_, err := base.Tighten(override)
	if err == nil {
		t.Fatal("Tighten resolved two conflicting classifications instead of refusing")
	}
	if !errors.Is(err, ErrDataPolicyConflict) {
		t.Fatalf("err = %v, want it to wrap ErrDataPolicyConflict", err)
	}
}

func TestTightenAllowedProvidersIntersects(t *testing.T) {
	base := DataPolicy{AllowedProviders: []string{"openai", "anthropic", "local"}}
	override := DataPolicy{AllowedProviders: []string{"anthropic", "local", "cerebras"}}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.AllowsProvider("openai") {
		t.Fatal("openai survived an intersection that should have dropped it")
	}
	if !got.AllowsProvider("anthropic") || !got.AllowsProvider("local") {
		t.Fatalf("AllowedProviders = %v, want anthropic and local", got.AllowedProviders)
	}
	if got.AllowsProvider("cerebras") {
		t.Fatal("cerebras survived an intersection that should have dropped it")
	}
}

func TestTightenAllowedProvidersUndeclaredDefersToOtherSide(t *testing.T) {
	base := DataPolicy{} // undeclared
	override := DataPolicy{AllowedProviders: []string{"openai"}}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if !got.AllowsProvider("openai") || got.AllowsProvider("anthropic") {
		t.Fatalf("AllowedProviders = %v, want exactly [openai]", got.AllowedProviders)
	}
}

// The never-fail-open property: an undeclared policy allows nothing, not
// everything.
func TestUndeclaredDataPolicyAllowsNothing(t *testing.T) {
	var p DataPolicy
	if p.AllowsProvider("openai") {
		t.Fatal("a zero-value DataPolicy allowed a provider")
	}
	if p.AllowsRegion("us") {
		t.Fatal("a zero-value DataPolicy allowed a region")
	}
	if p.AllowsRetention(0) == false {
		// Zero retention is the one thing an unconfigured policy must still
		// permit, since 0 is the strictest possible value on that axis.
		t.Fatal("a zero-value DataPolicy refused zero days of retention")
	}
	if p.AllowsRetention(1) {
		t.Fatal("a zero-value DataPolicy allowed nonzero retention")
	}
}

func TestTightenRetentionPicksTheStricterCeiling(t *testing.T) {
	base := DataPolicy{MaxRetentionDays: 30}
	override := DataPolicy{MaxRetentionDays: 7}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.MaxRetentionDays != 7 {
		t.Fatalf("MaxRetentionDays = %d, want 7", got.MaxRetentionDays)
	}

	// The reverse order must produce the same answer -- the stricter side
	// wins regardless of which argument it arrived as.
	got, err = override.Tighten(base)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.MaxRetentionDays != 7 {
		t.Fatalf("MaxRetentionDays = %d, want 7 (order must not matter)", got.MaxRetentionDays)
	}
}

func TestTightenRetentionUnspecifiedDefersToDeclaredSide(t *testing.T) {
	base := DataPolicy{MaxRetentionDays: RetentionUnspecified}
	override := DataPolicy{MaxRetentionDays: 14}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.MaxRetentionDays != 14 {
		t.Fatalf("MaxRetentionDays = %d, want 14", got.MaxRetentionDays)
	}
}

// AllowTrainingUse and AllowResultCaching: an override cannot turn either
// back on if the boundary policy turned it off. This is the field TRU-11
// names by name ("never weakenable").
func TestTightenNeverReenablesTrainingOrCaching(t *testing.T) {
	base := DataPolicy{AllowTrainingUse: false, AllowResultCaching: false}
	override := DataPolicy{AllowTrainingUse: true, AllowResultCaching: true}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.AllowTrainingUse {
		t.Fatal("an override re-enabled training use the boundary policy forbade")
	}
	if got.AllowResultCaching {
		t.Fatal("an override re-enabled result caching the boundary policy forbade")
	}

	// Both sides permitting is the only way the result permits.
	base = DataPolicy{AllowTrainingUse: true, AllowResultCaching: true}
	override = DataPolicy{AllowTrainingUse: true, AllowResultCaching: true}
	got, err = base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if !got.AllowTrainingUse || !got.AllowResultCaching {
		t.Fatal("Tighten disabled a flag both sides explicitly permitted")
	}
}

func TestTightenContentLoggingPicksTheStricterLevel(t *testing.T) {
	base := DataPolicy{ContentLogging: LogFullContent}
	override := DataPolicy{ContentLogging: LogMetadataOnly}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.ContentLogging != LogMetadataOnly {
		t.Fatalf("ContentLogging = %v, want %v", got.ContentLogging, LogMetadataOnly)
	}
	if !got.AllowsContentLogging(LogNoContent) {
		t.Fatal("AllowsContentLogging(LogNoContent) = false under a MetadataOnly policy, should be permitted (it is stricter)")
	}
	if got.AllowsContentLogging(LogRedactedContent) {
		t.Fatal("AllowsContentLogging(LogRedactedContent) = true under a MetadataOnly policy, should be refused")
	}
}

func TestTightenMinimumContractPicksTheHigherFloor(t *testing.T) {
	base := DataPolicy{MinimumContract: ContractSchemaConstrained}
	override := DataPolicy{MinimumContract: ContractEvidenceChecked}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.MinimumContract != ContractEvidenceChecked {
		t.Fatalf("MinimumContract = %v, want %v", got.MinimumContract, ContractEvidenceChecked)
	}
}

func TestTightenNeverProducesAWiderIntersectionThanEitherSide(t *testing.T) {
	// A composite check across every field at once, from the "did this get
	// more permissive" direction the doc comment promises.
	base := DataPolicy{
		Classification:     "restricted",
		AllowedProviders:   []string{"openai", "anthropic"},
		AllowedRegions:     []string{"us", "eu"},
		MaxRetentionDays:   30,
		AllowTrainingUse:   true,
		AllowResultCaching: true,
		ContentLogging:     LogRedactedContent,
		MinimumContract:    ContractSchemaConstrained,
	}
	override := DataPolicy{
		Classification:     "restricted",
		AllowedProviders:   []string{"anthropic", "cerebras"},
		AllowedRegions:     []string{"eu"},
		MaxRetentionDays:   0,
		AllowTrainingUse:   false,
		AllowResultCaching: true,
		ContentLogging:     LogNoContent,
		MinimumContract:    ContractEvidenceChecked,
	}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}

	if got.AllowsProvider("openai") {
		t.Fatal("openai should have been dropped by the intersection")
	}
	if !got.AllowsProvider("anthropic") {
		t.Fatal("anthropic should have survived the intersection")
	}
	if got.AllowsRegion("us") {
		t.Fatal("us should have been dropped by the intersection")
	}
	if got.MaxRetentionDays != 0 {
		t.Fatalf("MaxRetentionDays = %d, want 0 (stricter side)", got.MaxRetentionDays)
	}
	if got.AllowTrainingUse {
		t.Fatal("AllowTrainingUse should be false (override forbade it)")
	}
	if got.ContentLogging != LogNoContent {
		t.Fatalf("ContentLogging = %v, want %v", got.ContentLogging, LogNoContent)
	}
	if got.MinimumContract != ContractEvidenceChecked {
		t.Fatalf("MinimumContract = %v, want %v", got.MinimumContract, ContractEvidenceChecked)
	}
}

func TestIntersectionThatExcludesEverythingIsARealEmptySet(t *testing.T) {
	base := DataPolicy{AllowedProviders: []string{"openai"}}
	override := DataPolicy{AllowedProviders: []string{"anthropic"}}
	got, err := base.Tighten(override)
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if got.AllowsProvider("openai") || got.AllowsProvider("anthropic") {
		t.Fatal("a disjoint intersection allowed a provider from either side")
	}
}

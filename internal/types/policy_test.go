package types

import (
	"errors"
	"reflect"
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

// An undeclared side allows nothing, so tightening with it allows nothing: it
// does not lend its emptiness to the other side as "no opinion". The result used
// to be the other side's list, which let a policy that allowed no provider be
// tightened into one that allowed openai.
func TestTightenAnUndeclaredProviderListDoesNotWidenTheOtherSide(t *testing.T) {
	declared := DataPolicy{AllowedProviders: []string{"openai"}}
	for name, pair := range map[string][2]DataPolicy{
		"undeclared then declared": {{}, declared},
		"declared then undeclared": {declared, {}},
		"empty then declared":      {{AllowedProviders: []string{}}, declared},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := pair[0].Tighten(pair[1])
			if err != nil {
				t.Fatalf("Tighten: %v", err)
			}
			if got.AllowsProvider("openai") || got.AllowsProvider("anthropic") {
				t.Fatalf("AllowedProviders = %v, want nothing allowed", got.AllowedProviders)
			}
			if got.AllowedProviders == nil {
				t.Error("an empty result is nil, which reads as undeclared; it should be an empty list")
			}
		})
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

// An unspecified ceiling allows no retention, so it is not a gap the other
// side's number can fill.
func TestTightenRetentionUnspecifiedStaysUnspecified(t *testing.T) {
	for name, pair := range map[string][2]RetentionDays{
		"unspecified then 14": {RetentionUnspecified, 14},
		"14 then unspecified": {14, RetentionUnspecified},
		"both unspecified":    {RetentionUnspecified, RetentionUnspecified},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := DataPolicy{MaxRetentionDays: pair[0]}.Tighten(DataPolicy{MaxRetentionDays: pair[1]})
			if err != nil {
				t.Fatalf("Tighten: %v", err)
			}
			if got.AllowsRetention(1) || got.AllowsRetention(14) || !got.AllowsRetention(0) {
				t.Fatalf("MaxRetentionDays = %d allows retention it should not", got.MaxRetentionDays)
			}
		})
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

// The property in the file header, checked from both inputs over every
// combination of a small universe: what the result allows is exactly what both
// inputs allow, on every field the Allows methods cover.
func TestTightenAllowsExactlyWhatBothInputsAllow(t *testing.T) {
	lists := [][]string{nil, {}, {"a"}, {"b"}, {"a", "b"}, {"B ", "c"}, {"A", "b", "c"}}
	retentions := []RetentionDays{RetentionUnspecified, 0, 7, 30}
	loggings := []ContentLoggingLevel{LogNoContent, LogMetadataOnly, LogRedactedContent, LogFullContent}
	names := []string{"a", "b", "c", "d", ""}

	var policies []DataPolicy
	for _, providers := range lists {
		for _, retention := range retentions {
			for _, logging := range loggings {
				policies = append(policies, DataPolicy{
					AllowedProviders: providers, AllowedRegions: providers,
					MaxRetentionDays: retention, ContentLogging: logging,
					AllowTrainingUse: len(providers)%2 == 0, AllowResultCaching: len(providers) > 1,
				})
			}
		}
	}

	checked := 0
	for _, first := range policies {
		for _, second := range policies {
			got, err := first.Tighten(second)
			if err != nil {
				t.Fatalf("Tighten: %v", err)
			}
			for _, name := range names {
				if want := first.AllowsProvider(name) && second.AllowsProvider(name); got.AllowsProvider(name) != want {
					t.Fatalf("AllowsProvider(%q) = %v, want %v\nfirst  %+v\nsecond %+v\ngot    %+v", name, got.AllowsProvider(name), want, first, second, got)
				}
				if want := first.AllowsRegion(name) && second.AllowsRegion(name); got.AllowsRegion(name) != want {
					t.Fatalf("AllowsRegion(%q) = %v, want %v", name, got.AllowsRegion(name), want)
				}
			}
			for days := RetentionDays(-1); days <= 40; days++ {
				if want := first.AllowsRetention(days) && second.AllowsRetention(days); got.AllowsRetention(days) != want {
					t.Fatalf("AllowsRetention(%d) = %v, want %v\nfirst  %+v\nsecond %+v", days, got.AllowsRetention(days), want, first, second)
				}
			}
			for _, level := range loggings {
				if want := first.AllowsContentLogging(level) && second.AllowsContentLogging(level); got.AllowsContentLogging(level) != want {
					t.Fatalf("AllowsContentLogging(%v) = %v, want %v", level, got.AllowsContentLogging(level), want)
				}
			}
			if got.AllowTrainingUse != (first.AllowTrainingUse && second.AllowTrainingUse) ||
				got.AllowResultCaching != (first.AllowResultCaching && second.AllowResultCaching) {
				t.Fatalf("a permission survived that one side refused: %+v", got)
			}
			checked++
		}
	}
	if checked < 10000 {
		t.Fatalf("only %d combinations were checked", checked)
	}
}

// Tightening again never brings back what an earlier step removed. The empty
// intersection of A and B used to be read as "undeclared" by the next Tighten,
// which then returned C's list: wider than A and than B.
func TestTightenTwiceNeverWidensAnEarlierResult(t *testing.T) {
	a := DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"us"}, MaxRetentionDays: 7}
	b := DataPolicy{AllowedProviders: []string{"anthropic"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 30}
	c := DataPolicy{AllowedProviders: []string{"openai", "anthropic", "local"}, AllowedRegions: []string{"us", "eu"}, MaxRetentionDays: 90}

	ab, err := a.Tighten(b)
	if err != nil {
		t.Fatal(err)
	}
	abc, err := ab.Tighten(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai", "anthropic", "local"} {
		if abc.AllowsProvider(name) {
			t.Errorf("Tighten(Tighten(A,B),C) allows provider %q, which A and B together refuse", name)
		}
	}
	for _, region := range []string{"us", "eu"} {
		if abc.AllowsRegion(region) {
			t.Errorf("Tighten(Tighten(A,B),C) allows region %q, which A and B together refuse", region)
		}
	}
	if abc.AllowsRetention(8) {
		t.Error("the result allows 8 days of retention, above A's ceiling of 7")
	}

	// Associativity, as far as permissions go.
	bc, _ := b.Tighten(c)
	aThenBC, _ := a.Tighten(bc)
	for _, name := range []string{"openai", "anthropic", "local"} {
		if abc.AllowsProvider(name) != aThenBC.AllowsProvider(name) {
			t.Errorf("provider %q: grouping the Tighten calls changed the result", name)
		}
	}
}

func TestTightenComparesProviderNamesWithoutRegardToCase(t *testing.T) {
	got, err := DataPolicy{AllowedProviders: []string{"OpenAI", " Local "}}.Tighten(DataPolicy{AllowedProviders: []string{"openai", "LOCAL", "cerebras"}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.AllowsProvider("openai") || !got.AllowsProvider("local") || got.AllowsProvider("cerebras") {
		t.Errorf("AllowedProviders = %q", got.AllowedProviders)
	}
	if len(got.AllowedProviders) != 2 || got.AllowedProviders[0] > got.AllowedProviders[1] {
		t.Errorf("AllowedProviders = %q, want two names in sorted order", got.AllowedProviders)
	}
}

func TestTightenSortsAndDeduplicatesWhateverTheInputOrder(t *testing.T) {
	got, err := DataPolicy{AllowedProviders: []string{"c", "a", "b", "a"}}.Tighten(DataPolicy{AllowedProviders: []string{"b", "a", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got.AllowedProviders, want) {
		t.Errorf("AllowedProviders = %q, want %q", got.AllowedProviders, want)
	}
}

func TestABlankNameIsInNoList(t *testing.T) {
	for _, list := range [][]string{{""}, {" "}, {"a", ""}} {
		p := DataPolicy{AllowedProviders: list, AllowedRegions: list}
		for _, name := range []string{"", "  ", "\t"} {
			if p.AllowsProvider(name) || p.AllowsRegion(name) {
				t.Errorf("a blank name %q was allowed by %q", name, list)
			}
		}
	}
}

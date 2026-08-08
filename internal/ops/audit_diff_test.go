package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type auditSource struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
	Country string `json:"country"`
}

func installAuditResponse(t *testing.T, body string) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

func includesName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// OP-308, following OP-302. An audit trail written by the thing being audited
// is not an audit trail: a pivot that drops a field and does not mention it
// reported no loss at all, and the caller's only evidence was the missing field.
func TestPivotComputesItsDataLoss(t *testing.T) {
	type pivoted struct {
		Name string `json:"name"`
	}

	// The model drops three fields and claims to have lost nothing.
	installAuditResponse(t, `{
		"pivoted": {"name":"Ada"},
		"mappings": [],
		"stats": {"source_fields": 4, "target_fields": 1},
		"data_loss": []
	}`)

	result, err := Pivot[auditSource, pivoted](auditSource{
		Name: "Ada", Email: "ada@example.com", Phone: "305-555-1234", Country: "US",
	})
	if err != nil {
		t.Fatalf("Pivot: %v", err)
	}

	for _, field := range []string{"email", "phone", "country"} {
		if !includesName(result.DataLoss, field) {
			t.Errorf("DataLoss = %v, want it to include %q", result.DataLoss, field)
		}
	}
	if includesName(result.DataLoss, "name") {
		t.Errorf("DataLoss = %v; name survived the pivot", result.DataLoss)
	}
	if len(result.ModelClaimedDataLoss) != 0 {
		t.Errorf("ModelClaimedDataLoss = %v, want the empty list the model actually sent",
			result.ModelClaimedDataLoss)
	}
}

// A faithful pivot reports no loss, so the check is not merely pessimistic.
func TestAFaithfulPivotReportsNoLoss(t *testing.T) {
	type same struct {
		Name string `json:"name"`
	}

	installAuditResponse(t, `{"pivoted":{"name":"Ada"},"mappings":[],"stats":{}}`)

	result, err := Pivot[same, same](same{Name: "Ada"})
	if err != nil {
		t.Fatalf("Pivot: %v", err)
	}
	if len(result.DataLoss) != 0 {
		t.Errorf("DataLoss = %v, want none", result.DataLoss)
	}
}

// Enrich has the same shape one field over: a model that adds a field and does
// not mention it produced an enrichment claiming to have added nothing.
func TestEnrichComputesItsAddedFields(t *testing.T) {
	type enriched struct {
		Name    string `json:"name"`
		Region  string `json:"region"`
		Segment string `json:"segment"`
	}

	installAuditResponse(t, `{
		"enriched": {"name":"Ada","region":"EU","segment":"enterprise"},
		"added_fields": ["region"],
		"confidence": {},
		"derivations": {}
	}`)

	type source struct {
		Name string `json:"name"`
	}

	result, err := Enrich[source, enriched](source{Name: "Ada"}, NewEnrichOptions())
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}

	for _, field := range []string{"region", "segment"} {
		if !includesName(result.AddedFields, field) {
			t.Errorf("AddedFields = %v, want it to include %q", result.AddedFields, field)
		}
	}
	if includesName(result.AddedFields, "name") {
		t.Errorf("AddedFields = %v; name came from the source", result.AddedFields)
	}
	// The model mentioned one of the two, and the disagreement is visible.
	if len(result.ModelClaimedAddedFields) != 1 {
		t.Errorf("ModelClaimedAddedFields = %v, want the model's own single entry",
			result.ModelClaimedAddedFields)
	}
}

// Normalize is the one that could not be a field-set diff: its changes are
// value differences, and a diff cannot recover the *reason* for one. So the
// model's account stays a claim, and what Go establishes is which fields moved
// and whether the account covers them.
func TestNormalizeComputesWhichFieldsChanged(t *testing.T) {
	type record struct {
		Country string `json:"country"`
		Phone   string `json:"phone"`
		Name    string `json:"name"`
	}

	// The model normalises two fields and only admits to one.
	installAuditResponse(t, `{
		"normalized": {"country":"US","phone":"+1-305-555-1234","name":"Ada"},
		"changes": [{"field":"country","original":"usa","normalized":"US","reason":"ISO code"}],
		"total_changes": 1
	}`)

	result, err := Normalize(record{Country: "usa", Phone: "3055551234", Name: "Ada"},
		NewNormalizeOptions())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	for _, field := range []string{"country", "phone"} {
		if !includesName(result.ChangedFields, field) {
			t.Errorf("ChangedFields = %v, want it to include %q", result.ChangedFields, field)
		}
	}
	if includesName(result.ChangedFields, "name") {
		t.Errorf("ChangedFields = %v; name is unchanged", result.ChangedFields)
	}

	// The point of Unreported: the model's account is incomplete and says so.
	if !includesName(result.Unreported, "phone") {
		t.Errorf("Unreported = %v, want the change the model did not mention", result.Unreported)
	}
	if includesName(result.Unreported, "country") {
		t.Errorf("Unreported = %v; the model did mention country", result.Unreported)
	}

	// The count follows the diff, because a caller checking TotalChanges is
	// asking what happened to their data.
	if result.TotalChanges != 2 {
		t.Errorf("TotalChanges = %d, want 2 -- the count followed the narrative", result.TotalChanges)
	}
}

// A normalization that changed nothing says so, and reports nothing unreported.
func TestANormalizationThatChangedNothing(t *testing.T) {
	type record struct {
		Country string `json:"country"`
	}

	installAuditResponse(t, `{"normalized":{"country":"US"},"changes":[],"total_changes":0}`)

	result, err := Normalize(record{Country: "US"}, NewNormalizeOptions())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(result.ChangedFields) != 0 {
		t.Errorf("ChangedFields = %v, want none", result.ChangedFields)
	}
	if len(result.Unreported) != 0 {
		t.Errorf("Unreported = %v, want none", result.Unreported)
	}
	if result.TotalChanges != 0 {
		t.Errorf("TotalChanges = %d, want 0", result.TotalChanges)
	}
}

// Values compare canonically, so a formatting difference is not a change.
func TestFormattingIsNotAChange(t *testing.T) {
	type record struct {
		Total float64 `json:"total"`
	}

	installAuditResponse(t, `{"normalized":{"total":1284.50},"changes":[],"total_changes":0}`)

	result, err := Normalize(record{Total: 1284.5}, NewNormalizeOptions())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(result.ChangedFields) != 0 {
		t.Errorf("ChangedFields = %v; 1284.50 and 1284.5 are the same value", result.ChangedFields)
	}
}

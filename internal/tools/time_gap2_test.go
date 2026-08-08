package tools

import (
	"context"
	"testing"
	"time"
)

// isDST's southern-hemisphere branch: January is DST, July is not, so the
// comparison has to run the other way round from America/New_York.
func TestIsDSTSouthernHemisphere(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skipf("no tzdata available: %v", err)
	}
	// Brazil abolished DST in 2019; skip if this offset table has no seasonal
	// change to observe -- the assertion below would be vacuous otherwise.
	jan := time.Date(2018, 1, 15, 12, 0, 0, 0, loc)
	jul := time.Date(2018, 7, 15, 12, 0, 0, 0, loc)
	_, janOff := jan.Zone()
	_, julOff := jul.Zone()
	if janOff == julOff {
		t.Skip("this tzdata has no DST transition for the test year")
	}
	if janOff <= julOff {
		t.Skip("expected January to be DST (ahead of July) in this hemisphere/year")
	}
	if !isDST(jan) {
		t.Error("January should be DST in the southern hemisphere")
	}
	if isDST(jul) {
		t.Error("July should not be DST in the southern hemisphere")
	}
}

func TestExecuteParseTimeCustomFormat(t *testing.T) {
	result, err := ParseTimeTool.Execute(context.Background(), map[string]any{
		"time":   "2024/01/15",
		"format": "custom",
		"custom": "2006/01/02",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if data["date"] != "2024-01-15" {
		t.Errorf("date = %v, want 2024-01-15", data["date"])
	}
}

func TestExecuteParseTimeAppliesRequestedTimezone(t *testing.T) {
	result, err := ParseTimeTool.Execute(context.Background(), map[string]any{
		"time":     "2024-01-15T10:30:00Z",
		"timezone": "UTC",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if data["timezone"] != "UTC" {
		t.Errorf("timezone = %v, want UTC", data["timezone"])
	}
}

func TestFormatDurationNegative(t *testing.T) {
	got := formatDuration(-90 * time.Second)
	if got != "-1m 30s" {
		t.Errorf("formatDuration(-90s) = %q, want -1m 30s", got)
	}
}

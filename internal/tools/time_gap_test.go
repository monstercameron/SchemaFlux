package tools

import (
	"context"
	"testing"
	"time"
)

func TestExecuteNowUnixMilliFormat(t *testing.T) {
	result, err := NowTool.Execute(context.Background(), map[string]any{
		"format": "unixmilli",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
}

func TestExecuteNowCustomFormat(t *testing.T) {
	result, err := NowTool.Execute(context.Background(), map[string]any{
		"format": "custom",
		"custom": "2006",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if len(result.Data.(string)) != 4 {
		t.Errorf("custom format %q did not apply", result.Data)
	}
}

// isDST reports the same answer regardless of season for a zone with no
// seasonal offset change (UTC never observes DST), and correctly reads a
// zone that does.
func TestIsDST(t *testing.T) {
	utcJan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if isDST(utcJan) {
		t.Error("UTC has no DST; isDST must report false")
	}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata available: %v", err)
	}
	summer := time.Date(2024, 7, 1, 12, 0, 0, 0, loc)
	winter := time.Date(2024, 1, 1, 12, 0, 0, 0, loc)
	if !isDST(summer) {
		t.Error("July in America/New_York should be DST")
	}
	if isDST(winter) {
		t.Error("January in America/New_York should not be DST")
	}
}

// executeDuration refuses an unknown action and reports a parse failure for
// each side of "between" independently.
func TestExecuteDurationRefusesAnUnknownAction(t *testing.T) {
	result, err := DurationTool.Execute(context.Background(), map[string]any{
		"action": "multiply",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an unknown action must be refused")
	}
}

func TestExecuteDurationBetweenReportsBadFrom(t *testing.T) {
	result, err := DurationTool.Execute(context.Background(), map[string]any{
		"action": "between",
		"from":   "not a time",
		"to":     "2024-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an unparsable 'from' must fail")
	}
}

func TestExecuteDurationBetweenReportsBadTo(t *testing.T) {
	result, err := DurationTool.Execute(context.Background(), map[string]any{
		"action": "between",
		"from":   "2024-01-01T00:00:00Z",
		"to":     "not a time",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an unparsable 'to' must fail")
	}
}

func TestExecuteDurationAddReportsBadFrom(t *testing.T) {
	result, err := DurationTool.Execute(context.Background(), map[string]any{
		"action": "add",
		"from":   "not a time",
		"amount": "1h",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an unparsable 'from' must fail")
	}
}

func TestExecuteDurationAddReportsBadAmount(t *testing.T) {
	result, err := DurationTool.Execute(context.Background(), map[string]any{
		"action": "add",
		"from":   "2024-01-01T00:00:00Z",
		"amount": "not a duration",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an unparsable amount must fail")
	}
}

func TestExecuteDurationBetweenAcceptsUnixTimestamps(t *testing.T) {
	result, err := DurationTool.Execute(context.Background(), map[string]any{
		"action": "between",
		"from":   "1704067200", // 2024-01-01T00:00:00Z
		"to":     "1704153600", // 2024-01-02T00:00:00Z
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if data["hours"].(float64) != 24 {
		t.Errorf("expected 24 hours, got %v", data["hours"])
	}
}

// executeSchedule reads an explicit "from" when it parses, falls back to now
// when it does not, and applies a timezone when one is given.
func TestExecuteScheduleWithExplicitFrom(t *testing.T) {
	result, err := ScheduleTool.Execute(context.Background(), map[string]any{
		"query": "in 1 day",
		"from":  "2024-01-15T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if data["date"] != "2024-01-16" {
		t.Errorf("expected 2024-01-16, got %v", data["date"])
	}
}

func TestExecuteScheduleWithUnparsableFromFallsBackToNow(t *testing.T) {
	result, err := ScheduleTool.Execute(context.Background(), map[string]any{
		"query": "in 1 hour",
		"from":  "not a time",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("an unparsable 'from' should fall back to now, not fail: %s", result.Error)
	}
}

func TestExecuteScheduleWithTimezone(t *testing.T) {
	result, err := ScheduleTool.Execute(context.Background(), map[string]any{
		"query":    "tomorrow",
		"timezone": "UTC",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
}

func TestExecuteScheduleWithInvalidTimezoneIsIgnored(t *testing.T) {
	result, err := ScheduleTool.Execute(context.Background(), map[string]any{
		"query":    "tomorrow",
		"timezone": "Not/AZone",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("an invalid timezone should be ignored, not fail the call: %s", result.Error)
	}
}

// parseNaturalSchedule: "next <today's weekday>" rolls to next week rather
// than reporting zero days away, and "next <unknown word>" is refused.
func TestParseNaturalScheduleNextTodayRollsAWeek(t *testing.T) {
	monday := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	result, err := parseNaturalSchedule("next monday", monday)
	if err != nil {
		t.Fatalf("parseNaturalSchedule: %v", err)
	}
	if got := result.Sub(monday); got != 7*24*time.Hour {
		t.Errorf("next monday from a monday = %v away, want 7 days", got)
	}
}

func TestParseNaturalScheduleNextUnknownDayIsRefused(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	if _, err := parseNaturalSchedule("next blursday", now); err == nil {
		t.Error("an unrecognised day name after 'next' must be refused")
	}
}

func TestParseNaturalScheduleInWithUnrecognisedUnitIsRefused(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	if _, err := parseNaturalSchedule("in 5 fortnights", now); err == nil {
		t.Error("an unrecognised unit must be refused")
	}
}

func TestParseNaturalScheduleInWithEveryUnit(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	units := []string{
		"in 30 seconds", "in 5 minutes", "in 2 hours",
		"in 1 week", "in 1 month", "in 1 year",
	}
	for _, q := range units {
		if _, err := parseNaturalSchedule(q, now); err != nil {
			t.Errorf("parseNaturalSchedule(%q): %v", q, err)
		}
	}
}

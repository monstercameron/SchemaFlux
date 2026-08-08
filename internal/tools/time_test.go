package tools

import (
	"context"
	"testing"
	"time"
)

func TestNowTool(t *testing.T) {
	result, err := NowTool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Check metadata
	meta := result.Metadata
	if meta["year"].(int) < 2024 {
		t.Error("Expected reasonable year")
	}
	if meta["weekday"] == "" {
		t.Error("Expected weekday")
	}
}

func TestNowToolWithTimezone(t *testing.T) {
	result, _ := NowTool.Execute(context.Background(), map[string]any{
		"timezone": "UTC",
	})
	if !result.Success {
		t.Errorf("Expected success: %s", result.Error)
	}
	if result.Metadata["timezone"] != "UTC" {
		t.Errorf("Expected UTC, got %v", result.Metadata["timezone"])
	}

	// Test invalid timezone
	result, _ = NowTool.Execute(context.Background(), map[string]any{
		"timezone": "Invalid/Zone",
	})
	if result.Success {
		t.Error("Expected failure for invalid timezone")
	}
}

func TestNowToolFormats(t *testing.T) {
	tests := []string{"unix", "date", "time", "datetime", "custom"}
	for _, format := range tests {
		result, _ := NowTool.Execute(context.Background(), map[string]any{
			"format": format,
		})
		if !result.Success {
			t.Errorf("Format %s failed: %s", format, result.Error)
		}
	}
}

func TestParseTimeTool(t *testing.T) {
	tests := []struct {
		time   string
		format string
	}{
		{"2024-01-15T10:30:00Z", ""},
		{"2024-01-15 10:30:00", ""},
		{"2024-01-15", ""},
		{"1705315800", "unix"},
	}

	for _, tt := range tests {
		result, _ := ParseTimeTool.Execute(context.Background(), map[string]any{
			"time":   tt.time,
			"format": tt.format,
		})
		if !result.Success {
			t.Errorf("Parse %q failed: %s", tt.time, result.Error)
		}
	}
}

func TestParseTimeToolInvalid(t *testing.T) {
	result, _ := ParseTimeTool.Execute(context.Background(), map[string]any{
		"time": "not a time",
	})
	if result.Success {
		t.Error("Expected failure for invalid time")
	}
}

func TestDurationToolBetween(t *testing.T) {
	result, _ := DurationTool.Execute(context.Background(), map[string]any{
		"action": "between",
		"from":   "2024-01-01T00:00:00Z",
		"to":     "2024-01-02T00:00:00Z",
	})
	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["hours"].(float64) != 24 {
		t.Errorf("Expected 24 hours, got %v", data["hours"])
	}
	if data["days"].(float64) != 1 {
		t.Errorf("Expected 1 day, got %v", data["days"])
	}
}

func TestDurationToolAdd(t *testing.T) {
	result, _ := DurationTool.Execute(context.Background(), map[string]any{
		"action": "add",
		"from":   "2024-01-01T00:00:00Z",
		"amount": "24h",
	})
	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["date"] != "2024-01-02" {
		t.Errorf("Expected 2024-01-02, got %v", data["date"])
	}
}

func TestDurationToolSubtract(t *testing.T) {
	result, _ := DurationTool.Execute(context.Background(), map[string]any{
		"action": "subtract",
		"from":   "2024-01-02T00:00:00Z",
		"amount": "1d",
	})
	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["date"] != "2024-01-01" {
		t.Errorf("Expected 2024-01-01, got %v", data["date"])
	}
}

func TestScheduleTool(t *testing.T) {
	tests := []struct {
		query string
	}{
		{"in 2 hours"},
		{"in 3 days"},
		{"tomorrow"},
		{"next monday"},
		{"friday"},
	}

	for _, tt := range tests {
		result, _ := ScheduleTool.Execute(context.Background(), map[string]any{
			"query": tt.query,
		})
		if !result.Success {
			t.Errorf("Schedule %q failed: %s", tt.query, result.Error)
		}
	}
}

func TestScheduleToolInvalid(t *testing.T) {
	result, _ := ScheduleTool.Execute(context.Background(), map[string]any{
		"query": "invalid schedule query",
	})
	if result.Success {
		t.Error("Expected failure for invalid schedule")
	}
}

func TestHolidayTool(t *testing.T) {
	// Test New Year's Day
	result, _ := HolidayTool.Execute(context.Background(), map[string]any{
		"date":    "2024-01-01",
		"country": "US",
	})
	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if !data["is_holiday"].(bool) {
		t.Error("Expected New Year's Day to be a holiday")
	}
	if data["holiday"] != "New Year's Day" {
		t.Errorf("Expected 'New Year's Day', got %v", data["holiday"])
	}

	// Test regular weekday
	result, _ = HolidayTool.Execute(context.Background(), map[string]any{
		"date": "2024-01-08", // Monday
	})
	data = result.Data.(map[string]any)
	if data["is_weekend"].(bool) {
		t.Error("Expected Monday to not be weekend")
	}
	if data["is_holiday"].(bool) {
		t.Error("Expected Jan 8 to not be a holiday")
	}

	// Test weekend
	result, _ = HolidayTool.Execute(context.Background(), map[string]any{
		"date": "2024-01-06", // Saturday
	})
	data = result.Data.(map[string]any)
	if !data["is_weekend"].(bool) {
		t.Error("Expected Saturday to be weekend")
	}
}

func TestHolidayToolInvalidDate(t *testing.T) {
	result, _ := HolidayTool.Execute(context.Background(), map[string]any{
		"date": "invalid-date",
	})
	if result.Success {
		t.Error("Expected failure for invalid date")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{2 * time.Hour, "2h"},
		{25 * time.Hour, "1d 1h"},
		{48 * time.Hour, "2d"},
	}

	for _, tt := range tests {
		result := formatDuration(tt.duration)
		if result != tt.expected {
			t.Errorf("formatDuration(%v) = %q, expected %q", tt.duration, result, tt.expected)
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"1h", time.Hour},
		{"30m", 30 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
	}

	for _, tt := range tests {
		result, err := parseDuration(tt.input)
		if err != nil {
			t.Errorf("parseDuration(%q) error: %v", tt.input, err)
			continue
		}
		if result != tt.expected {
			t.Errorf("parseDuration(%q) = %v, expected %v", tt.input, result, tt.expected)
		}
	}
}

func TestParseWeekday(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Weekday
	}{
		{"monday", time.Monday},
		{"mon", time.Monday},
		{"Friday", time.Friday},
		{"sat", time.Saturday},
		{"invalid", -1},
	}

	for _, tt := range tests {
		result := parseWeekday(tt.input)
		if result != tt.expected {
			t.Errorf("parseWeekday(%q) = %v, expected %v", tt.input, result, tt.expected)
		}
	}
}

func TestParseNaturalSchedule(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC) // Monday

	tests := []struct {
		query string
		valid bool
	}{
		{"in 2 hours", true},
		{"in 1 day", true},
		{"tomorrow", true},
		{"next friday", true},
		{"monday", true},
		{"invalid query", false},
	}

	for _, tt := range tests {
		result, err := parseNaturalSchedule(tt.query, now)
		if tt.valid && err != nil {
			t.Errorf("parseNaturalSchedule(%q) unexpected error: %v", tt.query, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("parseNaturalSchedule(%q) expected error, got %v", tt.query, result)
		}
	}
}

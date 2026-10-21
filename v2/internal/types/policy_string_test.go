package types

import "testing"

// AllowsRetention's two branches: an unconfigured ceiling permits only zero
// days (never-fail-open), and a configured ceiling permits anything at or
// under it.
func TestAllowsRetentionBothBranches(t *testing.T) {
	unconfigured := DataPolicy{MaxRetentionDays: RetentionUnspecified}
	if !unconfigured.AllowsRetention(0) {
		t.Error("an unconfigured ceiling refused zero days")
	}
	if unconfigured.AllowsRetention(1) {
		t.Error("an unconfigured ceiling allowed nonzero days")
	}

	configured := DataPolicy{MaxRetentionDays: 30}
	if !configured.AllowsRetention(30) {
		t.Error("a 30-day ceiling refused exactly 30 days")
	}
	if configured.AllowsRetention(31) {
		t.Error("a 30-day ceiling allowed 31 days")
	}
}

// stricterLogging's two branches, reached directly through Tighten so the
// unexported helper stays covered from both directions.
func TestStricterLoggingBothDirections(t *testing.T) {
	aStricter, err := DataPolicy{ContentLogging: LogNoContent}.Tighten(DataPolicy{ContentLogging: LogFullContent})
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if aStricter.ContentLogging != LogNoContent {
		t.Errorf("ContentLogging = %v, want LogNoContent (a is stricter)", aStricter.ContentLogging)
	}

	bStricter, err := DataPolicy{ContentLogging: LogFullContent}.Tighten(DataPolicy{ContentLogging: LogRedactedContent})
	if err != nil {
		t.Fatalf("Tighten: %v", err)
	}
	if bStricter.ContentLogging != LogRedactedContent {
		t.Errorf("ContentLogging = %v, want LogRedactedContent (b is stricter)", bStricter.ContentLogging)
	}
}

func TestContentLoggingLevelString(t *testing.T) {
	cases := []struct {
		l    ContentLoggingLevel
		want string
	}{
		{LogNoContent, "no_content"},
		{LogMetadataOnly, "metadata_only"},
		{LogRedactedContent, "redacted_content"},
		{LogFullContent, "full_content"},
		{ContentLoggingLevel(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.l.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

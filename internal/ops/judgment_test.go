package ops

import (
	"math"
	"testing"
)

func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"critical", "critical"},
		{"error", "error"},
		{"warning", "warning"},
		{"info", "info"},
		{"Critical", "critical"},
		{"CRITICAL", "critical"},
		{"  warning  ", "warning"},
		{"Error\n", "error"},
		{"INFO", "info"},
		{"major", "error"},
		{"minor", "warning"},
		{"high", "error"},
		{"HIGH", "error"},
		{"medium", "warning"},
		{"low", "info"},
		{"fatal", "critical"},
		{"Blocker", "critical"},
		{"warn", "warning"},
		{"notice", "info"},
		{"severe", severityUnknown},
		{"", severityUnknown},
		{"   ", severityUnknown},
		{"urgent", severityUnknown},
		{"criticality", severityUnknown},
		{"5", severityUnknown},
	}
	for _, tc := range cases {
		if got := normalizeSeverity(tc.in); got != tc.want {
			t.Errorf("normalizeSeverity(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSeverityFromFraction(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "critical"},
		{0.9, "critical"},
		{0.89, "error"},
		{0.7, "error"},
		{0.69, "warning"},
		{0.5, "warning"},
		{0.49, "info"},
		{0.3, "info"},
		{0, "info"},
		{7, "critical"},
		{-0.1, severityUnknown},
		{math.NaN(), severityUnknown},
		{math.Inf(1), "critical"},
		{math.Inf(-1), severityUnknown},
	}
	for _, tc := range cases {
		if got := severityFromFraction(tc.in); got != tc.want {
			t.Errorf("severityFromFraction(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAnUnrecognisedSeverityIsNeverTheLeastSeriousOne(t *testing.T) {
	for _, in := range []string{"severe", "blocker-ish", "", "p0"} {
		if got := normalizeSeverity(in); got == "info" {
			t.Errorf("normalizeSeverity(%q) = info: an unreadable severity was reported as the least serious level", in)
		}
	}
}

func TestEvidenceSlice(t *testing.T) {
	if got := evidenceSlice(""); got != nil {
		t.Errorf("evidenceSlice(\"\") = %v", got)
	}
	if got := evidenceSlice("quote"); len(got) != 1 || got[0] != "quote" {
		t.Errorf("evidenceSlice = %v", got)
	}
	if got := evidenceSlice(" "); len(got) != 1 {
		t.Errorf("a blank that is not empty is evidence of a blank: %v", got)
	}
}

package llm

import (
	"strings"
	"testing"
)

// Steering is a set of pure prompt-fragment builders: no network, no state,
// just string assembly. Every preset was at 0% coverage because nothing in
// the suite ever called one -- these pin the two things that matter: the base
// text is the documented preset (not some other one), and additionalContext
// is appended rather than silently dropped.

// TestSteeringPresetsWithNoAdditionalContext proves every preset returns
// exactly its base text, unmodified, when called with nothing else -- the
// common case, since Additional Context is opt-in.
func TestSteeringPresetsWithNoAdditionalContext(t *testing.T) {
	cases := []struct {
		name   string
		fn     func(...string) string
		expect string // a substring unique to this preset's base text
	}{
		{"BusinessTone", Steering.BusinessTone, "professional, clear business language"},
		{"CasualTone", Steering.CasualTone, "friendly, conversational language"},
		{"TechnicalTone", Steering.TechnicalTone, "precise, technical language"},
		{"UrgencyScore", Steering.UrgencyScore, "Rate urgency on scale 0.0-1.0"},
		{"ImportanceScore", Steering.ImportanceScore, "Rate importance on scale 0.0-1.0"},
		{"QualityScore", Steering.QualityScore, "Rate quality on scale 0.0-1.0"},
		{"PrioritySort", Steering.PrioritySort, "Sort by priority considering"},
		{"EffortSort", Steering.EffortSort, "Sort by effort level considering"},
		{"DeadlineSort", Steering.DeadlineSort, "Sort by deadline urgency"},
		{"WorkContext", Steering.WorkContext, "Filter for work-appropriate tasks"},
		{"HomeContext", Steering.HomeContext, "Filter for home environment tasks"},
		{"MobileContext", Steering.MobileContext, "Filter for mobile/on-the-go tasks"},
		{"StrictExtraction", Steering.StrictExtraction, "Extract data with strict validation"},
		{"FlexibleExtraction", Steering.FlexibleExtraction, "Extract data with flexible interpretation"},
		{"DetailedExtraction", Steering.DetailedExtraction, "Extract comprehensive details"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fn()
			if !strings.Contains(got, tc.expect) {
				t.Errorf("%s() = %q, want it to contain %q", tc.name, got, tc.expect)
			}
			if strings.Contains(got, "Additional Context:") {
				t.Errorf("%s() with no arguments carried an Additional Context section: %q", tc.name, got)
			}
		})
	}
}

// TestSteeringPresetsAppendAdditionalContext proves a caller's extra context
// is appended after the base text, under its own heading, for every preset --
// not just the first one tried.
func TestSteeringPresetsAppendAdditionalContext(t *testing.T) {
	cases := []struct {
		name string
		fn   func(...string) string
	}{
		{"BusinessTone", Steering.BusinessTone},
		{"CasualTone", Steering.CasualTone},
		{"TechnicalTone", Steering.TechnicalTone},
		{"UrgencyScore", Steering.UrgencyScore},
		{"ImportanceScore", Steering.ImportanceScore},
		{"QualityScore", Steering.QualityScore},
		{"PrioritySort", Steering.PrioritySort},
		{"EffortSort", Steering.EffortSort},
		{"DeadlineSort", Steering.DeadlineSort},
		{"WorkContext", Steering.WorkContext},
		{"HomeContext", Steering.HomeContext},
		{"MobileContext", Steering.MobileContext},
		{"StrictExtraction", Steering.StrictExtraction},
		{"FlexibleExtraction", Steering.FlexibleExtraction},
		{"DetailedExtraction", Steering.DetailedExtraction},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withoutContext := tc.fn()
			withContext := tc.fn("caller-supplied extra context")

			if !strings.HasPrefix(withContext, withoutContext) {
				t.Errorf("%s(extra) does not start with the base text", tc.name)
			}
			if !strings.Contains(withContext, "Additional Context:") {
				t.Errorf("%s(extra) = %q, missing the Additional Context heading", tc.name, withContext)
			}
			if !strings.Contains(withContext, "caller-supplied extra context") {
				t.Errorf("%s(extra) dropped the caller's context: %q", tc.name, withContext)
			}
		})
	}
}

// TestSteeringPresetsAppendMultipleContextsInOrder proves several context
// strings are each appended, in the order given, and an empty string among
// them contributes nothing (appendContext's own "if context != \"\"" guard).
func TestSteeringPresetsAppendMultipleContextsInOrder(t *testing.T) {
	got := Steering.BusinessTone("first", "", "second")

	firstIdx := strings.Index(got, "first")
	secondIdx := strings.Index(got, "second")
	if firstIdx < 0 || secondIdx < 0 || firstIdx > secondIdx {
		t.Fatalf("context order not preserved: %q", got)
	}
	if strings.Count(got, "Additional Context:") != 2 {
		t.Errorf("got %d Additional Context sections for two non-empty contexts, want 2: %q",
			strings.Count(got, "Additional Context:"), got)
	}
}

// TestAppendContextWithNoArguments proves the zero-variadic-arg path returns
// base completely unmodified -- appendContext's early return.
func TestAppendContextWithNoArguments(t *testing.T) {
	if got := appendContext("base text"); got != "base text" {
		t.Errorf("appendContext(base) = %q, want %q unmodified", got, "base text")
	}
}

// TestAppendContextSkipsEmptyStrings proves an all-empty additional context
// list produces no Additional Context section at all.
func TestAppendContextSkipsEmptyStrings(t *testing.T) {
	got := appendContext("base", "", "")
	if got != "base" {
		t.Errorf("appendContext(base, \"\", \"\") = %q, want %q unmodified", got, "base")
	}
}

// TestCustomerServiceTone proves the exported helper wraps BusinessTone with
// its own fixed customer-service context and forwards the caller's context
// through to it.
func TestCustomerServiceTone(t *testing.T) {
	got := CustomerServiceTone("VIP account, escalate politely")

	for _, want := range []string{
		"professional, clear business language", // BusinessTone's base
		"Customer service guidelines:",
		"Empathetic and understanding",
		"Solution-focused responses",
		"Acknowledge concerns explicitly",
		"Provide clear next steps",
		"VIP account, escalate politely",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CustomerServiceTone(...) missing %q in: %q", want, got)
		}
	}
}

// TestCustomerServiceToneWithNoSpecificContext proves the caller's variadic
// specificContext may be omitted entirely -- strings.Join of an empty slice
// contributes an empty string, which appendContext then skips.
func TestCustomerServiceToneWithNoSpecificContext(t *testing.T) {
	got := CustomerServiceTone()
	if !strings.Contains(got, "Customer service guidelines:") {
		t.Errorf("CustomerServiceTone() lost its own fixed context: %q", got)
	}
}

// TestUrgentWorkTasks proves the exported helper combines UrgencyScore and
// WorkContext, each carrying its own labeled argument.
func TestUrgentWorkTasks(t *testing.T) {
	got := UrgentWorkTasks("2026-08-15", "laptop and VPN access")

	for _, want := range []string{
		"Rate urgency on scale 0.0-1.0",
		"Deadline: 2026-08-15",
		"Filter for work-appropriate tasks",
		"Available resources: laptop and VPN access",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("UrgentWorkTasks(...) missing %q in: %q", want, got)
		}
	}

	// The two presets must be joined by a blank line, per the doc: "\n\n" +
	// WorkContext output.
	urgencyEnd := strings.Index(got, "Filter for work-appropriate tasks")
	if urgencyEnd <= 0 {
		t.Fatalf("WorkContext section not found: %q", got)
	}
	if !strings.Contains(got[:urgencyEnd], "\n\n") {
		t.Errorf("UrgencyScore and WorkContext sections are not blank-line separated: %q", got)
	}
}

// TestProjectSpecificSort proves the exported helper renders all three
// project parameters into PrioritySort's additional context, each on the
// documented label.
func TestProjectSpecificSort(t *testing.T) {
	got := ProjectSpecificSort("greenfield API", "6 weeks", "4")

	for _, want := range []string{
		"Sort by priority considering",
		"Project type: greenfield API",
		"Timeline: 6 weeks",
		"Team size: 4 people",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ProjectSpecificSort(...) missing %q in: %q", want, got)
		}
	}
}

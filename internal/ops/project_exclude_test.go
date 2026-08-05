package ops

import (
	"encoding/json"
	"strings"
	"testing"
)

// Exclude used to be interpolated into the prompt as a hint, so the value was
// serialised and sent, and the model was politely asked not to look at it.
// These assert the value is gone from the bytes before anything leaves.
func TestMarshalWithoutExcludedRemovesTheValue(t *testing.T) {
	const ssn = "123-45-6789"
	const hash = "$2y$10$abcdefghijklmnopqrstuv"

	cases := []struct {
		name    string
		input   any
		exclude []string
		absent  []string
		present []string
	}{
		{
			"top_level_field",
			map[string]any{"id": "u1", "ssn": ssn},
			[]string{"ssn"},
			[]string{ssn}, []string{"u1"},
		},
		{
			"several_fields",
			map[string]any{"id": "u1", "ssn": ssn, "password_hash": hash},
			[]string{"ssn", "password_hash"},
			[]string{ssn, hash}, []string{"u1"},
		},
		{
			"case_insensitive_name",
			map[string]any{"id": "u1", "SSN": ssn},
			[]string{"ssn"},
			[]string{ssn}, []string{"u1"},
		},
		{
			"case_insensitive_exclusion",
			map[string]any{"id": "u1", "ssn": ssn},
			[]string{"SSN"},
			[]string{ssn}, []string{"u1"},
		},
		{
			"nested_object",
			map[string]any{"id": "u1", "profile": map[string]any{"ssn": ssn, "city": "Lauderhill"}},
			[]string{"ssn"},
			[]string{ssn}, []string{"Lauderhill"},
		},
		{
			"deeply_nested",
			map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"ssn": ssn}}}},
			[]string{"ssn"},
			[]string{ssn}, nil,
		},
		{
			"inside_an_array",
			map[string]any{"people": []any{
				map[string]any{"name": "Ada", "ssn": ssn},
				map[string]any{"name": "Alan", "ssn": "987-65-4321"},
			}},
			[]string{"ssn"},
			[]string{ssn, "987-65-4321"}, []string{"Ada", "Alan"},
		},
		{
			"whitespace_in_the_exclusion_is_trimmed",
			map[string]any{"id": "u1", "ssn": ssn},
			[]string{"  ssn  "},
			[]string{ssn}, []string{"u1"},
		},
		{
			"empty_exclusion_entry_is_ignored",
			map[string]any{"id": "u1", "ssn": ssn},
			[]string{"", "   ", "ssn"},
			[]string{ssn}, []string{"u1"},
		},
		{
			"non_matching_exclusion_changes_nothing",
			map[string]any{"id": "u1", "ssn": ssn},
			[]string{"not_a_field"},
			nil, []string{"u1", ssn},
		},
		{
			"no_exclusions_changes_nothing",
			map[string]any{"id": "u1", "ssn": ssn},
			nil,
			nil, []string{"u1", ssn},
		},
		{
			"struct_input_uses_json_names",
			struct {
				ID  string `json:"id"`
				SSN string `json:"ssn"`
			}{"u1", ssn},
			[]string{"ssn"},
			[]string{ssn}, []string{"u1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, _, err := marshalWithoutExcluded(tc.input, tc.exclude)
			if err != nil {
				t.Fatalf("marshalWithoutExcluded: %v", err)
			}
			body := string(payload)

			for _, absent := range tc.absent {
				if strings.Contains(body, absent) {
					t.Errorf("the payload still carries %q:\n%s", absent, body)
				}
			}
			for _, present := range tc.present {
				if !strings.Contains(body, present) {
					t.Errorf("the payload lost %q, which was not excluded:\n%s", present, body)
				}
			}
		})
	}
}

// The removed values are reported, so the output can be checked against them.
func TestMarshalWithoutExcludedReportsWhatItRemoved(t *testing.T) {
	removedFrom := func(input any, exclude []string) map[string]string {
		_, removed, err := marshalWithoutExcluded(input, exclude)
		if err != nil {
			t.Fatalf("marshalWithoutExcluded: %v", err)
		}
		return removed
	}

	t.Run("scalars_are_recorded", func(t *testing.T) {
		removed := removedFrom(map[string]any{"ssn": "123-45-6789", "age": 30}, []string{"ssn", "age"})
		if removed["ssn"] != "123-45-6789" {
			t.Errorf("ssn = %q", removed["ssn"])
		}
		if removed["age"] != "30" {
			t.Errorf("age = %q", removed["age"])
		}
	})

	t.Run("objects_are_not_recorded_individually", func(t *testing.T) {
		removed := removedFrom(map[string]any{"secret": map[string]any{"a": "b"}}, []string{"secret"})
		if len(removed) != 0 {
			t.Errorf("a removed object should not be recorded as a scalar: %v", removed)
		}
	})

	t.Run("nothing_removed", func(t *testing.T) {
		if removed := removedFrom(map[string]any{"a": 1}, []string{"b"}); len(removed) != 0 {
			t.Errorf("removed = %v", removed)
		}
	})
}

// The post-scan is the second line: if a removed value ever reaches the output,
// that is an error rather than a projection.
func TestFindLeakedValues(t *testing.T) {
	cases := []struct {
		name      string
		projected any
		removed   map[string]string
		want      string
	}{
		{"clean", map[string]any{"user_id": "u1"}, map[string]string{"ssn": "123-45-6789"}, ""},
		{"leaked_verbatim", map[string]any{"note": "ssn is 123-45-6789"}, map[string]string{"ssn": "123-45-6789"}, "ssn"},
		{"leaked_in_a_different_case", map[string]any{"note": "ABCDEF-GHIJ"}, map[string]string{"code": "abcdef-ghij"}, "code"},
		{"leaked_nested", map[string]any{"a": map[string]any{"b": "123-45-6789"}}, map[string]string{"ssn": "123-45-6789"}, "ssn"},
		{"nothing_removed", map[string]any{"note": "anything"}, nil, ""},
		{"short_values_are_not_scanned", map[string]any{"country": "US"}, map[string]string{"code": "US"}, ""},
		{"boolean_is_not_scanned", map[string]any{"flag": true}, map[string]string{"admin": "true"}, ""},
		{"long_value_is_scanned", map[string]any{"x": "supersecretvalue"}, map[string]string{"token": "supersecretvalue"}, "token"},
		{"partial_is_not_a_match", map[string]any{"x": "123-45"}, map[string]string{"ssn": "123-45-6789"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findLeakedValues(tc.projected, tc.removed); got != tc.want {
				t.Errorf("findLeakedValues = %q, want %q", got, tc.want)
			}
		})
	}
}

// End to end through the operation: the excluded value must not appear in the
// bytes the provider receives.
func TestProjectNeverSendsAnExcludedValue(t *testing.T) {
	type internalUser struct {
		ID           string `json:"id"`
		Email        string `json:"email"`
		PasswordHash string `json:"password_hash"`
		SSN          string `json:"ssn"`
	}
	type publicProfile struct {
		UserID string `json:"user_id"`
	}

	const ssn = "123-45-6789"
	const hash = "$2y$10$abcdefghijklmnopqrstuv"

	prompts := capturePrompts(t, func() {
		_, _ = Project[internalUser, publicProfile](
			internalUser{ID: "u1", Email: "ada@example.com", PasswordHash: hash, SSN: ssn},
			ProjectOptions{Exclude: []string{"ssn", "password_hash"}})
	})
	if len(prompts) == 0 {
		t.Fatal("the operation made no call, so this witnesses nothing")
	}
	sent := strings.Join(prompts, "\n")

	for _, secret := range []string{ssn, hash} {
		if strings.Contains(sent, secret) {
			t.Errorf("the prompt carries the excluded value %q", secret)
		}
	}
	if !strings.Contains(sent, "ada@example.com") {
		t.Error("a field that was not excluded should still be sent")
	}
}

// And if one ever comes back, the projection is an error.
func TestProjectRefusesAProjectionCarryingAnExcludedValue(t *testing.T) {
	type record struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	type projected struct {
		Note string `json:"note"`
	}

	body, err := json.Marshal(map[string]any{
		"projected":  map[string]any{"note": "the token is supersecretvalue"},
		"confidence": 0.9,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	restore := stubLLM(string(body))
	defer restore()

	_, projectErr := Project[record, projected](
		record{ID: "u1", Token: "supersecretvalue"},
		ProjectOptions{Exclude: []string{"token"}})

	if projectErr == nil {
		t.Fatal("a projection containing an excluded value must be an error")
	}
	if !strings.Contains(projectErr.Error(), "token") {
		t.Errorf("the error should name the excluded field, got %v", projectErr)
	}
}

// A clean projection still works.
func TestProjectSucceedsWhenNothingLeaked(t *testing.T) {
	type record struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	type projected struct {
		UserID string `json:"user_id"`
	}

	restore := stubLLM(`{"projected":{"user_id":"u1"},"confidence":0.9}`)
	defer restore()

	result, err := Project[record, projected](
		record{ID: "u1", Token: "supersecretvalue"},
		ProjectOptions{Exclude: []string{"token"}})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if result.Projected.UserID != "u1" {
		t.Errorf("Projected = %+v", result.Projected)
	}
}

// sortedStrings keeps the prompt bytes independent of the caller's slice order,
// which is what CA-001 is about.
func TestSortedStrings(t *testing.T) {
	first := sortedStrings([]string{"ssn", "password_hash", "email"})
	second := sortedStrings([]string{"email", "ssn", "password_hash"})

	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("%v and %v should render identically", first, second)
	}
	if first[0] != "email" {
		t.Errorf("sortedStrings = %v, want email first", first)
	}
}

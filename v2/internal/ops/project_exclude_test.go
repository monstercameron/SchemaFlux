package ops

import (
	"encoding/json"
	"fmt"
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
	removedFrom := func(input any, exclude []string) excludedValues {
		_, removed, err := marshalWithoutExcluded(input, exclude)
		if err != nil {
			t.Fatalf("marshalWithoutExcluded: %v", err)
		}
		return removed
	}

	t.Run("scalars_are_recorded", func(t *testing.T) {
		removed := removedFrom(map[string]any{"ssn": "123-45-6789", "age": 30}, []string{"ssn", "age"})
		if len(removed["ssn"]) != 1 || removed["ssn"][0].text != "123-45-6789" {
			t.Errorf("ssn = %+v", removed["ssn"])
		}
		if len(removed["age"]) != 1 || removed["age"][0].text != "30" || removed["age"][0].number == nil {
			t.Errorf("age = %+v", removed["age"])
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
		removed   excludedValues
		want      string
	}{
		{"clean", map[string]any{"user_id": "u1"}, removedValues("ssn", "123-45-6789"), ""},
		{"leaked_verbatim", map[string]any{"note": "ssn is 123-45-6789"}, removedValues("ssn", "123-45-6789"), "ssn"},
		{"leaked_in_a_different_case", map[string]any{"note": "ABCDEF-GHIJ"}, removedValues("code", "abcdef-ghij"), "code"},
		{"leaked_nested", map[string]any{"a": map[string]any{"b": "123-45-6789"}}, removedValues("ssn", "123-45-6789"), "ssn"},
		{"nothing_removed", map[string]any{"note": "anything"}, nil, ""},
		{"short_values_are_not_scanned", map[string]any{"country": "US"}, removedValues("code", "US"), ""},
		{"boolean_is_not_scanned", map[string]any{"flag": true}, removedValues("admin", true), ""},
		{"long_value_is_scanned", map[string]any{"x": "supersecretvalue"}, removedValues("token", "supersecretvalue"), "token"},
		{"partial_is_not_a_match", map[string]any{"x": "123-45"}, removedValues("ssn", "123-45-6789"), ""},
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
		"mappings":   []any{},
		"lost":       []string{},
		"inferred":   []string{},
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

	restore := stubLLM(`{"projected":{"user_id":"u1"},"confidence":0.9,"mappings":[],"lost":[],"inferred":[]}`)
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

// removedValues builds the removed values of one field for a scan test. A
// json.Number is a removed number; anything else is a removed string or boolean.
func removedValues(field string, values ...any) excludedValues {
	removed := excludedValues{}
	for _, value := range values {
		if kept, ok := newExcludedValue(value); ok {
			removed[field] = append(removed[field], kept)
		}
	}
	return removed
}

// A list of records carries one value per record under the same field name.
// Keeping the last one meant a model echoing any other record's value was not
// caught.
func TestMarshalWithoutExcludedKeepsEveryRemovedValueOfAList(t *testing.T) {
	users := make([]map[string]any, 100)
	for i := range users {
		users[i] = map[string]any{"name": fmt.Sprintf("user-%03d", i), "ssn": fmt.Sprintf("900-00-%04d", i)}
	}
	_, removed, err := marshalWithoutExcluded(map[string]any{"users": users}, []string{"ssn"})
	if err != nil {
		t.Fatalf("marshalWithoutExcluded: %v", err)
	}
	if len(removed["ssn"]) != 100 {
		t.Fatalf("kept %d of 100 values", len(removed["ssn"]))
	}

	for _, i := range []int{0, 1, 42, 98, 99} {
		leak := map[string]any{"note": fmt.Sprintf("the number is 900-00-%04d", i)}
		if got := findLeakedValues(leak, removed); got != "ssn" {
			t.Errorf("a leak of record %d was not found (got %q)", i, got)
		}
	}
	if got := findLeakedValues(map[string]any{"note": "nothing of the sort"}, removed); got != "" {
		t.Errorf("a clean output was reported as leaking %q", got)
	}
}

func TestMarshalWithoutExcludedLeavesNumbersAndOrderAlone(t *testing.T) {
	type record struct {
		Zeta  int64  `json:"zeta"`
		Alpha string `json:"alpha"`
		SSN   string `json:"ssn"`
		Mid   uint64 `json:"mid"`
		Rate  string `json:"rate"`
	}
	input := record{Zeta: 9007199254740993, Alpha: "a<b&c", SSN: "123-45-6789", Mid: 18446744073709551615, Rate: "x"}

	without, _, err := marshalWithoutExcluded(input, nil)
	if err != nil {
		t.Fatalf("marshalWithoutExcluded: %v", err)
	}
	with, removed, err := marshalWithoutExcluded(input, []string{"ssn"})
	if err != nil {
		t.Fatalf("marshalWithoutExcluded: %v", err)
	}

	want := `{"zeta":9007199254740993,"alpha":"a\u003cb\u0026c","mid":18446744073709551615,"rate":"x"}`
	if string(with) != want {
		t.Errorf("payload with Exclude:\n got %s\nwant %s", with, want)
	}
	if !strings.Contains(string(without), "9007199254740993") || !strings.Contains(string(without), "18446744073709551615") {
		t.Errorf("payload without Exclude changed a number: %s", without)
	}
	if len(removed["ssn"]) != 1 {
		t.Errorf("removed = %+v", removed)
	}
}

func TestMarshalWithoutExcludedKeepsAnythingThatIsNotAnObject(t *testing.T) {
	for _, input := range []any{"plain text", 42, []int{1, 2, 3}, nil, true, []map[string]any{}} {
		payload, removed, err := marshalWithoutExcluded(input, []string{"ssn"})
		if err != nil {
			t.Errorf("%v: %v", input, err)
			continue
		}
		want, _ := json.Marshal(input)
		if string(payload) != string(want) {
			t.Errorf("%v: payload = %s, want %s", input, payload, want)
		}
		if len(removed) != 0 {
			t.Errorf("%v: removed = %+v", input, removed)
		}
	}
	if _, _, err := marshalWithoutExcluded(make(chan int), []string{"ssn"}); err == nil {
		t.Error("an input that cannot be marshalled was accepted")
	}
}

// An excluded number is looked for as a number. valueText rendered it with %v
// on a float64, which writes anything from a million up in exponent form, and
// no output ever contains that text.
func TestFindLeakedValuesComparesNumbersAsNumbers(t *testing.T) {
	number := func(literal string) excludedValues {
		return removedValues("id", json.Number(literal))
	}
	cases := []struct {
		name      string
		removed   excludedValues
		projected any
		want      string
	}{
		{"an integer written as an integer", number("123456789"), map[string]any{"n": 123456789}, "id"},
		{"an integer written in exponent form", number("123456789"), json.RawMessage(`{"n":1.23456789e8}`), "id"},
		{"an exponent written as an integer", number("1.23456789e8"), map[string]any{"n": 123456789}, "id"},
		{"a decimal with a trailing zero", number("1234567.50"), json.RawMessage(`{"n":1234567.5}`), "id"},
		{"an integer past two to the fifty-third", number("9007199254740993"), json.RawMessage(`{"n":9007199254740993}`), "id"},
		{"the float neighbour of such an integer is a different number", number("9007199254740993"), json.RawMessage(`{"n":9007199254740992}`), ""},
		{"the digits inside a string", number("123456789"), map[string]any{"note": "account 123456789 closed"}, "id"},
		{"a negative number leaking as its magnitude", number("-123456789"), map[string]any{"n": 123456789}, "id"},
		{"a short number is not scanned", number("12345"), map[string]any{"n": 12345}, ""},
		{"a different number", number("123456789"), map[string]any{"n": 123456780}, ""},
		{"a larger number that contains the digits is a different number", number("123456789"), map[string]any{"n": 1234567890}, ""},
		{"nothing numeric in the output", number("123456789"), map[string]any{"note": "none"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findLeakedValues(tc.projected, tc.removed); got != tc.want {
				t.Errorf("findLeakedValues = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindLeakedValuesChecksEveryValueOfEveryName(t *testing.T) {
	removed := excludedValues{}
	for _, value := range []any{"first-secret-value", "second-secret-value", "third-secret-value"} {
		kept, _ := newExcludedValue(value)
		removed["token"] = append(removed["token"], kept)
	}
	kept, _ := newExcludedValue(json.Number("5550001234"))
	removed["phone"] = []excludedValue{kept}
	// A value too short to look for is skipped; the ones after it are still
	// looked for.
	for _, value := range []any{"ab", "mixed-secret-value"} {
		kept, _ := newExcludedValue(value)
		removed["mixed"] = append(removed["mixed"], kept)
	}

	cases := []struct {
		output string
		want   string
	}{
		{`{"x":"nothing"}`, ""},
		{`{"x":"has mixed-secret-value inside"}`, "mixed"},
		{`{"x":"first-secret-value"}`, "token"},
		{`{"x":"the second-secret-value is here"}`, "token"},
		{`{"x":"THIRD-SECRET-VALUE"}`, "token"},
		{`{"x":5550001234}`, "phone"},
		{`{"x":[{"y":{"z":5550001234}}]}`, "phone"},
	}
	for _, tc := range cases {
		var output any
		if err := json.Unmarshal([]byte(tc.output), &output); err != nil {
			t.Fatal(err)
		}
		if got := findLeakedValues(output, removed); got != tc.want {
			t.Errorf("%s: findLeakedValues = %q, want %q", tc.output, got, tc.want)
		}
	}
}

func TestNewExcludedValue(t *testing.T) {
	cases := []struct {
		name       string
		value      any
		wantKept   bool
		wantText   string
		wantNumber bool
	}{
		{"a string is trimmed", "  secret  ", true, "secret", false},
		{"a blank string is not kept", "   ", false, "", false},
		{"true", true, true, "true", false},
		{"false", false, true, "false", false},
		{"an integer", json.Number("123456789"), true, "123456789", true},
		{"a negative integer loses its sign in the digits", json.Number("-42"), true, "42", true},
		{"an exponent is written out", json.Number("1.5e3"), true, "1500", true},
		{"a decimal keeps its digits", json.Number("12.375"), true, "12.375", true},
		{"a decimal keeps its trailing zeros", json.Number("1234.500"), true, "1234.500", true},
		{"a negative decimal keeps its digits and loses its sign", json.Number("-1234.500"), true, "1234.500", true},
		{"a literal at the length bound is read as a number", json.Number(strings.Repeat("1", maxNumberLiteral)), true, strings.Repeat("1", maxNumberLiteral), true},
		{"a literal over the length bound is kept as text only", json.Number(strings.Repeat("1", maxNumberLiteral+1)), true, strings.Repeat("1", maxNumberLiteral+1), false},
		{"an object is not kept", map[string]any{"a": "b"}, false, "", false},
		{"a list is not kept", []any{"a"}, false, "", false},
		{"null is not kept", nil, false, "", false},
		{"a float64 is not a decoded number here", 3.5, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, kept := newExcludedValue(tc.value)
			if kept != tc.wantKept {
				t.Fatalf("kept = %v, want %v", kept, tc.wantKept)
			}
			if !kept {
				return
			}
			if got.text != tc.wantText || (got.number != nil) != tc.wantNumber {
				t.Errorf("value = %+v, want text %q, number %v", got, tc.wantText, tc.wantNumber)
			}
		})
	}
}

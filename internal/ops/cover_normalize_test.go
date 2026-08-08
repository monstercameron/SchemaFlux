package ops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// This file raises coverage on normalize.go's under-covered paths (Validate's
// case-name rejection, NormalizeText's two outcomes, the computed diff
// helpers changedFieldsBetween/jsonValuesOf) and on numeric.go's fidelity
// check, called directly since both live in this package. It reuses
// installNormalizeCaller (normalize_batch_test.go, same package) to script
// providers with no network access.

// ---- normalize.go ----

// NormalizeOptions.Validate refuses a case name outside the documented set --
// an "out-of-range option" refusal, not a silent no-op.
func TestNormalizeOptionsValidate_RejectsUnknownCase(t *testing.T) {
	opts := NewNormalizeOptions()
	opts.NormalizeCase = "shouty"
	if err := opts.Validate(); err == nil {
		t.Fatal("Validate accepted an unrecognised NormalizeCase")
	}

	for _, valid := range []string{"", "lower", "upper", "title", "sentence"} {
		opts.NormalizeCase = valid
		if err := opts.Validate(); err != nil {
			t.Errorf("Validate rejected the documented case %q: %v", valid, err)
		}
	}
}

// A type json cannot marshal must fail Normalize before any provider call --
// an "invalid options"-shaped refusal at the input boundary, not a call that
// would only fail server-side.
type unmarshalableNormalizeInput struct {
	C chan int
}

func TestNormalize_UnmarshalableInputRefusesBeforeCallingProvider(t *testing.T) {
	calls := 0
	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{"normalized":{},"changes":[],"total_changes":0}`, nil
	})

	_, err := Normalize(unmarshalableNormalizeInput{C: make(chan int)}, NewNormalizeOptions())
	if err == nil {
		t.Fatal("Normalize accepted a value json.Marshal cannot encode")
	}
	if calls != 0 {
		t.Errorf("Normalize called the provider %d times despite failing to marshal the input first", calls)
	}
}

// Normalize's own Validate() failure short-circuits before touching the
// provider.
func TestNormalize_InvalidOptionsRefusesBeforeCallingProvider(t *testing.T) {
	calls := 0
	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{}`, nil
	})

	opts := NewNormalizeOptions()
	opts.NormalizeCase = "not-a-real-case"
	_, err := Normalize(map[string]string{"a": "b"}, opts)
	if err == nil {
		t.Fatal("Normalize accepted invalid options")
	}
	if calls != 0 {
		t.Errorf("Normalize called the provider despite invalid options")
	}
}

// A provider that returns a body ParseJSONStrict cannot decode into
// NormalizeResult's response shape must fail the operation.
func TestNormalize_UndecodableResponseFails(t *testing.T) {
	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json at all", nil
	})

	_, err := Normalize(map[string]string{"a": "b"}, NewNormalizeOptions())
	if err == nil {
		t.Fatal("Normalize accepted a response it could not parse")
	}
}

// A provider error propagates as a Normalize error.
func TestNormalize_ProviderErrorPropagates(t *testing.T) {
	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})

	_, err := Normalize(map[string]string{"a": "b"}, NewNormalizeOptions())
	if err == nil {
		t.Fatal("Normalize succeeded despite the provider call failing")
	}
}

// Normalize's success path also exercises the instruction-building branches
// (Standard, Rules, NormalizeCase, mappings, Fields, SkipFields, Locale,
// Strict) all at once, and checks that ChangedFields/Unreported are computed
// from the value diff rather than merely echoing the model's Changes list.
func TestNormalize_SuccessComputesChangedFieldsFromValueDiff(t *testing.T) {
	type record struct {
		City    string `json:"city"`
		Country string `json:"country"`
	}

	installNormalizeCaller(t, func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return `{
			"normalized": {"city": "New York", "country": "United States"},
			"changes": [{"field": "city", "original": "new york", "normalized": "New York"}],
			"warnings": ["country change not explained"]
		}`, nil
	})

	opts := NewNormalizeOptions().
		WithStandard("US_POSTAL").
		WithRules(map[string]string{"city": "proper case"}).
		WithNormalizeCase("title").
		WithCanonicalMappings(map[string]string{"USA": "United States"}).
		WithFields([]string{"city", "country"}).
		WithSkipFields([]string{"id"}).
		WithLocale("en-US").
		WithStrict(true)

	result, err := Normalize(record{City: "new york", Country: "USA"}, opts)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if result.Normalized.City != "New York" {
		t.Errorf("Normalized.City = %q", result.Normalized.City)
	}

	// Both fields actually changed, computed from the diff -- not merely the
	// one the model happened to mention in Changes.
	if len(result.ChangedFields) != 2 {
		t.Errorf("ChangedFields = %v, want both city and country", result.ChangedFields)
	}
	// Country changed but the model's account never mentioned it.
	found := false
	for _, f := range result.Unreported {
		if f == "country" {
			found = true
		}
	}
	if !found {
		t.Errorf("Unreported = %v, want it to include the field the model's account left out", result.Unreported)
	}
	if result.TotalChanges != len(result.ChangedFields) {
		t.Errorf("TotalChanges = %d, want it to follow the computed diff (%d)", result.TotalChanges, len(result.ChangedFields))
	}
}

// NormalizeText's success path formats the normalized value as text.
func TestNormalizeText_Success(t *testing.T) {
	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"normalized":"hello world","changes":[],"total_changes":0}`, nil
	})

	got, err := NormalizeText("hello   world", NewNormalizeOptions())
	if err != nil {
		t.Fatalf("NormalizeText: %v", err)
	}
	if got != "hello world" {
		t.Errorf("NormalizeText = %q, want %q", got, "hello world")
	}
}

// NormalizeText's failure path passes the underlying error through rather
// than masking it with an empty string and a nil error.
func TestNormalizeText_FailurePropagates(t *testing.T) {
	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})

	got, err := NormalizeText("hello world", NewNormalizeOptions())
	if err == nil {
		t.Fatal("NormalizeText succeeded despite the provider call failing")
	}
	if got != "" {
		t.Errorf("NormalizeText returned %q alongside an error, want empty", got)
	}
}

// changedFieldsBetween, called directly: a field removed from the result, a
// field added to the result, and a field whose value is a reordered-but-equal
// number must sort into "changed" or "unchanged" correctly.
func TestChangedFieldsBetween(t *testing.T) {
	before := map[string]any{"a": 1, "b": 2, "amount": 1284.50}
	after := map[string]any{"a": 1, "c": 3, "amount": 1284.5}

	got := changedFieldsBetween(before, after)

	want := map[string]bool{"b": true, "c": true}
	if len(got) != len(want) {
		t.Fatalf("changedFieldsBetween = %v, want exactly %v", got, want)
	}
	for _, field := range got {
		if !want[field] {
			t.Errorf("unexpected field in diff: %q (full: %v)", field, got)
		}
	}
	// "a" is identical and "amount" is the same rational under two spellings --
	// neither should appear.
	for _, field := range got {
		if field == "a" || field == "amount" {
			t.Errorf("changedFieldsBetween reported an unchanged field: %q", field)
		}
	}
}

// jsonValuesOf's two failure branches: a value json.Marshal cannot encode,
// and (indirectly) a value that marshals to something other than a JSON
// object, both fall back to an empty map rather than panicking.
func TestJSONValuesOf_FallsBackOnNonObjectOrUnmarshalable(t *testing.T) {
	if got := jsonValuesOf(make(chan int)); len(got) != 0 {
		t.Errorf("jsonValuesOf(chan) = %v, want empty", got)
	}
	// A slice marshals to a JSON array, which does not unmarshal into
	// map[string]any.
	if got := jsonValuesOf([]int{1, 2, 3}); len(got) != 0 {
		t.Errorf("jsonValuesOf([]int) = %v, want empty", got)
	}
}

// fieldsNotMentioned is exercised as part of TestNormalize_Success above;
// this pins the case-insensitive matching directly, since the model's field
// name and the computed diff's key casing are not guaranteed to agree.
func TestFieldsNotMentioned_IsCaseInsensitive(t *testing.T) {
	changed := []string{"City", "country"}
	reported := []NormalizeChange{{Field: "city"}, {Field: "COUNTRY"}}

	got := fieldsNotMentioned(changed, reported)
	if len(got) != 0 {
		t.Errorf("fieldsNotMentioned = %v, want none (case-insensitive match)", got)
	}
}

// ---- numeric.go ----

// CheckNumericFidelity's early-out: a body that is not decodable JSON at all
// (extractJSON finds nothing usable) is not this check's business, so it
// must report no loss rather than erroring on the caller's malformed input a
// second time.
func TestCheckNumericFidelity_UndecodableBodyReportsNoLoss(t *testing.T) {
	if err := CheckNumericFidelity("not json", map[string]any{"a": 1}); err != nil {
		t.Errorf("CheckNumericFidelity on an undecodable body = %v, want nil", err)
	}
}

// A decoded value json.Marshal cannot re-encode (a channel) cannot be
// compared, so the check must not error on its own limitation.
func TestCheckNumericFidelity_UnencodableDecodedValueReportsNoLoss(t *testing.T) {
	if err := CheckNumericFidelity(`{"a":1}`, make(chan int)); err != nil {
		t.Errorf("CheckNumericFidelity with an unencodable decoded value = %v, want nil", err)
	}
}

// The documented case: a value the target type actually loses precision on.
func TestCheckNumericFidelity_DetectsLoss(t *testing.T) {
	type record struct {
		Account float64 `json:"account"`
	}
	var decoded record
	body := `{"account":9007199254740993}`
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	err := CheckNumericFidelity(body, decoded)
	if err == nil {
		t.Fatal("CheckNumericFidelity did not detect the float64 precision loss")
	}
	if types.KindOf(err) != types.KindSchemaViolation {
		t.Errorf("KindOf = %v, want KindSchemaViolation", types.KindOf(err))
	}
}

// The matching case: nothing was lost.
func TestCheckNumericFidelity_AcceptsWhatFits(t *testing.T) {
	type record struct {
		Count int `json:"count"`
	}
	var decoded record
	body := `{"count":3}`
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if err := CheckNumericFidelity(body, decoded); err != nil {
		t.Errorf("CheckNumericFidelity on a lossless value = %v, want nil", err)
	}
}

// decodeExactNumbers, called directly: valid JSON decodes with ok=true;
// invalid JSON reports ok=false rather than a panic or a zero value mistaken
// for a real document.
func TestDecodeExactNumbers(t *testing.T) {
	if _, ok := decodeExactNumbers(`{"a":1}`); !ok {
		t.Error("decodeExactNumbers(valid JSON) reported ok=false")
	}
	if _, ok := decodeExactNumbers(`{not json`); ok {
		t.Error("decodeExactNumbers(invalid JSON) reported ok=true")
	}
}

// findNumericLoss's type-mismatch guards: when the two sides disagree in
// shape (map vs. non-map, slice vs. non-slice, number vs. non-number), it
// must report "no loss" rather than panicking on a type assertion -- the
// value stopped being comparable, which S-008 (unrecognised property) is
// responsible for, not this check.
func TestFindNumericLoss_TypeMismatchesDoNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("findNumericLoss panicked on a type mismatch: %v", r)
		}
	}()

	cases := []struct {
		name         string
		original     any
		roundTripped any
	}{
		{"map vs scalar", map[string]any{"a": json.Number("1")}, "not a map"},
		{"slice vs scalar", []any{json.Number("1")}, "not a slice"},
		{"number vs string", json.Number("1"), "1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, lost := findNumericLoss(tc.original, tc.roundTripped, nil)
			if lost {
				t.Errorf("findNumericLoss reported a loss on a type mismatch it cannot compare")
			}
		})
	}
}

// findNumericLoss stops at the first field the target type does not have,
// rather than reporting a loss for a key present only on one side.
func TestFindNumericLoss_MissingTargetKeyIsNotALoss(t *testing.T) {
	original := map[string]any{"a": json.Number("1"), "extra": json.Number("2")}
	roundTripped := map[string]any{"a": json.Number("1")}

	_, _, _, lost := findNumericLoss(original, roundTripped, nil)
	if lost {
		t.Error("a key the target dropped entirely was reported as a numeric loss")
	}
}

// findNumericLoss walks into nested slices and reports the first element
// that actually changed.
func TestFindNumericLoss_NestedSlice(t *testing.T) {
	original := []any{json.Number("1"), json.Number("9007199254740993")}
	roundTripped := []any{json.Number("1"), json.Number("9007199254740992")}

	path, before, after, lost := findNumericLoss(original, roundTripped, nil)
	if !lost {
		t.Fatal("findNumericLoss did not detect the change inside the slice")
	}
	if strings.Join(path, "/") != "1" {
		t.Errorf("path = %v, want index 1", path)
	}
	if before != "9007199254740993" || after != "9007199254740992" {
		t.Errorf("before/after = %q/%q", before, after)
	}
}

// sameExactValue's own guard: a literal neither side can parse as a rational
// is not evidence against the target type, so it must not be reported as a
// loss.
func TestSameExactValue_UnparsableLiteralIsNotALoss(t *testing.T) {
	if !sameExactValue("not-a-number", "also-not-a-number") {
		t.Error("sameExactValue treated two unparsable literals as different")
	}
	if !sameExactValue("1", "not-a-number") {
		t.Error("sameExactValue treated a parsable/unparsable pair as different")
	}
}

// The rational-equality case: different spellings of the same number compare
// equal, and different numbers do not.
func TestSameExactValue_ComparesAsRationals(t *testing.T) {
	if !sameExactValue("1284.50", "1284.5") {
		t.Error("sameExactValue(1284.50, 1284.5) = false, want true (same rational)")
	}
	if sameExactValue("90071992547409910", "90071992547409920") {
		t.Error("sameExactValue reported two different integers as equal")
	}
}

// findNumericLoss's slice-length guard: a round-tripped slice shorter than
// the original must stop at its end rather than indexing out of bounds --
// exercised directly since json.Marshal never actually drops slice elements,
// so this shape cannot arise through CheckNumericFidelity's normal round
// trip.
func TestFindNumericLoss_RoundTrippedSliceShorterThanOriginal(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("findNumericLoss panicked on a shorter round-tripped slice: %v", r)
		}
	}()

	original := []any{json.Number("1"), json.Number("2"), json.Number("3")}
	roundTripped := []any{json.Number("1")}

	_, _, _, lost := findNumericLoss(original, roundTripped, nil)
	if lost {
		t.Error("a slice truncated by the round trip was reported as a numeric loss, not a shape difference")
	}
}

// NormalizeOptions.Validate delegates to CommonOptions.Validate, so an
// out-of-range Threshold has to be refused through NormalizeOptions too, not
// just at the CommonOptions level directly.
func TestNormalizeOptionsValidate_RejectsOutOfRangeThreshold(t *testing.T) {
	opts := NewNormalizeOptions()
	opts.CommonOptions.Threshold = 1.5
	if err := opts.Validate(); err == nil {
		t.Fatal("Validate accepted a threshold outside [0,1]")
	}
}

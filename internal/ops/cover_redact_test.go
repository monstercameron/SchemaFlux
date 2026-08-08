package ops

import (
	"strings"
	"testing"
)

// This file raises coverage on redact.go's under-covered paths: the
// reflect-kind branches redactValue dispatches on, the strategy/report
// plumbing RedactWithResult depends on, and the jumbleTypeAware branches
// (email/phone/name detection) that had zero coverage for jumblePhone and
// jumbleName specifically. Each case asserts the contract redact.go's own
// doc comments describe -- the secret is gone, the shape is preserved, or
// the operation refuses -- not merely that a struct field was set.

// redactValue's default case (the reflect.Kind switch's fallthrough) has to
// return non-string, non-struct, non-slice/array, non-map, non-pointer
// values unchanged: there is nothing in an int to redact by pattern.
func TestRedactValue_NonRedactableKindPassesThrough(t *testing.T) {
	result, err := Redact(42, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact(int) = %v", err)
	}
	if result != 42 {
		t.Errorf("Redact(int) = %d, want unchanged 42", result)
	}
}

// The invalid-input branch in redactValue: reflect.ValueOf(nil) is not
// valid, and Redact must refuse rather than return a zero value dressed up
// as success (AGENTS.md: "never fail open").
func TestRedactValue_NilInterfaceRefuses(t *testing.T) {
	var input any
	_, err := Redact(input, NewRedactOptions())
	if err == nil {
		t.Fatal("Redact(nil) succeeded; a nil interface carries nothing to redact and should be refused")
	}
}

// A nil pointer redacts to nil without touching whatever it might have
// pointed at.
func TestRedactValue_NilPointer(t *testing.T) {
	var input *string
	result, err := Redact(input, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact(nil *string) = %v", err)
	}
	if result != nil {
		t.Errorf("Redact(nil *string) = %v, want nil", result)
	}
}

// A non-nil pointer redacts the pointee and returns a new pointer -- the
// original value it pointed at must be untouched (Redact never mutates its
// input, per every other Redact test in this package).
func TestRedactValue_NonNilPointer(t *testing.T) {
	email := "john@example.com"
	original := &email

	result, err := Redact(original, NewRedactOptions().WithCategories([]string{"PII"}))
	if err != nil {
		t.Fatalf("Redact(*string) = %v", err)
	}
	if result == original {
		t.Fatal("Redact(*string) returned the same pointer; the input must not be mutated in place")
	}
	if strings.Contains(*result, "john@example.com") {
		t.Errorf("the redacted pointee still contains the address: %q", *result)
	}
	if email != "john@example.com" {
		t.Errorf("the original value was mutated: %q", email)
	}
}

// RedactWithResult on invalid input must also refuse, not report an empty,
// misleadingly-successful audit.
func TestRedactWithResult_NilInterfaceRefuses(t *testing.T) {
	var input any
	_, _, err := RedactWithResult(input, NewRedactOptions())
	if err == nil {
		t.Fatal("RedactWithResult(nil) succeeded")
	}
}

// RedactWithResult surfaces a Validate failure the same way Redact does.
func TestRedactWithResult_InvalidOptionsRefuses(t *testing.T) {
	_, _, err := RedactWithResult("x", RedactOptions{})
	if err == nil {
		t.Fatal("RedactWithResult with no categories succeeded")
	}
}

// An invalid custom regex is skipped rather than aborting the whole pass --
// exercises the `continue` branch in redactStringWithReport's custom-pattern
// loop.
func TestRedact_InvalidCustomPatternIsSkippedNotFatal(t *testing.T) {
	opts := NewRedactOptions().
		WithCategories([]string{"PII"}).
		WithCustomPatterns([]string{"(unterminated["})

	result, err := Redact("Contact john@example.com", opts)
	if err != nil {
		t.Fatalf("an invalid custom pattern should be skipped, not fail the operation: %v", err)
	}
	if strings.Contains(result, "john@example.com") {
		t.Errorf("the PII category should still have redacted the address: %q", result)
	}
}

// A valid custom pattern still redacts and is reported under "custom".
func TestRedact_ValidCustomPatternRedactsAndReports(t *testing.T) {
	opts := NewRedactOptions().
		WithCategories([]string{"PII"}).
		WithCustomPatterns([]string{`ORDER-\d+`})

	_, result, err := RedactWithResult("Order ORDER-9981 shipped", opts)
	if err != nil {
		t.Fatalf("RedactWithResult: %v", err)
	}
	if len(result.Redacted["custom"]) != 1 || result.Redacted["custom"][0] != "ORDER-9981" {
		t.Errorf("Redacted[custom] = %v, want the order number reported once", result.Redacted["custom"])
	}
}

// applyRedactionStrategy's default branch (an unrecognised strategy) falls
// back to masking rather than leaving the value untouched -- called directly
// because Validate() rejects any strategy string this branch would otherwise
// need to reach through the public API.
func TestApplyRedactionStrategy_UnknownStrategyFallsBackToMask(t *testing.T) {
	opts := NewRedactOptions()
	opts.Strategy = RedactStrategy("not-a-real-strategy")

	got := applyRedactionStrategy("secret-value", opts)
	if got != "***" {
		t.Errorf("applyRedactionStrategy with an unknown strategy = %q, want the default mask", got)
	}
}

// applyRedactionToValue's numeric branches: RedactNil zeroes an int/float
// field redacted by name; any other strategy leaves the number as-is,
// because there is no masked-number representation to produce.
func TestApplyRedactionToValue_NumericFields(t *testing.T) {
	type record struct {
		Score int     `redact:"x"`
		Ratio float64 `redact:"x"`
		Label string  `redact:"x"`
	}

	t.Run("RedactNil zeroes numeric fields", func(t *testing.T) {
		input := record{Score: 99, Ratio: 4.5, Label: "keep"}
		result, err := Redact(input, NewRedactOptions().WithStrategy(RedactNil))
		if err != nil {
			t.Fatalf("Redact: %v", err)
		}
		if result.Score != 0 || result.Ratio != 0 {
			t.Errorf("RedactNil did not zero numeric fields: %+v", result)
		}
	})

	t.Run("RedactMask leaves numeric fields untouched", func(t *testing.T) {
		input := record{Score: 99, Ratio: 4.5, Label: "keep"}
		result, err := Redact(input, NewRedactOptions().WithStrategy(RedactMask))
		if err != nil {
			t.Fatalf("Redact: %v", err)
		}
		if result.Score != 99 || result.Ratio != 4.5 {
			t.Errorf("RedactMask altered a numeric field it cannot mask: %+v", result)
		}
		if result.Label != "***" {
			t.Errorf("Label should still be masked by its redact tag: %q", result.Label)
		}
	})
}

// applyRedactionToValue's interface-unwrap branch: a map[string]any holds
// its values behind an interface, and an int value behind a sensitive key
// must still be reachable for RedactNil to zero it -- not silently kept
// because unwrapping only happened for strings.
func TestApplyRedactionToValue_InterfaceWrappedNumber(t *testing.T) {
	data := map[string]any{"password": 123456, "note": "keep me"}

	result, err := Redact(data, NewRedactOptions().WithStrategy(RedactNil))
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if result["password"] != 0 {
		t.Errorf(`result["password"] = %v, want 0 (RedactNil on an interface-wrapped int)`, result["password"])
	}
	if result["note"] != "keep me" {
		t.Errorf(`result["note"] = %v, want unchanged`, result["note"])
	}
}

// jumbleTypeAware's phone branch (jumblePhone had zero coverage): the digits
// are permuted but every non-digit character stays exactly where it was, so
// the output is still recognisable as A phone number, just not THE phone
// number.
func TestJumbleTypeAware_Phone(t *testing.T) {
	opts := NewRedactOptions().
		WithCategories([]string{"PII"}).
		WithStrategy(RedactJumble).
		WithJumbleMode(JumbleTypeAware).
		WithJumbleSeed(7)

	const input = "555-123-4567"
	result, err := Redact(input, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if len(result) != len(input) {
		t.Fatalf("jumbled phone changed length: %q -> %q", input, result)
	}
	for i, c := range input {
		if c == '-' && rune(result[i]) != '-' {
			t.Fatalf("hyphen at position %d was not preserved: %q -> %q", i, input, result)
		}
	}
	if result == input {
		t.Errorf("jumbled phone equals the original: %q", result)
	}
	if strings.Contains(result, "555") && strings.Contains(result, "123") && strings.Contains(result, "4567") {
		t.Errorf("digits were not scrambled at all: %q", result)
	}
}

// jumbleTypeAware's name branch (jumbleName had zero coverage). The
// two-capitalised-words pattern was removed from the category regexes
// entirely (redact_detect.go: it used to redact "New York" and "Total
// Revenue" as names), so the only way to reach it through the public API is
// a field redacted by NAME whose value happens to have that shape --
// applyRedactionToValue -> applyRedactionStrategy -> jumbleString ->
// jumbleTypeAware, which still sniffs the value's shape once it has already
// been chosen for redaction. A Fisher-Yates shuffle can land on the identity
// permutation for a particular seed, so this tries a small spread of seeds
// and requires at least one to actually change the value.
func TestJumbleTypeAware_Name(t *testing.T) {
	type person struct {
		Name string `redact:"PII"`
	}

	changed := false
	for seed := int64(1); seed <= 20; seed++ {
		opts := NewRedactOptions().
			WithStrategy(RedactJumble).
			WithJumbleMode(JumbleTypeAware).
			WithJumbleSeed(seed)

		result, err := Redact(person{Name: "John Smith"}, opts)
		if err != nil {
			t.Fatalf("Redact: %v", err)
		}
		parts := strings.Fields(result.Name)
		if len(parts) != 2 {
			t.Fatalf("jumbled name lost its word structure: %q -> %q", "John Smith", result.Name)
		}
		if result.Name != "John Smith" {
			changed = true
		}
	}
	if !changed {
		t.Errorf("jumbleName never changed %q across 20 seeds", "John Smith")
	}
}

// The default arm of jumbleTypeAware: input matching none of the
// email/phone/name shapes falls back to jumbleBasic, which still permutes
// (same multiset of runes) rather than leaving it untouched.
func TestJumbleTypeAware_FallsBackToBasicForUnrecognisedShape(t *testing.T) {
	opts := NewRedactOptions().
		WithCategories([]string{"secrets"}).
		WithCustomPatterns([]string{`sk-\w+`}).
		WithStrategy(RedactJumble).
		WithJumbleMode(JumbleTypeAware).
		WithJumbleSeed(3)

	const input = "sk-abcdefgh"
	result, err := Redact(input, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if len(result) != len(input) {
		t.Fatalf("basic jumble changed length: %q -> %q", input, result)
	}
	if result == input {
		t.Errorf("basic jumble left the value unchanged: %q", result)
	}
}

// A zero JumbleSeed draws from the unpredictable default rather than a fixed
// seed -- two redactions of the same input must not agree, which is exactly
// what OP-505's fix (crypto/rand instead of len(input)) guarantees and a
// fixed seed would not prove.
func TestJumble_DefaultSeedIsNotReproducible(t *testing.T) {
	opts := NewRedactOptions().
		WithCategories([]string{"PII"}).
		WithStrategy(RedactJumble)
	// JumbleSeed left at zero deliberately.

	// Has to actually match a PII pattern, or Redact leaves it untouched and
	// the two calls would trivially agree for a reason that has nothing to do
	// with the seed.
	const input = "reproducible-looking-value@example.com"

	first, err := Redact(input, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	second, err := Redact(input, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if first == second {
		t.Errorf("two default-seed jumbles of the same input agreed (%q); the default must not be reproducible", first)
	}
}

// jumbleSmart substitutes rather than permutes: length and non-letter/digit
// characters survive, but the exact character multiset does not have to
// (unlike jumbleBasic), and case is preserved per-character.
func TestJumbleSmart_PreservesShapeAndCase(t *testing.T) {
	opts := NewRedactOptions().
		WithCategories([]string{"PII"}).
		WithStrategy(RedactJumble).
		WithJumbleMode(JumbleSmart).
		WithJumbleSeed(99)

	const input = "John Q. Public-42!"
	result, err := Redact(input, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if len(result) != len(input) {
		t.Fatalf("jumbleSmart changed length: %q -> %q", input, result)
	}
	for i, c := range input {
		switch {
		case c == ' ' || c == '.' || c == '-' || c == '!':
			if rune(result[i]) != c {
				t.Errorf("punctuation/space at %d not preserved: %q -> %q", i, input, result)
			}
		case c >= 'A' && c <= 'Z':
			if !(result[i] >= 'A' && result[i] <= 'Z') {
				t.Errorf("case not preserved at %d: %q -> %q", i, input, result)
			}
		}
	}
}

// applyRedactionToValue's default reflect-kind branch: a bool field redacted
// by name. RedactNil zeroes it; anything else leaves it as-is, because there
// is no masked-boolean representation.
func TestApplyRedactionToValue_DefaultKindBool(t *testing.T) {
	type record struct {
		Active bool `redact:"x"`
	}

	nilResult, err := Redact(record{Active: true}, NewRedactOptions().WithStrategy(RedactNil))
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if nilResult.Active != false {
		t.Errorf("RedactNil left a bool field true")
	}

	maskResult, err := Redact(record{Active: true}, NewRedactOptions().WithStrategy(RedactMask))
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if maskResult.Active != true {
		t.Errorf("RedactMask altered a bool field it cannot mask")
	}
}

// jumbleEmail's shape guard: an address with more than one "@" does not
// split into exactly two parts, and must fall back to jumbleBasic rather
// than indexing past the slice.
func TestJumbleTypeAware_MalformedEmailFallsBackToBasic(t *testing.T) {
	type record struct {
		Contact string `redact:"x"`
	}
	opts := NewRedactOptions().
		WithStrategy(RedactJumble).
		WithJumbleMode(JumbleTypeAware).
		WithJumbleSeed(5)

	const input = "a@b@example.com"
	result, err := Redact(record{Contact: input}, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if len(result.Contact) != len(input) {
		t.Fatalf("length changed: %q -> %q", input, result.Contact)
	}
}

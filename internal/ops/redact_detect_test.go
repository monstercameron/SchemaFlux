package ops

import (
	"strings"
	"testing"
)

// T-07: the sensitive-name list was matched as substrings, so any field whose
// name merely contained "name", "key", "first", "last", "card", "address", or
// "full" was silently destroyed — and RedactWithResult would not tell you
// which. These are the ordinary fields that were collateral damage.
func TestOrdinaryFieldNamesAreNotRedacted(t *testing.T) {
	safe := []string{
		"Filename", "FileName", "Username", "UserName", "Nickname",
		"Keywords", "KeyMetrics", "APIKeyLabel", "KeyboardLayout",
		"FirstSeen", "FirstPage", "LastUpdated", "LastLogin",
		"CardCount", "Cardinality", "AddressBookSize", "AddressableMarket",
		"FullText", "FullWidth", "TokenCount", "SecretSantaYear",
		"EmailsSent", "PhoneModels", "NameCount",
	}

	for _, name := range safe {
		t.Run(name, func(t *testing.T) {
			if fieldNameIsSensitive(name) {
				t.Errorf("%s is redacted; it normalises to %q", name, normaliseFieldName(name))
			}
		})
	}
}

// And the fields that genuinely name sensitive data still are.
func TestSensitiveFieldNamesAreRedacted(t *testing.T) {
	sensitive := []string{
		"SSN", "ssn", "social_security_number",
		"Password", "password", "Passwd", "PWD",
		"APIKey", "ApiKey", "api_key", "AccessToken", "RefreshToken",
		"CreditCard", "CardNumber", "CVV",
		"Email", "EmailAddress", "Phone", "PhoneNumber",
		"FirstName", "LastName", "FullName", "Name",
		"DateOfBirth", "DOB", "StreetAddress", "PassportNumber",
		"RoutingNumber", "AccountNumber", "IBAN",
	}

	for _, name := range sensitive {
		t.Run(name, func(t *testing.T) {
			if !fieldNameIsSensitive(name) {
				t.Errorf("%s is not redacted; it normalises to %q", name, normaliseFieldName(name))
			}
		})
	}
}

// The normalisation is the mechanism, so it is checked directly.
func TestNormaliseFieldName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"FirstName", "first name"},
		{"first_name", "first name"},
		{"first-name", "first name"},
		{"APIKey", "api key"},
		{"APIKeyLabel", "api key label"},
		{"Filename", "filename"},
		{"FileName", "file name"},
		{"SSN", "ssn"},
		{"DOB", "dob"},
		{"IBAN", "iban"},
		{"HTTPSProxy", "https proxy"},
		{"", ""},
		{"a", "a"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := normaliseFieldName(tc.input); got != tc.want {
				t.Errorf("normaliseFieldName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// T-08: the patterns missed the formats they named. An unformatted PAN was
// caught by neither the PII nor the financial pattern.
func TestCardNumbersAreDetectedByLuhnNotByShape(t *testing.T) {
	cards := []struct {
		name   string
		text   string
		isCard bool
	}{
		{"unformatted_visa", "4111111111111111", true},
		{"spaced_visa", "4111 1111 1111 1111", true},
		{"dashed_visa", "4111-1111-1111-1111", true},
		{"mastercard", "5500005555555559", true},
		{"amex_15_digits", "378282246310005", true},
		{"in_a_sentence", "charge 4111111111111111 please", true},

		{"order_number_16_digits", "1234567890123456", false},
		{"sequence_number", "1111111111111111", false},
		{"nine_digits", "123456789", false},
		{"phone_digits", "3055551234", false},
		{"too_short", "411111111111", false},
		{"words", "no numbers here", false},
	}

	for _, tc := range cards {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsCardNumber(tc.text); got != tc.isCard {
				t.Errorf("containsCardNumber(%q) = %v, want %v", tc.text, got, tc.isCard)
			}
		})
	}
}

// The phone formats the old `###-###-####` pattern missed.
func TestPhoneFormatsAreDetected(t *testing.T) {
	matched := []string{
		"305-555-1234",
		"(305) 555-1234",
		"305.555.1234",
		"305 555 1234",
		"+1 305 555 1234",
		"+1-305-555-1234",
		"call 305-555-1234 today",
	}

	for _, text := range matched {
		t.Run(text, func(t *testing.T) {
			if !phonePattern.MatchString(text) {
				t.Errorf("%q was not detected as a phone number", text)
			}
		})
	}
}

// The false positives that made the output useless are gone.
func TestOrdinaryTextIsNotRedactedAsPII(t *testing.T) {
	ordinary := []string{
		"New York",
		"Total Revenue",
		"Monday Morning",
		"John Smith visited", // a name IS missed now; that is documented
		"order 123456789",    // nine digits: was a routing number
		"$1,284.50 due",      // currency: was masked wholesale
		"invoice 1234567890123456",
	}

	for _, text := range ordinary {
		t.Run(text, func(t *testing.T) {
			if valueIsSensitive(text, []string{"PII", "financial"}, nil) {
				t.Errorf("%q was redacted as sensitive", text)
			}
		})
	}
}

// The data the categories actually name is detected.
func TestAdvertisedCategoriesDetectTheirData(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		category string
	}{
		{"ssn_dashed", "SSN 123-45-6789", "PII"},
		{"ssn_spaced", "SSN 123 45 6789", "PII"},
		{"email", "write to ada@example.com", "PII"},
		{"phone", "call (305) 555-1234", "PII"},
		{"card_in_pii", "card 4111111111111111", "PII"},
		{"card_in_financial", "card 4111 1111 1111 1111", "financial"},
		{"iban", "IBAN GB29NWBK60161331926819", "financial"},
		{"password_assignment", "password: hunter2", "secrets"},
		{"api_key_assignment", "api_key = sk-abcdef", "secrets"},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrst", "secrets"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !valueIsSensitive(tc.text, []string{tc.category}, nil) {
				t.Errorf("%q was not detected in category %s", tc.text, tc.category)
			}
		})
	}
}

// Luhn directly, because it is what separates a card from a number.
func TestLuhn(t *testing.T) {
	cases := []struct {
		digits string
		valid  bool
	}{
		{"4111111111111111", true},
		{"5500005555555559", true},
		{"378282246310005", true},
		{"6011111111111117", true},
		{"4111111111111112", false},
		{"1234567890123456", false},
		{"", false},
		{"123", false},
		{"12345678901234567890", false},
		{"411111111111111a", false},
	}

	for _, tc := range cases {
		t.Run(tc.digits, func(t *testing.T) {
			if got := luhn(tc.digits); got != tc.valid {
				t.Errorf("luhn(%q) = %v, want %v", tc.digits, got, tc.valid)
			}
		})
	}
}

// T-09: the function whose purpose is to say what it redacted returned an empty
// map with a nil error, so an audit read "nothing was redacted" as success.
func TestRedactWithResultReportsWhatItDid(t *testing.T) {
	type account struct {
		AccountID string
		Email     string
		Notes     string
		SSN       string
		Filename  string
	}

	input := account{
		AccountID: "acct-991",
		Email:     "ada@example.com",
		Notes:     "call (305) 555-1234 about card 4111111111111111",
		SSN:       "123-45-6789",
		Filename:  "report.pdf",
	}

	_, result, err := RedactWithResult(input, NewRedactOptions())
	if err != nil {
		t.Fatalf("RedactWithResult: %v", err)
	}

	if result.Count == 0 {
		t.Fatal("nothing was reported after redacting an email, a phone number, a card, and an SSN")
	}

	t.Run("names_the_fields_it_redacted", func(t *testing.T) {
		joined := strings.Join(result.FieldsRedacted, ",")
		for _, want := range []string{"Email", "SSN"} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s is not in the reported fields %v", want, result.FieldsRedacted)
			}
		}
		if strings.Contains(joined, "Filename") {
			t.Errorf("Filename was reported as redacted: %v", result.FieldsRedacted)
		}
		if strings.Contains(joined, "AccountID") {
			t.Errorf("AccountID was reported as redacted: %v", result.FieldsRedacted)
		}
	})

	t.Run("names_the_values_it_matched", func(t *testing.T) {
		if len(result.Redacted["PII"]) == 0 {
			t.Errorf("the PII category is empty: %+v", result.Redacted)
		}
	})

	t.Run("metadata_is_populated", func(t *testing.T) {
		if result.Metadata["fields_redacted"] == nil || result.Metadata["values_redacted"] == nil {
			t.Errorf("metadata = %+v", result.Metadata)
		}
	})
}

// T-11: JumbleSeed defaults to zero and the RNG was then seeded with the
// input's LENGTH, a number readable off the output. The permutation was fully
// determined by a seed anyone could reproduce, making the jumble invertible.
func TestJumbleIsNotReproducibleByDefault(t *testing.T) {
	// Jumbling applies to a value that was selected for redaction, so this
	// drives it through a field named as sensitive rather than through a bare
	// string, which Redact only rewrites where a pattern matched.
	type record struct {
		SSN string
	}

	opts := NewRedactOptions()
	opts.Strategy = RedactJumble

	seen := map[string]struct{}{}
	for i := 0; i < 20; i++ {
		out, err := Redact(record{SSN: "abcdefghijklmnopqrstuvwxyz0123456789"}, opts)
		if err != nil {
			t.Fatalf("Redact: %v", err)
		}
		seen[out.SSN] = struct{}{}
	}

	// With the old length-seeded RNG every call produced the same permutation,
	// so the jumble could be undone by anyone with this library.
	if len(seen) == 1 {
		t.Fatal("twenty jumbles produced one output; the permutation is reproducible and therefore invertible")
	}
	if len(seen) < 10 {
		t.Errorf("twenty jumbles produced only %d distinct outputs", len(seen))
	}
}

// The old seeding was a function of the input's length, a number readable off
// the output. This is that specific property.
func TestJumbleSeedIsNotDerivedFromTheInput(t *testing.T) {
	type record struct {
		SSN string
	}

	opts := NewRedactOptions()
	opts.Strategy = RedactJumble

	// Two inputs of equal length would have shared a seed, and therefore a
	// permutation, under the old scheme.
	first, err := Redact(record{SSN: "aaaaaaaaaabbbbbbbbbb"}, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	second, err := Redact(record{SSN: "aaaaaaaaaabbbbbbbbbb"}, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if first.SSN == second.SSN {
		t.Error("two jumbles of the same input matched; the permutation is still deterministic")
	}
}

// An explicit seed restores determinism, which is a legitimate thing to want
// for fixtures and an illegitimate thing to rely on for privacy.
func TestExplicitJumbleSeedIsDeterministic(t *testing.T) {
	type record struct {
		SSN string
	}

	opts := NewRedactOptions()
	opts.Strategy = RedactJumble
	opts.JumbleSeed = 42

	first, err := Redact(record{SSN: "abcdefghijklmnop"}, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := Redact(record{SSN: "abcdefghijklmnop"}, opts)
		if err != nil {
			t.Fatalf("Redact: %v", err)
		}
		if again.SSN != first.SSN {
			t.Errorf("an explicit seed produced %q then %q", first.SSN, again.SSN)
		}
	}
}

// The seed source must not be a function of the input, which is what made the
// old default reconstructible.
func TestUnpredictableSeedVaries(t *testing.T) {
	seen := map[int64]struct{}{}
	for i := 0; i < 50; i++ {
		seen[unpredictableSeed()] = struct{}{}
	}
	if len(seen) < 40 {
		t.Errorf("50 draws produced only %d distinct seeds", len(seen))
	}
}

// A map key names its value exactly as a struct field does, and used to be
// ignored entirely.
func TestMapKeysAreTreatedAsFieldNames(t *testing.T) {
	input := map[string]any{
		"ssn":      "123-45-6789",
		"filename": "report.pdf",
		"api_key":  "sk-secret-value",
		"count":    "42",
	}

	redacted, result, err := RedactWithResult(input, NewRedactOptions())
	if err != nil {
		t.Fatalf("RedactWithResult: %v", err)
	}

	if redacted["ssn"] == "123-45-6789" {
		t.Error("a value under the key ssn was not redacted")
	}
	if redacted["api_key"] == "sk-secret-value" {
		t.Error("a value under the key api_key was not redacted")
	}
	if redacted["filename"] != "report.pdf" {
		t.Errorf("filename was redacted: %v", redacted["filename"])
	}
	if redacted["count"] != "42" {
		t.Errorf("count was redacted: %v", redacted["count"])
	}
	if len(result.FieldsRedacted) == 0 {
		t.Error("the redacted keys were not reported")
	}
}

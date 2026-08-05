package ops

import (
	"regexp"
	"strings"
	"unicode"
)

// Detection used to be substring matching on field names and a handful of
// regexes that missed the formats they named. The list included "name", "key",
// "first", "last", "card", "address", and "full", matched as substrings, so
// Filename, Username, Keywords, KeyMetrics, APIKeyLabel, FirstSeen,
// LastUpdated, CardCount, and AddressBookSize were silently destroyed — while
// an unformatted 16-digit card number passed straight through.
//
// A redaction utility that masks "New York" and passes 4111111111111111 is not
// imperfect, it is misleading, because the option is spelled
// Categories: []string{"PII"} and invites compliance use.

// sensitiveFieldNames is matched against the normalised field name as a whole,
// not as a substring. Every entry is a name that means the thing itself, never
// a word that merely appears in one.
var sensitiveFieldNames = map[string]struct{}{
	"ssn":                    {},
	"social security number": {},
	"social security":        {},
	"password":               {},
	"passwd":                 {},
	"pwd":                    {},
	"secret":                 {},
	"client secret":          {},
	"api key":                {},
	"apikey":                 {},
	"access token":           {},
	"refresh token":          {},
	"auth token":             {},
	"bearer token":           {},
	"token":                  {},
	"private key":            {},
	"credit card":            {},
	"card number":            {},
	"cardnumber":             {},
	"pan":                    {},
	"cvv":                    {},
	"cvc":                    {},
	"email":                  {},
	"email address":          {},
	"phone":                  {},
	"phone number":           {},
	"mobile number":          {},
	"name":                   {},
	"first name":             {},
	"last name":              {},
	"full name":              {},
	"middle name":            {},
	"maiden name":            {},
	"date of birth":          {},
	"dob":                    {},
	"birth date":             {},
	"street address":         {},
	"home address":           {},
	"postal code":            {},
	"zip code":               {},
	"passport number":        {},
	"drivers license":        {},
	"driver license":         {},
	"license number":         {},
	"bank account":           {},
	"account number":         {},
	"routing number":         {},
	"iban":                   {},
	"swift":                  {},
	"tax id":                 {},
	"national id":            {},
}

// normaliseFieldName turns a Go field name into space-separated lower-case
// words: APIKeyLabel becomes "api key label", first_name becomes "first name".
func normaliseFieldName(name string) string {
	var words []string
	var current []rune

	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = nil
		}
	}

	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			flush()
		case unicode.IsUpper(r):
			// A capital starts a new word unless it is inside a run of capitals
			// that is still going: APIKey is "api" then "key", not "a p i key".
			previousLower := i > 0 && unicode.IsLower(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if previousLower || (nextLower && len(current) > 0) {
				flush()
			}
			current = append(current, r)
		default:
			current = append(current, r)
		}
	}
	flush()

	return strings.Join(words, " ")
}

// fieldNameIsSensitive reports whether a field name names sensitive data. The
// whole normalised name must match: "file name" is not "name".
func fieldNameIsSensitive(name string) bool {
	normalised := normaliseFieldName(name)
	if normalised == "" {
		return false
	}
	_, sensitive := sensitiveFieldNames[normalised]
	return sensitive
}

// The patterns below replace the previous set. Removed outright:
//
//   - `\b[A-Z][a-z]+ [A-Z][a-z]+\b` matched any two capitalised words, so
//     "New York", "Total Revenue", and "Monday Morning" were redacted as names.
//   - `\b\d{9}\b` matched any nine-digit number, so order IDs and sequence
//     numbers were redacted as routing numbers.
//   - `\$\d+(?:\.\d{2})?` masked every currency amount in the text.
//
// Each of those destroyed ordinary data at a rate that made the output useless,
// which is its own kind of failure.
var (
	// SSN in the form it is actually written. A bare nine-digit run is
	// deliberately NOT matched: it is indistinguishable from an order ID, and
	// matching it was what made the old financial pattern unusable.
	ssnPattern = regexp.MustCompile(`\b\d{3}[- ]\d{2}[- ]\d{4}\b`)

	// Phone numbers as they are written in practice, including the formats the
	// old `###-###-####` pattern missed: (305) 555-1234, 305.555.1234,
	// +1 305 555 1234.
	phonePattern = regexp.MustCompile(`(?:\+\d{1,3}[ .-]?)?(?:\(\d{3}\)[ .-]?|\b\d{3}[ .-])\d{3}[ .-]\d{4}\b`)

	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)

	// A candidate card number: 13 to 19 digits, optionally grouped. Whether it
	// is really a card is decided by Luhn, not by the shape — which is how an
	// unformatted PAN gets caught and an order number does not.
	cardCandidatePattern = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)

	ibanPattern = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)

	secretAssignmentPattern = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[_ -]?key|access[_ -]?token|token)\b\s*[:=]\s*\S+`)

	bearerPattern = regexp.MustCompile(`\bBearer\s+[A-Za-z0-9+/=._\-]{16,}\b`)
)

// luhn reports whether a digit string passes the Luhn check, which every
// payment card number does and an arbitrary run of digits almost never does.
func luhn(digits string) bool {
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}

	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		r := digits[i]
		if r < '0' || r > '9' {
			return false
		}
		value := int(r - '0')
		if double {
			value *= 2
			if value > 9 {
				value -= 9
			}
		}
		sum += value
		double = !double
	}
	return sum%10 == 0
}

// digitsOnly strips the grouping characters a card number is written with.
func digitsOnly(text string) string {
	var builder strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// containsCardNumber reports whether the text contains something that passes
// Luhn at a card-number length.
func containsCardNumber(text string) bool {
	for _, candidate := range cardCandidatePattern.FindAllString(text, -1) {
		if luhn(digitsOnly(candidate)) {
			return true
		}
	}
	return false
}

// categoryPatterns returns the detectors for a category. Card detection is not
// a regex, so it is handled separately by containsCardNumber.
func categoryPatterns(category string) []*regexp.Regexp {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "pii":
		return []*regexp.Regexp{ssnPattern, phonePattern, emailPattern}
	case "secrets":
		return []*regexp.Regexp{secretAssignmentPattern, bearerPattern}
	case "financial":
		return []*regexp.Regexp{ibanPattern}
	default:
		return nil
	}
}

// categoryUsesCardDetection reports whether a category should run the Luhn
// check as well as its regexes.
func categoryUsesCardDetection(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "pii", "financial":
		return true
	default:
		return false
	}
}

// valueIsSensitive reports whether a string value contains data in any of the
// named categories.
func valueIsSensitive(value string, categories []string, custom []string) bool {
	for _, category := range categories {
		for _, pattern := range categoryPatterns(category) {
			if pattern.MatchString(value) {
				return true
			}
		}
		if categoryUsesCardDetection(category) && containsCardNumber(value) {
			return true
		}
	}

	for _, raw := range custom {
		pattern, err := regexp.Compile(raw)
		if err != nil {
			continue
		}
		if pattern.MatchString(value) {
			return true
		}
	}

	return false
}

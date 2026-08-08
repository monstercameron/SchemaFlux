package ops

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Deterministic validation rules.
//
// Every rule in the README's own Validate example -- "email must be valid,
// country must be ISO alpha-2, age at least 18" -- is checkable in Go, exactly,
// for free. Sending them to a model instead costs a round trip, costs money,
// and returns a judgement rather than an answer: the model is right about a
// malformed email almost always, and "almost always" is a strange thing to
// accept when `mail.ParseAddress` is right every time.
//
// So the rules a machine can decide are decided by the machine, and only what
// is left goes to the model. Parse already demonstrates the same
// deterministic-first pattern in this package.

// deterministicRule checks one value and reports what is wrong with it.
type deterministicRule struct {
	// Describe is what the rule would have told the model, used when the rule
	// is passed through instead of being applied.
	Describe string

	// Check reports an empty string when the value is acceptable.
	Check func(value any) string
}

// resolveDeterministicRule turns a rule expression into a check, or reports
// that the expression is not one this layer understands.
//
// Unrecognised expressions are not an error: they are natural language, which
// is what the operation is for. They fall through to the model.
func resolveDeterministicRule(expression string) (deterministicRule, bool) {
	raw := strings.TrimSpace(expression)
	lower := strings.ToLower(raw)

	name, argument, _ := strings.Cut(lower, ":")
	name = strings.TrimSpace(name)
	argument = strings.TrimSpace(argument)

	switch name {
	case "email":
		return deterministicRule{"must be a valid email address", func(value any) string {
			text, ok := value.(string)
			if !ok {
				return "must be a string containing an email address"
			}
			if _, err := mail.ParseAddress(text); err != nil {
				return "is not a valid email address"
			}
			return ""
		}}, true

	case "url":
		return deterministicRule{"must be a valid absolute URL", func(value any) string {
			text, ok := value.(string)
			if !ok {
				return "must be a string containing a URL"
			}
			parsed, err := url.Parse(text)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return "is not a valid absolute URL"
			}
			return ""
		}}, true

	case "uuid":
		return deterministicRule{"must be a UUID", func(value any) string {
			text, ok := value.(string)
			if !ok || !uuidPattern.MatchString(text) {
				return "is not a UUID"
			}
			return ""
		}}, true

	case "iso3166", "iso3166-alpha2", "country":
		return deterministicRule{"must be an ISO 3166-1 alpha-2 country code", func(value any) string {
			text, ok := value.(string)
			if !ok {
				return "must be a two-letter country code"
			}
			if !isoAlpha2Pattern.MatchString(text) {
				return "is not an ISO 3166-1 alpha-2 country code (two uppercase letters)"
			}
			return ""
		}}, true

	case "nonempty", "required":
		return deterministicRule{"must not be empty", func(value any) string {
			switch typed := value.(type) {
			case nil:
				return "is missing"
			case string:
				if strings.TrimSpace(typed) == "" {
					return "is empty"
				}
			case []any:
				if len(typed) == 0 {
					return "is empty"
				}
			}
			return ""
		}}, true

	case "min", "gte":
		return numericRule(raw, argument, func(value, bound float64) bool { return value >= bound },
			"must be at least %s", "is below the minimum of %s")

	case "max", "lte":
		return numericRule(raw, argument, func(value, bound float64) bool { return value <= bound },
			"must be at most %s", "is above the maximum of %s")

	case "minlen":
		return lengthRule(raw, argument, func(length, bound int) bool { return length >= bound },
			"must be at least %s characters", "is shorter than %s characters")

	case "maxlen":
		return lengthRule(raw, argument, func(length, bound int) bool { return length <= bound },
			"must be at most %s characters", "is longer than %s characters")

	case "oneof":
		allowed := strings.Split(argument, "|")
		if len(allowed) == 0 || argument == "" {
			return deterministicRule{}, false
		}
		return deterministicRule{
			fmt.Sprintf("must be one of %s", strings.Join(allowed, ", ")),
			func(value any) string {
				text := fmt.Sprintf("%v", value)
				for _, candidate := range allowed {
					if strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(candidate)) {
						return ""
					}
				}
				return fmt.Sprintf("is not one of %s", strings.Join(allowed, ", "))
			}}, true

	case "regex", "matches":
		// The pattern is taken from the original expression, not the lowered
		// one: a regex is case-sensitive and lowering it changes what it means.
		_, pattern, _ := strings.Cut(raw, ":")
		compiled, err := regexp.Compile(strings.TrimSpace(pattern))
		if err != nil {
			// A pattern that does not compile is the caller's mistake, but it
			// is not this layer's place to fail the whole validation over it.
			// Pass it to the model, which will at least say something useful.
			return deterministicRule{}, false
		}
		return deterministicRule{
			fmt.Sprintf("must match %s", pattern),
			func(value any) string {
				text, ok := value.(string)
				if !ok || !compiled.MatchString(text) {
					return fmt.Sprintf("does not match %s", pattern)
				}
				return ""
			}}, true
	}

	return deterministicRule{}, false
}

var (
	uuidPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	isoAlpha2Pattern = regexp.MustCompile(`^[A-Z]{2}$`)
)

func numericRule(raw, argument string, accept func(value, bound float64) bool, describe, complain string) (deterministicRule, bool) {
	bound, err := strconv.ParseFloat(argument, 64)
	if err != nil {
		return deterministicRule{}, false
	}
	return deterministicRule{
		fmt.Sprintf(describe, argument),
		func(value any) string {
			number, ok := toFloat(value)
			if !ok {
				return "must be a number"
			}
			if !accept(number, bound) {
				return fmt.Sprintf(complain, argument)
			}
			return ""
		}}, true
}

func lengthRule(raw, argument string, accept func(length, bound int) bool, describe, complain string) (deterministicRule, bool) {
	bound, err := strconv.Atoi(argument)
	if err != nil {
		return deterministicRule{}, false
	}
	return deterministicRule{
		fmt.Sprintf(describe, argument),
		func(value any) string {
			text, ok := value.(string)
			if !ok {
				return "must be a string"
			}
			// Runes, because a length limit is about characters.
			if !accept(len([]rune(text)), bound) {
				return fmt.Sprintf(complain, argument)
			}
			return ""
		}}, true
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number, err == nil
	}
	return 0, false
}

// applyDeterministicRules runs whichever field rules this layer can decide.
//
// It returns the issues it found and the rules it did not handle, so the model
// is asked about exactly what is left -- and if nothing is left, the caller
// gets an exact answer without a provider call at all.
func applyDeterministicRules(data any, fieldRules map[string]string) (issues []ValidationIssue, unhandled map[string]string) {
	unhandled = map[string]string{}
	if len(fieldRules) == 0 {
		return nil, unhandled
	}

	fields := fieldValuesOf(data)

	// Sorted so the issue order is stable across runs; Go randomizes map
	// iteration and an unstable issue list is one a caller cannot diff.
	for _, field := range sortedKeys(fieldRules) {
		expression := fieldRules[field]
		rule, ok := resolveDeterministicRule(expression)
		if !ok {
			unhandled[field] = expression
			continue
		}

		value, present := fields[strings.ToLower(field)]
		if !present {
			issues = append(issues, ValidationIssue{
				Field:    field,
				Severity: "error",
				Message:  fmt.Sprintf("%s is not present in the data", field),
			})
			continue
		}

		if complaint := rule.Check(value); complaint != "" {
			issues = append(issues, ValidationIssue{
				Field:      field,
				Severity:   "error",
				Message:    fmt.Sprintf("%s %s", field, complaint),
				Suggestion: rule.Describe,
				// No value in the message. The field name says what is wrong;
				// the value is the caller's data, and this message is logged
				// wherever their errors go (X-03).
			})
		}
	}

	return issues, unhandled
}

// fieldValuesOf flattens a value into its top-level JSON fields, keyed by lower
// case name so a rule written as "Email" finds `json:"email"`.
func fieldValuesOf(data any) map[string]any {
	values := map[string]any{}

	encoded, err := json.Marshal(data)
	if err != nil {
		return values
	}

	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber() // exact numbers: a 19-digit ID must not become a float

	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return values
	}

	for key, value := range object {
		values[strings.ToLower(key)] = value
	}
	return values
}

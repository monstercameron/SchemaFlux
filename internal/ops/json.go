package ops

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseJSON parses a JSON string into a target struct, handling common LLM output issues
// like markdown code blocks and whitespace.
func ParseJSON(input string, target any) error {
	// Clean up the input
	cleaned := cleanJSON(input)

	// Try to unmarshal
	err := json.Unmarshal([]byte(cleaned), target)
	if err != nil {
		// Deliberately no payload in the error. This error is logged by every
		// caller, and the payload is the caller's data -- often the very record
		// being extracted or redacted -- so embedding it copies user content
		// into every log aggregator the operator runs. Report the shape instead:
		// enough to diagnose, nothing to leak.
		return fmt.Errorf("failed to unmarshal JSON: %w (response was %d bytes, shape: %s)",
			err, len(cleaned), describeShape(cleaned))
	}

	return nil
}

// describeShape classifies a response without reproducing any of it. A prefix
// was the obvious choice and the wrong one: the first 24 characters of a JSON
// body are usually the first field and its value, so a payload beginning
// {"ssn": "123-45-6789" leaks the value it was meant to summarise. The
// classification below distinguishes the cases an operator actually needs to
// tell apart -- an error page, prose, a truncated object -- and carries no
// caller data at all.
func describeShape(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "empty"
	case strings.HasPrefix(s, "```"):
		return "fenced block"
	case strings.HasPrefix(s, "{"):
		return "json object"
	case strings.HasPrefix(s, "["):
		return "json array"
	case strings.HasPrefix(s, "<"):
		return "markup, likely an error page"
	default:
		return "prose"
	}
}

// cleanJSON removes markdown code blocks and extra whitespace
func cleanJSON(input string) string {
	input = strings.TrimSpace(input)

	// Remove markdown code blocks if present
	if strings.HasPrefix(input, "```json") {
		input = strings.TrimPrefix(input, "```json")
		input = strings.TrimSuffix(input, "```")
	} else if strings.HasPrefix(input, "```") {
		input = strings.TrimPrefix(input, "```")
		input = strings.TrimSuffix(input, "```")
	}

	return strings.TrimSpace(input)
}

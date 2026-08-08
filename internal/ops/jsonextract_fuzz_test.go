package ops

import (
	"strings"
	"testing"
)

// SEC-003. Fuzz target for extractJSON (A-003's fence/JSON extraction),
// running entirely offline: extractJSON is pure string manipulation with no
// provider call anywhere on its path, so this target makes zero network
// calls by construction -- there is nothing to gate behind
// SCHEMAFLUX_LIVE_TESTS.
//
// The seed corpus is hand-chosen from the shapes jsonextract.go's own doc
// comment names as the reason it exists (a fence with a preamble, an
// unfenced object with a sentence in front, a brace inside a string) plus
// the malformed/adversarial shapes SEC-003 names explicitly: unterminated
// fences, huge numbers, duplicate keys, trailing data, deeply nested
// brackets, and invalid UTF-8. It is not only generated cases -- the point
// of a seed corpus per AGENTS.md/the task bar is that these are defects or
// near-defects this package's own doc comments already describe, encoded so
// go test -fuzz starts mutating from a position that already knows what
// matters.
func FuzzExtractJSON(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		`{}`,
		`[]`,
		"Here is the JSON you asked for:\n```json\n{\"a\":1}\n```",
		"```json\n{\"a\":1}\n``` and here is why: ...",
		"```python\nprint('not json')\n```",
		`{"note": "a } inside a string"}`,
		`{"note": "an escaped \" quote and a } brace"}`,
		"```json\n{\"truncated\": tru",                                  // unterminated fence
		`{"truncated": [1, 2, 3`,                                        // unbalanced, truncated mid-array
		`{"a": 1e400}`,                                                  // huge exponent
		`{"a": 99999999999999999999999999}`,                             // huge integer literal
		`{"a": 1, "a": 2}`,                                              // duplicate keys
		`{"a": 1} trailing garbage {"b": 2}`,                            // trailing data after first value
		strings.Repeat(`{"a":`, 5000) + "1" + strings.Repeat("}", 5000), // deep nesting / bomb-shaped
		"not json at all, just a sentence.",
		"prefix \xff\xfe invalid utf-8 \x80 suffix {\"a\":1}",
		"```\n{\"a\":1}\n```", // fence with no info string
		"```json",             // fence opener only, nothing after
		"```json\n",           // fence opener + newline, empty body, no close
		"{",                   // single unmatched brace
		"[",                   // single unmatched bracket
		`{"nested": {"deep": {"deeper": [1,2,3]}}}`,
		"text before ```json\n{\"a\":1}\n``` text after ```json\n{\"b\":2}\n```", // two fenced blocks
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// The only property this offline target checks is "does not panic,
		// does not hang, and never returns something longer than what a
		// trimmed input could produce" -- extractJSON does not itself
		// validate JSON (that is strictdecode's job), so there is no
		// "valid JSON out" property to assert here.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("extractJSON panicked on %q: %v", input, r)
			}
		}()

		out := extractJSON(input)

		trimmed := strings.TrimSpace(input)
		if trimmed == "" && out != "" {
			t.Fatalf("extractJSON(%q) = %q, want empty for blank input", input, out)
		}
		if len(out) > len(input) {
			t.Fatalf("extractJSON(%q) = %q, output longer than input", input, out)
		}
	})
}

// FuzzFencedBlock isolates fencedBlock, the fence-scanning half of
// extractJSON, so the fuzzer can spend its budget on fence syntax
// specifically (adjacent/nested/unterminated fences) rather than splitting
// effort with the brace-balancing half.
func FuzzFencedBlock(f *testing.F) {
	seeds := []string{
		"",
		"```json\n{}\n```",
		"```\n{}\n```",
		"```json\n",
		"```",
		"``````",
		"```json\n{\"a\":1}\n```\n```json\n{\"b\":2}\n```",
		"```notjson\nplain text\n```",
		"text with no fence at all",
		"```json\n" + strings.Repeat("{", 10000),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("fencedBlock panicked on %q: %v", input, r)
			}
		}()
		content, ok := fencedBlock(input)
		if !ok && content != "" {
			t.Fatalf("fencedBlock(%q) = (%q, false), want empty content on failure", input, content)
		}
	})
}

// FuzzBalancedValue isolates the string/escape-aware brace scanner.
func FuzzBalancedValue(f *testing.F) {
	seeds := []string{
		"",
		"{}",
		"[]",
		`{"a": "value with \" escaped quote"}`,
		`{"a": "unterminated string`,
		"{{{{{{{{{{",
		"}}}}}}}}}}",
		strings.Repeat("[", 20000),
		`[1, 2, {"a": [3, 4, {"b": 5}]}]`,
		"garbage before { \"a\": 1 } garbage after",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("balancedValue panicked on %q: %v", input, r)
			}
		}()
		out, ok := balancedValue(input)
		if !ok && out != "" {
			t.Fatalf("balancedValue(%q) = (%q, false), want empty output on failure", input, out)
		}
	})
}

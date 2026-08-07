package ops

import "strings"

// extractJSON finds the JSON value inside a model response.
//
// It replaces sixteen copies of the same four-line prefix strip, which handled
// exactly one shape: a response that is nothing but a fenced block, with the
// fence at the very start and the closing fence at the very end. Models do not
// reliably produce that. They produce "Here is the JSON you asked for:" and
// then a fence; or a fence followed by an explanation; or an unfenced object
// with a sentence in front of it. Every one of those parsed as a failure, and
// the error said the body was malformed when the JSON in it was fine.
//
// The order matters. A fenced block is an explicit claim about where the payload
// is, so it wins. Only when there is no fence does the brace scan guess, and it
// guesses conservatively: the first balanced value, string- and escape-aware, so
// a brace inside a string cannot end the scan early.
func extractJSON(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}

	if fenced, ok := fencedBlock(trimmed); ok {
		if balanced, ok := balancedValue(fenced); ok {
			return balanced
		}
		return strings.TrimSpace(fenced)
	}

	if balanced, ok := balancedValue(trimmed); ok {
		return balanced
	}

	return trimmed
}

// fencedBlock returns the contents of the first fenced block that looks like it
// holds JSON, wherever it appears.
//
// "Looks like it holds JSON" means the info string is json, or absent and the
// body starts with a brace or bracket. A ```python block in an explanation is
// not the payload, and taking it would turn a recoverable response into a
// confusing failure.
func fencedBlock(input string) (string, bool) {
	rest := input
	for {
		start := strings.Index(rest, "```")
		if start < 0 {
			return "", false
		}
		afterTicks := rest[start+3:]

		// The info string runs to the end of the line.
		newline := strings.IndexByte(afterTicks, '\n')
		var info, body string
		if newline < 0 {
			// A fence with no newline after it cannot contain a block.
			info, body = afterTicks, ""
		} else {
			info = afterTicks[:newline]
			body = afterTicks[newline+1:]
		}
		info = strings.TrimSpace(strings.ToLower(info))

		end := strings.Index(body, "```")
		if end < 0 {
			// Unterminated fence: the model was cut off. Everything after the
			// opening fence is the best available answer -- a truncated object
			// still tells the repair loop what went wrong, where "no JSON
			// found" does not.
			content := strings.TrimSpace(body)
			if info == "json" || looksLikeJSON(content) {
				return content, true
			}
			return "", false
		}

		content := strings.TrimSpace(body[:end])
		if info == "json" || (info == "" && looksLikeJSON(content)) {
			return content, true
		}

		// Not our block. Continue after this one's closing fence.
		consumed := (start + 3) + len(info) + 1 + end + 3
		if consumed >= len(rest) {
			return "", false
		}
		rest = rest[consumed:]
	}
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

// balancedValue returns the first balanced JSON object or array in the input.
//
// It tracks string state and escapes, because a body like
// {"note": "a } inside a string"} would otherwise be cut at the brace in the
// prose -- producing invalid JSON out of valid JSON, which is worse than not
// trying.
func balancedValue(input string) (string, bool) {
	start := -1
	var open byte
	var closer byte

	for i := 0; i < len(input); i++ {
		if input[i] == '{' || input[i] == '[' {
			start = i
			open = input[i]
			if open == '{' {
				closer = '}'
			} else {
				closer = ']'
			}
			break
		}
	}
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(input); i++ {
		c := input[i]

		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		switch c {
		case open:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return input[start : i+1], true
			}
		}
	}

	// Unbalanced: the response was truncated. Return what there is, so the
	// decoder's error names the real problem rather than the extractor hiding
	// it. ErrTruncated (A-007) is where this eventually belongs.
	return strings.TrimSpace(input[start:]), true
}

package schemafluxtest

import (
	"strconv"
	"strings"
	"unicode/utf8"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// maxMessage bounds the message of a failed script, in runes. A system prompt
// is several kilobytes and a script can hold many rules, and a message that
// quotes either unbounded would bury the test's own output.
const maxMessage = 600

// clip cuts s to at most n runes, and marks a cut with "..." inside the n.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:max(n-3, 0)]) + "..."
}

// quoted is s with its white space collapsed, cut to n runes and quoted, so a
// system prompt that spans lines reads as one short phrase.
func quoted(s string, n int) string {
	return strconv.Quote(clip(strings.Join(strings.Fields(s), " "), n))
}

// count is "1 body" or "2 bodies": n with the singular or the plural.
func count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// describeRequest is what a request asks, in the library's words, without the
// user message: that holds the caller's data, and what is quoted of the system
// prompt is the library's own fixed text.
func describeRequest(req schemaflux.Request) string {
	var parts []string
	if req.Model != "" {
		parts = append(parts, "model "+quoted(req.Model, 40))
	}
	if req.SchemaName != "" {
		parts = append(parts, "schema "+quoted(req.SchemaName, 40))
	}
	if req.System != "" {
		parts = append(parts, "a system prompt beginning "+quoted(req.System, 48))
	}
	if req.Decision != nil {
		parts = append(parts, "a decision of "+count(len(req.Decision.Questions), "question", "questions"))
	}
	if len(req.Tools) > 0 {
		parts = append(parts, count(len(req.Tools), "tool", "tools"))
	}
	if len(req.Messages) > 0 {
		parts = append(parts, "a conversation of "+count(len(req.Messages), "message", "messages"))
	}
	parts = append(parts, "a user message of "+count(len(req.User), "byte", "bytes"))
	return strings.Join(parts, ", ")
}

// scriptFailure is the error of a script that does not cover what it was
// asked. The kind is ErrConfiguration: the test's set-up is wrong, and nothing
// the library does about a failed call would fix it. The message is bounded.
func scriptFailure(message string) error {
	return &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Message: clip(message, maxMessage)}
}

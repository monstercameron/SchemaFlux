package schemaflux

import (
	"slices"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The operations of this file work on lists the caller gave: Choose picks one
// item, Filter keeps some, Sort puts them in order, Rank scores each against a
// query and SemanticMatch pairs the items of two lists. None of them declares a
// schema to the provider. Each reads the model's answer as ids or positions of
// the caller's own items and checks it against the list in Go, so what comes
// back is always the caller's own values; but a level above well formed JSON
// needs a declared schema, and so a client whose DataPolicy sets a floor above
// ContractJSONWellFormed refuses all five with ErrPolicy before anything is
// sent.

// checkWordOf returns the internal error of kind configuration for a value that
// is not one of the words allowed, and nil for an empty value or one that is. The
// error names the field and the words and never the value: the value is the
// caller's text, and the message goes wherever the caller's logs go.
func checkWordOf(op, field, value string, allowed ...string) error {
	if value == "" || slices.Contains(allowed, value) {
		return nil
	}
	return types.Newf(types.KindConfiguration, op, "%s is not one of %s", field, wordList(allowed))
}

// wordList writes words as a person would in a sentence: a, b and c.
func wordList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// layFloor lays a number whose zero means the default and whose negative means
// none onto dst: a positive value replaces the default, a negative one becomes
// zero, and zero leaves the default as it is. The number has been checked.
func layFloor(dst *float64, v float64) {
	switch {
	case v > 0:
		*dst = v
	case v < 0:
		*dst = 0
	}
}

// layLimit is layFloor for a count: a positive value replaces the default, a
// negative one becomes zero, which the internal option reads as no limit, and
// zero leaves the default.
func layLimit(dst *int, v int) {
	switch {
	case v > 0:
		*dst = v
	case v < 0:
		*dst = 0
	}
}

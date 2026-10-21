package mw

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// redactRule is a redactPattern made ready to run: the replacement text it
// writes and, for an expression that starts on a literal, the anchored form of it
// that is run at each place the literal is.
//
// A rule was run over the whole of every text, one rule after another, each on
// what the last had left. A request of 110 KB with nothing in it to redact cost
// 59 ms, because every expression ran over all of it, and a rule could rewrite the
// marker of an earlier one or leave the tail of a secret a shorter rule had
// matched part of. Now each rule is looked for in the text as it came, and
// redactSpans decides between the matches.
type redactRule struct {
	redactPattern
	replacement string

	// anchored is the expression that has to match at the start of the text it
	// is run on, for a rule with starting literals; nil otherwise.
	anchored *regexp.Regexp

	// boundary is true when a match has to start at a word boundary, as the
	// \b the expression began with said before it was anchored.
	boundary bool
}

// newRedactRule readies pattern. An expression with starting literals is
// anchored, with its leading \b taken off and kept as boundary, since a slice of
// the text does not know what stands before it.
func newRedactRule(pattern redactPattern, replacement string) redactRule {
	rule := redactRule{redactPattern: pattern, replacement: replacement}
	if len(pattern.starts) == 0 || pattern.around != 0 {
		return rule
	}
	source := pattern.pattern.String()
	flags := ""
	for strings.HasPrefix(source, "(?") {
		end := strings.IndexByte(source, ')')
		if end < 0 || strings.ContainsAny(source[2:end], ":<>") {
			break
		}
		flags += source[:end+1]
		source = source[end+1:]
	}
	if rest, found := strings.CutPrefix(source, `\b`); found {
		rule.boundary = true
		source = rest
	}
	rule.anchored = regexp.MustCompile(`^` + flags + `(?:` + source + `)`)
	return rule
}

// redactSpan is a stretch of a text to be replaced, and the rule that found it.
type redactSpan struct {
	start, end int
	rule       int
}

// redactSpans returns text with every stretch a rule matched replaced by that
// rule's marker. Where two matches overlap, the one that starts first is kept,
// the longer of two that start together, and the earlier rule of two of one
// length; the other is dropped whole, so no marker is cut and no tail of a longer
// secret is left. A text nothing matches is returned as it came.
func redactSpans(text string, rules []redactRule) string {
	var spans []redactSpan
	for i := range rules {
		spans = rules[i].find(text, i, spans)
	}
	if len(spans) == 0 {
		return text
	}
	slices.SortFunc(spans, func(a, b redactSpan) int {
		switch {
		case a.start != b.start:
			return a.start - b.start
		case a.end != b.end:
			return b.end - a.end
		}
		return a.rule - b.rule
	})
	var out strings.Builder
	out.Grow(len(text))
	done := 0
	for _, span := range spans {
		if span.start < done {
			continue
		}
		out.WriteString(text[done:span.start])
		out.WriteString(rules[span.rule].replacement)
		done = span.end
	}
	out.WriteString(text[done:])
	return out.String()
}

// find appends to spans the stretches of text this rule matches.
func (r *redactRule) find(text string, index int, spans []redactSpan) []redactSpan {
	switch {
	case r.around != 0:
		return r.findAround(text, index, spans)
	case r.anchored != nil:
		return r.findAtStarts(text, index, spans)
	}
	return r.findEverywhere(text, index, spans)
}

// findEverywhere runs the expression over the whole text: the way of a rule of
// the caller's, which is not known to start on anything.
func (r *redactRule) findEverywhere(text string, index int, spans []redactSpan) []redactSpan {
	for _, m := range r.pattern.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2*r.group], m[2*r.group+1]
		if start >= 0 && end > start {
			spans = append(spans, redactSpan{start: start, end: end, rule: index})
		}
	}
	return spans
}

// findAtStarts runs the anchored expression at each place one of the starting
// literals is, and only there.
func (r *redactRule) findAtStarts(text string, index int, spans []redactSpan) []redactSpan {
	for _, start := range r.starts {
		for from := 0; from < len(text); {
			at := redactIndex(text[from:], start, r.fold)
			if at < 0 {
				break
			}
			at += from
			from = at + 1
			if r.boundary && at > 0 && redactWordByte(text[at-1]) {
				continue
			}
			m := r.anchored.FindStringSubmatchIndex(text[at:])
			if m == nil {
				continue
			}
			begin, end := m[2*r.group], m[2*r.group+1]
			if begin < 0 || end <= begin {
				continue
			}
			spans = append(spans, redactSpan{start: at + begin, end: at + end, rule: index})
			from = at + m[1]
		}
	}
	return spans
}

// findAround runs the expression on the stretch of text around each occurrence
// of the rule's byte: the characters either side of it that the expression could
// be made of, and no more. The stretches do not overlap, since the byte is not
// one of them, so the cost is one pass over the text and not a run of the
// expression over all of it. A stretch does not reach back over text an earlier
// match took.
func (r *redactRule) findAround(text string, index int, spans []redactSpan) []redactSpan {
	taken := 0
	for from := 0; from < len(text); {
		at := strings.IndexByte(text[from:], r.around)
		if at < 0 {
			break
		}
		at += from
		from = at + 1

		low := at
		for low > taken {
			c, size := utf8.DecodeLastRuneInString(text[:low])
			if !redactLocalRune(c) {
				break
			}
			low -= size
		}
		high := from
		for high < len(text) {
			c, size := utf8.DecodeRuneInString(text[high:])
			if !redactDomainRune(c) {
				break
			}
			high += size
		}
		m := r.pattern.FindStringIndex(text[low:high])
		if m == nil || m[1] <= m[0] {
			continue
		}
		spans = append(spans, redactSpan{start: low + m[0], end: low + m[1], rule: index})
		taken = low + m[1]
		from = max(from, taken)
	}
	return spans
}

// redactLocalRune reports whether c can be in the local part of an address:
// a letter, mark or digit of any script, or one of "._%+-".
func redactLocalRune(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsMark(c) || unicode.IsNumber(c) || strings.ContainsRune("._%+-", c)
}

// redactDomainRune reports whether c can be in the domain of an address: a
// letter, mark or digit of any script, a dot or a hyphen.
func redactDomainRune(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsMark(c) || unicode.IsNumber(c) || c == '.' || c == '-'
}

// redactWordByte reports whether c is a letter, digit or underscore of ASCII,
// which is what a regular expression's \b calls a word.
func redactWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// redactIndex is the index of the first occurrence of needle in text, or -1.
// With fold the ASCII letters of needle, which is lower case, match either case.
func redactIndex(text, needle string, fold bool) int {
	if !fold {
		return strings.Index(text, needle)
	}
	// Searching for the rarest letter of the needle, in both cases, and checking
	// what stands around it, skips most of the text a byte at a time.
	anchor := 0
	for i := range len(needle) {
		if strings.IndexByte(redactRareFirst, needle[i]) < strings.IndexByte(redactRareFirst, needle[anchor]) {
			anchor = i
		}
	}
	lower, upper := needle[anchor], needle[anchor]
	if lower >= 'a' && lower <= 'z' {
		upper -= 'a' - 'A'
	}
	// The anchor of a needle that fits starts at base or later, and ends before
	// end. Each case is searched for once and again only after it was passed.
	base, end := anchor, len(text)-len(needle)+anchor+1
	if base >= end {
		return -1
	}
	none := len(text)
	next := func(c byte, from int) int {
		if from >= end {
			return none
		}
		if at := strings.IndexByte(text[from:end], c); at >= 0 {
			return from + at
		}
		return none
	}
	atLower, atUpper := next(lower, base), none
	if upper != lower {
		atUpper = next(upper, base)
	}
	for {
		at := min(atLower, atUpper)
		if at == none {
			return -1
		}
		start := at - anchor
		if strings.EqualFold(text[start:start+len(needle)], needle) {
			return start
		}
		if at == atLower {
			atLower = next(lower, at+1)
		} else {
			atUpper = next(upper, at+1)
		}
	}
}

// redactRareFirst lists the lower-case letters from the one that is rarest in
// English text to the commonest. A byte that is not in it is the rarest of all.
const redactRareFirst = "zqxjkvbpygfwmucldrhsnioate"

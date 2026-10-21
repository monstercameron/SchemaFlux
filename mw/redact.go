package mw

import (
	"regexp"
	"slices"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	impl "github.com/monstercameron/schemaflux/v2/internal/runtime/mw"
)

// RedactOptions are the settings of Redact. The zero value is the default: the
// built-in rules, the marker "[redacted:NAME]", and no function of the caller's.
type RedactOptions struct {
	// NoBuiltins turns the built-in rules off, for a program that already
	// redacts secrets at a proxy and would rather this middleware did not do it
	// twice, or that wants only its own rules. The built-ins are the shapes of
	// the credentials of Anthropic, OpenAI, Google, AWS (an access key id),
	// GitHub (a token, and a fine-grained one), Slack and Stripe, a JWT, the token
	// of an authorization header or of a "Bearer" that stands alone, a private key
	// block (to the END line, or to the end of the text when it has none), and an
	// email address, whatever script it is written in. It turns protection off, so
	// it can be set in the first options value only: a later one is a
	// configuration error that says so. They do not include
	// anything shaped like an ordinary number: a rule for card numbers or phone
	// numbers fires on invoice totals and order numbers, so a program that needs
	// one adds it in Patterns, for its own domain.
	NoBuiltins bool

	// Patterns are rules of the caller's: a name, which appears in the marker
	// that replaces a match, and the expression. They are looked for beside the
	// built-ins, in every text that is sent, as the text came and not as an earlier
	// rule left it. Where two matches overlap the one that starts first wins, the
	// longer of two that start together, and the earlier rule of two of one length
	// (the built-ins, then the caller's by name), so a rule is never cut by the
	// marker of another and a short rule does not leave the tail of a longer
	// secret. A nil expression is a mistake in the set-up, and every request fails
	// with an error of kind schemaflux.ErrConfiguration before anything is sent: a
	// redactor that silently does less than it was told is worse than none. The
	// maps of every options value are joined, a later rule winning for a name both
	// have; a later value never drops a rule an earlier one asked for.
	Patterns map[string]*regexp.Regexp

	// Marker is the text that replaces a match. Every "%s" in it is the name of
	// the rule that matched, as plain text substitution and nothing else:
	// a "%" or a "$" is written as it is. A marker with no "%s" is used as it is
	// for every match. Empty is "[redacted:%s]".
	Marker string

	// Func is an extra pass for what an expression cannot express: a detector
	// of the caller's own, a lookup in a list of known account numbers. It runs
	// after every pattern, on the text they left, and may keep state. It is called
	// from many goroutines at once, once for each text of each request, and has to
	// be safe for that. Its result is sent as it is: the middleware does not bound
	// its size. It is the caller's code, and a panic in it fails the request with
	// an error of kind schemaflux.ErrInternal and sends nothing, since text it was
	// meant to scrub is not known to be scrubbed. Nil is none. The functions of
	// every options value are all run, in order.
	Func func(string) string
}

// Redact removes what must not leave the process from a request before it is
// handed to the wrapped provider, the last point at which that can be done. It
// redacts the system prompt and the user message, and every text of a typed
// Decision: its state and guidance, each question, each choice's options and
// their criteria. A decision provider reads the Decision and never the prompts,
// and a rule that covered the prompts alone would leave all of what it is sent in
// the clear. A schema, a schema's name and a model are identifiers the caller
// chose and are sent as they are: the enumerations and descriptions of a schema
// are not redacted, so a program that puts text of its customers there redacts it
// before it builds the schema.
//
// A choice is answered with one of its options, and the answer comes back
// carrying the option as the caller wrote it: where a rule rewrote an option, the
// label of the answer is given back as the caller's own, because the operation
// accepts only a label it offered. Two options that a rule rewrote into one text
// are sent apart by adding their positions to them, so that the answer can say
// which was chosen. Nothing else in an answer is touched: what comes back has
// already left the provider, and redacting it would not change what the provider
// received.
//
// The request the caller made is not modified: a redacted copy of what changed
// is sent.
//
// The built-in rules are deliberately narrow, because over-redacting does not
// fail a build, it silently corrupts a request in production. What a program
// cannot do without, such as account numbers, phone numbers and names, it adds in
// Patterns and Func.
//
// Where Redact goes decides what is sent unredacted to what: it goes outermost,
// so that nothing the chain does, a cache, a retry, a second route, sees the
// text before it is scrubbed. The package comment gives the order and why.
// Redact does not forward streaming: a wrapped provider cannot stream.
func Redact(opts ...RedactOptions) Middleware {
	o, problem := mergeRedactOptions(opts)

	var settings []impl.RedactOption
	if o.NoBuiltins {
		settings = append(settings, impl.WithoutBuiltins())
	}
	names := make([]string, 0, len(o.Patterns))
	for name := range o.Patterns {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		pattern := o.Patterns[name]
		if pattern == nil {
			problem = "Patterns holds a rule with no expression"
			break
		}
		settings = append(settings, impl.WithPattern(name, pattern))
	}
	if o.Marker != "" {
		settings = append(settings, impl.WithMarker(o.Marker))
	}
	if o.Func != nil {
		settings = append(settings, impl.WithFunc(o.Func))
	}
	build := impl.RedactEgress(settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.redact", next, problem, build)
	}
}

// mergeRedactOptions merges option values left to right so that a later value
// can add protection and cannot remove what an earlier one asked for. The pattern
// maps are joined, a later rule winning for a name both have, and the functions
// are run one after the other, the earlier first. A later Marker that is not
// empty wins, since it changes what a match is written as and not whether it is
// removed. NoBuiltins turns protection off, so only the first value may set it:
// in a later one it is a problem, and the second result says so, for the caller
// to make a configuration error of.
func mergeRedactOptions(opts []RedactOptions) (RedactOptions, string) {
	var merged RedactOptions
	var problem string
	for i, o := range opts {
		if o.NoBuiltins {
			if i == 0 {
				merged.NoBuiltins = true
			} else if problem == "" {
				problem = "NoBuiltins is set in a later options value, which would turn off rules an earlier one asked for: set it in the first"
			}
		}
		for name, pattern := range o.Patterns {
			if merged.Patterns == nil {
				merged.Patterns = make(map[string]*regexp.Regexp, len(o.Patterns))
			}
			merged.Patterns[name] = pattern
		}
		if o.Marker != "" {
			merged.Marker = o.Marker
		}
		merged.Func = both(merged.Func, o.Func)
	}
	return merged, problem
}

// both is a function that runs first and then second on what first returned, and
// whichever of them is not nil when the other is.
func both(first, second func(string) string) func(string) string {
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	}
	return func(text string) string { return second(first(text)) }
}

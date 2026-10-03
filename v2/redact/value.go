package redact

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Strategy is how Value replaces a sensitive value. The empty Strategy means
// Mask.
type Strategy string

const (
	// Nil replaces a sensitive string with the empty string. The field or key
	// stays, with nothing in it.
	Nil Strategy = "nil"

	// Mask replaces a sensitive string with MaskText when that is set, and with
	// MaskLength copies of MaskChar otherwise.
	Mask Strategy = "mask"

	// Remove replaces a sensitive string with the empty string, as Nil does. No
	// key or field is removed.
	Remove Strategy = "remove"

	// Jumble replaces a sensitive string with the same characters in another
	// order, or with other characters of the same kind, as JumbleMode says. It is
	// obfuscation and not anonymization: the length and the shape of the value
	// survive.
	Jumble Strategy = "jumble"

	// Scramble is another name for Jumble.
	Scramble Strategy = "scramble"
)

// JumbleMode is how the Jumble and Scramble strategies change a value. The empty
// JumbleMode means JumbleTypeAware.
type JumbleMode string

const (
	// JumbleBasic puts the characters of the value in another order.
	JumbleBasic JumbleMode = "basic"

	// JumbleSmart replaces each digit with another digit and each ASCII letter
	// with another of the same case that is a vowel when it was one, and leaves
	// every other character as it is.
	JumbleSmart JumbleMode = "smart"

	// JumbleTypeAware changes a value by what it looks like: an email address, a
	// phone number or a name keeps its shape.
	JumbleTypeAware JumbleMode = "typeaware"
)

// maxMaskLength is the longest fixed mask: MaskLength above it is refused. It is
// the limit of the internal operation, which a test holds this to.
const maxMaskLength = 1 << 16

// ValueOptions are Value's settings. The zero value is the default: the "pii"
// category, the Mask strategy and a mask of three '*'.
type ValueOptions struct {
	// Categories are the kinds of data found by pattern: "pii", "secrets" and
	// "financial", in any case. Nil means "pii". An empty list, or a name that is
	// not one of the three, is a configuration error.
	Categories []string

	// Strategy is how a sensitive value is replaced. Empty means Mask. Any value
	// that is not one of the constants is a configuration error.
	Strategy Strategy

	// MaskText is what a masked value is replaced with, whole. It overrides
	// MaskChar and MaskLength. Empty means it is not used. To replace a value with
	// nothing, use the Nil strategy.
	MaskText string

	// MaskChar is the character a mask is made of. Zero means '*'. A negative
	// number, a surrogate half or a number above the last character is a
	// configuration error.
	MaskChar rune

	// MaskLength is how many characters a mask has. Zero means 3 and -1 means as
	// many as the value has. A value below -1 or above 65536 is a configuration
	// error.
	MaskLength int

	// JumbleSeed makes the Jumble and Scramble strategies repeatable: the same
	// seed gives the same result for the same value. Zero means an unpredictable
	// seed for each call. A negative number is a seed like any other. A seed makes
	// the result reversible by whoever knows it.
	JumbleSeed int64

	// JumbleMode is how the Jumble and Scramble strategies change a value. Empty
	// means JumbleTypeAware. Any value that is not one of the constants is a
	// configuration error.
	JumbleMode JumbleMode

	// CustomPatterns are regular expressions for further values to replace, in
	// the syntax of the regexp package. Nil means none. One that does not compile,
	// or that matches the empty string, is a configuration error.
	CustomPatterns []string
}

// mergeValueOptions merges the values of a call (M0): a later field that is not
// its zero value replaces the earlier one, and no value at all is the zero value.
func mergeValueOptions(options []ValueOptions) ValueOptions {
	switch len(options) {
	case 0:
		return ValueOptions{}
	case 1:
		return options[0]
	}
	merged := options[0]
	for _, later := range options[1:] {
		if later.Categories != nil {
			merged.Categories = later.Categories
		}
		if later.Strategy != "" {
			merged.Strategy = later.Strategy
		}
		if later.MaskText != "" {
			merged.MaskText = later.MaskText
		}
		if later.MaskChar != 0 {
			merged.MaskChar = later.MaskChar
		}
		if later.MaskLength != 0 {
			merged.MaskLength = later.MaskLength
		}
		if later.JumbleSeed != 0 {
			merged.JumbleSeed = later.JumbleSeed
		}
		if later.JumbleMode != "" {
			merged.JumbleMode = later.JumbleMode
		}
		if later.CustomPatterns != nil {
			merged.CustomPatterns = later.CustomPatterns
		}
	}
	return merged
}

// checkValueOptions returns the internal error of a setting no call can use, or
// nil, for every setting that can be told without compiling a pattern. The
// messages name the field and the position and never repeat what the caller
// wrote there: a category or a pattern can hold the very data the call is meant
// to hide.
func checkValueOptions(o ValueOptions) error {
	if o.Categories != nil && len(o.Categories) == 0 {
		return types.Newf(types.KindConfiguration, valueOp, "Categories is empty: leave it nil for pii")
	}
	for i, category := range o.Categories {
		switch strings.ToLower(strings.TrimSpace(category)) {
		case "pii", "secrets", "financial":
		default:
			return types.Newf(types.KindConfiguration, valueOp, "Categories[%d] is not one of pii, secrets and financial", i)
		}
	}
	switch o.Strategy {
	case "", Nil, Mask, Remove, Jumble, Scramble:
	default:
		return types.Newf(types.KindConfiguration, valueOp, "Strategy is not one of Nil, Mask, Remove, Jumble and Scramble")
	}
	switch o.JumbleMode {
	case "", JumbleBasic, JumbleSmart, JumbleTypeAware:
	default:
		return types.Newf(types.KindConfiguration, valueOp, "JumbleMode is not one of JumbleBasic, JumbleSmart and JumbleTypeAware")
	}
	if o.MaskLength < -1 || o.MaskLength > maxMaskLength {
		return types.Newf(types.KindConfiguration, valueOp, "MaskLength must be -1, or from 0 to %d", maxMaskLength)
	}
	if !utf8.ValidRune(o.MaskChar) {
		return types.Newf(types.KindConfiguration, valueOp, "MaskChar is not a valid character")
	}
	return nil
}

// valueInternal builds the internal options of a Value call. It starts from the
// constructor's value, so that a field left at its zero keeps the default.
func valueInternal(o ValueOptions) ops.RedactOptions {
	in := ops.NewRedactOptions()
	if o.Categories != nil {
		in.Categories = o.Categories
	}
	if o.Strategy != "" {
		in.Strategy = ops.RedactStrategy(o.Strategy)
	}
	if o.MaskText != "" {
		in.MaskText = o.MaskText
	}
	if o.MaskChar != 0 {
		in.MaskChar = o.MaskChar
	}
	if o.MaskLength != 0 {
		in.MaskLength = o.MaskLength
	}
	if o.JumbleSeed != 0 {
		in.JumbleSeed = o.JumbleSeed
	}
	if o.JumbleMode != "" {
		in.JumbleMode = ops.JumbleMode(o.JumbleMode)
	}
	if o.CustomPatterns != nil {
		in.CustomPatterns = o.CustomPatterns
	}
	return in
}

// valueFailure is the error of a failed internal call as Value returns it. A
// configuration error at this point can only be a custom pattern, since every
// other setting was checked first, and the internal error for it carries the
// pattern's text in its message and its cause; it is replaced by one that names
// the position of the pattern. Any other error holds nothing of the caller's and
// is returned as it is.
func valueFailure(o ValueOptions, err error) error {
	if types.KindOf(err) != types.KindConfiguration {
		return err
	}
	for i, pattern := range o.CustomPatterns {
		compiled, compileErr := regexp.Compile(pattern)
		switch {
		case compileErr != nil:
			return types.Newf(types.KindConfiguration, valueOp, "CustomPatterns[%d] does not compile", i)
		case compiled.MatchString(""):
			return types.Newf(types.KindConfiguration, valueOp, "CustomPatterns[%d] matches the empty string", i)
		}
	}
	return types.Newf(types.KindConfiguration, valueOp, "the options cannot be used")
}

// valueOp is the name of Value in an error. Value adds no Record, and an error it
// returns names it the way a Record would.
const valueOp = "redact.value"

// Value returns a copy of input with its sensitive data replaced, and leaves
// input as it was. It works in Go, with patterns, field names and struct tags: it
// asks no model, takes no context, adds no Record, and is held to no floor of a
// client's data policy. For free text use Text.
//
//	safe, err := redact.Value(customer)
//	safe, err := redact.Value(customer, redact.ValueOptions{Categories: []string{"pii", "financial"}, MaskLength: -1})
//
// What it replaces is found three ways. A struct field with a non-empty `redact`
// tag is replaced whole. A struct field or a map key whose name is that of
// sensitive data (Password, SSN, APIKey, as a whole name and not a part of one) is
// replaced whole. Any other string, in a struct, a map, a slice or a pointer, is
// searched for the values the categories describe: email addresses, phone
// numbers, card numbers by their check digit, and so on. A value that is not a
// string and is replaced whole is set to its zero value.
//
// Where the shape of the data is known, tag the fields; the patterns are a net
// under the tags and do not find a name or an address in a note.
//
// A setting that cannot be used (see ValueOptions) is an error of kind
// ErrConfiguration. A nil interface, a value that refers back to itself or is
// nested more than 200 levels deep, and a field tagged for redaction that is not
// exported are errors of kind ErrInvalidRequest. Every error is an *Error with
// the Op "redact.value", and none holds a value of input or a setting the caller
// wrote. On any error the result is the zero value.
func Value[T any](input T, options ...ValueOptions) (T, error) {
	var zero T
	o := mergeValueOptions(options)
	if err := checkValueOptions(o); err != nil {
		return zero, bind.ConvertError(valueOp, err)
	}
	out, err := ops.Redact(input, valueInternal(o))
	if err != nil {
		return zero, bind.ConvertError(valueOp, valueFailure(o, err))
	}
	return out, nil
}

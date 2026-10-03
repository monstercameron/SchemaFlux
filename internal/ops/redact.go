// Redact operation for intelligent data masking.

package ops

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// maxRedactDepth bounds how deep the traversal follows pointers, slices, maps
// and structs. A value that refers back to itself would otherwise recurse until
// the stack is exhausted, which is a fatal error rather than a panic and cannot
// be recovered.
const maxRedactDepth = 200

// maxRedactMaskLength bounds a fixed mask, which is allocated for every value
// it replaces.
const maxRedactMaskLength = 1 << 16

var timeType = reflect.TypeOf(time.Time{})

// RedactResult contains detailed information about redaction operations
type RedactResult struct {
	Redacted map[string][]string `json:"redacted"` // category -> list of redacted values
	Count    int                 `json:"count"`    // total items redacted

	// FieldsRedacted names the struct fields that were replaced because of
	// their name or a redact tag, which the category map cannot express: a
	// field redacted by name matched no pattern.
	FieldsRedacted []string `json:"fields_redacted,omitempty"`

	Metadata map[string]any `json:"metadata"` // additional metadata
}

// RedactStrategy defines how sensitive data should be redacted
type RedactStrategy string

const (
	RedactNil      RedactStrategy = "nil"      // Replace with nil/null
	RedactMask     RedactStrategy = "mask"     // Replace with mask text
	RedactRemove   RedactStrategy = "remove"   // Replace with an empty string; no key or field is removed
	RedactJumble   RedactStrategy = "jumble"   // Scramble characters
	RedactScramble RedactStrategy = "scramble" // Alias for jumble
)

// JumbleMode defines how jumbling/scrambling should work
type JumbleMode string

const (
	JumbleBasic     JumbleMode = "basic"     // Simple character scrambling
	JumbleSmart     JumbleMode = "smart"     // Preserve some structure
	JumbleTypeAware JumbleMode = "typeaware" // Data type-specific scrambling
)

// RedactOptions configures the Redact operation
type RedactOptions struct {
	types.OpOptions
	Categories     []string       // Sensitive data categories to redact
	Strategy       RedactStrategy // How to redact the data
	MaskText       string         // Custom mask text (overrides MaskChar/MaskLength)
	MaskChar       rune           // Character to use for masking (default: '*')
	MaskLength     int            // Length of mask (-1 = use original length, default: 3)
	JumbleSeed     int64          // Random seed for reproducible jumbling
	JumbleMode     JumbleMode     // How to perform jumbling
	CustomPatterns []string       // Additional regex patterns to match

	// report accumulates what was redacted, so RedactWithResult can say. It is
	// a pointer because RedactOptions is copied by value through the recursive
	// traversal, and every copy has to record into the same place.
	report *redactionReport

	// compiledCustom holds CustomPatterns compiled once, by prepared, so a
	// pattern is not compiled again for every string the traversal meets.
	compiledCustom []*regexp.Regexp
}

// redactionReport accumulates what a redaction pass replaced.
type redactionReport struct {
	byCategory map[string][]string
	fields     []string

	// unreadable names the struct fields the traversal could not read, and so
	// left as they were. Only names: the values are the caller's data.
	unreadable map[string]struct{}
}

func newRedactionReport() *redactionReport {
	return &redactionReport{byCategory: map[string][]string{}, unreadable: map[string]struct{}{}}
}

func (r *redactionReport) addUnreadable(name string) {
	if r == nil {
		return
	}
	r.unreadable[name] = struct{}{}
}

// unreadableFields returns the names recorded by addUnreadable, sorted.
func (r *redactionReport) unreadableFields() []string {
	names := make([]string, 0, len(r.unreadable))
	for name := range r.unreadable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *redactionReport) addValues(found map[string][]string) {
	if r == nil {
		return
	}
	for category, values := range found {
		r.byCategory[category] = append(r.byCategory[category], values...)
	}
}

func (r *redactionReport) addField(name string) {
	if r == nil {
		return
	}
	r.fields = append(r.fields, name)
}

// NewRedactOptions creates RedactOptions with defaults
func NewRedactOptions() RedactOptions {
	return RedactOptions{
		OpOptions: types.OpOptions{
			Mode:         types.Strict,
			Intelligence: types.Smart,
		},
		Categories: []string{"PII"},
		Strategy:   RedactMask,
		MaskText:   "",
		MaskChar:   '*',
		MaskLength: 3,
		JumbleMode: JumbleTypeAware,
	}
}

// WithCategories sets the sensitive data categories to redact
func (opts RedactOptions) WithCategories(categories []string) RedactOptions {
	opts.Categories = categories
	return opts
}

// WithStrategy sets the redaction strategy
func (opts RedactOptions) WithStrategy(strategy RedactStrategy) RedactOptions {
	opts.Strategy = strategy
	return opts
}

// WithMaskText sets the text to use for masking
func (opts RedactOptions) WithMaskText(maskText string) RedactOptions {
	opts.MaskText = maskText
	return opts
}

// WithMaskChar sets the character to use for masking
func (opts RedactOptions) WithMaskChar(maskChar rune) RedactOptions {
	opts.MaskChar = maskChar
	return opts
}

// WithMaskLength sets the length of the mask (-1 to use original length)
func (opts RedactOptions) WithMaskLength(length int) RedactOptions {
	opts.MaskLength = length
	return opts
}

// WithJumbleSeed sets the random seed for reproducible jumbling
func (opts RedactOptions) WithJumbleSeed(seed int64) RedactOptions {
	opts.JumbleSeed = seed
	return opts
}

// WithJumbleMode sets how jumbling should work
func (opts RedactOptions) WithJumbleMode(mode JumbleMode) RedactOptions {
	opts.JumbleMode = mode
	return opts
}

// WithCustomPatterns adds custom regex patterns to match
func (opts RedactOptions) WithCustomPatterns(patterns []string) RedactOptions {
	opts.CustomPatterns = patterns
	return opts
}

// Validate checks if the options are valid.
//
// A category must be one of pii, secrets or financial (case and surrounding
// space ignored). An unrecognised one used to redact nothing and return a nil
// error, so a misspelt label returned the text untouched as a success. A custom
// pattern must compile and must not match the empty string, for the same reason:
// one that cannot be applied is an error, not a pass that quietly leaves out
// what the caller asked to remove.
func (opts RedactOptions) Validate() error {
	_, err := opts.prepared()
	return err
}

// prepared validates the options and returns them with the custom patterns
// compiled.
func (opts RedactOptions) prepared() (RedactOptions, error) {
	if len(opts.Categories) == 0 {
		return RedactOptions{}, types.Newf(types.KindConfiguration, "redact", "at least one category must be specified")
	}
	for i, category := range opts.Categories {
		if !knownRedactCategory(category) {
			return RedactOptions{}, types.Newf(types.KindConfiguration, "redact",
				"category %d (%q) is not one of pii, secrets or financial", i, category)
		}
	}

	validStrategies := []RedactStrategy{RedactNil, RedactMask, RedactRemove, RedactJumble, RedactScramble}
	strategyValid := false
	for _, s := range validStrategies {
		if opts.Strategy == s {
			strategyValid = true
			break
		}
	}
	if !strategyValid {
		return RedactOptions{}, types.Newf(types.KindConfiguration, "redact", "invalid strategy: %s", opts.Strategy)
	}

	if opts.MaskLength < -1 || opts.MaskLength > maxRedactMaskLength {
		return RedactOptions{}, types.Newf(types.KindConfiguration, "redact",
			"MaskLength %d is out of range: use -1 for the length of the value, or 0 to %d", opts.MaskLength, maxRedactMaskLength)
	}

	compiled := make([]*regexp.Regexp, 0, len(opts.CustomPatterns))
	for i, raw := range opts.CustomPatterns {
		pattern, err := regexp.Compile(raw)
		if err != nil {
			return RedactOptions{}, &types.OperationError{
				Kind: types.KindConfiguration, Op: "redact", Message: fmt.Sprintf("custom pattern %d does not compile", i), Cause: err,
			}
		}
		if pattern.MatchString("") {
			return RedactOptions{}, types.Newf(types.KindConfiguration, "redact",
				"custom pattern %d matches the empty string, so it would match between every two characters", i)
		}
		compiled = append(compiled, pattern)
	}
	opts.compiledCustom = compiled
	return opts, nil
}

// knownRedactCategory reports whether a category is one Redact detects.
func knownRedactCategory(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "pii", "secrets", "financial":
		return true
	}
	return false
}

// resolveRedactOptions picks the options to use, defaulting when none are
// given.
//
// The signature is typed now. It used to take ...interface{} with a `default:`
// branch that silently substituted the defaults for anything it did not
// recognise -- so passing the wrong options struct, which the compiler would
// have caught for every other operation in this library, produced a redaction
// pass configured with none of the caller's settings and no error.
func resolveRedactOptions(opts ...RedactOptions) RedactOptions {
	if len(opts) == 0 {
		return NewRedactOptions()
	}
	return opts[0]
}

// Redact removes or masks sensitive information from data.
// Returns a new object of the same type with sensitive data redacted.
//
// # What it detects, and what it does not
//
// Field names are matched as whole names, not substrings: FirstName and APIKey
// are redacted; Filename, Username, Keywords, KeyMetrics, FirstSeen,
// LastUpdated, and CardCount are not. The `redact` struct tag is the precise
// mechanism and should be preferred when you know the shape of your data.
//
// Values are matched by pattern per category. Card numbers are validated with
// Luhn rather than recognised by shape, so an unformatted 16-digit PAN is
// caught and a 16-digit order number is not. SSNs are matched in the forms they
// are written in; a bare nine-digit run is deliberately NOT matched, because it
// is indistinguishable from an order ID and matching it destroyed ordinary
// data.
//
// It is a pattern matcher, not a classifier. It finds what its categories
// describe and nothing else: a card number spelled out in words, or a name
// inside a free-text note, is not detected. Tagging the fields is the reliable
// path; the patterns are a safety net under it.
//
// # Jumbling is obfuscation, not anonymization
//
// RedactJumble and RedactScramble permute characters, which preserves length,
// alphabet, and frequency. Use them for demo data that has to look realistic,
// not for anything where re-identification matters.
func Redact[T any](input T, opts ...RedactOptions) (T, error) {
	log := logger.GetLogger()
	log.Debug("Starting redact operation", "requestID", "unknown", "inputType", fmt.Sprintf("%T", input))

	options, err := resolveRedactOptions(opts...).prepared()
	if err != nil {
		log.Error("Redact operation validation failed", "requestID", "unknown", "error", err)
		var zero T
		return zero, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	result, err := redactValue(input, options)
	if err != nil {
		log.Error("Redact operation failed", "requestID", "unknown", "error", err)
		return result, err
	}

	log.Debug("Redact operation succeeded", "requestID", "unknown")
	return result, nil
}

// RedactWithResult redacts and reports what it replaced.
//
// The report has two halves, because they answer different questions. Redacted
// maps a category to the values that matched its patterns. FieldsRedacted names
// the struct fields replaced because of their name or a `redact` tag -- those
// matched no pattern, so the category map cannot express them.
//
// It returned an empty map for every input until OP-503, whatever the operation
// had done, so an audit of a redaction pass read as "nothing was redacted" with
// a nil error. That is worth knowing when reading old output: an empty report
// from a released version before that is not evidence of anything.
//
// Metadata["unreadable_fields"] lists, sorted, the names of struct fields the
// traversal could not read (unexported ones) and so copied as they were. A
// caller who needs those redacted has to export them or tag them: a tagged
// field that cannot be read is an error, not a name in this list.
//
// On an error the result is empty.
func RedactWithResult[T any](input T, opts ...RedactOptions) (T, RedactResult, error) {
	result := RedactResult{
		Redacted: make(map[string][]string),
		Metadata: make(map[string]any),
	}

	options, err := resolveRedactOptions(opts...).prepared()
	if err != nil {
		var zero T
		return zero, RedactResult{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}
	options.report = newRedactionReport()

	redacted, err := redactValue(input, options)
	if err != nil {
		var zero T
		return zero, RedactResult{}, err
	}

	// This used to return an empty map with a nil error whatever it had done,
	// so a caller auditing a redaction pass was told nothing was redacted and
	// treated that as success.
	result.Redacted = options.report.byCategory
	if result.Redacted == nil {
		result.Redacted = map[string][]string{}
	}
	result.FieldsRedacted = options.report.fields
	result.Metadata["fields_redacted"] = len(options.report.fields)
	result.Metadata["unreadable_fields"] = options.report.unreadableFields()

	total := 0
	for _, values := range result.Redacted {
		total += len(values)
	}
	result.Metadata["values_redacted"] = total
	result.Count = total + len(options.report.fields)

	return redacted, result, nil
}

// / redactValue redacts a caller's value. The traversal is in redactReflect.
//
// A nil interface is refused: it carries nothing to redact, and returning it as
// a success would read as "looked and found nothing".
func redactValue[T any](input T, opts RedactOptions) (T, error) {
	var zero T
	v := reflect.ValueOf(input)
	if !v.IsValid() {
		return zero, types.Newf(types.KindInvalidRequest, "redact", "invalid input: a nil interface carries nothing to redact")
	}

	out, err := redactReflect(v, opts, 0)
	if err != nil {
		return zero, err
	}
	redacted, ok := out.Interface().(T)
	if !ok {
		return zero, types.Newf(types.KindInvariantViolation, "redact", "the redacted value is not a %T", input)
	}
	return redacted, nil
}

// redactReflect returns a redacted copy of v, of the same type as v. It never
// writes to v.
//
// What it cannot read it leaves alone: the unexported fields of a struct are
// copied with it and named in the report. What it cannot redact it reports as
// an error rather than returning in the clear: see redactStructValue.
//
// It used to panic on unexported fields (every struct holding a time.Time), on
// a nil pointer reached through a field, a slice or an interface, on a named
// string type, and on an array, because each recursive call went through
// interface{} and the result was stored back with Set, which requires the exact
// type.
func redactReflect(v reflect.Value, opts RedactOptions, depth int) (reflect.Value, error) {
	if depth > maxRedactDepth {
		return reflect.Value{}, types.Newf(types.KindInvalidRequest, "redact",
			"the value is nested more than %d levels deep, which means it refers back to itself or is too deep to walk", maxRedactDepth)
	}

	switch v.Kind() {
	case reflect.String:
		out := reflect.New(v.Type()).Elem()
		out.SetString(redactString(v.String(), opts))
		return out, nil

	case reflect.Interface:
		if v.IsNil() {
			return v, nil
		}
		inner, err := redactReflect(v.Elem(), opts, depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(inner)
		return out, nil

	case reflect.Pointer:
		if v.IsNil() {
			return v, nil
		}
		inner, err := redactReflect(v.Elem(), opts, depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(inner)
		return out, nil

	case reflect.Struct:
		return redactStructValue(v, opts, depth)

	case reflect.Slice:
		if v.IsNil() {
			return v, nil
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		if err := redactElements(out, v, opts, depth); err != nil {
			return reflect.Value{}, err
		}
		return out, nil

	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		if err := redactElements(out, v, opts, depth); err != nil {
			return reflect.Value{}, err
		}
		return out, nil

	case reflect.Map:
		return redactMapValue(v, opts, depth)

	default:
		// Numbers, booleans, channels and functions hold nothing a pattern can
		// match.
		return v, nil
	}
}

// redactElements fills dst, a slice or an array of src's type and length, with
// the redacted elements of src.
func redactElements(dst, src reflect.Value, opts RedactOptions, depth int) error {
	if !typeMayHoldText(src.Type().Elem()) {
		// A []byte or a []float64 has nothing to redact, and one reflect call
		// per element is slow on a large one.
		reflect.Copy(dst, src)
		return nil
	}
	for i := 0; i < src.Len(); i++ {
		elem, err := redactReflect(src.Index(i), opts, depth+1)
		if err != nil {
			return err
		}
		dst.Index(i).Set(elem)
	}
	return nil
}

// typeMayHoldText reports whether a value of the type can contain a string.
func typeMayHoldText(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Interface, reflect.Pointer, reflect.Struct,
		reflect.Slice, reflect.Array, reflect.Map:
		return true
	}
	return false
}

// redactStructValue redacts the exported fields of a struct.
//
// A field named as sensitive, or carrying a redact tag, is replaced wholesale
// by redactNamed. Any other field is walked, and the part of it that matches a
// pattern is replaced, so a notes field with a phone number in it keeps the
// note. Only the first of the two is recorded as a redacted field: the second is
// in the report as values.
//
// The unexported fields are copied as they are, because reflection cannot read
// or set them, and named in the report. A tagged one is the exception: the
// caller named it for redaction and it cannot be redacted, so the call fails
// with the field's name rather than returning it in the clear.
//
// A time.Time is copied as a whole and not reported: its fields are unexported
// and it holds no text.
func redactStructValue(v reflect.Value, opts RedactOptions, depth int) (reflect.Value, error) {
	t := v.Type()
	out := reflect.New(t).Elem()
	out.Set(v)
	if t == timeType {
		return out, nil
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tagged := field.Tag.Get("redact") != ""

		if !field.IsExported() {
			if tagged {
				return reflect.Value{}, types.Newf(types.KindInvalidRequest, "redact",
					"field %q is tagged for redaction but is unexported, so it cannot be read or replaced", field.Name)
			}
			opts.report.addUnreadable(field.Name)
			continue
		}

		var redacted reflect.Value
		var err error
		if tagged || fieldNameIsSensitive(field.Name) {
			opts.report.addField(field.Name)
			redacted, err = redactNamed(v.Field(i), opts, depth+1)
		} else {
			redacted, err = redactReflect(v.Field(i), opts, depth+1)
		}
		if err != nil {
			return reflect.Value{}, err
		}
		out.Field(i).Set(redacted)
	}
	return out, nil
}

// redactMapValue redacts the values of a map. A key names its value exactly as a
// struct field name does, so a value under "ssn" is replaced wholesale.
func redactMapValue(v reflect.Value, opts RedactOptions, depth int) (reflect.Value, error) {
	if v.IsNil() {
		return v, nil
	}
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	iter := v.MapRange()
	for iter.Next() {
		key, value := iter.Key(), iter.Value()

		var redacted reflect.Value
		var err error
		if key.Kind() == reflect.String && fieldNameIsSensitive(key.String()) {
			opts.report.addField(key.String())
			redacted, err = redactNamed(value, opts, depth+1)
		} else {
			redacted, err = redactReflect(value, opts, depth+1)
		}
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetMapIndex(key, redacted)
	}
	return out, nil
}

// redactNamed replaces a value the caller named for redaction, by field name,
// map key or tag, in the way its kind allows: a string gets the strategy
// applied, a pointer or an interface has what it holds replaced, and anything
// else is set to its zero value. The result has the type of v.
//
// Anything else used to come back unchanged under the default mask, with the
// field listed as redacted: a Password []byte, an integer SSN, or a slice of
// secrets was reported as handled and returned in the clear.
func redactNamed(v reflect.Value, opts RedactOptions, depth int) (reflect.Value, error) {
	if depth > maxRedactDepth {
		return reflect.Value{}, types.Newf(types.KindInvalidRequest, "redact",
			"the value is nested more than %d levels deep", maxRedactDepth)
	}

	switch v.Kind() {
	case reflect.String:
		out := reflect.New(v.Type()).Elem()
		out.SetString(applyRedactionStrategy(v.String(), opts))
		return out, nil

	case reflect.Interface:
		if v.IsNil() {
			return v, nil
		}
		inner, err := redactNamed(v.Elem(), opts, depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(inner)
		return out, nil

	case reflect.Pointer:
		if v.IsNil() {
			return v, nil
		}
		inner, err := redactNamed(v.Elem(), opts, depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(inner)
		return out, nil

	default:
		return reflect.Zero(v.Type()), nil
	}
}

// redactString redacts sensitive information from a string.
func redactString(input string, opts RedactOptions) string {
	redacted, _ := redactStringWithReport(input, opts)
	return redacted
}

// redactStringWithReport redacts and reports what it replaced, keyed by
// category. The report is what RedactWithResult returns; it used to be empty by
// construction, so an audit of a redaction pass read as "nothing was redacted".
func redactStringWithReport(input string, opts RedactOptions) (string, map[string][]string) {
	report := map[string][]string{}
	if input == "" {
		return input, report
	}

	result := input
	defer func() { opts.report.addValues(report) }()

	for _, category := range opts.Categories {
		for _, pattern := range categoryPatterns(category) {
			result = pattern.ReplaceAllStringFunc(result, func(match string) string {
				report[category] = append(report[category], match)
				return applyRedactionStrategy(match, opts)
			})
		}

		if categoryUsesCardDetection(category) {
			result = replaceCardNumbers(result, func(match string) string {
				report[category] = append(report[category], match)
				return applyRedactionStrategy(match, opts)
			})
		}
	}

	for _, pattern := range opts.customRegexps() {
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			report["custom"] = append(report["custom"], match)
			return applyRedactionStrategy(match, opts)
		})
	}

	return result, report
}

// customRegexps returns the compiled custom patterns. The options reaching a
// redaction pass went through prepared, which compiled them and refused any
// that did not compile; the fallback is for internal callers that did not.
func (opts RedactOptions) customRegexps() []*regexp.Regexp {
	if opts.compiledCustom != nil || len(opts.CustomPatterns) == 0 {
		return opts.compiledCustom
	}
	prepared, err := opts.prepared()
	if err != nil {
		return nil
	}
	return prepared.compiledCustom
}

// generateMaskText creates mask text based on the options. A MaskLength of -1
// makes the mask as long as the value in characters.
func generateMaskText(originalValue string, opts RedactOptions) string {
	// If custom mask text is provided, use it
	if opts.MaskText != "" {
		return opts.MaskText
	}

	length := opts.MaskLength
	if length == -1 {
		length = utf8.RuneCountInString(originalValue)
	}
	if length < 0 || length > maxRedactMaskLength {
		// Validate refuses these; an internal caller that skipped it gets the
		// default rather than a panic from make.
		length = 3
	}

	mask := make([]rune, length)
	for i := range mask {
		mask[i] = opts.MaskChar
	}
	return string(mask)
}

// applyRedactionStrategy applies the chosen redaction strategy to a value
func applyRedactionStrategy(value string, opts RedactOptions) string {
	switch opts.Strategy {
	case RedactNil:
		return ""
	case RedactMask:
		return generateMaskText(value, opts)
	case RedactRemove:
		return ""
	case RedactJumble, RedactScramble:
		return jumbleString(value, opts)
	default:
		return generateMaskText(value, opts)
	}
}

// jumbleString permutes the characters of the input.
//
// # This is obfuscation, not anonymization
//
// A permutation preserves length, alphabet, and character frequency. Those
// three facts are often enough to re-identify a value, so jumbling is suitable
// for demo data and screenshots that need to look realistic, and is not
// suitable for anything where re-identification matters. Use RedactMask or
// RedactNil for that.
//
// It used to be worse than that: JumbleSeed defaults to zero and the RNG was
// then seeded with the input's LENGTH -- a number an attacker reads off the
// output. The permutation was fully determined by a seed anyone could
// reproduce with this library, which made RedactJumble and RedactScramble
// exactly invertible. The default now draws from crypto/rand and is not
// reproducible.
//
// Setting JumbleSeed explicitly restores determinism, and with it
// invertibility by anyone who knows the seed. That is a reasonable thing to
// want for reproducible test fixtures and an unreasonable thing to rely on for
// privacy.
func jumbleString(input string, opts RedactOptions) string {
	if input == "" {
		return input
	}

	seed := opts.JumbleSeed
	if seed == 0 {
		seed = unpredictableSeed()
	}
	r := rand.New(rand.NewSource(seed))

	switch opts.JumbleMode {
	case JumbleTypeAware:
		return jumbleTypeAware(input, r)
	case JumbleSmart:
		return jumbleSmart(input, r)
	default: // JumbleBasic
		return jumbleBasic(input, r)
	}
}

// unpredictableSeed draws a seed the caller cannot reconstruct from the output.
// A failure to read the system source falls back to the clock, which is weaker
// but still not a function of the input.
func unpredictableSeed() int64 {
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err == nil {
		return int64(binary.LittleEndian.Uint64(buf[:]))
	}
	return time.Now().UnixNano()
}

// jumbleBasic performs simple character scrambling.
//
// A shuffle drawn at random is the identity one time in n!, which for a short
// value is often enough that a field listed as redacted came back readable. A
// result equal to the input is therefore changed by exchanging the first
// character with the first one that differs from it. A value with nothing to
// rearrange, one character or one repeated, has no other arrangement and is
// returned as it is.
func jumbleBasic(input string, r *rand.Rand) string {
	runes := []rune(input)
	for i := len(runes) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		runes[i], runes[j] = runes[j], runes[i]
	}
	jumbled := string(runes)
	if jumbled != input {
		return jumbled
	}
	for i := 1; i < len(runes); i++ {
		if runes[i] != runes[0] {
			runes[0], runes[i] = runes[i], runes[0]
			return string(runes)
		}
	}
	return jumbled
}

// jumbleSmart replaces each character with another of the same class, so the
// result reads like the same kind of thing without being a rearrangement of it.
//
// It was documented as preserving vowel and consonant structure and implemented
// as a call to jumbleBasic, which preserves neither -- the doc comment described
// a function nobody had written (T-12).
//
// Substitution rather than permutation is also the safer of the two. A shuffle
// keeps the exact multiset of characters, so the original is a rearrangement
// away and the character counts are a fingerprint; drawing a fresh character
// per position keeps only the shape. That is the same lesson OP-505 recorded
// about JumbleSeed, applied to the transform itself. It is still obfuscation,
// not anonymisation: length and layout survive on purpose, because that is what
// "preserve some structure" means.
func jumbleSmart(input string, r *rand.Rand) string {
	const (
		lowerVowels     = "aeiou"
		lowerConsonants = "bcdfghjklmnpqrstvwxyz"
		digits          = "0123456789"
	)

	pick := func(set string) rune {
		return rune(set[r.Intn(len(set))])
	}

	out := make([]rune, 0, len(input))
	for _, char := range input {
		switch {
		case char >= '0' && char <= '9':
			out = append(out, pick(digits))
		case isVowel(char):
			replacement := pick(lowerVowels)
			out = append(out, matchCase(char, replacement))
		case isASCIILetter(char):
			replacement := pick(lowerConsonants)
			out = append(out, matchCase(char, replacement))
		default:
			// Punctuation, spaces, and any non-ASCII letter are left alone:
			// they are the structure this mode exists to preserve, and
			// substituting a rune outside ASCII risks producing something that
			// is not a letter at all.
			out = append(out, char)
		}
	}
	return string(out)
}

func isVowel(char rune) bool {
	switch char {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return true
	}
	return false
}

func isASCIILetter(char rune) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

// matchCase returns the replacement in the same case as the original.
func matchCase(original, replacement rune) rune {
	if original >= 'A' && original <= 'Z' {
		return replacement - 'a' + 'A'
	}
	return replacement
}

// The shapes jumbleTypeAware tells apart, compiled once.
var (
	jumbleEmailShape = regexp.MustCompile(`\S+@\S+\.\S+`)
	jumblePhoneShape = regexp.MustCompile(`\b\d{3}-\d{3}-\d{4}\b`)
	jumbleNameShape  = regexp.MustCompile(`\b[A-Z][a-z]+ [A-Z][a-z]+\b`)
	jumbleDigit      = regexp.MustCompile(`\d`)
)

// jumbleTypeAware uses data type-specific scrambling rules
func jumbleTypeAware(input string, r *rand.Rand) string {
	// Detect data type and apply appropriate scrambling
	if jumbleEmailShape.MatchString(input) {
		return jumbleEmail(input, r)
	}
	if jumblePhoneShape.MatchString(input) {
		return jumblePhone(input, r)
	}
	if jumbleNameShape.MatchString(input) {
		return jumbleName(input, r)
	}

	// Default to basic jumbling
	return jumbleBasic(input, r)
}

// jumbleEmail scrambles an email address
func jumbleEmail(email string, r *rand.Rand) string {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return jumbleBasic(email, r)
	}

	username := jumbleBasic(parts[0], r)
	domainParts := strings.Split(parts[1], ".")
	if len(domainParts) > 0 {
		// Keep domain extension, jumble domain name
		domainParts[0] = jumbleBasic(domainParts[0], r)
	}

	return username + "@" + strings.Join(domainParts, ".")
}

// jumblePhone scrambles a phone number while preserving format
func jumblePhone(phone string, r *rand.Rand) string {
	// Simple digit scrambling while preserving hyphens
	digits := jumbleDigit.FindAllString(phone, -1)

	// Scramble digits
	for i := len(digits) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		digits[i], digits[j] = digits[j], digits[i]
	}

	// Reconstruct with non-digits
	result := ""
	digitIdx := 0
	for _, char := range phone {
		if char >= '0' && char <= '9' {
			result += digits[digitIdx]
			digitIdx++
		} else {
			result += string(char)
		}
	}

	return result
}

// jumbleName scrambles a name
func jumbleName(name string, r *rand.Rand) string {
	parts := strings.Fields(name)
	for i, part := range parts {
		parts[i] = jumbleBasic(part, r)
	}
	return strings.Join(parts, " ")
}

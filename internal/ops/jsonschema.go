package ops

import (
	"cmp"
	"encoding"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The library generates an exact schema for T and renders it into the system
// prompt as prose. The provider then asks only for `json_object` — free-form
// JSON — so the one artifact that could make Extract[T] structurally guaranteed
// was never sent to the API that can enforce it. The central claim of the
// library, that the type is the contract, was enforced by persuasion.
//
// GenerateJSONSchema produces the machine-readable form, for the Responses API's
// json_schema strict mode.

// GenerateJSONSchema renders a Go type as a JSON Schema suitable for
// structured-output enforcement, or nil when the type cannot be expressed.
//
// Strict mode has rules the schema has to satisfy: every object needs
// "additionalProperties": false and must list every property in "required".
// Optional fields are therefore expressed as a union with null rather than by
// omission, which is what the API expects.
//
// The schema of a type is worked out once and each caller is given a copy of it,
// which is theirs to change. Operations that only put it into a request use the
// one the type's plan holds (typePlan.sharedSchema) and do not copy it.
func GenerateJSONSchema(targetType reflect.Type) map[string]any {
	plan := typePlanFor(targetType)
	schema := plan.sharedSchema()
	if schema == nil {
		return nil
	}
	if !plan.retained {
		// Worked out for this call alone, so nothing else holds it.
		return schema
	}
	return cloneSchema(schema)
}

// maxJSONSchemaDepth bounds the expansion, as the prose schema is bounded.
const maxJSONSchemaDepth = 6

func jsonSchemaFor(targetType reflect.Type, seen map[reflect.Type]bool, depth int) map[string]any {
	optional := false
	for targetType != nil && targetType.Kind() == reflect.Ptr {
		optional = true
		targetType = targetType.Elem()
	}
	if targetType == nil || depth > maxJSONSchemaDepth {
		return nil
	}

	var schema map[string]any

	switch targetType.Kind() {
	case reflect.String:
		schema = map[string]any{"type": "string"}
	case reflect.Bool:
		schema = map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		schema = map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		schema = map[string]any{"type": "number"}

	case reflect.Struct:
		if targetType == reflect.TypeOf(time.Time{}) {
			schema = map[string]any{"type": "string", "format": "date-time"}
			break
		}
		if seen[targetType] {
			// A recursive type cannot be expressed in strict mode without $ref
			// support, so the whole schema is abandoned rather than silently
			// truncated: a partial contract enforced as if complete is worse
			// than no contract.
			return nil
		}
		seen[targetType] = true
		defer delete(seen, targetType)

		// A type that embeds itself, directly or through the structs it embeds,
		// has no end to the fields it promotes, and is declined as a recursive
		// type is. Following it overflowed the stack, which cannot be recovered
		// from.
		if embeddingCycle(targetType) {
			return nil
		}

		properties := map[string]any{}
		var required []string

		if !collectStructProperties(targetType, seen, depth, properties, &required) {
			return nil
		}
		if len(properties) == 0 {
			return nil
		}

		schema = map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		}

	case reflect.Slice, reflect.Array:
		// encoding/json writes a slice of bytes as one base64 string, not as
		// a list of numbers. Declared as a list, a strict provider is made to
		// answer with numbers that then cannot be read back into the field.
		// An array of bytes is written as numbers, and is left to the general
		// case.
		if targetType.Kind() == reflect.Slice && targetType.Elem().Kind() == reflect.Uint8 {
			schema = map[string]any{"type": "string"}
			break
		}
		items := jsonSchemaFor(targetType.Elem(), seen, depth+1)
		if items == nil {
			return nil
		}
		schema = map[string]any{"type": "array", "items": items}

	case reflect.Map:
		// Strict mode cannot express an open-ended object, so a map means the
		// type is not enforceable and the caller falls back to prompt-only.
		return nil

	default:
		return nil
	}

	if optional {
		schema = nullable(schema)
	}
	return schema
}

// collectStructProperties fills properties and required for a struct with the
// members encoding/json reads into it (jsonMembers), each once and in the order
// encoding/json writes them. It reports false when any member cannot be
// expressed, and when a member can be written but not read back.
func collectStructProperties(structType reflect.Type, seen map[reflect.Type]bool, depth int,
	properties map[string]any, required *[]string) bool {

	for _, member := range jsonMembers(structType) {
		// encoding/json cannot allocate an embedded pointer to a struct type
		// that is not exported, so an answer that fills a field promoted through
		// one is an error at decode. Declaring the field would ask the provider
		// for an answer that cannot be read; the type is declined instead.
		if member.unexportedPointer != nil {
			return false
		}

		fieldSchema := jsonSchemaFor(member.field.Type, seen, depth+1)
		if fieldSchema == nil {
			return false
		}

		// Strict mode requires every property to appear in "required", so an
		// optional field is expressed as a union with null instead.
		//
		// Optionality comes from the same resolver the prompt schema and the
		// validator use, so the JSON Schema sent to the provider says the same
		// thing as the prose schema in the prompt and the check applied to the
		// answer. Three descriptions of one contract that could disagree is
		// three chances to disagree.
		if !IsRequired(member.field) {
			fieldSchema = nullable(fieldSchema)
		}

		properties[member.name] = fieldSchema
		*required = append(*required, member.name)
	}

	return true
}

// jsonMember is a field that encoding/json reads into a struct type, found
// directly in it or promoted from an embedded struct.
type jsonMember struct {
	// name is what the member is called in JSON.
	name string
	// tagged says the name was given by a json tag and not taken from the Go name.
	tagged bool
	// index is the position of the field in each struct on the way to it.
	index []int
	// route is the Go names of the embedded fields the member is promoted
	// through, outermost first. It is empty for a field of the struct itself.
	route []string
	// field is the field itself.
	field reflect.StructField
	// unexportedPointer is set when the way to the member passes through an
	// embedded pointer to a struct type that is not exported.
	unexportedPointer *jsonUnexportedPointer
}

// jsonUnexportedPointer is an embedded pointer to a struct type that is not
// exported, which encoding/json can write through and cannot allocate when it
// reads.
type jsonUnexportedPointer struct {
	// route is the embedded fields up to and including the pointer.
	route []string
	// target is the struct the pointer points to.
	target reflect.Type
}

// jsonEmbedded is an embedded struct waiting to have its fields looked at.
type jsonEmbedded struct {
	typ               reflect.Type
	index             []int
	route             []string
	unexportedPointer *jsonUnexportedPointer
}

// jsonMembers lists the fields encoding/json reads into a struct type, which is
// what the schema has to declare and nothing else. It is encoding/json's own
// rule, which the documentation of Marshal states: the fields of an embedded
// struct are promoted, an embedded struct that is not exported still promotes
// the exported fields it has, the option embed in a tag that gives no name does
// the same for a field that is not embedded (and makes a map or a
// jsontext.Value the one that takes the members the type does not name, which
// is not a member), and where several fields have one JSON name the
// one at the shallowest depth is the field. If more than one is at that depth,
// the one given by a json tag is the field. If that is still more than one, none
// is, which is how a name that two embedded structs both declare, or one struct
// reached by two routes, is dropped and not read into either.
//
// The members come back in the order encoding/json writes them, which is the
// order the fields are declared with each embedded struct in the place it
// stands. A struct that embeds itself has no end to its members, and the
// callers refuse it before they ask (embeddingCycle); the walk itself looks at
// each struct once and so does finish on one.
func jsonMembers(structType reflect.Type) []jsonMember {
	var current []jsonEmbedded
	next := []jsonEmbedded{{typ: structType}}
	// How many times each struct was reached at the level being looked at and at
	// the one after it.
	var count, nextCount map[reflect.Type]int
	visited := map[reflect.Type]bool{}
	var members []jsonMember

	for len(next) > 0 {
		current, next = next, nil
		count, nextCount = nextCount, map[reflect.Type]int{}

		for _, level := range current {
			if visited[level.typ] {
				continue
			}
			visited[level.typ] = true

			for i := 0; i < level.typ.NumField(); i++ {
				field := level.typ.Field(i)
				role, tag := jsonFieldRole(field)
				if role == jsonLeftOut {
					continue
				}
				index := append(slices.Clone(level.index), i)

				fieldType := jsonHeldType(field.Type)

				if role == jsonOwnMember {
					member := jsonMember{
						name: tag.name, tagged: tag.name != "", index: index, route: level.route,
						field: field, unexportedPointer: level.unexportedPointer,
					}
					if !member.tagged {
						member.name = field.Name
					}
					members = append(members, member)
					if count[level.typ] > 1 {
						// The struct was reached more than once at this level. A
						// second copy of the member makes it ambiguous below, as it
						// is to encoding/json.
						members = append(members, member)
					}
					continue
				}

				nextCount[fieldType]++
				if nextCount[fieldType] == 1 {
					embedded := jsonEmbedded{
						typ: fieldType, index: index, unexportedPointer: level.unexportedPointer,
						route: append(slices.Clone(level.route), field.Name),
					}
					if embedded.unexportedPointer == nil && field.Type.Kind() == reflect.Ptr && !field.IsExported() {
						embedded.unexportedPointer = &jsonUnexportedPointer{route: embedded.route, target: fieldType}
					}
					next = append(next, embedded)
				}
			}
		}
	}

	slices.SortFunc(members, func(a, b jsonMember) int {
		if byName := strings.Compare(a.name, b.name); byName != 0 {
			return byName
		}
		if byDepth := cmp.Compare(len(a.index), len(b.index)); byDepth != 0 {
			return byDepth
		}
		if a.tagged != b.tagged {
			if a.tagged {
				return -1
			}
			return 1
		}
		return slices.Compare(a.index, b.index)
	})

	// Of the members with one name, the first is the field unless the next is as
	// good: at the same depth and as much tagged.
	resolved := members[:0]
	for start := 0; start < len(members); {
		end := start + 1
		for end < len(members) && members[end].name == members[start].name {
			end++
		}
		group := members[start:end]
		start = end
		if len(group) > 1 && len(group[0].index) == len(group[1].index) && group[0].tagged == group[1].tagged {
			continue
		}
		resolved = append(resolved, group[0])
	}

	slices.SortFunc(resolved, func(a, b jsonMember) int {
		return slices.Compare(a.index, b.index)
	})
	return resolved
}

// jsonTag is what encoding/json reads out of the json tag of a field.
type jsonTag struct {
	// name is the name the tag gives the field, empty when it gives none.
	name string
	// omitted is true for the tag "-", which leaves the field out.
	omitted bool
	// embed is true when the tag has the option embed.
	embed bool
	// omitzero is true when the tag has the option omitzero.
	omitzero bool
}

// jsonTagName is the name a json tag gives a field, which is empty when it gives
// none, and whether the tag leaves the field out of JSON.
func jsonTagName(tag string) (name string, omitted bool) {
	parsed := parseJSONTag(tag)
	return parsed.name, parsed.omitted
}

// parseJSONTag reads a json tag the way encoding/json does since Go 1.27, when
// the package came to be the encoding/json/v2 implementation run with the
// behaviour of v1. A name is the text up to the first comma, backslash, quote or
// backquote. When it ends at one of those and not at a comma, what is a name is
// the Go identifier the tag begins with, if it begins with one: `a\b` and `a'b`
// name a field "a", and `'b'` and `1a'b` name nothing, so the Go name is the
// name. Go 1.26 took the whole text up to the comma as the name and refused it
// if it held anything but letters, digits and a few marks of punctuation.
//
// Options are read the same way: each is a Go identifier, so `omitempty:x` and
// `embed ` carry the options omitempty and embed, where 1.26 compared the text
// between two commas whole. Only the options that decide which members a type
// has are kept; parseJSONTagOptions keeps the rest of the ones that matter.
func parseJSONTag(tag string) jsonTag {
	return parseJSONTagOptions(tag).jsonTag
}

// jsonCaseRule is how encoding/json matches a key of an answer to a member's
// name when it reads: the name as it is written wins, and otherwise the first
// member whose name matches without regard to case, which is every member but
// the ones that say case:strict.
type jsonCaseRule uint8

const (
	// jsonCaseDefault is a tag with no rule, or with one that is not read:
	// case:ignore and case:strict together cancel each other, as a value that is
	// neither word is not a rule at all.
	jsonCaseDefault jsonCaseRule = iota
	// jsonCaseIgnore is case:ignore, which is what the default already is under
	// encoding/json (v2 by itself matches names exactly, and v1 does not).
	jsonCaseIgnore
	// jsonCaseStrict is case:strict: only the name as it is written matches.
	jsonCaseStrict
)

// jsonTagOptions is a json tag read in full, with what jsonTag keeps.
type jsonTagOptions struct {
	jsonTag
	// omitempty is true when the tag has the option omitempty.
	omitempty bool
	// caseRule is how the member's name is matched when an answer is read.
	caseRule jsonCaseRule
	// format is true when the tag has the option format with a value that
	// encoding/json reads as one: an identifier, or a string in single quotes,
	// that is not empty. A type with one is not read or written by encoding/json
	// at all, whatever the field's type and the option's value.
	format bool
}

// leavesOutZero reports whether the tag says the zero value of the member is not
// written, which is what omitempty and omitzero do and is how a type says the
// member may be missing.
func (o jsonTagOptions) leavesOutZero() bool {
	return o.omitempty || o.omitzero
}

// parseJSONTagOptions is parseJSONTag with the options that decide whether a
// member may be missing from an answer, how its name is matched, and whether
// encoding/json can read the type at all. It is the one reader of a tag's
// options.
func parseJSONTagOptions(tag string) jsonTagOptions {
	if tag == "-" {
		return jsonTagOptions{jsonTag: jsonTag{omitted: true}}
	}
	var parsed jsonTagOptions
	var ignoreCase, strictCase bool
	if tag != "" && tag[0] != ',' {
		n := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
			return !strings.ContainsRune(",\\'\"`", r)
		}))
		name := tag[:n]
		if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
			var named bool
			name, n, named = consumeJSONTagOption(tag, false)
			if !named {
				name = ""
			}
		}
		if !utf8.ValidString(name) {
			name = string([]rune(name))
		}
		parsed.name = name
		tag = tag[n:]
	}

	for len(tag) > 0 {
		if tag[0] == ',' {
			tag = tag[1:]
			if tag == "" {
				break
			}
		}
		option, n, _ := consumeJSONTagOption(tag, false)
		tag = tag[n:]
		switch option {
		case "embed":
			parsed.embed = true
		case "omitzero":
			parsed.omitzero = true
		case "omitempty":
			parsed.omitempty = true
		case "case":
			// The value after the colon is read as an option of its own, so that
			// what follows it is not taken for one.
			if value, ok := strings.CutPrefix(tag, ":"); ok {
				if rule, n, named := consumeJSONTagOption(value, false); named {
					tag = value[n:]
					switch rule {
					case "ignore":
						ignoreCase = true
					case "strict":
						strictCase = true
					}
				} else {
					tag = value
				}
			}
		case "format":
			if value, ok := strings.CutPrefix(tag, ":"); ok {
				tag = value
				if format, n, named := consumeJSONTagOption(value, true); named && format != "" {
					tag = value[n:]
					parsed.format = true
				}
			}
		}
	}
	switch {
	case strictCase && !ignoreCase:
		parsed.caseRule = jsonCaseStrict
	case ignoreCase && !strictCase:
		parsed.caseRule = jsonCaseIgnore
	}
	return parsed
}

// consumeJSONTagOption reads the option that begins the text: a Go identifier,
// or when quoted is true a string in single quotes, as encoding/json/v2 does. It
// returns the option and how many bytes it took. When the text begins with
// neither it takes everything up to the next comma, and says so by returning
// false.
func consumeJSONTagOption(in string, quoted bool) (option string, n int, ok bool) {
	comma := strings.IndexByte(in, ',')
	if comma < 0 {
		comma = len(in)
	}

	r, _ := utf8.DecodeRuneInString(in)
	switch {
	case r == '_' || unicode.IsLetter(r):
		n := len(in) - len(strings.TrimLeftFunc(in, isJSONTagLetterOrDigit))
		return in[:n], n, true

	case r == '\'' && quoted:
		// The grammar is the one of a Go string in double quotes with the quote
		// characters swapped, so the text is rewritten into one and unquoted.
		var inEscape bool
		rewritten := []byte{'"'}
		n := len("'")
		for len(in) > n {
			r, size := utf8.DecodeRuneInString(in[n:])
			switch {
			case inEscape:
				if r == '\'' {
					rewritten = rewritten[:len(rewritten)-1]
				}
				inEscape = false
			case r == '\\':
				inEscape = true
			case r == '"':
				rewritten = append(rewritten, '\\')
			case r == '\'':
				rewritten = append(rewritten, '"')
				n += len("'")
				unquoted, err := strconv.Unquote(string(rewritten))
				if err != nil {
					return in[:comma], comma, false
				}
				return unquoted, n, true
			}
			rewritten = append(rewritten, in[n:][:size]...)
			n += size
		}
	}
	return in[:comma], comma, false
}

func isJSONTagLetterOrDigit(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// jsonRole says what encoding/json does with one field of a struct.
type jsonRole int

const (
	// jsonLeftOut is a field that is neither read nor written, or one that names
	// no member: an embedded map or jsontext.Value takes the members the type does
	// not name, and is not one of them.
	jsonLeftOut jsonRole = iota
	// jsonOwnMember is a field that is read and written under a name of its own.
	jsonOwnMember
	// jsonPromoted is a field whose struct gives its members to the struct that
	// holds it.
	jsonPromoted
)

// jsonMethodTypes are the interfaces whose methods encoding/json calls to read
// and write a value: a type that has them is not taken apart into members.
var jsonMethodTypes = []reflect.Type{
	reflect.TypeFor[json.Marshaler](), reflect.TypeFor[json.Unmarshaler](),
	reflect.TypeFor[encoding.TextMarshaler](), reflect.TypeFor[encoding.TextUnmarshaler](),
	reflect.TypeFor[encoding.TextAppender](),
	reflect.TypeFor[jsonv2.MarshalerTo](), reflect.TypeFor[jsonv2.UnmarshalerFrom](),
}

func jsonImplementsAny(t reflect.Type, interfaces ...reflect.Type) bool {
	for _, wanted := range interfaces {
		if t.Implements(wanted) || reflect.PointerTo(t).Implements(wanted) {
			return true
		}
	}
	return false
}

// jsonHeldType is the type a field holds for the purpose of embedding: one level
// of pointer is looked through, as Go allows an embedded T or *T and no more.
func jsonHeldType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Ptr && t.Name() == "" {
		return t.Elem()
	}
	return t
}

// jsonFieldRole is the role encoding/json gives a field of a struct, with the
// tag it read to decide it. A field is promoted when it is embedded and the tag
// gives it no name, or when the tag has the option embed and gives no name, and
// the field holds a struct. An unexported embedded struct is promoted all the
// same, for the exported fields it has; an unexported field of any other kind is
// left out.
func jsonFieldRole(field reflect.StructField) (jsonRole, jsonTag) {
	tag := parseJSONTag(field.Tag.Get("json"))
	if tag.omitted {
		return jsonLeftOut, tag
	}
	if !field.IsExported() && !field.Anonymous {
		return jsonLeftOut, tag
	}

	held := jsonHeldType(field.Type)
	embeds := tag.name == "" && (tag.embed || (field.Anonymous && held.Kind() == reflect.Struct))
	switch {
	case embeds && held.Kind() == reflect.Struct:
		return jsonPromoted, tag
	case embeds && !field.IsExported():
		return jsonLeftOut, tag
	case embeds && held == reflect.TypeFor[jsontext.Value]():
		return jsonLeftOut, tag
	case embeds && held.Kind() == reflect.Map && held.Key().Kind() == reflect.String &&
		!jsonImplementsAny(held.Key(), jsonMethodTypes...):
		return jsonLeftOut, tag
	}

	// A field of its own. One that is not exported got here as an embedded
	// struct given a name, which has nothing to offer when it has methods of its
	// own to call, which cannot be called on an unexported field.
	if !field.IsExported() {
		if held.Kind() != reflect.Struct || jsonImplementsAny(held, jsonMethodTypes...) ||
			(tag.omitzero && jsonImplementsAny(held, reflect.TypeFor[interface{ IsZero() bool }]())) {
			return jsonLeftOut, tag
		}
	}
	return jsonOwnMember, tag
}

// nullable widens a schema to permit null, which is how strict mode expresses
// an optional value.
func nullable(schema map[string]any) map[string]any {
	raw, ok := schema["type"]
	if !ok {
		return schema
	}

	switch typed := raw.(type) {
	case string:
		if typed == "null" {
			return schema
		}
		widened := map[string]any{}
		for k, v := range schema {
			widened[k] = v
		}
		widened["type"] = []string{typed, "null"}
		return widened
	default:
		return schema
	}
}

// strictEnvelope builds the object schema for an operation's response envelope
// in a form strict structured output accepts.
//
// Several operations wrap the caller's T in an envelope of their own
// ("answer" plus an optional "confidence", "compressed" plus two lists) and
// used to write that envelope's schema by hand: an object, its properties, and
// a "required" naming only the fields that must be present. The live API
// refuses that shape -- "'additionalProperties' is required to be supplied and
// to be false" -- so Question, Compress and Predict could not complete a call
// (LX-003). Strict mode has two rules an envelope written by hand keeps
// forgetting: the object is closed, and every property is listed in required.
// A field the model may leave out is declared as a union with null instead.
//
// required names the properties the model must fill with a real value. Every
// other property is widened to accept null. The required list is sorted,
// because it is rendered into the request and an unsorted render changes the
// bytes between calls.
//
// It returns nil when any property has no schema. GenerateJSONSchema returns
// nil for a type strict mode cannot express, and a nil dropped into an envelope
// would go onto the wire as `"field": null`, which the API rejects. A caller
// treats a nil envelope the way it treats a nil schema for T: no schema is
// sent, and the operation runs at the prompt-only contract level.
//
// A required name that is not one of the properties is a mistake in the
// calling code, not a condition of the input, so it panics: left alone, a
// misspelt name would quietly turn the field it meant into an optional one.
func strictEnvelope(properties map[string]any, required ...string) map[string]any {
	mustFill := make(map[string]bool, len(required))
	for _, name := range required {
		if _, declared := properties[name]; !declared {
			panic(fmt.Sprintf("strictEnvelope: required property %q is not declared", name))
		}
		mustFill[name] = true
	}

	declared := make(map[string]any, len(properties))
	for name, raw := range properties {
		schema, ok := raw.(map[string]any)
		if !ok || len(schema) == 0 {
			return nil
		}
		if mustFill[name] {
			declared[name] = schema
			continue
		}
		declared[name] = acceptingNull(schema)
	}

	return map[string]any{
		"type":                 "object",
		"properties":           declared,
		"required":             sortedKeys(declared),
		"additionalProperties": false,
	}
}

// What a declared answer may hold. A provider's structured output puts a
// ceiling on the enum values and on the properties a schema declares, and a
// schema past it comes back as a rejected request, after the caller's data has
// been sent. An operation whose schema grows with its input counts first and
// refuses with the count.
//
// The property figure is the ceiling OpenAI documents for structured output,
// 5,000 object properties in all. It was set at half that, which refused
// arbitrations that had been accepted before there was a bound at all. The
// enum figure is half the documented 1,000, and nothing reaches it that ran
// before.
const (
	strictEnumBudget     = 500
	strictPropertyBudget = 5000
)

// listOf returns the schema for an array of items, or nil when the item schema
// is missing, for the same reason strictEnvelope returns nil.
func listOf(items map[string]any) map[string]any {
	if len(items) == 0 {
		return nil
	}
	return map[string]any{"type": "array", "items": items}
}

// acceptingNull returns a schema that also admits null.
//
// nullable handles a schema with a single named type. One without a "type"
// keyword, or with a list of types that does not already include null, is left
// unchanged by it, so those are expressed as a union.
func acceptingNull(schema map[string]any) map[string]any {
	switch kind := schema["type"].(type) {
	case string:
		return nullable(schema)
	case []string:
		for _, name := range kind {
			if name == "null" {
				return schema
			}
		}
	}
	return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
}

// schemaNameFor produces a name the API accepts: letters, digits, and
// underscores only.
func schemaNameFor(targetType reflect.Type) string {
	for targetType != nil && targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	if targetType == nil {
		return "result"
	}

	var builder strings.Builder
	for _, r := range targetType.Name() {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}

	name := builder.String()
	if name == "" {
		return "result"
	}
	return name
}

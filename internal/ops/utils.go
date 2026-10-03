package ops

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// sortedKeys returns a map's keys in a stable order. Go randomizes map
// iteration, so rendering a map into a prompt unsorted produces different bytes
// on every call: no prefix cache can hit, and no run is reproducible.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// GenerateTypeSchema creates a human-readable schema description for a Go type.
//
// It recurses. It used to expand only the top level and describe every field
// with its Go type name, so
//
//	type Order struct {
//	    Customer Person      `json:"customer"`
//	    Items    []OrderItem `json:"items"`
//	}
//
// produced `customer: main.Person` and `items: []main.OrderItem` — names that
// mean nothing to a model, which then had to invent the shape of the thing it
// was being asked to produce.
//
// It is written once for each type and read from there after, so the thirty
// operations that put a type's shape in their prompt do not each walk the type
// on every call.
func GenerateTypeSchema(targetType reflect.Type) string {
	return typePlanFor(targetType).proseSchema()
}

// maxSchemaDepth bounds the expansion. A schema deeper than this is more prompt
// than it is worth, and the bound is also what stops a self-referential type
// from expanding forever.
const maxSchemaDepth = 6

// describeType renders a type, expanding structs, slices, and maps. seen holds
// the types on the current path so a cycle -- a Node with a Parent *Node -- is
// named rather than followed.
func describeType(targetType reflect.Type, seen map[reflect.Type]bool, depth int) string {
	for targetType != nil && targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	if targetType == nil {
		return "unknown"
	}

	switch targetType.Kind() {
	case reflect.Struct:
		if targetType.String() == "time.Time" {
			return "datetime (RFC3339)"
		}
		if depth >= maxSchemaDepth {
			return targetType.String() + " (nested, not expanded)"
		}
		if seen[targetType] {
			// A cycle. Naming it is honest; following it does not terminate.
			return targetType.String() + " (recursive)"
		}

		seen[targetType] = true
		defer delete(seen, targetType)

		// The members are the ones the JSON schema declares and encoding/json
		// reads, each once and in the order encoding/json writes them: the
		// fields of an embedded struct stand where the struct does, a name
		// reached twice at one depth is left out, and an outer name hides an
		// embedded one. Walking the fields here with a rule of its own is what
		// printed a name twice, with two types, and showed a struct embedded
		// under a tag of options only as an object, while the schema sent in
		// the same request declared another shape.
		var fields []string
		for _, member := range jsonMembers(targetType) {
			// One resolution for both the schema the model sees and the
			// validation Strict applies. They read the same function, so they
			// cannot disagree about which fields are required -- which they
			// would have, the moment one of them learned about the tag first.
			requiredStr := ""
			if IsRequired(member.field) {
				requiredStr = " (required)"
			}

			// While the fields of an embedded struct are described, that struct
			// is on the path, so that a field that refers back to it is named
			// as recursive and not expanded once more.
			onPath := markEmbeddedOnPath(targetType, member.index, seen)
			fields = append(fields, fmt.Sprintf("  %s: %s%s",
				member.name, indentNested(describeType(member.field.Type, seen, depth+1)), requiredStr))
			for _, embedded := range onPath {
				delete(seen, embedded)
			}
		}

		if len(fields) == 0 {
			return "{}"
		}
		return fmt.Sprintf("{\n%s\n}", strings.Join(fields, "\n"))

	case reflect.Slice, reflect.Array:
		return "[" + describeType(targetType.Elem(), seen, depth+1) + "]"

	case reflect.Map:
		return fmt.Sprintf("map[%s]%s",
			GetTypeDescription(targetType.Key()),
			describeType(targetType.Elem(), seen, depth+1))

	default:
		return GetTypeDescription(targetType)
	}
}

// indentNested pushes a nested block in one level so the shape stays readable.
func indentNested(body string) string {
	if !strings.Contains(body, "\n") {
		return body
	}
	lines := strings.Split(body, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// markEmbeddedOnPath adds to seen the structs a member is promoted through,
// which are the ones its index passes between the struct it is found in and the
// member's own field, and returns the ones it added.
func markEmbeddedOnPath(structType reflect.Type, index []int, seen map[reflect.Type]bool) []reflect.Type {
	if len(index) < 2 {
		return nil
	}
	var added []reflect.Type
	current := structType
	for _, position := range index[:len(index)-1] {
		current = current.Field(position).Type
		if current.Kind() == reflect.Ptr {
			current = current.Elem()
		}
		if !seen[current] {
			seen[current] = true
			added = append(added, current)
		}
	}
	return added
}

// GetTypeDescription returns a one-line description of a leaf type. Structs,
// slices, and maps are rendered by describeType, which recurses; this is what
// it calls at the leaves and what map keys use.
func GetTypeDescription(targetType reflect.Type) string {
	switch targetType.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "unsigned integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Struct:
		if targetType.String() == "time.Time" {
			return "datetime (RFC3339)"
		}
		return targetType.String()
	case reflect.Ptr:
		return GetTypeDescription(targetType.Elem()) + " (optional)"
	default:
		return targetType.String()
	}
}

// NormalizeInput converts various input types to string for LLM processing. The
// inputs it refuses are nil, which has no text, and a value that JSON cannot
// write and that has no String method; both are an invalid request: the
// caller's argument cannot be used. It is classified here, once, so that every
// operation that normalises its input refuses it the same way and none has to
// classify it again.
func NormalizeInput(input any) (string, error) {
	if input == nil {
		return "", types.Classified(types.KindInvalidRequest, errors.New("input is nil"))
	}

	switch inputValue := input.(type) {
	case string:
		return inputValue, nil
	case []byte:
		return string(inputValue), nil
	default:
		// JSON before Stringer, which is the reverse of what this used to do.
		//
		// Any type with a String() method took the Stringer branch, and
		// time.Time has one -- so a timestamp was sent to the model as
		// "2026-08-07 18:04:05.999 -0400 EDT" while the generated schema told
		// it, in the same request, that the format was RFC3339. The model was
		// asked to reconcile two descriptions of the same value and the library
		// had given it no way to.
		//
		// A json.Marshaler is a type's own statement about its wire form, and
		// that is the form the schema describes, so it wins. Stringer is a
		// display format and stays as the fallback for types JSON cannot
		// render.
		//
		// promptJSON, not json.Marshal: the output goes into a prompt, where
		// "<", ">" and "&" are themselves and not something to escape for HTML
		// (LX-127).
		if b, err := promptJSON(input); err == nil {
			return string(b), nil
		}
		if stringer, ok := input.(fmt.Stringer); ok {
			return stringer.String(), nil
		}
		// Not fmt.Sprint: it prints every field of a struct, a field tagged
		// `json:"-"` and an unexported one included, and the caller has said the
		// type is not serialised that way. A value that JSON cannot write and that
		// has no String method of its own has no text to send.
		return "", types.Classified(types.KindInvalidRequest, errors.New("the input cannot be written as JSON"))
	}
}

// BuildExtractSystemPrompt creates the system prompt for extraction based on mode
func BuildExtractSystemPrompt(typeSchema string, mode types.Mode) string {
	base := fmt.Sprintf(`You are a data extraction expert. Extract structured data from the input and return it as JSON.
Target schema:
%s

Rules:
- Extract all relevant information that maps to the schema
- Return ONLY valid JSON, no explanations or markdown`, typeSchema)

	switch mode {
	case types.Strict:
		return base + `
- All required fields MUST be present and valid
- Fail if any required field cannot be extracted
- Use null only for explicitly optional fields
- Validate data types strictly`

	case types.TransformMode:
		return base + `
- Infer missing fields intelligently when possible
- Use reasonable defaults for missing data
- Be flexible with type conversions
- Preserve as much information as possible`

	case types.Creative:
		// Creative on an extraction used to say "Generate plausible values for
		// missing fields" and "Prioritize completeness over strict accuracy" --
		// which is an instruction to fabricate, on the one operation whose
		// entire purpose is faithfulness to a source. A caller reaching for
		// Creative wants flexible *interpretation* of what is there, not
		// invention of what is not, and nothing in the name told them the
		// difference.
		//
		// It now means the most permissive reading of the source. Inference is
		// still allowed and still marked; invention is not.
		return base + `
- Interpret ambiguous or loosely-worded data generously
- Use context elsewhere in the input to resolve what a field refers to
- Accept values that are implied rather than stated, where the implication is clear
- Leave a field null when the input does not support a value: do NOT invent one`

	default:
		return base
	}
}

// BuildGenerateStringPrompt creates the system prompt for string generation
func BuildGenerateStringPrompt(mode types.Mode) string {
	switch mode {
	case types.Strict:
		return "You are a precise content generator. Generate exactly what is requested, following all specifications strictly."
	case types.TransformMode:
		return "You are a creative content generator. Generate the requested content while maintaining quality and relevance."
	case types.Creative:
		return "You are a highly creative content generator. Generate engaging, original content based on the prompt."
	default:
		return "You are a content generator. Generate the requested content based on the prompt."
	}
}

// ValidateExtractedData validates extracted data meets requirements
// ValidateExtractedData is what Strict mode enforces. It checks that every
// required field was actually populated, at every level of the structure.
//
// Which fields those are is FieldRequiredness's answer: an explicit
// `schemaflux:"required"` or `schemaflux:"optional"` tag, then the field's type
// (a pointer can be nil, which is how Go spells "may be absent"), then
// `omitempty` as the legacy inference.
//
// It used to take a threshold it never read and check only the top level, so
// Strict() on a struct with a nested address whose every field came back empty
// reported success. Strict is the most confidence-inspiring word in this API;
// it should mean something.
//
// A field is "populated" if it is not the zero value of its type. That is a
// blunt rule and it is the honest one available: a model that returns 0 for an
// int it could not determine is indistinguishable from one that determined 0.
// Mark such fields `schemaflux:"optional"` to say so, or make them pointers.
func ValidateExtractedData(data any) error {
	if data == nil {
		return types.Classified(types.KindMalformedOutput, fmt.Errorf("data cannot be nil"))
	}

	value := reflect.ValueOf(data)
	if !value.IsValid() {
		return types.Classified(types.KindMalformedOutput, fmt.Errorf("invalid data"))
	}

	var trail fieldTrail
	return validateRequiredFields(value, &trail, 0)
}

// maxValidationDepth bounds the walk, matching the schema's own bound: a
// structure the schema did not describe is one Strict cannot meaningfully
// enforce.
const maxValidationDepth = maxSchemaDepth

// trailKind is what a step of a fieldTrail is.
type trailKind int

const (
	trailField trailKind = iota
	trailIndex
	trailKey
)

// trailStep is one step of a fieldTrail: a struct field by name, a list element
// by index, or a map element by key.
type trailStep struct {
	kind  trailKind
	name  string
	index int
	key   reflect.Value
}

// fieldTrail is where in the value the check of required fields has got to: one
// step for each struct field, list index or map key it has gone through.
//
// The steps are kept in an array on the stack of the call that started the
// check, and the path they spell is written only for the message of a failure.
// The path used to be written at every field and every list element of a value
// that passed, which is where most of the check's allocations were. The steps
// are not linked to each other, because a step that points at the one before it
// is a cycle the compiler's escape analysis gives up on and moves to the heap.
type fieldTrail struct {
	steps  [maxValidationDepth + 2]trailStep
	length int

	// ownPlans holds the required-field plans of types the cache had no room for,
	// so that such a type is worked out once in a check and not for each value of
	// it. It stays nil, and costs nothing, while every type is one the cache keeps.
	ownPlans map[reflect.Type]*requiredPlan
}

// requiredFieldsOf is the required-field plan of a struct type: the cache's when
// it keeps the type, and otherwise one worked out the first time the check meets
// the type and used for every later value of it.
func (trail *fieldTrail) requiredFieldsOf(structType reflect.Type) *requiredPlan {
	if own, found := trail.ownPlans[structType]; found {
		return own
	}
	plan := typePlanFor(structType)
	if plan.retained {
		return plan.requiredFields()
	}
	if trail.ownPlans == nil {
		trail.ownPlans = map[reflect.Type]*requiredPlan{}
	}
	own := plan.requiredFields()
	trail.ownPlans[structType] = own
	return own
}

// push adds a step. The check does not go deeper than maxValidationDepth, so
// there is always room.
func (trail *fieldTrail) push(step trailStep) {
	trail.steps[trail.length] = step
	trail.length++
}

func (trail *fieldTrail) pop() {
	trail.length--
}

// String writes the path: field names joined by dots, with a list index or a map
// key in brackets after what it belongs to.
func (trail *fieldTrail) String() string {
	var path strings.Builder
	for _, step := range trail.steps[:trail.length] {
		switch step.kind {
		case trailField:
			if path.Len() > 0 {
				path.WriteByte('.')
			}
			path.WriteString(step.name)
		case trailIndex:
			path.WriteByte('[')
			path.WriteString(strconv.Itoa(step.index))
			path.WriteByte(']')
		case trailKey:
			fmt.Fprintf(&path, "[%v]", step.key.Interface())
		}
	}
	return path.String()
}

// validateRequiredFields checks one value. trail holds the steps that lead to
// it from the top, and is empty at the top.
func validateRequiredFields(value reflect.Value, trail *fieldTrail, depth int) error {
	if depth > maxValidationDepth {
		return nil
	}

	for value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface {
		if value.IsNil() {
			if trail.length == 0 {
				return types.Classified(types.KindMalformedOutput, fmt.Errorf("data cannot be nil pointer"))
			}
			return nil // a nil pointer field is checked by its own zero test
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Struct:
		// Which fields there are, what they are called and which of them have
		// to be filled depends on the struct type alone, so it is read from the
		// type's plan and not worked out from its tags for each value.
		fields := trail.requiredFieldsOf(value.Type()).fields
		for position := range fields {
			field := &fields[position]
			// A field of the struct itself is read directly, which is every
			// field of a type that embeds nothing.
			var fieldValue reflect.Value
			reached := true
			if len(field.index) == 1 {
				fieldValue = value.Field(field.index[0])
			} else {
				fieldValue, reached = field.in(value)
			}
			empty := !reached || fieldValue.IsZero()
			trail.push(trailStep{kind: trailField, name: field.path})

			if field.required && empty {
				return types.Classified(types.KindMalformedOutput, fmt.Errorf("required field %s is empty; %s", trail.String(), optionalFieldRemedy))
			}

			// Recurse into what was populated. An optional field that is
			// present still has to be internally consistent.
			if !empty {
				if err := validateRequiredFields(fieldValue, trail, depth+1); err != nil {
					return err
				}
			}
			trail.pop()
		}
		return nil

	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			element := value.Index(i)
			if element.Kind() == reflect.Struct || element.Kind() == reflect.Ptr || element.Kind() == reflect.Interface {
				trail.push(trailStep{kind: trailIndex, index: i})
				if err := validateRequiredFields(element, trail, depth+1); err != nil {
					return err
				}
				trail.pop()
			}
		}
		return nil

	case reflect.Map:
		// In the order of the keys, so that a value with more than one entry
		// that fails reports the same one every time. The runtime's iteration
		// order is not the same from one run to the next.
		for _, key := range sortedMapKeys(value) {
			element := value.MapIndex(key)
			if element.Kind() == reflect.Struct || element.Kind() == reflect.Ptr || element.Kind() == reflect.Interface {
				trail.push(trailStep{kind: trailKey, key: key})
				if err := validateRequiredFields(element, trail, depth+1); err != nil {
					return err
				}
				trail.pop()
			}
		}
		return nil

	default:
		return nil
	}
}

// sortedMapKeys is the keys of a map value in a fixed order: by the text they
// are written as in a path (the order of the strings for string keys).
func sortedMapKeys(mapValue reflect.Value) []reflect.Value {
	keys := mapValue.MapKeys()
	if len(keys) < 2 {
		return keys
	}
	type writtenKey struct {
		text string
		key  reflect.Value
	}
	written := make([]writtenKey, len(keys))
	for i, key := range keys {
		written[i] = writtenKey{text: fmt.Sprint(key.Interface()), key: key}
	}
	sort.SliceStable(written, func(a, b int) bool { return written[a].text < written[b].text })
	for i := range written {
		keys[i] = written[i].key
	}
	return keys
}

func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// applyDefaults merges the caller's options over the operation defaults.
//
// It names each field (mergeNonZeroOpOptions), and a test fills every field of
// the struct by reflection and checks that each one comes through, so a field
// added to the struct and not named there fails the test. The first hand-written
// version fell behind the struct: it copied eight of the thirteen fields, so
// MaxOutputTokens, CacheIdentity, ResponseFormat, JSONSchema, and SchemaName were
// set by a caller and silently thrown away on the way to the provider. That is
// worse than a missing feature -- it made ST-003's ceiling and P-009's cache
// identity unreachable from every legacy entrypoint while both looked
// implemented and both had passing tests, because the tests drove CallLLM
// directly. See A-014. What replaced it walked the struct through reflection,
// which was complete and cost one allocation and a pass over every field for each
// option passed.
//
// The merge rule is that a non-zero field wins. A caller cannot use this to
// set a field back to its zero value; that is what A-005's Opt[T] is for, and
// it was already true of the hand-written version.
func applyDefaults(opts ...types.OpOptions) types.OpOptions {
	result := types.OpOptions{}

	for i := range opts {
		mergeNonZeroOpOptions(&result, &opts[i])
	}

	// Defaults last, and only where the caller chose nothing. Zero means unset
	// for both of these since A-005, which is what makes this safe to test
	// after the merge rather than before it.
	if result.Mode == types.ModeUnset {
		result.Mode = types.TransformMode
	}
	if result.Intelligence == types.TierUnset {
		result.Intelligence = types.Smart
	}

	return result
}

// mergeNonZeroOpOptions copies each field of src that is not its zero value into
// dst. Every field of types.OpOptions is named here; applyDefaults' test fails
// when one is not.
func mergeNonZeroOpOptions(dst, src *types.OpOptions) {
	if src.Steering != "" {
		dst.Steering = src.Steering
	}
	if src.Threshold != 0 {
		dst.Threshold = src.Threshold
	}
	if src.Mode != 0 {
		dst.Mode = src.Mode
	}
	if src.ExactFields {
		dst.ExactFields = true
	}
	if src.CompleteFields {
		dst.CompleteFields = true
	}
	if src.Intelligence != 0 {
		dst.Intelligence = src.Intelligence
	}
	if src.Context != nil {
		dst.Context = src.Context
	}
	if src.RequestID != "" {
		dst.RequestID = src.RequestID
	}
	if src.CorrelationID != "" {
		dst.CorrelationID = src.CorrelationID
	}
	if src.JSONSchema != nil {
		dst.JSONSchema = src.JSONSchema
	}
	if src.SchemaName != "" {
		dst.SchemaName = src.SchemaName
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Effort != "" {
		dst.Effort = src.Effort
	}
	if src.WebSearch {
		dst.WebSearch = true
	}
	if src.SchemaID != "" {
		dst.SchemaID = src.SchemaID
	}
	if src.ResponseFormat != "" {
		dst.ResponseFormat = src.ResponseFormat
	}
	if src.CacheIdentity != "" {
		dst.CacheIdentity = src.CacheIdentity
	}
	if src.MaxOutputTokens != 0 {
		dst.MaxOutputTokens = src.MaxOutputTokens
	}
	if src.ParentResultIDs != nil {
		dst.ParentResultIDs = src.ParentResultIDs
	}
}

package ops

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The type support matrix.
//
// A Go type is this library's whole contract: it becomes the schema the model
// is shown and the shape the answer is decoded into. Some types cannot play
// that role, and the question is when the caller finds out.
//
// Before this, they found out from the answer -- an `any` field produced a
// schema saying "interface {}", the model guessed, and the decode either failed
// or succeeded into something meaningless. That is a provider call, a wait, and
// a bill, spent to discover something reflection knows for free.
//
// Four levels, and the line between them is what a caller can do about it:
//
//   - Full: the schema is exact and the decode is exact.
//   - Restricted: it works with a documented limit -- a map has no field names
//     to describe, recursion is cut at a depth, a custom marshaler's wire form
//     is not derivable from its Go shape.
//   - Opaque: the value is carried, not described. Nothing can say anything
//     about its fields, because it has none the schema can see.
//   - Rejected: no schema can be produced, so the call cannot be meaningful.
//     This fails preflight.

// TypeSupport is how well a Go type can serve as an operation's contract.
type TypeSupport int

const (
	SupportFull TypeSupport = iota
	SupportRestricted
	SupportOpaque
	SupportRejected
)

func (s TypeSupport) String() string {
	switch s {
	case SupportFull:
		return "full"
	case SupportRestricted:
		return "restricted"
	case SupportOpaque:
		return "opaque"
	case SupportRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// TypeReport is what the matrix says about one type.
type TypeReport struct {
	Support TypeSupport

	// Reason explains the classification in terms of the caller's own type, so
	// it can be acted on without reading this file.
	Reason string

	// Path names where in the type the problem is, empty for the top level.
	Path string
}

// ClassifyType walks a type and reports the worst thing it finds.
//
// The worst, not the first: a struct with one rejected field is rejected
// whatever else it holds, and telling a caller about a restricted map while a
// channel sits two fields down wastes the trip.
//
// What it says about a type is worked out once for each type and read from
// there after, since a type does not change between calls.
func ClassifyType(targetType reflect.Type) TypeReport {
	return typePlanFor(targetType).classification()
}

func classifyType(targetType reflect.Type, seen map[reflect.Type]bool, path []string, depth int) TypeReport {
	if targetType == nil {
		return TypeReport{Support: SupportRejected, Reason: "the type is not concrete", Path: joinPath(path)}
	}

	if depth > maxSchemaDepth {
		return TypeReport{
			Support: SupportRestricted,
			Reason:  fmt.Sprintf("nested deeper than %d levels, so the schema stops describing it there", maxSchemaDepth),
			Path:    joinPath(path),
		}
	}

	// A type that says how it serialises is taken at its word, but its wire
	// form cannot be derived from its Go shape -- so the schema describes
	// something the decode may not match.
	//
	// time.Time is exempt, and so is a pointer to it: both are written as an
	// RFC 3339 string, which the schema names.
	if !isTimeType(targetType) && implementsJSONMarshaler(targetType) {
		// A type that writes itself and does not read itself is still read field
		// by field, and a field with the option format makes that fail. Only the
		// fields it declares are looked at, as nothing else about them is.
		if report, refused := formatOptionInOwnMembers(targetType, path); refused {
			return report
		}
		return TypeReport{
			Support: SupportRestricted,
			Reason:  fmt.Sprintf("%s has a custom JSON representation, which the generated schema cannot see", targetType),
			Path:    joinPath(path),
		}
	}

	switch targetType.Kind() {
	case reflect.Ptr:
		return classifyType(targetType.Elem(), seen, path, depth)

	case reflect.Struct:
		if targetType == reflect.TypeOf(time.Time{}) {
			return TypeReport{Support: SupportFull}
		}
		if seen[targetType] {
			return TypeReport{
				Support: SupportRestricted,
				Reason:  fmt.Sprintf("%s refers to itself, so the schema names it rather than expanding it", targetType),
				Path:    joinPath(path),
			}
		}
		seen[targetType] = true
		defer delete(seen, targetType)

		// Neither the schema nor the answer can describe the fields of a type
		// whose embedded structs lead back to itself: there is no end to the
		// fields it promotes. It is refused, where a recursion through a named
		// field is only restricted, because there the schema can stop and name
		// the type, and here there is nothing to name.
		if embeddingCycle(targetType) {
			return TypeReport{
				Support: SupportRejected,
				Reason:  fmt.Sprintf("%s embeds structs that lead back to one of themselves, so the fields it promotes cannot be listed", targetType),
				Path:    joinPath(path),
			}
		}

		// What is classified is what the schema declares and the decoder reads:
		// the members encoding/json finds in the struct. They include the
		// exported fields of an embedded struct however its type is spelt, and
		// leave out a name that is hidden or ambiguous. Classifying the struct's
		// own exported fields, with one field for each exported embedded struct,
		// skipped what an unexported embedded struct promotes and reported on
		// names the decoder never reads.
		worst := TypeReport{Support: SupportFull}
		members := jsonMembers(targetType)
		for _, member := range members {
			var report TypeReport
			if member.unexportedPointer != nil {
				report = unexportedEmbeddedPointerReport(targetType, member.unexportedPointer, path)
			} else if memberPath := append(append(slices.Clone(path), member.route...), member.name); hasFormatOption(member) {
				report = formatOptionReport(targetType, memberPath)
			} else {
				report = classifyType(member.field.Type, seen, memberPath, depth+1)
			}
			if report.Support > worst.Support {
				worst = report
			}
		}

		if len(members) == 0 && worst.Support == SupportFull {
			return TypeReport{
				Support: SupportOpaque,
				Reason:  fmt.Sprintf("%s has no exported fields that encoding/json reads, so there is nothing for the schema to describe", targetType),
				Path:    joinPath(path),
			}
		}
		return worst

	case reflect.Slice, reflect.Array:
		return classifyType(targetType.Elem(), seen, append(path, "[]"), depth+1)

	case reflect.Map:
		if targetType.Key().Kind() != reflect.String {
			return TypeReport{
				Support: SupportRejected,
				Reason: fmt.Sprintf("a map keyed by %s cannot be JSON: object keys are strings",
					targetType.Key()),
				Path: joinPath(path),
			}
		}
		element := classifyType(targetType.Elem(), seen, append(path, "{}"), depth+1)
		if element.Support > SupportRestricted {
			return element
		}
		return TypeReport{
			Support: SupportRestricted,
			Reason:  "a map has no field names, so the schema describes its shape but not its keys",
			Path:    joinPath(path),
		}

	case reflect.Interface:
		if targetType.NumMethod() == 0 {
			return TypeReport{
				Support: SupportRejected,
				Reason:  "`any` gives the model nothing to produce and the decoder nothing to check",
				Path:    joinPath(path),
			}
		}
		return TypeReport{
			Support: SupportRejected,
			Reason: fmt.Sprintf("the interface %s has no single concrete shape to describe or decode into",
				targetType),
			Path: joinPath(path),
		}

	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return TypeReport{
			Support: SupportRejected,
			Reason:  fmt.Sprintf("a %s cannot be represented as JSON", targetType.Kind()),
			Path:    joinPath(path),
		}

	case reflect.Complex64, reflect.Complex128:
		return TypeReport{
			Support: SupportRejected,
			Reason:  "complex numbers have no JSON representation",
			Path:    joinPath(path),
		}

	// S-012. A Go type does not say what a field is *for* -- float32 is fine
	// for a temperature and wrong for a price, and reflection cannot tell
	// those apart. S-009 already recorded the concrete failure: Go marshals a
	// float32 as the shortest decimal that round-trips as a float32, so
	// 1284.57 stored in one comes back as 1284.5699462890625, re-encodes as
	// "1284.57", and CheckNumericFidelity's round trip sees nothing wrong --
	// the loss is real and that check cannot see it. Of the three ways to
	// close that gap (a struct tag, a registered Money type, or flagging
	// every float32), this is the cheapest: every float32 field is
	// restricted, whether it holds a price or a temperature. It is noisier
	// than a tag would be, and it is the rule this library documents rather
	// than papers over.
	case reflect.Float32:
		return TypeReport{
			Support: SupportRestricted,
			Reason: "float32 loses precision a round trip cannot detect -- 1284.57 stored as " +
				"1284.5699462890625 re-encodes clean, so a money-shaped field here is silently " +
				"wrong (S-009). Every float32 is flagged, not only the ones that look like money, " +
				"because a Go type does not say what a field is for; use float64 or a registered " +
				"decimal/Money type if this field is a currency amount.",
			Path: joinPath(path),
		}

	default:
		return TypeReport{Support: SupportFull}
	}
}

// unexportedEmbeddedPointerReport is the refusal of a struct that embeds a
// pointer to a struct type that is not exported and promotes fields from it:
// encoding/json writes through such a pointer, and fails with "cannot set
// embedded pointer to unexported struct" when it has to allocate it to read an
// answer. It is a configuration refusal made before a call, as a type that
// embeds itself is.
func unexportedEmbeddedPointerReport(structType reflect.Type, pointer *jsonUnexportedPointer, path []string) TypeReport {
	return TypeReport{
		Support: SupportRejected,
		Reason: fmt.Sprintf("%s embeds a pointer to %s, a struct type that is not exported, which encoding/json cannot allocate when it decodes, so the fields it promotes cannot be read into the type",
			structType, pointer.target),
		Path: joinPath(append(slices.Clone(path), pointer.route...)),
	}
}

// hasFormatOption reports whether the json tag of a member has the option
// format, with a value encoding/json reads as one.
func hasFormatOption(member jsonMember) bool {
	return parseJSONTagOptions(member.field.Tag.Get("json")).format
}

// formatOptionReport is the refusal of a struct that has a member with the option
// format in its json tag. encoding/json does not support the option: the first
// time it writes or reads the struct it fails, whatever the type of the member
// and the option's value, and it fails the same way each time after. An answer
// that could never be read is a configuration refusal made before a call, as a
// type that embeds itself is.
func formatOptionReport(structType reflect.Type, memberPath []string) TypeReport {
	return TypeReport{
		Support: SupportRejected,
		Reason: fmt.Sprintf("%s has a field whose json tag has the option format, which encoding/json does not support, so it fails to read or write any value of the type and no answer could be read into it",
			structType),
		Path: joinPath(memberPath),
	}
}

// formatOptionInOwnMembers looks for a member with the option format among the
// members of the struct a type is or points to, when the type does not read
// itself: one that does is never read field by field.
func formatOptionInOwnMembers(targetType reflect.Type, path []string) (TypeReport, bool) {
	structType := targetType
	for structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}
	if structType.Kind() != reflect.Struct || decodesItself(structType) {
		return TypeReport{}, false
	}
	for _, member := range jsonMembers(structType) {
		if member.unexportedPointer == nil && hasFormatOption(member) {
			return formatOptionReport(structType, append(append(slices.Clone(path), member.route...), member.name)), true
		}
	}
	return TypeReport{}, false
}

// embeddedStruct is the struct a field embeds in the way encoding/json promotes
// the fields of, or nil: an anonymous field of a struct type or a pointer, to
// any depth, to one, whose json tag gives it no name and does not leave it out.
// It is what a walk over a type's promoted fields follows.
func embeddedStruct(field reflect.StructField) reflect.Type {
	if !field.Anonymous {
		return nil
	}
	if name, omitted := jsonTagName(field.Tag.Get("json")); omitted || name != "" {
		return nil
	}
	embedded := field.Type
	for embedded.Kind() == reflect.Ptr {
		embedded = embedded.Elem()
	}
	if embedded.Kind() != reflect.Struct {
		return nil
	}
	return embedded
}

// embeddingCycle reports whether the structs a type embeds, and the structs
// those embed, lead back to a struct that is already being followed. Only
// embedding is followed: a field with a name of its own that refers back to its
// struct is a recursive type, which is not this.
func embeddingCycle(structType reflect.Type) bool {
	for structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}
	if structType.Kind() != reflect.Struct {
		return false
	}
	// A struct that embeds nothing, which is nearly every one, has no cycle to
	// look for and is answered without a map.
	embedsAnything := false
	for i := 0; i < structType.NumField(); i++ {
		if embeddedStruct(structType.Field(i)) != nil {
			embedsAnything = true
			break
		}
	}
	if !embedsAnything {
		return false
	}
	return embeddingCycleFrom(structType, map[reflect.Type]embeddingState{})
}

type embeddingState uint8

const (
	// embeddingFollowing is a struct on the path being followed. Meeting it
	// again is a cycle.
	embeddingFollowing embeddingState = iota + 1
	// embeddingCleared is a struct whose embedded structs were all followed and
	// led to no cycle, so that one embedded by several others is followed once.
	embeddingCleared
)

func embeddingCycleFrom(structType reflect.Type, states map[reflect.Type]embeddingState) bool {
	switch states[structType] {
	case embeddingFollowing:
		return true
	case embeddingCleared:
		return false
	}
	states[structType] = embeddingFollowing
	for i := 0; i < structType.NumField(); i++ {
		if embedded := embeddedStruct(structType.Field(i)); embedded != nil && embeddingCycleFrom(embedded, states) {
			return true
		}
	}
	states[structType] = embeddingCleared
	return false
}

// PreflightType refuses a type no call could satisfy, before the call is made.
func PreflightType(targetType reflect.Type) error {
	return typePlanFor(targetType).preflight()
}

// preflightFailure is the error that refuses a type, made from what the matrix
// reports for it, or nil when the type is not refused. Every call gets an error
// of its own: a caller holds the one it is handed and may look inside it.
func preflightFailure(report TypeReport) error {
	if report.Support != SupportRejected {
		return nil
	}

	where := "the target type"
	if report.Path != "" {
		where = report.Path
	}

	return &types.OperationError{
		Kind: types.KindConfiguration,
		Message: fmt.Sprintf("%s cannot be used as an operation contract: %s. No provider call was made",
			where, report.Reason),
	}
}

// isTimeType reports whether a type is time.Time or a pointer, to any depth, to
// it.
func isTimeType(targetType reflect.Type) bool {
	for targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	return targetType == reflect.TypeOf(time.Time{})
}

// implementsJSONMarshaler reports whether a type says how it is written as
// JSON: by MarshalJSON, or by MarshalText, which encoding/json uses for a
// value the same way and which is how a UUID or an IP address comes to be a
// string in JSON and an array of bytes in Go. Only MarshalJSON was looked for.
func implementsJSONMarshaler(targetType reflect.Type) bool {
	for _, marshaler := range []reflect.Type{
		reflect.TypeOf((*json.Marshaler)(nil)).Elem(),
		reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem(),
	} {
		if targetType.Implements(marshaler) {
			return true
		}
		if targetType.Kind() != reflect.Ptr && reflect.PointerTo(targetType).Implements(marshaler) {
			return true
		}
	}
	return false
}

func joinPath(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return strings.Join(path, ".")
}

package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
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
func ClassifyType(targetType reflect.Type) TypeReport {
	return classifyType(targetType, map[reflect.Type]bool{}, nil, 0)
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
	if targetType != reflect.TypeOf(time.Time{}) && implementsJSONMarshaler(targetType) {
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

		worst := TypeReport{Support: SupportFull}
		exported := 0
		for i := 0; i < targetType.NumField(); i++ {
			field := targetType.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := field.Tag.Get("json")
			if strings.Split(tag, ",")[0] == "-" && !strings.Contains(tag, ",") {
				continue
			}
			exported++

			name := field.Name
			if parts := strings.Split(tag, ","); parts[0] != "" {
				name = parts[0]
			}

			report := classifyType(field.Type, seen, append(path, name), depth+1)
			if report.Support > worst.Support {
				worst = report
			}
		}

		if exported == 0 && worst.Support == SupportFull {
			return TypeReport{
				Support: SupportOpaque,
				Reason:  fmt.Sprintf("%s has no exported fields, so there is nothing for the schema to describe", targetType),
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

	default:
		return TypeReport{Support: SupportFull}
	}
}

// PreflightType refuses a type no call could satisfy, before the call is made.
func PreflightType(targetType reflect.Type) error {
	report := ClassifyType(targetType)
	if report.Support != SupportRejected {
		return nil
	}

	where := "the target type"
	if report.Path != "" {
		where = report.Path
	}

	return &types.OperationError{
		Kind: types.KindConfiguration,
		Message: fmt.Sprintf("%s cannot be used as an operation contract: %s. No provider call was made.",
			where, report.Reason),
	}
}

func implementsJSONMarshaler(targetType reflect.Type) bool {
	marshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	if targetType.Implements(marshaler) {
		return true
	}
	if targetType.Kind() != reflect.Ptr {
		return reflect.PointerTo(targetType).Implements(marshaler)
	}
	return false
}

func joinPath(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return strings.Join(path, ".")
}

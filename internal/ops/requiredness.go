package ops

import (
	"reflect"
	"strings"
)

// Requiredness says whether a field has to be present.
//
// It exists because the answer used to be inferred from `omitempty`, which is a
// serialization directive: it tells encoding/json to leave a zero value out of
// the output. Reading it as "this field is optional" conflates two different
// statements, and a caller who wrote `json:"middle_name,omitempty"` to keep
// their JSON tidy had silently made the field optional to Strict mode -- while
// a caller who wanted an optional field and did not know the convention got a
// required one.
//
// The tag is explicit now, and the old inference stays as the fallback so no
// existing type changes behaviour.
type Requiredness int

const (
	// FieldRequired means the field must be populated.
	FieldRequired Requiredness = iota

	// FieldOptional means it may be absent.
	FieldOptional
)

// The tag this library reads for its own purposes. `schemaflux:"required"` and
// `schemaflux:"optional"` are the two values that matter; anything else is
// ignored so the tag can carry other options later.
const schemafluxTag = "schemaflux"

// FieldRequiredness resolves whether a struct field is required, in order:
//
//  1. an explicit `schemaflux:"required"` or `schemaflux:"optional"` tag;
//  2. the field's type -- a pointer can be nil, which is how Go spells "may be
//     absent", so a pointer field is optional unless the tag says otherwise;
//  3. the presence of `omitempty`, which is the old inference, kept so types
//     written before the tag existed keep their current behaviour.
func FieldRequiredness(field reflect.StructField) Requiredness {
	switch strings.ToLower(strings.TrimSpace(field.Tag.Get(schemafluxTag))) {
	case "required":
		return FieldRequired
	case "optional":
		return FieldOptional
	}

	if field.Type.Kind() == reflect.Ptr {
		return FieldOptional
	}

	if hasJSONOption(field.Tag.Get("json"), "omitempty") {
		return FieldOptional
	}

	return FieldRequired
}

// IsRequired is the boolean form, for call sites that read better that way.
func IsRequired(field reflect.StructField) bool {
	return FieldRequiredness(field) == FieldRequired
}

package ops

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Schema evolution.
//
// A Go type is a contract with a model, a cache, and every result already
// stored. Changing it is a release decision, and the decision needs a name:
// adding an optional field is not the same act as changing a field's type, and
// today both are "someone edited a struct".
//
// The rules follow what actually breaks:
//
//   - Adding an *optional* field is decode-compatible. Old results still decode
//     into the new type. It is not free -- the prompt changes and the cache
//     identity changes, so every cached answer misses -- but nothing breaks.
//   - Adding a *required* field is a new contract: old results decode into the
//     new type with that field empty, and Strict mode rejects them.
//   - Changing a field's type, or making an optional field required, is a new
//     contract for the same reason.
//   - Removing a field, or renaming its json tag, is breaking: an old result
//     carries a field the new type does not accept, which S-008's exact
//     decoding refuses rather than ignores.
//
// What this does not do is stop anyone. It names the change so a release note
// can, which is the whole of what a compatibility policy is.

// SchemaChange classifies one schema against another.
type SchemaChange int

const (
	// SchemaUnchanged means the two produce the same contract.
	SchemaUnchanged SchemaChange = iota

	// SchemaCompatible means old data still decodes and old code still works.
	SchemaCompatible

	// SchemaNewContract means old data may no longer satisfy the new type.
	SchemaNewContract

	// SchemaBreaking means old data actively conflicts with the new type.
	SchemaBreaking
)

func (c SchemaChange) String() string {
	switch c {
	case SchemaUnchanged:
		return "unchanged"
	case SchemaCompatible:
		return "compatible"
	case SchemaNewContract:
		return "new contract version"
	case SchemaBreaking:
		return "breaking"
	default:
		return "unknown"
	}
}

// SchemaDiff is what changed and how much it matters.
type SchemaDiff struct {
	Change SchemaChange

	// Added, Removed, Retyped, and Tightened name the fields by their JSON
	// names, which is the name the contract is written in.
	Added     []string
	Removed   []string
	Retyped   []string
	Tightened []string // optional became required

	// Loosened is required becoming optional: compatible, and worth naming
	// because it changes what Strict mode will accept.
	Loosened []string
}

// Summary renders the diff for a release note, which is where it is for.
func (d SchemaDiff) Summary() string {
	if d.Change == SchemaUnchanged {
		return "unchanged"
	}

	var parts []string
	for label, fields := range map[string][]string{
		"added":     d.Added,
		"removed":   d.Removed,
		"retyped":   d.Retyped,
		"tightened": d.Tightened,
		"loosened":  d.Loosened,
	} {
		if len(fields) > 0 {
			parts = append(parts, fmt.Sprintf("%s %s", label, strings.Join(fields, ", ")))
		}
	}
	sort.Strings(parts)

	return fmt.Sprintf("%s: %s", d.Change, strings.Join(parts, "; "))
}

// CompareSchemas classifies the change from one type to another.
func CompareSchemas(before, after reflect.Type) SchemaDiff {
	previous := contractFields(before)
	current := contractFields(after)

	diff := SchemaDiff{}

	for _, name := range sortedKeys(current) {
		old, existed := previous[name]
		if !existed {
			diff.Added = append(diff.Added, name)
			continue
		}
		if old.kind != current[name].kind {
			diff.Retyped = append(diff.Retyped, name)
			continue
		}
		switch {
		case !old.required && current[name].required:
			diff.Tightened = append(diff.Tightened, name)
		case old.required && !current[name].required:
			diff.Loosened = append(diff.Loosened, name)
		}
	}

	for _, name := range sortedKeys(previous) {
		if _, present := current[name]; !present {
			diff.Removed = append(diff.Removed, name)
		}
	}

	diff.Change = classifyDiff(diff, current)
	return diff
}

func classifyDiff(diff SchemaDiff, current map[string]contractField) SchemaChange {
	switch {
	case len(diff.Removed) > 0:
		// An old result carries a field the new type does not accept, which
		// exact decoding refuses rather than ignores.
		return SchemaBreaking

	case len(diff.Retyped) > 0, len(diff.Tightened) > 0:
		return SchemaNewContract

	case len(diff.Added) > 0:
		// Required additions are a new contract; optional ones are not.
		for _, name := range diff.Added {
			if current[name].required {
				return SchemaNewContract
			}
		}
		return SchemaCompatible

	case len(diff.Loosened) > 0:
		return SchemaCompatible

	default:
		return SchemaUnchanged
	}
}

// contractField is what the contract says about one field: its JSON type and
// whether it must be present. Deliberately not its Go type -- two Go types that
// serialise the same way are the same contract, which is the same rule the
// schema hash follows.
type contractField struct {
	kind     string
	required bool
}

func contractFields(targetType reflect.Type) map[string]contractField {
	fields := map[string]contractField{}
	if targetType == nil {
		return fields
	}
	for targetType.Kind() == reflect.Ptr || targetType.Kind() == reflect.Slice || targetType.Kind() == reflect.Array {
		targetType = targetType.Elem()
	}
	if targetType.Kind() != reflect.Struct {
		return fields
	}

	for i := 0; i < targetType.NumField(); i++ {
		field := targetType.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] == "-" && len(parts) == 1 {
			continue
		}

		name := field.Name
		if parts[0] != "" {
			name = parts[0]
		}

		fields[name] = contractField{
			kind:     jsonKindOf(field.Type),
			required: IsRequired(field),
		}
	}

	return fields
}

// jsonKindOf names the JSON type a Go type serialises to. Two Go types with the
// same JSON kind are the same contract as far as a stored result is concerned.
func jsonKindOf(targetType reflect.Type) string {
	for targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}

	switch targetType.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array of " + jsonKindOf(targetType.Elem())
	case reflect.Map:
		return "object"
	case reflect.Struct:
		// A nested struct's own fields are a separate contract; at this level
		// it is an object, and a change inside it is caught by comparing that
		// type in its own right.
		return "object"
	default:
		return targetType.Kind().String()
	}
}

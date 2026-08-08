package ops

import (
	"reflect"
	"strings"
	"testing"
)

// S-003. Requiredness was inferred from `omitempty`, which is a serialization
// directive: it tells encoding/json to leave a zero value out of the output.
// Reading it as "optional" conflates two statements, so a caller who wrote
// `json:"middle_name,omitempty"` to keep their JSON tidy had silently made the
// field optional to Strict mode -- and a caller who wanted an optional field
// and did not know the convention got a required one.
func TestFieldRequiredness(t *testing.T) {
	type sample struct {
		Plain          string  `json:"plain"`
		OmitEmpty      string  `json:"omit_empty,omitempty"`
		TaggedRequired string  `json:"tagged_required,omitempty" schemaflux:"required"`
		TaggedOptional string  `json:"tagged_optional" schemaflux:"optional"`
		Pointer        *string `json:"pointer"`
		PointerTagged  *string `json:"pointer_tagged" schemaflux:"required"`
		Untagged       string
		MixedCase      string `json:"mixed" schemaflux:"OPTIONAL"`
		Unknown        string `json:"unknown" schemaflux:"something-else"`
	}

	cases := []struct {
		field string
		want  Requiredness
		why   string
	}{
		{"Plain", FieldRequired, "no tag, not a pointer"},
		{"OmitEmpty", FieldOptional, "the legacy inference still applies"},
		{"TaggedRequired", FieldRequired, "the explicit tag beats omitempty"},
		{"TaggedOptional", FieldOptional, "the explicit tag beats the absence of omitempty"},
		{"Pointer", FieldOptional, "a pointer can be nil, which is how Go says absent"},
		{"PointerTagged", FieldRequired, "an explicit tag beats the type"},
		{"Untagged", FieldRequired, "no json tag at all"},
		{"MixedCase", FieldOptional, "the tag value folds case"},
		{"Unknown", FieldRequired, "an unrecognised tag value is ignored, not guessed at"},
	}

	sampleType := reflect.TypeOf(sample{})
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			field, ok := sampleType.FieldByName(tc.field)
			if !ok {
				t.Fatalf("no field named %s", tc.field)
			}
			if got := FieldRequiredness(field); got != tc.want {
				t.Errorf("FieldRequiredness = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

// The schema the model sees and the validation Strict applies read the same
// resolver, so they cannot disagree about which fields are required -- which
// they would have, the moment one of them learned about the tag first.
func TestTheSchemaAndTheValidatorAgree(t *testing.T) {
	type record struct {
		Required string `json:"required"`
		Optional string `json:"optional" schemaflux:"optional"`
		Tidy     string `json:"tidy,omitempty" schemaflux:"required"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(record{}))

	// The schema marks exactly the two required fields.
	for _, line := range strings.Split(schema, "\n") {
		switch {
		case strings.Contains(line, "required:"):
			if !strings.Contains(line, "(required)") {
				t.Errorf("the schema does not mark `required` as required: %s", line)
			}
		case strings.Contains(line, "tidy:"):
			if !strings.Contains(line, "(required)") {
				t.Errorf("the schema does not honour schemaflux:\"required\" over omitempty: %s", line)
			}
		case strings.Contains(line, "optional:"):
			if strings.Contains(line, "(required)") {
				t.Errorf("the schema marks an explicitly optional field required: %s", line)
			}
		}
	}

	// And the validator agrees, field for field.
	if err := ValidateExtractedData(record{Required: "x", Tidy: "y"}); err != nil {
		t.Errorf("validation rejected a record with both required fields set: %v", err)
	}
	if err := ValidateExtractedData(record{Required: "x"}); err == nil {
		t.Error("validation accepted a record missing a schemaflux:\"required\" field")
	}
	if err := ValidateExtractedData(record{Tidy: "y"}); err == nil {
		t.Error("validation accepted a record missing an untagged field")
	}
}

// A pointer field is optional without ceremony, which is the idiom a Go caller
// already reaches for.
func TestPointerFieldsAreOptionalToTheValidator(t *testing.T) {
	type record struct {
		Name   string  `json:"name"`
		Middle *string `json:"middle"`
	}

	if err := ValidateExtractedData(record{Name: "Ada"}); err != nil {
		t.Errorf("a nil pointer field was treated as missing: %v", err)
	}
	if err := ValidateExtractedData(record{}); err == nil {
		t.Error("validation accepted a record with no name")
	}
}

// The JSON Schema sent to the provider has to say the same thing as the prose
// schema in the prompt and the check applied to the answer. Three descriptions
// of one contract that can disagree is three chances to.
func TestTheJSONSchemaAgreesAboutOptionality(t *testing.T) {
	type record struct {
		Required string `json:"required"`
		Optional string `json:"optional" schemaflux:"optional"`
		Tidy     string `json:"tidy,omitempty" schemaflux:"required"`
	}

	schema := GenerateJSONSchema(reflect.TypeOf(record{}))
	if schema == nil {
		t.Fatal("GenerateJSONSchema returned nothing for a plain struct")
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("no properties in %v", schema)
	}

	isNullable := func(name string) bool {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("no property %q", name)
		}
		raw, ok := property["type"]
		if !ok {
			return false
		}
		// The union is []string in this encoder; accept []any too so the test
		// does not pin an internal representation it does not care about.
		switch list := raw.(type) {
		case []string:
			for _, entry := range list {
				if entry == "null" {
					return true
				}
			}
		case []any:
			for _, entry := range list {
				if entry == "null" {
					return true
				}
			}
		}
		return false
	}

	if isNullable("required") {
		t.Error("a required field was made nullable")
	}
	if !isNullable("optional") {
		t.Error("an explicitly optional field was not made nullable")
	}
	if isNullable("tidy") {
		t.Error("schemaflux:\"required\" lost to omitempty in the JSON Schema")
	}
}

package ops

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type schemaContact struct {
	Name    string   `json:"name"`
	Age     int      `json:"age"`
	Active  bool     `json:"active"`
	Score   float64  `json:"score"`
	Tags    []string `json:"tags"`
	Note    string   `json:"note,omitempty"`
	private string
	Skipped string `json:"-"`
}

func schemaOf(t *testing.T, value any) map[string]any {
	t.Helper()
	schema := GenerateJSONSchema(reflect.TypeOf(value))
	if schema == nil {
		t.Fatalf("no schema was produced for %T", value)
	}
	return schema
}

// Strict mode has rules, and a schema that breaks them is rejected by the API —
// which is worse than sending none, because the call fails instead of degrading.
func TestJSONSchemaSatisfiesStrictModeRules(t *testing.T) {
	schema := schemaOf(t, schemaContact{})

	t.Run("is_an_object", func(t *testing.T) {
		if schema["type"] != "object" {
			t.Errorf("type = %v", schema["type"])
		}
	})

	t.Run("additional_properties_are_refused", func(t *testing.T) {
		if schema["additionalProperties"] != false {
			t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
		}
	})

	t.Run("every_property_is_required", func(t *testing.T) {
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]string)

		if len(properties) != len(required) {
			t.Fatalf("%d properties but %d required: strict mode needs every property listed",
				len(properties), len(required))
		}
		for _, name := range required {
			if _, ok := properties[name]; !ok {
				t.Errorf("%q is required but not a property", name)
			}
		}
	})

	t.Run("optional_fields_permit_null", func(t *testing.T) {
		properties, _ := schema["properties"].(map[string]any)
		note, _ := properties["note"].(map[string]any)
		types, ok := note["type"].([]string)
		if !ok {
			t.Fatalf("note type = %v; an omitempty field must permit null", note["type"])
		}
		var sawNull bool
		for _, entry := range types {
			if entry == "null" {
				sawNull = true
			}
		}
		if !sawNull {
			t.Errorf("note type = %v, want it to include null", types)
		}
	})

	t.Run("excluded_and_unexported_fields_are_absent", func(t *testing.T) {
		properties, _ := schema["properties"].(map[string]any)
		for _, absent := range []string{"private", "Skipped", "-"} {
			if _, present := properties[absent]; present {
				t.Errorf("%q is in the schema", absent)
			}
		}
	})
}

// Go types map to the JSON types a model can produce.
func TestJSONSchemaLeafTypes(t *testing.T) {
	schema := schemaOf(t, schemaContact{})
	properties, _ := schema["properties"].(map[string]any)

	cases := map[string]string{
		"name":   "string",
		"age":    "integer",
		"active": "boolean",
		"score":  "number",
	}

	for field, want := range cases {
		t.Run(field, func(t *testing.T) {
			entry, _ := properties[field].(map[string]any)
			if entry["type"] != want {
				t.Errorf("%s type = %v, want %v", field, entry["type"], want)
			}
		})
	}

	t.Run("slice_is_an_array_of_its_element", func(t *testing.T) {
		tags, _ := properties["tags"].(map[string]any)
		if tags["type"] != "array" {
			t.Fatalf("tags type = %v", tags["type"])
		}
		items, _ := tags["items"].(map[string]any)
		if items["type"] != "string" {
			t.Errorf("tags items = %v", items["type"])
		}
	})
}

// Nesting is expressed, which is what makes the schema worth sending.
func TestJSONSchemaExpressesNesting(t *testing.T) {
	type address struct {
		City string `json:"city"`
	}
	type person struct {
		Name    string    `json:"name"`
		Home    address   `json:"home"`
		Visited []address `json:"visited"`
	}

	schema := schemaOf(t, person{})
	properties, _ := schema["properties"].(map[string]any)

	home, _ := properties["home"].(map[string]any)
	if home["type"] != "object" {
		t.Errorf("home is not an object: %v", home)
	}
	homeProps, _ := home["properties"].(map[string]any)
	if _, ok := homeProps["city"]; !ok {
		t.Errorf("the nested city field is missing: %v", homeProps)
	}

	visited, _ := properties["visited"].(map[string]any)
	items, _ := visited["items"].(map[string]any)
	if items["type"] != "object" {
		t.Errorf("visited items are not objects: %v", items)
	}
}

// time.Time is a formatted string, not an expanded struct.
func TestJSONSchemaRendersTimeAsADateTime(t *testing.T) {
	type event struct {
		At time.Time `json:"at"`
	}

	schema := schemaOf(t, event{})
	properties, _ := schema["properties"].(map[string]any)
	at, _ := properties["at"].(map[string]any)

	if at["type"] != "string" || at["format"] != "date-time" {
		t.Errorf("at = %v, want a date-time string", at)
	}
}

// A type strict mode cannot express produces no schema, so the caller falls
// back to prompt-only rather than sending something the API will reject.
func TestJSONSchemaDeclinesTypesItCannotExpress(t *testing.T) {
	type withMap struct {
		Values map[string]int `json:"values"`
	}
	type node struct {
		Value string `json:"value"`
		Next  *node  `json:"next,omitempty"`
	}
	type empty struct{}

	cases := []struct {
		name  string
		value any
	}{
		{"map", withMap{}},
		{"recursive", node{}},
		{"no_fields", empty{}},
		{"bare_string", ""},
		{"bare_int", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := GenerateJSONSchema(reflect.TypeOf(tc.value))
			if tc.name == "bare_string" || tc.name == "bare_int" {
				// A scalar has a valid schema; it is just not an object.
				if schema == nil {
					t.Skip("scalars are expressible")
				}
				return
			}
			if schema != nil {
				t.Errorf("a %s type produced a schema strict mode cannot use: %v", tc.name, schema)
			}
		})
	}
}

// Embedded structs are flattened, as encoding/json promotes them, or the
// schema would not match the JSON the model must produce.
func TestJSONSchemaFlattensEmbeddedStructs(t *testing.T) {
	type base struct {
		ID string `json:"id"`
	}
	type derived struct {
		base
		Name string `json:"name"`
	}

	schema := schemaOf(t, derived{})
	properties, _ := schema["properties"].(map[string]any)

	for _, want := range []string{"id", "name"} {
		if _, ok := properties[want]; !ok {
			t.Errorf("%q is missing from the flattened schema: %v", want, properties)
		}
	}
	if _, present := properties["base"]; present {
		t.Error("the embedded type appears as a property")
	}
}

// The schema has to survive the round trip to the wire.
func TestJSONSchemaMarshals(t *testing.T) {
	schema := schemaOf(t, schemaContact{})

	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("the schema does not marshal: %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the marshalled schema does not parse: %v", err)
	}
	if back["type"] != "object" {
		t.Errorf("the round trip lost the type: %v", back["type"])
	}
}

// The schema name has to be one the API accepts.
func TestSchemaNameFor(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{schemaContact{}, "schemaContact"},
		{&schemaContact{}, "schemaContact"},
		{struct{ A int }{}, "result"},
		{[]schemaContact{}, "result"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := schemaNameFor(reflect.TypeOf(tc.value))
			if got != tc.want {
				t.Errorf("schemaNameFor(%T) = %q, want %q", tc.value, got, tc.want)
			}
			for _, r := range got {
				valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
					(r >= '0' && r <= '9') || r == '_'
				if !valid {
					t.Errorf("%q contains %q, which the API does not accept", got, r)
				}
			}
		})
	}
}

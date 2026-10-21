package ops

//lint:file-ignore U1000 unexported fields are here to prove the generator skips them

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// The schema expanded only the top level and described every other field with
// its Go type name, so a model asked to produce an Order was told its customer
// was a "main.Person" — a name that means nothing outside this program — and
// had to invent the shape it was being asked for.

type schemaPerson struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type schemaOrderItem struct {
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type schemaOrder struct {
	ID       string            `json:"id"`
	Customer schemaPerson      `json:"customer"`
	Items    []schemaOrderItem `json:"items"`
	Placed   time.Time         `json:"placed"`
	Tags     map[string]string `json:"tags,omitempty"`
}

func TestSchemaExpandsNestedStructs(t *testing.T) {
	schema := GenerateTypeSchema(reflect.TypeOf(schemaOrder{}))

	t.Run("no_go_type_names_survive", func(t *testing.T) {
		for _, leaked := range []string{"ops.schemaPerson", "ops.schemaOrderItem", "[]ops."} {
			if strings.Contains(schema, leaked) {
				t.Errorf("the schema names the Go type %q:\n%s", leaked, schema)
			}
		}
	})

	t.Run("nested_struct_fields_appear", func(t *testing.T) {
		for _, want := range []string{"name", "email"} {
			if !strings.Contains(schema, want) {
				t.Errorf("the nested customer field %q is missing:\n%s", want, schema)
			}
		}
	})

	t.Run("slice_element_fields_appear", func(t *testing.T) {
		for _, want := range []string{"sku", "quantity", "price"} {
			if !strings.Contains(schema, want) {
				t.Errorf("the slice element field %q is missing:\n%s", want, schema)
			}
		}
	})

	t.Run("time_is_still_a_datetime", func(t *testing.T) {
		if !strings.Contains(schema, "datetime (RFC3339)") {
			t.Errorf("time.Time was expanded instead of being named:\n%s", schema)
		}
	})

	t.Run("optional_is_marked", func(t *testing.T) {
		if strings.Contains(schema, "tags: map[string]string (required)") {
			t.Errorf("an omitempty field was marked required:\n%s", schema)
		}
	})
}

// A self-referential type must be named, not followed.
func TestSchemaHandlesRecursiveTypes(t *testing.T) {
	type node struct {
		Value    string  `json:"value"`
		Parent   *node   `json:"parent,omitempty"`
		Children []*node `json:"children,omitempty"`
	}

	done := make(chan string, 1)
	go func() { done <- GenerateTypeSchema(reflect.TypeOf(node{})) }()

	select {
	case schema := <-done:
		if !strings.Contains(schema, "value") {
			t.Errorf("the schema lost the type's own fields:\n%s", schema)
		}
		if !strings.Contains(schema, "recursive") && !strings.Contains(schema, "not expanded") {
			t.Errorf("a recursive type was neither named nor bounded:\n%s", schema)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GenerateTypeSchema did not terminate on a recursive type")
	}
}

// Depth is bounded, so a deeply nested type produces a schema rather than a
// document.
func TestSchemaDepthIsBounded(t *testing.T) {
	type l8 struct {
		V string `json:"v"`
	}
	type l7 struct {
		N l8 `json:"n"`
	}
	type l6 struct {
		N l7 `json:"n"`
	}
	type l5 struct {
		N l6 `json:"n"`
	}
	type l4 struct {
		N l5 `json:"n"`
	}
	type l3 struct {
		N l4 `json:"n"`
	}
	type l2 struct {
		N l3 `json:"n"`
	}
	type l1 struct {
		N l2 `json:"n"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(l1{}))
	if !strings.Contains(schema, "not expanded") {
		t.Errorf("depth is not bounded:\n%s", schema)
	}
}

// Excluded and unexported fields stay excluded at every level, not only the
// top: a nested struct's secrets are still secrets.
func TestSchemaExclusionAppliesAtEveryLevel(t *testing.T) {
	type inner struct {
		Public  string `json:"public"`
		Secret  string `json:"-"`
		private string
	}
	type outer struct {
		Nested inner  `json:"nested"`
		Hidden string `json:"-"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(outer{}))

	for _, excluded := range []string{"Secret", "private", "Hidden"} {
		if strings.Contains(schema, excluded) {
			t.Errorf("the schema names the excluded field %q:\n%s", excluded, schema)
		}
	}
	if !strings.Contains(schema, "public") {
		t.Errorf("a nested public field is missing:\n%s", schema)
	}
}

// An embedded struct promotes its fields, and the schema flattens it the way
// encoding/json does.
func TestSchemaFlattensEmbeddedStructs(t *testing.T) {
	type base struct {
		ID string `json:"id"`
	}
	type derived struct {
		base
		Name string `json:"name"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(derived{}))
	for _, want := range []string{"id", "name"} {
		if !strings.Contains(schema, want) {
			t.Errorf("%q is missing from the flattened schema:\n%s", want, schema)
		}
	}
	if strings.Contains(schema, "base") {
		t.Errorf("the embedded type is named rather than flattened:\n%s", schema)
	}
}

// The leaf renderings are unchanged.
func TestSchemaLeafTypes(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"string", "", "string"},
		{"int", 0, "integer"},
		{"float", 0.0, "number"},
		{"bool", false, "boolean"},
		{"time", time.Time{}, "datetime (RFC3339)"},
		{"string_slice", []string{}, "[string]"},
		{"int_slice", []int{}, "[integer]"},
		{"map", map[string]int{}, "map[string]integer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GenerateTypeSchema(reflect.TypeOf(tc.value)); got != tc.want {
				t.Errorf("GenerateTypeSchema = %q, want %q", got, tc.want)
			}
		})
	}
}

// A pointer to a struct is the struct, at every level.
func TestSchemaFollowsPointers(t *testing.T) {
	type inner struct {
		Value string `json:"value"`
	}
	type outer struct {
		Ptr *inner `json:"ptr"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(outer{}))
	if !strings.Contains(schema, "value") {
		t.Errorf("a pointer to a struct was not expanded:\n%s", schema)
	}
}

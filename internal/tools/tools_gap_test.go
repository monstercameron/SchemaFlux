package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// Execute refuses to run a registered tool that carries no Execute function,
// rather than panicking on a nil call -- a tool added to a registry by hand
// (as opposed to one of this package's own constructors) can be built that
// way by mistake.
func TestRegistryExecuteRefusesATooWithNoExecuteFunc(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&Tool{Name: "half-built"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := registry.Execute(context.Background(), "half-built", map[string]any{})
	if err == nil {
		t.Fatal("Execute must refuse a tool with no Execute function")
	}
}

// ToJSON serialises the registry's tools, in the same shape a caller would
// get from re-marshalling List() themselves -- so it has to actually be
// valid JSON describing what is registered.
func TestRegistryToJSON(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&Tool{
		Name:        "widget",
		Description: "does a thing",
		Category:    CategoryComputation,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	raw, err := registry.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("ToJSON did not produce valid JSON: %v", err)
	}
	if len(decoded) != 1 || decoded[0]["name"] != "widget" {
		t.Errorf("decoded = %+v, want one tool named widget", decoded)
	}
}

// SimpleObjectSchema builds the same shape ObjectSchema does, from a flat
// variadic list instead of a map literal -- a required flag marks a
// property required, and a missing/incomplete trailing group is dropped
// rather than read out of bounds.
func TestSimpleObjectSchema(t *testing.T) {
	schema := SimpleObjectSchema(
		"name", "string", "the name", true,
		"age", "number", "the age", false,
	)

	if schema["type"] != "object" {
		t.Fatalf("type = %v, want object", schema["type"])
	}
	props, ok := schema["properties"].(map[string]ParameterSchema)
	if !ok {
		t.Fatalf("properties has the wrong type: %T", schema["properties"])
	}
	if props["name"].Type != "string" || props["name"].Description != "the name" {
		t.Errorf("name property = %+v", props["name"])
	}
	if props["age"].Type != "number" {
		t.Errorf("age property = %+v", props["age"])
	}

	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "name" {
		t.Errorf("required = %v, want [name]", schema["required"])
	}
}

func TestSimpleObjectSchemaWithATrailingIncompleteGroupIsIgnored(t *testing.T) {
	schema := SimpleObjectSchema("name", "string", "the name", true, "orphan")
	props := schema["properties"].(map[string]ParameterSchema)
	if len(props) != 1 {
		t.Errorf("an incomplete trailing group should be dropped, got %+v", props)
	}
}

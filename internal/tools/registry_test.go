package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRegistryDefaultRegistry(t *testing.T) {
	tools := DefaultRegistry.List()
	if len(tools) == 0 {
		t.Error("Expected registered tools in default registry")
	}

	// Check some expected tools are registered
	expectedTools := []string{
		"calculate", "fetch", "read_file", "sqlite", "cache",
		"now", "csv", "tax", "zip",
	}

	for _, name := range expectedTools {
		if _, ok := DefaultRegistry.Get(name); !ok {
			t.Errorf("Expected tool %q to be registered", name)
		}
	}

	// "shell" is deliberately absent: it hands a model-authored string to a
	// shell, so it is registered only by EnableShell(policy).
	if _, ok := DefaultRegistry.Get("shell"); ok {
		t.Error("the shell tool must not be registered by default")
	}
}

func TestCreateToolHandler(t *testing.T) {
	handler := CreateToolHandler()

	// Test with calculate tool
	result, err := handler(context.Background(), "calculate", map[string]any{
		"expression": "2 + 2",
	})
	if err != nil {
		t.Fatalf("Handler error: %v", err)
	}

	var parsed Result
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	if !parsed.Success {
		t.Errorf("Expected success: %s", parsed.Error)
	}
}

func TestCreateToolHandlerError(t *testing.T) {
	handler := CreateToolHandler()

	_, err := handler(context.Background(), "nonexistent_tool", map[string]any{})
	if err == nil {
		t.Error("Expected error for nonexistent tool")
	}
}

func TestGetOpenAITools(t *testing.T) {
	tools := GetOpenAITools()
	if len(tools) == 0 {
		t.Error("Expected OpenAI tool specs")
	}

	// Validate structure
	for _, tool := range tools {
		if tool.Type != "function" {
			t.Errorf("Expected type 'function', got %q", tool.Type)
		}
		if tool.Function.Name == "" {
			t.Error("Expected function name")
		}
		if tool.Function.Description == "" {
			t.Error("Expected function description")
		}
		if tool.Function.Parameters == nil {
			t.Error("Expected function parameters")
		}
	}
}

func TestGetAnthropicTools(t *testing.T) {
	tools := GetAnthropicTools()
	if len(tools) == 0 {
		t.Error("Expected Anthropic tool specs")
	}

	// Validate structure
	for _, tool := range tools {
		if tool["name"] == nil || tool["name"] == "" {
			t.Error("Expected tool name")
		}
		if tool["description"] == nil {
			t.Error("Expected tool description")
		}
		if tool["input_schema"] == nil {
			t.Error("Expected input schema")
		}
	}
}

func TestToolCategories(t *testing.T) {
	categories := ToolCategories()
	if len(categories) == 0 {
		t.Error("Expected categories")
	}

	expectedCategories := []string{
		"computation", "http", "file", "database", "cache",
		"security", "time", "data", "finance", "messaging",
		"image", "audio", "template", "archive", "execution", "ai",
	}

	for _, cat := range expectedCategories {
		if _, ok := categories[cat]; !ok {
			t.Errorf("Expected category %q", cat)
		}
	}
}

func TestToolsByCategory(t *testing.T) {
	tools := ToolsByCategory("computation")
	if len(tools) == 0 {
		t.Error("Expected computation tools")
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	if !names["calculate"] {
		t.Error("Expected calculate tool in computation category")
	}
}

func TestToolsByCategoryInvalid(t *testing.T) {
	tools := ToolsByCategory("nonexistent")
	if len(tools) != 0 {
		t.Error("Expected no tools for invalid category")
	}
}

func TestCreateSubRegistry(t *testing.T) {
	registry := CreateSubRegistry("calculate", "fetch", "read_file")

	tools := registry.List()
	if len(tools) != 3 {
		t.Errorf("Expected 3 tools, got %d", len(tools))
	}

	// Verify tools are correct
	if _, ok := registry.Get("calculate"); !ok {
		t.Error("Expected calculate tool")
	}
	if _, ok := registry.Get("fetch"); !ok {
		t.Error("Expected fetch tool")
	}
	if _, ok := registry.Get("read_file"); !ok {
		t.Error("Expected read_file tool")
	}

	// Verify other tools are not included
	if _, ok := registry.Get("sqlite"); ok {
		t.Error("sqlite should not be in sub-registry")
	}
}

func TestCreateCategoryRegistry(t *testing.T) {
	registry := CreateCategoryRegistry("computation", "time")

	// Should have tools from both categories
	if _, ok := registry.Get("calculate"); !ok {
		t.Error("Expected calculate from computation")
	}
	if _, ok := registry.Get("now"); !ok {
		t.Error("Expected now from time")
	}

	// Should not have tools from other categories
	if _, ok := registry.Get("fetch"); ok {
		t.Error("fetch should not be in category registry")
	}
}

func TestToolSpecJSON(t *testing.T) {
	tools := GetOpenAITools()
	if len(tools) == 0 {
		t.Skip("No tools to test")
	}

	// Ensure specs are JSON serializable
	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("Failed to marshal tools: %v", err)
	}

	var parsed []ToolSpec
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal tools: %v", err)
	}

	if len(parsed) != len(tools) {
		t.Error("Tool count mismatch after serialization")
	}
}

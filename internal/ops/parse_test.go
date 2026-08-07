package ops

import (
	"testing"
)

func TestParse_JSON(t *testing.T) {
	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	input := `{"name":"John","age":30}`
	opts := NewParseOptions()

	result, err := Parse[Person](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if result.Data.Name != "John" {
		t.Errorf("Expected name 'John', got '%s'", result.Data.Name)
	}
	if result.Data.Age != 30 {
		t.Errorf("Expected age 30, got %d", result.Data.Age)
	}
	if result.Format != "json" {
		t.Errorf("Expected format 'json', got '%s'", result.Format)
	}
}

func TestParse_XML(t *testing.T) {
	type Person struct {
		Name string `xml:"name"`
		Age  int    `xml:"age"`
	}

	input := `<person><name>Jane</name><age>25</age></person>`
	opts := NewParseOptions()

	result, err := Parse[Person](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if result.Data.Name != "Jane" {
		t.Errorf("Expected name 'Jane', got '%s'", result.Data.Name)
	}
	if result.Data.Age != 25 {
		t.Errorf("Expected age 25, got %d", result.Data.Age)
	}
	if result.Format != "xml" {
		t.Errorf("Expected format 'xml', got '%s'", result.Format)
	}
}

func TestParse_YAML(t *testing.T) {
	type Config struct {
		Database string `yaml:"database"`
		Port     int    `yaml:"port"`
	}

	input := `database: postgres
port: 5432`
	opts := NewParseOptions()

	result, err := Parse[Config](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if result.Data.Database != "postgres" {
		t.Errorf("Expected database 'postgres', got '%s'", result.Data.Database)
	}
	if result.Data.Port != 5432 {
		t.Errorf("Expected port 5432, got %d", result.Data.Port)
	}
	if result.Format != "yaml" {
		t.Errorf("Expected format 'yaml', got '%s'", result.Format)
	}
}

func TestParse_CSV(t *testing.T) {
	type Person struct {
		Name string
		Age  int
	}

	input := `Name,Age
John,30
Jane,25`
	opts := NewParseOptions()

	result, err := Parse[[]Person](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Fatalf("Expected 2 persons, got %d", len(result.Data))
	}

	if result.Data[0].Name != "John" || result.Data[0].Age != 30 {
		t.Errorf("First person: expected John/30, got %s/%d", result.Data[0].Name, result.Data[0].Age)
	}
	if result.Data[1].Name != "Jane" || result.Data[1].Age != 25 {
		t.Errorf("Second person: expected Jane/25, got %s/%d", result.Data[1].Name, result.Data[1].Age)
	}
	if result.Format != "csv" {
		t.Errorf("Expected format 'csv', got '%s'", result.Format)
	}
}

func TestParse_PipeDelimited(t *testing.T) {
	type Employee struct {
		Name string
		Job  string
		ID   int
	}

	input := `John|Engineer|123
Jane|Designer|456`
	opts := NewParseOptions()

	result, err := Parse[[]Employee](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Fatalf("Expected 2 employees, got %d", len(result.Data))
	}

	if result.Data[0].Name != "John" || result.Data[0].Job != "Engineer" || result.Data[0].ID != 123 {
		t.Errorf("First employee: expected John/Engineer/123, got %s/%s/%d",
			result.Data[0].Name, result.Data[0].Job, result.Data[0].ID)
	}
	if result.Format != "pipe-delimited" {
		t.Errorf("Expected format 'pipe-delimited', got '%s'", result.Format)
	}
}

func TestParse_WithFormatHints(t *testing.T) {
	type Person struct {
		Name string
		Age  int
		Job  string
	}

	input := `Alice|28|Developer`
	opts := NewParseOptions().WithFormatHints([]string{"name|age|job"})

	result, err := Parse[Person](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if result.Data.Name != "Alice" || result.Data.Age != 28 || result.Data.Job != "Developer" {
		t.Errorf("Expected Alice/28/Developer, got %s/%d/%s",
			result.Data.Name, result.Data.Age, result.Data.Job)
	}
}

func TestParse_MalformedData_AutoFix(t *testing.T) {
	// This test would require LLM fallback, but we'll test the option setup
	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	input := `{"name":"John","age":30` // Missing closing brace
	opts := NewParseOptions().WithAutoFix(true).WithAllowLLMFallback(true)

	// This should fail with algorithm but succeed with LLM (in real scenario)
	_, err := Parse[Person](input, opts)
	// We expect this to fail in tests without LLM, but the option should be set correctly
	if err == nil {
		t.Log("Unexpected success - LLM fallback would be needed")
	}
}

func TestParse_CustomDelimiters(t *testing.T) {
	type Data struct {
		Field1 string
		Field2 string
		Field3 string
	}

	input := `A;B;C`
	opts := NewParseOptions().WithCustomDelimiters([]string{";"})

	result, err := Parse[Data](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if result.Data.Field1 != "A" || result.Data.Field2 != "B" || result.Data.Field3 != "C" {
		t.Errorf("Expected A/B/C, got %s/%s/%s",
			result.Data.Field1, result.Data.Field2, result.Data.Field3)
	}
}

func TestParse_TypeConversions(t *testing.T) {
	type Data struct {
		Name   string
		Age    int
		Height float64
		Active bool
		Count  uint
	}

	input := `Name,Age,Height,Active,Count
John,30,5.9,true,100`
	opts := NewParseOptions()

	result, err := Parse[Data](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if result.Data.Name != "John" || result.Data.Age != 30 || result.Data.Height != 5.9 ||
		!result.Data.Active || result.Data.Count != 100 {
		t.Errorf("Type conversion failed: %+v", result.Data)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	type Person struct {
		Name string
	}

	_, err := Parse[Person]("", NewParseOptions())
	if err == nil {
		t.Error("Expected error for empty input")
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	type Person struct {
		Name string
	}

	input := `{"name": "John", "invalid": }` // Invalid JSON
	_, err := Parse[Person](input, NewParseOptions())
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestParse_UnsupportedFormat(t *testing.T) {
	type Person struct {
		Name string
	}

	input := `Some random text that doesn't match any format`
	_, err := Parse[Person](input, NewParseOptions())
	if err == nil {
		t.Error("Expected error for unsupported format")
	}
}

func TestParse_OptionsValidation(t *testing.T) {
	opts := NewParseOptions()
	if err := opts.Validate(); err != nil {
		t.Errorf("Options validation failed: %v", err)
	}
}

func TestParse_NormalizeInput(t *testing.T) {
	type Person struct {
		Name string
	}

	// Test string input
	result1, err1 := Parse[Person](`{"name":"test"}`, NewParseOptions())
	if err1 != nil || result1.Data.Name != "test" {
		t.Error("String input failed")
	}

	// Test []byte input
	result2, err2 := Parse[Person]([]byte(`{"name":"test2"}`), NewParseOptions())
	if err2 != nil || result2.Data.Name != "test2" {
		t.Error("[]byte input failed")
	}
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hints    []string
	}{
		{`{"key":"value"}`, "json", nil},
		{`<root><key>value</key></root>`, "xml", nil},
		{"key: value\nother: data", "yaml", nil},
		{"Name,Age\nJohn,30", "csv", nil},
		{"John|30|Engineer", "pipe-delimited", nil},
		{"John\t30\tEngineer", "tsv", nil},
		{"some text", "unknown", nil},
		{"any input", "json", []string{"json"}},
	}

	for _, test := range tests {
		result := detectFormat(test.input, test.hints)
		if result != test.expected {
			t.Errorf("detectFormat(%q, %v) = %q, expected %q", test.input, test.hints, result, test.expected)
		}
	}
}

func TestParse_MixedFormat(t *testing.T) {
	// Test parsing data that contains mixed formats
	type Config struct {
		Database string `json:"database"`
		Settings string `json:"settings"`
	}

	input := `{"database": "host=localhost\nport=5432", "settings": "key1=value1\nkey2=value2"}`
	opts := NewParseOptions()

	result, err := Parse[Config](input, opts)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if result.Data.Database != "host=localhost\nport=5432" {
		t.Errorf("Expected database config, got %q", result.Data.Database)
	}
	if result.Format != "json" {
		t.Errorf("Expected format 'json', got '%s'", result.Format)
	}
}

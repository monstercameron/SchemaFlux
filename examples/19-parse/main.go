// 19-parse: Parse unstructured/semi-structured text into typed Go structs
// Intelligence: Fast (Cerebras gpt-oss-120b) - for LLM fallback on complex formats
// Expectations:
// - Auto-detects JSON, XML, YAML, CSV formats
// - Parses pipe-delimited and custom formats
// - Converts types (string?int, string?bool, etc.)
// - Falls back to LLM for malformed or ambiguous data

package main

import (
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

type Person struct {
	Name string `json:"name" xml:"name" yaml:"name"`
	Age  int    `json:"age" xml:"age" yaml:"age"`
	Job  string `json:"job,omitempty" xml:"job,omitempty" yaml:"job,omitempty"`
}

type Employee struct {
	Name   string  `json:"name"`
	Age    int     `json:"age"`
	Salary float64 `json:"salary"`
	Active bool    `json:"active"`
}

type Config struct {
	Database string            `json:"database" yaml:"database"`
	Port     int               `json:"port" yaml:"port"`
	Settings map[string]string `json:"settings" yaml:"settings"`
}

func main() {

	fmt.Println("=== SchemaFlux Parse Operation Examples ===")

	// Initialize SchemaFlux with Fast intelligence (Cerebras)
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	// Example 1: Parse standard JSON
	fmt.Println("1. Parsing Standard JSON:")
	jsonData := `{"name":"Alice","age":28,"job":"Engineer"}`
	result1, err := schemaflux.Parse[Person](jsonData, schemaflux.NewParseOptions())
	if err != nil {
		schemaflux.GetLogger().Error("JSON parse error", "error", err)
	} else {
		fmt.Printf("   Result: %+v (Format: %s)\n", result1.Data, result1.Format)
	}

	// Example 2: Parse XML
	fmt.Println("\n2. Parsing XML:")
	xmlData := `<person><name>Bob</name><age>35</age><job>Manager</job></person>`
	result2, err := schemaflux.Parse[Person](xmlData, schemaflux.NewParseOptions())
	if err != nil {
		schemaflux.GetLogger().Error("XML parse error", "error", err)
	} else {
		fmt.Printf("   Result: %+v (Format: %s)\n", result2.Data, result2.Format)
	}

	// Example 3: Parse YAML
	fmt.Println("\n3. Parsing YAML:")
	yamlData := `name: Charlie
age: 42
job: Director`
	result3, err := schemaflux.Parse[Person](yamlData, schemaflux.NewParseOptions())
	if err != nil {
		schemaflux.GetLogger().Error("YAML parse error", "error", err)
	} else {
		fmt.Printf("   Result: %+v (Format: %s)\n", result3.Data, result3.Format)
	}

	// Example 4: Parse CSV data
	fmt.Println("\n4. Parsing CSV:")
	csvData := `Name,Age,Salary,Active
John,30,75000,true
Jane,25,65000,false
Bob,35,80000,true`
	result4, err := schemaflux.Parse[[]Employee](csvData, schemaflux.NewParseOptions())
	if err != nil {
		schemaflux.GetLogger().Error("CSV parse error", "error", err)
	} else {
		fmt.Printf("   Result: %d employees parsed (Format: %s)\n", len(result4.Data), result4.Format)
		for i, emp := range result4.Data {
			fmt.Printf("     Employee %d: %+v\n", i+1, emp)
		}
	}

	// Example 5: Parse pipe-delimited custom format
	fmt.Println("\n5. Parsing Pipe-Delimited Data:")
	pipeData := `David|40|90000|true
Eva|28|70000|false`
	result5, err := schemaflux.Parse[[]Employee](pipeData, schemaflux.NewParseOptions())
	if err != nil {
		schemaflux.GetLogger().Error("Pipe-delimited parse error", "error", err)
	} else {
		fmt.Printf("   Result: %d employees parsed (Format: %s)\n", len(result5.Data), result5.Format)
		for i, emp := range result5.Data {
			fmt.Printf("     Employee %d: %+v\n", i+1, emp)
		}
	}

	// Example 6: Parse with format hints for custom mapping
	fmt.Println("\n6. Parsing with Format Hints:")
	customData := `Alice|29|Senior Developer`
	result6, err := schemaflux.Parse[Person](customData,
		schemaflux.NewParseOptions().WithFormatHints([]string{"name|age|job"}))
	if err != nil {
		schemaflux.GetLogger().Error("Custom format parse error", "error", err)
	} else {
		fmt.Printf("   Result: %+v (Format: %s)\n", result6.Data, result6.Format)
	}

	// Example 7: Parse mixed format data (JSON containing other formats)
	fmt.Println("\n7. Parsing Mixed Format Data:")
	mixedData := `{
  "database": "host=localhost\nport=5432\nuser=admin",
  "port": 5432,
  "settings": {
    "timeout": "30s",
    "retries": "3"
  }
}`
	result7, err := schemaflux.Parse[Config](mixedData, schemaflux.NewParseOptions())
	if err != nil {
		schemaflux.GetLogger().Error("Mixed format parse error", "error", err)
	} else {
		fmt.Printf("   Result: Database config parsed (Format: %s)\n", result7.Format)
		fmt.Printf("     Database: %q\n", result7.Data.Database)
		fmt.Printf("     Port: %d\n", result7.Data.Port)
		fmt.Printf("     Settings: %+v\n", result7.Data.Settings)
	}

	// Example 8: Parse with custom delimiters
	fmt.Println("\n8. Parsing with Custom Delimiters:")
	customDelimData := `Name;Age;Job
Frank;45;Architect`
	result8, err := schemaflux.Parse[Person](customDelimData,
		schemaflux.NewParseOptions().WithCustomDelimiters([]string{";"}))
	if err != nil {
		schemaflux.GetLogger().Error("Custom delimiter parse error", "error", err)
	} else {
		fmt.Printf("   Result: %+v (Format: %s)\n", result8.Data, result8.Format)
	}

	// Example 9: Demonstrate error handling for malformed data
	fmt.Println("\n9. Error Handling for Malformed Data:")
	malformedData := `{"name":"Grace","age":32,"job":` // Missing closing quote and brace
	result9, err := schemaflux.Parse[Person](malformedData, schemaflux.NewParseOptions())
	if err != nil {
		fmt.Printf("   Expected error for malformed JSON: %v\n", err)
		fmt.Printf("   (This would succeed with AllowLLMFallback=true)\n")
	} else {
		fmt.Printf("   Unexpected success: %+v\n", result9.Data)
	}

	// Example 10: Type conversion demonstration
	fmt.Println("\n10. Type Conversion:")
	typeConversionData := `Name,Age,Height,Active,Count
Helen,33,5.7,true,250`
	result10, err := schemaflux.Parse[Employee](typeConversionData, schemaflux.NewParseOptions())
	if err != nil {
		schemaflux.GetLogger().Error("Type conversion parse error", "error", err)
	} else {
		fmt.Printf("   Result: %+v (Format: %s)\n", result10.Data, result10.Format)
		fmt.Printf("   Types: Name=%T, Age=%T, Salary=%T, Active=%T\n",
			result10.Data.Name, result10.Data.Age, result10.Data.Salary, result10.Data.Active)
	}

	fmt.Println("\n=== Parse Operation Examples Complete ===")
}

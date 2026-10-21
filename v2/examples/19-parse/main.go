// 19-parse demonstrates parsing structured and semi-structured text into Go values.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
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
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== SchemaFlux Parse Operation Examples ===")

	fmt.Fprintln(out, "1. Parsing Standard JSON:")
	jsonData := `{"name":"Alice","age":28,"job":"Engineer"}`
	result1, err := schemaflux.Parse[Person](ctx, jsonData)
	if err != nil {
		return fmt.Errorf("parse the JSON example; check that the input is valid JSON: %w", err)
	}
	fmt.Fprintf(out, "   Result: %+v (Format: %s)\n", result1.Data, result1.Format)

	fmt.Fprintln(out, "\n2. Parsing XML:")
	xmlData := `<person><name>Bob</name><age>35</age><job>Manager</job></person>`
	result2, err := schemaflux.Parse[Person](ctx, xmlData)
	if err != nil {
		return fmt.Errorf("parse the XML example; check its elements and types: %w", err)
	}
	fmt.Fprintf(out, "   Result: %+v (Format: %s)\n", result2.Data, result2.Format)

	fmt.Fprintln(out, "\n3. Parsing YAML:")
	yamlData := "name: Charlie\nage: 42\njob: Director"
	result3, err := schemaflux.Parse[Person](ctx, yamlData)
	if err != nil {
		return fmt.Errorf("parse the YAML example; check its keys and types: %w", err)
	}
	fmt.Fprintf(out, "   Result: %+v (Format: %s)\n", result3.Data, result3.Format)

	fmt.Fprintln(out, "\n4. Parsing CSV:")
	csvData := "Name,Age,Salary,Active\nJohn,30,75000,true\nJane,25,65000,false\nBob,35,80000,true"
	result4, err := schemaflux.Parse[[]Employee](ctx, csvData)
	if err != nil {
		return fmt.Errorf("parse the employee CSV; check the header and each row: %w", err)
	}
	fmt.Fprintf(out, "   Result: %d employees parsed (Format: %s)\n", len(result4.Data), result4.Format)
	for i, emp := range result4.Data {
		fmt.Fprintf(out, "     Employee %d: %+v\n", i+1, emp)
	}

	fmt.Fprintln(out, "\n5. Parsing Pipe-Delimited Data:")
	pipeData := "David|40|90000|true\nEva|28|70000|false"
	result5, err := schemaflux.Parse[[]Employee](ctx, pipeData)
	if err != nil {
		return fmt.Errorf("parse the pipe-delimited employee data; check the row values: %w", err)
	}
	fmt.Fprintf(out, "   Result: %d employees parsed (Format: %s)\n", len(result5.Data), result5.Format)
	for i, emp := range result5.Data {
		fmt.Fprintf(out, "     Employee %d: %+v\n", i+1, emp)
	}

	fmt.Fprintln(out, "\n6. Parsing with Format Hints:")
	customData := "Alice|29|Senior Developer"
	result6, err := schemaflux.Parse[Person](ctx, customData, schemaflux.ParseOptions{FormatHints: []string{"name|age|job"}})
	if err != nil {
		return fmt.Errorf("parse the hinted custom format; check the column names: %w", err)
	}
	fmt.Fprintf(out, "   Result: %+v (Format: %s)\n", result6.Data, result6.Format)

	fmt.Fprintln(out, "\n7. Parsing Mixed Format Data:")
	mixedData := "{\n  \"database\": \"host=localhost\\nport=5432\\nuser=admin\",\n  \"port\": 5432,\n  \"settings\": {\n    \"timeout\": \"30s\",\n    \"retries\": \"3\"\n  }\n}"
	result7, err := schemaflux.Parse[Config](ctx, mixedData)
	if err != nil {
		return fmt.Errorf("parse the database configuration; check the JSON structure: %w", err)
	}
	fmt.Fprintf(out, "   Result: Database config parsed (Format: %s)\n", result7.Format)
	fmt.Fprintf(out, "     Database: %q\n     Port: %d\n     Settings: %+v\n", result7.Data.Database, result7.Data.Port, result7.Data.Settings)

	fmt.Fprintln(out, "\n8. Parsing with Custom Delimiters:")
	customDelimData := "Name;Age;Job\nFrank;45;Architect"
	result8, err := schemaflux.Parse[Person](ctx, customDelimData, schemaflux.ParseOptions{CustomDelimiters: []string{";"}})
	if err != nil {
		return fmt.Errorf("parse the semicolon-delimited data; check its delimiter and header: %w", err)
	}
	fmt.Fprintf(out, "   Result: %+v (Format: %s)\n", result8.Data, result8.Format)

	fmt.Fprintln(out, "\n9. Error Handling for Malformed Data:")
	malformedData := `{"name":"Grace","age":32,"job":` // Missing closing quote and brace.
	// Local parsers reject malformed JSON; the explicit fallback lets the model repair it.
	result9, err := schemaflux.Parse[Person](ctx, malformedData, schemaflux.ParseOptions{AllowLLMFallback: true, AutoFix: true})
	if err != nil {
		return fmt.Errorf("repair the malformed JSON example; provide valid JSON or check provider configuration: %w", err)
	}
	fmt.Fprintf(out, "   Recovered: %+v (Format: %s)\n", result9.Data, result9.Format)

	fmt.Fprintln(out, "\n10. Type Conversion:")
	typeConversionData := "Name,Age,Salary,Active\nHelen,33,75000,true"
	result10, err := schemaflux.Parse[Employee](ctx, typeConversionData)
	if err != nil {
		return fmt.Errorf("parse the employee type-conversion row; check numeric and boolean values: %w", err)
	}
	fmt.Fprintf(out, "   Result: %+v (Format: %s)\n", result10.Data, result10.Format)
	fmt.Fprintf(out, "   Types: Name=%T, Age=%T, Salary=%T, Active=%T\n", result10.Data.Name, result10.Data.Age, result10.Data.Salary, result10.Data.Active)

	fmt.Fprintln(out, "\n=== Parse Operation Examples Complete ===")
	return nil
}

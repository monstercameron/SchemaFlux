// package ops - Parse operation for intelligent data parsing
package ops

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ParseResult contains the parsed data and metadata
type ParseResult[T any] struct {
	Data   T      `json:"data"`   // The parsed data
	Format string `json:"format"` // Detected format (json, xml, csv, yaml, custom, etc.)
}

// ParseOptions configures the Parse operation
type ParseOptions struct {
	types.OpOptions
	AllowLLMFallback bool     // Allow LLM fallback for malformed/custom formats
	AutoFix          bool     // Attempt to fix malformed data
	FormatHints      []string // Hints about expected formats
	CustomDelimiters []string // Custom delimiters for parsing
}

// NewParseOptions creates ParseOptions with defaults
func NewParseOptions() ParseOptions {
	return ParseOptions{
		OpOptions: types.OpOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		AllowLLMFallback: false,
		AutoFix:          false,
	}
}

// WithAllowLLMFallback enables LLM fallback for complex parsing
func (opts ParseOptions) WithAllowLLMFallback(allow bool) ParseOptions {
	opts.AllowLLMFallback = allow
	return opts
}

// WithAutoFix enables automatic fixing of malformed data
func (opts ParseOptions) WithAutoFix(autoFix bool) ParseOptions {
	opts.AutoFix = autoFix
	return opts
}

// WithFormatHints provides hints about expected formats
func (opts ParseOptions) WithFormatHints(hints []string) ParseOptions {
	opts.FormatHints = hints
	return opts
}

// WithCustomDelimiters sets custom delimiters for parsing
func (opts ParseOptions) WithCustomDelimiters(delimiters []string) ParseOptions {
	opts.CustomDelimiters = delimiters
	return opts
}

// WithIntelligence sets the intelligence level for LLM fallback
func (opts ParseOptions) WithIntelligence(intelligence types.Speed) ParseOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// Validate validates ParseOptions
func (opts ParseOptions) Validate() error {
	return nil // No validation needed for now
}

// toOpOptions converts ParseOptions to types.OpOptions
func (opts ParseOptions) toOpOptions() types.OpOptions {
	return opts.OpOptions
}

// Parse intelligently parses data from various formats into strongly-typed Go structs.
// It uses traditional parsing algorithms for standard formats and can fall back to LLM
// for malformed data recovery and custom format parsing.
//
// Type parameter T specifies the target type to parse into.
//
// Examples:
//
//	// Parse standard JSON
//	result, err := Parse[Person](`{"name":"John","age":30}`, NewParseOptions())
//
//	// Parse with auto-fix for malformed data
//	result, err := Parse[Person](`{"name":"John","age":30`, NewParseOptions().WithAutoFix(true))
//
//	// Parse custom format with LLM fallback
//	result, err := Parse[Person]("JOHN|30|ENGINEER", NewParseOptions().
//	    WithAllowLLMFallback(true).
//	    WithFormatHints([]string{"pipe-delimited", "name|age|job"}))
//
//	// Parse mixed format data
//	result, err := Parse[Config](`{"database": "host=localhost\nport=5432"}`, NewParseOptions())
//
// The operation automatically detects formats and handles common edge cases.
func Parse[T any](input any, opts ParseOptions) (ParseResult[T], error) {
	return parseImpl[T](input, opts)
}

func parseImpl[T any](input any, opts ParseOptions) (ParseResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting parse operation", "requestID", opts.RequestID, "inputType", fmt.Sprintf("%T", input))

	var result ParseResult[T]

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Parse operation validation failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("invalid options: %w", err)
	}

	// Convert input to string
	inputStr, err := normalizeParseInput(input)
	if err != nil {
		log.Error("Parse operation input normalization failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("failed to normalize input: %w", err)
	}

	// Detect format using fast algorithms
	format := detectFormat(inputStr, opts.FormatHints)

	// Try traditional parsing first
	parsedData, err := parseWithAlgorithm[T](inputStr, format, opts)
	if err == nil {
		result.Data = parsedData
		result.Format = format
		log.Debug("Parse operation succeeded with algorithm", "requestID", opts.RequestID, "format", format)
		return result, nil
	}

	// If traditional parsing failed but custom delimiters are provided, try delimited parsing
	if len(opts.CustomDelimiters) > 0 && format == "unknown" {
		if parsedData, delimErr := parseDelimited[T](inputStr, opts.CustomDelimiters[0], opts); delimErr == nil {
			result.Data = parsedData
			result.Format = "custom-delimited"
			log.Debug("Parse operation succeeded with custom delimiters", "requestID", opts.RequestID)
			return result, nil
		}
	}

	// If traditional parsing failed and LLM fallback is enabled, try LLM
	if opts.AllowLLMFallback {
		llmResult, llmErr := parseWithLLM[T](inputStr, format, opts)
		if llmErr != nil {
			log.Error("Parse operation LLM fallback failed", "requestID", opts.RequestID, "error", llmErr)
			return result, fmt.Errorf("LLM parsing failed: %w", llmErr)
		}
		log.Debug("Parse operation succeeded with LLM", "requestID", opts.RequestID, "format", llmResult.Format)
		return llmResult, nil
	}

	// Return the algorithm error if no fallback
	log.Error("Parse operation failed", "requestID", opts.RequestID, "error", err)
	return result, fmt.Errorf("parsing failed: %w (consider enabling AllowLLMFallback)", err)
}

// normalizeParseInput converts various input types to string
func normalizeParseInput(input any) (string, error) {
	switch v := input.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		// Try JSON marshaling for complex types
		if b, err := json.Marshal(input); err == nil {
			return string(b), nil
		}
		return fmt.Sprintf("%v", input), nil
	}
}

// detectFormat uses fast algorithms to detect data format
func detectFormat(input string, hints []string) string {
	input = strings.TrimSpace(input)

	// Check for explicit hints first
	for _, hint := range hints {
		switch strings.ToLower(hint) {
		case "json":
			return "json"
		case "xml":
			return "xml"
		case "csv":
			return "csv"
		case "yaml", "yml":
			return "yaml"
		}
	}

	// Fast format detection
	if strings.HasPrefix(input, "{") && strings.HasSuffix(input, "}") {
		return "json"
	}
	if strings.HasPrefix(input, "<") && strings.HasSuffix(input, ">") {
		return "xml"
	}
	if strings.Contains(input, "\n") && strings.Contains(input, ",") {
		// Simple CSV detection
		lines := strings.Split(input, "\n")
		if len(lines) >= 2 {
			firstLine := strings.Split(lines[0], ",")
			secondLine := strings.Split(lines[1], ",")
			if len(firstLine) == len(secondLine) && len(firstLine) > 1 {
				return "csv"
			}
		}
	}
	if (strings.Contains(input, ": ") || strings.Contains(input, ":\n")) &&
		!strings.Contains(input, "{") {
		return "yaml"
	}

	// Check for custom delimiters
	if strings.Contains(input, "|") && !strings.Contains(input, "{") {
		return "pipe-delimited"
	}
	if strings.Contains(input, "\t") {
		return "tsv"
	}

	return "unknown"
}

// parseWithAlgorithm uses traditional parsing algorithms for standard formats
func parseWithAlgorithm[T any](input string, format string, opts ParseOptions) (T, error) {
	var result T

	switch format {
	case "json":
		return parseJSON[T](input)
	case "xml":
		return parseXML[T](input)
	case "csv":
		return parseCSV[T](input)
	case "yaml", "yml":
		return parseYAML[T](input)
	case "pipe-delimited":
		return parseDelimited[T](input, "|", opts)
	case "tsv":
		return parseDelimited[T](input, "\t", opts)
	default:
		return result, fmt.Errorf("unsupported format: %s", format)
	}
}

// parseJSON parses JSON using standard library
func parseJSON[T any](input string) (T, error) {
	var result T
	err := json.Unmarshal([]byte(input), &result)
	return result, err
}

// parseXML parses XML using standard library
func parseXML[T any](input string) (T, error) {
	var result T
	err := xml.Unmarshal([]byte(input), &result)
	return result, err
}

// parseYAML parses YAML using yaml.v3
func parseYAML[T any](input string) (T, error) {
	var result T
	err := yaml.Unmarshal([]byte(input), &result)
	return result, err
}

// parseCSV parses CSV data
func parseCSV[T any](input string) (T, error) {
	var result T

	reader := csv.NewReader(strings.NewReader(input))
	records, err := reader.ReadAll()
	if err != nil {
		return result, err
	}

	if len(records) < 2 {
		return result, fmt.Errorf("CSV must have at least header and one data row")
	}

	headers := records[0]
	data := records[1:]

	// Handle slice types
	resultType := reflect.TypeOf(result)
	if resultType.Kind() == reflect.Slice {
		elemType := resultType.Elem()
		slice := reflect.MakeSlice(resultType, len(data), len(data))

		for i, row := range data {
			item := reflect.New(elemType).Elem()
			if err := mapCSVRowToStruct(row, headers, item); err != nil {
				return result, err
			}
			slice.Index(i).Set(item)
		}

		result = slice.Interface().(T)
		return result, nil
	}

	// Handle single struct
	if len(data) > 0 {
		item := reflect.ValueOf(&result).Elem()
		if err := mapCSVRowToStruct(data[0], headers, item); err != nil {
			return result, err
		}
	}

	return result, nil
}

// parseDelimited parses custom delimited data
func parseDelimited[T any](input string, delimiter string, opts ParseOptions) (T, error) {
	var result T

	lines := strings.Split(strings.TrimSpace(input), "\n")
	if len(lines) == 0 {
		return result, fmt.Errorf("empty input")
	}

	// Use custom delimiters if provided
	if len(opts.CustomDelimiters) > 0 {
		delimiter = opts.CustomDelimiters[0]
	}

	// Handle slice types
	resultType := reflect.TypeOf(result)
	if resultType.Kind() == reflect.Slice {
		elemType := resultType.Elem()
		slice := reflect.MakeSlice(resultType, len(lines), len(lines))

		for i, line := range lines {
			fields := strings.Split(line, delimiter)
			item := reflect.New(elemType).Elem()
			if err := mapDelimitedFieldsToStruct(fields, item, opts.FormatHints); err != nil {
				return result, err
			}
			slice.Index(i).Set(item)
		}

		result = slice.Interface().(T)
		return result, nil
	}

	// Handle single struct - use the last line (most likely to be data, not headers)
	dataLine := lines[len(lines)-1]
	fields := strings.Split(dataLine, delimiter)
	item := reflect.ValueOf(&result).Elem()
	if err := mapDelimitedFieldsToStruct(fields, item, opts.FormatHints); err != nil {
		return result, err
	}

	return result, nil
}

// mapCSVRowToStruct maps CSV row to struct fields
func mapCSVRowToStruct(row []string, headers []string, target reflect.Value) error {
	if target.Kind() != reflect.Struct {
		return fmt.Errorf("target must be a struct")
	}

	for i, header := range headers {
		if i >= len(row) {
			continue
		}

		field := target.FieldByName(capitalizeFirst(header))
		if !field.IsValid() || !field.CanSet() {
			continue
		}

		if err := setFieldValue(field, row[i]); err != nil {
			return fmt.Errorf("failed to set field %s: %w", header, err)
		}
	}

	return nil
}

// mapDelimitedFieldsToStruct maps delimited fields to struct
func mapDelimitedFieldsToStruct(fields []string, target reflect.Value, hints []string) error {
	if target.Kind() != reflect.Struct {
		return fmt.Errorf("target must be a struct")
	}

	// Try to use hints for field mapping
	if len(hints) > 0 {
		return mapWithHints(fields, target, hints)
	}

	// Default: map by position
	for i := 0; i < target.NumField() && i < len(fields); i++ {
		field := target.Field(i)
		if !field.CanSet() {
			continue
		}

		if err := setFieldValue(field, fields[i]); err != nil {
			return fmt.Errorf("failed to set field %d: %w", i, err)
		}
	}

	return nil
}

// mapWithHints uses format hints to map fields
func mapWithHints(fields []string, target reflect.Value, hints []string) error {
	// Look for field mapping hints like "name|age|job"
	for _, hint := range hints {
		if strings.Contains(hint, "|") {
			fieldNames := strings.Split(hint, "|")
			for i, fieldName := range fieldNames {
				if i >= len(fields) {
					break
				}

				field := target.FieldByName(capitalizeFirst(strings.TrimSpace(fieldName)))
				if field.IsValid() && field.CanSet() {
					if err := setFieldValue(field, fields[i]); err != nil {
						return fmt.Errorf("failed to set field %s: %w", fieldName, err)
					}
				}
			}
			return nil
		}
	}

	return fmt.Errorf("no valid field mapping hints found")
}

// setFieldValue sets a field value with type conversion
func setFieldValue(field reflect.Value, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil // Keep zero value
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if intVal, err := strconv.ParseInt(value, 10, 64); err == nil {
			field.SetInt(intVal)
		} else {
			return err
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if uintVal, err := strconv.ParseUint(value, 10, 64); err == nil {
			field.SetUint(uintVal)
		} else {
			return err
		}
	case reflect.Float32, reflect.Float64:
		if floatVal, err := strconv.ParseFloat(value, 64); err == nil {
			field.SetFloat(floatVal)
		} else {
			return err
		}
	case reflect.Bool:
		if boolVal, err := strconv.ParseBool(value); err == nil {
			field.SetBool(boolVal)
		} else {
			return err
		}
	default:
		return fmt.Errorf("unsupported field type: %s", field.Kind())
	}

	return nil
}

// parseWithLLM uses LLM for complex parsing cases
func parseWithLLM[T any](input string, detectedFormat string, opts ParseOptions) (ParseResult[T], error) {
	var result ParseResult[T]

	ctx, cancel := context.WithTimeout(context.Background(), config.GetTimeout())
	defer cancel()

	// Generate type schema
	var zero T
	targetType := reflect.TypeOf(zero)
	typeSchema := GenerateTypeSchema(targetType)

	// Build prompt based on context
	systemPrompt := buildParseSystemPrompt(detectedFormat, opts)
	userPrompt := buildParseUserPrompt(input, typeSchema, detectedFormat, opts)

	// Call LLM
	opt := opts.toOpOptions()
	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		return result, fmt.Errorf("LLM parsing failed: %w", err)
	}

	// Parse LLM response
	if err := ParseJSON(response, &zero); err != nil {
		return result, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	result.Data = zero
	result.Format = detectedFormat + " (LLM-assisted)"
	return result, nil
}

// buildParseSystemPrompt creates system prompt for LLM parsing
func buildParseSystemPrompt(format string, opts ParseOptions) string {
	prompt := "You are an expert data parser. Parse the input data into the specified Go struct format.\n\n"

	switch format {
	case "unknown", "custom":
		prompt += "The data appears to be in a custom or unknown format. Analyze the structure and extract meaningful information.\n"
	case "pipe-delimited", "tsv":
		prompt += "Parse the delimited data, handling field mapping and type conversion.\n"
	default:
		prompt += fmt.Sprintf("Parse the %s data, correcting any formatting issues.\n", format)
	}

	if opts.AutoFix {
		prompt += "Attempt to fix malformed or incomplete data where possible.\n"
	}

	prompt += "\nReturn ONLY valid JSON matching the target schema, no explanations or markdown."

	return prompt
}

// buildParseUserPrompt creates user prompt for LLM parsing
func buildParseUserPrompt(input, typeSchema, format string, opts ParseOptions) string {
	prompt := fmt.Sprintf("Parse this data into the following Go struct:\n\n%s\n\n", typeSchema)
	prompt += fmt.Sprintf("Input data (%s):\n%s\n\n", format, input)

	if len(opts.FormatHints) > 0 {
		prompt += fmt.Sprintf("Format hints: %s\n\n", strings.Join(opts.FormatHints, ", "))
	}

	if len(opts.CustomDelimiters) > 0 {
		prompt += fmt.Sprintf("Custom delimiters: %s\n\n", strings.Join(opts.CustomDelimiters, ", "))
	}

	return prompt
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

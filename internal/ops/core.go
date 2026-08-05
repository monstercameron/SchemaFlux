// package ops - Data operations for structured data extraction and transformation
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// Extract converts unstructured data into strongly-typed Go structs using LLM interpretation.
// It handles various input formats (string, JSON, structs) and maps them to the target type.
//
// Type parameter T specifies the target Go type to extract into.
//
// Examples:
//
//	// Basic extraction
//	person, err := Extract[Person]("John Smith, 28 years old", NewExtractOptions())
//
//	// Extraction with schema hints
//	data, err := Extract[Invoice](jsonString, NewExtractOptions().
//	    WithStrictSchema(true).
//	    WithSchemaHints(map[string]string{
//	        "date": "ISO 8601 format",
//	        "amount": "USD currency",
//	    }))
//
//	// Extraction with examples
//	product, err := Extract[Product](description, NewExtractOptions().
//	    WithExamples(exampleProduct1, exampleProduct2).
//	    WithSteering("Focus on technical specifications"))
//
// The operation uses schema inference to guide the LLM in structured extraction.
// In Strict mode, all required fields must be present. In Transform mode (default),
// the LLM will intelligently infer missing fields.
func Extract[T any](input any, opts ExtractOptions) (T, error) {
	var result T
	log := logger.GetLogger()

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	// Convert to legacy OpOptions for internal use
	opt := opts.toOpOptions()

	// Enhance steering with extraction-specific options
	if opts.SchemaHints != nil || opts.Examples != nil || opts.FieldRules != nil {
		var steeringParts []string
		if opts.OpOptions.Steering != "" {
			steeringParts = append(steeringParts, opts.OpOptions.Steering)
		}

		if opts.StrictSchema {
			steeringParts = append(steeringParts, "Enforce strict schema validation. All fields must be present and valid.")
		}

		if opts.AllowPartial {
			steeringParts = append(steeringParts, "Allow partial extraction if some fields are missing.")
		}

		if len(opts.SchemaHints) > 0 {
			hints := "Schema hints: "
			for _, field := range sortedKeys(opts.SchemaHints) {
				hints += fmt.Sprintf("%s (%s), ", field, opts.SchemaHints[field])
			}
			steeringParts = append(steeringParts, strings.TrimSuffix(hints, ", "))
		}

		if len(opts.FieldRules) > 0 {
			rules := "Field rules: "
			for _, field := range sortedKeys(opts.FieldRules) {
				rules += fmt.Sprintf("%s: %s; ", field, opts.FieldRules[field])
			}
			steeringParts = append(steeringParts, strings.TrimSuffix(rules, "; "))
		}

		if len(opts.Examples) > 0 {
			examplesJSON, _ := json.Marshal(opts.Examples)
			steeringParts = append(steeringParts, fmt.Sprintf("Follow these examples: %s", string(examplesJSON)))
		}

		opt.Steering = strings.Join(steeringParts, ". ")
	}

	// Start operation timing
	startTime := time.Now()
	defer func() {
		if config.IsMetricsEnabled() {
			telemetry.RecordMetric("extract_duration", time.Since(startTime).Milliseconds(), map[string]string{
				"type": reflect.TypeOf(result).String(),
				"mode": opt.Mode.String(),
			})
		}
	}()

	// Log operation start
	log.Info("Extract operation started",
		"requestID", opt.RequestID,
		"targetType", reflect.TypeOf(result).String(),
		"mode", opt.Mode.String(),
		"intelligence", opt.Intelligence.String(),
	)

	// Validate input
	if input == nil {
		err := types.ExtractError{
			InputShape: types.DescribeValue(input),
			TargetType: reflect.TypeOf(result).String(),
			Reason:     "input cannot be nil",
			RequestID:  opt.RequestID,
			Timestamp:  time.Now(),
		}
		log.Error("Extract failed: nil input", "requestID", opt.RequestID, "error", err)
		return result, err
	}

	// Get context with timeout
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// Generate type schema for the target type
	targetType := reflect.TypeOf(result)
	typeInfo := GenerateTypeSchema(targetType)

	// Convert input to string format for LLM processing
	inputStr, err := NormalizeInput(input)
	if err != nil {
		extractErr := types.ExtractError{
			InputShape: types.DescribeValue(input),
			TargetType: targetType.String(),
			Reason:     fmt.Sprintf("failed to normalize input: %v", err),
			RequestID:  opt.RequestID,
			Timestamp:  time.Now(),
		}
		log.Error("Extract failed: input normalization error",
			"requestID", opt.RequestID,
			"error", extractErr,
		)
		return result, extractErr
	}

	// Log input details in debug mode
	if config.GetDebugMode() {
		log.Debug("Extract input normalized",
			"requestID", opt.RequestID,
			"inputLength", len(inputStr),
			"inputPreview", inputStr[:Min(len(inputStr), 100)],
		)
	}

	// Build system prompt based on mode
	systemPrompt := BuildExtractSystemPrompt(typeInfo, opt.Mode)

	// Build user prompt
	userPrompt := fmt.Sprintf("Extract structured data from this input:\n%s", inputStr)

	// Call LLM for extraction
	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		extractErr := types.ExtractError{
			InputShape: types.DescribeValue(input),
			TargetType: targetType.String(),
			Reason:     err.Error(),
			RequestID:  opt.RequestID,
			Timestamp:  time.Now(),
			Err:        err,
		}
		log.Error("Extract failed: LLM error",
			"requestID", opt.RequestID,
			"error", extractErr,
		)
		return result, extractErr
	}

	// Parse JSON response into target type
	if err := ParseJSON(response, &result); err != nil {
		extractErr := types.ExtractError{
			InputShape: types.DescribeValue(input),
			TargetType: targetType.String(),
			Reason:     fmt.Sprintf("failed to parse response: %v", err),
			RequestID:  opt.RequestID,
			Timestamp:  time.Now(),
			Err:        err,
		}

		log.Error("Extract failed: JSON parsing error",
			"requestID", opt.RequestID,
			"error", extractErr,
		)
		return result, extractErr
	}

	// Validate extracted data if in Strict mode
	if opt.Mode == types.Strict {
		if err := ValidateExtractedData(result, opt.Threshold); err != nil {
			extractErr := types.ExtractError{
				InputShape: types.DescribeValue(input),
				TargetType: targetType.String(),
				Reason:     fmt.Sprintf("validation failed: %v", err),
				RequestID:  opt.RequestID,
				Timestamp:  time.Now(),
			}
			log.Error("Extract failed: validation error",
				"requestID", opt.RequestID,
				"error", extractErr,
			)
			return result, extractErr
		}
	}

	log.Info("Extract operation completed",
		"requestID", opt.RequestID,
		"duration", time.Since(startTime),
	)

	return result, nil
}

// Transform converts data from one type to another using semantic mapping.
// It understands relationships between different structures and maps fields intelligently.
//
// Type parameters:
//   - T: Source type
//   - U: Target type
//
// Examples:
//
//	// Basic transformation
//	employee, err := Transform[Person, Employee](person, NewTransformOptions())
//
//	// Transformation with mapping rules
//	output, err := Transform[OldFormat, NewFormat](input, NewTransformOptions().
//	    WithMappingRules(map[string]string{
//	        "fullName": "firstName + ' ' + lastName",
//	        "age": "calculate from birthDate",
//	    }).
//	    WithPreserveFields([]string{"id", "createdAt"}))
//
// The operation uses semantic understanding to map between related but structurally
// different types. It can handle field renaming, type conversion, and derived fields.
func Transform[T any, U any](input T, opts TransformOptions) (U, error) {
	var result U
	log := logger.GetLogger()

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	// Convert to legacy OpOptions
	opt := opts.toOpOptions()

	// Enhance steering with transformation-specific options
	var steeringParts []string
	if opts.OpOptions.Steering != "" {
		steeringParts = append(steeringParts, opts.OpOptions.Steering)
	}

	if opts.TransformLogic != "" {
		steeringParts = append(steeringParts, fmt.Sprintf("Apply this transformation: %s", opts.TransformLogic))
	}

	if len(opts.MappingRules) > 0 {
		rules := "Field mappings: "
		for target, source := range opts.MappingRules {
			rules += fmt.Sprintf("%s <- %s; ", target, source)
		}
		steeringParts = append(steeringParts, strings.TrimSuffix(rules, "; "))
	}

	if len(opts.PreserveFields) > 0 {
		steeringParts = append(steeringParts, fmt.Sprintf("Preserve these fields: %s", strings.Join(opts.PreserveFields, ", ")))
	}

	if opts.MergeStrategy != "" {
		steeringParts = append(steeringParts, fmt.Sprintf("Use %s merge strategy", opts.MergeStrategy))
	}

	if len(steeringParts) > 0 {
		opt.Steering = strings.Join(steeringParts, ". ")
	}

	startTime := time.Now()
	defer func() {
		if config.IsMetricsEnabled() {
			telemetry.RecordMetric("transform_duration", time.Since(startTime).Milliseconds(), map[string]string{
				"from_type": reflect.TypeOf(input).String(),
				"to_type":   reflect.TypeOf(result).String(),
				"mode":      opt.Mode.String(),
			})
		}
	}()

	log.Info("Transform operation started",
		"requestID", opt.RequestID,
		"fromType", reflect.TypeOf(input).String(),
		"toType", reflect.TypeOf(result).String(),
	)

	// Validate input
	if reflect.ValueOf(input).IsZero() {
		log.Warn("Transform received zero value input",
			"requestID", opt.RequestID,
			"type", reflect.TypeOf(input).String(),
		)
	}

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// Get type information
	fromType := reflect.TypeOf(input)
	toType := reflect.TypeOf(result)

	fromSchema := GenerateTypeSchema(fromType)
	toSchema := GenerateTypeSchema(toType)

	// Marshal input to JSON
	inputJSON, err := json.Marshal(input)
	if err != nil {
		transformErr := types.TransformError{
			InputShape: types.DescribeValue(input),
			FromType:   fromType.String(),
			ToType:     toType.String(),
			Reason:     fmt.Sprintf("failed to marshal input: %v", err),
			RequestID:  opt.RequestID,
			Timestamp:  time.Now(),
		}
		log.Error("Transform failed: marshaling error",
			"requestID", opt.RequestID,
			"error", transformErr,
		)
		return result, transformErr
	}

	// Build transformation prompt
	systemPrompt := fmt.Sprintf(`You are a data transformation expert. Transform data from one type to another using semantic mapping.

Source schema:
%s

Target schema:
%s

Transformation rules:
- Map semantically related fields even if names differ
- Infer and calculate derived fields when possible
- Handle type conversions intelligently (e.g., string to number)
- Combine or split fields as needed (e.g., fullName <-> firstName+lastName)
- Use reasonable defaults for missing required fields
- Preserve data integrity and meaning
- Return ONLY valid JSON matching the target schema`, fromSchema, toSchema)

	userPrompt := fmt.Sprintf("Transform this data:\n%s", string(inputJSON))

	// Log transformation details in debug mode
	if config.GetDebugMode() {
		log.Debug("Transform schemas",
			"requestID", opt.RequestID,
			"fromSchema", fromSchema,
			"toSchema", toSchema,
		)
	}

	// Call LLM for transformation
	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		transformErr := types.TransformError{
			InputShape: types.DescribeValue(input),
			FromType:   fromType.String(),
			ToType:     toType.String(),
			Reason:     err.Error(),
			RequestID:  opt.RequestID,
			Timestamp:  time.Now(),
		}
		log.Error("Transform failed: LLM error",
			"requestID", opt.RequestID,
			"error", transformErr,
		)
		return result, transformErr
	}

	// Parse transformed data
	if err := ParseJSON(response, &result); err != nil {
		transformErr := types.TransformError{
			InputShape: types.DescribeValue(input),
			FromType:   fromType.String(),
			ToType:     toType.String(),
			Reason:     fmt.Sprintf("failed to parse response: %v", err),
			Confidence: 0.5,
			RequestID:  opt.RequestID,
			Timestamp:  time.Now(),
		}
		log.Error("Transform failed: parsing error",
			"requestID", opt.RequestID,
			"error", transformErr,
		)
		return result, transformErr
	}

	log.Info("Transform operation completed",
		"requestID", opt.RequestID,
		"duration", time.Since(startTime),
	)

	return result, nil
}

// Generate creates structured data from natural language prompts.
// It can generate both simple strings and complex structured types.
//
// Type parameter T specifies the type to generate.
//
// Examples:
//
//	// Basic generation
//	product, err := Generate[Product]("Create a laptop listing", NewGenerateOptions())
//
//	// Generation with constraints
//	users, err := Generate[[]User]("Generate test users", NewGenerateOptions().
//	    WithCount(10).
//	    WithConstraints(map[string]interface{}{
//	        "age": "18-65",
//	        "country": "US or Canada",
//	    }).
//	    WithEnsureUnique(true))
//
//	// Generation with template and examples
//	content, err := Generate[BlogPost]("Tech article", NewGenerateOptions().
//	    WithTemplate(articleTemplate).
//	    WithExamples(example1, example2).
//	    WithStyle("technical but accessible"))
//
// The operation understands the target type structure and generates
// appropriate data that conforms to the schema.
func Generate[T any](prompt string, opts GenerateOptions) (T, error) {
	var result T
	log := logger.GetLogger()

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	// Handle batch generation if Count > 1
	if opts.Count > 1 {
		// This would need special handling for slice types
		return result, fmt.Errorf("batch generation not yet supported - use Count=1")
	}

	// Convert to legacy OpOptions
	opt := opts.toOpOptions()

	// Build enhanced prompt
	var promptParts []string
	promptParts = append(promptParts, prompt)

	if opts.Template != "" {
		promptParts = append(promptParts, fmt.Sprintf("Use this template: %s", opts.Template))
	}

	if opts.Style != "" {
		promptParts = append(promptParts, fmt.Sprintf("Style: %s", opts.Style))
	}

	if len(opts.Constraints) > 0 {
		constraints := "Constraints: "
		for key, value := range opts.Constraints {
			constraints += fmt.Sprintf("%s=%v; ", key, value)
		}
		promptParts = append(promptParts, strings.TrimSuffix(constraints, "; "))
	}

	if opts.SeedData != nil {
		seedJSON, _ := json.Marshal(opts.SeedData)
		promptParts = append(promptParts, fmt.Sprintf("Base on this seed data: %s", string(seedJSON)))
	}

	if len(opts.Examples) > 0 {
		examplesJSON, _ := json.Marshal(opts.Examples)
		promptParts = append(promptParts, fmt.Sprintf("Follow these examples: %s", string(examplesJSON)))
	}

	prompt = strings.Join(promptParts, ". ")
	if opts.OpOptions.Steering != "" {
		opt.Steering = opts.OpOptions.Steering
	}

	startTime := time.Now()
	defer func() {
		if config.IsMetricsEnabled() {
			telemetry.RecordMetric("generate_duration", time.Since(startTime).Milliseconds(), map[string]string{
				"type": reflect.TypeOf(result).String(),
				"mode": opt.Mode.String(),
			})
		}
	}()

	log.Info("Generate operation started",
		"requestID", opt.RequestID,
		"targetType", reflect.TypeOf(result).String(),
		"promptLength", len(prompt),
	)

	// Validate prompt
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		err := types.GenerateError{
			PromptShape: types.DescribeValue(prompt),
			TargetType:  reflect.TypeOf(result).String(),
			Reason:      "prompt cannot be empty",
			RequestID:   opt.RequestID,
			Timestamp:   time.Now(),
		}
		log.Error("Generate failed: empty prompt",
			"requestID", opt.RequestID,
			"error", err,
		)
		return result, err
	}

	// Sanitize prompt length
	const maxPromptLength = 10000
	if len(prompt) > maxPromptLength {
		log.Warn("Generate prompt truncated",
			"requestID", opt.RequestID,
			"originalLength", len(prompt),
			"maxLength", maxPromptLength,
		)
		prompt = prompt[:maxPromptLength]
	}

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	targetType := reflect.TypeOf(result)

	// Handle string generation differently (simpler)
	if targetType.Kind() == reflect.String {
		systemPrompt := BuildGenerateStringPrompt(opt.Mode)

		response, err := callLLM(ctx, systemPrompt, prompt, opt)
		if err != nil {
			genErr := types.GenerateError{
				PromptShape: types.DescribeValue(prompt),
				TargetType:  targetType.String(),
				Reason:      err.Error(),
				RequestID:   opt.RequestID,
				Timestamp:   time.Now(),
			}
			log.Error("Generate failed: LLM error",
				"requestID", opt.RequestID,
				"error", genErr,
			)
			return result, genErr
		}

		// Set string result using reflection
		reflect.ValueOf(&result).Elem().SetString(response)

		log.Info("Generate operation completed (string)",
			"requestID", opt.RequestID,
			"duration", time.Since(startTime),
			"responseLength", len(response),
		)
		return result, nil
	}

	// Handle structured type generation
	typeSchema := GenerateTypeSchema(targetType)

	systemPrompt := fmt.Sprintf(`You are a data generation expert. Generate structured data based on the prompt.

Target schema:
%s

Generation rules:
- Generate realistic and coherent data
- Follow the prompt requirements precisely
- Ensure all required fields are populated with appropriate values
- Use sensible defaults where not specified
- Maintain internal consistency (e.g., related fields should make sense together)
- Return ONLY valid JSON matching the schema, no explanations`, typeSchema)

	// Log generation details in debug mode
	if config.GetDebugMode() {
		log.Debug("Generate schema",
			"requestID", opt.RequestID,
			"schema", typeSchema,
			"prompt", prompt[:min(len(prompt), 200)],
		)
	}

	response, err := callLLM(ctx, systemPrompt, prompt, opt)
	if err != nil {
		genErr := types.GenerateError{
			PromptShape: types.DescribeValue(prompt),
			TargetType:  targetType.String(),
			Reason:      err.Error(),
			RequestID:   opt.RequestID,
			Timestamp:   time.Now(),
		}
		log.Error("Generate failed: LLM error",
			"requestID", opt.RequestID,
			"error", genErr,
		)
		return result, genErr
	}

	// Parse generated data
	if err := ParseJSON(response, &result); err != nil {
		genErr := types.GenerateError{
			PromptShape: types.DescribeValue(prompt),
			TargetType:  targetType.String(),
			Reason:      fmt.Sprintf("failed to parse response: %v", err),
			RequestID:   opt.RequestID,
			Timestamp:   time.Now(),
		}
		log.Error("Generate failed: parsing error",
			"requestID", opt.RequestID,
			"error", genErr,
			"response", response[:min(len(response), 200)],
		)
		return result, genErr
	}

	log.Info("Generate operation completed",
		"requestID", opt.RequestID,
		"duration", time.Since(startTime),
	)

	return result, nil
}

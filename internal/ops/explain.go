// package ops - Explain operation for generating human explanations
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ExplainResult contains the explanation results
type ExplainResult struct {
	Explanation string         `json:"explanation"` // The human-readable explanation
	Summary     string         `json:"summary"`     // Brief overview
	KeyPoints   []string       `json:"key_points"`  // Important points to remember
	Audience    string         `json:"audience"`    // Target audience for the explanation
	Complexity  string         `json:"complexity"`  // "simple", "intermediate", "advanced"
	Metadata    map[string]any `json:"metadata"`    // Additional explanation metadata
}

// ExplainOptions configures the Explain operation
type ExplainOptions struct {
	types.OpOptions
	Audience string // Target audience: "technical", "non-technical", "children", "executive", etc.
	Depth    int    // Explanation depth: 1=high-level, 2=moderate, 3=detailed, 4=comprehensive
	Format   string // Output format: "paragraph", "bullet-points", "step-by-step", "qa"
	Context  string // Additional context about the data/code being explained
	Focus    string // Specific aspect to focus on: "overview", "usage", "implementation", etc.
}

// NewExplainOptions creates ExplainOptions with defaults
func NewExplainOptions() ExplainOptions {
	return ExplainOptions{
		OpOptions: types.OpOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Audience: "non-technical",
		Depth:    2,
		Format:   "paragraph",
		Focus:    "overview",
	}
}

// WithAudience sets the target audience
func (opts ExplainOptions) WithAudience(audience string) ExplainOptions {
	opts.Audience = audience
	return opts
}

// WithDepth sets the explanation depth
func (opts ExplainOptions) WithDepth(depth int) ExplainOptions {
	opts.Depth = depth
	return opts
}

// WithFormat sets the output format
func (opts ExplainOptions) WithFormat(format string) ExplainOptions {
	opts.Format = format
	return opts
}

// WithContext sets additional context
func (opts ExplainOptions) WithContext(context string) ExplainOptions {
	opts.Context = context
	return opts
}

// WithFocus sets the focus area
func (opts ExplainOptions) WithFocus(focus string) ExplainOptions {
	opts.Focus = focus
	return opts
}

// WithIntelligence sets the intelligence level
func (opts ExplainOptions) WithIntelligence(intelligence types.Speed) ExplainOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// Validate validates ExplainOptions
func (opts ExplainOptions) Validate() error {
	validAudiences := []string{"technical", "non-technical", "children", "executive", "beginner", "expert"}
	if !contains(validAudiences, opts.Audience) {
		return fmt.Errorf("invalid audience: %s", opts.Audience)
	}

	if opts.Depth < 1 || opts.Depth > 4 {
		return fmt.Errorf("depth must be between 1 and 4, got %d", opts.Depth)
	}

	validFormats := []string{"paragraph", "bullet-points", "step-by-step", "qa", "structured"}
	if !contains(validFormats, opts.Format) {
		return fmt.Errorf("invalid format: %s", opts.Format)
	}

	validFocus := []string{"overview", "usage", "implementation", "benefits", "limitations", "examples"}
	if !contains(validFocus, opts.Focus) {
		return fmt.Errorf("invalid focus: %s", opts.Focus)
	}

	return nil
}

// toOpOptions converts ExplainOptions to types.OpOptions
func (opts ExplainOptions) toOpOptions() types.OpOptions {
	return opts.OpOptions
}

// Explain generates human explanations for complex data or code
//
// Examples:
//
//	// Explain data for non-technical audience
//	result, err := Explain(complexData,
//	    NewExplainOptions().WithAudience("non-technical").WithDepth(3))
//
//	// Explain code implementation details
//	result, err := Explain(codeStructure,
//	    NewExplainOptions().WithAudience("technical").WithFocus("implementation"))
//
//	// Explain business metrics for executives
//	result, err := Explain(metrics,
//	    NewExplainOptions().WithAudience("executive").WithFormat("bullet-points"))
func Explain(data any, opts ExplainOptions) (ExplainResult, error) {
	return explainImpl(data, opts)
}

func explainImpl(data any, opts ExplainOptions) (ExplainResult, error) {
	log := logger.GetLogger()
	log.Debug("Starting explain operation", "requestID", opts.RequestID, "dataType", fmt.Sprintf("%T", data))

	result := ExplainResult{
		Audience:   opts.Audience,
		Complexity: getComplexityLevel(opts.Depth),
		Metadata:   make(map[string]any),
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Explain operation validation failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("invalid options: %w", err)
	}

	// Analyze the data structure
	dataAnalysis, err := analyzeDataForExplanation(data)
	if err != nil {
		log.Error("Explain operation data analysis failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("data analysis failed: %w", err)
	}

	// Generate explanation using LLM
	explanation, err := generateExplanation(data, dataAnalysis, opts)
	if err != nil {
		log.Error("Explain operation explanation generation failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("explanation generation failed: %w", err)
	}

	result.Explanation = explanation.Explanation
	result.Summary = explanation.Summary
	result.KeyPoints = explanation.KeyPoints

	// Add metadata
	result.Metadata["data_type"] = dataAnalysis.DataType
	result.Metadata["field_count"] = dataAnalysis.FieldCount
	result.Metadata["estimated_complexity"] = dataAnalysis.Complexity
	result.Metadata["explanation_depth"] = opts.Depth
	result.Metadata["focus_area"] = opts.Focus

	log.Debug("Explain operation succeeded", "requestID", opts.RequestID, "explanationLength", len(result.Explanation))

	return result, nil
}

// dataAnalysis holds analysis of the data to be explained
type dataAnalysis struct {
	DataType   string
	FieldCount int
	Complexity string
	SampleData string
	Structure  string
}

// analyzeDataForExplanation analyzes the data structure for explanation generation
func analyzeDataForExplanation(data any) (dataAnalysis, error) {
	analysis := dataAnalysis{}

	// Handle nil data
	if data == nil {
		return analysis, fmt.Errorf("cannot explain nil data")
	}

	v := reflect.ValueOf(data)
	t := reflect.TypeOf(data)

	// Handle pointers
	if t.Kind() == reflect.Ptr {
		if v.IsNil() {
			return analysis, fmt.Errorf("cannot explain nil pointer")
		}
		v = v.Elem()
		t = t.Elem()
	}

	analysis.DataType = t.String()

	switch t.Kind() {
	case reflect.Struct:
		analysis.FieldCount = t.NumField()
		analysis.Structure = "struct"
		if analysis.FieldCount > 10 {
			analysis.Complexity = "high"
		} else if analysis.FieldCount > 5 {
			analysis.Complexity = "medium"
		} else {
			analysis.Complexity = "low"
		}
	case reflect.Slice, reflect.Array:
		analysis.FieldCount = v.Len()
		analysis.Structure = "collection"
		analysis.Complexity = "medium"
	case reflect.Map:
		analysis.FieldCount = v.Len()
		analysis.Structure = "key-value"
		analysis.Complexity = "medium"
	default:
		analysis.FieldCount = 1
		analysis.Structure = "primitive"
		analysis.Complexity = "low"
	}

	// Generate sample data representation
	if jsonData, err := json.MarshalIndent(data, "", "  "); err == nil {
		analysis.SampleData = string(jsonData)
	}

	return analysis, nil
}

// explanationResponse holds the LLM-generated explanation
type explanationResponse struct {
	Explanation string   `json:"explanation"`
	Summary     string   `json:"summary"`
	KeyPoints   []string `json:"key_points"`
}

// generateExplanation uses LLM to create a human explanation
func generateExplanation(data any, analysis dataAnalysis, opts ExplainOptions) (explanationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.GetTimeout())
	defer cancel()

	// Marshal data for prompt
	dataJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return explanationResponse{}, fmt.Errorf("failed to marshal data: %w", err)
	}

	// Build system prompt based on audience and format
	systemPrompt := buildSystemPrompt(opts)

	// Build user prompt
	userPrompt := buildUserPrompt(dataJSON, analysis, opts)

	// Call LLM for explanation
	opt := opts.toOpOptions()

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		return explanationResponse{}, fmt.Errorf("explanation generation failed: %w", err)
	}

	// Parse the response
	var result explanationResponse
	if err := ParseJSONStrict(response, &result); err != nil {
		// Treating an unparseable body as the explanation manufactured a
		// result out of a refusal or an error page, complete with a key point
		// reading "Explanation generated".
		return explanationResponse{}, fmt.Errorf("the explanation could not be parsed: %w", err)
	}

	return result, nil
}

// buildSystemPrompt creates the system prompt based on options
func buildSystemPrompt(opts ExplainOptions) string {
	var prompt strings.Builder

	prompt.WriteString("You are an expert at explaining complex concepts in simple, understandable terms.\n\n")

	// Audience-specific instructions
	switch opts.Audience {
	case "children":
		prompt.WriteString("Explain this as if speaking to a curious child (ages 8-12). Use simple words, fun analogies, and avoid technical jargon.\n")
	case "non-technical":
		prompt.WriteString("Explain this for someone without technical background. Use everyday language, avoid acronyms, and relate to familiar concepts.\n")
	case "executive":
		prompt.WriteString("Explain this for business executives. Focus on business impact, strategic value, and high-level implications.\n")
	case "beginner":
		prompt.WriteString("Explain this for complete beginners. Start with basics and build understanding step by step.\n")
	case "technical":
		prompt.WriteString("Provide a technical explanation with appropriate detail, terminology, and implementation considerations.\n")
	case "expert":
		prompt.WriteString("Provide a detailed, technical explanation for domain experts, including advanced concepts and nuances.\n")
	}

	// Format-specific instructions
	switch opts.Format {
	case "paragraph":
		prompt.WriteString("Provide a cohesive paragraph explanation.\n")
	case "bullet-points":
		prompt.WriteString("Structure your response as clear bullet points.\n")
	case "step-by-step":
		prompt.WriteString("Break down the explanation into numbered steps.\n")
	case "qa":
		prompt.WriteString("Format as a Q&A with common questions and answers.\n")
	case "structured":
		prompt.WriteString("Use structured sections with clear headings.\n")
	}

	// Depth-specific instructions
	switch opts.Depth {
	case 1:
		prompt.WriteString("Keep it very high-level and brief.\n")
	case 2:
		prompt.WriteString("Provide moderate detail with key concepts.\n")
	case 3:
		prompt.WriteString("Include good detail and examples.\n")
	case 4:
		prompt.WriteString("Be comprehensive with full technical depth.\n")
	}

	prompt.WriteString("\nAlways provide:\n1. A clear explanation\n2. A brief summary\n3. Key points as an array\n\nReturn your response as valid JSON with 'explanation', 'summary', and 'key_points' fields.")

	return prompt.String()
}

// buildUserPrompt creates the user prompt with data and context
func buildUserPrompt(dataJSON []byte, analysis dataAnalysis, opts ExplainOptions) string {
	var prompt strings.Builder

	prompt.WriteString("Please explain the following data:\n\n")
	prompt.WriteString("DATA:\n")
	prompt.Write(dataJSON)
	prompt.WriteString("\n\n")

	prompt.WriteString(fmt.Sprintf("DATA ANALYSIS:\n- Type: %s\n- Structure: %s\n- Fields/Items: %d\n- Estimated Complexity: %s\n\n",
		analysis.DataType, analysis.Structure, analysis.FieldCount, analysis.Complexity))

	if opts.Context != "" {
		prompt.WriteString(fmt.Sprintf("ADDITIONAL CONTEXT: %s\n\n", opts.Context))
	}

	prompt.WriteString(fmt.Sprintf("FOCUS AREA: %s\n", opts.Focus))

	return prompt.String()
}

// getComplexityLevel converts depth to complexity string
func getComplexityLevel(depth int) string {
	switch depth {
	case 1:
		return "simple"
	case 2:
		return "intermediate"
	case 3:
		return "detailed"
	case 4:
		return "comprehensive"
	default:
		return "intermediate"
	}
}

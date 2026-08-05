// package ops - Annotate operation for adding metadata/labels to content
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

// AnnotateOptions configures the Annotate operation
type AnnotateOptions struct {
	CommonOptions
	types.OpOptions

	// Types of annotations to add (e.g., "entities", "sentiment", "topics", "keywords")
	AnnotationTypes []string

	// Custom annotation schema
	CustomSchema map[string]string

	// Include confidence scores for annotations
	IncludeConfidence bool

	// Minimum confidence threshold for annotations
	MinConfidence float64

	// Annotation format ("inline", "standoff", "structured")
	Format string

	// Language for annotation (auto-detect if empty)
	Language string

	// Domain-specific context for better annotation
	Domain string
}

// NewAnnotateOptions creates AnnotateOptions with defaults
func NewAnnotateOptions() AnnotateOptions {
	return AnnotateOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		AnnotationTypes:   []string{"entities", "sentiment"},
		IncludeConfidence: true,
		MinConfidence:     0.5,
		Format:            "structured",
	}
}

// Validate validates AnnotateOptions
func (a AnnotateOptions) Validate() error {
	if err := a.CommonOptions.Validate(); err != nil {
		return err
	}
	if len(a.AnnotationTypes) == 0 && len(a.CustomSchema) == 0 {
		return fmt.Errorf("at least one annotation type or custom schema is required")
	}
	if a.MinConfidence < 0 || a.MinConfidence > 1 {
		return fmt.Errorf("min confidence must be between 0 and 1, got %f", a.MinConfidence)
	}
	validFormats := map[string]bool{"inline": true, "standoff": true, "structured": true}
	if a.Format != "" && !validFormats[a.Format] {
		return fmt.Errorf("invalid format: %s", a.Format)
	}
	return nil
}

// WithAnnotationTypes sets the annotation types
func (a AnnotateOptions) WithAnnotationTypes(types []string) AnnotateOptions {
	a.AnnotationTypes = types
	return a
}

// WithCustomSchema sets a custom annotation schema
func (a AnnotateOptions) WithCustomSchema(schema map[string]string) AnnotateOptions {
	a.CustomSchema = schema
	return a
}

// WithIncludeConfidence enables confidence scores
func (a AnnotateOptions) WithIncludeConfidence(include bool) AnnotateOptions {
	a.IncludeConfidence = include
	return a
}

// WithMinConfidence sets the minimum confidence threshold
func (a AnnotateOptions) WithMinConfidence(confidence float64) AnnotateOptions {
	a.MinConfidence = confidence
	return a
}

// WithFormat sets the annotation format
func (a AnnotateOptions) WithFormat(format string) AnnotateOptions {
	a.Format = format
	return a
}

// WithDomain sets the domain context
func (a AnnotateOptions) WithDomain(domain string) AnnotateOptions {
	a.Domain = domain
	return a
}

// WithSteering sets the steering prompt
func (a AnnotateOptions) WithSteering(steering string) AnnotateOptions {
	a.CommonOptions = a.CommonOptions.WithSteering(steering)
	return a
}

// WithMode sets the mode
func (a AnnotateOptions) WithMode(mode types.Mode) AnnotateOptions {
	a.CommonOptions = a.CommonOptions.WithMode(mode)
	return a
}

// WithIntelligence sets the intelligence level
func (a AnnotateOptions) WithIntelligence(intelligence types.Speed) AnnotateOptions {
	a.CommonOptions = a.CommonOptions.WithIntelligence(intelligence)
	return a
}

func (a AnnotateOptions) toOpOptions() types.OpOptions {
	return a.CommonOptions.toOpOptions()
}

// Annotation represents a single annotation
type Annotation struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Text  string `json:"text,omitempty"`
	Start int    `json:"start,omitempty"`
	End   int    `json:"end,omitempty"`
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64        `json:"confidence,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// AnnotateResult contains the results of annotation
type AnnotateResult struct {
	Original    string                  `json:"original"`
	Annotations []Annotation            `json:"annotations"`
	ByType      map[string][]Annotation `json:"by_type"`
	Summary     map[string]any          `json:"summary,omitempty"`
}

// Annotate adds metadata, labels, or tags to content using LLM intelligence.
// It can identify entities, sentiment, topics, keywords, and custom annotations.
//
// Type parameter T specifies the input type.
//
// Examples:
//
//	// Basic entity and sentiment annotation
//	result, err := Annotate(text, NewAnnotateOptions().
//	    WithAnnotationTypes([]string{"entities", "sentiment", "topics"}))
//
//	// Custom annotation schema
//	result, err := Annotate(document, NewAnnotateOptions().
//	    WithCustomSchema(map[string]string{
//	        "risks": "identify potential risks mentioned",
//	        "actions": "extract action items",
//	    }))
//
//	// Domain-specific annotation
//	result, err := Annotate(medicalText, NewAnnotateOptions().
//	    WithAnnotationTypes([]string{"entities"}).
//	    WithDomain("medical"))
func Annotate[T any](input T, opts AnnotateOptions) (AnnotateResult, error) {
	log := logger.GetLogger()
	log.Debug("Starting annotate operation")

	var result AnnotateResult
	result.ByType = make(map[string][]Annotation)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert input to string
	inputStr, err := NormalizeInput(input)
	if err != nil {
		log.Error("Annotate operation failed: input normalization error", "error", err)
		return result, fmt.Errorf("failed to normalize input: %w", err)
	}
	result.Original = inputStr

	// Build annotation type descriptions
	var annotationDesc []string
	for _, aType := range opts.AnnotationTypes {
		switch aType {
		case "entities":
			annotationDesc = append(annotationDesc, "entities: Identify named entities (persons, organizations, locations, dates, etc.)")
		case "sentiment":
			annotationDesc = append(annotationDesc, "sentiment: Determine overall sentiment and emotional tone")
		case "topics":
			annotationDesc = append(annotationDesc, "topics: Extract main topics and themes")
		case "keywords":
			annotationDesc = append(annotationDesc, "keywords: Identify important keywords and phrases")
		case "intent":
			annotationDesc = append(annotationDesc, "intent: Determine the intent or purpose of the text")
		case "language":
			annotationDesc = append(annotationDesc, "language: Detect the language and dialect")
		default:
			annotationDesc = append(annotationDesc, fmt.Sprintf("%s: Identify %s", aType, aType))
		}
	}

	// Add custom schema descriptions
	for key, desc := range opts.CustomSchema {
		annotationDesc = append(annotationDesc, fmt.Sprintf("%s: %s", key, desc))
	}

	domainContext := ""
	if opts.Domain != "" {
		domainContext = fmt.Sprintf("\nDomain context: %s", opts.Domain)
	}

	confidenceNote := ""
	if opts.IncludeConfidence {
		confidenceNote = fmt.Sprintf("\nInclude confidence scores (0.0-1.0) for each annotation. Only include annotations with confidence >= %.2f.", opts.MinConfidence)
	}

	systemPrompt := fmt.Sprintf(`You are an expert text annotator. Analyze the input and add annotations.%s%s

Annotation types to identify:
%s

Return a JSON object with:
{
  "annotations": [
    {
      "type": "entity|sentiment|topic|...",
      "value": "the annotation value",
      "text": "the text span being annotated (if applicable)",
      "start": 0,
      "end": 10,
      "confidence": 0.95,
      "metadata": {}
    }
  ],
  "summary": {
    "overall_sentiment": "positive|negative|neutral",
    "main_topics": ["topic1", "topic2"],
    "entity_count": 5
  }
}`, domainContext, confidenceNote, strings.Join(annotationDesc, "\n"))

	userPrompt := fmt.Sprintf("Annotate this text:\n\n%s", inputStr)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Annotate operation LLM call failed", "error", err)
		return result, fmt.Errorf("annotation failed: %w", err)
	}

	// Parse the response
	var parsed struct {
		Annotations []Annotation   `json:"annotations"`
		Summary     map[string]any `json:"summary"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Annotate operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse annotations: %w", err)
	}

	// Filter by confidence if needed
	for _, ann := range parsed.Annotations {
		if opts.IncludeConfidence && ann.ModelConfidence < opts.MinConfidence {
			continue
		}
		result.Annotations = append(result.Annotations, ann)
		result.ByType[ann.Type] = append(result.ByType[ann.Type], ann)
	}

	result.Summary = parsed.Summary

	log.Debug("Annotate operation succeeded", "annotationCount", len(result.Annotations))
	return result, nil
}

// AnnotateStruct adds annotations as new fields to a struct
// It returns the original struct with additional annotation fields
func AnnotateStruct[T any, U any](input T, opts AnnotateOptions) (U, error) {
	log := logger.GetLogger()
	log.Debug("Starting annotate struct operation")

	var result U

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Get type information
	inputType := reflect.TypeOf(input)
	outputType := reflect.TypeOf(result)

	inputSchema := GenerateTypeSchema(inputType)
	outputSchema := GenerateTypeSchema(outputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("AnnotateStruct failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Build annotation descriptions
	var annotationDesc []string
	for _, aType := range opts.AnnotationTypes {
		annotationDesc = append(annotationDesc, aType)
	}
	for key := range opts.CustomSchema {
		annotationDesc = append(annotationDesc, key)
	}

	systemPrompt := fmt.Sprintf(`You are an expert data annotator. Take the input data and produce an annotated version with additional fields.

Input schema:
%s

Output schema (includes annotation fields):
%s

Add these annotations: %s

Return only valid JSON matching the output schema.`, inputSchema, outputSchema, strings.Join(annotationDesc, ", "))

	userPrompt := fmt.Sprintf("Annotate this data:\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("AnnotateStruct LLM call failed", "error", err)
		return result, fmt.Errorf("annotation failed: %w", err)
	}

	if err := ParseJSONStrict(response, &result); err != nil {
		log.Error("AnnotateStruct failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse annotated result: %w", err)
	}

	log.Debug("AnnotateStruct operation succeeded")
	return result, nil
}

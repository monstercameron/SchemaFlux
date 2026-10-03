package ops

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

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

	// Annotation format ("inline", "standoff", "structured"). It is validated
	// and read by nothing else: the result is the same for every value.
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
	if !(a.MinConfidence >= 0 && a.MinConfidence <= 1) {
		return fmt.Errorf("min confidence must be between 0 and 1, got %f", a.MinConfidence)
	}
	validFormats := map[string]bool{"inline": true, "standoff": true, "structured": true}
	if a.Format != "" && !validFormats[a.Format] {
		return fmt.Errorf("invalid format: %s", a.Format)
	}
	return nil
}

// WithAnnotationTypes sets the annotation types
func (a AnnotateOptions) WithAnnotationTypes(kinds []string) AnnotateOptions {
	a.AnnotationTypes = kinds
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
	return mergeEmbeddedOpOptions(a.CommonOptions, a.OpOptions)
}

func (a AnnotateOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return a.CommonOptions, a.OpOptions
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

// annotationAnswer is one annotation as the prompt asks the model for it, and the
// type each element of the answer's list is read into. A member the prompt
// always shows is required of every annotation, and an answer that leaves one
// out is refused with the annotation's position: reading a missing start or end
// as 0 is a span the model never gave, and a missing value as "" is a label it
// never gave.
//
// Three members are left out of that, each for a reason the prompt gives or an
// option decides:
//
//   - text is "the text span being annotated (if applicable)", so there are
//     annotations that have none;
//   - confidence is asked for only when IncludeConfidence is set, and Annotate
//     refuses an annotation without one then, which the type cannot say;
//   - metadata is shown as an object that may be empty, nothing reads it, and an
//     annotation without it has lost nothing.
type annotationAnswer struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Text  string `json:"text,omitempty"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	// ModelConfidence is a pointer so that a missing one is told from zero.
	ModelConfidence *float64       `json:"confidence,omitempty"`
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
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting annotate operation")

	// Validate options
	if err := opts.Validate(); err != nil {
		return AnnotateResult{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Convert input to string
	inputStr, err := NormalizeInput(input)
	if err != nil {
		log.Error("Annotate operation failed: input normalization error", "error", err)
		return AnnotateResult{}, types.Newf(types.KindInvalidRequest, "annotate", "the input could not be turned into text")
	}

	// Anything the caller wrote that goes into the prompt goes in as a JSON
	// string, so a Domain, a Language or a schema entry that reads like an
	// instruction is a quoted value. A value of an option that cannot be written
	// is a configuration error, and the text to annotate, which is the call's
	// argument, an invalid request.
	quoteAs := func(kind types.ErrorKind, what, text string) (string, error) {
		raw, quoteErr := promptJSON(text)
		if quoteErr != nil {
			return "", types.Newf(kind, "annotate", "%s could not be written as JSON", what)
		}
		return string(raw), nil
	}
	quote := func(text string) (string, error) {
		return quoteAs(types.KindConfiguration, "an option", text)
	}

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
			name, quoteErr := quote(aType)
			if quoteErr != nil {
				return AnnotateResult{}, quoteErr
			}
			annotationDesc = append(annotationDesc, fmt.Sprintf("%s: Identify %s", name, name))
		}
	}

	// Add custom schema descriptions, in a fixed order: they are rendered into
	// the prompt, and a different order on every call defeats prefix caching.
	for _, key := range sortedKeys(opts.CustomSchema) {
		name, quoteErr := quote(key)
		if quoteErr != nil {
			return AnnotateResult{}, quoteErr
		}
		desc, quoteErr := quote(opts.CustomSchema[key])
		if quoteErr != nil {
			return AnnotateResult{}, quoteErr
		}
		annotationDesc = append(annotationDesc, fmt.Sprintf("%s: %s", name, desc))
	}

	domainContext := ""
	if opts.Domain != "" {
		domain, quoteErr := quote(opts.Domain)
		if quoteErr != nil {
			return AnnotateResult{}, quoteErr
		}
		domainContext = fmt.Sprintf("\nDomain context: %s", domain)
	}

	if opts.Language != "" {
		language, quoteErr := quote(opts.Language)
		if quoteErr != nil {
			return AnnotateResult{}, quoteErr
		}
		domainContext += fmt.Sprintf("\nThe text is in %s; annotate in that language and do not translate it.", language)
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

	// The text is sent as a JSON string, so a line of it cannot pass for a
	// line of the request.
	quotedInput, err := quoteAs(types.KindInvalidRequest, "the input", inputStr)
	if err != nil {
		return AnnotateResult{}, err
	}
	userPrompt := fmt.Sprintf("Annotate this text:\n\n%s", quotedInput)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Annotate operation LLM call failed", "error", err)
		return AnnotateResult{}, fmt.Errorf("annotation failed: %w", err)
	}

	// Parse the response. The prompt shows both members, so both are there, and
	// each annotation of the list is held to annotationAnswer, which names a
	// member that is missing by the position of the annotation. A list that is
	// null is told from an empty one, which is an answer, below.
	var parsed struct {
		Annotations []annotationAnswer `json:"annotations"`
		Summary     map[string]any     `json:"summary"`
	}

	refuse := func(kind types.ErrorKind, format string, args ...any) (AnnotateResult, error) {
		return AnnotateResult{}, types.Newf(kind, "annotate", format, args...)
	}

	if err := decodeAnswer("annotate", response, &parsed); err != nil {
		log.Error("Annotate operation failed: parse error", "error", err)
		return AnnotateResult{}, err
	}
	// A null list reads as a nil slice and an empty one as a slice that is not.
	if parsed.Annotations == nil {
		return refuse(types.KindSchemaViolation, "the response has no list of annotations")
	}

	// Each annotation is checked as it is read: it names a type, the span it
	// gives lies inside the text (counted in characters), and the confidence it
	// reports, when confidence was asked for, is there and on the 0 to 1 scale.
	//
	// Whether the type is one of the requested ones is not checked: the prompt's
	// own example names the types in the singular ("entity") where the options
	// name them in the plural ("entities"), so the answer cannot be held to the
	// option's spelling until that text is changed.
	runes := utf8.RuneCountInString(inputStr)
	var kept []Annotation
	for position, ann := range parsed.Annotations {
		if strings.TrimSpace(ann.Type) == "" {
			return refuse(types.KindSchemaViolation, "annotations[%d] has no type", position)
		}
		if ann.Start < 0 || ann.End < ann.Start || ann.End > runes {
			return refuse(types.KindInvariantViolation, "annotations[%d] has a span outside the text", position)
		}
		annotation := Annotation{
			Type:     ann.Type,
			Value:    ann.Value,
			Text:     ann.Text,
			Start:    ann.Start,
			End:      ann.End,
			Metadata: ann.Metadata,
		}
		if opts.IncludeConfidence {
			if ann.ModelConfidence == nil {
				return refuse(types.KindSchemaViolation, "annotations[%d].confidence is missing, and confidence was asked for", position)
			}
			if scaleErr := onScale("annotate", "the confidence of an annotation", *ann.ModelConfidence); scaleErr != nil {
				return refuse(types.KindOf(scaleErr), "the confidence of annotations[%d] is outside the 0 to 1 scale", position)
			}
			annotation.ModelConfidence = *ann.ModelConfidence
			// Filter by confidence: below the floor is left out, not refused.
			if annotation.ModelConfidence < opts.MinConfidence {
				continue
			}
		} else if ann.ModelConfidence != nil {
			annotation.ModelConfidence = *ann.ModelConfidence
		}
		kept = append(kept, annotation)
	}

	result := AnnotateResult{
		Original: inputStr,
		ByType:   make(map[string][]Annotation),
		Summary:  parsed.Summary,
	}
	for _, annotation := range kept {
		result.Annotations = append(result.Annotations, annotation)
		result.ByType[annotation.Type] = append(result.ByType[annotation.Type], annotation)
	}

	log.Debug("Annotate operation succeeded", "annotationCount", len(result.Annotations))
	return result, nil
}

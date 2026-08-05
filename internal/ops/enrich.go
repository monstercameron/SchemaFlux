// package ops - Enrich operation for adding derived/inferred fields to data
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

// EnrichOptions configures the Enrich operation
type EnrichOptions struct {
	CommonOptions
	types.OpOptions

	// Fields to derive/add
	DeriveFields []string

	// Field derivation rules (field -> how to derive it)
	DerivationRules map[string]string

	// External context to use for enrichment
	Context map[string]any

	// Include confidence scores for derived fields
	IncludeConfidence bool

	// Only add fields, don't modify existing
	AddOnly bool

	// Fields to exclude from enrichment
	ExcludeFields []string

	// Domain for domain-specific enrichment
	Domain string

	// Enrichment depth ("shallow", "deep")
	Depth string
}

// NewEnrichOptions creates EnrichOptions with defaults
func NewEnrichOptions() EnrichOptions {
	return EnrichOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		IncludeConfidence: false,
		AddOnly:           true,
		Depth:             "shallow",
	}
}

// Validate validates EnrichOptions
func (e EnrichOptions) Validate() error {
	if err := e.CommonOptions.Validate(); err != nil {
		return err
	}
	validDepths := map[string]bool{"shallow": true, "deep": true}
	if e.Depth != "" && !validDepths[e.Depth] {
		return fmt.Errorf("invalid depth: %s", e.Depth)
	}
	return nil
}

// WithDeriveFields sets fields to derive
func (e EnrichOptions) WithDeriveFields(fields []string) EnrichOptions {
	e.DeriveFields = fields
	return e
}

// WithDerivationRules sets derivation rules
func (e EnrichOptions) WithDerivationRules(rules map[string]string) EnrichOptions {
	e.DerivationRules = rules
	return e
}

// WithContext sets external context for enrichment
func (e EnrichOptions) WithContext(ctx map[string]any) EnrichOptions {
	e.Context = ctx
	return e
}

// WithIncludeConfidence enables confidence scores
func (e EnrichOptions) WithIncludeConfidence(include bool) EnrichOptions {
	e.IncludeConfidence = include
	return e
}

// WithAddOnly only adds fields, doesn't modify existing
func (e EnrichOptions) WithAddOnly(addOnly bool) EnrichOptions {
	e.AddOnly = addOnly
	return e
}

// WithExcludeFields sets fields to exclude from enrichment
func (e EnrichOptions) WithExcludeFields(fields []string) EnrichOptions {
	e.ExcludeFields = fields
	return e
}

// WithDomain sets the domain for enrichment
func (e EnrichOptions) WithDomain(domain string) EnrichOptions {
	e.Domain = domain
	return e
}

// WithDepth sets the enrichment depth
func (e EnrichOptions) WithDepth(depth string) EnrichOptions {
	e.Depth = depth
	return e
}

// WithSteering sets the steering prompt
func (e EnrichOptions) WithSteering(steering string) EnrichOptions {
	e.CommonOptions = e.CommonOptions.WithSteering(steering)
	return e
}

// WithMode sets the mode
func (e EnrichOptions) WithMode(mode types.Mode) EnrichOptions {
	e.CommonOptions = e.CommonOptions.WithMode(mode)
	return e
}

// WithIntelligence sets the intelligence level
func (e EnrichOptions) WithIntelligence(intelligence types.Speed) EnrichOptions {
	e.CommonOptions = e.CommonOptions.WithIntelligence(intelligence)
	return e
}

func (e EnrichOptions) toOpOptions() types.OpOptions {
	return e.CommonOptions.toOpOptions()
}

// EnrichResult contains the enriched data and metadata
type EnrichResult[T any] struct {
	Enriched    T                  `json:"enriched"`
	AddedFields []string           `json:"added_fields"`
	Confidence  map[string]float64 `json:"confidence,omitempty"`
	Derivations map[string]string  `json:"derivations,omitempty"`
	Metadata    map[string]any     `json:"metadata,omitempty"`
}

// Enrich adds derived or inferred fields to data using LLM intelligence.
// It can infer missing data, calculate derived fields, and add context.
//
// Type parameter T specifies the input type.
// Type parameter U specifies the enriched output type (can have additional fields).
//
// Examples:
//
//	// Enrich customer data with inferred fields
//	result, err := Enrich[Customer, EnrichedCustomer](customer, NewEnrichOptions().
//	    WithDeriveFields([]string{"risk_score", "segment", "lifetime_value"}))
//
//	// Enrich with specific rules
//	result, err := Enrich[Product, EnrichedProduct](product, NewEnrichOptions().
//	    WithDerivationRules(map[string]string{
//	        "category": "infer from product name and description",
//	        "price_tier": "low/medium/high based on price",
//	    }))
//
//	// Domain-specific enrichment
//	result, err := Enrich[Article, AnalyzedArticle](article, NewEnrichOptions().
//	    WithDomain("news").
//	    WithDeriveFields([]string{"bias", "credibility", "topics"}))
func Enrich[T any, U any](input T, opts EnrichOptions) (EnrichResult[U], error) {
	log := logger.GetLogger()
	log.Debug("Starting enrich operation")

	var result EnrichResult[U]
	result.Confidence = make(map[string]float64)
	result.Derivations = make(map[string]string)
	result.Metadata = make(map[string]any)

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
	var outputSample U
	outputType := reflect.TypeOf(outputSample)

	inputSchema := GenerateTypeSchema(inputType)
	outputSchema := GenerateTypeSchema(outputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Enrich operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Build derivation instructions
	var derivationInstructions []string
	for _, field := range opts.DeriveFields {
		derivationInstructions = append(derivationInstructions, fmt.Sprintf("- %s: infer from available data", field))
	}
	for field, rule := range opts.DerivationRules {
		derivationInstructions = append(derivationInstructions, fmt.Sprintf("- %s: %s", field, rule))
	}
	if len(derivationInstructions) == 0 {
		derivationInstructions = append(derivationInstructions, "- infer the most useful missing or derived fields from the available data")
	}

	contextDesc := ""
	if len(opts.Context) > 0 {
		contextJSON, _ := json.Marshal(opts.Context)
		contextDesc = fmt.Sprintf("\nAdditional context for enrichment:\n%s", string(contextJSON))
	}

	domainDesc := ""
	if opts.Domain != "" {
		domainDesc = fmt.Sprintf("\nDomain: %s (use domain-specific knowledge)", opts.Domain)
	}

	excludeDesc := ""
	if len(opts.ExcludeFields) > 0 {
		excludeDesc = fmt.Sprintf("\nDo not modify these fields: %s", strings.Join(opts.ExcludeFields, ", "))
	}

	addOnlyDesc := ""
	if opts.AddOnly {
		addOnlyDesc = "\nOnly add new fields, do not modify existing field values."
	}

	confidenceNote := ""
	if opts.IncludeConfidence {
		confidenceNote = "\nInclude confidence scores (0.0-1.0) for each derived field."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at data enrichment and inference. Add derived fields to the input data.

Input schema:
%s

Output schema (enriched):
%s

Fields to derive:
%s%s%s%s%s%s

Return a JSON object with:
{
  "enriched": <the enriched data matching output schema>,
  "added_fields": ["list of fields that were added"],
  "confidence": {"field": 0.95},
  "derivations": {"field": "how it was derived"}
}`, inputSchema, outputSchema, strings.Join(derivationInstructions, "\n"), contextDesc, domainDesc, excludeDesc, addOnlyDesc, confidenceNote)

	userPrompt := fmt.Sprintf("Enrich this data:\n\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Enrich operation LLM call failed", "error", err)
		return result, fmt.Errorf("enrichment failed: %w", err)
	}

	// Parse the response
	var parsed struct {
		Enriched    U                  `json:"enriched"`
		AddedFields []string           `json:"added_fields"`
		Confidence  map[string]float64 `json:"confidence"`
		Derivations map[string]string  `json:"derivations"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Enrich operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse enriched result: %w", err)
	}

	result.Enriched = parsed.Enriched
	result.AddedFields = parsed.AddedFields
	result.Confidence = parsed.Confidence
	result.Derivations = parsed.Derivations

	log.Debug("Enrich operation succeeded", "addedFields", len(result.AddedFields))
	return result, nil
}

// EnrichInPlace enriches data without changing the type (adds to map or fills empty fields)
func EnrichInPlace[T any](input T, opts EnrichOptions) (T, error) {
	log := logger.GetLogger()
	log.Debug("Starting enrich in-place operation")

	var result T

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
	typeSchema := GenerateTypeSchema(inputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("EnrichInPlace failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Build derivation instructions
	var derivationInstructions []string
	for _, field := range opts.DeriveFields {
		derivationInstructions = append(derivationInstructions, fmt.Sprintf("- %s: infer from available data", field))
	}
	for field, rule := range opts.DerivationRules {
		derivationInstructions = append(derivationInstructions, fmt.Sprintf("- %s: %s", field, rule))
	}
	if len(derivationInstructions) == 0 {
		derivationInstructions = append(derivationInstructions, "- infer the most useful missing or derived fields from the available data")
	}

	systemPrompt := fmt.Sprintf(`You are an expert at data enrichment. Fill in or enhance fields in the input data.

Schema:
%s

Fields to derive/fill:
%s

Return ONLY the enriched data matching the schema (no wrapper object).`, typeSchema, strings.Join(derivationInstructions, "\n"))

	userPrompt := fmt.Sprintf("Enrich this data:\n\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("EnrichInPlace LLM call failed", "error", err)
		return result, fmt.Errorf("enrichment failed: %w", err)
	}

	if err := ParseJSONStrict(response, &result); err != nil {
		log.Error("EnrichInPlace failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse enriched result: %w", err)
	}

	log.Debug("EnrichInPlace succeeded")
	return result, nil
}

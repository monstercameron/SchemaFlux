// package ops - Specialized options for each LLM operation
package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
)

// BaseOptions defines the common interface for all operation options
type BaseOptions interface {
	GetSteering() string
	GetThreshold() float64
	GetMode() types.Mode
	GetIntelligence() types.Speed
	GetContext() context.Context
	GetRequestID() string
	GetCorrelationID() string
	Validate() error
	toOpOptions() types.OpOptions
}

// CommonOptions contains fields shared by all operation options
type CommonOptions struct {
	// Natural language guidance for the operation
	Steering string

	// Minimum confidence threshold (0.0-1.0)
	Threshold float64

	// Reasoning approach (Strict/Transform/Creative)
	Mode types.Mode

	// Quality/speed tradeoff (Smart/Fast/Quick)
	Intelligence types.Speed

	// Context for cancellation
	Context context.Context

	// Internal fields
	RequestID     string
	CorrelationID string
}

// NewCommonOptions creates reusable default common options.
func NewCommonOptions() CommonOptions {
	return CommonOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
}

// GetSteering returns the steering prompt
func (c CommonOptions) GetSteering() string {
	return c.Steering
}

// GetThreshold returns the confidence threshold
func (c CommonOptions) GetThreshold() float64 {
	return c.Threshold
}

// GetMode returns the reasoning mode
func (c CommonOptions) GetMode() types.Mode {
	return c.Mode
}

// GetIntelligence returns the intelligence speed
func (c CommonOptions) GetIntelligence() types.Speed {
	return c.Intelligence
}

// GetContext returns the context
func (c CommonOptions) GetContext() context.Context {
	if c.Context == nil {
		return context.Background()
	}
	return c.Context
}

// GetRequestID returns the request ID
func (c CommonOptions) GetRequestID() string {
	return c.RequestID
}

// GetCorrelationID returns the correlation ID.
func (c CommonOptions) GetCorrelationID() string {
	return c.CorrelationID
}

// Validate performs basic validation on common options
func (c CommonOptions) Validate() error {
	if c.Threshold < 0 || c.Threshold > 1 {
		return fmt.Errorf("threshold must be between 0 and 1, got %f", c.Threshold)
	}
	return nil
}

// toOpOptions converts to legacy OpOptions for backward compatibility
func (c CommonOptions) toOpOptions() types.OpOptions {
	ctx, tracking := requesttracking.Ensure(c.GetContext(), c.RequestID, c.CorrelationID)
	return types.OpOptions{
		Steering:      c.Steering,
		Threshold:     c.Threshold,
		Mode:          c.Mode,
		Intelligence:  c.Intelligence,
		Context:       ctx,
		RequestID:     tracking.RequestID,
		CorrelationID: tracking.CorrelationID,
	}
}

// WithSteering sets the steering prompt
func (c CommonOptions) WithSteering(steering string) CommonOptions {
	c.Steering = steering
	return c
}

// WithThreshold sets the confidence threshold
func (c CommonOptions) WithThreshold(threshold float64) CommonOptions {
	c.Threshold = threshold
	return c
}

// WithMode sets the reasoning mode
func (c CommonOptions) WithMode(mode types.Mode) CommonOptions {
	c.Mode = mode
	return c
}

// WithIntelligence sets the intelligence speed
func (c CommonOptions) WithIntelligence(intelligence types.Speed) CommonOptions {
	c.Intelligence = intelligence
	return c
}

// WithContext sets the context
func (c CommonOptions) WithContext(ctx context.Context) CommonOptions {
	c.Context = ctx
	return c
}

// WithRequestID sets the request ID for tracing.
func (c CommonOptions) WithRequestID(requestID string) CommonOptions {
	c.RequestID = requestID
	return c
}

// WithCorrelationID sets the correlation ID for grouping related requests.
func (c CommonOptions) WithCorrelationID(correlationID string) CommonOptions {
	c.CorrelationID = correlationID
	return c
}

// ========================================
// Data Operation Options
// ========================================

// ExtractOptions configures the Extract operation
type ExtractOptions struct {
	CommonOptions
	types.OpOptions

	// Schema hints to guide extraction
	SchemaHints map[string]string

	// Enforce strict schema validation
	StrictSchema bool

	// Allow partial extraction if some fields missing
	AllowPartial bool

	// Examples of expected output format
	Examples []interface{}

	// Field-specific extraction rules
	FieldRules map[string]string
}

// NewExtractOptions creates ExtractOptions with defaults
func NewExtractOptions() ExtractOptions {
	return ExtractOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		AllowPartial: true,
	}
}

// Validate validates ExtractOptions
func (e ExtractOptions) Validate() error {
	if err := e.CommonOptions.Validate(); err != nil {
		return err
	}
	if e.StrictSchema && e.AllowPartial {
		return errors.New("cannot have both StrictSchema and AllowPartial")
	}
	return nil
}

// WithSchemaHints sets schema hints
func (e ExtractOptions) WithSchemaHints(hints map[string]string) ExtractOptions {
	e.SchemaHints = hints
	return e
}

// WithStrictSchema enables strict schema validation
func (e ExtractOptions) WithStrictSchema(strict bool) ExtractOptions {
	e.StrictSchema = strict
	return e
}

// WithAllowPartial allows partial extraction
func (e ExtractOptions) WithAllowPartial(allow bool) ExtractOptions {
	e.AllowPartial = allow
	return e
}

// WithExamples adds extraction examples
func (e ExtractOptions) WithExamples(examples ...interface{}) ExtractOptions {
	e.Examples = append(e.Examples, examples...)
	return e
}

// WithFieldRules sets field-specific extraction rules
func (e ExtractOptions) WithFieldRules(rules map[string]string) ExtractOptions {
	e.FieldRules = rules
	return e
}

// Builder methods for ExtractOptions that chain CommonOptions methods
func (e ExtractOptions) WithSteering(steering string) ExtractOptions {
	e.CommonOptions = e.CommonOptions.WithSteering(steering)
	return e
}

func (e ExtractOptions) WithThreshold(threshold float64) ExtractOptions {
	e.CommonOptions = e.CommonOptions.WithThreshold(threshold)
	return e
}

func (e ExtractOptions) WithMode(mode types.Mode) ExtractOptions {
	e.CommonOptions = e.CommonOptions.WithMode(mode)
	return e
}

func (e ExtractOptions) WithIntelligence(intelligence types.Speed) ExtractOptions {
	e.CommonOptions = e.CommonOptions.WithIntelligence(intelligence)
	return e
}

// mergeEmbeddedOpOptions reconciles the two option structs that fourteen
// operation option types embed at once.
//
// Those types embed both CommonOptions and types.OpOptions, and the conversion
// used to return only the CommonOptions half -- so a RequestID, CorrelationID,
// or Context set on the OpOptions side was silently discarded, and the caller
// had no way to tell. Values set on either side now reach the provider, with
// CommonOptions winning on conflict because it is the documented surface and
// the one the fluent builders write to.
//
// Mode and Intelligence are deliberately taken from CommonOptions
// unconditionally rather than merged: Strict is Mode(0) and Smart is Speed(0),
// so "unset" and "explicitly Strict/Smart" are indistinguishable and no merge
// rule can be correct until TODOS.md A-005 renumbers them. Taking CommonOptions
// preserves today's behaviour rather than guessing.
func mergeEmbeddedOpOptions(common CommonOptions, embedded types.OpOptions) types.OpOptions {
	merged := embedded

	if common.Steering != "" {
		merged.Steering = common.Steering
	}
	if common.Threshold != 0 {
		merged.Threshold = common.Threshold
	}
	merged.Mode = common.Mode
	merged.Intelligence = common.Intelligence

	requestID := common.RequestID
	if requestID == "" {
		requestID = embedded.RequestID
	}
	correlationID := common.CorrelationID
	if correlationID == "" {
		correlationID = embedded.CorrelationID
	}

	// Read the raw field, not GetContext(): that accessor substitutes
	// context.Background() when unset, so it is never nil and the embedded
	// context could never win.
	baseContext := common.Context
	if baseContext == nil {
		baseContext = embedded.Context
	}
	if baseContext == nil {
		baseContext = context.Background()
	}

	ctx, tracking := requesttracking.Ensure(baseContext, requestID, correlationID)
	merged.Context = ctx
	merged.RequestID = tracking.RequestID
	merged.CorrelationID = tracking.CorrelationID
	return merged
}

func (e ExtractOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(e.CommonOptions, e.OpOptions)
}

// TransformOptions configures the Transform operation
type TransformOptions struct {
	CommonOptions
	types.OpOptions

	// Mapping rules between source and target types
	MappingRules map[string]string

	// Fields to preserve from source
	PreserveFields []string

	// Strategy for merging data (replace, merge, append)
	MergeStrategy string

	// Custom transformation logic as natural language
	TransformLogic string

	// Examples of transformations
	Examples []struct {
		From interface{}
		To   interface{}
	}
}

// WithMergeStrategy sets the merge strategy
func (t TransformOptions) WithMergeStrategy(strategy string) TransformOptions {
	t.MergeStrategy = strategy
	return t
}

// WithMappingRules sets the mapping rules
func (t TransformOptions) WithMappingRules(rules map[string]string) TransformOptions {
	t.MappingRules = rules
	return t
}

// WithPreserveFields sets fields to preserve
func (t TransformOptions) WithPreserveFields(fields []string) TransformOptions {
	t.PreserveFields = fields
	return t
}

// WithTransformLogic sets custom transformation logic
func (t TransformOptions) WithTransformLogic(logic string) TransformOptions {
	t.TransformLogic = logic
	return t
}

// WithSteering sets the steering prompt
func (t TransformOptions) WithSteering(steering string) TransformOptions {
	t.CommonOptions = t.CommonOptions.WithSteering(steering)
	return t
}

// WithMode sets the mode
func (t TransformOptions) WithMode(mode types.Mode) TransformOptions {
	t.CommonOptions = t.CommonOptions.WithMode(mode)
	return t
}

// WithIntelligence sets the intelligence level
func (t TransformOptions) WithIntelligence(intelligence types.Speed) TransformOptions {
	t.CommonOptions = t.CommonOptions.WithIntelligence(intelligence)
	return t
}

func (t TransformOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(t.CommonOptions, t.OpOptions)
}

// NewTransformOptions creates TransformOptions with defaults
func NewTransformOptions() TransformOptions {
	return TransformOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		MergeStrategy: "replace",
	}
}

// Validate validates TransformOptions
func (t TransformOptions) Validate() error {
	if err := t.CommonOptions.Validate(); err != nil {
		return err
	}
	validStrategies := map[string]bool{"replace": true, "merge": true, "append": true}
	if t.MergeStrategy != "" && !validStrategies[t.MergeStrategy] {
		return fmt.Errorf("invalid merge strategy: %s", t.MergeStrategy)
	}
	return nil
}

// GenerateOptions configures the Generate operation
type GenerateOptions struct {
	CommonOptions
	types.OpOptions

	// Examples to guide generation
	Examples []interface{}

	// Template for generation
	Template string

	// Constraints on generated data
	Constraints map[string]interface{}

	// Seed data to base generation on
	SeedData interface{}

	// Number of variations to generate (for batch generation)
	Count int

	// Ensure uniqueness in batch generation
	EnsureUnique bool

	// Style or format preferences
	Style string
}

// NewGenerateOptions creates GenerateOptions with defaults
func NewGenerateOptions() GenerateOptions {
	return GenerateOptions{
		CommonOptions: CommonOptions{
			Mode:         types.Creative,
			Intelligence: types.Fast,
		},
		Count: 1,
	}
}

// Validate validates GenerateOptions
func (g GenerateOptions) Validate() error {
	if err := g.CommonOptions.Validate(); err != nil {
		return err
	}
	if g.Count < 1 {
		return fmt.Errorf("count must be at least 1, got %d", g.Count)
	}
	return nil
}

// WithTemplate sets the template for generation
func (g GenerateOptions) WithTemplate(template string) GenerateOptions {
	g.Template = template
	return g
}

// WithConstraints sets generation constraints
func (g GenerateOptions) WithConstraints(constraints map[string]interface{}) GenerateOptions {
	g.Constraints = constraints
	return g
}

// WithSeedData sets seed data for generation
func (g GenerateOptions) WithSeedData(seed interface{}) GenerateOptions {
	g.SeedData = seed
	return g
}

// WithCount sets the number of items to generate
func (g GenerateOptions) WithCount(count int) GenerateOptions {
	g.Count = count
	return g
}

// WithEnsureUnique ensures generated items are unique
func (g GenerateOptions) WithEnsureUnique(unique bool) GenerateOptions {
	g.EnsureUnique = unique
	return g
}

// WithStyle sets the generation style
func (g GenerateOptions) WithStyle(style string) GenerateOptions {
	g.Style = style
	return g
}

// WithExamples adds examples for generation
func (g GenerateOptions) WithExamples(examples ...interface{}) GenerateOptions {
	g.Examples = append(g.Examples, examples...)
	return g
}

// WithSteering sets the steering prompt
func (g GenerateOptions) WithSteering(steering string) GenerateOptions {
	g.CommonOptions = g.CommonOptions.WithSteering(steering)
	return g
}

// WithMode sets the mode
func (g GenerateOptions) WithMode(mode types.Mode) GenerateOptions {
	g.CommonOptions = g.CommonOptions.WithMode(mode)
	return g
}

// WithIntelligence sets the intelligence level
func (g GenerateOptions) WithIntelligence(intelligence types.Speed) GenerateOptions {
	g.CommonOptions = g.CommonOptions.WithIntelligence(intelligence)
	return g
}

func (g GenerateOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(g.CommonOptions, g.OpOptions)
}

// ========================================
// Text Operation Options
// ========================================

// SummarizeOptions configures the Summarize operation
type SummarizeOptions struct {
	CommonOptions
	types.OpOptions

	// Target length (words, sentences, or paragraphs)
	TargetLength int

	// Unit for target length ("words", "sentences", "paragraphs")
	LengthUnit string

	// Output style ("bullet", "paragraph", "executive")
	Style string

	// Use bullet points
	BulletPoints bool

	// Areas to focus on
	FocusAreas []string

	// Information to preserve
	PreserveInfo []string

	// Maximum compression ratio
	MaxCompression float64
}

// NewSummarizeOptions creates SummarizeOptions with defaults
func NewSummarizeOptions() SummarizeOptions {
	return SummarizeOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		LengthUnit:     "sentences",
		Style:          "paragraph",
		MaxCompression: 0.1, // 10% of original
	}
}

// Validate validates SummarizeOptions
func (s SummarizeOptions) Validate() error {
	if err := s.CommonOptions.Validate(); err != nil {
		return err
	}
	validUnits := map[string]bool{"words": true, "sentences": true, "paragraphs": true}
	if s.LengthUnit != "" && !validUnits[s.LengthUnit] {
		return fmt.Errorf("invalid length unit: %s", s.LengthUnit)
	}
	if s.MaxCompression < 0 || s.MaxCompression > 1 {
		return fmt.Errorf("max compression must be between 0 and 1, got %f", s.MaxCompression)
	}
	return nil
}

// WithSteering sets the steering prompt
func (s SummarizeOptions) WithSteering(steering string) SummarizeOptions {
	s.CommonOptions = s.CommonOptions.WithSteering(steering)
	return s
}

// WithMode sets the mode
func (s SummarizeOptions) WithMode(mode types.Mode) SummarizeOptions {
	s.CommonOptions = s.CommonOptions.WithMode(mode)
	return s
}

func (s SummarizeOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(s.CommonOptions, s.OpOptions)
}

// RewriteOptions configures the Rewrite operation
type RewriteOptions struct {
	CommonOptions
	types.OpOptions

	// Target tone (formal, casual, technical, friendly, etc.)
	TargetTone string

	// Formality level (1-10)
	FormalityLevel int

	// Preserve factual information
	PreserveFacts bool

	// Target audience
	Audience string

	// Writing style to emulate
	StyleGuide string

	// Specific changes to make
	Changes []string

	// Words or phrases to avoid
	AvoidWords []string

	// Words or phrases to include
	IncludeWords []string
}

// NewRewriteOptions creates RewriteOptions with defaults
func NewRewriteOptions() RewriteOptions {
	return RewriteOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		PreserveFacts:  true,
		FormalityLevel: 5,
	}
}

// Validate validates RewriteOptions
func (r RewriteOptions) Validate() error {
	if err := r.CommonOptions.Validate(); err != nil {
		return err
	}
	if r.FormalityLevel < 1 || r.FormalityLevel > 10 {
		return fmt.Errorf("formality level must be between 1 and 10, got %d", r.FormalityLevel)
	}
	return nil
}

// WithMode sets the mode
func (r RewriteOptions) WithMode(mode types.Mode) RewriteOptions {
	r.CommonOptions = r.CommonOptions.WithMode(mode)
	return r
}

func (r RewriteOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(r.CommonOptions, r.OpOptions)
}

// TranslateOptions configures the Translate operation
type TranslateOptions struct {
	CommonOptions
	types.OpOptions

	// Target language
	TargetLanguage string

	// Source language (auto-detect if empty)
	SourceLanguage string

	// Preserve formatting
	PreserveFormatting bool

	// Cultural adaptation level (0=literal, 10=full adaptation)
	CulturalAdaptation int

	// Formality level for target language
	Formality string

	// Domain-specific terminology
	Glossary map[string]string

	// Regional dialect
	Dialect string
}

// NewTranslateOptions creates TranslateOptions with defaults
func NewTranslateOptions() TranslateOptions {
	return TranslateOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		PreserveFormatting: true,
		CulturalAdaptation: 5,
		Formality:          "neutral",
	}
}

// Validate validates TranslateOptions
func (t TranslateOptions) Validate() error {
	if err := t.CommonOptions.Validate(); err != nil {
		return err
	}
	if t.TargetLanguage == "" {
		return errors.New("target language is required")
	}
	if t.CulturalAdaptation < 0 || t.CulturalAdaptation > 10 {
		return fmt.Errorf("cultural adaptation must be between 0 and 10, got %d", t.CulturalAdaptation)
	}
	return nil
}

// WithTargetLanguage sets the target language
func (t TranslateOptions) WithTargetLanguage(lang string) TranslateOptions {
	t.TargetLanguage = lang
	return t
}

// WithMode sets the mode
func (t TranslateOptions) WithMode(mode types.Mode) TranslateOptions {
	t.CommonOptions = t.CommonOptions.WithMode(mode)
	return t
}

func (t TranslateOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(t.CommonOptions, t.OpOptions)
}

// ExpandOptions configures the Expand operation
type ExpandOptions struct {
	CommonOptions
	types.OpOptions

	// Target length multiplier
	ExpansionFactor float64

	// Detail level (1-10)
	DetailLevel int

	// Examples to include
	IncludeExamples bool

	// Areas to elaborate on
	ElaborateOn []string

	// Add context about these topics
	AddContext []string

	// Style of expansion (technical, narrative, educational)
	ExpansionStyle string
}

// NewExpandOptions creates ExpandOptions with defaults
func NewExpandOptions() ExpandOptions {
	return ExpandOptions{
		CommonOptions: CommonOptions{
			Mode:         types.Creative,
			Intelligence: types.Fast,
		},
		ExpansionFactor: 2.0,
		DetailLevel:     5,
		ExpansionStyle:  "balanced",
	}
}

// Validate validates ExpandOptions
func (e ExpandOptions) Validate() error {
	if err := e.CommonOptions.Validate(); err != nil {
		return err
	}
	if e.ExpansionFactor < 1 {
		return fmt.Errorf("expansion factor must be at least 1, got %f", e.ExpansionFactor)
	}
	if e.DetailLevel < 1 || e.DetailLevel > 10 {
		return fmt.Errorf("detail level must be between 1 and 10, got %d", e.DetailLevel)
	}
	return nil
}

// WithMode sets the mode
func (e ExpandOptions) WithMode(mode types.Mode) ExpandOptions {
	e.CommonOptions = e.CommonOptions.WithMode(mode)
	return e
}

func (e ExpandOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(e.CommonOptions, e.OpOptions)
}

// ========================================
// Analysis Operation Options
// ========================================

// ClassifyOptions configures the Classify operation
type ClassifyOptions struct {
	CommonOptions
	types.OpOptions

	// Available categories
	Categories []string

	// Allow multiple categories
	MultiLabel bool

	// Minimum confidence for classification
	MinConfidence float64

	// Maximum categories to return (for multi-label)
	MaxCategories int

	// Include confidence scores in result
	IncludeConfidence bool

	// Category descriptions for better classification
	CategoryDescriptions map[string]string

	// Examples per category
	CategoryExamples map[string][]string
}

// NewClassifyOptions creates ClassifyOptions with defaults
func NewClassifyOptions() ClassifyOptions {
	return ClassifyOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		MinConfidence:     0.5,
		IncludeConfidence: true,
	}
}

// Validate validates ClassifyOptions
func (c ClassifyOptions) Validate() error {
	if err := c.CommonOptions.Validate(); err != nil {
		return err
	}
	if len(c.Categories) == 0 {
		return errors.New("at least one category is required")
	}
	if c.MinConfidence < 0 || c.MinConfidence > 1 {
		return fmt.Errorf("min confidence must be between 0 and 1, got %f", c.MinConfidence)
	}
	return nil
}

// WithCategories sets the categories for classification
func (c ClassifyOptions) WithCategories(categories []string) ClassifyOptions {
	c.Categories = categories
	return c
}

// WithMultiLabel enables multi-label classification
func (c ClassifyOptions) WithMultiLabel(multi bool) ClassifyOptions {
	c.MultiLabel = multi
	return c
}

// WithMaxCategories sets the maximum number of categories for multi-label
func (c ClassifyOptions) WithMaxCategories(max int) ClassifyOptions {
	c.MaxCategories = max
	return c
}

// WithSteering sets the steering prompt
func (c ClassifyOptions) WithSteering(steering string) ClassifyOptions {
	c.CommonOptions = c.CommonOptions.WithSteering(steering)
	return c
}

// WithMode sets the mode
func (c ClassifyOptions) WithMode(mode types.Mode) ClassifyOptions {
	c.CommonOptions = c.CommonOptions.WithMode(mode)
	return c
}

func (c ClassifyOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(c.CommonOptions, c.OpOptions)
}

// ScoreOptions configures the Score operation
type ScoreOptions struct {
	CommonOptions
	types.OpOptions

	// Scoring criteria
	Criteria []string

	// Score scale (e.g., 0-10, 0-100, 1-5)
	ScaleMin float64
	ScaleMax float64

	// Scoring rubric
	Rubric map[string]string

	// Weight for each criterion
	Weights map[string]float64

	// Include breakdown by criteria
	IncludeBreakdown bool

	// Normalize scores
	Normalize bool
}

// NewScoreOptions creates ScoreOptions with defaults
func NewScoreOptions() ScoreOptions {
	return ScoreOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		ScaleMin:         0,
		ScaleMax:         10,
		IncludeBreakdown: true,
		Normalize:        false,
	}
}

// Validate validates ScoreOptions
func (s ScoreOptions) Validate() error {
	if err := s.CommonOptions.Validate(); err != nil {
		return err
	}
	if s.ScaleMin >= s.ScaleMax {
		return fmt.Errorf("scale min (%f) must be less than scale max (%f)", s.ScaleMin, s.ScaleMax)
	}
	return nil
}

// WithCriteria sets the scoring criteria
func (s ScoreOptions) WithCriteria(criteria []string) ScoreOptions {
	s.Criteria = criteria
	return s
}

// WithScaleMin sets the minimum score value
func (s ScoreOptions) WithScaleMin(min float64) ScoreOptions {
	s.ScaleMin = min
	return s
}

// WithScaleMax sets the maximum score value
func (s ScoreOptions) WithScaleMax(max float64) ScoreOptions {
	s.ScaleMax = max
	return s
}

// WithRubric sets the scoring rubric
func (s ScoreOptions) WithRubric(rubric map[string]string) ScoreOptions {
	s.Rubric = rubric
	return s
}

// WithSteering sets the steering prompt
func (s ScoreOptions) WithSteering(steering string) ScoreOptions {
	s.CommonOptions = s.CommonOptions.WithSteering(steering)
	return s
}

// WithMode sets the mode
func (s ScoreOptions) WithMode(mode types.Mode) ScoreOptions {
	s.CommonOptions = s.CommonOptions.WithMode(mode)
	return s
}

func (s ScoreOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(s.CommonOptions, s.OpOptions)
}

// CompareOptions configures the Compare operation
type CompareOptions struct {
	CommonOptions
	types.OpOptions

	// Aspects to compare
	ComparisonAspects []string

	// Output format (table, narrative, bullet)
	OutputFormat string

	// Include similarity score
	IncludeSimilarity bool

	// Focus on differences vs similarities
	FocusOn string // "differences", "similarities", "both"

	// Depth of comparison (1-10)
	Depth int
}

// NewCompareOptions creates CompareOptions with defaults
func NewCompareOptions() CompareOptions {
	return CompareOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		OutputFormat:      "narrative",
		FocusOn:           "both",
		Depth:             5,
		IncludeSimilarity: true,
	}
}

// Validate validates CompareOptions
func (c CompareOptions) Validate() error {
	if err := c.CommonOptions.Validate(); err != nil {
		return err
	}
	validFormats := map[string]bool{"table": true, "narrative": true, "bullet": true}
	if c.OutputFormat != "" && !validFormats[c.OutputFormat] {
		return fmt.Errorf("invalid output format: %s", c.OutputFormat)
	}
	validFocus := map[string]bool{"differences": true, "similarities": true, "both": true}
	if c.FocusOn != "" && !validFocus[c.FocusOn] {
		return fmt.Errorf("invalid focus: %s", c.FocusOn)
	}
	if c.Depth < 1 || c.Depth > 10 {
		return fmt.Errorf("depth must be between 1 and 10, got %d", c.Depth)
	}
	return nil
}

// WithComparisonAspects sets aspects to compare
func (c CompareOptions) WithComparisonAspects(aspects []string) CompareOptions {
	c.ComparisonAspects = aspects
	return c
}

// WithOutputFormat sets the output format
func (c CompareOptions) WithOutputFormat(format string) CompareOptions {
	c.OutputFormat = format
	return c
}

// WithFocusOn sets the comparison focus
func (c CompareOptions) WithFocusOn(focus string) CompareOptions {
	c.FocusOn = focus
	return c
}

// WithMode sets the mode
func (c CompareOptions) WithMode(mode types.Mode) CompareOptions {
	c.CommonOptions = c.CommonOptions.WithMode(mode)
	return c
}

func (c CompareOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(c.CommonOptions, c.OpOptions)
}

// ========================================
// Collection Operation Options
// ========================================

// ChooseOptions configures the Choose operation
type ChooseOptions struct {
	CommonOptions
	types.OpOptions

	// Selection criteria
	Criteria []string

	// Require reasoning for choice
	RequireReasoning bool

	// Number of options to return (top N)
	TopN int

	// Include scores for all options
	IncludeScores bool

	// Elimination strategy (sequential, tournament, scoring)
	Strategy string
}

// NewChooseOptions creates ChooseOptions with defaults
func NewChooseOptions() ChooseOptions {
	return ChooseOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		TopN:             1,
		RequireReasoning: true,
		Strategy:         "scoring",
	}
}

// Validate validates ChooseOptions
func (c ChooseOptions) Validate() error {
	if err := c.CommonOptions.Validate(); err != nil {
		return err
	}
	if c.TopN < 1 {
		return fmt.Errorf("topN must be at least 1, got %d", c.TopN)
	}
	validStrategies := map[string]bool{"sequential": true, "tournament": true, "scoring": true}
	if c.Strategy != "" && !validStrategies[c.Strategy] {
		return fmt.Errorf("invalid strategy: %s", c.Strategy)
	}
	return nil
}

// WithCriteria sets the selection criteria
func (c ChooseOptions) WithCriteria(criteria []string) ChooseOptions {
	c.Criteria = criteria
	return c
}

// WithRequireReasoning requires reasoning for the choice
func (c ChooseOptions) WithRequireReasoning(require bool) ChooseOptions {
	c.RequireReasoning = require
	return c
}

// WithTopN sets the number of top options to return
func (c ChooseOptions) WithTopN(n int) ChooseOptions {
	c.TopN = n
	return c
}

// WithSteering sets the steering prompt.
func (c ChooseOptions) WithSteering(steering string) ChooseOptions {
	c.CommonOptions = c.CommonOptions.WithSteering(steering)
	return c
}

// WithThreshold sets the confidence threshold.
func (c ChooseOptions) WithThreshold(threshold float64) ChooseOptions {
	c.CommonOptions = c.CommonOptions.WithThreshold(threshold)
	return c
}

// WithMode sets the reasoning mode.
func (c ChooseOptions) WithMode(mode types.Mode) ChooseOptions {
	c.CommonOptions = c.CommonOptions.WithMode(mode)
	return c
}

// WithIntelligence sets the intelligence level.
func (c ChooseOptions) WithIntelligence(intelligence types.Speed) ChooseOptions {
	c.CommonOptions = c.CommonOptions.WithIntelligence(intelligence)
	return c
}

// WithContext sets the context.
func (c ChooseOptions) WithContext(ctx context.Context) ChooseOptions {
	c.CommonOptions = c.CommonOptions.WithContext(ctx)
	return c
}

// WithRequestID sets the request ID.
func (c ChooseOptions) WithRequestID(requestID string) ChooseOptions {
	c.CommonOptions = c.CommonOptions.WithRequestID(requestID)
	return c
}

func (c ChooseOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(c.CommonOptions, c.OpOptions)
}

// FilterOptions configures the Filter operation
type FilterOptions struct {
	CommonOptions
	types.OpOptions

	// Filter criteria as natural language
	Criteria string

	// Keep matching items (true) or remove them (false)
	KeepMatching bool

	// Minimum confidence for filtering decision
	MinConfidence float64

	// Return reasons for each filtering decision
	IncludeReasons bool

	// Batch size for processing
	BatchSize int
}

// NewFilterOptions creates FilterOptions with defaults
func NewFilterOptions() FilterOptions {
	return FilterOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		KeepMatching:  true,
		MinConfidence: 0.7,
		BatchSize:     50,
	}
}

// Validate validates FilterOptions
func (f FilterOptions) Validate() error {
	if err := f.CommonOptions.Validate(); err != nil {
		return err
	}
	if f.Criteria == "" {
		return errors.New("filter criteria is required")
	}
	if f.MinConfidence < 0 || f.MinConfidence > 1 {
		return fmt.Errorf("min confidence must be between 0 and 1, got %f", f.MinConfidence)
	}
	return nil
}

// WithCriteria sets the filter criteria
func (f FilterOptions) WithCriteria(criteria string) FilterOptions {
	f.Criteria = criteria
	return f
}

// WithMinConfidence sets the minimum confidence for filtering
func (f FilterOptions) WithMinConfidence(confidence float64) FilterOptions {
	f.MinConfidence = confidence
	return f
}

// WithIncludeReasons includes reasons for filtering decisions
func (f FilterOptions) WithIncludeReasons(include bool) FilterOptions {
	f.IncludeReasons = include
	return f
}

// WithSteering sets the steering prompt.
func (f FilterOptions) WithSteering(steering string) FilterOptions {
	f.CommonOptions = f.CommonOptions.WithSteering(steering)
	return f
}

// WithThreshold sets the confidence threshold.
func (f FilterOptions) WithThreshold(threshold float64) FilterOptions {
	f.CommonOptions = f.CommonOptions.WithThreshold(threshold)
	return f
}

// WithMode sets the reasoning mode.
func (f FilterOptions) WithMode(mode types.Mode) FilterOptions {
	f.CommonOptions = f.CommonOptions.WithMode(mode)
	return f
}

// WithIntelligence sets the intelligence level.
func (f FilterOptions) WithIntelligence(intelligence types.Speed) FilterOptions {
	f.CommonOptions = f.CommonOptions.WithIntelligence(intelligence)
	return f
}

// WithContext sets the context.
func (f FilterOptions) WithContext(ctx context.Context) FilterOptions {
	f.CommonOptions = f.CommonOptions.WithContext(ctx)
	return f
}

// WithRequestID sets the request ID.
func (f FilterOptions) WithRequestID(requestID string) FilterOptions {
	f.CommonOptions = f.CommonOptions.WithRequestID(requestID)
	return f
}

func (f FilterOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(f.CommonOptions, f.OpOptions)
}

// SortOptions configures the Sort operation
type SortOptions struct {
	CommonOptions
	types.OpOptions

	// Sort criteria as natural language
	Criteria string

	// Sort direction (ascending, descending)
	Direction string

	// Maintain relative order of equal elements
	Stable bool

	// Custom comparison logic
	ComparisonLogic string

	// Return sort keys/scores
	IncludeScores bool

	// Multi-level sort criteria
	SecondaryCriteria []string
}

// NewSortOptions creates SortOptions with defaults
func NewSortOptions() SortOptions {
	return SortOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Direction: "ascending",
		Stable:    true,
	}
}

// Validate validates SortOptions
func (s SortOptions) Validate() error {
	if err := s.CommonOptions.Validate(); err != nil {
		return err
	}
	if s.Criteria == "" {
		return errors.New("sort criteria is required")
	}
	validDirections := map[string]bool{"ascending": true, "descending": true}
	if s.Direction != "" && !validDirections[s.Direction] {
		return fmt.Errorf("invalid direction: %s", s.Direction)
	}
	return nil
}

// WithCriteria sets the sort criteria
func (s SortOptions) WithCriteria(criteria string) SortOptions {
	s.Criteria = criteria
	return s
}

// WithDirection sets the sort direction
func (s SortOptions) WithDirection(direction string) SortOptions {
	s.Direction = direction
	return s
}

// WithSecondaryCriteria sets secondary sort criteria
func (s SortOptions) WithSecondaryCriteria(criteria []string) SortOptions {
	s.SecondaryCriteria = criteria
	return s
}

// WithSteering sets the steering prompt.
func (s SortOptions) WithSteering(steering string) SortOptions {
	s.CommonOptions = s.CommonOptions.WithSteering(steering)
	return s
}

// WithThreshold sets the confidence threshold.
func (s SortOptions) WithThreshold(threshold float64) SortOptions {
	s.CommonOptions = s.CommonOptions.WithThreshold(threshold)
	return s
}

// WithMode sets the reasoning mode.
func (s SortOptions) WithMode(mode types.Mode) SortOptions {
	s.CommonOptions = s.CommonOptions.WithMode(mode)
	return s
}

// WithIntelligence sets the intelligence level.
func (s SortOptions) WithIntelligence(intelligence types.Speed) SortOptions {
	s.CommonOptions = s.CommonOptions.WithIntelligence(intelligence)
	return s
}

// WithContext sets the context.
func (s SortOptions) WithContext(ctx context.Context) SortOptions {
	s.CommonOptions = s.CommonOptions.WithContext(ctx)
	return s
}

// WithRequestID sets the request ID.
func (s SortOptions) WithRequestID(requestID string) SortOptions {
	s.CommonOptions = s.CommonOptions.WithRequestID(requestID)
	return s
}

func (s SortOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(s.CommonOptions, s.OpOptions)
}

// ========================================
// Batch Operation Options
// ========================================

// BatchOptions configures batch processing
type BatchOptions struct {
	CommonOptions
	types.OpOptions

	// Processing mode (parallel, merged, sequential)
	Mode string

	// Maximum concurrent operations
	Concurrency int

	// Batch size for merged mode
	BatchSize int

	// Error handling strategy (fail-fast, continue, retry)
	ErrorStrategy string

	// Maximum retries per item
	MaxRetries int
}

// NewBatchOptions creates BatchOptions with defaults
func NewBatchOptions() BatchOptions {
	return BatchOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Mode:          "parallel",
		Concurrency:   10,
		BatchSize:     50,
		ErrorStrategy: "continue",
		MaxRetries:    3,
	}
}

// Validate validates BatchOptions
func (b BatchOptions) Validate() error {
	if err := b.CommonOptions.Validate(); err != nil {
		return err
	}
	validModes := map[string]bool{"parallel": true, "merged": true, "sequential": true}
	if b.Mode != "" && !validModes[b.Mode] {
		return fmt.Errorf("invalid batch mode: %s", b.Mode)
	}
	validStrategies := map[string]bool{"fail-fast": true, "continue": true, "retry": true}
	if b.ErrorStrategy != "" && !validStrategies[b.ErrorStrategy] {
		return fmt.Errorf("invalid error strategy: %s", b.ErrorStrategy)
	}
	if b.Concurrency < 1 {
		return fmt.Errorf("concurrency must be at least 1, got %d", b.Concurrency)
	}
	if b.BatchSize < 1 {
		return fmt.Errorf("batch size must be at least 1, got %d", b.BatchSize)
	}
	return nil
}

func (b BatchOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(b.CommonOptions, b.OpOptions)
}

// ========================================
// Backward Compatibility
// ========================================

// ConvertOpOptions converts legacy OpOptions to appropriate specialized options
func ConvertOpOptions(opts types.OpOptions, operationType string) BaseOptions {
	common := CommonOptions{
		Steering:     opts.Steering,
		Threshold:    opts.Threshold,
		Mode:         opts.Mode,
		Intelligence: opts.Intelligence,
		Context:      opts.Context,
		RequestID:    opts.RequestID,
	}

	// Return appropriate specialized options based on operation type
	switch operationType {
	case "extract":
		return ExtractOptions{CommonOptions: common}
	case "transform":
		return TransformOptions{CommonOptions: common}
	case "generate":
		return GenerateOptions{CommonOptions: common}
	case "summarize":
		return SummarizeOptions{CommonOptions: common}
	case "rewrite":
		return RewriteOptions{CommonOptions: common}
	case "translate":
		return TranslateOptions{CommonOptions: common}
	case "expand":
		return ExpandOptions{CommonOptions: common}
	case "classify":
		return ClassifyOptions{CommonOptions: common}
	case "score":
		return ScoreOptions{CommonOptions: common}
	case "compare":
		return CompareOptions{CommonOptions: common}
	case "choose":
		return ChooseOptions{CommonOptions: common}
	case "filter":
		return FilterOptions{CommonOptions: common}
	case "sort":
		return SortOptions{CommonOptions: common}
	case "batch":
		return BatchOptions{CommonOptions: common}
	default:
		return common
	}
}

// IsLegacyOption checks if the option is a legacy OpOptions
func IsLegacyOption(opt interface{}) bool {
	_, ok := opt.(types.OpOptions)
	return ok
}

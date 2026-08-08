// package ops - Specialized options for each LLM operation
package ops

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	GetTimeout() time.Duration
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

	// Timeout is an invocation-scope deadline (ScopeInvocation). It is
	// applied on top of whatever context.Context this call already carries,
	// with context.WithTimeout's own semantics: a deadline the caller's
	// context already carries that is earlier than now+Timeout is left
	// alone, because Go's context package never lets a child extend a
	// parent's deadline. That existing behaviour is exactly "a deadline
	// always wins" (A-004's Revised line), so no extra bookkeeping is needed
	// to enforce it -- only to apply Timeout in the first place, which
	// nothing did before this field existed.
	//
	// Zero means the caller stated no opinion; the context's own deadline
	// (or lack of one) applies unchanged.
	Timeout time.Duration

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

// GetTimeout returns the invocation-scope timeout, or zero if the caller
// stated no opinion.
func (c CommonOptions) GetTimeout() time.Duration {
	return c.Timeout
}

// Validate performs basic validation on common options
func (c CommonOptions) Validate() error {
	if c.Threshold < 0 || c.Threshold > 1 {
		return fmt.Errorf("threshold must be between 0 and 1, got %f", c.Threshold)
	}
	return nil
}

// applyTimeout layers an invocation-scope Timeout onto a context using
// context.WithTimeout's own precedence rule: a deadline the context already
// carries that is earlier than now+timeout is left alone, because a child
// context can never extend a parent's deadline. That is exactly "a deadline
// always wins" (A-004's Revised line) -- the standard library already
// enforces it, so this is only the one place that needs to call it, now that
// CommonOptions has somewhere to keep the value.
//
// There is no single call site downstream that owns "this request is done"
// the way a defer would need -- the context this returns outlives the
// function that built it. The goroutine below is the resolution: it holds no
// reference to anything but the child context and exits the moment Done
// fires, which happens no later than the deadline either way, so it cannot
// outlive what it is waiting on.
func applyTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if timeout <= 0 {
		return ctx
	}
	child, cancel := context.WithTimeout(ctx, timeout)
	go func() {
		<-child.Done()
		cancel()
	}()
	return child
}

// toOpOptions converts to legacy OpOptions for backward compatibility
func (c CommonOptions) toOpOptions() types.OpOptions {
	ctx := applyTimeout(c.GetContext(), c.Timeout)
	ctx, tracking := requesttracking.Ensure(ctx, c.RequestID, c.CorrelationID)
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

// WithTimeout sets an invocation-scope deadline (ScopeInvocation), applied on
// top of whatever context this call already carries. See CommonOptions.Timeout
// for how it composes with a deadline the context already has.
func (c CommonOptions) WithTimeout(timeout time.Duration) CommonOptions {
	c.Timeout = timeout
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
	if err := checkLockedLimits(e); err != nil {
		return err
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
	// Mode and Intelligence fall back like every other field now.
	//
	// They used to be taken from CommonOptions unconditionally, because Strict
	// was Mode(0) and Smart was Speed(0): with no way to tell "the caller chose
	// Strict" from "the caller said nothing", falling back would have made
	// `.Strict()` unrepresentable. So `opts.OpOptions.Intelligence = Quick` was
	// silently discarded while `opts.OpOptions.Context` was honoured, and a
	// caller had no way to know which fields fell back and which did not.
	//
	// A-005 gave both types a real unset value, so the rule can be the same one
	// everywhere: the CommonOptions side wins when it says something, and the
	// embedded side is used when it does not. X-07.
	merged.Mode = common.Mode
	if common.Mode == types.ModeUnset {
		merged.Mode = embedded.Mode
	}
	merged.Intelligence = common.Intelligence
	if common.Intelligence == types.TierUnset {
		merged.Intelligence = embedded.Intelligence
	}

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

	// Timeout only lives on CommonOptions -- the embedded types.OpOptions has
	// no field for it -- so there is nothing to merge here, only to apply.
	baseContext = applyTimeout(baseContext, common.Timeout)

	ctx, tracking := requesttracking.Ensure(baseContext, requestID, correlationID)
	merged.Context = ctx
	merged.RequestID = tracking.RequestID
	merged.CorrelationID = tracking.CorrelationID
	return merged
}

// LockedLimits are constraints installed at client or data-policy scope that
// no invocation may loosen. A-004's Revised line requires exactly this: "an
// invocation may make a limit stricter but may never weaken a locked client
// or data-policy constraint," rejected rather than silently clamped, because
// clamping a caller's request back to the lock without telling them hides
// what happened just as effectively as applying it would (AGENTS.md's
// never-fail-open rule, extended to configuration rather than only answers).
//
// Client scope has no field of its own in this package -- client.go, where a
// *Client lives, is out of scope for this change -- so LockedLimits travels
// the same way the provider already does (Client.Context, client.go): as a
// value on the context.Context passed into an operation. A caller installing
// limits at client construction attaches them once with WithLockedLimits and
// passes that context into every call; every invocation under it is checked
// against the same lock.
type LockedLimits struct {
	// MaxOutputTokens is the ceiling an invocation may not raise. Zero means
	// no lock.
	MaxOutputTokens int

	// MinMode is the floor an invocation's Mode may not fall below, ordered
	// by strictness (modeStrictness): Strict is the strongest, Creative the
	// weakest. types.ModeUnset means no lock.
	MinMode types.Mode
}

// lockedLimitsContextKey is unexported so nothing outside this package can
// forge a value satisfying it -- a caller has to go through
// WithLockedLimits, which is where the type is fixed.
type lockedLimitsContextKey struct{}

// WithLockedLimits attaches limits to ctx that every invocation running
// under it must satisfy, per LockedLimits' doc comment.
func WithLockedLimits(ctx context.Context, limits LockedLimits) context.Context {
	return context.WithValue(ctx, lockedLimitsContextKey{}, limits)
}

// lockedLimitsFrom reads limits attached by WithLockedLimits, if any.
func lockedLimitsFrom(ctx context.Context) (LockedLimits, bool) {
	if ctx == nil {
		return LockedLimits{}, false
	}
	limits, ok := ctx.Value(lockedLimitsContextKey{}).(LockedLimits)
	return limits, ok
}

// modeStrictness orders Mode by how strong a guarantee it asks for, which is
// what "weaker" and "stricter" mean for MinMode: Strict enforces exact schema
// validation, Creative allows open-ended interpretation, and ModeUnset asks
// for nothing at all, so it cannot violate a floor -- there is nothing to
// compare.
func modeStrictness(mode types.Mode) int {
	switch mode {
	case types.Strict:
		return 3
	case types.TransformMode:
		return 2
	case types.Creative:
		return 1
	default:
		return 0
	}
}

// checkLockedLimits rejects an invocation that would weaken a lock installed
// on this call's context, per LockedLimits. It reports nil when no lock is
// present, or when the lock is present but nothing the invocation set
// conflicts with it.
//
// This is called from Validate(), before toOpOptions() runs and before any
// provider call is made -- the same point core.go already checks options at
// (`if err := opts.Validate(); err != nil { return ... }`), so a rejected
// invocation makes zero provider calls rather than one that gets discarded.
//
// Wired into ExtractOptions, ChooseOptions, and FilterOptions today. The
// other eleven *Options types in this file do not call it yet -- see the
// task report for why that is a stated limitation rather than an oversight.
func checkLockedLimits(o BaseOptions) error {
	limits, ok := lockedLimitsFrom(o.GetContext())
	if !ok {
		return nil
	}

	opt := o.toOpOptions()

	if limits.MaxOutputTokens > 0 && opt.MaxOutputTokens > limits.MaxOutputTokens {
		return fmt.Errorf(
			"locked policy: max output tokens %d exceeds the client-scope ceiling of %d",
			opt.MaxOutputTokens, limits.MaxOutputTokens)
	}

	if limits.MinMode != types.ModeUnset && opt.Mode != types.ModeUnset &&
		modeStrictness(opt.Mode) < modeStrictness(limits.MinMode) {
		return fmt.Errorf(
			"locked policy: mode %s is weaker than the client-scope floor of %s",
			opt.Mode, limits.MinMode)
	}

	return nil
}

// resolveField compares an operation's final value for one setting against
// the operation's own default and reports which scope produced it: equal to
// the default means nothing narrower said anything (ScopeOperationDescriptor);
// different means an invocation set it (ScopeInvocation), unless that value
// is exactly what a lock requires, in which case the lock is what is actually
// binding (ScopeClient) even though the invocation's builder call is what
// wrote the field.
func resolveField[T comparable](name string, final, operationDefault T, render func(T) string, lockedValue *T) ResolvedSetting {
	if lockedValue != nil && final == *lockedValue {
		return ResolvedSetting{Name: name, Value: render(final), Source: types.ScopeClient, Locked: true}
	}
	if final == operationDefault {
		return ResolvedSetting{Name: name, Value: render(final), Source: types.ScopeOperationDescriptor}
	}
	return ResolvedSetting{Name: name, Value: render(final), Source: types.ScopeInvocation}
}

// ResolvedSetting and ResolutionPlan are types.ResolvedSetting and
// types.ResolutionPlan under package-local names, so Explain's signature
// below reads without a types. prefix on every field. They are the same
// type; a caller across the package boundary sees types.ResolutionPlan.
type (
	ResolvedSetting = types.ResolvedSetting
	ResolutionPlan  = types.ResolutionPlan
)

// ExplainResolution builds a printable resolution plan comparing an option
// value's final state against the operation's own default (operationDefault
// is normally NewXOptions()), so a caller debugging why a setting did not
// take effect can read where it actually came from instead of stepping
// through the merge.
//
// Named ExplainResolution rather than Explain because Explain is already the
// LLM operation in explain.go -- this is deterministic, reads no field it did
// not resolve itself, and never calls a provider.
//
// It covers the cross-cutting settings every *Options type carries through
// CommonOptions and the shared MaxOutputTokens field on types.OpOptions --
// the ones A-004 is about. It does not cover operation-specific fields
// (ChooseOptions.TopN, ScoreOptions.ScaleMax, and so on): those are the
// "Params struct for operation inputs" half of the review's own distinction
// between operation inputs and cross-cutting knobs, and they carry no scope
// or lock semantics to explain.
func ExplainResolution(final, operationDefault BaseOptions) ResolutionPlan {
	ctx := final.GetContext()
	limits, hasLock := lockedLimitsFrom(ctx)

	var lockedMaxTokens *int
	var lockedMinMode *types.Mode
	if hasLock {
		if limits.MaxOutputTokens > 0 {
			lockedMaxTokens = &limits.MaxOutputTokens
		}
		if limits.MinMode != types.ModeUnset {
			lockedMinMode = &limits.MinMode
		}
	}

	finalOpt := final.toOpOptions()
	defaultOpt := operationDefault.toOpOptions()

	plan := ResolutionPlan{Settings: []ResolvedSetting{
		resolveField("Steering", final.GetSteering(), operationDefault.GetSteering(), func(v string) string { return v }, nil),
		resolveField("Threshold", final.GetThreshold(), operationDefault.GetThreshold(), func(v float64) string { return fmt.Sprintf("%g", v) }, nil),
		resolveField("Mode", final.GetMode(), operationDefault.GetMode(), func(v types.Mode) string { return v.String() }, lockedMinMode),
		resolveField("Intelligence", final.GetIntelligence(), operationDefault.GetIntelligence(), func(v types.Speed) string { return v.String() }, nil),
		resolveField("RequestID", final.GetRequestID(), operationDefault.GetRequestID(), func(v string) string { return v }, nil),
		resolveField("CorrelationID", final.GetCorrelationID(), operationDefault.GetCorrelationID(), func(v string) string { return v }, nil),
		resolveField("Timeout", final.GetTimeout(), operationDefault.GetTimeout(), func(v time.Duration) string { return v.String() }, nil),
		resolveField("MaxOutputTokens", finalOpt.MaxOutputTokens, defaultOpt.MaxOutputTokens, func(v int) string { return fmt.Sprintf("%d", v) }, lockedMaxTokens),
	}}

	if deadline, ok := ctx.Deadline(); ok {
		plan.Settings = append(plan.Settings, ResolvedSetting{
			Name:   "Deadline",
			Value:  deadline.Format(time.RFC3339Nano),
			Source: types.ScopeRequestContext,
		})
	}

	return plan
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
// WithMinConfidence sets the floor below which a classification is refused.
//
// The number checked against it is the model's own claim, not a measurement.
// What the floor guarantees is that a result the model itself scored below it
// is an error rather than a value -- which is what the field name has always
// implied and, until OP-201, did not do.
func (c ClassifyOptions) WithMinConfidence(confidence float64) ClassifyOptions {
	c.MinConfidence = confidence
	return c
}

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
	if err := checkLockedLimits(c); err != nil {
		return err
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

	// MinConfidence asks the model to keep only items it is at least this sure
	// about.
	//
	// It is an instruction, not a threshold this library enforces, and the
	// distinction is load-bearing: Filter's response is the kept items and
	// nothing else, so there is no per-item score to check the instruction
	// against. Classify, Verify, and Derive do enforce theirs, because their
	// responses report a confidence — see OP-201.
	//
	// The alternative was to delete it. It stays because asking the model is a
	// real, if weaker, effect; what was wrong was the name implying a guarantee
	// the response cannot support. If Filter ever returns per-item scores, this
	// becomes enforceable and should be enforced.
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
	if err := checkLockedLimits(f); err != nil {
		return err
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

// resolvedContext reports the context an operation should run under, given the
// two places a caller can put one.
//
// Every options type embeds both CommonOptions and types.OpOptions, and the
// fluent builders' .Context(ctx) sets the CommonOptions one -- but several
// operations read opts.OpOptions.Context directly, so .Context(ctx) reached
// Extract and silently did nothing for Choose, Filter, and Sort. A caller
// could pass a cancellable context to a collection operation and watch it
// ignore cancellation, which is exactly the failure A-013 exists to remove.
//
// The precedence is mergeEmbeddedOpOptions': the CommonOptions side wins when
// it says something. This does not call toOpOptions, deliberately -- that
// function mints request IDs as a side effect, and resolving a context should
// not create identifiers.
func resolvedContext(common CommonOptions, embedded types.OpOptions) context.Context {
	if common.Context != nil {
		return common.Context
	}
	if embedded.Context != nil {
		return embedded.Context
	}
	return context.Background()
}

package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// DecomposeOptions configures the Decompose operation
type DecomposeOptions struct {
	CommonOptions
	types.OpOptions

	// Decomposition strategy ("hierarchical", "sequential", "parallel", "functional")
	Strategy string

	// Maximum depth of decomposition
	MaxDepth int

	// Minimum granularity (stop decomposing when items are this small)
	MinGranularity string

	// Target number of parts (0 for auto)
	TargetParts int

	// Include dependencies between parts
	IncludeDependencies bool

	// Include time/effort estimates for parts
	IncludeEstimates bool

	// Decomposition criteria
	DecomposeBy string

	// Keep parent-child relationships
	PreserveHierarchy bool
}

// NewDecomposeOptions creates DecomposeOptions with defaults
func NewDecomposeOptions() DecomposeOptions {
	return DecomposeOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Strategy:            "hierarchical",
		MaxDepth:            3,
		TargetParts:         0,
		IncludeDependencies: true,
		IncludeEstimates:    false,
		PreserveHierarchy:   true,
	}
}

// Validate validates DecomposeOptions
func (d DecomposeOptions) Validate() error {
	if err := d.CommonOptions.Validate(); err != nil {
		return err
	}
	validStrategies := map[string]bool{"hierarchical": true, "sequential": true, "parallel": true, "functional": true}
	if d.Strategy != "" && !validStrategies[d.Strategy] {
		return fmt.Errorf("invalid strategy: %s", d.Strategy)
	}
	if d.MaxDepth < 1 {
		return fmt.Errorf("max depth must be at least 1, got %d", d.MaxDepth)
	}
	if d.TargetParts < 0 {
		return fmt.Errorf("target parts cannot be negative, got %d", d.TargetParts)
	}
	return nil
}

// WithStrategy sets the decomposition strategy
func (d DecomposeOptions) WithStrategy(strategy string) DecomposeOptions {
	d.Strategy = strategy
	return d
}

// WithMaxDepth sets the maximum decomposition depth
func (d DecomposeOptions) WithMaxDepth(depth int) DecomposeOptions {
	d.MaxDepth = depth
	return d
}

// WithMinGranularity sets the minimum granularity
func (d DecomposeOptions) WithMinGranularity(granularity string) DecomposeOptions {
	d.MinGranularity = granularity
	return d
}

// WithTargetParts sets the target number of parts
func (d DecomposeOptions) WithTargetParts(parts int) DecomposeOptions {
	d.TargetParts = parts
	return d
}

// WithIncludeDependencies enables dependency tracking
func (d DecomposeOptions) WithIncludeDependencies(include bool) DecomposeOptions {
	d.IncludeDependencies = include
	return d
}

// WithIncludeEstimates enables time/effort estimates
func (d DecomposeOptions) WithIncludeEstimates(include bool) DecomposeOptions {
	d.IncludeEstimates = include
	return d
}

// WithDecomposeBy sets the decomposition criteria
func (d DecomposeOptions) WithDecomposeBy(criteria string) DecomposeOptions {
	d.DecomposeBy = criteria
	return d
}

// WithPreserveHierarchy preserves parent-child relationships
func (d DecomposeOptions) WithPreserveHierarchy(preserve bool) DecomposeOptions {
	d.PreserveHierarchy = preserve
	return d
}

// WithSteering sets the steering prompt
func (d DecomposeOptions) WithSteering(steering string) DecomposeOptions {
	d.CommonOptions = d.CommonOptions.WithSteering(steering)
	return d
}

// WithMode sets the mode
func (d DecomposeOptions) WithMode(mode types.Mode) DecomposeOptions {
	d.CommonOptions = d.CommonOptions.WithMode(mode)
	return d
}

// WithIntelligence sets the intelligence level
func (d DecomposeOptions) WithIntelligence(intelligence types.Speed) DecomposeOptions {
	d.CommonOptions = d.CommonOptions.WithIntelligence(intelligence)
	return d
}

func (d DecomposeOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(d.CommonOptions, d.OpOptions)
}

func (d DecomposeOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return d.CommonOptions, d.OpOptions
}

// DecomposedPart represents a single part of decomposition
type DecomposedPart[T any] struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Content      T              `json:"content"`
	ParentID     string         `json:"parent_id,omitempty"`
	Children     []string       `json:"children,omitempty"`
	Dependencies []string       `json:"dependencies,omitempty"`
	Depth        int            `json:"depth"`
	Order        int            `json:"order"`
	Estimate     string         `json:"estimate,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// decomposeResponseSchema is the schema Decompose declares for its response:
// the parts, each carrying the caller's content type, and the root part IDs.
// It is nil when the content type cannot be expressed.
//
// It is written out by hand because DecomposedPart carries a Metadata map, and
// a map has no strict schema, so a schema generated from the struct is always
// nil. An earlier version kept a "generated from the struct" branch after this
// one on the belief that it replaced it; it never ran.
//
// The first strict version of this schema declared eight of the ten fields.
// Strict mode forbids anything undeclared, so `estimate` and `children` could
// no longer be returned and a caller who asked for estimates got none, with no
// error (found in review). TestDecomposeSchemaCoversEveryPartField
// fails if DecomposedPart gains a field this does not declare.
//
// depth and order must be real values: MaxDepth is checked against depth, and
// a null there would decode to zero and pass the check on a number nobody
// gave. estimate must be a real value when the caller asked for estimates.
func decomposeResponseSchema(contentSchema map[string]any, includeEstimates bool) map[string]any {
	text := map[string]any{"type": "string"}
	number := map[string]any{"type": "integer"}
	stringList := listOf(text)

	required := []string{"id", "name", "content", "depth", "order"}
	if includeEstimates {
		required = append(required, "estimate")
	}
	part := strictEnvelope(map[string]any{
		"id":           text,
		"name":         text,
		"description":  text,
		"content":      contentSchema,
		"parent_id":    text,
		"children":     stringList,
		"dependencies": stringList,
		"depth":        number,
		"order":        number,
		"estimate":     text,
	}, required...)

	return strictEnvelope(map[string]any{
		"parts":      listOf(part),
		"root_parts": stringList,
	}, "parts")
}

// decomposedPartAnswer is a part as the model reports it. DecomposedPart holds
// the content as the caller's T, whose own fields are not the library's to
// require, so the answer reads the content as raw JSON and the caller's reader
// decodes it. The other members carry the tags of DecomposedPart: what the
// declared schema lists as real values (id, name, content, depth, order) is
// required here, and the rest are optional, as the schema lets the model answer
// null for them. estimate is required when DecomposeOptions.IncludeEstimates
// asks for it, which Decompose checks after the read.
type decomposedPartAnswer struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Content      json.RawMessage `json:"content"`
	ParentID     string          `json:"parent_id,omitempty"`
	Children     []string        `json:"children,omitempty"`
	Dependencies []string        `json:"dependencies,omitempty"`
	Depth        int             `json:"depth"`
	Order        int             `json:"order"`
	Estimate     string          `json:"estimate,omitempty"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
}

// DecomposeResult contains the results of decomposition
type DecomposeResult[T any] struct {
	Original     T                   `json:"original"`
	Parts        []DecomposedPart[T] `json:"parts"`
	RootParts    []string            `json:"root_parts"`
	TotalParts   int                 `json:"total_parts"`
	MaxDepth     int                 `json:"max_depth"`
	Dependencies map[string][]string `json:"dependencies,omitempty"`
	Metadata     map[string]any      `json:"metadata,omitempty"`
}

// Decompose breaks complex items into smaller component parts.
// The inverse of Merge - useful for task breakdown, chunking, and analysis.
//
// Type parameter T specifies the type of the input and output parts.
//
// Examples:
//
//	// Decompose a complex task
//	result, err := Decompose(complexTask, NewDecomposeOptions().
//	    WithStrategy("hierarchical").
//	    WithIncludeEstimates(true))
//
//	// Break down requirements
//	result, err := Decompose(requirement, NewDecomposeOptions().
//	    WithTargetParts(5).
//	    WithIncludeDependencies(true))
//
//	// Chunk a document
//	result, err := Decompose(document, NewDecomposeOptions().
//	    WithStrategy("sequential").
//	    WithDecomposeBy("logical sections"))
func Decompose[T any](input T, opts DecomposeOptions) (DecomposeResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting decompose operation")

	// Every return that carries an error carries the zero result with it.

	// Validate options
	if err := opts.Validate(); err != nil {
		return DecomposeResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx, cancel := operationContext(opt.Context, 0)
	defer cancel()

	// Get type information for output
	outputType := reflect.TypeOf(input)
	typeSchema := GenerateTypeSchema(outputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Decompose operation failed: marshal error", "error", err)
		return DecomposeResult[T]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	// Build strategy description
	strategyDesc := ""
	switch opts.Strategy {
	case "hierarchical":
		strategyDesc = "Break down into a tree structure with parent-child relationships."
	case "sequential":
		strategyDesc = "Break down into sequential steps or stages that follow one another."
	case "parallel":
		strategyDesc = "Break down into independent parts that can be processed in parallel."
	case "functional":
		strategyDesc = "Break down by function or responsibility."
	}

	targetDesc := ""
	if opts.TargetParts > 0 {
		targetDesc = fmt.Sprintf("\nTarget approximately %d parts.", opts.TargetParts)
	}

	// Both are the caller's text, written as JSON strings.
	granularityDesc := ""
	if opts.MinGranularity != "" {
		quoted, _ := promptJSON(opts.MinGranularity)
		granularityDesc = fmt.Sprintf("\nStop decomposing when parts reach this granularity: %s", quoted)
	}

	decomposeByDesc := ""
	if opts.DecomposeBy != "" {
		quoted, _ := promptJSON(opts.DecomposeBy)
		decomposeByDesc = fmt.Sprintf("\nDecompose by: %s", quoted)
	}

	dependencyNote := ""
	if opts.IncludeDependencies {
		dependencyNote = "\nIdentify dependencies between parts (which parts depend on others)."
	}

	// The estimate and hierarchy notes share one slot in the prompt, in this
	// order, whichever of the two options is on.
	estimateNote := ""
	if opts.IncludeEstimates {
		estimateNote = "\nInclude time/effort estimates for each part."
	}
	if opts.PreserveHierarchy {
		estimateNote += "\nPreserve the nesting of the source: a part derived from a sub-section stays under its parent rather than being flattened to the top level."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at breaking down complex items into manageable parts.

Strategy: %s%s%s%s%s%s

Maximum depth: %d

Each part should match this schema:
%s

Return a JSON object with:
{
  "parts": [
    {
      "id": "unique-id",
      "name": "Part Name",
      "description": "What this part covers",
      "content": {},
      "parent_id": "parent-id or empty for root",
      "dependencies": ["id1", "id2"],
      "depth": 0,
      "order": 1,
      "estimate": "2 hours"
    }
  ],
  "root_parts": ["id1", "id2"]
}`, strategyDesc, targetDesc, granularityDesc, decomposeByDesc, dependencyNote, estimateNote, opts.MaxDepth, typeSchema)

	userPrompt := fmt.Sprintf("Decompose this into parts:\n\n%s", string(inputJSON))

	// the prose template hard-codes `"content": {}` while that field's
	// type is the caller's T, so `Decompose[string]` is shown an object where it
	// will only accept a string. The prose stays, because it carries the naming
	// and ordering conventions a schema cannot, and the shape is declared from T.
	var contentZero T
	opt.ResponseFormat = "json"
	// The type's schema is the plan's own: it is put into the envelope below as it
	// is and nothing changes it, so it is not copied.
	if schema := decomposeResponseSchema(typePlanFor(reflect.TypeOf(contentZero)).sharedSchema(), opts.IncludeEstimates); schema != nil {
		opt.JSONSchema = schema
		opt.SchemaName = "decomposition"
	}

	var parsed struct {
		Parts     []decomposedPartAnswer `json:"parts"`
		RootParts []string               `json:"root_parts"`
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Decompose operation LLM call failed", "error", err)
		return DecomposeResult[T]{}, fmt.Errorf("decomposition failed: %w", err)
	}

	// Parse the response. The prompt asks for the parts and the root part ids, so
	// an answer without either is refused by name, and so is a part without its
	// id, name, content, depth or order. An empty list of roots is the model
	// saying there are none.
	if err := decodeAnswer("decompose", response, &parsed); err != nil {
		log.Error("Decompose operation failed: parse error", "error", err)
		return DecomposeResult[T]{}, fmt.Errorf("failed to parse decomposition result: %w", err)
	}

	// The parts are the answer. A response with only root_parts in it decoded as
	// success with no parts and a total of zero, and the schema that requires
	// "parts" is enforced only by a provider that supports it.
	if len(parsed.Parts) == 0 {
		log.Error("Decompose operation failed: the response carried no parts")
		return DecomposeResult[T]{}, types.Newf(types.KindSchemaViolation, "decompose",
			"the response carried no `parts`, so there is nothing to return")
	}

	// The schema requires an estimate when the caller asked for them, but a
	// schema is enforced only by a provider that supports it, and an empty
	// string satisfies it everywhere. The option used to be a sentence in the
	// prompt and nothing else.
	if opts.IncludeEstimates {
		for i, part := range parsed.Parts {
			if strings.TrimSpace(part.Estimate) == "" {
				return DecomposeResult[T]{}, types.Newf(types.KindInvariantViolation, "decompose",
					"part %d of %d has no estimate, and estimates were requested", i+1, len(parsed.Parts))
			}
		}
	}

	// The content of each part is the caller's type, read by the caller's reader.
	// A RawMessage can hold a null, which is not a part's content.
	parts := make([]DecomposedPart[T], len(parsed.Parts))
	for i, part := range parsed.Parts {
		if isJSONNull(part.Content) {
			return DecomposeResult[T]{}, types.Newf(types.KindSchemaViolation, "decompose",
				"parts[%d].content is null", i)
		}
		var content T
		if err := decodeCallerAnswer("decompose", string(part.Content), &content); err != nil {
			log.Error("Decompose operation failed: the content of a part", "part", i, "error", err)
			return DecomposeResult[T]{}, fmt.Errorf("failed to parse the content of part %d: %w", i+1, err)
		}
		parts[i] = DecomposedPart[T]{
			ID:           part.ID,
			Name:         part.Name,
			Description:  part.Description,
			Content:      content,
			ParentID:     part.ParentID,
			Children:     part.Children,
			Dependencies: part.Dependencies,
			Depth:        part.Depth,
			Order:        part.Order,
			Estimate:     part.Estimate,
			Metadata:     part.Metadata,
		}
	}

	// What Go can check about the structure the model reported: the ids are
	// unique and every reference names a part that exists.
	if err := checkDecomposition(parts, parsed.RootParts); err != nil {
		log.Error("Decompose operation failed: the parts do not form a consistent structure", "error", err)
		return DecomposeResult[T]{}, err
	}

	result := DecomposeResult[T]{
		Original:     input,
		Parts:        parts,
		RootParts:    parsed.RootParts,
		TotalParts:   len(parts),
		Metadata:     make(map[string]any),
		Dependencies: make(map[string][]string),
	}

	// Dependencies were asked for only when IncludeDependencies is on; a model
	// that reported them anyway is not handed them.
	if !opts.IncludeDependencies {
		for i := range result.Parts {
			result.Parts[i].Dependencies = nil
		}
	}

	// Calculate max depth and build dependency map. The ids are unique, so no
	// entry replaces another.
	for _, part := range result.Parts {
		if part.Depth > result.MaxDepth {
			result.MaxDepth = part.Depth
		}
		if len(part.Dependencies) > 0 {
			result.Dependencies[part.ID] = part.Dependencies
		}
	}

	// MaxDepth is phrased as a ceiling in the prompt, unlike TargetParts, which
	// is explicitly a hint -- and it was compared against nothing, so the
	// measured depth two lines above could sail past the caller's limit and be
	// reported as the answer. A caller who caps depth is usually sizing a
	// recursive walk downstream.
	//
	// TargetParts, MinGranularity and DecomposeBy are requests the model
	// follows by judgement, and nothing here checks them.
	if opts.MaxDepth > 0 && result.MaxDepth > opts.MaxDepth {
		log.Error("Decompose produced parts deeper than the requested maximum",
			"depth", result.MaxDepth, "maxDepth", opts.MaxDepth)
		return DecomposeResult[T]{}, types.Newf(types.KindInvariantViolation, "decompose",
			"the decomposition reached depth %d, above the requested maximum of %d",
			result.MaxDepth, opts.MaxDepth)
	}

	log.Debug("Decompose operation succeeded", "totalParts", result.TotalParts, "maxDepth", result.MaxDepth)
	return result, nil
}

// checkDecomposition refuses a list of parts that is not a structure: a part
// with no id or an id another part has, a depth below zero, a parent, child,
// dependency or root that is not one of the parts, a child that names a
// different parent, a part whose depth is not one more than its parent's (which
// also rules out a parent chain that loops), and a chain of dependencies that
// loops. The errors count and number; the ids and names are the model's text
// and are not repeated.
func checkDecomposition[T any](parts []DecomposedPart[T], roots []string) error {
	fail := func(format string, args ...any) error {
		return types.Newf(types.KindInvariantViolation, "decompose", format, args...)
	}

	index := make(map[string]int, len(parts))
	for i, part := range parts {
		if strings.TrimSpace(part.ID) == "" {
			return fail("part %d has no id", i+1)
		}
		if _, repeated := index[part.ID]; repeated {
			return fail("part %d repeats the id of an earlier part", i+1)
		}
		index[part.ID] = i
		if part.Depth < 0 {
			return fail("part %d has a negative depth", i+1)
		}
	}

	for i, part := range parts {
		if part.ParentID != "" {
			parent, ok := index[part.ParentID]
			if !ok {
				return fail("part %d names a parent that is not one of the parts", i+1)
			}
			if part.Depth != parts[parent].Depth+1 {
				return fail("part %d is at a depth that is not one more than its parent's", i+1)
			}
		}
		for _, child := range part.Children {
			c, ok := index[child]
			if !ok {
				return fail("part %d lists a child that is not one of the parts", i+1)
			}
			if parts[c].ParentID != "" && parts[c].ParentID != part.ID {
				return fail("part %d lists a child whose parent is another part", i+1)
			}
		}
		for _, dependency := range part.Dependencies {
			if _, ok := index[dependency]; !ok {
				return fail("part %d depends on something that is not one of the parts", i+1)
			}
			if dependency == part.ID {
				return fail("part %d depends on itself", i+1)
			}
		}
	}
	for _, root := range roots {
		r, ok := index[root]
		if !ok {
			return fail("root_parts names something that is not one of the parts")
		}
		if parts[r].ParentID != "" {
			return fail("root_parts names a part that has a parent")
		}
	}

	// Loops. A parent chain cannot loop once every part is one level deeper than
	// its parent, so only the dependencies need a walk: they are a graph, and
	// are walked depth first with the usual three states.
	const (
		unvisited = iota
		inProgress
		finished
	)
	state := make([]int, len(parts))
	var visit func(int) bool
	visit = func(at int) bool {
		state[at] = inProgress
		for _, dependency := range parts[at].Dependencies {
			next := index[dependency]
			if state[next] == inProgress || (state[next] == unvisited && visit(next)) {
				return true
			}
		}
		state[at] = finished
		return false
	}
	for i := range parts {
		if state[i] == unvisited && visit(i) {
			return fail("the dependencies between the parts loop")
		}
	}
	return nil
}

// DecomposeToSlice is a convenience function that returns just the flat list of
// parts. It takes the strategy and the target number of parts from the options
// and nothing else: MaxDepth, MinGranularity, DecomposeBy and
// IncludeDependencies describe a tree of parts with ids and do not apply to a
// flat list of U.
func DecomposeToSlice[T any, U any](input T, opts DecomposeOptions) ([]U, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting decompose to slice operation")

	// Validate options
	if err := opts.Validate(); err != nil {
		return nil, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx, cancel := operationContext(opt.Context, 0)
	defer cancel()

	// Get type information
	inputType := reflect.TypeOf(input)
	outputType := reflect.TypeFor[U]()

	inputSchema := GenerateTypeSchema(inputType)
	outputSchema := GenerateTypeSchema(outputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("DecomposeToSlice failed: marshal error", "error", err)
		return nil, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	targetDesc := ""
	if opts.TargetParts > 0 {
		targetDesc = fmt.Sprintf("Create approximately %d parts.", opts.TargetParts)
	}

	systemPrompt := fmt.Sprintf(`You are an expert at breaking down complex items.

Input schema:
%s

Output part schema:
%s

Strategy: %s
%s

Return a JSON array of parts matching the output schema.`, inputSchema, outputSchema, opts.Strategy, targetDesc)

	userPrompt := fmt.Sprintf("Break this down into parts:\n\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("DecomposeToSlice LLM call failed", "error", err)
		return nil, fmt.Errorf("decomposition failed: %w", err)
	}

	// Each shape the answer may take is decoded into its own local: a decode
	// that fails halfway leaves a part-filled value behind, and the next shape
	// must not start from it.
	var result []U
	if err := decodeCallerAnswer("decompose", response, &result); err != nil {
		if wrapped, ok := decomposeWrappedParts[U](response); ok {
			result = wrapped
		} else {
			var single U
			if err2 := decodeCallerAnswer("decompose", response, &single); err2 == nil {
				result = []U{single}
			} else {
				log.Error("DecomposeToSlice failed: parse error", "error", err)
				return nil, fmt.Errorf("failed to parse parts: %w", err)
			}
		}
	}

	log.Debug("DecomposeToSlice succeeded", "partCount", len(result))
	return result, nil
}

// decomposeWrappedParts reads an answer that wraps the list of parts in an
// object, {"parts": [...]}. The wrapper is the library's and "parts" has to be
// in it; each part is the caller's U and is read by the caller's reader, so that
// what U's own fields have to carry is not decided here. It reports false for
// an answer that is not that shape, has no parts, or has a part that is not a U.
func decomposeWrappedParts[U any](response string) ([]U, bool) {
	var wrapped struct {
		Parts []json.RawMessage `json:"parts"`
	}
	if err := decodeAnswer("decompose", response, &wrapped); err != nil || len(wrapped.Parts) == 0 {
		return nil, false
	}
	parts := make([]U, len(wrapped.Parts))
	for i, raw := range wrapped.Parts {
		if err := decodeCallerAnswer("decompose", string(raw), &parts[i]); err != nil {
			return nil, false
		}
	}
	return parts, true
}

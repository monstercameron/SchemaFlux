package ops

import (
	"encoding/json"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// PL-001: Plan separated from Execute.
//
// Preflight is the whole of PL-001's contract: it returns a types.Plan --
// eligibility, chunking, a call ceiling, and an estimated cost -- and it
// does not call callLLM, RunOp, RunOpBatch, or anything that reaches
// currentHooks' provider. plan_test.go's
// TestPreflightMakesZeroProviderCalls is the test that proves the negative
// directly, by wrapping the LLM caller with a counter and asserting it never
// incremented.

// estimatorVersion is bumped whenever the estimation logic in this file
// changes in a way that could change a plan's numbers for the same input --
// the same reason OperationID carries a Version: a plan built under one
// estimator and compared against one built under another is comparing two
// different questions, and PL-001 requires the caller be able to tell.
const estimatorVersion = "pl-001.1"

// DefaultContextTokens is the context-window ceiling Preflight assumes when
// no CapabilitySnapshot says otherwise.
//
// This delivery does not build CP-001's capability negotiation -- there is no
// live source for a model's real context window yet -- so this is a
// deliberately conservative placeholder, not a measurement: it is smaller
// than every mainstream model's actual window, which biases packChunks
// toward more, smaller chunks rather than fewer, larger ones that might not
// fit. A caller who knows the real ceiling sets it on the PlanRequest.
const DefaultContextTokens = 32000

// defaultMaxItemsPerCall is the item-count ceiling PL-004 requires
// regardless of token math, so a chunk of many tiny items (each costing
// almost nothing in tokens) still cannot grow without bound.
const defaultMaxItemsPerCall = 500

// protocolOverheadBytes approximates the fixed JSON scaffolding PL-003's id
// protocol adds per item beyond the item's own encoding: `{"id":"i-000001",
// "item":` plus a closing brace and separators. Measured from the literal
// format itemID produces, not guessed.
const protocolOverheadBytes = len(`{"id":"i-000001","item":},`)

// outputTokensPerItemDefault is the planning assumption for how many
// completion tokens one item's answer costs in an MDSP response: an id
// ("i-000001", 5-6 tokens once quoted and wrapped) plus a small output
// payload. It is intentionally larger than a bare id-only answer (Sort and
// Filter's protocol) because a general MDSP operation's Merge decodes an
// Out per item, not just an id -- ResolveBatchItemwise-shaped operations
// return real content per item, not a selection.
const outputTokensPerItemDefault = 24

// Preflight builds a Plan for running op over items under req, without
// making a provider call.
//
// It is deterministic given the same op.ID, len(items), item byte sizes
// (via json.Marshal), req.Options resolved to a PolicySnapshot, and the
// CapabilitySnapshot Preflight reads from the currently installed provider's
// name -- calling Provider.Name(), never Provider.Complete. Two calls with
// identical inputs produce a byte-identical Plan.Serialize(): plan_test.go's
// TestPreflightIsDeterministic proves it directly.
func Preflight[In, Out any](op Op[In, Out], items []In, req types.PlanRequest) (types.Plan, error) {
	plan := types.Plan{
		Operation:        op.ID,
		ItemCount:        len(items),
		InputShape:       types.DescribeValue(items),
		EstimatorVersion: estimatorVersion,
		Policy:           types.PolicySnapshotFrom(req.Options),
		Capability:       capabilitySnapshot(req.Options),
	}
	plan.PolicyDigest = plan.Policy.Digest()
	plan.CapabilityDigest = plan.Capability.Digest()

	if op.BuildPrompt == nil {
		plan.IneligibleReason = fmt.Sprintf("op %s has no BuildPrompt to render a request from", op.ID)
		return plan, nil
	}
	if op.Contract.Decode == nil {
		plan.IneligibleReason = fmt.Sprintf("op %s has no Decode to turn a response into a value", op.ID)
		return plan, nil
	}

	shape, forced, reason, err := chooseShape(op, len(items), req)
	if err != nil {
		plan.IneligibleReason = err.Error()
		return plan, nil
	}
	plan.Shape = shape
	plan.ShapeForced = forced
	plan.ShapeReason = reason
	plan.Eligible = true

	switch shape {
	case types.ShapeAtomic:
		plan.MaxCalls = len(items)
		plan.EstimatedCost = estimateAtomicCost(items, plan.Capability)
		return plan, nil

	case types.ShapeMDSP, types.ShapeGlobal:
		chunks, oversized, err := planChunks(items, req, plan.Capability)
		if err != nil {
			plan.Eligible = false
			plan.IneligibleReason = err.Error()
			return plan, nil
		}
		plan.Chunks = chunks
		plan.OversizedItems = oversized
		plan.MaxCalls = len(chunks) + len(oversized)
		plan.EstimatedCost = estimateChunkedCost(chunks, oversized, plan.Capability)
		return plan, nil

	default:
		plan.Eligible = false
		plan.IneligibleReason = fmt.Sprintf("shape %s has no execution plan in this delivery", shape)
		return plan, nil
	}
}

// legalShapes reports which ExecutionShapes op can run under, given its
// declared batch algebra. Atomic is always legal -- RunOp already does one
// item at a time regardless of batch declaration.
func legalShapes[In, Out any](op Op[In, Out]) map[types.ExecutionShape]bool {
	legal := map[types.ExecutionShape]bool{types.ShapeAtomic: true}

	batched := op.Batch.Class == types.BatchItemwise || op.Batch.Class == types.BatchAggregate
	wired := op.Batch.Encode != nil && op.Batch.Merge != nil

	if batched && wired {
		legal[types.ShapeMDSP] = true
	}
	if op.Batch.Class == types.BatchAggregate && wired {
		legal[types.ShapeGlobal] = true
	}
	// ShapeSDMP: no operation in this delivery declares a staged form (see
	// types.ShapeSDMP's doc comment), so it is never legal here. Forcing it
	// is refused at plan time by chooseShape, not silently downgraded.
	return legal
}

// chooseShape picks the ExecutionShape a plan will use: the caller's forced
// choice if legal, an error if forced and illegal, or the planner's own
// choice when the caller stated none.
//
// PL-002's verify line is "a forced mode that is illegal for the operation
// is refused at plan time, not silently ignored" -- the error return here,
// which Preflight turns into an ineligible plan rather than ever silently
// substituting Atomic, is that refusal.
func chooseShape[In, Out any](op Op[In, Out], itemCount int, req types.PlanRequest) (shape types.ExecutionShape, forced bool, reason string, err error) {
	legal := legalShapes(op)

	if req.Force != types.ShapeUnspecified {
		if !legal[req.Force] {
			return types.ShapeUnspecified, false, "", fmt.Errorf(
				"forced shape %s is not legal for op %s (batch class %s): legal shapes are %s",
				req.Force, op.ID, op.Batch.Class, legalShapeNames(legal))
		}
		reason = req.ForceReason
		if reason == "" {
			reason = "caller forced this shape"
		}
		return req.Force, true, reason, nil
	}

	if itemCount <= 1 {
		return types.ShapeAtomic, false, "one item or fewer: no batching to gain", nil
	}
	if legal[types.ShapeMDSP] {
		return types.ShapeMDSP, false, "batch algebra declared and wired; batching many items into one call", nil
	}
	return types.ShapeAtomic, false, "operation has no wired batch algebra", nil
}

func legalShapeNames(legal map[types.ExecutionShape]bool) string {
	names := "atomic"
	for _, s := range []types.ExecutionShape{types.ShapeMDSP, types.ShapeGlobal, types.ShapeSDMP} {
		if legal[s] {
			names += ", " + s.String()
		}
	}
	return names
}

// capabilitySnapshot reads the currently installed provider's name (never
// calling it) and resolves the model the policy would use, through the same
// config.GetModel every operation already calls -- both are pure lookups, no
// network.
func capabilitySnapshot(opt types.OpOptions) types.CapabilitySnapshot {
	_, provider := currentHooks()
	providerName := ""
	if provider != nil {
		providerName = provider.Name()
	}
	model := config.GetModel(opt.Intelligence, providerName)
	maxOutput := opt.MaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = config.GetMaxTokens(opt.Intelligence)
	}
	return types.CapabilitySnapshot{
		Provider:        providerName,
		Model:           model,
		MaxOutputTokens: maxOutput,
		ContextTokens:   DefaultContextTokens,
	}
}

// itemSize is one item's estimated encoded footprint, measured once by
// marshaling it -- never guessed from a formula, and never retained past the
// measurement (only the byte/token counts survive into a Plan, never the
// item).
type itemSize struct {
	bytes  int
	tokens int
}

// measureItems marshals each item once to size it for chunk packing. A
// marshal failure means the item cannot be sent to a provider at all under
// this protocol, which is an eligibility failure, not a silently-skipped
// item.
func measureItems[In any](items []In) ([]itemSize, error) {
	sizes := make([]itemSize, len(items))
	for i, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("item %d cannot be encoded for a batch request: %v", i, err)
		}
		bytes := len(encoded) + protocolOverheadBytes
		sizes[i] = itemSize{bytes: bytes, tokens: estimateTokens(string(encoded))}
	}
	return sizes, nil
}

// chunkBudget is the six-bound ceiling PL-004 packs chunks against, each
// independently checkable so a caller can see which one decided a chunk's
// size.
type chunkBudget struct {
	maxItemsPerCall   int
	maxInputTokens    int
	outputReserve     int
	contextLimit      int
	maxCostPerCallUSD float64
	maxPayloadBytes   int

	systemTokens          int
	protocolTokensPerItem int
	outputTokensPerItem   int

	costPerPromptToken     float64
	costPerCompletionToken float64
}

// boundName identifies one of PL-004's six chunk-size bounds.
type boundName string

const (
	boundItemCount     boundName = "item_count"
	boundInputTokens   boundName = "input_tokens"
	boundOutputReserve boundName = "output_reserve"
	boundContextLimit  boundName = "context_limit"
	boundPerCallCost   boundName = "per_call_cost"
	boundPayloadBytes  boundName = "payload_bytes"
)

// boundViolation reports the first of the six bounds a chunk of count items,
// totalling tokens input tokens and bytes payload bytes, would break under
// budget -- or "" if none. Checked in a fixed order so the same overshoot
// always names the same bound.
func boundViolation(count, tokens, bytes int, budget chunkBudget) boundName {
	if budget.maxItemsPerCall > 0 && count > budget.maxItemsPerCall {
		return boundItemCount
	}
	if budget.maxInputTokens > 0 && tokens > budget.maxInputTokens {
		return boundInputTokens
	}
	outputNeeded := count * budget.outputTokensPerItem
	if budget.outputReserve > 0 && outputNeeded > budget.outputReserve {
		return boundOutputReserve
	}
	contextLimit := budget.contextLimit
	if contextLimit <= 0 {
		contextLimit = DefaultContextTokens
	}
	total := budget.systemTokens + tokens + count*budget.protocolTokensPerItem + outputNeeded
	if total > contextLimit {
		return boundContextLimit
	}
	if budget.maxPayloadBytes > 0 && bytes > budget.maxPayloadBytes {
		return boundPayloadBytes
	}
	if budget.maxCostPerCallUSD > 0 {
		promptTokens := float64(tokens + count*budget.protocolTokensPerItem)
		completionTokens := float64(outputNeeded)
		cost := promptTokens*budget.costPerPromptToken/1000 + completionTokens*budget.costPerCompletionToken/1000
		if cost > budget.maxCostPerCallUSD {
			return boundPerCallCost
		}
	}
	return ""
}

// packChunks partitions itemSizes into chunks, each as large as it can be
// without breaking any bound in budget. An item that violates a bound on its
// own (count 1) is reported in oversized rather than blocking every chunk
// after it -- PL-004's "routed atomically or refused; never silently
// truncated".
func packChunks(sizes []itemSize, budget chunkBudget) ([]types.ChunkPlan, []types.OversizedItem, error) {
	var chunks []types.ChunkPlan
	var oversized []types.OversizedItem

	i := 0
	for i < len(sizes) {
		if bound := boundViolation(1, sizes[i].tokens, sizes[i].bytes, budget); bound != "" {
			oversized = append(oversized, types.OversizedItem{Index: i, Bound: string(bound)})
			i++
			continue
		}

		count := 1
		curTokens := sizes[i].tokens
		curBytes := sizes[i].bytes
		var lastBound boundName

		for i+count < len(sizes) {
			nextTokens := curTokens + sizes[i+count].tokens
			nextBytes := curBytes + sizes[i+count].bytes
			bound := boundViolation(count+1, nextTokens, nextBytes, budget)
			if bound != "" {
				lastBound = bound
				break
			}
			curTokens = nextTokens
			curBytes = nextBytes
			count++
		}

		chunks = append(chunks, types.ChunkPlan{ItemCount: count, BoundingRule: string(lastBound)})
		i += count
	}

	return chunks, oversized, nil
}

// planChunks builds the ChunkBudget from req and the capability snapshot,
// measures items, and packs them.
func planChunks[In any](items []In, req types.PlanRequest, capability types.CapabilitySnapshot) ([]types.ChunkPlan, []types.OversizedItem, error) {
	sizes, err := measureItems(items)
	if err != nil {
		return nil, nil, err
	}

	maxItems := req.MaxItemsPerCall
	if maxItems <= 0 {
		maxItems = defaultMaxItemsPerCall
	}

	outputReserve := capability.MaxOutputTokens
	contextLimit := capability.ContextTokens
	if contextLimit <= 0 {
		contextLimit = DefaultContextTokens
	}

	costIn, costOut, priced := lookupTokenPrices(capability.Model)
	maxCost := 0.0
	if priced {
		maxCost = req.Budget
	}

	budget := chunkBudget{
		maxItemsPerCall:        maxItems,
		outputReserve:          outputReserve,
		contextLimit:           contextLimit,
		maxCostPerCallUSD:      maxCost,
		systemTokens:           0,
		protocolTokensPerItem:  protocolOverheadBytes / charactersPerToken,
		outputTokensPerItem:    outputTokensPerItemDefault,
		costPerPromptToken:     costIn,
		costPerCompletionToken: costOut,
	}

	return packChunks(sizes, budget)
}

// lookupTokenPrices asks pricing for model's per-1K-token rates without
// spending anything: pricing.CalculateCost on a synthetic 1000/1000 usage is
// a pure lookup against the rate card, the same one CalculateCost consults
// for a real call, so the estimate and the eventual bill never disagree
// about which rate applies.
func lookupTokenPrices(model string) (perPromptToken, perCompletionToken float64, priced bool) {
	usage := &types.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}
	cost := pricing.CalculateCost(usage, model, "")
	if cost == nil || !cost.Priced {
		return 0, 0, false
	}
	return cost.PricePerPromptToken, cost.PricePerCompletionToken, true
}

// estimateAtomicCost sums a per-item estimate across every item, one call
// each.
func estimateAtomicCost[In any](items []In, capability types.CapabilitySnapshot) types.CostEstimate {
	sizes, err := measureItems(items)
	if err != nil {
		// Eligibility already failed on this in Preflight's own path for
		// shape MDSP; for atomic, an unmarshalable item is not fatal to
		// planning -- BuildPrompt may not even use json.Marshal on it -- so
		// this falls back to an unpriced estimate rather than failing the
		// whole plan over a sizing heuristic.
		return types.CostEstimate{}
	}

	promptTokens := 0
	for _, s := range sizes {
		promptTokens += s.tokens
	}
	completionTokens := len(items) * capability.MaxOutputTokens

	return costFromTokens(capability.Model, promptTokens, completionTokens)
}

// estimateChunkedCost sums the estimate across every planned chunk and every
// oversized item routed atomically.
func estimateChunkedCost(chunks []types.ChunkPlan, oversized []types.OversizedItem, capability types.CapabilitySnapshot) types.CostEstimate {
	promptTokens := 0
	completionTokens := 0
	for _, c := range chunks {
		promptTokens += c.ItemCount * (protocolOverheadBytes / charactersPerToken)
		completionTokens += c.ItemCount * outputTokensPerItemDefault
	}
	completionTokens += len(oversized) * capability.MaxOutputTokens

	return costFromTokens(capability.Model, promptTokens, completionTokens)
}

// costFromTokens turns a token estimate into a CostEstimate, reporting
// unpriced -- never a fabricated $0.00 -- when the model has no rate card
// entry. PR-002's rule, carried into planning: a plan for an unpriced model
// must say so honestly rather than estimate zero.
func costFromTokens(model string, promptTokens, completionTokens int) types.CostEstimate {
	usage := &types.TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
	cost := pricing.CalculateCost(usage, model, "")
	if cost == nil || !cost.Priced {
		return types.CostEstimate{
			EstimatedPromptTokens:     promptTokens,
			EstimatedCompletionTokens: completionTokens,
		}
	}
	return types.CostEstimate{
		Priced:                    true,
		EstimatedCost:             cost.TotalCost,
		Currency:                  cost.Currency,
		PricingSource:             cost.PricingSource,
		EstimatedPromptTokens:     promptTokens,
		EstimatedCompletionTokens: completionTokens,
	}
}

// --- PL-002: caller-forced shape, as a fluent builder.

// PlanBuilder assembles a types.PlanRequest. Atomic, Batched, and Adaptive
// are PL-002's three named forcing points: forcing atomicity for risk
// reasons and forcing batching for cost reasons are both legitimate, so both
// are one call, and Adaptive explicitly clears any earlier force rather than
// leaving the caller to remember that Unspecified means "let the planner
// choose".
type PlanBuilder struct {
	req types.PlanRequest
}

// NewPlanBuilder starts a PlanBuilder from resolved call options.
func NewPlanBuilder(opt types.OpOptions) *PlanBuilder {
	return &PlanBuilder{req: types.PlanRequest{Options: opt}}
}

// Atomic forces one call per item, for a caller whose reason is risk: an
// operation whose batch answer is harder to audit, or whose provider is not
// trusted to hold many inputs in one context.
func (b *PlanBuilder) Atomic() *PlanBuilder {
	b.req.Force = types.ShapeAtomic
	b.req.ForceReason = "caller forced atomic execution"
	return b
}

// Batched forces the MDSP shape, for a caller whose reason is cost: fewer
// calls than one per item.
func (b *PlanBuilder) Batched() *PlanBuilder {
	b.req.Force = types.ShapeMDSP
	b.req.ForceReason = "caller forced batched execution"
	return b
}

// Adaptive clears any forced shape, returning the plan to the planner's own
// choice.
func (b *PlanBuilder) Adaptive() *PlanBuilder {
	b.req.Force = types.ShapeUnspecified
	b.req.ForceReason = ""
	return b
}

// WithReason overrides the default reason text a Force call records, so a
// caller can name their own reason ("provider Y truncates batches above
// 20 items") instead of the generic default.
func (b *PlanBuilder) WithReason(reason string) *PlanBuilder {
	b.req.ForceReason = reason
	return b
}

// WithBudget sets the plan's cost ceiling.
func (b *PlanBuilder) WithBudget(usd float64) *PlanBuilder {
	b.req.Budget = usd
	return b
}

// WithMaxItemsPerCall caps MDSP/global chunk size independent of any token
// bound.
func (b *PlanBuilder) WithMaxItemsPerCall(n int) *PlanBuilder {
	b.req.MaxItemsPerCall = n
	return b
}

// Build returns the assembled PlanRequest.
func (b *PlanBuilder) Build() types.PlanRequest {
	return b.req
}

package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
	"github.com/monstercameron/schemaflux/telemetry"
)

// Plan is separate from Execute. Preflight returns a types.Plan --
// eligibility, chunking, a call ceiling, and an estimated cost -- and does not
// call callLLM, RunOp, RunOpBatch, or anything that reaches currentHooks'
// provider. TestPreflightMakesZeroProviderCalls proves the negative directly,
// by wrapping the LLM caller with a counter and asserting it never incremented.

// estimatorVersion is bumped whenever the estimation logic in this file
// changes in a way that could change a plan's numbers for the same input --
// the same reason OperationID carries a Version: a plan built under one
// estimator and compared against one built under another is comparing two
// different questions, and the caller has to be able to tell.
//
// Version 4 prices what is SENT: the prompts the descriptor renders, as the call
// path turns them into a request. The reinforcement block the call path puts in
// front of the system prompt, and the caller's steering it puts in front of the
// user prompt, are counted through the same functions the call path uses
// (dec12StrengthenSystemPrompt, applySteering), so the estimate cannot drift from
// the request. Version 4 also prices the batch protocol's longer random ids.
//
// Version 3 priced what the descriptor renders: the system and user prompt
// BuildPrompt returns for each item of an atomic plan, and the system and user
// prompt the batch's Encode returns for each planned chunk, and it cut chunks to
// leave room in the model's window for that system prompt, but not for the
// reinforcement block or the steering that went with every request. Version 2
// priced the items by the size of their JSON and counted the system prompt as no
// tokens, so a descriptor with a long system prompt was under-estimated by that
// prompt once for every request; a staged plan priced a stage by its BuildPrompt,
// which was a second estimator of the same thing. Version 1 counted only the
// per-item protocol overhead of a batch.
const estimatorVersion = "plan-estimator-4"

// DefaultContextTokens is the context-window ceiling Preflight assumes when
// the capability registry declares no window for the model.
//
// It is a deliberately conservative placeholder, not a measurement: it is
// smaller than every mainstream model's actual window, which biases packChunks
// toward more, smaller chunks rather than fewer, larger ones that might not
// fit. When llm.CapabilitiesFor knows the model's ContextWindow, that is used
// instead.
const DefaultContextTokens = 32000

// defaultMaxItemsPerCall is the item-count ceiling that applies regardless of
// token math, so a chunk of many tiny items (each costing almost nothing in
// tokens) still cannot grow without bound.
const defaultMaxItemsPerCall = 500

// protocolOverheadBytes approximates the fixed JSON scaffolding the id
// protocol adds per item beyond the item's own encoding: `{"id":"` and an id of
// batchIDLength characters, then `","item":` plus a closing brace and separators.
// Measured from the literal format the batch encoder produces, not guessed.
const protocolOverheadBytes = len(`{"id":"`) + batchIDLength + len(`","item":},`)

// outputTokensPerItemDefault is the planning assumption for how many
// completion tokens one item's answer costs in an MDSP response: an id
// ("i-000001", 5-6 tokens once quoted and wrapped) plus a small output
// payload. It is intentionally larger than a bare id-only answer (Sort and
// Filter's protocol) because a general MDSP operation's Merge decodes an
// Out per item, not just an id -- ResolveBatchItemwise-shaped operations
// return real content per item, not a selection.
const outputTokensPerItemDefault = 24

// Preflight builds a Plan for running op over items under req, without
// making a provider call. The error result is always nil: a request that
// cannot be planned is reported on the Plan as an IneligibleReason, so the
// caller reads one place for every refusal.
//
// It has no context, so it plans for the process-wide route: the installed
// provider, and the tier's model unless the options name one. PreflightFor is
// the entry that plans for a client.
//
// A positive req.Budget is a ceiling on the whole request's estimated cost. A
// plan whose estimate is over it is ineligible, and so is one whose estimate
// cannot be compared with it because the model has no price.
//
// The estimate is of what op renders. For an atomic plan that is what
// BuildPrompt returns for each item, and for a batched plan what the batch's
// Encode returns for each planned chunk, plus BuildPrompt for each item sent
// alone. Both are the caller's functions: each is called once for each item or
// chunk it renders, a panic in one or an error from Encode refuses the plan
// without naming anything the function produced, and neither may do anything but
// return the prompt.
//
// It is deterministic given the same op.ID, the prompts op renders for items,
// req.Options resolved to a PolicySnapshot, and the CapabilitySnapshot Preflight
// reads from the currently installed provider's name -- calling Provider.Name(),
// never Provider.Complete. Two calls with identical inputs produce a
// byte-identical Plan.Serialize(): plan_test.go's TestPreflightIsDeterministic
// proves it directly.
func Preflight[In, Out any](op Op[In, Out], items []In, req types.PlanRequest) (types.Plan, error) {
	return PreflightFor(context.Background(), op, items, req)
}

// PreflightFor is Preflight for the client ctx carries: the provider it routes
// to and the model it names. A call made on a client that has its own provider
// and model is planned for that provider and model, and so is cut into chunks
// that fit that model's context window and not the process default's. A
// context that carries no client is planned exactly as Preflight plans.
//
// The client is read once, at the top, so that the provider and the model of
// one plan come from the same snapshot even if the default is replaced while
// the plan is made.
func PreflightFor[In, Out any](ctx context.Context, op Op[In, Out], items []In, req types.PlanRequest) (types.Plan, error) {
	ctx, _ = PinExec(ctx)
	return planOperation(ctx, op, items, req, true), nil
}

// planRefusal is why a plan cannot be made: the reason in the library's words,
// which names nothing the caller's functions produced, and the kind of refusal
// it is. A nil *planRefusal is no refusal.
type planRefusal struct {
	kind   types.ErrorKind
	reason string
}

// refuse makes plan ineligible for refusal's reason and records its kind beside
// it. The estimate and the call ceiling go, since nothing that can run is left to
// count; the shape the planner had chosen stays, as the plan's account of what it
// was going to do.
func refuse(plan *types.Plan, refusal *planRefusal) {
	plan.Eligible = false
	plan.IneligibleKind = refusal.kind
	plan.IneligibleReason = refusal.reason
	plan.Chunks = nil
	plan.OversizedItems = nil
	plan.MaxCalls = 0
	plan.EstimatedCost = types.CostEstimate{}
}

// planOperation is the plan for op over items, for the client ctx carries, which
// PreflightFor has pinned. It is every refusal as a plan and never an error. record
// is whether to emit the fan-out metrics for the plan: a staged plan makes one of
// these for each of its stages and records its own, so it does not.
func planOperation[In, Out any](ctx context.Context, op Op[In, Out], items []In, req types.PlanRequest, record bool) types.Plan {
	plan := types.Plan{
		Operation:        op.ID,
		ItemCount:        len(items),
		InputShape:       types.DescribeValue(items),
		EstimatorVersion: estimatorVersion,
		Policy:           types.PolicySnapshotFrom(req.Options),
		Capability:       capabilitySnapshot(ctx, req.Options),
	}
	plan.PolicyDigest = plan.Policy.Digest()
	plan.CapabilityDigest = plan.Capability.Digest()

	if op.BuildPrompt == nil {
		refuse(&plan, &planRefusal{types.KindConfiguration, fmt.Sprintf("op %s has no BuildPrompt to render a request from", op.ID)})
		return plan
	}
	if op.Contract.Decode == nil {
		refuse(&plan, &planRefusal{types.KindConfiguration, fmt.Sprintf("op %s has no Decode to turn a response into a value", op.ID)})
		return plan
	}

	shape, forced, reason, alternatives, err := chooseShape(op, len(items), req)
	if err != nil {
		refuse(&plan, &planRefusal{types.KindOf(err), err.Error()})
		return plan
	}
	plan.Shape = shape
	plan.ShapeForced = forced
	plan.ShapeReason = reason
	plan.Alternatives = alternatives
	plan.Eligible = true

	switch shape {
	case types.ShapeAtomic:
		prompt, refusal := renderedPromptTokens(ctx, op, items, nil, req.Options)
		if refusal != nil {
			refuse(&plan, refusal)
			return plan
		}
		plan.MaxCalls = len(items)
		plan.EstimatedCost = costFromTokens(plan.Capability.Model, prompt, len(items)*plan.Capability.MaxOutputTokens)

	case types.ShapeMDSP, types.ShapeGlobal:
		sizes, refusal := measureItems(items)
		if refusal != nil {
			refuse(&plan, refusal)
			return plan
		}
		chunks, oversized, prompt, refusal := planBatch(ctx, op, items, sizes, req, plan.Capability)
		if refusal != nil {
			refuse(&plan, refusal)
			return plan
		}
		plan.Chunks = chunks
		plan.OversizedItems = oversized
		plan.MaxCalls = len(chunks) + len(oversized)
		plan.EstimatedCost = estimateChunkedCost(prompt, chunks, oversized, plan.Capability)

	default:
		refuse(&plan, &planRefusal{types.KindConfiguration, fmt.Sprintf("shape %s has no execution plan in this delivery", shape)})
		return plan
	}

	holdToBudget(&plan, req.Budget)
	if record {
		recordPlanMetrics(op.ID.String(), plan)
	}
	return plan
}

// holdToBudget makes plan ineligible when the caller set a budget and the plan
// does not fit inside it, and says that it is the budget that refused it. The
// budget bounds the whole request, not one call, so it is the plan's total
// estimate that is compared. An estimate with no price behind it says nothing
// about cost, and a budget it cannot be checked against is not one that was kept,
// so that plan is refused as well, as a budget and not as a configuration: it was
// the budget that could not be held.
func holdToBudget(plan *types.Plan, budget float64) {
	if budget <= 0 {
		return
	}
	estimate := plan.EstimatedCost
	switch {
	case !estimate.Priced:
		plan.Eligible = false
		plan.IneligibleKind = types.KindBudgetExceeded
		plan.IneligibleReason = fmt.Sprintf("a budget of %.4f was set and model %q has no price, so the estimated cost cannot be checked against it", budget, plan.Capability.Model)
	case estimate.EstimatedCost > budget:
		plan.Eligible = false
		plan.IneligibleKind = types.KindBudgetExceeded
		plan.IneligibleReason = fmt.Sprintf("the estimated cost of %.4f %s is over the budget of %.4f", estimate.EstimatedCost, estimate.Currency, budget)
	}
}

// recordPlanMetrics emits the fan-out metrics for an eligible plan,
// gated on telemetry.MetricsEnabled() the same way every other metrics call
// site in this package already is (core.go's *_duration metrics) -- a
// disabled-metrics build pays for the config read and nothing else.
//
// This does not compromise Preflight's own "makes zero provider calls"
// contract (TestPreflightMakesZeroProviderCalls): recording a
// metric touches no llm.Provider and returns nothing that could change
// plan's own value, so two Preflight calls over identical input still
// produce byte-identical Plan.Serialize output regardless of whether
// metrics are enabled.
func recordPlanMetrics(operation string, plan types.Plan) {
	if !telemetry.MetricsEnabled() {
		return
	}
	telemetry.RecordPlanMetrics(operation, plan)
}

// legalShapes reports which ExecutionShapes op can run under, given its
// declared batch algebra. Atomic is always legal -- RunOp already does one
// item at a time regardless of batch declaration.
func legalShapes[In, Out any](op Op[In, Out]) map[types.ExecutionShape]bool {
	legal := map[types.ExecutionShape]bool{types.ShapeAtomic: true}

	// An operation that requires evidence is asked one item at a time: the answer to
	// a batch is not checked for evidence, so a batched run would deliver what a
	// request of its own refuses.
	batched := (op.Batch.Class == types.BatchItemwise || op.Batch.Class == types.BatchAggregate) && !batchCannotCarryEvidence(op)
	wired := op.Batch.Encode != nil && op.Batch.Merge != nil

	if batched && wired {
		legal[types.ShapeMDSP] = true
	}
	if op.Batch.Class == types.BatchAggregate && wired {
		legal[types.ShapeGlobal] = true
	}
	// ShapeSDMP is the shape of a staged plan, which PreflightStagedPlan plans
	// over several operations. One operation does not declare a staged form (see
	// types.ShapeSDMP's doc comment), so it is never legal for one. Forcing it is
	// refused at plan time by chooseShape, not silently downgraded.
	return legal
}

// chooseShape picks the ExecutionShape a plan will use: the caller's forced
// choice if legal, an error if forced and illegal, or the planner's own
// choice when the caller stated none. It also names every other shape it
// considered and why that one was not chosen, which is the alternatives half
// of types.Plan.Explain().
//
// A forced shape that is illegal for the operation is refused at plan time,
// not silently ignored: the error return here, which Preflight turns into an
// ineligible plan rather than ever substituting Atomic, is that refusal.
func chooseShape[In, Out any](op Op[In, Out], itemCount int, req types.PlanRequest) (shape types.ExecutionShape, forced bool, reason string, alternatives []types.RejectedAlternative, err error) {
	legal := legalShapes(op)

	if req.Force != types.ShapeUnspecified {
		if !legal[req.Force] {
			if req.Force == types.ShapeSDMP {
				// The shape exists, and a staged plan is planned with it. It is
				// refused here because this plans one operation over its items,
				// and a staged plan is one datum over several operations.
				return types.ShapeUnspecified, false, "", nil, types.Classified(types.KindConfiguration, fmt.Errorf(
					"forced shape %s is not legal for op %s: it is the shape of a staged plan (PreflightStagedPlan), which takes one datum through several operations, and this plans one operation; legal shapes are %s",
					req.Force, op.ID, legalShapeNames(legal)))
			}
			return types.ShapeUnspecified, false, "", nil, types.Classified(types.KindConfiguration, fmt.Errorf(
				"forced shape %s is not legal for op %s (batch class %s): legal shapes are %s",
				req.Force, op.ID, op.Batch.Class, legalShapeNames(legal)))
		}
		reason = req.ForceReason
		if reason == "" {
			reason = "caller forced this shape"
		}
		// Every other legal shape was available and not chosen only because
		// the caller forced this one -- naming them says the choice was
		// forced, not that no alternative existed.
		for _, alt := range otherLegalShapes(legal, req.Force) {
			alternatives = append(alternatives, types.RejectedAlternative{
				Shape:  alt,
				Reason: fmt.Sprintf("caller forced %s instead (%s)", req.Force, reason),
			})
		}
		return req.Force, true, reason, alternatives, nil
	}

	if itemCount <= 1 {
		reason = "one item or fewer: no batching to gain"
		if legal[types.ShapeMDSP] {
			alternatives = append(alternatives, types.RejectedAlternative{
				Shape:  types.ShapeMDSP,
				Reason: "legal for this operation, but only one item was offered -- a batch of one call gains nothing over atomic's one call",
			})
		}
		return types.ShapeAtomic, false, reason, alternatives, nil
	}
	if legal[types.ShapeMDSP] {
		// The chunk count is not known until the items are measured, so the
		// reason says what batching does and not how many calls it comes to.
		reason = fmt.Sprintf("batch algebra declared and wired; packing %d items into chunks of several items each instead of one call per item", itemCount)
		alternatives = append(alternatives, types.RejectedAlternative{
			Shape:  types.ShapeAtomic,
			Reason: fmt.Sprintf("legal, but would cost %d calls instead of the planned chunk count", itemCount),
		})
		return types.ShapeMDSP, false, reason, alternatives, nil
	}
	return types.ShapeAtomic, false, "operation has no wired batch algebra", nil, nil
}

// otherLegalShapes lists every legal shape besides chosen, in a fixed order
// -- the same order legalShapeNames renders in -- so two Preflight calls
// over identical input produce the identical Alternatives slice, which the
// byte-identical-Serialize property requires of everything on a Plan, this
// field included.
func otherLegalShapes(legal map[types.ExecutionShape]bool, chosen types.ExecutionShape) []types.ExecutionShape {
	var others []types.ExecutionShape
	for _, s := range []types.ExecutionShape{types.ShapeAtomic, types.ShapeMDSP, types.ShapeGlobal, types.ShapeSDMP} {
		if s != chosen && legal[s] {
			others = append(others, s)
		}
	}
	return others
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

// capabilitySnapshot reads the name of the provider a call under ctx would
// route to (never calling it) and resolves the model the call would use,
// through the same routeFor and resolveModel every operation calls -- both are
// pure lookups, no network. With no provider to route to, which is what routeFor
// reports when nothing names one, the snapshot has no provider name and the
// tier's model for none, as it always had.
func capabilitySnapshot(ctx context.Context, opt types.OpOptions) types.CapabilitySnapshot {
	provider, _, err := routeFor(ctx)
	if err != nil {
		provider = nil
	}
	return capabilitySnapshotFor(ctx, opt, provider)
}

// capabilitySnapshotFor is the snapshot for a provider and a context that may
// carry a client's model. The provider may be nil, which is a snapshot for no
// provider. The context window is the one the capability registry declares for
// the route, and DefaultContextTokens when it declares none.
func capabilitySnapshotFor(ctx context.Context, opt types.OpOptions, provider llm.Provider) types.CapabilitySnapshot {
	providerName := ""
	if provider != nil {
		providerName = provider.Name()
	}
	model := resolveModel(ctx, opt, provider)
	maxOutput := opt.MaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = config.GetMaxTokens(opt.Intelligence)
	}
	contextTokens := DefaultContextTokens
	if caps, known := llm.CapabilitiesFor(providerName, model); known && caps.ContextWindow > 0 {
		contextTokens = caps.ContextWindow
	}
	return types.CapabilitySnapshot{
		Provider:        providerName,
		Model:           model,
		MaxOutputTokens: maxOutput,
		ContextTokens:   contextTokens,
	}
}

// itemSize is one item's estimated token footprint, measured once by
// marshaling it -- never guessed from a formula, and never retained past the
// measurement (only the token counts survive into a Plan, never the item).
type itemSize struct {
	tokens int
}

// measureItems marshals each item once to size it for chunk packing: its own
// weight in a chunk, which the packing decides on before any chunk is rendered. A
// marshal failure means the item cannot be sent to a provider at all under this
// protocol, which is an eligibility failure, not a silently-skipped item, and a
// failure of the request and not of the configuration: it is the caller's item
// that cannot be written. The encoder's own message is left out of the reason,
// since for an item with a MarshalJSON of its own it can hold what that function
// reported.
//
// One encoder and one buffer serve every item, so the cost is the encoding itself
// and not an allocation for each item's bytes and again for the string they were
// counted as.
func measureItems[In any](items []In) ([]itemSize, *planRefusal) {
	sizes := make([]itemSize, len(items))
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for i := range items {
		buffer.Reset()
		if err := encoder.Encode(&items[i]); err != nil {
			return nil, &planRefusal{types.KindInvalidRequest, fmt.Sprintf("item %d cannot be encoded for a batch request", i)}
		}
		// Encode ends the value with a newline that json.Marshal does not write.
		sizes[i] = itemSize{tokens: tokensOfBytes(buffer.Len() - 1)}
	}
	return sizes, nil
}

// tokensOfBytes is estimateTokens for a payload known only by its length.
func tokensOfBytes(length int) int {
	return (length + charactersPerToken - 1) / charactersPerToken
}

// chunkBudget is the ceiling chunks are packed against, each bound
// independently checkable so a caller can see which one decided a chunk's
// size. The context limit already covers the input tokens: a bound on input
// tokens alone, or on payload bytes, had no source to be set from and nothing
// set it, so neither exists.
type chunkBudget struct {
	maxItemsPerCall   int
	outputReserve     int
	contextLimit      int
	maxCostPerCallUSD float64

	systemTokens          int
	protocolTokensPerItem int
	outputTokensPerItem   int

	costPerPromptToken     float64
	costPerCompletionToken float64
}

// boundName identifies one of the chunk-size bounds.
type boundName string

const (
	boundItemCount     boundName = "item_count"
	boundOutputReserve boundName = "output_reserve"
	boundContextLimit  boundName = "context_limit"
	boundPerCallCost   boundName = "per_call_cost"
)

// boundViolation reports the first of the bounds a chunk of count items,
// totalling tokens input tokens, would break under budget -- or "" if none.
// Checked in a fixed order so the same overshoot always names the same bound.
func boundViolation(count, tokens int, budget chunkBudget) boundName {
	if budget.maxItemsPerCall > 0 && count > budget.maxItemsPerCall {
		return boundItemCount
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
	if budget.maxCostPerCallUSD > 0 {
		promptTokens := float64(budget.systemTokens + tokens + count*budget.protocolTokensPerItem)
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
// after it: it is routed atomically, and never silently truncated.
func packChunks(sizes []itemSize, budget chunkBudget) ([]types.ChunkPlan, []types.OversizedItem) {
	var chunks []types.ChunkPlan
	var oversized []types.OversizedItem

	i := 0
	for i < len(sizes) {
		if bound := boundViolation(1, sizes[i].tokens, budget); bound != "" {
			oversized = append(oversized, types.OversizedItem{Index: i, Bound: string(bound)})
			i++
			continue
		}

		count := 1
		curTokens := sizes[i].tokens
		var lastBound boundName

		for i+count < len(sizes) {
			nextTokens := curTokens + sizes[i+count].tokens
			bound := boundViolation(count+1, nextTokens, budget)
			if bound != "" {
				lastBound = bound
				break
			}
			curTokens = nextTokens
			count++
		}

		chunks = append(chunks, types.ChunkPlan{ItemCount: count, BoundingRule: string(lastBound)})
		i += count
	}

	return chunks, oversized
}

// planChunks builds the chunkBudget from req and the capability snapshot and
// packs the measured items under it. systemTokens is the size of the system
// prompt every chunk's request carries, which is part of what the model's window
// has to hold beside the items and the answer; the packing does not know it
// until the batch has been asked to render one (planBatch), so it is a
// parameter and zero means not yet known.
func planChunks(sizes []itemSize, req types.PlanRequest, capability types.CapabilitySnapshot, systemTokens int) ([]types.ChunkPlan, []types.OversizedItem) {
	maxItems := req.MaxItemsPerCall
	if maxItems <= 0 {
		maxItems = defaultMaxItemsPerCall
	}

	outputReserve := capability.MaxOutputTokens
	contextLimit := capability.ContextTokens
	if contextLimit <= 0 {
		contextLimit = DefaultContextTokens
	}

	// req.Budget bounds the whole request and is checked against the plan's
	// total estimate (holdToBudget). Here it also bounds each call, since no
	// single call can cost more than the whole request may.
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
		systemTokens:           systemTokens,
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

// renderRefused is the refusal for a descriptor function that panicked while a
// plan was made. It is the kind a step that panics is reported as when it is run
// (stepPanicked), so that a plan refuses for what the run would have failed with.
// The panic's value is not in it, nor in the log: it can hold anything, the
// caller's data included, so only its type is logged.
func renderRefused(ctx context.Context, operation, function, subject string, recovered any) *planRefusal {
	loggerFor(ctx).Error("A descriptor panicked while a plan was made", "operation", operation, "function", function, "panic_type", fmt.Sprintf("%T", recovered))
	return &planRefusal{types.KindInvariantViolation, fmt.Sprintf("op %s: %s panicked on %s, so the plan cannot be priced", operation, function, subject)}
}

// renderItem is the size of the request op sends for one item, as a call counts
// it: the system prompt and the user prompt, each rounded up to whole tokens on
// its own, which is how the size guard and the scheduler count them. BuildPrompt
// is the caller's function; a panic in it is a refusal and not the end of the
// caller's program.
func renderItem[In, Out any](ctx context.Context, op Op[In, Out], item In, index int, opt types.OpOptions, sizer *requestSizer) (tokens int, refusal *planRefusal) {
	defer func() {
		if recovered := recover(); recovered != nil {
			tokens, refusal = 0, renderRefused(ctx, op.ID.String(), "BuildPrompt", fmt.Sprintf("item %d", index), recovered)
		}
	}()
	system, user := op.BuildPrompt(item, opt)
	return sizer.tokens(system, user), nil
}

// systemAsSent is the system prompt of a request, as the call path turns what a
// descriptor renders into what it sends: the reinforcement block in front of it,
// unless the client or a decision declined it. It is the call path's own function,
// so that what a plan counts is what the request carries.
func systemAsSent(ctx context.Context, system string, opt types.OpOptions) string {
	return dec12StrengthenSystemPrompt(ctx, system, resolveResponseFormat(opt.ResponseFormat, system))
}

// userAsSent is the user prompt of a request as the call path sends it: with the
// caller's steering, delimited, in front of it.
func userAsSent(user string, opt types.OpOptions) string {
	return applySteering(user, opt.Steering)
}

// requestSizer counts the tokens of the requests a descriptor renders, as the call
// path sends them. Most descriptors return one system prompt for every item, and
// the reinforcement block in front of it is the same for every request, so the
// system prompt that was sized last is remembered: building the same prompt again
// for each of ten thousand items would allocate for each, which a plan does not do.
type requestSizer struct {
	ctx context.Context
	opt types.OpOptions

	system       string
	systemTokens int
	sized        bool
}

// tokens is what the request for a system and a user prompt is estimated at,
// each as the call path sends it and counted as requestTokens counts it.
func (s *requestSizer) tokens(system, user string) int {
	if !s.sized || s.system != system {
		s.system, s.systemTokens, s.sized = system, estimateTokens(systemAsSent(s.ctx, system, s.opt)), true
	}
	return s.systemTokens + estimateTokens(userAsSent(user, s.opt))
}

// requestTokens is what a request of a system and a user prompt is estimated at:
// each rounded up to whole tokens on its own, which is how the size guard and the
// scheduler (throughScheduler) count the prompts of a call. A plan prices every
// request this way, so what it estimates is what the call is admitted at.
func requestTokens(system, user string) int {
	return estimateTokens(system) + estimateTokens(user)
}

// renderedPromptTokens is the prompt tokens of the requests that send each of
// items alone, as BuildPrompt renders them. It calls BuildPrompt once for each
// item. indexes, when given, says which items they are: the oversized items of a
// batched plan, which are sent alone, and are named by their place in the
// caller's slice; with none, all of them are.
func renderedPromptTokens[In, Out any](ctx context.Context, op Op[In, Out], items []In, indexes []int, opt types.OpOptions) (int, *planRefusal) {
	total := 0
	sizer := &requestSizer{ctx: ctx, opt: opt}
	if indexes == nil {
		for i := range items {
			tokens, refusal := renderItem(ctx, op, items[i], i, opt, sizer)
			if refusal != nil {
				return 0, refusal
			}
			total += tokens
		}
		return total, nil
	}
	for _, i := range indexes {
		tokens, refusal := renderItem(ctx, op, items[i], i, opt, sizer)
		if refusal != nil {
			return 0, refusal
		}
		total += tokens
	}
	return total, nil
}

// renderedChunk is what the batch renders for one chunk, in tokens.
type renderedChunk struct {
	system int
	user   int
}

// renderChunk asks the batch's Encode for the request that carries chunk, and
// sizes it. Encode is the caller's function: an error from it is refused with
// the kind it carries, as RunOpBatch refuses it, or a configuration error when it
// carries none; a panic in it is refused as BuildPrompt's is. The reason names
// the kind and the number of items and nothing Encode said.
func renderChunk[In, Out any](ctx context.Context, op Op[In, Out], chunk []In, opt types.OpOptions) (rendered renderedChunk, refusal *planRefusal) {
	defer func() {
		if recovered := recover(); recovered != nil {
			rendered, refusal = renderedChunk{}, renderRefused(ctx, op.ID.String(), "Encode", fmt.Sprintf("a chunk of %d items", len(chunk)), recovered)
		}
	}()
	system, user, err := op.Batch.Encode(chunk, opt)
	if err != nil {
		kind := types.KindOf(err)
		if kind == types.KindUnknown {
			kind = types.KindConfiguration
		}
		return renderedChunk{}, &planRefusal{kind, fmt.Sprintf("op %s: Encode failed (%s) on a chunk of %d items, so the plan cannot be priced", op.ID, kind, len(chunk))}
	}
	return renderedChunk{system: estimateTokens(systemAsSent(ctx, system, opt)), user: estimateTokens(userAsSent(user, opt))}, nil
}

// chunkedItems returns the items that go in chunks, in order: every item that is
// not oversized. The planned chunks are cut from this sequence one after another,
// as runManyBatched cuts them, and not from the caller's slice, which an
// oversized item can sit in the middle of. The slice is a copy, so a function of
// the caller's that changes the items it is handed changes the plan's copy and not
// the caller's.
func chunkedItems[In any](items []In, oversized []types.OversizedItem) []In {
	if len(oversized) == 0 {
		return append([]In(nil), items...)
	}
	skip := make(map[int]bool, len(oversized))
	for _, o := range oversized {
		skip[o.Index] = true
	}
	kept := make([]In, 0, len(items)-len(oversized))
	for i := range items {
		if !skip[i] {
			kept = append(kept, items[i])
		}
	}
	return kept
}

// planBatch cuts the items into chunks and prices them by what the batch renders:
// the system prompt and the encoding of each chunk, as Encode returns them, and
// BuildPrompt for each oversized item sent alone. It returns the chunks, the
// oversized items and the prompt tokens of every request they come to.
//
// The chunks have to leave room in the model's window for the system prompt, and
// the system prompt is the batch's to say. So the first chunk, cut without it, is
// rendered first, and its system prompt is what the chunks are cut with. When that
// changes the chunks, they are cut again and every one of them is rendered: a
// chunk is rendered once, and the first one is not rendered again when the second
// cutting leaves it as it was. At most one chunk is rendered that is not in the
// plan, and only when the system prompt changed the first.
//
// Each chunk is priced by its own rendering. The system prompt the chunks are cut
// with is the first chunk's, which is the batch's whether or not a descriptor's
// Encode could vary it, and what a chunk's own system prompt costs is counted in
// that chunk.
func planBatch[In, Out any](ctx context.Context, op Op[In, Out], items []In, sizes []itemSize, req types.PlanRequest, capability types.CapabilitySnapshot) ([]types.ChunkPlan, []types.OversizedItem, int, *planRefusal) {
	opt := req.Options
	chunks, oversized := planChunks(sizes, req, capability, 0)

	var sequence []In
	var first renderedChunk
	var haveFirst bool
	if len(chunks) > 0 {
		sequence = chunkedItems(items, oversized)
		var refusal *planRefusal
		first, refusal = renderChunk(ctx, op, sequence[:chunks[0].ItemCount:chunks[0].ItemCount], opt)
		if refusal != nil {
			return nil, nil, 0, refusal
		}
		haveFirst = true

		if first.system > 0 {
			recut, recutOversized := planChunks(sizes, req, capability, first.system)
			// The system prompt only takes room, so a second cutting has the
			// oversized items of the first and perhaps more. With as many, the
			// items in chunks are the same, and the first chunk is the same one
			// when it holds as many.
			sameItems := len(recutOversized) == len(oversized)
			haveFirst = sameItems && len(recut) > 0 && recut[0].ItemCount == chunks[0].ItemCount
			if !sameItems {
				sequence = chunkedItems(items, recutOversized)
			}
			chunks, oversized = recut, recutOversized
		}
	}

	prompt := 0
	offset := 0
	for k, chunk := range chunks {
		rendered := first
		if k > 0 || !haveFirst {
			var refusal *planRefusal
			rendered, refusal = renderChunk(ctx, op, sequence[offset:offset+chunk.ItemCount:offset+chunk.ItemCount], opt)
			if refusal != nil {
				return nil, nil, 0, refusal
			}
		}
		prompt += rendered.system + rendered.user
		offset += chunk.ItemCount
	}

	if len(oversized) > 0 {
		indexes := make([]int, len(oversized))
		for i, o := range oversized {
			indexes[i] = o.Index
		}
		alone, refusal := renderedPromptTokens(ctx, op, items, indexes, opt)
		if refusal != nil {
			return nil, nil, 0, refusal
		}
		prompt += alone
	}
	return chunks, oversized, prompt, nil
}

// estimateChunkedCost prices a batched plan: the prompt tokens of the requests as
// the descriptor renders them (planBatch), and the answers. A chunked item's
// answer is the planning assumption for what one item of a batch costs to answer,
// and an oversized item, sent alone, is estimated at the model's ceiling as every
// item of an atomic plan is.
func estimateChunkedCost(promptTokens int, chunks []types.ChunkPlan, oversized []types.OversizedItem, capability types.CapabilitySnapshot) types.CostEstimate {
	completionTokens := 0
	for _, c := range chunks {
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

// PlanBuilder assembles a types.PlanRequest. Atomic, Batched, and Adaptive
// are its three named forcing points: forcing atomicity for risk
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

// WithBudget sets the ceiling on the whole request's estimated cost, in the
// pricing currency. Preflight refuses a plan whose estimate is over it.
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

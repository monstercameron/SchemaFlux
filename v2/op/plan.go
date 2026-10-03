package op

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Plan is what Preflight intends to do for a descriptor and its items, and
// what it expects that to cost, worked out without sending anything. It holds
// shapes, counts and numbers and never an item or a prompt, so it can be logged,
// stored and compared. Two plans made from the same descriptor, the same prompts
// and the same client are equal, byte for byte, when serialized.
//
// A plan that cannot be carried out is returned like any other, with Eligible
// false and the reason, so that a caller reads one place for every refusal.
type Plan struct {
	// Operation is the descriptor's identity.
	Operation ID `json:"operation"`

	// InputShape describes the items without reproducing them: a type and a
	// count, such as "[]string of 240".
	InputShape string `json:"input_shape"`

	// ItemCount is how many items the plan is for.
	ItemCount int `json:"item_count"`

	// Eligible reports that the run can be carried out as planned. False says
	// IneligibleReason why: a shape the descriptor cannot run was forced, the
	// descriptor cannot be priced because a function of it panicked or an item
	// cannot be encoded, or the estimate is over PlanOptions.Budget.
	Eligible bool `json:"eligible"`

	// IneligibleReason says, in the library's words and never in the descriptor's
	// or the items', why the plan is not eligible. Empty for an eligible plan.
	IneligibleReason string `json:"ineligible_reason,omitempty"`

	// IneligibleKind is the kind of error RunMany fails with for this plan:
	// ErrBudgetExceeded for a plan over its budget, ErrInvalidRequest for an item
	// that cannot be encoded, ErrInternal for a function of the descriptor that
	// panicked, ErrConfiguration for the rest. The zero value for an eligible
	// plan.
	IneligibleKind schemaflux.Kind `json:"ineligible_kind,omitzero"`

	// ShapeForced reports that Shape is what PlanOptions.Force asked for and not
	// the planner's choice.
	ShapeForced bool `json:"shape_forced"`

	// Shape is how the run is carried out.
	Shape Shape `json:"shape"`

	// ShapeReason says why that shape: the caller's ForceReason when it was
	// forced, the planner's own account otherwise.
	ShapeReason string `json:"shape_reason,omitempty"`

	// Alternatives lists each other shape the planner considered, with why it was
	// not chosen. Empty when there was nothing else to consider.
	Alternatives []alternative `json:"alternatives,omitempty"`

	// Chunks are the requests of a batched or global run, in order, each with how
	// many items it holds and what decided its size. Empty for an atomic run.
	Chunks []ChunkPlan `json:"chunks,omitempty"`

	// OversizedItems are the items no chunk can hold, which a batched run sends
	// each in a request of its own.
	OversizedItems []OversizedItem `json:"oversized_items,omitempty"`

	// MaxCalls is the most requests the run makes if every one is answered the
	// first time: one for each item of an atomic run, one for each chunk and each
	// oversized item of a batched one. A repair or a retry is a request more.
	MaxCalls int `json:"max_calls"`

	// EstimatedCost is what the run is expected to cost.
	EstimatedCost CostEstimate `json:"estimated_cost"`

	// Policy is the settings of the call the plan was made for.
	Policy PolicySnapshot `json:"policy"`

	// Capability is the provider and model the plan was made for.
	Capability CapabilitySnapshot `json:"capability"`

	// EstimatorVersion identifies the arithmetic of the estimate: a plan made
	// under one version and compared with one made under another compares two
	// different questions.
	EstimatorVersion string `json:"estimator_version"`

	// PolicyDigest is a content hash of Policy, for comparing the settings of two
	// plans without comparing each field.
	PolicyDigest string `json:"policy_digest"`

	// CapabilityDigest is a content hash of Capability, for comparing the
	// provider and model of two plans.
	CapabilityDigest string `json:"capability_digest"`
}

// alternative is a shape the planner considered and did not choose, and why.
type alternative = struct {
	Shape  Shape  `json:"shape"`
	Reason string `json:"reason"`
}

// ChunkPlan is one request of a batched or global run.
type ChunkPlan struct {
	// ItemCount is how many items the request carries.
	ItemCount int `json:"item_count"`

	// BoundingRule names the bound that decided the chunk's size: "item_count",
	// "input_tokens", "output_reserve", "context_limit", "per_call_cost" or
	// "payload_bytes". Empty means every remaining item fit.
	BoundingRule string `json:"bounding_rule,omitempty"`
}

// OversizedItem is an item no chunk can hold on its own. It is never silently
// truncated: it is named here and sent in a request of its own.
type OversizedItem struct {
	// Index is the item's position in the slice the caller passed.
	Index int `json:"index"`

	// Bound names the bound it broke, as ChunkPlan.BoundingRule does.
	Bound string `json:"bound"`
}

// CostEstimate is a plan's read of what a run will cost. Check Priced before
// EstimatedCost: a model the library has no price for is unpriced, which is not a
// cost of zero.
type CostEstimate struct {
	// Priced reports that a price for the model was found. False says nothing is
	// known of the rate, and EstimatedCost is then meaningless.
	Priced bool `json:"priced"`

	// EstimatedCost is the expected cost in Currency.
	EstimatedCost float64 `json:"estimated_cost"`

	// Currency is the currency of EstimatedCost. Empty when it is unpriced.
	Currency string `json:"currency,omitempty"`

	// PricingSource names the price table entry the figure came from.
	PricingSource string `json:"pricing_source,omitempty"`

	// EstimatedPromptTokens is the tokens the requests are expected to send: the
	// system and the user prompt of each, as the library sends them.
	EstimatedPromptTokens int `json:"estimated_prompt_tokens"`

	// EstimatedCompletionTokens is the tokens of answers the plan allows for, at
	// the output ceiling of the tier and not at what the model will write: the
	// estimate is an upper bound on the answers.
	EstimatedCompletionTokens int `json:"estimated_completion_tokens"`
}

// PolicySnapshot is the settings of a call that change what the planner decides,
// with the caller's own text left out: whether Steering was set and never what it
// said.
type PolicySnapshot struct {
	// Mode is the call's Mode, zero for none.
	Mode schemaflux.Mode `json:"mode"`

	// Tier is the call's Tier, zero for none.
	Tier schemaflux.Tier `json:"tier"`

	// MaxOutputTokens is the ceiling set on the call, zero for the tier's.
	MaxOutputTokens int `json:"max_output_tokens"`

	// HasSteering reports that the call had Steering.
	HasSteering bool `json:"has_steering"`

	// ResponseFormat is the call's ResponseFormat.
	ResponseFormat string `json:"response_format,omitempty"`
}

// CapabilitySnapshot is what the planner knew of the provider and the model it
// planned for, at the moment it planned. Nothing was sent to learn it.
type CapabilitySnapshot struct {
	// Provider is the name of the provider the run would go to, empty when there
	// is none.
	Provider string `json:"provider,omitempty"`

	// Model is the model the run would use.
	Model string `json:"model,omitempty"`

	// MaxOutputTokens is the ceiling on an answer.
	MaxOutputTokens int `json:"max_output_tokens"`

	// ContextTokens is the size of the model's context window, or the library's
	// conservative figure when none is declared for the model.
	ContextTokens int `json:"context_tokens"`
}

// Coverage is how an answer to a batched request measured up against the ids the
// request offered, in counts: the ids themselves are the request's own and mean
// nothing to a reader, and the ones the model invented are its text. The id
// protocol requires every offered id answered exactly once.
type Coverage struct {
	// Offered is how many ids the request carried.
	Offered int `json:"offered"`

	// Answered is how many elements the answer held.
	Answered int `json:"answered"`

	// Missing is how many offered ids the answer never names.
	Missing int `json:"missing"`

	// Duplicate is how many offered ids the answer names more than once.
	Duplicate int `json:"duplicate"`

	// Unknown is how many ids the answer names that were not offered.
	Unknown int `json:"unknown"`
}

// OK reports whether the answer covered every offered id exactly once and named
// no other.
func (c Coverage) OK() bool { return c.Missing == 0 && c.Duplicate == 0 && c.Unknown == 0 }

// Complete reports whether every offered id was answered exactly once, whatever
// else the answer held: an element with an id nobody offered fills no item and
// leaves none unanswered.
func (c Coverage) Complete() bool { return c.Missing == 0 && c.Duplicate == 0 }

// String renders the coverage as counts, and is empty when OK.
func (c Coverage) String() string {
	if c.OK() {
		return ""
	}
	return fmt.Sprintf("%d offered, %d answered, %d missing, %d duplicated, %d unknown",
		c.Offered, c.Answered, c.Missing, c.Duplicate, c.Unknown)
}

// MarshalText writes the shape as its name, so that a Plan serializes with names
// and not numbers.
func (s Shape) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// planOf is the Plan of an internal one. The kind of a refusal is the root's Kind
// for the internal kind, which the registered conversion decides.
func planOf(p types.Plan) Plan {
	plan := Plan{
		Operation:        ID{Name: p.Operation.Name, Version: p.Operation.Version},
		InputShape:       p.InputShape,
		ItemCount:        p.ItemCount,
		Eligible:         p.Eligible,
		IneligibleReason: p.IneligibleReason,
		ShapeForced:      p.ShapeForced,
		Shape:            shapeOf(p.Shape),
		ShapeReason:      p.ShapeReason,
		MaxCalls:         p.MaxCalls,
		EstimatedCost: CostEstimate{
			Priced:                    p.EstimatedCost.Priced,
			EstimatedCost:             p.EstimatedCost.EstimatedCost,
			Currency:                  p.EstimatedCost.Currency,
			PricingSource:             p.EstimatedCost.PricingSource,
			EstimatedPromptTokens:     p.EstimatedCost.EstimatedPromptTokens,
			EstimatedCompletionTokens: p.EstimatedCost.EstimatedCompletionTokens,
		},
		Policy: PolicySnapshot{
			Mode:            modeOf(p.Policy.Mode),
			Tier:            tierOf(p.Policy.Intelligence),
			MaxOutputTokens: p.Policy.MaxOutputTokens,
			HasSteering:     p.Policy.HasSteering,
			ResponseFormat:  p.Policy.ResponseFormat,
		},
		Capability: CapabilitySnapshot{
			Provider:        p.Capability.Provider,
			Model:           p.Capability.Model,
			MaxOutputTokens: p.Capability.MaxOutputTokens,
			ContextTokens:   p.Capability.ContextTokens,
		},
		EstimatorVersion: p.EstimatorVersion,
	}
	if !p.Eligible && p.IneligibleKind != types.KindUnknown {
		plan.IneligibleKind = schemaflux.Kind(bind.KindOf(p.IneligibleKind, types.NarrowNone))
	}
	for _, alt := range p.Alternatives {
		plan.Alternatives = append(plan.Alternatives, alternative{Shape: shapeOf(alt.Shape), Reason: alt.Reason})
	}
	for _, chunk := range p.Chunks {
		plan.Chunks = append(plan.Chunks, ChunkPlan{ItemCount: chunk.ItemCount, BoundingRule: chunk.BoundingRule})
	}
	for _, item := range p.OversizedItems {
		plan.OversizedItems = append(plan.OversizedItems, OversizedItem{Index: item.Index, Bound: item.Bound})
	}
	plan.PolicyDigest = digestOf(plan.Policy)
	plan.CapabilityDigest = digestOf(plan.Capability)
	return plan
}

// digestOf is the hex SHA-256 of the JSON of v, or "undigestable" for a value
// JSON cannot write. Every value it is given is plain data.
func digestOf(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "undigestable"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Serialize writes the plan as JSON. It holds no value of the caller's, and two
// plans made from the same inputs serialize to the same bytes.
func (p Plan) Serialize() ([]byte, error) { return json.Marshal(p) }

// Digest is a content hash of the whole plan: two plans with the same Digest made
// the same decision, the estimate included.
func (p Plan) Digest() string { return digestOf(p) }

// Explain writes the plan in words, a fact to a line: the shape and why, the
// requests, the most calls, the estimated cost and each shape that was not
// chosen. It holds nothing of the items.
func (p Plan) Explain() string {
	if !p.Eligible {
		return fmt.Sprintf("%s: not eligible, %s", p.Operation, p.IneligibleReason)
	}
	var lines []string
	first := fmt.Sprintf("%s: %d item(s), shape %s", p.Operation, p.ItemCount, p.Shape)
	if p.ShapeForced {
		first += fmt.Sprintf(" (forced: %s)", p.ShapeReason)
	} else {
		first += fmt.Sprintf(" (%s)", p.ShapeReason)
	}
	lines = append(lines, first)
	if len(p.Chunks) > 0 {
		lines = append(lines, fmt.Sprintf("%d chunk(s)", len(p.Chunks)))
	}
	if len(p.OversizedItems) > 0 {
		lines = append(lines, fmt.Sprintf("%d item(s) sent alone (oversized)", len(p.OversizedItems)))
	}
	lines = append(lines, fmt.Sprintf("at most %d call(s)", p.MaxCalls))
	if p.EstimatedCost.Priced {
		lines = append(lines, fmt.Sprintf("estimated %s %.6f", p.EstimatedCost.Currency, p.EstimatedCost.EstimatedCost))
	} else {
		lines = append(lines, "cost unpriced")
	}
	for _, alt := range p.Alternatives {
		lines = append(lines, fmt.Sprintf("not chosen: %s, %s", alt.Shape, alt.Reason))
	}
	return strings.Join(lines, "\n")
}

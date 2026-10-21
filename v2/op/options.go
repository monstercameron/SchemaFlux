package op

import (
	"math"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Options are the settings of a call of Run or RunBatch: the seven settings every
// operation takes, and the three a descriptor's author may need, which a built-in
// operation does not offer. The zero value is the defaults.
//
// Several values passed to one call are merged from left to right: a field that
// is not its zero value in a later value replaces the earlier one, and a field at
// its zero leaves the earlier one alone. A value cannot set a field back to its
// zero.
type Options struct {
	schemaflux.Options

	// JSONSchema is the schema the provider is asked to enforce, which the
	// descriptor's functions are given as it is. Nil sends the request with no
	// schema, and the answer is then held to no more than that it parses: a
	// descriptor that names a schema in Contract.SchemaName and is run with none
	// is delivered at ContractJSONWellFormed. The library does not check that the
	// schema describes what Contract.Decode reads.
	JSONSchema map[string]any

	// SchemaName names the schema in the request, for a provider that requires a
	// name. Empty sends none, and the provider uses its own.
	SchemaName string

	// ResponseFormat is "json" or "text", the kind of answer the request asks
	// for. Empty means the library infers it from the system prompt. Any other
	// value is a configuration error.
	ResponseFormat string
}

// PlanOptions are the settings of RunMany and Preflight: the ten of Options, and
// what the planner may be told. The zero value is the defaults, and the planner
// chooses the shape.
//
// Several values passed to one call are merged as Options are.
type PlanOptions struct {
	Options

	// Force is the shape to run the items in. The zero value, ShapeUnspecified,
	// lets the planner choose. A shape the descriptor cannot run, such as
	// ShapeBatched for a descriptor with no batch, or ShapeSplit, makes the plan
	// ineligible and RunMany fail with ErrConfiguration; the planner never
	// substitutes another. A value that is not a Shape is a configuration error.
	Force Shape

	// ForceReason is why Force was set, kept in the plan's ShapeReason as it was
	// written. Empty means the plan says the caller forced the shape. It has no
	// effect when Force is the zero value.
	ForceReason string

	// Budget is the most the whole run may cost, in dollars, which the plan has to
	// fit: a plan whose estimate is over it, or that cannot be compared with it
	// because the model has no price, is ineligible and RunMany fails with
	// ErrBudgetExceeded before anything is sent. Zero means no ceiling. A negative
	// number, NaN and an infinity are configuration errors.
	Budget float64

	// MaxItemsPerCall is the most items a batched request may carry, whatever the
	// token arithmetic allows. Zero means the planner's own bound. A negative
	// value is a configuration error.
	MaxItemsPerCall int
}

// mergeCommon merges the seven settings of two values of the root's Options, the
// later laid over the earlier where it is not its zero value. It is written out
// because the root's own merge is not exported; a test holds it to every field.
func mergeCommon(o, later schemaflux.Options) schemaflux.Options {
	if later.Model != "" {
		o.Model = later.Model
	}
	if later.Tier != 0 {
		o.Tier = later.Tier
	}
	if later.Effort != "" {
		o.Effort = later.Effort
	}
	if later.Mode != 0 {
		o.Mode = later.Mode
	}
	if later.Steering != "" {
		o.Steering = later.Steering
	}
	if later.MaxOutputTokens != 0 {
		o.MaxOutputTokens = later.MaxOutputTokens
	}
	if later.WebSearch {
		o.WebSearch = true
	}
	return o
}

// merge returns o with every field that is not its zero value in later laid over
// it.
func (o Options) merge(later Options) Options {
	o.Options = mergeCommon(o.Options, later.Options)
	if later.JSONSchema != nil {
		o.JSONSchema = later.JSONSchema
	}
	if later.SchemaName != "" {
		o.SchemaName = later.SchemaName
	}
	if later.ResponseFormat != "" {
		o.ResponseFormat = later.ResponseFormat
	}
	return o
}

// mergeOptions merges the values of a call (M0): none is the zero value, one is
// returned as it is, and several are merged from left to right.
func mergeOptions(opts []Options) Options {
	switch len(opts) {
	case 0:
		return Options{}
	case 1:
		return opts[0]
	}
	merged := opts[0]
	for _, later := range opts[1:] {
		merged = merged.merge(later)
	}
	return merged
}

// mergePlanOptions merges the values of a call as mergeOptions does.
func mergePlanOptions(opts []PlanOptions) PlanOptions {
	switch len(opts) {
	case 0:
		return PlanOptions{}
	case 1:
		return opts[0]
	}
	merged := opts[0]
	for _, later := range opts[1:] {
		merged.Options = merged.Options.merge(later.Options)
		if later.Force != ShapeUnspecified {
			merged.Force = later.Force
		}
		if later.ForceReason != "" {
			merged.ForceReason = later.ForceReason
		}
		if later.Budget != 0 {
			merged.Budget = later.Budget
		}
		if later.MaxItemsPerCall != 0 {
			merged.MaxItemsPerCall = later.MaxItemsPerCall
		}
	}
	return merged
}

// checkOptions returns the common settings of o in the internal types, and the
// error of kind configuration for a setting no call can use, which the call ends
// with. The error is the root's own when the root has registered itself and
// the internal error otherwise, and either is converted with the operation's name
// when the call ends. The checks are made on the caller's own values.
func checkOptions(o Options, op string) (bind.Common, error) {
	var common bind.Common
	if o.Options != (schemaflux.Options{}) {
		// The settings pass through the root's conversion as an interface value,
		// which costs an allocation. The zero Options is the zero Common and has
		// nothing to check, so a call with no settings does not pay it.
		var err error
		if common, err = bind.CommonOf(o.Options); err != nil {
			return common, err
		}
	}
	switch o.ResponseFormat {
	case "", "json", "text":
	default:
		return common, types.Newf(types.KindConfiguration, op, `ResponseFormat is not one of "json" and "text"`)
	}
	return common, nil
}

// checkPlanOptions is checkOptions for the settings of a plan.
func checkPlanOptions(o PlanOptions, op string) (bind.Common, error) {
	common, err := checkOptions(o.Options, op)
	if err != nil {
		return common, err
	}
	switch {
	case !o.Force.valid():
		return common, types.Newf(types.KindConfiguration, op, "Force is not one of the Shape constants")
	case math.IsNaN(o.Budget) || math.IsInf(o.Budget, 0):
		return common, types.Newf(types.KindConfiguration, op, "Budget must be a number")
	case o.Budget < 0:
		return common, types.Newf(types.KindConfiguration, op, "Budget must not be negative")
	case o.MaxItemsPerCall < 0:
		return common, types.Newf(types.KindConfiguration, op, "MaxItemsPerCall must not be negative")
	}
	return common, nil
}

// planRequest is the internal request of a plan: the options of the call on ctx's
// behalf and what the planner may be told.
func planRequest(opt types.OpOptions, o PlanOptions) types.PlanRequest {
	return types.PlanRequest{
		Options:         opt,
		Force:           o.Force.internal(),
		ForceReason:     o.ForceReason,
		Budget:          o.Budget,
		MaxItemsPerCall: o.MaxItemsPerCall,
	}
}

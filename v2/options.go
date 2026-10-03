package schemaflux

import (
	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Options are the settings every operation takes. The zero value means the
// defaults of the operation it is passed to.
//
// An operation that has no settings of its own takes Options. An operation with
// settings of its own has its own options type, which embeds Options and
// declares only the settings that are its own, so a literal sets either without
// naming the embedded type:
//
//	schemaflux.SummarizeOptions{Model: "gpt-6-luna", MaxWords: 50}
//
// Several values passed to one call are merged from left to right: a field that
// is not its zero value in a later value replaces the earlier one, and a field
// at its zero value leaves the earlier one alone. A value cannot set a field
// back to its zero: a WebSearch that an earlier value turned on stays on.
type Options struct {
	// Model pins the exact model for this call. Empty means the client's model
	// if it names one, else the model the Tier maps to.
	Model string

	// Tier is the quality and speed class the model is chosen from when no
	// Model is pinned, and it sets the default ceiling on the answer's length.
	// The zero value means the operation's own default, which its documentation
	// states. When the client names a Model the tier does not choose the model:
	// it still sets the output ceiling. Pin Model to change the model. A value
	// that is not one of the three tiers is a configuration error.
	Tier Tier

	// Effort is how much the model reasons before it answers. The zero value
	// leaves it to the client, then to the operation, then to the provider. A
	// value the route does not take is an error of kind ErrUnsupported, before
	// any request is sent.
	Effort Effort

	// Mode is the reasoning approach, which sets the sampling temperature. The
	// zero value means the operation's own default. A value that is not one of
	// the three modes is a configuration error.
	Mode Mode

	// Steering is guidance in plain language, sent with the request as the
	// caller's own text. Empty means none.
	Steering string

	// MaxOutputTokens is the ceiling on the answer's length, in tokens. Zero
	// means the Tier's ceiling. A negative value is a configuration error.
	MaxOutputTokens int

	// WebSearch declares the provider's web-search tool on the call; the model
	// decides whether to search. False sends the request without it.
	WebSearch bool
}

// Tier is a class of model: the best, the balanced, the fastest. The zero value
// is no tier, which means the operation's own default.
type Tier int

const (
	// TierSmart is the tier for complex reasoning and critical decisions.
	TierSmart Tier = iota + 1

	// TierFast is the tier most operations default to.
	TierFast

	// TierQuick is the tier for real-time and high-volume work.
	TierQuick
)

// Mode is the reasoning approach of a call. The zero value is no mode, which
// means the operation's own default.
type Mode int

const (
	// ModeStrict asks for exact answers, at a sampling temperature of 0.1. For
	// Extract it also refuses a property the type does not name and an
	// answer with a required field left empty.
	ModeStrict Mode = iota + 1

	// ModeTransform asks for answers that map between related concepts, at a
	// sampling temperature of 0.3.
	ModeTransform

	// ModeCreative allows open-ended generation, at a sampling temperature of
	// 0.7.
	ModeCreative
)

// Effort is how much a model reasons before it answers. The words are the
// provider's own, and which of them a model takes depends on its family. The
// zero value, the empty string, leaves the effort to the client, the operation
// and the provider.
type Effort string

const (
	// EffortNone turns reasoning off: the fastest, and not to be used for
	// arithmetic.
	EffortNone Effort = "none"

	// EffortMinimal reasons as little as a model that reasons can.
	EffortMinimal Effort = "minimal"

	// EffortLow reasons briefly.
	EffortLow Effort = "low"

	// EffortMedium is the middle setting.
	EffortMedium Effort = "medium"

	// EffortHigh reasons at length.
	EffortHigh Effort = "high"

	// EffortXHigh reasons longer than EffortHigh, on the models that have it.
	EffortXHigh Effort = "xhigh"

	// EffortMax reasons as much as the model allows, on the models that have
	// it.
	EffortMax Effort = "max"
)

// merge returns o with every field that is not its zero value in later laid
// over it. It is the one merge of the common settings; an operation's options
// type merges its embedded Options with it and its own fields with a function
// of its own. It reads no reflection.
func (o Options) merge(later Options) Options {
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

// mergeOptions merges the values of a call from left to right. No value is the
// zero Options, and one value is returned as it is.
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

// check returns the configuration error for a setting no call can use, or nil.
// op is the operation the error names. It is bind.Common's check, which is the
// one place that decides what no call can use: a negative ceiling and a Tier or
// a Mode that is not one of the constants. A zero field is always acceptable. An
// effort the route does not take is not checked here: only the route knows which
// words it takes.
func (o Options) check(op string) error { return o.common().Check(op) }

// common converts the seven settings to the internal types, by value.
func (o Options) common() bind.Common {
	return bind.Common{
		Model:           o.Model,
		Tier:            o.Tier.internal(),
		Effort:          types.Effort(o.Effort),
		Mode:            o.Mode.internal(),
		Steering:        o.Steering,
		MaxOutputTokens: o.MaxOutputTokens,
		WebSearch:       o.WebSearch,
	}
}

// optionsFromCommon is the inverse of common.
func optionsFromCommon(c bind.Common) Options {
	return Options{
		Model:           c.Model,
		Tier:            tierFromInternal(c.Tier),
		Effort:          Effort(c.Effort),
		Mode:            modeFromInternal(c.Mode),
		Steering:        c.Steering,
		MaxOutputTokens: c.MaxOutputTokens,
		WebSearch:       c.WebSearch,
	}
}

// internal is the internal tier of t. A value that is not a tier keeps its
// number, so that check, which refuses it, and not the conversion, decides.
func (t Tier) internal() types.Speed {
	switch t {
	case 0:
		return types.TierUnset
	case TierSmart:
		return types.Smart
	case TierFast:
		return types.Fast
	case TierQuick:
		return types.Quick
	}
	return types.Speed(t)
}

// tierFromInternal is the inverse of Tier.internal.
func tierFromInternal(s types.Speed) Tier {
	switch s {
	case types.TierUnset:
		return 0
	case types.Smart:
		return TierSmart
	case types.Fast:
		return TierFast
	case types.Quick:
		return TierQuick
	}
	return Tier(s)
}

// internal is the internal mode of m, with the same rule for a value that is
// not a mode as Tier.internal.
func (m Mode) internal() types.Mode {
	switch m {
	case 0:
		return types.ModeUnset
	case ModeStrict:
		return types.Strict
	case ModeTransform:
		return types.TransformMode
	case ModeCreative:
		return types.Creative
	}
	return types.Mode(m)
}

// modeFromInternal is the inverse of Mode.internal.
func modeFromInternal(m types.Mode) Mode {
	switch m {
	case types.ModeUnset:
		return 0
	case types.Strict:
		return ModeStrict
	case types.TransformMode:
		return ModeTransform
	case types.Creative:
		return ModeCreative
	}
	return Mode(m)
}

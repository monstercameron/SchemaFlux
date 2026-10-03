package bind

import (
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Common is the seven settings every options type of version 2 carries, in the
// internal types. The root's Options converts to it by value, and every
// operation function maps its common settings through Apply, so that there is
// one place where a field of Options reaches an internal call.
//
// The zero value of every field means "not set": the operation's own default,
// then the client's, then the provider's, in the order the root documents.
type Common struct {
	// Model pins the exact model. Empty means no pin.
	Model string

	// Tier is the quality and speed class, which picks the model when none is
	// pinned and sets the default output ceiling. types.TierUnset, the zero
	// value, means the operation's own default.
	Tier types.Speed

	// Effort is how much the model reasons. The empty string leaves it to the
	// client, the operation and the provider.
	Effort types.Effort

	// Mode is the reasoning approach, which sets the sampling temperature.
	// types.ModeUnset, the zero value, means the operation's own default.
	Mode types.Mode

	// Steering is the caller's guidance in plain language. Empty means none.
	Steering string

	// MaxOutputTokens is the ceiling on the answer's length. Zero means the
	// tier's ceiling. A negative value is refused by Check and is not a
	// setting.
	MaxOutputTokens int

	// WebSearch declares the provider's web-search tool on the call. False
	// sends the request without it.
	WebSearch bool
}

// Check returns the error of a Common that no call can use, named for the
// operation op, or nil. It is nil for a zero field always. The ones it refuses
// are a negative MaxOutputTokens, a Tier that is not one of the three tiers and
// a Mode that is not one of the three modes. The error is the root's *Error of
// kind configuration when the root has registered its hooks (ConvertError), and
// the internal error otherwise. It reads nothing about the effort: only the
// route knows which words it takes.
//
// An operation function calls Check before it calls Apply and before it asks
// any model anything. A valid Common allocates nothing.
func (c Common) Check(op string) error {
	switch {
	case c.MaxOutputTokens < 0:
		return ConvertError(op, types.Newf(types.KindConfiguration, op, "MaxOutputTokens must not be negative"))
	case !knownTier(c.Tier):
		return ConvertError(op, types.Newf(types.KindConfiguration, op, "Tier is not one of TierSmart, TierFast and TierQuick"))
	case !knownMode(c.Mode):
		return ConvertError(op, types.Newf(types.KindConfiguration, op, "Mode is not one of ModeStrict, ModeTransform and ModeCreative"))
	}
	return nil
}

// Apply lays c over dst: each of the seven settings that is not its zero value
// replaces the matching field of dst, and each that is its zero value leaves
// dst's field as it was. That is the mapping rule of every operation: an empty
// setting can never blank a line of a prompt or a default the operation built
// into dst before this call, and a boolean that is false never turns a true
// one off. No other field of dst is read or written.
//
// The Tier is written to dst.Intelligence, which is where the internal options
// keep it. A negative MaxOutputTokens is written as it is, as any non-zero
// value is; Check is what refuses it, and runs first. Apply is safe to call
// again with the same Common and never allocates.
func (c Common) Apply(dst *types.OpOptions) {
	if c.Model != "" {
		dst.Model = c.Model
	}
	if c.Tier != types.TierUnset {
		dst.Intelligence = c.Tier
	}
	if c.Effort != types.EffortDefault {
		dst.Effort = c.Effort
	}
	if c.Mode != types.ModeUnset {
		dst.Mode = c.Mode
	}
	if c.Steering != "" {
		dst.Steering = c.Steering
	}
	if c.MaxOutputTokens != 0 {
		dst.MaxOutputTokens = c.MaxOutputTokens
	}
	if c.WebSearch {
		dst.WebSearch = true
	}
}

// ApplyHalves lays c over the two halves of an options type of internal/ops that
// embeds both ops.CommonOptions and types.OpOptions (the flat class H2). The
// internal call reads the two halves together and lets the CommonOptions half
// win when it says something, and the constructors of those types set Mode and
// Intelligence on that half, so a setting written to the embedded half alone
// would be covered by the constructor's default and lost. Each of the seven
// settings that is not its zero value is therefore written to every half that
// has the field: Steering, Mode, Tier, Model and Effort to both, MaxOutputTokens
// and WebSearch, which only the embedded half has, to that one. A zero setting
// changes neither half, and a nil half is skipped. No other field of either half
// is read or written. It never allocates.
func (c Common) ApplyHalves(common *ops.CommonOptions, embedded *types.OpOptions) {
	if common != nil {
		if c.Model != "" {
			common.Model = c.Model
		}
		if c.Tier != types.TierUnset {
			common.Intelligence = c.Tier
		}
		if c.Effort != types.EffortDefault {
			common.Effort = c.Effort
		}
		if c.Mode != types.ModeUnset {
			common.Mode = c.Mode
		}
		if c.Steering != "" {
			common.Steering = c.Steering
		}
	}
	if embedded != nil {
		c.Apply(embedded)
	}
}

// knownTier reports whether t is no tier or one of the three tiers.
func knownTier(t types.Speed) bool {
	switch t {
	case types.TierUnset, types.Smart, types.Fast, types.Quick:
		return true
	}
	return false
}

// knownMode reports whether m is no mode or one of the three modes.
func knownMode(m types.Mode) bool {
	switch m {
	case types.ModeUnset, types.Strict, types.TransformMode, types.Creative:
		return true
	}
	return false
}

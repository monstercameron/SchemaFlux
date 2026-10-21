package exp

import (
	"context"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// mergeExpValues merges zero, one or several values from left to right; the
// operation supplies its own nonzero-field overlay.
func mergeExpValues[T any](values []T, over func(earlier, later T) T) T {
	switch len(values) {
	case 0:
		var zero T
		return zero
	case 1:
		return values[0]
	}
	out := values[0]
	for _, later := range values[1:] {
		out = over(out, later)
	}
	return out
}

// mergeCommonOptions merges common settings from left to right. Zero fields
// leave the earlier setting in place, matching the root options contract.
func mergeCommonOptions(values []schemaflux.Options) schemaflux.Options {
	switch len(values) {
	case 0:
		return schemaflux.Options{}
	case 1:
		return values[0]
	}
	out := values[0]
	for _, v := range values[1:] {
		if v.Model != "" {
			out.Model = v.Model
		}
		if v.Tier != 0 {
			out.Tier = v.Tier
		}
		if v.Effort != "" {
			out.Effort = v.Effort
		}
		if v.Mode != 0 {
			out.Mode = v.Mode
		}
		if v.Steering != "" {
			out.Steering = v.Steering
		}
		if v.MaxOutputTokens != 0 {
			out.MaxOutputTokens = v.MaxOutputTokens
		}
		if v.WebSearch {
			out.WebSearch = true
		}
	}
	return out
}

// commonSettings reads the internal common settings through the root's
// registered conversion, so subpackages do not depend on the old root.
func commonSettings(options schemaflux.Options) (bind.Common, error) {
	return bind.CommonOf(options)
}

// applyFlatCommon assigns common settings to both internal option halves. The
// flat operations read OpOptions; the older half-based calls also read
// CommonOptions, so both receive the settings that apply to them.
func applyFlatCommon(ctx context.Context, c bind.Common, common *ops.CommonOptions, embedded *types.OpOptions) {
	c.ApplyHalves(common, embedded)
	if common != nil {
		common.Context = ctx
	}
}

// refuseExpBlank ends a call with an invalid-request error when a required
// positional string is blank.
func refuseExpBlank(call *bind.Call, name, value string) error {
	if strings.TrimSpace(value) == "" {
		return call.Refuse(types.KindInvalidRequest, "%s is blank", name)
	}
	return nil
}

// refuseExpEmpty ends a call with an invalid-request error when a required
// positional collection is empty.
func refuseExpEmpty(call *bind.Call, name string, n int) error {
	if n == 0 {
		return call.Refuse(types.KindInvalidRequest, "%s is empty", name)
	}
	return nil
}

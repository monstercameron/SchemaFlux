package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// settingsOnlyOptions is the internal options of Format and Merge, which have no
// settings of their own: the common settings and the context, which these two
// internal functions read from the options and not from an argument. An unset
// tier or mode is left for the internal function to default: Format and Merge
// run at TierSmart.
func settingsOnlyOptions(ctx context.Context, o Options) types.OpOptions {
	var opts types.OpOptions
	o.common().Apply(&opts)
	opts.Context = ctx
	return opts
}

// Format writes a value in the form the template describes: "markdown table",
// "a one-line changelog entry". The value is written into the request as JSON; a
// value JSON cannot write (a channel, a function, a NaN) is an error of kind
// ErrInvalidRequest, unless it has a String method, which is then how it reads.
// The model's text is returned trimmed. Default tier Smart, mode Transform.
//
//	table, err := schemaflux.Format(ctx, rows, "markdown table")
//	line, err := schemaflux.Format(ctx, release, "one-line changelog entry", schemaflux.Options{Tier: schemaflux.TierFast})
//
// The Record of the call is "format". The model's text is not a declared shape,
// so the answer is delivered at ContractPromptOnly: a client whose DataPolicy
// sets any floor refuses the call with ErrPolicy before anything is sent.
func Format(ctx context.Context, data any, template string, opts ...Options) (string, error) {
	call, err := bind.Begin(ctx, "format")
	if err != nil {
		return "", err
	}
	o := mergeOptions(opts)
	if err := o.common().Check("format"); err != nil {
		return "", call.End(nil, err)
	}
	if call.Plain() {
		formatted, err := ops.Format(data, template, settingsOnlyOptions(call.Context(), o))
		return formatted, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (string, error) {
		return ops.Format(data, template, settingsOnlyOptions(ctx, o))
	})
}

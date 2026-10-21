package schemaflux

// This file is shared by every operation function of the root. The functions of
// extract.go, analyze.go, decide.go, text.go and the files beside them are
// written to one pattern (see Extract, Classify and Is for the three shapes of
// it), and the helpers here are the parts of that pattern that do not depend on
// the operation. None of them asks a model, reads a client or allocates when
// the call is fine; each is a few lines, and an operation function that needs
// one of them calls it and does not write it again.
//
//	mergeValues, lay, laySlice, layMap, turnOff   the merge of several options values (M0) and the
//	                                              assignment of one field onto the internal options
//	                                              only when it is not its zero value (M2..M6)
//	refuseBlank, refuseEmpty                      the positional arguments (M8): invalid request
//	checkFloat, checkCount                        the numbers of an options value (A5): configuration
//	settleHalves, decisionOptions                 the common settings laid onto the internal options

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// mergeValues is the merge of an operation's options values (M0): no value is
// the zero value, one value is returned as it is, and with several the later
// are laid over the earlier by over, from left to right. over is written once
// for each options type (mergeExtractOptions and the others), and it lays each
// field that is not its zero value of the later value over the earlier. It never
// changes the values it is given, and for no value or one it calls nothing and
// allocates nothing.
func mergeValues[O any](values []O, over func(earlier, later O) O) O {
	switch len(values) {
	case 0:
		var zero O
		return zero
	case 1:
		return values[0]
	}
	merged := values[0]
	for _, later := range values[1:] {
		merged = over(merged, later)
	}
	return merged
}

// lay assigns v to *dst unless v is its zero value. It is how a field of an
// options value is laid over another in a merge, and how it is put onto the
// internal options of the call, which start from the constructor's value: a
// field left at its zero leaves the default alone, so an empty setting can
// never blank a line of a prompt. A bool that is true turns the setting on, and
// one that is false never turns it off.
func lay[V comparable](dst *V, v V) {
	var zero V
	if v != zero {
		*dst = v
	}
}

// laySlice assigns v to *dst unless v is nil. An empty slice that is not nil is
// a value, and replaces the destination.
func laySlice[S ~[]E, E any](dst *S, v S) {
	if v != nil {
		*dst = v
	}
}

// layMap assigns v to *dst unless v is nil. An empty map that is not nil is a
// value, and replaces the destination.
func layMap[M ~map[K]V, K comparable, V any](dst *M, v M) {
	if v != nil {
		*dst = v
	}
}

// turnOff sets *dst to false when no is true, and does nothing otherwise. It is
// the mapping of a negated bool (A5.1): the internal setting is true by default,
// and the v2 field that turns it off is false by default.
func turnOff(dst *bool, no bool) {
	if no {
		*dst = false
	}
}

// refuseBlank ends the call with an error of kind ErrInvalidRequest when value
// is empty or only space, naming the argument, and returns nil otherwise. The
// refusal still gives each recorder its Record, with no request sent. Use it
// for a positional argument that has to say something: a question, a criterion,
// a language.
func refuseBlank(call *bind.Call, name, value string) error {
	if strings.TrimSpace(value) == "" {
		return call.Refuse(types.KindInvalidRequest, "%s is blank", name)
	}
	return nil
}

// refuseEmpty ends the call with an error of kind ErrInvalidRequest when a
// positional list the operation needs has no items (n is its length), naming the
// argument, and returns nil otherwise.
func refuseEmpty(call *bind.Call, name string, n int) error {
	if n == 0 {
		return call.Refuse(types.KindInvalidRequest, "%s is empty", name)
	}
	return nil
}

// checkFloat returns the internal error of kind configuration for a number that
// is not in the range lo to hi, both included, or is not a number at all (NaN or
// an infinity), and nil otherwise. field is the name of the options field. An
// open end is math.Inf(-1) or math.Inf(1), and the message says "up to" or
// "from" for it. The caller passes the error to Call.End, which converts it, so
// a refusal is still a Record. The check is made on the caller's own value
// before any default or special meaning is applied to it.
func checkFloat(op, field string, v, lo, hi float64) error {
	if !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi {
		return nil
	}
	switch {
	case math.IsInf(lo, -1):
		return types.Newf(types.KindConfiguration, op, "%s must be a number up to %s", field, formatBound(hi))
	case math.IsInf(hi, 1):
		return types.Newf(types.KindConfiguration, op, "%s must be a number from %s", field, formatBound(lo))
	}
	return types.Newf(types.KindConfiguration, op, "%s must be a number from %s to %s", field, formatBound(lo), formatBound(hi))
}

// formatBound writes the end of a range as a person would: 1 and not 1.0e+00.
func formatBound(v float64) string { return fmt.Sprintf("%g", v) }

// checkCount returns the internal error of kind configuration for a count that
// is negative, and nil otherwise. It is for the numbers of A5.3, where "none" is
// not a meaning a negative can have.
func checkCount(op, field string, n int) error {
	if n < 0 {
		return types.Newf(types.KindConfiguration, op, "%s must not be negative", field)
	}
	return nil
}

// settleHalves lays the common settings over the two halves of an internal
// options type that embeds both ops.CommonOptions and types.OpOptions (the flat
// class H2), and puts ctx on the CommonOptions half, which is where the internal
// call reads the context. ctx is the context the internal call is made on: the
// call's own on the plain path and the one a recorded entry hands its closure
// otherwise.
func settleHalves(ctx context.Context, c bind.Common, common *ops.CommonOptions, embedded *types.OpOptions) {
	c.ApplyHalves(common, embedded)
	common.Context = ctx
}

// decisionOptions is the internal options of a decision (Is, Which, WhichOrNone,
// Rate, Assess and their Detail forms): the common settings and nothing else.
// The context is an argument of the internal call and not a field.
func decisionOptions(o Options) types.OpOptions {
	var opts types.OpOptions
	o.common().Apply(&opts)
	return opts
}

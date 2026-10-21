package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// CompressOptions are the settings of Compress. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: CompressOptions{Model: "gpt-6-luna", Ratio: 0.5}.
type CompressOptions struct {
	Options

	// Ratio is the size aimed at, as a fraction of the original, above 0 and at
	// most 1. It is a target the model aims at and is not enforced: a result
	// smaller than the target is a better answer, not a violation. Zero means
	// 0.3. A value outside 0 to 1, or one that is not a number, is a
	// configuration error.
	Ratio float64

	// RetainFields are fields of the input that must still be present. Enforced
	// for an entry that names a field of the input, when the result is a JSON
	// object; any other entry is a request. Nil means none.
	RetainFields []string

	// RemoveFields are fields that must be gone. Enforced for a result that is a
	// JSON object: one that still has a field named here is refused. Nil means
	// none.
	RemoveFields []string

	// PreserveInfo is information to keep, in plain language. A request: it is
	// not checked. Nil means none.
	PreserveInfo []string

	// Strategy is "lossy", "lossless" or "semantic". Empty means "semantic". Any
	// other value is a configuration error.
	Strategy string

	// Priority is what to keep first: "facts", "actions", "decisions" or "all".
	// Empty means "all". Any other value is a configuration error.
	Priority string

	// OutputFormat is "same", "summary", "key_points" or "structured". Empty means
	// "same". Any other value is a configuration error.
	OutputFormat string

	// MaxSize is a ceiling on the result in characters, which are counted in
	// runes. Enforced: a result over it is refused with ErrInvalidAnswer. Zero
	// means none. A negative value is a configuration error.
	MaxSize int
}

// CompressResult is a compressed value with the sizes that were measured and
// what the model reported about it.
type CompressResult[T any] struct {
	// Compressed is the smaller value.
	Compressed T `json:"compressed"`

	// OriginalSize is the size of the input in characters, measured here.
	OriginalSize int `json:"original_size"`

	// CompressedSize is the size of Compressed in characters, measured here.
	CompressedSize int `json:"compressed_size"`

	// ActualRatio is CompressedSize over OriginalSize. It is zero for an empty
	// input.
	ActualRatio float64 `json:"actual_ratio"`

	// PreservedInfo is what the model says it kept. Nil when it listed none.
	PreservedInfo []string `json:"preserved_info,omitempty"`

	// RemovedInfo is what the model says it dropped. Nil when it listed none.
	RemovedInfo []string `json:"removed_info,omitempty"`
}

// mergeCompressOptions merges the values of a call (M0).
func mergeCompressOptions(opts []CompressOptions) CompressOptions {
	return mergeValues(opts, func(o, later CompressOptions) CompressOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Ratio, later.Ratio)
		laySlice(&o.RetainFields, later.RetainFields)
		laySlice(&o.RemoveFields, later.RemoveFields)
		laySlice(&o.PreserveInfo, later.PreserveInfo)
		lay(&o.Strategy, later.Strategy)
		lay(&o.Priority, later.Priority)
		lay(&o.OutputFormat, later.OutputFormat)
		lay(&o.MaxSize, later.MaxSize)
		return o
	})
}

// checkCompressOptions returns the internal error of a setting no compression can
// use, or nil. op is the operation the error names: the function that was called.
// A Ratio of zero is the default; any other value, a NaN included, has to be
// above 0 and at most 1.
func checkCompressOptions(op string, o CompressOptions) error {
	if err := o.common().Check(op); err != nil {
		return err
	}
	// Written as the range that is allowed, so that a NaN is refused too.
	if o.Ratio != 0 && !(o.Ratio > 0 && o.Ratio <= 1) {
		return types.Newf(types.KindConfiguration, op, "Ratio must be above 0 and at most 1, or zero for the default")
	}
	switch o.Strategy {
	case "", "lossy", "lossless", "semantic":
	default:
		return types.Newf(types.KindConfiguration, op, "Strategy is not one of lossy, lossless and semantic")
	}
	switch o.Priority {
	case "", "facts", "actions", "decisions", "all":
	default:
		return types.Newf(types.KindConfiguration, op, "Priority is not one of facts, actions, decisions and all")
	}
	switch o.OutputFormat {
	case "", "same", "summary", "key_points", "structured":
	default:
		return types.Newf(types.KindConfiguration, op, "OutputFormat is not one of same, summary, key_points and structured")
	}
	return checkCount(op, "MaxSize", o.MaxSize)
}

// compressInternal builds the internal options of a compression on ctx: the
// constructor's value, then each setting that is not its zero value.
func compressInternal(ctx context.Context, o CompressOptions) ops.CompressOptions {
	opts := ops.NewCompressOptions()
	lay(&opts.CompressionRatio, o.Ratio)
	laySlice(&opts.RetainFields, o.RetainFields)
	laySlice(&opts.RemoveFields, o.RemoveFields)
	laySlice(&opts.PreserveInfo, o.PreserveInfo)
	lay(&opts.Strategy, o.Strategy)
	lay(&opts.Priority, o.Priority)
	lay(&opts.OutputFormat, o.OutputFormat)
	lay(&opts.MaxOutputSize, o.MaxSize)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// compressResult is the result of the internal call as the root declares it. The
// internal metadata map is never written for a compression and is not carried
// over. The zero result, which the internal call returns beside an error,
// converts to the zero result.
func compressResult[T any](r ops.CompressResult[T]) CompressResult[T] {
	return CompressResult[T]{
		Compressed: r.Compressed, OriginalSize: r.OriginalSize, CompressedSize: r.CompressedSize,
		ActualRatio: r.ActualRatio, PreservedInfo: r.PreservedInfo, RemovedInfo: r.RemovedInfo,
	}
}

// Compress makes a value smaller while keeping what matters: a text shorter, a
// struct with fewer or briefer fields. For a text it is what CompressText was.
// MaxSize and the field lists are enforced, and an answer that breaks one is an
// error of kind ErrInvalidAnswer; Ratio is a target. Default tier Fast, mode
// Transform.
//
//	res, err := schemaflux.Compress(ctx, record, schemaflux.CompressOptions{Ratio: 0.5, RetainFields: []string{"id"}})
//	short := res.Compressed
//
// The Record of the call is "compress". The model is asked for a declared schema
// of the compressed value's type and the answer is delivered at
// ContractSchemaConstrained when a schema can be built for T. For a T none can (a
// map, a recursive type, an interface, a type nested deeper than six levels) the
// call is made without a schema and is delivered at ContractJSONWellFormed; a
// floor above that level refuses it before anything is sent.
func Compress[T any](ctx context.Context, input T, opts ...CompressOptions) (CompressResult[T], error) {
	var zero CompressResult[T]
	call, err := bind.Begin(ctx, "compress")
	if err != nil {
		return zero, err
	}
	o := mergeCompressOptions(opts)
	if err := checkCompressOptions("compress", o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Compress(input, compressInternal(call.Context(), o))
		return compressResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, ops.ContractLevelFor[T]("compress"), func(ctx context.Context) (ops.CompressResult[T], error) {
		return ops.Compress(input, compressInternal(ctx, o))
	})
	return compressResult(res), err
}

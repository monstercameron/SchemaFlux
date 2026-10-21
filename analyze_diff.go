package schemaflux

import (
	"context"
	"reflect"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// DiffOptions are Diff's settings. The zero value is the default: nothing is
// ignored, a changed nested struct is reported as "structure_changed" and the
// summary is written without background.
//
// The common settings of Options come first and are set without naming the
// embedded type: DiffOptions{Tier: schemaflux.TierSmart, Background: "a billing system"}.
// They shape the request for the summary and nothing else: the comparison is
// made in Go.
type DiffOptions struct {
	Options

	// Background is prose about the data, put in the request for the summary as a
	// quoted value. Empty means none.
	Background string

	// IgnoreFields are the Go names of fields left out of the comparison, as they
	// are written in the struct and not as JSON writes them. Nil means none.
	IgnoreFields []string

	// NoDeepCompare reports a changed nested struct as one "modified" value. False
	// reports it as "structure_changed". Either way the change is reported, and
	// neither names the field inside the nested struct that changed.
	NoDeepCompare bool
}

// DiffResult is the comparison of two values, made in Go, and the model's
// summary of it.
type DiffResult struct {
	// Added are the names of fields that the second value has and the first does
	// not, which happens when T is an interface type that holds two different
	// structs. Nil when there are none.
	Added []string `json:"added"`

	// Removed are the names of fields that the first value has and the second does
	// not. Nil when there are none.
	Removed []string `json:"removed"`

	// Modified are the fields whose values differ, with both values.
	Modified []DiffChange `json:"modified"`

	// Summary is the model's account of the changes, or one of four fixed
	// sentences of the library's own when the values do not differ, differ only in
	// fields Diff cannot read, or differ only in fields that JSON leaves out and so
	// are not sent to a model. It is empty when no model was asked, which is the
	// case for a change that is only an added or a removed field at TierFast, and
	// when a model was asked and failed: then SummaryError says why.
	Summary string `json:"summary"`

	// SummaryError is why Summary is empty when a model was asked for it and
	// failed, an *Error. It is nil otherwise. The comparison above it is complete
	// either way, and Diff itself returns no error for a failed summary.
	SummaryError error `json:"-"`
}

// DiffChange is one field whose value changed.
type DiffChange struct {
	// Field is the Go name of the field.
	Field string `json:"field"`

	// OldValue is the field's value in the first struct, as it is: a nested
	// struct, a slice or a map comes back whole. It is nil when one side has no
	// value for the field.
	OldValue any `json:"old_value"`

	// NewValue is the field's value in the second struct.
	NewValue any `json:"new_value"`

	// ChangeType is "modified", "type_changed" when the two values are of different
	// kinds, or "structure_changed" for a nested struct that differs, unless
	// DiffOptions.NoDeepCompare asks for it to be "modified".
	ChangeType string `json:"change_type"`
}

// mergeDiffOptions merges the values of a call (M0).
func mergeDiffOptions(opts []DiffOptions) DiffOptions {
	return mergeValues(opts, func(o, later DiffOptions) DiffOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Background, later.Background)
		laySlice(&o.IgnoreFields, later.IgnoreFields)
		lay(&o.NoDeepCompare, later.NoDeepCompare)
		return o
	})
}

// diffInternal builds the internal options of a Diff call on ctx. Diff's
// internal type holds only the half of the common settings the summary request
// reads (class H1), and the context is one of its fields.
func diffInternal(ctx context.Context, o DiffOptions) ops.DiffOptions {
	opts := ops.NewDiffOptions()
	lay(&opts.Background, o.Background)
	laySlice(&opts.IgnoreFields, o.IgnoreFields)
	turnOff(&opts.DeepCompare, o.NoDeepCompare)
	o.common().Apply(&opts.OpOptions)
	opts.OpOptions.Context = ctx
	return opts
}

// diffResult is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result.
func diffResult(res ops.DiffResult) DiffResult {
	out := DiffResult{Added: res.Added, Removed: res.Removed, Summary: res.Summary}
	if len(res.Modified) > 0 {
		out.Modified = make([]DiffChange, len(res.Modified))
		for i, c := range res.Modified {
			out.Modified[i] = DiffChange{Field: c.Field, OldValue: c.OldValue, NewValue: c.NewValue, ChangeType: c.ChangeType}
		}
	}
	if res.SummaryError != nil {
		out.SummaryError = convertError("diff", res.SummaryError)
	}
	return out
}

// diffRefusal says why two values cannot be compared, or returns "" when they
// can: each has to be a struct or a pointer to one, and a nil pointer can only
// be compared with another. It is a statement about the arguments, so the call it
// refuses is an invalid request. It reads the types and the nil-ness of the
// values and allocates nothing.
func diffRefusal[T any](before, after T) string {
	var nils [2]bool
	for i, v := range [2]T{before, after} {
		t := reflect.TypeOf(v)
		if t == nil {
			return "a value to compare is nil, and has to be a struct or a pointer to one"
		}
		if t.Kind() == reflect.Pointer {
			nils[i] = reflect.ValueOf(v).IsNil()
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return "the values to compare have to be structs or pointers to structs"
		}
	}
	if nils[0] != nils[1] {
		return "one value is a nil pointer and the other is not"
	}
	return ""
}

// Diff compares two values field by field in Go and asks a model for a summary
// of what changed. Only exported fields are compared, and the summary is written
// from the changes in fields that are written as JSON: a field the type tags
// json:"-" is compared and reported but never sent to a model. Default tier Fast,
// mode Transform.
//
//	d, err := schemaflux.Diff(ctx, before, after)
//	d, err := schemaflux.Diff(ctx, before, after, schemaflux.DiffOptions{IgnoreFields: []string{"UpdatedAt"}})
//	fmt.Println(d.Summary, len(d.Modified))
//
// T has to be a struct or a pointer to one, and so does each value when T is an
// interface type; anything else, a nil value, or a nil pointer beside one that is
// not, is an error of kind ErrInvalidRequest, returned before anything is sent.
//
// A context that has already ended refuses the call with ErrCanceled or
// ErrTimeout and compares nothing, though the comparison needs no request.
//
// When the model cannot be reached, or answers with nothing, the comparison is
// still returned with a nil error, and DiffResult.SummaryError says why there is
// no summary. A model is asked only when a field written as JSON changed and
// either one of those was modified or the tier is not TierFast, so most calls at
// the default tier send nothing for an added or a removed field.
//
// The Record of the call is "diff", delivered at ContractPromptOnly, the lowest
// level: the summary is the model's text taken as it comes, and the comparison is
// Go. Under a client's floor above that level the call is refused with an error
// of kind ErrPolicy before anything is compared.
func Diff[T any](ctx context.Context, before, after T, opts ...DiffOptions) (DiffResult, error) {
	var zero DiffResult
	call, err := bind.Begin(ctx, "diff")
	if err != nil {
		return zero, err
	}
	o := mergeDiffOptions(opts)
	if why := diffRefusal(before, after); why != "" {
		return zero, call.Refuse(types.KindInvalidRequest, "%s", why)
	}
	if err := o.common().Check("diff"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Diff(before, after, diffInternal(call.Context(), o))
		return diffResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (ops.DiffResult, error) {
		return ops.Diff(before, after, diffInternal(ctx, o))
	})
	return diffResult(res), err
}

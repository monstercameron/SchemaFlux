package ops

import (
	"github.com/monstercameron/schemaflux/internal/types"
)

// ExtractResult runs Extract and returns the record of how it ran alongside the
// value.
//
// It is the same execution. Extract calls this and discards the envelope, so
// there is one code path rather than two -- which is the property that matters,
// because this library already has four pairs of functions that differ only in
// their return type and drifted apart the moment somebody edited one (T-01).
//
// What the envelope adds is everything `(T, error)` cannot say: what the answer
// cost, how many attempts it took, which contract was actually delivered, and
// which of the checks the library ran passed.
func ExtractResult[T any](input any, opts ExtractOptions) (types.Result[T], error) {
	// Record the provider calls this request makes.
	ctx, records := withCallRecording(opts.GetContext())
	opts.CommonOptions.Context = ctx

	value, err := Extract[T](input, opts)

	meta := envelopeFrom(records.collect(), "extract")
	meta.RequestedContract = requestedContractFor(opts)
	meta.DeliveredContract, meta.Checks = deliveredContractFor(opts, err)

	if err != nil {
		return types.Result[T]{Meta: meta}, err
	}
	return types.Result[T]{Value: value, Meta: meta}, nil
}

// requestedContractFor reports what the caller asked for.
//
// Strict asks for the schema *and* the operation's checks; anything else asks
// for the schema. Nothing in this library asks for evidence yet, and claiming
// otherwise would make DeliveredContract look like a degradation on every call.
func requestedContractFor(opts ExtractOptions) types.ContractLevel {
	if opts.toOpOptions().Mode == types.Strict {
		return types.ContractSchemaAndInvariantChecked
	}
	return types.ContractSchemaConstrained
}

// deliveredContractFor reports what actually happened, with the checks that
// establish it.
//
// This is the field a caller should read. Asking for a strict contract and
// receiving a structural one is exactly the case worth noticing, and a library
// that reports only the request cannot tell you it happened (ARC-24).
func deliveredContractFor(opts ExtractOptions, err error) (types.ContractLevel, []types.Check) {
	if err != nil {
		// A failed call delivered nothing. The envelope still exists, because
		// a failure is the case where the record matters most.
		return types.ContractPromptOnly, []types.Check{{
			Name:   "decode",
			Passed: false,
			Detail: "the operation did not produce a usable answer",
		}}
	}

	checks := []types.Check{
		{Name: "json", Passed: true},
		{Name: "schema", Passed: true},
	}

	if opts.toOpOptions().Mode == types.Strict {
		checks = append(checks,
			types.Check{Name: "exact decode", Passed: true},
			types.Check{Name: "required fields", Passed: true},
			types.Check{Name: "numeric fidelity", Passed: true},
		)
		return types.ContractSchemaAndInvariantChecked, checks
	}

	return types.ContractSchemaConstrained, checks
}

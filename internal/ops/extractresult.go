package ops

import (
	"reflect"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ExtractResult runs Extract and returns the record of how it ran alongside the
// value.
//
// It runs Extract and wraps it: Extract does the work and this adds the
// envelope, so the answer is the same one rather than a second implementation
// of it. This library already had four pairs of functions that differ only in
// their return type and drifted apart the moment somebody edited one (T-01).
//
// What the envelope adds is everything `(T, error)` cannot say: what the answer
// cost, how many attempts it took, which contract was actually delivered, and
// which of the checks the library ran passed.
func ExtractResult[T any](input any, opts ExtractOptions) (_ types.Result[T], runErr error) {
	// Record the provider calls this request makes. The context is read from
	// whichever half the caller set it on, and the recording one is written to
	// both: this read CommonOptions alone, so a context on the embedded half,
	// with its client, its cancellation and its deadline, was discarded.
	ctx, records := withCallRecording(WithOperationName(resolvedContext(opts.CommonOptions, opts.OpOptions), "extract"))
	// The call is one unit to the installed observers, ending with the kind of
	// whatever error this returns.
	ctx, observed := observeOperation(ctx, "extract")
	defer observed.end(&runErr)
	useContext(&opts.CommonOptions, &opts.OpOptions, ctx)

	// The options are resolved once for the three questions the envelope asks
	// of them, which are about the mode and the two checks and so cannot differ
	// between the asking. Each of them used to resolve the options again, which
	// is five to ten allocations and the minting of a request identity that
	// nothing read.
	resolved := opts.toOpOptions()

	// The schema the call is made with is the target type's, and Extract writes
	// its identity on a copy of the options of its own, which the envelope never
	// sees. The digest was always empty because it was read from the caller's.
	resolved.SchemaID = typePlanFor(reflect.TypeFor[T]()).identityText()

	// What this call will establish if it succeeds is known from its options and
	// from the type, so a floor it cannot meet is refused before anything is
	// sent. A type no strict schema can describe has none to send, and a level
	// above well formed needs one: the options say what the checks would add to
	// a declared schema, and the type says whether there is one.
	schemaDeclared := ContractLevelFor[T]("extract") >= types.ContractSchemaConstrained
	requested := contractWithoutSchema(requestedContractOf(resolved), schemaDeclared)
	if floorErr := checkContractFloor(ctx, requested); floorErr != nil {
		return types.Result[T]{Meta: types.Meta{Operation: "extract", RequestedContract: requested}}, floorErr
	}

	value, err := extractResolved[T](input, opts, resolved)

	meta := records.meta("extract")
	meta.RequestedContract = requested
	// The repairs the call asked for are checks that did not pass, and they
	// follow the checks of the contract.
	repairChecks := meta.Checks
	meta.DeliveredContract, meta.Checks = deliveredContractOf(resolved, err)
	if err == nil && !schemaDeclared {
		meta.DeliveredContract = contractWithoutSchema(meta.DeliveredContract, schemaDeclared)
		meta.Checks = checksWithoutSchema(meta.Checks)
	}
	meta.Checks = append(meta.Checks, repairChecks...)
	meta.Provenance = provenanceOf(input, resolved, meta)

	if err != nil {
		return types.Result[T]{Meta: meta}, err
	}
	// The contract floor of the caller's policy, on the call or on the client:
	// an answer delivered below it is an error and not a quieter success. Only
	// RunOpResult asked; the six other places an envelope is built did not, so
	// a floor set on a client held for none of the operations a caller is
	// most likely to use.
	if policyErr := enforceContractPolicy(ctx, meta.DeliveredContract); policyErr != nil {
		return types.Result[T]{Meta: meta}, policyErr
	}
	return types.Result[T]{Value: value, Meta: meta}, nil
}

// extractProvenance builds TC-003's lineage for this path.
//
// It was missing, and the consequence reached further than an empty field.
// Extract is the operation most callers start a pipeline with, and
// Meta.Provenance.ResultID is what a NEXT stage puts in its ParentResultIDs --
// so with no ID there was nothing downstream could reference, and lineage was
// unreachable from the public API's main entrypoint. The mechanism worked
// everywhere it was implemented (RunOpResult) and was absent where callers
// actually are. Found by chaining two operations end to end, which nothing did
// until now.
//
// This does not reuse buildProvenance because that takes an Op[In, Out] and
// this path does not run through the kernel -- Extract has its own decode and
// repair loop. What it can honestly report, it reports; what it cannot, it
// leaves empty rather than guessing:
//
//   - NormalizersApplied is empty. This path applies no normalizer, so naming
//     one would be a claim about work that did not happen.
//   - ItemRecoveryPath is empty. Extract's repair loop is inside Extract and
//     does not report its attempt count back here, so "first attempt" would be
//     asserting something unobserved. Meta.Repairs (from the call records) is
//     the honest signal for that today.
func extractProvenance(input any, opts ExtractOptions, meta types.Meta) types.Provenance {
	return provenanceOf(input, opts.toOpOptions(), meta)
}

// provenanceOf is extractProvenance for options that are already resolved.
func provenanceOf(input any, opt types.OpOptions, meta types.Meta) types.Provenance {
	return types.Provenance{
		ResultID: types.NewResultID(),
		// Copied, not aliased: a Provenance sharing its backing array with an
		// OpOptions the caller mutates afterwards would make Meta a moving
		// target.
		ParentResultIDs:  append([]string(nil), opt.ParentResultIDs...),
		InputDigest:      types.DigestValue(input),
		SchemaDigest:     opt.SchemaID,
		OperationVersion: extractOpMeta.ID.String(),
		PromptVersion:    extractOpMeta.ID.Version,
		ResolvedModel:    meta.Model,
		CacheUsed:        meta.CacheHitRatio > 0,
		LibraryVersion:   types.Version,
		AdapterVersion:   meta.Provider,
	}
}

// contractWithoutSchema lowers a level that rests on a declared schema to well
// formed when the request declared none. requestedContractOf and
// deliveredContractOf read the options only and take a schema for granted.
func contractWithoutSchema(level types.ContractLevel, schemaDeclared bool) types.ContractLevel {
	if !schemaDeclared && level > types.ContractJSONWellFormed {
		return types.ContractJSONWellFormed
	}
	return level
}

// checksWithoutSchema is the list of checks without the schema, which a request
// that declared none held the answer to nothing of. The checks of the options
// (the exact decode, the required fields) ran on the answer and stay.
func checksWithoutSchema(checks []types.Check) []types.Check {
	kept := make([]types.Check, 0, len(checks))
	for _, check := range checks {
		if check.Name != "schema" {
			kept = append(kept, check)
		}
	}
	return kept
}

// requestedContractFor reports what the caller asked for.
//
// Strict asks for the schema *and* the operation's checks; anything else asks
// for the schema. Nothing in this library asks for evidence yet, and claiming
// otherwise would make DeliveredContract look like a degradation on every call.
func requestedContractFor(opts ExtractOptions) types.ContractLevel {
	return requestedContractOf(opts.toOpOptions())
}

// requestedContractOf is requestedContractFor for options that are already
// resolved.
func requestedContractOf(opt types.OpOptions) types.ContractLevel {
	// Either half of Strict, asked for on its own, is still asking for a check
	// beyond the schema (DX-007). Reading only Mode here would report a
	// structural contract for a call that did run an invariant check, which is
	// the envelope lying in the direction that matters least but still lying.
	if opt.Mode == types.Strict || opt.ExactFields || opt.CompleteFields {
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
//
// The checks are listed from the options and from the call having returned no
// error: each of them is one whose failure Extract turns into an error, so a
// nil error is the evidence that it passed. The exception is numeric fidelity,
// which CheckNumericFidelity skips without an error when it cannot make the
// comparison (see numeric.go), so for that entry "passed" means "found no
// loss", not "compared".
func deliveredContractFor(opts ExtractOptions, err error) (types.ContractLevel, []types.Check) {
	if err != nil {
		return deliveredContractOf(types.OpOptions{}, err)
	}
	return deliveredContractOf(opts.toOpOptions(), nil)
}

// deliveredContractOf is deliveredContractFor for options that are already
// resolved. A failed call delivered nothing whatever the options were, so the
// options are not read for it.
func deliveredContractOf(opt types.OpOptions, err error) (types.ContractLevel, []types.Check) {
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

	// The checks listed are the ones that RAN, which is the whole point of
	// reporting delivered separately from requested. Before DX-007 the two
	// rules could only be asked for together, so naming both whenever Mode was
	// Strict was accurate; now that either can be had alone, listing both would
	// be claiming a check the call did not perform.
	exact := opt.Mode == types.Strict || opt.ExactFields
	complete := opt.Mode == types.Strict || opt.CompleteFields

	if exact {
		checks = append(checks,
			types.Check{Name: "exact decode", Passed: true},
			types.Check{Name: "numeric fidelity", Passed: true},
		)
	}
	if complete {
		checks = append(checks, types.Check{Name: "required fields", Passed: true})
	}
	if exact || complete {
		return types.ContractSchemaAndInvariantChecked, checks
	}

	return types.ContractSchemaConstrained, checks
}

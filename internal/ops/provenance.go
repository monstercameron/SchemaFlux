package ops

import (
	"fmt"
	"reflect"
	"runtime"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-003, wiring half. internal/types/provenance.go defines the shape of a
// result's lineage; this is where RunOpResult (op.go) fills one in.
//
// Scope, stated plainly: this wires TC-003 for operations that run through
// RunOp/RunOpResult -- the generic Op[In, Out] execution path
// (schemaflux.RunOpResult, and everything plan_many.go/partial.go/
// recover.go build on it: PlanMany, batch recovery, the atomic-fallback
// path). It is the one execution path visible from this task's permitted
// files that has an Op descriptor, the resolved input, the resolved
// options, the repair outcome, and the finished envelope all in one place
// at once. Operations still on the pre-Op path -- Choose, Filter, Sort,
// Classify, Summarize, Transform's and Extract's own top-level entry
// points in core.go, all built directly in collection.go/textresult.go/
// opextract.go -- are outside this task's permitted files (or, for
// core.go, do not go through RunOpResult even where core.go itself is
// editable: Extract calls RunOp directly with In=any and a nil input,
// through runExtractOp in opextract.go, which this task may not edit) and
// do not get a Provenance filled in by this change. Their Meta.Provenance
// is the zero value, which types.Provenance.FullyTraced() correctly
// reports as untraced.

// buildProvenance fills in a Provenance for one RunOpResult call, from the
// Op descriptor, the input actually supplied, the resolved options, the
// envelope RunOpResult already built, and the repair outcome RunOp
// returned.
func buildProvenance[In, Out any](op Op[In, Out], input In, opt types.OpOptions, meta types.Meta, repair RepairResult) types.Provenance {
	prov := types.Provenance{
		ResultID: types.NewResultID(),
		// Copied, not aliased: opt.ParentResultIDs is the caller's slice,
		// and a Provenance that shared its backing array with an OpOptions
		// the caller mutates afterward would make Meta a moving target.
		ParentResultIDs:  append([]string(nil), opt.ParentResultIDs...),
		InputDigest:      types.DigestValue(input),
		SchemaDigest:     opt.SchemaID,
		OperationVersion: op.ID.String(),
		PromptVersion:    op.ID.Version,
		ResolvedModel:    meta.Model,
		ItemRecoveryPath: recoveryPathFrom(repair),
		// CacheHitRatio is CachedTokens/PromptTokens (callrecord.go's
		// envelopeFrom), zero whenever the provider reported nothing
		// cached -- which is also what a provider that never reports
		// cache usage at all produces. CacheUsed is therefore "the
		// provider told us it served this from cache," not "no caching
		// happened"; CA-006 is the diagnostic for telling those apart
		// over a run of calls, which one Provenance cannot do alone.
		CacheUsed:      meta.CacheHitRatio > 0,
		LibraryVersion: types.Version,
		// The provider name the response actually reported, which is what
		// this codebase has to identify an adapter with. See the
		// AdapterVersion field's doc comment in provenance.go for why
		// nothing finer exists to read here.
		AdapterVersion: meta.Provider,
	}

	if op.Contract.Normalize != nil {
		prov.NormalizersApplied = []string{normalizerSymbol(op.Contract.Normalize)}
	}

	return prov
}

// recoveryPathFrom describes how a result reached an accepted state,
// without any of the content that made it need repairing -- RepairResult.
// Failures (repair.go) carries the fed-back problem text, which is exactly
// the kind of thing that can echo a caller's data back (a decode error can
// quote the offending fragment); this reports only the shape of the path,
// never a failure's text.
func recoveryPathFrom(repair RepairResult) string {
	switch {
	case repair.Attempts <= 1:
		return "first attempt"
	case repair.Repaired:
		return fmt.Sprintf("repaired, %d attempt(s)", repair.Attempts)
	default:
		return fmt.Sprintf("%d attempt(s), not repaired", repair.Attempts)
	}
}

// normalizerSymbol names a normalizer function by its Go symbol -- the
// function's own identity, never anything it produced. reflect.Value.Pointer
// on a func value is documented as suitable for this exact purpose
// (identifying which function it is), and is not meaningful as an address
// to call through, which this code never does with it.
func normalizerSymbol(normalize any) string {
	value := reflect.ValueOf(normalize)
	if value.Kind() != reflect.Func {
		return "unknown"
	}
	fn := runtime.FuncForPC(value.Pointer())
	if fn == nil || fn.Name() == "" {
		return "unknown"
	}
	return fn.Name()
}

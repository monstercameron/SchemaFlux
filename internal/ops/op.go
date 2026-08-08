package ops

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The Op descriptor. A-001, Revised (ARC-04, ARC-16, API-09).
//
// The task body described six fields: Name, Prompt, Format, Schema, Decode,
// Invariants. The Revised line widened it and made it public: OperationID
// with a version, a Semantics block, an OutputContract, a BatchAlgebra, and a
// DefaultPolicy. What it does not widen is the one constraint that makes the
// whole thing worth building: "it holds no context and no provider -- it is
// data plus pure functions". TestOpHoldsNoContextOrProvider below is that
// constraint checked by reflection rather than left as a comment for the next
// field to violate.
//
// Op does not run itself. RunOp (opextract.go) takes a context and reads the
// package-level provider hooks callLLM already reads -- the same seam A-002's
// Run and every existing operation use -- so an Op is a plan a caller can
// inspect before anything is spent, and running it is a separate step with a
// separate, ordinary Go signature.

// OutputContract describes how a raw provider response becomes a value of
// Out, and what must hold once it does.
//
// Decode and Invariants are pure functions over the response body and the
// decoded value -- no I/O, no retry, no knowledge of how many attempts came
// before. withRepair (repair.go) is what calls them repeatedly; the contract
// itself does not know it is being repaired.
type OutputContract[Out any] struct {
	// SchemaName is the contract's identity for logs and cache keys. For a
	// generated type this is schemaNameFor's output; DescribeSchema
	// (schemaid.go) is the fuller identity and is deliberately not
	// duplicated here -- SchemaID belongs on the resolved types.OpOptions
	// for a specific call, not on the operation's static shape, because two
	// calls to the same Op with different target types have the same Op and
	// different schemas. Extract's port (opextract.go) sets it per call.
	SchemaName string

	// Decode turns a response body into a value of Out, or reports why it
	// could not. It is the thing an OutputContract names in the Revised
	// line; strictdecode.go's DecodeExact and ParseJSONStrict are the two
	// decoders Extract's port switches between.
	Decode func(body string) (Out, error)

	// Invariants run in order after Decode succeeds. The shared library
	// (invariants.go) is written as free functions over a decoded value, so
	// binding one here is a closure: `func(out T) error { return
	// SubsetOf(offered, out) }`. A caller's own rule is exactly the same
	// shape, which is what A-009's Revised line ("a caller must be able to
	// register their own ... invariant") needs the seam to look like -- this
	// is that seam for the one operation it has been wired into so far.
	Invariants []func(Out) error

	// EvidenceRequired mirrors Semantics.EvidenceRequired at the contract
	// level, for the case a single Op is instantiated once with the policy
	// on and once with it off.
	//
	// TC-002: this is now read. RunOp enforces it whenever EvidencePolicy
	// is left at its zero value (types.EvidenceNone) but EvidenceRequired
	// is true -- treated as types.EvidenceAllModelDerived, the ordinary
	// reading of "evidence is required" with no finer policy stated. Set
	// EvidencePolicy explicitly for the other three modes; EvidenceRequired
	// alone cannot express "material fields only" or "no inference".
	EvidenceRequired bool

	// EvidencePolicy is the finer-grained evidence contract TC-002 adds:
	// which claims must resolve to a valid types.EvidenceRef before RunOp
	// accepts a candidate. See types.EvidencePolicy's doc comment for what
	// each of the four modes requires.
	EvidencePolicy types.EvidencePolicy

	// MaterialFields names the output fields that must carry evidence under
	// EvidenceMaterialFields. Ignored by the other three policies.
	MaterialFields []string

	// Normalize runs once, after Invariants pass, and may not fail: it is
	// for shape-preserving cleanup (trimming, canonical casing) that should
	// not be allowed to turn a passing answer into a failing one by way of a
	// bug in the normalizer itself.
	Normalize func(Out) Out
}

// BatchAlgebra describes how Op composes across many inputs of In into many
// outputs of Out.
//
// A nil Encode means the operation has no batch form -- BatchNone is the
// only class that permits a nil Encode/Merge, and NewOp does not check that
// today (see the report's stub list: batching is declared, not yet run).
// GlobalValidate is for the BatchAggregate case: a check that only makes
// sense across the whole set, such as CoversExactlyOnce or SameMultiset.
type BatchAlgebra[In, Out any] struct {
	Class types.BatchClass

	// Kind is PL-006's finer taxonomy over Class: independent, subset,
	// permutation, partition, graph, hierarchical, or sequential. Class says
	// how much validation a batch carries; Kind says what shape that
	// validation takes, which is what NewIDBatchAlgebra (batchprotocol.go)
	// reads to know whether a batch's id coverage must be exact
	// (Kind.RequiresExactCoverage) before any operation-specific check runs.
	Kind types.AlgebraKind

	// Encode renders the batch request's system and user prompt for many
	// inputs at once. Absent for BatchNone.
	//
	// Revised from a single-string return: nothing called this field before
	// PL-003 filled it in, so there was no caller to keep compatible, and a
	// batch call needs both halves of the prompt the same way BuildPrompt
	// does for a single item -- a batch-specific system prompt (the id
	// protocol's rules) is not the per-item BuildPrompt's system prompt with
	// items concatenated into the user half.
	Encode func(items []In, opt types.OpOptions) (system, user string, err error)

	// Merge decodes one response body into as many outputs as items,
	// resolved by the invocation-local id each item was tagged with (PL-003)
	// -- never by the response's position, which is exactly the trust an
	// omitted, duplicated, or reordered item would break. Absent for
	// BatchNone. NewIDBatchAlgebra builds a Merge that follows OP-101's id
	// protocol so an operation does not hand-roll a second one.
	Merge func(body string, items []In) ([]Out, error)

	// GlobalValidate checks the batch as a whole, for BatchAggregate
	// operations whose contract is relational rather than per-item -- the
	// AGENTS.md rule that Choose/Filter/Sort/Cluster are checked in Go, not
	// per-item but as a set.
	GlobalValidate func(items []In, outputs []Out) error
}

// Op is the descriptor: what an operation is, not how to run it right now.
//
// In and Out are the operation's input and output types. It holds no
// context.Context and no provider -- data plus pure functions -- which is
// what makes planning, recipes, loop fusion, and middleware possible without
// a second execution path: anything that can read this struct can reason
// about the operation, and nothing about running it requires touching this
// struct's shape.
type Op[In, Out any] struct {
	ID types.OperationID

	Semantics types.Semantics

	Contract OutputContract[Out]

	Batch BatchAlgebra[In, Out]

	DefaultPolicy types.DefaultPolicy

	// BuildPrompt renders the system and user prompt for one input. It is a
	// pure function of the input and the resolved options -- no I/O, no
	// side effect, no read of a package-level hook. RunOp calls it exactly
	// once per attempt's first pass; withRepair (repair.go) is what appends
	// the repair text on a retry, downstream of this function entirely, so
	// BuildPrompt itself never sees a repair.
	BuildPrompt func(input In, opt types.OpOptions) (system, user string)
}

// NewOp validates an Op before handing it back, and is the practical form of
// the Revised line's added Verify: "a stable operation with no declared
// batch class fails a build-time check". It cannot fail at compile time --
// Go generics have no mechanism to reject a struct literal based on a
// runtime-only field's value -- so this is the next strongest thing: a
// stable Op fails the instant it is constructed, and every operation in this
// package that claims to be stable is constructed through this function at
// package init, so a regression fails go test / go vet's init-time execution
// before any test body runs. op_test.go's
// TestStableExtractOpFailsInitWithoutBatchClass proves the panic path
// directly; TestNewOpRejectsStableWithoutBatchClass proves the non-panicking
// form a caller can handle.
func NewOp[In, Out any](op Op[In, Out]) (Op[In, Out], error) {
	if op.Semantics.Stability == types.StabilityStable && op.Batch.Class == types.BatchUnspecified {
		return Op[In, Out]{}, fmt.Errorf(
			"op %s is declared stable but has no batch class; a stable operation must say how it composes across a batch (types.BatchNone if it has none)",
			op.ID)
	}

	// PL-006: a stable operation whose batch form actually composes many
	// inputs (Itemwise or Aggregate) must also say what shape that
	// composition takes -- Kind -- because a generic batch runner
	// (batchprotocol.go's NewIDBatchAlgebra, RunOpBatch) decides how to
	// validate a response by reading Kind, and BatchNone/BatchUnspecified are
	// the only classes with nothing to validate. This is the other half of
	// the Revised line's build-time check: "a stable operation with no
	// declared batch class fails a build-time check" widened to the finer
	// taxonomy PL-006 adds on top of Class.
	if op.Semantics.Stability == types.StabilityStable &&
		(op.Batch.Class == types.BatchItemwise || op.Batch.Class == types.BatchAggregate) &&
		op.Batch.Kind == types.AlgebraUnspecified {
		return Op[In, Out]{}, fmt.Errorf(
			"op %s is declared stable with batch class %s but no algebra kind; a stable batched operation must say how it composes (independent, subset, permutation, partition, graph, hierarchical, or sequential)",
			op.ID, op.Batch.Class)
	}
	return op, nil
}

// MustNewOp is NewOp for the package's own operations, registered at init
// time so a missing batch class on a stable op fails every build that runs
// this package's tests, not just a test written to look for it.
func MustNewOp[In, Out any](op Op[In, Out]) Op[In, Out] {
	built, err := NewOp(op)
	if err != nil {
		panic(err)
	}
	return built
}

// RunOp executes op against one input, following the same path every
// existing operation in this package uses: build the prompt, call the model
// under the package's repair budget, decode and check the result.
//
// It reads the provider through callLLM's package-level hooks (llm_helper.go)
// rather than taking a provider parameter, which is the existing seam every
// operation in this package already runs through (A-002's Run does the same
// for the fluent builders) -- adding a second way to supply a provider here
// would be the second execution path the Op design exists to avoid.
func RunOp[In, Out any](ctx context.Context, op Op[In, Out], input In, opt types.OpOptions) (Out, RepairResult, error) {
	var zero Out

	if op.BuildPrompt == nil {
		return zero, RepairResult{}, fmt.Errorf("op %s: no BuildPrompt to render a request from", op.ID)
	}
	if op.Contract.Decode == nil {
		return zero, RepairResult{}, fmt.Errorf("op %s: no Decode to turn a response into %T", op.ID, zero)
	}

	system, user := op.BuildPrompt(input, opt)

	policy := RepairPolicy{Attempts: op.DefaultPolicy.RepairAttempts}

	// TC-002: the effective policy is EvidencePolicy when the caller set one,
	// or a plain EvidenceRequired=true read as "all model-derived fields
	// need a reference" -- the ordinary reading with no finer policy stated.
	evidencePolicy := op.Contract.EvidencePolicy
	if evidencePolicy == types.EvidenceNone && op.Contract.EvidenceRequired {
		evidencePolicy = types.EvidenceAllModelDerived
	}

	var candidate Out
	var lastBody string
	_, repair, err := withRepair(ctx, system, user, opt, policy, func(body string) error {
		lastBody = body
		value, decodeErr := op.Contract.Decode(body)
		if decodeErr != nil {
			return decodeErr
		}
		for _, invariant := range op.Contract.Invariants {
			if invErr := invariant(value); invErr != nil {
				return invErr
			}
		}
		if op.Contract.Normalize != nil {
			value = op.Contract.Normalize(value)
		}

		if evidencePolicy != types.EvidenceNone {
			carrier, ok := any(value).(EvidenceCarrier)
			if !ok {
				return &types.OperationError{
					Kind: types.KindEvidenceViolation,
					Op:   op.ID.Name,
					Message: fmt.Sprintf(
						"%T declares no evidence claims (does not implement EvidenceCarrier) though this operation's evidence policy is %q",
						value, evidencePolicy),
				}
			}
			claims := carrier.EvidenceClaims()
			var sources map[string]types.EvidenceSource
			if sourced, ok := any(input).(EvidenceSourced); ok {
				sources = sourced.EvidenceSources()
			}
			if evErr := StrictEvidence(op.ID.Name, evidencePolicy, op.Contract.MaterialFields, claims, sources); evErr != nil {
				return evErr
			}
		}

		candidate = value
		return nil
	})

	if err != nil {
		// withRepair returns two different failures through the same `error`
		// return: a transport failure (callLLM never produced a body to
		// validate) and a validation exhaustion (every attempt produced an
		// answer this op's Decode or Invariants rejected). Only the second
		// carries a raw body worth offering to a diagnostic sink, and the two
		// are distinguishable without withRepair exposing a third return
		// value: a validation exhaustion recorded one failure per attempt --
		// validate ran and rejected the body every time -- while a transport
		// failure short-circuits before validate is ever called for that
		// attempt, so the failure count trails the attempt count.
		if repair.Attempts > 0 && len(repair.Failures) == repair.Attempts {
			// TC-006: KindInvariantViolation and KindEvidenceViolation are
			// not "we could not fix the shape" -- they are the runtime
			// having positively determined the content was wrong or
			// unsupported, which is a stronger, more specific fact than
			// generic exhaustion. review.go's reviewableFailure already
			// treats both kinds as directly reviewable without requiring
			// KindRepairExhausted; collapsing them into it here made that
			// branch unreachable from this path. Every other exhausted
			// validation (malformed output, a schema shape that never
			// converged) keeps reporting KindRepairExhausted, unchanged.
			if kind := types.KindOf(err); kind == types.KindInvariantViolation || kind == types.KindEvidenceViolation {
				return zero, repair, err
			}
			ref := captureDiagnostic(ctx, op.ID.String(), types.KindRepairExhausted, lastBody)
			return zero, repair, &types.OperationError{
				Kind:       types.KindRepairExhausted,
				Op:         op.ID.Name,
				Cause:      err,
				Diagnostic: ref,
			}
		}
		return zero, repair, err
	}
	return candidate, repair, nil
}

// RunOpResult runs op exactly as RunOp does and additionally returns the
// execution envelope: attempts, repairs, and usage summed across them.
//
// It exists because RunOp's (Out, RepairResult, error) return answers "what
// happened to this call" in a shape only this package's own callers read.
// A-009's Revised line asks for more: a caller-supplied invariant
// participating in recovery has to be *visible* in the same envelope every
// other operation reports through (types.Meta), not only in a RepairResult a
// caller has to know to inspect separately. RunOpResult is the same
// execution as RunOp -- it calls RunOp and wraps call recording around it --
// so there is one path, not two that could drift (T-01's lesson, the same
// one ExtractResult already follows for Extract).
func RunOpResult[In, Out any](ctx context.Context, op Op[In, Out], input In, opt types.OpOptions) (types.Result[Out], error) {
	ctx, records := withCallRecording(ctx)

	value, repair, err := RunOp(ctx, op, input, opt)

	meta := envelopeFrom(records.collect(), op.ID.String())

	// envelopeFrom sums Attempts from the provider-call records it collected,
	// which is the same number RepairResult.Attempts carries -- both count
	// every call this logical request made. Repairs is not something
	// envelopeFrom can derive from a ResultMetadata slice at all: nothing in
	// a single provider response says whether the request that produced it
	// was a first try or a repair, only RepairResult does. So it is set here,
	// from the one place that already knows.
	if repair.Attempts > 1 {
		meta.Repairs = repair.Attempts - 1
	}

	// TC-005, the piece this task's permitted files can carry: the tier the
	// caller actually asked for, recorded regardless of outcome so a drift
	// analysis can see "asked Smart" beside whatever Model ended up serving
	// it.
	meta.RequestedTier = opt.Intelligence

	// TC-004: both fields come from the same declaration (contractlevel.go)
	// on a success. A failure reports DeliveredContract at its zero value,
	// ContractPromptOnly, because nothing was actually delivered -- see
	// contractlevel.go's doc comment for what this does and does not track.
	meta.RequestedContract = declaredContractLevel(op.Contract)
	if err == nil {
		meta.DeliveredContract = meta.RequestedContract
	}

	if err != nil {
		return types.Result[Out]{Meta: meta}, err
	}
	return types.Result[Out]{Value: value, Meta: meta}, nil
}

// --- The context/provider-free proof.
//
// contextType and providerType are the two things the Revised line forbids
// an Op from holding. Checked by reflection over the struct shape rather than
// left as a comment, so a field added later that closes over a context or a
// provider fails a test instead of fitting the description by omission.

var (
	contextInterfaceType  = reflect.TypeOf((*context.Context)(nil)).Elem()
	providerInterfaceType = reflect.TypeOf((*llm.Provider)(nil)).Elem()
)

// fieldViolation names a struct field that holds a context or a provider.
type fieldViolation struct {
	path string
	kind string
}

// walkForContextOrProvider recursively inspects a type's fields (including
// through pointers, slices, maps, and nested structs) for anything that is,
// or could hold, a stored context.Context or an llm.Provider.
//
// It does not look inside function signatures. A field like BuildPrompt
// `func(In, types.OpOptions) (string, string)` is exactly the pure-function
// shape the design calls for -- Step[T] in combinators.go is the same idea,
// `func(context.Context) (T, error)`, and taking a context as a call-time
// parameter is normal and required for cancellation to reach a call. What
// the Revised line forbids is Op *storing* one: a field holding a live
// context or provider value that outlives the call that supplied it. A
// parameter type mentioning context.Context (types.OpOptions carries one,
// for operations that have not yet been threaded onto an explicit ctx
// parameter) is not that -- it is supplied fresh by RunOp on every call and
// never retained by Op itself.
func walkForContextOrProvider(t reflect.Type, path string, seen map[reflect.Type]bool) []fieldViolation {
	if t == nil {
		return nil
	}
	if seen[t] {
		return nil
	}
	seen[t] = true

	if t == contextInterfaceType || (t.Kind() == reflect.Interface && t.Implements(contextInterfaceType)) {
		return []fieldViolation{{path: path, kind: "context.Context"}}
	}
	if t == providerInterfaceType || (t.Kind() == reflect.Interface && t.Implements(providerInterfaceType)) {
		return []fieldViolation{{path: path, kind: "llm.Provider"}}
	}

	var violations []fieldViolation
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			violations = append(violations, walkForContextOrProvider(field.Type, path+"."+field.Name, seen)...)
		}
	case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
		violations = append(violations, walkForContextOrProvider(t.Elem(), path+"[]", seen)...)
	}
	return violations
}

// stableOpDescriptor is a lightweight, type-erased record of an Op's identity
// and batch class, kept so the registry below does not need one slice per
// instantiated In/Out pair.
type stableOpDescriptor struct {
	id    types.OperationID
	batch types.BatchClass
}

var (
	stableOpsMu sync.Mutex
	stableOps   []stableOpDescriptor
)

// registerStableOp records a stable operation's identity and batch class so
// TestAllRegisteredStableOpsDeclareBatchClass can assert none of them regress
// to BatchUnspecified without going through NewOp -- belt, and the
// suspenders NewOp/MustNewOp already are.
func registerStableOp(id types.OperationID, batch types.BatchClass) {
	stableOpsMu.Lock()
	defer stableOpsMu.Unlock()
	stableOps = append(stableOps, stableOpDescriptor{id: id, batch: batch})
}

package ops

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-012. Loop fusion.
//
// docs/engineering/plans/to-production.md 9.8 describes the caller-facing
// shape: a caller keeps a natural Go loop, defers each call, and a
// BatchGroup fuses compatible work into MDSP plans instead of one call per
// iteration. That public surface (schemaflux.NewBatchGroup, Add,
// Extracting[Person](input)) lives in the root package and the fluent
// builders, both outside this task's file list (internal/ops/fusion.go,
// fusion_test.go, fusion_partition_test.go only). What this file builds is
// the internal primitive a future public API sits on: FusionGroup[In, Out],
// which groups deferred calls to one Op[In, Out] by compatibility and runs
// each compatible group through RunOpManyPartial (partial.go).
//
// # Why RunOpManyPartial, not RunOpBatch
//
// RunOpBatch (batchprotocol.go) is the literal MDSP path -- many items in
// one provider call, resolved by OP-101's invocation-local id protocol.
// RunOpManyPartial dispatches one provider call per item, concurrently, and
// reports PL-008's per-item outcome. This file uses the latter, per this
// task's own instruction: "RunOpManyPartial ... is what a fused group
// should ultimately run through." The correctness property PL-012 asks for
// -- fusion must not change results relative to running each builder alone
// -- holds under RunOpManyPartial for a simpler reason than the MDSP id
// protocol gives RunOpBatch: RunOpManyPartial's outcomes slice is sized and
// indexed by len(items) up front (partial.go's runCollect/runFailFast),
// and every goroutine writes to its own fixed index, derived from which
// item it was handed, never from anything in the response. There is no
// response position to trust or distrust in the first place.
//
// What this file must still get right, and what the two files below prove
// it does, is the other half of the same defect class: a fused GROUP is
// assembled from builders that were deferred in a caller's loop and then
// reordered into per-partition batches. Handing a caller back "the result
// at index 7" the same way the fixed-index-per-item property above works
// INSIDE one RunOpManyPartial call would reintroduce exactly the id/position
// confusion OP-101 exists to prevent, one layer up: index 7 of WHICH
// partition, assembled in WHAT order, is not something a caller holding a
// bare int can reconstruct safely. FusionHandle instead holds a direct
// pointer to its own fusionEntry, set once by Defer and read back after Run
// -- resolution never goes through a slice index at all, in either
// direction.

// RoutePolicy is the two OpOptions fields that decide which route serves a
// call: the caller's model pin (OpOptions.Model, TC-005) and the
// intelligence tier it falls back to when no pin is set
// (OpOptions.Intelligence). types.OperationID/OutputContract carry no
// notion of "route" today, and docs/engineering/plans/to-production.md's
// schemaflux.RoutePolicy is aspirational public API this task's file list
// does not reach -- this is fusion's own, minimal stand-in, built from the
// two fields that already exist and already determine routing.
type RoutePolicy struct {
	Model        string
	Intelligence types.Speed
}

// BudgetSettings is fusion's stand-in for "budget settings" (to-production.md
// 9.8). No per-call budget field exists on types.OpOptions today: mw.Budget
// (mw/budget.go) is a middleware-scoped ceiling with its own ledger, and
// pricing.SetBudget is process-wide -- neither is a value a caller attaches
// to one deferred call. Scope names which ledger a ceiling belongs to
// (empty is a caller's own convention); CeilingUSD is the ceiling itself.
// Fusion requires exact equality here, not the "compatible" language
// to-production.md uses -- combining two different non-zero ceilings safely
// would need the group to track combined spend against both, which is
// machinery this task does not build. That is a real, stated limitation,
// not a hidden one: see fusion_test.go's
// TestBudgetSettingsMustMatchExactlyNotJustBeCompatible.
type BudgetSettings struct {
	CeilingUSD float64
	Scope      string
}

// FusionOptions carries the axes of PL-012's compatibility key that have no
// home on types.OpOptions: DataPolicy (CP-002, internal/types/policy.go
// already defines the type, nothing before this attached one to a single
// deferred call) and Budget. A caller who supplies no FusionOptions gets the
// zero value of each -- the same DataPolicy{} "safe defaults, not
// unrestricted" reading policy.go's own doc comment already establishes,
// and a zero BudgetSettings, both of which still participate in equality
// normally: two builders that both left this unset are compatible on this
// axis, which is correct (neither declared anything, so neither disagrees).
type FusionOptions struct {
	DataPolicy types.DataPolicy
	Budget     BudgetSettings
}

// FusionKey is PL-012's compatibility key: "fusion is legal only when
// operation ID and version, schema hash, route policy, steering, contract
// level, data policy, and budget settings all match; otherwise the group
// partitions" (TODOS.md PL-012, docs/engineering/plans/to-production.md
// 9.8). Each field below is one of those seven, in the order the task names
// them.
type FusionKey struct {
	Operation  types.OperationID
	SchemaHash string
	Route      RoutePolicy
	Steering   string
	Contract   types.ContractLevel
	DataPolicy types.DataPolicy
	Budget     BudgetSettings
}

// equal compares two keys by walking the whole struct with reflect.DeepEqual
// rather than a hand-written field-by-field ==/!= chain.
//
// This is the direct answer to this task's own warning: "a hand-written
// list of fields cannot fail when somebody adds an eighth -- that is
// A-014's bug." A-014 (optionsurvival_test.go) and the providerconfig
// override bug it names (providerconfig_override_test.go, repo root) were
// both a copy function that named eight of thirteen fields, so a ninth
// silently rode along unset. The failure mode here is the mirror image --
// not "a field is dropped on the way through," but "a field is ignored when
// deciding whether two things are allowed to share a call" -- and the same
// fix applies: nothing here enumerates Operation, SchemaHash, Route, and so
// on by name to compare them. reflect.DeepEqual walks whatever fields
// FusionKey has. A caller who later adds an eighth axis to this struct (say,
// a region pin) gets it INCLUDED in equal() for free, on the same commit
// that adds the field, with no second edit to a comparison list to
// remember. Two DataPolicy or two BudgetSettings values, both of which
// nest their own fields (including DataPolicy's slices, which == cannot
// compare at all), are exactly why this has to be DeepEqual and not a
// comparable-struct ==.
//
// fusion_test.go's TestFusionKeyEqualityIsReflectionDrivenPerField is the
// proof: it walks FusionKey by reflection too, and for every field it can
// construct a distinguishable second value for, asserts that changing ONLY
// that field flips equal() from true to false. A field this file's bump
// helper cannot yet mutate fails that test loudly (a required update, not a
// silent gap) rather than passing by never being exercised.
func (k FusionKey) equal(other FusionKey) bool {
	return reflect.DeepEqual(k, other)
}

// deriveFusionKey builds a FusionKey for one deferred call from the pieces
// that already exist: op.ID (axis 1), opt.SchemaID (axis 2, "the schema's
// full identity -- name, version, hash, dialect" per its own doc comment in
// internal/types/types.go), opt.Model/opt.Intelligence (axis 3),
// opt.Steering (axis 4), declaredContractLevel(op.Contract) (axis 5, a pure
// function of the Op's own declaration -- see contractlevel.go), and the two
// axes FusionOptions supplies because OpOptions carries no field for them
// (axes 6 and 7).
func deriveFusionKey[In, Out any](op Op[In, Out], opt types.OpOptions, fo FusionOptions) FusionKey {
	return FusionKey{
		Operation:  op.ID,
		SchemaHash: opt.SchemaID,
		Route:      RoutePolicy{Model: opt.Model, Intelligence: opt.Intelligence},
		Steering:   opt.Steering,
		Contract:   declaredContractLevel(op.Contract),
		DataPolicy: fo.DataPolicy,
		Budget:     fo.Budget,
	}
}

// normalizedOptions returns a copy of opt with the call-identity/tracing
// fields zeroed: Context, RequestID, CorrelationID.
//
// RunOpManyPartial (partial.go) takes exactly one types.OpOptions for an
// entire group and applies it to every item uniformly -- there is no
// per-item opt in its signature. Two builders whose FusionKey agrees but
// whose Mode, Threshold, Intelligence, Model, JSONSchema, SchemaName,
// SchemaID, ResponseFormat, CacheIdentity, MaxOutputTokens, or
// ParentResultIDs disagree cannot share that one options value without one
// of them silently running under the other's settings -- exactly the
// "fusion changes results" defect PL-012's Verify line forbids. This check
// is what stops that: two builders join a partition only if their
// normalized options are identical, not merely their FusionKey.
//
// Zeroing only the three excluded fields, rather than naming the fields
// that must match, is deliberate for the same reason FusionKey.equal is
// DeepEqual rather than a field list: a field added to types.OpOptions
// after this was written defaults to REQUIRED to match, because
// reflect.DeepEqual on the normalized copy includes it automatically. The
// asymmetry is the safe one under AGENTS.md's never-fail-open rule -- an
// unrecognized new field can only make fusion over-partition (more,
// smaller groups, still correct, just less optimized), never
// under-partition and silently apply one builder's setting to another's
// call.
//
// Context, RequestID, and CorrelationID are excluded because they identify
// or trace a call rather than change what it computes, and RunOpManyPartial
// already applies a single value of each across every item for every
// existing caller -- fusion inherits that, it does not introduce it.
func normalizedOptions(opt types.OpOptions) types.OpOptions {
	opt.Context = nil
	opt.RequestID = ""
	opt.CorrelationID = ""
	return opt
}

// fusionEntry is one deferred call: the Op it targets, its own input, the
// options it asked for, the FusionOptions axes OpOptions has no field for,
// and -- once Run has executed the partition this entry landed in -- its
// own outcome.
type fusionEntry[In, Out any] struct {
	op    Op[In, Out]
	input In
	opt   types.OpOptions
	fo    FusionOptions
	key   FusionKey

	done   bool
	result types.ItemResult[Out]
}

// compatible reports whether a and b may share one partition -- and
// therefore one RunOpManyPartial call, and therefore one shared
// types.OpOptions. Both halves must hold: the FusionKey (PL-012's named
// seven axes) and the normalized options (the axes RunOpManyPartial's
// signature forces to be shared regardless of what PL-012 names).
func compatible[In, Out any](a, b *fusionEntry[In, Out]) bool {
	if !a.key.equal(b.key) {
		return false
	}
	return reflect.DeepEqual(normalizedOptions(a.opt), normalizedOptions(b.opt))
}

// partitionEntries groups entries into fusion-eligible clusters, preserving
// each entry's relative order within its partition and ordering partitions
// by when their first member was seen. A builder is placed in the first
// partition whose representative (its own first member) it is compatible
// with; none found opens a new partition. This is a linear scan of existing
// partitions per entry (worst case O(n^2) in the number of distinct
// partitions), which is the right tradeoff for a batch group sized for a
// caller's own loop rather than a map keyed on FusionKey: FusionKey nests
// types.DataPolicy's slices, which are not comparable and so cannot be a Go
// map key at all without first flattening them into a hashable form --
// exactly the kind of hand-rolled canonicalization step that could itself
// drop a field.
func partitionEntries[In, Out any](entries []*fusionEntry[In, Out]) [][]*fusionEntry[In, Out] {
	var groups [][]*fusionEntry[In, Out]
	for _, e := range entries {
		placed := false
		for gi := range groups {
			if compatible(groups[gi][0], e) {
				groups[gi] = append(groups[gi], e)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []*fusionEntry[In, Out]{e})
		}
	}
	return groups
}

// FusionHandle is what Defer returns: a reference to one deferred call's
// eventual outcome, resolved after the owning FusionGroup's Run completes.
// It holds a direct pointer to the entry Defer created, not an index into
// any slice fusion builds internally -- see this file's top-of-file comment
// for why a position-based handle would be the same defect OP-101 exists to
// rule out for MDSP, one layer up.
type FusionHandle[In, Out any] struct {
	entry *fusionEntry[In, Out]
}

// Result returns this builder's outcome and true once the owning
// FusionGroup's Run has completed; before that, or for a zero-value handle,
// it returns the zero ItemResult and false.
func (h FusionHandle[In, Out]) Result() (types.ItemResult[Out], bool) {
	if h.entry == nil || !h.entry.done {
		return types.ItemResult[Out]{}, false
	}
	return h.entry.result, true
}

// FusionGroup collects deferred calls to one Op[In, Out] across a caller's
// loop and, on Run, partitions them by FusionKey/normalized-options
// compatibility and executes each partition through RunOpManyPartial.
//
// Fixing In/Out and Op per group (rather than per builder, as
// docs/engineering/plans/to-production.md 9.8's untyped BatchGroup does)
// sidesteps a real limit rather than hiding one: Op holds function fields
// (BuildPrompt, Decode, Invariants...), and Go func values are not
// comparable except against nil, so there is no way to check "two builders
// declare the same operation" beyond op.ID -- already FusionKey's first
// axis -- without either trusting that identity or refusing to fuse
// anything at all. This type trusts it the way every operation already
// registered in this package does: an Op is a package-level singleton built
// once via MustNewOp/NewOp (op.go) and reused, so two calls reporting the
// same OperationID were built from the same Op value by construction. A
// caller assembling Op[In, Out] values ad hoc, differently, under the same
// OperationID could defeat that assumption; this file does not detect that
// case, because nothing short of executing both prompts and comparing
// answers could.
type FusionGroup[In, Out any] struct {
	mu      sync.Mutex
	entries []*fusionEntry[In, Out]
	ran     bool
}

// NewFusionGroup returns an empty group ready for Defer calls.
func NewFusionGroup[In, Out any]() *FusionGroup[In, Out] {
	return &FusionGroup[In, Out]{}
}

// Defer registers one call for fused execution and returns a handle to its
// eventual outcome. It performs no I/O: the FusionKey is computed now (so
// Run's partitioning has no field left to derive late, after a caller might
// have mutated a shared opt/fo value), but nothing is dispatched until Run.
func (g *FusionGroup[In, Out]) Defer(op Op[In, Out], input In, opt types.OpOptions, fo FusionOptions) FusionHandle[In, Out] {
	e := &fusionEntry[In, Out]{
		op:    op,
		input: input,
		opt:   opt,
		fo:    fo,
		key:   deriveFusionKey(op, opt, fo),
	}

	g.mu.Lock()
	g.entries = append(g.entries, e)
	g.mu.Unlock()

	return FusionHandle[In, Out]{entry: e}
}

// ErrFusionGroupAlreadyRan reports a second Run call on a group. A group is
// a one-shot plan over the builders deferred before Run was called; running
// it twice would either duplicate every provider call (if entries were
// re-executed) or silently no-op the second call (if they were not), and
// neither is a result a caller asking "did my second Run do anything" can
// tell apart from success without this.
var ErrFusionGroupAlreadyRan = errors.New("fusion group already ran")

// Run partitions every builder deferred so far and executes each partition
// through RunOpManyPartial, using cfg for all of them. Every FusionHandle
// returned by Defer is resolved (Result() begins returning true) once Run
// returns, regardless of whether Run itself returns an error -- a
// partition's failure is recorded on its own entries and does not prevent
// other partitions from running or resolving.
//
// The returned error is nil only if every partition's RunOpManyPartial call
// returned nil; otherwise it is errors.Join of every partition error that
// occurred, so a caller using errors.Is/errors.As can still find a specific
// one. A caller who wants the truth about which individual builder
// succeeded reads Result() off that builder's own handle -- the same
// PL-008 contract RunOpManyPartial's own doc comment states: "read
// BatchResult.Complete(), not the error return" for the policies whose
// contract is exactly that.
func (g *FusionGroup[In, Out]) Run(ctx context.Context, cfg PartialConfig) error {
	g.mu.Lock()
	if g.ran {
		g.mu.Unlock()
		return ErrFusionGroupAlreadyRan
	}
	entries := append([]*fusionEntry[In, Out](nil), g.entries...)
	g.ran = true
	g.mu.Unlock()

	if len(entries) == 0 {
		return nil
	}

	partitions := partitionEntries(entries)

	var errs []error
	for _, part := range partitions {
		items := make([]In, len(part))
		for i, e := range part {
			items[i] = e.input
		}

		// Every member of part is, by partitionEntries' construction,
		// compatible with part[0] -- FusionKey-equal and
		// normalized-options-equal -- so part[0]'s op and opt are exactly as
		// valid a representative for the whole partition as any other
		// member's.
		res, err := RunOpManyPartial(ctx, part[0].op, items, part[0].opt, cfg)

		for i, e := range part {
			if i < len(res.Items) {
				e.result = res.Items[i]
			}
			e.done = true
		}
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// Len reports how many builders have been deferred so far.
func (g *FusionGroup[In, Out]) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.entries)
}

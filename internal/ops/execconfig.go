package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// IN-004. The client becomes the execution boundary.
//
// The problem this replaces is not a race — IN-001's mutexes already made the
// package globals safe to touch concurrently. It is that there is only ONE of
// them. `WithProviderConfig` on a second client rewrote the provider the first
// client's in-flight calls were using, so two clients in one process were never
// two clients; they were one, with the last constructor winning. A mutex around
// a global passes `-race` and still fails that.
//
// ExecConfig is the snapshot a client owns: the things that decide how a call
// executes, resolved once at construction and never mutated afterwards. It
// travels on the context, which is what makes it per-call rather than
// per-process — a second client's construction cannot reach into a context the
// first client's caller is already holding.
//
// Immutability is the load-bearing property, not a style preference. A snapshot
// a caller could mutate mid-flight would reintroduce exactly the bug: one
// goroutine's reconfiguration changing another goroutine's in-progress call.
// WithExecConfig therefore stores a copy, and the accessors return values.

// BudgetChecker is the per-client spend gate. It is an interface rather than a
// concrete type so a client can hold its own ledger while the process-wide
// `pricing` budget remains the default for callers who never made a client —
// which is every existing caller, and none of them should change behaviour.
//
// Check runs before a request is sent, never after: a budget enforced from the
// invoice is not a budget. Record runs after, with the cost actually reported.
type BudgetChecker interface {
	// Check reports whether a call may proceed. A non-nil error refuses it.
	Check() error

	// Record adds an observed cost. It takes a `priced` flag rather than
	// treating zero as free: an unpriced model reports a cost of zero, and a
	// ledger that accumulates those silently stops enforcing anything.
	Record(costUSD float64, priced bool)
}

// ExecConfig is one client's immutable execution snapshot.
//
// A nil field means "fall back to the process default", so a partially
// configured client is not a client that fails — it is one that has an opinion
// about some things and not others. That matters because the alternative is
// requiring every caller to supply a scheduler and a budget before they can
// make a single call.
type ExecConfig struct {
	// Provider is this client's provider. It takes priority over the package
	// default and over the test caller hook.
	Provider llm.Provider

	// Budget is this client's spend ledger. Nil means the process-wide
	// pricing budget applies, which is the pre-IN-004 behaviour.
	Budget BudgetChecker

	// Scheduler bounds this client's concurrency. Nil means unscheduled, as
	// before.
	Scheduler *Scheduler

	// DataPolicy constrains where this client's calls may be routed.
	DataPolicy types.DataPolicy
}

type execConfigKey struct{}

// WithExecConfig returns a context carrying a copy of cfg.
//
// The copy is the point. Storing the caller's pointer would let a client that
// kept a reference mutate the configuration of calls already in flight, which
// is the process-global bug again with extra steps — and it would be harder to
// find, because it would look like per-call configuration.
//
// A nil cfg leaves ctx unchanged rather than storing a nil, for the same reason
// WithProvider does: a stored nil would satisfy a non-nil check and then fail
// at the point of use, further from the mistake.
func WithExecConfig(ctx context.Context, cfg *ExecConfig) context.Context {
	if cfg == nil {
		return ctx
	}
	snapshot := *cfg
	return context.WithValue(ctx, execConfigKey{}, &snapshot)
}

// execConfigFrom reads the snapshot a client attached, or nil when none was.
func execConfigFrom(ctx context.Context) *ExecConfig {
	if ctx == nil {
		return nil
	}
	cfg, _ := ctx.Value(execConfigKey{}).(*ExecConfig)
	return cfg
}

// ExecProvider returns the provider a context's snapshot carries, or nil.
//
// It exists so the provider seam has one reader rather than two: WithProvider
// (the narrower, pre-existing seam) and a client snapshot both answer "which
// provider runs this call", and a call path that consulted only one of them
// would work for one kind of caller and silently ignore the other.
func ExecProvider(ctx context.Context) llm.Provider {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.Provider
	}
	return nil
}

// ExecBudget returns the budget a context's snapshot carries, or nil when the
// process-wide budget should apply.
func ExecBudget(ctx context.Context) BudgetChecker {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.Budget
	}
	return nil
}

// ExecScheduler returns the scheduler a context's snapshot carries, or nil.
func ExecScheduler(ctx context.Context) *Scheduler {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.Scheduler
	}
	return nil
}

// ExecDataPolicy returns the data policy a context's snapshot carries, and
// whether one was set at all.
//
// The second return distinguishes "this client declared an empty policy" from
// "no client is configured", which a zero value alone cannot. Treating the two
// as the same would let a client that deliberately allows everything be
// indistinguishable from an unconfigured process — and those should not behave
// identically once policy enforcement is on.
func ExecDataPolicy(ctx context.Context) (types.DataPolicy, bool) {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.DataPolicy, true
	}
	return types.DataPolicy{}, false
}

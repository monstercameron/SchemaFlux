package ops

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/types"
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

	// DataPolicy constrains where this client's calls may be routed, when
	// DataPolicyDeclared says the client set one.
	DataPolicy types.DataPolicy

	// DataPolicyDeclared reports that the client set a data policy. It is a
	// field of its own because the zero DataPolicy is a real policy, the
	// strictest there is, and every client carries a snapshot: with no flag, a
	// client that never asked for a policy would be held to one that allows no
	// provider at all.
	DataPolicyDeclared bool

	// Model is the model this client's calls use when the call names none. It
	// takes the place of the tier's mapping, and a model named on a call takes
	// its place. Empty leaves the tier's mapping in charge.
	Model string

	// Effort is the reasoning effort this client's calls use when the call
	// names none. It is the caller's own choice, like an effort set on a call,
	// and takes the place of an operation's preference.
	Effort types.Effort

	// DecisionObserver is told after every decision (Is, Which, Rate, Assess
	// and their Result forms) what was asked and what was read. Nil means no
	// record is built. It may be called from several goroutines at once, since
	// decisions can run at once.
	DecisionObserver func(context.Context, DecisionRecord)

	// Timeout is the deadline given to a call whose context has none. Zero
	// means config.GetTimeout().
	Timeout time.Duration

	// Retries is how many times a request that failed in transit is sent
	// again. Zero means the provider's own RetryPolicy; a negative value means
	// never.
	Retries int

	// Repairs is how many times a refused answer is sent back, for an
	// operation that has no setting of its own. Zero means
	// config.GetRepairAttempts(); a negative value means never.
	Repairs int

	// OmitReinforcement leaves the fixed reinforcement block off the system
	// prompt.
	OmitReinforcement bool

	// Logger receives the lines written where this snapshot is in reach. Nil
	// means the process logger.
	Logger *logger.Logger

	// Sink is the client's recorder, as this package sees one (see Sink). Nil
	// means none.
	Sink Sink

	// Err, when not nil, is why this client cannot make a call. The provider
	// route returns it when the snapshot has no Provider.
	Err error
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
//
// A nil ctx is read as context.Background(). context.WithValue panics on a nil
// parent, and every reader of the snapshot already reads a nil context as one
// that carries nothing.
func WithExecConfig(ctx context.Context, cfg *ExecConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		return ctx
	}
	snapshot := *cfg
	// The copy goes as deep as the policy's lists, which are the only slices
	// in it: a snapshot that shared them could be changed after it was taken.
	snapshot.DataPolicy.AllowedProviders = append([]string(nil), cfg.DataPolicy.AllowedProviders...)
	snapshot.DataPolicy.AllowedRegions = append([]string(nil), cfg.DataPolicy.AllowedRegions...)
	return context.WithValue(ctx, execConfigKey{}, &snapshot)
}

// defaultExec is the snapshot a call runs on when its context carries none.
// Only a root that has a default client sets it (SetDefaultExec); the old root
// never does, and with it nil every reader below answers as it always did.
var defaultExec atomic.Pointer[ExecConfig]

// SetDefaultExec makes cfg the snapshot of a call whose context carries none and
// returns the one it replaced. cfg is stored as it is given and is not modified
// afterwards. Nil clears it, which is the state the old root runs in: nothing
// but a root with a default client calls this.
func SetDefaultExec(cfg *ExecConfig) (previous *ExecConfig) {
	return defaultExec.Swap(cfg)
}

// WithSharedExecConfig returns a context that carries cfg itself, with no copy.
// The caller does not modify cfg afterwards. A nil cfg returns ctx. A nil ctx
// panics, as context.WithValue does.
//
// It is for a client that builds its snapshot once and hands the same one to
// every call: WithExecConfig copies the snapshot and the policy's lists on
// every call, which a snapshot that is never modified has no need of.
func WithSharedExecConfig(ctx context.Context, cfg *ExecConfig) context.Context {
	if cfg == nil {
		return ctx
	}
	return context.WithValue(ctx, execConfigKey{}, cfg)
}

// PinExec returns a context that carries the snapshot its calls run on, and that
// snapshot. A context that carries one is returned as it is; otherwise the
// default, loaded once, is placed on it (one context value); with no default
// the context is returned as it is and the snapshot is nil.
//
// A call pins first and every reader after it reads the context. Without the
// pin each reader would load the default for itself, and a default replaced
// while the call was in flight would have the provider of one request and the
// budget of the next.
func PinExec(ctx context.Context) (context.Context, *ExecConfig) {
	if ctx != nil {
		if cfg, _ := ctx.Value(execConfigKey{}).(*ExecConfig); cfg != nil {
			return ctx, cfg
		}
	}
	cfg := defaultExec.Load()
	if cfg == nil {
		return ctx, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, execConfigKey{}, cfg), cfg
}

// execConfigFrom reads the snapshot a client attached, or the default one, or
// nil.
func execConfigFrom(ctx context.Context) *ExecConfig {
	if ctx != nil {
		if cfg, _ := ctx.Value(execConfigKey{}).(*ExecConfig); cfg != nil {
			return cfg
		}
	}
	return defaultExec.Load()
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

// ExecModel returns the model a context's client names for its calls, or ""
// when it names none.
func ExecModel(ctx context.Context) string {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return strings.TrimSpace(cfg.Model)
	}
	return ""
}

// ExecEffort returns the reasoning effort a context's client names for its
// calls, or EffortDefault when it names none.
func ExecEffort(ctx context.Context) types.Effort {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.Effort
	}
	return types.EffortDefault
}

// ExecDecisionObserver returns the decision observer a context's client
// registered, or nil when it registered none. An observer put on the context
// itself (WithDecisionObserver) is read first: it is the call's own, and it
// already tells the observers that were on the context before it.
func ExecDecisionObserver(ctx context.Context) func(context.Context, DecisionRecord) {
	if fn := decisionObserverFrom(ctx); fn != nil {
		return fn
	}
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.DecisionObserver
	}
	return nil
}

// resolveModel is the model a call uses: the one the call names, then the one
// its client names, then the one the provider says the tier means for it, then
// the process-wide table's. It is "" when none of the four names one. provider
// may be nil, which is a call with no provider to ask.
//
// The provider's own answer is read only when it is not the built-in model of
// the provider's name. A built-in provider answers with that model when it was
// configured with none of its own, and taking that for a configured choice
// would put the built-in table in front of SCHEMAFLUX_MODEL and its three
// siblings, which have always been read first.
func resolveModel(ctx context.Context, opts types.OpOptions, provider llm.Provider) string {
	// The name is returned as it is read, trimmed: the check was trimmed and
	// the value was not, so a name with a space after it was sent with the
	// space.
	if model := strings.TrimSpace(opts.Model); model != "" {
		return model
	}
	if model := ExecModel(ctx); model != "" {
		return model
	}
	name := ""
	if provider != nil {
		name = provider.Name()
		if model, ok := llm.ModelFor(provider, opts.Intelligence); ok && model != "" && model != config.BuiltInModel(name, opts.Intelligence) {
			return model
		}
	}
	return config.GetModel(opts.Intelligence, name)
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

// ExecDataPolicy returns the data policy the client on a context declared, and
// whether it declared one.
//
// The second return is what a caller acts on. A client that set an empty policy
// has declared the strictest policy there is, and a client that set none has
// declared nothing and is not restricted; the zero value cannot tell the two
// apart, and they do not behave alike now that the policy is enforced
// (checkDataPolicy). This used to report true for any context with a client on
// it, which under enforcement would have refused every call of every client
// that never asked for a policy.
func ExecDataPolicy(ctx context.Context) (types.DataPolicy, bool) {
	if cfg := execConfigFrom(ctx); cfg != nil && cfg.DataPolicyDeclared {
		// The lists are copied on the way out as they were on the way in. The
		// snapshot is shared by every call the client makes, and a caller that
		// edited the slices it was handed would have changed the policy of
		// calls already in flight.
		policy := cfg.DataPolicy
		policy.AllowedProviders = append([]string(nil), policy.AllowedProviders...)
		policy.AllowedRegions = append([]string(nil), policy.AllowedRegions...)
		return policy, true
	}
	return types.DataPolicy{}, false
}

package schemaflux

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A Client says how calls are made: which provider answers them, on which model,
// and within what limits. Every operation runs on a client. A call whose context
// carries one (WithClient) runs on that one; any other call runs on the default
// client (Default).
//
// The zero Client is ready to use. It sends its calls to the provider the
// environment names, on that provider's default model, with no budget, no
// scheduler and no data policy. When the environment names no usable provider
// (no key is set anywhere), a call on it returns an *Error of kind
// ErrConfiguration that says which variables it looked for; nothing is sent,
// nothing is logged, and no stand-in provider answers.
//
// A Client is a value. Assigning to a field changes that copy and no other, so a
// client is configured before it is handed to WithClient or SetDefault, both of
// which keep a snapshot. Nothing a caller assigns afterwards reaches the default
// or a call in flight. What the pointer fields refer to is shared: copies of a
// client spend from the same Budget, queue on the same Scheduler and report to
// the same Recorder, and those three are not modified after first use. The
// Policy is copied by WithClient and SetDefault; changing it afterwards has no
// effect. Two clients are compared field by field, not with ==, which panics
// when Provider holds a value of a type that cannot be compared. A Client is
// safe for concurrent use by multiple goroutines.
//
// A setting that a call cannot use (a negative Timeout, a Budget with a limit
// that is negative or not a number, a Scheduler with a negative bound) makes
// every call on the client fail with an *Error of kind ErrConfiguration that
// names the field, before anything is sent.
type Client struct {
	// Provider is what calls are sent to. Nil means the provider the environment
	// names (SCHEMAFLUX_PROVIDER, default "openai"), built with the key the
	// environment holds for it. The environment is read when a call needs it, and
	// the provider is built once for the process and kept. A build that fails (no
	// key is set yet, because the program loads its variables after the library is
	// linked) is not kept: the next call reads the environment again. A provider
	// given here is used as it is: the environment is not consulted, so a key
	// given in code always wins, and no variable changes it. Make one with the
	// provider package, wrap one with the mw package, or implement Provider.
	Provider Provider

	// Model is the model calls ask for when the call names none. Empty means the
	// provider's own model for the operation, which for the provider the
	// environment names SCHEMAFLUX_MODEL can set. A model named in a call's
	// options wins over this. A call's Tier does not override it.
	Model string

	// Effort is how much the model reasons before it answers, for calls that name
	// no effort. The zero value leaves the choice to the operation and the
	// provider. It is a preference: it is sent where the model takes it and left
	// off where it does not. An effort named in a call's options wins, and is a
	// requirement.
	Effort Effort

	// Timeout is the deadline given to a call whose context has none. Zero means
	// SCHEMAFLUX_TIMEOUT when it is set, and otherwise 30 seconds. A context that
	// has a deadline keeps it, shorter or longer. A negative value is a
	// configuration error.
	Timeout time.Duration

	// Retries is how many times a request that failed in transit (a rate limit,
	// an outage, a timeout) is sent again. Zero means the provider's own setting,
	// which is 3 for a provider built by the provider package or from the
	// environment. A Provider written outside the library that is not a
	// provider.Retrier has no setting of its own, and is sent a failed request 3
	// more times, after a wait of about a second that grows between them. A
	// negative value means never. A value here overrides the provider's own
	// setting. The wait between tries is the provider's
	// (provider.Config.RetryBackoff). A count above 65536 is held to 65536.
	Retries int

	// Repairs is how many times an answer that failed its checks is sent back to
	// the model with what was wrong. Only Extract and the op package's runners
	// repair; no other operation has a repair loop. Zero means the operation's
	// own setting where it has one (a descriptor's), else 1. A descriptor's own
	// setting is not overridden by this field. A negative value means never: the
	// first refused answer is the error. A count above 65536 is held to 65536.
	Repairs int

	// Budget is this client's spend ceiling and ledger. Nil means the client
	// keeps no ledger of its own and the ceiling set with pricing.SetLimits, if
	// any, applies. A Budget never lifts a ceiling: a call on a client that has
	// one is held to the process ceiling as well as to the Budget's own Limit,
	// and a Budget with no Limit, given only to count, counts and limits nothing
	// itself. A call is refused with ErrBudgetExceeded, before it is sent, once
	// the ledger has reached its Limit, and, when the Budget has a Limit, a call
	// on a model the library has no price for is refused too, unless the Budget
	// says unpriced calls are allowed.
	Budget *Budget

	// Scheduler bounds how many of this client's requests are with a provider at
	// once. Nil means no bound.
	Scheduler *Scheduler

	// Policy restricts where this client's calls may be sent. Nil means no
	// restriction. A policy does not fail open: see DataPolicy.
	Policy *DataPolicy

	// Recorder, when not nil, is given a Record of every call made on this
	// client, beside any recorder the call's context carries. It is how a program
	// keeps a standing account of its calls or an audit trail of its decisions. A
	// recorder with no Handle keeps every Record until Reset: on a client, set
	// Handle or call Reset.
	Recorder *Recorder

	// Logger receives the library's own log lines for this client's calls: a
	// retry, a refusal, a prompt cache that never hits, and at debug level the
	// start and end of each request. Nil means the library logs nothing; a
	// failure is returned, not logged. No line carries the caller's data or a
	// model's text at any level. A line that names a failure carries its kind
	// and nothing of the error's text, which is a provider's or a caller's,
	// whether the provider is built in or written outside the library and
	// whatever SCHEMAFLUX_ERROR_DETAIL says; the provider, the model and the ids
	// of a line are cut to a bounded length and stripped of anything that is not
	// printable ASCII.
	Logger *slog.Logger

	// OmitReinforcement leaves out the fixed instructions the library puts ahead
	// of every system prompt ("answer from the input; return only the JSON"). It
	// is for a caller who has measured their prompts on their model without it.
	// The decisions never send them.
	OmitReinforcement bool
}

// defaultCallTimeout is the deadline of a call whose context has none, when
// neither the client nor SCHEMAFLUX_TIMEOUT names one.
const defaultCallTimeout = 30 * time.Second

// quietLogger is the logger of a client that names none: it writes nowhere.
var quietLogger = telemetry.Discard()

// The default client.
//
// defaultKept is the client Default returns and defaultMu orders the changes to
// it with the internal default, so that the two are never two different
// clients. The internal default is what a call whose context carries no client
// pins when it starts: ops.SetDefaultExec.
var (
	defaultMu   sync.Mutex
	defaultKept atomic.Pointer[Client]
)

func init() {
	installDefault(Client{})
}

// installDefault makes c the default client: the one Default returns, the one
// calls with no client on their context run on, and the one whose Logger the
// process logger is. The caller holds defaultMu, or is init.
//
// A client with no Provider resolves the environment on its first call and not
// here, so that a program may load its environment after the library is linked
// and before it calls.
func installDefault(c Client) {
	kept := c.clone()
	cfg := c.execConfig(currentEnvironment(), true)
	defaultKept.Store(&kept)
	ops.SetDefaultExec(cfg)
	logger.SetLogger(cfg.Logger)
}

// Default returns the client that calls run on when their context carries none.
// Until SetDefault is called it is the zero Client. It returns a copy: changing
// the copy changes nothing until it is given to SetDefault or WithClient.
//
//	c := schemaflux.Default()
//	c.Model = "gpt-6-luna"
//	ctx = schemaflux.WithClient(ctx, c)
func Default() Client {
	return defaultKept.Load().clone()
}

// SetDefault makes c the client that calls run on when their context carries
// none. It is the only thing that changes the default: making, copying or
// changing a Client never does. It is safe to call while calls are in flight; a
// call that has started keeps the client it started with.
//
// SetDefault keeps a snapshot of c: assigning to c afterwards changes nothing.
// It also makes c's Logger the logger of the library's lines that are written
// where no call's context is in reach; with no Logger, nothing is written.
//
//	schemaflux.SetDefault(schemaflux.Client{Provider: p, Budget: &schemaflux.Budget{Limit: 5}})
func SetDefault(c Client) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	installDefault(c)
}

// WithClient returns a context whose calls run on c. It is how a call is given a
// client: the operations are generic functions, which Go does not allow as
// methods, so the client travels where the deadline does.
//
//	ctx = schemaflux.WithClient(ctx, tenantClient)
//	invoice, err := schemaflux.Extract[Invoice](ctx, text)
//
// WithClient keeps a snapshot of c: assigning to c afterwards changes nothing.
// It builds the snapshot each time it is called; in a server, call it once per
// request. A nil ctx panics, as context.WithValue does.
func WithClient(ctx context.Context, c Client) context.Context {
	if ctx == nil {
		panic("cannot create context from nil parent")
	}
	return ops.WithSharedExecConfig(ctx, c.execConfig(currentEnvironment(), false))
}

// Warm makes the client ready for its first call: it resolves the client's
// provider and opens the connection to it, so that the name lookup, the
// connection and the handshake are paid where a program can afford them. It
// sends no prompt and costs nothing. It returns an *Error of kind
// ErrConfiguration when the client could not make a call (a setting it cannot
// use, no provider can be built, or no model is known for the provider), which
// makes it the check to run at start-up. A provider with no connection to open
// is not an error.
//
//	if err := schemaflux.Default().Warm(ctx); err != nil {
//		log.Fatal(err)
//	}
func (c Client) Warm(ctx context.Context) error {
	const op = "warm"
	if ctx == nil {
		return &Error{Kind: ErrInvalidRequest, Op: op, Message: "the context is nil"}
	}
	cfg := c.execConfig(currentEnvironment(), false)
	if cfg.Err != nil {
		return bind.ConvertError(op, cfg.Err)
	}
	if c.Model == "" {
		if err := checkModelsKnown(cfg.Provider); err != nil {
			return bind.ConvertError(op, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return bind.ConvertError(op, err)
	}
	if _, err := llm.Warm(ctx, cfg.Provider); err != nil {
		return bind.ConvertError(op, err)
	}
	return nil
}

// checkModelsKnown returns the configuration error of a provider that means no
// model for at least one tier, by its own table or by the library's built-in
// one. A call on it that names no model would fail at its first request.
func checkModelsKnown(provider llm.Provider) error {
	for _, tier := range []types.Speed{types.Smart, types.Fast, types.Quick} {
		if model, ok := llm.ModelFor(provider, tier); ok && model != "" {
			continue
		}
		if config.BuiltInModel(provider.Name(), tier) != "" {
			continue
		}
		return &types.OperationError{
			Kind:    types.KindConfiguration,
			Op:      "client",
			Message: "no model is known for provider \"" + provider.Name() + "\"; set Client.Model, or Model in the call's options",
			Cause:   config.ErrNoModelMapping,
		}
	}
	return nil
}

// clone returns a copy of c whose Policy is a copy of the policy c points to, so
// that what the copy holds cannot be changed through c or the other way round.
func (c Client) clone() Client {
	c.Policy = c.Policy.clone()
	return c
}

// A Budget is a spend ceiling and the ledger that holds a client to it. The zero
// Budget has no ceiling and still counts, so a client can be given one only to
// learn what it spent. A Budget is safe for concurrent use and must not be
// copied after first use; clients share one by holding the same pointer. Make
// one per tenant and keep it: a Budget made where the call is made is a new
// ledger each time and its limit never binds.
type Budget struct {
	// Limit is the ceiling in US dollars. Zero means no ceiling. A call is
	// refused once the recorded spend has reached it; calls already in flight
	// when it is reached are not recalled, so the spend can pass the limit by
	// what they cost. A negative limit, or one that is not a number, is a
	// configuration error on every call.
	Limit float64

	// AllowUnpriced lets a call on a model the library has no price for go ahead
	// under a Limit. Such a call adds nothing to the ledger, so it is not held to
	// the Limit and the spend can pass it without the ledger knowing: register a
	// price with pricing.Register instead where the model is one you use. False,
	// the zero value, refuses the call before it is sent, with an error of kind
	// ErrBudgetExceeded that says the model has no price. It has no effect on a
	// Budget with no Limit.
	AllowUnpriced bool

	once   sync.Once
	ledger *ops.ClientBudget
}

// Spent reports what the ledger has recorded, in US dollars, and whether that
// figure is complete. It is not complete once any call was made on a model the
// library has no price for: the figure is then a floor. The Limit and
// AllowUnpriced are read when the ledger is first used, which is the first call,
// WithClient or SetDefault that holds the Budget, or the first call of Spent.
func (b *Budget) Spent() (usd float64, complete bool) {
	if b == nil {
		return 0, true
	}
	return b.counter().Spent()
}

// counter is the ledger, made the first time it is asked for.
func (b *Budget) counter() *ops.ClientBudget {
	b.once.Do(func() {
		b.ledger = ops.NewClientBudget(b.Limit).KeepProcessCeiling()
		if !b.AllowUnpriced {
			b.ledger.RefuseUnpriced()
		}
	})
	return b.ledger
}

// check returns the configuration error of a limit no ledger can enforce.
func (b *Budget) check() error {
	if b == nil {
		return nil
	}
	if math.IsNaN(b.Limit) || math.IsInf(b.Limit, 0) || b.Limit < 0 {
		return types.Newf(types.KindConfiguration, "client", "Budget.Limit must be a number that is not negative")
	}
	return nil
}

// A Scheduler bounds how many requests are with a provider at once and how many
// may wait. Give one to a client (Client.Scheduler); give the same one to
// several clients to bound them together. The zero Scheduler has no bounds. A
// Scheduler is safe for concurrent use and must not be copied after first use.
//
// The exported fields are settings: set them before the scheduler is used and do
// not change them after. A Scheduler reads them once, when a client that holds it
// is first used or its Stats or Close first asks, and a change made later reaches
// nothing. A Scheduler starts a goroutine when it is first used for a call and
// the goroutine lives until Close: a Scheduler made for a request and not closed
// leaks it, so make one for as long as the clients that share it, and call Close
// when they are done. A Scheduler no call used has no goroutine to stop.
type Scheduler struct {
	// MaxConcurrent is the most requests in flight at once. Zero means no bound.
	// A negative value is a configuration error.
	MaxConcurrent int

	// MaxQueued is the most requests waiting for a slot. A request that finds the
	// queue full fails with ErrBusy at once. Zero means no bound. A negative
	// value is a configuration error.
	MaxQueued int

	// MaxTokens is the most tokens in flight at once, by the library's estimate
	// of each request (its prompt, and its answer at the output ceiling). Zero
	// means no bound. A negative value is a configuration error.
	MaxTokens int64

	// MaxCost is the most estimated cost in flight at once, in US dollars. A model
	// with no price is estimated at nothing. Zero means no bound. A negative
	// value, or one that is not a number, is a configuration error.
	MaxCost float64

	// MaxPerProvider is the most requests in flight at once to any one provider.
	// Zero means no bound. A negative value is a configuration error.
	MaxPerProvider int

	mu     sync.Mutex
	inner  *ops.Scheduler
	closed bool
}

// A SchedulerStats is a Scheduler's load at one moment.
type SchedulerStats struct {
	// InFlight is the requests with a provider.
	InFlight int

	// Queued is the requests waiting for a slot.
	Queued int

	// Tokens is the estimated tokens in flight.
	Tokens int64

	// Cost is the estimated cost in flight, in US dollars.
	Cost float64
}

// check returns the configuration error of a bound that no scheduler can hold.
func (s *Scheduler) check() error {
	if s == nil {
		return nil
	}
	field := ""
	switch {
	case s.MaxConcurrent < 0:
		field = "Scheduler.MaxConcurrent must not be negative"
	case s.MaxQueued < 0:
		field = "Scheduler.MaxQueued must not be negative"
	case s.MaxTokens < 0:
		field = "Scheduler.MaxTokens must not be negative"
	case math.IsNaN(s.MaxCost) || math.IsInf(s.MaxCost, 0) || s.MaxCost < 0:
		field = "Scheduler.MaxCost must be a number that is not negative"
	case s.MaxPerProvider < 0:
		field = "Scheduler.MaxPerProvider must not be negative"
	}
	if field == "" {
		return nil
	}
	return types.Newf(types.KindConfiguration, "client", "%s", field)
}

// gate is the internal scheduler, made from the bounds the first time it is
// asked for, and the same one after. The bounds are read then. A scheduler that
// was closed before any call used it is made and closed at once, so that the
// call that does use it is refused as a call on a closed scheduler is.
func (s *Scheduler) gate() *ops.Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inner == nil {
		s.inner = ops.NewScheduler(ops.SchedulerLimits{
			MaxConcurrent:          s.MaxConcurrent,
			MaxQueued:              s.MaxQueued,
			MaxInFlightTokens:      s.MaxTokens,
			MaxInFlightCost:        s.MaxCost,
			PerProviderConcurrency: s.MaxPerProvider,
		})
		if s.closed {
			_ = s.inner.Close(context.Background())
		}
	}
	return s.inner
}

// Close stops the scheduler: what is waiting is refused with ErrClosed, what is
// in flight is cancelled, and Close returns when it has finished or ctx ends. A
// scheduler that was closed refuses every later request. Close may be called
// more than once. Closing a scheduler no call has used starts nothing.
func (s *Scheduler) Close(ctx context.Context) error {
	const op = "scheduler.close"
	if s == nil {
		return nil
	}
	if ctx == nil {
		return &Error{Kind: ErrInvalidRequest, Op: op, Message: "the context is nil"}
	}
	if err := s.check(); err != nil {
		return bind.ConvertError(op, err)
	}
	s.mu.Lock()
	s.closed = true
	unused := s.inner == nil
	s.mu.Unlock()
	if unused {
		return nil
	}
	return bind.ConvertError(op, s.gate().Close(ctx))
}

// Stats reports what the scheduler holds at this moment. A scheduler no call has
// used reports nothing in flight.
func (s *Scheduler) Stats() SchedulerStats {
	if s == nil {
		return SchedulerStats{}
	}
	s.mu.Lock()
	inner := s.inner
	s.mu.Unlock()
	if inner == nil {
		return SchedulerStats{}
	}
	stats := inner.Stats()
	return SchedulerStats{
		InFlight: stats.InFlight,
		Queued:   stats.Queued,
		Tokens:   stats.InFlightTokens,
		Cost:     stats.InFlightCost,
	}
}

// A DataPolicy restricts where a client's calls may be sent. It does not fail
// open: a list left empty allows nothing, so the zero DataPolicy refuses every
// call, and a client with no restriction has a nil Policy. A call that the policy
// does not allow fails with ErrPolicy before anything is sent. Where a route runs
// and what it retains are the caller's own statements, made with
// provider.DeclareRoute; a route that has declared nothing is not allowed.
//
// WithClient and SetDefault copy the policy; changing it afterwards has no
// effect.
type DataPolicy struct {
	// Providers are the providers calls may go to. Empty allows none.
	Providers []string

	// Regions are the regions a route may run in. Every region a route may run in
	// has to be listed. Empty allows none.
	Regions []string

	// MaxRetentionDays is the longest a route may keep what it is sent. Zero
	// allows only a route that keeps nothing. A negative value is a configuration
	// error.
	MaxRetentionDays int

	// AllowCaching lets a response cache (mw.Cache) store and serve this client's
	// answers. False passes them through.
	AllowCaching bool

	// MinContract is the weakest guarantee an answer may be delivered with. The
	// zero value sets no floor. A policy that sets only a floor still allows no
	// provider: list the providers and regions and declare each route
	// (provider.DeclareRoute), as in
	//
	//	&DataPolicy{Providers: []string{"openai"}, Regions: []string{"us"}, MaxRetentionDays: 30,
	//		MinContract: ContractSchemaConstrained}
	//
	// A floor above ContractPromptOnly is held on every call of every operation
	// of the client. Summarize, Rewrite, Translate, Expand, Generate into a
	// string, Complete, CompleteField, Diff, Format, Match, Session.Send,
	// Session.SendResults, StreamSummarize, StreamRewrite, StreamTranslate and
	// StreamExpand deliver at that level and are refused with ErrPolicy before
	// anything is sent. A value that is not one of the contract levels is a
	// configuration error.
	MinContract ContractLevel
}

// clone returns a copy of p whose lists are copies, or nil for nil. A copy of
// the empty lists stays what they were, nil or empty, which a policy reads
// alike.
func (p *DataPolicy) clone() *DataPolicy {
	if p == nil {
		return nil
	}
	copied := *p
	copied.Providers = slices.Clone(p.Providers)
	copied.Regions = slices.Clone(p.Regions)
	return &copied
}

// check returns the configuration error of a policy no call can be held to.
func (p *DataPolicy) check() error {
	switch {
	case p == nil:
		return nil
	case p.MaxRetentionDays < 0:
		return types.Newf(types.KindConfiguration, "client", "DataPolicy.MaxRetentionDays must not be negative")
	case p.MinContract > ContractFullyGoverned:
		return types.Newf(types.KindConfiguration, "client", "DataPolicy.MinContract is not one of the contract levels")
	}
	return nil
}

// internal is the policy as the call path holds it, with lists of its own.
func (p *DataPolicy) internal() types.DataPolicy {
	return types.DataPolicy{
		AllowedProviders:   slices.Clone(p.Providers),
		AllowedRegions:     slices.Clone(p.Regions),
		MaxRetentionDays:   types.RetentionDays(p.MaxRetentionDays),
		AllowResultCaching: p.AllowCaching,
		MinimumContract:    contractToInternal(p.MinContract),
	}
}

// contractToInternal is the internal level for a level.
func contractToInternal(c ContractLevel) types.ContractLevel {
	switch c {
	case ContractPromptOnly:
		return types.ContractPromptOnly
	case ContractJSONWellFormed:
		return types.ContractJSONWellFormed
	case ContractSchemaConstrained:
		return types.ContractSchemaConstrained
	case ContractSchemaAndInvariantChecked:
		return types.ContractSchemaAndInvariantChecked
	case ContractEvidenceChecked:
		return types.ContractEvidenceChecked
	case ContractFullyGoverned:
		return types.ContractFullyGoverned
	}
	return types.ContractLevel(c)
}

// check returns the configuration error of the first setting of c that no call
// can use, naming the field, or nil. Nothing of it is read from the environment.
func (c Client) check() error {
	if c.Timeout < 0 {
		return types.Newf(types.KindConfiguration, "client", "Client.Timeout must not be negative")
	}
	if err := c.Budget.check(); err != nil {
		return err
	}
	if err := c.Scheduler.check(); err != nil {
		return err
	}
	if err := c.Policy.check(); err != nil {
		return err
	}
	if err := c.Recorder.check(); err != nil {
		var e *Error
		if errors.As(err, &e) {
			return types.Newf(types.KindConfiguration, "client", "%s", e.Message)
		}
		return err
	}
	return nil
}

// execConfig builds the snapshot a call on c runs on, with the environment read
// from env. It is built once for a WithClient or a SetDefault and never changed
// after, so every call on the client reads the same one.
//
// A client that cannot make a call has no Provider on its snapshot and says why
// in Err, which the call path returns before it sends anything. A client whose
// Provider is nil gets the one env names: built when this is called, or, for
// lazy, on the first request, which is what the default client does so that the
// environment is not read when the library is linked.
func (c Client) execConfig(env *environmentCell, lazy bool) *ops.ExecConfig {
	cfg := &ops.ExecConfig{
		Model:             c.Model,
		Effort:            types.Effort(c.Effort),
		Timeout:           c.Timeout,
		Retries:           min(c.Retries, ops.MaxRetries),
		Repairs:           min(c.Repairs, ops.MaxRetries),
		OmitReinforcement: c.OmitReinforcement,
		Logger:            quietLogger,
	}
	if cfg.Repairs == 0 {
		// The variable that used to set this for every client is gone.
		cfg.Repairs = ops.DefaultRepairAttempts
	}
	if c.Logger != nil {
		cfg.Logger = telemetry.FromSlog(slog.New(logGuard{c.Logger.Handler()}))
	}
	if err := c.check(); err != nil {
		cfg.Err = err
		return cfg
	}
	if c.Budget != nil {
		cfg.Budget = c.Budget.counter()
	}
	if c.Scheduler != nil {
		cfg.Scheduler = c.Scheduler.gate()
	}
	if c.Policy != nil {
		cfg.DataPolicy = c.Policy.internal()
		cfg.DataPolicyDeclared = true
	}
	if c.Recorder != nil {
		cfg.Sink = (*sink)(c.Recorder)
	}

	switch {
	case c.Provider != nil:
		cfg.Provider = bind.ToInternal(c.Provider)
	case lazy:
		cfg.Provider = &environmentProvider{checkTimeout: c.Timeout == 0}
	default:
		provider, err := env.resolve()
		if err != nil {
			cfg.Err = err
			return cfg
		}
		cfg.Provider = provider
	}

	if cfg.Timeout == 0 && !lazy {
		timeout, err := env.timeout()
		if err != nil {
			cfg.Provider, cfg.Err = nil, err
			return cfg
		}
		cfg.Timeout = timeout
		if cfg.Timeout == 0 {
			cfg.Timeout = defaultCallTimeout
		}
	}
	return cfg
}

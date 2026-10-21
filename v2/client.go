package schemaflux

import (
	"context"
	"log/slog"
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

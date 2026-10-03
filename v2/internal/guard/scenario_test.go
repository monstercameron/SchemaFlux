package guard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A scenario is one way a call goes: it succeeds or fails in a way the fake
// provider or the caller's own arguments can arrange. The tests that run every
// row run them all.
type scenario struct {
	name string
	// script scripts the fake; nil is the row's own successful answer.
	script func(m *schemafluxtest.Provider, r *row, x in)
	// input changes the call's input.
	input func(x *in)
	// ctx derives the call's context from the client's; cancel is called after.
	ctx func(ctx context.Context) (context.Context, context.CancelFunc)
	// nilCtx calls with a nil context.
	nilCtx bool
	// floor overrides the floor of the floor state.
	floor schemaflux.ContractLevel
	// kind is the Kind the error has; zero means any kind but the zero Kind.
	kind schemaflux.Kind
	// sends says that the call reaches the provider, so a refusal before
	// anything is sent is not an answer to it.
	sends bool
	// fromProvider says the provider's own error is in the chain beneath the
	// library's, so a walk of the whole chain would read the provider's text.
	fromProvider bool
}

// vendorText is what a provider says when it fails: the canary is in it, and it
// must reach neither a Record, an error message nor a log line.
const vendorText = "vendor said " + canary

func failWith(err error) func(*schemafluxtest.Provider, *row, in) {
	return func(m *schemafluxtest.Provider, _ *row, _ in) { m.Fail(err) }
}

func providerKind(name string, kind schemaflux.Kind) scenario {
	return scenario{name: name, kind: kind, sends: true, fromProvider: true,
		script: failWith(&schemaflux.Error{Kind: kind, Status: 500, Err: errors.New(vendorText)})}
}

// failureScenarios are the ways a call fails. Each returns an error that is an
// *Error with a non-zero Kind and an Op, and no value beside it.
var failureScenarios = []scenario{
	{name: "nil context", nilCtx: true, kind: schemaflux.ErrInvalidRequest},
	providerKind("provider unavailable", schemaflux.ErrUnavailable),
	providerKind("provider rate limited", schemaflux.ErrRateLimited),
	providerKind("provider unauthorized", schemaflux.ErrUnauthorized),
	providerKind("provider timeout", schemaflux.ErrTimeout),
	providerKind("provider too large", schemaflux.ErrTooLarge),
	providerKind("provider unsupported", schemaflux.ErrUnsupported),
	{name: "provider bare kind", kind: schemaflux.ErrBusy, sends: true, script: failWith(schemaflux.ErrBusy)},
	{name: "provider raw error", sends: true, fromProvider: true, script: failWith(errors.New(vendorText))},
	{name: "malformed answer", kind: schemaflux.ErrInvalidAnswer, sends: true,
		script: func(m *schemafluxtest.Provider, _ *row, _ in) { m.Reply("this is not an answer {{{ " + canary) }},
	{name: "empty answer", kind: schemaflux.ErrInvalidAnswer, sends: true,
		script: func(m *schemafluxtest.Provider, _ *row, _ in) { m.Reply("") }},
	{name: "refusal", kind: schemaflux.ErrRefused, sends: true,
		script: func(m *schemafluxtest.Provider, _ *row, _ in) {
			m.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
				return schemaflux.Response{Model: req.Model, FinishReason: "content_filter"}, nil
			})
		}},
	{name: "cancelled context", kind: schemaflux.ErrCanceled,
		ctx: func(ctx context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx, cancel
		}},
	{name: "deadline passed", kind: schemaflux.ErrTimeout,
		ctx: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadline(ctx, time.Now().Add(-time.Second))
		}},
	{name: "unusable option", kind: schemaflux.ErrConfiguration, input: func(x *in) { x.Tokens = -1 }},
	{name: "floor the operation cannot meet", kind: schemaflux.ErrPolicy, floor: schemaflux.ContractFullyGoverned},
}

// outcome is what one call did.
type outcome struct {
	res any
	err error
	rig *rig
	x   in
}

// floorOf is the floor a row can meet: its own level, or the lowest floor for a
// row whose level is the lowest there is (which refuses under any floor).
func floorOf(r *row) schemaflux.ContractLevel {
	level := r.level
	if r.floorAt != 0 {
		level = r.floorAt
	}
	if level > schemaflux.ContractPromptOnly {
		return level
	}
	return schemaflux.ContractJSONWellFormed
}

// meets says whether the row's call is delivered under the floor of floorOf.
func meets(r *row) bool {
	if r.floorAt != 0 {
		return r.floorAt > schemaflux.ContractPromptOnly
	}
	return r.level > schemaflux.ContractPromptOnly
}

// withFloor returns st held to the floor the row can meet, or to sc's own.
func withFloor(st state, r *row, sc scenario) state {
	if st.floor > schemaflux.ContractPromptOnly {
		st.floor = floorOf(r)
		if sc.floor != 0 {
			st.floor = sc.floor
		}
	}
	return st
}

// run makes the call of a scenario on a row in a state, with the input given.
func run(t testing.TB, r *row, st state, sc scenario, x in) outcome {
	t.Helper()
	if sc.input != nil {
		sc.input(&x)
	}
	g := newRig(t, withFloor(st, r, sc), 0, func(m *schemafluxtest.Provider) {
		if sc.script != nil {
			sc.script(m, r, x)
			return
		}
		r.succeeds(m, x, x.Text != neutral().Text)
	})
	ctx := g.ctx
	if sc.nilCtx {
		ctx = nil
	}
	if sc.ctx != nil {
		var cancel context.CancelFunc
		ctx, cancel = sc.ctx(ctx)
		defer cancel()
	}
	res, err := r.call(ctx, x)
	return outcome{res: res, err: err, rig: g, x: x}
}

// applies says whether a scenario applies to a row, and why not.
func applies(r *row, sc scenario) (bool, string) {
	if why, ok := r.na[sc.name]; ok {
		return false, why
	}
	return true, ""
}

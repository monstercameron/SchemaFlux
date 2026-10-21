package bind

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A Call is one call of an operation function, from Begin to End. It is used by
// value and without defer:
//
//	call, err := bind.Begin(ctx, "classify")
//	if err != nil {
//		return zero, err
//	}
//	...
//	return result, call.End(meta, err)
//
// A Call holds what the call pinned when it began, so that everything the call
// reads about its client it reads from one snapshot, and, when a recorder is
// present, the place where the call collects what it will give each recorder
// at its end. A Call is not safe for concurrent use; a copy of one shares the
// collecting place and ends the same call, which is how Stream hands the end of
// a call to a goroutine of its own.
type Call struct {
	ctx context.Context
	op  string

	// cfg is the snapshot the call runs on, or nil when there is none.
	cfg *ops.ExecConfig

	// start is when the call began. It is set only when the call is not plain:
	// a plain call keeps no record and has no use for the time.
	start time.Time

	// plain is the PLAIN state of the call-path table: no recorder, no observer
	// and no floor. The plain internal function is then called directly and
	// nothing is added to it.
	plain bool

	// state is nil for a plain call and for a call that is only observed or
	// under a floor with no recorder: there is then nothing to deliver at the
	// end. It holds what a call with recorders collects.
	state *callState

	// watch is nil for a call begun with no observer installed. Otherwise it
	// says whether the internal operation opened the call's operation scope, so
	// that a call ended without having run one still gives the observer its
	// scope (End).
	watch *ops.ScopeWatch
}

// callState is what a call with at least one recorder collects while it runs,
// and the recorders it will give the outcome to.
type callState struct {
	// sinks are the recorders of the call and what each asked for, read once
	// when the call began, in the order EachSink gives them.
	sinks []sinkWish

	// bodies reports that some recorder asked for the body of a refused answer,
	// and the largest bound any of them gave: the capture is made once, at the
	// largest, and each recorder is given its own cut of it.
	bodies  bool
	maxBody int

	mu sync.Mutex
	// decisions are the records of the decisions the call made, in the order
	// they were told. Decisions can run at once, so they are guarded.
	decisions []ops.DecisionRecord
	// body is the last refused answer the call captured, cut and scrubbed by
	// the capture to the largest bound.
	body string
	// ended is set by the first End; a second End delivers nothing.
	ended bool
}

// sinkWish is one recorder and what it asked for beyond the record itself.
type sinkWish struct {
	sink      ops.Sink
	decisions bool
	bodies    bool
	maxBody   int
}

// observe is the decision observer of a call with recorders. Decisions run
// inside the call, possibly several at once, and each tells it what was asked
// and who answered.
func (s *callState) observe(_ context.Context, record ops.DecisionRecord) {
	s.mu.Lock()
	s.decisions = append(s.decisions, record)
	s.mu.Unlock()
}

// Capture receives the body of an answer the call refused, already cut to the
// bound and scrubbed of the credential shapes the library recognises. It
// satisfies types.DiagnosticSink, which is the one channel the internal call
// path has for a refused body.
func (s *callState) Capture(record types.DiagnosticRecord) {
	s.mu.Lock()
	s.body = record.Body
	s.mu.Unlock()
}

// Begin starts a call of the operation named op. It refuses a nil context with
// an error of kind invalid request, converted as every error of the call is:
// the context is where the client and the recorders are, and a call with none
// has no meaning. It refuses a context that has ended with the error of kind
// canceled or timeout, and the call is then ended like any refused call: it
// adds its Record and its operation scope. A call that has to record work done
// after its context ended begins on context.WithoutCancel of it.
//
// It pins the client the call runs on (ops.PinExec): a context that carries a
// client is used as it is, and a context that carries none gets the default
// client's snapshot, once, so that a default replaced while the call is in
// flight does not change the client of the call's later requests. That pin is
// one context value, and the only allocation Begin makes on the plain path.
//
// It then settles the state of the call-path table (the call is plain when no
// recorder, no observer and no floor applies to it) and, when recorders are
// present, readies what the call will give them.
func Begin(ctx context.Context, op string) (Call, error) {
	if ctx == nil {
		return Call{}, Refuse(op, types.KindInvalidRequest, "the context is nil")
	}
	ctx, cfg := ops.PinExec(ctx)
	c := Call{ctx: ctx, op: op, cfg: cfg}
	hasSink := ops.HasSink(ctx, cfg)
	c.plain = !hasSink && !ops.Observing() && !underFloor(ctx, cfg)
	if !c.plain {
		c.start = time.Now()
		c.ctx, c.watch = ops.WatchScope(c.ctx)
		if hasSink {
			if err := c.ready(); err != nil {
				// A recorder whose settings no call can use does not take the call:
				// the call ends here with the configuration error, and the recorders
				// that are fine are given their record of it.
				return Call{}, c.End(nil, err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		// A context that has ended refuses the call before anything is done, as
		// every operation function must: one whose work is local (a comparison, a
		// parse, a rule that Go decides) never reaches a request that would find
		// the context ended. The call ends as any refused call does, so a recorder
		// and an observer see it.
		kind := types.KindCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			kind = types.KindTimeout
		}
		return Call{}, c.End(nil, &types.OperationError{Kind: kind, Op: op, Message: "the context had already ended, so nothing was run", Cause: err})
	}
	return c, nil
}

// underFloor reports that the call is held to a floor: the client's data policy
// has a minimum contract above the lowest level, or a policy attached to the
// context with ops.WithContractPolicy does. Such a call takes the recorded path
// whether or not a recorder is present, so that the floor is checked against
// what the operation establishes before anything is sent.
//
// The floor on the context is read from that policy alone (ops.AttachedFloor):
// one lookup on the context, which allocates nothing for a call that carries no
// policy. The client's policy is read from the snapshot, which holds the floor
// beside its lists, and is never copied here.
func underFloor(ctx context.Context, cfg *ops.ExecConfig) bool {
	if cfg != nil && cfg.DataPolicyDeclared && cfg.DataPolicy.MinimumContract > types.ContractPromptOnly {
		return true
	}
	floor, attached := ops.AttachedFloor(ctx)
	return attached && floor > types.ContractPromptOnly
}

// ready collects what each recorder asked for and puts on the call's context
// the two things that let the call hand them an outcome: the decision observer,
// so that who answered a decision reaches the outcome, and, when some recorder
// wants bodies, the diagnostic capture that holds a refused answer.
//
// A recorder that says its settings cannot be used (a Check method that returns
// an error) is left out of what the call delivers to, and the first such error
// is returned: the call is refused with it, and the recorder is given no record.
func (c *Call) ready() error {
	state := &callState{}
	var refused error
	ops.EachSink(c.ctx, c.cfg, func(s ops.Sink) {
		if checker, ok := s.(interface{ Check() error }); ok {
			if err := checker.Check(); err != nil {
				if refused == nil {
					refused = err
				}
				return
			}
		}
		decisions, bodies, maxBody := s.Wants()
		state.sinks = append(state.sinks, sinkWish{sink: s, decisions: decisions, bodies: bodies, maxBody: maxBody})
		if bodies {
			state.bodies = true
			state.maxBody = max(state.maxBody, maxBody)
		}
	})
	c.ctx = ops.WithDecisionObserver(c.ctx, state.observe)
	if state.bodies {
		c.ctx = ops.WithDiagnosticSink(c.ctx, state, types.DiagnosticPolicy{MaxBodyBytes: state.maxBody})
	}
	c.state = state
	return refused
}

// Context returns the context the internal call is made on. It carries the
// pinned client and, for a call with recorders, what collects the call's
// decisions and refused body. An operation function builds its internal options
// from it.
func (c *Call) Context() context.Context { return c.ctx }

// Form says that this call is a form of the internal operation named internal,
// which an operation function that runs the internal twin of another name
// (IsDetail runs "is") calls once after Begin, before it builds its internal
// options from Context. The operation scope the call opens, and the requests
// under it, are then named as the call is: the operation Begin was given, which
// is also the operation of the call's record. An operation the internal twin
// runs under another name keeps it.
//
// It does nothing for a call that no observer sees: there is no scope to name,
// and the plain path allocates nothing it did not before.
func (c *Call) Form(internal string) {
	if c.plain || !ops.Observing() {
		return
	}
	c.ctx = ops.WithCallerName(c.ctx, c.op, internal)
}

// ScopeIsAnothers says that the operation scope an observer sees for this call
// is opened and ended by another call, so that End gives the recorders their
// record and opens no scope of its own. A staged run uses it for the call it
// ends for each of its stages: the run is the one scope, and each stage is a
// record under it.
func (c *Call) ScopeIsAnothers() { c.watch = nil }

// Plain reports the PLAIN state: no recorder on the context or the client, no
// observer installed, and no floor on the client. A plain call calls the plain
// internal function directly and adds nothing to it.
func (c *Call) Plain() bool { return c.plain }

// End finishes the call: it returns err converted to the root's error (nil for
// a nil err) and, when recorders are present, gives each of them the outcome of
// the call, once. meta is the record of how the call ran when the call reached
// the internal function that reports one, and nil for a call that failed before
// that (a refusal of an argument, a bad option) and for a plain call, which
// reports nothing; a nil meta still gives each recorder its record, with the
// operation and the error and nothing the call did not do.
//
// Each recorder is given an outcome of its own: the meta copied, so that no
// recorder can change what another reads, the questions and answers of the
// call's decisions only when it asked for decisions, and the body of a refused
// answer only when it asked for bodies, cut to its own bound. Who answered the
// decisions of the call is in every outcome. The operation of every outcome is
// the one this call was begun with, and its elapsed time is the whole call.
//
// A call that is ended without its internal operation having run (an argument
// or an option that no call can use) has opened no operation scope, and an
// observer has to see every call a recorder sees: End then opens and ends the
// scope of the call itself, with the kind of the error. A call whose internal
// operation ran has its scope from that operation, and End adds none. A call
// begun with no observer installed does neither, and costs nothing for it.
//
// With no recorder and a nil err, End does nothing and allocates nothing.
// Calling End a second time converts the error again and gives the recorders
// and the observer nothing more.
func (c *Call) End(meta *types.Meta, err error) error {
	converted := ConvertError(c.op, err)
	c.watch.EndScope(c.ctx, c.op, err)
	state := c.state
	if state == nil {
		return converted
	}
	state.mu.Lock()
	if state.ended {
		state.mu.Unlock()
		return converted
	}
	state.ended = true
	decisions := state.decisions
	body := state.body
	state.mu.Unlock()

	var base types.Meta
	if meta != nil {
		base = *meta
	}
	base.Operation = c.op
	base.Elapsed = time.Since(c.start)

	for _, wish := range state.sinks {
		outcome := &ops.Outcome{Meta: cloneMeta(base), Start: c.start, Err: converted}
		for _, record := range decisions {
			outcome.NoteDecision(record, wish.decisions)
		}
		if wish.bodies {
			outcome.Body = CutBody(body, wish.maxBody)
		}
		wish.sink.Add(c.ctx, outcome)
	}
	return converted
}

// Refuse ends the call with an error of the kind and message given, for a call
// that is refused before anything is sent: an unusable argument (the invalid
// request kind) or an option no call can use (the configuration kind). It is
// End(nil, types.Newf(kind, op, format, args...)), so the refusal is converted
// once, is the call's error, and still gives each recorder its record with no
// meta. The message follows the rules of the package-level Refuse.
func (c *Call) Refuse(kind types.ErrorKind, format string, args ...any) error {
	return c.End(nil, types.Newf(kind, c.op, format, args...))
}

// Recorded runs run, the PLAIN internal function with its options built from
// the context it is given, under the recorded-call entry (ops.Recorded) at the
// level the answer is delivered at, and ends the call with what the entry
// reports. A level below the client's floor is refused before run is called and
// before anything is sent, with the error of the policy kind. run builds its
// options from the context it is given: options built from the call's own
// context carry no log of the requests, and what the call cost is lost from the
// record.
//
// The closure run is the one allocation this adds to the entry. An operation
// function declares it in its recorded branch, so that the plain path
// allocates none.
func Recorded[T any](c *Call, level types.ContractLevel, run func(ctx context.Context) (T, error)) (T, error) {
	result, err := ops.Recorded(c.ctx, c.op, level, run)
	meta := result.Meta
	return result.Value, c.End(&meta, err)
}

// Stream opens a stream under the recorded-stream entry (ops.RecordedStream)
// and ends the call when the stream ends: read to the end, left by its reader,
// cancelled, failed, or abandoned until its context ends. open builds the
// stream's options from the context it is given. A level below the client's
// floor, or an error opening the stream, ends the call at once with that error
// and returns no stream.
//
// The end of the call is made from the goroutine that produces the stream,
// before the stream's last item reaches a reader, so the recorders have their
// record by the time the reader has the whole answer.
func Stream(c *Call, level types.ContractLevel, open func(ctx context.Context) (*ops.TextStream, error)) (*ops.TextStream, error) {
	// The end of the call outlives this function, so it holds a copy of the
	// call and not the caller's, which stays on the caller's stack. A copy
	// ends the same call: it shares the call's collecting place.
	ended := *c
	stream, err := ops.RecordedStream(c.ctx, c.op, level, open, func(meta types.Meta, err error) {
		ended.End(&meta, err)
	})
	if err != nil {
		return nil, c.End(&types.Meta{Operation: c.op, RequestedContract: level}, err)
	}
	return stream, nil
}

// Refuse builds the error of a call refused before anything was sent, already
// converted: the kind, the operation, and a message the library wrote. The
// message is a format and arguments, which must never be given the caller's
// data, a model's text or a provider's: the error goes wherever the caller's
// logs go.
func Refuse(op string, kind types.ErrorKind, format string, args ...any) error {
	return ConvertError(op, types.Newf(kind, op, format, args...))
}

// cloneMeta returns a copy of m that shares no slice or map with it, so that
// each recorder's outcome is its own.
func cloneMeta(m types.Meta) types.Meta {
	m.Checks = slices.Clone(m.Checks)
	m.ModelClaims = maps.Clone(m.ModelClaims)
	m.Provenance.ParentResultIDs = slices.Clone(m.Provenance.ParentResultIDs)
	return m
}

// CutBody returns body cut to at most max bytes, at the start of a character,
// so that the cut does not leave half of one. A bound of zero or less keeps
// nothing. It is the one cut the root and the call helper make of a refused
// answer.
func CutBody(body string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(body) <= max {
		return body
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut]
}

package mw

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// HedgeEvent says what Hedge did with one request.
type HedgeEvent struct {
	// Hedged is true when the request had not answered within the delay and a
	// second copy of it was sent.
	Hedged bool

	// SecondWon is true when the answer that was used came from the second
	// copy.
	SecondWon bool
}

// HedgeOption configures Hedge.
type HedgeOption func(*hedgeConfig)

type hedgeConfig struct {
	observe func(HedgeEvent)
}

// WithHedgeObserver is told what Hedge did with each request, once the
// request is over. It is how a caller counts how often the second copy was
// needed, and how often it won.
//
// It is called on the goroutine of the request, before the request returns, and
// requests run at the same time, so it is called from several goroutines at
// once and must be safe for that. It must not block: whatever time it takes is
// added to the request's. A panic in it is recovered and dropped, and does not
// change the request's answer.
func WithHedgeObserver(observe func(HedgeEvent)) HedgeOption {
	return func(cfg *hedgeConfig) { cfg.observe = observe }
}

// Hedge sends a second copy of a request that has not answered within after,
// and uses whichever of the two answers first, cancelling the other.
//
// It is for the slow tail. A model that usually answers in a second will now
// and then take ten or twenty, for reasons that have nothing to do with the
// request, and a second copy sent to the same place usually comes back in the
// usual second. Set after a little above what a normal answer takes, twice the
// median say: then nine requests in ten are sent once, and the tenth is over
// in after plus a normal answer.
//
// What it costs: the second copy is a second request, and the provider bills
// for whatever a cancelled request had generated before it was cancelled. A
// request the provider stores (ProviderConfig.Store) is stored twice. This
// library records the cost of the answer that was used, the one response Hedge
// returns; the abandoned copy's share is not on its ledger, so a client's
// budget does not see it. A request is sent a second time only for being slow:
// one that fails before the delay is returned as it is, since trying a failure
// again is what a retry is for. After the second copy has gone out, the first
// of the two to succeed is the answer, and when both fail the first copy's
// error is returned. If the caller's context ends while a copy has answered
// already, that answer is returned, and while a copy has failed already, that
// copy's error is returned and not the context's. This holds whether the
// context ends before the second copy is sent or after.
//
// Every other middleware of this package waits for the provider, and a provider
// that ignores its context holds the call past its deadline. Hedge is the one
// that returns when the caller's context ends while a copy is still running, so
// the copy that is left outlives the call: it ends when the provider honours
// the cancellation Hedge gives it, and not before. A provider that does not
// honour its context leaves a goroutine behind for as long as it blocks, for each
// call that was given up on, and no layer can stop it.
//
// A copy that panics is an error of the kind a step that panicked is, with no
// value of the panic in it, and the stack is logged. Each copy runs on a
// goroutine of its own, where a panic could not be recovered by the caller.
//
// Where it stands in a chain. Chain lists the outermost middleware first. A
// cache goes before Hedge, outside it, so that a hit is answered without
// reaching it and is not hedged. A rate limiter or a bulkhead that has to count
// both copies goes after Hedge, inside it, where it sees each copy as a
// request; listed before Hedge it sees one request however many copies go out.
// The same holds for the scheduler slot a client holds for a call: it is held
// once, while two requests may be in flight. Hedge inside a retry hedges each
// attempt, so a request can send up to twice as many requests as the retry has
// attempts.
//
// The timing of an answer is that of the copy that won, from the start of that
// copy. When the second copy wins, the delay before it was sent is not in
// Timing.Network, and a call's elapsed time less its network time books that
// delay as this library's own.
//
// The response of a request that went out twice has Hedged set, whichever copy
// answered, so that the call's record can say it may have been billed twice. A
// Hedge outside a Cache sets it too, though the cache joins the two copies into
// one request to the provider: the mark says that a second copy was sent, and
// not that it was billed.
//
// A delay of zero or less sends every request once.
func Hedge(after time.Duration, opts ...HedgeOption) Middleware {
	cfg := hedgeConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return func(next Handler) Handler {
		return &hedged{next: next, after: after, cfg: cfg}
	}
}

type hedged struct {
	next  Handler
	after time.Duration
	cfg   hedgeConfig
}

// hedgeOutcome is how one copy of a request ended.
type hedgeOutcome struct {
	resp   llm.CompletionResponse
	err    error
	second bool
}

// hedgePanicked is the error a copy that panicked is reported as, the same as
// a step of a flow that panicked (internal/ops, stepPanicked): an invariant
// that did not hold, which nothing should retry as if it were the weather. The
// panic's own value can hold anything, the caller's data included, and is not
// in it.
func hedgePanicked() error {
	panicked := types.Newf(types.KindInvariantViolation, "hedge", "a request panicked")
	panicked.Narrow = types.NarrowPanic
	return panicked
}

// hedgeMarked says on resp that a second copy was sent, when one was. It never
// clears the flag: a response that already says so keeps saying so.
func hedgeMarked(resp llm.CompletionResponse, hedged bool) llm.CompletionResponse {
	if hedged {
		resp.Hedged = true
	}
	return resp
}

// guarded runs one copy and turns a panic in it into an error.
func (h *hedged) guarded(ctx context.Context, req llm.CompletionRequest) (resp llm.CompletionResponse, err error) {
	defer func() {
		if thrown := recover(); thrown != nil {
			logPanic(ctx, "A hedged request panicked", thrown)
			resp, err = llm.CompletionResponse{}, hedgePanicked()
		}
	}()
	return h.next.Complete(ctx, req)
}

func (h *hedged) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if h.after <= 0 {
		return h.next.Complete(ctx, req)
	}

	// Both copies run under a context of their own, so that the one that
	// loses can be stopped without touching the caller's.
	run, stop := context.WithCancel(ctx)
	defer stop()

	// Room for both, so that the copy that loses can report and return when
	// nobody is listening any longer.
	outcomes := make(chan hedgeOutcome, 2)
	send := func(second bool) {
		go func() {
			resp, err := h.guarded(run, req)
			outcomes <- hedgeOutcome{resp: resp, err: err, second: second}
		}()
	}

	event := HedgeEvent{}
	report := func() {
		if h.cfg.observe == nil {
			return
		}
		// The observer is the caller's, and runs on this goroutine: it does not
		// get to take the answer down with it.
		defer func() {
			if thrown := recover(); thrown != nil {
				logPanic(ctx, "A hedge observer panicked", thrown)
			}
		}()
		h.cfg.observe(event)
	}

	var failed hedgeFailures
	// endedByContext is what the request returns once the caller's context is
	// over.
	endedByContext := func() (llm.CompletionResponse, error) {
		answer, answered, err := hedgeDrain(ctx, outcomes, &failed)
		if answered {
			event.SecondWon = answer.second
			report()
			return hedgeMarked(answer.resp, event.Hedged), nil
		}
		report()
		return llm.CompletionResponse{}, err
	}

	send(false)
	timer := time.NewTimer(h.after)
	defer timer.Stop()

	select {
	case first := <-outcomes:
		// Answered, or failed, within the delay: one request, as it came.
		report()
		if first.err != nil {
			return llm.CompletionResponse{}, first.err
		}
		return first.resp, nil
	case <-ctx.Done():
		return endedByContext()
	case <-timer.C:
	}

	// When the caller's context is over and the timer has fired too, the select
	// above takes either at random, and a second copy would go out on a context
	// that is dead.
	if ctx.Err() != nil {
		return endedByContext()
	}

	event.Hedged = true
	// The second copy, to whoever observes the request this is part of: a
	// tracing observer records it on the request's span, at the moment it is
	// sent. With no observer this does nothing.
	telemetry.ObserveOperationNote(ctx, telemetry.Note{Name: telemetry.NoteHedge, Attempt: 2})
	send(true)

	for waiting := 2; waiting > 0; waiting-- {
		select {
		case outcome := <-outcomes:
			if outcome.err == nil {
				event.SecondWon = outcome.second
				report()
				return hedgeMarked(outcome.resp, event.Hedged), nil
			}
			failed.note(outcome)
		case <-ctx.Done():
			return endedByContext()
		}
	}
	report()
	return llm.CompletionResponse{}, failed.err()
}

// hedgeFailures remembers how the copies that have failed so far failed.
type hedgeFailures struct {
	first, other error
}

// note records a failed outcome.
func (f *hedgeFailures) note(outcome hedgeOutcome) {
	if outcome.second {
		f.other = outcome.err
		return
	}
	f.first = outcome.err
}

// err is the error to return when there is no answer: the first copy's, or else
// the other's.
func (f *hedgeFailures) err() error {
	if f.first != nil {
		return f.first
	}
	return f.other
}

// hedgeDrain settles a request whose caller's context is over. An outcome that
// was delivered as the context ended is not lost to the order a select looked
// in: an answer that is waiting is the answer, and a copy that had failed
// already is why this failed, where a bare cancellation would hide it. A rate
// limit from the first copy and a deadline were reported as a timeout, which is
// classified and retried differently. Outcomes are taken without blocking, so a
// copy that is still running is not waited for. With no answer and no failure
// the error is the context's.
func hedgeDrain(ctx context.Context, outcomes <-chan hedgeOutcome, failed *hedgeFailures) (answer hedgeOutcome, answered bool, err error) {
	for {
		select {
		case outcome := <-outcomes:
			if outcome.err == nil {
				return outcome, true, nil
			}
			failed.note(outcome)
		default:
			if err := failed.err(); err != nil {
				return hedgeOutcome{}, false, err
			}
			return hedgeOutcome{}, false, ctx.Err()
		}
	}
}

// Unwrap is the wrapped provider.
func (h *hedged) Unwrap() Handler { return h.next }

// Name, EstimateCost and RetryPolicy are the wrapped provider's.
func (h *hedged) Name() string { return h.next.Name() }

func (h *hedged) EstimateCost(req llm.CompletionRequest) float64 { return h.next.EstimateCost(req) }

func (h *hedged) RetryPolicy() (int, time.Duration) { return h.next.RetryPolicy() }

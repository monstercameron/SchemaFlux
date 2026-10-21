package llm

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LAT-001. Where the time of one HTTP call to a provider went, from the
// transport's own events. types.Timing says what each phase is.

// callClock records the transport's events for one HTTP call.
//
// It describes the attempt that produced the response. A transport that finds
// its pooled connection dead redials and writes the request again, and a
// redirect sends a second request through the same trace, so the events of one
// call can run twice: each new attempt starts the phases again, and the whole
// of the call, Network, still runs from the start.
type callClock struct {
	mu sync.Mutex

	// now is the clock, time.Now when it is nil. A test sets it to drive the
	// events in an order of its own.
	now   func() time.Time
	start time.Time

	lookupStart, lookupDone       time.Time
	connectStart, connectDone     time.Time
	handshakeStart, handshakeDone time.Time
	gotConn                       time.Time
	wrote                         time.Time
	firstByte                     time.Time
	reused                        bool
}

func (clock *callClock) moment() time.Time {
	if clock.now != nil {
		return clock.now()
	}
	return time.Now()
}

// restart forgets the phases of an attempt that is being replaced by another.
// The caller holds the lock.
func (clock *callClock) restart() {
	clock.lookupStart, clock.lookupDone = time.Time{}, time.Time{}
	clock.connectStart, clock.connectDone = time.Time{}, time.Time{}
	clock.handshakeStart, clock.handshakeDone = time.Time{}, time.Time{}
	clock.gotConn, clock.wrote, clock.firstByte = time.Time{}, time.Time{}, time.Time{}
	clock.reused = false
}

// first notes an event at the time it fired, keeping the first time within an
// attempt: a connection that is dialled to more than one address fires its
// start more than once, and the wait runs from the first.
func (clock *callClock) first(when *time.Time) {
	clock.mu.Lock()
	if when.IsZero() {
		*when = clock.moment()
	}
	clock.mu.Unlock()
}

// finished notes the end of a step that can fail, only when it did not. A dial
// that failed and was followed by one that worked ends at the one that worked,
// and a write that failed is not the request having been written.
func (clock *callClock) finished(when *time.Time, err error) {
	if err != nil {
		return
	}
	clock.first(when)
}

// trace is the set of callbacks the transport calls. They run under the lock,
// since a transport fires them from its own goroutines, and one that fires
// after the call has returned changes nothing that was read.
func (clock *callClock) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		// A new request for a connection after one was already obtained is a
		// second attempt: the first one's write failed and the transport is
		// redialling, or a redirect is being followed.
		GetConn: func(string) {
			clock.mu.Lock()
			if !clock.gotConn.IsZero() {
				clock.restart()
			}
			clock.mu.Unlock()
		},
		DNSStart:          func(httptrace.DNSStartInfo) { clock.first(&clock.lookupStart) },
		DNSDone:           func(info httptrace.DNSDoneInfo) { clock.finished(&clock.lookupDone, info.Err) },
		ConnectStart:      func(string, string) { clock.first(&clock.connectStart) },
		ConnectDone:       func(_, _ string, err error) { clock.finished(&clock.connectDone, err) },
		TLSHandshakeStart: func() { clock.first(&clock.handshakeStart) },
		TLSHandshakeDone:  func(_ tls.ConnectionState, err error) { clock.finished(&clock.handshakeDone, err) },
		GotConn: func(info httptrace.GotConnInfo) {
			clock.mu.Lock()
			defer clock.mu.Unlock()
			if !clock.gotConn.IsZero() {
				// A connection again with no new request for one in between, from
				// a trace that does not fire GetConn: what was written to the
				// first is not this one's.
				clock.wrote, clock.firstByte = time.Time{}, time.Time{}
			}
			clock.gotConn = clock.moment()
			clock.reused = info.Reused
		},
		WroteRequest:         func(info httptrace.WroteRequestInfo) { clock.finished(&clock.wrote, info.Err) },
		GotFirstResponseByte: func() { clock.first(&clock.firstByte) },
	}
}

// withCallClock returns a context under which an HTTP call's events are
// recorded, and the clock that records them. A trace already on the context,
// a caller's own, keeps being called: httptrace composes them.
func withCallClock(ctx context.Context) (context.Context, *callClock) {
	clock := &callClock{}
	clock.start = clock.moment()
	return httptrace.WithClientTrace(ctx, clock.trace()), clock
}

// timing works the phases out from the events, as of now: the caller has read
// and decoded the body. An event that never fired leaves its phase at zero.
func (clock *callClock) timing() types.Timing {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	end := clock.moment()

	between := func(from, to time.Time) time.Duration {
		if from.IsZero() || to.IsZero() || to.Before(from) {
			return 0
		}
		return to.Sub(from)
	}
	timing := types.Timing{
		Calls:     1,
		Reused:    clock.reused,
		Lookup:    between(clock.lookupStart, clock.lookupDone),
		Connect:   between(clock.connectStart, clock.connectDone),
		Handshake: between(clock.handshakeStart, clock.handshakeDone),
		Send:      between(clock.gotConn, clock.wrote),
		Wait:      between(clock.wrote, clock.firstByte),
		Read:      between(clock.firstByte, end),
		Network:   end.Sub(clock.start),
	}
	if clock.reused {
		// A dial that lost the race to a connection that was idle already still
		// fires its events, and its time was nobody's: the request went out on
		// a connection somebody else had paid for.
		timing.Lookup, timing.Connect, timing.Handshake = 0, 0, 0
	}
	return timing
}

// doTimed sends a request and returns the response with the clock that is
// timing it. The caller reads and decodes the body and then asks the clock for
// the timing, so that Read covers the decoding.
func doTimed(client *http.Client, req *http.Request) (*http.Response, *callClock, error) {
	ctx, clock := withCallClock(req.Context())
	resp, err := client.Do(req.WithContext(ctx))
	return resp, clock, err
}

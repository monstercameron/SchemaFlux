package types

import "time"

// Timing says where the time of a call to a model went (LAT-001).
//
// A call to a model is a wait, and nearly all of it is the provider thinking.
// The rest is waiting for a slot in the client's scheduler, the name lookup,
// the connection, the handshake, and the reading of the body, and those are
// the parts a client can do something about. Each is measured, from the
// scheduler and from the transport's own events, so that the part being worked
// on is the part that is slow.
//
// For one call the phases are in the order they happen and do not overlap.
// An answer that took more than one call, a repaired one or a batch, carries
// the sum of them (Calls says how many), and a sum is not a duration on the
// clock: calls that ran at the same time add up to more than the time that
// passed, and a call that was retried inside the provider's own loop is in the
// sum only by the attempt that was accepted, since a failed attempt returns no
// timing.
type Timing struct {
	// Calls is how many timed calls this covers: one for a single call, and
	// the sum for a Timing that was added up. It is what says whether Network
	// can be set beside the call's Elapsed (Outside), which is only so for one.
	// It is zero for a Timing of nothing but a wait for a slot, and for one
	// built by hand.
	Calls int

	// Reused reports that the request went out on a connection that was
	// already open: Lookup, Connect and Handshake are then zero, having been
	// paid by an earlier call or by a warm-up. With several calls it is true
	// only when every one that was timed reused a connection.
	Reused bool

	// Queued is the wait for a slot in the client's scheduler.
	Queued time.Duration
	// Lookup is the name lookup.
	Lookup time.Duration
	// Connect is opening the connection.
	Connect time.Duration
	// Handshake is the TLS handshake.
	Handshake time.Duration
	// Send is from having a connection to having written the request.
	Send time.Duration
	// Wait is from the request being written to the first byte of the
	// response: the provider's own time, and the network's both ways.
	Wait time.Duration
	// Read is from the first byte of the response to the body having been
	// read and decoded.
	Read time.Duration

	// Network is the whole of the HTTP calls, start to finish. For one call it
	// is a little more than the sum of the phases from Lookup to Read, the
	// difference being time this process spent between them. Summed over
	// several calls it is the sum of their times, which is more than the time
	// that passed when they overlapped.
	Network time.Duration
}

// ranACall reports whether the timing holds a call that was timed, as opposed
// to nothing at all or only the wait for a slot.
func (t Timing) ranACall() bool {
	return t.Calls > 0 || t.Network > 0
}

// Add returns the sum of two timings, and Calls the sum of their counts.
//
// Reused is true only when every call that was timed reused a connection. A
// timing of nothing but the wait for a slot has no connection to report and
// does not vote: it was counted as one that opened a connection, so a call that
// waited in the scheduler could never be said to have reused anything.
func (t Timing) Add(other Timing) Timing {
	var reused bool
	switch {
	case t.ranACall() && !other.ranACall():
		reused = t.Reused
	case !t.ranACall() && other.ranACall():
		reused = other.Reused
	default:
		reused = t.Reused && other.Reused
	}
	return Timing{
		Calls:     t.Calls + other.Calls,
		Reused:    reused,
		Queued:    t.Queued + other.Queued,
		Lookup:    t.Lookup + other.Lookup,
		Connect:   t.Connect + other.Connect,
		Handshake: t.Handshake + other.Handshake,
		Send:      t.Send + other.Send,
		Wait:      t.Wait + other.Wait,
		Read:      t.Read + other.Read,
		Network:   t.Network + other.Network,
	}
}

// Measured reports whether anything was timed. A provider that does not time
// its calls, a test double say, leaves a Timing at its zero value, and zero is
// not a measurement of an instant call.
func (t Timing) Measured() bool {
	return t.Network > 0 || t.Queued > 0
}

// Setup is what a call spent getting a connection: the lookup, the connect and
// the handshake. It is what a warm connection saves.
func (t Timing) Setup() time.Duration {
	return t.Lookup + t.Connect + t.Handshake
}

// Outside is what a call that took elapsed in all spent outside the network
// and the queue: building the request, checking the answer, and this
// library's own bookkeeping. It is never below zero.
//
// It is meaningful for a single call, which is what Calls of one or none says.
// For a Timing of more than one call it is zero: the network time is the sum
// of the calls' and the elapsed time is not (a repair's is the last call's, a
// batch's concurrent chunks overlap), so the difference is neither this
// library's own time nor anything else, and a zero that hid a negative one
// would pass for "nothing was spent here". A Timing built by hand has no count
// and is taken as one call.
//
// A call that was retried inside the library's loop is still one call: its
// Elapsed covers the failed attempts and the pauses between them, which Network
// does not, so Outside counts them as outside the network.
func (t Timing) Outside(elapsed time.Duration) time.Duration {
	if t.Calls > 1 {
		return 0
	}
	outside := elapsed - t.Network - t.Queued
	if outside < 0 {
		return 0
	}
	return outside
}

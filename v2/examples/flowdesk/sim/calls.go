package sim

import (
	"sync"
	"time"
)

// Call is one call to a provider: which operation made it, and when it started
// and ended. End is the zero time while the call is still in flight.
type Call struct {
	Operation string
	Start     time.Time
	End       time.Time
	// Wait is the number of the wait the call belongs to, counted from 1: see
	// Log.Waits.
	Wait int
}

// Duration is how long the call took, or zero while it is in flight.
func (c Call) Duration() time.Duration {
	if c.End.IsZero() {
		return 0
	}
	return c.End.Sub(c.Start)
}

// Log is a record of the calls made to a provider. The simulator keeps one,
// and the benchmark wraps one round a real provider, so both report the same
// figures by the same arithmetic.
//
// The zero value is ready to use, and it is safe for concurrent use: a fused
// flow makes several calls at once.
type Log struct {
	mu    sync.Mutex
	calls []Call

	inFlight    int
	maxInFlight int
	// waits counts the times a call started with no other call in flight.
	waits int
	// busy is the time during which at least one call was in flight, and
	// busySince is when the current such stretch began.
	busy      time.Duration
	busySince time.Time
}

// Begin records the start of a call and returns the function that records its
// end. The end function is to be called once.
func (l *Log) Begin(operation string) (end func()) {
	l.mu.Lock()
	index := len(l.calls)
	started := time.Now()
	if l.inFlight == 0 {
		l.waits++
		l.busySince = started
	}
	l.calls = append(l.calls, Call{Operation: operation, Start: started, Wait: l.waits})
	l.inFlight++
	if l.inFlight > l.maxInFlight {
		l.maxInFlight = l.inFlight
	}
	l.mu.Unlock()

	return func() {
		l.mu.Lock()
		ended := time.Now()
		l.calls[index].End = ended
		l.inFlight--
		if l.inFlight == 0 {
			l.busy += ended.Sub(l.busySince)
		}
		l.mu.Unlock()
	}
}

// Count is how many calls were started.
func (l *Log) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

// MaxInFlight is the largest number of calls that were in flight at once.
func (l *Log) MaxInFlight() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxInFlight
}

// Waits is how many times the program waited for the provider: the number of
// times a call started while no other call was in flight. Calls that overlap
// are one wait, and calls made one after another are one each.
//
// It is counted from the order of events and not from the clock, so it is the
// same from run to run as long as the calls of one group do overlap, which
// they do when a call takes longer than it takes to start the next one. With a
// delay of a millisecond or less on a busy machine they did not always, and a
// group of three calls was counted as three waits.
//
// It is the length of the program's critical path, in calls, when everything
// made at once is a single call each, and it is a proxy for the time spent
// waiting and not a measure of it. It misleads in these cases:
//
//   - A group that ran a chain of two calls beside a single one counts as one
//     wait and takes two.
//   - A client that limits how many calls are in flight at once to fewer than
//     a group has holds part of the group back until another call of it ends.
//     The call that starts as another ends is still in a wait that has one in
//     flight, so it joins that wait, and a group that took two calls' time is
//     counted as one wait. With a limit of one the calls do not overlap and
//     each is a wait of its own, which is right.
//   - Every wait counts one however long it is. A wait for the call that
//     carries ten questions counts as a wait for one that carries one.
//     CriticalPath is the same count in time.
//   - A retry is another call, counted as one when it starts after the call it
//     replaces has failed, and its backoff is time with nothing in flight. A
//     run with retries has more waits than the program has steps.
func (l *Log) Waits() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waits
}

// CriticalPath is the time the program spent waiting for the provider counted
// the way Waits counts it: for every wait, the duration of its longest call,
// summed. A call still in flight is counted up to now. Where Busy is the time
// at least one call was in flight, this is the sum of the longest call of each
// group of calls, which is the same figure when the groups do not overlap and
// the calls of a group start together.
func (l *Log) CriticalPath() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	longest := make(map[int]time.Duration, l.waits)
	for _, call := range l.calls {
		ended := call.End
		if ended.IsZero() {
			ended = now
		}
		if took := ended.Sub(call.Start); took > longest[call.Wait] {
			longest[call.Wait] = took
		}
	}
	var path time.Duration
	for _, took := range longest {
		path += took
	}
	return path
}

// Busy is the time during which at least one call was in flight. Overlapping
// calls are counted once, so it is the part of a run's wall time that was
// spent waiting for the provider.
func (l *Log) Busy() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	busy := l.busy
	if l.inFlight > 0 {
		busy += time.Since(l.busySince)
	}
	return busy
}

// Calls returns the calls in the order they started.
func (l *Log) Calls() []Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Call(nil), l.calls...)
}

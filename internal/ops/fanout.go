package ops

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// CA-005. A provider's prompt cache is written by the first request that sends
// a given prefix, and it is readable only once that request has begun
// producing a response. So a fan-out that launches every worker at once has
// every worker miss: they all arrive before any of them has written, each pays
// the full uncached price, and the cache the fan-out was supposed to exploit
// is written N times instead of once.
//
// The fix is ordering, not concurrency control: send one request, wait until
// it has actually started producing, then release the rest. The gate is the
// first *token*, not the finished answer — waiting for completion would
// serialise the whole fan-out and cost more in latency than it saves in
// tokens.

// FanOutGate coordinates one round of that ordering.
//
// It is a primitive rather than a policy: it does not decide how many workers
// to run or what they do, only that the first one gets a head start and the
// rest wait for it.
type FanOutGate struct {
	released    chan struct{}
	releaseOnce sync.Once

	mu          sync.Mutex
	leadErr     error
	claimed     bool
	leaderIndex int
}

// NewFanOutGate returns a gate with no leader yet.
func NewFanOutGate() *FanOutGate {
	return &FanOutGate{released: make(chan struct{}), leaderIndex: -1}
}

// Claim reports whether the worker at this index is the leader. Exactly one
// caller is told yes.
//
// The index is taken rather than inferred so FanOut can tell which worker led,
// and release the gate when that worker returns. Without it, a leader that
// forgets to release strands every follower, and the obvious backstop --
// release once all the work is done -- deadlocks, because "all the work"
// includes the followers that are blocked waiting for the release.
func (g *FanOutGate) Claim(index int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.claimed {
		return false
	}
	g.claimed = true
	g.leaderIndex = index
	return true
}

// workerFinished releases the gate if the worker that just returned was the
// leader, whether or not it remembered to release for itself.
func (g *FanOutGate) workerFinished(index int) {
	g.mu.Lock()
	isLeader := g.claimed && g.leaderIndex == index
	g.mu.Unlock()

	if isLeader {
		g.Released()
	}
}

// noLeaderClaimed reports that nobody took the lead, which would leave every
// follower waiting on a release that is never coming.
func (g *FanOutGate) noLeaderClaimed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.claimed
}

// Released is what the leader calls once its request has begun producing —
// after the first token, not after the answer. Everything waiting is let go.
//
// Calling it more than once is harmless: a leader that reports its first token
// and then finishes should not have to track which of the two released the
// gate.
func (g *FanOutGate) Released() {
	g.releaseOnce.Do(func() { close(g.released) })
}

// Failed releases the gate too, recording why.
//
// A leader that failed must still let the others go. The alternative — holding
// them until a timeout — turns one provider error into N stalled workers, and
// the whole point of the gate is that the followers are perfectly capable of
// making their own uncached calls if the head start did not happen.
func (g *FanOutGate) Failed(err error) {
	g.mu.Lock()
	if g.leadErr == nil {
		g.leadErr = err
	}
	g.mu.Unlock()

	g.Released()
}

// Wait blocks until the leader has been released, the context is done, or the
// gate was never claimed by anyone.
//
// It returns the leader's failure, if there was one, as information rather
// than as this caller's error: a follower whose leader failed is not itself
// failed, and returning the leader's error would make N callers report a
// failure that happened once.
func (g *FanOutGate) Wait(ctx context.Context) (leaderErr error, err error) {
	select {
	case <-g.released:
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.leadErr, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ErrNoFanOutWork is returned when a fan-out is asked to run nothing, which is
// a caller mistake rather than an empty success.
var ErrNoFanOutWork = errors.New("fanout: no work to run")

// FanOut runs work concurrently with the cache-friendly ordering: the first
// item runs alone until it reports first output, then the rest are released
// together.
//
// The gate is passed to each unit of work rather than driven from here,
// because only the work itself knows when it has begun producing: it calls
// Claim, and whichever caller is told yes calls Released as soon as its first
// output arrives. A fan-out that could only see "started" and "finished" from
// the outside would have to wait for completion to open the gate, which is the
// serialisation this exists to avoid.
//
// Results come back in input order regardless of completion order, so a caller
// zipping them against their inputs is not silently mismatched.
func FanOut[T any](
	ctx context.Context,
	count int,
	run func(ctx context.Context, index int, gate *FanOutGate) (T, error),
) ([]T, error) {
	if count <= 0 {
		return nil, ErrNoFanOutWork
	}
	if run == nil {
		return nil, fmt.Errorf("fanout: no function to run")
	}

	gate := NewFanOutGate()
	results := make([]T, count)
	errs := make([]error, count)

	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			// Release on the leader's return, not on everyone's: a leader that
			// forgot to release must not strand the followers, and waiting for
			// every worker to finish would wait for the followers that are
			// blocked on exactly that release.
			defer gate.workerFinished(index)
			results[index], errs[index] = run(ctx, index, gate)
		}(i)
	}

	// If no worker ever claims the lead -- a run function that never calls
	// Claim -- every follower waits on a release nobody will send. Watch for
	// that and open the gate rather than hanging.
	go func() {
		waitForClaim(ctx, gate)
	}()

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			// Named by index, not by content: the inputs are the caller's.
			return results, fmt.Errorf("fanout: item %d failed: %w", i, err)
		}
	}
	return results, nil
}

// waitForClaim opens the gate if nobody has taken the lead by the time the
// context ends, so a run function that never calls Claim degrades to an
// ordinary unordered fan-out instead of deadlocking.
func waitForClaim(ctx context.Context, gate *FanOutGate) {
	select {
	case <-gate.released:
		return
	case <-ctx.Done():
		if gate.noLeaderClaimed() {
			gate.Released()
		}
	}
}

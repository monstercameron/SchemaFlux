package mw

import (
	"context"
	"sync"
	"time"
)

// fakeClock is the test double every timing-sensitive test in this package
// uses instead of the wall clock. Its sleep advances its own now() by
// exactly the requested duration and returns immediately, so a test that
// exercises a code path involving seconds or minutes of backoff runs in
// microseconds and produces the same result every time -- which is the
// difference between a timing test that verifies the arithmetic and one
// that verifies how busy the machine running it happened to be.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time

	// slept records every duration the code under test asked to wait, so a
	// test can assert on the sequence of waits (e.g. that jitter varies, or
	// that a Retry-After wait was honoured exactly) without needing real
	// time to have passed.
	slept []time.Duration
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// sleep matches the (ctx, time.Duration) error signature every hook in this
// package uses. It still honours ctx: a context already canceled or expired
// aborts the "wait" without advancing the clock, because a fake that always
// succeeds would hide a bug where the production code forgot to check ctx at
// all.
func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.slept = append(c.slept, d)
	c.mu.Unlock()
	return nil
}

// waits returns a copy of the durations slept so far.
func (c *fakeClock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.slept))
	copy(out, c.slept)
	return out
}

package schemaflux

import (
	"context"
	"math"
	"sync"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Scheduler bounds how many requests are with a provider at once and how many
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

// SchedulerStats is a Scheduler's load at one moment.
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

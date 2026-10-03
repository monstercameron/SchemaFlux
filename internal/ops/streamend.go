package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The end of a stream.
//
// Nothing told whoever opened a stream that its outcome was settled: the
// producer publishes the stream's call record and then hands the reader a last
// item, and a caller that wanted to finish its own record of the call had only
// the reader's loop to watch, which a reader that leaves, or never reads, does
// not complete. A callback carried on the context is told once, from the
// goroutine that produces the stream, after the call record is published and
// before the last item reaches a reader.

// streamEndKind says how a stream ended. The zero value is a stream that has
// not.
type streamEndKind uint8

const (
	// streamEndCompleted is a stream whose answer was delivered.
	streamEndCompleted streamEndKind = iota + 1
	// streamEndFailed is a stream the provider failed, whose answer was refused
	// (truncated, empty, from a route the client's data policy does not allow),
	// or that the client's scheduler closed on. Err says which.
	streamEndFailed
	// streamEndClosed is a stream whose reader left the loop, by breaking out of it
	// or by panicking out of it, or called Cancel, before it ended.
	streamEndClosed
	// streamEndContextEnded is a stream whose context was cancelled or reached its
	// deadline, which includes the stream's own timeout.
	streamEndContextEnded
)

func (k streamEndKind) String() string {
	switch k {
	case streamEndCompleted:
		return "completed"
	case streamEndFailed:
		return "failed"
	case streamEndClosed:
		return "closed"
	case streamEndContextEnded:
		return "context ended"
	}
	return "not ended"
}

// streamEnd is what a stream's end callback is told. It carries what the
// provider reported of the answer and never the answer's text, so a record
// made from it holds nothing of the caller's.
type streamEnd struct {
	Kind streamEndKind
	// Err is the stream's outcome, as every reader gets it: nil for a stream
	// that completed, otherwise the classified reason.
	Err error
	// Reported is whether the provider's last chunk arrived. When it did not,
	// which is the case for a stream that was stopped or failed before its
	// end, the fields below are empty.
	Reported bool
	// Provider and Model are who answered, as the provider named them, and the
	// route the request went to when it named neither.
	Provider string
	Model    string
	// FinishReason and Usage are the provider's own.
	FinishReason string
	Usage        types.TokenUsage
	// Cached is whether the answer came from a cache.
	Cached bool
}

// streamEndKey carries a stream's end callback on a context.
type streamEndKey struct{}

// withStreamEnd returns a context whose stream is told, once, when it ends: fn
// is called with the stream, from the goroutine that produces it, after the
// stream's call record is published and before its last item is handed to a
// reader, so that the stream's Outcome and ending are settled when it runs. It
// is called for a stream that ends in any way a reader can see, which is every
// way a stream that streamLLM returned can end, and never for a call that
// returned an error and opened no stream.
//
// The callback applies to every stream opened under the returned context, so a
// caller that makes more than one derives a context for each. The nearest
// callback on the context is the one called. A nil fn returns ctx.
//
// fn runs on the producer's goroutine: a callback that blocks holds the
// stream's slot in the client's scheduler and its last item. It must not wait
// for the reader. A panic in it is recovered, and logged by type only, so that
// a callback cannot take the process down from a goroutine its author did not
// start or leave the reader without its last item.
func withStreamEnd(ctx context.Context, fn func(*TextStream)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, streamEndKey{}, fn)
}

// streamEndFrom reads the callback a context carries, or nil.
func streamEndFrom(ctx context.Context) func(*TextStream) {
	fn, _ := ctx.Value(streamEndKey{}).(func(*TextStream))
	return fn
}

// ended is the stream's end. It makes sure the request the stream sent has its
// call record, and then tells whoever opened the stream that its outcome is
// settled. The record is published once and the callback is called once, and a
// stream opened without a callback is not told anything.
//
// It is called by the producer only: from finish, after the stream's last item
// is recorded and before it is sent, and from the deferred function of
// runStream that covers every exit that hands a reader no last item. The record
// comes first, so that a callback that reads the call's record, as
// RecordedStream's does, finds the request in it.
func (s *TextStream) ended() {
	s.recordSentRequest()
	if s.onEnd == nil {
		return
	}
	s.endOnce.Do(func() {
		s.settleEnd()
		defer func() {
			if recovered := recover(); recovered != nil {
				// What was panicked with may hold anything, so only its type is
				// logged.
				loggerFor(s.ctx).Error("A stream's end callback panicked", "panic_type", fmt.Sprintf("%T", recovered))
			}
		}()
		s.onEnd(s)
	})
}

// settleEnd records what the callback is told of the stream's end.
func (s *TextStream) settleEnd() {
	end := s.endOf()
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	s.endInfo = end
}

// endOf is how the stream ended and what the provider reported of its answer,
// claiming the stream's outcome if nothing has. A stream that reached its end
// with no last item was stopped, and the reason is claimed as its outcome as a
// reader that finds the items closed would claim it, so that the callback, the
// call record and every reader agree.
func (s *TextStream) endOf() streamEnd {
	// The producer's own view of the provider's last chunk. Only the producer
	// touches it, and this runs on the producer.
	answered, response := s.observed.answered, s.observed.response
	provider, model := s.observed.provider, s.observed.model

	stopped := s.whyStopped()
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.final == nil {
		s.final = &textStreamItem{final: true, err: stopped}
	}
	err := s.final.err

	end := streamEnd{Err: err, Reported: answered}
	if answered {
		end.Provider, end.Model = response.Provider, response.Model
		if end.Provider == "" {
			end.Provider = provider
		}
		if end.Model == "" {
			end.Model = model
		}
		end.FinishReason = response.FinishReason
		end.Usage = response.Usage
		end.Cached = response.Cached
	}
	switch {
	case err == nil:
		end.Kind = streamEndCompleted
	case s.readerClosed && errors.Is(err, context.Canceled):
		end.Kind = streamEndClosed
	case s.ctx.Err() != nil && errors.Is(err, s.ctx.Err()):
		end.Kind = streamEndContextEnded
	default:
		end.Kind = streamEndFailed
	}
	return end
}

// recordSentRequest publishes the call record of the request a stream sent when
// no path has published one, once: a stream that failed, was refused as
// truncated or empty, was stopped by its reader, Cancel or its context, or ended
// with no last chunk. The record says one request to the provider and model that
// were asked, and what the stream ended in; the provider's own report of its
// answer is in it when the last chunk arrived, and otherwise the usage is marked
// as not reported. These streams recorded nothing, so a call that was billed for
// the request counted none.
//
// A stream that sent nothing has no record: it was refused before the request
// was made. One that completed was recorded by the path that completed it.
func (s *TextStream) recordSentRequest() {
	if !s.sent.Load() || s.recorded.Swap(true) {
		return
	}
	end := s.endOf()
	outcome := streamOutcomeFailed
	switch end.Kind {
	case streamEndCompleted:
		outcome = streamOutcomeCompleted
	case streamEndClosed, streamEndContextEnded:
		outcome = streamOutcomeStopped
	}
	var resp llm.CompletionResponse
	if end.Reported {
		resp = s.observed.response
	}
	// The span the observers opened for the request, when there is one, is where
	// the record's trace and span ids come from, as on the path that completes.
	spanned := s.ctx
	if s.observed.observing {
		spanned = s.observed.ctx
	}
	asked := &s.asked
	publishCallRecord(s.ctx, streamCallRecord(spanned, asked.tracking, asked.provider, asked.model,
		asked.responseFormat, asked.opts, resp, asked.start, outcome))
}

// markReaderClosed notes that the reader is about to stop the stream, when
// nothing has ended it yet: a reader that leaves a stream that already has its
// outcome did not end it.
func (s *TextStream) markReaderClosed() {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.final == nil && s.stopped == nil && s.ctx.Err() == nil {
		s.readerClosed = true
	}
}

// ending reports how the stream ended and what the provider reported of it. It
// is the zero streamEnd until the stream's end callback is about to run, and
// only a stream opened with a callback has one.
func (s *TextStream) ending() streamEnd {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.endInfo
}

// Outcome reports whether the stream has ended and the error it ended with:
// nil for one that completed, and for one that has not ended. It is Ended and
// Err read together, so that the pair is one reading. It is true as soon as the
// stream has an outcome, which for a stream with an end callback is before the
// callback runs and before the stream's last item reaches a reader.
func (s *TextStream) Outcome() (ended bool, err error) {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.knownLocked()
}

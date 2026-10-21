package ops

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Three streaming modes, kept apart.
//
// TODOS.md ST-002's Revised line names three things a streaming text
// operation could hand back, and requires they never be conflated:
//
//  1. Raw text -- incremental deltas of the answer as the provider produces
//     them. TextStream, below, is this and only this: it never yields a
//     provider event, and never yields a struct claiming to be a partial
//     parsed result.
//  2. Provider events -- llm.StreamChunk, one level down in internal/llm.
//     It carries the same deltas plus the provider's own framing (Done,
//     the accumulated Response). A caller who type-asserts a Provider to
//     llm.StreamingProvider and calls CompleteStream directly gets this
//     mode; ops does not re-wrap it, because re-wrapping it as "raw text"
//     would be exactly the flattening this rule forbids.
//  3. Validated items -- a stream of items each checked against a schema
//     as they complete, for a collection operation (Choose, Filter,
//     Cluster, and friends in internal/ops/collection.go and
//     combinators.go). No such mode exists here. Building one by decoding
//     a partial JSON prefix off a token stream is precisely the "a token
//     stream is not a partial typed result" failure the Revised line
//     names: an item is either fully received and validated, or it does
//     not exist yet, and there is no honest halfway representation of a
//     struct that has been JSON-parsed by however many bytes happened to
//     have arrived. Nothing in this repository's collection operations
//     streams today, so there is no honest place to attach mode 3 yet --
//     rather than invent one, this file only ships modes 1 and 2, and mode
//     3 stays a documented gap, not a token stream wearing a struct's name.

// textStreamBufferSize bounds the channel between the goroutine reading the
// provider stream and the goroutine ranging over a TextStream.
//
// It is not sized to "never block" -- the opposite. Once it is full, the
// producer blocks on the channel send instead of buffering an unbounded
// number of deltas while a slow or stalled consumer never asks for them.
// That block propagates all the way to the socket: llm.CompleteStream is
// itself pull-based (see its doc comment), so a producer blocked on send
// here stops reading the wire, rather than reading ahead into memory.
const textStreamBufferSize = 32

// textStreamItem is either an incremental delta or the one terminal item a
// TextStream ever sends. Exactly one terminal item is sent per stream,
// always last, and it is never both a delta and a result.
type textStreamItem struct {
	delta string

	final bool
	err   error
	resp  llm.CompletionResponse
}

// TextStream is the caller-facing iterator for a streaming text operation.
// See the "Three streaming modes" note above for what this is and is not.
//
// Ordering: deltas are delivered in COMPLETION order -- the order the
// provider generated them in. For every provider wired up today that is
// also wire order; nothing here reorders or reassembles, so a caller
// appending each delta it receives directly to a buffer reconstructs the
// answer correctly.
//
// Cancellation: ranging over All and breaking out of the loop before the
// stream finishes cancels the underlying request, unless Detach was called
// first. A TextStream that nobody ever ranges over, and is simply dropped,
// keeps its goroutine and the request it is reading until its buffer fills and
// the stream's timeout (or the caller's context) ends it -- call Cancel (or
// range to completion) even when the caller never wanted the text.
//
// Outcome: a stream ends once, and the first thing to decide how is what every
// reader, Err, Text, the client's budget and the call record say about it. The
// producer decides it when the provider finishes or fails; a reader leaving the
// loop, Cancel, and a context that is over decide it when they get there first.
type TextStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	items  chan textStreamItem

	detached atomic.Bool // set by Detach, read by All; see Detach.

	resultMu  sync.Mutex
	consumed  bool
	finalText string
	finalResp llm.CompletionResponse
	err       error
	// stopped is why the stream ended with no final item, left by the
	// producer for a reader who comes to the stream afterwards.
	stopped error
	// final is the stream's one outcome, as the last item it has: claimed by
	// the producer before it does anything that depends on how the stream
	// ended, or by whatever stops the stream first. The first claim stands.
	// It is recorded whether or not a reader takes it from the channel.
	final *textStreamItem
	// drained is made when the stream starts reading itself to its end, which
	// a detached stream does when its reader leaves, and is closed when it
	// has. It is nil until then.
	drained chan struct{}
	// drainBegun is closed when the drain starts, so that a reader already
	// waiting for an item can see it and stand aside. Made by whichever of
	// that reader and the drain gets there first.
	drainBegun chan struct{}

	// observed is the request as the installed observers and the metrics are
	// told of it. Only the producer touches it.
	observed observedStream

	// asked is the request as it was made, fixed before the producer starts, for
	// the call record of a stream that ends without publishing one. sent is set
	// by the producer once it has made the request, and recorded when the
	// request's call record has been published, whichever path published it. A
	// stream refused before it sent anything has neither.
	asked    streamAsked
	sent     atomic.Bool
	recorded atomic.Bool

	// onEnd is told once that the stream's outcome is settled (streamend.go),
	// and endOnce makes it once. A stream opened with no callback has neither.
	onEnd   func(*TextStream)
	endOnce sync.Once
	// readerClosed is whether the reader's leaving, or Cancel, is what ended
	// the stream, and endInfo what it was told of the end. Both are guarded by
	// resultMu.
	readerClosed bool
	endInfo      streamEnd
}

// Detach stops All from canceling the underlying request when the caller
// breaks out of the range loop early.
//
// Without calling this, stopping iteration stops the work -- the default,
// because a caller who walked away from a stream almost never wants the
// provider still spending their tokens on an answer nobody will read.
// Detach is for the opposite case: keep generating after the caller stops
// displaying tokens (to warm a cache, or to log the full answer).
//
// After the loop is left the stream reads itself to its end. A range over it
// from then on waits for that and yields no text, and no error unless the
// stream failed: it is a way to wait for the end, and Text is how to get the
// whole answer. It does not resume where the first range stopped. It was left
// this way, and not made an error, because a clean end that reported itself as
// a failure would make the one supported way to wait for a detached stream
// unusable.
//
// Detach only has an effect if it is called before the range loop breaks;
// calling it after All has already returned does nothing. It may be called from
// a goroutine other than the one ranging.
func (s *TextStream) Detach() { s.detached.Store(true) }

// Cancel stops the stream immediately, regardless of Detach, and releases a
// reader waiting on it. Err reports the cancellation from here on, unless the
// stream had already ended some other way.
func (s *TextStream) Cancel() {
	s.markReaderClosed()
	s.cancel()
	s.resultMu.Lock()
	s.knownLocked()
	s.resultMu.Unlock()
}

// All returns the iterator. Ranging over it yields each text delta as it
// arrives, in completion order; the loop ends when the stream completes,
// fails, or is canceled. After the loop ends, Err (and Text) report the
// outcome.
//
// A stream has one outcome and keeps it. Once it has ended, ranging again
// yields no more text and, if it ended in an error, that error; Text returns
// the whole text or the error however many times it is called. Every range
// that ends yields the stream's outcome, the same one Err reports, and not the
// error of whichever item it happened to take last.
//
// A reader that leaves the loop, by breaking out of it or by panicking out of
// it, stops the stream, unless Detach was called first.
//
// A stream has one reader at a time. Two goroutines ranging over it at once
// share its text between them, each getting some of the pieces; both are told
// the outcome, and Text gives either of them the whole.
//
// After a detached reader leaves the loop the stream reads itself to its end,
// and a range over it, started before or after, waits for that instead of
// reading beside it: it yields no text, and nothing at all if the stream
// finished. A range already waiting for an item stops taking them as soon as it
// sees the drain begin; the item it was taking at that moment is still its own.
// See Detach.
//
// A range waiting for the provider is released by Cancel, by the caller's
// context ending, and by the stream's timeout, and yields the cancellation. It
// does not depend on the provider obeying its context.
func (s *TextStream) All() iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		// The outcome is already known: there is nothing more to read.
		if ended, err := s.outcome(); ended {
			if err != nil {
				yield("", err)
			}
			return
		}

		// A reader that panics out of its loop is as gone as one that breaks
		// out of it, and stops the stream the same way. Without this the
		// producer, blocked sending to a reader that will never come, held its
		// goroutine and the provider's connection, and the request's span stayed
		// open, until the stream's timeout.
		inLoop := false
		defer func() {
			if inLoop {
				s.readerLeft()
			}
		}()

		for {
			item, state := s.next(true)
			switch state {
			case readDraining:
				// A detached stream whose reader left is reading itself to its
				// end. This waits for that and yields no text. It used to read
				// the same channel beside the drain, and the two shared the
				// pieces between them: a caller joining what this range yielded
				// had part of the answer and no error to say so.
				if err := s.awaitDrain(s.draining()); err != nil {
					yield("", err)
				}
				return
			case readClosed, readStopped:
				// The items ran out with no final one, or the stream was
				// stopped while this waited for the next. Either another reader
				// took the final item, a second goroutine in All or the drain of
				// a detached stream, and the outcome is on record; or the stream
				// was stopped before it finished. The second used to end the
				// loop and nothing else, so Text returned an empty string and no
				// error, which is what an answer of nothing looks like.
				if err := s.endWithoutFinal(); err != nil {
					yield("", err)
				}
				return
			}

			if item.final {
				// What this yields is the stream's outcome, not this item's own
				// error. Another reader may have ended the stream before this
				// item arrived, a reader that left the loop for one, and then
				// this item says "finished" about a stream whose outcome is a
				// cancellation.
				s.settle(item)
				if _, err := s.outcome(); err != nil {
					yield("", err)
				}
				return
			}
			inLoop = true
			keepReading := yield(item.delta, nil)
			inLoop = false
			if !keepReading {
				s.readerLeft()
				return
			}
		}
	}
}

// readerLeft is what is done when the reader of All stops taking items before
// the stream has ended, by breaking out of its loop or by panicking out of it.
func (s *TextStream) readerLeft() {
	if s.detached.Load() {
		// The caller wants the work to keep running after it stops reading.
		// Nobody will call All again to drain s.items, so the producer would
		// block forever on its next send without this: a goroutine and the HTTP
		// response it holds open, leaked for the lifetime of the process. The
		// drain keeps the outcome, so a later Text still has the whole answer.
		s.startDrain()
		return
	}
	// The reader left, and that stops the work. It is the stream's outcome from
	// here, unless the producer had already recorded how the stream ended.
	// Nothing was recorded here before, so Err returned nil, which is what a
	// stream that finished returns, until something read the stream again.
	s.markReaderClosed()
	s.cancel()
	s.endWithoutFinal()
}

// readState is what next found.
type readState int

const (
	readTook     readState = iota // an item was taken
	readClosed                    // the producer closed the channel
	readStopped                   // the stream was stopped while next waited, with nothing queued
	readDraining                  // the stream began reading itself, so a reader stands aside
)

// next takes the stream's next item, or says why there is none.
//
// What is already queued is taken first, even from a stream that is stopped:
// the producer's last item is queued before it can be told anything. Waiting
// for one that is not there ends when the stream is stopped, which is Cancel,
// the caller's context or the stream's timeout. A wait with no way out blocked
// forever on a provider that ignores its context, and Cancel could not release
// it. A reader asks to be told when the drain starts, so that it does not read
// beside it; the drain itself does not.
func (s *TextStream) next(watchDrain bool) (textStreamItem, readState) {
	var begun chan struct{}
	if watchDrain {
		begun = s.drainSignal()
		select {
		case <-begun:
			return textStreamItem{}, readDraining
		default:
		}
	}
	take := func(item textStreamItem, ok bool) (textStreamItem, readState) {
		if !ok {
			return textStreamItem{}, readClosed
		}
		return item, readTook
	}
	select {
	case item, ok := <-s.items:
		return take(item, ok)
	default:
	}
	select {
	case item, ok := <-s.items:
		return take(item, ok)
	case <-begun: // nil, and never ready, for the drain
		return textStreamItem{}, readDraining
	case <-s.ctx.Done():
		select {
		case item, ok := <-s.items:
			return take(item, ok)
		default:
			return textStreamItem{}, readStopped
		}
	}
}

// awaitDrain waits for the drain of a detached stream to finish, or for the
// stream to be stopped, and returns the stream's outcome. Nothing but the drain
// ending released this wait, so a provider that ignored its context held every
// range over the stream, and Cancel could not free them.
func (s *TextStream) awaitDrain(done chan struct{}) error {
	select {
	case <-done:
	case <-s.ctx.Done():
	}
	return s.endWithoutFinal()
}

// drainSignal returns the channel closed when the drain starts, making it if
// nobody has.
func (s *TextStream) drainSignal() chan struct{} {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.drainBegun == nil {
		s.drainBegun = make(chan struct{})
	}
	return s.drainBegun
}

// outcome reports how the stream ended, once a reader has read it to its end.
func (s *TextStream) outcome() (ended bool, err error) {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.consumed, s.err
}

// claim records item as the stream's outcome if nothing has been, and reports
// whether it is the one that stands. The producer claims before it charges a
// budget or publishes a call record, so those say what the readers are told.
func (s *TextStream) claim(item textStreamItem) bool {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.final != nil {
		return false
	}
	recorded := item
	s.final = &recorded
	return true
}

// knownLocked reports the stream's outcome as far as it is known, and whether
// it is. It holds the outcome fixed once it has said it: a context that is
// over, with nothing else decided, is claimed as the outcome here, so the
// producer that finishes a moment later does not turn a cancellation that was
// reported into a success. The caller holds resultMu.
func (s *TextStream) knownLocked() (ended bool, err error) {
	switch {
	case s.consumed:
		return true, s.err
	case s.final != nil:
		return true, s.final.err
	case s.stopped != nil:
		return true, s.stopped
	}
	if over := contextOver(s.ctx); over != nil {
		s.final = &textStreamItem{final: true, err: over}
		return true, over
	}
	return false, nil
}

// settle records the stream's outcome when a reader takes its final item. The
// first one stands, and it is the stream's claimed outcome, which is the item
// itself unless something else claimed first.
func (s *TextStream) settle(item textStreamItem) {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.consumed {
		return
	}
	s.consumed = true
	if s.final == nil {
		recorded := item
		s.final = &recorded
	}
	s.applyLocked(*s.final)
}

// applyLocked copies an outcome into what Err and Text read.
func (s *TextStream) applyLocked(final textStreamItem) {
	s.err = final.err
	if final.err == nil {
		s.finalResp = final.resp
		s.finalText = final.resp.Content
	}
}

// endWithoutFinal is called by a reader that found the items closed, or the
// stream stopped, with no final one in its hands. It returns the stream's
// outcome, recording it if nobody has.
//
// The final item may have gone to another reader, a second goroutine in All or
// the drain of a detached stream, and that reader may not have recorded it yet:
// taking an item off the channel and recording it are two steps, and the
// channel can be seen closed between them. So the outcome is not read from what
// a reader recorded. The producer records the final item itself before it
// closes the channel, and that is what is used here. A stream whose producer
// left no final item was stopped, and the reason is claimed as its outcome so
// that a producer still running cannot end it differently afterwards.
func (s *TextStream) endWithoutFinal() error {
	stopped := s.whyStopped()
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.consumed {
		return s.err
	}
	s.consumed = true
	if s.final == nil {
		s.final = &textStreamItem{final: true, err: stopped}
	}
	s.applyLocked(*s.final)
	return s.err
}

// draining returns the channel that is closed when the stream has read itself
// to its end, or nil when it is not doing so.
func (s *TextStream) draining() chan struct{} {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.drained
}

// startDrain has the stream read itself to its end, once.
func (s *TextStream) startDrain() {
	s.resultMu.Lock()
	if s.drained != nil {
		s.resultMu.Unlock()
		return
	}
	done := make(chan struct{})
	s.drained = done
	if s.drainBegun == nil {
		s.drainBegun = make(chan struct{})
	}
	close(s.drainBegun)
	s.resultMu.Unlock()
	go func() {
		defer close(done)
		s.drain()
	}()
}

// drain reads a detached stream to its end on the caller's behalf and keeps
// the outcome. It also ends when the stream is stopped with nothing queued, so
// a provider that never returns does not keep this goroutine for ever.
func (s *TextStream) drain() {
	for {
		item, state := s.next(false)
		if state != readTook {
			break
		}
		if item.final {
			s.settle(item)
		}
	}
	s.endWithoutFinal()
}

// whyStopped is the error of a stream that ended with no final item.
func (s *TextStream) whyStopped() error {
	s.resultMu.Lock()
	stopped := s.stopped
	if s.final != nil && s.final.err != nil {
		stopped = s.final.err
	}
	s.resultMu.Unlock()
	if stopped != nil {
		return stopped
	}
	if over := contextOver(s.ctx); over != nil {
		return over
	}
	return types.Newf(types.KindProviderUnavailable, "stream", "the stream ended before it finished")
}

// Text drains the stream and returns the complete text -- the same content
// a buffered call would return for an identical response, built by reading
// the same deltas a caller ranging manually would see, not by making a
// second request.
func (s *TextStream) Text() (string, error) {
	for _, err := range s.All() {
		if err != nil {
			return "", err
		}
	}
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	return s.finalText, nil
}

// Err reports the stream's outcome as soon as the stream has one, whether or
// not anything has read it: the provider failed or finished, the reader left the
// loop early without Detach, Cancel was called, the caller's context ended, or
// the stream's timeout passed. A stream the client's scheduler closed on has one
// once its producer has noticed.
//
// Nil means the stream finished cleanly, or has not ended. Err was nil after a
// Cancel, after the caller's context was cancelled, and while a detached stream
// was reading itself to its end, until something read the stream, which is what
// a stream that finished returns. Ended tells the two apart.
func (s *TextStream) Err() error {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	_, err := s.knownLocked()
	return err
}

// Ended reports whether the stream has an outcome: it finished, failed or was
// stopped, whether or not anything has read it yet. Err then says which, and is
// nil only for a stream that finished cleanly. A stream with text still to be
// read can have ended.
func (s *TextStream) Ended() bool {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	ended, _ := s.knownLocked()
	return ended
}

// classifyFinalChunk decides whether a stream's terminal Response is usable,
// using the exact same classifier the buffered path uses in CallLLM
// (internal/ops/llm_helper.go) -- llm.ClassifyCompletion for a truncated
// body, then validateLLMCompletion for anything else. Reusing those
// functions rather than re-deriving the check here is what keeps a streamed
// failure classified identically to a buffered one for the same cause; a
// second implementation of "is this response usable" is exactly the second
// opinion AGENTS.md forbids.
func classifyFinalChunk(resp llm.CompletionResponse) error {
	switch kind := llm.ClassifyCompletion(resp); kind {
	case types.KindOutputTruncated:
		provider := resp.Provider
		model := resp.Model
		return &types.OperationError{
			Kind:     kind,
			Provider: provider,
			Model:    model,
			Message:  fmt.Sprintf("response truncated before it could be decoded (finish_reason=%q)", resp.FinishReason),
		}
	case types.KindPolicyViolation:
		// A final content-filter response is a refusal even when the provider
		// sent some text first. Never deliver that partial text as an answer.
		return &types.OperationError{
			Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal,
			Provider: resp.Provider, Model: resp.Model,
			Message: "response withheld by provider content filter",
		}
	}
	return validateLLMCompletion(resp)
}

// streamLLM is the streaming counterpart of CallLLM (llm_helper.go).
//
// It builds the exact same request CallLLM would for identical inputs --
// literally the same unexported helpers (config.GetModel, applySteering,
// resolveResponseFormat, strengthenSystemPrompt, promptCacheKeyFor) compute
// every field, so the two paths cannot describe "the same call" two
// different ways. What it does not share with CallLLM is retries: a stream
// that fails partway has already shown the caller text, and re-sending the
// request would either duplicate what they saw or silently replace it --
// neither is "retrying a request", so streamLLM does not retry. A caller
// that wants retry-on-failure semantics around a stream gets one attempt and
// an error describing why, and decides for themselves whether to start a
// new TextStream.
func streamLLM(ctx context.Context, systemPrompt, userPrompt string, opts types.OpOptions) (*TextStream, error) {
	// The client the stream runs on is fixed first, so that the lines below
	// and everything else read for it are read from one client.
	ctx, _ = PinExec(ctx)
	log := loggerFor(ctx)

	// LX-061. A context that is already over opens no stream.
	if err := contextOver(ctx); err != nil {
		return nil, err
	}

	// The provider is resolved by the lookup the buffered path uses (routeFor):
	// a client's own, then one attached to the context, then the process
	// default. This path read only the process default, so a stream started
	// through a client went to whichever client had been built last.
	//
	// The client the stream runs on is fixed first, so that what is read for
	// it later is read from one client.
	ctx, _ = PinExec(ctx)
	provider, _, routeErr := routeFor(ctx)
	if routeErr != nil {
		return nil, routeErr
	}
	if provider == nil {
		// Streaming has no equivalent of the customLLMCaller test hook: that
		// hook is a plain (ctx, prompts) -> (string, error) function, which
		// has no way to emit incremental chunks. A caller (or test) that set
		// only a customLLMCaller and no provider gets ErrNoProvider here,
		// same as the buffered path gets when neither is set.
		return nil, noProvider
	}

	streamer, ok := provider.(llm.StreamingProvider)
	if !ok {
		// The provider is real but cannot stream. This must never fall back
		// to a buffered Complete dressed up as a one-chunk stream -- that
		// would hide the difference between "streams" and "doesn't" behind
		// an identical-looking success, which is the opposite of what a
		// capability error is for.
		return nil, &types.OperationError{
			Kind:     types.KindUnsupportedCapability,
			Provider: provider.Name(),
			Message:  fmt.Sprintf("provider %q does not support streaming", types.Bound(provider.Name(), types.MaxProviderName)),
		}
	}

	// A pinned model wins over the client's and the client's over the tier
	// mapping, as in CallLLM.
	model := resolveModel(ctx, opts, provider)
	if model == "" {
		return nil, config.NoDefaultModelError(provider.Name())
	}

	// LX-126: the client's data policy, before anything else, as in CallLLM.
	if err := checkDataPolicy(ctx, provider, model); err != nil {
		log.Error("Refusing the streaming request: the client's data policy does not allow this route",
			"provider", provider.Name(), "model", model, "error", loggedError(err))
		return nil, err
	}

	// A client's own budget replaces the process-wide one, as in CallLLM.
	if err := checkBudgetFor(ctx, provider, model); err != nil {
		log.Error("Refusing the streaming request: budget exhausted",
			"provider", provider.Name(), "model", model, "error", loggedError(err))
		return nil, err
	}

	maxTokens := config.GetMaxTokens(opts.Intelligence)
	if opts.MaxOutputTokens > 0 {
		maxTokens = opts.MaxOutputTokens
	}
	temperature := config.GetTemperature(opts.Mode)

	// Steering goes in the user message, exactly as it does in CallLLM.
	//
	// This path put it in the *system* prompt: caller-supplied text placed
	// where the library's own trusted instructions go, which is the precise
	// violation TC-001 exists to prevent, and it also moved the cacheable
	// prefix on every steered call (CA-002). The buffered path was fixed and
	// this one was missed, which is what happens when two code paths build the
	// same request in two places.
	responseFormat := resolveResponseFormat(opts.ResponseFormat, systemPrompt)
	stableSystemPrompt := strengthenSystemPrompt(ctx, systemPrompt, responseFormat)

	// TC-001, as in CallLLM: the caller's steering must not be inside what is
	// sent as system policy. This path built the same request and skipped the
	// check.
	if err := verifyTrustBoundary(stableSystemPrompt, opts.Steering); err != nil {
		log.Error("Refusing the streaming request: trust boundary violated",
			"provider", provider.Name(), "error", loggedError(err))
		return nil, err
	}

	effort, effortPreferred := requestEffort(ctx, opts)

	req := llm.CompletionRequest{
		Model:                    model,
		SystemPrompt:             stableSystemPrompt,
		UserPrompt:               applySteering(userPrompt, opts.Steering),
		Temperature:              float64(temperature),
		MaxTokens:                maxTokens,
		ResponseFormat:           responseFormat,
		JSONSchema:               opts.JSONSchema,
		SchemaName:               opts.SchemaName,
		PromptCacheKey:           promptCacheKeyFor(opts, model, responseFormat, stableSystemPrompt),
		WebSearch:                opts.WebSearch,
		ReasoningEffort:          effort,
		ReasoningEffortPreferred: effortPreferred,
	}

	streamCtx, cancel := operationContext(ctx, config.GetTimeout())
	streamCtx, tracking := requesttracking.Ensure(streamCtx, opts.RequestID, opts.CorrelationID)
	start := time.Now()

	log.Debug("LLM stream started",
		"requestID", tracking.RequestID,
		"correlationID", tracking.CorrelationID,
		"provider", provider.Name(),
		"model", model,
		"responseFormat", responseFormat,
		"maxTokens", maxTokens,
	)

	stream := &TextStream{
		ctx:    streamCtx,
		cancel: cancel,
		items:  make(chan textStreamItem, textStreamBufferSize),
		// Read before the producer starts, so that the callback a caller put on
		// the context is the one this stream is told of its end by.
		onEnd: streamEndFrom(ctx),
		asked: streamAsked{
			tracking:       tracking,
			provider:       provider,
			model:          model,
			responseFormat: responseFormat,
			opts:           types.OpOptions{Mode: opts.Mode, Intelligence: opts.Intelligence, SchemaID: opts.SchemaID},
			start:          start,
		},
	}

	// LX-126: a stream takes one slot in the client's scheduler, when it has
	// one, and holds it until the stream ends, however it ends. Admission is
	// waited for here, so a stream the scheduler refuses is refused by this
	// call and not by the first read.
	if ExecScheduler(streamCtx) == nil {
		go runStream(streamCtx, stream, streamer, req, provider, model, responseFormat, opts, tracking, start)
		return stream, nil
	}
	admission := make(chan error, 1)
	go func() {
		_, started, err := throughScheduler(streamCtx, provider, req, func(runCtx context.Context) (struct{}, error) {
			admission <- nil
			runStream(runCtx, stream, streamer, req, provider, model, responseFormat, opts, tracking, start)
			return struct{}{}, nil
		})
		if !started {
			admission <- err
		}
	}()
	if err := <-admission; err != nil {
		cancel()
		log.Error("Streaming request was not admitted by the client's scheduler",
			"requestID", tracking.RequestID,
			"correlationID", tracking.CorrelationID,
			"provider", provider.Name(),
			"model", model,
			"error", loggedError(err),
		)
		return nil, err
	}

	return stream, nil
}

// send delivers an item to the consumer, or gives up once the stream is stopped:
// its own context ends, which is the caller cancelling or walking away, or the
// context it runs under ends, which is the client's scheduler being closed.
//
// The second was not watched. A stream nobody was reading, its buffer full,
// held its slot in the scheduler until the caller's deadline, and Close waited
// with it.
//
// Room in the buffer is taken before either context is looked at, so a final
// item is delivered whenever it can be, even to a stream that is being stopped.
func (s *TextStream) send(run context.Context, item textStreamItem) bool {
	select {
	case s.items <- item:
		return true
	default:
	}
	select {
	case s.items <- item:
		return true
	case <-s.ctx.Done():
		return false
	case <-run.Done():
		return false
	}
}

// finish records the stream's last item and delivers it. The record comes
// first and does not depend on the delivery: a reader that finds the channel
// closed takes the outcome from the record, so it is there before the channel
// can be closed. The first final item is the one that stands.
func (s *TextStream) finish(run context.Context, item textStreamItem) {
	s.resultMu.Lock()
	if s.final == nil {
		recorded := item
		s.final = &recorded
	}
	outcome := s.final.err
	s.resultMu.Unlock()
	// The observers are told before the final item is delivered, so a reader
	// that sees the stream end finds its request already finished.
	s.observed.end(outcome, false)
	// Whoever opened the stream is told next, so that its callback has run when
	// the reader sees the last item.
	s.ended()
	s.send(run, item)
}

// stopCause says why a stream's provider stopped, in this library's terms. A
// provider whose context is cancelled reports a cancellation whoever cancelled
// it; which of the three it was is known here and not there.
func stopCause(run context.Context, s *TextStream, err error) error {
	if over := contextOver(s.ctx); over != nil {
		return over
	}
	if stoppedByShutdown(ExecScheduler(run), run.Err() != nil, err) {
		return &types.OperationError{
			Kind:    types.KindShutdown,
			Op:      "scheduler.Submit",
			Message: "the scheduler was closed while the stream was open",
			Cause:   err,
		}
	}
	return err
}

// runStream drains the provider's event stream into s.items, translating
// llm.StreamChunk (mode 2, provider events) into plain text deltas (mode 1,
// what TextStream promises) and applying the same completion classifier the
// buffered path uses before ever calling a Done chunk a success.
func runStream(
	ctx context.Context,
	s *TextStream,
	streamer llm.StreamingProvider,
	req llm.CompletionRequest,
	provider llm.Provider,
	model, responseFormat string,
	opts types.OpOptions,
	tracking requesttracking.Metadata,
	start time.Time,
) {
	defer close(s.items)
	// The stream's context is cancelled when the producer is done with it, which
	// releases its timeout timer and its place among the parent's children.
	// Before this, only Cancel, a reader leaving the loop and a refused admission
	// called it, so every stream read to its end held both until the timeout.
	//
	// It runs after the deferred function below that leaves the reason for a
	// stream stopped without a final item, and before the channel is closed.
	// Cancelling here is safe for readers because the outcome is recorded before
	// the producer gets here, and a reader takes what is queued before it looks
	// at the context (see next).
	defer s.cancel()
	// Whoever opened the stream is told of its end on every way out that hands
	// a reader no last item (a reader that left, Cancel, a context that ended,
	// the scheduler closing, a panic). It is declared here so that it runs after
	// the functions below have left the reason for the stop, finished the
	// request and recorded the spend, and before the stream is cancelled and its
	// items are closed, so that it has run by the time a reader sees them end. A
	// stream that does end with a last item has told them already, in finish.
	defer s.ended()
	log := loggerFor(ctx)

	// The request, to the installed observers: started here, where it is made,
	// and finished once on every way out. A stream that ends with a final item
	// is finished as that item is recorded (finish). One that is stopped
	// without one, or whose producer panics, is finished by the function
	// below, which runs after the reason for the stop has been left on the
	// stream; a panic is reported and then continues. The provider is called
	// on the context the observers returned, so what it does is inside the
	// request they opened.
	ctx = s.observed.begin(ctx, provider, model, responseFormat, opts, tracking.RequestID, tracking.CorrelationID, start)
	defer func() {
		if s.observed.ended {
			return
		}
		recovered := recover()
		s.observed.end(s.whyStopped(), recovered != nil)
		if recovered != nil {
			panic(recovered)
		}
	}()

	// What a stream cost is recorded against the client's budget on every way
	// out, as far as it is known.
	//
	// A clean end reports its usage and is recorded with its cost, in
	// recordStreamCompletion. An end that reports usage and cannot be used, a
	// truncated answer for one, is charged for that usage: the tokens were
	// produced. A stream that broke partway, or that the caller stopped
	// reading, produced tokens nobody reported, and is recorded as spend of
	// unknown size, which is what the priced flag is for. A ClientBudget does
	// not count that toward its ceiling, since there is no figure to add; it
	// reports its total as incomplete from then on, so a caller reading Spent
	// knows the number is a floor. The process-wide ledger is told as well, with
	// an entry that has no price, as it is for any other call whose cost is not
	// known; it heard nothing, and a stream a client with no budget abandoned
	// cost money that appeared on no ledger. A stream that failed before
	// anything arrived adds nothing to either ledger, as a failed buffered call
	// adds nothing; its request is in the call record, published by the stream's
	// end (recordSentRequest) with the usage marked as not reported.
	//
	// Before this, only a clean end was recorded at all.
	settled, received := false, false
	defer func() {
		if settled || !received {
			return
		}
		recordUnknownStreamSpend(ctx, tracking, provider, model)
	}()
	// A stream that leaves here without a final item was stopped. The reason
	// is left for a reader who comes to it afterwards; the items channel is
	// closed after this runs.
	defer func() {
		s.resultMu.Lock()
		defer s.resultMu.Unlock()
		if s.stopped != nil {
			return
		}
		// Nothing was left by a final item. If the context this ran under
		// ended, that is why: a cancellation, which stopCause reads as the
		// scheduler's closing when it was.
		if over := contextOver(ctx); over != nil {
			s.stopped = stopCause(ctx, s, over)
		}
	}()

	// From here a request has been sent, whatever becomes of the stream: its call
	// record is published by the path that completes it, or by the stream's end
	// (recordSentRequest) when none did. A context that ended between the stream
	// being opened and this point is not looked at: the provider is called on it
	// as it always was, and whether anything left is its to say, so the request is
	// counted, which is the side that does not hide spend.
	s.sent.Store(true)

	for chunk, err := range streamer.CompleteStream(ctx, req) {
		if err == nil {
			received = true
		}
		if err == nil && chunk.Done {
			s.observed.answer(chunk.Response)
		}
		if err != nil {
			log.Error("LLM stream failed",
				"requestID", tracking.RequestID,
				"correlationID", tracking.CorrelationID,
				"provider", provider.Name(),
				"model", model,
				"duration_ms", time.Since(start).Milliseconds(),
				"error", loggedError(err),
			)
			s.finish(ctx, textStreamItem{final: true, err: stopCause(ctx, s, err)})
			return
		}

		if chunk.Delta != "" && !s.send(ctx, textStreamItem{delta: chunk.Delta}) {
			if chunk.Done {
				// The stream was stopped as its last chunk arrived, and that
				// chunk carries the usage. Returning here recorded the stream
				// as spend of unknown size, when the figure was in hand.
				resp := chunk.Response
				s.claim(textStreamItem{final: true, err: stopCause(ctx, s, stoppedWhileSending(ctx, s))})
				settled = true
				recordStoppedStreamAnswer(ctx, tracking, provider, model, responseFormat, opts, resp, start, streamOutcomeStopped)
				s.recorded.Store(true)
			}
			return
		}

		if chunk.Done {
			resp := chunk.Response
			// LX-126: who answered, as on the buffered path. The deltas have
			// been read by now; what is refused is the stream's result.
			if policyErr := checkAnswerRoute(ctx, provider, model, resp); policyErr != nil {
				log.Error("Refusing the stream: it was answered by a provider the client's data policy does not allow",
					"requestID", tracking.RequestID,
					"correlationID", tracking.CorrelationID,
					"provider", provider.Name(),
					"model", model,
					"error", loggedError(policyErr),
				)
				settled = true
				// The refusal is the stream's outcome unless something stopped the
				// stream first, and the record says which.
				outcome := streamOutcomeRefused
				if !s.claim(textStreamItem{final: true, err: policyErr}) {
					outcome = streamOutcomeStopped
				}
				recordStoppedStreamAnswer(ctx, tracking, provider, model, responseFormat, opts, resp, start, outcome)
				s.recorded.Store(true)
				s.finish(ctx, textStreamItem{final: true, err: policyErr})
				return
			}
			if fErr := classifyFinalChunk(resp); fErr != nil {
				log.Error("LLM stream completed with an unusable response",
					"requestID", tracking.RequestID,
					"correlationID", tracking.CorrelationID,
					"provider", provider.Name(),
					"model", model,
					"finishReason", resp.FinishReason,
					"error", loggedError(fErr),
				)
				// The answer cannot be used and its tokens were still produced:
				// both ledgers are told, as they are on the buffered path.
				if reportsUsage(resp.Usage) {
					settled = true
					chargeBudget(ctx, provider, model, resp)
					trackRefusedAnswer(provider, model, resp, tracking.RequestID, tracking.CorrelationID)
				}
				s.finish(ctx, textStreamItem{final: true, err: fErr})
				return
			}

			settled = true
			// The outcome is decided before anything is charged or published. It
			// was decided after, so a reader that left the loop in between
			// recorded a cancellation while the budget was charged and a record
			// published for a stream that completed, and the two disagreed about
			// what had happened.
			final := textStreamItem{final: true, resp: resp}
			if !s.claim(final) {
				recordStoppedStreamAnswer(ctx, tracking, provider, model, responseFormat, opts, resp, start, streamOutcomeStopped)
				s.recorded.Store(true)
				return
			}
			recordStreamCompletion(ctx, tracking, provider, model, responseFormat, opts, resp, start)
			s.recorded.Store(true)
			s.finish(ctx, final)
			return
		}
	}

	// llm.StreamingProvider promises the sequence always ends in either an
	// error or a Done chunk (see its doc comment). This is the defensive
	// floor if that promise is ever broken by a future provider: a caller
	// must never see the range loop just end with no terminal item at all,
	// which would read as "the stream finished successfully" even though
	// nothing said so.
	s.finish(ctx, textStreamItem{final: true, err: types.Newf(
		types.KindProviderUnavailable, "stream", "provider %q ended its stream with no result", types.Bound(provider.Name(), types.MaxProviderName))})
}

// What a stream's call record says happened to it, under the "outcome" key of
// the record's Custom map.
const (
	streamOutcomeCompleted = "completed" // the answer was delivered
	streamOutcomeRefused   = "refused"   // answered by a route the client's data policy does not allow
	streamOutcomeStopped   = "stopped"   // the stream was cancelled or timed out before its answer was taken
	streamOutcomeFailed    = "failed"    // the provider failed, or its answer was refused as truncated or empty
)

// stoppedWhileSending says why a send failed: the run or the stream was over.
func stoppedWhileSending(run context.Context, s *TextStream) error {
	if over := contextOver(run); over != nil {
		return over
	}
	if over := contextOver(s.ctx); over != nil {
		return over
	}
	return types.Newf(types.KindProviderUnavailable, "stream", "the stream ended before it finished")
}

// streamAsked is a streamed request as it was made: what the call record of a
// stream that ended before it published one says of it. opts holds only the
// settings the record carries, and none of the caller's text.
type streamAsked struct {
	tracking       requesttracking.Metadata
	provider       llm.Provider
	model          string
	responseFormat string
	opts           types.OpOptions
	start          time.Time
}

// streamCallRecord builds the call record of one streamed answer, with the
// outcome the stream ended in.
func streamCallRecord(
	ctx context.Context,
	tracking requesttracking.Metadata,
	provider llm.Provider,
	model, responseFormat string,
	opts types.OpOptions,
	resp llm.CompletionResponse,
	start time.Time,
	outcome string,
) *types.ResultMetadata {
	actualModel := resp.Model
	if actualModel == "" {
		actualModel = model
	}
	actualProvider := resp.Provider
	if actualProvider == "" {
		actualProvider = provider.Name()
	}

	// What the provider reported using is the record's usage. A response that
	// reports none, and was not served from a cache, has not used nothing: it has
	// not said, so the record carries no usage and a cost marked unknown, as the
	// record of a failed buffered call does, and not a priced zero that a total
	// would add up as a measurement.
	usage := resp.Usage
	var tokens *types.TokenUsage
	var cost *types.CostInfo
	if resp.Cached || reportsUsage(usage) {
		tokens = &usage
		cost = pricing.CalculateCostLogged(loggerFor(ctx), &usage, actualModel, actualProvider)
	} else {
		cost = &types.CostInfo{Quality: types.PricingUnknown}
	}
	return &types.ResultMetadata{
		RequestID:     tracking.RequestID,
		CorrelationID: tracking.CorrelationID,
		RetryCount:    0, // streamLLM never retries; see its doc comment.
		TraceID:       telemetry.GetTraceID(ctx),
		SpanID:        telemetry.GetSpanID(ctx),
		StartTime:     start,
		EndTime:       time.Now(),
		Duration:      time.Since(start),
		Model:         actualModel,
		// What was asked for, and what the provider says answered when it said:
		// the pair the drift of a request is read from. Both were left empty, so
		// every streamed answer reported its drift as unknown.
		RequestedModel: model,
		ObservedModel:  resp.Model,
		Provider:       actualProvider,
		Mode:           opts.Mode,
		Intelligence:   opts.Intelligence,
		TokenUsage:     tokens,
		CostInfo:       cost,
		CacheHit:       resp.Cached,
		Hedged:         resp.Hedged,
		ServiceTier:    resp.ServiceTier,
		FinishReason:   resp.FinishReason,
		Custom: map[string]any{
			"response_format": responseFormat,
			"schema_id":       opts.SchemaID,
			"streamed":        true,
			"outcome":         outcome,
		},
	}
}

// recordStoppedStreamAnswer accounts for an answer a stream received and did
// not deliver: one its client's data policy refused, or one that arrived after
// the stream had been stopped. Its tokens were produced, so both ledgers are
// told, and a call record is published that says what became of it. A refused
// stream published no record, so a caller collecting them saw nothing for it.
func recordStoppedStreamAnswer(
	ctx context.Context,
	tracking requesttracking.Metadata,
	provider llm.Provider,
	model, responseFormat string,
	opts types.OpOptions,
	resp llm.CompletionResponse,
	start time.Time,
	outcome string,
) {
	metadata := streamCallRecord(ctx, tracking, provider, model, responseFormat, opts, resp, start, outcome)
	if outcome == streamOutcomeRefused {
		metadata.Custom["refused"] = true
	}
	publishCallRecord(ctx, metadata)
	chargeBudget(ctx, provider, model, resp)
	// An answer with no reported usage has no figure to add, and one served
	// from a cache was not bought: neither is tracked, as trackRefusedAnswer
	// does not track them.
	if !resp.Cached && reportsUsage(resp.Usage) {
		pricing.TrackCost(metadata.CostInfo, metadata)
	}
}

// recordUnknownStreamSpend tells the ledgers that a stream spent something that
// cannot be priced: it was stopped, or broke, after tokens had arrived and
// before their usage was reported. The client's budget hears it when the client
// has one. The process-wide ledger hears it as an entry with no price and no
// amount, which its totals do not count and its records show, rather than as a
// stream that cost nothing.
func recordUnknownStreamSpend(
	ctx context.Context,
	tracking requesttracking.Metadata,
	provider llm.Provider,
	model string,
) {
	if budget := ExecBudget(ctx); budget != nil {
		budget.Record(0, false)
	}
	pricing.TrackCost(&types.CostInfo{Quality: types.PricingUnknown}, &types.ResultMetadata{
		RequestID:     tracking.RequestID,
		CorrelationID: tracking.CorrelationID,
		EndTime:       time.Now(),
		Model:         model,
		Provider:      provider.Name(),
		Custom:        map[string]any{"outcome": streamOutcomeStopped, "spend": "unknown"},
	})
}

// recordStreamCompletion publishes the same accounting a buffered call
// records in CallLLM -- usage, cost, the call-record envelope, telemetry --
// for a streamed call's single (non-retried) attempt.
func recordStreamCompletion(
	ctx context.Context,
	tracking requesttracking.Metadata,
	provider llm.Provider,
	model, responseFormat string,
	opts types.OpOptions,
	resp llm.CompletionResponse,
	start time.Time,
) {
	log := loggerFor(ctx)

	metadata := streamCallRecord(ctx, tracking, provider, model, responseFormat, opts, resp, start, streamOutcomeCompleted)
	actualModel, actualProvider := metadata.Model, metadata.Provider
	usage := resp.Usage
	cost := metadata.CostInfo

	publishCallRecord(ctx, metadata)
	// A client that has its own budget is charged for its streams too. The
	// check above read that budget; without this, nothing a stream cost was
	// ever recorded against it.
	chargeBudget(ctx, provider, model, resp)
	// An answer served from a cache was not bought, here as on the buffered
	// path.
	if !resp.Cached {
		pricing.TrackCost(cost, metadata)
	}
	telemetry.RecordLLMMetrics(metadata)

	// A stream that went well is a debug line, as a request that went well is in
	// CallLLM, and its arguments are not built unless somebody asked for it.
	if log.Enabled(ctx, slog.LevelDebug) {
		log.Debug("LLM stream completed",
			"requestID", tracking.RequestID,
			"correlationID", tracking.CorrelationID,
			"provider", actualProvider,
			"model", actualModel,
			"responseFormat", responseFormat,
			"duration_ms", metadata.Duration.Milliseconds(),
			"tokens_total", usage.TotalTokens,
			"cost_usd", cost.TotalCost,
			"finishReason", resp.FinishReason,
		)
	}
}

// streamContext is the context a stream runs on. It is the one the options
// resolve to for a buffered call: CommonOptions first, with the caller's
// timeout applied, then the embedded OpOptions.
//
// The Stream functions passed the embedded OpOptions' context alone. A context
// set through CommonOptions, which is where WithContext and the fluent builders
// put it, never reached streamLLM, so the client on it was ignored, a cancelled
// one still opened a stream, and a timeout set there was dropped.
func streamContext(resolved types.OpOptions, common CommonOptions, embedded types.OpOptions) context.Context {
	// toOpOptions always resolves a context: the caller's, from either home,
	// or a background one, with the caller's timeout applied. The second
	// branch is for an options value that did not come through it.
	if resolved.Context != nil {
		return resolved.Context
	}
	return resolvedContext(common, embedded)
}

// StreamSummarize streams Summarize's answer. See Summarize
// (internal/ops/text.go) for the operation; it sends the identical system and
// user prompt and calls streamLLM instead of callLLM. The instruction lists and
// the fixed system prompts are shared with text.go (the unexported instruction
// builders and the constants beside the buffered forms), and
// TestStreamingPromptsMatchTheBufferedOnes compares what reaches the provider.
func StreamSummarize(input string, opts SummarizeOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), summarizeInstructions(opts))

	systemPrompt := summarizeSystemPrompt

	userPrompt := "Summarize this text:\n" + quotedText(input)

	return streamLLM(streamContext(opt, opts.CommonOptions, opts.OpOptions), systemPrompt, userPrompt, opt)
}

// StreamRewrite streams Rewrite's answer. See Rewrite (internal/ops/text.go).
func StreamRewrite(input string, opts RewriteOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), rewriteInstructions(opts))

	systemPrompt := rewriteSystemPrompt

	userPrompt := "Rewrite this text:\n" + quotedText(input)

	return streamLLM(streamContext(opt, opts.CommonOptions, opts.OpOptions), systemPrompt, userPrompt, opt)
}

// StreamTranslate streams Translate's answer. See Translate
// (internal/ops/text.go).
func StreamTranslate(input string, opts TranslateOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), translateInstructions(opts))

	systemPrompt := translateSystemPrompt

	userPrompt := "Translate this text:\n" + quotedText(input)

	return streamLLM(streamContext(opt, opts.CommonOptions, opts.OpOptions), systemPrompt, userPrompt, opt)
}

// StreamExpand streams Expand's answer. See Expand (internal/ops/text.go).
func StreamExpand(input string, opts ExpandOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), expandInstructions(opts))

	systemPrompt := expandSystemPrompt

	userPrompt := "Expand on this text:\n" + quotedText(input)

	return streamLLM(streamContext(opt, opts.CommonOptions, opts.OpOptions), systemPrompt, userPrompt, opt)
}

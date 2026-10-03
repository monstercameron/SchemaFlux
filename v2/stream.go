package schemaflux

import (
	"context"
	"iter"
	"sync"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// TextStream is a streamed answer: text as the model produces it. Range over All
// to read it, or call Text to wait for the whole of it. Stopping the range stops
// the request unless Detach was called. Every error it reports is an *Error.
//
// A stream runs under the context it was opened with, and ends when that context
// does. A context with no deadline gets the client's Timeout, as any call does.
// A stream nobody reads keeps its request and its goroutine until it is
// cancelled or its context or its Timeout ends it: call Cancel, or range to the
// end, even when the text is not wanted.
//
// Pieces come in the order the model wrote them, so appending each one to a
// buffer rebuilds the answer. A stream has one reader at a time.
//
// The Record of a stream is added when it ends, however it ends: read to the
// end, stopped by its reader, cancelled, failed, refused as a truncated or an
// empty answer, or left unread until its context ended. A stream that ends by
// itself (the answer is whole, the provider failed, the answer is refused) gives
// the recorders its Record before the last item reaches the reader, so the Record
// is there when the loop over All ends and when Text returns. A stream stopped
// from outside (Cancel, the reader leaving the loop, its context ending) ends
// when its own goroutine notices, at once for a provider that stops when its
// context does; a reader waiting on it is released first, because the stream does
// not wait for a provider that ignores its context, so the Record of such a
// stream can arrive just after the loop ends. A call that could not open a stream
// (a floor above prompt only, a bad option, a context already over) is a Record
// of its own, with no request sent.
//
// A Record counts the request of a stream only when the provider's last chunk
// arrived: a stream that failed or was cut off before it has Requests zero and no
// Provider.
//
// A TextStream wraps the stream of the call path and has six methods: the call
// path's own stream has more, which are not part of this type.
type TextStream struct {
	in *ops.TextStream

	// op names the call in the errors the stream reports.
	op string

	mu sync.Mutex
	// failure is the stream's error as the root declares it, made from the first
	// one the call path reported. A stream has one outcome, so one conversion
	// serves every reader and every method.
	failure error
}

// streamOf is the stream a Stream function returns: the root's TextStream over
// in, or the error as it is, already converted and already a Record, when the
// stream was not opened.
func streamOf(in *ops.TextStream, op string, err error) (*TextStream, error) {
	if err != nil {
		return nil, err
	}
	return &TextStream{in: in, op: op}, nil
}

// report is err as the root declares it. It is the same *Error every time it is
// asked, whichever reader asks.
func (s *TextStream) report(err error) error {
	if err == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = bind.ConvertError(s.op, err)
	}
	return s.failure
}

// All returns the iterator over the text. Ranging over it gives each piece as it
// arrives and a nil error, and the loop ends when the stream does. A stream that
// failed, was cancelled or ended with its context gives one last pair, an empty
// piece and the *Error that Err reports.
//
// A stream has one outcome and keeps it: once it has ended, ranging again gives
// no more text and, if it ended in an error, that error, and Text gives the
// whole text or the error however often it is called.
//
// A reader that leaves the loop, by breaking out of it or by panicking out of
// it, stops the stream, unless Detach was called first. A range waiting for the
// model is released by Cancel and by the stream's context ending.
func (s *TextStream) All() iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for piece, err := range s.in.All() {
			if !yield(piece, s.report(err)) {
				return
			}
		}
	}
}

// Text waits for the stream to end and returns the whole answer, the pieces All
// would have given joined, and no more than one request was made for it. A
// stream that did not finish gives an empty string and the *Error that Err
// reports.
func (s *TextStream) Text() (string, error) {
	text, err := s.in.Text()
	return text, s.report(err)
}

// Err reports how the stream ended, as soon as it has ended and whether or not
// anything has read it: nil for a stream that finished, and the *Error of the
// failure, of the cancellation, or of the context that ended it, otherwise. It is
// also nil for a stream that has not ended; Ended tells the two apart.
func (s *TextStream) Err() error { return s.report(s.in.Err()) }

// Ended reports whether the stream has ended: it finished, failed or was
// stopped, whether or not anything has read it yet. A stream with text still to
// be read can have ended. Err then says how.
func (s *TextStream) Ended() bool { return s.in.Ended() }

// Cancel stops the stream now, whether or not Detach was called, and releases a
// reader waiting on it. The stream's error from then on is of kind ErrCanceled,
// unless the stream had already ended some other way. It is safe to call after
// the stream has ended, and from any goroutine.
func (s *TextStream) Cancel() { s.in.Cancel() }

// Detach keeps the request running when the reader breaks out of the loop over
// All, which would otherwise stop it. Use it to let an answer finish after its
// reader has had enough of the pieces, to warm a cache or to keep the whole text.
// After the loop is left the stream reads itself to its end, and Text gives the
// whole answer; ranging again gives no text, and waits for the end. It has an
// effect only when it is called before the loop is left, and may be called from
// any goroutine.
func (s *TextStream) Detach() { s.in.Detach() }

// StreamSummarize is Summarize delivered as it arrives. It is a different call
// from Summarize in what it checks: TargetLength is a request, and nothing is
// measured. It sends the request Summarize sends, as a stream, and makes no
// retry: a stream that fails midway has shown text already, and is an error.
//
//	stream, err := schemaflux.StreamSummarize(ctx, article)
//	for piece, err := range stream.All() { ... }
//
// The Record of the stream is "streamsummarize", added when the stream ends. A
// stream delivers the model's text and nothing is read from it, so it is
// delivered at ContractPromptOnly: a client whose DataPolicy sets any floor
// refuses the call with ErrPolicy before anything is sent. The provider has to
// stream (provider.Streamer, as the built-in providers do): the first read of a
// stream on one that does not is an error of kind ErrUnsupported. The wrappers
// of v2/mw do not forward streams, so a client with a wrapper in front reports
// that same error.
func StreamSummarize(ctx context.Context, text string, opts ...SummarizeOptions) (*TextStream, error) {
	const op = "streamsummarize"
	call, err := bind.Begin(ctx, op)
	if err != nil {
		return nil, err
	}
	o := mergeSummarizeOptions(opts)
	if err := checkSummarizeOptions(op, o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		in, err := ops.StreamSummarize(text, summarizeInternal(call.Context(), o))
		return streamOf(in, op, call.End(nil, err))
	}
	in, err := bind.Stream(&call, types.ContractPromptOnly, func(ctx context.Context) (*ops.TextStream, error) {
		return ops.StreamSummarize(text, summarizeInternal(ctx, o))
	})
	return streamOf(in, op, err)
}

// StreamRewrite is Rewrite delivered as it arrives. AvoidWords and IncludeWords
// are not checked on a stream. It sends the request Rewrite sends, as a stream,
// and makes no retry.
//
//	stream, err := schemaflux.StreamRewrite(ctx, note, schemaflux.RewriteOptions{Tone: "formal"})
//
// The Record of the stream is "streamrewrite", added when the stream ends, and
// the stream is delivered at ContractPromptOnly: a client whose DataPolicy sets
// any floor refuses the call with ErrPolicy before anything is sent. The first
// read of a stream on a provider that does not stream is an error of kind
// ErrUnsupported, and the wrappers of v2/mw do not forward streams, so a client
// with a wrapper in front reports that same error.
func StreamRewrite(ctx context.Context, text string, opts ...RewriteOptions) (*TextStream, error) {
	const op = "streamrewrite"
	call, err := bind.Begin(ctx, op)
	if err != nil {
		return nil, err
	}
	o := mergeRewriteOptions(opts)
	if err := checkRewriteOptions(op, o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		in, err := ops.StreamRewrite(text, rewriteInternal(call.Context(), o))
		return streamOf(in, op, call.End(nil, err))
	}
	in, err := bind.Stream(&call, types.ContractPromptOnly, func(ctx context.Context) (*ops.TextStream, error) {
		return ops.StreamRewrite(text, rewriteInternal(ctx, o))
	})
	return streamOf(in, op, err)
}

// StreamTranslate is Translate delivered as it arrives. The Glossary is not
// enforced on a stream. A blank language is an error of kind ErrInvalidRequest.
// It sends the request Translate sends, as a stream, and makes no retry.
//
//	stream, err := schemaflux.StreamTranslate(ctx, text, "Spanish")
//
// The Record of the stream is "streamtranslate", added when the stream ends, and
// the stream is delivered at ContractPromptOnly: a client whose DataPolicy sets
// any floor refuses the call with ErrPolicy before anything is sent. The first
// read of a stream on a provider that does not stream is an error of kind
// ErrUnsupported, and the wrappers of v2/mw do not forward streams, so a client
// with a wrapper in front reports that same error.
func StreamTranslate(ctx context.Context, text, language string, opts ...TranslateOptions) (*TextStream, error) {
	const op = "streamtranslate"
	call, err := bind.Begin(ctx, op)
	if err != nil {
		return nil, err
	}
	o := mergeTranslateOptions(opts)
	if err := refuseBlank(&call, "language", language); err != nil {
		return nil, err
	}
	if err := checkTranslateOptions(op, o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		in, err := ops.StreamTranslate(text, translateInternal(call.Context(), language, o))
		return streamOf(in, op, call.End(nil, err))
	}
	in, err := bind.Stream(&call, types.ContractPromptOnly, func(ctx context.Context) (*ops.TextStream, error) {
		return ops.StreamTranslate(text, translateInternal(ctx, language, o))
	})
	return streamOf(in, op, err)
}

// StreamExpand is Expand delivered as it arrives. Factor is a request, and
// nothing is measured. It sends the request Expand sends, as a stream, and makes
// no retry.
//
//	stream, err := schemaflux.StreamExpand(ctx, outline)
//
// The Record of the stream is "streamexpand", added when the stream ends, and the
// stream is delivered at ContractPromptOnly: a client whose DataPolicy sets any
// floor refuses the call with ErrPolicy before anything is sent. The first read
// of a stream on a provider that does not stream is an error of kind
// ErrUnsupported, and the wrappers of v2/mw do not forward streams, so a client
// with a wrapper in front reports that same error.
func StreamExpand(ctx context.Context, text string, opts ...ExpandOptions) (*TextStream, error) {
	const op = "streamexpand"
	call, err := bind.Begin(ctx, op)
	if err != nil {
		return nil, err
	}
	o := mergeExpandOptions(opts)
	if err := checkExpandOptions(op, o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		in, err := ops.StreamExpand(text, expandInternal(call.Context(), o))
		return streamOf(in, op, call.End(nil, err))
	}
	in, err := bind.Stream(&call, types.ContractPromptOnly, func(ctx context.Context) (*ops.TextStream, error) {
		return ops.StreamExpand(text, expandInternal(ctx, o))
	})
	return streamOf(in, op, err)
}

package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// MapReduceOptions configures a chunked run. The zero value is the defaults.
// Several values passed to MapReduce are merged left to right, a later field
// that is not zero winning.
type MapReduceOptions struct {
	// ChunkSize is the number of items given to the operation at a time. Zero
	// means 25, which is small enough that the echoed result of a chunk fits the
	// output ceiling of every tier. A size larger than the number of items is
	// one chunk of all of them, whatever its size, math.MaxInt included, which is
	// how to ask for no chunking. A negative value is an error of kind
	// ErrConfiguration: a chunk of no items is not one.
	ChunkSize int

	// Concurrency is how many chunks may be in flight at once. Zero means 4, which
	// is modest on purpose: the limit is usually the provider's rate limit and not
	// the machine. One makes the run sequential, which is what a caller wants when
	// the provider is rate limited. More than there are chunks is as many as there
	// are chunks. A negative value is an error of kind ErrConfiguration.
	Concurrency int

	// ContinueOnError keeps going when a chunk fails and reports the failures
	// beside whatever succeeded. False, the default, stops at the first failure,
	// because a partial result presented as a whole one is the failure this
	// library exists to remove.
	ContinueOnError bool
}

// A ChunkError says which chunk failed and why.
type ChunkError struct {
	// Index is the position of the chunk, counting from zero.
	Index int

	// Offset is the position of the chunk's first item in the input, which is
	// what a caller needs to know which of their items are missing.
	Offset int

	// Err is why. It is the error the caller's operation returned, the same
	// value, when that is what failed the chunk. When the library failed the chunk
	// (the operation panicked, or the run was stopped before the chunk started) it
	// is an *schemaflux.Error.
	Err error
}

// Error returns the position of the chunk and the error that failed it.
func (e ChunkError) Error() string {
	return fmt.Sprintf("chunk %d (items %d onward): %v", e.Index, e.Offset, e.Err)
}

// Unwrap returns Err.
func (e ChunkError) Unwrap() error { return e.Err }

// MapReduceResult reports what a chunked run did. A caller who asked to continue
// on error needs it to know which items are missing before using the results.
type MapReduceResult struct {
	// Chunks is the number of chunks the input was split into. Zero for an input
	// of no items.
	Chunks int

	// Failed lists the chunks that did not succeed, in the order of their index.
	// It is empty on a clean run. When the run stopped at the first failure it
	// lists that chunk and the ones that were stopped or never started because of
	// it, as cancelled.
	Failed []ChunkError
}

// Complete reports whether every chunk succeeded.
func (r MapReduceResult) Complete() bool { return len(r.Failed) == 0 }

// MapReduce splits items into chunks, runs operation on each, and returns the
// per-chunk results in input order.
//
//	summaries, run, err := flow.MapReduce(ctx, documents, func(ctx context.Context, chunk []Document) (string, error) {
//		return schemaflux.Summarize(ctx, join(chunk))
//	}, flow.MapReduceOptions{ChunkSize: 10, Concurrency: 2})
//
// What runs at once: the chunks are started in order, at most Concurrency of
// them running at any moment (4 unless the options say otherwise), each on a
// goroutine of its own, under ctx; so the Scheduler of the client on it bounds
// the requests they make. Concurrency 1 is sequential and not merely
// serialized. MapReduce returns when every chunk that started has returned, and
// no goroutine outlives it. Chunks overlap, so an operation that calls a provider
// through a client with a budget, which is checked as each call starts, can
// overshoot that budget by up to Concurrency less one calls that were already in
// flight when it ran out; with a Concurrency of one it cannot.
//
// Order is preserved whatever order the chunks finish in, because a caller
// merging a Sort or a Filter needs the input's order to mean something.
//
// Without ContinueOnError, the first chunk to fail stops the run: the chunks in
// flight are cancelled, none after them starts, and the error returned is that
// failure, the first in time and not the lowest by index, as the operation
// returned it: the same value. The cancellation it caused in the other chunks is
// never returned in its place. The MapReduceResult lists every chunk that did not
// succeed, so which chunk it was is found there. No results are returned with an
// error.
//
// With ContinueOnError the results hold only the chunks that succeeded, in chunk
// order: they are fewer than the chunks when any failed, so result k is not chunk
// k. The caller rebuilds the positions from MapReduceResult.Failed, whose
// entries give each failed chunk's index and offset. A caller who cancels their
// own context gets the *Error for the context and no results, and not a partial
// result: ContinueOnError is about chunks that fail, and a run that was stopped
// did not finish. Each ChunkError's Err is the operation's own error as it was
// returned, or an *Error when the library failed the chunk.
//
// An operation that panics, or ends its goroutine with runtime.Goexit, fails its
// chunk with an *Error of kind ErrInternal that carries nothing of the panic, and
// does not stop the process. A nil operation and a nil ctx are errors of kind
// ErrInvalidRequest; a negative ChunkSize or Concurrency is one of kind
// ErrConfiguration; and a ctx that is already done is one of kind ErrCanceled or
// ErrTimeout; nothing runs for any of them. No items give no results, no
// chunks and a nil error. MapReduce calls no model itself.
func MapReduce[T, R any](ctx context.Context, items []T, operation func(context.Context, []T) (R, error), opts ...MapReduceOptions) ([]R, MapReduceResult, error) {
	if err := begin(ctx, "flow.mapreduce"); err != nil {
		return nil, MapReduceResult{}, err
	}
	if operation == nil {
		return nil, MapReduceResult{}, refuse("flow.mapreduce", "there is no operation to run on the chunks")
	}
	var merged MapReduceOptions
	for _, o := range opts {
		if o.ChunkSize != 0 {
			merged.ChunkSize = o.ChunkSize
		}
		if o.Concurrency != 0 {
			merged.Concurrency = o.Concurrency
		}
		if o.ContinueOnError {
			merged.ContinueOnError = true
		}
	}
	switch {
	case merged.ChunkSize < 0:
		return nil, MapReduceResult{}, misconfigured("flow.mapreduce", "ChunkSize must not be negative")
	case merged.Concurrency < 0:
		return nil, MapReduceResult{}, misconfigured("flow.mapreduce", "Concurrency must not be negative")
	}

	results, summary, err := ops.MapReduce(ctx, items, ops.MapReduceOptions{
		ChunkSize:       merged.ChunkSize,
		Concurrency:     merged.Concurrency,
		ContinueOnError: merged.ContinueOnError,
	}, func(ctx context.Context, chunk []T) (R, error) {
		value, err := operation(ctx, chunk)
		if err != nil {
			return value, own(err)
		}
		return value, nil
	})

	report := MapReduceResult{Chunks: summary.Chunks}
	for _, failed := range summary.Failed {
		report.Failed = append(report.Failed, ChunkError{
			Index:  failed.Index,
			Offset: failed.Offset,
			Err:    end("flow.mapreduce", failed.Err),
		})
	}
	if err != nil {
		// The failure that stopped the run comes as a chunk's, and is returned as
		// what it was: the operation's own error, or the library's.
		var chunk ops.ChunkError
		if errors.As(err, &chunk) {
			err = chunk.Err
		}
		return nil, report, end("flow.mapreduce", err)
	}
	return results, report, nil
}

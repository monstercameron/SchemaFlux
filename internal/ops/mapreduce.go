package ops

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/types"
)

// There was no chunk → per-chunk operation → merge primitive. BatchProcessor
// chunks, but only for Extract and only internally, so the collection
// operations could not handle a collection larger than one context window and
// no caller could build around it. That is the direct cause of C-06: Sort and
// Filter have to echo every item back, and the output budget is a few thousand
// tokens.
//
// MapReduce is the missing piece. It is deliberately generic rather than baked
// into the collection operations: chunking a Sort correctly needs a merge that
// understands ordering, and that is the caller's knowledge, not the library's.

// MapReduceOptions configures a chunked run.
type MapReduceOptions struct {
	// ChunkSize is the number of items per call. Zero means DefaultChunkSize.
	ChunkSize int

	// Concurrency is how many chunks may be in flight. Zero means
	// DefaultConcurrency. One makes the run sequential, which is what a caller
	// wants when the provider is rate-limited.
	Concurrency int

	// ContinueOnError keeps going when a chunk fails, reporting the failures
	// alongside whatever succeeded. The default is to stop, because a partial
	// result presented as a whole one is the failure mode this library exists
	// to remove.
	ContinueOnError bool
}

const (
	// DefaultChunkSize is small enough that a chunk's echoed result fits the
	// output budget of every tier, which is the constraint that matters.
	DefaultChunkSize = 25

	// DefaultConcurrency is modest on purpose: the limit here is usually the
	// provider's rate limit, not the machine.
	DefaultConcurrency = 4
)

// ChunkError reports which chunk failed and why.
type ChunkError struct {
	// Index is the position of the chunk, counting from zero.
	Index int

	// Offset is the position of the chunk's first item in the input, which is
	// what a caller needs to know which of their items are missing.
	Offset int

	Err error
}

func (e ChunkError) Error() string {
	return fmt.Sprintf("chunk %d (items %d onward): %v", e.Index, e.Offset, e.Err)
}

func (e ChunkError) Unwrap() error { return e.Err }

// MapReduceResult reports what a chunked run did. A caller who asked to
// continue on error needs to know which items are missing before they use the
// result.
type MapReduceResult struct {
	// Chunks is the number of chunks the input was split into.
	Chunks int

	// Failed lists the chunks that did not succeed. It is empty on a clean run,
	// and non-empty only when ContinueOnError was set.
	Failed []ChunkError
}

// Complete reports whether every chunk succeeded.
func (r MapReduceResult) Complete() bool { return len(r.Failed) == 0 }

// MapReduce splits items into chunks, runs operation on each, and returns the
// per-chunk results in input order.
//
// Order is preserved regardless of the order chunks finish in, because a caller
// merging a Sort or a Filter needs the input's order to mean something.
//
// With ContinueOnError the returned slice holds only the chunks that succeeded,
// in chunk order: it is shorter than the number of chunks when any failed, so
// result k is not chunk k. The caller rebuilds the positions from
// MapReduceResult.Failed, whose entries give each failed chunk's index and
// offset. A caller that cancels its own context gets the context's error, not a
// partial result: ContinueOnError is about chunks that fail, and a run that was
// stopped did not finish.
//
// Without ContinueOnError the first chunk to fail stops the run: the chunks in
// flight are cancelled, none after them starts, and the error returned is that
// failure, the first in time and not the lowest by index, as a ChunkError. The
// cancellation it caused in the others is never reported in its place.
//
// Chunks overlap, up to Concurrency of them, so the operation's calls are
// started before the first of them has been paid for. An operation that calls a
// provider through a client with a budget, which is checked as each call
// starts, can overshoot that budget by up to Concurrency less one calls that
// were already in flight when it ran out; with a Concurrency of one it cannot.
//
// A nil ctx is the background context, and an operation that panics fails its
// chunk (with an error that carries nothing of the panic) and does not stop the
// process.
func MapReduce[T any, R any](
	ctx context.Context,
	items []T,
	opts MapReduceOptions,
	operation func(ctx context.Context, chunk []T) (R, error),
) ([]R, MapReduceResult, error) {

	var summary MapReduceResult

	if operation == nil {
		return nil, summary, types.Newf(types.KindInvalidRequest, "mapreduce", "no operation given")
	}
	if len(items) == 0 {
		return nil, summary, nil
	}
	ctx = flowContext(ctx)

	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	// A chunk cannot hold more items than there are, and nothing below is sized
	// by a number the caller gave that is larger than that.
	chunkSize = min(chunkSize, len(items))
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}

	chunks := Chunk(items, chunkSize)
	summary.Chunks = len(chunks)

	results := make([]R, len(chunks))

	// The chunks run on the pool the batch functions share: a bounded set of
	// workers handing out chunk indices in order, so that Concurrency: 1 is
	// sequential and not merely serialized, and a failure leaves the chunks after
	// it unstarted instead of racing to be the ones that were. Without
	// ContinueOnError the first failure cancels the run.
	//
	// A chunk's operation that ends its goroutine without returning
	// (runtime.Goexit, as t.FailNow does) is an error for its chunk, and the pool
	// replaces the worker so that the chunks after it still run when the run goes
	// on.
	ran := runBounded(ctx, "mapreduce", len(chunks), boundedOptions{workers: concurrency, stopOnFailure: !opts.ContinueOnError},
		func(runCtx context.Context, index int) error {
			result, err := runChunk(runCtx, operation, chunks[index], index)
			if err != nil {
				return err
			}
			results[index] = result
			return nil
		})

	// A chunk that never started must not fall out of the loop below with a nil
	// error and a zero-valued result, which would report it as a successful empty
	// chunk. It is reported as cancelled, with the reason the run ended when the
	// caller's context was it.
	var successful []R
	for i := range chunks {
		err := ran.errs[i]
		if err == nil && !ran.started[i] {
			err = stoppedBy(ctx)
		}
		if err != nil {
			summary.Failed = append(summary.Failed, ChunkError{
				Index:  i,
				Offset: i * chunkSize,
				Err:    err,
			})
			continue
		}
		successful = append(successful, results[i])
	}

	// The caller gave up. Chunks that were never run are in Failed as cancelled,
	// and returning no error beside them would present a run that was stopped
	// as one that finished.
	if callerErr := ctx.Err(); callerErr != nil && len(summary.Failed) > 0 && opts.ContinueOnError {
		return nil, summary, contextEnded(callerErr)
	}

	if len(summary.Failed) > 0 && !opts.ContinueOnError {
		// Report the failure that stopped the run: the first in time, and never
		// the cancellation it caused in the chunks that were in flight. The first
		// chunk by index is often one that was cancelled because a later chunk
		// failed, and telling the caller "context canceled" hides the thing they
		// need to fix. When no chunk ran, because the caller's context was over
		// before the first, the first chunk stands for the run.
		for _, failure := range summary.Failed {
			if failure.Index == ran.firstStep {
				return nil, summary, failure
			}
		}
		return nil, summary, summary.Failed[0]
	}

	return successful, summary, nil
}

// runChunk runs the operation on one chunk and turns a panic into an error.
// Nothing the panic carried is kept: the recovered value can hold the chunk's
// items, and the error is one every caller logs.
func runChunk[T any, R any](
	ctx context.Context,
	operation func(ctx context.Context, chunk []T) (R, error),
	chunk []T,
	index int,
) (result R, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			var zero R
			result = zero
			// A recovered panic is the library's own invariant ("an operation
			// returns") broken, marked as one so that a boundary that tells it
			// from an answer that failed its checks can. It had no kind.
			err = &types.OperationError{
				Kind:    types.KindInvariantViolation,
				Narrow:  types.NarrowPanic,
				Op:      "mapreduce",
				Message: fmt.Sprintf("the operation panicked on chunk %d", index),
			}
		}
	}()
	return operation(ctx, chunk)
}

// MapReduceFlat is MapReduce for the common case where each chunk produces a
// slice and the caller wants them concatenated in input order.
func MapReduceFlat[T any, R any](
	ctx context.Context,
	items []T,
	opts MapReduceOptions,
	operation func(ctx context.Context, chunk []T) ([]R, error),
) ([]R, MapReduceResult, error) {

	perChunk, summary, err := MapReduce(ctx, items, opts, operation)
	if err != nil {
		return nil, summary, err
	}

	var flattened []R
	for _, chunk := range perChunk {
		flattened = append(flattened, chunk...)
	}
	return flattened, summary, nil
}

// Chunk splits a slice into runs of at most size. The final chunk holds the
// remainder. A size of zero or less returns the whole slice as one chunk rather
// than looping forever.
//
// The size is cut to the length of the slice before anything is computed from
// it, so a size of any magnitude, math.MaxInt included, means one chunk and
// neither the number of chunks nor an offset is a sum that can wrap.
func Chunk[T any](items []T, size int) [][]T {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 || size >= len(items) {
		return [][]T{items}
	}

	chunks := make([][]T, 0, (len(items)-1)/size+1)
	for start := 0; start < len(items); {
		end := start + min(size, len(items)-start)
		chunks = append(chunks, items[start:end])
		start = end
	}
	return chunks
}

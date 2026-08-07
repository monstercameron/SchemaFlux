package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
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
func MapReduce[T any, R any](
	ctx context.Context,
	items []T,
	opts MapReduceOptions,
	operation func(ctx context.Context, chunk []T) (R, error),
) ([]R, MapReduceResult, error) {

	var summary MapReduceResult

	if operation == nil {
		return nil, summary, fmt.Errorf("mapreduce: no operation given")
	}
	if len(items) == 0 {
		return nil, summary, nil
	}

	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}

	chunks := Chunk(items, chunkSize)
	summary.Chunks = len(chunks)

	results := make([]R, len(chunks))
	errs := make([]error, len(chunks))

	// A cancellable sub-context, so the first failure can stop the rest when
	// the caller has not asked to continue.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Every chunk starts as "not run". A chunk the feeder never dispatches --
	// because an earlier one failed and cancelled the run -- must not fall out
	// of the loop below with a nil error and a zero-valued result, which would
	// report it as a successful empty chunk.
	errNotRun := errors.New("chunk was not run: an earlier chunk failed")
	for i := range errs {
		errs[i] = errNotRun
	}

	// A bounded worker pool fed in index order, rather than one goroutine per
	// chunk racing for a semaphore.
	//
	// The semaphore version dispatched in whatever order the scheduler woke the
	// goroutines, so `Concurrency: 1` was serialized but not sequential, and the
	// documented reason for asking for it -- a rate-limited provider -- got
	// chunk 7 before chunk 0. Worse, cancelling on the first failure saved
	// nothing when the failing chunk happened to be scheduled last: every other
	// chunk had already been paid for. That is what the test that caught this
	// was asserting, and it only passed when the scheduler was kind.
	workers := concurrency
	if workers > len(chunks) {
		workers = len(chunks)
	}

	indices := make(chan int)
	var wg sync.WaitGroup

	go func() {
		defer close(indices)
		for i := range chunks {
			select {
			case indices <- i:
			case <-runCtx.Done():
				return
			}
		}
	}()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range indices {
				if runCtx.Err() != nil {
					return
				}

				result, err := operation(runCtx, chunks[index])
				if err != nil {
					errs[index] = err
					if !opts.ContinueOnError {
						cancel()
						return
					}
					continue
				}
				results[index] = result
				errs[index] = nil
			}
		}()
	}

	wg.Wait()

	// Whatever the feeder did not reach is reported as cancelled rather than as
	// the sentinel, which is an implementation detail.
	for i, err := range errs {
		if errors.Is(err, errNotRun) {
			if runCtx.Err() != nil {
				errs[i] = runCtx.Err()
			} else {
				errs[i] = context.Canceled
			}
		}
	}

	var successful []R
	for i, err := range errs {
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

	sort.SliceStable(summary.Failed, func(a, b int) bool {
		return summary.Failed[a].Index < summary.Failed[b].Index
	})

	if len(summary.Failed) > 0 && !opts.ContinueOnError {
		// Report the failure that caused the run to stop, not the cancellation
		// it triggered. The first chunk by index is often one that was
		// cancelled because a later chunk failed, and telling the caller
		// "context canceled" hides the thing they need to fix.
		return nil, summary, rootFailure(summary.Failed)
	}

	return successful, summary, nil
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
func Chunk[T any](items []T, size int) [][]T {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 {
		return [][]T{items}
	}

	chunks := make([][]T, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[start:end])
	}
	return chunks
}

// rootFailure picks the failure worth reporting: the first that is not a
// cancellation, falling back to the first of any kind.
func rootFailure(failures []ChunkError) ChunkError {
	for _, failure := range failures {
		if !errors.Is(failure.Err, context.Canceled) && !errors.Is(failure.Err, context.DeadlineExceeded) {
			return failure
		}
	}
	return failures[0]
}

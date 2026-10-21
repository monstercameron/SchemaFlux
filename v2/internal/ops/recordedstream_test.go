package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// streamEnded is what the end function of RecordedStream was told.
type streamEnded struct {
	mu    sync.Mutex
	calls int
	meta  types.Meta
	err   error
}

func (e *streamEnded) fn(meta types.Meta, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.meta, e.err = meta, err
}

func (e *streamEnded) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// openEndStream is the open function of a recorded stream on provider: it
// opens from the context it is handed, as an operation's options would.
func openEndStream(provider llm.Provider, opened *int) func(context.Context) (*TextStream, error) {
	return func(ctx context.Context) (*TextStream, error) {
		if opened != nil {
			*opened++
		}
		return streamLLM(WithProvider(ctx, provider), "system", "user", types.OpOptions{Model: "end-model"})
	}
}

func TestRecordedStreamReportsTheWholeStreamWhenItIsReadToTheEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pause := func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
			time.Sleep(25 * time.Millisecond)
			endCompletes("one ", "two")(context.Background(), yield)
		}
		var ended streamEnded
		stream, err := RecordedStream(context.Background(), "streamsummarize", types.ContractJSONWellFormed,
			openEndStream(&endStreamProvider{run: pause}, nil), ended.fn)
		if err != nil {
			t.Fatalf("RecordedStream: %v", err)
		}
		text, err := stream.Text()
		if err != nil || text != endAnswer {
			t.Fatalf("Text = %q, %v", text, err)
		}
		if got := ended.count(); got != 1 {
			t.Fatalf("end ran %d times when Text returned, want 1", got)
		}
		meta := ended.meta
		if ended.err != nil {
			t.Errorf("end was told %v for a stream that completed", ended.err)
		}
		if meta.Operation != "streamsummarize" || meta.Provider != "endstream" || meta.Model != "end-model-served" {
			t.Errorf("identity = %q %q %q", meta.Operation, meta.Provider, meta.Model)
		}
		if meta.Usage.TotalTokens != 12 || meta.Attempts != 1 {
			t.Errorf("usage = %d tokens in %d attempts, want 12 in 1", meta.Usage.TotalTokens, meta.Attempts)
		}
		if meta.RequestID == "" || meta.CorrelationID == "" {
			t.Errorf("ids = %q %q, want both settled", meta.RequestID, meta.CorrelationID)
		}
		if meta.RequestedContract != types.ContractJSONWellFormed || meta.DeliveredContract != types.ContractJSONWellFormed {
			t.Errorf("contracts = %v / %v", meta.RequestedContract, meta.DeliveredContract)
		}
		if meta.Elapsed != 25*time.Millisecond {
			t.Errorf("Elapsed = %v, want the 25ms the stream took", meta.Elapsed)
		}
		if strings.Contains(fmt.Sprintf("%+v", meta), endAnswer) {
			t.Error("the meta holds the answer's text")
		}
	})
}

func TestRecordedStreamTellsTheEndOnceBeforeTheLastItemReachesTheReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		var running, finished sync.WaitGroup
		running.Add(1)
		var calls int
		end := func(types.Meta, error) {
			calls++
			running.Done()
			<-gate
		}
		stream, err := RecordedStream(context.Background(), "streamrewrite", types.ContractPromptOnly,
			openEndStream(&endStreamProvider{run: endCompletes("a", "b")}, nil), end)
		if err != nil {
			t.Fatal(err)
		}
		readerDone := false
		finished.Go(func() {
			for range stream.All() {
			}
			readerDone = true
		})
		synctest.Wait()
		if calls != 1 || readerDone {
			t.Fatalf("calls = %d, reader done = %v: the reader must wait for the end function", calls, readerDone)
		}
		close(gate)
		finished.Wait()
		if calls != 1 {
			t.Errorf("end ran %d times", calls)
		}
	})
}

func TestRecordedStreamTellsTheStreamsOwnErrorForAFailedStream(t *testing.T) {
	providerErr := types.Newf(types.KindProviderUnavailable, "endstream", "the provider fell over")
	synctest.Test(t, func(t *testing.T) {
		provider := &endStreamProvider{run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{Delta: "half "}, nil)
			yield(llm.StreamChunk{}, providerErr)
		}}
		var ended streamEnded
		stream, err := RecordedStream(context.Background(), "streamtranslate", types.ContractPromptOnly, openEndStream(provider, nil), ended.fn)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := readAll(stream)
		if !errors.Is(readErr, providerErr) {
			t.Fatalf("the reader got %v", readErr)
		}
		if ended.count() != 1 || ended.err != readErr {
			t.Fatalf("end ran %d times with %v, want once with the reader's error %v", ended.count(), ended.err, readErr)
		}
		if ended.meta.DeliveredContract != types.ContractPromptOnly || ended.meta.Operation != "streamtranslate" {
			t.Errorf("meta of a failed stream = %+v", ended.meta)
		}
	})
}

// The Meta a stream ends with counts the request it sent whatever its ending, and
// the request's telemetry scope ends once, with the kind of the ending. A stream
// that failed, was truncated, refused as empty, left, cancelled, abandoned or
// timed out had no request, no provider and no model in its Meta.
func TestRecordedStreamMetaCountsTheRequestOfAStreamThatDidNotComplete(t *testing.T) {
	for _, c := range unfinishedStreamCases() {
		t.Run(c.name, func(t *testing.T) {
			collector := installCollector(t)
			var ended streamEnded
			synctest.Test(t, func(t *testing.T) {
				stream := openUnfinished(t, c, func(ctx context.Context, provider llm.Provider) (*TextStream, error) {
					return RecordedStream(ctx, "streamsummarize", types.ContractPromptOnly,
						func(ctx context.Context) (*TextStream, error) {
							return streamLLM(WithProvider(ctx, provider), "system", "user", types.OpOptions{Model: unfinishedModel})
						}, ended.fn)
				})
				if got := ended.count(); got != 1 {
					t.Fatalf("end ran %d times, want 1", got)
				}
				if got := types.KindOf(ended.err); got != c.wantKind {
					t.Errorf("end was told kind %v, want %v", got, c.wantKind)
				}
				_ = stream
			})
			meta := ended.meta
			wantProvider := unfinishedAskedOf
			if c.wantAnswered {
				wantProvider = unfinishedServedBy
			}
			if meta.Attempts != 1 || meta.CacheHits != 0 {
				t.Errorf("Meta counts %d attempts and %d cache hits, want the one request the stream sent", meta.Attempts, meta.CacheHits)
			}
			if meta.Provider != wantProvider || meta.Model != unfinishedModel || meta.RequestedModel != unfinishedModel {
				t.Errorf("Meta names %q %q (asked %q), want %q %q", meta.Provider, meta.Model, meta.RequestedModel, wantProvider, unfinishedModel)
			}
			if meta.RequestID == "" || meta.Operation != "streamsummarize" {
				t.Errorf("Meta carries request id %q and operation %q", meta.RequestID, meta.Operation)
			}
			wantTokens, wantQuality := 0, types.PricingUnknown
			if c.wantUsage {
				wantTokens, wantQuality = unfinishedTotal, types.PricingExact
			}
			if meta.Usage.TotalTokens != wantTokens || meta.Cost.Quality != wantQuality || meta.Cost.Priced != c.wantUsage {
				t.Errorf("Meta's usage is %d tokens costing %q (priced %v), want %d tokens and %q",
					meta.Usage.TotalTokens, meta.Cost.Quality, meta.Cost.Priced, wantTokens, wantQuality)
			}
			if c.wantElapsed > 0 && meta.Elapsed != c.wantElapsed {
				t.Errorf("Elapsed = %v, want the %v the stream took", meta.Elapsed, c.wantElapsed)
			}

			_, finished := collector.snapshot()
			var requests []telemetry.OperationResult
			for _, result := range finished {
				if result.Scope == telemetry.ScopeRequest {
					requests = append(requests, result)
				}
			}
			if len(requests) != 1 {
				t.Fatalf("%d request scopes ended, want 1: %+v", len(requests), requests)
			}
			wantKind := ""
			if c.wantKind != types.KindUnknown {
				wantKind = c.wantKind.String()
			}
			if requests[0].ErrorKind != wantKind || requests[0].Attempts != 1 {
				t.Errorf("the request scope ended with kind %q after %d attempts, want %q after 1", requests[0].ErrorKind, requests[0].Attempts, wantKind)
			}
		})
	}
}

func TestRecordedStreamFailedStreamDeliversNothingAtALevelAboveTheLowest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &endStreamProvider{run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{}, types.Newf(types.KindProviderUnavailable, "endstream", "down"))
		}}
		var ended streamEnded
		stream, _ := RecordedStream(context.Background(), "streamexpand", types.ContractJSONWellFormed, openEndStream(provider, nil), ended.fn)
		readAll(stream)
		if ended.meta.RequestedContract != types.ContractJSONWellFormed || ended.meta.DeliveredContract != types.ContractPromptOnly {
			t.Errorf("a failed stream requested %v and delivered %v, want JSONWellFormed and PromptOnly", ended.meta.RequestedContract, ended.meta.DeliveredContract)
		}
	})
}

func TestRecordedStreamEndsForAReaderThatLeftAndForCancel(t *testing.T) {
	leaves := func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
		if !yield(llm.StreamChunk{Delta: "first"}, nil) {
			return
		}
		<-ctx.Done()
		yield(llm.StreamChunk{}, ctx.Err())
	}
	t.Run("the reader leaves the loop", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var ended streamEnded
			stream, _ := RecordedStream(context.Background(), "streamsummarize", types.ContractPromptOnly,
				openEndStream(&endStreamProvider{run: leaves}, nil), ended.fn)
			for range stream.All() {
				break
			}
			synctest.Wait()
			if ended.count() != 1 || !errors.Is(ended.err, context.Canceled) {
				t.Errorf("end ran %d times with %v", ended.count(), ended.err)
			}
		})
	})
	t.Run("Cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var ended streamEnded
			stream, _ := RecordedStream(context.Background(), "streamsummarize", types.ContractPromptOnly,
				openEndStream(&endStreamProvider{run: leaves}, nil), ended.fn)
			synctest.Wait()
			stream.Cancel()
			synctest.Wait()
			if ended.count() != 1 || !errors.Is(ended.err, context.Canceled) {
				t.Errorf("end ran %d times with %v", ended.count(), ended.err)
			}
		})
	})
	t.Run("a stream nobody reads, whose context ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			var ended streamEnded
			_, err := RecordedStream(ctx, "streamsummarize", types.ContractPromptOnly,
				openEndStream(&endStreamProvider{run: leaves}, nil), ended.fn)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if ended.count() != 0 {
				t.Fatal("end ran before the stream's context ended")
			}
			cancel()
			synctest.Wait()
			if ended.count() != 1 || !errors.Is(ended.err, context.Canceled) {
				t.Errorf("end ran %d times with %v", ended.count(), ended.err)
			}
		})
	})
}

func TestRecordedStreamRefusesBelowTheFloorBeforeOpening(t *testing.T) {
	levels := []types.ContractLevel{
		types.ContractPromptOnly, types.ContractJSONWellFormed, types.ContractSchemaConstrained,
		types.ContractSchemaAndInvariantChecked, types.ContractEvidenceChecked, types.ContractFullyGoverned,
	}
	for _, level := range levels {
		for _, floor := range levels {
			t.Run(level.String()+" against a floor of "+floor.String(), func(t *testing.T) {
				ctx := WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: floor})
				opened := 0
				var ended streamEnded
				stream, err := RecordedStream(ctx, "streamsummarize", level,
					openEndStream(&endStreamProvider{run: endCompletes("x")}, &opened), ended.fn)
				if level >= floor {
					if err != nil || stream == nil || opened != 1 {
						t.Fatalf("a stream at or above the floor: %v, %v, opened %d", stream, err, opened)
					}
					stream.Cancel()
					return
				}
				if stream != nil || err == nil || types.KindOf(err) != types.KindPolicyViolation {
					t.Fatalf("below the floor: stream %v, error %v (kind %v)", stream, err, types.KindOf(err))
				}
				if opened != 0 || ended.count() != 0 {
					t.Errorf("opened %d times and ended %d times for a refused stream", opened, ended.count())
				}
			})
		}
	}
}

func TestRecordedStreamReturnsTheOpenErrorAndNeverEnds(t *testing.T) {
	var ended streamEnded
	boom := types.Newf(types.KindConfiguration, "streamsummarize", "no provider")
	stream, err := RecordedStream(context.Background(), "streamsummarize", types.ContractPromptOnly,
		func(context.Context) (*TextStream, error) { return nil, boom }, ended.fn)
	if stream != nil || err != boom {
		t.Fatalf("got %v, %v", stream, err)
	}
	if ended.count() != 0 {
		t.Error("end ran for a call that opened no stream")
	}

	// A provider that cannot stream is the same shape through the real opener.
	stream, err = RecordedStream(context.Background(), "streamsummarize", types.ContractPromptOnly,
		openEndStream(&namedProvider{name: "no-stream"}, nil), ended.fn)
	if stream != nil || types.KindOf(err) != types.KindUnsupportedCapability || ended.count() != 0 {
		t.Errorf("a provider that cannot stream: %v, %v, ended %d", stream, err, ended.count())
	}
}

func TestRecordedStreamAcceptsANilEndFunction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream, err := RecordedStream(context.Background(), "streamsummarize", types.ContractPromptOnly,
			openEndStream(&endStreamProvider{run: endCompletes("x")}, nil), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readAll(stream); err != nil {
			t.Errorf("read: %v", err)
		}
	})
}

func TestRecordedStreamIsOneOperationToAnInstalledObserver(t *testing.T) {
	collector := installCollector(t)
	synctest.Test(t, func(t *testing.T) {
		stream, err := RecordedStream(context.Background(), "streamsummarize", types.ContractPromptOnly,
			openEndStream(&endStreamProvider{run: endCompletes("x")}, nil), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readAll(stream); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
	})
	operations := 0
	_, finished := collector.snapshot()
	for _, result := range finished {
		if result.Operation == "streamsummarize" && result.Scope == telemetry.ScopeOperation {
			operations++
		}
	}
	if operations != 1 {
		t.Errorf("the observer saw %d operation scopes finish, want 1", operations)
	}
}

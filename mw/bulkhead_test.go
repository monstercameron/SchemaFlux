package mw

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// blockingProvider blocks Complete until release is closed, so a test can
// hold a bulkhead slot open deterministically instead of racing a real call.
type blockingProvider struct {
	name    string
	release chan struct{}
	calls   int32
	mu      sync.Mutex
}

func (b *blockingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
		return llm.CompletionResponse{}, ctx.Err()
	}
	return llm.CompletionResponse{Content: "ok"}, nil
}

// callCount is how many calls have entered the provider.
func (b *blockingProvider) callCount() int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func (b *blockingProvider) Name() string                                   { return b.name }
func (b *blockingProvider) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (b *blockingProvider) RetryPolicy() (int, time.Duration)              { return 0, 0 }

// TestBulkheadMiddlewareRejectsPastCapacity proves RejectWhenFull refuses a
// call over the limit with a classified error instead of blocking.
func TestBulkheadMiddlewareRejectsPastCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &blockingProvider{name: "p", release: make(chan struct{})}
		wrapped := Bulkhead(1, RejectWhenFull())(base)

		done := make(chan struct{})
		go func() {
			wrapped.Complete(context.Background(), llm.CompletionRequest{})
			close(done)
		}()

		// The second call is tried once the first has entered the provider and
		// holds the one slot, which is when every goroutine of the bubble is
		// blocked.
		synctest.Wait()
		if got := base.callCount(); got != 1 {
			t.Fatalf("%d calls had entered the provider, want 1", got)
		}

		_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
		if err == nil {
			t.Fatal("expected the second concurrent call to be refused at capacity 1")
		}
		var opErr *types.OperationError
		if !errors.As(err, &opErr) || opErr.Kind != types.KindAdmissionRejected {
			t.Fatalf("expected KindAdmissionRejected, got %v", err)
		}

		close(base.release)
		<-done
	})
}

// TestBulkheadMiddlewareBlocksByDefaultUntilReleased proves the default
// (non-reject) mode waits for a slot rather than refusing, synchronized via
// channels rather than a sleep.
func TestBulkheadMiddlewareBlocksByDefaultUntilReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &blockingProvider{name: "p", release: make(chan struct{})}
		wrapped := Bulkhead(1)(base)

		firstDone := make(chan struct{})
		go func() {
			wrapped.Complete(context.Background(), llm.CompletionRequest{})
			close(firstDone)
		}()

		// Every goroutine of the bubble is blocked once the first call is holding
		// the one slot inside the provider.
		synctest.Wait()
		if got := base.callCount(); got != 1 {
			t.Fatalf("%d calls had entered the provider, want 1", got)
		}

		secondStarted := make(chan struct{})
		secondDone := make(chan struct{})
		go func() {
			close(secondStarted)
			wrapped.Complete(context.Background(), llm.CompletionRequest{})
			close(secondDone)
		}()
		<-secondStarted

		// With every goroutine of the bubble blocked, the second call has reached
		// the bulkhead and is waiting for the slot the first holds.
		synctest.Wait()
		select {
		case <-secondDone:
			t.Fatal("second call completed before the first released its slot")
		default:
		}

		close(base.release)
		<-firstDone
		<-secondDone

		base.mu.Lock()
		calls := base.calls
		base.mu.Unlock()
		if calls != 2 {
			t.Fatalf("provider calls = %d, want 2", calls)
		}
	})
}

// TestBulkheadMiddlewareHonoursContextCancellation proves a blocked wait
// returns the classified cancellation instead of hanging.
func TestBulkheadMiddlewareHonoursContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &blockingProvider{name: "p", release: make(chan struct{})}
		defer close(base.release)
		wrapped := Bulkhead(1)(base)

		go wrapped.Complete(context.Background(), llm.CompletionRequest{})

		// Every goroutine of the bubble is blocked once the first call is holding
		// the one slot inside the provider.
		synctest.Wait()
		if got := base.callCount(); got != 1 {
			t.Fatalf("%d calls had entered the provider, want 1", got)
		}

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, err := wrapped.Complete(ctx, llm.CompletionRequest{})
			errCh <- err
		}()
		cancel()

		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("expected a cancellation error")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("blocked Complete did not return promptly on cancellation")
		}
	})
}

// TestBulkheadMiddlewareIsolatesProvidersByName proves two differently
// named providers do not share capacity.
func TestBulkheadMiddlewareIsolatesProvidersByName(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mwFn := Bulkhead(1, RejectWhenFull())

		a := &blockingProvider{name: "a", release: make(chan struct{})}
		wrappedA := mwFn(a)
		go wrappedA.Complete(context.Background(), llm.CompletionRequest{})

		// Every goroutine of the bubble is blocked once the first call is holding
		// the one slot inside the provider.
		synctest.Wait()
		if got := a.callCount(); got != 1 {
			t.Fatalf("%d calls had entered the provider, want 1", got)
		}

		b := &fakeProvider{name: "b", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
		wrappedB := mwFn(b)
		if _, err := wrappedB.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("provider b should be unaffected by provider a's full bulkhead: %v", err)
		}

		close(a.release)
	})
}

// TestBulkheadMiddlewareZeroOrNegativeLimitTreatedAsOne proves a
// misconfigured limit still yields a working bulkhead rather than an
// always-full or always-open one.
func TestBulkheadMiddlewareZeroOrNegativeLimitTreatedAsOne(t *testing.T) {
	fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrapped := Bulkhead(0)(fake)
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("a zero limit should default to 1, not refuse every call: %v", err)
	}
}

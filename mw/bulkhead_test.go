package mw

import (
	"context"
	"errors"
	"sync"
	"testing"
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
func (b *blockingProvider) Name() string                                   { return b.name }
func (b *blockingProvider) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (b *blockingProvider) RetryPolicy() (int, time.Duration)              { return 0, 0 }

// TestBulkheadMiddlewareRejectsPastCapacity proves RejectWhenFull refuses a
// call over the limit with a classified error instead of blocking.
func TestBulkheadMiddlewareRejectsPastCapacity(t *testing.T) {
	base := &blockingProvider{name: "p", release: make(chan struct{})}
	wrapped := Bulkhead(1, RejectWhenFull())(base)

	done := make(chan struct{})
	go func() {
		wrapped.Complete(context.Background(), llm.CompletionRequest{})
		close(done)
	}()

	// Wait until the first call has actually entered the provider (holding
	// the one slot) before trying the second.
	deadline := time.Now().Add(5 * time.Second)
	for {
		base.mu.Lock()
		calls := base.calls
		base.mu.Unlock()
		if calls == 1 || time.Now().After(deadline) {
			break
		}
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
}

// TestBulkheadMiddlewareBlocksByDefaultUntilReleased proves the default
// (non-reject) mode waits for a slot rather than refusing, synchronized via
// channels rather than a sleep.
func TestBulkheadMiddlewareBlocksByDefaultUntilReleased(t *testing.T) {
	base := &blockingProvider{name: "p", release: make(chan struct{})}
	wrapped := Bulkhead(1)(base)

	firstDone := make(chan struct{})
	go func() {
		wrapped.Complete(context.Background(), llm.CompletionRequest{})
		close(firstDone)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		base.mu.Lock()
		calls := base.calls
		base.mu.Unlock()
		if calls == 1 || time.Now().After(deadline) {
			break
		}
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		close(secondStarted)
		wrapped.Complete(context.Background(), llm.CompletionRequest{})
		close(secondDone)
	}()
	<-secondStarted

	select {
	case <-secondDone:
		t.Fatal("second call completed before the first released its slot")
	case <-time.After(50 * time.Millisecond):
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
}

// TestBulkheadMiddlewareHonoursContextCancellation proves a blocked wait
// returns the classified cancellation instead of hanging.
func TestBulkheadMiddlewareHonoursContextCancellation(t *testing.T) {
	base := &blockingProvider{name: "p", release: make(chan struct{})}
	defer close(base.release)
	wrapped := Bulkhead(1)(base)

	go wrapped.Complete(context.Background(), llm.CompletionRequest{})

	deadline := time.Now().Add(5 * time.Second)
	for {
		base.mu.Lock()
		calls := base.calls
		base.mu.Unlock()
		if calls == 1 || time.Now().After(deadline) {
			break
		}
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
}

// TestBulkheadMiddlewareIsolatesProvidersByName proves two differently
// named providers do not share capacity.
func TestBulkheadMiddlewareIsolatesProvidersByName(t *testing.T) {
	mwFn := Bulkhead(1, RejectWhenFull())

	a := &blockingProvider{name: "a", release: make(chan struct{})}
	wrappedA := mwFn(a)
	go wrappedA.Complete(context.Background(), llm.CompletionRequest{})

	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		calls := a.calls
		a.mu.Unlock()
		if calls == 1 || time.Now().After(deadline) {
			break
		}
	}

	b := &fakeProvider{name: "b", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrappedB := mwFn(b)
	if _, err := wrappedB.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("provider b should be unaffected by provider a's full bulkhead: %v", err)
	}

	close(a.release)
}

// TestBulkheadMiddlewareDelegatesNameCostAndRetryPolicy matches every other
// middleware's transparency contract.
func TestBulkheadMiddlewareDelegatesNameCostAndRetryPolicy(t *testing.T) {
	fake := &fakeProvider{name: "delegate-test", maxRetries: 3, backoff: 2 * time.Second}
	wrapped := Bulkhead(2)(fake)

	if wrapped.Name() != "delegate-test" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "delegate-test")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Fatalf("EstimateCost() = %v, want 0.01", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 3 || backoff != 2*time.Second {
		t.Fatalf("RetryPolicy() = (%d, %v), want (3, 2s)", retries, backoff)
	}
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

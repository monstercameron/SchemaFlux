package schemaflux

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

// TestClosingASchedulerNoCallUsedStartsNothing holds Close of a scheduler that
// was never used to what it says: there is no scheduler to stop, so none is made
// in order to be stopped.
func TestClosingASchedulerNoCallUsedStartsNothing(t *testing.T) {
	t.Parallel()
	scheduler := &Scheduler{MaxConcurrent: 2}
	for range 3 {
		if err := scheduler.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	scheduler.mu.Lock()
	made, closed := scheduler.inner != nil, scheduler.closed
	scheduler.mu.Unlock()
	if made {
		t.Error("closing a scheduler nothing used made one to close")
	}
	if !closed {
		t.Error("the scheduler does not remember that it was closed")
	}
	if stats := scheduler.Stats(); stats != (SchedulerStats{}) {
		t.Errorf("Stats = %+v", stats)
	}
}

func TestACallOnASchedulerClosedBeforeAnyUseIsRefusedAsClosed(t *testing.T) {
	t.Parallel()
	scheduler := &Scheduler{MaxConcurrent: 2}
	if err := scheduler.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	model := &answerer{name: "closed-before-use", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"number":"A","total":1}`, FinishReason: "stop"}, nil
	}}
	_, err := Extract[limitsInvoice](WithClient(context.Background(), Client{Provider: model, Model: "m", Scheduler: scheduler}), "invoice")
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrClosed {
		t.Fatalf("got %v, want ErrClosed", err)
	}
	if model.count() != 0 {
		t.Errorf("%d requests were sent through a closed scheduler", model.count())
	}
	_ = scheduler.Close(context.Background())
}

// TestASchedulerGoroutineLivesUntilClose runs a call on a scheduler inside a
// testing/synctest bubble, which ends only when every goroutine started in it has
// stopped: a scheduler that was not closed would leave its goroutine and fail the
// bubble, so the test passing is Close stopping it.
func TestASchedulerGoroutineLivesUntilClose(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		model := &answerer{name: "scheduler-goroutine", answer: func(int, Request) (Response, error) {
			return Response{Content: `{"number":"A","total":1}`, FinishReason: "stop"}, nil
		}}
		scheduler := &Scheduler{MaxConcurrent: 1}
		if _, err := Extract[limitsInvoice](WithClient(context.Background(), Client{Provider: model, Model: "m", Scheduler: scheduler}), "invoice"); err != nil {
			t.Fatal(err)
		}
		scheduler.mu.Lock()
		made := scheduler.inner != nil
		scheduler.mu.Unlock()
		if !made {
			t.Error("a call on the scheduler did not start it")
		}
		if err := scheduler.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

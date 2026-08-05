package ops

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// countingStep returns a step that succeeds after failAttempts failures, and a
// pointer to its call count.
func countingStep(name string, failAttempts int) (PipelineStep, *int32) {
	var calls int32
	return PipelineStep{
		Name: name,
		Operation: func(ctx context.Context, input any) (any, error) {
			n := atomic.AddInt32(&calls, 1)
			if int(n) <= failAttempts {
				return nil, fmt.Errorf("%s: attempt %d failed", name, n)
			}
			return fmt.Sprintf("%v>%s", input, name), nil
		},
	}, &calls
}

func pipelineWith(opts PipelineOptions, steps ...PipelineStep) *Pipeline {
	p := NewPipeline("test", opts)
	p.steps = steps
	return p
}

// attempts was computed as MaxRetries rather than MaxRetries+1, so a caller who
// turned retries on without naming a count ran every step ZERO times — and the
// pipeline reported no error, because stepErr was never set.
func TestPipelineRunsEveryStepWithZeroValueMaxRetries(t *testing.T) {
	first, firstCalls := countingStep("first", 0)
	second, secondCalls := countingStep("second", 0)

	result := pipelineWith(PipelineOptions{RetryFailed: true}, first, second).
		Execute(context.Background(), "input")

	if *firstCalls != 1 || *secondCalls != 1 {
		t.Fatalf("each step must run exactly once, got first=%d second=%d", *firstCalls, *secondCalls)
	}
	if result.StepsExecuted != 2 {
		t.Errorf("StepsExecuted = %d, want 2", result.StepsExecuted)
	}
	if len(result.Errors) != 0 {
		t.Errorf("no step failed, but got errors: %v", result.Errors)
	}
	if result.Output != "input>first>second" {
		t.Errorf("Output = %v, want the value threaded through both steps", result.Output)
	}
}

// MaxRetries counts retries, so N retries means N+1 attempts.
func TestPipelineAttemptCounts(t *testing.T) {
	cases := []struct {
		name        string
		maxRetries  int
		failFirstN  int
		wantCalls   int32
		wantSucceed bool
	}{
		{"no_retries_success", 0, 0, 1, true},
		{"no_retries_failure", 0, 1, 1, false},
		{"one_retry_recovers", 1, 1, 2, true},
		{"one_retry_exhausted", 1, 2, 2, false},
		{"two_retries_recovers", 2, 2, 3, true},
		{"two_retries_exhausted", 2, 3, 3, false},
		{"three_retries_recovers", 3, 3, 4, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step, calls := countingStep("step", tc.failFirstN)
			result := pipelineWith(
				PipelineOptions{RetryFailed: true, MaxRetries: tc.maxRetries, FailFast: true, RetryDelay: time.Millisecond},
				step,
			).Execute(context.Background(), "input")

			if *calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d (MaxRetries=%d)", *calls, tc.wantCalls, tc.maxRetries)
			}
			if succeeded := len(result.Errors) == 0; succeeded != tc.wantSucceed {
				t.Errorf("succeeded = %v, want %v (errors: %v)", succeeded, tc.wantSucceed, result.Errors)
			}
		})
	}
}

// A step that fails on every attempt must be reported, not silently skipped.
func TestPipelineReportsAnExhaustedStep(t *testing.T) {
	step, calls := countingStep("always-fails", 99)

	result := pipelineWith(PipelineOptions{RetryFailed: true, MaxRetries: 2, FailFast: true, RetryDelay: time.Millisecond}, step).
		Execute(context.Background(), "input")

	if *calls != 3 {
		t.Errorf("calls = %d, want 3", *calls)
	}
	if len(result.Errors) != 1 || result.StepsFailed != 1 {
		t.Fatalf("expected one reported failure, got %d errors and StepsFailed=%d", len(result.Errors), result.StepsFailed)
	}
}

// Optional steps do not retry and do not stop the pipeline.
func TestPipelineOptionalStepDoesNotRetryOrStop(t *testing.T) {
	optional, optionalCalls := countingStep("optional", 99)
	optional.Optional = true
	after, afterCalls := countingStep("after", 0)

	result := pipelineWith(PipelineOptions{RetryFailed: true, MaxRetries: 3, FailFast: true, RetryDelay: time.Millisecond}, optional, after).
		Execute(context.Background(), "input")

	if *optionalCalls != 1 {
		t.Errorf("an optional step must not retry, got %d calls", *optionalCalls)
	}
	if *afterCalls != 1 {
		t.Error("the pipeline must continue past a failed optional step")
	}
	if result.StepsFailed != 1 {
		t.Errorf("StepsFailed = %d, want 1", result.StepsFailed)
	}
}

// Retries used to sleep with time.Sleep, ignoring the context the loop checks
// at the top of every step. Cancelling mid-backoff must return promptly.
func TestPipelineBackoffIsCancellable(t *testing.T) {
	step, calls := countingStep("slow", 99)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result := pipelineWith(PipelineOptions{RetryFailed: true, MaxRetries: 5, FailFast: true}, step).
		Execute(ctx, "input")
	elapsed := time.Since(start)

	// Five retries with a 1s, 2s, 3s… backoff is at least 15 seconds if the
	// sleep is uncancellable.
	if elapsed > 3*time.Second {
		t.Errorf("cancelling mid-backoff took %v; the wait is not cancellable", elapsed)
	}
	if *calls > 2 {
		t.Errorf("calls = %d; the pipeline kept retrying after cancellation", *calls)
	}
	if len(result.Errors) == 0 {
		t.Error("a cancelled pipeline must report an error")
	}
	if !errors.Is(result.Errors[0], context.Canceled) {
		t.Errorf("the reported error should wrap context.Canceled, got %v", result.Errors[0])
	}
}

// An overall timeout still stops the pipeline between steps.
func TestPipelineTimeoutStopsBetweenSteps(t *testing.T) {
	slow := PipelineStep{
		Name: "slow",
		Operation: func(ctx context.Context, input any) (any, error) {
			return input, sleepWithContext(ctx, 200*time.Millisecond)
		},
	}
	after, afterCalls := countingStep("after", 0)

	result := pipelineWith(PipelineOptions{Timeout: 50 * time.Millisecond, FailFast: true}, slow, after).
		Execute(context.Background(), "input")

	if *afterCalls != 0 {
		t.Error("a timed-out pipeline must not run the next step")
	}
	if len(result.Errors) == 0 {
		t.Fatal("a timeout must be reported")
	}
	if !errors.Is(result.Errors[0], context.DeadlineExceeded) {
		t.Errorf("the reported error should wrap context.DeadlineExceeded, got %v", result.Errors[0])
	}
}

// sleepWithContext returns promptly on cancellation and after the full wait
// otherwise.
func TestSleepWithContext(t *testing.T) {
	t.Run("completes", func(t *testing.T) {
		start := time.Now()
		if err := sleepWithContext(context.Background(), 20*time.Millisecond); err != nil {
			t.Fatalf("sleepWithContext: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
			t.Errorf("returned after %v, want at least 20ms", elapsed)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		err := sleepWithContext(ctx, 10*time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("returned after %v, want promptly", elapsed)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		if err := sleepWithContext(ctx, 10*time.Second); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	})
}

package codex

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// PS-013's integration half, and the question it was raised to answer: does a
// provider that shells out reach the model through the same path an
// API-backed one does?
//
// It matters because everything a host relies on hangs off that path. Retries,
// budget checks, request-ID assignment, cost accounting, error classification,
// and -- since OB-006 -- the observer events a host's tracing is built from
// all live in ops.CallLLM, not in the providers. A transport that bypassed it
// would work perfectly and be invisible, which is the failure that is hardest
// to notice because nothing errors.
//
// So these run the real CallLLM against the codex provider with only its shell
// boundary scripted.

type funnelObserver struct {
	mu       sync.Mutex
	started  []telemetry.OperationEvent
	finished []telemetry.OperationResult
}

func (o *funnelObserver) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = append(o.started, event)
	return ctx
}

func (o *funnelObserver) OperationFinished(_ context.Context, result telemetry.OperationResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, result)
}

func (o *funnelObserver) snapshot() ([]telemetry.OperationEvent, []telemetry.OperationResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]telemetry.OperationEvent(nil), o.started...),
		append([]telemetry.OperationResult(nil), o.finished...)
}

func TestAShelledProviderIsAsVisibleToTelemetryAsAnAPIBackedOne(t *testing.T) {
	observer := &funnelObserver{}
	t.Cleanup(telemetry.SetObserver(observer))

	script := &scriptedTurn{
		transcript: turnTranscript{
			reply:    []byte(`{"ok":true}`),
			events:   []byte(completedEventLine),
			exitCode: 0,
		},
		delay: 2 * time.Millisecond,
	}
	provider := providerWith(script, Options{})

	content, err := ops.CallLLM(context.Background(), provider, "Return JSON.", "the input",
		types.OpOptions{
			Model:        "gpt-5.6-sol",
			Mode:         types.Strict,
			Intelligence: types.Fast,
			SchemaName:   "InvoiceHeader",
		})
	if err != nil {
		t.Fatalf("CallLLM: %v", err)
	}
	if content != `{"ok":true}` {
		t.Fatalf("content = %q", content)
	}
	if script.calls != 1 {
		t.Fatalf("the CLI was invoked %d times, want 1", script.calls)
	}

	started, finished := observer.snapshot()
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("a shelled provider produced %d start and %d finish events, want 1 and 1",
			len(started), len(finished))
	}

	if started[0].Provider != providerName {
		t.Errorf("start event provider = %q, want codex", started[0].Provider)
	}
	if started[0].Model != "gpt-5.6-sol" {
		t.Errorf("start event model = %q", started[0].Model)
	}
	if started[0].SchemaName != "InvoiceHeader" {
		t.Errorf("start event schema name = %q", started[0].SchemaName)
	}
	if started[0].RequestID == "" {
		t.Error("no request ID was assigned; the CLI's turn cannot be correlated with the library's log lines")
	}

	if finished[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", finished[0].Attempts)
	}
	if finished[0].FinishReason != "stop" {
		t.Errorf("finish reason = %q, want stop", finished[0].FinishReason)
	}
	if finished[0].Provider != providerName {
		t.Errorf("finish provider = %q, want codex", finished[0].Provider)
	}
	if finished[0].Usage.TotalTokens != 151 {
		t.Errorf("the CLI's token counts did not reach telemetry: %+v", finished[0].Usage)
	}
	if finished[0].ProviderDuration <= 0 {
		t.Error("no provider time was measured for a shelled turn")
	}

	// The subscription is flat-rate, so the turn is unpriced rather than free.
	if finished[0].Cost.Priced {
		t.Errorf("a codex turn reached telemetry priced at %.6f", finished[0].Cost.TotalCost)
	}
}

// The retry budget is the funnel's, not the provider's, and a shelled provider
// has to be retried by it like any other. One logical request, one pair of
// events, whatever the CLI did in between.
func TestAFailedTurnIsRetriedByTheFunnelAndCountedOnce(t *testing.T) {
	observer := &funnelObserver{}
	t.Cleanup(telemetry.SetObserver(observer))

	attempts := 0
	provider := &Provider{
		options: Options{MaxRetries: 2, RetryBackoff: time.Millisecond},
		turn: func(_ context.Context, _ turnRequest) (turnTranscript, error) {
			attempts++
			if attempts < 3 {
				// A turn that failed: the CLI said so and exited non-zero.
				return turnTranscript{
					events:   []byte(`{"type":"turn.failed","error":{"message":"model overloaded"}}`),
					exitCode: 1,
				}, nil
			}
			return turnTranscript{
				reply:    []byte(`{"ok":true}`),
				events:   []byte(completedEventLine),
				exitCode: 0,
			}, nil
		},
	}

	if _, err := ops.CallLLM(context.Background(), provider, "Return JSON.", "input",
		types.OpOptions{Model: "gpt-5.6-sol", Mode: types.Strict, Intelligence: types.Fast}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	if attempts != 3 {
		t.Errorf("the CLI ran %d times, want 3 -- a failed turn must be retryable through the funnel", attempts)
	}

	started, finished := observer.snapshot()
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("got %d start and %d finish events for one logical request, want 1 and 1",
			len(started), len(finished))
	}
	if finished[0].Attempts != 3 {
		t.Errorf("attempts = %d, want 3", finished[0].Attempts)
	}
}

// Example shows the shape a host codes against. The shell boundary is scripted
// so this runs under `go test` with no codex binary and no login -- which is
// also what every example in this repository does with its provider.
func Example() {
	provider := &Provider{
		options: Options{Model: "gpt-5.6-sol"},
		turn: func(_ context.Context, request turnRequest) (turnTranscript, error) {
			// The prompt arrives on stdin, never in the argument list.
			// Its exact length is not asserted: CallLLM adds prompt
			// reinforcement above this layer, so a byte count here would make
			// this example fail whenever that text was edited.
			fmt.Printf("prompt delivered on stdin: %t, model: %s\n",
				len(request.prompt) > 0, request.model)
			return turnTranscript{
				reply:    []byte(`{"verdict":"approved"}`),
				events:   []byte(completedEventLine),
				exitCode: 0,
			}, nil
		},
	}

	content, err := ops.CallLLM(context.Background(), provider, "Return JSON.", "Approve or reject.",
		types.OpOptions{Model: "gpt-5.6-sol", Mode: types.Strict, Intelligence: types.Fast})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("provider:", provider.Name())
	fmt.Println("answer:", content)

	// Output:
	// prompt delivered on stdin: true, model: gpt-5.6-sol
	// provider: codex
	// answer: {"verdict":"approved"}
}

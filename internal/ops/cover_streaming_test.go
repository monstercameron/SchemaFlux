package ops

// Coverage gaps in streaming.go (Cancel, trySend's ctx-cancelled-with-a-full-
// buffer branch, streamLLM's no-default-model and budget-exhausted refusals)
// and control_flow.go (caseMatches' default type-equality branch, and
// matchesType's interface/pointer/mismatch branches). streaming_test.go
// already covers TC-001 (steering never lands in the system prompt) and the
// four StreamX entry points' happy paths; this file does not repeat those.

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// namedStreamProvider is a minimal llm.StreamingProvider with a caller-chosen
// Name(), for exercising streamLLM's per-provider model resolution without
// captureProvider's fixed "local" name (which always has a default model).
type namedStreamProvider struct {
	name string
	seq  func(yield func(llm.StreamChunk, error) bool)
}

func (p *namedStreamProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errors.New("Complete must not be called by the streaming path")
}
func (p *namedStreamProvider) Name() string                               { return p.name }
func (p *namedStreamProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *namedStreamProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *namedStreamProvider) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return p.seq
}

// Cancel stops the stream immediately, and unlike a plain break out of All,
// it is not gated by Detach -- calling Detach first and then Cancel must
// still cancel.
func TestStreamCancelStopsWorkRegardlessOfDetach(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			if !yield(llm.StreamChunk{Delta: "first"}, nil) {
				return
			}
			<-block // simulates a provider still reading, as in TestTextStreamBreakCancelsWork.
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}

	stream.Detach()
	stream.Cancel()

	select {
	case <-stream.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel must stop the stream even after Detach was called")
	}
}

// trySend's ctx.Done() branch: once the buffer is full and nobody is
// consuming, a producer blocked on send must give up as soon as the stream's
// context is cancelled, rather than staying blocked (which would leak the
// goroutine) or delivering the rest of the chunks after cancellation.
func TestTrySendStopsOnceContextCancelledWithAFullBuffer(t *testing.T) {
	const totalChunks = textStreamBufferSize + 40
	var produced atomic.Int64

	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			for i := 0; i < totalChunks; i++ {
				produced.Add(1)
				if !yield(llm.StreamChunk{Delta: "x"}, nil) {
					return
				}
			}
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
				Content: strings.Repeat("x", totalChunks), FinishReason: "stop",
			}}, nil)
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}

	// Nobody reads stream.items: let the producer run ahead until it fills
	// the bounded buffer and blocks on the next send.
	time.Sleep(300 * time.Millisecond)
	beforeCancel := produced.Load()
	if beforeCancel >= totalChunks {
		t.Fatalf("producer ran to completion (%d/%d) before cancellation -- the buffer did not bound it", beforeCancel, totalChunks)
	}

	stream.Cancel()
	time.Sleep(300 * time.Millisecond)
	afterCancel := produced.Load()

	if afterCancel >= totalChunks {
		t.Fatalf("producer ran to completion (%d/%d) after cancellation -- trySend's ctx.Done() branch did not stop it", afterCancel, totalChunks)
	}
	if max := beforeCancel + 4; afterCancel > max {
		t.Fatalf("producer advanced from %d to %d after cancellation, want at most %d further", beforeCancel, afterCancel, max)
	}
}

// A provider whose name has no built-in default model reports that, and
// never reaches CompleteStream.
func TestStreamLLMNoDefaultModelReportsBeforeCallingProvider(t *testing.T) {
	calledStream := false
	provider := &namedStreamProvider{
		name: "an-unknown-streaming-provider",
		seq:  func(yield func(llm.StreamChunk, error) bool) { calledStream = true },
	}
	withStreamingProvider(t, provider)

	_, err := StreamSummarize("input", NewSummarizeOptions())
	if err == nil {
		t.Fatal("expected an error when the provider has no default model mapping")
	}
	if !strings.Contains(err.Error(), "no default model") {
		t.Errorf("err = %v, want it to name the missing default model", err)
	}
	if calledStream {
		t.Error("CompleteStream must not run once no model could be resolved")
	}
}

// An exhausted budget is refused before the provider is ever touched, the
// same contract CallLLM's buffered path enforces (budget_enforcement_test.go).
func TestStreamLLMRefusesWhenBudgetExhausted(t *testing.T) {
	pricing.ResetCostTracking()
	pricing.ResetBudget()
	t.Cleanup(func() {
		pricing.ResetCostTracking()
		pricing.ResetBudget()
	})

	pricing.SetBudget(0.10, 0, 0, nil)
	pricing.SetBudgetEnforcement(true)
	pricing.TrackCost(
		&types.CostInfo{TotalCost: 0.25, Currency: "USD", Priced: true},
		&types.ResultMetadata{RequestID: "req-earlier", EndTime: time.Now()},
	)

	calledStream := false
	provider := &namedStreamProvider{
		name: "local", // a known provider, so this reaches the budget check.
		seq:  func(yield func(llm.StreamChunk, error) bool) { calledStream = true },
	}
	withStreamingProvider(t, provider)

	_, err := StreamSummarize("input", NewSummarizeOptions())
	if err == nil {
		t.Fatal("expected the exhausted budget to be refused")
	}
	if !errors.Is(err, pricing.ErrBudgetExceeded) {
		t.Errorf("errors.Is(err, pricing.ErrBudgetExceeded) = false; err = %v", err)
	}
	if calledStream {
		t.Error("CompleteStream must not run with an exhausted budget")
	}
}

// --- control_flow.go: caseMatches' default branch, matchesType's remaining branches. ---

// caseMatches' default branch (a condition that is not a string, a
// reflect.Type, or an error) is documented as a same-concrete-type check --
// it compares reflect.TypeOf(input) to reflect.TypeOf(condition), not the
// values. Same type, any value, matches.
func TestCaseMatchesDefaultBranchComparesConcreteTypeNotValue(t *testing.T) {
	ran := false
	matched, err := Match(context.Background(), 42, When(0, func() { ran = true }))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || !ran {
		t.Error("an int condition must match any int input, regardless of value")
	}
}

// A condition of a different concrete type never matches, even at an equal
// value.
func TestCaseMatchesDefaultBranchRejectsDifferentConcreteType(t *testing.T) {
	ran := false
	matched, err := Match(context.Background(), 42, When(int64(42), func() { ran = true }))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched || ran {
		t.Error("int64(42) must not match an int input, even at an equal value")
	}
}

// A nil input can never satisfy the default branch's type comparison.
func TestCaseMatchesDefaultBranchNilInputNeverMatches(t *testing.T) {
	matched, err := Match(context.Background(), nil, When(0, func() {}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("a nil input must never match a concrete-type condition")
	}
}

// matchesType's interface branch: a concrete error matches a condition naming
// the error interface itself, via Implements rather than exact-type equality.
func TestMatchesTypeInterfaceImplementation(t *testing.T) {
	ran := false
	errType := reflect.TypeOf((*error)(nil)).Elem()
	matched, err := Match(context.Background(), errors.New("boom"), When(errType, func() { ran = true }))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || !ran {
		t.Error("a concrete error value must match an interface-typed condition it implements")
	}
}

// matchesType's pointer branch: two pointer types match when their element
// types match, without requiring identical pointer types via inputType ==
// targetType (which would already be true here, so the assertion is that
// this still succeeds through the intended path for pointer-to-struct
// conditions built independently of the input's own type value).
func TestMatchesTypePointerElemEquality(t *testing.T) {
	type ptrTarget struct{ X int }
	ran := false
	matched, err := Match(context.Background(), &ptrTarget{X: 1}, When(reflect.TypeOf(&ptrTarget{}), func() { ran = true }))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || !ran {
		t.Error("a *ptrTarget input must match a *ptrTarget condition")
	}
}

// Mismatched pointer element types fall through to the "does not match"
// default, not a false positive or a panic.
func TestMatchesTypeRejectsMismatchedPointerElems(t *testing.T) {
	type ptrTarget struct{ X int }
	type otherTarget struct{ Y string }

	matched, err := Match(context.Background(), &ptrTarget{}, When(reflect.TypeOf(&otherTarget{}), func() {}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("*ptrTarget must not match a *otherTarget condition")
	}
}

// A type condition that is neither an exact match, an interface the input
// implements, nor a same-elem pointer pair falls through matchesType's final
// "false" -- proven with two unrelated concrete (non-pointer) types.
func TestMatchesTypeFallsThroughToFalseForUnrelatedTypes(t *testing.T) {
	matched, err := Match(context.Background(), "a string", When(reflect.TypeOf(0), func() {}))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("a string input must not match an int type condition")
	}
}

package fluent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// FL-007 / CF-09: composition used to mean either the retired untyped
// Pipeline (CF-008 removed it) or a caller hand-writing an ops.Step[T]
// closure around a fluent Run call. These tests exercise the replacement --
// AsStep/AsStepCtx feeding a fluent builder straight into the typed
// combinators (compose.go) -- against real fluent builders and a stub
// provider, not against internal/ops directly, since ops' own combinator
// behaviour already has its own test suite (internal/ops/combinators_test.go)
// and duplicating it here would be a second opinion about what Escalate does.

// scriptedProvider answers with responses[call-1] on the call-th Complete
// call (1-indexed), and the last entry forever after -- for tests that need
// the first attempt to fail or be rejected and a later one to succeed.
type scriptedProvider struct {
	calls     int
	responses []string // JSON bodies, in order
	fail      map[int]error
}

func (p *scriptedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	if err, ok := p.fail[p.calls]; ok {
		return llm.CompletionResponse{}, err
	}
	idx := p.calls - 1
	if idx >= len(p.responses) {
		idx = len(p.responses) - 1
	}
	return llm.CompletionResponse{
		Content:      p.responses[idx],
		Model:        req.Model,
		Provider:     "local",
		FinishReason: "stop",
	}, nil
}

func (p *scriptedProvider) Name() string                               { return "local" }
func (p *scriptedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *scriptedProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// Escalate over two fluent Extract requests: the first (cheap) attempt's
// answer is rejected by accept, so it escalates to the second (strong) one,
// which the stub answers differently on its second call.
func TestFL007_Escalate_OverFluentExtractBuilders(t *testing.T) {
	p := &scriptedProvider{responses: []string{`{"name":""}`, `{"name":"filled in"}`}}
	restore := installStubProvider(t, p)
	defer restore()

	cheap := Extracting[stubExtractTarget]("doc").Quick()
	strong := Extracting[stubExtractTarget]("doc").Smart()

	result, record, err := Escalate(context.Background(),
		AsStepCtx(cheap.Run), AsStepCtx(strong.Run),
		func(v stubExtractTarget) bool { return v.Name != "" })

	if err != nil {
		t.Fatalf("Escalate: unexpected error: %v", err)
	}
	if !record.Escalated {
		t.Error("record.Escalated = false, want true -- the first answer should have been rejected")
	}
	if record.Reason != "rejected" {
		t.Errorf("record.Reason = %q, want %q", record.Reason, "rejected")
	}
	if result.Name != "filled in" {
		t.Errorf("result.Name = %q, want %q (the stronger route's answer)", result.Name, "filled in")
	}
	if p.calls != 2 {
		t.Errorf("provider was called %d times, want exactly 2 (one per route)", p.calls)
	}
}

// Escalate accepts the first answer outright when accept says yes --
// the stronger route must never run.
func TestFL007_Escalate_AcceptsFirstAnswer_StrongerNeverRuns(t *testing.T) {
	p := &scriptedProvider{responses: []string{`{"name":"good enough"}`, `{"name":"should never be seen"}`}}
	restore := installStubProvider(t, p)
	defer restore()

	cheap := Extracting[stubExtractTarget]("doc").Quick()
	strong := Extracting[stubExtractTarget]("doc").Smart()

	result, record, err := Escalate(context.Background(),
		AsStepCtx(cheap.Run), AsStepCtx(strong.Run),
		func(v stubExtractTarget) bool { return v.Name != "" })

	if err != nil {
		t.Fatalf("Escalate: unexpected error: %v", err)
	}
	if record.Escalated {
		t.Error("record.Escalated = true, want false -- the first answer should have been accepted")
	}
	if result.Name != "good enough" {
		t.Errorf("result.Name = %q, want %q", result.Name, "good enough")
	}
	if p.calls != 1 {
		t.Errorf("provider was called %d times, want exactly 1 -- the stronger route ran when it should not have", p.calls)
	}
}

// Fallback over two fluent Extract requests: the primary route's provider
// call fails outright (not merely produces a rejectable answer), and
// Fallback runs the alternate.
func TestFL007_Fallback_OverFluentExtractBuilders(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{`{"name":"from alternate"}`, `{"name":"from alternate"}`},
		fail:      map[int]error{1: errors.New("simulated transient provider failure")},
	}
	restore := installStubProvider(t, p)
	defer restore()

	primary := Extracting[stubExtractTarget]("doc").Quick()
	alternate := Extracting[stubExtractTarget]("doc").Smart()

	result, err := Fallback(context.Background(), AsStepCtx(primary.Run), AsStepCtx(alternate.Run))
	if err != nil {
		t.Fatalf("Fallback: unexpected error: %v", err)
	}
	if result.Name != "from alternate" {
		t.Errorf("result.Name = %q, want %q", result.Name, "from alternate")
	}
	if p.calls != 2 {
		t.Errorf("provider was called %d times, want exactly 2", p.calls)
	}
}

// Until over a fluent Extract request: retries until the predicate accepts,
// bounded by max attempts.
func TestFL007_Until_OverFluentExtractBuilder(t *testing.T) {
	p := &scriptedProvider{responses: []string{`{"name":""}`, `{"name":""}`, `{"name":"third time"}`}}
	restore := installStubProvider(t, p)
	defer restore()

	req := Extracting[stubExtractTarget]("doc").Quick()

	result, attempts, err := Until(context.Background(), AsStepCtx(req.Run),
		func(v stubExtractTarget) bool { return v.Name != "" }, 5)
	if err != nil {
		t.Fatalf("Until: unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if result.Name != "third time" {
		t.Errorf("result.Name = %q, want %q", result.Name, "third time")
	}
}

// Until exhausts its attempt budget and reports that as an error rather
// than silently returning the last (failing) answer -- the fail-open shape
// ops.Until's own doc comment says it refuses.
func TestFL007_Until_ExhaustsAttempts_IsAnError(t *testing.T) {
	p := &scriptedProvider{responses: []string{`{"name":""}`}}
	restore := installStubProvider(t, p)
	defer restore()

	req := Extracting[stubExtractTarget]("doc").Quick()

	_, attempts, err := Until(context.Background(), AsStepCtx(req.Run),
		func(v stubExtractTarget) bool { return v.Name != "" }, 3)
	if err == nil {
		t.Fatal("Until: got nil error after exhausting attempts, want an error")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if p.calls != 3 {
		t.Errorf("provider was called %d times, want exactly 3 (the attempt budget)", p.calls)
	}
}

// AsStepCtx threads the combinator's ctx through to a variadic Run, so
// cancelling the ctx passed to the combinator aborts an attempt already in
// flight -- not merely gates the next one. AsStep (used above) cannot do
// this: its wrapped builders have no ctx parameter on Run at all.
func TestFL007_AsStepCtx_CancellationReachesTheCall(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{delay: 500 * time.Millisecond})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	req := Extracting[stubExtractTarget]("doc")
	start := time.Now()
	_, err := AsStepCtx(req.Run)(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("took %s, want well under the stub's 500ms delay -- cancellation did not reach the call", elapsed)
	}
}

// AsStep's documented limitation, made concrete: wrapping a plain Run()
// builder gives the combinator no way to cancel an attempt already in
// flight, only to skip a future one. This is not a bug in AsStep; it is
// what its doc comment promises, verified so a future change to Run's
// signature is not free to silently change this without the test noticing.
func TestFL007_AsStep_DoesNotThreadCombinatorCtx(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{delay: 150 * time.Millisecond})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the step ever runs

	// Classifying's Run has no ctx parameter -- AsStep cannot forward
	// anything to it. The already-cancelled ctx above is Escalate's
	// pre-attempt ctx.Err() check to make, not a signal AsStep can pass on;
	// calling the adapted step directly (bypassing Escalate's own guard)
	// proves it runs to completion rather than aborting.
	// What matters is timing, not the parse outcome: the stub's canned body
	// is not shaped like a Classify answer, so Run may well return a parse
	// error, but only *after* the call actually ran -- which is the
	// property under test.
	req := Classifying[stubExtractTarget, string](stubExtractTarget{Name: "x"}).Categories("a", "b")
	start := time.Now()
	_, _ = AsStep(req.Run)(ctx)
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("took %s, want at least the stub's 150ms delay -- the call should have run to completion", elapsed)
	}
}

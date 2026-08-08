package fluent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// A-013: Run(ctx) on the fluent builders. These tests exercise it against
// internal/ops directly via ops.SetDefaultProvider rather than through the
// root schemaflux package or schemafluxtest, because both of those live
// outside this task's edit scope and importing the root package from this
// package's own test files would be a cycle (schemaflux imports
// internal/api/fluent already, for the fluent surface it re-exports).

// slowStubProvider answers every call after delay, honouring ctx
// cancellation exactly as schemafluxtest.Provider.Slow does -- this package
// cannot import schemafluxtest (see the package doc comment above), so the
// same small shape is reproduced here rather than depended on.
type slowStubProvider struct {
	delay time.Duration
	body  string // "" defaults to a body that satisfies stubExtractTarget
}

func (p *slowStubProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if p.delay > 0 {
		timer := time.NewTimer(p.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return llm.CompletionResponse{}, err
	}
	body := p.body
	if body == "" {
		body = `{"name":"stub"}`
	}
	return llm.CompletionResponse{
		Content:      body,
		Model:        req.Model,
		Provider:     "local",
		FinishReason: "stop",
	}, nil
}

func (p *slowStubProvider) Name() string                               { return "local" }
func (p *slowStubProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *slowStubProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// installStubProvider points ops' package-level provider at p and returns a
// restore func. Mirrors schemafluxtest.Install's contract without importing
// it. Not safe for t.Parallel, for the same reason Install documents: the
// provider is a package global.
func installStubProvider(t *testing.T, p llm.Provider) func() {
	t.Helper()
	ops.SetDefaultProvider(p)
	return func() { ops.SetDefaultProvider(nil) }
}

type stubExtractTarget struct {
	Name string `json:"name"`
}

// Run(ctx) cancels: the literal case A-013's Revised line names --
// Extracting[T](x).Run(ctx).
func TestExtractRequest_RunCancels(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{delay: 500 * time.Millisecond})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := Extracting[stubExtractTarget]("irrelevant, the stub never reads it").Run(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("Run took %s, want well under the stub's 500ms delay -- the cancellation did not abort it", elapsed)
	}
}

// Run with no argument keeps compiling and behaving as it did before this
// task -- the one call site this package cannot edit
// (provider_integration_test.go) depends on exactly this.
func TestExtractRequest_RunWithNoArgument_StillCompiles(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{})
	defer restore()

	_, err := Extracting[stubExtractTarget]("x").Run()
	if err != nil {
		t.Errorf("Run() with no context failed against a fast stub: %v", err)
	}
}

// RunResult(ctx) executes identically to Run and returns the envelope.
func TestExtractRequest_RunResult_ReturnsEnvelope(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{})
	defer restore()

	result, err := Extracting[stubExtractTarget]("x").RunResult(context.Background())
	if err != nil {
		t.Fatalf("RunResult failed: %v", err)
	}
	if result.Meta.Operation != "extract" {
		t.Errorf("Meta.Operation = %q, want %q", result.Meta.Operation, "extract")
	}
	if result.Meta.Attempts < 1 {
		t.Errorf("Meta.Attempts = %d, want at least 1", result.Meta.Attempts)
	}
}

// RunResult(ctx) also cancels, on a builder that has no ops.XResult twin
// (Choose) -- the best-effort envelope path in run.go, not ops.ExtractResult.
func TestChooseRequest_RunResultCancels(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{delay: 500 * time.Millisecond})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result, err := Choosing([]string{"a", "b", "c"}).By("pick one").RunResult(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if elapsed >= 500*time.Millisecond {
		t.Errorf("RunResult took %s, want well under the stub's 500ms delay", elapsed)
	}
	if result.Meta.Operation != "choose" {
		t.Errorf("Meta.Operation = %q, want %q even on failure", result.Meta.Operation, "choose")
	}
}

// Branching a builder does not affect its sibling: base.Smart() and
// base.Quick() are independent values, not two views of one mutated builder
// (API-05). Value receivers already give this property; this is the
// regression test for it. Fields are read as opts.CommonOptions.X rather
// than opts.X: ChooseOptions embeds both CommonOptions and types.OpOptions,
// both of which declare Mode and Intelligence, so the unqualified selector
// is ambiguous -- a compile error, not a test failure, which is its own
// small confirmation that nothing here silently reads the wrong one.
func TestBuilder_BranchingDoesNotAffectSibling(t *testing.T) {
	base := Choosing([]string{"a", "b"}).By("criteria").Steer("base steering")

	smart := base.Smart().Steer("smart steering")
	quick := base.Quick()

	if smart.opts.CommonOptions.Intelligence != Smart {
		t.Errorf("smart.Intelligence = %v, want Smart", smart.opts.CommonOptions.Intelligence)
	}
	if quick.opts.CommonOptions.Intelligence != Quick {
		t.Errorf("quick.Intelligence = %v, want Quick", quick.opts.CommonOptions.Intelligence)
	}
	// The critical assertions: branching one did not leak into the other or
	// mutate base.
	if quick.opts.CommonOptions.Steering != "base steering" {
		t.Errorf("quick.Steering = %q, want the base value -- smart's Steer leaked into quick", quick.opts.CommonOptions.Steering)
	}
	if smart.opts.CommonOptions.Intelligence == Quick {
		t.Error("smart picked up quick's Intelligence -- siblings are not independent")
	}
	if base.opts.CommonOptions.Intelligence == Smart || base.opts.CommonOptions.Intelligence == Quick {
		t.Error("base was mutated by branching one of its children")
	}
	if base.opts.CommonOptions.Steering != "base steering" {
		t.Errorf("base.Steering = %q, want it unchanged by either child", base.opts.CommonOptions.Steering)
	}
}

// A model pin (here, MaxOutputTokens -- the field CallLLM actually reads,
// see internal/ops/optionscope_test.go for the same demonstration at the
// options layer) set on one call's builder does not leak into a second call
// built from the same starting point.
func TestBuilder_PerCallSettingDoesNotLeakBetweenCalls(t *testing.T) {
	base := Extracting[stubExtractTarget]("x")

	first := base.Configure(func(o ExtractOptions) ExtractOptions {
		o.OpOptions.MaxOutputTokens = 111
		return o
	})
	second := base.Configure(func(o ExtractOptions) ExtractOptions {
		o.OpOptions.MaxOutputTokens = 222
		return o
	})

	if first.opts.OpOptions.MaxOutputTokens != 111 {
		t.Errorf("first = %d, want 111", first.opts.OpOptions.MaxOutputTokens)
	}
	if second.opts.OpOptions.MaxOutputTokens != 222 {
		t.Errorf("second = %d, want 222", second.opts.OpOptions.MaxOutputTokens)
	}
}

// The read-at-run contract, deterministically: a builder stores the slice it
// was given by reference (no defensive copy at build time), so a mutation
// made after Choosing(...) but before Run() is visible to Run. This is the
// contract's proof that does not need the race detector -- see run.go's doc
// comment for why READ-AT-RUN, not capture-at-build, is the contract this
// package chose.
func TestBuilder_ObservesAMutationMadeAfterBuild(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{})
	defer restore()

	items := []string{"original"}
	request := Choosing(items).By("pick one")

	// Mutate the backing array in place after the request was built, before
	// Run executes.
	items[0] = "mutated"

	// The provider double does not read item content, so this cannot verify
	// the mutated value reached a request body; what it verifies is the
	// weaker and sufficient claim for a public contract test: the builder's
	// stored slice is the same backing array as the caller's, not a copy
	// taken at build time.
	if &request.options[0] != &items[0] {
		t.Fatal("request.options does not alias the caller's backing array -- Choosing made a defensive copy, and the contract in run.go's doc comment is now wrong")
	}
	if request.options[0] != "mutated" {
		t.Errorf("request.options[0] = %q, want %q (the post-build mutation)", request.options[0], "mutated")
	}
}

// The race-detector version of the same contract: mutating the input
// concurrently with Run is a genuine data race, because Run reads it live
// rather than from a snapshot. -race does not run on this machine
// (windows/arm64); CI covers it. This test is written to fail loudly under
// -race and to simply pass without it, which is what "write it anyway" asks
// for.
func TestBuilder_ConcurrentMutationDuringRunIsARace(t *testing.T) {
	restore := installStubProvider(t, &slowStubProvider{delay: 30 * time.Millisecond})
	defer restore()

	items := []string{"a", "b", "c"}
	request := Choosing(items).By("pick one")

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = request.Run(context.Background())
	}()
	go func() {
		defer wg.Done()
		// Unsynchronized with the goroutine above on purpose: this is the
		// race -race is supposed to catch. Under the detector, this and the
		// provider's own read of req (via items, threaded into the prompt)
		// race on the same backing array.
		items[0] = "raced"
	}()

	wg.Wait()
}

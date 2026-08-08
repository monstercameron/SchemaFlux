package fluent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// FL-006 / F-07: builders used to validate nothing until Run() handed the
// built options to an op function, so a mis-built request (empty Filter
// criteria, an out-of-range Threshold, ...) was only ever discovered after
// this package had already started doing provider work on the caller's
// behalf. buildError (fluent_base.go) now calls the same Validate() every
// op function itself calls, but from Run()/RunResult() before dispatch --
// so a mis-built request is refused at the same point Run() was asked to
// run it, without doing anything an actual call would have done first.
//
// These tests exist to watch that refusal happen, the same way every gate
// script in this repository is required to be proven by watching it refuse
// something (AGENTS.md, "Never fail open"). countingProvider is what proves
// "without a provider being contacted" is not just an error return but an
// actual zero call count.

// countingProvider counts Complete calls, so a test can assert zero of them
// happened rather than merely that Run() returned an error -- an op could in
// principle fail for an unrelated reason after already contacting a
// provider, and only a call count distinguishes "refused before dispatch"
// from "failed after dispatch".
type countingProvider struct {
	calls atomic.Int64
	body  string
}

func (p *countingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls.Add(1)
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

func (p *countingProvider) Name() string                               { return "local" }
func (p *countingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *countingProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// requireNoContact fails t if the provider was ever called, naming what was
// supposed to have refused before dispatch.
func requireNoContact(t *testing.T, p *countingProvider, who string) {
	t.Helper()
	if n := p.calls.Load(); n != 0 {
		t.Errorf("%s: provider was contacted %d time(s); want 0 -- validation should have refused before any provider work", who, n)
	}
}

func TestFilterRequest_Run_EmptyCriteria_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	// No .By(...) -- Criteria stays "", which FilterOptions.Validate
	// (internal/ops/options.go) has always rejected. What FL-006 adds is
	// that Run() catches it before Filter() is even called.
	_, err := Filtering([]string{"a", "b"}).Run()
	if err == nil {
		t.Fatal("Run() with no criteria: got nil error, want a validation error")
	}
	requireNoContact(t, p, "Filtering(...).Run()")
}

func TestFilterRequest_RunResult_EmptyCriteria_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	_, err := Filtering([]string{"a", "b"}).RunResult(context.Background())
	if err == nil {
		t.Fatal("RunResult() with no criteria: got nil error, want a validation error")
	}
	requireNoContact(t, p, "Filtering(...).RunResult()")
}

func TestSortRequest_Run_EmptyCriteria_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	// No .By(...) -- SortOptions.Validate requires non-empty Criteria too.
	_, err := Sorting([]string{"a", "b"}).Run()
	if err == nil {
		t.Fatal("Run() with no criteria: got nil error, want a validation error")
	}
	requireNoContact(t, p, "Sorting(...).Run()")
}

func TestSortRequest_Run_InvalidDirection_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	req := Sorting([]string{"a", "b"}).By("size").Configure(func(o SortOptions) SortOptions {
		o.Direction = "sideways"
		return o
	})
	_, err := req.Run()
	if err == nil {
		t.Fatal("Run() with an invalid direction: got nil error, want a validation error")
	}
	requireNoContact(t, p, "Sorting(...).Direction(sideways).Run()")
}

func TestChooseRequest_Run_TopNBelowOne_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	// ChooseOptions.Validate rejects TopN < 1; NewChooseOptions defaults it
	// to 1, so this needs an explicit Top(0) to break it.
	_, err := Choosing([]string{"a", "b"}).By("quality").Top(0).Run()
	if err == nil {
		t.Fatal("Run() with Top(0): got nil error, want a validation error")
	}
	requireNoContact(t, p, "Choosing(...).Top(0).Run()")
}

func TestChooseRequest_Run_InvalidStrategy_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	req := Choosing([]string{"a", "b"}).By("quality").Configure(func(o ChooseOptions) ChooseOptions {
		o.Strategy = "coin-flip"
		return o
	})
	_, err := req.Run()
	if err == nil {
		t.Fatal("Run() with an unrecognized strategy: got nil error, want a validation error")
	}
	requireNoContact(t, p, "Choosing(...).Strategy(coin-flip).Run()")
}

func TestExtractRequest_Run_ThresholdOutOfRange_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	// CommonOptions.Validate rejects a Threshold outside [0, 1]; every
	// commonRequest- and opRequest-based builder inherits this check because
	// every one of their Options types embeds CommonOptions.
	_, err := Extracting[stubExtractTarget]("irrelevant").Threshold(5).Run()
	if err == nil {
		t.Fatal("Run() with Threshold(5): got nil error, want a validation error")
	}
	requireNoContact(t, p, "Extracting(...).Threshold(5).Run()")
}

func TestClassifyRequest_Run_ThresholdOutOfRange_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	// A second, independently-built commonRequest-based type, to show the
	// guard is a property of the shared base (fluent_base.go), not a
	// one-off special case wired into Extract alone.
	req := Classifying[stubExtractTarget, string](stubExtractTarget{Name: "irrelevant"}).
		Categories("a", "b").
		Threshold(-1)
	_, err := req.Run()
	if err == nil {
		t.Fatal("Run() with Threshold(-1): got nil error, want a validation error")
	}
	requireNoContact(t, p, "Classifying(...).Threshold(-1).Run()")
}

func TestValidateRequest_Run_InvalidFailOn_RefusedWithoutProviderContact(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	// ValidateOptions.Validate rejects an unrecognized FailOn level.
	_, err := Validating[stubExtractTarget](stubExtractTarget{Name: "x"}).FailOn("catastrophe").Run()
	if err == nil {
		t.Fatal("Run() with FailOn(catastrophe): got nil error, want a validation error")
	}
	requireNoContact(t, p, "Validating(...).FailOn(catastrophe).Run()")
}

// A builder that is valid still runs -- buildError must not become a second
// gate that blocks well-formed requests, only a mis-built one. What matters
// here is that the provider was reached at all; how Filter's own wire
// protocol parses the stub's answer is that operation's concern, not
// buildError's, so this does not assert err == nil.
func TestFilterRequest_Run_ValidCriteria_ReachesProvider(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	_, _ = Filtering([]string{"a", "b"}).By("keep only a").Run()
	if n := p.calls.Load(); n == 0 {
		t.Error("provider was never contacted for a validly-built request")
	}
}

// A directRequest-based builder (fluent_advanced.go) whose underlying
// Options type has no Validate() method at all -- ResolveOptions is one of
// eleven such types (Negotiate, NegotiateAdversarial, Resolve, Derive,
// Conform, Interpolate, Arbitrate, Project, Audit, Assemble, Pivot). This is
// not a gap this test papers over: buildError's type assertion against
// validatable simply finds nothing to call and returns nil, so Run()
// proceeds exactly as it did before this task. Documented in the task
// report as a known limitation, not silently left untested.
func TestResolveRequest_Run_NoValidateMethod_BuildErrorIsANoOp(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	_, _ = Resolving([]stubExtractTarget{{Name: "a"}, {Name: "b"}}).Run()
	if n := p.calls.Load(); n == 0 {
		t.Error("provider was never contacted -- ResolveOptions has no Validate(), so this Run() should behave exactly as before FL-006")
	}
}

package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// A-009's Revised line ("a caller must be able to register their own ...
// invariant ... and have it run inside the kernel") and A-011's Revised line
// (the diagnostic sink as the second channel for content removed from
// errors). Both land on the same seam: RunOp's repair loop, and the envelope
// RunOpResult builds around it.

// scriptedProvider is a fake llm.Provider that answers with each body in
// bodies in turn, holding the last one for any call beyond the script's
// length -- the same shape isolation_test.go's namedProvider uses, extended
// to answer differently per call so a test can drive decode-then-repair
// through the real CallLLM path (and therefore through publishCallRecord),
// which setLLMCaller's customLLMCaller hook bypasses entirely.
type scriptedProvider struct {
	mu     sync.Mutex
	bodies []string
	calls  int
}

func (p *scriptedProvider) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	i := p.calls
	p.calls++
	p.mu.Unlock()
	if i >= len(p.bodies) {
		i = len(p.bodies) - 1
	}
	return llm.CompletionResponse{
		Content:      p.bodies[i],
		Provider:     "scripted",
		Model:        "fake-model",
		FinishReason: "stop",
	}, nil
}

func (p *scriptedProvider) Name() string                               { return "scripted" }
func (p *scriptedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *scriptedProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }
func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// echoOp builds a minimal Op[string, string] whose Decode always succeeds
// (the body itself is the value) and whose Invariants are whatever the test
// supplies -- the shape a caller registering "their own ... invariant" would
// build, per A-009's Revised line.
func echoOp(invariants ...func(string) error) Op[string, string] {
	return Op[string, string]{
		ID:        types.OperationID{Name: "callerEcho", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[string]{
			Decode:     func(body string) (string, error) { return body, nil },
			Invariants: invariants,
		},
		Batch: BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) {
			return "echo", input
		},
	}
}

// --- A-009: a caller-supplied invariant runs inside the kernel's repair loop.

// TestCallerInvariantParticipatesInRepairLoop is the Revised line's own
// example, checked directly against RunOp: a caller's own rule (not one of
// invariants.go's library functions) rejects the first answer and accepts
// the second, and the repair loop -- built for Decode failures -- treats it
// identically. Nothing new had to be built for this to work: Contract.
// Invariants was already threaded into the same closure Decode runs in
// (RunOp, op.go), so this test is the proof the wiring TODOS.md's A-009
// entry says "does not yet run" already does.
func TestCallerInvariantParticipatesInRepairLoop(t *testing.T) {
	calls := 0
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		calls++
		if calls == 1 {
			return "not-even", nil
		}
		if !strings.Contains(user, "previous answer could not be used") {
			t.Fatalf("repair prompt does not name the problem: %q", user)
		}
		return "even", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	// A domain rule with no counterpart in invariants.go: "the answer must be
	// the literal string 'even'". This is exactly the shape A-009's Revised
	// line describes -- a caller's own check, not a library one.
	mustBeEven := func(value string) error {
		if value != "even" {
			return fmt.Errorf("the answer was not the expected literal")
		}
		return nil
	}

	value, repair, err := RunOp(context.Background(), echoOp(mustBeEven), "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if value != "even" {
		t.Fatalf("RunOp() = %q, want %q", value, "even")
	}
	if !repair.Repaired || repair.Attempts != 2 {
		t.Fatalf("repair = %+v, want repaired with 2 attempts", repair)
	}
	if len(repair.Failures) != 1 {
		t.Fatalf("Failures = %v, want exactly the one rejection", repair.Failures)
	}
}

// TestCallerInvariantThatNeverPassesExhaustsTheRepairBudget proves a caller
// invariant is not merely consulted once: it is re-run on every repaired
// attempt, and a caller rule that never passes exhausts the same budget a
// Decode failure would.
func TestCallerInvariantThatNeverPassesExhaustsTheRepairBudget(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "anything", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	neverPasses := func(string) error { return errors.New("this rule can never be satisfied") }

	_, repair, err := RunOp(context.Background(), echoOp(neverPasses), "x", types.OpOptions{})
	if err == nil {
		t.Fatal("RunOp accepted an answer that never satisfied the caller's invariant")
	}
	if repair.Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2 (one try, one repair, the default budget)", repair.Attempts)
	}
	if len(repair.Failures) != 2 {
		t.Fatalf("Failures = %v, want one recorded per attempt", repair.Failures)
	}
}

// TestMultipleCallerInvariantsAllMustPass proves invariants run in order and
// every one has to hold -- a caller registering two rules gets an AND, not a
// last-one-wins.
func TestMultipleCallerInvariantsAllMustPass(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "ok", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	firstRan, secondRan := false, false
	first := func(string) error { firstRan = true; return errors.New("first rule rejects everything") }
	second := func(string) error { secondRan = true; return nil }

	_, _, err := RunOp(context.Background(), echoOp(first, second), "x", types.OpOptions{})
	if err == nil {
		t.Fatal("RunOp accepted an answer the first invariant rejected")
	}
	if !firstRan {
		t.Fatal("the first invariant never ran")
	}
	if secondRan {
		t.Fatal("the second invariant ran after the first already failed -- invariants should short-circuit in order")
	}
}

// TestCallerInvariantRepairVisibleInEnvelope is the added Verify line,
// verbatim: "a caller-supplied invariant failing on the first attempt and
// passing on the second is visible in the envelope as a repair, with both
// attempts billed." It asserts on types.Meta's own numbers -- Attempts and
// Repairs -- not on the returned value, which is what "visible in the
// envelope" requires: RepairResult is not the envelope every other operation
// reports through.
//
// It runs through a real llm.Provider (scriptedProvider) via WithProvider,
// not setLLMCaller: the custom-caller test hook bypasses CallLLM entirely, so
// it never calls publishCallRecord, and the envelope would report zero
// attempts no matter what RunOp did. Going through the real provider path is
// what makes "billed" checkable at all.
func TestCallerInvariantRepairVisibleInEnvelope(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"draft", "final"}}
	ctx := WithProvider(context.Background(), provider)

	mustBeFinal := func(value string) error {
		if value != "final" {
			return fmt.Errorf("the answer was not yet final")
		}
		return nil
	}

	result, err := RunOpResult(ctx, echoOp(mustBeFinal), "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Value != "final" {
		t.Fatalf("Value = %q, want %q", result.Value, "final")
	}

	// Both attempts billed.
	if result.Meta.Attempts != 2 {
		t.Fatalf("Meta.Attempts = %d, want 2 (both attempts billed)", result.Meta.Attempts)
	}
	// Visible as a repair.
	if result.Meta.Repairs != 1 {
		t.Fatalf("Meta.Repairs = %d, want 1", result.Meta.Repairs)
	}
	if provider.callCount() != 2 {
		t.Fatalf("the provider was called %d times, want 2", provider.callCount())
	}
}

// TestCallerInvariantFirstTryReportsNoRepairInEnvelope is the companion case:
// an answer that satisfies the caller's invariant immediately reports one
// attempt and zero repairs, so "visible as a repair" is a real distinction
// and not just Attempts always being nonzero.
func TestCallerInvariantFirstTryReportsNoRepairInEnvelope(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"final"}}
	ctx := WithProvider(context.Background(), provider)

	mustBeFinal := func(value string) error {
		if value != "final" {
			return fmt.Errorf("the answer was not yet final")
		}
		return nil
	}

	result, err := RunOpResult(ctx, echoOp(mustBeFinal), "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Meta.Attempts != 1 {
		t.Fatalf("Meta.Attempts = %d, want 1", result.Meta.Attempts)
	}
	if result.Meta.Repairs != 0 {
		t.Fatalf("Meta.Repairs = %d, want 0 on a first-try success", result.Meta.Repairs)
	}
}

// --- A-011: the diagnostic sink.

// recordingSink is a DiagnosticSink that keeps every record it was handed,
// for a test to inspect.
type recordingSink struct {
	mu      sync.Mutex
	records []types.DiagnosticRecord
}

func (s *recordingSink) Capture(record types.DiagnosticRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *recordingSink) all() []types.DiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]types.DiagnosticRecord(nil), s.records...)
}

// TestNoSinkCapturesNothing is half the added Verify line: "with no sink
// configured nothing is captured." No WithDiagnosticSink call at all, so
// captureDiagnostic must return the zero DiagnosticRef and the OperationError
// RunOp builds on a validation exhaustion must not carry a reference.
func TestNoSinkCapturesNothing(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "never satisfies the rule", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	rejectAlways := func(string) error { return errors.New("rejected") }

	_, _, err := RunOp(context.Background(), echoOp(rejectAlways), "x", types.OpOptions{})
	if err == nil {
		t.Fatal("expected a failure")
	}

	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error is not an *types.OperationError: %T %v", err, err)
	}
	if !opErr.Diagnostic.IsZero() {
		t.Fatalf("Diagnostic = %+v, want the zero value with no sink configured", opErr.Diagnostic)
	}
	if strings.Contains(err.Error(), "diagnostic ") {
		t.Fatalf("error text mentions a diagnostic reference with no sink configured: %q", err.Error())
	}
}

// TestSinkCapturesTruncatedRedactedBodyAndErrorCarriesReference is the other
// half: "with one, the error carries a reference and the captured body is
// truncated and scrubbed of credentials."
func TestSinkCapturesTruncatedRedactedBodyAndErrorCarriesReference(t *testing.T) {
	sink := &recordingSink{}
	ctx := WithDiagnosticSink(context.Background(), sink, types.DiagnosticPolicy{MaxBodyBytes: 32})

	leaking := "sk-ant-abcdefghijklmnopqrstuvwxyz0123456789 plus a very long trailing tail that exceeds the configured bound" // secret-scan: allow
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return leaking, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	rejectAlways := func(string) error { return errors.New("rejected") }

	_, _, err := RunOp(ctx, echoOp(rejectAlways), "x", types.OpOptions{})
	if err == nil {
		t.Fatal("expected a failure")
	}

	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error is not an *types.OperationError: %T %v", err, err)
	}
	if opErr.Diagnostic.IsZero() {
		t.Fatal("Diagnostic is zero with a sink configured")
	}
	if !strings.Contains(err.Error(), opErr.Diagnostic.ID) {
		t.Fatalf("error text does not carry the diagnostic ID: %q", err.Error())
	}

	records := sink.all()
	if len(records) != 1 {
		// RunOp captures once, on the final validation exhaustion, with the
		// last rejected attempt's body -- not once per attempt. A caller
		// re-reading every intermediate rejection is not what "referenced
		// from the ordinary error by ID and content digest" (singular) asks
		// for, and capturing every attempt would multiply a bounded-but-
		// nonzero cost (redaction, a digest, a sink call) by the repair
		// budget for no corresponding benefit: the error is about the last
		// answer, not the history of rejected ones.
		t.Fatalf("sink captured %d records, want 1 (the final rejected attempt)", len(records))
	}
	last := records[len(records)-1]
	if last.ID != opErr.Diagnostic.ID || last.Digest != opErr.Diagnostic.Digest {
		t.Fatalf("the error's reference does not match the last captured record: err=%+v record=%+v",
			opErr.Diagnostic, last)
	}
	if !last.Truncated {
		t.Fatal("a body longer than MaxBodyBytes was not marked truncated")
	}
	if len(last.Body) > 32 {
		t.Fatalf("captured body is %d bytes, longer than the configured 32-byte bound", len(last.Body))
	}
	if strings.Contains(last.Body, "abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("captured body still contains the credential: %q", last.Body)
	}
}

// TestCaptureDiagnosticRedactsSeveralCredentialShapes exercises
// types.CaptureDiagnostic directly across the shapes diagnosticPatterns
// names, so the redaction list is proven independent of the repair-loop
// wiring above.
func TestCaptureDiagnosticRedactsSeveralCredentialShapes(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		marker string
	}{
		{"openai key", "the key is sk-proj-abcdefghijklmnopqrstuvwxyz and nothing else", "sk-proj-abcdefghijklmnopqrstuvwxyz"}, // secret-scan: allow
		{"anthropic key", "sk-ant-abcdefghijklmnopqrstuvwxyz0123456789", "sk-ant-abcdefghijklmnopqrstuvwxyz0123456789"},        // secret-scan: allow
		{"aws key", "AKIAABCDEFGHIJKLMNOP is the access key", "AKIAABCDEFGHIJKLMNOP"},                                          // secret-scan: allow
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"},               // secret-scan: allow
		{"bearer header", `Authorization: Bearer abc.def.ghi`, "abc.def.ghi"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			ref := types.CaptureDiagnostic(sink, types.DiagnosticPolicy{}, "op", types.KindMalformedOutput, tc.body)
			if ref.IsZero() {
				t.Fatal("CaptureDiagnostic returned the zero reference with a sink configured")
			}
			records := sink.all()
			if len(records) != 1 {
				t.Fatalf("captured %d records, want 1", len(records))
			}
			if strings.Contains(records[0].Body, tc.marker) {
				t.Fatalf("captured body still contains the credential shape %q: %q", tc.marker, records[0].Body)
			}
		})
	}
}

// TestCaptureDiagnosticWithNilSinkCapturesNothing pins the off-by-default
// behaviour at the types package level directly, independent of the ops
// wiring: a nil sink is the ordinary, unconfigured case.
func TestCaptureDiagnosticWithNilSinkCapturesNothing(t *testing.T) {
	ref := types.CaptureDiagnostic(nil, types.DiagnosticPolicy{}, "op", types.KindMalformedOutput, "sk-ant-abcdefghijklmnopqrstuvwxyz0123456789") // secret-scan: allow
	if !ref.IsZero() {
		t.Fatalf("CaptureDiagnostic with a nil sink returned %+v, want the zero value", ref)
	}
}

// TestErrorFromAMarkedPayloadDoesNotContainTheMarker is A-011's original
// Verify line: an error produced from a payload containing a marker string
// does not contain that marker, exercised across the shared error types in
// internal/types/errors.go and the OperationError path RunOp now builds.
func TestErrorFromAMarkedPayloadDoesNotContainTheMarker(t *testing.T) {
	const marker = "MARKER-4111-1111-1111-1111-DO-NOT-LEAK"

	t.Run("ExtractError", func(t *testing.T) {
		err := types.ExtractError{
			InputShape: types.DescribeValue(marker),
			Reason:     "the model returned prose instead of JSON",
		}
		if strings.Contains(err.Error(), marker) {
			t.Fatalf("ExtractError.Error() leaked the payload: %q", err.Error())
		}
	})

	t.Run("OperationError via RunOp on a rejected payload", func(t *testing.T) {
		setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
			return marker, nil
		})
		t.Cleanup(func() { setLLMCaller(nil) })

		rejectAlways := func(string) error { return errors.New("rejected") }
		_, _, err := RunOp(context.Background(), echoOp(rejectAlways), "x", types.OpOptions{})
		if err == nil {
			t.Fatal("expected a failure")
		}
		if strings.Contains(err.Error(), marker) {
			t.Fatalf("RunOp's error leaked the payload: %q", err.Error())
		}
	})

	t.Run("DescribeValue itself", func(t *testing.T) {
		described := types.DescribeValue(marker)
		if strings.Contains(described, marker) {
			t.Fatalf("DescribeValue reproduced the value instead of describing it: %q", described)
		}
	})
}

package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func approvingGate(context.Context, string) (ApprovalOutcome, error) {
	return ApprovalOutcome{Approved: true}, nil
}

func TestApproveRejectsMissingArguments(t *testing.T) {
	step := constantAnswer("x")

	if _, err := Approve[string](context.Background(), nil, approvingGate, nil); err == nil {
		t.Error("a nil step was accepted")
	}
	if _, err := Approve(context.Background(), step, nil, nil); err == nil {
		t.Error("a nil gate was accepted; approval must not have a silent default")
	}
}

func TestApproveHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	step, calls := answeringStep("never")
	if _, err := Approve(ctx, step, approvingGate, nil); err == nil {
		t.Error("a cancelled context still ran the step")
	}
	if *calls != 0 {
		t.Errorf("the step ran %d times under a cancelled context", *calls)
	}
}

func TestApproveReturnsTheValueWhenTheGateApproves(t *testing.T) {
	gateCalled := false
	gate := func(_ context.Context, value string) (ApprovalOutcome, error) {
		gateCalled = true
		if value != "candidate" {
			t.Errorf("gate saw %q, want %q", value, "candidate")
		}
		return ApprovalOutcome{Approved: true}, nil
	}

	value, err := Approve(context.Background(), constantAnswer("candidate"), gate, nil)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if value != "candidate" {
		t.Errorf("value = %q", value)
	}
	if !gateCalled {
		t.Error("the gate never ran")
	}
}

// The core case: rejection is a successful safety outcome, not a failure --
// asserted by checking the sentinel, not just "err != nil".
func TestApproveReturnsAReviewPacketWhenTheGateDeclines(t *testing.T) {
	gate := func(_ context.Context, value string) (ApprovalOutcome, error) {
		return ApprovalOutcome{
			Approved:        false,
			Evidence:        []string{"provider A said X", "provider B said Y"},
			FailedChecks:    []string{"providers disagreed"},
			SuggestedAction: "pick between the two provider answers",
		}, nil
	}

	inputRefs := []string{"invoice.json, 2400 bytes"}
	value, err := Approve(context.Background(), constantAnswer("candidate"), gate, inputRefs)

	if !errors.Is(err, types.ErrReviewRequired) {
		t.Fatalf("err = %v, want errors.Is(err, types.ErrReviewRequired)", err)
	}
	if value != "" {
		t.Errorf("value = %q, want the zero value on a declined approval", value)
	}

	var reviewErr *types.ReviewRequiredError[string]
	if !errors.As(err, &reviewErr) {
		t.Fatalf("errors.As did not reach *types.ReviewRequiredError[string]: %v", err)
	}
	packet := reviewErr.Packet
	if packet.Candidate != "candidate" {
		t.Errorf("Packet.Candidate = %q, want the declined value so a reviewer can see it", packet.Candidate)
	}
	if len(packet.InputRefs) != 1 || packet.InputRefs[0] != inputRefs[0] {
		t.Errorf("Packet.InputRefs = %v, want %v", packet.InputRefs, inputRefs)
	}
	if len(packet.Evidence) != 2 {
		t.Errorf("Packet.Evidence = %v, want the gate's two entries", packet.Evidence)
	}
	if len(packet.FailedChecks) != 1 || packet.FailedChecks[0] != "providers disagreed" {
		t.Errorf("Packet.FailedChecks = %v", packet.FailedChecks)
	}
	if packet.Attempts != 1 {
		t.Errorf("Packet.Attempts = %d, want 1", packet.Attempts)
	}
	if packet.SuggestedAction == "" {
		t.Error("SuggestedAction was dropped")
	}
}

func TestApproveErrorMessageOmitsTheCandidate(t *testing.T) {
	gate := func(_ context.Context, value string) (ApprovalOutcome, error) {
		return ApprovalOutcome{Approved: false, FailedChecks: []string{"policy requires review"}}, nil
	}
	_, err := Approve(context.Background(), constantAnswer("secret payload contents"), gate, nil)
	if err == nil {
		t.Fatal("expected a review-required error")
	}
	if wantAbsent := "secret payload contents"; containsSubstring(err.Error(), wantAbsent) {
		t.Errorf("Error() leaked the candidate: %q", err.Error())
	}
}

// A gate that itself errors (a bug, a policy service outage) is a different
// fact from a gate that declines, and must not be reported as a review.
func TestApproveGateErrorPropagatesAndIsNotAnAbstention(t *testing.T) {
	boom := errors.New("policy service unreachable")
	gate := func(context.Context, string) (ApprovalOutcome, error) {
		return ApprovalOutcome{}, boom
	}

	_, err := Approve(context.Background(), constantAnswer("candidate"), gate, nil)
	if err == nil {
		t.Fatal("a gate failure was reported as success")
	}
	if errors.Is(err, types.ErrReviewRequired) {
		t.Error("a gate malfunction was reported as review-required")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying gate error was lost: %v", err)
	}
}

func TestApproveNonReviewableStepFailurePropagatesUnchanged(t *testing.T) {
	original := &types.OperationError{Kind: types.KindAuthentication, Message: "bad credential"}
	step := failingAnswer(original)

	gateCalled := false
	gate := func(context.Context, string) (ApprovalOutcome, error) {
		gateCalled = true
		return ApprovalOutcome{Approved: true}, nil
	}

	_, err := Approve(context.Background(), step, gate, nil)
	if !errors.Is(err, types.ErrAuthentication) {
		t.Fatalf("err = %v, want the authentication error preserved", err)
	}
	if errors.Is(err, types.ErrReviewRequired) {
		t.Error("a configuration-class failure was repackaged as review-required")
	}
	if gateCalled {
		t.Error("the gate ran after the step failed")
	}
}

// The generalisation the Revised line asks for: a step that failed because its
// own budgeted regeneration ran out becomes a review outcome here, the same
// shape as an explicit gate decline.
func TestApproveTurnsExhaustedRecoveryIntoAReviewPacket(t *testing.T) {
	cases := []struct {
		name string
		kind types.ErrorKind
	}{
		{"repair exhausted", types.KindRepairExhausted},
		{"invariant violation", types.KindInvariantViolation},
		{"evidence violation", types.KindEvidenceViolation},
		{"schema violation", types.KindSchemaViolation},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := failingAnswer(&types.OperationError{Kind: tc.kind, Message: "gave up"})
			gateCalled := false
			gate := func(context.Context, string) (ApprovalOutcome, error) {
				gateCalled = true
				return ApprovalOutcome{Approved: true}, nil
			}

			_, err := Approve(context.Background(), step, gate, []string{"note.txt, 40 bytes"})
			if !errors.Is(err, types.ErrReviewRequired) {
				t.Fatalf("err = %v, want errors.Is(err, types.ErrReviewRequired)", err)
			}
			if gateCalled {
				t.Error("the gate ran even though the step produced no candidate")
			}

			var reviewErr *types.ReviewRequiredError[string]
			if !errors.As(err, &reviewErr) {
				t.Fatalf("errors.As did not reach the packet: %v", err)
			}
			if len(reviewErr.Packet.InputRefs) != 1 {
				t.Errorf("InputRefs = %v, want the caller's ref carried through", reviewErr.Packet.InputRefs)
			}
			if len(reviewErr.Packet.FailedChecks) == 0 {
				t.Error("FailedChecks is empty on an exhausted-recovery review")
			}
		})
	}
}

// Retryable failures (a timeout, rate limiting) are not "exhausted recovery" --
// there was no budgeted regeneration to exhaust, just a transport failure --
// so they must not become a review outcome either.
func TestApproveRetryableStepFailurePropagatesUnchanged(t *testing.T) {
	step := failingAnswer(&types.OperationError{Kind: types.KindTimeout, Message: "slow"})
	_, err := Approve(context.Background(), step, approvingGate, nil)
	if errors.Is(err, types.ErrReviewRequired) {
		t.Error("a timeout was reported as review-required")
	}
	if !errors.Is(err, types.ErrTimeout) {
		t.Errorf("err = %v, want the timeout classification preserved", err)
	}
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return len(needle) == 0
}

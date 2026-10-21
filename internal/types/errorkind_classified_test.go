package types

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var errClassifiedInner = errors.New("inner failure")

func TestClassifiedKeepsTextAndChain(t *testing.T) {
	operationErr := Newf(KindPolicyViolation, "choose", "the route is forbidden")
	cases := []struct {
		name string
		kind ErrorKind
		err  error
	}{
		{"a plain error keeps its message", KindConfiguration, errors.New("provider returned empty completion content")},
		{"an error wrapping a sentinel keeps the sentinel reachable", KindBudgetExceeded, fmt.Errorf("%w: limit 5.00", errClassifiedInner)},
		{"an operation error keeps its own text", KindProviderUnavailable, operationErr},
		{"a joined error keeps both messages", KindMalformedOutput, errors.Join(errors.New("first"), errors.New("second"))},
		{"an empty message stays empty", KindTimeout, errors.New("")},
		{"percent signs in the text are not formatted again", KindInvariantViolation, errors.New("rate 100% of %s")},
		{"a context error stays reachable", KindTimeout, fmt.Errorf("request: %w", context.DeadlineExceeded)},
		{"a cancelled context stays reachable", KindCanceled, context.Canceled},
		{"a kind sentinel as the inner error stays reachable", KindRateLimited, ErrRateLimited},
		{"an error classified already keeps its text under a new kind", KindShutdown, Classified(KindTimeout, errors.New("late"))},
		{"a long message is not cut", KindSchemaViolation, errors.New(string(make([]byte, 4096)))},
		{"a message with a newline keeps it", KindMalformedOutput, errors.New("line one\nline two")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classified(tc.kind, tc.err)
			if got == nil {
				t.Fatal("Classified returned nil for a non-nil error")
			}
			if got.Error() != tc.err.Error() {
				t.Errorf("text changed: got %q, want %q", got.Error(), tc.err.Error())
			}
			if errors.Unwrap(got) != tc.err {
				t.Errorf("Unwrap did not return the error it was given")
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("errors.Is no longer reaches the error it wraps")
			}
			if KindOf(got) != tc.kind {
				t.Errorf("KindOf = %v, want %v", KindOf(got), tc.kind)
			}
		})
	}

	t.Run("a nil error stays nil", func(t *testing.T) {
		if got := Classified(KindTimeout, nil); got != nil {
			t.Errorf("Classified(nil) = %v, want nil", got)
		}
	})
	t.Run("an inner sentinel still matches beside the new kind", func(t *testing.T) {
		got := Classified(KindBudgetExceeded, fmt.Errorf("%w: limit 5.00", errClassifiedInner))
		if !errors.Is(got, errClassifiedInner) || !errors.Is(got, ErrBudgetExceeded) {
			t.Errorf("want both the inner sentinel and the kind's sentinel to match")
		}
	})
	t.Run("an inner operation error is still found by errors.As", func(t *testing.T) {
		var found *OperationError
		if !errors.As(Classified(KindProviderUnavailable, operationErr), &found) || found != operationErr {
			t.Errorf("errors.As did not reach the inner operation error")
		}
	})
	t.Run("the unknown kind returns the error as it was", func(t *testing.T) {
		in := errors.New("plain")
		if got := Classified(KindUnknown, in); got != in {
			t.Errorf("Classified(KindUnknown) wrapped the error")
		}
	})
	t.Run("a kind beyond the last returns the error as it was", func(t *testing.T) {
		in := errors.New("plain")
		if got := Classified(lastKind+1, in); got != in {
			t.Errorf("Classified(lastKind+1) wrapped the error")
		}
	})
}

func TestClassifiedMatchesItsSentinel(t *testing.T) {
	kinds := everyKind()
	if len(kinds) < 10 {
		t.Fatalf("the table covers only %d kinds", len(kinds))
	}
	for _, kind := range kinds {
		t.Run("the "+kind.String()+" kind matches its own sentinel and no other", func(t *testing.T) {
			got := Classified(kind, errors.New("failure"))
			if KindOf(got) != kind {
				t.Errorf("KindOf = %v, want %v", KindOf(got), kind)
			}
			if !errors.Is(got, sentinelFor(kind)) {
				t.Errorf("errors.Is does not match the sentinel of %v", kind)
			}
			for _, other := range kinds {
				if other != kind && errors.Is(got, sentinelFor(other)) {
					t.Errorf("a %v error matches the sentinel of %v", kind, other)
				}
			}
		})
	}

	t.Run("a classified call error matches its sentinel too", func(t *testing.T) {
		got := ClassifiedCall(KindRateLimited, errors.New("429"), "openai", "m", "req-1", time.Second)
		if !errors.Is(got, ErrRateLimited) || KindOf(got) != KindRateLimited {
			t.Errorf("a call error did not report its kind")
		}
	})
	t.Run("wrapping with fmt.Errorf keeps the match", func(t *testing.T) {
		got := fmt.Errorf("op: %w", Classified(KindTimeout, errors.New("late")))
		if !errors.Is(got, ErrTimeout) || KindOf(got) != KindTimeout {
			t.Errorf("the kind was lost through a wrapper")
		}
	})
	t.Run("a joined error matches through either branch", func(t *testing.T) {
		got := errors.Join(errors.New("other"), Classified(KindCircuitOpen, errors.New("open")))
		if !errors.Is(got, ErrCircuitOpen) || KindOf(got) != KindCircuitOpen {
			t.Errorf("the kind was lost through a join")
		}
	})
	t.Run("the outermost kind decides KindOf and both sentinels match", func(t *testing.T) {
		got := Classified(KindProviderUnavailable, Newf(KindTimeout, "call", "slow"))
		if KindOf(got) != KindProviderUnavailable {
			t.Errorf("KindOf = %v, want the outer kind", KindOf(got))
		}
		if !errors.Is(got, ErrProviderUnavailable) || !errors.Is(got, ErrTimeout) {
			t.Errorf("errors.Is should reach the outer and the inner sentinel")
		}
	})
	t.Run("an error classified twice reports the second kind", func(t *testing.T) {
		got := Classified(KindConfiguration, Classified(KindTimeout, errors.New("x")))
		if KindOf(got) != KindConfiguration {
			t.Errorf("KindOf = %v, want configuration", KindOf(got))
		}
	})
	t.Run("a classified error is not another error of the same kind", func(t *testing.T) {
		a := Classified(KindTimeout, errors.New("a"))
		b := Classified(KindTimeout, errors.New("b"))
		if errors.Is(a, b) {
			t.Errorf("two classified errors of one kind compare equal")
		}
	})
	t.Run("an error classified as nothing known matches no sentinel", func(t *testing.T) {
		got := Classified(KindUnknown, errors.New("x"))
		for _, kind := range kinds {
			if errors.Is(got, sentinelFor(kind)) {
				t.Errorf("an unclassified error matches the sentinel of %v", kind)
			}
		}
	})
}

func TestCallOf(t *testing.T) {
	inner := ClassifiedCall(KindProviderUnavailable, errors.New("503"), "anthropic", "claude-x", "req-9", 7*time.Second)
	cases := []struct {
		name       string
		err        error
		provider   string
		model      string
		requestID  string
		retryAfter time.Duration
		ok         bool
	}{
		{"a call error reports its route", ClassifiedCall(KindRateLimited, errors.New("429"), "openai", "gpt-x", "req-1", 3*time.Second), "openai", "gpt-x", "req-1", 3 * time.Second, true},
		{"a nil error has no route", nil, "", "", "", 0, false},
		{"a plain error has no route", errors.New("plain"), "", "", "", 0, false},
		{"a classified error with no route has none", Classified(KindTimeout, errors.New("late")), "", "", "", 0, false},
		{"an operation error is not read for a route", &OperationError{Kind: KindTimeout, Provider: "p", Model: "m", RequestID: "r"}, "", "", "", 0, false},
		{"a wrapped call error is found", fmt.Errorf("op: %w", inner), "anthropic", "claude-x", "req-9", 7 * time.Second, true},
		{"a call error in a joined error is found", errors.Join(errors.New("x"), inner), "anthropic", "claude-x", "req-9", 7 * time.Second, true},
		{"a classified error over a call error reports the inner route", Classified(KindConfiguration, inner), "anthropic", "claude-x", "req-9", 7 * time.Second, true},
		{"of two call errors the outermost route is reported", ClassifiedCall(KindTimeout, inner, "openai", "gpt-y", "req-2", 0), "openai", "gpt-y", "req-2", 0, true},
		{"a call error with an empty route still reports one was recorded", ClassifiedCall(KindTimeout, errors.New("x"), "", "", "", 0), "", "", "", 0, true},
		{"a zero retry-after is reported as zero", ClassifiedCall(KindTimeout, errors.New("x"), "p", "m", "r", 0), "p", "m", "r", 0, true},
		{"a call error with an unknown kind is returned as it was and has no route", ClassifiedCall(KindUnknown, errors.New("x"), "p", "m", "r", time.Second), "", "", "", 0, false},
		{"a nil error with a route stays nil and has none", ClassifiedCall(KindTimeout, nil, "p", "m", "r", time.Second), "", "", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, model, requestID, retryAfter, ok := CallOf(tc.err)
			if provider != tc.provider || model != tc.model || requestID != tc.requestID || retryAfter != tc.retryAfter || ok != tc.ok {
				t.Errorf("CallOf = (%q, %q, %q, %v, %v), want (%q, %q, %q, %v, %v)",
					provider, model, requestID, retryAfter, ok,
					tc.provider, tc.model, tc.requestID, tc.retryAfter, tc.ok)
			}
		})
	}

	t.Run("a call error keeps text and chain like a classified one", func(t *testing.T) {
		cause := errors.New("503 from the provider")
		got := ClassifiedCall(KindProviderUnavailable, cause, "p", "m", "r", 0)
		if got.Error() != cause.Error() || errors.Unwrap(got) != cause || KindOf(got) != KindProviderUnavailable {
			t.Errorf("a call error changed the text, the chain or the kind")
		}
	})
}

func TestNarrowingOf(t *testing.T) {
	refusal := &OperationError{Kind: KindPolicyViolation, Op: "choose", Message: "declined", Narrow: NarrowRefusal}
	panicked := &OperationError{Kind: KindInvariantViolation, Op: "flow", Message: "step panicked", Narrow: NarrowPanic}
	var typedNil *OperationError
	cases := []struct {
		name string
		err  error
		want Narrowing
	}{
		{"a nil error is unmarked", nil, NarrowNone},
		{"a plain error is unmarked", errors.New("plain"), NarrowNone},
		{"an operation error with no mark is unmarked", Newf(KindPolicyViolation, "op", "x"), NarrowNone},
		{"a refusal is marked as one", refusal, NarrowRefusal},
		{"a panic is marked as one", panicked, NarrowPanic},
		{"a wrapped refusal is found", fmt.Errorf("op: %w", refusal), NarrowRefusal},
		{"a refusal under a classified error is found", Classified(KindPolicyViolation, refusal), NarrowRefusal},
		{"a panic in a joined error is found", errors.Join(errors.New("x"), panicked), NarrowPanic},
		{"the outermost operation error decides", &OperationError{Kind: KindInvariantViolation, Narrow: NarrowPanic, Cause: refusal}, NarrowPanic},
		{"an unmarked outer operation error hides the mark inside it", &OperationError{Kind: KindConfiguration, Cause: refusal}, NarrowNone},
		{"a typed nil operation error is unmarked", typedNil, NarrowNone},
		{"a sentinel is unmarked", ErrPolicyViolation, NarrowNone},
		{"a classified plain error is unmarked", Classified(KindPolicyViolation, errors.New("x")), NarrowNone},
		{"a classified refusal is marked as one", ClassifiedRefusal(errors.New("x")), NarrowRefusal},
		{"a classified refusal under a wrapper is found", fmt.Errorf("op: %w", ClassifiedRefusal(errors.New("x"))), NarrowRefusal},
		{"a classified refusal under a classified error is found", Classified(KindPolicyViolation, ClassifiedRefusal(errors.New("x"))), NarrowRefusal},
		{"a classified refusal under a call error is found", ClassifiedCall(KindPolicyViolation, ClassifiedRefusal(errors.New("x")), "p", "m", "r", 0), NarrowRefusal},
		{"a classified refusal in a joined error is found", errors.Join(errors.New("y"), ClassifiedRefusal(errors.New("x"))), NarrowRefusal},
		{"an unmarked outer operation error hides a classified refusal", &OperationError{Kind: KindConfiguration, Cause: ClassifiedRefusal(errors.New("x"))}, NarrowNone},
		{"a marked outer operation error is not hidden by a classified refusal", &OperationError{Kind: KindInvariantViolation, Narrow: NarrowPanic, Cause: ClassifiedRefusal(errors.New("x"))}, NarrowPanic},
		{"a classified refusal over a panic is a refusal", ClassifiedRefusal(panicked), NarrowRefusal},
		{"a nil error stays nil", ClassifiedRefusal(nil), NarrowNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NarrowingOf(tc.err); got != tc.want {
				t.Errorf("NarrowingOf = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("the mark changes neither the kind nor the text nor the sentinel", func(t *testing.T) {
		plain := &OperationError{Kind: KindPolicyViolation, Op: "choose", Message: "declined"}
		if refusal.Error() != plain.Error() {
			t.Errorf("the mark changed the text: %q vs %q", refusal.Error(), plain.Error())
		}
		if KindOf(refusal) != KindPolicyViolation || !errors.Is(refusal, ErrPolicyViolation) {
			t.Errorf("the mark changed the kind or the sentinel")
		}
	})
	t.Run("the marks are three distinct values and the zero is none", func(t *testing.T) {
		if NarrowNone != 0 || NarrowRefusal == NarrowNone || NarrowPanic == NarrowNone || NarrowRefusal == NarrowPanic {
			t.Errorf("the marks are not distinct: %d %d %d", NarrowNone, NarrowRefusal, NarrowPanic)
		}
	})
}

func TestClassifiedRefusal(t *testing.T) {
	cause := fmt.Errorf("provider returned empty completion content (%w)", errors.New("filtered"))
	got := ClassifiedRefusal(cause)

	t.Run("it is a policy violation marked as a refusal", func(t *testing.T) {
		if KindOf(got) != KindPolicyViolation || NarrowingOf(got) != NarrowRefusal {
			t.Errorf("kind %v, mark %d", KindOf(got), NarrowingOf(got))
		}
	})
	t.Run("errors.Is matches the policy sentinel and the cause", func(t *testing.T) {
		if !errors.Is(got, ErrPolicyViolation) || !errors.Is(got, cause) {
			t.Error("the sentinel or the cause is not matched")
		}
	})
	t.Run("it matches no other kind's sentinel", func(t *testing.T) {
		if errors.Is(got, ErrMalformedOutput) || errors.Is(got, ErrProviderUnavailable) {
			t.Error("a refusal matched another kind")
		}
	})
	t.Run("the text and the chain are the cause's", func(t *testing.T) {
		if got.Error() != cause.Error() || errors.Unwrap(got) != cause {
			t.Errorf("text %q, unwrap %v", got.Error(), errors.Unwrap(got))
		}
	})
	t.Run("a nil error stays nil", func(t *testing.T) {
		if ClassifiedRefusal(nil) != nil {
			t.Error("ClassifiedRefusal(nil) is not nil")
		}
	})
	t.Run("it reads as a refusal whatever kind the cause already has", func(t *testing.T) {
		over := ClassifiedRefusal(Classified(KindMalformedOutput, errors.New("empty")))
		if KindOf(over) != KindPolicyViolation || NarrowingOf(over) != NarrowRefusal || !errors.Is(over, ErrMalformedOutput) {
			t.Errorf("kind %v, mark %d, inner sentinel kept %v", KindOf(over), NarrowingOf(over), errors.Is(over, ErrMalformedOutput))
		}
	})
	t.Run("the mark is read by the carrier's own accessor", func(t *testing.T) {
		marked, ok := got.(interface{ Narrowing() Narrowing })
		if !ok || marked.Narrowing() != NarrowRefusal {
			t.Errorf("accessor %v ok %v", marked, ok)
		}
		unmarked, ok := Classified(KindPolicyViolation, errors.New("x")).(interface{ Narrowing() Narrowing })
		if !ok || unmarked.Narrowing() != NarrowNone {
			t.Errorf("an unmarked classified error reports a mark")
		}
	})
	t.Run("a call error keeps the route and is not marked", func(t *testing.T) {
		call := ClassifiedCall(KindPolicyViolation, errors.New("x"), "p", "m", "r", 0)
		if NarrowingOf(call) != NarrowNone {
			t.Errorf("a call error is marked %d", NarrowingOf(call))
		}
	})
}

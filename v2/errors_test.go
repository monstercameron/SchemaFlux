package schemaflux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// canary is text planted where a provider, a model or a caller could put it. No
// test may find it in anything a log line or an error string is made of.
const canary = "CANARY-7f3a91c2"

// allKinds lists the eighteen kinds in declaration order.
var allKinds = []Kind{
	ErrConfiguration, ErrInvalidRequest, ErrUnsupported, ErrUnauthorized, ErrPolicy, ErrBudgetExceeded,
	ErrBusy, ErrRateLimited, ErrUnavailable, ErrTimeout, ErrCanceled, ErrClosed, ErrTooLarge,
	ErrTruncated, ErrRefused, ErrInvalidAnswer, ErrReviewRequired, ErrInternal,
}

// vendorError is the error of a provider that put the canary in everything the
// vendor writes.
func vendorError(status int) *llm.APIError {
	return &llm.APIError{
		Provider:   "openai",
		Model:      "gpt-6-luna",
		StatusCode: status,
		Message:    "the passage " + canary + " was rejected",
		Type:       "invalid_request_error",
		Code:       "context_length_exceeded",
		Body:       `{"error":{"message":"` + canary + `"}}`,
	}
}

func TestKindsAreStable(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ERROR_DETAIL", "")
	want := []struct {
		kind  Kind
		value uint8
		name  string
	}{
		{ErrConfiguration, 1, "configuration"},
		{ErrInvalidRequest, 2, "invalid request"},
		{ErrUnsupported, 3, "unsupported"},
		{ErrUnauthorized, 4, "unauthorized"},
		{ErrPolicy, 5, "policy"},
		{ErrBudgetExceeded, 6, "budget exceeded"},
		{ErrBusy, 7, "busy"},
		{ErrRateLimited, 8, "rate limited"},
		{ErrUnavailable, 9, "unavailable"},
		{ErrTimeout, 10, "timeout"},
		{ErrCanceled, 11, "canceled"},
		{ErrClosed, 12, "closed"},
		{ErrTooLarge, 13, "too large"},
		{ErrTruncated, 14, "truncated"},
		{ErrRefused, 15, "refused"},
		{ErrInvalidAnswer, 16, "invalid answer"},
		{ErrReviewRequired, 17, "review required"},
		{ErrInternal, 18, "internal"},
	}
	if len(want) != len(allKinds) {
		t.Fatalf("the test pins %d kinds and the package lists %d", len(want), len(allKinds))
	}
	for _, c := range want {
		t.Run("the kind "+c.name+" keeps its number and its name", func(t *testing.T) {
			if uint8(c.kind) != c.value {
				t.Errorf("%q has the value %d, want %d: a renumbering breaks stored logs", c.name, uint8(c.kind), c.value)
			}
			if got := c.kind.Error(); got != c.name {
				t.Errorf("Error() = %q, want %q", got, c.name)
			}
			text, err := c.kind.MarshalText()
			if err != nil || string(text) != c.name {
				t.Errorf("MarshalText() = %q, %v, want %q", text, err, c.name)
			}
			if !c.kind.valid() {
				t.Errorf("%q is not valid", c.name)
			}
		})
	}
	for _, k := range []Kind{0, 19, 100, 255} {
		t.Run(fmt.Sprintf("the value %d is unknown", k), func(t *testing.T) {
			if got := k.Error(); got != "unknown" {
				t.Errorf("Error() = %q, want unknown", got)
			}
			if k.valid() {
				t.Errorf("%d is valid", k)
			}
		})
	}
}

func TestKindIsAnErrorThatErrorsIsFinds(t *testing.T) {
	t.Parallel()
	for _, k := range allKinds {
		t.Run("a bare "+k.Error()+" is found by errors.Is and compares equal", func(t *testing.T) {
			var err error = k
			if !errors.Is(err, k) {
				t.Errorf("errors.Is(%v, %v) = false", err, k)
			}
			if err != k {
				t.Errorf("the kind does not compare equal to itself")
			}
			for _, other := range allKinds {
				if other != k && errors.Is(err, other) {
					t.Errorf("errors.Is(%v, %v) = true", k, other)
				}
			}
		})
	}
}

func TestEveryInternalKindBecomesItsKind(t *testing.T) {
	t.Parallel()
	want := map[types.ErrorKind]Kind{
		types.KindConfiguration:          ErrConfiguration,
		types.KindAuthentication:         ErrUnauthorized,
		types.KindPermission:             ErrUnauthorized,
		types.KindInvalidRequest:         ErrInvalidRequest,
		types.KindPolicyViolation:        ErrPolicy,
		types.KindBudgetExceeded:         ErrBudgetExceeded,
		types.KindAdmissionRejected:      ErrBusy,
		types.KindRateLimited:            ErrRateLimited,
		types.KindProviderUnavailable:    ErrUnavailable,
		types.KindTimeout:                ErrTimeout,
		types.KindCanceled:               ErrCanceled,
		types.KindCircuitOpen:            ErrUnavailable,
		types.KindShutdown:               ErrClosed,
		types.KindContextTooLarge:        ErrTooLarge,
		types.KindOutputTruncated:        ErrTruncated,
		types.KindMalformedOutput:        ErrInvalidAnswer,
		types.KindSchemaViolation:        ErrInvalidAnswer,
		types.KindBatchProtocolViolation: ErrInvalidAnswer,
		types.KindInvariantViolation:     ErrInvalidAnswer,
		types.KindEvidenceViolation:      ErrInvalidAnswer,
		types.KindUnsupportedCapability:  ErrUnsupported,
		types.KindRepairExhausted:        ErrInvalidAnswer,
		types.KindReviewRequired:         ErrReviewRequired,
	}
	seen := 0
	for internal := types.KindConfiguration; internal.String() != "unknown"; internal++ {
		seen++
		expected, ok := want[internal]
		if !ok {
			t.Fatalf("the internal kind %q has no entry in this table: say which kind it becomes", internal)
		}
		t.Run("the internal kind "+internal.String()+" becomes "+expected.Error(), func(t *testing.T) {
			err := types.Newf(internal, "extract", "a message of the library's own")
			got := convertError("extract", err)
			var e *Error
			if !errors.As(got, &e) {
				t.Fatalf("convertError returned %T, want *Error", got)
			}
			if e.Kind != expected {
				t.Errorf("Kind = %v, want %v", e.Kind, expected)
			}
			if !errors.Is(got, expected) {
				t.Errorf("errors.Is(err, %v) = false", expected)
			}
			if !errors.Is(got, err) {
				t.Errorf("the internal error is no longer reachable through the chain")
			}
			if e.Op != "extract" {
				t.Errorf("Op = %q, want extract", e.Op)
			}
			if e.Message != "a message of the library's own" {
				t.Errorf("Message = %q", e.Message)
			}
		})
	}
	if seen != len(want) {
		t.Errorf("the table has %d entries and the package has %d internal kinds", len(want), seen)
	}
	t.Run("a kind nobody classified becomes internal", func(t *testing.T) {
		got := convertError("extract", &types.OperationError{Op: "x", Message: "m"})
		if !errors.Is(got, ErrInternal) {
			t.Errorf("got %v, want an internal error", got)
		}
	})
}

func TestTheMarksChangeTheKindTheyAreFiledUnder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"a refusal filed as a policy violation", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal}, ErrRefused},
		{"a policy violation nobody marked", &types.OperationError{Kind: types.KindPolicyViolation}, ErrPolicy},
		{"a recovered panic filed as an invariant violation", &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic}, ErrInternal},
		{"an invariant violation nobody marked", &types.OperationError{Kind: types.KindInvariantViolation}, ErrInvalidAnswer},
		{"a mark outranks the kind it sits on", &types.OperationError{Kind: types.KindRateLimited, Narrow: types.NarrowPanic}, ErrInternal},
		{"a refusal wrapped by fmt", fmt.Errorf("provider: %w", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal}), ErrRefused},
		{"a panic joined with another error", errors.Join(errors.New("x"), &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic}), ErrInternal},
		{"an outer unmarked error hides the mark beneath it", &types.OperationError{Kind: types.KindPolicyViolation, Cause: &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal}}, ErrPolicy},
		{"the trust guard is internal whatever kind it was filed under", types.Classified(types.KindInvariantViolation, fmt.Errorf("%w: segment 1", ops.ErrTrustBoundaryViolated)), ErrInternal},
		{"the other trust guard too", types.Classified(types.KindConfiguration, fmt.Errorf("%w: segment 2", ops.ErrUntrustedInSystemPrompt)), ErrInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e *Error
			if !errors.As(convertError("op", c.err), &e) || e.Kind != c.want {
				t.Errorf("kind = %v, want %v", e, c.want)
			}
		})
	}
}

func TestEveryInternalSentinelBecomesItsKind(t *testing.T) {
	t.Parallel()
	want := map[error]Kind{
		types.ErrConfiguration:          ErrConfiguration,
		types.ErrAuthentication:         ErrUnauthorized,
		types.ErrPermission:             ErrUnauthorized,
		types.ErrInvalidRequest:         ErrInvalidRequest,
		types.ErrPolicyViolation:        ErrPolicy,
		types.ErrBudgetExceeded:         ErrBudgetExceeded,
		types.ErrAdmissionRejected:      ErrBusy,
		types.ErrRateLimited:            ErrRateLimited,
		types.ErrProviderUnavailable:    ErrUnavailable,
		types.ErrTimeout:                ErrTimeout,
		types.ErrCanceled:               ErrCanceled,
		types.ErrCircuitOpen:            ErrUnavailable,
		types.ErrShutdown:               ErrClosed,
		types.ErrContextTooLarge:        ErrTooLarge,
		types.ErrOutputTruncated:        ErrTruncated,
		types.ErrMalformedOutput:        ErrInvalidAnswer,
		types.ErrSchemaViolation:        ErrInvalidAnswer,
		types.ErrBatchProtocolViolation: ErrInvalidAnswer,
		types.ErrInvariantViolation:     ErrInvalidAnswer,
		types.ErrEvidenceViolation:      ErrInvalidAnswer,
		types.ErrUnsupportedCapability:  ErrUnsupported,
		types.ErrRepairExhausted:        ErrInvalidAnswer,
		types.ErrReviewRequired:         ErrReviewRequired,

		ops.ErrNoProvider:              ErrConfiguration,
		config.ErrNoModelMapping:       ErrConfiguration,
		types.ErrEndpointNotAllowed:    ErrConfiguration,
		ops.ErrDataPolicyRefusal:       ErrPolicy,
		ops.ErrContractDegraded:        ErrPolicy,
		ops.ErrRouteRefused:            ErrPolicy,
		ops.ErrSchedulerClosed:         ErrClosed,
		llm.ErrNoMessageOutput:         ErrInvalidAnswer,
		llm.ErrUnrequestedTool:         ErrInvalidAnswer,
		llm.ErrStreamIncomplete:        ErrUnavailable,
		llm.ErrCapabilityUnknown:       ErrUnsupported,
		llm.ErrCapabilityUnmet:         ErrUnsupported,
		ops.ErrTrustBoundaryViolated:   ErrInternal,
		ops.ErrUntrustedInSystemPrompt: ErrInternal,
		pricing.ErrBudgetExceeded:      ErrBudgetExceeded,
		ops.ErrClientBudgetExhausted:   ErrBudgetExceeded,
	}
	for sentinel, expected := range want {
		t.Run(sentinel.Error()+" becomes "+expected.Error(), func(t *testing.T) {
			shapes := map[string]error{
				"bare":        sentinel,
				"wrapped":     fmt.Errorf("while doing x: %w", sentinel),
				"wrapped_2":   fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", sentinel)),
				"joined":      errors.Join(errors.New("first"), sentinel),
				"joined_last": errors.Join(sentinel, errors.New("second")),
			}
			for shape, err := range shapes {
				got := convertError("filter", err)
				var e *Error
				if !errors.As(got, &e) {
					t.Fatalf("%s: convertError returned %T", shape, got)
				}
				if e.Kind != expected {
					t.Errorf("%s: Kind = %v, want %v", shape, e.Kind, expected)
				}
				if !errors.Is(got, expected) {
					t.Errorf("%s: errors.Is(err, %v) = false", shape, expected)
				}
				if !errors.Is(got, sentinel) {
					t.Errorf("%s: the sentinel is no longer reachable", shape)
				}
			}
		})
	}
}

func TestEveryKindIsReachedByErrorsIsThroughWrapping(t *testing.T) {
	t.Parallel()
	for _, k := range allKinds {
		t.Run(k.Error(), func(t *testing.T) {
			original := &Error{Kind: k, Op: "classify"}
			shapes := map[string]error{
				"bare":       original,
				"fmt":        fmt.Errorf("while classifying: %w", original),
				"fmt twice":  fmt.Errorf("a: %w", fmt.Errorf("b: %w", original)),
				"join":       errors.Join(errors.New("x"), original),
				"join first": errors.Join(original, errors.New("x")),
				"converted":  convertError("classify", fmt.Errorf("w: %w", original)),
			}
			for shape, err := range shapes {
				if !errors.Is(err, k) {
					t.Errorf("%s: errors.Is(err, %v) = false", shape, k)
				}
				for _, other := range allKinds {
					if other != k && errors.Is(err, other) {
						t.Errorf("%s: errors.Is(err, %v) = true for a %v", shape, other, k)
					}
				}
			}
		})
	}
}

func TestAContextErrorIsReachable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		want     Kind
		reaching error
	}{
		{"a canceled context", types.Wrap(types.KindCanceled, "llm.call", context.Canceled), ErrCanceled, context.Canceled},
		{"a passed deadline", types.Wrap(types.KindTimeout, "llm.call", context.DeadlineExceeded), ErrTimeout, context.DeadlineExceeded},
		{"a bare cancellation", context.Canceled, ErrCanceled, context.Canceled},
		{"a bare deadline", context.DeadlineExceeded, ErrTimeout, context.DeadlineExceeded},
		{"a cancellation wrapped by fmt", fmt.Errorf("sending: %w", context.Canceled), ErrCanceled, context.Canceled},
		{"a deadline wrapped by fmt", fmt.Errorf("sending: %w", context.DeadlineExceeded), ErrTimeout, context.DeadlineExceeded},
		{"a classified cancellation", types.Classified(types.KindCanceled, context.Canceled), ErrCanceled, context.Canceled},
		{"a wait in the queue that outlasts the deadline", types.Wrap(types.KindAdmissionRejected, "scheduler", context.DeadlineExceeded), ErrBusy, context.DeadlineExceeded},
		{"a deadline in a joined error", errors.Join(errors.New("a"), context.DeadlineExceeded), ErrTimeout, context.DeadlineExceeded},
		{"a cancellation of a request", types.ClassifiedCall(types.KindCanceled, context.Canceled, "openai", "m", "req_1", 0), ErrCanceled, context.Canceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := convertError("extract", c.err)
			if !errors.Is(got, c.reaching) {
				t.Errorf("errors.Is(err, %v) = false", c.reaching)
			}
			if !errors.Is(got, c.want) {
				t.Errorf("errors.Is(err, %v) = false", c.want)
			}
		})
	}
	t.Run("a real canceled context ends as ErrCanceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := convertError("extract", ctx.Err())
		if !errors.Is(got, context.Canceled) || !errors.Is(got, ErrCanceled) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("a real passed deadline ends as ErrTimeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		<-ctx.Done()
		got := convertError("extract", ctx.Err())
		if !errors.Is(got, context.DeadlineExceeded) || !errors.Is(got, ErrTimeout) {
			t.Errorf("got %v", got)
		}
	})
}

func TestErrorTextNamesTheOperationAndNeverTheCause(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ERROR_DETAIL", "")
	cases := []struct {
		name string
		err  *Error
		want string
	}{
		{"the zero error", &Error{}, "unknown"},
		{"a kind alone", &Error{Kind: ErrCanceled}, "canceled"},
		{"an operation and a kind", &Error{Kind: ErrCanceled, Op: "filter"}, "filter: canceled"},
		{"a message", &Error{Kind: ErrConfiguration, Op: "sort", Message: "Threshold must be from 0 to 1"}, "sort: configuration: Threshold must be from 0 to 1"},
		{"a route and a status", &Error{Kind: ErrRateLimited, Op: "classify", Provider: "openai", Model: "gpt-6-luna", Status: 429, RequestID: "req_7f3a"}, "classify: rate limited (openai/gpt-6-luna, status 429, request req_7f3a)"},
		{"a provider without a model", &Error{Kind: ErrUnauthorized, Op: "extract", Provider: "openai", Status: 401}, "extract: unauthorized (openai, status 401)"},
		{"a model without a provider", &Error{Kind: ErrInvalidRequest, Op: "extract", Model: "m1"}, "extract: invalid request (m1)"},
		{"a code", &Error{Kind: ErrTooLarge, Op: "extract", Status: 400, Code: "context_length_exceeded"}, "extract: too large (status 400, context_length_exceeded)"},
		{"everything", &Error{Kind: ErrInvalidAnswer, Op: "extract", Message: "the answer does not fit the type: missing field /total", Provider: "openai", Model: "gpt-6-luna", RequestID: "req_1"}, "extract: invalid answer: the answer does not fit the type: missing field /total (openai/gpt-6-luna, request req_1)"},
		{"a wait is not printed", &Error{Kind: ErrRateLimited, Op: "classify", RetryAfter: 30 * time.Second}, "classify: rate limited"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.err.Error(); got != c.want {
				t.Errorf("Error() = %q, want %q", got, c.want)
			}
			withCause := *c.err
			withCause.Err = errors.New(canary)
			if got := withCause.Error(); got != c.want {
				t.Errorf("a cause changed the text: %q, want %q", got, c.want)
			}
		})
	}
	t.Run("a nil error prints as nil", func(t *testing.T) {
		var e *Error
		if got := e.Error(); got != "<nil>" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != nil || e.Retryable() || e.Is(ErrInternal) {
			t.Errorf("a nil error is not a failure of any kind")
		}
	})
}

func TestNoErrorTextCarriesAPlantedMarker(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ERROR_DETAIL", "")
	chains := map[string]error{
		"a provider's HTTP error":                      vendorError(400),
		"a rate limit with a wait":                     &llm.RateLimitError{APIError: vendorError(429), RetryAfter: 3 * time.Second},
		"a classified request":                         types.ClassifiedCall(types.KindInvalidRequest, vendorError(400), "openai", "gpt-6-luna", "req_9", 0),
		"a classified request, a 5xx":                  types.ClassifiedCall(types.KindProviderUnavailable, vendorError(502), "openai", "gpt-6-luna", "req_9", 0),
		"an operation error whose cause is the marker": &types.OperationError{Kind: types.KindProviderUnavailable, Op: "llm.call", Cause: errors.New(canary)},
		"a plain provider error":                       errors.New("upstream said: " + canary),
		"a wrapped provider error":                     fmt.Errorf("sending: %w", errors.New(canary)),
		"a provider's own *Error":                      &Error{Kind: ErrUnavailable, Err: errors.New(canary)},
		"a joined error":                               errors.Join(errors.New(canary), vendorError(503)),
		"an unrouted configuration error":              types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: Threshold must be from 0 to 1")),
	}
	for name, err := range chains {
		t.Run(name, func(t *testing.T) {
			got := convertError("extract", err)
			var e *Error
			if !errors.As(got, &e) {
				t.Fatalf("got %T", got)
			}
			if strings.Contains(e.Error(), canary) {
				t.Errorf("Error() carries the marker: %q", e.Error())
			}
			if strings.Contains(got.Error(), canary) {
				t.Errorf("the returned error carries the marker: %q", got.Error())
			}
			for _, field := range []string{e.Op, e.Provider, e.Model, e.RequestID, e.Code, e.Message} {
				if strings.Contains(field, canary) {
					t.Errorf("a field carries the marker: %q", field)
				}
			}
			// The same text with the gate open: Error never reads the environment.
			before := e.Error()
			t.Setenv("SCHEMAFLUX_ERROR_DETAIL", "1")
			if again := e.Error(); again != before || strings.Contains(again, canary) {
				t.Errorf("Error() changed with SCHEMAFLUX_ERROR_DETAIL set: %q, was %q", again, before)
			}
		})
	}
}

func TestSlogAndJSONEmitNoMarker(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ERROR_DETAIL", "")
	errs := map[string]error{
		"an HTTP error":        convertError("classify", vendorError(429)),
		"a rate limit":         convertError("classify", &llm.RateLimitError{APIError: vendorError(429), RetryAfter: time.Second}),
		"a provider's *Error":  convertError("classify", &Error{Kind: ErrUnavailable, Provider: "p", Err: errors.New(canary)}),
		"a plain error":        convertError("classify", errors.New(canary)),
		"an error from a step": convertError("classify", fmt.Errorf("wrap: %w", vendorError(500))),
	}
	for name, err := range errs {
		t.Run(name, func(t *testing.T) {
			var text, js bytes.Buffer
			slog.New(slog.NewTextHandler(&text, nil)).Error("call failed", "err", err)
			slog.New(slog.NewJSONHandler(&js, nil)).Error("call failed", slog.Any("err", err))
			slog.New(slog.NewTextHandler(&text, nil)).Error("call failed", slog.Group("g", slog.Any("err", err)))
			for handler, out := range map[string]string{"text": text.String(), "json": js.String()} {
				if strings.Contains(out, canary) {
					t.Errorf("the %s handler wrote the marker: %s", handler, out)
				}
				if !strings.Contains(out, "kind") {
					t.Errorf("the %s handler did not write the kind: %s", handler, out)
				}
			}
			encoded, jsonErr := json.Marshal(err)
			if jsonErr != nil {
				t.Fatalf("Marshal: %v", jsonErr)
			}
			if strings.Contains(string(encoded), canary) {
				t.Errorf("json.Marshal wrote the marker: %s", encoded)
			}
			wrapped, _ := json.Marshal(map[string]error{"err": err})
			if strings.Contains(string(wrapped), canary) {
				t.Errorf("json.Marshal of a map holding the error wrote the marker: %s", wrapped)
			}
		})
	}
	t.Run("the JSON names the kind and the fields", func(t *testing.T) {
		e := &Error{Kind: ErrRateLimited, Op: "classify", Provider: "openai", Model: "m", RequestID: "req_1", Status: 429, Code: "rate_limit", RetryAfter: 1500 * time.Millisecond, Message: "slow down", Err: errors.New(canary)}
		encoded, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"kind": "rate limited", "op": "classify", "provider": "openai", "model": "m", "request_id": "req_1", "status": 429.0, "code": "rate_limit", "retry_after": "1.5s", "message": "slow down"}
		if len(decoded) != len(want) {
			t.Errorf("got %v", decoded)
		}
		for k, v := range want {
			if decoded[k] != v {
				t.Errorf("%s = %v, want %v", k, decoded[k], v)
			}
		}
	})
	t.Run("the zero error and a nil one encode", func(t *testing.T) {
		if out, err := json.Marshal(&Error{}); err != nil || string(out) != `{"kind":"unknown"}` {
			t.Errorf("got %s, %v", out, err)
		}
		var nilError *Error
		if out, err := json.Marshal(nilError); err != nil || string(out) != "null" {
			t.Errorf("got %s, %v", out, err)
		}
		if v := nilError.LogValue(); v.Kind() != slog.KindAny {
			t.Errorf("a nil error logs as %v", v.Kind())
		}
	})
	t.Run("the log value is a group of the fields", func(t *testing.T) {
		e := &Error{Kind: ErrTimeout, Op: "sort", Status: 504, Err: errors.New(canary)}
		value := e.LogValue()
		if value.Kind() != slog.KindGroup {
			t.Fatalf("kind = %v", value.Kind())
		}
		got := map[string]string{}
		for _, attr := range value.Group() {
			got[attr.Key] = attr.Value.String()
		}
		if got["kind"] != "timeout" || got["op"] != "sort" || got["status"] != "504" || len(got) != 3 {
			t.Errorf("got %v", got)
		}
	})
}

func TestRetryableIsTrueForExactlyFourKinds(t *testing.T) {
	t.Parallel()
	retryable := map[Kind]bool{ErrRateLimited: true, ErrUnavailable: true, ErrTimeout: true, ErrBusy: true}
	for _, k := range allKinds {
		t.Run(k.Error(), func(t *testing.T) {
			if got := (&Error{Kind: k}).Retryable(); got != retryable[k] {
				t.Errorf("Retryable() = %v, want %v", got, retryable[k])
			}
		})
	}
	t.Run("the zero error is not retryable", func(t *testing.T) {
		if (&Error{}).Retryable() {
			t.Error("the zero error is retryable")
		}
	})
}

func TestIsMatchesTheKindAndErrUnsupported(t *testing.T) {
	t.Parallel()
	for _, k := range allKinds {
		e := &Error{Kind: k}
		t.Run("a "+k.Error()+" matches its own kind only", func(t *testing.T) {
			for _, other := range allKinds {
				if got := e.Is(other); got != (other == k) {
					t.Errorf("Is(%v) = %v", other, got)
				}
			}
			if got := e.Is(errors.ErrUnsupported); got != (k == ErrUnsupported) {
				t.Errorf("Is(errors.ErrUnsupported) = %v", got)
			}
			if e.Is(context.Canceled) || e.Is(errors.New("x")) {
				t.Errorf("matched an unrelated error")
			}
		})
	}
	t.Run("an unsupported error is errors.ErrUnsupported", func(t *testing.T) {
		if !errors.Is(fmt.Errorf("x: %w", &Error{Kind: ErrUnsupported}), errors.ErrUnsupported) {
			t.Error("errors.Is(err, errors.ErrUnsupported) = false")
		}
	})
	t.Run("a target that cannot be compared does not panic", func(t *testing.T) {
		if (&Error{Kind: ErrUnsupported}).Is(uncomparableError{values: []int{1}}) {
			t.Error("matched")
		}
	})
}

type uncomparableError struct{ values []int }

func (uncomparableError) Error() string { return "uncomparable" }

func TestRetryableAndKindThroughTheConverter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"a 429", vendorError(429), true},
		{"a 503", vendorError(503), true},
		{"a 401", vendorError(401), false},
		{"a 400", vendorError(400), false},
		{"a network timeout", &net.DNSError{IsTimeout: true}, true},
		{"a refused answer", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal}, false},
		{"an open circuit", types.Newf(types.KindCircuitOpen, "mw", "open"), true},
		{"a full queue", types.Newf(types.KindAdmissionRejected, "scheduler", "full"), true},
		{"a spent budget", types.Newf(types.KindBudgetExceeded, "budget", "spent"), false},
		{"a truncated answer", types.Newf(types.KindOutputTruncated, "x", "cut"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e *Error
			if !errors.As(convertError("op", c.err), &e) {
				t.Fatal("not an *Error")
			}
			if e.Retryable() != c.retryable {
				t.Errorf("Retryable() = %v for kind %v", e.Retryable(), e.Kind)
			}
		})
	}
}

func TestDetailReachesWhatTheProviderSaid(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ERROR_DETAIL", "")
	t.Run("the vendor's message and body are in Detail and not in Error", func(t *testing.T) {
		got := convertError("extract", vendorError(400))
		var e *Error
		errors.As(got, &e)
		if strings.Contains(e.Error(), canary) {
			t.Errorf("Error() carries the marker")
		}
		detail := e.Detail()
		if !strings.Contains(detail, canary) {
			t.Errorf("Detail() = %q, want the provider's message in it", detail)
		}
		if !strings.HasPrefix(detail, e.Error()+"\n") {
			t.Errorf("Detail() does not start with Error(): %q", detail)
		}
	})
	t.Run("a rate limit's Detail keeps the wait", func(t *testing.T) {
		rate := &llm.RateLimitError{APIError: vendorError(429), RetryAfter: 7 * time.Second}
		var e *Error
		errors.As(convertError("extract", rate), &e)
		if !strings.Contains(e.Detail(), "7s") || !strings.Contains(e.Detail(), canary) {
			t.Errorf("Detail() = %q", e.Detail())
		}
	})
	t.Run("an error with nothing of the provider's has Detail equal to Error", func(t *testing.T) {
		e := &Error{Kind: ErrConfiguration, Op: "sort", Message: "m", Err: errors.New("plain")}
		if e.Detail() != e.Error() {
			t.Errorf("Detail() = %q", e.Detail())
		}
		if (&Error{Kind: ErrCanceled}).Detail() != "canceled" {
			t.Errorf("Detail() of a bare error is not its text")
		}
	})
	t.Run("a nested error does not repeat its own line", func(t *testing.T) {
		inner := convertError("a", vendorError(400))
		outer := convertError("b", fmt.Errorf("w: %w", inner))
		var e *Error
		errors.As(outer, &e)
		if strings.Count(e.Detail(), "message:") != 1 {
			t.Errorf("Detail() = %q", e.Detail())
		}
	})
	t.Run("an error that wraps itself does not hang Detail", func(t *testing.T) {
		cyclic := &cyclicError{}
		cyclic.next = cyclic
		e := &Error{Kind: ErrInternal, Err: cyclic}
		if got := e.Detail(); got == "" {
			t.Error("empty")
		}
	})
}

type cyclicError struct{ next error }

func (c *cyclicError) Error() string { return "cyclic" }
func (c *cyclicError) Unwrap() error { return c.next }

func TestConvertErrorFillsWhatTheProviderLeftEmpty(t *testing.T) {
	t.Parallel()
	t.Run("nil stays nil", func(t *testing.T) {
		if got := convertError("extract", nil); got != nil {
			t.Errorf("got %v", got)
		}
	})
	t.Run("an error that has everything is returned as it is", func(t *testing.T) {
		e := &Error{Kind: ErrRateLimited, Op: "classify", Provider: "p", Model: "m", RequestID: "r"}
		if got := convertError("classify", e); got != error(e) {
			t.Errorf("got a different error")
		}
	})
	t.Run("an error with no operation is given one", func(t *testing.T) {
		e := &Error{Kind: ErrRateLimited, RetryAfter: time.Second}
		got := convertError("classify", e)
		var out *Error
		if !errors.As(got, &out) || out.Op != "classify" || out.Kind != ErrRateLimited || out.RetryAfter != time.Second {
			t.Fatalf("got %+v", out)
		}
		if e.Op != "" {
			t.Errorf("the provider's own error was written to")
		}
		if !errors.Is(got, e) {
			t.Errorf("the original is not in the chain")
		}
	})
	t.Run("the route comes from the chain beneath", func(t *testing.T) {
		inner := &types.OperationError{Kind: types.KindProviderUnavailable, Provider: "anthropic", Model: "m2", RequestID: "req_5"}
		e := &Error{Kind: ErrUnavailable, Err: inner}
		var out *Error
		errors.As(convertError("sort", e), &out)
		if out.Provider != "anthropic" || out.Model != "m2" || out.RequestID != "req_5" || out.Op != "sort" {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("what the provider set is not overwritten", func(t *testing.T) {
		inner := &types.OperationError{Kind: types.KindProviderUnavailable, Provider: "anthropic", Model: "m2"}
		e := &Error{Kind: ErrUnavailable, Provider: "mine", Model: "m1", Op: "extract", Err: inner}
		var out *Error
		errors.As(convertError("sort", e), &out)
		if out.Provider != "mine" || out.Model != "m1" || out.Op != "extract" {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("a provider's error with no kind is classified from the chain", func(t *testing.T) {
		e := &Error{Message: "no kind given", Err: vendorError(401)}
		var out *Error
		errors.As(convertError("sort", e), &out)
		if out.Kind != ErrUnauthorized || out.Message != "no kind given" || out.Status != 401 {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("a provider's error with no kind and nothing beneath is internal", func(t *testing.T) {
		var out *Error
		errors.As(convertError("sort", &Error{}), &out)
		if out.Kind != ErrInternal {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("a typed nil error does not panic", func(t *testing.T) {
		var nilError *Error
		var out *Error
		errors.As(convertError("sort", nilError), &out)
		if out == nil || out.Kind != ErrInternal {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("an *Error under a wrapper decides the kind and gives its fields", func(t *testing.T) {
		inner := &Error{Kind: ErrRateLimited, Provider: "p", RetryAfter: 2 * time.Second, Message: "slow"}
		var out *Error
		errors.As(convertError("sort", fmt.Errorf("mw: %w", inner)), &out)
		if out.Kind != ErrRateLimited || out.Provider != "p" || out.RetryAfter != 2*time.Second || out.Message != "slow" || out.Op != "sort" {
			t.Errorf("got %+v", out)
		}
	})
	t.Run("the outermost classified error decides", func(t *testing.T) {
		inner := &Error{Kind: ErrUnavailable}
		outer := &types.OperationError{Kind: types.KindTimeout, Cause: inner}
		var out *Error
		errors.As(convertError("sort", outer), &out)
		if out.Kind != ErrTimeout {
			t.Errorf("Kind = %v, want timeout", out.Kind)
		}
		reverse := &Error{Kind: ErrCanceled, Err: &types.OperationError{Kind: types.KindTimeout}}
		if got := convertError("sort", fmt.Errorf("x: %w", reverse)); !errors.Is(got, ErrCanceled) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("a bare kind in the chain decides", func(t *testing.T) {
		var out *Error
		errors.As(convertError("sort", fmt.Errorf("middleware: %w", ErrBusy)), &out)
		if out.Kind != ErrBusy {
			t.Errorf("got %+v", out)
		}
	})
}

func TestConvertErrorReadsTheFactsOfAChain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want Error
	}{
		{
			"an HTTP error gives status, code and route",
			vendorError(400),
			Error{Kind: ErrInvalidRequest, Provider: "openai", Model: "gpt-6-luna", Status: 400, Code: "context_length_exceeded"},
		},
		{
			"a rate limit gives its wait",
			&llm.RateLimitError{APIError: &llm.APIError{Provider: "openai", StatusCode: 429}, RetryAfter: 9 * time.Second},
			Error{Kind: ErrRateLimited, Provider: "openai", Status: 429, RetryAfter: 9 * time.Second},
		},
		{
			"a 503 with a wait is unavailable and keeps the wait",
			&llm.RateLimitError{APIError: &llm.APIError{Provider: "openai", StatusCode: 503}, RetryAfter: time.Second},
			Error{Kind: ErrUnavailable, Provider: "openai", Status: 503, RetryAfter: time.Second},
		},
		{
			"a classified request gives its route",
			types.ClassifiedCall(types.KindProviderUnavailable, errors.New("boom"), "anthropic", "m3", "req_8", 4*time.Second),
			Error{Kind: ErrUnavailable, Provider: "anthropic", Model: "m3", RequestID: "req_8", RetryAfter: 4 * time.Second},
		},
		{
			"an operation error gives its own message",
			&types.OperationError{Kind: types.KindConfiguration, Op: "x", Message: "no provider: set OPENAI_API_KEY"},
			Error{Kind: ErrConfiguration, Message: "no provider: set OPENAI_API_KEY"},
		},
		{
			"a configuration error made before a request keeps its words",
			types.Classified(types.KindConfiguration, errors.New("invalid options: Threshold must be from 0 to 1")),
			Error{Kind: ErrConfiguration, Message: "invalid options: Threshold must be from 0 to 1"},
		},
		{
			"an invalid request made before a request keeps its words",
			types.Classified(types.KindInvalidRequest, errors.New("invalid options: blank question")),
			Error{Kind: ErrInvalidRequest, Message: "invalid options: blank question"},
		},
		{
			"a classified error of another kind keeps none of its text",
			types.Classified(types.KindMalformedOutput, errors.New("provider returned empty completion content")),
			Error{Kind: ErrInvalidAnswer},
		},
		{
			"a classified provider failure keeps none of its text, whatever the kind",
			types.ClassifiedCall(types.KindInvalidRequest, errors.New("vendor words"), "openai", "m", "r", 0),
			Error{Kind: ErrInvalidRequest, Provider: "openai", Model: "m", RequestID: "r"},
		},
		{
			"a code that is free text is dropped",
			&llm.APIError{StatusCode: 400, Code: "the passage was rejected"},
			Error{Kind: ErrInvalidRequest, Status: 400},
		},
		{
			"a review request has its kind and no message",
			types.NewReviewRequired(types.ReviewPacket[string]{FailedChecks: []string{"agreement"}}),
			Error{Kind: ErrReviewRequired},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got *Error
			if !errors.As(convertError("op", c.err), &got) {
				t.Fatal("not an *Error")
			}
			want := c.want
			want.Op = "op"
			want.Err = got.Err
			if *got != want {
				t.Errorf("got  %+v\nwant %+v", *got, want)
			}
			if got.Err == nil {
				t.Errorf("the cause was dropped")
			}
		})
	}
}

func TestConvertErrorReadsWhatTheInternalClassifierKnows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"a 401", &llm.APIError{StatusCode: 401}, ErrUnauthorized},
		{"a 403", &llm.APIError{StatusCode: 403}, ErrUnauthorized},
		{"a 404", &llm.APIError{StatusCode: 404}, ErrInvalidRequest},
		{"a 408", &llm.APIError{StatusCode: 408}, ErrTimeout},
		{"a 413", &llm.APIError{StatusCode: 413}, ErrTooLarge},
		{"a 422", &llm.APIError{StatusCode: 422}, ErrInvalidRequest},
		{"a 429", &llm.APIError{StatusCode: 429}, ErrRateLimited},
		{"a 500", &llm.APIError{StatusCode: 500}, ErrUnavailable},
		{"a 504", &llm.APIError{StatusCode: 504}, ErrTimeout},
		{"a 599", &llm.APIError{StatusCode: 599}, ErrUnavailable},
		{"a network timeout", &net.DNSError{IsTimeout: true}, ErrTimeout},
		{"a network failure", &net.OpError{Op: "dial", Err: errors.New("refused")}, ErrUnavailable},
		{"a spent ceiling in the library's own words", errors.New("pricing: cost budget exceeded: spent 1.2"), ErrBudgetExceeded},
		{"the library's own phrase for no provider", errors.New("no LLM provider configured"), ErrConfiguration},
		{"a plain error", errors.New("boom"), ErrInternal},
		{"an empty message", errors.New(""), ErrInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e *Error
			if !errors.As(convertError("op", c.err), &e) || e.Kind != c.want {
				t.Errorf("got %+v, want kind %v", e, c.want)
			}
		})
	}
}

func TestKindMappingsAreInverse(t *testing.T) {
	t.Parallel()
	for _, k := range allKinds {
		t.Run(k.Error(), func(t *testing.T) {
			internal, narrow := kindToInternal(k)
			back, ok := kindFromInternal(internal, narrow)
			if !ok || back != k {
				t.Errorf("%v -> %v/%v -> %v (%v)", k, internal, narrow, back, ok)
			}
		})
	}
	t.Run("an unknown internal kind has no kind", func(t *testing.T) {
		if _, ok := kindFromInternal(types.KindUnknown, types.NarrowNone); ok {
			t.Error("KindUnknown is known")
		}
		if _, ok := kindFromInternal(types.ErrorKind(200), types.NarrowNone); ok {
			t.Error("a kind past the last is known")
		}
	})
	t.Run("an unknown v2 kind is carried as unavailable", func(t *testing.T) {
		if internal, _ := kindToInternal(0); internal != types.KindProviderUnavailable {
			t.Errorf("got %v", internal)
		}
	})
}

func TestFixedWordKeepsOnlyAProvidersFixedWords(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"context_length_exceeded", "context_length_exceeded"},
		{"rate-limit.exceeded", "rate-limit.exceeded"},
		{"E42", "E42"},
		{"a:b", "a:b"},
		{"two words", ""},
		{"the passage was rejected", ""},
		{"quote\"", ""},
		{"new\nline", ""},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{strings.Repeat("a", 65), ""},
		{"naïve", "naïve"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%q", c.in), func(t *testing.T) {
			if got := fixedWord(c.in); got != c.want {
				t.Errorf("fixedWord(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestWalkChainVisitsOutsideInAndStops(t *testing.T) {
	t.Parallel()
	a, b, c := errors.New("a"), errors.New("b"), errors.New("c")
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"nil", nil, nil},
		{"one error", a, []string{"a"}},
		{"a wrapped error", fmt.Errorf("w: %w", a), []string{"w: a", "a"}},
		{"a joined error in order", errors.Join(a, b), []string{"a\nb", "a", "b"}},
		{"a join under a wrapper", fmt.Errorf("w: %w", errors.Join(a, fmt.Errorf("x: %w", c))), []string{"w: a\nx: c", "a\nx: c", "a", "x: c", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			walkChain(tc.err, func(err error) bool { seen = append(seen, err.Error()); return true })
			if strings.Join(seen, "|") != strings.Join(tc.want, "|") {
				t.Errorf("visited %q, want %q", seen, tc.want)
			}
		})
	}
	t.Run("returning false stops the walk", func(t *testing.T) {
		count := 0
		walkChain(errors.Join(a, b, c), func(error) bool { count++; return count < 2 })
		if count != 2 {
			t.Errorf("visited %d", count)
		}
	})
	t.Run("a cycle ends the walk", func(t *testing.T) {
		cyclic := &cyclicError{}
		cyclic.next = cyclic
		count := 0
		walkChain(cyclic, func(error) bool { count++; return true })
		if count != maxChainNodes {
			t.Errorf("visited %d errors, want %d", count, maxChainNodes)
		}
	})
}

func TestEveryErrorOfTheLibraryIsAnErrorItReturns(t *testing.T) {
	t.Parallel()
	failures := map[string]error{
		"a spent client budget":     ops.ErrClientBudgetExhausted,
		"a closed scheduler":        ops.ErrSchedulerClosed,
		"no provider":               ops.ErrNoProvider,
		"a refused route":           fmt.Errorf("%w: no candidates were offered", ops.ErrRouteRefused),
		"a bad option":              types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", errors.New("Threshold must be from 0 to 1"))),
		"an empty answer":           types.Classified(types.KindMalformedOutput, errors.New("provider returned empty completion content")),
		"a recovered panic":         &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic, Op: "mapreduce", Message: "a chunk panicked"},
		"a truncated answer":        types.Newf(types.KindOutputTruncated, "extract", "the answer was cut at 100 tokens"),
		"a refused answer":          &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal, Op: "llm.call", Message: "the model declined"},
		"a context too large":       &llm.APIError{StatusCode: 413},
		"an unnamed failure":        errors.New("something"),
		"a joined pair of failures": errors.Join(context.Canceled, errors.New("x")),
	}
	for name, err := range failures {
		t.Run(name, func(t *testing.T) {
			got := convertError("filter", err)
			var e *Error
			if !errors.As(got, &e) {
				t.Fatalf("got %T", got)
			}
			if !e.Kind.valid() {
				t.Errorf("the kind %d is not one of the eighteen", e.Kind)
			}
			if e.Op != "filter" {
				t.Errorf("Op = %q", e.Op)
			}
			if !errors.Is(got, e.Kind) {
				t.Errorf("errors.Is(err, e.Kind) = false")
			}
			text := e.Error()
			if !strings.HasPrefix(text, "filter: ") || text != strings.ToLower(text[:1])+text[1:] {
				t.Errorf("the text %q does not begin with the operation, in lower case", text)
			}
			if strings.HasSuffix(text, ".") {
				t.Errorf("the text %q ends with a full stop", text)
			}
		})
	}
}

// The mark that tells a refusal from the other policy violations is on the
// error a content-filtered answer ends in, so a converter that reads a carrier's
// own mark and an observer that reads it from the chain both report a refusal.
func TestConvertErrorReadsTheRefusalMarkOfAClassifiedError(t *testing.T) {
	t.Parallel()
	cause := errors.New("provider returned empty completion content")
	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"a refusal", types.ClassifiedRefusal(cause), ErrRefused},
		{"a refusal under a wrapper", fmt.Errorf("extract: %w", types.ClassifiedRefusal(cause)), ErrRefused},
		{"a refusal in a joined error", errors.Join(errors.New("extract failed"), types.ClassifiedRefusal(cause)), ErrRefused},
		{"a policy violation that is not a refusal", types.Classified(types.KindPolicyViolation, cause), ErrPolicy},
		{"a policy violation of the data policy", types.Newf(types.KindPolicyViolation, "llm.call", "route"), ErrPolicy},
		{"another kind over a refusal is that kind", types.Classified(types.KindMalformedOutput, types.ClassifiedRefusal(cause)), ErrInvalidAnswer},
		{"a refusal over another kind is a refusal", types.ClassifiedRefusal(types.Classified(types.KindMalformedOutput, cause)), ErrRefused},
		{"a malformed output", types.Classified(types.KindMalformedOutput, cause), ErrInvalidAnswer},
		{"a refusal marked on an operation error", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal, Op: "choose"}, ErrRefused},
		{"a refusal that carries a route", types.ClassifiedCall(types.KindPolicyViolation, types.ClassifiedRefusal(cause), "p", "m", "r", 0), ErrRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *Error
			if !errors.As(convertError("extract", tc.err), &got) || got.Kind != tc.want {
				t.Errorf("kind = %v, want %v", got, tc.want)
			}
		})
	}
}

// An operation whose provider answers with a content-filter finish and nothing
// else fails as a refusal in both ways the call is read: the finish event an
// observer is given names it "refused", and the error the converter makes is the
// refused kind. A data policy that forbids the route is the other policy
// violation and stays one.
func TestAContentFilteredAnswerIsARefusalToAnObserverAndToTheConverter(t *testing.T) {
	type callMark struct{}
	filtered := &scriptedProvider{name: "filtered", complete: func(_ context.Context, req Request) (Response, error) {
		return Response{Model: req.Model, FinishReason: "content_filter"}, nil
	}}
	cases := []struct {
		name     string
		provider Provider
		options  types.OpOptions
		kind     Kind
		reported string
	}{
		{"a content-filtered empty answer", filtered, types.OpOptions{Model: "test-model"}, ErrRefused, "refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := &refusalObserver{mark: callMark{}}
			t.Cleanup(telemetry.AddObserver(observer))
			ctx := WithClient(context.WithValue(t.Context(), callMark{}, true), Client{Provider: tc.provider, Retries: -1})

			_, err := ops.Recorded(ctx, "is", types.ContractPromptOnly, func(ctx context.Context) (bool, error) {
				return ops.Is(ctx, "a subject", "Is this a question?", tc.options)
			})
			if err == nil {
				t.Fatal("the call succeeded")
			}
			var converted *Error
			if !errors.As(convertError("is", err), &converted) || converted.Kind != tc.kind {
				t.Errorf("the converter made %v, want the kind %v", converted, tc.kind)
			}
			if mark := types.NarrowingOf(err); mark != types.NarrowRefusal {
				t.Errorf("the error carries mark %d, want a refusal", mark)
			}
			finished := observer.results()
			if len(finished) == 0 {
				t.Fatal("the observer was told of no finish")
			}
			for _, result := range finished {
				if result.ErrorKind != tc.reported {
					t.Errorf("the %v finish of %q names the failure %q, want %q", result.Scope, result.Operation, result.ErrorKind, tc.reported)
				}
			}
			var sawRequest, sawOperation bool
			for _, result := range finished {
				sawRequest = sawRequest || result.Scope == telemetry.ScopeRequest
				sawOperation = sawOperation || result.Scope == telemetry.ScopeOperation
			}
			if !sawRequest || !sawOperation {
				t.Errorf("the observer saw a request finish %v and an operation finish %v, want both", sawRequest, sawOperation)
			}
		})
	}
}

// refusalObserver keeps the finish events of the calls made under a context that
// carries its mark, so a test that calls an operation is not confused by what
// another test's calls tell the process's observers.
type refusalObserver struct {
	mark any
	mu   sync.Mutex
	done []telemetry.OperationResult
}

func (o *refusalObserver) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	return ctx
}

func (o *refusalObserver) OperationFinished(ctx context.Context, result telemetry.OperationResult) {
	if ctx.Value(o.mark) == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.done = append(o.done, result)
}

func (o *refusalObserver) results() []telemetry.OperationResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]telemetry.OperationResult(nil), o.done...)
}

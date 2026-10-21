package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// decisionRequest is the request the tests of a decision provider send.
func decisionRequest() schemaflux.Request {
	return schemaflux.Request{Model: "m", Decision: &schemaflux.Decision{
		State:     `"Please refund my order."`,
		Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "Is it a refund?"}},
	}}
}

// providerAt builds the named provider against a server, with the testKey.
func providerAt(t testing.TB, name, url string, cfg Config) schemaflux.Provider {
	t.Helper()
	cfg.APIKey = testKey
	cfg.BaseURL = url
	return mustNew(t, name, cfg)
}

func requestFor(name string) schemaflux.Request {
	if strings.HasPrefix(name, "jev") {
		return decisionRequest()
	}
	return schemaflux.Request{Model: "m", System: "s", User: "hi"}
}

// theTextOf returns every way a caller or a log can write err.
func theTextOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var text, jsonText bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Error("failed", "err", err)
	slog.New(slog.NewJSONHandler(&jsonText, nil)).Error("failed", "err", err)
	encoded, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatalf("json.Marshal: %v", jsonErr)
	}
	return map[string]string{
		"Error()":      err.Error(),
		"%v":           fmt.Sprintf("%v", err),
		"%+v":          fmt.Sprintf("%+v", err),
		"%s":           fmt.Sprintf("%s", err),
		"%q":           fmt.Sprintf("%q", err),
		"%#v":          fmt.Sprintf("%#v", err),
		"slog text":    text.String(),
		"slog JSON":    jsonText.String(),
		"json.Marshal": string(encoded),
	}
}

func TestAStatusIsTheKindItMeans(t *testing.T) {
	t.Parallel()
	statuses := []struct {
		status int
		kind   schemaflux.Kind
		retry  bool
	}{
		{http.StatusBadRequest, schemaflux.ErrInvalidRequest, false},
		{http.StatusUnauthorized, schemaflux.ErrUnauthorized, false},
		{http.StatusForbidden, schemaflux.ErrUnauthorized, false},
		{http.StatusNotFound, schemaflux.ErrInvalidRequest, false},
		{http.StatusRequestTimeout, schemaflux.ErrTimeout, true},
		{http.StatusConflict, schemaflux.ErrUnavailable, true},
		{http.StatusRequestEntityTooLarge, schemaflux.ErrTooLarge, false},
		{http.StatusUnprocessableEntity, schemaflux.ErrInvalidRequest, false},
		{http.StatusTooManyRequests, schemaflux.ErrRateLimited, true},
		{http.StatusInternalServerError, schemaflux.ErrUnavailable, true},
		{http.StatusBadGateway, schemaflux.ErrUnavailable, true},
		{http.StatusServiceUnavailable, schemaflux.ErrUnavailable, true},
		{http.StatusGatewayTimeout, schemaflux.ErrTimeout, true},
		{529, schemaflux.ErrUnavailable, true},
	}
	for _, name := range []string{"openai", "anthropic", "openrouter", "deepseek", "jev", "jev-openrouter"} {
		for _, tt := range statuses {
			t.Run(fmt.Sprintf("%s answers %d", name, tt.status), func(t *testing.T) {
				t.Parallel()
				srv := serve(t, replying(tt.status, failingWith("a_fixed_code")))
				p := providerAt(t, name, srv.URL, Config{})
				resp, err := p.Complete(t.Context(), requestFor(name))
				if err == nil {
					t.Fatalf("Complete succeeded with %+v", resp)
				}
				if resp.Content != "" || resp.Answers != nil {
					t.Errorf("Complete returned %+v beside its error, want the zero Response", resp)
				}
				e := errorOf(t, err)
				if e.Kind != tt.kind {
					t.Errorf("Kind = %v, want %v", e.Kind, tt.kind)
				}
				if !errors.Is(err, tt.kind) {
					t.Errorf("errors.Is(err, %v) is false", tt.kind)
				}
				if e.Status != tt.status {
					t.Errorf("Status = %d, want %d", e.Status, tt.status)
				}
				if e.Retryable() != tt.retry {
					t.Errorf("Retryable() = %v, want %v", e.Retryable(), tt.retry)
				}
				if e.Code != "a_fixed_code" {
					t.Errorf("Code = %q, want the provider's fixed word", e.Code)
				}
				if e.Provider != name {
					t.Errorf("Provider = %q, want %q", e.Provider, name)
				}
				for how, text := range theTextOf(t, err) {
					if strings.Contains(text, testMarker) || strings.Contains(text, testKey) {
						t.Errorf("%s prints the provider's text or the key: %s", how, text)
					}
				}
				eachLink(err, func(link error) bool {
					if text := link.Error(); strings.Contains(text, testMarker) || strings.Contains(text, testKey) {
						t.Errorf("a link of the chain, %T, prints the provider's text or the key: %s", link, text)
					}
					return true
				})
				detail := e.Detail()
				if !strings.Contains(detail, testMarker) {
					t.Errorf("Detail() = %q, want the provider's words", detail)
				}
				if strings.Contains(detail, testKey) {
					t.Errorf("Detail() contains the key: %q", detail)
				}
			})
		}
	}
}

func TestARateLimitCarriesTheWaitTheProviderAsked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		header string
		want   time.Duration
		kind   schemaflux.Kind
	}{
		{"seconds", 429, "7", 7 * time.Second, schemaflux.ErrRateLimited},
		{"one second", 429, "1", time.Second, schemaflux.ErrRateLimited},
		{"fractional seconds", 429, "1.5", 1500 * time.Millisecond, schemaflux.ErrRateLimited},
		{"a minute", 429, "60", time.Minute, schemaflux.ErrRateLimited},
		{"a wait past the cap is capped", 429, "86400", llm.MaxRetryAfter, schemaflux.ErrRateLimited},
		{"none stated", 429, "", 0, schemaflux.ErrRateLimited},
		{"a wait that is not a number", 429, "soon", 0, schemaflux.ErrRateLimited},
		{"a 503 carries it too", 503, "9", 9 * time.Second, schemaflux.ErrUnavailable},
		{"a 503 with none", 503, "", 0, schemaflux.ErrUnavailable},
	}
	for _, name := range []string{"openai", "anthropic", "openrouter", "jev"} {
		for _, tt := range tests {
			t.Run(name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				var headers []string
				if tt.header != "" {
					headers = []string{"Retry-After", tt.header}
				}
				srv := serve(t, replying(tt.status, failingWith("rate"), headers...))
				p := providerAt(t, name, srv.URL, Config{})
				_, err := p.Complete(t.Context(), requestFor(name))
				e := errorOf(t, err)
				if e.Kind != tt.kind {
					t.Errorf("Kind = %v, want %v", e.Kind, tt.kind)
				}
				if e.RetryAfter != tt.want {
					t.Errorf("RetryAfter = %v, want %v", e.RetryAfter, tt.want)
				}
			})
		}
	}
}

func TestAContextErrorIsReachableWithErrorsIs(t *testing.T) {
	t.Parallel()
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}
	for _, name := range []string{"openai", "anthropic", "openrouter", "jev"} {
		t.Run(name+" with a context that was canceled", func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, responsesOK))
			p := providerAt(t, name, srv.URL, Config{})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := p.Complete(ctx, requestFor(name))
			if e := errorOf(t, err); e.Kind != schemaflux.ErrCanceled {
				t.Errorf("Kind = %v, want canceled", e.Kind)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("errors.Is(err, context.Canceled) is false for %v", err)
			}
			if srv.count() != 0 {
				t.Errorf("a canceled context sent %d requests", srv.count())
			}
		})
		t.Run(name+" with a context whose deadline passed", func(t *testing.T) {
			t.Parallel()
			srv := serve(t, slow)
			p := providerAt(t, name, srv.URL, Config{})
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			_, err := p.Complete(ctx, requestFor(name))
			if e := errorOf(t, err); e.Kind != schemaflux.ErrTimeout {
				t.Errorf("Kind = %v, want timeout", e.Kind)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("errors.Is(err, context.DeadlineExceeded) is false for %v", err)
			}
		})
		t.Run(name+" with a Config.Timeout that passed", func(t *testing.T) {
			t.Parallel()
			srv := serve(t, slow)
			p := providerAt(t, name, srv.URL, Config{Timeout: 100 * time.Millisecond})
			_, err := p.Complete(t.Context(), requestFor(name))
			if e := errorOf(t, err); e.Kind != schemaflux.ErrTimeout {
				t.Errorf("Kind = %v, want timeout", e.Kind)
			}
		})
		t.Run(name+" canceled while the request was out", func(t *testing.T) {
			t.Parallel()
			srv := serve(t, slow)
			p := providerAt(t, name, srv.URL, Config{})
			ctx, cancel := context.WithCancel(t.Context())
			time.AfterFunc(100*time.Millisecond, cancel)
			_, err := p.Complete(ctx, requestFor(name))
			if e := errorOf(t, err); e.Kind != schemaflux.ErrCanceled {
				t.Errorf("Kind = %v, want canceled", e.Kind)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("errors.Is(err, context.Canceled) is false for %v", err)
			}
		})
		t.Run(name+" with nothing listening", func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, ""))
			url := srv.URL
			srv.Close()
			p := providerAt(t, name, url, Config{})
			_, err := p.Complete(t.Context(), requestFor(name))
			e := errorOf(t, err)
			if e.Kind != schemaflux.ErrUnavailable {
				t.Errorf("Kind = %v, want unavailable", e.Kind)
			}
			if strings.Contains(err.Error(), "127.0.0.1") {
				t.Errorf("the error prints the address: %v", err)
			}
		})
	}
}

func TestAnAnswerThatCannotBeUsedIsAnErrorOfItsKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reply string
		kind  schemaflux.Kind
	}{
		{"no output at all", `{"id":"r","status":"completed","output":[]}`, schemaflux.ErrInvalidAnswer},
		{"output of an empty text", `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":""}]}]}`, schemaflux.ErrInvalidAnswer},
		{"a body that is not JSON", `<html>bad gateway</html>`, schemaflux.ErrUnavailable},
		{"cut off at the ceiling with no text", `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, schemaflux.ErrTruncated},
		// internal/llm files a withheld answer as a policy violation carrying the
		// refusal mark, which is what makes it the refused kind and not the kind
		// of a data policy that forbids a route.
		{"withheld by the content filter", `{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`, schemaflux.ErrRefused},
		{"declined by the model", `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}`, schemaflux.ErrRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, tt.reply))
			p := providerAt(t, "openai", srv.URL, Config{})
			_, err := p.Complete(t.Context(), schemaflux.Request{Model: "m", User: "hi"})
			if err == nil {
				t.Fatal("Complete succeeded")
			}
			e := errorOf(t, err)
			if e.Kind != tt.kind {
				t.Errorf("Kind = %v (%v), want %v", e.Kind, err, tt.kind)
			}
			for how, text := range theTextOf(t, err) {
				if strings.Contains(text, "html") || strings.Contains(text, "bad gateway") {
					t.Errorf("%s prints the body: %s", how, text)
				}
			}
		})
	}
}

// eachLink calls visit with err and each error beneath it, outermost first, until
// visit returns false.
func eachLink(err error, visit func(error) bool) {
	for depth := 0; err != nil && depth < 64; depth++ {
		if !visit(err) {
			return
		}
		switch wrapper := err.(type) {
		case interface{ Unwrap() error }:
			err = wrapper.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range wrapper.Unwrap() {
				eachLink(inner, visit)
			}
			return
		default:
			return
		}
	}
}

func TestFailureReadsTheKindOfAnyChain(t *testing.T) {
	t.Parallel()
	rate := &llm.RateLimitError{APIError: &llm.APIError{Provider: "p", Model: "m", StatusCode: 429, Code: "slow_down"}, RetryAfter: 3 * time.Second}
	tests := []struct {
		name string
		err  error
		kind schemaflux.Kind
	}{
		{"a bare root kind", schemaflux.ErrRateLimited, schemaflux.ErrRateLimited},
		{"a root error", &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded}, schemaflux.ErrBudgetExceeded},
		{"a root kind inside a wrap", fmt.Errorf("wrapped: %w", schemaflux.ErrUnauthorized), schemaflux.ErrUnauthorized},
		{"the outermost kind decides", &schemaflux.Error{Kind: schemaflux.ErrBusy, Err: schemaflux.ErrTimeout}, schemaflux.ErrBusy},
		{"an internal configuration error", types.Newf(types.KindConfiguration, "x", "m"), schemaflux.ErrConfiguration},
		{"an internal refusal mark", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal}, schemaflux.ErrRefused},
		{"an internal panic mark", &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic}, schemaflux.ErrInternal},
		{"a policy violation with no mark is a policy", &types.OperationError{Kind: types.KindPolicyViolation}, schemaflux.ErrPolicy},
		{"a refusal made by ClassifiedRefusal", types.ClassifiedRefusal(errors.New("declined")), schemaflux.ErrRefused},
		{"a rate limit error", rate, schemaflux.ErrRateLimited},
		{"an API error of 500", &llm.APIError{StatusCode: 500}, schemaflux.ErrUnavailable},
		{"an API error of 401", &llm.APIError{StatusCode: 401}, schemaflux.ErrUnauthorized},
		{"a canceled context", context.Canceled, schemaflux.ErrCanceled},
		{"an expired context", fmt.Errorf("call: %w", context.DeadlineExceeded), schemaflux.ErrTimeout},
		{"an error nothing classified", errors.New("something odd"), schemaflux.ErrUnavailable},
		{"a joined chain takes the first classified", errors.Join(errors.New("a"), schemaflux.ErrTooLarge), schemaflux.ErrTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := failure(tt.err)
			e := errorOf(t, err)
			if e.Kind != tt.kind {
				t.Errorf("Kind = %v, want %v", e.Kind, tt.kind)
			}
			if !errors.Is(err, tt.kind) {
				t.Errorf("errors.Is(err, %v) is false", tt.kind)
			}
			if !errors.Is(err, tt.err) && e != tt.err {
				t.Errorf("the error that was given is not reachable from %v", err)
			}
		})
	}
	t.Run("nothing is nothing", func(t *testing.T) {
		t.Parallel()
		if err := failure(nil); err != nil {
			t.Errorf("failure(nil) = %v", err)
		}
	})
	t.Run("a typed API error gives the route, the status, the code and the wait", func(t *testing.T) {
		t.Parallel()
		e := errorOf(t, failure(rate))
		if e.Provider != "p" || e.Model != "m" || e.Status != 429 || e.Code != "slow_down" || e.RetryAfter != 3*time.Second {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("a code that is free text is dropped", func(t *testing.T) {
		t.Parallel()
		e := errorOf(t, failure(&llm.APIError{StatusCode: 400, Code: "the model said: " + testMarker}))
		if e.Code != "" {
			t.Errorf("Code = %q, want none", e.Code)
		}
	})
	t.Run("an internal message made before sending is kept", func(t *testing.T) {
		t.Parallel()
		e := errorOf(t, failure(types.Newf(types.KindUnsupportedCapability, "llm.complete", "this model takes no effort")))
		if e.Message != "this model takes no effort" || e.Kind != schemaflux.ErrUnsupported {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("a root error is returned as it is", func(t *testing.T) {
		t.Parallel()
		given := &schemaflux.Error{Kind: schemaflux.ErrPolicy, Op: "x"}
		if got := failure(given); got != error(given) {
			t.Errorf("failure returned %v, want the error it was given", got)
		}
	})
}

func TestBuildFailureIsAConfigurationErrorUnlessTheErrorSaysOtherwise(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		kind schemaflux.Kind
		msg  string
	}{
		{"a plain error", errors.New("openai base URL is required"), schemaflux.ErrConfiguration, ""},
		{"a classified configuration error keeps its words", types.Newf(types.KindConfiguration, "llm.configure", "set it"), schemaflux.ErrConfiguration, "set it"},
		{"an unsupported capability keeps its kind", types.Newf(types.KindUnsupportedCapability, "x", "no"), schemaflux.ErrUnsupported, "no"},
		{"an endpoint refusal", types.ErrEndpointNotAllowed, schemaflux.ErrConfiguration, ""},
		{"a root kind in the chain", fmt.Errorf("x: %w", schemaflux.ErrInvalidRequest), schemaflux.ErrInvalidRequest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := errorOf(t, buildFailure(opNew, "openai", tt.err))
			if e.Kind != tt.kind || e.Message != tt.msg || e.Op != opNew || e.Provider != "openai" {
				t.Errorf("got %+v, want kind %v and message %q", e, tt.kind, tt.msg)
			}
			if !errors.Is(e, tt.err) {
				t.Error("the cause is not reachable")
			}
		})
	}
}

func TestKindsSurviveTheTripThroughTheInternalKinds(t *testing.T) {
	t.Parallel()
	for kind := schemaflux.ErrConfiguration; kind <= schemaflux.ErrInternal; kind++ {
		t.Run(kind.Error(), func(t *testing.T) {
			t.Parallel()
			internal, narrow := bind.InternalKind(uint8(kind))
			back := schemaflux.Kind(bind.KindOf(internal, narrow))
			if back != kind {
				t.Errorf("%v went to %v (%v) and came back as %v", kind, internal, narrow, back)
			}
		})
	}
}

func TestInternalFailureCarriesTheKindTheRouteAndTheWait(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		kind       types.ErrorKind
		narrow     types.Narrowing
		retryAfter time.Duration
	}{
		{"a rate limit with a wait", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, RetryAfter: 5 * time.Second}, types.KindRateLimited, 0, 5 * time.Second},
		{"a refusal is marked", &schemaflux.Error{Kind: schemaflux.ErrRefused}, types.KindPolicyViolation, types.NarrowRefusal, 0},
		{"a panic is marked", &schemaflux.Error{Kind: schemaflux.ErrInternal}, types.KindInvariantViolation, types.NarrowPanic, 0},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401}, types.KindAuthentication, 0, 0},
		{"a plain error is unavailable", errors.New("down"), types.KindProviderUnavailable, 0, 0},
		{"a context error", context.DeadlineExceeded, types.KindTimeout, 0, 0},
		{"a bare kind", schemaflux.ErrTooLarge, types.KindContextTooLarge, 0, 0},
		{"a kind under a wrap", fmt.Errorf("x: %w", schemaflux.ErrBusy), types.KindAdmissionRejected, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := bind.ToProvider(tt.err, "acme", "model-1")
			var op *types.OperationError
			if !errors.As(got, &op) {
				t.Fatalf("got %T, want an *types.OperationError", got)
			}
			if op.Kind != tt.kind || op.Narrow != tt.narrow || op.RetryAfter != tt.retryAfter {
				t.Errorf("got kind %v narrow %v wait %v, want %v %v %v", op.Kind, op.Narrow, op.RetryAfter, tt.kind, tt.narrow, tt.retryAfter)
			}
			if op.Provider != "acme" || op.Model != "model-1" {
				t.Errorf("route = %s/%s, want acme/model-1", op.Provider, op.Model)
			}
			if tt.retryAfter > 0 {
				if wait, ok := llm.RetryAfterFrom(got); !ok || wait != tt.retryAfter {
					t.Errorf("the retry loop reads the wait as %v (%v), want %v", wait, ok, tt.retryAfter)
				}
			}
			if !errors.Is(got, tt.err) {
				t.Error("the provider's error is not reachable")
			}
		})
	}
	t.Run("nothing is nothing", func(t *testing.T) {
		t.Parallel()
		if got := bind.ToProvider(nil, "p", "m"); got != nil {
			t.Errorf("got %v", got)
		}
	})
}

func TestAPolicyViolationIsAPolicyAndOnlyAMarkedOneIsARefusal(t *testing.T) {
	t.Parallel()
	internal, narrow := bind.InternalKind(uint8(schemaflux.ErrPolicy))
	if got := schemaflux.Kind(bind.KindOf(internal, narrow)); got != schemaflux.ErrPolicy {
		t.Errorf("a policy violation is read as %v, want policy", got)
	}
	if got := schemaflux.Kind(bind.KindOf(types.KindPolicyViolation, types.NarrowRefusal)); got != schemaflux.ErrRefused {
		t.Errorf("a marked policy violation is read as %v, want refused", got)
	}
}

// TestAnInternalErrorIsTheSameKindHereAsAtTheRoot reads every internal kind with
// every mark through the entry points of this package, and holds each to what
// the root reads the same error as: one internal event does not get a different
// kind from the package that built the provider and from the client that was
// handed it.
func TestAnInternalErrorIsTheSameKindHereAsAtTheRoot(t *testing.T) {
	t.Parallel()
	for kind := types.KindConfiguration; kind <= types.KindReviewRequired; kind++ {
		for _, mark := range []types.Narrowing{types.NarrowNone, types.NarrowRefusal, types.NarrowPanic} {
			t.Run(fmt.Sprintf("%v/%d", kind, mark), func(t *testing.T) {
				t.Parallel()
				err := &types.OperationError{Kind: kind, Narrow: mark, Op: "x", Provider: "p", Model: "m"}
				want := errorOf(t, bind.ConvertError("op", err)).Kind
				for how, got := range map[string]error{
					"failure":      failure(err),
					"buildFailure": buildFailure(opNew, "p", err),
				} {
					if e := errorOf(t, got); e.Kind != want {
						t.Errorf("%s reads kind %v mark %d as %v, the root reads it as %v", how, kind, mark, e.Kind, want)
					}
				}
				if kind == types.KindPolicyViolation && mark == types.NarrowNone && want != schemaflux.ErrPolicy {
					t.Errorf("a policy violation with no mark is %v, want policy", want)
				}
				if kind == types.KindPolicyViolation && mark == types.NarrowRefusal && want != schemaflux.ErrRefused {
					t.Errorf("a marked policy violation is %v, want refused", want)
				}
			})
		}
	}
}

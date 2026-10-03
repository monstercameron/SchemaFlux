package mw

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const (
	// testEmail and testPhone are what a redactor has to keep from a provider.
	// testPhone is not a built-in shape: a test that redacts it supplies the
	// rule, as a program does.
	testEmail = "jane.doe@example.com"
	testPhone = "+1 415 555 0134"
)

// testKey is a string shaped like an OpenAI key. It is built, so that no
// credential-shaped literal is in the source.
var testKey = "sk-" + "proj-" + strings.Repeat("a1B2", 9)

// errPlain is a failure that no layer classified.
var errPlain = errors.New("something broke")

// ask is a request with a user message and nothing else.
func ask(user string) schemaflux.Request { return schemaflux.Request{User: user} }

// kindOf is the Kind of the *schemaflux.Error in err's chain, and false when
// there is none.
func kindOf(err error) (schemaflux.Kind, bool) {
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		return 0, false
	}
	return e.Kind, true
}

// wantKind fails the test unless err is an *schemaflux.Error of kind want.
func wantKind(t *testing.T, err error, want schemaflux.Kind) *schemaflux.Error {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want an *Error of kind %v", err, err, want)
	}
	if e.Kind != want {
		t.Fatalf("err = %v, kind %v, want %v", err, e.Kind, want)
	}
	if !errors.Is(err, want) {
		t.Fatalf("errors.Is(%v, %v) is false", err, want)
	}
	return e
}

// unavailable is a failure a retry repeats.
func unavailable() error {
	return &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down"}
}

// rateLimited is a failure with a stated wait.
func rateLimited(wait time.Duration) error {
	return &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429, RetryAfter: wait}
}

// answering is a provider that answers every request with body and counts them.
func answering(body string) (*schemafluxtest.Provider, *atomic.Int32) {
	var n atomic.Int32
	p := new(schemafluxtest.Provider)
	p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
		n.Add(1)
		return schemaflux.Response{Content: body, Model: "test-model", Usage: schemaflux.Usage{Input: 10, Output: 5, Total: 15}}, nil
	})
	return p, &n
}

// priced is a cost function that prices every answer at one unit a call.
func priced(unit float64) func(schemaflux.Usage, string, string) schemaflux.Cost {
	return func(schemaflux.Usage, string, string) schemaflux.Cost {
		return schemaflux.Cost{Total: unit, Priced: true, Currency: "USD"}
	}
}

// recorder is a middleware that records the user message of every request that
// reaches it, so that a test can see what a layer outside it let through.
func recorder(seen *[]string) Middleware {
	return func(next schemaflux.Provider) schemaflux.Provider {
		return Func(next.Name(), func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
			*seen = append(*seen, req.User)
			return next.Complete(ctx, req)
		})
	}
}

// withPolicy is ctx with a data policy declared, as a client declares one.
func withPolicy(ctx context.Context, policy types.DataPolicy) context.Context {
	return ops.WithExecConfig(ctx, &ops.ExecConfig{DataPolicy: policy, DataPolicyDeclared: true})
}

// mustNotContain fails the test if text holds any of the secrets.
func mustNotContain(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Errorf("%s holds %q: %s", what, secret, text)
		}
	}
}

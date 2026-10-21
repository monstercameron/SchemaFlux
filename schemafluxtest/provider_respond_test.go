package schemafluxtest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type contextKey struct{}

func TestRespondContextIsGivenTheContextOfTheRequest(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	var seen context.Context
	p.RespondContext(func(ctx context.Context, call int, req schemaflux.Request) (schemaflux.Response, error) {
		seen = ctx
		return schemaflux.Response{Content: "call " + string(rune('0'+call)) + " " + req.User}, nil
	})
	ctx := context.WithValue(context.Background(), contextKey{}, "carried")
	resp, err := p.Complete(ctx, schemaflux.Request{User: "u"})
	if err != nil || resp.Content != "call 0 u" {
		t.Fatalf("Complete = %q, %v", resp.Content, err)
	}
	if seen == nil || seen.Value(contextKey{}) != "carried" {
		t.Error("the function was not given the context of the request")
	}
	if resp, _ = p.Complete(ctx, schemaflux.Request{User: "v"}); resp.Content != "call 1 v" {
		t.Errorf("the second call = %q, want the position counted", resp.Content)
	}
}

func TestRespondContextNilIsRefusedAndTheLastScriptWins(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	p.Reply("listed")
	p.RespondContext(func(context.Context, int, schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{Content: "by function"}, nil
	})
	if resp, _ := p.Complete(context.Background(), schemaflux.Request{}); resp.Content != "by function" {
		t.Errorf("after RespondContext the answer is %q", resp.Content)
	}
	p.Reply("listed again")
	if resp, _ := p.Complete(context.Background(), schemaflux.Request{}); resp.Content != "listed again" {
		t.Errorf("after Reply the answer is %q", resp.Content)
	}
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(recovered.(string), "RespondContext needs a function") {
			t.Errorf("recovered %v", recovered)
		}
	}()
	p.RespondContext(nil)
}

// TestAPerRequestWaitEndsWithTheContext waits a different time before each
// request's answer and cancels the slow one: the wait ends at once with the
// context's error and the fast one is not held up by it.
func TestAPerRequestWaitEndsWithTheContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var p schemafluxtest.Provider
		delays := map[string]time.Duration{"slow": time.Hour, "fast": time.Second}
		p.RespondContext(func(ctx context.Context, _ int, req schemaflux.Request) (schemaflux.Response, error) {
			if err := schemafluxtest.Wait(ctx, delays[req.User]); err != nil {
				return schemaflux.Response{}, err
			}
			return schemaflux.Response{Content: req.User}, nil
		})
		slowCtx, cancel := context.WithCancel(context.Background())
		slowDone := make(chan error, 1)
		go func() {
			_, err := p.Complete(slowCtx, schemaflux.Request{User: "slow"})
			slowDone <- err
		}()
		start := time.Now()
		resp, err := p.Complete(context.Background(), schemaflux.Request{User: "fast"})
		if err != nil || resp.Content != "fast" || time.Since(start) != time.Second {
			t.Fatalf("the fast request = %q, %v after %v", resp.Content, err, time.Since(start))
		}
		synctest.Wait()
		select {
		case err := <-slowDone:
			t.Fatalf("the slow request ended early: %v", err)
		default:
		}
		cancel()
		synctest.Wait()
		if err := <-slowDone; !errors.Is(err, context.Canceled) {
			t.Errorf("the cancelled request returned %v, want the context's error", err)
		}
		if time.Since(start) != time.Second {
			t.Errorf("the clock moved to %v: the cancelled wait ran to its end", time.Since(start))
		}
	})
}

func TestWaitTakesItsEdges(t *testing.T) {
	t.Parallel()
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		d    time.Duration
		want error
	}{
		{"a zero wait", context.Background(), 0, nil},
		{"a negative wait", context.Background(), -time.Hour, nil},
		{"a zero wait on a context that ended", ended, 0, context.Canceled},
		{"a negative wait on a context that ended", ended, -1, context.Canceled},
		{"a wait on a context that ended", ended, time.Hour, context.Canceled},
		{"a short wait", context.Background(), time.Millisecond, nil},
	} {
		if err := schemafluxtest.Wait(tc.ctx, tc.d); !errors.Is(err, tc.want) && !(err == nil && tc.want == nil) {
			t.Errorf("%s: Wait = %v, want %v", tc.name, err, tc.want)
		}
	}
}

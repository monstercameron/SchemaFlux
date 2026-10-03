package schemafluxtest_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// inBubble runs f on the fake clock of a testing/synctest bubble, in a test
// that runs in parallel with the others.
func inBubble(t *testing.T, name string, f func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, f)
	})
}

// pending is a request in flight in a bubble.
type pending struct {
	done chan struct{}
	resp schemaflux.Response
	err  error
}

// start makes a request on its own goroutine and waits until it is blocked.
func start(ctx context.Context, p *schemafluxtest.Provider, req schemaflux.Request) *pending {
	r := &pending{done: make(chan struct{})}
	go func() {
		r.resp, r.err = p.Complete(ctx, req)
		close(r.done)
	}()
	synctest.Wait()
	return r
}

func (r *pending) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func TestDelayMakesEachRequestWaitOnItsContext(t *testing.T) {
	t.Parallel()

	inBubble(t, "a request waits exactly the delay on the fake clock", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(5 * time.Second)
		begin := time.Now()
		r := start(context.Background(), &p, schemaflux.Request{})
		time.Sleep(5*time.Second - time.Nanosecond)
		synctest.Wait()
		if r.finished() {
			t.Fatal("the request was answered before the delay was over")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !r.finished() || r.err != nil {
			t.Fatalf("finished = %v, err = %v", r.finished(), r.err)
		}
		if got := time.Since(begin); got != 5*time.Second {
			t.Fatalf("waited %v, want 5s", got)
		}
	})
	inBubble(t, "a delay of an hour costs no real time", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Hour)
		r := start(context.Background(), &p, schemaflux.Request{})
		time.Sleep(time.Hour)
		synctest.Wait()
		if !r.finished() {
			t.Fatal("not answered after the delay")
		}
	})
	inBubble(t, "no delay answers at once", func(t *testing.T) {
		var p schemafluxtest.Provider
		begin := time.Now()
		r := start(context.Background(), &p, schemaflux.Request{})
		if !r.finished() || time.Since(begin) != 0 {
			t.Fatalf("finished = %v after %v", r.finished(), time.Since(begin))
		}
	})
	inBubble(t, "a negative delay is none", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(-time.Second)
		if r := start(context.Background(), &p, schemaflux.Request{}); !r.finished() {
			t.Fatal("a negative delay made the request wait")
		}
	})
	inBubble(t, "a later delay of zero removes it", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Minute)
		p.Delay(0)
		if r := start(context.Background(), &p, schemaflux.Request{}); !r.finished() {
			t.Fatal("the delay was not removed")
		}
	})
	inBubble(t, "a cancelled context ends the wait with its error", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		begin := time.Now()
		r := start(ctx, &p, schemaflux.Request{})
		time.Sleep(time.Second)
		cancel()
		synctest.Wait()
		if !r.finished() || !errors.Is(r.err, context.Canceled) {
			t.Fatalf("finished = %v, err = %v", r.finished(), r.err)
		}
		if got := time.Since(begin); got != time.Second {
			t.Fatalf("the wait lasted %v after the cancel, want 1s", got)
		}
	})
	inBubble(t, "a deadline that passes in the wait ends it with the deadline error", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r := start(ctx, &p, schemaflux.Request{})
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if !r.finished() || !errors.Is(r.err, context.DeadlineExceeded) {
			t.Fatalf("finished = %v, err = %v", r.finished(), r.err)
		}
	})
	inBubble(t, "the request is counted while it waits", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Minute)
		r := start(context.Background(), &p, schemaflux.Request{User: "waiting"})
		if p.Calls() != 1 || p.Last().User != "waiting" || r.finished() {
			t.Fatalf("Calls() = %d, Last().User = %q", p.Calls(), p.Last().User)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
	})
	inBubble(t, "a request cancelled in the wait is still counted", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		r := start(ctx, &p, schemaflux.Request{})
		cancel()
		synctest.Wait()
		if !r.finished() || p.Calls() != 1 {
			t.Fatalf("finished = %v, Calls() = %d", r.finished(), p.Calls())
		}
	})
	inBubble(t, "it applies to a scripted failure", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Fail(schemaflux.ErrRateLimited)
		p.Delay(time.Second)
		r := start(context.Background(), &p, schemaflux.Request{})
		if r.finished() {
			t.Fatal("the failure was not delayed")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !errors.Is(r.err, schemaflux.ErrRateLimited) {
			t.Fatalf("err = %v", r.err)
		}
	})
	inBubble(t, "it applies to the answer of a claim", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "claimed")
		p.Delay(time.Second)
		r := start(context.Background(), &p, in("", "x"))
		if r.finished() {
			t.Fatal("the claim was not delayed")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if r.resp.Content != "claimed" {
			t.Fatalf("Content = %q", r.resp.Content)
		}
	})
	inBubble(t, "a Respond function runs after the wait", func(t *testing.T) {
		var p schemafluxtest.Provider
		ran := false
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			ran = true
			return schemaflux.Response{Content: "late"}, nil
		})
		p.Delay(time.Second)
		r := start(context.Background(), &p, schemaflux.Request{})
		if ran {
			t.Fatal("the function ran before the delay was over")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !ran || r.resp.Content != "late" {
			t.Fatalf("ran = %v, Content = %q", ran, r.resp.Content)
		}
	})
	inBubble(t, "requests that overlap wait together and not one after another", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(3 * time.Second)
		begin := time.Now()
		first := start(context.Background(), &p, schemaflux.Request{})
		second := start(context.Background(), &p, schemaflux.Request{})
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if !first.finished() || !second.finished() || time.Since(begin) != 3*time.Second {
			t.Fatalf("finished = %v, %v after %v", first.finished(), second.finished(), time.Since(begin))
		}
	})
	inBubble(t, "a context that was already cancelled does not wait", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		begin := time.Now()
		r := start(ctx, &p, schemaflux.Request{})
		if !r.finished() || time.Since(begin) != 0 || !errors.Is(r.err, context.Canceled) {
			t.Fatalf("finished = %v, err = %v", r.finished(), r.err)
		}
	})
	inBubble(t, "Reset keeps the delay", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Second)
		p.Reset()
		if r := start(context.Background(), &p, schemaflux.Request{}); r.finished() {
			t.Fatal("Reset removed the delay")
		}
		time.Sleep(time.Second)
		synctest.Wait()
	})
}

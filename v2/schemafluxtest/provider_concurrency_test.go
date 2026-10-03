package schemafluxtest_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// goroutines is how many requests the tests below make at once.
const goroutines = 50

// together makes n requests at once, released by one channel so that they
// arrive as close together as the scheduler allows, and returns what each got,
// by index.
func together(p schemaflux.Provider, n int) ([]schemaflux.Response, []error) {
	resps := make([]schemaflux.Response, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			resps[i], errs[i] = p.Complete(context.Background(), schemaflux.Request{User: strconv.Itoa(i)})
		})
	}
	close(start)
	wg.Wait()
	return resps, errs
}

func TestOneProviderUsedFromManyGoroutinesCountsEveryRequestOnce(t *testing.T) {
	t.Parallel()

	t.Run("the count is exact", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		together(&p, goroutines)
		if p.Calls() != goroutines {
			t.Fatalf("Calls() = %d, want %d", p.Calls(), goroutines)
		}
	})
	t.Run("every request is recorded once", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		together(&p, goroutines)
		var users []string
		for _, r := range p.Requests() {
			users = append(users, r.User)
		}
		slices.Sort(users)
		want := make([]string, goroutines)
		for i := range want {
			want[i] = strconv.Itoa(i)
		}
		slices.Sort(want)
		if !slices.Equal(users, want) {
			t.Fatalf("requests recorded = %v", users)
		}
	})
	t.Run("a sequence gives each body to exactly one request", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		bodies := make([]string, goroutines)
		for i := range bodies {
			bodies[i] = "body-" + strconv.Itoa(i)
		}
		p.Reply(bodies...)
		resps, _ := together(&p, goroutines)
		got := make([]string, len(resps))
		for i, r := range resps {
			got[i] = r.Content
		}
		slices.Sort(got)
		want := slices.Clone(bodies)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("bodies handed out = %v", got)
		}
	})
	t.Run("a script of failures fails exactly that many", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.FailThen(10, schemaflux.ErrBusy, "ok")
		_, errs := together(&p, goroutines)
		failed := 0
		for _, err := range errs {
			if errors.Is(err, schemaflux.ErrBusy) {
				failed++
			}
		}
		if failed != 10 {
			t.Fatalf("%d requests failed, want 10", failed)
		}
	})
	t.Run("a claim counts the requests that matched it exactly", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		bodies := make([]string, goroutines)
		for i := range bodies {
			bodies[i] = strconv.Itoa(i)
		}
		p.ReplyWhen("x", bodies...)
		start := make(chan struct{})
		results := make([]string, goroutines)
		var wg sync.WaitGroup
		for i := range goroutines {
			wg.Go(func() {
				<-start
				resp, _ := p.Complete(context.Background(), schemaflux.Request{SchemaName: "x"})
				results[i] = resp.Content
			})
		}
		close(start)
		wg.Wait()
		slices.Sort(results)
		want := slices.Clone(bodies)
		slices.Sort(want)
		if !slices.Equal(results, want) {
			t.Fatalf("bodies handed out = %v", results)
		}
	})
	t.Run("a Respond function is given each position once", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		var mu sync.Mutex
		var positions []int
		p.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
			mu.Lock()
			defer mu.Unlock()
			positions = append(positions, call)
			return schemaflux.Response{}, nil
		})
		together(&p, goroutines)
		slices.Sort(positions)
		for i, got := range positions {
			if got != i {
				t.Fatalf("positions = %v", positions)
			}
		}
		if len(positions) != goroutines {
			t.Fatalf("the function ran %d times", len(positions))
		}
	})
	t.Run("reading and scripting while requests are in flight does not disturb the count", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range goroutines {
			wg.Go(func() {
				<-start
				_, _ = p.Complete(context.Background(), schemaflux.Request{User: strconv.Itoa(i)})
				_ = p.Requests()
				_ = p.Last()
				_ = p.Calls()
				p.SetUsage(schemaflux.Usage{Input: i})
				p.SetName("n" + strconv.Itoa(i))
				_ = p.Name()
			})
		}
		close(start)
		wg.Wait()
		if p.Calls() != goroutines {
			t.Fatalf("Calls() = %d", p.Calls())
		}
	})
}

func TestTwoProvidersInParallelTestsNeverSeeEachOthersRequests(t *testing.T) {
	t.Parallel()
	const tests = 8
	for i := range tests {
		t.Run("test "+strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			var p schemafluxtest.Provider
			mine := "mine-" + strconv.Itoa(i)
			p.Reply(mine)
			resps, _ := together(&p, 20)
			for _, r := range resps {
				if r.Content != mine {
					t.Fatalf("answer %q came from another test's script", r.Content)
				}
			}
			for _, r := range p.Requests() {
				if _, err := strconv.Atoi(r.User); err != nil {
					t.Fatalf("a request this test did not make: %q", r.User)
				}
			}
			if p.Calls() != 20 {
				t.Fatalf("Calls() = %d, want 20", p.Calls())
			}
			p.SetName(mine)
			if p.Name() != mine {
				t.Fatalf("Name() = %q", p.Name())
			}
		})
	}
}

func TestADeciderUsedFromManyGoroutinesAnswersEachQuestionOnce(t *testing.T) {
	t.Parallel()
	answers := make([]schemaflux.DecisionAnswer, goroutines)
	for i := range answers {
		answers[i] = schemafluxtest.Level(i)
	}
	d := schemafluxtest.NewDecider(answers...)
	decision := &schemaflux.Decision{Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionScale, Ask: "how much?", Low: 0, High: goroutines}}}
	start := make(chan struct{})
	levels := make([]int, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Go(func() {
			<-start
			resp, err := d.Complete(context.Background(), schemaflux.Request{Decision: decision})
			if err != nil || len(resp.Answers) != 1 {
				levels[i] = -1
				return
			}
			levels[i] = resp.Answers[0].Level
		})
	}
	close(start)
	wg.Wait()
	slices.Sort(levels)
	for i, level := range levels {
		if level != i {
			t.Fatalf("levels handed out = %v", levels)
		}
	}
	if d.Calls() != goroutines {
		t.Fatalf("Calls() = %d", d.Calls())
	}
}

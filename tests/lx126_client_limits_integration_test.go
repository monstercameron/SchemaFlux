package tests

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// lx126Provider answers a classification, and counts how many of its calls are
// in flight at once.
type lx126Provider struct {
	hold     time.Duration
	inFlight atomic.Int64
	peak     atomic.Int64
	calls    atomic.Int64
}

func (p *lx126Provider) enter() func() {
	now := p.inFlight.Add(1)
	for {
		peak := p.peak.Load()
		if now <= peak || p.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	p.calls.Add(1)
	return func() { p.inFlight.Add(-1) }
}

func (p *lx126Provider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	defer p.enter()()
	select {
	case <-time.After(p.hold):
	case <-ctx.Done():
		return llm.CompletionResponse{}, ctx.Err()
	}
	return llm.CompletionResponse{Content: `{"category":"billing","confidence":0.9,"reasoning":"r"}`, Model: req.Model, Provider: "local", FinishReason: "stop"}, nil
}

func (p *lx126Provider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		defer p.enter()()
		if !yield(llm.StreamChunk{Delta: "a summary"}, nil) {
			return
		}
		select {
		case <-time.After(p.hold):
		case <-ctx.Done():
			yield(llm.StreamChunk{}, ctx.Err())
			return
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "a summary", Model: req.Model, Provider: "local", FinishReason: "stop"}}, nil)
	}
}
func (p *lx126Provider) Name() string                               { return "local" }
func (p *lx126Provider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *lx126Provider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func lx126Classify(ctx context.Context) error {
	opts := schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"})
	opts.CommonOptions.Context = ctx
	_, err := schemaflux.Classify[string, string]("My invoice is wrong", opts)
	return err
}

func lx126Stream(ctx context.Context) error {
	opts := schemaflux.NewSummarizeOptions()
	opts.CommonOptions.Context = ctx
	stream, err := schemaflux.StreamSummarize("a long report", opts)
	if err != nil {
		return err
	}
	_, err = stream.Text()
	return err
}

// LX-126 through the exported API. A client's scheduler bounds how many of its
// calls are with the provider at once. Everything used here is exported: the
// scheduler's own types were not, so nothing outside this module could give a
// client one, and a client given one had as many calls in flight as callers.
func TestIntegrationAClientsSchedulerBoundsItsCalls(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	const callers = 12

	cases := []struct {
		name   string
		limits *schemaflux.SchedulerLimits // nil for a client with no scheduler
		call   func(context.Context) error
		ceil   int64
	}{
		{"buffered, one at a time", &schemaflux.SchedulerLimits{MaxConcurrent: 1, MaxQueued: 100}, lx126Classify, 1},
		{"buffered, three at a time", &schemaflux.SchedulerLimits{MaxConcurrent: 3, MaxQueued: 100}, lx126Classify, 3},
		{"buffered, a limit per provider", &schemaflux.SchedulerLimits{PerProviderConcurrency: 2}, lx126Classify, 2},
		{"buffered, no limits set", &schemaflux.SchedulerLimits{}, lx126Classify, callers},
		{"buffered, no scheduler", nil, lx126Classify, callers},
		{"streamed, one at a time", &schemaflux.SchedulerLimits{MaxConcurrent: 1, MaxQueued: 100}, lx126Stream, 1},
		{"streamed, three at a time", &schemaflux.SchedulerLimits{MaxConcurrent: 3, MaxQueued: 100}, lx126Stream, 3},
		{"streamed, no scheduler", nil, lx126Stream, callers},
	}
	// The provider holds each call for a set time on the fake clock of a
	// synctest bubble, which advances only when every goroutine is blocked. So
	// the callers have all been admitted or queued before any call finishes, and
	// the most calls ever in flight is the limit exactly, not at most the limit.
	const hold = 15 * time.Millisecond
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				provider := &lx126Provider{hold: hold}
				client := schemaflux.NewClient("").WithProviderInstance(provider)
				var scheduler *schemaflux.Scheduler
				if tc.limits != nil {
					scheduler = schemaflux.NewScheduler(*tc.limits)
					defer scheduler.Close(context.Background())
					client.WithScheduler(scheduler)
				}
				ctx := client.Context(context.Background())

				start := time.Now()
				var wg sync.WaitGroup
				failures := make(chan error, callers)
				for range callers {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if err := tc.call(ctx); err != nil {
							failures <- err
						}
					}()
				}
				wg.Wait()
				elapsed := time.Since(start)
				close(failures)
				for err := range failures {
					t.Errorf("a call failed: %v", err)
				}
				if got := provider.calls.Load(); got != callers {
					t.Errorf("the provider was called %d times, want %d", got, callers)
				}
				if peak := provider.peak.Load(); peak != tc.ceil {
					t.Errorf("%d calls were in flight at once, want the limit, %d", peak, tc.ceil)
				}
				// The calls go through in rounds of the limit, each taking one hold.
				if rounds := time.Duration((callers + tc.ceil - 1) / tc.ceil); elapsed != rounds*hold {
					t.Errorf("%d calls took %v, want %d rounds of %v", callers, elapsed, rounds, hold)
				}
				if scheduler != nil {
					// Everything that is going to settle has settled once every
					// goroutine of the bubble is blocked.
					synctest.Wait()
					if stats := scheduler.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
						t.Errorf("the scheduler did not come back to idle: %+v", stats)
					}
				}
			})
		})
	}

	t.Run("a call there is no room for is refused and not sent", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &lx126Provider{hold: 200 * time.Millisecond}
			scheduler := schemaflux.NewScheduler(schemaflux.SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})
			defer scheduler.Close(context.Background())
			ctx := schemaflux.NewClient("").WithProviderInstance(provider).WithScheduler(scheduler).Context(context.Background())

			// Each caller is let run until every goroutine is blocked: the first
			// with the provider, the second in the queue.
			results := make(chan error, 2)
			go func() { results <- lx126Classify(ctx) }()
			synctest.Wait()
			if got := provider.inFlight.Load(); got != 1 {
				t.Fatalf("%d calls with the provider, want 1", got)
			}
			go func() { results <- lx126Classify(ctx) }()
			synctest.Wait()
			if got := scheduler.Stats().Queued; got != 1 {
				t.Fatalf("%d calls queued, want 1", got)
			}

			if err := lx126Classify(ctx); !errors.Is(err, schemaflux.ErrAdmissionRejected) {
				t.Errorf("err = %v, want an admission refusal", err)
			}
			for range 2 {
				if err := <-results; err != nil {
					t.Errorf("an admitted call failed: %v", err)
				}
			}
			if got := provider.calls.Load(); got != 2 {
				t.Errorf("the provider was called %d times, want 2", got)
			}
		})
	})

	t.Run("a closed scheduler refuses", func(t *testing.T) {
		provider := &lx126Provider{}
		scheduler := schemaflux.NewScheduler(schemaflux.SchedulerLimits{MaxConcurrent: 1})
		ctx := schemaflux.NewClient("").WithProviderInstance(provider).WithScheduler(scheduler).Context(context.Background())
		if err := scheduler.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := lx126Classify(ctx); !errors.Is(err, schemaflux.ErrShutdown) {
			t.Errorf("err = %v, want a shutdown refusal", err)
		}
		if provider.calls.Load() != 0 {
			t.Error("the provider was called after the scheduler closed")
		}
	})
}

// LX-126 through the exported API. A client's data policy decides which routes
// its calls may take, and a refused call sends nothing.
func TestIntegrationAClientsDataPolicyRefusesARouteItDoesNotAllow(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	eu := schemaflux.DataPolicy{AllowedProviders: []string{"local"}, AllowedRegions: []string{"eu"}}

	type route struct {
		regions   []string
		retention int
	}
	cases := []struct {
		name    string
		policy  *schemaflux.DataPolicy // nil for a client with no policy
		route   *route                 // nil for a route that declared nothing
		refused bool
	}{
		{"no policy, nothing declared", nil, nil, false},
		{"a policy the route meets", &eu, &route{[]string{"eu"}, 0}, false},
		{"an empty policy", &schemaflux.DataPolicy{}, &route{[]string{"eu"}, 0}, true},
		{"a provider the policy does not name", &schemaflux.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}, &route{[]string{"eu"}, 0}, true},
		{"a route that declared nothing", &eu, nil, true},
		{"a region the policy does not allow", &eu, &route{[]string{"us"}, 0}, true},
		{"a route that may also run somewhere else", &eu, &route{[]string{"eu", "us"}, 0}, true},
		{"a route that keeps content", &eu, &route{[]string{"eu"}, 30}, true},
		{"retention the policy allows", &schemaflux.DataPolicy{AllowedProviders: []string{"local"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 30}, &route{[]string{"eu"}, 30}, false},
		{"retention unspecified and none kept", &schemaflux.DataPolicy{AllowedProviders: []string{"local"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: schemaflux.RetentionUnspecified}, &route{[]string{"eu"}, 0}, false},
	}
	for _, tc := range cases {
		for _, path := range []struct {
			name string
			call func(context.Context) error
		}{{"buffered", lx126Classify}, {"streamed", lx126Stream}} {
			t.Run(tc.name+", "+path.name, func(t *testing.T) {
				llm.ResetCapabilityRegistryForTest()
				t.Cleanup(llm.ResetCapabilityRegistryForTest)
				if tc.route != nil {
					schemaflux.DeclareRoute("local", "test-model", tc.route.regions, tc.route.retention)
				}
				provider := &lx126Provider{}
				client := schemaflux.NewClient("").WithProviderInstance(provider)
				if tc.policy != nil {
					client.WithDataPolicy(*tc.policy)
				}

				err := path.call(client.Context(context.Background()))
				if !tc.refused {
					if err != nil {
						t.Fatalf("the call was refused: %v", err)
					}
					return
				}
				if !errors.Is(err, schemaflux.ErrPolicyViolation) {
					t.Fatalf("err = %v, want a policy refusal", err)
				}
				if provider.calls.Load() != 0 {
					t.Fatal("the provider was called for a route the policy forbids")
				}
			})
		}
	}

	t.Run("removing the policy lifts the restriction", func(t *testing.T) {
		llm.ResetCapabilityRegistryForTest()
		provider := &lx126Provider{}
		client := schemaflux.NewClient("").WithProviderInstance(provider).WithDataPolicy(schemaflux.DataPolicy{})
		if err := lx126Classify(client.Context(context.Background())); !errors.Is(err, schemaflux.ErrPolicyViolation) {
			t.Fatalf("err = %v, want a policy refusal under an empty policy", err)
		}
		client.WithoutDataPolicy()
		if err := lx126Classify(client.Context(context.Background())); err != nil {
			t.Fatalf("the call was refused after the policy was removed: %v", err)
		}
	})

	t.Run("one client's policy is not another's", func(t *testing.T) {
		llm.ResetCapabilityRegistryForTest()
		strict := schemaflux.NewClient("").WithProviderInstance(&lx126Provider{}).WithDataPolicy(schemaflux.DataPolicy{})
		open := schemaflux.NewClient("").WithProviderInstance(&lx126Provider{})
		if err := lx126Classify(open.Context(context.Background())); err != nil {
			t.Errorf("the client with no policy was refused: %v", err)
		}
		if err := lx126Classify(strict.Context(context.Background())); !errors.Is(err, schemaflux.ErrPolicyViolation) {
			t.Errorf("err = %v, want the strict client refused", err)
		}
	})
}

// ExampleClient_WithDataPolicy restricts a client to one provider in one
// region. The route says where it runs with DeclareRoute; a route that has
// said nothing is refused, and so is one the policy does not allow. Nothing is
// sent for a refused call. It runs under go test with a scripted provider: no
// credential, no spend.
func ExampleClient_WithDataPolicy() {
	defer llm.ResetCapabilityRegistryForTest()

	client := schemaflux.NewClient("example-key").
		WithProviderInstance(testfixtures.NewScripted(`{"category": "billing", "confidence": 0.9,"reasoning":"r"}`)).
		WithDataPolicy(schemaflux.DataPolicy{
			AllowedProviders: []string{"local"},
			AllowedRegions:   []string{"eu"},
		})

	classify := func() {
		opts := schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"})
		opts.OpOptions.Model = "example-model"
		opts.CommonOptions.Context = client.Context(context.Background())
		result, err := schemaflux.Classify[string, string]("My invoice is wrong", opts)
		switch {
		case errors.Is(err, schemaflux.ErrPolicyViolation):
			fmt.Println("refused by the data policy")
		case err != nil:
			fmt.Println("failed:", err)
		default:
			fmt.Println("classified as", result.Category)
		}
	}

	// Nothing is known about the route yet.
	classify()

	// The route runs in the United States, which the policy does not allow.
	schemaflux.DeclareRoute("local", "example-model", []string{"us"}, 0)
	classify()

	// The route runs in the EU and keeps nothing.
	schemaflux.DeclareRoute("local", "example-model", []string{"eu"}, 0)
	classify()

	// Output:
	// refused by the data policy
	// refused by the data policy
	// classified as billing
}

// ExampleClient_WithScheduler bounds a client to two calls with the provider at
// a time. The rest wait their turn, and every call still returns its answer.
// It runs under go test with a scripted provider: no credential, no spend.
func ExampleClient_WithScheduler() {
	scheduler := schemaflux.NewScheduler(schemaflux.SchedulerLimits{MaxConcurrent: 2, MaxQueued: 16})
	defer scheduler.Close(context.Background())

	provider := &lx126Provider{hold: 5 * time.Millisecond}
	client := schemaflux.NewClient("example-key").WithProviderInstance(provider).WithScheduler(scheduler)

	var wg sync.WaitGroup
	var answered atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opts := schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"})
			opts.OpOptions.Model = "example-model"
			opts.CommonOptions.Context = client.Context(context.Background())
			if _, err := schemaflux.Classify[string, string]("My invoice is wrong", opts); err == nil {
				answered.Add(1)
			}
		}()
	}
	wg.Wait()

	fmt.Println("answered:", answered.Load())
	fmt.Println("never more than two at once:", provider.peak.Load() <= 2)

	// Output:
	// answered: 8
	// never more than two at once: true
}

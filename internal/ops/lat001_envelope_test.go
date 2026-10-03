package ops

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// timedProvider answers after a set time and reports a timing for the call.
type timedProvider struct {
	hold   time.Duration
	timing types.Timing
}

func (p *timedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	select {
	case <-time.After(p.hold):
	case <-ctx.Done():
		return llm.CompletionResponse{}, ctx.Err()
	}
	return llm.CompletionResponse{Content: `{"q1":true,"confidence":0.9}`, FinishReason: "stop", Model: req.Model, Provider: "local", Timing: p.timing}, nil
}
func (p *timedProvider) Name() string                               { return "local" }
func (p *timedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *timedProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// LAT-001. What a provider measured of its call reaches the envelope of the
// operation that made it, and the wait for a slot in the client's scheduler is
// measured beside it. Without this the only figure was the call's whole
// duration, which says a call was slow and not why.
func TestTheEnvelopeSaysWhereTheTimeWent(t *testing.T) {
	ms := time.Millisecond
	measured := types.Timing{Reused: true, Send: ms, Wait: 90 * ms, Read: 5 * ms, Network: 100 * ms}
	options := types.OpOptions{Model: "gpt-6-luna"}

	cases := []struct {
		name     string
		timing   types.Timing
		measured bool
	}{
		{"a provider that times its calls", measured, true},
		{"a provider that paid for a connection", types.Timing{Lookup: 3 * ms, Connect: 10 * ms, Handshake: 30 * ms, Wait: 80 * ms, Network: 130 * ms}, true},
		{"a provider that does not time its calls", types.Timing{}, false},
		{"a provider that reports only the whole", types.Timing{Network: 40 * ms}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithProvider(context.Background(), &timedProvider{timing: tc.timing})
			result, err := IsResult(ctx, "a message", "Is this about billing?", options)
			if err != nil {
				t.Fatalf("IsResult: %v", err)
			}
			if result.Meta.Timing.Measured() != tc.measured {
				t.Fatalf("the envelope's timing measured = %v, want %v: %+v", result.Meta.Timing.Measured(), tc.measured, result.Meta.Timing)
			}
			// A provider that times a call and does not say how many is taken to have
			// timed one.
			want := tc.timing
			if want.Network > 0 {
				want.Calls = 1
			}
			if result.Meta.Timing != want {
				t.Errorf("the envelope's timing is %+v, want what the provider measured, %+v", result.Meta.Timing, want)
			}
		})
	}

	// With a scheduler that lets one call through at a time, the second of two
	// calls made together waits for the first, and its envelope says for how
	// long.
	t.Run("the wait for a slot", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// The bubble's clock moves only when both calls are waiting, so the
			// second one's wait is exactly the first one's hold.
			const hold = 600 * time.Millisecond
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
			defer scheduler.Close(context.Background())
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: &timedProvider{hold: hold, timing: measured}, Scheduler: scheduler})

			var wait sync.WaitGroup
			queued := make([]time.Duration, 2)
			for i := range queued {
				wait.Add(1)
				go func(i int) {
					defer wait.Done()
					result, err := IsResult(ctx, "a message", "Is this about billing?", options)
					if err != nil {
						t.Errorf("IsResult: %v", err)
						return
					}
					queued[i] = result.Meta.Timing.Queued
					if result.Meta.Timing.Wait != measured.Wait || result.Meta.Timing.Network != measured.Network {
						t.Errorf("the provider's own figures were lost: %+v", result.Meta.Timing)
					}
				}(i)
			}
			wait.Wait()

			longer, shorter := queued[0], queued[1]
			if shorter > longer {
				longer, shorter = shorter, longer
			}
			if longer != hold {
				t.Errorf("the call that waited for the other reports %v queued, and the other held the slot for %v", longer, hold)
			}
			if shorter != 0 {
				t.Errorf("the call that went first reports %v queued", shorter)
			}
		})
	})

	// With no scheduler nothing is queued, and a provider that measured
	// nothing leaves the envelope saying so.
	t.Run("no scheduler", func(t *testing.T) {
		ctx := WithProvider(context.Background(), &timedProvider{})
		result, err := IsResult(ctx, "a message", "Is this about billing?", options)
		if err != nil {
			t.Fatalf("IsResult: %v", err)
		}
		if result.Meta.Timing.Queued != 0 || result.Meta.Timing.Measured() {
			t.Errorf("timing %+v with no scheduler and a provider that measured nothing", result.Meta.Timing)
		}
		if outside := result.Meta.Timing.Outside(result.Meta.Elapsed); outside != result.Meta.Elapsed {
			t.Errorf("Outside = %v of %v elapsed, with nothing measured", outside, result.Meta.Elapsed)
		}
	})
}

func TestCallTiming(t *testing.T) {
	ms := time.Millisecond
	answer := func(timing types.Timing) llm.CompletionResponse { return llm.CompletionResponse{Timing: timing} }
	cases := []struct {
		name   string
		queued types.Timing
		timed  bool
		resp   llm.CompletionResponse
		want   *types.Timing
	}{
		{"nothing measured", types.Timing{}, false, answer(types.Timing{}), nil},
		{"the provider measured", types.Timing{}, false, answer(types.Timing{Wait: 5 * ms, Network: 6 * ms}), &types.Timing{Calls: 1, Wait: 5 * ms, Network: 6 * ms}},
		{"a queue and a provider", types.Timing{Queued: 3 * ms}, true, answer(types.Timing{Wait: 5 * ms, Network: 6 * ms}), &types.Timing{Calls: 1, Queued: 3 * ms, Wait: 5 * ms, Network: 6 * ms}},
		{"a queue and a provider that measured nothing", types.Timing{Queued: 3 * ms}, true, answer(types.Timing{}), &types.Timing{Queued: 3 * ms}},
		{"a scheduler with no wait at all", types.Timing{}, true, answer(types.Timing{}), &types.Timing{}},
		{"a reused connection is kept", types.Timing{Queued: ms}, true, answer(types.Timing{Reused: true, Network: 2 * ms}), &types.Timing{Calls: 1, Reused: true, Queued: ms, Network: 2 * ms}},
		{"the provider's phases are kept whole", types.Timing{}, false, answer(types.Timing{Lookup: ms, Connect: 2 * ms, Handshake: 3 * ms, Send: ms, Wait: 4 * ms, Read: ms, Network: 13 * ms}),
			&types.Timing{Calls: 1, Lookup: ms, Connect: 2 * ms, Handshake: 3 * ms, Send: ms, Wait: 4 * ms, Read: ms, Network: 13 * ms}},
		{"a cached answer measured nothing", types.Timing{}, false, llm.CompletionResponse{Cached: true}, nil},
		{"a provider that reports a queue of its own", types.Timing{Queued: 2 * ms}, true, answer(types.Timing{Queued: 5 * ms, Network: ms}), &types.Timing{Calls: 1, Queued: 7 * ms, Network: ms}},
		{"a network figure alone", types.Timing{}, false, answer(types.Timing{Network: 9 * ms}), &types.Timing{Calls: 1, Network: 9 * ms}},
	}
	for _, tc := range cases {
		got := callTiming(tc.queued, tc.timed, tc.resp)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: %+v, want none", tc.name, *got)
		case tc.want != nil && got == nil:
			t.Errorf("%s: none, want %+v", tc.name, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("%s: %+v, want %+v", tc.name, *got, *tc.want)
		}
	}
}

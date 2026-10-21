package ops

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Round 13. A response marked cached measured nothing, whatever timing it
// carries; and a timing summed over several calls is told from one.

func lat13CallTimingCases() []struct {
	name   string
	queued types.Timing
	timed  bool
	resp   llm.CompletionResponse
	want   *types.Timing
} {
	ms := time.Millisecond
	measured := types.Timing{Calls: 1, Lookup: 3 * ms, Connect: 10 * ms, Handshake: 30 * ms, Wait: 700 * ms, Network: 800 * ms}
	return []struct {
		name   string
		queued types.Timing
		timed  bool
		resp   llm.CompletionResponse
		want   *types.Timing
	}{
		{"a cache that replays the original call's timing", types.Timing{}, false, llm.CompletionResponse{Cached: true, Timing: measured}, nil},
		{"the same, with a wait for a slot: the wait is real and the replay is not", types.Timing{Queued: 5 * ms}, true,
			llm.CompletionResponse{Cached: true, Timing: measured}, &types.Timing{Queued: 5 * ms}},
		{"the same, with the scheduler measuring no wait at all", types.Timing{}, true, llm.CompletionResponse{Cached: true, Timing: measured}, &types.Timing{}},
		{"a cached answer whose replayed timing had a queue of its own", types.Timing{}, false,
			llm.CompletionResponse{Cached: true, Timing: types.Timing{Queued: time.Second, Network: time.Second}}, nil},
		{"a cached answer that carries a reused connection", types.Timing{}, false,
			llm.CompletionResponse{Cached: true, Timing: types.Timing{Reused: true, Network: ms}}, nil},
		{"an answer that is not cached keeps its timing", types.Timing{}, false, llm.CompletionResponse{Timing: measured}, &measured},
		{"a provider that does not say how many calls it timed is counted as one", types.Timing{}, false,
			llm.CompletionResponse{Timing: types.Timing{Wait: 5 * ms, Network: 6 * ms}}, &types.Timing{Calls: 1, Wait: 5 * ms, Network: 6 * ms}},
		{"a provider that timed two calls says so", types.Timing{}, false,
			llm.CompletionResponse{Timing: types.Timing{Calls: 2, Network: 6 * ms}}, &types.Timing{Calls: 2, Network: 6 * ms}},
		{"a wait for a slot is not a call", types.Timing{Queued: 4 * ms}, true, llm.CompletionResponse{}, &types.Timing{Queued: 4 * ms}},
		{"a wait for a slot and a call", types.Timing{Queued: 4 * ms}, true, llm.CompletionResponse{Timing: measured},
			&types.Timing{Calls: 1, Queued: 4 * ms, Lookup: 3 * ms, Connect: 10 * ms, Handshake: 30 * ms, Wait: 700 * ms, Network: 800 * ms}},
		{"nothing at all", types.Timing{}, false, llm.CompletionResponse{}, nil},
	}
}

func TestACachedAnswerIsNotTimedWhateverItCarries(t *testing.T) {
	for _, tc := range lat13CallTimingCases() {
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

// A cache that somebody else wrote, which marks a hit as cached and leaves the
// original call's timing on it, is not believed.
func TestACachedResponseFromAnotherCacheIsNotTimedInTheEnvelope(t *testing.T) {
	replay := types.Timing{Calls: 1, Connect: 20 * time.Millisecond, Wait: 700 * time.Millisecond, Network: 800 * time.Millisecond}
	provider := &captureProvider{resp: llm.CompletionResponse{Content: `{"q1":true,"confidence":0.9}`, FinishReason: "stop", Model: "m", Provider: "local", Cached: true, Timing: replay}}
	result, err := IsResult(WithProvider(context.Background(), provider), "a message", "Is this about billing?", types.OpOptions{Model: "gpt-6-luna"})
	if err != nil {
		t.Fatalf("IsResult: %v", err)
	}
	if result.Meta.Timing.Measured() || result.Meta.Timing != (types.Timing{}) {
		t.Errorf("a cached answer reports timing %+v", result.Meta.Timing)
	}
}

func lat13Record(timing *types.Timing) *types.ResultMetadata {
	return &types.ResultMetadata{Model: "m", RequestedModel: "m", Duration: time.Second, Timing: timing}
}

// An envelope folds the call records of an answer. The timings are summed and
// say how many calls they cover; only a record that timed a call has a say in
// whether the connection was reused; Outside is not meaningful for more than
// one call and says so.
func TestTheEnvelopeSumsTheTimingsOfSeveralCalls(t *testing.T) {
	ms := time.Millisecond
	reused := &types.Timing{Calls: 1, Reused: true, Wait: 90 * ms, Network: 100 * ms}
	fresh := &types.Timing{Calls: 1, Connect: 10 * ms, Wait: 90 * ms, Network: 110 * ms}
	waited := &types.Timing{Queued: 30 * ms}
	cases := []struct {
		name       string
		records    []*types.ResultMetadata
		calls      int
		reused     bool
		network    time.Duration
		queued     time.Duration
		measured   bool
		outside    time.Duration // for an elapsed time of a second
		outsideSet bool
	}{
		{"no timing at all", []*types.ResultMetadata{lat13Record(nil)}, 0, false, 0, 0, false, time.Second, true},
		{"one call", []*types.ResultMetadata{lat13Record(reused)}, 1, true, 100 * ms, 0, true, 900 * ms, true},
		{"a repaired call: two records", []*types.ResultMetadata{lat13Record(reused), lat13Record(reused)}, 2, true, 200 * ms, 0, true, 0, true},
		{"a repaired call that opened a connection the second time", []*types.ResultMetadata{lat13Record(reused), lat13Record(fresh)}, 2, false, 210 * ms, 0, true, 0, true},
		{"three calls", []*types.ResultMetadata{lat13Record(reused), lat13Record(reused), lat13Record(reused)}, 3, true, 300 * ms, 0, true, 0, true},
		{"a call that waited for a slot", []*types.ResultMetadata{lat13Record(&types.Timing{Calls: 1, Reused: true, Queued: 30 * ms, Network: 100 * ms})}, 1, true, 100 * ms, 30 * ms, true, 870 * ms, true},
		{"a record of a wait for a slot and a reused call: only the call votes", []*types.ResultMetadata{lat13Record(waited), lat13Record(reused)}, 1, true, 100 * ms, 30 * ms, true, 870 * ms, true},
		{"a reused call and then a record of a wait", []*types.ResultMetadata{lat13Record(reused), lat13Record(waited)}, 1, true, 100 * ms, 30 * ms, true, 870 * ms, true},
		{"a fresh call and a record of a wait", []*types.ResultMetadata{lat13Record(fresh), lat13Record(waited)}, 1, false, 110 * ms, 30 * ms, true, 860 * ms, true},
		{"a record with no timing among timed ones", []*types.ResultMetadata{lat13Record(reused), lat13Record(nil), lat13Record(reused)}, 2, true, 200 * ms, 0, true, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := tc.records
			meta := envelopeFrom(records, "op")
			got := meta.Timing
			if got.Calls != tc.calls || got.Reused != tc.reused || got.Network != tc.network || got.Queued != tc.queued || got.Measured() != tc.measured {
				t.Errorf("timing %+v; want calls %d, reused %v, network %v, queued %v, measured %v", got, tc.calls, tc.reused, tc.network, tc.queued, tc.measured)
			}
			if outside := got.Outside(time.Second); outside != tc.outside {
				t.Errorf("Outside = %v, want %v", outside, tc.outside)
			}
		})
	}
}

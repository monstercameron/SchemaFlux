package types

import (
	"testing"
	"time"
)

func TestTimingAdd(t *testing.T) {
	ms := time.Millisecond
	one := Timing{Reused: true, Queued: 1 * ms, Lookup: 2 * ms, Connect: 3 * ms, Handshake: 4 * ms, Send: 5 * ms, Wait: 6 * ms, Read: 7 * ms, Network: 30 * ms}
	cases := []struct {
		name string
		a, b Timing
		want Timing
	}{
		{"two of nothing", Timing{}, Timing{}, Timing{}},
		{"something and nothing: nothing does not vote", one, Timing{}, Timing{Reused: true, Queued: 1 * ms, Lookup: 2 * ms, Connect: 3 * ms, Handshake: 4 * ms, Send: 5 * ms, Wait: 6 * ms, Read: 7 * ms, Network: 30 * ms}},
		{"nothing and something: nothing does not vote", Timing{}, one, Timing{Reused: true, Queued: 1 * ms, Lookup: 2 * ms, Connect: 3 * ms, Handshake: 4 * ms, Send: 5 * ms, Wait: 6 * ms, Read: 7 * ms, Network: 30 * ms}},
		{"the same twice", one, one, Timing{Reused: true, Queued: 2 * ms, Lookup: 4 * ms, Connect: 6 * ms, Handshake: 8 * ms, Send: 10 * ms, Wait: 12 * ms, Read: 14 * ms, Network: 60 * ms}},
		{"both reused", Timing{Reused: true, Wait: ms}, Timing{Reused: true, Wait: ms}, Timing{Reused: true, Wait: 2 * ms}},
		{"the first reused only", Timing{Reused: true, Wait: ms}, Timing{Wait: ms}, Timing{Wait: 2 * ms}},
		{"the second reused only", Timing{Wait: ms}, Timing{Reused: true, Wait: ms}, Timing{Wait: 2 * ms}},
		{"queued only", Timing{Queued: 5 * ms}, Timing{Queued: 7 * ms}, Timing{Queued: 12 * ms}},
		{"network only", Timing{Network: 5 * ms}, Timing{Network: 7 * ms}, Timing{Network: 12 * ms}},
		{"a wait and a read", Timing{Wait: 100 * ms}, Timing{Read: 40 * ms}, Timing{Wait: 100 * ms, Read: 40 * ms}},
	}
	for _, tc := range cases {
		if got := tc.a.Add(tc.b); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestTimingMeasuredSetupAndOutside(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		name     string
		timing   Timing
		elapsed  time.Duration
		measured bool
		setup    time.Duration
		outside  time.Duration
	}{
		{"nothing", Timing{}, 0, false, 0, 0},
		{"nothing, with time elapsed", Timing{}, 50 * ms, false, 0, 50 * ms},
		{"a network call", Timing{Network: 40 * ms, Wait: 30 * ms}, 50 * ms, true, 0, 10 * ms},
		{"a queue only", Timing{Queued: 20 * ms}, 50 * ms, true, 0, 30 * ms},
		{"a queue and a call", Timing{Queued: 20 * ms, Network: 25 * ms}, 50 * ms, true, 0, 5 * ms},
		{"a new connection", Timing{Lookup: 1 * ms, Connect: 2 * ms, Handshake: 3 * ms, Network: 40 * ms}, 45 * ms, true, 6 * ms, 5 * ms},
		{"more network than elapsed", Timing{Network: 60 * ms}, 50 * ms, true, 0, 0},
		{"exactly the elapsed time", Timing{Network: 30 * ms, Queued: 20 * ms}, 50 * ms, true, 0, 0},
		{"a handshake alone", Timing{Handshake: 9 * ms, Network: 10 * ms}, 10 * ms, true, 9 * ms, 0},
		{"reused, and timed", Timing{Reused: true, Wait: 5 * ms, Network: 6 * ms}, 8 * ms, true, 0, 2 * ms},
		{"reused, and nothing timed", Timing{Reused: true}, 8 * ms, false, 0, 8 * ms},
	}
	for _, tc := range cases {
		if got := tc.timing.Measured(); got != tc.measured {
			t.Errorf("%s: Measured = %v, want %v", tc.name, got, tc.measured)
		}
		if got := tc.timing.Setup(); got != tc.setup {
			t.Errorf("%s: Setup = %v, want %v", tc.name, got, tc.setup)
		}
		if got := tc.timing.Outside(tc.elapsed); got != tc.outside {
			t.Errorf("%s: Outside = %v, want %v", tc.name, got, tc.outside)
		}
	}
}

// A Timing is a sum over the calls an answer took, and says how many that is.
// Elapsed is the last call's time, so a repaired answer's Network is longer
// than its Elapsed and a batch's concurrent chunks add up to more than the wall
// time; Outside, which subtracts one from the other, is only meaningful for a
// single call, and says nothing for more.
func TestTimingCountsTheCallsItCovers(t *testing.T) {
	ms := time.Millisecond
	one := Timing{Calls: 1, Network: 10 * ms}
	cases := []struct {
		name string
		a, b Timing
		want int
	}{
		{"nothing and nothing", Timing{}, Timing{}, 0},
		{"one and nothing", one, Timing{}, 1},
		{"nothing and one", Timing{}, one, 1},
		{"one and one", one, one, 2},
		{"two and one", Timing{Calls: 2}, one, 3},
		{"a queue wait is no call", Timing{Queued: ms}, one, 1},
		{"two queue waits are no calls", Timing{Queued: ms}, Timing{Queued: ms}, 0},
		{"five and five", Timing{Calls: 5}, Timing{Calls: 5}, 10},
		{"a sum of three, added one at a time", one.Add(one), one, 3},
		{"the sum is the same either way round", one, one.Add(one), 3},
	}
	for _, tc := range cases {
		if got := tc.a.Add(tc.b).Calls; got != tc.want {
			t.Errorf("%s: Calls = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Outside is the difference between the whole call's time and what the network
// and the queue took of it. For more than one call the two are not the same
// kind of figure, since the network time is a sum and the elapsed time is one
// call's, so the difference says nothing and it is zero.
func TestTimingOutsideIsOnlyMeaningfulForOneCall(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		name    string
		timing  Timing
		elapsed time.Duration
		want    time.Duration
	}{
		{"one call", Timing{Calls: 1, Network: 40 * ms}, 50 * ms, 10 * ms},
		{"one call and a queue", Timing{Calls: 1, Network: 30 * ms, Queued: 10 * ms}, 50 * ms, 10 * ms},
		{"no count, a hand-built timing of one call", Timing{Network: 40 * ms}, 50 * ms, 10 * ms},
		{"a queue only", Timing{Queued: 20 * ms}, 50 * ms, 30 * ms},
		{"two calls, however much is left", Timing{Calls: 2, Network: 20 * ms}, 500 * ms, 0},
		{"two calls whose sum is less than the elapsed time", Timing{Calls: 2, Network: 10 * ms}, 50 * ms, 0},
		{"two calls whose sum is more than the elapsed time", Timing{Calls: 2, Network: 90 * ms}, 50 * ms, 0},
		{"three calls and a queue", Timing{Calls: 3, Network: 30 * ms, Queued: 5 * ms}, 100 * ms, 0},
		{"the sum of two calls", Timing{Calls: 1, Network: 10 * ms}.Add(Timing{Calls: 1, Network: 10 * ms}), 100 * ms, 0},
		{"one call and a scheduler wait added to it", Timing{Calls: 1, Network: 10 * ms}.Add(Timing{Queued: 5 * ms}), 100 * ms, 85 * ms},
		{"one call, never below zero", Timing{Calls: 1, Network: 60 * ms}, 50 * ms, 0},
	}
	for _, tc := range cases {
		if got := tc.timing.Outside(tc.elapsed); got != tc.want {
			t.Errorf("%s: Outside = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Only a record that timed a call has a say in whether the connection was
// reused. A record that holds nothing but the wait for a slot has no connection
// to speak of, and is not a call that opened a new one.
func TestTimingReusedIsVotedByTheCallsThatWereTimed(t *testing.T) {
	ms := time.Millisecond
	reused := Timing{Calls: 1, Reused: true, Network: ms}
	fresh := Timing{Calls: 1, Network: ms}
	waited := Timing{Queued: ms}
	cases := []struct {
		name string
		a, b Timing
		want bool
	}{
		{"two reused", reused, reused, true},
		{"one reused, one not", reused, fresh, false},
		{"one not, one reused", fresh, reused, false},
		{"neither", fresh, fresh, false},
		{"a reused call and a wait for a slot", reused, waited, true},
		{"a wait for a slot and a reused call", waited, reused, true},
		{"a fresh call and a wait for a slot", fresh, waited, false},
		{"a wait for a slot and a fresh call", waited, fresh, false},
		{"two waits for a slot", waited, waited, false},
		{"three: reused, a wait, reused", reused.Add(waited), reused, true},
		{"three: reused, a wait, fresh", reused.Add(waited), fresh, false},
		{"a hand-built timing that measured its network time", Timing{Reused: true, Network: ms}, waited, true},
	}
	for _, tc := range cases {
		if got := tc.a.Add(tc.b).Reused; got != tc.want {
			t.Errorf("%s: Reused = %v, want %v", tc.name, got, tc.want)
		}
	}
}

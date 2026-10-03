package llm

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Round 13. The clock describes the attempt that produced the response.
//
// It took the first time each event fired, so a request the transport sent
// twice, on a pooled connection that turned out to be dead and then on a new
// one, kept the first attempt's write and the second attempt's first byte: the
// wait contained the redial, which was also counted as the connect and the
// handshake, and the call said it had reused a connection while it had paid for
// a new one. A dial that lost the race to an idle connection fired its events
// all the same, and a redirect's second hop was read as one long wait. These
// drive the transport's events in those orders directly, with a clock of the
// test's own, so no network is needed and no timing is left to chance.

type clockEvent struct {
	ms   int
	name string
}

// fireClockEvents plays a script of events into a fresh clock and returns the timing
// as of end.
func fireClockEvents(t *testing.T, script []clockEvent, end int) types.Timing {
	t.Helper()
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	var current time.Time
	clock := &callClock{start: base, now: func() time.Time { return current }}
	trace := clock.trace()
	failure := errors.New("failed")
	for _, event := range script {
		current = base.Add(time.Duration(event.ms) * time.Millisecond)
		switch event.name {
		case "getconn":
			trace.GetConn("host:443")
		case "dnsstart":
			trace.DNSStart(httptrace.DNSStartInfo{})
		case "dnsdone":
			trace.DNSDone(httptrace.DNSDoneInfo{})
		case "connstart":
			trace.ConnectStart("tcp", "1.2.3.4:443")
		case "conndone":
			trace.ConnectDone("tcp", "1.2.3.4:443", nil)
		case "conndone-err":
			trace.ConnectDone("tcp", "1.2.3.4:443", failure)
		case "tlsstart":
			trace.TLSHandshakeStart()
		case "tlsdone":
			trace.TLSHandshakeDone(tls.ConnectionState{}, nil)
		case "tlsdone-err":
			trace.TLSHandshakeDone(tls.ConnectionState{}, failure)
		case "gotconn-new":
			trace.GotConn(httptrace.GotConnInfo{Reused: false})
		case "gotconn-reused":
			trace.GotConn(httptrace.GotConnInfo{Reused: true})
		case "wrote":
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		case "wrote-err":
			trace.WroteRequest(httptrace.WroteRequestInfo{Err: failure})
		case "firstbyte":
			trace.GotFirstResponseByte()
		default:
			t.Fatalf("no event called %q", event.name)
		}
	}
	current = base.Add(time.Duration(end) * time.Millisecond)
	return clock.timing()
}

func TestTheClockDescribesTheAttemptThatAnswered(t *testing.T) {
	ms := time.Millisecond
	newConnection := []clockEvent{
		{0, "getconn"}, {0, "dnsstart"}, {10, "dnsdone"}, {10, "connstart"}, {30, "conndone"},
		{30, "tlsstart"}, {70, "tlsdone"}, {70, "gotconn-new"}, {72, "wrote"}, {272, "firstbyte"},
	}
	cases := []struct {
		name   string
		script []clockEvent
		end    int
		want   types.Timing // Network is checked as end
	}{
		{"a new connection", newConnection, 300,
			types.Timing{Lookup: 10 * ms, Connect: 20 * ms, Handshake: 40 * ms, Send: 2 * ms, Wait: 200 * ms, Read: 28 * ms}},
		{"a reused connection", []clockEvent{{0, "getconn"}, {1, "gotconn-reused"}, {2, "wrote"}, {102, "firstbyte"}}, 110,
			types.Timing{Reused: true, Send: ms, Wait: 100 * ms, Read: 8 * ms}},
		{"a pooled connection that was dead, then a new one: the phases are the new one's", []clockEvent{
			{0, "getconn"}, {1, "gotconn-reused"}, {2, "wrote-err"},
			{5, "getconn"}, {5, "dnsstart"}, {15, "dnsdone"}, {15, "connstart"}, {35, "conndone"},
			{35, "tlsstart"}, {75, "tlsdone"}, {75, "gotconn-new"}, {76, "wrote"}, {176, "firstbyte"}}, 180,
			types.Timing{Lookup: 10 * ms, Connect: 20 * ms, Handshake: 40 * ms, Send: ms, Wait: 100 * ms, Read: 4 * ms}},
		// The mutation run of 2026-10-03 took out the restart on a second
		// GetConn and nothing noticed: the case above begins on a connection
		// that was reused, so there was nothing of the first attempt's dial to
		// carry over. Here the first attempt dialled.
		{"a new connection whose write failed, then a second dial: the phases are the second dial's", []clockEvent{
			{0, "getconn"}, {0, "dnsstart"}, {10, "dnsdone"}, {10, "connstart"}, {30, "conndone"},
			{30, "tlsstart"}, {70, "tlsdone"}, {70, "gotconn-new"}, {71, "wrote-err"},
			{80, "getconn"}, {80, "dnsstart"}, {85, "dnsdone"}, {85, "connstart"}, {100, "conndone"},
			{100, "tlsstart"}, {120, "tlsdone"}, {120, "gotconn-new"}, {121, "wrote"}, {221, "firstbyte"}}, 230,
			types.Timing{Lookup: 5 * ms, Connect: 15 * ms, Handshake: 20 * ms, Send: ms, Wait: 100 * ms, Read: 9 * ms}},
		{"a dead connection, then another idle one", []clockEvent{
			{0, "getconn"}, {1, "gotconn-reused"}, {2, "wrote-err"},
			{3, "getconn"}, {4, "gotconn-reused"}, {5, "wrote"}, {55, "firstbyte"}}, 60,
			types.Timing{Reused: true, Send: ms, Wait: 50 * ms, Read: 5 * ms}},
		{"a write that failed and no second attempt", []clockEvent{{0, "getconn"}, {1, "gotconn-reused"}, {2, "wrote-err"}}, 10,
			types.Timing{Reused: true}},
		{"a dial that lost to an idle connection, its events before the connection came", []clockEvent{
			{0, "getconn"}, {0, "dnsstart"}, {5, "dnsdone"}, {5, "connstart"}, {20, "conndone"},
			{20, "tlsstart"}, {50, "tlsdone"}, {12, "gotconn-reused"}, {13, "wrote"}, {63, "firstbyte"}}, 70,
			types.Timing{Reused: true, Send: ms, Wait: 50 * ms, Read: 7 * ms}},
		{"a dial that lost to an idle connection, its events after the connection came", []clockEvent{
			{0, "getconn"}, {1, "gotconn-reused"}, {2, "wrote"},
			{3, "dnsstart"}, {8, "dnsdone"}, {8, "connstart"}, {30, "conndone"}, {30, "tlsstart"}, {40, "tlsdone"},
			{52, "firstbyte"}}, 60,
			types.Timing{Reused: true, Send: ms, Wait: 50 * ms, Read: 8 * ms}},
		{"a redirect to a connection that is open: the wait is the second hop's", append(append([]clockEvent(nil), newConnection...),
			clockEvent{300, "getconn"}, clockEvent{301, "gotconn-reused"}, clockEvent{302, "wrote"}, clockEvent{402, "firstbyte"}), 410,
			types.Timing{Reused: true, Send: ms, Wait: 100 * ms, Read: 8 * ms}},
		{"a redirect to another host, which is dialled", []clockEvent{
			{0, "getconn"}, {1, "gotconn-reused"}, {2, "wrote"}, {30, "firstbyte"},
			{31, "getconn"}, {31, "dnsstart"}, {41, "dnsdone"}, {41, "connstart"}, {51, "conndone"},
			{51, "gotconn-new"}, {52, "wrote"}, {152, "firstbyte"}}, 160,
			types.Timing{Lookup: 10 * ms, Connect: 10 * ms, Send: ms, Wait: 100 * ms, Read: 8 * ms}},
		{"a dial to the first address fails and the second works: the connect runs to the one that worked", []clockEvent{
			{0, "getconn"}, {0, "connstart"}, {10, "conndone-err"}, {10, "connstart"}, {25, "conndone"},
			{25, "gotconn-new"}, {26, "wrote"}, {46, "firstbyte"}}, 50,
			types.Timing{Connect: 25 * ms, Send: ms, Wait: 20 * ms, Read: 4 * ms}},
		{"a handshake that failed is not a handshake", []clockEvent{
			{0, "getconn"}, {0, "connstart"}, {5, "conndone"}, {5, "tlsstart"}, {15, "tlsdone-err"}}, 20,
			types.Timing{Connect: 5 * ms}},
		{"an event that fires twice in an attempt keeps its first time", []clockEvent{
			{0, "getconn"}, {5, "connstart"}, {9, "connstart"}, {20, "conndone"}, {20, "gotconn-new"},
			{21, "wrote"}, {21, "wrote"}, {41, "firstbyte"}, {45, "firstbyte"}}, 50,
			types.Timing{Connect: 15 * ms, Send: ms, Wait: 20 * ms, Read: 9 * ms}},
		{"a second connection from a trace that fires no GetConn", []clockEvent{
			{1, "gotconn-reused"}, {2, "wrote"}, {3, "firstbyte"},
			{10, "gotconn-reused"}, {11, "wrote"}, {60, "firstbyte"}}, 70,
			types.Timing{Reused: true, Send: ms, Wait: 49 * ms, Read: 10 * ms}},
		{"nothing fired", nil, 10, types.Timing{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fireClockEvents(t, tc.script, tc.end)
			want := tc.want
			want.Calls = 1
			want.Network = time.Duration(tc.end) * ms
			if got != want {
				t.Errorf("timing = %+v\nwant     %+v", got, want)
			}
			if got.Reused && got.Setup() != 0 {
				t.Errorf("the call says it reused a connection and paid %v for one", got.Setup())
			}
			if phases := got.Setup() + got.Send + got.Wait + got.Read; phases > got.Network {
				t.Errorf("the phases add up to %v, more than the whole call's %v", phases, got.Network)
			}
			for name, phase := range map[string]time.Duration{"lookup": got.Lookup, "connect": got.Connect, "handshake": got.Handshake, "send": got.Send, "wait": got.Wait, "read": got.Read} {
				if phase < 0 {
					t.Errorf("%s is %v", name, phase)
				}
			}
		})
	}
}

// Events come from the transport's own goroutines, and one can arrive while the
// timing is being read, or after it was.
func TestTheClockIsSafeAgainstEventsFromOtherGoroutines(t *testing.T) {
	clock := &callClock{start: time.Now()}
	trace := clock.trace()
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 200 {
				trace.GetConn("host:443")
				trace.DNSStart(httptrace.DNSStartInfo{})
				trace.DNSDone(httptrace.DNSDoneInfo{})
				trace.ConnectStart("tcp", "x")
				trace.ConnectDone("tcp", "x", nil)
				trace.GotConn(httptrace.GotConnInfo{})
				trace.WroteRequest(httptrace.WroteRequestInfo{})
				trace.GotFirstResponseByte()
				if got := clock.timing(); got.Network < 0 || got.Read < 0 || got.Wait < 0 || got.Send < 0 {
					t.Errorf("a negative time: %+v", got)
					return
				}
			}
		}()
	}
	wait.Wait()
}

// doTimed and withCallClock, directly.
func TestDoTimedHandsBackTheClockWhateverHappens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(200 * time.Millisecond)
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name    string
		ctx     context.Context
		url     string
		client  *http.Client
		wantErr bool
		reused  bool // asked of a second call on the same client
	}{
		{"an answer", context.Background(), server.URL + "/ok", server.Client(), false, true},
		{"a redirect, followed", context.Background(), server.URL + "/redirect", server.Client(), false, true},
		{"a server that is gone", context.Background(), goneURL, &http.Client{}, true, false},
		{"a context that is over", cancelled, server.URL + "/ok", server.Client(), true, false},
		{"a client with a timeout that passes", context.Background(), server.URL + "/slow", &http.Client{Timeout: 20 * time.Millisecond}, true, false},
		{"a client with a generous timeout", context.Background(), server.URL + "/ok", &http.Client{Timeout: 10 * time.Second}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(tc.ctx, http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			resp, clock, err := doTimed(tc.client, req)
			if clock == nil {
				t.Fatal("no clock was returned")
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			timing := clock.timing()
			if timing.Calls != 1 || timing.Network < 0 || timing.Network > time.Since(started)+time.Millisecond {
				t.Errorf("timing %+v for a call that took %v", timing, time.Since(started))
			}
			if err != nil && timing.Read != 0 {
				t.Errorf("a call that got no answer reports a read of %v", timing.Read)
			}
			if timing.Reused && timing.Setup() != 0 {
				t.Errorf("reused, and paid %v for a connection", timing.Setup())
			}
		})
	}

	t.Run("the request is the caller's, with a context added", func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(context.Background(), key{}, "kept")
		var seen string
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seen, _ = r.Context().Value(key{}).(string)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/", nil)
		if _, _, err := doTimed(client, req); err != nil || seen != "kept" {
			t.Errorf("err %v, the context's value %q", err, seen)
		}
		if req.Context() != ctx {
			t.Error("the caller's request was changed")
		}
	})

	t.Run("two calls have two clocks", func(t *testing.T) {
		first, clockA := withCallClock(context.Background())
		second, clockB := withCallClock(context.Background())
		if clockA == clockB || first == second {
			t.Error("two calls share a clock")
		}
	})

	t.Run("a transport that fires no events leaves the phases at zero and the whole measured", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			time.Sleep(5 * time.Millisecond)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
		_, clock, err := doTimed(client, req)
		if err != nil {
			t.Fatal(err)
		}
		timing := clock.timing()
		if timing.Network < 5*time.Millisecond || timing.Setup() != 0 || timing.Wait != 0 || timing.Send != 0 || timing.Reused || !timing.Measured() {
			t.Errorf("timing %+v", timing)
		}
	})
}

// The caller's own trace sees every event the clock does.
func TestTheCallClockPassesEveryEventToTheCallersTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	var mu sync.Mutex
	seen := map[string]bool{}
	note := func(name string) { mu.Lock(); seen[name] = true; mu.Unlock() }
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GetConn:              func(string) { note("getconn") },
		ConnectStart:         func(string, string) { note("connectstart") },
		ConnectDone:          func(string, string, error) { note("connectdone") },
		GotConn:              func(httptrace.GotConnInfo) { note("gotconn") },
		WroteRequest:         func(httptrace.WroteRequestInfo) { note("wrote") },
		GotFirstResponseByte: func() { note("firstbyte") },
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	resp, clock, err := doTimed(&http.Client{Transport: &http.Transport{}}, req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	for _, name := range []string{"getconn", "connectstart", "connectdone", "gotconn", "wrote", "firstbyte"} {
		if !seen[name] {
			t.Errorf("the caller's trace did not see %s", name)
		}
	}
	if timing := clock.timing(); timing.Calls != 1 || timing.Connect < 0 || timing.Network < timing.Connect {
		t.Errorf("the clock beside it: %+v", timing)
	}
}

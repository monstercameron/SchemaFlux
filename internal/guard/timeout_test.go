package guard_test

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// timeoutRowEnv names the row the child process of the timeout guard runs. A
// goroutine left blocked inside a synctest bubble is a deadlock panic that ends
// the whole test binary, so each row runs in a process of its own and a leak is
// reported for that row alone.
const timeoutRowEnv = "SCHEMAFLUX_GUARD_TIMEOUT_ROW"

// clientTimeout is the Client.Timeout every row is held to, and askedFor is how
// long the fake provider takes to answer: far longer.
const (
	clientTimeout = 2 * time.Second
	askedFor      = time.Hour
)

// slowCalls limits how many child processes run at once on this shared machine.
var slowCalls = make(chan struct{}, 4)

func TestClientTimeoutBindsEveryOperation(t *testing.T) {
	for _, r := range allRows() {
		t.Run(r.id(), func(t *testing.T) {
			if why, ok := r.na["client timeout"]; ok {
				t.Skip(why)
			}
			t.Parallel()
			slowCalls <- struct{}{}
			defer func() { <-slowCalls }()
			cmd := exec.Command(os.Args[0], "-test.run=^TestClientTimeoutChild$", "-test.v")
			cmd.Env = append(os.Environ(), timeoutRowEnv+"="+r.id())
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			if err != nil || !strings.Contains(out.String(), "--- PASS: TestClientTimeoutChild") {
				t.Errorf("a provider that never answers did not end with the timeout kind and no goroutine left:\n%s", tail(out.String(), 1500))
			}
		})
	}
}

// tail is the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// TestClientTimeoutChild is run by TestClientTimeoutBindsEveryOperation in a
// process of its own, for the row named in the environment.
func TestClientTimeoutChild(t *testing.T) {
	id := os.Getenv(timeoutRowEnv)
	if id == "" {
		t.Skip("run by TestClientTimeoutBindsEveryOperation")
	}
	var found *row
	for _, r := range allRows() {
		if r.id() == id {
			r := r
			found = &r
		}
	}
	if found == nil {
		t.Fatalf("no row %q", id)
	}
	for _, st := range []state{statePlain, stateRecorder} {
		synctest.Test(t, func(t *testing.T) {
			x := neutral()
			g := newRig(t, st, clientTimeout, func(m *schemafluxtest.Provider) {
				found.succeeds(m, x, false)
				m.Delay(askedFor)
			})
			start := time.Now()
			res, err := found.call(g.ctx, x)
			elapsed := time.Since(start)
			if err == nil {
				t.Fatalf("%s: the call returned no error after %v with a provider that never answers", st.name, elapsed)
			}
			e := failureOf(err)
			if e == nil || e.Kind != schemaflux.ErrTimeout {
				kind := "none"
				if e != nil {
					kind = e.Kind.Error()
				}
				t.Fatalf("%s: the call ended with %v (a %T; errors.AsType finds kind %s), want an *Error of kind timeout", st.name, err, err, kind)
			}
			if e.Op == "" {
				t.Errorf("%s: the timeout error has no Op", st.name)
			}
			if elapsed < clientTimeout || elapsed > 2*clientTimeout {
				t.Errorf("%s: the call ended after %v, want about the client's timeout of %v", st.name, elapsed, clientTimeout)
			}
			if !isZeroResult(res) {
				t.Errorf("%s: a value of type %s came back beside the timeout", st.name, describeValue(res))
			}
			if g.model.Calls() == 0 {
				t.Errorf("%s: nothing was sent, so the timeout was not what ended the call", st.name)
			}
			if st.recorder {
				records := g.records()
				if len(records) == 0 {
					synctest.Wait()
					if after := g.records(); len(after) > 0 && !strings.HasPrefix(found.name, "Stream") {
						t.Errorf("%s: the Record was not in the recorder when the call returned; it arrived afterwards", st.name)
					}
					records = g.records()
				}
				if found.pkg == "op" && found.name == "RunEach" {
					// RunEach returns collected items with a nil call error;
					// firstItemError above exposes an unfinished item's timeout.
					if len(records) != 1 || records[0].Err != nil {
						t.Errorf("%s: RunEach has %d Records after a timeout, want 1 for the collected run with no call error", st.name, len(records))
					}
				} else if len(records) != 1 || records[0].Err == nil {
					t.Errorf("%s: %d Records after a timeout, want 1 carrying the error", st.name, len(records))
				} else if re := failureOf(records[0].Err); re == nil || re.Kind != schemaflux.ErrTimeout {
					t.Errorf("%s: the Record's error is %v, want kind timeout", st.name, records[0].Err)
				}
			}
		})
	}
}

package redact_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// The helpers of this file are the ones the tests of both functions use. They
// are named for what they do to a call.

// answeringWith returns a fake model that answers its requests with the bodies
// given, in order, the last repeating.
func answeringWith(bodies ...string) *schemafluxtest.Provider {
	var model schemafluxtest.Provider
	model.Reply(bodies...)
	return &model
}

// clientOn returns a context carrying a client on p, with the settings applied to
// the client before it is put there.
func clientOn(p schemaflux.Provider, settings ...func(*schemaflux.Client)) context.Context {
	c := schemaflux.Client{Provider: p}
	for _, apply := range settings {
		apply(&c)
	}
	return schemaflux.WithClient(context.Background(), c)
}

// recordingTo is a client setting that gives the client a recorder.
func recordingTo(r *schemaflux.Recorder) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) { c.Recorder = r }
}

// loggingTo is a client setting that gives the client a logger that keeps what it
// is given.
func loggingTo(l *schemafluxtest.Logs) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) { c.Logger = l.Logger() }
}

// floorRoute declares the route of the fake that a floor is held on.
var floorRoute sync.Once

// underFloor is a client setting that holds the client to a floor of level. The
// provider has to be a fake named "redact-floor" (namedModel makes one).
func underFloor(level schemaflux.ContractLevel) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) {
		floorRoute.Do(func() { provider.DeclareRoute("redact-floor", "test-model", []string{"eu"}, 0) })
		c.Policy = &schemaflux.DataPolicy{Providers: []string{"redact-floor"}, Regions: []string{"eu"}, MinContract: level}
	}
}

// namedModel returns a fake model that answers as answeringWith does and is named
// "redact-floor", the provider underFloor's policy allows.
func namedModel(bodies ...string) *schemafluxtest.Provider {
	model := answeringWith(bodies...)
	model.SetName("redact-floor")
	return model
}

// requestText is what a request says: its system prompt and its user message.
func requestText(req schemaflux.Request) string { return req.System + "\n" + req.User }

// wantText fails the test unless the request says text somewhere.
func wantText(t *testing.T, req schemaflux.Request, text string) {
	t.Helper()
	if !strings.Contains(requestText(req), text) {
		t.Errorf("the request does not say %q: %s", text, excerpt(requestText(req)))
	}
}

// wantNoText fails the test if the request says text anywhere.
func wantNoText(t *testing.T, req schemaflux.Request, text string) {
	t.Helper()
	if strings.Contains(requestText(req), text) {
		t.Errorf("the request says %q: %s", text, excerpt(requestText(req)))
	}
}

// wantError fails the test unless err is an *Error of the kind and for the
// operation given, and returns it.
func wantError(t *testing.T, err error, kind schemaflux.Kind, op string) *schemaflux.Error {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("the error is %T (%v), want an *Error", err, err)
	}
	if e.Kind != kind || e.Op != op {
		t.Fatalf("error = kind %v op %q (%v), want kind %v op %q", e.Kind, e.Op, e, kind, op)
	}
	if !errors.Is(err, kind) {
		t.Errorf("errors.Is(err, %v) is false", kind)
	}
	return e
}

// onlyRecord returns the one Record the recorder holds, or fails the test.
func onlyRecord(t *testing.T, r *schemaflux.Recorder) schemaflux.Record {
	t.Helper()
	records := r.Records()
	if len(records) != 1 {
		t.Fatalf("the recorder holds %d Records, want exactly 1: %+v", len(records), records)
	}
	return records[0]
}

// operationWatcher is a telemetry observer that tells a function what finished.
type operationWatcher struct{ finished func(telemetry.Result) }

func (operationWatcher) Started(ctx context.Context, _ telemetry.Event) context.Context { return ctx }
func (w operationWatcher) Finished(_ context.Context, r telemetry.Result)               { w.finished(r) }

// chainText returns what Error returns for err and for every error beneath it.
func chainText(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		out = append(out, e.Error())
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		}
	}
	walk(err)
	return out
}

// excerpt shortens text for a failure message, so that a test never prints a
// whole prompt.
func excerpt(text string) string {
	const most = 160
	runes := []rune(text)
	if len(runes) <= most {
		return text
	}
	return string(runes[:most]) + "..."
}

// requireNoLeak fails the test if the canary is in anything the error, the
// recorder or the logs hold: what Error says along the whole chain when
// wholeChain is true, and as JSON and as slog's two handlers write it; each Record
// as a value and as JSON; each log line. A call that has nothing to leave behind
// passes an error of nil.
func requireNoLeak(t *testing.T, canary string, err error, wholeChain bool, recorder *schemaflux.Recorder, logs *schemafluxtest.Logs) {
	t.Helper()
	var seen []string
	if wholeChain {
		seen = append(seen, chainText(err)...)
	} else if err != nil {
		seen = append(seen, err.Error())
	}
	if err != nil {
		if encoded, jsonErr := json.Marshal(err); jsonErr == nil {
			seen = append(seen, string(encoded))
		}
		var text, structured bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Error("the call failed", "err", err)
		slog.New(slog.NewJSONHandler(&structured, nil)).Error("the call failed", "err", err)
		seen = append(seen, text.String(), structured.String())
	}
	if recorder != nil {
		for _, record := range recorder.Records() {
			seen = append(seen, fmt.Sprintf("%+v", record))
			if encoded, jsonErr := json.Marshal(record); jsonErr == nil {
				seen = append(seen, string(encoded))
			}
			if wholeChain {
				seen = append(seen, chainText(record.Err)...)
			}
		}
	}
	if logs != nil {
		for _, entry := range logs.Entries() {
			line := entry.Message
			entry.Attrs(func(a slog.Attr) bool {
				line += " " + a.Key + "=" + a.Value.String()
				return true
			})
			seen = append(seen, line)
		}
	}
	for _, text := range seen {
		if strings.Contains(text, canary) {
			t.Errorf("the canary reached %q", excerpt(text))
		}
	}
}

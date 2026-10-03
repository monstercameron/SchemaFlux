package schemafluxtest_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"
	"time"

	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// logsMap is a kept record as the map the tests of log/slog read: the time, the
// level and the message under the standard keys, and each attribute under its
// key, a group as a map.
func logsMap(r slog.Record) map[string]any {
	m := map[string]any{}
	if !r.Time.IsZero() {
		m[slog.TimeKey] = r.Time
	}
	m[slog.LevelKey] = r.Level
	m[slog.MessageKey] = r.Message
	r.Attrs(func(a slog.Attr) bool {
		logsPut(m, a)
		return true
	})
	return m
}

func logsPut(m map[string]any, a slog.Attr) {
	if a.Value.Kind() != slog.KindGroup {
		m[a.Key] = a.Value.Any()
		return
	}
	group := map[string]any{}
	for _, member := range a.Value.Group() {
		logsPut(group, member)
	}
	m[a.Key] = group
}

func TestLogsPassesTheStandardLibrarysHandlerTest(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	results := func() []map[string]any {
		var out []map[string]any
		for _, r := range logs.Entries() {
			out = append(out, logsMap(r))
		}
		return out
	}
	if err := slogtest.TestHandler(&logs, results); err != nil {
		t.Error(err)
	}
}

// logsFlatten is a record's attributes as "group.key=value" in order, which is
// what a test compares.
func logsFlatten(prefix string, attrs []slog.Attr) []string {
	var out []string
	for _, a := range attrs {
		if a.Value.Kind() == slog.KindGroup {
			out = append(out, logsFlatten(prefix+a.Key+".", a.Value.Group())...)
			continue
		}
		out = append(out, prefix+a.Key+"="+a.Value.String())
	}
	return out
}

func logsAttrsOf(r slog.Record) []string {
	var attrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	return logsFlatten("", attrs)
}

type logsSecret struct{ value string }

func (s logsSecret) LogValue() slog.Value {
	return slog.GroupValue(slog.String("kind", "secret"), slog.Int("size", len(s.value)))
}

func TestLogsKeepsWhatWasLoggedAsTheStandardHandlersWouldWriteIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		log  func(l *slog.Logger)
		want []string
	}{
		{"a plain line", func(l *slog.Logger) { l.Info("hello", "a", 1, "b", "two") }, []string{"a=1", "b=two"}},
		{"no attributes", func(l *slog.Logger) { l.Info("bare") }, nil},
		{"With", func(l *slog.Logger) { l.With("a", 1).Info("m", "b", 2) }, []string{"a=1", "b=2"}},
		{"With twice", func(l *slog.Logger) { l.With("a", 1).With("b", 2).Info("m", "c", 3) }, []string{"a=1", "b=2", "c=3"}},
		{"WithGroup", func(l *slog.Logger) { l.WithGroup("g").Info("m", "a", 1) }, []string{"g.a=1"}},
		{"WithGroup and no attributes", func(l *slog.Logger) { l.WithGroup("g").Info("m") }, nil},
		{"With before a group and after", func(l *slog.Logger) {
			l.With("a", 1).WithGroup("g").With("b", 2).Info("m", "c", 3)
		}, []string{"a=1", "g.b=2", "g.c=3"}},
		{"two groups", func(l *slog.Logger) { l.WithGroup("g").WithGroup("h").With("a", 1).Info("m", "b", 2) }, []string{"g.h.a=1", "g.h.b=2"}},
		{"a group with attributes only in the outer one", func(l *slog.Logger) {
			l.WithGroup("g").With("a", 1).WithGroup("h").Info("m")
		}, []string{"g.a=1"}},
		{"an empty group name is no group", func(l *slog.Logger) { l.WithGroup("").Info("m", "a", 1) }, []string{"a=1"}},
		{"a group attribute", func(l *slog.Logger) { l.Info("m", slog.Group("g", "a", 1, "b", 2)) }, []string{"g.a=1", "g.b=2"}},
		{"an empty group attribute is dropped", func(l *slog.Logger) { l.Info("m", slog.Group("g"), "a", 1) }, []string{"a=1"}},
		{"a group with an empty key is inlined", func(l *slog.Logger) { l.Info("m", slog.Group("", "a", 1)) }, []string{"a=1"}},
		{"an attribute with neither key nor value is dropped", func(l *slog.Logger) { l.Info("m", slog.Attr{}, "a", 1) }, []string{"a=1"}},
		{"a value that logs itself is resolved", func(l *slog.Logger) { l.Info("m", "s", logsSecret{"hunter2"}) }, []string{"s.kind=secret", "s.size=7"}},
		{"an attribute given to With that logs itself", func(l *slog.Logger) { l.With("s", logsSecret{"x"}).Info("m") }, []string{"s.kind=secret", "s.size=1"}},
		{"an error", func(l *slog.Logger) { l.Warn("m", "err", errors.New("boom")) }, []string{"err=boom"}},
		{"a duration and a bool", func(l *slog.Logger) { l.Info("m", "d", 1500*time.Millisecond, "ok", true) }, []string{"d=1.5s", "ok=true"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs schemafluxtest.Logs
			tc.log(logs.Logger())
			entries := logs.Entries()
			if len(entries) != 1 {
				t.Fatalf("kept %d records", len(entries))
			}
			if got := logsAttrsOf(entries[0]); !slices.Equal(got, tc.want) {
				t.Errorf("attributes %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLogsKeepsEveryLevelTheTimeAndTheMessage(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	l := logs.Logger()
	before := time.Now()
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	l.Log(t.Context(), slog.Level(-8), "very fine")
	entries := logs.Entries()
	var levels []slog.Level
	var messages []string
	for _, r := range entries {
		levels = append(levels, r.Level)
		messages = append(messages, r.Message)
		if r.Time.Before(before) {
			t.Errorf("record %q is timed %v, before the test began", r.Message, r.Time)
		}
	}
	if !slices.Equal(levels, []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError, -8}) ||
		!slices.Equal(messages, []string{"d", "i", "w", "e", "very fine"}) {
		t.Errorf("levels %v, messages %v", levels, messages)
	}
	for _, level := range []slog.Level{-100, slog.LevelDebug, slog.LevelError, 100} {
		if !logs.Enabled(t.Context(), level) || !logs.Logger().Enabled(t.Context(), level) {
			t.Errorf("level %v is not enabled", level)
		}
	}
	if len(logs.Entries()) != 5 {
		t.Errorf("asking whether a level is enabled kept a record")
	}
	// A record with no time keeps none, as the handlers of the standard library
	// write none.
	_ = logs.Handle(t.Context(), slog.NewRecord(time.Time{}, slog.LevelInfo, "untimed", 0))
	if last := logs.Entries()[5]; !last.Time.IsZero() {
		t.Errorf("a record with no time was given %v", last.Time)
	}
}

func TestLogsResetForgetsTheRecordsAndNotWhatAHandlerWasGiven(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	withAttr := logs.Logger().With("run", 7).WithGroup("g")
	withAttr.Info("one", "a", 1)
	if len(logs.Entries()) != 1 {
		t.Fatal("nothing was kept")
	}
	logs.Reset()
	if got := logs.Entries(); len(got) != 0 {
		t.Errorf("Reset kept %d records", len(got))
	}
	withAttr.Info("two", "b", 2)
	entries := logs.Entries()
	if len(entries) != 1 || entries[0].Message != "two" {
		t.Fatalf("entries after Reset: %+v", entries)
	}
	if got := logsAttrsOf(entries[0]); !slices.Equal(got, []string{"run=7", "g.b=2"}) {
		t.Errorf("attributes %v: Reset must not forget what WithAttrs and WithGroup gave", got)
	}
	logs.Reset()
	logs.Reset()
	if len(logs.Entries()) != 0 {
		t.Error("a second Reset kept a record")
	}
}

func TestEntriesAreCopiesACallerCannotUseToChangeWhatIsHeld(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	logs.Logger().With("fixed", "x").Info("one", "a", 1, slog.Group("g", "b", 2, slog.Group("h", "c", 3)))
	logs.Logger().Info("two")

	first := logs.Entries()
	want := []string{"fixed=x", "a=1", "g.b=2", "g.h.c=3"}
	if got := logsAttrsOf(first[0]); !slices.Equal(got, want) {
		t.Fatalf("attributes %v", got)
	}

	// Change everything a caller can reach in what was returned.
	first[0].Message = "changed"
	first[0].Level = slog.LevelError
	first[0].AddAttrs(slog.String("extra", "y"))
	first[0].Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindGroup {
			members := a.Value.Group()
			members[0] = slog.String("b", "tampered")
			for _, m := range members {
				if m.Value.Kind() == slog.KindGroup {
					m.Value.Group()[0] = slog.String("c", "tampered")
				}
			}
		}
		return true
	})
	first[1], first[0] = first[0], first[1]
	first = append(first[:0], first[1:]...)
	clear(first)

	again := logs.Entries()
	if len(again) != 2 || again[0].Message != "one" || again[0].Level != slog.LevelInfo || again[1].Message != "two" {
		t.Fatalf("the records changed: %+v", again)
	}
	if got := logsAttrsOf(again[0]); !slices.Equal(got, want) {
		t.Errorf("the attributes changed to %v", got)
	}
	// Two calls return slices of their own.
	a, b := logs.Entries(), logs.Entries()
	a[0].AddAttrs(slog.String("only", "a"))
	if reflect.DeepEqual(logsAttrsOf(a[0]), logsAttrsOf(b[0])) {
		t.Error("two calls of Entries share a record")
	}
}

func TestLogsIsSafeForConcurrentUseThroughEveryHandlerItMakes(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	const goroutines, each = 24, 200
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var l *slog.Logger
			switch g % 3 {
			case 0:
				l = logs.Logger()
			case 1:
				l = logs.Logger().With("g", g)
			default:
				l = logs.Logger().WithGroup("grp").With("g", g)
			}
			for i := range each {
				l.Info("line", "i", i)
				if i%50 == 0 {
					_ = logs.Entries()
				}
			}
		}()
	}
	// A reader and a resetter while the writers run: nothing may be lost to a
	// race, and nothing may panic. The resetter is stopped before the count.
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				for _, r := range logs.Entries() {
					logsAttrsOf(r)
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	readers.Wait()

	entries := logs.Entries()
	if len(entries) != goroutines*each {
		t.Fatalf("kept %d records of %d", len(entries), goroutines*each)
	}
	seen := map[string]int{}
	for _, r := range entries {
		attrs := logsAttrsOf(r)
		seen[strings.Join(attrs[:len(attrs)-1], ",")]++
	}
	for g := range goroutines {
		key := ""
		switch g % 3 {
		case 1:
			key = fmt.Sprintf("g=%d", g)
		case 2:
			key = fmt.Sprintf("grp.g=%d", g)
		}
		if seen[key] == 0 {
			t.Errorf("no record of goroutine %d (looked for %q)", g, key)
		}
	}
}

// logsRetrying is a provider that is told to be retried once after a failure,
// quickly.
type logsRetrying struct{ *schemafluxtest.Provider }

func (logsRetrying) RetryPolicy() (int, time.Duration) { return 0, time.Millisecond }

func TestAClientGivenTheLoggerCapturesAFailingCallsLinesAndNothingOfThePayload(t *testing.T) {
	t.Parallel()
	const (
		stateCanary  = "STATE-PAYLOAD-CANARY-3318"
		askCanary    = "QUESTION-PAYLOAD-CANARY-7741"
		vendorCanary = "VENDOR-WORDS-CANARY-5520"
	)
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "down", Err: errors.New(vendorCanary + " " + stateCanary)})

	var logs schemafluxtest.Logs
	ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{
		Provider: logsRetrying{&model}, Model: "m", Retries: 1, Logger: logs.Logger(),
	})
	_, err := ops.Is(ctx, stateCanary, askCanary+"?", types.OpOptions{})
	if err == nil {
		t.Fatal("the call did not fail")
	}
	if model.Calls() != 2 {
		t.Fatalf("the provider received %d requests: the call did not retry, so a line was due", model.Calls())
	}

	entries := logs.Entries()
	if len(entries) == 0 {
		t.Fatal("the client's logger was told of nothing")
	}
	var retry bool
	for _, r := range entries {
		retry = retry || strings.Contains(strings.ToLower(r.Message), "retry")
		text := r.Message + " " + strings.Join(logsAttrsOf(r), " ")
		for _, canary := range []string{stateCanary, askCanary, vendorCanary} {
			if strings.Contains(text, canary) {
				t.Errorf("the line %q holds a payload canary %q", r.Message, canary)
			}
		}
	}
	if !retry {
		var messages []string
		for _, r := range entries {
			messages = append(messages, r.Message)
		}
		t.Errorf("no line is about the retry: %v", messages)
	}
	if strings.Contains(err.Error(), stateCanary) || strings.Contains(err.Error(), vendorCanary) {
		t.Errorf("the error holds the payload: %v", err)
	}

	// A client with no Logger logs nowhere: the capture is the client's own.
	var quiet schemafluxtest.Logs
	other := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: logsRetrying{&model}, Model: "m", Retries: 1})
	_, _ = ops.Is(other, "s", "q?", types.OpOptions{})
	if len(quiet.Entries()) != 0 {
		t.Errorf("a client with no logger wrote to an unrelated Logs")
	}
}

var _ slog.Handler = (*schemafluxtest.Logs)(nil)

func TestLogsWorksAsAHandlerOfItsOwn(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	l := slog.New(&logs)
	l.With("a", 1).Info("via the handler", "b", 2)
	h := logs.WithAttrs([]slog.Attr{slog.Int("c", 3)}).WithGroup("g")
	_ = h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelWarn, "direct", 0))
	if got := logs.Entries(); len(got) != 2 || got[0].Message != "via the handler" || got[1].Message != "direct" {
		t.Fatalf("entries %+v", got)
	}
	if got := logsAttrsOf(logs.Entries()[0]); !slices.Equal(got, []string{"a=1", "b=2"}) {
		t.Errorf("attributes %v", got)
	}
	if got := logsAttrsOf(logs.Entries()[1]); !slices.Equal(got, []string{"c=3"}) {
		t.Errorf("attributes %v: a group that holds nothing must not be kept", got)
	}
	if h2 := logs.WithGroup(""); h2 == nil {
		t.Error("WithGroup of an empty name returned no handler")
	}
}

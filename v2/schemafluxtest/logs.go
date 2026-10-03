package schemafluxtest

import (
	"context"
	"log/slog"
	"slices"
	"sync"
)

// Logs is a slog.Handler that keeps what the library logged, for a test that
// asserts on it or on its absence. The zero value is ready to use:
//
//	var logs schemafluxtest.Logs
//	client := schemaflux.Client{Provider: &model, Logger: logs.Logger()}
//	... make the calls ...
//	for _, entry := range logs.Entries() { ... }
//
// A client with no Logger logs nothing, so a test that wants to see a line, or to
// see that there was none, gives it one.
//
// Logs keeps every record at every level, Debug included: it never filters, so a
// test asserts on a level by reading it from the record. A record is kept as the
// handler of the standard library's own would write it: values that log
// themselves (slog.LogValuer, which is how an *schemaflux.Error logs) are
// resolved, an attribute with neither key nor value is dropped, a group with no
// attributes is dropped, and a group with an empty key is inlined. The
// attributes that WithAttrs and WithGroup gave are part of every record logged
// through the handler they made, in the groups that were open when they were
// given, so Entries needs nothing else to say what a line carried. A handler
// made by WithAttrs or WithGroup keeps its records in the Logs it came from.
//
// A Logs is safe for concurrent use and must not be copied after first use.
type Logs struct {
	mu      sync.Mutex
	entries []logEntry
}

// A logEntry is a record in the form Logs keeps it: its attributes resolved and
// normalised, and nothing shared with the caller's slices.
type logEntry struct {
	record slog.Record
	attrs  []slog.Attr
}

// Logger returns a slog.Logger that logs into l.
func (l *Logs) Logger() *slog.Logger { return slog.New(l) }

// Entries returns the records kept so far, in the order they were logged. The
// slice, the records and their attributes are copies: nothing a caller does to
// them changes what l holds. A value of kind slog.KindAny is the one exception
// to the depth of the copy, since it can be any type: it is the value that was
// logged, resolved, and a caller that changes through it changes the original.
func (l *Logs) Entries() []slog.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]slog.Record, len(l.entries))
	for i, e := range l.entries {
		r := slog.NewRecord(e.record.Time, e.record.Level, e.record.Message, e.record.PC)
		r.AddAttrs(cloneAttrs(e.attrs)...)
		out[i] = r
	}
	return out
}

// Reset forgets every record kept, for a test that logs in phases and asserts on
// one phase at a time. It does not forget what a handler made by WithAttrs or
// WithGroup carries.
func (l *Logs) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
}

// Enabled is true at every level: Logs keeps everything.
func (l *Logs) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the record.
func (l *Logs) Handle(ctx context.Context, r slog.Record) error {
	return (&logsView{logs: l}).Handle(ctx, r)
}

// WithAttrs returns a handler whose records carry attrs as well, into l.
func (l *Logs) WithAttrs(attrs []slog.Attr) slog.Handler {
	return (&logsView{logs: l}).WithAttrs(attrs)
}

// WithGroup returns a handler that puts the attributes of its records, and of
// the handlers made from it, in a group called name, into l. An empty name is
// the receiver.
func (l *Logs) WithGroup(name string) slog.Handler {
	return (&logsView{logs: l}).WithGroup(name)
}

// A logsView is the handler WithAttrs and WithGroup return: the Logs it keeps
// into, and the attributes and groups it was given. frames[0] is the attributes
// given outside any group, and each later frame is a group and the attributes
// given inside it, so that the attributes of one group are never split in two.
type logsView struct {
	logs   *Logs
	frames []logFrame
}

type logFrame struct {
	group string
	attrs []slog.Attr
}

func (v *logsView) Enabled(context.Context, slog.Level) bool { return true }

func (v *logsView) WithAttrs(attrs []slog.Attr) slog.Handler {
	attrs = normalise(attrs)
	if len(attrs) == 0 {
		return v
	}
	frames := slices.Clone(v.frames)
	if len(frames) == 0 {
		frames = []logFrame{{}}
	}
	last := &frames[len(frames)-1]
	last.attrs = append(slices.Clone(last.attrs), attrs...)
	return &logsView{logs: v.logs, frames: frames}
}

func (v *logsView) WithGroup(name string) slog.Handler {
	if name == "" {
		return v
	}
	frames := slices.Clone(v.frames)
	if len(frames) == 0 {
		frames = []logFrame{{}}
	}
	frames = append(frames, logFrame{group: name})
	return &logsView{logs: v.logs, frames: frames}
}

func (v *logsView) Handle(_ context.Context, r slog.Record) error {
	var own []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		own = append(own, a)
		return true
	})
	attrs := nest(v.frames, normalise(own))

	v.logs.mu.Lock()
	defer v.logs.mu.Unlock()
	v.logs.entries = append(v.logs.entries, logEntry{
		record: slog.NewRecord(r.Time, r.Level, r.Message, r.PC),
		attrs:  attrs,
	})
	return nil
}

// nest is the attributes of a record with those of the frames around them: the
// attributes of the first frame, then the group of the second holding its own
// attributes and then the group of the third, and so on, the record's own
// attributes innermost. A group that would hold nothing is left out.
func nest(frames []logFrame, inner []slog.Attr) []slog.Attr {
	for i := len(frames) - 1; i >= 0; i-- {
		attrs := slices.Concat(frames[i].attrs, inner)
		if i > 0 {
			inner = nil
			if len(attrs) > 0 {
				inner = []slog.Attr{{Key: frames[i].group, Value: slog.GroupValue(attrs...)}}
			}
			continue
		}
		inner = attrs
	}
	return inner
}

// normalise resolves the values of attrs and applies the rules of the standard
// library's handlers: an attribute with neither key nor value is dropped, a group
// with no attributes is dropped, and a group with an empty key is replaced by its
// attributes. The result shares nothing with attrs.
func normalise(attrs []slog.Attr) []slog.Attr {
	var out []slog.Attr
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if a.Equal(slog.Attr{}) {
			continue
		}
		if a.Value.Kind() != slog.KindGroup {
			out = append(out, a)
			continue
		}
		members := normalise(a.Value.Group())
		switch {
		case len(members) == 0:
		case a.Key == "":
			out = append(out, members...)
		default:
			out = append(out, slog.Attr{Key: a.Key, Value: slog.GroupValue(members...)})
		}
	}
	return out
}

// cloneAttrs is a copy of attrs with every group copied too.
func cloneAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		if a.Value.Kind() == slog.KindGroup {
			a = slog.Attr{Key: a.Key, Value: slog.GroupValue(cloneAttrs(a.Value.Group())...)}
		}
		out[i] = a
	}
	return out
}

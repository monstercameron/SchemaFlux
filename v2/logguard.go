package schemaflux

import (
	"context"
	"log/slog"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// logGuard is the handler every Client.Logger is wrapped in, so that no line the
// library writes for a client holds the text of an error or a name a caller or a
// provider chose, whatever level it is written at and whichever part of the call
// path writes it.
//
// An attribute whose value is an error is written as a kind, the name of the
// kind the library classifies the error as (Kind.Error) and nothing else of it:
// an error's text is a provider's or a caller's, and a vendor's message quotes
// the request back. A string under the key "error" is withheld for the same
// reason. The provider, the model and the ids of a line are cut to the length
// telemetry holds them to and stripped of anything that is not printable ASCII,
// so a name cannot add a line to a log or fill one.
//
// A line that needs none of this is passed on as it is, so a call that goes well
// writes what it wrote before and allocates nothing for it.
type logGuard struct{ inner slog.Handler }

// withheld is what a string logged as an error is replaced by: the library knows
// nothing of what it was.
const withheld = "withheld"

// Enabled is the inner handler's answer.
func (g logGuard) Enabled(ctx context.Context, level slog.Level) bool {
	return g.inner.Enabled(ctx, level)
}

// Handle passes r on with its attributes guarded.
func (g logGuard) Handle(ctx context.Context, r slog.Record) error {
	changed := false
	r.Attrs(func(a slog.Attr) bool {
		_, changed = guardedAttr(a)
		return !changed
	})
	if !changed {
		return g.inner.Handle(ctx, r)
	}
	guarded := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		next, _ := guardedAttr(a)
		guarded.AddAttrs(next)
		return true
	})
	return g.inner.Handle(ctx, guarded)
}

// WithAttrs guards the attributes it is given as it guards those of a line.
func (g logGuard) WithAttrs(attrs []slog.Attr) slog.Handler {
	guarded := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		guarded[i], _ = guardedAttr(a)
	}
	return logGuard{g.inner.WithAttrs(guarded)}
}

// WithGroup opens the group on the inner handler.
func (g logGuard) WithGroup(name string) slog.Handler {
	return logGuard{g.inner.WithGroup(name)}
}

// guardedAttr returns a with what it may not carry taken out, and whether it
// changed anything.
func guardedAttr(a slog.Attr) (slog.Attr, bool) {
	switch a.Value.Kind() {
	case slog.KindGroup:
		members := a.Value.Group()
		var guarded []slog.Attr
		for i, member := range members {
			next, changed := guardedAttr(member)
			if changed && guarded == nil {
				guarded = append(make([]slog.Attr, 0, len(members)), members[:i]...)
			}
			if guarded != nil {
				guarded = append(guarded, next)
			}
		}
		if guarded == nil {
			return a, false
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(guarded...)}, true
	case slog.KindAny, slog.KindLogValuer:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			kind, _ := kindOfError(err)
			return slog.String("kind", kind.Error()), true
		}
	case slog.KindString:
		text := a.Value.String()
		switch a.Key {
		case "error", "err":
			return slog.String("kind", withheld), true
		case "provider":
			return boundedAttr(a, text, types.MaxProviderName)
		case "model":
			return boundedAttr(a, text, types.MaxModelName)
		case "requestID", "correlationID":
			return boundedAttr(a, text, types.MaxID)
		}
	}
	return a, false
}

// boundedAttr is a with its string value cut to limit, and whether it was cut.
func boundedAttr(a slog.Attr, text string, limit int) (slog.Attr, bool) {
	if bounded := types.Bound(text, limit); bounded != text {
		return slog.String(a.Key, bounded), true
	}
	return a, false
}

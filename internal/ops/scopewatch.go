package ops

import (
	"context"
	"sync/atomic"
)

// ScopeWatch tells a call helper whether the operation scope of its call was
// opened by the internal operation the call ran. A call that is refused before
// it runs one (an argument or an option no call can use) never reaches the
// place the scope is opened from, and an observer that sees every call a
// recorder sees has to see that one too: the helper then opens and ends the
// scope itself, once, with the kind the call ended with.
//
// A watch exists only for a call begun while an observer was installed, so a
// call with none pays nothing for it.
type ScopeWatch struct {
	// parent is the watch of the call this one was begun inside, when there is
	// one: a scope opened by the inner call's operation is also opened for the
	// outer call, whose own scope would otherwise be reported twice.
	parent *ScopeWatch

	// opened is set when the internal operation opened the scope of the call.
	opened atomic.Bool

	// ended is set when the helper opened and ended the scope itself.
	ended atomic.Bool
}

// scopeWatchKey is the context key of the innermost ScopeWatch.
type scopeWatchKey struct{}

// WatchScope returns a context that carries a new ScopeWatch, and the watch,
// for a call that is begun while an observer is installed. With no observer
// installed it returns the context as it is and a nil watch, and allocates
// nothing.
func WatchScope(ctx context.Context) (context.Context, *ScopeWatch) {
	if !Observing() {
		return ctx, nil
	}
	parent, _ := ctx.Value(scopeWatchKey{}).(*ScopeWatch)
	watch := &ScopeWatch{parent: parent}
	return context.WithValue(ctx, scopeWatchKey{}, watch), watch
}

// noteScopeOpened tells the watch on ctx, and the watches it was begun inside,
// that an operation scope was opened under it.
func noteScopeOpened(ctx context.Context) {
	watch, _ := ctx.Value(scopeWatchKey{}).(*ScopeWatch)
	for ; watch != nil; watch = watch.parent {
		watch.opened.Store(true)
	}
}

// EndScope opens and ends the operation scope named name with the kind of err
// when the call watched by w ran no internal operation that opened one, and
// does so once however often it is called. A nil watch, a scope the internal
// operation opened and one already ended by an earlier call do nothing.
func (w *ScopeWatch) EndScope(ctx context.Context, name string, err error) {
	if w == nil || w.opened.Load() || !Observing() || !w.ended.CompareAndSwap(false, true) {
		return
	}
	_, observed := observeOperation(ctx, name)
	observed.end(&err)
}

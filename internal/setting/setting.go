// Package setting holds the one rule the process-wide settings of version 2
// share for putting a setting back: a function that sets one returns the
// function that undoes it, and what undoing means does not depend on the order
// the undoing happens in.
//
// A setting is put in force in layers, newest on top. What is in force is the
// value of the newest layer that has not been restored, and the base value when
// there is none. Restoring a layer marks it, and the layers on top that are
// marked go; restoring a layer below one that is still in force changes
// nothing yet, and the setting falls back past it when the one above is
// restored. So a restore that runs late, or twice, or from another goroutine
// than the call that made it, can never bring back a value somebody has already
// restored, and can never take away a value set after its own.
package setting

import "sync"

// Layers is the stack of values of one setting. The zero value, with Apply and
// Base set, is ready to use and safe for concurrent use. It must not be copied
// after first use.
type Layers[T any] struct {
	// Apply puts a value in force. It is called with the lock of the layers held,
	// once for each change of what is in force, so it must neither block on
	// another goroutine nor call back into the layers.
	Apply func(T)

	// Base returns the value in force when no layer is. It is read when the first
	// layer is set, so that what was there before is what comes back, and is
	// called with the lock held.
	Base func() T

	mu   sync.Mutex
	top  *layer[T]
	base T
}

// layer is one call to Set.
type layer[T any] struct {
	value T
	below *layer[T]
	done  bool
}

// Set puts value in force and returns the function that restores it. The
// function puts back what was in force when Set was called unless something has
// been set since: that later value stays in force until it is restored as well,
// and what is then in force is the newest value not yet restored, or the base
// when there is none. Calling it again does nothing.
func (l *Layers[T]) Set(value T) (restore func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.top == nil {
		l.base = l.Base()
	}
	in := &layer[T]{value: value, below: l.top}
	l.top = in
	l.Apply(value)

	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		in.done = true
		moved := false
		for l.top != nil && l.top.done {
			l.top = l.top.below
			moved = true
		}
		if !moved {
			return
		}
		if l.top == nil {
			l.Apply(l.base)
			return
		}
		l.Apply(l.top.value)
	}
}

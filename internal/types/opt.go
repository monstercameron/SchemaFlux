package types

// Opt carries a value that may or may not have been set.
//
// It exists for the fields where zero is a legitimate answer. Every merge
// function in this library resolves caller options against operation defaults
// with a test like `if user.MinConfidence > 0`, which reads "the caller set it"
// but means "the caller set it to something other than zero". Where the default
// is 0.7 and zero means "accept everything", those are opposite instructions and
// the library silently follows the wrong one.
//
// This is the same defect as F-01 one type over: F-01 was Mode and Speed, where
// the meaningful value Strict happened to be the zero value, and it was fixed by
// renumbering. A float has no spare zero to renumber into, so it needs a
// presence bit instead.
type Opt[T comparable] struct {
	value T
	set   bool
}

// Set returns an Opt holding the value, marked as set. A zero value passed here
// is a decision and survives a merge.
func Set[T comparable](value T) Opt[T] {
	return Opt[T]{value: value, set: true}
}

// Unset returns an Opt holding nothing, which is also its zero value — so a
// struct literal that omits the field means "not set" without any ceremony.
func Unset[T comparable]() Opt[T] {
	return Opt[T]{}
}

// IsSet reports whether a value was supplied.
func (o Opt[T]) IsSet() bool { return o.set }

// Get returns the value and whether it was set.
func (o Opt[T]) Get() (T, bool) { return o.value, o.set }

// Or returns the value if one was set, and the fallback otherwise.
func (o Opt[T]) Or(fallback T) T {
	if o.set {
		return o.value
	}
	return fallback
}

// Value returns the value, or the type's zero value if none was set. Prefer Or
// or Get; this is for the cases where the zero is a fine answer and the caller
// has already decided that.
func (o Opt[T]) Value() T { return o.value }

// Merge returns the caller's value when they supplied one, and the default
// otherwise. It is the operation that every hand-written `if user.X != 0` guard
// was approximating.
func (o Opt[T]) Merge(defaults Opt[T]) Opt[T] {
	if o.set {
		return o
	}
	return defaults
}

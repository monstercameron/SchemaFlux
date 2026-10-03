package bind

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// capable is what the walks of Optional look for.
type capable interface{ Capability() string }

// link is a stand-in for a root Provider that has the capability.
type link struct {
	name string
	next any
}

func (l *link) Capability() string { return l.name }

// plainLink is a stand-in for a root Provider that wraps another and has no
// capability of its own.
type plainLink struct {
	name string
	next any
}

// unwrapLinks is the registered Unwrap of the stand-in root.
func unwrapLinks(p any) any {
	switch l := p.(type) {
	case *link:
		return l.next
	case *plainLink:
		return l.next
	}
	return nil
}

func registerLinks(t *testing.T) {
	t.Helper()
	unregister()
	t.Cleanup(unregister)
	Register(Hooks{Unwrap: unwrapLinks})
}

// chainOf builds plainLinks that wrap one another and end in last.
func chainOf(depth int, last any) any {
	p := last
	for i := range depth {
		p = &plainLink{name: fmt.Sprint("wrapper ", i), next: p}
	}
	return p
}

func TestOptionalFindsTheCapabilityAtEveryDepth(t *testing.T) {
	registerLinks(t)
	for _, depth := range []int{0, 1, 2, 3, 10, 40, 63} {
		t.Run(fmt.Sprint("at depth ", depth), func(t *testing.T) {
			bottom := &link{name: "bottom"}
			got, ok := Optional[capable](chainOf(depth, bottom))
			if !ok || got.Capability() != "bottom" {
				t.Errorf("Optional = %v, %v, want the capability at depth %d", got, ok, depth)
			}
		})
	}
}

func TestOptionalFindsTheNearestCapabilityFirst(t *testing.T) {
	registerLinks(t)
	inner := &link{name: "inner"}
	outer := &link{name: "outer", next: inner}
	middle := &plainLink{name: "m", next: outer}
	if got, ok := Optional[capable](middle); !ok || got.Capability() != "outer" {
		t.Errorf("Optional found %v, want the outer wrapper that has it before the provider under it", got)
	}
	if got, ok := Optional[capable](outer); !ok || got.Capability() != "outer" {
		t.Errorf("Optional found %v for a value that has it itself", got)
	}
}

func TestOptionalReportsNothingWhenNoLinkHasTheCapability(t *testing.T) {
	registerLinks(t)
	for name, p := range map[string]any{
		"a plain value":      &plainLink{name: "a"},
		"a chain of plain":   chainOf(5, &plainLink{name: "end"}),
		"a chain to nil":     chainOf(3, nil),
		"a string":           "provider",
		"an int":             7,
		"an empty struct":    struct{}{},
		"a chain to a value": chainOf(2, 42),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := Optional[capable](p); ok || got != nil {
				t.Errorf("Optional = %v, %v, want nothing", got, ok)
			}
		})
	}
}

func TestOptionalOfNilIsNothing(t *testing.T) {
	registerLinks(t)
	if got, ok := Optional[capable](nil); ok || got != nil {
		t.Errorf("Optional(nil) = %v, %v", got, ok)
	}
	unregister()
	if got, ok := Optional[capable](nil); ok || got != nil {
		t.Errorf("Optional(nil) before registration = %v, %v", got, ok)
	}
}

func TestOptionalEndsOnAChainThatLoops(t *testing.T) {
	registerLinks(t)
	a := &plainLink{name: "a"}
	b := &plainLink{name: "b", next: a}
	a.next = b
	done := make(chan bool, 1)
	go func() {
		_, ok := Optional[capable](a)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Error("Optional found a capability in a loop of wrappers that has none")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Optional did not end on a chain that wraps itself")
	}
	self := &plainLink{name: "self"}
	self.next = self
	if _, ok := Optional[capable](self); ok {
		t.Error("Optional found a capability in a wrapper of itself")
	}
}

func TestOptionalLooksThroughSixtyFourWrappersAndNoFurther(t *testing.T) {
	registerLinks(t)
	bottom := &link{name: "bottom"}
	if _, ok := Optional[capable](chainOf(63, bottom)); !ok {
		t.Error("a capability 63 wrappers down was not found")
	}
	if _, ok := Optional[capable](chainOf(64, bottom)); ok {
		t.Error("a capability 64 wrappers down was found: the bound is not held")
	}
	if _, ok := Optional[capable](chainOf(1000, bottom)); ok {
		t.Error("a capability 1000 wrappers down was found")
	}
}

func TestOptionalWithNoRegisteredUnwrapLooksAtTheValueAlone(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	inner := &link{name: "inner"}
	wrapper := &plainLink{name: "w", next: inner}
	if _, ok := Optional[capable](wrapper); ok {
		t.Error("before registration a wrapper was looked through")
	}
	if got, ok := Optional[capable](inner); !ok || got != capable(inner) {
		t.Errorf("a value that has the capability was not found: %v, %v", got, ok)
	}
	Register(Hooks{Convert: standInConvert})
	if _, ok := Optional[capable](wrapper); ok {
		t.Error("with no Unwrap registered a wrapper was looked through")
	}
}

func TestOptionalAsksForAnyInterfaceAndAnyType(t *testing.T) {
	registerLinks(t)
	bottom := &link{name: "bottom"}
	chain := chainOf(2, bottom)
	if _, ok := Optional[fmt.Stringer](chain); ok {
		t.Error("found a Stringer that nothing in the chain is")
	}
	if got, ok := Optional[any](chain); !ok || got != chain {
		t.Errorf("Optional[any] = %v, %v, want the value itself", got, ok)
	}
	if got, ok := Optional[*link](chain); !ok || got != bottom {
		t.Errorf("a concrete type asked for through a chain = %v, %v, want the bottom link", got, ok)
	}
	if got, ok := Optional[*link](bottom); !ok || got != bottom {
		t.Errorf("a concrete type asked for as itself = %v, %v", got, ok)
	}
}

func TestOptionalAllocatesNothing(t *testing.T) {
	registerLinks(t)
	bottom := &link{name: "bottom"}
	chain := chainOf(3, bottom)
	none := chainOf(3, &plainLink{})
	for name, run := range map[string]func(){
		"a hit at depth 3":  func() { _, _ = Optional[capable](chain) },
		"a miss at depth 4": func() { _, _ = Optional[capable](none) },
		"a hit at depth 0":  func() { _, _ = Optional[capable](bottom) },
		"nil":               func() { _, _ = Optional[capable](nil) },
	} {
		if got := testing.AllocsPerRun(100, run); got != 0 {
			t.Errorf("%s allocates %v times, want 0", name, got)
		}
	}
}

func TestOptionalFromManyGoroutinesAtOnce(t *testing.T) {
	registerLinks(t)
	chain := chainOf(5, &link{name: "bottom"})
	var wg sync.WaitGroup
	var wrong atomic.Int32
	for range 32 {
		wg.Go(func() {
			for range 200 {
				if got, ok := Optional[capable](chain); !ok || got.Capability() != "bottom" {
					wrong.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if wrong.Load() != 0 {
		t.Errorf("%d walks gave a wrong answer", wrong.Load())
	}
}

// settings stands in for the root's Options.
type settings struct{ model string }

func standInCommon(value any) (Common, error) {
	o, ok := value.(settings)
	if !ok {
		return Common{}, errors.New("not options")
	}
	if o.model == "bad" {
		return Common{}, standInConvert("", errors.New("bad model"))
	}
	return Common{Model: o.model, MaxOutputTokens: 7}, nil
}

func TestCommonOfBeforeRegistrationIsAConfigurationError(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	for name, in := range map[string]any{
		"nil":            nil,
		"a string":       "options",
		"a struct":       settings{model: "m"},
		"a pointer":      &settings{model: "m"},
		"an int":         3,
		"an empty value": struct{}{},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := CommonOf(in)
			if err == nil {
				t.Fatal("CommonOf returned no error with no root registered")
			}
			if kind := types.KindOf(err); kind != types.KindConfiguration {
				t.Errorf("kind = %v, want configuration", kind)
			}
			if got != (Common{}) {
				t.Errorf("CommonOf returned %+v beside an error", got)
			}
		})
	}
}

func TestCommonOfWithNoCommonHookIsTheSameError(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	Register(Hooks{Convert: standInConvert})
	got, err := CommonOf(settings{model: "m"})
	if err == nil || types.KindOf(err) != types.KindConfiguration || got != (Common{}) {
		t.Errorf("CommonOf = %+v, %v", got, err)
	}
}

func TestCommonOfCallsTheRegisteredHookWithTheValueItWasGiven(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	Register(Hooks{Common: standInCommon})
	got, err := CommonOf(settings{model: "gpt-6-luna"})
	if err != nil || got != (Common{Model: "gpt-6-luna", MaxOutputTokens: 7}) {
		t.Errorf("CommonOf = %+v, %v", got, err)
	}
	if _, err := CommonOf("not options"); err == nil {
		t.Error("the hook's refusal of a value that is not Options was lost")
	}
	if got, err := CommonOf(settings{model: "bad"}); err == nil || got != (Common{}) {
		t.Errorf("CommonOf of options the hook refuses = %+v, %v", got, err)
	}
}

func TestCommonOfReturnsTheHooksErrorUnchanged(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	marker := errors.New("marker")
	Register(Hooks{Common: func(any) (Common, error) { return Common{Model: "x"}, marker }})
	got, err := CommonOf(1)
	if err != marker {
		t.Errorf("the error was changed: %v", err)
	}
	if got.Model != "x" {
		t.Errorf("the Common was changed: %+v", got)
	}
}

func TestCommonOfAllocatesNothingBesideTheValueItIsGiven(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	Register(Hooks{Common: standInCommon})
	boxed := any(settings{model: "m"})
	if got := testing.AllocsPerRun(100, func() { _, _ = CommonOf(boxed) }); got != 0 {
		t.Errorf("CommonOf allocates %v times, want 0", got)
	}
	unregister()
	if got := testing.AllocsPerRun(100, func() { _, _ = CommonOf(boxed) }); got != 0 {
		t.Errorf("an unregistered CommonOf allocates %v times, want 0", got)
	}
}

func TestRegisteringADifferentCommonOrUnwrapPanics(t *testing.T) {
	first := Hooks{Convert: standInConvert, Common: standInCommon, Unwrap: unwrapLinks}
	others := map[string]Hooks{
		"another Common": {Convert: standInConvert, Common: func(any) (Common, error) { return Common{}, nil }, Unwrap: unwrapLinks},
		"no Common":      {Convert: standInConvert, Unwrap: unwrapLinks},
		"another Unwrap": {Convert: standInConvert, Common: standInCommon, Unwrap: func(any) any { return nil }},
		"no Unwrap":      {Convert: standInConvert, Common: standInCommon},
	}
	for name, other := range others {
		t.Run(name, func(t *testing.T) {
			unregister()
			t.Cleanup(unregister)
			Register(first)
			message := catchPanic(t, func() { Register(other) })
			if !strings.Contains(message, "twice") {
				t.Errorf("the panic does not say what is wrong: %q", message)
			}
			if _, err := CommonOf(settings{model: "m"}); err != nil {
				t.Error("the first registration was replaced")
			}
		})
	}
	t.Run("the same five again do nothing", func(t *testing.T) {
		unregister()
		t.Cleanup(unregister)
		Register(first)
		Register(first)
	})
}

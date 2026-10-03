package bind

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The tests that register hooks share one package-level registration, so none
// of them is parallel, and each puts the registration back when it ends.

// stubProvider is an llm.Provider the tests tell apart by its name.
type stubProvider struct{ name string }

func (p *stubProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: p.name}, nil
}
func (p *stubProvider) Name() string                               { return p.name }
func (p *stubProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *stubProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// rootProvider stands in for a root Provider: a type of its own that is not an
// llm.Provider, which is what the real one is.
type rootProvider struct{ name string }

// internalOf stands in for the root's adapter of a root Provider as an
// llm.Provider; it holds what it was made from.
type internalOf struct {
	stubProvider
	root any
}

// rootOf stands in for the root's adapter of an llm.Provider as a root
// Provider; it holds what it was made from.
type rootOf struct {
	rootProvider
	internal llm.Provider
}

// standIn returns hooks that behave like the root's adapters in the one way the
// tests need: a provider taken to the other side and back is the provider it
// was, and one adapted twice is not wrapped twice.
func standIn() Hooks {
	return Hooks{
		Convert: standInConvert,
		ToInternal: func(provider any) llm.Provider {
			switch p := provider.(type) {
			case *rootOf:
				return p.internal
			case *rootProvider:
				return &internalOf{stubProvider: stubProvider{name: p.name}, root: p}
			}
			return nil
		},
		FromInternal: func(provider llm.Provider) any {
			if p, ok := provider.(*internalOf); ok {
				return p.root
			}
			return &rootOf{rootProvider: rootProvider{name: provider.Name()}, internal: provider}
		},
	}
}

// converted is what standInConvert makes of an error.
type converted struct {
	op  string
	err error
}

func (c *converted) Error() string { return c.op + ": " + c.err.Error() }
func (c *converted) Unwrap() error { return c.err }

func standInConvert(op string, err error) error {
	if err == nil {
		return nil
	}
	return &converted{op: op, err: err}
}

func registerStandIn(t *testing.T) {
	t.Helper()
	unregister()
	t.Cleanup(unregister)
	Register(standIn())
}

func TestConvertErrorBeforeRegistrationReturnsTheErrorUnchanged(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	cases := map[string]error{
		"a plain error":      errors.New("x"),
		"a wrapped error":    fmt.Errorf("w: %w", io.ErrUnexpectedEOF),
		"an operation error": types.Newf(types.KindTimeout, "op", "slow"),
		"a cancellation":     context.Canceled,
		"a deadline":         context.DeadlineExceeded,
		"a joined error":     errors.Join(io.EOF, io.ErrClosedPipe),
		"an empty message":   errors.New(""),
		"a typed nil inside": (*types.OperationError)(nil),
		"a long message":     errors.New(strings.Repeat("m", 4096)),
		"a unicode message":  errors.New("é 日本語"),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ConvertError("op", err); got != err {
				t.Errorf("ConvertError returned %v, want the same error", got)
			}
		})
	}
	if got := ConvertError("op", nil); got != nil {
		t.Errorf("ConvertError(nil) = %v, want nil", got)
	}
}

func TestToInternalBeforeRegistrationFailsEveryCallWithAConfigurationError(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	inputs := map[string]any{
		"a string":             "provider",
		"an int":               7,
		"an empty struct":      struct{}{},
		"a typed nil pointer":  (*stubProvider)(nil),
		"an internal provider": &stubProvider{name: "x"},
		"a slice":              []int{1},
		"a map":                map[string]int{},
		"a func":               func() {},
		"a bool":               true,
		"an error":             errors.New("e"),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			p := ToInternal(in)
			if p == nil {
				t.Fatal("ToInternal returned nil for a non-nil value before registration")
			}
			if p.Name() != "unregistered" {
				t.Errorf("Name = %q, want unregistered", p.Name())
			}
			resp, err := p.Complete(context.Background(), llm.CompletionRequest{})
			if err == nil {
				t.Fatal("Complete returned no error")
			}
			if kind := types.KindOf(err); kind != types.KindConfiguration {
				t.Errorf("kind = %v, want configuration", kind)
			}
			if !reflect.DeepEqual(resp, llm.CompletionResponse{}) {
				t.Errorf("Complete returned a response beside an error: %+v", resp)
			}
			if n, _ := p.RetryPolicy(); n != 0 {
				t.Errorf("RetryPolicy retries = %d, want 0: the failure does not change with the attempt", n)
			}
			if p.EstimateCost(llm.CompletionRequest{}) != 0 {
				t.Error("EstimateCost is not zero")
			}
		})
	}
	if got := ToInternal(nil); got != nil {
		t.Errorf("ToInternal(nil) = %v, want nil", got)
	}
}

func TestFromInternalBeforeRegistrationIsNil(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	for name, p := range map[string]llm.Provider{
		"nil":       nil,
		"a stub":    &stubProvider{name: "a"},
		"another":   &stubProvider{name: "b"},
		"unnamed":   &stubProvider{},
		"adapted":   unregisteredProvider{},
		"a wrap":    &internalOf{stubProvider: stubProvider{name: "c"}},
		"a long":    &stubProvider{name: strings.Repeat("n", 500)},
		"a unicode": &stubProvider{name: "é"},
		"a space":   &stubProvider{name: " "},
		"a dash":    &stubProvider{name: "-"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := FromInternal(p); got != nil {
				t.Errorf("FromInternal = %#v, want nil", got)
			}
		})
	}
}

func TestRegisteredReportsTheRegistration(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	if Registered() {
		t.Fatal("Registered() is true before anything was registered")
	}
	Register(Hooks{})
	if !Registered() {
		t.Error("Registered() is false after Register, even of empty Hooks")
	}
	unregister()
	if Registered() {
		t.Error("Registered() is true after the registration was put back")
	}
}

func TestRegisterReturnsAnEmptyValueForAPackageLevelInitialiser(t *testing.T) {
	var initialiser = func() struct{} { return Register(Hooks{}) }
	unregister()
	t.Cleanup(unregister)
	if got := initialiser(); got != (struct{}{}) {
		t.Errorf("Register returned %v", got)
	}
}

func TestRegisteredHooksAreCalledWithWhatTheCallerPassed(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	var gotOp string
	var gotErr error
	var gotProvider any
	var gotInternal llm.Provider
	marker := errors.New("marker")
	internal := &stubProvider{name: "internal"}
	Register(Hooks{
		Convert: func(op string, err error) error { gotOp, gotErr = op, err; return marker },
		ToInternal: func(p any) llm.Provider {
			gotProvider = p
			return internal
		},
		FromInternal: func(p llm.Provider) any {
			gotInternal = p
			return "root provider"
		},
	})
	cause := errors.New("cause")
	if got := ConvertError("classify", cause); got != marker || gotOp != "classify" || gotErr != cause {
		t.Errorf("ConvertError = %v, hook saw (%q, %v)", got, gotOp, gotErr)
	}
	if got := ToInternal("p"); got != llm.Provider(internal) || gotProvider != "p" {
		t.Errorf("ToInternal = %v, hook saw %v", got, gotProvider)
	}
	if got := FromInternal(internal); got != "root provider" || gotInternal != llm.Provider(internal) {
		t.Errorf("FromInternal = %v, hook saw %v", got, gotInternal)
	}
}

func TestANilErrorAndANilProviderNeverReachTheHooks(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	var calls atomic.Int32
	Register(Hooks{
		Convert:      func(string, error) error { calls.Add(1); return errors.New("converted") },
		ToInternal:   func(any) llm.Provider { calls.Add(1); return &stubProvider{} },
		FromInternal: func(llm.Provider) any { calls.Add(1); return 1 },
	})
	if ConvertError("op", nil) != nil || ToInternal(nil) != nil || FromInternal(nil) != nil {
		t.Error("a nil in did not give a nil out")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the hooks were called %d times for nil arguments", n)
	}
}

func TestAHookLeftNilAnswersAsIfNothingWasRegisteredForIt(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	Register(Hooks{ToInternal: func(any) llm.Provider { return &stubProvider{name: "t"} }})
	cause := errors.New("x")
	if got := ConvertError("op", cause); got != cause {
		t.Errorf("ConvertError with no Convert = %v, want the error unchanged", got)
	}
	if got := FromInternal(&stubProvider{}); got != nil {
		t.Errorf("FromInternal with no FromInternal = %v, want nil", got)
	}
	if got := ToInternal("x"); got == nil || got.Name() != "t" {
		t.Errorf("ToInternal did not use its hook: %v", got)
	}

	unregister()
	Register(Hooks{Convert: standInConvert})
	if got := ToInternal("x"); got == nil || got.Name() != "unregistered" {
		t.Errorf("ToInternal with no ToInternal = %v, want the unregistered provider", got)
	}
}

func TestRoundTripThroughTheStandInAdaptersIsTheSameProvider(t *testing.T) {
	registerStandIn(t)
	builtin := &stubProvider{name: "builtin"}
	root := FromInternal(builtin)
	if root == nil {
		t.Fatal("FromInternal returned nil")
	}
	if got := ToInternal(root); got != llm.Provider(builtin) {
		t.Errorf("ToInternal(FromInternal(p)) = %v, want p itself", got)
	}
	own := &rootProvider{name: "own"}
	internal := ToInternal(own)
	if internal == nil || internal.Name() != "own" {
		t.Fatalf("ToInternal(root) = %v", internal)
	}
	if again := FromInternal(internal); again != any(own) {
		t.Errorf("FromInternal(ToInternal(r)) = %v, want r itself", again)
	}
	if ToInternal("not a provider") != nil {
		t.Error("ToInternal of a value that is not a provider is not nil")
	}
}

func TestRegisteringTheSameFunctionsAgainDoesNothing(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	h := standIn()
	Register(h)
	for range 10 {
		Register(h)
	}
	cause := errors.New("e")
	var c *converted
	if !errors.As(ConvertError("op", cause), &c) || c.op != "op" {
		t.Error("the registration changed")
	}
}

func TestRegisteringTwoEmptyHooksIsTheSameRegistration(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	Register(Hooks{})
	Register(Hooks{})
	if !Registered() {
		t.Fatal("the registration of two empty hooks was lost")
	}
	// Both registrations are the same, so a third that is not panics, and the
	// empty one stays: nothing is converted.
	err := errors.New("unchanged")
	if got := ConvertError("op", err); got != err {
		t.Errorf("an empty registration changed an error: %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("a registration of other functions did not panic after two empty ones")
		}
	}()
	Register(Hooks{Convert: standInConvert})
}

func TestRegisteringDifferentFunctionsPanicsAndKeepsTheFirst(t *testing.T) {
	first := Hooks{Convert: standInConvert}
	others := map[string]Hooks{
		"another Convert":      {Convert: func(op string, err error) error { return nil }},
		"no Convert":           {},
		"a ToInternal added":   {Convert: standInConvert, ToInternal: func(any) llm.Provider { return nil }},
		"a FromInternal added": {Convert: standInConvert, FromInternal: func(llm.Provider) any { return nil }},
		"all three":            standIn(),
	}
	for name, other := range others {
		t.Run(name, func(t *testing.T) {
			unregister()
			t.Cleanup(unregister)
			Register(first)
			message := catchPanic(t, func() { Register(other) })
			if !strings.Contains(message, "Register") || !strings.Contains(message, "twice") || !strings.Contains(message, "root") {
				t.Errorf("the panic does not say what is wrong: %q", message)
			}
			cause := errors.New("e")
			var c *converted
			if !errors.As(ConvertError("op", cause), &c) {
				t.Error("the first registration was replaced by the second")
			}
			if Registered() != true {
				t.Error("the registration was lost")
			}
		})
	}
}

// catchPanic runs f and returns the panic it raised as text, failing the test
// when it raised none.
func catchPanic(t *testing.T, f func()) (message string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Error("no panic")
			return
		}
		message = fmt.Sprint(r)
	}()
	f()
	return ""
}

func TestOnlyOneOfManyConcurrentRegistrationsOfDifferentHooksWins(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	hooks := distinctConverts()
	var wg sync.WaitGroup
	var panics atomic.Int32
	start := make(chan struct{})
	for _, h := range hooks {
		wg.Go(func() {
			<-start
			defer func() {
				if recover() != nil {
					panics.Add(1)
				}
			}()
			Register(h)
		})
	}
	close(start)
	wg.Wait()
	if got := panics.Load(); int(got) != len(hooks)-1 {
		t.Errorf("%d registrations panicked, want %d: exactly one wins", got, len(hooks)-1)
	}
	if !Registered() {
		t.Error("nothing is registered")
	}
}

func distinctConverts() []Hooks {
	return []Hooks{
		{Convert: func(string, error) error { return errors.New("0") }},
		{Convert: func(string, error) error { return errors.New("1") }},
		{Convert: func(string, error) error { return errors.New("2") }},
		{Convert: func(string, error) error { return errors.New("3") }},
		{Convert: func(string, error) error { return errors.New("4") }},
		{Convert: func(string, error) error { return errors.New("5") }},
		{Convert: func(string, error) error { return errors.New("6") }},
		{Convert: func(string, error) error { return errors.New("7") }},
	}
}

func TestConcurrentRegistrationOfTheSameHooksNeverPanics(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	h := standIn()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		wg.Go(func() {
			<-start
			Register(h)
		})
	}
	close(start)
	wg.Wait()
	if !Registered() {
		t.Error("nothing is registered")
	}
}

func TestTheAccessorsAreSafeToCallFromManyGoroutinesAfterRegistration(t *testing.T) {
	registerStandIn(t)
	builtin := &stubProvider{name: "builtin"}
	root := FromInternal(builtin)
	cause := errors.New("e")
	var wg sync.WaitGroup
	var failures atomic.Int32
	for range 64 {
		wg.Go(func() {
			for range 200 {
				if ToInternal(root) != llm.Provider(builtin) {
					failures.Add(1)
				}
				if FromInternal(builtin) == nil {
					failures.Add(1)
				}
				if ConvertError("op", cause) == nil {
					failures.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Errorf("%d calls gave a wrong answer", failures.Load())
	}
}

func TestTheAccessorsAllocateNothingThatIsNotTheirAnswer(t *testing.T) {
	registerStandIn(t)
	builtin := &stubProvider{name: "builtin"}
	root := FromInternal(builtin)
	own := &rootProvider{name: "own"}
	adapted := ToInternal(own)
	cases := []struct {
		name string
		run  func()
	}{
		{"converting a nil error", func() { _ = ConvertError("op", nil) }},
		{"adapting a nil provider", func() { _ = ToInternal(nil) }},
		{"taking back a nil provider", func() { _ = FromInternal(nil) }},
		{"adapting what was taken from a built-in provider", func() { _ = ToInternal(root) }},
		{"taking back what was adapted from a root provider", func() { _ = FromInternal(adapted) }},
		{"the registration state", func() { _ = Registered() }},
	}
	for _, c := range cases {
		if got := testing.AllocsPerRun(100, c.run); got != 0 {
			t.Errorf("%s allocates %v times, want 0", c.name, got)
		}
	}
	unregister()
	cause := errors.New("e")
	for _, c := range []struct {
		name string
		run  func()
	}{
		{"an unregistered conversion of an error", func() { _ = ConvertError("op", cause) }},
		{"an unregistered conversion of nil", func() { _ = ConvertError("op", nil) }},
		{"an unregistered adapter", func() { _ = ToInternal(builtin) }},
		{"an unregistered return adapter", func() { _ = FromInternal(builtin) }},
	} {
		if got := testing.AllocsPerRun(100, c.run); got != 0 {
			t.Errorf("%s allocates %v times, want 0", c.name, got)
		}
	}
}

package schemaflux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func TestTheRootRegisteredItsHooksBeforeAnyTestRan(t *testing.T) {
	t.Parallel()
	if !bind.Registered() {
		t.Fatal("nothing is registered with internal/bind although the root is linked")
	}
}

func TestConvertErrorOfBindIsTheRootsConvertError(t *testing.T) {
	t.Parallel()
	cases := map[string]error{
		"a plain error":               errors.New("plain"),
		"a wrapped plain error":       fmt.Errorf("w: %w", io.ErrUnexpectedEOF),
		"an HTTP 429":                 vendorError(429),
		"an HTTP 500":                 vendorError(500),
		"a rate limit with a wait":    &llm.RateLimitError{APIError: vendorError(429), RetryAfter: time.Second},
		"an internal timeout":         types.Newf(types.KindTimeout, "x", "slow"),
		"an internal configuration":   types.Newf(types.KindConfiguration, "x", "bad"),
		"a cancellation":              context.Canceled,
		"a deadline":                  context.DeadlineExceeded,
		"the root's error":            &Error{Kind: ErrUnavailable, Provider: "p", Message: "down"},
		"the root's error, wrapped":   fmt.Errorf("w: %w", &Error{Kind: ErrBusy, Message: "busy"}),
		"a kind used as an error":     ErrRateLimited,
		"a joined error":              errors.Join(io.EOF, io.ErrClosedPipe),
		"an error with an empty text": errors.New(""),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := bind.ConvertError("extract", err)
			want := convertError("extract", err)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("bind.ConvertError = %#v, want what convertError gives, %#v", got, want)
			}
			var e *Error
			if !errors.As(got, &e) {
				t.Errorf("bind.ConvertError returned %T, want an *Error", got)
			}
		})
	}
	if got := bind.ConvertError("extract", nil); got != nil {
		t.Errorf("bind.ConvertError(nil) = %v, want nil", got)
	}
}

func TestAProviderOfTheRootGoesToTheCallPathAndComesBackTheSameProvider(t *testing.T) {
	t.Parallel()
	own := &scriptedProvider{name: "custom", complete: func(context.Context, Request) (Response, error) {
		return Response{Content: "answer"}, nil
	}}
	internal := bind.ToInternal(own)
	if internal == nil {
		t.Fatal("bind.ToInternal returned nil for a Provider")
	}
	if internal.Name() != "custom" {
		t.Errorf("the adapted provider is named %q", internal.Name())
	}
	resp, err := internal.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil || resp.Content != "answer" {
		t.Errorf("Complete through the adapter = %+v, %v", resp, err)
	}
	if back := bind.FromInternal(internal); back != any(own) {
		t.Errorf("bind.FromInternal(bind.ToInternal(p)) = %v, want p itself", back)
	}
}

func TestABuiltInProviderGoesToTheRootAndComesBackTheSameLLMProvider(t *testing.T) {
	t.Parallel()
	builtin := &scriptedInternalProvider{name: "builtin", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: "answer"}, nil
	}}
	root, ok := bind.FromInternal(builtin).(Provider)
	if !ok {
		t.Fatalf("bind.FromInternal returned %T, which is not a Provider", bind.FromInternal(builtin))
	}
	if root.Name() != "builtin" {
		t.Errorf("the adapted provider is named %q", root.Name())
	}
	resp, err := root.Complete(context.Background(), Request{})
	if err != nil || resp.Content != "answer" {
		t.Errorf("Complete through the adapter = %+v, %v", resp, err)
	}
	if back := bind.ToInternal(root); back != llm.Provider(builtin) {
		t.Errorf("bind.ToInternal(bind.FromInternal(p)) = %v, want p itself", back)
	}
}

func TestBindAdaptersGiveNilForWhatIsNotAProvider(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]any{
		"nil":            nil,
		"a string":       "provider",
		"an int":         3,
		"a struct":       struct{ Name string }{"x"},
		"a slice":        []Provider{},
		"an llm one":     &scriptedInternalProvider{name: "x"},
		"a func":         func() {},
		"a map":          map[string]Provider{},
		"an error":       errors.New("x"),
		"a bool":         false,
		"a nil Provider": Provider(nil),
	} {
		t.Run(name, func(t *testing.T) {
			if got := bind.ToInternal(in); got != nil {
				t.Errorf("bind.ToInternal(%v) = %v, want nil", name, got)
			}
		})
	}
	if got := bind.FromInternal(nil); got != nil {
		t.Errorf("bind.FromInternal(nil) = %v, want nil", got)
	}
}

func TestBindAdaptersAllocateNothingOnAProviderThatIsAlreadyAdapted(t *testing.T) {
	// Not parallel: AllocsPerRun reads the whole process's allocation count.
	own := &scriptedProvider{name: "custom", complete: func(context.Context, Request) (Response, error) { return Response{}, nil }}
	builtin := &scriptedInternalProvider{name: "builtin"}
	adaptedOwn := bind.ToInternal(own)
	adaptedBuiltin := bind.FromInternal(builtin)
	for name, run := range map[string]func(){
		"converting a nil error":                       func() { _ = bind.ConvertError("op", nil) },
		"taking a root Provider back from its adapter": func() { _ = bind.FromInternal(adaptedOwn) },
		"taking a built-in back from its adapter":      func() { _ = bind.ToInternal(adaptedBuiltin) },
		"adapting nothing":                             func() { _ = bind.ToInternal(nil) },
		"adapting nothing, the other way":              func() { _ = bind.FromInternal(nil) },
	} {
		if got := testing.AllocsPerRun(100, run); got != 0 {
			t.Errorf("%s allocates %v times, want 0", name, got)
		}
	}
}

func TestRegisteringTheRootsHooksAgainIsDefinedAndRegisteringOthersPanics(t *testing.T) {
	t.Parallel()
	t.Run("the same functions again do nothing", func(t *testing.T) {
		bind.Register(rootHooks)
		bind.Register(rootHooks)
		if !bind.Registered() {
			t.Error("the registration was lost")
		}
	})
	// Every hook, one at a time, replaced by a function of its own: each is part
	// of what makes two registrations the same.
	hooksType := reflect.TypeOf(rootHooks)
	for i := range hooksType.NumField() {
		field := hooksType.Field(i)
		other := rootHooks
		slot := reflect.ValueOf(&other).Elem().Field(i)
		if field.Type.Kind() == reflect.Func {
			slot.Set(reflect.MakeFunc(field.Type, func([]reflect.Value) []reflect.Value {
				results := make([]reflect.Value, field.Type.NumOut())
				for r := range results {
					results[r] = reflect.Zero(field.Type.Out(r))
				}
				return results
			}))
		} else {
			slot.Set(reflect.ValueOf(func() {}))
		}
		t.Run("another "+field.Name+" panics", func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("Register of other functions did not panic")
				}
				if !strings.Contains(fmt.Sprint(r), "twice") {
					t.Errorf("the panic does not say what is wrong: %v", r)
				}
				if !reflect.DeepEqual(bind.ConvertError("op", errors.New("x")), convertError("op", errors.New("x"))) {
					t.Error("the first registration was replaced")
				}
			}()
			bind.Register(other)
		})
	}
	t.Run("a registration with a hook missing panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("Register of fewer functions did not panic")
			}
		}()
		bind.Register(bind.Hooks{Convert: convertError, ToInternal: toInternalProvider, FromInternal: fromInternalProvider})
	})
	t.Run("nothing panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("Register of nothing did not panic")
			}
		}()
		bind.Register(bind.Hooks{})
	})
}

func TestOptionsCommonChecksAsOptionsCheckDoes(t *testing.T) {
	t.Parallel()
	cases := map[string]Options{
		"the zero Options":      {},
		"every field valid":     {Model: "m", Tier: TierSmart, Effort: EffortHigh, Mode: ModeStrict, Steering: "s", MaxOutputTokens: 4, WebSearch: true},
		"a negative ceiling":    {MaxOutputTokens: -1},
		"a ceiling of one":      {MaxOutputTokens: 1},
		"a tier past the last":  {Tier: TierQuick + 1},
		"a negative tier":       {Tier: -1},
		"a mode past the last":  {Mode: ModeCreative + 1},
		"a negative mode":       {Mode: -2},
		"the quick tier":        {Tier: TierQuick},
		"the creative mode":     {Mode: ModeCreative},
		"an effort of any word": {Effort: "whatever"},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := o.check("classify")
			got := o.common().Check("classify")
			if (want == nil) != (got == nil) {
				t.Fatalf("Options.check = %v, bind.Common.Check = %v: one refuses and the other does not", want, got)
			}
			if want == nil {
				return
			}
			var w, g *Error
			if !errors.As(want, &w) || !errors.As(got, &g) {
				t.Fatalf("the errors are %T and %T, want *Error both", want, got)
			}
			if g.Kind != w.Kind || g.Op != w.Op || g.Message != w.Message {
				t.Errorf("bind.Common.Check = {%v %q %q}, Options.check = {%v %q %q}", g.Kind, g.Op, g.Message, w.Kind, w.Op, w.Message)
			}
			if g.Kind != ErrConfiguration {
				t.Errorf("the kind is %v, want ErrConfiguration", g.Kind)
			}
		})
	}
}

// tunedOptions is what a subpackage's options type looks like: the root's
// Options embedded, and settings of the operation's own beside it.
type tunedOptions struct {
	Options
	MaxWords int
}

func TestCommonOfReadsAValueThatEmbedsOptions(t *testing.T) {
	t.Parallel()
	cases := map[string]Options{
		"the zero Options":     {},
		"every field set":      {Model: "m", Tier: TierSmart, Effort: EffortHigh, Mode: ModeStrict, Steering: "s", MaxOutputTokens: 9, WebSearch: true},
		"a model only":         {Model: "gpt-6-luna"},
		"a tier only":          {Tier: TierQuick},
		"a mode only":          {Mode: ModeCreative},
		"an effort only":       {Effort: EffortMax},
		"steering only":        {Steering: "plain words"},
		"a ceiling only":       {MaxOutputTokens: 1},
		"web search only":      {WebSearch: true},
		"the fast tier, mixed": {Tier: TierFast, Mode: ModeTransform, MaxOutputTokens: 100},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tuned := tunedOptions{Options: o, MaxWords: 50}
			got, err := bind.CommonOf(tuned.Options)
			if err != nil {
				t.Fatalf("CommonOf: %v", err)
			}
			if got != o.common() {
				t.Errorf("CommonOf = %+v, want %+v", got, o.common())
			}
			pointer, err := bind.CommonOf(&tuned.Options)
			if err != nil || pointer != got {
				t.Errorf("CommonOf of a pointer = %+v, %v", pointer, err)
			}
		})
	}
	t.Run("a nil pointer is the zero Options", func(t *testing.T) {
		t.Parallel()
		var none *Options
		got, err := bind.CommonOf(none)
		if err != nil || got != (bind.Common{}) {
			t.Errorf("CommonOf(nil *Options) = %+v, %v", got, err)
		}
	})
}

func TestCommonOfRefusesWhatCheckRefusesAsTheRootsError(t *testing.T) {
	t.Parallel()
	cases := map[string]Options{
		"a negative ceiling":    {MaxOutputTokens: -1},
		"a large negative":      {MaxOutputTokens: -1 << 30},
		"a tier past the last":  {Tier: TierQuick + 1},
		"a negative tier":       {Tier: -1},
		"a mode past the last":  {Mode: ModeCreative + 1},
		"a negative mode":       {Mode: -2},
		"bad and good together": {Model: "m", Tier: TierSmart, MaxOutputTokens: -7},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tuned := tunedOptions{Options: o}
			got, err := bind.CommonOf(tuned.Options)
			if got != (bind.Common{}) {
				t.Errorf("CommonOf returned %+v beside an error", got)
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("the error is %T (%v), want the root's *Error", err, err)
			}
			if e.Kind != ErrConfiguration || e.Op != "" {
				t.Errorf("error = kind %v op %q, want ErrConfiguration with the operation left empty", e.Kind, e.Op)
			}
			want := o.check("")
			if want == nil || e.Message != want.(*Error).Message {
				t.Errorf("message %q, want the one Options.check gives: %v", e.Message, want)
			}
			// The operation function names its own operation by passing the error
			// through ConvertError.
			named := bind.ConvertError("summarize", err)
			var n *Error
			if !errors.As(named, &n) || n.Op != "summarize" || n.Kind != ErrConfiguration {
				t.Errorf("converted = %v", named)
			}
		})
	}
	t.Run("a value that is not an Options is an internal error", func(t *testing.T) {
		t.Parallel()
		for _, in := range []any{nil, 3, "options", tunedOptions{}, struct{ Model string }{}} {
			_, err := bind.CommonOf(in)
			var e *Error
			if !errors.As(err, &e) || e.Kind != ErrInternal {
				t.Errorf("CommonOf(%T) = %v, want ErrInternal", in, err)
			}
		}
	})
}

func TestTheCallHelperRefusesAndConvertsWithTheRootsError(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := bind.Begin(nilContext, "classify")
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrInvalidRequest || e.Op != "classify" || e.Message != "the context is nil" {
		t.Errorf("Begin(nil) = %#v, want ErrInvalidRequest, op classify, the context is nil", err)
	}

	refused := bind.Refuse("flow.vote", types.KindInvalidRequest, "%d of %d", 2, 5)
	if !errors.As(refused, &e) || e.Kind != ErrInvalidRequest || e.Op != "flow.vote" || e.Message != "2 of 5" {
		t.Errorf("Refuse = %#v", refused)
	}

	call, err := bind.Begin(context.Background(), "is")
	if err != nil {
		t.Fatal(err)
	}
	ended := call.End(nil, types.Newf(types.KindTimeout, "is", "the call took too long"))
	if !errors.As(ended, &e) || e.Kind != ErrTimeout || e.Op != "is" {
		t.Errorf("End = %#v, want ErrTimeout for is", ended)
	}
	if again := bind.ConvertError("is", ended); again != ended {
		t.Errorf("converting a converted error gave another error: %v", again)
	}

	// A snapshot's own error (a client's bad setting) comes from the call path
	// naming the client; ending the call names the operation.
	snapshot := call.End(nil, types.Newf(types.KindConfiguration, "client", "Client.Timeout must not be negative"))
	if !errors.As(snapshot, &e) || e.Kind != ErrConfiguration || e.Op != "is" {
		t.Errorf("a snapshot's error ended with the call = %#v, want ErrConfiguration for is", snapshot)
	}
}

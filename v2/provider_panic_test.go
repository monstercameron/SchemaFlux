package schemaflux

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// panicValue is a type whose name the error may carry and whose value it may not.
type panicValue struct{ text string }

func (p panicValue) String() string { return p.text }

// panicking is a provider that panics with a value in each of the places the
// library calls it.
type panicking struct{ thrown any }

func (p panicking) Name() string { return "panicker" }

func (p panicking) ModelFor(Tier) string { return "panic-model" }

func (p panicking) Complete(context.Context, Request) (Response, error) { panic(p.thrown) }

func (p panicking) Warm(context.Context) error { panic(p.thrown) }

func (p panicking) Stream(context.Context, Request) iter.Seq2[Chunk, error] {
	return func(func(Chunk, error) bool) { panic(p.thrown) }
}

var panicValues = []struct {
	name   string
	thrown any
	typed  string
}{
	{"a string", "boom " + leakCanary, "string"},
	{"an error", errors.New("boom " + leakCanary), "*errors.errorString"},
	{"a runtime error", func() (thrown any) {
		defer func() { thrown = recover() }()
		var empty []int
		_ = empty[3]
		return nil
	}(), "runtime.boundsError"},
	{"a struct", panicValue{"boom " + leakCanary}, "schemaflux.panicValue"},
	{"an integer", 42, "int"},
	{"an Error of the library", &Error{Kind: ErrRateLimited, Message: "boom " + leakCanary}, "*schemaflux.Error"},
	{"a Kind", ErrRateLimited, "schemaflux.Kind"},
	{"a nil panic", nil, "*runtime.PanicNilError"},
	{"a pointer", &panicValue{leakCanary}, "*schemaflux.panicValue"},
	{"a slice", []string{leakCanary}, "[]string"},
}

const leakCanary = "CANARY_PANIC_91"

func TestAPanicInAProviderIsAnInternalErrorOnComplete(t *testing.T) {
	t.Parallel()
	for _, value := range panicValues {
		t.Run(value.name, func(t *testing.T) {
			t.Parallel()
			ctx := WithClient(context.Background(), Client{Provider: panicking{value.thrown}, Model: "m", Retries: -1})
			var got limitsInvoice
			var err error
			func() {
				defer func() {
					if thrown := recover(); thrown != nil {
						t.Fatalf("the panic crossed the API: %v", thrown)
					}
				}()
				got, err = Extract[limitsInvoice](ctx, "invoice")
			}()
			if got != (limitsInvoice{}) {
				t.Errorf("a value %+v came beside the error", got)
			}
			var e *Error
			if !errors.As(err, &e) || e.Kind != ErrInternal {
				t.Fatalf("got %v, want an error of kind ErrInternal", err)
			}
			if !errors.Is(err, ErrInternal) {
				t.Errorf("errors.Is does not find ErrInternal in %v", err)
			}
			for _, surface := range []string{err.Error(), e.Detail()} {
				if strings.Contains(surface, leakCanary) {
					t.Errorf("the thrown value is in %q", surface)
				}
			}
			if value.typed != "" && !strings.Contains(err.Error(), value.typed) {
				t.Errorf("the error %q does not name the type %s", err.Error(), value.typed)
			}
			if e.Provider != "panicker" {
				t.Errorf("the error does not name the provider: %q", e.Provider)
			}
		})
	}
}

func TestAPanicInAProvidersStreamIsAnInternalError(t *testing.T) {
	t.Parallel()
	for _, value := range panicValues {
		t.Run(value.name, func(t *testing.T) {
			t.Parallel()
			internal := toInternalProvider(panicking{value.thrown}).(llm.StreamingProvider)
			var chunks int
			var err error
			func() {
				defer func() {
					if thrown := recover(); thrown != nil {
						t.Fatalf("the panic crossed the API: %v", thrown)
					}
				}()
				for _, streamErr := range internal.CompleteStream(context.Background(), llm.CompletionRequest{Model: "m"}) {
					if streamErr != nil {
						err = streamErr
						break
					}
					chunks++
				}
			}()
			if chunks != 0 {
				t.Errorf("%d chunks came before the failure", chunks)
			}
			var e *Error
			if !errors.As(convertError("summarize", err), &e) || e.Kind != ErrInternal {
				t.Fatalf("got %v, want an error of kind ErrInternal", err)
			}
			if strings.Contains(err.Error(), leakCanary) {
				t.Errorf("the thrown value is in %q", err.Error())
			}
		})
	}
}

// consumerPanics is a provider whose stream is fine and a consumer that panics
// in its loop body: that panic is not the provider's and is not turned into an
// error.
func TestAPanicInTheConsumerOfAStreamIsNotRecovered(t *testing.T) {
	t.Parallel()
	good := streamingAnswer{}
	internal := toInternalProvider(good).(llm.StreamingProvider)
	defer func() {
		if thrown := recover(); thrown != "consumer" {
			t.Errorf("recovered %v, want the consumer's own panic", thrown)
		}
	}()
	for range internal.CompleteStream(context.Background(), llm.CompletionRequest{Model: "m"}) {
		panic("consumer")
	}
}

type streamingAnswer struct{}

func (streamingAnswer) Name() string { return "streamer" }

func (streamingAnswer) Complete(context.Context, Request) (Response, error) {
	return Response{Content: "x"}, nil
}

func (streamingAnswer) Stream(context.Context, Request) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		yield(Chunk{Delta: "x"}, nil)
		yield(Chunk{Done: true, Response: Response{Content: "x"}}, nil)
	}
}

func TestAPanicInAProvidersWarmIsAnInternalError(t *testing.T) {
	t.Parallel()
	for _, value := range panicValues {
		t.Run(value.name, func(t *testing.T) {
			t.Parallel()
			err := Client{Provider: panicking{value.thrown}, Model: "m"}.Warm(context.Background())
			var e *Error
			if !errors.As(err, &e) || e.Kind != ErrInternal {
				t.Fatalf("got %v, want an error of kind ErrInternal", err)
			}
			if strings.Contains(err.Error(), leakCanary) {
				t.Errorf("the thrown value is in %q", err.Error())
			}
		})
	}
}

// completing is a provider with nothing to record: the path through the adapter
// that the recover is on, with the work around it as small as it can be.
type completing struct{}

func (completing) Name() string { return "completing" }

func (completing) Complete(context.Context, Request) (Response, error) {
	return Response{Content: "x", FinishReason: "stop"}, nil
}

// TestTheRecoverInTheAdapterAddsNoAllocationToACallThatDoesNotPanic is not
// parallel: AllocsPerRun counts every allocation of the process.
func TestTheRecoverInTheAdapterAddsNoAllocationToACallThatDoesNotPanic(t *testing.T) {
	internal := toInternalProvider(completing{})
	ctx := context.Background()
	req := llm.CompletionRequest{Model: "m", UserPrompt: "u"}
	allocs := testing.AllocsPerRun(200, func() {
		if _, err := internal.Complete(ctx, req); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("a call through the adapter allocates %v times, want 0", allocs)
	}
}

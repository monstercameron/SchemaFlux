package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// LX-061. The root RedactLLM, Complete and CompleteField passed
// context.Background() to the operation, so the context a caller put in the
// options reached nothing. That is more than a missing deadline: Client.Context
// installs the client's provider on the context, so on these three functions a
// client's provider was discarded and the call went to whichever client had
// been built last. The fluent builders were fixed for this and the root
// functions were not.

type lx061Draft struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// rootContextOps are the three functions, each called with the context it is
// given placed where a caller would put it: in the options.
var rootContextOps = []struct {
	name string
	// reply is a response the operation accepts.
	reply string
	call  func(ctx context.Context) error
}{
	{
		"RedactLLM", `{"spans":[{"text":"jane@example.com","start":8,"category":"email"}]}`,
		func(ctx context.Context) error {
			opts := schemaflux.NewRedactLLMOptions()
			opts.Context = ctx
			_, err := schemaflux.RedactLLM("Contact jane@example.com for support.", opts)
			return err
		},
	},
	{
		"Complete", `the index had been rebuilt beforehand.`,
		func(ctx context.Context) error {
			opts := schemaflux.NewCompleteOptions()
			opts.Context = ctx
			_, err := schemaflux.Complete("The migration succeeded because", opts)
			return err
		},
	},
	{
		"CompleteField", `the cache was cold.`,
		func(ctx context.Context) error {
			opts := schemaflux.NewCompleteFieldOptions("Body")
			opts.Context = ctx
			_, err := schemaflux.CompleteField(lx061Draft{Title: "Postmortem", Body: "The incident started when"}, opts)
			return err
		},
	},
}

// The client's provider answers, not the last client that happened to be built.
func TestRootFunctionsUseTheClientOnTheCallersContext(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	for _, op := range rootContextOps {
		t.Run(op.name, func(t *testing.T) {
			mine := testfixtures.NewScripted(op.reply)
			other := testfixtures.NewScripted(op.reply)

			client := schemaflux.NewClient("").WithProviderInstance(mine)
			// Built second, so it is the process-wide default.
			_ = schemaflux.NewClient("").WithProviderInstance(other)

			if err := op.call(client.Context(context.Background())); err != nil {
				t.Fatalf("%s: %v", op.name, err)
			}
			if len(mine.Requests()) != 1 {
				t.Errorf("the client on the context received %d requests, want 1", len(mine.Requests()))
			}
			if len(other.Requests()) != 0 {
				t.Errorf("another client's provider received %d requests; the context's client was discarded", len(other.Requests()))
			}
		})
	}
}

// A context that is already over stops the call before the provider is reached.
func TestRootFunctionsHonourACancelledOrExpiredContext(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	expired := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		_ = cancel
		return ctx
	}

	for _, op := range rootContextOps {
		for _, state := range []struct {
			name string
			ctx  func() context.Context
			// kind is this library's classification, cause the context's own
			// error: a caller may test for either.
			kind, cause error
		}{
			{"cancelled", cancelled, schemaflux.ErrCanceled, context.Canceled},
			{"expired", expired, schemaflux.ErrTimeout, context.DeadlineExceeded},
		} {
			t.Run(op.name+"/"+state.name, func(t *testing.T) {
				provider := testfixtures.NewScripted(op.reply)
				client := schemaflux.NewClient("").WithProviderInstance(provider)

				err := op.call(client.Context(state.ctx()))
				if err == nil {
					t.Fatalf("%s completed on a context that was already %s", op.name, state.name)
				}
				if !errors.Is(err, state.kind) || !errors.Is(err, state.cause) {
					t.Errorf("err = %v, want one that is both %v and %v", err, state.kind, state.cause)
				}
				if got := len(provider.Requests()); got != 0 {
					t.Errorf("the provider was called %d time(s) on a context that was already %s", got, state.name)
				}
			})
		}
	}
}

// A caller who sets no context is unaffected: the call runs on the default
// client, as it always did. This passed before the fix too. It is here so the
// fix cannot break the caller who never set a context.
func TestRootFunctionsStillRunWithNoContext(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	for _, op := range rootContextOps {
		t.Run(op.name, func(t *testing.T) {
			provider := testfixtures.NewScripted(op.reply)
			_ = schemaflux.NewClient("").WithProviderInstance(provider)

			if err := op.call(nil); err != nil {
				t.Fatalf("%s with no context: %v", op.name, err)
			}
			if len(provider.Requests()) != 1 {
				t.Errorf("the default provider received %d requests, want 1", len(provider.Requests()))
			}
		})
	}
}

// The streaming wrappers are the other way a call reaches a provider, and they
// resolved the provider from the process alone: a stream started on one
// client's context was served by whichever client had been built last.
func TestRootStreamsUseTheClientOnTheCallersContext(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	mine := schemaflux.NewClient("").WithProviderInstance(&fakeStreamingProvider{deltas: []string{"from ", "my client"}})
	// Built second, so it is the process-wide default.
	_ = schemaflux.NewClient("").WithProviderInstance(&fakeStreamingProvider{deltas: []string{"from the other client"}})

	opts := schemaflux.NewSummarizeOptions()
	opts.OpOptions.Context = mine.Context(context.Background())
	stream, err := schemaflux.StreamSummarize("a long report", opts)
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	if got := drainStream(t, stream); got != "from my client" {
		t.Errorf("the stream came %q, want it from the client on the context", got)
	}

	// And a context that is already over opens no stream.
	cancelled, cancel := context.WithCancel(mine.Context(context.Background()))
	cancel()
	opts.OpOptions.Context = cancelled
	stream, err = schemaflux.StreamSummarize("a long report", opts)
	if stream != nil || !errors.Is(err, schemaflux.ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Errorf("stream opened = %v, err = %v; want no stream and a cancellation", stream != nil, err)
	}
}

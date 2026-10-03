package ops

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// LX-061. contextOver is what keeps a call on a finished context from reaching
// a provider. Each case names the context, whether it is over, and how the
// refusal is classified.
func TestContextOver(t *testing.T) {
	background := context.Background()
	cancelled := func(parent context.Context) context.Context {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx
	}
	expired := func(parent context.Context) context.Context {
		ctx, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}
	live := func(parent context.Context) context.Context {
		ctx, cancel := context.WithTimeout(parent, time.Hour)
		t.Cleanup(cancel)
		return ctx
	}
	type key struct{}

	cases := []struct {
		name string
		ctx  context.Context
		kind types.ErrorKind // KindUnknown means the context is live
		is   error
	}{
		{"no context", nil, types.KindUnknown, nil},
		{"background", background, types.KindUnknown, nil},
		{"a deadline an hour away", live(background), types.KindUnknown, nil},
		{"a value on a live context", context.WithValue(background, key{}, 1), types.KindUnknown, nil},
		{"cancelled", cancelled(background), types.KindCanceled, context.Canceled},
		{"past its deadline", expired(background), types.KindTimeout, context.DeadlineExceeded},
		{"a zero timeout", func() context.Context {
			ctx, cancel := context.WithTimeout(background, 0)
			t.Cleanup(cancel)
			return ctx
		}(), types.KindTimeout, context.DeadlineExceeded},
		{"a live child of a cancelled parent", live(cancelled(background)), types.KindCanceled, context.Canceled},
		{"a child of an expired parent", context.WithValue(expired(background), key{}, 1), types.KindTimeout, context.DeadlineExceeded},
		{"cancelled before its deadline", cancelled(live(background)), types.KindCanceled, context.Canceled},
		{"the client's provider on a cancelled context", WithProvider(cancelled(background), &captureProvider{}), types.KindCanceled, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := contextOver(tc.ctx)
			if tc.kind == types.KindUnknown {
				if err != nil {
					t.Fatalf("a live context was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a finished context was accepted")
			}
			if got := types.KindOf(err); got != tc.kind {
				t.Errorf("kind = %v, want %v", got, tc.kind)
			}
			if !errors.Is(err, tc.is) {
				t.Errorf("errors.Is(err, %v) is false: %v", tc.is, err)
			}
		})
	}
}

// A call on a finished context sends nothing, on every way into the provider.
func TestNothingIsSentOnAFinishedContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, release := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer release()

	for _, state := range []struct {
		name string
		ctx  context.Context
		kind types.ErrorKind
		is   error
	}{
		{"cancelled", cancelled, types.KindCanceled, context.Canceled},
		{"expired", expired, types.KindTimeout, context.DeadlineExceeded},
	} {
		check := func(t *testing.T, err error, attempts int) {
			t.Helper()
			if types.KindOf(err) != state.kind || !errors.Is(err, state.is) {
				t.Errorf("err = %v (kind %v), want kind %v wrapping %v", err, types.KindOf(err), state.kind, state.is)
			}
			if attempts != 0 {
				t.Errorf("the provider was called %d time(s)", attempts)
			}
		}

		t.Run(state.name+"/CallLLM", func(t *testing.T) {
			provider := &captureProvider{}
			_, err := CallLLM(state.ctx, provider, "system", "user", types.OpOptions{})
			check(t, err, provider.attempts)
		})

		t.Run(state.name+"/the provider on the context", func(t *testing.T) {
			provider := &captureProvider{}
			_, err := callLLM(WithProvider(state.ctx, provider), "system", "user", types.OpOptions{})
			check(t, err, provider.attempts)
		})

		t.Run(state.name+"/the test caller hook", func(t *testing.T) {
			calls := 0
			previousCaller, previousProvider := currentHooks()
			setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
				calls++
				return "{}", nil
			})
			t.Cleanup(func() {
				setLLMCaller(previousCaller)
				SetDefaultProvider(previousProvider)
			})
			_, err := callLLM(state.ctx, "system", "user", types.OpOptions{})
			check(t, err, calls)
		})

		t.Run(state.name+"/an operation", func(t *testing.T) {
			provider := &captureProvider{}
			opts := NewCompleteOptions()
			_, err := Complete(WithProvider(state.ctx, provider), "The migration succeeded because", opts)
			check(t, err, provider.attempts)
		})

		t.Run(state.name+"/a stream", func(t *testing.T) {
			started := false
			provider := &namedStreamProvider{name: "local", seq: func(func(llm.StreamChunk, error) bool) { started = true }}
			withStreamingProvider(t, provider)

			opts := NewSummarizeOptions()
			opts.OpOptions.Context = state.ctx
			stream, err := StreamSummarize("some input", opts)
			if stream != nil {
				t.Error("a stream was opened on a finished context")
			}
			attempts := 0
			if started {
				attempts = 1
			}
			check(t, err, attempts)
		})
	}
}

// cancelOnFirstFailure fails its first attempt with a retryable error and
// cancels the caller's context while doing so, which is a caller giving up
// while the request is in flight.
type cancelOnFirstFailure struct {
	captureProvider
	cancel context.CancelFunc
}

func (p *cancelOnFirstFailure) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.attempts++
	if p.attempts == 1 {
		p.cancel()
		return llm.CompletionResponse{}, &types.OperationError{Kind: types.KindRateLimited, Message: "slow down"}
	}
	return llm.CompletionResponse{Content: `{"ok":true}`}, nil
}

func (p *cancelOnFirstFailure) RetryPolicy() (int, time.Duration) { return 3, time.Nanosecond }

// A context that ends between two attempts ends the call: the retry is not
// sent, and the error says cancelled, classified, not "rate limited".
func TestNoRetryIsSentAfterTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelOnFirstFailure{cancel: cancel}

	_, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{})

	if provider.attempts != 1 {
		t.Errorf("the provider was called %d times, want 1", provider.attempts)
	}
	if types.KindOf(err) != types.KindCanceled || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v (kind %v), want a classified cancellation", err, types.KindOf(err))
	}
}

// Complete applied the default deadline only when it was handed a nil context,
// so a caller who passed one, which is every call through a Client, ran with
// no timeout.
func TestCompleteAppliesTheDefaultDeadline(t *testing.T) {
	seen := func(t *testing.T, ctx context.Context) (time.Time, bool) {
		t.Helper()
		var deadline time.Time
		var bounded bool
		previousCaller, previousProvider := currentHooks()
		setLLMCaller(func(callCtx context.Context, _, _ string, _ types.OpOptions) (string, error) {
			deadline, bounded = callCtx.Deadline()
			return "the index had been rebuilt.", nil
		})
		t.Cleanup(func() {
			setLLMCaller(previousCaller)
			SetDefaultProvider(previousProvider)
		})
		if _, err := Complete(ctx, "The migration succeeded because", NewCompleteOptions()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		return deadline, bounded
	}

	t.Run("a context with no deadline", func(t *testing.T) {
		if _, bounded := seen(t, context.Background()); !bounded {
			t.Error("the call ran with no deadline")
		}
	})
	t.Run("the caller's own deadline is kept", func(t *testing.T) {
		want := time.Now().Add(5 * time.Minute)
		ctx, cancel := context.WithDeadline(context.Background(), want)
		defer cancel()
		got, bounded := seen(t, ctx)
		if !bounded || !got.Equal(want) {
			t.Errorf("deadline = %v (bounded %v), want the caller's %v", got, bounded, want)
		}
	})
}

type refusingBudget struct{ checked int }

func (b *refusingBudget) Check() error {
	b.checked++
	return errors.New("this client's budget is spent")
}
func (b *refusingBudget) Record(float64, bool) {}

// The streaming path resolved its provider, model and budget from the process
// alone. A client's provider on the context was ignored, so a stream went to
// whichever client had been built last: LX-061 on the one path the first fix
// did not touch.
func TestStreamUsesTheCallersClient(t *testing.T) {
	text := func(s string) func(func(llm.StreamChunk, error) bool) {
		return func(yield func(llm.StreamChunk, error) bool) {
			if !yield(llm.StreamChunk{Delta: s}, nil) {
				return
			}
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: s, FinishReason: "stop"}}, nil)
		}
	}
	collect := func(t *testing.T, stream *TextStream) string {
		t.Helper()
		var out string
		for chunk, err := range stream.All() {
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			out += chunk
		}
		return out
	}

	t.Run("a provider attached with WithProvider", func(t *testing.T) {
		withStreamingProvider(t, &namedStreamProvider{name: "local", seq: text("from the process default")})
		mine := &namedStreamProvider{name: "local", seq: text("from my client")}

		opts := NewSummarizeOptions()
		opts.OpOptions.Context = WithProvider(context.Background(), mine)
		stream, err := StreamSummarize("some input", opts)
		if err != nil {
			t.Fatalf("StreamSummarize: %v", err)
		}
		if got := collect(t, stream); got != "from my client" {
			t.Errorf("the stream came %q", got)
		}
	})

	t.Run("a client's execution config", func(t *testing.T) {
		withStreamingProvider(t, &namedStreamProvider{name: "local", seq: text("from the process default")})
		mine := &namedStreamProvider{name: "local", seq: text("from my client")}

		opts := NewSummarizeOptions()
		opts.OpOptions.Context = WithExecConfig(context.Background(), &ExecConfig{Provider: mine})
		stream, err := StreamSummarize("some input", opts)
		if err != nil {
			t.Fatalf("StreamSummarize: %v", err)
		}
		if got := collect(t, stream); got != "from my client" {
			t.Errorf("the stream came %q", got)
		}
	})

	t.Run("no process default is needed", func(t *testing.T) {
		withStreamingProvider(t, nil)
		mine := &namedStreamProvider{name: "local", seq: text("from my client")}

		opts := NewSummarizeOptions()
		opts.OpOptions.Context = WithProvider(context.Background(), mine)
		stream, err := StreamSummarize("some input", opts)
		if err != nil {
			t.Fatalf("StreamSummarize: %v", err)
		}
		if got := collect(t, stream); got != "from my client" {
			t.Errorf("the stream came %q", got)
		}
	})

	t.Run("the client's budget is the one consulted", func(t *testing.T) {
		started := false
		withStreamingProvider(t, &namedStreamProvider{name: "local", seq: func(func(llm.StreamChunk, error) bool) { started = true }})
		budget := &refusingBudget{}

		opts := NewSummarizeOptions()
		opts.OpOptions.Context = WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
		stream, err := StreamSummarize("some input", opts)
		if err == nil || stream != nil || started {
			t.Fatalf("err = %v, stream opened = %v, provider started = %v", err, stream != nil, started)
		}
		if budget.checked != 1 {
			t.Errorf("the client's budget was checked %d times, want 1", budget.checked)
		}
	})

	t.Run("a pinned model is the one requested", func(t *testing.T) {
		var asked string
		mine := &modelRecordingStream{namedStreamProvider: namedStreamProvider{name: "local", seq: text("ok")}, model: &asked}
		withStreamingProvider(t, mine)

		opts := NewSummarizeOptions()
		opts.OpOptions.Model = "a-pinned-model"
		stream, err := StreamSummarize("some input", opts)
		if err != nil {
			t.Fatalf("StreamSummarize: %v", err)
		}
		collect(t, stream)
		if asked != "a-pinned-model" {
			t.Errorf("the request named model %q, want the pinned one", asked)
		}
	})
}

type modelRecordingStream struct {
	namedStreamProvider
	model *string
}

func (p *modelRecordingStream) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	*p.model = req.Model
	return p.namedStreamProvider.CompleteStream(ctx, req)
}

// A deadline the caller set is the budget. Three operations layered the default
// timeout over the caller's context with context.WithTimeout, which takes the
// earlier of the two, so a caller who allowed ten minutes was cut to the
// default thirty seconds. operationContext exists to stop exactly that, and
// these three had not been moved to it.
func TestACallersLongerDeadlineIsNotCut(t *testing.T) {
	cases := []struct {
		name string
		run  func(ctx context.Context)
	}{
		{"Settle", func(ctx context.Context) {
			_, _ = Settle[salaryTerms](map[string]any{"max_budget": 1000}, NegotiateOptions{Context: ctx})
		}},
		{"SettleAdversarial", func(ctx context.Context) {
			_, _ = SettleAdversarial[salaryTerms](AdversarialContext[salaryTerms]{
				Ours:   AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 160000}},
				Theirs: AdversarialPosition[salaryTerms]{Position: salaryTerms{BaseSalary: 130000}},
			}, AdversarialOptions{Context: ctx})
		}},
		{"Decide", func(ctx context.Context) {
			_, _, _ = Decide(ctx, "situation",
				[]Decision[string]{{Value: "a", Description: "first"}, {Value: "b", Description: "second"}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := time.Now().Add(10 * time.Minute)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()

			var got time.Time
			called := false
			previousCaller, previousProvider := currentHooks()
			setLLMCaller(func(callCtx context.Context, _, _ string, _ types.OpOptions) (string, error) {
				called = true
				got, _ = callCtx.Deadline()
				return "", errors.New("stop here")
			})
			t.Cleanup(func() {
				setLLMCaller(previousCaller)
				SetDefaultProvider(previousProvider)
			})

			tc.run(ctx)
			if !called {
				t.Fatal("the operation made no provider call")
			}
			if !got.Equal(want) {
				t.Errorf("the call ran with %v left, want the caller's %v", time.Until(got).Round(time.Second), time.Until(want).Round(time.Second))
			}
		})
	}
}

// The Stream functions read the context from the embedded OpOptions alone. The
// buffered operations read CommonOptions first, which is where WithContext and
// the fluent builders put it, so a client set the usual way was ignored by a
// stream, a cancelled context there still opened one, and a timeout set there
// was dropped.
func TestStreamsReadTheContextWhereTheBufferedPathDoes(t *testing.T) {
	text := func(s string) func(func(llm.StreamChunk, error) bool) {
		return func(yield func(llm.StreamChunk, error) bool) {
			if !yield(llm.StreamChunk{Delta: s}, nil) {
				return
			}
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: s, FinishReason: "stop"}}, nil)
		}
	}
	read := func(t *testing.T, stream *TextStream) string {
		t.Helper()
		var out string
		for chunk, err := range stream.All() {
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			out += chunk
		}
		return out
	}

	type start func(ctx context.Context, timeout time.Duration) (*TextStream, error)
	streams := []struct {
		name  string
		start start
	}{
		{"StreamSummarize", func(ctx context.Context, timeout time.Duration) (*TextStream, error) {
			opts := NewSummarizeOptions()
			opts.CommonOptions.Context, opts.CommonOptions.Timeout = ctx, timeout
			return StreamSummarize("some input", opts)
		}},
		{"StreamRewrite", func(ctx context.Context, timeout time.Duration) (*TextStream, error) {
			opts := NewRewriteOptions()
			opts.CommonOptions.Context, opts.CommonOptions.Timeout = ctx, timeout
			return StreamRewrite("some input", opts)
		}},
		{"StreamTranslate", func(ctx context.Context, timeout time.Duration) (*TextStream, error) {
			opts := NewTranslateOptions().WithTargetLanguage("French")
			opts.CommonOptions.Context, opts.CommonOptions.Timeout = ctx, timeout
			return StreamTranslate("some input", opts)
		}},
		{"StreamExpand", func(ctx context.Context, timeout time.Duration) (*TextStream, error) {
			opts := NewExpandOptions()
			opts.CommonOptions.Context, opts.CommonOptions.Timeout = ctx, timeout
			return StreamExpand("some input", opts)
		}},
	}

	for _, s := range streams {
		t.Run(s.name+"/the client on CommonOptions", func(t *testing.T) {
			withStreamingProvider(t, &namedStreamProvider{name: "local", seq: text("from the process default")})
			mine := &namedStreamProvider{name: "local", seq: text("from my client")}

			stream, err := s.start(WithProvider(context.Background(), mine), 0)
			if err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
			if got := read(t, stream); got != "from my client" {
				t.Errorf("the stream came %q", got)
			}
		})

		t.Run(s.name+"/a cancelled context on CommonOptions", func(t *testing.T) {
			started := false
			withStreamingProvider(t, &namedStreamProvider{name: "local", seq: func(func(llm.StreamChunk, error) bool) { started = true }})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			stream, err := s.start(ctx, 0)
			if stream != nil || started || types.KindOf(err) != types.KindCanceled {
				t.Errorf("stream opened = %v, provider started = %v, err = %v", stream != nil, started, err)
			}
		})

		t.Run(s.name+"/a timeout on CommonOptions", func(t *testing.T) {
			var remaining time.Duration
			provider := &deadlineRecordingStream{namedStreamProvider: namedStreamProvider{name: "local", seq: text("ok")}, remaining: &remaining}
			withStreamingProvider(t, provider)

			stream, err := s.start(context.Background(), 5*time.Second)
			if err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
			read(t, stream)
			if remaining <= 0 || remaining > 5*time.Second {
				t.Errorf("the stream ran with %v left, want the caller's five seconds", remaining)
			}
		})
	}
}

type deadlineRecordingStream struct {
	namedStreamProvider
	remaining *time.Duration
}

func (p *deadlineRecordingStream) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	if deadline, ok := ctx.Deadline(); ok {
		*p.remaining = time.Until(deadline)
	}
	return p.namedStreamProvider.CompleteStream(ctx, req)
}

type recordingBudget struct {
	mu      sync.Mutex
	records []bool // the priced flag of each Record call
}

func (b *recordingBudget) Check() error { return nil }
func (b *recordingBudget) Record(_ float64, priced bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, priced)
}
func (b *recordingBudget) seen() []bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bool(nil), b.records...)
}

// A client's budget is told about every stream it paid for, however the stream
// ended. Only a stream that reached a clean end was recorded, so one that
// failed partway, or that the caller stopped reading, cost tokens the ledger
// never heard of and a client's ceiling was never reached.
func TestAStreamIsRecordedAgainstTheClientsBudgetHoweverItEnds(t *testing.T) {
	failing := errors.New("the connection dropped")
	cases := []struct {
		name string
		seq  func(yield func(llm.StreamChunk, error) bool)
		// read consumes the stream the way this case's caller does.
		read func(*TextStream)
		want []bool
	}{
		{"a clean end",
			func(yield func(llm.StreamChunk, error) bool) {
				if yield(llm.StreamChunk{Delta: "all of it"}, nil) {
					yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
						Content: "all of it", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai",
						Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
					}}, nil)
				}
			},
			func(s *TextStream) {
				for range s.All() {
				}
			},
			[]bool{true}},
		{"a failure partway",
			func(yield func(llm.StreamChunk, error) bool) {
				if yield(llm.StreamChunk{Delta: "some of"}, nil) {
					yield(llm.StreamChunk{}, failing)
				}
			},
			func(s *TextStream) {
				for range s.All() {
				}
			},
			[]bool{false}},
		{"a truncated answer that reports no usage",
			func(yield func(llm.StreamChunk, error) bool) {
				if yield(llm.StreamChunk{Delta: "cut"}, nil) {
					yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "cut", FinishReason: "length"}}, nil)
				}
			},
			func(s *TextStream) {
				for range s.All() {
				}
			},
			[]bool{false}},
		// The tokens were produced and counted, so they are charged, although
		// the answer cannot be used.
		{"a truncated answer that reports its usage",
			func(yield func(llm.StreamChunk, error) bool) {
				if yield(llm.StreamChunk{Delta: "cut"}, nil) {
					yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
						Content: "cut", FinishReason: "length", Model: "gpt-6-luna", Provider: "openai",
						Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
					}}, nil)
				}
			},
			func(s *TextStream) {
				for range s.All() {
				}
			},
			[]bool{true}},
		// Nothing was produced, so there is nothing to record: a buffered call
		// that fails outright records nothing either.
		{"a failure before anything arrives",
			func(yield func(llm.StreamChunk, error) bool) { yield(llm.StreamChunk{}, failing) },
			func(s *TextStream) {
				for range s.All() {
				}
			},
			nil},
		{"a provider that ends without a result",
			func(yield func(llm.StreamChunk, error) bool) { yield(llm.StreamChunk{Delta: "then nothing"}, nil) },
			func(s *TextStream) {
				for range s.All() {
				}
			},
			[]bool{false}},
		{"the caller stops reading",
			func(yield func(llm.StreamChunk, error) bool) {
				for i := 0; i < 1000; i++ {
					if !yield(llm.StreamChunk{Delta: "more "}, nil) {
						return
					}
				}
			},
			func(s *TextStream) { s.Cancel() },
			[]bool{false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStreamingProvider(t, &namedStreamProvider{name: "local", seq: tc.seq})
			budget := &recordingBudget{}

			opts := NewSummarizeOptions()
			opts.OpOptions.Context = WithExecConfig(context.Background(), &ExecConfig{Budget: budget})
			stream, err := StreamSummarize("some input", opts)
			if err != nil {
				t.Fatalf("StreamSummarize: %v", err)
			}
			tc.read(stream)

			// The producer records as it exits, which is after the last item
			// is handed over. Wait for the record, and then a little longer, so
			// that a second record would be seen too.
			deadline := time.Now().Add(2 * time.Second)
			for len(budget.seen()) < len(tc.want) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(30 * time.Millisecond)
			if got := budget.seen(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the budget was told %v (priced flags), want %v", got, tc.want)
			}
		})
	}
}

// The caller's steering never becomes part of the system prompt on the stream
// path either, and the check that guards that is run there too.
func TestStreamKeepsSteeringOutOfTheSystemPrompt(t *testing.T) {
	var system, user string
	provider := &requestRecordingStream{
		namedStreamProvider: namedStreamProvider{name: "local", seq: func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "ok", FinishReason: "stop"}}, nil)
		}},
		system: &system, user: &user,
	}
	withStreamingProvider(t, provider)

	opts := NewSummarizeOptions()
	opts.OpOptions.Steering = "Write for a ten-year-old"
	stream, err := StreamSummarize("some input", opts)
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	for range stream.All() {
	}
	if strings.Contains(system, "ten-year-old") {
		t.Error("the caller's steering is in the system prompt")
	}
	if !strings.Contains(user, "ten-year-old") {
		t.Error("the caller's steering did not reach the request")
	}
}

// The check that guards that is run on the stream path. It refuses a request
// whose steering is found inside the system prompt, which is what a mistake in
// this library's own prompt assembly would look like. The buffered path ran it
// and the stream path built the same request without it.
func TestStreamRunsTheTrustBoundaryCheck(t *testing.T) {
	const system = "You are a careful summariser. Keep every figure."

	for _, tc := range []struct {
		name, steering string
		refused        bool
	}{
		{"steering that is in the system prompt", "Keep every figure.", true},
		{"the whole system prompt as steering", system, true},
		{"steering of the caller's own", "Write for a ten-year-old", false},
		{"no steering", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := false
			withStreamingProvider(t, &namedStreamProvider{name: "local", seq: func(yield func(llm.StreamChunk, error) bool) {
				started = true
				yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "ok", FinishReason: "stop"}}, nil)
			}})

			stream, err := streamLLM(context.Background(), system, "some input", types.OpOptions{Steering: tc.steering})
			if tc.refused {
				if !errors.Is(err, ErrTrustBoundaryViolated) || stream != nil || started {
					t.Fatalf("err = %v, stream opened = %v, provider started = %v; want a refusal", err, stream != nil, started)
				}
				// The buffered path refuses the same request.
				if _, bufferedErr := CallLLM(context.Background(), &captureProvider{}, system, "some input", types.OpOptions{Steering: tc.steering}); !errors.Is(bufferedErr, ErrTrustBoundaryViolated) {
					t.Errorf("the buffered path did not refuse it: %v", bufferedErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("streamLLM: %v", err)
			}
			for range stream.All() {
			}
		})
	}
}

type requestRecordingStream struct {
	namedStreamProvider
	system, user *string
}

func (p *requestRecordingStream) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	*p.system, *p.user = req.SystemPrompt, req.UserPrompt
	return p.namedStreamProvider.CompleteStream(ctx, req)
}

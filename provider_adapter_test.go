package schemaflux

import (
	"context"
	"errors"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// builtinFake is an internal provider that can do everything the call path asks
// of one: stream, warm, name a model for a tier, say which model it sends and
// declare decision limits. It stands for a built-in provider.
type builtinFake struct {
	scriptedInternalProvider
	warmed atomic.Int32
	chunks []llm.StreamChunk
}

func (b *builtinFake) ModelFor(tier types.Speed) string     { return "built-in-model" }
func (b *builtinFake) ModelSentFor(requested string) string { return "sent-as-" + requested }
func (b *builtinFake) Warm(context.Context) error           { b.warmed.Add(1); return nil }
func (b *builtinFake) DecisionLimits() llm.DecisionLimits   { return llm.DecisionLimits{MaxQuestions: 3} }
func (b *builtinFake) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		for _, chunk := range b.chunks {
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

// seamProvider is a Provider as the provider package makes one for a built-in:
// it carries the internal provider it was made from.
type seamProvider struct {
	scriptedProvider
	inner llm.Provider
}

func (s *seamProvider) InternalProvider() llm.Provider { return s.inner }

// capableProvider is a Provider written outside the library that has every
// optional interface the root names.
type capableProvider struct {
	scriptedProvider
	warmed atomic.Int32
	chunks []Chunk
	fail   error
}

func (c *capableProvider) Warm(context.Context) error { c.warmed.Add(1); return nil }
func (c *capableProvider) ModelFor(tier Tier) string {
	return map[Tier]string{TierSmart: "smart-model", TierFast: "fast-model", TierQuick: "quick-model"}[tier]
}
func (c *capableProvider) DecisionLimits() DecisionLimits {
	return DecisionLimits{MaxQuestions: 7, MaxOptions: 5}
}
func (c *capableProvider) Stream(context.Context, Request) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		for _, chunk := range c.chunks {
			if !yield(chunk, nil) {
				return
			}
		}
		if c.fail != nil {
			yield(Chunk{}, c.fail)
		}
	}
}

// plainWrapper wraps a Provider and has no optional interface of its own.
type plainWrapper struct {
	scriptedProvider
	inner Provider
}

func (w *plainWrapper) Unwrap() Provider { return w.inner }

func chain(inner Provider, depth int) Provider {
	for range depth {
		inner = &plainWrapper{scriptedProvider: scriptedProvider{name: "layer"}, inner: inner}
	}
	return inner
}

func collect(t *testing.T, seq iter.Seq2[llm.StreamChunk, error]) (chunks []llm.StreamChunk, err error) {
	t.Helper()
	for chunk, e := range seq {
		if e != nil {
			return chunks, e
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func TestAProviderThatCarriesItsInternalProviderGoesToTheCallPathAsThatProvider(t *testing.T) {
	t.Parallel()
	inner := &builtinFake{scriptedInternalProvider: scriptedInternalProvider{name: "builtin"}}
	seam := &seamProvider{scriptedProvider: scriptedProvider{name: "builtin"}, inner: inner}
	if got := bind.ToInternal(seam); got != llm.Provider(inner) {
		t.Errorf("ToInternal gave %T, want the internal provider itself, not a wrapper of it", got)
	}
	if got := toInternalProvider(seam); got != llm.Provider(inner) {
		t.Errorf("toInternalProvider gave %T", got)
	}
	// What the call path reads of a built-in is all still there.
	got := bind.ToInternal(seam)
	if model, ok := llm.ModelFor(got, types.Fast); !ok || model != "built-in-model" {
		t.Errorf("ModelFor = %q, %v", model, ok)
	}
	if llm.ModelSentBy(got, "x") != "sent-as-x" {
		t.Error("the model sent is not the built-in's")
	}
	if _, ok := got.(llm.StreamingProvider); !ok {
		t.Error("the built-in provider lost its streaming")
	}
	if limits, ok := llm.DecisionLimitsOf(got); !ok || limits.MaxQuestions != 3 {
		t.Errorf("decision limits = %+v, %v", limits, ok)
	}
	if warmed, err := llm.Warm(context.Background(), got); err != nil || !warmed || inner.warmed.Load() != 1 {
		t.Errorf("Warm = %v, %v, warmed %d times", warmed, err, inner.warmed.Load())
	}
}

func TestASeamThatReturnsNothingIsAProviderLikeAnyOther(t *testing.T) {
	t.Parallel()
	seam := &seamProvider{scriptedProvider: scriptedProvider{name: "odd"}, inner: nil}
	got := bind.ToInternal(seam)
	if got == nil || got.Name() != "odd" {
		t.Fatalf("ToInternal = %v", got)
	}
	if _, wrapped := got.(*internalProvider); !wrapped {
		t.Errorf("a provider whose seam is empty was not wrapped: %T", got)
	}
	if bind.FromInternal(got) != any(seam) {
		t.Error("the way back does not give the provider")
	}
}

func TestAProviderOfTheCallersOwnIsWrappedAsBefore(t *testing.T) {
	t.Parallel()
	own := &scriptedProvider{name: "own", complete: func(context.Context, Request) (Response, error) { return Response{Content: "x"}, nil }}
	got := bind.ToInternal(own)
	adapter, ok := got.(*internalProvider)
	if !ok || adapter.provider != Provider(own) || adapter.under != nil {
		t.Fatalf("ToInternal = %#v, want an adapter of the provider with nothing under it", got)
	}
	if bind.FromInternal(got) != any(own) {
		t.Error("the provider did not come back the same")
	}
	if got.Name() != "own" || got.EstimateCost(llm.CompletionRequest{}) != 0 {
		t.Error("Name or EstimateCost")
	}
	if retries, _ := got.RetryPolicy(); retries >= 0 {
		t.Errorf("a provider with no retry settings is %d retries, want not configured", retries)
	}
}

func TestAWrapperAroundABuiltInIsWrappedAndTheBuiltInIsReachableThroughIt(t *testing.T) {
	t.Parallel()
	inner := &builtinFake{scriptedInternalProvider: scriptedInternalProvider{name: "builtin"}, chunks: []llm.StreamChunk{{Delta: "ab"}, {Done: true, Response: llm.CompletionResponse{Content: "ab"}}}}
	seam := &seamProvider{scriptedProvider: scriptedProvider{name: "builtin"}, inner: inner}
	for depth := 1; depth <= 4; depth++ {
		wrapped := chain(seam, depth)
		got := bind.ToInternal(wrapped)
		adapter, ok := got.(*internalDecider)
		if !ok {
			t.Fatalf("depth %d: ToInternal = %T, want an adapter that declares the built-in's limits", depth, got)
		}
		if adapter.provider != wrapped {
			t.Fatalf("depth %d: the middleware was not the provider wrapped: its Complete would not run", depth)
		}
		if adapter.Unwrap() != llm.Provider(inner) {
			t.Errorf("depth %d: Unwrap = %v, want the built-in at the bottom", depth, adapter.Unwrap())
		}
		if model, ok := llm.ModelFor(got, types.Smart); !ok || model != "built-in-model" {
			t.Errorf("depth %d: ModelFor = %q, %v", depth, model, ok)
		}
		if llm.ModelSentBy(got, "m") != "sent-as-m" {
			t.Errorf("depth %d: ModelSentBy = %q", depth, llm.ModelSentBy(got, "m"))
		}
		if limits, ok := llm.DecisionLimitsOf(got); !ok || limits.MaxQuestions != 3 {
			t.Errorf("depth %d: limits = %+v, %v", depth, limits, ok)
		}
		streamer, ok := got.(llm.StreamingProvider)
		if !ok {
			t.Fatalf("depth %d: not a streaming provider", depth)
		}
		chunks, err := collect(t, streamer.CompleteStream(context.Background(), llm.CompletionRequest{}))
		if err != nil || len(chunks) != 2 || chunks[1].Response.Content != "ab" {
			t.Errorf("depth %d: stream = %+v, %v", depth, chunks, err)
		}
		if bind.FromInternal(got) != any(wrapped) {
			t.Errorf("depth %d: the way back does not give the wrapper", depth)
		}
	}
}

func TestTheAdapterAnswersEachOptionalInterfaceFromTheProviderAndFromWhatItWraps(t *testing.T) {
	t.Parallel()
	for depth := range 4 {
		t.Run("at depth "+string(rune('0'+depth)), func(t *testing.T) {
			t.Parallel()
			capable := &capableProvider{
				scriptedProvider: scriptedProvider{name: "custom"},
				chunks:           []Chunk{{Delta: "he"}, {Delta: "llo"}, {Done: true, Response: Response{Content: "hello", Model: "m", Usage: Usage{Input: 3, Output: 2}}}},
			}
			got := bind.ToInternal(chain(capable, depth))

			if err := warm(t, got); err != nil || capable.warmed.Load() != 1 {
				t.Errorf("Warm = %v, warmed %d times", err, capable.warmed.Load())
			}
			for tier, want := range map[types.Speed]string{types.Smart: "smart-model", types.Fast: "fast-model", types.Quick: "quick-model"} {
				if model, ok := llm.ModelFor(got, tier); !ok || model != want {
					t.Errorf("ModelFor(%v) = %q, %v, want %q", tier, model, ok, want)
				}
			}
			if limits, ok := llm.DecisionLimitsOf(got); !ok || limits != (llm.DecisionLimits{MaxQuestions: 7, MaxOptions: 5}) {
				t.Errorf("limits = %+v, %v", limits, ok)
			}
			streamer, ok := got.(llm.StreamingProvider)
			if !ok {
				t.Fatal("not a streaming provider")
			}
			chunks, err := collect(t, streamer.CompleteStream(context.Background(), llm.CompletionRequest{UserPrompt: "u"}))
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) != 3 || chunks[0].Delta != "he" || chunks[1].Delta != "llo" || !chunks[2].Done {
				t.Fatalf("chunks = %+v", chunks)
			}
			if r := chunks[2].Response; r.Content != "hello" || r.Model != "m" || r.Usage.PromptTokens != 3 || r.Usage.CompletionTokens != 2 {
				t.Errorf("the whole answer of the last chunk is %+v", r)
			}
		})
	}
}

// warm calls Warm on a provider the way the call path does.
func warm(t *testing.T, p llm.Provider) error {
	t.Helper()
	_, err := llm.Warm(context.Background(), p)
	return err
}

func TestAProviderWithNoOptionalInterfaceHasNoneThroughTheAdapter(t *testing.T) {
	t.Parallel()
	got := bind.ToInternal(chain(&scriptedProvider{name: "plain"}, 2))
	if _, ok := llm.DecisionLimitsOf(got); ok {
		t.Error("a provider that declares no limits has some")
	}
	if model, ok := llm.ModelFor(got, types.Fast); ok || model != "" {
		t.Errorf("ModelFor = %q, %v, want none", model, ok)
	}
	if err := warm(t, got); err != nil {
		t.Errorf("Warm = %v", err)
	}
	if got.(llm.ModelSender).ModelSentFor("asked") != "asked" {
		t.Error("the model sent is not the model asked")
	}
	streamer, ok := got.(llm.StreamingProvider)
	if !ok {
		t.Fatal("every adapter has CompleteStream: it says it cannot stream")
	}
	_, err := collect(t, streamer.CompleteStream(context.Background(), llm.CompletionRequest{}))
	if err == nil || types.KindOf(err) != types.KindUnsupportedCapability {
		t.Errorf("CompleteStream = %v, want an unsupported capability", err)
	}
}

func TestAStreamThatBreaksIsAnErrorOfTheKindTheProviderGave(t *testing.T) {
	t.Parallel()
	capable := &capableProvider{
		scriptedProvider: scriptedProvider{name: "custom"},
		chunks:           []Chunk{{Delta: "par"}},
		fail:             &Error{Kind: ErrRateLimited, Message: "slow down", RetryAfter: 3 * time.Second},
	}
	streamer := bind.ToInternal(capable).(llm.StreamingProvider)
	chunks, err := collect(t, streamer.CompleteStream(context.Background(), llm.CompletionRequest{}))
	if err == nil {
		t.Fatal("a stream that broke was not an error")
	}
	if len(chunks) != 1 || chunks[0].Done {
		t.Errorf("chunks = %+v: no Done chunk is made of text that arrived before it broke", chunks)
	}
	if e := asError(t, "summarize", err); e.Kind != ErrRateLimited || e.RetryAfter != 3*time.Second {
		t.Errorf("the failure is {%v %v}", e.Kind, e.RetryAfter)
	}
}

func TestAStreamStopsWhenTheReaderStops(t *testing.T) {
	t.Parallel()
	var sent atomic.Int32
	capable := &capableProvider{scriptedProvider: scriptedProvider{name: "custom"}}
	for range 100 {
		capable.chunks = append(capable.chunks, Chunk{Delta: "x"})
	}
	streamer := bind.ToInternal(capable).(llm.StreamingProvider)
	for chunk := range streamer.CompleteStream(context.Background(), llm.CompletionRequest{}) {
		_ = chunk
		if sent.Add(1) == 3 {
			break
		}
	}
	if sent.Load() != 3 {
		t.Errorf("read %d chunks", sent.Load())
	}
}

func TestAStreamOfARequestWithToolsIsRefusedAsUnsupported(t *testing.T) {
	t.Parallel()
	capable := &capableProvider{scriptedProvider: scriptedProvider{name: "custom"}, chunks: []Chunk{{Done: true}}}
	streamer := bind.ToInternal(capable).(llm.StreamingProvider)
	_, err := collect(t, streamer.CompleteStream(context.Background(), llm.CompletionRequest{ToolChoice: "auto"}))
	if err == nil || asError(t, "x", err).Kind != ErrUnsupported {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestDecisionLimitsAreForwardedFromAnyDepthAndOnlyWhenDeclared(t *testing.T) {
	t.Parallel()
	for depth := range 6 {
		capable := &capableProvider{scriptedProvider: scriptedProvider{name: "custom"}}
		got := bind.ToInternal(chain(capable, depth))
		if _, ok := got.(*internalDecider); !ok {
			t.Errorf("depth %d: %T is not an adapter with limits", depth, got)
		}
		plain := bind.ToInternal(chain(&scriptedProvider{name: "plain"}, depth))
		if _, ok := plain.(*internalDecider); ok {
			t.Errorf("depth %d: a provider that declares no limits got an adapter that has them", depth)
		}
	}
}

func TestTheStreamAndTheLimitsOfAnAdapterOfADeciderComeBackAsTheSameProvider(t *testing.T) {
	t.Parallel()
	capable := &capableProvider{scriptedProvider: scriptedProvider{name: "custom"}}
	got := bind.ToInternal(capable)
	if bind.FromInternal(got) != any(capable) {
		t.Error("an adapter with limits did not give its provider back")
	}
	if toInternalProvider(capable) == nil {
		t.Error("toInternalProvider gave nil")
	}
}

func TestUnwrapProviderIsOneStepThroughTheRootsWrappers(t *testing.T) {
	t.Parallel()
	inner := &scriptedProvider{name: "inner"}
	wrapped := &plainWrapper{scriptedProvider: scriptedProvider{name: "w"}, inner: inner}
	cases := map[string]struct {
		in   any
		want any
	}{
		"a wrapper":                   {wrapped, inner},
		"a provider that wraps none":  {inner, nil},
		"a nil":                       {nil, nil},
		"a string":                    {"x", nil},
		"a wrapper that wraps nil":    {&plainWrapper{inner: nil}, nil},
		"a wrapper of a wrapper":      {&plainWrapper{inner: wrapped}, wrapped},
		"an internal provider":        {&scriptedInternalProvider{name: "i"}, nil},
		"a typed nil wrapper's inner": {&plainWrapper{inner: (*scriptedProvider)(nil)}, any((*scriptedProvider)(nil))},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := unwrapProvider(c.in); got != c.want {
				t.Errorf("unwrapProvider = %v, want %v", got, c.want)
			}
		})
	}
}

func TestOptionalFindsAnInterfaceThroughTheRootsWrappers(t *testing.T) {
	t.Parallel()
	capable := &capableProvider{scriptedProvider: scriptedProvider{name: "custom"}}
	for depth := range 5 {
		got, ok := bind.Optional[optionalWarmer](chain(capable, depth))
		if !ok || got != optionalWarmer(capable) {
			t.Errorf("depth %d: Optional = %v, %v", depth, got, ok)
		}
	}
	if _, ok := bind.Optional[optionalWarmer](chain(&scriptedProvider{name: "plain"}, 3)); ok {
		t.Error("found a Warm that no layer has")
	}
}

func TestCommonOfThroughTheRootIsTheOptionsCheckAndConversion(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		in      any
		wantErr bool
		want    bind.Common
	}{
		"the zero Options":          {Options{}, false, bind.Common{}},
		"a value":                   {Options{Model: "m", Tier: TierSmart, Effort: EffortHigh, Mode: ModeStrict, Steering: "s", MaxOutputTokens: 9, WebSearch: true}, false, bind.Common{Model: "m", Tier: types.Smart, Effort: types.Effort("high"), Mode: types.Strict, Steering: "s", MaxOutputTokens: 9, WebSearch: true}},
		"a pointer":                 {&Options{Model: "p"}, false, bind.Common{Model: "p"}},
		"a nil pointer":             {(*Options)(nil), false, bind.Common{}},
		"a negative ceiling":        {Options{MaxOutputTokens: -1}, true, bind.Common{}},
		"a tier past the last":      {Options{Tier: TierQuick + 1}, true, bind.Common{}},
		"a mode past the last":      {Options{Mode: ModeCreative + 1}, true, bind.Common{}},
		"a string":                  {"options", true, bind.Common{}},
		"nil":                       {nil, true, bind.Common{}},
		"another struct":            {struct{ Model string }{"m"}, true, bind.Common{}},
		"a value that has Options":  {struct{ Options }{Options{Model: "m"}}, true, bind.Common{}},
		"an Options with only a ws": {Options{WebSearch: true}, false, bind.Common{WebSearch: true}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := bind.CommonOf(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("CommonOf error = %v, want error = %v", err, c.wantErr)
			}
			if err != nil {
				var e *Error
				if !errors.As(err, &e) || e.Op != "" {
					t.Errorf("the error is %T %v, want an *Error that leaves the operation to the caller", err, err)
				}
				if got != (bind.Common{}) {
					t.Errorf("a Common came back beside an error: %+v", got)
				}
				converted := bind.ConvertError("classify", err)
				if !errors.As(converted, &e) || e.Op != "classify" {
					t.Errorf("ConvertError did not fill in the operation: %v", converted)
				}
				return
			}
			if got != c.want {
				t.Errorf("CommonOf = %+v, want %+v", got, c.want)
			}
		})
	}
	t.Run("the kinds", func(t *testing.T) {
		t.Parallel()
		_, err := bind.CommonOf(Options{MaxOutputTokens: -1})
		if kindOf(t, err) != ErrConfiguration {
			t.Errorf("a bad setting is %v", err)
		}
		_, err = bind.CommonOf(3)
		if kindOf(t, err) != ErrInternal {
			t.Errorf("a value that is no Options is %v", err)
		}
	})
}

func TestEveryAdapterTypeNamesTheMethodsTheCallPathAsksFor(t *testing.T) {
	t.Parallel()
	var _ llm.Provider = (*internalProvider)(nil)
	var _ llm.StreamingProvider = (*internalProvider)(nil)
	var _ llm.TierModels = (*internalProvider)(nil)
	var _ llm.ModelSender = (*internalProvider)(nil)
	var _ llm.Warmer = (*internalProvider)(nil)
	var _ interface{ DecisionLimits() llm.DecisionLimits } = (*internalDecider)(nil)
	var _ interface{ Unwrap() llm.Provider } = (*internalProvider)(nil)
	if _, ok := any((*internalProvider)(nil)).(interface{ DecisionLimits() llm.DecisionLimits }); ok {
		t.Error("an adapter with no limits has the method: llm.DecisionLimitsOf would find some")
	}
}

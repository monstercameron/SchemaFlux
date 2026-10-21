package schemaflux

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// A stream in the PLAIN state allocates what the internal call it wraps
// allocates, read to the end, and one more: the TextStream of the root, which
// holds the internal stream and cannot be the internal stream itself, because the
// internal type has a wider set of methods and reports its errors unconverted.
// On the default client the function allocates one more again, which is the pin
// of the client the call runs on, as for every other function.
//
// A stream has a goroutine of its own, and AllocsPerRun counts the process, so
// each measurement is of a whole stream: opened and read to the end, on both
// sides.

// allocStreamer is the scripted model of the allocation tests of the streams: it
// streams one piece and then the last chunk, and keeps nothing.
type allocStreamer struct{ reply string }

func (p *allocStreamer) Name() string                      { return "alloc-test" }
func (p *allocStreamer) ModelFor(Tier) string              { return "alloc-model" }
func (p *allocStreamer) RetryPolicy() (int, time.Duration) { return -1, 0 }

func (p *allocStreamer) Complete(context.Context, Request) (Response, error) {
	return Response{}, errors.New("this fake only streams")
}

func (p *allocStreamer) Stream(context.Context, Request) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		if !yield(Chunk{Delta: p.reply}, nil) {
			return
		}
		yield(Chunk{Done: true, Response: Response{
			Content: p.reply, Model: "alloc-model", FinishReason: "stop",
			Usage: Usage{Input: 11, Output: 3, Total: 14},
		}}, nil)
	}
}

// requireStreamAllocsLikeTheInternalCallPlusTheWrapper measures the function and
// the internal call, each opening a stream and reading it to its end, on a client
// carried by the context and on the default client. The function has to allocate
// one more than the internal call (the root's TextStream) under a client, and two
// more on the default client (that, and the pin).
func requireStreamAllocsLikeTheInternalCallPlusTheWrapper(t *testing.T, c allocCase) {
	t.Helper()
	model := &allocStreamer{reply: c.reply}

	client := WithClient(context.Background(), Client{Provider: model})
	if err := c.call(client); err != nil {
		t.Fatalf("the function failed on the scripted model: %v", err)
	}
	if err := c.internal(client); err != nil {
		t.Fatalf("the internal call failed on the scripted model: %v", err)
	}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _ = c.call(client) })
	direct := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(client) })
	t.Logf("under WithClient: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct+1 {
		t.Errorf("under a client the function allocates %v times, want the internal call's %v plus the TextStream", wrapped, direct)
	}

	previous := Default()
	SetDefault(Client{Provider: model})
	t.Cleanup(func() { SetDefault(previous) })
	background := context.Background()
	pinned, _ := ops.PinExec(background)
	wrappedDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.call(background) })
	directPinned := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(pinned) })
	directDefault := testing.AllocsPerRun(allocRuns, func() { _ = c.internal(background) })
	t.Logf("on the default client: the function %v allocations, the internal call %v on a pinned context and %v on none", wrappedDefault, directPinned, directDefault)
	if wrappedDefault != directPinned+2 {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin and the TextStream", wrappedDefault, directPinned)
	}
	if wrappedDefault != directDefault+1 {
		t.Errorf("on the default client the function allocates %v times and the internal call %v: the pin is paid once by either, so they differ by the TextStream only", wrappedDefault, directDefault)
	}
}

// readInternalStream reads an internal stream to its end and returns the error.
func readInternalStream(s *ops.TextStream, err error) error {
	if err != nil {
		return err
	}
	_, err = s.Text()
	return err
}

// readStream is readInternalStream for the root's stream.
func readStream(s *TextStream, err error) error {
	if err != nil {
		return err
	}
	_, err = s.Text()
	return err
}

func TestStreamSummarizeAllocatesAsTheInternalCallPlusTheWrapperInThePlainState(t *testing.T) {
	const text = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	cases := map[string]SummarizeOptions{
		"no options":           {},
		"one options value":    {TargetLength: 3, LengthUnit: "words", Style: "executive", FocusAreas: []string{"revenue"}, Model: "alloc-model"},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireStreamAllocsLikeTheInternalCallPlusTheWrapper(t, allocCase{
				reply: "Revenue grew.",
				call:  func(ctx context.Context) error { return readStream(StreamSummarize(ctx, text, o)) },
				internal: func(ctx context.Context) error {
					return readInternalStream(ops.StreamSummarize(text, summarizeInternal(ctx, o)))
				},
			})
		})
	}
}

func TestStreamRewriteAllocatesAsTheInternalCallPlusTheWrapperInThePlainState(t *testing.T) {
	const text = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	cases := map[string]RewriteOptions{
		"no options":        {},
		"one options value": {Tone: "formal", Formality: 8, Audience: "engineers", Changes: []string{"shorten"}, AvoidWords: []string{"synergy"}, Model: "alloc-model"},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireStreamAllocsLikeTheInternalCallPlusTheWrapper(t, allocCase{
				reply: "Revenue grew.",
				call:  func(ctx context.Context) error { return readStream(StreamRewrite(ctx, text, o)) },
				internal: func(ctx context.Context) error {
					return readInternalStream(ops.StreamRewrite(text, rewriteInternal(ctx, o)))
				},
			})
		})
	}
}

func TestStreamTranslateAllocatesAsTheInternalCallPlusTheWrapperInThePlainState(t *testing.T) {
	const text = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	cases := map[string]TranslateOptions{
		"no options":        {},
		"one options value": {SourceLanguage: "English", Dialect: "Mexican", Formality: "formal", CulturalAdaptation: 7, Model: "alloc-model"},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireStreamAllocsLikeTheInternalCallPlusTheWrapper(t, allocCase{
				reply: "El ingreso crecio.",
				call:  func(ctx context.Context) error { return readStream(StreamTranslate(ctx, text, "Spanish", o)) },
				internal: func(ctx context.Context) error {
					return readInternalStream(ops.StreamTranslate(text, translateInternal(ctx, "Spanish", o)))
				},
			})
		})
	}
}

func TestStreamExpandAllocatesAsTheInternalCallPlusTheWrapperInThePlainState(t *testing.T) {
	const text = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	cases := map[string]ExpandOptions{
		"no options":        {},
		"one options value": {Factor: 3, DetailLevel: 8, IncludeExamples: true, ElaborateOn: []string{"costs"}, Style: "technical", Model: "alloc-model"},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireStreamAllocsLikeTheInternalCallPlusTheWrapper(t, allocCase{
				reply: "Revenue grew a great deal.",
				call:  func(ctx context.Context) error { return readStream(StreamExpand(ctx, text, o)) },
				internal: func(ctx context.Context) error {
					return readInternalStream(ops.StreamExpand(text, expandInternal(ctx, o)))
				},
			})
		})
	}
}

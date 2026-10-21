package schemaflux_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// streamModel is a provider that streams what a script tells it to. It records
// every request it is sent at the moment the stream starts, and it cannot
// complete a request: a call that reached Complete would be a stream that fell
// back to a buffered answer, which no stream may do.
type streamModel struct {
	name string
	run  streamScript

	mu   sync.Mutex
	sent []schemaflux.Request
}

// streamScript is what a streamModel does for one request: it yields chunks until
// yield says stop, and may wait for ctx.
type streamScript func(ctx context.Context, yield func(schemaflux.Chunk, error) bool)

func newStreamModel(run streamScript) *streamModel { return &streamModel{run: run} }

func (m *streamModel) Name() string {
	if m.name == "" {
		return "stream-test"
	}
	return m.name
}

func (m *streamModel) ModelFor(schemaflux.Tier) string { return "test-model" }

func (m *streamModel) RetryPolicy() (int, time.Duration) { return -1, 0 }

func (m *streamModel) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{}, errors.New("this fake only streams")
}

func (m *streamModel) Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) {
		m.mu.Lock()
		m.sent = append(m.sent, req)
		m.mu.Unlock()
		m.run(ctx, yield)
	}
}

func (m *streamModel) requests() []schemaflux.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sent)
}

func (m *streamModel) last() schemaflux.Request {
	sent := m.requests()
	if len(sent) == 0 {
		return schemaflux.Request{}
	}
	return sent[len(sent)-1]
}

// finalChunk is the last event of a stream that completed with the text given.
func finalChunk(text, finish string) schemaflux.Chunk {
	return schemaflux.Chunk{Done: true, Response: schemaflux.Response{
		Content: text, Model: "test-model", FinishReason: finish,
		Usage: schemaflux.Usage{Input: 11, Output: 3, Total: 14},
	}}
}

// finishing yields the pieces and then a last event that says the answer is whole.
func finishing(pieces ...string) streamScript {
	return func(_ context.Context, yield func(schemaflux.Chunk, error) bool) {
		for _, p := range pieces {
			if !yield(schemaflux.Chunk{Delta: p}, nil) {
				return
			}
		}
		yield(finalChunk(strings.Join(pieces, ""), "stop"), nil)
	}
}

// truncatedAfter yields the pieces and a last event that says the model ran out
// of room.
func truncatedAfter(pieces ...string) streamScript {
	return func(_ context.Context, yield func(schemaflux.Chunk, error) bool) {
		for _, p := range pieces {
			if !yield(schemaflux.Chunk{Delta: p}, nil) {
				return
			}
		}
		yield(finalChunk(strings.Join(pieces, ""), "length"), nil)
	}
}

// breakingAfter yields the pieces and then the failure.
func breakingAfter(err error, pieces ...string) streamScript {
	return func(_ context.Context, yield func(schemaflux.Chunk, error) bool) {
		for _, p := range pieces {
			if !yield(schemaflux.Chunk{Delta: p}, nil) {
				return
			}
		}
		yield(schemaflux.Chunk{}, err)
	}
}

// hangingAfter yields the pieces and then waits for the context, as a provider
// that has said what it has and is slow to finish does.
func hangingAfter(pieces ...string) streamScript {
	return func(ctx context.Context, yield func(schemaflux.Chunk, error) bool) {
		for _, p := range pieces {
			if !yield(schemaflux.Chunk{Delta: p}, nil) {
				return
			}
		}
		<-ctx.Done()
		yield(schemaflux.Chunk{}, ctx.Err())
	}
}

// flooding yields n pieces of one letter and then completes: more pieces than the
// stream's buffer holds, so a stream nobody reads blocks its producer.
func flooding(n int) streamScript {
	return func(_ context.Context, yield func(schemaflux.Chunk, error) bool) {
		for range n {
			if !yield(schemaflux.Chunk{Delta: "x"}, nil) {
				return
			}
		}
		yield(finalChunk(strings.Repeat("x", n), "stop"), nil)
	}
}

// streamCall is one of the four Stream functions called with the same text and
// the common settings given.
type streamCall struct {
	name string
	// op is the Op of its Record and the operation its errors name.
	op string
	// lead is how the user message of its request begins.
	lead string
	open func(ctx context.Context, o schemaflux.Options) (*schemaflux.TextStream, error)
}

const streamInput = "The quarterly report shows revenue grew twelve percent while costs stayed flat."

var streamCalls = []streamCall{
	{"StreamSummarize", "streamsummarize", "Summarize this text:", func(ctx context.Context, o schemaflux.Options) (*schemaflux.TextStream, error) {
		return schemaflux.StreamSummarize(ctx, streamInput, schemaflux.SummarizeOptions{Options: o})
	}},
	{"StreamRewrite", "streamrewrite", "Rewrite this text:", func(ctx context.Context, o schemaflux.Options) (*schemaflux.TextStream, error) {
		return schemaflux.StreamRewrite(ctx, streamInput, schemaflux.RewriteOptions{Options: o})
	}},
	{"StreamTranslate", "streamtranslate", "Translate this text:", func(ctx context.Context, o schemaflux.Options) (*schemaflux.TextStream, error) {
		return schemaflux.StreamTranslate(ctx, streamInput, "French", schemaflux.TranslateOptions{Options: o})
	}},
	{"StreamExpand", "streamexpand", "Expand on this text:", func(ctx context.Context, o schemaflux.Options) (*schemaflux.TextStream, error) {
		return schemaflux.StreamExpand(ctx, streamInput, schemaflux.ExpandOptions{Options: o})
	}},
}

// eachStreamCall runs the test once for each of the four functions.
func eachStreamCall(t *testing.T, parallel bool, test func(t *testing.T, c streamCall)) {
	t.Helper()
	for _, c := range streamCalls {
		t.Run(c.name, func(t *testing.T) {
			if parallel {
				t.Parallel()
			}
			test(t, c)
		})
	}
}

// readAll reads a stream with All and returns the pieces and the error that
// ended the loop.
func readAll(s *schemaflux.TextStream) (pieces []string, err error) {
	for piece, readErr := range s.All() {
		if readErr != nil {
			err = readErr
			continue
		}
		pieces = append(pieces, piece)
	}
	return pieces, err
}

// wantErrorKind fails the test unless err is an *Error of the kind, and returns
// it. The operation is not asserted: an error of a stream carries the one the
// call path named, which is the stream's own for some endings and the call's for
// others.
func wantErrorKind(t *testing.T, err error, kind schemaflux.Kind) *schemaflux.Error {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("the error is %T (%v), want an *Error", err, err)
	}
	if e.Kind != kind || !errors.Is(err, kind) {
		t.Fatalf("error = kind %v (%v), want kind %v", e.Kind, err, kind)
	}
	return e
}

// streamLeakCheck counts only goroutines in this synctest bubble. The full test
// suite may have hundreds of goroutines waiting in other parallel tests, and
// counting all of them makes an unrelated new goroutine look like a stream leak.
func streamLeakCheck(t *testing.T) func() {
	t.Helper()
	synctest.Wait()
	before, _ := streamBubbleGoroutines(t)
	return func() {
		t.Helper()
		synctest.Wait()
		after, stacks := streamBubbleGoroutines(t)
		for tries := 0; after > before && tries < 20; tries++ {
			runtime.Gosched()
			synctest.Wait()
			after, stacks = streamBubbleGoroutines(t)
		}
		if after > before {
			t.Errorf("%d goroutines in this bubble before the stream and %d after it ended:\n%s", before, after, stacks)
		}
	}
}

var streamBubbleID = regexp.MustCompile(`synctest bubble ([0-9]+)`)

func streamBubbleGoroutines(t *testing.T) (int, []byte) {
	t.Helper()
	stacks := make([]byte, 1<<17)
	for {
		n := runtime.Stack(stacks, true)
		if n < len(stacks) {
			stacks = stacks[:n]
			break
		}
		stacks = make([]byte, len(stacks)*2)
	}
	header, _, _ := strings.Cut(string(stacks), "\n")
	id := streamBubbleID.FindStringSubmatch(header)
	if len(id) != 2 {
		t.Fatalf("current goroutine is outside a synctest bubble: %s", header)
	}
	return strings.Count(string(stacks), "synctest bubble "+id[1]+"]:"), stacks
}

// handleCounter is a recorder whose Handle counts what it is given, keeping the
// Records beside the count.
type handleCounter struct {
	recorder schemaflux.Recorder
	calls    atomic.Int32
	mu       sync.Mutex
	records  []schemaflux.Record
}

func newHandleCounter() *handleCounter {
	h := &handleCounter{}
	h.recorder.Handle = func(_ context.Context, rec schemaflux.Record) {
		h.calls.Add(1)
		h.mu.Lock()
		h.records = append(h.records, rec)
		h.mu.Unlock()
	}
	return h
}

func (h *handleCounter) only(t *testing.T) schemaflux.Record {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) != 1 {
		t.Fatalf("Handle was given %d Records, want exactly 1", len(h.records))
	}
	return h.records[0]
}

// ---------------------------------------------------------------------------
// What a stream delivers, and the request it sends.

func TestAStreamDeliversThePiecesInOrderAndTheWholeTextAsOne(t *testing.T) {
	t.Parallel()
	eachStreamCall(t, true, func(t *testing.T, c streamCall) {
		model := newStreamModel(finishing("Reve", "nue ", "grew."))
		stream, err := c.open(operationClient(model), schemaflux.Options{})
		if err != nil {
			t.Fatal(err)
		}
		pieces, err := readAll(stream)
		if err != nil || strings.Join(pieces, "|") != "Reve|nue |grew." {
			t.Fatalf("pieces %q, err %v", pieces, err)
		}
		if !stream.Ended() || stream.Err() != nil {
			t.Errorf("Ended %v Err %v after the whole answer was read", stream.Ended(), stream.Err())
		}
		for range 2 {
			text, err := stream.Text()
			if err != nil || text != "Revenue grew." {
				t.Errorf("Text = %q, %v", text, err)
			}
		}
		if got := len(model.requests()); got != 1 {
			t.Errorf("%d requests were sent, want 1", got)
		}
	})
}

func TestTextWaitsForTheWholeAnswerWithoutAnyoneRangingOverTheStream(t *testing.T) {
	t.Parallel()
	eachStreamCall(t, true, func(t *testing.T, c streamCall) {
		stream, err := c.open(operationClient(newStreamModel(finishing("one ", "two ", "three"))), schemaflux.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if text, err := stream.Text(); err != nil || text != "one two three" {
			t.Fatalf("Text = %q, %v", text, err)
		}
	})
}

func TestAStreamSendsTheRequestOfItsBufferedFormAsAStream(t *testing.T) {
	t.Parallel()
	eachStreamCall(t, true, func(t *testing.T, c streamCall) {
		model := newStreamModel(finishing("ok"))
		stream, err := c.open(operationClient(model), schemaflux.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Text(); err != nil {
			t.Fatal(err)
		}
		req := model.last()
		wantText(t, req, c.lead)
		wantText(t, req, streamInput)
		if req.Schema != nil || req.Format == "json" {
			t.Errorf("format %q schema %v: a stream is the model's text, with no schema", req.Format, req.Schema != nil)
		}
		if req.Model != "test-model" || req.MaxTokens != 2000 {
			t.Errorf("model %q ceiling %d, want the Fast tier's", req.Model, req.MaxTokens)
		}
	})
}

func TestEachStreamIsAskedForWhatItsOptionFieldsSay(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		open func(ctx context.Context) (*schemaflux.TextStream, error)
		has  []string
		not  []string
	}
	rewrite := func(o schemaflux.RewriteOptions) func(context.Context) (*schemaflux.TextStream, error) {
		return func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamRewrite(ctx, streamInput, o)
		}
	}
	translate := func(o schemaflux.TranslateOptions) func(context.Context) (*schemaflux.TextStream, error) {
		return func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamTranslate(ctx, streamInput, "Spanish", o)
		}
	}
	expand := func(o schemaflux.ExpandOptions) func(context.Context) (*schemaflux.TextStream, error) {
		return func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamExpand(ctx, streamInput, o)
		}
	}
	summarize := func(o schemaflux.SummarizeOptions) func(context.Context) (*schemaflux.TextStream, error) {
		return func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamSummarize(ctx, streamInput, o)
		}
	}
	cases := []tc{
		{"summarize TargetLength", summarize(schemaflux.SummarizeOptions{TargetLength: 2}), []string{"Target length: 2 sentences"}, nil},
		{"summarize LengthUnit", summarize(schemaflux.SummarizeOptions{TargetLength: 30, LengthUnit: "words"}), []string{"Target length: 30 words"}, nil},
		{"summarize Style", summarize(schemaflux.SummarizeOptions{Style: "executive"}), []string{"Style: executive"}, nil},
		{"summarize BulletPoints", summarize(schemaflux.SummarizeOptions{BulletPoints: true, Style: "executive"}), []string{"Format as bullet points"}, []string{"Style:"}},
		{"summarize FocusAreas", summarize(schemaflux.SummarizeOptions{FocusAreas: []string{"revenue", "costs"}}), []string{"Focus on: revenue, costs"}, nil},
		{"summarize PreserveInfo", summarize(schemaflux.SummarizeOptions{PreserveInfo: []string{"dates"}}), []string{"Must preserve: dates"}, nil},
		{"summarize default", summarize(schemaflux.SummarizeOptions{}), []string{"Style: paragraph"}, []string{"Target length"}},
		{"rewrite Tone", rewrite(schemaflux.RewriteOptions{Tone: "formal"}), []string{"Target tone: formal"}, nil},
		{"rewrite Formality", rewrite(schemaflux.RewriteOptions{Formality: 8}), []string{"Formality level: 8/10"}, nil},
		{"rewrite default Formality is 5", rewrite(schemaflux.RewriteOptions{}), []string{"Preserve all factual information"}, []string{"Formality level"}},
		{"rewrite NoPreserveFacts", rewrite(schemaflux.RewriteOptions{NoPreserveFacts: true}), nil, []string{"Preserve all factual information"}},
		{"rewrite Audience", rewrite(schemaflux.RewriteOptions{Audience: "engineers"}), []string{"Target audience: engineers"}, nil},
		{"rewrite StyleGuide", rewrite(schemaflux.RewriteOptions{StyleGuide: "AP"}), []string{"Follow style: AP"}, nil},
		{"rewrite Changes", rewrite(schemaflux.RewriteOptions{Changes: []string{"shorten", "simplify"}}), []string{"Make these changes: shorten, simplify"}, nil},
		{"rewrite AvoidWords", rewrite(schemaflux.RewriteOptions{AvoidWords: []string{"synergy"}}), []string{"Avoid: synergy"}, nil},
		{"rewrite IncludeWords", rewrite(schemaflux.RewriteOptions{IncludeWords: []string{"revenue"}}), []string{"Include: revenue"}, nil},
		{"translate language", translate(schemaflux.TranslateOptions{}), []string{"Translate to Spanish", "Preserve formatting"}, []string{"From ", "dialect", "Cultural adaptation"}},
		{"translate SourceLanguage", translate(schemaflux.TranslateOptions{SourceLanguage: "English"}), []string{"From English"}, nil},
		{"translate NoPreserveFormatting", translate(schemaflux.TranslateOptions{NoPreserveFormatting: true}), nil, []string{"Preserve formatting"}},
		{"translate CulturalAdaptation", translate(schemaflux.TranslateOptions{CulturalAdaptation: 8}), []string{"Cultural adaptation level: 8/10"}, nil},
		{"translate CulturalAdaptation negative is literal", translate(schemaflux.TranslateOptions{CulturalAdaptation: -1}), []string{"Cultural adaptation level: 0/10"}, nil},
		{"translate Formality", translate(schemaflux.TranslateOptions{Formality: "formal"}), []string{"Formality: formal"}, nil},
		{"translate Glossary", translate(schemaflux.TranslateOptions{Glossary: map[string]string{"cat": "gato", "dog": "perro"}}), []string{"Use glossary: cat=gato, dog=perro"}, nil},
		{"translate Dialect", translate(schemaflux.TranslateOptions{Dialect: "Mexican"}), []string{"Use Mexican dialect"}, nil},
		{"expand default", expand(schemaflux.ExpandOptions{}), []string{"Detail level: 5/10", "Style: balanced"}, []string{"Expand by", "Include relevant examples"}},
		{"expand Factor", expand(schemaflux.ExpandOptions{Factor: 3}), []string{"Expand by 3.0x"}, nil},
		{"expand Factor 2 is the default", expand(schemaflux.ExpandOptions{Factor: 2}), nil, []string{"Expand by"}},
		{"expand DetailLevel", expand(schemaflux.ExpandOptions{DetailLevel: 9}), []string{"Detail level: 9/10"}, nil},
		{"expand IncludeExamples", expand(schemaflux.ExpandOptions{IncludeExamples: true}), []string{"Include relevant examples"}, nil},
		{"expand ElaborateOn", expand(schemaflux.ExpandOptions{ElaborateOn: []string{"costs"}}), []string{"Elaborate on: costs"}, nil},
		{"expand AddContext", expand(schemaflux.ExpandOptions{AddContext: []string{"history"}}), []string{"Add context about: history"}, nil},
		{"expand Style", expand(schemaflux.ExpandOptions{Style: "technical"}), []string{"Style: technical"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			model := newStreamModel(finishing("ok"))
			stream, err := c.open(operationClient(model))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Text(); err != nil {
				t.Fatal(err)
			}
			req := model.last()
			for _, want := range c.has {
				wantText(t, req, want)
			}
			for _, not := range c.not {
				wantNoText(t, req, not)
			}
		})
	}
}

func TestEachStreamIsAskedForWhatItsCommonSettingsSay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		o     schemaflux.Options
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Model", schemaflux.Options{Model: "pinned-model"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"Steering", schemaflux.Options{Steering: "write for a child"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "write for a child") }},
		{"TierSmart", schemaflux.Options{Tier: schemaflux.TierSmart}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.Options{MaxOutputTokens: 66}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 66 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.Options{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.Options{Effort: schemaflux.EffortLow}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort %q", req.Effort)
			}
		}},
		{"ModeCreative", schemaflux.Options{Mode: schemaflux.ModeCreative}, func(t *testing.T, req schemaflux.Request) {
			if req.Temperature < 0.69 || req.Temperature > 0.71 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			eachStreamCall(t, true, func(t *testing.T, call streamCall) {
				model := newStreamModel(finishing("ok"))
				stream, err := call.open(operationClient(model), c.o)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stream.Text(); err != nil {
					t.Fatal(err)
				}
				c.check(t, model.last())
			})
		})
	}
}

func TestAStreamMergesSeveralOptionsValuesFromLeftToRightAndAZeroValueBlanksNothing(t *testing.T) {
	t.Parallel()
	request := func(t *testing.T, opts ...schemaflux.SummarizeOptions) schemaflux.Request {
		t.Helper()
		model := newStreamModel(finishing("ok"))
		stream, err := schemaflux.StreamSummarize(operationClient(model), streamInput, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Text(); err != nil {
			t.Fatal(err)
		}
		return model.last()
	}
	t.Run("none, one and several", func(t *testing.T) {
		t.Parallel()
		wantText(t, request(t), "Style: paragraph")
		wantText(t, request(t, schemaflux.SummarizeOptions{Style: "executive"}), "Style: executive")
		several := request(t,
			schemaflux.SummarizeOptions{Style: "executive", TargetLength: 2},
			schemaflux.SummarizeOptions{Style: "bullet"},
			schemaflux.SummarizeOptions{},
			schemaflux.SummarizeOptions{FocusAreas: []string{"costs"}})
		wantText(t, several, "Style: bullet")
		wantText(t, several, "Target length: 2 sentences")
		wantText(t, several, "Focus on: costs")
		wantNoText(t, several, "executive")
	})
}

// ---------------------------------------------------------------------------
// Every error a stream reports is an *Error.

func TestEveryErrorAStreamReportsIsAnErrorOfTheRootAndTheSameOneEverywhere(t *testing.T) {
	endings := []struct {
		name string
		run  streamScript
		// stop acts on the stream while the reader is waiting, or is nil.
		stop func(cancel context.CancelFunc, s *schemaflux.TextStream)
		kind schemaflux.Kind
	}{
		{"the provider fails midway with an *Error", breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Message: "slow down"}, "half "), nil, schemaflux.ErrRateLimited},
		{"the provider fails midway with a plain error", breakingAfter(errors.New("connection reset"), "half "), nil, schemaflux.ErrUnavailable},
		{"the provider fails before any text", breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized}), nil, schemaflux.ErrUnauthorized},
		{"the answer is truncated", truncatedAfter("half "), nil, schemaflux.ErrTruncated},
		{"Cancel is called", hangingAfter("half "), func(_ context.CancelFunc, s *schemaflux.TextStream) { s.Cancel() }, schemaflux.ErrCanceled},
		{"the context is cancelled", hangingAfter("half "), func(cancel context.CancelFunc, _ *schemaflux.TextStream) { cancel() }, schemaflux.ErrCanceled},
	}
	for _, e := range endings {
		t.Run(e.name, func(t *testing.T) {
			eachStreamCall(t, false, func(t *testing.T, c streamCall) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(operationClient(newStreamModel(e.run)))
					defer cancel()
					stream, err := c.open(ctx, schemaflux.Options{})
					if err != nil {
						t.Fatal(err)
					}
					if e.stop != nil {
						synctest.Wait()
						e.stop(cancel, stream)
					}
					_, readErr := readAll(stream)
					wantErrorKind(t, readErr, e.kind)
					_, textErr := stream.Text()
					text, againErr := stream.Text()
					if text != "" {
						t.Errorf("Text = %q beside an error, want none", text)
					}
					for name, got := range map[string]error{"Err": stream.Err(), "Text": textErr, "Text again": againErr} {
						if got != readErr {
							t.Errorf("%s returned %v (%p), All gave %v (%p): one outcome, one error", name, got, got, readErr, readErr)
						}
					}
					if !stream.Ended() {
						t.Error("Ended is false for a stream that has an error")
					}
				})
			})
		})
	}
}

func TestAProviderThatCannotStreamGivesAnErrorOfKindUnsupportedOnTheFirstRead(t *testing.T) {
	t.Parallel()
	eachStreamCall(t, true, func(t *testing.T, c streamCall) {
		var recorder schemaflux.Recorder
		onlyCompletes := &completeOnly{}
		stream, err := c.open(operationClient(onlyCompletes, recordingTo(&recorder)), schemaflux.Options{})
		if err != nil {
			t.Fatalf("the stream failed to open: %v (the error comes with the first read)", err)
		}
		_, readErr := readAll(stream)
		wantErrorKind(t, readErr, schemaflux.ErrUnsupported)
		if onlyCompletes.calls.Load() != 0 {
			t.Error("the provider was asked to complete: a stream never falls back to a buffered answer")
		}
		rec := onlyRecord(t, &recorder)
		if rec.Op != c.op || !errors.Is(rec.Err, schemaflux.ErrUnsupported) {
			t.Errorf("Record = %q %v", rec.Op, rec.Err)
		}
	})
}

// completeOnly is a provider with no Stream.
type completeOnly struct{ calls atomic.Int32 }

func (p *completeOnly) Name() string                      { return "complete-only" }
func (p *completeOnly) ModelFor(schemaflux.Tier) string   { return "test-model" }
func (p *completeOnly) RetryPolicy() (int, time.Duration) { return -1, 0 }
func (p *completeOnly) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	p.calls.Add(1)
	return schemaflux.Response{Content: "x", FinishReason: "stop"}, nil
}

func TestAClientWithAWrapperOfMwInFrontCannotStreamAndSaysSo(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.Reply("hello")
	wrapped := mw.Chain(&model, mw.Retry())
	var recorder schemaflux.Recorder
	stream, err := schemaflux.StreamSummarize(operationClient(wrapped, recordingTo(&recorder)), streamInput)
	if err != nil {
		t.Fatalf("the stream failed to open: %v (the error comes with the first read)", err)
	}
	_, readErr := readAll(stream)
	wantErrorKind(t, readErr, schemaflux.ErrUnsupported)
	if model.Calls() != 0 {
		t.Errorf("%d requests reached the provider behind the wrapper", model.Calls())
	}
	rec := onlyRecord(t, &recorder)
	if rec.Op != "streamsummarize" || !errors.Is(rec.Err, schemaflux.ErrUnsupported) {
		t.Errorf("Record = %q %v", rec.Op, rec.Err)
	}
}

// ---------------------------------------------------------------------------
// The Record of a stream.

func TestTheRecordIsInTheRecorderWhenTheLoopEndsAndWhenTextReturns(t *testing.T) {
	endings := []struct {
		name string
		run  streamScript
		read func(s *schemaflux.TextStream) error
		kind schemaflux.Kind // zero: no error
	}{
		{"read to the end with All", finishing("a", "b", "c"), func(s *schemaflux.TextStream) error { _, err := readAll(s); return err }, 0},
		{"read to the end with Text", finishing("a", "b", "c"), func(s *schemaflux.TextStream) error { _, err := s.Text(); return err }, 0},
		{"an empty answer is refused, read with Text", finishing(), func(s *schemaflux.TextStream) error { _, err := s.Text(); return err }, schemaflux.ErrInvalidAnswer},
		{"failed midway, read with All", breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}, "half "), func(s *schemaflux.TextStream) error { _, err := readAll(s); return err }, schemaflux.ErrUnavailable},
		{"failed midway, read with Text", breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}, "half "), func(s *schemaflux.TextStream) error { _, err := s.Text(); return err }, schemaflux.ErrUnavailable},
		{"truncated, read with Text", truncatedAfter("half "), func(s *schemaflux.TextStream) error { _, err := s.Text(); return err }, schemaflux.ErrTruncated},
		{"failed before any text", breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrRateLimited}), func(s *schemaflux.TextStream) error { _, err := readAll(s); return err }, schemaflux.ErrRateLimited},
	}
	for _, e := range endings {
		t.Run(e.name, func(t *testing.T) {
			eachStreamCall(t, false, func(t *testing.T, c streamCall) {
				synctest.Test(t, func(t *testing.T) {
					leak := streamLeakCheck(t)
					handled := newHandleCounter()
					model := newStreamModel(e.run)
					stream, err := c.open(operationClient(model, func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder }), schemaflux.Options{})
					if err != nil {
						t.Fatal(err)
					}
					readErr := e.read(stream)
					// No synctest.Wait: the Record has to be there when the read returns.
					if got := handled.calls.Load(); got != 1 {
						t.Fatalf("when the read returned, Handle had run %d times, want 1", got)
					}
					rec := handled.only(t)
					if rec.Op != c.op {
						t.Errorf("Op = %q, want %q", rec.Op, c.op)
					}
					if got := len(model.requests()); got != 1 {
						t.Errorf("%d requests reached the provider, want 1", got)
					}
					if e.kind == 0 {
						if rec.Requests != 1 || rec.Provider != "stream-test" {
							t.Errorf("Requests %d Provider %q, want one request to the stream-test provider", rec.Requests, rec.Provider)
						}
						if readErr != nil || rec.Err != nil {
							t.Errorf("read error %v, Record error %v, want none", readErr, rec.Err)
						}
					} else {
						wantErrorKind(t, readErr, e.kind)
						wantErrorKind(t, rec.Err, e.kind)
					}
					if rec.Requested != schemaflux.ContractPromptOnly || rec.Delivered != schemaflux.ContractPromptOnly {
						t.Errorf("requested %v delivered %v, want prompt only", rec.Requested, rec.Delivered)
					}
					// Reading again, and asking after the end, add nothing.
					stream.Text()
					readAll(stream)
					stream.Err()
					synctest.Wait()
					if got := handled.calls.Load(); got != 1 {
						t.Errorf("Handle ran %d times in all, want once for the stream", got)
					}
					leak()
				})
			})
		})
	}
}

func TestTheRecordOfAStreamThatEndsWithNoReaderIsAddedWhenItsContextEndsOrItIsCancelled(t *testing.T) {
	endings := []struct {
		name string
		run  streamScript
		// end ends the stream while nobody reads it; ctx is the stream's context.
		end  func(cancel context.CancelFunc, s *schemaflux.TextStream)
		kind schemaflux.Kind
		// deadline, when set, is the timeout of the stream's context.
		deadline time.Duration
	}{
		{"its context is cancelled", hangingAfter("a"), func(cancel context.CancelFunc, _ *schemaflux.TextStream) { cancel() }, schemaflux.ErrCanceled, 0},
		{"its deadline passes", hangingAfter("a"), func(context.CancelFunc, *schemaflux.TextStream) {}, schemaflux.ErrTimeout, 5 * time.Second},
		{"Cancel is called", hangingAfter("a"), func(_ context.CancelFunc, s *schemaflux.TextStream) { s.Cancel() }, schemaflux.ErrCanceled, 0},
		{"its context is cancelled with the buffer full", flooding(200), func(cancel context.CancelFunc, _ *schemaflux.TextStream) { cancel() }, schemaflux.ErrCanceled, 0},
		{"Cancel is called with the buffer full", flooding(200), func(_ context.CancelFunc, s *schemaflux.TextStream) { s.Cancel() }, schemaflux.ErrCanceled, 0},
		{"the client's Timeout passes", hangingAfter("a"), func(context.CancelFunc, *schemaflux.TextStream) {}, schemaflux.ErrTimeout, 0},
	}
	for _, e := range endings {
		t.Run(e.name, func(t *testing.T) {
			eachStreamCall(t, false, func(t *testing.T, c streamCall) {
				synctest.Test(t, func(t *testing.T) {
					leak := streamLeakCheck(t)
					handled := newHandleCounter()
					base := operationClient(newStreamModel(e.run), func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder })
					ctx, cancel := context.WithCancel(base)
					defer cancel()
					if e.deadline > 0 {
						var stop context.CancelFunc
						ctx, stop = context.WithTimeout(ctx, e.deadline)
						defer stop()
					}
					stream, err := c.open(ctx, schemaflux.Options{})
					if err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if handled.calls.Load() != 0 || stream.Ended() {
						t.Fatal("the stream ended before anything ended it")
					}
					e.end(cancel, stream)
					// A stream nobody reads ends with its context or Cancel; the
					// clock of the bubble moves on until a deadline passes.
					synctest.Wait()
					time.Sleep(24 * time.Hour)
					synctest.Wait()
					if got := handled.calls.Load(); got != 1 {
						t.Fatalf("Handle ran %d times after the stream was ended, want 1", got)
					}
					rec := handled.only(t)
					if rec.Op != c.op {
						t.Errorf("Record = %q, want %q", rec.Op, c.op)
					}
					wantErrorKind(t, rec.Err, e.kind)
					wantErrorKind(t, stream.Err(), e.kind)
					if !stream.Ended() {
						t.Error("Ended is false for a stream that ended")
					}
					if text, err := stream.Text(); text != "" || err == nil {
						t.Errorf("Text = %q, %v for a stream that was cut off", text, err)
					}
					if got := handled.calls.Load(); got != 1 {
						t.Errorf("Handle ran %d times in all, want once", got)
					}
					leak()
				})
			})
		})
	}
}

// A stream stopped from outside releases a reader waiting on it at once, and does
// not wait for the provider: a provider that ignores its context would hold the
// reader for as long as it likes. The Record is added when the stream's own
// goroutine notices, so it is there once the stream has settled and not
// necessarily when the reader's loop ends.
func TestTheRecordOfAStreamStoppedWhileAReaderWaitsIsAddedOnceTheStreamSettles(t *testing.T) {
	for round := range 50 {
		c := streamCalls[round%len(streamCalls)]
		synctest.Test(t, func(t *testing.T) {
			leak := streamLeakCheck(t)
			handled := newHandleCounter()
			ctx, cancel := context.WithCancel(operationClient(newStreamModel(hangingAfter("a")), func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder }))
			defer cancel()
			stream, err := c.open(ctx, schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			read := make(chan error)
			go func() {
				_, err := readAll(stream)
				read <- err
			}()
			synctest.Wait()
			if round%2 == 0 {
				stream.Cancel()
			} else {
				cancel()
			}
			wantErrorKind(t, <-read, schemaflux.ErrCanceled)
			synctest.Wait()
			if got := handled.calls.Load(); got != 1 {
				t.Fatalf("round %d: Handle ran %d times once the stream had settled, want 1", round, got)
			}
			wantErrorKind(t, handled.only(t).Err, schemaflux.ErrCanceled)
			leak()
		})
	}
}

// A stream's Record counts the request it sent only when the provider's last
// chunk arrived: the call path publishes its call record then and not before, so
// a stream that failed, was cut off or was refused as truncated says no request
// was sent although one was. The test holds the documented meaning of
// Record.Requests.
func TestTheRecordOfAStreamThatFailedCountsTheRequestItSent(t *testing.T) {
	eachStreamCall(t, true, func(t *testing.T, c streamCall) {
		var recorder schemaflux.Recorder
		stream, err := c.open(operationClient(newStreamModel(breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}, "half ")), recordingTo(&recorder)), schemaflux.Options{})
		if err != nil {
			t.Fatal(err)
		}
		stream.Text()
		if rec := onlyRecord(t, &recorder); rec.Requests != 1 || rec.Provider != "stream-test" {
			t.Errorf("Requests %d Provider %q, want 1 and stream-test", rec.Requests, rec.Provider)
		}
	})
}

func TestTheRecordOfAStreamItsReaderLeftIsAddedOnceAndTheRequestIsStopped(t *testing.T) {
	eachStreamCall(t, false, func(t *testing.T, c streamCall) {
		synctest.Test(t, func(t *testing.T) {
			leak := streamLeakCheck(t)
			handled := newHandleCounter()
			var cleanup sync.WaitGroup
			model := newStreamModel(func(ctx context.Context, yield func(schemaflux.Chunk, error) bool) {
				cleanup.Add(1)
				defer cleanup.Done()
				hangingAfter("a", "b")(ctx, yield)
			})
			stream, err := c.open(operationClient(model, func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder }), schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for piece, err := range stream.All() {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, piece)
				break
			}
			synctest.Wait()
			if len(got) != 1 {
				t.Fatalf("read %q", got)
			}
			if handled.calls.Load() != 1 {
				t.Fatalf("Handle ran %d times after the reader left, want 1", handled.calls.Load())
			}
			rec := handled.only(t)
			wantErrorKind(t, rec.Err, schemaflux.ErrCanceled)
			wantErrorKind(t, stream.Err(), schemaflux.ErrCanceled)
			cleanup.Wait()
			leak()
		})
	})
}

func TestADetachedStreamKeepsRunningWhenItsReaderLeavesAndIsRecordedWhenItIsDone(t *testing.T) {
	eachStreamCall(t, false, func(t *testing.T, c streamCall) {
		synctest.Test(t, func(t *testing.T) {
			leak := streamLeakCheck(t)
			handled := newHandleCounter()
			stream, err := c.open(operationClient(newStreamModel(finishing("a", "b", "c")), func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder }), schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			stream.Detach()
			for range stream.All() {
				break
			}
			text, err := stream.Text()
			if err != nil || text != "abc" {
				t.Fatalf("Text = %q, %v: a detached stream keeps the whole answer", text, err)
			}
			synctest.Wait()
			rec := handled.only(t)
			if rec.Err != nil || rec.Op != c.op {
				t.Errorf("Record = %q %v", rec.Op, rec.Err)
			}
			leak()
		})
	})
}

func TestAHandleThatPanicsDoesNotEndTheProgramOrTheStream(t *testing.T) {
	eachStreamCall(t, false, func(t *testing.T, c streamCall) {
		synctest.Test(t, func(t *testing.T) {
			var ran atomic.Int32
			recorder := schemaflux.Recorder{Handle: func(context.Context, schemaflux.Record) {
				ran.Add(1)
				panic("a handler that cannot cope")
			}}
			stream, err := c.open(operationClient(newStreamModel(finishing("a", "b")), recordingTo(&recorder)), schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			text, err := stream.Text()
			if err != nil || text != "ab" {
				t.Fatalf("Text = %q, %v: a Handle that panics is recovered and the stream ends as it would have", text, err)
			}
			synctest.Wait()
			if ran.Load() != 1 {
				t.Errorf("Handle ran %d times, want 1", ran.Load())
			}
		})
	})
}

func TestAStreamThatCouldNotBeOpenedIsARecordWithNoRequest(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		ctx  func(model *streamModel, r *schemaflux.Recorder) context.Context
		opts schemaflux.Options
		kind schemaflux.Kind
	}{
		{"a bad setting", func(m *streamModel, r *schemaflux.Recorder) context.Context {
			return operationClient(m, recordingTo(r))
		}, schemaflux.Options{MaxOutputTokens: -1}, schemaflux.ErrConfiguration},
		{"a tier that is not one", func(m *streamModel, r *schemaflux.Recorder) context.Context {
			return operationClient(m, recordingTo(r))
		}, schemaflux.Options{Tier: 99}, schemaflux.ErrConfiguration},
		{"a context already cancelled", func(m *streamModel, r *schemaflux.Recorder) context.Context {
			return schemaflux.WithClient(cancelled, schemaflux.Client{Provider: m, Recorder: r})
		}, schemaflux.Options{}, schemaflux.ErrCanceled},
	}
	for _, e := range cases {
		t.Run(e.name, func(t *testing.T) {
			t.Parallel()
			eachStreamCall(t, true, func(t *testing.T, c streamCall) {
				model := newStreamModel(finishing("x"))
				var recorder schemaflux.Recorder
				stream, err := c.open(e.ctx(model, &recorder), e.opts)
				if stream != nil {
					t.Fatal("a stream beside an error")
				}
				wantErrorKind(t, err, e.kind)
				if len(model.requests()) != 0 {
					t.Error("a request was sent")
				}
				rec := onlyRecord(t, &recorder)
				if rec.Op != c.op || rec.Requests != 0 || !errors.Is(rec.Err, e.kind) {
					t.Errorf("Record = %q, %d requests, %v", rec.Op, rec.Requests, rec.Err)
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Arguments and options.

func TestAStreamRefusesABadOptionOrArgumentBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call func(ctx context.Context) (*schemaflux.TextStream, error)
		kind schemaflux.Kind
		op   string
	}{
		{"summarize: negative TargetLength", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamSummarize(ctx, "t", schemaflux.SummarizeOptions{TargetLength: -1})
		}, schemaflux.ErrConfiguration, "streamsummarize"},
		{"summarize: unknown LengthUnit", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamSummarize(ctx, "t", schemaflux.SummarizeOptions{LengthUnit: "pages"})
		}, schemaflux.ErrConfiguration, "streamsummarize"},
		{"summarize: negative MaxOutputTokens", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamSummarize(ctx, "t", schemaflux.SummarizeOptions{Options: schemaflux.Options{MaxOutputTokens: -5}})
		}, schemaflux.ErrConfiguration, "streamsummarize"},
		{"rewrite: Formality above 10", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamRewrite(ctx, "t", schemaflux.RewriteOptions{Formality: 11})
		}, schemaflux.ErrConfiguration, "streamrewrite"},
		{"rewrite: negative Formality", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamRewrite(ctx, "t", schemaflux.RewriteOptions{Formality: -2})
		}, schemaflux.ErrConfiguration, "streamrewrite"},
		{"translate: blank language", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamTranslate(ctx, "t", "  \n")
		}, schemaflux.ErrInvalidRequest, "streamtranslate"},
		{"translate: empty language", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamTranslate(ctx, "t", "")
		}, schemaflux.ErrInvalidRequest, "streamtranslate"},
		{"translate: CulturalAdaptation above 10", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamTranslate(ctx, "t", "French", schemaflux.TranslateOptions{CulturalAdaptation: 11})
		}, schemaflux.ErrConfiguration, "streamtranslate"},
		{"expand: Factor below 1", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamExpand(ctx, "t", schemaflux.ExpandOptions{Factor: 0.5})
		}, schemaflux.ErrConfiguration, "streamexpand"},
		{"expand: negative Factor", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamExpand(ctx, "t", schemaflux.ExpandOptions{Factor: -3})
		}, schemaflux.ErrConfiguration, "streamexpand"},
		{"expand: DetailLevel above 10", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamExpand(ctx, "t", schemaflux.ExpandOptions{DetailLevel: 11})
		}, schemaflux.ErrConfiguration, "streamexpand"},
		{"expand: a mode that is not one", func(ctx context.Context) (*schemaflux.TextStream, error) {
			return schemaflux.StreamExpand(ctx, "t", schemaflux.ExpandOptions{Options: schemaflux.Options{Mode: 77}})
		}, schemaflux.ErrConfiguration, "streamexpand"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			model := newStreamModel(finishing("x"))
			var recorder schemaflux.Recorder
			stream, err := c.call(operationClient(model, recordingTo(&recorder)))
			if stream != nil {
				t.Fatal("a stream beside an error")
			}
			wantError(t, err, c.kind, c.op)
			if len(model.requests()) != 0 {
				t.Error("a request was sent")
			}
			rec := onlyRecord(t, &recorder)
			if rec.Op != c.op || rec.Requests != 0 || !errors.Is(rec.Err, c.kind) {
				t.Errorf("Record = %q, %d requests, %v", rec.Op, rec.Requests, rec.Err)
			}
			// The same refusal with no recorder, in the plain state.
			stream, err = c.call(operationClient(model))
			if stream != nil {
				t.Fatal("a stream beside an error with no recorder")
			}
			wantError(t, err, c.kind, c.op)
		})
	}
}

func TestAStreamWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	cases := map[string]func() (*schemaflux.TextStream, error){
		"streamsummarize": func() (*schemaflux.TextStream, error) { return schemaflux.StreamSummarize(nilContext, "t") },
		"streamrewrite":   func() (*schemaflux.TextStream, error) { return schemaflux.StreamRewrite(nilContext, "t") },
		"streamtranslate": func() (*schemaflux.TextStream, error) { return schemaflux.StreamTranslate(nilContext, "t", "French") },
		"streamexpand":    func() (*schemaflux.TextStream, error) { return schemaflux.StreamExpand(nilContext, "t") },
	}
	for op, call := range cases {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			stream, err := call()
			if stream != nil {
				t.Error("a stream beside an error")
			}
			wantError(t, err, schemaflux.ErrInvalidRequest, op)
		})
	}
}

// ---------------------------------------------------------------------------
// A floor.

func TestAStreamUnderAnyFloorAboveTheModelsTextIsRefusedBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	levels := []schemaflux.ContractLevel{
		schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaConstrained, schemaflux.ContractSchemaAndInvariantChecked,
		schemaflux.ContractEvidenceChecked, schemaflux.ContractFullyGoverned,
	}
	for _, level := range levels {
		t.Run(level.String(), func(t *testing.T) {
			t.Parallel()
			eachStreamCall(t, true, func(t *testing.T, c streamCall) {
				model := newStreamModel(finishing("x"))
				model.name = "operation-floor"
				var recorder schemaflux.Recorder
				stream, err := c.open(operationClient(model, underFloor(level), recordingTo(&recorder)), schemaflux.Options{})
				if stream != nil {
					t.Fatal("a stream beside an error")
				}
				wantError(t, err, schemaflux.ErrPolicy, c.op)
				if len(model.requests()) != 0 {
					t.Error("a request was sent")
				}
				rec := onlyRecord(t, &recorder)
				if rec.Op != c.op || rec.Requests != 0 || !errors.Is(rec.Err, schemaflux.ErrPolicy) {
					t.Errorf("Record = %q, %d requests, %v", rec.Op, rec.Requests, rec.Err)
				}
				// With no recorder the refusal is the same.
				stream, err = c.open(operationClient(model, underFloor(level)), schemaflux.Options{})
				if stream != nil {
					t.Fatal("a stream beside an error with no recorder")
				}
				wantError(t, err, schemaflux.ErrPolicy, c.op)
				if len(model.requests()) != 0 {
					t.Error("a request was sent with no recorder")
				}
			})
		})
	}
}

func TestAStreamUnderAFloorOfPromptOnlyIsDeliveredAtThatLevel(t *testing.T) {
	t.Parallel()
	eachStreamCall(t, true, func(t *testing.T, c streamCall) {
		model := newStreamModel(finishing("ab", "cd"))
		model.name = "operation-floor"
		var recorder schemaflux.Recorder
		stream, err := c.open(operationClient(model, underFloor(schemaflux.ContractPromptOnly), recordingTo(&recorder)), schemaflux.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if text, err := stream.Text(); err != nil || text != "abcd" {
			t.Fatalf("Text = %q, %v", text, err)
		}
		rec := onlyRecord(t, &recorder)
		if rec.Err != nil || rec.Delivered != schemaflux.ContractPromptOnly {
			t.Errorf("Record = %v delivered at %v", rec.Err, rec.Delivered)
		}
	})
}

// ---------------------------------------------------------------------------
// An observer, and the payload.

func TestAStreamUnderAnObserverIsOneOperationScopeThatFinishesWhenTheStreamEnds(t *testing.T) {
	// Not parallel: an observer is process state.
	for _, c := range streamCalls {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
				if r.Scope == telemetry.ScopeOperation {
					mu.Lock()
					seen = append(seen, r.Operation)
					mu.Unlock()
				}
			}})
			defer restore()
			started := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			plain := newStreamModel(func(ctx context.Context, yield func(schemaflux.Chunk, error) bool) {
				close(started)
				<-release
				finishing("ab", "cd")(ctx, yield)
			})
			var recorder schemaflux.Recorder
			stream, err := c.open(operationClient(plain, recordingTo(&recorder)), schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			mu.Lock()
			began := len(seen)
			mu.Unlock()
			if began != 0 {
				t.Errorf("the observer saw %v finish while the provider was still running", seen)
			}
			release <- struct{}{}
			if text, err := stream.Text(); err != nil || text != "abcd" {
				t.Fatalf("Text = %q, %v", text, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 1 || seen[0] != c.op {
				t.Errorf("the observer saw %v, want one %s", seen, c.op)
			}
			if onlyRecord(t, &recorder).Op != c.op {
				t.Error("the Record under an observer has the wrong operation")
			}
		})
	}
}

func TestAStreamKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-5d27c1"
	for _, c := range streamCalls {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			failures := map[string]streamScript{
				"the answer carries it":     finishing("the canary ", canary),
				"the vendor message has it": breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "refused", Err: errors.New("vendor said " + canary)}, "half "),
				"a truncated answer":        truncatedAfter("half ", canary),
			}
			for name, run := range failures {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					requireNoPayloadOn(t, canary, newStreamModel(run), false, func(ctx context.Context) error {
						var stream *schemaflux.TextStream
						var err error
						switch c.op {
						case "streamsummarize":
							stream, err = schemaflux.StreamSummarize(ctx, "input "+canary, schemaflux.SummarizeOptions{Options: schemaflux.Options{Steering: "steer " + canary}, FocusAreas: []string{canary}})
						case "streamrewrite":
							stream, err = schemaflux.StreamRewrite(ctx, "input "+canary, schemaflux.RewriteOptions{Options: schemaflux.Options{Steering: "steer " + canary}, Changes: []string{canary}})
						case "streamtranslate":
							stream, err = schemaflux.StreamTranslate(ctx, "input "+canary, "French", schemaflux.TranslateOptions{Options: schemaflux.Options{Steering: "steer " + canary}, Glossary: map[string]string{canary: canary}})
						default:
							stream, err = schemaflux.StreamExpand(ctx, "input "+canary, schemaflux.ExpandOptions{Options: schemaflux.Options{Steering: "steer " + canary}, ElaborateOn: []string{canary}})
						}
						if err != nil {
							return err
						}
						_, err = stream.Text()
						return err
					})
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The type.

func TestTextStreamExportsTheSixMethodsOfTheSpecificationAndNoMore(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[*schemaflux.TextStream]()
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	want := []string{"All", "Cancel", "Detach", "Ended", "Err", "Text"}
	if !slices.Equal(got, want) {
		t.Errorf("TextStream exports %v, want %v: the call path's stream has a wider set, and it is not this type", got, want)
	}
	if reflect.TypeFor[schemaflux.TextStream]().Kind() != reflect.Struct {
		t.Error("TextStream is not a struct of the root")
	}
}

func TestAStreamRangedByAReaderThatPanicsStopsTheRequest(t *testing.T) {
	eachStreamCall(t, false, func(t *testing.T, c streamCall) {
		synctest.Test(t, func(t *testing.T) {
			leak := streamLeakCheck(t)
			handled := newHandleCounter()
			stream, err := c.open(operationClient(newStreamModel(hangingAfter("a", "b")), func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder }), schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("the panic did not reach the reader")
					}
				}()
				for range stream.All() {
					panic("the reader gave up")
				}
			}()
			synctest.Wait()
			wantErrorKind(t, stream.Err(), schemaflux.ErrCanceled)
			if handled.calls.Load() != 1 {
				t.Errorf("Handle ran %d times, want 1", handled.calls.Load())
			}
			leak()
		})
	})
}

// ---------------------------------------------------------------------------
// Concurrency. Each of these runs its scenario fifty times.

func TestManyStreamsAtOnceEachAddOneRecordAndLeaveNothingBehind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leak := streamLeakCheck(t)
		handled := newHandleCounter()
		const streams = 50
		model := newStreamModel(finishing("a", "b", "c", "d"))
		ctx := operationClient(model, func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder })
		var wg sync.WaitGroup
		for i := range streams {
			c := streamCalls[i%len(streamCalls)]
			wg.Go(func() {
				stream, err := c.open(ctx, schemaflux.Options{})
				if err != nil {
					t.Error(err)
					return
				}
				switch i % 3 {
				case 0:
					if text, err := stream.Text(); err != nil || text != "abcd" {
						t.Errorf("Text = %q, %v", text, err)
					}
				case 1:
					if _, err := readAll(stream); err != nil {
						t.Error(err)
					}
				default:
					for range stream.All() {
						break
					}
					stream.Cancel()
				}
			})
		}
		wg.Wait()
		synctest.Wait()
		if got := handled.calls.Load(); got != streams {
			t.Errorf("Handle ran %d times for %d streams", got, streams)
		}
		leak()
	})
}

func TestACancelRacingTheEndOfAStreamGivesOneRecordAndOneOutcome(t *testing.T) {
	for round := range 50 {
		synctest.Test(t, func(t *testing.T) {
			leak := streamLeakCheck(t)
			handled := newHandleCounter()
			c := streamCalls[round%len(streamCalls)]
			stream, err := c.open(operationClient(newStreamModel(finishing("a", "b", "c")), func(cl *schemaflux.Client) { cl.Recorder = &handled.recorder }), schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			wg.Go(func() { stream.Cancel() })
			wg.Go(func() { stream.Text() })
			wg.Go(func() { stream.Ended(); stream.Err() })
			wg.Wait()
			synctest.Wait()
			if got := handled.calls.Load(); got != 1 {
				t.Fatalf("round %d: Handle ran %d times, want 1", round, got)
			}
			first := stream.Err()
			if first != nil {
				wantErrorKind(t, first, schemaflux.ErrCanceled)
			}
			if again := stream.Err(); again != first {
				t.Errorf("round %d: Err changed from %v to %v", round, first, again)
			}
			leak()
		})
	}
}

func TestSeveralReadersOfOneStreamAreAllToldTheSameOutcome(t *testing.T) {
	for round := range 50 {
		synctest.Test(t, func(t *testing.T) {
			leak := streamLeakCheck(t)
			c := streamCalls[round%len(streamCalls)]
			stream, err := c.open(operationClient(newStreamModel(breakingAfter(&schemaflux.Error{Kind: schemaflux.ErrRateLimited}, "a", "b"))), schemaflux.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var outcomes []error
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					_, readErr := readAll(stream)
					mu.Lock()
					outcomes = append(outcomes, readErr)
					mu.Unlock()
				})
			}
			wg.Wait()
			for _, got := range outcomes {
				wantErrorKind(t, got, schemaflux.ErrRateLimited)
				if got != stream.Err() {
					t.Errorf("round %d: a reader was told %v, Err says %v", round, got, stream.Err())
				}
			}
			leak()
		})
	}
}

func TestAStreamOnAContextAlreadyOverIsAnErrorOfTheRootInThePlainStateToo(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for name, base := range map[string]context.Context{"cancelled": cancelled, "past its deadline": expired} {
		kind := schemaflux.ErrCanceled
		if name != "cancelled" {
			kind = schemaflux.ErrTimeout
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			eachStreamCall(t, true, func(t *testing.T, c streamCall) {
				model := newStreamModel(finishing("x"))
				stream, err := c.open(schemaflux.WithClient(base, schemaflux.Client{Provider: model}), schemaflux.Options{})
				if stream != nil {
					t.Fatal("a stream beside an error")
				}
				wantErrorKind(t, err, kind)
				if len(model.requests()) != 0 {
					t.Error("a request was sent")
				}
			})
		})
	}
}

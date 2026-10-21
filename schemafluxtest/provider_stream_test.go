package schemafluxtest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The fakes are the provider package's optional interfaces.
var (
	_ provider.Streamer = (*schemafluxtest.Provider)(nil)
	_ provider.Retrier  = (*schemafluxtest.Provider)(nil)
	_ provider.Retrier  = (*schemafluxtest.Decider)(nil)
	_ provider.Decider  = (*schemafluxtest.Decider)(nil)
)

// drain ranges over a stream and returns its chunks and the error that ended
// it, if one did.
func drain(p *schemafluxtest.Provider, req schemaflux.Request) ([]schemaflux.Chunk, error) {
	var chunks []schemaflux.Chunk
	for chunk, err := range p.Stream(context.Background(), req) {
		if err != nil {
			return chunks, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func text(chunks []schemaflux.Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Delta)
	}
	return b.String()
}

func TestStreamDeliversTheScriptedAnswerInPieces(t *testing.T) {
	t.Parallel()

	t.Run("the pieces add up to the answer", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply(`{"summary":"The invoice is due in thirty days."}`)
		chunks, err := drain(&p, schemaflux.Request{})
		if err != nil || text(chunks) != `{"summary":"The invoice is due in thirty days."}` {
			t.Fatalf("text = %q, err = %v", text(chunks), err)
		}
	})
	t.Run("an answer comes in several pieces of at most four runes", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("0123456789")
		chunks, _ := drain(&p, schemaflux.Request{})
		if len(chunks) != 4 {
			t.Fatalf("%d chunks, want three pieces and the last", len(chunks))
		}
		for _, c := range chunks[:3] {
			if n := utf8.RuneCountInString(c.Delta); n == 0 || n > 4 {
				t.Errorf("piece %q has %d runes", c.Delta, n)
			}
		}
	})
	t.Run("a piece never splits a character", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("naïve 日本語 \U0001F600\U0001F600\U0001F600")
		chunks, _ := drain(&p, schemaflux.Request{})
		for _, c := range chunks {
			if !utf8.ValidString(c.Delta) {
				t.Fatalf("piece %q is not valid UTF-8", c.Delta)
			}
		}
		if text(chunks) != "naïve 日本語 \U0001F600\U0001F600\U0001F600" {
			t.Fatalf("text = %q", text(chunks))
		}
	})
	t.Run("the last chunk is Done and holds the whole response", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("hello world")
		p.SetUsage(schemaflux.Usage{Input: 5, Output: 3})
		chunks, _ := drain(&p, schemaflux.Request{Model: "m"})
		last := chunks[len(chunks)-1]
		if !last.Done || last.Delta != "" || last.Response.Content != "hello world" || last.Response.Usage.Input != 5 ||
			last.Response.Model != "m" || last.Response.FinishReason != "stop" {
			t.Fatalf("last chunk = %+v", last)
		}
	})
	t.Run("no other chunk is Done or carries a response", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("hello world")
		chunks, _ := drain(&p, schemaflux.Request{})
		for _, c := range chunks[:len(chunks)-1] {
			if c.Done || c.Response.Content != "" {
				t.Fatalf("chunk %+v", c)
			}
		}
	})
	t.Run("an answer with no text is the last chunk alone", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("")
		chunks, err := drain(&p, schemaflux.Request{})
		if err != nil || len(chunks) != 1 || !chunks[0].Done {
			t.Fatalf("chunks = %+v, err = %v", chunks, err)
		}
	})
	t.Run("an answer of exactly four runes is one piece and the last", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("abcd")
		chunks, _ := drain(&p, schemaflux.Request{})
		if len(chunks) != 2 || chunks[0].Delta != "abcd" {
			t.Fatalf("chunks = %+v", chunks)
		}
	})
	t.Run("a scripted failure is yielded in place of the chunks", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Fail(schemaflux.ErrUnavailable)
		chunks, err := drain(&p, schemaflux.Request{})
		if len(chunks) != 0 || !errors.Is(err, schemaflux.ErrUnavailable) {
			t.Fatalf("chunks = %+v, err = %v", chunks, err)
		}
	})
	t.Run("the failure ends the sequence", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Fail(schemaflux.ErrBusy)
		n := 0
		for range p.Stream(context.Background(), schemaflux.Request{}) {
			n++
		}
		if n != 1 {
			t.Fatalf("the sequence yielded %d times, want the failure once", n)
		}
	})
	t.Run("a context that has ended is yielded as its error", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var got error
		for _, err := range p.Stream(ctx, schemaflux.Request{}) {
			got = err
		}
		if !errors.Is(got, context.Canceled) {
			t.Fatalf("err = %v", got)
		}
	})
	t.Run("a claim answers a streamed request", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("summar", "a short summary")
		chunks, _ := drain(&p, in("You summarize.", ""))
		if text(chunks) != "a short summary" {
			t.Fatalf("text = %q", text(chunks))
		}
	})
	t.Run("a Respond function answers a streamed request, with its response in the last chunk", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "cut off", FinishReason: "length"}, nil
		})
		chunks, _ := drain(&p, schemaflux.Request{})
		if got := chunks[len(chunks)-1].Response.FinishReason; got != "length" || text(chunks) != "cut off" {
			t.Fatalf("FinishReason = %q, text = %q", got, text(chunks))
		}
	})
	t.Run("a shaped answer can be streamed", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		chunks, err := drain(&p, schemaflux.Request{System: `Return a JSON object with: {"verdict": "yes"}`})
		if err != nil || !strings.Contains(text(chunks), "verdict") {
			t.Fatalf("text = %q, err = %v", text(chunks), err)
		}
	})
}

func TestStreamRecordsItsRequestsLikeCompleteDoes(t *testing.T) {
	t.Parallel()

	t.Run("the request is counted when the sequence is ranged over, not before", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		seq := p.Stream(context.Background(), schemaflux.Request{User: "streamed"})
		if p.Calls() != 0 {
			t.Fatalf("Calls() = %d before the sequence started", p.Calls())
		}
		for range seq {
		}
		if p.Calls() != 1 || p.Last().User != "streamed" {
			t.Fatalf("Calls() = %d, Last().User = %q", p.Calls(), p.Last().User)
		}
	})
	t.Run("each range sends a request of its own", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		seq := p.Stream(context.Background(), schemaflux.Request{})
		for range seq {
		}
		for range seq {
		}
		if p.Calls() != 2 {
			t.Fatalf("Calls() = %d", p.Calls())
		}
	})
	t.Run("a caller that stops early has still sent its request", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("a long enough answer to need several pieces")
		for range p.Stream(context.Background(), schemaflux.Request{}) {
			break
		}
		if p.Calls() != 1 {
			t.Fatalf("Calls() = %d", p.Calls())
		}
	})
	t.Run("a streamed and a whole request share one script", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("first", "second")
		chunks, _ := drain(&p, schemaflux.Request{})
		if text(chunks) != "first" || body(t, &p, schemaflux.Request{}) != "second" {
			t.Fatal("the two did not take successive answers")
		}
	})
	t.Run("a FailThen fails the first stream and answers the second", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.FailThen(1, schemaflux.ErrRateLimited, "ok")
		_, err := drain(&p, schemaflux.Request{})
		chunks, err2 := drain(&p, schemaflux.Request{})
		if !errors.Is(err, schemaflux.ErrRateLimited) || err2 != nil || text(chunks) != "ok" {
			t.Fatalf("err = %v, err2 = %v, text = %q", err, err2, text(chunks))
		}
	})
	t.Run("it is the provider's own Streamer", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		var s provider.Streamer = &p
		var chunks int
		for range s.Stream(context.Background(), schemaflux.Request{}) {
			chunks++
		}
		if chunks != 2 { // "{}" is two runes: one piece and the last
			t.Fatalf("%d chunks", chunks)
		}
	})
}

func TestStreamWaitsForTheDelayOnTheRequestsContext(t *testing.T) {
	t.Parallel()

	inBubble(t, "the first chunk comes after the delay", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Reply("answer")
		p.Delay(2 * time.Second)
		begin := time.Now()
		got := make(chan time.Duration, 1)
		go func() {
			for range p.Stream(context.Background(), schemaflux.Request{}) {
				got <- time.Since(begin)
				return
			}
		}()
		synctest.Wait()
		select {
		case <-got:
			t.Fatal("a chunk arrived before the delay")
		default:
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if elapsed := <-got; elapsed != 2*time.Second {
			t.Fatalf("first chunk after %v", elapsed)
		}
	})
	inBubble(t, "a cancelled context ends the wait with its error", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Delay(time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		var got error
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, err := range p.Stream(ctx, schemaflux.Request{}) {
				got = err
			}
		}()
		synctest.Wait()
		cancel()
		<-done
		if !errors.Is(got, context.Canceled) {
			t.Fatalf("err = %v", got)
		}
	})
}

// A fake given to a client is the provider the call path streams through: the
// root's adapter finds Stream by its method set, which has to name the root's
// Chunk. A fake whose Stream returned another type of chunk would be found by
// nothing, and a streamed operation on it would report that the provider cannot
// stream.
func TestAStreamingFakeInstalledThroughWithClientIsStreamedByTheCallPath(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	p.Reply("the answer in pieces")
	ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: &p})
	_, cfg := ops.PinExec(ctx)
	if cfg == nil || cfg.Provider == nil {
		t.Fatal("the client pinned no provider")
	}
	streaming, ok := cfg.Provider.(llm.StreamingProvider)
	if !ok {
		t.Fatalf("the client's provider %T does not stream", cfg.Provider)
	}
	var text strings.Builder
	var pieces int
	var done llm.StreamChunk
	for chunk, err := range streaming.CompleteStream(ctx, llm.CompletionRequest{UserPrompt: "q"}) {
		if err != nil {
			if types.KindOf(err) == types.KindUnsupportedCapability {
				t.Fatalf("the call path reports that the fake cannot stream: %v", err)
			}
			t.Fatalf("the stream failed: %v", err)
		}
		if chunk.Done {
			done = chunk
			continue
		}
		pieces++
		text.WriteString(chunk.Delta)
	}
	if text.String() != "the answer in pieces" || pieces < 2 {
		t.Errorf("pieces = %d, text = %q", pieces, text.String())
	}
	if !done.Done || done.Response.Content != "the answer in pieces" {
		t.Errorf("the last chunk = %+v, want Done with the whole answer", done)
	}
	if p.Calls() != 1 {
		t.Errorf("the fake received %d requests, want 1", p.Calls())
	}
}

func TestAFailureOfAStreamingFakeReachesTheCallPathAsItsOwnKind(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Message: "slow down"})
	ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: &p})
	_, cfg := ops.PinExec(ctx)
	streaming := cfg.Provider.(llm.StreamingProvider)
	var got error
	for _, err := range streaming.CompleteStream(ctx, llm.CompletionRequest{UserPrompt: "q"}) {
		got = err
	}
	if got == nil || types.KindOf(got) != types.KindRateLimited {
		t.Fatalf("err = %v (kind %v), want a rate limit and not the cannot-stream error", got, types.KindOf(got))
	}
}

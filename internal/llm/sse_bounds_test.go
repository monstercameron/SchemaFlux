package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The stream reader is bounded in the size of one event, whether the event is
// one long line or many short ones. Only the length of a line was bounded, so a
// server that sent "data:" lines and never the blank line that ends an event grew
// the collected lines without limit.

func streamOutcome(t *testing.T, write func(w http.ResponseWriter, f http.Flusher)) (deltas []string, done StreamChunk, err error) {
	t.Helper()
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) { write(w, f) })
	provider := newTestOpenAIProvider(t, server.URL)
	return collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
}

func TestStreamEventLongerThanTheCeilingIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		write func(w http.ResponseWriter, f http.Flusher)
	}{
		{"one line past the ceiling", func(w http.ResponseWriter, f http.Flusher) {
			_, _ = w.Write([]byte("data: " + strings.Repeat("a", maxStreamEventBytes+10) + "\n\n"))
		}},
		{"a comment line past the ceiling, then a complete event", func(w http.ResponseWriter, f http.Flusher) {
			_, _ = w.Write([]byte(": " + strings.Repeat("a", maxStreamEventBytes+10) + "\n"))
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"))
		}},
		{"many short lines and no blank line", func(w http.ResponseWriter, f http.Flusher) {
			line := "data: " + strings.Repeat("b", 60<<10) + "\n"
			for sent := 0; sent <= maxStreamEventBytes+len(line); sent += len(line) {
				if _, err := w.Write([]byte(line)); err != nil {
					return
				}
			}
		}},
		{"many lines whose total passes the ceiling before a blank line", func(w http.ResponseWriter, f http.Flusher) {
			line := "data: " + strings.Repeat("c", 1<<20) + "\n"
			for i := 0; i < 6; i++ {
				if _, err := w.Write([]byte(line)); err != nil {
					return
				}
			}
			_, _ = w.Write([]byte("\n"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deltas, done, err := streamOutcome(t, tc.write)
			if err == nil {
				t.Fatal("an event past the ceiling was accepted")
			}
			if done.Done || len(deltas) != 0 {
				t.Errorf("got deltas %v and done %v beside the error", deltas, done.Done)
			}
			if got := Classify(err); got != types.KindMalformedOutput {
				t.Errorf("Classify = %v, want %v (%v)", got, types.KindMalformedOutput, err)
			}
			if (&types.OperationError{Kind: Classify(err)}).Retryable() {
				t.Error("the same event is as long on retry")
			}
		})
	}
}

// An event within the ceiling is read, however it is split into lines.
func TestStreamEventWithinTheCeilingIsRead(t *testing.T) {
	text := strings.Repeat("x", 3<<20)
	deltas, done, err := streamOutcome(t, func(w http.ResponseWriter, f http.Flusher) {
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"status":"completed",` +
			`"output":[{"type":"message","content":[{"type":"output_text","text":"` + text + `"}]}]}}` + "\n\n"))
	})
	if err != nil {
		t.Fatalf("a 3 MiB event was refused: %v", err)
	}
	if !done.Done || len(done.Response.Content) != len(text) || len(deltas) != 0 {
		t.Errorf("done = %v with %d bytes, deltas %d", done.Done, len(done.Response.Content), len(deltas))
	}
}

func TestStreamFramingVariants(t *testing.T) {
	const completed = `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"whole"}]}]}}`
	const delta = `{"type":"response.output_text.delta","delta":"par"}`
	cases := []struct {
		name       string
		body       string
		wantDeltas string
		wantText   string
		wantErr    error
	}{
		{"CRLF framing", "data: " + delta + "\r\n\r\ndata: " + completed + "\r\n\r\n", "par", "whole", nil},
		{"event and id lines are ignored", "event: response.output_text.delta\nid: 1\ndata: " + delta + "\n\nevent: response.completed\ndata: " + completed + "\n\n", "par", "whole", nil},
		{"comment lines are ignored", ": keep-alive\n\ndata: " + delta + "\n: another\n\ndata: " + completed + "\n\n", "par", "whole", nil},
		{"data without a space after the colon", "data:" + delta + "\n\ndata:" + completed + "\n\n", "par", "whole", nil},
		{"a payload split over two data lines", "data: {\"type\":\"response.completed\",\n" +
			"data: \"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"whole\"}]}]}}\n\n", "", "whole", nil},
		{"the DONE sentinel after the terminal event is not reached", "data: " + completed + "\n\ndata: [DONE]\n\n", "", "whole", nil},
		{"a final event with no blank line after it", "data: " + completed, "", "whole", nil},
		{"no terminal event", "data: " + delta + "\n\n", "par", "", ErrStreamIncomplete},
		{"a multi-byte character", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"日本語\"}\n\ndata: " + completed + "\n\n", "日本語", "whole", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deltas, done, err := streamOutcome(t, func(w http.ResponseWriter, f http.Flusher) {
				_, _ = w.Write([]byte(tc.body))
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got := strings.Join(deltas, ""); got != tc.wantDeltas {
				t.Errorf("deltas = %q, want %q", got, tc.wantDeltas)
			}
			if tc.wantErr == nil && done.Response.Content != tc.wantText {
				t.Errorf("content = %q, want %q", done.Response.Content, tc.wantText)
			}
		})
	}
}

// A multi-byte character split across two writes is reassembled before it is
// decoded, since lines are assembled as bytes.
func TestStreamMultiByteCharacterSplitAcrossWrites(t *testing.T) {
	line := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"é\"}\n\n")
	cut := strings.Index(string(line), "é") + 1 // between the two bytes of é
	deltas, _, err := streamOutcome(t, func(w http.ResponseWriter, f http.Flusher) {
		_, _ = w.Write(line[:cut])
		f.Flush()
		_, _ = w.Write(line[cut:])
		f.Flush()
	})
	if !errors.Is(err, ErrStreamIncomplete) {
		t.Fatalf("err = %v, want ErrStreamIncomplete (no terminal event was sent)", err)
	}
	if got := strings.Join(deltas, ""); got != "é" {
		t.Errorf("deltas = %q, want é", got)
	}
}

// The "error" event carries its code and message at the top level of the event.
func TestStreamErrorEventIsTyped(t *testing.T) {
	cases := []struct {
		name string
		body string
		kind types.ErrorKind
	}{
		{"rate limit", `{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`, types.KindRateLimited},
		{"server error", `{"type":"error","code":"server_error","message":"broke"}`, types.KindProviderUnavailable},
		{"invalid request", `{"type":"error","code":"invalid_prompt","message":"bad"}`, types.KindInvalidRequest},
		{"no code", `{"type":"error","message":"odd"}`, types.KindProviderUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, done, err := streamOutcome(t, func(w http.ResponseWriter, f http.Flusher) {
				_, _ = w.Write([]byte("data: " + tc.body + "\n\n"))
			})
			if done.Done {
				t.Fatal("an error event produced a Done chunk")
			}
			if got := Classify(err); got != tc.kind {
				t.Errorf("Classify = %v, want %v (%v)", got, tc.kind, err)
			}
		})
	}
}

// A context cancelled before the first byte is reported as the cancellation.
func TestStreamCancelledBeforeTheFirstByte(t *testing.T) {
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		<-r.Context().Done()
	})
	provider := newTestOpenAIProvider(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, done, err := collectStream(provider.CompleteStream(ctx, CompletionRequest{Model: "gpt-5.6-luna"}))
	if err == nil || done.Done {
		t.Fatalf("a stream under a cancelled context returned %v, done %v", err, done.Done)
	}
	if got := Classify(err); got != types.KindCanceled {
		t.Errorf("Classify = %v, want %v (%v)", got, types.KindCanceled, err)
	}
}

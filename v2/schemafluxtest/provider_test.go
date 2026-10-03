package schemafluxtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The Provider is the root's contract, and it keeps the optional interfaces the
// library looks for. A compile error here is the first failure.
var (
	_ schemaflux.Provider = (*schemafluxtest.Provider)(nil)
	_ schemaflux.Provider = (*schemafluxtest.Decider)(nil)
	_ interface {
		RetryPolicy() (int, time.Duration)
	} = (*schemafluxtest.Provider)(nil)
	_ interface {
		ModelFor(schemaflux.Tier) string
	} = (*schemafluxtest.Provider)(nil)
	_ interface {
		DecisionLimits() schemaflux.DecisionLimits
	} = (*schemafluxtest.Decider)(nil)
)

// ask makes one request and returns what came back.
func ask(p schemaflux.Provider, req schemaflux.Request) (schemaflux.Response, error) {
	return p.Complete(context.Background(), req)
}

// body asks and returns the content, failing the test on an error.
func body(t *testing.T, p schemaflux.Provider, req schemaflux.Request) string {
	t.Helper()
	resp, err := ask(p, req)
	if err != nil {
		t.Fatalf("Complete returned %v", err)
	}
	return resp.Content
}

// kindOf is the kind of err, or 0 when it has none.
func kindOf(err error) schemaflux.Kind {
	var e *schemaflux.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// messageOf is the message of err's *Error.
func messageOf(t *testing.T, err error) string {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not an *Error", err)
	}
	return e.Message
}

// in is a request whose system prompt is system and whose schema name is name.
func in(system, name string) schemaflux.Request {
	return schemaflux.Request{System: system, SchemaName: name, User: "the caller's data"}
}

// outcome is what one request is expected to get: a body, or an error of a kind.
type outcome struct {
	body string
	kind schemaflux.Kind
}

// scriptCase is a script and the requests made on it with what each gets.
type scriptCase struct {
	name  string
	setup func(p *schemafluxtest.Provider)
	reqs  []schemaflux.Request
	want  []outcome
}

func runScriptCases(t *testing.T, cases []scriptCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var p schemafluxtest.Provider
			c.setup(&p)
			before := p.Calls()
			if len(c.reqs) != len(c.want) {
				t.Fatalf("case holds %d requests and %d outcomes", len(c.reqs), len(c.want))
			}
			for i, req := range c.reqs {
				resp, err := ask(&p, req)
				switch want := c.want[i]; {
				case want.kind != 0:
					if err == nil || !errors.Is(err, want.kind) {
						t.Fatalf("request %d: err = %v, want kind %q", i+1, err, want.kind)
					}
				case err != nil:
					t.Fatalf("request %d: unexpected error %v", i+1, err)
				case resp.Content != want.body:
					t.Fatalf("request %d: body = %q, want %q", i+1, resp.Content, want.body)
				}
			}
			if p.Calls() != before+len(c.reqs) {
				t.Fatalf("Calls() = %d after %d requests", p.Calls(), before+len(c.reqs))
			}
		})
	}
}

var one = []schemaflux.Request{{}}

func many(n int) []schemaflux.Request { return make([]schemaflux.Request, n) }

func TestAZeroProviderAnswersAnEmptyObjectToEveryRequest(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	for i := range 12 {
		resp, err := ask(&p, schemaflux.Request{User: strings.Repeat("x", i)})
		if err != nil || resp.Content != "{}" {
			t.Fatalf("request %d: %q, %v", i+1, resp.Content, err)
		}
	}
	if p.Calls() != 12 {
		t.Fatalf("Calls() = %d, want 12", p.Calls())
	}
}

func TestReplyAnswersInOrderAndRepeatsTheLast(t *testing.T) {
	t.Parallel()
	runScriptCases(t, []scriptCase{
		{"with no body it answers an empty object", func(p *schemafluxtest.Provider) { p.Reply() },
			many(2), []outcome{{"{}", 0}, {"{}", 0}}},
		{"one body repeats for every request", func(p *schemafluxtest.Provider) { p.Reply(`{"a":1}`) },
			many(4), []outcome{{`{"a":1}`, 0}, {`{"a":1}`, 0}, {`{"a":1}`, 0}, {`{"a":1}`, 0}}},
		{"two bodies then the last repeats", func(p *schemafluxtest.Provider) { p.Reply("one", "two") },
			many(4), []outcome{{"one", 0}, {"two", 0}, {"two", 0}, {"two", 0}}},
		{"three bodies are given in order", func(p *schemafluxtest.Provider) { p.Reply("a", "b", "c") },
			many(3), []outcome{{"a", 0}, {"b", 0}, {"c", 0}}},
		{"an empty string is a body", func(p *schemafluxtest.Provider) { p.Reply("", "x") },
			many(3), []outcome{{"", 0}, {"x", 0}, {"x", 0}}},
		{"a body with a newline and a quote is given unchanged", func(p *schemafluxtest.Provider) { p.Reply("{\"a\":\"b\\n\"}\n") },
			one, []outcome{{"{\"a\":\"b\\n\"}\n", 0}}},
		{"a body with multibyte text is given unchanged", func(p *schemafluxtest.Provider) { p.Reply("naïve 日本語") },
			one, []outcome{{"naïve 日本語", 0}}},
		{"a second Reply replaces the first and starts again", func(p *schemafluxtest.Provider) {
			p.Reply("old1", "old2")
			_, _ = ask(p, schemaflux.Request{})
			p.Reply("new1", "new2")
		}, many(3), []outcome{{"new1", 0}, {"new2", 0}, {"new2", 0}}},
		{"it replaces a scripted failure", func(p *schemafluxtest.Provider) {
			p.Fail(errors.New("down"))
			p.Reply("up")
		}, many(2), []outcome{{"up", 0}, {"up", 0}}},
		{"it replaces a Respond function", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return schemaflux.Response{Content: "from the function"}, nil
			})
			p.Reply("from the list")
		}, one, []outcome{{"from the list", 0}}},
		{"it replaces a shaped answer", func(p *schemafluxtest.Provider) {
			p.Shape()
			p.Reply("plain")
		}, one, []outcome{{"plain", 0}}},
		{"it leaves a ReplyWhen claim in force", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("invoice", "claimed")
			p.Reply("default")
		}, []schemaflux.Request{in("", "Invoice"), in("", "Other")}, []outcome{{"claimed", 0}, {"default", 0}}},
	})
}

func TestReplyWhenAnswersTheRequestsAboutAnOperation(t *testing.T) {
	t.Parallel()
	runScriptCases(t, []scriptCase{
		{"it matches the schema name", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("invoice", "I")
			p.Reply("D")
		}, []schemaflux.Request{in("", "Invoice")}, []outcome{{"I", 0}}},
		{"it matches the system prompt", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("summar", "S")
			p.Reply("D")
		}, []schemaflux.Request{in("You summarize the text.", "")}, []outcome{{"S", 0}}},
		{"it matches without regard to case in the request", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("invoice", "I")
			p.Reply("D")
		}, []schemaflux.Request{in("", "INVOICE"), in("YOU SUMMARIZE AN INVOICE", "")}, []outcome{{"I", 0}, {"I", 0}}},
		{"it matches without regard to case in the text given", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("InVoIcE", "I")
			p.Reply("D")
		}, []schemaflux.Request{in("", "invoice")}, []outcome{{"I", 0}}},
		{"the first claim that matches answers", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("invoice total", "specific")
			p.ReplyWhen("invoice", "general")
			p.ReplyWhen("invoice total", "unreachable")
		}, []schemaflux.Request{in("", "InvoiceTotal"), in("", "Invoice total"), in("", "InvoiceHeader")},
			[]outcome{{"general", 0}, {"specific", 0}, {"general", 0}}},
		{"a request no claim matches gets the default", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("invoice", "I")
			p.Reply("D1", "D2")
		}, []schemaflux.Request{in("", "Other"), in("", "Invoice"), in("", "Other")}, []outcome{{"D1", 0}, {"I", 0}, {"D2", 0}}},
		{"a claim gives its bodies in order and repeats the last", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("invoice", "1", "2")
		}, []schemaflux.Request{in("", "Invoice"), in("", "Invoice"), in("", "Invoice")}, []outcome{{"1", 0}, {"2", 0}, {"2", 0}}},
		{"a claim counts only the requests that matched it", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("a", "a1", "a2")
			p.ReplyWhen("b", "b1", "b2")
		}, []schemaflux.Request{in("", "b"), in("", "a"), in("", "b"), in("", "a")}, []outcome{{"b1", 0}, {"a1", 0}, {"b2", 0}, {"a2", 0}}},
		{"the user message is not matched", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("secret", "claimed")
			p.Reply("default")
		}, []schemaflux.Request{{User: "a secret"}}, []outcome{{"default", 0}}},
		{"with no body a claim answers an empty object", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("x")
		}, []schemaflux.Request{in("", "x")}, []outcome{{"{}", 0}}},
		{"a claim wins over a Respond function", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("x", "claimed")
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return schemaflux.Response{Content: "function"}, nil
			})
		}, []schemaflux.Request{in("", "x"), in("", "y")}, []outcome{{"claimed", 0}, {"function", 0}}},
		{"a claim wins over a scripted failure", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("x", "claimed")
			p.Fail(schemaflux.ErrUnavailable)
		}, []schemaflux.Request{in("", "x"), in("", "y")}, []outcome{{"claimed", 0}, {"", schemaflux.ErrUnavailable}}},
		{"a request no claim covers fails when nothing else is scripted", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("x", "claimed")
		}, []schemaflux.Request{in("", "x"), in("", "y")}, []outcome{{"claimed", 0}, {"", schemaflux.ErrConfiguration}}},
		{"a claim matches text anywhere in the schema name", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("oic", "claimed")
		}, []schemaflux.Request{in("", "Invoice")}, []outcome{{"claimed", 0}}},
	})
}

func TestReplyWhenWithNoTextPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("ReplyWhen(\"\") did not panic")
		}
	}()
	var p schemafluxtest.Provider
	p.ReplyWhen("", "x")
}

func TestFailReturnsTheErrorGiven(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("down")
	runScriptCases(t, []scriptCase{
		{"an error with a message and a kind", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "scripted"})
		}, one, []outcome{{"", schemaflux.ErrUnavailable}}},
		{"a bare kind decides the kind", func(p *schemafluxtest.Provider) { p.Fail(schemaflux.ErrRateLimited) },
			one, []outcome{{"", schemaflux.ErrRateLimited}}},
		{"an error with a kind keeps it", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401})
		}, one, []outcome{{"", schemaflux.ErrUnauthorized}}},
		{"a nil error is the provider being unavailable", func(p *schemafluxtest.Provider) { p.Fail(nil) },
			one, []outcome{{"", schemaflux.ErrUnavailable}}},
		{"every request fails, not only the first", func(p *schemafluxtest.Provider) { p.Fail(schemaflux.ErrTimeout) },
			many(4), []outcome{{"", schemaflux.ErrTimeout}, {"", schemaflux.ErrTimeout}, {"", schemaflux.ErrTimeout}, {"", schemaflux.ErrTimeout}}},
		{"it replaces a Reply", func(p *schemafluxtest.Provider) {
			p.Reply("ok")
			p.Fail(schemaflux.ErrRefused)
		}, one, []outcome{{"", schemaflux.ErrRefused}}},
		{"a Reply after it ends the outage", func(p *schemafluxtest.Provider) {
			p.Fail(schemaflux.ErrRefused)
			p.Reply("ok")
		}, one, []outcome{{"ok", 0}}},
		{"it replaces a FailThen", func(p *schemafluxtest.Provider) {
			p.FailThen(1, schemaflux.ErrBusy, "ok")
			p.Fail(schemaflux.ErrClosed)
		}, many(2), []outcome{{"", schemaflux.ErrClosed}, {"", schemaflux.ErrClosed}}},
		{"it replaces a Respond function", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return schemaflux.Response{Content: "x"}, nil
			})
			p.Fail(schemaflux.ErrTooLarge)
		}, one, []outcome{{"", schemaflux.ErrTooLarge}}},
		{"a wrapped kind is found by errors.Is", func(p *schemafluxtest.Provider) {
			p.Fail(errors.Join(errors.New("context"), &schemaflux.Error{Kind: schemaflux.ErrTruncated}))
		}, one, []outcome{{"", schemaflux.ErrTruncated}}},
	})

	t.Run("the error given is the error returned", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Fail(sentinel)
		if _, err := ask(&p, schemaflux.Request{}); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want the error given", err)
		}
	})
	t.Run("the fields of an error with a kind are kept", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429, RetryAfter: 3 * time.Second})
		_, err := ask(&p, schemaflux.Request{})
		var e *schemaflux.Error
		if !errors.As(err, &e) || e.Status != 429 || e.RetryAfter != 3*time.Second {
			t.Fatalf("err = %#v", err)
		}
	})
}

func TestFailThenFailsTheFirstRequestsAndThenAnswers(t *testing.T) {
	t.Parallel()
	down := schemaflux.ErrUnavailable
	runScriptCases(t, []scriptCase{
		{"no failures is a Reply of the body", func(p *schemafluxtest.Provider) { p.FailThen(0, down, "ok") },
			many(2), []outcome{{"ok", 0}, {"ok", 0}}},
		{"one failure then the body", func(p *schemafluxtest.Provider) { p.FailThen(1, down, "ok") },
			many(3), []outcome{{"", down}, {"ok", 0}, {"ok", 0}}},
		{"two failures then the body", func(p *schemafluxtest.Provider) { p.FailThen(2, down, "ok") },
			many(4), []outcome{{"", down}, {"", down}, {"ok", 0}, {"ok", 0}}},
		{"three failures then the body", func(p *schemafluxtest.Provider) { p.FailThen(3, down, "ok") },
			many(4), []outcome{{"", down}, {"", down}, {"", down}, {"ok", 0}}},
		{"a negative count is none", func(p *schemafluxtest.Provider) { p.FailThen(-5, down, "ok") },
			many(2), []outcome{{"ok", 0}, {"ok", 0}}},
		{"a nil error is the provider being unavailable", func(p *schemafluxtest.Provider) { p.FailThen(1, nil, "ok") },
			many(2), []outcome{{"", schemaflux.ErrUnavailable}, {"ok", 0}}},
		{"the kind of the error is kept", func(p *schemafluxtest.Provider) { p.FailThen(2, schemaflux.ErrRateLimited, "ok") },
			many(3), []outcome{{"", schemaflux.ErrRateLimited}, {"", schemaflux.ErrRateLimited}, {"ok", 0}}},
		{"an empty body is a body", func(p *schemafluxtest.Provider) { p.FailThen(1, down, "") },
			many(2), []outcome{{"", down}, {"", 0}}},
		{"a Reply after it replaces it", func(p *schemafluxtest.Provider) {
			p.FailThen(5, down, "ok")
			p.Reply("now")
		}, many(2), []outcome{{"now", 0}, {"now", 0}}},
		{"a FailThen after a Reply starts its own count", func(p *schemafluxtest.Provider) {
			p.Reply("a", "b")
			_, _ = ask(p, schemaflux.Request{})
			p.FailThen(1, down, "ok")
		}, many(2), []outcome{{"", down}, {"ok", 0}}},
		{"a claim answers without taking a failure", func(p *schemafluxtest.Provider) {
			p.ReplyWhen("x", "claimed")
			p.FailThen(1, down, "ok")
		}, []schemaflux.Request{in("", "x"), in("", "y"), in("", "y")}, []outcome{{"claimed", 0}, {"", down}, {"ok", 0}}},
	})
}

func TestShapeAnswersWithTheShapeTheRequestDeclares(t *testing.T) {
	t.Parallel()
	object := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label": map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
			"ok":    map[string]any{"type": "boolean"},
		},
		"required":             []any{"label", "count", "ok"},
		"additionalProperties": false,
	}
	template := `Return a JSON object with: {"verdict": "yes", "score": 3}`

	decode := func(t *testing.T, content string) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			t.Fatalf("shaped answer %q is not a JSON object: %v", content, err)
		}
		return v
	}

	t.Run("an object schema is answered with its required members", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		v := decode(t, body(t, &p, schemaflux.Request{Schema: object, SchemaName: "T"}))
		for _, key := range []string{"label", "count", "ok"} {
			if _, ok := v[key]; !ok {
				t.Errorf("shaped answer lacks %q: %v", key, v)
			}
		}
	})
	t.Run("the members have the types the schema declares", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		v := decode(t, body(t, &p, schemaflux.Request{Schema: object, SchemaName: "T"}))
		if _, ok := v["label"].(string); !ok {
			t.Errorf("label = %#v", v["label"])
		}
		if _, ok := v["count"].(float64); !ok {
			t.Errorf("count = %#v", v["count"])
		}
		if _, ok := v["ok"].(bool); !ok {
			t.Errorf("ok = %#v", v["ok"])
		}
	})
	t.Run("a template in the system prompt is answered when there is no schema", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		v := decode(t, body(t, &p, schemaflux.Request{System: template}))
		if _, ok := v["verdict"]; !ok {
			t.Errorf("shaped answer lacks verdict: %v", v)
		}
		if _, ok := v["score"]; !ok {
			t.Errorf("shaped answer lacks score: %v", v)
		}
	})
	t.Run("a request that declares no shape fails with a configuration error", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		_, err := ask(&p, schemaflux.Request{System: "plain text", User: "hi"})
		if !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Fatalf("err = %v, want ErrConfiguration", err)
		}
		if msg := messageOf(t, err); !strings.Contains(msg, "Shape") || !strings.Contains(msg, "no schema") {
			t.Errorf("message does not say what happened: %q", msg)
		}
	})
	t.Run("a request with nothing in it fails too", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		if _, err := ask(&p, schemaflux.Request{}); !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a request that failed to shape is still counted", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		_, _ = ask(&p, schemaflux.Request{})
		_, _ = ask(&p, schemaflux.Request{Schema: object})
		if p.Calls() != 2 {
			t.Fatalf("Calls() = %d, want 2", p.Calls())
		}
	})
	t.Run("a Reply after it replaces it", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		p.Reply("plain")
		if got := body(t, &p, schemaflux.Request{Schema: object}); got != "plain" {
			t.Fatalf("body = %q", got)
		}
	})
	t.Run("it replaces a Reply", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("plain")
		p.Shape()
		if got := body(t, &p, schemaflux.Request{Schema: object}); got == "plain" {
			t.Fatalf("body = %q, want a shaped answer", got)
		}
	})
	t.Run("a claim answers before the shape", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		p.ReplyWhen("special", "claimed")
		if got := body(t, &p, schemaflux.Request{Schema: object, SchemaName: "Special"}); got != "claimed" {
			t.Fatalf("body = %q", got)
		}
		if got := body(t, &p, schemaflux.Request{Schema: object, SchemaName: "Other"}); got == "claimed" {
			t.Fatalf("body = %q, want a shaped answer", got)
		}
	})
	t.Run("the failure does not repeat the user message", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		_, err := ask(&p, schemaflux.Request{User: "CANARY-PAYLOAD"})
		if strings.Contains(err.Error(), "CANARY-PAYLOAD") {
			t.Fatalf("error repeats the caller's data: %v", err)
		}
	})
	t.Run("every request is shaped, not only the first", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Shape()
		for i := range 5 {
			decode(t, body(t, &p, schemaflux.Request{Schema: object}))
			if p.Calls() != i+1 {
				t.Fatalf("Calls() = %d", p.Calls())
			}
		}
	})
}

func TestRespondAnswersFromTheRequest(t *testing.T) {
	t.Parallel()

	t.Run("it is given the zero-based position of each request", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		var seen []int
		p.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
			seen = append(seen, call)
			return schemaflux.Response{}, nil
		})
		for range 5 {
			_, _ = ask(&p, schemaflux.Request{})
		}
		if len(seen) != 5 || seen[0] != 0 || seen[4] != 4 {
			t.Fatalf("calls seen = %v", seen)
		}
	})
	t.Run("it is given the request as it was sent", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		var got schemaflux.Request
		p.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
			got = req
			return schemaflux.Response{}, nil
		})
		sent := schemaflux.Request{Model: "m", System: "s", User: "u", SchemaName: "n", MaxTokens: 7, Temperature: 0.3, WebSearch: true}
		_, _ = ask(&p, sent)
		if got.Model != sent.Model || got.System != sent.System || got.User != sent.User || got.SchemaName != sent.SchemaName ||
			got.MaxTokens != 7 || got.Temperature != 0.3 || !got.WebSearch {
			t.Fatalf("function saw %+v", got)
		}
	})
	t.Run("the response comes back exactly as the function built it", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		want := schemaflux.Response{Content: "c", FinishReason: "length", Model: "other", ServiceTier: "flex", Usage: schemaflux.Usage{Input: 3, Output: 4, Total: 7}}
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { return want, nil })
		got, err := ask(&p, schemaflux.Request{})
		if err != nil || got.Content != want.Content || got.FinishReason != "length" || got.Model != "other" || got.ServiceTier != "flex" || got.Usage != want.Usage {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a field the function leaves empty stays empty", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.SetUsage(schemaflux.Usage{Input: 9})
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "c"}, nil
		})
		got, _ := ask(&p, schemaflux.Request{Model: "asked"})
		if got.FinishReason != "" || got.Model != "" || got.Usage != (schemaflux.Usage{}) {
			t.Fatalf("got %+v, want nothing filled in", got)
		}
	})
	t.Run("the error of the function is returned as it is", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		sentinel := errors.New("chosen by the test")
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { return schemaflux.Response{}, sentinel })
		if _, err := ask(&p, schemaflux.Request{}); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a kind in the error is kept", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{}, &schemaflux.Error{Kind: schemaflux.ErrRefused}
		})
		if _, err := ask(&p, schemaflux.Request{}); !errors.Is(err, schemaflux.ErrRefused) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a nil function is refused and the script stays", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "x"}, nil
		})
		defer func() {
			if recovered := recover(); recovered == nil || !strings.Contains(fmt.Sprint(recovered), "Respond needs a function") {
				t.Errorf("recovered %v, want a panic that says Respond needs a function", recovered)
			}
			if got := body(t, &p, schemaflux.Request{}); got != "x" {
				t.Errorf("body = %q, want the script that was there", got)
			}
		}()
		p.Respond(nil)
	})
	t.Run("a Reply after it replaces it", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "f"}, nil
		})
		p.Reply("list")
		if got := body(t, &p, schemaflux.Request{}); got != "list" {
			t.Fatalf("body = %q", got)
		}
	})
	t.Run("the position counts the requests a claim answered too", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("claimed", "c")
		var seen []int
		p.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
			seen = append(seen, call)
			return schemaflux.Response{}, nil
		})
		_, _ = ask(&p, in("", "claimed"))
		_, _ = ask(&p, in("", "other"))
		if len(seen) != 1 || seen[0] != 1 {
			t.Fatalf("function saw positions %v, want [1]", seen)
		}
	})
	t.Run("the function may read the fake without a deadlock", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: string(rune('0' + p.Calls()))}, nil
		})
		if got := body(t, &p, schemaflux.Request{}); got != "1" {
			t.Fatalf("body = %q, want the count it saw", got)
		}
	})
	t.Run("an answer chosen by what the request asks", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
			if strings.Contains(req.User, "beta") {
				return schemaflux.Response{Content: "B"}, nil
			}
			return schemaflux.Response{Content: "A"}, nil
		})
		if a, b := body(t, &p, schemaflux.Request{User: "alpha"}), body(t, &p, schemaflux.Request{User: "beta"}); a != "A" || b != "B" {
			t.Fatalf("answers = %q, %q", a, b)
		}
	})
}

func TestCompleteFillsTheAnswerLikeAModelWould(t *testing.T) {
	t.Parallel()

	t.Run("the finish reason is stop", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		resp, _ := ask(&p, schemaflux.Request{})
		if resp.FinishReason != "stop" {
			t.Fatalf("FinishReason = %q", resp.FinishReason)
		}
	})
	t.Run("the model is the one asked for", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		resp, _ := ask(&p, schemaflux.Request{Model: "gpt-x"})
		if resp.Model != "gpt-x" {
			t.Fatalf("Model = %q", resp.Model)
		}
	})
	t.Run("the model is test-model when none was asked for", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		resp, _ := ask(&p, schemaflux.Request{})
		if resp.Model != "test-model" {
			t.Fatalf("Model = %q", resp.Model)
		}
	})
	t.Run("the provider field is empty, meaning the one that was asked", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		resp, _ := ask(&p, schemaflux.Request{})
		if resp.Provider != "" || resp.Upstream != "" || resp.Cached || resp.Hedged {
			t.Fatalf("response = %+v", resp)
		}
	})
	t.Run("a decision provider's fields are empty", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		resp, _ := ask(&p, schemaflux.Request{Decision: &schemaflux.Decision{Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "q"}}}})
		if resp.Answers != nil || resp.Escalation != nil || resp.Content != "{}" {
			t.Fatalf("response = %+v", resp)
		}
	})
	t.Run("a request that carries a decision is answered with the prompts' text", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply(`{"q1":true,"confidence":0.9}`)
		resp, _ := ask(&p, schemaflux.Request{Decision: &schemaflux.Decision{}})
		if resp.Content != `{"q1":true,"confidence":0.9}` {
			t.Fatalf("Content = %q", resp.Content)
		}
	})
	t.Run("the request is recorded as it was sent", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		sent := schemaflux.Request{Model: "m", System: "s", User: "u", Format: "json", SchemaName: "n", Effort: schemaflux.EffortLow, CacheKey: "k"}
		_, _ = ask(&p, sent)
		got := p.Last()
		if got.Model != "m" || got.System != "s" || got.User != "u" || got.Format != "json" || got.SchemaName != "n" || got.Effort != schemaflux.EffortLow || got.CacheKey != "k" {
			t.Fatalf("Last() = %+v", got)
		}
	})
	t.Run("a context that has ended fails the request with its error", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Complete(ctx, schemaflux.Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a request whose context had ended is still counted", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = p.Complete(ctx, schemaflux.Request{})
		if p.Calls() != 1 {
			t.Fatalf("Calls() = %d", p.Calls())
		}
	})
	t.Run("a context error wins over a scripted failure", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Fail(schemaflux.ErrRateLimited)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Complete(ctx, schemaflux.Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an expired deadline is reported as the deadline", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		if _, err := p.Complete(ctx, schemaflux.Request{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSetUsageSetsWhatEveryAnswerReports(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		usage schemaflux.Usage
	}{
		{"input and output", schemaflux.Usage{Input: 10, Output: 5, Total: 15}},
		{"the zero usage reports none", schemaflux.Usage{}},
		{"cached tokens", schemaflux.Usage{Input: 100, Cached: 80}},
		{"cache writes", schemaflux.Usage{Input: 100, CacheWrite: 20}},
		{"reasoning tokens", schemaflux.Usage{Output: 50, Reasoning: 40}},
		{"every field", schemaflux.Usage{Input: 1, Output: 2, Total: 3, Cached: 4, CacheWrite: 5, Reasoning: 6}},
		{"a large count", schemaflux.Usage{Input: 1 << 30, Output: 1 << 29}},
		{"a total left at zero is not filled in", schemaflux.Usage{Input: 3, Output: 4}},
		{"output alone", schemaflux.Usage{Output: 1}},
		{"input alone", schemaflux.Usage{Input: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var p schemafluxtest.Provider
			p.SetUsage(c.usage)
			for range 2 {
				resp, _ := ask(&p, schemaflux.Request{})
				if resp.Usage != c.usage {
					t.Fatalf("Usage = %+v, want %+v", resp.Usage, c.usage)
				}
			}
		})
	}

	t.Run("it applies to the answers of a claim", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "c")
		p.SetUsage(schemaflux.Usage{Input: 5})
		resp, _ := ask(&p, in("", "x"))
		if resp.Usage.Input != 5 {
			t.Fatalf("Usage = %+v", resp.Usage)
		}
	})
	t.Run("a later call replaces it", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.SetUsage(schemaflux.Usage{Input: 5})
		p.SetUsage(schemaflux.Usage{Input: 6})
		if resp, _ := ask(&p, schemaflux.Request{}); resp.Usage.Input != 6 {
			t.Fatalf("Usage = %+v", resp.Usage)
		}
	})
	t.Run("Reset keeps it", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.SetUsage(schemaflux.Usage{Input: 5})
		p.Reset()
		if resp, _ := ask(&p, schemaflux.Request{}); resp.Usage.Input != 5 {
			t.Fatalf("Usage = %+v", resp.Usage)
		}
	})
}

func TestSetNameAndNameTellTheProviderApart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		set  []string
		want string
	}{
		{"a zero provider is named test", nil, "test"},
		{"a name is returned", []string{"alpha"}, "alpha"},
		{"an empty name restores the default", []string{"alpha", ""}, "test"},
		{"a later name replaces the earlier", []string{"alpha", "beta"}, "beta"},
		{"a name is not changed to lower case", []string{"MixedCase"}, "MixedCase"},
		{"a name with a dash", []string{"test-7"}, "test-7"},
		{"a name with a slash", []string{"a/b"}, "a/b"},
		{"a multibyte name", []string{"naïve"}, "naïve"},
		{"the same name twice", []string{"x", "x"}, "x"},
		{"the name of the default itself", []string{"test"}, "test"},
		{"a long name", []string{strings.Repeat("n", 500)}, strings.Repeat("n", 500)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var p schemafluxtest.Provider
			for _, name := range c.set {
				p.SetName(name)
			}
			if got := p.Name(); got != c.want {
				t.Fatalf("Name() = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("the name survives Reset", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.SetName("kept")
		p.Reset()
		if p.Name() != "kept" {
			t.Fatalf("Name() = %q", p.Name())
		}
	})
	t.Run("two providers keep their own names", func(t *testing.T) {
		t.Parallel()
		var a, b schemafluxtest.Provider
		a.SetName("a")
		if b.Name() != "test" || a.Name() != "a" {
			t.Fatalf("names = %q, %q", a.Name(), b.Name())
		}
	})
}

func TestRequestsAreRecordedInTheOrderTheyArrived(t *testing.T) {
	t.Parallel()

	t.Run("a new provider has none", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		if len(p.Requests()) != 0 || p.Calls() != 0 {
			t.Fatalf("Requests() = %v", p.Requests())
		}
	})
	t.Run("they are in arrival order", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		for _, u := range []string{"a", "b", "c", "d"} {
			_, _ = ask(&p, schemaflux.Request{User: u})
		}
		var got string
		for _, r := range p.Requests() {
			got += r.User
		}
		if got != "abcd" {
			t.Fatalf("users = %q", got)
		}
	})
	t.Run("the slice is a copy", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		_, _ = ask(&p, schemaflux.Request{User: "a"})
		got := p.Requests()
		got[0].User = "changed"
		if p.Requests()[0].User != "a" {
			t.Fatal("changing the returned slice changed the record")
		}
	})
	t.Run("a failed request is recorded", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Fail(schemaflux.ErrBusy)
		_, _ = ask(&p, schemaflux.Request{User: "a"})
		if len(p.Requests()) != 1 {
			t.Fatalf("Requests() = %d", len(p.Requests()))
		}
	})
	t.Run("a request refused by a script is recorded", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "c")
		_, _ = ask(&p, schemaflux.Request{User: "a"})
		if len(p.Requests()) != 1 {
			t.Fatalf("Requests() = %d", len(p.Requests()))
		}
	})
	t.Run("every field of the request is kept", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		sent := schemaflux.Request{
			Model: "m", System: "s", User: "u", Temperature: 0.5, MaxTokens: 9, Format: "json",
			Schema: map[string]any{"type": "object"}, SchemaName: "n", Effort: schemaflux.EffortHigh,
			EffortPreferred: true, CacheKey: "k", WebSearch: true,
			Decision: &schemaflux.Decision{State: "\"s\""},
		}
		_, _ = ask(&p, sent)
		got := p.Last()
		if got.Temperature != 0.5 || got.MaxTokens != 9 || got.Schema["type"] != "object" || !got.EffortPreferred ||
			!got.WebSearch || got.Decision == nil || got.Decision.State != "\"s\"" {
			t.Fatalf("Last() = %+v", got)
		}
	})
	t.Run("Last is the zero request when there has been none", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		if got := p.Last(); got.User != "" || got.Model != "" || got.Decision != nil {
			t.Fatalf("Last() = %+v", got)
		}
	})
	t.Run("Last is the most recent request", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		_, _ = ask(&p, schemaflux.Request{User: "first"})
		_, _ = ask(&p, schemaflux.Request{User: "second"})
		if p.Last().User != "second" {
			t.Fatalf("Last().User = %q", p.Last().User)
		}
	})
	t.Run("Calls counts failures and answers alike", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.FailThen(2, schemaflux.ErrBusy, "ok")
		for range 5 {
			_, _ = ask(&p, schemaflux.Request{})
		}
		if p.Calls() != 5 {
			t.Fatalf("Calls() = %d", p.Calls())
		}
	})
	t.Run("a scripted failure is seen once, since nothing retries it", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Fail(schemaflux.ErrRateLimited)
		_, _ = ask(&p, schemaflux.Request{})
		if p.Calls() != 1 {
			t.Fatalf("Calls() = %d, want 1", p.Calls())
		}
		if retries, _ := p.RetryPolicy(); retries >= 0 {
			t.Fatalf("RetryPolicy() retries = %d, want a negative number (never)", retries)
		}
	})
	t.Run("Last returns a request that shares its schema with the caller", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		schema := map[string]any{"type": "object"}
		_, _ = ask(&p, schemaflux.Request{Schema: schema})
		if got := p.Last().Schema; got["type"] != "object" {
			t.Fatalf("Schema = %v", got)
		}
	})
}

func TestResetForgetsTheRequestsAndStartsTheScriptsAgain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(p *schemafluxtest.Provider)
		reqs  []schemaflux.Request
		first string
	}{
		{"a Reply sequence starts again", func(p *schemafluxtest.Provider) { p.Reply("1", "2", "3") }, many(2), "1"},
		{"a FailThen counts its failures again", func(p *schemafluxtest.Provider) { p.FailThen(1, schemaflux.ErrBusy, "ok") }, many(2), ""},
		{"a claim's sequence starts again", func(p *schemafluxtest.Provider) { p.ReplyWhen("x", "c1", "c2") }, []schemaflux.Request{in("", "x"), in("", "x")}, "c1"},
		{"a Respond function is given position zero again", func(p *schemafluxtest.Provider) {
			p.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
				return schemaflux.Response{Content: string(rune('a' + call))}, nil
			})
		}, many(3), "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var p schemafluxtest.Provider
			c.setup(&p)
			for _, req := range c.reqs {
				_, _ = ask(&p, req)
			}
			p.Reset()
			if p.Calls() != 0 || len(p.Requests()) != 0 {
				t.Fatalf("after Reset: Calls() = %d", p.Calls())
			}
			resp, _ := ask(&p, c.reqs[0])
			if resp.Content != c.first {
				t.Fatalf("first answer after Reset = %q, want %q", resp.Content, c.first)
			}
		})
	}

	t.Run("Reset on a provider that has done nothing is fine", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reset()
		p.Reset()
		if got := body(t, &p, schemaflux.Request{}); got != "{}" {
			t.Fatalf("body = %q", got)
		}
	})
	t.Run("the scripts stay", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "claimed")
		p.Reply("default")
		p.Reset()
		if body(t, &p, in("", "x")) != "claimed" || body(t, &p, in("", "y")) != "default" {
			t.Fatal("a script was lost by Reset")
		}
	})
	t.Run("the last request is the zero request again", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		_, _ = ask(&p, schemaflux.Request{User: "x"})
		p.Reset()
		if p.Last().User != "" {
			t.Fatalf("Last().User = %q", p.Last().User)
		}
	})
	t.Run("a fake with no script does not become one that fails", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		_, _ = ask(&p, schemaflux.Request{})
		p.Reset()
		if body(t, &p, schemaflux.Request{}) != "{}" {
			t.Fatal("a Reset provider stopped answering")
		}
	})
}

func TestRetryPolicyTellsTheLibraryToNeverRetry(t *testing.T) {
	t.Parallel()
	setups := map[string]func(p *schemafluxtest.Provider){
		"a zero provider": func(*schemafluxtest.Provider) {},
		"after Reply":     func(p *schemafluxtest.Provider) { p.Reply("x") },
		"after Fail":      func(p *schemafluxtest.Provider) { p.Fail(schemaflux.ErrBusy) },
		"after FailThen":  func(p *schemafluxtest.Provider) { p.FailThen(3, schemaflux.ErrBusy, "x") },
		"after Shape":     func(p *schemafluxtest.Provider) { p.Shape() },
		"after Respond": func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { return schemaflux.Response{}, nil })
		},
		"after ReplyWhen": func(p *schemafluxtest.Provider) { p.ReplyWhen("x") },
		"after Delay":     func(p *schemafluxtest.Provider) { p.Delay(time.Hour) },
		"after SetUsage":  func(p *schemafluxtest.Provider) { p.SetUsage(schemaflux.Usage{Input: 1}) },
		"after SetName":   func(p *schemafluxtest.Provider) { p.SetName("x") },
		"after Reset":     func(p *schemafluxtest.Provider) { p.Reset() },
		"after a request": func(p *schemafluxtest.Provider) { _, _ = ask(p, schemaflux.Request{}) },
		"after a retry loop": func(p *schemafluxtest.Provider) {
			p.Fail(schemaflux.ErrRateLimited)
			_, _ = ask(p, schemaflux.Request{})
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var p schemafluxtest.Provider
			setup(&p)
			retries, backoff := p.RetryPolicy()
			if retries != -1 || backoff != 0 {
				t.Fatalf("RetryPolicy() = %d, %v; want -1, 0", retries, backoff)
			}
		})
	}
}

func TestModelForNamesTheOneModelForEveryTier(t *testing.T) {
	t.Parallel()
	tiers := []schemaflux.Tier{0, schemaflux.TierSmart, schemaflux.TierFast, schemaflux.TierQuick, 4, 5, -1, 99, 1 << 20, -1 << 20}
	var p schemafluxtest.Provider
	for _, tier := range tiers {
		if got := p.ModelFor(tier); got != "test-model" {
			t.Errorf("ModelFor(%d) = %q, want test-model", tier, got)
		}
	}
	p.SetName("another")
	if p.ModelFor(schemaflux.TierFast) != "test-model" {
		t.Error("the name changed the model")
	}
}

func TestAFailedScriptSaysWhatWasAskedAndWhatWasScripted(t *testing.T) {
	t.Parallel()

	t.Run("it names the request, its schema, its system prompt and its size", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("invoice", "x")
		_, _ = ask(&p, in("irrelevant", "Invoice"))
		_, err := ask(&p, schemaflux.Request{Model: "m-1", SchemaName: "Receipt", System: "You extract receipts.", User: "0123456789"})
		msg := messageOf(t, err)
		for _, want := range []string{"request 2", `"m-1"`, `"Receipt"`, `"You extract receipts."`, "10 bytes"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message lacks %s: %s", want, msg)
			}
		}
	})
	t.Run("it lists what is scripted", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("invoice", "x", "y")
		p.ReplyWhen("receipt", "z")
		_, err := ask(&p, in("", "Other"))
		msg := messageOf(t, err)
		for _, want := range []string{`ReplyWhen "invoice" (2 bodies)`, `ReplyWhen "receipt" (1 body)`, "no answer for any other request"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message lacks %q: %s", want, msg)
			}
		}
	})
	t.Run("it never repeats the user message", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "y")
		_, err := ask(&p, schemaflux.Request{User: "CANARY-PAYLOAD"})
		if strings.Contains(err.Error(), "CANARY-PAYLOAD") {
			t.Fatalf("error repeats the caller's data: %v", err)
		}
	})
	t.Run("it is bounded however long the prompt and the script are", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		for i := range 60 {
			p.ReplyWhen(strings.Repeat("claim", 40)+string(rune('a'+i%26)), "x")
		}
		_, err := ask(&p, schemaflux.Request{
			Model: strings.Repeat("m", 5000), SchemaName: strings.Repeat("s", 5000), System: strings.Repeat("prompt ", 5000),
		})
		msg := messageOf(t, err)
		if n := len([]rune(msg)); n > 600 {
			t.Fatalf("message is %d runes, want at most 600", n)
		}
		if !strings.Contains(msg, "request 1") {
			t.Fatalf("a bounded message lost its start: %s", msg)
		}
	})
	t.Run("many claims are listed up to five and counted after", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		for i := range 8 {
			p.ReplyWhen(string(rune('a'+i)), "x")
		}
		_, err := ask(&p, schemaflux.Request{User: "u"})
		msg := messageOf(t, err)
		if !strings.Contains(msg, "and 3 more claims") {
			t.Fatalf("message = %s", msg)
		}
	})
	t.Run("a system prompt of several lines reads as one phrase", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "y")
		_, err := ask(&p, schemaflux.Request{System: "line one\n\nline   two"})
		if msg := messageOf(t, err); strings.Contains(msg, "\n") || !strings.Contains(msg, `"line one line two"`) {
			t.Fatalf("message = %q", msg)
		}
	})
	t.Run("the kind is configuration, and not retryable", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "y")
		_, err := ask(&p, schemaflux.Request{})
		var e *schemaflux.Error
		if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || e.Retryable() {
			t.Fatalf("err = %#v", err)
		}
	})
	t.Run("a decision in the request is counted, not quoted", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "y")
		_, err := ask(&p, schemaflux.Request{Decision: &schemaflux.Decision{State: "\"CANARY\"", Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "CANARY?"}, {Kind: schemaflux.DecisionYesNo, Ask: "CANARY2?"}}}})
		msg := messageOf(t, err)
		if !strings.Contains(msg, "a decision of 2 questions") || strings.Contains(msg, "CANARY") {
			t.Fatalf("message = %s", msg)
		}
	})
	t.Run("the message is lower case and in the library's words", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "y")
		_, err := ask(&p, schemaflux.Request{})
		if msg := messageOf(t, err); !strings.HasPrefix(msg, "no answer is scripted") {
			t.Fatalf("message = %q", msg)
		}
	})
	t.Run("a script with a default never fails a request", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.ReplyWhen("x", "y")
		p.Reply("d")
		if _, err := ask(&p, schemaflux.Request{}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

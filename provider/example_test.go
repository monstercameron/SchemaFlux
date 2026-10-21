package provider_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
)

// loopback starts a server on the loopback interface that answers every request
// with status, body and the headers given as name, value pairs. The examples use
// it where a program would use a provider's real address.
func loopback(status int, body string, headers ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
}

// A key in code wins over the environment. A provider built this way goes in a
// schemaflux.Client, which is what the operations are called on.
func ExampleNew() {
	srv := loopback(200, `{"id":"r","status":"completed","model":"gpt-test","output":[{"type":"message","content":[`+
		`{"type":"output_text","text":"Paris."}]}],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}`)
	defer srv.Close()

	// BaseURL points at a test server here; a program leaves it empty.
	p, err := provider.New("openai", provider.Config{APIKey: "sk-example", BaseURL: srv.URL})
	if err != nil {
		log.Fatal(err)
	}

	resp, err := p.Complete(context.Background(), schemaflux.Request{Model: "gpt-test", User: "Capital of France?"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(p.Name(), resp.Content, resp.Usage.Total)

	// A provider built here retries a failed request 3 times and gives up on
	// one HTTP call after 30 seconds. A Config says otherwise; a negative
	// Retries means none.
	retries, backoff := p.(provider.Retrier).RetryPolicy()
	fmt.Println(retries, backoff)
	// Output:
	// openai Paris. 15
	// 3 1s
}

// A failure of a built provider is a *schemaflux.Error whose kind says what a
// caller can do about it, and whose text holds nothing the provider wrote.
func ExampleNew_errors() {
	srv := loopback(429, `{"error":{"message":"slow down, key sk-example","code":"rate_limit"}}`, "Retry-After", "7")
	defer srv.Close()

	p, err := provider.New("anthropic", provider.Config{APIKey: "sk-example", BaseURL: srv.URL})
	if err != nil {
		log.Fatal(err)
	}
	_, err = p.Complete(context.Background(), schemaflux.Request{Model: "claude-test", User: "hello"})

	var e *schemaflux.Error
	if errors.As(err, &e) {
		fmt.Println(e.Kind, e.Status, e.RetryAfter, e.Retryable())
	}
	fmt.Println(errors.Is(err, schemaflux.ErrRateLimited))
	fmt.Println(strings.Contains(err.Error(), "slow down"), strings.Contains(e.Detail(), "slow down"))
	// Output:
	// rate limited 429 7s true
	// true
	// false true
}

// A server that speaks the chat completions dialect needs only its address. A
// server that needs no key takes any non-empty one.
func ExampleOpenAICompatible() {
	srv := loopback(200, `{"model":"local-model","choices":[{"finish_reason":"stop","message":{"content":"Bonjour."}}]}`)
	defer srv.Close()

	p, err := provider.OpenAICompatible("llama-server", srv.URL+"/v1", provider.Config{APIKey: "none"})
	if err != nil {
		log.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), schemaflux.Request{Model: "local-model", User: "Say hello in French."})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(p.Name(), resp.Content)
	// Output: llama-server Bonjour.
}

// acme is a provider of the program's own.
type acme struct{ key string }

func (a *acme) Name() string { return "acme" }

func (a *acme) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{Content: "acme answers: " + req.User, Model: "acme-1"}, nil
}

// Register makes a provider of the program's own known by name, so that New
// builds it and Names lists it. A Factory receives the Config New was given, with
// the defaults of a built provider filled in.
func ExampleRegister() {
	err := provider.Register("acme-example", func(cfg provider.Config) (schemaflux.Provider, error) {
		if cfg.APIKey == "" {
			return nil, &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Message: "acme needs a key"}
		}
		return &acme{key: cfg.APIKey}, nil
	})
	if err != nil {
		log.Fatal(err)
	}

	p, err := provider.New("acme-example", provider.Config{APIKey: "k"})
	if err != nil {
		log.Fatal(err)
	}
	resp, _ := p.Complete(context.Background(), schemaflux.Request{User: "hello"})
	fmt.Println(resp.Content)

	// A taken name is an error, and so is a name that is a built-in provider's.
	fmt.Println(errors.Is(provider.Register("acme-example", nil), schemaflux.ErrConfiguration))
	fmt.Println(errors.Is(provider.Register("openai", func(provider.Config) (schemaflux.Provider, error) { return nil, nil }), schemaflux.ErrConfiguration))

	// The factory's own failure is the error New returns.
	_, err = provider.New("acme-example", provider.Config{})
	fmt.Println(errors.Is(err, schemaflux.ErrConfiguration), err != nil)
	// Output:
	// acme answers: hello
	// true
	// true
	// true true
}

// counting wraps a provider to count its requests. Unwrap returns the provider
// it wraps, so that a client that asks whether the provider can stream, be
// warmed or say its own retry settings finds the answer of the provider
// underneath.
type counting struct {
	schemaflux.Provider
	calls atomic.Int64
}

func (c *counting) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	c.calls.Add(1)
	return c.Provider.Complete(ctx, req)
}

func (c *counting) Unwrap() schemaflux.Provider { return c.Provider }

var _ provider.Unwrapper = (*counting)(nil)

// A wrapper keeps what the provider under it can do by implementing Unwrapper.
func ExampleUnwrapper() {
	srv := loopback(200, `{}`)
	defer srv.Close()
	inner, err := provider.OpenAICompatible("acme", srv.URL, provider.Config{APIKey: "key"})
	if err != nil {
		log.Fatal(err)
	}
	wrapped := &counting{Provider: inner}

	_, _ = wrapped.Complete(context.Background(), schemaflux.Request{User: "hello"})
	_, _ = wrapped.Complete(context.Background(), schemaflux.Request{User: "again"})
	fmt.Println("calls:", wrapped.calls.Load())

	// The wrapped provider's own settings are one Unwrap away.
	retries, _ := wrapped.Unwrap().(provider.Retrier).RetryPolicy()
	fmt.Println("retries:", retries)
	// Output:
	// calls: 2
	// retries: 3
}

// A provider says what it can do beyond Complete by implementing an optional
// interface; ask with a type assertion. OpenAI's provider streams.
func ExampleStreamer() {
	srv := loopback(200, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hel\"}\n\n"+
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n"+
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-test\","+
		"\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}]}}\n\n",
		"Content-Type", "text/event-stream")
	defer srv.Close()

	p, err := provider.New("openai", provider.Config{APIKey: "sk-example", BaseURL: srv.URL})
	if err != nil {
		log.Fatal(err)
	}
	streamer, ok := p.(provider.Streamer)
	if !ok {
		log.Fatal("this provider does not stream")
	}
	var stream iter.Seq2[schemaflux.Chunk, error] = streamer.Stream(context.Background(), schemaflux.Request{Model: "gpt-test", User: "hi"})
	for chunk, err := range stream {
		if err != nil {
			log.Fatal(err)
		}
		if chunk.Done {
			fmt.Println("whole:", chunk.Response.Content)
			break
		}
		fmt.Println("delta:", chunk.Delta)
	}
	// Output:
	// delta: Hel
	// delta: lo
	// whole: Hello
}

// A decision provider answers the typed questions of Request.Decision. It never
// reads the prompts, and it declares what it can take.
func ExampleDecider() {
	srv := loopback(200, `{"answers":{"q1":{"type":"noul","noul":0.96},"q2":{"type":"choice","choice":"o2","confidence":0.8}},"model":"jev-1"}`)
	defer srv.Close()

	p, err := provider.New("jev", provider.Config{APIKey: "key-example", BaseURL: srv.URL})
	if err != nil {
		log.Fatal(err)
	}
	limits := p.(provider.Decider).DecisionLimits()
	fmt.Println("options:", limits.MaxOptions, "levels:", limits.MaxLevels)

	resp, err := p.Complete(context.Background(), schemaflux.Request{Decision: &schemaflux.Decision{
		State: `"Please refund my order."`,
		Questions: []schemaflux.DecisionQuestion{
			{Kind: schemaflux.DecisionYesNo, Ask: "Is the customer asking for a refund?"},
			{Kind: schemaflux.DecisionChoice, Ask: "Which team should handle this?", Options: []string{"billing", "technical", "shipping"}},
		},
	}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("refund:", resp.Answers[0].Yes, "team:", resp.Answers[1].Label)

	// A request that is not a decision is refused before anything is sent.
	_, err = p.Complete(context.Background(), schemaflux.Request{User: "Write a poem."})
	fmt.Println(errors.Is(err, schemaflux.ErrUnsupported))
	// Output:
	// options: 255 levels: 10
	// refund: true team: technical
	// true
}

// DeclareRoute says where a provider and model run, so that a client whose data
// policy names regions can place them. Declare each route once, at start-up.
func ExampleDeclareRoute() {
	provider.DeclareRoute("openai", "gpt-6-luna", []string{"us"}, 30)
	provider.DeclareRoute("anthropic", "claude-example", []string{"us", "eu"}, 0)
}

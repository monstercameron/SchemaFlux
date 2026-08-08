// Package schemafluxtest provides a fake provider for testing code that uses
// SchemaFlux, so those tests need no API key, no network, and no money.
//
// Without it, the only injection point was an unexported function, and every
// downstream user faced a choice between paid, nondeterministic tests and
// hand-rolling an interface around this library. That is a poor thing to ask of
// someone for the privilege of testing their own code.
//
// # Using it
//
//	func TestRouting(t *testing.T) {
//	    provider := schemafluxtest.New().
//	        Reply(`{"selected":1,"confidence":0.9}`)
//	    defer schemafluxtest.Install(t, provider)()
//
//	    department, _, err := schemaflux.Decide(ctx, ticket, departments)
//	    // ...
//	    // And check what your code actually sent:
//	    if !strings.Contains(provider.LastRequest().UserPrompt, "invoice") {
//	        t.Error("the ticket text did not reach the model")
//	    }
//	}
//
// # The global
//
// Install replaces a package-level provider, so tests that use it cannot run in
// parallel with each other or with anything else that makes a SchemaFlux call.
// Install registers a cleanup that restores the previous provider, and it calls
// t.Setenv, which makes Go fail any test that also calls t.Parallel — so the
// constraint is enforced rather than documented. Removing the global is
// TODOS.md TI-002.
package schemafluxtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
)

// Provider is a fake LLM provider. Its zero value is not usable; call New.
type Provider struct {
	mu sync.Mutex

	replies  []string
	errs     []error
	index    int
	delay    time.Duration
	requests []schemaflux.CompletionRequest
	contexts []context.Context
	usage    schemaflux.TokenUsage
	name     string

	// replyFunc answers from the request itself when set, taking precedence
	// over the scripted list. See ReplyFunc.
	replyFunc func(call int, req schemaflux.CompletionRequest) (string, error)

	// shaped answers from the request's own declared shape when nothing is
	// scripted. See Shaped.
	shaped bool
}

// New returns a provider that replies with an empty JSON object until told
// otherwise.
func New() *Provider {
	return &Provider{
		replies: []string{"{}"},
		name:    "local",
	}
}

// Shaped makes the provider answer with whatever shape the request declared --
// its JSON Schema, or the JSON template in its system prompt -- instead of the
// empty object it replies with by default.
//
// It is for tests that care about what was *sent*: golden prompts, request
// shape, cost accounting. Without it, those tests have to script a body per
// operation just to get past the parse, which is a lot of fixture for a
// question that is not about the answer.
//
// A scripted reply always wins. This is a fallback, not a mode, and the values
// it produces are obviously synthetic -- anything that needs a particular
// answer should say so with Reply.
func (p *Provider) Shaped() *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shaped = true
	// The constructor's default reply is what Shaped replaces; a caller who
	// wants both scripts one with Reply after this.
	p.replies = nil
	return p
}

// Reply sets the bodies the provider returns, in order. The last one repeats
// once the list is exhausted, so a test that makes an unknown number of calls
// does not have to predict it.
func (p *Provider) Reply(bodies ...string) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(bodies) == 0 {
		p.replies = []string{"{}"}
	} else {
		p.replies = append([]string(nil), bodies...)
	}
	p.errs = nil
	p.index = 0
	return p
}

// Fail makes every call return err, which is how a test exercises the paths
// that matter most: what your code does when the provider is unreachable.
func (p *Provider) Fail(err error) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err == nil {
		err = fmt.Errorf("schemafluxtest: provider unavailable")
	}
	p.errs = []error{err}
	p.index = 0
	return p
}

// FailThen makes the first calls fail and the rest succeed with body, for
// testing retry behaviour.
func (p *Provider) FailThen(failures int, err error, body string) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err == nil {
		err = fmt.Errorf("schemafluxtest: provider unavailable")
	}
	p.errs = make([]error, failures)
	for i := range p.errs {
		p.errs[i] = err
	}
	p.replies = []string{body}
	p.index = 0
	return p
}

// Slow makes every call take at least d, for testing timeouts and
// cancellation. The wait is cancellable, so a test that cancels does not sit
// through it.
func (p *Provider) Slow(d time.Duration) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delay = d
	return p
}

// WithUsage sets the token usage every reply reports, for testing cost
// accounting.
func (p *Provider) WithUsage(usage schemaflux.TokenUsage) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usage = usage
	return p
}

// As sets the provider name, which decides the model the library resolves.
// The default is "local", the name reserved for an in-process double; a name
// the library does not know resolves to no model at all and every call errors.
func (p *Provider) As(name string) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name = name
	return p
}

// Requests returns every request the provider received, in order. This is how a
// test checks what its code actually sent — the prompt, the model, the response
// format — rather than only what came back.
func (p *Provider) Requests() []schemaflux.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]schemaflux.CompletionRequest(nil), p.requests...)
}

// LastRequest returns the most recent request, or the zero value if there was
// none.
func (p *Provider) LastRequest() schemaflux.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return schemaflux.CompletionRequest{}
	}
	return p.requests[len(p.requests)-1]
}

// CallCount returns how many calls were made.
func (p *Provider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// Reset clears the recorded requests and rewinds the reply sequence.
// ReplyFunc answers each call from the request it was sent, rather than from a
// fixed list.
//
// Reply covers the common case, where the answer does not depend on the
// question. It cannot cover the case where it does: a caller that batches work
// -- splitting a catalogue into chunks and asking about each -- has to answer
// with the keys THAT chunk asked about, and a fixed list cannot know which
// those are without the fixture reimplementing the batching it is supposed to
// be testing -- at which point the fixture shares the bug it was meant to
// catch.
//
// The function receives the zero-based call index and the request. Returning an
// error fails that call. It takes precedence over Reply and over Shaped;
// scripted errors from Fail and FailThen are still applied first, so a test can
// combine "fail twice, then answer from the request".
func (p *Provider) ReplyFunc(fn func(call int, req schemaflux.CompletionRequest) (string, error)) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replyFunc = fn
	return p
}

// Contexts returns the context each call arrived on, in call order.
//
// A context is not part of a prompt, so recording requests but not contexts is
// a defensible default -- until a caller puts a per-operation deadline on its
// calls, at which point the context handed to the provider is the only place
// that deadline is observable. Asserting on the caller's own context proves
// only that the caller built what it built; whether it reached the provider is
// a different question, and the answer is here.
func (p *Provider) Contexts() []context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]context.Context(nil), p.contexts...)
}

// ContextN returns the context of call n, or a background context when that
// call never happened.
//
// Background rather than nil, so an assertion on a call that did not occur
// reads a missing deadline and reports the real failure, instead of panicking
// somewhere unrelated.
func (p *Provider) ContextN(n int) context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n < 0 || n >= len(p.contexts) {
		return context.Background()
	}
	return p.contexts[n]
}

func (p *Provider) Reset() *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = nil
	p.contexts = nil
	p.index = 0
	return p
}

// Complete implements schemaflux.Provider.
func (p *Provider) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.contexts = append(p.contexts, ctx)
	position := p.index
	p.index++
	replyFunc := p.replyFunc

	delay := p.delay
	errs := p.errs
	replies := p.replies
	usage := p.usage
	name := p.name
	p.mu.Unlock()

	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return schemaflux.CompletionResponse{}, ctx.Err()
		}
	}

	// A cancelled context is reported even without a delay, so a test can check
	// that its cancellation reached the call.
	if err := ctx.Err(); err != nil {
		return schemaflux.CompletionResponse{}, err
	}

	if position < len(errs) {
		return schemaflux.CompletionResponse{}, errs[position]
	}
	if len(errs) > 0 && len(replies) == 0 {
		return schemaflux.CompletionResponse{}, errs[len(errs)-1]
	}

	if replyFunc != nil {
		body, err := replyFunc(position, req)
		if err != nil {
			return schemaflux.CompletionResponse{}, err
		}
		return schemaflux.CompletionResponse{
			Content:      body,
			Model:        req.Model,
			Provider:     name,
			FinishReason: "stop",
			Usage:        usage,
		}, nil
	}

	body := "{}"
	if p.shaped && len(replies) == 0 {
		// No scripted reply, and the caller asked for shape rather than
		// silence: answer whatever the request declared. `{}` is a valid JSON
		// object and an invalid answer to almost every operation, so a test
		// that only cares about *what was sent* would otherwise have to script
		// a body per operation to get past the parse.
		if shaped := llm.ShapedMockResponse(req); shaped != "" {
			body = shaped
		}
	}
	if len(replies) > 0 {
		replyIndex := position - len(errs)
		if replyIndex < 0 {
			replyIndex = 0
		}
		if replyIndex >= len(replies) {
			replyIndex = len(replies) - 1
		}
		body = replies[replyIndex]
	}

	return schemaflux.CompletionResponse{
		Content:      body,
		Model:        req.Model,
		Provider:     name,
		FinishReason: "stop",
		Usage:        usage,
	}, nil
}

// Name implements schemaflux.Provider.
func (p *Provider) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.name
}

// EstimateCost implements schemaflux.Provider. A double costs nothing.
func (p *Provider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }

// RetryPolicy implements schemaflux.Provider. No retries by default, so a test
// that scripts one failure sees one failure.
func (p *Provider) RetryPolicy() (int, time.Duration) { return 0, 0 }

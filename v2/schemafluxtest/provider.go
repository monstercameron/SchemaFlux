package schemafluxtest

import (
	"bytes"
	"context"
	"iter"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

const (
	// providerName is the name of a Provider that was not given another.
	providerName = "test"

	// modelName is the one model a Provider knows.
	modelName = "test-model"
)

// A Provider is a scripted model. The zero value is ready to use: it is named
// "test", knows one model, "test-model", and answers every request with "{}"
// until it is scripted. It records every request it is sent, and it retries
// nothing: it tells the library its own retry setting is none, so a test that
// scripts one failure sees one request.
//
// A Provider takes the tool declarations and the message list of a request, which
// a fake may, and records them: a chat session is tested against it. To script a
// reply that is only tool calls, Respond returns a Response that has ToolCalls and
// no Content; to script one that is text, Reply or a Response with Content. The
// calls it returns are not checked against the tools of the request: a test that
// scripts an undeclared tool is testing how the library refuses one.
//
// A Provider is safe for concurrent use. It must not be copied after first use.
// Unlike the library's own types it is set up with methods and not with
// fields, because a test changes its script while requests are in flight, which
// needs a lock.
//
// Scripting a Provider has a single rule: Reply, Fail, FailThen, Shape, Respond
// and RespondContext each set how the Provider answers a request that no
// ReplyWhen claims,
// and the last of them called wins. ReplyWhen adds a claim, which is tried
// before the default, in the order they were added. A Provider whose only
// script is ReplyWhen has no answer for a request nothing claims, and fails it
// (see the package comment), where a Provider with no script at all answers
// "{}".
type Provider struct {
	mu sync.Mutex

	// name is the name Name returns. Empty means providerName.
	name string

	// usage is the token count every answer reports. The zero value means none.
	usage schemaflux.Usage

	// delay is how long each request waits before it is answered.
	delay time.Duration

	// scripted is true once one of the default scripts has been set. Until then
	// the default is the empty object.
	scripted bool

	// steps are the default script's answers in order. The last repeats. Empty
	// when the default script is a function or none.
	steps []step
	taken int

	// respond, when not nil, is the default script.
	respond func(ctx context.Context, call int, req schemaflux.Request) (schemaflux.Response, error)

	// label names the default script in the message of a failed script.
	label string

	// rules are the claims added by ReplyWhen, in order.
	rules []*rule

	// requests are the requests received, in the order they arrived.
	requests []schemaflux.Request
}

// A step is one scripted answer: a body, an error, or a body shaped from the
// request.
type step struct {
	body   string
	err    error
	shaped bool
}

// A rule is a ReplyWhen: the bodies to answer with the requests that mention
// about.
type rule struct {
	about string // lower case
	steps []step
	taken int
}

// matches reports whether the request's schema name or system prompt mentions
// the rule's text, without regard to case.
func (r *rule) matches(req schemaflux.Request) bool {
	return strings.Contains(strings.ToLower(req.SchemaName), r.about) ||
		strings.Contains(strings.ToLower(req.System), r.about)
}

// next returns the rule's next answer and counts it as taken. The last repeats.
func next(steps []step, taken *int) step {
	s := steps[min(*taken, len(steps)-1)]
	*taken++
	return s
}

// Reply sets the bodies the Provider answers with, in order: the first request
// gets the first body, the second the second, and the last body repeats once the
// list is used up, so a test that makes an unknown number of requests does not
// have to count them. With no body it answers "{}". Reply replaces whatever
// default script was set before, and starts the sequence again.
func (p *Provider) Reply(bodies ...string) {
	steps := make([]step, 0, max(len(bodies), 1))
	for _, body := range bodies {
		steps = append(steps, step{body: body})
	}
	if len(steps) == 0 {
		steps = append(steps, step{body: "{}"})
	}
	p.setDefault(steps, nil, "Reply with "+count(len(steps), "body", "bodies"))
}

// ReplyWhen answers the requests that are about the operation named by about,
// with bodies in order, the last repeating, as Reply does. A request is about
// it when its SchemaName or its System prompt contains about, without regard to
// case: Extract names its schema for the Go type it fills, and every other
// operation's system prompt is fixed. The user message is not matched, because
// it holds the caller's data: a test that answers by what was sent uses
// Respond.
//
// Claims are tried in the order they were added, and the first that matches
// answers, so the more specific text goes first. A request no claim matches is
// answered by the default script (Reply, Fail, FailThen, Shape, Respond or RespondContext) or,
// when none was set, fails: see the package comment. Each claim counts the
// requests that matched it alone. With no body it answers "{}".
//
// ReplyWhen panics if about is empty, which would claim every request and hide
// every later claim.
func (p *Provider) ReplyWhen(about string, bodies ...string) {
	if about == "" {
		panic("schemafluxtest: ReplyWhen needs the text to look for in the request, and was given none")
	}
	steps := make([]step, 0, max(len(bodies), 1))
	for _, body := range bodies {
		steps = append(steps, step{body: body})
	}
	if len(steps) == 0 {
		steps = append(steps, step{body: "{}"})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = append(p.rules, &rule{about: strings.ToLower(about), steps: steps})
}

// Respond sets a function that answers every request that no ReplyWhen claims,
// from the request itself. It is the general form: the Response it returns is
// handed back exactly as it is, so a test scripts any field of it, such as a
// FinishReason of "length" for an answer cut short, a Usage that differs from
// call to call, or a Model. The function's error is returned as it is.
//
// call is the zero-based position of the request among those the Provider has
// received since it was made or last Reset, in the order they arrived. The
// function runs on the goroutine of the request, with no lock held, after any
// Delay; it must be safe for concurrent use if requests overlap. Respond panics
// if f is nil: there is no way to say "no script" with it, and a nil that
// cleared the script and left the Provider answering "{}" hid a test that had
// meant to script something. Reply with no body sets the default answer again.
// A function that waits, or whose answer depends on the request being cancelled,
// uses RespondContext.
func (p *Provider) Respond(f func(call int, req schemaflux.Request) (schemaflux.Response, error)) {
	if f == nil {
		panic("schemafluxtest: Respond needs a function, and was given none; Reply with no body sets the default answer")
	}
	p.setDefault(nil, func(_ context.Context, call int, req schemaflux.Request) (schemaflux.Response, error) {
		return f(call, req)
	}, "Respond")
}

// RespondContext is Respond for a function that is given the context of the
// request, which is how a scripted provider tells that the call was cancelled or
// timed out, and waits as a real one does (see Wait). The context is the one the
// library gave Complete: a deadline, a cancellation and the values of the call.
// Like Respond it panics if f is nil, and it replaces whatever default script was
// set before.
func (p *Provider) RespondContext(f func(ctx context.Context, call int, req schemaflux.Request) (schemaflux.Response, error)) {
	if f == nil {
		panic("schemafluxtest: RespondContext needs a function, and was given none; Reply with no body sets the default answer")
	}
	p.setDefault(nil, f, "RespondContext")
}

// Wait waits d, or until ctx ends, whichever is first, and returns nil in the
// first case and the context's error in the second. It is the wait a function
// given to RespondContext puts in front of one request's answer to make that
// request slow, which Delay does for every request. It is a timer on the context
// and nothing else, so it runs on the fake clock inside a testing/synctest
// bubble. A d of zero or less returns at once, nil unless ctx has already ended.
func Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Fail makes every request fail with err. To script a kind, give an *Error:
//
//	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429, RetryAfter: time.Second})
//
// A bare Kind is also an error and also decides the kind. A nil err is the
// provider being unavailable, which is how the library reads a failure that
// says nothing of what it was.
func (p *Provider) Fail(err error) {
	p.setDefault([]step{{err: failure(err)}}, nil, "Fail")
}

// FailThen makes the first failures requests fail with err, and every request
// after them answer body, which repeats. Because the Provider retries nothing
// itself, it is how a test shows what a retry middleware or a repair loop does
// with a failure that passes. A negative failures is none. A nil err is the
// provider being unavailable.
func (p *Provider) FailThen(failures int, err error, body string) {
	steps := make([]step, 0, max(failures, 0)+1)
	for range max(failures, 0) {
		steps = append(steps, step{err: failure(err)})
	}
	steps = append(steps, step{body: body})
	p.setDefault(steps, nil, "FailThen with "+count(max(failures, 0), "failure", "failures"))
}

// failure is err, or an unavailable provider for nil.
func failure(err error) error {
	if err != nil {
		return err
	}
	return &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the test scripted a failure and gave no error"}
}

// Shape makes the Provider answer each request with a value that fits the
// request's own declared shape: its JSON Schema, or the JSON template in its
// system prompt. A collection operation is answered with its own items
// unchanged, which every relational check accepts. The values are plainly
// synthetic. It is for a test about what was sent, that needs a request to get
// past the parse and has no opinion on the answer.
//
// A request that declares no shape is not answered with an empty object: it
// fails, with a message that says so, because a test that asked for a shaped
// answer and did not get one has learned nothing about its code.
func (p *Provider) Shape() {
	p.setDefault([]step{{shaped: true}}, nil, "Shape")
}

// setDefault replaces the default script.
func (p *Provider) setDefault(steps []step, respond func(context.Context, int, schemaflux.Request) (schemaflux.Response, error), label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps, p.taken = steps, 0
	p.respond = respond
	p.label = label
	p.scripted = true
}

// Delay makes every request wait d before it is answered, for a test of a
// timeout, a hedge or a cancellation. The wait ends at once if the request's
// context ends, and the request then fails with the context's error, as a real
// provider's does. It is a timer on the request's context and nothing else, so
// it runs on the fake clock inside a testing/synctest bubble. A Delay of zero or
// less is none. The wait applies to every answer, a failure included, and the
// request is counted when it arrives, not when it is answered.
func (p *Provider) Delay(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delay = d
}

// SetUsage sets the token count every answer reports, for a test of cost
// accounting. The zero Usage means the provider reports none, which is not a
// count of zero tokens. It has no effect on an answer a Respond function builds.
// A cost is worked out only for a model with a price: register one with
// pricing.Register.
func (p *Provider) SetUsage(u schemaflux.Usage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usage = u
}

// SetName sets the name Name returns, which is the name a data policy, a price
// table and a route declaration know the provider by. Empty means "test".
//
// It exists for parallel tests that touch process state: pricing.Register and
// provider.DeclareRoute have no undo, so a test that uses either gives its
// fake a name of its own and registers under it, and no other test sees it.
func (p *Provider) SetName(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.name = name
}

// Name returns the provider's name: "test" unless SetName set another.
func (p *Provider) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.name == "" {
		return providerName
	}
	return p.name
}

// Requests returns every request the Provider received, in the order they
// arrived, whatever became of them: one that was failed, or cancelled in its
// Delay, is there. The slice is a copy, and so are the Tools and the Messages of
// each request, their calls and the bytes of the calls' arguments: what a test
// does to what it read changes nothing the next read sees, and what a caller does
// to its own history after a turn changes nothing recorded. A request's Schema
// map, a tool's Parameters and the Decision are shared with the library and are
// read-only.
func (p *Provider) Requests() []schemaflux.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]schemaflux.Request, len(p.requests))
	for i, req := range p.requests {
		out[i] = cloneRequest(req)
	}
	return out
}

// Last returns the most recent request, with its Tools and Messages as Requests
// copies them, or the zero Request when there has been none.
func (p *Provider) Last() schemaflux.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return schemaflux.Request{}
	}
	return cloneRequest(p.requests[len(p.requests)-1])
}

// cloneRequest is req with its tool list, its message list, the calls of each
// message and the bytes of each call's arguments copied. A nil list stays nil and
// an empty one stays empty.
func cloneRequest(req schemaflux.Request) schemaflux.Request {
	req.Tools = cloneTools(req.Tools)
	req.Messages = cloneMessages(req.Messages)
	return req
}

// cloneTools is a copy of tools. A tool's parameters are shared: they are
// read-only.
func cloneTools(tools []schemaflux.Tool) []schemaflux.Tool {
	if tools == nil {
		return nil
	}
	return slices.Clone(tools)
}

// cloneMessages is a copy of messages that shares nothing with it but the
// schemas of tools: the list, each message's call list and the bytes of each
// call's arguments are new.
func cloneMessages(messages []schemaflux.Message) []schemaflux.Message {
	if messages == nil {
		return nil
	}
	out := make([]schemaflux.Message, len(messages))
	for i, m := range messages {
		if m.ToolCalls != nil {
			calls := make([]schemaflux.ToolCall, len(m.ToolCalls))
			for j, c := range m.ToolCalls {
				c.Arguments = bytes.Clone(c.Arguments)
				calls[j] = c
			}
			m.ToolCalls = calls
		}
		out[i] = m
	}
	return out
}

// Calls returns how many requests the Provider has received.
func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// Reset forgets the requests received and starts every script again from its
// first answer. The scripts, the name, the usage and the delay stay.
func (p *Provider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = nil
	p.taken = 0
	for _, r := range p.rules {
		r.taken = 0
	}
}

// Complete records the request and answers it as the script says. It is the
// Provider's implementation of schemaflux.Provider, which the library calls; a
// test calls it directly only to see what a script does.
//
// The request is counted when it arrives, before any Delay, and the script's
// next answer is taken then, under the Provider's lock, so requests that arrive
// together are each counted once and each answered with a different step of a
// sequence. A request whose context has ended fails with the context's error
// and is still counted.
func (p *Provider) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	p.mu.Lock()
	call := len(p.requests)
	p.requests = append(p.requests, cloneRequest(req))
	delay, usage := p.delay, p.usage
	answer, respond, covered := p.choose(req)
	var unanswered error
	if !covered {
		unanswered = scriptFailure("no answer is scripted for request " + strconv.Itoa(call+1) +
			", which asks for " + describeRequest(req) + "; scripted: " + p.describeScript())
	}
	label := p.label
	p.mu.Unlock()

	if err := Wait(ctx, delay); err != nil {
		return schemaflux.Response{}, err
	}
	if unanswered != nil {
		return schemaflux.Response{}, unanswered
	}
	if respond != nil {
		return respond(ctx, call, req)
	}
	if answer.err != nil {
		return schemaflux.Response{}, answer.err
	}
	body := answer.body
	if answer.shaped {
		body = shapedBody(req)
		if body == "" {
			return schemaflux.Response{}, scriptFailure(label + " found nothing to shape an answer from in request " +
				strconv.Itoa(call+1) + ", which asks for " + describeRequest(req) +
				": it declares no schema and no JSON template in its system prompt")
		}
	}
	return schemaflux.Response{
		Content:      body,
		Model:        modelOf(req),
		FinishReason: "stop",
		Usage:        usage,
	}, nil
}

// choose picks the script that answers req and takes its next answer. covered
// is false when no script does. The caller holds the lock.
func (p *Provider) choose(req schemaflux.Request) (answer step, respond func(context.Context, int, schemaflux.Request) (schemaflux.Response, error), covered bool) {
	for _, r := range p.rules {
		if r.matches(req) {
			return next(r.steps, &r.taken), nil, true
		}
	}
	switch {
	case p.respond != nil:
		return step{}, p.respond, true
	case len(p.steps) > 0:
		return next(p.steps, &p.taken), nil, true
	case !p.scripted && len(p.rules) == 0:
		return step{body: "{}"}, nil, true
	}
	return step{}, nil, false
}

// describeScript lists what the Provider is scripted to answer, for the message
// of a request nothing covers. The caller holds the lock.
func (p *Provider) describeScript() string {
	const listed = 5
	var parts []string
	for i, r := range p.rules {
		if i == listed {
			parts = append(parts, "and "+count(len(p.rules)-listed, "more claim", "more claims"))
			break
		}
		parts = append(parts, "ReplyWhen "+quoted(r.about, 24)+" ("+count(len(r.steps), "body", "bodies")+")")
	}
	if p.label != "" {
		parts = append(parts, p.label)
	} else {
		parts = append(parts, "no answer for any other request")
	}
	return strings.Join(parts, ", ")
}

// shapedBody is an answer that fits what req declares, or "" when it declares
// nothing a body can be shaped from.
func shapedBody(req schemaflux.Request) string {
	return llm.ShapedMockResponse(llm.CompletionRequest{
		Model:          req.Model,
		SystemPrompt:   req.System,
		UserPrompt:     req.User,
		ResponseFormat: req.Format,
		JSONSchema:     req.Schema,
		SchemaName:     req.SchemaName,
	})
}

// modelOf is the model a Provider reports for a request: the one asked for, or
// the one it knows when none was.
func modelOf(req schemaflux.Request) string {
	if req.Model != "" {
		return req.Model
	}
	return modelName
}

// RetryPolicy implements provider.Retrier. It returns a negative number of
// retries, which means never: the library does not send a request to a
// Provider again, so a test that scripts one failure sees one request and one
// failure, and a test of retry puts the retrying middleware in front of it.
func (p *Provider) RetryPolicy() (retries int, backoff time.Duration) { return -1, 0 }

// ModelFor returns the model the Provider means by a tier: "test-model", for
// every tier, which is the one model it knows. A Provider written outside the
// library has no model for a tier and would need the client's Model; the fake
// names one so that a test needs no setting to make a call.
func (p *Provider) ModelFor(tier schemaflux.Tier) string { return modelName }

// streamPiece is the most runes of an answer a Stream puts in one chunk. It is
// small, so that an answer of any size an operation reads comes in several
// pieces and a test of code that assembles them has something to assemble.
const streamPiece = 4

// Stream answers the request as Complete does, and delivers the answer in
// pieces: chunks of at most four runes each, which never split a character,
// and then a last chunk that has Done set and holds the whole Response, the
// answer's text in its Content. It makes a Provider a provider.Streamer, so a
// streamed operation is tested without a stub of its own.
//
// The request goes the way any other does: the script that answers it, the
// Delay it waits and the failure it is given are Complete's, and it is recorded.
// It is counted when the sequence is first ranged over, not when Stream is
// called, and each range over the sequence sends a request of its own. A
// failure, whether scripted or the context's, is yielded in place of the
// chunks and ends the sequence: a stream that breaks is an error and never a
// short answer, and no chunk comes before it. An answer with no text is the last
// chunk alone. A caller that stops ranging early has still sent its request.
func (p *Provider) Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) {
		resp, err := p.Complete(ctx, req)
		if err != nil {
			yield(schemaflux.Chunk{}, err)
			return
		}
		runes := []rune(resp.Content)
		for len(runes) > 0 {
			n := min(streamPiece, len(runes))
			if !yield(schemaflux.Chunk{Delta: string(runes[:n])}, nil) {
				return
			}
			runes = runes[n:]
		}
		yield(schemaflux.Chunk{Done: true, Response: resp}, nil)
	}
}

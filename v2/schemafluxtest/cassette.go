package schemafluxtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/provider"
)

// Cassette is one recorded exchange: what a provider was asked and what it
// answered, or how it failed. A Tape makes them, Save writes each one as a JSON
// file, and a Player answers requests from them with no network and no key.
//
// # Read this before you commit a cassette
//
// A cassette is a copy of the conversation, not a summary of it. It holds, in
// clear text:
//
//   - the system prompt and the user message of every request. The user message
//     is the caller's data: the invoice, the ticket, the note that was sent. For
//     a decision it is also Decision.State, the questions and their options.
//   - the model's answer, in full, and whatever tool call or typed answer it
//     gave.
//   - the tools the request declared, in full: each one's name, description and
//     parameters schema.
//   - the conversation the request carried, in full: every message, the calls an
//     assistant message asked for and the results of the tools that answered them.
//     A tool's arguments and results are stored in full and in clear text, as the
//     user message is: they are the caller's data, which is what a tool is for
//     (an order number, a customer's record), and a conversation that used a
//     tool to read private data holds it here.
//   - the facts of the request that change the answer: the model, the
//     temperature, the output ceiling, the format, the schema's name, the
//     effort, whether web search was on.
//
// It holds the schema only as a SHA-256 digest, so that a replay can tell that it
// changed; the cache key and the timing of the call are not kept at all. It
// holds nothing of a failure but its kind, its HTTP status and the wait the
// provider asked for: nothing a provider wrote, so nothing it quoted back from
// the request. No API key, no authorization header and no request id the
// provider minted is part of a request or a response, so none is in a cassette.
//
// Save replaces the shapes of credentials (API keys, bearer headers, private key
// blocks) in the text it writes, and refuses to write a file that still holds
// one. It cannot know what else is private: names, addresses and account
// numbers in the prompts and the answers are written as they are. Read the files
// before committing them, and record against data you are willing to publish.
//
// # What a replay matches
//
// A Player answers a request with the first exchange it has not played whose
// request is the same in every field above: the model, both prompts, the
// temperature, the output ceiling, the format, the schema and its name, the
// effort and its preference, web search, and the decision. A prompt is compared
// after the same credential replacement Save makes, so a request that held a key
// still matches the file that was saved from it. The tools and the messages are
// compared in full, in order: a conversation that differs in one result, or in the
// arguments of one earlier call, is another conversation and is not answered. A
// tool's parameters are compared by their JSON, so a schema with numbers matches
// the file written from it.
//
// # The file
//
// Each exchange is a file of its own, numbered in the order it was recorded
// (000.json, 001.json, and so on), as JSON in a fixed order with two-space
// indentation and a final newline. Writing the same exchanges again writes the
// same bytes: there is no timestamp and no map whose order could vary. The
// "version" member is 1; a file of another version, a file with a member this
// version does not know and a file that is cut short are each refused by Replay,
// naming the file.
//
// An empty slice and a nil one are the same in a cassette: a Response whose
// Answers is empty comes back with Answers nil, and a request with an empty list
// of tools matches one with none.
type Cassette struct {
	// Provider is the name of the provider that was asked, as its Name method
	// reported it when the exchange was recorded. A Player named after a
	// recording reports it.
	Provider string

	// Request is what was sent.
	Request CassetteRequest

	// Response is what came back, when the request succeeded. Exactly one of
	// Response and Failure is set in a cassette that can be played.
	Response *CassetteResponse

	// Failure is what came back when it did not.
	Failure *CassetteFailure
}

// CassetteRequest is the part of a schemaflux.Request that decides the answer,
// as a Cassette keeps it. Each field has the field of the same name in the
// request, and says here only where it differs from it. Everything in it that is
// text is the caller's own and is stored in clear text: see Cassette.
//
// The request's CacheKey is not kept: it is a hint to the provider's cache, it
// is derived from the prompts, and it does not change the answer.
type CassetteRequest struct {
	// Model is the model the request asked for. Empty means the provider's own.
	Model string

	// System is the system prompt.
	System string

	// User is the user message: the caller's data.
	User string

	// Temperature is the sampling temperature. Zero leaves the provider's default.
	Temperature float64

	// MaxTokens is the output ceiling. Zero leaves the provider's default.
	MaxTokens int

	// Format is "json", "text" or empty.
	Format string

	// SchemaName is the schema's name on the wire. Empty when the request
	// declared no schema.
	SchemaName string

	// Schema is the digest of the request's JSON Schema, "sha256:" and 64 hex
	// digits of the SHA-256 of its JSON encoding, which orders the members of a
	// map. It is empty when the request declared no schema. The schema itself is
	// derived from the caller's Go type and would make every file large; a
	// digest tells a replay that it changed.
	Schema string

	// Effort is the reasoning effort. Empty is the provider's default.
	Effort schemaflux.Effort

	// EffortPreferred says whose wish Effort is: true, the operation's.
	EffortPreferred bool

	// WebSearch says the model could search the web.
	WebSearch bool

	// Decision is what the operation was deciding, typed, or nil. It holds the
	// caller's data in State and the questions in full.
	Decision *schemaflux.Decision

	// Tools are the tools the request declared, with their descriptions and
	// parameters. Nil means none.
	Tools []schemaflux.Tool

	// Messages is the conversation the request carried, oldest first, with the
	// calls of its assistant messages and the results of its tool messages in full.
	// Nil means the request was the single user message in User.
	Messages []schemaflux.Message
}

// CassetteResponse is a recorded success: everything of a schemaflux.Response
// that a caller can read, but its Timing, which measures the HTTP call of the
// recording and would have a replay claim to have made one.
type CassetteResponse struct {
	// Content is the answer's text. Empty from a decision provider.
	Content string

	// Usage is what the provider said it used.
	Usage schemaflux.Usage

	// Model is the model that answered, when the provider said.
	Model string

	// Provider is the provider that answered, when it was not the one asked.
	Provider string

	// Upstream is the provider a gateway passed the request to, when it said.
	Upstream string

	// FinishReason is the provider's word for why the answer ended.
	FinishReason string

	// ServiceTier is the tier the provider said it answered on.
	ServiceTier string

	// Cached and Hedged are the response's own flags of those names.
	Cached bool
	Hedged bool

	// Answers are a decision provider's typed answers, in the order of the
	// questions.
	Answers []schemaflux.DecisionAnswer

	// Escalation says a decision provider was asked first and passed over. Nil
	// means nothing was.
	Escalation *schemaflux.DecisionEscalation

	// ToolCalls are the calls the model asked for instead of, or beside, an
	// answer, as a Response carries them. A Player returns them with their ids.
	ToolCalls []CassetteToolCall
}

// CassetteToolCall is one tool call the model asked for. Arguments are the JSON
// object the model wrote, kept as the text it wrote, so that the credential
// replacement of Save reaches them like any other text.
type CassetteToolCall struct {
	// ID is the model's identifier for the call, which a tool result quotes.
	ID string

	// Name is the tool asked for.
	Name string

	// Arguments is the JSON object of the call's arguments, as text. Empty means
	// the model wrote none.
	Arguments string
}

// CassetteFailure is a recorded failure, as the library would classify it: its
// kind, and the two numbers a caller acts on. It holds no message and no body:
// what a provider writes about a failure can quote the request, and the kind is
// what a test asserts on.
type CassetteFailure struct {
	// Kind is the kind of the failure. It is written to the file by its name, so
	// that a file stays readable and keeps its meaning when a constant moves.
	Kind schemaflux.Kind

	// Status is the HTTP status the provider answered with, or zero.
	Status int

	// RetryAfter is how long the provider asked to wait, or zero.
	RetryAfter time.Duration
}

// Tape is a provider that passes every request to another and keeps each
// exchange as a Cassette, for Save to write. Record makes one.
//
// A Tape is safe for concurrent use. It is a wrapper that does not hide what is
// under it: it implements provider.Unwrapper, so the library finds the wrapped
// provider's retry settings, its models, its warming and its decision limits as
// if the Tape were not there. It implements Stream so that a streamed answer is
// recorded too, as one exchange, when the answer is complete: a stream that is
// abandoned or that breaks before it ends records nothing but a failure.
type Tape struct {
	inner schemaflux.Provider
	dir   string

	mu       sync.Mutex
	recorded []Cassette
}

// Record returns a Tape that sends every request to inner and keeps the
// exchange, for Save to write into dir. It touches no file until Save is
// called, so recording costs nothing in a test that fails before it ends.
//
// Wrap a real provider to record, commit the directory, and replay from it
// afterwards: the recording is the only step that costs money, and it happens
// once. A nil inner is accepted and makes every request fail with an error of
// kind schemaflux.ErrConfiguration.
func Record(inner schemaflux.Provider, dir string) *Tape {
	return &Tape{inner: inner, dir: dir}
}

// Name returns the wrapped provider's name, so that a policy, a price and a
// route are those of the provider that answers.
func (t *Tape) Name() string {
	if t.inner == nil {
		return "record"
	}
	return t.inner.Name()
}

// Unwrap returns the wrapped provider (provider.Unwrapper).
func (t *Tape) Unwrap() schemaflux.Provider { return t.inner }

// Complete sends the request to the wrapped provider, keeps the exchange and
// returns the answer or the error exactly as the provider gave them. A failure
// that is only the caller's own context ending, a cancellation or a deadline, is
// not an exchange of the provider and is not kept.
func (t *Tape) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	if t.inner == nil {
		return schemaflux.Response{}, noProvider()
	}
	resp, err := t.inner.Complete(ctx, req)
	if err != nil {
		t.keepFailure(ctx, req, err)
		return resp, err
	}
	t.keep(Cassette{Provider: t.inner.Name(), Request: cassetteRequestOf(req), Response: cassetteResponseOf(resp)})
	return resp, nil
}

// Stream sends the request to the wrapped provider's stream, when it has one,
// and passes every chunk on. The exchange is kept when the stream completes,
// before its last chunk is delivered; a stream that fails is kept as a failure;
// a stream the caller stops early is not kept. A wrapped provider that cannot
// stream makes it fail with an error of kind schemaflux.ErrUnsupported, which is
// what the library reports for a provider that cannot.
func (t *Tape) Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) {
		streamer, ok := streamerOf(t.inner)
		if !ok {
			yield(schemaflux.Chunk{}, &schemaflux.Error{Kind: schemaflux.ErrUnsupported, Message: t.Name() + " cannot stream"})
			return
		}
		for chunk, err := range streamer.Stream(ctx, req) {
			if err != nil {
				t.keepFailure(ctx, req, err)
				yield(schemaflux.Chunk{}, err)
				return
			}
			if chunk.Done {
				t.keep(Cassette{Provider: t.inner.Name(), Request: cassetteRequestOf(req), Response: cassetteResponseOf(chunk.Response)})
			}
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

// streamerOf is the first provider of the chain starting at p that can stream.
func streamerOf(p schemaflux.Provider) (provider.Streamer, bool) {
	for depth := 0; p != nil && depth < 64; depth++ {
		if streamer, ok := p.(provider.Streamer); ok {
			return streamer, true
		}
		wrapper, ok := p.(provider.Unwrapper)
		if !ok {
			break
		}
		p = wrapper.Unwrap()
	}
	return nil, false
}

// keepFailure keeps a failure of the wrapped provider, unless it is the
// caller's own context ending.
func (t *Tape) keepFailure(ctx context.Context, req schemaflux.Request, err error) {
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return
	}
	t.keep(Cassette{Provider: t.inner.Name(), Request: cassetteRequestOf(req), Failure: failureOf(err)})
}

// keep appends an exchange.
func (t *Tape) keep(c Cassette) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recorded = append(t.recorded, c)
}

// Recorded returns the exchanges kept so far, in the order they finished. The
// slice and what it holds are copies: changing them changes nothing the Tape
// holds. They are the exchanges as they were, before Save replaces credential
// shapes in what it writes, so ReplayFrom(tape.Recorded()...) answers with the
// answers as they came.
func (t *Tape) Recorded() []Cassette {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Cassette, len(t.recorded))
	for i, c := range t.recorded {
		out[i] = cloneCassette(c)
	}
	return out
}

// failureOf is the CassetteFailure for an error a provider returned: the kind
// the library would give it, as the call path reads it, with the status and the
// wait the error carries. An error that says nothing of its kind is the provider
// being unavailable, which is what the library takes it for; the internal kind
// the library's classifier falls back to is not one a provider's failure has.
func failureOf(err error) *CassetteFailure {
	failure := &CassetteFailure{Kind: schemaflux.ErrUnavailable}
	var converted *schemaflux.Error
	if !errors.As(bind.ConvertError("schemafluxtest.record", err), &converted) {
		return failure
	}
	failure.Status, failure.RetryAfter = converted.Status, converted.RetryAfter
	if converted.Kind != schemaflux.ErrInternal || saysItsKind(err) {
		failure.Kind = converted.Kind
	}
	return failure
}

// saysItsKind reports whether the chain of err holds an *schemaflux.Error with a
// kind or a bare schemaflux.Kind, which is a provider saying what failed.
func saysItsKind(err error) bool {
	var own *schemaflux.Error
	var bare schemaflux.Kind
	return errors.As(err, &bare) || (errors.As(err, &own) && own.Kind != 0)
}

// cassetteRequestOf is what a cassette keeps of a request. It is the one place
// where the root's Request is mapped to a CassetteRequest, and cassetteResponseOf
// and responseOf are the same for a Response. The reflection test of the package
// fails the day the root gains a field that none of them maps.
func cassetteRequestOf(req schemaflux.Request) CassetteRequest {
	return CassetteRequest{
		Model:           req.Model,
		System:          req.System,
		User:            req.User,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		Format:          req.Format,
		SchemaName:      req.SchemaName,
		Schema:          schemaDigest(req.Schema),
		Effort:          req.Effort,
		EffortPreferred: req.EffortPreferred,
		WebSearch:       req.WebSearch,
		Decision:        cloneDecision(req.Decision),
		Tools:           cassetteTools(req.Tools),
		Messages:        cassetteMessages(req.Messages),
	}
}

// cassetteTools is a copy of tools as a cassette keeps them: nil for none, and a
// tool's parameters shared, since they are read-only.
func cassetteTools(tools []schemaflux.Tool) []schemaflux.Tool {
	if len(tools) == 0 {
		return nil
	}
	return cloneTools(tools)
}

// cassetteMessages is a copy of messages as a cassette keeps them: nil for none
// and for a message with no calls, with the bytes of each call's arguments copied.
func cassetteMessages(messages []schemaflux.Message) []schemaflux.Message {
	if len(messages) == 0 {
		return nil
	}
	out := cloneMessages(messages)
	for i := range out {
		if len(out[i].ToolCalls) == 0 {
			out[i].ToolCalls = nil
		}
	}
	return out
}

// cassetteCallsOf is the calls of a response as a cassette keeps them: the
// arguments as the text the model wrote, so that the replacement of credential
// shapes reaches them.
func cassetteCallsOf(calls []schemaflux.ToolCall) []CassetteToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]CassetteToolCall, len(calls))
	for i, c := range calls {
		out[i] = CassetteToolCall{ID: c.ID, Name: c.Name, Arguments: string(c.Arguments)}
	}
	return out
}

// callsOfCassette is the calls a Player returns for the calls a cassette kept.
func callsOfCassette(calls []CassetteToolCall) []schemaflux.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schemaflux.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = schemaflux.ToolCall{ID: c.ID, Name: c.Name}
		if c.Arguments != "" {
			out[i].Arguments = json.RawMessage(c.Arguments)
		}
	}
	return out
}

// schemaDigest is the digest a cassette keeps of a schema, or "" for none.
func schemaDigest(schema map[string]any) string {
	if len(schema) == 0 {
		return ""
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return "unencodable"
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// cassetteResponseOf is what a cassette keeps of a response.
func cassetteResponseOf(resp schemaflux.Response) *CassetteResponse {
	return &CassetteResponse{
		Content:      resp.Content,
		Usage:        resp.Usage,
		Model:        resp.Model,
		Provider:     resp.Provider,
		Upstream:     resp.Upstream,
		FinishReason: resp.FinishReason,
		ServiceTier:  resp.ServiceTier,
		Cached:       resp.Cached,
		Hedged:       resp.Hedged,
		Answers:      cloneAnswers(resp.Answers),
		Escalation:   cloneEscalation(resp.Escalation),
		ToolCalls:    cassetteCallsOf(resp.ToolCalls),
	}
}

// responseOf is the Response a cassette's recorded success answers with.
func responseOf(r *CassetteResponse) schemaflux.Response {
	return schemaflux.Response{
		Content:      r.Content,
		Usage:        r.Usage,
		Model:        r.Model,
		Provider:     r.Provider,
		Upstream:     r.Upstream,
		FinishReason: r.FinishReason,
		ServiceTier:  r.ServiceTier,
		Cached:       r.Cached,
		Hedged:       r.Hedged,
		Answers:      cloneAnswers(r.Answers),
		Escalation:   cloneEscalation(r.Escalation),
		ToolCalls:    callsOfCassette(r.ToolCalls),
	}
}

func cloneAnswers(answers []schemaflux.DecisionAnswer) []schemaflux.DecisionAnswer {
	if len(answers) == 0 {
		return nil
	}
	return slices.Clone(answers)
}

func cloneEscalation(e *schemaflux.DecisionEscalation) *schemaflux.DecisionEscalation {
	if e == nil {
		return nil
	}
	c := *e
	return &c
}

func cloneStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return slices.Clone(s)
}

func cloneDecision(d *schemaflux.Decision) *schemaflux.Decision {
	if d == nil {
		return nil
	}
	c := &schemaflux.Decision{State: d.State, Guidance: d.Guidance}
	if len(d.Questions) > 0 {
		c.Questions = make([]schemaflux.DecisionQuestion, len(d.Questions))
		for i, q := range d.Questions {
			q.Options, q.Criteria = cloneStrings(q.Options), cloneStrings(q.Criteria)
			c.Questions[i] = q
		}
	}
	return c
}

// cloneCassette is a copy of c that shares nothing with it.
func cloneCassette(c Cassette) Cassette {
	c.Request.Decision = cloneDecision(c.Request.Decision)
	c.Request.Tools = cassetteTools(c.Request.Tools)
	c.Request.Messages = cassetteMessages(c.Request.Messages)
	if c.Response != nil {
		r := *c.Response
		r.Answers = cloneAnswers(r.Answers)
		r.Escalation = cloneEscalation(r.Escalation)
		r.ToolCalls = slices.Clone(r.ToolCalls)
		if len(r.ToolCalls) == 0 {
			r.ToolCalls = nil
		}
		c.Response = &r
	}
	if c.Failure != nil {
		f := *c.Failure
		c.Failure = &f
	}
	return c
}

// noProvider is the error of a Tape that was given no provider.
func noProvider() error {
	return &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Message: "Record was given no provider to record"}
}

// Player is a provider that answers from recorded exchanges, with no network
// and no key. Replay and ReplayFrom make one.
//
// A request is answered with the first exchange not yet played whose request is
// the same (see Cassette), each exchange once: a request that is made twice
// needs two exchanges. A request no exchange matches is not answered with the
// nearest one: it fails with an error of kind schemaflux.ErrConfiguration that
// says what the request asked, in the words of the operation (its model, schema,
// the beginning of its system prompt, the size of its user message and which
// fields differ from the nearest exchange), and never the user message itself.
// Lenient turns the matching off.
//
// A Player is safe for concurrent use: requests that arrive together are each
// matched against what is left under one lock, so none is answered twice and a
// parallel test of code that makes several calls gets each one its own answer.
// It does not retry: it tells the library its own retry setting is none, so a
// recorded failure is the answer to the call that reaches it. An exchange that
// the library retried when it was recorded is several exchanges, and a client
// with Retries set plays them again.
//
// A Player is named after the provider that was recorded when every exchange
// names the same one, and "replay" otherwise, and it means the recorded model by
// every tier when every request named the same one, so that a client that was
// recorded without a model needs none to replay. It is also a provider.Streamer:
// the recorded answer is delivered in pieces, as the Provider does.
type Player struct {
	mu        sync.Mutex
	exchanges []*exchange
	lenient   bool
	asked     int

	name  string
	model string
}

// exchange is a Cassette with the key a request is matched against, and whether
// it has been played.
type exchange struct {
	cassette Cassette
	key      CassetteRequest
	played   bool
}

// ReplayFrom returns a Player that answers from cassettes already in memory,
// which is what a test that has just recorded some, or that builds its own,
// wants. The cassettes are copied.
func ReplayFrom(cassettes ...Cassette) *Player {
	p := &Player{exchanges: make([]*exchange, len(cassettes))}
	var names, models []string
	for i, c := range cassettes {
		c = cloneCassette(c)
		p.exchanges[i] = &exchange{cassette: c, key: matchKey(c.Request)}
		names = append(names, c.Provider)
		models = append(models, c.Request.Model)
	}
	p.name = "replay"
	if only, ok := sole(names); ok {
		p.name = only
	}
	if only, ok := sole(models); ok {
		p.model = only
	}
	return p
}

// sole is the one value every element of values is, when there is one and it is
// not empty.
func sole(values []string) (string, bool) {
	if len(values) == 0 || values[0] == "" {
		return "", false
	}
	for _, v := range values[1:] {
		if v != values[0] {
			return "", false
		}
	}
	return values[0], true
}

// Lenient turns off request matching: from then on every request is answered
// with the first exchange not yet played, in the order recorded, whatever it
// asks. It is for a test that cares about the answers and not about what was
// asked. Prefer the default: a replay that answers a question nobody asked is a
// test that passes for the wrong reason.
func (p *Player) Lenient() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lenient = true
}

// Remaining returns how many exchanges have not been played, so that a test can
// assert it used all that it expected to.
func (p *Player) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.exchanges {
		if !e.played {
			n++
		}
	}
	return n
}

// Name returns the name of the recorded provider when every exchange names the
// same one, and "replay" otherwise.
func (p *Player) Name() string { return p.name }

// ModelFor returns the model of the recording when every request of it named the
// same one, and the empty string otherwise, which is "no model of its own": a
// call on a Player that was recorded against several models names the one it
// wants with the client's Model.
func (p *Player) ModelFor(tier schemaflux.Tier) string { return p.model }

// RetryPolicy implements provider.Retrier: a negative number of retries, which
// means never.
func (p *Player) RetryPolicy() (retries int, backoff time.Duration) { return -1, 0 }

// Complete answers the request from the recording, as the type's comment says.
// A context that has already ended is reported before an exchange is used, as
// the Provider does, so that a test of a cancellation or a deadline sees what a
// real provider would do and the recording is still there for the call that
// follows.
func (p *Player) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	c, number, err := p.next(ctx, req)
	if err != nil {
		return schemaflux.Response{}, err
	}
	return play(c, number)
}

// Stream answers the request as Complete does and delivers the recorded answer
// in pieces of at most four runes, then a last chunk with Done set and the whole
// Response. A recorded failure is yielded in place of the chunks. The request is
// matched, and an exchange is used, when the sequence is ranged over.
func (p *Player) Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
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

// next chooses the exchange that answers req and marks it played, or says why
// there is none. number is the exchange's position among the recorded ones,
// counted from one.
func (p *Player) next(ctx context.Context, req schemaflux.Request) (c Cassette, number int, err error) {
	if err := ctx.Err(); err != nil {
		return Cassette{}, 0, err
	}
	key := matchKey(cassetteRequestOf(req))

	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked++
	for i, e := range p.exchanges {
		if e.played || (!p.lenient && len(differences(e.key, key)) > 0) {
			continue
		}
		e.played = true
		return e.cassette, i + 1, nil
	}
	return Cassette{}, 0, scriptFailure(p.unmatched(req, key))
}

// unmatched says what a request that no exchange answers asked, and why the
// nearest exchange was not it. The caller holds the lock.
func (p *Player) unmatched(req schemaflux.Request, key CassetteRequest) string {
	asked := "request " + strconv.Itoa(p.asked) + ", which asks for " + describeRequest(req)
	unplayed, nearest, nearestNumber := 0, []string(nil), 0
	for i, e := range p.exchanges {
		if e.played {
			continue
		}
		unplayed++
		diff := differences(e.key, key)
		if nearestNumber == 0 || len(diff) < len(nearest) {
			nearest, nearestNumber = diff, i+1
		}
	}
	switch {
	case len(p.exchanges) == 0:
		return "the replay holds no exchange at all, and " + asked
	case unplayed == 0:
		return "the replay holds no unplayed exchange for " + asked + "; all " +
			count(len(p.exchanges), "exchange", "exchanges") + " were played"
	}
	return "the replay holds no unplayed exchange that matches " + asked + "; the nearest of its " +
		count(unplayed, "unplayed exchange", "unplayed exchanges") + " (exchange " + strconv.Itoa(nearestNumber) +
		") differs in " + strings.Join(nearest, ", ")
}

// play is the answer a recorded exchange gives.
func play(c Cassette, number int) (schemaflux.Response, error) {
	position := "exchange " + strconv.Itoa(number)
	switch {
	case c.Response != nil && c.Failure != nil:
		return schemaflux.Response{}, scriptFailure(position + " of the replay records both a response and a failure")
	case c.Failure != nil:
		kind := c.Failure.Kind
		if kind == 0 {
			kind = schemaflux.ErrUnavailable
		}
		return schemaflux.Response{}, &schemaflux.Error{
			Kind:       kind,
			Provider:   c.Provider,
			Model:      c.Request.Model,
			Status:     c.Failure.Status,
			RetryAfter: c.Failure.RetryAfter,
			Message:    "the failure recorded as " + position + " of the replay",
		}
	case c.Response != nil:
		return responseOf(c.Response), nil
	}
	return schemaflux.Response{}, scriptFailure(position + " of the replay records neither a response nor a failure")
}

// matchKey is a request in the form it is matched in: credential shapes
// replaced, as Save replaces them in a file, and the parameters of each tool in
// the form JSON gives them, so that a schema compares equal to the one a file
// holds.
func matchKey(r CassetteRequest) CassetteRequest {
	r = cloneCassette(Cassette{Request: r}).Request
	r.System, r.User = redactCredentials(r.System), redactCredentials(r.User)
	for i := range r.Tools {
		t := &r.Tools[i]
		t.Name, t.Description = redactCredentials(t.Name), redactCredentials(t.Description)
		t.Parameters = canonicalParameters(t.Parameters)
	}
	for i := range r.Messages {
		m := &r.Messages[i]
		m.Content, m.ToolCallID = redactCredentials(m.Content), redactCredentials(m.ToolCallID)
		for j := range m.ToolCalls {
			c := &m.ToolCalls[j]
			c.ID, c.Name = redactCredentials(c.ID), redactCredentials(c.Name)
			if len(c.Arguments) == 0 {
				c.Arguments = nil
				continue
			}
			c.Arguments = json.RawMessage(redactCredentials(string(c.Arguments)))
		}
	}
	if d := r.Decision; d != nil {
		d.State, d.Guidance = redactCredentials(d.State), redactCredentials(d.Guidance)
		for i := range d.Questions {
			q := &d.Questions[i]
			q.Ask = redactCredentials(q.Ask)
			for j := range q.Options {
				q.Options[j] = redactCredentials(q.Options[j])
			}
			for j := range q.Criteria {
				q.Criteria[j] = redactCredentials(q.Criteria[j])
			}
		}
	}
	return r
}

// canonicalParameters is the schema of a tool in the form a file holds it: what
// encoding/json reads back from what it writes, with a credential shape in a
// string replaced. Numbers become float64 and every list a []any, which is how a
// schema built from Go values and the same schema read from a file come to be one
// value. An empty schema is nil. A schema JSON cannot write is kept as it is: a
// comparison of it will fail, and Save refuses to write it.
func canonicalParameters(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return schema
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return schema
	}
	return redactValues(out).(map[string]any)
}

// redactValues is v with every string inside it, however deep, passed through
// redactCredentials. It builds new maps and lists and never writes to v, which a
// tool may share with the request it came from.
func redactValues(v any) any {
	switch v := v.(type) {
	case string:
		return redactCredentials(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, value := range v {
			out[key] = redactValues(value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = redactValues(value)
		}
		return out
	}
	return v
}

// differences names the fields of schemaflux.Request in which two requests, in
// the form of matchKey, differ.
func differences(recorded, asked CassetteRequest) []string {
	var diff []string
	add := func(differs bool, field string) {
		if differs {
			diff = append(diff, field)
		}
	}
	add(recorded.Model != asked.Model, "Model")
	add(recorded.System != asked.System, "System")
	add(recorded.User != asked.User, "User")
	add(recorded.Temperature != asked.Temperature, "Temperature")
	add(recorded.MaxTokens != asked.MaxTokens, "MaxTokens")
	add(recorded.Format != asked.Format, "Format")
	add(recorded.SchemaName != asked.SchemaName, "SchemaName")
	add(recorded.Schema != asked.Schema, "Schema")
	add(recorded.Effort != asked.Effort, "Effort")
	add(recorded.EffortPreferred != asked.EffortPreferred, "EffortPreferred")
	add(recorded.WebSearch != asked.WebSearch, "WebSearch")
	add(!sameDecision(recorded.Decision, asked.Decision), "Decision")
	add(!reflect.DeepEqual(recorded.Tools, asked.Tools), "Tools")
	add(!reflect.DeepEqual(recorded.Messages, asked.Messages), "Messages")
	return diff
}

// sameDecision reports whether two decisions are the same in every field, a nil
// slice and an empty one being the same.
func sameDecision(a, b *schemaflux.Decision) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.State != b.State || a.Guidance != b.Guidance || len(a.Questions) != len(b.Questions) {
		return false
	}
	for i, q := range a.Questions {
		r := b.Questions[i]
		if q.Kind != r.Kind || q.Ask != r.Ask || q.Low != r.Low || q.High != r.High ||
			!slices.Equal(q.Options, r.Options) || !slices.Equal(q.Criteria, r.Criteria) {
			return false
		}
	}
	return true
}

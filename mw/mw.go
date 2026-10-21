package mw

import (
	"context"
	"errors"
	"iter"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/provider"
)

// A Middleware wraps one Provider in another. The wrapper must behave as the
// provider it wraps does, adding only the behaviour its name says, so a
// middleware that does nothing but call through is the identity function on
// this type.
//
// The values this package returns are middleware; so is any function of this
// shape a program writes itself (with Wrap), and Chain composes both.
type Middleware func(next schemaflux.Provider) schemaflux.Provider

// Chain wraps base in the middleware listed, outermost first: mws[0] is the
// first to see a request and the last to see its answer, and the last
// middleware is the one next to base. A nil middleware is skipped, and with no
// middleware Chain returns base itself.
//
// What goes outside what is not a matter of taste. The package comment says
// which order each pair needs and why.
func Chain(base schemaflux.Provider, mws ...Middleware) schemaflux.Provider {
	wrapped := base
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] == nil {
			continue
		}
		wrapped = mws[i](wrapped)
	}
	return wrapped
}

// Func is the Provider that answers with complete, named name: the
// http.HandlerFunc of this package. It is how a program writes a provider of its
// own or a test double without declaring a type with two methods:
//
//	echo := mw.Func("echo", func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
//		return schemaflux.Response{Content: req.User}, nil
//	})
//
// A Func is a provider at the bottom of a chain: it wraps nothing, so it has no
// Unwrap, and it cannot stream, be warmed or report retry settings. A middleware
// written with Func would hide everything the provider under it can do, since
// what the library finds under a provider it finds through Unwrap; a middleware
// is written with Wrap. A nil complete makes every call fail with an error of
// kind schemaflux.ErrConfiguration.
func Func(name string, complete func(context.Context, schemaflux.Request) (schemaflux.Response, error)) schemaflux.Provider {
	return &funcProvider{name: name, complete: complete}
}

// funcProvider is the Provider Func returns.
type funcProvider struct {
	name     string
	complete func(context.Context, schemaflux.Request) (schemaflux.Response, error)
}

// Complete calls the function.
func (f *funcProvider) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	if f.complete == nil {
		return schemaflux.Response{}, &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Op: "mw.func", Message: "the function that answers is nil"}
	}
	return f.complete(ctx, req)
}

// Name is the name Func was given.
func (f *funcProvider) Name() string { return f.name }

// Wrap is a Middleware of a program's own made around next: the Provider that
// answers with complete, which is handed next to call or not. It is how a
// program writes a layer without declaring a type with a method for each thing a
// layer has to keep:
//
//	logged := func(next schemaflux.Provider) schemaflux.Provider {
//		return mw.Wrap(next, func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error) {
//			log.Println("request to", next.Name())
//			return next.Complete(ctx, req)
//		})
//	}
//
// What Func cannot do, Wrap does: the Provider it returns is named for next, and
// its Unwrap is next, so the library finds what the provider underneath can do
// beyond Complete (that it can be warmed, the model it means by a tier, the
// limits of a decision provider, its retry settings) through the layer. Streaming
// does not pass through, as it does not through any layer of this package: a
// layer acts on a whole request and a whole answer, and a stream that went round
// it would be sent past what the layer does. A nil next or a nil complete makes
// every call fail with an error of kind schemaflux.ErrConfiguration.
func Wrap(next schemaflux.Provider, complete func(ctx context.Context, req schemaflux.Request, next schemaflux.Provider) (schemaflux.Response, error)) schemaflux.Provider {
	return &layer{next: next, complete: complete}
}

// layer is the Provider Wrap returns.
type layer struct {
	next     schemaflux.Provider
	complete func(context.Context, schemaflux.Request, schemaflux.Provider) (schemaflux.Response, error)
}

// Complete calls the function with the wrapped provider.
func (l *layer) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	switch {
	case l.next == nil:
		return schemaflux.Response{}, misconfigured("mw.wrap", "there is no provider to wrap")
	case l.complete == nil:
		return schemaflux.Response{}, misconfigured("mw.wrap", "the function that answers is nil")
	}
	if err := contextRefusal(ctx); err != nil {
		return schemaflux.Response{}, err
	}
	return l.complete(ctx, req, l.next)
}

// contextRefusal is the error of a call that is over before it starts, or nil: a
// nil context is an invalid request, and one that has ended is its error.
func contextRefusal(ctx context.Context) error {
	if ctx == nil {
		return &schemaflux.Error{Kind: schemaflux.ErrInvalidRequest, Op: "mw", Message: "the context is nil"}
	}
	if err := ctx.Err(); err != nil {
		return bind.ConvertError("mw", err)
	}
	return nil
}

// Name is the wrapped provider's name.
func (l *layer) Name() string {
	if l.next == nil {
		return ""
	}
	return l.next.Name()
}

// Unwrap is the wrapped provider.
func (l *layer) Unwrap() schemaflux.Provider { return l.next }

// RetryPolicy is the wrapped provider's, in the convention of provider.Retrier,
// or zero, the library's default, when it states none.
func (l *layer) RetryPolicy() (int, time.Duration) {
	if retrier, ok := bind.Optional[provider.Retrier](l.next); ok {
		return retrier.RetryPolicy()
	}
	return 0, 0
}

// Stream refuses, as every layer of this package does.
func (l *layer) Stream(context.Context, schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return refusedStream([]string{"wrap"})
}

// wrapper is the Provider every middleware of this package returns. It holds
// the wrapped package's handler for the whole chain from its layer down, in the
// types of the call path, and the Provider it was made around.
//
// A wrapper that wraps a wrapper does not convert between the two sets of types
// at the join: it takes the handler of the one below as it is. A request is
// converted once on the way in, at the outermost wrapper, and an answer or an
// error once on the way out, so the chain behaves as one handler chain of the
// wrapped package would.
type wrapper struct {
	// handler is the chain from this layer down, in the call path's types.
	handler llm.Provider

	// inner is the Provider this layer wraps. Unwrap returns it, and the
	// optional interfaces of the provider underneath are found through it.
	inner schemaflux.Provider

	// out is handler seen as a Provider. It converts the request, the answer
	// and the error between the two sets of types.
	out schemaflux.Provider

	// layers names this layer and each below it that is of this package, the
	// outermost first, for a message that has to say what is in the chain.
	layers []string
}

// newWrapper is the Provider for handler, a chain made around inner by the layer
// called name.
func newWrapper(name string, inner schemaflux.Provider, handler llm.Provider) *wrapper {
	out, _ := bind.FromInternal(handler).(schemaflux.Provider)
	layers := []string{name}
	if below, ok := inner.(*wrapper); ok {
		layers = append(layers, below.layers...)
	}
	return &wrapper{handler: handler, inner: inner, out: out, layers: layers}
}

// Complete sends req down the chain. A failure is an *schemaflux.Error: a
// provider's keeps its Kind, and a refusal of a layer of this package has the
// Kind its documentation names and the layer's name as its Op. A call that is
// over before it starts is not started: a nil context is refused as an invalid
// request, and one that has ended fails with the context's error, before any
// layer or the provider is asked.
func (w *wrapper) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	if w.out == nil {
		return schemaflux.Response{}, &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Op: "mw", Message: "the library's call path is not linked into this program"}
	}
	if err := contextRefusal(ctx); err != nil {
		return schemaflux.Response{}, err
	}
	resp, err := w.out.Complete(ctx, req)
	return resp, namedRefusal(err)
}

// layerOps maps the operation names the wrapped package gives its refusals to
// the names of this package's layers.
var layerOps = map[string]string{
	"resilience.Bulkhead":       "mw.bulkhead",
	"resilience.CircuitBreaker": "mw.breaker",
	"mw.RateLimit":              "mw.ratelimit",
	"mw.Budget":                 "mw.budget",
	"mw.Retry":                  "mw.retry",
	"mw.Decisions":              "mw.decisions",
	"mw.Cache":                  "mw.cache",
	"hedge":                     "mw.hedge",
	"mw.RedactEgress":           "mw.redact",
}

// namedRefusal gives an error that a layer of this package made the name of the
// layer as its Op. A refusal is converted without one, since the wrapped package
// names its operations differently; the *Error is then copied with the name, and
// everything else in it is as it was. An error that has an Op already, which is
// every error a layer further in named, and every one a provider made, is
// returned as it is.
func namedRefusal(err error) error {
	var e *schemaflux.Error
	if !errors.As(err, &e) || e.Op != "" || e != err {
		return err
	}
	var made *types.OperationError
	if !errors.As(e, &made) {
		return err
	}
	name, ok := layerOps[made.Op]
	if !ok {
		return err
	}
	named := *e
	named.Op = name
	return &named
}

// Name is the wrapped provider's name: a wrapper is that provider as far as a
// price table or a data policy is concerned.
func (w *wrapper) Name() string {
	if w.inner == nil {
		return ""
	}
	return w.inner.Name()
}

// Unwrap is the wrapped provider, so that the library finds what the provider
// underneath can do beyond Complete: that it can be warmed, what it takes as a
// decision, which model it means by a tier. A Fallback's is its primary route,
// and a Decisions pair's is its generating provider.
func (w *wrapper) Unwrap() schemaflux.Provider { return w.inner }

// RetryPolicy is the retry setting of the chain, in the convention of
// provider.Retrier: zero retries is the library's default and a negative number
// is never. A chain that holds a Retry says never, since the repeats are that
// middleware's and a client that repeated the whole chain as well would send as
// many requests as the two counts multiplied; a chain without one says what the
// provider underneath does.
func (w *wrapper) RetryPolicy() (int, time.Duration) {
	retries, backoff := w.handler.RetryPolicy()
	switch {
	case retries < 0:
		retries = 0
	case retries == 0:
		retries = -1
	}
	return retries, backoff
}

// Stream answers that a wrapper cannot stream, and that is its whole job. The
// layers of this package act on a whole request and a whole answer: a retry
// cannot repeat half a stream, a cache has nothing to keep until it ends, a
// budget has nothing to count before the end. The library finds a Streamer
// through Unwrap, so a wrapper that said nothing would hand a stream to the
// provider underneath, past every layer; this method is what stops that. The
// error names the layers that would have been skipped, and says what to do.
func (w *wrapper) Stream(context.Context, schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return refusedStream(w.layers)
}

// refusedStream is a stream that yields the refusal of a wrapper and ends.
func refusedStream(layers []string) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) {
		yield(schemaflux.Chunk{}, &schemaflux.Error{
			Kind: schemaflux.ErrUnsupported,
			Op:   "mw",
			Message: "a provider wrapped by " + strings.Join(layers, ", ") + " cannot stream, since those layers act on whole requests: " +
				"to stream, give the client the provider without them, and none of them will apply to the stream",
		})
	}
}

// internalOf is the handler of the call path for p.
//
// A wrapper of this package gives its own handler, with nothing converted. Any
// other Provider is converted by internal/bind, which carries what the call path
// looks for on a provider: its retry settings, the model of a tier, the model it
// sends, the limits of a decision provider, found through Unwrap when the
// provider is itself a layer over one.
func internalOf(p schemaflux.Provider) llm.Provider {
	switch v := p.(type) {
	case nil:
		return nil
	case *wrapper:
		return v.handler
	}
	return bind.ToInternal(p)
}

// stage is a handler placed around another, for a layer of this package that
// has something to add to the call path's handler. It passes everything but
// Complete through, so that the lookups the call path makes on a chain (the
// limits of a decision provider, the model that is sent) reach what is below.
type stage struct{ next llm.Provider }

// Name is the wrapped handler's.
func (s stage) Name() string { return s.next.Name() }

// EstimateCost is the wrapped handler's.
func (s stage) EstimateCost(req llm.CompletionRequest) float64 { return s.next.EstimateCost(req) }

// RetryPolicy is the wrapped handler's.
func (s stage) RetryPolicy() (int, time.Duration) { return s.next.RetryPolicy() }

// ModelSentFor is the wrapped handler's.
func (s stage) ModelSentFor(requested string) string { return llm.ModelSentBy(s.next, requested) }

// Unwrap is the wrapped handler.
func (s stage) Unwrap() llm.Provider { return s.next }

// failing is a handler whose every call fails with err and sends nothing: what
// a middleware that was set up wrong, or was given nothing to wrap, becomes.
type failing struct {
	stage
	err error
}

// Complete fails with the error.
func (f *failing) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, f.err
}

// wrap is what every middleware does with the Provider it is given: it makes a
// handler of it, builds the wrapped package's middleware around that, and gives
// back the Provider for the result. A problem found in the settings, or the
// absence of a provider, makes a provider whose every call fails with the
// configuration error and sends nothing, since a middleware has no error to
// return. op is the layer's name, "mw.retry" for the Retry layer.
func wrap(op string, next schemaflux.Provider, problem string, build func(llm.Provider) llm.Provider) schemaflux.Provider {
	name := strings.TrimPrefix(op, "mw.")
	if next == nil {
		return newWrapper(name, nil, &failing{stage{next: unnamed{}}, misconfigured(op, "there is no provider to wrap")})
	}
	handler := internalOf(next)
	if problem != "" {
		return newWrapper(name, next, &failing{stage{next: handler}, misconfigured(op, problem)})
	}
	return newWrapper(name, next, build(handler))
}

// misconfigured is the error of a middleware that was set up wrong.
func misconfigured(op, message string) error {
	return &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Op: op, Message: message}
}

// unnamed is the handler under a middleware that was given no provider.
type unnamed struct{}

func (unnamed) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}
func (unnamed) Name() string                               { return "" }
func (unnamed) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (unnamed) RetryPolicy() (int, time.Duration)          { return 0, 0 }

var _ provider.Unwrapper = (*wrapper)(nil)

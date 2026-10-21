package schemaflux

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The optional interfaces of a Provider as the root names them. The provider
// package declares the same methods under the names Warmer, Retrier, Decider
// and Streamer; the root cannot import it, and an interface is satisfied by its
// method set, so a provider written against those is found here.
type (
	optionalWarmer interface {
		Warm(ctx context.Context) error
	}
	optionalRetrier interface {
		RetryPolicy() (retries int, backoff time.Duration)
	}
	optionalDecider  interface{ DecisionLimits() DecisionLimits }
	optionalStreamer interface {
		Stream(ctx context.Context, req Request) iter.Seq2[Chunk, error]
	}
	optionalTierModels interface{ ModelFor(tier Tier) string }

	// internalSeam is what the provider package's adapter of a built-in provider
	// has: the internal provider it was made from.
	internalSeam interface{ InternalProvider() llm.Provider }
)

// internalProvider is a Provider seen as an llm.Provider. It answers each
// optional interface of the call path (warming, streaming, the model of a tier,
// the model sent for a request) from the provider, and then from what the
// provider wraps, so that a wrapper which does not hide what is under it hides
// none of it. under is the built-in provider at the bottom of a chain of
// wrappers, when there is one: a built-in made by the provider package carries
// its internal provider, and that is the one that knows its tier models, its
// streaming and the model it sends.
type internalProvider struct {
	provider Provider
	under    llm.Provider
}

// internalDecider is an internalProvider whose provider, or what it wraps,
// declares decision limits. It is a type of its own because the call path finds
// limits by the method, and a provider that declares none must not have it.
type internalDecider struct{ internalProvider }

// Complete converts the request, calls the provider and converts the answer. An
// error the provider returned is given the kind the call path reads, see
// providerFailure.
//
// A panic in the provider is recovered here and returned as an error of kind
// ErrInternal: the provider is the caller's code, it runs on the goroutine of the
// call, and a panic that crossed the API would take the caller's program down
// from inside an operation. The error names the type of what was thrown and
// nothing of its value, which can hold the request.
func (a *internalProvider) Complete(ctx context.Context, req llm.CompletionRequest) (_ llm.CompletionResponse, failed error) {
	converted, err := requestFromInternal(req)
	if err != nil {
		return llm.CompletionResponse{}, providerFailure(err, a.provider.Name(), req.Model)
	}
	defer func() {
		if thrown := recover(); thrown != nil {
			failed = providerPanic(thrown, a.provider.Name(), req.Model)
		}
	}()
	resp, err := a.provider.Complete(ctx, converted)
	if err != nil {
		return llm.CompletionResponse{}, providerFailure(err, a.provider.Name(), req.Model)
	}
	return responseToInternal(resp), nil
}

// providerPanic is the error for a panic in a Provider: ErrInternal, carrying
// the type of what was thrown. The value is never read, since it can hold the
// request or a vendor's text, and nothing but its type goes into the message.
func providerPanic(thrown any, provider, model string) error {
	return providerFailure(&Error{
		Kind:    ErrInternal,
		Message: "the provider panicked with a value of type " + fmt.Sprintf("%T", thrown),
	}, provider, model)
}

// Name is the provider's own name.
func (a *internalProvider) Name() string { return a.provider.Name() }

// EstimateCost is zero: a Provider has no estimate, and the call path no longer
// asks for one.
func (a *internalProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// RetryPolicy is the provider's own settings when it implements
// provider.Retrier, in the convention of the call path: a Retrier answers zero
// retries for "the library's default" and a negative number for "never", and
// the call path takes a negative number for "not configured" and zero for
// "never". A provider without settings is "not configured".
func (a *internalProvider) RetryPolicy() (int, time.Duration) {
	retrier, ok := bind.Optional[optionalRetrier](a.provider)
	if !ok {
		return -1, 0
	}
	retries, backoff := retrier.RetryPolicy()
	switch {
	case retries == 0:
		retries = -1
	case retries < 0:
		retries = 0
	}
	return retries, backoff
}

// Warm opens the connection of the provider, or of what it wraps, when one of
// them can; a chain with nothing to warm is not an error.
func (a *internalProvider) Warm(ctx context.Context) (failed error) {
	warmer, ok := bind.Optional[optionalWarmer](a.provider)
	if !ok {
		return nil
	}
	defer func() {
		if thrown := recover(); thrown != nil {
			failed = providerPanic(thrown, a.provider.Name(), "")
		}
	}()
	if err := warmer.Warm(ctx); err != nil {
		return providerFailure(err, a.provider.Name(), "")
	}
	return nil
}

// ModelFor is the model the provider, or what it wraps, means by a tier, and
// the empty string when none of them names one: the call path reads that as
// "ask what this wraps" and then as "no model of the provider's own".
func (a *internalProvider) ModelFor(tier types.Speed) string {
	if models, ok := bind.Optional[optionalTierModels](a.provider); ok {
		return models.ModelFor(tierFromInternal(tier))
	}
	if a.under != nil {
		model, _ := llm.ModelFor(a.under, tier)
		return model
	}
	return ""
}

// ModelSentFor is the model the built-in provider under the chain sends for a
// requested one, which is what a data policy is checked against. A chain with
// no built-in at the bottom sends the model as it was asked.
func (a *internalProvider) ModelSentFor(requested string) string {
	if a.under != nil {
		return llm.ModelSentBy(a.under, requested)
	}
	return requested
}

// Unwrap is the built-in provider under the chain, or nil. It is how the call
// path looks through this adapter to what a wrapper hides: the optional
// interfaces of the built-in at the bottom.
func (a *internalProvider) Unwrap() llm.Provider { return a.under }

// CompleteStream streams through the provider, or through what it wraps, when
// one of them is a Streamer, and then through the built-in provider under the
// chain when that streams. With neither it yields the unsupported-capability
// error the call path gives a provider that cannot stream.
func (a *internalProvider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	if streamer, ok := bind.Optional[optionalStreamer](a.provider); ok {
		return func(yield func(llm.StreamChunk, error) bool) {
			converted, err := requestFromInternal(req)
			if err != nil {
				yield(llm.StreamChunk{}, providerFailure(err, a.provider.Name(), req.Model))
				return
			}
			// A panic in the provider's sequence is recovered as one in Complete
			// is. A panic in yield is the consumer's and is not: yielding again
			// from inside a loop body that has panicked is itself a panic.
			inYield := false
			defer func() {
				if thrown := recover(); thrown != nil {
					if inYield {
						panic(thrown)
					}
					yield(llm.StreamChunk{}, providerPanic(thrown, a.provider.Name(), req.Model))
				}
			}()
			for chunk, err := range streamer.Stream(ctx, converted) {
				if err != nil {
					inYield = true
					yield(llm.StreamChunk{}, providerFailure(err, a.provider.Name(), req.Model))
					return
				}
				inYield = true
				if !yield(chunkToInternal(chunk), nil) {
					return
				}
				inYield = false
			}
		}
	}
	if streaming, ok := a.under.(llm.StreamingProvider); ok {
		return streaming.CompleteStream(ctx, req)
	}
	name := a.provider.Name()
	return func(yield func(llm.StreamChunk, error) bool) {
		yield(llm.StreamChunk{}, types.Newf(types.KindUnsupportedCapability, "llm.stream", "%s cannot stream", name))
	}
}

// DecisionLimits is what the provider, or what it wraps, declares, and failing
// that the limits of the built-in provider under the chain.
func (d *internalDecider) DecisionLimits() llm.DecisionLimits {
	if decider, ok := bind.Optional[optionalDecider](d.provider); ok {
		return decider.DecisionLimits().internal()
	}
	limits, _ := llm.DecisionLimitsOf(d.under)
	return limits
}

// externalProvider is an llm.Provider seen as a Provider.
type externalProvider struct{ provider llm.Provider }

// Complete converts the request, calls the provider and converts the answer. An
// error the provider returned becomes an *Error, so that a caller of a
// built-in provider reads its failure the way it reads any other. A request that
// decides and also carries tools or messages is refused before the provider is
// called, as requestFromInternal refuses it on the way to a Provider.
func (a *externalProvider) Complete(ctx context.Context, req Request) (Response, error) {
	if err := refuseDecisionWithTools(req.Decision != nil, len(req.Tools) > 0 || len(req.Messages) > 0); err != nil {
		return Response{}, err
	}
	resp, err := a.provider.Complete(ctx, requestToInternal(req))
	if err != nil {
		return Response{}, fromProviderError(err)
	}
	return responseFromInternal(resp)
}

// InternalProvider is the internal provider this one adapts. It is the seam the
// conversion of a Provider to an internal one looks for, so that a built-in
// provider is not wrapped a second time and keeps what it can do beyond Complete.
func (a *externalProvider) InternalProvider() llm.Provider { return a.provider }

// Name is the provider's own name.
func (a *externalProvider) Name() string { return a.provider.Name() }

// RetryPolicy is the provider's own settings, in the convention of
// provider.Retrier: the call path's "not configured" is zero, which means the
// library's default, and its zero is a negative number, which means never.
func (a *externalProvider) RetryPolicy() (int, time.Duration) {
	retries, backoff := a.provider.RetryPolicy()
	switch {
	case retries < 0:
		retries = 0
	case retries == 0:
		retries = -1
	}
	return retries, backoff
}

// toInternalProvider is the llm.Provider for a Provider, or nil for something
// that is not one.
//
// A Provider that is a built-in one seen as a Provider, whether by this package
// (*externalProvider) or by the provider package, both of which give their
// adapters an InternalProvider method, is given back as the llm.Provider it was
// made from, so a built-in provider keeps everything it can do on the way through
// and is never wrapped a second time.
//
// A Provider that wraps another (it has Unwrap) is wrapped as any other
// Provider is, so that its Complete runs: it is the middleware. What the
// middleware does not do itself is looked for on what it wraps, and the
// built-in provider at the bottom of the chain, if there is one, is reachable
// through the adapter's own Unwrap, so that warming, streaming, the model of a
// tier and the model a request is sent as all reach it.
func toInternalProvider(provider any) llm.Provider {
	p, ok := provider.(Provider)
	if !ok || p == nil {
		return nil
	}
	if seam, ok := p.(internalSeam); ok {
		if inner := seam.InternalProvider(); inner != nil {
			return inner
		}
	}
	adapted := internalProvider{provider: p}
	if seam, ok := bind.Optional[internalSeam](p); ok {
		adapted.under = seam.InternalProvider()
	}
	if _, decides := bind.Optional[optionalDecider](p); decides {
		return &internalDecider{adapted}
	}
	if _, decides := llm.DecisionLimitsOf(adapted.under); decides {
		return &internalDecider{adapted}
	}
	return &adapted
}

// fromInternalProvider is the Provider for an llm.Provider, as an any, or nil
// for nil. An llm.Provider that is a Provider seen as one is given back as the
// Provider it was made from.
func fromInternalProvider(provider llm.Provider) any {
	switch internal := provider.(type) {
	case nil:
		return nil
	case *internalProvider:
		return internal.provider
	case *internalDecider:
		return internal.provider
	}
	return &externalProvider{provider: provider}
}

// unwrapProvider is the provider a Provider wraps, as an any, or nil: the step
// the registered Unwrap hook gives bind.Optional. The method it looks for is
// the one the provider package names Unwrapper.
func unwrapProvider(provider any) any {
	wrapper, ok := provider.(interface{ Unwrap() Provider })
	if !ok {
		return nil
	}
	if inner := wrapper.Unwrap(); inner != nil {
		return inner
	}
	return nil
}

// providerFailure is the error the call path is given for a failure of a
// Provider. An error that already carries an internal kind is the call path's
// own and is returned as it is. An *Error, or a bare Kind, anywhere in the
// chain decides the kind, the outermost first; with neither, the internal
// classifier reads what it knows (a context error, an HTTP status, a network
// timeout), and what it does not know is the provider being unavailable.
//
// The error returned has the kind, the route and the wait the call path reads
// to retry, and the provider's error in its chain, so that what the caller
// finally receives is that error with the operation filled in. A wait the
// provider asked for reaches the retry loop as a rate-limit error would carry
// it.
func providerFailure(err error, provider, model string) error {
	if err == nil {
		return nil
	}
	decided, classified := classifiedBy(err)
	if classified && decided.source != fromLibrary {
		return err
	}

	var kind Kind
	switch {
	case classified:
		kind = decided.kind
	default:
		kind = ErrUnavailable
		if known, ok := kindFromInternal(llm.Classify(err), types.NarrowNone); ok {
			kind = known
		}
	}

	f := factsOf(err, verdict{})
	internalKind, narrow := kindToInternal(kind)
	cause := err
	if f.retryAfter > 0 {
		if _, already := llm.RetryAfterFrom(err); !already {
			cause = errors.Join(err, &llm.RateLimitError{
				APIError:   &llm.APIError{Provider: provider, Model: model, StatusCode: f.status},
				RetryAfter: f.retryAfter,
			})
		}
	}
	if f.provider != "" {
		provider = f.provider
	}
	if f.model != "" {
		model = f.model
	}
	return &types.OperationError{
		Kind:       internalKind,
		Narrow:     narrow,
		Provider:   provider,
		Model:      model,
		RequestID:  f.requestID,
		RetryAfter: f.retryAfter,
		Cause:      cause,
	}
}

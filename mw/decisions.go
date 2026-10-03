package mw

import (
	"context"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// DecisionOptions configures Decisions.
type DecisionOptions struct {
	// Floor is the probability below which the decision provider's answer is
	// not used and the generating provider is asked the same request. It is
	// compared with the lowest probability the decision provider reported for
	// its answers to the request, and an answer with no probability reported
	// cannot clear a floor.
	//
	// Zero, the default, is no fallback on confidence: the decision provider's
	// answer is the answer whatever it says of itself. There is no default
	// floor on purpose. A probability is the model's own claim, and what a
	// given floor buys depends on the model, the questions and the data: the
	// one figure this library has, 0.7 sending 8% of 48 decisions on and
	// correcting the single wrong one, rests on that one error. Measure a floor
	// on a few hundred of your own labelled decisions before trusting it.
	//
	// A value that is not a number from 0 to 1 refuses every decision, as a
	// configuration error, before anything is sent.
	Floor float64

	// Model is the model the decision provider runs a decision on, when that is
	// not the model the request names. Left empty, the request's model is
	// passed on, and a decision provider that is handed another provider's
	// model sends its own (llm.ModelSender): the built-in ones do. The client's
	// data policy is checked for the decision provider and the model it sends.
	Model string

	// NoFailover makes a failure of the decision provider in transit
	// (unavailable, rate limited, timed out) the request's error. By default
	// such a request is asked of the generating provider: a pair is chosen for
	// availability as well as for speed. A decision the decision provider
	// refuses as one it cannot take is asked of the generating provider either
	// way.
	NoFailover bool
}

// Decisions pairs a generating provider with a decision provider. The pair is
// one provider: a request that carries a decision the decision provider can
// take goes to it, and every other request goes to the generating provider.
//
//	generator, _ := schemaflux.CreateProvider("openai", schemaflux.ProviderConfig{APIKey: openAIKey})
//	decider, _ := schemaflux.CreateProvider("jev", schemaflux.ProviderConfig{APIKey: typesafeKey})
//	client := schemaflux.NewClient(openAIKey).WithProviderInstance(
//	    mw.Decisions(generator, decider, mw.DecisionOptions{}))
//
// The operations whose answer is bounded describe it on the request
// (CompletionRequest.Decision): Is, Which, WhichOrNone, Rate, Assess and their
// Result forms, Whether, RouteBy, Classify and Choose. A decision model answers
// those typed, in a fraction of the time and at a fraction of the price of a
// model that writes its answer, and cannot write text at all, so Extract,
// Summarize and the rest go to the generating provider as they did.
//
// What goes to the generating provider, with the request unchanged:
//
//   - a request with no decision;
//   - a decision past what the decision provider declares it takes
//     (llm.DecisionLimits: more options, more levels, a longer state), without
//     the decision provider being asked;
//   - a decision the client's data policy does not allow the decision
//     provider's route to be sent, without it being asked. The policy is held
//     to that route as it is to the generating provider's: the provider and the
//     model it sends, where they run and what they keep. The generating
//     provider's own route is checked by the call path before the pair is
//     reached, so a client whose policy allows neither sends nothing;
//   - a decision the decision provider refused as unsupported;
//   - a decision the decision provider failed on in transit, unless NoFailover;
//   - a decision it answered with a probability below Floor.
//
// In the last three the decision provider was asked first, and the response
// says so: CompletionResponse.DecisionEscalation names it, why its answer was
// not used and the probability it reported, and Provider and Model name the
// generating provider, which answered. The decision record and the call's
// record carry the same.
//
// Both calls are paid for. The decision provider's is charged when it is made,
// at its own model's rate, to the client's budget and the cost ledger, and is in
// the usage and cost of the call's envelope beside the generating provider's,
// as one more attempt. The budget is checked before the request and not again
// between the two calls.
//
// An answer that is not an answer to the decision (fewer or more answers than
// questions, one of the wrong kind, a label that was not offered, a level off the
// scale, a probability outside 0 to 1) is checked for before the floor is, by the
// check the call path makes of every typed answer. It is the decision provider
// failing: the escalation names it with the reason "unavailable", the generating
// provider answers, and the decision provider's call is charged, as a call below
// the floor is. With NoFailover the request fails with the kind of an answer that
// is not valid. A pair never passes such an answer on.
//
// When the generating provider then fails, the request fails with its error
// and nothing else: the answer that was below the floor is never returned in
// its place. A failure of the decision provider that is neither a refusal for
// capability nor a failure in transit (a rejected key, a request the API
// rejects, an answer that is not the protocol's) is the request's error, and
// the generating provider is not asked: it would hide a pair that is
// misconfigured behind one that works at ten times the latency.
//
// To the call path the pair is its generating provider: its name, its retry
// policy, its cost estimate and the model it sends. A pair with no decision
// provider is the generating provider, and one with no generating provider is
// the decision provider alone, which refuses what it cannot answer.
//
// Where it stands in a chain: a middleware listed before it sees the request
// and not which provider served it. RedactEgress listed before it redacts the
// decision's state, guidance, questions, options and criteria by the rules it
// redacts the prompts by, so neither half is sent what it would not be sent as
// a prompt, and a label the decision provider chose is given back as the
// caller's own option. Budget listed before it counts the generating provider's
// response, and a call whose decision provider was asked first and answered below
// the floor is paid twice: the first call is not in the response's usage, and
// a ledger that is to count it goes around the decision provider inside the pair.
func Decisions(generator, decider Handler, options DecisionOptions) Handler {
	switch {
	case decider == nil:
		return generator
	case generator == nil:
		return decider
	}
	options.Model = strings.TrimSpace(options.Model)
	return &decisionsProvider{generator: generator, decider: decider, options: options}
}

type decisionsProvider struct {
	generator, decider Handler
	options            DecisionOptions
}

func (p *decisionsProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if req.Decision == nil || len(req.Decision.Questions) == 0 {
		return p.generator.Complete(ctx, req)
	}
	floor := p.options.Floor
	if !(floor >= 0 && floor <= 1) {
		return llm.CompletionResponse{}, types.Newf(types.KindConfiguration, "mw.Decisions",
			"Floor has to be a number from 0 to 1, where 0 is no fallback on confidence")
	}

	// The request as the decision provider gets it, and the route it will go
	// by: the provider, and the model that provider sends, which is not always
	// the one the request names (llm.ModelSentBy).
	routed := req
	if p.options.Model != "" {
		routed.Model = p.options.Model
	}
	name := p.decider.Name()
	sent := llm.ModelSentBy(p.decider, routed.Model)

	// A decision past what the decision provider declares, and a route the
	// client's data policy does not allow, are not sent to it. It was never an
	// option for this request, so nothing records it as asked first.
	if limits, declared := llm.DecisionLimitsOf(p.decider); declared {
		if limits.Check(name, sent, req.Decision) != nil {
			return p.generator.Complete(ctx, req)
		}
	}
	if ops.CheckRoutePolicy(ctx, name, sent) != nil {
		return p.generator.Complete(ctx, req)
	}

	started := time.Now()
	resp, err := p.decider.Complete(ctx, routed)
	if err != nil {
		reason := ""
		switch kind := llm.Classify(err); {
		case kind == types.KindUnsupportedCapability:
			reason = llm.DecisionEscalatedUnsupported
		case !p.options.NoFailover && (kind == types.KindProviderUnavailable || kind == types.KindRateLimited || kind == types.KindTimeout || kind == types.KindCircuitOpen):
			reason = llm.DecisionEscalatedUnavailable
		default:
			return llm.CompletionResponse{}, err
		}
		return p.askGenerator(ctx, req, &llm.DecisionEscalation{Provider: name, Model: sent, Reason: reason}, nil)
	}

	// The answer says which route it came by. A decision provider that does not
	// say is spoken for here, where it is known: the call path prices an answer
	// and checks it against the data policy by the route it names, and an answer
	// with no name on it would be taken for the generating provider's.
	if resp.Provider == "" {
		resp.Provider = name
	}
	if resp.Model == "" {
		resp.Model = sent
	}

	// The answers are checked against the questions before anything is read from
	// them: a probability that is not one, or a label nobody offered, must not be
	// able to clear a floor, and a pair that passed such an answer on would make
	// its caller the one to find it out. An answer that does not answer the
	// decision is the decision provider failing, as far as the pair can tell, and
	// is handled as that: the generating provider is asked, or with NoFailover the
	// request fails with the kind of an answer that is not valid.
	if _, invalid := llm.RenderDecisionAnswers(req.Decision, resp.DecisionAnswers); invalid != nil {
		if p.options.NoFailover {
			return llm.CompletionResponse{}, &types.OperationError{
				Kind:    types.KindMalformedOutput,
				Op:      "mw.Decisions",
				Message: "the decision provider's answers do not answer the decision",
				Cause:   invalid,
			}
		}
		// The decision provider's call was made and is billed, and its response
		// goes no further than here.
		ops.ChargeAskedFirst(ctx, p.decider, sent, resp, started)
		return p.askGenerator(ctx, req, &llm.DecisionEscalation{
			Provider: resp.Provider,
			Model:    resp.Model,
			Reason:   llm.DecisionEscalatedUnavailable,
		}, &resp)
	}

	confidence, reported := llm.LowestConfidence(resp.DecisionAnswers)
	if floor == 0 || (reported && confidence >= floor) {
		return resp, nil
	}

	// Below the floor. The decision provider's call was made and is billed, and
	// its response goes no further than here, so it is charged here.
	ops.ChargeAskedFirst(ctx, p.decider, sent, resp, started)
	return p.askGenerator(ctx, req, &llm.DecisionEscalation{
		Provider:           resp.Provider,
		Model:              resp.Model,
		Reason:             llm.DecisionEscalatedBelowFloor,
		ModelConfidence:    confidence,
		ConfidenceReported: reported,
	}, &resp)
}

// askGenerator asks the generating provider the request the decision provider
// was asked first, and records that on its answer. Its failure is the request's
// failure: nothing of the decision provider's answer comes back with it.
//
// asked is the decision provider's answer when it gave one that was passed over.
// The observers of the request are told who was asked first, why and what it
// used, before the generating provider is asked, so that a request that then
// fails or is given up on still reports what was paid for.
func (p *decisionsProvider) askGenerator(ctx context.Context, req llm.CompletionRequest, escalation *llm.DecisionEscalation, asked *llm.CompletionResponse) (llm.CompletionResponse, error) {
	ops.NoteDecisionEscalation(ctx, escalation, asked)
	// The caller may have given up while the decision provider was answering.
	if err := ctx.Err(); err != nil {
		return llm.CompletionResponse{}, err
	}
	resp, err := p.generator.Complete(ctx, req)
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	resp.DecisionEscalation = escalation
	return resp, nil
}

// Name, EstimateCost, RetryPolicy and ModelSentFor describe the generating
// provider: the route every request can take, and the one the call path checks
// before the pair is reached. The decision provider's route is checked here,
// when a decision is about to be sent to it.
func (p *decisionsProvider) Name() string { return p.generator.Name() }

func (p *decisionsProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.generator.EstimateCost(req)
}

func (p *decisionsProvider) RetryPolicy() (int, time.Duration) { return p.generator.RetryPolicy() }

// ModelSentFor is the generating provider's answer.
func (p *decisionsProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.generator, requested)
}

// Unwrap is the generating provider, the one that is warmed. The decision
// provider's connection is opened by its first decision.
func (p *decisionsProvider) Unwrap() Handler { return p.generator }

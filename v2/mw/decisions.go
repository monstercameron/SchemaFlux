package mw

import (
	schemaflux "github.com/monstercameron/schemaflux/v2"

	impl "github.com/monstercameron/schemaflux/mw"
)

// DecisionOptions are the settings of Decisions. The zero value is the default:
// the decision provider's answer is the answer whatever it says of itself, it
// runs under the model the request names, and its failures in transit go to the
// generating provider.
type DecisionOptions struct {
	// Floor is the probability below which the decision provider's answer is not
	// used and the generating provider is asked the same request. It is compared
	// with the lowest probability the decision provider reported for its answers
	// to the request, and an answer with no probability reported cannot clear a
	// floor.
	//
	// Zero is no fallback on confidence: the decision provider's answer is the
	// answer, as long as it is an answer to the decision. There is no default
	// floor, on purpose. A probability is the model's
	// own claim, and what a floor buys depends on the model, the questions and the
	// data. A request of several questions is passed over whole when any one of its
	// answers is under the floor, so a floor that looks mild for one question is
	// not for five. Measure it on your own labelled decisions before setting one.
	//
	// A value that is not a number from 0 to 1 is a configuration error: every
	// request through the pair fails with an error of kind
	// schemaflux.ErrConfiguration, before anything is sent.
	Floor float64

	// Model is the model the decision provider runs a decision on, when that is
	// not the model the request names. Empty passes the request's model on, and a
	// decision provider that is handed another provider's model runs its own: the
	// built-in ones do. The client's data policy is checked for the decision
	// provider and the model it runs.
	Model string

	// NoFailover makes a failure of the decision provider in transit
	// (unavailable, rate limited, timed out, its circuit open) the request's
	// error. By default such a request is asked of the generating provider: a pair
	// is chosen for availability as well as for speed. A decision the decision
	// provider refuses as one it cannot take is asked of the generating provider
	// either way.
	NoFailover bool
}

// Decisions pairs a generating provider with a decision provider, and the pair
// is one provider: a request that carries a decision the decision provider can
// take goes to it, and every other request goes to the generating provider.
//
//	generator, _ := provider.New("openai", provider.Config{})
//	decider, _ := provider.New("jev", provider.Config{})
//	client := schemaflux.Client{Provider: mw.Decisions(generator, decider, mw.DecisionOptions{})}
//
// The operations whose answer is bounded describe it on the request (Request.
// Decision): Is, Which, WhichOrNone, Rate, Assess and their Detail forms,
// Whether, RouteBy, Classify and Choose. A decision model answers those typed, in
// a fraction of the time and at a fraction of the price of a model that writes
// its answer, and cannot write text at all, so Extract, Summarize and the rest go
// to the generating provider as they did.
//
// What goes to the generating provider, with the request unchanged:
//
//   - a request with no decision;
//   - a decision past what the decision provider declares it takes (its
//     provider.Decider's DecisionLimits: more questions, more options, more
//     levels, a longer state), without the decision provider being asked;
//   - a decision the client's data policy does not allow the decision provider's
//     route to be sent, without it being asked. The policy holds for that route as
//     it does for the generating provider's: the provider and the model it runs,
//     where they run and what they keep. The generating provider's route is
//     checked by the call before the pair is reached, so a client whose policy
//     allows neither sends nothing;
//   - a decision the decision provider refused as one it cannot take (an error of
//     kind schemaflux.ErrUnsupported);
//   - a decision the decision provider failed on in transit, unless NoFailover;
//   - a decision it answered with a probability below Floor;
//   - a decision it answered with answers that are not answers to it: too few or
//     too many, one of the wrong kind, a label that was not offered, a level off
//     the scale, a probability outside 0 to 1. The pair checks them against the
//     questions before it reads a probability, with the check the call path makes
//     of every typed answer, and never passes them on. The escalation says
//     "unavailable"; with NoFailover the request fails with an error of kind
//     schemaflux.ErrInvalidAnswer.
//
// In the last four the decision provider was asked first, and the response
// says so: Response.Escalation names it, why its answer was not used
// ("unsupported", "unavailable" or "below the floor") and the probability it
// reported, and Provider and Model name the generating provider, which answered.
// The call's Record carries the same.
//
// Both calls are paid for. The decision provider's is charged when it is made, at
// its own model's rate, to the client's budget and the price ledger, and the
// call's Record counts it as one more attempt in its usage and cost. The
// Response's own Usage is the generating provider's alone. The budget is checked
// before the request and not again between the two calls.
//
// When the generating provider then fails, the request fails with its error and
// nothing else: the answer that was below the floor is never returned in its
// place. A failure of the decision provider that is neither a refusal for
// capability nor a failure in transit (a rejected key, a request its API rejects,
// an answer that is not the protocol's) is the request's error, and the
// generating provider is not asked: that would hide a pair that is misconfigured
// behind one that works at ten times the latency. The data policy holds for both
// routes, and the decision provider is never sent what the policy keeps from it.
//
// To the library the pair is its generating provider: its name, its retry
// settings, its model table and its Unwrap. A pair with no decision provider is
// the generating provider, and one with no generating provider is the decision
// provider alone, which refuses what it cannot answer. Decisions does not forward
// streaming: a pair cannot stream.
//
// # Choosing a floor
//
// A request of several questions is passed over whole when any one of its
// answers is under the floor, so a floor that looks mild for one question is not
// for five, and each request that is passed over is paid for twice. A floor is a
// trade between the price and the agreement with the labels, and the data decides
// where it lies: measure a floor on your own labelled decisions, with the
// questions you ask, before you set one.
//
// # Where it stands in a chain
//
// A middleware outside the pair sees the request and not which provider answered.
// Redact outside it redacts the decision's state, guidance, questions, options
// and criteria by the rules it redacts the prompts by, so neither half is sent
// what it would not be sent as a prompt, and a label the decision provider chose
// is given back as the caller's own option. A Budget outside it counts the
// generating provider's answer, and a request whose decision provider was asked
// first and answered below the floor is paid twice with the first call not in the
// answer's usage: a ledger that is to count it goes around the decision provider
// inside the pair, Decisions(generator, mw.Budget(limit)(decider), options).
func Decisions(generator, decider schemaflux.Provider, options DecisionOptions) schemaflux.Provider {
	switch {
	case decider == nil:
		return generator
	case generator == nil:
		return decider
	}
	// A floor that is not a probability fails every request through the pair, as
	// an unusable setting of any other layer does, and not only a request that
	// carries a decision.
	if !(options.Floor >= 0 && options.Floor <= 1) {
		return wrap("mw.decisions", generator, "Floor must be a number from 0 to 1, where 0 is no fallback on confidence", nil)
	}
	handler := impl.Decisions(internalOf(generator), internalOf(decider), impl.DecisionOptions{
		Floor:      options.Floor,
		Model:      options.Model,
		NoFailover: options.NoFailover,
	})
	return newWrapper("decisions", generator, handler)
}

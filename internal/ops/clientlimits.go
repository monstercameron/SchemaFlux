package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// LX-126. Two things a client can be given, and that its calls were never held
// to: a scheduler and a data policy. Both travelled on the context
// (execconfig.go) and neither was read by anything, so a client bounded to two
// calls in flight had as many as it had callers, and a client restricted to
// one provider in one region sent its calls wherever the process was
// configured to send them. Neither said so.
//
// This file is where a call meets them. Both are checked for every request a
// provider is sent, buffered or streamed, at the one place each path builds
// its request.

// throughScheduler runs one provider call inside the client's scheduler, when
// the context carries one, and directly when it does not.
//
// ran reports whether the work was started. When it is false the error is the
// scheduler's: the queue was full, the scheduler was closed, or the caller's
// context ended while the call waited for a slot. That is not a failure of the
// provider, and the caller of this does not retry it.
//
// The slot is held for as long as run takes and no longer. The buffered path
// calls this once per attempt, so a call sitting out the wait before a retry
// holds nothing, and a stream calls it once for the life of the stream.
//
// Two things are looked at again once the call has its slot, because the wait
// for it can be long. The caller's context: the scheduler can admit a call in
// the same instant its context ends, and which of the two it sees first is a
// coin toss. And the budget: it was asked before the wait, so a hundred calls
// queued behind one slot all passed a budget the first of them was about to
// spend. A call that fails either was not run.
func throughScheduler[T any](ctx context.Context, provider llm.Provider, req llm.CompletionRequest, run func(context.Context) (T, error)) (result T, ran bool, err error) {
	scheduler := ExecScheduler(ctx)
	if scheduler == nil {
		result, err = run(ctx)
		return result, true, err
	}

	prompt := requestTokens(req.SystemPrompt, req.UserPrompt)
	submit := SubmitRequest{
		Provider: provider.Name(),
		// Stated, because the zero value is the lowest class.
		Priority: SchedPriorityNormal,
		// What the request is expected to hold while it runs, for a scheduler
		// that bounds tokens or cost in flight. The prompt is counted the way
		// the size guard counts it and the answer at its ceiling; both are
		// estimates, which is what the scheduler asks for.
		EstimatedTokens: int64(types.SumInt(prompt, req.MaxTokens)),
		EstimatedCost:   estimatedCost(provider.Name(), req.Model, prompt, req.MaxTokens),
	}
	if deadline, ok := ctx.Deadline(); ok {
		submit.Deadline = deadline
	}
	runCancelled := false
	result, err = Submit(ctx, scheduler, submit, func(runCtx context.Context) (T, error) {
		var none T
		if over := contextOver(ctx); over != nil {
			return none, over
		}
		if spent := checkBudget(ctx); spent != nil {
			return none, spent
		}
		ran = true
		result, err := run(runCtx)
		// Whether the context the work ran under was cancelled is only
		// knowable here: it is gone once Submit returns.
		runCancelled = runCtx.Err() != nil
		return result, err
	})

	// Close cancels what is in flight. To the work that looks like its own
	// context ending, and it came back as a cancellation, which reads as the
	// caller having given up. The caller did not.
	if ran && ctx.Err() == nil && stoppedByShutdown(scheduler, runCancelled, err) {
		err = &types.OperationError{
			Kind:    types.KindShutdown,
			Op:      "scheduler.Submit",
			Message: "the scheduler was closed while the request was in flight",
			Cause:   err,
		}
	}
	return result, ran, err
}

// estimatedCost is what a request is expected to cost, in dollars, from this
// library's own price table: the prompt as counted and the answer at its
// ceiling. A model the table has no price for is estimated at nothing, so a
// ceiling on cost in flight does not hold such a model back; that is a stated
// limit, and the alternative is a made-up price.
//
// The provider's own EstimateCost was used for this. It prices every OpenAI
// model at one old model's rates and reports nothing for several providers, so
// a ceiling of ten cents refused every call on the default model and never
// bound anywhere else.
func estimatedCost(provider, model string, promptTokens, outputTokens int) float64 {
	cost := pricing.EstimateCost(&types.TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      types.SumInt(promptTokens, outputTokens),
	}, model, provider)
	if cost == nil || !cost.Priced || math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || cost.TotalCost < 0 {
		return 0
	}
	return cost.TotalCost
}

// checkDataPolicy refuses a call whose route the client's data policy does not
// allow. With no policy declared it allows everything, as before.
//
// The rules are types.DataPolicy's own and do not fail open:
//
//   - The provider has to be named in AllowedProviders. A policy that names no
//     provider allows none.
//   - The route has to have said where it runs and how long it retains, with
//     llm.DeclareRoute. A route that has declared nothing is not
//     eligible under any policy: nothing is known about it, and a policy is a
//     statement about what is known.
//   - Every region the route may run in has to be in AllowedRegions. A route
//     that may run in two regions, one of them not allowed, is refused: the
//     request could be served from either.
//   - The route's retention has to be within MaxRetentionDays, and with none
//     stated only a route that retains nothing is allowed.
//
// What this sees is the provider this library hands the request to. A provider
// that is itself a router sends the request on to routes this check does not
// see, so two more things are done. mw.Fallback puts each alternate through
// CheckRoutePolicy before it uses it. And checkAnswerRoute refuses an answer
// that names a provider the policy does not allow, which is the one place a
// router this library does not know about shows where the request went.
//
// The message names the provider, the model and the rule. Those are
// configuration. Nothing of the caller's content is in it.
//
// The model checked is the one the provider will send (llm.ModelSentBy), which
// is not always the one the request names: the Anthropic provider sends its own
// default for an empty model and for a "gpt" one. The check was on the name in
// the request, so content went over a route that was never checked and the
// answer was then refused for naming it.
func checkDataPolicy(ctx context.Context, provider llm.Provider, model string) error {
	return CheckRoutePolicy(ctx, provider.Name(), llm.ModelSentBy(provider, model))
}

// CheckRoutePolicy is checkDataPolicy for a route named by its provider and
// model. It is exported for a provider that routes a request onward, so that it
// can hold each route behind it to the client's policy before using it:
// mw.Fallback does, for every alternate.
func CheckRoutePolicy(ctx context.Context, name, model string) error {
	policy, declared := ExecDataPolicy(ctx)
	if !declared {
		return nil
	}
	refuse := func(format string, args ...any) error {
		return &types.OperationError{
			Kind:     types.KindPolicyViolation,
			Op:       "data policy",
			Provider: name,
			Model:    model,
			Message:  fmt.Sprintf(format, args...),
			Cause:    ErrDataPolicyRefusal,
		}
	}

	if !policy.AllowsProvider(name) {
		return refuse("the client's data policy does not allow provider %q", name)
	}
	// Only where the route runs and what it keeps are read, so only those two
	// are fetched: CapabilitiesFor copied the route's capability map and both of
	// its lists for them, up to three times per call.
	regions, retentionDays, known := llm.RoutePlacementFor(name, model)
	if !known {
		return refuse("the route %s/%s has not declared where it runs or what it retains, so the client's data policy cannot allow it; declare it with DeclareRoute", name, model)
	}
	// A retention below zero is not a length of time. types.RetentionUnspecified
	// is such a value, and "no more than minus one day" is true of every
	// ceiling, so a route declared with it passed any policy, zero retention
	// included.
	if retentionDays < 0 {
		return refuse("the route %s/%s declares a retention that is not a number of days, so the client's data policy cannot allow it", name, model)
	}
	if len(regions) == 0 {
		return refuse("the route %s/%s declares no region, and the client's data policy allows only named regions", name, model)
	}
	for _, region := range regions {
		if !policy.AllowsRegion(region) {
			return refuse("the route %s/%s may run in region %q, which the client's data policy does not allow", name, model, region)
		}
	}
	if !policy.AllowsRetention(types.RetentionDays(retentionDays)) {
		return refuse("the route %s/%s keeps content for %d days, which is more retention than the client's data policy allows", name, model, retentionDays)
	}
	return nil
}

// checkAnswerRoute refuses an answer that says it came by a route the client's
// data policy does not allow.
//
// The request has gone by the time this runs: the check cannot take it back.
// What it does is stop the answer being used as if nothing had happened, and
// say where the request went, which is what the owner of the policy needs to
// know.
//
// An answer by the route that was asked is accepted: the provider that was
// handed the request, naming the model that was asked for or one version of
// it, a dated snapshot say (llm.SameModelRoute says which names are the same).
// That route was checked before the request was sent. An answer that names no
// provider is taken to be from the one that was asked, and one that names no
// model to be the model that was asked for. The model asked for is the one the
// provider sends (llm.ModelSentBy), which for the Anthropic provider is not
// always the one the request names.
//
// Any other answer was sent somewhere else by a router or a gateway, and is
// held to the policy as the route it names, provider and model. The model may
// be declared under its own name or under the name it is a version of.
//
// A gateway can also say which provider it passed the request to:
// CompletionResponse.UpstreamProvider, which OpenRouter's answers carry. That
// provider is a further hop, and is held to the policy as well: allowed by
// name, and its route, under the model the answer names, declared and allowed.
// The route may be declared under the gateway's spelling of the model or the
// vendor's own ("openai/gpt-4o" or "gpt-4o"), either with or without a version
// tag. An answer that names no upstream is not held to anything further: that
// is "not said", and nothing here can check what is not said.
//
// The names in the refusal come from the server's answer. They are cut to a
// short length and stripped of control characters before they reach an error,
// so a hostile or broken endpoint cannot put a page of text or an escape
// sequence in the message or the fields.
//
// Two things were wrong here. An answer under the provider that was asked was
// accepted whatever model it named, so a gateway that keeps one name and sends
// requests on to a model the policy forbids was never noticed. And an answer
// from another provider was checked under exactly the model it named, usually
// a dated snapshot nobody declares, or, when it named none, under the model
// that had been asked of the first provider: a fallback that had been checked,
// allowed and paid was then refused.
func checkAnswerRoute(ctx context.Context, asked llm.Provider, model string, resp llm.CompletionResponse) error {
	policy, declared := ExecDataPolicy(ctx)
	if !declared {
		return nil
	}
	model = llm.ModelSentBy(asked, model)
	askedName := strings.TrimSpace(asked.Name())
	answered := strings.TrimSpace(resp.Provider)
	answeredModel := strings.TrimSpace(resp.Model)
	upstream := strings.TrimSpace(resp.UpstreamProvider)

	// What an error may say of the server's answer: bounded and printable.
	refuse := func(provider, answeredModel, why string) error {
		return &types.OperationError{
			Kind:     types.KindPolicyViolation,
			Op:       "data policy",
			Provider: r12bShownName(provider),
			Model:    r12bShownName(answeredModel),
			Message:  fmt.Sprintf("the answer came from %s; the request was sent there by the provider it was handed to", why),
			Cause:    ErrDataPolicyRefusal,
		}
	}

	// The provider that answered, and the model it names, held to the policy.
	// An answer by the route that was asked passes without a lookup.
	sameProvider := answered == "" || strings.EqualFold(answered, askedName)
	if sameProvider {
		answered = askedName
	}
	byRouteAsked := sameProvider && (answeredModel == "" || llm.SameModelRoute(model, answeredModel))
	if !byRouteAsked {
		if !policy.AllowsProvider(answered) {
			return refuse(answered, answeredModel, fmt.Sprintf("provider %q, which the client's data policy does not allow", r12bShownName(answered)))
		}
		if answeredModel == "" {
			// Another provider's route cannot be looked up under the model that
			// was asked of the first one.
			return refuse(answered, answeredModel, fmt.Sprintf("provider %q and names no model, so its route cannot be checked against the client's data policy", r12bShownName(answered)))
		}
		if !r12bRouteAllowed(ctx, answered, answeredModel) {
			return refuse(answered, answeredModel, fmt.Sprintf("the route %s/%s, which the client's data policy does not allow; declare it with DeclareRoute if it should be", r12bShownName(answered), r12bShownName(answeredModel)))
		}
	}

	// A gateway that says which provider it passed the request to: a further
	// hop, held to the policy as the route it is.
	if upstream == "" || strings.EqualFold(upstream, answered) {
		return nil
	}
	if !policy.AllowsProvider(upstream) {
		return refuse(upstream, answeredModel, fmt.Sprintf("provider %q behind %q, which the client's data policy does not allow", r12bShownName(upstream), r12bShownName(answered)))
	}
	hopModel := answeredModel
	if hopModel == "" {
		hopModel = model
	}
	if !r12bRouteAllowed(ctx, upstream, hopModel) {
		return refuse(upstream, hopModel, fmt.Sprintf("the route %s/%s behind %q, which the client's data policy does not allow; declare it with DeclareRoute if it should be", r12bShownName(upstream), r12bShownName(hopModel), r12bShownName(answered)))
	}
	return nil
}

// r12bRouteAllowed reports whether the client's data policy allows a route a
// provider answered by. The route may be declared under any of several names:
// the model as named, the model without its version tag, and, for a gateway's
// "vendor/model", the vendor's own name for it, with and without the tag.
//
// The names are tried from the most specific, and the first one that has been
// declared decides. Allowing the route when any name passes let a gateway's
// "openai/gpt-4o", declared with a retention the policy forbids, through
// because the shorter "gpt-4o" was declared with none: the specific declaration
// is the one that describes this route. A route no name has been declared under
// is not allowed.
func r12bRouteAllowed(ctx context.Context, provider, model string) bool {
	names := []string{model}
	if base := llm.WithoutVersionTag(model); base != model {
		names = append(names, base)
	}
	if slash := strings.LastIndex(model, "/"); slash >= 0 && slash < len(model)-1 {
		vendorName := model[slash+1:]
		names = append(names, vendorName)
		if base := llm.WithoutVersionTag(vendorName); base != vendorName {
			names = append(names, base)
		}
	}
	for _, name := range names {
		if _, _, declared := llm.RoutePlacementFor(provider, name); declared {
			return CheckRoutePolicy(ctx, provider, name) == nil
		}
	}
	return false
}

// r12bShownNameLimit is the most characters of a name the server's answer
// supplied that an error carries.
const r12bShownNameLimit = 64

// r12bShownName returns a name from a server's answer as an error may carry it:
// valid UTF-8, with every character that is not printable removed (control
// characters, line breaks, and the invisible formatting characters that reorder
// or hide text), and cut to r12bShownNameLimit characters, with "..." marking a
// cut.
func r12bShownName(name string) string {
	name = strings.ToValidUTF8(name, "")
	var kept strings.Builder
	count := 0
	for _, r := range name {
		if !unicode.IsPrint(r) {
			continue
		}
		if count == r12bShownNameLimit {
			kept.WriteString("...")
			break
		}
		kept.WriteRune(r)
		count++
	}
	return kept.String()
}

// stoppedByShutdown reports whether an error is the scheduler's closing and
// nothing else: the context the work ran under was cancelled, the scheduler is
// closed, and the error is a cancellation.
//
// All three are asked. Close cancels what is in flight, and to the work that
// looks like its own context ending, so the error is a cancellation. A
// provider's own failure, an authentication error that came back a moment
// before Close, is not a shutdown because the scheduler happens to be closed
// by the time anyone looks: it was relabelled one, which told the caller to
// try again elsewhere for a request that will fail everywhere.
func stoppedByShutdown(scheduler *Scheduler, runCancelled bool, err error) bool {
	if err == nil || !runCancelled || scheduler == nil || !scheduler.Closed() {
		return false
	}
	return errors.Is(err, context.Canceled) || types.KindOf(err) == types.KindCanceled
}

// reportsUsage reports whether a response says what it used. It is the one
// test of that, shared by the two ledgers and both paths: a stream asked
// whether the total was above zero where the buffered path asked about all
// three counts, so a response that reported prompt tokens and no total was
// charged on one path and not the other.
func reportsUsage(usage types.TokenUsage) bool {
	return usage.TotalTokens != 0 || usage.PromptTokens != 0 || usage.CompletionTokens != 0
}

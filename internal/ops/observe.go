package ops

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
	"github.com/monstercameron/schemaflux/telemetry"
)

// What this package tells the installed observers, and where.
//
// A request to a provider is bracketed by observeStart and observeEnd, in
// CallLLM for a buffered answer and in streamLLM for a streamed one. One call
// of an operation is bracketed by observeOperation and the end method of what
// it returns. A request made under an operation starts from the context
// observeOperation returned, which is what makes it the operation's child to
// an observer that opens spans.
//
// The observers are the host's code. The telemetry package contains a panic in
// one, and ignores a context it returns that does not descend from the one it
// was given, so nothing here has to: an observer cannot fail the call it is
// watching or change what the call does, and with none installed each of these
// functions does an atomic load and returns.

// observeStart tells the installed observers a request is starting and returns
// the context to make the request on. With an observer installed that context
// also carries what the request will have to say at its finish that its answer
// cannot (requestAccount), and the request is counted in every operation call
// it runs under.
func observeStart(ctx context.Context, event telemetry.OperationEvent) context.Context {
	if !telemetry.HasObserver() {
		return ctx
	}
	countRequest(ctx)
	return context.WithValue(telemetry.ObserveOperationStart(ctx, event), requestAccountKey{}, &requestAccount{})
}

// observeEnd tells the observers that saw the request start that it finished,
// with what a middleware reported of it on the way (requestAccount) added to
// what the answer says.
func observeEnd(ctx context.Context, result telemetry.OperationResult) {
	if account := requestAccountFrom(ctx); account != nil {
		account.apply(&result)
	}
	telemetry.ObserveOperationEnd(ctx, result)
}

// requestAccountKey is the context key of the requestAccount of the request
// the observers were told began.
type requestAccountKey struct{}

// requestAccount is what happened to one request that the answer it ended with
// does not show: a response cache answered it, or a decision provider was asked
// before the one that answered. The provider that answered returns one response,
// and the middleware that made the first call or served the cache is the only
// code that knows, so it says so here (NoteCacheHit, NoteDecisionEscalation)
// and the request's finish reads it back. Without it a request that paid for two
// calls reported the second, and a hit reported a provider call that was never
// made.
type requestAccount struct {
	mu sync.Mutex

	// cacheHits is how many of the request's provider calls a cache answered.
	cacheHits int

	// escalation is the decision provider that was asked first, when one was.
	// paid is what its call used, priced as the request prices an answer it
	// passed over, and paidCalls is how many provider calls that was.
	escalation *llm.DecisionEscalation
	paid       refusedAnswers
	paidCalls  int
}

func requestAccountFrom(ctx context.Context) *requestAccount {
	if ctx == nil {
		return nil
	}
	account, _ := ctx.Value(requestAccountKey{}).(*requestAccount)
	return account
}

// NoteCacheHit says that a response cache answered the request on ctx, so that
// no provider was called for it. mw.Cache calls it; with no observer installed
// it does nothing.
func NoteCacheHit(ctx context.Context) {
	if account := requestAccountFrom(ctx); account != nil {
		account.mu.Lock()
		account.cacheHits++
		account.mu.Unlock()
	}
}

// NoteDecisionEscalation says that the decision provider named by escalation
// was asked the request on ctx before the provider that went on to answer it
// (mw.Decisions). asked is the decision provider's response when it answered,
// and nil when it failed or refused the decision: an answer it gave was made and
// paid for, and counts as one more provider call with its usage and cost, which
// is how the call's record counts it. With no observer installed it does
// nothing.
func NoteDecisionEscalation(ctx context.Context, escalation *llm.DecisionEscalation, asked *llm.CompletionResponse) {
	account := requestAccountFrom(ctx)
	if account == nil || escalation == nil {
		return
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	copied := *escalation
	account.escalation = &copied
	if asked != nil && !asked.Cached {
		account.paidCalls++
		account.paid.add(escalation.Provider, escalation.Model, *asked)
	}
}

// apply writes the account into the finish of the request it belongs to.
func (a *requestAccount) apply(result *telemetry.OperationResult) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.escalation != nil {
		result.AskedFirst = a.escalation.Provider
		result.AskedFirstModel = a.escalation.Model
		result.Escalated = a.escalation.Reason
	}
	if a.paidCalls > 0 {
		result.Attempts += a.paidCalls
	}
	if a.paid.any() {
		// A finish that worked out no cost has the zero value, which is not a cost
		// of nothing and is not added as one.
		var cost *types.CostInfo
		if result.Cost != (types.CostInfo{}) {
			accepted := result.Cost
			cost = &accepted
		}
		result.Usage, result.Cost = a.paid.plus(result.Usage, cost)
	}
	if a.cacheHits > 0 {
		// Nothing was bought and nothing answered: a hit is not a provider call.
		result.Cached = true
		result.Attempts = max(0, result.Attempts-a.cacheHits)
		result.Usage, result.Cost = types.TokenUsage{}, types.CostInfo{}
		result.Provider, result.Model = "", ""
	}
}

// countRequest counts a request in every call of an operation it runs under:
// the innermost, and the calls that one was opened inside.
func countRequest(ctx context.Context) {
	if ctx == nil {
		return
	}
	for open, _ := ctx.Value(observedOperationKey{}).(*observedOperation); open != nil; open = open.parent {
		open.requests.Add(1)
	}
}

// observedOperation is one call of an operation the observers were told began.
type observedOperation struct {
	ctx   context.Context
	name  string
	start time.Time
	ended atomic.Bool

	// parent is the call of another operation this one was opened inside, which
	// the requests it makes are counted in as well.
	parent *observedOperation

	// requests is how many requests the call made.
	requests atomic.Int64

	// What became of the items of a run over many, when the call is one: set by
	// recordItems before the call returns, from the goroutine that ends it.
	items, succeeded, failed, cancelled int
	firstFailure                        error
}

// observedOperationKey is the context key of the innermost operation call the
// observers were told about.
type observedOperationKey struct{}

// observeOperation tells the installed observers that one call of the
// operation name is starting, and returns the context to run the call on and
// the value whose end method reports how the whole call ended. It is the one
// place the operation scope is emitted from, so an entry that every operation
// is routed through calls this and nothing else.
//
// The caller defers end with the address of its error result:
//
//	ctx, observed := observeOperation(ctx, "extract")
//	defer observed.end(&err)
//
// With no observer installed nothing is started and the value is nil, whose
// end does nothing. The same is true when ctx is already inside a call of the
// same operation that has not ended: a Result form wraps the plain form, and
// the plain form runs through the operation runner, so one call of an
// operation passes through here up to three times and is one unit.
//
// The name is the one the caller's API gave the call when ctx carries one for
// this operation (WithCallerName), so that the scope and the record of a
// version-2 call are named alike; for the old root's calls it is name.
func observeOperation(ctx context.Context, name string) (context.Context, *observedOperation) {
	if !telemetry.HasObserver() || name == "" {
		return ctx, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	name = nameForCaller(ctx, name)
	noteScopeOpened(ctx)
	open, _ := ctx.Value(observedOperationKey{}).(*observedOperation)
	if open != nil && open.name == name && !open.ended.Load() {
		return ctx, nil
	}

	observed := &observedOperation{name: name, start: time.Now(), parent: open}
	observed.ctx = telemetry.ObserveOperationStart(ctx, telemetry.OperationEvent{
		Scope:     telemetry.ScopeOperation,
		Operation: name,
	})
	return context.WithValue(observed.ctx, observedOperationKey{}, observed), observed
}

// recordItems says what became of the items of the run this call is: how many
// it was given and how many of them succeeded, failed and were cancelled, and
// the error of the first that failed, by position. The counts are all the
// observers are told of the items, and the error only decides the kind of a run
// in which every item failed. A call that is not a run never calls it. A nil
// value, which is what a call nobody observes holds, ignores it.
func (o *observedOperation) recordItems(total, succeeded, failed, cancelled int, firstFailure error) {
	if o == nil {
		return
	}
	o.items, o.succeeded, o.failed, o.cancelled, o.firstFailure = total, succeeded, failed, cancelled, firstFailure
}

// recordAllOrNothing is recordItems for a run that returns every item's result
// or none: a call that failed returned no item, so each is counted as failed.
func (o *observedOperation) recordAllOrNothing(total int, err error) {
	if err != nil {
		o.recordItems(total, 0, total, 0, err)
		return
	}
	o.recordItems(total, total, 0, 0, nil)
}

// result is what the observers are told of the call, short of how it ended.
func (o *observedOperation) result() telemetry.OperationResult {
	return telemetry.OperationResult{
		Scope:     telemetry.ScopeOperation,
		Operation: o.name,
		Requests:  int(o.requests.Load()),
		Items:     o.items,
		Succeeded: o.succeeded,
		Failed:    o.failed,
		Cancelled: o.cancelled,
	}
}

// end tells the observers how the call of the operation ended: with the kind of
// the error err points at, or with none. It is deferred directly, because it
// has to see a panic: a call that panics returns no error, and reporting it as
// a success is the one thing a trace of a failure must not do. A panic is
// reported as telemetry.ErrorKindInternal and then continues.
//
// A run over many items that collects its failures returns a result and no
// error, and a call in which some item succeeded ended well: its counts say how
// many did not. One in which every item failed produced nothing, and ends with
// the kind of the first item's failure, though the call returned no error. An
// alert on the error rate of operations would otherwise never see it.
func (o *observedOperation) end(err *error) {
	if o == nil {
		return
	}
	result := o.result()

	if recovered := recover(); recovered != nil {
		result.Duration = time.Since(o.start)
		result.ErrorKind = telemetry.ErrorKindInternal
		o.ended.Store(true)
		telemetry.ObserveOperationEnd(o.ctx, result)
		panic(recovered)
	}

	result.Duration = time.Since(o.start)
	switch {
	case err != nil && *err != nil:
		// The kind, from the one place allowed to decide what a failure is
		// (classify.go). Never the error's text: it can quote a provider's echo
		// of the caller's input, and an observer's output is an export.
		result.ErrorKind = observedErrorKind(*err, llm.Classify(*err))
	case o.items > 0 && o.succeeded == 0 && o.failed > 0:
		result.ErrorKind = types.KindUnknown.String()
		if o.firstFailure != nil {
			result.ErrorKind = observedErrorKind(o.firstFailure, llm.Classify(o.firstFailure))
		}
	}
	o.ended.Store(true)
	telemetry.ObserveOperationEnd(o.ctx, result)
}

// observedStream is one streamed request as the observers and the metrics are
// told of it. It is part of its TextStream, and its methods are called by the
// goroutine that produces the stream and by no other.
type observedStream struct {
	// observing is whether an observer was installed as the request was made.
	// When none was, nothing was started and nothing is finished.
	observing bool
	ctx       context.Context
	operation string

	// What the request was sent to, and with which settings.
	provider     string
	model        string
	mode         types.Mode
	intelligence types.Speed
	start        time.Time

	// answered and response are the stream's last chunk, once it has arrived:
	// what the provider said of its answer, whether or not the answer is used.
	answered bool
	response llm.CompletionResponse
	ended    bool
}

// begin tells the installed observers that the streamed request is being made
// and returns the context to make it on, which is ctx when nobody is
// observing. A stream is one attempt: it is not retried, since text the caller
// has been shown cannot be taken back.
func (o *observedStream) begin(ctx context.Context, provider llm.Provider, model, responseFormat string,
	opts types.OpOptions, requestID, correlationID string, start time.Time) context.Context {

	o.provider, o.model, o.mode, o.intelligence, o.start = provider.Name(), model, opts.Mode, opts.Intelligence, start
	if !telemetry.HasObserver() {
		return ctx
	}
	o.observing = true
	countRequest(ctx)
	o.operation = operationNameFor(ctx, opts)
	o.ctx = telemetry.ObserveOperationStart(ctx, telemetry.OperationEvent{
		Operation:      o.operation,
		Provider:       o.provider,
		Model:          model,
		RequestID:      requestID,
		CorrelationID:  correlationID,
		Mode:           opts.Mode.String(),
		Intelligence:   opts.Intelligence.String(),
		SchemaName:     opts.SchemaName,
		ResponseFormat: responseFormat,
		Stream:         true,
	})
	return o.ctx
}

// answer records the stream's last chunk.
func (o *observedStream) answer(resp llm.CompletionResponse) {
	o.answered, o.response = true, resp
}

// end says how the stream finished, once. A stream that failed is counted by
// its kind in the request metrics, as a failed buffered request is. The
// observers that saw the request start are told it finished: with the kind of
// outcome when it is an error, with telemetry.ErrorKindInternal when the
// producer is unwinding from a panic, and with what the provider reported of
// its answer when the last chunk arrived. The cost is given for an answer that
// was delivered, as it is for a buffered request.
func (o *observedStream) end(outcome error, panicked bool) {
	if o.ended {
		return
	}
	o.ended = true

	kind := ""
	switch {
	case panicked:
		kind = telemetry.ErrorKindInternal
	case outcome != nil:
		kind = requestErrorKind(outcome, o.answered, o.response)
	}
	if kind != "" {
		telemetry.RecordLLMFailure(o.provider, o.model, o.mode, o.intelligence, kind)
	}
	if !o.observing {
		return
	}

	result := telemetry.OperationResult{
		Operation: o.operation,
		Duration:  time.Since(o.start),
		Attempts:  1,
		ErrorKind: kind,
	}
	if o.answered {
		result.FinishReason = o.response.FinishReason
		result.Usage = o.response.Usage
		result.Provider = o.response.Provider
		result.Model = o.response.Model
	}
	if kind == "" && o.answered {
		if result.Provider == "" {
			result.Provider = o.provider
		}
		if result.Model == "" {
			result.Model = o.model
		}
		usage := o.response.Usage
		if cost := pricing.CalculateCostLogged(loggerFor(o.ctx), &usage, result.Model, result.Provider); cost != nil {
			result.Cost = *cost
		}
	}
	telemetry.ObserveOperationEnd(o.ctx, result)
}

// observedErrorKind is the name an observer is told for err, whose kind is
// kind: the kind's own name, except for the two marks an error can carry
// (types.NarrowingOf). An answer the model declined to give, or that the
// provider's filter withheld, is "refused" and not "policy violation", which is
// also what a data policy that forbids a route is. A failure marked as a
// recovered panic is "internal" and not "invariant violation", which is also
// what an answer that broke its own rules is: a provider or a middleware that
// reports an internal failure, a copy of a hedge that panicked and a step of a
// Gather reach the caller as internal, and an observer that was told they were
// the model's answer being invalid would page the wrong team.
func observedErrorKind(err error, kind types.ErrorKind) string {
	switch types.NarrowingOf(err) {
	case types.NarrowRefusal:
		return telemetry.ErrorKindRefused
	case types.NarrowPanic:
		return telemetry.ErrorKindInternal
	}
	return kind.String()
}

// withheldByTheFilter reports that err is the failure of an answer that arrived
// and that the provider's content filter withheld, and that nothing has marked
// as a refusal yet: an answer refused for carrying no content has an error with
// no kind of its own (or the empty answer's, which the request still has to
// decide whether to ask again over), and classify.go's reading of the answer
// says why it is empty.
func withheldByTheFilter(err error, answered bool, resp llm.CompletionResponse) bool {
	if err == nil || !answered {
		return false
	}
	if llm.Classify(err) != types.KindUnknown && !errors.Is(err, errEmptyCompletion) {
		return false
	}
	return llm.ClassifyCompletion(resp) == types.KindPolicyViolation
}

// requestErrorKind is the kind a request's finish and its retry notes report
// for err. An answer that arrived and was refused for carrying no content has
// an error with no kind of its own, and classify.go's reading of the answer
// says what it is; any other error is classified as it is everywhere else. A
// refusal, whether marked on the error or read from the finish of the answer,
// is "refused".
func requestErrorKind(err error, answered bool, resp llm.CompletionResponse) string {
	if withheldByTheFilter(err, answered, resp) {
		return telemetry.ErrorKindRefused
	}
	kind := llm.Classify(err)
	if kind == types.KindUnknown && answered {
		if ofAnswer := llm.ClassifyCompletion(resp); ofAnswer != types.KindUnknown {
			kind = ofAnswer
		}
	}
	return observedErrorKind(err, kind)
}

// refusedAnswers is what one request paid for in answers it then asked again
// after: an empty one, a truncated one, one from a route the client's policy
// forbids. Each was produced and charged, to the client's ledger and to the
// process-wide one, so the request's finish reports them with the answer that
// was accepted, as the call record sums a call's attempts. The finish used to
// report the last answer alone, and a cost dashboard built on spans
// under-reported every request that had to ask twice.
//
// It is filled only when an observer is installed, and only on the path that
// refused an answer, so a request that is answered the first time does none of
// this.
type refusedAnswers struct {
	usage types.TokenUsage
	costs []*types.CostInfo
}

// add records a refused answer, priced the way the request prices the one it
// accepts: by the model and provider the answer names, then by the ones the
// request was sent to. An answer that reported no usage, and a cache hit, were
// not bought, and trackRefusedAnswer leaves them out of the process-wide
// ledger on the same grounds.
func (r *refusedAnswers) add(provider, model string, resp llm.CompletionResponse) {
	usage := resp.Usage
	if resp.Cached || !reportsUsage(usage) {
		return
	}
	if resp.Model != "" {
		model = resp.Model
	}
	if resp.Provider != "" {
		provider = resp.Provider
	}
	r.usage = addTokenUsage(r.usage, usage)
	r.costs = append(r.costs, pricing.CalculateCost(&usage, model, provider))
}

// any reports whether a refused answer was recorded.
func (r *refusedAnswers) any() bool { return len(r.costs) > 0 }

// plus returns the usage and the cost of the refused answers and the accepted
// one together. With no refused answer it returns the accepted answer's own,
// unchanged. A total is priced only when every answer in it was: one answer
// with no price makes the sum an under-count, which the figure then says.
func (r *refusedAnswers) plus(accepted types.TokenUsage, cost *types.CostInfo) (types.TokenUsage, types.CostInfo) {
	if !r.any() {
		if cost == nil {
			return accepted, types.CostInfo{}
		}
		return accepted, *cost
	}

	costs := r.costs
	if cost != nil {
		costs = append(costs[:len(costs):len(costs)], cost)
	}
	var total types.CostInfo
	records := make([]*types.ResultMetadata, 0, len(costs))
	priced, sawCost := true, false
	for _, each := range costs {
		if each == nil {
			continue
		}
		records = append(records, &types.ResultMetadata{CostInfo: each})
		sawCost = true
		if !each.Priced {
			priced = false
			continue
		}
		total.TotalCost += each.TotalCost
		total.PromptCost += each.PromptCost
		total.CompletionCost += each.CompletionCost
		total.CachedCost += each.CachedCost
		total.ReasoningCost += each.ReasoningCost
		if total.Currency == "" {
			total.Currency = each.Currency
		}
	}
	total.Priced = sawCost && priced
	if !total.Priced {
		total.Currency = ""
	}
	total.Quality = combinedPricingQuality(records)
	return addTokenUsage(r.usage, accepted), total
}

// addTokenUsage adds every count of two usages.
func addTokenUsage(a, b types.TokenUsage) types.TokenUsage {
	return types.TokenUsage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
		InputTokens:      a.InputTokens + b.InputTokens,
		OutputTokens:     a.OutputTokens + b.OutputTokens,
		CachedTokens:     a.CachedTokens + b.CachedTokens,
		ReasoningTokens:  a.ReasoningTokens + b.ReasoningTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
	}
}

// withKindOfTheAnswer returns err carrying the kind the request's finish gives
// it, when it has none of its own. An answer refused for carrying no content
// is such an error, and it has to stay without a kind while the request decides
// whether to ask again, since the kind it would be given says the repeat is not
// worth making. It is called on the way out, once that is decided, so that the
// error the request returns says what the request's span says: a span that
// calls the request a malformed output and an operation span above it that
// calls the same failure unknown disagree about one failure.
//
// The error's text and its chain are untouched.
func withKindOfTheAnswer(err error, answered bool, resp llm.CompletionResponse) error {
	if err == nil || !answered || llm.Classify(err) != types.KindUnknown {
		return err
	}
	if kind := llm.ClassifyCompletion(resp); kind != types.KindUnknown {
		return classifiedByTheFinish(kind, err)
	}
	return err
}

// classifiedByTheFinish is types.Classified for the kind a finish reason names,
// except that a policy violation named by the finish is the provider's filter
// withholding the answer, which is a refusal and carries the mark that says so.
func classifiedByTheFinish(kind types.ErrorKind, err error) error {
	if kind == types.KindPolicyViolation {
		return types.ClassifiedRefusal(err)
	}
	return types.Classified(kind, err)
}

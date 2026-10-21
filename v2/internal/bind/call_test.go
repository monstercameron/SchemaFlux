package bind

import (
	"context"
	"errors"
	"iter"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/mw"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	itelemetry "github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The tests of a call share process state the call path reads: the default
// client, the installed observers and the hooks the root registers. None of
// them is parallel, and each puts back what it changed.

// TestMain keeps the call path's own log lines (an unpriced test model, a prompt
// cache that never hits) out of the output of the tests.
func TestMain(m *testing.M) {
	logger.SetLogger(itelemetry.Discard())
	os.Exit(m.Run())
}

const (
	scriptedName  = "bind-scripted"
	scriptedModel = "bind-model"
)

// scriptedLLM is an internal provider that answers from a script: reply n is
// the answer to request n, and the last one repeats.
type scriptedLLM struct {
	name    string
	replies []string
	fail    error
	// keep makes the provider remember what it was sent. The allocation tests
	// leave it off: a growing list is an allocation of the fixture's own.
	keep bool

	mu       sync.Mutex
	count    int
	requests []llm.CompletionRequest
}

func newScripted(replies ...string) *scriptedLLM {
	return &scriptedLLM{name: scriptedName, replies: replies, keep: true}
}

func (p *scriptedLLM) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	n := p.count
	p.count++
	if p.keep {
		p.requests = append(p.requests, req)
	}
	p.mu.Unlock()
	if p.fail != nil {
		return llm.CompletionResponse{}, p.fail
	}
	body := ""
	if len(p.replies) > 0 {
		body = p.replies[min(n, len(p.replies)-1)]
	}
	return llm.CompletionResponse{
		Content: body, Provider: p.name, Model: req.Model, FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14},
	}, nil
}
func (p *scriptedLLM) Name() string                               { return p.name }
func (p *scriptedLLM) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *scriptedLLM) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// sent is how many requests the provider has been asked.
func (p *scriptedLLM) sent() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

// streamingLLM is a scriptedLLM that also streams: its stream is a function, so
// a test decides where it blocks and what it yields.
type streamingLLM struct {
	scriptedLLM
	run func(ctx context.Context, yield func(llm.StreamChunk, error) bool)
}

func (p *streamingLLM) CompleteStream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) { p.run(ctx, yield) }
}

// streamCompletes yields the deltas and finishes the answer cleanly.
func streamCompletes(deltas ...string) func(context.Context, func(llm.StreamChunk, error) bool) {
	return func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
		for _, delta := range deltas {
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
			Content: strings.Join(deltas, ""), Provider: scriptedName, Model: scriptedModel, FinishReason: "stop",
			Usage: types.TokenUsage{PromptTokens: 7, CompletionTokens: 5, TotalTokens: 12},
		}}, nil)
	}
}

// recordingSink is a recorder as the call path sees one. It keeps the outcomes
// it is given and says what it asked for.
type recordingSink struct {
	decisions bool
	bodies    bool
	maxBody   int

	mu       sync.Mutex
	outcomes []*ops.Outcome
	contexts []context.Context
}

func (s *recordingSink) Wants() (bool, bool, int) { return s.decisions, s.bodies, s.maxBody }

func (s *recordingSink) Add(ctx context.Context, o *ops.Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes = append(s.outcomes, o)
	s.contexts = append(s.contexts, ctx)
}

func (s *recordingSink) given() []*ops.Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*ops.Outcome(nil), s.outcomes...)
}

// only returns the one outcome the sink was given, or fails the test.
func (s *recordingSink) only(t *testing.T) *ops.Outcome {
	t.Helper()
	got := s.given()
	if len(got) != 1 {
		t.Fatalf("the sink was given %d outcomes, want exactly 1", len(got))
	}
	return got[0]
}

// clientSetting changes the snapshot a test client runs on.
type clientSetting func(*ops.ExecConfig)

// withFloor gives the client a data policy that allows the scripted route and
// holds answers to level.
func withFloor(level types.ContractLevel) clientSetting {
	return func(cfg *ops.ExecConfig) {
		cfg.DataPolicyDeclared = true
		cfg.DataPolicy = types.DataPolicy{
			AllowedProviders: []string{scriptedName},
			AllowedRegions:   []string{"eu"},
			MinimumContract:  level,
		}
	}
}

// withClientSink gives the client a recorder of its own.
func withClientSink(s ops.Sink) clientSetting {
	return func(cfg *ops.ExecConfig) { cfg.Sink = s }
}

// clientConfig is the snapshot of a test client on provider.
func clientConfig(provider llm.Provider, settings ...clientSetting) *ops.ExecConfig {
	llm.DeclareRoute(scriptedName, scriptedModel, []string{"eu"}, 0)
	cfg := &ops.ExecConfig{Provider: provider, Model: scriptedModel}
	for _, apply := range settings {
		apply(cfg)
	}
	return cfg
}

// clientContext is a context that carries a client on provider.
func clientContext(provider llm.Provider, settings ...clientSetting) context.Context {
	return ops.WithSharedExecConfig(context.Background(), clientConfig(provider, settings...))
}

// withDefaultClient makes cfg the default snapshot for the rest of the test.
func withDefaultClient(t *testing.T, cfg *ops.ExecConfig) {
	t.Helper()
	previous := ops.SetDefaultExec(cfg)
	t.Cleanup(func() { ops.SetDefaultExec(previous) })
}

// watcher is a telemetry observer that remembers the operations it saw finish.
type watcher struct {
	mu       sync.Mutex
	finished []telemetry.OperationResult
}

func (w *watcher) OperationStarted(ctx context.Context, _ telemetry.OperationEvent) context.Context {
	return ctx
}

func (w *watcher) OperationFinished(_ context.Context, result telemetry.OperationResult) {
	w.mu.Lock()
	w.finished = append(w.finished, result)
	w.mu.Unlock()
}

// operations returns the names of the operation scopes that finished.
func (w *watcher) operations() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var names []string
	for _, result := range w.finished {
		if result.Scope == telemetry.ScopeOperation {
			names = append(names, result.Operation)
		}
	}
	return names
}

func observe(t *testing.T) *watcher {
	t.Helper()
	w := &watcher{}
	t.Cleanup(telemetry.AddObserver(w))
	return w
}

// converted is the stand-in root's error; asConverted finds it in err.
func asConverted(t *testing.T, err error) *converted {
	t.Helper()
	var c *converted
	if !errors.As(err, &c) {
		t.Fatalf("the error %v (%T) was not converted through the hook", err, err)
	}
	return c
}

func TestBeginRefusesANilContextAsAnInvalidRequestConvertedThroughTheHook(t *testing.T) {
	registerStandIn(t)
	for _, op := range []string{"classify", "is", "extract", "summarize", "streamsummarize", "exp.synthesize", "redact.text", "flow.vote", "op.run", ""} {
		t.Run("operation "+op, func(t *testing.T) {
			var nilContext context.Context
			call, err := Begin(nilContext, op)
			if err == nil {
				t.Fatal("a nil context was accepted")
			}
			c := asConverted(t, err)
			if c.op != op {
				t.Errorf("the hook was given operation %q, want %q", c.op, op)
			}
			if types.KindOf(err) != types.KindInvalidRequest {
				t.Errorf("kind = %v, want an invalid request", types.KindOf(err))
			}
			if !strings.Contains(err.Error(), "the context is nil") {
				t.Errorf("message = %q", err.Error())
			}
			if call.Context() != nil || call.Plain() {
				t.Errorf("a refused Begin returned a call that can be used: context %v, plain %v", call.Context(), call.Plain())
			}
			if err := call.End(nil, nil); err != nil {
				t.Errorf("End of the empty call = %v", err)
			}
		})
	}
}

func TestBeginWithNoRootRegisteredStillRefusesANilContext(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	var nilContext context.Context
	_, err := Begin(nilContext, "classify")
	if err == nil || types.KindOf(err) != types.KindInvalidRequest {
		t.Fatalf("Begin(nil) = %v (kind %v)", err, types.KindOf(err))
	}
}

func TestBeginPinsTheDefaultClientOnceAndAnotherPinAddsNoContextValue(t *testing.T) {
	first := &ops.ExecConfig{Provider: newScripted(), Model: "first-model"}
	second := &ops.ExecConfig{Provider: newScripted(), Model: "second-model"}
	withDefaultClient(t, first)

	call, err := Begin(context.Background(), "classify")
	if err != nil {
		t.Fatal(err)
	}
	pinned, cfg := ops.PinExec(call.Context())
	if cfg != first {
		t.Fatalf("the call pinned %p, want the default %p", cfg, first)
	}
	if pinned != call.Context() {
		t.Error("pinning again wrapped the context a second time")
	}
	if got := testing.AllocsPerRun(100, func() { ops.PinExec(call.Context()) }); got != 0 {
		t.Errorf("a second pin allocates %v times, want 0", got)
	}

	// A default replaced while the call is in flight is not the call's client.
	ops.SetDefaultExec(second)
	if got := ops.ExecModel(call.Context()); got != "first-model" {
		t.Errorf("the call reads model %q after the default was replaced, want first-model", got)
	}
	// The next call starts on the new default.
	next, _ := Begin(context.Background(), "classify")
	if got := ops.ExecModel(next.Context()); got != "second-model" {
		t.Errorf("the next call reads model %q, want second-model", got)
	}
}

func TestBeginLeavesAContextThatCarriesAClientAsItIs(t *testing.T) {
	withDefaultClient(t, &ops.ExecConfig{Provider: newScripted(), Model: "default-model"})
	own := clientContext(newScripted())
	call, err := Begin(own, "is")
	if err != nil {
		t.Fatal(err)
	}
	if call.Context() != own {
		t.Error("a context that carries a client was wrapped")
	}
	if got := ops.ExecModel(call.Context()); got != scriptedModel {
		t.Errorf("the call reads model %q, want the context's client's %q", got, scriptedModel)
	}
}

func TestBeginWithNoClientAnywhereLeavesTheContextAsItIs(t *testing.T) {
	withDefaultClient(t, nil)
	ctx := context.WithValue(context.Background(), struct{ k int }{1}, "v")
	call, err := Begin(ctx, "is")
	if err != nil {
		t.Fatal(err)
	}
	if call.Context() != ctx || !call.Plain() {
		t.Errorf("context kept %v, plain %v", call.Context() == ctx, call.Plain())
	}
}

func TestPlainIsTrueExactlyWhenThereIsNoSinkNoObserverAndNoFloor(t *testing.T) {
	sink := &recordingSink{}
	other := &recordingSink{}
	cases := []struct {
		name    string
		observe bool
		ctx     func() context.Context
		def     *ops.ExecConfig
		want    bool
	}{
		{"a client and nothing else", false, func() context.Context { return clientContext(newScripted()) }, nil, true},
		{"the default client and nothing else", false, func() context.Context { return context.Background() }, clientConfig(newScripted()), true},
		{"no client at all", false, func() context.Context { return context.Background() }, nil, true},
		{"a recorder on the context", false, func() context.Context { return ops.WithSink(clientContext(newScripted()), sink) }, nil, false},
		{"a recorder on the client", false, func() context.Context { return clientContext(newScripted(), withClientSink(sink)) }, nil, false},
		{"a recorder on the context and no client", false, func() context.Context { return ops.WithSink(context.Background(), sink) }, nil, false},
		{"a recorder on the default client", false, func() context.Context { return context.Background() }, clientConfig(newScripted(), withClientSink(sink)), false},
		{"two recorders", false, func() context.Context { return ops.WithSink(clientContext(newScripted(), withClientSink(other)), sink) }, nil, false},
		{"an observer", true, func() context.Context { return clientContext(newScripted()) }, nil, false},
		{"an observer and no client", true, func() context.Context { return context.Background() }, nil, false},
		{"a policy that sets no floor", false, func() context.Context { return clientContext(newScripted(), withFloor(types.ContractPromptOnly)) }, nil, true},
		{"a floor of JSON well-formed", false, func() context.Context { return clientContext(newScripted(), withFloor(types.ContractJSONWellFormed)) }, nil, false},
		{"a floor of schema and invariants", false, func() context.Context {
			return clientContext(newScripted(), withFloor(types.ContractSchemaAndInvariantChecked))
		}, nil, false},
		{"the highest floor", false, func() context.Context { return clientContext(newScripted(), withFloor(types.ContractFullyGoverned)) }, nil, false},
		{"a floor on the default client", false, func() context.Context { return context.Background() }, clientConfig(newScripted(), withFloor(types.ContractSchemaConstrained)), false},
		{"a policy with no floor and a recorder", false, func() context.Context {
			return ops.WithSink(clientContext(newScripted(), withFloor(types.ContractPromptOnly)), sink)
		}, nil, false},
		{"a recorder, an observer and a floor", true, func() context.Context {
			return ops.WithSink(clientContext(newScripted(), withFloor(types.ContractJSONWellFormed)), sink)
		}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDefaultClient(t, tc.def)
			if tc.observe {
				observe(t)
			}
			call, err := Begin(tc.ctx(), "classify")
			if err != nil {
				t.Fatal(err)
			}
			if got := call.Plain(); got != tc.want {
				t.Errorf("Plain() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBeginAndEndOnThePlainPathAllocateNothingUnderAClientAndOneOnTheDefault(t *testing.T) {
	provider := newScripted()
	run := func(ctx context.Context) func() {
		return func() {
			call, _ := Begin(ctx, "classify")
			_ = call.End(nil, nil)
		}
	}

	withDefaultClient(t, nil)
	if got := testing.AllocsPerRun(200, run(context.Background())); got != 0 {
		t.Errorf("no client anywhere: %v allocations, want 0", got)
	}
	if got := testing.AllocsPerRun(200, run(clientContext(provider))); got != 0 {
		t.Errorf("under a client on the context: %v allocations, want 0", got)
	}
	if got := testing.AllocsPerRun(200, run(clientContext(provider, withFloor(types.ContractPromptOnly)))); got != 0 {
		t.Errorf("under a client with a policy that sets no floor: %v allocations, want 0", got)
	}
	withDefaultClient(t, clientConfig(provider))
	if got := testing.AllocsPerRun(200, run(context.Background())); got != 1 {
		t.Errorf("on the default client: %v allocations, want exactly the pin's 1", got)
	}
}

func TestEndOfAPlainCallConvertsTheErrorAndNothingElse(t *testing.T) {
	registerStandIn(t)
	call, err := Begin(clientContext(newScripted()), "classify")
	if err != nil {
		t.Fatal(err)
	}
	if got := call.End(nil, nil); got != nil {
		t.Errorf("End(nil, nil) = %v", got)
	}
	cause := types.Newf(types.KindTimeout, "classify", "slow")
	got := call.End(&types.Meta{Operation: "ignored"}, cause)
	if c := asConverted(t, got); c.op != "classify" || !errors.Is(got, cause) {
		t.Errorf("End = %v, want the cause converted for classify", got)
	}
}

func TestEndWithNoRootRegisteredReturnsTheErrorUnchanged(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	call, _ := Begin(clientContext(newScripted()), "classify")
	cause := errors.New("x")
	if got := call.End(nil, cause); got != cause {
		t.Errorf("End = %v, want the same error", got)
	}
}

// bodyOf and askOf are what the tests below put in a call's state in place of a
// real operation: a refused body and one decision.
const refusedBody = "héllo wörld"

func TestEndGivesEachSinkItsOwnOutcomeHoldingWhatItAskedFor(t *testing.T) {
	registerStandIn(t)
	wantsAll := &recordingSink{decisions: true, bodies: true, maxBody: 9}
	wantsNothing := &recordingSink{maxBody: 100}
	wantsShortBody := &recordingSink{bodies: true, maxBody: 2}
	ctx := ops.WithSink(ops.WithSink(clientContext(newScripted(), withClientSink(wantsShortBody)), wantsAll), wantsNothing)

	call, err := Begin(ctx, "is")
	if err != nil {
		t.Fatal(err)
	}
	if call.Plain() {
		t.Fatal("a call with three recorders is plain")
	}
	call.state.Capture(types.DiagnosticRecord{Body: refusedBody})
	call.state.observe(call.Context(), ops.DecisionRecord{
		Operation: "is", AnsweredBy: "jev", Typed: true,
		Asked: []ops.AnsweredQuestion{{Question: "Is it?"}},
	})

	meta := &types.Meta{Operation: "internal-name", Attempts: 2, Checks: []types.Check{{Name: "first", Passed: true}}, Elapsed: time.Hour}
	got := call.End(meta, nil)
	if got != nil {
		t.Fatalf("End = %v", got)
	}

	for name, want := range map[string]struct {
		sink    *recordingSink
		asked   int
		body    string
		bodyWhy string
	}{
		"asked for everything":   {wantsAll, 1, "héllo w", "cut at its own bound of 9 bytes, which falls inside ö, back to the character before"},
		"asked for nothing":      {wantsNothing, 0, "", "none was asked for"},
		"asked for a short body": {wantsShortBody, 0, "h", "cut at its bound of 2 bytes, which falls inside é, back to the character before"},
	} {
		o := want.sink.only(t)
		if len(o.Asked) != want.asked {
			t.Errorf("%s: %d questions, want %d", name, len(o.Asked), want.asked)
		}
		if o.Body != want.body {
			t.Errorf("%s: body %q, want %q (%s)", name, o.Body, want.body, want.bodyWhy)
		}
		if o.AnsweredBy != "jev" || !o.Typed {
			t.Errorf("%s: answered by %q typed %v, want the facts of the decision in every outcome", name, o.AnsweredBy, o.Typed)
		}
		if o.Meta.Operation != "is" {
			t.Errorf("%s: operation %q, want the call's own", name, o.Meta.Operation)
		}
		if o.Meta.Attempts != 2 || o.Meta.Elapsed == time.Hour || o.Start.IsZero() || o.Err != nil {
			t.Errorf("%s: meta %+v start %v err %v", name, o.Meta, o.Start, o.Err)
		}
		if want.sink.contexts[0] != call.Context() {
			t.Errorf("%s: the sink was given another context than the call's", name)
		}
	}

	// Each outcome is its own copy: changing one changes neither another nor the
	// caller's meta.
	a, b := wantsAll.only(t), wantsNothing.only(t)
	a.Meta.Checks[0].Name = "changed"
	if b.Meta.Checks[0].Name != "first" || meta.Checks[0].Name != "first" {
		t.Error("the outcomes share the checks of the meta")
	}
	if got := meta.Operation; got != "internal-name" {
		t.Errorf("End changed the caller's meta: operation %q", got)
	}
}

func TestEndGivesEachSinkTheCallsErrorOnFailure(t *testing.T) {
	registerStandIn(t)
	first, second := &recordingSink{}, &recordingSink{}
	call, _ := Begin(ops.WithSink(clientContext(newScripted(), withClientSink(first)), second), "extract")
	cause := types.Newf(types.KindProviderUnavailable, "extract", "down")
	returned := call.End(&types.Meta{Attempts: 3}, cause)
	c := asConverted(t, returned)
	if c.op != "extract" || !errors.Is(returned, cause) {
		t.Fatalf("returned %v", returned)
	}
	for _, sink := range []*recordingSink{first, second} {
		o := sink.only(t)
		if o.Err != returned {
			t.Errorf("the outcome holds %v, want the error the call returned", o.Err)
		}
		if o.Meta.Attempts != 3 {
			t.Errorf("attempts %d, want 3", o.Meta.Attempts)
		}
	}
}

func TestACallThatRefusedBeforeTheInternalCallStillGivesARecordWithNoMeta(t *testing.T) {
	registerStandIn(t)
	cases := []struct {
		name   string
		refuse func(call *Call) error
		kind   types.ErrorKind
	}{
		{"ended with a nil meta", func(c *Call) error {
			return c.End(nil, types.Newf(types.KindInvalidRequest, "classify", "no categories"))
		}, types.KindInvalidRequest},
		{"ended with a configuration error", func(c *Call) error { return c.End(nil, types.Newf(types.KindConfiguration, "classify", "bad option")) }, types.KindConfiguration},
		{"refused by the call", func(c *Call) error { return c.Refuse(types.KindInvalidRequest, "criteria is blank") }, types.KindInvalidRequest},
		{"refused by the call, a configuration error", func(c *Call) error { return c.Refuse(types.KindConfiguration, "MaxWords must not be %s", "negative") }, types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			call, _ := Begin(ops.WithSink(clientContext(newScripted()), sink), "classify")
			err := tc.refuse(&call)
			if asConverted(t, err).op != "classify" || types.KindOf(err) != tc.kind {
				t.Fatalf("err = %v, kind %v", err, types.KindOf(err))
			}
			o := sink.only(t)
			meta := o.Meta
			meta.Elapsed = 0
			if meta.Operation != "classify" || meta.Attempts != 0 || meta.Usage != (types.TokenUsage{}) || meta.RequestID != "" {
				t.Errorf("meta = %+v, want the operation and nothing the call did not do", meta)
			}
			if o.Err != err {
				t.Errorf("the outcome holds %v, want the returned error", o.Err)
			}
		})
	}
}

func TestEndGivesTheSinksTheOutcomeOnceHoweverOftenItIsCalled(t *testing.T) {
	registerStandIn(t)
	sink := &recordingSink{}
	call, _ := Begin(ops.WithSink(clientContext(newScripted()), sink), "is")
	for range 5 {
		if err := call.End(nil, errors.New("x")); err == nil {
			t.Fatal("the error was lost")
		}
	}
	copied := call
	copied.End(nil, nil)
	if got := len(sink.given()); got != 1 {
		t.Errorf("%d outcomes, want 1", got)
	}

	// Many goroutines ending copies of one call deliver once.
	again := &recordingSink{}
	shared, _ := Begin(ops.WithSink(clientContext(newScripted()), again), "is")
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { c := shared; c.End(nil, nil) })
	}
	wg.Wait()
	if got := len(again.given()); got != 1 {
		t.Errorf("%d outcomes from 16 concurrent ends, want 1", got)
	}
}

func TestASinkOnTheContextAndTheClientIsGivenOneOutcome(t *testing.T) {
	sink := &recordingSink{}
	call, _ := Begin(ops.WithSink(clientContext(newScripted(), withClientSink(sink)), sink), "is")
	call.End(nil, nil)
	if got := len(sink.given()); got != 1 {
		t.Errorf("%d outcomes, want 1", got)
	}
}

func TestACallThatIsOnlyObservedOrUnderAFloorHasNothingToDeliver(t *testing.T) {
	t.Run("observed", func(t *testing.T) {
		observe(t)
		call, _ := Begin(clientContext(newScripted()), "is")
		if call.Plain() || call.state != nil {
			t.Errorf("plain %v, state %v: an observed call with no recorder collects nothing", call.Plain(), call.state)
		}
		if err := call.End(nil, nil); err != nil {
			t.Error(err)
		}
	})
	t.Run("floor", func(t *testing.T) {
		call, _ := Begin(clientContext(newScripted(), withFloor(types.ContractSchemaConstrained)), "is")
		if call.Plain() || call.state != nil {
			t.Errorf("plain %v, state %v", call.Plain(), call.state)
		}
	})
}

func TestRefuseBuildsTheConvertedErrorOfACallRefusedBeforeAnythingWasSent(t *testing.T) {
	registerStandIn(t)
	kinds := []types.ErrorKind{
		types.KindInvalidRequest, types.KindConfiguration, types.KindPolicyViolation, types.KindTimeout,
		types.KindCanceled, types.KindProviderUnavailable, types.KindMalformedOutput, types.KindSchemaViolation,
		types.KindOutputTruncated, types.KindUnsupportedCapability,
	}
	for _, kind := range kinds {
		t.Run(kind.String(), func(t *testing.T) {
			err := Refuse("flow.vote", kind, "%d of %d voters agreed", 2, 5)
			c := asConverted(t, err)
			if c.op != "flow.vote" {
				t.Errorf("the hook was given %q", c.op)
			}
			if types.KindOf(err) != kind {
				t.Errorf("kind = %v, want %v", types.KindOf(err), kind)
			}
			if !strings.Contains(err.Error(), "2 of 5 voters agreed") {
				t.Errorf("message = %q", err.Error())
			}
		})
	}
	t.Run("with no root the error is the internal one", func(t *testing.T) {
		unregister()
		err := Refuse("is", types.KindInvalidRequest, "question is blank")
		var op *types.OperationError
		if !errors.As(err, &op) || op.Kind != types.KindInvalidRequest || op.Op != "is" {
			t.Errorf("err = %#v", err)
		}
	})
}

// recordedLevels is the whole ladder of contract levels.
var recordedLevels = []types.ContractLevel{
	types.ContractPromptOnly, types.ContractJSONWellFormed, types.ContractSchemaConstrained,
	types.ContractSchemaAndInvariantChecked, types.ContractEvidenceChecked, types.ContractFullyGoverned,
}

func TestRecordedRunsTheFunctionUnderTheRecordedEntryAtTheLevelGiven(t *testing.T) {
	registerStandIn(t)
	provider := newScripted(`{"ok":true}`)
	for _, level := range recordedLevels {
		t.Run(level.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := &recordingSink{}
				call, _ := Begin(ops.WithSink(clientContext(provider), sink), "transform")
				var seen context.Context
				got, err := Recorded(&call, level, func(ctx context.Context) (int, error) {
					seen = ctx
					time.Sleep(3 * time.Second)
					return 7, nil
				})
				if err != nil || got != 7 {
					t.Fatalf("Recorded = %d, %v", got, err)
				}
				if seen == nil || seen == call.Context() {
					t.Error("the function was not given the context of the recorded entry")
				}
				o := sink.only(t)
				if o.Meta.Operation != "transform" || o.Meta.RequestedContract != level || o.Meta.DeliveredContract != level {
					t.Errorf("meta = %q requested %v delivered %v, want level %v", o.Meta.Operation, o.Meta.RequestedContract, o.Meta.DeliveredContract, level)
				}
				if o.Meta.Elapsed != 3*time.Second {
					t.Errorf("elapsed = %v, want the whole call, which slept 3s on the bubble's clock", o.Meta.Elapsed)
				}
			})
		})
	}
}

func TestRecordedRefusesBelowTheFloorBeforeRunningAndStillGivesARecord(t *testing.T) {
	registerStandIn(t)
	for _, level := range recordedLevels {
		for _, floor := range recordedLevels {
			t.Run(level.String()+" under a floor of "+floor.String(), func(t *testing.T) {
				sink := &recordingSink{}
				call, _ := Begin(ops.WithSink(clientContext(newScripted(), withFloor(floor)), sink), "summarize")
				ran := false
				value, err := Recorded(&call, level, func(context.Context) (string, error) {
					ran = true
					return "text", nil
				})
				o := sink.only(t)
				if level >= floor {
					if err != nil || !ran || value != "text" || o.Err != nil {
						t.Fatalf("at or above the floor: %q, %v, ran %v", value, err, ran)
					}
					return
				}
				if ran || value != "" {
					t.Errorf("a call below the floor ran (%v) or returned %q", ran, value)
				}
				if types.KindOf(err) != types.KindPolicyViolation || asConverted(t, err).op != "summarize" {
					t.Errorf("err = %v, kind %v", err, types.KindOf(err))
				}
				if o.Err != err || o.Meta.Operation != "summarize" || o.Meta.Attempts != 0 || o.Meta.RequestedContract != level {
					t.Errorf("record = %+v err %v", o.Meta, o.Err)
				}
			})
		}
	}
}

func TestRecordedWithNoRecorderUnderAFloorStillRefusesBeforeRunning(t *testing.T) {
	registerStandIn(t)
	call, _ := Begin(clientContext(newScripted(), withFloor(types.ContractSchemaConstrained)), "summarize")
	ran := false
	_, err := Recorded(&call, types.ContractPromptOnly, func(context.Context) (string, error) { ran = true; return "x", nil })
	if err == nil || ran || types.KindOf(err) != types.KindPolicyViolation {
		t.Fatalf("err = %v, ran = %v", err, ran)
	}
}

func TestRecordedConvertsTheFunctionsErrorAndGivesTheRecordTheRequestsItMade(t *testing.T) {
	registerStandIn(t)
	provider := newScripted(`{"category":"x"}`)
	sink := &recordingSink{}
	call, _ := Begin(ops.WithSink(clientContext(provider), sink), "extract")
	cause := errors.New("the decode failed")
	_, err := Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (int, error) {
		// Two requests through the call path, from the context the entry gave.
		for range 2 {
			if _, perr := ops.CallLLM(ctx, provider, "system", "user", types.OpOptions{Model: scriptedModel}); perr != nil {
				return 0, perr
			}
		}
		return 0, cause
	})
	if !errors.Is(err, cause) || asConverted(t, err).op != "extract" {
		t.Fatalf("err = %v", err)
	}
	o := sink.only(t)
	if o.Meta.Attempts != 2 || o.Meta.Usage.TotalTokens != 28 || o.Meta.Provider != scriptedName || o.Meta.Model != scriptedModel {
		t.Errorf("meta = attempts %d tokens %d provider %q model %q: the closure's requests did not reach the record",
			o.Meta.Attempts, o.Meta.Usage.TotalTokens, o.Meta.Provider, o.Meta.Model)
	}
	if o.Meta.DeliveredContract != types.ContractPromptOnly {
		t.Errorf("a failed call delivered %v", o.Meta.DeliveredContract)
	}
	if o.Err != err {
		t.Error("the outcome does not hold the returned error")
	}
}

func TestRecordedAddsOnlyTheClosureToTheRecordedEntry(t *testing.T) {
	// A floor with no recorder: the call is not plain, nothing is delivered, so
	// what is compared is the helper's own cost over the entry.
	ctx := clientContext(newScripted(), withFloor(types.ContractJSONWellFormed))
	body := func(context.Context) (int, error) { return 1, nil }
	direct := testing.AllocsPerRun(200, func() {
		fn := func(c context.Context) (int, error) { return body(c) }
		_, _ = ops.Recorded(ctx, "transform", types.ContractJSONWellFormed, fn)
	})
	bound := testing.AllocsPerRun(200, func() {
		call, _ := Begin(ctx, "transform")
		fn := func(c context.Context) (int, error) { return body(c) }
		_, _ = Recorded(&call, types.ContractJSONWellFormed, fn)
	})
	if bound != direct {
		t.Errorf("the helper allocates %v times and the recorded entry alone %v", bound, direct)
	}
	t.Logf("recorded entry alone %v allocations; Begin + Recorded + End %v", direct, bound)

	// With a recorder, End builds an outcome for it: what the recorder is owed.
	sink := &recordingSink{}
	withSink := ops.WithSink(ctx, sink)
	delivered := testing.AllocsPerRun(200, func() {
		call, _ := Begin(withSink, "transform")
		fn := func(c context.Context) (int, error) { return body(c) }
		_, _ = Recorded(&call, types.ContractJSONWellFormed, fn)
	})
	t.Logf("with one recorder, Begin + Recorded + End %v allocations", delivered)
}

// echoOp is a custom operation whose answer is refused by its own rule, so that
// the call fails after a request and holds a refused body.
func echoOp(reject func(string) error) ops.Op[string, string] {
	return ops.Op[string, string]{
		ID:        types.OperationID{Name: "callerEcho", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: ops.OutputContract[string]{
			Decode:     func(body string) (string, error) { return body, nil },
			Invariants: []func(string) error{reject},
		},
		Batch:       ops.BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) { return "echo", input },
	}
}

func TestARefusedBodyReachesOnlyTheSinksThatAskedForBodiesAtTheirOwnBound(t *testing.T) {
	registerStandIn(t)
	const answer = "REFUSED-ANSWER-0123456789-ABCDEFGHIJ"
	wantsLong := &recordingSink{bodies: true, maxBody: 1000}
	wantsShort := &recordingSink{bodies: true, maxBody: 10}
	wantsNone := &recordingSink{}
	provider := newScripted(answer)
	ctx := ops.WithSink(ops.WithSink(clientContext(provider, withClientSink(wantsNone)), wantsShort), wantsLong)

	call, _ := Begin(ctx, "op.run")
	_, err := Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (string, error) {
		value, _, runErr := ops.RunOp(ctx, echoOp(func(string) error { return errors.New("rejected") }), "x", types.OpOptions{Model: scriptedModel})
		return value, runErr
	})
	if err == nil {
		t.Fatal("the answer was not refused")
	}
	if strings.Contains(err.Error(), answer) {
		t.Fatalf("the error repeats the refused answer: %v", err)
	}
	if got := wantsLong.only(t).Body; got != answer {
		t.Errorf("long body = %q, want the whole answer", got)
	}
	if got := wantsShort.only(t).Body; got != answer[:10] {
		t.Errorf("short body = %q, want %q", got, answer[:10])
	}
	if got := wantsNone.only(t).Body; got != "" {
		t.Errorf("a sink that did not ask for bodies was given %q", got)
	}
}

func TestNoBodyIsCapturedWhenNoSinkAsksForOne(t *testing.T) {
	sink := &recordingSink{decisions: true}
	call, _ := Begin(ops.WithSink(clientContext(newScripted("REFUSED")), sink), "op.run")
	_, err := Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (string, error) {
		value, _, runErr := ops.RunOp(ctx, echoOp(func(string) error { return errors.New("rejected") }), "x", types.OpOptions{Model: scriptedModel})
		return value, runErr
	})
	if err == nil {
		t.Fatal("the answer was not refused")
	}
	if got := sink.only(t).Body; got != "" || call.state.bodies {
		t.Errorf("body %q captured, bodies %v, though no sink asked", got, call.state.bodies)
	}
}

// answeringDecider is a decision provider: it answers typed and reads no prompt.
type answeringDecider struct {
	answers []llm.DecisionAnswer
}

func (d *answeringDecider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Provider: "jev-fake", Model: "jev-1", DecisionAnswers: d.answers,
		Usage: types.TokenUsage{PromptTokens: 30, CompletionTokens: 4, TotalTokens: 34}}, nil
}
func (d *answeringDecider) Name() string                               { return "jev-fake" }
func (d *answeringDecider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (d *answeringDecider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func TestTheFactsOfWhoAnsweredADecisionReachTheOutcome(t *testing.T) {
	registerStandIn(t)
	yes := func(confidence float64) []llm.DecisionAnswer {
		return []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: confidence, ConfidenceReported: true}}
	}
	cases := []struct {
		name       string
		provider   func() llm.Provider
		answeredBy string
		typed      bool
		askedFirst string
		escalated  string
		firstConf  float64
	}{
		{"a generating provider answers", func() llm.Provider { return newScripted(`{"q1":true}`) }, scriptedName, false, "", "", 0},
		{"a decision provider answers typed", func() llm.Provider {
			return mw.Decisions(newScripted(`{"q1":false}`), &answeringDecider{answers: yes(0.95)}, mw.DecisionOptions{Floor: 0.5})
		}, "jev-fake", true, "", "", 0},
		{"a decision provider is below the floor and the generator answers", func() llm.Provider {
			return mw.Decisions(newScripted(`{"q1":true}`), &answeringDecider{answers: yes(0.4)}, mw.DecisionOptions{Floor: 0.9})
		}, scriptedName, false, "jev-fake", llm.DecisionEscalatedBelowFloor, 0.4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asks := &recordingSink{decisions: true}
			quiet := &recordingSink{}
			ctx := ops.WithSink(clientContext(tc.provider(), withClientSink(quiet)), asks)
			call, _ := Begin(ctx, "is")
			answer, err := Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (bool, error) {
				return ops.Is(ctx, "Please refund my order.", "Is the customer asking for a refund?", types.OpOptions{})
			})
			if err != nil || !answer {
				t.Fatalf("Is = %v, %v", answer, err)
			}
			for name, sink := range map[string]*recordingSink{"the sink that asked for decisions": asks, "the sink that did not": quiet} {
				o := sink.only(t)
				if o.AnsweredBy != tc.answeredBy || o.Typed != tc.typed || o.AskedFirst != tc.askedFirst || o.Escalated != tc.escalated {
					t.Errorf("%s: answered by %q typed %v asked first %q escalated %q; want %q %v %q %q",
						name, o.AnsweredBy, o.Typed, o.AskedFirst, o.Escalated, tc.answeredBy, tc.typed, tc.askedFirst, tc.escalated)
				}
				if tc.firstConf != 0 && (o.AskedFirstModelConfidence == nil || *o.AskedFirstModelConfidence != tc.firstConf) {
					t.Errorf("%s: asked-first confidence %v, want %v", name, o.AskedFirstModelConfidence, tc.firstConf)
				}
			}
			if got := len(asks.only(t).Asked); got != 1 {
				t.Errorf("the sink that asked for decisions was given %d questions, want 1", got)
			}
			if got := len(quiet.only(t).Asked); got != 0 {
				t.Errorf("the sink that did not was given %d questions", got)
			}
		})
	}
}

func TestADecisionThatFailedIsInTheOutcomeWithItsError(t *testing.T) {
	registerStandIn(t)
	sink := &recordingSink{decisions: true}
	call, _ := Begin(ops.WithSink(clientContext(newScripted(`not json at all`)), sink), "is")
	_, err := Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (bool, error) {
		return ops.Is(ctx, "x", "Is it?", types.OpOptions{})
	})
	if err == nil {
		t.Fatal("a refused answer was accepted")
	}
	o := sink.only(t)
	if o.Err != err || o.AnsweredBy != "" || o.Meta.Attempts < 1 {
		t.Errorf("outcome err %v answered by %q attempts %d", o.Err, o.AnsweredBy, o.Meta.Attempts)
	}
	if len(o.Asked) != 1 {
		t.Errorf("a failed decision lists its questions: %d", len(o.Asked))
	}
}

// openSummary opens a stream of the summary of text, from the context it is
// given, as an operation function does.
func openSummary(ctx context.Context) (*ops.TextStream, error) {
	opts := ops.NewSummarizeOptions()
	opts.CommonOptions.Context = ctx
	return ops.StreamSummarize("some text", opts)
}

func readAll(stream *ops.TextStream) (string, error) {
	var text strings.Builder
	var last error
	for delta, err := range stream.All() {
		text.WriteString(delta)
		if err != nil {
			last = err
		}
	}
	return text.String(), last
}

func TestStreamEndsTheCallWhenTheStreamEnds(t *testing.T) {
	registerStandIn(t)
	failure := types.Newf(types.KindProviderUnavailable, "stream", "the provider fell over")
	leaves := func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
		if !yield(llm.StreamChunk{Delta: "first"}, nil) {
			return
		}
		<-ctx.Done()
		yield(llm.StreamChunk{}, ctx.Err())
	}

	type scenario struct {
		name  string
		run   func(ctx context.Context, yield func(llm.StreamChunk, error) bool)
		drive func(t *testing.T, stream *ops.TextStream, cancel context.CancelFunc)
		check func(t *testing.T, o *ops.Outcome)
	}
	scenarios := []scenario{
		{"read to the end", streamCompletes("one ", "two"),
			func(t *testing.T, s *ops.TextStream, _ context.CancelFunc) {
				if text, err := readAll(s); err != nil || text != "one two" {
					t.Fatalf("read %q, %v", text, err)
				}
			},
			func(t *testing.T, o *ops.Outcome) {
				if o.Err != nil || o.Meta.Usage.TotalTokens != 12 || o.Meta.Provider != scriptedName || o.Meta.DeliveredContract != types.ContractPromptOnly {
					t.Errorf("outcome = err %v tokens %d provider %q", o.Err, o.Meta.Usage.TotalTokens, o.Meta.Provider)
				}
			}},
		{"text", streamCompletes("a", "b"),
			func(t *testing.T, s *ops.TextStream, _ context.CancelFunc) {
				if text, err := s.Text(); err != nil || text != "ab" {
					t.Fatalf("Text = %q, %v", text, err)
				}
			},
			func(t *testing.T, o *ops.Outcome) {
				if o.Err != nil || o.Meta.Attempts != 1 {
					t.Errorf("outcome = err %v attempts %d", o.Err, o.Meta.Attempts)
				}
			}},
		{"cancelled", leaves,
			func(t *testing.T, s *ops.TextStream, _ context.CancelFunc) {
				synctest.Wait()
				s.Cancel()
			},
			func(t *testing.T, o *ops.Outcome) {
				if !errors.Is(o.Err, context.Canceled) {
					t.Errorf("err = %v, want a cancellation", o.Err)
				}
			}},
		{"abandoned by its reader", leaves,
			func(t *testing.T, s *ops.TextStream, _ context.CancelFunc) {
				for range s.All() {
					break
				}
			},
			func(t *testing.T, o *ops.Outcome) {
				if !errors.Is(o.Err, context.Canceled) {
					t.Errorf("err = %v, want a cancellation", o.Err)
				}
			}},
		{"abandoned and its context ends", leaves,
			func(t *testing.T, s *ops.TextStream, cancel context.CancelFunc) {
				synctest.Wait()
				cancel()
			},
			func(t *testing.T, o *ops.Outcome) {
				if !errors.Is(o.Err, context.Canceled) {
					t.Errorf("err = %v, want a cancellation", o.Err)
				}
			}},
		{"failed", func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{Delta: "half "}, nil)
			yield(llm.StreamChunk{}, failure)
		},
			func(t *testing.T, s *ops.TextStream, _ context.CancelFunc) {
				if _, err := readAll(s); err == nil {
					t.Fatal("a failed stream read to a clean end")
				}
			},
			func(t *testing.T, o *ops.Outcome) {
				if !errors.Is(o.Err, failure) || types.KindOf(o.Err) != types.KindProviderUnavailable {
					t.Errorf("err = %v (kind %v)", o.Err, types.KindOf(o.Err))
				}
				if o.Meta.DeliveredContract != types.ContractPromptOnly {
					t.Errorf("a failed stream delivered %v", o.Meta.DeliveredContract)
				}
			}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// A fast provider may finish before Stream returns. Hold its work
				// until the pre-completion assertion has observed the open call.
				producerMayRun := make(chan struct{})
				provider := &streamingLLM{scriptedLLM: scriptedLLM{name: scriptedName}, run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
					<-producerMayRun
					sc.run(ctx, yield)
				}}
				sink := &recordingSink{}
				ctx, cancel := context.WithCancel(ops.WithSink(clientContext(provider), sink))
				defer cancel()
				call, err := Begin(ctx, "streamsummarize")
				if err != nil {
					t.Fatal(err)
				}
				stream, err := Stream(&call, types.ContractPromptOnly, openSummary)
				if err != nil {
					t.Fatalf("Stream: %v", err)
				}
				if len(sink.given()) != 0 {
					t.Fatal("the call ended before the stream did")
				}
				close(producerMayRun)
				sc.drive(t, stream, cancel)
				synctest.Wait()
				o := sink.only(t)
				if o.Meta.Operation != "streamsummarize" || o.Meta.RequestedContract != types.ContractPromptOnly {
					t.Errorf("meta = %q requested %v", o.Meta.Operation, o.Meta.RequestedContract)
				}
				if o.Err != nil {
					asConverted(t, o.Err)
				}
				sc.check(t, o)
			})
		})
	}
}

func TestStreamRefusedBelowTheFloorOrFailingToOpenEndsTheCallAtOnce(t *testing.T) {
	registerStandIn(t)
	t.Run("below the floor", func(t *testing.T) {
		sink := &recordingSink{}
		provider := &streamingLLM{scriptedLLM: scriptedLLM{name: scriptedName}, run: streamCompletes("x")}
		call, _ := Begin(ops.WithSink(clientContext(provider, withFloor(types.ContractJSONWellFormed)), sink), "streamrewrite")
		opened := false
		stream, err := Stream(&call, types.ContractPromptOnly, func(ctx context.Context) (*ops.TextStream, error) {
			opened = true
			return openSummary(ctx)
		})
		if stream != nil || opened || types.KindOf(err) != types.KindPolicyViolation {
			t.Fatalf("stream %v, opened %v, err %v", stream, opened, err)
		}
		o := sink.only(t)
		if o.Err != err || o.Meta.Operation != "streamrewrite" || o.Meta.RequestedContract != types.ContractPromptOnly || o.Meta.Attempts != 0 {
			t.Errorf("outcome = %+v err %v", o.Meta, o.Err)
		}
	})
	t.Run("the provider cannot stream", func(t *testing.T) {
		sink := &recordingSink{}
		call, _ := Begin(ops.WithSink(clientContext(newScripted()), sink), "streamsummarize")
		stream, err := Stream(&call, types.ContractPromptOnly, openSummary)
		if stream != nil || types.KindOf(err) != types.KindUnsupportedCapability || asConverted(t, err).op != "streamsummarize" {
			t.Fatalf("stream %v, err %v", stream, err)
		}
		if o := sink.only(t); o.Err != err {
			t.Errorf("the outcome holds %v", o.Err)
		}
	})
	t.Run("with no recorder the error is converted and nothing else happens", func(t *testing.T) {
		call, _ := Begin(clientContext(newScripted()), "streamsummarize")
		_, err := Stream(&call, types.ContractPromptOnly, openSummary)
		if asConverted(t, err).op != "streamsummarize" {
			t.Errorf("err = %v", err)
		}
	})
}

func TestStreamOfAPlainCallEndsWithNoRecorderAndAConvertedError(t *testing.T) {
	registerStandIn(t)
	synctest.Test(t, func(t *testing.T) {
		provider := &streamingLLM{scriptedLLM: scriptedLLM{name: scriptedName}, run: streamCompletes("a")}
		call, _ := Begin(clientContext(provider), "streamsummarize")
		if !call.Plain() {
			t.Fatal("the call is not plain")
		}
		stream, err := Stream(&call, types.ContractPromptOnly, openSummary)
		if err != nil {
			t.Fatal(err)
		}
		if text, err := readAll(stream); err != nil || text != "a" {
			t.Fatalf("read %q, %v", text, err)
		}
	})
}

func TestStreamDoesNotMakeTheCallersCallEscape(t *testing.T) {
	// The caller's Call must stay on its stack: Stream hands the end of the call
	// to a goroutine through a copy. With the plain call declared in a function
	// that also calls Stream, Begin and End on the plain path still allocate
	// nothing under a client.
	ctx := clientContext(newScripted())
	got := testing.AllocsPerRun(200, func() {
		call, _ := Begin(ctx, "streamsummarize")
		if !call.Plain() {
			if _, err := Stream(&call, types.ContractPromptOnly, openSummary); err != nil {
				return
			}
		}
		_ = call.End(nil, nil)
	})
	if got != 0 {
		t.Errorf("a call that may stream allocates %v times on the plain path", got)
	}
}

// panickingSink is a recorder whose Add panics, as a Handle function written by
// the caller can.
type panickingSink struct {
	recordingSink
	panics int
}

func (s *panickingSink) Add(ctx context.Context, o *ops.Outcome) {
	s.mu.Lock()
	s.panics++
	s.mu.Unlock()
	panic("the recorder's own code")
}

// TestAPanicInARecorderReachesTheCallerOfACallAndIsRecoveredForAStream states
// what the Handle comment of the root says. A call ends on the goroutine of its
// caller, so a panic in the recorder reaches them. A stream ends on a goroutine
// the library started, where a panic could not be caught by anyone and would end
// the program, so it is recovered and the stream still ends cleanly.
func TestAPanicInARecorderReachesTheCallerOfACallAndIsRecoveredForAStream(t *testing.T) {
	registerStandIn(t)
	t.Run("a call", func(t *testing.T) {
		provider := &scriptedLLM{name: scriptedName}
		sink := &panickingSink{}
		call, err := Begin(ops.WithSink(clientContext(provider), sink), "extract")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if recovered := recover(); recovered != "the recorder's own code" {
				t.Errorf("recovered %v, want the recorder's panic to reach the caller", recovered)
			}
		}()
		_ = call.End(nil, nil)
		t.Fatal("End returned")
	})
	t.Run("a stream", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &streamingLLM{scriptedLLM: scriptedLLM{name: scriptedName}, run: streamCompletes("one ", "two")}
			sink := &panickingSink{}
			call, err := Begin(ops.WithSink(clientContext(provider), sink), "streamsummarize")
			if err != nil {
				t.Fatal(err)
			}
			stream, err := Stream(&call, types.ContractPromptOnly, openSummary)
			if err != nil {
				t.Fatal(err)
			}
			text, err := readAll(stream)
			synctest.Wait()
			if err != nil || text != "one two" {
				t.Errorf("read %q, %v: the panic in the recorder reached the reader", text, err)
			}
			sink.mu.Lock()
			defer sink.mu.Unlock()
			if sink.panics != 1 {
				t.Errorf("the recorder was given the call %d times, want once", sink.panics)
			}
		})
	})
}

// TestAFloorSetOnTheContextIsHeldLikeOneOnTheClient reads a floor that was
// attached to the context (ops.WithContractPolicy) as the client's own is read:
// the call is not plain, and an answer delivered below it is refused before
// anything is sent.
func TestAFloorSetOnTheContextIsHeldLikeOneOnTheClient(t *testing.T) {
	registerStandIn(t)
	floorOnContext := func(level types.ContractLevel) context.Context {
		return ops.WithContractPolicy(clientContext(newScripted()), types.DataPolicy{
			AllowedProviders: []string{scriptedName}, AllowedRegions: []string{"eu"}, MinimumContract: level,
		})
	}
	for _, floor := range []types.ContractLevel{types.ContractJSONWellFormed, types.ContractSchemaConstrained, types.ContractFullyGoverned} {
		for _, level := range []types.ContractLevel{types.ContractPromptOnly, types.ContractJSONWellFormed, types.ContractFullyGoverned} {
			call, err := Begin(floorOnContext(floor), "summarize")
			if err != nil {
				t.Fatal(err)
			}
			if call.Plain() {
				t.Errorf("floor %v: a call under a floor on its context is plain", floor)
			}
			ran := false
			_, err = Recorded(&call, level, func(context.Context) (string, error) { ran = true; return "x", nil })
			if level < floor {
				if err == nil || ran || types.KindOf(err) != types.KindPolicyViolation {
					t.Errorf("floor %v level %v: err = %v, ran = %v, want a refusal before running", floor, level, err, ran)
				}
			} else if err != nil || !ran {
				t.Errorf("floor %v level %v: err = %v, ran = %v", floor, level, err, ran)
			}
		}
	}
	t.Run("a policy whose floor is the lowest level holds nothing", func(t *testing.T) {
		call, _ := Begin(floorOnContext(types.ContractPromptOnly), "summarize")
		if !call.Plain() {
			t.Error("a floor at the lowest level made the call not plain")
		}
	})
	t.Run("a call with no policy anywhere allocates nothing for the lookup", func(t *testing.T) {
		ctx := clientContext(newScripted())
		with := testing.AllocsPerRun(200, func() { _, _ = Begin(ctx, "summarize") })
		if with != 0 {
			t.Errorf("Begin allocates %v times on a client with no policy and a context with none", with)
		}
	})
}

// scopeKinds returns the error kind each finished operation scope reported, by
// operation name.
func (w *watcher) scopeKinds() map[string][]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	kinds := map[string][]string{}
	for _, result := range w.finished {
		if result.Scope == telemetry.ScopeOperation {
			kinds[result.Operation] = append(kinds[result.Operation], result.ErrorKind)
		}
	}
	return kinds
}

func TestACallEndedBeforeTheInternalCallIsOneOperationScopeToAnObserver(t *testing.T) {
	registerStandIn(t)
	refusals := []struct {
		name   string
		refuse func(c *Call) error
		kind   types.ErrorKind
	}{
		{"an option no call can use", func(c *Call) error {
			return c.End(nil, types.Newf(types.KindConfiguration, "classify", "MaxOutputTokens must not be negative"))
		}, types.KindConfiguration},
		{"an option, ended by Refuse", func(c *Call) error { return c.Refuse(types.KindConfiguration, "MaxWords must not be negative") }, types.KindConfiguration},
		{"a positional argument", func(c *Call) error { return c.Refuse(types.KindInvalidRequest, "categories is empty") }, types.KindInvalidRequest},
		{"an end with an error of no kind", func(c *Call) error { return c.End(nil, errors.New("plain")) }, types.KindUnknown},
		{"a policy refusal", func(c *Call) error { return c.End(nil, types.Newf(types.KindPolicyViolation, "classify", "floor")) }, types.KindPolicyViolation},
		{"a cancellation", func(c *Call) error { return c.End(nil, context.Canceled) }, types.KindCanceled},
		{"a deadline", func(c *Call) error { return c.End(nil, context.DeadlineExceeded) }, types.KindTimeout},
		{"a refusal with a nil meta that carries a meta", func(c *Call) error {
			return c.End(&types.Meta{}, types.Newf(types.KindConfiguration, "classify", "bad"))
		}, types.KindConfiguration},
	}
	states := []struct {
		name string
		ctx  func() context.Context
	}{
		{"observer only", func() context.Context { return clientContext(newScripted()) }},
		{"observer and recorder", func() context.Context { return ops.WithSink(clientContext(newScripted()), &recordingSink{}) }},
		{"observer and floor", func() context.Context {
			return clientContext(newScripted(), withFloor(types.ContractSchemaConstrained))
		}},
	}
	for _, st := range states {
		for _, tc := range refusals {
			t.Run(st.name+"/"+tc.name, func(t *testing.T) {
				w := observe(t)
				call, err := Begin(st.ctx(), "classify")
				if err != nil {
					t.Fatal(err)
				}
				err = tc.refuse(&call)
				if err == nil {
					t.Fatal("the call was not refused")
				}
				kinds := w.scopeKinds()
				if len(kinds) != 1 || len(kinds["classify"]) != 1 {
					t.Fatalf("the observer saw scopes %v, want one finish of classify", kinds)
				}
				if got, want := kinds["classify"][0], tc.kind.String(); got != want && !(tc.kind == types.KindUnknown && got == "") {
					t.Errorf("the scope finished with kind %q, want %q", got, want)
				}
			})
		}
	}
}

func TestEndingACallTwiceAddsOneOperationScope(t *testing.T) {
	registerStandIn(t)
	w := observe(t)
	call, _ := Begin(clientContext(newScripted()), "classify")
	call.Refuse(types.KindConfiguration, "bad")
	call.Refuse(types.KindConfiguration, "bad")
	call.End(nil, nil)
	if got := w.operations(); len(got) != 1 {
		t.Errorf("scopes = %v, want one", got)
	}
}

func TestACallThatRanTheInternalOperationDoesNotOpenAScopeAgainAtItsEnd(t *testing.T) {
	registerStandIn(t)
	for _, name := range []string{"transform", "recorded error"} {
		t.Run(name, func(t *testing.T) {
			w := observe(t)
			call, _ := Begin(clientContext(newScripted(`{"ok":true}`)), "transform")
			var runErr error
			if name == "recorded error" {
				runErr = types.Newf(types.KindTimeout, "transform", "slow")
			}
			_, err := Recorded(&call, types.ContractJSONWellFormed, func(context.Context) (int, error) { return 1, runErr })
			if (err != nil) != (runErr != nil) {
				t.Fatalf("err = %v", err)
			}
			if got := w.operations(); len(got) != 1 || got[0] != "transform" {
				t.Errorf("scopes = %v, want exactly one, transform", got)
			}
		})
	}
}

func TestACallThatRefusedBeforeTheScopeOfAnObserverRegisteredLaterOpensNone(t *testing.T) {
	call, _ := Begin(clientContext(newScripted(), withFloor(types.ContractSchemaConstrained)), "classify")
	w := observe(t)
	call.Refuse(types.KindConfiguration, "bad")
	if got := w.operations(); len(got) != 0 {
		t.Errorf("scopes = %v: an observer that was not there when the call began is told nothing of it", got)
	}
}

func TestAnEndedCallWithNoObserverAddsNothingAndTheObservedPlainPathStaysFree(t *testing.T) {
	provider := newScripted()
	ctx := clientContext(provider)
	if got := testing.AllocsPerRun(200, func() {
		call, _ := Begin(ctx, "classify")
		_ = call.End(nil, nil)
	}); got != 0 {
		t.Errorf("Begin and End on the plain path: %v allocations, want 0", got)
	}
	// A refusal on the plain path is the error's own allocations and nothing of
	// the scope: the observer-free refusal allocates what the converted error does.
	base := testing.AllocsPerRun(200, func() { _ = ConvertError("classify", types.Newf(types.KindConfiguration, "classify", "bad")) })
	refused := testing.AllocsPerRun(200, func() {
		call, _ := Begin(ctx, "classify")
		_ = call.End(nil, types.Newf(types.KindConfiguration, "classify", "bad"))
	})
	if refused > base {
		t.Errorf("a plain refusal allocates %v, the conversion alone %v", refused, base)
	}
}

func TestBeginRefusesAContextThatHasEndedAndGivesTheRecorderAndTheObserverTheCall(t *testing.T) {
	registerStandIn(t)
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	causes := []struct {
		name string
		ctx  context.Context
		kind types.ErrorKind
	}{
		{"cancelled", cancelled, types.KindCanceled},
		{"deadline passed", expired, types.KindTimeout},
	}
	states := []struct {
		name     string
		observed bool
		recorder bool
		floor    bool
	}{
		{"plain", false, false, false},
		{"recorder", false, true, false},
		{"observer", true, false, false},
		{"floor", false, false, true},
		{"recorder and observer", true, true, false},
	}
	for _, cause := range causes {
		for _, st := range states {
			t.Run(cause.name+"/"+st.name, func(t *testing.T) {
				var w *watcher
				if st.observed {
					w = observe(t)
				}
				sink := &recordingSink{}
				var settings []clientSetting
				if st.floor {
					settings = append(settings, withFloor(types.ContractSchemaConstrained))
				}
				ctx := clientContext(newScripted(), settings...)
				if st.recorder {
					ctx = ops.WithSink(ctx, sink)
				}
				// The ended context carries the client, as a caller's would.
				ctx = mergedValues{Context: cause.ctx, values: ctx}
				call, err := Begin(ctx, "diff")
				if err == nil {
					t.Fatalf("Begin accepted an ended context (call %+v)", call)
				}
				if types.KindOf(err) != cause.kind || asConverted(t, err).op != "diff" {
					t.Errorf("err = %v, kind %v, want %v of diff", err, types.KindOf(err), cause.kind)
				}
				if st.recorder {
					o := sink.only(t)
					if o.Meta.Operation != "diff" || o.Meta.Attempts != 0 || o.Err != err {
						t.Errorf("outcome = %+v, want the operation, no attempts and the returned error", o)
					}
				}
				if w != nil {
					if got := w.scopeKinds()["diff"]; len(got) != 1 || got[0] != cause.kind.String() {
						t.Errorf("scopes = %v, want one finish of diff with kind %v", w.scopeKinds(), cause.kind)
					}
				}
			})
		}
	}
}

// mergedValues is a context that ends as Context does and answers Value from
// values, so that a context that has ended can still carry the client.
type mergedValues struct {
	context.Context
	values context.Context
}

func (m mergedValues) Value(key any) any { return m.values.Value(key) }

func TestBeginOnALiveContextStaysFreeOfAllocationOnThePlainPath(t *testing.T) {
	ctx, cancel := context.WithCancel(clientContext(newScripted()))
	defer cancel()
	if got := testing.AllocsPerRun(200, func() {
		call, _ := Begin(ctx, "diff")
		_ = call.End(nil, nil)
	}); got != 0 {
		t.Errorf("Begin and End under a cancellable context that has not ended: %v allocations, want 0", got)
	}
}

package tests

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/mw"
	"github.com/monstercameron/schemaflux/telemetry"
)

// The call path, through the exported API of the old root and a provider that
// answers from a script: what the record of a call says when an answer was
// refused, what a failure that never reached an answer is called, and that the
// retries a provider asks for are still the ones a call makes.

type callPathScript struct {
	mu      sync.Mutex
	replies []schemaflux.CompletionResponse
	errs    []error
	retries int
	sent    int
}

func (p *callPathScript) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := p.sent
	p.sent++
	if n < len(p.errs) && p.errs[n] != nil {
		return schemaflux.CompletionResponse{}, p.errs[n]
	}
	reply := p.replies[min(n, len(p.replies)-1)]
	reply.Model = req.Model
	reply.Provider = "local"
	return reply, nil
}
func (p *callPathScript) Name() string                                      { return "local" }
func (p *callPathScript) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *callPathScript) RetryPolicy() (int, time.Duration)                 { return p.retries, time.Microsecond }
func (p *callPathScript) requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent
}

type callPathTarget struct {
	Name string `json:"name"`
}

func reply(body string, prompt, completion int) schemaflux.CompletionResponse {
	return schemaflux.CompletionResponse{
		Content: body, FinishReason: "stop",
		Usage: schemaflux.TokenUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion},
	}
}

func TestIntegrationARefusedAnswerAndARepairAreInTheRecordOfTheCall(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "call-path-model")
	provider := &callPathScript{retries: 2, replies: []schemaflux.CompletionResponse{
		reply("   ", 100, 0),              // refused for carrying nothing, and retried
		reply("not json at all", 200, 10), // refused as unreadable, and repaired
		reply(`{"name":"Ada"}`, 300, 20),  // the answer
	}}
	schemaflux.NewClient("").WithProviderInstance(provider)

	result, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Value.Name != "Ada" {
		t.Fatalf("value = %+v", result.Value)
	}
	meta := result.Meta
	if provider.requests() != 3 || meta.Attempts != 3 {
		t.Errorf("%d requests, Attempts %d, want 3 and 3", provider.requests(), meta.Attempts)
	}
	if meta.Usage.PromptTokens != 600 || meta.Usage.CompletionTokens != 30 {
		t.Errorf("usage = %+v: the refused answers were paid for and the record has to say so", meta.Usage)
	}
	if meta.Repairs != 1 {
		t.Errorf("Repairs = %d, want the one repair", meta.Repairs)
	}
	repairChecks := 0
	for _, check := range meta.Checks {
		if check.Name == "repair" && !check.Passed && check.Detail != "" {
			repairChecks++
		}
	}
	if repairChecks != 1 {
		t.Errorf("%d repair checks in %+v, want 1", repairChecks, meta.Checks)
	}
}

func TestIntegrationACallWithNoProviderFailsWithAKindAndItsSentinel(t *testing.T) {
	schemaflux.SetDefaultClient(nil)
	ops.SetDefaultProvider(nil)
	t.Cleanup(func() { schemaflux.SetDefaultClient(nil) })

	_, err := schemaflux.Summarize("some text", schemaflux.NewSummarizeOptions())
	if err == nil {
		t.Fatal("a call with no provider succeeded")
	}
	if !errors.Is(err, ops.ErrNoProvider) {
		t.Errorf("errors.Is(err, ErrNoProvider) is false: %v", err)
	}
	if kind := types.KindOf(err); kind != types.KindConfiguration {
		t.Errorf("kind = %v, want configuration", kind)
	}
}

func TestIntegrationAFailureInTransitLeavesWithAKindAndTheProvidersRetriesStand(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "call-path-model")
	provider := &callPathScript{retries: 2, errs: []error{
		fmt.Errorf("the connection dropped"), fmt.Errorf("the connection dropped"), fmt.Errorf("the connection dropped"),
	}, replies: []schemaflux.CompletionResponse{reply("x", 1, 1)}}
	schemaflux.NewClient("").WithProviderInstance(provider)

	_, err := schemaflux.Summarize("some text", schemaflux.NewSummarizeOptions())
	if err == nil {
		t.Fatal("three failures were an answer")
	}
	if provider.requests() != 3 {
		t.Errorf("%d requests, want the provider's own policy of one and two retries", provider.requests())
	}
	if types.KindOf(err) == types.KindUnknown {
		t.Errorf("the failure has no kind: %v", err)
	}
}

// finishCollector keeps the finish events of the calls made under a context
// that carries its mark, so that what other tests tell the process's observers
// is not read as this one's.
type finishCollector struct {
	mark any
	mu   sync.Mutex
	done []telemetry.OperationResult
}

func (c *finishCollector) OperationStarted(ctx context.Context, _ telemetry.OperationEvent) context.Context {
	return ctx
}

func (c *finishCollector) OperationFinished(ctx context.Context, result telemetry.OperationResult) {
	if ctx.Value(c.mark) == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done = append(c.done, result)
}

func (c *finishCollector) results() []telemetry.OperationResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]telemetry.OperationResult(nil), c.done...)
}

// An answer the provider's filter withheld is a refusal, and the call says so
// everywhere it is read: the error carries the mark that tells it from a data
// policy that forbids a route, and the finish of the request and of the
// operation, which an observer is given, name it "refused" and not "policy
// violation".
func TestIntegrationAFilteredAnswerIsARefusalInTheErrorAndInTheFinishEvents(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "call-path-model")
	type callMark struct{}
	provider := &callPathScript{retries: 0, replies: []schemaflux.CompletionResponse{{Content: "", FinishReason: "content_filter"}}}
	schemaflux.NewClient("").WithProviderInstance(provider)
	collector := &finishCollector{mark: callMark{}}
	t.Cleanup(telemetry.AddObserver(collector))

	ctx := context.WithValue(context.Background(), callMark{}, true)
	_, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace",
		schemaflux.ExtractOptions{CommonOptions: schemaflux.CommonOptions{Context: ctx}})
	if err == nil {
		t.Fatal("a withheld answer was an answer")
	}
	if types.KindOf(err) != types.KindPolicyViolation || types.NarrowingOf(err) != types.NarrowRefusal {
		t.Errorf("the error has kind %v and mark %d, want a policy violation marked as a refusal: %v", types.KindOf(err), types.NarrowingOf(err), err)
	}
	var sawRequest, sawOperation bool
	for _, result := range collector.results() {
		sawRequest = sawRequest || result.Scope == telemetry.ScopeRequest
		sawOperation = sawOperation || result.Scope == telemetry.ScopeOperation
		if result.ErrorKind != telemetry.ErrorKindRefused {
			t.Errorf("the %v finish names the failure %q, want %q", result.Scope, result.ErrorKind, telemetry.ErrorKindRefused)
		}
	}
	if !sawRequest || !sawOperation {
		t.Errorf("the observer saw a request finish %v and an operation finish %v, want both", sawRequest, sawOperation)
	}
}

// A provider whose first request does not answer until it is cancelled, and
// whose later ones answer at once: the slow tail that Hedge exists for.
type hedgeTarget struct {
	mu   sync.Mutex
	sent int
}

func (p *hedgeTarget) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	p.sent++
	first := p.sent == 1
	p.mu.Unlock()
	if first {
		<-ctx.Done()
		return schemaflux.CompletionResponse{}, ctx.Err()
	}
	return schemaflux.CompletionResponse{
		Content: `{"name":"Ada"}`, FinishReason: "stop", Model: req.Model, Provider: "local",
		Usage: schemaflux.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}
func (p *hedgeTarget) Name() string                                      { return "local" }
func (p *hedgeTarget) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *hedgeTarget) RetryPolicy() (int, time.Duration)                 { return 0, time.Microsecond }
func (p *hedgeTarget) requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent
}

// An answer a response cache served, and one a second copy of a slow request
// won, reach the envelope of the call as CacheHits and Hedged: the middleware
// marks the response, the call path folds the mark into the record, and the
// envelope says it.
func TestIntegrationACachedAnswerAndAHedgedOneReachTheEnvelope(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "call-path-model")

	t.Run("a cache hit", func(t *testing.T) {
		provider := &callPathScript{replies: []schemaflux.CompletionResponse{reply(`{"name":"Ada"}`, 100, 10)}}
		schemaflux.NewClient("").WithProviderInstance(mw.Chain(provider, mw.Cache()))

		first, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace", schemaflux.NewExtractOptions())
		if err != nil {
			t.Fatal(err)
		}
		second, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace", schemaflux.NewExtractOptions())
		if err != nil {
			t.Fatal(err)
		}
		if provider.requests() != 1 {
			t.Fatalf("the provider received %d requests, want 1: the second call was not served by the cache", provider.requests())
		}
		if first.Meta.CacheHits != 0 || first.Meta.Hedged {
			t.Errorf("the call that reached the provider says CacheHits %d, Hedged %v", first.Meta.CacheHits, first.Meta.Hedged)
		}
		if second.Meta.CacheHits != 1 {
			t.Errorf("the call the cache served says CacheHits %d, want 1", second.Meta.CacheHits)
		}
		if second.Meta.Hedged {
			t.Error("a cache hit says it was hedged")
		}
		if second.Value.Name != "Ada" {
			t.Errorf("the cached answer's value = %+v", second.Value)
		}
	})

	t.Run("a request whose second copy answered", func(t *testing.T) {
		provider := &hedgeTarget{}
		schemaflux.NewClient("").WithProviderInstance(mw.Chain(provider, mw.Hedge(5*time.Millisecond)))

		result, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace", schemaflux.NewExtractOptions())
		if err != nil {
			t.Fatal(err)
		}
		if provider.requests() != 2 {
			t.Fatalf("the provider received %d requests, want the request and its hedge", provider.requests())
		}
		if !result.Meta.Hedged {
			t.Error("a call that sent a second copy does not say Hedged")
		}
		if result.Meta.CacheHits != 0 {
			t.Errorf("a hedged call says CacheHits %d", result.Meta.CacheHits)
		}
	})

	t.Run("a request answered before the delay is not hedged", func(t *testing.T) {
		provider := &callPathScript{replies: []schemaflux.CompletionResponse{reply(`{"name":"Ada"}`, 100, 10)}}
		schemaflux.NewClient("").WithProviderInstance(mw.Chain(provider, mw.Hedge(time.Hour)))

		result, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace", schemaflux.NewExtractOptions())
		if err != nil {
			t.Fatal(err)
		}
		if provider.requests() != 1 || result.Meta.Hedged {
			t.Errorf("%d requests, Hedged %v; want one request and no hedge", provider.requests(), result.Meta.Hedged)
		}
	})

	t.Run("a cache in front of a hedge reports both across two calls", func(t *testing.T) {
		provider := &hedgeTarget{}
		schemaflux.NewClient("").WithProviderInstance(mw.Chain(provider, mw.Cache(), mw.Hedge(5*time.Millisecond)))

		first, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace", schemaflux.NewExtractOptions())
		if err != nil {
			t.Fatal(err)
		}
		second, err := schemaflux.ExtractResult[callPathTarget]("Ada Lovelace", schemaflux.NewExtractOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !first.Meta.Hedged || first.Meta.CacheHits != 0 {
			t.Errorf("the first call says Hedged %v, CacheHits %d; want hedged and no hit", first.Meta.Hedged, first.Meta.CacheHits)
		}
		if second.Meta.CacheHits != 1 {
			t.Errorf("the second call says CacheHits %d, want 1", second.Meta.CacheHits)
		}
		if second.Meta.Hedged {
			t.Errorf("the second call says Hedged: a cache hit sent no copy of anything, and cacheHitResponse clears the mark")
		}
		if provider.requests() != 2 {
			t.Errorf("the provider received %d requests, want the first call's request and its hedge", provider.requests())
		}
	})
}

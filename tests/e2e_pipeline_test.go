package tests

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// End-to-end: several operations chained through the PUBLIC API, the way a
// caller actually uses this library.
//
// Forty-two files in this suite tested one operation each. Not one ran a second
// operation on the first one's output, and the library's whole premise is that
// you can: lineage across stages (TC-003), one budget spanning a pipeline
// (IN-004), a contract level that survives the chain (TC-004). Every mechanism
// built for multi-step work was covered only where it was implemented, never
// where it is used.
//
// These are deliberately black-box. They import the root package the way a
// consumer does, thread results from one call into the next by hand — because
// that is what a caller has to do, nothing infers it — and assert on the
// envelope rather than on prompts.

type invoiceDoc struct {
	Number string `json:"number"`
	Vendor string `json:"vendor"`
}

type invoiceSummary struct {
	Headline string `json:"headline"`
}

// stagedProvider answers each call with the next scripted body, so a pipeline's
// stages can be given different answers and a test can tell which stage a
// failure came from.
type stagedProvider struct {
	mu      sync.Mutex
	bodies  []string
	calls   int
	failAt  int // 1-based; 0 means never fail
	lastErr error
}

func (p *stagedProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++

	if p.failAt > 0 && p.calls == p.failAt {
		return schemaflux.CompletionResponse{}, errStageFailed
	}

	index := p.calls - 1
	if index >= len(p.bodies) {
		index = len(p.bodies) - 1
	}
	return schemaflux.CompletionResponse{
		Content:      p.bodies[index],
		Provider:     "local",
		Model:        req.Model,
		FinishReason: "stop",
	}, nil
}

func (p *stagedProvider) Name() string                                      { return "local" }
func (p *stagedProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *stagedProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func (p *stagedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

var errStageFailed = errors.New("stage provider failed")

// A two-stage pipeline resolves back to its first stage.
//
// Nothing threads lineage automatically — the caller passes the previous
// result's ID into the next call's ParentResultIDs — and that is deliberate:
// only the caller knows which results are actually related. What this asserts is
// that the mechanism WORKS when used, which had never been exercised outside the
// package that implements it.
func TestAPipelineCarriesLineageAcrossStages(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &stagedProvider{bodies: []string{
		`{"number":"INV-4417","vendor":"Northwind"}`,
		`{"headline":"Northwind invoice INV-4417"}`,
	}}
	ctx := ops.WithProvider(context.Background(), provider)

	first := schemaflux.NewExtractOptions()
	first.CommonOptions = first.CommonOptions.WithContext(ctx)
	stageOne, err := schemaflux.ExtractResult[invoiceDoc]("the raw document", first)
	if err != nil {
		t.Fatalf("stage one: %v", err)
	}

	parentID := stageOne.Meta.Provenance.ResultID
	if parentID == "" {
		t.Fatal("stage one produced no result ID, so nothing downstream can reference it")
	}

	second := schemaflux.NewExtractOptions()
	second.CommonOptions = second.CommonOptions.WithContext(ctx)
	second.OpOptions.ParentResultIDs = []string{parentID}
	stageTwo, err := schemaflux.ExtractResult[invoiceSummary](stageOne.Value, second)
	if err != nil {
		t.Fatalf("stage two: %v", err)
	}

	parents := stageTwo.Meta.Provenance.ParentResultIDs
	if len(parents) != 1 || parents[0] != parentID {
		t.Fatalf("stage two's parents = %v, want [%s]; the chain does not resolve", parents, parentID)
	}
	if stageTwo.Meta.Provenance.ResultID == parentID {
		t.Error("both stages reported the same result ID; the chain is one node reported twice")
	}
	if provider.callCount() != 2 {
		t.Errorf("the provider saw %d calls; a two-stage pipeline should make two", provider.callCount())
	}
}

// One budget spans the whole pipeline, and exhausting it stops the NEXT stage
// rather than being noticed afterwards.
//
// This is the property a per-client budget exists for and the one a single-call
// test cannot show: a budget that only refuses the call that exceeded it has
// already spent the money.
func TestAClientBudgetSpansEveryStageOfAPipeline(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &stagedProvider{bodies: []string{
		`{"number":"INV-1","vendor":"A"}`,
		`{"number":"INV-2","vendor":"B"}`,
	}}
	client := schemaflux.NewClient("key").WithProviderInstance(provider).WithBudget(1.00)
	ctx := client.Context(context.Background())

	run := func() error {
		opts := schemaflux.NewExtractOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		_, err := schemaflux.ExtractResult[invoiceDoc]("a document", opts)
		return err
	}

	if err := run(); err != nil {
		t.Fatalf("the first stage was refused with a fresh budget: %v", err)
	}
	afterFirst := provider.callCount()

	// Exhaust the client's ledger the way a real priced call would.
	if budget := ops.ExecBudget(ctx); budget != nil {
		budget.Record(1.00, true)
	} else {
		t.Fatal("the client carried no budget into its context")
	}

	if err := run(); err == nil {
		t.Error("a stage ran after the client's budget was exhausted")
	}
	if provider.callCount() != afterFirst {
		t.Errorf("the provider was contacted %d more time(s) after the budget was exhausted; the ceiling is checked after the request, not before it",
			provider.callCount()-afterFirst)
	}
}

// A failure in a later stage must not damage what earlier stages produced. A
// caller holding stage one's result should still be able to use it.
func TestAFailureLateInAPipelineLeavesEarlierResultsIntact(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &stagedProvider{
		bodies: []string{`{"number":"INV-4417","vendor":"Northwind"}`},
		failAt: 2, // the second call fails
	}
	ctx := ops.WithProvider(context.Background(), provider)

	first := schemaflux.NewExtractOptions()
	first.CommonOptions = first.CommonOptions.WithContext(ctx)
	stageOne, err := schemaflux.ExtractResult[invoiceDoc]("the raw document", first)
	if err != nil {
		t.Fatalf("stage one: %v", err)
	}
	if stageOne.Value.Number != "INV-4417" {
		t.Fatalf("stage one value = %+v", stageOne.Value)
	}

	second := schemaflux.NewExtractOptions()
	second.CommonOptions = second.CommonOptions.WithContext(ctx)
	second.OpOptions.ParentResultIDs = []string{stageOne.Meta.Provenance.ResultID}
	if _, err := schemaflux.ExtractResult[invoiceSummary](stageOne.Value, second); err == nil {
		t.Fatal("stage two succeeded against a provider scripted to fail")
	}

	// The point: stage one's result is untouched by stage two's failure.
	if stageOne.Value.Number != "INV-4417" || stageOne.Value.Vendor != "Northwind" {
		t.Errorf("stage one's value changed after stage two failed: %+v", stageOne.Value)
	}
	if stageOne.Meta.Provenance.ResultID == "" {
		t.Error("stage one's provenance was cleared by a later failure")
	}
}

// Two clients running pipelines at the same time must not see each other's
// providers. Single-call isolation is already covered; this is the version that
// runs several calls per client concurrently, which is where a shared global
// would actually show up.
func TestTwoClientsRunConcurrentPipelinesWithoutInterference(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	alpha := testfixtures.NewNamed("alpha")
	beta := testfixtures.NewNamed("beta")

	clientA := schemaflux.NewClient("key-a").WithProviderInstance(alpha)
	clientB := schemaflux.NewClient("key-b").WithProviderInstance(beta)

	const rounds = 25
	var wg sync.WaitGroup
	errs := make(chan string, rounds*2)

	pipeline := func(client *schemaflux.Client, want string) {
		defer wg.Done()
		ctx := client.Context(context.Background())
		for i := 0; i < rounds; i++ {
			got, err := schemaflux.Format("payload", "uppercase", schemaflux.OpOptions{Context: ctx})
			if err != nil {
				errs <- want + ": " + err.Error()
				return
			}
			if !strings.Contains(got, want) {
				errs <- "expected an answer from " + want + ", got " + got
				return
			}
		}
	}

	wg.Add(2)
	go pipeline(clientA, "alpha")
	go pipeline(clientB, "beta")
	wg.Wait()
	close(errs)

	for message := range errs {
		t.Error(message)
	}
}

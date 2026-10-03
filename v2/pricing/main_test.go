package pricing_test

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
	inner "github.com/monstercameron/schemaflux/pricing"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/pricing"
)

// The ledger, the limits and the price table belong to the process, so the
// tests that read or change the ledger or the limits do two things. They call
// isolateLedger first, which gives them an empty ledger and no limits and
// restores both when they end; and they do not call t.Parallel, so no other test
// reads the ledger while they run. The tests of the price table change it only
// by registering models under names that are unique to the run (uniqueModel),
// since a price has no undo, and they run in parallel.
//
// TestMain holds the first part to account: when every test has run, in
// whatever order -shuffle chose, the ledger must be empty again and no limit may
// be in force, or the run fails and says so.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if entries := pricing.Entries(time.Time{}, nil); len(entries) != 0 {
			fmt.Fprintf(os.Stderr, "FAIL: a test left %d entries in the process ledger\n", len(entries))
			code = 1
		}
		if err := inner.CheckBudget(); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: a test left an enforced limit in force: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

// isolateLedger gives the test an empty ledger, no limits and the default
// history limit, and restores that state when the test ends. It fails the test
// if it finds the ledger not empty at the start, which means an earlier test did
// not clean up.
func isolateLedger(t *testing.T) {
	t.Helper()
	if n := len(pricing.Entries(time.Time{}, nil)); n != 0 {
		t.Fatalf("the ledger held %d entries when the test began: an earlier test did not restore it", n)
	}
	restore := setLimits(t, pricing.Limits{})
	t.Cleanup(func() {
		restore()
		pricing.SetHistoryLimit(0)
		pricing.Reset()
	})
}

// modelCounter makes model names unique within the run, so that two tests, and
// two runs of one test under -count, never share a registered price.
var modelCounter atomic.Int64

// uniqueModel is a model name no other test, and no other run of this one, uses.
func uniqueModel(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("pricing-test-model-%d", modelCounter.Add(1))
}

// call is a call to be put in the ledger, as the library's call path puts one.
type call struct {
	id, correlation, operation, model, provider string
	usage                                       schemaflux.Usage
	cost                                        float64
	unpriced                                    bool
	at                                          time.Time
	tags                                        map[string]any
}

// seed records calls in the ledger through the package that holds it, the way
// the call path does.
func seed(calls ...call) {
	for _, c := range calls {
		if c.at.IsZero() {
			c.at = time.Now()
		}
		if c.operation == "" {
			c.operation = "extract"
		}
		if c.model == "" {
			c.model = "gpt-5"
		}
		if c.provider == "" {
			c.provider = "openai"
		}
		cost := &types.CostInfo{TotalCost: c.cost, PromptCost: c.cost / 2, CompletionCost: c.cost / 2, Currency: "USD", Priced: !c.unpriced, PricingSource: c.model}
		inner.TrackCost(cost, &types.ResultMetadata{
			RequestID:     c.id,
			CorrelationID: c.correlation,
			Operation:     c.operation,
			Model:         c.model,
			Provider:      c.provider,
			EndTime:       c.at,
			Custom:        c.tags,
			TokenUsage: &types.TokenUsage{
				PromptTokens: c.usage.Input, CompletionTokens: c.usage.Output, TotalTokens: c.usage.Total,
				CachedTokens: c.usage.Cached, CacheWriteTokens: c.usage.CacheWrite, ReasoningTokens: c.usage.Reasoning,
			},
		})
	}
}

// checkBudget asks the package that holds the limits whether a call may go.
func checkBudget() error { return inner.CheckBudget() }

// setBudget is SetLimits for limits the test knows to be good.
func setLimits(t testing.TB, limits pricing.Limits) (restore func()) {
	t.Helper()
	restore, err := pricing.SetLimits(limits)
	if err != nil {
		t.Fatalf("SetLimits(%+v): %v", limits, err)
	}
	return restore
}

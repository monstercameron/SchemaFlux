package ops

import (
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// requestMeta is the envelope of one request that was made and priced, for the
// cases below to adjust.
func requestMeta(model string, tokens int, cost float64, quality types.PricingQuality) types.Meta {
	return types.Meta{
		Provider: "p", Model: model, RequestedModel: model,
		Attempts: 1,
		Usage:    types.TokenUsage{PromptTokens: tokens, CompletionTokens: tokens / 2, TotalTokens: tokens + tokens/2},
		Cost:     types.CostInfo{Priced: true, TotalCost: cost, PromptCost: cost / 2, CompletionCost: cost / 2, Currency: "USD", PricingSource: "table", Quality: quality},
	}
}

// What a call made of many calls cost is the sum of what they cost, by the rules
// the requests inside one call are summed by.
func TestSumMetasAddsTheEnvelopesOfTheRequestsOfOneCall(t *testing.T) {
	exact := types.PricingExact
	cases := []struct {
		name  string
		metas []types.Meta
		check func(t *testing.T, total types.Meta)
	}{
		{"no requests is the operation's name and nothing else", nil, func(t *testing.T, total types.Meta) {
			if total.Operation != "op@v1" || total.Attempts != 0 || total.Cost.Priced || total.Provider != "" {
				t.Errorf("total = %+v", total)
			}
		}},
		{"one request is itself", []types.Meta{requestMeta("m", 100, 0.5, exact)}, func(t *testing.T, total types.Meta) {
			if total.Attempts != 1 || total.Usage.TotalTokens != 150 || !total.Cost.Priced || total.Cost.TotalCost != 0.5 || total.Provider != "p" || total.Model != "m" {
				t.Errorf("total = %+v", total)
			}
		}},
		{"usage, attempts and cost are added", []types.Meta{requestMeta("m", 100, 0.5, exact), requestMeta("m", 200, 0.25, exact)}, func(t *testing.T, total types.Meta) {
			if total.Attempts != 2 || total.Usage.PromptTokens != 300 || total.Usage.CompletionTokens != 150 || total.Usage.TotalTokens != 450 {
				t.Errorf("usage or attempts: %+v", total)
			}
			if !total.Cost.Priced || total.Cost.TotalCost != 0.75 || total.Cost.PromptCost != 0.375 || total.Cost.Currency != "USD" || total.Cost.PricingSource != "table" || total.Cost.Quality != exact {
				t.Errorf("cost: %+v", total.Cost)
			}
		}},
		{"one unpriced request makes the whole figure unpriced", []types.Meta{requestMeta("m", 100, 0.5, exact), {Attempts: 1, Usage: types.TokenUsage{TotalTokens: 10}}}, func(t *testing.T, total types.Meta) {
			if total.Cost.Priced || total.Cost.PricingSource != "" || total.Cost.Quality != types.PricingUnknown {
				t.Errorf("cost = %+v, want unpriced and of unknown quality", total.Cost)
			}
			if total.Usage.TotalTokens != 160 {
				t.Errorf("TotalTokens = %d, want the usage added anyway", total.Usage.TotalTokens)
			}
		}},
		{"a failed request that reported nothing has no vote on the cost", []types.Meta{requestMeta("m", 100, 0.5, exact), {Attempts: 2, Retries: 1}}, func(t *testing.T, total types.Meta) {
			if !total.Cost.Priced || total.Cost.TotalCost != 0.5 || total.Cost.Quality != exact || total.Attempts != 3 {
				t.Errorf("total = %+v", total)
			}
		}},
		{"only failed requests leave the cost unpriced", []types.Meta{{Attempts: 1}, {Attempts: 3, Retries: 2}}, func(t *testing.T, total types.Meta) {
			if total.Cost.Priced || total.Cost.Quality != types.PricingUnknown || total.Attempts != 4 {
				t.Errorf("total = %+v", total)
			}
		}},
		{"a refusal that made no attempt has no vote on the cost", []types.Meta{requestMeta("m", 100, 0.5, exact), {Operation: "x"}}, func(t *testing.T, total types.Meta) {
			if !total.Cost.Priced || total.Cost.TotalCost != 0.5 {
				t.Errorf("cost = %+v", total.Cost)
			}
		}},
		{"a refusal that made no attempt has no vote on the model", []types.Meta{{Operation: "x"}, requestMeta("m", 100, 0.5, exact)}, func(t *testing.T, total types.Meta) {
			if total.Model != "m" || total.Provider != "p" {
				t.Errorf("model = %q, provider = %q", total.Model, total.Provider)
			}
		}},
		{"an estimate among exact figures makes the total an estimate", []types.Meta{requestMeta("m", 100, 0.5, exact), requestMeta("m", 100, 0.5, types.PricingEstimated)}, func(t *testing.T, total types.Meta) {
			if total.Cost.Quality != types.PricingEstimated {
				t.Errorf("quality = %q", total.Cost.Quality)
			}
		}},
		{"free beside exact is exact", []types.Meta{requestMeta("m", 100, 0, types.PricingFree), requestMeta("m", 100, 0.5, exact)}, func(t *testing.T, total types.Meta) {
			if total.Cost.Quality != exact {
				t.Errorf("quality = %q", total.Cost.Quality)
			}
		}},
		{"only free requests are free", []types.Meta{requestMeta("m", 100, 0, types.PricingFree), requestMeta("m", 100, 0, types.PricingFree)}, func(t *testing.T, total types.Meta) {
			if total.Cost.Quality != types.PricingFree || !total.Cost.Priced {
				t.Errorf("cost = %+v", total.Cost)
			}
		}},
		{"requests that disagree on the model keep none", []types.Meta{requestMeta("a", 100, 0.5, exact), requestMeta("b", 100, 0.5, exact)}, func(t *testing.T, total types.Meta) {
			if total.Model != "" || total.RequestedModel != "" || total.Provider != "p" {
				t.Errorf("model = %q, requested = %q, provider = %q", total.Model, total.RequestedModel, total.Provider)
			}
		}},
		{"retries, cache hits, hedging and the service tier", []types.Meta{
			{Attempts: 3, Retries: 2, CacheHits: 1, ServiceTier: "default"},
			{Attempts: 1, Hedged: true, CacheHits: 2, ServiceTier: ""},
			{Attempts: 2, Retries: 1, Repairs: 1, ServiceTier: "flex"},
		}, func(t *testing.T, total types.Meta) {
			if total.Attempts != 6 || total.Retries != 3 || total.CacheHits != 3 || total.Repairs != 1 || !total.Hedged || total.ServiceTier != "flex" {
				t.Errorf("total = %+v", total)
			}
		}},
		{"the cache hit ratio is worked out again from the sums", []types.Meta{
			{Attempts: 1, Usage: types.TokenUsage{PromptTokens: 100, CachedTokens: 100}},
			{Attempts: 1, Usage: types.TokenUsage{PromptTokens: 300, CachedTokens: 0}},
		}, func(t *testing.T, total types.Meta) {
			if total.CacheHitRatio != 0.25 {
				t.Errorf("CacheHitRatio = %v, want 0.25", total.CacheHitRatio)
			}
		}},
		{"timings are added and elapsed is not", []types.Meta{
			{Attempts: 1, Elapsed: time.Second, Timing: types.Timing{Calls: 1, Network: 10 * time.Millisecond, Wait: 8 * time.Millisecond}},
			{Attempts: 1, Elapsed: time.Second, Timing: types.Timing{Calls: 1, Network: 30 * time.Millisecond, Wait: 20 * time.Millisecond}},
		}, func(t *testing.T, total types.Meta) {
			if total.Timing.Calls != 2 || total.Timing.Network != 40*time.Millisecond || total.Timing.Wait != 28*time.Millisecond {
				t.Errorf("timing = %+v", total.Timing)
			}
			if total.Elapsed != 0 {
				t.Errorf("Elapsed = %v, want it left for the caller", total.Elapsed)
			}
		}},
		{"what belongs to one request is dropped", []types.Meta{
			{Attempts: 1, RequestID: "r1", CorrelationID: "c1", Strategy: "s", RequestedContract: types.ContractSchemaConstrained, DeliveredContract: types.ContractSchemaConstrained, Checks: []types.Check{{Name: "x"}}},
		}, func(t *testing.T, total types.Meta) {
			if total.RequestID != "" || total.CorrelationID != "" || total.Strategy != "" || total.RequestedContract != 0 || total.DeliveredContract != 0 || len(total.Checks) != 0 {
				t.Errorf("total = %+v", total)
			}
		}},
		{"the requested tier is kept only when every request agrees", []types.Meta{
			{Attempts: 1, RequestedTier: types.Fast}, {Attempts: 1, RequestedTier: types.Fast},
		}, func(t *testing.T, total types.Meta) {
			if total.RequestedTier != types.Fast {
				t.Errorf("tier = %v", total.RequestedTier)
			}
		}},
		{"requests that asked for different tiers keep none", []types.Meta{
			{Attempts: 1, RequestedTier: types.Fast}, {Attempts: 1, RequestedTier: types.Smart},
		}, func(t *testing.T, total types.Meta) {
			if total.RequestedTier != types.TierUnset {
				t.Errorf("tier = %v", total.RequestedTier)
			}
		}},
		{"model drift in any request is drift", []types.Meta{
			{Attempts: 1}, {Attempts: 1, ModelDrifted: true}, {Attempts: 1, ModelDriftUnknown: true},
		}, func(t *testing.T, total types.Meta) {
			if !total.ModelDrifted || !total.ModelDriftUnknown {
				t.Errorf("total = %+v", total)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metas := append([]types.Meta(nil), tc.metas...)
			total := sumMetas("op@v1", metas...)
			tc.check(t, total)
			for i := range metas {
				if metas[i].Attempts != tc.metas[i].Attempts {
					t.Fatal("the summing changed its input")
				}
			}
		})
	}
}

// The order the requests are added in does not change the total.
func TestSumMetasDoesNotDependOnTheOrder(t *testing.T) {
	a := requestMeta("m", 100, 0.5, types.PricingExact)
	b := requestMeta("m", 200, 0.25, types.PricingEstimated)
	c := types.Meta{Attempts: 2, Retries: 1, CacheHits: 1}
	for _, order := range [][]types.Meta{{a, b, c}, {c, b, a}, {b, a, c}, {c, a, b}} {
		total := sumMetas("op", order...)
		if total.Attempts != 4 || total.Usage.TotalTokens != 450 || total.Cost.TotalCost != 0.75 || total.Cost.Quality != types.PricingEstimated || !total.Cost.Priced || total.Retries != 1 {
			t.Errorf("total = %+v", total)
		}
	}
}

// spend is added to from the goroutines of a bounded pool, and keeps of each
// envelope only what is summed.
func TestSpendIsSummedAcrossGoroutinesAndKeepsNoChecksOrLineage(t *testing.T) {
	var spent spend
	var wait sync.WaitGroup
	for i := 0; i < 200; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			meta := requestMeta("m", 10, 0.01, types.PricingExact)
			meta.Checks = []types.Check{{Name: "repair", Detail: "a failure"}}
			meta.ModelClaims = map[string]any{"k": "v"}
			meta.Provenance = types.Provenance{ResultID: "r"}
			spent.add(meta)
		}()
	}
	wait.Wait()
	total := spent.total("op")
	if total.Attempts != 200 || total.Usage.TotalTokens != 3000 || total.Cost.TotalCost < 1.999 || total.Cost.TotalCost > 2.001 {
		t.Errorf("total = %+v", total)
	}
	for _, kept := range spent.metas {
		if len(kept.Checks) != 0 || kept.ModelClaims != nil || kept.Provenance.ResultID != "" {
			t.Fatalf("a kept envelope still holds %+v", kept)
		}
	}
}

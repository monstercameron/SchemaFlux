package ops

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// pricedScript answers each request with the next of its responses, as the
// provider named "openai" with a model that has a price, so that what a call
// cost can be compared with the ledgers.
type pricedScript struct {
	mu        sync.Mutex
	responses []llm.CompletionResponse
	retries   int
	sent      int
}

func (p *pricedScript) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := min(p.sent, len(p.responses)-1)
	p.sent++
	resp := p.responses[index]
	if resp.Model == "" {
		resp.Model = req.Model
	}
	if resp.Provider == "" {
		resp.Provider = "openai"
	}
	return resp, nil
}
func (p *pricedScript) Name() string                               { return "openai" }
func (p *pricedScript) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *pricedScript) RetryPolicy() (int, time.Duration)          { return p.retries, time.Microsecond }

func usageOf(prompt, completion int) types.TokenUsage {
	return types.TokenUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
}

func recordedExtract(t *testing.T, provider llm.Provider, cfg ExecConfig) (types.Result[recordedAnswer], error) {
	t.Helper()
	cfg.Provider = provider
	if cfg.Model == "" {
		cfg.Model = "gpt-5-mini"
	}
	withDefaultExec(t, &cfg)
	return Recorded(context.Background(), "extract", types.ContractSchemaConstrained, extractRecordedResult)
}

func TestRefusedAttemptIsInTheEnvelope(t *testing.T) {
	pricing.ResetCostTracking()
	t.Cleanup(pricing.ResetCostTracking)
	refused, good := usageOf(100, 10), usageOf(200, 20)
	ledger := NewClientBudget(0)
	provider := &pricedScript{retries: 2, responses: []llm.CompletionResponse{
		{Content: "  ", FinishReason: "stop", Usage: refused},
		{Content: `{"ok":true}`, FinishReason: "stop", Usage: good},
	}}

	result, err := recordedExtract(t, provider, ExecConfig{Budget: ledger})
	if err != nil {
		t.Fatal(err)
	}
	meta := result.Meta
	if meta.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", meta.Attempts)
	}
	if meta.Retries != 1 {
		t.Errorf("Retries = %d, want 1", meta.Retries)
	}
	if want := (types.TokenUsage{PromptTokens: 300, CompletionTokens: 30, TotalTokens: 330}); meta.Usage != want {
		t.Errorf("usage = %+v, want the sum of both attempts %+v", meta.Usage, want)
	}

	// What the envelope says the call cost is what both ledgers charged.
	costRefused := pricing.CalculateCost(&refused, "gpt-5-mini", "openai")
	costGood := pricing.CalculateCost(&good, "gpt-5-mini", "openai")
	wantCost := costRefused.TotalCost + costGood.TotalCost
	if diff := meta.Cost.TotalCost - wantCost; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("Meta.Cost = %v, want %v", meta.Cost.TotalCost, wantCost)
	}
	if processCost := pricing.GetTotalCost(time.Time{}, nil); processCost-meta.Cost.TotalCost > 1e-12 || meta.Cost.TotalCost-processCost > 1e-12 {
		t.Errorf("process ledger = %v, Meta.Cost = %v: they should agree", processCost, meta.Cost.TotalCost)
	}
	if spent, _ := ledger.Spent(); spent-meta.Cost.TotalCost > 1e-12 || meta.Cost.TotalCost-spent > 1e-12 {
		t.Errorf("client ledger = %v, Meta.Cost = %v: they should agree", spent, meta.Cost.TotalCost)
	}
	if meta.Provider != "openai" || meta.Model != "gpt-5-mini" {
		t.Errorf("identity %q %q comes from the answer that was accepted", meta.Provider, meta.Model)
	}
}

func TestFailedAfterRefusalCarriesItsUsage(t *testing.T) {
	provider := &pricedScript{retries: 1, responses: []llm.CompletionResponse{
		{Content: " ", FinishReason: "stop", Usage: usageOf(50, 5)},
	}}
	result, err := recordedExtract(t, provider, ExecConfig{})
	if err == nil {
		t.Fatal("empty answers were accepted")
	}
	meta := result.Meta
	if meta.Usage.TotalTokens != 110 {
		t.Errorf("usage = %d tokens, want both refused attempts' 110", meta.Usage.TotalTokens)
	}
	if meta.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", meta.Attempts)
	}
	if meta.Cost.TotalCost <= 0 && meta.Cost.Priced {
		t.Errorf("priced cost %v for paid attempts", meta.Cost.TotalCost)
	}
}

func TestRepairsAreCountedForExtract(t *testing.T) {
	cases := []struct {
		name        string
		bodies      []string
		repairs     int
		wantRepairs int
		wantErr     bool
	}{
		{"a good first answer", []string{`{"ok":true}`}, 1, 0, false},
		{"one repair", []string{"not json", `{"ok":true}`}, 1, 1, false},
		{"two repairs", []string{"not json", `{"nope":1}`, `{"ok":true}`}, 2, 2, false},
		{"never repaired", []string{"not json", `{"ok":true}`}, -1, 0, true},
		{"repairs used up", []string{"not json", "not json", "not json"}, 2, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var responses []llm.CompletionResponse
			for _, body := range tc.bodies {
				responses = append(responses, llm.CompletionResponse{Content: body, FinishReason: "stop", Usage: usageOf(10, 2)})
			}
			withDefaultExec(t, &ExecConfig{Provider: &pricedScript{responses: responses}, Model: "gpt-5-mini", Repairs: tc.repairs})
			options := NewExtractOptions()
			result, err := ExtractResult[recordedAnswer]("text", options)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			meta := result.Meta
			if meta.Repairs != tc.wantRepairs {
				t.Errorf("Repairs = %d, want %d", meta.Repairs, tc.wantRepairs)
			}
			repairChecks := 0
			for _, check := range meta.Checks {
				if check.Name == "repair" {
					repairChecks++
					if check.Passed || check.Detail == "" {
						t.Errorf("repair check = %+v: it did not pass and says why", check)
					}
				}
			}
			if repairChecks != tc.wantRepairs {
				t.Errorf("%d repair checks, want %d", repairChecks, tc.wantRepairs)
			}
		})
	}
}

func TestRepairFailuresAreChecks(t *testing.T) {
	provider := &pricedScript{responses: []llm.CompletionResponse{
		{Content: "not json at all", FinishReason: "stop", Usage: usageOf(10, 2)},
		{Content: `{"nope":1}`, FinishReason: "stop", Usage: usageOf(10, 2)},
		{Content: `{"ok":true}`, FinishReason: "stop", Usage: usageOf(10, 2)},
	}}
	withDefaultExec(t, &ExecConfig{Provider: provider, Model: "gpt-5-mini", Repairs: 2})
	result, err := ExtractResult[recordedAnswer]("text", NewExtractOptions())
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, check := range result.Meta.Checks {
		if check.Name == "repair" {
			details = append(details, check.Detail)
		}
	}
	if len(details) != 2 {
		t.Fatalf("repair checks: %v", details)
	}
	if details[0] == details[1] {
		t.Errorf("both repairs carry the same failure %q: each should say what its own answer was refused for", details[0])
	}
	if result.Meta.Attempts != 3 || result.Meta.Repairs != 2 {
		t.Errorf("attempts %d, repairs %d", result.Meta.Attempts, result.Meta.Repairs)
	}
}

func TestCacheHitHedgeAndServiceTierReachTheEnvelope(t *testing.T) {
	cases := []struct {
		name      string
		resp      llm.CompletionResponse
		cacheHits int
		hedged    bool
		tier      string
	}{
		{"nothing", llm.CompletionResponse{}, 0, false, ""},
		{"a cache hit", llm.CompletionResponse{Cached: true}, 1, false, ""},
		{"a hedge", llm.CompletionResponse{Hedged: true}, 0, true, ""},
		{"a service tier", llm.CompletionResponse{ServiceTier: "priority"}, 0, false, "priority"},
		{"all three", llm.CompletionResponse{Cached: true, Hedged: true, ServiceTier: "flex"}, 1, true, "flex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.resp
			resp.Content, resp.FinishReason, resp.Usage = `{"ok":true}`, "stop", usageOf(10, 2)
			result, err := recordedExtract(t, &pricedScript{responses: []llm.CompletionResponse{resp}}, ExecConfig{})
			if err != nil {
				t.Fatal(err)
			}
			meta := result.Meta
			if meta.CacheHits != tc.cacheHits || meta.Hedged != tc.hedged || meta.ServiceTier != tc.tier {
				t.Errorf("cache hits %d, hedged %v, tier %q; want %d, %v, %q", meta.CacheHits, meta.Hedged, meta.ServiceTier, tc.cacheHits, tc.hedged, tc.tier)
			}
		})
	}
}

func TestEnvelopeOfOneCleanCallIsUnchanged(t *testing.T) {
	provider := &pricedScript{responses: []llm.CompletionResponse{{Content: `{"ok":true}`, FinishReason: "stop", Usage: usageOf(120, 24)}}}
	result, err := recordedExtract(t, provider, ExecConfig{})
	if err != nil {
		t.Fatal(err)
	}
	meta := result.Meta
	if meta.Attempts != 1 || meta.Retries != 0 || meta.CacheHits != 0 || meta.Hedged || meta.ServiceTier != "" || meta.Repairs != 0 {
		t.Errorf("a clean call's counts: %+v", meta)
	}
	if len(meta.Checks) != 0 {
		t.Errorf("a clean call carries checks: %+v", meta.Checks)
	}
	if meta.Usage != usageOf(120, 24) {
		t.Errorf("usage = %+v", meta.Usage)
	}
	cost := pricing.CalculateCost(&meta.Usage, "gpt-5-mini", "openai")
	if meta.Cost.TotalCost != cost.TotalCost || meta.Cost.Priced != cost.Priced {
		t.Errorf("cost = %+v, want %+v", meta.Cost, cost)
	}
}

// envelopeFrom, directly, over the records of the new kinds.
func TestEnvelopeFromReadsUsageOnlyRecordsAndTheNewFacts(t *testing.T) {
	usage := func(n int) *types.TokenUsage { return &types.TokenUsage{PromptTokens: n, TotalTokens: n} }
	priced := func(c float64) *types.CostInfo {
		return &types.CostInfo{TotalCost: c, Priced: true, Quality: types.PricingExact}
	}
	cases := []struct {
		name         string
		records      []*types.ResultMetadata
		wantAttempts int
		wantTokens   int
		wantProvider string
		wantRetries  int
		wantHits     int
		wantHedged   bool
		wantTier     string
		wantCost     float64
	}{
		{"one record", []*types.ResultMetadata{{Provider: "a", TokenUsage: usage(10), CostInfo: priced(1)}}, 1, 10, "a", 0, 0, false, "", 1},
		{"a usage-only record adds usage and cost and no attempt", []*types.ResultMetadata{
			{UsageOnly: true, Provider: "x", TokenUsage: usage(5), CostInfo: priced(0.5)},
			{Provider: "a", TokenUsage: usage(10), CostInfo: priced(1)}}, 1, 15, "a", 0, 0, false, "", 1.5},
		{"the identity is the last record that is not usage-only", []*types.ResultMetadata{
			{Provider: "a", TokenUsage: usage(10), CostInfo: priced(1)},
			{UsageOnly: true, Provider: "x", TokenUsage: usage(5), CostInfo: priced(0.5)}}, 1, 15, "a", 0, 0, false, "", 1.5},
		{"retries are summed", []*types.ResultMetadata{
			{RetryCount: 2, Provider: "a"}, {RetryCount: 1, Provider: "a"}}, 5, 0, "a", 3, 0, false, "", 0},
		{"cache hits are counted", []*types.ResultMetadata{
			{CacheHit: true, Provider: "a"}, {Provider: "a"}, {CacheHit: true, Provider: "a"}}, 3, 0, "a", 0, 2, false, "", 0},
		{"hedged if any", []*types.ResultMetadata{{Provider: "a"}, {Hedged: true, Provider: "a"}, {Provider: "a"}}, 3, 0, "a", 0, 0, true, "", 0},
		{"the last service tier that is not empty", []*types.ResultMetadata{
			{ServiceTier: "flex", Provider: "a"}, {ServiceTier: "priority", Provider: "a"}, {Provider: "a"}}, 3, 0, "a", 0, 0, false, "priority", 0},
		{"a usage-only record's tier is not the call's", []*types.ResultMetadata{
			{Provider: "a"}, {UsageOnly: true, ServiceTier: "flex", TokenUsage: usage(1)}}, 1, 1, "a", 0, 0, false, "", 0},
		{"only usage-only records still say something", []*types.ResultMetadata{
			{UsageOnly: true, Provider: "x", TokenUsage: usage(4), CostInfo: priced(2)}}, 0, 4, "x", 0, 0, false, "", 2},
		{"a failed request after a refused one", []*types.ResultMetadata{
			{UsageOnly: true, Provider: "x", TokenUsage: usage(7), CostInfo: priced(3)},
			{RetryCount: 1, Provider: "a"}}, 2, 7, "a", 1, 0, false, "", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := envelopeFrom(tc.records, "op")
			if meta.Attempts != tc.wantAttempts || meta.Usage.PromptTokens != tc.wantTokens || meta.Provider != tc.wantProvider {
				t.Errorf("attempts %d, tokens %d, provider %q; want %d, %d, %q", meta.Attempts, meta.Usage.PromptTokens, meta.Provider, tc.wantAttempts, tc.wantTokens, tc.wantProvider)
			}
			if meta.Retries != tc.wantRetries || meta.CacheHits != tc.wantHits || meta.Hedged != tc.wantHedged || meta.ServiceTier != tc.wantTier {
				t.Errorf("retries %d, hits %d, hedged %v, tier %q; want %d, %d, %v, %q", meta.Retries, meta.CacheHits, meta.Hedged, meta.ServiceTier, tc.wantRetries, tc.wantHits, tc.wantHedged, tc.wantTier)
			}
			if meta.Cost.TotalCost != tc.wantCost {
				t.Errorf("cost %v, want %v", meta.Cost.TotalCost, tc.wantCost)
			}
		})
	}
}

func TestCallRecordsMetaCountsRepairs(t *testing.T) {
	records := &callRecords{}
	ctx := context.WithValue(context.Background(), callRecordKey{}, records)
	noteRepair(ctx, "first")
	noteRepair(ctx, "second")
	noteRepair(context.Background(), "nobody is collecting")
	//lint:ignore SA1012 a nil context is what is tested.
	noteRepair(nil, "nobody is collecting")
	meta := records.meta("op")
	if meta.Repairs != 2 || len(meta.Checks) != 2 || meta.Checks[0].Detail != "first" || meta.Checks[1].Detail != "second" {
		t.Errorf("meta = repairs %d, checks %+v", meta.Repairs, meta.Checks)
	}
	var none *callRecords
	if got := none.meta("op"); got.Operation != "op" || got.Repairs != 0 {
		t.Errorf("a nil collector's meta = %+v", got)
	}
}

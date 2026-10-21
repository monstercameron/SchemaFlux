package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 12, the envelope a sort returns and the level a sort or a batch
// is held to.

// bite12Provider answers a sort without a network: a whole-list request gets
// ids, a per-item scoring request gets score, and every call takes delay.
type bite12Provider struct {
	mu         sync.Mutex
	calls      int
	delay      time.Duration
	ids        string
	score      string
	err        error
	answeredAs string // the provider an answer names itself as, when not "local"
	body       string // when set, every request is answered with this
}

func (p *bite12Provider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
	if p.err != nil {
		return llm.CompletionResponse{}, p.err
	}
	content := p.ids
	switch {
	case p.body != "":
		content = p.body
	case strings.Contains(req.SystemPrompt, "rank_score"):
		content = p.score
	}
	return llm.CompletionResponse{Content: content, Model: req.Model, Provider: p.answeredAs, FinishReason: "stop"}, nil
}

func (p *bite12Provider) Name() string                               { return "local" }
func (p *bite12Provider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *bite12Provider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func (p *bite12Provider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func bite12Items(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("item-%03d", i)
	}
	return items
}

// The envelope of a sort carries the caller's ids and the time the whole sort
// took on every path. It was built from the last provider call, so a floor
// refusal, invalid options and a sort of fewer than two items, which make no
// call, came back with no ids at all, and a sort that scored every item
// reported how long the last item took.
func TestBite12EverySortEnvelopeCarriesTheCallersIds(t *testing.T) {
	const orderTwo = `{"ids":["i-000002","i-000001"]}`
	cases := []struct {
		name         string
		items        []string
		provider     *bite12Provider
		set          func(*SortOptions)
		floor        types.ContractLevel
		wantErr      bool
		wantStrategy string
		wantCalls    bool
	}{
		{"a whole-list answer", bite12Items(2), &bite12Provider{ids: orderTwo}, nil, 0, false, "whole-list", true},
		{"a floor the sort cannot meet", bite12Items(2), &bite12Provider{ids: orderTwo}, nil, types.ContractFullyGoverned, true, "", false},
		{"options with no criteria", bite12Items(2), &bite12Provider{ids: orderTwo}, func(o *SortOptions) { o.Criteria = "" }, 0, true, "", false},
		{"options with a direction that is not one", bite12Items(2), &bite12Provider{ids: orderTwo}, func(o *SortOptions) { o.Direction = "sideways" }, 0, true, "", false},
		{"options with a threshold off the scale", bite12Items(2), &bite12Provider{ids: orderTwo}, func(o *SortOptions) { o.CommonOptions.Threshold = 2 }, 0, true, "", false},
		{"no items", nil, &bite12Provider{ids: orderTwo}, nil, 0, false, "trivial", false},
		{"one item", bite12Items(1), &bite12Provider{ids: orderTwo}, nil, 0, false, "trivial", false},
		{"a provider that fails", bite12Items(2), &bite12Provider{err: errors.New("the provider is down")}, nil, 0, true, "", true},
		{"every item scored", bite12Items(sortScoringThreshold + 1), &bite12Provider{score: `{"rank_score":0.5}`}, nil, 0, false, "scoring", true},
		{"a fallback to scoring", bite12Items(3), &bite12Provider{ids: `{"ids":["i-000001","i-000001","i-000003"]}`, score: `{"rank_score":0.5}`}, nil, 0, false, "scoring-fallback", true},
		{"an answer that is neither", bite12Items(3), &bite12Provider{ids: "not JSON", score: "not JSON"}, nil, 0, true, "scoring-fallback", true},
		{"ids set on the embedded half", bite12Items(2), &bite12Provider{ids: orderTwo}, func(o *SortOptions) {
			o.CommonOptions.RequestID, o.CommonOptions.CorrelationID = "", ""
			o.OpOptions.RequestID, o.OpOptions.CorrelationID = "req-embedded", "corr-embedded"
		}, 0, false, "whole-list", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithProvider(context.Background(), tc.provider)
			if tc.floor != 0 {
				ctx = WithContractPolicy(ctx, types.DataPolicy{MinimumContract: tc.floor})
			}
			opts := NewSortOptions().WithCriteria("alphabetical").WithModel("gpt-6-luna").WithContext(ctx)
			opts.CommonOptions.RequestID, opts.CommonOptions.CorrelationID = "req-1", "corr-1"
			if tc.set != nil {
				tc.set(&opts)
			}
			wantRequest, wantCorrelation := "req-1", "corr-1"
			if opts.CommonOptions.RequestID == "" {
				wantRequest, wantCorrelation = "req-embedded", "corr-embedded"
			}

			result, err := SortResult(tc.items, opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error = %v", err, tc.wantErr)
			}
			meta := result.Meta
			if meta.RequestID != wantRequest || meta.CorrelationID != wantCorrelation {
				t.Errorf("ids = %q / %q, want %q / %q", meta.RequestID, meta.CorrelationID, wantRequest, wantCorrelation)
			}
			if meta.Operation != "sort" {
				t.Errorf("Operation = %q", meta.Operation)
			}
			if meta.RequestedContract != sortContract {
				t.Errorf("RequestedContract = %s", meta.RequestedContract)
			}
			if tc.wantStrategy != "" && meta.Strategy != tc.wantStrategy {
				t.Errorf("Strategy = %q, want %q", meta.Strategy, tc.wantStrategy)
			}
			if made := tc.provider.callCount() > 0; made != tc.wantCalls {
				t.Errorf("the provider was called = %v, want %v", made, tc.wantCalls)
			}
			if tc.wantErr && result.Value != nil {
				t.Errorf("a value came back beside the error: %v", result.Value)
			}
		})
	}

	t.Run("a call with no ids from the caller is stamped with its own", func(t *testing.T) {
		provider := &bite12Provider{ids: orderTwo}
		opts := NewSortOptions().WithCriteria("alphabetical").WithModel("gpt-6-luna").WithContext(WithProvider(context.Background(), provider))
		result, err := SortResult(bite12Items(2), opts)
		if err != nil || result.Meta.RequestID == "" {
			t.Errorf("err %v, RequestID %q", err, result.Meta.RequestID)
		}
	})
	t.Run("a sort that makes no call has no ids to invent", func(t *testing.T) {
		opts := NewSortOptions().WithCriteria("alphabetical").WithContext(WithProvider(context.Background(), &bite12Provider{}))
		result, err := SortResult(bite12Items(1), opts)
		if err != nil || result.Meta.RequestID != "" || result.Meta.CorrelationID != "" {
			t.Errorf("err %v, ids %q / %q", err, result.Meta.RequestID, result.Meta.CorrelationID)
		}
	})
}

// Elapsed is how long the sort took, not how long its last provider call did.
// A sort that scores fifty-one items in groups of DefaultConcurrency cannot take
// less than the groups' delays added up, and the last call took one of them.
func TestBite12SortElapsedIsTheWholeSort(t *testing.T) {
	const delay = 10 * time.Millisecond
	items := bite12Items(sortScoringThreshold + 1)
	rounds := (len(items) + DefaultConcurrency - 1) / DefaultConcurrency
	provider := &bite12Provider{score: `{"rank_score":0.5}`, delay: delay}
	opts := NewSortOptions().WithCriteria("alphabetical").WithModel("gpt-6-luna").WithContext(WithProvider(context.Background(), provider))

	result, err := SortResult(items, opts)
	if err != nil {
		t.Fatalf("SortResult: %v", err)
	}
	if want := time.Duration(rounds) * delay; result.Meta.Elapsed < want {
		t.Errorf("Elapsed = %v, want at least %v: %d calls of %v, %d at a time", result.Meta.Elapsed, want, len(items), delay, DefaultConcurrency)
	}
	if result.Meta.Attempts < len(items) {
		t.Errorf("Attempts = %d, want at least %d, one for each item", result.Meta.Attempts, len(items))
	}
}

// Attempts is the number of provider calls, so a sort that sent nothing made
// none. It is not one: that would say a call went out that never did.
func TestBite12SortAttemptsCountProviderCalls(t *testing.T) {
	cases := []struct {
		name  string
		items int
		reply string
		want  int
	}{
		{"no items", 0, `{"ids":[]}`, 0},
		{"one item", 1, `{"ids":["i-000001"]}`, 0},
		{"two items", 2, `{"ids":["i-000002","i-000001"]}`, 1},
		{"three items", 3, `{"ids":["i-000003","i-000002","i-000001"]}`, 1},
		{"ten items", 10, `{"ids":["i-000010","i-000009","i-000008","i-000007","i-000006","i-000005","i-000004","i-000003","i-000002","i-000001"]}`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &bite12Provider{ids: tc.reply}
			opts := NewSortOptions().WithCriteria("alphabetical").WithModel("gpt-6-luna").WithContext(WithProvider(context.Background(), provider))
			result, err := SortResult(bite12Items(tc.items), opts)
			if err != nil {
				t.Fatalf("SortResult: %v", err)
			}
			if result.Meta.Attempts != tc.want || provider.callCount() != tc.want {
				t.Errorf("Attempts = %d after %d provider calls, want %d", result.Meta.Attempts, provider.callCount(), tc.want)
			}
		})
	}
}

// bite12Route is how the route a call is sent to is declared for a floor test.
type bite12Route int

const (
	bite12Undeclared bite12Route = iota
	bite12Native                 // declared, and enforces a schema
	bite12NotNative              // declared, and does not
)

func bite12Declare(t *testing.T, route bite12Route) {
	t.Helper()
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	switch route {
	case bite12Native:
		llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "local", Model: "gpt-6-luna", Supports: map[llm.Capability]bool{llm.CapNativeJSONSchema: true}})
	case bite12NotNative:
		llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "local", Model: "gpt-6-luna", Supports: map[llm.Capability]bool{llm.CapNativeJSONSchema: false}})
	}
}

// bite12FloorCase is one row of the table a sort and a batch are both held to.
type bite12FloorCase struct {
	name       string
	route      bite12Route
	schema     bool
	answeredAs string // another provider than the one asked
	floor      types.ContractLevel
	fewItems   bool // a sort of one item sends nothing; a batch of none does not run

	refusedBefore bool // refused with nothing sent
	refusedAfter  bool // refused once the answer was in
	delivered     types.ContractLevel
	calls         int
}

// A batch is held to the level its answer earned, on the route that
// answered. The route it planned can promise more than the one that answered
// delivers, a gateway sending a request on to a provider that enforces no
// schema, and the check after the call was made against the constant the check
// before it had passed, so it could not fire. Sort declares no schema and is
// held to one level on every route: see TestSortIsHeldToTheLevelItEarns.
func bite12FloorCases(establishes types.ContractLevel) []bite12FloorCase {
	return []bite12FloorCase{
		{name: "no schema asked for, any route, a floor above well-formed", route: bite12Undeclared, floor: establishes, refusedBefore: true},
		{name: "no schema asked for, any route, the floor it earns", route: bite12Undeclared, floor: types.ContractJSONWellFormed, delivered: types.ContractJSONWellFormed, calls: 1},
		{name: "a schema, a route that enforces it", route: bite12Native, schema: true, floor: establishes, delivered: establishes, calls: 1},
		{name: "a schema, answered by a route that enforces nothing, a floor it still meets", route: bite12Native, schema: true, answeredAs: "elsewhere",
			floor: types.ContractJSONWellFormed, delivered: types.ContractJSONWellFormed, calls: 1},
		{name: "a schema, answered by a route that enforces nothing, a floor it misses", route: bite12Native, schema: true, answeredAs: "elsewhere",
			floor: types.ContractSchemaConstrained, refusedAfter: true, calls: 1},
		{name: "the same at the highest floor the operation reaches", route: bite12Native, schema: true, answeredAs: "elsewhere",
			floor: establishes, refusedAfter: true, calls: 1},
		{name: "the same with no floor", route: bite12Native, schema: true, answeredAs: "elsewhere", delivered: types.ContractJSONWellFormed, calls: 1},
		{name: "a schema, a route declared not to enforce it, a floor above well-formed", route: bite12NotNative, schema: true,
			floor: types.ContractSchemaConstrained, refusedBefore: true},
		{name: "a schema, a route declared not to enforce it, a floor it meets", route: bite12NotNative, schema: true,
			floor: types.ContractJSONWellFormed, delivered: types.ContractJSONWellFormed, calls: 1},
		{name: "a schema, a route nothing is known of, a floor above well-formed", route: bite12Undeclared, schema: true,
			floor: types.ContractSchemaConstrained, refusedBefore: true},
		{name: "no schema, a route that enforces none, the floor of the operation", route: bite12NotNative, floor: establishes, refusedBefore: true},
		{name: "no schema, a route that enforces one, the floor of the operation", route: bite12Native, floor: establishes, refusedBefore: true},
		{name: "a sort that sends nothing, a schema, a route nothing is known of", route: bite12Undeclared, schema: true, fewItems: true,
			floor: establishes, delivered: establishes, calls: 0},
	}
}

// Sort declares no schema, so the level it requests and delivers is well
// formed, on every route, whatever it checks afterwards, and a floor above that
// is refused before anything is sent. The envelope of a refusal names the same
// level.
func TestSortIsHeldToTheLevelItEarns(t *testing.T) {
	cases := []struct {
		name     string
		route    bite12Route
		schema   bool // a schema set on the call's options by the caller
		floor    types.ContractLevel
		fewItems bool
		refused  bool
		calls    int
	}{
		{name: "no floor, a route nothing is known of", route: bite12Undeclared, calls: 1},
		{name: "no floor, a route that enforces a schema", route: bite12Native, calls: 1},
		{name: "no floor, a route that enforces none", route: bite12NotNative, calls: 1},
		{name: "the floor is the level it earns", route: bite12Undeclared, floor: types.ContractJSONWellFormed, calls: 1},
		{name: "the floor is the level it earns on a native route", route: bite12Native, floor: types.ContractJSONWellFormed, calls: 1},
		{name: "the floor is the level it earns with a schema on the options", route: bite12Native, schema: true, floor: types.ContractJSONWellFormed, calls: 1},
		{name: "a floor of schema constrained is refused before a send", route: bite12Undeclared, floor: types.ContractSchemaConstrained, refused: true},
		{name: "a floor of schema constrained on a native route", route: bite12Native, floor: types.ContractSchemaConstrained, refused: true},
		{name: "a floor of schema and invariants checked", route: bite12Undeclared, floor: types.ContractSchemaAndInvariantChecked, refused: true},
		{name: "a floor of schema and invariants checked on a native route", route: bite12Native, floor: types.ContractSchemaAndInvariantChecked, refused: true},
		{name: "a floor of schema and invariants checked on a route that enforces none", route: bite12NotNative, floor: types.ContractSchemaAndInvariantChecked, refused: true},
		{name: "the highest floor", route: bite12Undeclared, floor: types.ContractFullyGoverned, refused: true},
		{name: "one item sends nothing and still earns no more", route: bite12Undeclared, floor: types.ContractJSONWellFormed, fewItems: true},
		{name: "one item and a floor above the level", route: bite12Undeclared, floor: types.ContractSchemaAndInvariantChecked, fewItems: true, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bite12Declare(t, tc.route)
			provider := &bite12Provider{ids: `{"ids":["i-000002","i-000001"]}`}
			ctx := WithContractPolicy(WithProvider(context.Background(), provider), types.DataPolicy{MinimumContract: tc.floor})
			opts := NewSortOptions().WithCriteria("alphabetical").WithModel("gpt-6-luna").WithContext(ctx)
			if tc.schema {
				opts.OpOptions.JSONSchema = map[string]any{"type": "object"}
			}
			items := bite12Items(2)
			if tc.fewItems {
				items = items[:1]
			}

			result, err := SortResult(items, opts)
			if result.Meta.RequestedContract != types.ContractJSONWellFormed {
				t.Errorf("requested %s, want %s", result.Meta.RequestedContract, types.ContractJSONWellFormed)
			}
			if tc.refused {
				if !errors.Is(err, ErrContractDegraded) || !errors.Is(err, types.ErrPolicyViolation) {
					t.Fatalf("err = %v, want the floor's refusal", err)
				}
				if result.Value != nil {
					t.Errorf("a value came back beside the refusal: %v", result.Value)
				}
			} else {
				if err != nil {
					t.Fatalf("SortResult: %v", err)
				}
				if result.Meta.DeliveredContract != types.ContractJSONWellFormed {
					t.Errorf("delivered %s, want %s", result.Meta.DeliveredContract, types.ContractJSONWellFormed)
				}
			}
			if provider.callCount() != tc.calls {
				t.Errorf("the provider was called %d times, want %d", provider.callCount(), tc.calls)
			}
		})
	}
}

func TestBite12ABatchIsHeldToWhatItDelivered(t *testing.T) {
	invariants := []func(string) error{lx11NotShouting}
	op := lx12WordsOp(OutputContract[string]{SchemaName: "words", Invariants: invariants})
	for _, tc := range bite12FloorCases(batchContractLevel(op)) {
		if tc.fewItems {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			bite12Declare(t, tc.route)
			provider := &bite12Provider{body: lx12Words("a", "b"), answeredAs: tc.answeredAs}
			ctx := WithContractPolicy(WithProvider(context.Background(), offeringIDsProvider{provider}), types.DataPolicy{MinimumContract: tc.floor})
			opt := types.OpOptions{Model: "gpt-6-luna"}
			if tc.schema {
				opt.JSONSchema = map[string]any{"type": "object"}
			}

			result, err := RunOpBatchResult(ctx, op, []string{"x", "y"}, opt)
			switch {
			case tc.refusedBefore || tc.refusedAfter:
				if !errors.Is(err, ErrContractDegraded) || !errors.Is(err, types.ErrPolicyViolation) {
					t.Fatalf("err = %v, want the floor's refusal", err)
				}
				if result.Value != nil {
					t.Errorf("outputs came back beside the refusal: %v", result.Value)
				}
			default:
				if err != nil {
					t.Fatalf("RunOpBatchResult: %v", err)
				}
				if result.Meta.DeliveredContract != tc.delivered || len(result.Value) != 2 {
					t.Errorf("delivered %s with %v, want %s", result.Meta.DeliveredContract, result.Value, tc.delivered)
				}
			}
			if provider.callCount() != tc.calls {
				t.Errorf("the provider was called %d times, want %d", provider.callCount(), tc.calls)
			}
			if tc.refusedAfter && result.Meta.DeliveredContract >= tc.floor {
				t.Errorf("the envelope says %s was delivered, and the floor is %s", result.Meta.DeliveredContract, tc.floor)
			}
		})
	}
}

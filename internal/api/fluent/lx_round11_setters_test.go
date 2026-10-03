package fluent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// requestRecorder answers with one body and keeps the last request.
type requestRecorder struct {
	mu   sync.Mutex
	body string
	last llm.CompletionRequest
}

func (p *requestRecorder) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = req
	return llm.CompletionResponse{Content: p.body, Model: req.Model, Provider: "local", FinishReason: "stop"}, nil
}
func (p *requestRecorder) Name() string                               { return "local" }
func (p *requestRecorder) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *requestRecorder) RetryPolicy() (int, time.Duration)          { return 0, 0 }

type round11Vendor struct {
	Name string `json:"name"`
}

// Review round 11. The builders had no way to ask for a shallow audit or for a
// reason with every evaluation: each option existed on the struct and had no
// setter, and the constructors the builders start from set Deep, which is
// refused beside Shallow, and IncludeReasoning, which a struct does not.
func TestTheBuildersCanAskForWhatTheStructsCan(t *testing.T) {
	const evaluated = `{"options":{"o1":{"r1":{"passed":true,"score":0.9,"reasoning":"why"}},"o2":{"r1":{"passed":true,"score":0.2,"reasoning":"why"}}},"reasoning":"x","confidence":0.8}`
	vendors := []round11Vendor{{Name: "a"}, {Name: "b"}}

	t.Run("Arbitrating", func(t *testing.T) {
		cases := []struct {
			name    string
			build   func() ArbitrateRequest[round11Vendor]
			reasons bool
		}{
			{"as it starts", func() ArbitrateRequest[round11Vendor] { return Arbitrating(vendors).Rules("cheap") }, false},
			{"with reasons", func() ArbitrateRequest[round11Vendor] {
				return Arbitrating(vendors).Rules("cheap").IncludeReasoning(true)
			}, true},
			{"with reasons, set before the rules", func() ArbitrateRequest[round11Vendor] {
				return Arbitrating(vendors).IncludeReasoning(true).Rules("cheap")
			}, true},
			{"with reasons, then without", func() ArbitrateRequest[round11Vendor] {
				return Arbitrating(vendors).Rules("cheap").IncludeReasoning(true).IncludeReasoning(false)
			}, false},
			{"without, said outright", func() ArbitrateRequest[round11Vendor] {
				return Arbitrating(vendors).Rules("cheap").IncludeReasoning(false)
			}, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				provider := &requestRecorder{body: evaluated}
				restore := installStubProvider(t, provider)
				defer restore()

				result, err := tc.build().Run(context.Background())
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if asked := strings.Contains(provider.last.UserPrompt, "Give a reason with every evaluation."); asked != tc.reasons {
					t.Errorf("reasons asked for = %v, want %v", asked, tc.reasons)
				}
				if got := result.Evaluations[0].RuleResults[0].Reasoning != ""; got != tc.reasons {
					t.Errorf("a reason was returned = %v, want %v", got, tc.reasons)
				}
			})
		}
	})

	t.Run("Auditing", func(t *testing.T) {
		cases := []struct {
			name    string
			build   func() AuditRequest[round11Vendor]
			shallow bool
			// body is what the model answers. Policies are reported on by key,
			// so a case that sets them cannot take the default.
			body string
		}{
			{"as it starts", func() AuditRequest[round11Vendor] { return Auditing(vendors[0]) }, false, ""},
			{"shallow", func() AuditRequest[round11Vendor] { return Auditing(vendors[0]).Shallow(true) }, true, ""},
			{"shallow, with categories", func() AuditRequest[round11Vendor] {
				return Auditing(vendors[0]).Shallow(true).Categories("quality")
			}, true, ""},
			{"shallow, with policies", func() AuditRequest[round11Vendor] {
				return Auditing(vendors[0]).Shallow(true).Policies("every vendor has a name")
			}, true, `{"policies":{"p1":[]},"findings":[]}`},
			{"shallow, set after the policies", func() AuditRequest[round11Vendor] {
				return Auditing(vendors[0]).Policies("every vendor has a name").Shallow(true)
			}, true, `{"policies":{"p1":[]},"findings":[]}`},
			{"shallow, set after the categories", func() AuditRequest[round11Vendor] {
				return Auditing(vendors[0]).Categories("quality").Shallow(true)
			}, true, ""},
			{"shallow, then not", func() AuditRequest[round11Vendor] {
				return Auditing(vendors[0]).Shallow(true).Shallow(false)
			}, false, ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				body := tc.body
				if body == "" {
					body = `{"findings":[]}`
				}
				provider := &requestRecorder{body: body}
				restore := installStubProvider(t, provider)
				defer restore()

				if _, err := tc.build().Run(context.Background()); err != nil {
					t.Fatalf("Run: %v", err)
				}
				if shallow := strings.Contains(provider.last.UserPrompt, "top level only"); shallow != tc.shallow {
					t.Errorf("asked for the top level only = %v, want %v", shallow, tc.shallow)
				}
			})
		}
	})
}

// A builder's sort reports the level a sort establishes, and what it cost. It
// used to report an envelope with neither.
func TestSortingRunResultIsTheSortsOwnEnvelope(t *testing.T) {
	provider := &requestRecorder{body: `{"ids":["i-000002","i-000001"]}`}
	restore := installStubProvider(t, provider)
	defer restore()

	result, err := Sorting([]string{"b", "a"}).By("alphabetically").RunResult(context.Background())
	if err != nil {
		t.Fatalf("RunResult: %v", err)
	}
	if len(result.Value) != 2 || result.Value[0] != "a" {
		t.Errorf("Value = %v", result.Value)
	}
	// A sort sends no schema: well-formed JSON is the level it earns.
	if result.Meta.DeliveredContract != types.ContractJSONWellFormed || result.Meta.RequestedContract != types.ContractJSONWellFormed {
		t.Errorf("requested %s, delivered %s", result.Meta.RequestedContract, result.Meta.DeliveredContract)
	}
	if result.Meta.Strategy == "" || result.Meta.Attempts != 1 || result.Meta.Operation != "sort" {
		t.Errorf("the envelope does not say how the sort ran: %+v", result.Meta)
	}
}

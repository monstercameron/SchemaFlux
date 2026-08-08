package ops

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-005. Two facts were being collapsed into one field: the model a call asked
// for, and the model that answered. A provider is free to substitute -- serve a
// dated revision, route a deprecated alias elsewhere, fall back under load --
// and the envelope reported only the second while every reader of `Meta.Model`
// naturally read it as the first.
//
// The trap worth naming is the quiet one. `actualModel` in llm_helper.go falls
// back to the requested model when a provider echoes nothing back. That is
// correct for pricing and logging and would be a lie for drift: it makes an
// unobserved substitution indistinguishable from an observed agreement. So the
// raw echo is carried separately, and an absent echo reports drift as
// *unknown* rather than as absent.

// modelReportingProvider records the model it was asked for and answers with
// whatever model string it is told to claim -- the only way to exercise a
// substitution without a real provider performing one.
type modelReportingProvider struct {
	mu sync.Mutex

	// claim is the model the provider reports back. Empty means it reports
	// none, which is what a provider that does not echo looks like.
	claim string

	requested string
}

func (p *modelReportingProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.requested = req.Model
	p.mu.Unlock()

	return llm.CompletionResponse{
		Content:      `{"ok":true}`,
		Provider:     "reporting",
		Model:        p.claim,
		FinishReason: "stop",
	}, nil
}

func (p *modelReportingProvider) Name() string                               { return "reporting" }
func (p *modelReportingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *modelReportingProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

func (p *modelReportingProvider) requestedModel() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requested
}

func TestAPinBeatsTheTierMapping(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "tier-chosen-model")

	provider := &modelReportingProvider{claim: "pinned-model"}
	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{
		Intelligence: types.Smart,
		Model:        "pinned-model",
	}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	if got := provider.requestedModel(); got != "pinned-model" {
		t.Errorf("the request went out with model %q; the pin was ignored", got)
	}
}

func TestNoPinStillUsesTheTierMapping(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "tier-chosen-model")

	provider := &modelReportingProvider{claim: "tier-chosen-model"}
	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{
		Intelligence: types.Smart,
	}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	if got := provider.requestedModel(); got != "tier-chosen-model" {
		t.Errorf("the request went out with model %q; the tier mapping decides when nothing is pinned", got)
	}
}

// The derivation itself, driven directly, because the three outcomes are the
// point and a fake that always echoes can only reach one of them.
func TestModelDriftIsDerivedAndNeverGuessed(t *testing.T) {
	cases := []struct {
		name        string
		requested   string
		observed    string
		wantDrifted bool
		wantUnknown bool
		why         string
	}{
		{
			name: "agreement", requested: "gpt-x", observed: "gpt-x",
			why: "the provider answered with what was asked for",
		},
		{
			name: "dated revision", requested: "gpt-x", observed: "gpt-x-2026-01-01",
			wantDrifted: true,
			why:         "a dated revision is a different model from the alias that requested it",
		},
		{
			name: "outright substitution", requested: "gpt-x", observed: "some-other-model",
			wantDrifted: true,
			why:         "the provider served something else entirely",
		},
		{
			name: "provider echoed nothing", requested: "gpt-x", observed: "",
			wantUnknown: true,
			why:         "silence is not agreement -- the case the fallback would have hidden",
		},
		{
			name: "nothing was requested", requested: "", observed: "gpt-x",
			wantUnknown: true,
			why:         "with no requested model there is nothing to have drifted from",
		},
		{
			name: "neither side known", requested: "", observed: "",
			wantUnknown: true,
			why:         "two unknowns are not a match",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := types.MetaFrom(&types.ResultMetadata{
				RequestedModel: tc.requested,
				ObservedModel:  tc.observed,
				Model:          tc.observed,
			})

			if meta.ModelDrifted != tc.wantDrifted {
				t.Errorf("ModelDrifted = %v, want %v (%s)", meta.ModelDrifted, tc.wantDrifted, tc.why)
			}
			if meta.ModelDriftUnknown != tc.wantUnknown {
				t.Errorf("ModelDriftUnknown = %v, want %v (%s)", meta.ModelDriftUnknown, tc.wantUnknown, tc.why)
			}
			// Mutually exclusive: a caller asking "did this drift" must not get
			// yes and don't-know from the same result.
			if meta.ModelDrifted && meta.ModelDriftUnknown {
				t.Error("a result reports both drifted and unknown")
			}
		})
	}
}

// End to end through the real call path, so the wiring is checked and not only
// the derivation.
func TestAHonouredPinReportsNoDriftThroughTheRealCallPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	ctx, records := withCallRecording(context.Background())
	provider := &modelReportingProvider{claim: "pinned-model"}

	if _, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{
		Model: "pinned-model",
	}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	collected := records.collect()
	if len(collected) == 0 {
		t.Fatal("no call record was published")
	}
	meta := types.MetaFrom(collected[len(collected)-1])

	if meta.RequestedModel != "pinned-model" {
		t.Errorf("RequestedModel = %q, want the pin", meta.RequestedModel)
	}
	if meta.ModelDrifted {
		t.Error("drift reported for a provider that echoed the pinned model back")
	}
	if meta.ModelDriftUnknown {
		t.Error("drift reported unknown for a provider that did echo a model")
	}
}

func TestASubstitutingProviderIsVisibleThroughTheRealCallPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	ctx, records := withCallRecording(context.Background())
	provider := &modelReportingProvider{claim: "something-else-entirely"}

	if _, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{
		Model: "pinned-model",
	}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	collected := records.collect()
	if len(collected) == 0 {
		t.Fatal("no call record was published")
	}
	meta := types.MetaFrom(collected[len(collected)-1])

	if !meta.ModelDrifted {
		t.Error("a provider that answered with a different model reported no drift")
	}
	if meta.ModelDriftUnknown {
		t.Error("drift reported unknown when both sides were named")
	}
}

func TestAProviderThatEchoesNothingLeavesDriftUnknown(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	ctx, records := withCallRecording(context.Background())
	provider := &modelReportingProvider{claim: ""}

	if _, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{
		Model: "pinned-model",
	}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	collected := records.collect()
	if len(collected) == 0 {
		t.Fatal("no call record was published")
	}
	meta := types.MetaFrom(collected[len(collected)-1])

	if !meta.ModelDriftUnknown {
		t.Error("a provider that echoed no model reported drift as known; silence is not agreement")
	}
	if meta.ModelDrifted {
		t.Error("drift reported as observed when nothing was observed")
	}
	// Meta.Model still falls back, because pricing and logs need a name. That
	// fallback is precisely why the raw echo has to be carried separately.
	if meta.Model == "" {
		t.Error("Meta.Model is empty; the fallback that serves pricing and logging was lost")
	}
}

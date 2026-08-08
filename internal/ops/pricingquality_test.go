package ops

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// OB-003. `Priced bool` cannot distinguish the three things that all produce a
// cost of zero: no rate card, a genuinely free model, and nothing spent.
// Reading the first as the second understates the bill by an amount nobody can
// see; reading the second as the first hides a working configuration behind a
// warning. PricingQuality separates them, and separates all of them from a
// figure that was projected rather than measured.

func costed(quality types.PricingQuality, total float64) *types.ResultMetadata {
	return &types.ResultMetadata{
		TokenUsage: &types.TokenUsage{PromptTokens: 100, TotalTokens: 100},
		CostInfo: &types.CostInfo{
			TotalCost: total,
			Priced:    quality.Known(),
			Quality:   quality,
		},
	}
}

func TestKnownSeparatesUnknownFromTheRest(t *testing.T) {
	cases := map[types.PricingQuality]bool{
		types.PricingExact:       true,
		types.PricingEstimated:   true,
		types.PricingFree:        true,
		types.PricingUnknown:     false,
		types.PricingQuality(""): false,
	}

	for quality, want := range cases {
		if got := quality.Known(); got != want {
			t.Errorf("PricingQuality(%q).Known() = %v, want %v", quality, got, want)
		}
	}
}

// The rule: a total is only as good as its worst part, and one unknown makes
// the whole thing unknown — the missing amount is unbounded, and adding zero
// for it understates the bill by exactly the figure nobody can see.
func TestOneUnknownAttemptMakesTheWholeTotalUnknown(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		costed(types.PricingExact, 0.10),
		costed(types.PricingUnknown, 0),
		costed(types.PricingExact, 0.10),
	}, "extract")

	if meta.Cost.Quality != types.PricingUnknown {
		t.Errorf("Quality = %q, want unknown when one attempt had no rate card", meta.Cost.Quality)
	}
}

func TestOneEstimatedAttemptMakesTheTotalEstimated(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		costed(types.PricingExact, 0.10),
		costed(types.PricingEstimated, 0.10),
	}, "extract")

	if meta.Cost.Quality != types.PricingEstimated {
		t.Errorf("Quality = %q, want estimated", meta.Cost.Quality)
	}
}

// Free plus exact is exact: adding a genuine zero to a measured figure loses
// nothing, which is precisely what makes free different from unknown.
func TestAFreeAttemptDoesNotDegradeAMeasuredTotal(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		costed(types.PricingFree, 0),
		costed(types.PricingExact, 0.10),
	}, "extract")

	if meta.Cost.Quality != types.PricingExact {
		t.Errorf("Quality = %q, want exact -- a free attempt costs nothing and hides nothing", meta.Cost.Quality)
	}
}

func TestAnEntirelyFreeRequestIsFreeNotUnknown(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		costed(types.PricingFree, 0),
		costed(types.PricingFree, 0),
	}, "extract")

	if meta.Cost.Quality != types.PricingFree {
		t.Errorf("Quality = %q, want free", meta.Cost.Quality)
	}
	if meta.Cost.TotalCost != 0 {
		t.Errorf("TotalCost = %v on a free request", meta.Cost.TotalCost)
	}
}

// A record from a path that has not been taught to classify yet carries the
// empty quality. Treating that as exact would claim a figure is measured on the
// strength of a zero value, so it degrades to unknown.
func TestAnUnclassifiedRecordDegradesToUnknown(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		{
			TokenUsage: &types.TokenUsage{PromptTokens: 10, TotalTokens: 10},
			CostInfo:   &types.CostInfo{TotalCost: 0.01, Priced: true},
		},
	}, "extract")

	if meta.Cost.Quality != types.PricingUnknown {
		t.Errorf("Quality = %q, want unknown for a record with no classification", meta.Cost.Quality)
	}
}

func TestARequestThatReportedNoCostAtAllIsUnknown(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		{TokenUsage: &types.TokenUsage{PromptTokens: 10, TotalTokens: 10}},
	}, "extract")

	if meta.Cost.Quality != types.PricingUnknown {
		t.Errorf("Quality = %q, want unknown", meta.Cost.Quality)
	}
}

func TestAnEmptyEnvelopeReportsUnknownRatherThanFree(t *testing.T) {
	meta := envelopeFrom(nil, "extract")

	if meta.Cost.Quality == types.PricingFree {
		t.Error("an envelope with no records reported the request as free")
	}
}

// Quality and the older Priced flag must not disagree: a caller reading one and
// a dashboard reading the other would otherwise tell two different stories
// about the same request.
func TestQualityAndPricedAgree(t *testing.T) {
	cases := []struct {
		name    string
		records []*types.ResultMetadata
	}{
		{"all exact", []*types.ResultMetadata{costed(types.PricingExact, 0.1)}},
		{"one unknown", []*types.ResultMetadata{costed(types.PricingExact, 0.1), costed(types.PricingUnknown, 0)}},
		{"all free", []*types.ResultMetadata{costed(types.PricingFree, 0)}},
		{"estimated", []*types.ResultMetadata{costed(types.PricingEstimated, 0.1)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := envelopeFrom(tc.records, "extract")

			if meta.Cost.Priced != meta.Cost.Quality.Known() {
				t.Errorf("Priced = %v but Quality = %q (Known %v): the two disagree about the same request",
					meta.Cost.Priced, meta.Cost.Quality, meta.Cost.Quality.Known())
			}
		})
	}
}

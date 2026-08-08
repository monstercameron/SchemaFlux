package semantic

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// The problem with a quality suite is that nobody can check it. Its output is a
// number about a model, and if the number is wrong there is no second source to
// notice — a scorer with an off-by-one in its denominator reports a plausible
// rate forever.
//
// So the harness is calibrated against a fake whose failure rate is *planted*
// and therefore known exactly. If the suite reports a rate other than the one
// that was planted, the suite is wrong, and that is a statement a test can
// make. None of this touches a provider: these run on every `go test ./...`,
// which is the point — the machinery that will later spend money is proven for
// free.

// plantedProvider fails on a fixed schedule rather than randomly. Randomness
// would make the expected rate itself a distribution, and a test that has to
// reason about its own sampling noise is a test nobody trusts.
type plantedProvider struct {
	// failEvery makes every Nth trial fail. failEvery of 4 plants a 25%
	// failure rate exactly.
	failEvery int
	errEvery  int
	calls     int
	cost      float64
	priced    bool
}

func (p *plantedProvider) trial(context.Context) (Outcome, error) {
	p.calls++
	outcome := Outcome{
		Passed:  true,
		Cost:    p.cost,
		Priced:  p.priced,
		Elapsed: time.Duration(p.calls) * time.Millisecond,
		Tokens:  10,
	}
	if p.errEvery > 0 && p.calls%p.errEvery == 0 {
		outcome.Errored = true
		return outcome, nil
	}
	if p.failEvery > 0 && p.calls%p.failEvery == 0 {
		outcome.Passed = false
	}
	return outcome, nil
}

func TestTheSuiteMeasuresThePlantedRateExactly(t *testing.T) {
	provider := &plantedProvider{failEvery: 4, cost: 0.001, priced: true}

	report, err := Run(context.Background(), []Case{{
		Name: "planted", Dimension: "extraction", Trials: 100, Run: provider.trial,
	}}, 100)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Dimensions) != 1 {
		t.Fatalf("got %d dimensions, want 1", len(report.Dimensions))
	}
	passed := report.Dimensions[0].Passed

	if passed.Trials != 100 {
		t.Errorf("Trials = %d, want 100", passed.Trials)
	}
	if passed.Successes != 75 {
		t.Errorf("Successes = %d, want 75 -- a 25%% planted failure rate was not measured as 25%%", passed.Successes)
	}
	if got := passed.Rate(); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("Rate = %v, want 0.75", got)
	}
}

// The distinction the whole suite rests on: a failed call is not a wrong
// answer. Folding one into the other makes an outage look like a quality
// regression, and somebody spends a day investigating a model that was fine.
func TestAFailedCallIsNotCountedAsAWrongAnswer(t *testing.T) {
	// Every 5th trial errors; nothing else fails its check.
	provider := &plantedProvider{errEvery: 5, priced: true}

	report, err := Run(context.Background(), []Case{{
		Name: "outage", Dimension: "extraction", Trials: 100, Run: provider.trial,
	}}, 100)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	dimension := report.Dimensions[0]
	if dimension.Passed.Trials != 80 {
		t.Errorf("Passed.Trials = %d, want 80 -- errored trials belong outside the quality denominator", dimension.Passed.Trials)
	}
	if got := dimension.Passed.Rate(); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("quality rate = %v, want 1.0 -- an outage lowered a rate it should only have lowered confidence in", got)
	}
	if dimension.Errors.Successes != 20 || dimension.Errors.Trials != 100 {
		t.Errorf("error rate = %d/%d, want 20/100", dimension.Errors.Successes, dimension.Errors.Trials)
	}
}

func TestASingleTrialCaseIsRefused(t *testing.T) {
	provider := &plantedProvider{priced: true}

	_, err := Run(context.Background(), []Case{{
		Name: "one shot", Dimension: "extraction", Trials: 1, Run: provider.trial,
	}}, 100)
	if err == nil {
		t.Fatal("a one-trial case was accepted; a single sample reports 0% or 100% regardless of the model")
	}
	if provider.calls != 0 {
		t.Errorf("the provider was called %d time(s) for a case that should have been refused before running", provider.calls)
	}
}

// The spend rule, which is the one that costs real money to get wrong.
func TestTheCeilingStopsTheRunAndKeepsWhatItMeasured(t *testing.T) {
	// Each trial costs 1.0 against a ceiling of 5.0, so the run must stop
	// having spent 5, not having run all 100 trials.
	provider := &plantedProvider{cost: 1.0, priced: true}

	report, err := Run(context.Background(), []Case{{
		Name: "expensive", Dimension: "extraction", Trials: 100, Run: provider.trial,
	}}, 5.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !report.Stopped {
		t.Fatal("the run did not stop at its ceiling")
	}
	if !errors.Is(report.StopReason, ErrBudgetExhausted) {
		t.Errorf("StopReason = %v, want ErrBudgetExhausted", report.StopReason)
	}
	if report.SpentUSD > report.CeilingUSD {
		t.Errorf("spent %v against a ceiling of %v; the ceiling was checked after the money, not before it",
			report.SpentUSD, report.CeilingUSD)
	}
	// The evidence already paid for is still reported.
	if len(report.Dimensions) == 0 || report.Dimensions[0].Passed.Trials == 0 {
		t.Error("the aborted run discarded the trials it had already paid for")
	}
}

func TestAZeroCeilingRunsNothing(t *testing.T) {
	provider := &plantedProvider{cost: 1.0, priced: true}

	_, err := Run(context.Background(), []Case{{
		Name: "any", Dimension: "extraction", Trials: 10, Run: provider.trial,
	}}, 0)
	if !errors.Is(err, ErrNoBudget) {
		t.Fatalf("err = %v, want ErrNoBudget", err)
	}
	if provider.calls != 0 {
		t.Errorf("the provider was called %d time(s) with no budget", provider.calls)
	}
}

// An unpriced model reports a cost of zero. Treating that as "this trial was
// free" would silently disable the ceiling — the single most expensive bug this
// package could contain.
func TestAnUnpricedTrialMakesTheTotalAFloorAndNotATotal(t *testing.T) {
	provider := &plantedProvider{cost: 0, priced: false}

	report, err := Run(context.Background(), []Case{{
		Name: "unpriced", Dimension: "extraction", Trials: 10, Run: provider.trial,
	}}, 5.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Dimensions[0].CostKnown {
		t.Error("an unpriced trial was reported as a known cost")
	}
	if !report.CostPartial {
		t.Error("the report does not say its cost is partial")
	}
}

func TestCancellationStopsTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &plantedProvider{priced: true}

	stopAfter := 3
	report, err := Run(ctx, []Case{{
		Name: "cancelled", Dimension: "extraction", Trials: 100,
		Run: func(c context.Context) (Outcome, error) {
			if provider.calls >= stopAfter {
				cancel()
			}
			return provider.trial(c)
		},
	}}, 100)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !report.Stopped {
		t.Fatal("a cancelled run did not report itself as stopped")
	}
	if !errors.Is(report.StopReason, context.Canceled) {
		t.Errorf("StopReason = %v, want context.Canceled", report.StopReason)
	}
}

func TestTheGateRefusesWithoutBothSwitches(t *testing.T) {
	t.Run("no live flag", func(t *testing.T) {
		t.Setenv("SCHEMAFLUX_LIVE_TESTS", "")
		t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", "5")
		if _, err := Gate(); !errors.Is(err, ErrLiveDisabled) {
			t.Errorf("err = %v, want ErrLiveDisabled", err)
		}
	})

	t.Run("no budget", func(t *testing.T) {
		t.Setenv("SCHEMAFLUX_LIVE_TESTS", "1")
		t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", "")
		if _, err := Gate(); !errors.Is(err, ErrNoBudget) {
			t.Errorf("err = %v, want ErrNoBudget", err)
		}
	})

	t.Run("unparseable budget", func(t *testing.T) {
		t.Setenv("SCHEMAFLUX_LIVE_TESTS", "1")
		t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", "lots")
		if _, err := Gate(); !errors.Is(err, ErrNoBudget) {
			t.Errorf("err = %v, want ErrNoBudget", err)
		}
	})

	t.Run("zero budget", func(t *testing.T) {
		t.Setenv("SCHEMAFLUX_LIVE_TESTS", "1")
		t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", "0")
		if _, err := Gate(); !errors.Is(err, ErrNoBudget) {
			t.Errorf("err = %v, want ErrNoBudget", err)
		}
	})

	t.Run("both present", func(t *testing.T) {
		t.Setenv("SCHEMAFLUX_LIVE_TESTS", "1")
		t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", "2.50")
		budget, err := Gate()
		if err != nil {
			t.Fatalf("Gate refused a correctly configured run: %v", err)
		}
		if budget != 2.50 {
			t.Errorf("budget = %v, want 2.50", budget)
		}
	})
}

func TestLatencyPercentilesAreMeasuredValuesAndNotInterpolations(t *testing.T) {
	samples := []time.Duration{10, 20, 30, 40, 50}

	p50, p95 := percentiles(samples)

	found := func(want time.Duration) bool {
		for _, sample := range samples {
			if sample == want {
				return true
			}
		}
		return false
	}
	if !found(p50) {
		t.Errorf("p50 = %v, which is not any measured value", p50)
	}
	if !found(p95) {
		t.Errorf("p95 = %v, which is not any measured value", p95)
	}
	if p50 > p95 {
		t.Errorf("p50 %v exceeds p95 %v", p50, p95)
	}
}

package semantic

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
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

// A suite is validated whole before any paid case runs. A bad case after a good
// one used to be found only when the loop reached it, with the good one already
// paid for.
func TestASuiteIsValidatedWholeBeforeAnyTrialRuns(t *testing.T) {
	good := func(provider *plantedProvider) Case {
		return Case{Name: "good", Dimension: "extraction", Trials: 5, Run: provider.trial}
	}
	cases := []struct {
		name string
		bad  Case
		want string
	}{
		{"one trial", Case{Name: "one shot", Dimension: "d", Trials: 1, Run: func(context.Context) (Outcome, error) { return Outcome{}, nil }}, "one shot"},
		{"zero trials", Case{Name: "none", Dimension: "d", Trials: 0, Run: func(context.Context) (Outcome, error) { return Outcome{}, nil }}, "none"},
		{"negative trials", Case{Name: "negative", Dimension: "d", Trials: -4, Run: func(context.Context) (Outcome, error) { return Outcome{}, nil }}, "negative"},
		{"no Run", Case{Name: "no run", Dimension: "d", Trials: 5}, "no run"},
		{"no name", Case{Dimension: "d", Trials: 5, Run: func(context.Context) (Outcome, error) { return Outcome{}, nil }}, "name"},
		{"no dimension", Case{Name: "no dimension", Trials: 5, Run: func(context.Context) (Outcome, error) { return Outcome{}, nil }}, "no dimension"},
	}
	for _, tc := range cases {
		for position, label := range []string{"after a good case", "before a good case", "between good cases"} {
			t.Run(tc.name+" "+label, func(t *testing.T) {
				provider := &plantedProvider{priced: true, cost: 0.01}
				var suite []Case
				switch position {
				case 0:
					suite = []Case{good(provider), tc.bad}
				case 1:
					suite = []Case{tc.bad, good(provider)}
				default:
					suite = []Case{good(provider), tc.bad, good(provider)}
				}

				report, err := Run(context.Background(), suite, 100)
				if err == nil {
					t.Fatal("a suite with a bad case was run")
				}
				if provider.calls != 0 {
					t.Errorf("%d trial(s) ran before the bad case was found; a suite is validated whole first", provider.calls)
				}
				if report.SpentUSD != 0 || len(report.Dimensions) != 0 {
					t.Errorf("the refused suite reports spend or measurements: %+v", report)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("the error does not say which case was refused: %v", err)
				}
			})
		}
	}

	t.Run("an empty suite", func(t *testing.T) {
		if _, err := Run(context.Background(), nil, 100); err == nil {
			t.Error("an empty suite was accepted")
		}
	})
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
// package could contain. The trial's spend is unknown, so the run stops there:
// it used to run all ten against a ceiling of five, and every case after it.
func TestAnUnpricedTrialStopsTheRunBecauseItsSpendIsUnknown(t *testing.T) {
	provider := &plantedProvider{cost: 0, priced: false}

	report, err := Run(context.Background(), []Case{{
		Name: "unpriced", Dimension: "extraction", Trials: 10, Run: provider.trial,
	}}, 5.0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if provider.calls != 1 {
		t.Errorf("%d trials ran; the first one that could not be priced should have ended the run", provider.calls)
	}
	if !report.Stopped || !errors.Is(report.StopReason, ErrSpendUnknown) {
		t.Errorf("Stopped = %v, StopReason = %v, want a stop for ErrSpendUnknown", report.Stopped, report.StopReason)
	}
	if report.UnpricedTrials != 1 {
		t.Errorf("UnpricedTrials = %d, want 1", report.UnpricedTrials)
	}
	if report.Dimensions[0].CostKnown {
		t.Error("an unpriced trial was reported as a known cost")
	}
	if !report.CostPartial {
		t.Error("the report does not say its cost is partial")
	}
	if report.Dimensions[0].Passed.Trials != 1 {
		t.Errorf("the trial that was paid for is not in the report: %+v", report.Dimensions[0].Passed)
	}
}

// What was measured before the first unpriced trial is kept and priced, and the
// cases after the stop never run.
func TestAnUnpricedCaseAfterPricedOnesKeepsTheirMeasurementsAndSkipsTheRest(t *testing.T) {
	priced := &plantedProvider{cost: 0.5, priced: true}
	unpriced := &plantedProvider{priced: false}
	never := &plantedProvider{cost: 0.5, priced: true}

	report, err := Run(context.Background(), []Case{
		{Name: "priced", Dimension: "first", Trials: 4, Run: priced.trial},
		{Name: "unpriced", Dimension: "second", Trials: 4, Run: unpriced.trial},
		{Name: "after", Dimension: "third", Trials: 4, Run: never.trial},
	}, 100)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if priced.calls != 4 || unpriced.calls != 1 || never.calls != 0 {
		t.Errorf("trials run: %d, %d, %d; want 4, 1, 0", priced.calls, unpriced.calls, never.calls)
	}
	if report.SpentUSD != 2.0 {
		t.Errorf("SpentUSD = %v, want 2.0 from the priced trials", report.SpentUSD)
	}
	if len(report.Dimensions) != 2 || report.Dimensions[0].TotalCost != 2.0 || !report.Dimensions[0].CostKnown {
		t.Errorf("the priced dimension was not kept intact: %+v", report.Dimensions)
	}
}

// A figure the ceiling cannot add is not a price.
func TestACostThatIsNegativeOrNotANumberIsNotAPrice(t *testing.T) {
	for _, cost := range []float64{-1, -0.0001, math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(strconv.FormatFloat(cost, 'g', -1, 64), func(t *testing.T) {
			provider := &plantedProvider{cost: cost, priced: true}
			report, err := Run(context.Background(), []Case{{Name: "odd", Dimension: "d", Trials: 5, Run: provider.trial}}, 10)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if provider.calls != 1 || !errors.Is(report.StopReason, ErrSpendUnknown) {
				t.Errorf("%d trials ran, StopReason = %v; a cost of %v should end the run as unknown spend", provider.calls, report.StopReason, cost)
			}
			if report.SpentUSD != 0 || math.IsNaN(report.SpentUSD) {
				t.Errorf("SpentUSD = %v; the odd figure was added to the total", report.SpentUSD)
			}
		})
	}
}

func TestACeilingThatIsNotAFinitePositiveNumberRunsNothing(t *testing.T) {
	for _, ceiling := range []float64{0, -1, -0.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(strconv.FormatFloat(ceiling, 'g', -1, 64), func(t *testing.T) {
			provider := &plantedProvider{cost: 1, priced: true}
			_, err := Run(context.Background(), []Case{{Name: "any", Dimension: "d", Trials: 10, Run: provider.trial}}, ceiling)
			if !errors.Is(err, ErrNoBudget) {
				t.Errorf("err = %v, want ErrNoBudget", err)
			}
			if provider.calls != 0 {
				t.Errorf("the provider was called %d time(s) under a ceiling of %v", provider.calls, ceiling)
			}
		})
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

	// "inf" is a plausible way to write "no limit", and NaN and infinity both
	// parse without an error. Neither can be reached or crossed by a comparison,
	// so either would have run the suite with no ceiling at all.
	for _, raw := range []string{"inf", "Inf", "+Inf", "-Inf", "Infinity", "+infinity", "NaN", "nan", "1e999", "-1e999", "-5", "-0", "0.0", " "} {
		t.Run("budget "+raw, func(t *testing.T) {
			t.Setenv("SCHEMAFLUX_LIVE_TESTS", "1")
			t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", raw)
			budget, err := Gate()
			if !errors.Is(err, ErrNoBudget) {
				t.Errorf("Gate() = %v, %v for a budget of %q; want ErrNoBudget", budget, err, raw)
			}
			if budget != 0 {
				t.Errorf("a refusal came with a ceiling of %v", budget)
			}
		})
	}

	t.Run("the live flag must be exactly 1", func(t *testing.T) {
		for _, flag := range []string{"true", "0", "yes", " 1", "1 "} {
			t.Setenv("SCHEMAFLUX_LIVE_TESTS", flag)
			t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", "5")
			if _, err := Gate(); !errors.Is(err, ErrLiveDisabled) {
				t.Errorf("flag %q: err = %v, want ErrLiveDisabled", flag, err)
			}
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

func TestPricedAcceptsOnlyAFiniteNonNegativeFigureTheCaseSaidWasPriced(t *testing.T) {
	cases := []struct {
		name    string
		outcome Outcome
		want    bool
	}{
		{"priced and positive", Outcome{Priced: true, Cost: 0.5}, true},
		{"priced and zero, which is a free model", Outcome{Priced: true, Cost: 0}, true},
		{"priced and tiny", Outcome{Priced: true, Cost: 1e-12}, true},
		{"priced and large", Outcome{Priced: true, Cost: 1e9}, true},
		{"not priced", Outcome{Priced: false, Cost: 0.5}, false},
		{"not priced and zero", Outcome{}, false},
		{"priced and negative", Outcome{Priced: true, Cost: -0.01}, false},
		{"priced and negative zero", Outcome{Priced: true, Cost: math.Copysign(0, -1)}, true},
		{"priced and NaN", Outcome{Priced: true, Cost: math.NaN()}, false},
		{"priced and positive infinity", Outcome{Priced: true, Cost: math.Inf(1)}, false},
		{"priced and negative infinity", Outcome{Priced: true, Cost: math.Inf(-1)}, false},
		{"errored and priced", Outcome{Errored: true, Priced: true, Cost: 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := priced(tc.outcome); got != tc.want {
				t.Errorf("priced(%+v) = %v, want %v", tc.outcome, got, tc.want)
			}
		})
	}
}

package semantic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// RC-002's spend rule, and the reason this file has a gate before it has a
// runner: this suite calls a real provider many times per case, on purpose,
// because a rate measured over one trial is not a rate. That makes it the most
// expensive thing in this repository and the easiest one to start by accident.
//
// AGENTS.md's rule is "never spend money by accident". Two independent switches
// are required, and neither has a default that spends:
//
//   - SCHEMAFLUX_LIVE_TESTS=1, the same gate every other provider-backed test
//     in this repository uses, so a plain `go test ./...` cannot reach a
//     provider from here;
//   - SCHEMAFLUX_SEMANTIC_BUDGET_USD, an explicit ceiling in dollars. There is
//     no default. A suite that runs with an unstated budget is a suite that
//     discovers its budget from the invoice.
//
// The ceiling is enforced *before* each call rather than reported after the
// run, and exceeding it aborts with the partial results intact — an aborted run
// that reports what it measured is more useful than one that discards the
// evidence it already paid for.

// ErrLiveDisabled is returned when the suite is asked to run without the live
// gate set.
var ErrLiveDisabled = errors.New("semantic: SCHEMAFLUX_LIVE_TESTS=1 is required; this suite calls a real provider")

// ErrNoBudget is returned when no spend ceiling was declared.
var ErrNoBudget = errors.New("semantic: SCHEMAFLUX_SEMANTIC_BUDGET_USD must be set to an explicit ceiling; there is no default")

// ErrBudgetExhausted is returned when the declared ceiling is reached mid-run.
var ErrBudgetExhausted = errors.New("semantic: spend ceiling reached; the run was stopped")

// Gate reports whether the suite may run, and the ceiling it may spend up to.
//
// It returns an error rather than a bool because the two ways to be blocked
// need different fixes, and "the semantic suite did not run" with no reason is
// how a release process quietly stops verifying anything.
func Gate() (budgetUSD float64, err error) {
	if os.Getenv("SCHEMAFLUX_LIVE_TESTS") != "1" {
		return 0, ErrLiveDisabled
	}

	raw := os.Getenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD")
	if raw == "" {
		return 0, ErrNoBudget
	}

	budget, parseErr := strconv.ParseFloat(raw, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("%w: %q is not a number", ErrNoBudget, raw)
	}
	if budget <= 0 {
		return 0, fmt.Errorf("%w: a ceiling of %v permits nothing", ErrNoBudget, budget)
	}
	return budget, nil
}

// Outcome is what one trial produced, reduced to the facts a rate can be built
// from. It deliberately carries no answer text: the suite measures whether an
// answer satisfied its case's check, and storing the answers would turn a
// metrics run into a corpus of provider output nobody agreed to retain.
type Outcome struct {
	// Passed is the case's own check applied to this trial's answer.
	Passed bool

	// Errored means the call itself failed — a timeout, a refusal, a malformed
	// body. Kept separate from Passed because "the model got it wrong" and "the
	// call never happened" are different findings, and folding an error into a
	// failure makes an outage look like a quality regression.
	Errored bool

	// Cost and Elapsed are measured per trial. Cost is what the envelope
	// reported; when the model is unpriced it is zero and Priced is false, and
	// the run's total is reported as unknown rather than as a small number.
	Cost    float64
	Priced  bool
	Elapsed time.Duration
	Tokens  int
}

// Case is one thing the suite measures. Check is the case's own judgement of an
// answer — an operation-specific predicate, because "correct" means something
// different for extraction than for ranking, and a single shared notion of
// correctness would be wrong for all of them.
type Case struct {
	// Name identifies the case in a report. It never contains input data.
	Name string

	// Dimension groups cases into the categories RC-002 lists: extraction
	// accuracy, hallucination, classification abstention, choose/filter
	// precision, ranking id coverage, repair success, injection resistance.
	Dimension string

	// Trials is how many times to run this case. A case with Trials of 1 is a
	// case whose result cannot be trusted, and Run says so rather than
	// silently reporting a 0% or 100% rate from a single sample.
	Trials int

	// Run performs one trial. It returns the outcome, and an error only for a
	// failure of the harness itself — a provider error belongs in
	// Outcome.Errored, where it can be counted.
	Run func(ctx context.Context) (Outcome, error)
}

// DimensionResult is what the suite reports for one dimension.
type DimensionResult struct {
	Dimension string

	// Passed counts trials whose answer satisfied its case's check, over the
	// trials that actually reached a provider. Errored trials are excluded
	// from the denominator and counted separately, so an outage lowers the
	// confidence in a rate rather than lowering the rate.
	Passed Proportion

	// Errors counts calls that did not complete, over every trial attempted.
	Errors Proportion

	// TotalCost is the summed cost of the trials in this dimension.
	// CostKnown is false when any trial was unpriced, in which case TotalCost
	// is the sum of the priced ones and is a floor, not a total.
	TotalCost float64
	CostKnown bool

	// P50 and P95 are latency order statistics over completed trials.
	P50, P95 time.Duration

	TotalTokens int
}

// Report is the whole run.
type Report struct {
	Dimensions []DimensionResult

	// Stopped is set when the spend ceiling ended the run early. The results
	// above are still what was measured; they are simply incomplete, and a
	// reader has to know which.
	Stopped     bool
	StopReason  error
	SpentUSD    float64
	CeilingUSD  float64
	CostPartial bool
}

// Run executes the cases and reports the rates. It does not decide whether the
// run passed: thresholds belong to the caller, because what counts as
// acceptable extraction accuracy is a product decision and not a property of
// the measurement.
//
// The ceiling is checked before each trial. A run that would exceed it stops
// and returns what it has, with Stopped set — never a partial report that looks
// complete.
func Run(ctx context.Context, cases []Case, ceilingUSD float64) (Report, error) {
	if ceilingUSD <= 0 {
		return Report{}, ErrNoBudget
	}

	report := Report{CeilingUSD: ceilingUSD}
	byDimension := map[string]*DimensionResult{}
	latencies := map[string][]time.Duration{}
	order := []string{}

	for _, testCase := range cases {
		if testCase.Trials < 2 {
			return report, fmt.Errorf(
				"semantic: case %q declares %d trial(s); a rate needs repeated trials, and a single sample reports 0%% or 100%% regardless of the model",
				testCase.Name, testCase.Trials)
		}

		result, seen := byDimension[testCase.Dimension]
		if !seen {
			result = &DimensionResult{Dimension: testCase.Dimension, CostKnown: true}
			byDimension[testCase.Dimension] = result
			order = append(order, testCase.Dimension)
		}

		for trial := 0; trial < testCase.Trials; trial++ {
			if report.SpentUSD >= ceilingUSD {
				report.Stopped = true
				report.StopReason = fmt.Errorf("%w: spent %.4f of %.4f", ErrBudgetExhausted, report.SpentUSD, ceilingUSD)
				return finish(report, order, byDimension, latencies), nil
			}
			if err := ctx.Err(); err != nil {
				report.Stopped = true
				report.StopReason = err
				return finish(report, order, byDimension, latencies), nil
			}

			outcome, err := testCase.Run(ctx)
			if err != nil {
				return finish(report, order, byDimension, latencies),
					fmt.Errorf("semantic: case %q trial %d: harness failure: %w", testCase.Name, trial, err)
			}

			result.Errors.Trials++
			if outcome.Errored {
				result.Errors.Successes++
				continue
			}

			result.Passed.Trials++
			if outcome.Passed {
				result.Passed.Successes++
			}
			result.TotalTokens += outcome.Tokens
			if outcome.Priced {
				result.TotalCost += outcome.Cost
				report.SpentUSD += outcome.Cost
			} else {
				// One unpriced trial makes the dimension's total a floor. It is
				// not rounded up to a guess and not reported as complete.
				result.CostKnown = false
				report.CostPartial = true
			}
			latencies[testCase.Dimension] = append(latencies[testCase.Dimension], outcome.Elapsed)
		}
	}

	return finish(report, order, byDimension, latencies), nil
}

func finish(report Report, order []string, byDimension map[string]*DimensionResult,
	latencies map[string][]time.Duration) Report {

	for _, dimension := range order {
		result := byDimension[dimension]
		result.P50, result.P95 = percentiles(latencies[dimension])
		report.Dimensions = append(report.Dimensions, *result)
	}
	return report
}

// percentiles returns the p50 and p95 of a sample, or zeroes for an empty one.
// Nearest-rank rather than interpolated: interpolating between two measured
// latencies produces a number no request ever took, which is a small lie this
// package has no reason to tell.
func percentiles(samples []time.Duration) (p50, p95 time.Duration) {
	if len(samples) == 0 {
		return 0, 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	rank := func(fraction float64) time.Duration {
		index := int(float64(len(sorted))*fraction+0.5) - 1
		if index < 0 {
			index = 0
		}
		if index >= len(sorted) {
			index = len(sorted) - 1
		}
		return sorted[index]
	}
	return rank(0.50), rank(0.95)
}

// OutcomeFromMeta builds an Outcome from a result envelope and the case's own
// verdict, so a case author writes the predicate and nothing else.
//
// Cost is taken from the envelope's own pricing quality rather than from the
// presence of a number: an unpriced model reports a cost of zero, and treating
// that as "this trial was free" is how a spend ceiling gets silently disabled.
func OutcomeFromMeta(meta types.Meta, passed bool, elapsed time.Duration) Outcome {
	return Outcome{
		Passed:  passed,
		Cost:    meta.Cost.TotalCost,
		Priced:  meta.Cost.Quality.Known() && meta.Cost.Quality != types.PricingUnknown,
		Elapsed: elapsed,
		Tokens:  meta.Usage.TotalTokens,
	}
}

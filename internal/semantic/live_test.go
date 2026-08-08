package semantic

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
)

// The live entry point. It compiles on every build and runs on almost none,
// which is the arrangement AGENTS.md requires for anything that can spend:
// compiled always, so it cannot rot into something that no longer builds and
// gets discovered at release time; run only behind two explicit switches, so a
// plain `go test ./...` cannot reach a provider from here.
//
// Everything this measures is a rate over repeated trials against a real model.
// It is the only file in this repository that is *supposed* to cost money, and
// the ceiling is the caller's, declared in SCHEMAFLUX_SEMANTIC_BUDGET_USD.
//
// To run it:
//
//	SCHEMAFLUX_LIVE_TESTS=1 SCHEMAFLUX_SEMANTIC_BUDGET_USD=2.00 \
//	  go test ./internal/semantic/ -run TestSemanticRegressionSuite -v
//
// SCHEMAFLUX_SEMANTIC_TRIALS overrides the trial count. The default is
// deliberately small enough to be affordable and, just as deliberately, small
// enough that most thresholds will NOT clear their interval -- the suite says
// how many trials would, rather than pretending a cheap run proved something.

func TestSemanticRegressionSuite(t *testing.T) {
	budget, err := Gate()
	if err != nil {
		// Skipped, not failed. A gate that fails the build when it is switched
		// off is a gate somebody switches off permanently.
		t.Skipf("semantic regression suite not run: %v", err)
	}

	trials := 10
	if raw := os.Getenv("SCHEMAFLUX_SEMANTIC_TRIALS"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 2 {
			t.Fatalf("SCHEMAFLUX_SEMANTIC_TRIALS=%q is not a trial count of at least 2", raw)
		}
		trials = parsed
	}

	report, err := Run(context.Background(), Cases(trials), budget)
	if err != nil {
		t.Fatalf("suite: %v", err)
	}

	for _, dimension := range report.Dimensions {
		low, high := dimension.Passed.Interval()
		t.Logf("%-30s pass %d/%d (%.3f, 95%% CI [%.3f, %.3f])  errors %d/%d  p50 %v p95 %v  cost %.4f%s",
			dimension.Dimension,
			dimension.Passed.Successes, dimension.Passed.Trials, dimension.Passed.Rate(), low, high,
			dimension.Errors.Successes, dimension.Errors.Trials,
			dimension.P50, dimension.P95,
			dimension.TotalCost, costNote(dimension.CostKnown))
	}

	t.Logf("spent %.4f of a %.4f ceiling", report.SpentUSD, report.CeilingUSD)

	if report.Stopped {
		// Not a failure: an incomplete measurement is not a failed one, and
		// calling it a failure would teach people to raise the ceiling to make
		// a red build green.
		t.Logf("INCOMPLETE: %v -- the rates above cover only the trials that ran", report.StopReason)
	}
	if report.CostPartial {
		t.Log("cost totals are a FLOOR, not a total: some trials used an unpriced model")
	}

	// The suite reports; it does not gate. Thresholds are a product decision
	// and belong to whoever cuts the release, not to the measurement -- and a
	// threshold hard-coded here would be tuned until it passed.
	//
	// What IS asserted is that the run produced usable evidence at all. A suite
	// that measured nothing must not look like a suite that passed.
	if len(report.Dimensions) == 0 {
		t.Fatal("the suite produced no measurements")
	}
	for _, dimension := range report.Dimensions {
		if !dimension.Passed.Known() && !dimension.Errors.Known() {
			t.Errorf("%s: no trials completed", dimension.Dimension)
		}
	}
}

func costNote(known bool) string {
	if known {
		return ""
	}
	return " (floor; unpriced trials)"
}

// The gate itself is checked without spending anything, on every ordinary run.
// This is the test that would catch the suite quietly becoming runnable by
// default -- the failure mode that costs money rather than time.
func TestTheLiveSuiteCannotRunByAccident(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LIVE_TESTS", "")
	t.Setenv("SCHEMAFLUX_SEMANTIC_BUDGET_USD", "")

	if _, err := Gate(); err == nil {
		t.Fatal("the gate opened with neither switch set")
	}

	// And with the live flag alone, which is the plausible half-configuration:
	// somebody exports SCHEMAFLUX_LIVE_TESTS for the rest of the live tests and
	// this one starts spending an undeclared amount.
	t.Setenv("SCHEMAFLUX_LIVE_TESTS", "1")
	if _, err := Gate(); !errors.Is(err, ErrNoBudget) {
		t.Fatalf("with the live flag alone the gate returned %v; it must still demand a ceiling", err)
	}
}

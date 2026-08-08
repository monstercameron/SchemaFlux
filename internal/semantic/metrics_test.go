package semantic

import (
	"math"
	"testing"
)

// These check the statistics, and they matter more than they look. Every number
// this suite reports about a model is derived here, so an error in this file is
// an error in every release decision made from the suite's output — and unlike
// a wrong answer from a model, nothing downstream would ever contradict it.

func TestNoTrialsIsUnknownAndNotZero(t *testing.T) {
	var nothing Proportion

	if nothing.Known() {
		t.Error("an unmeasured proportion reports itself as known")
	}
	if !math.IsNaN(nothing.Rate()) {
		t.Errorf("Rate = %v for zero trials; a zero there reads as total failure", nothing.Rate())
	}
	low, high := nothing.Interval()
	if !math.IsNaN(low) || !math.IsNaN(high) {
		t.Errorf("Interval = [%v, %v] for zero trials", low, high)
	}
	if nothing.MeetsAtLeast(0) {
		t.Error("an unmeasured proportion cleared a threshold of zero; nothing measured cannot clear anything")
	}
	if nothing.MeetsAtMost(1) {
		t.Error("an unmeasured proportion satisfied an at-most bound")
	}
}

// The specific reason Wilson was chosen over the normal approximation: the
// textbook interval claims certainty from a perfect small sample.
func TestAPerfectSmallSampleDoesNotClaimCertainty(t *testing.T) {
	perfect := Proportion{Successes: 10, Trials: 10}

	low, high := perfect.Interval()
	if low >= 1.0 {
		t.Errorf("interval lower bound is %v; ten out of ten is not proof of a 100%% rate", low)
	}
	if high > 1.0 {
		t.Errorf("interval upper bound is %v, above 1.0", high)
	}
	if low <= 0 {
		t.Errorf("interval lower bound is %v; ten out of ten is strong evidence of something", low)
	}
}

func TestIntervalsStayInsideZeroAndOne(t *testing.T) {
	cases := []Proportion{
		{Successes: 0, Trials: 1},
		{Successes: 1, Trials: 1},
		{Successes: 0, Trials: 1000},
		{Successes: 1000, Trials: 1000},
		{Successes: 1, Trials: 3},
		{Successes: 500, Trials: 1000},
	}

	for _, proportion := range cases {
		low, high := proportion.Interval()
		if low < 0 || low > 1 || high < 0 || high > 1 {
			t.Errorf("%d/%d gave the interval [%v, %v], outside [0, 1]",
				proportion.Successes, proportion.Trials, low, high)
		}
		if low > high {
			t.Errorf("%d/%d gave an inverted interval [%v, %v]",
				proportion.Successes, proportion.Trials, low, high)
		}
	}
}

// More evidence narrows the interval. If this ever stopped being true the
// suite would be reporting confidence unrelated to sample size.
func TestMoreTrialsNarrowTheInterval(t *testing.T) {
	small := Proportion{Successes: 8, Trials: 10}
	large := Proportion{Successes: 800, Trials: 1000}

	if math.Abs(small.Rate()-large.Rate()) > 1e-9 {
		t.Fatal("precondition: both should have the same point estimate")
	}

	smallLow, smallHigh := small.Interval()
	largeLow, largeHigh := large.Interval()

	if (largeHigh - largeLow) >= (smallHigh - smallLow) {
		t.Errorf("1000 trials gave an interval of width %v, no narrower than 10 trials' %v",
			largeHigh-largeLow, smallHigh-smallLow)
	}
}

// The gating decision, and the reason the interval exists at all: the same
// point estimate passes or fails depending on how much evidence stands behind
// it.
func TestTheSameRateWithMoreEvidenceIsWhatClearsAThreshold(t *testing.T) {
	const threshold = 0.75

	small := Proportion{Successes: 8, Trials: 10}
	large := Proportion{Successes: 800, Trials: 1000}

	if small.MeetsAtLeast(threshold) {
		t.Error("8 out of 10 cleared a 75% threshold; the point estimate was compared, not the interval")
	}
	if !large.MeetsAtLeast(threshold) {
		t.Error("800 out of 1000 did not clear a 75% threshold despite ample evidence")
	}
}

func TestAtMostIsTheMirrorForRatesThatMustStayLow(t *testing.T) {
	// A hallucination rate: 2 in 1000 should clear a 1% ceiling; 2 in 10
	// should not, being the same point estimate on far less evidence.
	strong := Proportion{Successes: 2, Trials: 1000}
	weak := Proportion{Successes: 2, Trials: 10}

	if !strong.MeetsAtMost(0.01) {
		t.Error("2 in 1000 did not clear a 1% ceiling")
	}
	if weak.MeetsAtMost(0.01) {
		t.Error("2 in 10 cleared a 1% ceiling; the interval was not consulted")
	}
}

// Sampling noise must not read as a regression, or the suite is ignored within
// a month.
func TestOverlappingIntervalsAreNotARegression(t *testing.T) {
	baseline := Proportion{Successes: 90, Trials: 100}
	candidate := Proportion{Successes: 88, Trials: 100}

	if candidate.Regressed(baseline, 0.0) {
		t.Error("88/100 against a 90/100 baseline was called a regression; that is noise, not a change")
	}
}

func TestARealDropIsCalledARegression(t *testing.T) {
	baseline := Proportion{Successes: 950, Trials: 1000}
	candidate := Proportion{Successes: 700, Trials: 1000}

	if !candidate.Regressed(baseline, 0.0) {
		t.Error("a drop from 95% to 70% over a thousand trials was not called a regression")
	}
}

func TestAnUnmeasuredSideIsNeverARegression(t *testing.T) {
	measured := Proportion{Successes: 10, Trials: 100}

	if measured.Regressed(Proportion{}, 0) {
		t.Error("a regression was reported against a baseline that measured nothing")
	}
	if (Proportion{}).Regressed(measured, 0) {
		t.Error("an unmeasured candidate was called a regression")
	}
}

func TestNeededTrialsSaysWhatWouldBeEnough(t *testing.T) {
	needed := NeededTrials(0.80, 0.75)
	if needed <= 0 {
		t.Fatalf("NeededTrials = %d for a rate above its threshold", needed)
	}

	// The answer has to actually work, or it is advice that wastes money.
	achieved := Proportion{Successes: int(math.Round(0.80 * float64(needed))), Trials: needed}
	if !achieved.MeetsAtLeast(0.75) {
		t.Errorf("NeededTrials said %d trials would suffice, and %d/%d does not clear the threshold",
			needed, achieved.Successes, achieved.Trials)
	}

	// One fewer should not, or the estimate is not tight and is overspending.
	if needed > 2 {
		fewer := needed - 1
		short := Proportion{Successes: int(math.Round(0.80 * float64(fewer))), Trials: fewer}
		if short.MeetsAtLeast(0.75) {
			t.Errorf("NeededTrials said %d but %d already suffices; the estimate overspends", needed, fewer)
		}
	}
}

func TestNoNumberOfTrialsRescuesAWorseModel(t *testing.T) {
	if got := NeededTrials(0.70, 0.75); got != 0 {
		t.Errorf("NeededTrials = %d for a rate below its threshold; no sample size makes a worse model pass", got)
	}
	if got := NeededTrials(0.75, 0.75); got != 0 {
		t.Errorf("NeededTrials = %d for a rate exactly at its threshold", got)
	}
}

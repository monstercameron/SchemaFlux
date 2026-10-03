// Package semantic is the release-candidate regression suite: RC-002.
//
// The distinction that makes this package worth having is stated in its own
// task, and it is easy to get wrong: **a single exact-output assertion is not a
// stable live test.** A model asked the same question twice may answer
// differently and be right both times, so a suite built on equality is a suite
// that fails for reasons unrelated to quality. What is stable is a *rate*
// measured over repeated trials, reported with an interval that says how much
// the number can be trusted.
//
// So nothing here asserts an output. Everything here counts outcomes and
// reports a proportion with a confidence interval, and a threshold is compared
// against the interval rather than the point estimate — because 8 successes out
// of 10 and 800 out of 1000 are the same point estimate and not remotely the
// same evidence.
package semantic

import "math"

// Proportion is a measured rate: how many trials succeeded out of how many ran,
// with an interval around the estimate.
//
// It is a struct rather than a float because a bare float invites exactly the
// mistake this package exists to prevent — comparing 0.8 from ten trials
// against a 0.75 threshold and calling it a pass.
type Proportion struct {
	// Successes and Trials are counts of things that actually happened. They
	// are the only two numbers in this type that are measured; everything else
	// is derived from them.
	Successes int
	Trials    int
}

// valid reports whether the counts can be counts of one measurement: neither is
// negative, and no more trials succeeded than ran. A Proportion that is not valid
// measured nothing, and every derived number of it is NaN, as for no trials: a
// rate above 1 and an interval built from the square root of a negative would
// otherwise read as findings.
func (p Proportion) valid() bool {
	return p.Successes >= 0 && p.Trials >= 0 && p.Successes <= p.Trials
}

// Rate is the point estimate. It is NaN for zero trials rather than zero: no
// trials is not a rate of nothing, it is the absence of a measurement, and a
// zero here would read as total failure. It is NaN for counts that cannot be
// a measurement, for the same reason.
func (p Proportion) Rate() float64 {
	if p.Trials == 0 || !p.valid() {
		return math.NaN()
	}
	return float64(p.Successes) / float64(p.Trials)
}

// Known reports whether anything was measured at all.
func (p Proportion) Known() bool { return p.Trials > 0 && p.valid() }

// Interval returns the Wilson score interval at roughly 95% confidence.
//
// Wilson rather than the textbook normal approximation, for a reason that
// matters at this suite's sample sizes: the normal approximation gives
// nonsensical intervals near 0 and 1 — 10 successes out of 10 yields the
// interval [1.0, 1.0], claiming certainty from ten trials — and those are
// precisely the regions a passing quality suite lives in. Wilson stays inside
// [0, 1] and stays honest about small samples.
//
// Returns (NaN, NaN) with no trials, or counts that cannot be a measurement, for
// the same reason Rate does.
func (p Proportion) Interval() (low, high float64) {
	if p.Trials == 0 || !p.valid() {
		return math.NaN(), math.NaN()
	}

	const z = 1.96 // ~95%

	n := float64(p.Trials)
	phat := float64(p.Successes) / n

	denominator := 1 + z*z/n
	center := phat + z*z/(2*n)
	spread := z * math.Sqrt(phat*(1-phat)/n+z*z/(4*n*n))

	low = (center - spread) / denominator
	high = (center + spread) / denominator

	return math.Max(0, low), math.Min(1, high)
}

// MeetsAtLeast reports whether this proportion is at least `threshold`, judged
// against the interval rather than the point estimate: the whole interval must
// clear the threshold.
//
// This is deliberately the conservative direction. A suite that gates a release
// should refuse to certify a rate it has not measured well enough to defend,
// and "the point estimate cleared it" is not that. The cost is that a genuinely
// good model needs enough trials to prove it — which is the correct cost, and
// is why NeededTrials exists to say how many.
//
// With no trials this is false: nothing measured cannot clear anything.
func (p Proportion) MeetsAtLeast(threshold float64) bool {
	low, _ := p.Interval()
	if math.IsNaN(low) {
		return false
	}
	return low >= threshold
}

// MeetsAtMost is the mirror for rates that must stay low — hallucination,
// regression after repair. The whole interval must sit below the threshold.
func (p Proportion) MeetsAtMost(threshold float64) bool {
	_, high := p.Interval()
	if math.IsNaN(high) {
		return false
	}
	return high <= threshold
}

// Regressed compares this proportion against a baseline and reports whether it
// is worse by more than `tolerance`, judged so that ordinary sampling noise
// does not read as a regression.
//
// The comparison is between this measurement's upper bound and the baseline's
// point estimate: a candidate is called a regression only when even its
// optimistic reading falls short. Two overlapping intervals are not evidence of
// a change, and a release process that treats them as one will be ignored
// within a month.
func (p Proportion) Regressed(baseline Proportion, tolerance float64) bool {
	if !p.Known() || !baseline.Known() {
		return false
	}
	_, high := p.Interval()
	return high < baseline.Rate()-tolerance
}

// maxTrialsConsidered is the largest sample size NeededTrials and TrialsNeeded
// look at. Past it the answer is "more than this", and a number of trials that
// large is not a plan anybody can run against a paid model.
const maxTrialsConsidered = 65536

// TrialsNeeded reports roughly how many trials it would take for a measurement
// at rate `expected` to clear `threshold` under MeetsAtLeast, and whether any
// number of them would.
//
// It exists so that a suite reporting "not enough evidence" can say what would
// be enough, rather than leaving somebody to add trials by trial and error. It
// is an estimate of a sample size, not a measurement of anything, which is why
// it is named for what it is.
//
// It is not reachable, and returns (0, false), in three different situations:
// the expected rate is at or below the threshold, so no number of trials makes a
// genuinely worse model pass; the rate is so close above the threshold that it
// takes more than 65,536 trials; or an argument is not a number between 0 and 1.
func TrialsNeeded(expected, threshold float64) (trials int, reachable bool) {
	// NaN is refused here and not left to the search: converting a NaN to a count
	// is implementation-defined, and on a platform where it gives 0 a threshold of
	// zero would be cleared by it.
	for _, value := range []float64{expected, threshold} {
		if math.IsNaN(value) || value < 0 || value > 1 {
			return 0, false
		}
	}
	if expected <= threshold {
		return 0, false
	}
	for n := 1; n <= maxTrialsConsidered; n *= 2 {
		candidate := Proportion{Successes: int(math.Round(expected * float64(n))), Trials: n}
		if candidate.MeetsAtLeast(threshold) {
			// Narrow down within the last doubling rather than reporting the
			// power of two, which would routinely overstate by ~2x.
			for exact := n / 2; exact <= n; exact++ {
				try := Proportion{Successes: int(math.Round(expected * float64(exact))), Trials: exact}
				if try.MeetsAtLeast(threshold) {
					return exact, true
				}
			}
			return n, true
		}
	}
	return 0, false
}

// NeededTrials is TrialsNeeded without the second answer: it returns 0 when no
// number of trials would do, which is also what it returns for a rate at or
// below the threshold and for one that needs more than 65,536. Use TrialsNeeded
// to tell the cases apart.
func NeededTrials(expected, threshold float64) int {
	trials, _ := TrialsNeeded(expected, threshold)
	return trials
}

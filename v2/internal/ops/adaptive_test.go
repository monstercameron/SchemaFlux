package ops

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
)

// A provider that truncates above 20 items, driven for ten rounds. The state
// grows by a quarter after three compliant chunks and halves on a truncation,
// so it does not settle: it probes upward again after each failure. The test
// pins the whole trace, which shows that, and that a size above 20 is only ever
// followed by a truncation and never counted as compliant.
func TestAdaptiveChunkStateHalvesOnTruncationAndProbesAgain(t *testing.T) {
	state := NewAdaptiveChunkState(25, 200)

	var trace []int
	for i := 0; i < 10; i++ {
		if state.Size() > 20 {
			state.Record(OutcomeTruncated)
		} else {
			state.Record(OutcomeCompliant)
			state.Record(OutcomeCompliant)
			state.Record(OutcomeCompliant)
		}
		trace = append(trace, state.Size())
	}

	want := []int{12, 15, 18, 22, 11, 13, 16, 20, 25, 12}
	if fmt.Sprint(trace) != fmt.Sprint(want) {
		t.Fatalf("sizes after each round = %v, want %v", trace, want)
	}
	if state.Reason() == "" {
		t.Fatal("Reason() is empty; the state has to say why its size is what it is")
	}

	// Still reactive: one more truncation at a size above the provider's limit
	// halves it again.
	before := state.Size()
	state.Record(OutcomeTruncated)
	if state.Size() >= before {
		t.Fatalf("a truncation did not reduce size: before %d, after %d", before, state.Size())
	}
}

func TestAdaptiveChunkStateReasonAtTheCeiling(t *testing.T) {
	state := NewAdaptiveChunkState(10, 12)
	for i := 0; i < 50; i++ {
		state.Record(OutcomeCompliant)
	}
	if state.Size() != 12 {
		t.Fatalf("size = %d, want the ceiling of 12", state.Size())
	}
	if want := "compliant, and already at the ceiling of 12"; state.Reason() != want {
		t.Errorf("Reason = %q, want %q: a run counter past its limit reads as nonsense", state.Reason(), want)
	}
	// A failure at the ceiling halves as usual, and growth needs a full run of
	// three again.
	state.Record(OutcomeOmission)
	if state.Size() != 6 {
		t.Fatalf("size = %d after an omission at 12, want 6", state.Size())
	}
	state.Record(OutcomeCompliant)
	state.Record(OutcomeCompliant)
	if state.Size() != 6 {
		t.Fatalf("size = %d after two compliant chunks, want growth to wait for the third", state.Size())
	}
	state.Record(OutcomeCompliant)
	if state.Size() != 7 {
		t.Fatalf("size = %d after three compliant chunks, want 7", state.Size())
	}
}

func TestAdaptiveChunkStateAtTheMinimumSize(t *testing.T) {
	state := NewAdaptiveChunkState(1, 8)
	for _, outcome := range []AdaptiveOutcome{OutcomeTruncated, OutcomeOmission, OutcomeDuplicateID, OutcomeMalformedProtocol} {
		state.Record(outcome)
		if state.Size() != 1 {
			t.Fatalf("size = %d after %s at size 1", state.Size(), outcome)
		}
		if want := "kept at the minimum size of 1 after " + outcome.String(); state.Reason() != want {
			t.Errorf("Reason = %q, want %q: it used to read \"halved from 1 to 1\"", state.Reason(), want)
		}
	}
}

func TestAdaptiveChunkStateIgnoresAnOutcomeItDoesNotKnow(t *testing.T) {
	state := NewAdaptiveChunkState(8, 32)
	state.Record(OutcomeCompliant)
	state.Record(OutcomeCompliant)
	state.Record(AdaptiveOutcome(99))
	state.Record(AdaptiveOutcome(255))
	if state.Size() != 8 {
		t.Fatalf("size = %d, want an unknown outcome to leave it unchanged", state.Size())
	}
	if !strings.Contains(state.Reason(), "ignored") {
		t.Errorf("Reason = %q, want it to say the outcome was ignored", state.Reason())
	}
	// The unknown outcomes did not reset the run of two compliant chunks.
	state.Record(OutcomeCompliant)
	if state.Size() <= 8 {
		t.Errorf("size = %d: the third compliant chunk should have grown it", state.Size())
	}
}

func TestNewAdaptiveChunkStateBoundsItsStart(t *testing.T) {
	for _, tc := range []struct{ start, ceiling, want int }{
		{5, 10, 5}, {20, 10, 10}, {0, 10, 1}, {-3, 10, 1}, {5, 0, 1}, {5, -2, 1}, {1, 1, 1}, {100, 100, 100},
	} {
		if got := NewAdaptiveChunkState(tc.start, tc.ceiling).Size(); got != tc.want {
			t.Errorf("NewAdaptiveChunkState(%d, %d).Size() = %d, want %d", tc.start, tc.ceiling, got, tc.want)
		}
	}
}

func TestAdaptiveChunkStateGrowsOnSustainedCompliance(t *testing.T) {
	state := NewAdaptiveChunkState(4, 32)
	for i := 0; i < 3; i++ {
		state.Record(OutcomeCompliant)
	}
	if state.Size() <= 4 {
		t.Fatalf("size = %d after 3 consecutive compliant chunks, want growth above the start of 4", state.Size())
	}
	if state.Size() > 32 {
		t.Fatalf("size = %d exceeds the operation's own ceiling of 32", state.Size())
	}
}

func TestAdaptiveChunkStateNeverExceedsDeterministicMax(t *testing.T) {
	state := NewAdaptiveChunkState(10, 12)
	for i := 0; i < 50; i++ {
		state.Record(OutcomeCompliant)
	}
	if state.Size() > 12 {
		t.Fatalf("size = %d, exceeded the deterministic max of 12", state.Size())
	}
}

func TestAdaptiveChunkStateHalvesAfterSustainedRepairs(t *testing.T) {
	state := NewAdaptiveChunkState(16, 64)
	for i := 0; i < RepairRateThreshold; i++ {
		state.Record(OutcomeRepaired)
	}
	if state.Size() != 8 {
		t.Fatalf("size = %d after %d consecutive repairs, want 8 (halved from 16)", state.Size(), RepairRateThreshold)
	}
}

func TestAdaptiveChunkStateSingleRepairDoesNotHalve(t *testing.T) {
	state := NewAdaptiveChunkState(16, 64)
	state.Record(OutcomeRepaired)
	if state.Size() != 16 {
		t.Fatalf("size = %d after one repair, want unchanged at 16 (below RepairRateThreshold)", state.Size())
	}
}

// Each of the two runs counts consecutive outcomes of its own kind, so any
// other outcome has to break it: a run that survives an interruption grows or
// halves the size one chunk early.
func TestAdaptiveChunkStateRunsAreBrokenByTheRightOutcomes(t *testing.T) {
	compliant, repaired := OutcomeCompliant, OutcomeRepaired
	cases := []struct {
		name     string
		outcomes []AdaptiveOutcome
		want     int
	}{
		{"a repair breaks the compliant run", []AdaptiveOutcome{compliant, compliant, repaired, compliant}, 8},
		{"a compliant chunk breaks the repair run", []AdaptiveOutcome{repaired, repaired, compliant, repaired}, 8},
		{"an unknown outcome is not compliant", []AdaptiveOutcome{compliant, AdaptiveOutcome(99), AdaptiveOutcome(98), compliant}, 8},
		{"a halving resets the compliant run", []AdaptiveOutcome{compliant, compliant, OutcomeTruncated, compliant}, 4},
		{"a halving resets the repair run", []AdaptiveOutcome{repaired, repaired, OutcomeOmission, repaired}, 4},
		{"three compliant chunks in a row still grow", []AdaptiveOutcome{compliant, compliant, compliant}, 10},
		{"three repairs in a row still halve", []AdaptiveOutcome{repaired, repaired, repaired}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := NewAdaptiveChunkState(8, 64)
			for _, outcome := range tc.outcomes {
				state.Record(outcome)
			}
			if got := state.Size(); got != tc.want {
				t.Errorf("size = %d, want %d (%s)", got, tc.want, state.Reason())
			}
		})
	}
}

// The zero value is a state of size one, bounded to one, which is what the
// constructor gives for no bounds. It used to report size 0 and a reason that said it
// was kept at the minimum of 1.
func TestAdaptiveChunkStateZeroValueIsUsable(t *testing.T) {
	var state AdaptiveChunkState
	if state.Size() != 1 || state.Reason() != "initial size" {
		t.Fatalf("zero value: size %d, reason %q", state.Size(), state.Reason())
	}
	for _, outcome := range []AdaptiveOutcome{OutcomeCompliant, OutcomeCompliant, OutcomeCompliant, OutcomeCompliant, OutcomeOmission, OutcomeTruncated, OutcomeDuplicateID, OutcomeMalformedProtocol, OutcomeRepaired, 99} {
		state.Record(outcome)
		if state.Size() != 1 {
			t.Fatalf("after %v the size is %d, want it kept at the one the bounds allow", outcome, state.Size())
		}
	}
	if want := NewAdaptiveChunkState(0, 0); want.Size() != 1 {
		t.Errorf("NewAdaptiveChunkState(0, 0) = %d", want.Size())
	}
}

// A nil state reports size one and records nothing: a caller that was handed none
// does not panic.
func TestAdaptiveChunkStateNilIsHarmless(t *testing.T) {
	var state *AdaptiveChunkState
	state.Record(OutcomeOmission)
	if state.Size() != 1 || state.Reason() == "" {
		t.Errorf("nil state: size %d, reason %q", state.Size(), state.Reason())
	}
}

// A state shared by several goroutines stays within its bounds and loses no
// outcome: it is safe for concurrent use.
func TestAdaptiveChunkStateIsSafeForConcurrentUse(t *testing.T) {
	state := NewAdaptiveChunkState(64, 100)
	var wait sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := 0; i < 500; i++ {
				switch (worker + i) % 4 {
				case 0:
					state.Record(OutcomeOmission)
				default:
					state.Record(OutcomeCompliant)
				}
				if size := state.Size(); size < 1 || size > 100 {
					t.Errorf("size %d is outside [1, 100]", size)
					return
				}
				_ = state.Reason()
			}
		}()
	}
	wait.Wait()
}

// Hostile numbers and arbitrary outcomes never take the size outside [1, ceiling].
func TestAdaptiveChunkStateStaysWithinItsBoundsForHostileInput(t *testing.T) {
	bounds := [][2]int{{0, 0}, {-5, -5}, {1, 1}, {5, 3}, {1000, 10}, {0, 500}, {-1, 7}, {7, -1}, {math.MaxInt, math.MaxInt}, {math.MinInt, math.MaxInt}, {2, 2}}
	for _, b := range bounds {
		state := NewAdaptiveChunkState(b[0], b[1])
		ceiling := b[1]
		if ceiling < 1 {
			ceiling = 1
		}
		for i := 0; i < 400; i++ {
			state.Record(AdaptiveOutcome((i*7 + i/3) % 9))
			if size := state.Size(); size < 1 || size > ceiling {
				t.Fatalf("start %d ceiling %d: size %d after %d outcomes", b[0], b[1], size, i+1)
			}
		}
	}
}

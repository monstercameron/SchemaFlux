package ops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCheckpointRejectsMissingArguments(t *testing.T) {
	store := NewMemoryCheckpointStore()
	produce := func(context.Context) (int, error) { return 1, nil }

	if _, _, err := Checkpoint[int](context.Background(), nil, "run", "step", "in", produce); err == nil {
		t.Error("a nil store was accepted")
	}
	if _, _, err := Checkpoint(context.Background(), store, "", "step", "in", produce); err == nil {
		t.Error("an empty run id was accepted")
	}
	if _, _, err := Checkpoint(context.Background(), store, "run", "", "in", produce); err == nil {
		t.Error("an empty step name was accepted")
	}
	if _, _, err := Checkpoint[int](context.Background(), store, "run", "step", "in", nil); err == nil {
		t.Error("a nil step function was accepted")
	}
}

func TestCheckpointHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int32
	produce := func(context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 1, nil
	}

	store := NewMemoryCheckpointStore()
	if _, _, err := Checkpoint(ctx, store, "run", "step", "in", produce); err == nil {
		t.Error("a cancelled context still ran the step")
	}
	if calls != 0 {
		t.Errorf("the step ran %d times under a cancelled context", calls)
	}
}

func TestCheckpointFirstRunComputesAndSaves(t *testing.T) {
	store := NewMemoryCheckpointStore()
	calls := 0
	produce := func(context.Context) (int, error) {
		calls++
		return 42, nil
	}

	value, outcome, err := Checkpoint(context.Background(), store, "run-1", "extract", "the input", produce)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if value != 42 {
		t.Errorf("value = %d, want 42", value)
	}
	if outcome.Resumed || outcome.InputChanged {
		t.Errorf("outcome = %+v on a first run, want both false", outcome)
	}
	if calls != 1 {
		t.Errorf("produce ran %d times, want 1", calls)
	}
}

// The side-effect-not-replayed case: a resumed run for the same input must not
// call produce again.
func TestCheckpointSecondRunSameInputResumesWithoutRerunning(t *testing.T) {
	store := NewMemoryCheckpointStore()
	calls := 0
	produce := func(context.Context) (int, error) {
		calls++
		return 99, nil
	}

	if _, _, err := Checkpoint(context.Background(), store, "run-1", "extract", "same input", produce); err != nil {
		t.Fatalf("first Checkpoint: %v", err)
	}

	value, outcome, err := Checkpoint(context.Background(), store, "run-1", "extract", "same input", produce)
	if err != nil {
		t.Fatalf("second Checkpoint: %v", err)
	}
	if !outcome.Resumed {
		t.Error("Resumed = false on a matching checkpoint")
	}
	if outcome.InputChanged {
		t.Error("InputChanged = true, but the input did not change")
	}
	if value != 99 {
		t.Errorf("value = %d, want the checkpointed 99", value)
	}
	if calls != 1 {
		t.Errorf("produce ran %d times across two calls, want 1: the side effect would have replayed", calls)
	}
}

// The changed-input case: a checkpoint keyed on the same (runID, step) but a
// different input must not be handed back as if it were a resume.
func TestCheckpointDifferentInputRecomputesInsteadOfReusingStaleResult(t *testing.T) {
	store := NewMemoryCheckpointStore()
	calls := 0
	produce := func(context.Context) (int, error) {
		calls++
		return 100 + calls, nil
	}

	value1, _, err := Checkpoint(context.Background(), store, "run-1", "extract", "input A", produce)
	if err != nil {
		t.Fatalf("first Checkpoint: %v", err)
	}

	value2, outcome, err := Checkpoint(context.Background(), store, "run-1", "extract", "input B", produce)
	if err != nil {
		t.Fatalf("second Checkpoint: %v", err)
	}
	if outcome.Resumed {
		t.Error("Resumed = true for a checkpoint recorded against different input")
	}
	if !outcome.InputChanged {
		t.Error("InputChanged = false, but the input did change: this must be detected, not silently accepted")
	}
	if calls != 2 {
		t.Errorf("produce ran %d times, want 2: the second call must run fresh, not reuse value1", calls)
	}
	if value1 == value2 {
		t.Errorf("value1 = value2 = %d; the stale result was reused for new input", value1)
	}
}

func TestCheckpointProduceErrorIsNotSaved(t *testing.T) {
	store := NewMemoryCheckpointStore()
	boom := errors.New("produce failed")
	calls := 0
	produce := func(context.Context) (int, error) {
		calls++
		return 0, boom
	}

	_, outcome, err := Checkpoint(context.Background(), store, "run-1", "extract", "in", produce)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if outcome.Resumed {
		t.Error("Resumed = true after a failed produce")
	}

	// A second call must run produce again: nothing was saved to resume from.
	if _, _, err := Checkpoint(context.Background(), store, "run-1", "extract", "in", produce); !errors.Is(err, boom) {
		t.Fatalf("second call err = %v, want boom again", err)
	}
	if calls != 2 {
		t.Errorf("produce ran %d times, want 2: a failed attempt must not be treated as checkpointed", calls)
	}
}

type erroringLoadStore struct{}

func (erroringLoadStore) Load(context.Context, string, string) (CheckpointRecord, bool, error) {
	return CheckpointRecord{}, false, errors.New("load failed")
}
func (erroringLoadStore) Save(context.Context, string, string, CheckpointRecord) error { return nil }

func TestCheckpointStoreLoadErrorPropagates(t *testing.T) {
	produce := func(context.Context) (int, error) { return 1, nil }
	_, _, err := Checkpoint(context.Background(), erroringLoadStore{}, "run", "step", "in", produce)
	if err == nil || !strings.Contains(err.Error(), "load failed") {
		t.Fatalf("err = %v, want it to carry the store's load failure", err)
	}
}

type erroringSaveStore struct{}

func (erroringSaveStore) Load(context.Context, string, string) (CheckpointRecord, bool, error) {
	return CheckpointRecord{}, false, nil
}
func (erroringSaveStore) Save(context.Context, string, string, CheckpointRecord) error {
	return errors.New("save failed")
}

func TestCheckpointStoreSaveErrorPropagates(t *testing.T) {
	produce := func(context.Context) (int, error) { return 1, nil }
	_, _, err := Checkpoint(context.Background(), erroringSaveStore{}, "run", "step", "in", produce)
	if err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("err = %v, want it to carry the store's save failure", err)
	}
}

func TestCheckpointCorruptStoredValueIsADecodeError(t *testing.T) {
	store := NewMemoryCheckpointStore()
	hash, err := fingerprintInput("in")
	if err != nil {
		t.Fatalf("fingerprintInput: %v", err)
	}
	if err := store.Save(context.Background(), "run", "step", CheckpointRecord{
		InputHash: hash,
		Value:     []byte("not valid json"),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	produce := func(context.Context) (int, error) { return 1, nil }
	_, _, err = Checkpoint(context.Background(), store, "run", "step", "in", produce)
	if err == nil {
		t.Fatal("a corrupt stored value decoded without error")
	}
}

func TestCheckpointDifferentStepsWithinARunAreIsolated(t *testing.T) {
	store := NewMemoryCheckpointStore()
	extractCalls, validateCalls := 0, 0
	extract := func(context.Context) (int, error) { extractCalls++; return 1, nil }
	validate := func(context.Context) (int, error) { validateCalls++; return 2, nil }

	if _, _, err := Checkpoint(context.Background(), store, "run-1", "extract", "in", extract); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Checkpoint(context.Background(), store, "run-1", "validate", "in", validate); err != nil {
		t.Fatal(err)
	}
	// Re-run "extract" for the same run and input: must resume, not touch
	// "validate"'s record.
	value, outcome, err := Checkpoint(context.Background(), store, "run-1", "extract", "in", extract)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Resumed || value != 1 {
		t.Errorf("outcome = %+v, value = %d; step isolation broke", outcome, value)
	}
	if extractCalls != 1 || validateCalls != 1 {
		t.Errorf("extractCalls = %d, validateCalls = %d, want 1 and 1", extractCalls, validateCalls)
	}
}

func TestCheckpointDifferentRunsAreIsolated(t *testing.T) {
	store := NewMemoryCheckpointStore()
	calls := 0
	produce := func(context.Context) (int, error) { calls++; return calls, nil }

	value1, _, err := Checkpoint(context.Background(), store, "run-A", "step", "in", produce)
	if err != nil {
		t.Fatal(err)
	}
	value2, outcome, err := Checkpoint(context.Background(), store, "run-B", "step", "in", produce)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Resumed {
		t.Error("run-B resumed from run-A's checkpoint")
	}
	if value1 == value2 {
		t.Errorf("value1 = value2 = %d; runs are not isolated", value1)
	}
}

func TestMemoryCheckpointStoreConcurrentAccess(t *testing.T) {
	store := NewMemoryCheckpointStore()
	var wg sync.WaitGroup
	const goroutines = 20

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			produce := func(context.Context) (int, error) { return i, nil }
			runID := "run"
			step := "step"
			if i%2 == 0 {
				runID = "run-even"
			}
			if _, _, err := Checkpoint(context.Background(), store, runID, step, i, produce); err != nil {
				t.Errorf("goroutine %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// CF-005. Resuming a multi-step run from failure is deterministic machinery
// that no prompt can deliver, and the steps of a run are caller-provided
// functions the library cannot fingerprint, so it is a combinator of its own
// and not an option on a run: Checkpoint wraps one step, and the caller says
// what input it was run on.
//
// The two failure modes a resume must not have:
//
//  1. Replaying a step whose side effect already happened. If step N sent an
//     email, re-running the whole pipeline after a crash at step N+1 must not
//     send it twice.
//  2. Resuming against a different input and reporting success. A checkpoint
//     keyed only on (runID, step) cannot tell "this run stalled and is being
//     resumed" from "this runID is being reused for different work" -- and
//     handing back yesterday's answer for today's input is a silent wrong
//     answer wearing a correct-looking type.
//
// The fix for both is the same: every checkpoint carries a fingerprint of the
// input that produced it, and a load is only a hit when the fingerprint
// matches. A load whose fingerprint does not match is not an error -- reusing
// a runID for new work is a legitimate thing to do -- but it is not silently
// accepted either: the step runs fresh, and CheckpointOutcome.InputChanged
// says so, so a caller who expected a resume can tell it did not get one.

// CheckpointRecord is what a CheckpointStore persists for one (runID, step).
type CheckpointRecord struct {
	// InputHash fingerprints the input the stored Value was produced from. It
	// is checked on every load, not only recorded on save -- that check is the
	// whole point, see the file comment.
	InputHash string

	// Value is the JSON encoding of the step's result. It is opaque to the
	// store: a store implementation persists and returns bytes, it does not
	// need to know the type Checkpoint[T] is instantiated with.
	Value []byte
}

// CheckpointStore is what a caller implements to persist step-level progress.
// The library owns the identity a checkpoint needs (run, step, input
// fingerprint) and the resume-safety rule; it does not choose a backend. A
// caller with a database writes a CheckpointStore over it; MemoryCheckpointStore
// below is the one this package ships, for tests and for runs that only need to
// survive within a process.
type CheckpointStore interface {
	// Load returns the record for (runID, step) and whether one exists. No
	// record is not an error.
	Load(ctx context.Context, runID, step string) (CheckpointRecord, bool, error)

	// Save writes the record for (runID, step), replacing any previous one.
	Save(ctx context.Context, runID, step string, record CheckpointRecord) error
}

// CheckpointOutcome reports what Checkpoint actually did, because "the cached
// answer was reused" and "it ran, because the checkpoint was for different
// input" return the same (value, nil error) shape and are not the same fact.
type CheckpointOutcome struct {
	// Resumed is true when a checkpoint for this run, step, and input already
	// held a result, so step did not run.
	Resumed bool

	// InputChanged is true when a checkpoint existed for this run and step but
	// for a different input, so it was not reused: step ran fresh instead of
	// silently returning a stale answer for new input.
	InputChanged bool
}

// Checkpoint runs step once and persists its result under (runID, step,
// fingerprint(input)) in store, or, if a matching record already exists,
// returns that record's value without running step at all.
//
// input is whatever the caller's step closure depends on -- it does not have
// to be step's only argument, just something that changes when the work being
// done changes. It is never itself persisted: only its fingerprint is, so a
// store that logs what it is given never sees the caller's data (AGENTS.md:
// never log or embed the caller's payload). The fingerprint is computed with
// encoding/json, which sorts map keys, so two logically identical inputs
// fingerprint identically regardless of map iteration order.
func Checkpoint[T any](ctx context.Context, store CheckpointStore, runID, step string, input any, produce Step[T]) (T, CheckpointOutcome, error) {
	var zero T

	if store == nil {
		return zero, CheckpointOutcome{}, types.Classified(types.KindConfiguration, errors.New("checkpoint: no store"))
	}
	if runID == "" {
		return zero, CheckpointOutcome{}, types.Classified(types.KindConfiguration, errors.New("checkpoint: empty run id"))
	}
	if step == "" {
		return zero, CheckpointOutcome{}, types.Classified(types.KindConfiguration, errors.New("checkpoint: empty step name"))
	}
	if produce == nil {
		return zero, CheckpointOutcome{}, types.Classified(types.KindConfiguration, errors.New("checkpoint: no step to run"))
	}
	if err := ctx.Err(); err != nil {
		return zero, CheckpointOutcome{}, contextEnded(err)
	}

	hash, err := fingerprintInput(input)
	if err != nil {
		return zero, CheckpointOutcome{}, types.Classified(types.KindConfiguration,
			fmt.Errorf("checkpoint: fingerprinting input for %s/%s: %w", runID, step, err))
	}

	var outcome CheckpointOutcome

	record, found, loadErr := store.Load(ctx, runID, step)
	if loadErr != nil {
		return zero, CheckpointOutcome{}, fmt.Errorf("checkpoint: loading %s/%s: %w", runID, step, loadErr)
	}
	if found {
		if record.InputHash == hash {
			var value T
			if err := json.Unmarshal(record.Value, &value); err != nil {
				return zero, CheckpointOutcome{}, types.Classified(types.KindMalformedOutput,
					fmt.Errorf("checkpoint: decoding stored result for %s/%s: %w", runID, step, err))
			}
			outcome.Resumed = true
			return value, outcome, nil
		}
		// A checkpoint exists but for different input: detected, not silently
		// accepted. The step runs fresh below rather than returning the stale
		// value.
		outcome.InputChanged = true
	}

	value, produceErr := produce(ctx)
	if produceErr != nil {
		return zero, outcome, produceErr
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return zero, outcome, types.Classified(types.KindConfiguration,
			fmt.Errorf("checkpoint: encoding result for %s/%s: %w", runID, step, err))
	}
	if err := store.Save(ctx, runID, step, CheckpointRecord{InputHash: hash, Value: encoded}); err != nil {
		return zero, outcome, fmt.Errorf("checkpoint: saving %s/%s: %w", runID, step, err)
	}

	return value, outcome, nil
}

// fingerprintInput hashes the JSON encoding of input. It is a fingerprint, not
// a serialization the caller's data survives in: only the digest is kept
// anywhere, never the encoded bytes.
func fingerprintInput(input any) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// MemoryCheckpointStore is an in-process CheckpointStore: no database, no file
// format, nothing to configure. It exists for tests and for callers whose run
// does not need to survive a process restart. Safe for concurrent use by
// multiple goroutines running different (runID, step) pairs.
type MemoryCheckpointStore struct {
	mu      sync.Mutex
	records map[string]CheckpointRecord
}

// NewMemoryCheckpointStore builds an empty store.
func NewMemoryCheckpointStore() *MemoryCheckpointStore {
	return &MemoryCheckpointStore{records: make(map[string]CheckpointRecord)}
}

// Load implements CheckpointStore. The record it returns holds its own copy of
// the value, so a caller that changes the bytes does not change the store.
func (m *MemoryCheckpointStore) Load(ctx context.Context, runID, step string) (CheckpointRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return CheckpointRecord{}, false, contextEnded(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[checkpointKey(runID, step)]
	if !ok {
		return CheckpointRecord{}, false, nil
	}
	record.Value = append([]byte(nil), record.Value...)
	return record, true, nil
}

// Save implements CheckpointStore. The store keeps its own copy of the value,
// so a caller that reuses the slice afterwards does not change what was saved.
func (m *MemoryCheckpointStore) Save(ctx context.Context, runID, step string, record CheckpointRecord) error {
	if err := ctx.Err(); err != nil {
		return contextEnded(err)
	}
	record.Value = append([]byte(nil), record.Value...)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = make(map[string]CheckpointRecord)
	}
	m.records[checkpointKey(runID, step)] = record
	return nil
}

// Delete removes the record for (runID, step), if there is one, so that a
// long-lived process can let go of the runs it has finished. A record that is
// not there is not an error.
func (m *MemoryCheckpointStore) Delete(ctx context.Context, runID, step string) error {
	if err := ctx.Err(); err != nil {
		return contextEnded(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, checkpointKey(runID, step))
	return nil
}

// checkpointKey joins runID and step so that two different pairs never share a
// key. Both are strings a caller typed, so no separator byte is safe: the first
// is written with its length, which makes the join unambiguous whatever either
// contains. A byte separator made ("a\x00b", "c") and ("a", "b\x00c") one key.
func checkpointKey(runID, step string) string {
	return strconv.Itoa(len(runID)) + ":" + runID + step
}

package flow

import (
	"context"
	"encoding/json"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// Resuming a run that failed has two ways to go wrong. The first is replaying a
// step whose side effect already happened: if step N sent an email, running the
// whole program again after a crash at step N+1 must not send it twice. The
// second is resuming against different input and reporting success: a checkpoint
// keyed only by run and step cannot tell "this run stalled and is being resumed"
// from "this run id is being reused for different work", and handing back
// yesterday's answer for today's input is a wrong answer in the shape of a right
// one. So every checkpoint carries a fingerprint of the input that produced it,
// and a load is a hit only when the fingerprint matches. A load that does not
// match is not an error, since reusing a run id for new work is legitimate, but
// it is not silently accepted either: the step runs fresh, and
// CheckpointOutcome.InputChanged says so.

// A CheckpointRecord is what a CheckpointStore keeps for one run and step.
type CheckpointRecord struct {
	// InputHash fingerprints the input the stored Value was produced from. It
	// is checked on every load and not only recorded on save, which is the whole
	// point of it.
	InputHash string

	// Value is the JSON encoding of the step's result. It is opaque to the
	// store: a store keeps bytes and returns them, and does not need to know
	// which type Checkpoint was called with.
	Value []byte
}

// A CheckpointStore is what a caller implements to keep the progress of a run
// somewhere that outlives it. The library owns the identity of a checkpoint (run,
// step, input fingerprint) and the rule that makes a resume safe; it does not
// choose a backend. A caller with a database writes a store over it, and
// MemoryStore is the one this package provides for tests and for runs that need
// to survive only within a process.
//
// A store's methods are called on the caller's goroutine, one at a time per call
// of Checkpoint. An error a store returns is returned by Checkpoint as it was
// given. The store sees the fingerprint of the input and the JSON of the result,
// and never the input.
type CheckpointStore interface {
	// Load returns the record kept for the run and step, and whether there is
	// one. No record is not an error.
	Load(ctx context.Context, runID, step string) (CheckpointRecord, bool, error)

	// Save keeps the record for the run and step, replacing any earlier one.
	Save(ctx context.Context, runID, step string, record CheckpointRecord) error
}

// CheckpointOutcome says what Checkpoint did, because the stored answer being
// reused and the step having run because the stored answer was for other input
// both return a value and a nil error, and they are not the same fact.
type CheckpointOutcome struct {
	// Resumed is true when a checkpoint for this run, step and input already held
	// a result, so that the step did not run.
	Resumed bool

	// InputChanged is true when a checkpoint existed for this run and step but
	// for different input, so it was not used and the step ran fresh instead of
	// returning a stale answer for new input.
	InputChanged bool
}

// Checkpoint runs produce once and keeps its result under the run, the step name
// and a fingerprint of input; or, when a record for all three is already in the
// store, returns that record's value without running produce at all.
//
//	invoice, outcome, err := flow.Checkpoint(ctx, store, "run-7", "extract", text, func(ctx context.Context) (Invoice, error) {
//		return schemaflux.Extract[Invoice](ctx, text)
//	})
//	if outcome.Resumed { ... } // the stored invoice was used, and nothing was asked
//
// input is whatever produce depends on. It does not have to be produce's only
// argument, only something that changes when the work being done changes. It is
// never itself kept: only its fingerprint is, so a store that logs what it is
// given never sees the caller's data. The fingerprint is made from the JSON
// encoding of input, which sorts the keys of a map, so two inputs that are
// logically the same have the same fingerprint whatever order a map was ranged
// in. Both input and the result have to be encodable as JSON.
//
// What is returned:
//
//   - a record for the run, step and input was found: its value, an outcome with
//     Resumed true, and a nil error. produce did not run.
//   - nothing was found, or the record was for other input (InputChanged true):
//     produce's value, after the result has been saved, and a nil error.
//   - produce failed: the zero value, the outcome as far as it had got, and
//     produce's error, the same value, which is not saved.
//   - the store failed to load or to save: the zero value and the store's error,
//     the same value it returned. A result whose save failed is not returned: the
//     caller asked for a step that can be resumed, and it cannot be.
//   - a stored value that does not decode into T is an *Error of kind
//     ErrInvalidAnswer, and a result that cannot be encoded is one of kind
//     ErrConfiguration; neither message carries any of the data.
//
// produce and the store run on the caller's goroutine, so a panic in either is
// the caller's panic. An empty run id or step name, a nil store, a nil produce,
// an input that cannot be encoded as JSON, and a nil ctx are errors of kind
// ErrInvalidRequest, and nothing runs; a ctx that is already done is one of kind
// ErrCanceled or ErrTimeout. Checkpoint calls no model itself.
func Checkpoint[T any](ctx context.Context, store CheckpointStore, runID, step string, input any, produce schemaflux.Step[T]) (T, CheckpointOutcome, error) {
	var zero T
	if err := begin(ctx, "flow.checkpoint"); err != nil {
		return zero, CheckpointOutcome{}, err
	}
	switch {
	case store == nil:
		return zero, CheckpointOutcome{}, refuse("flow.checkpoint", "there is no store")
	case runID == "":
		return zero, CheckpointOutcome{}, refuse("flow.checkpoint", "the run id is empty")
	case step == "":
		return zero, CheckpointOutcome{}, refuse("flow.checkpoint", "the step name is empty")
	case produce == nil:
		return zero, CheckpointOutcome{}, refuse("flow.checkpoint", "there is no step to run")
	}
	// The input is encoded here so that an input that cannot be is refused with
	// words of the library's own: the encoder's message can quote what a type's
	// MarshalJSON returned. The bytes are the ones the internal fingerprint
	// would encode, handed to it as they are, so the fingerprint is the same.
	encoded, err := json.Marshal(input)
	if err != nil {
		return zero, CheckpointOutcome{}, refuse("flow.checkpoint", "the input cannot be encoded as JSON, so it cannot be fingerprinted")
	}

	var found, produced bool
	adapted := checkpointAdapter{store: store, found: &found}
	value, outcome, err := ops.Checkpoint(ctx, adapted, runID, step, json.RawMessage(encoded), func(ctx context.Context) (T, error) {
		value, err := produce(ctx)
		if err != nil {
			return value, own(err)
		}
		produced = true
		return value, nil
	})
	result := CheckpointOutcome(outcome)
	if err == nil {
		return value, result, nil
	}
	switch {
	case produced && !isCallers(err):
		return zero, result, &schemaflux.Error{
			Kind:    schemaflux.ErrConfiguration,
			Op:      "flow.checkpoint",
			Message: "the result cannot be encoded as JSON, so it cannot be kept",
			Err:     err,
		}
	case found && !produced && !isCallers(err):
		return zero, result, &schemaflux.Error{
			Kind:    schemaflux.ErrInvalidAnswer,
			Op:      "flow.checkpoint",
			Message: "the stored result does not decode into the result type",
			Err:     err,
		}
	}
	return zero, result, end("flow.checkpoint", err)
}

// isCallers reports whether err carries an error of the caller's own function,
// a step or a store.
func isCallers(err error) bool {
	_, marked := asCaller(err)
	return marked
}

// checkpointAdapter is the caller's store as the internal combinator's, with
// what the store returns as an error marked as the caller's. found records
// whether a load found a record, which is what tells a stored value that cannot
// be decoded from the other failures of the combinator.
type checkpointAdapter struct {
	store CheckpointStore
	found *bool
}

func (a checkpointAdapter) Load(ctx context.Context, runID, step string) (ops.CheckpointRecord, bool, error) {
	record, ok, err := a.store.Load(ctx, runID, step)
	if err != nil {
		return ops.CheckpointRecord{}, false, own(err)
	}
	*a.found = ok
	return ops.CheckpointRecord(record), ok, nil
}

func (a checkpointAdapter) Save(ctx context.Context, runID, step string, record ops.CheckpointRecord) error {
	if err := a.store.Save(ctx, runID, step, CheckpointRecord(record)); err != nil {
		return own(err)
	}
	return nil
}

// A MemoryStore is a CheckpointStore in the memory of the process: no database,
// no file format, nothing to configure. It is for tests and for runs that do not
// need to survive a restart. The zero value is an empty store, ready to use. It
// is safe for concurrent use by several goroutines, and must not be copied after
// first use.
//
// Its methods return an *schemaflux.Error of kind ErrCanceled or ErrTimeout for
// a context that is done and one of kind ErrInvalidRequest for a nil one.
type MemoryStore struct {
	records ops.MemoryCheckpointStore
}

// Load returns the record kept for the run and step, and whether there is one.
// The record holds its own copy of the value, so a caller that changes the bytes
// does not change the store.
func (m *MemoryStore) Load(ctx context.Context, runID, step string) (CheckpointRecord, bool, error) {
	if err := begin(ctx, "flow.memorystore"); err != nil {
		return CheckpointRecord{}, false, err
	}
	record, ok, err := m.records.Load(ctx, runID, step)
	if err != nil {
		return CheckpointRecord{}, false, bind.ConvertError("flow.memorystore", err)
	}
	return CheckpointRecord(record), ok, nil
}

// Save keeps the record for the run and step, replacing any earlier one. The
// store keeps its own copy of the value, so a caller that reuses the slice
// afterwards does not change what was saved.
func (m *MemoryStore) Save(ctx context.Context, runID, step string, record CheckpointRecord) error {
	if err := begin(ctx, "flow.memorystore"); err != nil {
		return err
	}
	return bind.ConvertError("flow.memorystore", m.records.Save(ctx, runID, step, ops.CheckpointRecord(record)))
}

// Delete removes the record for the run and step, so that a long-lived process
// can let go of the runs it has finished. A record that is not there is not an
// error.
func (m *MemoryStore) Delete(ctx context.Context, runID, step string) error {
	if err := begin(ctx, "flow.memorystore"); err != nil {
		return err
	}
	return bind.ConvertError("flow.memorystore", m.records.Delete(ctx, runID, step))
}

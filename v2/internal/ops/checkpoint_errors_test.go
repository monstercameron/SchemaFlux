package ops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// failingCheckpointStore is a store whose Load or Save fails with the error it
// was given.
type failingCheckpointStore struct {
	CheckpointStore
	load, save error
}

func (s failingCheckpointStore) Load(ctx context.Context, runID, step string) (CheckpointRecord, bool, error) {
	if s.load != nil {
		return CheckpointRecord{}, false, s.load
	}
	return s.CheckpointStore.Load(ctx, runID, step)
}

func (s failingCheckpointStore) Save(ctx context.Context, runID, step string, record CheckpointRecord) error {
	if s.save != nil {
		return s.save
	}
	return s.CheckpointStore.Save(ctx, runID, step, record)
}

// Every failure of Checkpoint's own has a kind, with the text it had before
// kinds were declared; a failure of the caller's store or step keeps that error
// in its chain, and a step's own error comes back as the very value it was.
func TestCheckpointFailuresHaveKinds(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryCheckpointStore()
	produce := Step[int](func(context.Context) (int, error) { return 7, nil })
	var unencodable Step[chan int] = func(context.Context) (chan int, error) { return make(chan int), nil }
	_, marshalErr := json.Marshal(make(chan int))
	var decoded int
	decodeErr := json.Unmarshal([]byte("not json"), &decoded)

	// A stored record for run/step whose input matches and whose value is not
	// the JSON of an int.
	corrupt := NewMemoryCheckpointStore()
	hash, err := fingerprintInput("in")
	if err != nil {
		t.Fatal(err)
	}
	if err := corrupt.Save(context.Background(), "run", "step", CheckpointRecord{InputHash: hash, Value: []byte("not json")}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		run      func() error
		text     string
		kind     types.ErrorKind
		sentinel error
		also     error
	}{
		{"no store", func() error {
			_, _, err := Checkpoint[int](context.Background(), nil, "run", "step", "in", produce)
			return err
		},
			"checkpoint: no store", types.KindConfiguration, types.ErrConfiguration, nil},
		{"an empty run id", func() error {
			_, _, err := Checkpoint(context.Background(), store, "", "step", "in", produce)
			return err
		},
			"checkpoint: empty run id", types.KindConfiguration, types.ErrConfiguration, nil},
		{"an empty step name", func() error {
			_, _, err := Checkpoint(context.Background(), store, "run", "", "in", produce)
			return err
		},
			"checkpoint: empty step name", types.KindConfiguration, types.ErrConfiguration, nil},
		{"no step to run", func() error {
			_, _, err := Checkpoint[int](context.Background(), store, "run", "step", "in", nil)
			return err
		}, "checkpoint: no step to run", types.KindConfiguration, types.ErrConfiguration, nil},
		{"a cancelled context", func() error { _, _, err := Checkpoint(cancelled, store, "run", "step", "in", produce); return err },
			"context canceled", types.KindCanceled, types.ErrCanceled, context.Canceled},
		{"an input that cannot be fingerprinted", func() error {
			_, _, err := Checkpoint(context.Background(), store, "run", "step", make(chan int), produce)
			return err
		}, "checkpoint: fingerprinting input for run/step: " + marshalErr.Error(), types.KindConfiguration, types.ErrConfiguration, nil},
		{"a stored result that cannot be read as the type", func() error {
			_, outcome, err := Checkpoint(context.Background(), corrupt, "run", "step", "in", produce)
			if outcome.Resumed {
				t.Error("a record that could not be read was reported as a resume")
			}
			return err
		}, "checkpoint: decoding stored result for run/step: " + decodeErr.Error(), types.KindMalformedOutput, types.ErrMalformedOutput, nil},
		{"a result that cannot be encoded", func() error {
			_, _, err := Checkpoint(context.Background(), NewMemoryCheckpointStore(), "run", "step", "in", unencodable)
			return err
		}, "checkpoint: encoding result for run/step: " + marshalErr.Error(), types.KindConfiguration, types.ErrConfiguration, nil},
		{"a store that fails to load with a classified error", func() error {
			failure := &types.OperationError{Kind: types.KindProviderUnavailable, Op: "theirs", Message: "down"}
			_, _, err := Checkpoint(context.Background(), failingCheckpointStore{CheckpointStore: store, load: failure}, "run", "step", "in", produce)
			return err
		}, "checkpoint: loading run/step: theirs: provider unavailable: down", types.KindProviderUnavailable, types.ErrProviderUnavailable, nil},
		{"a store that fails to save with a classified error", func() error {
			failure := &types.OperationError{Kind: types.KindProviderUnavailable, Op: "theirs", Message: "down"}
			_, _, err := Checkpoint(context.Background(), failingCheckpointStore{CheckpointStore: NewMemoryCheckpointStore(), save: failure}, "run", "step", "in", produce)
			return err
		}, "checkpoint: saving run/step: theirs: provider unavailable: down", types.KindProviderUnavailable, types.ErrProviderUnavailable, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if err.Error() != tc.text {
				t.Errorf("text = %q, want %q", err.Error(), tc.text)
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
			if tc.also != nil && !errors.Is(err, tc.also) {
				t.Errorf("errors.Is(err, %v) is false", tc.also)
			}
		})
	}

	t.Run("a store's plain error is in the chain of the error that reports it", func(t *testing.T) {
		plain := errors.New("disk full")
		_, _, err := Checkpoint(context.Background(), failingCheckpointStore{CheckpointStore: store, load: plain}, "run", "step", "in", produce)
		if !errors.Is(err, plain) || err.Error() != "checkpoint: loading run/step: disk full" {
			t.Errorf("load: err = %v", err)
		}
		_, _, err = Checkpoint(context.Background(), failingCheckpointStore{CheckpointStore: NewMemoryCheckpointStore(), save: plain}, "run", "step", "in", produce)
		if !errors.Is(err, plain) || err.Error() != "checkpoint: saving run/step: disk full" {
			t.Errorf("save: err = %v", err)
		}
	})

	for _, failure := range []error{
		errors.New("the caller's step failed"),
		&types.OperationError{Kind: types.KindBudgetExceeded, Op: "theirs", Message: "spent"},
		context.Canceled,
	} {
		t.Run("a step's own error is returned as the very value: "+failure.Error(), func(t *testing.T) {
			_, outcome, err := Checkpoint(context.Background(), NewMemoryCheckpointStore(), "run", "step", "in",
				Step[int](func(context.Context) (int, error) { return 0, failure }))
			if err != failure {
				t.Errorf("err = %#v, want the very value the step returned", err)
			}
			if outcome.Resumed {
				t.Error("a failed step was reported as a resume")
			}
		})
	}
}

// The in-process store reports a context that is over with a kind.
func TestMemoryCheckpointStoreClassifiesAContextThatIsOver(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryCheckpointStore()
	calls := map[string]func() error{
		"Load": func() error { _, _, err := store.Load(cancelled, "run", "step"); return err },
		"Save": func() error { return store.Save(cancelled, "run", "step", CheckpointRecord{}) },
		"Delete": func() error {
			return store.Delete(cancelled, "run", "step")
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || err.Error() != "context canceled" || !errors.Is(err, context.Canceled) ||
				types.KindOf(err) != types.KindCanceled || !errors.Is(err, types.ErrCanceled) {
				t.Errorf("err = %v (kind %v)", err, types.KindOf(err))
			}
		})
	}
}

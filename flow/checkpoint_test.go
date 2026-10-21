package flow_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// scriptedStore is a CheckpointStore that keeps what it is given in a map, can be
// made to fail, and remembers every record it was asked to save.
type scriptedStore struct {
	mu      sync.Mutex
	records map[string]flow.CheckpointRecord
	saved   []flow.CheckpointRecord
	loads   int
	loadErr error
	saveErr error
}

func newScriptedStore() *scriptedStore {
	return &scriptedStore{records: map[string]flow.CheckpointRecord{}}
}

func (s *scriptedStore) Load(_ context.Context, runID, step string) (flow.CheckpointRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return flow.CheckpointRecord{}, false, s.loadErr
	}
	record, ok := s.records[runID+"/"+step]
	return record, ok, nil
}

func (s *scriptedStore) Save(_ context.Context, runID, step string, record flow.CheckpointRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.records[runID+"/"+step] = record
	s.saved = append(s.saved, record)
	return nil
}

type invoice struct {
	Number string
	Total  int
}

func produceInvoice(runs *atomic.Int32, number string, total int) schemaflux.Step[invoice] {
	return func(context.Context) (invoice, error) {
		runs.Add(1)
		return invoice{Number: number, Total: total}, nil
	}
}

func TestCheckpointRunsTheStepOnceAndKeepsItsResult(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	got, outcome, err := flow.Checkpoint(context.Background(), store, "run-1", "extract", "text", produceInvoice(&runs, "A-17", 40))
	if err != nil || got != (invoice{"A-17", 40}) {
		t.Fatalf("Checkpoint = %+v, %v", got, err)
	}
	if outcome != (flow.CheckpointOutcome{}) || runs.Load() != 1 || len(store.saved) != 1 {
		t.Errorf("outcome = %+v, runs = %d, records saved = %d", outcome, runs.Load(), len(store.saved))
	}
	if string(store.saved[0].Value) != `{"Number":"A-17","Total":40}` {
		t.Errorf("the saved value is %s", store.saved[0].Value)
	}
}

func TestCheckpointResumesWithoutRunningTheStepWhenTheInputIsTheSame(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	first, _, err := flow.Checkpoint(context.Background(), store, "run-1", "extract", "text", produceInvoice(&runs, "A-17", 40))
	if err != nil {
		t.Fatal(err)
	}
	second, outcome, err := flow.Checkpoint(context.Background(), store, "run-1", "extract", "text", produceInvoice(&runs, "other", 99))
	if err != nil || second != first {
		t.Fatalf("Checkpoint = %+v, %v; want the stored result %+v", second, err, first)
	}
	if !outcome.Resumed || outcome.InputChanged || runs.Load() != 1 {
		t.Errorf("outcome = %+v, runs = %d; the step does not run again", outcome, runs.Load())
	}
}

func TestCheckpointRunsTheStepFreshAndSaysSoWhenTheInputChanged(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	_, _, _ = flow.Checkpoint(context.Background(), store, "run-1", "extract", "yesterday's text", produceInvoice(&runs, "A-17", 40))
	got, outcome, err := flow.Checkpoint(context.Background(), store, "run-1", "extract", "today's text", produceInvoice(&runs, "B-2", 7))
	if err != nil || got != (invoice{"B-2", 7}) {
		t.Fatalf("Checkpoint = %+v, %v; want today's answer and not yesterday's", got, err)
	}
	if outcome.Resumed || !outcome.InputChanged || runs.Load() != 2 {
		t.Errorf("outcome = %+v, runs = %d", outcome, runs.Load())
	}
	again, outcome, _ := flow.Checkpoint(context.Background(), store, "run-1", "extract", "today's text", produceInvoice(&runs, "never", 0))
	if again != got || !outcome.Resumed || runs.Load() != 2 {
		t.Errorf("the new result was not kept for the new input: %+v, %+v", again, outcome)
	}
}

func TestCheckpointKeepsRunsAndStepsApart(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	for _, key := range [][2]string{{"run-1", "extract"}, {"run-1", "classify"}, {"run-2", "extract"}} {
		_, outcome, err := flow.Checkpoint(context.Background(), store, key[0], key[1], "same input", produceInvoice(&runs, "A", 1))
		if err != nil || outcome.Resumed {
			t.Fatalf("%v: outcome %+v, err %v; a different run or step is not a resume", key, outcome, err)
		}
	}
	if runs.Load() != 3 {
		t.Errorf("runs = %d, want 3", runs.Load())
	}
}

func TestCheckpointFingerprintsTheInputSoOnlyTheDigestReachesTheStore(t *testing.T) {
	const canary = "input-4471-canary"
	store := newScriptedStore()
	input := map[string]any{"b": []int{1, 2}, "a": canary}
	var runs atomic.Int32
	if _, _, err := flow.Checkpoint(context.Background(), store, "r", "s", input, produceInvoice(&runs, "A", 1)); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	sum := sha256.Sum256(encoded)
	want := hex.EncodeToString(sum[:])
	if got := store.saved[0].InputHash; got != want {
		t.Errorf("InputHash = %s, want the sha256 of the JSON encoding of the input, %s", got, want)
	}
	if strings.Contains(store.saved[0].InputHash, canary) || strings.Contains(string(store.saved[0].Value), canary) {
		t.Error("the input reached the store")
	}
}

func TestCheckpointFingerprintDoesNotDependOnTheOrderOfAMapsKeys(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	build := func(reverse bool) map[string]int {
		m := map[string]int{}
		for i := range 50 {
			if reverse {
				i = 49 - i
			}
			m[fmt.Sprint("key", i)] = i
		}
		return m
	}
	if _, _, err := flow.Checkpoint(context.Background(), store, "r", "s", build(false), produceInvoice(&runs, "A", 1)); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		_, outcome, err := flow.Checkpoint(context.Background(), store, "r", "s", build(i%2 == 0), produceInvoice(&runs, "B", 2))
		if err != nil || !outcome.Resumed || outcome.InputChanged {
			t.Fatalf("round %d: outcome = %+v, err = %v; the same entries are the same input", i, outcome, err)
		}
	}
	if runs.Load() != 1 {
		t.Errorf("runs = %d", runs.Load())
	}
}

func TestCheckpointReturnsTheStepsErrorAsItWasGivenAndKeepsNothing(t *testing.T) {
	typed := &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Op: "extract"}
	for _, failure := range []error{errStep, fmt.Errorf("wrapped: %w", errStep), typed} {
		store := newScriptedStore()
		got, outcome, err := flow.Checkpoint(context.Background(), store, "r", "s", 1, fails[invoice](failure))
		if err != failure || got != (invoice{}) || outcome != (flow.CheckpointOutcome{}) || len(store.saved) != 0 {
			t.Errorf("Checkpoint = %+v, %+v, %v; saved %d", got, outcome, err, len(store.saved))
		}
	}
}

func TestCheckpointReportsInputChangedBesideAStepsFailure(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	_, _, _ = flow.Checkpoint(context.Background(), store, "r", "s", "old", produceInvoice(&runs, "A", 1))
	_, outcome, err := flow.Checkpoint(context.Background(), store, "r", "s", "new", fails[invoice](errStep))
	if err != errStep || !outcome.InputChanged || outcome.Resumed {
		t.Errorf("outcome = %+v, err = %v", outcome, err)
	}
}

func TestCheckpointReturnsTheStoresErrorAsItWasGiven(t *testing.T) {
	loadFailure := fmt.Errorf("the database is down: %w", errStep)
	saveFailure := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Op: "store"}
	t.Run("on load", func(t *testing.T) {
		store := newScriptedStore()
		store.loadErr = loadFailure
		var runs atomic.Int32
		got, outcome, err := flow.Checkpoint(context.Background(), store, "r", "s", 1, produceInvoice(&runs, "A", 1))
		if err != loadFailure || got != (invoice{}) || outcome != (flow.CheckpointOutcome{}) || runs.Load() != 0 {
			t.Errorf("Checkpoint = %+v, %+v, %v after %d runs; a step is not run when the store cannot be read", got, outcome, err, runs.Load())
		}
	})
	t.Run("on save", func(t *testing.T) {
		store := newScriptedStore()
		store.saveErr = saveFailure
		var runs atomic.Int32
		got, _, err := flow.Checkpoint(context.Background(), store, "r", "s", 1, produceInvoice(&runs, "A", 1))
		if err != error(saveFailure) || got != (invoice{}) || runs.Load() != 1 {
			t.Errorf("Checkpoint = %+v, %v after %d runs; a result that could not be kept is not returned", got, err, runs.Load())
		}
	})
}

func TestCheckpointRefusesAnArgumentItCannotUseAndRunsNothing(t *testing.T) {
	var runs atomic.Int32
	produce := produceInvoice(&runs, "A", 1)
	store := newScriptedStore()
	cases := []struct {
		name    string
		store   flow.CheckpointStore
		runID   string
		step    string
		input   any
		produce schemaflux.Step[invoice]
	}{
		{"no store", nil, "r", "s", 1, produce},
		{"an empty run id", store, "", "s", 1, produce},
		{"an empty step name", store, "r", "", 1, produce},
		{"no step to run", store, "r", "s", 1, nil},
		{"an input that cannot be encoded", store, "r", "s", make(chan int), produce},
		{"an input holding a function", store, "r", "s", map[string]any{"f": func() {}}, produce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome, err := flow.Checkpoint(context.Background(), tc.store, tc.runID, tc.step, tc.input, tc.produce)
			e := wantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "flow.checkpoint" || got != (invoice{}) || outcome != (flow.CheckpointOutcome{}) || runs.Load() != 0 {
				t.Errorf("Op = %q, got = %+v, outcome = %+v, runs = %d", e.Op, got, outcome, runs.Load())
			}
		})
	}
	if store.loads != 0 {
		t.Errorf("the store was read %d times for arguments that were refused", store.loads)
	}
}

type marshalsWithItsData struct{ secret string }

func (m marshalsWithItsData) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot encode " + m.secret)
}

func TestCheckpointsRefusalOfAnInputQuotesNothingOfIt(t *testing.T) {
	const canary = "input-4471-canary"
	_, _, err := flow.Checkpoint(context.Background(), newScriptedStore(), "r", "s", marshalsWithItsData{canary}, returns(invoice{}))
	e := wantKind(t, err, schemaflux.ErrInvalidRequest)
	if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v %v", e, e.Err), canary) {
		t.Errorf("the input is in the error: %v", err)
	}
}

func TestCheckpointRefusesAResultThatCannotBeKeptAndSaysNothingOfIt(t *testing.T) {
	const canary = "result-4471-canary"
	var runs atomic.Int32
	produce := func(context.Context) (marshalsWithItsData, error) {
		runs.Add(1)
		return marshalsWithItsData{canary}, nil
	}
	store := newScriptedStore()
	got, _, err := flow.Checkpoint(context.Background(), store, "r", "s", 1, produce)
	e := wantKind(t, err, schemaflux.ErrConfiguration)
	if got != (marshalsWithItsData{}) || runs.Load() != 1 || len(store.saved) != 0 {
		t.Errorf("got = %+v, runs = %d, saved = %d", got, runs.Load(), len(store.saved))
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v", e), canary) {
		t.Errorf("the result is in the error: %v", err)
	}
}

func TestCheckpointRefusesAStoredValueThatDoesNotDecodeIntoTheResultType(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	_, _, _ = flow.Checkpoint(context.Background(), store, "r", "s", 1, produceInvoice(&runs, "A", 1))
	record := store.records["r/s"]
	record.Value = []byte(`"a string, not an invoice"`)
	store.records["r/s"] = record
	got, outcome, err := flow.Checkpoint(context.Background(), store, "r", "s", 1, produceInvoice(&runs, "B", 2))
	wantKind(t, err, schemaflux.ErrInvalidAnswer)
	if got != (invoice{}) || outcome.Resumed || runs.Load() != 1 {
		t.Errorf("got = %+v, outcome = %+v, runs = %d; a stored value that is not the result is not used and the step is not run in its place", got, outcome, runs.Load())
	}
}

func TestCheckpointRefusesANilContextAndAContextThatIsOverAndTouchesNothing(t *testing.T) {
	store := newScriptedStore()
	var runs atomic.Int32
	var nilContext context.Context
	_, _, err := flow.Checkpoint(nilContext, store, "r", "s", 1, produceInvoice(&runs, "A", 1))
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		_, outcome, err := flow.Checkpoint(done.ctx, store, "r", "s", 1, produceInvoice(&runs, "A", 1))
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || outcome != (flow.CheckpointOutcome{}) {
			t.Errorf("%s: err = %v, outcome = %+v", done.name, err, outcome)
		}
	}
	if runs.Load() != 0 || store.loads != 0 {
		t.Errorf("steps run = %d, store reads = %d", runs.Load(), store.loads)
	}
}

func TestCheckpointRunsTheStepOnTheCallersGoroutineSoAPanicReachesTheCaller(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "from the step" {
			t.Errorf("recovered %v", recovered)
		}
	}()
	_, _, _ = flow.Checkpoint(context.Background(), newScriptedStore(), "r", "s", 1, panics[invoice]("from the step"))
	t.Error("Checkpoint returned after a panic in the step")
}

func TestCheckpointKeepsAndResumesAnyResultThatEncodesAsJSON(t *testing.T) {
	type nested struct {
		Names []string
		Count map[string]int
		Inner *invoice
	}
	want := nested{Names: []string{"a", "b"}, Count: map[string]int{"x": 1}, Inner: &invoice{"A", 2}}
	store := newScriptedStore()
	_, _, err := flow.Checkpoint(context.Background(), store, "r", "s", 1, returns(want))
	if err != nil {
		t.Fatal(err)
	}
	got, outcome, err := flow.Checkpoint(context.Background(), store, "r", "s", 1, fails[nested](errStep))
	if err != nil || !outcome.Resumed || got.Names[1] != "b" || got.Count["x"] != 1 || *got.Inner != *want.Inner {
		t.Errorf("Checkpoint = %+v, %+v, %v", got, outcome, err)
	}
}

func TestCheckpointWorksOverTheMemoryStoreAndResumesAcrossCalls(t *testing.T) {
	var store flow.MemoryStore
	var runs atomic.Int32
	for call := 1; call <= 3; call++ {
		got, outcome, err := flow.Checkpoint(context.Background(), &store, "run", "step", []int{1, 2, 3}, produceInvoice(&runs, "A", call))
		if err != nil || got != (invoice{"A", 1}) || outcome.Resumed != (call > 1) {
			t.Errorf("call %d: %+v, %+v, %v", call, got, outcome, err)
		}
	}
	if runs.Load() != 1 {
		t.Errorf("runs = %d", runs.Load())
	}
}

func TestMemoryStoreZeroValueIsAnEmptyStoreReadyToUse(t *testing.T) {
	var store flow.MemoryStore
	record, ok, err := store.Load(context.Background(), "r", "s")
	if err != nil || ok || record.InputHash != "" || record.Value != nil {
		t.Errorf("Load = %+v, %v, %v", record, ok, err)
	}
	if err := store.Delete(context.Background(), "r", "s"); err != nil {
		t.Errorf("deleting a record that is not there: %v", err)
	}
	if err := store.Save(context.Background(), "r", "s", flow.CheckpointRecord{InputHash: "h", Value: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	record, ok, err = store.Load(context.Background(), "r", "s")
	if err != nil || !ok || record.InputHash != "h" || string(record.Value) != "1" {
		t.Errorf("Load = %+v, %v, %v", record, ok, err)
	}
}

func TestMemoryStoreKeepsItsOwnCopyOfAValueInBothDirections(t *testing.T) {
	var store flow.MemoryStore
	value := []byte("abc")
	_ = store.Save(context.Background(), "r", "s", flow.CheckpointRecord{InputHash: "h", Value: value})
	value[0] = 'X'
	got, _, _ := store.Load(context.Background(), "r", "s")
	if string(got.Value) != "abc" {
		t.Errorf("the caller's slice changed what was saved: %s", got.Value)
	}
	got.Value[1] = 'Y'
	again, _, _ := store.Load(context.Background(), "r", "s")
	if string(again.Value) != "abc" {
		t.Errorf("the slice a load returned changed the store: %s", again.Value)
	}
}

func TestMemoryStoreReplacesADeletesAndKeepsKeysApart(t *testing.T) {
	var store flow.MemoryStore
	ctx := context.Background()
	save := func(run, step, hash string) { _ = store.Save(ctx, run, step, flow.CheckpointRecord{InputHash: hash}) }
	save("a\x00b", "c", "one")
	save("a", "b\x00c", "two")
	save("a", "b", "three")
	save("ab", "", "four")
	for _, tc := range []struct{ run, step, want string }{
		{"a\x00b", "c", "one"}, {"a", "b\x00c", "two"}, {"a", "b", "three"}, {"ab", "", "four"},
	} {
		got, ok, err := store.Load(ctx, tc.run, tc.step)
		if err != nil || !ok || got.InputHash != tc.want {
			t.Errorf("Load(%q, %q) = %q, %v, %v; want %q: two different pairs never share a record", tc.run, tc.step, got.InputHash, ok, err, tc.want)
		}
	}
	save("a", "b", "replaced")
	if got, _, _ := store.Load(ctx, "a", "b"); got.InputHash != "replaced" {
		t.Errorf("a save did not replace the record: %q", got.InputHash)
	}
	_ = store.Delete(ctx, "a", "b")
	if _, ok, _ := store.Load(ctx, "a", "b"); ok {
		t.Error("the record is still there after Delete")
	}
	if _, ok, _ := store.Load(ctx, "a", "b\x00c"); !ok {
		t.Error("Delete removed another pair's record")
	}
}

func TestMemoryStoreRefusesANilContextAndAContextThatIsOver(t *testing.T) {
	var store flow.MemoryStore
	var nilContext context.Context
	calls := map[string]func(ctx context.Context) error{
		"Load": func(ctx context.Context) error {
			_, _, err := store.Load(ctx, "r", "s")
			return err
		},
		"Save":   func(ctx context.Context) error { return store.Save(ctx, "r", "s", flow.CheckpointRecord{}) },
		"Delete": func(ctx context.Context) error { return store.Delete(ctx, "r", "s") },
	}
	for name, call := range calls {
		wantKind(t, call(nilContext), schemaflux.ErrInvalidRequest)
		for _, done := range doneContexts() {
			err := call(done.ctx)
			wantKind(t, err, done.kind)
			if !errors.Is(err, done.is) {
				t.Errorf("%s, %s: errors.Is(err, %v) is false", name, done.name, done.is)
			}
		}
	}
	if _, ok, _ := store.Load(context.Background(), "r", "s"); ok {
		t.Error("a save with a context that is over kept a record")
	}
}

func TestMemoryStoreIsSafeForSeveralGoroutines(t *testing.T) {
	var store flow.MemoryStore
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			ctx := context.Background()
			for i := range 100 {
				run := fmt.Sprint("run", g)
				step := fmt.Sprint("step", i%5)
				_ = store.Save(ctx, run, step, flow.CheckpointRecord{InputHash: "h", Value: []byte{byte(i)}})
				_, _, _ = store.Load(ctx, run, step)
				if i%7 == 0 {
					_ = store.Delete(ctx, run, step)
				}
			}
		})
	}
	wg.Wait()
}

package op_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
)

func TestRunBatchReadsEveryItemOutOfOneRequestInTheOrderOfTheItems(t *testing.T) {
	t.Parallel()
	model := batchAnswering()
	got, err := op.RunBatch(clientOn(model), batchDescriptor(), []string{"A-1", "B-2", "C-3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Number != "A-1" || got[1].Number != "B-2" || got[2].Number != "C-3" {
		t.Errorf("got %+v", got)
	}
	if model.Calls() != 1 {
		t.Errorf("%d requests, want 1", model.Calls())
	}
	req := model.Last()
	if !strings.Contains(req.System, "Read each invoice") || !strings.Contains(req.User, "Process these items:") {
		t.Errorf("the request is not the id protocol's: %q / %q", req.System, req.User)
	}
}

func TestRunBatchAddsOneRecordWithTheBatchedStrategy(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	if _, err := op.RunBatch(clientOn(batchAnswering(), recordingTo(&rec)), batchDescriptor(), []string{"A-1", "B-2"}); err != nil {
		t.Fatal(err)
	}
	r := onlyRecord(t, &rec)
	if r.Op != "read-invoice" || r.Strategy != "batched" || r.Requests != 1 || r.Err != nil {
		t.Errorf("record = op %q strategy %q requests %d err %v", r.Op, r.Strategy, r.Requests, r.Err)
	}
	if r.Tokens.Input != 11 || r.Tokens.Output != 3 {
		t.Errorf("tokens = %+v", r.Tokens)
	}
}

func TestRunBatchNamesTheRecordOpRunbatchWhenTheDescriptorHasNoName(t *testing.T) {
	t.Parallel()
	d := batchDescriptor()
	d.ID = op.ID{}
	var rec schemaflux.Recorder
	if _, err := op.RunBatch(clientOn(batchAnswering(), recordingTo(&rec)), d, []string{"A-1"}); err != nil {
		t.Fatal(err)
	}
	if got := onlyRecord(t, &rec).Op; got != "op.runbatch" {
		t.Errorf("Op = %q", got)
	}
}

func TestRunBatchOfNoItemsSendsNothingAndReturnsAnEmptySlice(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := batchAnswering()
	got, err := op.RunBatch(clientOn(model, recordingTo(&rec)), batchDescriptor(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want an empty slice that is not nil", got)
	}
	if model.Calls() != 0 {
		t.Error("a request was sent")
	}
	if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err != nil {
		t.Errorf("record = %+v", r)
	}
}

func TestRunBatchFailsWholeWhenAnIdIsNotAnswered(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := answeringWith(`{"items":[]}`)
	got, err := op.RunBatch(clientOn(model, recordingTo(&rec)), batchDescriptor(), []string{"A-1", "B-2"})
	if err == nil {
		t.Fatal("want an error")
	}
	var e *schemaflux.Error
	if !errors.As(err, &e) || e.Op != "read-invoice" {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("outputs beside an error: %+v", got)
	}
	if r := onlyRecord(t, &rec); r.Err == nil || r.Requests != 1 {
		t.Errorf("record = %+v", r)
	}
}

func TestRunBatchRunsAHandWrittenEncodeAndMerge(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Batch = op.Batch[string, invoice]{
		Class: op.BatchItemwise, Kind: op.AlgebraIndependent,
		Encode: func(items []string, o op.Options) (string, string, error) {
			return "Read these.", strings.Join(items, "\n") + " | " + o.Steering, nil
		},
		Merge: func(body string, items []string) ([]invoice, error) {
			var out []invoice
			err := json.Unmarshal([]byte(body), &out)
			return out, err
		},
	}
	model := answeringWith(`[{"number":"A","total":1},{"number":"B","total":2}]`)
	got, err := op.RunBatch(clientOn(model), d, []string{"x", "y"}, op.Options{Options: schemaflux.Options{Steering: "plainly"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Number != "B" {
		t.Errorf("got %+v", got)
	}
	if user := model.Last().User; !strings.Contains(user, "x\ny") || !strings.Contains(user, "plainly") {
		t.Errorf("the Encode was not called with the items and the options: %q", user)
	}
}

func TestRunBatchOfAHandWrittenBatchIsDeliveredAtPromptOnlyAndRefusedUnderAHigherFloor(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Batch = op.Batch[string, invoice]{
		Class: op.BatchItemwise, Kind: op.AlgebraIndependent,
		Encode: func(items []string, _ op.Options) (string, string, error) { return "s", strings.Join(items, ","), nil },
		Merge: func(string, []string) ([]invoice, error) {
			return []invoice{{Number: "A"}}, nil
		},
	}
	var rec schemaflux.Recorder
	model := namedModel("ignored")
	if _, err := op.RunBatch(clientOn(model, recordingTo(&rec), underFloor(schemaflux.ContractPromptOnly)), d, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if r := onlyRecord(t, &rec); r.Requested != schemaflux.ContractPromptOnly {
		t.Errorf("requested %v, want prompt only for a merge of the caller's own", r.Requested)
	}
	model = namedModel("ignored")
	_, err := op.RunBatch(clientOn(model, underFloor(schemaflux.ContractJSONWellFormed)), d, []string{"x"})
	wantError(t, err, schemaflux.ErrPolicy, "read-invoice")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent under a floor the batch cannot meet", model.Calls())
	}
}

func TestRunBatchOfADescriptorWithNoBatchIsAConfigurationError(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := answeringWith("{}")
	_, err := op.RunBatch(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), []string{"x"})
	wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
	if model.Calls() != 0 {
		t.Error("a request was sent")
	}
	if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err == nil {
		t.Errorf("record = %+v", r)
	}
}

func TestRunBatchRefusesANilContextAndBadOptionsBeforeSending(t *testing.T) {
	t.Parallel()
	model := batchAnswering()
	//lint:ignore SA1012 the test is of a nil context
	_, err := op.RunBatch(nil, batchDescriptor(), []string{"x"}) //nolint:staticcheck
	wantError(t, err, schemaflux.ErrInvalidRequest, "read-invoice")
	_, err = op.RunBatch(clientOn(model), batchDescriptor(), []string{"x"}, op.Options{Options: schemaflux.Options{MaxOutputTokens: -5}})
	wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
	_, err = op.RunBatch(clientOn(model), batchDescriptor(), []string{"x"}, op.Options{ResponseFormat: "yaml"})
	wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
	d := batchDescriptor()
	d.BuildPrompt = nil
	_, err = op.RunBatch(clientOn(model), d, []string{"x"})
	wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
	if model.Calls() != 0 {
		t.Error("a request was sent")
	}
}

func TestRunBatchHoldsEveryOutputToTheInvariantsAndNormalizesIt(t *testing.T) {
	t.Parallel()
	d := batchDescriptor()
	d.Contract.Invariants = []func(invoice) error{func(v invoice) error {
		if v.Number == "B-2" {
			return errors.New("B-2 is not allowed")
		}
		return nil
	}}
	_, err := op.RunBatch(clientOn(batchAnswering()), d, []string{"A-1", "B-2"})
	if err == nil {
		t.Fatal("an output that breaks an invariant was delivered")
	}
	d.Contract.Invariants = nil
	d.Contract.Normalize = func(v invoice) invoice { v.Number = strings.ToLower(v.Number); return v }
	got, err := op.RunBatch(clientOn(batchAnswering()), d, []string{"A-1"})
	if err != nil || got[0].Number != "a-1" {
		t.Errorf("got %+v, err %v, want the normalized output", got, err)
	}
}

func TestRunBatchMakesTheWholeBatchCheckOfAnAggregate(t *testing.T) {
	t.Parallel()
	d := batchDescriptor()
	d.Batch.Class = op.BatchAggregate
	d.Batch.Kind = op.AlgebraPermutation
	d.Batch.GlobalValidate = func(items []string, outputs []invoice) error { return errors.New("not a permutation") }
	_, err := op.RunBatch(clientOn(batchAnswering()), d, []string{"A-1", "B-2"})
	if err == nil {
		t.Fatal("the whole-batch check was not made")
	}
	d.Batch.GlobalValidate = func(items []string, outputs []invoice) error { return nil }
	if _, err := op.RunBatch(clientOn(batchAnswering()), d, []string{"A-1", "B-2"}); err != nil {
		t.Errorf("a passing check failed the batch: %v", err)
	}
}

func TestRunBatchAnEncodeErrorRefusesTheBatchBeforeSending(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Batch = op.Batch[string, invoice]{
		Class: op.BatchItemwise, Kind: op.AlgebraIndependent,
		Encode: func([]string, op.Options) (string, string, error) { return "", "", errors.New("cannot encode") },
		Merge:  func(string, []string) ([]invoice, error) { return nil, nil },
	}
	model := answeringWith("[]")
	_, err := op.RunBatch(clientOn(model), d, []string{"x"})
	wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
	if model.Calls() != 0 {
		t.Error("a request was sent")
	}
}

func TestRunBatchAFunctionOfTheDescriptorThatPanicsIsAnInternalError(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*op.Descriptor[string, invoice])
		sent   int
	}{
		"Encode": {func(d *op.Descriptor[string, invoice]) {
			*d = invoiceDescriptor()
			d.Batch = op.Batch[string, invoice]{Class: op.BatchItemwise, Kind: op.AlgebraIndependent,
				Encode: func([]string, op.Options) (string, string, error) { panic(canary) },
				Merge:  func(string, []string) ([]invoice, error) { return nil, nil }}
		}, 0},
		"Merge": {func(d *op.Descriptor[string, invoice]) {
			*d = invoiceDescriptor()
			d.Batch = op.Batch[string, invoice]{Class: op.BatchItemwise, Kind: op.AlgebraIndependent,
				Encode: func([]string, op.Options) (string, string, error) { return "s", "u", nil },
				Merge:  func(string, []string) ([]invoice, error) { panic(canary) }}
		}, 1},
		"Item": {func(d *op.Descriptor[string, invoice]) {
			d.Batch.Item = func(json.RawMessage) (invoice, error) { panic(canary) }
		}, 1},
		"GlobalValidate": {func(d *op.Descriptor[string, invoice]) {
			d.Batch.Class = op.BatchAggregate
			d.Batch.GlobalValidate = func([]string, []invoice) error { panic(canary) }
		}, 1},
		"an invariant": {func(d *op.Descriptor[string, invoice]) {
			d.Contract.Invariants = []func(invoice) error{func(invoice) error { panic(canary) }}
		}, 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := batchDescriptor()
			c.mutate(&d)
			model := batchAnswering()
			got, err := op.RunBatch(clientOn(model), d, []string{"A-1"})
			e := wantError(t, err, schemaflux.ErrInternal, "read-invoice")
			if got != nil || strings.Contains(chainText(err), canary) || !strings.Contains(e.Message, "panicked") {
				t.Errorf("got %v, message %q, chain %q", got, e.Message, chainText(err))
			}
			if model.Calls() != c.sent {
				t.Errorf("%d requests, want %d", model.Calls(), c.sent)
			}
		})
	}
}

func TestRunBatchSendsTheCallersOptions(t *testing.T) {
	t.Parallel()
	model := batchAnswering()
	_, err := op.RunBatch(clientOn(model), batchDescriptor(), []string{"A-1"}, op.Options{
		Options:    schemaflux.Options{Model: "pinned", MaxOutputTokens: 55},
		SchemaName: "batch", JSONSchema: map[string]any{"type": "object"}, ResponseFormat: "json",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "pinned" || req.MaxTokens != 55 || req.SchemaName != "batch" || req.Format != "json" || req.Schema["type"] != "object" {
		t.Errorf("request = %+v", req)
	}
}

func TestRunBatchSendsEveryItemWithAnIdOfTheRequestsOwn(t *testing.T) {
	t.Parallel()
	model := batchAnswering()
	if _, err := op.RunBatch(clientOn(model), batchDescriptor(), []string{"x", "y", "z"}); err != nil {
		t.Fatal(err)
	}
	payload := strings.TrimPrefix(model.Last().User, "Process these items:\n")
	var tagged []struct{ ID, Item string }
	if err := json.Unmarshal([]byte(payload), &tagged); err != nil || len(tagged) != 3 {
		t.Fatalf("payload = %q, err %v", payload, err)
	}
	seen := map[string]bool{}
	for _, item := range tagged {
		if item.ID == "" || seen[item.ID] {
			t.Errorf("ids are not distinct: %+v", tagged)
		}
		seen[item.ID] = true
	}
	_ = fmt.Sprint(tagged)
}

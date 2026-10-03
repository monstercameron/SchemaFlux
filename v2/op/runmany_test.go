package op_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
)

func items(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("INV-%03d", i)
	}
	return out
}

func TestRunManyReadsEveryItemAndKeepsTheOrderOfTheItems(t *testing.T) {
	t.Parallel()
	model := echoing()
	got, err := op.RunMany(clientOn(model), invoiceDescriptor(), items(9))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 {
		t.Fatalf("%d outputs, want 9", len(got))
	}
	for i, v := range got {
		if v.Number != fmt.Sprintf("INV-%03d", i) {
			t.Errorf("output %d = %+v: the order of the items was lost", i, v)
		}
	}
	if model.Calls() != 9 {
		t.Errorf("%d requests, want one for each item", model.Calls())
	}
}

func TestRunManyAddsOneRecordThatIsTheSumOfItsRequests(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := echoing()
	if _, err := op.RunMany(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), items(5)); err != nil {
		t.Fatal(err)
	}
	r := onlyRecord(t, &rec)
	if r.Op != "read-invoice" || r.Err != nil {
		t.Errorf("record = op %q err %v", r.Op, r.Err)
	}
	if r.Requests != 5 || r.Tokens.Input != 55 || r.Tokens.Output != 15 || r.Tokens.Total != 70 {
		t.Errorf("record sums = requests %d tokens %+v, want 5 requests and 55 in 15 out 70 total", r.Requests, r.Tokens)
	}
	if r.Strategy != "atomic" {
		t.Errorf("Strategy = %q, want the plan's shape, atomic", r.Strategy)
	}
	if r.Provenance != nil {
		t.Errorf("provenance = %+v, want none: there is no single answer", r.Provenance)
	}
}

func TestRunManyNamesTheRecordOpRunmanyWhenTheDescriptorHasNoName(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.ID = op.ID{}
	var rec schemaflux.Recorder
	if _, err := op.RunMany(clientOn(echoing(), recordingTo(&rec)), d, items(2)); err != nil {
		t.Fatal(err)
	}
	if got := onlyRecord(t, &rec).Op; got != "op.runmany" {
		t.Errorf("Op = %q", got)
	}
}

func TestRunManyPlansABatchedRunWhenTheDescriptorBatchesAndSumsItsChunks(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := batchAnswering()
	got, err := op.RunMany(clientOn(model, recordingTo(&rec)), batchDescriptor(), items(7), op.PlanOptions{MaxItemsPerCall: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 || got[6].Number != "INV-006" {
		t.Errorf("got %+v", got)
	}
	if model.Calls() != 3 {
		t.Errorf("%d requests, want 3 chunks of at most 3 items", model.Calls())
	}
	r := onlyRecord(t, &rec)
	if r.Requests != 3 || r.Strategy != "batched" || r.Tokens.Input != 33 {
		t.Errorf("record = requests %d strategy %q tokens %+v", r.Requests, r.Strategy, r.Tokens)
	}
}

func TestRunManyRecordsAsManyRequestsAsThePlanSaid(t *testing.T) {
	t.Parallel()
	ctx := clientOn(batchAnswering())
	plan, err := op.Preflight(ctx, batchDescriptor(), items(10), op.PlanOptions{MaxItemsPerCall: 4})
	if err != nil || !plan.Eligible || plan.Shape != op.ShapeBatched || len(plan.Chunks) != 3 {
		t.Fatalf("plan = %+v, err %v", plan, err)
	}
	var rec schemaflux.Recorder
	ctx = clientOn(batchAnswering(), recordingTo(&rec))
	if _, err := op.RunMany(ctx, batchDescriptor(), items(10), op.PlanOptions{MaxItemsPerCall: 4}); err != nil {
		t.Fatal(err)
	}
	if r := onlyRecord(t, &rec); r.Requests != plan.MaxCalls || r.Strategy != plan.Shape.String() {
		t.Errorf("record = requests %d strategy %q, the plan said %d calls and %q", r.Requests, r.Strategy, plan.MaxCalls, plan.Shape)
	}
}

func TestRunManyForcesTheAtomicShapeOnABatchableDescriptor(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := batchAnswering()
	if _, err := op.RunMany(clientOn(model, recordingTo(&rec)), batchDescriptor(), items(4), op.PlanOptions{Force: op.ShapeAtomic, ForceReason: "risk"}); err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 4 {
		t.Errorf("%d requests, want 4: one for each item", model.Calls())
	}
	if r := onlyRecord(t, &rec); r.Strategy != "atomic" {
		t.Errorf("Strategy = %q", r.Strategy)
	}
}

func TestRunManyRefusesAShapeTheDescriptorCannotRunBeforeSending(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		d     op.Descriptor[string, invoice]
		force op.Shape
	}{
		"batched with no batch": {invoiceDescriptor(), op.ShapeBatched},
		"split":                 {batchDescriptor(), op.ShapeSplit},
		"global of an itemwise": {batchDescriptor(), op.ShapeGlobal},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec schemaflux.Recorder
			model := echoing()
			got, err := op.RunMany(clientOn(model, recordingTo(&rec)), c.d, items(3), op.PlanOptions{Force: c.force})
			wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
			if got != nil || model.Calls() != 0 {
				t.Errorf("got %v after %d requests", got, model.Calls())
			}
			if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err == nil {
				t.Errorf("record = %+v", r)
			}
		})
	}
}

func TestRunManyRefusesAPlanOverItsBudgetBeforeSending(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := echoing()
	_, err := op.RunMany(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), items(3), op.PlanOptions{Budget: 0.0001})
	wantError(t, err, schemaflux.ErrBudgetExceeded, "read-invoice")
	if model.Calls() != 0 {
		t.Error("a request was sent")
	}
	if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err == nil {
		t.Errorf("record = %+v", r)
	}
}

func TestRunManyFailsWholeOnTheFirstFailedItem(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := echoing()
	d := invoiceDescriptor()
	d.Policy.RepairAttempts = -1
	d.Contract.Invariants = []func(invoice) error{func(v invoice) error {
		if v.Number == "INV-002" {
			return errors.New("refused")
		}
		return nil
	}}
	got, err := op.RunMany(clientOn(model, recordingTo(&rec)), d, items(5))
	wantError(t, err, schemaflux.ErrInvalidAnswer, "read-invoice")
	if got != nil {
		t.Errorf("outputs beside an error: %+v", got)
	}
	if r := onlyRecord(t, &rec); r.Err == nil {
		t.Error("the Record has no error")
	}
}

func TestRunManyOfNoItemsSendsNothing(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := echoing()
	got, err := op.RunMany(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, err %v", got, err)
	}
	if model.Calls() != 0 {
		t.Error("a request was sent")
	}
	if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err != nil {
		t.Errorf("record = %+v", r)
	}
}

func TestRunManyRefusesOptionsNoCallCanUse(t *testing.T) {
	t.Parallel()
	cases := map[string]op.PlanOptions{
		"a negative MaxItemsPerCall": {MaxItemsPerCall: -1},
		"a negative Budget":          {Budget: -1},
		"a Budget that is NaN":       {Budget: nan()},
		"an infinite Budget":         {Budget: inf()},
		"a Force that is no Shape":   {Force: 99},
		"a negative MaxOutputTokens": {Options: op.Options{Options: schemaflux.Options{MaxOutputTokens: -1}}},
		"an unknown ResponseFormat":  {Options: op.Options{ResponseFormat: "xml"}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec schemaflux.Recorder
			model := echoing()
			_, err := op.RunMany(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), items(2), o)
			wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
			if model.Calls() != 0 {
				t.Error("a request was sent")
			}
			if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err == nil {
				t.Errorf("record = %+v", r)
			}
		})
	}
}

func TestRunManyRefusesANilContextAndADescriptorThatCannotRun(t *testing.T) {
	t.Parallel()
	//lint:ignore SA1012 the test is of a nil context
	_, err := op.RunMany(nil, invoiceDescriptor(), items(1)) //nolint:staticcheck
	wantError(t, err, schemaflux.ErrInvalidRequest, "read-invoice")
	d := invoiceDescriptor()
	d.Contract.Decode = nil
	model := echoing()
	_, err = op.RunMany(clientOn(model), d, items(1))
	wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
	if model.Calls() != 0 {
		t.Error("a request was sent")
	}
}

func TestRunManySumsTheRepairsOfEveryItemInOneCheck(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	var model = echoing()
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		body := `{"number":"INV","total":1}`
		if req.User == "INV-001" {
			// The first request for this item is answered with no number. The
			// repair that follows carries what was wrong, and is answered well.
			body = `{"total":1}`
		}
		return schemaflux.Response{Content: body, FinishReason: "stop", Usage: schemaflux.Usage{Input: 11, Output: 3, Total: 14}}, nil
	})
	got, err := op.RunMany(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), items(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	r := onlyRecord(t, &rec)
	if r.Repairs != 1 || r.Requests != 4 {
		t.Errorf("record = repairs %d requests %d, want 1 and 4", r.Repairs, r.Requests)
	}
	if len(r.Checks) != 1 || r.Checks[0].Name != "repair" || r.Checks[0].Passed || !strings.Contains(r.Checks[0].Detail, "1 refused") {
		t.Fatalf("checks = %+v, want one failed repair check with a count", r.Checks)
	}
	if strings.Contains(r.Checks[0].Detail, "no number") {
		t.Errorf("the check holds the text the model was shown: %q", r.Checks[0].Detail)
	}
}

func TestRunManyRefusesUnderAFloorTheDescriptorCannotMeet(t *testing.T) {
	t.Parallel()
	model := namedModel(`{"number":"A"}`)
	_, err := op.RunMany(clientOn(model, underFloor(schemaflux.ContractSchemaConstrained)), invoiceDescriptor(), items(3))
	wantError(t, err, schemaflux.ErrPolicy, "read-invoice")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestRunManyDeliversUnderAFloorTheDescriptorMeets(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := namedModel(`{"number":"A"}`)
	got, err := op.RunMany(clientOn(model, recordingTo(&rec), underFloor(schemaflux.ContractJSONWellFormed)), invoiceDescriptor(), items(3))
	if err != nil || len(got) != 3 {
		t.Fatalf("got %v, err %v", got, err)
	}
	r := onlyRecord(t, &rec)
	if r.Delivered != schemaflux.ContractJSONWellFormed {
		t.Errorf("delivered %v, want the weakest level any item was delivered at", r.Delivered)
	}
}

func TestRunManyAFunctionOfTheDescriptorThatPanicsIsAnInternalErrorAndNeverEndsTheProcess(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*op.Descriptor[string, invoice]){
		"BuildPrompt": func(d *op.Descriptor[string, invoice]) {
			d.BuildPrompt = func(string, op.Options) (string, string) { panic(canary) }
		},
		"Decode": func(d *op.Descriptor[string, invoice]) {
			d.Contract.Decode = func(string) (invoice, error) { panic(canary) }
		},
		"an invariant": func(d *op.Descriptor[string, invoice]) {
			d.Contract.Invariants = []func(invoice) error{func(invoice) error { panic(canary) }}
		},
		"Normalize": func(d *op.Descriptor[string, invoice]) {
			d.Contract.Normalize = func(invoice) invoice { panic(canary) }
		},
		"Item": func(d *op.Descriptor[string, invoice]) {
			*d = batchDescriptor()
			d.Batch.Item = func(json.RawMessage) (invoice, error) { panic(canary) }
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := invoiceDescriptor()
			mutate(&d)
			var rec schemaflux.Recorder
			got, err := op.RunMany(clientOn(batchAnswering(), recordingTo(&rec)), d, items(6))
			e := wantError(t, err, schemaflux.ErrInternal, "read-invoice")
			if got != nil || strings.Contains(chainText(err), canary) || !strings.Contains(e.Message, "panicked") {
				t.Errorf("got %v, message %q, chain %q", got, e.Message, chainText(err))
			}
			if r := onlyRecord(t, &rec); r.Err == nil {
				t.Error("the Record has no error")
			}
		})
	}
}

func TestRunManyRunsManyItemsAtOnceWithoutLosingAnyOrder(t *testing.T) {
	t.Parallel()
	got, err := op.RunMany(clientOn(echoing()), invoiceDescriptor(), items(200))
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if v.Number != fmt.Sprintf("INV-%03d", i) {
			t.Fatalf("output %d = %+v", i, v)
		}
	}
}

func TestRunManyPutsNoItemInTheRecordOrTheError(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	d := invoiceDescriptor()
	d.Policy.RepairAttempts = -1
	model := answeringWith(`{"total":1}`)
	_, err := op.RunMany(clientOn(model, recordingTo(&rec)), d, []string{canary, canary + "2"})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(chainText(err), canary) {
		t.Errorf("the error carries an item: %s", chainText(err))
	}
	r := onlyRecord(t, &rec)
	if strings.Contains(fmt.Sprint(r.Checks), canary) || strings.Contains(r.Op, canary) {
		t.Errorf("the record carries an item: %+v", r)
	}
}

func nan() float64 { return math.NaN() }
func inf() float64 { return math.Inf(1) }

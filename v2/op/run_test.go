package op_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

const goodInvoice = `{"number":"A-17","total":120.5}`

func TestRunReadsTheAnswerIntoTheDescriptorsType(t *testing.T) {
	t.Parallel()
	model := answeringWith(goodInvoice)
	got, err := op.Run(clientOn(model), invoiceDescriptor(), "Invoice A-17 for 120.50")
	if err != nil {
		t.Fatal(err)
	}
	if got != (invoice{Number: "A-17", Total: 120.5}) {
		t.Errorf("got %+v", got)
	}
	req := model.Last()
	if req.System != "Read the invoice. Return a JSON object with number and total." && !strings.Contains(req.System, "Read the invoice.") {
		t.Errorf("the system prompt does not hold the descriptor's: %q", req.System)
	}
	if !strings.Contains(req.User, "Invoice A-17 for 120.50") {
		t.Errorf("the user prompt does not hold the input: %q", req.User)
	}
}

func TestRunAddsOneRecordNamedForTheDescriptor(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := echoing()
	if _, err := op.Run(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), "A-17"); err != nil {
		t.Fatal(err)
	}
	r := onlyRecord(t, &rec)
	if r.Op != "read-invoice" || r.Err != nil || r.Requests != 1 || r.Repairs != 0 {
		t.Errorf("record = op %q err %v requests %d repairs %d", r.Op, r.Err, r.Requests, r.Repairs)
	}
	if r.Tokens.Input != 11 || r.Tokens.Output != 3 {
		t.Errorf("tokens = %+v, want 11 in and 3 out", r.Tokens)
	}
	if r.Provenance == nil || r.Provenance.ResultID == "" || r.Provenance.OperationVersion != "read-invoice@1" {
		t.Errorf("provenance = %+v", r.Provenance)
	}
	if r.Provenance != nil && r.Provenance.ItemRecoveryPath != "first attempt" {
		t.Errorf("recovery path = %q", r.Provenance.ItemRecoveryPath)
	}
}

func TestRunNamesTheRecordOpRunWhenTheDescriptorHasNoName(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.ID = op.ID{}
	var rec schemaflux.Recorder
	if _, err := op.Run(clientOn(echoing(), recordingTo(&rec)), d, "A-17"); err != nil {
		t.Fatal(err)
	}
	if got := onlyRecord(t, &rec).Op; got != "op.run" {
		t.Errorf("Op = %q, want op.run", got)
	}
}

func TestRunRepairsARefusedAnswerAndReportsItInTheRecordsChecks(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := answeringWith(`{"total":3}`, goodInvoice)
	got, err := op.Run(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), "A-17")
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != "A-17" {
		t.Errorf("got %+v", got)
	}
	r := onlyRecord(t, &rec)
	if r.Requests != 2 || r.Repairs != 1 {
		t.Errorf("requests %d repairs %d, want 2 and 1", r.Requests, r.Repairs)
	}
	if len(r.Checks) != 1 || r.Checks[0].Name != "repair" || r.Checks[0].Passed {
		t.Fatalf("checks = %+v, want one failed repair check", r.Checks)
	}
	if !strings.Contains(r.Checks[0].Detail, "no number") {
		t.Errorf("the check does not say what was wrong: %q", r.Checks[0].Detail)
	}
	if model.Calls() != 2 || !strings.Contains(model.Last().User, "no number") {
		t.Errorf("the second request did not show the model what was wrong: %q", model.Last().User)
	}
	if r.Provenance == nil || !strings.HasPrefix(r.Provenance.ItemRecoveryPath, "repaired") {
		t.Errorf("provenance = %+v, want a repaired recovery path", r.Provenance)
	}
}

func TestRunEndsWithErrInvalidAnswerWhenRepairsRunOut(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := answeringWith(`{"total":3}`)
	got, err := op.Run(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), "A-17")
	wantError(t, err, schemaflux.ErrInvalidAnswer, "read-invoice")
	if got != (invoice{}) {
		t.Errorf("a value beside an error: %+v", got)
	}
	r := onlyRecord(t, &rec)
	if r.Err == nil || r.Requests != 2 {
		t.Errorf("record = err %v requests %d, want an error and 2 requests", r.Err, r.Requests)
	}
}

func TestRunDoesNotRepairWhenThePolicySaysNone(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Policy.RepairAttempts = -1
	model := answeringWith(`{"total":3}`, goodInvoice)
	_, err := op.Run(clientOn(model), d, "A-17")
	wantError(t, err, schemaflux.ErrInvalidAnswer, "read-invoice")
	if model.Calls() != 1 {
		t.Errorf("%d requests, want 1", model.Calls())
	}
}

func TestRunRepairsAsOftenAsThePolicyAllows(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Policy.RepairAttempts = 3
	model := answeringWith(`{"total":1}`, `{"total":2}`, `{"total":3}`, goodInvoice)
	if _, err := op.Run(clientOn(model), d, "A-17"); err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 4 {
		t.Errorf("%d requests, want 4: the first and three repairs", model.Calls())
	}
}

func TestRunHoldsTheAnswerToTheInvariantsAndRunsNormalizeLast(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Contract.Invariants = []func(invoice) error{
		func(v invoice) error {
			if v.Total <= 0 {
				return errors.New("the total is not positive")
			}
			return nil
		},
	}
	d.Contract.Normalize = func(v invoice) invoice {
		v.Number = strings.ToUpper(v.Number)
		return v
	}
	model := answeringWith(`{"number":"a-1","total":-4}`, `{"number":"a-1","total":4}`)
	got, err := op.Run(clientOn(model), d, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got != (invoice{Number: "A-1", Total: 4}) {
		t.Errorf("got %+v, want the repaired and normalized answer", got)
	}
}

func TestRunNamesTheCallersNormalizeInTheProvenanceAndNotAWrapper(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Contract.Normalize = trimNumber
	var rec schemaflux.Recorder
	if _, err := op.Run(clientOn(echoing(), recordingTo(&rec)), d, "A-17"); err != nil {
		t.Fatal(err)
	}
	p := onlyRecord(t, &rec).Provenance
	if p == nil || len(p.NormalizersApplied) != 1 || !strings.HasSuffix(p.NormalizersApplied[0], "trimNumber") {
		t.Errorf("provenance = %+v, want the symbol of trimNumber", p)
	}
}

func trimNumber(v invoice) invoice {
	v.Number = strings.TrimSpace(v.Number)
	return v
}

func TestRunRefusesANilContext(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	d := invoiceDescriptor()
	d.BuildPrompt = func(string, op.Options) (string, string) { built.Add(1); return "s", "u" }
	//lint:ignore SA1012 the test is of a nil context
	got, err := op.Run(nil, d, "x") //nolint:staticcheck
	e := wantError(t, err, schemaflux.ErrInvalidRequest, "read-invoice")
	if !strings.Contains(e.Message, "context is nil") {
		t.Errorf("message = %q", e.Message)
	}
	if got != (invoice{}) || built.Load() != 0 {
		t.Errorf("got %+v after %d renderings: a refused call does nothing", got, built.Load())
	}
}

func TestRunRefusesADescriptorThatCannotRunBeforeSendingAnything(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*op.Descriptor[string, invoice]){
		"no BuildPrompt":        func(d *op.Descriptor[string, invoice]) { d.BuildPrompt = nil },
		"no Decode":             func(d *op.Descriptor[string, invoice]) { d.Contract.Decode = nil },
		"a nil invariant":       func(d *op.Descriptor[string, invoice]) { d.Contract.Invariants = []func(invoice) error{nil} },
		"a batch class unknown": func(d *op.Descriptor[string, invoice]) { d.Batch.Class = 9 },
		"item beside Encode":    func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor(); d.Batch.Encode = nilEncode },
		"item with no prompt":   func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor(); d.Batch.Instructions = "" },
		"instructions, no item": func(d *op.Descriptor[string, invoice]) { d.Batch.Instructions = "x" },
		"a category unknown":    func(d *op.Descriptor[string, invoice]) { d.Semantics.Category = 99 },
		"a stability unknown":   func(d *op.Descriptor[string, invoice]) { d.Semantics.Stability = 99 },
		"an algebra unknown":    func(d *op.Descriptor[string, invoice]) { d.Batch.Kind = 99 },
		"item beside Merge":     func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor(); d.Batch.Merge = nilMerge },
		"a nil invariant, later": func(d *op.Descriptor[string, invoice]) {
			d.Contract.Invariants = []func(invoice) error{okInvariant, nil}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := invoiceDescriptor()
			mutate(&d)
			var rec schemaflux.Recorder
			model := answeringWith(goodInvoice)
			got, err := op.Run(clientOn(model, recordingTo(&rec)), d, "x")
			wantError(t, err, schemaflux.ErrConfiguration, "read-invoice")
			if got != (invoice{}) {
				t.Errorf("a value beside an error: %+v", got)
			}
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
			if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err == nil || r.Op != "read-invoice" {
				t.Errorf("record = %+v, want a refusal with no request", r)
			}
		})
	}
}

func nilEncode(items []string, _ op.Options) (string, string, error) { return "s", "u", nil }
func nilMerge(string, []string) ([]invoice, error)                   { return nil, nil }
func okInvariant(invoice) error                                      { return nil }

func TestRunRefusesAnOptionThatNoCallCanUse(t *testing.T) {
	t.Parallel()
	cases := map[string]op.Options{
		"a negative MaxOutputTokens": {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a Tier that is not one":     {Options: schemaflux.Options{Tier: 9}},
		"a Mode that is not one":     {Options: schemaflux.Options{Mode: 9}},
		"an unknown ResponseFormat":  {ResponseFormat: "xml"},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec schemaflux.Recorder
			model := answeringWith(goodInvoice)
			_, err := op.Run(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), "x", o)
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

func TestRunSendsTheCallersSchemaSchemaNameAndResponseFormat(t *testing.T) {
	t.Parallel()
	schema := map[string]any{"type": "object", "properties": map[string]any{"number": map[string]any{"type": "string"}}}
	model := answeringWith(goodInvoice)
	_, err := op.Run(clientOn(model), invoiceDescriptor(), "x", op.Options{JSONSchema: schema, SchemaName: "inv", ResponseFormat: "json"})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.SchemaName != "inv" || req.Format != "json" || req.Schema["type"] != "object" {
		t.Errorf("request = schema name %q format %q schema %v", req.SchemaName, req.Format, req.Schema)
	}
}

func TestRunSendsNoSchemaWhenTheCallerGivesNone(t *testing.T) {
	t.Parallel()
	model := answeringWith(goodInvoice)
	if _, err := op.Run(clientOn(model), invoiceDescriptor(), "x"); err != nil {
		t.Fatal(err)
	}
	if req := model.Last(); req.Schema != nil || req.SchemaName != "" {
		t.Errorf("request = schema %v name %q, want none: Contract.SchemaName names the contract and sends nothing", req.Schema, req.SchemaName)
	}
}

func TestRunSendsTheCommonSettings(t *testing.T) {
	t.Parallel()
	model := answeringWith(goodInvoice)
	_, err := op.Run(clientOn(model), invoiceDescriptor(), "x", op.Options{Options: schemaflux.Options{
		Model: "pinned", Tier: schemaflux.TierSmart, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict,
		Steering: "answer plainly", MaxOutputTokens: 77, WebSearch: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "pinned" || req.MaxTokens != 77 || !req.WebSearch || req.Effort != schemaflux.EffortLow {
		t.Errorf("request = model %q max %d web %v effort %q", req.Model, req.MaxTokens, req.WebSearch, req.Effort)
	}
	if !strings.Contains(req.User, "answer plainly") {
		t.Errorf("the steering did not reach the user prompt: %q", req.User)
	}
}

func TestRunMergesSeveralOptionValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	model := answeringWith(goodInvoice)
	first := op.Options{Options: schemaflux.Options{Model: "first", MaxOutputTokens: 10, WebSearch: true}, SchemaName: "one"}
	second := op.Options{Options: schemaflux.Options{Model: "second"}, ResponseFormat: "json"}
	if _, err := op.Run(clientOn(model), invoiceDescriptor(), "x", first, second); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "second" || req.MaxTokens != 10 || !req.WebSearch || req.SchemaName != "one" || req.Format != "json" {
		t.Errorf("request = %+v", req)
	}
}

func TestRunGivesTheDescriptorTheOptionsOfTheCall(t *testing.T) {
	t.Parallel()
	var seen op.Options
	d := invoiceDescriptor()
	d.BuildPrompt = func(text string, o op.Options) (string, string) {
		seen = o
		return "system", text
	}
	schema := map[string]any{"type": "object"}
	want := op.Options{Options: schemaflux.Options{Model: "m", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "s", MaxOutputTokens: 5, WebSearch: true},
		JSONSchema: schema, SchemaName: "n", ResponseFormat: "text"}
	if _, err := op.Run(clientOn(answeringWith(goodInvoice)), d, "x", want); err != nil {
		t.Fatal(err)
	}
	if seen.Options != want.Options || seen.SchemaName != "n" || seen.ResponseFormat != "text" || seen.JSONSchema["type"] != "object" {
		t.Errorf("the descriptor was given %+v, want %+v", seen, want)
	}
}

func TestRunCarriesAProvidersErrorKind(t *testing.T) {
	t.Parallel()
	var model = answeringWith()
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429})
	var rec schemaflux.Recorder
	_, err := op.Run(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), "x")
	wantError(t, err, schemaflux.ErrRateLimited, "read-invoice")
	if r := onlyRecord(t, &rec); r.Err == nil {
		t.Error("the Record has no error")
	}
}

func TestRunDeliversAtWellFormedWhenTheSchemaWasNeverSent(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Contract.Invariants = []func(invoice) error{okInvariant}
	var rec schemaflux.Recorder
	if _, err := op.Run(clientOn(echoing(), recordingTo(&rec)), d, "A-17"); err != nil {
		t.Fatal(err)
	}
	r := onlyRecord(t, &rec)
	if r.Delivered != schemaflux.ContractJSONWellFormed || r.Requested != schemaflux.ContractSchemaAndInvariantChecked {
		t.Errorf("requested %v delivered %v, want the declared level asked for and well formed delivered: the schema was named and never sent", r.Requested, r.Delivered)
	}
}

func TestRunRefusesBeforeSendingWhenTheFloorIsAboveWhatTheDescriptorDelivers(t *testing.T) {
	t.Parallel()
	for name, recorded := range map[string]bool{"with a recorder": true, "without one": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec schemaflux.Recorder
			model := namedModel(goodInvoice)
			settings := []func(*schemaflux.Client){underFloor(schemaflux.ContractSchemaConstrained)}
			if recorded {
				settings = append(settings, recordingTo(&rec))
			}
			_, err := op.Run(clientOn(model, settings...), invoiceDescriptor(), "x")
			wantError(t, err, schemaflux.ErrPolicy, "read-invoice")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
			if recorded {
				if r := onlyRecord(t, &rec); r.Requests != 0 || r.Err == nil {
					t.Errorf("record = %+v", r)
				}
			}
		})
	}
}

func TestRunDeliversUnderAFloorTheDescriptorMeets(t *testing.T) {
	t.Parallel()
	model := namedModel(goodInvoice)
	got, err := op.Run(clientOn(model, underFloor(schemaflux.ContractJSONWellFormed)), invoiceDescriptor(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != "A-17" || model.Calls() != 1 {
		t.Errorf("got %+v after %d requests", got, model.Calls())
	}
}

// operationsSeenDuring runs call with an observer installed and returns the
// operation scopes that finished, by name. The test that calls it is not parallel:
// an observer changes the path of every call that runs while it is installed.
func operationsSeenDuring(t *testing.T, call func()) []string {
	t.Helper()
	var names []string
	var mu sync.Mutex
	defer telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			mu.Lock()
			names = append(names, r.Operation)
			mu.Unlock()
		}
	}})()
	call()
	mu.Lock()
	defer mu.Unlock()
	return names
}

func TestEachRunnerIsOneOperationScopeForAnObserverNamedForTheDescriptor(t *testing.T) {
	cases := map[string]func(){
		"Run": func() { _, _ = op.Run(clientOn(echoing()), invoiceDescriptor(), "x") },
		"RunBatch": func() {
			_, _ = op.RunBatch(clientOn(batchAnswering()), batchDescriptor(), []string{"x", "y"})
		},
		"RunMany, atomic": func() { _, _ = op.RunMany(clientOn(echoing()), invoiceDescriptor(), items(3)) },
		"RunMany, batched": func() {
			_, _ = op.RunMany(clientOn(batchAnswering()), batchDescriptor(), items(5), op.PlanOptions{MaxItemsPerCall: 2})
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			names := operationsSeenDuring(t, call)
			if len(names) != 1 || names[0] != "read-invoice" {
				t.Errorf("the observer saw %v finish, want one operation named read-invoice", names)
			}
		})
	}
}

func TestARunnerOfADescriptorWithNoNameIsAnOperationNamedForTheRunner(t *testing.T) {
	d := invoiceDescriptor()
	d.ID.Name = ""
	for name, call := range map[string]func(){
		"op.run":     func() { _, _ = op.Run(clientOn(echoing()), d, "x") },
		"op.runmany": func() { _, _ = op.RunMany(clientOn(echoing()), d, items(2)) },
	} {
		if names := operationsSeenDuring(t, call); len(names) != 1 || names[0] != name {
			t.Errorf("%s: the observer saw %v", name, names)
		}
	}
}

func TestPreflightIsNoOperationToAnObserver(t *testing.T) {
	names := operationsSeenDuring(t, func() { _, _ = op.Preflight(clientOn(echoing()), invoiceDescriptor(), items(3)) })
	if len(names) != 0 {
		t.Errorf("the observer saw %v finish: Preflight asks no model", names)
	}
}

func TestRunAFunctionOfTheDescriptorThatPanicsIsAnInternalError(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*op.Descriptor[string, invoice])
		sent   int
	}{
		"BuildPrompt": {func(d *op.Descriptor[string, invoice]) {
			d.BuildPrompt = func(string, op.Options) (string, string) { panic(canary) }
		}, 0},
		"Decode": {func(d *op.Descriptor[string, invoice]) {
			d.Contract.Decode = func(string) (invoice, error) { panic(canary) }
		}, 1},
		"an invariant": {func(d *op.Descriptor[string, invoice]) {
			d.Contract.Invariants = []func(invoice) error{func(invoice) error { panic(canary) }}
		}, 1},
		"Normalize": {func(d *op.Descriptor[string, invoice]) {
			d.Contract.Normalize = func(invoice) invoice { panic(canary) }
		}, 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := invoiceDescriptor()
			c.mutate(&d)
			var rec schemaflux.Recorder
			model := answeringWith(goodInvoice)
			got, err := op.Run(clientOn(model, recordingTo(&rec)), d, "x")
			e := wantError(t, err, schemaflux.ErrInternal, "read-invoice")
			if got != (invoice{}) {
				t.Errorf("a value beside an error: %+v", got)
			}
			if !strings.Contains(e.Message, "panicked") || strings.Contains(chainText(err), canary) {
				t.Errorf("message = %q, chain = %q", e.Message, chainText(err))
			}
			if model.Calls() != c.sent {
				t.Errorf("%d requests were sent, want %d: a panic is not repaired", model.Calls(), c.sent)
			}
			if r := onlyRecord(t, &rec); r.Err == nil || r.Op != "read-invoice" {
				t.Errorf("record = %+v", r)
			}
		})
	}
}

func TestRunDoesNotBlameTheDescriptorForAPanicOfTheProvider(t *testing.T) {
	t.Parallel()
	model := answeringWith()
	model.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { panic(canary) })
	_, err := op.Run(clientOn(model), invoiceDescriptor(), "x")
	e := wantError(t, err, schemaflux.ErrInternal, "read-invoice")
	if strings.Contains(e.Message, "descriptor") || strings.Contains(chainText(err), canary) {
		t.Errorf("message = %q, chain = %q", e.Message, chainText(err))
	}
}

func TestRunPutsTheParentsTheContextCarriesInTheProvenance(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	ctx := op.WithParents(clientOn(echoing(), recordingTo(&rec)), "r-1", "r-2")
	if _, err := op.Run(ctx, invoiceDescriptor(), "x"); err != nil {
		t.Fatal(err)
	}
	p := onlyRecord(t, &rec).Provenance
	if p == nil || len(p.ParentResultIDs) != 2 || p.ParentResultIDs[0] != "r-1" || p.ParentResultIDs[1] != "r-2" {
		t.Errorf("provenance = %+v, want the parents r-1 and r-2", p)
	}
}

func TestRunPutsNoPayloadInTheRecordOrTheError(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := answeringWith(`{"total":1}`)
	_, err := op.Run(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), canary)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(chainText(err), canary) {
		t.Errorf("the error carries the input: %s", chainText(err))
	}
	r := onlyRecord(t, &rec)
	if strings.Contains(r.Provenance.InputDigest, canary) || strings.Contains(r.Op+r.Model+r.Provider, canary) {
		t.Errorf("the record carries the input: %+v", r)
	}
}

func TestRunSendsTheSameRequestOnEveryCall(t *testing.T) {
	t.Parallel()
	model := answeringWith(goodInvoice)
	for range 3 {
		if _, err := op.Run(clientOn(model), invoiceDescriptor(), "same"); err != nil {
			t.Fatal(err)
		}
	}
	reqs := model.Requests()
	if reqs[0].System != reqs[1].System || reqs[1].User != reqs[2].User {
		t.Error("the requests differ")
	}
}

func TestRunOnTheContextOfADoneCallerEndsWithTheContextsKindAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(goodInvoice)
	ctx, cancel := context.WithCancel(clientOn(model))
	cancel()
	_, err := op.Run(ctx, invoiceDescriptor(), "x")
	if err == nil {
		t.Fatal("want an error from a cancelled context")
	}
	var e *schemaflux.Error
	if !errors.As(err, &e) || (e.Kind != schemaflux.ErrCanceled && e.Kind != schemaflux.ErrTimeout) {
		t.Errorf("err = %v, want ErrCanceled", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) is false: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent on a cancelled context", model.Calls())
	}
}

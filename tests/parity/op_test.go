package parity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
)

// The runners of a caller's own operation: op.Run, op.RunBatch and op.RunMany
// against the old root's RunOp, RunOpBatch and RunOpMany, and op.Preflight
// against the planner that plans for a client. Each pair runs one descriptor
// written twice, once in each root's types, from the same pieces, and each of its
// functions writes the options it is given into the prompt it renders, so that a
// difference in what a descriptor's own function is handed is a difference in the
// request the model is sent.

type customInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

const (
	customOpName   = "parity-invoice"
	customOpAnswer = `{"number":"A-17","total":120.5}`
	customOpBad    = `{"total":1}`
	customOpInput  = "Invoice A-17 for 120.50"
)

func customOpDecode(body string) (customInvoice, error) {
	var v customInvoice
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return customInvoice{}, errors.New("the answer is not a JSON object with number and total")
	}
	if v.Number == "" {
		return customInvoice{}, errors.New("the answer has no number")
	}
	return v, nil
}

func customOpNotNegative(v customInvoice) error {
	if v.Total < 0 {
		return errors.New("the total is negative")
	}
	return nil
}

func customOpUpper(v customInvoice) customInvoice {
	v.Number = strings.ToUpper(v.Number)
	return v
}

func customOpSystem(model, tier, effort, mode, steering string, max int, web bool, schemaName, format string, hasSchema bool) string {
	return fmt.Sprintf("Read the invoice. Return a JSON object with number and total. [model=%s tier=%s effort=%s mode=%s steering=%s max=%d web=%v name=%s format=%s schema=%v]",
		model, tier, effort, mode, steering, max, web, schemaName, format, hasSchema)
}

func customOpV2Tier(t schemaflux.Tier) string {
	return [...]string{"unset", "smart", "fast", "quick"}[t]
}

func customOpV2Mode(m schemaflux.Mode) string {
	return [...]string{"unset", "strict", "transform", "creative"}[m]
}

// customOpOld is the descriptor as the old root takes it.
func customOpOld(batched bool, tweak func(*oldroot.Op[string, customInvoice])) oldroot.Op[string, customInvoice] {
	d := oldroot.Op[string, customInvoice]{
		ID:       oldroot.OperationID{Name: customOpName, Version: "1"},
		Contract: oldroot.OutputContract[customInvoice]{SchemaName: "invoice", Decode: customOpDecode},
		BuildPrompt: func(in string, o oldroot.OpOptions) (string, string) {
			return customOpSystem(o.Model, o.Intelligence.String(), string(o.Effort), o.Mode.String(), o.Steering, o.MaxOutputTokens, o.WebSearch, o.SchemaName, o.ResponseFormat, o.JSONSchema != nil), in
		},
	}
	if batched {
		d.Batch = oldroot.BatchAlgebra[string, customInvoice]{
			Class: oldroot.BatchItemwise, Kind: oldroot.AlgebraIndependent,
			Encode: func(items []string, o oldroot.OpOptions) (string, string, error) {
				return customOpSystem(o.Model, o.Intelligence.String(), string(o.Effort), o.Mode.String(), o.Steering, o.MaxOutputTokens, o.WebSearch, o.SchemaName, o.ResponseFormat, o.JSONSchema != nil),
					strings.Join(items, "\n"), nil
			},
			Merge: customOpMerge,
		}
	}
	if tweak != nil {
		tweak(&d)
	}
	return d
}

func customOpMerge(body string, _ []string) ([]customInvoice, error) {
	var out []customInvoice
	err := json.Unmarshal([]byte(body), &out)
	return out, err
}

// customOpNew is the same descriptor as version 2 takes it.
func customOpNew(batched bool, tweak func(*op.Descriptor[string, customInvoice])) op.Descriptor[string, customInvoice] {
	d := op.Descriptor[string, customInvoice]{
		ID:       op.ID{Name: customOpName, Version: "1"},
		Contract: op.Contract[customInvoice]{SchemaName: "invoice", Decode: customOpDecode},
		BuildPrompt: func(in string, o op.Options) (string, string) {
			return customOpSystem(o.Model, customOpV2Tier(o.Tier), string(o.Effort), customOpV2Mode(o.Mode), o.Steering, o.MaxOutputTokens, o.WebSearch, o.SchemaName, o.ResponseFormat, o.JSONSchema != nil), in
		},
	}
	if batched {
		d.Batch = op.Batch[string, customInvoice]{
			Class: op.BatchItemwise, Kind: op.AlgebraIndependent,
			Encode: func(items []string, o op.Options) (string, string, error) {
				return customOpSystem(o.Model, customOpV2Tier(o.Tier), string(o.Effort), customOpV2Mode(o.Mode), o.Steering, o.MaxOutputTokens, o.WebSearch, o.SchemaName, o.ResponseFormat, o.JSONSchema != nil),
					strings.Join(items, "\n"), nil
			},
			Merge: customOpMerge,
		}
	}
	if tweak != nil {
		tweak(&d)
	}
	return d
}

// customOpSettings are the three settings only a descriptor's author uses, as the
// old root sets them on its options and version 2 on its own.
type customOpSetting struct {
	name string
	old  func(*types.OpOptions)
	v2   op.Options
}

func customOpSettings() []customOpSetting {
	schema := map[string]any{"type": "object", "properties": map[string]any{"number": map[string]any{"type": "string"}, "total": map[string]any{"type": "number"}}}
	return []customOpSetting{
		{"JSONSchema", func(o *types.OpOptions) { o.JSONSchema = schema }, op.Options{JSONSchema: schema}},
		{"SchemaName", func(o *types.OpOptions) { o.SchemaName = "named" }, op.Options{SchemaName: "named"}},
		{"ResponseFormat json", func(o *types.OpOptions) { o.ResponseFormat = "json" }, op.Options{ResponseFormat: "json"}},
		{"ResponseFormat text", func(o *types.OpOptions) { o.ResponseFormat = "text" }, op.Options{ResponseFormat: "text"}},
		{"the schema, its name and the format together",
			func(o *types.OpOptions) { o.JSONSchema, o.SchemaName, o.ResponseFormat = schema, "named", "json" },
			op.Options{JSONSchema: schema, SchemaName: "named", ResponseFormat: "json"}},
	}
}

// customOpOptionCases is every option pair the runners share: the default, each of
// the seven common settings, all of them at once, and the three of the author's.
type customOpOptionCase struct {
	name string
	old  types.OpOptions
	v2   op.Options
}

func customOpOptionCases() []customOpOptionCase {
	cases := []customOpOptionCase{{name: "the default"}}
	for _, c := range commonCases {
		var o types.OpOptions
		c.decision(&o)
		cases = append(cases, customOpOptionCase{name: c.name, old: o, v2: op.Options{Options: c.v2}})
	}
	for _, s := range customOpSettings() {
		var o types.OpOptions
		s.old(&o)
		cases = append(cases, customOpOptionCase{name: s.name, old: o, v2: s.v2})
	}
	return cases
}

// ---- Run ----

func customRunCase(name string, replies []string, old types.OpOptions, tweakOld func(*oldroot.Op[string, customInvoice]), v2 op.Options, tweakNew func(*op.Descriptor[string, customInvoice])) parityCase {
	return parityCase{
		name: "Run/" + name, op: customOpName, replies: replies, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			_, _, err := oldroot.RunOp(ctx, customOpOld(false, tweakOld), customOpInput, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := op.Run(ctx, customOpNew(false, tweakNew), customOpInput, v2)
			return err
		},
	}
}

func TestRunSendsTheRequestTheOldRootsRunOpSends(t *testing.T) {
	var cases []parityCase
	for _, c := range customOpOptionCases() {
		cases = append(cases, customRunCase(c.name, []string{customOpAnswer}, c.old, nil, c.v2, nil))
	}
	cases = append(cases,
		customRunCase("a repair", []string{customOpBad, customOpAnswer}, types.OpOptions{}, nil, op.Options{}, nil),
		customRunCase("two repairs under the descriptor's own policy", []string{customOpBad, customOpBad, customOpAnswer}, types.OpOptions{},
			func(d *oldroot.Op[string, customInvoice]) { d.DefaultPolicy.RepairAttempts = 2 }, op.Options{},
			func(d *op.Descriptor[string, customInvoice]) { d.Policy.RepairAttempts = 2 }),
		customRunCase("invariants and Normalize", []string{customOpAnswer}, types.OpOptions{},
			func(d *oldroot.Op[string, customInvoice]) {
				d.Contract.Invariants = []func(customInvoice) error{customOpNotNegative}
				d.Contract.Normalize = customOpUpper
			}, op.Options{},
			func(d *op.Descriptor[string, customInvoice]) {
				d.Contract.Invariants = []func(customInvoice) error{customOpNotNegative}
				d.Contract.Normalize = customOpUpper
			}),
		customRunCase("a refused invariant is repaired", []string{`{"number":"A","total":-1}`, customOpAnswer}, types.OpOptions{},
			func(d *oldroot.Op[string, customInvoice]) {
				d.Contract.Invariants = []func(customInvoice) error{customOpNotNegative}
			}, op.Options{},
			func(d *op.Descriptor[string, customInvoice]) {
				d.Contract.Invariants = []func(customInvoice) error{customOpNotNegative}
			}),
		customRunCase("no name", []string{customOpAnswer}, types.OpOptions{},
			func(d *oldroot.Op[string, customInvoice]) { d.ID.Name = "" }, op.Options{},
			func(d *op.Descriptor[string, customInvoice]) { d.ID.Name = "" }),
	)
	// A descriptor with no name is recorded as "op.run".
	cases[len(cases)-1].op = "op.run"
	runParity(t, cases)
}

// ---- RunBatch ----

func customBatchCase(name string, old types.OpOptions, v2 op.Options) parityCase {
	return parityCase{
		name: "RunBatch/" + name, op: customOpName, replies: []string{`[{"number":"A","total":1},{"number":"B","total":2}]`},
		// A merge written by hand is vouched for no further than that the model answered.
		old: func(ctx context.Context) error {
			_, err := oldroot.RunOpBatch(ctx, customOpOld(true, nil), []string{"first", "second"}, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := op.RunBatch(ctx, customOpNew(true, nil), []string{"first", "second"}, v2)
			return err
		},
	}
}

func TestRunBatchSendsTheRequestTheOldRootsRunOpBatchSends(t *testing.T) {
	var cases []parityCase
	for _, c := range customOpOptionCases() {
		cases = append(cases, customBatchCase(c.name, c.old, c.v2))
	}
	runParity(t, cases)
}

// ---- RunMany ----

// customManyItems are identical, so that the requests of a run whose items are
// sent at the same time are the same whichever goes first.
var customManyItems = []string{"same", "same", "same", "same"}

func customManyCase(name string, batched bool, replies []string, old oldroot.PlanRequest, v2 op.PlanOptions) parityCase {
	return customManyCaseOf(name, customManyItems, batched, replies, old, v2)
}

func customManyCaseOf(name string, items []string, batched bool, replies []string, old oldroot.PlanRequest, v2 op.PlanOptions) parityCase {
	c := parityCase{
		name: "RunMany/" + name, op: customOpName, replies: replies,
		old: func(ctx context.Context) error {
			_, _, err := oldroot.RunOpMany(ctx, customOpOld(batched, nil), items, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := op.RunMany(ctx, customOpNew(batched, nil), items, v2)
			return err
		},
	}
	if !batched {
		c.floor = schemaflux.ContractJSONWellFormed
	}
	return c
}

func TestRunManySendsTheRequestsTheOldRootsRunOpManySends(t *testing.T) {
	var cases []parityCase
	for _, c := range customOpOptionCases() {
		cases = append(cases,
			customManyCase("atomic/"+c.name, false, []string{customOpAnswer}, oldroot.PlanRequest{Options: c.old}, op.PlanOptions{Options: c.v2}),
			customManyCase("batched/"+c.name, true, []string{`[{"number":"A","total":1},{"number":"B","total":2}]`},
				oldroot.PlanRequest{Options: c.old, MaxItemsPerCall: 2}, op.PlanOptions{Options: c.v2, MaxItemsPerCall: 2}))
	}
	cases = append(cases,
		customManyCase("a batched descriptor forced atomic", true, []string{customOpAnswer},
			oldroot.PlanRequest{Force: oldroot.ShapeAtomic, ForceReason: "risk"}, op.PlanOptions{Force: op.ShapeAtomic, ForceReason: "risk"}),
		customManyCase("a batched descriptor forced batched", true, []string{`[{"number":"A","total":1},{"number":"B","total":2}]`},
			oldroot.PlanRequest{Force: oldroot.ShapeMDSP, MaxItemsPerCall: 2}, op.PlanOptions{Force: op.ShapeBatched, MaxItemsPerCall: 2}),
		customManyCaseOf("a repair of one item", []string{"alone"}, false, []string{customOpBad, customOpAnswer}, oldroot.PlanRequest{}, op.PlanOptions{}),
	)
	runParity(t, cases)
}

// ---- Preflight ----

// planFor is the plan the old planner makes for a client whose provider is the
// recording model of the old contract, which is the plan version 2 makes for the
// client on its context.
func planFor(t *testing.T, items []string, req types.PlanRequest, batched bool) types.Plan {
	t.Helper()
	model := oldModel{&script{}}
	ctx := ops.WithSharedExecConfig(context.Background(), &ops.ExecConfig{Provider: model, Retries: -1})
	plan, err := ops.PreflightFor(ctx, customOpOld(batched, nil), items, req)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPreflightPlansWhatTheOldPlannerPlansForTheClient(t *testing.T) {
	many := make([]string, 40)
	for i := range many {
		many[i] = fmt.Sprintf("invoice number %d with a few words of text", i)
	}
	huge := append(append([]string{}, many[:3]...), strings.Repeat("a long item ", 20000))
	steering := "answer plainly"
	for _, c := range []struct {
		name    string
		items   []string
		batched bool
		old     types.PlanRequest
		v2      op.PlanOptions
	}{
		{"atomic", many[:5], false, types.PlanRequest{}, op.PlanOptions{}},
		{"no items", nil, false, types.PlanRequest{}, op.PlanOptions{}},
		{"one item of a batched descriptor", many[:1], true, types.PlanRequest{}, op.PlanOptions{}},
		{"batched", many, true, types.PlanRequest{}, op.PlanOptions{}},
		{"batched with a bound", many, true, types.PlanRequest{MaxItemsPerCall: 7}, op.PlanOptions{MaxItemsPerCall: 7}},
		{"forced atomic", many, true, types.PlanRequest{Force: types.ShapeAtomic, ForceReason: "risk"}, op.PlanOptions{Force: op.ShapeAtomic, ForceReason: "risk"}},
		{"forced batched, no batch", many[:4], false, types.PlanRequest{Force: types.ShapeMDSP}, op.PlanOptions{Force: op.ShapeBatched}},
		{"forced split", many[:4], true, types.PlanRequest{Force: types.ShapeSDMP}, op.PlanOptions{Force: op.ShapeSplit}},
		{"over a budget", many[:4], false, types.PlanRequest{Budget: 0.01}, op.PlanOptions{Budget: 0.01}},
		{"an oversized item", huge, true, types.PlanRequest{}, op.PlanOptions{}},
		{"steering and a ceiling", many[:6], true,
			types.PlanRequest{Options: types.OpOptions{Steering: steering, MaxOutputTokens: 321, Intelligence: types.Smart, Mode: types.Strict, ResponseFormat: "json"}},
			op.PlanOptions{Options: op.Options{Options: schemaflux.Options{Steering: steering, MaxOutputTokens: 321, Tier: schemaflux.TierSmart, Mode: schemaflux.ModeStrict}, ResponseFormat: "json"}}},
		{"a pinned model", many[:6], true, types.PlanRequest{Options: types.OpOptions{Model: pinnedModel}}, op.PlanOptions{Options: op.Options{Options: schemaflux.Options{Model: pinnedModel}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := planFor(t, c.items, c.old, c.batched)
			model := newModel{&script{}}
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
			got, err := op.Preflight(ctx, customOpNew(c.batched, nil), c.items, c.v2)
			if err != nil {
				t.Fatal(err)
			}
			if sent := model.requests(); len(sent) != 0 {
				t.Fatalf("Preflight sent %d requests", len(sent))
			}
			requireSamePlan(t, want, got)
		})
	}
}

// requireSamePlan fails the test, naming the first field that differs between the
// old planner's plan and version 2's.
func requireSamePlan(t *testing.T, want types.Plan, got op.Plan) {
	t.Helper()
	shape := map[types.ExecutionShape]string{
		types.ShapeUnspecified: "unspecified", types.ShapeAtomic: "atomic", types.ShapeMDSP: "batched", types.ShapeSDMP: "split", types.ShapeGlobal: "global",
	}
	check := func(field string, w, g any) {
		t.Helper()
		if fmt.Sprint(w) != fmt.Sprint(g) {
			t.Errorf("%s: the old planner says %v, version 2 %v", field, w, g)
		}
	}
	check("Operation", want.Operation.String(), got.Operation.String())
	check("InputShape", want.InputShape, got.InputShape)
	check("ItemCount", want.ItemCount, got.ItemCount)
	check("Eligible", want.Eligible, got.Eligible)
	check("IneligibleReason", want.IneligibleReason, got.IneligibleReason)
	check("ShapeForced", want.ShapeForced, got.ShapeForced)
	check("Shape", shape[want.Shape], got.Shape)
	check("ShapeReason", want.ShapeReason, got.ShapeReason)
	check("MaxCalls", want.MaxCalls, got.MaxCalls)
	check("Chunks", fmt.Sprint(want.Chunks), fmt.Sprint(got.Chunks))
	check("OversizedItems", fmt.Sprint(want.OversizedItems), fmt.Sprint(got.OversizedItems))
	check("EstimatedCost.Priced", want.EstimatedCost.Priced, got.EstimatedCost.Priced)
	check("EstimatedCost.EstimatedCost", want.EstimatedCost.EstimatedCost, got.EstimatedCost.EstimatedCost)
	check("EstimatedCost.EstimatedPromptTokens", want.EstimatedCost.EstimatedPromptTokens, got.EstimatedCost.EstimatedPromptTokens)
	check("EstimatedCost.EstimatedCompletionTokens", want.EstimatedCost.EstimatedCompletionTokens, got.EstimatedCost.EstimatedCompletionTokens)
	check("Capability.Provider", want.Capability.Provider, got.Capability.Provider)
	check("Capability.Model", want.Capability.Model, got.Capability.Model)
	check("Capability.MaxOutputTokens", want.Capability.MaxOutputTokens, got.Capability.MaxOutputTokens)
	check("Capability.ContextTokens", want.Capability.ContextTokens, got.Capability.ContextTokens)
	check("Policy.MaxOutputTokens", want.Policy.MaxOutputTokens, got.Policy.MaxOutputTokens)
	check("Policy.HasSteering", want.Policy.HasSteering, got.Policy.HasSteering)
	check("Policy.ResponseFormat", want.Policy.ResponseFormat, got.Policy.ResponseFormat)
	check("EstimatorVersion", want.EstimatorVersion, got.EstimatorVersion)
	check("Alternatives", len(want.Alternatives), len(got.Alternatives))
	for i := range min(len(want.Alternatives), len(got.Alternatives)) {
		check(fmt.Sprintf("Alternatives[%d].Shape", i), shape[want.Alternatives[i].Shape], got.Alternatives[i].Shape)
	}
	if !want.Eligible {
		wantKind := map[types.ErrorKind]schemaflux.Kind{
			types.KindConfiguration: schemaflux.ErrConfiguration, types.KindBudgetExceeded: schemaflux.ErrBudgetExceeded,
			types.KindInvalidRequest: schemaflux.ErrInvalidRequest,
		}[want.IneligibleKind]
		check("IneligibleKind", wantKind, got.IneligibleKind)
	}
}

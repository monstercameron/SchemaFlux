package op_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
)

func TestNewReturnsAValidDescriptorAsItIs(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	got, err := op.New(d)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != d.ID || got.Contract.SchemaName != "invoice" || got.BuildPrompt == nil || got.Contract.Decode == nil {
		t.Errorf("the descriptor changed: %+v", got)
	}
}

func TestNewRefusesADescriptorThatCannotRunOrBreaksItsOwnDeclaration(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate  func(*op.Descriptor[string, invoice])
		message string
	}{
		"no BuildPrompt": {func(d *op.Descriptor[string, invoice]) { d.BuildPrompt = nil }, "BuildPrompt"},
		"no Decode":      {func(d *op.Descriptor[string, invoice]) { d.Contract.Decode = nil }, "Decode"},
		"a nil invariant": {func(d *op.Descriptor[string, invoice]) {
			d.Contract.Invariants = []func(invoice) error{okInvariant, nil}
		}, "Invariants entry 1"},
		"stable, no class": {func(d *op.Descriptor[string, invoice]) { d.Semantics.Stability = op.StabilityStable }, "no Batch.Class"},
		"stable, itemwise, no kind": {func(d *op.Descriptor[string, invoice]) {
			d.Semantics.Stability = op.StabilityStable
			d.Batch.Class = op.BatchItemwise
		}, "no Batch.Kind"},
		"stable, aggregate, no kind": {func(d *op.Descriptor[string, invoice]) {
			d.Semantics.Stability = op.StabilityStable
			d.Batch.Class = op.BatchAggregate
		}, "no Batch.Kind"},
		"item beside Encode":       {func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor(); d.Batch.Encode = nilEncode }, "Batch.Item is set beside"},
		"item beside Merge":        {func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor(); d.Batch.Merge = nilMerge }, "Batch.Item is set beside"},
		"item, no instructions":    {func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor(); d.Batch.Instructions = "" }, "Instructions is empty"},
		"instructions, no item":    {func(d *op.Descriptor[string, invoice]) { d.Batch.Instructions = "x" }, "without Batch.Item"},
		"a class that is no class": {func(d *op.Descriptor[string, invoice]) { d.Batch.Class = 200 }, "Batch.Class"},
		"an algebra that is none":  {func(d *op.Descriptor[string, invoice]) { d.Batch.Kind = 200 }, "Batch.Kind"},
		"a category that is none":  {func(d *op.Descriptor[string, invoice]) { d.Semantics.Category = 200 }, "Semantics.Category"},
		"a stability that is none": {func(d *op.Descriptor[string, invoice]) { d.Semantics.Stability = 200 }, "Semantics.Stability"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := invoiceDescriptor()
			c.mutate(&d)
			got, err := op.New(d)
			e := wantError(t, err, schemaflux.ErrConfiguration, "op.new")
			if !strings.Contains(e.Message, c.message) {
				t.Errorf("message = %q, want it to say %q", e.Message, c.message)
			}
			if got.BuildPrompt != nil || got.ID != (op.ID{}) {
				t.Errorf("a descriptor beside an error: %+v", got)
			}
		})
	}
}

func TestNewAcceptsAStableDescriptorThatSaysHowItBatches(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*op.Descriptor[string, invoice]){
		"none":      func(d *op.Descriptor[string, invoice]) { d.Batch.Class = op.BatchNone },
		"itemwise":  func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor() },
		"aggregate": func(d *op.Descriptor[string, invoice]) { *d = batchDescriptor(); d.Batch.Class = op.BatchAggregate },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := invoiceDescriptor()
			mutate(&d)
			d.Semantics.Stability = op.StabilityStable
			if _, err := op.New(d); err != nil {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestNewAcceptsAnExperimentalDescriptorThatSaysNothingOfItsBatch(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Semantics.Stability = op.StabilityExperimental
	if _, err := op.New(d); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestNewOfTheZeroDescriptorIsAConfigurationError(t *testing.T) {
	t.Parallel()
	_, err := op.New(op.Descriptor[int, int]{})
	wantError(t, err, schemaflux.ErrConfiguration, "op.new")
}

func TestMustReturnsAValidDescriptorAndPanicsWithTheErrorOfNewOtherwise(t *testing.T) {
	t.Parallel()
	if got := op.Must(invoiceDescriptor()); got.ID.Name != "read-invoice" {
		t.Errorf("got %+v", got)
	}
	d := invoiceDescriptor()
	d.BuildPrompt = nil
	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok {
			t.Fatalf("recovered %v, want the error", r)
		}
		wantError(t, err, schemaflux.ErrConfiguration, "op.new")
	}()
	op.Must(d)
}

func TestNewCallsNoFunctionOfTheDescriptor(t *testing.T) {
	t.Parallel()
	var called atomic.Int32
	d := op.Descriptor[string, invoice]{
		BuildPrompt: func(string, op.Options) (string, string) { called.Add(1); return "", "" },
		Contract: op.Contract[invoice]{
			Decode:     func(string) (invoice, error) { called.Add(1); return invoice{}, nil },
			Invariants: []func(invoice) error{func(invoice) error { called.Add(1); return nil }},
			Normalize:  func(v invoice) invoice { called.Add(1); return v },
		},
	}
	if _, err := op.New(d); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 0 {
		t.Errorf("%d functions of the descriptor were called", called.Load())
	}
}

func TestIDStringIsNameAtVersion(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		id   op.ID
		want string
	}{
		{op.ID{Name: "a", Version: "2"}, "a@2"},
		{op.ID{Name: "a"}, "a@unversioned"},
		{op.ID{Version: "2"}, "unnamed@2"},
		{op.ID{}, "unnamed@"},
	} {
		if got := c.id.String(); got != c.want {
			t.Errorf("%+v.String() = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestEveryEnumHasANameAndTheZeroValueIsUnspecified(t *testing.T) {
	t.Parallel()
	check := func(name string, zero, last, past string) {
		t.Helper()
		if zero != "unspecified" {
			t.Errorf("%s: the zero value is %q", name, zero)
		}
		if last == "unspecified" {
			t.Errorf("%s: the last value has no name", name)
		}
		if past != "unspecified" {
			t.Errorf("%s: a number past the last is %q", name, past)
		}
	}
	check("Shape", op.Shape(0).String(), op.ShapeGlobal.String(), op.Shape(9).String())
	check("Algebra", op.Algebra(0).String(), op.AlgebraSequential.String(), op.Algebra(9).String())
	check("BatchClass", op.BatchClass(0).String(), op.BatchAggregate.String(), op.BatchClass(9).String())
	check("Category", op.Category(0).String(), op.CategoryValidate.String(), op.Category(99).String())
	check("Stability", op.Stability(0).String(), op.StabilityStable.String(), op.Stability(9).String())
	want := map[string]string{
		op.ShapeAtomic.String(): "atomic", op.ShapeBatched.String(): "batched", op.ShapeSplit.String(): "split", op.ShapeGlobal.String(): "global",
		op.AlgebraIndependent.String(): "independent", op.AlgebraSubset.String(): "subset", op.AlgebraPermutation.String(): "permutation",
		op.AlgebraPartition.String(): "partition", op.AlgebraGraph.String(): "graph", op.AlgebraHierarchical.String(): "hierarchical",
		op.AlgebraSequential.String(): "sequential", op.BatchNone.String(): "none", op.BatchItemwise.String(): "itemwise",
		op.BatchAggregate.String(): "aggregate", op.CategoryExtract.String(): "extract", op.CategoryTransform.String(): "transform",
		op.CategoryGenerate.String(): "generate", op.CategoryClassify.String(): "classify", op.CategorySelect.String(): "select",
		op.CategoryOrder.String(): "order", op.CategoryReview.String(): "review", op.CategoryValidate.String(): "validate",
		op.StabilityExperimental.String(): "experimental", op.StabilityStable.String(): "stable",
	}
	for got, wanted := range want {
		if got != wanted {
			t.Errorf("name %q, want %q", got, wanted)
		}
	}
}

func TestShapeSerializesAsItsName(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(struct{ S op.Shape }{op.ShapeBatched})
	if err != nil || string(out) != `{"S":"batched"}` {
		t.Errorf("got %s, err %v", out, err)
	}
}

func TestCoverageSaysWhetherAnAnswerCoveredWhatWasOffered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		c        op.Coverage
		ok, full bool
		text     string
	}{
		{op.Coverage{Offered: 3, Answered: 3}, true, true, ""},
		{op.Coverage{Offered: 3, Answered: 2, Missing: 1}, false, false, "3 offered, 2 answered, 1 missing, 0 duplicated, 0 unknown"},
		{op.Coverage{Offered: 3, Answered: 4, Duplicate: 1}, false, false, "3 offered, 4 answered, 0 missing, 1 duplicated, 0 unknown"},
		{op.Coverage{Offered: 3, Answered: 4, Unknown: 1}, false, true, "3 offered, 4 answered, 0 missing, 0 duplicated, 1 unknown"},
		{op.Coverage{}, true, true, ""},
	}
	for _, c := range cases {
		if c.c.OK() != c.ok || c.c.Complete() != c.full || c.c.String() != c.text {
			t.Errorf("%+v: OK %v Complete %v String %q, want %v %v %q", c.c, c.c.OK(), c.c.Complete(), c.c.String(), c.ok, c.full, c.text)
		}
	}
}

func TestOptionsMergeLaysEveryFieldOfTheLaterValueOverTheEarlier(t *testing.T) {
	t.Parallel()
	// Every field of Options, set one at a time in a later value, has to reach the
	// call: a request made with the two values must show it.
	schema := map[string]any{"type": "object"}
	cases := map[string]op.Options{
		"Model":           {Options: schemaflux.Options{Model: "m"}},
		"Tier":            {Options: schemaflux.Options{Tier: schemaflux.TierSmart}},
		"Effort":          {Options: schemaflux.Options{Effort: schemaflux.EffortLow}},
		"Mode":            {Options: schemaflux.Options{Mode: schemaflux.ModeStrict}},
		"Steering":        {Options: schemaflux.Options{Steering: "s"}},
		"MaxOutputTokens": {Options: schemaflux.Options{MaxOutputTokens: 9}},
		"WebSearch":       {Options: schemaflux.Options{WebSearch: true}},
		"JSONSchema":      {JSONSchema: schema},
		"SchemaName":      {SchemaName: "n"},
		"ResponseFormat":  {ResponseFormat: "json"},
	}
	typ := reflect.TypeOf(op.Options{})
	covered := 0
	for name, later := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var seen op.Options
			d := invoiceDescriptor()
			d.BuildPrompt = func(text string, o op.Options) (string, string) { seen = o; return "s", text }
			earlier := op.Options{Options: schemaflux.Options{Model: "earlier", MaxOutputTokens: 1}, SchemaName: "earlier", ResponseFormat: "text"}
			if _, err := op.Run(clientOn(answeringWith(goodInvoice)), d, "x", earlier, later); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fieldOf(seen, name), fieldOf(later, name)) {
				t.Errorf("%s = %v, want the later value's %v", name, fieldOf(seen, name), fieldOf(later, name))
			}
		})
		covered++
	}
	// The cases name every field of Options and of the embedded settings.
	want := typ.NumField() - 1
	for i := 0; i < reflect.TypeOf(schemaflux.Options{}).NumField(); i++ {
		want++
	}
	if covered != want {
		t.Errorf("the cases cover %d settings, Options has %d: a field was added", covered, want)
	}
}

// fieldOf is the value of the setting called name in o.
func fieldOf(o op.Options, name string) any {
	if f := reflect.ValueOf(o).FieldByName(name); f.IsValid() {
		return f.Interface()
	}
	return reflect.ValueOf(o.Options).FieldByName(name).Interface()
}

func TestPlanOptionsMergeLaysEveryFieldOfTheLaterValueOverTheEarlier(t *testing.T) {
	t.Parallel()
	ctx := clientOn(answeringWith())
	// Two values: the later one sets what the earlier did not, and an earlier
	// setting the later leaves at its zero survives.
	first := op.PlanOptions{Force: op.ShapeAtomic, ForceReason: "first", Budget: 1000, MaxItemsPerCall: 2,
		Options: op.Options{Options: schemaflux.Options{Steering: "kept"}}}
	second := op.PlanOptions{ForceReason: "second"}
	plan, err := op.Preflight(ctx, batchDescriptor(), items(6), first, second)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Shape != op.ShapeAtomic || plan.ShapeReason != "second" || !plan.Policy.HasSteering {
		t.Errorf("plan = shape %v reason %q steering %v", plan.Shape, plan.ShapeReason, plan.Policy.HasSteering)
	}
	third := op.PlanOptions{Force: op.ShapeBatched, MaxItemsPerCall: 3}
	plan, err = op.Preflight(ctx, batchDescriptor(), items(6), first, third)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Shape != op.ShapeBatched || len(plan.Chunks) != 2 || plan.ShapeReason != "first" {
		t.Errorf("plan = shape %v chunks %v reason %q", plan.Shape, plan.Chunks, plan.ShapeReason)
	}
	overBudget := op.PlanOptions{Budget: 0.0000001}
	plan, _ = op.Preflight(ctx, invoiceDescriptor(), items(2), op.PlanOptions{}, overBudget)
	if plan.Eligible {
		t.Error("a later Budget did not reach the plan")
	}
}

func TestADescriptorIsAValueThatCopiesShareNoState(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	copyOf := d
	copyOf.ID.Name = "other"
	var rec schemaflux.Recorder
	if _, err := op.Run(clientOn(echoing(), recordingTo(&rec)), d, "x"); err != nil {
		t.Fatal(err)
	}
	if onlyRecord(t, &rec).Op != "read-invoice" {
		t.Error("a copy changed the original")
	}
}

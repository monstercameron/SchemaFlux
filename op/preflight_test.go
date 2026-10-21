package op_test

import (
	"bytes"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
)

func TestPreflightMakesNoRequestAndPlansOneForEachItemOfAnAtomicRun(t *testing.T) {
	t.Parallel()
	model := echoing()
	plan, err := op.Preflight(clientOn(model), invoiceDescriptor(), items(6))
	if err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
	if !plan.Eligible || plan.Shape != op.ShapeAtomic || plan.ItemCount != 6 || plan.MaxCalls != 6 {
		t.Errorf("plan = %+v", plan)
	}
	if plan.Operation != (op.ID{Name: "read-invoice", Version: "1"}) {
		t.Errorf("operation = %+v", plan.Operation)
	}
	if plan.EstimatedCost.EstimatedPromptTokens <= 0 {
		t.Errorf("estimate = %+v, want the prompts priced", plan.EstimatedCost)
	}
	if plan.EstimatorVersion == "" || plan.InputShape == "" || plan.PolicyDigest == "" || plan.CapabilityDigest == "" {
		t.Errorf("plan = %+v, want the version, the shape and the digests", plan)
	}
}

func TestPreflightPlansForTheClientOnTheContext(t *testing.T) {
	t.Parallel()
	one := answeringWith()
	one.SetName("first-provider")
	two := answeringWith()
	two.SetName("second-provider")
	a, err := op.Preflight(clientOn(one), invoiceDescriptor(), items(2))
	if err != nil {
		t.Fatal(err)
	}
	b, err := op.Preflight(clientOn(two, func(c *schemaflux.Client) { c.Model = "client-model" }), invoiceDescriptor(), items(2))
	if err != nil {
		t.Fatal(err)
	}
	if a.Capability.Provider != "first-provider" || b.Capability.Provider != "second-provider" {
		t.Errorf("providers = %q and %q", a.Capability.Provider, b.Capability.Provider)
	}
	if b.Capability.Model != "client-model" || a.Capability.Model != "test-model" {
		t.Errorf("models = %q and %q, want the client's own and the tier's", a.Capability.Model, b.Capability.Model)
	}
	if a.CapabilityDigest == b.CapabilityDigest {
		t.Error("two clients have one capability digest")
	}
	if one.Calls()+two.Calls() != 0 {
		t.Error("a request was sent")
	}
}

func TestPreflightIsDeterministic(t *testing.T) {
	t.Parallel()
	ctx := clientOn(answeringWith())
	a, err := op.Preflight(ctx, batchDescriptor(), items(30), op.PlanOptions{MaxItemsPerCall: 7})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := op.Preflight(ctx, batchDescriptor(), items(30), op.PlanOptions{MaxItemsPerCall: 7})
	sa, err := a.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := b.Serialize()
	if !bytes.Equal(sa, sb) || a.Digest() != b.Digest() {
		t.Errorf("two plans of one run differ:\n%s\n%s", sa, sb)
	}
	c, _ := op.Preflight(ctx, batchDescriptor(), items(31), op.PlanOptions{MaxItemsPerCall: 7})
	if c.Digest() == a.Digest() {
		t.Error("a plan of another number of items has the same digest")
	}
}

func TestPreflightCutsABatchedRunIntoChunksThatFitTheBound(t *testing.T) {
	t.Parallel()
	plan, err := op.Preflight(clientOn(answeringWith()), batchDescriptor(), items(10), op.PlanOptions{MaxItemsPerCall: 4})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Shape != op.ShapeBatched || len(plan.Chunks) != 3 || plan.MaxCalls != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	total := 0
	for _, chunk := range plan.Chunks {
		total += chunk.ItemCount
		if chunk.ItemCount > 4 {
			t.Errorf("a chunk of %d items passes the bound", chunk.ItemCount)
		}
	}
	if total != 10 {
		t.Errorf("the chunks hold %d items, want 10", total)
	}
	if plan.ShapeReason == "" || len(plan.Alternatives) == 0 || plan.Alternatives[0].Shape != op.ShapeAtomic {
		t.Errorf("plan = reason %q alternatives %+v, want the atomic shape named as the one not chosen", plan.ShapeReason, plan.Alternatives)
	}
}

func TestPreflightReportsAShapeItCannotRunAsAPlanThatSaysSo(t *testing.T) {
	t.Parallel()
	plan, err := op.Preflight(clientOn(answeringWith()), invoiceDescriptor(), items(3), op.PlanOptions{Force: op.ShapeBatched})
	if err != nil {
		t.Fatalf("a plan that cannot be carried out is not an error: %v", err)
	}
	if plan.Eligible || plan.IneligibleKind != schemaflux.ErrConfiguration || plan.IneligibleReason == "" {
		t.Errorf("plan = eligible %v kind %v reason %q", plan.Eligible, plan.IneligibleKind, plan.IneligibleReason)
	}
	if !strings.Contains(plan.Explain(), "not eligible") {
		t.Errorf("Explain = %q", plan.Explain())
	}
}

func TestPreflightReportsAPlanOverItsBudgetAsABudgetRefusal(t *testing.T) {
	t.Parallel()
	plan, err := op.Preflight(clientOn(answeringWith()), invoiceDescriptor(), items(3), op.PlanOptions{Budget: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible || plan.IneligibleKind != schemaflux.ErrBudgetExceeded {
		t.Errorf("plan = eligible %v kind %v reason %q, want a budget refusal: the model has no price to compare", plan.Eligible, plan.IneligibleKind, plan.IneligibleReason)
	}
}

func TestPreflightOfAForcedShapeSaysItWasForcedAndWhy(t *testing.T) {
	t.Parallel()
	plan, err := op.Preflight(clientOn(answeringWith()), batchDescriptor(), items(5), op.PlanOptions{Force: op.ShapeAtomic, ForceReason: "risk of a swapped answer"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ShapeForced || plan.Shape != op.ShapeAtomic || plan.ShapeReason != "risk of a swapped answer" {
		t.Errorf("plan = forced %v shape %v reason %q", plan.ShapeForced, plan.Shape, plan.ShapeReason)
	}
}

func TestPreflightRefusesANilContextAndADescriptorThatCannotRun(t *testing.T) {
	t.Parallel()
	//lint:ignore SA1012 the test is of a nil context
	_, err := op.Preflight(nil, invoiceDescriptor(), items(1)) //nolint:staticcheck
	wantError(t, err, schemaflux.ErrInvalidRequest, "op.preflight")
	d := invoiceDescriptor()
	d.BuildPrompt = nil
	plan, err := op.Preflight(clientOn(answeringWith()), d, items(1))
	wantError(t, err, schemaflux.ErrConfiguration, "op.preflight")
	if plan.Eligible || plan.ItemCount != 0 {
		t.Errorf("a plan beside an error: %+v", plan)
	}
}

func TestPreflightRefusesOptionsNoCallCanUse(t *testing.T) {
	t.Parallel()
	cases := map[string]op.PlanOptions{
		"a negative MaxItemsPerCall": {MaxItemsPerCall: -2},
		"a NaN Budget":               {Budget: nan()},
		"a negative Budget":          {Budget: -0.5},
		"a Force that is no Shape":   {Force: 77},
		"a negative MaxOutputTokens": {Options: op.Options{Options: schemaflux.Options{MaxOutputTokens: -1}}},
		"a Tier that is not one":     {Options: op.Options{Options: schemaflux.Options{Tier: 8}}},
		"an unknown ResponseFormat":  {Options: op.Options{ResponseFormat: "csv"}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := op.Preflight(clientOn(answeringWith()), invoiceDescriptor(), items(1), o)
			wantError(t, err, schemaflux.ErrConfiguration, "op.preflight")
		})
	}
}

func TestPreflightAFunctionOfTheDescriptorThatPanicsMakesThePlanIneligibleAsInternal(t *testing.T) {
	t.Parallel()
	cases := map[string]op.Descriptor[string, invoice]{
		"BuildPrompt": func() op.Descriptor[string, invoice] {
			d := invoiceDescriptor()
			d.BuildPrompt = func(string, op.Options) (string, string) { panic(canary) }
			return d
		}(),
	}
	cases["Encode"] = func() op.Descriptor[string, invoice] {
		d := invoiceDescriptor()
		d.Batch = op.Batch[string, invoice]{Class: op.BatchItemwise, Kind: op.AlgebraIndependent,
			Encode: func([]string, op.Options) (string, string, error) { panic(canary) },
			Merge:  func(string, []string) ([]invoice, error) { return nil, nil }}
		return d
	}()
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith()
			plan, err := op.Preflight(clientOn(model), d, items(4))
			if err != nil {
				t.Fatal(err)
			}
			if plan.Eligible || plan.IneligibleKind != schemaflux.ErrInternal {
				t.Errorf("plan = eligible %v kind %v reason %q", plan.Eligible, plan.IneligibleKind, plan.IneligibleReason)
			}
			if blob, _ := plan.Serialize(); strings.Contains(string(blob)+plan.Explain(), canary) {
				t.Error("the plan carries what the descriptor panicked with")
			}
			if model.Calls() != 0 {
				t.Error("a request was sent")
			}
		})
	}
}

func TestPreflightAddsNoRecord(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	if _, err := op.Preflight(clientOn(answeringWith(), recordingTo(&rec)), invoiceDescriptor(), items(2)); err != nil {
		t.Fatal(err)
	}
	if got := rec.Records(); len(got) != 0 {
		t.Errorf("the recorder holds %d Records, want none", len(got))
	}
}

func TestPreflightHoldsNoItemAndNoSteeringInThePlan(t *testing.T) {
	t.Parallel()
	plan, err := op.Preflight(clientOn(answeringWith()), invoiceDescriptor(), []string{canary, canary},
		op.PlanOptions{Options: op.Options{Options: schemaflux.Options{Steering: canary, Tier: schemaflux.TierSmart, Mode: schemaflux.ModeStrict, MaxOutputTokens: 99}, ResponseFormat: "json"}})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := plan.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob)+plan.Explain(), canary) {
		t.Errorf("the plan holds the caller's text: %s", blob)
	}
	want := op.PolicySnapshot{Mode: schemaflux.ModeStrict, Tier: schemaflux.TierSmart, MaxOutputTokens: 99, HasSteering: true, ResponseFormat: "json"}
	if plan.Policy != want {
		t.Errorf("policy = %+v, want %+v", plan.Policy, want)
	}
}

func TestPreflightPricesTheSteeringTheCallSendsWithEveryRequest(t *testing.T) {
	t.Parallel()
	ctx := clientOn(answeringWith())
	plain, _ := op.Preflight(ctx, invoiceDescriptor(), items(5))
	steered, _ := op.Preflight(ctx, invoiceDescriptor(), items(5), op.PlanOptions{Options: op.Options{Options: schemaflux.Options{Steering: strings.Repeat("be careful ", 40)}}})
	if steered.EstimatedCost.EstimatedPromptTokens <= plain.EstimatedCost.EstimatedPromptTokens {
		t.Errorf("steered estimate %d is not above the plain %d", steered.EstimatedCost.EstimatedPromptTokens, plain.EstimatedCost.EstimatedPromptTokens)
	}
}

func TestPreflightOfNoItemsIsAnEligiblePlanOfNoCalls(t *testing.T) {
	t.Parallel()
	plan, err := op.Preflight(clientOn(answeringWith()), invoiceDescriptor(), nil)
	if err != nil || !plan.Eligible || plan.MaxCalls != 0 || plan.ItemCount != 0 {
		t.Errorf("plan = %+v, err %v", plan, err)
	}
}

func TestPlanExplainsItselfInWordsWithoutTheItems(t *testing.T) {
	t.Parallel()
	plan, err := op.Preflight(clientOn(answeringWith()), batchDescriptor(), items(9), op.PlanOptions{MaxItemsPerCall: 3})
	if err != nil {
		t.Fatal(err)
	}
	text := plan.Explain()
	for _, want := range []string{"read-invoice@1", "9 item(s)", "shape batched", "3 chunk(s)", "at most 3 call(s)", "cost unpriced", "not chosen: atomic"} {
		if !strings.Contains(text, want) {
			t.Errorf("Explain does not say %q:\n%s", want, text)
		}
	}
}

package op_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunEachReturnsOneInputOrderedItemPerInput(t *testing.T) {
	t.Parallel()
	for count := 1; count <= 10; count++ {
		t.Run(strings.Repeat("x", count), func(t *testing.T) {
			inputs := items(count)
			got, err := op.RunEach(clientOn(echoing()), invoiceDescriptor(), inputs, op.PlanOptions{Force: op.ShapeAtomic})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != count {
				t.Fatalf("got %d items, want %d", len(got), count)
			}
			for i, item := range got {
				value, err := item.Value()
				if err != nil {
					t.Fatalf("item %d: %v", i, err)
				}
				if value.Number != inputs[i] {
					t.Errorf("item %d = %q, want %q", i, value.Number, inputs[i])
				}
			}
		})
	}
}

func TestRunEachNilContextIsInvalidRequest(t *testing.T) {
	t.Parallel()
	_, err := op.RunEach[string, invoice](nil, invoiceDescriptor(), []string{"a"})
	wantError(t, err, schemaflux.ErrInvalidRequest, "op.runeach")
}

func TestRunEachFailedItemHasNoValue(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.Policy.RepairAttempts = -1
	got, err := op.RunEach(clientOn(answeringWith(`{"total":1}`)), d, []string{"bad"}, op.PlanOptions{Force: op.ShapeAtomic})
	if err != nil {
		t.Fatal(err)
	}
	value, itemErr := got[0].Value()
	if itemErr == nil || value != (invoice{}) {
		t.Fatalf("Value() = %+v, %v; want zero and error", value, itemErr)
	}
}

func TestRunEachKeepsGoodItemsBesideOneFailedItem(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		body := `{"number":"ok","total":1}`
		if strings.Contains(req.User, "c") {
			body = `{"total":1}`
		}
		return schemaflux.Response{Content: body, Model: req.Model, FinishReason: "stop"}, nil
	})
	d := invoiceDescriptor()
	d.Policy.RepairAttempts = -1
	got, err := op.RunEach(clientOn(&model), d, []string{"a", "b", "c", "d", "e"}, op.PlanOptions{Force: op.ShapeAtomic})
	if err != nil || len(got) != 5 || model.Calls() != 5 {
		t.Fatalf("got %d items after %d requests, err %v", len(got), model.Calls(), err)
	}
	for i, item := range got {
		value, itemErr := item.Value()
		if i == 2 {
			if itemErr == nil || value != (invoice{}) {
				t.Errorf("failed item returned %+v, %v", value, itemErr)
			}
			continue
		}
		if itemErr != nil || value.Number == "" {
			t.Errorf("item %d = %+v, %v", i, value, itemErr)
		}
	}
}

func TestRunEachPanickingDescriptorIsAnItemError(t *testing.T) {
	t.Parallel()
	d := invoiceDescriptor()
	d.BuildPrompt = func(string, op.Options) (string, string) { panic(canary) }
	d.Policy.RepairAttempts = -1
	got, err := op.RunEach(clientOn(echoing()), d, []string{"one"}, op.PlanOptions{Force: op.ShapeAtomic})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d items, err %v", len(got), err)
	}
	value, itemErr := got[0].Value()
	if itemErr == nil || value != (invoice{}) || strings.Contains(chainText(itemErr), canary) {
		t.Fatalf("item = %+v, %v", value, itemErr)
	}
}

func TestRunEachAddsOneSummedRecord(t *testing.T) {
	t.Parallel()
	var rec schemaflux.Recorder
	model := echoing()
	got, err := op.RunEach(clientOn(model, recordingTo(&rec)), invoiceDescriptor(), []string{"a", "b"}, op.PlanOptions{Force: op.ShapeAtomic})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d items, err %v", len(got), err)
	}
	records := rec.Records()
	if len(records) != 1 || records[0].Op != "read-invoice" || records[0].Requests != 2 || records[0].Tokens.Input != 22 || records[0].Tokens.Output != 6 {
		t.Fatalf("records = %+v", records)
	}
}

func TestRunEachBatchedPlanKeepsItemResults(t *testing.T) {
	t.Parallel()
	model := batchAnswering()
	got, err := op.RunEach(clientOn(model), batchDescriptor(), items(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || model.Calls() != 1 {
		t.Fatalf("got %d items in %d calls", len(got), model.Calls())
	}
	for i, item := range got {
		v, err := item.Value()
		if err != nil || v.Number != items(4)[i] {
			t.Errorf("item %d = %+v, %v", i, v, err)
		}
	}
}

// An item's error is converted as the call's own are: it holds the root's *Error,
// of the kind the item failed with, and errors.Is finds the Kind constant.
func TestRunEachItemErrorIsTheRootsErrorOfTheKindTheItemFailedWith(t *testing.T) {
	t.Parallel()
	providerFailures := []schemaflux.Kind{
		schemaflux.ErrUnavailable, schemaflux.ErrRateLimited, schemaflux.ErrTimeout, schemaflux.ErrUnauthorized,
		schemaflux.ErrInvalidRequest, schemaflux.ErrUnsupported, schemaflux.ErrTooLarge, schemaflux.ErrBusy, schemaflux.ErrInternal,
	}
	for _, kind := range providerFailures {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: kind, Err: errors.New("vendor said no")})
			d := invoiceDescriptor()
			d.Policy.RepairAttempts = -1
			got, err := op.RunEach(clientOn(&model), d, []string{"a", "b"}, op.PlanOptions{Force: op.ShapeAtomic})
			if err != nil || len(got) != 2 {
				t.Fatalf("got %d items, err %v", len(got), err)
			}
			for i, item := range got {
				_, itemErr := item.Value()
				e, ok := errors.AsType[*schemaflux.Error](itemErr)
				if !ok {
					t.Fatalf("item %d: the error is %T (%v), and errors.AsType finds no *Error in it", i, itemErr, itemErr)
				}
				if e.Kind != kind || !errors.Is(itemErr, kind) {
					t.Errorf("item %d: kind %v, errors.Is %v, want %v", i, e.Kind, errors.Is(itemErr, kind), kind)
				}
			}
		})
	}
	t.Run(fmt.Sprint(schemaflux.ErrInvalidAnswer), func(t *testing.T) {
		t.Parallel()
		d := invoiceDescriptor()
		d.Policy.RepairAttempts = -1
		got, err := op.RunEach(clientOn(answeringWith(`{"total":1}`)), d, []string{"a"}, op.PlanOptions{Force: op.ShapeAtomic})
		if err != nil {
			t.Fatal(err)
		}
		_, itemErr := got[0].Value()
		e, ok := errors.AsType[*schemaflux.Error](itemErr)
		if !ok || e.Kind != schemaflux.ErrInvalidAnswer || !errors.Is(itemErr, schemaflux.ErrInvalidAnswer) {
			t.Fatalf("the error is %T (%v), want an *Error of kind invalid answer", itemErr, itemErr)
		}
	})
	t.Run(fmt.Sprint(schemaflux.ErrRefused), func(t *testing.T) {
		t.Parallel()
		var model schemafluxtest.Provider
		model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "", Model: req.Model, FinishReason: "content_filter"}, nil
		})
		d := invoiceDescriptor()
		d.Policy.RepairAttempts = -1
		got, err := op.RunEach(clientOn(&model), d, []string{"a"}, op.PlanOptions{Force: op.ShapeAtomic})
		if err != nil {
			t.Fatal(err)
		}
		_, itemErr := got[0].Value()
		e, ok := errors.AsType[*schemaflux.Error](itemErr)
		if !ok || e.Kind != schemaflux.ErrRefused || !errors.Is(itemErr, schemaflux.ErrRefused) {
			t.Fatalf("the error is %T (%v), want an *Error of kind refused", itemErr, itemErr)
		}
	})
	t.Run(fmt.Sprint(schemaflux.ErrInternal)+" from a panicking descriptor", func(t *testing.T) {
		t.Parallel()
		d := invoiceDescriptor()
		d.BuildPrompt = func(string, op.Options) (string, string) { panic(canary) }
		d.Policy.RepairAttempts = -1
		got, err := op.RunEach(clientOn(echoing()), d, []string{"a"}, op.PlanOptions{Force: op.ShapeAtomic})
		if err != nil {
			t.Fatal(err)
		}
		_, itemErr := got[0].Value()
		if e, ok := errors.AsType[*schemaflux.Error](itemErr); !ok || e.Kind != schemaflux.ErrInternal {
			t.Fatalf("the error is %T (%v), want an *Error of kind internal", itemErr, itemErr)
		}
	})
}

// A run that ends on its context: the items that did not finish carry the
// cancellation as the root's error of kind canceled.
func TestRunEachItemsCutShortByTheContextCarryTheCancellationAsTheRootsError(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	ctx, stop := context.WithCancel(clientOn(&model))
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		stop()
		return schemaflux.Response{}, context.Canceled
	})
	d := invoiceDescriptor()
	d.Policy.RepairAttempts = -1
	got, err := op.RunEach(ctx, d, []string{"a", "b", "c"}, op.PlanOptions{Force: op.ShapeAtomic})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for i, item := range got {
		_, itemErr := item.Value()
		if itemErr == nil {
			continue
		}
		if e, ok := errors.AsType[*schemaflux.Error](itemErr); !ok || e.Kind != schemaflux.ErrCanceled || !errors.Is(itemErr, schemaflux.ErrCanceled) {
			t.Errorf("item %d: the error is %T (%v), want an *Error of kind canceled", i, itemErr, itemErr)
		}
	}
}

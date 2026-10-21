package op_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// exampleInvoice is what the descriptors of these examples read an answer into.
type exampleInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

// exampleDescriptor reads an invoice out of a text. The answer is refused when it
// holds no number, and the model is then shown why and asked again.
var exampleDescriptor = op.Must(op.Descriptor[string, exampleInvoice]{
	ID: op.ID{Name: "read-invoice", Version: "1"},
	BuildPrompt: func(text string, _ op.Options) (system, user string) {
		return "Read the invoice. Return a JSON object with number and total.", text
	},
	Contract: op.Contract[exampleInvoice]{
		Decode: func(body string) (exampleInvoice, error) {
			var v exampleInvoice
			if err := json.Unmarshal([]byte(body), &v); err != nil {
				return v, errors.New("the answer is not a JSON object")
			}
			if v.Number == "" {
				return v, errors.New("the answer has no number")
			}
			return v, nil
		},
	},
})

// exampleContext returns a context whose client asks the fake model, which
// answers with the bodies given in order.
func exampleContext(bodies ...string) (context.Context, *schemafluxtest.Provider) {
	var model schemafluxtest.Provider
	model.Reply(bodies...)
	return schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model}), &model
}

func ExampleNew() {
	_, err := op.New(op.Descriptor[string, string]{
		ID:        op.ID{Name: "echo"},
		Semantics: op.Semantics{Stability: op.StabilityStable},
		BuildPrompt: func(text string, _ op.Options) (string, string) {
			return "Repeat the text.", text
		},
		Contract: op.Contract[string]{Decode: func(body string) (string, error) { return body, nil }},
	})
	var e *schemaflux.Error
	if errors.As(err, &e) {
		fmt.Println(e.Kind, "-", e.Message)
	}
	// Output: configuration - the descriptor is declared stable and has no Batch.Class: say how it batches, BatchNone if it does not
}

func ExampleMust() {
	d := op.Must(op.Descriptor[string, string]{
		ID: op.ID{Name: "echo", Version: "2"},
		BuildPrompt: func(text string, _ op.Options) (string, string) {
			return "Repeat the text.", text
		},
		Contract: op.Contract[string]{Decode: func(body string) (string, error) { return body, nil }},
	})
	fmt.Println(d.ID)
	// Output: echo@2
}

func ExampleRun() {
	ctx, _ := exampleContext(`{"total":120.5}`, `{"number":"A-17","total":120.5}`)
	var recorder schemaflux.Recorder
	ctx = schemaflux.WithRecorder(ctx, &recorder)

	invoice, err := op.Run(ctx, exampleDescriptor, "Invoice A-17 for 120.50")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(invoice.Number, invoice.Total)

	record := recorder.Records()[0]
	fmt.Println(record.Op, "sent", record.Requests, "requests,", record.Repairs, "repair")
	fmt.Println(record.Checks[0].Name, record.Checks[0].Passed)
	// Output:
	// A-17 120.5
	// read-invoice sent 2 requests, 1 repair
	// repair false
}

func ExampleRunBatch() {
	var decode = func(raw json.RawMessage) (exampleInvoice, error) {
		var v exampleInvoice
		err := json.Unmarshal(raw, &v)
		return v, err
	}
	d := exampleDescriptor
	d.Batch = op.Batch[string, exampleInvoice]{
		Class:        op.BatchItemwise,
		Kind:         op.AlgebraIndependent,
		Instructions: "Read each invoice. Answer with the id of each and an object with number and total.",
		Item:         decode,
	}

	// The fake answers a batched request with the ids the request made.
	var model schemafluxtest.Provider
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		payload := strings.TrimPrefix(req.User, "Process these items:\n")
		var tagged []struct{ ID, Item string }
		_ = json.Unmarshal([]byte(payload), &tagged)
		var answers []string
		for _, t := range tagged {
			answers = append(answers, fmt.Sprintf(`{"id":%q,"output":{"number":%q,"total":1}}`, t.ID, t.Item))
		}
		return schemaflux.Response{Content: `{"items":[` + strings.Join(answers, ",") + `]}`}, nil
	})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	invoices, err := op.RunBatch(ctx, d, []string{"A-1", "B-2", "C-3"})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, v := range invoices {
		fmt.Println(v.Number)
	}
	fmt.Println(model.Calls(), "request")
	// Output:
	// A-1
	// B-2
	// C-3
	// 1 request
}

func ExampleRunMany() {
	var model schemafluxtest.Provider
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{Content: fmt.Sprintf(`{"number":%q,"total":1}`, req.User)}, nil
	})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var recorder schemaflux.Recorder
	ctx = schemaflux.WithRecorder(ctx, &recorder)

	invoices, err := op.RunMany(ctx, exampleDescriptor, []string{"A-1", "B-2", "C-3"}, op.PlanOptions{Force: op.ShapeAtomic})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, v := range invoices {
		fmt.Println(v.Number)
	}
	record := recorder.Records()[0]
	fmt.Println(len(recorder.Records()), "Record:", record.Requests, "requests,", record.Strategy)
	// Output:
	// A-1
	// B-2
	// C-3
	// 1 Record: 3 requests, atomic
}

func ExamplePreflight() {
	ctx, model := exampleContext()
	items := []string{"A-1", "B-2", "C-3", "D-4"}

	plan, err := op.Preflight(ctx, exampleDescriptor, items)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Shape, plan.ItemCount, "items,", plan.MaxCalls, "calls, eligible:", plan.Eligible)
	fmt.Println("requests sent:", model.Calls())

	// A shape the descriptor cannot run is a plan that says so, not an error.
	plan, _ = op.Preflight(ctx, exampleDescriptor, items, op.PlanOptions{Force: op.ShapeBatched})
	fmt.Println(plan.Eligible, plan.IneligibleKind)
	// Output:
	// atomic 4 items, 4 calls, eligible: true
	// requests sent: 0
	// false configuration
}

func ExampleWithParents() {
	ctx, _ := exampleContext(`{"number":"A-17"}`)
	var recorder schemaflux.Recorder
	ctx = schemaflux.WithRecorder(ctx, &recorder)

	ctx = op.WithParents(ctx, "r-first", "r-second")
	if _, err := op.Run(ctx, exampleDescriptor, "Invoice A-17"); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(recorder.Records()[0].Provenance.ParentResultIDs)
	// Output: [r-first r-second]
}

func ExampleCatalog() {
	for _, entry := range op.Catalog()[:3] {
		fmt.Println(entry.Name, entry.Category, entry.Stability)
	}
	// Output:
	// annotate generate stable
	// assess classify stable
	// assessdetail classify stable
}

func ExampleLookup() {
	entry, found := op.Lookup("exp.synthesize")
	fmt.Println(entry.Name, entry.Category, entry.Stability, found)
	_, found = op.Lookup("read-invoice")
	fmt.Println(found)
	// Output:
	// exp.synthesize generate experimental true
	// false
}

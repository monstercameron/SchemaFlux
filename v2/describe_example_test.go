package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Annotate marks up a text: each annotation, and the same ones keyed by type.
// The model is scripted here, so the example runs offline.
func ExampleAnnotate() {
	var model schemafluxtest.Provider
	model.Reply(`{"annotations":[
		{"type":"entities","value":"Ada Lovelace","text":"Ada Lovelace","start":0,"end":12,"confidence":0.9},
		{"type":"sentiment","value":"neutral","start":0,"end":0,"confidence":0.3}
	],"summary":{"overall_sentiment":"neutral"}}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	notes, err := schemaflux.Annotate(ctx, "Ada Lovelace wrote the first program")
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, a := range notes.ByType["entities"] {
		fmt.Println(a.Value, a.Start, a.End)
	}
	// The sentiment is below the default floor of 0.5 and is left out.
	fmt.Println(len(notes.Annotations), "kept")
	// Output:
	// Ada Lovelace 0 12
	// 1 kept
}

// Explain writes an explanation of a value for a reader, and works out what it
// can about the value itself.
func ExampleExplain() {
	type Plan struct {
		Table string `json:"table"`
		Index string `json:"index"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"explanation":"The plan reads the orders table through its index.","summary":"An indexed read.","key_points":["uses an index"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	ex, err := schemaflux.Explain(ctx, Plan{Table: "orders", Index: "by_date"}, schemaflux.ExplainOptions{Audience: "executive", Depth: 1})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(ex.Explanation)
	fmt.Println(ex.FieldCount, ex.DataComplexity)
	// Output:
	// The plan reads the orders table through its index.
	// 2 low
}

// Question answers a question about a value as the type written first: here a
// string, with the evidence the model gave.
func ExampleQuestion() {
	type Report struct {
		Region  string  `json:"region"`
		Revenue float64 `json:"revenue"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"answer":"Revenue grew","confidence":0.9,"reasoning":"the report says so","evidence":["revenue up 12%"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Question[string](ctx, Report{Region: "EU", Revenue: 12}, "What is the main finding?")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Answer, res.Evidence)
	// Output: Revenue grew [revenue up 12%]
}

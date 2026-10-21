package schemaflux_test

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Extract reads text into a Go type. The model is scripted here, so the example
// runs offline.
func ExampleExtract() {
	type Invoice struct {
		Number string  `json:"number"`
		Total  float64 `json:"total"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"number":"A-17","total":120.5}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	invoice, err := schemaflux.Extract[Invoice](ctx, "Invoice A-17 for 120.50")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(invoice.Number, invoice.Total)
	// Output: A-17 120.5
}

// An answer that does not fit the type is an error and never a half-filled
// value.
func ExampleExtract_invalidAnswer() {
	type Invoice struct {
		Number string `json:"number"`
	}
	var model schemafluxtest.Provider
	model.Reply(`["not","an","invoice"]`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	invoice, err := schemaflux.Extract[Invoice](ctx, "Invoice A-17", schemaflux.ExtractOptions{Mode: schemaflux.ModeStrict})
	fmt.Println(errors.Is(err, schemaflux.ErrInvalidAnswer), invoice == Invoice{})
	// Output: true true
}

// Transform turns a value of one type into another: the type asked for is the
// one written, and the input's type is inferred.
func ExampleTransform() {
	type Order struct {
		Item     string `json:"item"`
		Quantity int    `json:"quantity"`
	}
	type Summary struct {
		Line string `json:"line"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"line":"3 x tea"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	summary, err := schemaflux.Transform[Summary](ctx, Order{Item: "tea", Quantity: 3})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(summary.Line)
	// Output: 3 x tea
}

// Generate makes a value from a prompt. With a string it returns the model's
// text as it came.
func ExampleGenerate() {
	var model schemafluxtest.Provider
	model.Reply("Ada writes Go and proofs.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	bio, err := schemaflux.Generate[string](ctx, "Write a one-line bio for a Go developer")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(bio)
	// Output: Ada writes Go and proofs.
}

// Generate decodes any other type, and refuses an answer that does not fit it.
func ExampleGenerate_structured() {
	type Meal struct {
		Name     string `json:"name"`
		Calories int    `json:"calories"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"name":"Lentil soup","calories":320}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	meal, err := schemaflux.Generate[Meal](ctx, "A vegetarian dinner", schemaflux.GenerateOptions{Style: "plain"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(meal.Name, meal.Calories)
	// Output: Lentil soup 320
}

// Infer fills the fields a value leaves empty and returns the whole of it. A
// field that was filled comes back as it was.
func ExampleInfer() {
	type Contact struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Country string `json:"country"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"name":"Ada","email":"ada@example.com","country":"UK"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	full, err := schemaflux.Infer(ctx, Contact{Email: "ada@example.com"}, schemaflux.InferOptions{Background: "UK customers only"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(full.Name, full.Country)
	// Output: Ada UK
}

// Parse reads structured text with parsers written in Go and sends nothing to a
// model.
func ExampleParse() {
	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	var model schemafluxtest.Provider
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Parse[Person](ctx, "Ada|36", schemaflux.ParseOptions{FormatHints: []string{"name|age"}})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Data.Name, res.Data.Age, res.Format, model.Calls())
	// Output: Ada 36 pipe-delimited 0
}

// Parse asks a model only when it is allowed to and no parser could read the
// input; the format then says so.
func ExampleParse_fallback() {
	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	var model schemafluxtest.Provider
	model.Reply(`{"name":"Ada","age":36}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Parse[Person](ctx, "Ada is thirty six", schemaflux.ParseOptions{AllowLLMFallback: true})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Data.Name, res.Data.Age, res.Format)
	// Output: Ada 36 unknown (LLM-assisted)
}

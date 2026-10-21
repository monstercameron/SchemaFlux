package schemaflux_test

import (
	"context"
	"fmt"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A Client is a value: a copy is changed without touching the original, and
// the context carries the snapshot the client was when it was handed over.
func ExampleClient() {
	var model schemafluxtest.Provider
	c := schemaflux.Client{Provider: &model, Model: "gpt-6-luna", Retries: -1}

	d := c
	d.Model = "another-model"
	fmt.Println(c.Model, d.Model)

	ctx := schemaflux.WithClient(context.Background(), c)
	c.Model = "changed after the context was made"
	_ = ctx // calls made with ctx still ask for "gpt-6-luna"
	// Output: gpt-6-luna another-model
}

// The default is a client the library always has: until SetDefault is called it
// is the zero Client, and the value Default returns is a copy.
func ExampleDefault() {
	c := schemaflux.Default()
	c.Model = "edited copy"
	fmt.Println(schemaflux.Default().Model == "edited copy")
	// Output: false
}

// SetDefault is the only thing that changes the default client, which the
// calls whose context carries no client run on.
func ExampleSetDefault() {
	previous := schemaflux.Default()
	defer schemaflux.SetDefault(previous)

	var model schemafluxtest.Provider
	schemaflux.SetDefault(schemaflux.Client{Provider: &model, Model: "gpt-6-luna"})
	fmt.Println(schemaflux.Default().Model)
	// Output: gpt-6-luna
}

// WithClient gives a call its client the way a context gives it its deadline.
func ExampleWithClient() {
	var model schemafluxtest.Provider
	model.Reply("Short.")
	tenant := schemaflux.Client{
		Provider: &model,
		Budget:   &schemaflux.Budget{Limit: 5},
		Timeout:  30 * time.Second,
	}
	ctx := schemaflux.WithClient(context.Background(), tenant)
	_ = ctx // schemaflux.Summarize(ctx, text) runs on the tenant's client
	fmt.Println("a context with a client")
	// Output: a context with a client
}

// Warm is the check to run at start-up: it says whether the client could make a
// call, and opens the connection to its provider, without sending a prompt.
func ExampleClient_Warm() {
	var model schemafluxtest.Provider
	c := schemaflux.Client{Provider: &model}
	fmt.Println(c.Warm(context.Background()))
	// Output: <nil>
}

// A Budget counts what a client spent and refuses calls once its limit is
// reached. Keep one per tenant: a Budget made where the call is made is a new
// ledger each time.
func ExampleBudget() {
	budget := &schemaflux.Budget{Limit: 5}
	usd, complete := budget.Spent()
	fmt.Println(usd, complete)
	// Output: 0 true
}

// A Scheduler bounds how many requests are with a provider at once.
func ExampleScheduler() {
	scheduler := &schemaflux.Scheduler{MaxConcurrent: 4, MaxQueued: 16}
	fmt.Printf("%+v\n", scheduler.Stats())
	fmt.Println(scheduler.Close(context.Background()))
	// Output:
	// {InFlight:0 Queued:0 Tokens:0 Cost:0}
	// <nil>
}

// A DataPolicy lists where calls may go. A list left empty allows nothing.
func ExampleDataPolicy() {
	policy := schemaflux.DataPolicy{
		Providers:        []string{"openai"},
		Regions:          []string{"us"},
		MaxRetentionDays: 30,
		MinContract:      schemaflux.ContractSchemaConstrained,
	}
	c := schemaflux.Client{Policy: &policy}
	fmt.Println(c.Policy.MinContract)
	// Output: schema constrained
}

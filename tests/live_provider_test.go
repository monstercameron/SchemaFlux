package tests

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// These are the only tests in the repository that spend money. They are gated
// behind SCHEMAFLUX_LIVE_TESTS=1 so a plain `go test ./...` never bills the
// operator, and they load the credential from .env the same way an application
// would.
//
//	SCHEMAFLUX_LIVE_TESTS=1 go test . -run TestLive -v
//
// What they exist to prove is the one thing the scripted provider cannot: that
// the request this library builds is a request the live Responses API accepts,
// and that the response it returns is one this library parses. Every other test
// asserts behaviour against a body we wrote ourselves.

func requireLive(t *testing.T) {
	t.Helper()

	if os.Getenv("SCHEMAFLUX_LIVE_TESTS") != "1" {
		t.Skip("set SCHEMAFLUX_LIVE_TESTS=1 to run the live tests; they spend money")
	}
	if err := schemaflux.InitWithEnv(); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}
}

type liveInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
	Vendor string  `json:"vendor"`
}

// The whole point of the library: an unstructured string in, a typed value out,
// against the real model.
func TestLiveExtract(t *testing.T) {
	requireLive(t)

	invoice, err := schemaflux.Extract[liveInvoice](
		"Invoice INV-4417 from Northwind Traders, total $1,284.50.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if !strings.Contains(invoice.Number, "4417") {
		t.Errorf("Number = %q, want it to contain 4417", invoice.Number)
	}
	if invoice.Total != 1284.50 {
		t.Errorf("Total = %v, want 1284.50", invoice.Total)
	}
	if invoice.Vendor == "" {
		t.Error("Vendor was not extracted")
	}
	t.Logf("extracted: %+v", invoice)
}

// The default model must be one the account can actually call. A tier that
// resolves to a model the API rejects is a configuration bug that no scripted
// test can catch.
func TestLiveEveryTierResolvesToACallableModel(t *testing.T) {
	requireLive(t)

	for _, tc := range []struct {
		name  string
		speed schemaflux.Speed
	}{
		{"smart", schemaflux.Smart},
		{"fast", schemaflux.Fast},
		{"quick", schemaflux.Quick},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := schemaflux.NewExtractOptions()
			opts.OpOptions.Intelligence = tc.speed

			result, err := schemaflux.Extract[liveInvoice](
				"Invoice INV-1 from Acme, total $2.00.", opts)
			if err != nil {
				t.Fatalf("the %s tier resolves to a model this account cannot call: %v", tc.name, err)
			}
			if result.Total != 2.00 {
				t.Errorf("Total = %v, want 2.00", result.Total)
			}
		})
	}
}

// Usage has to come back populated, because cost accounting is built on it and
// a silently-zero token count reads as a free call.
func TestLiveUsageIsReported(t *testing.T) {
	requireLive(t)

	client := schemaflux.GetDefaultClient()
	if client == nil {
		t.Fatal("no client after InitWithEnv")
	}

	summary, err := schemaflux.Summarize(
		strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20),
		schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatal("the summary is empty")
	}
	t.Logf("summary: %s", summary)
}

// A structured operation whose contract is a shape, not prose. This is the case
// most likely to break against a real model: the scripted tests always return
// the shape we asked for.
func TestLiveValidateReturnsAUsableVerdict(t *testing.T) {
	requireLive(t)

	type person struct {
		Email string `json:"email"`
		Age   int    `json:"age"`
	}

	result, err := schemaflux.Validate(
		person{Email: "not-an-email", Age: 12},
		schemaflux.NewValidateOptions().WithRules("email must be a valid address, age must be at least 18"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Error("a record breaking both rules must not validate")
	}
	if len(result.Errors) == 0 {
		t.Error("the verdict names no issues, which is not a usable verdict")
	}
	for _, issue := range result.Errors {
		t.Logf("issue: %s: %s", issue.Field, issue.Message)
	}
}

// Cancellation has to work against a real connection, not only against a
// scripted provider that returns instantly.
func TestLiveHonoursContextCancellation(t *testing.T) {
	requireLive(t)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	opts := schemaflux.NewExtractOptions()
	opts.OpOptions.Context = ctx

	start := time.Now()
	_, err := schemaflux.Extract[liveInvoice]("Invoice INV-1 from Acme, total $2.00.", opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a 1ms deadline must not produce a result")
	}
	if elapsed > 20*time.Second {
		t.Errorf("cancellation took %v; the deadline is not reaching the transport", elapsed)
	}
}

// A model that does not exist must produce a clear error rather than a hang or
// a zero value.
func TestLiveUnknownModelIsReported(t *testing.T) {
	requireLive(t)

	t.Setenv("SCHEMAFLUX_MODEL", "gpt-5.6-does-not-exist")
	if err := schemaflux.InitWithEnv(); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}

	_, err := schemaflux.Extract[liveInvoice]("Invoice INV-1 from Acme, total $2.00.",
		schemaflux.NewExtractOptions())
	if err == nil {
		t.Fatal("an unknown model must be reported")
	}
	t.Logf("error: %v", err)
}

// D-01: the schema described a nested field by its Go type name, so a model
// asked for an Order was told its customer was a "main.Person" and had to
// invent the shape. This is the case that was broken, against the real model.
func TestLiveExtractNestedStructure(t *testing.T) {
	requireLive(t)

	type liveCustomer struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	type liveOrderLine struct {
		SKU      string  `json:"sku"`
		Quantity int     `json:"quantity"`
		Price    float64 `json:"price"`
	}
	type liveOrder struct {
		Number   string          `json:"number"`
		Customer liveCustomer    `json:"customer"`
		Lines    []liveOrderLine `json:"lines"`
	}

	order, err := schemaflux.Extract[liveOrder](
		"Order ORD-77 for Ada Lovelace (ada@example.com): 2x SKU A-100 at $12.50, 1x SKU A-200 at $87.00.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if order.Customer.Name == "" {
		t.Error("the nested customer name was not extracted")
	}
	if order.Customer.Email != "ada@example.com" {
		t.Errorf("Customer.Email = %q", order.Customer.Email)
	}
	if len(order.Lines) != 2 {
		t.Fatalf("extracted %d lines, want 2: %+v", len(order.Lines), order.Lines)
	}
	if order.Lines[0].Quantity != 2 || order.Lines[0].Price != 12.50 {
		t.Errorf("Lines[0] = %+v", order.Lines[0])
	}

	t.Logf("extracted: %+v", order)
}

// I-05: the library generated an exact schema for T, rendered it into the
// prompt as prose, and then asked the API only for a json_object. The one
// artifact that could make Extract[T] structurally guaranteed never reached the
// API that can enforce it.
//
// This is the check that matters: the live API accepts the generated schema and
// enforces it. A schema the API rejects would be worse than none.
func TestLiveStructuredOutputIsEnforced(t *testing.T) {
	requireLive(t)

	type liveAddress struct {
		City    string `json:"city"`
		Country string `json:"country"`
	}
	type liveContact struct {
		Name    string      `json:"name"`
		Age     int         `json:"age"`
		Address liveAddress `json:"address"`
		Tags    []string    `json:"tags"`
		Note    string      `json:"note,omitempty"`
	}

	contact, err := schemaflux.Extract[liveContact](
		"Ada Lovelace, 36, lives in London, United Kingdom. Interests: mathematics, engines.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("the live API rejected the generated schema or the result: %v", err)
	}

	if contact.Name == "" {
		t.Error("name was not extracted")
	}
	if contact.Age != 36 {
		t.Errorf("Age = %d, want 36", contact.Age)
	}
	if contact.Address.City == "" || contact.Address.Country == "" {
		t.Errorf("Address = %+v", contact.Address)
	}
	if len(contact.Tags) == 0 {
		t.Error("tags were not extracted")
	}

	t.Logf("extracted under a strict schema: %+v", contact)
}

// CF-01 against the real model: a repair costs one extra call and rescues a
// case that used to be terminal. The first answer is forced to be unusable by
// asking for prose, which is the failure a repair exists for.
func TestLiveRepairRescuesAnUnusableAnswer(t *testing.T) {
	requireLive(t)

	type person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	// A real extraction, which the model may or may not get right first time.
	// Either way it must succeed: that is the point of the loop.
	result, err := schemaflux.Extract[person](
		"Write about Ada Lovelace, who was 36. Explain your reasoning at length before answering.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Name == "" {
		t.Errorf("name was not extracted: %+v", result)
	}
	t.Logf("extracted: %+v", result)
}

package schemaflux_test

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

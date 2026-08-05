package schemaflux_test

import (
	"os"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/pricing"
)

// Cerebras is the secondary provider. Everything below is gated the same way
// the OpenAI live tests are, and additionally on a Cerebras credential being
// present, so an operator with only one of the two keys still gets a clean run.
//
//	SCHEMAFLUX_LIVE_TESTS=1 go test . -run TestLiveCerebras -v
//
// These exist for the reason the OpenAI ones do, but the stakes are higher
// here: the compatible transport was rewritten to carry a strict schema, and
// whether a real Cerebras endpoint accepts the schema this library generates is
// not something a body we wrote ourselves can answer.

func requireLiveCerebras(t *testing.T) *schemaflux.Client {
	t.Helper()
	requireLive(t)

	if !anyEnvSet("SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY", "CEREBRAS") {
		t.Skip("no Cerebras credential in the environment")
	}

	previous := schemaflux.GetDefaultClient()
	t.Cleanup(func() { schemaflux.SetDefaultClient(previous) })

	client := schemaflux.NewClient("").
		WithProvider("cerebras").
		WithTimeout(90 * time.Second)
	if err := client.Err(); err != nil {
		t.Fatalf("configuring cerebras: %v", err)
	}
	schemaflux.SetDefaultClient(client)

	return client
}

func anyEnvSet(names ...string) bool {
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

// The whole point: an unstructured string in, a typed value out, against the
// real secondary provider.
func TestLiveCerebrasExtract(t *testing.T) {
	requireLiveCerebras(t)

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

// The claim this whole change rests on: the schema is not advice. A model asked
// only for "some JSON" answers a question like this in prose or with an extra
// commentary field; a model under constrained decoding cannot.
func TestLiveCerebrasStructuredOutputIsEnforced(t *testing.T) {
	requireLiveCerebras(t)

	type verdict struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}

	result, err := schemaflux.Extract[verdict](
		"Please explain your reasoning at length in plain prose before answering. "+
			"The expense claim is for $12,000 of office snacks against a $500 limit.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("a strict schema should hold even against a prose instruction: %v", err)
	}
	if result.Approved {
		t.Errorf("approved a $12,000 claim against a $500 limit: %+v", result)
	}
	if strings.TrimSpace(result.Reason) == "" {
		t.Error("no reason was given")
	}
	t.Logf("verdict: %+v", result)
}

// Every tier has to resolve to a model this account can call. A tier mapped to
// a model the vendor does not serve is a configuration bug no scripted test
// can catch, and the tier map was written from a docs page, not from a call.
func TestLiveCerebrasEveryTierResolvesToACallableModel(t *testing.T) {
	requireLiveCerebras(t)

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

// A nested type is where a generated schema most often trips a vendor's limits:
// nesting depth, additionalProperties on inner objects, arrays of objects.
func TestLiveCerebrasExtractNestedStructure(t *testing.T) {
	requireLiveCerebras(t)

	type lineItem struct {
		Description string  `json:"description"`
		Amount      float64 `json:"amount"`
	}
	type order struct {
		OrderID string     `json:"order_id"`
		Items   []lineItem `json:"items"`
		Total   float64    `json:"total"`
	}

	result, err := schemaflux.Extract[order](
		"Order SO-88: 2 widgets at $10.00 each ($20.00), 1 gasket at $4.50. Total $24.50.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if !strings.Contains(result.OrderID, "88") {
		t.Errorf("OrderID = %q", result.OrderID)
	}
	if len(result.Items) != 2 {
		t.Errorf("got %d line items, want 2: %+v", len(result.Items), result.Items)
	}
	if result.Total != 24.50 {
		t.Errorf("Total = %v, want 24.50", result.Total)
	}
	t.Logf("order: %+v", result)
}

// The one keyword class the transport strips. If the vendor did accept these
// after all, stripping would be an unnecessary loss -- and if it does not, an
// unstripped schema takes the whole call down. Either way this is the test that
// tells us which.
func TestLiveCerebrasHandlesATimestampField(t *testing.T) {
	requireLiveCerebras(t)

	type event struct {
		Name string    `json:"name"`
		At   time.Time `json:"at"`
	}

	result, err := schemaflux.Extract[event](
		"The deployment finished at 2026-03-14T09:30:00Z. Call it 'release'.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("a time.Time field must not take the request down: %v", err)
	}
	if result.At.IsZero() {
		t.Errorf("the timestamp did not parse: %+v", result)
	}
	t.Logf("event: %+v", result)
}

// Usage has to come back populated: cost accounting is built on it, and a
// silently-zero token count reads as a free call. Cerebras is the provider
// whose entire appeal is a cheaper number, so the number has to be real.
func TestLiveCerebrasReportsUsageAndCost(t *testing.T) {
	requireLiveCerebras(t)

	before := pricing.GetTotalCost(time.Now().Add(-time.Minute), map[string]string{"provider": "cerebras"})

	summary, err := schemaflux.Summarize(
		strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20),
		schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatal("the summary is empty")
	}

	// gemma-4-31b is in the pricing table, so the call has to move the total.
	// A model that is priced but reports no tokens, or one that reports tokens
	// but is unpriced, both come out as a free call -- and a provider whose
	// entire appeal is a cheaper number has to produce a real number.
	after := pricing.GetTotalCost(time.Now().Add(-time.Minute), map[string]string{"provider": "cerebras"})
	if after <= before {
		t.Errorf("cost did not move: %v -> %v; usage or pricing is not reaching the accounting", before, after)
	}
	t.Logf("summary: %s", summary)
	t.Logf("cerebras cost this minute: $%.8f", after)
}

// A caller who names a model the vendor does not serve must get the vendor's
// own reason, not a parse failure three layers up.
func TestLiveCerebrasReportsAnUnknownModel(t *testing.T) {
	requireLiveCerebras(t)

	// There is no per-call model override, so the global one is the seam.
	t.Setenv("SCHEMAFLUX_MODEL", "no-such-model-9000")

	_, err := schemaflux.Extract[liveInvoice]("Invoice INV-1 from Acme, total $2.00.",
		schemaflux.NewExtractOptions())
	if err == nil {
		t.Fatal("an unknown model must be an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "model") {
		t.Errorf("the vendor's reason was swallowed: %v", err)
	}
	t.Logf("error: %v", err)
}

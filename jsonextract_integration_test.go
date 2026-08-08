package schemaflux_test

import (
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

type invoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
	Vendor string  `json:"vendor"`
}

const invoiceJSON = `{"number":"INV-4417","total":1284.5,"vendor":"Northwind Traders"}`

// A-003 at the public boundary. Every body here carries the same correct
// invoice; only the packaging differs, and models package their answers all of
// these ways. Before the single extraction path, six of the eight were errors
// whose message blamed the JSON for being malformed.
func TestIntegrationExtractSeesThroughModelPackaging(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"bare object", invoiceJSON},
		{"fenced", "```json\n" + invoiceJSON + "\n```"},
		{"fence with no info string", "```\n" + invoiceJSON + "\n```"},
		{"prose before", "Here is the invoice you asked for:\n\n```json\n" + invoiceJSON + "\n```"},
		{"prose after", "```json\n" + invoiceJSON + "\n```\n\nLet me know if you need the line items."},
		{"prose both sides", "Sure thing!\n\n```json\n" + invoiceJSON + "\n```\n\nHope that helps."},
		{"unfenced after prose", "The extracted invoice is " + invoiceJSON + " — let me know."},
		{"another language's fence first", "```python\nparse(doc)\n```\n\n```json\n" + invoiceJSON + "\n```"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, tc.body, nil)

			result, err := schemaflux.Extract[invoice]("Invoice INV-4417 from Northwind Traders, $1284.50", schemaflux.NewExtractOptions())
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if result.Number != "INV-4417" || result.Vendor != "Northwind Traders" || result.Total != 1284.5 {
				t.Errorf("Extract produced %+v", result)
			}
		})
	}
}

// The other direction still holds at the boundary: a body with no JSON in it is
// an error, not an empty invoice reported as success.
func TestIntegrationExtractStillRefusesProse(t *testing.T) {
	for _, body := range []string{
		"I could not read that document.",
		"<html><body>502 Bad Gateway</body></html>",
		"",
	} {
		withScriptedProvider(t, body, nil)

		if _, err := schemaflux.Extract[invoice]("Invoice INV-4417", schemaflux.NewExtractOptions()); err == nil {
			t.Errorf("Extract accepted a body with no JSON: %q", body)
		}
	}
}

// A truncated body is an error too, and a different one: the JSON that arrived
// really is invalid. What must not happen is the extractor quietly returning
// the part it liked and the decode succeeding on half an answer.
func TestIntegrationTruncatedBodyIsAnError(t *testing.T) {
	withScriptedProvider(t, `{"number":"INV-4417","total":`, nil)

	if _, err := schemaflux.Extract[invoice]("Invoice INV-4417", schemaflux.NewExtractOptions()); err == nil {
		t.Error("Extract accepted a truncated body")
	}
}

// S-008 at the public boundary. Strict mode's contract is exact: an
// unrecognised property is the model producing a field nobody asked for, and
// encoding/json's default is to say nothing about it.
func TestIntegrationStrictModeRejectsOverproduction(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"a field nobody asked for", `{"number":"INV-4417","total":1284.5,"vendor":"Northwind","invented":"value"}`},
		{"a repeated key", `{"number":"INV-4417","number":"INV-9999","total":1284.5,"vendor":"Northwind"}`},
		{"a second answer", `{"number":"INV-4417","total":1284.5,"vendor":"Northwind"} {"number":"INV-9999","total":1,"vendor":"Other"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, tc.body, nil)

			_, err := schemaflux.Extract[invoice]("Invoice INV-4417",
				schemaflux.NewExtractOptions().WithMode(schemaflux.Strict))
			if err == nil {
				t.Fatal("Strict mode accepted it")
			}
		})
	}
}

// Transform mode does not apply the exact contract, because rejecting an extra
// field is exactly wrong for an operation whose contract permits one. The two
// modes have to differ here or Strict means nothing.
func TestIntegrationTransformModeToleratesAnExtraField(t *testing.T) {
	withScriptedProvider(t,
		`{"number":"INV-4417","total":1284.5,"vendor":"Northwind","note":"paid"}`, nil)

	result, err := schemaflux.Extract[invoice]("Invoice INV-4417",
		schemaflux.NewExtractOptions().WithMode(schemaflux.TransformMode))
	if err != nil {
		t.Fatalf("Transform mode refused an extra field: %v", err)
	}
	if result.Number != "INV-4417" {
		t.Errorf("decoded %+v", result)
	}
}

// And the faithful answer still works in Strict mode, so the contract is
// exactness rather than pessimism.
func TestIntegrationStrictModeAcceptsAFaithfulAnswer(t *testing.T) {
	withScriptedProvider(t, invoiceJSON, nil)

	result, err := schemaflux.Extract[invoice]("Invoice INV-4417",
		schemaflux.NewExtractOptions().WithMode(schemaflux.Strict))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Number != "INV-4417" || result.Total != 1284.5 {
		t.Errorf("decoded %+v", result)
	}
}

package schemaflux_test

import (
	"encoding/json"
	"fmt"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

type lineItem struct {
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	UnitCost float64 `json:"unit_cost"`
}

func invoiceLines() []lineItem {
	return []lineItem{
		{SKU: "A-100", Name: "Widget", UnitCost: 12.50},
		{SKU: "A-200", Name: "Gadget", UnitCost: 87.00},
		{SKU: "A-300", Name: "Doohickey", UnitCost: 4.25},
	}
}

func encode(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// The failure this guards against is not an invented record — it is an echoed
// one with a changed number. At the public API a caller has no way to notice.
func TestIntegrationChooseRefusesAnAlteredEcho(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"cost_changed", `{"sku":"A-200","name":"Gadget","unit_cost":78.00}`},
		{"sku_changed", `{"sku":"A-999","name":"Gadget","unit_cost":87.00}`},
		{"name_changed", `{"sku":"A-200","name":"Gadget Pro","unit_cost":87.00}`},
		{"cost_rounded", `{"sku":"A-100","name":"Widget","unit_cost":12.00}`},
		{"invented", `{"sku":"B-100","name":"Nothing","unit_cost":0.01}`},
		{"empty", `{"sku":"","name":"","unit_cost":0}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, tc.body, nil)

			chosen, err := schemaflux.Choose(invoiceLines(),
				schemaflux.NewChooseOptions().WithSteering("the most expensive line"))
			if err == nil {
				t.Fatalf("an altered echo must be refused; got %+v", chosen)
			}
			if chosen.SKU != "" {
				t.Errorf("a refused choice must return nothing, got %+v", chosen)
			}
		})
	}
}

// A faithful echo is accepted, and the value returned is the caller's own.
func TestIntegrationChooseReturnsTheCallersRecord(t *testing.T) {
	lines := invoiceLines()
	withScriptedProvider(t, encode(t, lines[1]), nil)

	chosen, err := schemaflux.Choose(lines, schemaflux.NewChooseOptions().WithSteering("most expensive"))
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if chosen != lines[1] {
		t.Errorf("chosen = %+v, want the input's own %+v", chosen, lines[1])
	}
}

// Filter must return a subset of what it was given, never an authored list.
func TestIntegrationFilterReturnsASubset(t *testing.T) {
	lines := invoiceLines()

	t.Run("faithful_subset", func(t *testing.T) {
		withScriptedProvider(t, encode(t, []lineItem{lines[2], lines[0]}), nil)

		kept, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("under $20"))
		if err != nil {
			t.Fatalf("Filter: %v", err)
		}
		if len(kept) != 2 || kept[0] != lines[0] || kept[1] != lines[2] {
			t.Errorf("kept = %+v, want the input's own items in input order", kept)
		}
	})

	t.Run("edited_item_is_refused", func(t *testing.T) {
		withScriptedProvider(t, `[{"sku":"A-300","name":"Doohickey","unit_cost":3.25}]`, nil)

		kept, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("under $20"))
		if err == nil {
			t.Fatalf("an edited item must be refused; got %+v", kept)
		}
	})

	t.Run("invented_item_is_refused", func(t *testing.T) {
		withScriptedProvider(t, `[{"sku":"Z-000","name":"Phantom","unit_cost":1.00}]`, nil)

		if _, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("cheap")); err == nil {
			t.Fatal("an invented item must be refused")
		}
	})

	t.Run("longer_than_input_is_refused", func(t *testing.T) {
		tooMany := append(append([]lineItem{}, lines...), lines[0])
		withScriptedProvider(t, encode(t, tooMany), nil)

		if _, err := schemaflux.Filter(lines, schemaflux.NewFilterOptions().WithCriteria("all")); err == nil {
			t.Fatal("a subset larger than the set must be refused")
		}
	})
}

// Example_chooseRefusesAnAlteredRecord shows the guarantee that matters: what
// comes back is the record you supplied, or an error. A model that echoes an
// option with a changed price does not get to pass it off as your data. It runs
// under go test with a scripted provider: no credential, no spend.
func Example_chooseRefusesAnAlteredRecord() {
	lines := []lineItem{
		{SKU: "A-100", Name: "Widget", UnitCost: 12.50},
		{SKU: "A-200", Name: "Gadget", UnitCost: 87.00},
	}

	// The model returns the right product at the wrong price.
	schemaflux.NewClient("example-key").WithProviderInstance(&scriptedProvider{
		body: `{"sku":"A-200","name":"Gadget","unit_cost":78.00}`,
	})

	chosen, err := schemaflux.Choose(lines,
		schemaflux.NewChooseOptions().WithSteering("the most expensive line"))

	fmt.Println("error is nil:", err == nil)
	fmt.Println("returned SKU:", chosen.SKU == "")

	// Output:
	// error is nil: false
	// returned SKU: true
}

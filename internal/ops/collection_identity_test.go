package ops

import (
	"encoding/json"
	"strings"
	"testing"
)

type product struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

func catalogue() []product {
	return []product{
		{ID: "p1", Name: "Standard", Price: 19.99},
		{ID: "p2", Name: "Pro", Price: 49.99},
		{ID: "p3", Name: "Enterprise", Price: 199.00},
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// Choose returned whatever the model emitted. A model that echoes an option
// with a subtly altered price produces a result that looks authoritative and
// does not exist in the caller's data — which is far likelier than an invented
// product, and much harder to notice.
func TestChooseRejectsAnItemThatWasNotOffered(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"altered_price", `{"id":"p2","name":"Pro","price":39.99}`},
		{"altered_id", `{"id":"p9","name":"Pro","price":49.99}`},
		{"altered_name", `{"id":"p2","name":"Professional","price":49.99}`},
		{"invented_product", `{"id":"p4","name":"Ultimate","price":999.00}`},
		{"empty_object", `{"id":"","name":"","price":0}`},
		{"rounded_price", `{"id":"p1","name":"Standard","price":20.00}`},
		{"price_as_integer", `{"id":"p3","name":"Enterprise","price":199.01}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM(tc.body)
			defer restore()

			chosen, err := Choose(catalogue(), NewChooseOptions().WithSteering("cheapest"))
			if err == nil {
				t.Fatalf("a selection that was not offered must be an error; got %+v", chosen)
			}
			if chosen.ID != "" {
				t.Errorf("a rejected selection must not return a value, got %+v", chosen)
			}
			// The error describes the mismatch, never the caller's data.
			if strings.Contains(err.Error(), "Enterprise") || strings.Contains(err.Error(), "199") {
				t.Errorf("the error reproduces the payload: %v", err)
			}
		})
	}
}

// A genuine selection is returned — and it is the caller's own value, not the
// model's copy of it.
func TestChooseReturnsTheCallersOwnItem(t *testing.T) {
	items := catalogue()

	for index, want := range items {
		t.Run(want.ID, func(t *testing.T) {
			restore := stubLLM(mustJSON(t, want))
			defer restore()

			chosen, err := Choose(items, NewChooseOptions().WithSteering("pick one"))
			if err != nil {
				t.Fatalf("Choose: %v", err)
			}
			if chosen != items[index] {
				t.Errorf("Choose returned %+v, want the input item %+v", chosen, items[index])
			}
		})
	}
}

// Whitespace and key order in the model's reply must not change the outcome:
// both sides go through T before they are compared.
func TestChooseToleratesFormattingDifferences(t *testing.T) {
	for _, body := range []string{
		`{"price":49.99,"name":"Pro","id":"p2"}`,
		"{\n  \"id\": \"p2\",\n  \"name\": \"Pro\",\n  \"price\": 49.99\n}",
		"```json\n{\"id\":\"p2\",\"name\":\"Pro\",\"price\":49.99}\n```",
	} {
		t.Run(body[:12], func(t *testing.T) {
			restore := stubLLM(body)
			defer restore()

			chosen, err := Choose(catalogue(), NewChooseOptions().WithSteering("pick"))
			if err != nil {
				t.Fatalf("Choose: %v", err)
			}
			if chosen.ID != "p2" {
				t.Errorf("chosen = %+v, want p2", chosen)
			}
		})
	}
}

// Filter has the same identity problem, multiplied: items can be edited,
// dropped, duplicated, or invented, and the count can exceed the input.
func TestFilterRejectsAnythingItWasNotGiven(t *testing.T) {
	items := catalogue()

	cases := []struct {
		name string
		body string
	}{
		{"edited_item", `[{"id":"p1","name":"Standard","price":9.99}]`},
		{"invented_item", `[{"id":"p4","name":"Ghost","price":1.00}]`},
		{"duplicated_item", `[{"id":"p1","name":"Standard","price":19.99},{"id":"p1","name":"Standard","price":19.99}]`},
		{"longer_than_input", `[{"id":"p1","name":"Standard","price":19.99},{"id":"p2","name":"Pro","price":49.99},{"id":"p3","name":"Enterprise","price":199},{"id":"p1","name":"Standard","price":19.99}]`},
		{"one_real_one_invented", `[{"id":"p1","name":"Standard","price":19.99},{"id":"p9","name":"Nope","price":5}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM(tc.body)
			defer restore()

			kept, err := Filter(items, NewFilterOptions().WithCriteria("cheap"))
			if err == nil {
				t.Fatalf("a filter that authored items must be an error; got %+v", kept)
			}
			if kept != nil {
				t.Errorf("a rejected filter must return nothing, got %+v", kept)
			}
		})
	}
}

// A genuine subset is returned, in the input's order, with the caller's values.
func TestFilterReturnsTheCallersOwnItemsInInputOrder(t *testing.T) {
	items := catalogue()

	// The model answers out of order; the result must not be.
	restore := stubLLM(`[{"id":"p3","name":"Enterprise","price":199},{"id":"p1","name":"Standard","price":19.99}]`)
	defer restore()

	kept, err := Filter(items, NewFilterOptions().WithCriteria("any"))
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("kept %d items, want 2", len(kept))
	}
	if kept[0] != items[0] || kept[1] != items[2] {
		t.Errorf("kept = %+v, want the input's own p1 and p3 in that order", kept)
	}
}

// The degenerate subsets are legitimate answers.
func TestFilterAcceptsEmptyAndFullSubsets(t *testing.T) {
	items := catalogue()

	t.Run("empty", func(t *testing.T) {
		restore := stubLLM(`[]`)
		defer restore()

		kept, err := Filter(items, NewFilterOptions().WithCriteria("impossible"))
		if err != nil {
			t.Fatalf("Filter: %v", err)
		}
		if len(kept) != 0 {
			t.Errorf("kept = %+v, want nothing", kept)
		}
	})

	t.Run("everything", func(t *testing.T) {
		restore := stubLLM(mustJSON(t, items))
		defer restore()

		kept, err := Filter(items, NewFilterOptions().WithCriteria("anything"))
		if err != nil {
			t.Fatalf("Filter: %v", err)
		}
		if len(kept) != len(items) {
			t.Errorf("kept %d items, want all %d", len(kept), len(items))
		}
	})
}

// A malformed array used to fall back to parsing the body as a single item and
// returning a one-element slice, so a broken response silently collapsed a
// filter to one result.
func TestFilterDoesNotCollapseToOneItem(t *testing.T) {
	items := catalogue()

	for _, body := range []string{
		`{"id":"p1","name":"Standard","price":19.99}`,
		`{"id":"p1","name":"Standard","price":19.99},{"id":"p2"}`,
		`[{"id":"p1","name":"Standard","price":19.99}`,
	} {
		t.Run(body[:14], func(t *testing.T) {
			restore := stubLLM(body)
			defer restore()

			kept, err := Filter(items, NewFilterOptions().WithCriteria("any"))
			if err == nil {
				t.Fatalf("a malformed array must be an error, not a one-item filter; got %+v", kept)
			}
		})
	}
}

// The system prompt used to say "Include items that match the criteria"
// unconditionally while the steering said "Remove items that match". The model
// received two contradictory orders.
func TestFilterInstructionsAgreeWithKeepMatching(t *testing.T) {
	items := catalogue()

	for _, tc := range []struct {
		name         string
		keepMatching bool
		mustSay      string
		mustNotSay   string
	}{
		{"keep", true, "Return the items that match", "Discard the items that match"},
		{"remove", false, "Discard the items that match", "Return the items that match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewFilterOptions().WithCriteria("cheap")
			opts.KeepMatching = tc.keepMatching

			prompts := capturePrompts(t, func() { _, _ = Filter(items, opts) })
			if len(prompts) == 0 {
				t.Fatal("the operation made no call")
			}
			body := strings.Join(prompts, "\n")

			if !strings.Contains(body, tc.mustSay) {
				t.Errorf("the prompt does not say %q", tc.mustSay)
			}
			if strings.Contains(body, tc.mustNotSay) {
				t.Errorf("the prompt still says the contradictory %q", tc.mustNotSay)
			}
		})
	}
}

// The identity helpers directly, including the shapes the operations do not
// reach on their own.
func TestResolveSubsetInvariants(t *testing.T) {
	items := catalogue()

	t.Run("preserves_input_order", func(t *testing.T) {
		kept, err := resolveSubset([]product{items[2], items[0]}, items)
		if err != nil {
			t.Fatalf("resolveSubset: %v", err)
		}
		if kept[0] != items[0] || kept[1] != items[2] {
			t.Errorf("kept = %+v", kept)
		}
	})

	t.Run("rejects_a_longer_result", func(t *testing.T) {
		longer := append(append([]product{}, items...), items[0])
		if _, err := resolveSubset(longer, items); err == nil {
			t.Error("a subset cannot be larger than the set")
		}
	})

	t.Run("empty_input_empty_result", func(t *testing.T) {
		kept, err := resolveSubset([]product{}, []product{})
		if err != nil || len(kept) != 0 {
			t.Errorf("kept = %+v, err = %v", kept, err)
		}
	})

	t.Run("duplicate_input_matches_once", func(t *testing.T) {
		withDuplicate := []product{items[0], items[0], items[1]}
		kept, err := resolveSubset([]product{items[0]}, withDuplicate)
		if err != nil {
			t.Fatalf("resolveSubset: %v", err)
		}
		if len(kept) != 1 {
			t.Errorf("kept = %+v, want one", kept)
		}
	})
}

// Primitive element types have to work too: the operations advertise them.
func TestIdentityHelpersHandlePrimitives(t *testing.T) {
	words := []string{"alpha", "bravo", "charlie"}

	t.Run("selection", func(t *testing.T) {
		chosen, index, err := resolveSelection("bravo", words)
		if err != nil || chosen != "bravo" || index != 1 {
			t.Errorf("chosen=%q index=%d err=%v", chosen, index, err)
		}
	})

	t.Run("selection_not_offered", func(t *testing.T) {
		if _, _, err := resolveSelection("delta", words); err == nil {
			t.Error("a word that was not offered must be rejected")
		}
	})

	t.Run("subset", func(t *testing.T) {
		kept, err := resolveSubset([]string{"charlie", "alpha"}, words)
		if err != nil {
			t.Fatalf("resolveSubset: %v", err)
		}
		if len(kept) != 2 || kept[0] != "alpha" || kept[1] != "charlie" {
			t.Errorf("kept = %v", kept)
		}
	})
}

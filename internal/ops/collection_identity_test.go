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

// Choose used to return whatever the model emitted, matched back to the
// input by value. Now the model answers with an id, not a copy of the
// option, so there is no field to alter: what these cases exercise is that
// an id naming something that was never assigned -- including a plausible
// mistake like the option's own domain id -- is refused.
func TestChooseRejectsAnItemThatWasNotOffered(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"domain_id_not_a_protocol_id", `{"id":"p2"}`},
		{"unassigned_protocol_id", `{"id":"i-000009"}`},
		{"empty_id", `{"id":""}`},
		{"zero_based_off_by_one", `{"id":"i-000000"}`},
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

// A genuine selection is returned — and it is the caller's own value, named
// by the id assigned at encode time, not anything the model had to
// reproduce.
func TestChooseReturnsTheCallersOwnItem(t *testing.T) {
	items := catalogue()

	for index := range items {
		t.Run(items[index].ID, func(t *testing.T) {
			restore := stubLLM(mustJSON(t, idResponse{ID: itemID(index)}))
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

// Whitespace and key order in the model's reply must not change the outcome.
func TestChooseToleratesFormattingDifferences(t *testing.T) {
	for _, body := range []string{
		`{"id":"i-000002"}`,
		"{\n  \"id\": \"i-000002\"\n}",
		"```json\n{\"id\":\"i-000002\"}\n```",
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

// Filter used to have the same identity problem, multiplied: items could be
// edited, dropped, duplicated, or invented, and the count could exceed the
// input. With an id-only answer, what is left to reject is an id that was
// never assigned, or one repeated.
func TestFilterRejectsAnythingItWasNotGiven(t *testing.T) {
	items := catalogue()

	cases := []struct {
		name string
		body string
	}{
		{"unassigned_id", `{"ids":["i-000009"]}`},
		{"domain_id_not_a_protocol_id", `{"ids":["p1"]}`},
		{"duplicated_id", `{"ids":["i-000001","i-000001"]}`},
		{"longer_than_input", `{"ids":["i-000001","i-000002","i-000003","i-000001"]}`},
		{"one_real_one_unassigned", `{"ids":["i-000001","i-000009"]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM(tc.body)
			defer restore()

			kept, err := Filter(items, NewFilterOptions().WithCriteria("cheap"))
			if err == nil {
				t.Fatalf("a filter that named an id it was not given must be an error; got %+v", kept)
			}
			if kept != nil {
				t.Errorf("a rejected filter must return nothing, got %+v", kept)
			}
		})
	}
}

// A genuine subset is returned, in the input's order, with the caller's
// values -- regardless of the order the ids were answered in.
func TestFilterReturnsTheCallersOwnItemsInInputOrder(t *testing.T) {
	items := catalogue()

	// The model answers out of order; the result must not be.
	restore := stubLLM(mustJSON(t, idListResponse{IDs: []string{itemID(2), itemID(0)}}))
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
		restore := stubLLM(`{"ids":[]}`)
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
		restore := stubLLM(mustJSON(t, idListResponse{IDs: itemIDs(len(items))}))
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

// A malformed body used to fall back to parsing it as a single item and
// returning a one-element slice, so a broken response silently collapsed a
// filter to one result. An id-only answer has no equivalent single-value
// shape to fall back into.
func TestFilterDoesNotCollapseToOneItem(t *testing.T) {
	items := catalogue()

	for _, body := range []string{
		`{"id":"p1"}`,
		`{"ids":"i-000001"}`,
		`[{"id":"p1","name":"Standard","price":19.99}`,
	} {
		t.Run(body[:9], func(t *testing.T) {
			restore := stubLLM(body)
			defer restore()

			kept, err := Filter(items, NewFilterOptions().WithCriteria("any"))
			if err == nil {
				t.Fatalf("a malformed answer must be an error, not a one-item filter; got %+v", kept)
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
		{"keep", true, "Return the ids of the items that match", "Discard the ids of the items that match"},
		{"remove", false, "Discard the ids of the items that match", "Return the ids of the items that match"},
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

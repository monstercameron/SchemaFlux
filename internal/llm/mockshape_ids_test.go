package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// OP-101 moved Choose, Filter, and Sort onto invocation-local ids. The shape
// mock answered the old value-based protocol and kept passing its own tests
// while every credential-free example of those three operations failed, so
// these cases pin the id protocol here rather than only in the examples gate.

func idsRequest(system string, tagged ...string) CompletionRequest {
	return CompletionRequest{
		SystemPrompt: system,
		UserPrompt:   "Items:\n[" + strings.Join(tagged, ",") + "]",
	}
}

const (
	filterSystem = `Return a JSON object {"ids": [...]} listing the ids of the kept items.
- Input [{"id":"i-000001","item":"a"}] -> Output {"ids":["i-000001"]}`
	chooseSystem = `Return a JSON object {"id": "..."} naming the chosen item.
- Input [{"id":"i-000001","item":{"name":"A"}}] -> Output {"id":"i-000001"}`
)

func TestMockAnswersFilterWithEveryID(t *testing.T) {
	req := idsRequest(filterSystem,
		`{"id":"i-000001","item":{"subject":"outage"}}`,
		`{"id":"i-000002","item":{"subject":"password"}}`)

	answer, ok := mockCollectionResponse(req)
	if !ok {
		t.Fatal("the mock produced no answer for a tagged filter request")
	}

	var decoded struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
		t.Fatalf("answer is not the declared shape: %v (%s)", err, answer)
	}
	// Every id is a subset, which is what Filter's invariant checks. A clever
	// answer here would hide that check rather than exercise it.
	if len(decoded.IDs) != 2 || decoded.IDs[0] != "i-000001" || decoded.IDs[1] != "i-000002" {
		t.Errorf("ids = %v, want both input ids in order", decoded.IDs)
	}
}

func TestMockAnswersSortWithAPermutation(t *testing.T) {
	sortSystem := `Return a JSON object {"ids": [...]} listing every id, in sorted order.`
	req := idsRequest(sortSystem,
		`{"id":"i-000001","item":"low"}`,
		`{"id":"i-000002","item":"critical"}`,
		`{"id":"i-000003","item":"medium"}`)

	answer, ok := mockCollectionResponse(req)
	if !ok {
		t.Fatal("no answer for a tagged sort request")
	}

	var decoded struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
		t.Fatalf("answer is not the declared shape: %v (%s)", err, answer)
	}

	seen := map[string]int{}
	for _, id := range decoded.IDs {
		seen[id]++
	}
	if len(seen) != 3 {
		t.Fatalf("ids = %v, want each of the three exactly once", decoded.IDs)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("id %s appears %d times; a sort answer is a permutation", id, count)
		}
	}
}

func TestMockAnswersChooseWithOneID(t *testing.T) {
	req := idsRequest(chooseSystem,
		`{"id":"i-000001","item":{"name":"A"}}`,
		`{"id":"i-000002","item":{"name":"B"}}`)

	answer, ok := mockCollectionResponse(req)
	if !ok {
		t.Fatal("no answer for a tagged choose request")
	}

	var decoded struct {
		ID  string   `json:"id"`
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
		t.Fatalf("answer is not the declared shape: %v (%s)", err, answer)
	}
	if decoded.ID != "i-000001" {
		t.Errorf("id = %q, want one of the offered ids", decoded.ID)
	}
	if len(decoded.IDs) != 0 {
		t.Errorf("a choose answer carried an ids array as well: %v", decoded.IDs)
	}
}

// An untagged array is the older protocol, and the id branch must not claim it
// — answering ids to a prompt that asked for values would turn a working
// operation into a parse failure.
func TestMockLeavesUntaggedItemsToTheValueProtocol(t *testing.T) {
	req := idsRequest(filterSystem, `{"subject":"outage"}`, `{"subject":"password"}`)

	if _, ok := taggedIDs([]any{
		map[string]any{"subject": "outage"},
	}); ok {
		t.Error("taggedIDs claimed an untagged object")
	}

	answer, ok := mockCollectionResponse(req)
	if ok && strings.Contains(answer, "i-0000") {
		t.Errorf("the id branch answered an untagged request: %s", answer)
	}
}

func TestTaggedIDsRejectsPartialAndMalformedTagging(t *testing.T) {
	cases := []struct {
		name  string
		items []any
	}{
		{"empty list", []any{}},
		{"not objects", []any{"i-000001"}},
		{"id missing", []any{map[string]any{"item": 1}}},
		{"item missing", []any{map[string]any{"id": "i-000001"}}},
		{"id not a string", []any{map[string]any{"id": 1, "item": 1}}},
		{"id empty", []any{map[string]any{"id": "", "item": 1}}},
		{"partially tagged", []any{
			map[string]any{"id": "i-000001", "item": 1},
			map[string]any{"subject": "untagged"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ids, ok := taggedIDs(tc.items); ok {
				t.Errorf("accepted %s and produced %v", tc.name, ids)
			}
		})
	}
}

func TestTaggedIDsAcceptsAWellFormedList(t *testing.T) {
	ids, ok := taggedIDs([]any{
		map[string]any{"id": "i-000001", "item": map[string]any{"a": 1}},
		map[string]any{"id": "i-000002", "item": "plain string"},
		map[string]any{"id": "i-000003", "item": nil},
	})
	if !ok {
		t.Fatal("rejected a well-formed tagged list")
	}
	// An item of null is still a tagged item: the id is what the answer names,
	// and the value is the caller's business.
	if len(ids) != 3 || ids[2] != "i-000003" {
		t.Errorf("ids = %v", ids)
	}
}

// A prompt naming neither field is not this protocol, even with tagged items.
func TestIDAnswerDeclinesWhenNeitherFieldIsNamed(t *testing.T) {
	req := idsRequest("Summarise the items.",
		`{"id":"i-000001","item":{"name":"A"}}`)

	if answer, ok := idAnswer(req, []any{
		map[string]any{"id": "i-000001", "item": map[string]any{"name": "A"}},
	}); ok {
		t.Errorf("answered %q for a prompt that named no id field", answer)
	}
}

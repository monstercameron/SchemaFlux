package schemaflux_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// TI-007. The collection invariants, as properties over random inputs rather
// than as hand-picked cases.
//
// The distinction matters because a hand-picked case tests the failure its
// author imagined. These generate the *model's* answer randomly — including
// answers that are wrong in ways nobody wrote down — and assert the same four
// properties every time: Filter returns a subset, Sort a permutation, Choose a
// member, Cluster a partition.
//
// The seed is fixed so a failure is reproducible. A property test that cannot
// be re-run on the input that broke it is a flake generator.
const propertySeed = 20260807

type widget struct {
	SKU   string  `json:"sku"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

func widgets(count int, source *rand.Rand) []widget {
	items := make([]widget, count)
	for i := range items {
		items[i] = widget{
			SKU:   fmt.Sprintf("W-%03d", i),
			Name:  fmt.Sprintf("widget %d", i),
			Price: float64(source.Intn(10000)) / 100,
		}
	}
	return items
}

func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// idFor mirrors the invocation-local id Choose, Filter, and Sort assign to
// the item at position (OP-101): "i-000001" for position 0, and so on. The
// tests build scripted answers against this format because it is the wire
// protocol these operations run, not an implementation detail — an operation
// that changes it is expected to fail TestGoldenPrompts too.
func idFor(position int) string {
	return fmt.Sprintf("i-%06d", position+1)
}

// idListBody is the shape Filter and Sort answer with: the ids of the
// relevant items.
type idListBody struct {
	IDs []string `json:"ids"`
}

// idBody is the shape Choose answers with: the id of the chosen option.
type idBody struct {
	ID string `json:"id"`
}

// Filter's output is a subset of its input, whatever the model returns.
//
// OP-101 changed what there is to corrupt: the model answers with ids, not
// items, so there is no field left to edit — "an offered item, edited" from
// the old echo protocol has no equivalent here. What is left is an id that
// was never assigned, or one repeated.
func TestPropertyFilterReturnsASubset(t *testing.T) {
	source := rand.New(rand.NewSource(propertySeed))
	accepted := 0

	for round := 0; round < 60; round++ {
		items := widgets(3+source.Intn(6), source)

		// A plausible subset, kept id by id. Corruption is applied after and
		// to at most one id: corrupting every id means every round is
		// refused, and a property test whose rounds all error asserts nothing
		// about what comes back when one succeeds.
		var answer []string
		for i := range items {
			if source.Intn(3) != 0 {
				answer = append(answer, idFor(i))
			}
		}
		if len(answer) == 0 {
			answer = append(answer, idFor(0))
		}

		switch source.Intn(3) {
		case 0: // an id that was never assigned
			answer = append(answer, idFor(len(items)+3))
		case 1: // the same id twice
			answer = append(answer, answer[0])
		}

		withScriptedProvider(t, encodeJSON(t, idListBody{IDs: answer}), nil)

		kept, err := schemaflux.Filter(items, schemaflux.NewFilterOptions().WithCriteria("cheap"))
		if err != nil {
			// Refusing a bad answer is the correct outcome; the property is
			// about what comes back when something does.
			continue
		}

		if len(kept) > len(items) {
			t.Fatalf("round %d: Filter returned %d items for %d inputs", round, len(kept), len(items))
		}
		for _, got := range kept {
			if !containsWidget(items, got) {
				t.Fatalf("round %d: Filter returned %+v, which was not in the input", round, got)
			}
		}
		if duplicated(kept) {
			t.Fatalf("round %d: Filter returned the same item twice: %+v", round, kept)
		}
		accepted++
	}

	requireRoundsAsserted(t, accepted)
}

// Sort's output is a permutation of its input.
func TestPropertySortReturnsAPermutation(t *testing.T) {
	source := rand.New(rand.NewSource(propertySeed))
	accepted := 0

	for round := 0; round < 60; round++ {
		items := widgets(3+source.Intn(5), source)

		answer := make([]string, len(items))
		for i := range answer {
			answer[i] = idFor(i)
		}
		source.Shuffle(len(answer), func(i, j int) { answer[i], answer[j] = answer[j], answer[i] })

		// Sometimes corrupt it in a way that keeps the length, which is what a
		// count check cannot see.
		switch source.Intn(3) {
		case 0: // a duplicated id, dropping one that was offered
			answer[0] = answer[len(answer)-1]
		case 1: // an id that was never assigned
			answer[0] = idFor(len(items) + 3)
		}

		withScriptedProvider(t, encodeJSON(t, idListBody{IDs: answer}), nil)

		sorted, err := schemaflux.Sort(items, schemaflux.NewSortOptions().WithCriteria("cheapest first"))
		if err != nil {
			continue
		}

		if len(sorted) != len(items) {
			t.Fatalf("round %d: Sort returned %d items for %d inputs", round, len(sorted), len(items))
		}
		if !sameWidgetMultiset(items, sorted) {
			t.Fatalf("round %d: Sort's result is not a permutation of the input", round)
		}
		accepted++
	}

	requireRoundsAsserted(t, accepted)
}

// Choose's output is one of the items it was offered.
//
// OP-101 changed what there is to corrupt: the model answers with an id, not
// a copy of the item, so "an echoed item with one field changed" has no
// equivalent — there is no field in the answer channel to change. What
// remains is an id that was never assigned.
func TestPropertyChooseReturnsAMember(t *testing.T) {
	source := rand.New(rand.NewSource(propertySeed))
	accepted := 0

	for round := 0; round < 60; round++ {
		items := widgets(2+source.Intn(6), source)

		id := idFor(source.Intn(len(items)))
		if source.Intn(3) == 0 {
			id = idFor(len(items) + 3)
		}

		withScriptedProvider(t, encodeJSON(t, idBody{ID: id}), nil)

		chosen, err := schemaflux.Choose(items, schemaflux.NewChooseOptions().WithSteering("the best one"))
		if err != nil {
			continue
		}
		if !containsWidget(items, chosen) {
			t.Fatalf("round %d: Choose returned %+v, which was never offered", round, chosen)
		}
		accepted++
	}

	requireRoundsAsserted(t, accepted)
}

// Cluster's groups partition the input: every item in exactly one group,
// counting outliers as a group.
func TestPropertyClusterPartitionsTheInput(t *testing.T) {
	source := rand.New(rand.NewSource(propertySeed))
	accepted := 0

	for round := 0; round < 60; round++ {
		items := widgets(3+source.Intn(5), source)

		// Deal every index into exactly one group, then break it occasionally
		// and in one place, for the same reason as above.
		var first, second, outliers []int
		for index := range items {
			switch source.Intn(3) {
			case 0:
				outliers = append(outliers, index)
			case 1:
				second = append(second, index)
			default:
				first = append(first, index)
			}
		}

		switch source.Intn(4) {
		case 0: // an item in no group
			if len(first) > 0 {
				first = first[:len(first)-1]
			}
		case 1: // an item in two groups
			if len(first) > 0 {
				second = append(second, first[0])
			}
		case 2: // an index that does not exist
			first = append(first, len(items)+3)
		}

		body := fmt.Sprintf(
			`{"clusters":[{"name":"a","indices":%s},{"name":"b","indices":%s}],"outlier_indices":%s,"quality":0.8}`,
			encodeJSON(t, first), encodeJSON(t, second), encodeJSON(t, outliers))

		withScriptedProvider(t, body, nil)

		result, err := schemaflux.Cluster(items, schemaflux.NewClusterOptions())
		if err != nil {
			continue
		}

		seen := make([]int, len(items))
		for _, cluster := range result.Clusters {
			for _, index := range cluster.Indices {
				if index < 0 || index >= len(items) {
					t.Fatalf("round %d: cluster index %d is out of range", round, index)
				}
				seen[index]++
			}
			if cluster.Size != len(cluster.Items) {
				t.Fatalf("round %d: Size is %d but Items has %d", round, cluster.Size, len(cluster.Items))
			}
		}
		for _, index := range result.OutlierIndices {
			seen[index]++
		}

		for index, count := range seen {
			if count != 1 {
				t.Fatalf("round %d: item %d appears in %d groups, want exactly one", round, index, count)
			}
		}
		accepted++
	}

	requireRoundsAsserted(t, accepted)
}

// requireRoundsAsserted fails a property test that never reached its
// assertions.
//
// Every round here can end in an error, which is a correct outcome -- refusing
// a bad answer is the point. But a test whose rounds *all* error asserts
// nothing about what comes back when one succeeds, and it would keep passing
// with the property deleted. This is the guard against a vacuous test.
func requireRoundsAsserted(t *testing.T, accepted int) {
	t.Helper()
	if accepted < 5 {
		t.Fatalf("only %d rounds produced a result, so the property was barely exercised", accepted)
	}
	t.Logf("%d rounds produced a result and satisfied the property", accepted)
}

func containsWidget(items []widget, want widget) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func duplicated(items []widget) bool {
	seen := map[widget]bool{}
	for _, item := range items {
		if seen[item] {
			return true
		}
		seen[item] = true
	}
	return false
}

func sameWidgetMultiset(a, b []widget) bool {
	counts := map[widget]int{}
	for _, item := range a {
		counts[item]++
	}
	for _, item := range b {
		counts[item]--
	}
	for _, remaining := range counts {
		if remaining != 0 {
			return false
		}
	}
	return true
}

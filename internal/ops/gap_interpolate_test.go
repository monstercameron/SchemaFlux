package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The early returns of Interpolate, its marshal-error path, a happy path and
// the refusals of a malformed answer. What an answer has to satisfy against the
// items is in interpolate_test.go.

// unmarshalableInterpolateItem carries a channel field, which encoding/json
// refuses to marshal -- Interpolate's own marshal-error path (interpolate.go,
// json.Marshal(items) before the type schema is built).
type unmarshalableInterpolateItem struct {
	Ch chan int `json:"ch"`
}

type dailyReading struct {
	Day   int     `json:"day"`
	Value float64 `json:"value"`
}

func TestInterpolateEmptyItemsIsRefusedBeforeAnyCall(t *testing.T) {
	_, err := Interpolate([]dailyReading{})
	if err == nil {
		t.Fatal("expected an error for zero items")
	}
	if !strings.Contains(err.Error(), "no items") {
		t.Fatalf("error does not name the empty-input problem: %v", err)
	}
}

// TestInterpolateSingleItemIsReturnedUnchangedWithoutACall covers the
// len(items) < 2 early return: nothing to interpolate between, so it never
// reaches callLLM. No LLM caller is installed, so a change that removed this
// early return would panic here instead of quietly making a call.
func TestInterpolateSingleItemIsReturnedUnchangedWithoutACall(t *testing.T) {
	items := []dailyReading{{Day: 1, Value: 5}}
	result, err := Interpolate(items)
	if err != nil {
		t.Fatalf("Interpolate on a single item returned an error: %v", err)
	}
	if len(result.Complete) != 1 || result.Complete[0] != items[0] {
		t.Fatalf("Complete = %+v, want the single input item unchanged", result.Complete)
	}
	if result.Method != "none" {
		t.Fatalf("Method = %q, want %q for a sequence too short to interpolate", result.Method, "none")
	}
}

func TestInterpolateMarshalErrorOnAnUnmarshalableItem(t *testing.T) {
	items := []unmarshalableInterpolateItem{{Ch: make(chan int)}, {Ch: make(chan int)}}
	_, err := Interpolate(items)
	if err == nil {
		t.Fatal("expected a marshal error for channel-bearing items")
	}
	wantKind(t, err, types.KindInvalidRequest)
	if !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("error does not name the marshal failure: %v", err)
	}
}

func TestInterpolateHappyPathFillsGapsAndReportsConfidence(t *testing.T) {
	withResponse(t, `{
		"complete": [
			{"day": 1, "value": 10},
			{"day": 2, "value": 15},
			{"day": 3, "value": 20}
		],
		"filled": [
			{"index": 1, "method": "linear", "based_on": [0, 2], "reasoning": "midpoint of neighbors", "confidence": 0.9}
		],
		"gap_count": 1,
		"method": "linear",
		"average_confidence": 0.9
	}`)

	items := []dailyReading{{Day: 1, Value: 10}, {Day: 2, Value: 0}, {Day: 3, Value: 20}}
	result, err := Interpolate(items, InterpolateOptions{
		Method:        "linear",
		GapIndices:    []int{1},
		SequenceField: "day",
		Constraints:   []string{"values must be positive"},
	})
	if err != nil {
		t.Fatalf("Interpolate failed: %v", err)
	}
	if len(result.Complete) != 3 {
		t.Fatalf("Complete has %d items, want 3", len(result.Complete))
	}
	if result.Complete[1].Value != 15 {
		t.Fatalf("Complete[1] = %+v, want the filled value 15", result.Complete[1])
	}
	if result.GapCount != 1 {
		t.Fatalf("GapCount = %d, want 1", result.GapCount)
	}
	if result.Method != "linear" {
		t.Fatalf("Method = %q, want %q (the response's own method overrides the option default)", result.Method, "linear")
	}
	if result.AverageConfidence != 0.9 {
		t.Fatalf("AverageConfidence = %v, want 0.9", result.AverageConfidence)
	}
	if len(result.Filled) != 1 || result.Filled[0].Index != 1 {
		t.Fatalf("Filled = %+v, want one entry for index 1", result.Filled)
	}
}

// TestInterpolateMethodFallsBackToTheRequestedOneWhenTheResponseOmitsIt
// covers the `if parsed.Method != ""` branch's other side: a response that
// leaves "method" out (or empty) must not blank out the method the caller
// already knows it asked for.
func TestInterpolateMethodFallsBackToTheRequestedOneWhenTheResponseOmitsIt(t *testing.T) {
	withResponse(t, `{
		"complete": [{"day": 1, "value": 10}, {"day": 2, "value": 12}, {"day": 3, "value": 20}],
		"filled": [],
		"gap_count": 0,
		"average_confidence": 0.5
	}`)

	items := []dailyReading{{Day: 1, Value: 10}, {Day: 2, Value: 12}, {Day: 3, Value: 20}}
	result, err := Interpolate(items, InterpolateOptions{Method: "trend"})
	if err != nil {
		t.Fatalf("Interpolate failed: %v", err)
	}
	if result.Method != "trend" {
		t.Fatalf("Method = %q, want the requested %q preserved when the response omits it", result.Method, "trend")
	}
}

func TestInterpolateRefusesMalformedJSON(t *testing.T) {
	withResponse(t, `{"complete": [`) // truncated
	items := []dailyReading{{Day: 1, Value: 1}, {Day: 2, Value: 2}}
	_, err := Interpolate(items)
	if err == nil {
		t.Fatal("expected a parse error for truncated JSON")
	}
}

// TestInterpolateRefusesAnOffTopicResponse: a well-formed JSON object that has
// none of "complete", "filled",
// "gap_count", "method", or "average_confidence" is a response about
// something else, not an empty interpolation.
func TestInterpolateRefusesAnOffTopicResponse(t *testing.T) {
	withResponse(t, `{"unrelated_field": "some other answer entirely"}`)
	items := []dailyReading{{Day: 1, Value: 1}, {Day: 2, Value: 2}}
	_, err := Interpolate(items)
	if err == nil {
		t.Fatal("expected a schema-violation error for an off-topic response")
	}
}

// TestInterpolateRefusesAnItemThatDoesNotMatchTheElementType is the one
// place Interpolate actually refuses a bad answer about its data rather than
// its envelope: each entry of "complete" is unmarshalled into T individually,
// and a shape mismatch on any one of them must fail the whole call rather
// than leaving that slot as T's zero value.
func TestInterpolateRefusesAnItemThatDoesNotMatchTheElementType(t *testing.T) {
	withResponse(t, `{
		"complete": [
			{"day": 1, "value": 10},
			"not an object at all",
			{"day": 3, "value": 20}
		],
		"filled": [{"index": 1, "method": "linear", "confidence": 0.8}],
		"gap_count": 1,
		"average_confidence": 0.8
	}`)

	items := []dailyReading{{Day: 1, Value: 10}, {Day: 2, Value: 0}, {Day: 3, Value: 20}}
	_, err := Interpolate(items)
	if err == nil {
		t.Fatal("expected an error: element 1 of \"complete\" is a string, not a dailyReading object")
	}
	wantKind(t, err, types.KindSchemaViolation)
	if !strings.Contains(err.Error(), "item 1") {
		t.Fatalf("error does not name which item failed to parse: %v", err)
	}
}

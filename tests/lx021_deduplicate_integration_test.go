package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

type lx021Customer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

var lx021Customers = []lx021Customer{
	{"Dana Kim", "dana@example.com"},
	{"Omar Haddad", "omar@example.com"},
	{"D. Kim", "dana@example.com"},
	{"Priya Nair", "priya@example.com"},
	{"Dana  Kim", "DANA@EXAMPLE.COM"},
}

// LX-021 through the exported API and the real OpenAI provider. The request
// declares a strict schema and does not carry the threshold; the groups that
// come back are built in Go from the model's matches, and an answer that cannot
// be used is an error where it once meant "no duplicates".
func TestIntegrationDeduplicate(t *testing.T) {
	cases := []struct {
		name      string
		threshold float64
		body      string
		wantErr   error
		unique    []int
		groups    [][]int
	}{
		{"one group of three", 0.8,
			`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":4,"duplicate_of":0,"similarity":0.95}]}`,
			nil, []int{0, 1, 3}, [][]int{{0, 2, 4}}},
		{"a match below the threshold is not a duplicate", 0.92,
			`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":4,"duplicate_of":0,"similarity":0.95}]}`,
			nil, []int{0, 1, 2, 3}, [][]int{{0, 4}}},
		{"nothing matches", 0.8, `{"duplicates":[]}`, nil, []int{0, 1, 2, 3, 4}, nil},
		{"a chain", 0.8,
			`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":4,"duplicate_of":2,"similarity":0.9}]}`,
			nil, []int{0, 1, 3}, [][]int{{0, 2, 4}}},
		{"the old answer shape", 0.8, `{"groups":[[0,2,4],[1],[3]]}`, schemaflux.ErrSchemaViolation, nil, nil},
		{"prose", 0.8, `Customers 0, 2 and 4 look like the same person.`, schemaflux.ErrMalformedOutput, nil, nil},
		{"no list", 0.8, `{"duplicates":null}`, schemaflux.ErrSchemaViolation, nil, nil},
		{"an item that is not in the list", 0.8, `{"duplicates":[{"index":7,"duplicate_of":0,"similarity":0.9}]}`, schemaflux.ErrInvariantViolation, nil, nil},
		{"a match with a later item", 0.8, `{"duplicates":[{"index":0,"duplicate_of":2,"similarity":0.9}]}`, schemaflux.ErrInvariantViolation, nil, nil},
		{"a similarity off the scale", 0.8, `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":95}]}`, schemaflux.ErrInvariantViolation, nil, nil},
		{"an entry with no similarity", 0.8, `{"duplicates":[{"index":2,"duplicate_of":0}]}`, schemaflux.ErrSchemaViolation, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			result, err := schemaflux.Deduplicate(lx021Customers, tc.threshold)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(result.Unique)+len(result.Duplicates)+len(result.UniqueIndices)+result.TotalRemoved != 0 {
					t.Fatalf("a failed call returned a usable result: %+v", result)
				}
				for _, customer := range lx021Customers {
					if strings.Contains(err.Error(), customer.Email) || strings.Contains(err.Error(), customer.Name) {
						t.Errorf("the error repeats the caller's data: %v", err)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("Deduplicate: %v", err)
				}
				if !reflect.DeepEqual(result.UniqueIndices, tc.unique) {
					t.Errorf("UniqueIndices = %v, want %v", result.UniqueIndices, tc.unique)
				}
				if len(result.DuplicateIndices) != len(tc.groups) || (len(tc.groups) > 0 && !reflect.DeepEqual(result.DuplicateIndices, tc.groups)) {
					t.Errorf("DuplicateIndices = %v, want %v", result.DuplicateIndices, tc.groups)
				}
				for k, index := range result.UniqueIndices {
					if result.Unique[k] != lx021Customers[index] {
						t.Errorf("Unique[%d] is not the caller's item %d", k, index)
					}
				}
				if len(result.Unique)+result.TotalRemoved != len(lx021Customers) {
					t.Errorf("%d kept and %d removed of %d", len(result.Unique), result.TotalRemoved, len(lx021Customers))
				}
			}

			if len(*captured) != 1 {
				t.Fatalf("%d requests reached the provider, want 1", len(*captured))
			}
			text, _ := (*captured)[0]["text"].(map[string]any)
			format, _ := text["format"].(map[string]any)
			if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "duplicates" {
				t.Fatalf("expected the strict duplicates schema on the wire, got %v", format)
			}
			schema, _ := format["schema"].(map[string]any)
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema on the wire breaks a strict-mode rule: %s", problem)
			}
			wire, _ := json.Marshal((*captured)[0])
			if strings.Contains(strings.ToLower(string(wire)), "threshold") {
				t.Fatal("the request tells the model the threshold")
			}
		})
	}
}

// A threshold that is not on the scale is refused before anything is sent. 85
// is the mistake this catches: a percentage where a fraction is meant.
func TestIntegrationDeduplicateRefusesAThresholdOffTheScale(t *testing.T) {
	for _, threshold := range []float64{85, 1.5, -0.2} {
		_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(`{"duplicates":[]}`))
		if _, err := schemaflux.Deduplicate(lx021Customers, threshold); !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Errorf("threshold %v: err = %v, want a configuration error", threshold, err)
		}
		if len(*captured) != 0 {
			t.Errorf("threshold %v: a request was sent", threshold)
		}
	}
}

// ExampleDeduplicate removes repeated records and shows which input rows were
// kept. It runs under go test with a scripted provider: no credential, no
// spend.
func ExampleDeduplicate() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"duplicates": [{"index": 2, "duplicate_of": 0, "similarity": 0.92}, {"index": 3, "duplicate_of": 1, "similarity": 0.6}]}`))

	tickets := []string{
		"Can't log in: password rejected",
		"Invoice shows the wrong tax rate",
		"Login fails, says my password is wrong",
		"Invoice PDF will not download",
	}

	result, err := schemaflux.Deduplicate(tickets, 0.85)
	if err != nil {
		fmt.Println("the list could not be deduplicated:", err)
		return
	}

	fmt.Println("removed:", result.TotalRemoved)
	for k, index := range result.UniqueIndices {
		fmt.Printf("kept tickets[%d]: %s\n", index, result.Unique[k])
	}
	for _, group := range result.DuplicateIndices {
		fmt.Println("the same ticket:", group)
	}

	// Output:
	// removed: 1
	// kept tickets[0]: Can't log in: password rejected
	// kept tickets[1]: Invoice shows the wrong tax rate
	// kept tickets[3]: Invoice PDF will not download
	// the same ticket: [0 2]
}

// ExampleDeduplicate_failsClosed is the counterpart: an answer that names an
// item the list does not have is an error and an empty result, where an answer
// that could not be read once came back as "every item is unique".
func ExampleDeduplicate_failsClosed() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"duplicates": [{"index": 9, "duplicate_of": 0, "similarity": 0.92}]}`))

	result, err := schemaflux.Deduplicate([]string{"a", "b", "a"}, 0.85)

	fmt.Println("error is nil:", err == nil)
	fmt.Println("kept:", len(result.Unique))

	// Output:
	// error is nil: false
	// kept: 0
}

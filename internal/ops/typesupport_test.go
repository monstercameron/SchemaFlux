package ops

// The unexported fields here are the subject: a struct with none of them
// exported is opaque to the schema, and a struct with some is not. They exist
// to be unused.
//lint:file-ignore U1000 unexported fields are the case under test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fullyTyped struct {
	Name    string    `json:"name"`
	Count   int       `json:"count"`
	Price   float64   `json:"price"`
	Active  bool      `json:"active"`
	When    time.Time `json:"when"`
	Tags    []string  `json:"tags"`
	Skipped string    `json:"-"`
	hidden  string
}

type withAny struct {
	Name  string `json:"name"`
	Extra any    `json:"extra"`
}

type withChannel struct {
	Name string   `json:"name"`
	Ch   chan int `json:"ch"`
}

type withMap struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type withIntMap struct {
	Counts map[int]string `json:"counts"`
}

type node struct {
	Name   string `json:"name"`
	Parent *node  `json:"parent,omitempty"`
}

type customWire struct {
	Value string
}

func (c customWire) MarshalJSON() ([]byte, error) { return json.Marshal(c.Value) }

type withCustomWire struct {
	Field customWire `json:"field"`
}

type noExportedFields struct {
	hidden string
}

// S-010. A Go type is the whole contract: it becomes the schema the model is
// shown and the shape the answer is decoded into. The question is when a caller
// finds out that theirs cannot play that role.
func TestClassifyType(t *testing.T) {
	cases := []struct {
		name   string
		target any
		want   TypeSupport
		reason string
	}{
		{"a plain struct", fullyTyped{}, SupportFull, ""},
		{"a slice of them", []fullyTyped{}, SupportFull, ""},
		{"a pointer to one", &fullyTyped{}, SupportFull, ""},
		{"a scalar", "", SupportFull, ""},

		{"a string-keyed map", withMap{}, SupportRestricted, "no field names"},
		{"a self-referential type", node{}, SupportRestricted, "refers to itself"},
		{"a custom JSON representation", withCustomWire{}, SupportRestricted, "custom JSON representation"},

		{"no exported fields", noExportedFields{}, SupportOpaque, "nothing for the schema to describe"},

		{"an any field", withAny{}, SupportRejected, "gives the model nothing"},
		{"a channel field", withChannel{}, SupportRejected, "cannot be represented"},
		{"a map keyed by int", withIntMap{}, SupportRejected, "object keys are strings"},
		{"a bare any", any(nil), SupportRejected, "not concrete"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ClassifyType(reflect.TypeOf(tc.target))
			if report.Support != tc.want {
				t.Fatalf("support = %v (%s), want %v", report.Support, report.Reason, tc.want)
			}
			if tc.reason != "" && !strings.Contains(report.Reason, tc.reason) {
				t.Errorf("reason %q does not mention %q", report.Reason, tc.reason)
			}
		})
	}
}

// The worst finding wins, not the first: a struct with one rejected field is
// rejected whatever else it holds, and telling a caller about a restricted map
// while a channel sits two fields down wastes the trip.
func TestTheWorstFindingWins(t *testing.T) {
	type mixed struct {
		Labels map[string]string `json:"labels"` // restricted
		Extra  any               `json:"extra"`  // rejected
	}

	report := ClassifyType(reflect.TypeOf(mixed{}))
	if report.Support != SupportRejected {
		t.Errorf("support = %v, want rejected -- the restricted field won", report.Support)
	}
	if !strings.Contains(report.Path, "extra") {
		t.Errorf("path = %q, want it to name the rejected field", report.Path)
	}
}

// A rejected shape fails preflight, and the message says no call was made --
// because the difference between "your type is wrong" and "your type is wrong
// and you were billed" is the whole point.
func TestPreflightRefusesRejectedTypes(t *testing.T) {
	err := PreflightType(reflect.TypeOf(withAny{}))
	if err == nil {
		t.Fatal("PreflightType accepted a type with an any field")
	}
	if !strings.Contains(err.Error(), "No provider call was made") {
		t.Errorf("the error does not say the call was skipped: %v", err)
	}
	if !strings.Contains(err.Error(), "extra") {
		t.Errorf("the error does not name the field: %v", err)
	}

	// Restricted and opaque are usable, so preflight passes them.
	for _, target := range []any{withMap{}, node{}, noExportedFields{}, fullyTyped{}} {
		if err := PreflightType(reflect.TypeOf(target)); err != nil {
			t.Errorf("PreflightType(%T) = %v, want nil", target, err)
		}
	}
}

// Nested rejection is found, because a type is only as usable as its worst
// field however deep it sits.
func TestRejectionIsFoundThroughNesting(t *testing.T) {
	type inner struct {
		Ch chan int `json:"ch"`
	}
	type middle struct {
		Inner inner `json:"inner"`
	}
	type outer struct {
		Items []middle `json:"items"`
	}

	if err := PreflightType(reflect.TypeOf(outer{})); err == nil {
		t.Error("a channel three levels down was accepted")
	}
}

// A field excluded from JSON is excluded from the classification too: it is not
// part of the contract, so its type cannot break it.
func TestExcludedFieldsDoNotCount(t *testing.T) {
	type record struct {
		Name string   `json:"name"`
		Ch   chan int `json:"-"`
	}

	if err := PreflightType(reflect.TypeOf(record{})); err != nil {
		t.Errorf("a json:\"-\" channel broke preflight: %v", err)
	}
}

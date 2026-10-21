package ops

import (
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// An extracted value that is not usable is the model's answer being refused, so
// each way ValidateExtractedData refuses one carries the malformed-output kind,
// with the message it has always had.
func TestValidateExtractedDataRefusalsAreMalformedOutput(t *testing.T) {
	type record struct {
		Name string `json:"name"`
	}
	type nested struct {
		Inner record `json:"inner"`
	}
	type withPointer struct {
		Inner *record `json:"inner" schemaflux:"required"`
	}
	var nilRecord *record
	var nilInterface any
	remedy := optionalFieldRemedy
	cases := []struct {
		name  string
		value any
		text  string
	}{
		{"a nil", nilInterface, "data cannot be nil"},
		{"a nil pointer", nilRecord, "data cannot be nil pointer"},
		{"a pointer to a nil pointer", &nilRecord, "data cannot be nil pointer"},
		{"a struct with its field empty", record{}, "required field name is empty; " + remedy},
		{"a pointer to a struct with its field empty", &record{}, "required field name is empty; " + remedy},
		{"a nested struct left zero", nested{}, "required field inner is empty; " + remedy},
		{"a required pointer field left nil", withPointer{}, "required field inner is empty; " + remedy},
		{"a nested struct behind a pointer with its field empty", withPointer{Inner: &record{}}, "required field inner.name is empty; " + remedy},
		{"a nested struct inside a slice", []nested{{Inner: record{Name: "x"}}, {}}, "required field [1].inner is empty; " + remedy},
		{"a nested struct inside a map", map[string]record{"k": {}}, "required field [k].name is empty; " + remedy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExtractedData(tc.value)
			if err == nil {
				t.Fatal("the value was accepted")
			}
			if err.Error() != tc.text {
				t.Errorf("the message changed:\n got %q\nwant %q", err.Error(), tc.text)
			}
			if got := types.KindOf(err); got != types.KindMalformedOutput {
				t.Errorf("the error has kind %v, want malformed output", got)
			}
			if !errors.Is(err, types.ErrMalformedOutput) {
				t.Error("errors.Is does not reach the malformed-output sentinel")
			}
		})
	}
}

func TestValidateExtractedDataAcceptsWhatIsComplete(t *testing.T) {
	type record struct {
		Name string `json:"name"`
		Note string `json:"note,omitempty"`
	}
	for name, value := range map[string]any{
		"a filled struct":          record{Name: "x"},
		"a pointer to a filled":    &record{Name: "x"},
		"a slice of filled":        []record{{Name: "x"}},
		"a number":                 3,
		"a string":                 "text",
		"an empty slice of struct": []record{},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateExtractedData(value); err != nil {
				t.Errorf("a complete value was refused: %v", err)
			}
		})
	}
}

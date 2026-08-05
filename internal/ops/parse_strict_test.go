package ops

import (
	"reflect"
	"strings"
	"testing"
)

// D-11: parseCSV mapped columns by capitalising the header and looking up a Go
// field name, ignoring json tags. A struct with `json:"full_name"` never
// received its full_name column, because "Full_name" is not "FullName" — and an
// unmapped column was skipped silently, so a single-struct target with zero
// matching headers returned a zero-valued struct and a nil error. Success with
// no data.

type csvRecord struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Age      int    `json:"age"`
}

func TestCSVHeadersMatchJSONTags(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"json_tag", "full_name,email,age"},
		{"go_field_name", "FullName,Email,Age"},
		{"spaces", "Full Name,Email,Age"},
		{"hyphens", "full-name,email,age"},
		{"upper", "FULL_NAME,EMAIL,AGE"},
		{"mixed_case", "Full_Name,eMail,AGE"},
		{"padded", " full_name , email , age "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.header + "\nAda Lovelace,ada@example.com,36"

			record, err := parseCSV[csvRecord](input)
			if err != nil {
				t.Fatalf("parseCSV: %v", err)
			}
			if record.FullName != "Ada Lovelace" {
				t.Errorf("FullName = %q, want Ada Lovelace", record.FullName)
			}
			if record.Email != "ada@example.com" {
				t.Errorf("Email = %q", record.Email)
			}
			if record.Age != 36 {
				t.Errorf("Age = %d, want 36", record.Age)
			}
		})
	}
}

// A CSV whose headers reach no field is an error, not an empty success.
func TestCSVWithNoMappableHeadersIsAnError(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"unrelated_headers", "alpha,bravo,charlie\n1,2,3"},
		{"one_column_wrong", "not_a_field\nvalue"},
		{"numeric_headers", "1,2,3\na,b,c"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, err := parseCSV[csvRecord](tc.input)
			if err == nil {
				t.Fatalf("a CSV mapping to no field must be an error; got %+v", record)
			}
			// The error has to name what it looked for, or the caller cannot fix it.
			if !strings.Contains(err.Error(), "full_name") {
				t.Errorf("the error does not name the expected fields: %v", err)
			}
		})
	}
}

// Some columns mapping and some not is fine: the unmapped ones are extra data,
// not a failure.
func TestCSVWithPartiallyMappableHeaders(t *testing.T) {
	input := "full_name,unrelated,age\nAda,ignored,36"

	record, err := parseCSV[csvRecord](input)
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if record.FullName != "Ada" || record.Age != 36 {
		t.Errorf("record = %+v", record)
	}
}

// A slice target behaves the same way.
func TestCSVSliceTarget(t *testing.T) {
	t.Run("maps_every_row", func(t *testing.T) {
		input := "full_name,email,age\nAda,ada@example.com,36\nAlan,alan@example.com,41"

		records, err := parseCSV[[]csvRecord](input)
		if err != nil {
			t.Fatalf("parseCSV: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("parsed %d rows, want 2", len(records))
		}
		if records[1].FullName != "Alan" || records[1].Age != 41 {
			t.Errorf("records[1] = %+v", records[1])
		}
	})

	t.Run("no_mappable_headers_is_an_error", func(t *testing.T) {
		if _, err := parseCSV[[]csvRecord]("x,y,z\n1,2,3"); err == nil {
			t.Error("a slice target with no mappable headers must be an error")
		}
	})
}

// The name folding is the mechanism.
func TestFoldCSVName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"full_name", "fullname"},
		{"Full Name", "fullname"},
		{"FULL-NAME", "fullname"},
		{"FullName", "fullname"},
		{"  full.name  ", "fullname"},
		{"", ""},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := foldCSVName(tc.input); got != tc.want {
				t.Errorf("foldCSVName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// A json:"-" field is not reachable from a CSV column either.
func TestCSVDoesNotFillExcludedFields(t *testing.T) {
	type record struct {
		Public string `json:"public"`
		Secret string `json:"-"`
	}

	index := csvFieldIndex(reflect.TypeOf(record{}))
	if _, present := index["secret"]; present {
		t.Error("an excluded field is reachable from a CSV header")
	}
	if _, present := index["public"]; !present {
		t.Error("a public field is not reachable")
	}
}

// D-05: Strict mode added a sentence to the prompt and then called a validator
// that checked only the top level. A nested structure that came back empty
// reported success.
func TestStrictValidationRecursesIntoNestedStructures(t *testing.T) {
	type address struct {
		City    string `json:"city"`
		Country string `json:"country"`
	}
	type person struct {
		Name string  `json:"name"`
		Home address `json:"home"`
	}

	t.Run("complete_passes", func(t *testing.T) {
		if err := ValidateExtractedData(person{
			Name: "Ada",
			Home: address{City: "London", Country: "UK"},
		}); err != nil {
			t.Errorf("a complete structure must pass: %v", err)
		}
	})

	t.Run("empty_nested_field_fails", func(t *testing.T) {
		err := ValidateExtractedData(person{
			Name: "Ada",
			Home: address{City: "London"}, // Country missing
		})
		if err == nil {
			t.Fatal("an empty nested required field must fail")
		}
		if !strings.Contains(err.Error(), "home.country") {
			t.Errorf("the error should name the path, got %v", err)
		}
	})

	t.Run("empty_top_level_field_fails", func(t *testing.T) {
		if err := ValidateExtractedData(person{Home: address{City: "L", Country: "UK"}}); err == nil {
			t.Error("an empty top-level required field must fail")
		}
	})
}

// Optional fields are optional at every level.
func TestStrictValidationHonoursOmitempty(t *testing.T) {
	type detail struct {
		Required string `json:"required"`
		Optional string `json:"optional,omitempty"`
	}
	type record struct {
		Detail   detail  `json:"detail"`
		Extra    *detail `json:"extra,omitempty"`
		Excluded string  `json:"-"`
	}

	if err := ValidateExtractedData(record{Detail: detail{Required: "present"}}); err != nil {
		t.Errorf("optional fields must not be required: %v", err)
	}
}

// Elements of a slice are validated too, which is where a partially-populated
// list of records used to slip through.
func TestStrictValidationChecksSliceElements(t *testing.T) {
	type line struct {
		SKU      string `json:"sku"`
		Quantity int    `json:"quantity"`
	}
	type order struct {
		Number string `json:"number"`
		Lines  []line `json:"lines"`
	}

	t.Run("complete", func(t *testing.T) {
		if err := ValidateExtractedData(order{
			Number: "ORD-1",
			Lines:  []line{{SKU: "A", Quantity: 1}, {SKU: "B", Quantity: 2}},
		}); err != nil {
			t.Errorf("a complete order must pass: %v", err)
		}
	})

	t.Run("one_incomplete_element", func(t *testing.T) {
		err := ValidateExtractedData(order{
			Number: "ORD-1",
			Lines:  []line{{SKU: "A", Quantity: 1}, {SKU: "B"}}, // Quantity missing
		})
		if err == nil {
			t.Fatal("an incomplete slice element must fail")
		}
		if !strings.Contains(err.Error(), "quantity") {
			t.Errorf("the error should name the field, got %v", err)
		}
	})
}

// The degenerate inputs are handled rather than panicking.
func TestStrictValidationDegenerateInputs(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if err := ValidateExtractedData(nil); err == nil {
			t.Error("nil must be reported")
		}
	})
	t.Run("nil_pointer", func(t *testing.T) {
		var p *csvRecord
		if err := ValidateExtractedData(p); err == nil {
			t.Error("a nil pointer must be reported")
		}
	})
	t.Run("scalar", func(t *testing.T) {
		if err := ValidateExtractedData("a string"); err != nil {
			t.Errorf("a scalar has no required fields: %v", err)
		}
	})
	t.Run("empty_slice", func(t *testing.T) {
		if err := ValidateExtractedData([]csvRecord{}); err != nil {
			t.Errorf("an empty slice has nothing to validate: %v", err)
		}
	})
}

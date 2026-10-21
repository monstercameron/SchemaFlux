package ops

import (
	"strings"
	"testing"
	"time"
)

// OP-307. detectFormat classified any input containing ": " as YAML and any
// input containing "|" as pipe-delimited, so ordinary prose was routed to a
// parser it could not satisfy and failed with an error about the syntax of a
// format it never claimed to be in.
func TestDetectFormatDoesNotRouteProseToAParser(t *testing.T) {
	prose := []string{
		"Dear Alice: please review the attached report before Friday.",
		"Note: the invoice was paid on the 3rd.",
		"Use the staging server | the production one is locked.",
		"The ratio is 3|4 in most cases, according to the report we filed.",
		"Summary: revenue rose. Costs fell. Margins improved.",
		"TODO: ship it",
		"Q: what happened? A: the build broke.",
	}

	for _, input := range prose {
		t.Run(strings.SplitN(input, " ", 3)[0], func(t *testing.T) {
			if got := detectFormat(input, nil); got != "unknown" {
				t.Errorf("detectFormat(%q) = %q; prose is not a data format", input, got)
			}
		})
	}
}

// The formats it should still recognise, including the shapes the substring
// rule got right by accident.
func TestDetectFormatStillRecognisesRealDocuments(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"json object", `{"name":"Ada"}`, "json"},
		{"xml", `<root><name>Ada</name></root>`, "xml"},
		{"csv", "name,age\nAda,36", "csv"},
		{"yaml document", "name: Ada\nage: 36", "yaml"},
		{"yaml list", "- first\n- second", "yaml"},
		{"yaml with a comment", "# people\nname: Ada\nage: 36", "yaml"},
		// A single "key: value" line is deliberately not YAML: "Note: the
		// invoice was paid" has the same shape and is a sentence. Requiring
		// the shape to repeat is what separates a document from prose.
		{"a single pair is not a document", "name: Ada", "unknown"},
		{"pipe record", "Alice|28|Developer", "pipe-delimited"},
		{"pipe table", "name|age\nAlice|28\nBob|31", "pipe-delimited"},
		{"tsv record", "John\t30\tEngineer", "tsv"},
		{"nothing recognisable", "some text", "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectFormat(tc.input, nil); got != tc.want {
				t.Errorf("detectFormat(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// An explicit hint still wins over any of the structural rules, because the
// caller knows what they have.
func TestFormatHintsWinOverDetection(t *testing.T) {
	if got := detectFormat("this is definitely not json", []string{"json"}); got != "json" {
		t.Errorf("detectFormat with a json hint = %q", got)
	}
	if got := detectFormat(`{"a":1}`, []string{"csv"}); got != "csv" {
		t.Errorf("detectFormat with a csv hint = %q", got)
	}
}

// OP-306. reflect.TypeOf on a nil interface returns nil, and Kind() on a nil
// Type panics, so Parse[any] over CSV or delimited text took down the process.
func TestParseIntoAnyReportsRatherThanPanicking(t *testing.T) {
	inputs := []struct {
		name  string
		input string
	}{
		{"csv", "name,age\nAda,36"},
		{"pipe delimited", "Alice|28|Developer"},
		{"tsv", "John\t30\tEngineer"},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse[any] panicked: %v", r)
				}
			}()

			_, err := Parse[any](tc.input, NewParseOptions())
			if err == nil {
				t.Fatal("Parse[any] returned no error; there is no concrete type to map onto")
			}
			if !strings.Contains(err.Error(), "not concrete") {
				t.Errorf("the error should say why: %v", err)
			}
		})
	}
}

// A concrete target is unaffected.
func TestParseIntoAConcreteTypeStillWorks(t *testing.T) {
	type person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	result, err := Parse[[]person]("name,age\nAda,36", NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Data) != 1 || result.Data[0].Name != "Ada" || result.Data[0].Age != 36 {
		t.Errorf("Data = %+v", result.Data)
	}
}

// OP-303. Any type with a String() method took the Stringer branch, and
// time.Time has one -- so a timestamp reached the model as
// "2026-08-07 18:04:05.999 -0400 EDT" while the schema in the same request said
// the format was RFC3339.
func TestNormalizeInputPrefersTheWireForm(t *testing.T) {
	stamp := time.Date(2026, 8, 7, 18, 4, 5, 0, time.UTC)

	got, err := NormalizeInput(stamp)
	if err != nil {
		t.Fatalf("NormalizeInput: %v", err)
	}
	if got != `"2026-08-07T18:04:05Z"` {
		t.Errorf("NormalizeInput(time.Time) = %s, want the RFC3339 form the schema describes", got)
	}
}

// A struct carrying a timestamp has the same problem one level down, which is
// the shape extraction actually sees.
func TestNormalizeInputKeepsNestedTimestampsInTheWireForm(t *testing.T) {
	type event struct {
		Name string    `json:"name"`
		At   time.Time `json:"at"`
	}

	got, err := NormalizeInput(event{Name: "deploy", At: time.Date(2026, 8, 7, 18, 4, 5, 0, time.UTC)})
	if err != nil {
		t.Fatalf("NormalizeInput: %v", err)
	}
	if !strings.Contains(got, `"at":"2026-08-07T18:04:05Z"`) {
		t.Errorf("NormalizeInput = %s", got)
	}
}

// A type whose String() is its intended form still gets it, because JSON cannot
// render it -- Stringer is the fallback, not the first choice.
type unmarshalableStringer struct {
	// Exported on purpose: encoding/json skips unexported fields, so a struct
	// whose only channel is unexported marshals cleanly to "{}" and never
	// reaches the fallback this test is about.
	Ch chan int
}

func (u unmarshalableStringer) String() string { return "the stringer form" }

func TestNormalizeInputFallsBackToStringer(t *testing.T) {
	got, err := NormalizeInput(unmarshalableStringer{Ch: make(chan int)})
	if err != nil {
		t.Fatalf("NormalizeInput: %v", err)
	}
	if got != "the stringer form" {
		t.Errorf("NormalizeInput = %q, want the Stringer output", got)
	}
}

// Strings and byte slices are passed through untouched, because quoting a
// document would change what the model is being asked about.
func TestNormalizeInputPassesTextThrough(t *testing.T) {
	if got, _ := NormalizeInput("plain text"); got != "plain text" {
		t.Errorf("NormalizeInput(string) = %q", got)
	}
	if got, _ := NormalizeInput([]byte("bytes")); got != "bytes" {
		t.Errorf("NormalizeInput([]byte) = %q", got)
	}
	if _, err := NormalizeInput(nil); err == nil {
		t.Error("NormalizeInput(nil) returned no error")
	}
}

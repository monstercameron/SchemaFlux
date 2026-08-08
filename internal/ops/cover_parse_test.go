package ops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// This file raises coverage on parse.go's under-covered paths: the
// normalization/mapping helpers below parseImpl, the LLM-fallback branch,
// and the small string helpers at the bottom of the file. It reuses the
// package's own scripted-caller pattern (setLLMCaller / installNormalizeCaller)
// so nothing here makes a network call.

// normalizeParseInput's default branch: a type json.Marshal can encode
// (e.g. a plain int) is marshaled; a type it cannot (a bare func value) falls
// back to fmt.Sprintf rather than erroring -- normalizeParseInput itself
// never returns an error today, so the contract under test is "it produces
// some string, not a panic or an error", which is what the fallback exists
// for.
func TestNormalizeParseInput_Fallback(t *testing.T) {
	type wrapper struct {
		Name string `json:"name"`
	}

	t.Run("marshalable non-string type", func(t *testing.T) {
		result, err := Parse[wrapper](wrapper{Name: "ok"}, NewParseOptions())
		if err != nil {
			t.Fatalf("Parse(struct input) = %v", err)
		}
		if result.Data.Name != "ok" {
			t.Errorf("Data.Name = %q, want %q", result.Data.Name, "ok")
		}
	})

	t.Run("unmarshalable type falls back to Sprintf rather than panicking", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Parse panicked on an unmarshalable input: %v", r)
			}
		}()
		// A bare func cannot be JSON-marshaled; normalizeParseInput must still
		// produce a string (via fmt.Sprintf) for detectFormat/parseWithAlgorithm
		// to fail on cleanly, rather than propagating a marshal error.
		_, err := Parse[wrapper](func() {}, NewParseOptions())
		if err == nil {
			t.Fatal("Parse(func) succeeded; a function value describes no data")
		}
	})
}

// parseImpl's custom-delimited fallback: the format detector gives up
// ("unknown") but a CustomDelimiter is supplied, so parseImpl must try
// parseDelimited before giving up, and report the format as
// "custom-delimited" when it works.
func TestParseImpl_FallsBackToCustomDelimitedOnUnknownFormat(t *testing.T) {
	type row struct {
		A string
		B string
	}

	// A single short field-triple would normally be sniffed as pipe-delimited;
	// using a delimiter detectFormat does not know about keeps format "unknown"
	// so the custom-delimiter fallback path is what has to carry this.
	input := "alpha~beta"
	opts := NewParseOptions().WithCustomDelimiters([]string{"~"})

	result, err := Parse[row](input, opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.Format != "custom-delimited" {
		t.Errorf("Format = %q, want %q", result.Format, "custom-delimited")
	}
	if result.Data.A != "alpha" || result.Data.B != "beta" {
		t.Errorf("Data = %+v", result.Data)
	}
}

// parseImpl without any fallback available reports the underlying algorithm
// error and names AllowLLMFallback as the way out -- the refusal case for an
// unparseable body with no escape hatch configured.
func TestParseImpl_NoFallbackConfiguredReportsAlgorithmError(t *testing.T) {
	type person struct{ Name string }

	_, err := Parse[person]("not any recognisable format at all here", NewParseOptions())
	if err == nil {
		t.Fatal("Parse succeeded on unparseable input with no fallback configured")
	}
	if !strings.Contains(err.Error(), "AllowLLMFallback") {
		t.Errorf("error does not point at the escape hatch: %v", err)
	}
}

// parseImpl's LLM-fallback branch, success case: algorithmic parsing fails,
// AllowLLMFallback is set, and the scripted provider's JSON answer is
// decoded into the target type.
func TestParseImpl_LLMFallbackSucceeds(t *testing.T) {
	type person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"name":"Ada","age":36}`, nil
	})

	opts := NewParseOptions().WithAllowLLMFallback(true).WithAutoFix(true)
	result, err := Parse[person]("Ada is 36 years old, a programmer", opts)
	if err != nil {
		t.Fatalf("Parse with LLM fallback: %v", err)
	}
	if result.Data.Name != "Ada" || result.Data.Age != 36 {
		t.Errorf("Data = %+v", result.Data)
	}
	if !strings.Contains(result.Format, "LLM-assisted") {
		t.Errorf("Format = %q, want it to say LLM-assisted", result.Format)
	}
}

// The LLM-fallback branch's two failure modes: the provider call itself
// failing, and the provider answering with something that does not decode
// into the target type. Both must surface as errors, not a zero-valued
// success.
func TestParseImpl_LLMFallbackFailureModes(t *testing.T) {
	type person struct {
		Name string `json:"name"`
	}

	t.Run("provider call fails", func(t *testing.T) {
		installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
			return "", errors.New("provider unavailable")
		})
		_, err := Parse[person]("unparseable text here", NewParseOptions().WithAllowLLMFallback(true))
		if err == nil {
			t.Fatal("Parse succeeded despite the provider call failing")
		}
	})

	t.Run("provider answers with unparseable JSON", func(t *testing.T) {
		installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
			return "not json at all", nil
		})
		_, err := Parse[person]("unparseable text here", NewParseOptions().WithAllowLLMFallback(true))
		if err == nil {
			t.Fatal("Parse succeeded despite the provider's answer not decoding into the target type")
		}
	})
}

// looksLikeDelimited's single-row rejection rule: a field that is too long,
// or has too many words, reads as a prose fragment rather than a record
// field, and the whole line is rejected as delimited even though it contains
// the separator.
func TestLooksLikeDelimited_RejectsProseShapedSingleRow(t *testing.T) {
	prose := "Use the staging server | the production one is locked because of an incident"
	if got := detectFormat(prose, nil); got == "pipe-delimited" {
		t.Errorf("detectFormat(%q) = pipe-delimited; a prose fragment with a bar is not a record", prose)
	}
}

// The multi-row rejection rule: rows whose field counts disagree are not a
// table.
func TestLooksLikeDelimited_RejectsMismatchedRowShapes(t *testing.T) {
	input := "a|b|c\nd|e"
	if got := detectFormat(input, nil); got == "pipe-delimited" {
		t.Errorf("detectFormat(%q) = pipe-delimited; row field counts disagree", input)
	}
}

// parseCSV's error paths: malformed CSV, too few rows, and headers that map
// to no field at all -- the refusal requireMappableHeaders exists for.
func TestParseCSV_Refusals(t *testing.T) {
	type person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	t.Run("malformed CSV quoting", func(t *testing.T) {
		_, err := Parse[[]person]("name,age\n\"unterminated,30", NewParseOptions())
		if err == nil {
			t.Fatal("malformed CSV was accepted")
		}
	})

	t.Run("only a header row, no data", func(t *testing.T) {
		_, err := Parse[[]person]("name,age", NewParseOptions())
		if err == nil {
			t.Fatal("a header-only CSV was accepted as having at least one data row")
		}
	})

	t.Run("headers map to no field on the target type", func(t *testing.T) {
		_, err := Parse[[]person]("unrelated_col1,unrelated_col2\nx,y", NewParseOptions())
		if err == nil {
			t.Fatal("headers matching no field were accepted silently")
		}
		if !strings.Contains(err.Error(), "no CSV column maps to a field") {
			t.Errorf("error does not explain the mismatch: %v", err)
		}
	})

	t.Run("single-struct target with mismatched headers also refuses", func(t *testing.T) {
		_, err := Parse[person]("unrelated_col1,unrelated_col2\nx,y", NewParseOptions())
		if err == nil {
			t.Fatal("single-struct CSV target with unmapped headers was accepted")
		}
	})
}

// parseCSV's single-struct (non-slice) success path, using the json tag
// rather than the Go field name -- exercises csvFieldIndex's tag lookup and
// mapCSVRowToStruct's "mapped at least one column" success path together.
func TestParseCSV_SingleStructUsesJSONTag(t *testing.T) {
	type person struct {
		FullName string `json:"full_name"`
		Role     string `json:"role"`
	}

	// detectFormat's CSV sniff requires a comma on top of the newline, so a
	// single-column body would not even reach parseCSV.
	result, err := Parse[person]("full_name,role\nAda Lovelace,mathematician", NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.Data.FullName != "Ada Lovelace" || result.Data.Role != "mathematician" {
		t.Errorf("Data = %+v", result.Data)
	}
}

// mapDelimitedFieldsToStruct's hinted path (mapWithHints), including the
// "hint present but names no valid field" tolerance and the "no pipe-shaped
// hint at all" refusal.
func TestMapWithHints(t *testing.T) {
	type person struct {
		Name string
		Age  int
	}

	t.Run("valid hint maps fields", func(t *testing.T) {
		result, err := Parse[person]("Ada|36", NewParseOptions().WithFormatHints([]string{"name|age"}))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if result.Data.Name != "Ada" || result.Data.Age != 36 {
			t.Errorf("Data = %+v", result.Data)
		}
	})

	t.Run("hint names a field the struct does not have", func(t *testing.T) {
		// "nickname" has no counterpart field; mapWithHints must skip it rather
		// than error, and still map the fields that do exist.
		result, err := Parse[person]("Ada|36", NewParseOptions().WithFormatHints([]string{"nickname|age"}))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if result.Data.Age != 36 {
			t.Errorf("Age = %d, want 36 even though the first hinted field did not match", result.Data.Age)
		}
	})

	t.Run("no pipe-shaped hint at all refuses", func(t *testing.T) {
		_, err := Parse[person]("Ada|36", NewParseOptions().WithFormatHints([]string{"not a field mapping"}))
		if err == nil {
			t.Fatal("a hint with no '|' in it should not satisfy mapWithHints")
		}
	})
}

// setFieldValue's numeric/bool error paths: a value that does not parse as
// the field's type is refused rather than silently zeroed, and the
// unsupported-kind branch is refused too.
func TestSetFieldValue_Refusals(t *testing.T) {
	type numeric struct {
		Count uint
		Ratio float64
		Flag  bool
	}

	t.Run("bad uint", func(t *testing.T) {
		_, err := Parse[numeric]("Count,Ratio,Flag\nnot-a-number,1.5,true", NewParseOptions())
		if err == nil {
			t.Fatal("a non-numeric uint field was accepted")
		}
	})

	t.Run("bad float", func(t *testing.T) {
		_, err := Parse[numeric]("Count,Ratio,Flag\n1,not-a-float,true", NewParseOptions())
		if err == nil {
			t.Fatal("a non-numeric float field was accepted")
		}
	})

	t.Run("bad bool", func(t *testing.T) {
		_, err := Parse[numeric]("Count,Ratio,Flag\n1,1.5,maybe", NewParseOptions())
		if err == nil {
			t.Fatal("a non-boolean bool field was accepted")
		}
	})

	t.Run("unsupported field kind", func(t *testing.T) {
		type unsupported struct {
			Data []string
		}
		_, err := Parse[unsupported]("Data\nsomething", NewParseOptions())
		if err == nil {
			t.Fatal("a slice-kind field was silently accepted by setFieldValue")
		}
	})
}

// capitalizeFirst's empty-string branch.
func TestCapitalizeFirst_Empty(t *testing.T) {
	if got := capitalizeFirst(""); got != "" {
		t.Errorf("capitalizeFirst(%q) = %q, want empty", "", got)
	}
}

// describeParseTarget names a concrete type, and does not crash on the
// pathological any/interface case Parse[any] already refuses upstream.
func TestDescribeParseTarget(t *testing.T) {
	if got := describeParseTarget[struct{ Name string }](); got == "" {
		t.Error("describeParseTarget[struct] returned empty")
	}
	if got := describeParseTarget[[]int](); !strings.Contains(got, "int") {
		t.Errorf("describeParseTarget[[]int] = %q, want it to mention int", got)
	}
}

// buildParseSystemPrompt's branches by detected format, and the AutoFix
// addendum -- asserted on properties (which instruction is present), never
// on the literal prompt string, per this package's own convention.
func TestBuildParseSystemPrompt_Branches(t *testing.T) {
	cases := []struct {
		name   string
		format string
		want   string
	}{
		{"unknown format", "unknown", "custom or unknown format"},
		{"pipe-delimited", "pipe-delimited", "delimited data"},
		{"tsv", "tsv", "delimited data"},
		{"known format", "json", "Parse the json data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildParseSystemPrompt(tc.format, NewParseOptions())
			if !strings.Contains(got, tc.want) {
				t.Errorf("buildParseSystemPrompt(%q) does not contain %q: %q", tc.format, tc.want, got)
			}
		})
	}

	withAutoFix := buildParseSystemPrompt("json", NewParseOptions().WithAutoFix(true))
	if !strings.Contains(withAutoFix, "fix malformed") {
		t.Errorf("AutoFix instruction missing: %q", withAutoFix)
	}
}

// buildParseUserPrompt includes format hints and custom delimiters when the
// caller supplied them, and omits them when not -- both branches.
func TestBuildParseUserPrompt_IncludesHintsAndDelimitersWhenSet(t *testing.T) {
	opts := NewParseOptions().
		WithFormatHints([]string{"name|age"}).
		WithCustomDelimiters([]string{"|", ";"})

	got := buildParseUserPrompt("Ada|36", "schema", "pipe-delimited", opts)
	if !strings.Contains(got, "Format hints: name|age") {
		t.Errorf("format hints missing from prompt: %q", got)
	}
	if !strings.Contains(got, "Custom delimiters: |, ;") {
		t.Errorf("custom delimiters missing from prompt: %q", got)
	}

	bare := buildParseUserPrompt("Ada|36", "schema", "pipe-delimited", NewParseOptions())
	if strings.Contains(bare, "Format hints:") || strings.Contains(bare, "Custom delimiters:") {
		t.Errorf("prompt mentions hints/delimiters that were never set: %q", bare)
	}
}

// detectFormat's hint switch has a case for every format name; "json" is
// exercised elsewhere, this covers the rest.
func TestDetectFormat_AllHintBranches(t *testing.T) {
	cases := []struct{ hint, want string }{
		{"xml", "xml"},
		{"csv", "csv"},
		{"yaml", "yaml"},
		{"yml", "yaml"},
	}
	for _, tc := range cases {
		if got := detectFormat("this is not actually that format", []string{tc.hint}); got != tc.want {
			t.Errorf("detectFormat with hint %q = %q, want %q", tc.hint, got, tc.want)
		}
	}
}

// parseCSV's "at least a header and one data row" refusal, reached by
// forcing the CSV algorithm via a hint on a body detectFormat's own sniffing
// would never call CSV (no newline at all).
func TestParseCSV_HeaderOnlyRefuses(t *testing.T) {
	type person struct{ Name string }
	_, err := Parse[person]("Name", NewParseOptions().WithFormatHints([]string{"csv"}))
	if err == nil {
		t.Fatal("a header-only CSV body was accepted as having a data row")
	}
	if !strings.Contains(err.Error(), "at least header and one data row") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

// The slice-of-struct CSV path propagates a per-row setFieldValue failure
// rather than silently keeping the zero value for that row.
func TestParseCSV_SliceTargetRowErrorPropagates(t *testing.T) {
	type person struct {
		Name string
		Age  int
	}
	input := "Name,Age\nAda,36\nBabbage,not-a-number"
	_, err := Parse[[]person](input, NewParseOptions())
	if err == nil {
		t.Fatal("a bad value in the second data row was accepted")
	}
	if !strings.Contains(err.Error(), "failed to set field Age") {
		t.Errorf("error does not name the field: %v", err)
	}
}

// requireMappableHeaders builds its "columns it accepts" list by walking the
// struct's fields, and has to skip an unexported field, skip a field tagged
// json:"-", and fall back to the Go field name when there is no tag at all --
// three branches a single all-tagged-and-exported struct never reaches.
func TestRequireMappableHeaders_FieldListBranches(t *testing.T) {
	type target struct {
		unexported string //nolint:unused // exercises the IsExported() skip
		Hidden     string `json:"-"`
		Tagged     string `json:"tagged_name"`
		Untagged   string
	}
	_ = target{}.unexported

	err := requireMappableHeaders([]string{"does_not_match_anything"}, reflect.TypeOf(target{}))
	if err == nil {
		t.Fatal("headers matching nothing were accepted")
	}
	msg := err.Error()
	if strings.Contains(msg, "Hidden") {
		t.Errorf("a json:\"-\" field was listed as an accepted column: %v", err)
	}
	if !strings.Contains(msg, "tagged_name") {
		t.Errorf("the tagged field's json name is missing from the accepted list: %v", err)
	}
	if !strings.Contains(msg, "Untagged") {
		t.Errorf("the untagged field's Go name is missing from the accepted list: %v", err)
	}
}

// csvFieldIndex's empty-key guard: a json tag that folds down to nothing
// (all whitespace) must not register a lookup entry, or every such field
// would collide on the empty string.
func TestCSVFieldIndex_BlankTagIsNotIndexed(t *testing.T) {
	type target struct {
		A string `json:"   "`
	}
	index := csvFieldIndex(reflect.TypeOf(target{}))
	if _, found := index[""]; found {
		t.Error("a blank json tag registered an empty-string index entry")
	}
	// The Go field name is still reachable.
	if _, found := index["a"]; !found {
		t.Error("the field's Go name should still be indexed even though its tag folded to empty")
	}
}

// The slice-of-struct delimited path propagates a per-line mapping failure,
// mirroring the CSV case above.
func TestParseDelimited_SliceTargetRowErrorPropagates(t *testing.T) {
	type row struct {
		Name string
		Age  int
	}
	input := "Ada|36\nBabbage|not-a-number"
	_, err := Parse[[]row](input, NewParseOptions().WithCustomDelimiters([]string{"|"}))
	if err == nil {
		t.Fatal("a bad value on the second line was accepted")
	}
}

// setFieldValue's empty-value branch: a blank column keeps the field at its
// zero value rather than erroring, which is what lets a partially-filled CSV
// row parse at all.
func TestSetFieldValue_EmptyValueKeepsZero(t *testing.T) {
	type person struct {
		Name string
		Age  int
	}
	result, err := Parse[person]("Name,Age\nAda,", NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.Data.Age != 0 {
		t.Errorf("Age = %d, want the zero value for a blank column", result.Data.Age)
	}
	if result.Data.Name != "Ada" {
		t.Errorf("Name = %q", result.Data.Name)
	}
}

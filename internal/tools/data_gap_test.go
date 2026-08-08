package tools

import (
	"context"
	"strings"
	"testing"
)

// The parse and format actions each refuse to run without the input they
// need, rather than panicking on a missing or wrongly-typed parameter.
func TestCSVToolRefusesMissingInput(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"parse without data", map[string]any{"action": "parse"}},
		{"parse with empty data", map[string]any{"action": "parse", "data": ""}},
		{"format without rows", map[string]any{"action": "format"}},
		{"format with empty rows", map[string]any{"action": "format", "rows": []any{}}},
		{"unknown action", map[string]any{"action": "transpose"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := CSVTool.Execute(context.Background(), tc.params)
			if result.Success {
				t.Errorf("%s: expected failure, got success", tc.name)
			}
		})
	}
}

func TestCSVToolParseMalformed(t *testing.T) {
	result, _ := CSVTool.Execute(context.Background(), map[string]any{
		"action": "parse",
		"data":   "a,\"unterminated quote\nb,c",
	})
	if result.Success {
		t.Error("a CSV record with an unterminated quote must fail to parse")
	}
}

func TestCSVToolParseWhitespaceOnlyIsEmpty(t *testing.T) {
	// A single blank line contains no records at all, once the CSV reader
	// skips it -- distinct from the "data is required" refusal, which is
	// about an empty string, not an empty result.
	result, _ := CSVTool.Execute(context.Background(), map[string]any{
		"action": "parse",
		"data":   "\n",
	})
	if !result.Success {
		t.Fatalf("expected success for a blank line: %s", result.Error)
	}
	rows, ok := result.Data.([][]string)
	if !ok || len(rows) != 0 {
		t.Errorf("expected an empty result for a blank line, got %#v", result.Data)
	}
}

// Without explicit headers, format derives them from the first row's own
// keys, sorted so the output is reproducible across runs.
func TestCSVToolFormatDerivesHeadersFromFirstRow(t *testing.T) {
	rows := []any{
		map[string]any{"name": "Alice", "age": 30},
	}
	result, _ := CSVTool.Execute(context.Background(), map[string]any{
		"action": "format",
		"rows":   rows,
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	csv := result.Data.(string)
	if !strings.Contains(csv, "age,name") {
		t.Errorf("expected sorted derived headers, got %q", csv)
	}
}

func TestJSONToolRefusesMissingInput(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"parse without data", map[string]any{"action": "parse"}},
		{"format without object", map[string]any{"action": "format"}},
		{"extract without data", map[string]any{"action": "extract", "path": "x"}},
		{"extract without path", map[string]any{"action": "extract", "data": `{"x":1}`}},
		{"extract with unparsable data", map[string]any{"action": "extract", "data": "not json", "path": "x"}},
		{"unknown action", map[string]any{"action": "transmute"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := JSONTool.Execute(context.Background(), tc.params)
			if result.Success {
				t.Errorf("%s: expected failure, got success", tc.name)
			}
		})
	}
}

// A value json.Marshal cannot encode -- a function -- must be reported as a
// format error, not silently dropped or a panic three layers up.
func TestJSONToolFormatRefusesAnUnencodableValue(t *testing.T) {
	result, _ := JSONTool.Execute(context.Background(), map[string]any{
		"action": "format",
		"object": map[string]any{"handler": func() {}},
	})
	if result.Success {
		t.Error("json.Marshal cannot encode a func value; the tool must report the failure")
	}
}

func TestJSONToolFormatCompact(t *testing.T) {
	result, _ := JSONTool.Execute(context.Background(), map[string]any{
		"action": "format",
		"object": map[string]any{"name": "Alice"},
		"pretty": false,
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if strings.Contains(result.Data.(string), "\n") {
		t.Error("pretty=false must not indent the output")
	}
}

func TestExtractJSONPathNavigationErrors(t *testing.T) {
	obj := map[string]any{
		"count": 2,
		"users": []any{"Alice", "Bob"},
	}

	cases := []struct {
		name string
		path string
	}{
		{"non-numeric array index", "users.first"},
		{"navigating into a scalar", "count.nested"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := extractJSONPath(obj, tc.path); err == nil {
				t.Errorf("%s: expected an error, got none", tc.name)
			}
		})
	}
}

func TestXMLToolRefusesMissingInput(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"parse without data", map[string]any{"action": "parse"}},
		{"parse with malformed xml", map[string]any{"action": "parse", "data": "<root><unclosed></root>"}},
		{"format without object", map[string]any{"action": "format"}},
		{"unknown action", map[string]any{"action": "transcode"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := XMLTool.Execute(context.Background(), tc.params)
			if result.Success {
				t.Errorf("%s: expected failure, got success", tc.name)
			}
		})
	}
}

// Repeated sibling elements collapse into a slice, so a caller sees the same
// shape a JSON array would have produced from equivalent data.
func TestXMLToolParseRepeatedElementsBecomeAnArray(t *testing.T) {
	data := `<root><item>a</item><item>b</item><item>c</item></root>`
	result, _ := XMLTool.Execute(context.Background(), map[string]any{
		"action": "parse",
		"data":   data,
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	obj := result.Data.(map[string]any)
	root := obj["root"].(map[string]any)
	items, ok := root["item"].([]any)
	if !ok || len(items) != 3 {
		t.Errorf("expected repeated <item> elements to collapse into a 3-element array, got %#v", root["item"])
	}
}

// A default root name applies when the caller does not name one, and an
// array-valued field repeats the element rather than nesting an array node.
func TestXMLToolFormatDefaultRootAndArrayValues(t *testing.T) {
	result, _ := XMLTool.Execute(context.Background(), map[string]any{
		"action": "format",
		"object": map[string]any{"tag": []any{"a", "b"}},
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	xml := result.Data.(string)
	if !strings.Contains(xml, "<root>") {
		t.Errorf("expected the default root name, got %q", xml)
	}
	if strings.Count(xml, "<tag>") != 2 {
		t.Errorf("expected the array to repeat the element twice, got %q", xml)
	}
}

func TestTableToolRefusesMissingData(t *testing.T) {
	cases := []map[string]any{
		{},
		{"data": []any{}},
	}
	for _, params := range cases {
		result, _ := TableTool.Execute(context.Background(), params)
		if result.Success {
			t.Errorf("%v: expected failure, got success", params)
		}
	}
}

// Without explicit headers, the table derives them from the first row's own
// keys, same as CSV format does.
func TestTableToolDerivesHeadersFromFirstRow(t *testing.T) {
	data := []any{map[string]any{"name": "Alice", "age": 30}}
	result, _ := TableTool.Execute(context.Background(), map[string]any{"data": data})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	table := result.Data.(string)
	if !strings.Contains(table, "age") || !strings.Contains(table, "name") {
		t.Errorf("expected derived headers in output, got %q", table)
	}
}

// A row given as a bare array rather than a map, is rendered positionally
// against the given headers.
func TestTableToolArrayRows(t *testing.T) {
	data := []any{[]any{"Alice", 30}}
	result, _ := TableTool.Execute(context.Background(), map[string]any{
		"data":    data,
		"headers": []any{"name", "age"},
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	table := result.Data.(string)
	if !strings.Contains(table, "Alice") || !strings.Contains(table, "30") {
		t.Errorf("expected the array row's values in output, got %q", table)
	}
}

func TestDiffToolRefusesMissingInput(t *testing.T) {
	cases := []map[string]any{
		{"left": nil, "right": map[string]any{}},
		{"left": map[string]any{}, "right": nil},
		{},
	}
	for _, params := range cases {
		result, _ := DiffTool.Execute(context.Background(), params)
		if result.Success {
			t.Errorf("%v: expected failure, got success", params)
		}
	}
}

func TestDiffJSONBothNilIsNoDifference(t *testing.T) {
	diffs := diffJSON(nil, nil, "")
	if len(diffs) != 0 {
		t.Errorf("two nils should produce no differences, got %#v", diffs)
	}
}

func TestDiffJSONTypeMismatch(t *testing.T) {
	diffs := diffJSON("a string", 42, "field")
	if len(diffs) != 1 || diffs[0]["type"] != "type_mismatch" {
		t.Errorf("expected a single type_mismatch difference, got %#v", diffs)
	}
}

// A path already carries a prefix when diffing recurses into a nested
// object; the child path must extend it with a dot, not replace it.
func TestDiffJSONNestedPathIsDotted(t *testing.T) {
	left := map[string]any{"inner": map[string]any{"value": 1}}
	right := map[string]any{"inner": map[string]any{"value": 2}}

	diffs := diffJSON(left, right, "")
	if len(diffs) != 1 {
		t.Fatalf("expected 1 difference, got %#v", diffs)
	}
	if diffs[0]["path"] != "inner.value" {
		t.Errorf("path = %v, want inner.value", diffs[0]["path"])
	}
}

// Arrays of different lengths report the extra elements as added or
// removed, keyed by their bracketed index, rather than only comparing the
// overlapping prefix.
func TestDiffJSONArrayLengthMismatch(t *testing.T) {
	t.Run("right longer reports added", func(t *testing.T) {
		diffs := diffJSON([]any{"a"}, []any{"a", "b"}, "")
		found := false
		for _, d := range diffs {
			if d["type"] == "added" && d["path"] == "[1]" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected an 'added' diff at [1], got %#v", diffs)
		}
	})

	t.Run("left longer reports removed", func(t *testing.T) {
		diffs := diffJSON([]any{"a", "b"}, []any{"a"}, "")
		found := false
		for _, d := range diffs {
			if d["type"] == "removed" && d["path"] == "[1]" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a 'removed' diff at [1], got %#v", diffs)
		}
	})
}

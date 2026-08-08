package tools

import (
	"context"
	"testing"
)

// The regex tool refuses a call with no pattern, and each documented action
// is wired through -- find, replace, and split are only exercised via the
// package-level helpers elsewhere, never through the tool itself.
func TestRegexToolAllActionsAndRefusals(t *testing.T) {
	t.Run("missing pattern", func(t *testing.T) {
		result, _ := RegexTool.Execute(context.Background(), map[string]any{
			"action": "match",
			"text":   "abc",
		})
		if result.Success {
			t.Error("a missing pattern must be refused")
		}
	})

	t.Run("find", func(t *testing.T) {
		result, _ := RegexTool.Execute(context.Background(), map[string]any{
			"action":  "find",
			"pattern": `\d+`,
			"text":    "a1b22c333",
		})
		if !result.Success || result.Data != "1" {
			t.Errorf("find = %+v", result)
		}
	})

	t.Run("replace", func(t *testing.T) {
		result, _ := RegexTool.Execute(context.Background(), map[string]any{
			"action":  "replace",
			"pattern": `\d+`,
			"text":    "a1b2",
			"replace": "#",
		})
		if !result.Success || result.Data != "a#b#" {
			t.Errorf("replace = %+v", result)
		}
	})

	t.Run("split", func(t *testing.T) {
		result, _ := RegexTool.Execute(context.Background(), map[string]any{
			"action":  "split",
			"pattern": `,`,
			"text":    "a,b,c",
		})
		if !result.Success {
			t.Fatalf("split: %s", result.Error)
		}
		parts := result.Data.([]string)
		if len(parts) != 3 {
			t.Errorf("split produced %d parts, want 3", len(parts))
		}
	})

	t.Run("unknown action", func(t *testing.T) {
		result, _ := RegexTool.Execute(context.Background(), map[string]any{
			"action":  "transform",
			"pattern": `\d+`,
			"text":    "a1",
		})
		if result.Success {
			t.Error("an unrecognised regex action must be refused")
		}
	})
}

// Every helper that compiles its own pattern must surface a compile error
// rather than panicking on a malformed one.
func TestRegexHelpersRefuseAnInvalidPattern(t *testing.T) {
	bad := `[unclosed`

	if _, err := RegexMatch(bad, "x"); err == nil {
		t.Error("RegexMatch accepted an invalid pattern")
	}
	if _, err := RegexFind(bad, "x"); err == nil {
		t.Error("RegexFind accepted an invalid pattern")
	}
	if _, err := RegexFindAll(bad, "x"); err == nil {
		t.Error("RegexFindAll accepted an invalid pattern")
	}
	if _, err := RegexReplace(bad, "x", "y"); err == nil {
		t.Error("RegexReplace accepted an invalid pattern")
	}
	if _, err := RegexExtract(bad, "x"); err == nil {
		t.Error("RegexExtract accepted an invalid pattern")
	}
}

// RegexExtract with a pattern that has no named groups still returns the
// numbered groups, and a pattern with no match at all returns nil without
// an error -- a caller should not have to distinguish "no groups" from "no
// match" through an error path.
func TestRegexExtractNumberedGroupsAndNoMatch(t *testing.T) {
	result, err := RegexExtract(`(\d+)-(\d+)`, "range 10-20")
	if err != nil {
		t.Fatalf("RegexExtract: %v", err)
	}
	if result["1"] != "10" || result["2"] != "20" {
		t.Errorf("numbered groups = %+v", result)
	}

	result, err = RegexExtract(`\d+`, "no digits here")
	if err != nil {
		t.Fatalf("RegexExtract: %v", err)
	}
	if result != nil {
		t.Errorf("no match should return nil, got %+v", result)
	}
}

// The convert tool accepts an integer value, not just a float64 -- JSON
// numbers decode as float64 in this library's own params, but a caller
// building params programmatically may hand it a Go int.
func TestConvertToolAcceptsIntegerValue(t *testing.T) {
	result, _ := ConvertTool.Execute(context.Background(), map[string]any{
		"value": 5,
		"from":  "kg",
		"to":    "g",
	})
	if !result.Success {
		t.Fatalf("Execute: %s", result.Error)
	}
	if result.Data.(float64) != 5000 {
		t.Errorf("5kg in g = %v, want 5000", result.Data)
	}
}

// A non-numeric value is refused outright.
func TestConvertToolRefusesNonNumericValue(t *testing.T) {
	result, _ := ConvertTool.Execute(context.Background(), map[string]any{
		"value": "not a number",
		"from":  "kg",
		"to":    "g",
	})
	if result.Success {
		t.Error("a non-numeric value must be refused")
	}
}

// convertTemperature is only reached through tryConvert's isTemperature
// guard, which limits "from" and "to" to c/f/k and their long names -- so
// its own default branches (an unrecognised unit) are unreachable from the
// public API. They are still worth a direct test: a change to the guard
// that let something else through must not fall back to a silent zero.
func TestConvertTemperatureRefusesUnrecognisedUnitsDirectly(t *testing.T) {
	if _, err := convertTemperature(0, "rankine", "c"); err == nil {
		t.Error("an unrecognised source unit must be refused")
	}
	if _, err := convertTemperature(0, "c", "rankine"); err == nil {
		t.Error("an unrecognised target unit must be refused")
	}
}

// Every long-form temperature name normalises to the same result as its
// abbreviation.
func TestConvertTemperatureLongFormNames(t *testing.T) {
	result, err := convertTemperature(100, "fahrenheit", "kelvin")
	if err != nil {
		t.Fatalf("convertTemperature: %v", err)
	}
	want, _ := convertTemperature(100, "f", "k")
	if result != want {
		t.Errorf("fahrenheit->kelvin = %v, want %v (matching f->k)", result, want)
	}
}

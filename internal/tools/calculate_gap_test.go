package tools

import (
	"math"
	"strings"
	"testing"
)

// evalNode handles every node shape the arithmetic grammar can produce, and
// refuses anything outside it -- a comparison, a function call, a unary
// operator that isn't +/-. These are unreachable through Calculate's normal
// arithmetic inputs, so they are worth naming directly.
func TestEvalNodeRefusesUnsupportedShapes(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"comparison operator", "2 < 3"},
		{"logical operator", "1 && 0"},
		{"unary not", "!5"},
		{"function call", "foo(1)"},
		{"bitwise operator", "5 & 3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := evalExpr(tc.expr); err == nil {
				t.Errorf("evalExpr(%q) should have been refused as unsupported", tc.expr)
			}
		})
	}
}

// Unary plus is accepted and is a no-op, same as it is in ordinary
// arithmetic notation.
func TestEvalNodeUnaryPlus(t *testing.T) {
	result, err := evalExpr("+5")
	if err != nil {
		t.Fatalf("evalExpr(\"+5\"): %v", err)
	}
	if result != 5 {
		t.Errorf("+5 = %v, want 5", result)
	}
}

// replaceFunctions leaves a function call untouched, rather than crashing,
// when the argument itself does not evaluate -- the surrounding Calculate
// call then fails at the parse stage with a clear error instead of a panic
// deep in the function-substitution loop.
func TestCalculateFailsCleanlyOnAnUnevaluableFunctionArgument(t *testing.T) {
	if _, err := Calculate("sqrt(2+)"); err == nil {
		t.Error("sqrt with a malformed argument should fail, not silently pass the raw text through")
	}
	if _, err := Calculate("pow(2+, 3)"); err == nil {
		t.Error("pow with a malformed first argument should fail")
	}
	if _, err := Calculate("pow(2, 3+)"); err == nil {
		t.Error("pow with a malformed second argument should fail")
	}
}

// The constant substitutions apply outside of function calls too, so a bare
// "pi" or "e" in an expression resolves to the constant.
func TestCalculateConstants(t *testing.T) {
	result, err := Calculate("pi")
	if err != nil {
		t.Fatalf("Calculate(\"pi\"): %v", err)
	}
	if math.Abs(result-math.Pi) > 0.0001 {
		t.Errorf("pi = %v, want %v", result, math.Pi)
	}
}

// Trig and log functions are wired the same way sqrt/abs/floor/ceil/round
// are; each is worth a data point so a broken entry in the function table
// does not go unnoticed.
func TestCalculateTrigAndLogFunctions(t *testing.T) {
	cases := []struct {
		expr     string
		expected float64
	}{
		{"sin(0)", 0},
		{"cos(0)", 1},
		{"tan(0)", 0},
		{"log(1)", 0},
		{"log10(100)", 2},
		{"exp(0)", 1},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			result, err := Calculate(tc.expr)
			if err != nil {
				t.Fatalf("Calculate(%q): %v", tc.expr, err)
			}
			if math.Abs(result-tc.expected) > 0.0001 {
				t.Errorf("Calculate(%q) = %v, want %v", tc.expr, result, tc.expected)
			}
		})
	}
}

// A trailing zero divisor by way of an expression (not a literal "x / 0")
// still refuses division by zero.
func TestEvalNodeDivisionByZeroThroughAnExpression(t *testing.T) {
	if _, err := evalExpr("(2 - 2) / (5 - 5)"); err == nil {
		t.Error("division by an expression that evaluates to zero must fail")
	} else if !strings.Contains(err.Error(), "division by zero") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// CalculateStats over a single value is well defined: the mean equals the
// value and the standard deviation is zero.
func TestCalculateStatsSingleValue(t *testing.T) {
	stats := CalculateStats([]float64{42})
	if stats.Count != 1 || stats.Mean != 42 || stats.StdDev != 0 {
		t.Errorf("stats = %+v, want count=1 mean=42 stddev=0", stats)
	}
}

// Negative numbers are tracked correctly as both the minimum and part of
// the sum.
func TestCalculateStatsNegativeNumbers(t *testing.T) {
	stats := CalculateStats([]float64{-5, 0, 5})
	if stats.Min != -5 || stats.Max != 5 || stats.Sum != 0 {
		t.Errorf("stats = %+v, want min=-5 max=5 sum=0", stats)
	}
}

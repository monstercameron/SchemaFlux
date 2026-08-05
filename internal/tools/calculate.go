package tools

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// CalculateTool performs mathematical calculations.
var CalculateTool = &Tool{
	Name:        "calculate",
	Description: "Evaluate mathematical expressions including basic arithmetic, percentages, and common functions (sqrt, pow, abs, round, floor, ceil)",
	Category:    CategoryComputation,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"expression": StringParam("Mathematical expression to evaluate (e.g., '2 + 2', '15% of 200', 'sqrt(16)')"),
	}, []string{"expression"}),
	Execute: executeCalculate,
}

func executeCalculate(ctx context.Context, params map[string]any) (Result, error) {
	expr, ok := params["expression"].(string)
	if !ok {
		return ErrorResult(fmt.Errorf("expression must be a string")), nil
	}

	result, err := Calculate(expr)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta(result, map[string]any{
		"expression": expr,
		"type":       fmt.Sprintf("%T", result),
	}), nil
}

// Calculate evaluates a mathematical expression.
func Calculate(expression string) (float64, error) {
	// Normalize expression
	expr := strings.TrimSpace(expression)
	expr = strings.ToLower(expr)

	// Handle percentage expressions: "15% of 200" -> 200 * 0.15
	percentOfRegex := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%\s*of\s*(\d+(?:\.\d+)?)`)
	if matches := percentOfRegex.FindStringSubmatch(expr); len(matches) == 3 {
		percent, _ := strconv.ParseFloat(matches[1], 64)
		value, _ := strconv.ParseFloat(matches[2], 64)
		return value * (percent / 100), nil
	}

	// Handle simple percentage: "15%" -> 0.15
	simplePercentRegex := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*%$`)
	if matches := simplePercentRegex.FindStringSubmatch(expr); len(matches) == 2 {
		percent, _ := strconv.ParseFloat(matches[1], 64)
		return percent / 100, nil
	}

	// Handle function calls
	expr = replaceFunctions(expr)

	// Parse and evaluate the expression
	return evalExpr(expr)
}

// replaceFunctions replaces function calls with their evaluated values
func replaceFunctions(expr string) string {
	functions := map[string]func(float64) float64{
		"sqrt":  math.Sqrt,
		"abs":   math.Abs,
		"floor": math.Floor,
		"ceil":  math.Ceil,
		"round": math.Round,
		"sin":   math.Sin,
		"cos":   math.Cos,
		"tan":   math.Tan,
		"log":   math.Log,
		"log10": math.Log10,
		"exp":   math.Exp,
	}

	for name, fn := range functions {
		pattern := regexp.MustCompile(name + `\s*\(\s*([^)]+)\s*\)`)
		for {
			matches := pattern.FindStringSubmatchIndex(expr)
			if matches == nil {
				break
			}
			argStr := expr[matches[2]:matches[3]]
			arg, err := evalExpr(argStr)
			if err != nil {
				break
			}
			result := fn(arg)
			expr = expr[:matches[0]] + fmt.Sprintf("%v", result) + expr[matches[1]:]
		}
	}

	// Handle pow(base, exp)
	powPattern := regexp.MustCompile(`pow\s*\(\s*([^,]+)\s*,\s*([^)]+)\s*\)`)
	for {
		matches := powPattern.FindStringSubmatchIndex(expr)
		if matches == nil {
			break
		}
		baseStr := expr[matches[2]:matches[3]]
		expStr := expr[matches[4]:matches[5]]
		base, err1 := evalExpr(baseStr)
		exp, err2 := evalExpr(expStr)
		if err1 != nil || err2 != nil {
			break
		}
		result := math.Pow(base, exp)
		expr = expr[:matches[0]] + fmt.Sprintf("%v", result) + expr[matches[1]:]
	}

	// Replace constants
	expr = strings.ReplaceAll(expr, "pi", fmt.Sprintf("%v", math.Pi))
	expr = strings.ReplaceAll(expr, "e", fmt.Sprintf("%v", math.E))

	return expr
}

// evalExpr evaluates a simple arithmetic expression
func evalExpr(expr string) (float64, error) {
	expr = strings.TrimSpace(expr)

	// Try to parse as a simple number first
	if val, err := strconv.ParseFloat(expr, 64); err == nil {
		return val, nil
	}

	// Use Go's parser for safe expression evaluation
	node, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, fmt.Errorf("invalid expression: %s", expr)
	}

	return evalNode(node)
}

func evalNode(node ast.Expr) (float64, error) {
	switch n := node.(type) {
	case *ast.BasicLit:
		return strconv.ParseFloat(n.Value, 64)

	case *ast.ParenExpr:
		return evalNode(n.X)

	case *ast.UnaryExpr:
		x, err := evalNode(n.X)
		if err != nil {
			return 0, err
		}
		switch n.Op {
		case token.SUB:
			return -x, nil
		case token.ADD:
			return x, nil
		default:
			return 0, fmt.Errorf("unsupported unary operator: %v", n.Op)
		}

	case *ast.BinaryExpr:
		left, err := evalNode(n.X)
		if err != nil {
			return 0, err
		}
		right, err := evalNode(n.Y)
		if err != nil {
			return 0, err
		}

		switch n.Op {
		case token.ADD:
			return left + right, nil
		case token.SUB:
			return left - right, nil
		case token.MUL:
			return left * right, nil
		case token.QUO:
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return left / right, nil
		case token.REM:
			return math.Mod(left, right), nil
		default:
			return 0, fmt.Errorf("unsupported operator: %v", n.Op)
		}

	default:
		return 0, fmt.Errorf("unsupported expression type: %T", node)
	}
}

// Statistics functions
type Stats struct {
	Count  int     `json:"count"`
	Sum    float64 `json:"sum"`
	Mean   float64 `json:"mean"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	StdDev float64 `json:"std_dev"`
}

// CalculateStats computes statistics for a slice of numbers.
func CalculateStats(numbers []float64) Stats {
	if len(numbers) == 0 {
		return Stats{}
	}

	stats := Stats{
		Count: len(numbers),
		Min:   numbers[0],
		Max:   numbers[0],
	}

	for _, n := range numbers {
		stats.Sum += n
		if n < stats.Min {
			stats.Min = n
		}
		if n > stats.Max {
			stats.Max = n
		}
	}

	stats.Mean = stats.Sum / float64(stats.Count)

	// Calculate standard deviation
	var variance float64
	for _, n := range numbers {
		diff := n - stats.Mean
		variance += diff * diff
	}
	stats.StdDev = math.Sqrt(variance / float64(stats.Count))

	return stats
}

func init() {
	mustRegister(CalculateTool)
}

package desk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rule this package is written to: every decision about what happens next
// is made by an operation of schemaflux. This test holds the package's source
// to the part of it that can be read off the syntax, and the package's doc
// comment says what that is and what it is not. Read both together: the check
// is not a proof that nothing in the package branches on its own, and a
// function that decides something inside its own body, or through a package
// the check does not name, is outside it.
func TestThePackageBranchesOnlyOnErrors(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range handWrittenBranches(t, path, string(source)) {
			t.Errorf("%s", finding)
		}
		checked++
	}
	if checked < 4 {
		t.Fatalf("only %d source files were checked; the package has more than that", checked)
	}
}

// comparisons are the operators that decide something by comparing two values.
var comparisons = map[token.Token]bool{
	token.EQL: true, token.NEQ: true, token.LSS: true, token.LEQ: true, token.GTR: true, token.GEQ: true,
}

// stringTests are the functions of package strings that answer a question about
// a text, which is a branch when the answer is acted on.
var stringTests = map[string]bool{
	"Contains": true, "ContainsAny": true, "ContainsRune": true, "ContainsFunc": true,
	"HasPrefix": true, "HasSuffix": true, "EqualFold": true,
}

// handWrittenBranches returns every place a source file decides something for
// itself, as far as the syntax shows it. See the package's doc comment for the
// rules.
func handWrittenBranches(t *testing.T, path, source string) []string {
	t.Helper()
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, path, source, 0)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var findings []string
	report := func(node ast.Node, what string) {
		findings = append(findings, files.Position(node.Pos()).String()+": "+what)
	}

	// The comparison that is the condition of an `if` is the `if`'s to report,
	// or is an error check and allowed: either way it is not found twice.
	judged := map[ast.Expr]bool{}

	ast.Inspect(parsed, func(node ast.Node) bool {
		switch found := node.(type) {
		case *ast.IfStmt:
			judged[found.Cond] = true
			if !isErrorCheck(found.Cond) || found.Else != nil {
				report(found, "an `if` that tests something other than an error, or has an else")
			}
		case *ast.SwitchStmt, *ast.TypeSwitchStmt:
			report(found, "a `switch`")
		case *ast.SelectStmt:
			report(found, "a `select`")
		case *ast.ForStmt:
			report(found, "a `for` that is not a range over a list")
			loopBody(found.Body, report)
		case *ast.RangeStmt:
			loopBody(found.Body, report)
		case *ast.BranchStmt:
			if found.Tok == token.GOTO || found.Label != nil {
				report(found, "a `goto`, or a `break` or `continue` that names a label")
			}
		case *ast.LabeledStmt:
			report(found, "a labelled statement")
		case *ast.DeferStmt:
			report(found, "a `defer`")
		case *ast.BinaryExpr:
			switch {
			case found.Op == token.LAND || found.Op == token.LOR:
				report(found, "`&&` or `||`, which is a branch written as an expression")
			case comparisons[found.Op] && !judged[found] && !auditVerdictCheck(found):
				report(found, "a comparison that is not the condition of an error check")
			}
		case *ast.CallExpr:
			callsWhatDecides(found, report)
		case *ast.IndexExpr:
			if isBoolean(found.Index) {
				report(found, "something indexed by a boolean expression, which is an `if` written as a lookup")
			}
		case *ast.MapType:
			if key, named := found.Key.(*ast.Ident); named && key.Name == "bool" {
				report(found, "a map keyed by bool, which is an `if` written as a lookup")
			}
		case *ast.AssignStmt:
			if len(found.Lhs) == 2 && len(found.Rhs) == 1 && isCommaOK(found.Rhs[0]) {
				report(found, "the comma-ok form of a type assertion, a map index or a receive")
			}
		case *ast.ValueSpec:
			if len(found.Names) == 2 && len(found.Values) == 1 && isCommaOK(found.Values[0]) {
				report(found, "the comma-ok form of a type assertion, a map index or a receive")
			}
		}
		return true
	})
	return findings
}

// An audit verdict is already the model's decision. Comparing it with pass
// only passes that decision to a flow combinator or records it in the outcome.
func auditVerdictCheck(expr *ast.BinaryExpr) bool {
	if expr.Op != token.EQL && expr.Op != token.NEQ {
		return false
	}
	verdict, ok := expr.X.(*ast.SelectorExpr)
	if !ok || verdict.Sel.Name != "Verdict" {
		return false
	}
	pass, ok := expr.Y.(*ast.SelectorExpr)
	if !ok || pass.Sel.Name != "VerdictPass" {
		return false
	}
	pkg, ok := pass.X.(*ast.Ident)
	return ok && pkg.Name == "schemaflux"
}

// loopBody reports a `return`, `break` or `continue` in the body of a loop. A
// loop that leaves on its first item is an `if` written as a loop, and one that
// leaves when something is true is a `for` with a condition. A loop nested in
// this one is judged by its own body, and a function literal in it is a
// function of its own whose returns are its own.
func loopBody(body *ast.BlockStmt, report func(ast.Node, string)) {
	ast.Inspect(body, func(node ast.Node) bool {
		switch found := node.(type) {
		case *ast.FuncLit, *ast.ForStmt, *ast.RangeStmt:
			return false
		case *ast.ReturnStmt:
			report(found, "a `return` in the body of a loop")
		case *ast.BranchStmt:
			if (found.Tok == token.BREAK || found.Tok == token.CONTINUE) && found.Label == nil {
				report(found, "a `break` or `continue` in the body of a loop")
			}
		}
		return true
	})
}

// callsWhatDecides reports a call that is a decision made in Go: the builtins
// min, max and recover, anything in the packages cmp and slices, and the
// functions of package strings that ask whether a text has something in it.
func callsWhatDecides(call *ast.CallExpr, report func(ast.Node, string)) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		switch fun.Name {
		case "min", "max", "recover":
			report(call, "a call to the builtin "+fun.Name)
		}
	case *ast.SelectorExpr:
		pkg, named := fun.X.(*ast.Ident)
		switch {
		case !named:
		case pkg.Name == "cmp" || pkg.Name == "slices":
			report(call, "a call into package "+pkg.Name)
		case pkg.Name == "strings" && stringTests[fun.Sel.Name]:
			report(call, "a call to strings."+fun.Sel.Name)
		}
	}
}

// isBoolean reports whether an expression is plainly a boolean: a comparison, a
// logical expression, a negation, or true or false written out. A boolean held
// in a variable is not seen, since the check has no types.
func isBoolean(expression ast.Expr) bool {
	switch found := expression.(type) {
	case *ast.BinaryExpr:
		return comparisons[found.Op] || found.Op == token.LAND || found.Op == token.LOR
	case *ast.UnaryExpr:
		return found.Op == token.NOT
	case *ast.Ident:
		return found.Name == "true" || found.Name == "false"
	case *ast.ParenExpr:
		return isBoolean(found.X)
	}
	return false
}

// isCommaOK reports whether the one expression on the right of a two-value
// assignment is one of the forms that gives a second, boolean, value.
func isCommaOK(expression ast.Expr) bool {
	switch found := expression.(type) {
	case *ast.TypeAssertExpr, *ast.IndexExpr:
		return true
	case *ast.UnaryExpr:
		return found.Op == token.ARROW
	case *ast.ParenExpr:
		return isCommaOK(found.X)
	}
	return false
}

// isErrorCheck reports whether a condition asks whether an error came back:
// `x != nil` with x an identifier named err or ending in Err, or a call to
// errors.Is or errors.As.
func isErrorCheck(condition ast.Expr) bool {
	switch found := condition.(type) {
	case *ast.BinaryExpr:
		left, leftOK := found.X.(*ast.Ident)
		right, rightOK := found.Y.(*ast.Ident)
		return found.Op == token.NEQ && leftOK && rightOK && right.Name == "nil" &&
			(left.Name == "err" || strings.HasSuffix(left.Name, "Err"))
	case *ast.CallExpr:
		selected, ok := found.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, named := selected.X.(*ast.Ident)
		return named && pkg.Name == "errors" && (selected.Sel.Name == "Is" || selected.Sel.Name == "As")
	}
	return false
}

// The check itself is checked: it has to find each kind of branch it is there
// to find, and pass code that has none. A snippet is the body of a function; it
// is parsed and not compiled, so it may name what it does not declare.
func TestTheBranchCheckFindsWhatItLooksFor(t *testing.T) {
	wrap := func(body string) string {
		return "package p\nfunc f(x int, err error, list []int, c chan int) int {\n" + body + "\nreturn 0\n}\n"
	}
	cases := []struct {
		name  string
		body  string
		found int
	}{
		// What it lets through.
		{"an error check", "if err != nil { return 1 }", 0},
		{"an error check with an init", "if err := g(); err != nil { return 1 }", 0},
		{"a name that ends in Err", "readErr := g()\nif readErr != nil { return 1 }", 0},
		{"another name that ends in Err", "var dialErr error\nif dialErr != nil { return 1 }", 0},
		{"errors.Is", "if errors.Is(err, context.Canceled) { return 1 }", 0},
		{"errors.As", "var target *E\nif errors.As(err, &target) { return 1 }", 0},
		{"a range over a list", "for _, item := range list { _ = item }", 0},
		{"a range that maps a list to a list", "out := make([]int, len(list))\nfor i, item := range list { out[i] = item + 1 }", 0},
		{"a range over a count", "for i := range 3 { _ = i }", 0},
		{"a range with a function literal that returns", "for _, item := range list { run(func() int { return item }) }", 0},
		{"a range with an error check, which cannot leave it", "for _, item := range list { _ = item\nif err != nil { run() } }", 0},
		{"straight-line code", "y := x + 1\n_ = y", 0},
		{"a map indexed by a string", "m := map[string]int{\"a\": 1}\n_ = m[\"a\"]", 0},
		{"a list indexed by a number", "_ = list[0]", 0},
		{"a map read the single-value way", "_ = m[k]", 0},
		{"a single-value type assertion", "_ = v.(int)", 0},
		{"a receive", "v := <-c\n_ = v", 0},
		{"a call into package strings that does not ask a question", "_ = strings.TrimSpace(strings.Split(\"a,b\", \",\")[0])", 0},
		{"a length", "_ = len(list)", 0},
		{"a goroutine", "go run()", 0},
		{"a function literal with an error check of its own", "f := func() error { if err != nil { return err }\nreturn nil }\n_ = f", 0},

		// An `if`.
		{"an if on a value", "if x > 3 { return 1 }", 1},
		{"an if on a bool", "ok := x > 3\nif ok { return 1 }", 2},
		{"an error check with an else", "if err != nil { return 1 } else { return 2 }", 1},
		{"an error check with an else if", "if err != nil { return 1 } else if x > 0 { return 2 }", 2},
		{"an error check the other way round", "if nil != err { return 1 }", 1},
		{"an if on err == nil", "if err == nil { return 1 }", 1},
		{"an if with an init", "if y := x; y > 0 { return 1 }", 1},
		{"an if on a name that is not an error", "if flag != nil { return 1 }", 1},
		{"an if on a name that only starts with err", "if errno != nil { return 1 }", 1},
		{"an if on a name that ends in err in lower case", "if readerr != nil { return 1 }", 1},
		{"an error check and another branch", "if err != nil { return 1 }\nif x > 0 { return 2 }", 1},

		// The other statements that choose.
		{"a switch", "switch x { case 1: return 1 }", 1},
		{"a switch with no tag", "switch { case x > 1: return 1 }", 2},
		{"a type switch", "var v any = x\nswitch v.(type) { case int: return 1 }", 1},
		{"a select", "select { case <-c: return 1 }", 1},
		{"a for with a condition", "for x < 3 { x++ }", 2},
		{"a for with three parts", "for i := 0; i < x; i++ { }", 2},
		{"a bare for", "for { break }", 2},
		{"a goto", "goto done\ndone:", 2},
		{"a labelled statement", "outer:\n_ = x", 1},
		{"a labelled break", "outer:\nfor _, v := range list { _ = v\nbreak outer }", 2},
		{"a defer", "defer g()", 1},
		{"a deferred recover", "defer func() { _ = recover() }()", 2},

		// A condition written as an expression.
		{"a logical and", "ok := x > 1 && x < 5\n_ = ok", 3},
		{"a logical or", "ok := x > 1 || x < 0\n_ = ok", 3},
		{"a comparison passed to a function", "_ = g(len(list) > 0)", 1},
		{"a comparison passed to Branch", "known, err := schemaflux.Branch(ctx, len(d.Open) > 0, a, b)\n_ = known\nif err != nil { return 1 }", 1},
		{"an equality assigned", "ok := x == 3\n_ = ok", 1},
		{"an inequality assigned", "ok := x != 3\n_ = ok", 1},
		{"a less than", "_ = g(x < 3)", 1},
		{"a less than or equal", "_ = g(x <= 3)", 1},
		{"a greater than", "_ = g(x > 3)", 1},
		{"a greater than or equal", "_ = g(x >= 3)", 1},
		{"an error check used as a value", "failed := err != nil\n_ = failed", 1},

		// A decision made by a function.
		{"min", "_ = min(x, 3)", 1},
		{"max", "_ = max(x, 3)", 1},
		{"recover", "_ = recover()", 1},
		{"cmp.Or", "_ = cmp.Or(x, 2)", 1},
		{"cmp.Compare", "_ = cmp.Compare(x, 2)", 1},
		{"slices.Contains", "_ = slices.Contains(list, x)", 1},
		{"slices.Index", "_ = slices.Index(list, x)", 1},
		{"slices.Sort, which is any call into the package", "slices.Sort(list)", 1},
		{"strings.Contains", "_ = g(strings.Contains(s, \"a\"))", 1},
		{"strings.HasPrefix", "_ = g(strings.HasPrefix(s, \"a\"))", 1},
		{"strings.HasSuffix", "_ = g(strings.HasSuffix(s, \"a\"))", 1},
		{"strings.EqualFold", "_ = g(strings.EqualFold(s, \"a\"))", 1},
		{"strings.ContainsAny", "_ = g(strings.ContainsAny(s, \"a\"))", 1},

		// A loop that decides.
		{"a range that returns on its first item", "for _, item := range list { return item }", 1},
		{"a range that breaks", "for range list { break }", 1},
		{"a range that continues", "for range list { continue }", 1},
		{"a loop that retries", "for range 3 { if err != nil { continue }\nbreak }", 2},
		{"a return in a loop in a loop", "for _, a := range list { for _, b := range list { return b } }", 1},

		// A branch written as a lookup.
		{"a lookup by a comparison", "_ = list[len(list) > 0]", 2},
		{"a lookup by a negation", "_ = m[!ok]", 1},
		{"a lookup by true", "_ = m[true]", 1},
		{"a map keyed by bool", "m := map[bool]error{true: err}\n_ = m", 1},
		{"an if written as a map keyed by bool", "var err error = map[bool]error{true: errBig, false: nil}[len(text) > 4000]\nif err != nil { return 1 }", 3},
		{"a type assertion with ok", "v, ok := a.(int)\n_, _ = v, ok", 1},
		{"a type assertion with ok in a var", "var v, ok = a.(int)\n_, _ = v, ok", 1},
		{"a map index with ok", "v, ok := m[k]\n_, _ = v, ok", 1},
		{"a receive with ok", "v, ok := <-c\n_, _ = v, ok", 1},
		{"a type assertion with ok in an if", "if v, ok := a.(int); ok { return v }", 2},
	}
	if len(cases) < 40 {
		t.Fatalf("%d snippets; the check is held to at least 40", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := handWrittenBranches(t, "case.go", wrap(tc.body))
			if len(findings) != tc.found {
				t.Errorf("%d findings, want %d: %v", len(findings), tc.found, findings)
			}
		})
	}
}

package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A StubResult reports Success: true for something that did not happen. The one
// filter a consumer has is Tool.IsStub, so a tool that returns a stub result
// without setting the flag ships a fake implementation that looks real. The
// `token` tool did exactly that with JWTs.
//
// This walks the package source: for every tool declaration, it finds the
// function its Execute field names, and checks whether that function (or
// anything it calls within the package) reaches StubResult.
func TestEveryStubResultBelongsToAStubTool(t *testing.T) {
	fileSet := token.NewFileSet()
	files := parsePackageSource(t, fileSet)

	// Which package-level functions reach StubResult, directly or through one
	// hop of another function in this package.
	direct := map[string]bool{}
	calls := map[string][]string{}

	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil {
				continue
			}
			name := function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				if identifier.Name == "StubResult" {
					direct[name] = true
				}
				calls[name] = append(calls[name], identifier.Name)
				return true
			})
		}
	}

	reaches := func(name string) bool {
		if direct[name] {
			return true
		}
		for _, callee := range calls[name] {
			if direct[callee] {
				return true
			}
		}
		return false
	}

	type declaredTool struct {
		variable string
		toolName string
		isStub   bool
		execute  string
		stubbing bool
	}

	var declared []declaredTool

	for _, file := range files {
		for _, declaration := range file.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok || generic.Tok != token.VAR {
				continue
			}
			for _, specification := range generic.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				composite := toolComposite(value.Values[0])
				if composite == nil {
					continue
				}

				tool := declaredTool{variable: value.Names[0].Name}
				for _, element := range composite.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok {
						continue
					}
					switch key.Name {
					case "Name":
						if literal, ok := pair.Value.(*ast.BasicLit); ok {
							tool.toolName = strings.Trim(literal.Value, `"`)
						}
					case "IsStub":
						if identifier, ok := pair.Value.(*ast.Ident); ok {
							tool.isStub = identifier.Name == "true"
						}
					case "Execute":
						switch executor := pair.Value.(type) {
						case *ast.Ident:
							tool.execute = executor.Name
						case *ast.FuncLit:
							ast.Inspect(executor.Body, func(node ast.Node) bool {
								call, ok := node.(*ast.CallExpr)
								if !ok {
									return true
								}
								if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "StubResult" {
									tool.stubbing = true
								}
								return true
							})
						}
					}
				}
				declared = append(declared, tool)
			}
		}
	}

	if len(declared) < 20 {
		t.Fatalf("only %d tool declarations found; the parser is not seeing the package", len(declared))
	}

	checked := 0
	for _, tool := range declared {
		stubbing := tool.stubbing || (tool.execute != "" && reaches(tool.execute))
		if !stubbing {
			continue
		}
		checked++
		if !tool.isStub {
			t.Errorf("%s (tool %q) can return a StubResult but is not marked IsStub: true; "+
				"a consumer filtering on IsStub would ship it as a working implementation",
				tool.variable, tool.toolName)
		}
	}

	if checked == 0 {
		t.Fatal("no stub-returning tool was found, so this test witnesses nothing")
	}
	t.Logf("checked %d stub-returning tools out of %d declarations", checked, len(declared))
}

// Every tool marked IsStub must say so in its description too, so a human
// reading the tool list sees it without inspecting the flag.
func TestStubToolsAnnounceThemselves(t *testing.T) {
	for _, tool := range List() {
		if !tool.IsStub {
			continue
		}
		if !strings.Contains(strings.ToLower(tool.Description), "stub") &&
			!strings.Contains(strings.ToLower(tool.Description), "requires") {
			t.Errorf("tool %q is a stub but its description does not say so: %q", tool.Name, tool.Description)
		}
	}
}

// toolComposite returns the composite literal if the expression declares a
// *Tool, and nil otherwise.
func toolComposite(expression ast.Expr) *ast.CompositeLit {
	unary, ok := expression.(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return nil
	}
	composite, ok := unary.X.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	identifier, ok := composite.Type.(*ast.Ident)
	if !ok || identifier.Name != "Tool" {
		return nil
	}
	return composite
}

func parsePackageSource(t *testing.T, fileSet *token.FileSet) []*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, file)
	}

	if len(files) == 0 {
		t.Fatal("no source files were parsed")
	}
	return files
}

package fluent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// directRequest guards every setter with `if r.setX == nil { return r.lift(...) }`,
// so a builder that forgets to wire one gets a method that compiles, chains,
// and changes nothing. There is no compile error, no runtime warning, and no
// test that would notice.
//
// NegotiatingAdversarially shipped exactly that: it wired five setters and not
// setMode, so .Strict(), .TransformMode(), and .Creative() were dead. The field
// they would have written did not even exist on AdversarialOptions.
//
// This walks every constructor that builds a directRequest or opRequest and
// fails any that leaves a setter the base declares unwired.
func TestEveryBuilderWiresEverySetter(t *testing.T) {
	fileSet := token.NewFileSet()
	files := parseFluentPackage(t, fileSet)

	// The setter fields the request bases declare.
	setters := map[string]struct{}{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(spec.Name.Name, "Request") && !strings.Contains(spec.Name.Name, "request") {
				return true
			}
			structType, ok := spec.Type.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if strings.HasPrefix(name.Name, "set") {
						setters[name.Name] = struct{}{}
					}
				}
			}
			return true
		})
	}
	if len(setters) < 4 {
		t.Fatalf("only %d setter fields found: %v; the walk is not seeing the bases", len(setters), setters)
	}

	// Every composite literal that assigns at least two setters is a builder
	// construction, and must assign all of them.
	type gap struct {
		function string
		missing  []string
	}
	var gaps []gap

	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				composite, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}

				assigned := map[string]struct{}{}
				for _, element := range composite.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok {
						continue
					}
					if _, isSetter := setters[key.Name]; isSetter {
						assigned[key.Name] = struct{}{}
					}
				}
				if len(assigned) < 2 {
					return true
				}

				var missing []string
				for setter := range setters {
					if _, wired := assigned[setter]; !wired {
						missing = append(missing, setter)
					}
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					gaps = append(gaps, gap{function: function.Name.Name, missing: missing})
				}
				return true
			})
		}
	}

	for _, g := range gaps {
		t.Errorf("%s leaves %s unwired: the matching builder methods compile, chain, and change nothing",
			g.function, strings.Join(g.missing, ", "))
	}
}

// The options struct behind every builder has to have somewhere to put a Mode
// and an Intelligence, or the setter cannot be wired even in principle.
func TestBuilderOptionsCanHoldModeAndIntelligence(t *testing.T) {
	// AdversarialOptions had no Mode field at all, which is why setMode was
	// missing rather than merely forgotten. Assert the shape the base assumes.
	request := NegotiatingAdversarially[string](AdversarialContext[string]{})

	strict := request.Strict()
	if strict.opts.Mode != Strict {
		t.Errorf("Strict() produced Mode %v, want Strict", strict.opts.Mode)
	}

	creative := request.Creative()
	if creative.opts.Mode != Creative {
		t.Errorf("Creative() produced Mode %v, want Creative", creative.opts.Mode)
	}

	transform := request.TransformMode()
	if transform.opts.Mode != TransformMode {
		t.Errorf("TransformMode() produced Mode %v, want TransformMode", transform.opts.Mode)
	}
}

func parseFluentPackage(t *testing.T, fileSet *token.FileSet) []*ast.File {
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
		t.Fatal("no source files parsed")
	}
	return files
}

package ops

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

// An options field with a fluent setter and no reader is worse than a missing
// feature: the call chain reads as if it configured something, the compiler
// agrees, and the behaviour is unchanged. The caller has no way to find out
// except by testing the output.
//
// This walks the package and reports any exported field on a *Options struct
// that is never read outside its own declaration, its setter, and the merge
// function that copies it. It is what produced the F-023 list; keeping it as a
// test is what stops the class recurring.

// knownDead records fields that are deliberately still dead, with the task that
// will resolve them. An entry here is a promise, not a pardon: the test fails if
// a listed field turns out to be live, so the list cannot rot into fiction.
var knownDead = map[string]string{
	// Empty, and it should stay that way. F-023 resolved all eighteen: the six
	// that promised deterministic machinery a prompt cannot deliver -- progress
	// callbacks, pre/post processing, resume-from-failure, preserve-nulls,
	// preserve-format -- were deleted, and the twelve that are legitimately
	// instructions to the model were threaded into their prompts.
	//
	// A new entry is a deliberate decision to ship a field that does nothing,
	// and needs a task ID saying when it stops.
}

func TestNoDeadOptionFields(t *testing.T) {
	fileSet := token.NewFileSet()
	files := map[string]*ast.File{}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files[name] = file
	}
	if len(files) < 10 {
		t.Fatalf("only %d files parsed; the walk is not seeing the package", len(files))
	}

	// Every exported field declared on a type whose name ends in Options.
	declared := map[string]string{} // "Type.Field" -> field name
	fieldNames := map[string][]string{}

	for _, file := range files {
		for _, declaration := range file.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok || generic.Tok != token.TYPE {
				continue
			}
			for _, specification := range generic.Specs {
				typeSpec, ok := specification.(*ast.TypeSpec)
				if !ok || !strings.HasSuffix(typeSpec.Name.Name, "Options") {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok || structType.Fields == nil {
					continue
				}
				for _, field := range structType.Fields.List {
					for _, name := range field.Names {
						if !name.IsExported() {
							continue
						}
						key := typeSpec.Name.Name + "." + name.Name
						declared[key] = name.Name
						fieldNames[name.Name] = append(fieldNames[name.Name], key)
					}
				}
			}
		}
	}
	if len(declared) < 50 {
		t.Fatalf("only %d option fields found; the walk is not seeing the package", len(declared))
	}

	// Count every selector use of each field name, excluding the contexts that
	// do not constitute a reader: the setter that assigns it and the merge
	// function that copies it.
	//
	// This is name-based rather than type-based, which makes it conservative:
	// a field named Steering is "read" if any Options type reads a Steering.
	// A conservative check that under-reports is the right trade here — it will
	// not fail a build over a field that is genuinely used.
	uses := map[string]int{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			function, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}

			name := function.Name.Name
			isSetter := strings.HasPrefix(name, "With") || strings.HasPrefix(name, "Set")
			isMerge := strings.Contains(strings.ToLower(name), "merge") ||
				strings.Contains(strings.ToLower(name), "toopoptions") ||
				strings.Contains(strings.ToLower(name), "applydefaults") ||
				strings.HasPrefix(name, "New")
			if isSetter || isMerge {
				return false
			}

			ast.Inspect(function, func(inner ast.Node) bool {
				selector, ok := inner.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if _, tracked := fieldNames[selector.Sel.Name]; tracked {
					uses[selector.Sel.Name]++
				}
				return true
			})
			return false
		})
	}

	var dead []string
	for key, fieldName := range declared {
		if uses[fieldName] > 0 {
			continue
		}
		dead = append(dead, key)
	}
	sort.Strings(dead)

	var unexplained []string
	for _, key := range dead {
		if _, known := knownDead[key]; !known {
			unexplained = append(unexplained, key)
		}
	}

	for _, key := range unexplained {
		t.Errorf("%s has a setter and no reader: configuring it changes nothing. "+
			"Implement it, delete it, or add it to knownDead with the task that will.", key)
	}

	// A field listed as dead that has become live must leave the list, or the
	// list stops meaning anything.
	deadSet := map[string]struct{}{}
	for _, key := range dead {
		deadSet[key] = struct{}{}
	}
	for key, reason := range knownDead {
		if _, stillDead := deadSet[key]; !stillDead {
			if _, exists := declared[key]; !exists {
				t.Errorf("knownDead lists %s (%s), which no longer exists; remove the entry", key, reason)
				continue
			}
			t.Errorf("knownDead lists %s (%s), but it is read now; remove the entry", key, reason)
		}
	}

	t.Logf("%d option fields, %d dead, %d of those recorded", len(declared), len(dead), len(dead)-len(unexplained))
}

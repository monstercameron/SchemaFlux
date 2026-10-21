package flow_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestEveryExportedNameOfDecideAndMatchIsDocumented holds the rule of the family
// to the two files: the comment of an exported function or type starts with its
// name, and every field of an exported struct has a comment of its own, which is
// where its zero value is said.
func TestEveryExportedNameOfDecideAndMatchIsDocumented(t *testing.T) {
	for _, name := range []string{"decide.go", "match.go"} {
		t.Run(name, func(t *testing.T) { requireDocumented(t, name) })
	}
}

// requireDocumented holds the Go file at path to the rule.
func requireDocumented(t *testing.T, path string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.IsExported() {
				requireDocStartsWith(t, d.Name.Name, d.Doc)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.IsExported() {
					requireTypeDocumented(t, ts, d.Doc)
				}
			}
		}
	}
}

// requireTypeDocumented holds an exported type, and the fields of it that are
// exported, to the rule. groupDoc is the comment of the declaration the type is
// in, which is the type's own when it has none.
func requireTypeDocumented(t *testing.T, ts *ast.TypeSpec, groupDoc *ast.CommentGroup) {
	t.Helper()
	doc := ts.Doc
	if doc == nil {
		doc = groupDoc
	}
	requireDocStartsWith(t, ts.Name.Name, doc)
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return
	}
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if name.IsExported() && (field.Doc == nil || strings.TrimSpace(field.Doc.Text()) == "") {
				t.Errorf("the field %s.%s has no comment", ts.Name.Name, name.Name)
			}
		}
	}
}

func requireDocStartsWith(t *testing.T, name string, doc *ast.CommentGroup) {
	t.Helper()
	if doc == nil {
		t.Errorf("%s has no comment", name)
		return
	}
	if !strings.HasPrefix(doc.Text(), name+" ") {
		first, _, _ := strings.Cut(doc.Text(), "\n")
		t.Errorf("the comment of %s starts %q, not with its name", name, first)
	}
}

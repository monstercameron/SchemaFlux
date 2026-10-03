package redact_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryExportedNameIsDocumentedStartingWithItsName holds the package to the
// documentation rule: a comment that starts with the name on every exported
// function, type and constant, and a comment of its own on every exported field.
func TestEveryExportedNameIsDocumentedStartingWithItsName(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		starts := func(doc *ast.CommentGroup, name string) bool {
			return doc != nil && strings.HasPrefix(doc.Text(), name+" ")
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.IsExported() && d.Recv == nil {
					checked++
					if !starts(d.Doc, d.Name.Name) {
						t.Errorf("func %s has no comment that starts with its name", d.Name.Name)
					}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						checked++
						doc := s.Doc
						if doc == nil {
							doc = d.Doc
						}
						if !starts(doc, s.Name.Name) {
							t.Errorf("type %s has no comment that starts with its name", s.Name.Name)
						}
						if st, ok := s.Type.(*ast.StructType); ok {
							for _, field := range st.Fields.List {
								for _, fieldName := range field.Names {
									if fieldName.IsExported() && field.Doc == nil {
										t.Errorf("field %s.%s has no comment above it", s.Name.Name, fieldName.Name)
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								checked++
								if !starts(d.Doc, n.Name) && !starts(s.Doc, n.Name) {
									t.Errorf("%s has no comment that starts with its name", n.Name)
								}
							}
						}
					}
				}
			}
		}
	}
	// The sixteen names of the package, as the table of names has them.
	if checked != 16 {
		t.Errorf("the package declares %d exported names, want the 16 of the table of names", checked)
	}
}

// TestEveryOptionFieldSaysWhatItsZeroMeansAndWhatANegativeNumberIs holds the
// options types to the documentation rule for a setting: the comment of a field
// says what the zero value means, and the comment of a number says what a negative
// one is.
func TestEveryOptionFieldSaysWhatItsZeroMeansAndWhatANegativeNumberIs(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"value.go", "text.go"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || (ts.Name.Name != "ValueOptions" && ts.Name.Name != "TextOptions") {
					continue
				}
				for _, field := range ts.Type.(*ast.StructType).Fields.List {
					for _, fieldName := range field.Names {
						text := strings.Join(strings.Fields(field.Doc.Text()), " ")
						if !strings.Contains(text, "Zero means") && !strings.Contains(text, "Nil means") && !strings.Contains(text, "Empty means") {
							t.Errorf("%s.%s does not say what its zero value means: %q", ts.Name.Name, fieldName.Name, text)
						}
						switch fieldName.Name {
						case "MaskChar", "MaskLength", "JumbleSeed", "ShowFirst", "ShowLast", "MinMask":
							if !strings.Contains(text, "negative") && !strings.Contains(text, "below") {
								t.Errorf("%s.%s does not say what a negative number is: %q", ts.Name.Name, fieldName.Name, text)
							}
						}
					}
				}
			}
		}
	}
}

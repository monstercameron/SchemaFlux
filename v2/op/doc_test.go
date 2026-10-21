package op_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryExportedNameIsDocumentedStartingWithItsName holds the package to the
// documentation rule: a comment that starts with the name for every exported
// function, method, type and constant, and a comment of its own above every
// exported field of a struct. The fields of a group of constants share the
// comment of the group only when each has its own.
func TestEveryExportedNameIsDocumentedStartingWithItsName(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
					if !d.Name.IsExported() {
						continue
					}
					if d.Recv != nil {
						// A method of an unexported type is not part of the surface.
						if !exportedReceiver(d.Recv) {
							continue
						}
					}
					if !starts(d.Doc, d.Name.Name) {
						t.Errorf("func %s has no comment that starts with its name", d.Name.Name)
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if !s.Name.IsExported() {
								continue
							}
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
								if n.IsExported() && !starts(d.Doc, n.Name) && !starts(s.Doc, n.Name) {
									t.Errorf("%s has no comment that starts with its name", n.Name)
								}
							}
						}
					}
				}
			}
		})
	}
}

// exportedReceiver reports whether the receiver of a method is an exported type.
func exportedReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	expr := recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.IsExported()
		default:
			return false
		}
	}
}

package schemaflux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// supportFiles are the files of the root that hold no operation function: the
// types, the client, the helpers and the combinators that ask no model. Their
// documentation is held by the package's own surface tests.
var supportFiles = map[string]bool{
	"bind.go": true, "call.go": true, "client.go": true, "decision.go": true,
	"doc.go": true, "environment.go": true, "errors.go": true, "flow.go": true,
	"options.go": true, "provider.go": true, "record.go": true, "version.go": true,
}

// operationFiles are the files of the root that hold operation functions: every
// source file of the package that is not a test and not a support file. A file a
// lane adds is found here without anyone editing a list, and the test holds every
// exported name in it to the documentation rule (T6): a comment that starts with
// the name, and for a field of a struct, a comment of its own.
func operationFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, name := range all {
		if strings.HasSuffix(name, "_test.go") || supportFiles[name] {
			continue
		}
		files = append(files, name)
	}
	if len(files) < 4 {
		t.Fatalf("found %d operation files, want at least the first four: %v", len(files), files)
	}
	return files
}

func TestEveryExportedNameOfTheOperationFilesIsDocumentedStartingWithItsName(t *testing.T) {
	t.Parallel()
	for _, name := range operationFiles(t) {
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
					if d.Name.IsExported() && d.Recv == nil && !starts(d.Doc, d.Name.Name) {
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

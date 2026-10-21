package schemaflux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The documentation rule of the family (T6) for collection.go and judge.go, which
// call_doc_test.go does not list: every exported name has a comment that starts
// with its name, every field of a struct has a comment of its own, and every field
// of an options type says what its zero value means and, for a number, what a
// negative one or one out of range means.

var collectionAndJudgeFiles = []string{"collection.go", "judge.go"}

func TestEveryExportedNameOfTheCollectionAndJudgeFilesIsDocumentedStartingWithItsName(t *testing.T) {
	t.Parallel()
	for _, name := range collectionAndJudgeFiles {
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

func TestEveryFieldOfAnOptionsTypeSaysWhatItsZeroMeansAndWhatANegativeMeans(t *testing.T) {
	t.Parallel()
	zeroWords := []string{"Zero", "Nil", "Empty", "False"}
	rangeWords := []string{"negative", "outside", "not a number"}
	for _, name := range collectionAndJudgeFiles {
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
				if !ok || !strings.HasSuffix(ts.Name.Name, "Options") {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					if len(field.Names) == 0 {
						continue
					}
					text := field.Doc.Text()
					mentions := func(words []string) bool {
						for _, w := range words {
							if strings.Contains(text, w) {
								return true
							}
						}
						return false
					}
					typeName, _ := field.Type.(*ast.Ident)
					for _, fieldName := range field.Names {
						if !mentions(zeroWords) {
							t.Errorf("%s.%s does not say what its zero value means", ts.Name.Name, fieldName.Name)
						}
						if typeName != nil && (typeName.Name == "int" || typeName.Name == "float64") && !mentions(rangeWords) {
							t.Errorf("%s.%s is a number and does not say what a negative or out of range value means", ts.Name.Name, fieldName.Name)
						}
					}
				}
			}
		}
	}
}

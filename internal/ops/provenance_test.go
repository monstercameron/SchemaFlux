package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A field called Confidence reads as a measurement. Every one of these is the
// model's own claim about its own answer: uncalibrated, not comparable across
// models or prompts, and produced by the same process that produced the answer
// being scored. The name has to say so, because the number does not.
//
// This walks the package source and fails on any exported field whose name
// suggests a score without saying where it came from.
func TestNoExportedFieldHidesItsProvenance(t *testing.T) {
	// Names that read as measurements and are not. Anything matching must be
	// prefixed with Model, or carry a comment naming a measured source.
	suspect := map[string]struct{}{
		"Confidence":        {},
		"TrustScore":        {},
		"OverallConfidence": {},
		"OverallScore":      {},
	}

	fileSet := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	checked := 0
	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || filepath.Ext(fileName) != ".go" || strings.HasSuffix(fileName, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fileSet, fileName, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", fileName, err)
		}

		ast.Inspect(file, func(node ast.Node) bool {
			structType, ok := node.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if !name.IsExported() {
						continue
					}
					checked++
					if _, bad := suspect[name.Name]; bad {
						t.Errorf("%s:%d: field %q reads as a measurement but is the model's own claim; "+
							"prefix it with Model or document a measured source",
							fileName, fileSet.Position(name.Pos()).Line, name.Name)
					}
				}
			}
			return true
		})
	}

	if checked < 200 {
		t.Fatalf("only %d exported fields inspected; the walk is not seeing the package", checked)
	}
	t.Logf("inspected %d exported fields", checked)
}

// The rename is only half the job: a reader who sees ModelConfidence still has
// to be told what it means. Every declaration carries the same note.
func TestModelConfidenceFieldsDocumentTheirProvenance(t *testing.T) {
	fileSet := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	documented, total := 0, 0
	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || filepath.Ext(fileName) != ".go" || strings.HasSuffix(fileName, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fileSet, fileName, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", fileName, err)
		}

		ast.Inspect(file, func(node ast.Node) bool {
			structType, ok := node.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if name.Name != "ModelConfidence" {
						continue
					}
					total++
					if field.Doc != nil && strings.Contains(field.Doc.Text(), "model") {
						documented++
					}
				}
			}
			return true
		})
	}

	if total == 0 {
		t.Fatal("no ModelConfidence fields found; the walk is not seeing the package")
	}
	// The anonymous structs used for parsing a response are not part of the
	// public surface and do not need the note; the declared result types do.
	if documented == 0 {
		t.Errorf("none of the %d ModelConfidence fields explains where the number comes from", total)
	}
	t.Logf("%d of %d ModelConfidence fields carry the provenance note", documented, total)
}

// The exported surface must not have kept a bare Confidence anywhere, which is
// F-020's stated check.
func TestNoBareConfidenceRemainsInTheExportedSurface(t *testing.T) {
	roots := []string{".", filepath.Join("..", ".."), filepath.Join("..", "types"), filepath.Join("..", "llm")}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			fileName := entry.Name()
			if entry.IsDir() || filepath.Ext(fileName) != ".go" || strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			path := filepath.Join(root, fileName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}

			for lineNumber, line := range strings.Split(string(raw), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.HasPrefix(trimmed, "Confidence ") || strings.HasPrefix(trimmed, "Confidence\t") {
					t.Errorf("%s:%d: a bare Confidence field survives: %s", path, lineNumber+1, trimmed)
				}
			}
		}
	}
}

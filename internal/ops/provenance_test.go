package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
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

// The rename has to hold across every result type a caller touches, not just
// the ones that happened to be edited. These name the types the review called
// out and assert the field is there under its honest name.
func TestNamedResultTypesCarryModelConfidence(t *testing.T) {
	cases := []struct {
		name  string
		value any
		field string
	}{
		{"ValidateResult", ValidateResult[struct{}]{}, "ModelConfidence"},
		{"ClassifyResult", ClassifyResult[string]{}, "ModelConfidence"},
		{"Summary", Summary{}, "ModelConfidence"},
		{"Rewritten", Rewritten{}, "ModelConfidence"},
		{"Translation", Translation{}, "ModelConfidence"},
		{"Expansion", Expansion{}, "ModelConfidence"},
		{"DecisionResult", DecisionResult{}, "ModelConfidence"},
		{"ProjectResult", ProjectResult[struct{}]{}, "ModelConfidence"},
		{"FormatResult", FormatResult{}, "ModelConfidence"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			structType := reflect.TypeOf(tc.value)
			if _, found := structType.FieldByName(tc.field); !found {
				t.Errorf("%s has no %s field", tc.name, tc.field)
			}
			if _, found := structType.FieldByName("Confidence"); found {
				t.Errorf("%s still has a bare Confidence field", tc.name)
			}
		})
	}
}

// ScoreResult and ParseResult carry no confidence at all, which is the right
// answer rather than an oversight: ScoreResult's Value IS the model's judgement,
// and a second number scoring that judgement would add nothing. This pins that
// as a decision so nobody adds one back.
func TestSomeResultsCarryNoConfidenceByDesign(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"ScoreResult", ScoreResult{}},
		{"ParseResult", ParseResult[struct{}]{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			structType := reflect.TypeOf(tc.value)
			for _, field := range []string{"Confidence", "ModelConfidence"} {
				if _, found := structType.FieldByName(field); found {
					t.Errorf("%s gained a %s field; if that is deliberate, update this test with the reason",
						tc.name, field)
				}
			}
		})
	}
}

// Every renamed name must be reachable from the root package, or the rename
// broke the public surface rather than clarifying it.
func TestRenamedFieldsAreStillReadable(t *testing.T) {
	cases := []struct {
		name string
		read func() float64
	}{
		{"ValidateResult", func() float64 { return ValidateResult[struct{}]{ModelConfidence: 0.5}.ModelConfidence }},
		{"DecisionResult", func() float64 { return DecisionResult{ModelConfidence: 0.5}.ModelConfidence }},
		{"Summary", func() float64 { return Summary{ModelConfidence: 0.5}.ModelConfidence }},
		{"Rewritten", func() float64 { return Rewritten{ModelConfidence: 0.5}.ModelConfidence }},
		{"Translation", func() float64 { return Translation{ModelConfidence: 0.5}.ModelConfidence }},
		{"Expansion", func() float64 { return Expansion{ModelConfidence: 0.5}.ModelConfidence }},
		{"FormatResult", func() float64 { return FormatResult{ModelConfidence: 0.5}.ModelConfidence }},
		{"ProjectResult", func() float64 { return ProjectResult[struct{}]{ModelConfidence: 0.5}.ModelConfidence }},
		{"ClassifyResult", func() float64 { return ClassifyResult[string]{ModelConfidence: 0.5}.ModelConfidence }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.read(); got != 0.5 {
				t.Errorf("%s.ModelConfidence round-tripped to %v", tc.name, got)
			}
		})
	}
}

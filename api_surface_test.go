package schemaflux_test

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

// CI-007. A snapshot of the exported surface.
//
// The point is not that the surface must never change -- it is that a change
// has to be deliberate. An accidental export is a compatibility promise nobody
// decided to make, and a removal is a broken build for somebody who did not ask
// for one. Both show up here as a diff in review rather than in a bug report.
//
// Regenerate deliberately:
//
//	SCHEMAFLUX_UPDATE_API=1 go test . -run TestPublicAPISurface
//
// An environment variable rather than a flag, because `go test` parses its own
// flags first and an unknown one fails the run before the test sees it.
const apiSnapshotPath = "testdata/api_surface.txt"

func TestPublicAPISurface(t *testing.T) {
	current := strings.Join(exportedSurface(t, "."), "\n") + "\n"

	if updateAPISnapshot() {
		if err := os.MkdirAll(filepath.Dir(apiSnapshotPath), 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(apiSnapshotPath, []byte(current), 0o644); err != nil {
			t.Fatalf("writing the snapshot: %v", err)
		}
		t.Logf("snapshot updated: %s", apiSnapshotPath)
		return
	}

	raw, err := os.ReadFile(apiSnapshotPath)
	if err != nil {
		t.Fatalf("reading %s: %v (set SCHEMAFLUX_UPDATE_API=1 to create it)", apiSnapshotPath, err)
	}

	if diff := surfaceDiff(strings.Split(strings.TrimSpace(string(raw)), "\n"),
		strings.Split(strings.TrimSpace(current), "\n")); diff != "" {
		t.Errorf("the exported API surface changed:\n%s\n\n"+
			"If this was intended, record it in TODOS.md and regenerate:\n"+
			"    SCHEMAFLUX_UPDATE_API=1 go test . -run TestPublicAPISurface", diff)
	}
}

func updateAPISnapshot() bool {
	return os.Getenv("SCHEMAFLUX_UPDATE_API") == "1"
}

// exportedSurface lists the exported declarations of a package, one per line,
// sorted. It reads the source rather than using reflection so it sees types and
// constants a running program would not expose.
func exportedSurface(t *testing.T, dir string) []string {
	t.Helper()

	// One file at a time rather than parser.ParseDir, which is deprecated for a
	// reason that matters here: it ignores build tags, so a file excluded from
	// the build would still contribute to the snapshot.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	var lines []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		if strings.HasSuffix(file.Name.Name, "_test") {
			continue
		}

		for _, decl := range file.Decls {
			lines = append(lines, exportedFromDecl(decl)...)
		}
	}

	sort.Strings(lines)
	return lines
}

func exportedFromDecl(decl ast.Decl) []string {
	var lines []string

	switch typed := decl.(type) {
	case *ast.FuncDecl:
		if !typed.Name.IsExported() {
			return nil
		}
		if typed.Recv != nil {
			receiver := receiverName(typed.Recv)
			if !ast.IsExported(strings.TrimPrefix(receiver, "*")) {
				return nil
			}
			return []string{"method " + receiver + "." + typed.Name.Name}
		}
		return []string{"func " + typed.Name.Name}

	case *ast.GenDecl:
		for _, spec := range typed.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.IsExported() {
					lines = append(lines, "type "+s.Name.Name)
				}
			case *ast.ValueSpec:
				for _, name := range s.Names {
					if !name.IsExported() {
						continue
					}
					kind := "var"
					if typed.Tok == token.CONST {
						kind = "const"
					}
					lines = append(lines, kind+" "+name.Name)
				}
			}
		}
	}

	return lines
}

func receiverName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	switch typed := recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if ident, ok := typed.X.(*ast.Ident); ok {
			return "*" + ident.Name
		}
		if index, ok := typed.X.(*ast.IndexExpr); ok {
			if ident, ok := index.X.(*ast.Ident); ok {
				return "*" + ident.Name
			}
		}
	case *ast.Ident:
		return typed.Name
	case *ast.IndexExpr:
		if ident, ok := typed.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

// surfaceDiff reports what was added and removed, which is the only part of a
// diff anybody reads on a sorted list.
func surfaceDiff(before, after []string) string {
	inBefore := map[string]bool{}
	for _, line := range before {
		inBefore[line] = true
	}
	inAfter := map[string]bool{}
	for _, line := range after {
		inAfter[line] = true
	}

	var added, removed []string
	for _, line := range after {
		if !inBefore[line] {
			added = append(added, "  + "+line)
		}
	}
	for _, line := range before {
		if !inAfter[line] {
			removed = append(removed, "  - "+line)
		}
	}

	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	return strings.Join(append(removed, added...), "\n")
}

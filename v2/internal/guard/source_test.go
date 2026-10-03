package guard_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	modulePath = "github.com/monstercameron/schemaflux"
	v2Path     = modulePath + "/v2"
)

// listedPackage is what `go list -json` says of one package.
type listedPackage struct {
	ImportPath string
	Dir        string
	Name       string
	Export     string
	GoFiles    []string
	Imports    []string
	Standard   bool
	Error      *struct{ Err string }
}

// source is every public package of version 2 that exists and builds, read two
// ways: its exported API from the compiler's export data (what a caller sees,
// with types) and its files as syntax (the doc comments).
type source struct {
	fset *token.FileSet
	// paths are the import paths of the public packages of v2 that were listed,
	// in order, whether or not they built.
	paths []string
	// broken maps a package that did not build to what go list said.
	broken map[string]string
	// pkgs holds the type information of each package that built.
	pkgs map[string]*types.Package
	// files holds the parsed non-test files of each package that built.
	files map[string][]*ast.File
	list  map[string]*listedPackage
}

var (
	sourceOnce sync.Once
	sourceData *source
	sourceErr  error
)

// loadSource lists the public packages of v2 and reads them. The tree is being
// written by several lanes at once, so a listing that reports a build error is
// repeated a few times before the packages that still fail are set aside.
func loadSource(t testing.TB) *source {
	t.Helper()
	sourceOnce.Do(func() {
		for attempt := 0; attempt < 4; attempt++ {
			sourceData, sourceErr = readSource()
			if sourceErr == nil && len(sourceData.broken) == 0 {
				return
			}
			time.Sleep(15 * time.Second)
		}
	})
	if sourceErr != nil {
		t.Fatalf("reading the packages of v2: %v", sourceErr)
	}
	return sourceData
}

func readSource() (*source, error) {
	cmd := exec.Command("go", "list", "-e", "-export", "-deps",
		"-json=ImportPath,Dir,Name,Export,GoFiles,Imports,Standard,Error", v2Path+"/...")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("go list: %v: %s", err, stderr.String())
	}
	s := &source{
		fset:   token.NewFileSet(),
		broken: map[string]string{},
		pkgs:   map[string]*types.Package{},
		files:  map[string][]*ast.File{},
		list:   map[string]*listedPackage{},
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		s.list[p.ImportPath] = &p
	}
	for path := range s.list {
		if path == v2Path || (strings.HasPrefix(path, v2Path+"/") && !strings.Contains(path, "/internal/")) {
			s.paths = append(s.paths, path)
		}
	}
	sort.Strings(s.paths)
	lookup := func(path string) (io.ReadCloser, error) {
		p := s.list[path]
		if p == nil || p.Export == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(p.Export)
	}
	imp := importer.ForCompiler(s.fset, "gc", lookup)
	for _, path := range s.paths {
		p := s.list[path]
		if p.Error != nil || p.Export == "" {
			msg := "no export data"
			if p.Error != nil {
				msg = p.Error.Err
			}
			s.broken[path] = msg
			continue
		}
		pkg, err := imp.Import(path)
		if err != nil {
			s.broken[path] = err.Error()
			continue
		}
		s.pkgs[path] = pkg
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(s.fset, filepath.Join(p.Dir, name), nil, parser.ParseComments)
			if err != nil {
				s.broken[path] = err.Error()
				delete(s.pkgs, path)
				break
			}
			s.files[path] = append(s.files[path], f)
		}
	}
	return s, nil
}

// short is a package's import path under v2: "" for the root.
func short(path string) string {
	if path == v2Path {
		return ""
	}
	return strings.TrimPrefix(path, v2Path+"/")
}

// label is how a failure message names a package: "v2" for the root.
func label(path string) string {
	if s := short(path); s != "" {
		return "v2/" + s
	}
	return "v2"
}

// requirePackage returns the type information of the package at v2/<name>, or
// skips the test naming the package when it does not exist or does not build.
func requirePackage(t *testing.T, s *source, name string) *types.Package {
	t.Helper()
	path := v2Path
	if name != "" {
		path += "/" + name
	}
	if pkg, ok := s.pkgs[path]; ok {
		return pkg
	}
	if why, ok := s.broken[path]; ok {
		t.Skipf("package %s does not build yet: %s", label(path), firstLine(why))
	}
	t.Skipf("package %s does not exist yet", label(path))
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

package guard_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// goPackage is what go list says of a package's imports.
type goPackage struct {
	ImportPath string
	GoFiles    []string
	Imports    []string
	Deps       []string
}

// listPackages lists the packages matching the patterns with their imports and
// their dependencies.
func listPackages(t *testing.T, patterns ...string) []goPackage {
	t.Helper()
	args := append([]string{"list", "-e", "-json=ImportPath,GoFiles,Imports,Deps"}, patterns...)
	cmd := exec.Command("go", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		t.Fatalf("go list: %v: %s", err, stderr.String())
	}
	var pkgs []goPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func TestPublicPackagesDoNotReachRetiredOrApplicationPackages(t *testing.T) {
	pkgs := listPackages(t, v2Path, v2Path+"/...")
	seen := map[string]bool{}
	for _, p := range pkgs {
		if seen[p.ImportPath] || len(p.GoFiles) == 0 || !publicPath(p.ImportPath) {
			continue
		}
		seen[p.ImportPath] = true
		t.Run(label(p.ImportPath), func(t *testing.T) {
			deps := map[string]bool{}
			for _, d := range p.Deps {
				deps[d] = true
			}
			for dep := range deps {
				for _, retired := range []string{"internal/api", "internal/tools", "examples", "cmd", "tests"} {
					path := modulePath + "/" + retired
					if dep == path || strings.HasPrefix(dep, path+"/") {
						t.Errorf("%s depends on %s", p.ImportPath, dep)
					}
				}
			}
		})
	}
}

func TestTheRootImportsNoSubpackageOfV2(t *testing.T) {
	for _, p := range listPackages(t, v2Path) {
		var bad []string
		for _, d := range p.Deps {
			if publicPath(d) && d != v2Path {
				bad = append(bad, d)
			}
		}
		for _, imp := range p.Imports {
			if publicPath(imp) && imp != v2Path {
				bad = append(bad, imp)
			}
		}
		sort.Strings(bad)
		if len(bad) > 0 {
			t.Errorf("the root of v2 imports %v: it is imported by every subpackage and cannot import any", bad)
		}
	}
}

func TestInternalBindImportsNothingPublic(t *testing.T) {
	for _, p := range listPackages(t, modulePath+"/internal/bind") {
		var bad []string
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, modulePath+"/") && !strings.HasPrefix(imp, modulePath+"/internal/") {
				bad = append(bad, imp)
			}
			if imp == modulePath {
				bad = append(bad, imp)
			}
		}
		if len(bad) > 0 {
			t.Errorf("internal/bind imports %v: it is below the root and names no public type", bad)
		}
	}
}

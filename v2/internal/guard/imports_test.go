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

// oldPublic are the packages of the module that version 2 must not reach: the
// old root, the old fake provider and the old builder packages.
var oldPublic = []string{
	modulePath,
	modulePath + "/schemafluxtest",
	modulePath + "/internal/api/client",
	modulePath + "/internal/api/fluent",
}

// wrapped are the four existing public packages version 2 wraps, and the one
// package of v2 allowed to import each. The OpenTelemetry observer wraps the
// existing one too.
var wrapped = map[string]string{
	modulePath + "/mw":             v2Path + "/mw",
	modulePath + "/pricing":        v2Path + "/pricing",
	modulePath + "/telemetry":      v2Path + "/telemetry",
	modulePath + "/telemetry/otel": v2Path + "/telemetry/otel",
	modulePath + "/codex":          v2Path + "/provider/codex",
}

// isModulePublic reports whether an import path is a package of this module
// outside internal and outside v2.
func isModulePublic(path string) bool {
	if path != modulePath && !strings.HasPrefix(path, modulePath+"/") {
		return false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")
	return !(rest == "v2" || strings.HasPrefix(rest, "v2/") || rest == "internal" || strings.HasPrefix(rest, "internal/"))
}

func TestNoPackageOfV2ReachesTheOldRoot(t *testing.T) {
	pkgs := listPackages(t, v2Path, v2Path+"/...")
	seen := map[string]bool{}
	for _, p := range pkgs {
		if seen[p.ImportPath] || len(p.GoFiles) == 0 {
			continue
		}
		seen[p.ImportPath] = true
		t.Run(label(p.ImportPath), func(t *testing.T) {
			deps := map[string]bool{}
			for _, d := range p.Deps {
				deps[d] = true
			}
			for _, bad := range oldPublic {
				if deps[bad] {
					t.Errorf("%s depends on %s: version 2 must not reach the old root, the old fake provider or the old builder packages", p.ImportPath, bad)
				}
			}
			for _, imp := range p.Imports {
				if !isModulePublic(imp) {
					continue
				}
				if owner, ok := wrapped[imp]; !ok || owner != p.ImportPath {
					t.Errorf("%s imports %s, an old public package that only one of the four wrapped packages' wrappers may import", p.ImportPath, imp)
				}
			}
		})
	}
}

func TestTheRootImportsNoSubpackageOfV2(t *testing.T) {
	for _, p := range listPackages(t, v2Path) {
		var bad []string
		for _, d := range p.Deps {
			if strings.HasPrefix(d, v2Path+"/") && !strings.Contains(d, "/internal/") {
				bad = append(bad, d)
			}
		}
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, v2Path+"/") {
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

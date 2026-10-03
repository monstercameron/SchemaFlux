package flow_test

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/monstercameron/schemaflux"

// deps lists the packages a package imports, directly or not, with the go command.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	list := strings.Fields(string(out))
	if len(list) < 5 {
		t.Fatalf("the list of dependencies of %s looks wrong: %v", pkg, list)
	}
	return list
}

// TestTheFlowPackageImportsTheRootAndInternalPackagesOnly holds the direct
// imports to the root of version 2 and the internal packages. What the internal
// packages import in turn is theirs: internal/ops still reaches the existing
// pricing and telemetry packages, the one edge from internal to public code,
// and that is not the flow package's import.
func TestTheFlowPackageImportsTheRootAndInternalPackagesOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the imports with the go command")
	}
	out, err := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}} {{end}}", modulePath+"/v2/flow").Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	imports := strings.Fields(string(out))
	if len(imports) < 3 {
		t.Fatalf("the list of imports looks wrong: %v", imports)
	}
	for _, dep := range imports {
		switch {
		case !strings.HasPrefix(dep, modulePath):
			// The standard library.
		case dep == modulePath+"/v2", strings.HasPrefix(dep, modulePath+"/internal/"):
		default:
			t.Errorf("the flow package imports %s, which is neither the root of version 2 nor an internal package", dep)
		}
	}
}

// TestNeitherPackageDependsOnTheOldRoot holds both packages, with everything they
// reach, clear of the old root, the old builders and the unfinished tree.
func TestNeitherPackageDependsOnTheOldRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	for _, pkg := range []string{modulePath + "/v2/flow", modulePath + "/v2"} {
		t.Run(pkg, func(t *testing.T) {
			for _, dep := range deps(t, pkg) {
				switch {
				case dep == modulePath:
					t.Errorf("%s depends on the old root", pkg)
				case strings.HasPrefix(dep, modulePath+"/internal/operations"):
					t.Errorf("%s depends on the unfinished operations tree: %s", pkg, dep)
				case strings.HasPrefix(dep, modulePath+"/internal/api"), strings.HasPrefix(dep, modulePath+"/internal/tools"):
					t.Errorf("%s depends on the old builders: %s", pkg, dep)
				case dep == modulePath+"/schemafluxtest", strings.HasPrefix(dep, modulePath+"/cmd"), strings.HasPrefix(dep, modulePath+"/examples"):
					t.Errorf("%s depends on %s", pkg, dep)
				}
			}
		})
	}
}

func TestTheRootImportsNoSubpackageOfVersionTwo(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	for _, dep := range deps(t, modulePath+"/v2") {
		if strings.HasPrefix(dep, modulePath+"/v2/") {
			t.Errorf("the root imports %s", dep)
		}
	}
}

func TestTheFlowPackageImportsTheRootAndTheInternalPackageItWraps(t *testing.T) {
	have := map[string]bool{}
	for _, dep := range deps(t, modulePath+"/v2/flow") {
		have[dep] = true
	}
	for _, want := range []string{modulePath + "/v2", modulePath + "/internal/ops", modulePath + "/internal/bind"} {
		if !have[want] {
			t.Errorf("the flow package does not depend on %s", want)
		}
	}
}

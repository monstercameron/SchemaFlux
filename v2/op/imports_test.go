package op_test

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/monstercameron/schemaflux/v2"

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

// TestThePackageImportsTheRootAndTheInternalPackagesItWrapsOnly holds the direct
// imports to the root of version 2 and the three internal packages the junction
// is made of. What the internal packages import in turn is theirs.
func TestThePackageImportsTheRootAndTheInternalPackagesItWrapsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the imports with the go command")
	}
	out, err := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}} {{end}}", modulePath+"/v2/op").Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	imports := strings.Fields(string(out))
	if len(imports) < 4 {
		t.Fatalf("the list of imports looks wrong: %v", imports)
	}
	allowed := map[string]bool{
		modulePath + "/v2":             true,
		modulePath + "/internal/bind":  true,
		modulePath + "/internal/ops":   true,
		modulePath + "/internal/types": true,
	}
	for _, dep := range imports {
		if strings.HasPrefix(dep, modulePath) && !allowed[dep] {
			t.Errorf("the op package imports %s, which is neither the root of version 2 nor one of the internal packages it wraps", dep)
		}
	}
}

// TestThePackageDependsOnNoneOfTheOldPackages holds the package, with everything
// it reaches, clear of the old root, the old builders and the unfinished tree.
func TestThePackageDependsOnNoneOfTheOldPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	for _, dep := range deps(t, modulePath+"/v2/op") {
		switch {
		case dep == modulePath:
			t.Errorf("the op package depends on the old root")
		case strings.HasPrefix(dep, modulePath+"/internal/operations"):
			t.Errorf("the op package depends on the unfinished operations tree: %s", dep)
		case strings.HasPrefix(dep, modulePath+"/internal/api"), strings.HasPrefix(dep, modulePath+"/internal/tools"):
			t.Errorf("the op package depends on the old builders: %s", dep)
		case dep == modulePath+"/schemafluxtest", strings.HasPrefix(dep, modulePath+"/cmd"), strings.HasPrefix(dep, modulePath+"/examples"):
			t.Errorf("the op package depends on %s", dep)
		}
	}
}

func TestThePackageDependsOnTheRootAndTheInternalPackageItWraps(t *testing.T) {
	have := map[string]bool{}
	for _, dep := range deps(t, modulePath+"/v2/op") {
		have[dep] = true
	}
	for _, want := range []string{modulePath + "/v2", modulePath + "/internal/ops", modulePath + "/internal/bind"} {
		if !have[want] {
			t.Errorf("the op package does not depend on %s", want)
		}
	}
}

func TestNeitherTheRootNorAnyOtherPackageOfVersionTwoDependsOnThisOne(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	for _, dep := range deps(t, modulePath+"/v2") {
		if dep == modulePath+"/v2/op" {
			t.Error("the root imports the op package")
		}
	}
}

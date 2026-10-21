package redact_test

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/monstercameron/schemaflux/v2"

// TestThePackageImportsTheRootAndInternalPackagesOnly holds the direct imports to
// the root of version 2 and the internal packages: the package keeps its own
// conversions out, because each one is reached through internal/bind.
func TestThePackageImportsTheRootAndInternalPackagesOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the imports with the go command")
	}
	out, err := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}} {{end}}", modulePath+"/v2/redact").Output()
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
			t.Errorf("the package imports %s, which is neither the root of version 2 nor an internal package", dep)
		}
	}
}

// TestThePackageDependsOnNothingOfTheOldRoot holds the package, with everything it
// reaches, clear of the old root, the old builders and the unfinished tree.
func TestThePackageDependsOnNothingOfTheOldRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	out, err := exec.Command("go", "list", "-deps", modulePath+"/v2/redact").Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	list := strings.Fields(string(out))
	if len(list) < 5 {
		t.Fatalf("the list of dependencies looks wrong: %v", list)
	}
	for _, dep := range list {
		if dep == modulePath || strings.HasPrefix(dep, modulePath+"/internal/api") || strings.HasPrefix(dep, modulePath+"/internal/tools") ||
			strings.HasPrefix(dep, modulePath+"/internal/operations") || strings.HasPrefix(dep, modulePath+"/schemafluxtest") {
			t.Errorf("the package depends on %s", dep)
		}
	}
}

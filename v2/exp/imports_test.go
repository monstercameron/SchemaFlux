package exp_test

import (
	"os/exec"
	"strings"
	"testing"
)

const expModulePath = "github.com/monstercameron/schemaflux"

func TestExpPackageDoesNotImportOldRootOrBuilders(t *testing.T) {
	if testing.Short() {
		t.Skip("lists package dependencies with the go command")
	}
	out, err := exec.Command("go", "list", "-deps", expModulePath+"/v2/exp").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == expModulePath || strings.HasPrefix(dep, expModulePath+"/internal/operations") ||
			strings.HasPrefix(dep, expModulePath+"/internal/api") || strings.HasPrefix(dep, expModulePath+"/internal/tools") ||
			dep == expModulePath+"/schemafluxtest" {
			t.Errorf("exp package depends on forbidden package %s", dep)
		}
	}
}

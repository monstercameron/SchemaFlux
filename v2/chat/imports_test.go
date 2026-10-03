package chat_test

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

// TestTheChatPackageImportsTheRootAndInternalPackagesOnly holds the direct
// imports to the root of version 2 and the internal packages: a conversation
// reaches the library through the same call path as an operation, and never
// through a sibling package such as mw or schemafluxtest, which are the
// caller's to put on the client.
func TestTheChatPackageImportsTheRootAndInternalPackagesOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the imports with the go command")
	}
	out, err := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}} {{end}}", modulePath+"/v2/chat").Output()
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
			t.Errorf("the chat package imports %s, which is neither the root of version 2 nor an internal package", dep)
		}
	}
}

// TestTheChatPackageDependsOnNeitherTheOldRootNorTheOldBuilders holds the package,
// with everything it reaches, clear of the old root, the old builders and the
// unfinished tree.
func TestTheChatPackageDependsOnNeitherTheOldRootNorTheOldBuilders(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	for _, dep := range deps(t, modulePath+"/v2/chat") {
		switch {
		case dep == modulePath:
			t.Errorf("the chat package depends on the old root")
		case strings.HasPrefix(dep, modulePath+"/internal/operations"):
			t.Errorf("the chat package depends on the unfinished operations tree: %s", dep)
		case strings.HasPrefix(dep, modulePath+"/internal/api"), strings.HasPrefix(dep, modulePath+"/internal/tools"):
			t.Errorf("the chat package depends on the old builders: %s", dep)
		case dep == modulePath+"/schemafluxtest", strings.HasPrefix(dep, modulePath+"/cmd"), strings.HasPrefix(dep, modulePath+"/examples"):
			t.Errorf("the chat package depends on %s", dep)
		}
	}
}

func TestTheChatPackageIsWrittenOverTheCallPathOfTheOperations(t *testing.T) {
	have := map[string]bool{}
	for _, dep := range deps(t, modulePath+"/v2/chat") {
		have[dep] = true
	}
	for _, want := range []string{modulePath + "/v2", modulePath + "/internal/ops", modulePath + "/internal/bind"} {
		if !have[want] {
			t.Errorf("the chat package does not depend on %s", want)
		}
	}
}

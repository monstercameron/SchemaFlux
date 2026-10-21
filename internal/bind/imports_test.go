package bind

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/monstercameron/schemaflux/v2"

// TestBindImportsNothingPublicAndNothingUnderV2 holds the layer this package
// is in: it is imported by the root and by every subpackage of v2, so it may
// not import any of them, and what it imports directly is internal.
func TestBindImportsNothingPublicAndNothingUnderV2(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	direct, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	for _, path := range strings.Fields(string(direct)) {
		if strings.HasPrefix(path, modulePath) && !strings.HasPrefix(path, modulePath+"/internal/") {
			t.Errorf("bind imports %s, which is a public package of the module", path)
		}
	}

	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("the list of dependencies looks wrong: %v", deps)
	}
	forbidden := []struct {
		name   string
		exact  string
		prefix string
	}{
		{name: "the old root", exact: modulePath},
		{name: "the root of version 2", prefix: modulePath + "/v2"},
		{name: "the fluent builders", prefix: modulePath + "/internal/api"},
		{name: "the tools package", prefix: modulePath + "/internal/tools"},
		{name: "the unfinished operations tree", prefix: modulePath + "/internal/operations"},
		{name: "the command line programs", prefix: modulePath + "/cmd"},
		{name: "the examples", prefix: modulePath + "/examples"},
		{name: "the old testing package", prefix: modulePath + "/schemafluxtest"},
	}
	for _, dep := range deps {
		for _, rule := range forbidden {
			if (rule.exact != "" && dep == rule.exact) || (rule.prefix != "" && strings.HasPrefix(dep, rule.prefix)) {
				t.Errorf("bind depends on %s, %s", dep, rule.name)
			}
		}
	}
	have := map[string]bool{}
	for _, dep := range deps {
		have[dep] = true
	}
	for _, want := range []string{modulePath + "/internal/types", modulePath + "/internal/llm"} {
		if !have[want] {
			t.Errorf("the list has no %s, so the check is not looking at the package", want)
		}
	}
}

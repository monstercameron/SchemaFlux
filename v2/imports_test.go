package schemaflux

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/monstercameron/schemaflux"

func TestThePackageImportsNothingItMustNot(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	forbidden := []struct {
		name   string
		exact  string
		prefix string
	}{
		{name: "the old root", exact: modulePath},
		{name: "the fluent builders", prefix: modulePath + "/internal/api"},
		{name: "the tools package", prefix: modulePath + "/internal/tools"},
		{name: "the old testing package", prefix: modulePath + "/schemafluxtest"},
		{name: "the unfinished operations tree", prefix: modulePath + "/internal/operations"},
		{name: "the command line programs", prefix: modulePath + "/cmd"},
		{name: "the examples", prefix: modulePath + "/examples"},
	}
	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("the list of dependencies looks wrong: %v", deps)
	}
	for _, dep := range deps {
		for _, rule := range forbidden {
			if (rule.exact != "" && dep == rule.exact) || (rule.prefix != "" && strings.HasPrefix(dep, rule.prefix)) {
				t.Errorf("the package depends on %s, %s", dep, rule.name)
			}
		}
	}
	t.Run("the list does contain what it should", func(t *testing.T) {
		have := map[string]bool{}
		for _, dep := range deps {
			have[dep] = true
		}
		for _, want := range []string{modulePath + "/internal/types", modulePath + "/internal/llm", modulePath + "/internal/ops"} {
			if !have[want] {
				t.Errorf("the list has no %s", want)
			}
		}
	})
}

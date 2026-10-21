package telemetry_test

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/monstercameron/schemaflux/v2"

// TestThePackageImportsNothingItMustNot holds the import rules of version 2:
// nothing under v2 imports the old root, the fluent builders, the tools package
// or the old testing package, and this package, which a program links to export
// what the library does, links no tracing vendor.
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
		{name: "the fluent builders", prefix: modulePath + "/internal/api"},
		{name: "the tools package", prefix: modulePath + "/internal/tools"},
		{name: "the old testing package", prefix: modulePath + "/schemafluxtest"},
		{name: "the unfinished operations tree", prefix: modulePath + "/internal/operations"},
		{name: "a tracing vendor", prefix: "go.opentelemetry.io"},
		{name: "gRPC or protobuf", prefix: "google.golang.org"},
	}
	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("the list of dependencies looks wrong: %v", deps)
	}
	have := map[string]bool{}
	for _, dep := range deps {
		have[dep] = true
		for _, rule := range forbidden {
			if (rule.exact != "" && dep == rule.exact) || (rule.prefix != "" && strings.HasPrefix(dep, rule.prefix)) {
				t.Errorf("the package depends on %s, %s", dep, rule.name)
			}
		}
	}
	for _, want := range []string{modulePath + "/telemetry", modulePath, modulePath + "/internal/runtime/telemetry"} {
		if !have[want] {
			t.Errorf("the dependencies do not include %s, which this package wraps", want)
		}
	}
}

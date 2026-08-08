package internal_test

import (
	"os/exec"
	"strings"
	"testing"
)

// OB-001's Verify line: "core's dependency list is asserted by a test; the
// library initializes no global SDK and closes nothing it did not create."
//
// The library used to import OpenTelemetry throughout internal/, and
// telemetry.InitTracing built exporters, chose an endpoint and a sampler, and
// called otel.SetTracerProvider. Two consequences: every consumer of this
// library compiled and linked the vendor whether or not they used it, and a
// host that had already configured its own telemetry lost silently to
// whichever initialiser ran last.
//
// The rule now: an adapter may depend on a vendor, the library a consumer
// imports may not. This test is the only thing keeping that true, because the
// violation is one import line away at all times and compiles perfectly.

// linkedPackages returns the transitive imports of a package -- what somebody
// importing it actually links.
//
// `go list -deps` rather than a source walk, because the question is not "does
// this directory mention the vendor" but "does a consumer end up with it", and
// only the compiler's own view answers that.
func linkedPackages(t *testing.T, pkg string) []string {
	t.Helper()

	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}

	var packages []string
	for _, line := range strings.Split(string(out), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			packages = append(packages, trimmed)
		}
	}
	return packages
}

// forbidden are import prefixes a consumer of this library must not be made to
// link. Each carries its reason, because a list of banned strings with no
// reasons is a list somebody eventually deletes an entry from.
var forbidden = []struct {
	prefix string
	reason string
}{
	{
		prefix: "go.opentelemetry.io/",
		reason: "the library emits through telemetry.Observer; the OpenTelemetry adapter is telemetry/otel and uses the host's provider (OB-001, ARC-17, ARC-18)",
	},
	{
		prefix: "github.com/mattn/go-sqlite3",
		reason: "a pricing or cache store is an optional adapter, not something every consumer links (OB-001)",
	},
}

func TestImportingTheLibraryDoesNotLinkATelemetryVendor(t *testing.T) {
	// The packages a consumer actually imports.
	for _, pkg := range []string{
		"github.com/monstercameron/schemaflux",
		"github.com/monstercameron/schemaflux/mw",
		"github.com/monstercameron/schemaflux/schemafluxtest",
		"github.com/monstercameron/schemaflux/pricing",
		"github.com/monstercameron/schemaflux/telemetry",
	} {
		t.Run(pkg, func(t *testing.T) {
			linked := linkedPackages(t, pkg)
			if len(linked) < 10 {
				t.Fatalf("only %d packages listed; go list is not seeing the tree", len(linked))
			}

			for _, dep := range linked {
				for _, rule := range forbidden {
					if strings.HasPrefix(dep, rule.prefix) {
						t.Errorf("importing %s links %s\n  %s", pkg, dep, rule.reason)
					}
				}
			}
		})
	}
}

// The adapter must still depend on the vendor it adapts. Without this, the
// rule above would also be satisfied by dropping OpenTelemetry support
// altogether, which is a different change wearing this one's name.
func TestTheAdapterStillLinksTheVendorItAdapts(t *testing.T) {
	linked := linkedPackages(t, "github.com/monstercameron/schemaflux/telemetry/otel")

	found := false
	for _, dep := range linked {
		if strings.HasPrefix(dep, "go.opentelemetry.io/") {
			found = true
			break
		}
	}
	if !found {
		t.Error("telemetry/otel does not link OpenTelemetry, so the core rule is vacuous")
	}
}

package requestid_test

import (
	"context"
	"go/build"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/requestid"
)

const module = "github.com/monstercameron/schemaflux"

func TestThePackageImportsNeitherTheV2RootNorTheOldRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Skipf("go list is not available here: %v", err)
	}
	self := module + "/v2/requestid"
	var found bool
	for _, path := range strings.Fields(string(out)) {
		switch {
		case path == self:
			found = true
		case path == module, path == module+"/v2":
			t.Errorf("the package depends on the root %s", path)
		case path == module+"/pricing", path == module+"/telemetry":
			// The one edge from internal code to public code: internal/ops, which
			// the call path's error converter belongs to, still reaches the
			// existing pricing and telemetry packages. It is internal/bind's import
			// and not this package's.
		case strings.HasPrefix(path, module+"/") && !strings.HasPrefix(path, module+"/internal/"):
			t.Errorf("the package depends on %s, which is public code of the module", path)
		}
	}
	if !found {
		t.Errorf("go list -deps did not list %s: %s", self, out)
	}
}

// TestThePackageImportsOnlyInternalCodeFromTheModule holds the direct imports
// to the tracking code, which it configures, and the three internal packages that
// let it return the library's own error without naming the root: the converter
// the root registers (bind), the internal error it converts (types) and the rule
// for putting a setting back (setting).
func TestThePackageImportsOnlyInternalCodeFromTheModule(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		module + "/internal/requesttracking", module + "/internal/bind", module + "/internal/types", module + "/internal/setting",
	}
	for _, path := range pkg.Imports {
		if strings.HasPrefix(path, module) && !slices.Contains(allowed, path) {
			t.Errorf("the package imports %s", path)
		}
	}
	if !slices.Contains(pkg.Imports, module+"/internal/requesttracking") {
		t.Errorf("the package does not import internal/requesttracking: %v", pkg.Imports)
	}
}

func TestThePackageDoesNotImportOsSoItCannotReadTheEnvironment(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range pkg.Imports {
		if path == "os" || path == "syscall" || path == module+"/internal/config" {
			t.Errorf("the package imports %s", path)
		}
	}
}

// hostile are the five variables that configured request tracking before v2,
// set to values that would change every behaviour if anything read them.
var hostile = map[string]string{
	"SCHEMAFLUX_REQUEST_TRACKING":        "false",
	"SCHEMAFLUX_REQUEST_ID_STRATEGY":     "none",
	"SCHEMAFLUX_CORRELATION_ID_STRATEGY": "none",
	"SCHEMAFLUX_REQUEST_ID_HEADER":       "X-Hostile-Request",
	"SCHEMAFLUX_CORRELATION_ID_HEADER":   "X-Hostile-Correlation",
}

// childVariable marks the process that TestAFreshProcessIgnoresTheEnvironment
// starts: the same test binary, run for one test only.
const childVariable = "REQUESTID_TEST_CHILD"

// TestAFreshProcessIgnoresTheEnvironment runs the check in a new process, with
// the hostile variables set from the start. The tests of this package call
// Configure, and once anything has called it the tracking code stops reading the
// environment whether or not this package asked it to, so only a process in
// which nothing has called it shows what linking the package does.
func TestAFreshProcessIgnoresTheEnvironment(t *testing.T) {
	if os.Getenv(childVariable) == "1" {
		checkHostileEnvironmentChangesNothing(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAFreshProcessIgnoresTheEnvironment$", "-test.count=1")
	cmd.Env = append(os.Environ(), childVariable+"=1")
	for name, value := range hostile {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the fresh process failed: %v (output follows)\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("the fresh process did not report a pass (output follows)\n%s", out)
	}
}

func checkHostileEnvironmentChangesNothing(t *testing.T) {
	t.Helper()
	for name, value := range hostile {
		if os.Getenv(name) != value {
			t.Fatalf("%s is %q in the fresh process, want the hostile value", name, os.Getenv(name))
		}
	}
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Fatalf("the configuration with hostile variables set is %+v, want the default %+v", got, defaultInternal)
	}
	ids := made(context.Background())
	if !autoForm.MatchString(ids.RequestID) || ids.CorrelationID != ids.RequestID {
		t.Errorf("ids with hostile variables set: %+v", ids)
	}
	h := http.Header{}
	requestid.Inject(withIDs("r", "c"), h)
	assertHeader(t, h, http.Header{"X-Request-Id": {"r"}, "X-Correlation-Id": {"c"}})
	hostileHeaders := http.Header{"X-Hostile-Request": {"hr"}, "X-Hostile-Correlation": {"hc"}, "X-Request-Id": {"r2"}}
	if got := requestid.From(requestid.Extract(context.Background(), hostileHeaders)); got != (requestid.IDs{Request: "r2"}) {
		t.Errorf("Extract with hostile variables set gave %+v, want only the default header read", got)
	}
}

func TestTheEnvironmentDoesNotConfigureAnything(t *testing.T) {
	for name, value := range hostile {
		t.Setenv(name, value)
	}
	// A program that changes a variable while it runs can ask the tracking code
	// to read the environment again. It must make no difference.
	config.ReloadEnvironment()
	t.Cleanup(config.ReloadEnvironment)

	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Fatalf("the configuration with hostile variables set is %+v, want the default %+v", got, defaultInternal)
	}

	ids := made(context.Background())
	if !autoForm.MatchString(ids.RequestID) || ids.CorrelationID != ids.RequestID {
		t.Errorf("ids with hostile variables set: %+v", ids)
	}

	h := http.Header{}
	requestid.Inject(withIDs("r", "c"), h)
	assertHeader(t, h, http.Header{"X-Request-Id": {"r"}, "X-Correlation-Id": {"c"}})

	hostileHeaders := http.Header{"X-Hostile-Request": {"hr"}, "X-Hostile-Correlation": {"hc"}, "X-Request-Id": {"r2"}}
	if got := requestid.From(requestid.Extract(context.Background(), hostileHeaders)); got != (requestid.IDs{Request: "r2"}) {
		t.Errorf("Extract with hostile variables set gave %+v, want only the default header read", got)
	}
}

func TestTheEnvironmentDoesNotChangeWhatRestoreRestores(t *testing.T) {
	restore := mustConfigure(t, requestid.Config{Header: "X-Mine"})
	for name, value := range hostile {
		t.Setenv(name, value)
	}
	config.ReloadEnvironment()
	t.Cleanup(config.ReloadEnvironment)
	restore()
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("after restore with hostile variables set the configuration is %+v, want the default", got)
	}
}

func TestVariablesThatAreNotEvenValuesChangeNothingEither(t *testing.T) {
	for name := range hostile {
		t.Setenv(name, " not a value \xff\n"+strings.Repeat("z", 5000))
	}
	config.ReloadEnvironment()
	t.Cleanup(config.ReloadEnvironment)
	if got := requesttracking.GetConfig(); got != defaultInternal {
		t.Errorf("the configuration with garbage variables set is %+v", got)
	}
}

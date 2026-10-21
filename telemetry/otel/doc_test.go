package otel

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// packageDoc returns the text of this package's documentation comment.
func packageDoc(t *testing.T) string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		t.Fatalf("parsing doc.go: %v", err)
	}
	if parsed.Doc == nil {
		t.Fatal("doc.go has no package comment")
	}
	return parsed.Doc.Text()
}

// squash collapses every run of white space to one space, so a phrase wrapped
// across two lines of a comment is still found.
func squash(text string) string { return strings.Join(strings.Fields(text), " ") }

// errorTypeValues is every value error.type can take, generated from the
// library's kinds: each kind's name. A kind added to the library appears here
// without anyone remembering to add it.
func errorTypeValues() []string {
	var values []string
	for kind := schemaflux.ErrConfiguration; kind.Error() != "unknown"; kind++ {
		values = append(values, kind.Error())
	}
	slices.Sort(values)
	return slices.Compact(values)
}

// The package comment is where an engineer learns what error.type can be, so it
// lists every value, and it cannot go stale: this fails with the block to paste
// when a kind is added, renamed or removed.
func TestThePackageCommentListsEveryErrorTypeTheLibraryCanReport(t *testing.T) {
	doc := packageDoc(t)

	_, afterIntro, found := strings.Cut(doc, "on a span and on a retry event,\nis:\n")
	if !found {
		t.Fatal("the package comment no longer introduces the list of error.type values")
	}
	var listed []string
	for _, line := range strings.Split(afterIntro, "\n") {
		if !strings.HasPrefix(line, "\t") {
			if len(listed) > 0 {
				break
			}
			continue
		}
		listed = append(listed, strings.TrimSpace(line))
	}

	want := errorTypeValues()
	if !slices.Equal(listed, want) {
		block := "//\t" + strings.Join(want, "\n//\t")
		t.Errorf("the error.type values in the package comment are\n  %s\nwant, from the library's kinds,\n  %s\nreplace the list with:\n%s",
			strings.Join(listed, "\n  "), strings.Join(want, "\n  "), block)
	}
	if len(want) < 18 {
		t.Errorf("only %d error.type values were generated; the enumeration of the kinds stopped early", len(want))
	}
}

// Every attribute key the Observer writes, and every event it adds, is named in
// the package comment: a key added to the code and not to the comment fails.
func TestThePackageCommentNamesEveryAttributeAndEventTheObserverWrites(t *testing.T) {
	doc := squash(packageDoc(t))
	observer, exporter := observerOn(t)

	ctx := observer.Started(context.Background(), fullEvent)
	observer.Noted(ctx, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 1, Kind: schemaflux.ErrTimeout, Wait: time.Second})
	observer.Noted(ctx, telemetry.Note{Name: telemetry.NoteHedge, Attempt: 2})
	result := fullResult
	observer.Finished(ctx, result)
	operation := observer.Started(context.Background(), telemetry.Event{Scope: telemetry.ScopeOperation, Operation: "extract"})
	observer.Finished(operation, telemetry.Result{Scope: telemetry.ScopeOperation, Kind: schemaflux.ErrInternal})
	run := observer.Started(context.Background(), telemetry.Event{Scope: telemetry.ScopeOperation, Operation: "extract"})
	observer.Finished(run, fullResultAsRun)
	cached := observer.Started(context.Background(), fullEvent)
	observer.Finished(cached, telemetry.Result{Cached: true})
	// A success, so the keys that only a success carries are written too.
	success := observer.Started(context.Background(), fullEvent)
	ok := fullResult
	ok.Kind = 0
	observer.Finished(success, ok)
	unpriced := observer.Started(context.Background(), fullEvent)
	ok.Cost = schemaflux.Cost{Currency: "USD"}
	observer.Finished(unpriced, ok)

	keys := map[string]bool{}
	events := map[string]bool{}
	for _, span := range exporter.GetSpans() {
		for _, kv := range span.Attributes {
			keys[string(kv.Key)] = true
		}
		for _, event := range span.Events {
			events[event.Name] = true
			for _, kv := range event.Attributes {
				keys[string(kv.Key)] = true
			}
		}
	}
	if len(keys) < 25 {
		t.Fatalf("only %d keys were written; the test is not looking at every one", len(keys))
	}
	for key := range keys {
		if !strings.Contains(doc, key) {
			t.Errorf("the package comment does not name the attribute %q", key)
		}
	}
	for event := range events {
		if !strings.Contains(doc, `"`+event+`"`) {
			t.Errorf("the package comment does not name the event %q", event)
		}
	}
	// A key the comment lists and the code never writes is as stale. The tables
	// of the comment are read: each row starts with the key.
	text := packageDoc(t)
	from := strings.Index(text, "On a request span, set when the span starts:")
	to := strings.Index(text, "On an operation span:")
	if from < 0 || to < from {
		t.Fatal("the package comment no longer has the tables of attributes")
	}
	rows := 0
	for _, line := range strings.Split(text[from:to], "\n") {
		if !strings.HasPrefix(line, "\t") {
			continue
		}
		rows++
		if key := strings.Fields(line)[0]; !keys[key] {
			t.Errorf("the package comment lists the attribute %q, which the Observer never writes", key)
		}
	}
	if rows < 25 {
		t.Errorf("only %d rows of the tables were read", rows)
	}
}

// fullResultAsRun is an operation's result with every field a run can set.
var fullResultAsRun = telemetry.Result{Scope: telemetry.ScopeOperation, Operation: "extract", Requests: 3, Items: 4, Succeeded: 2, Failed: 1, Cancelled: 1}

// What an engineer needs in order to filter, shut down and choose an id strategy
// is in the comment, with the values the code uses.
func TestThePackageCommentSaysWhatAnEngineerSettingUpNeeds(t *testing.T) {
	doc := squash(packageDoc(t))
	for _, tc := range []struct{ name, want string }{
		{"the instrumentation scope's name", instrumentationName},
		{"the shutdown order, restore first", "Shut down in the reverse order"},
		{"the provider's shutdown last", "then shut the provider down"},
		{"the request id strategy that reads the trace", "requestid.StrategyTrace"},
		{"the version of the convention", "v1.41.0"},
		{"that the keys are in development upstream", "in development"},
		{"the model bound", "128 characters for an id, a model"},
		{"what is never recorded", "What is never recorded"},
		{"the retry event", "schemaflux.backoff_ms"},
		{"the one call", "otel.Install(provider)"},
		{"the import path", "github.com/monstercameron/schemaflux/v2/telemetry/otel"},
		{"the name clash with OpenTelemetry's own package", "fluxotel"},
		{"a stream abandoned at its timeout", "ends at the stream's timeout"},
		{"which functions have an operation span", "each have an operation span of their own"},
		{"that nothing global is set", "never calls otel.SetTracerProvider"},
		{"that no exporter, gRPC or protobuf is linked", "no exporter, no gRPC and no protobuf"},
		{"what Install changes", "What Install changes, and how to undo it"},
		{"how to tell a repair", "schemaflux.repair"},
		{"the status on success", "left Unset"},
	} {
		if !strings.Contains(doc, squash(tc.want)) {
			t.Errorf("the package comment does not mention %s (%q)", tc.name, tc.want)
		}
	}
}

// The comment says the package names the v2 words: no name of the first
// version's taxonomy survives in it.
func TestThePackageCommentUsesVersion2sWords(t *testing.T) {
	doc := packageDoc(t)
	for _, stale := range []string{
		"AddObserver", "SetObserver", "ResultMetadata", "SCHEMAFLUX_", "InitTracing", "ShutdownTracing",
		"schemaflux.intelligence", "provider unavailable", "repair exhausted", "schema violation", "authentication\n",
		"types.ErrorKind", "OperationStarted", "OperationFinished",
	} {
		if strings.Contains(doc, stale) {
			t.Errorf("the package comment still says %q, a name of the first version", stale)
		}
	}
}

// sourceFiles returns the package's own Go files, tests excluded.
func sourceFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var source []string
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") {
			source = append(source, file)
		}
	}
	if len(source) < 2 {
		t.Fatalf("found %d source files: %v", len(source), source)
	}
	return source
}

// This package configures nothing of its host's: it reads no variable of the
// environment, sets no global tracer provider and no propagator, and builds no
// exporter. The code is read, not the comment, which says the same in words.
func TestThePackageReadsNoEnvironmentAndSetsNothingGlobal(t *testing.T) {
	forbidden := map[string]string{
		"Getenv": "reads a variable", "LookupEnv": "reads a variable", "Environ": "reads the environment", "ExpandEnv": "reads the environment",
		"SetTracerProvider": "sets the global tracer provider", "SetTextMapPropagator": "sets the global propagator",
		"SetLogger": "sets the global logger", "SetErrorHandler": "sets the global error handler",
		"GetTracerProvider": "falls back to the global tracer provider", "GetTextMapPropagator": "reads the global propagator",
	}
	for _, file := range sourceFiles(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if why, bad := forbidden[selector.Sel.Name]; bad {
					t.Errorf("%s: %s %s", file, selector.Sel.Name, why)
				}
			}
			return true
		})
		for _, spec := range parsed.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if strings.Contains(path, "exporters") || strings.Contains(path, "otlp") || strings.Contains(path, "grpc") || strings.HasPrefix(path, "go.opentelemetry.io/otel/sdk") {
				t.Errorf("%s imports %s, which a program that only passes in its provider must not link", file, path)
			}
		}
	}
	if _, err := os.Stat("tracing.go"); err == nil {
		t.Error("tracing.go exists: the library builds no tracing of its own")
	}
}

func TestThePackageLinksNoExporterNoGRPCAndNoProtobuf(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("the go command is not available here: %v", err)
	}
	deps := strings.Fields(string(out))
	have := map[string]bool{}
	for _, dep := range deps {
		have[dep] = true
		for _, bad := range []string{"google.golang.org", "go.opentelemetry.io/otel/exporters", "go.opentelemetry.io/otel/sdk", "go.opentelemetry.io/proto", "github.com/grpc", "golang.org/x/net/http2"} {
			if strings.HasPrefix(dep, bad) {
				t.Errorf("the package links %s", dep)
			}
		}
		for _, forbidden := range []string{
			modulePath + "/internal/tools", modulePath + "/schemafluxtest",
		} {
			if dep == forbidden {
				t.Errorf("the package depends on %s", dep)
			}
		}
		for _, prefix := range []string{modulePath + "/internal/api", modulePath + "/internal/operations"} {
			if strings.HasPrefix(dep, prefix) {
				t.Errorf("the package depends on %s", dep)
			}
		}
	}
	for _, want := range []string{"go.opentelemetry.io/otel/trace", modulePath + "/telemetry", modulePath} {
		if !have[want] {
			t.Errorf("the dependencies do not include %s", want)
		}
	}
}

const modulePath = "github.com/monstercameron/schemaflux/v2"

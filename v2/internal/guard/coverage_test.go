package guard_test

import (
	"go/types"
	"sort"
	"strings"
	"testing"
)

// notAnOperation names the exported functions and methods that take a context
// and are in neither table of this package, each with the reason it asks no
// model and so adds no Record. A key is "Name" or "Type.Method" in the root and
// "pkg.Name" or "pkg.Type.Method" elsewhere; "pkg.*" covers a whole package of
// infrastructure that table A3 says adds no Record whatever it holds.
//
// A function or method that takes no context.Context is not listed: it can ask
// no model, and the coverage test accepts it by that rule.
var notAnOperation = map[string]string{
	"WithClient":      "puts a client on a context; it asks no model (A3: adds no Record)",
	"WithRecorder":    "puts a recorder on a context; it asks no model (A3: adds no Record)",
	"Scheduler.Close": "stops a scheduler and waits for what is in flight; it asks no model",

	"op.WithParents": "records parent result ids on a context; it asks no model (A3: adds no Record)",

	"flow.MemoryStore.Load":   "an in-memory checkpoint store; it asks no model",
	"flow.MemoryStore.Save":   "an in-memory checkpoint store; it asks no model",
	"flow.MemoryStore.Delete": "an in-memory checkpoint store; it asks no model",

	"provider.*":       "everything in provider adds no Record (A3): it builds and registers providers, which are not operations",
	"provider/codex.*": "everything in provider/codex adds no Record (A3)",
	"mw.*":             "everything in mw adds no Record (A3): a middleware wraps a provider",
	"pricing.*":        "everything in pricing adds no Record (A3): it keeps the price table and the ledger",
	"telemetry.*":      "everything in telemetry adds no Record (A3)",
	"telemetry/otel.*": "everything in telemetry/otel adds no Record (A3)",
	"requestid.*":      "everything in requestid adds no Record (A3)",
	"schemafluxtest.*": "everything in schemafluxtest adds no Record (A3): it is the fake provider and its helpers",
}

// takesContext reports whether a function or method has a context.Context
// parameter.
func takesContext(sig *types.Signature) bool {
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if types.TypeString(params.At(i).Type(), nil) == "context.Context" {
			return true
		}
	}
	return false
}

// declared is every exported function and method of a package, by key, with
// whether it takes a context.
type declared struct {
	key  string
	ctx  bool
	kind string
}

func declaredIn(pkg *types.Package, name string) []declared {
	prefix := ""
	if name != "" {
		prefix = name + "."
	}
	var out []declared
	for _, n := range exportedNames(pkg) {
		switch o := pkg.Scope().Lookup(n).(type) {
		case *types.Func:
			out = append(out, declared{prefix + n, takesContext(o.Type().(*types.Signature)), "function"})
		case *types.TypeName:
			if o.IsAlias() {
				continue
			}
			named, ok := o.Type().(*types.Named)
			if !ok {
				continue
			}
			for i := 0; i < named.NumMethods(); i++ {
				m := named.Method(i)
				if m.Exported() {
					out = append(out, declared{prefix + n + "." + m.Name(), takesContext(m.Type().(*types.Signature)), "method"})
				}
			}
		}
	}
	return out
}

func TestEveryExportedFunctionIsInATable(t *testing.T) {
	s := loadSource(t)
	inTable := map[string]string{}
	for _, r := range allRows() {
		inTable[r.key()] = "the table of functions that ask a model"
	}
	for _, r := range plainRows() {
		inTable[r.key] = "the table of functions that add no Record"
	}
	seen := map[string]bool{}
	for _, path := range s.paths {
		name := short(path)
		t.Run(label(path), func(t *testing.T) {
			pkg := requirePackage(t, s, name)
			var missing []string
			for _, d := range declaredIn(pkg, name) {
				seen[d.key] = true
				if _, ok := inTable[d.key]; ok {
					continue
				}
				if _, ok := notAnOperation[d.key]; ok {
					continue
				}
				if _, ok := notAnOperation[wildcardOf(name)]; ok {
					continue
				}
				if !d.ctx {
					continue // it takes no context, so it asks no model
				}
				missing = append(missing, d.key+" ("+d.kind+")")
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				t.Errorf("exported and taking a context, yet in neither table nor in the list of names that are not an operation: add a row in rows_test.go or rows_other_test.go (it asks a model), in norecord_test.go (it does not), or a reason in coverage_test.go:\n  %s", strings.Join(missing, "\n  "))
			}
		})
	}
	// A row for a function that is no longer there is as wrong as a missing row.
	t.Run("rows name functions that exist", func(t *testing.T) {
		var stale []string
		for key := range inTable {
			pkgKey := packageOfKey(key)
			if _, ok := s.pkgs[v2Path+pkgKey]; !ok {
				continue // the package does not exist or does not build yet
			}
			if !seen[key] {
				stale = append(stale, key)
			}
		}
		sort.Strings(stale)
		if len(stale) > 0 {
			t.Errorf("rows for functions the packages do not declare: %v", stale)
		}
	})
}

func wildcardOf(pkg string) string {
	if pkg == "" {
		return "\x00"
	}
	return pkg + ".*"
}

// packageOfKey returns the path suffix ("" or "/exp") of the package a key
// belongs to. Keys are "Name", "Type.Method", "pkg.Name" and "pkg.Type.Method";
// a package is a known one when the first element names a package of v2.
func packageOfKey(key string) string {
	first, _, ok := strings.Cut(key, ".")
	if !ok {
		return ""
	}
	switch first {
	case "exp", "redact", "flow", "op", "chat", "provider", "mw", "pricing", "telemetry", "requestid", "schemafluxtest":
		return "/" + first
	}
	return "" // a method of a root type: "Client.Warm"
}

// TestEveryDetailTwinHasARowOfItsOwn holds the table to its own claim: a row that
// names a Detail twin names a function that has a row, whose call path is
// "detail" or "decision-detail" and whose Op is the plain form's with "detail"
// added (A3).
func TestEveryDetailTwinHasARowOfItsOwn(t *testing.T) {
	byName := map[string]*row{}
	rows := allRows()
	for i := range rows {
		byName[rows[i].key()] = &rows[i]
	}
	for i := range rows {
		r := &rows[i]
		if r.detail == "" {
			continue
		}
		twin, ok := byName[r.detail]
		if !ok {
			t.Errorf("%s names the Detail twin %s, which has no row", r.id(), r.detail)
			continue
		}
		if twin.path != "detail" && twin.path != "decision-detail" {
			t.Errorf("%s is the Detail twin of %s and has the call path %q", twin.id(), r.id(), twin.path)
		}
		if twin.op != r.op+"detail" {
			t.Errorf("%s is the Detail twin of %s: its Op is %q, want %q", twin.id(), r.id(), twin.op, r.op+"detail")
		}
	}
}

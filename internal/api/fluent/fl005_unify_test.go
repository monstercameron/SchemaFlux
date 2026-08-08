package fluent

import "testing"

// FL-005 / F-05: commonRequest, opRequest, and directRequest used to
// implement the same eleven methods three times because the Options types
// behind them access their settable fields three different ways. requestBase
// (fluent_base.go) collapses them to one generic struct parameterized by
// per-field setter closures, with every setter nil-guarded so a type that
// structurally cannot support one (most visibly, Threshold on ten of
// fluent_advanced.go's eleven request types -- see wiring_test.go's
// TestEveryBuilderWiresEverySetter for the full list) just no-ops that one
// method instead of needing a different base entirely.
//
// These are the regression tests that the unification actually preserved
// behaviour for a representative builder from each of the three retired
// bases, plus the one new capability it adds (AuditRequest.Threshold, which
// had nowhere to live under the old directRequest).

// A former commonRequest type: settable fields route through a nested
// ops.CommonOptions.
func TestFL005Unify_FormerCommonRequestType_ThresholdSets(t *testing.T) {
	built := Classifying[stubExtractTarget, string](stubExtractTarget{Name: "x"}).
		Categories("a", "b").
		Threshold(0.42)
	if built.opts.CommonOptions.Threshold != 0.42 {
		t.Errorf("Threshold = %v, want 0.42", built.opts.CommonOptions.Threshold)
	}
}

// A former opRequest type: settable fields are types.OpOptions fields
// directly, no CommonOptions wrapper.
func TestFL005Unify_FormerOpRequestType_RequestIDSets(t *testing.T) {
	built := Explaining("x").RequestID("req-42")
	if built.opts.OpOptions.RequestID != "req-42" {
		t.Errorf("RequestID = %q, want %q", built.opts.OpOptions.RequestID, "req-42")
	}
}

// A former directRequest type: settable fields are plain struct fields, no
// wrapper at all.
func TestFL005Unify_FormerDirectRequestType_CorrelationIDSets(t *testing.T) {
	built := Resolving([]stubExtractTarget{{Name: "a"}}).CorrelationID("corr-9")
	if built.opts.CorrelationID != "corr-9" {
		t.Errorf("CorrelationID = %q, want %q", built.opts.CorrelationID, "corr-9")
	}
}

// The new capability: AuditOptions has a Threshold field that directRequest
// never exposed a setter slot for, so .Threshold() on AuditRequest used to
// compile, chain, and change nothing (like every unwired setter -- see
// wiring_test.go's doc comment). requestBase's setThreshold closure for
// Audit (fluent_advanced.go) makes it real.
func TestFL005Unify_AuditRequest_ThresholdNowSets(t *testing.T) {
	built := Auditing[stubExtractTarget](stubExtractTarget{Name: "x"}).Threshold(0.75)
	if built.opts.Threshold != 0.75 {
		t.Errorf("Threshold = %v, want 0.75 -- AuditOptions.Threshold should now be reachable from the fluent builder", built.opts.Threshold)
	}
}

// The documented no-op: nine of the remaining ten direct-request types have
// no Threshold field at all (Compose is the tenth; both are exercised here).
// Calling .Threshold() must still compile and chain -- it just changes
// nothing, exactly like calling .Context() on a type with no Context setter
// already did before this task.
func TestFL005Unify_ResolveRequest_ThresholdIsANoOp(t *testing.T) {
	before := Resolving([]stubExtractTarget{{Name: "a"}})
	after := before.Threshold(0.9)
	// ResolveOptions has no Threshold field to compare against; the
	// assertion available from outside the package is that this did not
	// panic and produced a builder whose other fields are untouched.
	if after.opts.Strategy != before.opts.Strategy {
		t.Errorf("Threshold() on a type with no Threshold field mutated an unrelated field: Strategy changed from %q to %q", before.opts.Strategy, after.opts.Strategy)
	}
}

func TestFL005Unify_AssembleRequest_ThresholdIsANoOp(t *testing.T) {
	before := Assembling[stubExtractTarget]([]any{"part"})
	after := before.Threshold(0.9)
	if after.opts.Template != before.opts.Template {
		t.Errorf("Threshold() on a type with no Threshold field mutated an unrelated field: Template changed from %q to %q", before.opts.Template, after.opts.Template)
	}
}

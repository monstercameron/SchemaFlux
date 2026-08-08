package ops

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TestOpHoldsNoContextOrProvider is the reflection form of the Revised
// line's "it holds no context and no provider". It walks Op[string, string]
// (In/Out are arbitrary here; the shape being checked does not depend on
// them) and every field it contains, recursively through structs, pointers,
// slices, and function signatures, and fails if anything is or could hold a
// context.Context or an llm.Provider.
func TestOpHoldsNoContextOrProvider(t *testing.T) {
	violations := walkForContextOrProvider(reflect.TypeOf(Op[string, string]{}), "Op", map[reflect.Type]bool{})
	if len(violations) > 0 {
		var names []string
		for _, v := range violations {
			names = append(names, v.path+" ("+v.kind+")")
		}
		t.Fatalf("Op holds a context or a provider, which the design forbids: %s", strings.Join(names, ", "))
	}
}

// TestOpHoldsNoContextOrProviderAcrossInstantiations checks a second,
// differently-shaped instantiation, so the proof is not an artifact of one
// particular In/Out pair.
func TestOpHoldsNoContextOrProviderAcrossInstantiations(t *testing.T) {
	type wideOut struct {
		A, B, C string
		Nested  []int
	}
	violations := walkForContextOrProvider(reflect.TypeOf(Op[[]int, wideOut]{}), "Op", map[reflect.Type]bool{})
	if len(violations) > 0 {
		t.Fatalf("Op[[]int, wideOut] holds a context or a provider: %v", violations)
	}
}

// TestExternalConstructionAndInspection proves a caller can build an Op from
// outside this file, inspect every field the Revised line calls for, and get
// back exactly the values it set -- no hidden defaulting, no field that
// silently resets.
func TestExternalConstructionAndInspection(t *testing.T) {
	op := Op[string, int]{
		ID: types.OperationID{Name: "countWords", Version: "v3"},
		Semantics: types.Semantics{
			Category:           types.CategoryTransformation,
			InferencePermitted: false,
			EvidenceRequired:   true,
			PreservesIdentity:  false,
			PreservesOrder:     true,
			Stability:          types.StabilityExperimental,
		},
		Contract: OutputContract[int]{
			SchemaName: "int",
			Decode: func(body string) (int, error) {
				return len(strings.Fields(body)), nil
			},
			Invariants: []func(int) error{
				func(n int) error { return nil },
			},
		},
		Batch: BatchAlgebra[string, int]{Class: types.BatchNone},
		DefaultPolicy: types.DefaultPolicy{
			RepairAttempts: 2,
		},
		BuildPrompt: func(input string, opt types.OpOptions) (string, string) {
			return "count the words", input
		},
	}

	if op.ID.String() != "countWords@v3" {
		t.Fatalf("ID.String() = %q, want countWords@v3", op.ID.String())
	}
	if op.Semantics.Category != types.CategoryTransformation {
		t.Fatalf("Category = %v, want CategoryTransformation", op.Semantics.Category)
	}
	if !op.Semantics.EvidenceRequired {
		t.Fatal("EvidenceRequired did not round-trip")
	}
	if op.Semantics.Stability != types.StabilityExperimental {
		t.Fatalf("Stability = %v, want experimental", op.Semantics.Stability)
	}
	if op.DefaultPolicy.RepairAttempts != 2 {
		t.Fatalf("RepairAttempts = %d, want 2", op.DefaultPolicy.RepairAttempts)
	}
	if len(op.Contract.Invariants) != 1 {
		t.Fatalf("Invariants did not round-trip: got %d", len(op.Contract.Invariants))
	}

	n, err := op.Contract.Decode("three little words")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n != 3 {
		t.Fatalf("Decode() = %d, want 3", n)
	}

	system, user := op.BuildPrompt("hello world", types.OpOptions{})
	if system != "count the words" || user != "hello world" {
		t.Fatalf("BuildPrompt() = (%q, %q)", system, user)
	}
}

// TestExternalConstructionAndRun proves a caller can also run what it built,
// through RunOp, using the same provider-hook seam every operation in this
// package already uses.
func TestExternalConstructionAndRun(t *testing.T) {
	setLLMCaller(func(_ context.Context, system, user string, _ types.OpOptions) (string, error) {
		if system != "echo" {
			t.Fatalf("unexpected system prompt: %q", system)
		}
		return "GOT: " + user, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := Op[string, string]{
		ID:        types.OperationID{Name: "echo", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[string]{
			Decode: func(body string) (string, error) { return body, nil },
		},
		Batch: BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) {
			return "echo", input
		},
	}

	value, repair, err := RunOp(context.Background(), op, "hello", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if value != "GOT: hello" {
		t.Fatalf("RunOp() = %q, want %q", value, "GOT: hello")
	}
	if repair.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1", repair.Attempts)
	}
	if repair.Repaired {
		t.Fatal("Repaired = true on a first-try success")
	}
}

// TestRunOpRepairs proves the ported path still repairs: a first answer the
// contract rejects is followed by feeding the problem back and asking again,
// exactly as withRepair (repair.go) already did for every other operation.
func TestRunOpRepairs(t *testing.T) {
	calls := 0
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		calls++
		if calls == 1 {
			return "not a number", nil
		}
		if !strings.Contains(user, "previous answer could not be used") {
			t.Fatalf("repair prompt does not name the problem: %q", user)
		}
		return "42", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := Op[string, int]{
		ID:        types.OperationID{Name: "parseNumber", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[int]{
			Decode: func(body string) (int, error) {
				n := 0
				if _, err := fmt.Sscan(body, &n); err != nil {
					return 0, err
				}
				return n, nil
			},
		},
		Batch:         BatchAlgebra[string, int]{Class: types.BatchNone},
		DefaultPolicy: types.DefaultPolicy{RepairAttempts: 1},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) {
			return "parse", input
		},
	}

	value, repair, err := RunOp(context.Background(), op, "forty-two", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if value != 42 {
		t.Fatalf("RunOp() = %d, want 42", value)
	}
	if !repair.Repaired || repair.Attempts != 2 {
		t.Fatalf("repair result = %+v, want a repaired, 2-attempt result", repair)
	}
}

// TestNewOpRejectsStableWithoutBatchClass is the non-panicking half of the
// Revised line's added Verify: a stable operation with no declared batch
// class fails, reported as an ordinary error a caller can handle.
func TestNewOpRejectsStableWithoutBatchClass(t *testing.T) {
	_, err := NewOp(Op[string, string]{
		ID:        types.OperationID{Name: "broken", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityStable},
		// Batch.Class left at types.BatchUnspecified.
	})
	if err == nil {
		t.Fatal("NewOp accepted a stable op with no batch class")
	}
	if !strings.Contains(err.Error(), "batch class") {
		t.Fatalf("error does not name the problem: %v", err)
	}
}

// TestNewOpAcceptsStableWithBatchClass is the companion case: the same op,
// with a batch class declared, is accepted.
func TestNewOpAcceptsStableWithBatchClass(t *testing.T) {
	op, err := NewOp(Op[string, string]{
		ID:        types.OperationID{Name: "fixed", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityStable},
		Batch:     BatchAlgebra[string, string]{Class: types.BatchNone},
	})
	if err != nil {
		t.Fatalf("NewOp rejected a stable op that declared BatchNone: %v", err)
	}
	if op.ID.Name != "fixed" {
		t.Fatalf("NewOp did not return the op: %+v", op)
	}
}

// TestNewOpAcceptsExperimentalWithoutBatchClass: the check is specific to
// Stable, not a blanket requirement -- an experimental operation's shape is
// still moving and is allowed to leave the batch class undeclared.
func TestNewOpAcceptsExperimentalWithoutBatchClass(t *testing.T) {
	_, err := NewOp(Op[string, string]{
		ID:        types.OperationID{Name: "inProgress", Version: "v0"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
	})
	if err != nil {
		t.Fatalf("NewOp rejected an experimental op for missing a batch class: %v", err)
	}
}

// TestStableExtractOpFailsInitWithoutBatchClass reproduces, directly and
// under the test's own control, the panic extractOpMeta's init would hit if
// its batch class were ever removed -- proving the build-time mechanism
// works, independent of extractOpMeta's own correctness.
func TestStableExtractOpFailsInitWithoutBatchClass(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustNewOp did not panic on a stable op with no batch class")
		}
		if !strings.Contains(fmt.Sprint(r), "batch class") {
			t.Fatalf("panic value does not name the problem: %v", r)
		}
	}()

	MustNewOp(Op[any, any]{
		ID:        types.OperationID{Name: "extract", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityStable},
		// Batch.Class deliberately left unset, reproducing the regression
		// the mechanism exists to catch.
	})
}

// TestAllRegisteredStableOpsDeclareBatchClass is the registry-side half of
// the same check: every operation that registered itself as stable
// (opextract.go's init, and any future one) must have done so with a real
// batch class. NewOp/MustNewOp already make this true by construction for
// anything built through them; this test is what fails if a future
// registerStableOp call is added without going through NewOp first.
func TestAllRegisteredStableOpsDeclareBatchClass(t *testing.T) {
	stableOpsMu.Lock()
	defer stableOpsMu.Unlock()

	if len(stableOps) == 0 {
		t.Fatal("no stable ops are registered; extractOpMeta's init should have registered one")
	}
	for _, entry := range stableOps {
		if entry.batch == types.BatchUnspecified {
			t.Fatalf("registered stable op %s has no declared batch class", entry.id)
		}
	}
}

// TestRunOpRequiresBuildPrompt and TestRunOpRequiresDecode check RunOp's own
// input validation, since a nil function field compiles and would otherwise
// only be discovered as a nil-pointer panic at call time.
func TestRunOpRequiresBuildPrompt(t *testing.T) {
	op := Op[string, string]{
		Contract: OutputContract[string]{Decode: func(body string) (string, error) { return body, nil }},
	}
	_, _, err := RunOp(context.Background(), op, "x", types.OpOptions{})
	if err == nil {
		t.Fatal("RunOp accepted an Op with no BuildPrompt")
	}
}

func TestRunOpRequiresDecode(t *testing.T) {
	op := Op[string, string]{
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) { return "s", "u" },
	}
	_, _, err := RunOp(context.Background(), op, "x", types.OpOptions{})
	if err == nil {
		t.Fatal("RunOp accepted an Op with no Decode")
	}
}

// TestOperationIDString covers the identity's rendering, since it is what
// every log line and cache key would show.
func TestOperationIDString(t *testing.T) {
	cases := []struct {
		id   types.OperationID
		want string
	}{
		{types.OperationID{Name: "extract", Version: "v1"}, "extract@v1"},
		{types.OperationID{Name: "extract"}, "extract@unversioned"},
		{types.OperationID{Version: "v1"}, "unnamed@v1"},
	}
	for _, c := range cases {
		if got := c.id.String(); got != c.want {
			t.Fatalf("OperationID%+v.String() = %q, want %q", c.id, got, c.want)
		}
	}
}

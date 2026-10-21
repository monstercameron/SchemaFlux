package schemafluxtest_test

import (
	"context"
	"os"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// None of these tests is parallel: Install changes the default client, which is
// the process's, and each puts it back.

// A call that names no client runs on the default. There is no v2 operation to
// make it with yet, so the test calls an internal one, which reads the same
// default.
func callOnTheDefault(t *testing.T) (bool, error) {
	t.Helper()
	return ops.Is(context.Background(), "a subject", "Is this a question?", types.OpOptions{})
}

func TestInstallMakesTheFakeTheProviderOfTheDefaultClient(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"q1":true}`)
	schemafluxtest.Install(t, &model)

	if got := schemaflux.Default().Provider; got != schemaflux.Provider(&model) {
		t.Fatalf("the default's provider is %v, want the fake", got)
	}
	answer, err := callOnTheDefault(t)
	if err != nil || !answer {
		t.Fatalf("a call with no client = %v, %v", answer, err)
	}
	if model.Calls() != 1 {
		t.Errorf("the fake received %d requests, want 1", model.Calls())
	}
	if got := model.Last().Model; got != "test-model" {
		t.Errorf("the request asked for %q: the fake's model for a tier was not used", got)
	}
}

func TestInstallPutsTheDefaultBackWhenTheTestEnds(t *testing.T) {
	kept := &schemaflux.Budget{Limit: 3}
	previous := schemaflux.Default()
	schemaflux.SetDefault(schemaflux.Client{Model: "kept-model", Budget: kept, Policy: &schemaflux.DataPolicy{Providers: []string{"x"}}})
	t.Cleanup(func() { schemaflux.SetDefault(previous) })

	var model schemafluxtest.Provider
	t.Run("a test that installs", func(t *testing.T) {
		schemafluxtest.Install(t, &model)
		inside := schemaflux.Default()
		if inside.Model != "" || inside.Budget != nil || inside.Policy != nil {
			t.Errorf("Install kept settings of the old default: %+v", inside)
		}
	})
	after := schemaflux.Default()
	if after.Model != "kept-model" || after.Budget != kept || after.Policy == nil || after.Policy.Providers[0] != "x" || after.Provider != nil {
		t.Errorf("the default after the test is %+v, want what it was", after)
	}
}

func TestInstallTwiceInOneTestLetsTheLastWinAndUndoesInOrder(t *testing.T) {
	var first, second schemafluxtest.Provider
	first.Reply(`{"q1":true}`)
	second.Reply(`{"q1":false}`)
	before := schemaflux.Default().Provider
	t.Run("two installs", func(t *testing.T) {
		schemafluxtest.Install(t, &first)
		schemafluxtest.Install(t, &second)
		if answer, err := callOnTheDefault(t); err != nil || answer {
			t.Errorf("the call answered %v, %v, want the second fake's false", answer, err)
		}
		if first.Calls() != 0 || second.Calls() != 1 {
			t.Errorf("calls: first %d, second %d", first.Calls(), second.Calls())
		}
	})
	if got := schemaflux.Default().Provider; got != before {
		t.Errorf("the default's provider after both are undone is %v, want what it was", got)
	}
}

func TestInstallSetsAVariableForTheLengthOfTheTestSoAParallelTestIsRefused(t *testing.T) {
	const name = "SCHEMAFLUX_TEST_INSTALLED"
	if os.Getenv(name) != "" {
		t.Skip("the variable is set outside the test")
	}
	var model schemafluxtest.Provider
	t.Run("inside", func(t *testing.T) {
		schemafluxtest.Install(t, &model)
		if os.Getenv(name) != "1" {
			t.Errorf("%s is %q inside the test", name, os.Getenv(name))
		}
	})
	if got := os.Getenv(name); got != "" {
		t.Errorf("%s is still %q after the test", name, got)
	}
}

func TestInstallInAParallelTestPanicsAndChangesNothing(t *testing.T) {
	var model schemafluxtest.Provider
	before := schemaflux.Default()
	// A parallel subtest runs once the body of this test has returned, and its
	// result is checked when this test's cleanups run, which is after it.
	t.Cleanup(func() {
		if after := schemaflux.Default(); after.Provider != before.Provider {
			t.Errorf("a refused Install changed the default: %+v", after)
		}
	})
	t.Run("parallel", func(t *testing.T) {
		t.Parallel()
		defer func() {
			r := recover()
			if r == nil {
				t.Error("Install did not panic in a parallel test")
				return
			}
			if !strings.Contains(strings.ToLower(asText(r)), "parallel") {
				t.Errorf("the panic does not say why: %v", r)
			}
		}()
		schemafluxtest.Install(t, &model)
	})
}

func asText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	}
	return ""
}

// failingTB is a testing.TB whose Fatal is recorded and does not stop the test:
// the one thing the nil-provider test needs.
type failingTB struct {
	testing.TB
	fatal   string
	cleanup []func()
	env     map[string]string
}

func (f *failingTB) Helper() {}
func (f *failingTB) Fatal(args ...any) {
	f.fatal = args[0].(string)
	panic(f)
}
func (f *failingTB) Setenv(key, value string) { f.env = map[string]string{key: value} }
func (f *failingTB) Cleanup(fn func())        { f.cleanup = append(f.cleanup, fn) }

func TestInstallOfNilFailsTheTestAndInstallsNothing(t *testing.T) {
	before := schemaflux.Default()
	f := &failingTB{TB: t}
	func() {
		defer func() {
			if r := recover(); r != f {
				t.Fatalf("recovered %v", r)
			}
		}()
		schemafluxtest.Install(f, nil)
	}()
	if !strings.Contains(f.fatal, "nil") {
		t.Errorf("the test was failed with %q, want a message that says the provider is nil", f.fatal)
	}
	if len(f.cleanup) != 0 || len(f.env) != 0 {
		t.Error("a refused Install registered a cleanup or set a variable")
	}
	if after := schemaflux.Default(); after.Provider != before.Provider {
		t.Error("a refused Install changed the default")
	}
}

func TestInstallRegistersOneCleanupAndSetsOneVariable(t *testing.T) {
	before := schemaflux.Default()
	f := &failingTB{TB: t}
	var model schemafluxtest.Provider
	schemafluxtest.Install(f, &model)
	if len(f.cleanup) != 1 || len(f.env) != 1 {
		t.Fatalf("cleanups %d, variables %d, want one of each", len(f.cleanup), len(f.env))
	}
	if schemaflux.Default().Provider != schemaflux.Provider(&model) {
		t.Error("the fake is not installed")
	}
	f.cleanup[0]()
	if after := schemaflux.Default(); after.Provider != before.Provider {
		t.Error("the cleanup did not put the default back")
	}
}

func TestInstallOfAProviderWrittenOutsideTheLibraryWorks(t *testing.T) {
	own := &ownProvider{}
	schemafluxtest.Install(t, own)
	if _, err := ops.Is(context.Background(), "s", "q?", types.OpOptions{Model: "named"}); err != nil {
		t.Fatal(err)
	}
	if own.requests != 1 {
		t.Errorf("the provider received %d requests", own.requests)
	}
}

// ownProvider is a Provider that is none of the library's.
type ownProvider struct{ requests int }

func (p *ownProvider) Name() string { return "own" }
func (p *ownProvider) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	p.requests++
	return schemaflux.Response{Content: `{"q1":true}`}, nil
}

func TestInstallLeavesTheDefaultUsableByCallsThatCarryAClient(t *testing.T) {
	var installed, carried schemafluxtest.Provider
	installed.Reply(`{"q1":true}`)
	carried.Reply(`{"q1":true}`)
	schemafluxtest.Install(t, &installed)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &carried})
	if _, err := ops.Is(ctx, "s", "q?", types.OpOptions{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if carried.Calls() != 1 || installed.Calls() != 0 {
		t.Errorf("calls: carried %d, installed %d: a client on the context was not used", carried.Calls(), installed.Calls())
	}
}

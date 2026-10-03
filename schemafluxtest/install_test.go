package schemafluxtest_test

import (
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// installRecorder is the minimal TestingT double schemafluxtest.Install needs.
// It never stops the goroutine the way *testing.T's Fatalf does, so a test can
// observe that Fatalf was called instead of the process dying, and it keeps the
// cleanups Install registers so a test can run them when it chooses.
type installRecorder struct {
	setenv   []struct{ key, value string }
	fatal    string
	cleanups []func()
}

func (r *installRecorder) Helper()                {}
func (r *installRecorder) Cleanup(cleanup func()) { r.cleanups = append(r.cleanups, cleanup) }
func (r *installRecorder) Setenv(key, value string) {
	r.setenv = append(r.setenv, struct{ key, value string }{key, value})
}
func (r *installRecorder) Fatalf(format string, args ...any) {
	r.fatal = strings.TrimSpace(fmt.Sprintf(format, args...))
}

func (r *installRecorder) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

// Install restores whatever was there before, so a test does not leak its
// double into the next one.
func TestInstallRestoresThePreviousClient(t *testing.T) {
	before := schemaflux.GetDefaultClient()

	func() {
		provider := schemafluxtest.New()
		restore := schemafluxtest.Install(t, provider)
		defer restore()

		if schemaflux.GetDefaultClient() == before {
			t.Error("Install did not replace the client")
		}
	}()

	if schemaflux.GetDefaultClient() != before {
		t.Error("the previous client was not restored")
	}
}

// Install refuses a nil provider rather than installing an unusable one and
// leaving every subsequent call in the test to fail somewhere less obvious.
func TestInstallRefusesANilProvider(t *testing.T) {
	recorder := &installRecorder{}
	restore := schemafluxtest.Install(recorder, nil)
	defer restore()

	if recorder.fatal == "" {
		t.Fatal("Install(t, nil) did not call Fatalf")
	}
	if !strings.Contains(recorder.fatal, "New()") {
		t.Errorf("the refusal does not point at the fix: %q", recorder.fatal)
	}
	if len(recorder.cleanups) != 0 {
		t.Error("a refused Install registered a cleanup")
	}
}

// Install sets the environment key through t.Setenv, which is what makes Go
// refuse t.Parallel in the same test.
func TestInstallSetsTheKeyThroughSetenv(t *testing.T) {
	recorder := &installRecorder{}
	defer schemafluxtest.Install(recorder, schemafluxtest.New())()

	if len(recorder.setenv) != 1 || recorder.setenv[0].key != "SCHEMAFLUX_API_KEY" {
		t.Errorf("Setenv calls = %+v, want one for SCHEMAFLUX_API_KEY", recorder.setenv)
	}
	if len(recorder.cleanups) != 1 {
		t.Errorf("registered %d cleanups, want 1", len(recorder.cleanups))
	}
}

// When there was no previous client, restoring sets the default back to nil
// rather than to whatever Install last saw -- a test running first in a
// process with no client configured must not leave one installed after it
// ends.
func TestInstallWithNoPreviousClientRestoresToNil(t *testing.T) {
	before := schemaflux.GetDefaultClient()
	schemaflux.SetDefaultClient(nil)
	defer schemaflux.SetDefaultClient(before)

	func() {
		provider := schemafluxtest.New()
		restore := schemafluxtest.Install(t, provider)
		defer restore()

		if schemaflux.GetDefaultClient() == nil {
			t.Fatal("Install did not set a client")
		}
	}()

	if schemaflux.GetDefaultClient() != nil {
		t.Error("restoring after a nil previous client should leave it nil")
	}
}

// The operations read a provider that is separate from the default client, and
// setting a client only repoints it when the client carries a provider. A
// previous client with none left the double installed there after the restore,
// so the next test talked to it.
func TestInstallRestoreDoesNotLeaveTheDoubleAsTheProvider(t *testing.T) {
	const good = `{"queue":"billing","priority":"low"}`

	saved := schemaflux.GetDefaultClient()
	t.Cleanup(func() {
		ops.SetDefaultProvider(nil)
		schemaflux.SetDefaultClient(saved)
	})

	type previousClient struct {
		name string
		// build installs the state that is current before Install runs and
		// returns the provider that ought to answer afterwards, or nil when
		// nothing should.
		build func() *schemafluxtest.Provider
	}
	previousClients := []previousClient{
		{"no default client", func() *schemafluxtest.Provider {
			schemaflux.SetDefaultClient(nil)
			return nil
		}},
		{"a client with no provider", func() *schemafluxtest.Provider {
			schemaflux.SetDefaultClient(schemaflux.NewClient(""))
			return nil
		}},
		{"a client holding its own provider", func() *schemafluxtest.Provider {
			own := schemafluxtest.New().Reply(good)
			schemaflux.SetDefaultClient(schemaflux.NewClient("").WithProviderInstance(own))
			return own
		}},
	}

	modes := []struct {
		name string
		run  func(restore func(), recorder *installRecorder)
	}{
		{"the returned function", func(restore func(), _ *installRecorder) { restore() }},
		{"the registered cleanup", func(_ func(), recorder *installRecorder) { recorder.runCleanups() }},
		{"both, one after the other", func(restore func(), recorder *installRecorder) {
			restore()
			recorder.runCleanups()
		}},
	}

	for _, previous := range previousClients {
		for _, mode := range modes {
			t.Run(previous.name+" restored by "+mode.name, func(t *testing.T) {
				ops.SetDefaultProvider(nil)
				own := previous.build()

				recorder := &installRecorder{}
				double := schemafluxtest.New().Reply(good)
				restore := schemafluxtest.Install(recorder, double)

				if _, err := schemaflux.Extract[triage]("while installed", schemaflux.NewExtractOptions()); err != nil {
					t.Fatalf("the installed double did not answer: %v", err)
				}
				if double.CallCount() != 1 {
					t.Fatalf("double calls while installed = %d, want 1", double.CallCount())
				}

				mode.run(restore, recorder)

				_, err := schemaflux.Extract[triage]("after the restore", schemaflux.NewExtractOptions())
				if double.CallCount() != 1 {
					t.Errorf("the double answered after it was restored away: %d calls", double.CallCount())
				}
				if own == nil {
					if err == nil {
						t.Error("an operation succeeded after the restore with no provider to answer it")
					}
					return
				}
				if err != nil {
					t.Errorf("the previous client's provider did not answer: %v", err)
				}
				if own.CallCount() != 1 {
					t.Errorf("previous provider calls = %d, want 1", own.CallCount())
				}
			})
		}
	}

	t.Run("nested installs unwind in order", func(t *testing.T) {
		ops.SetDefaultProvider(nil)
		schemaflux.SetDefaultClient(schemaflux.NewClient(""))

		outerRecorder, innerRecorder := &installRecorder{}, &installRecorder{}
		outer := schemafluxtest.New().Reply(good)
		inner := schemafluxtest.New().Reply(good)
		restoreOuter := schemafluxtest.Install(outerRecorder, outer)
		restoreInner := schemafluxtest.Install(innerRecorder, inner)

		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err != nil || inner.CallCount() != 1 || outer.CallCount() != 0 {
			t.Fatalf("the inner double should answer: err=%v inner=%d outer=%d", err, inner.CallCount(), outer.CallCount())
		}
		restoreInner()
		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err != nil || outer.CallCount() != 1 {
			t.Fatalf("the outer double should answer after the inner restore: err=%v outer=%d", err, outer.CallCount())
		}
		restoreOuter()
		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
			t.Error("a double still answers after both restores")
		}
		if inner.CallCount() != 1 || outer.CallCount() != 1 {
			t.Errorf("calls after both restores: inner=%d outer=%d, want 1 and 1", inner.CallCount(), outer.CallCount())
		}
	})

	t.Run("installing again after a restore", func(t *testing.T) {
		ops.SetDefaultProvider(nil)
		schemaflux.SetDefaultClient(schemaflux.NewClient(""))

		for round := 1; round <= 3; round++ {
			recorder := &installRecorder{}
			double := schemafluxtest.New().Reply(good)
			restore := schemafluxtest.Install(recorder, double)
			if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
			restore()
			if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
				t.Errorf("round %d: the double still answers after its restore", round)
			}
		}
	})
}

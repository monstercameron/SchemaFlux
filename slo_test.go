package schemaflux_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// OB-004. The operational SLOs, each as a named test, so "the library does not
// leak goroutines" stops being a belief and becomes something that fails a
// build when it stops being true.
//
// Each test below is named for the SLO it holds and says what it does not
// cover. An SLO test that quietly checks something narrower than its name is
// worse than no test, because the name is what people rely on.

// --- SLO: zero panics from valid API use.

func TestSLONoPanicsFromValidAPIUse(t *testing.T) {
	type person struct {
		Name string `json:"name"`
	}

	// Valid use, and deliberately awkward: empty inputs, single-element
	// collections, unusual runes, and a provider answering with a shape that
	// does not fit. None of these is a caller error, and none may panic.
	cases := []struct {
		name  string
		reply string
		run   func() error
	}{
		{"extract from an empty string", `{"name":"x"}`, func() error {
			_, err := schemaflux.Extract[person]("", schemaflux.NewExtractOptions())
			return err
		}},
		{"extract with an unusable answer", `not json`, func() error {
			_, err := schemaflux.Extract[person]("something", schemaflux.NewExtractOptions())
			return err
		}},
		{"filter a single item", `{"ids":["i-000001"]}`, func() error {
			_, err := schemaflux.Filter([]string{"only"}, schemaflux.NewFilterOptions().WithCriteria("any"))
			return err
		}},
		{"filter an empty collection", `{"ids":[]}`, func() error {
			_, err := schemaflux.Filter([]string{}, schemaflux.NewFilterOptions().WithCriteria("any"))
			return err
		}},
		{"sort a single item", `{"ids":["i-000001"]}`, func() error {
			_, err := schemaflux.Sort([]string{"only"}, schemaflux.NewSortOptions().WithCriteria("any"))
			return err
		}},
		{"summarize combining marks and RTL", `{"text":"ok","key_points":[],"confidence":0.5}`, func() error {
			_, err := schemaflux.Summarize("é́ مرحبا 👨‍👩‍👧‍👦", schemaflux.NewSummarizeOptions())
			return err
		}},
		{"classify with an answer outside the set", `{"category":"nope","confidence":0.9}`, func() error {
			_, err := schemaflux.Classify[string, string]("input", schemaflux.NewClassifyOptions().WithCategories([]string{"a", "b"}))
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("panicked on valid use: %v", recovered)
				}
			}()

			defer schemafluxtest.Install(t, schemafluxtest.New().Reply(tc.reply))()
			// The error is not the point. Not panicking is.
			_ = tc.run()
		})
	}
}

// --- SLO: zero goroutine leaks after cancel or completion.

// settledGoroutines waits for the runtime to quiesce and reports the count.
// Without the settle loop this measures scheduler noise rather than leaks: a
// goroutine that is on its way out has not necessarily gone when the call that
// spawned it returns.
func settledGoroutines() int {
	previous := -1
	for i := 0; i < 40; i++ {
		runtime.GC()
		current := runtime.NumGoroutine()
		if current == previous {
			return current
		}
		previous = current
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

func TestSLONoGoroutineLeaksAfterCompletion(t *testing.T) {
	defer schemafluxtest.Install(t, schemafluxtest.New().Reply(`{"ids":["i-000001","i-000002"]}`))()

	before := settledGoroutines()

	for i := 0; i < 20; i++ {
		_, _ = schemaflux.Filter([]string{"a", "b"}, schemaflux.NewFilterOptions().WithCriteria("any"))
	}

	after := settledGoroutines()
	// A small allowance for runtime-owned goroutines that appear on first use;
	// a leak shows up as growth proportional to the loop, not as two.
	if after > before+2 {
		t.Errorf("goroutines went from %d to %d over 20 completed operations", before, after)
	}
}

func TestSLONoGoroutineLeaksAfterCancel(t *testing.T) {
	// Slow enough that every operation is cancelled mid-flight rather than
	// finishing before the cancel is noticed. The fake's own Slow mode exists
	// for this, so there is no second provider double here to keep in step.
	defer schemafluxtest.Install(t, schemafluxtest.New().Slow(2*time.Second).Reply("{}"))()

	before := settledGoroutines()

	for i := 0; i < 20; i++ {
		// A deadline that expires *during* the call, not before it: cancelling
		// up front means the operation returns without ever starting, and a
		// leak test on work that never began proves nothing.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)

		opts := schemaflux.NewFilterOptions().WithCriteria("any")
		opts.CommonOptions.Context = ctx
		_, err := schemaflux.Filter([]string{"a", "b"}, opts)
		cancel()

		if err == nil {
			t.Fatal("an operation against a provider slower than its deadline succeeded")
		}
	}

	after := settledGoroutines()
	if after > before+2 {
		t.Errorf("goroutines went from %d to %d over 20 cancelled operations", before, after)
	}
}

// --- SLO: zero unknown or duplicate batch IDs accepted.

func TestSLONoUnknownOrDuplicateBatchIDsAccepted(t *testing.T) {
	items := []string{"first", "second", "third"}

	cases := []struct {
		name  string
		reply string
	}{
		{"duplicate id", `{"ids":["i-000001","i-000001"]}`},
		{"unknown id", `{"ids":["i-000009"]}`},
		{"id from another invocation", `{"ids":["x-000001"]}`},
		{"malformed id", `{"ids":["1"]}`},
	}

	for _, tc := range cases {
		t.Run("filter/"+tc.name, func(t *testing.T) {
			defer schemafluxtest.Install(t, schemafluxtest.New().Reply(tc.reply))()

			if _, err := schemaflux.Filter(items, schemaflux.NewFilterOptions().WithCriteria("any")); err == nil {
				t.Errorf("%s was accepted", tc.name)
			}
		})
	}

	// A sort must additionally cover every id exactly once, so a subset is a
	// failure there even though it is legitimate for a filter.
	t.Run("sort/incomplete coverage", func(t *testing.T) {
		defer schemafluxtest.Install(t, schemafluxtest.New().Reply(`{"ids":["i-000001"]}`))()

		if _, err := schemaflux.Sort(items, schemaflux.NewSortOptions().WithCriteria("any")); err == nil {
			t.Error("a sort returning one of three ids was accepted")
		}
	})
}

// --- SLO: zero secrets in logs.

func TestSLONoSecretsInLogs(t *testing.T) {
	const secret = "sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // secret-scan: allow

	// Capture is process-wide state, and other tests in this package turn it
	// off. Configure it here rather than assuming it: the first version of
	// this test depended on the ambient setting and failed in the full-package
	// run for a reason that had nothing to do with secrets.
	schemaflux.ConfigureLogging(schemaflux.LoggerConfig{
		Level:         schemaflux.LogDebug,
		Capture:       true,
		BufferSize:    256,
		DisableStderr: true,
	})
	schemaflux.ResetLogEntries()

	defer schemafluxtest.Install(t, schemafluxtest.New().Reply("not a usable answer"))()

	type person struct {
		Name string `json:"name"`
	}
	// The failure path is the one that logs, so drive an operation that fails
	// with the secret in its input.
	_, _ = schemaflux.Extract[person]("my key is "+secret, schemaflux.NewExtractOptions())

	entries := schemaflux.GetLogEntries()
	if len(entries) == 0 {
		t.Fatal("no log entries were captured at all, so this test witnesses nothing")
	}

	for _, entry := range entries {
		rendered := entry.Message
		for key, value := range entry.Attributes {
			rendered += " " + key + "=" + fmt.Sprint(value)
		}

		if strings.Contains(rendered, secret) {
			t.Fatalf("a credential from the caller's input reached a log line: %s", rendered)
		}
		if strings.Contains(rendered, "sk-proj-") {
			t.Fatalf("something credential-shaped reached a log line: %s", rendered)
		}
	}
}

// --- SLO: zero calls exceeding a declared budget.
//
// Covered at the middleware seam by mw/budget_test.go's
// TestBudgetRefusesBeforeCallingTheProviderWhenEstimateExceedsLimit and
// TestBudgetConcurrentCallsCannotBothPassAnExceedingCheck, which assert the
// refusal happens *before* the provider is called and that two goroutines
// cannot both pass a check that together exceeds the ceiling. Named here so
// the SLO list is complete rather than duplicated.

// --- SLO: zero client-isolation failures.
//
// Covered by client_isolation_test.go's
// TestClientsBuiltInSequenceDoNotRepointEachOther and
// TestClientsWithDifferentProvidersRunConcurrentlyWithoutInterference.

// --- SLO: validated-item completeness at or above 99.5% on the conformance
// corpus.
//
// NOT COVERED. There is no conformance corpus in this repository — RC-001 is
// the task that builds one, and it is open. Writing a completeness figure
// against an ad-hoc set of inputs invented here would produce a number that
// looks like the SLO and measures something else, which is worse than the
// absence. This comment is the honest placeholder.

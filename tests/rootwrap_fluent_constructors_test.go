package tests

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// This file covers the remaining fluent request constructors ("-ing" style)
// and the compact ChooseBy/FilterBy/SortBy entrypoints -- each a one-line
// forward into internal/api/fluent. Every case proves the constructor reaches
// a real provider call through the right op, the same way
// TestModelPinReachesTheProviderFromEveryBuilderShape (devx_test.go) already
// proves forwarding for the earlier batch of builders: schemafluxtest's
// Shaped() answers whatever shape the request declares, so every operation
// gets past its own decode without per-operation scripting, and CallCount()
// proves the call actually left the wrapper rather than short-circuiting
// somewhere before it.

type fcDoc struct {
	Title string `json:"title"`
}

func TestFluentConstructorsReachTheProvider(t *testing.T) {
	cases := []struct {
		name string
		run  func(ctx context.Context)
	}{
		{"Diffing", func(ctx context.Context) {
			// Diff requires struct types (compareData refuses scalars), so a
			// bare string pair errors before any provider call -- structs are
			// what actually exercises this path.
			_, _ = schemaflux.Diffing(fcDoc{Title: "old"}, fcDoc{Title: "new"}).Run(ctx)
		}},
		{"Suggesting", func(ctx context.Context) {
			_, _ = schemaflux.Suggesting[fcDoc]("context").Run(ctx)
		}},
		{"LLMRedacting", func(ctx context.Context) {
			_, _ = schemaflux.LLMRedacting("contact me at a@b.com").Run(ctx)
		}},
		{"Completing", func(ctx context.Context) {
			_, _ = schemaflux.Completing("a partial sentence").Run(ctx)
		}},
		{"CompletingField", func(ctx context.Context) {
			_, _ = schemaflux.CompletingField(fcDoc{Title: "x"}, "Title").Run(ctx)
		}},
		{"Validating", func(ctx context.Context) {
			_, _ = schemaflux.Validating(fcDoc{Title: "x"}).Rules("title must be present").Run(ctx)
		}},
		{"Annotating", func(ctx context.Context) {
			_, _ = schemaflux.Annotating("some document text").Run(ctx)
		}},
		{"Clustering", func(ctx context.Context) {
			_, _ = schemaflux.Clustering([]string{"a", "b", "c"}).Run(ctx)
		}},
		{"Ranking", func(ctx context.Context) {
			_, _ = schemaflux.Ranking([]string{"a", "b"}).By("relevance").Run(ctx)
		}},
		{"Compressing", func(ctx context.Context) {
			_, _ = schemaflux.Compressing("a long document").Run(ctx)
		}},
		{"CompressingText", func(ctx context.Context) {
			_, _ = schemaflux.CompressingText("a long document").Run(ctx)
		}},
		{"Decomposing", func(ctx context.Context) {
			_, _ = schemaflux.Decomposing("a complex task").Run(ctx)
		}},
		{"DecomposingInto", func(ctx context.Context) {
			_, _ = schemaflux.DecomposingInto[string, string]("a complex task").Run(ctx)
		}},
		{"Enriching", func(ctx context.Context) {
			_, _ = schemaflux.Enriching[fcDoc, fcDoc](fcDoc{Title: "x"}).Run(ctx)
		}},
		{"EnrichingInPlace", func(ctx context.Context) {
			_, _ = schemaflux.EnrichingInPlace(fcDoc{Title: "x"}).Run(ctx)
		}},
		{"Normalizing", func(ctx context.Context) {
			_, _ = schemaflux.Normalizing(fcDoc{Title: "x"}).Run(ctx)
		}},
		{"NormalizingText", func(ctx context.Context) {
			_, _ = schemaflux.NormalizingText("messy   text").Run(ctx)
		}},
		{"NormalizingBatch", func(ctx context.Context) {
			_, _ = schemaflux.NormalizingBatch([]string{"a", "b"}).Run(ctx)
		}},
		{"Matching", func(ctx context.Context) {
			_, _ = schemaflux.Matching([]string{"a"}, []string{"b"}).Run(ctx)
		}},
		{"MatchingOne", func(ctx context.Context) {
			_, _ = schemaflux.MatchingOne("a", []string{"b", "c"}).Run(ctx)
		}},
		// Critiquing is deliberately absent from this table: CritiqueOptions
		// requires at least one Criteria/Rubric entry (critique.go's
		// Validate), but CritiqueRequest's fluent builder exposes no setter
		// for either -- only Steering/Mode/Intelligence/Model/Context/
		// RequestID/CorrelationID. Critiquing(...).Run() therefore always
		// fails validation before any provider call, for every caller; see
		// TestCritiquingAlwaysFailsValidation below and the bug noted in this
		// task's final report.
		{"Synthesizing", func(ctx context.Context) {
			_, _ = schemaflux.Synthesizing[string]([]any{"source one", "source two"}).Run(ctx)
		}},
		{"Predicting", func(ctx context.Context) {
			_, _ = schemaflux.Predicting[string]("historical data").Run(ctx)
		}},
		{"Verifying", func(ctx context.Context) {
			_, _ = schemaflux.Verifying("a claim to check").Run(ctx)
		}},
		{"VerifyingClaim", func(ctx context.Context) {
			_, _ = schemaflux.VerifyingClaim("a specific claim").Run(ctx)
		}},
		{"Negotiating", func(ctx context.Context) {
			_, _ = schemaflux.Negotiating[string]("constraints").Run(ctx)
		}},
		{"NegotiatingAdversarially", func(ctx context.Context) {
			_, _ = schemaflux.NegotiatingAdversarially[salaryTerms](adversarialCtx()).Run(ctx)
		}},
		{"Deriving", func(ctx context.Context) {
			_, _ = schemaflux.Deriving[fcDoc, fcDoc](fcDoc{Title: "x"}).Run(ctx)
		}},
		{"Conforming", func(ctx context.Context) {
			_, _ = schemaflux.Conforming(fcDoc{Title: "x"}, "USPS").Run(ctx)
		}},
		{"Interpolating", func(ctx context.Context) {
			_, _ = schemaflux.Interpolating([]string{"a", "", "c"}).Run(ctx)
		}},
		{"Arbitrating", func(ctx context.Context) {
			_, _ = schemaflux.Arbitrating([]string{"a", "b"}).Rules("prefer the shorter option").Run(ctx)
		}},
		{"Auditing", func(ctx context.Context) {
			_, _ = schemaflux.Auditing(fcDoc{Title: "x"}).Run(ctx)
		}},
		{"Assembling", func(ctx context.Context) {
			_, _ = schemaflux.Assembling[fcDoc]([]any{"part one", "part two"}).Run(ctx)
		}},
		{"Pivoting", func(ctx context.Context) {
			_, _ = schemaflux.Pivoting[fcDoc, fcDoc](fcDoc{Title: "x"}).Run(ctx)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := schemafluxtest.New().Shaped()
			schemafluxtest.Install(t, p)

			tc.run(context.Background())

			if p.CallCount() == 0 {
				t.Fatalf("%s never reached the provider, so its forwarding is unproven", tc.name)
			}
		})
	}
}

// Redacting is deterministic and pattern-based, so it never reaches the
// provider -- that is correct behaviour, not a gap, and is why it is excluded
// from TestFluentConstructorsReachTheProvider above.
func TestRedactingRedactsWithoutAProviderCall(t *testing.T) {
	p := schemafluxtest.New().Shaped()
	schemafluxtest.Install(t, p)

	// NewRedactOptions defaults Categories to ["PII"], which already matches
	// an email address -- no fluent setter is even needed for this case.
	type contact struct{ Note string }
	got, err := schemaflux.Redacting(contact{Note: "reach ada@example.com"}).Run(context.Background())
	if err != nil {
		t.Fatalf("Redacting: %v", err)
	}
	if got.Note == "reach ada@example.com" {
		t.Errorf("Note = %q, want the email redacted", got.Note)
	}
	if p.CallCount() != 0 {
		t.Errorf("CallCount = %d, want 0: Redact is deterministic and must not call the provider", p.CallCount())
	}
}

// BUG (found while covering this wrapper, not fixed -- out of scope for this
// task, which may only touch tests/*_test.go): CritiqueOptions.Validate
// (internal/ops/critique.go) requires at least one Criteria or Rubric entry,
// but CritiqueRequest's fluent builder (internal/api/fluent/fluent_extended.go,
// newCritiqueRequest / CritiqueRequest, ~line 950-1020) exposes setters only
// for Steering/Mode/Intelligence/Model/Context/RequestID/CorrelationID --
// there is no Criteria(...) or Rubric(...) method. schemaflux.Critiquing(x).Run()
// therefore ALWAYS fails validation before any provider call, for every
// caller, with no way to fix it from the fluent API; the only working path is
// the legacy schemaflux.Critique(input, opts) / CritiqueWithModel(input, opts)
// entrypoints where opts.Criteria is set directly on the struct. The fix
// would be adding a Criteria/Rubric setter to CritiqueRequest, which is
// outside this task's tests/*_test.go-only scope.
func TestCritiquingAlwaysFailsValidation(t *testing.T) {
	p := schemafluxtest.New().Shaped()
	schemafluxtest.Install(t, p)

	_, err := schemaflux.Critiquing("a draft essay").Run(context.Background())
	if err == nil {
		t.Fatal("expected Critiquing to fail validation (no Criteria/Rubric setter exists on the fluent builder)")
	}
	if p.CallCount() != 0 {
		t.Errorf("CallCount = %d, want 0: validation should fail before any provider call", p.CallCount())
	}
}

// --- ChooseBy / FilterBy / SortBy -------------------------------------------

func TestChooseByReturnsOneOfTheOfferedOptions(t *testing.T) {
	// Ids are "i-NNNNNN", 1-based (collection.go's itemIDs); "i-000002" names
	// the second option.
	testfixturesScriptedChoose(t, `{"id":"i-000002"}`)

	options := []string{"apple", "banana", "cherry"}
	chosen, err := schemaflux.ChooseBy(options, "starts with b")
	if err != nil {
		t.Fatalf("ChooseBy: %v", err)
	}
	if chosen != "banana" {
		t.Errorf("chosen = %q, want banana (the second option)", chosen)
	}
}

func TestFilterByReturnsASubsetOfTheOfferedItems(t *testing.T) {
	// Ids are "i-NNNNNN", 1-based (collection.go's itemIDs).
	testfixturesScriptedChoose(t, `{"ids":["i-000001","i-000003"]}`)

	items := []string{"keep me", "drop me", "keep me too"}
	filtered, err := schemaflux.FilterBy(items, "should be kept")
	if err != nil {
		t.Fatalf("FilterBy: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered = %v, want 2 items", filtered)
	}
}

func TestSortByReturnsAPermutationOfTheOfferedItems(t *testing.T) {
	testfixturesScriptedChoose(t, `{"ids":["i-000003","i-000001","i-000002"]}`)

	items := []string{"a", "b", "c"}
	sorted, err := schemaflux.SortBy(items, "reverse alphabetical")
	if err != nil {
		t.Fatalf("SortBy: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("sorted = %v, want a permutation of the 3 inputs", sorted)
	}
}

// testfixturesScriptedChoose installs a scripted provider through
// schemafluxtest, matching the naming already used for Choose/Filter/Sort's
// distinct response shapes (selected index, selected ids, or an order).
func testfixturesScriptedChoose(t *testing.T, body string) {
	t.Helper()
	p := schemafluxtest.New().Reply(body)
	schemafluxtest.Install(t, p)
}

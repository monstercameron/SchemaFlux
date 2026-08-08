package semantic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// scriptedProvider is a minimal fake llm.Provider for driving corpus.go's
// case builders deterministically. It exists instead of schemafluxtest.Provider
// because these tests need to plant an exact pass/fail answer per call (a
// fixed, known "hallucinated" or "off-list" reply), not a scripted sequence
// installed process-globally -- ops.WithProvider scopes a provider to one
// context, which is what lets each subtest below run independently with its
// own answer with no shared, order-sensitive global state.
//
// It never touches a network: Complete returns exactly what the test
// configured, or exactly the error the test configured.
type scriptedProvider struct {
	reply string
	err   error
}

func (p *scriptedProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if p.err != nil {
		return llm.CompletionResponse{}, p.err
	}
	return llm.CompletionResponse{
		Content:      p.reply,
		Model:        req.Model,
		Provider:     "local",
		FinishReason: "stop",
	}, nil
}

// Name returns "local", the provider name this library reserves for an
// in-process double (see schemafluxtest.Provider.As's doc comment) -- any
// other name has no built-in model mapping and every call fails before it
// ever reaches Complete.
func (p *scriptedProvider) Name() string { return "local" }

func (p *scriptedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// RetryPolicy returns no retries: a scripted answer is deterministic, so
// there is nothing a retry would change, and a nonzero backoff here would
// only slow the suite down for no benefit.
func (p *scriptedProvider) RetryPolicy() (int, time.Duration) { return 0, 0 }

func ctxWithReply(t *testing.T, reply string) context.Context {
	t.Helper()
	t.Setenv("SCHEMAFLUX_LLM_MAX_RETRIES", "0")
	return ops.WithProvider(context.Background(), &scriptedProvider{reply: reply})
}

func ctxWithProviderError(t *testing.T) context.Context {
	t.Helper()
	t.Setenv("SCHEMAFLUX_LLM_MAX_RETRIES", "0")
	return ops.WithProvider(context.Background(), &scriptedProvider{err: errors.New("scripted provider failure")})
}

// runCase drives one trial of a Case and fails the test if the harness
// itself reported an error -- corpus.go's own contract is that Run "returns
// an error only for a failure of the harness itself", never for a provider
// or model failure (that is Outcome.Errored), so a non-nil error here means
// something in the test's own scripting broke, not a case under test.
func runCase(t *testing.T, c Case, ctx context.Context) Outcome {
	t.Helper()
	outcome, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("case %q: harness error: %v", c.Name, err)
	}
	return outcome
}

func TestExtractionAccuracy(t *testing.T) {
	c := extractionAccuracy(2)
	if c.Dimension != "extraction accuracy" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes when the number is found", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"INV-4417","total":1284.50,"vendor":"Northwind Traders"}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true Errored=false", outcome)
		}
	})

	t.Run("fails when the number is wrong", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"WRONG-0001","total":1284.50,"vendor":"Northwind Traders"}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false Errored=false", outcome)
		}
	})

	t.Run("counts a provider failure as errored, not failed", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithProviderError(t))
		if !outcome.Errored {
			t.Errorf("outcome = %+v, want Errored=true", outcome)
		}
		if outcome.Passed {
			t.Error("an errored trial reported Passed=true")
		}
	})
}

func TestExtractionHallucination(t *testing.T) {
	c := extractionHallucination(2)
	if c.Dimension != "hallucination" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes when no number is invented", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"","total":0,"vendor":"Northwind Traders"}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true", outcome)
		}
	})

	t.Run("fails when a number is fabricated", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"INV-9999","total":0,"vendor":"Northwind Traders"}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false -- a fabricated invoice number must fail this case", outcome)
		}
	})
}

func TestMissingFieldHonesty(t *testing.T) {
	c := missingFieldHonesty(2)
	if c.Dimension != "missing fields" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes when the unsupported total stays zero", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"","total":0,"vendor":"Northwind Traders"}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true", outcome)
		}
	})

	t.Run("fails when a plausible total is invented", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"","total":42.00,"vendor":"Northwind Traders"}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false -- an invented total for an unsupported field must fail", outcome)
		}
	})
}

func TestClassificationAccuracy(t *testing.T) {
	c := classificationAccuracy(2)
	if c.Dimension != "classification accuracy" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes when the category matches", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"category":"billing","confidence":0.92,"alternatives":[],"reasoning":"duplicate charge"}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true", outcome)
		}
	})

	t.Run("fails when the category is wrong", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"category":"shipping","confidence":0.9,"alternatives":[],"reasoning":"x"}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false", outcome)
		}
	})
}

func TestClassificationAbstention(t *testing.T) {
	c := classificationAbstention(2)
	if c.Dimension != "classification abstention" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes on a low-confidence answer", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"category":"billing","confidence":0.2,"alternatives":[],"reasoning":"weak match"}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true (confidence below 0.5)", outcome)
		}
	})

	// A refusal (provider/parse error) IS the correct behaviour for this
	// case -- corpus.go's own comment says so -- so this is the one case in
	// the corpus where a provider error must produce Passed=true, not
	// Errored=true, and it deserves its own assertion because it is the
	// exception to every other case's error handling in this file.
	t.Run("passes on an outright refusal", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithProviderError(t))
		if outcome.Errored {
			t.Error("classificationAbstention counted a refusal as Errored; it should count as Passed")
		}
		if !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true for a refusal", outcome)
		}
	})

	t.Run("fails on a confident off-topic answer", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"category":"billing","confidence":0.95,"alternatives":[],"reasoning":"confident but wrong"}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false -- confidently forcing an off-topic input into a category must fail", outcome)
		}
	})
}

func TestChoosePrecision(t *testing.T) {
	c := choosePrecision(2)
	if c.Dimension != "choose precision" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes when the refund option on the list is chosen", func(t *testing.T) {
		// Options are tagged i-000001.. in input order; index 0 is the
		// refund-for-duplicate-charge option.
		outcome := runCase(t, c, ctxWithReply(t, `{"id":"i-000001"}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true", outcome)
		}
	})

	t.Run("fails when a different on-list option is chosen", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"id":"i-000002"}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false", outcome)
		}
	})

	// An id the caller never offered is a harness/provider-level failure
	// (Choose refuses it, see OP-101/collection.go's MemberOf check), which
	// surfaces here as Errored -- proving the "on the list" half of this
	// case's check is actually enforced, not merely asserted for a value
	// that would always be on the list anyway.
	t.Run("an invented id is refused, not silently accepted", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"id":"i-999999"}`))
		if !outcome.Errored {
			t.Errorf("outcome = %+v, want Errored=true for an id that was never offered", outcome)
		}
	})
}

func TestFilterRecall(t *testing.T) {
	c := filterRecall(2)
	if c.Dimension != "filter precision and recall" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	// items: i-000001 charged twice, i-000002 where's my package,
	// i-000003 billed wrong amount, i-000004 cannot log in.
	t.Run("passes when both billing items are kept and no others", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"ids":["i-000001","i-000003"]}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true", outcome)
		}
	})

	t.Run("fails on incomplete recall", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"ids":["i-000001"]}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false -- missing a billing item is a recall failure", outcome)
		}
	})

	t.Run("fails on poor precision", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"ids":["i-000001","i-000002","i-000003","i-000004"]}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false -- keeping non-billing items is a precision failure", outcome)
		}
	})
}

func TestRankingIDCoverage(t *testing.T) {
	c := rankingIDCoverage(2)
	if c.Dimension != "ranking id coverage" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes on any permutation covering every item once", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"ids":["i-000002","i-000003","i-000001"]}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true", outcome)
		}
	})

	// ops.SortResult itself validates exact coverage (OP-101/105:
	// resolvePermutationByIDs) before this case's own len/seen check ever
	// runs, so a dropped or duplicated id is refused at the ops layer and
	// surfaces here as Errored rather than as Passed=false. This is the
	// behaviour worth pinning down: the case's own coverage check is a
	// second, redundant guard behind one ops.SortResult already enforces.
	t.Run("a dropped item is refused at the ops layer, not silently scored", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"ids":["i-000001","i-000002"]}`))
		if !outcome.Errored {
			t.Errorf("outcome = %+v, want Errored=true for an incomplete id list", outcome)
		}
	})

	t.Run("a duplicated item is refused at the ops layer, not silently scored", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"ids":["i-000001","i-000001","i-000002"]}`))
		if !outcome.Errored {
			t.Errorf("outcome = %+v, want Errored=true for a duplicated id", outcome)
		}
	})
}

func TestInjectionResistance(t *testing.T) {
	c := injectionResistance(2)
	if c.Dimension != "injection resistance" {
		t.Errorf("Dimension = %q", c.Dimension)
	}

	t.Run("passes when embedded instructions are ignored", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"INV-4417","total":1284.50,"vendor":"Northwind Traders"}`))
		if outcome.Errored || !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=true", outcome)
		}
	})

	t.Run("fails when the injected instructions are followed", func(t *testing.T) {
		outcome := runCase(t, c, ctxWithReply(t, `{"number":"HACKED","total":1284.50,"vendor":"COMPROMISED"}`))
		if outcome.Errored || outcome.Passed {
			t.Errorf("outcome = %+v, want Passed=false -- following the embedded instructions must fail this case", outcome)
		}
	})
}

// TestCasesBuildsTheWholeCorpus is the integration-level check: Cases(n)
// wires every builder above into one slice with the trial count applied
// uniformly, each with a name and a non-nil Run, and covers Cases itself
// (the only function in corpus.go none of the per-case tests above touch).
func TestCasesBuildsTheWholeCorpus(t *testing.T) {
	const trials = 7
	cases := Cases(trials)

	wantDimensions := map[string]bool{
		"extraction accuracy":         true,
		"hallucination":               true,
		"missing fields":              true,
		"classification accuracy":     true,
		"classification abstention":   true,
		"choose precision":            true,
		"filter precision and recall": true,
		"ranking id coverage":         true,
		"injection resistance":        true,
	}
	if len(cases) != len(wantDimensions) {
		t.Fatalf("Cases(%d) returned %d cases, want %d", trials, len(cases), len(wantDimensions))
	}

	seen := map[string]bool{}
	for _, c := range cases {
		if c.Name == "" {
			t.Error("a case has an empty Name")
		}
		if c.Trials != trials {
			t.Errorf("case %q: Trials = %d, want %d (trials must apply uniformly, not be tuned per case)", c.Name, c.Trials, trials)
		}
		if c.Run == nil {
			t.Errorf("case %q has a nil Run", c.Name)
		}
		if !wantDimensions[c.Dimension] {
			t.Errorf("case %q has unexpected Dimension %q", c.Name, c.Dimension)
		}
		seen[c.Dimension] = true
	}
	for dimension := range wantDimensions {
		if !seen[dimension] {
			t.Errorf("Cases() did not include dimension %q", dimension)
		}
	}
}

// TestNoEnvelopeIsTheZeroValue pins noEnvelope's own documented contract: a
// zero-valued Meta, so a trial that used an envelope-less operation
// (Choose/Filter/Classify) is honestly reported as unpriced rather than
// silently treated as free.
func TestNoEnvelopeIsTheZeroValue(t *testing.T) {
	meta := noEnvelope()
	outcome := OutcomeFromMeta(meta, true, time.Millisecond)
	if outcome.Priced {
		t.Error("noEnvelope's zero-valued Meta produced Priced=true")
	}
	if outcome.Cost != 0 {
		t.Errorf("noEnvelope's zero-valued Meta produced a nonzero cost: %v", outcome.Cost)
	}
}

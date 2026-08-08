package fluent

import (
	"testing"
)

// setters_test.go's build/set/read-back discipline, extended to every
// requestBase-based builder in fluent_analysis.go, fluent_extended.go, and
// fluent_advanced.go that reach_test.go / analysis_extended_reach_test.go /
// analysis_extended_setters_test.go did not already drive through
// Steer/RequestID/CorrelationID/Model. Each of those four setters lives
// inside a per-construction-site closure (fluent_base.go's requestBase);
// wiring_test.go proves every closure is *wired*, this proves each one, on
// each type, actually *applies* -- the two are different claims, and only
// the second is what a caller observes.
func TestCommonSettersApplyOnTheRemainingBuilders(t *testing.T) {
	const model = "pinned-model-x"

	cases := []struct {
		name string
		opts any
	}{
		{"Classifying", Classifying[stubExtractTarget, string](stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Negotiating", Negotiating[stubExtractTarget](map[string]any{"budget": "low"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"NegotiatingAdversarially", NegotiatingAdversarially(AdversarialContext[stubExtractTarget]{}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Auditing", Auditing(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Scoring", Scoring(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Comparing", Comparing(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"CheckingSimilarity", CheckingSimilarity(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Inferring", Inferring(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Diffing", Diffing(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Explaining", Explaining(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Parsing", Parsing[stubExtractTarget]("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Summarizing", Summarizing("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Rewriting", Rewriting("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Translating", Translating("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Expanding", Expanding("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Suggesting", Suggesting[string]("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Redacting", Redacting(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"LLMRedacting", LLMRedacting("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Completing", Completing("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"CompletingField", CompletingField(stubExtractTarget{Name: "x"}, "Name").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Validating", Validating(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Asking", Asking[stubExtractTarget, string](stubExtractTarget{Name: "x"}, "q").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Annotating", Annotating(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Clustering", Clustering([]stubExtractTarget{{Name: "a"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Ranking", Ranking([]stubExtractTarget{{Name: "a"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Compressing", Compressing(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"CompressingText", CompressingText("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Decomposing", Decomposing(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"DecomposingInto", DecomposingInto[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Enriching", Enriching[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"EnrichingInPlace", EnrichingInPlace(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Normalizing", Normalizing(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"NormalizingText", NormalizingText("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"NormalizingBatch", NormalizingBatch([]stubExtractTarget{{Name: "a"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Matching", Matching([]stubExtractTarget{{Name: "a"}}, []stubExtractTarget{{Name: "b"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"MatchingOne", MatchingOne(stubExtractTarget{Name: "a"}, []stubExtractTarget{{Name: "b"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Critiquing", Critiquing(stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Synthesizing", Synthesizing[stubExtractTarget]([]any{"a"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Predicting", Predicting[stubExtractTarget]("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Verifying", Verifying("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"VerifyingClaim", VerifyingClaim("x").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Resolving", Resolving([]stubExtractTarget{{Name: "a"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Deriving", Deriving[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Conforming", Conforming(stubExtractTarget{Name: "x"}, "standard").
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Interpolating", Interpolating([]stubExtractTarget{{Name: "a"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Arbitrating", Arbitrating([]stubExtractTarget{{Name: "a"}}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Projecting", Projecting[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Assembling", Assembling[stubExtractTarget]([]any{"part"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
		{"Pivoting", Pivoting[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).
			Steer("s").RequestID("r").CorrelationID("c").Model(model).Threshold(0.5).opts},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireSet(t, tc.name, "Steering", tc.opts, "s")
			requireSet(t, tc.name, "RequestID", tc.opts, "r")
			requireSet(t, tc.name, "CorrelationID", tc.opts, "c")
			requireSet(t, tc.name, "Model", tc.opts, model)
		})
	}
}

// Mode and Intelligence, on the same set of builders: FL-005's requestBase
// stores these through the same per-type setMode/setIntelligence closures,
// and wiring_test.go's TestEveryBuilderWiresEverySetter only proves the
// closures are assigned, not that calling Strict()/Smart() through the
// package's own Mode/Speed constants moves the right field.
func TestModeAndIntelligenceApplyOnTheRemainingBuilders(t *testing.T) {
	cases := []struct {
		name string
		opts any
	}{
		{"Classifying", Classifying[stubExtractTarget, string](stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Negotiating", Negotiating[stubExtractTarget](map[string]any{"budget": "low"}).Creative().Fast().opts},
		{"NegotiatingAdversarially", NegotiatingAdversarially(AdversarialContext[stubExtractTarget]{}).Creative().Fast().opts},
		{"Auditing", Auditing(stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Scoring", Scoring(stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Comparing", Comparing(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).Creative().Fast().opts},
		{"Summarizing", Summarizing("x").Creative().Fast().opts},
		{"Suggesting", Suggesting[string]("x").Creative().Fast().opts},
		{"Validating", Validating(stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Annotating", Annotating(stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Ranking", Ranking([]stubExtractTarget{{Name: "a"}}).Creative().Fast().opts},
		{"Critiquing", Critiquing(stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Verifying", Verifying("x").Creative().Fast().opts},
		{"Resolving", Resolving([]stubExtractTarget{{Name: "a"}}).Creative().Fast().opts},
		{"Deriving", Deriving[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Conforming", Conforming(stubExtractTarget{Name: "x"}, "standard").Creative().Fast().opts},
		{"Interpolating", Interpolating([]stubExtractTarget{{Name: "a"}}).Creative().Fast().opts},
		{"Arbitrating", Arbitrating([]stubExtractTarget{{Name: "a"}}).Creative().Fast().opts},
		{"Projecting", Projecting[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).Creative().Fast().opts},
		{"Assembling", Assembling[stubExtractTarget]([]any{"part"}).Creative().Fast().opts},
		{"Pivoting", Pivoting[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).Creative().Fast().opts},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireSet(t, tc.name, "Mode", tc.opts, Creative)
			requireSet(t, tc.name, "Intelligence", tc.opts, Fast)
		})
	}

	// Smart/Quick, distinctly, on one representative of each remaining
	// three-embedding-shape family -- CommonOptions (Scoring), OpOptions
	// (CheckingSimilarity), and plain-field (Resolving) -- so the "the tier
	// setters are distinguishable from silence" property setters_test.go's
	// TestTheTierSettersAreDistinguishableFromSilence proved on the six core
	// builders is not accidentally only true of those six.
	t.Run("Smart_and_Quick_distinguishable", func(t *testing.T) {
		smart := Scoring(stubExtractTarget{Name: "x"}).Smart().opts
		quick := Scoring(stubExtractTarget{Name: "x"}).Quick().opts
		requireSet(t, "Scoring.Smart", "Intelligence", smart, Smart)
		requireSet(t, "Scoring.Quick", "Intelligence", quick, Quick)

		smartSim := CheckingSimilarity(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).Smart().opts
		quickSim := CheckingSimilarity(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).Quick().opts
		requireSet(t, "CheckingSimilarity.Smart", "Intelligence", smartSim, Smart)
		requireSet(t, "CheckingSimilarity.Quick", "Intelligence", quickSim, Quick)

		smartRes := Resolving([]stubExtractTarget{{Name: "a"}}).Smart().opts
		quickRes := Resolving([]stubExtractTarget{{Name: "a"}}).Quick().opts
		if smartRes.Intelligence != Smart {
			t.Errorf("Resolving.Smart: Intelligence = %v, want Smart", smartRes.Intelligence)
		}
		if quickRes.Intelligence != Quick {
			t.Errorf("Resolving.Quick: Intelligence = %v, want Quick", quickRes.Intelligence)
		}
	})
}

// Run()'s buildError-refusal branch ("var zero T; return zero, err"),
// exercised on every builder whose underlying Options type has a Validate()
// that can actually fail -- fl006_validate_test.go proved the property on
// six builders; these are the rest of the package's Options types with a
// real Validate(). Threshold(5) is the universal trigger: every one of
// these types' Validate() calls CommonOptions.Validate() (or, for
// CheckingSimilarity, its own SimilarityThreshold check) before anything
// else, and CommonOptions.Validate()/SimilarOptions.Validate() both reject a
// threshold outside [0, 1] -- see internal/ops/options.go:126 and
// internal/ops/analysis.go:711.
//
// Not included: InferOptions, DiffOptions, and ParseOptions each implement
// Validate() as an unconditional `return nil` (internal/ops/infer.go,
// internal/ops/diff.go, internal/ops/parse.go), and the eleven
// fluent_advanced.go types other than Audit have no Validate() method at
// all (fl006_validate_test.go's TestResolveRequest_Run_NoValidateMethod_
// BuildErrorIsANoOp documents the pattern for Resolve). For all fourteen,
// Run()'s buildError branch is genuinely unreachable without a change to
// internal/ops -- outside this task's edit scope -- so it stays uncovered
// rather than papered over with a fake trigger.
func TestThresholdOutOfRangeRefusesWithoutProviderContact_RemainingBuilders(t *testing.T) {
	run := func(t *testing.T, name string, run func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			p := &countingProvider{}
			restore := installStubProvider(t, p)
			defer restore()

			err := run()
			if err == nil {
				t.Fatalf("%s: Run() with Threshold(5) returned nil error, want a validation error", name)
			}
			requireNoContact(t, p, name)
		})
	}

	run(t, "Scoring", func() error {
		_, err := Scoring(stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "Comparing", func() error {
		_, err := Comparing(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).Threshold(5).Run()
		return err
	})
	run(t, "CheckingSimilarity", func() error {
		_, err := CheckingSimilarity(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).Threshold(5).Run()
		return err
	})
	run(t, "Summarizing", func() error {
		_, err := Summarizing("x").Threshold(5).Run()
		return err
	})
	run(t, "Rewriting", func() error {
		_, err := Rewriting("x").Threshold(5).Run()
		return err
	})
	run(t, "Translating", func() error {
		_, err := Translating("x").Threshold(5).Run()
		return err
	})
	run(t, "Expanding", func() error {
		_, err := Expanding("x").Threshold(5).Run()
		return err
	})
	run(t, "Suggesting", func() error {
		_, err := Suggesting[string]("x").Threshold(5).Run()
		return err
	})
	run(t, "Asking", func() error {
		_, err := Asking[stubExtractTarget, string](stubExtractTarget{Name: "x"}, "q").Threshold(5).Run()
		return err
	})
	run(t, "Annotating", func() error {
		_, err := Annotating(stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "Clustering", func() error {
		_, err := Clustering([]stubExtractTarget{{Name: "a"}}).Threshold(5).Run()
		return err
	})
	run(t, "Ranking", func() error {
		_, err := Ranking([]stubExtractTarget{{Name: "a"}}).By("x").Threshold(5).Run()
		return err
	})
	run(t, "Compressing", func() error {
		_, err := Compressing(stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "CompressingText", func() error {
		_, err := CompressingText("x").Threshold(5).Run()
		return err
	})
	run(t, "Decomposing", func() error {
		_, err := Decomposing(stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "DecomposingInto", func() error {
		_, err := DecomposingInto[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "Enriching", func() error {
		_, err := Enriching[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "EnrichingInPlace", func() error {
		_, err := EnrichingInPlace(stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "Normalizing", func() error {
		_, err := Normalizing(stubExtractTarget{Name: "x"}).Threshold(5).Run()
		return err
	})
	run(t, "NormalizingText", func() error {
		_, err := NormalizingText("x").Threshold(5).Run()
		return err
	})
	run(t, "NormalizingBatch", func() error {
		_, err := NormalizingBatch([]stubExtractTarget{{Name: "a"}}).Threshold(5).Run()
		return err
	})
	run(t, "Matching", func() error {
		_, err := Matching([]stubExtractTarget{{Name: "a"}}, []stubExtractTarget{{Name: "b"}}).Threshold(5).Run()
		return err
	})
	run(t, "MatchingOne", func() error {
		_, err := MatchingOne(stubExtractTarget{Name: "a"}, []stubExtractTarget{{Name: "b"}}).Threshold(5).Run()
		return err
	})
	run(t, "Synthesizing", func() error {
		_, err := Synthesizing[stubExtractTarget]([]any{"a"}).Threshold(5).Run()
		return err
	})
	run(t, "Predicting", func() error {
		_, err := Predicting[stubExtractTarget]("x").Threshold(5).Run()
		return err
	})
	run(t, "Verifying", func() error {
		_, err := Verifying("x").Threshold(5).Run()
		return err
	})
	run(t, "VerifyingClaim", func() error {
		_, err := VerifyingClaim("x").Threshold(5).Run()
		return err
	})

	// Critiquing has no Threshold-independent path to a validation error --
	// its own Validate() checks CommonOptions first, same as the rest -- but
	// it is exercised via its natural invalid state instead (no Criteria/
	// Rubric set, see analysis_extended_reach_test.go's comment on why the
	// fluent surface offers no Criteria()/Rubric() setter), to show the same
	// refusal-before-dispatch property holds without Configure.
	run(t, "Critiquing_NoCriteriaOrRubric", func() error {
		_, err := Critiquing(stubExtractTarget{Name: "x"}).Run()
		return err
	})

	// RedactOptions, RedactLLMOptions, and CompleteOptions embed OpOptions
	// only and their Validate() never inspects OpOptions.Threshold (see
	// internal/ops/redact.go, redact_llm.go, complete.go) -- Configure is
	// used instead of Threshold(5) to reach each one's actual invalid state.
	run(t, "Redacting_InvalidStrategy", func() error {
		_, err := Redacting(stubExtractTarget{Name: "x"}).Configure(func(o RedactOptions) RedactOptions {
			o.Strategy = RedactStrategy("not-a-real-strategy")
			return o
		}).Run()
		return err
	})
	run(t, "LLMRedacting_NoCategories", func() error {
		_, err := LLMRedacting("x").Configure(func(o RedactLLMOptions) RedactLLMOptions {
			o.Categories = nil
			return o
		}).Run()
		return err
	})
	run(t, "Completing_NonPositiveMaxLength", func() error {
		_, err := Completing("x").Configure(func(o CompleteOptions) CompleteOptions {
			o.MaxLength = 0
			return o
		}).Run()
		return err
	})
}

package fluent

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// reach_test.go proved the first thirty-odd entrypoints across the core,
// advanced, and shorthand surfaces actually dispatch to a provider rather
// than compiling and silently doing nothing. Its own
// TestTheAnalysisAndExtendedEntrypointsReachTheProvider covered ten of
// fluent_analysis.go's and fluent_extended.go's entrypoints as "the largest
// part of this package and the least covered" -- but stopped at ten. This
// file is the rest of that same claim: every remaining entrypoint in
// fluent_analysis.go, fluent_extended.go, and the two fluent_advanced.go
// builders reach_test.go's own advanced-surface test did not reach
// (NegotiatingAdversarially, Assembling).
//
// Redacting and CompletingField's sibling LLMRedacting/Completing are a
// deliberate split: Redact (internal/ops/redact.go) is a pattern matcher --
// AGENTS.md's "decide locally what can be decided locally" -- and never
// contacts a provider at all, so it is exercised only in the setters test,
// not here. Everything below it that shares the section is genuinely
// LLM-backed.
func TestTheRemainingAnalysisAndExtendedEntrypointsReachTheProvider(t *testing.T) {
	reaches(t, "CheckingSimilarity", `{"score":0.5,"similar":true}`, func(ctx context.Context) error {
		_, err := CheckingSimilarity(reachTarget{Name: "a"}, reachTarget{Name: "b"}).Context(ctx).Run()
		return err
	})

	// Parse tries a fast algorithmic decode first and only reaches a
	// provider when that fails and AllowLLMFallback is on (parse.go's
	// parseImpl) -- so the input has to be something no built-in format
	// detector recognises.
	reaches(t, "Parsing", `{"name":"parsed"}`, func(ctx context.Context) error {
		_, err := Parsing[reachTarget]("not any known format @@@ 123").
			AllowLLMFallback(true).Context(ctx).Run()
		return err
	})

	reaches(t, "Suggesting", `["a","b"]`, func(ctx context.Context) error {
		_, err := Suggesting[string]("suggest some names").Context(ctx).Run()
		return err
	})

	// LLMRedacting, Completing, and CompletingField are NOT run through
	// reaches()/ops.WithProvider like every other entrypoint here -- see
	// TestContextIsIgnoredByRedactLLMCompleteAndCompleteField below, which
	// documents why: their aliases.go wrappers call the underlying ops
	// function with a hardcoded context.Background() instead of the ctx
	// .Context(ctx) stored on opts, so a per-call provider attached via
	// ops.WithProvider(ctx, p) never reaches them. They still dispatch
	// through the package-level default provider, which is what this
	// package's own installStubProvider helper (run_test.go) sets up, so
	// that is what proves these three reach a provider at all.
	t.Run("LLMRedacting", func(t *testing.T) {
		p := &countingProvider{body: `{"redacted":"...","found":[]}`}
		restore := installStubProvider(t, p)
		defer restore()
		_, _ = LLMRedacting("call me at 555-1234").Run()
		if p.calls.Load() == 0 {
			t.Error("LLMRedacting never reached the provider")
		}
	})

	t.Run("Completing", func(t *testing.T) {
		p := &countingProvider{body: `{"completion":"...done"}`}
		restore := installStubProvider(t, p)
		defer restore()
		_, _ = Completing("the quick brown fox").Run()
		if p.calls.Load() == 0 {
			t.Error("Completing never reached the provider")
		}
	})

	t.Run("CompletingField", func(t *testing.T) {
		p := &countingProvider{body: `{"value":"filled"}`}
		restore := installStubProvider(t, p)
		defer restore()
		_, _ = CompletingField(reachTarget{Name: "partial"}, "Name").Run()
		if p.calls.Load() == 0 {
			t.Error("CompletingField never reached the provider")
		}
	})

	// Rules must be free text ("must be an adult") for Validate to reach a
	// model at all -- ValidateDeterministically's own doc comment (and
	// AGENTS.md's "decide locally") is why a pure FieldRules validation
	// never would.
	reaches(t, "Validating", `{"valid":true,"errors":[]}`, func(ctx context.Context) error {
		_, err := Validating[reachTarget](reachTarget{Name: "x"}).
			Rules("the name must sound trustworthy").Context(ctx).Run()
		return err
	})

	reaches(t, "Asking", `{"answer":"x"}`, func(ctx context.Context) error {
		_, err := Asking[reachTarget, string](reachTarget{Name: "x"}, "what is this?").Context(ctx).Run()
		return err
	})

	reaches(t, "Annotating", `{"annotations":[]}`, func(ctx context.Context) error {
		_, err := Annotating(reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Clustering", `{"clusters":[]}`, func(ctx context.Context) error {
		_, err := Clustering([]reachTarget{{Name: "a"}, {Name: "b"}}).Context(ctx).Run()
		return err
	})

	reaches(t, "Ranking", `{"ranked":[]}`, func(ctx context.Context) error {
		_, err := Ranking([]reachTarget{{Name: "a"}, {Name: "b"}}).By("relevance").Context(ctx).Run()
		return err
	})

	reaches(t, "Compressing", `{"compressed":{}}`, func(ctx context.Context) error {
		_, err := Compressing(reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "CompressingText", `{"text":"short"}`, func(ctx context.Context) error {
		_, err := CompressingText("a much longer piece of text to compress").Context(ctx).Run()
		return err
	})

	reaches(t, "Decomposing", `{"parts":[]}`, func(ctx context.Context) error {
		_, err := Decomposing(reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "DecomposingInto", `[{"name":"a"}]`, func(ctx context.Context) error {
		_, err := DecomposingInto[reachTarget, reachTarget](reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Enriching", `{"name":"enriched"}`, func(ctx context.Context) error {
		_, err := Enriching[reachTarget, reachTarget](reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "EnrichingInPlace", `{"name":"enriched"}`, func(ctx context.Context) error {
		_, err := EnrichingInPlace(reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Normalizing", `{"name":"normal"}`, func(ctx context.Context) error {
		_, err := Normalizing(reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "NormalizingText", `{"text":"normal"}`, func(ctx context.Context) error {
		_, err := NormalizingText("  MESSY text  ").Context(ctx).Run()
		return err
	})

	reaches(t, "NormalizingBatch", `[{"name":"normal"}]`, func(ctx context.Context) error {
		_, err := NormalizingBatch([]reachTarget{{Name: "a"}, {Name: "b"}}).Context(ctx).Run()
		return err
	})

	reaches(t, "Matching", `{"pairs":[]}`, func(ctx context.Context) error {
		_, err := Matching([]reachTarget{{Name: "a"}}, []reachTarget{{Name: "b"}}).Context(ctx).Run()
		return err
	})

	reaches(t, "MatchingOne", `[]`, func(ctx context.Context) error {
		_, err := MatchingOne(reachTarget{Name: "a"}, []reachTarget{{Name: "b"}}).Context(ctx).Run()
		return err
	})

	// CritiqueRequest (fluent_extended.go) exposes no .Criteria()/.Rubric()
	// builder method at all, and CritiqueOptions.Validate requires at least
	// one of the two -- so Configure is the only way to build a valid
	// Critiquing() request from this package's fluent surface. That gap is
	// reported alongside this test rather than silently worked around.
	reaches(t, "Critiquing", `{"issues":[],"overall":"fine"}`, func(ctx context.Context) error {
		_, err := Critiquing(reachTarget{Name: "x"}).
			Configure(func(o CritiqueOptions) CritiqueOptions {
				o.Criteria = []string{"clarity"}
				return o
			}).
			Context(ctx).Run()
		return err
	})

	reaches(t, "Synthesizing", `{"name":"synthesized"}`, func(ctx context.Context) error {
		_, err := Synthesizing[reachTarget]([]any{reachTarget{Name: "a"}, reachTarget{Name: "b"}}).Context(ctx).Run()
		return err
	})

	reaches(t, "Predicting", `{"prediction":{}}`, func(ctx context.Context) error {
		_, err := Predicting[reachTarget](reachTarget{Name: "x"}).Context(ctx).Run()
		return err
	})

	reaches(t, "Verifying", `{"verified":true}`, func(ctx context.Context) error {
		_, err := Verifying("the sky is blue").Context(ctx).Run()
		return err
	})

	reaches(t, "VerifyingClaim", `{"verified":true}`, func(ctx context.Context) error {
		_, err := VerifyingClaim("the sky is blue").Context(ctx).Run()
		return err
	})

	reaches(t, "NegotiatingAdversarially", `{"agreement":{},"satisfaction":{}}`, func(ctx context.Context) error {
		_, err := NegotiatingAdversarially(AdversarialContext[reachTarget]{}).Context(ctx).Run()
		return err
	})

	reaches(t, "Assembling", `{"assembled":{}}`, func(ctx context.Context) error {
		_, err := Assembling[reachTarget]([]any{"part one", "part two"}).Context(ctx).Run()
		return err
	})
}

// BUG (found while writing this test, not fixed -- this package may only
// edit *_test.go): LLMRedacting, Completing, and CompletingField each build
// on a fluent request type (RedactTextRequest, CompleteRequest,
// CompleteFieldRequest) whose .Context(ctx) stores ctx on
// opts.OpOptions.Context exactly like every other builder in this package.
// But aliases.go's RedactLLM, Complete, and CompleteField -- the functions
// their Run() methods call -- ignore that stored value and the ctx their own
// Run(ctx ...context.Context) variadic parameter would have supplied,
// hardcoding context.Background() instead:
//
//	func RedactLLM(input string, opts RedactLLMOptions) (RedactLLMResult, error) {
//		return ops.RedactLLM(context.Background(), input, opts)
//	}
//
// internal/ops.RedactLLM/Complete/CompleteField take ctx as their own
// parameter separate from opts, and only fall back to opts.Context when the
// passed ctx is nil (see internal/ops/complete.go's completeImpl and
// internal/ops/redact_llm.go's RedactLLM) -- context.Background() is never
// nil, so that fallback never triggers. internal/ops/llm_helper.go's
// providerFromContext reads the provider from exactly the ctx callLLM
// receives, so a provider attached with ops.WithProvider(ctx, p) -- what
// Client.Context(ctx) and this test's own reaches() helper use -- is
// silently discarded for these three entrypoints alone; they still work
// because they fall through to the package-level default provider, the
// same one installStubProvider (run_test.go) installs, which is why
// TestTheRemainingAnalysisAndExtendedEntrypointsReachTheProvider reaches
// LLMRedacting, Completing, and CompletingField must reach the provider on the
// caller's context, like every other entrypoint.
//
// They did not. aliases.go passed context.Background() unconditionally, so
// `.Context(ctx)` on these three compiled, chained, and reached nothing. The
// consequence was not a missing deadline: Client.Context installs a per-call
// provider on the context (IN-004), so a client's provider was silently
// discarded here and the call fell through to the process-wide default. Two
// clients with different providers got whichever was installed last, on exactly
// these three paths and nowhere else.
//
// This test was written against the broken behaviour, asserting the per-call
// provider was NOT reached, with a note to invert it once aliases.go forwarded
// the context. It has been inverted: the per-call provider must win, and the
// global must not be touched.
func TestRedactLLMCompleteAndCompleteFieldHonourTheCallersContext(t *testing.T) {
	fresh := func() *countingProvider { return &countingProvider{body: `{}`} }

	cases := []struct {
		name string
		run  func(ctx context.Context)
	}{
		{"LLMRedacting", func(ctx context.Context) {
			_, _ = LLMRedacting("call 555-1234").Context(ctx).Run()
		}},
		{"Completing", func(ctx context.Context) {
			_, _ = Completing("some text").Context(ctx).Run()
		}},
		{"CompletingField", func(ctx context.Context) {
			_, _ = CompletingField(reachTarget{Name: "x"}, "Name").Context(ctx).Run()
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			global := fresh()
			restore := installStubProvider(t, global)
			defer restore()

			perCall := fresh()
			tc.run(ops.WithProvider(context.Background(), perCall))

			if perCall.calls.Load() == 0 {
				t.Errorf("%s did not reach the provider on the caller's context; "+
					"aliases.go is discarding opts.Context again", tc.name)
			}
			if global.calls.Load() != 0 {
				t.Errorf("%s reached the process-wide provider %d time(s) as well; "+
					"a per-call provider must win outright, or two clients still interfere",
					tc.name, global.calls.Load())
			}
		})
	}
}

// RunDetailed on Summarize/Rewrite/Translate/Expand goes around Run()
// entirely, straight to the *WithMetadata alias (fluent_analysis.go, e.g.
// SummarizeRequest.RunDetailed calling SummarizeWithMetadata) -- Run()
// reaching a provider says nothing about whether that second path also
// does.
func TestRunDetailedEntrypointsReachTheProvider(t *testing.T) {
	call := func(t *testing.T, name, body string, run func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			provider := &countingProvider{body: body}
			restore := installStubProvider(t, provider)
			defer restore()

			_ = run()

			if n := provider.calls.Load(); n == 0 {
				t.Errorf("%s never reached the provider; RunDetailed does not dispatch", name)
			}
		})
	}

	call(t, "SummarizeRequest.RunDetailed", `{"text":"short","key_points":[],"confidence":0.5}`, func() error {
		_, err := Summarizing("a longer piece of text to summarise").RunDetailed()
		return err
	})

	call(t, "RewriteRequest.RunDetailed", `{"text":"rewritten","changes_made":[],"confidence":0.5}`, func() error {
		_, err := Rewriting("some text").RunDetailed()
		return err
	})

	call(t, "TranslateRequest.RunDetailed", `{"text":"traducido","confidence":0.5}`, func() error {
		_, err := Translating("some text").To("Spanish").RunDetailed()
		return err
	})

	call(t, "ExpandRequest.RunDetailed", `{"text":"a much longer text","added_content":[],"confidence":0.5}`, func() error {
		_, err := Expanding("brief").RunDetailed()
		return err
	})
}

// RunResult on Transform, Generate, Filter, and Sort: Choose and Extract's
// RunResult were already exercised (run_test.go), but Transform, Generate,
// Filter, and Sort's own RunResult wrappers -- the runEnvelope path for
// operations with no ops.XResult twin -- were not, and each is a distinct
// composite literal that could independently forget to call through.
func TestRunResultReachesTheProviderOnEveryCoreBuilder(t *testing.T) {
	call := func(t *testing.T, name, body string, run func(ctx context.Context) error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			provider := &countingProvider{body: body}
			ctx := context.Background()
			restore := installStubProvider(t, provider)
			defer restore()

			_ = run(ctx)

			if n := provider.calls.Load(); n == 0 {
				t.Errorf("%s never reached the provider; RunResult does not dispatch", name)
			}
		})
	}

	call(t, "Transforming.RunResult", `{"name":"x"}`, func(ctx context.Context) error {
		_, err := Transforming[reachTarget, reachTarget](reachTarget{Name: "in"}).RunResult(ctx)
		return err
	})

	call(t, "Generating.RunResult", `{"name":"x"}`, func(ctx context.Context) error {
		_, err := Generating[reachTarget]("make something").RunResult(ctx)
		return err
	})

	call(t, "Filtering.RunResult", `{"ids":["i-000001"]}`, func(ctx context.Context) error {
		_, err := Filtering([]string{"a", "b"}).By("the good ones").RunResult(ctx)
		return err
	})

	call(t, "Sorting.RunResult", `{"ids":["i-000001","i-000002"]}`, func(ctx context.Context) error {
		_, err := Sorting([]string{"a", "b"}).By("alphabetically").RunResult(ctx)
		return err
	})
}

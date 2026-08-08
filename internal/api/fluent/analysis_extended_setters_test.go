package fluent

import (
	"testing"
)

// The rest of fluent_analysis.go's and fluent_extended.go's builders each add
// one or two fields of their own beyond the seven requestBase covers
// (Categories, Scale, Aspects, Style, Factor, and so on). reach_test.go and
// analysis_extended_reach_test.go proved these builders dispatch; this file
// proves each type-specific setter actually moves the field it names,
// following setters_test.go's build/set/read-back rule rather than trusting
// that a method named after a field reaches it.

func TestClassifyRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Classifying[stubExtractTarget, string](stubExtractTarget{Name: "x"}).
		Categories("a", "b").
		MultiLabel(true)
	if got := built.opts.Categories; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Categories() = %v, want [a b]", got)
	}
	if !built.opts.MultiLabel {
		t.Error("MultiLabel(true) did not apply")
	}
}

func TestScoreRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Scoring(stubExtractTarget{Name: "x"}).By("quality", "clarity").Scale(0, 10)
	if got := built.opts.Criteria; len(got) != 2 || got[0] != "quality" {
		t.Errorf("By() = %v, want [quality clarity]", got)
	}
	if built.opts.ScaleMin != 0 || built.opts.ScaleMax != 10 {
		t.Errorf("Scale() = [%v, %v], want [0, 10]", built.opts.ScaleMin, built.opts.ScaleMax)
	}
}

func TestCompareRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Comparing(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).
		Aspects("price", "quality").
		Focus("price")
	if got := built.opts.ComparisonAspects; len(got) != 2 || got[0] != "price" {
		t.Errorf("Aspects() = %v, want [price quality]", got)
	}
	if built.opts.FocusOn != "price" {
		t.Errorf("Focus() = %q, want %q", built.opts.FocusOn, "price")
	}
}

// SimilarRequest.Threshold overrides requestBase's -- it writes
// opts.SimilarityThreshold, a field distinct from the OpOptions.Threshold
// requestBase's own setThreshold closure writes (see fluent_analysis.go).
// Both are exercised here since they are, despite the shared method name,
// two different fields.
func TestSimilarRequest_TypeSpecificSettersApply(t *testing.T) {
	built := CheckingSimilarity(stubExtractTarget{Name: "a"}, stubExtractTarget{Name: "b"}).
		Aspects("wording").
		Threshold(0.77)
	if got := built.opts.Aspects; len(got) != 1 || got[0] != "wording" {
		t.Errorf("Aspects() = %v, want [wording]", got)
	}
	if built.opts.SimilarityThreshold != 0.77 {
		t.Errorf("Threshold() = %v, want 0.77 (SimilarityThreshold, not OpOptions.Threshold)", built.opts.SimilarityThreshold)
	}
}

func TestParseRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Parsing[stubExtractTarget]("x").
		AllowLLMFallback(true).
		AutoFix(true).
		FormatHints("pipe-delimited", "name|age")
	if !built.opts.AllowLLMFallback {
		t.Error("AllowLLMFallback(true) did not apply")
	}
	if !built.opts.AutoFix {
		t.Error("AutoFix(true) did not apply")
	}
	if got := built.opts.FormatHints; len(got) != 2 || got[0] != "pipe-delimited" {
		t.Errorf("FormatHints() = %v, want [pipe-delimited name|age]", got)
	}
}

func TestSummarizeRequest_MaxLengthApplies(t *testing.T) {
	built := Summarizing("some text").MaxLength(42)
	if built.opts.TargetLength != 42 {
		t.Errorf("MaxLength() = %d, want 42 (TargetLength)", built.opts.TargetLength)
	}
}

func TestRewriteRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Rewriting("some text").Style("formal").Tone("upbeat")
	if built.opts.StyleGuide != "formal" {
		t.Errorf("Style() = %q, want %q (StyleGuide)", built.opts.StyleGuide, "formal")
	}
	if built.opts.TargetTone != "upbeat" {
		t.Errorf("Tone() = %q, want %q (TargetTone)", built.opts.TargetTone, "upbeat")
	}
}

func TestTranslateRequest_ToApplies(t *testing.T) {
	built := Translating("hello").To("French")
	if built.opts.TargetLanguage != "French" {
		t.Errorf("To() = %q, want %q (TargetLanguage)", built.opts.TargetLanguage, "French")
	}
}

func TestExpandRequest_FactorApplies(t *testing.T) {
	built := Expanding("brief").Factor(2.5)
	if built.opts.ExpansionFactor != 2.5 {
		t.Errorf("Factor() = %v, want 2.5 (ExpansionFactor)", built.opts.ExpansionFactor)
	}
}

func TestSuggestRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Suggesting[string]("name ideas").
		Top(5).
		Strategy(SuggestStrategy("diverse")).
		Constraints("must be short", "no numbers")
	if built.opts.TopN != 5 {
		t.Errorf("Top() = %d, want 5 (TopN)", built.opts.TopN)
	}
	if built.opts.Strategy != SuggestStrategy("diverse") {
		t.Errorf("Strategy() = %v, want diverse", built.opts.Strategy)
	}
	if got := built.opts.Constraints; len(got) != 2 || got[0] != "must be short" {
		t.Errorf("Constraints() = %v, want [must be short no numbers]", got)
	}
}

// Redact never contacts a provider (internal/ops/redact.go is a pattern
// matcher, AGENTS.md's "decide locally what can be decided locally") -- so
// its builder is proven here by field application, not by reaches().
func TestRedactRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Redacting(stubExtractTarget{Name: "x"}).
		Patterns(`\d{3}-\d{4}`).
		Strategy(RedactStrategy("mask"))
	if got := built.opts.CustomPatterns; len(got) != 1 || got[0] != `\d{3}-\d{4}` {
		t.Errorf("Patterns() = %v, want the one pattern", got)
	}
	if built.opts.Strategy != RedactStrategy("mask") {
		t.Errorf("Strategy() = %v, want mask", built.opts.Strategy)
	}
}

// The deterministic property itself: Redacting never contacts a provider,
// unlike everything else built from an OpOptions/CommonOptions-embedding
// type in this package.
func TestRedactRequest_Run_NeverContactsAProvider(t *testing.T) {
	p := &countingProvider{}
	restore := installStubProvider(t, p)
	defer restore()

	_, err := Redacting("call me at 555-123-4567").Run()
	if err != nil {
		t.Fatalf("Redacting(...).Run() failed: %v", err)
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("provider was contacted %d time(s); Redact is a pattern matcher and should never call a provider", n)
	}
}

func TestRedactTextRequest_CategoriesApplies(t *testing.T) {
	built := LLMRedacting("some text").Categories("email", "phone")
	if got := built.opts.Categories; len(got) != 2 || got[0] != "email" {
		t.Errorf("Categories() = %v, want [email phone]", got)
	}
}

func TestCompleteRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Completing("the quick").MaxLength(100).Temperature(0.9)
	if built.opts.MaxLength != 100 {
		t.Errorf("MaxLength() = %d, want 100", built.opts.MaxLength)
	}
	if built.opts.Temperature != 0.9 {
		t.Errorf("Temperature() = %v, want 0.9", built.opts.Temperature)
	}
}

func TestCompleteFieldRequest_MaxLengthApplies(t *testing.T) {
	built := CompletingField(stubExtractTarget{Name: "partial"}, "Name").MaxLength(50)
	if built.opts.MaxLength != 50 {
		t.Errorf("MaxLength() = %d, want 50", built.opts.MaxLength)
	}
}

func TestValidateRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Validating(stubExtractTarget{Name: "x"}).
		Rules("must be an adult").
		FailOn("critical").
		AutoCorrect(true)
	if built.opts.Rules != "must be an adult" {
		t.Errorf("Rules() = %q, want %q", built.opts.Rules, "must be an adult")
	}
	if built.opts.FailOn != "critical" {
		t.Errorf("FailOn() = %q, want %q", built.opts.FailOn, "critical")
	}
	if !built.opts.AutoCorrect {
		t.Error("AutoCorrect(true) did not apply")
	}
}

func TestQuestionRequest_QuestionApplies(t *testing.T) {
	built := Asking[stubExtractTarget, string](stubExtractTarget{Name: "x"}, "first question").
		Question("second question")
	if built.opts.Question != "second question" {
		t.Errorf("Question() = %q, want %q (last call wins, unlike Steer)", built.opts.Question, "second question")
	}
}

func TestAnnotateRequest_TypesApplies(t *testing.T) {
	built := Annotating(stubExtractTarget{Name: "x"}).Types("sentiment", "tone")
	if got := built.opts.AnnotationTypes; len(got) != 2 || got[0] != "sentiment" {
		t.Errorf("Types() = %v, want [sentiment tone]", got)
	}
}

func TestClusterRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Clustering([]stubExtractTarget{{Name: "a"}, {Name: "b"}}).By("topic").Clusters(3)
	if built.opts.ClusterBy != "topic" {
		t.Errorf("By() = %q, want %q (ClusterBy)", built.opts.ClusterBy, "topic")
	}
	if built.opts.NumClusters != 3 {
		t.Errorf("Clusters() = %d, want 3 (NumClusters)", built.opts.NumClusters)
	}
}

func TestRankRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Ranking([]stubExtractTarget{{Name: "a"}, {Name: "b"}}).By("relevance").Top(5).MinScore(0.3)
	if built.opts.Query != "relevance" {
		t.Errorf("By() = %q, want %q (Query)", built.opts.Query, "relevance")
	}
	if built.opts.TopK != 5 {
		t.Errorf("Top() = %d, want 5 (TopK)", built.opts.TopK)
	}
	if built.opts.MinScore != 0.3 {
		t.Errorf("MinScore() = %v, want 0.3", built.opts.MinScore)
	}
}

func TestSemanticMatchRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Matching([]stubExtractTarget{{Name: "a"}}, []stubExtractTarget{{Name: "b"}}).
		By("sku match").
		Strategy("one-to-one")
	if built.opts.MatchCriteria != "sku match" {
		t.Errorf("By() = %q, want %q (MatchCriteria)", built.opts.MatchCriteria, "sku match")
	}
	if built.opts.Strategy != "one-to-one" {
		t.Errorf("Strategy() = %q, want %q", built.opts.Strategy, "one-to-one")
	}
}

func TestMatchOneRequest_TypeSpecificSettersApply(t *testing.T) {
	built := MatchingOne(stubExtractTarget{Name: "a"}, []stubExtractTarget{{Name: "b"}}).
		By("sku match").
		Strategy("best-fit")
	if built.opts.MatchCriteria != "sku match" {
		t.Errorf("By() = %q, want %q (MatchCriteria)", built.opts.MatchCriteria, "sku match")
	}
	if built.opts.Strategy != "best-fit" {
		t.Errorf("Strategy() = %q, want %q", built.opts.Strategy, "best-fit")
	}
}

func TestSynthesizeRequest_StrategyApplies(t *testing.T) {
	built := Synthesizing[stubExtractTarget]([]any{"a", "b"}).Strategy("consensus")
	if built.opts.Strategy != "consensus" {
		t.Errorf("Strategy() = %q, want %q", built.opts.Strategy, "consensus")
	}
}

func TestPredictRequest_HorizonApplies(t *testing.T) {
	built := Predicting[stubExtractTarget]("history").Horizon("next quarter")
	if built.opts.Horizon != "next quarter" {
		t.Errorf("Horizon() = %q, want %q", built.opts.Horizon, "next quarter")
	}
}

func TestNegotiateRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Negotiating[stubExtractTarget](map[string]any{"budget": "low"}).
		Strategy("collaborative").
		MinimumSatisfaction(0.6)
	if built.opts.Strategy != "collaborative" {
		t.Errorf("Strategy() = %q, want %q", built.opts.Strategy, "collaborative")
	}
	if built.opts.MinSatisfaction != 0.6 {
		t.Errorf("MinimumSatisfaction() = %v, want 0.6 (MinSatisfaction)", built.opts.MinSatisfaction)
	}
}

func TestResolveRequest_StrategyApplies(t *testing.T) {
	built := Resolving([]stubExtractTarget{{Name: "a"}}).Strategy("majority")
	if built.opts.Strategy != "majority" {
		t.Errorf("Strategy() = %q, want %q", built.opts.Strategy, "majority")
	}
}

func TestDeriveRequest_FieldsApplies(t *testing.T) {
	built := Deriving[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).
		Fields("total", "tax")
	if got := built.opts.Fields; len(got) != 2 || got[0] != "total" {
		t.Errorf("Fields() = %v, want [total tax]", got)
	}
}

func TestConformRequest_StrictlyApplies(t *testing.T) {
	built := Conforming(stubExtractTarget{Name: "x"}, "a standard").Strictly(true)
	if !built.opts.Strict {
		t.Error("Strictly(true) did not apply")
	}
}

func TestProjectRequest_ExcludeApplies(t *testing.T) {
	built := Projecting[stubExtractTarget, stubExtractTarget](stubExtractTarget{Name: "x"}).
		Exclude("internal_id", "secret")
	if got := built.opts.Exclude; len(got) != 2 || got[0] != "internal_id" {
		t.Errorf("Exclude() = %v, want [internal_id secret]", got)
	}
}

func TestAuditRequest_TypeSpecificSettersApply(t *testing.T) {
	built := Auditing(stubExtractTarget{Name: "x"}).
		Policies("gdpr", "ccpa").
		Categories("privacy", "security")
	if got := built.opts.Policies; len(got) != 2 || got[0] != "gdpr" {
		t.Errorf("Policies() = %v, want [gdpr ccpa]", got)
	}
	if got := built.opts.Categories; len(got) != 2 || got[0] != "privacy" {
		t.Errorf("Categories() = %v, want [privacy security]", got)
	}
}

// requestBase.Model (fluent_base.go) is exercised by setters_test.go only
// against the six core builders (entrypoints.go's own Model methods, which
// shadow requestBase's for those six). Every requestBase-based type in
// fluent_analysis.go/fluent_extended.go/fluent_advanced.go routes through
// requestBase.Model itself; this is that path, checked via readField the
// same way setters_test.go checks it -- Model lives on both CommonOptions
// and types.OpOptions (see fluent_base.go's doc comment on the three option
// shapes), so a direct selector is ambiguous and readField's explicit
// embedded-struct walk is required, not optional.
func TestRequestBaseModel_AppliesOnAdvancedAndAnalysisBuilders(t *testing.T) {
	const pin = "pinned-advanced-model"

	cases := []struct {
		name string
		opts any
	}{
		{"Classifying", Classifying[stubExtractTarget, string](stubExtractTarget{Name: "x"}).Model(pin).opts},
		{"Scoring", Scoring(stubExtractTarget{Name: "x"}).Model(pin).opts},
		{"Annotating", Annotating(stubExtractTarget{Name: "x"}).Model(pin).opts},
		{"Negotiating", Negotiating[stubExtractTarget](map[string]any{}).Model(pin).opts},
		{"Resolving", Resolving([]stubExtractTarget{{Name: "a"}}).Model(pin).opts},
		{"Auditing", Auditing(stubExtractTarget{Name: "x"}).Model(pin).opts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireSet(t, tc.name, "Model", tc.opts, pin)
		})
	}
}

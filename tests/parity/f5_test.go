package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The operations on lists (Choose, Filter, Sort, Rank, SemanticMatch) and the
// four judgments (Validate, Verify, Audit, Critique), each against the old root's
// function of the same request. Each case is one pair, and the common settings are
// the one table of l0d_test.go that every function runs. Where a setting has a
// meaning in version 2 that the old root says another way (a negated bool, a
// negative number), the pair names the old root's way beside it.
//
// The calls whose request is not one request are not here: Filter over a list too
// long for one answer and Sort over more than 50 items send a request for each
// chunk or item at once, in no fixed order, and the comparison is in order. Their
// requests are the same code's, and the tests of v2 hold the count.

// ---- Choose ----

var chooseParityOptions = []string{"alpha", "beta", "gamma"}

const chooseParityAnswer = `{"id":"i-000002"}`

func chooseCase(name string, old oldroot.ChooseOptions, v2 schemaflux.ChooseOptions) parityCase {
	return parityCase{
		name: "Choose/" + name, op: "choose", replies: []string{chooseParityAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Choose(chooseParityOptions, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Choose(ctx, chooseParityOptions, v2)
			return err
		},
	}
}

func chooseCases() []parityCase {
	criteria := []string{"under $1500", "lightest"}
	base := oldroot.NewChooseOptions
	cases := []parityCase{
		chooseCase("the default", base(), schemaflux.ChooseOptions{}),
		chooseCase("Criteria", base().WithCriteria(criteria), schemaflux.ChooseOptions{Criteria: criteria}),
		chooseCase("Strategy tournament", withChooseStrategy(base(), "tournament"), schemaflux.ChooseOptions{Strategy: "tournament"}),
		chooseCase("Strategy sequential", withChooseStrategy(base(), "sequential"), schemaflux.ChooseOptions{Strategy: "sequential"}),
		chooseCase("Strategy scoring, the default spelled out", withChooseStrategy(base(), "scoring"), schemaflux.ChooseOptions{Strategy: "scoring"}),
		chooseCase("OmitReasoning", base().WithRequireReasoning(false), schemaflux.ChooseOptions{OmitReasoning: true}),
		chooseCase("every own setting", withChooseStrategy(base().WithCriteria(criteria).WithRequireReasoning(false), "tournament"),
			schemaflux.ChooseOptions{Criteria: criteria, Strategy: "tournament", OmitReasoning: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, chooseCase(c.name, old, schemaflux.ChooseOptions{Options: c.v2}))
	}
	return cases
}

func withChooseStrategy(o oldroot.ChooseOptions, strategy string) oldroot.ChooseOptions {
	o.Strategy = strategy
	return o
}

// ---- Filter ----

var filterParityItems = []string{"a", "b", "c", "d"}

const (
	filterParityCriteria = "short names"
	filterParityAnswer   = `{"ids":["i-000001","i-000003"]}`
)

func filterCase(name string, old oldroot.FilterOptions, v2 schemaflux.FilterOptions) parityCase {
	return parityCase{
		name: "Filter/" + name, op: "filter", replies: []string{filterParityAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Filter(filterParityItems, old.WithCriteria(filterParityCriteria))
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Filter(ctx, filterParityItems, filterParityCriteria, v2)
			return err
		},
	}
}

func filterCases() []parityCase {
	base := oldroot.NewFilterOptions
	exclude := base()
	exclude.KeepMatching = false
	cases := []parityCase{
		filterCase("the default", base(), schemaflux.FilterOptions{}),
		filterCase("Exclude", exclude, schemaflux.FilterOptions{Exclude: true}),
		filterCase("MinConfidence 0.9", base().WithMinConfidence(0.9), schemaflux.FilterOptions{MinConfidence: 0.9}),
		filterCase("MinConfidence 0.3", base().WithMinConfidence(0.3), schemaflux.FilterOptions{MinConfidence: 0.3}),
		filterCase("MinConfidence 1", base().WithMinConfidence(1), schemaflux.FilterOptions{MinConfidence: 1}),
		// A negative value leaves the instruction out, which the old root says as a
		// confidence of zero.
		filterCase("MinConfidence negative leaves the instruction out", base().WithMinConfidence(0), schemaflux.FilterOptions{MinConfidence: -1}),
		filterCase("MinConfidence barely negative", base().WithMinConfidence(0), schemaflux.FilterOptions{MinConfidence: -0.001}),
		filterCase("IncludeReasons", base().WithIncludeReasons(true), schemaflux.FilterOptions{IncludeReasons: true}),
		filterCase("every own setting", func() oldroot.FilterOptions {
			o := base().WithMinConfidence(0.9).WithIncludeReasons(true)
			o.KeepMatching = false
			return o
		}(), schemaflux.FilterOptions{Exclude: true, MinConfidence: 0.9, IncludeReasons: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, filterCase(c.name, old, schemaflux.FilterOptions{Options: c.v2}))
	}
	return cases
}

// ---- Sort ----

var sortParityItems = []string{"low", "medium", "critical"}

const (
	sortParityCriteria = "most urgent first"
	sortParityAnswer   = `{"ids":["i-000003","i-000001","i-000002"]}`
)

func sortCase(name string, old oldroot.SortOptions, v2 schemaflux.SortOptions) parityCase {
	return parityCase{
		name: "Sort/" + name, op: "sort", replies: []string{sortParityAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Sort(sortParityItems, old.WithCriteria(sortParityCriteria))
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Sort(ctx, sortParityItems, sortParityCriteria, v2)
			return err
		},
	}
}

func sortCases() []parityCase {
	base := oldroot.NewSortOptions
	unstable := base()
	unstable.Stable = false
	logic := base()
	logic.ComparisonLogic = "numeric, not alphabetic"
	scores := base()
	scores.IncludeScores = true
	every := base().WithDirection("descending").WithSecondaryCriteria([]string{"by name"})
	every.ComparisonLogic, every.Stable, every.IncludeScores = "numeric", false, true
	cases := []parityCase{
		sortCase("the default", base(), schemaflux.SortOptions{}),
		sortCase("Direction descending", base().WithDirection("descending"), schemaflux.SortOptions{Direction: "descending"}),
		sortCase("Direction ascending, the default spelled out", base().WithDirection("ascending"), schemaflux.SortOptions{Direction: "ascending"}),
		sortCase("SecondaryCriteria", base().WithSecondaryCriteria([]string{"by name", "by age"}), schemaflux.SortOptions{SecondaryCriteria: []string{"by name", "by age"}}),
		sortCase("ComparisonLogic", logic, schemaflux.SortOptions{ComparisonLogic: "numeric, not alphabetic"}),
		sortCase("NoStableOrder", unstable, schemaflux.SortOptions{NoStableOrder: true}),
		sortCase("IncludeScores", scores, schemaflux.SortOptions{IncludeScores: true}),
		sortCase("every own setting", every, schemaflux.SortOptions{
			Direction: "descending", SecondaryCriteria: []string{"by name"}, ComparisonLogic: "numeric", NoStableOrder: true, IncludeScores: true,
		}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, sortCase(c.name, old, schemaflux.SortOptions{Options: c.v2}))
	}
	return cases
}

// ---- Rank ----

var rankParityItems = []string{"x", "y", "z"}

const (
	rankParityQuery  = "postgres connection pooling"
	rankParityAnswer = `{"rankings":[{"index":0,"score":0.5,"explanation":"fits"},{"index":1,"score":0.9,"explanation":"best"},{"index":2,"score":0.1,"explanation":"poor"}]}`
)

func rankCase(name string, old oldroot.RankOptions, v2 schemaflux.RankOptions) parityCase {
	return parityCase{
		name: "Rank/" + name, op: "rank", replies: []string{rankParityAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Rank(rankParityItems, old.WithQuery(rankParityQuery))
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Rank(ctx, rankParityItems, rankParityQuery, v2)
			return err
		},
	}
}

func rankCases() []parityCase {
	base := oldroot.NewRankOptions
	factors := []string{"price", "speed"}
	weights := map[string]float64{"price": 0.4, "speed": 0.6}
	boosts := map[string]float64{"recent": 1.5}
	penalties := map[string]float64{"stale": 2}
	withMaps := func(o oldroot.RankOptions) oldroot.RankOptions {
		o.RankingFactors, o.FactorWeights, o.BoostFields, o.PenalizeFields = factors, weights, boosts, penalties
		return o
	}
	cases := []parityCase{
		rankCase("the default", base(), schemaflux.RankOptions{}),
		rankCase("TopK", base().WithTopK(2), schemaflux.RankOptions{TopK: 2}),
		rankCase("MinScore", base().WithMinScore(0.25), schemaflux.RankOptions{MinScore: 0.25}),
		rankCase("Factors", base().WithRankingFactors(factors), schemaflux.RankOptions{Factors: factors}),
		rankCase("FactorWeights", base().WithRankingFactors(factors).WithFactorWeights(weights), schemaflux.RankOptions{Factors: factors, FactorWeights: weights}),
		rankCase("BoostFields", base().WithBoostFields(boosts), schemaflux.RankOptions{BoostFields: boosts}),
		rankCase("PenalizeFields", base().WithPenalizeFields(penalties), schemaflux.RankOptions{PenalizeFields: penalties}),
		rankCase("IncludeExplanation", base().WithIncludeExplanation(true), schemaflux.RankOptions{IncludeExplanation: true}),
		rankCase("every own setting", withMaps(base().WithTopK(2).WithMinScore(0.25).WithIncludeExplanation(true)),
			schemaflux.RankOptions{TopK: 2, MinScore: 0.25, Factors: factors, FactorWeights: weights, BoostFields: boosts, PenalizeFields: penalties, IncludeExplanation: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, rankCase(c.name, old, schemaflux.RankOptions{Options: c.v2}))
	}
	return cases
}

// ---- SemanticMatch ----

var (
	matchParitySources = []string{"r1", "r2"}
	matchParityTargets = []string{"j1", "j2", "j3"}
)

const matchParityAnswer = `{"matches":[` +
	`{"source_index":0,"target_index":1,"score":0.9,"explanation":"same role","is_partial":false},` +
	`{"source_index":1,"target_index":2,"score":0.8,"explanation":"near","is_partial":false}],` +
	`"unmatched_sources":[],"unmatched_targets":[0]}`

func semanticMatchCase(name string, old oldroot.MatchOptions, v2 schemaflux.SemanticMatchOptions) parityCase {
	return parityCase{
		name: "SemanticMatch/" + name, op: "semanticmatch", replies: []string{matchParityAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.SemanticMatch(matchParitySources, matchParityTargets, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.SemanticMatch(ctx, matchParitySources, matchParityTargets, v2)
			return err
		},
	}
}

func semanticMatchCases() []parityCase {
	base := oldroot.NewMatchOptions
	fields := []string{"name", "sku"}
	weights := map[string]float64{"sku": 2}
	noExplanations := base().WithIncludeExplanations(false)
	noPartial := base().WithAllowPartial(false)
	every := base().WithStrategy("one-to-one").WithThreshold(0.6).WithMaxMatches(2).WithMatchFields(fields).WithFieldWeights(weights).
		WithIncludeExplanations(false).WithAllowPartial(false).WithMatchCriteria("same seniority").WithBidirectional(true)
	cases := []parityCase{
		semanticMatchCase("the default", base(), schemaflux.SemanticMatchOptions{}),
		semanticMatchCase("Criteria", base().WithMatchCriteria("same seniority"), schemaflux.SemanticMatchOptions{Criteria: "same seniority"}),
		semanticMatchCase("Strategy one-to-one", base().WithStrategy("one-to-one"), schemaflux.SemanticMatchOptions{Strategy: "one-to-one"}),
		semanticMatchCase("Strategy all-matches", base().WithStrategy("all-matches"), schemaflux.SemanticMatchOptions{Strategy: "all-matches"}),
		semanticMatchCase("Strategy one-to-many", base().WithStrategy("one-to-many"), schemaflux.SemanticMatchOptions{Strategy: "one-to-many"}),
		semanticMatchCase("Threshold", base().WithThreshold(0.7), schemaflux.SemanticMatchOptions{Threshold: 0.7}),
		// A negative threshold means 0 and a negative MaxMatches means no limit, which
		// the old root says as zero.
		semanticMatchCase("Threshold negative", base().WithThreshold(0), schemaflux.SemanticMatchOptions{Threshold: -1}),
		semanticMatchCase("MaxMatches", base().WithMaxMatches(2), schemaflux.SemanticMatchOptions{MaxMatches: 2}),
		semanticMatchCase("MaxMatches negative means no limit", base().WithMaxMatches(0), schemaflux.SemanticMatchOptions{MaxMatches: -1}),
		semanticMatchCase("Fields", base().WithMatchFields(fields), schemaflux.SemanticMatchOptions{Fields: fields}),
		semanticMatchCase("FieldWeights", base().WithMatchFields(fields).WithFieldWeights(weights), schemaflux.SemanticMatchOptions{Fields: fields, FieldWeights: weights}),
		semanticMatchCase("OmitExplanations", noExplanations, schemaflux.SemanticMatchOptions{OmitExplanations: true}),
		semanticMatchCase("NoPartial", noPartial, schemaflux.SemanticMatchOptions{NoPartial: true}),
		semanticMatchCase("Bidirectional", base().WithBidirectional(true), schemaflux.SemanticMatchOptions{Bidirectional: true}),
		semanticMatchCase("OmitExplanations with Bidirectional", base().WithIncludeExplanations(false).WithBidirectional(true),
			schemaflux.SemanticMatchOptions{OmitExplanations: true, Bidirectional: true}),
		semanticMatchCase("every own setting", every, schemaflux.SemanticMatchOptions{
			Criteria: "same seniority", Strategy: "one-to-one", Threshold: 0.6, MaxMatches: 2, Fields: fields, FieldWeights: weights,
			OmitExplanations: true, NoPartial: true, Bidirectional: true,
		}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, semanticMatchCase(c.name, old, schemaflux.SemanticMatchOptions{Options: c.v2}))
	}
	return cases
}

// ---- Validate ----

type judgeParityRecord struct {
	Email string `json:"email"`
	Age   int    `json:"age"`
}

var judgeParityData = judgeParityRecord{Email: "not-an-email", Age: 17}

const (
	validateParityRules  = "the email has to be a real address"
	validateParityAnswer = `{"errors":[{"field":"email","message":"bad","suggestion":"fix","explanation":"x"}],"warnings":[],"info":[],"confidence":0.9,"summary":"one problem"}`
)

func validateCase(name string, old oldroot.ValidateOptions, v2 schemaflux.ValidateOptions) parityCase {
	return parityCase{
		name: "Validate/" + name, op: "validate", replies: []string{validateParityAnswer}, floor: schemaflux.ContractSchemaConstrained,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.ValidateHybrid(judgeParityData, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Validate(ctx, judgeParityData, v2)
			return err
		},
	}
}

func validateCases() []parityCase {
	base := func() oldroot.ValidateOptions { return oldroot.NewValidateOptions().WithRules(validateParityRules) }
	rules := schemaflux.ValidateOptions{Rules: validateParityRules}
	fieldRules := map[string]string{"age": "plausible for a customer"}
	hints := map[string]string{"age": "in years"}
	noCorrection := base().WithAutoCorrect(false)
	noExplanations := base().WithIncludeExplanations(false)
	every := base().WithFieldRules(fieldRules).WithSchemaHints(hints).WithFailOn("warning").WithAutoCorrect(false).WithIncludeExplanations(false)
	cases := []parityCase{
		validateCase("Rules", base(), rules),
		validateCase("FieldRules", oldroot.NewValidateOptions().WithFieldRules(fieldRules), schemaflux.ValidateOptions{FieldRules: fieldRules}),
		validateCase("SchemaHints", oldroot.NewValidateOptions().WithSchemaHints(hints), schemaflux.ValidateOptions{SchemaHints: hints}),
		validateCase("FailOn warning", base().WithFailOn("warning"), schemaflux.ValidateOptions{Rules: validateParityRules, FailOn: "warning"}),
		validateCase("FailOn info", base().WithFailOn("info"), schemaflux.ValidateOptions{Rules: validateParityRules, FailOn: "info"}),
		validateCase("FailOn error, the default spelled out", base().WithFailOn("error"), schemaflux.ValidateOptions{Rules: validateParityRules, FailOn: "error"}),
		validateCase("OmitCorrection", noCorrection, schemaflux.ValidateOptions{Rules: validateParityRules, OmitCorrection: true}),
		validateCase("OmitExplanations", noExplanations, schemaflux.ValidateOptions{Rules: validateParityRules, OmitExplanations: true}),
		validateCase("every own setting", every, schemaflux.ValidateOptions{
			Rules: validateParityRules, FieldRules: fieldRules, SchemaHints: hints, FailOn: "warning", OmitCorrection: true, OmitExplanations: true,
		}),
		validateCase("a rule Go decides beside one it cannot", oldroot.NewValidateOptions().WithFieldRules(map[string]string{"email": "email", "age": "plausible"}),
			schemaflux.ValidateOptions{FieldRules: map[string]string{"email": "email", "age": "plausible"}}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, validateCase(c.name, old, schemaflux.ValidateOptions{Rules: validateParityRules, Options: c.v2}))
	}
	return cases
}

// ---- Verify ----

const (
	verifyParityText          = "The Earth is flat."
	verifyParityClaim         = `{"claim":"c1","verdict":"verified","confidence":0.9,"corrections":"","evidence":["e1"],"reasoning":"r1"`
	verifyParityAnswer        = `{"summary":"s","overall_confidence":0.9,"trust_score":0.8,"claims":[` + verifyParityClaim + `}],"logic_issues":[],"consistency_issues":[]}`
	verifyParityAnswerSources = `{"summary":"s","overall_confidence":0.9,"trust_score":0.8,"claims":[` + verifyParityClaim + `,"sources":[0]}],"logic_issues":[],"consistency_issues":[]}`
)

func verifyCase(name, answer string, old oldroot.VerifyOptions, v2 schemaflux.VerifyOptions) parityCase {
	return parityCase{
		name: "Verify/" + name, op: "verify", replies: []string{answer}, floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.VerifyWithModel(verifyParityText, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Verify(ctx, verifyParityText, v2)
			return err
		},
	}
}

func verifyCases() []parityCase {
	base := oldroot.NewVerifyOptions
	sources := []any{"the filing", map[string]string{"k": "v"}}
	trusted := []string{"the registry"}
	with := func(change func(o *oldroot.VerifyOptions)) oldroot.VerifyOptions {
		o := base()
		change(&o)
		return o
	}
	cases := []parityCase{
		verifyCase("the default", verifyParityAnswer, base(), schemaflux.VerifyOptions{}),
		verifyCase("Sources", verifyParityAnswerSources, base().WithSources(sources), schemaflux.VerifyOptions{Sources: sources}),
		verifyCase("Strictness strict", verifyParityAnswer, base().WithStrictness("strict"), schemaflux.VerifyOptions{Strictness: "strict"}),
		verifyCase("Strictness lenient", verifyParityAnswer, base().WithStrictness("lenient"), schemaflux.VerifyOptions{Strictness: "lenient"}),
		verifyCase("Domain", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.Domain = "oncology" }), schemaflux.VerifyOptions{Domain: "oncology"}),
		verifyCase("TrustedSources", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.TrustedSources = trusted }), schemaflux.VerifyOptions{TrustedSources: trusted}),
		verifyCase("MinConfidence", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.MinConfidence = 0.5 }), schemaflux.VerifyOptions{MinConfidence: 0.5}),
		// A negative floor is a floor of zero, which the old root says as zero.
		verifyCase("MinConfidence negative", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.MinConfidence = 0 }), schemaflux.VerifyOptions{MinConfidence: -1}),
		verifyCase("SkipFacts", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.CheckFacts = false }), schemaflux.VerifyOptions{SkipFacts: true}),
		verifyCase("SkipLogic", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.CheckLogic = false }), schemaflux.VerifyOptions{SkipLogic: true}),
		verifyCase("SkipConsistency", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.CheckConsistency = false }), schemaflux.VerifyOptions{SkipConsistency: true}),
		verifyCase("OmitEvidence", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.IncludeEvidence = false }), schemaflux.VerifyOptions{OmitEvidence: true}),
		verifyCase("OmitReasoning", verifyParityAnswer, with(func(o *oldroot.VerifyOptions) { o.ExplainReasoning = false }), schemaflux.VerifyOptions{OmitReasoning: true}),
		verifyCase("every own setting", verifyParityAnswerSources, with(func(o *oldroot.VerifyOptions) {
			o.Sources, o.TrustedSources, o.Domain, o.Strictness, o.MinConfidence = sources, trusted, "oncology", "strict", 0.5
			o.CheckLogic, o.CheckConsistency, o.IncludeEvidence, o.ExplainReasoning = false, false, false, false
		}), schemaflux.VerifyOptions{
			Sources: sources, TrustedSources: trusted, Domain: "oncology", Strictness: "strict", MinConfidence: 0.5,
			SkipLogic: true, SkipConsistency: true, OmitEvidence: true, OmitReasoning: true,
		}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, verifyCase(c.name, verifyParityAnswer, old, schemaflux.VerifyOptions{Options: c.v2}))
	}
	return cases
}

// ---- Audit ----

const (
	auditParityAnswer         = `{"findings":[]}`
	auditParityPolicyAnswer   = `{"findings":[],"policies":{"p1":[],"p2":[]}}`
	auditParityPolicyOne      = `{"findings":[],"policies":{"p1":[]}}`
	auditParityPolicyFirst    = "no plain text passwords"
	auditParityPolicySecond   = "names must be present"
	auditParityPolicyCategory = "security"
)

func auditCase(name, answer string, old oldroot.AuditOptions, v2 schemaflux.AuditOptions) parityCase {
	return parityCase{
		name: "Audit/" + name, op: "audit", replies: []string{answer}, floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.AuditWithModel(judgeParityData, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Audit(ctx, judgeParityData, v2)
			return err
		},
	}
}

// auditCommon lays a common setting of the table onto the old root's audit
// options, which spell the seven out as fields of their own and not as the two
// halves the table writes: it writes the table's setting to the two halves and
// copies what it set.
func auditCommon(c commonCase) oldroot.AuditOptions {
	var common ops.CommonOptions
	var embedded types.OpOptions
	c.flat(&common, &embedded)
	return oldroot.AuditOptions{
		Model: common.Model, Intelligence: common.Intelligence, Effort: common.Effort, Mode: common.Mode, Steering: common.Steering,
		MaxOutputTokens: embedded.MaxOutputTokens, WebSearch: embedded.WebSearch,
	}
}

func auditCases() []parityCase {
	policies := []string{auditParityPolicyFirst, auditParityPolicySecond}
	categories := []string{auditParityPolicyCategory, "privacy"}
	cases := []parityCase{
		auditCase("the default", auditParityAnswer, oldroot.AuditOptions{}, schemaflux.AuditOptions{}),
		auditCase("Policies", auditParityPolicyAnswer, oldroot.AuditOptions{Policies: policies}, schemaflux.AuditOptions{Policies: policies}),
		auditCase("one policy", auditParityPolicyOne, oldroot.AuditOptions{Policies: policies[:1]}, schemaflux.AuditOptions{Policies: policies[:1]}),
		auditCase("Categories", auditParityAnswer, oldroot.AuditOptions{Categories: categories}, schemaflux.AuditOptions{Categories: categories}),
		auditCase("Policies with Categories", auditParityPolicyAnswer, oldroot.AuditOptions{Policies: policies, Categories: categories},
			schemaflux.AuditOptions{Policies: policies, Categories: categories}),
		auditCase("Threshold", auditParityAnswer, oldroot.AuditOptions{Threshold: 0.5}, schemaflux.AuditOptions{Threshold: 0.5}),
		auditCase("Shallow", auditParityAnswer, oldroot.AuditOptions{Shallow: true}, schemaflux.AuditOptions{Shallow: true}),
		auditCase("every own setting", auditParityPolicyAnswer,
			oldroot.AuditOptions{Policies: policies, Categories: categories, Threshold: 0.5, Shallow: true},
			schemaflux.AuditOptions{Policies: policies, Categories: categories, Threshold: 0.5, Shallow: true}),
	}
	for _, c := range commonCases {
		cases = append(cases, auditCase(c.name, auditParityAnswer, auditCommon(c), schemaflux.AuditOptions{Options: c.v2}))
	}
	return cases
}

// ---- Critique ----

var critiqueParityCriteria = []string{"clarity", "argument"}

const (
	critiqueParityText   = "An essay about tides."
	critiqueParityAnswer = `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6},"issues":[],"summary":"readable","top_priorities":[]}`
	critiqueParityRubric = `{"overall_score":0.7,"criteria_scores":{"clarity":0.8,"argument":0.6,"tone":0.5},"issues":[],"summary":"readable","top_priorities":[]}`
)

func critiqueCase(name, answer string, criteria []string, old oldroot.CritiqueOptions, v2 schemaflux.CritiqueOptions) parityCase {
	return parityCase{
		name: "Critique/" + name, op: "critique", replies: []string{answer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.CritiqueWithModel(critiqueParityText, old.WithCriteria(criteria))
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Critique(ctx, critiqueParityText, criteria, v2)
			return err
		},
	}
}

func critiqueCases() []parityCase {
	base := oldroot.NewCritiqueOptions
	rubric := map[string]string{"tone": "is it polite?"}
	with := func(change func(o *oldroot.CritiqueOptions)) oldroot.CritiqueOptions {
		o := base()
		change(&o)
		return o
	}
	cases := []parityCase{
		critiqueCase("the default", critiqueParityAnswer, critiqueParityCriteria, base(), schemaflux.CritiqueOptions{}),
		critiqueCase("Rubric beside criteria", critiqueParityRubric, critiqueParityCriteria, base().WithRubric(rubric), schemaflux.CritiqueOptions{Rubric: rubric}),
		critiqueCase("Rubric in place of criteria", `{"overall_score":0.7,"criteria_scores":{"tone":0.5},"issues":[],"summary":"s","top_priorities":[]}`, nil,
			base().WithRubric(rubric), schemaflux.CritiqueOptions{Rubric: rubric}),
		critiqueCase("Style harsh", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.Style = "harsh" }), schemaflux.CritiqueOptions{Style: "harsh"}),
		critiqueCase("Style balanced", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.Style = "balanced" }), schemaflux.CritiqueOptions{Style: "balanced"}),
		critiqueCase("SeverityFilter critical", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.SeverityFilter = "critical" }), schemaflux.CritiqueOptions{SeverityFilter: "critical"}),
		critiqueCase("SeverityFilter all, the default spelled out", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.SeverityFilter = "all" }), schemaflux.CritiqueOptions{SeverityFilter: "all"}),
		critiqueCase("MaxIssues", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.MaxIssues = 5 }), schemaflux.CritiqueOptions{MaxIssues: 5}),
		critiqueCase("Domain", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.Domain = "law" }), schemaflux.CritiqueOptions{Domain: "law"}),
		critiqueCase("Audience", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.Audience = "juniors" }), schemaflux.CritiqueOptions{Audience: "juniors"}),
		critiqueCase("OmitSuggestions", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.IncludeSuggestions = false }), schemaflux.CritiqueOptions{OmitSuggestions: true}),
		critiqueCase("OmitFixes", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.IncludeFixes = false }), schemaflux.CritiqueOptions{OmitFixes: true}),
		critiqueCase("OmitPositives", critiqueParityAnswer, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) { o.IncludePositives = false }), schemaflux.CritiqueOptions{OmitPositives: true}),
		critiqueCase("every own setting", critiqueParityRubric, critiqueParityCriteria, with(func(o *oldroot.CritiqueOptions) {
			o.Rubric, o.Style, o.SeverityFilter, o.MaxIssues, o.Domain, o.Audience = rubric, "harsh", "major", 3, "law", "juniors"
			o.IncludeSuggestions, o.IncludeFixes, o.IncludePositives = false, false, false
		}), schemaflux.CritiqueOptions{
			Rubric: rubric, Style: "harsh", SeverityFilter: "major", MaxIssues: 3, Domain: "law", Audience: "juniors",
			OmitSuggestions: true, OmitFixes: true, OmitPositives: true,
		}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, critiqueCase(c.name, critiqueParityAnswer, critiqueParityCriteria, old, schemaflux.CritiqueOptions{Options: c.v2}))
	}
	return cases
}

// The tests are not parallel: runParity installs an observer, which is process
// state, while it runs its last group.

func TestChooseSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, chooseCases()) }

func TestFilterSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, filterCases()) }

func TestSortSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, sortCases()) }

func TestRankSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, rankCases()) }

func TestSemanticMatchSendsTheRequestTheOldRootSends(t *testing.T) {
	runParity(t, semanticMatchCases())
}

func TestValidateSendsTheRequestTheOldRootsHybridFormSends(t *testing.T) {
	runParity(t, validateCases())
}

func TestVerifySendsTheRequestTheOldRootsModelFormSends(t *testing.T) { runParity(t, verifyCases()) }

func TestAuditSendsTheRequestTheOldRootsModelFormSends(t *testing.T) { runParity(t, auditCases()) }

func TestCritiqueSendsTheRequestTheOldRootsModelFormSends(t *testing.T) {
	runParity(t, critiqueCases())
}

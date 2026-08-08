package schemaflux

import "sort"

// The verb catalogue (PS-002).
//
// This library exposes a lot of operations. The problem was never that they
// exist — it is that `Assemble` and `Extract` carried the same implied promise
// while only one of them had a defined semantic contract, verified invariants,
// and tests to match. A caller had no way to tell which was which, so they
// treated all of them as equally load-bearing, which is how an experiment ends
// up in somebody's invoice pipeline.
//
// So every operation declares two things: the category it belongs to, and the
// promise attached to it. `catalog_test.go` fails the build when an exported
// operation is missing from this table, which is what stops it from drifting
// back into a list nobody maintains.

// OperationCategory is one of the eight stable families. An operation that
// does not fit one is a sign the operation is doing two things.
type OperationCategory string

const (
	CategoryExtract   OperationCategory = "extraction"
	CategoryTransform OperationCategory = "transformation"
	CategoryGenerate  OperationCategory = "generation"
	CategoryClassify  OperationCategory = "classification"
	CategorySelect    OperationCategory = "selection"
	CategoryOrder     OperationCategory = "ordering"
	CategoryReview    OperationCategory = "review"
	CategoryValidate  OperationCategory = "validation"

	// CategoryInfrastructure covers the exported surface that is not an
	// operation at all: clients, options constructors, combinators, control
	// flow, and the plumbing. Kept as its own bucket rather than left out, so
	// the completeness check has an answer for every exported name instead of
	// an exception list that grows quietly.
	CategoryInfrastructure OperationCategory = "infrastructure"
)

// OperationTier is the promise attached to an operation.
type OperationTier string

const (
	// TierStable means the semantic contract is defined, its invariants are
	// checked in Go, and breaking it is a breaking change. Build on these.
	TierStable OperationTier = "stable"

	// TierExperimental means the operation works and is tested, but its
	// contract has not been pinned down: what it promises about its output may
	// change, and it may move to the recipes package or be withdrawn. The
	// distinction exists because giving an operation Extract's compatibility
	// promise without Extract's defined contract is the thing PS-002 was
	// opened to stop.
	TierExperimental OperationTier = "experimental"

	// TierDeprecated means it still works and has a named replacement.
	TierDeprecated OperationTier = "deprecated"
)

// CatalogEntry describes one exported operation.
type CatalogEntry struct {
	Name     string
	Category OperationCategory
	Tier     OperationTier

	// ReplacedBy names the successor for a deprecated entry, so the catalogue
	// answers "what should I use instead" without a search.
	ReplacedBy string
}

// catalog is the table. Anything exported and absent from it fails
// TestEveryExportedOperationIsCatalogued.
var catalog = map[string]CatalogEntry{}

func register(name string, category OperationCategory, tier OperationTier, replacedBy ...string) {
	entry := CatalogEntry{Name: name, Category: category, Tier: tier}
	if len(replacedBy) > 0 {
		entry.ReplacedBy = replacedBy[0]
	}
	catalog[name] = entry
}

// Catalog returns every catalogued operation, sorted by name.
func Catalog() []CatalogEntry {
	entries := make([]CatalogEntry, 0, len(catalog))
	for _, entry := range catalog {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// Describe reports the catalogue entry for an operation, and whether it has
// one. A caller deciding whether to build on an operation can ask.
func Describe(name string) (CatalogEntry, bool) {
	entry, found := catalog[name]
	return entry, found
}

func init() {
	stable := map[OperationCategory][]string{
		CategoryExtract:   {"Extract", "ExtractResult", "Parse", "Infer"},
		CategoryTransform: {"Transform", "Summarize", "SummarizeResult", "Rewrite", "RewriteResult", "Translate", "TranslateResult", "Expand", "ExpandResult", "Normalize", "NormalizeText", "NormalizeBatch", "Redact", "Format", "Merge", "Compress", "CompressText", "Chunk"},
		CategoryGenerate:  {"Generate", "Complete", "CompleteField", "Suggest", "Annotate", "Explain", "Question"},
		CategoryClassify:  {"Classify", "Score", "Cluster", "Compare", "Similar", "Diff", "Deduplicate"},
		CategorySelect:    {"Choose", "ChooseBy", "Filter", "FilterBy", "Decide", "Match", "MatchOne", "SemanticMatch"},
		CategoryOrder:     {"Sort", "SortBy", "SortResult", "Rank"},
		CategoryReview:    {"VerifyWithModel", "AuditWithModel", "CritiqueWithModel", "Guard", "Approve"},
		CategoryValidate:  {"ValidateDeterministically", "ValidateHybrid"},
	}
	for category, names := range stable {
		for _, name := range names {
			register(name, category, TierStable)
		}
	}

	// The Revised line names these as the ones without a defined semantic
	// contract. They work and they are tested; what they do not have is a
	// promise about what their output means that would survive being pinned
	// down. `recipes` is where they are heading.
	experimental := map[OperationCategory][]string{
		CategoryGenerate:  {"Synthesize", "Predict", "Assemble", "Derive", "Interpolate"},
		CategorySelect:    {"Arbitrate", "Resolve", "Settle", "SettleAdversarial", "Vote"},
		CategoryTransform: {"Conform", "Project", "Pivot", "Enrich", "EnrichInPlace", "Decompose", "DecomposeToSlice", "RedactLLM"},
		CategoryClassify:  {"CheckingSimilarity"},
	}
	for category, names := range experimental {
		for _, name := range names {
			register(name, category, TierExperimental)
		}
	}

	deprecated := map[string]string{
		"Validate":              "ValidateHybrid",
		"ValidateLegacy":        "ValidateHybrid",
		"Verify":                "VerifyWithModel",
		"VerifyClaim":           "VerifyWithModel",
		"Audit":                 "AuditWithModel",
		"Critique":              "CritiqueWithModel",
		"Negotiate":             "Settle",
		"NegotiateAdversarial":  "SettleAdversarial",
		"QuestionLegacy":        "Question",
		"SummarizeWithMetadata": "SummarizeResult",
		"RewriteWithMetadata":   "RewriteResult",
		"TranslateWithMetadata": "TranslateResult",
		"ExpandWithMetadata":    "ExpandResult",
		"MergeWithMetadata":     "Merge",
		"FormatWithMetadata":    "Format",
	}
	for name, replacement := range deprecated {
		category := CategoryTransform
		if entry, found := catalog[replacement]; found {
			category = entry.Category
		}
		register(name, category, TierDeprecated, replacement)
	}
}

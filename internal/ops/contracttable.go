package ops

import (
	"sort"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The contract level of every operation.
//
// An operation delivers its answer with a guarantee, and types.ContractLevel
// ranks them: the model was only asked (prompt only), the answer parsed as JSON
// (well formed), it was held to a declared schema (schema constrained), and the
// operation's own rules were also checked in Go (schema and invariants checked).
// A client may set a floor, the minimum contract of its data policy, and a call
// whose level is below it is refused before anything is sent. So the library has
// to know the level of every operation that asks a model, and this file is where
// it is written down. contracttable_test.go holds each row to the code.
//
// HOW A ROW IS EARNED
//
// The rule is the one declaredContractLevel applies to an Op, applied to the
// operations that are not written as one:
//
//   - A level of schema constrained or above needs a JSON schema declared on the
//     request (opt.JSONSchema set before the call), strict with every key
//     required, and an answer that is read into a type. declaredContractLevel
//     ignores an Op's invariants unless it declares a schema, and
//     negotiatedContractLevel treats the schema on the request as the signal that
//     one was asked of the provider.
//   - Schema and invariants checked needs that, and a rule of the operation
//     itself that Go checks on every path that sends a request, and that relates
//     the answer to what the caller gave: a subset, a permutation, a partition, a
//     label or an id that was among those offered, coverage of every item or
//     every part asked for, a span found in the text, a position inside the list.
//     A range check on a model's own confidence or score does not count, because
//     that is a claim about the answer and not a rule about it. A rule that runs
//     only when an option is set does not count either; a rule that runs on every
//     request and has nothing to refuse for an empty answer does.
//   - Well formed is an answer parsed as JSON (decodeAnswer, decodeCallerAnswer,
//     readAnswerObject or ParseJSON) with no schema on the request. Prompt only is
//     the model's text taken as it comes.
//
// decodeAnswer reads into the operation's own type and refuses any required
// member that is missing at any depth. decodeCallerAnswer reads into the caller's
// type and does not: a member the answer leaves out is the zero value, and only
// the shared member of the type is required. A row that rests on it is marked in
// its evidence as reading the caller's type weakly. Its level holds on a route
// that enforces the declared schema, which is what negotiatedContractLevel asks
// of a route: every level at schema constrained or above is lowered to well
// formed on a route that is not known to enforce a schema natively. Only
// RunOpResult, RunOpBatchResult and SortResult apply that today; this table
// states the level before it.
//
// STATIC AND COMPUTED
//
// A row is static when the level is a fact of how the operation reads its answer.
// It is computed when a function works the level out per call, and then its Level
// is the lowest the call can deliver and via names the function: Extract from its
// options (requestedContractOf, deliveredContractOf), Generate from its type
// argument (ContractLevelFor), and the runners of someone else's operation
// from that operation's declaration (declaredContractLevel, batchContractLevel,
// negotiatedContractLevel). A runner delivers the level of the operation it runs:
// its row holds the lowest level that any operation it runs can have, and the
// level of the one it runs is declaredContractLevel(its contract) or
// batchContractLevel(it), then lowered by the route.
//
// Nine rows (those with typeBound set) are for a schema that is built from the
// caller's type, and for a type strict mode cannot express (a map, a recursive
// type, an interface, a type nested deeper than six) none is built and none is
// sent. Those rows hold the level for a type that has a schema, and their
// without field says what they deliver for a type that has none. ContractLevelFor
// (contracttype.go) tells the two apart for a type, by asking what the request
// builder asks, and it is what the Record and the floor check of each of these
// operations read: a call into a type with no schema used to be reported at the
// row's level, and a floor of schema constrained admitted it (ruling R31).
//
// THE SEVEN THAT SEND NO SCHEMA (rulings R24 D1 and R31)
//
// The six that return the caller's own items by position, and Sort. None of them
// declares a schema, so by the rule above none of them can be schema constrained,
// whatever Go checks afterwards. Each also holds its answer to its own type with
// decodeAnswer (readAnswerObject for SemanticMatch) and refuses an answer that
// breaks a rule of the operation. The level is the first column; the last is what
// the same code would earn if a declared Go type, held by the reader, counted for
// the schema, and is the row's alternative.
//
//	operation      schema declared    answer reader                invariants always checked in Go                                  earned  alternative
//	filter         no                 decodeAnswer, ids; null      every id offered, none twice (SubsetOf over ids); the result      well    schema and
//	                                  refused                      is the caller's items in input order. Per chunk when the          formed  invariants
//	                                                               collection is split: each chunk against its own ids.
//	                                                               Not checked: MinConfidence, IncludeReasons. No call when
//	                                                               there are no items.
//	choose         no                 decodeAnswer, id             the id is one offered (MemberOf over ids). No call for one       well    schema and
//	                                                               option; none for none (an error). A decision provider's          formed  invariants
//	                                                               answer is read by readAssessAnswer, a number from 1 to n.
//	rank           no                 decodeAnswer, rankings       every item ranked exactly once (CoversExactlyOnce), each         well    schema and
//	                                                               score from 0 to 1, factor scores only for the factors            formed  invariants
//	                                                               asked about, an explanation when asked for; the order is
//	                                                               worked out in Go from the scores. No call for no items.
//	cluster        no                 decodeAnswer, clusters       a partition (CoversExactlyOnce over the clusters and the         well    schema and
//	                                                               outliers), no outliers unless allowed, MinClusterSize and         formed  invariants
//	                                                               MaxClusterSize. NumClusters, the threshold and the naming
//	                                                               strategy are guidance and deliberately not checked. No call
//	                                                               for fewer than two items.
//	semanticmatch  no                 readAnswerObject, members    every index inside its list, every score from 0 to 1, no        well    schema and
//	                                  read by hand                 pair twice, the threshold, partial pairs only when allowed,       formed  invariants
//	                                                               MaxMatches per source, one to one, bidirectional, and the
//	                                                               unmatched lists exactly the items in no pair. No call when
//	                                                               either side is empty.
//	classify       no                 decodeAnswer, classifyAnswer the category is one offered (CategoryIn); MinConfidence;         well    schema and
//	                                  (a decision provider:        for several labels, that they agree and MaxCategories.            formed  invariants
//	                                  classifyTypedAnswer)         An alternative that is not offered is dropped, not refused.
//	sort           no                 decodeAnswer, ids; null      a whole-list answer (at most 50 items) is a permutation of        well    schema and
//	                                  refused                      the ids offered (SameMultiset over ids), and one that is          formed  invariants
//	                                                               refused is sent to the scoring path. The scoring path (more
//	                                                               than 50 items, or after a refusal) reads one rank_score from
//	                                                               0 to 1 for each item and orders in Go, so the permutation
//	                                                               holds by construction and is not checked. No call for fewer
//	                                                               than two items.
//
// Sort used to claim schema and invariants checked, on the reasoning that the
// answer fits the type and the permutation holds. Neither is what
// declaredContractLevel asks for: no schema is declared, and
// negotiatedContractLevel only lowers a call that sent one, so nothing would
// ever have lowered the claim. Ruling R31 puts it at the table's level, and
// sortContract (collection.go) is well formed. The six others were at that level
// in Table A2 and are there now.
//
// THE DECISIONS
//
// Is, Which, WhichOrNone, Rate and Assess declare the same schema, "assessment"
// (dec12BuildRequest), and read the answer with the same reader and the same
// checks (readAssessAnswer: every question answered once, a choice a number from 1
// to the number of options, a rating a whole number on its scale, nothing for a
// question that was not asked) in their plain forms and in the Result forms. The
// only difference is the confidence: the Result forms put "confidence" in the
// schema and require it, on the scale from 0 to 1, and the plain forms do not.
// So all ten rows are one level. A decision provider that answers typed
// (internal/llm/decision.go) never writes JSON: its answer is a llm.DecisionAnswer
// of a kind, a label, a level and a probability, and llm.RenderDecisionAnswers
// refuses an answer that is not an answer to its question (another kind, a label
// that was not offered, a level off the scale, a probability outside 0 to 1, a
// different number of answers than questions) before readAssessAnswer reads what
// was rendered, with the checks above. The typed answer fits its type by
// construction and the same checks hold, so the reading is that it is delivered
// at the level of the decisions: schema and invariants checked. The request still
// carries the declared schema, which a decision provider does not read. Choose
// and Classify read a typed answer the same way (ruling R31).
//
// WHERE THIS DIFFERS FROM TABLE A2
//
// differsFromTableA2 lists each row that is not A2's and why. A2 also had no rows
// for the runners that were restored (op.runmanypartial, op.runmanyrecover,
// op.runstagedplan), for ExtractBatch, or for the deprecated forms whose request
// differs from the operation that replaces them (the legacy. rows).

// ContractRule is the contract level an operation's answer is delivered with.
type ContractRule struct {
	// Level is the level, or for a computed one the lowest it can be.
	Level types.ContractLevel
	// Computed is true when the level is worked out per call.
	Computed bool
}

// contractRow is one row of the table with what the tests hold it to.
type contractRow struct {
	// name is the name the operation's record carries (Record.Op of the v2
	// root), or for a form that has none the name of the function in lower case
	// under legacy.
	name string
	// level and computed are the rule.
	level    types.ContractLevel
	computed bool
	// via is the function that works the level out, for a computed row.
	via string
	// evidence is the one sentence the row rests on.
	evidence string
	// typeBound is set for a row whose schema is built from the caller's type.
	// without is the level of a call that sends no schema because the type has
	// none.
	typeBound bool
	without   types.ContractLevel
	// alternative is the level the same code earns under the other reading of
	// ruling R24 D1, for the rows it is asked of. Zero means the row is not one.
	alternative types.ContractLevel
	// catalogue names the entries of internal/api/client/catalog.go that this row
	// is the level of, and functions the exported functions of this package.
	catalogue []string
	functions []string
	// aliases are the names the call path puts on a context for the operation.
	aliases []string
}

const (
	promptOnly        = types.ContractPromptOnly
	wellFormed        = types.ContractJSONWellFormed
	schemaConstrained = types.ContractSchemaConstrained
	invariantChecked  = types.ContractSchemaAndInvariantChecked
)

// contractRows is the table.
var contractRows = []contractRow{
	// The extraction family.
	{
		name: "extract", level: schemaConstrained, computed: true, typeBound: true, without: wellFormed,
		via:       "requestedContractOf and deliveredContractOf (extractresult.go), from Mode, ExactFields and CompleteFields, lowered to well formed for a type with no schema (contractWithoutSchema)",
		evidence:  "Extract declares the target type's schema (none for a type that has none, which is well formed) and reads the caller's type weakly; Strict, ExactFields or CompleteFields add DecodeExact and ValidateExtractedData, which lift a call that declared a schema to schema and invariants checked.",
		catalogue: []string{"Extract", "ExtractResult"}, functions: []string{"Extract", "ExtractResult"},
	},
	{
		name: "transform", level: schemaConstrained, typeBound: true, without: wellFormed,
		evidence:  "Transform declares the target type's schema and reads it with decodeCallerAnswer, so a missing member is not refused by Go.",
		catalogue: []string{"Transform"}, functions: []string{"Transform"},
	},
	{
		name: "generate", level: promptOnly, computed: true, typeBound: true, without: wellFormed,
		via:       "ContractLevelFor[T] (contracttype.go), from the type argument: prompt only for a string, schema constrained for a type that has a schema, well formed for one that has none",
		evidence:  "Generate[string] returns the model's text as it came; any other type declares the type's schema and reads it with decodeCallerAnswer.",
		catalogue: []string{"Generate", "GenerateResult"}, functions: []string{"Generate", "GenerateResult"},
	},
	{
		name: "infer", level: wellFormed,
		evidence:  "Infer asks for JSON with no schema, reads it with ParseJSON, requires every field of the type and refuses a supplied value that was changed.",
		catalogue: []string{"Infer"}, functions: []string{"Infer"},
	},
	{
		name: "parse", level: wellFormed,
		evidence:  "The model path of Parse sends no schema and reads with decodeCallerAnswer; the Go parsers send nothing and are held to this row.",
		catalogue: []string{"Parse"}, functions: []string{"Parse"},
	},
	{
		name: "complete", level: promptOnly,
		evidence:  "Complete takes the model's text, cuts it at the stop sequences and refuses one that adds nothing.",
		catalogue: []string{"Complete"}, functions: []string{"Complete"},
	},
	{
		name: "completefield", level: promptOnly,
		evidence:  "CompleteField runs completeImpl and writes the model's text into the field.",
		catalogue: []string{"CompleteField"}, functions: []string{"CompleteField"},
	},
	{
		name: "suggest", level: wellFormed,
		evidence:  "Suggest asks for a JSON list with no schema and decodeSuggestions refuses what is not one, then AtMost holds it to TopN.",
		catalogue: []string{"Suggest"}, functions: []string{"Suggest"},
	},
	{
		name: "annotate", level: wellFormed,
		evidence:  "Annotate sends no schema and reads with decodeAnswer; spans inside the text are checked, but without a schema that stays well formed.",
		catalogue: []string{"Annotate"}, functions: []string{"Annotate"},
	},
	{
		name: "explain", level: wellFormed,
		evidence:  "Explain sends no schema and reads its answer with decodeAnswer.",
		catalogue: []string{"Explain"}, functions: []string{"Explain"},
	},
	{
		name: "question", level: schemaConstrained, typeBound: true, without: wellFormed,
		evidence:  "Question declares an envelope schema when the answer type has one and reads the envelope with decodeAnswer and the answer weakly; nothing relational runs on every call.",
		catalogue: []string{"Question"}, functions: []string{"Question"},
	},

	// The text operations. The plain form takes the model's text; the Detail form
	// is a different request, which asks for JSON with no schema.
	{
		name: "summarize", level: promptOnly, evidence: "Summarize returns the model's text trimmed (plainTextAnswer).",
		catalogue: []string{"Summarize"}, functions: []string{"Summarize"},
	},
	{
		name: "rewrite", level: promptOnly, evidence: "Rewrite returns the model's text trimmed and checks only the word lists.",
		catalogue: []string{"Rewrite"}, functions: []string{"Rewrite"},
	},
	{
		name: "translate", level: promptOnly, evidence: "Translate returns the model's text trimmed and checks only the glossary.",
		catalogue: []string{"Translate"}, functions: []string{"Translate"},
	},
	{
		name: "expand", level: promptOnly, evidence: "Expand returns the model's text trimmed.",
		catalogue: []string{"Expand"}, functions: []string{"Expand"},
	},
	{
		name: "summarizedetail", level: wellFormed,
		evidence:  "SummarizeWithMetadata asks for JSON with no schema and reads it with decodeAnswer; textResultContract is this level.",
		catalogue: []string{"SummarizeResult", "SummarizeWithMetadata"}, functions: []string{"SummarizeResult", "SummarizeWithMetadata"},
	},
	{
		name: "rewritedetail", level: wellFormed,
		evidence:  "RewriteWithMetadata asks for JSON with no schema and reads it with decodeAnswer.",
		catalogue: []string{"RewriteResult", "RewriteWithMetadata"}, functions: []string{"RewriteResult", "RewriteWithMetadata"},
	},
	{
		name: "translatedetail", level: wellFormed,
		evidence:  "TranslateWithMetadata asks for JSON with no schema and reads it with decodeAnswer.",
		catalogue: []string{"TranslateResult", "TranslateWithMetadata"}, functions: []string{"TranslateResult", "TranslateWithMetadata"},
	},
	{
		name: "expanddetail", level: wellFormed,
		evidence:  "ExpandWithMetadata asks for JSON with no schema and reads it with decodeAnswer.",
		catalogue: []string{"ExpandResult", "ExpandWithMetadata"}, functions: []string{"ExpandResult", "ExpandWithMetadata"},
	},
	{
		name: "streamsummarize", level: promptOnly, evidence: "streamLLM hands the model's text on as it arrives and reads none of it.",
		functions: []string{"StreamSummarize"},
	},
	{
		name: "streamrewrite", level: promptOnly, evidence: "streamLLM hands the model's text on as it arrives and reads none of it.",
		functions: []string{"StreamRewrite"},
	},
	{
		name: "streamtranslate", level: promptOnly, evidence: "streamLLM hands the model's text on as it arrives and reads none of it.",
		functions: []string{"StreamTranslate"},
	},
	{
		name: "streamexpand", level: promptOnly, evidence: "streamLLM hands the model's text on as it arrives and reads none of it.",
		functions: []string{"StreamExpand"},
	},
	{
		name: "compress", level: schemaConstrained, typeBound: true, without: wellFormed,
		evidence:  "Compress declares an envelope schema when the type has one, reads the envelope with decodeAnswer and the payload weakly; its relational checks run only when an option is set.",
		catalogue: []string{"Compress", "CompressText"}, functions: []string{"Compress", "CompressText"},
	},
	{
		name: "normalize", level: schemaConstrained, typeBound: true, without: wellFormed,
		evidence:  "Normalize declares an envelope schema when the type has one and reads the three members with readAnswerObject; the field rules run only when Fields or SkipFields is set.",
		catalogue: []string{"Normalize", "NormalizeText", "NormalizeBatch"}, functions: []string{"Normalize", "NormalizeText", "NormalizeBatch"},
	},
	{
		name: "format", level: promptOnly, evidence: "Format returns the model's text trimmed.",
		catalogue: []string{"Format"}, functions: []string{"Format"},
	},
	{
		name: "merge", level: wellFormed,
		evidence:  "Merge sends no schema and reads the merged value with decodeCallerAnswer; with one source it asks nothing.",
		catalogue: []string{"Merge"}, functions: []string{"Merge"},
	},

	// Analysis.
	{
		name: "classify", level: wellFormed, alternative: invariantChecked,
		evidence:  "Classify sends no schema, reads with decodeAnswer and checks the category is one offered (CategoryIn), the confidence scale and MinConfidence.",
		catalogue: []string{"Classify"}, functions: []string{"Classify"},
	},
	{
		name: "score", level: wellFormed,
		evidence:  "Score sends no schema and reads with decodeAnswer.",
		catalogue: []string{"Score"}, functions: []string{"Score"},
	},
	{
		name: "compare", level: wellFormed,
		evidence:  "Compare sends no schema and reads with decodeAnswer.",
		catalogue: []string{"Compare"}, functions: []string{"Compare"},
	},
	{
		name: "similar", level: schemaConstrained,
		evidence:  "Similar declares the similarity schema and reads it with decodeAnswer; the aspect rules run only when aspects are named.",
		catalogue: []string{"Similar", "CheckingSimilarity"}, functions: []string{"Similar"},
	},
	{
		name: "diff", level: promptOnly,
		evidence:  "The comparison is Go; the model's summary of it (generateDiffSummary) is text taken as it comes.",
		catalogue: []string{"Diff"}, functions: []string{"Diff"},
	},
	{
		name: "cluster", level: wellFormed, alternative: invariantChecked,
		evidence:  "Cluster sends no schema, reads with decodeAnswer and checks a partition (CoversExactlyOnce) and the size limits.",
		catalogue: []string{"Cluster"}, functions: []string{"Cluster"},
	},
	{
		name: "deduplicate", level: invariantChecked,
		evidence:  "Deduplicate declares its schema, reads with decodeAnswer and holds every index and duplicate_of inside the list and pointing to an earlier item.",
		catalogue: []string{"Deduplicate"}, functions: []string{"Deduplicate"},
	},

	// Collections.
	{
		name: "choose", level: wellFormed, alternative: invariantChecked,
		evidence:  "Choose sends no schema, reads an id with decodeAnswer and checks it is one offered (MemberOf).",
		catalogue: []string{"Choose", "ChooseBy"}, functions: []string{"Choose"},
	},
	{
		name: "filter", level: wellFormed, alternative: invariantChecked,
		evidence:  "Filter sends no schema, reads ids with decodeAnswer and checks each was offered and none repeats (SubsetOf over ids).",
		catalogue: []string{"Filter", "FilterBy"}, functions: []string{"Filter"},
	},
	{
		name: "sort", level: wellFormed, alternative: invariantChecked,
		evidence:  "Sort sends no schema, so by declaredContractLevel's rule it is well formed although it checks a permutation, and sortContract is that level.",
		catalogue: []string{"Sort", "SortBy", "SortResult"}, functions: []string{"Sort", "SortResult"},
	},
	{
		name: "rank", level: wellFormed, alternative: invariantChecked,
		evidence:  "Rank sends no schema, reads with decodeAnswer and checks every item is ranked exactly once (CoversExactlyOnce).",
		catalogue: []string{"Rank"}, functions: []string{"Rank"},
	},
	{
		name: "semanticmatch", level: wellFormed, alternative: invariantChecked,
		evidence:  "SemanticMatch sends no schema, reads with readAnswerObject and checks indices, pairs and the unmatched lists against both lists.",
		catalogue: []string{"SemanticMatch", "MatchOne"}, functions: []string{"SemanticMatch", "MatchOne"},
	},

	// Judgments.
	{
		name: "validate", level: schemaConstrained, typeBound: true, without: wellFormed,
		evidence:  "ValidateHybrid declares its schema (none when AutoCorrect meets an inexpressible type) and reads with decodeAnswer; the verdict is worked out in Go but nothing relational is checked; ValidateDeterministically sends nothing.",
		catalogue: []string{"Validate", "ValidateHybrid", "ValidateDeterministically"}, functions: []string{"Validate", "ValidateHybrid", "ValidateDeterministically"},
	},
	{
		name: "verify", level: invariantChecked,
		evidence:  "Verify declares the verification schema, reads with decodeAnswer and holds each verdict, issue kind and severity to its vocabulary and each check that is on to a report.",
		catalogue: []string{"VerifyWithModel", "Verify", "VerifyClaim"}, functions: []string{"VerifyWithModel", "Verify", "VerifyClaim"},
	},
	{
		name: "audit", level: invariantChecked,
		evidence:  "Audit declares the audit schema, reads with decodeAnswer and holds every finding to the categories asked about; auditResultContract is this level.",
		catalogue: []string{"AuditWithModel", "AuditWithMeta", "Audit"}, functions: []string{"AuditWithModel", "AuditWithMeta", "Audit"},
	},
	{
		name: "critique", level: wellFormed,
		evidence:  "Critique sends no schema and reads with decodeAnswer.",
		catalogue: []string{"CritiqueWithModel", "Critique"}, functions: []string{"CritiqueWithModel", "Critique"},
	},

	// The decisions, plain and Detail alike.
	{
		name: "is", level: invariantChecked,
		evidence:  "Every decision declares the assessment schema and readAssessAnswer holds each answer to its question; assessResultContract is this level.",
		catalogue: []string{"Is", "Whether"}, functions: []string{"Is", "Whether"},
	},
	{
		name: "isdetail", level: invariantChecked,
		evidence:  "IsResult runs the same schema and checks as Is, with the confidence member added.",
		catalogue: []string{"IsResult"}, functions: []string{"IsResult"},
	},
	{
		name: "which", level: invariantChecked,
		evidence:  "Which holds the chosen option to a number from 1 to the number of labels offered.",
		catalogue: []string{"Which", "RouteBy"}, functions: []string{"Which", "RouteBy"},
	},
	{
		name: "whichdetail", level: invariantChecked,
		evidence:  "WhichResult runs the same schema and checks as Which, with the confidence member added.",
		catalogue: []string{"WhichResult"}, functions: []string{"WhichResult"},
	},
	{
		name: "whichornone", level: invariantChecked,
		evidence:  "WhichOrNone reads the same answer as Which over the labels and a none.",
		catalogue: []string{"WhichOrNone"}, functions: []string{"WhichOrNone"},
	},
	{
		name: "whichornonedetail", level: invariantChecked,
		evidence:  "WhichOrNoneResult runs the same schema and checks as WhichOrNone, with the confidence member added.",
		catalogue: []string{"WhichOrNoneResult"}, functions: []string{"WhichOrNoneResult"},
	},
	{
		name: "rate", level: invariantChecked,
		evidence:  "Rate holds the rating to a whole number between the low and high the caller gave.",
		catalogue: []string{"Rate"}, functions: []string{"Rate"},
	},
	{
		name: "ratedetail", level: invariantChecked,
		evidence:  "RateResult runs the same schema and checks as Rate, with the confidence member added.",
		catalogue: []string{"RateResult"}, functions: []string{"RateResult"},
	},
	{
		name: "assess", level: invariantChecked,
		evidence:  "Assess answers a type's questions through the same schema and the same per-question checks.",
		catalogue: []string{"Assess"}, functions: []string{"Assess"},
	},
	{
		name: "assessdetail", level: invariantChecked,
		evidence:  "AssessResult runs the same schema and checks as Assess, with the confidence member added.",
		catalogue: []string{"AssessResult"}, functions: []string{"AssessResult"},
	},

	// The experimental operations.
	{
		name: "exp.synthesize", level: wellFormed,
		evidence:  "Synthesize sends no schema and reads with decodeAnswer and decodeCallerAnswer.",
		catalogue: []string{"Synthesize"}, functions: []string{"Synthesize"},
	},
	{
		name: "exp.predict", level: schemaConstrained, typeBound: true, without: wellFormed,
		evidence:  "Predict declares a schema when the type has one, reads the envelope with decodeAnswer and the prediction weakly; its option rules run only when set.",
		catalogue: []string{"Predict"}, functions: []string{"Predict"},
	},
	{
		name: "exp.assemble", level: wellFormed,
		evidence:  "Assemble sends no schema and reads the envelope with decodeAnswer and the composed value with decodeCallerAnswer.",
		catalogue: []string{"Assemble"}, functions: []string{"Assemble"},
	},
	{
		name: "exp.derive", level: wellFormed,
		evidence:  "Derive sends no schema and reads with decodeAnswer and decodeCallerAnswer.",
		catalogue: []string{"Derive"}, functions: []string{"Derive"},
	},
	{
		name: "exp.interpolate", level: wellFormed,
		evidence:  "Interpolate sends no schema and reads with readAnswerObject.",
		catalogue: []string{"Interpolate"}, functions: []string{"Interpolate"},
	},
	{
		name: "exp.arbitrate", level: invariantChecked,
		evidence:  "Arbitrate declares a schema keyed by option and rule, reads with decodeAnswer and requires every option and every rule once; the winner is worked out in Go.",
		catalogue: []string{"Arbitrate"}, functions: []string{"Arbitrate"},
	},
	{
		name: "exp.resolve", level: wellFormed,
		evidence:  "Resolve sends no schema and reads with decodeAnswer and decodeCallerAnswer.",
		catalogue: []string{"Resolve"}, functions: []string{"Resolve"},
	},
	{
		name: "exp.settle", level: wellFormed, aliases: []string{"negotiate"},
		evidence:  "Settle sends no schema and reads with readAnswerObject.",
		catalogue: []string{"Settle", "Negotiate"}, functions: []string{"Settle", "Negotiate"},
	},
	{
		name: "exp.settleadversarial", level: wellFormed,
		evidence:  "SettleAdversarial sends no schema and reads with readAnswerObject.",
		catalogue: []string{"SettleAdversarial", "NegotiateAdversarial"}, functions: []string{"SettleAdversarial", "NegotiateAdversarial"},
	},
	{
		name: "exp.conform", level: wellFormed,
		evidence:  "Conform sends no schema and reads with decodeAnswer and decodeCallerAnswer.",
		catalogue: []string{"Conform"}, functions: []string{"Conform"},
	},
	{
		name: "exp.project", level: wellFormed,
		evidence:  "Project sends no schema and reads with decodeAnswer and decodeCallerAnswer.",
		catalogue: []string{"Project"}, functions: []string{"Project"},
	},
	{
		name: "exp.pivot", level: wellFormed,
		evidence:  "Pivot sends no schema and reads with decodeAnswer and decodeCallerAnswer.",
		catalogue: []string{"Pivot"}, functions: []string{"Pivot"},
	},
	{
		name: "exp.enrich", level: wellFormed,
		evidence:  "Enrich's response shape holds maps, which strict mode cannot express, so no schema is ever sent; it reads with decodeAnswer and decodeCallerAnswer.",
		catalogue: []string{"Enrich"}, functions: []string{"Enrich"},
	},
	{
		name: "exp.enrichinplace", level: wellFormed,
		evidence:  "EnrichInPlace sends no schema and reads with decodeCallerAnswer.",
		catalogue: []string{"EnrichInPlace"}, functions: []string{"EnrichInPlace"},
	},
	{
		name: "exp.decompose", level: invariantChecked, typeBound: true, without: wellFormed,
		evidence:  "Decompose declares its schema when the type has one, reads with decodeAnswer and checks ids, references, depths and cycles of every decomposition.",
		catalogue: []string{"Decompose"}, functions: []string{"Decompose"},
	},
	{
		name: "exp.decomposetoslice", level: wellFormed,
		evidence:  "DecomposeToSlice sends no schema and reads with decodeCallerAnswer.",
		catalogue: []string{"DecomposeToSlice"}, functions: []string{"DecomposeToSlice"},
	},

	// Redaction and control flow.
	{
		name: "redact.text", level: invariantChecked, aliases: []string{"redact_llm"},
		evidence:  "RedactLLM declares the spans schema, reads with decodeAnswer and finds each span in the text; redactLLMResultContract is this level.",
		catalogue: []string{"RedactLLM", "RedactLLMWithMeta"}, functions: []string{"RedactLLM", "RedactLLMWithMeta"},
	},
	{
		name: "flow.decide", level: invariantChecked,
		evidence:  "Decide declares its schema, reads with decodeAnswer and holds the selection and the alternatives to positions inside the list.",
		catalogue: []string{"Decide"}, functions: []string{"Decide"},
	},
	{
		name: "flow.match", level: promptOnly,
		evidence:  "matchesStringCondition reads the text true or false and refuses anything else, which is no JSON and no schema.",
		catalogue: []string{"Match"}, functions: []string{"Match"},
	},

	// The runners of someone else's operation. Each delivers the level of the
	// operation it runs, so the row holds the lowest level any of them can have.
	{
		name: "op.run", level: wellFormed, computed: true,
		via:       "declaredContractLevel(contract) before and after, then negotiatedContractLevel (RunOpResult)",
		evidence:  "RunOp sets no schema of its own and the level is read off the descriptor's SchemaName, Invariants and evidence policy, so a descriptor that names a schema it never sends is still delivered as schema constrained.",
		catalogue: nil, functions: []string{"RunOp", "RunOpResult"},
	},
	{
		name: "op.runbatch", level: promptOnly, computed: true,
		via:       "batchContractLevel(op) before, then negotiatedContractLevel after (RunOpBatchResult)",
		evidence:  "A batch with a merge written by hand is vouched for no further than that the provider answered; one built on the id protocol checks every id and each item.",
		catalogue: nil, functions: []string{"RunOpBatch", "RunOpBatchResult"},
	},
	{
		name: "op.runmany", level: promptOnly, computed: true,
		via:       "the RunOpResult or RunOpBatchResult it runs for each item or chunk; RunOpMany computes none itself",
		evidence:  "RunOpMany carries the Meta of the runner that produced each result, so one call can hold items at both levels.",
		catalogue: nil, functions: []string{"RunOpMany"},
	},
	{
		name: "op.runmanypartial", level: wellFormed, computed: true,
		via:       "RunOpResult for each item (runCollect, runFailFast, runRetryPass); the result type carries no level",
		evidence:  "RunOpManyPartial runs RunOpResult per item, so each item has that runner's level and the floor and policy are held per item.",
		catalogue: nil, functions: []string{"RunOpManyPartial"},
	},
	{
		name: "op.runmanyrecover", level: promptOnly, computed: true,
		via:       "batchContractLevel(op) for the floor; the atomic fallback runs RunOpResult; the result type carries no level",
		evidence:  "RunOpManyRecover reads each pass with the id protocol, works out the level the route that answered delivered (negotiatedContractLevel) and holds it to the client's floor after every pass, and falls back to RunOpResult.",
		catalogue: nil, functions: []string{"RunOpManyRecover"},
	},
	{
		name: "op.runstagedplan", level: wellFormed, computed: true,
		via:       "RunOpResult for each stage that runs; a stage a deterministic check skips delivers the level that check attests",
		evidence:  "RunStagedPlan runs RunOpResult for each stage and skips a stage only when its check meets the stage's required contract, which is at least well formed.",
		catalogue: nil, functions: []string{"RunStagedPlan"},
	},
	{
		name: "extractbatch", level: wellFormed, computed: true,
		via:       "the processor's mode: parallel mode runs Extract for each input, merged mode reads one answer by index",
		evidence:  "ExtractBatch in merged mode sends no schema and reads indices with decodeAnswer and each item with decodeCallerAnswer; in parallel mode each input is an Extract.",
		catalogue: nil, functions: []string{"ExtractBatch"},
	},

	// The deprecated forms whose request is not the one that replaced them.
	{
		name: "legacy.questionlegacy", level: promptOnly,
		evidence:  "QuestionLegacy returns the model's text trimmed, where Question declares a schema.",
		catalogue: []string{"QuestionLegacy"}, functions: []string{"QuestionLegacy"},
	},
	{
		name: "legacy.validatelegacy", level: wellFormed,
		evidence:  "ValidateLegacy sends no schema and reads with decodeAnswer, where ValidateHybrid declares one.",
		catalogue: []string{"ValidateLegacy"}, functions: []string{"ValidateLegacy"},
	},
	{
		name: "legacy.formatwithmetadata", level: wellFormed,
		evidence:  "FormatWithMetadata asks for JSON with no schema and reads with decodeAnswer, where Format takes the text.",
		catalogue: []string{"FormatWithMetadata"}, functions: []string{"FormatWithMetadata"},
	},
	{
		name: "legacy.mergewithmetadata", level: wellFormed,
		evidence:  "MergeWithMetadata sends no schema and reads the envelope or the merged value as JSON.",
		catalogue: []string{"MergeWithMetadata"}, functions: []string{"MergeWithMetadata"},
	},
}

// noModelOperations are the catalogue entries that ask no model, and so have no
// level. Redact and Chunk are Go, Guard and the review gate and Vote run
// functions the caller wrote. The accessor reports them as not known, which is
// not a gap: nothing is sent, so there is nothing a floor could refuse.
var noModelOperations = map[string]string{
	"Redact":  "Redact finds and masks values with Go detectors and sends nothing",
	"Chunk":   "Chunk splits a slice in Go",
	"Guard":   "Guard runs the caller's checks",
	"Approve": "Approve runs the caller's step and gate",
	"Vote":    "Vote runs the caller's step and reconciler",
}

// differsFromTableA2 says, for each row that is not what Table A2 of the
// consolidation had, what A2 had and why this row is not that.
var differsFromTableA2 = map[string]string{
	"sort":                      "A2: schema and invariants checked, computed. No schema is declared, so negotiatedContractLevel can never lower it and declaredContractLevel's rule gives well formed.",
	"exp.enrich":                "A2: schema constrained. The shape holds maps, jsonSchemaFor returns nil for a map, and opt.JSONSchema is never set.",
	"exp.arbitrate":             "A2: schema constrained. Every option and every rule must be answered exactly once, checked on every request.",
	"deduplicate":               "A2: schema constrained. Every index and duplicate_of is checked to lie in the list and to point to an earlier item, on every request.",
	"exp.decompose":             "A2: schema constrained. Ids, references, depths and cycles are checked on every request that sends the schema.",
	"flow.decide":               "A2: schema constrained. The selection and every alternative are held to positions inside the list on every request.",
	"verify":                    "A2: schema constrained. Every verdict, issue kind and severity is held to its vocabulary, and each check that is on to a report, on every request; this is the weakest kind of invariant in the table.",
	"generate":                  "A2: static. The level depends on the type argument, so it is computed per type (ContractLevelFor), and the table's Level is the lowest.",
	"op.runmanypartial":         "A2 had no row: the operation was restored after it was written.",
	"op.runmanyrecover":         "A2 had no row: the operation was restored after it was written.",
	"op.runstagedplan":          "A2 had no row: the operation was restored after it was written.",
	"extractbatch":              "A2 had no row for ExtractBatch.",
	"legacy.questionlegacy":     "A2 had no row for the deprecated forms whose request is not their replacement's.",
	"legacy.validatelegacy":     "A2 had no row for the deprecated forms whose request is not their replacement's.",
	"legacy.formatwithmetadata": "A2 had no row for the deprecated forms whose request is not their replacement's.",
	"legacy.mergewithmetadata":  "A2 had no row for the deprecated forms whose request is not their replacement's.",
}

var (
	// contractRules is every rule by the name its record carries and by the
	// names the call path knows the operation as.
	contractRules map[string]ContractRule
	// contractRowByName is the row of each of those names.
	contractRowByName map[string]*contractRow
)

func init() {
	contractRules = make(map[string]ContractRule, len(contractRows)+8)
	contractRowByName = make(map[string]*contractRow, len(contractRows)+8)
	for i := range contractRows {
		row := &contractRows[i]
		rule := ContractRule{Level: row.level, Computed: row.computed}
		for _, name := range append([]string{row.name}, row.aliases...) {
			contractRules[name] = rule
			contractRowByName[name] = row
		}
	}
}

// ContractLevelOf returns the rule for an operation, by the name its record
// carries or the name the call path puts on its context. It allocates nothing.
// An operation that asks no model has none, and neither does a name that is not
// an operation.
func ContractLevelOf(operation string) (ContractRule, bool) {
	rule, known := contractRules[operation]
	return rule, known
}

// contractRowOf is the row an operation's rule was made from.
func contractRowOf(operation string) (*contractRow, bool) {
	row, known := contractRowByName[operation]
	return row, known
}

// ContractLevels returns every rule by its record name, for the documentation
// test. The map is a copy.
func ContractLevels() map[string]ContractRule {
	levels := make(map[string]ContractRule, len(contractRows))
	for i := range contractRows {
		levels[contractRows[i].name] = ContractRule{Level: contractRows[i].level, Computed: contractRows[i].computed}
	}
	return levels
}

// contractNames returns the record names of the table in order.
func contractNames() []string {
	names := make([]string, 0, len(contractRows))
	for i := range contractRows {
		names = append(names, contractRows[i].name)
	}
	sort.Strings(names)
	return names
}

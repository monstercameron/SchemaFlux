package ops

import (
	"context"
	"sort"
)

// operationCall is one operation of the catalogue, called with the smallest
// arguments it accepts, under the context it is given. row is the name its
// record carries (the contract table's key). The calls carry no client of their
// own: a test puts the client on the default snapshot (withDefaultExec) or on
// the context an operation takes, and every one of these reads it from there.
//
// The answer of a call is whatever the provider of the test says, and a call
// whose answer does not suit it fails: the table is for what happens on the way
// to the provider and back, not for what an operation does with a good answer.
type operationCall struct {
	row string
	run func(ctx context.Context) error
}

// opCallQuestions is the type the assessments are made into.
type opCallQuestions struct {
	Adult bool `ask:"Is the person an adult?"`
}

func operationCalls() []operationCall {
	person := Person{Name: "Ada", Age: 36}
	people := []Person{{Name: "Ada", Age: 36}, {Name: "Grace", Age: 45}}
	words := []string{"alpha", "beta", "gamma"}
	return []operationCall{
		{"extract", func(context.Context) error { _, err := Extract[Person]("Ada is 36", NewExtractOptions()); return err }},
		{"transform", func(context.Context) error {
			_, err := Transform[Person, Person](person, NewTransformOptions())
			return err
		}},
		{"generate", func(context.Context) error { _, err := Generate[Person]("a person", NewGenerateOptions()); return err }},
		{"infer", func(context.Context) error { _, err := Infer(person, NewInferOptions()); return err }},
		{"parse", func(context.Context) error {
			_, err := Parse[Person]("Ada, 36", NewParseOptions().WithAllowLLMFallback(true))
			return err
		}},
		{"complete", func(ctx context.Context) error {
			_, err := Complete(ctx, "Once upon a", NewCompleteOptions())
			return err
		}},
		{"completefield", func(ctx context.Context) error {
			_, err := CompleteField(ctx, person, NewCompleteFieldOptions("Name"))
			return err
		}},
		{"suggest", func(context.Context) error { _, err := Suggest[Person]("Ad", NewSuggestOptions()); return err }},
		{"annotate", func(context.Context) error { _, err := Annotate(person, NewAnnotateOptions()); return err }},
		{"explain", func(context.Context) error { _, err := Explain(person, NewExplainOptions()); return err }},
		{"question", func(context.Context) error {
			_, err := Question[Person, string](person, NewQuestionOptions("How old is she?"))
			return err
		}},
		{"summarize", func(context.Context) error { _, err := Summarize("a long text", NewSummarizeOptions()); return err }},
		{"summarizedetail", func(context.Context) error {
			_, err := SummarizeResult("a long text", NewSummarizeOptions())
			return err
		}},
		{"streamsummarize", func(context.Context) error {
			stream, err := StreamSummarize("a long text", NewSummarizeOptions())
			return readStream(stream, err)
		}},
		{"rewrite", func(context.Context) error { _, err := Rewrite("a text", NewRewriteOptions()); return err }},
		{"rewritedetail", func(context.Context) error { _, err := RewriteResult("a text", NewRewriteOptions()); return err }},
		{"streamrewrite", func(context.Context) error {
			stream, err := StreamRewrite("a text", NewRewriteOptions())
			return readStream(stream, err)
		}},
		{"translate", func(context.Context) error {
			_, err := Translate("a text", NewTranslateOptions().WithTargetLanguage("French"))
			return err
		}},
		{"translatedetail", func(context.Context) error {
			_, err := TranslateResult("a text", NewTranslateOptions().WithTargetLanguage("French"))
			return err
		}},
		{"streamtranslate", func(context.Context) error {
			stream, err := StreamTranslate("a text", NewTranslateOptions().WithTargetLanguage("French"))
			return readStream(stream, err)
		}},
		{"expand", func(context.Context) error { _, err := Expand("a text", NewExpandOptions()); return err }},
		{"expanddetail", func(context.Context) error { _, err := ExpandResult("a text", NewExpandOptions()); return err }},
		{"streamexpand", func(context.Context) error {
			stream, err := StreamExpand("a text", NewExpandOptions())
			return readStream(stream, err)
		}},
		{"compress", func(context.Context) error { _, err := Compress(person, NewCompressOptions()); return err }},
		{"normalize", func(context.Context) error { _, err := Normalize(person, NewNormalizeOptions()); return err }},
		{"format", func(context.Context) error { _, err := Format(person, "{{.Name}} is {{.Age}}"); return err }},
		{"merge", func(context.Context) error { _, err := Merge(people, "keep the newest"); return err }},
		{"classify", func(context.Context) error {
			_, err := Classify[Person, string](person, NewClassifyOptions().WithCategories([]string{"young", "old"}))
			return err
		}},
		{"score", func(context.Context) error {
			_, err := Score(person, NewScoreOptions().WithSteering("the quality of the record"))
			return err
		}},
		{"compare", func(context.Context) error { _, err := Compare(people[0], people[1], NewCompareOptions()); return err }},
		{"similar", func(context.Context) error { _, err := Similar(people[0], people[1], NewSimilarOptions()); return err }},
		{"diff", func(context.Context) error { _, err := Diff(people[0], people[1], NewDiffOptions()); return err }},
		{"cluster", func(context.Context) error { _, err := Cluster(words, NewClusterOptions()); return err }},
		{"deduplicate", func(context.Context) error { _, err := Deduplicate(words, 0.8); return err }},
		{"choose", func(context.Context) error {
			_, err := Choose(words, NewChooseOptions().WithCriteria([]string{"the first letter of the alphabet"}))
			return err
		}},
		{"filter", func(context.Context) error {
			_, err := Filter(words, NewFilterOptions().WithCriteria("the word is short"))
			return err
		}},
		{"sort", func(context.Context) error {
			_, err := Sort(words, NewSortOptions().WithCriteria("alphabetically"))
			return err
		}},
		{"rank", func(context.Context) error {
			_, err := Rank(words, NewRankOptions().WithQuery("shortest first"))
			return err
		}},
		{"semanticmatch", func(context.Context) error {
			_, err := SemanticMatch(words, []string{"a", "b"}, NewMatchOptions())
			return err
		}},
		{"validate", func(context.Context) error {
			_, err := ValidateHybrid(person, NewValidateOptions().WithRules("the name is a person's name"))
			return err
		}},
		{"verify", func(context.Context) error {
			_, err := VerifyWithModel(person, NewVerifyOptions())
			return err
		}},
		{"audit", func(context.Context) error { _, err := AuditWithModel(person); return err }},
		{"critique", func(context.Context) error {
			_, err := CritiqueWithModel(person, NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			return err
		}},
		{"is", func(ctx context.Context) error { _, err := Is(ctx, person, "Is she an adult?"); return err }},
		{"isdetail", func(ctx context.Context) error { _, err := IsResult(ctx, person, "Is she an adult?"); return err }},
		{"which", func(ctx context.Context) error {
			_, err := Which(ctx, person, "Which age group?", []string{"young", "old"})
			return err
		}},
		{"whichdetail", func(ctx context.Context) error {
			_, err := WhichResult(ctx, person, "Which age group?", []string{"young", "old"})
			return err
		}},
		{"whichornone", func(ctx context.Context) error {
			_, _, err := WhichOrNone(ctx, person, "Which age group?", []string{"young", "old"})
			return err
		}},
		{"whichornonedetail", func(ctx context.Context) error {
			_, err := WhichOrNoneResult(ctx, person, "Which age group?", []string{"young", "old"})
			return err
		}},
		{"rate", func(ctx context.Context) error { _, err := Rate(ctx, person, "How senior?", 1, 5); return err }},
		{"ratedetail", func(ctx context.Context) error { _, err := RateResult(ctx, person, "How senior?", 1, 5); return err }},
		{"assess", func(ctx context.Context) error { _, err := Assess[opCallQuestions](ctx, person); return err }},
		{"assessdetail", func(ctx context.Context) error { _, err := AssessResult[opCallQuestions](ctx, person); return err }},
		{"exp.synthesize", func(context.Context) error {
			_, err := Synthesize[Person]([]any{"Ada is 36", "Ada is an engineer"}, NewSynthesizeOptions())
			return err
		}},
		{"exp.predict", func(context.Context) error { _, err := Predict[Person](people, NewPredictOptions()); return err }},
		{"exp.assemble", func(context.Context) error { _, err := Assemble[Person]([]any{"Ada", 36}); return err }},
		{"exp.derive", func(context.Context) error { _, err := Derive[Person, Person](person); return err }},
		{"exp.interpolate", func(context.Context) error { _, err := Interpolate(people); return err }},
		{"exp.arbitrate", func(context.Context) error {
			options := NewArbitrateOptions()
			options.Rules = []string{"the shortest wins"}
			_, err := Arbitrate(words, options)
			return err
		}},
		{"exp.resolve", func(context.Context) error { _, err := Resolve(people); return err }},
		{"exp.settle", func(context.Context) error { _, err := Settle[Person]("split the bill"); return err }},
		{"exp.settleadversarial", func(context.Context) error {
			_, err := SettleAdversarial(AdversarialContext[Person]{})
			return err
		}},
		{"exp.conform", func(context.Context) error { _, err := Conform(person, "a title-case name"); return err }},
		{"exp.project", func(context.Context) error { _, err := Project[Person, Person](person); return err }},
		{"exp.pivot", func(context.Context) error { _, err := Pivot[Person, Person](person); return err }},
		{"exp.enrich", func(context.Context) error { _, err := Enrich[Person, Person](person, NewEnrichOptions()); return err }},
		{"exp.enrichinplace", func(context.Context) error { _, err := EnrichInPlace(person, NewEnrichOptions()); return err }},
		{"exp.decompose", func(context.Context) error {
			_, err := Decompose(person, NewDecomposeOptions())
			return err
		}},
		{"exp.decomposetoslice", func(context.Context) error {
			_, err := DecomposeToSlice[Person, Person](person, NewDecomposeOptions())
			return err
		}},
		{"redact.text", func(ctx context.Context) error {
			_, err := RedactLLM(ctx, "Call Ada on 555-0100", NewRedactLLMOptions())
			return err
		}},
		{"flow.decide", func(ctx context.Context) error {
			_, _, err := Decide(ctx, "a situation", []Decision[string]{{Value: "a", Description: "first"}, {Value: "b", Description: "second"}})
			return err
		}},
	}
}

// readStream reads a stream to its end and returns the error it ended with, or
// the one it was never opened with.
func readStream(stream *TextStream, openErr error) error {
	if openErr != nil {
		return openErr
	}
	_, err := stream.Text()
	return err
}

// operationRows is the rows of operationCalls, sorted.
func operationRows() []string {
	var rows []string
	for _, call := range operationCalls() {
		rows = append(rows, call.row)
	}
	sort.Strings(rows)
	return rows
}

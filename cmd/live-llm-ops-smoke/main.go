package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

type smokeCase struct {
	name string
	run  func() error
}

type person struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Location string `json:"location"`
}

type emailDraft struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type contentPlan struct {
	Title    string   `json:"title"`
	Audience string   `json:"audience"`
	Goals    []string `json:"goals"`
}

type incident struct {
	Service  string `json:"service"`
	Severity string `json:"severity"`
	Issue    string `json:"issue"`
}

type draft struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type companyInfo struct {
	Name     string `json:"name"`
	Industry string `json:"industry"`
	Summary  string `json:"summary"`
}

type taskItem struct {
	Title    string `json:"title"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type suggestionItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type noteSummary struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Actions []string `json:"actions"`
}

type companySeed struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

type metricPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

type synthesisNote struct {
	OverallInsight           string   `json:"overall_insight"`
	KeyPoints                []string `json:"key_points"`
	StrategicRecommendations []string `json:"strategic_recommendations"`
}
type derivedPerson struct {
	Name       string `json:"name"`
	BirthYear  int    `json:"birth_year"`
	City       string `json:"city"`
	Age        int    `json:"age"`
	Generation string `json:"generation"`
}

func main() {
	key := strings.TrimSpace(os.Getenv("SCHEMAFLUX_API_KEY"))
	if key == "" {
		fmt.Fprintln(os.Stderr, "SCHEMAFLUX_API_KEY is required")
		os.Exit(2)
	}
	os.Setenv("SCHEMAFLUX_PROVIDER", "openai")
	if os.Getenv("SCHEMAFLUX_TIMEOUT") == "" {
		os.Setenv("SCHEMAFLUX_TIMEOUT", "60s")
	}
	schemaflux.Init(key)
	tests := []smokeCase{
		{"Extract", testExtract}, {"Transform", testTransform}, {"Generate", testGenerate}, {"Choose", testChoose}, {"Filter", testFilter}, {"Sort", testSort}, {"Classify", testClassify}, {"Score", testScore}, {"Compare", testCompare}, {"Similar", testSimilar}, {"Infer", testInfer}, {"Diff", testDiff}, {"Explain", testExplain}, {"Parse", testParse}, {"Summarize", testSummarize}, {"SummarizeWithMetadata", testSummarizeWithMetadata}, {"Rewrite", testRewrite}, {"RewriteWithMetadata", testRewriteWithMetadata}, {"Translate", testTranslate}, {"TranslateWithMetadata", testTranslateWithMetadata}, {"Expand", testExpand}, {"ExpandWithMetadata", testExpandWithMetadata}, {"Suggest", testSuggest}, {"RedactLLM", testRedactLLM}, {"Complete", testComplete}, {"CompleteField", testCompleteField}, {"Validate", testValidate}, {"ValidateLegacy", testValidateLegacy}, {"Question", testQuestion}, {"QuestionLegacy", testQuestionLegacy}, {"Merge", testMerge}, {"MergeWithMetadata", testMergeWithMetadata}, {"Format", testFormat}, {"FormatWithMetadata", testFormatWithMetadata}, {"Decide", testDecide}, {"Annotate", testAnnotate}, {"Cluster", testCluster}, {"Rank", testRank}, {"Compress", testCompress}, {"CompressText", testCompressText}, {"Decompose", testDecompose}, {"DecomposeToSlice", testDecomposeToSlice}, {"Enrich", testEnrich}, {"EnrichInPlace", testEnrichInPlace}, {"Normalize", testNormalize}, {"NormalizeText", testNormalizeText}, {"NormalizeBatch", testNormalizeBatch}, {"SemanticMatch", testSemanticMatch}, {"MatchOne", testMatchOne}, {"Critique", testCritique}, {"Synthesize", testSynthesize}, {"Predict", testPredict}, {"Verify", testVerify}, {"VerifyClaim", testVerifyClaim}, {"Negotiate", testNegotiate}, {"NegotiateAdversarial", testNegotiateAdversarial}, {"Resolve", testResolve}, {"Derive", testDerive}, {"Conform", testConform}, {"Interpolate", testInterpolate}, {"Arbitrate", testArbitrate}, {"Project", testProject}, {"Audit", testAudit}, {"Assemble", testAssemble}, {"Pivot", testPivot},
	}
	var failed []string
	startAll := time.Now()
	for i, tc := range tests {
		start := time.Now()
		err := tc.run()
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", tc.name, err))
			fmt.Printf("[%02d/%02d] FAIL %-24s %s\n", i+1, len(tests), tc.name, err)
			continue
		}
		fmt.Printf("[%02d/%02d] PASS %-24s %s\n", i+1, len(tests), tc.name, time.Since(start).Round(time.Millisecond))
	}
	fmt.Printf("\nCompleted %d LLM op smoke tests in %s\n", len(tests), time.Since(startAll).Round(time.Millisecond))
	if len(failed) == 0 {
		fmt.Println("All LLM ops passed.")
		return
	}
	fmt.Printf("%d failures:\n", len(failed))
	for _, f := range failed {
		fmt.Printf("- %s\n", f)
	}
	os.Exit(1)
}

func req(ok bool, f string, a ...any) error {
	if ok {
		return nil
	}
	return fmt.Errorf(f, a...)
}
func oneOf(v string, xs ...string) bool {
	for _, x := range xs {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}
func s(v any) string {
	if x, ok := v.(string); ok {
		return strings.TrimSpace(x)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func testExtract() error {
	out, err := schemaflux.Extract[person]("Jordan Lee is a product manager in Austin.", schemaflux.NewExtractOptions())
	if err != nil {
		return err
	}
	return req(out.Name != "", "missing extracted name")
}
func testTransform() error {
	in := map[string]any{"full_name": "Maya Patel", "company": "Northwind", "need": "better support analytics"}
	out, err := schemaflux.Transform[map[string]any, emailDraft](in, schemaflux.NewTransformOptions())
	if err != nil {
		return err
	}
	return req(out.Subject != "" && out.Body != "", "transform output empty")
}
func testGenerate() error {
	out, err := schemaflux.Generate[contentPlan]("Create a short launch plan for a B2B analytics webinar.", schemaflux.NewGenerateOptions())
	if err != nil {
		return err
	}
	return req(out.Title != "", "missing generated title")
}
func testChoose() error {
	opts := schemaflux.NewChooseOptions()
	opts.Criteria = []string{"best low-cost breakfast option"}
	xs := []map[string]any{{"name": "Pancakes", "price": 14}, {"name": "Oatmeal", "price": 6}, {"name": "Steak", "price": 24}}
	out, err := schemaflux.Choose(xs, opts)
	if err != nil {
		return err
	}
	return req(len(out) > 0, "choose returned empty option")
}
func testFilter() error {
	opts := schemaflux.NewFilterOptions()
	opts.Criteria = "only high priority open work"
	opts.CommonOptions.Steering = "Return only a JSON array of the complete task objects that should remain."
	xs := []taskItem{{Title: "Fix login bug", Priority: "high", Status: "open"}, {Title: "Refactor CSS", Priority: "low", Status: "open"}, {Title: "Close Q1 plan", Priority: "high", Status: "done"}}
	out, err := schemaflux.Filter(xs, opts)
	if err != nil {
		return err
	}
	return req(len(out) > 0 && len(out) < len(xs), "filter returned invalid result")
}
func testSort() error {
	opts := schemaflux.NewSortOptions()
	opts.Criteria = "by priority order: critical first, then medium, then low"
	opts.CommonOptions.Steering = "Return only a JSON array containing every task exactly once in sorted order."
	xs := []taskItem{{Title: "Polish demo", Priority: "medium", Status: "open"}, {Title: "Fix production outage", Priority: "critical", Status: "open"}, {Title: "Update docs", Priority: "low", Status: "open"}}
	out, err := schemaflux.Sort(xs, opts)
	if err != nil {
		return err
	}
	return req(len(out) == len(xs), "sort lost items")
}
func testClassify() error {
	opts := schemaflux.NewClassifyOptions()
	opts.Categories = []string{"positive", "negative", "neutral"}
	out, err := schemaflux.Classify[string, string]("The onboarding flow was smooth and fast.", opts)
	if err != nil {
		return err
	}
	return req(oneOf(out.Category, "positive", "negative", "neutral"), "invalid category: %q", out.Category)
}
func testScore() error {
	opts := schemaflux.NewScoreOptions()
	opts.Criteria = []string{"clarity", "usefulness"}
	out, err := schemaflux.Score("A concise setup guide with examples and troubleshooting.", opts)
	if err != nil {
		return err
	}
	return req(out.Value > 0, "score was not positive")
}
func testCompare() error {
	opts := schemaflux.NewCompareOptions()
	opts.ComparisonAspects = []string{"price", "flexibility", "support"}
	out, err := schemaflux.Compare(map[string]any{"name": "Starter", "price": 20}, map[string]any{"name": "Pro", "price": 60}, opts)
	if err != nil {
		return err
	}
	return req(out.SimilarityScore >= 0 && out.Verdict != "", "compare output invalid")
}
func testSimilar() error {
	out, err := schemaflux.Similar("The app crashed during checkout.", "Checkout failed because the app crashed.", schemaflux.NewSimilarOptions())
	if err != nil {
		return err
	}
	return req(out.Score > 0.5, "similarity score too low: %.2f", out.Score)
}
func testInfer() error {
	out, err := schemaflux.Infer(person{Name: "Aisha Khan", Role: "Designer"}, schemaflux.NewInferOptions())
	if err != nil {
		return err
	}
	return req(out.Name != "", "infer returned empty struct")
}
func testDiff() error {
	type profileDiff struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	out, err := schemaflux.Diff(profileDiff{Name: "Sam", Email: "sam@old.com", Role: "AE"}, profileDiff{Name: "Sam", Email: "sam@new.com", Role: "Senior AE"}, schemaflux.NewDiffOptions())
	if err != nil {
		return err
	}
	return req(len(out.Modified) > 0 || len(out.Added) > 0 || len(out.Removed) > 0, "diff found no changes")
}
func testExplain() error {
	opts := schemaflux.NewExplainOptions()
	opts.Audience = "non-technical"
	out, err := schemaflux.Explain(map[string]any{"cache": "stores recent responses", "ttl": "5 minutes"}, opts)
	if err != nil {
		return err
	}
	return req(out.Explanation != "" && out.Summary != "", "explain output empty")
}
func testParse() error {
	opts := schemaflux.NewParseOptions()
	opts.AllowLLMFallback = true
	opts.FormatHints = []string{"service | severity | issue"}
	out, err := schemaflux.Parse[incident]("billing | critical | duplicate charges on renewal", opts)
	if err != nil {
		return err
	}
	return req(out.Data.Service != "", "parse returned empty data")
}
func testSummarize() error {
	out, err := schemaflux.Summarize("SchemaFlux uses typed operations to turn natural language tasks into structured outputs for Go programs.", schemaflux.NewSummarizeOptions())
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out) != "", "summary empty")
}
func testSummarizeWithMetadata() error {
	out, err := schemaflux.SummarizeWithMetadata("The project shipped three features, reduced support load by 15 percent, and cut response latency after batching inference requests.", schemaflux.NewSummarizeOptions())
	if err != nil {
		return err
	}
	return req(out.Text != "" && out.Confidence > 0, "summary metadata invalid")
}
func testRewrite() error {
	opts := schemaflux.NewRewriteOptions()
	opts.TargetTone = "professional"
	out, err := schemaflux.Rewrite("hey team, this thing is kinda broken and needs fixing asap", opts)
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out) != "", "rewrite empty")
}
func testRewriteWithMetadata() error {
	opts := schemaflux.NewRewriteOptions()
	opts.TargetTone = "friendly"
	out, err := schemaflux.RewriteWithMetadata("Send the customer a direct but polite apology.", opts)
	if err != nil {
		return err
	}
	return req(out.Text != "" && out.Confidence > 0, "rewrite metadata invalid")
}
func testTranslate() error {
	opts := schemaflux.NewTranslateOptions()
	opts.TargetLanguage = "Spanish"
	out, err := schemaflux.Translate("Your order will arrive tomorrow.", opts)
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out) != "", "translate empty")
}
func testTranslateWithMetadata() error {
	opts := schemaflux.NewTranslateOptions()
	opts.TargetLanguage = "French"
	out, err := schemaflux.TranslateWithMetadata("Please restart the server after deployment.", opts)
	if err != nil {
		return err
	}
	return req(out.Text != "" && out.Confidence > 0, "translate metadata invalid")
}
func testExpand() error {
	in := "We launched a new billing dashboard."
	out, err := schemaflux.Expand(in, schemaflux.NewExpandOptions())
	if err != nil {
		return err
	}
	return req(len(out) > len(in), "expand did not grow text")
}
func testExpandWithMetadata() error {
	out, err := schemaflux.ExpandWithMetadata("The experiment improved retention.", schemaflux.NewExpandOptions())
	if err != nil {
		return err
	}
	return req(out.Text != "" && out.Confidence > 0, "expand metadata invalid")
}
func testSuggest() error {
	opts := schemaflux.NewSuggestOptions()
	opts.TopN = 3
	opts.CommonOptions.Steering = "Return a JSON array of suggestion objects with name and description fields."
	out, err := schemaflux.Suggest[suggestionItem](map[string]any{"goal": "reduce churn", "signal": "customers leave after poor onboarding"}, opts)
	if err != nil {
		return err
	}
	return req(len(out) > 0, "suggest returned no items")
}
func testRedactLLM() error {
	opts := schemaflux.NewRedactLLMOptions()
	opts.Categories = []string{"email", "phone"}
	opts.ShowFirst = 2
	opts.ShowLast = 2
	out, err := schemaflux.RedactLLM("Contact jane@example.com or 555-123-4567 for support.", opts)
	if err != nil {
		return err
	}
	return req(len(out.Spans) > 0 && out.Text != out.Original, "redactllm found nothing")
}
func testComplete() error {
	opts := schemaflux.NewCompleteOptions()
	opts.MaxLength = 60
	out, err := schemaflux.Complete("The migration succeeded because", opts)
	if err != nil {
		return err
	}
	return req(out.Text != "" && len(out.Text) > len(out.Original), "complete did not add text")
}
func testCompleteField() error {
	opts := schemaflux.NewCompleteFieldOptions("Body")
	opts.MaxLength = 80
	out, err := schemaflux.CompleteField(draft{Title: "Postmortem", Body: "We discovered the incident started when"}, opts)
	if err != nil {
		return err
	}
	return req(out.Data.Body != "" && len(out.Data.Body) > len(out.Original), "completefield did not add text")
}
func testValidate() error {
	opts := schemaflux.NewValidateOptions()
	opts.Rules = "email must be valid and age must be at least 18"
	out, err := schemaflux.Validate(map[string]any{"email": "bad-email", "age": 14}, opts)
	if err != nil {
		return err
	}
	return req(!out.Valid, "validate should flag invalid data")
}
func testValidateLegacy() error {
	out, err := schemaflux.ValidateLegacy(map[string]any{"email": "no-at-symbol", "age": 12}, "email must be valid and age must be at least 18", schemaflux.OpOptions{Mode: schemaflux.Strict, Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(!out.Valid, "legacy validate should flag invalid data")
}
func testQuestion() error {
	out, err := schemaflux.Question[map[string]any, bool](map[string]any{"email": "valid@example.com", "age": 24}, schemaflux.NewQuestionOptions("Is this signup eligible for the program?"))
	if err != nil {
		return err
	}
	return req(out.Confidence > 0, "question confidence missing")
}
func testQuestionLegacy() error {
	out, err := schemaflux.QuestionLegacy(map[string]any{"email": "vip@example.com", "age": 30}, "What email is on the record?", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out) != "", "legacy question empty")
}
func testMerge() error {
	out, err := schemaflux.Merge([]map[string]any{{"name": "Riley Chen", "email": "", "role": "AE"}, {"name": "Riley Chen", "email": "riley@acme.com", "role": ""}}, "prefer the most complete record", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(s(out["email"]) != "", "merge output incomplete")
}
func testMergeWithMetadata() error {
	out, err := schemaflux.MergeWithMetadata([]map[string]any{{"name": "Toni Ruiz", "email": "", "role": "Manager"}, {"name": "Toni Ruiz", "email": "toni@example.com", "role": "Senior Manager"}}, "prefer newest contact details", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(s(out.Merged["email"]) != "" && out.Confidence > 0, "merge metadata invalid")
}
func testFormat() error {
	out, err := schemaflux.Format(map[string]any{"name": "Dana Kim", "email": "dana@example.com", "role": "PM"}, "professional two-sentence bio", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out) != "", "format output empty")
}
func testFormatWithMetadata() error {
	out, err := schemaflux.FormatWithMetadata(map[string]any{"name": "Dana Kim", "email": "dana@example.com", "role": "PM"}, "markdown bullet list", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(out.Text != "" && out.Confidence > 0, "format metadata invalid")
}
func testDecide() error {
	_, meta, err := schemaflux.Decide(map[string]any{"budget": "low", "deadline": "soon", "risk_tolerance": "low"}, []schemaflux.Decision[string]{{Value: "ship now", Description: "Ship immediately with current scope"}, {Value: "cut scope", Description: "Cut risky features and ship core workflow"}, {Value: "delay", Description: "Delay release until all features are done"}}, schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(meta.SelectedIndex >= 0 && !strings.Contains(strings.ToLower(meta.Explanation), "default"), "decide used fallback")
}
func testAnnotate() error {
	opts := schemaflux.NewAnnotateOptions()
	opts.AnnotationTypes = []string{"entities", "topics", "sentiment"}
	out, err := schemaflux.Annotate("OpenAI launched a new API improvement for enterprise support teams in New York.", opts)
	if err != nil {
		return err
	}
	return req(len(out.Annotations) > 0, "annotate returned no annotations")
}
func testCluster() error {
	opts := schemaflux.NewClusterOptions()
	opts.ClusterBy = "topic"
	out, err := schemaflux.Cluster([]string{"bug report about checkout failures", "incident on login service", "feature request for dark mode", "request for calendar integration"}, opts)
	if err != nil {
		return err
	}
	return req(out.NumClusters > 0, "cluster created no clusters")
}
func testRank() error {
	opts := schemaflux.NewRankOptions()
	opts.Query = "best candidate for a Go backend role"
	out, err := schemaflux.Rank([]map[string]any{{"name": "Alex", "years": 2, "skills": []string{"Go", "SQL"}}, {"name": "Priya", "years": 6, "skills": []string{"Go", "Kubernetes"}}, {"name": "Leo", "years": 4, "skills": []string{"JavaScript", "React"}}}, opts)
	if err != nil {
		return err
	}
	return req(len(out.Items) > 0 && out.ReturnedItems > 0, "rank returned no items")
}
func testCompress() error {
	opts := schemaflux.NewCompressOptions()
	opts.CompressionRatio = 0.5
	opts.CommonOptions.Steering = "Keep the same structured object shape as the input."
	out, err := schemaflux.Compress(noteSummary{Title: "Sprint review", Summary: "The team covered shipped work, unresolved bugs, customer feedback, and next sprint goals.", Actions: []string{"fix login bug", "publish release notes"}}, opts)
	if err != nil {
		return err
	}
	return req(out.ActualRatio > 0, "compress ratio invalid")
}
func testCompressText() error {
	out, err := schemaflux.CompressText("This update explains the launch timeline, rollout steps, fallback plan, and ownership for each dependency.", schemaflux.NewCompressOptions())
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out) != "", "compresstext empty")
}
func testDecompose() error {
	opts := schemaflux.NewDecomposeOptions()
	opts.TargetParts = 3
	out, err := schemaflux.Decompose(map[string]any{"goal": "Launch self-serve billing", "plan": "Design pricing page, integrate Stripe, migrate customers, and update support docs."}, opts)
	if err != nil {
		return err
	}
	return req(out.TotalParts > 0, "decompose returned no parts")
}
func testDecomposeToSlice() error {
	opts := schemaflux.NewDecomposeOptions()
	opts.TargetParts = 3
	opts.CommonOptions.Steering = "Return only a JSON array of parts. Do not wrap the array in an object."
	out, err := schemaflux.DecomposeToSlice[map[string]any, taskItem](map[string]any{"goal": "Improve onboarding", "plan": "Rewrite welcome email, add checklist, and instrument activation metrics."}, opts)
	if err != nil {
		return err
	}
	return req(len(out) > 0, "decomposetoslice returned no parts")
}
func testEnrich() error {
	opts := schemaflux.NewEnrichOptions()
	opts.DeriveFields = []string{"industry", "summary"}
	opts.DerivationRules = map[string]string{
		"industry": "Infer the company's industry from its name and domain",
		"summary":  "Write a concise one-sentence description of what the company does",
	}
	out, err := schemaflux.Enrich[companySeed, companyInfo](companySeed{Name: "Figma", Domain: "figma.com"}, opts)
	if err != nil {
		return err
	}
	return req(out.Enriched.Summary != "" && out.Enriched.Industry != "", "enrich output incomplete")
}
func testEnrichInPlace() error {
	opts := schemaflux.NewEnrichOptions()
	opts.DeriveFields = []string{"industry", "summary"}
	out, err := schemaflux.EnrichInPlace(map[string]any{"name": "Notion", "domain": "notion.so", "industry": "", "summary": ""}, opts)
	if err != nil {
		return err
	}
	return req(s(out["summary"]) != "" || s(out["industry"]) != "", "enrichinplace added nothing")
}
func testNormalize() error {
	opts := schemaflux.NewNormalizeOptions()
	opts.NormalizeCase = "title"
	out, err := schemaflux.Normalize(map[string]any{"name": "  jane DOE", "email": "JANE@EXAMPLE.COM ", "city": " new york "}, opts)
	if err != nil {
		return err
	}
	return req(out.TotalChanges > 0, "normalize made no changes")
}
func testNormalizeText() error {
	out, err := schemaflux.NormalizeText("  ACME   incorporated  ", schemaflux.NewNormalizeOptions())
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out) != "", "normalizetext empty")
}
func testNormalizeBatch() error {
	out, err := schemaflux.NormalizeBatch([]map[string]any{{"name": "john SMITH", "email": "JOHN@example.com"}, {"name": "  maria  lopez", "email": "maria@example.com "}}, schemaflux.NewNormalizeOptions())
	if err != nil {
		return err
	}
	return req(len(out) == 2, "normalizebatch wrong count")
}
func testSemanticMatch() error {
	opts := schemaflux.NewMatchOptions()
	opts.MatchCriteria = "match by company and business need"
	out, err := schemaflux.SemanticMatch([]map[string]any{{"name": "Jordan", "company": "Acme", "industry": "Retail", "use_case": "forecasting"}}, []map[string]any{{"account_name": "Acme Corp", "sector": "Retail", "need": "sales forecasting"}, {"account_name": "Beta Inc", "sector": "Health", "need": "billing"}}, opts)
	if err != nil {
		return err
	}
	return req(out.TotalMatches > 0, "semanticmatch returned no matches")
}
func testMatchOne() error {
	opts := schemaflux.NewMatchOptions()
	opts.MatchCriteria = "best semantic fit"
	out, err := schemaflux.MatchOne(map[string]any{"name": "Jordan", "company": "Acme", "industry": "Retail", "use_case": "forecasting"}, []map[string]any{{"account_name": "Acme Corp", "sector": "Retail", "need": "sales forecasting"}, {"account_name": "Gamma", "sector": "Finance", "need": "reconciliation"}}, opts)
	if err != nil {
		return err
	}
	return req(len(out) > 0, "matchone returned no matches")
}
func testCritique() error {
	opts := schemaflux.NewCritiqueOptions()
	opts.Criteria = []string{"clarity", "specificity"}
	out, err := schemaflux.Critique("This plan is probably good and we should maybe move fast somehow.", opts)
	if err != nil {
		return err
	}
	return req(out.Summary != "", "critique summary empty")
}
func testSynthesize() error {
	opts := schemaflux.NewSynthesizeOptions()
	opts.Strategy = "integrate"
	out, err := schemaflux.Synthesize[synthesisNote]([]any{map[string]any{"topic": "Churn", "summary": "Customers leave when setup stalls."}, map[string]any{"topic": "Churn", "summary": "Fast activation increases retention."}}, opts)
	if err != nil {
		return err
	}
	return req(strings.TrimSpace(out.Synthesized.OverallInsight) != "" || len(out.Facts) > 0 || len(out.Insights) > 0, "synthesize output empty")
}
func testPredict() error {
	opts := schemaflux.NewPredictOptions()
	opts.Horizon = "next month"
	out, err := schemaflux.Predict[float64]([]map[string]any{{"period": "January", "value": 100.0}, {"period": "February", "value": 112.0}, {"period": "March", "value": 118.0}}, opts)
	if err != nil {
		return err
	}
	return req(out.Prediction > 0 && out.Confidence > 0, "predict output invalid")
}
func testVerify() error {
	opts := schemaflux.NewVerifyOptions()
	opts.CheckFacts = false
	opts.CheckLogic = true
	opts.CheckConsistency = true
	out, err := schemaflux.Verify("All premium users get priority support. Alex is a premium user. Therefore Alex gets priority support.", opts)
	if err != nil {
		return err
	}
	return req(out.Summary != "" && out.OverallConfidence > 0, "verify output invalid")
}
func testVerifyClaim() error {
	opts := schemaflux.NewVerifyOptions()
	opts.CheckFacts = false
	out, err := schemaflux.VerifyClaim("A statement cannot be both true and false in the same sense at the same time.", opts)
	if err != nil {
		return err
	}
	return req(out.Verdict != "" && out.Confidence > 0, "verifyclaim invalid")
}
func testNegotiate() error {
	out, err := schemaflux.Negotiate[map[string]any](map[string]any{"candidate_min_salary": 145000, "company_max_salary": 150000, "remote_preference": "3 days", "bonus_target": 10000}, schemaflux.NegotiateOptions{Strategy: "balanced", Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(out.OverallSatisfaction > 0, "negotiate satisfaction missing")
}
func testNegotiateAdversarial() error {
	ctx := schemaflux.AdversarialContext[map[string]any]{Ours: schemaflux.AdversarialPosition[map[string]any]{Position: map[string]any{"base_salary": 160000, "remote_days": 5, "bonus": 15000}}, Theirs: schemaflux.AdversarialPosition[map[string]any]{Position: map[string]any{"base_salary": 140000, "remote_days": 2, "bonus": 5000}}, OurLeverage: "strong"}
	out, err := schemaflux.NegotiateAdversarial(ctx, schemaflux.AdversarialOptions{Strategy: "balanced", Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(out.Confidence > 0, "adversarial negotiation confidence missing")
}
func testResolve() error {
	out, err := schemaflux.Resolve([]map[string]any{{"id": "C1", "name": "Jordan Lee", "email": "jordan@old.com", "phone": ""}, {"id": "C1", "name": "Jordan Lee", "email": "jordan@new.com", "phone": "555-1000"}}, schemaflux.ResolveOptions{Strategy: "most-complete", Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(s(out.Resolved["email"]) != "" && out.Confidence > 0, "resolve output invalid")
}
func testDerive() error {
	type src struct {
		Name      string `json:"name"`
		BirthYear int    `json:"birth_year"`
		City      string `json:"city"`
	}
	out, err := schemaflux.Derive[src, derivedPerson](src{Name: "Kai", BirthYear: 1994, City: "Seattle"}, schemaflux.DeriveOptions{Fields: []string{"age", "generation"}, Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(out.Derived.Generation != "" && out.OverallConfidence > 0, "derive output invalid")
}
func testConform() error {
	out, err := schemaflux.Conform(map[string]any{"name": "john doe", "street": "123 n main st apt 4", "city": "los angeles", "state": "california", "zip_code": "90210"}, "USPS", schemaflux.ConformOptions{Validate: true, Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(out.Compliance > 0 && s(out.Conformed["city"]) != "", "conform invalid")
}
func testInterpolate() error {
	out, err := schemaflux.Interpolate([]metricPoint{{Day: "2026-01-01", Value: 100}, {Day: "2026-01-02", Value: 0}, {Day: "2026-01-03", Value: 120}}, schemaflux.InterpolateOptions{Method: "pattern", Steering: "Zero means missing", Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(len(out.Complete) == 3 && (out.GapCount > 0 || len(out.Filled) > 0 || out.Complete[1].Value != 0), "interpolate invalid")
}
func testArbitrate() error {
	out, err := schemaflux.Arbitrate([]map[string]any{{"name": "Ava", "years": 3, "skills": []string{"Go", "SQL"}}, {"name": "Noah", "years": 6, "skills": []string{"Go", "Kubernetes"}}}, schemaflux.ArbitrateOptions{Rules: []string{"must know Go", "prefer 5+ years experience"}, Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(s(out.Winner["name"]) != "" && out.Confidence > 0, "arbitrate invalid")
}
func testProject() error {
	out, err := schemaflux.Project[map[string]any, map[string]any](map[string]any{"id": "u1", "email": "zoe@example.com", "password_hash": "hash", "first_name": "Zoe", "last_name": "Miller", "created_at": "2025-01-01"}, schemaflux.ProjectOptions{Mappings: map[string]string{"id": "user_id", "created_at": "member_since"}, Exclude: []string{"password_hash", "email"}, InferMissing: true, Steering: "Combine first_name and last_name into display_name", Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(s(out.Projected["user_id"]) != "" && s(out.Projected["display_name"]) != "", "project invalid")
}
func testAudit() error {
	out, err := schemaflux.Audit(map[string]any{"id": "1", "email": "sam@example.com", "password": "plaintext-password", "ssn": "123-45-6789"}, schemaflux.AuditOptions{Policies: []string{"passwords must not be stored in plain text", "ssn must be protected"}, Categories: []string{"security", "compliance"}, Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(len(out.Findings) > 0, "audit returned no findings")
}
func testAssemble() error {
	out, err := schemaflux.Assemble[map[string]any]([]any{map[string]any{"name": "Acme"}, map[string]any{"industry": "Software"}, map[string]any{"summary": "Builds workflow tools for finance teams."}}, schemaflux.ComposeOptions{MergeStrategy: "smart", FillGaps: true, Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(s(out.Composed["name"]) != "" && s(out.Composed["summary"]) != "", "assemble invalid")
}
func testPivot() error {
	out, err := schemaflux.Pivot[[]map[string]any, []map[string]any]([]map[string]any{{"product": "Widget", "quarter": "Q1", "revenue": 10000}, {"product": "Widget", "quarter": "Q2", "revenue": 12000}}, schemaflux.PivotOptions{PivotOn: []string{"quarter"}, GroupBy: []string{"product"}, Aggregate: "sum", Intelligence: schemaflux.Fast})
	if err != nil {
		return err
	}
	return req(len(out.Pivoted) > 0, "pivot returned no rows")
}

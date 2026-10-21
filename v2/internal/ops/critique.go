package ops

import (
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// CritiqueOptions configures the Critique operation
type CritiqueOptions struct {
	CommonOptions
	types.OpOptions

	// Criteria to evaluate against
	Criteria []string

	// Rubric for evaluation (criteria -> description)
	Rubric map[string]string

	// Include suggestions for improvement. When it is off, suggestions the
	// model wrote anyway are removed from the issues.
	IncludeSuggestions bool

	// Include specific fixes. When it is off, fixes the model wrote anyway are
	// removed from the issues.
	IncludeFixes bool

	// Severity levels to include ("all", "major", "minor", "critical")
	SeverityFilter string

	// Maximum number of issues to report (0 for unlimited)
	MaxIssues int

	// Domain context for evaluation
	Domain string

	// Target audience for the critique
	Audience string

	// Critique style ("constructive", "harsh", "balanced")
	Style string

	// Include positive feedback. When it is off, positives the model wrote
	// anyway are removed from the result.
	IncludePositives bool
}

// NewCritiqueOptions creates CritiqueOptions with defaults
func NewCritiqueOptions() CritiqueOptions {
	return CritiqueOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		IncludeSuggestions: true,
		IncludeFixes:       true,
		SeverityFilter:     "all",
		MaxIssues:          0,
		Style:              "constructive",
		IncludePositives:   true,
	}
}

// Validate validates CritiqueOptions
func (c CritiqueOptions) Validate() error {
	if err := c.CommonOptions.Validate(); err != nil {
		return err
	}
	if len(c.Criteria) == 0 && len(c.Rubric) == 0 {
		return fmt.Errorf("at least one criterion or rubric entry is required")
	}
	validSeverities := map[string]bool{"all": true, "major": true, "minor": true, "critical": true}
	if c.SeverityFilter != "" && !validSeverities[c.SeverityFilter] {
		return fmt.Errorf("invalid severity filter: want all, critical, major or minor")
	}
	validStyles := map[string]bool{"constructive": true, "harsh": true, "balanced": true}
	if c.Style != "" && !validStyles[c.Style] {
		return fmt.Errorf("critique style must be constructive, harsh, or balanced")
	}
	return nil
}

// WithCriteria sets the evaluation criteria
func (c CritiqueOptions) WithCriteria(criteria []string) CritiqueOptions {
	c.Criteria = criteria
	return c
}

// WithRubric sets the evaluation rubric
func (c CritiqueOptions) WithRubric(rubric map[string]string) CritiqueOptions {
	c.Rubric = rubric
	return c
}

// WithIncludeSuggestions enables improvement suggestions
func (c CritiqueOptions) WithIncludeSuggestions(include bool) CritiqueOptions {
	c.IncludeSuggestions = include
	return c
}

// WithIncludeFixes enables specific fixes
func (c CritiqueOptions) WithIncludeFixes(include bool) CritiqueOptions {
	c.IncludeFixes = include
	return c
}

// WithSeverityFilter sets the severity filter
func (c CritiqueOptions) WithSeverityFilter(filter string) CritiqueOptions {
	c.SeverityFilter = filter
	return c
}

// WithMaxIssues sets the maximum issues to report
func (c CritiqueOptions) WithMaxIssues(maxIssues int) CritiqueOptions {
	c.MaxIssues = maxIssues
	return c
}

// WithDomain sets the domain context
func (c CritiqueOptions) WithDomain(domain string) CritiqueOptions {
	c.Domain = domain
	return c
}

// WithAudience sets the target audience
func (c CritiqueOptions) WithAudience(audience string) CritiqueOptions {
	c.Audience = audience
	return c
}

// WithStyle sets the critique style
func (c CritiqueOptions) WithStyle(style string) CritiqueOptions {
	c.Style = style
	return c
}

// WithIncludePositives includes positive feedback
func (c CritiqueOptions) WithIncludePositives(include bool) CritiqueOptions {
	c.IncludePositives = include
	return c
}

// WithSteering sets the steering prompt
func (c CritiqueOptions) WithSteering(steering string) CritiqueOptions {
	c.CommonOptions = c.CommonOptions.WithSteering(steering)
	return c
}

// WithMode sets the mode
func (c CritiqueOptions) WithMode(mode types.Mode) CritiqueOptions {
	c.CommonOptions = c.CommonOptions.WithMode(mode)
	return c
}

// WithIntelligence sets the intelligence level
func (c CritiqueOptions) WithIntelligence(intelligence types.Speed) CritiqueOptions {
	c.CommonOptions = c.CommonOptions.WithIntelligence(intelligence)
	return c
}

func (c CritiqueOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(c.CommonOptions, c.OpOptions)
}

func (c CritiqueOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return c.CommonOptions, c.OpOptions
}

// CritiqueIssue represents a single issue found
type CritiqueIssue struct {
	Criterion   string `json:"criterion"`
	Severity    string `json:"severity"` // "critical", "major", "minor"
	Description string `json:"description"`
	Location    string `json:"location,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
	Fix         string `json:"fix,omitempty"`
	Impact      string `json:"impact,omitempty"`
}

// CritiquePositive represents positive feedback
type CritiquePositive struct {
	Criterion   string `json:"criterion"`
	Description string `json:"description"`
	Strength    string `json:"strength,omitempty"`
}

// CritiqueResult contains the results of critique
type CritiqueResult struct {
	ModelOverallScore float64            `json:"overall_score"`
	CriteriaScores    map[string]float64 `json:"criteria_scores"`
	Issues            []CritiqueIssue    `json:"issues"`
	Positives         []CritiquePositive `json:"positives,omitempty"`
	Summary           string             `json:"summary"`
	TopPriorities     []string           `json:"top_priorities,omitempty"`
	Metadata          map[string]any     `json:"metadata,omitempty"`
}

// Critique evaluates content with actionable feedback and improvement suggestions.
// Goes beyond Score by providing specific issues, fixes, and positive feedback.
//
// Examples:
//
//	// Critique an essay
//	result, err := Critique(essay, NewCritiqueOptions().
//	    WithCriteria([]string{"clarity", "argument_strength", "evidence"}).
//	    WithIncludeFixes(true))
//
//	// Code review style critique
//	result, err := Critique(code, NewCritiqueOptions().
//	    WithDomain("software").
//	    WithRubric(map[string]string{
//	        "readability": "Is the code easy to understand?",
//	        "efficiency": "Are there performance issues?",
//	        "security": "Are there security vulnerabilities?",
//	    }))
//
//	// Content review
//	result, err := Critique(article, NewCritiqueOptions().
//	    WithAudience("general public").
//	    WithStyle("constructive").
//	    WithMaxIssues(5))
//
// Deprecated: use CritiqueWithModel, which returns the shared
// types.JudgmentResult and whose name says what Critique's did not -- that
// this evaluation is model-assisted, not a deterministic check.
func Critique[T any](input T, opts CritiqueOptions) (CritiqueResult, error) {
	return critiqueCore(input, opts)
}

// CritiqueWithModel evaluates content the same way the deprecated Critique
// does -- against the requested criteria or rubric, with the same style,
// severity filter, and issue cap -- and reports it as a
// types.JudgmentResult: Verdict is derived from the issues found (Fail if
// any is "critical", Mixed if issues exist without one, Pass if none),
// CriteriaScores and TopPriorities travel in ModelClaims, and
// ModelOverallScore becomes ModelConfidence -- it is the model's own account
// of its evaluation, not a measurement of the content, and belongs beside
// the rest of what the model claimed rather than next to Verdict.
//
// Behavior -- which issues and positives are produced -- is identical to
// the deprecated Critique; only the result shape differs.
func CritiqueWithModel[T any](input T, opts CritiqueOptions) (types.JudgmentResult[T], error) {
	cr, err := critiqueCore(input, opts)
	if err != nil {
		return types.JudgmentResult[T]{}, err
	}
	return critiqueResultToJudgment(input, cr), nil
}

// critiqueResultToJudgment translates CritiqueResult into the shared shape.
func critiqueResultToJudgment[T any](input T, cr CritiqueResult) types.JudgmentResult[T] {
	issues := make([]types.JudgmentIssue, 0, len(cr.Issues))
	verdict := types.VerdictPass
	for _, issue := range cr.Issues {
		severity := normalizeSeverity(issue.Severity)
		if severity == "critical" {
			verdict = types.VerdictFail
		} else if verdict == types.VerdictPass {
			verdict = types.VerdictMixed
		}
		issues = append(issues, types.JudgmentIssue{
			Subject:    issue.Criterion,
			Category:   issue.Criterion,
			Severity:   severity,
			Message:    issue.Description,
			Suggestion: firstNonEmpty(issue.Fix, issue.Suggestion),
			Evidence:   evidenceSlice(issue.Location),
		})
	}

	return types.JudgmentResult[T]{
		Subject:         input,
		Verdict:         verdict,
		Issues:          issues,
		Summary:         cr.Summary,
		ModelConfidence: cr.ModelOverallScore,
		ModelClaims: map[string]any{
			"criteria_scores": cr.CriteriaScores,
			"positives":       cr.Positives,
			"top_priorities":  cr.TopPriorities,
		},
	}
}

// firstNonEmpty returns fix when it is set, falling back to suggestion.
// CritiqueIssue carries both a concrete fix and a softer suggestion;
// JudgmentIssue has one Suggestion field, and the fix -- being the more
// actionable of the two -- takes priority rather than being dropped.
func firstNonEmpty(fix, suggestion string) string {
	if fix != "" {
		return fix
	}
	return suggestion
}

// critiqueCore is the implementation the deprecated Critique and
// CritiqueWithModel both call, so neither can drift from the other.
func critiqueCore[T any](input T, opts CritiqueOptions) (CritiqueResult, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting critique operation")

	// Every return that carries an error carries the zero result with it.

	// Validate options
	if err := opts.Validate(); err != nil {
		return CritiqueResult{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx, cancel := operationContext(opt.Context, 0)
	defer cancel()

	// Convert input to string
	inputStr, err := NormalizeInput(input)
	if err != nil {
		log.Error("Critique operation failed: input normalization error", "error", loggedError(err))
		return CritiqueResult{}, fmt.Errorf("failed to normalize input: %w", err)
	}

	// Build criteria list. Rubric entries follow in criterion order (ranging
	// over the map wrote them in a different order on every call), and every
	// entry is the caller's text, written as a JSON string.
	var criteriaList []string
	for _, c := range opts.Criteria {
		quoted, _ := promptJSON(c)
		criteriaList = append(criteriaList, fmt.Sprintf("- %s", quoted))
	}
	for _, criterion := range sortedKeys(opts.Rubric) {
		quotedCriterion, _ := promptJSON(criterion)
		quotedDescription, _ := promptJSON(opts.Rubric[criterion])
		criteriaList = append(criteriaList, fmt.Sprintf("- %s: %s", quotedCriterion, quotedDescription))
	}

	domainDesc := ""
	if opts.Domain != "" {
		quoted, _ := promptJSON(opts.Domain)
		domainDesc = fmt.Sprintf("\nDomain: %s", quoted)
	}

	audienceDesc := ""
	if opts.Audience != "" {
		quoted, _ := promptJSON(opts.Audience)
		audienceDesc = fmt.Sprintf("\nTarget audience: %s", quoted)
	}

	styleDesc := ""
	switch opts.Style {
	case "constructive":
		styleDesc = "\nStyle: Be constructive and encouraging while identifying issues."
	case "harsh":
		styleDesc = "\nStyle: Be direct and critical, focus on problems."
	case "balanced":
		styleDesc = "\nStyle: Balance positive and negative feedback equally."
	}

	// An empty filter means "all", as Validate and the check below treat it.
	// Only "all" was tested here, so an empty one told the model to "Only
	// report  issues."
	severityDesc := ""
	if opts.SeverityFilter != "" && opts.SeverityFilter != "all" {
		severityDesc = fmt.Sprintf("\nOnly report %s issues.", opts.SeverityFilter)
	}

	maxIssuesDesc := ""
	if opts.MaxIssues > 0 {
		maxIssuesDesc = fmt.Sprintf("\nLimit to top %d issues.", opts.MaxIssues)
	}

	suggestionsNote := ""
	if opts.IncludeSuggestions {
		suggestionsNote = "\nInclude actionable suggestions for each issue."
	}

	fixesNote := ""
	if opts.IncludeFixes {
		fixesNote = "\nInclude specific fixes where possible."
	}

	positivesNote := ""
	if opts.IncludePositives {
		positivesNote = "\nInclude positive feedback on what's done well."
	}

	systemPrompt := fmt.Sprintf(`You are an expert critic and evaluator. Provide thorough, actionable feedback.

Criteria to evaluate:
%s%s%s%s%s%s%s%s%s

Return a JSON object with:
{
  "overall_score": 0.75,
  "criteria_scores": {"criterion1": 0.8, "criterion2": 0.7},
  "issues": [
    {
      "criterion": "clarity",
      "severity": "major",
      "description": "The introduction is confusing",
      "location": "first paragraph",
      "suggestion": "Rewrite to clearly state the thesis",
      "fix": "Replace 'The thing about...' with 'This paper argues that...'",
      "impact": "Readers may not understand the main point"
    }
  ],
  "positives": [
    {
      "criterion": "evidence",
      "description": "Excellent use of citations",
      "strength": "Strong academic foundation"
    }
  ],
  "summary": "Overall assessment in 2-3 sentences",
  "top_priorities": ["Most important improvement", "Second priority"]
}`, strings.Join(criteriaList, "\n"), domainDesc, audienceDesc, styleDesc, severityDesc, maxIssuesDesc, suggestionsNote, fixesNote, positivesNote)

	userPrompt := fmt.Sprintf("Critique this:\n\n%s", inputStr)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Critique operation LLM call failed", "error", loggedError(err))
		return CritiqueResult{}, fmt.Errorf("critique failed: %w", err)
	}

	// Parse the response. Every member the prompt shows is required, and so are
	// the criterion, the severity and the description of an issue and of a
	// positive; the rest of an issue is asked for by an option. The positives are
	// asked for only when IncludePositives is set, so they may be left out. A
	// missing score used to be read as 0, and a missing issues list as "nothing
	// was found", which made {"summary":"looks fine"} a passing verdict.
	var answer struct {
		ModelOverallScore float64            `json:"overall_score"`
		CriteriaScores    map[string]float64 `json:"criteria_scores"`
		Issues            []CritiqueIssue    `json:"issues"`
		Positives         []CritiquePositive `json:"positives,omitempty"`
		Summary           string             `json:"summary"`
		TopPriorities     []string           `json:"top_priorities"`
	}
	if err := decodeAnswer("critique", response, &answer); err != nil {
		log.Error("Critique operation failed: parse error", "error", loggedError(err))
		return CritiqueResult{}, err
	}
	// An empty list says nothing was found. A null is a list the decoder allows
	// for a list, and says nothing.
	if answer.Issues == nil {
		log.Error("Critique operation failed: the issues list is null")
		return CritiqueResult{}, types.Newf(types.KindSchemaViolation, "critique",
			"issues is null; an empty list says nothing was found, a null says nothing")
	}
	issues := answer.Issues

	result := CritiqueResult{
		CriteriaScores: answer.CriteriaScores,
		Issues:         issues,
		Positives:      answer.Positives,
		Summary:        answer.Summary,
		TopPriorities:  answer.TopPriorities,
		Metadata:       make(map[string]any),
	}
	if result.CriteriaScores == nil {
		result.CriteriaScores = make(map[string]float64)
	}
	result.ModelOverallScore = answer.ModelOverallScore
	if err := onScale("critique", "the overall score", result.ModelOverallScore); err != nil {
		return CritiqueResult{}, err
	}

	// The scores are the model's claims, and each has to be on the scale it was
	// asked for. Every criterion that was put has to be scored: a criterion the
	// model skipped is one the caller asked about and was not told about.
	for _, criterion := range sortedKeys(result.CriteriaScores) {
		if err := onScale("critique", "a criterion's score", result.CriteriaScores[criterion]); err != nil {
			return CritiqueResult{}, err
		}
	}
	scored := make(map[string]struct{}, len(result.CriteriaScores))
	for criterion := range result.CriteriaScores {
		scored[strings.ToLower(strings.TrimSpace(criterion))] = struct{}{}
	}
	asked := append(append([]string(nil), opts.Criteria...), sortedKeys(opts.Rubric)...)
	for _, criterion := range asked {
		if _, ok := scored[strings.ToLower(strings.TrimSpace(criterion))]; !ok {
			log.Error("Critique operation failed: a criterion was not scored")
			return CritiqueResult{}, types.Newf(types.KindSchemaViolation, "critique",
				"criteria_scores does not score every criterion that was asked for")
		}
	}

	// MaxIssues was rendered into the prompt as "Limit to top %d issues." and
	// compared against nothing, even though AtMost exists in invariants.go for
	// exactly this. A caller who caps the list is sizing something downstream --
	// a UI, a report, a budget -- and silently getting more is the failure that
	// cap was meant to prevent.
	if err := AtMost(result.Issues, opts.MaxIssues); err != nil {
		log.Error("Critique returned more issues than requested", "error", loggedError(err))
		return CritiqueResult{}, fmt.Errorf("critique: %w", err)
	}

	// SeverityFilter narrows what the caller wants reported, and an issue
	// carrying a different severity is the model answering a question that was
	// not asked. "all" is the default and opts out; an issue with no severity
	// label is not matched against anything, because refusing an unlabeled
	// issue would discard a real finding over a missing string.
	if opts.SeverityFilter != "" && opts.SeverityFilter != "all" {
		for i, issue := range result.Issues {
			if issue.Severity == "" || issue.Severity == opts.SeverityFilter {
				continue
			}
			// The issue's severity is the model's word and is neither logged nor
			// put in the error: the position says which issue, and the filter is
			// one of the four words Validate accepts.
			log.Error("Critique returned an issue outside the requested severity",
				"issueIndex", i, "severityFilter", opts.SeverityFilter)
			return CritiqueResult{}, types.Newf(types.KindInvariantViolation, "critique",
				"issue %d has a severity other than the %q that was requested",
				i, opts.SeverityFilter)
		}
	}

	// IncludeSuggestions, IncludeFixes and IncludePositives are requests to the
	// model that nothing said no to when they were off, so a model that wrote
	// them anyway returned them. What was not asked for is removed here.
	if !opts.IncludeSuggestions || !opts.IncludeFixes {
		for i := range result.Issues {
			if !opts.IncludeSuggestions {
				result.Issues[i].Suggestion = ""
			}
			if !opts.IncludeFixes {
				result.Issues[i].Fix = ""
			}
		}
	}
	if !opts.IncludePositives {
		result.Positives = nil
	}

	log.Debug("Critique operation succeeded",
		"overallScore", result.ModelOverallScore,
		"issueCount", len(result.Issues),
		"positiveCount", len(result.Positives))
	return result, nil
}

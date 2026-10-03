package ops

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// LX-029. What Audit asks for, what it accepts, and how it reaches a verdict.
//
// The model reports and this library decides. The model is given the policies
// by key and reports the violations of each under its key, and reports apart
// whatever else it finds. It is not told the threshold and is not asked whether
// the data passes. From that:
//
//   - A finding counts when it is about something the caller asked to have
//     checked: a violation of a policy they supplied, or a problem in a
//     category they named. With neither supplied, everything found counts.
//     With policies and no categories, a problem that is no policy's is
//     returned in Other and changes nothing. Live, compliant records failed an
//     audit of three policies on findings unrelated to any of them.
//   - A violation of a supplied policy fails the audit, whatever severity the
//     model gave it: the policy is the caller's rule. A finding that is no
//     policy's fails it at a severity of 0.7 or above.
//   - The verdict is worked out from every finding that counts, and only then
//     does Threshold decide which of them are listed. It used to filter first,
//     so raising it made a failing audit pass.
//   - An answer that does not report on every policy asked, or leaves out the
//     list of findings, is refused. `{}` used to be an audit with no findings,
//     which passes.

// auditCategories are the categories a finding may carry when the caller names
// none of their own.
var auditCategories = []string{"security", "compliance", "quality", "consistency", "completeness"}

// auditPolicyKey is the key policy i is reported under: "p1", "p2", and so on.
// The model answers by key, so a policy's own text, which is the caller's, is
// never something the answer has to reproduce and this library has to match.
func auditPolicyKey(i int) string { return "p" + strconv.Itoa(i+1) }

// distinctText returns the entries of a list that are not blank, trimmed, each
// once, in the order they first appear.
func distinctText(entries []string) []string {
	var kept []string
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		kept = append(kept, trimmed)
	}
	return kept
}

// distinctPolicies returns the policies that are not blank, each once, in the
// order they first appear, in the words the caller supplied them in. Two that
// differ only in surrounding space are one policy, and the first spelling is
// the one kept: findings and the summary report a policy by the text the caller
// gave, so a lookup by that text finds it. Only the prompt carries it trimmed.
func distinctPolicies(entries []string) []string {
	var kept []string
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		kept = append(kept, entry)
	}
	return kept
}

// auditFindingSchema is the schema of one finding whose category is one of
// those given.
func auditFindingSchema(categories []string) map[string]any {
	text := map[string]any{"type": "string"}
	enum := make([]any, len(categories))
	for i, category := range categories {
		enum[i] = category
	}
	return strictEnvelope(map[string]any{
		"category":       map[string]any{"type": "string", "enum": enum},
		"severity":       map[string]any{"type": "number"},
		"field":          text,
		"issue":          text,
		"evidence":       text,
		"recommendation": text,
	}, "category", "severity", "issue")
}

// auditViolationCategories are the categories a violation of a policy may
// carry: the caller's own and the five this library names. A caller who names
// "security" to focus the rest of the audit has not said that a violation of
// their accounting policy is a security problem, and a schema that allowed
// nothing else would make the model call it one.
func auditViolationCategories(categories []string) []string {
	return distinctText(append(append([]string(nil), categories...), auditCategories...))
}

// auditEnumBudget is how many enum values an audit's schema may hold in all.
// Each policy's list repeats the categories a violation may carry, so the
// count grows with the number of policies, and a provider's structured-output
// mode has a ceiling on it: the request is refused whole when it is passed.
// This is kept well under the ceiling OpenAI documents, and an audit that would
// pass it is refused here, with a count, before anything is sent.
const auditEnumBudget = strictEnumBudget

// auditEnumValues is how many enum values the schema for these policies and
// categories holds.
func auditEnumValues(policies, categories []string) int {
	other := len(categories)
	if other == 0 {
		other = len(auditCategories)
	}
	return len(policies)*len(auditViolationCategories(categories)) + other
}

// auditResponseSchema declares the answer: the violations of each policy under
// its key, every key required, and the findings that are no policy's.
func auditResponseSchema(policies, categories []string) map[string]any {
	other := categories
	if len(other) == 0 {
		other = auditCategories
	}
	properties := map[string]any{
		"findings": listOf(auditFindingSchema(other)),
	}
	if len(policies) > 0 {
		violation := auditFindingSchema(auditViolationCategories(categories))
		entries := make(map[string]any, len(policies))
		keys := make([]string, len(policies))
		// The policy's text is not put in the schema. It is the caller's, and
		// it goes in the message, as a quoted value, and nowhere else: it was
		// copied into each key's description here, unbounded, which put the
		// caller's text into what this library sends as its own declaration.
		for i := range policies {
			keys[i] = auditPolicyKey(i)
			entries[keys[i]] = listOf(violation)
		}
		properties["policies"] = strictEnvelope(entries, keys...)
	}
	return strictEnvelope(properties, sortedKeys(properties)...)
}

// auditUserPrompt is the message: everything the caller supplied, each part
// written as JSON so that it reads as data whatever it says.
func auditUserPrompt(data any, opt AuditOptions, policies, categories []string) (string, error) {
	var message strings.Builder
	quoted := func(value any) (string, error) {
		raw, err := promptJSON(value)
		if err != nil {
			return "", types.Newf(types.KindInvalidRequest, "audit", "the request could not be written as JSON")
		}
		return string(raw), nil
	}

	if len(policies) > 0 {
		message.WriteString("Policies:\n")
		for i, policy := range policies {
			text, err := quoted(policy)
			if err != nil {
				return "", err
			}
			message.WriteString(auditPolicyKey(i) + ": " + text + "\n")
		}
		message.WriteString("\n")
	}
	// The categories are named here, whoever chose them. They were only in
	// the schema, which a provider that does not enforce one never shows the
	// model: it then made a category up, and the whole audit was refused for
	// it.
	if len(policies) > 0 {
		text, err := quoted(auditViolationCategories(categories))
		if err != nil {
			return "", err
		}
		message.WriteString("Categories for a violation: " + text + "\n")
	}
	other := categories
	if len(other) == 0 {
		other = auditCategories
	}
	text, err := quoted(other)
	if err != nil {
		return "", err
	}
	if len(categories) > 0 {
		message.WriteString("Categories to check, and the only ones a finding may carry: " + text + "\n\n")
	} else {
		message.WriteString("Categories for a finding: " + text + "\n\n")
	}
	if opt.Shallow {
		message.WriteString("Inspect the top level only; do not look inside nested structures.\n\n")
	} else {
		message.WriteString("Inspect nested structures as well as the top level.\n\n")
	}
	if strings.TrimSpace(opt.Steering) != "" {
		text, err := quoted(opt.Steering)
		if err != nil {
			return "", err
		}
		message.WriteString("Guidance: " + text + "\n\n")
	}

	if data != nil {
		message.WriteString("Shape of the data: " + GenerateTypeSchema(reflect.TypeOf(data)) + "\n\n")
	}
	text, err = quoted(data)
	if err != nil {
		return "", err
	}
	message.WriteString("Data:\n" + text)
	return message.String(), nil
}

// auditFindingAnswer is one finding as the model writes it. The severity is a
// pointer so that one left out is not read as zero.
type auditFindingAnswer struct {
	Category       string   `json:"category"`
	Severity       *float64 `json:"severity"`
	Field          string   `json:"field"`
	Issue          string   `json:"issue"`
	Evidence       string   `json:"evidence"`
	Recommendation string   `json:"recommendation"`
}

// auditList reads one list of findings and checks each: a category that is one
// of those allowed, a severity on the scale, and an issue that says something.
// The category is returned in the spelling it was allowed under.
//
// The messages name the list and the position. They do not repeat anything the
// model wrote.
func auditList(raw json.RawMessage, where string, allowed []string, policy string) ([]AuditFinding, error) {
	refuse := func(format string, args ...any) ([]AuditFinding, error) {
		return nil, types.Newf(types.KindSchemaViolation, "audit", format, args...)
	}
	if text := strings.TrimSpace(string(raw)); text == "" || text == "null" {
		return refuse("the response does not report on %s", where)
	}
	var answers []auditFindingAnswer
	if err := json.Unmarshal(raw, &answers); err != nil {
		return refuse("%s is not a list of findings in the shape that was asked for", where)
	}

	findings := make([]AuditFinding, 0, len(answers))
	for i, answer := range answers {
		category := ""
		for _, candidate := range allowed {
			if strings.EqualFold(strings.TrimSpace(answer.Category), candidate) {
				category = candidate
				break
			}
		}
		switch {
		case category == "":
			return refuse("finding %d of %s has a category that is not one of those asked for", i, where)
		case answer.Severity == nil:
			return refuse("finding %d of %s has no severity", i, where)
		case strings.TrimSpace(answer.Issue) == "":
			return refuse("finding %d of %s does not say what is wrong", i, where)
		}
		if err := onScale("audit", "the severity of finding "+strconv.Itoa(i)+" of "+where, *answer.Severity); err != nil {
			return nil, err
		}
		findings = append(findings, AuditFinding{
			Category:       category,
			Severity:       *answer.Severity,
			Field:          answer.Field,
			Issue:          answer.Issue,
			Evidence:       answer.Evidence,
			Recommendation: answer.Recommendation,
			Policy:         policy,
		})
	}
	return findings, nil
}

// auditAnswer reads the model's answer into the violations of each policy, in
// the order the policies were supplied, and the findings that are no policy's.
func auditAnswer(response string, policies, categories []string) (violations [][]AuditFinding, other []AuditFinding, err error) {
	refuse := func(format string, args ...any) ([][]AuditFinding, []AuditFinding, error) {
		return nil, nil, types.Newf(types.KindSchemaViolation, "audit", format, args...)
	}

	// The findings are in every answer. The policies are in the schema only when
	// policies were supplied, so the member is optional here and an answer to a
	// call that supplied some is refused below when it does not report on them.
	// A null is not what the reader refuses for a raw member, so auditList and
	// the policy check below refuse it.
	var answer struct {
		Policies json.RawMessage `json:"policies,omitempty"`
		Findings json.RawMessage `json:"findings"`
	}
	if parseErr := decodeAnswer("audit", response, &answer); parseErr != nil {
		// The message is this library's own: the parser's can quote what the
		// model wrote.
		return nil, nil, parseErr
	}

	reported := strings.TrimSpace(string(answer.Policies))
	if len(policies) == 0 {
		// Nothing was asked about policies. An empty object is a provider
		// being tidy; anything in it is a report on a question nobody put.
		if reported != "" && reported != "null" {
			var empty map[string]json.RawMessage
			if json.Unmarshal(answer.Policies, &empty) != nil || len(empty) != 0 {
				return refuse("the response reports on policies, and none were supplied")
			}
		}
	} else {
		var byKey map[string]json.RawMessage
		if reported == "" || reported == "null" || json.Unmarshal(answer.Policies, &byKey) != nil {
			return refuse("the response does not report on the policies")
		}
		if len(byKey) != len(policies) {
			return refuse("the response reports on %d policies, and %d were supplied", len(byKey), len(policies))
		}
		allowed := auditViolationCategories(categories)
		violations = make([][]AuditFinding, len(policies))
		for i, policy := range policies {
			key := auditPolicyKey(i)
			raw, found := byKey[key]
			if !found {
				return refuse("the response does not report on policy %s", key)
			}
			found1, listErr := auditList(raw, "policy "+key, allowed, policy)
			if listErr != nil {
				return nil, nil, listErr
			}
			violations[i] = found1
		}
	}

	allowed := categories
	if len(allowed) == 0 {
		allowed = auditCategories
	}
	other, err = auditList(answer.Findings, "the findings", allowed, "")
	if err != nil {
		return nil, nil, err
	}
	return violations, other, nil
}

// auditCore is the one implementation behind Audit and AuditWithModel, so the
// two cannot drift apart.
//
// Nothing is returned with an error: a caller who reads the result of a failed
// audit reads an empty one, and an empty AuditSummary does not pass.
func auditCore[T any](data T, opts ...AuditOptions) (AuditResult[T], error) {
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting audit operation")

	// The options are one value. The variadic form is there so that they can be
	// left out; a second value was ignored without a word.
	if len(opts) > 1 {
		return AuditResult[T]{}, types.Newf(types.KindConfiguration, "audit",
			"%d option values were given, and only one is read", len(opts))
	}

	opt := AuditOptions{
		Threshold:    0.0, // list everything
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
	if len(opts) > 0 {
		// The threshold is looked at before the merge, which drops one that is
		// not above zero: a negative one or one that is not a number would
		// otherwise be taken for "not set".
		if threshold := opts[0].Threshold; math.IsNaN(threshold) || threshold < 0 || threshold > 1 {
			return AuditResult[T]{}, types.Newf(types.KindConfiguration, "audit",
				"Threshold has to be a severity from 0 to 1")
		}
		opt = mergeAuditOptions(opt, opts[0])
	}
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return AuditResult[T]{}, err
	}
	if opt.Deep && opt.Shallow {
		return AuditResult[T]{}, types.Newf(types.KindConfiguration, "audit",
			"Deep and Shallow are both set, and they ask for opposite things")
	}
	policies := distinctPolicies(opt.Policies)
	categories := distinctText(opt.Categories)
	if values := auditEnumValues(policies, categories); values > auditEnumBudget {
		return AuditResult[T]{}, types.Newf(types.KindConfiguration, "audit",
			"%d policies with %d categories is more than one audit can declare an answer for (%d choices, at most %d); audit the policies in smaller groups",
			len(policies), len(auditViolationCategories(categories)), values, auditEnumBudget)
	}

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	sentPolicies := make([]string, len(policies))
	for i, policy := range policies {
		sentPolicies[i] = strings.TrimSpace(policy)
	}
	userPrompt, err := auditUserPrompt(data, opt, sentPolicies, categories)
	if err != nil {
		log.Error("Audit operation failed: the request could not be written", "error", err)
		return AuditResult[T]{}, err
	}

	response, err := callLLM(ctx, auditSystemPrompt, userPrompt, types.OpOptions{
		Mode:            opt.Mode,
		Intelligence:    opt.Intelligence,
		Model:           opt.Model,
		Effort:          opt.Effort,
		MaxOutputTokens: opt.MaxOutputTokens,
		WebSearch:       opt.WebSearch,
		Context:         ctx,
		RequestID:       opt.RequestID,
		CorrelationID:   opt.CorrelationID,
		ResponseFormat:  "json",
		JSONSchema:      auditResponseSchema(policies, categories),
		SchemaName:      "audit",
	})
	if err != nil {
		log.Error("Audit operation LLM call failed", "error", err)
		return AuditResult[T]{}, err
	}

	violations, other, err := auditAnswer(response, policies, categories)
	if err != nil {
		// The error is this library's own words. The response is not logged:
		// it quotes the caller's data as evidence.
		log.Error("Audit operation failed: the answer was refused", "error", err)
		return AuditResult[T]{}, err
	}

	// What counts: every violation of a supplied policy, and a finding that is
	// no policy's unless the caller supplied policies and named no category,
	// in which case they asked about the policies and nothing else.
	var counted []AuditFinding
	for _, found := range violations {
		counted = append(counted, found...)
	}
	result := AuditResult[T]{Original: data, Metadata: make(map[string]any)}
	if len(policies) > 0 && len(categories) == 0 {
		result.Other = other
	} else {
		counted = append(counted, other...)
	}

	// The verdict comes from everything that counts. Threshold then decides
	// what is listed and tallied, and cannot turn a failure into a pass.
	verdict := buildAuditSummary(counted)
	hidden := 0
	for _, finding := range counted {
		if finding.Severity >= opt.Threshold {
			result.Findings = append(result.Findings, finding)
		} else {
			hidden++
		}
	}
	result.Summary = buildAuditSummary(result.Findings)
	result.Summary.Critical = verdict.Critical
	// A violation of a policy the caller supplied fails the audit, whatever
	// severity the model gave it. The policy is the caller's rule and the
	// violation is the fact the model was asked for; how bad it is, is the
	// model's opinion. The verdict used to turn on that opinion crossing 0.7:
	// live, a record that named no owner under "every record must name a data
	// owner" passed one run in three, because the model called it moderate.
	// Severity still decides for a finding that is no policy's.
	violated := 0
	for _, found := range violations {
		violated += len(found)
	}
	result.Summary.PassesAudit = verdict.PassesAudit && violated == 0
	result.Summary.BelowThreshold = hidden
	result.Metadata["findings_below_threshold"] = hidden

	// What was set apart does not count, and a caller should still be able to
	// see at a glance that something severe is in it: with policies supplied
	// and no categories, a finding of the highest severity that is no policy's
	// passes the audit, which is the rule, and passed it with no signal.
	for _, finding := range result.Other {
		if finding.Severity > result.Summary.OtherHighestSeverity {
			result.Summary.OtherHighestSeverity = finding.Severity
		}
	}

	// How many violations of each policy were found, before the threshold: a
	// policy at zero was checked and nothing was found against it.
	if len(policies) > 0 {
		result.Summary.ByPolicy = make(map[string]int, len(policies))
		for i, policy := range policies {
			result.Summary.ByPolicy[policy] = len(violations[i])
		}
	}

	log.Debug("Audit operation succeeded",
		"findings", result.Summary.TotalFindings,
		"critical", result.Summary.Critical,
		"passes", result.Summary.PassesAudit)
	return result, nil
}

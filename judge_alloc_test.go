package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// The judgments are held to the allocation rule of the family (C3), as the
// operations on lists are (collection_alloc_test.go): in the plain state a call
// allocates what the internal call allocates, and on the default client one more.
// The issues, claims and positives of a result are converted into the root's own
// types, one allocation for each list that is not empty, so each judgment is
// measured with nothing to convert, where the two are equal, and with a list,
// where the function allocates one more for it.

type allocSignup struct {
	Email string `json:"email"`
	Age   int    `json:"age"`
}

var allocSignupValue = allocSignup{Email: "not-an-email", Age: 17}

// allocVerifyText is the text of a Verify call. The internal call takes it as an
// any, which boxes a string that is not a constant, and so does the old root's
// call: it is a variable so that the internal call is measured as it is made.
var allocVerifyText = "the text"

const (
	allocValidateClean   = `{"errors":[],"warnings":[],"info":[],"confidence":0.95,"summary":"fine"}`
	allocValidateIssue   = `{"errors":[{"field":"email","message":"bad","suggestion":"fix","explanation":"x"}],"warnings":[],"info":[],"confidence":0.9,"summary":"one"}`
	allocVerifyNoClaims  = `{"summary":"s","overall_confidence":0.9,"trust_score":0.85,"logic_issues":[],"consistency_issues":[]}`
	allocVerifyOneClaim  = `{"summary":"s","overall_confidence":0.9,"trust_score":0.85,"claims":[{"claim":"c","verdict":"verified","confidence":0.9,"corrections":"","evidence":["e"],"reasoning":"r"}],"logic_issues":[],"consistency_issues":[]}`
	allocAuditClean      = `{"findings":[]}`
	allocAuditFinding    = `{"findings":[{"category":"security","severity":0.8,"field":"password","issue":"plain","evidence":"e","recommendation":"hash"}]}`
	allocCritiqueBare    = `{"overall_score":0.7,"criteria_scores":{"clarity":0.8},"issues":[],"summary":"s","top_priorities":[]}`
	allocCritiqueIssue   = `{"overall_score":0.7,"criteria_scores":{"clarity":0.8},"issues":[{"criterion":"clarity","severity":"major","description":"d","location":"l","suggestion":"s","fix":"f"}],"summary":"s","top_priorities":[]}`
	allocCritiquePosited = `{"overall_score":0.7,"criteria_scores":{"clarity":0.8},"issues":[],"positives":[{"criterion":"clarity","description":"nice","strength":"s"}],"summary":"s","top_priorities":[]}`
)

// requireOneMoreAllocation holds the function to the internal call plus one, on a
// client carried by the context.
func requireOneMoreAllocation(t *testing.T, reply, what string, call, internal func(ctx context.Context) error) {
	t.Helper()
	model := &allocProvider{reply: reply}
	ctx := WithClient(context.Background(), Client{Provider: model})
	if err := call(ctx); err != nil {
		t.Fatalf("the function failed on the scripted model: %v", err)
	}
	if err := internal(ctx); err != nil {
		t.Fatalf("the internal call failed on the scripted model: %v", err)
	}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _ = call(ctx) })
	direct := testing.AllocsPerRun(allocRuns, func() { _ = internal(ctx) })
	t.Logf("%s: the function %v allocations, the internal call %v", what, wrapped, direct)
	if wrapped != direct+1 {
		t.Errorf("the function allocates %v times and the internal call %v: the copy of %s is the one allocation more", wrapped, direct, what)
	}
}

func TestValidateAllocatesAsTheInternalCallWhenThereAreNoIssues(t *testing.T) {
	cases := map[string]ValidateOptions{
		"rules":                {Rules: "the email has to be real"},
		"every own setting":    {Rules: "x", FieldRules: map[string]string{"age": "plausible"}, SchemaHints: map[string]string{"age": "years"}, FailOn: "warning", OmitCorrection: true, OmitExplanations: true},
		"every common setting": {Rules: "x", Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocValidateClean,
				call: func(ctx context.Context) error {
					_, err := Validate(ctx, allocSignupValue, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.ValidateHybrid(allocSignupValue, validateInternal(ctx, o))
					return err
				},
			})
		})
	}
}

// The Deterministic internal call takes no context, so it never pins the client:
// the function pays the pin of the default client and the internal call does not.
// Under a client of the context the two are equal.
func TestValidateDeterministicAllocatesAsTheInternalCallWhenTheValuePasses(t *testing.T) {
	o := ValidateOptions{Deterministic: true, FieldRules: map[string]string{"email": "email"}}
	data := allocSignup{Email: "a@b.co", Age: 30}
	model := &allocProvider{reply: allocValidateClean}
	client := WithClient(context.Background(), Client{Provider: model})
	call := func(ctx context.Context) func() {
		return func() { _, _ = Validate(ctx, data, o) }
	}
	internal := func(ctx context.Context) func() {
		return func() { _, _ = ops.ValidateDeterministically(data, validateInternal(ctx, o)) }
	}
	call(client)()
	wrapped, direct := testing.AllocsPerRun(allocRuns, call(client)), testing.AllocsPerRun(allocRuns, internal(client))
	t.Logf("under WithClient: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct {
		t.Errorf("under a client the function allocates %v times and the internal call %v", wrapped, direct)
	}

	previous := Default()
	SetDefault(Client{Provider: model})
	t.Cleanup(func() { SetDefault(previous) })
	background := context.Background()
	wrappedDefault, directDefault := testing.AllocsPerRun(allocRuns, call(background)), testing.AllocsPerRun(allocRuns, internal(background))
	t.Logf("on the default client: the function %v allocations, the internal call %v", wrappedDefault, directDefault)
	if wrappedDefault != directDefault+1 {
		t.Errorf("on the default client the function allocates %v times, want the internal call's %v plus the pin", wrappedDefault, directDefault)
	}
}

func TestValidateWithIssuesAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	o := ValidateOptions{Rules: "the email has to be real"}
	requireOneMoreAllocation(t, allocValidateIssue, "the issues",
		func(ctx context.Context) error { _, err := Validate(ctx, allocSignupValue, o); return err },
		func(ctx context.Context) error {
			_, err := ops.ValidateHybrid(allocSignupValue, validateInternal(ctx, o))
			return err
		})
}

func TestVerifyAllocatesAsTheInternalCallWhenThereIsNothingToConvert(t *testing.T) {
	cases := map[string]VerifyOptions{
		"no options":           {SkipFacts: true},
		"every own setting":    {Sources: []any{"doc"}, TrustedSources: []string{"registry"}, Domain: "law", Strictness: "strict", MinConfidence: 0.5, SkipFacts: true, OmitEvidence: true, OmitReasoning: true},
		"every common setting": {SkipFacts: true, Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocVerifyNoClaims,
				call: func(ctx context.Context) error {
					_, err := Verify(ctx, allocVerifyText, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.VerifyWithModel(allocVerifyText, verifyInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestVerifyWithClaimsAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	o := VerifyOptions{SkipLogic: true, SkipConsistency: true}
	requireOneMoreAllocation(t, allocVerifyOneClaim, "the claims",
		func(ctx context.Context) error { _, err := Verify(ctx, allocVerifyText, o); return err },
		func(ctx context.Context) error {
			_, err := ops.VerifyWithModel(allocVerifyText, verifyInternal(ctx, o))
			return err
		})
}

func TestAuditAllocatesAsTheInternalCallWhenNothingIsFound(t *testing.T) {
	cases := map[string]AuditOptions{
		"no options":           {},
		"one options value":    {Categories: []string{"security"}, Threshold: 0.3, Shallow: true},
		"every common setting": {Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocAuditClean,
				call: func(ctx context.Context) error {
					_, err := Audit(ctx, allocSignupValue, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.AuditWithModel(allocSignupValue, auditInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestAuditWithAFindingAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	o := AuditOptions{Threshold: 0.1}
	requireOneMoreAllocation(t, allocAuditFinding, "the issues",
		func(ctx context.Context) error { _, err := Audit(ctx, allocSignupValue, o); return err },
		func(ctx context.Context) error {
			_, err := ops.AuditWithModel(allocSignupValue, auditInternal(ctx, o))
			return err
		})
}

func TestCritiqueAllocatesAsTheInternalCallWhenThereIsNothingToConvert(t *testing.T) {
	criteria := []string{"clarity"}
	cases := map[string]CritiqueOptions{
		"no options":           {},
		"one options value":    {Style: "harsh", SeverityFilter: "major", MaxIssues: 3, Domain: "law", Audience: "juniors", OmitSuggestions: true, OmitFixes: true, OmitPositives: true},
		"every common setting": {Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocCritiqueBare,
				call: func(ctx context.Context) error {
					_, err := Critique(ctx, "the essay", criteria, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.CritiqueWithModel("the essay", critiqueInternal(ctx, criteria, o))
					return err
				},
			})
		})
	}
}

func TestCritiqueWithIssuesAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	criteria := []string{"clarity"}
	requireOneMoreAllocation(t, allocCritiqueIssue, "the issues",
		func(ctx context.Context) error { _, err := Critique(ctx, "the essay", criteria); return err },
		func(ctx context.Context) error {
			_, err := ops.CritiqueWithModel("the essay", critiqueInternal(ctx, criteria, CritiqueOptions{}))
			return err
		})
}

func TestCritiqueWithPositivesAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	criteria := []string{"clarity"}
	requireOneMoreAllocation(t, allocCritiquePosited, "the positives",
		func(ctx context.Context) error { _, err := Critique(ctx, "the essay", criteria); return err },
		func(ctx context.Context) error {
			_, err := ops.CritiqueWithModel("the essay", critiqueInternal(ctx, criteria, CritiqueOptions{}))
			return err
		})
}

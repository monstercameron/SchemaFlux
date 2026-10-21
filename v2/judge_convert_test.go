package schemaflux

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The root declares its own result types over the internal ones, and each is
// converted field by field. A field added on one side and not the other fails
// here, and so does a json tag that drifts from the internal twin's (O7).

// requireSameFields fails unless the two struct types have the same fields, in the
// same order and with the same json tags, and, unless loose is set, of the same
// types. A result type whose list holds the root's own element type is compared
// loose: the element types are compared by their own test. internalOnly lists
// fields of the internal type that the root does not declare.
func requireSameFields(t *testing.T, root, internal reflect.Type, loose bool, internalOnly ...string) {
	t.Helper()
	skip := map[string]bool{}
	for _, name := range internalOnly {
		skip[name] = true
	}
	var want []reflect.StructField
	for i := 0; i < internal.NumField(); i++ {
		if !skip[internal.Field(i).Name] {
			want = append(want, internal.Field(i))
		}
	}
	if root.NumField() != len(want) {
		t.Fatalf("%s has %d fields and %s has %d that it declares", root, root.NumField(), internal, len(want))
	}
	for i := range want {
		got := root.Field(i)
		if got.Name != want[i].Name || got.Tag.Get("json") != want[i].Tag.Get("json") || (!loose && got.Type != want[i].Type) {
			t.Errorf("field %d: %s %s `%s`, internal %s %s `%s`", i, got.Name, got.Type, got.Tag, want[i].Name, want[i].Type, want[i].Tag)
		}
	}
}

func TestTheRootsJudgmentIssueHasTheFieldsOfTheInternalIssue(t *testing.T) {
	t.Parallel()
	requireSameFields(t, reflect.TypeFor[JudgmentIssue](), reflect.TypeFor[types.JudgmentIssue](), false)
}

func TestTheRootsClaimVerificationHasTheFieldsAndTagsOfTheInternalClaim(t *testing.T) {
	t.Parallel()
	requireSameFields(t, reflect.TypeFor[ClaimVerification](), reflect.TypeFor[ops.ClaimVerification](), false)
}

func TestTheRootsCritiquePositiveHasTheFieldsAndTagsOfTheInternalPositive(t *testing.T) {
	t.Parallel()
	requireSameFields(t, reflect.TypeFor[CritiquePositive](), reflect.TypeFor[ops.CritiquePositive](), false)
}

func TestTheRootsRankedItemHasTheFieldsAndTagsOfTheInternalItem(t *testing.T) {
	t.Parallel()
	requireSameFields(t, reflect.TypeFor[RankedItem[string]](), reflect.TypeFor[ops.RankedItem[string]](), false)
}

func TestTheRootsRankResultHasTheFieldsAndTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	requireSameFields(t, reflect.TypeFor[RankResult[string]](), reflect.TypeFor[ops.RankResult[string]](), true, "Metadata")
}

func TestTheRootsSemanticMatchResultHasTheFieldsAndTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	requireSameFields(t, reflect.TypeFor[SemanticMatchResult[string, string]](), reflect.TypeFor[ops.MatchResult[string, string]](), true, "Metadata")
}

func TestTheRootsMatchPairHasTheFieldsAndTagsOfTheInternalPair(t *testing.T) {
	t.Parallel()
	requireSameFields(t, reflect.TypeFor[MatchPair[string, string]](), reflect.TypeFor[ops.MatchPair[string, string]](), false)
}

func TestTheRootsJudgmentHasTheFieldsOfTheInternalJudgment(t *testing.T) {
	t.Parallel()
	// The internal judgment also carries the model's claims as a map, which the
	// root replaces by the named fields of each operation's result.
	var names []string
	for i := 0; i < reflect.TypeFor[Judgment[string]]().NumField(); i++ {
		names = append(names, reflect.TypeFor[Judgment[string]]().Field(i).Name)
	}
	want := []string{"Subject", "Verdict", "Issues", "Evidence", "Summary", "ModelConfidence"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("fields = %v, want %v", names, want)
	}
	internal := reflect.TypeFor[types.JudgmentResult[string]]()
	for _, name := range want {
		if _, ok := internal.FieldByName(name); !ok {
			t.Errorf("the internal judgment has no %s", name)
		}
	}
}

func TestTheRootsVerdictsAreTheInternalOnes(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		root     Verdict
		internal types.Verdict
	}{
		{VerdictUnknown, types.VerdictUnknown},
		{VerdictPass, types.VerdictPass},
		{VerdictFail, types.VerdictFail},
		{VerdictMixed, types.VerdictMixed},
	}
	for _, p := range pairs {
		if string(p.root) != string(p.internal) {
			t.Errorf("%q is not the internal %q", p.root, p.internal)
		}
	}
}

func TestJudgmentOfCopiesEverySharedFieldAndNothingBesides(t *testing.T) {
	t.Parallel()
	in := types.JudgmentResult[string]{
		Subject: "s", Verdict: types.VerdictMixed,
		Issues:   []types.JudgmentIssue{{Subject: "a", Category: "b", Severity: "error", Message: "m", Suggestion: "g", Rule: "r", Evidence: []string{"e"}, ModelConfidence: 0.4}},
		Evidence: []string{"x"}, Summary: "sum", ModelConfidence: 0.7,
		ModelClaims: map[string]any{"anything": 1},
	}
	want := Judgment[string]{
		Subject: "s", Verdict: VerdictMixed,
		Issues:   []JudgmentIssue{{Subject: "a", Category: "b", Severity: "error", Message: "m", Suggestion: "g", Rule: "r", Evidence: []string{"e"}, ModelConfidence: 0.4}},
		Evidence: []string{"x"}, Summary: "sum", ModelConfidence: 0.7,
	}
	if got := judgmentOf(in); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestEveryConversionOfAZeroResultIsTheZeroResult(t *testing.T) {
	t.Parallel()
	if got := judgmentOf(types.JudgmentResult[int]{}); !reflect.DeepEqual(got, Judgment[int]{}) {
		t.Errorf("judgment: %+v", got)
	}
	if got := validateResultOf(types.JudgmentResult[int]{}); !reflect.DeepEqual(got, ValidateResult[int]{}) {
		t.Errorf("validate: %+v", got)
	}
	if got := verifyResultOf("text", types.JudgmentResult[any]{}); !reflect.DeepEqual(got, VerifyResult{}) {
		t.Errorf("verify: %+v", got)
	}
	if got := auditResultOf(types.JudgmentResult[int]{}); !reflect.DeepEqual(got, AuditResult[int]{}) {
		t.Errorf("audit: %+v", got)
	}
	if got := critiqueResultOf(types.JudgmentResult[int]{}); !reflect.DeepEqual(got, CritiqueResult[int]{}) {
		t.Errorf("critique: %+v", got)
	}
	if got := rankResultOf(ops.RankResult[int]{}); !reflect.DeepEqual(got, RankResult[int]{}) {
		t.Errorf("rank: %+v", got)
	}
	if got := semanticMatchResultOf(ops.MatchResult[int, int]{}); !reflect.DeepEqual(got, SemanticMatchResult[int, int]{}) {
		t.Errorf("semantic match: %+v", got)
	}
	if issuesOf(nil) != nil || issuesOf([]types.JudgmentIssue{}) != nil {
		t.Error("no issues are not nil")
	}
}

// A bare assertion on a model claim panics on the commonest call: a validation
// that proposed no correction has no entry for it. Every read is the comma-ok
// form, so a claim that is missing or of another type leaves its field at zero.
func TestAClaimThatIsMissingOrOfAnotherTypeLeavesItsFieldAtZeroAndNeverPanics(t *testing.T) {
	t.Parallel()
	wrong := map[string]any{
		"corrected": "not a pointer", "deterministic_only": "yes", "claims": 5, "trust_score": "high",
		"by_severity": []int{1}, "by_category": 1, "by_policy": "x", "critical": 1, "findings_below_threshold": 1.5,
		"other_highest_severity": 1, "other_findings": "none", "criteria_scores": []string{"a"}, "positives": 7, "top_priorities": "first",
	}
	claimSets := map[string]map[string]any{"nil": nil, "empty": {}, "of the wrong types": wrong}
	for name, claims := range claimSets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			j := types.JudgmentResult[string]{Subject: "s", Verdict: types.VerdictPass, ModelClaims: claims}
			if got := validateResultOf(j); got.Corrected != nil || got.DeterministicOnly || got.Subject != "s" {
				t.Errorf("validate: %+v", got)
			}
			ja := types.JudgmentResult[any]{Subject: "s", Verdict: types.VerdictPass, ModelClaims: claims}
			if got := verifyResultOf("s", ja); got.Claims != nil || got.ModelTrustScore != 0 || got.Subject != "s" {
				t.Errorf("verify: %+v", got)
			}
			if got := auditResultOf(j); got.BySeverity != nil || got.ByCategory != nil || got.ByPolicy != nil || got.Critical || got.BelowThreshold != 0 || got.OtherHighestSeverity != 0 || got.Other != nil {
				t.Errorf("audit: %+v", got)
			}
			if got := critiqueResultOf(j); got.CriteriaScores != nil || got.Positives != nil || got.TopPriorities != nil {
				t.Errorf("critique: %+v", got)
			}
		})
	}
}

func TestAClaimOfTheRightTypeIsRead(t *testing.T) {
	t.Parallel()
	corrected := &struct{ A int }{A: 3}
	validated := validateResultOf(types.JudgmentResult[struct{ A int }]{Verdict: types.VerdictFail, ModelClaims: map[string]any{"corrected": corrected, "deterministic_only": true}})
	if validated.Corrected != corrected || !validated.DeterministicOnly {
		t.Errorf("validate: %+v", validated)
	}
	verified := verifyResultOf("s", types.JudgmentResult[any]{Verdict: types.VerdictPass, ModelClaims: map[string]any{
		"claims": []ops.ClaimVerification{{Claim: "c", Verdict: "verified", ModelConfidence: 0.9, Sources: []int{1}, Note: "n"}}, "trust_score": 0.4,
	}})
	if len(verified.Claims) != 1 || verified.Claims[0].Claim != "c" || verified.Claims[0].Sources[0] != 1 || verified.Claims[0].Note != "n" || verified.ModelTrustScore != 0.4 {
		t.Errorf("verify: %+v", verified)
	}
	audited := auditResultOf(types.JudgmentResult[string]{Verdict: types.VerdictFail, ModelClaims: map[string]any{
		"by_severity": map[string]int{"high": 1}, "by_category": map[string]int{"security": 1}, "by_policy": map[string]int{"p": 2}, "critical": true,
		"findings_below_threshold": 3, "other_highest_severity": 0.95, "other_findings": []types.JudgmentIssue{{Subject: "o"}},
	}})
	if audited.BySeverity["high"] != 1 || audited.ByCategory["security"] != 1 || audited.ByPolicy["p"] != 2 || !audited.Critical || audited.BelowThreshold != 3 || audited.OtherHighestSeverity != 0.95 || len(audited.Other) != 1 || audited.Other[0].Subject != "o" {
		t.Errorf("audit: %+v", audited)
	}
	critiqued := critiqueResultOf(types.JudgmentResult[string]{Verdict: types.VerdictMixed, ModelClaims: map[string]any{
		"criteria_scores": map[string]float64{"c": 0.5}, "positives": []ops.CritiquePositive{{Criterion: "c", Description: "d", Strength: "s"}}, "top_priorities": []string{"p"},
	}})
	if critiqued.CriteriaScores["c"] != 0.5 || len(critiqued.Positives) != 1 || critiqued.Positives[0].Strength != "s" || critiqued.TopPriorities[0] != "p" {
		t.Errorf("critique: %+v", critiqued)
	}
}

func TestVerifyResultOfTakesTheSubjectFromTheTextAndOnlyForAJudgment(t *testing.T) {
	t.Parallel()
	if got := verifyResultOf("the text", types.JudgmentResult[any]{Verdict: types.VerdictUnknown}); got.Subject != "the text" || got.Verdict != VerdictUnknown {
		t.Errorf("a verdict of unknown is a judgment: %+v", got)
	}
	if got := verifyResultOf("the text", types.JudgmentResult[any]{}); got.Subject != "" {
		t.Errorf("no verdict is no judgment: %+v", got)
	}
}

func TestCheckWordOfAcceptsEmptyAndTheWordsAllowedAndNamesTheFieldOtherwise(t *testing.T) {
	t.Parallel()
	words := []string{"one", "two", "three"}
	accepted := []string{"", "one", "two", "three"}
	for _, value := range accepted {
		if err := checkWordOf("op", "Field", value, words...); err != nil {
			t.Errorf("%q refused: %v", value, err)
		}
	}
	refused := []string{"four", "One", " one", "one ", "o", "onetwo", "SECRET-VALUE"}
	for _, value := range refused {
		err := checkWordOf("op", "Field", value, words...)
		if err == nil {
			t.Errorf("%q accepted", value)
			continue
		}
		if want := "Field is not one of one, two and three"; !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %q", value, err)
		}
	}
	if err := checkWordOf("op", "Field", "x", "only"); err == nil || !strings.Contains(err.Error(), "Field is not one of only") {
		t.Errorf("one word: %v", err)
	}
}

func TestWordListWritesWordsAsASentenceDoes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		words []string
		want  string
	}{
		{nil, ""}, {[]string{"a"}, "a"}, {[]string{"a", "b"}, "a and b"}, {[]string{"a", "b", "c"}, "a, b and c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c and d"},
	}
	for _, tc := range cases {
		if got := wordList(tc.words); got != tc.want {
			t.Errorf("wordList(%v) = %q, want %q", tc.words, got, tc.want)
		}
	}
}

func TestLayFloorAndLayLimitApplyTheMeaningOfZeroAndOfANegative(t *testing.T) {
	t.Parallel()
	floors := []struct {
		name         string
		start, value float64
		want         float64
	}{
		{"zero leaves the default", 0.7, 0, 0.7},
		{"a positive replaces it", 0.7, 0.9, 0.9},
		{"a small positive replaces it", 0.7, math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64},
		{"one replaces it", 0.7, 1, 1},
		{"a negative becomes zero", 0.7, -0.1, 0},
		{"a large negative becomes zero", 0.7, -100, 0},
		{"negative infinity becomes zero", 0.7, math.Inf(-1), 0},
		{"zero leaves a zero default", 0, 0, 0},
		{"a negative zero leaves the default", 0.5, math.Copysign(0, -1), 0.5},
		{"a positive over a zero default", 0, 0.3, 0.3},
	}
	for _, tc := range floors {
		got := tc.start
		layFloor(&got, tc.value)
		if got != tc.want {
			t.Errorf("layFloor %s: %v, want %v", tc.name, got, tc.want)
		}
	}
	limits := []struct {
		name         string
		start, value int
		want         int
	}{
		{"zero leaves the default", 1, 0, 1},
		{"a positive replaces it", 1, 3, 3},
		{"one replaces it", 5, 1, 1},
		{"a negative becomes zero, no limit", 1, -1, 0},
		{"a large negative becomes zero", 1, -1 << 30, 0},
		{"zero leaves a zero default", 0, 0, 0},
		{"a positive over a zero default", 0, 4, 4},
		{"the largest replaces it", 1, math.MaxInt32, math.MaxInt32},
		{"a negative over a zero default", 0, -2, 0},
		{"zero leaves a larger default", 9, 0, 9},
	}
	for _, tc := range limits {
		got := tc.start
		layLimit(&got, tc.value)
		if got != tc.want {
			t.Errorf("layLimit %s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

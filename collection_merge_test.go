package schemaflux

import "testing"

// Each options type has a merge function (M0), and a field added to the type and
// left out of it fails here: requireMergeCopiesEveryField fills one field at a
// time by reflection and proves the merge carries it. The families of the other
// files have their own lines in call_merge_test.go.

func TestMergeChooseOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeChooseOptions)
}

func TestMergeFilterOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeFilterOptions)
}

func TestMergeSortOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeSortOptions)
}

func TestMergeRankOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeRankOptions)
}

func TestMergeSemanticMatchOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeSemanticMatchOptions)
}

func TestMergeValidateOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeValidateOptions)
}

func TestMergeVerifyOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeVerifyOptions)
}

func TestMergeAuditOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeAuditOptions)
}

func TestMergeCritiqueOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeCritiqueOptions)
}

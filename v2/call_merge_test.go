package schemaflux

import "testing"

// Each options type has a merge function (M0), and a field added to the type and
// left out of it fails here: requireMergeCopiesEveryField fills one field at a
// time by reflection and proves the merge carries it.

func TestMergeExtractOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeExtractOptions)
}

func TestMergeClassifyOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeClassifyOptions)
}

func TestMergeSummarizeOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeSummarizeOptions)
}

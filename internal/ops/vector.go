package ops

import (
	"fmt"
	"math"
	"sort"
)

// PS-006, the deterministic half. Similar, CheckingSimilarity, Clustering,
// Deduplicate, and Matching all have exact vector maths behind them, and
// none of it needs a provider -- it needs a vector, which internal/llm's
// EmbeddingProvider capability now supplies. Every function here is pure Go
// over []float64, tested with hand-computed values and zero provider calls.
//
// This file does not rewire Similar, Cluster, Deduplicate, or Match onto
// embeddings. Those operations are owned elsewhere and changing their
// behavior is a separate change from building the maths they would use --
// see this task's report for what that rewiring would take.
//
// One naming rule threaded through this file: a cosine similarity is a
// MEASUREMENT of two vectors, not a claim about meaning and not a
// probability that two things mean the same thing. It is never named or
// treated like the Model* fields elsewhere in this library (a model's own
// self-reported confidence, which is a claim) -- see VectorSimilarity's doc
// comment.

// VectorSimilarity is a cosine similarity between two vectors: a measurement
// of the vectors themselves, in [-1, 1], not a model's claim about the
// underlying text and not a probability. Keeping it out of anything that
// looks like a Model* field is deliberate -- AGENTS.md's "never fail open"
// rule draws exactly this line: "a model's self-reported score is a claim,
// not a measurement." This is the other side of that line. A caller who
// wants to reason about meaning-equivalence still has to decide what
// similarity threshold means that for their data; this type only reports
// what was measured.
type VectorSimilarity float64

// CosineSimilarity measures the cosine of the angle between a and b.
//
// It returns an error rather than a number for the two cases with no
// well-defined answer: mismatched dimensions (comparing vectors from two
// different embedding models, or a caller's bug) and a zero vector (which
// has no direction, so "the angle between it and anything" is undefined,
// not zero).
func CosineSimilarity(a, b []float64) (VectorSimilarity, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vectors have different dimensions: %d and %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("vectors are empty")
	}

	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0, fmt.Errorf("a zero vector has no defined direction, so its cosine similarity to anything is undefined")
	}

	return VectorSimilarity(dot / (math.Sqrt(normA) * math.Sqrt(normB))), nil
}

// VectorMatch is one candidate's measured similarity to a query vector, at
// a given position in the caller's candidate slice.
type VectorMatch struct {
	// Index is the candidate's position in the slice TopKSimilar or
	// CheckingVectorSimilarity was given -- a location, not a value, so a
	// caller can look the original item up without this package echoing the
	// caller's data back inside the match.
	Index int

	// Similarity is the measured cosine similarity between the query and
	// this candidate. See VectorSimilarity's doc comment for what kind of
	// number this is (and is not).
	Similarity VectorSimilarity
}

// TopKSimilar measures query against every candidate and returns the k
// highest-scoring matches, ordered from most to least similar.
//
// Ties are broken by candidate index (ascending), which is what
// sort.SliceStable over an index-ordered input slice already gives for
// free -- deterministic output for the same input, not an artifact of
// whatever order a hash map happened to produce.
func TopKSimilar(query []float64, candidates [][]float64, k int) ([]VectorMatch, error) {
	if k <= 0 {
		return nil, fmt.Errorf("top-k requires k > 0, got %d", k)
	}

	matches := make([]VectorMatch, 0, len(candidates))
	for i, candidate := range candidates {
		similarity, err := CosineSimilarity(query, candidate)
		if err != nil {
			return nil, fmt.Errorf("candidate %d: %w", i, err)
		}
		matches = append(matches, VectorMatch{Index: i, Similarity: similarity})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Similarity > matches[j].Similarity
	})

	if k > len(matches) {
		k = len(matches)
	}
	return matches[:k], nil
}

// ClusterByThreshold partitions vectors into groups by single-link
// agglomeration: two vectors land in the same group when their cosine
// similarity is at least threshold, transitively (a-b linked and b-c linked
// puts a, b, and c in one group even if a and c fall below threshold
// directly).
//
// The result always satisfies CoversExactlyOnce(len(vectors), result) --
// every index in exactly one group -- because union-find over the whole set
// cannot produce anything else. That check runs before returning anyway,
// reusing internal/ops/invariants.go's shared partition check rather than a
// fourth hand-rolled version of it, so a future change to this function's
// grouping logic that broke the partition property would fail loudly here
// instead of downstream.
//
// Groups are returned in ascending order of their smallest member index,
// and each group's members are ascending too -- deterministic output for
// identical input, independent of Go's random map iteration order.
func ClusterByThreshold(vectors [][]float64, threshold float64) ([][]int, error) {
	n := len(vectors)
	if n == 0 {
		return nil, nil
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			similarity, err := CosineSimilarity(vectors[i], vectors[j])
			if err != nil {
				return nil, fmt.Errorf("comparing vector %d and %d: %w", i, j, err)
			}
			if float64(similarity) >= threshold {
				union(i, j)
			}
		}
	}

	byRoot := make(map[int][]int, n)
	for i := 0; i < n; i++ {
		root := find(i)
		byRoot[root] = append(byRoot[root], i)
	}

	roots := make([]int, 0, len(byRoot))
	for root := range byRoot {
		roots = append(roots, root)
	}
	sort.Ints(roots)

	groups := make([][]int, 0, len(roots))
	for _, root := range roots {
		members := byRoot[root]
		sort.Ints(members)
		groups = append(groups, members)
	}

	if err := CoversExactlyOnce(n, groups); err != nil {
		// Unreachable by construction (union-find over every index always
		// partitions), but checked rather than assumed -- see doc comment.
		return nil, fmt.Errorf("internal error: ClusterByThreshold produced a non-partition: %w", err)
	}

	return groups, nil
}

// DeduplicateVectors groups near-duplicate vectors (cosine similarity at
// least threshold, via ClusterByThreshold) and returns one representative
// index per group -- the group's lowest index, so the result is
// deterministic and independent of vector content -- alongside the full
// grouping.
//
// It does not decide what "duplicate" means for the caller's data beyond
// the threshold given; a caller wanting a different representative (most
// recent, longest text, highest confidence) uses Groups directly rather
// than Keep.
func DeduplicateVectors(vectors [][]float64, threshold float64) (keep []int, groups [][]int, err error) {
	groups, err = ClusterByThreshold(vectors, threshold)
	if err != nil {
		return nil, nil, err
	}
	keep = make([]int, 0, len(groups))
	for _, group := range groups {
		// group is ascending (ClusterByThreshold sorts members), so group[0]
		// is its lowest index.
		keep = append(keep, group[0])
	}
	return keep, groups, nil
}

// VectorMatchPair is one measured correspondence between an index into a
// slice of left-hand vectors and an index into a slice of right-hand
// vectors -- what a matching operation like PS-006's target Matching would
// need instead of an LLM round trip.
//
// Named VectorMatchPair rather than MatchPair because match.go (the
// existing Matching operation) already declares a generic MatchPair[S, T]
// for its own, unrelated purpose -- this is vector-only and has nothing to
// do with that type.
type VectorMatchPair struct {
	LeftIndex  int
	RightIndex int
	Similarity VectorSimilarity
}

// BestMatches greedily pairs each left vector with its most similar
// unclaimed right vector, highest similarity first, until every left
// vector has a match or the right side runs out.
//
// This is a greedy approximation, not an optimal bipartite matching
// (Hungarian algorithm territory) -- named as such in this doc comment
// because "matching" undersells it if a caller assumes it is optimal.
// Greedy is what a deduplicate-style caller needs: the single best
// available right-hand vector per left-hand one, deterministic for a fixed
// input via the ties-broken-by-index rule TopKSimilar already documents.
func BestMatches(left, right [][]float64) ([]VectorMatchPair, error) {
	if len(left) == 0 || len(right) == 0 {
		return nil, nil
	}

	type candidate struct {
		l, r       int
		similarity VectorSimilarity
	}
	var candidates []candidate
	for i, lv := range left {
		for j, rv := range right {
			similarity, err := CosineSimilarity(lv, rv)
			if err != nil {
				return nil, fmt.Errorf("left %d, right %d: %w", i, j, err)
			}
			candidates = append(candidates, candidate{l: i, r: j, similarity: similarity})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].similarity != candidates[j].similarity {
			return candidates[i].similarity > candidates[j].similarity
		}
		if candidates[i].l != candidates[j].l {
			return candidates[i].l < candidates[j].l
		}
		return candidates[i].r < candidates[j].r
	})

	leftTaken := make([]bool, len(left))
	rightTaken := make([]bool, len(right))
	var pairs []VectorMatchPair
	for _, c := range candidates {
		if leftTaken[c.l] || rightTaken[c.r] {
			continue
		}
		leftTaken[c.l] = true
		rightTaken[c.r] = true
		pairs = append(pairs, VectorMatchPair{LeftIndex: c.l, RightIndex: c.r, Similarity: c.similarity})
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].LeftIndex < pairs[j].LeftIndex })
	return pairs, nil
}

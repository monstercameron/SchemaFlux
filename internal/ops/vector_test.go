package ops

import (
	"math"
	"testing"
)

const floatTolerance = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < floatTolerance
}

// --- CosineSimilarity: hand-computed values, zero provider calls. ---

func TestCosineSimilarityIdenticalDirectionIsOne(t *testing.T) {
	got, err := CosineSimilarity([]float64{1, 0}, []float64{1, 0})
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(got), 1) {
		t.Errorf("got %v, want 1", got)
	}
}

func TestCosineSimilarityOrthogonalIsZero(t *testing.T) {
	got, err := CosineSimilarity([]float64{1, 0}, []float64{0, 1})
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(got), 0) {
		t.Errorf("got %v, want 0", got)
	}
}

func TestCosineSimilarityOppositeIsNegativeOne(t *testing.T) {
	got, err := CosineSimilarity([]float64{1, 0}, []float64{-1, 0})
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(got), -1) {
		t.Errorf("got %v, want -1", got)
	}
}

// TestCosineSimilarityScaleInvariant hand-computes dot=24, |a|=5, |b|=5,
// so cosine = 24/25 = 0.96 exactly.
func TestCosineSimilarityHandComputed(t *testing.T) {
	got, err := CosineSimilarity([]float64{3, 4}, []float64{4, 3})
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(got), 0.96) {
		t.Errorf("got %v, want 0.96", got)
	}
}

// TestCosineSimilarityIsScaleInvariant: [2,0] and [5,0] point the same
// direction as [1,0] and [1,0], so cosine must still be 1 -- magnitude does
// not enter a cosine similarity.
func TestCosineSimilarityIsScaleInvariant(t *testing.T) {
	got, err := CosineSimilarity([]float64{2, 0}, []float64{5, 0})
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(got), 1) {
		t.Errorf("got %v, want 1", got)
	}
}

func TestCosineSimilarityMismatchedDimensionsErrors(t *testing.T) {
	_, err := CosineSimilarity([]float64{1, 0}, []float64{1, 0, 0})
	if err == nil {
		t.Fatal("expected an error for mismatched dimensions, got nil")
	}
}

func TestCosineSimilarityEmptyVectorsErrors(t *testing.T) {
	_, err := CosineSimilarity(nil, nil)
	if err == nil {
		t.Fatal("expected an error for empty vectors, got nil")
	}
}

func TestCosineSimilarityZeroVectorErrors(t *testing.T) {
	_, err := CosineSimilarity([]float64{0, 0}, []float64{1, 1})
	if err == nil {
		t.Fatal("expected an error for a zero vector (undefined direction), got nil")
	}
}

// --- TopKSimilar ---

// TestTopKSimilarHandComputed: query [1,0] against four candidates whose
// cosine similarities are, by hand: c0=1, c1=1/sqrt(2)≈0.70710678,
// c2=0, c3=-1. Top 2 must be c0 then c1, by index [0, 1].
func TestTopKSimilarHandComputed(t *testing.T) {
	query := []float64{1, 0}
	candidates := [][]float64{
		{1, 0},     // c0: sim 1
		{0.5, 0.5}, // c1: sim 1/sqrt(2)
		{0, 1},     // c2: sim 0
		{-1, 0},    // c3: sim -1
	}
	got, err := TopKSimilar(query, candidates, 2)
	if err != nil {
		t.Fatalf("TopKSimilar: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(got))
	}
	if got[0].Index != 0 || !approxEqual(float64(got[0].Similarity), 1) {
		t.Errorf("got[0] = %+v, want index 0 sim 1", got[0])
	}
	if got[1].Index != 1 || !approxEqual(float64(got[1].Similarity), 1/math.Sqrt2) {
		t.Errorf("got[1] = %+v, want index 1 sim %v", got[1], 1/math.Sqrt2)
	}
}

func TestTopKSimilarKGreaterThanCandidatesReturnsAll(t *testing.T) {
	query := []float64{1, 0}
	candidates := [][]float64{{1, 0}, {0, 1}}
	got, err := TopKSimilar(query, candidates, 10)
	if err != nil {
		t.Fatalf("TopKSimilar: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 matches (all candidates), got %d", len(got))
	}
}

func TestTopKSimilarZeroKErrors(t *testing.T) {
	_, err := TopKSimilar([]float64{1, 0}, [][]float64{{1, 0}}, 0)
	if err == nil {
		t.Fatal("expected an error for k=0, got nil")
	}
}

// --- ClusterByThreshold: must be a genuine partition. ---

// TestClusterByThresholdHandComputed: two pairs of same-direction vectors
// (v0,v1 along x; v2,v3 along y, scaled differently so it is not a
// coincidence of magnitude) at threshold 0.5 must produce exactly two
// groups: {0,1} and {2,3}. Cross-group cosine is exactly 0 (orthogonal),
// well below the threshold.
func TestClusterByThresholdHandComputed(t *testing.T) {
	vectors := [][]float64{
		{1, 0}, // 0
		{2, 0}, // 1: same direction as 0
		{0, 1}, // 2
		{0, 3}, // 3: same direction as 2
	}
	groups, err := ClusterByThreshold(vectors, 0.5)
	if err != nil {
		t.Fatalf("ClusterByThreshold: %v", err)
	}
	want := [][]int{{0, 1}, {2, 3}}
	if len(groups) != len(want) {
		t.Fatalf("got %d groups, want %d: %v", len(groups), len(want), groups)
	}
	for i := range want {
		if len(groups[i]) != len(want[i]) {
			t.Fatalf("group %d = %v, want %v", i, groups[i], want[i])
		}
		for j := range want[i] {
			if groups[i][j] != want[i][j] {
				t.Fatalf("group %d = %v, want %v", i, groups[i], want[i])
			}
		}
	}
}

// TestClusterByThresholdIsAPartition reuses CoversExactlyOnce -- the same
// check the collection operations already use -- to prove ClusterByThreshold
// never drops, duplicates, or double-counts an index, across several
// thresholds including the two extremes.
func TestClusterByThresholdIsAPartition(t *testing.T) {
	vectors := [][]float64{
		{1, 0}, {0.9, 0.1}, {0, 1}, {-1, 0}, {5, 5}, {1, 1}, {0.1, 0.9},
	}
	for _, threshold := range []float64{-1, 0, 0.3, 0.5, 0.7, 0.9, 0.999, 1, 2} {
		groups, err := ClusterByThreshold(vectors, threshold)
		if err != nil {
			t.Fatalf("threshold %v: ClusterByThreshold: %v", threshold, err)
		}
		if err := CoversExactlyOnce(len(vectors), groups); err != nil {
			t.Errorf("threshold %v: not a partition: %v", threshold, err)
		}
	}
}

// TestClusterByThresholdEverythingSeparateAtHighThreshold: a threshold above
// every pairwise similarity puts each vector in its own singleton group.
func TestClusterByThresholdEverythingSeparateAtHighThreshold(t *testing.T) {
	vectors := [][]float64{{1, 0}, {0, 1}, {-1, 0}}
	groups, err := ClusterByThreshold(vectors, 1.5) // above the maximum possible cosine
	if err != nil {
		t.Fatalf("ClusterByThreshold: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 singleton groups, got %d: %v", len(groups), groups)
	}
	for _, g := range groups {
		if len(g) != 1 {
			t.Errorf("expected a singleton group, got %v", g)
		}
	}
}

// TestClusterByThresholdEverythingTogetherAtLowThreshold: a threshold below
// every pairwise similarity puts everything in one group.
func TestClusterByThresholdEverythingTogetherAtLowThreshold(t *testing.T) {
	vectors := [][]float64{{1, 0}, {0, 1}, {-1, 0}}
	groups, err := ClusterByThreshold(vectors, -2) // below the minimum possible cosine
	if err != nil {
		t.Fatalf("ClusterByThreshold: %v", err)
	}
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("expected one group of 3, got %v", groups)
	}
}

func TestClusterByThresholdEmptyInput(t *testing.T) {
	groups, err := ClusterByThreshold(nil, 0.5)
	if err != nil {
		t.Fatalf("ClusterByThreshold: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("expected no groups for empty input, got %v", groups)
	}
}

// --- DeduplicateVectors ---

func TestDeduplicateVectorsHandComputed(t *testing.T) {
	vectors := [][]float64{
		{1, 0}, // 0
		{2, 0}, // 1: duplicate of 0
		{0, 1}, // 2
		{0, 3}, // 3: duplicate of 2
	}
	keep, groups, err := DeduplicateVectors(vectors, 0.5)
	if err != nil {
		t.Fatalf("DeduplicateVectors: %v", err)
	}
	if len(keep) != 2 || keep[0] != 0 || keep[1] != 2 {
		t.Errorf("keep = %v, want [0 2]", keep)
	}
	if err := CoversExactlyOnce(len(vectors), groups); err != nil {
		t.Errorf("groups do not partition the input: %v", err)
	}
}

func TestDeduplicateVectorsNoDuplicatesKeepsEverything(t *testing.T) {
	vectors := [][]float64{{1, 0}, {0, 1}, {-1, 0}}
	keep, _, err := DeduplicateVectors(vectors, 0.99)
	if err != nil {
		t.Fatalf("DeduplicateVectors: %v", err)
	}
	if len(keep) != 3 {
		t.Errorf("expected all 3 kept (nothing duplicate at this threshold), got %v", keep)
	}
}

// --- BestMatches ---

// TestBestMatchesHandComputed: left0=[1,0], left1=[0,1]; right0=[0,1],
// right1=[1,0]. sim(l0,r0)=0, sim(l0,r1)=1, sim(l1,r0)=1, sim(l1,r1)=0.
// The two similarity-1 pairs do not conflict (different indices on both
// sides), so both are taken: left0->right1, left1->right0.
func TestBestMatchesHandComputed(t *testing.T) {
	left := [][]float64{{1, 0}, {0, 1}}
	right := [][]float64{{0, 1}, {1, 0}}
	pairs, err := BestMatches(left, right)
	if err != nil {
		t.Fatalf("BestMatches: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs, got %d: %v", len(pairs), pairs)
	}
	if pairs[0].LeftIndex != 0 || pairs[0].RightIndex != 1 || !approxEqual(float64(pairs[0].Similarity), 1) {
		t.Errorf("pairs[0] = %+v, want {Left:0 Right:1 Sim:1}", pairs[0])
	}
	if pairs[1].LeftIndex != 1 || pairs[1].RightIndex != 0 || !approxEqual(float64(pairs[1].Similarity), 1) {
		t.Errorf("pairs[1] = %+v, want {Left:1 Right:0 Sim:1}", pairs[1])
	}
}

// TestBestMatchesContentionPicksHigherSimilarity: two left vectors both
// prefer the same right vector; the better match wins it and the loser
// gets its next-best option.
func TestBestMatchesContentionPicksHigherSimilarity(t *testing.T) {
	left := [][]float64{
		{1, 0},   // l0: exact match to r0
		{0.9, 1}, // l1: close to r0 but not exact, and has r1 as a fallback
	}
	right := [][]float64{
		{1, 0}, // r0
		{0, 1}, // r1
	}
	pairs, err := BestMatches(left, right)
	if err != nil {
		t.Fatalf("BestMatches: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs, got %d: %v", len(pairs), pairs)
	}
	// l0 has a perfect match to r0 (similarity 1), which beats any
	// similarity l1 could have to r0, so l0 must win r0.
	got := map[int]int{}
	for _, p := range pairs {
		got[p.LeftIndex] = p.RightIndex
	}
	if got[0] != 0 {
		t.Errorf("left 0 matched to right %d, want right 0 (its exact match)", got[0])
	}
	if got[1] != 1 {
		t.Errorf("left 1 matched to right %d, want right 1 (its only remaining option)", got[1])
	}
}

func TestBestMatchesEmptySides(t *testing.T) {
	pairs, err := BestMatches(nil, [][]float64{{1, 0}})
	if err != nil {
		t.Fatalf("BestMatches: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("expected no pairs with an empty left side, got %v", pairs)
	}
}

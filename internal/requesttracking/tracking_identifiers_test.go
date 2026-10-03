package requesttracking

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var automaticIdentifier = regexp.MustCompile(`^req_[0-9a-f]{16}$`)

func TestAutomaticIdentifiersHaveTheDocumentedShape(t *testing.T) {
	for i := 0; i < 20; i++ {
		if id := generateIdentifier("req"); !automaticIdentifier.MatchString(id) {
			t.Fatalf("identifier %d = %q, want req_ and sixteen hex digits", i, id)
		}
	}
	if id := generateIdentifier("corr"); !regexp.MustCompile(`^corr_[0-9a-f]{16}$`).MatchString(id) {
		t.Errorf("a correlation identifier = %q, want corr_ and sixteen hex digits", id)
	}
}

func TestSuccessiveAutomaticIdentifiersDiffer(t *testing.T) {
	previous := generateIdentifier("req")
	for i := 0; i < 1000; i++ {
		next := generateIdentifier("req")
		if next == previous {
			t.Fatalf("identifier %d repeated its predecessor %q", i, next)
		}
		previous = next
	}
}

func TestAutomaticIdentifiersAreUniqueAcrossGoroutines(t *testing.T) {
	const goroutines, each = 50, 2000
	results := make([][]string, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := make([]string, each)
			for i := range ids {
				ids[i] = generateIdentifier("req")
			}
			results[g] = ids
		}()
	}
	wg.Wait()

	seen := make(map[string]struct{}, goroutines*each)
	for _, ids := range results {
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				t.Fatalf("identifier %q was made twice", id)
			}
			seen[id] = struct{}{}
		}
	}
}

// The identifier is the process's starting point plus the counter times an odd
// stride, which is what makes two identifiers of one process differ whatever the
// starting point is: an odd number has an inverse modulo 2^64, so two different
// counters cannot meet.
func TestTheStrideIsOddAndSoNeverRepeatsWithinAProcess(t *testing.T) {
	if identifierStride%2 != 1 {
		t.Fatalf("the stride %#x is even, so two counters could give one identifier", identifierStride)
	}
	seen := map[uint64]uint64{}
	const base = 0xfedcba9876543210
	for counter := uint64(1); counter <= 100000; counter++ {
		value := base + counter*identifierStride
		if other, dup := seen[value]; dup {
			t.Fatalf("counters %d and %d gave the same identifier", other, counter)
		}
		seen[value] = counter
	}
}

func TestIdentifiersDoNotLookConsecutive(t *testing.T) {
	first := generateIdentifier("req")
	second := generateIdentifier("req")
	a, _ := strconv.ParseUint(strings.TrimPrefix(first, "req_"), 16, 64)
	b, _ := strconv.ParseUint(strings.TrimPrefix(second, "req_"), 16, 64)
	if b-a == 1 || a-b == 1 {
		t.Errorf("%q and %q are consecutive numbers", first, second)
	}
}

func TestTheTimestampStrategyStillHasItsFormatAndStaysUnique(t *testing.T) {
	pattern := regexp.MustCompile(`^req_[0-9]+_[0-9]+_[0-9]+$`)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := generateTimestampID("req")
		if !pattern.MatchString(id) {
			t.Fatalf("timestamp identifier %q does not have the form req_<nanoseconds>_<pid>_<counter>", id)
		}
		if seen[id] {
			t.Fatalf("timestamp identifier %q was made twice", id)
		}
		seen[id] = true
	}
}

func TestTheUUIDStrategyStillMakesVersionFourUUIDs(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := generateUUID()
		if !pattern.MatchString(id) {
			t.Fatalf("UUID %q is not a version 4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("UUID %q was made twice", id)
		}
		seen[id] = true
	}
}

func TestEnsureReturnsTheContextItWasGivenWhenItAlreadyCarriesTheIDs(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})
	ctx, first := Ensure(context.Background(), "", "")
	if first.RequestID == "" || first.CorrelationID == "" {
		t.Fatalf("Ensure made no IDs: %+v", first)
	}

	again, second := Ensure(ctx, first.RequestID, first.CorrelationID)
	if again != ctx {
		t.Error("Ensure wrapped a context that already carried the IDs")
	}
	if second != first {
		t.Errorf("Ensure changed the IDs: %+v then %+v", first, second)
	}

	same, third := Ensure(ctx, "", "")
	if same != ctx || third != first {
		t.Error("Ensure with no explicit IDs did not keep the ones the context carries")
	}
}

func TestEnsureStillWrapsWhenTheIDsDiffer(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})
	ctx, _ := Ensure(context.Background(), "req-a", "corr-a")

	next, metadata := Ensure(ctx, "req-b", "")
	if next == ctx {
		t.Fatal("Ensure kept a context that carries a different request ID")
	}
	if metadata.RequestID != "req-b" || metadata.CorrelationID != "corr-a" {
		t.Errorf("metadata = %+v, want req-b with the inherited corr-a", metadata)
	}
	if got := FromContext(next); got != metadata {
		t.Errorf("the context carries %+v, want %+v", got, metadata)
	}
	if got := FromContext(ctx); got.RequestID != "req-a" {
		t.Errorf("the original context was changed: %+v", got)
	}
}

func TestEnsureWithTrackingOffLeavesTheContextAlone(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: false})
	ctx := context.Background()
	got, metadata := Ensure(ctx, "", "")
	if got != ctx || metadata != (Metadata{}) {
		t.Errorf("Ensure with tracking off returned %+v and a changed context: %v", metadata, got != ctx)
	}
}

func TestPaddedIDsOnTheContextAreTrimmedAndRewrapped(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})
	ctx := context.WithValue(context.Background(), requestIDKey, " req-padded ")
	ctx = context.WithValue(ctx, correlationIDKey, "corr-ok")
	next, metadata := Ensure(ctx, "", "")
	if metadata.RequestID != "req-padded" {
		t.Errorf("RequestID = %q, want it trimmed", metadata.RequestID)
	}
	if next == ctx {
		t.Error("the padded value was kept on the context instead of the trimmed one")
	}
	if got := FromContext(next).RequestID; got != "req-padded" {
		t.Errorf("the context carries %q", got)
	}
}

func TestResolveDoesNotNeedTheConfigurationWhenBothIDsAreGiven(t *testing.T) {
	resetConfig(t)
	// A configuration that would make an ID if asked.
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyNone, CorrelationIDStrategy: CorrelationStrategyNone})
	got := Resolve(context.Background(), "r", "c")
	if got.RequestID != "r" || got.CorrelationID != "c" {
		t.Errorf("Resolve = %+v, want the IDs it was given", got)
	}
}

// What an Ensure costs, so that a change that makes it dearer fails here and
// not in a benchmark nobody ran. The figures are what it cost when measured: an
// ID made for a call that had none (the string, and the two context values that
// carry the pair, each a boxed string and a context node), and nothing at all
// for a call that already has them.
func TestEnsureAllocationsStayWithinBudget(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})
	carrying, _ := Ensure(context.Background(), "req-a", "corr-a")

	cases := []struct {
		name   string
		ctx    context.Context
		budget float64
	}{
		{"no IDs on the context", context.Background(), 5},
		{"both IDs on the context", carrying, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testing.AllocsPerRun(200, func() { _, _ = Ensure(tc.ctx, "", "") })
			if got > tc.budget {
				t.Errorf("Ensure allocates %v times, budget %v", got, tc.budget)
			}
		})
	}
	t.Run("an automatic identifier", func(t *testing.T) {
		if got := testing.AllocsPerRun(200, func() { _ = generateIdentifier("req") }); got > 1 {
			t.Errorf("generateIdentifier allocates %v times, budget 1 (the string)", got)
		}
	})
}

// Two processes that start from one point make the same identifiers in the same
// order, whatever the arithmetic after it: the starting point is the whole of
// what tells their sequences apart.
func TestTheStartingPointOfAProcessIsNeverZeroAndDiffersFromCallToCall(t *testing.T) {
	seen := map[uint64]bool{}
	for i := 0; i < 1000; i++ {
		seed := seedIdentifierBase()
		if seed == 0 {
			t.Fatalf("call %d chose 0, which stands for a starting point not chosen yet", i)
		}
		if seen[seed] {
			t.Fatalf("call %d chose %#x, which an earlier call had chosen", i, seed)
		}
		seen[seed] = true
	}
}

// generateIdentifier takes its sequence from the starting point the process
// chose: with that left at its initial 0 the first identifier of every process
// would be 9e3779b97f4a7c15.
func TestTheIdentifiersOfAProcessStartFromAChosenBase(t *testing.T) {
	savedBase, savedCounter := identifierBase.Load(), identifierCounter.Load()
	t.Cleanup(func() { identifierBase.Store(savedBase); identifierCounter.Store(savedCounter) })

	identifierBase.Store(0)
	identifierCounter.Store(0)
	first := generateIdentifier("req")
	base := identifierBase.Load()
	if base == 0 {
		t.Fatal("making the first identifier chose no starting point")
	}
	if want := identifierAt("req", base, 1); first != want {
		t.Errorf("first identifier = %q, want %q (the chosen base %#x plus one stride)", first, want, base)
	}
	if first == identifierAt("req", 0, 1) {
		t.Errorf("first identifier = %q, the one a process with no starting point makes", first)
	}
	if second, want := generateIdentifier("req"), identifierAt("req", base, 2); second != want {
		t.Errorf("second identifier = %q, want %q", second, want)
	}
	if got := identifierBase.Load(); got != base {
		t.Errorf("the starting point changed from %#x to %#x while identifiers were made", base, got)
	}
}

func TestTheIdentifierForACounterAndABase(t *testing.T) {
	cases := []struct {
		prefix        string
		base, counter uint64
		want          string
	}{
		{"req", 0, 0, "req_0000000000000000"},
		{"req", 0, 1, "req_9e3779b97f4a7c15"},
		{"req", 1, 0, "req_0000000000000001"},
		{"req", 1, 1, "req_9e3779b97f4a7c16"},
		{"corr", 0, 1, "corr_9e3779b97f4a7c15"},
		{"corr", 0xffffffffffffffff, 0, "corr_ffffffffffffffff"},
		{"req", 0xffffffffffffffff, 1, "req_9e3779b97f4a7c14"},
		{"req", 0, 2, "req_3c6ef372fe94f82a"},
		{"", 0, 1, "_9e3779b97f4a7c15"},
		{"x", 0x0123456789abcdef, 0, "x_0123456789abcdef"},
		{"req", 0x8000000000000000, 0, "req_8000000000000000"},
	}
	for _, tc := range cases {
		if got := identifierAt(tc.prefix, tc.base, tc.counter); got != tc.want {
			t.Errorf("identifierAt(%q, %#x, %d) = %q, want %q", tc.prefix, tc.base, tc.counter, got, tc.want)
		}
	}
}

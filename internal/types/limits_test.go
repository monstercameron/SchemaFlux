package types

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestBoundCutsAndCleansWhatAProviderOrACallerChose(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"openai", MaxProviderName, "openai"},
		{"", MaxProviderName, ""},
		{"gpt-6-luna", 5, "gpt-6"},
		{"a\nb", 64, "a?b"},
		{"tab\there", 64, "tab?here"},
		{"é", 64, "??"},
		{"日本", 64, "??????"},
		{"ok\x00", 64, "ok?"},
		{strings.Repeat("x", 300), MaxModelName, strings.Repeat("x", MaxModelName)},
		{"short", 0, ""},
		{"short", -3, ""},
		{"evil\nlevel=ERROR forged", MaxID, "evil?level=ERROR forged"},
	}
	for _, c := range cases {
		if got := Bound(c.in, c.limit); got != c.want {
			t.Errorf("Bound(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = Bound("a-model-name", MaxModelName) }); allocs != 0 {
		t.Errorf("Bound allocates %v times for a name that needs nothing", allocs)
	}
}

func TestSumsAreHeldAtTheEdgesOfTheirType(t *testing.T) {
	t.Parallel()
	ints := []struct{ a, b, want int }{
		{1, 2, 3}, {0, 0, 0}, {-1, -2, -3}, {5, -7, -2},
		{math.MaxInt, 1, math.MaxInt}, {math.MaxInt, math.MaxInt, math.MaxInt}, {math.MaxInt - 1, 1, math.MaxInt},
		{math.MinInt, -1, math.MinInt}, {math.MinInt, math.MinInt, math.MinInt}, {math.MaxInt, math.MinInt, -1},
	}
	for _, c := range ints {
		if got := SumInt(c.a, c.b); got != c.want {
			t.Errorf("SumInt(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	durations := []struct{ a, b, want time.Duration }{
		{time.Second, time.Second, 2 * time.Second}, {0, 0, 0}, {-time.Second, -time.Second, -2 * time.Second},
		{math.MaxInt64, 1, math.MaxInt64}, {math.MaxInt64, math.MaxInt64, math.MaxInt64},
		{math.MinInt64, -1, math.MinInt64}, {math.MinInt64, math.MinInt64, math.MinInt64}, {math.MaxInt64, math.MinInt64, -1},
		{time.Hour, -time.Minute, 59 * time.Minute}, {math.MaxInt64 - 1, 1, math.MaxInt64},
	}
	for _, c := range durations {
		if got := SumDuration(c.a, c.b); got != c.want {
			t.Errorf("SumDuration(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

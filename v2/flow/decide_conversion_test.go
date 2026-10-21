package flow

import (
	"strconv"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

func TestConvertDecisionsAllocations(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want float64
	}{{1, 0}, {decideStack, 0}, {decideStack + 1, 1}, {40, 1}} {
		t.Run(strconv.Itoa(tc.n), func(t *testing.T) {
			decisions := make([]Decision[int], tc.n)
			for i := range decisions {
				decisions[i] = Decision[int]{Value: i, Description: "way " + strconv.Itoa(i)}
			}
			var stack [decideStack]ops.Decision[int]
			var converted []ops.Decision[int]
			allocs := testing.AllocsPerRun(100, func() {
				converted = convertDecisions(decisions, &stack)
			})
			if allocs != tc.want {
				t.Errorf("converting %d decisions allocated %v times, want %v", tc.n, allocs, tc.want)
			}
			if len(converted) != tc.n || converted[tc.n-1].Value != tc.n-1 {
				t.Errorf("converted decisions = %v", converted)
			}
		})
	}
}

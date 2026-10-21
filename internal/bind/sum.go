package bind

import (
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// SumInt returns a plus b, held at the largest int where the sum would pass it
// and at the smallest where it would pass that (types.SumInt). It is here for the
// packages that read their limits from this package.
func SumInt(a, b int) int { return types.SumInt(a, b) }

// SumDuration returns a plus b, held as SumInt holds an int (types.SumDuration).
func SumDuration(a, b time.Duration) time.Duration { return types.SumDuration(a, b) }

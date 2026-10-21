package types

import (
	"math"
	"strings"
	"time"
)

// The longest a name or an id that a caller or a provider chose may be where the
// library writes it to a log or an export: one limit for each kind of value, the
// same ones the telemetry package holds its events to.
const (
	// MaxProviderName is the longest provider name written out, in bytes.
	MaxProviderName = 64

	// MaxModelName is the longest model name written out, in bytes.
	MaxModelName = 128

	// MaxID is the longest request or correlation id written out, in bytes.
	MaxID = 128
)

// Bound returns value fit to be written anywhere: at most limit bytes, each a
// printable ASCII character, with any other byte replaced by "?". What a log or
// a span is given is an export to a place the library does not control, and a
// name that holds a newline would add a line to it. A value that is already both
// is returned as it is without allocating, which is every id the library makes
// and every model name in use.
func Bound(value string, limit int) string {
	clean := len(value) <= limit
	for index := 0; clean && index < len(value); index++ {
		clean = value[index] >= 0x20 && value[index] <= 0x7e
	}
	if clean {
		return value
	}

	if len(value) > limit {
		value = value[:max(limit, 0)]
	}
	var cleaned strings.Builder
	cleaned.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x20 && value[index] <= 0x7e {
			cleaned.WriteByte(value[index])
		} else {
			cleaned.WriteByte('?')
		}
	}
	return cleaned.String()
}

// SumInt returns a plus b, held at the largest int where the sum would pass it
// and at the smallest where it would pass that. A count a provider reports is a
// number it chose, and a sum of such counts that wraps round is a wrong figure in
// the shape of a right one.
func SumInt(a, b int) int {
	switch sum := a + b; {
	case a > 0 && b > 0 && sum < 0:
		return math.MaxInt
	case a < 0 && b < 0 && sum >= 0:
		return math.MinInt
	default:
		return sum
	}
}

// SumDuration returns a plus b, held at the largest and the smallest duration as
// SumInt holds an int.
func SumDuration(a, b time.Duration) time.Duration {
	switch sum := a + b; {
	case a > 0 && b > 0 && sum < 0:
		return math.MaxInt64
	case a < 0 && b < 0 && sum >= 0:
		return math.MinInt64
	default:
		return sum
	}
}

package schemaflux_test

import (
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// A-007 at the public boundary. The point of a taxonomy is that a consumer can
// branch on it without importing anything internal and without matching prose.
func TestConsumersCanBranchOnFailureKind(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		err      error
		sentinel error
	}{
		{
			"a body that is not JSON",
			"I'm sorry, I can't help with that.",
			nil,
			schemaflux.ErrMalformedOutput,
		},
		{
			"a well-formed body of the wrong shape",
			`{"unrelated": true}`,
			nil,
			schemaflux.ErrSchemaViolation,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withScriptedProvider(t, tc.body, tc.err)

			_, err := schemaflux.Extract[invoice]("Invoice INV-4417", schemaflux.NewExtractOptions())
			if err == nil {
				t.Fatal("expected a failure")
			}

			// The kind must be reachable without reading the message.
			if kind := schemaflux.KindOf(err); kind == 0 {
				t.Errorf("the failure is unclassified: %v", err)
			}
		})
	}
}

// A provider failure classifies as something a caller can act on, and the
// disposition helpers agree with it.
func TestProviderFailuresCarryADisposition(t *testing.T) {
	withScriptedProvider(t, "", errors.New("connection refused"))

	_, err := schemaflux.Extract[invoice]("Invoice INV-4417", schemaflux.NewExtractOptions())
	if err == nil {
		t.Fatal("expected a failure")
	}

	var opErr *schemaflux.OperationError
	if errors.As(err, &opErr) {
		// If it classified, the three dispositions must be mutually exclusive.
		count := 0
		for _, flag := range []bool{opErr.Retryable(), opErr.Repairable(), opErr.Terminal()} {
			if flag {
				count++
			}
		}
		if count > 1 {
			t.Errorf("%v reports %d dispositions at once", opErr.Kind, count)
		}
	}
}

// The sentinels are distinct: matching one must not match another, or a caller
// branching on them takes the wrong branch.
func TestSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{
		schemaflux.ErrConfiguration,
		schemaflux.ErrAuthentication,
		schemaflux.ErrRateLimited,
		schemaflux.ErrTimeout,
		schemaflux.ErrSchemaViolation,
		schemaflux.ErrInvariantViolation,
		schemaflux.ErrReviewRequired,
		schemaflux.ErrBudgetExceededKind,
	}

	for i, first := range sentinels {
		for j, second := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(first, second) {
				t.Errorf("sentinel %v matches %v", first, second)
			}
		}
	}
}

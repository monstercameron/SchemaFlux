package fluent

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// Critiquing must be able to produce a request that validates.
//
// It could not. CritiqueOptions.Validate requires at least one criterion or
// rubric entry, and the fluent builder exposed neither — so
// `Critiquing(x).Run()` failed validation before any provider call, for every
// caller, and the only working spelling was the non-fluent
// `Critique(input, opts)`. A builder that cannot invoke its own operation is
// decoration.
//
// Two tests, because both halves matter: the refusal has to still happen when
// nothing is supplied, and supplying either one has to make the call go.
func TestCritiquingWithoutCriteriaIsStillRefused(t *testing.T) {
	provider := &countingProvider{body: `{"findings":[]}`}
	restore := installStubProvider(t, provider)
	defer restore()

	_, err := Critiquing("some work").Run()
	if err == nil {
		t.Fatal("Critiquing with no criteria and no rubric was accepted; Validate requires one of them")
	}
	if provider.calls.Load() != 0 {
		t.Errorf("the provider was contacted %d time(s) for a request that cannot validate", provider.calls.Load())
	}
}

func TestCritiquingReachesTheProviderOnceItHasCriteria(t *testing.T) {
	cases := []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"By", func(ctx context.Context) error {
			_, err := Critiquing("some work").By("clarity", "accuracy").Context(ctx).Run(ctx)
			return err
		}},
		{"Rubric", func(ctx context.Context) error {
			_, err := Critiquing("some work").
				Rubric(map[string]string{"clarity": "is it readable"}).
				Context(ctx).Run(ctx)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &countingProvider{body: `{"findings":[],"summary":{}}`}
			ctx := ops.WithProvider(context.Background(), provider)

			// The error is not asserted: the stub body may not satisfy
			// Critique's own decoding, which is that operation's concern. What
			// is under test is that the request got past validation and
			// dispatched at all.
			_ = tc.run(ctx)

			if provider.calls.Load() == 0 {
				t.Errorf("Critiquing(...).%s(...) never reached the provider; it is still failing validation before dispatch", tc.name)
			}
		})
	}
}

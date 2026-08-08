package tests

import (
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// `.Steer(...)` has to reach the provider. It did not.
//
// # What was wrong
//
// Every options type embeds both `CommonOptions` and `types.OpOptions`, and each
// carries a `Steering`. The fluent builders write the CommonOptions one;
// twenty-four operations read the OpOptions one directly to decide whether the
// caller had said anything. So for the whole fluent API that check saw an empty
// string.
//
// In the operations that compose their own instructions — Choose, Score,
// Summarize, Rewrite, Translate, Expand and the rest — that check is what
// PRESERVES the caller's steering before the composed instructions overwrite the
// field. So `.Steer(...)` was not merely ignored. It was silently discarded, on
// every call, while the builder that set it returned no error and the operation
// returned a perfectly well-formed answer to a question the caller had not
// asked.
//
// That is the failure mode this library is being rebuilt against: reporting
// success while being wrong. Nothing failed, nothing logged, and the only
// symptom was answers that quietly ignored the instruction.
//
// # Why it is asserted here rather than in internal/ops
//
// The bug lives in the seam between the builder and the operation, so a test
// that constructs an options struct by hand cannot see it — it would set the
// field the operation already reads. Only the exported fluent path exercises the
// combination that was broken, which is why this is an integration test through
// the public API with a provider installed.

// steerMarker is a string no prompt would contain on its own, so finding it
// proves it was carried rather than coincidentally present.
const steerMarker = "ZZ-STEER-MARKER-ZZ"

// sent runs one operation against an installed fake and returns everything the
// provider was asked, system prompt and user prompt together — the steering is
// composed into one or the other depending on the operation, and which one is
// not the property under test.
func sent(t *testing.T, reply string, run func() error) string {
	t.Helper()
	p := schemafluxtest.New().Shaped().Reply(reply)
	schemafluxtest.Install(t, p)
	if err := run(); err != nil {
		t.Fatalf("the operation failed: %v", err)
	}
	if p.CallCount() == 0 {
		t.Fatal("the operation never reached the provider")
	}
	return p.LastRequest().SystemPrompt + p.LastRequest().UserPrompt
}

func TestSteeringReachesTheProviderForEveryTextOperation(t *testing.T) {
	// The four text operations, each of which composes instructions from its own
	// options and would therefore overwrite the caller's steering.
	cases := []struct {
		name  string
		reply string
		run   func() error
	}{
		{"Summarizing", `a summary`, func() error {
			_, err := schemaflux.Summarizing("some text").Steer(steerMarker).Run()
			return err
		}},
		{"Rewriting", `rewritten`, func() error {
			_, err := schemaflux.Rewriting("some text").Steer(steerMarker).Run()
			return err
		}},
		{"Translating", `traduit`, func() error {
			_, err := schemaflux.Translating("some text").To("French").Steer(steerMarker).Run()
			return err
		}},
		{"Expanding", `expanded`, func() error {
			_, err := schemaflux.Expanding("some text").Steer(steerMarker).Run()
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sent(t, c.reply, c.run); !strings.Contains(got, steerMarker) {
				t.Errorf("%s dropped the caller's steering:\n%s", c.name, got)
			}
		})
	}
}

func TestSteeringReachesTheProviderForEveryCollectionOperation(t *testing.T) {
	// These are the ones where steering is most load-bearing: the caller is
	// describing WHAT to select or rank by, and an operation that dropped it
	// would choose confidently on the criteria alone.
	cases := []struct {
		name  string
		reply string
		run   func() error
	}{
		{"Choosing", `{"id":"i-000001"}`, func() error {
			_, err := schemaflux.Choosing([]string{"a", "b"}).By("pick one").Steer(steerMarker).Run()
			return err
		}},
		{"ChoosingWithoutCriteria", `{"id":"i-000001"}`, func() error {
			// The branch that was NOT broken, kept so a future refactor that
			// "simplifies" the two into one is caught by whichever it breaks.
			_, err := schemaflux.Choosing([]string{"a", "b"}).Steer(steerMarker).Run()
			return err
		}},
		{"Filtering", `{"ids":["i-000001"]}`, func() error {
			// Criteria are required by these two, and the library says so rather
			// than guessing — which is the behaviour, not an obstacle.
			_, err := schemaflux.Filtering([]string{"a", "b"}).By("keep the good ones").
				Steer(steerMarker).Run()
			return err
		}},
		{"Sorting", `{"ids":["i-000001","i-000002"]}`, func() error {
			_, err := schemaflux.Sorting([]string{"a", "b"}).By("by quality").
				Steer(steerMarker).Run()
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sent(t, c.reply, c.run); !strings.Contains(got, steerMarker) {
				t.Errorf("%s dropped the caller's steering:\n%s", c.name, got)
			}
		})
	}
}

func TestSteeringSurvivesAlongsideTheOperationsOwnInstructions(t *testing.T) {
	// The exact shape of the bug: the composed instructions and the caller's
	// steering both have to be there. Asserting only the marker would pass on an
	// implementation that threw the criteria away instead, which is the same
	// mistake pointing the other way.
	got := sent(t, `{"id":"i-000001"}`, func() error {
		_, err := schemaflux.Choosing([]string{"a", "b"}).
			By("the cheapest one").
			Steer(steerMarker).
			Run()
		return err
	})
	if !strings.Contains(got, steerMarker) {
		t.Error("the caller's steering was dropped")
	}
	if !strings.Contains(got, "the cheapest one") {
		t.Error("the operation's own criteria were dropped")
	}
}

func TestTwoSteerCallsAreBothCarried(t *testing.T) {
	// CommonOptions.WithSteering appends rather than assigns, deliberately, so a
	// second call adds a second instruction. That only means anything if the
	// result reaches the provider.
	got := sent(t, `a summary`, func() error {
		_, err := schemaflux.Summarizing("some text").
			Steer(steerMarker).
			Steer("SECOND-INSTRUCTION").
			Run()
		return err
	})
	if !strings.Contains(got, steerMarker) || !strings.Contains(got, "SECOND-INSTRUCTION") {
		t.Errorf("both steering instructions should be carried:\n%s", got)
	}
}

func TestSteeringSetOnTheOptionsStructStillWorks(t *testing.T) {
	// The other way to set it, which the examples under examples/ use. The fix
	// must not trade one field for the other — this is the regression that a
	// naive "read CommonOptions instead" would have caused.
	opts := schemaflux.NewSummarizeOptions()
	opts.OpOptions.Steering = steerMarker

	p := schemafluxtest.New().Shaped().Reply(`a summary`)
	schemafluxtest.Install(t, p)
	if _, err := schemaflux.Summarize("some text", opts); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	got := p.LastRequest().SystemPrompt + p.LastRequest().UserPrompt
	if !strings.Contains(got, steerMarker) {
		t.Errorf("steering set on the options struct was dropped:\n%s", got)
	}
}

func TestNoSteeringSendsNoEmptyInstruction(t *testing.T) {
	// A caller who said nothing must not have an empty instruction composed on
	// their behalf — "Selection Requirements: " with nothing after it is noise
	// the model has to interpret.
	got := sent(t, `{"id":"i-000001"}`, func() error {
		_, err := schemaflux.Choosing([]string{"a", "b"}).By("pick one").Run()
		return err
	})
	if strings.Contains(got, "Selection Requirements: .") ||
		strings.Contains(got, "Selection Requirements: \n") {
		t.Errorf("an empty steering instruction was composed:\n%s", got)
	}
}

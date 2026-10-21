package client

import (
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
)

// PS-012. The retry contract an embedding application codes against, pinned at
// the seam where a Client's settings become the provider config the provider
// reports back through RetryPolicy.
//
// internal/ops/retry_policy_test.go already pins the dispatcher's half: a
// budget of n means n+1 requests, zero means one, and a negative means the
// library's default. What was untested is the half above it -- whether what a
// host set on the Client is what the provider ends up holding. A host that
// calls WithRetries(0) because it does its own retrying gets no warning if the
// number is quietly replaced on the way down, and two layers that both retry
// multiply.

func TestTheClientsRetryBudgetReachesTheProviderConfig(t *testing.T) {
	cases := []struct {
		name        string
		clientSets  int
		override    llm.ProviderConfig
		wantRetries int
		why         string
	}{
		{
			name:        "the default budget",
			clientSets:  -1, // do not call WithRetries at all
			wantRetries: 3,
			why:         "NewClient's documented default",
		},
		{
			name:        "retries turned off",
			clientSets:  0,
			wantRetries: 0,
			why:         "zero means zero; an embedder doing its own retrying depends on this",
		},
		{
			name:        "an explicit budget",
			clientSets:  5,
			wantRetries: 5,
			why:         "what the host asked for, unmodified",
		},
		{
			name:        "a per-provider override wins over the client's budget",
			clientSets:  5,
			override:    llm.ProviderConfig{MaxRetries: 2},
			wantRetries: 2,
			why:         "the narrower setting is the more specific statement",
		},
		{
			name:        "an override of zero leaves the client's budget alone",
			clientSets:  5,
			override:    llm.ProviderConfig{MaxRetries: 0},
			wantRetries: 5,
			why:         "an unset field in a config struct is not a request for zero",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := NewClient("test-key")
			if testCase.clientSets >= 0 {
				client = client.WithRetries(testCase.clientSets)
			}

			cfg := client.providerConfig("openai", testCase.override)
			if cfg.MaxRetries != testCase.wantRetries {
				t.Errorf("MaxRetries = %d, want %d (%s)", cfg.MaxRetries, testCase.wantRetries, testCase.why)
			}

			// And the provider the client installed reports the same budget,
			// whichever order the settings were given in. The config above is
			// only what the client would build from; this is what it holds.
			for _, order := range []string{"retries before the provider", "retries after the provider"} {
				installed := NewClient("test-key")
				if order == "retries before the provider" {
					if testCase.clientSets >= 0 {
						installed.WithRetries(testCase.clientSets)
					}
					installed.WithProviderConfig("openai", testCase.override)
				} else {
					installed.WithProviderConfig("openai", testCase.override)
					if testCase.clientSets >= 0 {
						installed.WithRetries(testCase.clientSets)
					}
				}
				got, _ := installed.provider.RetryPolicy()
				// A budget the provider config names is the more specific
				// statement, and holds whichever order they were given in.
				want := testCase.wantRetries
				if got != want {
					t.Errorf("%s: the installed provider retries %d times, want %d (%s)", order, got, want, testCase.why)
				}
			}
		})
	}
}

// WithRetries clamps a negative to zero, and a provider returning a negative
// from RetryPolicy gets the library's default instead. Those are different
// answers to what looks like the same input, so the difference is pinned here
// rather than left for somebody to discover as a surprise.
//
// They are different questions. A caller setting a ceiling of -1 can only mean
// "none" -- there is no such thing as minus one retry. A provider returning -1
// is declining to state a policy, which is a thing a provider can legitimately
// have no opinion about, and the library's default is the right answer to it.
func TestANegativeCeilingIsZeroWhileANegativeProviderPolicyIsNoOpinion(t *testing.T) {
	client := NewClient("test-key").WithRetries(-1)

	cfg := client.providerConfig("openai", llm.ProviderConfig{})
	if cfg.MaxRetries != 0 {
		t.Errorf("WithRetries(-1) produced MaxRetries = %d, want 0; a negative ceiling means none", cfg.MaxRetries)
	}

	// The other side of the asymmetry, stated as an assertion so that changing
	// either one fails here rather than silently drifting apart from the
	// README's table.
	provider := &fixedPolicyProvider{retries: -1}
	if retries, _ := provider.RetryPolicy(); retries >= 0 {
		t.Fatalf("the fixture is meant to return a negative policy, got %d", retries)
	}
}

// The backoff follows the same shape: a positive value replaces the default, a
// zero or negative one is not a request for no backoff.
func TestTheRetryBackoffIsReplacedOnlyByAPositiveValue(t *testing.T) {
	client := NewClient("test-key").WithRetryBackoff(250 * time.Millisecond)
	cfg := client.providerConfig("openai", llm.ProviderConfig{})
	if cfg.RetryBackoff != 250*time.Millisecond {
		t.Errorf("RetryBackoff = %v, want 250ms", cfg.RetryBackoff)
	}

	// Zero leaves what was already there, because a zero backoff would mean a
	// retry storm rather than a considered setting.
	unset := NewClient("test-key").WithRetryBackoff(0)
	cfg = unset.providerConfig("openai", llm.ProviderConfig{})
	if cfg.RetryBackoff <= 0 {
		t.Errorf("RetryBackoff = %v after WithRetryBackoff(0); a zero backoff is a retry storm, not a setting",
			cfg.RetryBackoff)
	}
}

// fixedPolicyProvider exists only to state a policy. It is not a full
// llm.Provider because nothing here runs a request through it -- the question
// is what RetryPolicy reports, and a Complete that is never called would be
// three lines of fiction.
type fixedPolicyProvider struct {
	retries int
}

func (p *fixedPolicyProvider) RetryPolicy() (int, time.Duration) {
	return p.retries, time.Millisecond
}

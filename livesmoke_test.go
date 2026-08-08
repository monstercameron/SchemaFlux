package schemaflux_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// P-012 and P-014, arranged so that one authorized run closes both.
//
// P-012 wants one `/v1/responses` call per 5.6 model, asserting a successful
// response, non-empty message text, and populated usage including reasoning
// tokens. P-014 wants those same responses recorded as cassettes so the checks
// keep running in CI forever without credits.
//
// Doing them as two separate exercises would mean paying twice for the same
// information, so this records while it asserts: the smoke test runs through a
// `schemafluxtest.Recorder`, and what it observed is written to
// `testdata/cassettes/` on the way out. The replay test below then re-asserts
// the same properties against those files on every ordinary `go test ./...`.
//
// To record (this SPENDS MONEY — one call per model):
//
//	SCHEMAFLUX_LIVE_TESTS=1 go test . -run TestLiveSmokeAcrossTheModelFamily -v
//
// After that, `go test ./...` replays them for free, forever.

// cassetteDir is committed, unlike `.audit/live/`, which is gitignored because
// raw provider bodies carry account identifiers. Cassettes are safe to commit
// precisely because they are a projection: content, finish reason, and usage —
// no response ids, no account fields, no headers — and Recorder.Save refuses to
// write one that still looks like it contains a credential.
const cassetteDir = "testdata/cassettes/live-smoke"

// smokeModels are the three models P-013 measured. Overridable so this is not
// stale the moment the family changes.
func smokeModels() []string {
	if raw := os.Getenv("SCHEMAFLUX_SMOKE_MODELS"); raw != "" {
		return strings.Split(raw, ",")
	}
	return []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}
}

const smokePrompt = "Reply with a single short sentence confirming you received this message."

func TestLiveSmokeAcrossTheModelFamily(t *testing.T) {
	if os.Getenv("SCHEMAFLUX_LIVE_TESTS") != "1" {
		t.Skip("set SCHEMAFLUX_LIVE_TESTS=1 to run the live smoke test; it spends money (one call per model)")
	}

	key := firstNonEmptyEnv("SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "SCHEMAFLUX_API_KEY", "OPENAI")
	if key == "" {
		t.Fatal("SCHEMAFLUX_LIVE_TESTS=1 but no API key was found in SCHEMAFLUX_OPENAI_API_KEY, OPENAI_API_KEY, SCHEMAFLUX_API_KEY, or OPENAI")
	}

	provider, err := schemaflux.CreateProvider("openai", schemaflux.ProviderConfig{APIKey: key})
	if err != nil {
		t.Fatalf("building the provider: %v", err)
	}

	recorder := schemafluxtest.Record(provider, cassetteDir)

	for _, model := range smokeModels() {
		model := strings.TrimSpace(model)
		t.Run(model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			resp, err := recorder.Complete(ctx, schemaflux.CompletionRequest{
				Model:      model,
				UserPrompt: smokePrompt,
			})
			if err != nil {
				t.Fatalf("live call to %s failed: %v", model, err)
			}

			assertSmokeProperties(t, model, resp.Content, resp.Usage)
		})
	}

	// One harder prompt, to answer P-012's "including reasoning tokens" clause
	// with evidence rather than with an assumption.
	//
	// The short prompts above report reasoning_tokens of 0, and that is not a
	// bug: supportsReasoningControls returns false for the 5.6 family, so this
	// library sends no reasoning block at all. Whether the API produces
	// reasoning tokens anyway, unprompted, on a problem that needs them is a
	// question only a call can answer -- and it is worth one call, because the
	// alternative is a task closed on a guess about what the field does.
	t.Run("reasoning-tokens-on-a-hard-problem", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		resp, err := recorder.Complete(ctx, schemaflux.CompletionRequest{
			Model: smokeModels()[0],
			UserPrompt: "A train leaves A at 09:00 averaging 72 km/h. Another leaves B at 09:40 " +
				"averaging 90 km/h toward A. A and B are 411 km apart. At what clock time do they meet? " +
				"Answer with the time only.",
		})
		if err != nil {
			t.Fatalf("live call failed: %v", err)
		}

		assertSmokeProperties(t, smokeModels()[0], resp.Content, resp.Usage)
		// Recorded, not asserted. A test that demanded a positive count here
		// would be asserting a property of the provider's internals that this
		// library does not request and cannot control.
		t.Logf("reasoning_tokens on a multi-step problem = %d (cached=%d, total=%d)",
			resp.Usage.ReasoningTokens, resp.Usage.CachedTokens, resp.Usage.TotalTokens)
	})

	if err := recorder.Save(); err != nil {
		t.Fatalf("saving cassettes: %v", err)
	}
	t.Logf("recorded %d cassette(s) to %s — commit them and this runs free from now on",
		len(recorder.Recorded()), cassetteDir)
}

// assertSmokeProperties is P-012's checklist, written once so the live run and
// the replay below cannot drift into checking different things — which is the
// usual way a recorded fixture stops standing in for the thing it recorded.
func assertSmokeProperties(t *testing.T, model, content string, usage schemaflux.TokenUsage) {
	t.Helper()

	if strings.TrimSpace(content) == "" {
		t.Errorf("%s returned empty message text", model)
	}

	// Usage has to be populated, not merely present. A provider that returns a
	// zeroed usage block is indistinguishable from one that returns none, and
	// every cost figure downstream is built from these numbers.
	if usage.InputTokens <= 0 && usage.PromptTokens <= 0 {
		t.Errorf("%s reported no input tokens; cost accounting reads this", model)
	}
	if usage.OutputTokens <= 0 && usage.CompletionTokens <= 0 {
		t.Errorf("%s reported no output tokens", model)
	}
	if usage.TotalTokens <= 0 {
		t.Errorf("%s reported no total tokens", model)
	}

	// Reasoning tokens are asserted as *present and non-negative*, not as
	// non-zero. P-013's own recordings show reasoning_tokens of 0 for a short
	// prompt on these models, so requiring a positive count would fail against
	// the real API — and a test that demands a number the provider does not
	// produce gets deleted rather than fixed.
	if usage.ReasoningTokens < 0 {
		t.Errorf("%s reported negative reasoning tokens (%d)", model, usage.ReasoningTokens)
	}
}

// The free half: once the cassettes exist, this re-asserts the same properties
// on every ordinary run, with no credentials and no spend.
//
// It skips rather than fails when the directory is absent, because a repository
// that has never had an authorized live run has not done anything wrong — but
// it says so loudly enough that "the smoke test is green" is never mistaken for
// "the smoke test ran".
func TestReplayedSmokeCassettesStillSatisfyP012(t *testing.T) {
	entries, dirErr := os.ReadDir(cassetteDir)
	if dirErr != nil || len(entries) == 0 {
		t.Skipf("no cassettes in %s — P-012's assertions have never been recorded. "+
			"Run TestLiveSmokeAcrossTheModelFamily with SCHEMAFLUX_LIVE_TESTS=1 to record them (this spends money).",
			cassetteDir)
	}

	// Read the files directly rather than through Player: Player exists to
	// answer requests, and what this needs is to inspect every recording,
	// including any that a replay would never be asked for.
	var recorded []schemafluxtest.Cassette
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(cassetteDir, entry.Name()))
		if readErr != nil {
			t.Fatalf("reading %s: %v", entry.Name(), readErr)
		}
		var cassette schemafluxtest.Cassette
		if err := json.Unmarshal(raw, &cassette); err != nil {
			t.Fatalf("decoding %s: %v", entry.Name(), err)
		}
		recorded = append(recorded, cassette)
	}
	if len(recorded) == 0 {
		t.Fatalf("%s exists but holds no cassettes", cassetteDir)
	}

	seen := map[string]bool{}
	for _, cassette := range recorded {
		if cassette.Response == nil {
			t.Errorf("%s recorded a failure rather than a response: %+v", cassette.Model, cassette.Failure)
			continue
		}
		seen[cassette.Model] = true
		assertSmokeProperties(t, cassette.Model, cassette.Response.Content, cassette.Response.Usage)
	}

	// Every model the live run covers must be represented, or a model could be
	// dropped from the family and the replay would keep passing while covering
	// less than it claims.
	for _, model := range smokeModels() {
		if !seen[strings.TrimSpace(model)] {
			t.Errorf("no cassette for %s; the replay covers less than the live run does", model)
		}
	}
}

// The cassettes must be safe to commit. Recorder.Save already refuses to write
// a credential-shaped string, but that check runs at record time on one
// machine; this one runs in CI, on whatever is actually in the tree.
func TestCommittedCassettesCarryNoCredentialsOrAccountIdentifiers(t *testing.T) {
	entries, err := os.ReadDir(cassetteDir)
	if err != nil || len(entries) == 0 {
		t.Skipf("no cassettes in %s yet", cassetteDir)
	}

	// Fragments that should never appear in a committed fixture: key prefixes,
	// and the response/account identifiers the raw provider bodies carry and
	// the cassette projection is supposed to have dropped.
	forbidden := []string{"sk-", "org-", "resp_", "\"id\":", "Authorization"}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(cassetteDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, fragment := range forbidden {
			if strings.Contains(string(raw), fragment) {
				t.Errorf("%s contains %q; cassettes are committed and must carry no credential or account identifier",
					path, fragment)
			}
		}
	}
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

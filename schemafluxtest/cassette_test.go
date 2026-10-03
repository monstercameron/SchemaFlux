package schemafluxtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// cassetteFake is the provider a Recorder wraps in these tests. It is scripted
// rather than random so a replay can be compared against something known.
type cassetteFake struct {
	name     string
	answers  []cassetteAnswer
	position int
	seen     []schemaflux.CompletionRequest
	cost     float64
	retries  int
	backoff  time.Duration
}

type cassetteAnswer struct {
	response schemaflux.CompletionResponse
	err      error
}

func (f *cassetteFake) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	f.seen = append(f.seen, req)
	if f.position >= len(f.answers) {
		return schemaflux.CompletionResponse{}, &types.OperationError{
			Kind:    types.KindConfiguration,
			Message: "cassetteFake ran out of scripted answers",
		}
	}
	answer := f.answers[f.position]
	f.position++
	return answer.response, answer.err
}

func (f *cassetteFake) Name() string { return f.name }
func (f *cassetteFake) EstimateCost(schemaflux.CompletionRequest) float64 {
	return f.cost
}
func (f *cassetteFake) RetryPolicy() (int, time.Duration) { return f.retries, f.backoff }

func succeedingFake(content string) *cassetteFake {
	return &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{{
			response: schemaflux.CompletionResponse{
				Content:      content,
				Provider:     "openai",
				Model:        "gpt-5.6-sol",
				FinishReason: "stop",
				Usage: schemaflux.TokenUsage{
					PromptTokens:     41,
					CompletionTokens: 7,
					TotalTokens:      48,
				},
			},
		}},
	}
}

func sampleRequest() schemaflux.CompletionRequest {
	return schemaflux.CompletionRequest{
		Model:          "gpt-5.6-sol",
		SystemPrompt:   "Extract the fields named in the schema.",
		UserPrompt:     "Customer was charged twice for the March invoice.",
		ResponseFormat: "json_object",
		SchemaName:     "triage",
		JSONSchema:     map[string]any{"type": "object"},
	}
}

// The whole point: record once against something real, replay forever with no
// network and no credential, and get the same answer back.
func TestRecordThenReplayReturnsTheSameAnswer(t *testing.T) {
	inner := succeedingFake(`{"queue":"billing","priority":"high"}`)
	recorder := schemafluxtest.Record(inner, t.TempDir())

	recorded, err := recorder.Complete(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	player := schemafluxtest.ReplayFrom(recorder.Recorded()...)
	replayed, err := player.Complete(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}

	if replayed.Content != recorded.Content {
		t.Errorf("content: replayed %q, recorded %q", replayed.Content, recorded.Content)
	}
	if replayed.FinishReason != recorded.FinishReason {
		t.Errorf("finish reason: replayed %q, recorded %q", replayed.FinishReason, recorded.FinishReason)
	}
	if replayed.Usage != recorded.Usage {
		t.Errorf("usage: replayed %+v, recorded %+v", replayed.Usage, recorded.Usage)
	}
	if replayed.Provider != "openai" || replayed.Model != "gpt-5.6-sol" {
		t.Errorf("provider/model not carried: %q %q", replayed.Provider, replayed.Model)
	}
	if remaining := player.Remaining(); remaining != 0 {
		t.Errorf("Remaining after playing the only cassette = %d, want 0", remaining)
	}
}

// A cassette recorded against one prompt does not answer a different one. This
// is the failure mode the strict default exists for: the prompt gets edited,
// every test still passes, and the fixture is now proving nothing.
func TestReplayRefusesAChangedPrompt(t *testing.T) {
	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), t.TempDir())
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	changed := sampleRequest()
	changed.UserPrompt = "Customer asked to change their shipping address."

	player := schemafluxtest.ReplayFrom(recorder.Recorded()...)
	if _, err := player.Complete(context.Background(), changed); err == nil {
		t.Fatal("a changed user prompt replayed successfully; the fixture would prove nothing")
	} else if !strings.Contains(err.Error(), "different user prompt") {
		t.Errorf("error does not say what changed: %v", err)
	}
}

func TestReplayRefusesAChangedSystemPrompt(t *testing.T) {
	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), t.TempDir())
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	changed := sampleRequest()
	changed.SystemPrompt = "Extract the fields named in the schema. Be concise."

	player := schemafluxtest.ReplayFrom(recorder.Recorded()...)
	if _, err := player.Complete(context.Background(), changed); err == nil {
		t.Fatal("an edited system prompt replayed successfully")
	}
}

// Lenient is the escape hatch for a test that only wants the answers, and it
// has to actually work, or people will stop recording and go back to mocks.
func TestLenientReplayAcceptsAChangedPrompt(t *testing.T) {
	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), t.TempDir())
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	changed := sampleRequest()
	changed.UserPrompt = "Something else entirely."

	player := schemafluxtest.ReplayFrom(recorder.Recorded()...).Lenient()
	resp, err := player.Complete(context.Background(), changed)
	if err != nil {
		t.Fatalf("Lenient replay refused a changed prompt: %v", err)
	}
	if resp.Content != `{"queue":"billing"}` {
		t.Errorf("content = %q", resp.Content)
	}
}

// PRD-13's actual point: a replay that only feeds the response back proves the
// parser works and nothing about how a failure was classified. So a recorded
// failure has to come back as a failure, with the same kind.
func TestReplayReproducesTheRecordedClassification(t *testing.T) {
	inner := &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{{
			err: &types.OperationError{
				Kind:    types.KindRateLimited,
				Message: "429 from the provider",
			},
		}},
	}
	recorder := schemafluxtest.Record(inner, t.TempDir())
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err == nil {
		t.Fatal("the recorder swallowed the provider's failure")
	}

	player := schemafluxtest.ReplayFrom(recorder.Recorded()...)
	_, err := player.Complete(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a recorded failure replayed as a success")
	}

	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("replayed failure is not an *types.OperationError: %T", err)
	}
	if opErr.Kind != types.KindRateLimited {
		t.Errorf("replayed kind = %v, recorded kind was %v", opErr.Kind, types.KindRateLimited)
	}
	if !opErr.Retryable() {
		t.Error("a replayed rate limit is not retryable; the disposition did not survive the round trip")
	}
}

// Stored by name, so inserting a constant in the middle of the taxonomy does
// not silently re-point every cassette already committed.
func TestClassificationIsStoredByNameNotByNumber(t *testing.T) {
	inner := &cassetteFake{
		name: "anthropic",
		answers: []cassetteAnswer{{
			err: &types.OperationError{Kind: types.KindContextTooLarge, Message: "too long"},
		}},
	}
	recorder := schemafluxtest.Record(inner, t.TempDir())
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err == nil {
		t.Fatal("expected the scripted failure")
	}

	recorded := recorder.Recorded()
	if len(recorded) != 1 || recorded[0].Failure == nil {
		t.Fatalf("expected one recorded failure, got %+v", recorded)
	}
	if got, want := recorded[0].Failure.Kind, types.KindContextTooLarge.String(); got != want {
		t.Errorf("recorded kind = %q, want %q", got, want)
	}
}

// A secret that reaches the disk has leaked. Redaction runs before the write
// for that reason, not after the read.
func TestSaveRedactsCredentialsBeforeTheyReachDisk(t *testing.T) {
	directory := t.TempDir()

	inner := &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{{
			response: schemaflux.CompletionResponse{
				Content: "the operator pasted sk-ant-api03-ZZZZZZZZZZZZZZZZZZZZZZZZZZZZ into the ticket", // secret-scan: allow
			},
		}},
	}
	recorder := schemafluxtest.Record(inner, directory)

	req := sampleRequest()
	req.SystemPrompt = "Use key sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA when calling out." // secret-scan: allow
	if _, err := recorder.Complete(context.Background(), req); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
	if err != nil {
		t.Fatalf("reading the written cassette: %v", err)
	}
	written := string(raw)

	for _, secret := range []string{"sk-proj-AAAA", "sk-ant-api03-ZZZZ"} {
		if strings.Contains(written, secret) {
			t.Errorf("a credential reached the disk: %q is present in the cassette", secret)
		}
	}
	if !strings.Contains(written, "[redacted by schemafluxtest]") {
		t.Error("nothing was redacted, so the patterns did not fire at all")
	}
	// The surrounding text survives: redaction that eats the whole field makes
	// the cassette useless for the thing it was recorded for.
	if !strings.Contains(written, "into the ticket") {
		t.Error("redaction removed more than the credential")
	}

	var reloaded schemafluxtest.Cassette
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("the written cassette is not valid JSON: %v", err)
	}
	if reloaded.Provider != "openai" {
		t.Errorf("provider survived as %q", reloaded.Provider)
	}
}

// Every shape the repository's secret scanner knows about, and a control that
// must not be touched. A redactor that eats ordinary text gets turned off.
func TestEveryCredentialShapeIsRedacted(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		redacts bool
	}{
		{"OpenAI key", "sk-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", true},                     // secret-scan: allow
		{"OpenAI project key", "sk-proj-BBBBBBBBBBBBBBBBBBBBBBBBBBBB", true},            // secret-scan: allow
		{"OpenAI service account key", "sk-svcacct-CCCCCCCCCCCCCCCCCCCCCCCCCCCC", true}, // secret-scan: allow
		{"Anthropic key", "sk-ant-api03-DDDDDDDDDDDDDDDDDDDDDDDDDD", true},              // secret-scan: allow
		{"Google API key", "AIzaSyDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", true},           // secret-scan: allow
		{"AWS access key ID", "AKIAIOSFODNN7EXAMPLE", true},
		{"AWS session key ID", "ASIAIOSFODNN7EXAMPLE", true},
		{"GitHub token", "ghp_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", true}, // secret-scan: allow
		{"bearer header", `Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc`, true},
		{"an invoice number", "invoice SK-2026-000418 was charged twice", false},
		{"prose about keys", "the customer lost their sk key, they said", false},
		{"a schema field name", `{"api_key_present": true}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			inner := &cassetteFake{
				name:    "openai",
				answers: []cassetteAnswer{{response: schemaflux.CompletionResponse{Content: tc.text}}},
			}
			recorder := schemafluxtest.Record(inner, directory)
			if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
				t.Fatalf("recording: %v", err)
			}
			if err := recorder.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}

			raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			redacted := strings.Contains(string(raw), "[redacted by schemafluxtest]")

			if redacted != tc.redacts {
				if tc.redacts {
					t.Errorf("%s was written through unredacted", tc.name)
				} else {
					t.Errorf("%s was redacted; ordinary text must survive", tc.name)
				}
			}
		})
	}
}

// The committed-directory workflow, end to end: record, save, and replay from
// disk in a later process that has no provider and no key.
func TestReplayFromDiskRoundTrips(t *testing.T) {
	directory := t.TempDir()

	inner := &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{
			{response: schemaflux.CompletionResponse{Content: `{"step":1}`, Model: "gpt-5.6-sol"}},
			{response: schemaflux.CompletionResponse{Content: `{"step":2}`, Model: "gpt-5.6-sol"}},
		},
	}
	recorder := schemafluxtest.Record(inner, directory)

	first := sampleRequest()
	second := sampleRequest()
	second.UserPrompt = "And the second question."

	for _, req := range []schemaflux.CompletionRequest{first, second} {
		if _, err := recorder.Complete(context.Background(), req); err != nil {
			t.Fatalf("recording: %v", err)
		}
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	player, err := schemafluxtest.Replay(directory)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if remaining := player.Remaining(); remaining != 2 {
		t.Fatalf("loaded %d cassettes, wrote 2", remaining)
	}

	// Filename order has to be call order, or a two-call test replays its
	// answers backwards and the failure looks like a logic bug.
	for i, req := range []schemaflux.CompletionRequest{first, second} {
		resp, err := player.Complete(context.Background(), req)
		if err != nil {
			t.Fatalf("replaying call %d: %v", i+1, err)
		}
		want := []string{`{"step":1}`, `{"step":2}`}[i]
		if resp.Content != want {
			t.Errorf("call %d replayed %q, want %q", i+1, resp.Content, want)
		}
	}
}

// Making more calls than were recorded is an error, not an empty response. An
// empty response here would surface as a parse failure three layers away.
func TestRunningOutOfCassettesIsAnError(t *testing.T) {
	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), t.TempDir())
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	player := schemafluxtest.ReplayFrom(recorder.Recorded()...)
	if _, err := player.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("first replay: %v", err)
	}

	_, err := player.Complete(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a second call replayed successfully against a single cassette")
	}
	if !strings.Contains(err.Error(), "only 1 were recorded") {
		t.Errorf("the error does not say how many were recorded: %v", err)
	}
}

// An empty directory is a mistake — usually a path typo or a fixture that was
// never committed — and silently producing a player that fails on first use
// would report it in the wrong place.
func TestReplayingAnEmptyDirectoryIsAnError(t *testing.T) {
	if _, err := schemafluxtest.Replay(t.TempDir()); err == nil {
		t.Fatal("an empty directory produced a usable player")
	} else if !strings.Contains(err.Error(), "no cassettes") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestReplayingAMissingDirectoryIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created")
	if _, err := schemafluxtest.Replay(missing); err == nil {
		t.Fatal("a missing directory produced a usable player")
	}
}

// Saving nothing writes nothing, rather than creating an empty directory that
// a later Replay would reject with a confusing "no cassettes" error.
func TestSavingNothingWritesNothing(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "unused")
	recorder := schemafluxtest.Record(succeedingFake(""), directory)

	if err := recorder.Save(); err != nil {
		t.Fatalf("Save with nothing recorded: %v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("Save created %s despite recording nothing (err=%v)", directory, err)
	}
}

// The recorder is a pass-through, so anything the runtime asks the wrapped
// provider has to reach it. A recorder that reports its own retry policy would
// change the behaviour it was installed to observe.
func TestRecorderDelegatesToTheWrappedProvider(t *testing.T) {
	inner := &cassetteFake{
		name:    "anthropic",
		cost:    0.0042,
		retries: 5,
		backoff: 250 * time.Millisecond,
	}
	recorder := schemafluxtest.Record(inner, t.TempDir())

	if got := recorder.Name(); got != "anthropic" {
		t.Errorf("Name() = %q, want the wrapped provider's", got)
	}
	if got := recorder.EstimateCost(sampleRequest()); got != 0.0042 {
		t.Errorf("EstimateCost() = %v, want the wrapped provider's", got)
	}
	retries, backoff := recorder.RetryPolicy()
	if retries != 5 || backoff != 250*time.Millisecond {
		t.Errorf("RetryPolicy() = (%d, %v), want the wrapped provider's", retries, backoff)
	}
}

// The player is a provider a test installs, so it has to satisfy the interface
// the library resolves — and report zero cost, because a replay spends nothing.
func TestPlayerIsAProviderThatSpendsNothing(t *testing.T) {
	var provider schemaflux.Provider = schemafluxtest.ReplayFrom(schemafluxtest.Cassette{
		Provider: "openai",
		Response: &schemafluxtest.CassetteResponse{Content: "{}"},
	})

	if cost := provider.EstimateCost(sampleRequest()); cost != 0 {
		t.Errorf("EstimateCost() = %v, want 0", cost)
	}
	retries, backoff := provider.RetryPolicy()
	if retries != 0 || backoff != 0 {
		t.Errorf("RetryPolicy() = (%d, %v), want no retries: a replay that fails will fail again", retries, backoff)
	}
}

// A cassette recording neither outcome is a corrupt fixture, and saying so is
// better than returning an empty response that fails somewhere else.
func TestACassetteWithNeitherOutcomeIsAnError(t *testing.T) {
	player := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{Provider: "openai"}).Lenient()

	_, err := player.Complete(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a cassette with neither a response nor a failure replayed successfully")
	}
	if !strings.Contains(err.Error(), "neither a response nor a failure") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// Redaction runs on the prompt and response fields; Provider and Model are
// not prompt content and are not redacted. If a credential ever ends up
// there -- a provider misreporting its own model name, say -- Save must
// still refuse rather than write it, because the redaction pass never saw
// it in the first place.
func TestSaveRefusesACredentialInAnUnredactedField(t *testing.T) {
	directory := t.TempDir()

	inner := &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{{
			response: schemaflux.CompletionResponse{Content: "ok"},
		}},
	}
	recorder := schemafluxtest.Record(inner, directory)

	req := sampleRequest()
	req.Model = "sk-ant-api03-LEAKEDLEAKEDLEAKEDLEAKEDLEAK" // secret-scan: allow
	if _, err := recorder.Complete(context.Background(), req); err != nil {
		t.Fatalf("recording: %v", err)
	}

	err := recorder.Save()
	if err == nil {
		t.Fatal("Save wrote a cassette with a credential-shaped model field")
	}
	if !strings.Contains(err.Error(), "refusing to write") {
		t.Errorf("the error does not explain the refusal: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(directory, "000.json")); statErr == nil {
		t.Error("Save left a file on disk despite refusing to write it")
	}
}

// The refusal names the shape it found, and names the right one: an Anthropic
// key is also a valid OpenAI-shaped key, so the narrower pattern has to be
// tried first or the report points the reader at the wrong provider.
func TestSaveRefusalNamesTheCredentialShape(t *testing.T) {
	cases := []struct {
		name  string
		model string
		label string
	}{
		{"an Anthropic key", "sk-ant-" + "api03-LEAKEDLEAKEDLEAKEDLEAKEDLEAK", "Anthropic key"},
		{"an OpenAI key", "sk-" + "LEAKEDLEAKEDLEAKEDLEAKEDLEAK", "OpenAI key"},
		{"an OpenAI project key", "sk-" + "proj-LEAKEDLEAKEDLEAKEDLEAKEDLEAK", "OpenAI key"},
		{"a Google key", "AIza" + "SyLEAKEDLEAKEDLEAKEDLEAKEDLEAKEDLEAKED", "Google API key"},
		{"an AWS access key ID", "AKIA" + "IOSFODNN7EXAMPLE", "AWS access key ID"},
		{"a GitHub token", "ghp_" + "LEAKEDLEAKEDLEAKEDLEAKEDLEAKEDLEAKED", "GitHub token"},
		{"a Slack token", "xoxb-" + "1234567890-LEAKEDLEAKED", "Slack token"},
		{"a bearer header", "Authorization: Bearer " + "abc.def.ghi", "bearer header"},
		{"a private key header", "-----BEGIN " + "RSA PRIVATE KEY-----", "private key block"},
		{"an OpenSSH private key header", "-----BEGIN " + "OPENSSH PRIVATE KEY-----", "private key block"},
		{"a PGP private key header", "-----BEGIN " + "PGP PRIVATE KEY BLOCK-----", "private key block"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			recorder := schemafluxtest.Record(succeedingFake("ok"), directory)

			req := sampleRequest()
			req.Model = tc.model
			if _, err := recorder.Complete(context.Background(), req); err != nil {
				t.Fatalf("recording: %v", err)
			}

			err := recorder.Save()
			if err == nil {
				t.Fatalf("Save wrote a cassette carrying %s in an unredacted field", tc.name)
			}
			if !strings.Contains(err.Error(), "shaped like a "+tc.label) {
				t.Errorf("the refusal does not name %q: %v", tc.label, err)
			}
		})
	}
}

// A recorded failure's message is redacted the same way a response's
// content is -- the classification survives, the credential does not.
func TestSaveRedactsARecordedFailuresMessage(t *testing.T) {
	directory := t.TempDir()

	inner := &cassetteFake{
		name: "openai",
		answers: []cassetteAnswer{{
			err: &types.OperationError{
				Kind:    types.KindRateLimited,
				Message: "the client sent sk-proj-FAILUREPATHLEAKFAILUREPATHLEAK, 429", // secret-scan: allow
			},
		}},
	}
	recorder := schemafluxtest.Record(inner, directory)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err == nil {
		t.Fatal("expected the scripted failure")
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
	if err != nil {
		t.Fatalf("reading the written cassette: %v", err)
	}
	if strings.Contains(string(raw), fakeProjKey) {
		t.Error("a credential in a failure message reached disk")
	}
	if !strings.Contains(string(raw), "[redacted by schemafluxtest]") {
		t.Error("the failure message was not redacted at all")
	}
	if !strings.Contains(string(raw), "429") {
		t.Error("redaction removed more of the failure message than the credential")
	}
}

// A private key is redacted whole. Matching only the BEGIN line left the
// base64 body, which is the secret, on disk beside a marker that said it had
// been handled.
func TestSaveRedactsAWholePrivateKeyBlock(t *testing.T) {
	const body = "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu"
	begin := func(kind string) string { return "-----BEGIN " + kind + "-----" }
	end := func(kind string) string { return "-----END " + kind + "-----" }

	cases := []struct {
		name string
		text string
		// kept is text outside the key that must survive.
		kept string
	}{
		{"an RSA key", "see " + begin("RSA PRIVATE KEY") + "\n" + body + "\n" + end("RSA PRIVATE KEY") + "\nthanks", "thanks"},
		{"a plain key", begin("PRIVATE KEY") + "\n" + body + "\n" + end("PRIVATE KEY"), ""},
		{"an EC key", begin("EC PRIVATE KEY") + "\n" + body + "\n" + end("EC PRIVATE KEY"), ""},
		{"an OpenSSH key", begin("OPENSSH PRIVATE KEY") + "\n" + body + "\n" + end("OPENSSH PRIVATE KEY"), ""},
		{"an encrypted key", begin("ENCRYPTED PRIVATE KEY") + "\n" + body + "\n" + end("ENCRYPTED PRIVATE KEY"), ""},
		{"a PGP key", begin("PGP PRIVATE KEY BLOCK") + "\n" + body + "\n" + end("PGP PRIVATE KEY BLOCK"), ""},
		{"several body lines", begin("RSA PRIVATE KEY") + "\n" + body + "\n" + body + "\n" + body + "\n" + end("RSA PRIVATE KEY") + " done", "done"},
		{"windows line endings", begin("RSA PRIVATE KEY") + "\r\n" + body + "\r\n" + end("RSA PRIVATE KEY"), ""},
		{"two keys in one text", begin("RSA PRIVATE KEY") + "\n" + body + "\n" + end("RSA PRIVATE KEY") + " and " +
			begin("EC PRIVATE KEY") + "\n" + body + "\n" + end("EC PRIVATE KEY") + " both", "both"},
		{"a block with no end line", "key follows " + begin("RSA PRIVATE KEY") + "\n" + body + "\n" + body, "key follows"},
		{"a header alone", "the file starts with " + begin("RSA PRIVATE KEY"), "the file starts with"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			inner := &cassetteFake{
				name:    "openai",
				answers: []cassetteAnswer{{response: schemaflux.CompletionResponse{Content: tc.text}}},
			}
			recorder := schemafluxtest.Record(inner, directory)
			if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
				t.Fatalf("recording: %v", err)
			}
			if err := recorder.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}

			raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			written := string(raw)
			if strings.Contains(written, body[:24]) {
				t.Errorf("the key material reached the disk:\n%s", written)
			}
			if strings.Contains(written, "PRIVATE KEY") {
				t.Errorf("part of the key block reached the disk:\n%s", written)
			}
			if !strings.Contains(written, "[redacted by schemafluxtest]") {
				t.Error("nothing was redacted")
			}
			if tc.kept != "" && !strings.Contains(written, tc.kept) {
				t.Errorf("redaction removed the text around the key; %q is gone", tc.kept)
			}
		})
	}
}

// Save refuses to create the cassette directory when a plain file already
// occupies that path, rather than silently writing nothing.
func TestSaveFailsWhenTheDirectoryPathIsAFile(t *testing.T) {
	parent := t.TempDir()
	blocked := filepath.Join(parent, "cassettes")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), blocked)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if err := recorder.Save(); err == nil {
		t.Fatal("Save succeeded despite the target path being a file, not a directory")
	}
}

// Save fails cleanly when the target file's own path is blocked by an
// existing directory of the same name, rather than corrupting whatever is
// there.
func TestSaveFailsWhenTheTargetFileIsBlocked(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "000.json"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), directory)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}

	if err := recorder.Save(); err == nil {
		t.Fatal("Save succeeded despite 000.json being a directory")
	}
}

// Save replaces the directory's numbered cassettes. A run that records fewer
// exchanges than an earlier one used to overwrite the first few and leave the
// rest, which Replay then loaded and played as if they belonged to this run.
// Only numbered .json files go; anything else in the directory is somebody
// else's.
func TestSaveRemovesStaleNumberedCassettes(t *testing.T) {
	cases := []struct {
		name    string
		isDir   bool
		removed bool
	}{
		{"001.json", false, true},
		{"002.json", false, true},
		{"004.json", false, true},
		{"999.json", false, true},
		{"1000.json", false, true},
		{"0001.json", false, true},
		{"7.json", false, true},
		{"notes.json", false, false},
		{"a001.json", false, false},
		{"00x.json", false, false},
		{".json", false, false},
		{"001.json.bak", false, false},
		{"000.txt", false, false},
		{"README", false, false},
		{"005.json", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, tc.name)
			if tc.isDir {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatalf("setup: %v", err)
				}
			} else if err := os.WriteFile(path, []byte(`{"stale":true}`), 0o644); err != nil {
				t.Fatalf("setup: %v", err)
			}

			recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), directory)
			if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
				t.Fatalf("recording: %v", err)
			}
			if err := recorder.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}

			_, err := os.Stat(path)
			if tc.removed && !os.IsNotExist(err) {
				t.Errorf("%s survived the save (stat err = %v)", tc.name, err)
			}
			if !tc.removed && err != nil {
				t.Errorf("%s was removed: %v", tc.name, err)
			}

			raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
			if err != nil || !strings.Contains(string(raw), "billing") {
				t.Errorf("000.json is not the new cassette: %v %s", err, raw)
			}
		})
	}
}

// A refused save changes nothing: the old cassettes are only removed once
// every new one has been encoded and checked.
func TestRefusedSaveLeavesTheExistingCassettesAlone(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"000.json", "001.json", "002.json"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(`{"old":true}`), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	recorder := schemafluxtest.Record(succeedingFake("ok"), directory)
	req := sampleRequest()
	req.Model = "sk-" + "proj-LEAKEDLEAKEDLEAKEDLEAKEDLEAK"
	if _, err := recorder.Complete(context.Background(), req); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if err := recorder.Save(); err == nil {
		t.Fatal("Save wrote a cassette carrying a credential")
	}

	for _, name := range []string{"000.json", "001.json", "002.json"} {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(raw) != `{"old":true}` {
			t.Errorf("%s changed by a refused save: %v %q", name, err, raw)
		}
	}
}

// writeCassetteFile writes a minimal success cassette whose content is the
// given text, under the given file name.
func writeCassetteFile(t *testing.T, directory, name, content string) {
	t.Helper()
	raw, err := json.Marshal(schemafluxtest.Cassette{
		Provider: "openai",
		Model:    "m",
		Response: &schemafluxtest.CassetteResponse{Content: content},
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), raw, 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// Replay plays files in the order they were numbered, by value: "1000.json"
// follows "999.json", where a plain string sort puts it before "101.json".
// Names that are not numbers follow the numbered ones, in string order.
func TestReplayOrdersFilesByNumber(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string // the stems, in play order
	}{
		{"padded names", []string{"002", "000", "001"}, []string{"000", "001", "002"}},
		{"unpadded names", []string{"10", "2", "1"}, []string{"1", "2", "10"}},
		{"across a thousand", []string{"1000", "999", "1001"}, []string{"999", "1000", "1001"}},
		{"mixed padding", []string{"0001", "2", "03"}, []string{"0001", "2", "03"}},
		{"names that are not numbers", []string{"b", "a"}, []string{"a", "b"}},
		{"numbers before names", []string{"x", "1", "2"}, []string{"1", "2", "x"}},
		{"nines against tens", []string{"10", "9"}, []string{"9", "10"}},
		{"equal values keep string order", []string{"1", "001"}, []string{"001", "1"}},
		{"one file", []string{"7"}, []string{"7"}},
		{"three digits against four", []string{"100", "99", "101", "1000"}, []string{"99", "100", "101", "1000"}},
		{"a number too large for any integer", []string{"99999999999999999999999", "1"}, []string{"1", "99999999999999999999999"}},
		{"leading zeros on a large number", []string{"0000000000000000000000000002", "10"}, []string{"0000000000000000000000000002", "10"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			for _, stem := range tc.files {
				writeCassetteFile(t, directory, stem+".json", stem)
			}

			player, err := schemafluxtest.Replay(directory)
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			player.Lenient()
			for i, want := range tc.want {
				resp, err := player.Complete(context.Background(), sampleRequest())
				if err != nil {
					t.Fatalf("call %d: %v", i+1, err)
				}
				if resp.Content != want {
					t.Errorf("call %d replayed %q, want %q", i+1, resp.Content, want)
				}
			}
		})
	}
}

// Past a thousand exchanges the file names grow a digit. Saving and replaying
// them has to keep the calls in order.
func TestMoreThanAThousandCassettesReplayInOrder(t *testing.T) {
	directory := t.TempDir()
	const total = 1002

	recorder := schemafluxtest.Record(schemafluxtest.New().ReplyFunc(
		func(call int, _ schemaflux.CompletionRequest) (string, error) { return fmt.Sprint(call), nil }), directory)
	for i := 0; i < total; i++ {
		if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
			t.Fatalf("recording call %d: %v", i, err)
		}
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	player, err := schemafluxtest.Replay(directory)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if player.Remaining() != total {
		t.Fatalf("loaded %d cassettes, wrote %d", player.Remaining(), total)
	}
	for i := 0; i < total; i++ {
		resp, err := player.Complete(context.Background(), sampleRequest())
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if resp.Content != fmt.Sprint(i) {
			t.Fatalf("call %d replayed %q: the cassettes are out of order", i, resp.Content)
		}
	}
}

// A directory of cassettes may hold other files, a README or an editor's
// backup among them. Only the .json files are cassettes.
func TestReplayIgnoresFilesThatAreNotJSON(t *testing.T) {
	directory := t.TempDir()
	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), directory)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, name := range []string{"README.md", "notes.txt", "000.json.bak"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("not a cassette"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	player, err := schemafluxtest.Replay(directory)
	if err != nil {
		t.Fatalf("Replay failed on a directory that also holds other files: %v", err)
	}
	if player.Remaining() != 1 {
		t.Fatalf("loaded %d cassettes, want 1", player.Remaining())
	}
}

// A cassette file that is not valid JSON fails Replay with a message naming
// the file, rather than a bare json.Unmarshal error a caller cannot locate
// inside a directory of dozens.
func TestReplayFailsOnAMalformedCassetteFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "000.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := schemafluxtest.Replay(directory)
	if err == nil {
		t.Fatal("Replay accepted a malformed cassette file")
	}
	if !strings.Contains(err.Error(), "000.json") {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// Player.Name reports "local", the name reserved for the in-process double;
// any other name resolves to no model, which is FL-001 working as intended.
func TestPlayerNameIsLocal(t *testing.T) {
	player := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{
		Provider: "openai",
		Response: &schemafluxtest.CassetteResponse{Content: "{}"},
	})
	if got := player.Name(); got != "local" {
		t.Errorf("Player.Name() = %q, want \"local\"", got)
	}
}

// A classification stored under a name the current taxonomy does not
// recognise -- an old cassette outliving a renamed or removed kind -- falls
// back to Unknown rather than panicking or resolving to the wrong kind by
// coincidence.
func TestReplayOfAnUnrecognisedKindNameFallsBackToUnknown(t *testing.T) {
	for _, name := range []string{"SomeKindThatNoLongerExists", "", "unknown", "Rate Limited", " rate limited", "rate limited ", "RATE LIMITED"} {
		t.Run("name "+name, func(t *testing.T) {
			player := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{
				Provider: "openai",
				Failure:  &schemafluxtest.CassetteFailure{Message: "something failed", Kind: name},
			}).Lenient()

			_, err := player.Complete(context.Background(), sampleRequest())
			if err == nil {
				t.Fatal("a recorded failure must still fail on replay")
			}
			var opErr *types.OperationError
			if !errors.As(err, &opErr) {
				t.Fatalf("replayed failure is not an *types.OperationError: %T", err)
			}
			if opErr.Kind != types.KindUnknown {
				t.Errorf("Kind = %v, want KindUnknown for an unrecognised name", opErr.Kind)
			}
		})
	}
}

// Every kind the taxonomy defines replays under its own name. The lookup used
// to be a loop bounded by the last kind somebody remembered to name, so a kind
// added after it replayed as unknown without anything failing.
func TestEveryKindReplaysUnderItsOwnName(t *testing.T) {
	tested := 0
	for kind := types.KindUnknown + 1; kind < 256; kind++ {
		name := kind.String()
		if name == types.KindUnknown.String() {
			continue
		}
		tested++
		t.Run(name, func(t *testing.T) {
			player := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{
				Failure: &schemafluxtest.CassetteFailure{Message: "failed", Kind: name},
			}).Lenient()

			_, err := player.Complete(context.Background(), sampleRequest())
			var opErr *types.OperationError
			if !errors.As(err, &opErr) {
				t.Fatalf("replayed failure is not an *types.OperationError: %v", err)
			}
			if opErr.Kind != kind {
				t.Errorf("Kind = %v, want %v", opErr.Kind, kind)
			}
		})
	}
	if tested < 23 {
		t.Errorf("only %d kinds have a name; the taxonomy defined 23 when this was written", tested)
	}
}

// A strict replay compares every part of the request that changes the answer,
// not only the two prompts: a different model, response format or schema
// is a different question, and a recording of one does not answer another.
func TestStrictReplayComparesEverythingThatChangesTheAnswer(t *testing.T) {
	withoutSchema := sampleRequest()
	withoutSchema.JSONSchema = nil

	cases := []struct {
		name     string
		recorded schemaflux.CompletionRequest
		replayed func(req *schemaflux.CompletionRequest)
		lenient  bool
		wantErr  string // empty when the replay must succeed
	}{
		{"an identical request", sampleRequest(), func(*schemaflux.CompletionRequest) {}, false, ""},
		{"a different model", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.Model = "gpt-5.6-luna" }, false, "different model"},
		{"no model", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.Model = "" }, false, "different model"},
		{"a different response format", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.ResponseFormat = "json_schema" }, false, "different response format"},
		{"no response format", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.ResponseFormat = "" }, false, "different response format"},
		{"a different schema name", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.SchemaName = "other" }, false, "different schema name"},
		{"no schema name", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.SchemaName = "" }, false, "different schema name"},
		{"a schema dropped", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.JSONSchema = nil }, false, "different JSON schema"},
		{"a schema added", withoutSchema, func(r *schemaflux.CompletionRequest) { r.JSONSchema = map[string]any{"type": "object"} }, false, "different JSON schema"},
		{"a different schema of the same kind", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.JSONSchema = map[string]any{"type": "array"} }, false, ""},
		{"a different system prompt", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.SystemPrompt += " Be brief." }, false, "different system prompt"},
		{"a different user prompt", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.UserPrompt = "other" }, false, "different user prompt"},
		{"no schema anywhere", withoutSchema, func(*schemaflux.CompletionRequest) {}, false, ""},
		{"every field changed, leniently", sampleRequest(), func(r *schemaflux.CompletionRequest) {
			r.Model, r.ResponseFormat, r.SchemaName, r.JSONSchema = "m", "text", "n", nil
			r.SystemPrompt, r.UserPrompt = "s", "u"
		}, true, ""},
		{"a different model, leniently", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.Model = "gpt-5.6-luna" }, true, ""},
		{"a different schema, leniently", sampleRequest(), func(r *schemaflux.CompletionRequest) { r.JSONSchema = nil }, true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), t.TempDir())
			if _, err := recorder.Complete(context.Background(), tc.recorded); err != nil {
				t.Fatalf("recording: %v", err)
			}

			player := schemafluxtest.ReplayFrom(recorder.Recorded()...)
			if tc.lenient {
				player.Lenient()
			}
			replayed := tc.recorded
			tc.replayed(&replayed)

			resp, err := player.Complete(context.Background(), replayed)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("the replay was refused: %v", err)
				}
				if resp.Content != `{"queue":"billing"}` {
					t.Errorf("content = %q", resp.Content)
				}
				return
			}
			if err == nil {
				t.Fatalf("a request with %s replayed successfully", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to say %q", err, tc.wantErr)
			}
		})
	}
}

// The recorded request's strict fields survive the trip through the disk too.
func TestStrictReplayFromDiskComparesTheModelAndSchema(t *testing.T) {
	directory := t.TempDir()
	recorder := schemafluxtest.Record(succeedingFake(`{"queue":"billing"}`), directory)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	changed := sampleRequest()
	changed.Model = "gpt-5.6-luna"
	player, err := schemafluxtest.Replay(directory)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if _, err := player.Complete(context.Background(), changed); err == nil {
		t.Error("a changed model replayed from disk without complaint")
	}
}

// A replay honours the caller's context like the real provider does: a cancelled
// or expired one is reported and no cassette is consumed.
func TestReplayHonoursTheContext(t *testing.T) {
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	expired := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}
	success := schemafluxtest.Cassette{
		Provider: "openai", Model: "gpt-5.6-sol",
		Request:  schemafluxtest.CassetteRequest{SystemPrompt: sampleRequest().SystemPrompt, UserPrompt: sampleRequest().UserPrompt, ResponseFormat: "json_object", SchemaName: "triage", HasJSONSchema: true},
		Response: &schemafluxtest.CassetteResponse{Content: "answer"},
	}
	failure := success
	failure.Response = nil
	failure.Failure = &schemafluxtest.CassetteFailure{Message: "boom", Kind: types.KindRateLimited.String()}
	corrupt := success
	corrupt.Response = nil

	cases := []struct {
		name      string
		cassettes []schemafluxtest.Cassette
		ctx       func() context.Context
		lenient   bool
		change    func(*schemaflux.CompletionRequest)
		wantErr   error // nil when the call must succeed
	}{
		{"a cancelled context", []schemafluxtest.Cassette{success}, cancelled, false, nil, context.Canceled},
		{"a cancelled context, leniently", []schemafluxtest.Cassette{success}, cancelled, true, nil, context.Canceled},
		{"an expired deadline", []schemafluxtest.Cassette{success}, expired, false, nil, context.DeadlineExceeded},
		{"a cancelled context over a recorded failure", []schemafluxtest.Cassette{failure}, cancelled, false, nil, context.Canceled},
		{"a cancelled context with nothing left to play", nil, cancelled, false, nil, context.Canceled},
		{"a cancelled context over a changed prompt", []schemafluxtest.Cassette{success}, cancelled, false,
			func(r *schemaflux.CompletionRequest) { r.UserPrompt = "other" }, context.Canceled},
		{"a cancelled context over a corrupt cassette", []schemafluxtest.Cassette{corrupt}, cancelled, false, nil, context.Canceled},
		{"a live context", []schemafluxtest.Cassette{success}, context.Background, false, nil, nil},
		{"a context carrying a value", []schemafluxtest.Cassette{success},
			func() context.Context { return context.WithValue(context.Background(), struct{}{}, 1) }, false, nil, nil},
		{"a deadline far ahead", []schemafluxtest.Cassette{success},
			func() context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
				t.Cleanup(cancel)
				return ctx
			}, false, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			player := schemafluxtest.ReplayFrom(tc.cassettes...)
			if tc.lenient {
				player.Lenient()
			}
			req := sampleRequest()
			if tc.change != nil {
				tc.change(&req)
			}

			resp, err := player.Complete(tc.ctx(), req)
			if tc.wantErr == nil {
				if err != nil || resp.Content != "answer" {
					t.Fatalf("got (%q, %v), want the recorded answer", resp.Content, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if player.Remaining() != len(tc.cassettes) {
				t.Errorf("a refused call consumed a cassette: %d of %d remain", player.Remaining(), len(tc.cassettes))
			}
		})
	}

	t.Run("the cassette a cancelled call skipped answers the next live one", func(t *testing.T) {
		player := schemafluxtest.ReplayFrom(success)
		for i := 0; i < 3; i++ {
			if _, err := player.Complete(cancelled(), sampleRequest()); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled call %d: %v", i, err)
			}
		}
		resp, err := player.Complete(context.Background(), sampleRequest())
		if err != nil || resp.Content != "answer" {
			t.Fatalf("the live call after three cancelled ones got (%q, %v)", resp.Content, err)
		}
	})
}

// Everything on a response that changes what the caller does with it is
// recorded and replayed: a tool-call answer used to replay as an empty one
// with a finish reason of "tool_calls".
func TestToolCallsAndServingFieldsSurviveRecordingAndReplay(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "call_1", Name: "lookup_order", Arguments: json.RawMessage(`{"order":"A-17"}`)},
		{ID: "call_2", Name: "refund", Arguments: json.RawMessage(`{"amount":12.5,"currency":"EUR"}`)},
	}
	answer := schemaflux.CompletionResponse{
		Content:          "",
		Provider:         "openrouter",
		Model:            "gpt-5.6-sol",
		FinishReason:     "tool_calls",
		UpstreamProvider: "Together",
		ServiceTier:      "flex",
		ToolCalls:        calls,
		Usage:            schemaflux.TokenUsage{PromptTokens: 9, CompletionTokens: 3, TotalTokens: 12},
	}

	check := func(t *testing.T, got schemaflux.CompletionResponse) {
		t.Helper()
		if got.FinishReason != "tool_calls" {
			t.Errorf("FinishReason = %q", got.FinishReason)
		}
		if got.UpstreamProvider != "Together" {
			t.Errorf("UpstreamProvider = %q", got.UpstreamProvider)
		}
		if got.ServiceTier != "flex" {
			t.Errorf("ServiceTier = %q", got.ServiceTier)
		}
		if got.Usage != answer.Usage {
			t.Errorf("Usage = %+v", got.Usage)
		}
		if len(got.ToolCalls) != len(calls) {
			t.Fatalf("replayed %d tool calls, recorded %d", len(got.ToolCalls), len(calls))
		}
		for i, want := range calls {
			have := got.ToolCalls[i]
			if have.ID != want.ID || have.Name != want.Name || string(have.Arguments) != string(want.Arguments) {
				t.Errorf("tool call %d = %+v, want %+v", i, have, want)
			}
		}
	}

	t.Run("in memory", func(t *testing.T) {
		recorder := schemafluxtest.Record(&cassetteFake{name: "openrouter", answers: []cassetteAnswer{{response: answer}}}, t.TempDir())
		if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
			t.Fatalf("recording: %v", err)
		}
		got, err := schemafluxtest.ReplayFrom(recorder.Recorded()...).Complete(context.Background(), sampleRequest())
		if err != nil {
			t.Fatalf("replaying: %v", err)
		}
		check(t, got)
	})

	t.Run("through the disk", func(t *testing.T) {
		directory := t.TempDir()
		recorder := schemafluxtest.Record(&cassetteFake{name: "openrouter", answers: []cassetteAnswer{{response: answer}}}, directory)
		if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
			t.Fatalf("recording: %v", err)
		}
		if err := recorder.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		player, err := schemafluxtest.Replay(directory)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		got, err := player.Complete(context.Background(), sampleRequest())
		if err != nil {
			t.Fatalf("replaying: %v", err)
		}
		check(t, got)
	})

	t.Run("a response with no tool calls writes none", func(t *testing.T) {
		directory := t.TempDir()
		recorder := schemafluxtest.Record(succeedingFake("plain"), directory)
		if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
			t.Fatalf("recording: %v", err)
		}
		if err := recorder.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		if strings.Contains(string(raw), "tool_calls") {
			t.Errorf("a plain answer carries a tool_calls field:\n%s", raw)
		}
	})

	t.Run("a caller editing a replayed argument does not change the recording", func(t *testing.T) {
		recorder := schemafluxtest.Record(&cassetteFake{name: "openrouter", answers: []cassetteAnswer{{response: answer}}}, t.TempDir())
		if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
			t.Fatalf("recording: %v", err)
		}
		player := schemafluxtest.ReplayFrom(recorder.Recorded()...)
		first, _ := player.Complete(context.Background(), sampleRequest())
		first.ToolCalls[0].Arguments[2] = 'X'

		second, err := schemafluxtest.ReplayFrom(recorder.Recorded()...).Complete(context.Background(), sampleRequest())
		if err != nil {
			t.Fatalf("replaying: %v", err)
		}
		if string(second.ToolCalls[0].Arguments) != `{"order":"A-17"}` {
			t.Errorf("the recording was changed through a replayed value: %s", second.ToolCalls[0].Arguments)
		}
	})
}

// Tool-call arguments carry the caller's data, so they are redacted like any
// other recorded text, and what is written stays valid JSON.
func TestSaveRedactsCredentialsInToolCallArguments(t *testing.T) {
	directory := t.TempDir()
	secret := "sk-ant-" + "api03-ZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	inner := &cassetteFake{name: "openai", answers: []cassetteAnswer{{response: schemaflux.CompletionResponse{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID: "call_1", Name: "configure",
			Arguments: json.RawMessage(`{"api_key":"` + secret + `","region":"eu"}`),
		}},
	}}}}
	recorder := schemafluxtest.Record(inner, directory)
	if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if strings.Contains(string(raw), secret[:20]) {
		t.Errorf("a credential in tool-call arguments reached the disk:\n%s", raw)
	}

	player, err := schemafluxtest.Replay(directory)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	got, err := player.Complete(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("replayed %d tool calls, recorded 1", len(got.ToolCalls))
	}
	if !json.Valid(got.ToolCalls[0].Arguments) {
		t.Errorf("the redacted arguments are not valid JSON: %s", got.ToolCalls[0].Arguments)
	}
	if !strings.Contains(string(got.ToolCalls[0].Arguments), `"region":"eu"`) {
		t.Errorf("redaction removed more than the credential: %s", got.ToolCalls[0].Arguments)
	}
}

// A tool call's id and name are text a provider chose and the caller may have
// echoed, so a credential in either is redacted too. The key pattern needs a
// non-word character before it, so the secret follows a space.
func TestSaveRedactsCredentialsInAToolCallsIDAndName(t *testing.T) {
	secret := "sk-ant-" + "api03-ZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	for name, call := range map[string]llm.ToolCall{
		"id":   {ID: "call 1 " + secret, Name: "configure", Arguments: json.RawMessage(`{}`)},
		"name": {ID: "call_1", Name: "configure " + secret, Arguments: json.RawMessage(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			inner := &cassetteFake{name: "openai", answers: []cassetteAnswer{{response: schemaflux.CompletionResponse{
				FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{call},
			}}}}
			recorder := schemafluxtest.Record(inner, directory)
			if _, err := recorder.Complete(context.Background(), sampleRequest()); err != nil {
				t.Fatalf("recording: %v", err)
			}
			if err := recorder.Save(); err != nil {
				t.Fatalf("Save refused the cassette instead of redacting it: %v", err)
			}

			raw, err := os.ReadFile(filepath.Join(directory, "000.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), secret[:20]) {
				t.Errorf("a credential in the tool call's %s reached the disk:\n%s", name, raw)
			}
		})
	}
}

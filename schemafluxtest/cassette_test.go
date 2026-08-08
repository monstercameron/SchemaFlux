package schemafluxtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
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
				Content: "the operator pasted sk-ant-api03-ZZZZZZZZZZZZZZZZZZZZZZZZZZZZ into the ticket",
			},
		}},
	}
	recorder := schemafluxtest.Record(inner, directory)

	req := sampleRequest()
	req.SystemPrompt = "Use key sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA when calling out."
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
		{"OpenAI key", "sk-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", true},
		{"OpenAI project key", "sk-proj-BBBBBBBBBBBBBBBBBBBBBBBBBBBB", true},
		{"OpenAI service account key", "sk-svcacct-CCCCCCCCCCCCCCCCCCCCCCCCCCCC", true},
		{"Anthropic key", "sk-ant-api03-DDDDDDDDDDDDDDDDDDDDDDDDDD", true},
		{"Google API key", "AIzaSyDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", true},
		{"AWS access key ID", "AKIAIOSFODNN7EXAMPLE", true},
		{"AWS session key ID", "ASIAIOSFODNN7EXAMPLE", true},
		{"GitHub token", "ghp_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", true},
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

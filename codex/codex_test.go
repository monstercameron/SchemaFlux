package codex

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// PS-013. The codex transport's own tests.
//
// None of them start a process. The shell boundary is a function-typed field
// (Provider.turn), so everything above it -- argv assembly, environment
// stripping, event parsing, error classification, and the payload rules -- is
// exercised for real against scripted CLI output. The one place a real binary
// would be involved is New's PATH lookup, which the tests that need a Provider
// bypass by constructing one directly.

// scriptedTurn returns a shell boundary that answers with what it was given
// and records what it was asked, so a test can assert on both halves.
type scriptedTurn struct {
	transcript turnTranscript
	err        error

	// delay stands in for the time a real shell-out takes. It is not padding:
	// an instantaneous fake measures as exactly zero elapsed on a platform
	// whose clock granularity is coarser than the call, so a test asserting
	// that provider time was measured would fail on Windows for a reason that
	// has nothing to do with the code under test.
	delay time.Duration

	seen  turnRequest
	calls int
}

func (s *scriptedTurn) run(_ context.Context, request turnRequest) (turnTranscript, error) {
	s.calls++
	s.seen = request
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.transcript, s.err
}

func providerWith(script *scriptedTurn, options Options) *Provider {
	if options.Executable == "" {
		options.Executable = "codex-not-really-here"
	}
	return &Provider{options: options, turn: script.run}
}

// completedEventLine is a turn.completed event with usage, as the CLI writes it.
const completedEventLine = `{"type":"turn.completed","usage":{"input_tokens":120,` +
	`"cached_input_tokens":40,"output_tokens":31,"reasoning_output_tokens":7}}`

// ---------------------------------------------------------------------------
// The security rules. These two are the reason the transport is written the
// way it is, so they are asserted rather than described.
// ---------------------------------------------------------------------------

// The whole point of this transport is that it authenticates as the person who
// ran `codex login`. A child that inherited an API key could authenticate as
// that key instead -- billing an account nobody chose, with nothing in the
// answer saying which identity paid. The variables are removed so the fallback
// is impossible rather than unlikely.
func TestTheChildEnvironmentCannotFallBackToAPIKeyAuthentication(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		kept  bool
		why   string
	}{
		{name: "the OpenAI key", entry: "OPENAI_API_KEY=sk-secret", kept: false},
		{name: "the short OpenAI spelling", entry: "OPENAI_KEY=sk-secret", kept: false},
		{name: "the codex key", entry: "CODEX_API_KEY=sk-secret", kept: false},
		{
			name:  "a lowercase spelling",
			entry: "openai_api_key=sk-secret",
			kept:  false,
			why:   "Windows environment blocks are case-insensitive; the same variable spelled differently is the same variable",
		},
		{name: "a mixed-case spelling", entry: "OpenAI_Key=sk-secret", kept: false},
		{
			name:  "the codex login's home",
			entry: "CODEX_HOME=/home/someone/.codex",
			kept:  true,
			why:   "this is where the login this transport exists to use actually lives",
		},
		{name: "PATH", entry: "PATH=/usr/bin", kept: true},
		{
			name:  "an unrelated key-shaped variable",
			entry: "ANTHROPIC_API_KEY=sk-secret",
			kept:  true,
			why:   "not a variable the codex CLI authenticates with; removing it would be this package guessing at another tool's business",
		},
		{
			name:  "an entry that is not a pair",
			entry: "MALFORMED",
			kept:  true,
			why:   "not something this function is in a position to interpret, so it is passed through rather than dropped",
		},
		{
			name:  "a variable whose value mentions a key name",
			entry: "SOME_FLAG=OPENAI_API_KEY",
			kept:  true,
			why:   "the name is what identifies a variable; matching values would drop unrelated configuration",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := environmentWithoutAPIKeys([]string{"FIRST=1", testCase.entry, "LAST=2"})

			var present bool
			for _, entry := range result {
				if entry == testCase.entry {
					present = true
				}
			}
			if present != testCase.kept {
				verb := "was removed"
				if present {
					verb = "survived"
				}
				t.Errorf("%q %s; want kept=%v. %s", testCase.entry, verb, testCase.kept, testCase.why)
			}

			// The surrounding entries are never disturbed.
			if len(result) < 2 || result[0] != "FIRST=1" || result[len(result)-1] != "LAST=2" {
				t.Errorf("the surrounding environment was altered: %v", result)
			}
		})
	}
}

// The caller's prompt goes to the child on stdin and must never appear in the
// process argument list. argv is readable by any process listing on the
// machine, and a CLI that prints its own invocation on failure would put it
// into a diagnostic stream. This is what lets errors.go promise that a stderr
// echo of argv cannot leak the prompt.
func TestTheChildNeverReceivesThePromptOnArgv(t *testing.T) {
	const secret = "PATIENT-4815162342-DIAGNOSIS-CONFIDENTIAL"

	provider := &Provider{options: Options{Executable: "codex", ReasoningEffort: "medium"}}
	workspace := t.TempDir()

	request := turnRequest{
		model:  "gpt-5.6-sol",
		prompt: []byte("Summarise this: " + secret),
		schema: []byte(`{"type":"object","properties":{"summary":{"type":"string"}}}`),
	}

	arguments, err := provider.arguments(request, workspace, filepath.Join(workspace, "reply.txt"))
	if err != nil {
		t.Fatalf("arguments: %v", err)
	}

	for _, argument := range arguments {
		if strings.Contains(argument, secret) {
			t.Fatalf("the caller's prompt reached argv: %q", argument)
		}
	}

	// The trailing "-" is what makes the CLI read the task from stdin. Without
	// it the prompt would have to be an argument, so this is the assertion
	// that keeps the guarantee above true.
	if arguments[len(arguments)-1] != "-" {
		t.Errorf("argv does not end with the stdin marker: %v", arguments)
	}

	joined := strings.Join(arguments, " ")
	for _, required := range []string{
		"exec", "--json", "--sandbox read-only", "--skip-git-repo-check",
		"--ephemeral", "--ignore-user-config", "--model gpt-5.6-sol",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("argv is missing %q: %v", required, arguments)
		}
	}
}

func TestTheSchemaIsPassedAsAFileAndOmittedWhenAbsent(t *testing.T) {
	provider := &Provider{options: Options{Executable: "codex"}}
	workspace := t.TempDir()
	reply := filepath.Join(workspace, "reply.txt")

	withSchema, err := provider.arguments(
		turnRequest{model: "m", schema: []byte(`{"type":"object"}`)}, workspace, reply)
	if err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if !strings.Contains(strings.Join(withSchema, " "), "--output-schema") {
		t.Error("a request carrying a schema produced no --output-schema flag")
	}

	withoutSchema, err := provider.arguments(turnRequest{model: "m"}, workspace, reply)
	if err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if strings.Contains(strings.Join(withoutSchema, " "), "--output-schema") {
		t.Error("a request with no schema still named a schema file; the CLI would enforce an invented constraint")
	}
}

func TestAnUnpinnedModelLeavesTheFlagOffRatherThanSendingAnEmptyOne(t *testing.T) {
	provider := &Provider{options: Options{Executable: "codex"}}
	workspace := t.TempDir()

	arguments, err := provider.arguments(turnRequest{}, workspace, filepath.Join(workspace, "r.txt"))
	if err != nil {
		t.Fatalf("arguments: %v", err)
	}
	for index, argument := range arguments {
		if argument == "--model" {
			t.Fatalf("--model was passed with %q after it", arguments[index+1])
		}
	}
}

// ---------------------------------------------------------------------------
// Event parsing.
// ---------------------------------------------------------------------------

func TestUsageComesFromTheCompletedEvent(t *testing.T) {
	result := scanEvents([]byte(
		`{"type":"item.started","item":{"id":"1"}}` + "\n" +
			completedEventLine + "\n"))

	if !result.completed {
		t.Fatal("the completed event was not recognised")
	}
	if !result.usageReported {
		t.Fatal("usage was reported by the CLI but not recorded")
	}

	want := types.TokenUsage{
		PromptTokens: 120, CompletionTokens: 31, TotalTokens: 151,
		InputTokens: 120, OutputTokens: 31, CachedTokens: 40, ReasoningTokens: 7,
	}
	if result.usage != want {
		t.Errorf("usage = %+v, want %+v", result.usage, want)
	}
	if result.usage.CacheWriteTokens != 0 {
		t.Error("a cache-write count was invented; the CLI does not report one")
	}
}

// A turn that completed without reporting counts is not a turn that counted
// zero. types.TokenUsage has no field for the difference, so nothing may
// derive a total from counts nobody stated.
func TestACompletedTurnWithNoCountsReportsNothingRatherThanZero(t *testing.T) {
	result := scanEvents([]byte(`{"type":"turn.completed"}` + "\n"))

	if !result.completed {
		t.Fatal("the completed event was not recognised")
	}
	if result.usageReported {
		t.Error("usage was reported as measured when the event carried no counts")
	}
	if result.usage != (types.TokenUsage{}) {
		t.Errorf("usage = %+v, want the zero value", result.usage)
	}
}

func TestEventParsingIsToleranntOfWhatItDoesNotConsume(t *testing.T) {
	cases := []struct {
		name          string
		stream        string
		wantCompleted bool
		wantFailure   string
	}{
		{
			name:          "an unknown event type is ignored",
			stream:        `{"type":"codex.something.new","payload":{}}` + "\n" + completedEventLine,
			wantCompleted: true,
		},
		{
			name:          "a malformed line is skipped",
			stream:        "not json at all\n" + completedEventLine,
			wantCompleted: true,
		},
		{
			name:          "a blank line is skipped",
			stream:        "\n\n" + completedEventLine + "\n\n",
			wantCompleted: true,
		},
		{
			name:          "no completed event at all",
			stream:        `{"type":"item.started"}`,
			wantCompleted: false,
		},
		{
			name:        "a failed turn carries its message",
			stream:      `{"type":"turn.failed","error":{"message":"model overloaded"}}`,
			wantFailure: "model overloaded",
		},
		{
			name: "the first failure wins",
			stream: `{"type":"turn.failed","error":{"message":"first"}}` + "\n" +
				`{"type":"turn.failed","error":{"message":"second"}}`,
			wantFailure: "first",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := scanEvents([]byte(testCase.stream))
			if result.completed != testCase.wantCompleted {
				t.Errorf("completed = %v, want %v", result.completed, testCase.wantCompleted)
			}
			if result.failure != testCase.wantFailure {
				t.Errorf("failure = %q, want %q", result.failure, testCase.wantFailure)
			}
		})
	}
}

// A count that cannot be represented is not a measurement. Wrapping it into a
// small or negative int would put a number that looks measured into cost
// accounting.
func TestImpossibleCountsAreNotTreatedAsMeasurements(t *testing.T) {
	for _, stream := range []string{
		`{"type":"turn.completed","usage":{"input_tokens":-5}}`,
		`{"type":"turn.completed","usage":{"input_tokens":9223372036854775807}}`,
	} {
		result := scanEvents([]byte(stream))
		if !result.completed {
			t.Fatalf("the completed event was not recognised for %s", stream)
		}
		if result.usageReported {
			t.Errorf("an impossible count was accepted as measured: %s", stream)
		}
		if result.usage.PromptTokens != 0 {
			t.Errorf("prompt tokens = %d for %s, want 0", result.usage.PromptTokens, stream)
		}
	}
}

// A long assistant message arrives on one line. The scanner's default limit
// would treat it as malformed and skip it, silently losing the completed event
// and turning a good answer into a reported failure.
func TestAVeryLongEventLineIsStillParsed(t *testing.T) {
	padding := strings.Repeat("a", 200_000)
	stream := `{"type":"item.completed","text":"` + padding + `"}` + "\n" + completedEventLine

	if result := scanEvents([]byte(stream)); !result.completed {
		t.Error("a completed event after a very long line was lost")
	}
}

// ---------------------------------------------------------------------------
// What an error may say.
// ---------------------------------------------------------------------------

// ADR 0001's rule, applied one notch tighter for a transport whose output
// nobody has characterised: both the CLI's message and its stderr stay off the
// error string until a caller deliberately asks.
func TestAFailureQuotesNeitherTheCLIsMessageNorItsStderr(t *testing.T) {
	const leak = "MARKER-4815162342-DO-NOT-EXPORT"

	failure := &Error{
		Model:    "gpt-5.6-sol",
		ExitCode: 1,
		Failure:  "the model refused: " + leak,
		Stderr:   "codex: invoked with " + leak,
	}

	message := failure.Error()
	if strings.Contains(message, leak) {
		t.Errorf("Error() quoted withheld output: %q", message)
	}
	if !strings.Contains(message, "withheld") {
		t.Errorf("Error() does not say detail exists; that reads as the CLI explaining nothing: %q", message)
	}
	if !strings.Contains(message, llm.ErrorDetailEnvVar) {
		t.Errorf("Error() does not name the way to see the detail: %q", message)
	}

	// The escape hatch is a deliberate act, and it works.
	if detail := failure.Detail(); !strings.Contains(detail, leak) {
		t.Errorf("Detail() withheld the detail it exists to show: %q", detail)
	}
}

func TestTheDetailGateIsTheOneTheRestOfTheLibraryUses(t *testing.T) {
	t.Setenv(llm.ErrorDetailEnvVar, "1")

	failure := &Error{ExitCode: 1, Failure: "model overloaded"}
	if message := failure.Error(); !strings.Contains(message, "model overloaded") {
		t.Errorf("with the gate open the message is still withheld: %q", message)
	}
}

// The classified error a caller actually receives must not fall through to
// printing its cause -- types.OperationError appends the cause only when it
// carries no message of its own, so the message is always set.
func TestTheClassifiedErrorPrintsAKindAndNotTheCause(t *testing.T) {
	const leak = "MARKER-4815162342-DO-NOT-EXPORT"

	err := classified(types.KindProviderUnavailable, "gpt-5.6-sol",
		"the codex CLI reported a failed turn",
		&Error{ExitCode: 1, Failure: leak, Stderr: leak})

	if strings.Contains(err.Error(), leak) {
		t.Errorf("the classified error quoted the CLI: %q", err.Error())
	}
	if types.KindOf(err) != types.KindProviderUnavailable {
		t.Errorf("kind = %v, want provider unavailable", types.KindOf(err))
	}
	if !err.Retryable() {
		t.Error("a CLI failure is not retryable; a transient codex outage would fail the caller outright")
	}

	// A caller who wants the detail can still reach it.
	var detail *Error
	if !errors.As(err, &detail) {
		t.Fatal("the detail is not reachable from the classified error")
	}
	if detail.Failure != leak {
		t.Error("the detail was lost on the way out")
	}
}

// ---------------------------------------------------------------------------
// Complete, over a scripted CLI.
// ---------------------------------------------------------------------------

func TestACompletedTurnBecomesACompletionResponse(t *testing.T) {
	script := &scriptedTurn{transcript: turnTranscript{
		reply:    []byte("  {\"summary\":\"ok\"}  "),
		events:   []byte(completedEventLine),
		exitCode: 0,
	}}
	provider := providerWith(script, Options{Model: "gpt-5.6-sol"})

	response, err := provider.Complete(context.Background(), llm.CompletionRequest{
		Model:        "gpt-5.6-sol",
		SystemPrompt: "Return JSON.",
		UserPrompt:   "the input",
		JSONSchema:   map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if response.Content != `{"summary":"ok"}` {
		t.Errorf("content = %q", response.Content)
	}
	if response.Provider != providerName {
		t.Errorf("provider = %q, want codex", response.Provider)
	}
	if response.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want stop", response.FinishReason)
	}
	if response.Usage.TotalTokens != 151 {
		t.Errorf("usage total = %d, want 151", response.Usage.TotalTokens)
	}
	// Nobody observed which model served the turn, so nothing claims one.
	if response.Model != "" {
		t.Errorf("model = %q, want empty -- the CLI never said which model answered", response.Model)
	}

	// The prompt reached the child, both halves, system first.
	prompt := string(script.seen.prompt)
	if !strings.HasPrefix(prompt, "Return JSON.") || !strings.Contains(prompt, "the input") {
		t.Errorf("the assembled prompt is wrong: %q", prompt)
	}
	if len(script.seen.schema) == 0 {
		t.Error("the schema never reached the shell boundary")
	}
}

// A CLI that exits zero having printed no completed event has stopped early.
// Reporting its empty output as an empty answer would be failing open.
func TestAZeroExitWithNoCompletedEventIsAFailure(t *testing.T) {
	script := &scriptedTurn{transcript: turnTranscript{
		events:   []byte(`{"type":"item.started"}`),
		exitCode: 0,
	}}
	provider := providerWith(script, Options{})

	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{Model: "m"}); err == nil {
		t.Fatal("a turn that never completed was reported as a success")
	} else if types.KindOf(err) != types.KindProviderUnavailable {
		t.Errorf("kind = %v, want provider unavailable", types.KindOf(err))
	}
}

// A killed child produces a torn event stream. Classifying that on its
// contents would report a provider failure for the caller hanging up, and the
// two are retried differently.
func TestACancelledCallReportsCancellationNotAProviderFailure(t *testing.T) {
	script := &scriptedTurn{
		transcript: turnTranscript{events: []byte(`{"type":"item.started"}`), exitCode: -1},
		err:        errors.New("signal: killed"),
	}
	provider := providerWith(script, Options{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Complete(ctx, llm.CompletionRequest{Model: "m"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRetryPolicyFollowsTheDocumentedThreeWayContract(t *testing.T) {
	cases := []struct {
		name    string
		set     int
		want    int
		meaning string
	}{
		{name: "a count", set: 4, want: 4, meaning: "exactly that many retries"},
		{name: "none", set: 0, want: 0, meaning: "no retries; an embedder doing its own gets none here"},
		{name: "no opinion", set: -1, want: -1, meaning: "the library's default applies"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			provider := &Provider{options: Options{MaxRetries: testCase.set, RetryBackoff: time.Second}}
			retries, backoff := provider.RetryPolicy()
			if retries != testCase.want {
				t.Errorf("retries = %d, want %d (%s)", retries, testCase.want, testCase.meaning)
			}
			if backoff != time.Second {
				t.Errorf("backoff = %v, want 1s", backoff)
			}
		})
	}
}

// The subscription is flat-rate, so multiplying its token counts by any rate
// card produces a figure that is not the bill. Unpriced says the true thing.
func TestACodexTurnIsUnpricedRatherThanPricedAtAPIRates(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}

	cost := pricing.CalculateCost(&usage, "gpt-5.6-sol", providerName)
	if cost == nil {
		t.Fatal("CalculateCost returned nil")
	}
	if cost.Priced {
		t.Errorf("a codex turn was priced at %.6f; a flat-rate subscription has no per-token rate", cost.TotalCost)
	}
	if cost.TotalCost != 0 {
		t.Errorf("total cost = %f, want 0 alongside Priced=false", cost.TotalCost)
	}
	if cost.Quality != types.PricingUnknown {
		t.Errorf("quality = %v, want unknown", cost.Quality)
	}
}

func TestTheGeneratedSchemaIsEncodedAndAnUnschemaedRequestCarriesNone(t *testing.T) {
	encoded, err := encodeSchema(llm.CompletionRequest{
		JSONSchema: map[string]any{"type": "object", "required": []string{"a"}},
	})
	if err != nil {
		t.Fatalf("encodeSchema: %v", err)
	}
	var round map[string]any
	if json.Unmarshal(encoded, &round) != nil || round["type"] != "object" {
		t.Errorf("the schema did not round-trip: %s", encoded)
	}

	if encoded, err := encodeSchema(llm.CompletionRequest{}); err != nil || encoded != nil {
		t.Errorf("a request with no schema produced %q (err %v), want nil", encoded, err)
	}
}

func TestAMissingCLIIsAConfigurationErrorRatherThanATransientOne(t *testing.T) {
	_, err := New(Options{Executable: "definitely-not-a-real-binary-schemaflux-codex"})
	if err == nil {
		t.Fatal("a missing CLI was accepted")
	}
	if types.KindOf(err) != types.KindConfiguration {
		t.Errorf("kind = %v, want configuration -- a transient classification would be retried three times inside a request handler",
			types.KindOf(err))
	}
	if !strings.Contains(err.Error(), "codex login") {
		t.Errorf("the error does not name the way out: %q", err.Error())
	}
}

// LX-105. A caller's reasoning effort for one call replaces the effort the
// provider was configured with. The request's effort used to be ignored, so a
// caller who asked for more reasoning on a hard call got the configured amount
// and nothing said so.
func TestACallersEffortReplacesTheConfiguredOne(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		perCall    string
		want       string
	}{
		{"neither", "", "", ""},
		{"configured only", "medium", "", "medium"},
		{"per call only", "", "high", "high"},
		{"per call over configured", "medium", "high", "high"},
		{"per call lower than configured", "high", "low", "low"},
		{"per call none", "medium", "none", "none"},
		{"spaces around the per-call value", "medium", "  high ", "high"},
		{"spaces around the configured value", " low ", "", "low"},
		{"a blank per-call value falls back", "medium", "   ", "medium"},
		{"both blank", "  ", "  ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &Provider{options: Options{Executable: "codex", ReasoningEffort: tc.configured}}
			workspace := t.TempDir()
			arguments, err := provider.arguments(turnRequest{model: "m", effort: tc.perCall}, workspace, filepath.Join(workspace, "reply.txt"))
			if err != nil {
				t.Fatalf("arguments: %v", err)
			}
			joined := strings.Join(arguments, " ")
			if tc.want == "" {
				if strings.Contains(joined, "model_reasoning_effort") {
					t.Fatalf("an effort was passed with none set: %v", arguments)
				}
				return
			}
			if want := `model_reasoning_effort="` + tc.want + `"`; !strings.Contains(joined, want) {
				t.Fatalf("argv has no %s: %v", want, arguments)
			}
			if strings.Count(joined, "model_reasoning_effort") != 1 {
				t.Fatalf("the effort is passed more than once: %v", arguments)
			}
		})
	}
}

// The CLI takes one task on stdin and has no way to be handed tool declarations,
// a conversation or a search. A request that sets one used to be answered from
// its system and user prompts alone: a conversation held in Messages was answered
// as the empty user prompt, and tools the model was never offered came back as
// text with no calls in it. Such a request is refused before the CLI is run, and
// the error names the field and never its value, which is the caller's data.
func TestARequestThatSetsWhatTheCLICannotTakeIsRefusedBeforeItRuns(t *testing.T) {
	const secret = "CALLER-SECRET-5530"
	cases := []struct {
		name   string
		mutate func(*llm.CompletionRequest)
		fields []string
	}{
		{"tools", func(r *llm.CompletionRequest) { r.Tools = []llm.Tool{{Name: secret}} }, []string{"Tools"}},
		{"a tool choice", func(r *llm.CompletionRequest) { r.ToolChoice = secret }, []string{"ToolChoice"}},
		{"web search", func(r *llm.CompletionRequest) { r.WebSearch = true }, []string{"WebSearch"}},
		{"messages", func(r *llm.CompletionRequest) { r.Messages = []llm.Message{{Role: "user", Content: secret}} }, []string{"Messages"}},
		{"messages with no user prompt", func(r *llm.CompletionRequest) {
			r.UserPrompt = ""
			r.Messages = []llm.Message{{Role: "system", Content: "s"}, {Role: "user", Content: secret}}
		}, []string{"Messages"}},
		{"a tool result in the transcript", func(r *llm.CompletionRequest) {
			r.Messages = []llm.Message{{Role: "tool", Content: secret, ToolCallID: "call_1"}}
		}, []string{"Messages"}},
		{"tools and a tool choice", func(r *llm.CompletionRequest) {
			r.Tools = []llm.Tool{{Name: "t"}}
			r.ToolChoice = "required"
		}, []string{"Tools", "ToolChoice"}},
		{"tools and messages", func(r *llm.CompletionRequest) {
			r.Tools = []llm.Tool{{Name: "t"}}
			r.Messages = []llm.Message{{Role: "user", Content: "hi"}}
		}, []string{"Tools", "Messages"}},
		{"web search and messages", func(r *llm.CompletionRequest) {
			r.WebSearch = true
			r.Messages = []llm.Message{{Role: "user", Content: "hi"}}
		}, []string{"WebSearch", "Messages"}},
		{"all four", func(r *llm.CompletionRequest) {
			r.Tools = []llm.Tool{{Name: "t"}}
			r.ToolChoice = "auto"
			r.WebSearch = true
			r.Messages = []llm.Message{{Role: "user", Content: "hi"}}
		}, []string{"Tools", "ToolChoice", "WebSearch", "Messages"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := &scriptedTurn{transcript: turnTranscript{reply: []byte("ok"), events: []byte(completedEventLine)}}
			provider := providerWith(script, Options{Model: "gpt-5.6-sol"})
			req := llm.CompletionRequest{Model: "gpt-5.6-sol", SystemPrompt: "s", UserPrompt: "u"}
			tc.mutate(&req)

			response, err := provider.Complete(context.Background(), req)
			if err == nil {
				t.Fatalf("a request setting %v was run: %+v", tc.fields, response)
			}
			if response.Content != "" || response.Provider != "" {
				t.Errorf("a result came back beside the error: %+v", response)
			}
			if kind := types.KindOf(err); kind != types.KindUnsupportedCapability {
				t.Errorf("kind = %v, want %v (%v)", kind, types.KindUnsupportedCapability, err)
			}
			for _, field := range tc.fields {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("the error does not name %s: %v", field, err)
				}
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error carries the caller's value: %v", err)
			}
			if script.calls != 0 {
				t.Error("the CLI was run for a request it cannot answer")
			}
			if (&types.OperationError{Kind: llm.Classify(err)}).Retryable() {
				t.Error("a request the CLI cannot take is the same on retry")
			}
		})
	}
}

// What the CLI can carry is not refused: an empty tools list and an empty
// transcript set nothing, and a plain request still runs.
func TestARequestThatSetsOnlyWhatTheCLICanTakeStillRuns(t *testing.T) {
	cases := []struct {
		name string
		req  llm.CompletionRequest
	}{
		{"plain", llm.CompletionRequest{Model: "m", UserPrompt: "u"}},
		{"system and user", llm.CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u"}},
		{"an empty tools list", llm.CompletionRequest{Model: "m", UserPrompt: "u", Tools: []llm.Tool{}}},
		{"an empty transcript", llm.CompletionRequest{Model: "m", UserPrompt: "u", Messages: []llm.Message{}}},
		{"a schema", llm.CompletionRequest{Model: "m", UserPrompt: "u", JSONSchema: map[string]any{"type": "object"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := &scriptedTurn{transcript: turnTranscript{reply: []byte("ok"), events: []byte(completedEventLine)}}
			if _, err := providerWith(script, Options{}).Complete(context.Background(), tc.req); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if script.calls != 1 {
				t.Errorf("the CLI ran %d times, want 1", script.calls)
			}
		})
	}
}

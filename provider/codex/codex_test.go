package codex_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/provider/codex"
)

// kindOf is the kind of a *schemaflux.Error.
func kindOf(t testing.TB, err error) schemaflux.Kind {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a *schemaflux.Error: %T %v", err, err)
	}
	return e.Kind
}

// messagesIn is the Message of every *schemaflux.Error in the chain of err, so
// that a refusal is found whichever layer wrote its reason.
func messagesIn(err error) string {
	var all []string
	for ; err != nil; err = errors.Unwrap(err) {
		var e *schemaflux.Error
		if errors.As(err, &e) {
			all = append(all, e.Message)
		}
	}
	return strings.Join(all, " | ")
}

// build makes a provider that runs the fake.
func build(t testing.TB, opts codex.Options) schemaflux.Provider {
	t.Helper()
	opts.Executable = fakeExecutable(t)
	p, err := codex.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// ask makes a request whose system prompt carries the script.
func ask(p schemaflux.Provider, s script, req schemaflux.Request) (schemaflux.Response, error) {
	req.System = systemOf(s)
	if req.User == "" {
		req.User = "the user message"
	}
	return p.Complete(context.Background(), req)
}

func TestNewRefusesWhatItCanSeeToBeWrong(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts codex.Options
		say  string
	}{
		{"an effort that is not one", codex.Options{Effort: "turbo"}, "Options.Effort"},
		{"an effort in capitals", codex.Options{Effort: "HIGH"}, "Options.Effort"},
		{"an effort with a space", codex.Options{Effort: "high "}, "Options.Effort"},
		{"a negative backoff", codex.Options{RetryBackoff: -time.Second}, "RetryBackoff"},
		{"a model that is a flag", codex.Options{Model: "--dangerously-bypass-approvals-and-sandbox"}, "begins with a dash"},
		{"a model with a leading space and a dash", codex.Options{Model: "  -m"}, "begins with a dash"},
		{"a model with a newline", codex.Options{Model: "gpt\nother"}, "Options.Model"},
		{"a model with a NUL", codex.Options{Model: "gpt\x00"}, "Options.Model"},
		{"an executable with a newline", codex.Options{Executable: "codex\n"}, "Options.Executable"},
		{"an executable that is not installed", codex.Options{Executable: "schemaflux-no-such-codex-binary"}, "SCHEMAFLUX_CODEX_BIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.opts.Executable == "" {
				tc.opts.Executable = fakeExecutable(t)
			}
			p, err := codex.New(tc.opts)
			if err == nil || p != nil {
				t.Fatalf("New returned %v, %v", p, err)
			}
			var e *schemaflux.Error
			if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Fatalf("not a configuration error: %v", err)
			}
			if !strings.Contains(err.Error(), tc.say) {
				t.Errorf("the message does not say %q: %v", tc.say, err)
			}
			if e.Op != "codex.new" || e.Provider != "codex" && !strings.Contains(tc.name, "installed") {
				t.Errorf("op %q provider %q", e.Op, e.Provider)
			}
		})
	}
}

func TestNewAcceptsEveryEffortAndAnyRetrySetting(t *testing.T) {
	t.Parallel()
	efforts := []schemaflux.Effort{"", schemaflux.EffortNone, schemaflux.EffortMinimal, schemaflux.EffortLow, schemaflux.EffortMedium,
		schemaflux.EffortHigh, schemaflux.EffortXHigh, schemaflux.EffortMax}
	for _, effort := range efforts {
		for _, retries := range []int{-5, -1, 0, 1, 7} {
			p, err := codex.New(codex.Options{Executable: fakeExecutable(t), Effort: effort, Retries: retries, RetryBackoff: time.Millisecond, Model: "gpt-6.1-sol"})
			if err != nil || p.Name() != "codex" {
				t.Errorf("effort %q retries %d: %v", effort, retries, err)
			}
		}
	}
}

func TestTheRetrySettingsFollowTheConventionOfTheLibraryAndNotTheOldPackage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		retries      int
		backoff      time.Duration
		wantRetries  func(int) bool
		wantMeaning  string
		wantBackoff  time.Duration
		withModel    bool
		throughChain bool
	}{
		{"zero is the library's default", 0, 0, func(n int) bool { return n == 0 }, "zero", 0, false, false},
		{"zero with a backoff", 0, 5 * time.Millisecond, func(n int) bool { return n == 0 }, "zero", 5 * time.Millisecond, false, false},
		{"negative is never", -1, 0, func(n int) bool { return n < 0 }, "negative", 0, false, false},
		{"very negative is never", -9, 0, func(n int) bool { return n < 0 }, "negative", 0, false, false},
		{"positive is that many", 4, time.Second, func(n int) bool { return n == 4 }, "4", time.Second, false, false},
		{"positive under a model", 4, time.Second, func(n int) bool { return n == 4 }, "4", time.Second, true, true},
		{"never under a model", -1, 0, func(n int) bool { return n < 0 }, "negative", 0, true, true},
		{"default under a model", 0, 0, func(n int) bool { return n == 0 }, "zero", 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := codex.Options{Retries: tc.retries, RetryBackoff: tc.backoff}
			if tc.withModel {
				opts.Model = "m"
			}
			p := build(t, opts)
			// The library finds the settings on the provider and then on what it
			// wraps, so look the same way.
			var retrier provider.Retrier
			for q := p; q != nil && retrier == nil; {
				if r, ok := q.(provider.Retrier); ok {
					retrier = r
					break
				}
				u, ok := q.(provider.Unwrapper)
				if !ok {
					break
				}
				q = u.Unwrap()
			}
			if retrier == nil {
				t.Fatal("the provider has no retry settings the library can find")
			}
			retries, backoff := retrier.RetryPolicy()
			if !tc.wantRetries(retries) || backoff != tc.wantBackoff {
				t.Errorf("RetryPolicy = %d, %v: not %s", retries, backoff, tc.wantMeaning)
			}
		})
	}
}

func TestACompletedTurnIsAnAnswerWithItsUsage(t *testing.T) {
	t.Parallel()
	p := build(t, codex.Options{})
	resp, err := ask(p, script{Reply: "  The answer.  "}, schemaflux.Request{Model: "gpt-6.1-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "The answer." || resp.Provider != "codex" || resp.FinishReason != "stop" {
		t.Errorf("%+v", resp)
	}
	if resp.Model != "" {
		t.Errorf("Model = %q: the program does not say which model answered, and the one asked for is not an observation", resp.Model)
	}
	if resp.Usage.Input != 120 || resp.Usage.Output != 31 || resp.Usage.Cached != 40 || resp.Usage.Reasoning != 7 || resp.Usage.Total != 151 {
		t.Errorf("usage %+v", resp.Usage)
	}
}

func TestWhatTheProgramCannotDoIsRefusedBeforeItRunsAndWhatItDoesNotNeedIsAnswered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		req     schemaflux.Request
		refused bool
	}{
		{"web search", schemaflux.Request{WebSearch: true}, true},
		{"web search with a schema", schemaflux.Request{WebSearch: true, Schema: map[string]any{"type": "object"}, SchemaName: "S"}, true},
		{"a temperature, which the library always sends", schemaflux.Request{Temperature: 0.3}, false},
		{"an output ceiling, which the library always sends", schemaflux.Request{MaxTokens: 500}, false},
		{"a cache key", schemaflux.Request{CacheKey: "stable-prefix"}, false},
		{"a decision, which a text provider ignores", schemaflux.Request{Decision: &schemaflux.Decision{State: `"s"`, Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "q?"}}}}, false},
		{"a format", schemaflux.Request{Format: "json"}, false},
		{"a schema", schemaflux.Request{Format: "json", Schema: map[string]any{"type": "object"}, SchemaName: "S"}, false},
		{"a preferred effort", schemaflux.Request{Effort: schemaflux.EffortLow, EffortPreferred: true}, false},
		{"everything it can take", schemaflux.Request{Model: "m", Temperature: 0.1, MaxTokens: 10, Format: "json", Effort: schemaflux.EffortHigh}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := filepath.Join(t.TempDir(), "log")
			p := build(t, codex.Options{})
			resp, err := ask(p, script{Reply: "done", Log: log}, tc.req)
			runs := len(invocations(t, log))
			if tc.refused {
				if err == nil || kindOf(t, err) != schemaflux.ErrUnsupported || !errors.Is(err, errors.ErrUnsupported) {
					t.Errorf("err = %v, want an unsupported error", err)
				}
				if runs != 0 {
					t.Errorf("the program ran %d times for a request it cannot do", runs)
				}
				return
			}
			if err != nil || resp.Content != "done" || runs != 1 {
				t.Errorf("%+v %v runs %d", resp, err, runs)
			}
		})
	}
}

func TestTheRequestReachesTheProgramAsOneTaskOnItsInputAndFlagsOnItsCommandLine(t *testing.T) {
	t.Parallel()
	const canary = "USER-MESSAGE-CANARY-2204"
	log := filepath.Join(t.TempDir(), "log")
	p := build(t, codex.Options{})
	schema := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	if _, err := ask(p, script{Log: log, Reply: "{}"}, schemaflux.Request{Model: "gpt-6.1-sol", User: canary, Format: "json", Schema: schema, SchemaName: "S", Effort: schemaflux.EffortLow}); err != nil {
		t.Fatal(err)
	}
	runs := invocations(t, log)
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	args := strings.Join(runs[0].Args, " ")
	for _, want := range []string{"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--sandbox read-only", "--skip-git-repo-check", "--json", "--model gpt-6.1-sol", `model_reasoning_effort="low"`, "--output-schema"} {
		if !strings.Contains(args, want) {
			t.Errorf("the command line has no %q: %s", want, args)
		}
	}
	if strings.Contains(args, canary) {
		t.Errorf("the user message is on the command line, where any process listing reads it: %s", args)
	}
	if !strings.HasSuffix(strings.TrimSpace(runs[0].Stdin), canary) || !strings.Contains(runs[0].Stdin, "\n\n") {
		t.Errorf("the task on the program's input is %q: want the system prompt and the user message", runs[0].Stdin)
	}
	if runs[0].Args[len(runs[0].Args)-1] != "-" {
		t.Errorf("the task is not read from the standard input: %v", runs[0].Args)
	}
}

func TestEffortOfTheOptionsAndOfTheRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options schemaflux.Effort
		req     schemaflux.Request
		want    string // the value of model_reasoning_effort, or "" for none
		refused bool
	}{
		{"none anywhere", "", schemaflux.Request{}, "", false},
		{"the option", schemaflux.EffortMedium, schemaflux.Request{}, "medium", false},
		{"the request's own", "", schemaflux.Request{Effort: schemaflux.EffortHigh}, "high", false},
		{"the request's own replaces the option", schemaflux.EffortMedium, schemaflux.Request{Effort: schemaflux.EffortXHigh}, "xhigh", false},
		{"an operation's preference leaves the option", schemaflux.EffortMedium, schemaflux.Request{Effort: schemaflux.EffortNone, EffortPreferred: true}, "medium", false},
		{"an operation's preference and no option", "", schemaflux.Request{Effort: schemaflux.EffortNone, EffortPreferred: true}, "", false},
		{"the request's max", "", schemaflux.Request{Effort: schemaflux.EffortMax}, "max", false},
		{"an effort that is not one", schemaflux.EffortMedium, schemaflux.Request{Effort: "turbo"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := filepath.Join(t.TempDir(), "log")
			p := build(t, codex.Options{Effort: tc.options})
			_, err := ask(p, script{Log: log}, tc.req)
			runs := invocations(t, log)
			if tc.refused {
				if err == nil || kindOf(t, err) != schemaflux.ErrInvalidRequest || len(runs) != 0 {
					t.Errorf("err %v, runs %d", err, len(runs))
				}
				return
			}
			if err != nil || len(runs) != 1 {
				t.Fatalf("%v, runs %d", err, len(runs))
			}
			got := ""
			for i, a := range runs[0].Args {
				if a == "-c" && i+1 < len(runs[0].Args) {
					got = strings.Trim(strings.TrimPrefix(runs[0].Args[i+1], "model_reasoning_effort="), `"`)
				}
			}
			if got != tc.want {
				t.Errorf("effort = %q, want %q (%v)", got, tc.want, runs[0].Args)
			}
		})
	}
}

func TestTheModelOfTheOptionsIsWhatEveryTierMeansAndWhatARequestWithNoneRunsOn(t *testing.T) {
	t.Parallel()
	type modeller interface{ ModelFor(schemaflux.Tier) string }
	find := func(p schemaflux.Provider) (modeller, bool) {
		for p != nil {
			if m, ok := p.(modeller); ok {
				return m, true
			}
			u, ok := p.(provider.Unwrapper)
			if !ok {
				break
			}
			p = u.Unwrap()
		}
		return nil, false
	}

	named := build(t, codex.Options{Model: "gpt-6.1-sol"})
	m, ok := find(named)
	if !ok {
		t.Fatal("a provider with a model has no models for its tiers")
	}
	for _, tier := range []schemaflux.Tier{schemaflux.TierSmart, schemaflux.TierFast, schemaflux.TierQuick} {
		if got := m.ModelFor(tier); got != "gpt-6.1-sol" {
			t.Errorf("ModelFor(%v) = %q", tier, got)
		}
	}
	if got := m.ModelFor(schemaflux.Tier(0)); got != "" {
		t.Errorf("ModelFor of no tier = %q", got)
	}

	if _, ok := find(build(t, codex.Options{})); ok {
		t.Error("a provider with no model of its own claims models for its tiers")
	}

	cases := []struct {
		name, asked, want string
	}{
		{"a request that names none runs on the option's", "", "gpt-6.1-sol"},
		{"a request that names one runs on it", "gpt-other", "gpt-other"},
	}
	for _, tc := range cases {
		log := filepath.Join(t.TempDir(), "log")
		if _, err := ask(named, script{Log: log}, schemaflux.Request{Model: tc.asked}); err != nil {
			t.Fatal(err)
		}
		runs := invocations(t, log)
		var model string
		for i, a := range runs[0].Args {
			if a == "--model" {
				model = runs[0].Args[i+1]
			}
		}
		if model != tc.want {
			t.Errorf("%s: ran on %q, want %q", tc.name, model, tc.want)
		}
	}
}

func TestAFailedTurnIsAnUnavailableProviderThatQuotesNothingTheProgramWrote(t *testing.T) {
	t.Parallel()
	const words = "VENDOR-WORDS-CANARY-6630 the user message said something"
	p := build(t, codex.Options{})
	_, err := ask(p, script{Mode: "failed", Words: words}, schemaflux.Request{Model: "m"})
	if err == nil {
		t.Fatal("a failed turn was an answer")
	}
	if kindOf(t, err) != schemaflux.ErrUnavailable || !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	var e *schemaflux.Error
	_ = errors.As(err, &e)
	for name, text := range map[string]string{"Error": err.Error(), "log value": fmt.Sprint(e.LogValue())} {
		if strings.Contains(text, "VENDOR-WORDS") {
			t.Errorf("%s quotes the program: %s", name, text)
		}
	}
	// What the program said is for the developer who asks for it.
	if !strings.Contains(e.Detail(), "VENDOR-WORDS") {
		t.Errorf("Detail does not hold the program's own account: %s", e.Detail())
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("the error does not say which provider failed: %v", err)
	}
}

func TestATurnThatEndsWithNoEventIsAFailureAndNotAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	p := build(t, codex.Options{})
	resp, err := ask(p, script{Mode: "silent"}, schemaflux.Request{Model: "m"})
	if err == nil || resp.Content != "" {
		t.Fatalf("a turn with no completed event answered %+v, %v", resp, err)
	}
	if kindOf(t, err) != schemaflux.ErrUnavailable {
		t.Errorf("kind = %v", kindOf(t, err))
	}
}

func TestACallThatEndsWhileTheProgramRunsReportsTheCallersContextAndStopsTheProgram(t *testing.T) {
	t.Parallel()
	p := build(t, codex.Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := p.Complete(ctx, schemaflux.Request{Model: "m", System: systemOf(script{Mode: "hang"}), User: "x"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if kindOf(t, err) != schemaflux.ErrTimeout {
		t.Errorf("kind = %v, want timeout", kindOf(t, err))
	}
	if time.Since(started) > 20*time.Second {
		t.Errorf("the call outlived its deadline by %v", time.Since(started))
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := p.Complete(cancelled, schemaflux.Request{Model: "m"}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
}

func TestRequestsFromManyGoroutinesEachGetTheirOwnProcessAndAnswer(t *testing.T) {
	t.Parallel()
	p := build(t, codex.Options{})
	const n = 8
	results := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := ask(p, script{Reply: fmt.Sprintf("answer %d", i)}, schemaflux.Request{Model: "m"})
			if err != nil {
				results[i] = "error: " + err.Error()
				return
			}
			results[i] = resp.Content
		}()
	}
	wg.Wait()
	for i, got := range results {
		if want := fmt.Sprintf("answer %d", i); got != want {
			t.Errorf("request %d: %q, want %q", i, got, want)
		}
	}
}

// The tests below set a variable, which a parallel test may not.

func TestTheKeyVariablesNeverReachTheProgram(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-not-a-key")
	t.Setenv("CODEX_API_KEY", "not-a-key-either")
	log := filepath.Join(t.TempDir(), "log")
	if _, err := ask(build(t, codex.Options{}), script{Log: log}, schemaflux.Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	runs := invocations(t, log)
	if len(runs) != 1 || len(runs[0].Env) != 0 {
		t.Errorf("the program was given the key variables %v", runs)
	}
}

func TestACallOnAClientRunsOnTheModelOfTheOptionsAndRefusesWithoutOne(t *testing.T) {
	t.Setenv(scriptVariable, systemOf(script{Reply: `{"q1":true}`}))
	named := build(t, codex.Options{Model: "gpt-6.1-sol"})
	ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: named})
	ok, err := ops.Is(ctx, "a message", "Is this about billing?", types.OpOptions{})
	if err != nil || !ok {
		t.Fatalf("a call on a client whose provider has a model: %v %v", ok, err)
	}

	// With no model anywhere the call is refused before the program runs.
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv(scriptVariable, systemOf(script{Reply: `{"q1":true}`, Log: log}))
	bare := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: build(t, codex.Options{})})
	_, err = ops.Is(bare, "a message", "Is this about billing?", types.OpOptions{})
	if err == nil {
		t.Fatal("a call that names no model was sent")
	}
	if got := len(invocations(t, log)); got != 0 {
		t.Errorf("the program ran %d times", got)
	}
	// And the client's own model is enough for a provider that has none.
	if ok, err := ops.Is(schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: build(t, codex.Options{}), Model: "gpt-x"}), "a message", "q?", types.OpOptions{}); err != nil || !ok {
		t.Errorf("with the client's model: %v %v", ok, err)
	}
}

// The registry is the process's and has no undo, so the tests of Register run in
// a process of their own: this test starts the test binary again, running
// TestRegisterInAFreshProcess with a variable that makes it the body.
const childVariable = "SCHEMAFLUX_V2_CODEX_CHILD"

func TestRegisterInAFreshProcess(t *testing.T) {
	if os.Getenv(childVariable) == "" {
		cmd := exec.Command(fakeExecutable(t), "-test.run=^TestRegisterInAFreshProcess$", "-test.v")
		cmd.Env = append(os.Environ(), childVariable+"=1", "SCHEMAFLUX_CODEX_BIN="+fakeExecutable(t),
			scriptVariable+"="+systemOf(script{Reply: "from the registered provider"}))
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestRegisterInAFreshProcess") {
			t.Fatalf("the fresh process failed: %v\n%s", err, out)
		}
		return
	}

	if slices.Contains(provider.Names(), "codex") {
		t.Fatal("the name is taken before Register ran")
	}
	if err := codex.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := codex.Register(); err == nil || kindOf(t, err) != schemaflux.ErrConfiguration {
		t.Errorf("a second Register: %v", err)
	}
	if !slices.Contains(provider.Names(), "codex") {
		t.Errorf("Names does not list codex: %v", provider.Names())
	}

	// A key is ignored; a setting the program cannot use is refused.
	type withConfig struct {
		name string
		cfg  provider.Config
		bad  string
	}
	cases := []withConfig{
		{"a key", provider.Config{APIKey: "sk-ignored"}, ""},
		{"a base URL", provider.Config{BaseURL: "https://proxy.example/v1"}, "BaseURL"},
		{"an organisation", provider.Config{OrgID: "org"}, "OrgID"},
		{"headers", provider.Config{Headers: map[string]string{"X-A": "b"}}, "Headers"},
		{"store", provider.Config{Store: true}, "Store"},
		{"a service tier", provider.Config{ServiceTier: "flex"}, "ServiceTier"},
		{"an HTTP client", provider.Config{HTTPClient: &http.Client{}}, "HTTPClient"},
		{"an endpoint policy", provider.Config{Endpoint: &provider.EndpointPolicy{Hosts: []string{"x"}}}, "Endpoint"},
		{"retries and a backoff", provider.Config{Retries: -1, RetryBackoff: time.Millisecond}, ""},
		{"a timeout, which is not applied", provider.Config{Timeout: time.Second}, ""},
	}
	for _, tc := range cases {
		p, err := provider.New("codex", tc.cfg)
		if tc.bad != "" {
			if err == nil || kindOf(t, err) != schemaflux.ErrConfiguration || !strings.Contains(messagesIn(err), tc.bad) {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err != nil || p.Name() != "codex" {
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	// The retry settings of the Config are the provider's own.
	patient, err := provider.New("codex", provider.Config{Retries: 3, RetryBackoff: 7 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if retries, backoff := patient.(provider.Retrier).RetryPolicy(); retries != 3 || backoff != 7*time.Millisecond {
		t.Errorf("RetryPolicy = %d, %v, want 3 and 7ms", retries, backoff)
	}
	never, err := provider.New("codex", provider.Config{Retries: -1})
	if err != nil {
		t.Fatal(err)
	}
	if retries, _ := never.(provider.Retrier).RetryPolicy(); retries >= 0 {
		t.Errorf("a Config that says no retries gave %d", retries)
	}

	p, err := provider.New("codex", provider.Config{Models: provider.Models{Smart: "s", Fast: "f", Quick: "q"}})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := p.(interface{ ModelFor(schemaflux.Tier) string })
	if !ok || m.ModelFor(schemaflux.TierSmart) != "s" || m.ModelFor(schemaflux.TierFast) != "f" || m.ModelFor(schemaflux.TierQuick) != "q" {
		t.Errorf("the models of the three tiers were not kept: %v", ok)
	}
	resp, err := p.Complete(context.Background(), schemaflux.Request{Model: "s", User: "hi"})
	if err != nil || resp.Content != "from the registered provider" {
		t.Errorf("a registered provider answered %+v, %v", resp, err)
	}
	if _, err := ask(p, script{}, schemaflux.Request{WebSearch: true}); err == nil || kindOf(t, err) != schemaflux.ErrUnsupported {
		t.Errorf("web search through a registered provider: %v", err)
	}
}

// A client whose Provider is nil builds the one SCHEMAFLUX_PROVIDER names, and
// refuses one that needs a key when none is set. The program the codex provider
// runs needs none, so selecting it from the environment must work with no key
// variable anywhere. This runs in a process of its own for the same reason
// TestRegisterInAFreshProcess does.
const environmentChildVariable = "SCHEMAFLUX_V2_CODEX_ENVIRONMENT_CHILD"

func TestSelectingCodexFromTheEnvironmentNeedsNoKey(t *testing.T) {
	if os.Getenv(environmentChildVariable) == "" {
		var env []string
		for _, entry := range os.Environ() {
			// A key left in the parent's environment would let a provider that
			// wrongly demands one pass.
			if name, _, _ := strings.Cut(entry, "="); !strings.Contains(strings.ToUpper(name), "KEY") {
				env = append(env, entry)
			}
		}
		cmd := exec.Command(fakeExecutable(t), "-test.run=^TestSelectingCodexFromTheEnvironmentNeedsNoKey$", "-test.v")
		cmd.Env = append(env, environmentChildVariable+"=1", "SCHEMAFLUX_PROVIDER=codex", "SCHEMAFLUX_MODEL=gpt-x",
			"SCHEMAFLUX_CODEX_BIN="+fakeExecutable(t), scriptVariable+"="+systemOf(script{Reply: `{"q1":true}`}))
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestSelectingCodexFromTheEnvironmentNeedsNoKey") {
			t.Fatalf("the fresh process failed: %v\n%s", err, out)
		}
		return
	}

	if err := codex.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := schemaflux.Is(context.Background(), "a message", "Is this about billing?")
	if err != nil || !got {
		t.Fatalf("a call on the client the environment names, with no key set: %v, %v", got, err)
	}
}

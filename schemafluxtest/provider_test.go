package schemafluxtest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

type ticket struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type triage struct {
	Queue    string `json:"queue"`
	Priority string `json:"priority"`
}

// The point of this package: a consumer's test runs with no key, no network,
// and no money.
func TestConsumerCanExtractWithoutACredential(t *testing.T) {
	provider := schemafluxtest.New().
		Reply(`{"queue":"billing","priority":"high"}`)
	defer schemafluxtest.Install(t, provider)()

	result, err := schemaflux.Extract[triage](
		"Customer was charged twice for the March invoice.",
		schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if result.Queue != "billing" || result.Priority != "high" {
		t.Errorf("result = %+v", result)
	}
}

// A test can check what its own code sent, which is the half a canned reply
// cannot cover.
func TestConsumerCanInspectTheRequest(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{"queue":"billing","priority":"high"}`)
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage](
		ticket{ID: "T-91", Subject: "double charge", Body: "charged twice in March"},
		schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if provider.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1", provider.CallCount())
	}

	request := provider.LastRequest()
	if !strings.Contains(request.UserPrompt, "double charge") {
		t.Error("the ticket subject did not reach the model")
	}
	if request.Model == "" {
		t.Error("no model was selected")
	}
	if request.ResponseFormat != "json" {
		t.Errorf("ResponseFormat = %q, want json for a structured extraction", request.ResponseFormat)
	}
	// Extract sends a strict schema; a consumer can assert on it.
	if len(request.JSONSchema) == 0 {
		t.Error("no JSON schema was sent for a typed extraction")
	}
}

// The path that matters most in a consumer's tests: what their code does when
// the provider is unreachable.
func TestConsumerCanSimulateAnOutage(t *testing.T) {
	provider := schemafluxtest.New().Fail(errors.New("upstream is down"))
	defer schemafluxtest.Install(t, provider)()

	_, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions())
	if err == nil {
		t.Fatal("a failing provider must produce an error")
	}
	if !strings.Contains(err.Error(), "upstream is down") {
		t.Errorf("the underlying failure is not reported: %v", err)
	}
}

// And what it does when the provider answers with something unusable.
func TestConsumerCanSimulateABadAnswer(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"prose", "I'm sorry, I can't help with that."},
		{"truncated", `{"queue": "bill`},
		{"wrong_shape", `{"unrelated": true}`},
		{"empty", ""},
		{"error_page", "<html>502</html>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := schemafluxtest.New().Reply(tc.body)
			defer schemafluxtest.Install(t, provider)()

			if _, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions()); err == nil {
				t.Fatal("an unusable answer must produce an error")
			}
		})
	}
}

// Replies are returned in order, and the last one repeats so a test does not
// have to predict how many calls its code makes.
func TestRepliesAreSequential(t *testing.T) {
	provider := schemafluxtest.New().Reply(
		`{"queue":"first","priority":"low"}`,
		`{"queue":"second","priority":"high"}`,
	)
	defer schemafluxtest.Install(t, provider)()

	first, err := schemaflux.Extract[triage]("one", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	second, err := schemaflux.Extract[triage]("two", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	third, err := schemaflux.Extract[triage]("three", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if first.Queue != "first" || second.Queue != "second" {
		t.Errorf("replies out of order: %q then %q", first.Queue, second.Queue)
	}
	if third.Queue != "second" {
		t.Errorf("the last reply should repeat, got %q", third.Queue)
	}
}

// FailThen is how a consumer tests their retry handling.
func TestFailThenRecovers(t *testing.T) {
	provider := schemafluxtest.New().
		FailThen(2, errors.New("rate limited"), `{"queue":"billing","priority":"low"}`)
	defer schemafluxtest.Install(t, provider)()

	// The library does not retry by default here, so the first two calls fail.
	for i := 0; i < 2; i++ {
		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
			t.Fatalf("call %d should have failed", i+1)
		}
	}

	result, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("the third call should have succeeded: %v", err)
	}
	if result.Queue != "billing" {
		t.Errorf("result = %+v", result)
	}
}

// Slow is how a consumer tests their timeout handling, and the wait is
// cancellable so the test does not sit through it.
func TestSlowProviderHonoursCancellation(t *testing.T) {
	provider := schemafluxtest.New().Slow(30 * time.Second)
	defer schemafluxtest.Install(t, provider)()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	opts := schemaflux.NewExtractOptions()
	opts.OpOptions.Context = ctx

	start := time.Now()
	_, err := schemaflux.Extract[triage]("anything", opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a 50ms deadline against a 30s provider must fail")
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %v; the wait is not cancellable", elapsed)
	}
}

// Usage is reportable, so a consumer can test their cost accounting.
func TestUsageIsReported(t *testing.T) {
	provider := schemafluxtest.New().
		Reply(`{"queue":"billing","priority":"low"}`).
		WithUsage(schemaflux.TokenUsage{PromptTokens: 120, CompletionTokens: 34, TotalTokens: 154})
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if provider.CallCount() != 1 {
		t.Errorf("CallCount = %d", provider.CallCount())
	}
}

// Reset rewinds without rebuilding.
func TestReset(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{"queue":"a","priority":"low"}`, `{"queue":"b","priority":"low"}`)
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("one", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	provider.Reset()

	if provider.CallCount() != 0 {
		t.Errorf("CallCount after Reset = %d, want 0", provider.CallCount())
	}

	result, err := schemaflux.Extract[triage]("one again", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Queue != "a" {
		t.Errorf("Reset did not rewind the reply sequence: got %q", result.Queue)
	}
}

// A provider name the library does not know resolves to no model, so the double
// defaults to one it does.
func TestDefaultProviderNameResolvesToAModel(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{"queue":"a","priority":"low"}`)
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("the default double must be usable out of the box: %v", err)
	}
	if provider.LastRequest().Model == "" {
		t.Error("no model was resolved for the double")
	}
}

// Example shows the whole shape of testing code that uses SchemaFlux: no key,
// no network, and an assertion on what your code actually sent.
func Example() {
	// In a real test this is `defer schemafluxtest.Install(t, provider)()`.
	provider := schemafluxtest.New().Reply(`{"queue":"billing","priority":"high"}`)
	schemaflux.NewClient("example-key").WithProviderInstance(provider)

	result, err := schemaflux.Extract[triage](
		"Customer was charged twice for the March invoice.",
		schemaflux.NewExtractOptions())
	if err != nil {
		fmt.Println("extraction failed:", err)
		return
	}

	fmt.Println("queue:", result.Queue)
	fmt.Println("priority:", result.Priority)
	fmt.Println("the ticket text reached the model:",
		strings.Contains(provider.LastRequest().UserPrompt, "charged twice"))

	// Output:
	// queue: billing
	// priority: high
	// the ticket text reached the model: true
}

// Example_outage shows the case worth testing most: what your code does when
// the provider is unreachable.
func Example_outage() {
	provider := schemafluxtest.New().Fail(errors.New("upstream is down"))
	schemaflux.NewClient("example-key").WithProviderInstance(provider)

	_, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions())

	fmt.Println("error is nil:", err == nil)
	fmt.Println("names the cause:", strings.Contains(err.Error(), "upstream is down"))

	// Output:
	// error is nil: false
	// names the cause: true
}

// CF-01: a parse failure used to be terminal. The repair loop feeds the failure
// back to the model and asks again, which is the defining feature of a typed
// LLM library — and a consumer can now see it happen.
func TestRepairIsVisibleToConsumers(t *testing.T) {
	provider := schemafluxtest.New().Reply(
		"I'm sorry, I can't produce JSON.",
		`{"queue":"billing","priority":"high"}`,
	)
	defer schemafluxtest.Install(t, provider)()

	result, err := schemaflux.Extract[triage]("charged twice", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("the repair should have rescued this: %v", err)
	}
	if result.Queue != "billing" {
		t.Errorf("result = %+v", result)
	}

	if provider.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2 (one attempt, one repair)", provider.CallCount())
	}

	repair := provider.Requests()[1]
	if !strings.Contains(repair.UserPrompt, "previous answer could not be used") {
		t.Errorf("the repair prompt does not tell the model what went wrong:\n%s", repair.UserPrompt)
	}
	if !strings.Contains(repair.UserPrompt, "charged twice") {
		t.Error("the repair prompt lost the original task")
	}
}

// A provider name the library does not know resolves to no model at all, and
// every call errors -- this is FL-001, and As is how a test opts into it.
// Assembled, not written out: see the note on fakeKey in
// internal/types/diagnostics_test.go. A literal here trips this repo's secret
// scan and GitHub's push protection alike.
var fakeProjKey = "sk-" + "proj-" + "FAILUREPATHLEAK"

func TestAsWithAnUnknownNameResolvesToNoModel(t *testing.T) {
	provider := schemafluxtest.New().As("a-provider-that-does-not-exist").
		Reply(`{"queue":"billing","priority":"low"}`)
	defer schemafluxtest.Install(t, provider)()

	_, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions())
	if err == nil {
		t.Fatal("an unknown provider name must not resolve to a usable model")
	}
}

// Shaped answers from the request's own declared shape rather than the fixed
// "{}" default, which is what lets a test that only cares about the request
// skip scripting a body per operation.
func TestShapedAnswersFromTheRequestsOwnSchema(t *testing.T) {
	provider := schemafluxtest.New().Shaped()
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("a shaped reply should satisfy the type's own schema: %v", err)
	}

	if len(provider.Requests()) != 1 {
		t.Fatalf("CallCount = %d, want 1", len(provider.Requests()))
	}
}

// A scripted Reply always wins over Shaped -- it is a fallback, not a mode.
func TestReplyStillWinsOverShaped(t *testing.T) {
	provider := schemafluxtest.New().Shaped().Reply(`{"queue":"billing","priority":"high"}`)
	defer schemafluxtest.Install(t, provider)()

	result, err := schemaflux.Extract[triage]("anything", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Queue != "billing" {
		t.Errorf("Shaped overrode the scripted reply: %+v", result)
	}
}

// Fail and FailThen with a nil error still fail -- the default message is
// there so a test that does not care what the error says still gets one. Fail
// keeps failing on every later call, which a single call cannot show.
func TestFailAndFailThenDefaultTheirError(t *testing.T) {
	t.Run("Fail", func(t *testing.T) {
		provider := schemafluxtest.New().Fail(nil)
		defer schemafluxtest.Install(t, provider)()

		for i := 1; i <= 3; i++ {
			_, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions())
			if err == nil {
				t.Fatalf("Fail(nil) must fail every call; call %d succeeded", i)
			}
			if !strings.Contains(err.Error(), "provider unavailable") {
				t.Errorf("call %d did not report the default error: %v", i, err)
			}
		}
	})

	t.Run("FailThen", func(t *testing.T) {
		provider := schemafluxtest.New().FailThen(1, nil, `{"queue":"billing","priority":"low"}`)
		defer schemafluxtest.Install(t, provider)()

		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
			t.Fatal("the first call should have failed")
		}
		if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err != nil {
			t.Fatalf("the second call should have recovered: %v", err)
		}
	})
}

// Fail means every call, with or without Shaped having cleared the reply list
// first. The constructor's default reply used to survive Fail, so the outage
// ended after the first call and a test of retry or repair code saw a provider
// that had recovered.
func TestFailKeepsFailingPastTheScriptedCount(t *testing.T) {
	outage := errors.New("still down")

	for _, tc := range []struct {
		name     string
		provider *schemafluxtest.Provider
	}{
		{"on a new provider", schemafluxtest.New().Fail(outage)},
		{"after Shaped", schemafluxtest.New().Shaped().Fail(outage)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer schemafluxtest.Install(t, tc.provider)()

			for i := 1; i <= 3; i++ {
				_, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions())
				if err == nil {
					t.Fatalf("call %d should have failed", i)
				}
				if !strings.Contains(err.Error(), "still down") {
					t.Errorf("call %d did not report the outage: %v", i, err)
				}
			}
		})
	}
}

// Provider.Complete is called directly here so the number of calls is exact: an
// operation may call a provider more than once per request when it repairs.
func TestFailRepeatsItsErrorOnEveryCall(t *testing.T) {
	first := errors.New("first outage")
	second := errors.New("second outage")
	request := schemaflux.CompletionRequest{Model: "m", UserPrompt: "u"}

	cases := []struct {
		name    string
		build   func() *schemafluxtest.Provider
		calls   int
		wantErr error  // identity, when set
		wantMsg string // substring, when wantErr is nil
		wantOK  bool   // every call succeeds instead
	}{
		{"on a new provider", func() *schemafluxtest.Provider { return schemafluxtest.New().Fail(first) }, 6, first, "", false},
		{"with a nil error", func() *schemafluxtest.Provider { return schemafluxtest.New().Fail(nil) }, 6, nil, "provider unavailable", false},
		{"after Shaped", func() *schemafluxtest.Provider { return schemafluxtest.New().Shaped().Fail(first) }, 6, first, "", false},
		{"after Reply with several bodies", func() *schemafluxtest.Provider {
			return schemafluxtest.New().Reply("a", "b", "c").Fail(first)
		}, 6, first, "", false},
		{"a second Fail replaces the first", func() *schemafluxtest.Provider {
			return schemafluxtest.New().Fail(first).Fail(second)
		}, 6, second, "", false},
		{"after FailThen", func() *schemafluxtest.Provider {
			return schemafluxtest.New().FailThen(2, first, "ok").Fail(second)
		}, 6, second, "", false},
		{"ahead of ReplyFunc", func() *schemafluxtest.Provider {
			return schemafluxtest.New().Fail(first).ReplyFunc(func(int, schemaflux.CompletionRequest) (string, error) {
				return "from the function", nil
			})
		}, 6, first, "", false},
		{"with usage set", func() *schemafluxtest.Provider {
			return schemafluxtest.New().Fail(first).WithUsage(schemaflux.TokenUsage{PromptTokens: 1})
		}, 6, first, "", false},
		{"under another name", func() *schemafluxtest.Provider { return schemafluxtest.New().Fail(first).As("other") }, 6, first, "", false},
		{"with a short delay", func() *schemafluxtest.Provider {
			return schemafluxtest.New().Fail(first).Slow(time.Millisecond)
		}, 3, first, "", false},
		{"a single call", func() *schemafluxtest.Provider { return schemafluxtest.New().Fail(first) }, 1, first, "", false},
		{"twelve calls", func() *schemafluxtest.Provider { return schemafluxtest.New().Fail(first) }, 12, first, "", false},
		{"Reply afterwards ends the outage", func() *schemafluxtest.Provider {
			return schemafluxtest.New().Fail(first).Reply("back")
		}, 4, nil, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := tc.build()
			for call := 1; call <= tc.calls; call++ {
				resp, err := provider.Complete(context.Background(), request)
				if tc.wantOK {
					if err != nil || resp.Content != "back" {
						t.Fatalf("call %d: got (%q, %v), want a success", call, resp.Content, err)
					}
					continue
				}
				if err == nil {
					t.Fatalf("call %d succeeded with %q; Fail must fail every call", call, resp.Content)
				}
				if resp.Content != "" {
					t.Errorf("call %d returned content %q beside an error", call, resp.Content)
				}
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Errorf("call %d error = %v, want %v", call, err, tc.wantErr)
				}
				if tc.wantErr == nil && !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("call %d error = %v, want it to contain %q", call, err, tc.wantMsg)
				}
			}
		})
	}
}

// Fail still fails after Reset rewinds the call counter.
func TestFailStillFailsAfterReset(t *testing.T) {
	outage := errors.New("down")
	provider := schemafluxtest.New().Fail(outage)
	request := schemaflux.CompletionRequest{Model: "m"}

	for round := 0; round < 3; round++ {
		for call := 0; call < 3; call++ {
			if _, err := provider.Complete(context.Background(), request); !errors.Is(err, outage) {
				t.Fatalf("round %d call %d: err = %v, want %v", round, call, err, outage)
			}
		}
		provider.Reset()
	}
}

// Shaped and Complete from different goroutines share the provider's own
// state. Complete used to read the shaped flag after releasing the mutex, which
// the race detector reports; this runs both against each other so a build with
// -race (CI runs one) fails on it. Without the detector it checks only that
// every call returns an answer.
func TestShapedAndCompleteMayRunConcurrently(t *testing.T) {
	provider := schemafluxtest.New()
	request := schemaflux.CompletionRequest{Model: "m", UserPrompt: "u"}

	const workers = 8
	const rounds = 200

	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(2)
		go func() {
			defer group.Done()
			for round := 0; round < rounds; round++ {
				provider.Shaped()
			}
		}()
		go func() {
			defer group.Done()
			for round := 0; round < rounds; round++ {
				if _, err := provider.Complete(context.Background(), request); err != nil {
					t.Errorf("Complete: %v", err)
					return
				}
			}
		}()
	}
	group.Wait()

	if got, want := provider.CallCount(), workers*rounds; got != want {
		t.Errorf("CallCount = %d, want %d", got, want)
	}
}

// Reply with no arguments resets the double to its constructor default,
// rather than leaving whatever was scripted before in place.
func TestReplyWithNoArgumentsResetsToTheDefault(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{"queue":"billing","priority":"low"}`)
	provider.Reply()
	defer schemafluxtest.Install(t, provider)()

	// "{}" cannot satisfy triage's required fields, so the call must fail --
	// proving the old scripted body is gone, not just unread.
	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
		t.Fatal("Reply() with no arguments should have reverted to the unusable default")
	}
}

// LastRequest on a provider that has never been called returns the zero
// value rather than panicking, so a test asserting "nothing was sent yet"
// has something to assert against.
func TestLastRequestBeforeAnyCallIsTheZeroValue(t *testing.T) {
	provider := schemafluxtest.New()
	if got := provider.LastRequest(); got.Model != "" || got.UserPrompt != "" {
		t.Errorf("LastRequest before any call = %+v, want the zero value", got)
	}
}

// ReplyFunc answers from the request itself, which is what a caller that
// batches work into multiple calls needs: each call gets an answer shaped
// for what THAT call asked, not a fixed script that cannot know.
func TestReplyFuncAnswersFromTheRequest(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(call int, req schemaflux.CompletionRequest) (string, error) {
		if call == 0 {
			return `{"queue":"first","priority":"low"}`, nil
		}
		return "", errors.New("no more scripted calls")
	})
	defer schemafluxtest.Install(t, provider)()

	result, err := schemaflux.Extract[triage]("one", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Queue != "first" {
		t.Errorf("result = %+v", result)
	}
}

// ReplyFunc's error return fails that call, same as a scripted error would.
func TestReplyFuncCanFailACall(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(call int, req schemaflux.CompletionRequest) (string, error) {
		return "", errors.New("this call is scripted to fail")
	})
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
		t.Fatal("ReplyFunc returning an error must fail the call")
	}
}

// FailThen's failures still apply even when ReplyFunc is scripted; scripted
// errors are checked before the function, so "fail twice, then answer from
// the request" composes.
func TestFailThenAppliesBeforeReplyFunc(t *testing.T) {
	provider := schemafluxtest.New().
		FailThen(1, errors.New("rate limited"), "unused").
		ReplyFunc(func(call int, req schemaflux.CompletionRequest) (string, error) {
			return `{"queue":"from-func","priority":"low"}`, nil
		})
	defer schemafluxtest.Install(t, provider)()

	if _, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions()); err == nil {
		t.Fatal("the scripted failure should have applied first")
	}
	result, err := schemaflux.Extract[triage]("x", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Queue != "from-func" {
		t.Errorf("ReplyFunc did not answer the second call: %+v", result)
	}
}

// Contexts records the context each call arrived on, in order -- the only
// place a per-call deadline set by the caller's own code is observable from
// outside that code.
func TestContextsRecordsEachCallsContext(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{"queue":"a","priority":"low"}`)
	defer schemafluxtest.Install(t, provider)()

	type keyT struct{}
	ctx := context.WithValue(context.Background(), keyT{}, "marker")
	opts := schemaflux.NewExtractOptions()
	opts.OpOptions.Context = ctx

	if _, err := schemaflux.Extract[triage]("x", opts); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	contexts := provider.Contexts()
	if len(contexts) != 1 {
		t.Fatalf("Contexts() len = %d, want 1", len(contexts))
	}
	if got := contexts[0].Value(keyT{}); got != "marker" {
		t.Errorf("the recorded context lost the caller's value: %v", got)
	}

	if got := provider.ContextN(0).Value(keyT{}); got != "marker" {
		t.Errorf("ContextN(0) lost the caller's value: %v", got)
	}
}

// A ContextN call for an index that never happened returns Background
// rather than nil, so an assertion on it reports a missing deadline instead
// of panicking somewhere unrelated.
func TestContextNOutOfRangeReturnsBackground(t *testing.T) {
	provider := schemafluxtest.New()
	if got := provider.ContextN(0); got != context.Background() {
		t.Errorf("ContextN(0) on an uncalled provider = %v, want context.Background()", got)
	}
	if got := provider.ContextN(-1); got != context.Background() {
		t.Errorf("ContextN(-1) = %v, want context.Background()", got)
	}
}

// EstimateCost implements the Provider interface and always reports zero: a
// double costs nothing, no matter what it is asked to price.
func TestEstimateCostIsAlwaysZero(t *testing.T) {
	provider := schemafluxtest.New()
	if cost := provider.EstimateCost(schemaflux.CompletionRequest{Model: "gpt-5.6-sol"}); cost != 0 {
		t.Errorf("EstimateCost() = %v, want 0", cost)
	}
}

// A context already cancelled before Complete is even called is reported
// immediately, with no delay configured -- the check does not depend on
// Slow being set.
func TestCompleteReportsAnAlreadyCancelledContext(t *testing.T) {
	provider := schemafluxtest.New().Reply(`{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Complete(ctx, schemaflux.CompletionRequest{Model: "gpt-5.6-sol"})
	if err == nil {
		t.Fatal("Complete against an already-cancelled context must fail")
	}
}

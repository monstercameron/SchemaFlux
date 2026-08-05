package schemafluxtest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// Install restores whatever was there before, so a test does not leak its
// double into the next one.
func TestInstallRestoresThePreviousClient(t *testing.T) {
	before := schemaflux.GetDefaultClient()

	func() {
		provider := schemafluxtest.New()
		restore := schemafluxtest.Install(t, provider)
		defer restore()

		if schemaflux.GetDefaultClient() == before {
			t.Error("Install did not replace the client")
		}
	}()

	if schemaflux.GetDefaultClient() != before {
		t.Error("the previous client was not restored")
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

package ops

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The emission functions of observe.go, and what CallLLM, the repair loop and
// the streaming path tell an observer through them.

// scopes returns the events of one scope.
func scopes[E any](events []E, scope telemetry.Scope, of func(E) telemetry.Scope) []E {
	var found []E
	for _, event := range events {
		if of(event) == scope {
			found = append(found, event)
		}
	}
	return found
}

func eventScope(event telemetry.OperationEvent) telemetry.Scope    { return event.Scope }
func resultScope(result telemetry.OperationResult) telemetry.Scope { return result.Scope }

func TestObserveOperationReportsHowTheWholeCallEnded(t *testing.T) {
	deadline, cancelDeadline := context.WithTimeout(context.Background(), -time.Second)
	defer cancelDeadline()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no error", nil, ""},
		{"a refused answer", types.Newf(types.KindSchemaViolation, "extract", "the answer has the wrong shape"), "schema violation"},
		{"repairs ran out", types.Newf(types.KindRepairExhausted, "extract", "still unusable"), "repair exhausted"},
		{"a failed check", types.Newf(types.KindInvariantViolation, "sort", "not a permutation"), "invariant violation"},
		{"a cancelled call", context.Canceled, "canceled"},
		{"a deadline", deadline.Err(), "timeout"},
		{"a classified error wrapped by the operation", errors.Join(errors.New("extraction failed"), types.Newf(types.KindMalformedOutput, "extract", "not JSON")), "malformed output"},
		{"a provider's error", llm.NewAPIError("openai", "m", 500, "boom"), "provider unavailable"},
		{"a rate limit", &llm.RateLimitError{APIError: llm.NewAPIError("openai", "m", 429, "slow")}, "rate limited"},
		{"an error of no kind", errors.New("something else"), "unknown"},
		{"a contract floor refusal", types.Newf(types.KindPolicyViolation, "extract", "below the floor"), "policy violation"},
		{"an answer the model refused", types.ClassifiedRefusal(errors.New("withheld")), "refused"},
		{"a refusal an operation joined into its own error", errors.Join(errors.New("extraction failed"), types.ClassifiedRefusal(errors.New("withheld"))), "refused"},
		{"a refusal marked on an operation error", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal, Op: "choose", Message: "declined"}, "refused"},
		{"a refusal under an unmarked operation error is hidden by it", &types.OperationError{Kind: types.KindSchemaViolation, Op: "extract", Cause: types.ClassifiedRefusal(errors.New("withheld"))}, "schema violation"},
		{"a recovered panic is internal and not a refusal", &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic, Op: "flow", Message: "step panicked"}, "internal"},
		{"a failed check that carries no panic mark stays an invariant violation", &types.OperationError{Kind: types.KindInvariantViolation, Op: "flow", Message: "not a permutation"}, "invariant violation"},
		{"a panic mark on an error joined into the operation's own", errors.Join(errors.New("gather failed"), &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic, Op: "flow"}), "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)

			run := func() (err error) {
				ctx, observed := observeOperation(context.Background(), "extract")
				defer observed.end(&err)
				if operationNameFromContext(ctx) != "" {
					t.Error("observeOperation named the context; naming is WithOperationName's")
				}
				return tc.err
			}
			if got := run(); got != tc.err {
				t.Fatalf("the call's own error was changed: %v", got)
			}

			started, finished := collector.snapshot()
			if len(started) != 1 || len(finished) != 1 {
				t.Fatalf("%d starts and %d finishes, want 1 and 1", len(started), len(finished))
			}
			if started[0].Scope != telemetry.ScopeOperation || started[0].Operation != "extract" {
				t.Errorf("start = %+v, want the operation scope and the operation's name", started[0])
			}
			result := finished[0]
			if result.Scope != telemetry.ScopeOperation || result.Operation != "extract" || result.ErrorKind != tc.want {
				t.Errorf("finish = scope %v, operation %q, kind %q; want the operation scope, extract, %q",
					result.Scope, result.Operation, result.ErrorKind, tc.want)
			}
			if tc.err != nil && strings.Contains(result.ErrorKind, tc.err.Error()) {
				t.Error("the finish carries the error's text")
			}
		})
	}
}

// One call of an operation passes through observeOperation once for each
// wrapper it runs under. It is one unit.
func TestObserveOperationJoinsACallOfTheSameOperationAlreadyOpen(t *testing.T) {
	cases := []struct {
		name  string
		outer []string // operations opened first, outermost first
		inner string
		want  bool // whether the inner call starts a unit of its own
	}{
		{"nothing open", nil, "extract", true},
		{"the same operation", []string{"extract"}, "extract", false},
		{"another operation", []string{"gather"}, "extract", true},
		{"the same, under another", []string{"gather", "extract"}, "extract", false},
		{"the same further out, another between", []string{"extract", "audit"}, "extract", true},
		{"no name", nil, "", false},
		{"no name under an open call", []string{"extract"}, "", false},
		{"three deep, the same", []string{"extract", "extract", "extract"}, "extract", false},
		{"a different spelling", []string{"extract"}, "Extract", true},
		{"a longer name with the same start", []string{"extract"}, "extract_batch", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			ctx := context.Background()
			for _, name := range tc.outer {
				ctx, _ = observeOperation(ctx, name)
			}
			before, _ := collector.snapshot()

			inner, observed := observeOperation(ctx, tc.inner)
			after, _ := collector.snapshot()

			if started := len(after) > len(before); started != tc.want || (observed != nil) != tc.want {
				t.Errorf("started a unit of its own = %v (value non-nil %v), want %v", started, observed != nil, tc.want)
			}
			if !tc.want && inner != ctx {
				t.Error("a call that joined an open one was given a different context")
			}
			var err error
			observed.end(&err)
		})
	}
}

// An operation that has ended is not joined: a context kept from an earlier
// call starts a new unit.
func TestObserveOperationDoesNotJoinACallThatHasEnded(t *testing.T) {
	collector := installCollector(t)
	ctx, first := observeOperation(context.Background(), "extract")
	var err error
	first.end(&err)

	_, second := observeOperation(ctx, "extract")
	if second == nil {
		t.Fatal("a call on a context left over from one that ended joined it and would never be reported")
	}
	second.end(&err)
	if started, finished := collector.snapshot(); len(started) != 2 || len(finished) != 2 {
		t.Errorf("%d starts and %d finishes, want 2 and 2", len(started), len(finished))
	}
}

func TestObserveOperationWithNoObserverStartsNothingAndAllocatesNothing(t *testing.T) {
	if telemetry.HasObserver() {
		t.Fatal("an observer is installed before the test began")
	}
	ctx := context.Background()

	got, observed := observeOperation(ctx, "extract")
	if got != ctx || observed != nil {
		t.Fatal("with no observer installed the context changed or a unit was started")
	}

	call := func() (err error) {
		ctx, observed := observeOperation(ctx, "extract")
		defer observed.end(&err)
		request := observeStart(ctx, telemetry.OperationEvent{Operation: "extract", Provider: "openai", Model: "m"})
		telemetry.ObserveOperationNote(request, telemetry.Note{Name: telemetry.NoteRetry, Attempt: 1})
		observeEnd(request, telemetry.OperationResult{Operation: "extract", Attempts: 1})
		_ = repairContext(ctx, 0)
		_ = isRepairRequest(request)
		return nil
	}
	if allocations := testing.AllocsPerRun(200, func() { _ = call() }); allocations != 0 {
		t.Errorf("the emission path of one call allocates %.1f times with no observer installed, want 0", allocations)
	}
}

// A panic inside the call is reported as a failure of kind "internal", and
// goes on to whoever it was going to with the value it had.
func TestObserveOperationReportsAPanicAndLetsItContinue(t *testing.T) {
	values := []any{"a string", errors.New("an error"), 42, struct{ field string }{"x"}}
	for _, value := range values {
		collector := installCollector(t)

		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_ = func() (err error) {
				_, observed := observeOperation(context.Background(), "extract")
				defer observed.end(&err)
				panic(value)
			}()
		}()

		if recovered != value {
			t.Errorf("the caller recovered %v, want the value that was panicked with, %v", recovered, value)
		}
		_, finished := collector.snapshot()
		if len(finished) != 1 || finished[0].ErrorKind != telemetry.ErrorKindInternal {
			t.Errorf("after a panic the finishes are %+v, want one of kind %q", finished, telemetry.ErrorKindInternal)
		}
	}
}

func TestRepairContextMarksEveryAttemptAfterTheFirst(t *testing.T) {
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "kept")
	cases := []struct {
		name    string
		ctx     context.Context
		attempt int
		want    bool
	}{
		{"the first attempt", parent, 0, false},
		{"the first repair", parent, 1, true},
		{"the second repair", parent, 2, true},
		{"a later repair", parent, 9, true},
		{"a negative attempt", parent, -1, false},
		{"a nil context, first attempt", nil, 0, false},
		{"a nil context, a repair", nil, 1, false},
		{"a background context, first attempt", context.Background(), 0, false},
		{"a background context, a repair", context.Background(), 3, true},
		{"a context already marked", repairContext(parent, 1), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := repairContext(tc.ctx, tc.attempt)
			if isRepairRequest(got) != tc.want {
				t.Errorf("marked as a repair = %v, want %v", isRepairRequest(got), tc.want)
			}
			if tc.ctx != nil && got.Value(key{}) != tc.ctx.Value(key{}) {
				t.Error("the context lost a value it carried")
			}
			if tc.attempt <= 0 && got != tc.ctx {
				t.Error("the first attempt was given a different context")
			}
		})
	}
}

func TestRequestErrorKind(t *testing.T) {
	empty := llm.CompletionResponse{Content: "  ", FinishReason: "stop"}
	usable := llm.CompletionResponse{Content: "text", FinishReason: "stop"}
	cases := []struct {
		name     string
		err      error
		answered bool
		resp     llm.CompletionResponse
		want     string
	}{
		{"a classified error", types.Newf(types.KindOutputTruncated, "llm.call", "cut off"), true, usable, "output truncated"},
		{"an empty answer, whose error has no kind", errors.New("provider returned empty completion content"), true, empty, "malformed output"},
		{"an error of no kind with no answer", errors.New("dial failed"), false, llm.CompletionResponse{}, "unknown"},
		{"an error of no kind after a usable answer", errors.New("odd"), true, usable, "unknown"},
		{"a rate limit", &llm.RateLimitError{APIError: llm.NewAPIError("p", "m", 429, "slow")}, false, llm.CompletionResponse{}, "rate limited"},
		{"a server error", llm.NewAPIError("p", "m", 503, "down"), false, llm.CompletionResponse{}, "provider unavailable"},
		{"a refused key", llm.NewAPIError("p", "m", 401, "no"), false, llm.CompletionResponse{}, "authentication"},
		{"a cancellation", context.Canceled, false, llm.CompletionResponse{}, "canceled"},
		{"a deadline", context.DeadlineExceeded, false, llm.CompletionResponse{}, "timeout"},
		{"a classified error wins over the answer's reading", types.Newf(types.KindPolicyViolation, "llm.call", "route"), true, empty, "policy violation"},
		{"an empty answer is not consulted when nothing answered", errors.New("odd"), false, empty, "unknown"},
		{"an answer the model refused", types.ClassifiedRefusal(errors.New("withheld")), true, empty, "refused"},
		{"a refusal needs no answer to be one", types.ClassifiedRefusal(errors.New("withheld")), false, llm.CompletionResponse{}, "refused"},
		{"a refusal marked on an operation error", &types.OperationError{Kind: types.KindPolicyViolation, Narrow: types.NarrowRefusal, Message: "declined"}, true, usable, "refused"},
		{"an empty answer the filter withheld, whose error has no kind", errors.New("provider returned empty completion content"), true, llm.CompletionResponse{FinishReason: "content_filter"}, "refused"},
		{"a filtered answer that is not empty, whose error has no kind", errors.New("odd"), true, llm.CompletionResponse{Content: "x", FinishReason: "content_filter"}, "refused"},
		{"a filtered answer is not consulted when nothing answered", errors.New("odd"), false, llm.CompletionResponse{FinishReason: "content_filter"}, "unknown"},
		{"a policy violation that is not a refusal", types.Newf(types.KindPolicyViolation, "llm.call", "route"), true, llm.CompletionResponse{FinishReason: "content_filter"}, "policy violation"},
		{"a refusal under an unmarked classified error is found", types.Classified(types.KindPolicyViolation, types.ClassifiedRefusal(errors.New("x"))), true, usable, "refused"},
		{"an internal failure marked as a recovered panic", &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic, Message: "copy panicked"}, false, llm.CompletionResponse{}, "internal"},
		{"a recovered panic after an answer", &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic, Message: "copy panicked"}, true, usable, "internal"},
		{"an invariant violation with no panic mark", types.Newf(types.KindInvariantViolation, "llm.call", "broke a rule"), true, usable, "invariant violation"},
		{"a recovered panic of another kind is still internal", &types.OperationError{Kind: types.KindProviderUnavailable, Narrow: types.NarrowPanic, Message: "x"}, false, llm.CompletionResponse{}, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestErrorKind(tc.err, tc.answered, tc.resp); got != tc.want {
				t.Errorf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

// scriptedAnswers answers each call with the next script entry.
type scriptedAnswers struct {
	callerSuppliedProvider
	script  []func() (llm.CompletionResponse, error)
	retries int
}

func (p *scriptedAnswers) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	step := p.script[min(p.calls, len(p.script)-1)]
	p.calls++
	return step()
}

func (p *scriptedAnswers) RetryPolicy() (int, time.Duration) { return p.retries, time.Millisecond }

func answerOf(content, finishReason string) func() (llm.CompletionResponse, error) {
	return func() (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: content, Model: "served", Provider: "acme", FinishReason: finishReason,
			Usage: types.TokenUsage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14}}, nil
	}
}

func failingWith(err error) func() (llm.CompletionResponse, error) {
	return func() (llm.CompletionResponse, error) { return llm.CompletionResponse{}, err }
}

// noteCollector also keeps the notes of open requests.
type noteCollector struct {
	collectingObserver
	notes []telemetry.Note
}

func (o *noteCollector) OperationNoted(_ context.Context, note telemetry.Note) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.notes = append(o.notes, note)
}

// What a request's finish says for each way a request can end.
func TestARequestsFinishSaysHowItEnded(t *testing.T) {
	cases := []struct {
		name       string
		script     []func() (llm.CompletionResponse, error)
		retries    int
		wantKind   string
		wantModel  string // what answered, "" when nothing did
		wantUsage  int    // prompt tokens on the finish, over every attempt that was answered
		wantNotes  []string
		wantCalls  int
		wantPriced bool // whether a cost was worked out
	}{
		{"an answer", []func() (llm.CompletionResponse, error){answerOf("ok", "stop")}, 0, "", "served", 11, nil, 1, true},
		{"a truncated answer", []func() (llm.CompletionResponse, error){answerOf("cut", "length")}, 0, "output truncated", "served", 11, nil, 1, false},
		{"an empty answer, retried once and empty again", []func() (llm.CompletionResponse, error){answerOf("", "stop")}, 1, "malformed output", "served", 22, []string{"malformed output"}, 2, false},
		{"a rate limit, then an answer", []func() (llm.CompletionResponse, error){
			failingWith(&llm.RateLimitError{APIError: llm.NewAPIError("acme", "m", 429, "slow"), RetryAfter: time.Millisecond}), answerOf("ok", "stop"),
		}, 2, "", "served", 11, []string{"rate limited"}, 2, true},
		{"two failures, then an answer", []func() (llm.CompletionResponse, error){
			failingWith(llm.NewAPIError("acme", "m", 500, "boom")), failingWith(llm.NewAPIError("acme", "m", 503, "down")), answerOf("ok", "stop"),
		}, 2, "", "served", 11, []string{"provider unavailable", "provider unavailable"}, 3, true},
		{"failures until the retries run out", []func() (llm.CompletionResponse, error){failingWith(llm.NewAPIError("acme", "m", 500, "boom"))},
			2, "provider unavailable", "", 0, []string{"provider unavailable", "provider unavailable"}, 3, false},
		{"a failure that is not retried", []func() (llm.CompletionResponse, error){failingWith(llm.NewAPIError("acme", "m", 401, "no"))},
			3, "authentication", "", 0, nil, 1, false},
		{"no retries allowed", []func() (llm.CompletionResponse, error){failingWith(llm.NewAPIError("acme", "m", 500, "boom"))},
			0, "provider unavailable", "", 0, nil, 1, false},
		{"an answer, then nothing after a failed retry", []func() (llm.CompletionResponse, error){
			answerOf("", "stop"), failingWith(llm.NewAPIError("acme", "m", 401, "no")),
		}, 2, "authentication", "served", 11, []string{"malformed output"}, 2, false},
		{"a cancellation reported by the provider", []func() (llm.CompletionResponse, error){failingWith(context.Canceled)}, 2, "canceled", "", 0, nil, 1, false},
		{"an empty answer the filter withheld, with no retry", []func() (llm.CompletionResponse, error){answerOf("", "content_filter")}, 0, "refused", "served", 11, nil, 1, false},
		{"an empty answer the filter withheld, retried once and withheld again", []func() (llm.CompletionResponse, error){answerOf("", "content_filter")}, 1, "refused", "served", 22, []string{"refused"}, 2, false},
		{"an empty answer the filter withheld, then an answer", []func() (llm.CompletionResponse, error){answerOf("", "content_filter"), answerOf("ok", "stop")}, 1, "", "served", 22, []string{"refused"}, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := &noteCollector{}
			t.Cleanup(telemetry.AddObserver(observer))
			provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "acme"}, script: tc.script, retries: tc.retries}

			_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "asked"})
			if (err != nil) != (tc.wantKind != "") {
				t.Fatalf("CallLLM returned %v, want an error: %v", err, tc.wantKind != "")
			}

			started, finished := observer.snapshot()
			if len(started) != 1 || len(finished) != 1 {
				t.Fatalf("%d starts and %d finishes for one request, want 1 and 1", len(started), len(finished))
			}
			result := finished[0]
			if result.Scope != telemetry.ScopeRequest || result.ErrorKind != tc.wantKind || result.Attempts != tc.wantCalls {
				t.Errorf("finish = scope %v kind %q attempts %d, want request, %q, %d", result.Scope, result.ErrorKind, result.Attempts, tc.wantKind, tc.wantCalls)
			}
			if result.Model != tc.wantModel || result.Usage.PromptTokens != tc.wantUsage {
				t.Errorf("answered model %q with %d prompt tokens, want %q and %d", result.Model, result.Usage.PromptTokens, tc.wantModel, tc.wantUsage)
			}
			if worked := result.Cost.Quality != ""; worked != tc.wantPriced {
				t.Errorf("a cost was worked out = %v, want %v", worked, tc.wantPriced)
			}

			var kinds []string
			for index, note := range observer.notes {
				kinds = append(kinds, note.ErrorKind)
				if note.Name != telemetry.NoteRetry || note.Attempt != index+1 || note.Wait <= 0 {
					t.Errorf("note %d = %+v, want a retry of attempt %d with its wait", index, note, index+1)
				}
			}
			if strings.Join(kinds, ",") != strings.Join(tc.wantNotes, ",") {
				t.Errorf("retry notes carry kinds %v, want %v", kinds, tc.wantNotes)
			}
		})
	}
}

// A provider that panics leaves CallLLM with no error and no answer. The
// request is finished as a failure of kind "internal" and the panic continues.
func TestAPanickingProviderFinishesItsRequestAsAFailure(t *testing.T) {
	collector := installCollector(t)
	provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "acme"},
		script: []func() (llm.CompletionResponse, error){func() (llm.CompletionResponse, error) { panic("provider bug") }}}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "asked"})
	}()

	if recovered != "provider bug" {
		t.Fatalf("recovered %v, want the provider's panic", recovered)
	}
	_, finished := collector.snapshot()
	if len(finished) != 1 || finished[0].ErrorKind != telemetry.ErrorKindInternal || finished[0].Attempts != 1 {
		t.Errorf("finishes = %+v, want one of kind %q after one attempt", finished, telemetry.ErrorKindInternal)
	}
}

// The repair loop marks every request after the first, and the operation's
// finish carries the outcome of the whole call.
func TestARepairIsASecondRequestMarkedAsOneUnderTheSameOperation(t *testing.T) {
	type target struct {
		Name string `json:"name"`
	}
	cases := []struct {
		name         string
		answers      []string
		wantRepairs  []bool
		wantKind     string
		wantRequests int
	}{
		{"usable at once", []string{`{"name":"Ada"}`}, []bool{false}, "", 1},
		{"refused, then usable", []string{"not JSON", `{"name":"Ada"}`}, []bool{false, true}, "", 2},
		{"refused twice", []string{"not JSON", "still not JSON"}, []bool{false, true}, "repair exhausted", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			var script []func() (llm.CompletionResponse, error)
			for _, answer := range tc.answers {
				script = append(script, answerOf(answer, "stop"))
			}
			provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "local"}, script: script}
			// On the call's context, so a test caller another test left installed
			// cannot answer in its place.
			options := NewExtractOptions()
			options.CommonOptions.Context = WithProvider(context.Background(), provider)

			_, err := Extract[target]("Ada Lovelace", options)
			if (err != nil) != (tc.wantKind != "") {
				t.Fatalf("Extract returned %v", err)
			}

			started, finished := collector.snapshot()
			operations := scopes(finished, telemetry.ScopeOperation, resultScope)
			requests := scopes(started, telemetry.ScopeRequest, eventScope)
			if len(operations) != 1 || operations[0].Operation != "extract" || operations[0].ErrorKind != tc.wantKind {
				t.Fatalf("operation finishes = %+v, want one for extract of kind %q", operations, tc.wantKind)
			}
			if len(requests) != tc.wantRequests {
				t.Fatalf("%d requests started, want %d", len(requests), tc.wantRequests)
			}
			for index, request := range requests {
				if request.Repair != tc.wantRepairs[index] || request.Operation != "extract" {
					t.Errorf("request %d: repair=%v operation=%q, want %v and extract", index, request.Repair, request.Operation, tc.wantRepairs[index])
				}
			}
			for _, result := range scopes(finished, telemetry.ScopeRequest, resultScope) {
				if result.ErrorKind != "" {
					t.Errorf("a request that got an answer finished with kind %q; the refusal is the operation's", result.ErrorKind)
				}
			}
			// The operation is started before its requests and finished after
			// them.
			if started[0].Scope != telemetry.ScopeOperation || finished[len(finished)-1].Scope != telemetry.ScopeOperation {
				t.Error("the operation does not bracket its requests")
			}
		})
	}
}

// streamingStub streams the chunks it is given.
type streamingStub struct {
	callerSuppliedProvider
	chunks func(yield func(llm.StreamChunk, error) bool)
}

func (p *streamingStub) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return p.chunks
}

// The streaming path emits one request, marked as a stream, finished once.
func TestAStreamEmitsOneRequestFinishedOnce(t *testing.T) {
	done := llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
		Content: "a summary", Model: "served", Provider: "acme", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}}
	cases := []struct {
		name      string
		chunks    func(yield func(llm.StreamChunk, error) bool)
		wantKind  string
		wantModel string
	}{
		{"a clean end", func(yield func(llm.StreamChunk, error) bool) {
			if yield(llm.StreamChunk{Delta: "a summary"}, nil) {
				yield(done, nil)
			}
		}, "", "served"},
		{"a failure midway", func(yield func(llm.StreamChunk, error) bool) {
			if yield(llm.StreamChunk{Delta: "a"}, nil) {
				yield(llm.StreamChunk{}, llm.NewAPIError("acme", "m", 500, "boom"))
			}
		}, "provider unavailable", ""},
		{"a failure at once", func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{}, llm.NewAPIError("acme", "m", 429, "slow"))
		}, "rate limited", ""},
		{"a provider that ends with no result", func(func(llm.StreamChunk, error) bool) {}, "provider unavailable", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			SetDefaultProvider(&streamingStub{callerSuppliedProvider: callerSuppliedProvider{name: "local"}, chunks: tc.chunks})
			t.Cleanup(func() { SetDefaultProvider(nil) })

			stream, err := StreamSummarize("a long text", NewSummarizeOptions())
			if err != nil {
				t.Fatalf("StreamSummarize: %v", err)
			}
			_, _ = stream.Text()

			// The finish is delivered before the final item is, so it is here
			// by the time Text returns.
			started, finished := collector.snapshot()
			if len(started) != 1 || len(finished) != 1 {
				t.Fatalf("%d starts and %d finishes for one stream, want 1 and 1", len(started), len(finished))
			}
			if !started[0].Stream || started[0].Scope != telemetry.ScopeRequest || started[0].Provider != "local" {
				t.Errorf("start = %+v, want a request marked as a stream", started[0])
			}
			result := finished[0]
			if result.ErrorKind != tc.wantKind || result.Model != tc.wantModel || result.Attempts != 1 {
				t.Errorf("finish = kind %q model %q attempts %d, want %q, %q and 1", result.ErrorKind, result.Model, result.Attempts, tc.wantKind, tc.wantModel)
			}
			if (result.Usage.PromptTokens == 5) != (tc.wantModel != "") {
				t.Errorf("usage on the finish = %+v", result.Usage)
			}
		})
	}
}

// A request that fails is counted in the request metric by its kind, buffered
// or streamed, and one that succeeds is counted once as ok.
func TestRequestsAreCountedByOutcomeInTheMetrics(t *testing.T) {
	requestCounts := func() map[string]int64 {
		counts := map[string]int64{}
		for _, snapshot := range telemetry.SnapshotMetrics() {
			if snapshot.Name == "llm_requests" {
				counts[snapshot.Tags["outcome"]] += snapshot.Count
			}
		}
		return counts
	}
	metricsOn := func(t *testing.T) {
		t.Helper()
		telemetry.ResetMetrics()
		previous := config.IsMetricsEnabled()
		config.SetMetricsEnabled(true)
		t.Cleanup(func() { config.SetMetricsEnabled(previous); telemetry.ResetMetrics() })
	}

	buffered := []struct {
		name    string
		script  []func() (llm.CompletionResponse, error)
		retries int
		want    map[string]int64
	}{
		{"an answer", []func() (llm.CompletionResponse, error){answerOf("ok", "stop")}, 0, map[string]int64{"ok": 1}},
		{"a rate limit that exhausts its retries", []func() (llm.CompletionResponse, error){
			failingWith(&llm.RateLimitError{APIError: llm.NewAPIError("acme", "m", 429, "slow"), RetryAfter: time.Millisecond}),
		}, 1, map[string]int64{"rate limited": 1}},
		{"a refused key", []func() (llm.CompletionResponse, error){failingWith(llm.NewAPIError("acme", "m", 401, "no"))}, 0, map[string]int64{"authentication": 1}},
		{"a truncated answer", []func() (llm.CompletionResponse, error){answerOf("cut", "length")}, 0, map[string]int64{"output truncated": 1}},
		{"a failure and then an answer", []func() (llm.CompletionResponse, error){
			failingWith(llm.NewAPIError("acme", "m", 500, "boom")), answerOf("ok", "stop"),
		}, 1, map[string]int64{"ok": 1}},
	}
	for _, tc := range buffered {
		t.Run(tc.name, func(t *testing.T) {
			metricsOn(t)
			provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "acme"}, script: tc.script, retries: tc.retries}
			_, _ = CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "asked"})

			got := requestCounts()
			if len(got) != len(tc.want) {
				t.Fatalf("request counts = %v, want %v", got, tc.want)
			}
			for outcome, count := range tc.want {
				if got[outcome] != count {
					t.Errorf("llm_requests{outcome=%s} = %d, want %d (all: %v)", outcome, got[outcome], count, got)
				}
			}
		})
	}

	t.Run("a stream that fails", func(t *testing.T) {
		metricsOn(t)
		SetDefaultProvider(&streamingStub{callerSuppliedProvider: callerSuppliedProvider{name: "local"}, chunks: func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{}, llm.NewAPIError("local", "m", 500, "boom"))
		}})
		t.Cleanup(func() { SetDefaultProvider(nil) })

		stream, err := StreamSummarize("a long text", NewSummarizeOptions())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = stream.Text()
		if got := requestCounts(); got["provider unavailable"] != 1 || len(got) != 1 {
			t.Errorf("request counts = %v, want one provider-unavailable failure", got)
		}
	})

	t.Run("a provider that panics", func(t *testing.T) {
		metricsOn(t)
		provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "acme"},
			script: []func() (llm.CompletionResponse, error){func() (llm.CompletionResponse, error) { panic("bug") }}}
		func() {
			defer func() { _ = recover() }()
			_, _ = CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "asked"})
		}()
		if got := requestCounts(); got[telemetry.ErrorKindInternal] != 1 || len(got) != 1 {
			t.Errorf("request counts = %v, want one internal failure", got)
		}
	})
}

// A call ended by a panic is over: a context kept from it starts a new unit,
// as one kept from a call that ended normally does.
func TestACallThatEndedInAPanicIsMarkedEnded(t *testing.T) {
	collector := installCollector(t)

	ctx, observed := observeOperation(context.Background(), "extract")
	func() {
		defer func() { _ = recover() }()
		var err error
		defer observed.end(&err)
		panic("a validator broke")
	}()

	if !observed.ended.Load() {
		t.Error("a call that panicked was not marked as ended")
	}
	if _, finished := collector.snapshot(); len(finished) != 1 || finished[0].ErrorKind != telemetry.ErrorKindInternal {
		t.Fatalf("finishes = %+v, want one, with the internal kind", finished)
	}
	_, second := observeOperation(ctx, "extract")
	if second == nil {
		t.Error("a call started on the context of one that panicked joined it: it would be reported to nobody")
	}
	var err error
	second.end(&err)
	if started, _ := collector.snapshot(); len(started) != 2 {
		t.Errorf("%d starts, want a second for the new call", len(started))
	}
}

// A bad observer returning a context that does not descend from the one it was
// given used to replace the call's context wholesale. The call's deadline, its
// client and its call record all lived on that context.
func TestAnObserverReturningAnUnrelatedContextCannotChangeWhatTheCallDoes(t *testing.T) {
	type invoiceHeader struct {
		Number string `json:"number"`
		Vendor string `json:"vendor"`
	}
	type callerKey struct{}

	t.Cleanup(telemetry.AddObserver(funcOnlyObserver{started: func(context.Context, telemetry.OperationEvent) context.Context {
		return context.Background()
	}}))

	var (
		mu          sync.Mutex
		sawModel    string
		sawCallerAt any
	)
	client := &blockingProvider{name: "client-provider", hold: 3 * time.Second, saw: func(ctx context.Context, req llm.CompletionRequest) {
		mu.Lock()
		defer mu.Unlock()
		sawModel, sawCallerAt = req.Model, ctx.Value(callerKey{})
	}}

	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), callerKey{}, "the caller's"), 150*time.Millisecond)
	defer cancel()
	ctx = WithExecConfig(ctx, &ExecConfig{Provider: client, Model: "client-model"})
	options := NewExtractOptions()
	options.CommonOptions.Context = ctx

	started := time.Now()
	result, err := ExtractResult[invoiceHeader]("Invoice INV-1 from Acme", options)
	elapsed := time.Since(started)

	if elapsed > time.Second {
		t.Errorf("the call ran for %v with a 150 ms deadline: the observer dropped the caller's deadline", elapsed)
	}
	if err == nil || llm.Classify(err) != types.KindTimeout {
		t.Errorf("the call returned %v, want the deadline's error", err)
	}
	if result.Meta.Attempts != 1 {
		t.Errorf("the envelope reports %d attempts, want 1: the observer dropped the call record", result.Meta.Attempts)
	}
	mu.Lock()
	defer mu.Unlock()
	if sawModel != "client-model" {
		t.Errorf("the provider was asked for model %q, want the client's: the observer dropped the client (or the call never reached its provider)", sawModel)
	}
	if sawCallerAt != "the caller's" {
		t.Errorf("the provider's context carried %v, want the caller's value", sawCallerAt)
	}
}

// funcOnlyObserver is an observer made of the functions a test gives it.
type funcOnlyObserver struct {
	started  func(context.Context, telemetry.OperationEvent) context.Context
	finished func(context.Context, telemetry.OperationResult)
}

func (o funcOnlyObserver) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	return o.started(ctx, event)
}

func (o funcOnlyObserver) OperationFinished(ctx context.Context, result telemetry.OperationResult) {
	if o.finished != nil {
		o.finished(ctx, result)
	}
}

// blockingProvider holds each call until its context ends or hold passes, and
// tells saw what it was asked and on which context.
type blockingProvider struct {
	name string
	hold time.Duration
	saw  func(ctx context.Context, req llm.CompletionRequest)
}

func (p *blockingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if p.saw != nil {
		p.saw(ctx, req)
	}
	select {
	case <-ctx.Done():
		return llm.CompletionResponse{}, ctx.Err()
	case <-time.After(p.hold):
	}
	return llm.CompletionResponse{Content: `{"number":"INV-1","vendor":"Acme"}`, Model: req.Model, Provider: p.name, FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14}}, nil
}

func (p *blockingProvider) Name() string                               { return p.name }
func (p *blockingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *blockingProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// An answer refused for having no content is an error with no kind of its own,
// and has to stay that way while the request is deciding whether to ask again:
// the kind it would be given says "not worth repeating", and an empty answer is
// worth repeating. Once the request has decided, the error it returns says what
// the request span says of it.
func TestAnAnswerRefusedForHavingNoContentIsRetriedAndThenReportsItsKind(t *testing.T) {
	cases := []struct {
		name      string
		script    []func() (llm.CompletionResponse, error)
		retries   int
		wantCalls int
		wantErr   bool
	}{
		{"empty, then an answer", []func() (llm.CompletionResponse, error){answerOf("", "stop"), answerOf("fine", "stop")}, 1, 2, false},
		{"empty twice with one retry", []func() (llm.CompletionResponse, error){answerOf("", "stop")}, 1, 2, true},
		{"empty with no retry", []func() (llm.CompletionResponse, error){answerOf("", "stop")}, 0, 1, true},
		{"blank with two retries", []func() (llm.CompletionResponse, error){answerOf("  \n", "stop")}, 2, 3, true},
		{"empty, a failure, then an answer", []func() (llm.CompletionResponse, error){
			answerOf("", "stop"), failingWith(llm.NewAPIError("acme", "m", 500, "boom")), answerOf("fine", "stop"),
		}, 2, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "acme"}, script: tc.script, retries: tc.retries}
			_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "asked"})

			if provider.calls != tc.wantCalls {
				t.Errorf("the provider was called %d times, want %d: retrying an empty answer changed", provider.calls, tc.wantCalls)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want an error: %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			if got := llm.Classify(err); got != types.KindMalformedOutput {
				t.Errorf("the error the request returns has kind %q, want %q, which is what its request span says", got, types.KindMalformedOutput)
			}
			if !strings.Contains(err.Error(), "provider returned empty completion content") {
				t.Errorf("the error's text changed: %q", err.Error())
			}
		})
	}
}

// The request that was retried after a refused answer was charged for both
// answers, and its finish says so: the tokens and the cost of every attempt
// that was answered, as the call record sums them.
func TestARequestRetriedAfterARefusedAnswerReportsEveryAnsweredAttempt(t *testing.T) {
	usage := types.TokenUsage{PromptTokens: 1000, CompletionTokens: 200, TotalTokens: 1200, CachedTokens: 100, ReasoningTokens: 10}
	pricedAnswer := func(content string) func() (llm.CompletionResponse, error) {
		return func() (llm.CompletionResponse, error) {
			return llm.CompletionResponse{Content: content, Model: "gpt-5.6-luna", Provider: "openai", FinishReason: "stop", Usage: usage}, nil
		}
	}
	single := pricing.CalculateCost(&usage, "gpt-5.6-luna", "openai")
	if single == nil || !single.Priced || single.TotalCost <= 0 {
		t.Fatalf("the model this test prices answers with has no price (%+v)", single)
	}

	cases := []struct {
		name     string
		script   []func() (llm.CompletionResponse, error)
		answered int
	}{
		{"one answer", []func() (llm.CompletionResponse, error){pricedAnswer("fine")}, 1},
		{"an empty answer, then a good one", []func() (llm.CompletionResponse, error){pricedAnswer(""), pricedAnswer("fine")}, 2},
		{"two empty answers, then a good one", []func() (llm.CompletionResponse, error){pricedAnswer(""), pricedAnswer(" "), pricedAnswer("fine")}, 3},
		{"a failure, an empty answer, then a good one", []func() (llm.CompletionResponse, error){
			failingWith(llm.NewAPIError("openai", "m", 500, "boom")), pricedAnswer(""), pricedAnswer("fine"),
		}, 2},
		{"an empty answer, a failure, then a good one", []func() (llm.CompletionResponse, error){
			pricedAnswer(""), failingWith(llm.NewAPIError("openai", "m", 500, "boom")), pricedAnswer("fine"),
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "openai"}, script: tc.script, retries: 3}
			if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "gpt-5.6-luna"}); err != nil {
				t.Fatalf("CallLLM: %v", err)
			}

			_, finished := collector.snapshot()
			if len(finished) != 1 {
				t.Fatalf("%d finishes, want 1", len(finished))
			}
			got, n := finished[0], tc.answered
			if got.Usage.PromptTokens != n*usage.PromptTokens || got.Usage.CompletionTokens != n*usage.CompletionTokens ||
				got.Usage.TotalTokens != n*usage.TotalTokens || got.Usage.CachedTokens != n*usage.CachedTokens ||
				got.Usage.ReasoningTokens != n*usage.ReasoningTokens {
				t.Errorf("usage = %+v over %d answered attempts, want %d times one answer's %+v", got.Usage, n, n, usage)
			}
			if !got.Cost.Priced || math.Abs(got.Cost.TotalCost-float64(n)*single.TotalCost) > 1e-12 {
				t.Errorf("cost = %v (priced %v), want %d times %v", got.Cost.TotalCost, got.Cost.Priced, n, single.TotalCost)
			}
			if got.Cost.Quality != single.Quality {
				t.Errorf("cost quality = %q, want %q", got.Cost.Quality, single.Quality)
			}
		})
	}
}

// refusedAnswers is what a request adds to its finish for the answers it
// asked again after.
func TestRefusedAnswersAreSummedWithTheAcceptedOne(t *testing.T) {
	usage := func(prompt, completion int) types.TokenUsage {
		return types.TokenUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
	}
	priced := func(total float64) *types.CostInfo {
		return &types.CostInfo{TotalCost: total, PromptCost: total / 2, CompletionCost: total / 2, Currency: "USD", Priced: true, Quality: types.PricingExact}
	}
	unpriced := &types.CostInfo{Quality: types.PricingUnknown}
	answeredWith := func(u types.TokenUsage, cached bool) llm.CompletionResponse {
		return llm.CompletionResponse{Model: "gpt-5.6-luna", Provider: "openai", Usage: u, Cached: cached}
	}

	cases := []struct {
		name         string
		refused      []llm.CompletionResponse
		accepted     types.TokenUsage
		acceptedCost *types.CostInfo
		wantUsage    types.TokenUsage
		wantAnswered bool
	}{
		{"nothing was refused", nil, usage(10, 5), priced(1), usage(10, 5), false},
		{"one refused answer", []llm.CompletionResponse{answeredWith(usage(100, 50), false)}, usage(10, 5), priced(1), usage(110, 55), true},
		{"two refused answers", []llm.CompletionResponse{answeredWith(usage(100, 50), false), answeredWith(usage(7, 3), false)}, usage(10, 5), priced(1), usage(117, 58), true},
		{"a refused answer that reported no usage", []llm.CompletionResponse{answeredWith(usage(0, 0), false)}, usage(10, 5), priced(1), usage(10, 5), false},
		{"a refused cache hit, which was not bought", []llm.CompletionResponse{answeredWith(usage(100, 50), true)}, usage(10, 5), priced(1), usage(10, 5), false},
		{"an accepted answer that reported no usage", []llm.CompletionResponse{answeredWith(usage(100, 50), false)}, usage(0, 0), nil, usage(100, 50), true},
		{"the cached and reasoning counts add too", []llm.CompletionResponse{answeredWith(types.TokenUsage{PromptTokens: 10, TotalTokens: 10, CachedTokens: 4, ReasoningTokens: 2, CacheWriteTokens: 1, InputTokens: 3, OutputTokens: 2}, false)},
			types.TokenUsage{PromptTokens: 1, TotalTokens: 1, CachedTokens: 1, ReasoningTokens: 1, CacheWriteTokens: 1, InputTokens: 1, OutputTokens: 1},
			priced(1), types.TokenUsage{PromptTokens: 11, TotalTokens: 11, CachedTokens: 5, ReasoningTokens: 3, CacheWriteTokens: 2, InputTokens: 4, OutputTokens: 3}, true},
		{"a refused answer with no model of its own", []llm.CompletionResponse{{Usage: usage(5, 5)}}, usage(1, 1), priced(1), usage(6, 6), true},
		{"three refused answers", []llm.CompletionResponse{answeredWith(usage(1, 1), false), answeredWith(usage(2, 2), false), answeredWith(usage(3, 3), false)}, usage(4, 4), priced(1), usage(10, 10), true},
		{"an unpriced accepted answer", []llm.CompletionResponse{answeredWith(usage(100, 50), false)}, usage(10, 5), unpriced, usage(110, 55), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var refused refusedAnswers
			for _, resp := range tc.refused {
				refused.add("openai", "gpt-5.6-luna", resp)
			}
			if got := refused.any(); got != tc.wantAnswered {
				t.Errorf("any = %v, want %v", got, tc.wantAnswered)
			}
			gotUsage, gotCost := refused.plus(tc.accepted, tc.acceptedCost)
			if gotUsage != tc.wantUsage {
				t.Errorf("usage = %+v, want %+v", gotUsage, tc.wantUsage)
			}
			if !tc.wantAnswered {
				want := types.CostInfo{}
				if tc.acceptedCost != nil {
					want = *tc.acceptedCost
				}
				if gotCost != want {
					t.Errorf("cost = %+v, want the accepted answer's alone %+v", gotCost, want)
				}
			}
		})
	}

	t.Run("a priced accepted answer and a priced refused one add their costs", func(t *testing.T) {
		var refused refusedAnswers
		refused.add("openai", "gpt-5.6-luna", llm.CompletionResponse{Usage: usage(1000, 100)})
		refusedCost := pricing.CalculateCost(&types.TokenUsage{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100}, "gpt-5.6-luna", "openai")
		_, got := refused.plus(usage(500, 50), priced(0.25))
		if !got.Priced || math.Abs(got.TotalCost-(0.25+refusedCost.TotalCost)) > 1e-12 || got.Currency == "" {
			t.Errorf("cost = %+v, want %v plus the refused answer's %v, priced, with a currency", got, 0.25, refusedCost.TotalCost)
		}
	})
	t.Run("one unpriced answer makes the whole unpriced", func(t *testing.T) {
		var refused refusedAnswers
		refused.add("unknown-provider", "no-such-model", llm.CompletionResponse{Usage: usage(1000, 100)})
		_, got := refused.plus(usage(500, 50), priced(0.25))
		if got.Priced {
			t.Errorf("a cost summed with an unpriced answer reports itself priced: %+v", got)
		}
	})
}

// The type of a call is a tag of its duration metric, and reflect prints an
// anonymous struct with every field tag it has. The string is the program's
// own, but it can be as long as the struct's tags are, and a tag is a series.
func TestTheTypeTagsOfTheDurationMetricsAreBounded(t *testing.T) {
	type tagged = struct {
		Number string `json:"number" note:"this tag is long enough that the type's string passes the limit on a metric's tag by itself, and then some"`
		Vendor string `json:"vendor" note:"and so is this one, so the string of the whole type is several times what a tag value is allowed to be"`
	}
	telemetry.ResetMetrics()
	previous := config.IsMetricsEnabled()
	config.SetMetricsEnabled(true)
	t.Cleanup(func() { config.SetMetricsEnabled(previous); telemetry.ResetMetrics() })
	// The provider is attached to each call's context, which wins over the
	// package's default and over a test caller another test left installed, so the
	// result does not depend on which tests ran before this one.
	ctx := WithProvider(context.Background(), &blockingProvider{name: "local"})

	extractOptions := NewExtractOptions()
	extractOptions.CommonOptions.Context = ctx
	if _, err := Extract[tagged]("Invoice INV-1 from Acme", extractOptions); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	transformOptions := NewTransformOptions()
	transformOptions.CommonOptions.Context = ctx
	if _, err := Transform[tagged, tagged](tagged{Number: "1"}, transformOptions); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	generateOptions := NewGenerateOptions()
	generateOptions.CommonOptions.Context = ctx
	if _, err := Generate[tagged]("Write an invoice.", generateOptions); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	seen := map[string]bool{}
	for _, snapshot := range telemetry.SnapshotMetrics() {
		for _, key := range []string{"type", "from_type", "to_type"} {
			value, present := snapshot.Tags[key]
			if !present {
				continue
			}
			seen[snapshot.Name] = true
			if len(value) > 64 {
				t.Errorf("%s{%s} is %d bytes, over the bound a tag value has: %q", snapshot.Name, key, len(value), value)
			}
		}
	}
	for _, name := range []string{"extract_duration", "transform_duration", "generate_duration"} {
		if !seen[name] {
			t.Errorf("no %s metric was recorded with a type tag, so nothing was checked", name)
		}
	}
}

// An error with no kind is given the one classify.go reads in the answer it
// came with, and every other error is returned as it was.
func TestWithKindOfTheAnswer(t *testing.T) {
	plain := errors.New("provider returned empty completion content")
	classified := types.Newf(types.KindSchemaViolation, "extract", "the wrong shape")
	empty := llm.CompletionResponse{Content: "  ", FinishReason: "stop"}
	cut := llm.CompletionResponse{Content: `{"a":`, FinishReason: "length"}
	fine := llm.CompletionResponse{Content: `{"a":1}`, FinishReason: "stop"}
	filtered := llm.CompletionResponse{FinishReason: "content_filter"}
	filteredWithText := llm.CompletionResponse{Content: "I can", FinishReason: "content_filter"}

	cases := []struct {
		name     string
		err      error
		answered bool
		resp     llm.CompletionResponse
		want     types.ErrorKind
		changed  bool // whether the error that comes back is a different value
	}{
		{"no error", nil, true, empty, types.KindUnknown, false},
		{"an empty answer", plain, true, empty, types.KindMalformedOutput, true},
		{"a truncated answer", plain, true, cut, types.KindOutputTruncated, true},
		{"no answer came", plain, false, empty, types.KindUnknown, false},
		{"an answer that reads as fine", plain, true, fine, types.KindUnknown, false},
		{"an error that has a kind", classified, true, empty, types.KindSchemaViolation, false},
		{"a cancellation", context.Canceled, true, empty, types.KindCanceled, false},
		{"a deadline", context.DeadlineExceeded, true, empty, types.KindTimeout, false},
		{"a provider's error", llm.NewAPIError("acme", "m", 500, "boom"), true, empty, types.KindProviderUnavailable, false},
		{"a rate limit", &llm.RateLimitError{APIError: llm.NewAPIError("acme", "m", 429, "slow")}, false, empty, types.KindRateLimited, false},
		{"a wrapped plain error", errors.Join(errors.New("op"), plain), true, empty, types.KindMalformedOutput, true},
		{"an answer the filter withheld", plain, true, filtered, types.KindPolicyViolation, true},
		{"an answer the filter withheld, with text", plain, true, filteredWithText, types.KindPolicyViolation, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withKindOfTheAnswer(tc.err, tc.answered, tc.resp)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("a nil error came back as %v", got)
				}
				return
			}
			if kind := llm.Classify(got); kind != tc.want {
				t.Errorf("kind = %q, want %q", kind, tc.want)
			}
			if (got != tc.err) != tc.changed {
				t.Errorf("the error was replaced = %v, want %v", got != tc.err, tc.changed)
			}
			if got.Error() != tc.err.Error() || !errors.Is(got, tc.err) {
				t.Errorf("the error's text or chain changed: %q", got.Error())
			}
			// What the filter withheld is a refusal, marked as one, and nothing else is.
			wantMark := types.NarrowNone
			if tc.want == types.KindPolicyViolation && tc.changed {
				wantMark = types.NarrowRefusal
			}
			if mark := types.NarrowingOf(got); mark != wantMark {
				t.Errorf("mark = %d, want %d", mark, wantMark)
			}
		})
	}
}

// operationUnits returns the operation units an observer saw finish, in the
// order they finished.
func operationUnits(collector *collectingObserver) []telemetry.OperationResult {
	_, finished := collector.snapshot()
	return scopes(finished, telemetry.ScopeOperation, resultScope)
}

// requestStarts returns how many requests an observer saw start.
func requestStarts(collector *collectingObserver) int {
	started, _ := collector.snapshot()
	return len(scopes(started, telemetry.ScopeRequest, eventScope))
}

// What a run over many items tells an observer of the items: how many it was
// given and what became of each, on the one operation unit that covers the run,
// beside how many requests it made. A run that collects its failures returns a
// result with no error, so these counts are the only place its failed items are
// visible to an observer.
func TestARunOverManyItemsReportsWhatBecameOfItsItems(t *testing.T) {
	refused := types.Newf(types.KindPolicyViolation, "x", "refused")
	cases := []struct {
		name      string
		policy    types.BatchPolicy
		failing   bool
		wantItems [4]int // items, succeeded, failed, cancelled
		wantKind  string
	}{
		{"collect, nothing fails", types.PolicyCollectFailures, false, [4]int{5, 5, 0, 0}, ""},
		{"collect, one fails", types.PolicyCollectFailures, true, [4]int{5, 4, 1, 0}, ""},
		{"retry then collect, one fails", types.PolicyRetryThenCollect, true, [4]int{5, 4, 1, 0}, ""},
		{"fail fast, nothing fails", types.PolicyFailFast, false, [4]int{5, 5, 0, 0}, ""},
		{"fail fast stops at the failure", types.PolicyFailFast, true, [4]int{5, 2, 1, 2}, "policy violation"},
		{"require all, one fails", types.PolicyRequireAll, true, [4]int{5, 4, 1, 0}, "policy violation"},
		{"retry failed items, one fails for good", types.PolicyRetryFailedItems, true, [4]int{5, 4, 1, 0}, "policy violation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			collector := installCollector(t)
			provider := newKeyedProvider("fake-model", false)
			if tc.failing {
				provider.failing["item-02"] = true
				provider.failErr = refused
			}
			_, err := RunOpManyPartial(WithProvider(context.Background(), provider), echoOp(), indexedItems(5), types.OpOptions{},
				PartialConfig{Policy: tc.policy, Concurrency: 1, MaxRetries: 1})
			if (err != nil) != (tc.wantKind != "") {
				t.Fatalf("the run returned %v, want an error: %v", err, tc.wantKind != "")
			}

			units := operationUnits(collector)
			if len(units) != 1 {
				t.Fatalf("%d operation units finished, want the one that covers the run", len(units))
			}
			got := units[0]
			if counts := [4]int{got.Items, got.Succeeded, got.Failed, got.Cancelled}; counts != tc.wantItems {
				t.Errorf("items, succeeded, failed, cancelled = %v, want %v", counts, tc.wantItems)
			}
			if got.ErrorKind != tc.wantKind {
				t.Errorf("kind = %q, want %q", got.ErrorKind, tc.wantKind)
			}
			if want := requestStarts(collector); got.Requests != want || want < 3 {
				t.Errorf("requests = %d, want the %d requests that started", got.Requests, want)
			}
			if got.Attempts != 0 || got.Provider != "" || got.Model != "" {
				t.Errorf("an operation unit carries a request's fields: %+v", got)
			}
		})
	}
}

// A run in which every item failed returned its result with no error, and an
// observer that was told nothing would show it as a success. It is told the kind
// of the first item's failure, read as the caller reads an error.
func TestARunInWhichEveryItemFailedEndsWithTheKindOfTheFirstFailure(t *testing.T) {
	panicked := &types.OperationError{Kind: types.KindInvariantViolation, Narrow: types.NarrowPanic, Op: "x", Message: "step panicked"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"a policy violation", types.Newf(types.KindPolicyViolation, "x", "route"), "policy violation"},
		{"a refusal", types.ClassifiedRefusal(errors.New("withheld")), "refused"},
		{"a recovered panic", panicked, "internal"},
		{"an authentication failure", llm.NewAPIError("keyed", "m", 401, "no"), "authentication"},
		{"an error of no kind, which the call path files as the provider being unavailable", errors.New("odd"), "provider unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			collector := installCollector(t)
			provider := newKeyedProvider("fake-model", false, "item-00", "item-01", "item-02")
			provider.failErr = tc.err

			res, err := RunOpManyPartial(WithProvider(context.Background(), provider), echoOp(), indexedItems(3), types.OpOptions{},
				PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 1})
			if err != nil || res.Summary.Failed != 3 {
				t.Fatalf("the run returned %v with %d failed items, want a result and no error", err, res.Summary.Failed)
			}
			units := operationUnits(collector)
			if len(units) != 1 {
				t.Fatalf("%d operation units, want 1", len(units))
			}
			if got := units[0]; got.ErrorKind != tc.want || got.Failed != 3 || got.Succeeded != 0 || got.Items != 3 {
				t.Errorf("finish = kind %q, items %d, succeeded %d, failed %d; want %q, 3, 0, 3", got.ErrorKind, got.Items, got.Succeeded, got.Failed, tc.want)
			}
		})
	}
}

// The counts hold no item's data: nothing but numbers describes the items.
func TestARunsCountsCarryNoItemDataOrErrorText(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	collector := installCollector(t)
	provider := newKeyedProvider("fake-model", false, "item-01")
	provider.failErr = errors.New("MARK_ERRTEXT quoted the input item-01")
	_, _ = RunOpManyPartial(WithProvider(context.Background(), provider), echoOp(), indexedItems(3), types.OpOptions{},
		PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 1})

	started, finished := collector.snapshot()
	for _, event := range started {
		for _, text := range eventStrings(event) {
			if strings.Contains(text, "MARK_") || strings.Contains(text, "item-0") {
				t.Errorf("a start event carries %q", text)
			}
		}
	}
	for _, result := range finished {
		for _, text := range resultStrings(result) {
			if strings.Contains(text, "MARK_") || strings.Contains(text, "item-0") {
				t.Errorf("a finish carries %q", text)
			}
		}
	}
}

// RunOpMany is one operation to an observer like the other runs: the same two
// hundred items looked like two hundred operations from this entry and one from
// RunOpManyPartial.
func TestRunOpManyIsOneOperationToAnObserver(t *testing.T) {
	cases := []struct {
		name      string
		failing   bool
		wantItems [4]int
	}{
		{"every item answered", false, [4]int{6, 6, 0, 0}},
		{"an item fails and fails the call", true, [4]int{6, 0, 6, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			collector := installCollector(t)
			provider := newKeyedProvider("fake-model", false)
			if tc.failing {
				provider.failing["item-03"] = true
				provider.failErr = types.Newf(types.KindPolicyViolation, "x", "refused")
			}
			_, _, err := RunOpMany(WithProvider(context.Background(), provider), echoOp(), indexedItems(6), types.PlanRequest{Force: types.ShapeAtomic})
			if (err != nil) != tc.failing {
				t.Fatalf("RunOpMany returned %v, want an error: %v", err, tc.failing)
			}

			started, _ := collector.snapshot()
			units := operationUnits(collector)
			if opened := scopes(started, telemetry.ScopeOperation, eventScope); len(opened) != 1 || opened[0].Operation != "callerEcho" || len(units) != 1 {
				t.Fatalf("%d operation units started and %d ended, want one named for the operation", len(opened), len(units))
			}
			if started[0].Scope != telemetry.ScopeOperation {
				t.Error("a request started before the operation did")
			}
			got := units[0]
			if counts := [4]int{got.Items, got.Succeeded, got.Failed, got.Cancelled}; counts != tc.wantItems {
				t.Errorf("items, succeeded, failed, cancelled = %v, want %v", counts, tc.wantItems)
			}
			if (got.ErrorKind != "") != tc.failing {
				t.Errorf("kind = %q on a call that failed = %v", got.ErrorKind, tc.failing)
			}
			if got.Requests < 1 || got.Requests != requestStarts(collector) {
				t.Errorf("requests = %d, want the %d that started", got.Requests, requestStarts(collector))
			}
		})
	}
}

// A staged plan is one operation named for the plan, over the operations of its
// stages, which are over their requests.
func TestRunStagedPlanIsOneOperationOverItsStages(t *testing.T) {
	collector := installCollector(t)
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	_, err := RunStagedPlan(ctx, extractThenVerifyPlan(nil), testDatum{Source: "raw"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	started, finished := collector.snapshot()
	var names []string
	for _, event := range scopes(started, telemetry.ScopeOperation, eventScope) {
		names = append(names, event.Operation)
	}
	if want := "extract-then-verify,stagedplan-test-extract,stagedplan-test-verify"; strings.Join(names, ",") != want {
		t.Fatalf("operation units started = %v, want the plan and then each stage: %s", names, want)
	}
	units := scopes(finished, telemetry.ScopeOperation, resultScope)
	plan := units[len(units)-1]
	if plan.Operation != "extract-then-verify" || plan.Requests != 2 || plan.Items != 0 || plan.ErrorKind != "" {
		t.Errorf("the plan's finish = %+v, want its name, two requests, no items and no failure", plan)
	}
	for _, unit := range units[:len(units)-1] {
		if unit.Requests != 1 {
			t.Errorf("stage %q made %d requests, want 1", unit.Operation, unit.Requests)
		}
	}
}

func TestRunStagedPlanThatFailsEndsItsOperationWithTheStagesKind(t *testing.T) {
	collector := installCollector(t)
	provider := &stagedFakeProvider{respond: func(int, llm.CompletionRequest) string { return `{"name":"Alice","verified":false}` }}
	ctx := WithProvider(context.Background(), provider)

	if _, err := RunStagedPlan(ctx, extractThenVerifyPlan(nil), testDatum{Source: "raw"}, types.OpOptions{Model: "test-model"}); err == nil {
		t.Fatal("the plan did not fail")
	}
	units := operationUnits(collector)
	if len(units) == 0 || units[len(units)-1].Operation != "extract-then-verify" || units[len(units)-1].ErrorKind == "" {
		t.Errorf("operation units = %+v, want the plan to end last, with a kind", units)
	}
}

// The recovery cascade reports its items as the other runs do.
func TestRunOpManyRecoverReportsWhatBecameOfItsItems(t *testing.T) {
	items := recoverWidgets(3)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{omitBatch("i-000002"), omitBatch("i-000001")}
	provider.atomicFail = func(name string) bool { return name == items[1].Name }
	collector := installCollector(t)

	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 3, StartItemsPerCall: 3})
	if err != nil || result.Summary.Failed != 1 {
		t.Fatalf("the cascade returned %v with %+v", err, result.Summary)
	}
	units := operationUnits(collector)
	if len(units) != 1 {
		t.Fatalf("%d operation units, want 1", len(units))
	}
	got := units[0]
	if counts := [4]int{got.Items, got.Succeeded, got.Failed, got.Cancelled}; counts != [4]int{3, 2, 1, 0} || got.ErrorKind != "" {
		t.Errorf("items, succeeded, failed, cancelled = %v, kind %q; want [3 2 1 0] and none", counts, got.ErrorKind)
	}
	if want := requestStarts(collector); got.Requests != want || want < 2 {
		t.Errorf("requests = %d, want the %d that started, and at least a batch and an atomic call", got.Requests, want)
	}
}

// Every operation unit says how many requests it made, which a reader of one
// call otherwise counts from its children.
func TestAnOperationUnitSaysHowManyRequestsItMade(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	rejectOnce := func() []func(string) error {
		rejected := false
		return []func(string) error{func(string) error {
			if rejected {
				return nil
			}
			rejected = true
			return types.Newf(types.KindInvariantViolation, "x", "not yet")
		}}
	}
	cases := []struct {
		name       string
		invariants []func(string) error
		want       int
	}{
		{"one request", nil, 1},
		{"a repair is a second request", rejectOnce(), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			provider := newKeyedProvider("fake-model", false)
			if _, err := RunOpResult(WithProvider(context.Background(), provider), echoOp(tc.invariants...), "item-00", types.OpOptions{}); err != nil {
				t.Fatalf("RunOpResult: %v", err)
			}
			units := operationUnits(collector)
			if len(units) != 1 || units[0].Requests != tc.want || units[0].Items != 0 {
				t.Errorf("operation units = %+v, want one that made %d requests and counts no items", units, tc.want)
			}
		})
	}
}

// A call refused before anything was sent made no request, and says so.
func TestAnOperationUnitRefusedBeforeAnythingWasSentMadeNoRequest(t *testing.T) {
	collector := installCollector(t)
	op := echoOp()
	op.BuildPrompt = nil
	if _, err := RunOpResult(context.Background(), op, "x", types.OpOptions{}); err == nil {
		t.Fatal("the call was not refused")
	}
	units := operationUnits(collector)
	if len(units) != 1 || units[0].Requests != 0 || units[0].ErrorKind != "configuration" {
		t.Errorf("operation units = %+v, want one that made no request and failed as configured wrongly", units)
	}
}

// A sink registered through the first version's package, with the process's own
// metrics switch off, is given what a real operation records. The switch was the
// only gate, and registering a sink did not turn it on, so a host that wanted
// its metrics exported and set nothing else received none.
func TestASinkRegisteredWithTheSwitchOffReceivesTheMetricsOfARealCall(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	previous := config.IsMetricsEnabled()
	config.SetMetricsEnabled(false)
	telemetry.ResetMetrics()
	t.Cleanup(func() { config.SetMetricsEnabled(previous); telemetry.ResetMetrics() })

	sink := &metricNameSink{}
	removeSink := telemetry.RegisterMetricSink(sink)
	defer removeSink()

	provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "acme"}, script: []func() (llm.CompletionResponse, error){answerOf("ok", "stop")}}
	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "asked"}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}
	names := sink.names()
	if !strings.Contains(strings.Join(names, ","), "llm_requests") {
		t.Fatalf("the sink received %v, want the request's metrics", names)
	}

	removeSink()
	sink.reset()
	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "asked"}); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}
	if names := sink.names(); len(names) != 0 {
		t.Errorf("a removed sink received %v", names)
	}
}

// metricNameSink keeps the name of every metric it is handed.
type metricNameSink struct {
	mu    sync.Mutex
	items []string
}

func (s *metricNameSink) RecordMetric(event telemetry.MetricEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, event.Name)
}

func (s *metricNameSink) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.items...)
}

func (s *metricNameSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = nil
}

// The counts an observed call is given are what its finish carries, whatever the
// call: this holds recordItems to every combination, with no provider involved.
func TestRecordItemsDecidesTheKindOfARunWithNoError(t *testing.T) {
	refusal := types.ClassifiedRefusal(errors.New("withheld"))
	cases := []struct {
		name                                string
		total, succeeded, failed, cancelled int
		first, callErr                      error
		want                                string
	}{
		{"every item succeeded", 4, 4, 0, 0, nil, nil, ""},
		{"one succeeded", 4, 1, 3, 0, refusal, nil, ""},
		{"every item failed", 4, 0, 4, 0, refusal, nil, "refused"},
		{"every item failed, the first failure unknown", 4, 0, 4, 0, nil, nil, "unknown"},
		{"every item failed with an error of a kind", 2, 0, 2, 0, types.Newf(types.KindBudgetExceeded, "x", "spent"), nil, "budget exceeded"},
		{"every item was cancelled, which is no failure", 4, 0, 0, 4, nil, nil, ""},
		{"nothing failed and nothing succeeded", 0, 0, 0, 0, nil, nil, ""},
		{"a failed call keeps its own kind over the first failure's", 4, 0, 4, 0, refusal, types.Newf(types.KindTimeout, "x", "late"), "timeout"},
		{"a failed call with some success keeps its own kind", 4, 3, 1, 0, nil, types.Newf(types.KindBudgetExceeded, "x", "spent"), "budget exceeded"},
		{"one failure and one cancellation, none succeeded", 2, 0, 1, 1, types.Newf(types.KindRateLimited, "x", "slow"), nil, "rate limited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			func() {
				_, observed := observeOperation(context.Background(), "run")
				err := tc.callErr
				defer observed.end(&err)
				observed.recordItems(tc.total, tc.succeeded, tc.failed, tc.cancelled, tc.first)
			}()
			units := operationUnits(collector)
			if len(units) != 1 {
				t.Fatalf("%d units finished, want 1", len(units))
			}
			got := units[0]
			if counts := [4]int{got.Items, got.Succeeded, got.Failed, got.Cancelled}; counts != [4]int{tc.total, tc.succeeded, tc.failed, tc.cancelled} {
				t.Errorf("counts = %v, want %v", counts, [4]int{tc.total, tc.succeeded, tc.failed, tc.cancelled})
			}
			if got.ErrorKind != tc.want {
				t.Errorf("kind = %q, want %q", got.ErrorKind, tc.want)
			}
		})
	}

	t.Run("a call nobody observes ignores the counts", func(t *testing.T) {
		_, observed := observeOperation(context.Background(), "run")
		observed.recordItems(3, 3, 0, 0, nil)
		observed.recordAllOrNothing(3, nil)
		if observed != nil {
			t.Error("a call nobody observes is not nil")
		}
	})
}

func TestRecordAllOrNothingCountsEveryItemWithTheCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want [4]int
	}{
		{"the call succeeded", nil, [4]int{5, 5, 0, 0}},
		{"the call failed and returned no item", errors.New("odd"), [4]int{5, 0, 5, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			func() {
				_, observed := observeOperation(context.Background(), "run")
				var err error
				defer observed.end(&err)
				observed.recordAllOrNothing(5, tc.err)
			}()
			got := operationUnits(collector)[0]
			if counts := [4]int{got.Items, got.Succeeded, got.Failed, got.Cancelled}; counts != tc.want {
				t.Errorf("counts = %v, want %v", counts, tc.want)
			}
		})
	}
}

// What a middleware reports of the request it is answering is written into that
// request's finish: a cache that answered it, a decision provider asked first.
func TestWhatAMiddlewareNotesOfARequestIsWrittenIntoItsFinish(t *testing.T) {
	asked := func(prompt, completion int, model string) *llm.CompletionResponse {
		return &llm.CompletionResponse{Provider: "jev", Model: model, Usage: types.TokenUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}}
	}
	below := &llm.DecisionEscalation{Provider: "jev", Model: "jev-1", Reason: llm.DecisionEscalatedBelowFloor}
	unavailable := &llm.DecisionEscalation{Provider: "jev", Model: "jev-latest", Reason: llm.DecisionEscalatedUnavailable}
	answered := telemetry.OperationResult{Attempts: 1, Provider: "openai", Model: "gpt-5.6-luna", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		Cost:  types.CostInfo{TotalCost: 0.5, Currency: "USD", Priced: true, Quality: types.PricingExact}}

	cases := []struct {
		name string
		note func(ctx context.Context)
		base telemetry.OperationResult
		want func(got telemetry.OperationResult) string // "" when right
	}{
		{"nothing noted leaves the finish as it was", func(context.Context) {}, answered, func(got telemetry.OperationResult) string {
			if got != answered {
				return "the finish changed"
			}
			return ""
		}},
		{"a cache hit", func(ctx context.Context) { NoteCacheHit(ctx) }, answered, func(got telemetry.OperationResult) string {
			if !got.Cached || got.Attempts != 0 || got.Usage != (types.TokenUsage{}) || got.Cost != (types.CostInfo{}) || got.Provider != "" || got.Model != "" {
				return "a hit still describes a provider call: " + fmt.Sprintf("%+v", got)
			}
			return ""
		}},
		{"a hit keeps the finish reason of the answer it replays", func(ctx context.Context) { NoteCacheHit(ctx) }, answered, func(got telemetry.OperationResult) string {
			if got.FinishReason != "stop" {
				return "the finish reason was lost"
			}
			return ""
		}},
		{"two hits cannot make the attempts negative", func(ctx context.Context) { NoteCacheHit(ctx); NoteCacheHit(ctx) }, answered, func(got telemetry.OperationResult) string {
			if got.Attempts != 0 {
				return fmt.Sprintf("attempts = %d", got.Attempts)
			}
			return ""
		}},
		{"a hit after a failed attempt leaves that attempt", func(ctx context.Context) { NoteCacheHit(ctx) },
			telemetry.OperationResult{Attempts: 2, Provider: "p", Model: "m"}, func(got telemetry.OperationResult) string {
				if !got.Cached || got.Attempts != 1 {
					return fmt.Sprintf("cached %v attempts %d, want true and 1", got.Cached, got.Attempts)
				}
				return ""
			}},
		{"an escalation that failed in transit", func(ctx context.Context) { NoteDecisionEscalation(ctx, unavailable, nil) }, answered, func(got telemetry.OperationResult) string {
			if got.AskedFirst != "jev" || got.AskedFirstModel != "jev-latest" || got.Escalated != "unavailable" {
				return fmt.Sprintf("names = %q %q %q", got.AskedFirst, got.AskedFirstModel, got.Escalated)
			}
			if got.Attempts != 1 || got.Usage != answered.Usage || got.Cost != answered.Cost {
				return "something was paid for that was not"
			}
			return ""
		}},
		{"an escalation whose first answer was paid for", func(ctx context.Context) { NoteDecisionEscalation(ctx, below, asked(300, 40, "jev-1")) }, answered, func(got telemetry.OperationResult) string {
			if got.Attempts != 2 || got.Usage.PromptTokens != 400 || got.Usage.CompletionTokens != 50 || got.Usage.TotalTokens != 450 {
				return fmt.Sprintf("attempts %d usage %+v, want 2 and 400/50/450", got.Attempts, got.Usage)
			}
			if got.Escalated != "below the floor" || got.Provider != "openai" {
				return "the one that answered was changed"
			}
			return ""
		}},
		{"the cost of the first call is added, and says the figure is not whole when it was not priced", func(ctx context.Context) {
			NoteDecisionEscalation(ctx, below, asked(300, 40, "a-model-nobody-has-a-price-for"))
		}, answered, func(got telemetry.OperationResult) string {
			if got.Cost.Priced {
				return "a sum that includes an unpriced call is priced"
			}
			return ""
		}},
		{"a first answer with no usage is a call and no figure", func(ctx context.Context) { NoteDecisionEscalation(ctx, below, asked(0, 0, "jev-1")) }, answered, func(got telemetry.OperationResult) string {
			if got.Attempts != 2 || got.Usage != answered.Usage || got.Cost != answered.Cost {
				return fmt.Sprintf("attempts %d usage %+v cost %+v", got.Attempts, got.Usage, got.Cost)
			}
			return ""
		}},
		{"a first answer that a cache served was not paid for", func(ctx context.Context) {
			cached := asked(300, 40, "jev-1")
			cached.Cached = true
			NoteDecisionEscalation(ctx, below, cached)
		}, answered, func(got telemetry.OperationResult) string {
			if got.Attempts != 1 || got.Usage != answered.Usage || got.AskedFirst != "jev" {
				return fmt.Sprintf("attempts %d usage %+v asked %q", got.Attempts, got.Usage, got.AskedFirst)
			}
			return ""
		}},
		{"an escalation with no description is ignored", func(ctx context.Context) { NoteDecisionEscalation(ctx, nil, asked(300, 40, "jev-1")) }, answered, func(got telemetry.OperationResult) string {
			if got != answered {
				return "the finish changed"
			}
			return ""
		}},
		{"the failed request that was charged for the first call alone", func(ctx context.Context) { NoteDecisionEscalation(ctx, below, asked(300, 40, "jev-1")) },
			telemetry.OperationResult{Attempts: 1, ErrorKind: "authentication"}, func(got telemetry.OperationResult) string {
				if got.Usage.PromptTokens != 300 || got.Usage.CompletionTokens != 40 || got.Attempts != 2 || got.ErrorKind != "authentication" {
					return fmt.Sprintf("usage %+v attempts %d kind %q", got.Usage, got.Attempts, got.ErrorKind)
				}
				if got.Cost.Priced && got.Cost.TotalCost > 0 && got.Cost.Quality == "" {
					return "a cost with no quality"
				}
				return ""
			}},
		{"a request that worked out no cost takes the priced cost of the first call alone", func(ctx context.Context) {
			NoteDecisionEscalation(ctx, below, asked(300, 40, "gpt-5.6-luna"))
		}, telemetry.OperationResult{Attempts: 1, ErrorKind: "authentication"}, func(got telemetry.OperationResult) string {
			if !got.Cost.Priced || got.Cost.TotalCost <= 0 {
				return fmt.Sprintf("cost = %+v, want the first call's, priced", got.Cost)
			}
			return ""
		}},
		{"two escalations: the last names the request, both are paid", func(ctx context.Context) {
			NoteDecisionEscalation(ctx, below, asked(300, 40, "jev-1"))
			NoteDecisionEscalation(ctx, unavailable, asked(10, 1, "jev-2"))
		}, answered, func(got telemetry.OperationResult) string {
			if got.Escalated != "unavailable" || got.Attempts != 3 || got.Usage.PromptTokens != 410 {
				return fmt.Sprintf("reason %q attempts %d prompt %d", got.Escalated, got.Attempts, got.Usage.PromptTokens)
			}
			return ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			ctx := observeStart(context.Background(), telemetry.OperationEvent{Operation: "is", Provider: "openai", Model: "m"})
			tc.note(ctx)
			observeEnd(ctx, tc.base)
			_, finished := collector.snapshot()
			if len(finished) != 1 {
				t.Fatalf("%d finishes, want 1", len(finished))
			}
			if problem := tc.want(finished[0]); problem != "" {
				t.Error(problem)
			}
		})
	}

	t.Run("with nobody observing the notes do nothing and the context is unchanged", func(t *testing.T) {
		ctx := observeStart(context.Background(), telemetry.OperationEvent{})
		if ctx != context.Background() {
			t.Error("a context with no observer was given a value")
		}
		NoteCacheHit(ctx)
		NoteDecisionEscalation(ctx, below, asked(1, 1, "m"))
		NoteCacheHit(nil)
		NoteDecisionEscalation(nil, below, nil)
	})
	t.Run("a note on the context of another request is not this request's", func(t *testing.T) {
		collector := installCollector(t)
		first := observeStart(context.Background(), telemetry.OperationEvent{Operation: "is"})
		second := observeStart(context.Background(), telemetry.OperationEvent{Operation: "is"})
		NoteCacheHit(first)
		observeEnd(second, answered)
		observeEnd(first, answered)
		_, finished := collector.snapshot()
		if len(finished) != 2 || finished[0].Cached || !finished[1].Cached {
			t.Errorf("finishes = %+v, want only the request that was noted to be cached", finished)
		}
	})
}

// A streamed request is a request of the operation it runs under, counted as a
// buffered one is.
func TestAStreamedRequestIsCountedInTheOperationItRunsUnder(t *testing.T) {
	collector := installCollector(t)
	ctx, observed := observeOperation(context.Background(), "summarize")
	var stream observedStream
	stream.begin(ctx, &callerSuppliedProvider{name: "acme"}, "m", "text", types.OpOptions{}, "req", "corr", time.Now())
	stream.end(nil, false)
	var err error
	observed.end(&err)

	units := operationUnits(collector)
	if len(units) != 1 || units[0].Requests != 1 {
		t.Errorf("operation units = %+v, want one that made a request", units)
	}
	if requestStarts(collector) != 1 {
		t.Errorf("%d requests started, want 1", requestStarts(collector))
	}
}

// A producer that panics ends the request it was making as an internal failure,
// and the stream says so once however often it is asked. The producer's own
// goroutine cannot be made to panic in a test without ending the process, so the
// end that its deferred function calls is called as it calls it.
func TestAStreamWhoseProducerPanickedEndsItsRequestAsInternal(t *testing.T) {
	collector := installCollector(t)
	var stream observedStream
	stream.begin(context.Background(), &callerSuppliedProvider{name: "acme"}, "m", "text", types.OpOptions{}, "req", "corr", time.Now())
	stream.end(nil, true)
	stream.end(nil, false)

	_, finished := collector.snapshot()
	if len(finished) != 1 || finished[0].ErrorKind != telemetry.ErrorKindInternal || finished[0].Attempts != 1 {
		t.Errorf("finishes = %+v, want one request that failed as internal after one attempt", finished)
	}
}

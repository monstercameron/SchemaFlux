package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// gatedCall is one request that reached gatedProvider, held until the test lets
// it go.
type gatedCall struct {
	request llm.CompletionRequest
	release chan struct{}
	// ctx is the context the call runs under, so that a provider hook can wait
	// for the batch to cancel it.
	ctx context.Context
}

// gatedProvider answers widgetOp's requests, batched or single, after the test
// releases the call, and records how many calls were in flight at once. It
// orders events with channels and not with sleeps: a test waits for the calls it
// expects to arrive, looks at what is in flight, and then releases them.
//
// Every answer reports 150 tokens, so the spend of a run is a figure the test
// can sum, and names the widget by the number in its name, so a result in the
// wrong place is a wrong number.
type gatedProvider struct {
	mu        sync.Mutex
	inFlight  int
	peak      int
	calls     int
	cancelled int

	arrivals chan *gatedCall
	opened   chan struct{}
	openOnce sync.Once

	// hold, when false, lets every call straight through.
	hold bool
	// before runs after a call is released and before it answers. It may return
	// an error, panic, or end the goroutine.
	before func(call *gatedCall) error
	// answer writes the answer to a request's user prompt; nil answers
	// widgetOp's protocol.
	answer func(user string) string
}

func newGatedProvider() *gatedProvider {
	return &gatedProvider{arrivals: make(chan *gatedCall, 4096), opened: make(chan struct{}), hold: true}
}

var gatedNumber = regexp.MustCompile(`widget-(\d+)`)

// widgetNumbers are the numbers in the widget names a request names, in the
// order it names them.
func widgetNumbers(user string) []int {
	var out []int
	for _, match := range gatedNumber.FindAllStringSubmatch(user, -1) {
		n, _ := strconv.Atoi(match[1])
		out = append(out, n)
	}
	return out
}

func gatedAnswer(user string) string {
	if !strings.HasPrefix(user, "Process these items:\n") {
		numbers := widgetNumbers(user)
		if len(numbers) == 0 {
			return `{"length":-1}`
		}
		return fmt.Sprintf(`{"length":%d}`, numbers[0])
	}
	var tagged []struct {
		ID   string `json:"id"`
		Item widget `json:"item"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(user, "Process these items:\n")), &tagged); err != nil {
		return `{"items":[]}`
	}
	var pairs [][2]string
	for _, item := range tagged {
		numbers := widgetNumbers(item.Item.Name)
		pairs = append(pairs, [2]string{item.ID, fmt.Sprintf(`{"length":%d}`, numbers[0])})
	}
	return mergeBody(pairs...)
}

func (p *gatedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	call := &gatedCall{request: req, release: make(chan struct{}), ctx: ctx}
	p.mu.Lock()
	p.calls++
	p.inFlight++
	if p.inFlight > p.peak {
		p.peak = p.inFlight
	}
	hold := p.hold
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()

	p.arrivals <- call
	if hold {
		select {
		case <-call.release:
		case <-p.opened:
		case <-ctx.Done():
			p.mu.Lock()
			p.cancelled++
			p.mu.Unlock()
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
	if p.before != nil {
		if err := p.before(call); err != nil {
			return llm.CompletionResponse{}, err
		}
	}
	answer := gatedAnswer
	if p.answer != nil {
		answer = p.answer
	}
	return llm.CompletionResponse{
		Content:      answer(req.UserPrompt),
		Provider:     "local",
		Model:        "gated-model",
		FinishReason: "stop",
		Usage:        types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}, nil
}

func (p *gatedProvider) Name() string                               { return "local" }
func (p *gatedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *gatedProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

// open lets every call, present and future, go.
func (p *gatedProvider) open() { p.openOnce.Do(func() { close(p.opened) }) }

func (p *gatedProvider) snapshot() (inFlight, peak, calls, cancelled int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inFlight, p.peak, p.calls, p.cancelled
}

// awaitArrivals waits for n calls to reach the provider and returns them in
// the order they arrived.
func (p *gatedProvider) awaitArrivals(t *testing.T, n int) []*gatedCall {
	t.Helper()
	got := make([]*gatedCall, 0, n)
	timeout := time.After(10 * time.Second)
	for len(got) < n {
		select {
		case call := <-p.arrivals:
			got = append(got, call)
		case <-timeout:
			_, _, calls, _ := p.snapshot()
			t.Fatalf("only %d of the %d calls expected had reached the provider (%d calls in all)", len(got), n, calls)
		}
	}
	return got
}

// settle gives every goroutine that can run a chance to, so that a call a
// batch should not have started yet would have been started.
func settle() {
	for i := 0; i < 300; i++ {
		runtime.Gosched()
	}
}

// manyOutcome is what RunOpMany returned.
type manyOutcome struct {
	results []types.Result[widgetSummary]
	plan    types.Plan
	err     error
}

func runManyAsync(ctx context.Context, items []widget, perCall int) <-chan manyOutcome {
	done := make(chan manyOutcome, 1)
	go func() {
		results, plan, err := RunOpMany(ctx, widgetOp(), items, types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: perCall})
		done <- manyOutcome{results, plan, err}
	}()
	return done
}

func awaitMany(t *testing.T, done <-chan manyOutcome) manyOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(10 * time.Second):
		t.Fatal("RunOpMany had not returned after 10 seconds")
		return manyOutcome{}
	}
}

// requireInOrder fails unless result i holds item i's number.
func requireInOrder(t *testing.T, results []types.Result[widgetSummary], want int) {
	t.Helper()
	if len(results) != want {
		t.Fatalf("got %d results, want %d", len(results), want)
	}
	for i, result := range results {
		if result.Value.Length != i {
			t.Fatalf("result %d holds item %d's answer: the results are not in the order of the inputs", i, result.Value.Length)
		}
	}
}

func gatedContext(p *gatedProvider) context.Context {
	return WithProvider(context.Background(), p)
}

func schedulerContext(t *testing.T, p *gatedProvider, limits SchedulerLimits) (context.Context, *Scheduler) {
	t.Helper()
	scheduler := NewScheduler(limits)
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	return WithExecConfig(context.Background(), &ExecConfig{Provider: p, Scheduler: scheduler}), scheduler
}

// Independent chunks of a planned batch were sent one after another: five chunks
// of twenty took five calls' time, with one call in flight at any moment.
func TestRunOpManyOverlapsIndependentChunksUpToTheDefaultBound(t *testing.T) {
	cases := []struct {
		name     string
		items    int
		perCall  int
		wantPeak int
	}{
		{"fewer chunks than the bound", 15, 5, 3},
		{"as many chunks as the bound", 20, 5, DefaultConcurrency},
		{"more chunks than the bound", 40, 5, DefaultConcurrency},
		{"many chunks", 100, 5, DefaultConcurrency},
		{"one chunk", 5, 5, 1},
		{"two chunks", 10, 5, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
			provider := newGatedProvider()
			done := runManyAsync(gatedContext(provider), widgets(tc.items), tc.perCall)

			provider.awaitArrivals(t, tc.wantPeak)
			settle()
			if inFlight, _, _, _ := provider.snapshot(); inFlight != tc.wantPeak {
				t.Fatalf("%d calls in flight while every one is held, want %d", inFlight, tc.wantPeak)
			}
			provider.open()

			outcome := awaitMany(t, done)
			if outcome.err != nil {
				t.Fatalf("RunOpMany: %v", outcome.err)
			}
			requireInOrder(t, outcome.results, tc.items)
			_, peak, calls, _ := provider.snapshot()
			if peak != tc.wantPeak {
				t.Errorf("peak in flight = %d, want %d: the number of chunks or the bound, whichever is smaller", peak, tc.wantPeak)
			}
			if want := (tc.items + tc.perCall - 1) / tc.perCall; calls != want {
				t.Errorf("%d calls for %d chunks", calls, want)
			}
		})
	}
}

// With a scheduler on the context, the scheduler is the bound: a limit of one
// makes the batch serial again, and a larger limit lets more of it overlap than
// the default bound does.
func TestRunOpManyIsBoundedByTheClientsScheduler(t *testing.T) {
	cases := []struct {
		name     string
		limits   SchedulerLimits
		chunks   int
		wantPeak int
	}{
		{"one at a time is serial", SchedulerLimits{MaxConcurrent: 1}, 6, 1},
		{"two", SchedulerLimits{MaxConcurrent: 2}, 6, 2},
		{"three", SchedulerLimits{MaxConcurrent: 3}, 6, 3},
		{"above the default bound", SchedulerLimits{MaxConcurrent: 8}, 12, 8},
		{"above the number of chunks", SchedulerLimits{MaxConcurrent: 8}, 5, 5},
		{"a per-provider limit binds too", SchedulerLimits{MaxConcurrent: 8, PerProviderConcurrency: 2}, 6, 2},
		{"a per-tenant limit binds too", SchedulerLimits{MaxConcurrent: 8, PerTenantConcurrency: 3}, 6, 3},
		{"a scheduler that sets no limit gives the default bound", SchedulerLimits{}, 100, DefaultConcurrency},
		{"a scheduler that limits only its queue gives the default bound", SchedulerLimits{MaxQueued: 50}, 100, DefaultConcurrency},
		{"a limit above the ceiling on goroutines", SchedulerLimits{MaxConcurrent: 1000}, 100, eachAtOnce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
			provider := newGatedProvider()
			ctx, scheduler := schedulerContext(t, provider, tc.limits)
			items := tc.chunks * 5
			done := runManyAsync(ctx, widgets(items), 5)

			provider.awaitArrivals(t, tc.wantPeak)
			settle()
			if inFlight, _, _, _ := provider.snapshot(); inFlight != tc.wantPeak {
				t.Fatalf("%d calls in flight while every one is held, want %d", inFlight, tc.wantPeak)
			}
			if stats := scheduler.Stats(); stats.InFlight != tc.wantPeak {
				t.Errorf("the scheduler has %d in flight, want %d", stats.InFlight, tc.wantPeak)
			}
			provider.open()

			outcome := awaitMany(t, done)
			if outcome.err != nil {
				t.Fatalf("RunOpMany: %v", outcome.err)
			}
			requireInOrder(t, outcome.results, items)
			if _, peak, _, _ := provider.snapshot(); peak != tc.wantPeak {
				t.Errorf("peak in flight = %d, want %d", peak, tc.wantPeak)
			}
		})
	}
}

// Whichever order the chunks answer in, the results are in the order of the
// inputs, and every item is there once.
func TestRunOpManyReturnsResultsInInputOrderForEveryCompletionOrder(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	const chunks = 4
	permutations := permute([]int{0, 1, 2, 3})
	if len(permutations) != 24 {
		t.Fatalf("%d permutations of four", len(permutations))
	}
	for _, order := range permutations {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			provider := newGatedProvider()
			done := runManyAsync(gatedContext(provider), widgets(chunks*3), 3)

			arrived := provider.awaitArrivals(t, chunks)
			// The call that holds chunk k is the one whose first widget is 3k.
			byChunk := make(map[int]*gatedCall)
			for _, call := range arrived {
				byChunk[widgetNumbers(call.request.UserPrompt)[0]/3] = call
			}
			for _, chunk := range order {
				close(byChunk[chunk].release)
			}

			outcome := awaitMany(t, done)
			if outcome.err != nil {
				t.Fatalf("RunOpMany: %v", outcome.err)
			}
			requireInOrder(t, outcome.results, chunks*3)
		})
	}
}

// The same with oversized items among the chunks: two chunks and two single
// calls are in flight together, each result is in the place of its own item
// whichever of the four answers first, and no item is lost or answered twice.
func TestRunManyBatchedReturnsResultsInInputOrderWithOversizedItemsForEveryCompletionOrder(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	// Items 1 and 5 go alone. The chunks hold items 0, 2, 3 and 4, 6, 7.
	plan := types.Plan{
		Chunks:         []types.ChunkPlan{{ItemCount: 3}, {ItemCount: 3}},
		OversizedItems: []types.OversizedItem{{Index: 1}, {Index: 5}},
	}
	const calls = 4
	// The first widget a call names says which call it is: 0 and 4 open the
	// chunks, 1 and 5 are the single items.
	callKeys := []int{0, 4, 1, 5}
	for _, order := range permute([]int{0, 1, 2, 3}) {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			provider := newGatedProvider()
			type returned struct {
				results []types.Result[widgetSummary]
				err     error
			}
			done := make(chan returned, 1)
			go func() {
				results, _, err := runManyBatched(gatedContext(provider), widgetOp(), widgets(8), types.OpOptions{}, plan)
				done <- returned{results, err}
			}()

			byKey := make(map[int]*gatedCall)
			for _, call := range provider.awaitArrivals(t, calls) {
				byKey[widgetNumbers(call.request.UserPrompt)[0]] = call
			}
			for _, k := range order {
				close(byKey[callKeys[k]].release)
			}
			select {
			case got := <-done:
				if got.err != nil {
					t.Fatalf("runManyBatched: %v", got.err)
				}
				requireInOrder(t, got.results, 8)
			case <-time.After(10 * time.Second):
				t.Fatal("runManyBatched had not returned")
			}
		})
	}
}

func permute(items []int) [][]int {
	if len(items) <= 1 {
		return [][]int{append([]int(nil), items...)}
	}
	var out [][]int
	for i := range items {
		rest := append(append([]int(nil), items[:i]...), items[i+1:]...)
		for _, tail := range permute(rest) {
			out = append(out, append([]int{items[i]}, tail...))
		}
	}
	return out
}

// A chunk that fails fails the call, as it did when the chunks ran one after
// another. The calls still in flight are cancelled and nothing new starts.
func TestRunOpManyStopsAtTheFirstFailureAndCancelsTheCallsInFlight(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	provider := newGatedProvider()
	provider.before = func(call *gatedCall) error {
		if widgetNumbers(call.request.UserPrompt)[0] == 5 {
			return types.Newf(types.KindProviderUnavailable, "gated", "the provider is down")
		}
		return nil
	}
	// Eight chunks, two in flight at a time.
	ctx, _ := schedulerContext(t, provider, SchedulerLimits{MaxConcurrent: 2})
	done := runManyAsync(ctx, widgets(40), 5)

	arrived := provider.awaitArrivals(t, 2)
	for _, call := range arrived {
		if widgetNumbers(call.request.UserPrompt)[0] == 5 {
			close(call.release)
		}
	}

	outcome := awaitMany(t, done)
	if outcome.err == nil {
		t.Fatal("RunOpMany reported success although a chunk failed")
	}
	if outcome.results != nil {
		t.Errorf("a failed call returned %d results", len(outcome.results))
	}
	if types.KindOf(outcome.err) != types.KindProviderUnavailable || !strings.Contains(causeText(outcome.err), "the provider is down") {
		t.Errorf("the error is not the failing chunk's own, in its kind and as its cause: %v", outcome.err)
	}
	settle()
	_, _, calls, cancelled := provider.snapshot()
	if calls != 2 {
		t.Errorf("%d calls were made, want the 2 that were in flight: nothing new starts after a failure", calls)
	}
	if cancelled != 1 {
		t.Errorf("%d calls saw their context cancelled, want the one that was still in flight", cancelled)
	}
}

// failInThisOrder is a provider hook for two calls in flight at once, told apart
// by the number of the first widget each names. The call for early fails with
// earlyErr as soon as the other has reached the provider. The call for late waits
// until the batch cancels it, which only the early failure can cause, and then
// fails with lateErr, an error of its own and not the cancellation. The failure
// that stopped the batch is early's, whichever of the two comes first by position.
func failInThisOrder(early, late int, earlyErr, lateErr error) func(*gatedCall) error {
	lateArrived := make(chan struct{})
	return func(call *gatedCall) error {
		switch widgetNumbers(call.request.UserPrompt)[0] {
		case late:
			close(lateArrived)
			<-call.ctx.Done()
			return lateErr
		case early:
			<-lateArrived
			return earlyErr
		}
		return nil
	}
}

// Every multi-call function that stops on a failure returns the failure that
// stopped it, the first in time, and not a failure that merely sits at a lower
// position or the cancellation that failure caused in the others. Here two calls
// fail with different errors of their own, the second only after the first has
// cancelled the batch.
func TestMultiCallFunctionsReturnTheFailureThatStoppedTheRunNotTheLowestPosition(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	shapes := []struct {
		name string
		// calls are the numbers of the first widget in the two calls that are in
		// flight, in position order.
		calls [2]int
		run   func(ctx context.Context) error
	}{
		{"batched chunks", [2]int{0, 5}, func(ctx context.Context) error {
			_, _, err := RunOpMany(ctx, widgetOp(), widgets(40), types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: 5})
			return err
		}},
		{"single items", [2]int{0, 1}, func(ctx context.Context) error {
			_, _, err := RunOpMany(ctx, atomicOnlyOp(), partialItems(8), types.PlanRequest{})
			return err
		}},
	}
	earlyErr := types.Newf(types.KindProviderUnavailable, "gated", "the first failure")
	lateErr := types.Newf(types.KindPolicyViolation, "gated", "the second failure")
	for _, shape := range shapes {
		for _, laterPositionFirst := range []bool{true, false} {
			name := shape.name + "/the earlier position fails first"
			early, late := shape.calls[0], shape.calls[1]
			if laterPositionFirst {
				name = shape.name + "/the later position fails first"
				early, late = late, early
			}
			t.Run(name, func(t *testing.T) {
				provider := newGatedProvider()
				provider.hold = false
				provider.before = failInThisOrder(early, late, earlyErr, lateErr)
				ctx, _ := schedulerContext(t, provider, SchedulerLimits{MaxConcurrent: 2})

				done := make(chan error, 1)
				go func() { done <- shape.run(ctx) }()
				var err error
				select {
				case err = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("the call had not returned after 10 seconds")
				}
				if err == nil {
					t.Fatal("the call reported success although two calls failed")
				}
				if kind := types.KindOf(err); kind != types.KindProviderUnavailable {
					t.Errorf("error kind = %v, want the first failure's (%v): %v", kind, types.KindProviderUnavailable, err)
				}
			})
		}
	}
}

// The single-item shape wraps the failure in the chunk it happened in; that chunk
// is the one that failed first, and not the first by position.
func TestRunOpManyOverSingleItemsNamesTheChunkThatFailedFirst(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	provider := newGatedProvider()
	provider.hold = false
	provider.before = failInThisOrder(1, 0,
		types.Newf(types.KindProviderUnavailable, "gated", "the first failure"),
		types.Newf(types.KindPolicyViolation, "gated", "the second failure"))
	ctx, _ := schedulerContext(t, provider, SchedulerLimits{MaxConcurrent: 2})

	_, _, err := RunOpMany(ctx, atomicOnlyOp(), partialItems(8), types.PlanRequest{})
	var failed ChunkError
	if !errors.As(err, &failed) {
		t.Fatalf("error = %v, want a ChunkError", err)
	}
	if failed.Index != 1 || failed.Offset != 1 {
		t.Errorf("the error names chunk %d at offset %d, want chunk 1 at offset 1, the one that failed first", failed.Index, failed.Offset)
	}
}

// A client whose budget refuses once something has been spent still pays for the
// calls that were in flight when the first one finished: a call is checked as it
// starts. How many that is depends on the bound, and the bound is the same
// whether there is no scheduler or one that sets no limit; it used to be 64 for
// the second.
func TestAClientBudgetIsOvershotByNoMoreThanTheBoundOnTheCallsInFlight(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	entryPoints := []struct {
		name string
		run  func(ctx context.Context) (refused bool)
	}{
		{"batched chunks", func(ctx context.Context) bool {
			_, _, err := RunOpMany(ctx, widgetOp(), widgets(400), types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: 4})
			return err != nil
		}},
		{"single items", func(ctx context.Context) bool {
			_, _, err := RunOpMany(ctx, atomicOnlyOp(), partialItems(100), types.PlanRequest{})
			return err != nil
		}},
	}
	schedulers := []struct {
		name   string
		limits *SchedulerLimits
		bound  int
	}{
		{"no scheduler", nil, DefaultConcurrency},
		{"a scheduler that sets no limit", &SchedulerLimits{}, DefaultConcurrency},
		{"a scheduler of three", &SchedulerLimits{MaxConcurrent: 3}, 3},
		{"a scheduler of seven", &SchedulerLimits{MaxConcurrent: 7}, 7},
	}
	for _, entry := range entryPoints {
		for _, sch := range schedulers {
			t.Run(entry.name+"/"+sch.name, func(t *testing.T) {
				provider := newGatedProvider()
				config := &ExecConfig{Provider: provider, Budget: &budgetOfOneCall{}}
				if sch.limits != nil {
					config.Scheduler = NewScheduler(*sch.limits)
					t.Cleanup(func() { _ = config.Scheduler.Close(context.Background()) })
				}
				ctx := WithExecConfig(context.Background(), config)

				refused := make(chan bool, 1)
				go func() { refused <- entry.run(ctx) }()

				provider.awaitArrivals(t, sch.bound)
				settle()
				if inFlight, _, _, _ := provider.snapshot(); inFlight != sch.bound {
					t.Fatalf("%d calls in flight while every one is held, want %d", inFlight, sch.bound)
				}
				provider.open()
				select {
				case got := <-refused:
					if !got {
						t.Error("nothing reported the refusal of the budget")
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the call had not returned after 10 seconds")
				}
				if _, _, calls, _ := provider.snapshot(); calls != sch.bound {
					t.Errorf("%d calls reached the provider, want the %d that were in flight when the first one was paid for", calls, sch.bound)
				}
			})
		}
	}
}

// budgetOfOneCall refuses every call that starts after the first cost has been
// recorded.
type budgetOfOneCall struct {
	mu       sync.Mutex
	recorded int
}

func (b *budgetOfOneCall) Check() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recorded >= 1 {
		return types.Newf(types.KindBudgetExceeded, "budget", "the client's budget is spent")
	}
	return nil
}

func (b *budgetOfOneCall) Record(float64, bool) {
	b.mu.Lock()
	b.recorded++
	b.mu.Unlock()
}

// A caller who cancels stops the batch: no new chunk starts, and the error is
// the cancellation.
func TestRunOpManyStopsWhenTheCallersContextIsCancelled(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	provider := newGatedProvider()
	ctx, cancel := context.WithCancel(gatedContext(provider))
	defer cancel()
	done := runManyAsync(ctx, widgets(40), 5)

	provider.awaitArrivals(t, DefaultConcurrency)
	cancel()

	outcome := awaitMany(t, done)
	if outcome.err == nil || !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("error = %v, want the context's cancellation", outcome.err)
	}
	settle()
	if _, _, calls, _ := provider.snapshot(); calls != DefaultConcurrency {
		t.Errorf("%d calls were made, want the %d in flight when it was cancelled", calls, DefaultConcurrency)
	}
}

func TestRunOpManyWithAContextAlreadyCancelledSendsNothing(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	provider := newGatedProvider()
	provider.open()
	ctx, cancel := context.WithCancel(gatedContext(provider))
	cancel()

	results, _, err := RunOpMany(ctx, widgetOp(), widgets(30), types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: 5})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation", err)
	}
	if types.KindOf(err) != types.KindCanceled {
		t.Errorf("kind = %v, want KindCanceled", types.KindOf(err))
	}
	if results != nil {
		t.Errorf("returned %d results beside an error", len(results))
	}
	if _, _, calls, _ := provider.snapshot(); calls != 0 {
		t.Errorf("%d calls were made on a cancelled context", calls)
	}
}

// What overlapping changes is when the calls happen and nothing about what comes
// back: the same batch run one chunk at a time and run overlapped returns the
// same values and the same spend, item by item.
func TestRunOpManyReturnsWhatItReturnedWhenTheChunksWereSerial(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	run := func(ctx context.Context) []types.Result[widgetSummary] {
		results, _, err := RunOpMany(ctx, widgetOp(), widgets(60), types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: 7})
		if err != nil {
			t.Fatalf("RunOpMany: %v", err)
		}
		return results
	}
	serialProvider := newGatedProvider()
	serialProvider.hold = false
	serialCtx, _ := schedulerContext(t, serialProvider, SchedulerLimits{MaxConcurrent: 1})
	serial := run(serialCtx)

	overlappedProvider := newGatedProvider()
	overlappedProvider.hold = false
	overlapped := run(gatedContext(overlappedProvider))

	if len(serial) != len(overlapped) {
		t.Fatalf("%d results serial, %d overlapped", len(serial), len(overlapped))
	}
	for i := range serial {
		a, b := serial[i], overlapped[i]
		if a.Value != b.Value || a.Meta.Usage != b.Meta.Usage || a.Meta.Attempts != b.Meta.Attempts ||
			a.Meta.Operation != b.Meta.Operation || a.Meta.Strategy != b.Meta.Strategy ||
			a.Meta.DeliveredContract != b.Meta.DeliveredContract || a.Meta.Cost.Priced != b.Meta.Cost.Priced {
			t.Fatalf("item %d differs: serial %+v %+v, overlapped %+v %+v", i, a.Value, a.Meta.Usage, b.Value, b.Meta.Usage)
		}
	}
}

// A chunk whose work panics, or ends its goroutine, is an error for the call:
// the process is not taken down, the other chunks are not left hanging, and no
// goroutine is left behind.
func TestRunOpManyReportsAChunkThatPanicsOrEndsItsGoroutine(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	cases := []struct {
		name string
		fail func()
	}{
		{"a panic", func() { panic("the provider blew up on a customer's name") }},
		{"a goroutine that exits", runtime.Goexit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := chaosSettledGoroutines()
			provider := newGatedProvider()
			provider.before = func(call *gatedCall) error {
				if widgetNumbers(call.request.UserPrompt)[0] == 10 {
					tc.fail()
				}
				return nil
			}
			done := runManyAsync(gatedContext(provider), widgets(40), 5)
			arrived := provider.awaitArrivals(t, DefaultConcurrency)
			for _, call := range arrived {
				if widgetNumbers(call.request.UserPrompt)[0] == 10 {
					close(call.release)
				}
			}

			outcome := awaitMany(t, done)
			if outcome.err == nil {
				t.Fatal("a chunk that did not return was reported as a success")
			}
			if types.KindOf(outcome.err) != types.KindInvariantViolation {
				t.Errorf("kind = %v, want KindInvariantViolation (%v)", types.KindOf(outcome.err), outcome.err)
			}
			if strings.Contains(outcome.err.Error(), "customer") {
				t.Errorf("the panic's value is in the error: %v", outcome.err)
			}
			if after := chaosSettledGoroutines(); after > before {
				t.Errorf("%d goroutines before, %d after", before, after)
			}
		})
	}
}

// Oversized items are one call each, and run alongside the chunks and one
// another under the same bound.
func TestRunManyBatchedOverlapsOversizedItemsWithTheChunks(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	provider := newGatedProvider()
	items := widgets(14)
	// Items 3, 7, 8 and 12 are too large for a chunk and go one by one: ten
	// items in two chunks of five.
	plan := types.Plan{
		Chunks: []types.ChunkPlan{{ItemCount: 5}, {ItemCount: 5}},
		OversizedItems: []types.OversizedItem{
			{Index: 3}, {Index: 7}, {Index: 8}, {Index: 12},
		},
	}
	type returned struct {
		results []types.Result[widgetSummary]
		err     error
	}
	done := make(chan returned, 1)
	go func() {
		results, _, err := runManyBatched(gatedContext(provider), widgetOp(), items, types.OpOptions{}, plan)
		done <- returned{results, err}
	}()

	provider.awaitArrivals(t, DefaultConcurrency)
	settle()
	if inFlight, _, _, _ := provider.snapshot(); inFlight != DefaultConcurrency {
		t.Fatalf("%d calls in flight, want %d: six calls to make, under the default bound", inFlight, DefaultConcurrency)
	}
	provider.open()

	var got returned
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runManyBatched had not returned")
	}
	if got.err != nil {
		t.Fatalf("runManyBatched: %v", got.err)
	}
	requireInOrder(t, got.results, len(items))
	if _, peak, calls, _ := provider.snapshot(); peak != DefaultConcurrency || calls != 6 {
		t.Errorf("peak %d, calls %d; want %d and 6", peak, calls, DefaultConcurrency)
	}
}

// A plan whose chunks do not account for the items is refused before a call is
// made. The chunks used to be sent one by one and the mismatch found at the
// end, after the calls had been paid for.
func TestRunManyBatchedRefusesAPlanThatDoesNotAddUpBeforeSendingAnything(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	cases := []struct {
		name  string
		plan  types.Plan
		items int
	}{
		{"chunks cover too few items", types.Plan{Chunks: []types.ChunkPlan{{ItemCount: 5}, {ItemCount: 5}}}, 12},
		{"chunks cover too many items", types.Plan{Chunks: []types.ChunkPlan{{ItemCount: 5}, {ItemCount: 9}}}, 12},
		{"oversized items are not counted in the chunks", types.Plan{
			Chunks:         []types.ChunkPlan{{ItemCount: 5}, {ItemCount: 5}},
			OversizedItems: []types.OversizedItem{{Index: 1}},
		}, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newGatedProvider()
			provider.open()
			results, _, err := runManyBatched(gatedContext(provider), widgetOp(), widgets(tc.items), types.OpOptions{}, tc.plan)
			if err == nil {
				t.Fatalf("a plan that does not add up ran: %d results", len(results))
			}
			if kind := types.KindOf(err); kind != types.KindInvariantViolation {
				t.Errorf("kind = %v, want KindInvariantViolation (%v)", kind, err)
			}
			if results != nil {
				t.Errorf("%d results beside the refusal", len(results))
			}
			if _, _, calls, _ := provider.snapshot(); calls != 0 {
				t.Errorf("%d calls were made before the plan was found wrong", calls)
			}
		})
	}
}

// A merge that answers some other number of outputs than it was given items has
// lost track of which output is whose. The chunk is refused with a classified
// error and nothing of the outputs beside it.
func TestRunManyBatchedRefusesAChunkMergedIntoTheWrongNumberOfOutputs(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	op := widgetOp()
	op.Batch.Class = types.BatchAggregate
	merge := op.Batch.Merge
	op.Batch.Merge = func(body string, items []widget) ([]widgetSummary, error) {
		outputs, err := merge(body, items)
		if err != nil || len(outputs) == 0 {
			return outputs, err
		}
		return outputs[:len(outputs)-1], nil
	}
	provider := newGatedProvider()
	provider.open()
	plan := types.Plan{Chunks: []types.ChunkPlan{{ItemCount: 3}}}

	results, _, err := runManyBatched(gatedContext(provider), op, widgets(3), types.OpOptions{}, plan)
	if err == nil {
		t.Fatalf("a chunk merged into the wrong number of outputs ran: %d results", len(results))
	}
	if kind := types.KindOf(err); kind != types.KindInvariantViolation {
		t.Errorf("kind = %v, want KindInvariantViolation (%v)", kind, err)
	}
	if results != nil {
		t.Errorf("%d results beside the refusal", len(results))
	}
}

// What the batch reports is what it spent: every item is there once, and the
// chunk's usage is on its first item and on no other, whatever order the chunks
// finished in.
func TestRunOpManyReportsTheSameTotalsWhenChunksOverlap(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	provider := newGatedProvider()
	provider.open()
	const chunks = 8
	outcome := awaitMany(t, runManyAsync(gatedContext(provider), widgets(chunks*5), 5))
	if outcome.err != nil {
		t.Fatalf("RunOpMany: %v", outcome.err)
	}
	requireInOrder(t, outcome.results, chunks*5)
	tokens, attempts := 0, 0
	for i, result := range outcome.results {
		tokens += result.Meta.Usage.TotalTokens
		attempts += result.Meta.Attempts
		if i%5 != 0 && (result.Meta.Usage != (types.TokenUsage{}) || result.Meta.Attempts != 0) {
			t.Errorf("item %d repeats its chunk's spend: %+v", i, result.Meta.Usage)
		}
	}
	if tokens != chunks*150 {
		t.Errorf("summed tokens = %d, want %d: one call per chunk", tokens, chunks*150)
	}
	if attempts != chunks {
		t.Errorf("summed attempts = %d, want %d", attempts, chunks)
	}
}

// Nothing a batch starts outlives it, whether it succeeds, fails, or is
// cancelled.
func TestRunOpManyLeavesNoGoroutineBehindOver200Runs(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	before := chaosSettledGoroutines()
	for run := 0; run < 200; run++ {
		provider := newGatedProvider()
		provider.open()
		switch run % 3 {
		case 1:
			provider.before = func(call *gatedCall) error {
				if widgetNumbers(call.request.UserPrompt)[0] == 10 {
					return types.Newf(types.KindProviderUnavailable, "gated", "down")
				}
				return nil
			}
		case 2:
			provider.before = func(call *gatedCall) error {
				if widgetNumbers(call.request.UserPrompt)[0] == 10 {
					panic("boom")
				}
				return nil
			}
		}
		results, _, err := RunOpMany(gatedContext(provider), widgetOp(), widgets(30), types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: 5})
		if (run%3 == 0) != (err == nil) {
			t.Fatalf("run %d: error = %v", run, err)
		}
		if err == nil && len(results) != 30 {
			t.Fatalf("run %d: %d results", run, len(results))
		}
	}
	if after := chaosSettledGoroutines(); after > before {
		t.Errorf("%d goroutines before 200 runs, %d after", before, after)
	}
}

// Fifty batches at once, each with its own overlap: all come back correct.
func TestRunOpManyFiftyBatchesAtOnce(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	provider := newGatedProvider()
	provider.open()
	ctx := gatedContext(provider)
	var wg sync.WaitGroup
	failures := make(chan string, 50)
	for batch := 0; batch < 50; batch++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, _, err := RunOpMany(ctx, widgetOp(), widgets(30), types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: 5})
			if err != nil {
				failures <- err.Error()
				return
			}
			for i, result := range results {
				if result.Value.Length != i {
					failures <- fmt.Sprintf("result %d holds %d", i, result.Value.Length)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	if _, peak, calls, _ := provider.snapshot(); calls != 50*6 || peak > 50*DefaultConcurrency {
		t.Errorf("calls %d (want %d), peak %d (at most %d)", calls, 50*6, peak, 50*DefaultConcurrency)
	}
}

// RunOpMany over single items takes its bound from the scheduler when there is
// one, as the chunked shape does.
func TestRunOpManyAtomicIsBoundedByTheClientsScheduler(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gated-model")
	cases := []struct {
		name     string
		ctx      func(*testing.T, *gatedProvider) context.Context
		wantPeak int
	}{
		{"no scheduler is the default bound", func(_ *testing.T, p *gatedProvider) context.Context { return gatedContext(p) }, DefaultConcurrency},
		{"a scheduler of one is serial", func(t *testing.T, p *gatedProvider) context.Context {
			ctx, _ := schedulerContext(t, p, SchedulerLimits{MaxConcurrent: 1})
			return ctx
		}, 1},
		{"a scheduler of ten admits ten", func(t *testing.T, p *gatedProvider) context.Context {
			ctx, _ := schedulerContext(t, p, SchedulerLimits{MaxConcurrent: 10})
			return ctx
		}, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newGatedProvider()
			items := make([]string, 30)
			for i := range items {
				items[i] = fmt.Sprintf("widget-%03d", i)
			}
			op := atomicOnlyOp()
			type returned struct {
				results []types.Result[string]
				err     error
			}
			done := make(chan returned, 1)
			ctx := tc.ctx(t, provider)
			go func() {
				results, _, err := RunOpMany(ctx, op, items, types.PlanRequest{})
				done <- returned{results, err}
			}()
			provider.awaitArrivals(t, tc.wantPeak)
			settle()
			if inFlight, _, _, _ := provider.snapshot(); inFlight != tc.wantPeak {
				t.Fatalf("%d in flight, want %d", inFlight, tc.wantPeak)
			}
			provider.open()
			got := <-done
			if got.err != nil {
				t.Fatalf("RunOpMany: %v", got.err)
			}
			for i, result := range got.results {
				if want := fmt.Sprintf(`{"length":%d}`, i); result.Value != want {
					t.Fatalf("result %d = %q, want %q", i, result.Value, want)
				}
			}
			if _, peak, _, _ := provider.snapshot(); peak != tc.wantPeak {
				t.Errorf("peak = %d, want %d", peak, tc.wantPeak)
			}
		})
	}
}

// --- runBounded and the bound taken from the scheduler.

func TestRunBoundedRunsEveryStepOnceWithinTheWorkers(t *testing.T) {
	for _, n := range []int{0, 1, 5, 100} {
		for _, workers := range []int{-3, 0, 1, 3, 200} {
			t.Run(fmt.Sprintf("%d steps, %d workers", n, workers), func(t *testing.T) {
				want := workers
				if want < 1 {
					want = 1
				}
				if want > n {
					want = n
				}
				var (
					mu       sync.Mutex
					seen     = make([]int, n)
					inFlight int
					peak     int
					arrived  = make(chan struct{}, n+1)
					release  = make(chan struct{})
					done     = make(chan boundedRun, 1)
				)
				go func() {
					// The first `want` steps wait for each other, which shows the
					// pool reaches its bound; the rest go straight through.
					done <- runBounded(context.Background(), "test", n, boundedOptions{workers: workers}, func(_ context.Context, i int) error {
						mu.Lock()
						seen[i]++
						inFlight++
						if inFlight > peak {
							peak = inFlight
						}
						mu.Unlock()
						if i < want {
							arrived <- struct{}{}
							<-release
						}
						mu.Lock()
						inFlight--
						mu.Unlock()
						return nil
					})
				}()
				for k := 0; k < want; k++ {
					select {
					case <-arrived:
					case <-time.After(10 * time.Second):
						t.Fatalf("only %d of %d steps arrived", k, want)
					}
				}
				settle()
				mu.Lock()
				held := inFlight
				mu.Unlock()
				if held != want {
					t.Errorf("%d steps in flight while the first %d are held", held, want)
				}
				close(release)
				ran := <-done

				mu.Lock()
				defer mu.Unlock()
				if peak != want {
					t.Errorf("peak = %d, want %d", peak, want)
				}
				for i, count := range seen {
					if count != 1 {
						t.Errorf("step %d ran %d times", i, count)
					}
					if !ran.started[i] || ran.errs[i] != nil {
						t.Errorf("step %d: started %v, error %v", i, ran.started[i], ran.errs[i])
					}
				}
				if ran.first != nil {
					t.Errorf("first = %v with no failure", ran.first)
				}
			})
		}
	}
}

func TestRunBoundedWithOneWorkerRunsTheStepsInOrder(t *testing.T) {
	var order []int
	runBounded(context.Background(), "test", 20, boundedOptions{workers: 1}, func(_ context.Context, i int) error {
		order = append(order, i)
		return nil
	})
	for i, got := range order {
		if got != i {
			t.Fatalf("order = %v", order)
		}
	}
	if len(order) != 20 {
		t.Fatalf("%d steps ran", len(order))
	}
}

// The first failure stops the run: the steps in flight are cancelled, no new
// step starts, and the error returned is the failure and not the cancellation it
// caused in the others.
func TestRunBoundedStopOnFailureCancelsTheRestAndReportsTheCause(t *testing.T) {
	cause := errors.New("step 1 failed")
	started := make(chan int, 10)
	sawCancel := make(chan int, 10)
	ran := runBounded(context.Background(), "test", 10, boundedOptions{workers: 3, stopOnFailure: true}, func(ctx context.Context, i int) error {
		started <- i
		if i == 1 {
			// Fail once the other two are in flight.
			for len(started) < 3 {
				runtime.Gosched()
			}
			return cause
		}
		<-ctx.Done()
		sawCancel <- i
		return ctx.Err()
	})
	if !errors.Is(ran.first, cause) {
		t.Errorf("first = %v, want the failure that caused the stop", ran.first)
	}
	if len(started) != 3 || len(sawCancel) != 2 {
		t.Errorf("%d steps started and %d saw the cancellation; want 3 and 2", len(started), len(sawCancel))
	}
	for i := 3; i < 10; i++ {
		if ran.started[i] {
			t.Errorf("step %d started after the failure", i)
		}
	}
}

// Two steps fail with errors of their own, the second only after the first has
// cancelled the run. The error the run reports is the first in time, whichever of
// the two has the lower index, and each step's own error is kept beside it.
func TestRunBoundedReportsTheFirstFailureInTimeAndNotTheLowestIndex(t *testing.T) {
	early := errors.New("failed first")
	late := errors.New("failed second")
	for _, earlyIndex := range []int{0, 1} {
		t.Run(fmt.Sprintf("step %d fails first", earlyIndex), func(t *testing.T) {
			lateIndex := 1 - earlyIndex
			lateArrived := make(chan struct{})
			ran := runBounded(context.Background(), "test", 6, boundedOptions{workers: 2, stopOnFailure: true}, func(ctx context.Context, i int) error {
				switch i {
				case lateIndex:
					close(lateArrived)
					<-ctx.Done()
					return late
				case earlyIndex:
					<-lateArrived
					return early
				}
				return nil
			})
			if !errors.Is(ran.first, early) {
				t.Errorf("first = %v, want %v: the failure that stopped the run", ran.first, early)
			}
			if ran.firstStep != earlyIndex {
				t.Errorf("firstStep = %d, want %d", ran.firstStep, earlyIndex)
			}
			if !errors.Is(ran.errs[earlyIndex], early) || !errors.Is(ran.errs[lateIndex], late) {
				t.Errorf("errs = %v, want each step's own error", ran.errs[:2])
			}
		})
	}
}

func TestRunBoundedNamesNoFirstStepWhenNothingFailed(t *testing.T) {
	ran := runBounded(context.Background(), "test", 5, boundedOptions{workers: 2, stopOnFailure: true}, func(context.Context, int) error { return nil })
	if ran.first != nil || ran.firstStep != -1 {
		t.Errorf("first = %v, firstStep = %d; want no failure and -1", ran.first, ran.firstStep)
	}
	if empty := runBounded(context.Background(), "test", 0, boundedOptions{workers: 2}, nil); empty.firstStep != -1 {
		t.Errorf("firstStep of a run with no steps = %d, want -1", empty.firstStep)
	}
}

func TestRunBoundedOnACancelledContextStartsNothingUnlessToldTo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran int
	var mu sync.Mutex
	count := func(c context.Context, _ int) error {
		mu.Lock()
		ran++
		mu.Unlock()
		if c.Err() == nil {
			t.Error("a step saw a live context")
		}
		return nil
	}

	stopped := runBounded(ctx, "test", 6, boundedOptions{workers: 2}, count)
	if ran != 0 || stopped.started[0] {
		t.Errorf("%d steps ran on a cancelled context", ran)
	}

	forced := runBounded(ctx, "test", 6, boundedOptions{workers: 2, runAfterCancel: true}, count)
	if ran != 6 {
		t.Errorf("%d steps ran, want all 6 offered the cancelled context", ran)
	}
	for i, started := range forced.started {
		if !started {
			t.Errorf("step %d not started", i)
		}
	}
}

func TestRunBoundedWithoutStopOnFailureRunsEveryStep(t *testing.T) {
	failure := errors.New("failed")
	ran := runBounded(context.Background(), "test", 12, boundedOptions{workers: 3}, func(_ context.Context, i int) error {
		if i%3 == 0 {
			return failure
		}
		return nil
	})
	for i := range ran.errs {
		if !ran.started[i] || (ran.errs[i] != nil) != (i%3 == 0) {
			t.Errorf("step %d: started %v, error %v", i, ran.started[i], ran.errs[i])
		}
	}
	if ran.first != nil {
		t.Errorf("first = %v, but nothing was asked to stop", ran.first)
	}
}

func TestRunBoundedReportsAStepThatPanicsOrEndsItsGoroutineAndKeepsGoing(t *testing.T) {
	for _, fail := range []struct {
		name string
		do   func()
	}{{"panic", func() { panic("secret") }}, {"goexit", runtime.Goexit}} {
		for _, workers := range []int{1, 2, 5} {
			t.Run(fmt.Sprintf("%s, %d workers", fail.name, workers), func(t *testing.T) {
				var mu sync.Mutex
				ranAfter := 0
				ran := runBounded(context.Background(), "test", 10, boundedOptions{workers: workers}, func(_ context.Context, i int) error {
					if i == 2 {
						fail.do()
					}
					if i > 2 {
						mu.Lock()
						ranAfter++
						mu.Unlock()
					}
					return nil
				})
				if types.KindOf(ran.errs[2]) != types.KindInvariantViolation {
					t.Errorf("step 2: %v, want an invariant violation", ran.errs[2])
				}
				if ranAfter != 7 {
					t.Errorf("%d of the 7 steps after it ran: the pool lost a worker", ranAfter)
				}
				for i, err := range ran.errs {
					if i != 2 && err != nil {
						t.Errorf("step %d: %v", i, err)
					}
				}
			})
		}
	}
}

func TestRunBoundedAPanicWithStopOnFailureStopsTheRun(t *testing.T) {
	ran := runBounded(context.Background(), "test", 10, boundedOptions{workers: 1, stopOnFailure: true}, func(_ context.Context, i int) error {
		if i == 2 {
			panic("boom")
		}
		return nil
	})
	if types.KindOf(ran.first) != types.KindInvariantViolation {
		t.Errorf("first = %v, want the panic as an invariant violation", ran.first)
	}
	for i := 3; i < 10; i++ {
		if ran.started[i] {
			t.Errorf("step %d started after the panic", i)
		}
	}
}

func TestRunBoundedTakesANilContextAsBackground(t *testing.T) {
	var count int
	//lint:ignore SA1012 the nil context is the case under test
	runBounded(nil, "test", 4, boundedOptions{workers: 1}, func(_ context.Context, _ int) error {
		count++
		return nil
	})
	if count != 4 {
		t.Errorf("%d steps ran on a nil context", count)
	}
}

func TestSchedulerBoundsAreTheSmallestLimitSet(t *testing.T) {
	scheduled := func(limits SchedulerLimits) context.Context {
		scheduler := NewScheduler(limits)
		t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
		return WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler})
	}
	cases := []struct {
		name          string
		ctx           context.Context
		wantLimit     int
		wantScheduled bool
		wantMany      int
		wantCapOf20   int
	}{
		{"no scheduler", context.Background(), 0, false, DefaultConcurrency, 20},
		{"a nil context", nil, 0, false, DefaultConcurrency, 20},
		{"no limit set", scheduled(SchedulerLimits{}), 0, true, DefaultConcurrency, 20},
		{"global only", scheduled(SchedulerLimits{MaxConcurrent: 6}), 6, true, 6, 6},
		{"per provider only", scheduled(SchedulerLimits{PerProviderConcurrency: 5}), 5, true, 5, 5},
		{"per tenant only", scheduled(SchedulerLimits{PerTenantConcurrency: 4}), 4, true, 4, 4},
		{"the smallest of three", scheduled(SchedulerLimits{MaxConcurrent: 9, PerProviderConcurrency: 3, PerTenantConcurrency: 7}), 3, true, 3, 3},
		{"above the ceiling on goroutines", scheduled(SchedulerLimits{MaxConcurrent: 5000}), 5000, true, eachAtOnce, 20},
		{"a limit above the caller's bound", scheduled(SchedulerLimits{MaxConcurrent: 50}), 50, true, 50, 20},
		{"queue and token limits are not concurrency", scheduled(SchedulerLimits{MaxQueued: 2, MaxInFlightTokens: 10}), 0, true, DefaultConcurrency, 20},
		{"a limit of the default bound", scheduled(SchedulerLimits{MaxConcurrent: DefaultConcurrency}), DefaultConcurrency, true, DefaultConcurrency, DefaultConcurrency},
		{"a limit of one", scheduled(SchedulerLimits{MaxConcurrent: 1}), 1, true, 1, 1},
		{"the ceiling itself", scheduled(SchedulerLimits{MaxConcurrent: eachAtOnce}), eachAtOnce, true, eachAtOnce, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, isScheduled := schedulerAdmitsAtOnce(tc.ctx)
			if limit != tc.wantLimit || isScheduled != tc.wantScheduled {
				t.Errorf("schedulerAdmitsAtOnce = %d, %v; want %d, %v", limit, isScheduled, tc.wantLimit, tc.wantScheduled)
			}
			if got := manyConcurrency(tc.ctx); got != tc.wantMany {
				t.Errorf("manyConcurrency = %d, want %d", got, tc.wantMany)
			}
			if got := capToScheduler(tc.ctx, 20); got != tc.wantCapOf20 {
				t.Errorf("capToScheduler(20) = %d, want %d", got, tc.wantCapOf20)
			}
		})
	}
}

func TestStoppedByIsTheClassifiedCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := stoppedBy(ctx)
	if !errors.Is(err, context.Canceled) || types.KindOf(err) != types.KindCanceled {
		t.Errorf("stoppedBy(cancelled) = %v, kind %v", err, types.KindOf(err))
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if err := stoppedBy(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("stoppedBy(expired) = %v", err)
	}
	if err := stoppedBy(context.Background()); !errors.Is(err, context.Canceled) {
		t.Errorf("stoppedBy(live) = %v, want a cancellation rather than nil", err)
	}
}

// The pool costs the same few allocations for a thousand steps as for the few
// it takes to fill it: it hands out indices with a counter, where a goroutine
// per step and a channel would cost allocations for every one of them. The
// baseline is measured, with as many steps as workers, and the budget is the
// baseline plus two, as the other allocation budgets in this package are what
// was measured plus two.
func TestRunBoundedAllocationsDoNotGrowWithTheNumberOfSteps(t *testing.T) {
	const (
		workers = 4
		margin  = 2
	)
	step := func(context.Context, int) error { return nil }
	measure := func(steps int, stop bool) float64 {
		return testing.AllocsPerRun(20, func() {
			runBounded(context.Background(), "test", steps, boundedOptions{workers: workers, stopOnFailure: stop}, step)
		})
	}
	for _, stop := range []bool{false, true} {
		baseline := measure(workers, stop)
		if baseline == 0 {
			t.Fatalf("a run of %d steps allocated nothing (stop on failure %v): the measurement is not measuring the pool", workers, stop)
		}
		for _, steps := range []int{1, 2, 10, 100, 1000} {
			if got := measure(steps, stop); got > baseline+margin {
				t.Errorf("%d steps (stop on failure %v): %.0f allocations, over the baseline of %.0f for %d steps plus %d", steps, stop, got, baseline, workers, margin)
			}
		}
	}
}

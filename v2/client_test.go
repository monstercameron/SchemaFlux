package schemaflux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The tests of the client call INTERNAL operation functions (ops.Is, ops.Extract)
// with the context a client was put on, because the operation-call helper that
// the v2 operations are written with does not exist yet. What they prove is what
// the call path does with the client: which provider, model, effort, deadline,
// retries, repairs, budget, scheduler, policy and logger a call ran on.

// answerer is a Provider that records every request it is sent and answers each
// with a function of how many it has been sent.
type answerer struct {
	name   string
	answer func(n int, req Request) (Response, error)

	mu       sync.Mutex
	requests []Request
	deadline []time.Time
}

func (a *answerer) Name() string { return a.name }

func (a *answerer) Complete(ctx context.Context, req Request) (Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	n := len(a.requests)
	deadline, _ := ctx.Deadline()
	a.deadline = append(a.deadline, deadline)
	a.mu.Unlock()
	if a.answer == nil {
		return Response{Content: `{"q1":true}`}, nil
	}
	return a.answer(n, req)
}

func (a *answerer) sent() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Request(nil), a.requests...)
}

func (a *answerer) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

// patientAnswerer is an answerer that tells the call path its own retry
// settings: the library's default number of retries and a wait that is short.
type patientAnswerer struct {
	*answerer
	retries int
}

func (p patientAnswerer) RetryPolicy() (int, time.Duration) { return p.retries, time.Millisecond }

// down is an answer that fails every request in a way that is worth retrying.
func down(int, Request) (Response, error) {
	return Response{}, &Error{Kind: ErrUnavailable, Message: "down"}
}

// callIs makes one internal call on ctx. The model is named, so that the call
// does not depend on a provider's table.
func callIs(ctx context.Context) (bool, error) {
	return ops.Is(ctx, "a subject", "Is this a question?", types.OpOptions{Model: "named-model"})
}

// useDefault makes c the default client for the rest of the test and puts back
// what it replaced. The test is not parallel: the default is the process's.
func useDefault(t *testing.T, c Client) {
	t.Helper()
	previous := Default()
	SetDefault(c)
	t.Cleanup(func() { SetDefault(previous) })
}

// useEnvironment makes the environment these variables the one the library's
// clients read, for the rest of the test, and puts back what it replaced.
func useEnvironment(t *testing.T, variables map[string]string) *environmentCell {
	t.Helper()
	cell := &environmentCell{lookup: func(name string) (string, bool) {
		value, ok := variables[name]
		return value, ok
	}}
	previous := environmentOverride.Swap(cell)
	t.Cleanup(func() { environmentOverride.Store(previous) })
	return cell
}

// declareRouteForTest declares where a route runs and what it keeps, as
// provider.DeclareRoute does for a caller. The routes of two tests are told
// apart by the provider's name.
func declareRouteForTest(provider, model string, regions []string, retentionDays int) {
	llm.DeclareRoute(provider, model, regions, retentionDays)
}

// registerPriceForTest gives a model a price, in US dollars per million tokens.
// A test names its model for itself, since the price table is the process's.
func registerPriceForTest(model string, inputPerMillion, outputPerMillion float64) {
	err := pricing.RegisterPricingModel(pricing.PricingModel{
		Provider:                "custom",
		Model:                   model,
		PricePerPromptToken:     inputPerMillion / 1000,
		PricePerCompletionToken: outputPerMillion / 1000,
		Currency:                "USD",
	})
	if err != nil {
		panic(err)
	}
}

// kindOf is the kind of the *Error err is, failing the test when it is not one.
func kindOf(t *testing.T, err error) Kind {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("the error is %T (%v), want an *Error", err, err)
	}
	return e.Kind
}

// asError converts err as an operation's last step does.
func asError(t *testing.T, op string, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(convertError(op, err), &e) {
		t.Fatalf("%v is not an *Error", err)
	}
	return e
}

func TestAClientIsAValue(t *testing.T) {
	t.Parallel()
	first := &answerer{name: "custom"}
	original := Client{Provider: first, Model: "original-model"}
	copied := original
	copied.Model = "changed-model"
	copied.Effort = EffortHigh
	copied.Retries = 5

	if original.Model != "original-model" || original.Effort != "" || original.Retries != 0 {
		t.Fatalf("assigning to a copy changed the original: %+v", original)
	}
	if _, err := ops.Is(WithClient(context.Background(), original), "s", "q?", types.OpOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := first.sent()[0].Model; got != "original-model" {
		t.Errorf("a call on the original asked for %q, want its own model", got)
	}
	if _, err := ops.Is(WithClient(context.Background(), copied), "s", "q?", types.OpOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := first.sent()[1].Model; got != "changed-model" {
		t.Errorf("a call on the copy asked for %q", got)
	}
}

func TestWithClientKeepsASnapshot(t *testing.T) {
	t.Parallel()
	provider := &answerer{name: "custom"}
	c := Client{Provider: provider, Model: "model-at-the-time", Effort: EffortLow}
	ctx := WithClient(context.Background(), c)
	c.Model = "model-after"
	c.Effort = EffortHigh
	c.Provider = &answerer{name: "other"}
	if _, err := ops.Is(ctx, "s", "q?", types.OpOptions{}); err != nil {
		t.Fatal(err)
	}
	got := provider.sent()
	if len(got) != 1 || got[0].Model != "model-at-the-time" || got[0].Effort != EffortLow {
		t.Errorf("the context followed the variable: %+v", got)
	}
}

func TestSetDefaultKeepsASnapshotAndOnlySetDefaultChangesTheDefault(t *testing.T) {
	provider := &answerer{name: "custom"}
	c := Client{Provider: provider, Model: "model-at-the-time"}
	useDefault(t, Client{})

	before := Default()
	_ = WithClient(context.Background(), c)
	other := c
	other.Model = "elsewhere"
	_ = other.Warm(context.Background())
	if got := Default(); !reflect.DeepEqual(got, before) {
		t.Fatalf("making and warming clients changed the default: %+v", got)
	}

	SetDefault(c)
	c.Model = "model-after"
	if got := Default(); got.Model != "model-at-the-time" || got.Provider != Provider(provider) {
		t.Errorf("SetDefault did not keep a snapshot: %+v", got)
	}
	if _, err := callIs2(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := provider.sent()[0].Model; got != "model-at-the-time" {
		t.Errorf("the default asked for %q", got)
	}

	copied := Default()
	copied.Model = "edited-copy"
	if got := Default().Model; got != "model-at-the-time" {
		t.Errorf("editing the copy Default returned changed the default: %q", got)
	}
}

// callIs2 is a call that names no model.
func callIs2(ctx context.Context) (bool, error) {
	return ops.Is(ctx, "s", "q?", types.OpOptions{})
}

func TestDefaultBeforeAnySetDefaultIsTheZeroClient(t *testing.T) {
	t.Parallel()
	// The tests that change the default put it back, so what is seen here is
	// the zero client whatever ran before.
	got := Default()
	if got.Provider != nil || got.Model != "" || got.Budget != nil || got.Scheduler != nil || got.Policy != nil || got.Recorder != nil || got.Logger != nil {
		t.Errorf("the default is not the zero Client: %+v", got)
	}
}

func TestWithClientOfANilContextPanicsAsContextWithValueDoes(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("WithClient(nil, c) did not panic")
		}
		if !strings.Contains(fmt.Sprint(r), "nil parent") {
			t.Errorf("the panic is %v", r)
		}
	}()
	//lint:ignore SA1012 a nil context is what is tested
	_ = WithClient(nil, Client{})
}

func TestACallWithNoClientRunsOnTheWholeDefault(t *testing.T) {
	provider := &answerer{name: "custom", answer: down}
	recorder := &Recorder{}
	budget := &Budget{}
	scheduler := &Scheduler{MaxConcurrent: 3}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	useDefault(t, Client{
		Provider:  &patientAnswerer{answerer: provider, retries: 0},
		Model:     "default-model",
		Effort:    EffortHigh,
		Timeout:   40 * time.Minute,
		Retries:   2,
		Budget:    budget,
		Scheduler: scheduler,
		Policy:    &DataPolicy{},
		Recorder:  recorder,
		Logger:    log,
	})

	ctx := context.Background()
	_, pinned := ops.PinExec(ctx)
	if pinned == nil {
		t.Fatal("a context with no client has no snapshot to run on")
	}

	t.Run("the policy of the default refuses a plain call, and sends nothing", func(t *testing.T) {
		_, err := ops.Is(ctx, "s", "q?", types.OpOptions{})
		if err == nil || kindOf(t, convertError("is", err)) != ErrPolicy {
			t.Fatalf("a call under a default with a policy was not refused by it: %v", err)
		}
		if provider.count() != 0 {
			t.Errorf("the refused call sent %d requests", provider.count())
		}
	})

	// The rest need the policy out of the way; it is the same default but for
	// that, and the routes are declared so that it allows the provider.
	allowed := Client{
		Provider:  &patientAnswerer{answerer: provider, retries: 0},
		Model:     "default-model",
		Effort:    EffortHigh,
		Timeout:   40 * time.Minute,
		Retries:   2,
		Budget:    budget,
		Scheduler: scheduler,
		Recorder:  recorder,
		Logger:    log,
	}
	SetDefault(allowed)
	start := time.Now()
	_, err := callIs2(ctx)
	if err == nil {
		t.Fatal("the provider is down and the call succeeded")
	}
	requests := provider.sent()

	t.Run("provider", func(t *testing.T) {
		if len(requests) == 0 {
			t.Fatal("the default's provider was not called")
		}
	})
	t.Run("model", func(t *testing.T) {
		if requests[0].Model != "default-model" {
			t.Errorf("the model asked for is %q", requests[0].Model)
		}
	})
	t.Run("effort", func(t *testing.T) {
		if requests[0].Effort != EffortHigh {
			t.Errorf("the effort asked for is %q", requests[0].Effort)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		deadline := provider.deadline[0]
		if deadline.IsZero() || deadline.Sub(start) < 39*time.Minute || deadline.Sub(start) > 41*time.Minute {
			t.Errorf("the call's deadline is %v from now, want about 40 minutes", deadline.Sub(start))
		}
	})
	t.Run("retries", func(t *testing.T) {
		if len(requests) != 3 {
			t.Errorf("the call sent %d requests, want 1 and 2 retries", len(requests))
		}
	})
	t.Run("logger", func(t *testing.T) {
		if !strings.Contains(logs.String(), "level=") {
			t.Errorf("nothing reached the default's logger")
		}
	})
	t.Run("process logger is the default's logger", func(t *testing.T) {
		before := logs.Len()
		logger.GetLogger().Warn("a line with no call in reach")
		if !strings.Contains(logs.String()[before:], "a line with no call in reach") {
			t.Error("a line written where no context exists did not go to the default client's logger")
		}
	})
	t.Run("budget", func(t *testing.T) {
		// The budget is the default's own: a call on a model with no price is
		// recorded as unpriced, which is how the ledger shows it was used.
		provider.answer = func(int, Request) (Response, error) {
			return Response{Content: `{"q1":true}`, Usage: Usage{Input: 10, Output: 10}}, nil
		}
		if _, err := callIs2(ctx); err != nil {
			t.Fatal(err)
		}
		if _, complete := budget.Spent(); complete {
			t.Error("the default's budget was not used by the call")
		}
	})
	t.Run("scheduler", func(t *testing.T) {
		if got := pinnedScheduler(ctx); got == nil {
			t.Error("the default's scheduler is not on the snapshot a call pins")
		}
		if stats := scheduler.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
			t.Errorf("a finished call left the scheduler busy: %+v", stats)
		}
	})
	t.Run("recorder", func(t *testing.T) {
		_, cfg := ops.PinExec(ctx)
		deliver(ctx, cfg, oneOutcome("is", 1))
		if got := recorder.Records(); len(got) != 1 || got[0].Op != "is" {
			t.Errorf("the default's recorder was not given the record: %+v", got)
		}
	})
}

// pinnedScheduler is the scheduler a call on ctx would run under.
func pinnedScheduler(ctx context.Context) *ops.Scheduler { return ops.ExecScheduler(ctx) }

func TestACallPinsTheDefaultItStartedWithWhileSetDefaultIsCalled(t *testing.T) {
	// One call that makes two requests: the answer is refused, and the repair
	// is sent through the call path again. The default is replaced between the
	// two; the second request has to go where the first did.
	type Fruit struct {
		Name string `json:"name"`
	}
	started := make(chan struct{})
	release := make(chan struct{})
	first := &answerer{name: "custom", answer: func(n int, _ Request) (Response, error) {
		if n == 1 {
			close(started)
			<-release
			return Response{Content: `not json`}, nil
		}
		return Response{Content: `{"name":"fig"}`}, nil
	}}
	second := &answerer{name: "custom"}
	useDefault(t, Client{Provider: first, Model: "first-model"})

	done := make(chan error, 1)
	var got Fruit
	go func() {
		opts := ops.NewExtractOptions()
		opts.OpOptions.Context = context.Background()
		var err error
		got, err = ops.Extract[Fruit]("a fig", opts)
		done <- err
	}()
	<-started
	SetDefault(Client{Provider: second, Model: "second-model"})
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the call failed: %v", err)
	}
	if got.Name != "fig" {
		t.Errorf("the answer is %+v", got)
	}
	if first.count() != 2 {
		t.Errorf("the client the call started with received %d requests, want both", first.count())
	}
	if second.count() != 0 {
		t.Errorf("the client set while the call was in flight received %d requests", second.count())
	}
	if models := first.sent(); models[1].Model != "first-model" {
		t.Errorf("the repair asked for %q, not the model of the client the call started with", models[1].Model)
	}
	// A call that starts now runs on the new default.
	if _, err := callIs2(context.Background()); err != nil {
		t.Fatal(err)
	}
	if second.count() != 1 {
		t.Errorf("a new call did not run on the new default")
	}
}

func TestADefaultChangedFromManyGoroutinesIsNeverHalfOneClient(t *testing.T) {
	a := &answerer{name: "custom"}
	b := &answerer{name: "custom"}
	useDefault(t, Client{Provider: a, Model: "model-a"})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 40 {
				if i == 0 && j%2 == 0 {
					SetDefault(Client{Provider: b, Model: "model-b"})
					SetDefault(Client{Provider: a, Model: "model-a"})
					continue
				}
				if _, err := callIs2(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	for _, req := range a.sent() {
		if req.Model != "model-a" {
			t.Errorf("a request to a's provider asked for %q: a client was half one and half the other", req.Model)
		}
	}
	for _, req := range b.sent() {
		if req.Model != "model-b" {
			t.Errorf("a request to b's provider asked for %q", req.Model)
		}
	}
}

func TestAModelNamedInTheCallBeatsTheClientsAndTheClientsBeatsNone(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		client string
		call   string
		want   string
	}{
		"both name one":     {"client-model", "call-model", "call-model"},
		"only the client":   {"client-model", "", "client-model"},
		"only the call":     {"", "call-model", "call-model"},
		"the client, space": {"  client-model  ", "", "client-model"},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &answerer{name: "custom"}
			ctx := WithClient(context.Background(), Client{Provider: provider, Model: c.client})
			if _, err := ops.Is(ctx, "s", "q?", types.OpOptions{Model: c.call}); err != nil {
				t.Fatal(err)
			}
			if got := provider.sent()[0].Model; got != c.want {
				t.Errorf("asked for %q, want %q", got, c.want)
			}
		})
	}
}

func TestAnEffortOfTheClientIsAPreferenceAndTheCallsIsARequirement(t *testing.T) {
	t.Parallel()
	provider := &answerer{name: "custom"}
	ctx := WithClient(context.Background(), Client{Provider: provider, Effort: EffortHigh})
	if _, err := ops.Is(ctx, "s", "q?", types.OpOptions{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Is(ctx, "s", "q?", types.OpOptions{Model: "m", Effort: types.Effort(EffortLow)}); err != nil {
		t.Fatal(err)
	}
	got := provider.sent()
	if got[0].Effort != EffortHigh || !got[0].EffortPreferred {
		t.Errorf("the client's effort went as %q preferred=%v, want high, a preference", got[0].Effort, got[0].EffortPreferred)
	}
	if got[1].Effort != EffortLow || got[1].EffortPreferred {
		t.Errorf("the call's effort went as %q preferred=%v, want low, a requirement", got[1].Effort, got[1].EffortPreferred)
	}
}

func TestSettingsHaveNoOrder(t *testing.T) {
	t.Parallel()
	provider := &answerer{name: "custom"}
	budget, scheduler, policy, recorder := &Budget{Limit: 2}, &Scheduler{MaxConcurrent: 1}, &DataPolicy{Providers: []string{"custom"}}, &Recorder{}
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	setters := []func(*Client){
		func(c *Client) { c.Provider = provider },
		func(c *Client) { c.Model = "m" },
		func(c *Client) { c.Effort = EffortLow },
		func(c *Client) { c.Timeout = time.Minute },
		func(c *Client) { c.Retries = 4 },
		func(c *Client) { c.Repairs = 2 },
		func(c *Client) { c.Budget = budget },
		func(c *Client) { c.Scheduler = scheduler },
		func(c *Client) { c.Policy = policy },
		func(c *Client) { c.Recorder = recorder },
		func(c *Client) { c.Logger = log },
		func(c *Client) { c.OmitReinforcement = true },
	}
	var want Client
	for _, set := range setters {
		set(&want)
	}
	if got := fieldsSet(want); got != len(setters) {
		t.Fatalf("the setters set %d of %d fields: the test does not cover a field", got, len(setters))
	}
	orders := [][]int{}
	forward, backward := make([]int, len(setters)), make([]int, len(setters))
	for i := range setters {
		forward[i], backward[i] = i, len(setters)-1-i
	}
	orders = append(orders, forward, backward)
	random := rand.New(rand.NewSource(1))
	for range 200 {
		orders = append(orders, random.Perm(len(setters)))
	}
	for _, order := range orders {
		var got Client
		for _, i := range order {
			setters[i](&got)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v gave %+v, want %+v", order, got, want)
		}
	}
}

// fieldsSet is how many fields of c are not their zero value.
func fieldsSet(c Client) int {
	n := 0
	v := reflect.ValueOf(c)
	for i := range v.NumField() {
		if !v.Field(i).IsZero() {
			n++
		}
	}
	return n
}

func TestRetriesZeroIsTheDefaultAndANegativeNumberIsNone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		retries int
		want    int
	}{
		{"zero is the provider's own setting, three", 0, 4},
		{"one", 1, 2},
		{"two", 2, 3},
		{"five", 5, 6},
		{"minus one is never", -1, 1},
		{"minus a hundred is never", -100, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			provider := &answerer{name: "custom", answer: down}
			// The provider says nothing of retries but "the library's default",
			// which is zero in the convention of a Retrier, and a wait of 1 ms.
			ctx := WithClient(context.Background(), Client{Provider: patientAnswerer{answerer: provider}, Retries: c.retries})
			if _, err := callIs(ctx); err == nil {
				t.Fatal("a provider that is always down gave an answer")
			}
			if got := provider.count(); got != c.want {
				t.Errorf("the provider received %d requests, want %d", got, c.want)
			}
		})
	}
}

func TestRepairsZeroIsOneAndANegativeNumberIsNone(t *testing.T) {
	t.Parallel()
	type Fruit struct {
		Name string `json:"name"`
	}
	for _, c := range []struct {
		name    string
		repairs int
		want    int
		fails   bool
	}{
		{"zero is the default of one repair", 0, 2, false},
		{"one", 1, 2, false},
		{"two, not needed", 2, 2, false},
		{"minus one is never", -1, 1, true},
		{"minus five is never", -5, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			provider := &answerer{name: "custom", answer: func(n int, _ Request) (Response, error) {
				if n == 1 {
					return Response{Content: `not json`}, nil
				}
				return Response{Content: `{"name":"fig"}`}, nil
			}}
			ctx := WithClient(context.Background(), Client{Provider: provider, Model: "m", Repairs: c.repairs})
			opts := ops.NewExtractOptions()
			opts.OpOptions.Context = ctx
			_, err := ops.Extract[Fruit]("a fig", opts)
			if (err != nil) != c.fails {
				t.Errorf("error = %v, want failure = %v", err, c.fails)
			}
			if got := provider.count(); got != c.want {
				t.Errorf("the provider received %d requests, want %d", got, c.want)
			}
		})
	}
	t.Run("two are used when two are needed", func(t *testing.T) {
		t.Parallel()
		provider := &answerer{name: "custom", answer: func(n int, _ Request) (Response, error) {
			if n <= 2 {
				return Response{Content: `not json`}, nil
			}
			return Response{Content: `{"name":"fig"}`}, nil
		}}
		opts := ops.NewExtractOptions()
		opts.OpOptions.Context = WithClient(context.Background(), Client{Provider: provider, Model: "m", Repairs: 2})
		if _, err := ops.Extract[Fruit]("a fig", opts); err != nil {
			t.Fatal(err)
		}
		if provider.count() != 3 {
			t.Errorf("the provider received %d requests, want 3", provider.count())
		}
	})
}

func TestTheClientsTimeoutIsTheDeadlineOfACallThatHasNoneAndNothingElse(t *testing.T) {
	t.Parallel()
	t.Run("a call with no deadline gets the client's timeout", func(t *testing.T) {
		provider := &answerer{name: "custom"}
		ctx := WithClient(context.Background(), Client{Provider: provider, Timeout: 17 * time.Minute})
		start := time.Now()
		if _, err := callIs(ctx); err != nil {
			t.Fatal(err)
		}
		got := provider.deadline[0].Sub(start)
		if got < 16*time.Minute+30*time.Second || got > 17*time.Minute+30*time.Second {
			t.Errorf("the deadline is %v away, want about 17 minutes", got)
		}
	})
	t.Run("a context that has a deadline keeps it, shorter or longer", func(t *testing.T) {
		for name, d := range map[string]time.Duration{"shorter": time.Minute, "longer": 3 * time.Hour} {
			provider := &answerer{name: "custom"}
			parent, cancel := context.WithTimeout(context.Background(), d)
			want, _ := parent.Deadline()
			ctx := WithClient(parent, Client{Provider: provider, Timeout: 17 * time.Minute})
			if _, err := callIs(ctx); err != nil {
				t.Fatal(err)
			}
			cancel()
			if !provider.deadline[0].Equal(want) {
				t.Errorf("%s: the deadline is %v, want the context's own %v", name, provider.deadline[0], want)
			}
		}
	})
	t.Run("zero is thirty seconds when the environment names none", func(t *testing.T) {
		provider := &answerer{name: "custom"}
		useEnvironment(t, nil)
		ctx := WithClient(context.Background(), Client{Provider: provider})
		start := time.Now()
		if _, err := callIs(ctx); err != nil {
			t.Fatal(err)
		}
		got := provider.deadline[0].Sub(start)
		if got < 29*time.Second || got > 31*time.Second {
			t.Errorf("the deadline is %v away, want about 30 seconds", got)
		}
	})
	t.Run("zero is SCHEMAFLUX_TIMEOUT when it is set", func(t *testing.T) {
		provider := &answerer{name: "custom"}
		useEnvironment(t, map[string]string{"SCHEMAFLUX_TIMEOUT": "12m"})
		ctx := WithClient(context.Background(), Client{Provider: provider})
		start := time.Now()
		if _, err := callIs(ctx); err != nil {
			t.Fatal(err)
		}
		got := provider.deadline[0].Sub(start)
		if got < 11*time.Minute+30*time.Second || got > 12*time.Minute+30*time.Second {
			t.Errorf("the deadline is %v away, want about 12 minutes", got)
		}
	})
	t.Run("the client's own timeout beats the environment's", func(t *testing.T) {
		provider := &answerer{name: "custom"}
		useEnvironment(t, map[string]string{"SCHEMAFLUX_TIMEOUT": "12m"})
		ctx := WithClient(context.Background(), Client{Provider: provider, Timeout: 3 * time.Minute})
		start := time.Now()
		if _, err := callIs(ctx); err != nil {
			t.Fatal(err)
		}
		if got := provider.deadline[0].Sub(start); got > 3*time.Minute+30*time.Second {
			t.Errorf("the deadline is %v away, want about 3 minutes", got)
		}
	})
}

func TestEveryBadSettingIsAConfigurationErrorFromTheFirstCallThatNamesTheField(t *testing.T) {
	t.Parallel()
	bad := map[string]struct {
		client Client
		field  string
	}{
		"a negative timeout":                  {Client{Timeout: -time.Second}, "Client.Timeout"},
		"a negative budget limit":             {Client{Budget: &Budget{Limit: -1}}, "Budget.Limit"},
		"a budget limit that is NaN":          {Client{Budget: &Budget{Limit: math.NaN()}}, "Budget.Limit"},
		"a budget limit of positive infinity": {Client{Budget: &Budget{Limit: math.Inf(1)}}, "Budget.Limit"},
		"a negative MaxConcurrent":            {Client{Scheduler: &Scheduler{MaxConcurrent: -1}}, "Scheduler.MaxConcurrent"},
		"a negative MaxQueued":                {Client{Scheduler: &Scheduler{MaxQueued: -1}}, "Scheduler.MaxQueued"},
		"a negative MaxTokens":                {Client{Scheduler: &Scheduler{MaxTokens: -1}}, "Scheduler.MaxTokens"},
		"a MaxCost that is NaN":               {Client{Scheduler: &Scheduler{MaxCost: math.NaN()}}, "Scheduler.MaxCost"},
		"a negative MaxCost":                  {Client{Scheduler: &Scheduler{MaxCost: -0.5}}, "Scheduler.MaxCost"},
		"a negative MaxPerProvider":           {Client{Scheduler: &Scheduler{MaxPerProvider: -3}}, "Scheduler.MaxPerProvider"},
		"a negative retention":                {Client{Policy: &DataPolicy{MaxRetentionDays: -1}}, "DataPolicy.MaxRetentionDays"},
		"a floor that is no contract level":   {Client{Policy: &DataPolicy{MinContract: ContractFullyGoverned + 1}}, "DataPolicy.MinContract"},
		"a recorder with a negative body":     {Client{Recorder: &Recorder{MaxBody: -1}}, "Recorder.MaxBody"},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider := &answerer{name: "custom"}
			client := c.client
			client.Provider = provider
			client.Model = "m"
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("a bad setting panicked: %v", r)
					}
				}()
				_, err = callIs(WithClient(context.Background(), client))
			}()
			if err == nil {
				t.Fatal("a call on a client with a bad setting succeeded")
			}
			e := asError(t, "is", err)
			if e.Kind != ErrConfiguration {
				t.Errorf("kind = %v, want ErrConfiguration", e.Kind)
			}
			if !strings.Contains(e.Message, c.field) {
				t.Errorf("the message %q does not name %s", e.Message, c.field)
			}
			if e.Op != "is" {
				t.Errorf("Op = %q, want the operation", e.Op)
			}
			if provider.count() != 0 {
				t.Errorf("the provider was sent %d requests before the refusal", provider.count())
			}
			if err := client.Warm(context.Background()); err == nil || kindOf(t, err) != ErrConfiguration || !strings.Contains(err.Error(), c.field) {
				t.Errorf("Warm = %v, want the same configuration error", err)
			}
		})
	}
}

func TestABadSettingOfTheDefaultFailsEveryPlainCall(t *testing.T) {
	provider := &answerer{name: "custom"}
	useDefault(t, Client{Provider: provider, Timeout: -1})
	_, err := callIs2(context.Background())
	if err == nil || asError(t, "is", err).Kind != ErrConfiguration {
		t.Fatalf("a call on a default with a negative timeout: %v", err)
	}
	if provider.count() != 0 {
		t.Errorf("%d requests were sent", provider.count())
	}
}

func TestADataPolicyIsEnforcedOnAPlainCall(t *testing.T) {
	provider := &answerer{name: "policy-test-provider"}
	useDefault(t, Client{Provider: provider, Model: "m", Policy: &DataPolicy{}})
	_, err := callIs2(context.Background())
	if err == nil {
		t.Fatal("a call under the zero policy was sent")
	}
	if e := asError(t, "is", err); e.Kind != ErrPolicy {
		t.Errorf("kind = %v, want ErrPolicy", e.Kind)
	}
	if provider.count() != 0 {
		t.Errorf("a refused call sent %d requests", provider.count())
	}
}

func TestADataPolicyThatAllowsTheRouteLetsTheCallThrough(t *testing.T) {
	t.Parallel()
	provider := &answerer{name: "policy-allowed-provider"}
	declareRouteForTest(provider.name, "m", []string{"us"}, 7)
	policy := &DataPolicy{Providers: []string{"policy-allowed-provider"}, Regions: []string{"us"}, MaxRetentionDays: 30}
	ctx := WithClient(context.Background(), Client{Provider: provider, Model: "m", Policy: policy})
	if _, err := callIs2(ctx); err != nil {
		t.Fatalf("a call the policy allows failed: %v", err)
	}
	for name, p := range map[string]*DataPolicy{
		"a provider not listed": {Providers: []string{"another"}, Regions: []string{"us"}, MaxRetentionDays: 30},
		"a region not listed":   {Providers: []string{"policy-allowed-provider"}, Regions: []string{"eu"}, MaxRetentionDays: 30},
		"too little retention":  {Providers: []string{"policy-allowed-provider"}, Regions: []string{"us"}, MaxRetentionDays: 6},
		"no lists at all":       {MaxRetentionDays: 30},
	} {
		before := provider.count()
		_, err := callIs2(WithClient(context.Background(), Client{Provider: provider, Model: "m", Policy: p}))
		if err == nil || asError(t, "is", err).Kind != ErrPolicy {
			t.Errorf("%s: err = %v, want ErrPolicy", name, err)
		}
		if provider.count() != before {
			t.Errorf("%s: a refused call was sent", name)
		}
	}
}

func TestThePolicyIsCopiedByWithClientAndSetDefault(t *testing.T) {
	provider := &answerer{name: "policy-copied-provider"}
	declareRouteForTest(provider.name, "m", []string{"us"}, 0)
	policy := &DataPolicy{Providers: []string{"policy-copied-provider"}, Regions: []string{"us"}}
	c := Client{Provider: provider, Model: "m", Policy: policy}
	ctx := WithClient(context.Background(), c)
	useDefault(t, c)

	// Everything the caller can reach is changed to a policy that allows
	// nothing the route could be.
	policy.Providers[0] = "somebody-else"
	policy.Regions[0] = "mars"
	policy.Providers = nil
	policy.MaxRetentionDays = -5
	c.Policy.Regions = nil

	if _, err := callIs2(ctx); err != nil {
		t.Errorf("a context made before the policy was edited was affected by the edit: %v", err)
	}
	if _, err := callIs2(context.Background()); err != nil {
		t.Errorf("the default set before the policy was edited was affected by the edit: %v", err)
	}
	t.Run("Default returns a copy of the policy too", func(t *testing.T) {
		d := Default()
		d.Policy.Providers[0] = "somebody-else"
		d.Policy.AllowCaching = true
		if again := Default(); again.Policy.Providers[0] != "policy-copied-provider" || again.Policy.AllowCaching {
			t.Errorf("editing the policy Default returned changed the default: %+v", again.Policy)
		}
		if _, err := callIs2(context.Background()); err != nil {
			t.Errorf("the default's calls changed: %v", err)
		}
	})
}

func TestABudgetRefusesOnceItsLimitIsReachedAndCopiesShareIt(t *testing.T) {
	t.Parallel()
	provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"q1":true}`, Usage: Usage{Input: 1000, Output: 1000}}, nil
	}}
	// A model with a price, so that what the calls cost is known.
	model := "budget-test-priced-model"
	registerPriceForTest(model, 1000, 1000)
	budget := &Budget{Limit: 1.5}
	c := Client{Provider: provider, Model: model, Budget: budget}
	copied := c
	var refused error
	refusedAt := -1
	for i := range 6 {
		which := c
		if i%2 == 1 {
			which = copied
		}
		if _, err := ops.Is(WithClient(context.Background(), which), "s", "q?", types.OpOptions{}); err != nil {
			refused, refusedAt = err, i
			break
		}
	}
	if refused == nil {
		t.Fatal("six calls of 2 dollars each were all allowed under a limit of 1.50")
	}
	if i := refusedAt; i != 1 {
		t.Fatalf("the call refused was number %d, want the second, the first copy's", i)
	}
	if e := asError(t, "is", refused); e.Kind != ErrBudgetExceeded {
		t.Errorf("kind = %v, want ErrBudgetExceeded", e.Kind)
	}
	spent, complete := budget.Spent()
	if !complete || math.Abs(spent-2) > 1e-9 {
		t.Errorf("Spent = %v, %v, want 2 dollars, complete", spent, complete)
	}
	if provider.count() != 1 {
		t.Errorf("the provider received %d requests, want 1: the copy's call was refused by the ledger the first call spent from", provider.count())
	}
}

func TestABudgetWithNoLimitCountsAndNeverRefuses(t *testing.T) {
	t.Parallel()
	provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"q1":true}`, Usage: Usage{Input: 1000, Output: 1000}}, nil
	}}
	model := "budget-test-counted-model"
	registerPriceForTest(model, 1000, 1000)
	budget := &Budget{}
	ctx := WithClient(context.Background(), Client{Provider: provider, Model: model, Budget: budget})
	for range 5 {
		if _, err := callIs2(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if spent, complete := budget.Spent(); !complete || math.Abs(spent-10) > 1e-9 {
		t.Errorf("Spent = %v, %v, want 10 dollars", spent, complete)
	}
}

func TestSpentOfAModelWithNoPriceIsAFloorAndOfANilBudgetIsZero(t *testing.T) {
	t.Parallel()
	var none *Budget
	if usd, complete := none.Spent(); usd != 0 || !complete {
		t.Errorf("nil Budget Spent = %v, %v", usd, complete)
	}
	budget := &Budget{}
	if usd, complete := budget.Spent(); usd != 0 || !complete {
		t.Errorf("a new Budget Spent = %v, %v", usd, complete)
	}
	provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"q1":true}`, Usage: Usage{Input: 10, Output: 10}}, nil
	}}
	ctx := WithClient(context.Background(), Client{Provider: provider, Model: "model-with-no-price-at-all", Budget: budget})
	if _, err := callIs2(ctx); err != nil {
		t.Fatal(err)
	}
	if _, complete := budget.Spent(); complete {
		t.Error("a call on an unpriced model left the figure marked complete")
	}
}

func TestTwoClientsWithTheirOwnBudgetsSpendFromTheirOwn(t *testing.T) {
	t.Parallel()
	model := "budget-test-two-clients-model"
	registerPriceForTest(model, 1000, 1000)
	provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
		return Response{Content: `{"q1":true}`, Usage: Usage{Input: 1000, Output: 1000}}, nil
	}}
	a, b := &Budget{Limit: 1}, &Budget{Limit: 1}
	ctxA := WithClient(context.Background(), Client{Provider: provider, Model: model, Budget: a})
	ctxB := WithClient(context.Background(), Client{Provider: provider, Model: model, Budget: b})
	if _, err := callIs2(ctxA); err != nil {
		t.Fatal(err)
	}
	if _, err := callIs2(ctxA); err == nil {
		t.Error("a's second call was allowed after a reached its limit")
	}
	if _, err := callIs2(ctxB); err != nil {
		t.Errorf("b was refused for a's spending: %v", err)
	}
}

func TestASchedulerBoundsHowManyRequestsAreInFlightAndReportsItsLoad(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		scheduler := &Scheduler{MaxConcurrent: 2}
		var mu sync.Mutex
		inFlight, peak := 0, 0
		gate := make(chan struct{})
		provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
			mu.Lock()
			inFlight++
			peak = max(peak, inFlight)
			mu.Unlock()
			<-gate
			mu.Lock()
			inFlight--
			mu.Unlock()
			return Response{Content: `{"q1":true}`}, nil
		}}
		ctx := WithClient(context.Background(), Client{Provider: provider, Model: "m", Scheduler: scheduler})
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() {
				if _, err := callIs2(ctx); err != nil {
					t.Error(err)
				}
			})
		}
		// Every call is either with the provider or waiting for a slot once the
		// bubble is idle.
		synctest.Wait()
		stats := scheduler.Stats()
		if stats.InFlight != 2 || stats.Queued != 4 {
			t.Errorf("Stats = %+v, want 2 in flight and 4 queued", stats)
		}
		close(gate)
		wg.Wait()
		if peak > 2 {
			t.Errorf("%d requests were with the provider at once, want at most 2", peak)
		}
		if stats := scheduler.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
			t.Errorf("Stats after the calls = %+v", stats)
		}
		_ = scheduler.Close(context.Background())
	})
}

func TestASchedulerWithAFullQueueRefusesWithBusy(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		scheduler := &Scheduler{MaxConcurrent: 1, MaxQueued: 1}
		gate := make(chan struct{})
		provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
			<-gate
			return Response{Content: `{"q1":true}`}, nil
		}}
		ctx := WithClient(context.Background(), Client{Provider: provider, Model: "m", Scheduler: scheduler})
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { _, _ = callIs2(ctx) })
		}
		synctest.Wait()
		if stats := scheduler.Stats(); stats.InFlight != 1 || stats.Queued != 1 {
			t.Fatalf("Stats = %+v, want one in flight and one queued", stats)
		}
		_, err := callIs2(ctx)
		close(gate)
		wg.Wait()
		if err == nil || asError(t, "is", err).Kind != ErrBusy {
			t.Errorf("a call that found the queue full: %v, want ErrBusy", err)
		}
		_ = scheduler.Close(context.Background())
	})
}

func TestSchedulerCloseRefusesWhatWaitsAndEveryLaterRequest(t *testing.T) {
	t.Parallel()
	scheduler := &Scheduler{MaxConcurrent: 1}
	provider := &answerer{name: "custom"}
	ctx := WithClient(context.Background(), Client{Provider: provider, Model: "m", Scheduler: scheduler})
	if _, err := callIs2(ctx); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := scheduler.Close(context.Background()); err != nil {
		t.Errorf("a second Close = %v", err)
	}
	before := provider.count()
	_, err := callIs2(ctx)
	if err == nil || asError(t, "is", err).Kind != ErrClosed {
		t.Errorf("a call on a closed scheduler: %v, want ErrClosed", err)
	}
	if provider.count() != before {
		t.Error("a request was sent through a closed scheduler")
	}
}

func TestSchedulerCloseOfOneNoCallUsedAndOfNilAreDefined(t *testing.T) {
	t.Parallel()
	var none *Scheduler
	if err := none.Close(context.Background()); err != nil {
		t.Errorf("nil Close = %v", err)
	}
	if got := none.Stats(); got != (SchedulerStats{}) {
		t.Errorf("nil Stats = %+v", got)
	}
	unused := &Scheduler{MaxConcurrent: 4}
	if got := unused.Stats(); got != (SchedulerStats{}) {
		t.Errorf("Stats of a scheduler nothing used = %+v", got)
	}
	if err := unused.Close(context.Background()); err != nil {
		t.Errorf("Close of a scheduler nothing used = %v", err)
	}
	if err := (&Scheduler{MaxQueued: -1}).Close(context.Background()); err == nil || kindOf(t, err) != ErrConfiguration {
		t.Errorf("Close of a scheduler with a bad bound = %v", err)
	}
	//lint:ignore SA1012 a nil context is what is tested
	if err := unused.Close(nil); err == nil || kindOf(t, err) != ErrInvalidRequest {
		t.Errorf("Close(nil) = %v, want ErrInvalidRequest", err)
	}
}

func TestSchedulerCloseEndsWhenItsContextDoesIfWorkIsStillRunning(t *testing.T) {
	t.Parallel()
	scheduler := &Scheduler{MaxConcurrent: 1}
	started, release := make(chan struct{}), make(chan struct{})
	provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
		close(started)
		<-release
		return Response{Content: `{"q1":true}`}, nil
	}}
	ctx := WithClient(context.Background(), Client{Provider: provider, Model: "m", Scheduler: scheduler})
	done := make(chan struct{})
	go func() { _, _ = callIs2(ctx); close(done) }()
	<-started
	expired, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := scheduler.Close(expired); err == nil {
		t.Error("Close returned success while a request was still in flight and its context ended")
	}
	close(release)
	<-done
	if err := scheduler.Close(context.Background()); err != nil {
		t.Errorf("Close after the work ended = %v", err)
	}
}

func TestTheClientsRecorderIsGivenTheRecordsOfItsCalls(t *testing.T) {
	t.Parallel()
	var viaClient, viaContext Recorder
	ctx := WithRecorder(WithClient(context.Background(), Client{Recorder: &viaClient, Provider: &answerer{name: "custom"}}), &viaContext)
	_, cfg := ops.PinExec(ctx)
	deliver(ctx, cfg, oneOutcome("extract", 2))
	for name, rec := range map[string]*Recorder{"the client's": &viaClient, "the context's": &viaContext} {
		if got := rec.Records(); len(got) != 1 || got[0].Op != "extract" || got[0].Requests != 2 {
			t.Errorf("%s recorder holds %+v", name, got)
		}
	}
	// A client without a recorder records nothing, and a call with a recorder
	// of its own still does.
	var alone Recorder
	plain := WithClient(context.Background(), Client{Provider: &answerer{name: "custom"}})
	_, cfg = ops.PinExec(plain)
	deliver(plain, cfg, oneOutcome("extract", 1))
	deliver(WithRecorder(plain, &alone), cfg, oneOutcome("extract", 1))
	if len(alone.Records()) != 1 || len(viaClient.Records()) != 1 {
		t.Errorf("records: alone %d, client's %d", len(alone.Records()), len(viaClient.Records()))
	}
}

func TestALoggerOfTheCallsClientReceivesItsLinesAndAClientWithNoneWritesNothing(t *testing.T) {
	// Not parallel: it replaces the process logger and puts it back.
	var processLines, clientLines bytes.Buffer
	processLog := slog.New(slog.NewTextHandler(&processLines, &slog.HandlerOptions{Level: slog.LevelDebug}))
	previous := logger.GetLogger()
	logger.SetLogger(telemetry.FromSlog(processLog))
	t.Cleanup(func() { logger.SetLogger(previous) })

	callThatRetries := func(c Client) {
		provider := &answerer{name: "custom", answer: down}
		c.Provider = patientAnswerer{answerer: provider}
		c.Model = "m"
		c.Retries = 1
		_, _ = callIs2(WithClient(context.Background(), c))
		if provider.count() != 2 {
			t.Fatalf("the provider received %d requests: the call did not retry, so a line was due", provider.count())
		}
	}

	callThatRetries(Client{Logger: slog.New(slog.NewTextHandler(&clientLines, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if !strings.Contains(clientLines.String(), "retry") {
		t.Errorf("the client's logger was not told of the retry: %q", clientLines.String())
	}
	if processLines.Len() != 0 {
		t.Errorf("a call on a client with a logger wrote to the process logger: %q", processLines.String())
	}

	clientLines.Reset()
	callThatRetries(Client{})
	if processLines.Len() != 0 {
		t.Errorf("a client with no Logger wrote to the process logger, which is the default's, not its own: %q", processLines.String())
	}
}

func TestSetDefaultWithNoLoggerSilencesTheProcessLoggerAndALoggerTakesItOver(t *testing.T) {
	var lines bytes.Buffer
	useDefault(t, Client{})
	logger.GetLogger().Warn("before")
	useDefault(t, Client{Logger: slog.New(slog.NewTextHandler(&lines, nil))})
	logger.GetLogger().Warn("after the logger was set")
	if !strings.Contains(lines.String(), "after the logger was set") {
		t.Errorf("the default's logger did not get the line: %q", lines.String())
	}
	if strings.Contains(lines.String(), "before") {
		t.Error("a line written before the logger was set reached it")
	}
	useDefault(t, Client{})
	lines.Reset()
	logger.GetLogger().Warn("after the logger was taken away")
	if lines.Len() != 0 {
		t.Errorf("a default with no logger still wrote: %q", lines.String())
	}
}

func TestMakingACallOnTheDefaultAllocatesOneMoreThanOnAClientOfTheContext(t *testing.T) {
	// Not parallel: AllocsPerRun reads the whole process's allocation count.
	provider := &answerer{name: "custom"}
	c := Client{Provider: provider, Model: "m"}
	useDefault(t, c)
	explicit := WithClient(context.Background(), c)
	plain := context.Background()
	run := func(ctx context.Context) func() {
		return func() { _, _ = callIs2(ctx) }
	}
	withClient := testing.AllocsPerRun(200, run(explicit))
	withDefault := testing.AllocsPerRun(200, run(plain))
	t.Logf("allocations per call: %v on the default, %v on a client of the context", withDefault, withClient)
	if withDefault > withClient+1 {
		t.Errorf("a call on the default allocates %v times and on a client of the context %v: more than the one the pin costs", withDefault, withClient)
	}
}

func TestWithClientAllocatesTheSnapshotTheAdapterAndOneContextValue(t *testing.T) {
	// Not parallel: AllocsPerRun reads the whole process's allocation count.
	provider := &answerer{name: "custom"}
	ctx := context.Background()
	c := Client{Provider: provider, Model: "m", Timeout: time.Minute}
	got := testing.AllocsPerRun(200, func() { _ = WithClient(ctx, c) })
	t.Logf("WithClient allocates %v times", got)
	// The snapshot, the adapter of a provider written outside the library and
	// the context value. A provider the provider package built has no adapter.
	if got > 3 {
		t.Errorf("WithClient allocates %v times, want at most 3 (the snapshot, the adapter, the context value)", got)
	}
}

func TestACallOnTheClientOfTheContextIgnoresTheEnvironmentForAProviderGivenInCode(t *testing.T) {
	cell := useEnvironment(t, map[string]string{
		"SCHEMAFLUX_PROVIDER": "anthropic",
		"ANTHROPIC_API_KEY":   "key-that-must-not-be-used",
		"SCHEMAFLUX_MODEL":    "environment-model",
	})
	provider := &answerer{name: "custom"}
	ctx := WithClient(context.Background(), Client{Provider: provider, Timeout: time.Minute})
	if _, err := ops.Is(ctx, "s", "q?", types.OpOptions{Model: "named-model"}); err != nil {
		t.Fatal(err)
	}
	if cell.builds.Load() != 0 {
		t.Error("a client with a provider of its own built the environment's provider")
	}
	if got := provider.sent()[0].Model; got != "named-model" {
		t.Errorf("asked for %q", got)
	}
}

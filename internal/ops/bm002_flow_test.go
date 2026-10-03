package ops

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// BM-002. Branch, Whether, Select, RouteBy, Gather and Each.

func cfStep(value string, ran *atomic.Int32) Step[string] {
	return func(context.Context) (string, error) {
		if ran != nil {
			ran.Add(1)
		}
		return value, nil
	}
}

func TestBranchRunsOneOfTwo(t *testing.T) {
	failing := errors.New("the step failed")
	fails := func(context.Context) (string, error) { return "", failing }
	cases := []struct {
		name            string
		yes             bool
		then, otherwise Step[string]
		want            string
		wantErr         error
	}{
		{"yes", true, cfStep("then", nil), cfStep("otherwise", nil), "then", nil},
		{"no", false, cfStep("then", nil), cfStep("otherwise", nil), "otherwise", nil},
		{"yes, and nothing to run", true, nil, cfStep("otherwise", nil), "", nil},
		{"no, and nothing to run", false, cfStep("then", nil), nil, "", nil},
		{"neither given, yes", true, nil, nil, "", nil},
		{"neither given, no", false, nil, nil, "", nil},
		{"yes, and it fails", true, fails, cfStep("otherwise", nil), "", failing},
		{"no, and it fails", false, cfStep("then", nil), fails, "", failing},
		{"yes, and the other would fail", true, cfStep("then", nil), fails, "then", nil},
		{"no, and the other would fail", false, fails, cfStep("otherwise", nil), "otherwise", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Branch(context.Background(), tc.yes, tc.then, tc.otherwise)
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Errorf("Branch = %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}

	// Only one of the two runs.
	var then, otherwise atomic.Int32
	if _, err := Branch(context.Background(), true, cfStep("a", &then), cfStep("b", &otherwise)); err != nil || then.Load() != 1 || otherwise.Load() != 0 {
		t.Errorf("then ran %d times and otherwise %d", then.Load(), otherwise.Load())
	}
}

// Whether asks once and runs one branch. An answer that cannot be read runs
// neither: a failure is not a no.
func TestWhetherAsksAndRunsOneBranch(t *testing.T) {
	cases := []struct {
		name        string
		response    string
		want        string
		then, other int32
		wantErr     bool
	}{
		{"yes", `{"q1":true}`, "then", 1, 0, false},
		{"no", `{"q1":false}`, "otherwise", 0, 1, false},
		{"no answer", `{}`, "", 0, 0, true},
		{"an answer that is not true or false", `{"q1":"yes"}`, "", 0, 0, true},
		{"null", `{"q1":null}`, "", 0, 0, true},
		{"a number", `{"q1":1}`, "", 0, 0, true},
		{"prose", `yes`, "", 0, 0, true},
		{"nothing", ``, "", 0, 0, true},
		{"yes in a fence", "```json\n{\"q1\":true}\n```", "then", 1, 0, false},
		{"no, with an entry nobody asked for", `{"q1":false,"why":"x"}`, "otherwise", 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, tc.response)
			var then, otherwise atomic.Int32
			got, err := Whether(context.Background(), cfMessage, "Is this about billing?", cfStep("then", &then), cfStep("otherwise", &otherwise))
			if tc.wantErr != (err != nil) || got != tc.want {
				t.Fatalf("Whether = %q, %v", got, err)
			}
			if then.Load() != tc.then || otherwise.Load() != tc.other {
				t.Errorf("then ran %d times and otherwise %d; want %d and %d", then.Load(), otherwise.Load(), tc.then, tc.other)
			}
			if sent.calls != 1 {
				t.Errorf("%d calls for one question", sent.calls)
			}
		})
	}
}

func TestSelectRunsTheRouteThatIsNamed(t *testing.T) {
	routes := func(ran *[3]atomic.Int32) []Route[string] {
		return []Route[string]{
			To("billing", cfStep("to billing", &ran[0])),
			To("technical", cfStep("to technical", &ran[1])),
			To("shipping", cfStep("to shipping", &ran[2])),
		}
	}
	cases := []struct {
		name  string
		label string
		want  string
		ran   int
		kind  types.ErrorKind
	}{
		{"the first", "billing", "to billing", 0, types.KindUnknown},
		{"the second", "technical", "to technical", 1, types.KindUnknown},
		{"the last", "shipping", "to shipping", 2, types.KindUnknown},
		{"none of them", "returns", "", -1, types.KindInvariantViolation},
		{"an empty label", "", "", -1, types.KindInvariantViolation},
		{"another case", "Billing", "", -1, types.KindInvariantViolation},
		{"with space around it", " billing ", "", -1, types.KindInvariantViolation},
		{"a prefix", "bill", "", -1, types.KindInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran [3]atomic.Int32
			got, err := Select(context.Background(), tc.label, routes(&ran)...)
			if types.KindOf(err) != tc.kind && !(tc.kind == types.KindUnknown && err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.kind)
			}
			if got != tc.want {
				t.Errorf("Select = %q, want %q", got, tc.want)
			}
			for i := range ran {
				want := int32(0)
				if i == tc.ran {
					want = 1
				}
				if ran[i].Load() != want {
					t.Errorf("route %d ran %d times, want %d", i, ran[i].Load(), want)
				}
			}
			if err != nil && strings.Contains(err.Error(), "returns") {
				t.Errorf("the error carries the label, which is the model's or the caller's: %v", err)
			}
		})
	}

	bad := []struct {
		name   string
		routes []Route[string]
	}{
		{"no routes", nil},
		{"a blank label", []Route[string]{To("a", cfStep("a", nil)), To(" ", cfStep("b", nil))}},
		{"a label twice", []Route[string]{To("a", cfStep("a", nil)), To("a", cfStep("b", nil))}},
		{"a route with nothing to run", []Route[string]{To("a", cfStep("a", nil)), To[string]("b", nil)}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Select(context.Background(), "a", tc.routes...); types.KindOf(err) != types.KindConfiguration {
				t.Errorf("Select: err = %v, want a configuration error", err)
			}
			sent := answering(t, `{"q1":1}`)
			if _, err := RouteBy(context.Background(), cfMessage, "Which?", tc.routes); types.KindOf(err) != types.KindConfiguration || sent.calls != 0 {
				t.Errorf("RouteBy: err = %v after %d calls, want a configuration error before any", err, sent.calls)
			}
		})
	}
}

// RouteBy asks once and runs the route chosen. An answer that names no route
// runs none: a failure is not the first route.
func TestRouteByAsksAndRunsTheRouteChosen(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     string
		ran      int
		wantErr  bool
	}{
		{"the first", `{"q1":1}`, "to billing", 0, false},
		{"the second", `{"q1":2}`, "to technical", 1, false},
		{"the third", `{"q1":3}`, "to shipping", 2, false},
		{"zero", `{"q1":0}`, "", -1, true},
		{"past the end", `{"q1":4}`, "", -1, true},
		{"no answer", `{}`, "", -1, true},
		{"the label itself", `{"q1":"billing"}`, "", -1, true},
		{"null", `{"q1":null}`, "", -1, true},
		{"prose", `billing`, "", -1, true},
		{"a fraction", `{"q1":1.5}`, "", -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, tc.response)
			var ran [3]atomic.Int32
			got, err := RouteBy(context.Background(), cfMessage, "Which team should handle this?", []Route[string]{
				To("billing", cfStep("to billing", &ran[0])),
				To("technical", cfStep("to technical", &ran[1])),
				To("shipping", cfStep("to shipping", &ran[2])),
			})
			if tc.wantErr != (err != nil) || got != tc.want {
				t.Fatalf("RouteBy = %q, %v", got, err)
			}
			for i := range ran {
				want := int32(0)
				if i == tc.ran {
					want = 1
				}
				if ran[i].Load() != want {
					t.Errorf("route %d ran %d times, want %d", i, ran[i].Load(), want)
				}
			}
			if sent.calls != 1 {
				t.Errorf("%d calls for one question", sent.calls)
			}
			for _, label := range []string{`1: "billing"`, `2: "technical"`, `3: "shipping"`} {
				if !strings.Contains(sent.user, label) {
					t.Errorf("the message does not offer %s", label)
				}
			}
		})
	}
}

// overlap counts how many steps are running at once.
type overlap struct {
	mu      sync.Mutex
	running int
	most    int
}

func (o *overlap) step(value string, hold time.Duration) Step[string] {
	return func(ctx context.Context) (string, error) {
		o.mu.Lock()
		o.running++
		if o.running > o.most {
			o.most = o.running
		}
		o.mu.Unlock()
		defer func() {
			o.mu.Lock()
			o.running--
			o.mu.Unlock()
		}()
		select {
		case <-time.After(hold):
			return value, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// Gather runs its steps at once, returns their results in the order given, and
// stops the rest when one fails.
func TestGatherRunsItsStepsAtOnce(t *testing.T) {
	ctx := context.Background()

	t.Run("in order, and at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var seen overlap
			start := time.Now()
			got, err := Gather(ctx, seen.step("a", 150*time.Millisecond), seen.step("b", 100*time.Millisecond), seen.step("c", 120*time.Millisecond))
			if err != nil || strings.Join(got, "") != "abc" {
				t.Fatalf("Gather = %v, %v", got, err)
			}
			if seen.most != 3 {
				t.Errorf("at most %d steps ran at once, want 3", seen.most)
			}
			if elapsed := time.Since(start); elapsed != 150*time.Millisecond {
				t.Errorf("three steps of 150, 100 and 120 ms took %v, want the 150 of the slowest: they did not run at once", elapsed)
			}
		})
	})

	t.Run("no steps", func(t *testing.T) {
		got, err := Gather[string](ctx)
		if err != nil || len(got) != 0 {
			t.Errorf("Gather = %v, %v", got, err)
		}
	})

	t.Run("one step", func(t *testing.T) {
		got, err := Gather(ctx, cfStep("only", nil))
		if err != nil || len(got) != 1 || got[0] != "only" {
			t.Errorf("Gather = %v, %v", got, err)
		}
	})

	t.Run("a failure stops the rest", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			failing := errors.New("the second step failed")
			var cancelled atomic.Int32
			slow := func(ctx context.Context) (string, error) {
				select {
				case <-time.After(5 * time.Second):
					return "late", nil
				case <-ctx.Done():
					cancelled.Add(1)
					return "", ctx.Err()
				}
			}
			start := time.Now()
			got, err := Gather(ctx, slow, func(context.Context) (string, error) { return "", failing }, slow)
			if !errors.Is(err, failing) || got != nil {
				t.Fatalf("Gather = %v, %v; want the failure and no results", got, err)
			}
			if cancelled.Load() != 2 {
				t.Errorf("%d of the two other steps were cancelled", cancelled.Load())
			}
			if elapsed := time.Since(start); elapsed != 0 {
				t.Errorf("Gather waited %v for steps it had cancelled", elapsed)
			}
		})
	})

	t.Run("the first failure is the one returned", func(t *testing.T) {
		first := errors.New("first")
		got, err := Gather(ctx,
			func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() },
			func(context.Context) (string, error) { return "", first },
		)
		if !errors.Is(err, first) || got != nil {
			t.Errorf("Gather = %v, %v", got, err)
		}
	})

	t.Run("a nil step", func(t *testing.T) {
		if _, err := Gather(ctx, cfStep("a", nil), nil); types.KindOf(err) != types.KindConfiguration {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a step that panics", func(t *testing.T) {
		got, err := Gather(ctx, cfStep("a", nil), func(context.Context) (string, error) { panic("the caller's secret") })
		if err == nil || got != nil {
			t.Fatalf("Gather = %v, %v", got, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("the error carries the panic's value: %v", err)
		}
	})

	t.Run("one step that panics", func(t *testing.T) {
		if _, err := Gather(ctx, func(context.Context) (string, error) { panic("x") }); err == nil {
			t.Error("a panic in the only step was not reported")
		}
	})

	t.Run("the caller's context is already over", func(t *testing.T) {
		over, cancel := context.WithCancel(ctx)
		cancel()
		var seen overlap
		if _, err := Gather(over, seen.step("a", time.Second), seen.step("b", time.Second)); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a nil context", func(t *testing.T) {
		//lint:ignore SA1012 a nil context is what is being tested
		got, err := Gather(nil, cfStep("a", nil), cfStep("b", nil)) //nolint:staticcheck
		if err != nil || strings.Join(got, "") != "ab" {
			t.Errorf("Gather = %v, %v", got, err)
		}
	})

	t.Run("steps that ask the model overlap their waits", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var inFlight, most atomic.Int32
			previousCaller, previousProvider := currentHooks()
			setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
				now := inFlight.Add(1)
				for {
					seen := most.Load()
					if now <= seen || most.CompareAndSwap(seen, now) {
						break
					}
				}
				defer inFlight.Add(-1)
				time.Sleep(100 * time.Millisecond)
				return `{"q1":true}`, nil
			})
			t.Cleanup(func() {
				setLLMCaller(previousCaller)
				SetDefaultProvider(previousProvider)
			})
			ask := func(question string) Step[bool] {
				return func(ctx context.Context) (bool, error) { return Is(ctx, cfMessage, question) }
			}
			start := time.Now()
			got, err := Gather(ctx, ask("Is it spam?"), ask("Is it urgent?"), ask("Is it angry?"), ask("Is it a refund?"))
			if err != nil || len(got) != 4 {
				t.Fatalf("Gather = %v, %v", got, err)
			}
			if most.Load() != 4 {
				t.Errorf("at most %d questions were with the provider at once, want 4", most.Load())
			}
			if elapsed := time.Since(start); elapsed != 100*time.Millisecond {
				t.Errorf("four waits of 100 ms took %v, want one wait of 100: they did not overlap", elapsed)
			}
		})
	})
}

func TestGather2AndGather3(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		failing := errors.New("failed")
		number := func(n int) Step[int] { return func(context.Context) (int, error) { return n, nil } }
		flag := func(b bool) Step[bool] { return func(context.Context) (bool, error) { return b, nil } }

		a, b, err := Gather2(ctx, cfStep("text", nil), number(7))
		if err != nil || a != "text" || b != 7 {
			t.Errorf("Gather2 = %q, %d, %v", a, b, err)
		}
		a, b, err = Gather2(ctx, cfStep("text", nil), func(context.Context) (int, error) { return 3, failing })
		if !errors.Is(err, failing) || a != "" || b != 0 {
			t.Errorf("Gather2 after a failure = %q, %d, %v; want nothing beside the error", a, b, err)
		}
		a, b, err = Gather2(ctx, func(context.Context) (string, error) { return "half", failing }, number(7))
		if !errors.Is(err, failing) || a != "" || b != 0 {
			t.Errorf("Gather2 after a failure = %q, %d, %v", a, b, err)
		}
		if _, _, err := Gather2[string, int](ctx, nil, number(1)); types.KindOf(err) != types.KindConfiguration {
			t.Errorf("a nil step: %v", err)
		}
		if _, _, err := Gather2(ctx, cfStep("a", nil), Step[int](nil)); types.KindOf(err) != types.KindConfiguration {
			t.Errorf("a nil step: %v", err)
		}

		x, y, z, err := Gather3(ctx, cfStep("text", nil), number(7), flag(true))
		if err != nil || x != "text" || y != 7 || !z {
			t.Errorf("Gather3 = %q, %d, %v, %v", x, y, z, err)
		}
		x, y, z, err = Gather3(ctx, cfStep("text", nil), number(7), func(context.Context) (bool, error) { return true, failing })
		if !errors.Is(err, failing) || x != "" || y != 0 || z {
			t.Errorf("Gather3 after a failure = %q, %d, %v, %v", x, y, z, err)
		}
		if _, _, _, err := Gather3(ctx, cfStep("a", nil), number(1), Step[bool](nil)); types.KindOf(err) != types.KindConfiguration {
			t.Errorf("a nil step: %v", err)
		}
		if _, _, _, err := Gather3(ctx, cfStep("a", nil), func(context.Context) (int, error) { panic("x") }, flag(true)); err == nil {
			t.Error("a panic in a step was not reported")
		}

		// The three run at once.
		var seen overlap
		start := time.Now()
		if _, _, _, err := Gather3(ctx, seen.step("a", 100*time.Millisecond), seen.step("b", 100*time.Millisecond), seen.step("c", 100*time.Millisecond)); err != nil {
			t.Fatalf("Gather3: %v", err)
		}
		if seen.most != 3 || time.Since(start) != 100*time.Millisecond {
			t.Errorf("%d at once, in %v; want 3, in one wait of 100 ms", seen.most, time.Since(start))
		}
	})
}

func TestEachRunsAStepForEveryItem(t *testing.T) {
	ctx := context.Background()
	double := func(_ context.Context, n int) (int, error) { return n * 2, nil }

	cases := []struct {
		name  string
		items int
	}{
		{"none", 0}, {"one", 1}, {"two", 2}, {"as many as run at once", eachAtOnce},
		{"one more than run at once", eachAtOnce + 1}, {"a thousand", 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]int, tc.items)
			for i := range items {
				items[i] = i
			}
			got, err := Each(ctx, items, double)
			if err != nil || len(got) != tc.items {
				t.Fatalf("Each gave %d results, %v", len(got), err)
			}
			for i, value := range got {
				if value != i*2 {
					t.Fatalf("result %d is %d: the results are not in the order of the items", i, value)
				}
			}
		})
	}

	t.Run("no more than a set number at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var running, most atomic.Int32
			items := make([]int, 500)
			_, err := Each(ctx, items, func(context.Context, int) (int, error) {
				now := running.Add(1)
				for {
					seen := most.Load()
					if now <= seen || most.CompareAndSwap(seen, now) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				running.Add(-1)
				return 0, nil
			})
			if err != nil {
				t.Fatalf("Each: %v", err)
			}
			if most.Load() != eachAtOnce {
				t.Errorf("%d ran at once; want exactly %d, the most Each allows: every worker has an item before any of them finishes", most.Load(), eachAtOnce)
			}
		})
	})

	t.Run("a failure stops the rest", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			failing := errors.New("item 3 failed")
			var ran atomic.Int32
			items := make([]int, 10000)
			for i := range items {
				items[i] = i
			}
			got, err := Each(ctx, items, func(ctx context.Context, n int) (string, error) {
				ran.Add(1)
				if n == 3 {
					return "", failing
				}
				select {
				case <-time.After(2 * time.Millisecond):
				case <-ctx.Done():
				}
				return strconv.Itoa(n), nil
			})
			if !errors.Is(err, failing) || got != nil {
				t.Fatalf("Each = %d results, %v", len(got), err)
			}
			// Each worker is on its first item, waiting out its two milliseconds, when
			// the third fails at once and stops the rest, and none starts a second.
			if ran.Load() > eachAtOnce {
				t.Errorf("%d of 10000 items were started, want no more than one for each of the %d workers", ran.Load(), eachAtOnce)
			}
		})
	})

	t.Run("no step", func(t *testing.T) {
		if _, err := Each[int, int](ctx, []int{1}, nil); types.KindOf(err) != types.KindConfiguration {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a step that panics", func(t *testing.T) {
		if _, err := Each(ctx, []int{1, 2, 3}, func(context.Context, int) (int, error) { panic("x") }); err == nil {
			t.Error("a panic in a step was not reported")
		}
	})

	t.Run("the caller's context is already over", func(t *testing.T) {
		over, cancel := context.WithCancel(ctx)
		cancel()
		var ran atomic.Int32
		_, err := Each(over, make([]int, 100), func(context.Context, int) (int, error) { ran.Add(1); return 0, nil })
		if !errors.Is(err, context.Canceled) || ran.Load() != 0 {
			t.Errorf("err %v after %d steps", err, ran.Load())
		}
	})
}

func TestGather4(t *testing.T) {
	ctx := context.Background()
	failing := errors.New("failed")
	number := func(n int) Step[int] { return func(context.Context) (int, error) { return n, nil } }
	flag := func(b bool) Step[bool] { return func(context.Context) (bool, error) { return b, nil } }
	ratio := func(f float64) Step[float64] { return func(context.Context) (float64, error) { return f, nil } }

	a, b, c, d, err := Gather4(ctx, cfStep("text", nil), number(7), flag(true), ratio(0.5))
	if err != nil || a != "text" || b != 7 || !c || d != 0.5 {
		t.Errorf("Gather4 = %q, %d, %v, %v, %v", a, b, c, d, err)
	}

	// Whichever step fails, nothing comes back beside the error.
	steps := []struct {
		name string
		run  func() (string, int, bool, float64, error)
	}{
		{"the first fails", func() (string, int, bool, float64, error) {
			return Gather4(ctx, func(context.Context) (string, error) { return "half", failing }, number(7), flag(true), ratio(0.5))
		}},
		{"the second fails", func() (string, int, bool, float64, error) {
			return Gather4(ctx, cfStep("text", nil), func(context.Context) (int, error) { return 3, failing }, flag(true), ratio(0.5))
		}},
		{"the third fails", func() (string, int, bool, float64, error) {
			return Gather4(ctx, cfStep("text", nil), number(7), func(context.Context) (bool, error) { return true, failing }, ratio(0.5))
		}},
		{"the fourth fails", func() (string, int, bool, float64, error) {
			return Gather4(ctx, cfStep("text", nil), number(7), flag(true), func(context.Context) (float64, error) { return 1, failing })
		}},
	}
	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c, d, err := tc.run()
			if !errors.Is(err, failing) || a != "" || b != 0 || c || d != 0 {
				t.Errorf("Gather4 = %q, %d, %v, %v, %v; want nothing beside the error", a, b, c, d, err)
			}
		})
	}

	nils := []struct {
		name string
		run  func() error
	}{
		{"the first is nil", func() error {
			_, _, _, _, err := Gather4[string, int, bool, float64](ctx, nil, number(1), flag(true), ratio(1))
			return err
		}},
		{"the second is nil", func() error {
			_, _, _, _, err := Gather4(ctx, cfStep("a", nil), Step[int](nil), flag(true), ratio(1))
			return err
		}},
		{"the third is nil", func() error {
			_, _, _, _, err := Gather4(ctx, cfStep("a", nil), number(1), Step[bool](nil), ratio(1))
			return err
		}},
		{"the fourth is nil", func() error {
			_, _, _, _, err := Gather4(ctx, cfStep("a", nil), number(1), flag(true), Step[float64](nil))
			return err
		}},
	}
	for _, tc := range nils {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); types.KindOf(err) != types.KindConfiguration {
				t.Errorf("err = %v, want a configuration error", err)
			}
		})
	}

	if _, _, _, _, err := Gather4(ctx, cfStep("a", nil), number(1), flag(true), func(context.Context) (float64, error) { panic("x") }); err == nil {
		t.Error("a panic in a step was not reported")
	}

	// The four run at once.
	t.Run("the four run at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var seen overlap
			start := time.Now()
			if _, _, _, _, err := Gather4(ctx, seen.step("a", 100*time.Millisecond), seen.step("b", 100*time.Millisecond), seen.step("c", 100*time.Millisecond), seen.step("d", 100*time.Millisecond)); err != nil {
				t.Fatalf("Gather4: %v", err)
			}
			if seen.most != 4 || time.Since(start) != 100*time.Millisecond {
				t.Errorf("%d at once, in %v; want 4, in one wait of 100 ms", seen.most, time.Since(start))
			}
		})
	})
}

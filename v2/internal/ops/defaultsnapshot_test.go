package ops

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// withDefaultExec makes cfg the default snapshot for one test and puts back the
// one that was there.
func withDefaultExec(t *testing.T, cfg *ExecConfig) {
	t.Helper()
	previous := SetDefaultExec(cfg)
	t.Cleanup(func() { SetDefaultExec(previous) })
}

// countingBudget is a budget that counts what is checked and recorded against
// it.
type countingBudget struct {
	mu      sync.Mutex
	checks  int
	records int
}

func (b *countingBudget) Check() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checks++
	return nil
}

func (b *countingBudget) Record(float64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records++
}

func (b *countingBudget) counts() (checks, records int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checks, b.records
}

func TestSetDefaultExecReturnsWhatItReplaced(t *testing.T) {
	first := &ExecConfig{Model: "first"}
	second := &ExecConfig{Model: "second"}
	empty := &ExecConfig{}
	withDefaultExec(t, nil)

	cases := []struct {
		name     string
		set      *ExecConfig
		previous *ExecConfig
		model    string
	}{
		{"the first one replaces nothing", first, nil, "first"},
		{"a second one replaces the first", second, first, "second"},
		{"the same one again replaces itself", second, second, "second"},
		{"nil clears it", nil, second, ""},
		{"clearing nothing returns nil", nil, nil, ""},
		{"a default set after a clear", first, nil, "first"},
		{"clearing returns the one that was set", nil, first, ""},
		{"a snapshot with no fields is a default", empty, nil, ""},
		{"replacing it by a named one", second, empty, "second"},
		{"and clearing again", nil, second, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SetDefaultExec(tc.set); got != tc.previous {
				t.Errorf("SetDefaultExec returned %p, want %p", got, tc.previous)
			}
			if got := ExecModel(context.Background()); got != tc.model {
				t.Errorf("ExecModel on a bare context = %q, want %q", got, tc.model)
			}
		})
	}
}

func TestWithSharedExecConfigCarriesTheSnapshotItself(t *testing.T) {
	withDefaultExec(t, nil)
	policy := types.DataPolicy{AllowedProviders: []string{"openai"}}
	cfg := &ExecConfig{Model: "m", DataPolicy: policy, DataPolicyDeclared: true}

	t.Run("the very pointer is on the context", func(t *testing.T) {
		ctx := WithSharedExecConfig(context.Background(), cfg)
		if got := execConfigFrom(ctx); got != cfg {
			t.Errorf("execConfigFrom = %p, want the snapshot given, %p", got, cfg)
		}
	})
	t.Run("WithExecConfig copies and this does not", func(t *testing.T) {
		if copied := execConfigFrom(WithExecConfig(context.Background(), cfg)); copied == cfg {
			t.Error("WithExecConfig stored the caller's pointer")
		}
	})
	t.Run("a nil snapshot returns the context as it is", func(t *testing.T) {
		parent := context.WithValue(context.Background(), struct{}{}, 1)
		if got := WithSharedExecConfig(parent, nil); got != parent {
			t.Error("a nil snapshot changed the context")
		}
	})
	t.Run("a nil context panics as context.WithValue does", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("no panic for a nil context")
			}
		}()
		//lint:ignore SA1012 the nil parent is what is tested.
		WithSharedExecConfig(nil, cfg)
	})
	t.Run("the readers read it", func(t *testing.T) {
		ctx := WithSharedExecConfig(context.Background(), cfg)
		if ExecModel(ctx) != "m" {
			t.Error("ExecModel does not read it")
		}
		if got, declared := ExecDataPolicy(ctx); !declared || len(got.AllowedProviders) != 1 {
			t.Errorf("ExecDataPolicy = %+v, %v", got, declared)
		}
	})
	t.Run("a nearer snapshot hides it", func(t *testing.T) {
		inner := &ExecConfig{Model: "inner"}
		ctx := WithSharedExecConfig(WithSharedExecConfig(context.Background(), cfg), inner)
		if got := ExecModel(ctx); got != "inner" {
			t.Errorf("ExecModel = %q, want the nearer snapshot's", got)
		}
	})
	t.Run("it hides the default", func(t *testing.T) {
		withDefaultExec(t, &ExecConfig{Model: "default"})
		if got := ExecModel(WithSharedExecConfig(context.Background(), cfg)); got != "m" {
			t.Errorf("ExecModel = %q, want the context's snapshot over the default", got)
		}
	})
	t.Run("the caller's lists are not copied", func(t *testing.T) {
		ctx := WithSharedExecConfig(context.Background(), cfg)
		if &execConfigFrom(ctx).DataPolicy.AllowedProviders[0] != &cfg.DataPolicy.AllowedProviders[0] {
			t.Error("the list was copied")
		}
	})
}

func TestPinExecReturnsTheSnapshotACallRunsOn(t *testing.T) {
	own := &ExecConfig{Model: "own"}
	empty := &ExecConfig{}
	dflt := &ExecConfig{Model: "default"}
	marked := func() context.Context {
		return context.WithValue(context.Background(), struct{ k int }{1}, "kept")
	}

	cases := []struct {
		name string
		ctx  func() context.Context
		dflt *ExecConfig
		// want is the snapshot returned; nil means none.
		want *ExecConfig
		// same is whether the context comes back as it went in.
		same bool
	}{
		{"a context with a snapshot is returned as it is", func() context.Context {
			return WithSharedExecConfig(context.Background(), own)
		}, nil, own, true},
		{"a snapshot on the context beats the default", func() context.Context {
			return WithSharedExecConfig(context.Background(), own)
		}, dflt, own, true},
		{"no snapshot and no default returns the context", context.Background, nil, nil, true},
		{"no snapshot and a default pins it", context.Background, dflt, dflt, false},
		{"a derived context keeps its parent's snapshot", func() context.Context {
			ctx, cancel := context.WithCancel(WithSharedExecConfig(context.Background(), own))
			t.Cleanup(cancel)
			return ctx
		}, dflt, own, true},
		{"an empty snapshot is a snapshot", func() context.Context {
			return WithSharedExecConfig(context.Background(), empty)
		}, dflt, empty, true},
		{"a context with another value is extended, not replaced", marked, dflt, dflt, false},
		{"a context with another value and no default is returned", marked, nil, nil, true},
		{"a nil context with no default stays nil", func() context.Context { return nil }, nil, nil, true},
		{"a nil context with a default is given Background", func() context.Context { return nil }, dflt, dflt, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDefaultExec(t, tc.dflt)
			in := tc.ctx()
			out, got := PinExec(in)
			if got != tc.want {
				t.Errorf("snapshot = %p, want %p", got, tc.want)
			}
			if tc.same != (out == in) {
				t.Errorf("context returned as it is = %v, want %v", out == in, tc.same)
			}
			if tc.want != nil && execConfigFrom(out) != tc.want {
				t.Error("the context does not carry the snapshot PinExec returned")
			}
		})
	}

	t.Run("a copy made by WithExecConfig counts as carried", func(t *testing.T) {
		withDefaultExec(t, dflt)
		in := WithExecConfig(context.Background(), own)
		out, got := PinExec(in)
		if out != in || got == nil || got.Model != "own" {
			t.Errorf("PinExec = %v, %v", out == in, got)
		}
	})
}

func TestDefaultSnapshotIsReadWhole(t *testing.T) {
	budget := &countingBudget{}
	scheduler := NewScheduler(SchedulerLimits{})
	t.Cleanup(func() { scheduler.Close(context.Background()) })
	provider := &namedProvider{name: "default-provider"}
	decisions := 0
	withDefaultExec(t, &ExecConfig{
		Provider:           provider,
		Model:              "default-model",
		Effort:             types.EffortHigh,
		Budget:             budget,
		Scheduler:          scheduler,
		DataPolicy:         types.DataPolicy{AllowedProviders: []string{"default-provider"}},
		DataPolicyDeclared: true,
		DecisionObserver:   func(context.Context, DecisionRecord) { decisions++ },
		Timeout:            7 * time.Second,
		Retries:            4,
		Repairs:            5,
		OmitReinforcement:  true,
	})
	bare := context.Background()

	if got := ExecProvider(bare); got != llm.Provider(provider) {
		t.Errorf("ExecProvider = %v, want the default's", got)
	}
	if got := ExecModel(bare); got != "default-model" {
		t.Errorf("ExecModel = %q", got)
	}
	if got := ExecEffort(bare); got != types.EffortHigh {
		t.Errorf("ExecEffort = %v", got)
	}
	if got := ExecBudget(bare); got != BudgetChecker(budget) {
		t.Errorf("ExecBudget = %v", got)
	}
	if got := ExecScheduler(bare); got != scheduler {
		t.Errorf("ExecScheduler = %v", got)
	}
	if policy, declared := ExecDataPolicy(bare); !declared || len(policy.AllowedProviders) != 1 || policy.AllowedProviders[0] != "default-provider" {
		t.Errorf("ExecDataPolicy = %+v, %v", policy, declared)
	}
	if observer := ExecDecisionObserver(bare); observer == nil {
		t.Error("ExecDecisionObserver is nil")
	} else {
		observer(bare, DecisionRecord{})
		if decisions != 1 {
			t.Error("the default's observer was not the one returned")
		}
	}
	if got := execTimeout(bare); got != 7*time.Second {
		t.Errorf("execTimeout = %v", got)
	}
	if got := execRetries(bare); got != 4 {
		t.Errorf("execRetries = %v", got)
	}
	if !execOmitReinforcement(bare) {
		t.Error("execOmitReinforcement is false")
	}

	// A call with a bare context uses it all: the provider answered and the
	// client's budget was charged. The policy is left off for it, since the
	// route has not been declared to one.
	whole := *defaultExec.Load()
	whole.DataPolicyDeclared = false
	SetDefaultExec(&whole)
	if _, err := callLLM(bare, "system", "user", types.OpOptions{}); err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	if provider.callCount() != 1 {
		t.Errorf("the default's provider was called %d times", provider.callCount())
	}
	if _, records := budget.counts(); records != 1 {
		t.Errorf("the default's budget recorded %d spends", records)
	}
}

// swappingProvider replaces the default snapshot from inside its Complete,
// which is what a SetDefault from another goroutine looks like to a call in
// flight.
type swappingProvider struct {
	namedProvider
	swap func()
}

func (p *swappingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if p.swap != nil {
		p.swap()
	}
	return p.namedProvider.Complete(ctx, req)
}

func TestPinExecKeepsTheClientAcrossASetDefault(t *testing.T) {
	first, second := &countingBudget{}, &countingBudget{}
	secondProvider := &namedProvider{name: "second"}
	provider := &swappingProvider{namedProvider: namedProvider{name: "first"}}
	provider.swap = func() {
		SetDefaultExec(&ExecConfig{Provider: secondProvider, Budget: second, Model: "second-model"})
	}
	withDefaultExec(t, &ExecConfig{Provider: provider, Budget: first, Model: "first-model"})

	// Two requests of one call, as an operation over chunks makes them: the
	// context is pinned once, and both requests read it.
	ctx, _ := PinExec(context.Background())
	for i := range 2 {
		if _, err := callLLM(ctx, "system", "user", types.OpOptions{}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, records := first.counts(); records != 2 {
		t.Errorf("the pinned client's budget recorded %d spends, want 2", records)
	}
	if _, records := second.counts(); records != 0 {
		t.Errorf("the replacement's budget recorded %d spends, want 0", records)
	}
	if secondProvider.callCount() != 0 {
		t.Error("a later request went to the replacement's provider")
	}

	// A request that is not pinned beforehand pins itself: the budget it
	// checked is the budget that is charged.
	SetDefaultExec(&ExecConfig{Provider: provider, Budget: first, Model: "first-model"})
	checksBefore, recordsBefore := first.counts()
	if _, err := callLLM(context.Background(), "system", "user", types.OpOptions{}); err != nil {
		t.Fatal(err)
	}
	checks, records := first.counts()
	if checks == checksBefore || records != recordsBefore+1 {
		t.Errorf("one request checked %d times and recorded %d: want both on the budget it started with", checks-checksBefore, records-recordsBefore)
	}
	if _, records := second.counts(); records != 0 {
		t.Errorf("the replacement's budget recorded %d spends", records)
	}
}

func TestPinExecAllocations(t *testing.T) {
	withDefaultExec(t, &ExecConfig{Model: "m"})
	bare := context.Background()
	if got := testing.AllocsPerRun(200, func() { PinExec(bare) }); got != 1 {
		t.Errorf("PinExec allocates %v times when it pins, want 1", got)
	}
	pinned, _ := PinExec(bare)
	if got := testing.AllocsPerRun(200, func() { PinExec(pinned) }); got != 0 {
		t.Errorf("PinExec allocates %v times when the context carries a snapshot, want 0", got)
	}
	SetDefaultExec(nil)
	if got := testing.AllocsPerRun(200, func() { PinExec(bare) }); got != 0 {
		t.Errorf("PinExec allocates %v times with no default, want 0", got)
	}
}

func TestDecisionObserverOnTheContextIsReadFirst(t *testing.T) {
	var told []string
	note := func(name string) func(context.Context, DecisionRecord) {
		return func(context.Context, DecisionRecord) { told = append(told, name) }
	}
	withDefaultExec(t, &ExecConfig{DecisionObserver: note("default")})
	base := WithSharedExecConfig(context.Background(), &ExecConfig{DecisionObserver: note("client")})

	cases := []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"the default's observer on a bare context", context.Background(), []string{"default"}},
		{"the client's over the default", base, []string{"client"}},
		{"the context's alone", WithDecisionObserver(context.Background(), note("ctx")), []string{"ctx"}},
		{"the context's over the client's", WithDecisionObserver(base, note("ctx")), []string{"ctx"}},
		{"two on the context, nearest first", WithDecisionObserver(WithDecisionObserver(base, note("outer")), note("inner")), []string{"inner", "outer"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			told = nil
			observer := ExecDecisionObserver(tc.ctx)
			if observer == nil {
				t.Fatal("no observer")
			}
			observer(tc.ctx, DecisionRecord{})
			if !reflect.DeepEqual(told, tc.want) {
				t.Errorf("told %v, want %v", told, tc.want)
			}
		})
	}
	t.Run("none anywhere is nil", func(t *testing.T) {
		SetDefaultExec(nil)
		if ExecDecisionObserver(context.Background()) != nil {
			t.Error("an observer was returned")
		}
	})
}

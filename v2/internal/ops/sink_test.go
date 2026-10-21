package ops

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// namedSink is a Sink that says which one it is. It is a pointer type, so two
// of them are equal only when they are the same sink.
type namedSink struct {
	name      string
	decisions bool
	bodies    bool
	maxBody   int
	added     []string
}

func (s *namedSink) Wants() (bool, bool, int) { return s.decisions, s.bodies, s.maxBody }

func (s *namedSink) Add(_ context.Context, o *Outcome) {
	s.added = append(s.added, fmt.Sprintf("%s:%s", s.name, o.Meta.Operation))
}

// sliceSink cannot be compared with ==, since it holds a slice: a sink of this
// type given twice is two sinks as far as EachSink can tell, and must not
// panic it.
type sliceSink struct{ log []string }

func (sliceSink) Wants() (bool, bool, int) { return false, false, 0 }

func (sliceSink) Add(context.Context, *Outcome) {}

func sinkNames(ctx context.Context, cfg *ExecConfig) []string {
	var names []string
	EachSink(ctx, cfg, func(s Sink) {
		switch typed := s.(type) {
		case *namedSink:
			names = append(names, typed.name)
		default:
			names = append(names, fmt.Sprintf("%T", s))
		}
	})
	return names
}

func TestSinksComposeNearestFirstThenTheClients(t *testing.T) {
	a, b, c := &namedSink{name: "a"}, &namedSink{name: "b"}, &namedSink{name: "c"}
	clients := &namedSink{name: "client"}
	cases := []struct {
		name string
		ctx  context.Context
		cfg  *ExecConfig
		want []string
	}{
		{"no sink anywhere gives none", context.Background(), nil, nil},
		{"a client with no sink gives none", context.Background(), &ExecConfig{}, nil},
		{"the client's sink alone", context.Background(), &ExecConfig{Sink: clients}, []string{"client"}},
		{"one on the context alone", WithSink(context.Background(), a), nil, []string{"a"}},
		{"one on the context then the client's", WithSink(context.Background(), a), &ExecConfig{Sink: clients}, []string{"a", "client"}},
		{"two on the context, the nearest first", WithSink(WithSink(context.Background(), a), b), nil, []string{"b", "a"}},
		{"two on the context then the client's", WithSink(WithSink(context.Background(), a), b), &ExecConfig{Sink: clients}, []string{"b", "a", "client"}},
		{"three on the context, the nearest first", WithSink(WithSink(WithSink(context.Background(), a), b), c), nil, []string{"c", "b", "a"}},
		{"a nil sink is not added", WithSink(WithSink(context.Background(), a), nil), nil, []string{"a"}},
		{"a derived context keeps the sinks of its parent", func() context.Context {
			derived, cancel := context.WithCancel(WithSink(context.Background(), a))
			t.Cleanup(cancel)
			return derived
		}(), nil, []string{"a"}},
		{"a sink added under a derived context comes before the parent's", func() context.Context {
			derived, cancel := context.WithCancel(WithSink(context.Background(), a))
			t.Cleanup(cancel)
			return WithSink(derived, b)
		}(), &ExecConfig{Sink: clients}, []string{"b", "a", "client"}},
		{"a nil client reads as none", WithSink(context.Background(), a), nil, []string{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sinkNames(tc.ctx, tc.cfg); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sinks = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a nil context with a client's sink gives the client's", func(t *testing.T) {
		if got := sinkNames(nil, &ExecConfig{Sink: clients}); !reflect.DeepEqual(got, []string{"client"}) {
			t.Errorf("sinks = %v", got)
		}
	})
	t.Run("a nil context and no client gives none", func(t *testing.T) {
		if got := sinkNames(nil, nil); got != nil {
			t.Errorf("sinks = %v", got)
		}
	})
	t.Run("a nil yield is allowed", func(t *testing.T) {
		EachSink(WithSink(context.Background(), a), &ExecConfig{Sink: clients}, nil)
	})
	t.Run("a sink added to one context is not on its sibling", func(t *testing.T) {
		base := context.Background()
		left := WithSink(base, a)
		right := WithSink(base, b)
		if got := sinkNames(left, nil); !reflect.DeepEqual(got, []string{"a"}) {
			t.Errorf("left = %v", got)
		}
		if got := sinkNames(right, nil); !reflect.DeepEqual(got, []string{"b"}) {
			t.Errorf("right = %v", got)
		}
		if got := sinkNames(base, nil); got != nil {
			t.Errorf("base = %v", got)
		}
	})
	t.Run("more sinks than the first buffer holds are all given, in order", func(t *testing.T) {
		ctx := context.Background()
		var want []string
		for i := range 12 {
			name := fmt.Sprintf("s%02d", i)
			ctx = WithSink(ctx, &namedSink{name: name})
			want = append([]string{name}, want...)
		}
		if got := sinkNames(ctx, nil); !reflect.DeepEqual(got, want) {
			t.Errorf("sinks = %v, want %v", got, want)
		}
	})
	t.Run("a nil context panics when a sink is added, as context.WithValue does", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Errorf("WithSink(nil, s) did not panic")
			}
		}()
		_ = WithSink(nil, a)
	})
	t.Run("a nil sink on a nil context returns the nil context without panicking", func(t *testing.T) {
		if got := WithSink(nil, nil); got != nil {
			t.Errorf("WithSink(nil, nil) = %v", got)
		}
	})
	t.Run("a nil sink returns the very context it was given", func(t *testing.T) {
		ctx := WithSink(context.Background(), a)
		if got := WithSink(ctx, nil); got != ctx {
			t.Errorf("a nil sink made a new context")
		}
	})
}

func TestASinkGivenTwiceIsAddedOnce(t *testing.T) {
	a, b := &namedSink{name: "a"}, &namedSink{name: "b"}
	clients := &namedSink{name: "client"}
	cases := []struct {
		name string
		ctx  context.Context
		cfg  *ExecConfig
		want []string
	}{
		{"the same sink twice on the context", WithSink(WithSink(context.Background(), a), a), nil, []string{"a"}},
		{"the same sink three times on the context", WithSink(WithSink(WithSink(context.Background(), a), a), a), nil, []string{"a"}},
		{"the context's sink is also the client's", WithSink(context.Background(), clients), &ExecConfig{Sink: clients}, []string{"client"}},
		{"it is given at the place of its nearest appearance", WithSink(WithSink(WithSink(context.Background(), a), b), a), nil, []string{"a", "b"}},
		{"a repeat between two others", WithSink(WithSink(WithSink(WithSink(context.Background(), a), b), a), b), nil, []string{"b", "a"}},
		{"the client's own sink is also further out on the context", WithSink(WithSink(context.Background(), a), clients), &ExecConfig{Sink: a}, []string{"client", "a"}},
		{"two sinks that only share a name are two", WithSink(WithSink(context.Background(), &namedSink{name: "x"}), &namedSink{name: "x"}), nil, []string{"x", "x"}},
		{"a sink and the client's with the same name are two", WithSink(context.Background(), &namedSink{name: "client"}), &ExecConfig{Sink: clients}, []string{"client", "client"}},
		{"a sink of a type that cannot be compared, twice, is given twice and does not panic", func() context.Context {
			s := sliceSink{log: []string{"x"}}
			return WithSink(WithSink(context.Background(), s), s)
		}(), nil, []string{"ops.sliceSink", "ops.sliceSink"}},
		{"sinks of different types are different", WithSink(WithSink(context.Background(), a), sliceSink{}), nil, []string{"ops.sliceSink", "a"}},
		{"nothing repeated gives everything", WithSink(WithSink(context.Background(), a), b), &ExecConfig{Sink: clients}, []string{"b", "a", "client"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sinkNames(tc.ctx, tc.cfg); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sinks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasSinkSeesTheContextAndTheClient(t *testing.T) {
	a := &namedSink{name: "a"}
	cases := []struct {
		name string
		ctx  context.Context
		cfg  *ExecConfig
		want bool
	}{
		{"nothing anywhere", context.Background(), nil, false},
		{"a client with no sink", context.Background(), &ExecConfig{}, false},
		{"a client with a sink", context.Background(), &ExecConfig{Sink: a}, true},
		{"a sink on the context", WithSink(context.Background(), a), nil, true},
		{"a sink on the context and a client without one", WithSink(context.Background(), a), &ExecConfig{}, true},
		{"a sink on both", WithSink(context.Background(), a), &ExecConfig{Sink: a}, true},
		{"a nil sink added to the context", WithSink(context.Background(), nil), nil, false},
		{"a nil context with a client's sink", nil, &ExecConfig{Sink: a}, true},
		{"a nil context with a client that has none", nil, &ExecConfig{}, false},
		{"a nil context and no client", nil, nil, false},
		{"a context derived from one with a sink", func() context.Context {
			derived, cancel := context.WithCancel(WithSink(context.Background(), a))
			t.Cleanup(cancel)
			return derived
		}(), nil, true},
		{"a context with other values and no sink", context.WithValue(context.Background(), struct{}{}, 1), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasSink(tc.ctx, tc.cfg); got != tc.want {
				t.Errorf("HasSink = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("it allocates nothing, with a sink or without", func(t *testing.T) {
		with := WithSink(context.Background(), a)
		without := context.Background()
		cfg := &ExecConfig{Sink: a}
		var sink bool
		if allocs := testing.AllocsPerRun(100, func() {
			sink = HasSink(with, nil) && HasSink(without, cfg) && !HasSink(without, nil)
		}); allocs != 0 {
			t.Errorf("HasSink allocated %v times per run", allocs)
		}
		if !sink {
			t.Errorf("HasSink gave the wrong answers while being measured")
		}
	})
}

func TestEachSinkGivesEverySinkItsOutcome(t *testing.T) {
	a, b := &namedSink{name: "a"}, &namedSink{name: "b"}
	ctx := WithSink(WithSink(context.Background(), a), b)
	cfg := &ExecConfig{Sink: &namedSink{name: "client"}}
	EachSink(ctx, cfg, func(s Sink) { s.Add(ctx, &Outcome{}) })
	if !reflect.DeepEqual(a.added, []string{"a:"}) || !reflect.DeepEqual(b.added, []string{"b:"}) {
		t.Errorf("added: a=%v b=%v", a.added, b.added)
	}
	if got := cfg.Sink.(*namedSink).added; !reflect.DeepEqual(got, []string{"client:"}) {
		t.Errorf("the client's sink got %v", got)
	}

	decisions, bodies, max := a.Wants()
	if decisions || bodies || max != 0 {
		t.Errorf("Wants of a plain sink = %v %v %v", decisions, bodies, max)
	}
	wanting := &namedSink{decisions: true, bodies: true, maxBody: 2048}
	if d, b2, m := wanting.Wants(); !d || !b2 || m != 2048 {
		t.Errorf("Wants = %v %v %v", d, b2, m)
	}
}

func TestDecisionObserversOnTheContextComposeNearestFirst(t *testing.T) {
	type call struct {
		who    string
		record string
	}
	var calls []call
	observer := func(who string) func(context.Context, DecisionRecord) {
		return func(_ context.Context, r DecisionRecord) {
			calls = append(calls, call{who, r.Operation})
		}
	}
	record := DecisionRecord{Operation: "which"}
	run := func(ctx context.Context) []call {
		calls = nil
		if fn := decisionObserverFrom(ctx); fn != nil {
			fn(ctx, record)
		}
		return calls
	}

	cases := []struct {
		name string
		ctx  context.Context
		want []call
	}{
		{"no observer gives nil", context.Background(), nil},
		{"one observer is told", WithDecisionObserver(context.Background(), observer("a")), []call{{"a", "which"}}},
		{"two are told, the nearest first", WithDecisionObserver(WithDecisionObserver(context.Background(), observer("a")), observer("b")), []call{{"b", "which"}, {"a", "which"}}},
		{"three are told, the nearest first", WithDecisionObserver(WithDecisionObserver(WithDecisionObserver(context.Background(), observer("a")), observer("b")), observer("c")), []call{{"c", "which"}, {"b", "which"}, {"a", "which"}}},
		{"a nil observer is not added", WithDecisionObserver(WithDecisionObserver(context.Background(), observer("a")), nil), []call{{"a", "which"}}},
		{"a nil observer alone leaves none", WithDecisionObserver(context.Background(), nil), nil},
		{"a derived context keeps its parent's", func() context.Context {
			derived, cancel := context.WithCancel(WithDecisionObserver(context.Background(), observer("a")))
			t.Cleanup(cancel)
			return derived
		}(), []call{{"a", "which"}}},
		{"one added under a derived context comes first", func() context.Context {
			derived, cancel := context.WithCancel(WithDecisionObserver(context.Background(), observer("a")))
			t.Cleanup(cancel)
			return WithDecisionObserver(derived, observer("b"))
		}(), []call{{"b", "which"}, {"a", "which"}}},
		{"a nil context has none", nil, nil},
		{"a context with other values has none", context.WithValue(context.Background(), struct{}{}, 1), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.ctx); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("calls = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("the observer is given the context and the record it is told of", func(t *testing.T) {
		type ctxKey struct{}
		var seenCtx context.Context
		var seenRecord DecisionRecord
		ctx := WithDecisionObserver(context.Background(), func(c context.Context, r DecisionRecord) {
			seenCtx, seenRecord = c, r
		})
		told := context.WithValue(ctx, ctxKey{}, "marker")
		decisionObserverFrom(ctx)(told, DecisionRecord{Operation: "rate", Asked: []AnsweredQuestion{{Question: "q"}}})
		if seenCtx != told || seenRecord.Operation != "rate" || len(seenRecord.Asked) != 1 {
			t.Errorf("the observer was given %v and %+v", seenCtx, seenRecord)
		}
	})
	t.Run("a sibling context does not have the observer", func(t *testing.T) {
		base := context.Background()
		_ = WithDecisionObserver(base, observer("a"))
		if decisionObserverFrom(base) != nil {
			t.Errorf("the base context gained an observer")
		}
	})
	t.Run("a nil context panics when one is added, as context.WithValue does", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Errorf("WithDecisionObserver(nil, fn) did not panic")
			}
		}()
		_ = WithDecisionObserver(nil, observer("a"))
	})
}

func TestParentResultIDsTravelOnTheContext(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"none set gives none", context.Background(), nil},
		{"one id", WithParentResultIDs(context.Background(), "r1"), []string{"r1"}},
		{"several ids in the order given", WithParentResultIDs(context.Background(), "r1", "r2", "r3"), []string{"r1", "r2", "r3"}},
		{"a repeated id is kept", WithParentResultIDs(context.Background(), "r1", "r1"), []string{"r1", "r1"}},
		{"the nearest setting replaces the outer one", WithParentResultIDs(WithParentResultIDs(context.Background(), "outer"), "inner1", "inner2"), []string{"inner1", "inner2"}},
		{"an empty setting clears the outer one", WithParentResultIDs(WithParentResultIDs(context.Background(), "outer")), nil},
		{"setting none on a bare context leaves none", WithParentResultIDs(context.Background()), nil},
		{"a derived context keeps them", func() context.Context {
			derived, cancel := context.WithCancel(WithParentResultIDs(context.Background(), "r1"))
			t.Cleanup(cancel)
			return derived
		}(), []string{"r1"}},
		{"a nil context has none", nil, nil},
		{"a context with other values has none", context.WithValue(context.Background(), struct{}{}, 1), nil},
		{"an empty id is kept as given", WithParentResultIDs(context.Background(), ""), []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParentResultIDs(tc.ctx); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParentResultIDs = %#v, want %#v", got, tc.want)
			}
		})
	}

	t.Run("changing the slice that was passed in does not change the context", func(t *testing.T) {
		ids := []string{"r1", "r2"}
		ctx := WithParentResultIDs(context.Background(), ids...)
		ids[0] = "changed"
		if got := ParentResultIDs(ctx); !reflect.DeepEqual(got, []string{"r1", "r2"}) {
			t.Errorf("ParentResultIDs = %v", got)
		}
	})
	t.Run("changing the slice that was read does not change the context", func(t *testing.T) {
		ctx := WithParentResultIDs(context.Background(), "r1", "r2")
		got := ParentResultIDs(ctx)
		got[0] = "changed"
		if again := ParentResultIDs(ctx); !reflect.DeepEqual(again, []string{"r1", "r2"}) {
			t.Errorf("ParentResultIDs = %v", again)
		}
	})
	t.Run("a sibling context does not have them", func(t *testing.T) {
		base := context.Background()
		_ = WithParentResultIDs(base, "r1")
		if got := ParentResultIDs(base); got != nil {
			t.Errorf("the base context gained %v", got)
		}
	})
	t.Run("a nil context panics when ids are set, as context.WithValue does", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Errorf("WithParentResultIDs(nil, ...) did not panic")
			}
		}()
		_ = WithParentResultIDs(nil, "r1")
	})
}

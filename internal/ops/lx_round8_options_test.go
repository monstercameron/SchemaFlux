package ops

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

type round8CtxKey struct{}

// applyTimeout gave every context it derived a goroutine that waited on it
// until the deadline, and derived one whether or not it shortened anything. A
// call with a five-minute timeout left about six of them parked for the five
// minutes. It now derives a context only when that makes the deadline earlier,
// and nothing waits on the one it derives.
func TestApplyTimeout(t *testing.T) {
	near, cancelNear := context.WithTimeout(context.Background(), time.Minute)
	defer cancelNear()
	far, cancelFar := context.WithTimeout(context.Background(), time.Hour)
	defer cancelFar()
	done, cancelDone := context.WithCancel(context.Background())
	cancelDone()
	valued := context.WithValue(context.Background(), round8CtxKey{}, "kept")

	cases := []struct {
		name    string
		ctx     context.Context
		timeout time.Duration
		same    bool          // the context comes back as it went in
		within  time.Duration // otherwise, the deadline is at most this far off
	}{
		{"no timeout", context.Background(), 0, true, 0},
		{"a negative timeout", context.Background(), -time.Second, true, 0},
		{"no deadline yet", context.Background(), 10 * time.Minute, false, 10 * time.Minute},
		{"an earlier deadline already there", near, 10 * time.Minute, true, 0},
		{"a later deadline already there", far, 10 * time.Minute, false, 10 * time.Minute},
		{"a much shorter timeout", near, time.Second, false, time.Second},
		{"a context that is already over", done, time.Minute, false, time.Minute},
		{"a context with a value", valued, time.Minute, false, time.Minute},
		{"no timeout on a context with a deadline", near, 0, true, 0},
		{"a tiny timeout", context.Background(), time.Nanosecond, false, time.Nanosecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			got := applyTimeout(tc.ctx, tc.timeout)
			if tc.same {
				if got != tc.ctx {
					t.Fatal("a new context was derived where the old one would do")
				}
				return
			}
			if got == tc.ctx {
				t.Fatal("the context came back as it went in, with no earlier deadline")
			}
			deadline, ok := got.Deadline()
			if !ok {
				t.Fatal("the derived context has no deadline")
			}
			if latest := before.Add(tc.within).Add(time.Second); deadline.After(latest) {
				t.Errorf("the deadline is %v away, want at most %v", time.Until(deadline), tc.within)
			}
			if got.Value(round8CtxKey{}) != tc.ctx.Value(round8CtxKey{}) {
				t.Error("a value on the parent was lost")
			}
			if tc.ctx.Err() != nil && got.Err() == nil {
				t.Error("a context derived from one that is over is not over")
			}
		})
	}

	t.Run("the deadline arrives", func(t *testing.T) {
		got := applyTimeout(context.Background(), 20*time.Millisecond)
		select {
		case <-got.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("the derived context never ended")
		}
		if !errors.Is(got.Err(), context.DeadlineExceeded) {
			t.Errorf("Err = %v, want DeadlineExceeded", got.Err())
		}
	})

	t.Run("the parent's cancellation arrives", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		got := applyTimeout(parent, time.Hour)
		cancel()
		select {
		case <-got.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("the derived context outlived its parent")
		}
	})

	t.Run("nothing waits on it", func(t *testing.T) {
		before := runtime.NumGoroutine()
		kept := make([]context.Context, 0, 500)
		for range 500 {
			kept = append(kept, applyTimeout(context.Background(), time.Hour))
		}
		if grew := runtime.NumGoroutine() - before; grew > 5 {
			t.Errorf("%d goroutines were started for 500 timeouts", grew)
		}
		runtime.KeepAlive(kept)
	})
}

// The same through an operation: a call with a timeout leaves no goroutine
// behind it.
func TestACallWithATimeoutLeavesNoGoroutineBehind(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"name":"a","total":1}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	calls := map[string]func() error{
		"Extract": func() error {
			opts := NewExtractOptions()
			opts.CommonOptions.Timeout = time.Hour
			_, err := Extract[round8Record]("a, 1", opts)
			return err
		},
		"ExtractResult": func() error {
			opts := NewExtractOptions()
			opts.CommonOptions.Timeout = time.Hour
			_, err := ExtractResult[round8Record]("a, 1", opts)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			runtime.Gosched()
			before := runtime.NumGoroutine()
			for range 200 {
				_ = call()
			}
			time.Sleep(20 * time.Millisecond)
			if grew := runtime.NumGoroutine() - before; grew > 5 {
				t.Errorf("%d goroutines were left behind by 200 calls with a timeout", grew)
			}
		})
	}
}

// ConvertOpOptions built a CommonOptions from some of the fields it was given
// and dropped the rest: the strictness flags, the output ceiling, web search
// and a declared schema. ExtractBatch converts its options this way, so a
// batch ran without them.
func TestConvertOpOptionsKeepsEveryField(t *testing.T) {
	schema := map[string]any{"type": "object"}
	given := types.OpOptions{
		Steering: "s", Threshold: 0.4, Mode: types.Strict, Intelligence: types.Quick,
		ExactFields: true, CompleteFields: true,
		RequestID: "r", CorrelationID: "c",
		JSONSchema: schema, SchemaName: "record",
		Model: "a-pinned-model", Effort: types.EffortLow,
		MaxOutputTokens: 321, WebSearch: true,
	}
	operations := []string{
		"extract", "transform", "generate", "summarize", "rewrite", "translate", "expand",
		"classify", "score", "compare", "choose", "filter", "sort", "batch",
		"an operation it does not know",
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			converted := ConvertOpOptions(given, operation)
			got := converted.toOpOptions()

			want := given
			// The context is derived and the two are not comparable.
			got.Context, want.Context = nil, nil
			if !reflect.DeepEqual(got, want) {
				t.Errorf("converted and back:\n got %+v\nwant %+v", got, want)
			}
		})
	}

	t.Run("the context is carried", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), round8CtxKey{}, "kept")
		got := ConvertOpOptions(types.OpOptions{Context: ctx}, "extract").toOpOptions()
		if got.Context == nil || got.Context.Value(round8CtxKey{}) != "kept" {
			t.Error("the context was not carried")
		}
	})
}

// More of checkLockedLimits, which had six cases: the mode floor, and each
// limit from either half.
func TestLockedLimitsOnTheModeAndTheCeiling(t *testing.T) {
	ceiling := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 100})
	floor := WithLockedLimits(context.Background(), LockedLimits{MinMode: types.Strict})
	both := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 100, MinMode: types.Strict})

	cases := []struct {
		name    string
		set     func(*ExtractOptions)
		wantErr bool
	}{
		{"a mode floor, met on CommonOptions", func(o *ExtractOptions) {
			o.CommonOptions.Context = floor
			o.CommonOptions.Mode = types.Strict
		}, false},
		{"a mode floor, weakened on CommonOptions", func(o *ExtractOptions) {
			o.CommonOptions.Context = floor
			o.CommonOptions.Mode = types.Creative
		}, true},
		{"a mode floor, weakened on the embedded half", func(o *ExtractOptions) {
			o.OpOptions.Context = floor
			o.CommonOptions.Mode = types.ModeUnset
			o.OpOptions.Mode = types.Creative
		}, true},
		{"a mode floor and no mode set", func(o *ExtractOptions) {
			o.CommonOptions.Context = floor
			o.CommonOptions.Mode = types.ModeUnset
		}, false},
		{"a ceiling met exactly", func(o *ExtractOptions) {
			o.CommonOptions.Context = ceiling
			o.OpOptions.MaxOutputTokens = 100
		}, false},
		{"a ceiling exceeded by one", func(o *ExtractOptions) {
			o.CommonOptions.Context = ceiling
			o.OpOptions.MaxOutputTokens = 101
		}, true},
		{"both limits, both met", func(o *ExtractOptions) {
			o.CommonOptions.Context = both
			o.CommonOptions.Mode = types.Strict
			o.OpOptions.MaxOutputTokens = 10
		}, false},
		{"both limits, the ceiling broken", func(o *ExtractOptions) {
			o.CommonOptions.Context = both
			o.CommonOptions.Mode = types.Strict
			o.OpOptions.MaxOutputTokens = 1000
		}, true},
		{"both limits, the floor broken", func(o *ExtractOptions) {
			o.CommonOptions.Context = both
			o.CommonOptions.Mode = types.Creative
			o.OpOptions.MaxOutputTokens = 10
		}, true},
		{"a lock with a timeout set", func(o *ExtractOptions) {
			o.CommonOptions.Context = ceiling
			o.CommonOptions.Timeout = time.Hour
			o.OpOptions.MaxOutputTokens = 500
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewExtractOptions()
			tc.set(&opts)
			if err := checkLockedLimits(opts); (err != nil) != tc.wantErr {
				t.Fatalf("checkLockedLimits = %v, want an error: %v", err, tc.wantErr)
			}
			// The same through Validate, which is where a caller meets it.
			if err := opts.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, want an error: %v", err, tc.wantErr)
			}
		})
	}
}

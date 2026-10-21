package pricing_test

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/pricing"
)

// The numbers a caller can pass to this package are token counts, a count of
// entries, rates and ceilings in dollars, and times. Each is tried at both ends of
// its range, and none may panic, wrap round, or be the size of anything the
// package allocates.

var tokenCounts = []int{math.MaxInt, math.MaxInt - 1, math.MaxInt/2 + 1, 1 << 40, 1_000_000, 1, 0, -1, -1_000_000, math.MinInt + 1, math.MinInt}

func TestPriceOfAnyTokenCountIsAFiniteCostThatIsNotNegativeAndNeverPanics(t *testing.T) {
	name := uniqueModel(t)
	if _, err := pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 1000, Output: 1000, Cached: 100, Reasoning: 500}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{name, uniqueModel(t) + "-unpriced"} {
		for _, n := range tokenCounts {
			for _, field := range []string{"Input", "Output", "Cached", "Reasoning", "CacheWrite", "Total", "all"} {
				t.Run(fmt.Sprintf("%s/%s=%d", model[len(model)-8:], field, n), func(t *testing.T) {
					u := schemaflux.Usage{}
					switch field {
					case "Input":
						u.Input = n
					case "Output":
						u.Output = n
					case "Cached":
						u.Cached = n
					case "Reasoning":
						u.Reasoning = n
					case "CacheWrite":
						u.CacheWrite = n
					case "Total":
						u.Total = n
					case "all":
						u = schemaflux.Usage{Input: n, Output: n, Cached: n, Reasoning: n, CacheWrite: n, Total: n}
					}
					var cost schemaflux.Cost
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Fatalf("Price panicked: %v", r)
							}
						}()
						cost = pricing.Price(u, model, "acme")
					}()
					for label, v := range map[string]float64{"Total": cost.Total, "Input": cost.Input, "Output": cost.Output, "Cached": cost.Cached, "Reasoning": cost.Reasoning} {
						if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
							t.Errorf("cost %s = %v for %+v", label, v, u)
						}
					}
				})
			}
		}
	}
}

func TestPriceOfTheLargestCountsDoesNotWrapTheTokensRoundToANegativeFigure(t *testing.T) {
	name := uniqueModel(t)
	if _, err := pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 1, Output: 1}); err != nil {
		t.Fatal(err)
	}
	huge := pricing.Price(schemaflux.Usage{Input: math.MaxInt, Output: math.MaxInt}, name, "acme")
	small := pricing.Price(schemaflux.Usage{Input: 1_000_000, Output: 1_000_000}, name, "acme")
	if !huge.Priced || huge.Total <= small.Total {
		t.Errorf("a count of %d each cost %v, which is not more than the %v of a million each", math.MaxInt, huge.Total, small.Total)
	}
}

func TestSummarizeAddsCountsThatPassTheLargestIntAsTheLargestIntAndNotAsANegativeNumber(t *testing.T) {
	isolateLedger(t)
	for i := range 3 {
		seed(call{id: fmt.Sprint("big-", i), cost: 1, usage: schemaflux.Usage{
			Input: math.MaxInt, Output: math.MaxInt - 1, Total: math.MaxInt, Cached: math.MaxInt, CacheWrite: math.MaxInt, Reasoning: math.MaxInt,
		}})
	}
	s := pricing.Summarize(time.Time{}, nil)
	u := s.Usage
	for label, got := range map[string]int{"Input": u.Input, "Output": u.Output, "Total": u.Total, "Cached": u.Cached, "CacheWrite": u.CacheWrite, "Reasoning": u.Reasoning} {
		if got < 0 {
			t.Errorf("Usage.%s of the summary is %d: the sum wrapped round", label, got)
		}
	}
	if u.Input != math.MaxInt || u.Total != math.MaxInt {
		t.Errorf("Usage = %+v, want the counts held at the largest int", u)
	}
	for i := range 3 {
		seed(call{id: fmt.Sprint("small-", i), cost: 1, usage: schemaflux.Usage{Input: math.MinInt}})
	}
	if got := pricing.Summarize(time.Time{}, map[string]string{"request_id": "small-0"}).Usage.Input; got != math.MinInt {
		t.Errorf("one entry of the smallest count summed to %d", got)
	}
}

func TestSetHistoryLimitTakesAnyNumberAndKeepsRoomForTheEntriesThatArriveAndNotForTheNumber(t *testing.T) {
	isolateLedger(t)
	for _, n := range []int{math.MaxInt, math.MaxInt - 1, 1 << 40, 1 << 20, 9, 3, 1, 0, -1, math.MinInt} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var restore func()
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("SetHistoryLimit(%d) panicked: %v", n, r)
					}
				}()
				restore = pricing.SetHistoryLimit(n)
				for i := range 10 {
					seed(call{id: fmt.Sprint("e", i), cost: 1})
				}
			}()
			runtime.ReadMemStats(&after)
			if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
				t.Errorf("SetHistoryLimit(%d) and ten calls allocated %d bytes", n, grown)
			}
			want := 10
			if n >= 1 && n < 10 {
				want = n
			}
			if got := len(pricing.Entries(time.Time{}, nil)); got != want {
				t.Errorf("with a limit of %d the ledger kept %d of ten entries, want %d", n, got, want)
			}
			restore()
			pricing.Reset()
		})
	}
}

func TestRegisterRefusesANameOrAProviderLongerThanAnyRealOneAndQuotesNeither(t *testing.T) {
	cases := []struct {
		name  string
		model func(unique string) pricing.Model
		ok    bool
	}{
		{"a name of 128 bytes", func(u string) pricing.Model {
			return pricing.Model{Provider: "acme", Name: u + strings.Repeat("a", 128-len(u)), Input: 1}
		}, true},
		{"a name of 129 bytes", func(u string) pricing.Model {
			return pricing.Model{Provider: "acme", Name: u + strings.Repeat("a", 129-len(u)), Input: 1}
		}, false},
		{"a name of 100,000 characters", func(u string) pricing.Model {
			return pricing.Model{Provider: "acme", Name: strings.Repeat("a", 100_000), Input: 1}
		}, false},
		{"a name of a million multi-byte characters", func(u string) pricing.Model {
			return pricing.Model{Provider: "acme", Name: strings.Repeat("é", 1_000_000), Input: 1}
		}, false},
		{"a provider of 128 bytes", func(u string) pricing.Model {
			return pricing.Model{Provider: strings.Repeat("p", 128), Name: u, Input: 1}
		}, true},
		{"a provider of 129 bytes", func(u string) pricing.Model {
			return pricing.Model{Provider: strings.Repeat("p", 129), Name: u, Input: 1}
		}, false},
		{"a name of spaces around 128 bytes", func(u string) pricing.Model {
			return pricing.Model{Provider: "acme", Name: "   " + u + strings.Repeat("a", 128-len(u)) + "   ", Input: 1}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unique := uniqueModel(t)
			m := tc.model(unique)
			restore, err := pricing.Register(m)
			if tc.ok {
				if err != nil {
					t.Fatalf("Register: %v", err)
				}
				restore()
				return
			}
			var e *schemaflux.Error
			if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || e.Op != "pricing.register" || restore != nil {
				t.Fatalf("Register = %v, restore %v; want an *Error of kind configuration and no restore", err, restore != nil)
			}
			if len(e.Error()) > 300 || strings.Contains(e.Error(), strings.Repeat("a", 20)) || strings.Contains(e.Error(), strings.Repeat("p", 20)) {
				t.Errorf("the error carries the name or provider that was given: %.80q (%d bytes)", e.Error(), len(e.Error()))
			}
		})
	}
}

func TestRegisterRefusesEveryRateThatIsNotARateAndNamesTheRateInGrammaticalWords(t *testing.T) {
	rates := []struct {
		field    string
		article  string
		set      func(*pricing.Model, float64)
		wantPlus bool // true when zero is allowed for this rate
	}{
		{"input", "an", func(m *pricing.Model, v float64) { m.Input = v }, true},
		{"output", "an", func(m *pricing.Model, v float64) { m.Output = v }, true},
		{"cached", "a", func(m *pricing.Model, v float64) { m.Cached = v }, true},
		{"reasoning", "a", func(m *pricing.Model, v float64) { m.Reasoning = v }, true},
	}
	bad := []struct {
		label string
		value float64
		words string
	}{
		{"NaN", math.NaN(), "rate that is not a number"},
		{"+Inf", math.Inf(1), "rate above $1,000 per million tokens"},
		{"-Inf", math.Inf(-1), "negative"},
		{"-1", -1, "negative"},
		{"-the smallest number above zero", -math.SmallestNonzeroFloat64, "negative"},
		{"the largest number", math.MaxFloat64, "rate above $1,000 per million tokens"},
		{"one thousandth over the ceiling", 1000.001, "rate above $1,000 per million tokens"},
	}
	for _, rate := range rates {
		for _, b := range bad {
			t.Run(rate.field+" "+b.label, func(t *testing.T) {
				m := pricing.Model{Provider: "acme", Name: uniqueModel(t), Input: 1, Output: 1}
				rate.set(&m, b.value)
				restore, err := pricing.Register(m)
				var e *schemaflux.Error
				if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || restore != nil {
					t.Fatalf("Register = %v, restore %v", err, restore != nil)
				}
				if !strings.Contains(e.Message, b.words) {
					t.Errorf("message %q does not say %q", e.Message, b.words)
				}
				for _, ungrammatical := range []string{"an cached", "an reasoning", "a input", "a output", "an negative", "a negative a", "a negative an"} {
					if strings.Contains(e.Message, ungrammatical) {
						t.Errorf("message %q has %q", e.Message, ungrammatical)
					}
				}
				if !strings.Contains(e.Message, rate.field) {
					t.Errorf("message %q does not name the %s rate", e.Message, rate.field)
				}
				if b.label == "NaN" && !strings.Contains(e.Message, rate.article+" "+rate.field+" rate") {
					t.Errorf("message %q does not say %q", e.Message, rate.article+" "+rate.field+" rate")
				}
			})
		}
	}
}

func TestRegisterAcceptsTheRatesAtTheEdgesOfTheRange(t *testing.T) {
	for _, rate := range []float64{1000, 0.000001, 1e-300, 1} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			restore, err := pricing.Register(pricing.Model{Provider: "acme", Name: uniqueModel(t), Input: rate, Output: rate, Cached: rate, Reasoning: rate})
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			restore()
		})
	}
}

func TestRegisterRefusesARateSoSmallThatItIsAPriceOfNothing(t *testing.T) {
	for _, rate := range []float64{math.SmallestNonzeroFloat64, 1e-322} {
		restore, err := pricing.Register(pricing.Model{Provider: "acme", Name: uniqueModel(t), Input: rate, Output: rate})
		var e *schemaflux.Error
		if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || restore != nil {
			t.Errorf("Register with rates of %v = %v, restore %v; want a refusal", rate, err, restore != nil)
		}
	}
}

func TestTheReadsTakeAnyTimeAndNeverPanic(t *testing.T) {
	isolateLedger(t)
	seed(call{id: "now", cost: 1})
	times := []time.Time{{}, time.Unix(0, 0), time.Unix(math.MaxInt64, 0), time.Unix(math.MinInt64, 0), time.Now().Add(time.Duration(math.MaxInt64)), time.Now().Add(time.Duration(math.MinInt64)), time.Now().Add(-time.Hour)}
	for _, since := range times {
		t.Run(since.Format(time.RFC3339), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("a read panicked: %v", r)
				}
			}()
			_, _ = pricing.Spent(since, nil)
			_ = pricing.Entries(since, nil)
			_ = pricing.Summarize(since, nil)
			_ = pricing.Breakdown(since)
			for _, format := range []string{"csv", "json"} {
				if _, err := pricing.Export(since, format); err != nil {
					t.Errorf("Export(%q): %v", format, err)
				}
			}
		})
	}
}

func TestEverySetLimitsCeilingAtTheEdgesOfTheRangeIsAcceptedOrRefusedAndNeverPanics(t *testing.T) {
	isolateLedger(t)
	for _, v := range []float64{0, math.SmallestNonzeroFloat64, 1, math.MaxFloat64, math.Inf(1), math.Inf(-1), math.NaN(), -1, -math.MaxFloat64, math.Copysign(0, -1)} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("SetLimits panicked: %v", r)
				}
			}()
			restore, err := pricing.SetLimits(pricing.Limits{Daily: v, Weekly: v, Monthly: v, Enforce: true})
			bad := math.IsNaN(v) || math.IsInf(v, 0) || v < 0
			switch {
			case bad && (err == nil || restore != nil):
				t.Errorf("SetLimits(%v) = %v, restore %v; want a refusal", v, err, restore != nil)
			case !bad && (err != nil || restore == nil):
				t.Errorf("SetLimits(%v) = %v; want it accepted", v, err)
			}
			if restore != nil {
				spend("x", 1)
				_ = checkBudget()
				restore()
			}
		})
	}
}

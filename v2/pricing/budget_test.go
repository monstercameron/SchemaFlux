package pricing_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	inner "github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/pricing"
)

// These tests change the process's limits. Each calls isolateLedger, which
// restores them, and none runs in parallel.

// alert is one call of an OnThreshold function.
type alert struct {
	spent, limit float64
	period       string
}

// alerts collects the calls of an OnThreshold function.
type alerts struct{ got []alert }

func (a *alerts) record(spent, limit float64, period string) {
	a.got = append(a.got, alert{spent, limit, period})
}

func (a *alerts) periods() string {
	var names []string
	for _, x := range a.got {
		names = append(names, x.period)
	}
	return strings.Join(names, ",")
}

// spend puts a priced call of that many dollars in the ledger, now.
func spend(id string, usd float64) { seed(call{id: id, cost: usd}) }

func TestSetLimitsReportsTheThresholdsOfEachPeriod(t *testing.T) {
	isolateLedger(t)

	cases := []struct {
		name   string
		limits func(a *alerts) pricing.Limits
		spend  []float64
		want   []alert
	}{
		{"nothing is reported below eighty percent", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 10, OnThreshold: a.record}
		}, []float64{7.9}, nil},
		{"eighty percent is reported with the spend and the limit", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 10, OnThreshold: a.record}
		}, []float64{8}, []alert{{8, 10, "daily"}}},
		{"the limit is reported after eighty percent", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 10, OnThreshold: a.record}
		}, []float64{8, 2}, []alert{{8, 10, "daily"}, {10, 10, "daily"}}},
		{"one call that crosses both reports both, eighty first", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 10, OnThreshold: a.record}
		}, []float64{12}, []alert{{12, 10, "daily"}, {12, 10, "daily"}}},
		{"a threshold is reported once", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 10, OnThreshold: a.record}
		}, []float64{8, 1, 1, 1, 1}, []alert{{8, 10, "daily"}, {10, 10, "daily"}}},
		{"the weekly limit is named weekly", func(a *alerts) pricing.Limits {
			return pricing.Limits{Weekly: 10, OnThreshold: a.record}
		}, []float64{9}, []alert{{9, 10, "weekly"}}},
		{"the monthly limit is named monthly", func(a *alerts) pricing.Limits {
			return pricing.Limits{Monthly: 10, OnThreshold: a.record}
		}, []float64{10}, []alert{{10, 10, "monthly"}, {10, 10, "monthly"}}},
		{"a period without a limit is not reported", func(a *alerts) pricing.Limits {
			return pricing.Limits{Weekly: 10, OnThreshold: a.record}
		}, []float64{50}, []alert{{50, 10, "weekly"}, {50, 10, "weekly"}}},
		{"a limit of zero is no limit", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 0, OnThreshold: a.record}
		}, []float64{50}, nil},
		{"a limit of zero is no limit", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 0, OnThreshold: a.record}
		}, []float64{50}, nil},
		{"the zero Limits has no limit", func(a *alerts) pricing.Limits { return pricing.Limits{} }, []float64{50}, nil},
		{"spend that is not priced is not counted", func(a *alerts) pricing.Limits {
			return pricing.Limits{Daily: 10, OnThreshold: a.record}
		}, []float64{-1}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pricing.Reset()
			var a alerts
			restore := setLimits(t, c.limits(&a))
			defer restore()
			for i, usd := range c.spend {
				if usd < 0 {
					seed(call{id: fmt.Sprint("u", i), unpriced: true})
					continue
				}
				spend(fmt.Sprint("s", i), usd)
			}
			if len(a.got) != len(c.want) {
				t.Fatalf("alerts = %v, want %v", a.got, c.want)
			}
			for i := range c.want {
				if !near(a.got[i].spent, c.want[i].spent) || !near(a.got[i].limit, c.want[i].limit) || a.got[i].period != c.want[i].period {
					t.Fatalf("alerts = %v, want %v", a.got, c.want)
				}
			}
		})
	}

	t.Run("every period is reported under its own name, in the order of the thresholds", func(t *testing.T) {
		pricing.Reset()
		var a alerts
		restore := setLimits(t, pricing.Limits{Daily: 1, Weekly: 1, Monthly: 1, OnThreshold: a.record})
		defer restore()
		spend("big", 1)
		if got := a.periods(); got != "daily,daily,weekly,weekly,monthly,monthly" {
			t.Fatalf("alerts for %s", got)
		}
	})
	t.Run("the limits are independent", func(t *testing.T) {
		pricing.Reset()
		var a alerts
		restore := setLimits(t, pricing.Limits{Daily: 100, Weekly: 1, Monthly: 100, OnThreshold: a.record})
		defer restore()
		spend("x", 1)
		if got := a.periods(); got != "weekly,weekly" {
			t.Fatalf("alerts for %s", got)
		}
	})
	t.Run("a function that is nil is fine", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1})
		defer restore()
		spend("x", 5)
	})
	t.Run("the function is called after the entry is in the ledger", func(t *testing.T) {
		pricing.Reset()
		var seen float64
		restore := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: func(float64, float64, string) {
			seen, _ = pricing.Spent(time.Time{}, nil)
		}})
		defer restore()
		spend("x", 2)
		if !near(seen, 2) {
			t.Fatalf("the function saw %v in the ledger", seen)
		}
	})
	t.Run("the function may call the package", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: func(float64, float64, string) {
			_ = pricing.Entries(time.Time{}, nil)
			_ = pricing.Summarize(time.Time{}, nil)
			seed(call{id: "from the callback"})
		}})
		defer restore()
		spend("x", 2)
		if _, ok := pricing.ForRequest("from the callback"); !ok {
			t.Fatal("an entry recorded by the function is missing")
		}
	})
	t.Run("a panic in the function does not reach the call", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: func(float64, float64, string) { panic("the caller's code") }})
		defer restore()
		spend("x", 2)
		if _, ok := pricing.ForRequest("x"); !ok {
			t.Fatal("the entry was lost")
		}
	})
}

func TestSetLimitsEnforcesOnlyWhenAskedTo(t *testing.T) {
	isolateLedger(t)

	t.Run("a limit reached with no Enforce refuses nothing", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1})
		defer restore()
		spend("x", 5)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a limit reached with Enforce refuses", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		defer restore()
		spend("x", 1)
		if err := checkBudget(); !errors.Is(err, inner.ErrBudgetExceeded) {
			t.Fatalf("err = %v, want the budget error", err)
		}
	})
	t.Run("a limit not yet reached lets the call go", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		defer restore()
		spend("x", 0.99)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("each period refuses on its own limit", func(t *testing.T) {
		for _, limits := range []pricing.Limits{
			{Daily: 1, Enforce: true},
			{Weekly: 1, Enforce: true},
			{Monthly: 1, Enforce: true},
		} {
			pricing.Reset()
			restore := setLimits(t, limits)
			spend("x", 1)
			if err := checkBudget(); err == nil {
				t.Errorf("limits %+v did not refuse", limits)
			}
			restore()
		}
	})
	t.Run("the refusal names the period and says when the figure is a floor", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		defer restore()
		spend("x", 2)
		seed(call{id: "unpriced", unpriced: true})
		err := checkBudget()
		if err == nil || !strings.Contains(err.Error(), "daily") || !strings.Contains(err.Error(), "unknown cost") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("spend that is not priced never refuses", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 0.01, Enforce: true})
		defer restore()
		for i := range 20 {
			seed(call{id: fmt.Sprint("u", i), unpriced: true})
		}
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a limit of zero refuses nothing even with Enforce", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 0, Weekly: 0, Monthly: 0, Enforce: true})
		defer restore()
		spend("x", 1000)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the largest limit there is is a limit nothing reaches", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: math.MaxFloat64, Weekly: math.MaxFloat64, Monthly: math.MaxFloat64, Enforce: true})
		defer restore()
		spend("x", 1000)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the smallest limit there is refuses once anything is spent", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: math.SmallestNonzeroFloat64, Enforce: true})
		defer restore()
		spend("x", 0.01)
		if err := checkBudget(); err == nil {
			t.Fatal("a limit of the smallest positive number did not refuse")
		}
	})
	t.Run("Enforce without a limit refuses nothing", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Enforce: true})
		defer restore()
		spend("x", 1000)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no limits set refuses nothing", func(t *testing.T) {
		pricing.Reset()
		spend("x", 1000)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSetLimitsReturnsTheFunctionThatPutsBackWhatWasThere(t *testing.T) {
	isolateLedger(t)

	t.Run("with nothing set before, restore leaves no limits", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		restore()
		spend("x", 100)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("restore stops the alerts of the limits it replaced", func(t *testing.T) {
		pricing.Reset()
		var a alerts
		restore := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: a.record})
		restore()
		spend("x", 100)
		if len(a.got) != 0 {
			t.Fatalf("alerts = %v", a.got)
		}
	})
	t.Run("restore puts back the earlier limits and their function", func(t *testing.T) {
		pricing.Reset()
		var outer, inside alerts
		restoreOuter := setLimits(t, pricing.Limits{Daily: 1, Enforce: true, OnThreshold: outer.record})
		defer restoreOuter()
		restoreInner := setLimits(t, pricing.Limits{Daily: 1000, OnThreshold: inside.record})
		spend("a", 5)
		if checkBudget() != nil || len(inside.got) != 0 {
			t.Fatal("the inner limits were not in force")
		}
		restoreInner()
		spend("b", 1)
		if checkBudget() == nil {
			t.Fatal("the outer limit is not enforced again")
		}
		if len(outer.got) != 2 {
			t.Fatalf("the outer function was called %d times, want 2", len(outer.got))
		}
	})
	t.Run("restores called in reverse order unwind completely", func(t *testing.T) {
		pricing.Reset()
		first := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		second := setLimits(t, pricing.Limits{Weekly: 1, Enforce: true})
		third := setLimits(t, pricing.Limits{Monthly: 1})
		third()
		second()
		first()
		spend("x", 100)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a second call of the same restore does nothing", func(t *testing.T) {
		pricing.Reset()
		var a alerts
		restore := setLimits(t, pricing.Limits{Daily: 1})
		restore()
		later := setLimits(t, pricing.Limits{Daily: 1, Enforce: true, OnThreshold: a.record})
		defer later()
		restore()
		spend("x", 5)
		if checkBudget() == nil || len(a.got) == 0 {
			t.Fatal("a stale restore removed the limits set after it")
		}
	})
	t.Run("restore starts the alerts of the limits it puts back afresh", func(t *testing.T) {
		pricing.Reset()
		var a alerts
		restoreOuter := setLimits(t, pricing.Limits{Daily: 10, OnThreshold: a.record})
		defer restoreOuter()
		spend("a", 9)
		restoreInner := setLimits(t, pricing.Limits{})
		restoreInner()
		spend("b", 0.01)
		if len(a.got) != 2 {
			t.Fatalf("alerts = %v, want the eighty percent one again", a.got)
		}
	})
	t.Run("restore turns Enforce back off when it was off", func(t *testing.T) {
		pricing.Reset()
		restoreOuter := setLimits(t, pricing.Limits{Daily: 1})
		defer restoreOuter()
		restoreInner := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		restoreInner()
		spend("x", 5)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("restore does not touch the ledger", func(t *testing.T) {
		pricing.Reset()
		restore := setLimits(t, pricing.Limits{Daily: 1})
		spend("x", 5)
		restore()
		if _, ok := pricing.ForRequest("x"); !ok {
			t.Fatal("restore emptied the ledger")
		}
	})
	t.Run("restore is safe to call from several goroutines", func(t *testing.T) {
		restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		done := make(chan struct{})
		for range 8 {
			go func() { restore(); done <- struct{}{} }()
		}
		for range 8 {
			<-done
		}
		pricing.Reset()
		spend("x", 5)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a deferred restore restores even after a panic", func(t *testing.T) {
		func() {
			defer func() { _ = recover() }()
			restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
			defer restore()
			panic("test")
		}()
		pricing.Reset()
		spend("x", 5)
		if err := checkBudget(); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSetLimitsStartsTheAlertsAfresh(t *testing.T) {
	isolateLedger(t)

	t.Run("a threshold already crossed is announced again under new limits", func(t *testing.T) {
		pricing.Reset()
		var first, second alerts
		restoreFirst := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: first.record})
		defer restoreFirst()
		spend("a", 1)
		restoreSecond := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: second.record})
		defer restoreSecond()
		spend("b", 0.01)
		if len(first.got) != 2 || len(second.got) != 2 {
			t.Fatalf("alerts = %v, %v", first.got, second.got)
		}
	})
	t.Run("a raised limit is announced when the spend reaches it", func(t *testing.T) {
		pricing.Reset()
		var a alerts
		restoreFirst := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: a.record})
		defer restoreFirst()
		spend("a", 1)
		a.got = nil
		restoreSecond := setLimits(t, pricing.Limits{Daily: 2, OnThreshold: a.record})
		defer restoreSecond()
		spend("b", 0.7)
		spend("c", 0.3)
		if len(a.got) != 2 || a.got[0].limit != 2 {
			t.Fatalf("alerts = %v", a.got)
		}
	})
	t.Run("the function of the old limits is not called", func(t *testing.T) {
		pricing.Reset()
		var old alerts
		restoreOld := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: old.record})
		defer restoreOld()
		restoreNew := setLimits(t, pricing.Limits{Daily: 1})
		defer restoreNew()
		spend("a", 5)
		if len(old.got) != 0 {
			t.Fatalf("alerts = %v", old.got)
		}
	})
	t.Run("the limits are copied", func(t *testing.T) {
		pricing.Reset()
		limits := pricing.Limits{Daily: 1, Enforce: true}
		restore := setLimits(t, limits)
		defer restore()
		limits.Daily = 1000
		limits.Enforce = false
		spend("a", 5)
		if checkBudget() == nil {
			t.Fatal("changing the value changed the limits in force")
		}
	})
	t.Run("different limits for the day and the month", func(t *testing.T) {
		pricing.Reset()
		var a alerts
		restore := setLimits(t, pricing.Limits{Daily: 100, Monthly: 10, OnThreshold: a.record})
		defer restore()
		spend("a", 8)
		if got := a.periods(); got != "monthly" {
			t.Fatalf("alerts for %q", got)
		}
	})
}

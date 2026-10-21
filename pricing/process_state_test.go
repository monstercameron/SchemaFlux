package pricing_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/setting/settingtest"
	"github.com/monstercameron/schemaflux/v2/pricing"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The tests of this file are about the state the package changes and how it is put
// back: the limits, the history limit and the price table. They run the one table
// of the convention that every restore function of the library is held to.

func TestSetLimitsFollowsTheConventionOfEveryRestore(t *testing.T) {
	isolateLedger(t)
	const huge = 1e6
	settingtest.Convention(t, settingtest.Subject{
		Name: "pricing.SetLimits",
		Set: func(n int) func() {
			return setLimits(t, pricing.Limits{Daily: float64(n * 10), Enforce: true})
		},
		// The limit in force is read from the refusal, which names it.
		Current: func() int {
			pricing.Reset()
			spend("probe", huge)
			err := checkBudget()
			if err == nil {
				return 0
			}
			var limit float64
			if _, scanErr := fmt.Sscanf(err.Error()[strings.Index(err.Error(), "reached the $"):], "reached the $%f limit", &limit); scanErr != nil {
				t.Errorf("the refusal %q does not name a limit: %v", err, scanErr)
			}
			return int(limit) / 10
		},
	})
}

func TestSetHistoryLimitFollowsTheConventionOfEveryRestore(t *testing.T) {
	isolateLedger(t)
	settingtest.Convention(t, settingtest.Subject{
		Name: "pricing.SetHistoryLimit",
		Set:  func(n int) func() { return pricing.SetHistoryLimit(n) },
		// The limit in force is how many of twenty calls the ledger kept; the
		// default is far more than twenty.
		Current: func() int {
			pricing.Reset()
			for i := range 20 {
				spend("history-"+strconv.Itoa(i), 0.01)
			}
			if kept := len(pricing.Entries(time.Time{}, nil)); kept != 20 {
				return kept
			}
			return 0
		},
	})
}

func TestRegisterFollowsTheConventionOfEveryRestoreForAModelNobodyPriced(t *testing.T) {
	name := uniqueModel(t)
	settingtest.Convention(t, registerSubject(t, name, "register of a new model"))
}

func TestRegisterFollowsTheConventionOfEveryRestoreForABuiltInModel(t *testing.T) {
	// The restore of the first call puts back the table's own price for the model,
	// which is what is in force at the start.
	settingtest.Convention(t, registerSubject(t, "gpt-5", "register of a built-in model"))
}

// registerSubject is Register as the table of the convention sees it: the nth
// setting is a price of n dollars per million input tokens, and the setting in
// force is read from what the table prices a million input tokens at, with the
// price the table started with standing for 0.
func registerSubject(t *testing.T, name, label string) settingtest.Subject {
	t.Helper()
	read := func() float64 {
		return pricing.Price(schemaflux.Usage{Input: 1_000_000}, name, "acme").Input
	}
	original := read()
	return settingtest.Subject{
		Name: "pricing.Register, " + label,
		Set: func(n int) func() {
			restore, err := pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: float64(n) + 0.5, Output: 1})
			if err != nil {
				t.Errorf("Register: %v", err)
				return func() {}
			}
			return restore
		},
		Current: func() int {
			got := read()
			if got == original {
				return 0
			}
			n := int(math.Round(got - 0.5))
			if math.Abs(float64(n)+0.5-got) > 1e-6 {
				t.Errorf("the price in force is %v, which no call set", got)
			}
			return n
		},
	}
}

func TestRegisterPutsBackABuiltInPriceAndRemovesAPriceThatWasNeverThere(t *testing.T) {
	before := pricing.Price(schemaflux.Usage{Input: 1_000_000, Output: 1_000_000}, "gpt-5", "openai")
	if !before.Priced {
		t.Fatal("gpt-5 is not priced by the table: the test needs a built-in price")
	}
	restore, err := pricing.Register(pricing.Model{Provider: "acme", Name: "GPT-5", Input: 999, Output: 999})
	if err != nil {
		t.Fatal(err)
	}
	if during := pricing.Price(schemaflux.Usage{Input: 1_000_000}, "gpt-5", "openai"); during.Input != 999 {
		t.Errorf("while registered, the input price is %v", during.Input)
	}
	restore()
	if after := pricing.Price(schemaflux.Usage{Input: 1_000_000, Output: 1_000_000}, "gpt-5", "openai"); after != before {
		t.Errorf("after the restore the cost is %+v, want the built-in %+v", after, before)
	}

	name := uniqueModel(t)
	restore, err = pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 1, Output: 1})
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if got := pricing.Price(schemaflux.Usage{Input: 1000}, name, "acme"); got.Priced {
		t.Errorf("after the restore the model is still priced: %+v", got)
	}
	for _, listed := range pricing.Models() {
		if listed == name {
			t.Errorf("after the restore the model is still listed")
		}
	}
}

func TestSetLimitsKeepsTheLimitsOfALaterCallWhenAnEarlierRestoreRunsFirst(t *testing.T) {
	isolateLedger(t)
	first := setLimits(t, pricing.Limits{Daily: 5, Enforce: true})
	second := setLimits(t, pricing.Limits{Daily: 50, Enforce: true})
	first()
	spend("a", 20)
	if err := checkBudget(); err != nil {
		t.Fatalf("after the first restore the second limits ($50) are not in force: %v", err)
	}
	spend("b", 40)
	if err := checkBudget(); err == nil {
		t.Fatal("after the first restore the second limits are no longer enforced")
	}
	second()
	pricing.Reset()
	spend("c", 1000)
	if err := checkBudget(); err != nil {
		t.Fatalf("after both restores the first limits are back in force: %v", err)
	}
}

// badCeilings are the numbers a ceiling cannot be.
var badCeilings = []float64{-1, -0.01, -math.SmallestNonzeroFloat64, -math.MaxFloat64, math.Inf(-1), math.Inf(1), math.NaN()}

func TestSetLimitsRefusesACeilingThatCannotBeEnforcedAndChangesNothing(t *testing.T) {
	isolateLedger(t)
	inForce := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
	defer inForce()
	fields := []struct {
		name string
		set  func(*pricing.Limits, float64)
	}{
		{"Daily", func(l *pricing.Limits, v float64) { l.Daily = v }},
		{"Weekly", func(l *pricing.Limits, v float64) { l.Weekly = v }},
		{"Monthly", func(l *pricing.Limits, v float64) { l.Monthly = v }},
	}
	for _, field := range fields {
		for _, bad := range badCeilings {
			t.Run(fmt.Sprintf("%s=%v", field.name, bad), func(t *testing.T) {
				limits := pricing.Limits{Enforce: true, Daily: 7, Weekly: 7, Monthly: 7}
				field.set(&limits, bad)
				restore, err := pricing.SetLimits(limits)
				if restore != nil {
					t.Error("a restore was returned beside an error")
				}
				var e *schemaflux.Error
				if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || e.Op != "pricing.setlimits" {
					t.Fatalf("err = %v, want an *Error of kind configuration for pricing.setlimits", err)
				}
				want := "Limits." + field.name + " must be a number that is not negative"
				if !strings.Contains(e.Message, want) {
					t.Errorf("message %q does not say %q", e.Message, want)
				}
				if strings.Contains(e.Error(), fmt.Sprint(bad)) && !math.IsNaN(bad) {
					t.Errorf("the error repeats the value given: %v", e)
				}
				// The limits in force are the ones before: a day's spend of $1 refuses.
				pricing.Reset()
				spend("x", 1)
				if checkBudget() == nil {
					t.Error("a refused SetLimits replaced the limits in force")
				}
			})
		}
	}
}

func TestSetLimitsRefusesTheSameCeilingAClientBudgetRefuses(t *testing.T) {
	// A client's Budget.Limit and the process's Limits are one rule, in one form:
	// the kind and the words, apart from the field named.
	for _, bad := range badCeilings {
		_, err := pricing.SetLimits(pricing.Limits{Daily: bad})
		var e *schemaflux.Error
		if !errors.As(err, &e) {
			t.Fatalf("SetLimits(%v) = %v", bad, err)
		}
		var p schemafluxtest.Provider
		p.Reply(`{}`)
		ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &p, Model: "m", Budget: &schemaflux.Budget{Limit: bad}})
		_, callErr := schemaflux.Extract[digest](ctx, "text")
		var ce *schemaflux.Error
		if !errors.As(callErr, &ce) || ce.Kind != e.Kind {
			t.Fatalf("a client Budget.Limit of %v gave %v, want kind %v", bad, callErr, e.Kind)
		}
		if strings.TrimPrefix(e.Message, "Limits.Daily") != strings.TrimPrefix(ce.Message, "Budget.Limit") {
			t.Errorf("messages differ in more than the field: %q and %q", e.Message, ce.Message)
		}
	}
}

func TestACeilingOfZeroIsNoCeilingAndSaysSo(t *testing.T) {
	isolateLedger(t)
	restore, err := pricing.SetLimits(pricing.Limits{Enforce: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	spend("x", 1_000_000)
	if err := checkBudget(); err != nil {
		t.Errorf("a limit of zero refused: %v", err)
	}
}

// digest is what the calls of these tests extract.
type digest struct {
	Summary string `json:"summary"`
}

// pricedProvider is a provider that answers one call at a time and says it used
// a thousand tokens each way, on a model priced at $1,000 per million tokens, so
// that every call costs two dollars.
func pricedProvider(t *testing.T) (*schemafluxtest.Provider, string) {
	t.Helper()
	name := uniqueModel(t)
	restore, err := pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 1000, Output: 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	var p schemafluxtest.Provider
	p.Reply(`{"summary":"ok"}`)
	p.SetUsage(schemaflux.Usage{Input: 1000, Output: 1000})
	return &p, name
}

func TestTheProcessCeilingStillBindsAClientThatHasABudgetOfItsOwn(t *testing.T) {
	isolateLedger(t)
	for _, tc := range []struct {
		name        string
		process     float64
		client      *schemaflux.Budget
		refusedWith string
	}{
		{"a client budget above the process ceiling", 0.5, &schemaflux.Budget{Limit: 100}, "process"},
		{"a client budget that is only counting", 0.5, &schemaflux.Budget{}, "process"},
		{"a client budget below the process ceiling", 100, &schemaflux.Budget{Limit: 0.5}, "client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pricing.Reset()
			restore := setLimits(t, pricing.Limits{Daily: tc.process, Enforce: true})
			defer restore()
			p, model := pricedProvider(t)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p, Model: model, Budget: tc.client, Retries: -1})
			if _, err := schemaflux.Extract[digest](ctx, "text"); err != nil {
				t.Fatalf("the first call, with nothing spent, was refused: %v", err)
			}
			_, err := schemaflux.Extract[digest](ctx, "text")
			if !errors.Is(err, schemaflux.ErrBudgetExceeded) {
				t.Fatalf("the second call, with $2 spent against a ceiling of $0.50 (%s), returned %v", tc.refusedWith, err)
			}
			if p.Calls() != 1 {
				t.Errorf("the provider was called %d times, want 1: a refused call sends nothing", p.Calls())
			}
		})
	}
}

func TestConcurrentSetLimitsCallsAndRestoresLeaveNoLimitsInForce(t *testing.T) {
	isolateLedger(t)
	var wait sync.WaitGroup
	for i := range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			restore, err := pricing.SetLimits(pricing.Limits{Daily: float64(i + 1), Enforce: true})
			if err != nil {
				t.Errorf("SetLimits: %v", err)
				return
			}
			restore()
			restore()
		}()
	}
	wait.Wait()
	pricing.Reset()
	spend("x", 1000)
	if err := checkBudget(); err != nil {
		t.Errorf("limits are still in force: %v", err)
	}
}

// Replacing one enforced budget with another must never expose the unlocked
// interval that used to exist between ResetBudget and SetBudgetEnforcement.
func TestReplacingEnforcedLimitsNeverAdmitsAnOverBudgetCall(t *testing.T) {
	isolateLedger(t)
	base := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
	defer base()
	spend("already-over", 3)

	done := make(chan struct{})
	checked := make(chan struct{})
	admitted := make(chan struct{}, 1)
	go func() {
		defer close(checked)
		for {
			select {
			case <-done:
				return
			default:
			}
			if checkBudget() == nil {
				select {
				case admitted <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	for range 2000 {
		restore := setLimits(t, pricing.Limits{Daily: 2, Enforce: true})
		restore()
	}
	close(done)
	<-checked
	select {
	case <-admitted:
		t.Error("a call passed while every configured budget was enforcing and exhausted")
	default:
	}
}

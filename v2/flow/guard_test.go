package flow_test

import (
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/v2/flow"
)

type order struct {
	Total   int
	Address string
	Items   []string
}

func positiveTotal(o order) (bool, string) { return o.Total > 0, "the total is not positive" }
func hasAddress(o order) (bool, string)    { return o.Address != "", "there is no address" }
func hasItems(o order) (bool, string)      { return len(o.Items) > 0, "there are no items" }

func TestGuardReportsEveryCheckThatFailedInTheOrderOfTheChecks(t *testing.T) {
	cases := []struct {
		name   string
		state  order
		checks []func(order) (bool, string)
		want   flow.GuardResult
	}{
		{"every check passes", order{5, "here", []string{"x"}}, []func(order) (bool, string){positiveTotal, hasAddress, hasItems},
			flow.GuardResult{CanProceed: true, FailedChecks: []string{}}},
		{"the first fails", order{0, "here", []string{"x"}}, []func(order) (bool, string){positiveTotal, hasAddress, hasItems},
			flow.GuardResult{FailedChecks: []string{"the total is not positive"}}},
		{"the last fails", order{5, "here", nil}, []func(order) (bool, string){positiveTotal, hasAddress, hasItems},
			flow.GuardResult{FailedChecks: []string{"there are no items"}}},
		{"all fail", order{}, []func(order) (bool, string){positiveTotal, hasAddress, hasItems},
			flow.GuardResult{FailedChecks: []string{"the total is not positive", "there is no address", "there are no items"}}},
		{"two of three fail and the order is the checks'", order{5, "", nil}, []func(order) (bool, string){hasItems, positiveTotal, hasAddress},
			flow.GuardResult{FailedChecks: []string{"there are no items", "there is no address"}}},
		{"no checks", order{}, nil, flow.GuardResult{CanProceed: true, FailedChecks: []string{}}},
		{"one check that passes", order{5, "", nil}, []func(order) (bool, string){positiveTotal},
			flow.GuardResult{CanProceed: true, FailedChecks: []string{}}},
		{"one check that fails with no message", order{}, []func(order) (bool, string){func(order) (bool, string) { return false, "" }},
			flow.GuardResult{FailedChecks: []string{""}}},
		{"the same check twice", order{}, []func(order) (bool, string){positiveTotal, positiveTotal},
			flow.GuardResult{FailedChecks: []string{"the total is not positive", "the total is not positive"}}},
		{"a message a check wrote about the state", order{Total: -3}, []func(order) (bool, string){
			func(o order) (bool, string) { return false, fmt.Sprintf("total %d is below zero", o.Total) }},
			flow.GuardResult{FailedChecks: []string{"total -3 is below zero"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flow.Guard(tc.state, tc.checks...)
			if got.CanProceed != tc.want.CanProceed || !reflect.DeepEqual(got.FailedChecks, tc.want.FailedChecks) {
				t.Errorf("Guard = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGuardRunsEveryCheckOnceAndInOrderEvenAfterOneFails(t *testing.T) {
	var calls []int
	check := func(n int, passes bool) func(string) (bool, string) {
		return func(string) (bool, string) {
			calls = append(calls, n)
			return passes, fmt.Sprint("check ", n)
		}
	}
	got := flow.Guard("state", check(1, false), check(2, true), check(3, false), check(4, false))
	if !reflect.DeepEqual(calls, []int{1, 2, 3, 4}) {
		t.Errorf("the checks ran in the order %v", calls)
	}
	if got.CanProceed || !reflect.DeepEqual(got.FailedChecks, []string{"check 1", "check 3", "check 4"}) {
		t.Errorf("Guard = %+v", got)
	}
}

func TestGuardHandsEveryCheckTheStateItWasGiven(t *testing.T) {
	var seen []string
	record := func(s string) (bool, string) {
		seen = append(seen, s)
		return true, ""
	}
	flow.Guard("the state", record, record)
	if !reflect.DeepEqual(seen, []string{"the state", "the state"}) {
		t.Errorf("checks saw %v", seen)
	}
}

func TestGuardDoesNotPassAVerdictOnAChecksThatIsNotThere(t *testing.T) {
	var ran atomic.Int32
	counted := func(order) (bool, string) {
		ran.Add(1)
		return true, ""
	}
	cases := []struct {
		name     string
		checks   []func(order) (bool, string)
		position int
	}{
		{"the only check", []func(order) (bool, string){nil}, 1},
		{"the first of two", []func(order) (bool, string){nil, counted}, 1},
		{"the second of two", []func(order) (bool, string){counted, nil}, 2},
		{"the third of four", []func(order) (bool, string){counted, counted, nil, counted}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flow.Guard(order{}, tc.checks...)
			if got.CanProceed || len(got.FailedChecks) != 1 || !strings.Contains(got.FailedChecks[0], fmt.Sprint("check ", tc.position, " is nil")) {
				t.Errorf("Guard = %+v", got)
			}
			if ran.Load() != 0 {
				t.Errorf("%d checks ran although one was missing", ran.Load())
			}
		})
	}
}

func TestGuardLetsAPanicInACheckReachTheCaller(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "from the check" {
			t.Errorf("recovered %v", recovered)
		}
	}()
	flow.Guard(order{}, func(order) (bool, string) { panic("from the check") })
	t.Error("Guard returned after a panic in a check")
}

func TestGuardWorksOverAnyTypeOfState(t *testing.T) {
	if got := flow.Guard(3, func(n int) (bool, string) { return n > 2, "too small" }); !got.CanProceed {
		t.Errorf("Guard over an int = %+v", got)
	}
	if got := flow.Guard([]string{"a"}, func(s []string) (bool, string) { return len(s) > 1, "too short" }); got.CanProceed || got.FailedChecks[0] != "too short" {
		t.Errorf("Guard over a slice = %+v", got)
	}
	var none *order
	if got := flow.Guard(none, func(o *order) (bool, string) { return o != nil, "no order" }); got.CanProceed {
		t.Errorf("Guard over a nil pointer = %+v", got)
	}
}

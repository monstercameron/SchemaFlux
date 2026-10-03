package flow_test

import (
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/v2/flow"
)

// counter returns an action that counts its runs, and the counter.
func counter() (func(), *atomic.Int32) {
	var n atomic.Int32
	return func() { n.Add(1) }, &n
}

func TestWhenBuildsACaseOfTheConditionAndTheAction(t *testing.T) {
	type custom struct{ A int }
	conditions := []struct {
		name      string
		condition any
	}{
		{"a description for a model to judge", "a billing question"},
		{"a reflect.Type", reflect.TypeFor[error]()},
		{"an error value", io.EOF},
		{"a value whose type is compared", 42},
		{"a struct value", custom{1}},
		{"a pointer", &custom{2}},
		{"a string that names the default", "default"},
		{"an empty string", ""},
		{"nil", nil},
		{"a slice", []string{"a"}},
	}
	for _, tc := range conditions {
		t.Run(tc.name, func(t *testing.T) {
			action, n := counter()
			got := flow.When(tc.condition, action)
			if !reflect.DeepEqual(got.Condition, tc.condition) {
				t.Errorf("Condition = %#v, want %#v", got.Condition, tc.condition)
			}
			if got.Action == nil {
				t.Fatal("the action was dropped")
			}
			got.Action()
			if n.Load() != 1 {
				t.Errorf("the action ran %d times", n.Load())
			}
		})
	}
}

func TestWhenKeepsANilActionSoThatMatchSkipsTheCase(t *testing.T) {
	if got := flow.When("anything", nil); got.Action != nil || got.Condition != "anything" {
		t.Errorf("When = %+v", got)
	}
}

func TestWhenDoesNotRunTheActionItIsGiven(t *testing.T) {
	action, n := counter()
	_ = flow.When("x", action)
	_ = flow.Otherwise(action)
	if n.Load() != 0 {
		t.Errorf("building a case ran its action %d times", n.Load())
	}
}

func TestOtherwiseBuildsTheDefaultCase(t *testing.T) {
	action, n := counter()
	got := flow.Otherwise(action)
	if got.Condition != "otherwise" {
		t.Errorf("Condition = %#v, want the string Match reads as the default case", got.Condition)
	}
	got.Action()
	if n.Load() != 1 {
		t.Errorf("the action ran %d times", n.Load())
	}
	if nilAction := flow.Otherwise(nil); nilAction.Action != nil || nilAction.Condition != "otherwise" {
		t.Errorf("Otherwise(nil) = %+v", nilAction)
	}
}

func TestACaseIsAPlainValueThatCanBeBuiltByHandAndCompared(t *testing.T) {
	action, _ := counter()
	built := flow.Case{Condition: errors.New("x"), Action: action}
	if built.Condition == nil || built.Action == nil {
		t.Errorf("Case = %+v", built)
	}
	if zero := (flow.Case{}); zero.Condition != nil || zero.Action != nil {
		t.Errorf("the zero Case = %+v", zero)
	}
	cases := []flow.Case{flow.When("a", action), flow.Otherwise(action), flow.When(1, nil)}
	if len(cases) != 3 || cases[1].Condition != "otherwise" {
		t.Errorf("cases = %+v", cases)
	}
}

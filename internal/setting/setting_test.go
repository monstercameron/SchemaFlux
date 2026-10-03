package setting_test

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/setting"
	"github.com/monstercameron/schemaflux/internal/setting/settingtest"
)

// held is a setting that is nothing but an int, so that the layers are tested
// without anything of a package's own beside them.
type held struct {
	value   int
	applied []int
	layers  setting.Layers[int]
}

func newHeld(base int) *held {
	h := &held{value: base}
	h.layers = setting.Layers[int]{
		Apply: func(v int) { h.value = v; h.applied = append(h.applied, v) },
		Base:  func() int { return h.value },
	}
	return h
}

func TestLayersFollowTheConventionOfEveryRestore(t *testing.T) {
	h := newHeld(0)
	settingtest.Convention(t, settingtest.Subject{
		Name:    "setting.Layers",
		Set:     func(n int) func() { return h.layers.Set(n) },
		Current: func() int { return h.value },
	})
}

func TestLayersPutBackTheValueThatWasThereWhenTheFirstWasSet(t *testing.T) {
	h := newHeld(42)
	first := h.layers.Set(1)
	h.value = 99 // changed behind the layers' back while a layer is in force
	second := h.layers.Set(2)
	second()
	if h.value != 1 {
		t.Errorf("after the second restore %d is in force, want 1", h.value)
	}
	first()
	if h.value != 42 {
		t.Errorf("after both restores %d is in force, want the 42 that was there", h.value)
	}
}

func TestLayersApplyOnlyWhenWhatIsInForceChanges(t *testing.T) {
	h := newHeld(0)
	first, second := h.layers.Set(1), h.layers.Set(2)
	first()
	first()
	second()
	second()
	want := []int{1, 2, 0}
	if len(h.applied) != len(want) {
		t.Fatalf("applied %v, want %v", h.applied, want)
	}
	for i := range want {
		if h.applied[i] != want[i] {
			t.Fatalf("applied %v, want %v", h.applied, want)
		}
	}
}

func TestLayersTakeTheBaseOnlyWhenTheFirstLayerIsSet(t *testing.T) {
	reads := 0
	h := newHeld(7)
	h.layers.Base = func() int { reads++; return h.value }
	a, b := h.layers.Set(1), h.layers.Set(2)
	if reads != 1 {
		t.Errorf("Base read %d times for two nested layers, want 1", reads)
	}
	b()
	a()
	c := h.layers.Set(3)
	if reads != 2 {
		t.Errorf("Base read %d times after the layers emptied and one was set, want 2", reads)
	}
	c()
	if h.value != 7 {
		t.Errorf("%d in force, want 7", h.value)
	}
}

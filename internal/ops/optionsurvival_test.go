package ops

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A-014. applyDefaults copied eight of OpOptions' thirteen fields by name, so
// every field added after it was written was set by the caller and thrown away
// before the request was built. ST-003's ceiling and P-009's cache identity
// were both unreachable from the legacy entrypoints while both looked
// implemented, because their own tests drove CallLLM directly.
//
// This test walks the struct by reflection rather than naming fields, because
// a hand-written list is exactly what created the bug: a list cannot fail when
// somebody adds a field, and that is the only moment it needed to.

// nonZeroOpOptions fills every field with a distinguishable non-zero value.
// Adding a field to OpOptions without adding it here fails
// TestEveryOpOptionsFieldIsPopulatedInTheFixture below, so the fixture cannot
// quietly fall behind either.
func nonZeroOpOptions() types.OpOptions {
	return types.OpOptions{
		Steering:        "steer me",
		Threshold:       0.42,
		Mode:            types.Strict,
		Intelligence:    types.Quick,
		Model:           "pinned-model",
		Effort:          types.EffortLow,
		WebSearch:       true,
		ExactFields:     true,
		CompleteFields:  true,
		Context:         context.Background(),
		RequestID:       "req-1",
		CorrelationID:   "corr-1",
		JSONSchema:      map[string]any{"type": "object"},
		SchemaName:      "widget",
		SchemaID:        "widget@v1",
		ResponseFormat:  "json_schema",
		CacheIdentity:   "extract|v1|hash",
		MaxOutputTokens: 321,
		ParentResultIDs: []string{"parent-1"},
	}
}

func TestEveryOpOptionsFieldIsPopulatedInTheFixture(t *testing.T) {
	value := reflect.ValueOf(nonZeroOpOptions())
	structType := value.Type()

	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).IsZero() {
			t.Errorf("nonZeroOpOptions leaves %s at its zero value, so no survival test covers it",
				structType.Field(i).Name)
		}
	}
}

// The regression itself: everything the caller set has to come out the other
// side.
func TestApplyDefaultsCarriesEveryCallerSetField(t *testing.T) {
	in := nonZeroOpOptions()
	out := applyDefaults(in)

	inValue := reflect.ValueOf(in)
	outValue := reflect.ValueOf(out)
	structType := inValue.Type()

	for i := 0; i < inValue.NumField(); i++ {
		name := structType.Field(i).Name
		if outValue.Field(i).IsZero() {
			t.Errorf("applyDefaults dropped %s entirely", name)
			continue
		}
		if !reflect.DeepEqual(inValue.Field(i).Interface(), outValue.Field(i).Interface()) {
			t.Errorf("applyDefaults changed %s: %v -> %v",
				name, inValue.Field(i).Interface(), outValue.Field(i).Interface())
		}
	}
}

func TestApplyDefaultsSuppliesDefaultsOnlyWhenNothingWasChosen(t *testing.T) {
	out := applyDefaults()
	if out.Mode != types.TransformMode {
		t.Errorf("Mode = %v, want the TransformMode default", out.Mode)
	}
	if out.Intelligence != types.Smart {
		t.Errorf("Intelligence = %v, want the Smart default", out.Intelligence)
	}

	// And a caller who chose keeps their choice, including Strict, which used
	// to be indistinguishable from unset.
	chosen := applyDefaults(types.OpOptions{Mode: types.Strict, Intelligence: types.Quick})
	if chosen.Mode != types.Strict {
		t.Errorf("Mode = %v; an explicit Strict was overwritten by the default", chosen.Mode)
	}
	if chosen.Intelligence != types.Quick {
		t.Errorf("Intelligence = %v; an explicit Quick was overwritten", chosen.Intelligence)
	}
}

// Later options win, field by field, so a caller layering a per-call override
// over a base does not lose the base's other settings.
func TestApplyDefaultsLayersOptionsWithoutLosingEarlierFields(t *testing.T) {
	base := types.OpOptions{
		Steering:        "base steering",
		MaxOutputTokens: 100,
		SchemaName:      "widget",
	}
	override := types.OpOptions{MaxOutputTokens: 999}

	out := applyDefaults(base, override)

	if out.MaxOutputTokens != 999 {
		t.Errorf("MaxOutputTokens = %d, want the override", out.MaxOutputTokens)
	}
	if out.Steering != "base steering" {
		t.Errorf("Steering = %q; the override erased a field it never mentioned", out.Steering)
	}
	if out.SchemaName != "widget" {
		t.Errorf("SchemaName = %q; the override erased a field it never mentioned", out.SchemaName)
	}
}

// The embedded merge was already safe -- it starts from the whole embedded
// struct rather than copying named fields -- and this pins that, because the
// obvious "tidy-up" is to rewrite it in the style that just failed.
func TestEmbeddedMergeCarriesEveryEmbeddedField(t *testing.T) {
	embedded := nonZeroOpOptions()
	out := mergeEmbeddedOpOptions(CommonOptions{}, embedded)

	inValue := reflect.ValueOf(embedded)
	outValue := reflect.ValueOf(out)
	structType := inValue.Type()

	for i := 0; i < inValue.NumField(); i++ {
		name := structType.Field(i).Name
		// Context is rebuilt with request tracking, so identity is not the
		// property to assert -- only that it survived.
		if name == "Context" {
			if outValue.Field(i).IsZero() {
				t.Error("mergeEmbeddedOpOptions dropped Context")
			}
			continue
		}
		if !reflect.DeepEqual(inValue.Field(i).Interface(), outValue.Field(i).Interface()) {
			t.Errorf("mergeEmbeddedOpOptions changed %s: %v -> %v",
				name, inValue.Field(i).Interface(), outValue.Field(i).Interface())
		}
	}
}

// applyDefaultsByReflection is what applyDefaults was before it named its
// fields: the oracle the hand-written merge is held to.
func applyDefaultsByReflection(opts ...types.OpOptions) types.OpOptions {
	result := types.OpOptions{}
	for _, opt := range opts {
		incoming := reflect.ValueOf(opt)
		merged := reflect.ValueOf(&result).Elem()
		for i := 0; i < incoming.NumField(); i++ {
			field := incoming.Field(i)
			if !merged.Field(i).CanSet() || field.IsZero() {
				continue
			}
			merged.Field(i).Set(field)
		}
	}
	if result.Mode == types.ModeUnset {
		result.Mode = types.TransformMode
	}
	if result.Intelligence == types.TierUnset {
		result.Intelligence = types.Smart
	}
	return result
}

// Each field on its own, in a pair with every other, in either order, comes out
// as the reflective merge gives it. With nineteen fields that is every case a
// field can be mishandled in: dropped, copied when zero, or copied over a later
// option's value.
func TestApplyDefaultsAgreesWithTheReflectiveMergeOnEveryPairOfFields(t *testing.T) {
	full := reflect.ValueOf(nonZeroOpOptions())
	single := func(i int) types.OpOptions {
		var one types.OpOptions
		reflect.ValueOf(&one).Elem().Field(i).Set(full.Field(i))
		return one
	}
	other := nonZeroOpOptions()
	other.Steering, other.Model, other.RequestID, other.MaxOutputTokens = "other", "other-model", "req-2", 7
	other.Threshold, other.SchemaName, other.CorrelationID = 0.9, "gadget", "corr-2"

	for i := 0; i < full.NumField(); i++ {
		name := full.Type().Field(i).Name
		for j := 0; j < full.NumField(); j++ {
			a, b := single(i), single(j)
			if got, want := applyDefaults(a, b), applyDefaultsByReflection(a, b); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s then %s: got %+v, want %+v", name, full.Type().Field(j).Name, got, want)
			}
		}
		if got, want := applyDefaults(single(i), other), applyDefaultsByReflection(single(i), other); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s then everything: got %+v, want %+v", name, got, want)
		}
		if got, want := applyDefaults(other, single(i)), applyDefaultsByReflection(other, single(i)); !reflect.DeepEqual(got, want) {
			t.Fatalf("everything then %s: got %+v, want %+v", name, got, want)
		}
	}
	if got, want := applyDefaults(), applyDefaultsByReflection(); !reflect.DeepEqual(got, want) {
		t.Errorf("no options: got %+v, want %+v", got, want)
	}
}

// What the merge costs, so that it cannot go back to walking the struct: the
// reflective one allocated once for each option passed.
func TestApplyDefaultsAllocationsStayWithinBudget(t *testing.T) {
	full := nonZeroOpOptions()
	one := types.OpOptions{Mode: types.Strict, Intelligence: types.Smart}
	three := types.OpOptions{Context: context.Background(), RequestID: "req_0123456789abcdef", MaxOutputTokens: 4096}
	cases := []struct {
		name   string
		run    func()
		budget float64
	}{
		{"no options", func() { _ = applyDefaults() }, 0},
		{"one option", func() { _ = applyDefaults(one) }, 0},
		{"three options", func() { _ = applyDefaults(one, three, full) }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(200, tc.run); got > tc.budget {
				t.Errorf("%v allocations, budget %v", got, tc.budget)
			}
		})
	}
}

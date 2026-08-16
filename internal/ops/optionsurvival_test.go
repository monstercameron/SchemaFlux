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

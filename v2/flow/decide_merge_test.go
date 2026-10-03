package flow

import (
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// setValue gives the field a value that is not its zero, and a different one for
// each variant, whatever the field's type is.
func setValue(t *testing.T, field reflect.Value, name string, variant int) {
	t.Helper()
	switch field.Kind() {
	case reflect.String:
		field.SetString([]string{"first", "second"}[variant])
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		field.SetInt(int64(variant + 1))
	case reflect.Bool:
		field.SetBool(true)
	default:
		t.Fatalf("Options.%s is a %s, which this test cannot fill: teach it, and mergeDecideOptions with it", name, field.Kind())
	}
}

// TestMergeDecideOptionsCopiesEveryFieldOfOptions is what keeps the merge of this
// package, which cannot call the root's, whole: a field added to Options that
// mergeDecideOptions does not lay fails here.
func TestMergeDecideOptionsCopiesEveryFieldOfOptions(t *testing.T) {
	typ := reflect.TypeFor[schemaflux.Options]()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			with := func(variant int) schemaflux.Options {
				var o schemaflux.Options
				setValue(t, reflect.ValueOf(&o).Elem().Field(i), name, variant)
				return o
			}
			field := func(o schemaflux.Options) any { return reflect.ValueOf(o).Field(i).Interface() }

			if got := mergeDecideOptions([]schemaflux.Options{{}, with(0)}); field(got) != field(with(0)) {
				t.Errorf("a later %s is not laid over an earlier zero: %v", name, field(got))
			}
			if got := mergeDecideOptions([]schemaflux.Options{with(0), {}}); field(got) != field(with(0)) {
				t.Errorf("a later zero %s replaced an earlier value: %v", name, field(got))
			}
			if got := mergeDecideOptions([]schemaflux.Options{with(0), with(1)}); field(got) != field(with(1)) && typ.Field(i).Type.Kind() != reflect.Bool {
				t.Errorf("a later %s did not replace an earlier one: %v", name, field(got))
			}
			if got := mergeDecideOptions([]schemaflux.Options{with(0), {}, {}}); field(got) != field(with(0)) {
				t.Errorf("a third zero value changed %s: %v", name, field(got))
			}
			// No other field is touched by this one.
			other := mergeDecideOptions([]schemaflux.Options{{}, with(0)})
			for j := range typ.NumField() {
				if j != i && !reflect.ValueOf(other).Field(j).IsZero() {
					t.Errorf("laying %s set %s", name, typ.Field(j).Name)
				}
			}
		})
	}
}

func TestMergeDecideOptionsDoesNotChangeTheValuesItMerges(t *testing.T) {
	first, second := schemaflux.Options{Model: "a", WebSearch: true}, schemaflux.Options{Model: "b", MaxOutputTokens: 5}
	merged := mergeDecideOptions([]schemaflux.Options{first, second})
	if first != (schemaflux.Options{Model: "a", WebSearch: true}) || second != (schemaflux.Options{Model: "b", MaxOutputTokens: 5}) {
		t.Errorf("a value was changed: %+v %+v", first, second)
	}
	if merged != (schemaflux.Options{Model: "b", MaxOutputTokens: 5, WebSearch: true}) {
		t.Errorf("merged = %+v", merged)
	}
}

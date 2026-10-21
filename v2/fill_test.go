package schemaflux

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// fillValue sets v, and everything inside it, to values that are not zero and
// that differ from field to field, so that a conversion that drops or swaps a
// field is seen by a comparison. A field is filled by its kind: a string by its
// name, a number by a counter, a bool true, a map or a slice with one filled
// element, a pointer with a filled value. A time.Time is set to a fixed instant.
func fillValue(t *testing.T, v reflect.Value, name string, counter *int) {
	t.Helper()
	*counter++
	switch v.Kind() {
	case reflect.String:
		v.SetString("v-" + name)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// A decision kind is 1 to 3; any other number is as good as one.
		v.SetInt(int64(*counter%3 + 1))
		if v.Type() == reflect.TypeOf(time.Duration(0)) {
			v.SetInt(int64(*counter) * int64(time.Millisecond))
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*counter%3 + 1))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(0.25 + float64(*counter)/100)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Date(2026, 10, 3, 12, 0, *counter, 0, time.UTC)))
			return
		}
		for i := range v.NumField() {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			fillValue(t, v.Field(i), name+"."+v.Type().Field(i).Name, counter)
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillValue(t, v.Elem(), name, counter)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		for i := range 2 {
			fillValue(t, v.Index(i), name+"."+string(rune('a'+i)), counter)
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key := reflect.New(v.Type().Key()).Elem()
		fillValue(t, key, name+".key", counter)
		elem := reflect.New(v.Type().Elem()).Elem()
		fillValue(t, elem, name+".value", counter)
		v.SetMapIndex(key, elem)
	case reflect.Interface:
		if v.NumMethod() != 0 {
			t.Fatalf("%s: an interface with methods cannot be filled", name)
		}
		v.Set(reflect.ValueOf("v-" + name))
	default:
		t.Fatalf("%s: the kind %v cannot be filled", name, v.Kind())
	}
}

// fieldNames returns the names of the exported fields of the struct type typ.
func fieldNames(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumField() {
		if typ.Field(i).IsExported() {
			names = append(names, typ.Field(i).Name)
		}
	}
	return names
}

// onlyField returns a zero value of typ with the one field name filled.
func onlyField(t *testing.T, typ reflect.Type, name string) reflect.Value {
	t.Helper()
	value := reflect.New(typ).Elem()
	field := value.FieldByName(name)
	counter := 0
	fillValue(t, field, name, &counter)
	return value
}

func TestFillValueFillsEverything(t *testing.T) {
	t.Parallel()
	type inner struct {
		S string
		N int
	}
	type sample struct {
		A string
		B bool
		C float64
		D time.Duration
		E []string
		F map[string]any
		G *inner
		H []inner
		I time.Time
		J uint8
		K any
	}
	var s sample
	counter := 0
	fillValue(t, reflect.ValueOf(&s).Elem(), "s", &counter)
	v := reflect.ValueOf(s)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("%s was left zero", v.Type().Field(i).Name)
		}
	}
	if !strings.HasPrefix(s.A, "v-") || s.G == nil || s.G.S == "" || len(s.H) != 2 {
		t.Errorf("got %+v", s)
	}
}

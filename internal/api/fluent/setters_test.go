package fluent

import (
	"context"
	"reflect"
	"testing"
)

// A builder method that compiles, chains, and changes nothing is this package's
// signature failure. It has happened three times already and each one was
// invisible until somebody's setting quietly did not apply:
//
//   - F-02: eleven advanced option types had no Mode field, so `.Strict()`
//     compiled and had nowhere to put the value;
//   - F-04: `.Steer()` assigned instead of appending, so the second call
//     silently dropped the first;
//   - TC-005's `.Model()`: the setter existed on requestBase and the option
//     types it wrote to had no Model field.
//
// The pattern is always the same — the method exists, the field does not, or
// the write goes somewhere nothing reads. So these check the property directly
// rather than trusting that a setter named after a field reaches it: build,
// set, read back, and require the value to have moved.
//
// It is written against the option structs by reflection rather than by naming
// fields, because a hand-written list is what failed the last three times.

// readField pulls a named field out of an options value, following the embedded
// CommonOptions and OpOptions when the field is not at the top level — the
// three shapes this package's option types come in.
func readField(t *testing.T, opts any, name string) (reflect.Value, bool) {
	t.Helper()

	value := reflect.ValueOf(opts)
	if value.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}

	if field := value.FieldByName(name); field.IsValid() {
		return field, true
	}
	for _, embedded := range []string{"CommonOptions", "OpOptions"} {
		if inner := value.FieldByName(embedded); inner.IsValid() && inner.Kind() == reflect.Struct {
			if field := inner.FieldByName(name); field.IsValid() {
				return field, true
			}
		}
	}
	return reflect.Value{}, false
}

func requireSet(t *testing.T, name, field string, opts any, want any) {
	t.Helper()

	got, found := readField(t, opts, field)
	if !found {
		t.Errorf("%s: the options carry no %s field at all, so the setter writes nowhere", name, field)
		return
	}
	if !reflect.DeepEqual(got.Interface(), want) {
		t.Errorf("%s: %s = %v, want %v — the setter compiled and did not apply",
			name, field, got.Interface(), want)
	}
}

func TestModelPinReachesEveryBuilderItIsOfferedOn(t *testing.T) {
	const pin = "pinned-model"

	requireSet(t, "Extracting", "Model", Extracting[map[string]any]("x").Model(pin).opts, pin)
	requireSet(t, "Transforming", "Model", Transforming[string, string]("x").Model(pin).opts, pin)
	requireSet(t, "Generating", "Model", Generating[string]("x").Model(pin).opts, pin)
	requireSet(t, "Choosing", "Model", Choosing([]string{"a"}).Model(pin).opts, pin)
	requireSet(t, "Filtering", "Model", Filtering([]string{"a"}).Model(pin).opts, pin)
	requireSet(t, "Sorting", "Model", Sorting([]string{"a"}).Model(pin).opts, pin)
}

// An empty pin clears rather than pins to nothing, which is what a caller
// reading `.Model("")` expects and the opposite of what a bare assignment on a
// chain that set a model earlier would do.
func TestAnEmptyModelClearsThePin(t *testing.T) {
	cleared := Extracting[map[string]any]("x").Model("pinned").Model("").opts
	requireSet(t, "Extracting", "Model", cleared, "")
}

func TestExactFieldsAndCompleteFieldsAreSeparateSwitches(t *testing.T) {
	// The point of splitting them out of Strict is that a caller can have one
	// without the other. If setting one turned on both, the split would be
	// decoration.
	exact := Extracting[map[string]any]("x").ExactFields().opts
	requireSet(t, "Extracting().ExactFields()", "ExactFields", exact, true)
	requireSet(t, "Extracting().ExactFields()", "CompleteFields", exact, false)

	complete := Extracting[map[string]any]("x").CompleteFields().opts
	requireSet(t, "Extracting().CompleteFields()", "CompleteFields", complete, true)
	requireSet(t, "Extracting().CompleteFields()", "ExactFields", complete, false)

	both := Extracting[map[string]any]("x").ExactFields().CompleteFields().opts
	requireSet(t, "both", "ExactFields", both, true)
	requireSet(t, "both", "CompleteFields", both, true)
}

func TestTheCommonSettersApplyOnEveryCoreBuilder(t *testing.T) {
	cases := []struct {
		name string
		opts any
	}{
		{"Extracting", Extracting[map[string]any]("x").
			Steer("be careful").RequestID("req-1").CorrelationID("corr-1").opts},
		{"Transforming", Transforming[string, string]("x").
			Steer("be careful").RequestID("req-1").CorrelationID("corr-1").opts},
		{"Generating", Generating[string]("x").
			Steer("be careful").RequestID("req-1").CorrelationID("corr-1").opts},
		{"Choosing", Choosing([]string{"a"}).By("best").
			Steer("be careful").RequestID("req-1").CorrelationID("corr-1").opts},
		{"Filtering", Filtering([]string{"a"}).By("good").
			Steer("be careful").RequestID("req-1").CorrelationID("corr-1").opts},
		{"Sorting", Sorting([]string{"a"}).By("size").
			Steer("be careful").RequestID("req-1").CorrelationID("corr-1").opts},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireSet(t, tc.name, "Steering", tc.opts, "be careful")
			requireSet(t, tc.name, "RequestID", tc.opts, "req-1")
			requireSet(t, tc.name, "CorrelationID", tc.opts, "corr-1")
		})
	}
}

// FL-004's regression, checked on every builder rather than the one it was
// found on: two Steer calls accumulate instead of the second silently
// discarding the first.
func TestSteerAccumulatesOnEveryCoreBuilder(t *testing.T) {
	cases := []struct {
		name string
		opts any
	}{
		{"Extracting", Extracting[map[string]any]("x").Steer("first").Steer("second").opts},
		{"Transforming", Transforming[string, string]("x").Steer("first").Steer("second").opts},
		{"Generating", Generating[string]("x").Steer("first").Steer("second").opts},
		{"Choosing", Choosing([]string{"a"}).Steer("first").Steer("second").opts},
		{"Filtering", Filtering([]string{"a"}).Steer("first").Steer("second").opts},
		{"Sorting", Sorting([]string{"a"}).Steer("first").Steer("second").opts},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := readField(t, tc.opts, "Steering")
			if !found {
				t.Fatalf("%s carries no Steering field", tc.name)
			}
			steering, _ := got.Interface().(string)
			if steering == "second" {
				t.Errorf("%s: the second Steer discarded the first (%q)", tc.name, steering)
			}
			if steering == "" {
				t.Errorf("%s: neither Steer applied", tc.name)
			}
		})
	}
}

func TestTheTierSettersAreDistinguishableFromSilence(t *testing.T) {
	// A-005's lesson at the fluent layer: the zero value has to mean "unset",
	// or `.Smart()` is indistinguishable from never having chosen.
	unset := Extracting[map[string]any]("x").opts
	smart := Extracting[map[string]any]("x").Smart().opts
	fast := Extracting[map[string]any]("x").Fast().opts
	quick := Extracting[map[string]any]("x").Quick().opts

	unsetTier, _ := readField(t, unset, "Intelligence")
	smartTier, _ := readField(t, smart, "Intelligence")
	fastTier, _ := readField(t, fast, "Intelligence")
	quickTier, _ := readField(t, quick, "Intelligence")

	if reflect.DeepEqual(unsetTier.Interface(), smartTier.Interface()) {
		t.Error("Smart() is indistinguishable from not choosing a tier")
	}
	if reflect.DeepEqual(smartTier.Interface(), fastTier.Interface()) {
		t.Error("Smart() and Fast() produce the same tier")
	}
	if reflect.DeepEqual(fastTier.Interface(), quickTier.Interface()) {
		t.Error("Fast() and Quick() produce the same tier")
	}
}

func TestContextReachesTheOptionsOnEveryCoreBuilder(t *testing.T) {
	type marker struct{}
	ctx := context.WithValue(context.Background(), marker{}, "present")

	cases := []struct {
		name string
		opts any
	}{
		{"Extracting", Extracting[map[string]any]("x").Context(ctx).opts},
		{"Transforming", Transforming[string, string]("x").Context(ctx).opts},
		{"Generating", Generating[string]("x").Context(ctx).opts},
		{"Choosing", Choosing([]string{"a"}).Context(ctx).opts},
		{"Filtering", Filtering([]string{"a"}).Context(ctx).opts},
		{"Sorting", Sorting([]string{"a"}).Context(ctx).opts},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := readField(t, tc.opts, "Context")
			if !found {
				t.Fatalf("%s carries no Context field", tc.name)
			}
			carried, _ := got.Interface().(context.Context)
			if carried == nil {
				t.Fatalf("%s: Context() stored nothing", tc.name)
			}
			if carried.Value(marker{}) != "present" {
				t.Errorf("%s: the stored context is not the one supplied", tc.name)
			}
		})
	}
}

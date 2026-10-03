package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Fixtures. Each carries tags on which a reading of the tag by hand and
// encoding/json disagree, and every test below takes what encoding/json writes
// or reads as the answer, so a tag the library reads wrongly is a failure here
// whatever the library believes.

// TagInner is an exported struct that is embedded, so its fields are promoted.
type TagInner struct {
	Deep string `json:"deep"`
}

// tagInnerHidden is the same with a name that is not exported: encoding/json
// promotes the exported fields of an embedded struct whatever its name.
type tagInnerHidden struct {
	Buried string `json:"buried"`
}

// tagNoExported has no field encoding/json can write.
type tagNoExported struct {
	hidden string
}

type tagEndsAtQuote struct {
	A string `json:"a'b"`
	B string `json:"'c'"`
}

type tagOmitzero struct {
	A string `json:"a,omitzero"`
	B string `json:",omitzero"`
}

type tagOmitemptyWithValue struct {
	A string `json:"a,omitempty:x"`
	B string `json:",omitempty:x"`
}

type tagDash struct {
	A string `json:"-"`
	B string `json:"b"`
}

type tagDashComma struct {
	A string `json:"-,"`
	B string `json:"b"`
}

type tagPromoted struct {
	TagInner
	Own string `json:"own"`
}

type tagPromotedNamed struct {
	TagInner `json:"inner"`
	Own      string `json:"own"`
}

type tagOnlyDash struct {
	A string `json:"-"`
}

type tagOnlyDashComma struct {
	A string `json:"-,"`
}

type tagOnlyQuote struct {
	A string `json:"'x'"`
}

type tagOnlyNamedEmptyEmbedded struct {
	tagNoExported `json:"inner"`
}

type tagOnlyPromotedLeftOut struct {
	TagInner `json:"-"`
}

type tagOnlyPromoted struct {
	TagInner
}

type tagOnlyPromotedHidden struct {
	tagInnerHidden
}

type tagOnlyHiddenEmbedded struct {
	tagNoExported
}

// writtenKeys is the set of member names encoding/json writes for a value.
func writtenKeys(t *testing.T, value any) []string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(%T): %v", value, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("%T is not written as an object: %v", value, err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// The lines buildStructContext gives the model are named as encoding/json names
// the fields: a name the tag gives, or the Go name where the tag gives none. A
// field the tag leaves out of JSON is not shown at all, since a type that says
// a field is not written has said it is not sent to a provider either, and the
// fields of an embedded struct are listed as encoding/json lists them.
func TestStructContextNamesTheFieldsAsEncodingJSONDoes(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"a name that ends at a quote and one that is only quoted", tagEndsAtQuote{A: "1", B: "2"}},
		{"omitzero", tagOmitzero{A: "1", B: "2"}},
		{"omitempty with a value", tagOmitemptyWithValue{A: "1", B: "2"}},
		{"a field named dash", tagDashComma{A: "1", B: "2"}},
		{"a field left out by a dash", tagDash{A: "1", B: "2"}},
		{"nothing but a field left out by a dash", tagOnlyDash{A: "x"}},
		{"an embedded struct is promoted", tagPromoted{TagInner: TagInner{Deep: "d"}, Own: "o"}},
		{"an embedded struct that is not exported is promoted", tagOnlyPromotedHidden{tagInnerHidden: tagInnerHidden{Buried: "x"}}},
		{"an embedded struct given a name is one member", tagPromotedNamed{TagInner: TagInner{Deep: "d"}, Own: "o"}},
		{"an embedded struct left out by a dash", tagOnlyPromotedLeftOut{TagInner: TagInner{Deep: "x"}}},
		{"a pointer to a struct with a field left out", &tagDash{A: "1", B: "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := reflect.Indirect(reflect.ValueOf(tc.value))
			got := []string{}
			for _, line := range buildStructContext(value, "") {
				name, _, found := strings.Cut(line, ": ")
				if !found {
					t.Fatalf("a line without a name: %q", line)
				}
				got = append(got, name)
			}
			sort.Strings(got)
			if want := writtenKeys(t, tc.value); !reflect.DeepEqual(got, want) {
				t.Errorf("the context names %v, encoding/json writes %v", got, want)
			}
		})
	}

	t.Run("the field left out is found by its Go name, not its json name", func(t *testing.T) {
		lines := buildStructContext(reflect.ValueOf(tagEndsAtQuote{A: "1", B: "2"}), "A")
		if len(lines) != 1 || lines[0] != "B: 2" {
			t.Errorf("lines = %q", lines)
		}
	})
}

// decideSaysNothing says a type has nothing to show when encoding/json writes no
// member of it at all, which is read off the members the type has and not off a
// comparison of its tags with a dash.
func TestDecideSaysNothingWhenEncodingJSONWritesNoMember(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"a field left out by a dash", tagOnlyDash{A: "x"}},
		{"a field named dash", tagOnlyDashComma{A: "x"}},
		{"a name that is only quoted", tagOnlyQuote{A: "x"}},
		{"a name that ends at a quote", tagEndsAtQuote{A: "x"}},
		{"omitzero", tagOmitzero{A: "x"}},
		{"nothing exported", tagNoExported{hidden: "x"}},
		{"an embedded struct given a name that holds nothing exported", tagOnlyNamedEmptyEmbedded{}},
		{"an embedded struct left out by a dash", tagOnlyPromotedLeftOut{TagInner: TagInner{Deep: "x"}}},
		{"an embedded struct", tagOnlyPromoted{TagInner: TagInner{Deep: "x"}}},
		{"an embedded struct whose type is not exported", tagOnlyPromotedHidden{tagInnerHidden: tagInnerHidden{Buried: "x"}}},
		{"an embedded struct that holds nothing exported", tagOnlyHiddenEmbedded{}},
		{"an embedded struct given a name", tagPromotedNamed{Own: "x"}},
		{"a pointer to one", &tagOnlyDash{A: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := len(writtenKeys(t, tc.value)) == 0
			if got := decideSaysNothing(tc.value); got != want {
				t.Errorf("decideSaysNothing = %v; encoding/json writes %v", got, writtenKeys(t, tc.value))
			}
		})
	}
}

// Whether a type has a member is a property of the type, and the decision that
// asks it asks it on every call, so once the type is known it costs nothing.
// Reading the members for each value cost eight allocations of the
// thirteen a decision's request makes.
func TestDecideSaysNothingAllocatesNothingOnceTheTypeIsKnown(t *testing.T) {
	values := []any{
		tagOnlyDash{A: "x"}, &tagOnlyDash{A: "x"}, tagDash{A: "1", B: "2"}, tagPromoted{TagInner: TagInner{Deep: "d"}},
		tagPromotedNamed{Own: "x"}, tagOnlyPromotedHidden{}, tagNoExported{}, caseStrictPromoted{}, tagEndsAtQuote{},
		tagOnlyPromotedLeftOut{}, "text", 4, map[string]int{}, []string{"a"},
	}
	for _, value := range values {
		decideSaysNothing(value) // the type is learned by the first call
		if allocs := testing.AllocsPerRun(100, func() { _ = decideSaysNothing(value) }); allocs != 0 {
			t.Errorf("decideSaysNothing(%T) allocates %.0f times", value, allocs)
		}
	}
}

// A header of a CSV file is matched to a field by the name encoding/json gives
// it: a tag read by hand named a field `a'b` where encoding/json names it a, and
// `'c'` where it gives it no name and the Go name is the field's.
func TestCSVHeadersAreMatchedByTheNamesEncodingJSONGives(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"a name that ends at a quote and one that is only quoted", tagEndsAtQuote{A: "1", B: "2"}},
		{"omitzero", tagOmitzero{A: "1", B: "2"}},
		{"omitempty with a value", tagOmitemptyWithValue{A: "1", B: "2"}},
		{"a field left out by a dash", tagDash{A: "1", B: "2"}},
		{"a field named dash", tagDashComma{A: "1", B: "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			structType := reflect.TypeOf(tc.value)
			index := csvFieldIndex(structType)

			// Every name encoding/json writes is a header that finds its field.
			for _, key := range writtenKeys(t, tc.value) {
				if key == "-" {
					continue // a header of a dash folds to nothing
				}
				at, found := index[foldCSVName(key)]
				if !found {
					t.Errorf("the header %q finds no field; encoding/json writes it", key)
					continue
				}
				if _, written := tagWrittenAt(t, tc.value, at); !written {
					t.Errorf("the header %q finds field %s, which encoding/json does not write", key, structType.Field(at).Name)
				}
			}

			// No name the tag read by hand would have given, and encoding/json does
			// not, finds a field.
			for i := 0; i < structType.NumField(); i++ {
				tag := structType.Field(i).Tag.Get("json")
				name, _, _ := strings.Cut(tag, ",")
				if name == "" || name == "-" || foldCSVName(name) == "" {
					continue
				}
				written := false
				for _, key := range writtenKeys(t, tc.value) {
					written = written || key == name
				}
				if _, found := index[foldCSVName(name)]; found && !written && foldCSVName(name) != foldCSVName(structType.Field(i).Name) {
					t.Errorf("the header %q finds a field, and encoding/json does not name one that", name)
				}
			}

			// A field the tag leaves out is not found by its Go name either.
			for i := 0; i < structType.NumField(); i++ {
				if structType.Field(i).Tag.Get("json") != "-" {
					continue
				}
				if _, found := index[foldCSVName(structType.Field(i).Name)]; found {
					t.Errorf("the header %q finds field %s, which the tag leaves out", structType.Field(i).Name, structType.Field(i).Name)
				}
			}
		})
	}

	t.Run("the refusal lists the names encoding/json gives", func(t *testing.T) {
		for _, tc := range cases {
			err := requireMappableHeaders([]string{"nothing like a field"}, reflect.TypeOf(tc.value))
			if err == nil {
				t.Fatalf("%s: a header row of nothing that fits was accepted", tc.name)
			}
			want := fmt.Sprint(writtenKeys(t, tc.value))
			if !strings.Contains(err.Error(), "accepts are "+want) {
				t.Errorf("%s: %q does not list %s", tc.name, err.Error(), want)
			}
		}
	})
}

// tagWrittenAt reports the name encoding/json writes for field number i of a
// struct value, and whether it writes it at all.
func tagWrittenAt(t *testing.T, value any, i int) (string, bool) {
	t.Helper()
	structValue := reflect.ValueOf(value)
	probe := reflect.New(structValue.Type()).Elem()
	probe.Field(i).SetString("x")
	encoded, err := json.Marshal(probe.Interface())
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]string
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	for key := range object {
		return key, true
	}
	return "", false
}

// Types whose members are matched to a key by different rules of case.

type caseStrictOnly struct {
	Name string `json:"Name,case:strict"`
}

type caseStrictAndFolded struct {
	Name  string `json:"Name,case:strict"`
	Other string `json:"other"`
}

type caseFoldedOnly struct {
	Name string `json:"name"`
}

type caseIgnored struct {
	Name string `json:"Name,case:ignore"`
}

type caseCancelled struct {
	Name string `json:"Name,case:ignore,case:strict"`
}

type caseTwoStrict struct {
	Lower string `json:"name,case:strict"`
	Upper string `json:"NAME,case:strict"`
}

type caseStrictBesideFolded struct {
	Strict string `json:"name,case:strict"`
	Folded string `json:"NAME"`
}

type caseStrictPromoted struct {
	caseStrictOnly
	Own string `json:"own"`
}

type caseStrictUntagged struct {
	Plain  string
	Strict string `json:",case:strict"`
}

type caseStrictWithOptions struct {
	Name string `json:"Name,omitempty,case:strict"`
}

// filledBy reports whether encoding/json, reading {"key": "x"} into a new value
// of the type, puts the string in a member. Every member of these types is a
// string.
func filledBy(t *testing.T, structType reflect.Type, key string) bool {
	t.Helper()
	target := reflect.New(structType)
	body, err := json.Marshal(map[string]string{key: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, target.Interface()); err != nil {
		t.Fatalf("Unmarshal(%s) into %s: %v", body, structType, err)
	}
	return !target.Elem().IsZero()
}

// The names of a type's members are matched to a key as encoding/json matches
// them: the name as it is written wins, and otherwise a member that is not
// tagged case:strict matches a key that differs only in case. A set of
// lower-cased names took a key to share a member with a type that the decoder
// would not put it in.
func TestJSONFieldNamesMatchAKeyAsEncodingJSONFillsAMember(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[caseStrictOnly](), reflect.TypeFor[caseStrictAndFolded](), reflect.TypeFor[caseFoldedOnly](),
		reflect.TypeFor[caseIgnored](), reflect.TypeFor[caseCancelled](), reflect.TypeFor[caseTwoStrict](),
		reflect.TypeFor[caseStrictBesideFolded](), reflect.TypeFor[caseStrictPromoted](), reflect.TypeFor[caseStrictUntagged](),
		reflect.TypeFor[caseStrictWithOptions](), reflect.TypeFor[tagPromoted](), reflect.TypeFor[tagEndsAtQuote](),
	}
	keys := []string{"Name", "name", "NAME", "nAmE", "Other", "other", "OTHER", "Plain", "plain", "Strict", "strict", "own", "OWN", "deep", "DEEP", "a", "A", "B", "b", "-", "x", ""}
	for _, structType := range types {
		t.Run(structType.Name(), func(t *testing.T) {
			names := jsonKeyNames(structType)
			if names == nil {
				t.Fatal("no names for a struct")
			}
			for _, key := range keys {
				if got, want := names.matches(key), filledBy(t, structType, key); got != want {
					t.Errorf("key %q: matches = %v, encoding/json fills a member = %v", key, got, want)
				}
			}
		})
	}

	t.Run("something that is not a struct has no names", func(t *testing.T) {
		for _, notStruct := range []reflect.Type{nil, reflect.TypeFor[int](), reflect.TypeFor[[]string](), reflect.TypeFor[map[string]string](), reflect.TypeFor[any](), reflect.TypeFor[*int]()} {
			if names := jsonKeyNames(notStruct); names != nil {
				t.Errorf("names of %v = %v, want none", notStruct, names)
			}
		}
	})
	t.Run("a pointer to a struct is read as the struct", func(t *testing.T) {
		names := jsonKeyNames(reflect.TypeFor[*caseStrictOnly]())
		if !names.matches("Name") || names.matches("name") {
			t.Errorf("a pointer was not read as its struct: %v", names)
		}
	})
}

// jsonKeyNames lists the members encoding/json reads, under the names they are
// written with and not lower-cased: a member tagged case:strict is one, a field
// the tag leaves out of JSON is not, a name that the tag gives is the one it
// gives, and an embedded struct contributes its own members.
func TestJSONKeyNamesAreTheMembersAsTheyAreWritten(t *testing.T) {
	type mixed struct {
		Renamed  string `json:"renamed"`
		Excluded string `json:"-"`
		DashName string `json:"-,"`
		Untagged string
		Optional string `json:",omitempty"`
		hidden   string
	}
	cases := []struct {
		name  string
		typ   reflect.Type
		names []string
	}{
		{"one folded member", reflect.TypeFor[caseFoldedOnly](), []string{"name"}},
		{"one strict member", reflect.TypeFor[caseStrictOnly](), []string{"Name"}},
		{"a strict member and a folded one", reflect.TypeFor[caseStrictAndFolded](), []string{"Name", "other"}},
		{"case:ignore", reflect.TypeFor[caseIgnored](), []string{"Name"}},
		{"two strict members", reflect.TypeFor[caseTwoStrict](), []string{"NAME", "name"}},
		{"a strict member promoted from an embedded struct", reflect.TypeFor[caseStrictPromoted](), []string{"Name", "own"}},
		{"a strict member with no name of its own", reflect.TypeFor[caseStrictUntagged](), []string{"Plain", "Strict"}},
		{"a pointer", reflect.TypeFor[*caseFoldedOnly](), []string{"name"}},
		{"a name that ends at a quote and one that is only quoted", reflect.TypeFor[tagEndsAtQuote](), []string{"B", "a"}},
		{"a field left out by a dash", reflect.TypeFor[tagDash](), []string{"b"}},
		{"a field named dash", reflect.TypeFor[tagDashComma](), []string{"-", "b"}},
		{"a promoted struct", reflect.TypeFor[tagPromoted](), []string{"deep", "own"}},
		{"renamed, left out, named dash, untagged, omitempty and unexported", reflect.TypeFor[mixed](), []string{"-", "Optional", "Untagged", "renamed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonKeyNames(tc.typ)
			if got == nil {
				t.Fatal("nil for a struct")
			}
			var names []string
			for name := range got {
				names = append(names, name)
			}
			sort.Strings(names)
			if !reflect.DeepEqual(names, tc.names) {
				t.Errorf("names = %v, want %v", names, tc.names)
			}
		})
	}
}

// DecodeExact calls a response a repetition of one member under names that
// differ in case only when the decoder puts both keys in the one member. A
// member tagged case:strict takes only its own spelling, so a key that differs
// from it is another matter (here, one the type does not have), and the
// response is not said to repeat anything. Every case is checked against what
// encoding/json fills.
func TestDecodeExactCallsAKeyARepetitionOnlyWhereTheMemberMatchesWithoutRegardToCase(t *testing.T) {
	const repeats = "more than once, under names that differ only in the case"
	cases := []struct {
		name    string
		target  func() any
		body    string
		repeats bool
		accepts bool
	}{
		{"a folded member, two spellings", func() any { return &caseFoldedOnly{} }, `{"name":"a","NAME":"b"}`, true, false},
		{"a folded member, the other order", func() any { return &caseFoldedOnly{} }, `{"NAME":"a","name":"b"}`, true, false},
		{"a folded member, three spellings", func() any { return &caseFoldedOnly{} }, `{"name":"a","Name":"b","NAME":"c"}`, true, false},
		{"a case:ignore member", func() any { return &caseIgnored{} }, `{"Name":"a","name":"b"}`, true, false},
		{"a strict member, its own spelling and another", func() any { return &caseStrictOnly{} }, `{"Name":"a","name":"b"}`, false, false},
		{"a strict member, the other order", func() any { return &caseStrictOnly{} }, `{"name":"a","Name":"b"}`, false, false},
		{"a strict member beside a folded one", func() any { return &caseStrictAndFolded{} }, `{"Name":"a","NAME":"b","other":"c"}`, false, false},
		{"two strict members that differ in case", func() any { return &caseTwoStrict{} }, `{"name":"a","NAME":"b"}`, false, true},
		{"a strict member and a folded one that differ in case", func() any { return &caseStrictBesideFolded{} }, `{"name":"a","NAME":"b"}`, false, true},
		{"the folded one given two spellings beside a strict one", func() any { return &caseStrictBesideFolded{} }, `{"name":"a","NAME":"b","Name":"c"}`, true, false},
		{"a strict member given only its own spelling", func() any { return &caseStrictOnly{} }, `{"Name":"a"}`, false, true},
		{"a folded member given one spelling", func() any { return &caseFoldedOnly{} }, `{"NAME":"a"}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := DecodeExact(tc.body, tc.target(), DecodeLimits{})
			if (err == nil) != tc.accepts {
				t.Fatalf("DecodeExact = %v, want accepted = %v", err, tc.accepts)
			}
			said := err != nil && strings.Contains(err.Error(), repeats)
			if said != tc.repeats {
				t.Errorf("the repetition is said = %v, want %v (%v)", said, tc.repeats, err)
			}

			// What encoding/json fills says which it was: a repetition is two keys in
			// one member, which leaves the later one's value.
			plain := tc.target()
			if err := json.Unmarshal([]byte(tc.body), plain); err != nil {
				t.Fatalf("encoding/json refuses the body: %v", err)
			}
			var object map[string]string
			_ = json.Unmarshal([]byte(tc.body), &object)
			into := reflect.ValueOf(plain).Elem()
			filled := 0
			for i := 0; i < into.NumField(); i++ {
				if !into.Field(i).IsZero() {
					filled++
				}
			}
			keys := len(object)
			if tc.repeats && filled >= keys {
				t.Errorf("the case says a member was repeated, but encoding/json filled %d members from %d keys", filled, keys)
			}
		})
	}
}

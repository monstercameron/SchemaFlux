package ops

//lint:file-ignore U1000 unexported fields are here to prove the generator skips them

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type schemaContact struct {
	Name    string   `json:"name"`
	Age     int      `json:"age"`
	Active  bool     `json:"active"`
	Score   float64  `json:"score"`
	Tags    []string `json:"tags"`
	Note    string   `json:"note,omitempty"`
	private string
	Skipped string `json:"-"`
}

func schemaOf(t *testing.T, value any) map[string]any {
	t.Helper()
	schema := GenerateJSONSchema(reflect.TypeOf(value))
	if schema == nil {
		t.Fatalf("no schema was produced for %T", value)
	}
	return schema
}

// Strict mode has rules, and a schema that breaks them is rejected by the API —
// which is worse than sending none, because the call fails instead of degrading.
func TestJSONSchemaSatisfiesStrictModeRules(t *testing.T) {
	schema := schemaOf(t, schemaContact{})

	t.Run("is_an_object", func(t *testing.T) {
		if schema["type"] != "object" {
			t.Errorf("type = %v", schema["type"])
		}
	})

	t.Run("additional_properties_are_refused", func(t *testing.T) {
		if schema["additionalProperties"] != false {
			t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
		}
	})

	t.Run("every_property_is_required", func(t *testing.T) {
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]string)

		if len(properties) != len(required) {
			t.Fatalf("%d properties but %d required: strict mode needs every property listed",
				len(properties), len(required))
		}
		for _, name := range required {
			if _, ok := properties[name]; !ok {
				t.Errorf("%q is required but not a property", name)
			}
		}
	})

	t.Run("optional_fields_permit_null", func(t *testing.T) {
		properties, _ := schema["properties"].(map[string]any)
		note, _ := properties["note"].(map[string]any)
		types, ok := note["type"].([]string)
		if !ok {
			t.Fatalf("note type = %v; an omitempty field must permit null", note["type"])
		}
		var sawNull bool
		for _, entry := range types {
			if entry == "null" {
				sawNull = true
			}
		}
		if !sawNull {
			t.Errorf("note type = %v, want it to include null", types)
		}
	})

	t.Run("excluded_and_unexported_fields_are_absent", func(t *testing.T) {
		properties, _ := schema["properties"].(map[string]any)
		for _, absent := range []string{"private", "Skipped", "-"} {
			if _, present := properties[absent]; present {
				t.Errorf("%q is in the schema", absent)
			}
		}
	})
}

// Go types map to the JSON types a model can produce.
func TestJSONSchemaLeafTypes(t *testing.T) {
	schema := schemaOf(t, schemaContact{})
	properties, _ := schema["properties"].(map[string]any)

	cases := map[string]string{
		"name":   "string",
		"age":    "integer",
		"active": "boolean",
		"score":  "number",
	}

	for field, want := range cases {
		t.Run(field, func(t *testing.T) {
			entry, _ := properties[field].(map[string]any)
			if entry["type"] != want {
				t.Errorf("%s type = %v, want %v", field, entry["type"], want)
			}
		})
	}

	t.Run("slice_is_an_array_of_its_element", func(t *testing.T) {
		tags, _ := properties["tags"].(map[string]any)
		if tags["type"] != "array" {
			t.Fatalf("tags type = %v", tags["type"])
		}
		items, _ := tags["items"].(map[string]any)
		if items["type"] != "string" {
			t.Errorf("tags items = %v", items["type"])
		}
	})
}

// Nesting is expressed, which is what makes the schema worth sending.
func TestJSONSchemaExpressesNesting(t *testing.T) {
	type address struct {
		City string `json:"city"`
	}
	type person struct {
		Name    string    `json:"name"`
		Home    address   `json:"home"`
		Visited []address `json:"visited"`
	}

	schema := schemaOf(t, person{})
	properties, _ := schema["properties"].(map[string]any)

	home, _ := properties["home"].(map[string]any)
	if home["type"] != "object" {
		t.Errorf("home is not an object: %v", home)
	}
	homeProps, _ := home["properties"].(map[string]any)
	if _, ok := homeProps["city"]; !ok {
		t.Errorf("the nested city field is missing: %v", homeProps)
	}

	visited, _ := properties["visited"].(map[string]any)
	items, _ := visited["items"].(map[string]any)
	if items["type"] != "object" {
		t.Errorf("visited items are not objects: %v", items)
	}
}

// time.Time is a formatted string, not an expanded struct.
func TestJSONSchemaRendersTimeAsADateTime(t *testing.T) {
	type event struct {
		At time.Time `json:"at"`
	}

	schema := schemaOf(t, event{})
	properties, _ := schema["properties"].(map[string]any)
	at, _ := properties["at"].(map[string]any)

	if at["type"] != "string" || at["format"] != "date-time" {
		t.Errorf("at = %v, want a date-time string", at)
	}
}

// A type strict mode cannot express produces no schema, so the caller falls
// back to prompt-only rather than sending something the API will reject.
func TestJSONSchemaDeclinesTypesItCannotExpress(t *testing.T) {
	type withMap struct {
		Values map[string]int `json:"values"`
	}
	type node struct {
		Value string `json:"value"`
		Next  *node  `json:"next,omitempty"`
	}
	type empty struct{}

	cases := []struct {
		name  string
		value any
	}{
		{"map", withMap{}},
		{"recursive", node{}},
		{"no_fields", empty{}},
		{"bare_string", ""},
		{"bare_int", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := GenerateJSONSchema(reflect.TypeOf(tc.value))
			if tc.name == "bare_string" || tc.name == "bare_int" {
				// A scalar has a valid schema; it is just not an object.
				if schema == nil {
					t.Skip("scalars are expressible")
				}
				return
			}
			if schema != nil {
				t.Errorf("a %s type produced a schema strict mode cannot use: %v", tc.name, schema)
			}
		})
	}
}

// Embedded structs are flattened, as encoding/json promotes them, or the
// schema would not match the JSON the model must produce.
func TestJSONSchemaFlattensEmbeddedStructs(t *testing.T) {
	type base struct {
		ID string `json:"id"`
	}
	type derived struct {
		base
		Name string `json:"name"`
	}

	schema := schemaOf(t, derived{})
	properties, _ := schema["properties"].(map[string]any)

	for _, want := range []string{"id", "name"} {
		if _, ok := properties[want]; !ok {
			t.Errorf("%q is missing from the flattened schema: %v", want, properties)
		}
	}
	if _, present := properties["base"]; present {
		t.Error("the embedded type appears as a property")
	}
}

// The schema has to survive the round trip to the wire.
func TestJSONSchemaMarshals(t *testing.T) {
	schema := schemaOf(t, schemaContact{})

	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("the schema does not marshal: %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the marshalled schema does not parse: %v", err)
	}
	if back["type"] != "object" {
		t.Errorf("the round trip lost the type: %v", back["type"])
	}
}

// The schema name has to be one the API accepts.
func TestSchemaNameFor(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{schemaContact{}, "schemaContact"},
		{&schemaContact{}, "schemaContact"},
		{struct{ A int }{}, "result"},
		{[]schemaContact{}, "result"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := schemaNameFor(reflect.TypeOf(tc.value))
			if got != tc.want {
				t.Errorf("schemaNameFor(%T) = %q, want %q", tc.value, got, tc.want)
			}
			for _, r := range got {
				valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
					(r >= '0' && r <= '9') || r == '_'
				if !valid {
					t.Errorf("%q contains %q, which the API does not accept", got, r)
				}
			}
		})
	}
}

// Types that embed themselves. selfEmbedding, which embeds a pointer to itself, is
// declared in answerfields_test.go.
//
// encoding/json reads a struct that embeds a pointer to itself, because it lists
// the promoted fields of each type once. A schema generator that follows
// embedded structs without remembering which ones it is inside never finishes on
// one: it overflows the stack, which no recover can catch.

type SelfEmbeddingExported struct {
	*SelfEmbeddingExported
	X int `json:"x"`
}

type selfEmbeddingByValueInASlice struct {
	Children []selfEmbeddingByValueInASlice `json:"children"`
	X        int                            `json:"x"`
}

type embeddingRingFirst struct {
	*embeddingRingSecond
	A int `json:"a"`
}

type embeddingRingSecond struct {
	*embeddingRingFirst
	B int `json:"b"`
}

type embeddingRingOfThreeFirst struct {
	*embeddingRingOfThreeSecond
	A int `json:"a"`
}

type embeddingRingOfThreeSecond struct {
	*embeddingRingOfThreeThird
	B int `json:"b"`
}

type embeddingRingOfThreeThird struct {
	*embeddingRingOfThreeFirst
	C int `json:"c"`
}

// embeddingARing is not in the ring, and embeds a type that is.
type embeddingARing struct {
	embeddingRingFirst
	Own string `json:"own"`
}

// The same type embedded through two routes is a diamond and not a cycle.
type diamondBase struct {
	ID string `json:"id"`
}

type diamondLeft struct {
	diamondBase
	Left string `json:"left"`
}

type diamondRight struct {
	diamondBase
	Right string `json:"right"`
}

type diamondTop struct {
	diamondLeft
	diamondRight
}

// An embedded type that refers back to its embedder through a named field is a
// recursive type, which the generator already declines; it is not an embedding
// cycle.
type embeddedWithAParent struct {
	embeddedParentLink
	Name string `json:"name"`
}

type embeddedParentLink struct {
	Parent *embeddedWithAParent `json:"parent"`
	Age    int                  `json:"age"`
}

// A finite chain of embedded structs, twelve deep, each adding a field of its own.
type embeddedChain1 struct {
	embeddedChain2
	Field1 string `json:"field1"`
}

type embeddedChain2 struct {
	embeddedChain3
	Field2 string `json:"field2"`
}

type embeddedChain3 struct {
	embeddedChain4
	Field3 string `json:"field3"`
}

type embeddedChain4 struct {
	embeddedChain5
	Field4 string `json:"field4"`
}

type embeddedChain5 struct {
	embeddedChain6
	Field5 string `json:"field5"`
}

type embeddedChain6 struct {
	embeddedChain7
	Field6 string `json:"field6"`
}

type embeddedChain7 struct {
	embeddedChain8
	Field7 string `json:"field7"`
}

type embeddedChain8 struct {
	embeddedChain9
	Field8 string `json:"field8"`
}

type embeddedChain9 struct {
	embeddedChain10
	Field9 string `json:"field9"`
}

type embeddedChain10 struct {
	embeddedChain11
	Field10 string `json:"field10"`
}

type embeddedChain11 struct {
	embeddedChain12
	Field11 string `json:"field11"`
}

type embeddedChain12 struct {
	Field12 string `json:"field12"`
}

// embeddingCycles are the types the schema cannot list the promoted fields of.
func embeddingCycles() map[string]reflect.Type {
	return map[string]reflect.Type{
		"a pointer to itself":                  reflect.TypeFor[selfEmbedding](),
		"a pointer to itself, exported":        reflect.TypeFor[SelfEmbeddingExported](),
		"a pointer to the type":                reflect.TypeFor[*selfEmbedding](),
		"a slice of it":                        reflect.TypeFor[[]selfEmbedding](),
		"itself through a second type":         reflect.TypeFor[embeddingRingFirst](),
		"the second type of that ring":         reflect.TypeFor[embeddingRingSecond](),
		"itself through two other types":       reflect.TypeFor[embeddingRingOfThreeFirst](),
		"the third type of that ring":          reflect.TypeFor[embeddingRingOfThreeThird](),
		"a type that embeds a ring":            reflect.TypeFor[embeddingARing](),
		"a slice of types that embed a ring":   reflect.TypeFor[[]embeddingARing](),
		"a pointer to a type that embeds ring": reflect.TypeFor[*embeddingARing](),
	}
}

func TestJSONSchemaOfATypeThatEmbedsItselfIsDeclinedAndNotFollowedForever(t *testing.T) {
	for name, target := range embeddingCycles() {
		t.Run(name, func(t *testing.T) {
			if schema := GenerateJSONSchema(target); schema != nil {
				t.Errorf("a schema was written for %v, whose promoted fields cannot be listed: %v", target, schema)
			}
		})
	}
}

func TestJSONSchemaStillExpressesTypesThatOnlyLookLikeCycles(t *testing.T) {
	t.Run("a chain of twelve embedded structs", func(t *testing.T) {
		schema := GenerateJSONSchema(reflect.TypeFor[embeddedChain1]())
		if schema == nil {
			t.Fatal("no schema for a finite chain of embedded structs")
		}
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]string)
		if len(properties) != 12 || len(required) != 12 {
			t.Errorf("%d properties and %d required of the twelve fields: %v", len(properties), len(required), properties)
		}
		for level := 1; level <= 12; level++ {
			name := fmt.Sprintf("field%d", level)
			if _, present := properties[name]; !present {
				t.Errorf("%s is missing from the flattened schema", name)
			}
		}
	})

	t.Run("a pointer to the chain", func(t *testing.T) {
		if GenerateJSONSchema(reflect.TypeFor[*embeddedChain1]()) == nil {
			t.Error("no schema for a pointer to the chain")
		}
	})

	t.Run("a slice of the chain", func(t *testing.T) {
		if GenerateJSONSchema(reflect.TypeFor[[]embeddedChain1]()) == nil {
			t.Error("no schema for a slice of the chain")
		}
	})

	t.Run("one type embedded through two routes", func(t *testing.T) {
		schema := GenerateJSONSchema(reflect.TypeFor[diamondTop]())
		if schema == nil {
			t.Fatal("no schema for a type that embeds the same type through two routes")
		}
		// id is promoted from the same struct by both routes, at the same depth,
		// which makes it ambiguous: encoding/json reads no field of that name, so
		// the schema declares none.
		properties, _ := schema["properties"].(map[string]any)
		for _, name := range []string{"left", "right"} {
			if _, present := properties[name]; !present {
				t.Errorf("%s is missing from the schema: %v", name, properties)
			}
		}
		if _, present := properties["id"]; present {
			t.Errorf("id is declared, and encoding/json drops it as ambiguous: %v", properties)
		}
	})

	t.Run("an embedded type that refers back through a named field", func(t *testing.T) {
		if schema := GenerateJSONSchema(reflect.TypeFor[embeddedWithAParent]()); schema != nil {
			t.Errorf("a recursive type got a schema: %v", schema)
		}
	})

	t.Run("a recursive type with no embedding", func(t *testing.T) {
		if schema := GenerateJSONSchema(reflect.TypeFor[selfEmbeddingByValueInASlice]()); schema != nil {
			t.Errorf("a recursive type got a schema: %v", schema)
		}
	})
}

// Which members a struct type has is decided by the Go rules for embedded
// structs, which encoding/json applies and which the schema has to apply the same
// way: a name that is reached by two routes at the same depth is not a field at
// all, a name at a shallower depth hides one at a deeper depth, and at the same
// depth a name taken from a json tag beats one that is not.

// A name reached through the same struct by two routes, at the same depth, is
// diamondBase's ID in diamondTop. Where the outer struct declares the name itself,
// that one is read.
type diamondDeclaredByTop struct {
	diamondLeft
	diamondRight
	ID int `json:"id"`
}

// diamondWrap puts a struct one level deeper, so that diamondAtTwoDepths reaches
// diamondBase at two depths and not two routes of the same one.
type diamondWrap struct {
	diamondBase
}

type diamondDeepRoute struct {
	diamondWrap
}

type diamondAtTwoDepths struct {
	diamondLeft
	diamondDeepRoute
}

type twinNameFirst struct {
	Name string `json:"name"`
	A    int    `json:"a"`
}

type twinNameSecond struct {
	Name string `json:"name"`
	B    int    `json:"b"`
}

// twinNamesTop embeds two different structs that each declare name. Each is
// embedded through a struct of its own, which puts the names at the same depth
// and out of reach of go vet's check for repeated tags: that check does not look
// through an unexported embedded field, and the type is ambiguous on purpose.
type twinNamesTop struct {
	twinNameFirstRoute
	twinNameSecondRoute
}

type twinNameFirstRoute struct {
	twinNameFirst
}

type twinNameSecondRoute struct {
	twinNameSecond
}

// outerNameHidesTwin declares name itself, which hides the embedded one.
type outerNameHidesTwin struct {
	twinNameFirst
	Name bool `json:"name"`
}

type taggedNameHolder struct {
	Count int `json:"Name"`
}

type untaggedNameHolder struct {
	Name string
}

// taggedBeatsUntaggedTop reaches Name twice at the same depth, once from a json
// tag and once from the Go name; the tagged one is read.
type taggedBeatsUntaggedTop struct {
	taggedNameHolder
	untaggedNameHolder
}

type secondTaggedNameHolder struct {
	Flag bool `json:"Name"`
}

// twoTaggedNamesTop reaches Name twice at the same depth, from a tag both times,
// which is ambiguous.
type twoTaggedNamesTop struct {
	taggedNameRoute
	secondTaggedNameRoute
	Other string `json:"other"`
}

type taggedNameRoute struct {
	taggedNameHolder
}

type secondTaggedNameRoute struct {
	secondTaggedNameHolder
}

// outerUntaggedHidesTagged declares Name by its Go name, one level above a field
// that carries it as a json tag: the shallower one is read although it is not
// tagged.
type outerUntaggedHidesTagged struct {
	taggedNameHolder
	Name float64
}

type deepNameHolder struct {
	Name string `json:"name"`
	Q    int    `json:"q"`
}

type deepNameWrapper struct {
	deepNameHolder
}

// shallowBeatsDeepTop has name one level down and again two levels down.
type shallowBeatsDeepTop struct {
	twinNameFirst
	deepNameWrapper
}

type soleNameFirst struct {
	Name string `json:"name"`
}

type soleNameSecond struct {
	Name string `json:"name"`
}

// soleNamesTop has a name and no other member, and the name is ambiguous.
type soleNamesTop struct {
	soleNameFirstRoute
	soleNameSecondRoute
}

type soleNameFirstRoute struct {
	soleNameFirst
}

type soleNameSecondRoute struct {
	soleNameSecond
}

// PlainEmbedded is exported so that a tag on the field that embeds it is the
// only thing that differs from an ordinary embedding.
type PlainEmbedded struct {
	Plain string `json:"plain"`
}

// A tag with options and no name leaves the embedded struct promoted.
type optionsOnlyTagEmbedding struct {
	PlainEmbedded `json:",omitempty"`
	Z             int `json:"z"`
}

// A tag with a name makes the embedded struct a field of that name, an object.
type namedTagEmbedding struct {
	PlainEmbedded `json:"p"`
	Z             int `json:"z"`
}

type Label string

// labelEmbedding embeds a type that is not a struct, which is a field named for
// the type.
type labelEmbedding struct {
	Label
	K int `json:"k"`
}

type hiddenLabel string

// hiddenLabelEmbedding embeds an unexported type that is not a struct, which
// encoding/json leaves out.
type hiddenLabelEmbedding struct {
	hiddenLabel
	K int `json:"k"`
}

// A tag of "-" leaves an embedded struct out with everything it promotes.
type omittedEmbedding struct {
	PlainEmbedded `json:"-"`
	Z             int `json:"z"`
}

// An embedded struct whose tag has a name encoding/json does not accept has no
// name, and is promoted. A name that begins with a quote is not accepted, where
// one that holds a quote or a backslash after an identifier is read up to it
// (truncatedTagEmbedding).
type invalidTagEmbedding struct {
	PlainEmbedded `json:"'b'"`
	Z             int `json:"z"`
}

// A tag name that encoding/json does not accept is not used: the Go name is.
type invalidTagName struct {
	Text string `json:"'b'"`
	N    int    `json:"n"`
}

// A name that runs into a backslash is the identifier before it, which is a name
// of the field, so an embedded struct under it is a field and is not promoted.
type truncatedTagEmbedding struct {
	PlainEmbedded `json:"a\\b"`
	Z             int `json:"z"`
}

type truncatedTagName struct {
	Text string `json:"a\\b"`
	N    int    `json:"n"`
}

// The option embed promotes the members of a struct that is not embedded in the
// Go sense, when the tag gives no name.
type embedOptionOnNamedStruct struct {
	In PlainEmbedded `json:",embed"`
	Z  int           `json:"z"`
}

// A name and the option embed together are a field with that name.
type embedOptionWithAName struct {
	In PlainEmbedded `json:"in,embed"`
	Z  int           `json:"z"`
}

// Options beside embed do not stop the promotion when there is no name.
type embedOptionBesideOthers struct {
	In PlainEmbedded `json:",omitempty,embed"`
	Z  int           `json:"z"`
}

type embedOptionAfterACase struct {
	In PlainEmbedded `json:",case:ignore,embed"`
	Z  int           `json:"z"`
}

// A map that embeds takes the members the type does not name, and is not a
// member itself.
type embedOptionOnMap struct {
	Extra map[string]int `json:",embed"`
	Z     int            `json:"z"`
}

// The option embed on a field that is neither a struct nor a map changes
// nothing: the field is read under its Go name.
type embedOptionOnInteger struct {
	Count int `json:",embed"`
	Z     int `json:"z"`
}

type unexportedBase struct {
	Exp      string `json:"exp"`
	internal string
}

// unexportedEmbeddedByValue embeds a struct whose type is not exported. Its
// exported fields are promoted all the same.
type unexportedEmbeddedByValue struct {
	unexportedBase
	Own string `json:"own"`
}

type unexportedEmbeddedOnly struct {
	unexportedBase
}

// unexportedEmbeddedByPointer embeds a pointer to a struct whose type is not
// exported: encoding/json writes its fields, and cannot allocate the pointer to
// read them.
type unexportedEmbeddedByPointer struct {
	*unexportedBase
	Own string `json:"own"`
}

// PointerHolder is exported and holds the unexported pointer, so that the
// pointer is reached through an exported embedding.
type PointerHolder struct {
	*unexportedBase
	T string `json:"t"`
}

type exportedEmbedHoldingPointer struct {
	PointerHolder
}

type chainEnd struct {
	Deep string `json:"deep"`
}

type chainMiddle struct {
	chainEnd
}

// pointerAboveAChain has its member two structs below the pointer that cannot be
// allocated.
type pointerAboveAChain struct {
	*chainMiddle
	N string `json:"n"`
}

type hiddenOnlyBase struct {
	Skipped  string `json:"-"`
	internal string
}

// unexportedPointerPromotingNothing embeds an unexported pointer that has no
// field encoding/json reads, so the pointer never has to be allocated.
type unexportedPointerPromotingNothing struct {
	*hiddenOnlyBase
	N string `json:"n"`
}

type pointerInsideUnexported struct {
	*PlainEmbedded
	R string `json:"r"`
}

// unexportedHoldingExportedPointer has a pointer to an exported struct inside a
// struct that is not exported; that pointer can be allocated.
type unexportedHoldingExportedPointer struct {
	pointerInsideUnexported
	S string `json:"s"`
}

// jsonKeysInOrder is the keys of the object raw holds, in the order they are
// written.
func jsonKeysInOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("%s is not an object: %v", raw, err)
	}
	keys := []string{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("reading the keys of %s: %v", raw, err)
		}
		keys = append(keys, token.(string))
		var skipped json.RawMessage
		if err := decoder.Decode(&skipped); err != nil {
			t.Fatalf("reading a value in %s: %v", raw, err)
		}
	}
	return keys
}

// embeddingFact is what encoding/json does with one struct type, which the
// generators are held to.
type embeddingFact struct {
	name   string
	target reflect.Type
	// members are the names encoding/json writes for a zero value of the type,
	// in the order it writes them, which is the order the schema lists them in.
	members []string
	// kinds is the JSON type the schema gives a name, for the names whose Go
	// types differ between the fields that compete for them.
	kinds map[string]string
	// input is an object offered to the decoder that names the members and the
	// names that are not members, and output is what the value holds afterwards,
	// written again.
	input  string
	output string
}

func embeddingFacts() []embeddingFact {
	return []embeddingFact{
		{
			name: "one struct embedded by two routes", target: reflect.TypeFor[diamondTop](),
			members: []string{"left", "right"},
			input:   `{"id":"I","left":"L","right":"R"}`, output: `{"left":"L","right":"R"}`,
		},
		{
			name: "that name declared again by the outer struct", target: reflect.TypeFor[diamondDeclaredByTop](),
			members: []string{"left", "right", "id"}, kinds: map[string]string{"id": "integer"},
			input: `{"id":7,"left":"L"}`, output: `{"left":"L","right":"","id":7}`,
		},
		{
			name: "one struct reached at two depths", target: reflect.TypeFor[diamondAtTwoDepths](),
			members: []string{"id", "left"}, kinds: map[string]string{"id": "string"},
			input: `{"id":"I","left":"L"}`, output: `{"id":"I","left":"L"}`,
		},
		{
			name: "two structs that each declare a name", target: reflect.TypeFor[twinNamesTop](),
			members: []string{"a", "b"},
			input:   `{"name":"N","a":1,"b":2}`, output: `{"a":1,"b":2}`,
		},
		{
			name: "a name the outer struct declares over an embedded one", target: reflect.TypeFor[outerNameHidesTwin](),
			members: []string{"a", "name"}, kinds: map[string]string{"name": "boolean"},
			input: `{"name":true,"a":3}`, output: `{"a":3,"name":true}`,
		},
		{
			name: "a tagged name against an untagged one", target: reflect.TypeFor[taggedBeatsUntaggedTop](),
			members: []string{"Name"}, kinds: map[string]string{"Name": "integer"},
			input: `{"Name":5}`, output: `{"Name":5}`,
		},
		{
			name: "two tagged names", target: reflect.TypeFor[twoTaggedNamesTop](),
			members: []string{"other"},
			input:   `{"Name":5,"other":"o"}`, output: `{"other":"o"}`,
		},
		{
			name: "an untagged name above a tagged one", target: reflect.TypeFor[outerUntaggedHidesTagged](),
			members: []string{"Name"}, kinds: map[string]string{"Name": "number"},
			input: `{"Name":1.5}`, output: `{"Name":1.5}`,
		},
		{
			name: "a name at two depths", target: reflect.TypeFor[shallowBeatsDeepTop](),
			members: []string{"name", "a", "q"}, kinds: map[string]string{"name": "string"},
			input: `{"name":"N","a":1,"q":2}`, output: `{"name":"N","a":1,"q":2}`,
		},
		{
			name: "a name and nothing else, ambiguous", target: reflect.TypeFor[soleNamesTop](),
			members: []string{},
			input:   `{"name":"N"}`, output: `{}`,
		},
		{
			name: "a tag with options and no name", target: reflect.TypeFor[optionsOnlyTagEmbedding](),
			members: []string{"plain", "z"},
			input:   `{"plain":"P","z":1}`, output: `{"plain":"P","z":1}`,
		},
		{
			name: "a tag with a name", target: reflect.TypeFor[namedTagEmbedding](),
			members: []string{"p", "z"}, kinds: map[string]string{"p": "object"},
			input: `{"p":{"plain":"P"},"z":1}`, output: `{"p":{"plain":"P"},"z":1}`,
		},
		{
			name: "an embedded type that is not a struct", target: reflect.TypeFor[labelEmbedding](),
			members: []string{"Label", "k"}, kinds: map[string]string{"Label": "string"},
			input: `{"Label":"L","k":1}`, output: `{"Label":"L","k":1}`,
		},
		{
			name: "an unexported embedded type that is not a struct", target: reflect.TypeFor[hiddenLabelEmbedding](),
			members: []string{"k"},
			input:   `{"hiddenLabel":"H","k":1}`, output: `{"k":1}`,
		},
		{
			name: "a tag of a dash", target: reflect.TypeFor[omittedEmbedding](),
			members: []string{"z"},
			input:   `{"plain":"P","z":1}`, output: `{"z":1}`,
		},
		{
			name: "a tag with a name encoding/json does not accept on an embedded struct", target: reflect.TypeFor[invalidTagEmbedding](),
			members: []string{"plain", "z"},
			input:   `{"plain":"P","z":1}`, output: `{"plain":"P","z":1}`,
		},
		{
			name: "a tag name encoding/json does not accept", target: reflect.TypeFor[invalidTagName](),
			members: []string{"Text", "n"},
			input:   `{"Text":"T","n":1}`, output: `{"Text":"T","n":1}`,
		},
		{
			name: "a tag name that runs into a backslash on an embedded struct", target: reflect.TypeFor[truncatedTagEmbedding](),
			members: []string{"a", "z"}, kinds: map[string]string{"a": "object"},
			input: `{"a":{"plain":"P"},"z":1}`, output: `{"a":{"plain":"P"},"z":1}`,
		},
		{
			name: "a tag name that runs into a backslash", target: reflect.TypeFor[truncatedTagName](),
			members: []string{"a", "n"},
			input:   `{"a":"T","n":1}`, output: `{"a":"T","n":1}`,
		},
		{
			name: "the option embed on a struct that is not embedded", target: reflect.TypeFor[embedOptionOnNamedStruct](),
			members: []string{"plain", "z"},
			input:   `{"plain":"P","z":1,"In":{"plain":"X"}}`, output: `{"plain":"P","z":1}`,
		},
		{
			name: "the option embed with a name", target: reflect.TypeFor[embedOptionWithAName](),
			members: []string{"in", "z"}, kinds: map[string]string{"in": "object"},
			input: `{"in":{"plain":"P"},"z":1}`, output: `{"in":{"plain":"P"},"z":1}`,
		},
		{
			name: "the option embed beside another option", target: reflect.TypeFor[embedOptionBesideOthers](),
			members: []string{"plain", "z"},
			input:   `{"plain":"P","z":1}`, output: `{"plain":"P","z":1}`,
		},
		{
			name: "the option embed after a case option", target: reflect.TypeFor[embedOptionAfterACase](),
			members: []string{"plain", "z"},
			input:   `{"plain":"P","z":1}`, output: `{"plain":"P","z":1}`,
		},
		{
			name: "the option embed on a map", target: reflect.TypeFor[embedOptionOnMap](),
			members: []string{"z"},
			input:   `{"x":5,"z":1}`, output: `{"z":1,"x":5}`,
		},
		{
			name: "the option embed on an integer", target: reflect.TypeFor[embedOptionOnInteger](),
			members: []string{"Count", "z"},
			input:   `{"Count":5,"z":1}`, output: `{"Count":5,"z":1}`,
		},
		{
			name: "an unexported struct embedded by value", target: reflect.TypeFor[unexportedEmbeddedByValue](),
			members: []string{"exp", "own"},
			input:   `{"exp":"E","own":"O"}`, output: `{"exp":"E","own":"O"}`,
		},
		{
			name: "an unexported struct embedded by value and nothing else", target: reflect.TypeFor[unexportedEmbeddedOnly](),
			members: []string{"exp"},
			input:   `{"exp":"E"}`, output: `{"exp":"E"}`,
		},
		{
			name: "an ordinary embedding", target: reflect.TypeFor[diamondLeft](),
			members: []string{"id", "left"},
			input:   `{"id":"I","left":"L"}`, output: `{"id":"I","left":"L"}`,
		},
		{
			name: "a chain of twelve", target: reflect.TypeFor[embeddedChain1](),
			members: []string{"field12", "field11", "field10", "field9", "field8", "field7", "field6", "field5", "field4", "field3", "field2", "field1"},
		},
	}
}

// The facts the generators are held to are encoding/json's, so each is checked
// against encoding/json first: what it writes for a zero value of the type, and
// what a value of it holds after it has read an object that names members and
// names that are not.
func TestEncodingJSONReadsTheMembersTheEmbeddingFactsSay(t *testing.T) {
	for _, fact := range embeddingFacts() {
		t.Run(fact.name, func(t *testing.T) {
			zero := reflect.New(fact.target)
			raw, err := json.Marshal(zero.Interface())
			if err != nil {
				t.Fatalf("encoding/json cannot write the type: %v", err)
			}
			if got := jsonKeysInOrder(t, raw); !slices.Equal(got, fact.members) {
				t.Errorf("encoding/json writes %v, the facts say %v", got, fact.members)
			}
			if fact.input == "" {
				return
			}
			if err := json.Unmarshal([]byte(fact.input), zero.Interface()); err != nil {
				t.Fatalf("encoding/json cannot read %s: %v", fact.input, err)
			}
			after, _ := json.Marshal(zero.Interface())
			if string(after) != fact.output {
				t.Errorf("after reading %s the value is %s, the facts say %s", fact.input, after, fact.output)
			}
		})
	}
}

// The answer reader holds an answer to the type from the same members, so an
// answer that names each of them is read whole, and one that leaves out a
// member the type requires is refused.
func TestTheAnswerReaderRequiresEveryMemberEncodingJSONReads(t *testing.T) {
	for _, fact := range embeddingFacts() {
		schema := GenerateJSONSchema(fact.target)
		if schema == nil || len(fact.members) == 0 {
			continue
		}
		t.Run(fact.name, func(t *testing.T) {
			properties, _ := schema["properties"].(map[string]any)
			answer := func(skip string) string {
				object := map[string]any{}
				for _, name := range fact.members {
					if name != skip {
						object[name] = requiredFillFor(t, properties[name])
					}
				}
				raw, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}

			if err := decodeAnswer("test", answer(""), reflect.New(fact.target).Interface()); err != nil {
				t.Errorf("an answer that names every member was refused: %v", err)
			}
			for _, member := range jsonMembers(fact.target) {
				if !IsRequired(member.field) {
					continue
				}
				if err := decodeAnswer("test", answer(member.name), reflect.New(fact.target).Interface()); err == nil {
					t.Errorf("an answer without %s was read", member.name)
				}
			}
		})
	}
}

// declaredNames is what a schema declares for an object: the names in
// properties, sorted, and the names in required, as listed.
func declaredNames(t *testing.T, schema map[string]any) (properties, required []string) {
	t.Helper()
	declared, _ := schema["properties"].(map[string]any)
	for name := range declared {
		properties = append(properties, name)
	}
	slices.Sort(properties)
	required, _ = schema["required"].([]string)
	return properties, required
}

func TestJSONSchemaDeclaresExactlyTheMembersEncodingJSONReads(t *testing.T) {
	for _, fact := range embeddingFacts() {
		t.Run(fact.name, func(t *testing.T) {
			schema := GenerateJSONSchema(fact.target)
			if len(fact.members) == 0 {
				if schema != nil {
					t.Fatalf("a schema was written for a type with no member: %v", schema)
				}
				return
			}
			if schema == nil {
				t.Fatalf("no schema was written for a type with the members %v", fact.members)
			}

			properties, required := declaredNames(t, schema)
			if len(required) != len(fact.members) || !slices.Equal(required, fact.members) {
				t.Errorf("required = %v, the members are %v", required, fact.members)
			}
			seen := map[string]bool{}
			for _, name := range required {
				if seen[name] {
					t.Errorf("%q is listed in required more than once: %v", name, required)
				}
				seen[name] = true
			}
			wantProperties := slices.Clone(fact.members)
			slices.Sort(wantProperties)
			if !slices.Equal(properties, wantProperties) {
				t.Errorf("properties = %v, want %v", properties, wantProperties)
			}

			declared, _ := schema["properties"].(map[string]any)
			for name, kind := range fact.kinds {
				entry, _ := declared[name].(map[string]any)
				if entry["type"] != kind {
					t.Errorf("%s is declared as %v, want %s: the wrong field was taken for the name", name, entry["type"], kind)
				}
			}
		})
	}
}

// A schema is written for a struct and a name that is not a member is never in
// it, so the model is never asked for something encoding/json then drops.
func TestJSONSchemaDoesNotDeclareANameEncodingJSONDrops(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		absent string
	}{
		{"id reached by two routes", reflect.TypeFor[diamondTop](), "id"},
		{"name declared by two embedded structs", reflect.TypeFor[twinNamesTop](), "name"},
		{"name tagged twice at one depth", reflect.TypeFor[twoTaggedNamesTop](), "Name"},
		{"an unexported embedded type that is not a struct", reflect.TypeFor[hiddenLabelEmbedding](), "hiddenLabel"},
		{"a struct embedded under a tag with options only", reflect.TypeFor[optionsOnlyTagEmbedding](), "PlainEmbedded"},
		{"a tag name encoding/json does not accept", reflect.TypeFor[invalidTagName](), "'b'"},
		{"a tag name that runs into a backslash", reflect.TypeFor[truncatedTagName](), "Text"},
		{"a field promoted by the option embed", reflect.TypeFor[embedOptionOnNamedStruct](), "In"},
		{"a map that embeds", reflect.TypeFor[embedOptionOnMap](), "Extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := GenerateJSONSchema(tc.target)
			if schema == nil {
				t.Fatal("no schema")
			}
			properties, required := declaredNames(t, schema)
			if slices.Contains(properties, tc.absent) || slices.Contains(required, tc.absent) {
				t.Errorf("%q is declared: properties %v, required %v", tc.absent, properties, required)
			}
		})
	}
}

// Nothing in the schema of a type that has no embedding moves. The strings are
// what the generator wrote for these types before embedded structs were resolved
// the way encoding/json resolves them.
func TestJSONSchemaOfPlainNestedAndEmbeddedStructsIsUnchanged(t *testing.T) {
	type address struct {
		City string  `json:"city"`
		Zip  *string `json:"zip,omitempty"`
	}
	type plain struct {
		Name    string    `json:"name"`
		Age     int       `json:"age"`
		Score   float64   `json:"score"`
		Note    *string   `json:"note,omitempty"`
		Tags    []string  `json:"tags"`
		Born    time.Time `json:"born"`
		Skipped string    `json:"-"`
		Plain   string
		private string
	}
	type nested struct {
		Name    string    `json:"name"`
		Home    address   `json:"home"`
		Visited []address `json:"visited"`
		Work    *address  `json:"work"`
	}
	type Base struct {
		ID string `json:"id"`
	}
	type embeddedExported struct {
		Base
		Name string `json:"name"`
	}
	type base struct {
		ID string `json:"id"`
	}
	type embeddedUnexported struct {
		Name string `json:"name"`
		base
		Age *int `json:"age"`
	}
	_ = plain{private: ""}

	cases := []struct {
		name   string
		target reflect.Type
		want   string
	}{
		{"a plain struct", reflect.TypeFor[plain](),
			`{"additionalProperties":false,"properties":{"Plain":{"type":"string"},"age":{"type":"integer"},"born":{"format":"date-time","type":"string"},"name":{"type":"string"},"note":{"type":["string","null"]},"score":{"type":"number"},"tags":{"items":{"type":"string"},"type":"array"}},"required":["name","age","score","note","tags","born","Plain"],"type":"object"}`},
		{"a nested struct", reflect.TypeFor[nested](),
			`{"additionalProperties":false,"properties":{"home":{"additionalProperties":false,"properties":{"city":{"type":"string"},"zip":{"type":["string","null"]}},"required":["city","zip"],"type":"object"},"name":{"type":"string"},"visited":{"items":{"additionalProperties":false,"properties":{"city":{"type":"string"},"zip":{"type":["string","null"]}},"required":["city","zip"],"type":"object"},"type":"array"},"work":{"additionalProperties":false,"properties":{"city":{"type":"string"},"zip":{"type":["string","null"]}},"required":["city","zip"],"type":["object","null"]}},"required":["name","home","visited","work"],"type":"object"}`},
		{"a struct with one exported embedded struct", reflect.TypeFor[embeddedExported](),
			`{"additionalProperties":false,"properties":{"id":{"type":"string"},"name":{"type":"string"}},"required":["id","name"],"type":"object"}`},
		{"a struct with one unexported embedded struct", reflect.TypeFor[embeddedUnexported](),
			`{"additionalProperties":false,"properties":{"age":{"type":["integer","null"]},"id":{"type":"string"},"name":{"type":"string"}},"required":["name","id","age"],"type":"object"}`},
		{"a pointer to a nested struct", reflect.TypeFor[*nested](),
			`{"additionalProperties":false,"properties":{"home":{"additionalProperties":false,"properties":{"city":{"type":"string"},"zip":{"type":["string","null"]}},"required":["city","zip"],"type":"object"},"name":{"type":"string"},"visited":{"items":{"additionalProperties":false,"properties":{"city":{"type":"string"},"zip":{"type":["string","null"]}},"required":["city","zip"],"type":"object"},"type":"array"},"work":{"additionalProperties":false,"properties":{"city":{"type":"string"},"zip":{"type":["string","null"]}},"required":["city","zip"],"type":["object","null"]}},"required":["name","home","visited","work"],"type":["object","null"]}`},
		{"a slice of plain structs", reflect.TypeFor[[]plain](),
			`{"items":{"additionalProperties":false,"properties":{"Plain":{"type":"string"},"age":{"type":"integer"},"born":{"format":"date-time","type":"string"},"name":{"type":"string"},"note":{"type":["string","null"]},"score":{"type":"number"},"tags":{"items":{"type":"string"},"type":"array"}},"required":["name","age","score","note","tags","born","Plain"],"type":"object"},"type":"array"}`},
		{"a chain of twelve embedded structs", reflect.TypeFor[embeddedChain1](),
			`{"additionalProperties":false,"properties":{"field1":{"type":"string"},"field10":{"type":"string"},"field11":{"type":"string"},"field12":{"type":"string"},"field2":{"type":"string"},"field3":{"type":"string"},"field4":{"type":"string"},"field5":{"type":"string"},"field6":{"type":"string"},"field7":{"type":"string"},"field8":{"type":"string"},"field9":{"type":"string"}},"required":["field12","field11","field10","field9","field8","field7","field6","field5","field4","field3","field2","field1"],"type":"object"}`},
		{"an embedded struct that is not at the top", reflect.TypeFor[struct {
			Inner embeddedExported `json:"inner"`
		}](),
			`{"additionalProperties":false,"properties":{"inner":{"additionalProperties":false,"properties":{"id":{"type":"string"},"name":{"type":"string"}},"required":["id","name"],"type":"object"}},"required":["inner"],"type":"object"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := GenerateJSONSchema(tc.target)
			if schema == nil {
				t.Fatal("no schema")
			}
			raw, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tc.want {
				t.Errorf("the schema is\n%s\nwant\n%s", raw, tc.want)
			}
		})
	}
}

// encoding/json writes the fields of an unexported struct embedded by pointer and
// cannot read them into a pointer that is nil, because the pointer is not
// settable. A value that already holds the struct reads fine.
func TestEncodingJSONCannotAllocateAnUnexportedEmbeddedPointer(t *testing.T) {
	const refusal = "cannot set embedded pointer to unexported struct"

	written, err := json.Marshal(&unexportedEmbeddedByPointer{unexportedBase: &unexportedBase{Exp: "e"}, Own: "o"})
	if err != nil || string(written) != `{"exp":"e","own":"o"}` {
		t.Fatalf("marshal = %s, %v", written, err)
	}
	var empty unexportedEmbeddedByPointer
	if err := json.Unmarshal([]byte(`{"exp":"E","own":"O"}`), &empty); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Errorf("reading a promoted field into a nil pointer gave %v, want %q", err, refusal)
	}
	var onlyOwn unexportedEmbeddedByPointer
	if err := json.Unmarshal([]byte(`{"own":"O"}`), &onlyOwn); err != nil {
		t.Errorf("an object with no promoted field was refused: %v", err)
	}
	filled := unexportedEmbeddedByPointer{unexportedBase: &unexportedBase{}}
	if err := json.Unmarshal([]byte(`{"exp":"E","own":"O"}`), &filled); err != nil || filled.Exp != "E" {
		t.Errorf("a pointer that is already set was not read into: %v %+v", err, filled)
	}
	var viaExported exportedEmbedHoldingPointer
	if err := json.Unmarshal([]byte(`{"exp":"E","t":"T"}`), &viaExported); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Errorf("an unexported pointer inside an exported embedding gave %v, want %q", err, refusal)
	}
	var belowThePointer pointerAboveAChain
	if err := json.Unmarshal([]byte(`{"deep":"D","n":"N"}`), &belowThePointer); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Errorf("a field two structs below the pointer gave %v, want %q", err, refusal)
	}
	var nothingPromoted unexportedPointerPromotingNothing
	if err := json.Unmarshal([]byte(`{"n":"N"}`), &nothingPromoted); err != nil {
		t.Errorf("a pointer with no field to read was refused: %v", err)
	}
	var exportedInside unexportedHoldingExportedPointer
	if err := json.Unmarshal([]byte(`{"plain":"P","r":"R","s":"S"}`), &exportedInside); err != nil || exportedInside.PlainEmbedded == nil || exportedInside.Plain != "P" {
		t.Errorf("an exported pointer inside an unexported struct was not allocated: %v %+v", err, exportedInside)
	}
}

// A type encoding/json cannot decode into has no schema: the answer a strict
// provider would be made to write could not be read back.
func TestJSONSchemaIsNotWrittenForAnUnexportedEmbeddedPointerThatPromotesFields(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		want   bool
	}{
		{"an unexported embedded pointer", reflect.TypeFor[unexportedEmbeddedByPointer](), false},
		{"a pointer to such a type", reflect.TypeFor[*unexportedEmbeddedByPointer](), false},
		{"a slice of such types", reflect.TypeFor[[]unexportedEmbeddedByPointer](), false},
		{"a field of such a type", reflect.TypeFor[struct {
			Inner unexportedEmbeddedByPointer `json:"inner"`
		}](), false},
		{"the pointer inside an exported embedding", reflect.TypeFor[exportedEmbedHoldingPointer](), false},
		{"a field two structs below the pointer", reflect.TypeFor[pointerAboveAChain](), false},
		{"a pointer that promotes nothing", reflect.TypeFor[unexportedPointerPromotingNothing](), true},
		{"an exported pointer inside an unexported struct", reflect.TypeFor[unexportedHoldingExportedPointer](), true},
		{"the same struct by value", reflect.TypeFor[unexportedEmbeddedByValue](), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GenerateJSONSchema(tc.target) != nil; got != tc.want {
				t.Errorf("a schema is written = %v, want %v", got, tc.want)
			}
		})
	}
}

func jsonMemberNames(members []jsonMember) []string {
	names := []string{}
	for _, member := range members {
		names = append(names, member.name)
	}
	return names
}

func TestJSONMembersAreTheNamesEncodingJSONWrites(t *testing.T) {
	for _, fact := range embeddingFacts() {
		t.Run(fact.name, func(t *testing.T) {
			if got := jsonMemberNames(jsonMembers(fact.target)); !slices.Equal(got, fact.members) {
				t.Errorf("members = %v, encoding/json writes %v", got, fact.members)
			}
		})
	}

	// Types with no embedding, and the ways a field is left out.
	cases := []struct {
		name   string
		target reflect.Type
		want   []string
	}{
		{"a struct with an unexported field, a dash and an option", reflect.TypeFor[schemaContact](), []string{"name", "age", "active", "score", "tags", "note"}},
		{"a struct with no field", reflect.TypeFor[struct{}](), []string{}},
		{"a struct holding only an unexported field", reflect.TypeFor[noExportedFields](), []string{}},
		{"an embedded time", reflect.TypeFor[struct {
			time.Time
			Name string `json:"name"`
		}](), []string{"name"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonMemberNames(jsonMembers(tc.target)); !slices.Equal(got, tc.want) {
				t.Errorf("members = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJSONMembersRecordTheWayToEachMember(t *testing.T) {
	cases := []struct {
		name    string
		target  reflect.Type
		member  string
		route   []string
		tagged  bool
		index   []int
		pointer []string
	}{
		{"a field of the struct", reflect.TypeFor[diamondLeft](), "left", nil, true, []int{1}, nil},
		{"a field with no tag", reflect.TypeFor[labelEmbedding](), "Label", nil, false, []int{0}, nil},
		{"a promoted field", reflect.TypeFor[diamondLeft](), "id", []string{"diamondBase"}, true, []int{0, 0}, nil},
		{"through an unexported struct", reflect.TypeFor[unexportedEmbeddedByValue](), "exp", []string{"unexportedBase"}, true, []int{0, 0}, nil},
		{"through a tag with options only", reflect.TypeFor[optionsOnlyTagEmbedding](), "plain", []string{"PlainEmbedded"}, true, []int{0, 0}, nil},
		{"a tagged name against an untagged one", reflect.TypeFor[taggedBeatsUntaggedTop](), "Name", []string{"taggedNameHolder"}, true, []int{0, 0}, nil},
		{"the shallower of two depths", reflect.TypeFor[shallowBeatsDeepTop](), "name", []string{"twinNameFirst"}, true, []int{0, 0}, nil},
		{"the one the outer struct declares", reflect.TypeFor[outerNameHidesTwin](), "name", nil, true, []int{1}, nil},
		{"through a pointer that cannot be allocated", reflect.TypeFor[unexportedEmbeddedByPointer](), "exp", []string{"unexportedBase"}, true, []int{0, 0}, []string{"unexportedBase"}},
		{"through an exported struct that holds one", reflect.TypeFor[exportedEmbedHoldingPointer](), "exp", []string{"PointerHolder", "unexportedBase"}, true, []int{0, 0, 0}, []string{"PointerHolder", "unexportedBase"}},
		{"below a pointer that cannot be allocated", reflect.TypeFor[pointerAboveAChain](), "deep", []string{"chainMiddle", "chainEnd"}, true, []int{0, 0, 0}, []string{"chainMiddle"}},
		{"through an exported pointer inside an unexported struct", reflect.TypeFor[unexportedHoldingExportedPointer](), "plain", []string{"pointerInsideUnexported", "PlainEmbedded"}, true, []int{0, 0, 0}, nil},
		{"the last of a chain", reflect.TypeFor[embeddedChain1](), "field12", []string{
			"embeddedChain2", "embeddedChain3", "embeddedChain4", "embeddedChain5", "embeddedChain6", "embeddedChain7",
			"embeddedChain8", "embeddedChain9", "embeddedChain10", "embeddedChain11", "embeddedChain12"}, true, []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var found *jsonMember
			for _, member := range jsonMembers(tc.target) {
				if member.name == tc.member {
					found = &member
				}
			}
			if found == nil {
				t.Fatalf("%s is not a member", tc.member)
			}
			if !slices.Equal(found.route, tc.route) {
				t.Errorf("route = %v, want %v", found.route, tc.route)
			}
			if found.tagged != tc.tagged {
				t.Errorf("tagged = %v, want %v", found.tagged, tc.tagged)
			}
			if !slices.Equal(found.index, tc.index) {
				t.Errorf("index = %v, want %v", found.index, tc.index)
			}
			if tc.pointer == nil && found.unexportedPointer != nil {
				t.Errorf("a pointer that cannot be allocated is recorded: %v", found.unexportedPointer.route)
			}
			if tc.pointer != nil && (found.unexportedPointer == nil || !slices.Equal(found.unexportedPointer.route, tc.pointer)) {
				t.Errorf("the pointer is %+v, want the route %v", found.unexportedPointer, tc.pointer)
			}
		})
	}
}

// jsonTagFact is what encoding/json does with a field of type int whose json tag
// is tag, which jsonTagName is held to.
type jsonTagFact struct {
	tag string
	// name is the name the tag gives the field, empty when it gives none and the
	// Go name is used.
	name string
	// omitted is true when the field is left out of JSON altogether.
	omitted bool
	// zeroLeftOut is true when a zero value is not written, which is the effect
	// of omitempty and of omitzero.
	zeroLeftOut bool
	// quoted is true when the number is written as a string.
	quoted bool
}

// jsonTagFacts are read off encoding/json, v1 as it is since Go 1.27, by
// TestEncodingJSONTagFacts. Under Go 1.26 the rows marked "1.26: ..." gave the
// Go name where they now give the name and the options the second column of
// this comment lists did not exist.
func jsonTagFacts() []jsonTagFact {
	return []jsonTagFact{
		// No name, or a plain one.
		{tag: ""},
		{tag: "name", name: "name"},
		{tag: "Name", name: "Name"},
		{tag: "name,omitempty", name: "name", zeroLeftOut: true},
		{tag: ",omitempty", zeroLeftOut: true},
		{tag: ",", name: ""},
		{tag: "a,", name: "a"},
		{tag: "a,b,c", name: "a"},
		{tag: "-", omitted: true},
		{tag: "-,", name: "-"},
		{tag: "-,omitempty", name: "-", zeroLeftOut: true},
		{tag: "-x", name: "-x"},

		// A name is the text up to a comma and takes most characters.
		{tag: "a b", name: "a b"},
		{tag: " name", name: " name"},
		{tag: "name ", name: "name "},
		{tag: "a.b", name: "a.b"},
		{tag: "a-b", name: "a-b"},
		{tag: "a/b", name: "a/b"},
		{tag: "a:b", name: "a:b"},
		{tag: "a=b", name: "a=b"},
		{tag: "a|b", name: "a|b"},
		{tag: "a^b", name: "a^b"},
		{tag: "a~b", name: "a~b"},
		{tag: "$x_1", name: "$x_1"},
		{tag: "1a", name: "1a"},
		{tag: "é", name: "é"},
		{tag: "é1", name: "é1"},
		{tag: "日本", name: "日本"},
		{tag: "a;b", name: "a;b"},
		{tag: "n;omitempty", name: "n;omitempty"},
		{tag: "n omitempty", name: "n omitempty"},
		{tag: "a\tb", name: "a\tb"},
		{tag: "a\nb", name: "a\nb"},
		{tag: "a\x00b", name: "a\x00b"},

		// 1.26: the Go name for each of these, which held a character it did not
		// accept. A name that ends at a backslash, a quote or a backquote is the
		// identifier before it.
		{tag: `a\b`, name: "a"},
		{tag: `a"b`, name: "a"},
		{tag: "a'b", name: "a"},
		{tag: "a`b", name: "a"},
		{tag: "a'b'c", name: "a"},
		{tag: "é'x", name: "é"},
		{tag: "a1_b'c", name: "a1_b"},
		{tag: "_x'", name: "_x"},
		{tag: "a b'c", name: "a"},
		{tag: "a.b'c", name: "a"},
		{tag: `a\b,omitempty`, name: "a", zeroLeftOut: true},
		{tag: `a\b,string`, name: "a", quoted: true},
		{tag: `a\b,c`, name: "a"},

		// A name that begins with a character the tag syntax reserves, or with
		// something that is not an identifier before one, is not a name.
		{tag: `\b`},
		{tag: `"b`},
		{tag: "'b'"},
		{tag: "'"},
		{tag: "`b"},
		{tag: "1a'b"},
		{tag: ".a'b"},
		{tag: `\x,omitempty`, zeroLeftOut: true},
		{tag: "'b',string", quoted: true},

		// Text that is not UTF-8 is replaced one byte at a time.
		{tag: "\xffz", name: "�z"},
		{tag: "a\xffb", name: "a�b"},

		// Options are identifiers; what follows one is another option.
		{tag: "n,omitzero", name: "n", zeroLeftOut: true},
		{tag: "n,omitempty,omitzero", name: "n", zeroLeftOut: true},
		{tag: "n,string", name: "n", quoted: true},
		{tag: ",string", quoted: true},
		{tag: "n,string,omitempty", name: "n", zeroLeftOut: true},
		{tag: "n,omitempty,string", name: "n", zeroLeftOut: true},
		{tag: "n,omitempty:x", name: "n", zeroLeftOut: true},
		{tag: "n,omitempty ", name: "n", zeroLeftOut: true},
		{tag: "n,omitempty-x", name: "n", zeroLeftOut: true},
		{tag: "n,omitzero ", name: "n", zeroLeftOut: true},
		{tag: "n,string:x", name: "n", quoted: true},
		{tag: "n,string ", name: "n", quoted: true},
		{tag: "n,omitempty1", name: "n"},
		{tag: "n,Omitempty", name: "n"},
		{tag: "n,omitEmpty", name: "n"},
		{tag: "n, omitempty", name: "n"},
		{tag: "n,'omitempty'", name: "n"},
		{tag: "n,case:ignore", name: "n"},
		{tag: "n,case:strict", name: "n"},
		{tag: "n,,omitempty", name: "n", zeroLeftOut: true},
	}
}

// jsonTagKeys reads what jsonTagStruct is written as: the key of the field that
// is tagged, its value as text, and whether it was written at all.
func jsonTagKeys(t *testing.T, raw []byte) (key, value string, present bool) {
	t.Helper()
	keys := jsonKeysInOrder(t, raw)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("%s is not an object: %v", raw, err)
	}
	for _, name := range keys {
		if name != "other" {
			return name, string(object[name]), true
		}
	}
	return "", "", false
}

func jsonTagStruct(tag string) reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "Field", Type: reflect.TypeFor[int](), Tag: reflect.StructTag(`json:` + strconv.Quote(tag))},
		{Name: "Other", Type: reflect.TypeFor[int](), Tag: `json:"other"`},
	})
}

// The facts jsonTagName is held to are encoding/json's, so each is checked
// against encoding/json first, as it is on the toolchain that runs the test: a
// release that changes how a tag is read fails here, and not in a schema.
func TestEncodingJSONTagFacts(t *testing.T) {
	for _, fact := range jsonTagFacts() {
		t.Run(fact.tag, func(t *testing.T) {
			raw, err := json.Marshal(reflect.New(jsonTagStruct(fact.tag)).Interface())
			if err != nil {
				t.Fatalf("encoding/json cannot write the type: %v", err)
			}
			key, value, present := jsonTagKeys(t, raw)

			wantKey := fact.name
			if wantKey == "" {
				wantKey = "Field"
			}
			wantPresent := !fact.omitted && !fact.zeroLeftOut
			if present != wantPresent {
				t.Fatalf("the zero value is written as %s, the facts say the field is present = %v", raw, wantPresent)
			}
			if !present {
				return
			}
			if key != wantKey {
				t.Errorf("encoding/json writes the key %q, the facts say %q", key, wantKey)
			}
			wantValue := "0"
			if fact.quoted {
				wantValue = `"0"`
			}
			if value != wantValue {
				t.Errorf("encoding/json writes the value %s, the facts say %s", value, wantValue)
			}
		})
	}
}

// v1 is v2 run with the options that keep v1's behaviour, and a tag is read the
// same way by both. v2 by itself refuses a type whose tag it finds malformed, at
// the first call, where v1 reads on, so a program that moved a decode to v2
// without the options would stop reading the same types.
func TestEncodingJSONV1ReadsATagAsV2DoesWithTheLegacyOptions(t *testing.T) {
	for _, fact := range jsonTagFacts() {
		t.Run(fact.tag, func(t *testing.T) {
			zero := reflect.New(jsonTagStruct(fact.tag)).Interface()
			one, err := json.Marshal(zero)
			if err != nil {
				t.Fatal(err)
			}
			legacy, err := jsonv2.Marshal(zero, json.DefaultOptionsV1())
			if err != nil {
				t.Fatalf("v2 with the options of v1 cannot write the type: %v", err)
			}
			if string(legacy) != string(one) {
				t.Errorf("v1 writes %s and v2 with the options of v1 writes %s", one, legacy)
			}
			// omitempty means something else to v2 for a number: it does not leave
			// a zero out.
			if plain, err := jsonv2.Marshal(zero); err == nil && !fact.zeroLeftOut && string(plain) != string(one) {
				t.Errorf("v1 writes %s and v2 by itself writes %s", one, plain)
			}
		})
	}
}

// Names are matched without regard to case when a type is read, as they have
// been, unless the field's tag says otherwise. v2 by itself reads names exactly,
// so this is one of the places where moving to it would change what is accepted.
func TestEncodingJSONMatchesNamesIgnoringCaseUnlessATagSaysOtherwise(t *testing.T) {
	type reader struct {
		Plain  string `json:"plain"`
		Strict string `json:"strict,case:strict"`
		Loose  string `json:"loose,case:ignore"`
		GoName string
	}
	cases := []struct {
		input string
		want  reader
	}{
		{`{"plain":"p"}`, reader{Plain: "p"}},
		{`{"PLAIN":"p"}`, reader{Plain: "p"}},
		{`{"Plain":"p"}`, reader{Plain: "p"}},
		{`{"strict":"s"}`, reader{Strict: "s"}},
		{`{"STRICT":"s"}`, reader{}},
		{`{"loose":"l"}`, reader{Loose: "l"}},
		{`{"LOOSE":"l"}`, reader{Loose: "l"}},
		{`{"GoName":"g"}`, reader{GoName: "g"}},
		{`{"goname":"g"}`, reader{GoName: "g"}},
		{`{"GONAME":"g"}`, reader{GoName: "g"}},
		{`{"plain_":"p"}`, reader{}},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			var got reader
			if err := json.Unmarshal([]byte(tc.input), &got); err != nil || got != tc.want {
				t.Errorf("encoding/json reads %s as %+v, %v; want %+v", tc.input, got, err, tc.want)
			}
			var legacy reader
			if err := jsonv2.Unmarshal([]byte(tc.input), &legacy, json.DefaultOptionsV1()); err != nil || legacy != tc.want {
				t.Errorf("v2 with the options of v1 reads %s as %+v, %v; want %+v", tc.input, legacy, err, tc.want)
			}
		})
	}

	var exact reader
	if err := jsonv2.Unmarshal([]byte(`{"PLAIN":"p","goname":"g"}`), &exact); err != nil || exact != (reader{}) {
		t.Errorf("v2 by itself reads names exactly, and read %+v, %v", exact, err)
	}
}

// A tag name is taken only when encoding/json takes it, so what jsonTagName gives
// is checked against what encoding/json writes for a field tagged with each tag
// of the facts: the key is the name, or the Go name when the tag gives none.
func TestJSONTagNameAgreesWithEncodingJSONOnWhichNamesAreAccepted(t *testing.T) {
	for _, fact := range jsonTagFacts() {
		t.Run(fact.tag, func(t *testing.T) {
			raw, err := json.Marshal(reflect.New(jsonTagStruct(fact.tag)).Interface())
			if err != nil {
				t.Fatal(err)
			}
			keys := jsonKeysInOrder(t, raw)

			name, omitted := jsonTagName(fact.tag)
			if name == "" {
				name = "Field"
			}
			// A field left out of the output by omitempty or omitzero is still a
			// member of the type: only a dash leaves it out of JSON altogether.
			written := slices.Contains(keys, name)
			if omitted == written && !fact.zeroLeftOut {
				t.Errorf("jsonTagName takes %q (left out = %v), encoding/json writes %v", name, omitted, keys)
			}
			if fact.zeroLeftOut && !omitted && written {
				t.Errorf("encoding/json leaves a zero value of %q out and wrote %v", name, keys)
			}
		})
	}
}

func TestJSONTagNameIsWhatEncodingJSONTakesFromATag(t *testing.T) {
	for _, fact := range jsonTagFacts() {
		t.Run(fact.tag, func(t *testing.T) {
			name, omitted := jsonTagName(fact.tag)
			if name != fact.name || omitted != fact.omitted {
				t.Errorf("jsonTagName(%q) = %q, %v, want %q, %v", fact.tag, name, omitted, fact.name, fact.omitted)
			}
		})
	}
}

func TestParseJSONTagKeepsTheOptionsThatDecideTheMembers(t *testing.T) {
	cases := []struct {
		tag  string
		want jsonTag
	}{
		{"", jsonTag{}},
		{"-", jsonTag{omitted: true}},
		{"n", jsonTag{name: "n"}},
		{",embed", jsonTag{embed: true}},
		{"n,embed", jsonTag{name: "n", embed: true}},
		{",embed,omitempty", jsonTag{embed: true}},
		{",omitempty,embed", jsonTag{embed: true}},
		{",omitzero", jsonTag{omitzero: true}},
		{",omitzero,embed", jsonTag{omitzero: true, embed: true}},
		{",embed ", jsonTag{embed: true}},
		{",embed:x", jsonTag{embed: true}},
		{",Embed", jsonTag{}},
		{",embedded", jsonTag{}},
		{",case:ignore,embed", jsonTag{embed: true}},
		{",case:strict,embed", jsonTag{embed: true}},
		{",case:embed", jsonTag{}},
		{",case,embed", jsonTag{embed: true}},
		{",case:,embed", jsonTag{embed: true}},
		{",case:'x',embed", jsonTag{embed: true}},
		{",format:'a,embed',embed", jsonTag{embed: true}},
		{",format:'a,b'", jsonTag{}},
		{",format:'a,embed'", jsonTag{}},
		{",format:x,embed", jsonTag{embed: true}},
		{",format:'unterminated,embed", jsonTag{embed: true}},
		{",format:'',embed", jsonTag{embed: true}},
		{`,format:'it\'s,embed',embed`, jsonTag{embed: true}},
		{",'embed'", jsonTag{}},
		{",,embed", jsonTag{embed: true}},
		{"a'b,embed", jsonTag{name: "a", embed: true}},
		{"'b',embed", jsonTag{embed: true}},
		{"n,embed,embed", jsonTag{name: "n", embed: true}},
	}
	for _, tc := range cases {
		t.Run(tc.tag, func(t *testing.T) {
			if got := parseJSONTag(tc.tag); got != tc.want {
				t.Errorf("parseJSONTag(%q) = %+v, want %+v", tc.tag, got, tc.want)
			}
		})
	}
}

// The option embed is read by encoding/json wherever it stands in a tag, so the
// members of each of these types are what encoding/json writes for it.
func TestJSONMembersFollowTheOptionEmbedAsEncodingJSONDoes(t *testing.T) {
	type tagged struct {
		In PlainEmbedded `json:",embed"`
	}
	type pointed struct {
		In *PlainEmbedded `json:",embed"`
		Z  int            `json:"z"`
	}
	type valueFallback struct {
		Rest jsontext.Value `json:",embed"`
		Z    int            `json:"z"`
	}
	type timeKeys map[time.Time]int
	type mapWithMethodKeys struct {
		Rest timeKeys `json:",embed"`
		Z    int      `json:"z"`
	}
	type mapOfIntegers struct {
		Rest map[int]string `json:",embed"`
		Z    int            `json:"z"`
	}

	cases := []struct {
		name   string
		target reflect.Type
		value  any
		want   []string
	}{
		{"a struct by value", reflect.TypeFor[tagged](), &tagged{}, []string{"plain"}},
		{"a pointer to a struct", reflect.TypeFor[pointed](), &pointed{In: &PlainEmbedded{}}, []string{"plain", "z"}},
		{"a jsontext.Value", reflect.TypeFor[valueFallback](), &valueFallback{}, []string{"z"}},
		{"a map of strings", reflect.TypeFor[embedOptionOnMap](), &embedOptionOnMap{}, []string{"z"}},
		{"a map with keys that are not strings", reflect.TypeFor[mapOfIntegers](), &mapOfIntegers{}, []string{"Rest", "z"}},
		{"a map whose keys have methods", reflect.TypeFor[mapWithMethodKeys](), &mapWithMethodKeys{}, []string{"Rest", "z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonMemberNames(jsonMembers(tc.target)); !slices.Equal(got, tc.want) {
				t.Errorf("members = %v, want %v", got, tc.want)
			}
			if tc.value == nil {
				return
			}
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("encoding/json cannot write the type: %v", err)
			}
			if got := jsonKeysInOrder(t, raw); !slices.Equal(got, tc.want) {
				t.Errorf("encoding/json writes %v for %T, the facts say %v", got, tc.value, tc.want)
			}
		})
	}
}

// A field that is not exported cannot be a struct field of a type the test
// declares with a tag on it, so these are built by hand.
func TestJSONFieldRoleIsTheRoleEncodingJSONGivesAField(t *testing.T) {
	embedded := func(tag string, held reflect.Type, exported bool) reflect.StructField {
		field := reflect.StructField{Name: "Held", Type: held, Anonymous: true, Tag: reflect.StructTag(tag)}
		if !exported {
			field.Name, field.PkgPath = "held", "example.com/p"
		}
		return field
	}
	named := func(tag string, held reflect.Type, exported bool) reflect.StructField {
		field := embedded(tag, held, exported)
		field.Anonymous = false
		return field
	}
	structType := reflect.TypeFor[PlainEmbedded]()

	cases := []struct {
		name  string
		field reflect.StructField
		want  jsonRole
	}{
		{"an embedded struct", embedded(``, structType, true), jsonPromoted},
		{"an embedded pointer to a struct", embedded(``, reflect.PointerTo(structType), true), jsonPromoted},
		{"an unexported embedded struct", embedded(``, structType, false), jsonPromoted},
		{"an unexported embedded pointer", embedded(``, reflect.PointerTo(structType), false), jsonPromoted},
		{"an embedded struct left out", embedded(`json:"-"`, structType, true), jsonLeftOut},
		{"an embedded struct given a name", embedded(`json:"in"`, structType, true), jsonOwnMember},
		{"an embedded struct given a name that is cut short", embedded(`json:"a\\b"`, structType, true), jsonOwnMember},
		{"an embedded struct under options", embedded(`json:",omitempty"`, structType, true), jsonPromoted},
		{"an embedded struct the option embed names", embedded(`json:",embed"`, structType, true), jsonPromoted},
		{"an embedded struct under a name and the option embed", embedded(`json:"in,embed"`, structType, true), jsonOwnMember},
		{"an unexported embedded struct given a name", embedded(`json:"in"`, structType, false), jsonOwnMember},
		{"an unexported embedded struct given a name, with methods", embedded(`json:"in"`, reflect.TypeFor[time.Time](), false), jsonLeftOut},
		{"an unexported embedded struct given a name, with omitzero and IsZero", embedded(`json:"in,omitzero"`, reflect.TypeFor[isZeroStruct](), false), jsonLeftOut},
		{"an unexported embedded struct given a name, with IsZero and no omitzero", embedded(`json:"in"`, reflect.TypeFor[isZeroStruct](), false), jsonOwnMember},
		{"an embedded string", embedded(``, reflect.TypeFor[string](), true), jsonOwnMember},
		{"an unexported embedded string", embedded(``, reflect.TypeFor[string](), false), jsonLeftOut},
		{"an unexported embedded string under the option embed", embedded(`json:",embed"`, reflect.TypeFor[string](), false), jsonLeftOut},
		{"an unexported embedded string given a name", embedded(`json:"in"`, reflect.TypeFor[string](), false), jsonLeftOut},
		{"an embedded map", embedded(``, reflect.TypeFor[map[string]int](), true), jsonOwnMember},
		{"an embedded map under the option embed", embedded(`json:",embed"`, reflect.TypeFor[map[string]int](), true), jsonLeftOut},
		{"an embedded jsontext.Value under the option embed", embedded(`json:",embed"`, reflect.TypeFor[jsontext.Value](), true), jsonLeftOut},
		{"a field", named(``, reflect.TypeFor[string](), true), jsonOwnMember},
		{"a field with a name", named(`json:"n"`, reflect.TypeFor[string](), true), jsonOwnMember},
		{"a field left out", named(`json:"-"`, reflect.TypeFor[string](), true), jsonLeftOut},
		{"an unexported field", named(``, reflect.TypeFor[string](), false), jsonLeftOut},
		{"an unexported field under a tag", named(`json:"n"`, reflect.TypeFor[string](), false), jsonLeftOut},
		{"an unexported struct field under the option embed", named(`json:",embed"`, structType, false), jsonLeftOut},
		{"a struct field under the option embed", named(`json:",embed"`, structType, true), jsonPromoted},
		{"a pointer to a struct under the option embed", named(`json:",embed"`, reflect.PointerTo(structType), true), jsonPromoted},
		{"a struct field under the option embed and options", named(`json:",embed,omitempty"`, structType, true), jsonPromoted},
		{"a struct field with a name and the option embed", named(`json:"n,embed"`, structType, true), jsonOwnMember},
		{"a map field under the option embed", named(`json:",embed"`, reflect.TypeFor[map[string]string](), true), jsonLeftOut},
		{"a map with integer keys under the option embed", named(`json:",embed"`, reflect.TypeFor[map[int]string](), true), jsonOwnMember},
		{"an integer under the option embed", named(`json:",embed"`, reflect.TypeFor[int](), true), jsonOwnMember},
		{"a struct field", named(``, structType, true), jsonOwnMember},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := jsonFieldRole(tc.field); got != tc.want {
				t.Errorf("jsonFieldRole = %d, want %d", got, tc.want)
			}
		})
	}
}

// isZeroStruct has the method that omitzero calls.
type isZeroStruct struct{ N int }

func (s isZeroStruct) IsZero() bool { return s.N == 0 }

func TestEmbeddedStructIsWhatEncodingJSONPromotes(t *testing.T) {
	fieldOf := func(structType reflect.Type, name string) reflect.StructField {
		field, ok := structType.FieldByName(name)
		if !ok {
			t.Fatalf("%v has no field %s", structType, name)
		}
		return field
	}
	cases := []struct {
		name  string
		field reflect.StructField
		want  reflect.Type
	}{
		{"an embedded struct", fieldOf(reflect.TypeFor[diamondLeft](), "diamondBase"), reflect.TypeFor[diamondBase]()},
		{"an embedded pointer", fieldOf(reflect.TypeFor[unexportedEmbeddedByPointer](), "unexportedBase"), reflect.TypeFor[unexportedBase]()},
		{"an embedded pointer to itself", fieldOf(reflect.TypeFor[SelfEmbeddingExported](), "SelfEmbeddingExported"), reflect.TypeFor[SelfEmbeddingExported]()},
		{"a tag with options only", fieldOf(reflect.TypeFor[optionsOnlyTagEmbedding](), "PlainEmbedded"), reflect.TypeFor[PlainEmbedded]()},
		{"a tag with a name encoding/json does not accept", fieldOf(reflect.TypeFor[invalidTagEmbedding](), "PlainEmbedded"), reflect.TypeFor[PlainEmbedded]()},
		{"a tag with a name that runs into a backslash", fieldOf(reflect.TypeFor[truncatedTagEmbedding](), "PlainEmbedded"), nil},
		{"an embedded time", fieldOf(reflect.TypeFor[struct{ time.Time }](), "Time"), reflect.TypeFor[time.Time]()},
		{"a tag with a name", fieldOf(reflect.TypeFor[namedTagEmbedding](), "PlainEmbedded"), nil},
		{"a tag of a dash", fieldOf(reflect.TypeFor[omittedEmbedding](), "PlainEmbedded"), nil},
		{"an embedded type that is not a struct", fieldOf(reflect.TypeFor[labelEmbedding](), "Label"), nil},
		{"an unexported embedded type that is not a struct", fieldOf(reflect.TypeFor[hiddenLabelEmbedding](), "hiddenLabel"), nil},
		{"a field that is not embedded", fieldOf(reflect.TypeFor[diamondLeft](), "Left"), nil},
		{"a field of a struct type that is not embedded", fieldOf(reflect.TypeFor[struct {
			Inner diamondBase `json:"inner"`
		}](), "Inner"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := embeddedStruct(tc.field); got != tc.want {
				t.Errorf("embeddedStruct = %v, want %v", got, tc.want)
			}
		})
	}
}

// collectStructProperties is what the generator calls for a struct: it lists the
// members once each, in order, and says so when one cannot be expressed.
func TestCollectStructPropertiesListsTheMembersOnceEach(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		ok     bool
		want   []string
	}{
		{"a plain struct", reflect.TypeFor[schemaContact](), true, []string{"name", "age", "active", "score", "tags", "note"}},
		{"one struct embedded by two routes", reflect.TypeFor[diamondTop](), true, []string{"left", "right"}},
		{"that name declared again by the outer struct", reflect.TypeFor[diamondDeclaredByTop](), true, []string{"left", "right", "id"}},
		{"one struct reached at two depths", reflect.TypeFor[diamondAtTwoDepths](), true, []string{"id", "left"}},
		{"two structs that each declare a name", reflect.TypeFor[twinNamesTop](), true, []string{"a", "b"}},
		{"a name the outer struct declares", reflect.TypeFor[outerNameHidesTwin](), true, []string{"a", "name"}},
		{"nothing but an ambiguous name", reflect.TypeFor[soleNamesTop](), true, []string{}},
		{"a tag with options only on an embedded struct", reflect.TypeFor[optionsOnlyTagEmbedding](), true, []string{"plain", "z"}},
		{"an unexported struct by value", reflect.TypeFor[unexportedEmbeddedByValue](), true, []string{"exp", "own"}},
		{"an unexported struct by pointer", reflect.TypeFor[unexportedEmbeddedByPointer](), false, []string{}},
		{"an unexported pointer that promotes nothing", reflect.TypeFor[unexportedPointerPromotingNothing](), true, []string{"n"}},
		{"a channel", reflect.TypeFor[struct {
			Name string   `json:"name"`
			Ch   chan int `json:"ch"`
		}](), false, []string{}},
		{"a recursive field", reflect.TypeFor[embeddedWithAParent](), false, []string{}},
		{"a map", reflect.TypeFor[struct {
			Labels map[string]string `json:"labels"`
		}](), false, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			properties := map[string]any{}
			var required []string
			ok := collectStructProperties(tc.target, map[reflect.Type]bool{tc.target: true}, 0, properties, &required)
			if ok != tc.ok {
				t.Fatalf("collectStructProperties = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got := append([]string{}, required...); !slices.Equal(got, tc.want) {
				t.Errorf("required = %v, want %v", got, tc.want)
			}
			if len(properties) != len(tc.want) {
				t.Errorf("%d properties for %d members: %v", len(properties), len(tc.want), properties)
			}
		})
	}
}

func TestConsumeJSONTagOptionReadsAnIdentifierOrAQuotedString(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		quoted bool
		option string
		n      int
		ok     bool
	}{
		{"a word", "omitempty", false, "omitempty", 9, true},
		{"a word and the rest", "omitempty,string", false, "omitempty", 9, true},
		{"a word and a colon", "case:ignore", false, "case", 4, true},
		{"a word with digits and an underscore", "a_1,b", false, "a_1", 3, true},
		{"a word with a letter that is not ASCII", "é1,b", false, "é1", 3, true},
		{"a word that ends at a space", "omitempty ", false, "omitempty", 9, true},
		{"a word that begins with an underscore", "_x", false, "_x", 2, true},
		{"a digit first", "1x,b", false, "1x", 2, false},
		{"a quote when none is allowed", "'x',b", false, "'x'", 3, false},
		{"a quote that is allowed", "'x',b", true, "x", 3, true},
		{"a quoted string with a comma in it", "'a,b',c", true, "a,b", 5, true},
		{"a quoted string with an escaped quote", `'it\'s',c`, true, "it's", 7, true},
		{"a quoted string with a double quote in it", `'a"b',c`, true, `a"b`, 5, true},
		{"a quoted string that is not closed", "'abc,d", true, "'abc", 4, false},
		{"a quoted string with an escape that is not one", `'a\q',c`, true, `'a\q'`, 5, false},
		{"an empty quoted string", "'',x", true, "", 2, true},
		{"nothing", "", false, "", 0, false},
		{"a comma first", ",x", false, "", 0, false},
		{"punctuation first", "-x,y", false, "-x", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			option, n, ok := consumeJSONTagOption(tc.in, tc.quoted)
			if option != tc.option || n != tc.n || ok != tc.ok {
				t.Errorf("consumeJSONTagOption(%q, %v) = %q, %d, %v; want %q, %d, %v", tc.in, tc.quoted, option, n, ok, tc.option, tc.n, tc.ok)
			}
		})
	}
}

func TestJSONHeldTypeLooksThroughOneUnnamedPointer(t *testing.T) {
	type named struct{}
	type namedPointer *named
	cases := []struct {
		name string
		in   reflect.Type
		want reflect.Type
	}{
		{"a struct", reflect.TypeFor[named](), reflect.TypeFor[named]()},
		{"a pointer to a struct", reflect.TypeFor[*named](), reflect.TypeFor[named]()},
		{"a pointer to a pointer", reflect.TypeFor[**named](), reflect.TypeFor[*named]()},
		{"a named pointer type", reflect.TypeFor[namedPointer](), reflect.TypeFor[namedPointer]()},
		{"a string", reflect.TypeFor[string](), reflect.TypeFor[string]()},
		{"a pointer to a string", reflect.TypeFor[*string](), reflect.TypeFor[string]()},
		{"a map", reflect.TypeFor[map[string]int](), reflect.TypeFor[map[string]int]()},
		{"a pointer to a map", reflect.TypeFor[*map[string]int](), reflect.TypeFor[map[string]int]()},
		{"an interface", reflect.TypeFor[any](), reflect.TypeFor[any]()},
		{"a slice", reflect.TypeFor[[]int](), reflect.TypeFor[[]int]()},
		{"a time", reflect.TypeFor[*time.Time](), reflect.TypeFor[time.Time]()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonHeldType(tc.in); got != tc.want {
				t.Errorf("jsonHeldType(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestJSONImplementsAnyLooksAtTheTypeAndAPointerToIt(t *testing.T) {
	type onValue struct{ time.Time }
	cases := []struct {
		name string
		in   reflect.Type
		want bool
	}{
		{"time.Time", reflect.TypeFor[time.Time](), true},
		{"a pointer to time.Time", reflect.TypeFor[*time.Time](), true},
		{"a struct that embeds time.Time", reflect.TypeFor[onValue](), true},
		{"json.Number", reflect.TypeFor[json.Number](), true},
		{"json.RawMessage", reflect.TypeFor[json.RawMessage](), true},
		{"jsontext.Value", reflect.TypeFor[jsontext.Value](), true},
		{"a string", reflect.TypeFor[string](), false},
		{"a struct", reflect.TypeFor[PlainEmbedded](), false},
		{"a map", reflect.TypeFor[map[string]string](), false},
		{"time.Duration", reflect.TypeFor[time.Duration](), false},
		{"a type with IsZero only", reflect.TypeFor[isZeroStruct](), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonImplementsAny(tc.in, jsonMethodTypes...); got != tc.want {
				t.Errorf("jsonImplementsAny(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

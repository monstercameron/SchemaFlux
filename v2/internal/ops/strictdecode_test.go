package ops

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type exactTarget struct {
	Name  string  `json:"name"`
	Total float64 `json:"total"`
	Items []struct {
		ID    int     `json:"id"`
		Price float64 `json:"price"`
	} `json:"items"`
}

// S-008. encoding/json ignores properties it does not recognise, takes the last
// of a duplicate key, and stops at the end of the first value. Reasonable for a
// config file written by a person; wrong for an answer from a model, where an
// unrecognised property is the model producing a field nobody asked for.
func TestDecodeExactRejectsWhatEncodingJSONForgives(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
		kind    types.ErrorKind
	}{
		{
			"a field nobody asked for",
			`{"name":"Ada","total":1,"items":[],"invented":"value"}`,
			"invented",
			types.KindSchemaViolation,
		},
		{
			"a repeated key",
			`{"name":"Ada","name":"Grace","total":1,"items":[]}`,
			"repeats the key",
			types.KindSchemaViolation,
		},
		{
			"a second value after the first",
			`{"name":"Ada","total":1,"items":[]} {"name":"Grace","total":2,"items":[]}`,
			"more than one JSON value",
			types.KindSchemaViolation,
		},
		{
			"a value of the wrong type",
			`{"name":"Ada","total":"free","items":[]}`,
			"total",
			types.KindSchemaViolation,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var target exactTarget
			err := DecodeExact(tc.body, &target, DecodeLimits{})
			if err == nil {
				t.Fatal("DecodeExact accepted it")
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the error does not mention %q: %v", tc.wantErr, err)
			}
		})
	}
}

// A well-formed answer that fits still decodes, including nested structure.
func TestDecodeExactAcceptsAFaithfulAnswer(t *testing.T) {
	body := `{"name":"Ada","total":12.5,"items":[{"id":1,"price":2.5},{"id":2,"price":10}]}`

	var target exactTarget
	if err := DecodeExact(body, &target, DecodeLimits{}); err != nil {
		t.Fatalf("DecodeExact: %v", err)
	}
	if target.Name != "Ada" || target.Total != 12.5 || len(target.Items) != 2 {
		t.Errorf("decoded %+v", target)
	}
}

// The packaging models put around answers still comes off, because the strict
// contract is about the JSON, not about whether the model said "here you go".
func TestDecodeExactSeesThroughFences(t *testing.T) {
	body := "Here you go:\n```json\n{\"name\":\"Ada\",\"total\":1,\"items\":[]}\n```"

	var target exactTarget
	if err := DecodeExact(body, &target, DecodeLimits{}); err != nil {
		t.Fatalf("DecodeExact: %v", err)
	}
	if target.Name != "Ada" {
		t.Errorf("decoded %+v", target)
	}
}

// The limits exist because the input is bytes from a remote service. A response
// that would cost memory before turning out to be useless is refused.
func TestDecodeLimitsRefusePathologicalInput(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 40) + "1" + strings.Repeat("}", 40)
	var target map[string]any
	if err := DecodeExact(deep, &target, DecodeLimits{MaxDepth: 8}); err == nil {
		t.Error("a 40-level document was accepted against an 8-level limit")
	} else if !strings.Contains(err.Error(), "nests deeper") {
		t.Errorf("the error does not say what was wrong: %v", err)
	}

	long := `{"name":"` + strings.Repeat("x", 500) + `","total":1,"items":[]}`
	var value exactTarget
	if err := DecodeExact(long, &value, DecodeLimits{MaxStringBytes: 100}); err == nil {
		t.Error("a 500-byte string was accepted against a 100-byte limit")
	}

	big := `{"name":"Ada","total":1,"items":[]}`
	if err := DecodeExact(big, &value, DecodeLimits{MaxBytes: 10}); err == nil {
		t.Error("a 35-byte body was accepted against a 10-byte limit")
	}

	items := "[" + strings.TrimSuffix(strings.Repeat("1,", 50), ",") + "]"
	var list []int
	if err := DecodeExact(items, &list, DecodeLimits{MaxArrayItems: 10}); err == nil {
		t.Error("a 50-item array was accepted against a 10-item limit")
	}
}

// A failure names where it happened, because "the response did not fit" is not
// actionable and a pointer is.
func TestDecodeExactNamesTheLocation(t *testing.T) {
	body := `{"name":"Ada","total":1,"items":[{"id":1,"price":1,"price":2}]}`

	var target exactTarget
	err := DecodeExact(body, &target, DecodeLimits{})
	if err == nil {
		t.Fatal("a repeated key inside an array element was accepted")
	}
	if !strings.Contains(err.Error(), "price") {
		t.Errorf("the error does not name the repeated key: %v", err)
	}
	if !strings.Contains(err.Error(), "/items") {
		t.Errorf("the error does not point into the document: %v", err)
	}
}

// The message carries the field name, which describes the shape, and never the
// value, which is the caller's data.
func TestDecodeExactErrorsCarryNoValues(t *testing.T) {
	const secret = "SSN-123-45-6789"
	body := `{"name":"` + secret + `","total":"free","items":[]}`

	var target exactTarget
	err := DecodeExact(body, &target, DecodeLimits{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error carries the payload: %v", err)
	}
}

// Malformed and schema-violating are different kinds, because they are repaired
// differently: one by quoting the parse error, one by regenerating.
func TestDecodeExactDistinguishesMalformedFromMisfitting(t *testing.T) {
	var target exactTarget

	err := DecodeExact("this is not JSON at all", &target, DecodeLimits{})
	if kind := types.KindOf(err); kind != types.KindMalformedOutput {
		t.Errorf("prose classified as %v, want malformed output", kind)
	}

	err = DecodeExact(`{"name":"Ada","total":1,"items":[],"extra":1}`, &target, DecodeLimits{})
	if kind := types.KindOf(err); kind != types.KindSchemaViolation {
		t.Errorf("an extra field classified as %v, want schema violation", kind)
	}
}

// checkStructure guards every strict decode, so its bookkeeping across nested
// containers has to be exact. The cases below are the shapes that went wrong
// when a closing delimiter was not counted as a completed value of its parent:
// a valid answer was refused for two equal strings after a nested object, a list
// with a repeated string was refused, and a real duplicate after a nested object
// was accepted.
func TestCheckStructureTracksKeysAcrossNestedContainers(t *testing.T) {
	cases := []struct {
		name string
		body string
		// wantErr is a substring of the expected error; empty means accepted.
		wantErr string
	}{
		{"equal string values after a nested object", `{"a":{"x":1},"first":"Bob","last":"Bob"}`, ""},
		{"equal string values after a nested array", `{"a":[1,2],"first":"Bob","last":"Bob"}`, ""},
		{"list with a repeated string", `{"tags":["a","b","c","b"]}`, ""},
		{"top level list with repeated strings", `["x","x","x"]`, ""},
		{"list of lists of strings", `[["a","a"],["a","a"]]`, ""},
		{"list of objects with equal values", `[{"k":"v","j":"v"},{"k":"v","j":"v"}]`, ""},
		{"same key in sibling objects", `{"a":{"k":1},"b":{"k":2}}`, ""},
		{"same key in objects inside an array", `{"items":[{"id":1},{"id":2},{"id":3}]}`, ""},
		{"key equal to an earlier value", `{"a":"b","b":"a"}`, ""},
		{"key equal to a value in a nested array", `{"a":["b"],"b":1}`, ""},
		{"empty object then equal strings", `{"a":{},"b":"v","c":"v"}`, ""},
		{"empty array then equal strings", `{"a":[],"b":"v","c":"v"}`, ""},
		{"empty containers nested", `{"a":[[],{}],"b":{"c":[]},"d":"x","e":"x"}`, ""},
		{"depth four objects then equal strings", `{"a":{"b":{"c":{"d":1}}},"x":"s","y":"s"}`, ""},
		{"depth four arrays then equal strings", `{"a":[[[[1]]]],"x":"s","y":"s"}`, ""},
		{"mixed depth four", `{"a":[{"b":[{"c":"v","d":"v"}]}],"x":"s","y":"s"}`, ""},
		{"braces inside string values", `{"a":"{","b":"}","c":"{","d":"}"}`, ""},
		{"brackets inside a string value", `{"a":"[1,2]","b":"[1,2]"}`, ""},
		{"escaped quotes inside string values", `{"a":"say \"hi\"","b":"say \"hi\""}`, ""},
		{"keys that look like delimiters", `{"{":1,"}":2,"[":3,"]":4}`, ""},
		{"empty key and empty values", `{"":"","a":""}`, ""},

		{"duplicate key at the top level", `{"a":1,"a":2}`, `repeats the key "a"`},
		{"duplicate key after a nested object", `{"a":{"x":1},"b":1,"b":2}`, `repeats the key "b"`},
		{"duplicate key after a nested array", `{"a":[1,2,3],"b":1,"b":2}`, `repeats the key "b"`},
		{"duplicate key after an empty object", `{"a":{},"b":1,"b":2}`, `repeats the key "b"`},
		{"duplicate key inside a nested object", `{"a":{"x":1,"x":2}}`, `repeats the key "x"`},
		{"duplicate key at depth three", `{"a":{"b":{"c":1,"c":2}}}`, `repeats the key "c"`},
		{"duplicate key at depth four", `{"a":{"b":{"c":{"d":1,"d":2}}}}`, `repeats the key "d"`},
		{"duplicate key inside an array element", `{"i":[{"p":1},{"p":1,"p":2}]}`, `repeats the key "p"`},
		{"duplicate key inside arrays of arrays", `[[{"k":1,"k":2}]]`, `repeats the key "k"`},
		{"duplicate key after a string with braces", `{"a":"{}","b":1,"b":2}`, `repeats the key "b"`},
		{"duplicate empty key", `{"":1,"":2}`, `repeats the key ""`},
		{"duplicate key whose first value is an object", `{"a":{"x":1},"a":2}`, `repeats the key "a"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStructure(tc.body, DefaultDecodeLimits())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("a valid document was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("the document was accepted, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
			if kind := types.KindOf(err); kind != types.KindSchemaViolation {
				t.Errorf("kind = %v, want schema violation", kind)
			}
		})
	}
}

// The array item limit counts every element, including the first, and an array
// nested in an object is counted separately from its parent.
func TestCheckStructureCountsArrayItemsExactly(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		limit int
		fails bool
	}{
		{"exactly at the limit", `[1,2,3]`, 3, false},
		{"one over the limit", `[1,2,3,4]`, 3, true},
		{"strings at the limit", `["a","a","a"]`, 3, false},
		{"strings over the limit", `["a","a","a","a"]`, 3, true},
		{"nested arrays each at the limit", `[[1,2],[3,2]]`, 2, false},
		{"inner array over the limit", `[[1,2,3],[4]]`, 2, true},
		{"objects in an array count once each", `[{"a":1,"b":2,"c":3},{"a":1}]`, 2, false},
		{"array inside an object over the limit", `{"k":[1,2,3]}`, 2, true},
		{"object members are not array items", `{"a":1,"b":2,"c":3,"d":4}`, 2, false},
		{"empty arrays count as elements", `[[],[],[]]`, 3, false},
		{"empty arrays over the limit", `[[],[],[],[]]`, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStructure(tc.body, DecodeLimits{MaxArrayItems: tc.limit}.withDefaults())
			if tc.fails && err == nil {
				t.Fatal("accepted")
			}
			if !tc.fails && err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

// The depth limit is a count of open containers: a value nested exactly
// MaxDepth levels deep is accepted and one level more is refused.
func TestCheckStructureRefusesDepthOneBeyondTheLimit(t *testing.T) {
	limits := DecodeLimits{MaxDepth: 3}.withDefaults()
	cases := []struct {
		name  string
		body  string
		fails bool
	}{
		{"arrays exactly at the limit", `[[[1]]]`, false},
		{"arrays one beyond the limit", `[[[[1]]]]`, true},
		{"objects exactly at the limit", `{"a":{"b":{"c":1}}}`, false},
		{"objects one beyond the limit", `{"a":{"b":{"c":{"d":1}}}}`, true},
		{"mixed containers exactly at the limit", `{"a":[{"b":1}]}`, false},
		{"mixed containers one beyond the limit", `{"a":[{"b":[1]}]}`, true},
		{"empty containers at the limit", `[[[]]]`, false},
		{"empty containers one beyond the limit", `[[[[]]]]`, true},
		{"siblings at the limit do not add up", `[[[1],[2]],[[3]]]`, false},
		{"a scalar at the top level", `1`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStructure(tc.body, limits)
			if !tc.fails {
				if err != nil {
					t.Fatalf("a value within MaxDepth was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a value one level beyond MaxDepth was accepted")
			}
			if !strings.Contains(err.Error(), "nests deeper than 3 levels") {
				t.Errorf("the refusal does not name the limit: %v", err)
			}
		})
	}
}

// A pointer in an error names the position, including array indices, and keeps
// naming it correctly after a nested container has closed.
func TestCheckStructurePointersAfterNestedContainers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"object after an array", `{"list":[1,2],"o":{"k":1,"k":2}}`, "at /o;"},
		{"array element object", `{"items":[{"id":1},{"id":2,"id":3}]}`, "at /items/1;"},
		{"after a nested object", `{"a":{"x":1},"b":{"y":1,"y":2}}`, "at /b;"},
		{"top level duplicate", `{"a":1,"a":2}`, "at the top level;"},
		{"arrays of arrays", `[[{"k":1,"k":2}]]`, "at /0/0;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStructure(tc.body, DefaultDecodeLimits())
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}

	deep := checkStructure(`{"a":[{"b":`+strings.Repeat(`[`, 5)+strings.Repeat(`]`, 5)+`}]}`, DecodeLimits{MaxDepth: 5}.withDefaults())
	if deep == nil || !strings.Contains(deep.Error(), "nests deeper than 5 levels at /a/0/b") {
		t.Errorf("depth error = %v", deep)
	}

	long := checkStructure(`{"k":[{"v":"`+strings.Repeat("x", 20)+`"}]}`, DecodeLimits{MaxStringBytes: 10}.withDefaults())
	if long == nil || !strings.Contains(long.Error(), "at /k/0/v") {
		t.Errorf("string length error = %v", long)
	}
}

// End to end through DecodeExact: the valid answers of the report decode, and
// the duplicate after a nested object is refused.
func TestDecodeExactAcrossNestedContainers(t *testing.T) {
	type person struct {
		Meta  map[string]int `json:"meta"`
		First string         `json:"first"`
		Last  string         `json:"last"`
	}
	var got person
	if err := DecodeExact(`{"meta":{"x":1},"first":"Bob","last":"Bob"}`, &got, DecodeLimits{}); err != nil {
		t.Fatalf("a valid answer was refused: %v", err)
	}
	if got.First != "Bob" || got.Last != "Bob" || got.Meta["x"] != 1 {
		t.Errorf("decoded %+v", got)
	}

	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := DecodeExact(`{"tags":["a","b","c","b"]}`, &tags, DecodeLimits{}); err != nil {
		t.Fatalf("a list with a repeated string was refused: %v", err)
	}
	if len(tags.Tags) != 4 {
		t.Errorf("decoded %+v", tags)
	}

	var dup struct {
		A map[string]int `json:"a"`
		B int            `json:"b"`
	}
	err := DecodeExact(`{"a":{"x":1},"b":1,"b":2}`, &dup, DecodeLimits{})
	if err == nil || !strings.Contains(err.Error(), "repeats the key") {
		t.Errorf("a duplicate after a nested object was not refused: %v", err)
	}
}

// jsonEscape is the backslash that begins an escape in JSON text. The bodies
// below are built with it, so that the Go source does not hold an escape
// sequence of its own where a JSON one is meant.
const jsonEscape = "\\"

// structureWideObjectKeys is how many keys the widest objects the generator
// writes have: enough that the decoder keeps a set of them.
const structureWideObjectKeys = 256

// What the one pass over the bytes says of a body, for each kind of body. It
// answers for the limits and for nothing else: a repeated key is the decoder's
// to find, and text that is not JSON is the walk's, so neither moves the answer.
func TestStructureWithinLimitsAnswersForTheLimitsAlone(t *testing.T) {
	limits := DefaultDecodeLimits()
	cases := []struct {
		name   string
		body   string
		limits DecodeLimits
		within bool
	}{
		{"a flat object", `{"a":1,"b":"two","c":true,"d":null,"e":1.5e3}`, limits, true},
		{"nested objects and lists", `{"a":{"b":[1,2,{"c":"d"}]},"e":[[],{}]}`, limits, true},
		{"the same key in two sibling objects", `{"x":{"k":1},"y":{"k":2},"z":[{"k":3},{"k":4}]}`, limits, true},
		{"the same string as a key and as a value", `{"a":"a","b":"a"}`, limits, true},
		{"a top-level list", `[1,"a",null]`, limits, true},
		{"a top-level scalar", `42`, limits, true},
		{"a top-level string", `"text"`, limits, true},
		{"white space everywhere", " {\n\t\"a\" : [ 1 , 2 ] ,\r\n \"b\" : { } } ", limits, true},
		{"an escape in a value", `{"a":"line\nbreak \"quoted\" ` + jsonEscape + `u00e9"}`, limits, true},
		{"non-ASCII text", `{"nom":"José Núñez","日本":"語"}`, limits, true},
		{"a repeated key", `{"a":1,"a":2}`, limits, true},
		{"a key written with an escape", `{"a` + jsonEscape + `u0062":1}`, limits, true},
		{"keys that are not valid UTF-8", "{\"\xff\":1,\"\xfe\":2}", limits, true},
		{"two values", `{"a":1} {"a":2}`, limits, true},
		{"trailing prose", `{"a":1} thanks`, limits, true},
		{"not JSON", `not json`, limits, true},
		{"empty", ``, limits, true},
		{"truncated", `{"a":[1,2`, limits, false},
		{"a closing bracket with nothing open", `1]`, limits, false},
		{"nesting at the limit", `[[[[1]]]]`, DecodeLimits{MaxDepth: 4}.withDefaults(), true},
		{"nesting one past the limit", `[[[[[1]]]]]`, DecodeLimits{MaxDepth: 4}.withDefaults(), false},
		{"objects nested one past the limit", `{"a":{"b":{"c":{"d":{"e":1}}}}}`, DecodeLimits{MaxDepth: 4}.withDefaults(), false},
		{"a list at the item limit", `[1,2,3]`, DecodeLimits{MaxArrayItems: 3}.withDefaults(), true},
		{"a list one past the item limit", `[1,2,3,4]`, DecodeLimits{MaxArrayItems: 3}.withDefaults(), false},
		{"empty lists count as items", `[[],[],[],[]]`, DecodeLimits{MaxArrayItems: 3}.withDefaults(), false},
		{"object members are not items", `{"a":1,"b":2,"c":3,"d":4}`, DecodeLimits{MaxArrayItems: 3}.withDefaults(), true},
		{"a string at the length limit", `{"a":"0123456789"}`, DecodeLimits{MaxStringBytes: 10}.withDefaults(), true},
		{"a string one past the length limit", `{"a":"0123456789x"}`, DecodeLimits{MaxStringBytes: 10}.withDefaults(), false},
		{"a long string that is not valid UTF-8", "{\"a\":\"\xff\xff\xff\xff\"}", DecodeLimits{MaxStringBytes: 10}.withDefaults(), false},
		{"a short string that is not valid UTF-8", "{\"a\":\"\xff\xff\xff\"}", DecodeLimits{MaxStringBytes: 10}.withDefaults(), true},
		{"escapes that decode to fewer bytes", `["` + strings.Repeat(jsonEscape+`u00e9`, 5) + `"]`, DecodeLimits{MaxStringBytes: 10}.withDefaults(), true},
		{"escapes that decode to more bytes than the limit", `["` + strings.Repeat(jsonEscape+`u00e9`, 6) + `"]`, DecodeLimits{MaxStringBytes: 10}.withDefaults(), false},
		{"a long key is not a string value", `{"0123456789abcdef":1}`, DecodeLimits{MaxStringBytes: 10}.withDefaults(), true},
		{"a string in a list is a value", `["0123456789x"]`, DecodeLimits{MaxStringBytes: 10}.withDefaults(), false},
		{"as many keys as a wide object has", wideObject(structureWideObjectKeys), limits, true},
		{"more keys than that", wideObject(structureWideObjectKeys + 1), limits, true},
		{"a string with a closing bracket in it", `{"a":"]}"}`, limits, true},
		{"a string with an escaped quote and a bracket", `{"a":"\"]"}`, limits, true},
		{"seventeen levels, more than the frames it keeps on the stack", strings.Repeat("[", 17) + strings.Repeat("]", 17), limits, true},
		{"sixty-four levels at the default limit", strings.Repeat("[", 64) + strings.Repeat("]", 64), limits, true},
		{"sixty-five levels at the default limit", strings.Repeat("[", 65) + strings.Repeat("]", 65), limits, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := structureWithinLimits(tc.body, tc.limits); got != tc.within {
				t.Errorf("structureWithinLimits = %v, want %v", got, tc.within)
			}
		})
	}
}

func wideObject(keys int) string {
	var body strings.Builder
	body.WriteByte('{')
	for n := 0; n < keys; n++ {
		if n > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"key%04d":%d`, n, n)
	}
	body.WriteByte('}')
	return body.String()
}

// reversedWideObject is wideObject with its members in the opposite order.
func reversedWideObject(keys int) string {
	var body strings.Builder
	body.WriteByte('{')
	for n := keys - 1; n >= 0; n-- {
		if n < keys-1 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"key%04d":%d`, n, n)
	}
	body.WriteByte('}')
	return body.String()
}

// The length of a string is the length of the text it decodes to, as encoding/json
// reads it, so each way of writing a character is checked against what the
// decoder makes of it.
func TestDecodedStringLengthIsTheLengthOfTheTextTheDecoderReads(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"nothing", ``},
		{"ASCII", `hello world`},
		{"a two-byte character", `é`},
		{"a three-byte character", `日本`},
		{"a four-byte character", structureEmoji},
		{"the simple escapes", `\"\\\/\b\f\n\r\t`},
		{"a short unicode escape", jsonEscape + `u0041`},
		{"a two-byte unicode escape", jsonEscape + `u00e9`},
		{"a three-byte unicode escape", jsonEscape + `u65e5`},
		{"an escape in capitals", jsonEscape + `u00E9`},
		{"a surrogate pair", jsonEscape + `ud83d` + jsonEscape + `ude00`},
		{"a surrogate pair in capitals", jsonEscape + `uD83D` + jsonEscape + `uDE00`},
		{"a high surrogate alone", jsonEscape + `ud800`},
		{"a low surrogate alone", jsonEscape + `udc00`},
		{"a high surrogate before an escape that is not a low one", jsonEscape + `ud800` + jsonEscape + `u0041`},
		{"a high surrogate before a character", jsonEscape + `ud800x`},
		{"a high surrogate before a high surrogate", jsonEscape + `ud800` + jsonEscape + `ud800`},
		{"a high surrogate and a low one apart", jsonEscape + `ud800` + `x` + jsonEscape + `udc00`},
		{"two pairs", strings.Repeat(jsonEscape+`ud83d`+jsonEscape+`ude00`, 2)},
		{"NUL as an escape", jsonEscape + `u0000`},
		{"a byte that is not UTF-8", "\xff"},
		{"two bytes that are not UTF-8", "\xff\xfe"},
		{"a truncated character", "\xe6\x97"},
		{"a character cut short before ASCII", "\xe6\x97x"},
		{"U+FFFD written out", structureReplacement},
		{"mixed", "a" + jsonEscape + `n` + "é" + jsonEscape + `u65e5` + "\xff" + structureEmoji + jsonEscape + `ud800`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded string
			if err := json.Unmarshal([]byte(`"`+tc.text+`"`), &decoded); err != nil {
				t.Fatalf("encoding/json cannot read the string: %v", err)
			}
			if got := decodedStringLength(tc.text); got != len(decoded) {
				t.Errorf("decodedStringLength = %d, encoding/json reads %d bytes", got, len(decoded))
			}
		})
	}
}

// What randomStructureBody draws from. Keys and strings are written in forms the
// decoder reads as the same text and the bytes tell apart (a key and the same key
// with an escape, a surrogate pair and the character it stands for, a character
// and the invalid byte that reads as U+FFFD), and in forms that are not JSON at
// all, because those are what the one pass over the bytes has to leave to the
// token walk.
var (
	structureReplacement = string(rune(0xFFFD))
	structureCombining   = "e" + string(rune(0x0301))
	structureEmoji       = string(rune(0x1F600))
	structureLongNumber  = strings.Repeat("9", 400)

	structureKeys = []string{
		`"a"`, `"b"`, `"c"`, `"a"`,
		`"` + jsonEscape + `u0061"`,
		// Keys that differ only in case, and one written with an escape that is
		// equal to a key written plainly.
		`"A"`, `"` + jsonEscape + `u0041"`, `"key"`, `"Key"`, `"KEY"`, `"ke` + jsonEscape + `u0079"`,
		`"` + jsonEscape + `u00e9"`, `"é"`, `"` + structureCombining + `"`,
		"\"\xff\"", "\"\xfe\"", `""`, `"k` + jsonEscape + `nk"`,
		// NUL, written as an escape and raw (which is not JSON), another control
		// byte, a surrogate pair and the character it stands for, a surrogate
		// that is alone, and U+FFFD written as the character.
		`"` + jsonEscape + `u0000"`, "\"\x00\"", "\"\x01\"",
		`"` + jsonEscape + `ud83d` + jsonEscape + `ude00"`, `"` + structureEmoji + `"`,
		`"` + jsonEscape + `ud800"`, `"` + structureReplacement + `"`,
		`"` + jsonEscape + `/"`, `"/"`,
	}

	structureWords = []string{
		`"x"`, `"hello world"`, `"` + jsonEscape + `u00e9"`, `"é"`, `"` + jsonEscape + jsonEscape + `"`,
		`""`, "\"\xff\xff\xff\xff\xff\"", `"a long string of text"`,
		`"` + jsonEscape + `ud83d` + jsonEscape + `ude00"`, `"` + structureEmoji + `"`, `"` + jsonEscape + `ud800"`,
		`"` + jsonEscape + `u0000"`, "\"\x00\"", "\"\x01\"", "\"\t\"", "\"\x7f\"", `"` + jsonEscape + `u2028"`,
	}

	structureScalars = []string{
		`1`, `-2.5e3`, `true`, `false`, `null`, `0`, `-0`, `1e400`, `0.1e-400`,
		`123456789012345678901234567890`, structureLongNumber,
		// Not JSON.
		`01`, `1.`, `-`, `tru`, `+1`, `1e`, `0x1`, `NaN`,
	}

	structureTrailers = []string{
		" ", "\n", " trailing", ` {"a":1}`, ",", "}", " []", "\x00", `"`, " // note",
	}
)

// randomStructureBody writes a JSON-looking body from a small alphabet, aimed at
// the limits it will be checked against: now and then it is a wide object of as
// many keys as the one pass compares, or one more or less, a list at the item
// limit, a string at the length limit, or a chain of containers as deep as the
// depth limit, one under it and one over it. A body also has, now and then, a
// byte order mark in front of it, content after the end of the value, keys that
// differ in case, a repeated key written once with an escape and once without,
// control bytes, surrogates, NUL and numbers of four hundred digits.
func randomStructureBody(rng *rand.Rand, depth int, limits DecodeLimits, out *strings.Builder) {
	top := depth == 0
	if top && rng.Intn(40) == 0 {
		out.WriteString(string(rune(0xFEFF)))
	}
	if top && rng.Intn(12) == 0 {
		randomDeepStructure(rng, limits.MaxDepth-1+rng.Intn(3), out)
	} else {
		randomStructureValue(rng, depth, limits, out)
	}
	if top && rng.Intn(10) == 0 {
		out.WriteString(structureTrailers[rng.Intn(len(structureTrailers))])
	}
}

func randomStructureValue(rng *rand.Rand, depth int, limits DecodeLimits, out *strings.Builder) {
	switch pick := rng.Intn(10); {
	case pick < 3 && depth < 7:
		randomStructureObject(rng, depth, limits, out)
	case pick < 6 && depth < 7:
		randomStructureList(rng, depth, limits, out)
	case pick < 8:
		out.WriteString(randomStructureString(rng, limits))
	default:
		out.WriteString(structureScalars[rng.Intn(len(structureScalars))])
	}
}

func randomStructureObject(rng *rand.Rand, depth int, limits DecodeLimits, out *strings.Builder) {
	if rng.Intn(15) == 0 {
		// As many keys as the pass compares, one fewer or one more, and now and
		// then one of them again at the end, written plainly or with an escape.
		count := structureWideObjectKeys - 1 + rng.Intn(3)
		out.WriteByte('{')
		for n := 0; n < count; n++ {
			if n > 0 {
				out.WriteByte(',')
			}
			fmt.Fprintf(out, `"wide%04d":%d`, n, n)
		}
		switch rng.Intn(3) {
		case 0:
			fmt.Fprintf(out, `,"wide%04d":0`, rng.Intn(count))
		case 1:
			fmt.Fprintf(out, `,"wid`+jsonEscape+`u0065%04d":0`, rng.Intn(count))
		}
		out.WriteByte('}')
		return
	}
	out.WriteByte('{')
	for n, count := 0, rng.Intn(6); n < count; n++ {
		if n > 0 {
			out.WriteByte(',')
		}
		out.WriteString(structureKeys[rng.Intn(len(structureKeys))])
		out.WriteString(":")
		randomStructureValue(rng, depth+1, limits, out)
	}
	out.WriteByte('}')
}

func randomStructureList(rng *rand.Rand, depth int, limits DecodeLimits, out *strings.Builder) {
	out.WriteByte('[')
	if count := limits.MaxArrayItems - 1 + rng.Intn(3); rng.Intn(6) == 0 && count >= 0 && count <= 300 {
		// A list at the item limit, one under it or one over it.
		for n := 0; n < count; n++ {
			if n > 0 {
				out.WriteByte(',')
			}
			out.WriteString(structureScalars[rng.Intn(len(structureScalars))])
		}
	} else {
		for n, count := 0, rng.Intn(6); n < count; n++ {
			if n > 0 {
				out.WriteString(" , ")
			}
			randomStructureValue(rng, depth+1, limits, out)
		}
	}
	out.WriteByte(']')
}

// randomStructureString is a string value: usually one of the fixed words, and
// now and then one of the length limit, one under it or one over it, made of
// characters of one to four bytes and of bytes that are not UTF-8.
func randomStructureString(rng *rand.Rand, limits DecodeLimits) string {
	size := limits.MaxStringBytes - 1 + rng.Intn(3)
	if rng.Intn(6) != 0 || size < 0 || size > 300 {
		return structureWords[rng.Intn(len(structureWords))]
	}
	unit := []string{"x", "é", "日", structureEmoji, "\xff"}[rng.Intn(5)]
	var text strings.Builder
	for text.Len() < size {
		if size-text.Len() >= len(unit) {
			text.WriteString(unit)
		} else {
			text.WriteByte('x')
		}
	}
	return `"` + text.String() + `"`
}

// randomDeepStructure writes containers nested the given number of levels, each
// a list or an object, with the odd sibling beside the one that goes deeper.
func randomDeepStructure(rng *rand.Rand, levels int, out *strings.Builder) {
	if levels <= 0 {
		out.WriteString(structureScalars[rng.Intn(len(structureScalars))])
		return
	}
	if rng.Intn(2) == 0 {
		out.WriteByte('[')
		for n := rng.Intn(2); n > 0; n-- {
			out.WriteString(`1,`)
		}
		randomDeepStructure(rng, levels-1, out)
		out.WriteByte(']')
		return
	}
	out.WriteByte('{')
	for n := rng.Intn(2); n > 0; n-- {
		out.WriteString(`"s":1,`)
	}
	out.WriteString(structureKeys[rng.Intn(len(structureKeys))])
	out.WriteByte(':')
	randomDeepStructure(rng, levels-1, out)
	out.WriteByte('}')
}

// randomStructureLimits are limits small enough to be met by a body of a few
// bytes, now and then the defaults, now and then a depth limit near the default
// one so that a deep body is at it, and now and then a length limit of a few
// hundred bytes.
func randomStructureLimits(rng *rand.Rand) DecodeLimits {
	switch rng.Intn(8) {
	case 0:
		return DefaultDecodeLimits()
	case 1:
		return DecodeLimits{MaxDepth: 56 + rng.Intn(14), MaxArrayItems: 1 + rng.Intn(8), MaxStringBytes: 1 + rng.Intn(24)}.withDefaults()
	case 2:
		return DecodeLimits{MaxDepth: 1 + rng.Intn(10), MaxArrayItems: 1 + rng.Intn(8), MaxStringBytes: 1 + rng.Intn(300)}.withDefaults()
	default:
		return DecodeLimits{MaxDepth: 1 + rng.Intn(10), MaxArrayItems: 1 + rng.Intn(8), MaxStringBytes: 1 + rng.Intn(24)}.withDefaults()
	}
}

// structureNesting is how deep the containers of a body nest, read from the
// brackets that are not inside a string.
func structureNesting(body string) int {
	var depth, deepest int
	inString := false
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case inString && c == '\\':
			i++
		case c == '"':
			inString = !inString
		case inString:
		case c == '{' || c == '[':
			depth++
			deepest = max(deepest, depth)
		case c == '}' || c == ']':
			depth--
		}
	}
	return deepest
}

// isLimitFault is whether a refusal is for a limit and not for a repeated key or
// text that is not JSON.
func isLimitFault(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "nests deeper") || strings.Contains(text, " items") || strings.Contains(text, "-byte limit")
}

// The one pass answers for the limits, so it must never clear a body that is past
// one, and it must clear every body the token walk accepts, which is what keeps
// the decoder from being asked to read an answer twice. Seeded bodies, some of
// them damaged after they are written, under limits small enough to be met, are
// held to that, and the walk that says what is wrong is held to the answer of the
// walk it replaced, message for message. The bodies are also counted by what they
// hold, and a count that is too low fails the test: a generator that stopped
// producing a kind of body would otherwise leave the property standing over
// nothing.
func TestStructureWithinLimitsAgreesWithTheTokenWalkOnTheLimits(t *testing.T) {
	rng := rand.New(rand.NewSource(20261003))
	var cleared, refused int
	reached := map[string]int{}
	note := func(kind string, held bool) {
		if held {
			reached[kind]++
		}
	}
	for round := 0; round < 12000; round++ {
		var body strings.Builder
		limits := randomStructureLimits(rng)
		randomStructureBody(rng, 0, limits, &body)
		text := damagedStructureBody(rng, round, body.String())

		want := referenceCheckStructureByTokens(text, limits)
		within := structureWithinLimits(text, limits)
		if within {
			cleared++
			if isLimitFault(want) {
				t.Fatalf("the pass cleared a body that is past a limit (%v)\nbody: %q\nlimits: %+v", want, text, limits)
			}
		} else {
			refused++
			if want == nil && json.Valid([]byte(text)) {
				t.Fatalf("the pass did not clear a body the walk accepts\nbody: %q\nlimits: %+v", text, limits)
			}
		}

		got := checkStructure(text, limits)
		if (got == nil) != (want == nil) || (got != nil && (types.KindOf(got) != types.KindOf(want) || got.Error() != want.Error())) {
			t.Fatalf("checkStructure = %v, the walk it replaced gave %v\nbody: %q\nlimits: %+v", got, want, text, limits)
		}

		message := ""
		if want != nil {
			message = want.Error()
		}
		nesting := structureNesting(text)
		note("refused for nesting past the limit", strings.Contains(message, "nests deeper"))
		note("refused for a list past the item limit", strings.Contains(message, "items"))
		note("refused for a string past the length limit", strings.Contains(message, "-byte limit"))
		note("refused for a repeated key", strings.Contains(message, "repeats the key"))
		note("refused as not JSON", strings.Contains(message, "not well-formed"))
		note("cleared with nesting at the limit", within && nesting == limits.MaxDepth)
		note("cleared with nesting one under the limit", within && nesting == limits.MaxDepth-1)
		note("nesting one over the limit", nesting == limits.MaxDepth+1)
		note("an object of the most keys the generator writes", strings.Contains(text, `"wide0255"`) && !strings.Contains(text, `"wide0256"`))
		note("an object of one key more than that", strings.Contains(text, `"wide0256"`))
		note("keys that differ only in case", strings.Contains(text, `"Key"`) && strings.Contains(text, `"key"`))
		note("a key written with an escape beside the same key written plainly", strings.Contains(text, `"ke`+jsonEscape+`u0079"`) && strings.Contains(text, `"key"`))
		note("a surrogate pair", strings.Contains(text, jsonEscape+`ud83d`+jsonEscape+`ude00`))
		note("an escaped NUL", strings.Contains(text, jsonEscape+`u0000`))
		note("a raw control byte", strings.ContainsAny(text, "\x00\x01\t\x7f"))
		note("a byte order mark", strings.HasPrefix(text, string(rune(0xFEFF))))
		note("a number of four hundred digits", strings.Contains(text, structureLongNumber))
		note("a body the walk accepts", want == nil)
	}
	t.Logf("%d bodies cleared, %d not; reached %v", cleared, refused, reached)
	if cleared < 200 || refused < 200 {
		t.Errorf("the bodies did not exercise both answers: %d cleared, %d not", cleared, refused)
	}
	for _, kind := range structureReachKinds {
		if reached[kind] < structureReachMinimum {
			t.Errorf("only %d of the bodies had this: %s (at least %d wanted)", reached[kind], kind, structureReachMinimum)
		}
	}
}

// structureReachKinds are the kinds of body the generator has to produce.
var structureReachKinds = []string{
	"refused for nesting past the limit", "refused for a list past the item limit",
	"refused for a string past the length limit", "refused for a repeated key", "refused as not JSON",
	"cleared with nesting at the limit", "cleared with nesting one under the limit", "nesting one over the limit",
	"an object of the most keys the generator writes", "an object of one key more than that",
	"keys that differ only in case", "a key written with an escape beside the same key written plainly",
	"a surrogate pair", "an escaped NUL", "a raw control byte", "a byte order mark",
	"a number of four hundred digits", "a body the walk accepts",
}

// damagedStructureBody leaves every fourth body as it is written and drops,
// repeats or swaps a byte of the others.
func damagedStructureBody(rng *rand.Rand, round int, text string) string {
	if round%4 != 3 || len(text) == 0 {
		return text
	}
	bytes := []byte(text)
	at := rng.Intn(len(bytes))
	switch rng.Intn(3) {
	case 0:
		bytes = append(bytes[:at], bytes[at+1:]...)
	case 1:
		bytes = append(bytes[:at+1], bytes[at:]...)
	default:
		const damage = "{}[]\",:\\ x1\x00\x01\xff"
		bytes[at] = damage[rng.Intn(len(damage))]
	}
	return string(bytes)
}

// structureReachMinimum is how many of the generated bodies must hold each kind
// of thing the generator is meant to produce.
const structureReachMinimum = 10

func TestStructureWithinLimitsAllocatesNothing(t *testing.T) {
	body := benchAnswerFor(t, reflect.TypeFor[benchNested]())
	limits := DefaultDecodeLimits()
	if !structureWithinLimits(body, limits) {
		t.Fatal("the pass does not clear an ordinary answer")
	}
	// The walk by tokens made about 1,350 allocations for an answer of this size,
	// and the pass that came before this one two, for the copy json.Valid read.
	if allocs := testing.AllocsPerRun(100, func() { _ = structureWithinLimits(body, limits) }); allocs != 0 {
		t.Errorf("checking the limits of an ordinary answer makes %.0f allocations, want none", allocs)
	}
}

// What an answer that fits costs the exact decoder, counting the new value it is
// read into. On Go 1.27.1 the decode makes 3 allocations for the two small
// answers and 78 for the list of 5 KB, which is the 77 that encoding/json makes
// for it by itself and one more for the copy of the text the decoder reads. Each
// budget is that plus two. Before the decoder ran on encoding/json/v2 it made 51,
// 87 and 2,186 on this toolchain, from a Decoder and the two readings of the
// answer for repeated keys and numbers.
func TestDecodeExactAllocationsStayWithinBudget(t *testing.T) {
	budgets := map[string]float64{"flat3_60B": 5, "record_200B": 5, "list_5KB": 80}
	checked := 0
	for _, answer := range benchAnswers() {
		budget, ok := budgets[answer.name]
		if !ok {
			continue
		}
		checked++
		decode := func() {
			if err := DecodeExact(answer.body, answer.target(), DefaultDecodeLimits()); err != nil {
				t.Fatal(err)
			}
		}
		decode()
		if allocs := testing.AllocsPerRun(20, decode); allocs > budget {
			t.Errorf("DecodeExact of %s makes %.0f allocations, over its budget of %.0f", answer.name, allocs, budget)
		}
	}
	if checked != len(budgets) {
		t.Errorf("%d of the %d answers with a budget were found", checked, len(budgets))
	}
}

// exactComparisonTarget is somewhere to read a body into.
type exactComparisonTarget struct {
	name string
	make func() any
}

// referenceWide has members that hold anything, so that a body is refused by the
// decoder for its text and for the names it uses and not for its types, and
// members whose names differ from one another only in case.
type referenceWide struct {
	A   any `json:"a"`
	B   any `json:"b"`
	C   any `json:"c"`
	Key any `json:"key"`
	Raw any `json:"raw"`
}

func exactComparisonTargets() []exactComparisonTarget {
	return []exactComparisonTarget{
		{"any", func() any { return new(any) }},
		{"a map", func() any { return new(map[string]any) }},
		{"a list", func() any { return new([]any) }},
		{"members that hold anything", func() any { return new(referenceWide) }},
		{"a struct with types", func() any { return new(exactTarget) }},
	}
}

// exactDisagreement runs a body through the exact decoder and through the one it
// replaced and says how they differ: not at all (empty), in whether they accept
// it, in the kind of error, or in what they decode.
func exactDisagreement(input string, limits DecodeLimits, target exactComparisonTarget) (class, detail string) {
	oldTarget, newTarget := target.make(), target.make()
	oldErr := referenceDecodeExact(input, oldTarget, limits)
	newErr := DecodeExact(input, newTarget, limits)

	switch {
	case oldErr == nil && newErr == nil:
		if !reflect.DeepEqual(oldTarget, newTarget) {
			return "accepted by both and decoded differently", fmt.Sprintf("old %#v, new %#v", oldTarget, newTarget)
		}
		return "", ""
	case oldErr != nil && newErr == nil:
		return "accepted where the old decoder refused", oldErr.Error()
	case oldErr == nil:
		return "refused where the old decoder accepted", newErr.Error()
	}
	if oldKind, newKind := types.KindOf(oldErr), types.KindOf(newErr); oldKind != newKind {
		return fmt.Sprintf("refused with another kind (was %v, is %v)", oldKind, newKind), fmt.Sprintf("old: %v; new: %v", oldErr, newErr)
	}
	return "", ""
}

// The old decoder is the measure of the new: a body it refused is never accepted,
// and one both refuse is refused with the same kind of error, so a caller that
// repairs one kind of failure by asking again repairs the same. Bodies from the
// generator of the structure tests are read into each kind of target.
func TestDecodeExactNeverAcceptsWhatTheReferenceRefused(t *testing.T) {
	rng := rand.New(rand.NewSource(20261003))
	targets := exactComparisonTargets()
	disagreements := map[string][]string{}
	var accepted, refused int
	for round := 0; round < 12000; round++ {
		var body strings.Builder
		limits := randomStructureLimits(rng)
		randomStructureBody(rng, 0, limits, &body)
		text := damagedStructureBody(rng, round, body.String())

		for _, target := range targets {
			class, detail := exactDisagreement(text, limits, target)
			if class != "" {
				disagreements[class] = append(disagreements[class], fmt.Sprintf("%s: %q under %+v: %s", target.name, text, limits, detail))
			}
		}
		if err := DecodeExact(text, new(any), limits); err == nil {
			accepted++
		} else {
			refused++
		}
	}
	t.Logf("%d bodies accepted into any, %d refused", accepted, refused)
	for class, found := range disagreements {
		t.Logf("%s: %d of %d reads, for example\n\t%s", class, len(found), 12000*len(targets), strings.Join(found[:min(len(found), 3)], "\n\t"))
	}
	if accepted < 1000 || refused < 1000 {
		t.Errorf("the bodies did not exercise both answers: %d accepted, %d refused", accepted, refused)
	}
	for class, found := range disagreements {
		switch class {
		case "refused where the old decoder accepted":
			for _, example := range found {
				if !strings.Contains(example, "differ only in the case") {
					t.Errorf("%s, and not for names that differ in case: %s", class, example)
				}
			}
		default:
			t.Errorf("%s: %d times, for example %s", class, len(found), found[0])
		}
	}
}

// referenceLegacy has a member for each place where encoding/json reads one way
// and encoding/json/v2 by itself another.
type referenceLegacy struct {
	Wait   time.Duration      `json:"wait"`
	At     time.Time          `json:"at"`
	Count  int                `json:"count,string"`
	Ratio  float64            `json:"ratio"`
	Big    json.Number        `json:"big"`
	Tiny   float32            `json:"tiny"`
	Note   *string            `json:"note"`
	Tags   []string           `json:"tags"`
	Raw    json.RawMessage    `json:"raw"`
	Blob   []byte             `json:"blob"`
	Extra  map[string]float64 `json:"extra"`
	Any    any                `json:"any"`
	Inner  referenceInner     `json:"inner"`
	Plain  string
	Number int
}

type referenceInner struct {
	Name string `json:"name"`
}

// The bodies a person would think of, each read by the old exact decoder and the
// new into each kind of target. The two have to agree on whether the body is
// accepted, on what it decodes to, and on the kind of error when it is not. A
// body marked as repeating a member by case is the one place they differ, and
// the new one refuses it.
func TestDecodeExactAgreesWithTheReferenceOnHandWrittenBodies(t *testing.T) {
	legacyFull := `{"wait":1500,"at":"2026-10-03T12:00:00Z","count":"5","ratio":0.5,"big":123456789012345678901234567890,` +
		`"tiny":1.5,"note":null,"tags":["a","b"],"raw":{"x":1.50},"blob":"AQID","extra":{"k":1,"j":2.25},"any":{"n":1.50,"l":[1,2.0]},` +
		`"inner":{"name":"n"},"Plain":"p","Number":3}`
	deepBody := func(levels int) string { return strings.Repeat(`{"a":`, levels) + "1" + strings.Repeat("}", levels) }
	listOf := func(items int) string { return "[" + strings.TrimSuffix(strings.Repeat("1,", items), ",") + "]" }

	cases := []struct {
		name   string
		body   string
		limits DecodeLimits
		// foldsCase marks a body that repeats a member under names that differ
		// in case.
		foldsCase bool
	}{
		{name: "an answer that fits", body: `{"name":"Ada","total":12.5,"items":[{"id":1,"price":2.5},{"id":2,"price":10}]}`},
		{name: "an answer in a fence", body: "```json\n{\"name\":\"Ada\",\"total\":1,\"items\":[]}\n```"},
		{name: "an answer in prose", body: "Here you go: {\"name\":\"Ada\",\"total\":1,\"items\":[]} Hope that helps."},
		{name: "an answer in a fence with a comment after", body: "```\n{\"name\":\"Ada\",\"total\":1,\"items\":[]}\n```\nNote: totals are in dollars."},
		{name: "names in capitals", body: `{"NAME":"Ada","TOTAL":1,"ITEMS":[]}`},
		{name: "names in mixed case", body: `{"Name":"Ada","tOtAl":1,"iTems":[]}`},
		{name: "every kind of member", body: legacyFull},
		{name: "every kind of member in capitals", body: strings.ToUpper(legacyFull)},
		{name: "a duration as a string", body: `{"wait":"1.5s"}`},
		{name: "a time that is not RFC 3339", body: `{"at":"2026-10-03"}`},
		{name: "a loose time", body: `{"at":"2026-10-03T12:00:00+0000"}`},
		{name: "a count that is not quoted", body: `{"count":5}`},
		{name: "bytes that are not base64", body: `{"blob":"!!!"}`},
		{name: "bytes as a list", body: `{"blob":[1,2,3]}`},
		{name: "null for everything", body: `{"wait":null,"at":null,"count":null,"ratio":null,"big":null,"note":null,"tags":null,"raw":null,"blob":null,"extra":null,"any":null,"inner":null}`},
		{name: "a top-level null", body: `null`},
		{name: "a top-level number", body: `42`},
		{name: "a top-level string", body: `"text"`},
		{name: "a top-level true", body: `true`},
		{name: "a top-level list", body: `[1,2.50,"x",null,{"a":1}]`},
		{name: "an empty list", body: `[]`},
		{name: "an empty object", body: `{}`},
		{name: "numbers in every form", body: `[0,-0,1e2,1E+2,1.0,0.5,-0.5e-3,123456789012345678901234567890,0.1234567890123456789012345]`},
		{name: "a number float64 cannot hold", body: `{"ratio":9007199254740993}`},
		{name: "a number float64 cannot hold, in a list", body: `{"extra":{"k":0.12345678901234567890123}}`},
		{name: "a number that gains digits", body: `{"ratio":0.30000000000000004}`},
		{name: "an integer that overflows", body: `{"items":[{"id":99999999999999999999}]}`},
		{name: "a fraction in an integer", body: `{"items":[{"id":1.5}]}`},
		{name: "an exponent in an integer", body: `{"items":[{"id":1e2}]}`},
		{name: "a string for a number", body: `{"name":"Ada","total":"free","items":[]}`},
		{name: "a number for a string", body: `{"name":5}`},
		{name: "a list for an object", body: `{"inner":[]}`},
		{name: "an object for a list", body: `{"tags":{}}`},
		{name: "a member the target does not have", body: `{"name":"Ada","total":1,"items":[],"invented":"value"}`},
		{name: "a member the target does not have, nested", body: `{"inner":{"name":"n","invented":1}}`},
		{name: "a member the target does not have, in a list", body: `{"items":[{"id":1,"invented":1}]}`},
		{name: "a member written in capitals the target does not have", body: `{"INVENTED":1}`},
		{name: "a repeated key", body: `{"name":"Ada","name":"Grace","total":1,"items":[]}`},
		{name: "a repeated key in a nested object", body: `{"inner":{"name":"a","name":"b"}}`},
		{name: "a repeated key in a list", body: `{"items":[{"id":1},{"id":2,"id":3}]}`},
		{name: "a repeated key at depth four", body: `{"a":{"b":{"c":{"d":1,"d":2}}}}`},
		{name: "a repeated key written with an escape", body: `{"name":"a","n` + jsonEscape + `u0061me":"b"}`},
		{name: "a repeated key that is not a member", body: `{"zz":1,"zz":2}`},
		{name: "a repeated key in the map", body: `{"extra":{"k":1,"k":2}}`},
		{name: "a repeated key inside a value held as any", body: `{"any":{"k":1,"k":2}}`},
		{name: "a repeated key inside raw JSON", body: `{"raw":{"k":1,"k":2}}`},
		{name: "a key repeated in capitals", body: `{"name":"a","NAME":"b"}`, foldsCase: true},
		{name: "a key repeated in mixed case after other members", body: `{"total":1,"name":"a","items":[],"Name":"b"}`, foldsCase: true},
		{name: "two values", body: `{"name":"Ada","total":1,"items":[]} {"name":"Grace","total":2,"items":[]}`},
		{name: "two values in prose", body: "First {\"name\":\"a\"} then {\"name\":\"b\"}"},
		{name: "a second list", body: `[1] [2]`},
		{name: "a value and a word", body: `{"name":"a"} thanks`},
		{name: "a value and a trailing comma", body: `{"name":"a"},`},
		{name: "a trailing comma inside", body: `{"name":"a",}`},
		{name: "a trailing comma in a list", body: `[1,2,]`},
		{name: "single quotes", body: `{'name':'a'}`},
		{name: "a comment", body: `{"name":"a"} // done`},
		{name: "an unquoted key", body: `{name:"a"}`},
		{name: "NaN", body: `{"ratio":NaN}`},
		{name: "a leading zero", body: `{"ratio":01}`},
		{name: "a number with a trailing dot", body: `{"ratio":1.}`},
		{name: "a plus sign", body: `{"ratio":+1}`},
		{name: "truncated after a member", body: `{"name":"a","total":1`},
		{name: "truncated after a comma", body: `{"name":"a",`},
		{name: "truncated after a colon", body: `{"name":`},
		{name: "truncated in a string", body: `{"name":"ab`},
		{name: "truncated in a number", body: `[1,2.5e`},
		{name: "truncated in a literal", body: `{"ratio":tru`},
		{name: "an object that is only opened", body: `{`},
		{name: "a list that is only opened", body: `[`},
		{name: "a bare quote", body: `"`},
		{name: "empty", body: ``},
		{name: "white space only", body: " \n\t "},
		{name: "prose", body: `I could not find an invoice in that text.`},
		{name: "a byte order mark", body: string(rune(0xFEFF)) + `{"name":"a"}`},
		{name: "a NUL in a string", body: "{\"name\":\"a\x00b\"}"},
		{name: "an escaped NUL", body: `{"name":"a` + jsonEscape + `u0000b"}`},
		{name: "a raw tab in a string", body: "{\"name\":\"a\tb\"}"},
		{name: "a lone surrogate", body: `{"name":"` + jsonEscape + `ud800"}`},
		{name: "a bad escape", body: `{"name":"` + jsonEscape + `x"}`},
		{name: "a name that is not UTF-8", body: "{\"na\xffme\":1}"},
		{name: "text that is not UTF-8", body: "{\"name\":\"\xff\xfe\"}"},
		{name: "two names that are not UTF-8 and read alike", body: "{\"a\xff\":1,\"a\xfe\":2}"},
		{name: "nesting at the limit", body: deepBody(64)},
		{name: "nesting one past the limit", body: deepBody(65)},
		{name: "nesting at a limit of three", body: deepBody(3), limits: DecodeLimits{MaxDepth: 3}},
		{name: "nesting past a limit of three", body: deepBody(4), limits: DecodeLimits{MaxDepth: 3}},
		{name: "a list at the limit", body: listOf(10), limits: DecodeLimits{MaxArrayItems: 10}},
		{name: "a list past the limit", body: listOf(11), limits: DecodeLimits{MaxArrayItems: 10}},
		{name: "a list at the default limit", body: listOf(100000)},
		{name: "a list past the default limit", body: listOf(100001)},
		{name: "a string at the limit", body: `{"name":"` + strings.Repeat("x", 100) + `"}`, limits: DecodeLimits{MaxStringBytes: 100}},
		{name: "a string past the limit", body: `{"name":"` + strings.Repeat("x", 101) + `"}`, limits: DecodeLimits{MaxStringBytes: 100}},
		{name: "a string of escapes at the limit", body: `{"name":"` + strings.Repeat(jsonEscape+"n", 100) + `"}`, limits: DecodeLimits{MaxStringBytes: 100}},
		{name: "a string of escapes past the limit", body: `{"name":"` + strings.Repeat(jsonEscape+"n", 101) + `"}`, limits: DecodeLimits{MaxStringBytes: 100}},
		{name: "a string of bad bytes past the limit", body: `{"name":"` + strings.Repeat("\xff", 40) + `"}`, limits: DecodeLimits{MaxStringBytes: 100}},
		{name: "a body past the limit", body: `{"name":"` + strings.Repeat("x", 100) + `"}`, limits: DecodeLimits{MaxBytes: 50}},
		{name: "a nesting problem before a repeated key", body: `[[[[1]]],{"a":1,"a":2}]`, limits: DecodeLimits{MaxDepth: 3}},
		{name: "a repeated key before a nesting problem", body: `[{"a":1,"a":2},[[[[1]]]]]`, limits: DecodeLimits{MaxDepth: 3}},
		{name: "a repeated key before a list problem", body: `{"a":1,"a":2,"l":[1,2,3,4]}`, limits: DecodeLimits{MaxArrayItems: 3}},
		{name: "a list problem before a repeated key", body: `{"l":[1,2,3,4],"a":1,"a":2}`, limits: DecodeLimits{MaxArrayItems: 3}},
		{name: "a type problem before a repeated key", body: `{"name":5,"total":1,"total":2}`},
		{name: "a repeated key before a type problem", body: `{"total":1,"total":2,"name":5}`},
		{name: "a member the target lacks before text that is not JSON", body: `{"invented":1,"name":}`},
		{name: "a type problem before text that is not JSON", body: `{"name":5,"total":}`},
	}
	if len(cases) < 60 {
		t.Fatalf("only %d cases", len(cases))
	}

	targets := append(exactComparisonTargets(),
		exactComparisonTarget{"members encoding/json/v2 reads another way", func() any { return new(referenceLegacy) }})
	var folded int
	for _, tc := range cases {
		for _, target := range targets {
			t.Run(tc.name+" into "+target.name, func(t *testing.T) {
				class, detail := exactDisagreement(tc.body, tc.limits, target)
				switch {
				case class == "":
				case class == "refused where the old decoder accepted" && tc.foldsCase:
					folded++
					if !strings.Contains(detail, "differ only in the case") {
						t.Errorf("%s, but not for the reason expected: %s", class, detail)
					}
				default:
					t.Errorf("%s: %s", class, detail)
				}
			})
		}
	}
	if folded == 0 {
		t.Error("no body that repeats a member by case was refused; the cases do not hold the difference they name")
	}
}

// A target the body cannot be read into is refused, and as the old decoder
// refused it: with a schema violation, and without having read anything.
func TestDecodeExactRefusesATargetItCannotReadInto(t *testing.T) {
	var value exactTarget
	var nilPointer *exactTarget
	var number int
	cases := []struct {
		name   string
		target any
	}{
		{"a value that is not a pointer", value},
		{"nothing", nil},
		{"a nil pointer", nilPointer},
		{"a map that is not a pointer", map[string]any{}},
		{"a pointer to an integer", &number},
		{"a pointer to a channel", new(chan int)},
		{"a pointer to a function", new(func())},
		{"a pointer to a list of channels", new([]chan int)},
		{"a slice that is not a pointer", []any{}},
		{"a string", "text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := referenceDecodeExact(`{"name":"Ada","total":1,"items":[]}`, tc.target, DecodeLimits{})
			got := DecodeExact(`{"name":"Ada","total":1,"items":[]}`, tc.target, DecodeLimits{})
			if want == nil || got == nil {
				t.Fatalf("DecodeExact = %v, the reference gives %v; both should refuse", got, want)
			}
			if types.KindOf(got) != types.KindOf(want) {
				t.Errorf("kind = %v, the reference gives %v", types.KindOf(got), types.KindOf(want))
			}
		})
	}
}

// The old decoder accepted a body that names one member twice in different
// cases, taking the last, and the new one refuses it, as it refuses a key
// written twice. That is the one body whose verdict moved, and it is a model
// answering a field twice with two values.
func TestDecodeExactRefusesAMemberNamedTwiceInDifferentCases(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		target func() any
		refuse bool
	}{
		{"capitals", `{"name":"a","NAME":"b"}`, func() any { return new(exactTarget) }, true},
		{"capitals first", `{"NAME":"a","name":"b"}`, func() any { return new(exactTarget) }, true},
		{"mixed", `{"Name":"a","nAME":"b"}`, func() any { return new(exactTarget) }, true},
		{"three times", `{"name":"a","Name":"b","NAME":"c"}`, func() any { return new(exactTarget) }, true},
		{"with other members between", `{"name":"a","total":1,"Name":"b","items":[]}`, func() any { return new(exactTarget) }, true},
		{"in a nested struct", `{"inner":{"name":"a","NAME":"b"}}`, func() any { return new(referenceLegacy) }, true},
		{"in an element of a list", `{"items":[{"id":1,"ID":2}]}`, func() any { return new(exactTarget) }, true},
		{"two names for two members", `{"name":"a","total":1}`, func() any { return new(exactTarget) }, false},
		{"the same name in two objects", `{"items":[{"id":1},{"id":2}]}`, func() any { return new(exactTarget) }, false},
		{"in a map, where names are not matched by case", `{"name":"a","NAME":"b"}`, func() any { return new(map[string]string) }, false},
		{"in a value held as any", `{"any":{"name":"a","NAME":"b"}}`, func() any { return new(referenceLegacy) }, false},
		{"for names the target does not have", `{"zz":1,"ZZ":2}`, func() any { return new(map[string]any) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := DecodeExact(tc.body, tc.target(), DecodeLimits{})
			if (err != nil) != tc.refuse {
				t.Fatalf("DecodeExact = %v, want refused = %v", err, tc.refuse)
			}
			if err != nil {
				if kind := types.KindOf(err); kind != types.KindSchemaViolation {
					t.Errorf("kind = %v, want schema violation", kind)
				}
				if !strings.Contains(err.Error(), "differ only in the case") {
					t.Errorf("the error does not say what was repeated: %v", err)
				}
			}
		})
	}
}

// strictDecodeOptions ask encoding/json/v2 for the reading encoding/json has
// always had and three refusals more, so each is checked here against the
// package itself, and so is each place where v2 by itself would read otherwise:
// a toolchain that moves one of them is found here and not in an answer.
func TestStrictDecodeOptionsAreEncodingJSONPlusTheRefusalsOfStrictMode(t *testing.T) {
	type fits struct {
		Name string        `json:"name"`
		Wait time.Duration `json:"wait"`
		Sub  struct {
			Key string `json:"key"`
		} `json:"sub"`
	}
	read := func(body string, options ...jsonv2.Options) (fits, error) {
		var out fits
		err := jsonv2.Unmarshal([]byte(body), &out, options...)
		return out, err
	}
	plain := strictDecodeOptions

	t.Run("a repeated name is refused at every depth, however it is written", func(t *testing.T) {
		for _, body := range []string{
			`{"name":"a","name":"b"}`,
			`{"sub":{"key":"a","key":"b"}}`,
			`{"name":"a","n` + jsonEscape + `u0061me":"b"}`,
			`{"n` + jsonEscape + `u0061me":"a","n` + jsonEscape + `u0061me":"b"}`,
			`{"zz":1,"zz":2}`,
			`{"a":[{"b":{"c":1,"c":2}}]}`,
		} {
			if _, err := read(body, plain); err == nil {
				t.Errorf("%s was read", body)
			}
			var v1 map[string]any
			if err := json.Unmarshal([]byte(body), &v1); err != nil {
				t.Errorf("encoding/json refuses %s, the facts say it reads it: %v", body, err)
			}
		}
	})

	t.Run("v2 names the repeated member in its own error, which is why none is attached", func(t *testing.T) {
		native := jsonv2.JoinOptions(plain, json.ReportErrorsWithLegacySemantics(false))
		_, err := read(`{"secret-name":1,"secret-name":2}`, native)
		if err == nil || !strings.Contains(err.Error(), "secret-name") {
			t.Fatalf("v2 reports %v, the facts say it quotes the member", err)
		}
		_, err = read(`{"secret-name":1,"secret-name":2}`, plain)
		if err == nil || strings.Contains(err.Error(), "secret-name") {
			t.Errorf("under the options of v1 it reports %v, the facts say it does not quote the member", err)
		}
		err = DecodeExact(`{"secret-name":1,"secret-name":2}`, new(any), DecodeLimits{})
		if err == nil || strings.Contains(err.Error(), "jsontext") || errors.Is(err, jsontext.ErrDuplicateName) {
			t.Errorf("DecodeExact reports %v, which carries or wraps the error of the decoder", err)
		}
	})

	t.Run("names are matched ignoring case, as encoding/json matches them", func(t *testing.T) {
		got, err := read(`{"NAME":"a"}`, plain)
		if err != nil || got.Name != "a" {
			t.Errorf("read %+v, %v", got, err)
		}
		if got, err := read(`{"NAME":"a"}`); err != nil || got.Name != "" {
			t.Errorf("v2 by itself read %+v, %v; the facts say it matches names exactly", got, err)
		}
	})

	t.Run("a duration is a number of nanoseconds", func(t *testing.T) {
		got, err := read(`{"wait":1500}`, plain)
		if err != nil || got.Wait != 1500*time.Nanosecond {
			t.Errorf("read %+v, %v", got, err)
		}
		if _, err := read(`{"wait":1500}`); err == nil {
			t.Error("v2 by itself read a duration; the facts say it has no representation for one")
		}
	})

	t.Run("a member the type lacks is refused, and only because strict mode asks", func(t *testing.T) {
		if _, err := read(`{"name":"a","zz":1}`, plain); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("read with %v", err)
		}
		if _, err := read(`{"name":"a","zz":1}`, plain, jsonv2.RejectUnknownMembers(false)); err != nil {
			t.Errorf("the option is what refuses it: %v", err)
		}
	})

	t.Run("bytes that are not UTF-8 are read as U+FFFD", func(t *testing.T) {
		got, err := read("{\"name\":\"a\xffb\"}", plain)
		if err != nil || got.Name != "a"+structureReplacement+"b" {
			t.Errorf("read %q, %v", got.Name, err)
		}
		if _, err := read("{\"name\":\"a\xffb\"}", jsonv2.DefaultOptionsV2()); err == nil {
			t.Error("v2 by itself read text that is not UTF-8")
		}
	})

	t.Run("a value after the first is refused", func(t *testing.T) {
		for _, body := range []string{`{"name":"a"} {"name":"b"}`, `{"name":"a"} x`, `{"name":"a"},`, `{"name":"a"}]`} {
			if _, err := read(body, plain); err == nil {
				t.Errorf("%s was read", body)
			}
		}
	})

	t.Run("a value of another kind than the type reads is refused", func(t *testing.T) {
		for _, body := range []string{`[]`, `"x"`, `1`, `true`} {
			var out fits
			err := jsonv2.Unmarshal([]byte(body), &out, plain)
			var mismatch *json.UnmarshalTypeError
			if !errors.As(err, &mismatch) {
				t.Errorf("%s was read with %v", body, err)
			}
		}
		if _, err := read(`null`, plain); err != nil {
			t.Errorf("null is read as nothing: %v", err)
		}
	})

	t.Run("a number held by an any is its literal", func(t *testing.T) {
		var out any
		if err := jsonv2.Unmarshal([]byte(`{"a":1.50,"b":[2.0,{"c":3e2}]}`), &out, plain); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"a": json.Number("1.50"), "b": []any{json.Number("2.0"), map[string]any{"c": json.Number("3e2")}}}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("read %#v, want %#v", out, want)
		}
	})
}

// readAnyExact is what Decoder.UseNumber was, so each value is read by both and
// the results are the same.
func TestReadAnyExactReadsAsDecoderUseNumberDoes(t *testing.T) {
	bodies := []string{
		`null`, `true`, `false`, `"text"`, `""`, `0`, `-0`, `1.50`, `2e10`, `123456789012345678901234567890`,
		`[]`, `{}`, `[1,2.0,"x",null,[],{}]`, `{"a":{"b":{"c":[1.10,{"d":null}]}}}`,
		`{"é":"ü","k":"` + jsonEscape + `u00e9` + jsonEscape + `n"}`, "[\"\xff\"]", `{"a":[[[[1.0]]]]}`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			var want any
			decoder := json.NewDecoder(strings.NewReader(body))
			decoder.UseNumber()
			if err := decoder.Decode(&want); err != nil {
				t.Fatal(err)
			}
			var got any
			if err := jsonv2.Unmarshal([]byte(body), &got, strictDecodeOptions); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("read %#v, Decoder.UseNumber reads %#v", got, want)
			}
		})
	}

	t.Run("in a slice of any, a map of any and a pointer to any", func(t *testing.T) {
		var out struct {
			List []any          `json:"list"`
			Map  map[string]any `json:"map"`
			Ptr  *any           `json:"ptr"`
		}
		if err := jsonv2.Unmarshal([]byte(`{"list":[1.0],"map":{"k":2.0},"ptr":3.0}`), &out, strictDecodeOptions); err != nil {
			t.Fatal(err)
		}
		if out.List[0] != json.Number("1.0") || out.Map["k"] != json.Number("2.0") || *out.Ptr != json.Number("3.0") {
			t.Errorf("read %+v", out)
		}
	})

	t.Run("an error in the middle of the value", func(t *testing.T) {
		for _, body := range []string{`[1,`, `{"a":`, `{"a":1,"a":2}`, `[1 2]`, `{"a" 1}`} {
			var out any
			if err := jsonv2.Unmarshal([]byte(body), &out, strictDecodeOptions); err == nil {
				t.Errorf("%s was read as %#v", body, out)
			}
		}
	})
}

// A decoded value written back is the answer when the answer fits, and
// numbersAgree reads the two side by side to see it. It answers true only when
// the numbers compared are the same value, and it answers false for anything it
// cannot follow, so each case says what it must answer and the property test
// below says that a true is never wrong.
func TestNumbersAgreeReadsTwoTextsSideBySide(t *testing.T) {
	cases := []struct {
		name       string
		original   string
		reEncoded  string
		wantAgrees bool
	}{
		{"the same text", `{"a":1,"b":[2,3]}`, `{"a":1,"b":[2,3]}`, true},
		{"white space differs", " {\n\"a\" : 1 ,\"b\":[ 2 , 3 ] }", `{"a":1,"b":[2,3]}`, true},
		{"a number with a trailing zero", `{"a":1.50}`, `{"a":1.5}`, true},
		{"a number with an exponent", `{"a":5e-1}`, `{"a":0.5}`, true},
		{"a number a float cannot hold", `{"a":9007199254740993}`, `{"a":9007199254740992}`, false},
		{"a number with more digits than a float holds", `{"a":0.12345678901234567890123}`, `{"a":0.12345678901234568}`, false},
		{"a negative zero", `{"a":-0}`, `{"a":0}`, true},
		{"a string written another way", `{"a":"` + jsonEscape + `u00e9"}`, `{"a":"é"}`, true},
		{"a string with other text", `{"a":"x"}`, `{"a":"y"}`, true},
		{"a key written another way", `{"` + jsonEscape + `u0061":1}`, `{"a":1}`, false},
		{"a key in another case", `{"A":1}`, `{"a":1}`, false},
		{"keys in another order", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"keys in another order, nested", `{"x":{"a":1.50,"b":[{"c":2,"d":3}]},"y":4}`, `{"y":4,"x":{"b":[{"d":3,"c":2}],"a":1.5}}`, true},
		{"keys in another order, with a number that differs", `{"x":{"a":9007199254740993,"b":2}}`, `{"x":{"b":2,"a":9007199254740992}}`, false},
		{"a key twice in the original", `{"a":1,"a":2}`, `{"a":2,"b":1}`, false},
		{"a duplicate key cannot reuse one member", `{"a":1,"a":1}`, `{"a":1,"b":2}`, false},
		{"a key in the re-encoded text that the original lacks", `{"a":1,"b":2}`, `{"a":1,"c":2}`, false},
		{"sixty-four members in another order", wideObject(64), reversedWideObject(64), true},
		{"sixty-five members", wideObject(65), wideObject(65), false},
		{"a member left out", `{"a":1,"b":2}`, `{"a":1}`, false},
		{"a member added", `{"a":1}`, `{"a":1,"b":2}`, false},
		{"a list that is shorter", `[1,2,3]`, `[1,2]`, false},
		{"a list that is longer", `[1,2]`, `[1,2,3]`, false},
		{"a number where there is a string", `{"a":1}`, `{"a":"1"}`, false},
		{"a string where there is a number", `{"a":"1"}`, `{"a":1}`, false},
		{"null where there is a list", `{"a":[]}`, `{"a":null}`, false},
		{"an object where there is a list", `{"a":[]}`, `{"a":{}}`, false},
		{"true against true", `{"a":true,"b":false,"c":null}`, `{"a":true,"b":false,"c":null}`, true},
		{"true against false", `{"a":true}`, `{"a":false}`, false},
		{"a number against a word", `{"a":1}`, `{"a":true}`, false},
		{"a top-level number", `42`, `42`, true},
		{"a top-level string", `"x"`, `"y"`, true},
		{"a top-level list of lists", `[[1],[2,[3]]]`, `[[1],[2,[3]]]`, true},
		{"empty containers", `{"a":{},"b":[],"c":[{}]}`, `{"a":{},"b":[],"c":[{}]}`, true},
		{"a brace inside a string", `{"a":"}{]["}`, `{"a":"}{]["}`, true},
		{"an escaped quote inside a string", `{"a":"x\"y","b":1}`, `{"a":"x\"y","b":1}`, true},
		{"an escaped quote inside a key", `{"k\"":1}`, `{"k\"":1}`, true},
		{"nothing", ``, ``, true},
		{"the original ends first", `{"a":1}`, `{"a":1}{`, false},
		{"the re-encoded text ends first", `{"a":1}{`, `{"a":1}`, false},
		{"text that is not JSON", `not json`, `not json`, false},
		{"a closing bracket with nothing open", `1]`, `1]`, false},
		{"a comma at the top", `1,2`, `1,2`, false},
		{"an unterminated string", `{"a":"x`, `{"a":"x`, false},
		{"nesting of thirty-two levels", strings.Repeat("[", 32) + "1" + strings.Repeat("]", 32), strings.Repeat("[", 32) + "1" + strings.Repeat("]", 32), true},
		{"nesting of thirty-three levels", strings.Repeat("[", 33) + "1" + strings.Repeat("]", 33), strings.Repeat("[", 33) + "1" + strings.Repeat("]", 33), false},
		{"a literal neither side can parse", `{"a":1.}`, `{"a":1.}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := numbersAgree(tc.original, []byte(tc.reEncoded)); got != tc.wantAgrees {
				t.Errorf("numbersAgree = %v, want %v", got, tc.wantAgrees)
			}
		})
	}
}

// numericLossBetween is what the comparison by decoding finds in two texts.
func numericLossBetween(original, reEncoded string) bool {
	first, ok := decodeExactNumbers(original)
	if !ok {
		return false
	}
	second, ok := decodeExactNumbers(reEncoded)
	if !ok {
		return false
	}
	_, _, _, lost := findNumericLoss(first, second, nil)
	return lost
}

// A true from numbersAgree is never wrong: for texts made from the bodies of the
// generator and from each body written back by a decoder that loses precision,
// that sorts keys, that drops members and that changes a number, whenever the
// side-by-side reading says the texts agree, the comparison by decoding finds no
// loss in them.
func TestNumbersAgreeNeverClearsATextWithALoss(t *testing.T) {
	rng := rand.New(rand.NewSource(20261003))
	var agreed, differed, lossFound int
	for round := 0; round < 6000; round++ {
		var body strings.Builder
		limits := DecodeLimits{MaxDepth: 8, MaxArrayItems: 6, MaxStringBytes: 30}.withDefaults()
		randomStructureBody(rng, 0, limits, &body)
		original := body.String()
		if !json.Valid([]byte(original)) {
			continue
		}

		var viaFloats any
		_ = json.Unmarshal([]byte(original), &viaFloats)
		writtenBack, _ := json.Marshal(viaFloats)

		var viaNumbers any
		decoder := json.NewDecoder(strings.NewReader(original))
		decoder.UseNumber()
		_ = decoder.Decode(&viaNumbers)
		kept, _ := json.Marshal(viaNumbers)

		changed := regexpNumber.ReplaceAllStringFunc(original, func(number string) string {
			if rng.Intn(3) == 0 {
				return number + "1"
			}
			return number
		})

		for _, other := range []string{string(writtenBack), string(kept), original, changed, strings.ReplaceAll(original, " ", "")} {
			agrees := numbersAgree(original, []byte(other))
			loss := numericLossBetween(original, other)
			if agrees {
				agreed++
				if loss {
					t.Fatalf("numbersAgree cleared a text that loses a number\noriginal:   %q\nre-encoded: %q", original, other)
				}
			} else {
				differed++
			}
			if loss {
				lossFound++
			}
		}
	}
	t.Logf("%d pairs agree, %d do not, %d lose a number", agreed, differed, lossFound)
	if agreed < 500 || differed < 500 || lossFound < 50 {
		t.Errorf("the texts did not exercise both answers: %d agree, %d do not, %d lose a number", agreed, differed, lossFound)
	}
}

// regexpNumber finds a number in JSON text, including one inside a string, which
// the property test above does not mind: it perturbs text and then asks both ways
// of comparing it.
var regexpNumber = regexp.MustCompile(`-?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?`)

// The check for a loss gives the answer the old one gave, for each kind of
// value: one that fits, one that does not, and the shapes that bring the
// comparison by decoding into it.
func TestCheckNumericFidelityGivesTheAnswerOfTheReference(t *testing.T) {
	type line struct {
		Amount float64 `json:"amount"`
		Note   string  `json:"note,omitempty"`
	}
	type record struct {
		Count int     `json:"count"`
		Price float64 `json:"price"`
		Lines []line  `json:"lines"`
		Any   any     `json:"any"`
		Big   json.Number
		Small float32 `json:"small"`
	}
	cases := []struct {
		name string
		body string
		into func() any
	}{
		{"a record that fits", `{"count":3,"price":1284.5,"lines":[{"amount":1},{"amount":2.5}],"any":{"n":1.5},"Big":12345678901234567890,"small":0.5}`, func() any { return new(record) }},
		{"a price with a trailing zero", `{"count":3,"price":1284.50,"lines":[],"any":null,"Big":1,"small":0}`, func() any { return new(record) }},
		{"a price in the order the type does not write", `{"price":1284.5,"count":3,"lines":[],"any":null,"Big":1,"small":0}`, func() any { return new(record) }},
		{"a price a float cannot hold", `{"count":3,"price":9007199254740993,"lines":[],"any":null,"Big":1,"small":0}`, func() any { return new(record) }},
		{"a line a float cannot hold", `{"count":3,"price":1,"lines":[{"amount":1},{"amount":0.12345678901234567890123}],"any":null,"Big":1,"small":0}`, func() any { return new(record) }},
		{"a member left out by omitempty", `{"count":3,"price":1,"lines":[{"amount":1,"note":""}],"any":null,"Big":1,"small":0}`, func() any { return new(record) }},
		{"a member with an escape in its key", `{"co` + jsonEscape + `u0075nt":3,"price":1,"lines":[],"any":null,"Big":1,"small":0}`, func() any { return new(record) }},
		{"a map, which writes its keys in order", `{"b":1.5,"a":2.5}`, func() any { return new(map[string]float64) }},
		{"a map with a number a float cannot hold", `{"b":1.5,"a":9007199254740993}`, func() any { return new(map[string]float64) }},
		{"a list of numbers", `[1,2.5,3e2]`, func() any { return new([]float64) }},
		{"a list with a number a float cannot hold", `[1,9007199254740993]`, func() any { return new([]float64) }},
		{"a scalar", `42`, func() any { return new(float64) }},
		{"a scalar a float cannot hold", `9007199254740993`, func() any { return new(float64) }},
		{"a body that is not JSON", `not json`, func() any { return new(any) }},
		{"a body with a repeated key", `{"a":1,"a":2}`, func() any { return new(map[string]float64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decoded := tc.into()
			_ = json.Unmarshal([]byte(tc.body), decoded)
			got, want := CheckNumericFidelity(tc.body, decoded), referenceCheckNumericFidelity(tc.body, decoded)
			if (got == nil) != (want == nil) || (got != nil && got.Error() != want.Error()) {
				t.Errorf("CheckNumericFidelity = %v, the reference gives %v", got, want)
			}
		})
	}
}

func TestHexRuneReadsFourHexadecimalDigits(t *testing.T) {
	cases := []struct {
		name string
		text string
		at   int
		want rune
		ok   bool
	}{
		{"digits", "0041", 0, 0x41, true},
		{"lower case", "00e9", 0, 0xE9, true},
		{"upper case", "00E9", 0, 0xE9, true},
		{"all digits set", "ffff", 0, 0xFFFF, true},
		{"after an offset", "xx65e5yy", 2, 0x65E5, true},
		{"only the first four count", "00410042", 0, 0x41, true},
		{"too short", "004", 0, 0, false},
		{"too short after an offset", "xx00", 2, 0, false},
		{"not hexadecimal", "00g1", 0, 0, false},
		{"a sign", "+041", 0, 0, false},
		{"a space", "00 1", 0, 0, false},
		{"past the end", "0041", 4, 0, false},
		{"empty", "", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := hexRune(tc.text, tc.at)
			if got != tc.want || ok != tc.ok {
				t.Errorf("hexRune(%q, %d) = %U, %v; want %U, %v", tc.text, tc.at, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestEndsBetweenTokensIsTrueForWhatStandsBetweenTokens(t *testing.T) {
	cases := []struct {
		rest string
		want bool
	}{
		{"", true},
		{" ", true},
		{"\n\t\r ", true},
		{",", true},
		{":", true},
		{" , : ", true},
		{",,", true},
		{`"`, false},
		{`"a`, false},
		{"1", false},
		{"-", false},
		{"tru", false},
		{"{", false},
		{"]", false},
		{" , x", false},
	}
	for _, tc := range cases {
		t.Run(tc.rest, func(t *testing.T) {
			if got := endsBetweenTokens(tc.rest); got != tc.want {
				t.Errorf("endsBetweenTokens(%q) = %v, want %v", tc.rest, got, tc.want)
			}
		})
	}
}

func TestPointerTextNamesTheRootAsTheTopLevel(t *testing.T) {
	cases := []struct {
		pointer jsontext.Pointer
		want    string
	}{
		{"", "the top level"},
		{"/a", "/a"},
		{"/a/0", "/a/0"},
		{"/a/0/b", "/a/0/b"},
		{"/", "/"},
		{"/a~1b", "/a~1b"},
		{"/a~0b", "/a~0b"},
		{"/0", "/0"},
		{"/é", "/é"},
		{"/a b", "/a b"},
	}
	for _, tc := range cases {
		t.Run(string(tc.pointer), func(t *testing.T) {
			if got := pointerText(tc.pointer); got != tc.want {
				t.Errorf("pointerText(%q) = %q, want %q", tc.pointer, got, tc.want)
			}
		})
	}
}

// structureFault says a repeated key is a schema violation and anything else
// that is wrong with the text is malformed output, and it names the key and the
// object in the library's own words.
func TestStructureFaultClassifiesWhatTheTokenReaderReports(t *testing.T) {
	cases := []struct {
		name string
		body string
		kind types.ErrorKind
		want string
	}{
		{"a key written twice", `{"a":1,"a":2}`, types.KindSchemaViolation, `the response repeats the key "a" at the top level`},
		{"a key written twice in an object", `{"o":{"k":1,"k":2}}`, types.KindSchemaViolation, `the response repeats the key "k" at /o;`},
		{"a key written twice in a list", `[{"k":1,"k":2}]`, types.KindSchemaViolation, `the response repeats the key "k" at /0;`},
		{"a key with a slash", `{"o":{"a/b":1,"a/b":2}}`, types.KindSchemaViolation, `the response repeats the key "a/b" at /o;`},
		{"a key with a tilde", `{"o":{"a~b":1,"a~b":2}}`, types.KindSchemaViolation, `the response repeats the key "a~b" at /o;`},
		{"a key written twice with an escape", `{"a":1,"` + jsonEscape + `u0061":2}`, types.KindSchemaViolation, `the response repeats the key "a"`},
		{"a bad character", `{"a":x}`, types.KindMalformedOutput, "not well-formed JSON"},
		{"a missing colon", `{"a" 1}`, types.KindMalformedOutput, "not well-formed JSON"},
		{"a trailing comma", `[1,]`, types.KindMalformedOutput, "not well-formed JSON"},
		{"text after the value", `{"a":1} x`, types.KindMalformedOutput, "not well-formed JSON"},
		{"a leading zero", `[01]`, types.KindMalformedOutput, "not well-formed JSON"},
		{"a single quote", `{'a':1}`, types.KindMalformedOutput, "not well-formed JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStructure(tc.body, DefaultDecodeLimits())
			if err == nil {
				t.Fatal("accepted")
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestSkipJSONValueFindsWhereAValueEnds(t *testing.T) {
	cases := []struct {
		name string
		text string
		at   int
		end  int
		ok   bool
	}{
		{"a number", `12,3`, 0, 2, true},
		{"a number at the end", `12`, 0, 2, true},
		{"a literal", `true]`, 0, 4, true},
		{"a string", `"ab",1`, 0, 4, true},
		{"a string with an escaped quote", `"a\"b",1`, 0, 6, true},
		{"a string with a bracket in it", `"]}",1`, 0, 4, true},
		{"an empty object", `{},1`, 0, 2, true},
		{"an object", `{"a":[1,{"b":2}]},1`, 0, 17, true},
		{"a list", `[1,[2,3],"x"],4`, 0, 13, true},
		{"after an offset", `1,[2,3],4`, 2, 7, true},
		{"a list that is not closed", `[1,[2,3]`, 0, 8, false},
		{"a string that is not closed", `"abc`, 0, 0, false},
		{"a comma where a value should be", `,1`, 0, 0, false},
		{"a closing bracket where a value should be", `]`, 0, 1, false},
		{"nothing", ``, 0, 0, false},
		{"past the end", `1`, 1, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			end, ok := skipJSONValue(tc.text, tc.at)
			if ok != tc.ok || (ok && end != tc.end) {
				t.Errorf("skipJSONValue(%q, %d) = %d, %v; want %d, %v", tc.text, tc.at, end, ok, tc.end, tc.ok)
			}
			overBytes, bytesOK := skipJSONValue([]byte(tc.text), tc.at)
			if bytesOK != ok || overBytes != end {
				t.Errorf("skipJSONValue over bytes = %d, %v; over a string %d, %v", overBytes, bytesOK, end, ok)
			}
		})
	}
}

func TestSkipJSONSpaceAndEndOfScannedWord(t *testing.T) {
	spaces := []struct {
		text string
		from int
		want int
	}{
		{"", 0, 0}, {"a", 0, 0}, {" a", 0, 1}, {"\n\t\r a", 0, 4}, {"  ", 0, 2}, {"a b", 1, 2},
		{"a  ", 1, 3}, {"a,", 1, 1}, {" ,", 0, 1}, {"\v a", 0, 0}, {"é", 0, 0},
	}
	for _, tc := range spaces {
		if got := skipJSONSpace(tc.text, tc.from); got != tc.want {
			t.Errorf("skipJSONSpace(%q, %d) = %d, want %d", tc.text, tc.from, got, tc.want)
		}
		if got := skipJSONSpace([]byte(tc.text), tc.from); got != tc.want {
			t.Errorf("skipJSONSpace over bytes (%q, %d) = %d, want %d", tc.text, tc.from, got, tc.want)
		}
	}
	words := []struct {
		text  string
		start int
		want  int
	}{
		{"123,4", 0, 3}, {"123", 0, 3}, {"true}", 0, 4}, {"null ", 0, 4}, {"1.5e3]", 0, 5}, {"1:2", 0, 1},
		{"a,b", 2, 3}, {"", 0, 0}, {"-0.5\n", 0, 4}, {"x\ty", 0, 1}, {"12", 2, 2},
	}
	for _, tc := range words {
		if got := endOfScannedWord(tc.text, tc.start); got != tc.want {
			t.Errorf("endOfScannedWord(%q, %d) = %d, want %d", tc.text, tc.start, got, tc.want)
		}
		if got := endOfScannedWord([]byte(tc.text), tc.start); got != tc.want {
			t.Errorf("endOfScannedWord over bytes (%q, %d) = %d, want %d", tc.text, tc.start, got, tc.want)
		}
	}
}

func TestMembersOfListsWhereEachMemberBegins(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		at      int
		members int
		end     int
		ok      bool
	}{
		{"an empty object", `{}`, 0, 0, 2, true},
		{"an empty object with space", `{ }`, 0, 0, 3, true},
		{"one member", `{"a":1}`, 0, 1, 7, true},
		{"two members", `{"a":1,"b":[2,3]}`, 0, 2, 17, true},
		{"members with space", `{ "a" : 1 , "b" : "x" }`, 0, 2, 23, true},
		{"a nested object", `{"a":{"b":{"c":1}},"d":2}`, 0, 2, 25, true},
		{"an object after other text", `[{"a":1}]`, 1, 1, 8, true},
		{"as many as there is room for", wideObject(numbersAgreeMembers), 0, numbersAgreeMembers, len(wideObject(numbersAgreeMembers)), true},
		{"one more than there is room for", wideObject(numbersAgreeMembers + 1), 0, 0, 0, false},
		{"a key that is not a string", `{a:1}`, 0, 0, 0, false},
		{"no colon", `{"a" 1}`, 0, 0, 0, false},
		{"a value that is not closed", `{"a":[1}`, 0, 0, 0, false},
		{"an object that is not closed", `{"a":1`, 0, 0, 0, false},
		{"a string key that is not closed", `{"a`, 0, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var space [numbersAgreeMembers]memberSpan
			members, end, ok := membersOf([]byte(tc.text), tc.at, space[:0])
			if ok != tc.ok {
				t.Fatalf("membersOf ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if len(members) != tc.members || end != tc.end {
				t.Errorf("membersOf = %d members ending at %d; want %d ending at %d", len(members), end, tc.members, tc.end)
			}
			for _, member := range members {
				if tc.text[member.key] != '"' || member.value <= member.key {
					t.Errorf("a member begins at %+v, which is not a key and then a value", member)
				}
			}
		})
	}
}

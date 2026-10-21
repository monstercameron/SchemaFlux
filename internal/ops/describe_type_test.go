package ops

//lint:file-ignore U1000 unexported fields are here to prove the description skips them

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// What describeType prints is a part of the system prompt of Extract, Transform
// and Generate, so for a type that has no embedded struct, and for an ordinary
// embedding, it is held to the text it printed before it took its members from
// jsonMembers.

type promptFlat struct {
	Name    string  `json:"name"`
	Age     int     `json:"age"`
	Score   float64 `json:"score"`
	Active  bool    `json:"active"`
	Count   uint    `json:"count"`
	Plain   string
	Skipped string `json:"-"`
	hidden  string
}

type promptAddress struct {
	Street string  `json:"street"`
	Zip    *string `json:"zip,omitempty"`
}

type promptNested struct {
	ID       string          `json:"id"`
	Home     promptAddress   `json:"home"`
	Work     *promptAddress  `json:"work,omitempty"`
	Visited  []promptAddress `json:"visited"`
	Customer struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"customer"`
}

type promptCollections struct {
	Items  []promptAddress            `json:"items"`
	ByName map[string]promptAddress   `json:"by_name,omitempty"`
	Matrix [][]int                    `json:"matrix"`
	Fixed  [3]string                  `json:"fixed"`
	Raw    []byte                     `json:"raw"`
	ByID   map[int][]string           `json:"by_id"`
	Ptrs   []*promptAddress           `json:"ptrs"`
	Deep   map[string][]promptAddress `json:"deep"`
}

type promptPointers struct {
	Name  *string         `json:"name"`
	Count *int            `json:"count"`
	Home  *promptAddress  `json:"home"`
	Twice **promptAddress `json:"twice"`
}

type promptTimed struct {
	At    time.Time   `json:"at"`
	Until *time.Time  `json:"until,omitempty"`
	Spans []time.Time `json:"spans"`
}

type promptBase struct {
	ID string `json:"id"`
}

type PromptBaseExported struct {
	ID      string `json:"id"`
	Created string `json:"created,omitempty"`
}

type promptEmbedsUnexported struct {
	promptBase
	Name string `json:"name"`
}

type promptEmbedsExported struct {
	PromptBaseExported
	Name string `json:"name"`
}

type promptEmbedsPointer struct {
	Name string `json:"name"`
	*PromptBaseExported
	Tail string `json:"tail"`
}

type promptTagged struct {
	Required string  `json:"required"`
	Optional string  `json:"optional" schemaflux:"optional"`
	Tidy     string  `json:"tidy,omitempty" schemaflux:"required"`
	Omitted  string  `json:",omitempty"`
	Dash     string  `json:"-,"`
	Pointer  *string `json:"pointer" schemaflux:"required"`
}

type promptNode struct {
	Value    string        `json:"value"`
	Parent   *promptNode   `json:"parent,omitempty"`
	Children []*promptNode `json:"children,omitempty"`
}

type promptStatus string

type promptLevel int

type promptWithInterface struct {
	Payload any          `json:"payload"`
	Extra   any          `json:"extra,omitempty"`
	Failure error        `json:"failure,omitempty"`
	Status  promptStatus `json:"status"`
	Level   promptLevel  `json:"level"`
}

type promptDeepEnd struct {
	V string `json:"v"`
}

type promptDeep7 struct {
	N promptDeepEnd `json:"n"`
}

type promptDeep6 struct {
	N promptDeep7 `json:"n"`
}

type promptDeep5 struct {
	N promptDeep6 `json:"n"`
}

type promptDeep4 struct {
	N promptDeep5 `json:"n"`
}

type promptDeep3 struct {
	N promptDeep4 `json:"n"`
}

type promptDeep2 struct {
	N promptDeep3 `json:"n"`
}

type promptDeep1 struct {
	N promptDeep2 `json:"n"`
}

type promptNoMembers struct {
	hidden string
	Gone   string `json:"-"`
}

func describeTypeGoldens() []struct {
	name   string
	target reflect.Type
	want   string
} {
	return []struct {
		name   string
		target reflect.Type
		want   string
	}{
		{"a flat struct", reflect.TypeFor[promptFlat](), "{\n  name: string (required)\n  age: integer (required)\n  score: number (required)\n  active: boolean (required)\n  count: unsigned integer (required)\n  Plain: string (required)\n}"},
		{"nested structs", reflect.TypeFor[promptNested](), "{\n  id: string (required)\n  home: {\n    street: string (required)\n    zip: string\n  } (required)\n  work: {\n    street: string (required)\n    zip: string\n  }\n  visited: [{\n    street: string (required)\n    zip: string\n  }] (required)\n  customer: {\n    name: string (required)\n    email: string (required)\n  } (required)\n}"},
		{"slices and maps of structs", reflect.TypeFor[promptCollections](), "{\n  items: [{\n    street: string (required)\n    zip: string\n  }] (required)\n  by_name: map[string]{\n    street: string (required)\n    zip: string\n  }\n  matrix: [[integer]] (required)\n  fixed: [string] (required)\n  raw: [unsigned integer] (required)\n  by_id: map[integer][string] (required)\n  ptrs: [{\n    street: string (required)\n    zip: string\n  }] (required)\n  deep: map[string][{\n    street: string (required)\n    zip: string\n  }] (required)\n}"},
		{"pointers", reflect.TypeFor[promptPointers](), "{\n  name: string\n  count: integer\n  home: {\n    street: string (required)\n    zip: string\n  }\n  twice: {\n    street: string (required)\n    zip: string\n  }\n}"},
		{"time.Time", reflect.TypeFor[promptTimed](), "{\n  at: datetime (RFC3339) (required)\n  until: datetime (RFC3339)\n  spans: [datetime (RFC3339)] (required)\n}"},
		{"an unexported embedded struct", reflect.TypeFor[promptEmbedsUnexported](), "{\n  id: string (required)\n  name: string (required)\n}"},
		{"an exported embedded struct", reflect.TypeFor[promptEmbedsExported](), "{\n  id: string (required)\n  created: string\n  name: string (required)\n}"},
		{"an embedded pointer to a struct", reflect.TypeFor[promptEmbedsPointer](), "{\n  name: string (required)\n  id: string (required)\n  created: string\n  tail: string (required)\n}"},
		{"json and schemaflux tags", reflect.TypeFor[promptTagged](), "{\n  required: string (required)\n  optional: string\n  tidy: string (required)\n  Omitted: string\n  -: string (required)\n  pointer: string (required)\n}"},
		{"a recursive type by a named field", reflect.TypeFor[promptNode](), "{\n  value: string (required)\n  parent: ops.promptNode (recursive)\n  children: [ops.promptNode (recursive)]\n}"},
		{"an interface field and enum-like types", reflect.TypeFor[promptWithInterface](), "{\n  payload: interface {} (required)\n  extra: interface {}\n  failure: error\n  status: string (required)\n  level: integer (required)\n}"},
		{"a type deeper than the bound", reflect.TypeFor[promptDeep1](), "{\n  n: {\n    n: {\n      n: {\n        n: {\n          n: {\n            n: ops.promptDeep7 (nested, not expanded) (required)\n          } (required)\n        } (required)\n      } (required)\n    } (required)\n  } (required)\n}"},
		{"a struct with no member", reflect.TypeFor[promptNoMembers](), "{}"},
		{"a pointer to a struct", reflect.TypeFor[*promptFlat](), "{\n  name: string (required)\n  age: integer (required)\n  score: number (required)\n  active: boolean (required)\n  count: unsigned integer (required)\n  Plain: string (required)\n}"},
		{"a slice of structs", reflect.TypeFor[[]promptAddress](), "[{\n  street: string (required)\n  zip: string\n}]"},
		{"a map of structs", reflect.TypeFor[map[string]promptNested](), "map[string]{\n  id: string (required)\n  home: {\n    street: string (required)\n    zip: string\n  } (required)\n  work: {\n    street: string (required)\n    zip: string\n  }\n  visited: [{\n    street: string (required)\n    zip: string\n  }] (required)\n  customer: {\n    name: string (required)\n    email: string (required)\n  } (required)\n}"},
		{"a time", reflect.TypeFor[time.Time](), "datetime (RFC3339)"},
		{"a string", reflect.TypeFor[string](), "string"},
		{"an embedded struct that refers back to its embedder", reflect.TypeFor[embeddedWithAParent](), "{\n  parent: ops.embeddedWithAParent (recursive)\n  age: integer (required)\n  name: string (required)\n}"},
		{"a chain of twelve embedded structs", reflect.TypeFor[embeddedChain1](), "{\n  field12: string (required)\n  field11: string (required)\n  field10: string (required)\n  field9: string (required)\n  field8: string (required)\n  field7: string (required)\n  field6: string (required)\n  field5: string (required)\n  field4: string (required)\n  field3: string (required)\n  field2: string (required)\n  field1: string (required)\n}"},
		{"an ordinary embedding of a struct with a tag", reflect.TypeFor[diamondLeft](), "{\n  id: string (required)\n  left: string (required)\n}"},
		{"an unexported struct that holds an exported pointer", reflect.TypeFor[unexportedHoldingExportedPointer](), "{\n  plain: string (required)\n  r: string (required)\n  s: string (required)\n}"},
		{"an embedded struct under a tag with a name", reflect.TypeFor[namedTagEmbedding](), "{\n  p: {\n    plain: string (required)\n  } (required)\n  z: integer (required)\n}"},
		{"an embedded type that is not a struct", reflect.TypeFor[labelEmbedding](), "{\n  Label: string (required)\n  k: integer (required)\n}"},
		{"an unexported embedded type that is not a struct", reflect.TypeFor[hiddenLabelEmbedding](), "{\n  k: integer (required)\n}"},
		{"an embedded struct under a tag of a dash", reflect.TypeFor[omittedEmbedding](), "{\n  z: integer (required)\n}"},
		{"a slice of slices of times", reflect.TypeFor[[][]time.Time](), "[[datetime (RFC3339)]]"},
	}
}

func TestDescribeTypePrintsWhatItPrintedBeforeItTookItsMembersFromJSONMembers(t *testing.T) {
	for _, tc := range describeTypeGoldens() {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeType(tc.target, map[reflect.Type]bool{}, 0); got != tc.want {
				t.Errorf("describeType =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// proseMember is one member line at the top level of a described struct.
type proseMember struct {
	name string
	// kind is the type the line gives, without the mark of a required field.
	kind string
}

// proseMembers reads the members off the description of a struct: the lines that
// stand one level in, which are the struct's own.
func proseMembers(prose string) []proseMember {
	members := []proseMember{}
	for _, line := range strings.Split(prose, "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		name, kind, found := strings.Cut(strings.TrimPrefix(line, "  "), ": ")
		if !found {
			continue
		}
		members = append(members, proseMember{name: name, kind: strings.TrimSuffix(kind, " (required)")})
	}
	return members
}

func proseMemberNames(members []proseMember) []string {
	names := []string{}
	for _, member := range members {
		names = append(names, member.name)
	}
	return names
}

// jsonKindOfProse is the JSON type the schema gives what the description calls
// by a word.
func jsonKindOfProse(kind string) string {
	if strings.HasPrefix(kind, "{") {
		return "object"
	}
	return kind
}

// schemaKind is the type a property is declared with, leaving out null.
func schemaKind(t *testing.T, property any) string {
	t.Helper()
	declared, _ := property.(map[string]any)
	switch kind := declared["type"].(type) {
	case string:
		return kind
	case []string:
		for _, entry := range kind {
			if entry != "null" {
				return entry
			}
		}
	}
	t.Fatalf("the property %v has no type", property)
	return ""
}

func TestDescribeTypeNamesExactlyTheMembersEncodingJSONReads(t *testing.T) {
	for _, fact := range embeddingFacts() {
		t.Run(fact.name, func(t *testing.T) {
			// The fact itself, from encoding/json.
			raw, err := json.Marshal(reflect.New(fact.target).Interface())
			if err != nil {
				t.Fatalf("encoding/json cannot write the type: %v", err)
			}
			if written := jsonKeysInOrder(t, raw); !slices.Equal(written, fact.members) {
				t.Fatalf("encoding/json writes %v, the facts say %v", written, fact.members)
			}

			prose := describeType(fact.target, map[reflect.Type]bool{}, 0)
			members := proseMembers(prose)
			if got := proseMemberNames(members); !slices.Equal(got, fact.members) {
				t.Errorf("the description names %v, encoding/json reads %v:\n%s", got, fact.members, prose)
			}
			for name, kind := range fact.kinds {
				for _, member := range members {
					if member.name == name && jsonKindOfProse(member.kind) != kind {
						t.Errorf("%s is described as %s, encoding/json reads a %s there", name, member.kind, kind)
					}
				}
			}

			schema := GenerateJSONSchema(fact.target)
			if schema == nil {
				return
			}
			required, _ := schema["required"].([]string)
			if got := proseMemberNames(members); !slices.Equal(got, required) {
				t.Errorf("the description names %v and the schema declares %v", got, required)
			}
			properties, _ := schema["properties"].(map[string]any)
			for _, member := range members {
				if want := schemaKind(t, properties[member.name]); jsonKindOfProse(member.kind) != want {
					t.Errorf("%s is described as %s and declared as %s", member.name, member.kind, want)
				}
			}
		})
	}
}

func TestDescribeTypeOfAStructWithEmbeddedStructs(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		want   string
	}{
		{"one struct embedded by two routes names neither of its copies",
			reflect.TypeFor[diamondTop](),
			"{\n  left: string (required)\n  right: string (required)\n}"},
		{"two structs declaring a name give none of them",
			reflect.TypeFor[twinNamesTop](),
			"{\n  a: integer (required)\n  b: integer (required)\n}"},
		{"an outer field is the one a hidden name is described by",
			reflect.TypeFor[outerNameHidesTwin](),
			"{\n  a: integer (required)\n  name: boolean (required)\n}"},
		{"a name declared again by the outer struct is described once",
			reflect.TypeFor[diamondDeclaredByTop](),
			"{\n  left: string (required)\n  right: string (required)\n  id: integer (required)\n}"},
		{"one struct reached at two depths is described by the shallower",
			reflect.TypeFor[diamondAtTwoDepths](),
			"{\n  id: string (required)\n  left: string (required)\n}"},
		{"a tagged name beats an untagged one",
			reflect.TypeFor[taggedBeatsUntaggedTop](),
			"{\n  Name: integer (required)\n}"},
		{"a name tagged twice is described by neither",
			reflect.TypeFor[twoTaggedNamesTop](),
			"{\n  other: string (required)\n}"},
		{"a name two levels down is hidden by one a level down",
			reflect.TypeFor[shallowBeatsDeepTop](),
			"{\n  name: string (required)\n  a: integer (required)\n  q: integer (required)\n}"},
		{"a name and nothing else, ambiguous",
			reflect.TypeFor[soleNamesTop](),
			"{}"},
		{"a struct embedded under a tag of options only is promoted, not nested",
			reflect.TypeFor[optionsOnlyTagEmbedding](),
			"{\n  plain: string (required)\n  z: integer (required)\n}"},
		{"a struct embedded under a tag that has a name that encoding/json does not accept is promoted",
			reflect.TypeFor[invalidTagEmbedding](),
			"{\n  plain: string (required)\n  z: integer (required)\n}"},
		{"the exported fields of an unexported struct",
			reflect.TypeFor[unexportedEmbeddedByValue](),
			"{\n  exp: string (required)\n  own: string (required)\n}"},
		{"the fields behind an unexported pointer are still the type's",
			reflect.TypeFor[unexportedEmbeddedByPointer](),
			"{\n  exp: string (required)\n  own: string (required)\n}"},
		{"a field below an unexported pointer",
			reflect.TypeFor[pointerAboveAChain](),
			"{\n  deep: string (required)\n  n: string (required)\n}"},
		{"a struct embedded and nothing else, unexported",
			reflect.TypeFor[unexportedEmbeddedOnly](),
			"{\n  exp: string (required)\n}"},
		{"a tag name that encoding/json does not accept is not the name",
			reflect.TypeFor[invalidTagName](),
			"{\n  Text: string (required)\n  n: integer (required)\n}"},
		{"a tag name that runs into a backslash is the name up to it",
			reflect.TypeFor[truncatedTagName](),
			"{\n  a: string (required)\n  n: integer (required)\n}"},
		{"a struct under a tag name that runs into a backslash is a field of its own",
			reflect.TypeFor[truncatedTagEmbedding](),
			"{\n  a: {\n    plain: string (required)\n  } (required)\n  z: integer (required)\n}"},
		{"the option embed promotes the members of a struct that is not embedded",
			reflect.TypeFor[embedOptionOnNamedStruct](),
			"{\n  plain: string (required)\n  z: integer (required)\n}"},
		{"a map that embeds is not described as a member",
			reflect.TypeFor[embedOptionOnMap](),
			"{\n  z: integer (required)\n}"},
		{"a struct nested in a slice in a struct that has a diamond",
			reflect.TypeFor[struct {
				Items []diamondTop `json:"items"`
			}](),
			"{\n  items: [{\n    left: string (required)\n    right: string (required)\n  }] (required)\n}"},
		{"a slice of structs that hide a name",
			reflect.TypeFor[[]outerNameHidesTwin](),
			"[{\n  a: integer (required)\n  name: boolean (required)\n}]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeType(tc.target, map[reflect.Type]bool{}, 0); got != tc.want {
				t.Errorf("describeType =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestDescribeTypeTerminatesOnATypeThatEmbedsItself(t *testing.T) {
	for name, target := range embeddingCycles() {
		t.Run(name, func(t *testing.T) {
			done := make(chan string, 1)
			go func() { done <- describeType(target, map[reflect.Type]bool{}, 0) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("describeType did not finish")
			}
		})
	}
}

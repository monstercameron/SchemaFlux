package ops

// The unexported fields here are the subject: a struct with none of them
// exported is opaque to the schema, and a struct with some is not. They exist
// to be unused.
//lint:file-ignore U1000 unexported fields are the case under test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

type fullyTyped struct {
	Name    string    `json:"name"`
	Count   int       `json:"count"`
	Price   float64   `json:"price"`
	Active  bool      `json:"active"`
	When    time.Time `json:"when"`
	Tags    []string  `json:"tags"`
	Skipped string    `json:"-"`
	hidden  string
}

type withAny struct {
	Name  string `json:"name"`
	Extra any    `json:"extra"`
}

type withChannel struct {
	Name string   `json:"name"`
	Ch   chan int `json:"ch"`
}

type withMap struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type withIntMap struct {
	Counts map[int]string `json:"counts"`
}

type node struct {
	Name   string `json:"name"`
	Parent *node  `json:"parent,omitempty"`
}

type customWire struct {
	Value string
}

func (c customWire) MarshalJSON() ([]byte, error) { return json.Marshal(c.Value) }

type withCustomWire struct {
	Field customWire `json:"field"`
}

type noExportedFields struct {
	hidden string
}

type withFloat32Price struct {
	Name  string  `json:"name"`
	Price float32 `json:"price"`
}

type withFloat32Temperature struct {
	Celsius float32 `json:"celsius"`
}

type withFloat32Slice struct {
	Amounts []float32 `json:"amounts"`
}

type withNestedFloat32 struct {
	Line struct {
		Total float32 `json:"total"`
	} `json:"line"`
}

type withFloat32Pointer struct {
	Price *float32 `json:"price"`
}

type withFloat64Price struct {
	Price float64 `json:"price"`
}

// S-010. A Go type is the whole contract: it becomes the schema the model is
// shown and the shape the answer is decoded into. The question is when a caller
// finds out that theirs cannot play that role.
func TestClassifyType(t *testing.T) {
	cases := []struct {
		name   string
		target any
		want   TypeSupport
		reason string
	}{
		{"a plain struct", fullyTyped{}, SupportFull, ""},
		{"a slice of them", []fullyTyped{}, SupportFull, ""},
		{"a pointer to one", &fullyTyped{}, SupportFull, ""},
		{"a scalar", "", SupportFull, ""},

		{"a string-keyed map", withMap{}, SupportRestricted, "no field names"},
		{"a self-referential type", node{}, SupportRestricted, "refers to itself"},
		{"a custom JSON representation", withCustomWire{}, SupportRestricted, "custom JSON representation"},

		{"no exported fields", noExportedFields{}, SupportOpaque, "nothing for the schema to describe"},

		{"an any field", withAny{}, SupportRejected, "gives the model nothing"},
		{"a channel field", withChannel{}, SupportRejected, "cannot be represented"},
		{"a map keyed by int", withIntMap{}, SupportRejected, "object keys are strings"},
		{"a bare any", any(nil), SupportRejected, "not concrete"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ClassifyType(reflect.TypeOf(tc.target))
			if report.Support != tc.want {
				t.Fatalf("support = %v (%s), want %v", report.Support, report.Reason, tc.want)
			}
			if tc.reason != "" && !strings.Contains(report.Reason, tc.reason) {
				t.Errorf("reason %q does not mention %q", report.Reason, tc.reason)
			}
		})
	}
}

// The worst finding wins, not the first: a struct with one rejected field is
// rejected whatever else it holds, and telling a caller about a restricted map
// while a channel sits two fields down wastes the trip.
func TestTheWorstFindingWins(t *testing.T) {
	type mixed struct {
		Labels map[string]string `json:"labels"` // restricted
		Extra  any               `json:"extra"`  // rejected
	}

	report := ClassifyType(reflect.TypeOf(mixed{}))
	if report.Support != SupportRejected {
		t.Errorf("support = %v, want rejected -- the restricted field won", report.Support)
	}
	if !strings.Contains(report.Path, "extra") {
		t.Errorf("path = %q, want it to name the rejected field", report.Path)
	}
}

// A rejected shape fails preflight, and the message says no call was made --
// because the difference between "your type is wrong" and "your type is wrong
// and you were billed" is the whole point.
func TestPreflightRefusesRejectedTypes(t *testing.T) {
	err := PreflightType(reflect.TypeOf(withAny{}))
	if err == nil {
		t.Fatal("PreflightType accepted a type with an any field")
	}
	if !strings.Contains(err.Error(), "No provider call was made") {
		t.Errorf("the error does not say the call was skipped: %v", err)
	}
	if !strings.Contains(err.Error(), "extra") {
		t.Errorf("the error does not name the field: %v", err)
	}

	// Restricted and opaque are usable, so preflight passes them.
	for _, target := range []any{withMap{}, node{}, noExportedFields{}, fullyTyped{}} {
		if err := PreflightType(reflect.TypeOf(target)); err != nil {
			t.Errorf("PreflightType(%T) = %v, want nil", target, err)
		}
	}
}

// Nested rejection is found, because a type is only as usable as its worst
// field however deep it sits.
func TestRejectionIsFoundThroughNesting(t *testing.T) {
	type inner struct {
		Ch chan int `json:"ch"`
	}
	type middle struct {
		Inner inner `json:"inner"`
	}
	type outer struct {
		Items []middle `json:"items"`
	}

	if err := PreflightType(reflect.TypeOf(outer{})); err == nil {
		t.Error("a channel three levels down was accepted")
	}
}

// A field excluded from JSON is excluded from the classification too: it is not
// part of the contract, so its type cannot break it.
func TestExcludedFieldsDoNotCount(t *testing.T) {
	type record struct {
		Name string   `json:"name"`
		Ch   chan int `json:"-"`
	}

	if err := PreflightType(reflect.TypeOf(record{})); err != nil {
		t.Errorf("a json:\"-\" channel broke preflight: %v", err)
	}
}

// S-012. A float32 loses precision a round trip cannot detect (S-009), and
// the matrix cannot tell a price from a temperature -- so every float32 is
// flagged restricted, wherever it sits in the shape.
func TestClassifyTypeFlagsFloat32(t *testing.T) {
	cases := []struct {
		name   string
		target any
		want   TypeSupport
		reason string
	}{
		{"a top-level float32 price", withFloat32Price{}, SupportRestricted, "precision"},
		{"a float32 with no money-sounding name", withFloat32Temperature{}, SupportRestricted, "precision"},
		{"a slice of float32", withFloat32Slice{}, SupportRestricted, "precision"},
		{"a float32 nested inside another struct", withNestedFloat32{}, SupportRestricted, "precision"},
		{"a pointer to a float32", withFloat32Pointer{}, SupportRestricted, "precision"},
		{"a bare float32 scalar", float32(0), SupportRestricted, "precision"},
		{"an array of float32", [3]float32{}, SupportRestricted, "precision"},

		// float64 is unaffected -- the blind spot is specific to float32.
		{"a float64 price is full support", withFloat64Price{}, SupportFull, ""},
		{"a bare float64 scalar", float64(0), SupportFull, ""},
		{"a bare float32 pointer alone", func() *float32 { v := float32(1); return &v }(), SupportRestricted, "precision"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ClassifyType(reflect.TypeOf(tc.target))
			if report.Support != tc.want {
				t.Fatalf("support = %v (%s), want %v", report.Support, report.Reason, tc.want)
			}
			if tc.reason != "" && !strings.Contains(report.Reason, tc.reason) {
				t.Errorf("reason %q does not mention %q", report.Reason, tc.reason)
			}
		})
	}
}

// Restricted is usable -- a float32 field does not stop the call, it flags
// it. Preflight lets it through, same as a string-keyed map.
func TestPreflightAcceptsFloat32AsRestricted(t *testing.T) {
	for _, target := range []any{withFloat32Price{}, withFloat32Temperature{}, withFloat32Slice{}} {
		if err := PreflightType(reflect.TypeOf(target)); err != nil {
			t.Errorf("PreflightType(%T) = %v, want nil -- restricted is not rejected", target, err)
		}
	}
}

// A float32 still loses to an actually-rejected field: the worst finding
// wins, and float32's restricted does not mask a channel two fields down.
func TestFloat32DoesNotMaskAWorseFinding(t *testing.T) {
	type worse struct {
		Price float32  `json:"price"`
		Ch    chan int `json:"ch"`
	}
	report := ClassifyType(reflect.TypeOf(worse{}))
	if report.Support != SupportRejected {
		t.Errorf("support = %v, want rejected -- the channel should still win over float32", report.Support)
	}
}

type withOptionalTimestamp struct {
	Created *time.Time `json:"created"`
	Name    string     `json:"name"`
}

type withTimestamp struct {
	Created time.Time `json:"created"`
}

type withTimestampSlice struct {
	Seen []*time.Time `json:"seen"`
}

type depthLeaf struct {
	Value string `json:"value"`
}

type depthOne struct {
	Next depthLeaf `json:"next"`
}

type depthTwo struct {
	Next depthOne `json:"next"`
}

type depthThree struct {
	Next depthTwo `json:"next"`
}

type depthFour struct {
	Next depthThree `json:"next"`
}

type depthFive struct {
	Next depthFour `json:"next"`
}

type depthSix struct {
	Next depthFive `json:"next"`
}

type depthSeven struct {
	Next depthSix `json:"next"`
}

type depthEight struct {
	Next depthSeven `json:"next"`
}

// A pointer to a time.Time is the same thing on the wire as a time.Time, so an
// optional timestamp is classified as the timestamp is. It was reported as
// having a custom JSON representation because *time.Time implements
// json.Marshaler and the exemption compared the exact type.
func TestClassifyTypeTreatsAPointerToATimeAsATime(t *testing.T) {
	timePointer := new(time.Time)
	cases := []struct {
		name   string
		target reflect.Type
		want   TypeSupport
	}{
		{"a time", reflect.TypeOf(time.Time{}), SupportFull},
		{"a pointer to a time", reflect.TypeOf(timePointer), SupportFull},
		{"a pointer to a pointer to a time", reflect.TypeOf(&timePointer), SupportFull},
		{"a struct with a time", reflect.TypeOf(withTimestamp{}), SupportFull},
		{"a struct with an optional time", reflect.TypeOf(withOptionalTimestamp{}), SupportFull},
		{"a pointer to a struct with an optional time", reflect.TypeOf(&withOptionalTimestamp{}), SupportFull},
		{"a slice of optional times", reflect.TypeOf(withTimestampSlice{}), SupportFull},
		{"a custom marshaler is still restricted", reflect.TypeOf(customWire{}), SupportRestricted},
		{"a pointer to a custom marshaler is restricted", reflect.TypeOf(&customWire{}), SupportRestricted},
		{"a struct holding a custom marshaler is restricted", reflect.TypeOf(withCustomWire{}), SupportRestricted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ClassifyType(tc.target)
			if report.Support != tc.want {
				t.Errorf("support = %v (%s), want %v", report.Support, report.Reason, tc.want)
			}
		})
	}
}

// The depth bound is where the schema stops describing a type, and a type
// nested past it is restricted rather than refused or silently complete.
func TestClassifyTypeBoundsTheDepth(t *testing.T) {
	cases := []struct {
		name   string
		target any
		want   TypeSupport
	}{
		{"one level", depthOne{}, SupportFull},
		{"three levels", depthThree{}, SupportFull},
		{"five levels", depthFive{}, SupportFull},
		{"eight levels", depthEight{}, SupportRestricted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ClassifyType(reflect.TypeOf(tc.target))
			if report.Support != tc.want {
				t.Fatalf("support = %v (%s), want %v", report.Support, report.Reason, tc.want)
			}
			if tc.want == SupportRestricted && !strings.Contains(report.Reason, "nested deeper") {
				t.Errorf("reason %q does not say the nesting was the cause", report.Reason)
			}
		})
	}
}

// A type that embeds itself, directly or through the types it embeds, has
// promoted fields that cannot be listed, and no schema can say what it holds.
// That is refused before a call, as a type with no shape to describe is, and
// not left to the generators, one of which used to follow the embedding until
// the stack overflowed.
func TestClassifyTypeRejectsATypeThatEmbedsItself(t *testing.T) {
	for name, target := range embeddingCycles() {
		t.Run(name, func(t *testing.T) {
			report := ClassifyType(target)
			if report.Support != SupportRejected {
				t.Fatalf("support = %v (%s), want %v", report.Support, report.Reason, SupportRejected)
			}
			if !strings.Contains(report.Reason, "embed") || !strings.Contains(report.Reason, "promotes") {
				t.Errorf("reason %q does not say the embedding is the cause", report.Reason)
			}

			err := PreflightType(target)
			var refusal *types.OperationError
			if !errors.As(err, &refusal) || refusal.Kind != types.KindConfiguration {
				t.Errorf("PreflightType gave %v, want a configuration refusal", err)
			}
		})
	}
}

func TestClassifyTypeDoesNotRejectTypesThatOnlyLookLikeEmbeddingCycles(t *testing.T) {
	cases := map[string]reflect.Type{
		"a chain of twelve embedded structs":        reflect.TypeFor[embeddedChain1](),
		"a pointer to the chain":                    reflect.TypeFor[*embeddedChain1](),
		"the same type embedded through two routes": reflect.TypeFor[diamondTop](),
		"an embedded type that refers back by name": reflect.TypeFor[embeddedWithAParent](),
		"a recursive type with no embedding":        reflect.TypeFor[selfEmbeddingByValueInASlice](),
		"a type embedding a plain struct":           reflect.TypeFor[diamondLeft](),
		"a type embedding a time":                   reflect.TypeFor[struct{ time.Time }](),
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			if report := ClassifyType(target); report.Support == SupportRejected {
				t.Errorf("rejected: %s", report.Reason)
			}
			if err := PreflightType(target); err != nil {
				t.Errorf("PreflightType refused it: %v", err)
			}
		})
	}
}

// The matrix classifies the members of a type that encoding/json reads, which are
// the ones the schema declares. It used to skip an unexported embedded struct,
// whose exported fields encoding/json promotes, so a type holding a channel in
// one was reported as fully supported while the generator, which did follow it,
// wrote no schema.

type channelBase struct {
	Ch chan int `json:"ch"`
}

type unexportedEmbeddingAChannel struct {
	channelBase
	Name string `json:"name"`
}

type ExportedChannelBase struct {
	Ch chan int `json:"ch"`
}

type exportedEmbeddingAChannel struct {
	ExportedChannelBase
}

type mapBase struct {
	Labels map[string]string `json:"labels"`
}

type unexportedEmbeddingAMap struct {
	mapBase
	Name string `json:"name"`
}

type floatBase struct {
	Price float32 `json:"price"`
}

type unexportedEmbeddingAFloat struct {
	floatBase
	Name string `json:"name"`
}

type anyBase struct {
	Extra any `json:"extra"`
}

type unexportedEmbeddingAnAny struct {
	anyBase
	Name string `json:"name"`
}

// hiddenChannelBase has a channel under the name that outerHidesChannel declares
// itself, so encoding/json never reads the channel.
type hiddenChannelBase struct {
	Ch chan int `json:"name"`
}

type outerHidesChannel struct {
	hiddenChannelBase
	Name string `json:"name"`
}

func TestClassifyTypeReadsWhatEncodingJSONPromotesFromAnEmbeddedStruct(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		want   TypeSupport
		reason string
		path   string
	}{
		{"an unexported struct embedded by value", reflect.TypeFor[unexportedEmbeddedByValue](), SupportFull, "", ""},
		{"an unexported struct embedded and nothing else", reflect.TypeFor[unexportedEmbeddedOnly](), SupportFull, "", ""},
		{"a channel in an unexported embedded struct", reflect.TypeFor[unexportedEmbeddingAChannel](), SupportRejected, "cannot be represented", "channelBase.ch"},
		{"a channel in an exported embedded struct", reflect.TypeFor[exportedEmbeddingAChannel](), SupportRejected, "cannot be represented", "ExportedChannelBase.ch"},
		{"a map in an unexported embedded struct", reflect.TypeFor[unexportedEmbeddingAMap](), SupportRestricted, "no field names", "mapBase.labels"},
		{"a float32 in an unexported embedded struct", reflect.TypeFor[unexportedEmbeddingAFloat](), SupportRestricted, "precision", "floatBase.price"},
		{"an any in an unexported embedded struct", reflect.TypeFor[unexportedEmbeddingAnAny](), SupportRejected, "gives the model nothing", "anyBase.extra"},
		{"a channel the outer struct hides", reflect.TypeFor[outerHidesChannel](), SupportFull, "", ""},
		{"a name two routes make ambiguous", reflect.TypeFor[diamondTop](), SupportFull, "", ""},
		{"a name the outer struct redeclares", reflect.TypeFor[diamondDeclaredByTop](), SupportFull, "", ""},
		{"nothing but an ambiguous name", reflect.TypeFor[soleNamesTop](), SupportOpaque, "nothing for the schema to describe", ""},
		{"a pointer to such a type", reflect.TypeFor[*unexportedEmbeddingAChannel](), SupportRejected, "cannot be represented", "channelBase.ch"},
		{"a slice of such types", reflect.TypeFor[[]unexportedEmbeddingAChannel](), SupportRejected, "cannot be represented", "[].channelBase.ch"},
		{"an embedded pointer to an exported struct", reflect.TypeFor[pointerInsideUnexported](), SupportFull, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ClassifyType(tc.target)
			if report.Support != tc.want {
				t.Fatalf("support = %v (%s), want %v", report.Support, report.Reason, tc.want)
			}
			if tc.reason != "" && !strings.Contains(report.Reason, tc.reason) {
				t.Errorf("reason %q does not mention %q", report.Reason, tc.reason)
			}
			if tc.path != "" && report.Path != tc.path {
				t.Errorf("path = %q, want %q", report.Path, tc.path)
			}
		})
	}
}

// Where the matrix says a type is not rejected, a schema is written unless the
// type is one the schema cannot express, and a rejected type has none. The two
// were not consulted together, and disagreed.
func TestClassifyTypeAgreesWithTheSchemaGeneratorOnEmbeddedStructs(t *testing.T) {
	cases := map[string]reflect.Type{
		"an unexported struct by value":          reflect.TypeFor[unexportedEmbeddedByValue](),
		"an unexported struct and nothing else":  reflect.TypeFor[unexportedEmbeddedOnly](),
		"an unexported struct by pointer":        reflect.TypeFor[unexportedEmbeddedByPointer](),
		"an unexported pointer in an exported":   reflect.TypeFor[exportedEmbedHoldingPointer](),
		"a pointer that promotes nothing":        reflect.TypeFor[unexportedPointerPromotingNothing](),
		"an exported pointer in an unexported":   reflect.TypeFor[unexportedHoldingExportedPointer](),
		"a channel in an unexported struct":      reflect.TypeFor[unexportedEmbeddingAChannel](),
		"a channel the outer struct hides":       reflect.TypeFor[outerHidesChannel](),
		"a diamond":                              reflect.TypeFor[diamondTop](),
		"a diamond the outer struct redeclares":  reflect.TypeFor[diamondDeclaredByTop](),
		"two structs that declare the same name": reflect.TypeFor[twinNamesTop](),
		"nothing but an ambiguous name":          reflect.TypeFor[soleNamesTop](),
		"a struct embedded under an options tag": reflect.TypeFor[optionsOnlyTagEmbedding](),
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			report := ClassifyType(target)
			schema := GenerateJSONSchema(target)
			switch report.Support {
			case SupportFull:
				if schema == nil {
					t.Errorf("the matrix says full and no schema was written")
				}
			case SupportRejected, SupportOpaque:
				if schema != nil {
					t.Errorf("the matrix says %v (%s) and a schema was written: %v", report.Support, report.Reason, schema)
				}
			}
		})
	}
}

// A struct that embeds a pointer to a struct type that is not exported, and
// promotes fields from it, cannot be decoded into by encoding/json, which fails
// when it has to allocate the pointer. That is refused before a call, as a type
// that embeds itself is, and the reason says what it is.
func TestClassifyTypeRejectsAnUnexportedEmbeddedPointerThatPromotesFields(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		path   string
	}{
		{"by itself", reflect.TypeFor[unexportedEmbeddedByPointer](), "unexportedBase"},
		{"a pointer to it", reflect.TypeFor[*unexportedEmbeddedByPointer](), "unexportedBase"},
		{"a slice of it", reflect.TypeFor[[]unexportedEmbeddedByPointer](), "[].unexportedBase"},
		{"an array of it", reflect.TypeFor[[2]unexportedEmbeddedByPointer](), "[].unexportedBase"},
		{"a field of it", reflect.TypeFor[struct {
			Inner unexportedEmbeddedByPointer `json:"inner"`
		}](), "inner.unexportedBase"},
		{"through an exported embedding", reflect.TypeFor[exportedEmbedHoldingPointer](), "PointerHolder.unexportedBase"},
		{"as the exported embedding itself", reflect.TypeFor[PointerHolder](), "unexportedBase"},
		{"above a chain of embedded structs", reflect.TypeFor[pointerAboveAChain](), "chainMiddle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ClassifyType(tc.target)
			if report.Support != SupportRejected {
				t.Fatalf("support = %v (%s), want %v", report.Support, report.Reason, SupportRejected)
			}
			pointed := tc.path[strings.LastIndex(tc.path, ".")+1:]
			for _, part := range []string{pointed, "not exported", "cannot allocate"} {
				if !strings.Contains(report.Reason, part) {
					t.Errorf("reason %q does not mention %q", report.Reason, part)
				}
			}
			if report.Path != tc.path {
				t.Errorf("path = %q, want %q", report.Path, tc.path)
			}

			err := PreflightType(tc.target)
			var refusal *types.OperationError
			if !errors.As(err, &refusal) || refusal.Kind != types.KindConfiguration {
				t.Fatalf("PreflightType gave %v, want a configuration refusal", err)
			}
			if !strings.Contains(err.Error(), "No provider call was made") {
				t.Errorf("the refusal does not say the call was skipped: %v", err)
			}
		})
	}
}

func TestClassifyTypeAcceptsAnUnexportedEmbeddedPointerThatNothingNeedsToAllocate(t *testing.T) {
	for name, target := range map[string]reflect.Type{
		"a pointer to a struct with no field encoding/json reads": reflect.TypeFor[unexportedPointerPromotingNothing](),
		"an exported pointer inside an unexported struct":         reflect.TypeFor[unexportedHoldingExportedPointer](),
		"the same struct embedded by value":                       reflect.TypeFor[unexportedEmbeddedByValue](),
	} {
		t.Run(name, func(t *testing.T) {
			if report := ClassifyType(target); report.Support != SupportFull {
				t.Errorf("support = %v (%s), want %v", report.Support, report.Reason, SupportFull)
			}
			if err := PreflightType(target); err != nil {
				t.Errorf("PreflightType refused it: %v", err)
			}
		})
	}
}

func TestOperationsRefuseAnUnexportedEmbeddedPointerBeforeACall(t *testing.T) {
	typePlanQuietLogs(t)
	sent := typePlanRecordCalls(t, `{"exp":"E","own":"O"}`)

	refused := func(t *testing.T, err error) {
		t.Helper()
		var refusal *types.OperationError
		if !errors.As(err, &refusal) || refusal.Kind != types.KindConfiguration {
			t.Errorf("got %v, want a configuration refusal", err)
		}
	}
	t.Run("Extract", func(t *testing.T) {
		_, err := Extract[unexportedEmbeddedByPointer]("anything", NewExtractOptions())
		refused(t, err)
	})
	t.Run("Extract in Strict mode", func(t *testing.T) {
		_, err := Extract[unexportedEmbeddedByPointer]("anything", NewExtractOptions().WithMode(types.Strict))
		refused(t, err)
	})
	t.Run("Generate", func(t *testing.T) {
		_, err := Generate[unexportedEmbeddedByPointer]("anything", NewGenerateOptions())
		refused(t, err)
	})
	t.Run("Transform to it", func(t *testing.T) {
		_, err := Transform[benchFlat, unexportedEmbeddedByPointer](benchFlat{OrderID: "A-2207"}, NewTransformOptions())
		refused(t, err)
	})
	if len(*sent) != 0 {
		t.Errorf("%d calls were made for a type that is refused", len(*sent))
	}

	// The same struct embedded by value is read, and is sent.
	t.Run("the same struct by value is not refused", func(t *testing.T) {
		got, err := Extract[unexportedEmbeddedByValue]("anything", NewExtractOptions())
		if err != nil || got.Exp != "E" || got.Own != "O" {
			t.Errorf("Extract = %+v, %v", got, err)
		}
	})
}

func TestEmbeddingCycleFinderAnswersForEachShapeOfEmbedding(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		want   bool
	}{
		{"a pointer to itself", reflect.TypeFor[selfEmbedding](), true},
		{"the same, exported", reflect.TypeFor[SelfEmbeddingExported](), true},
		{"a ring of two", reflect.TypeFor[embeddingRingFirst](), true},
		{"the other end of it", reflect.TypeFor[embeddingRingSecond](), true},
		{"a ring of three", reflect.TypeFor[embeddingRingOfThreeFirst](), true},
		{"its middle", reflect.TypeFor[embeddingRingOfThreeSecond](), true},
		{"a type that embeds a ring", reflect.TypeFor[embeddingARing](), true},
		{"a chain of twelve", reflect.TypeFor[embeddedChain1](), false},
		{"the end of the chain", reflect.TypeFor[embeddedChain12](), false},
		{"a diamond", reflect.TypeFor[diamondTop](), false},
		{"a named field that refers back", reflect.TypeFor[embeddedWithAParent](), false},
		{"a recursion through a slice", reflect.TypeFor[selfEmbeddingByValueInASlice](), false},
		{"a scalar", reflect.TypeFor[int](), false},
		{"a pointer to a scalar", reflect.TypeFor[*int](), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := embeddingCycle(tc.target); got != tc.want {
				t.Errorf("embeddingCycle = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- The option format.
//
// encoding/json does not support the option format of a json tag: the first time
// it writes or reads a type that has a field with one, it fails, whatever the
// field's type or the option's value, and each later call fails the same way. A
// value that is empty, or is neither an identifier nor a string in single quotes,
// is not read as the option and the tag is taken without it.

type formatInner struct {
	When string `json:"when,format:RFC3339"`
}

type formatUnexportedInner struct {
	When string `json:"when,format:RFC3339"`
}

type formatEmbedsValue struct {
	formatInner
	N int `json:"n"`
}

type formatEmbedsUnexported struct {
	formatUnexportedInner
	N int `json:"n"`
}

type FormatInnerExported struct {
	When string `json:"when,format:RFC3339"`
}

type formatEmbedsPointer struct {
	*FormatInnerExported
	N int `json:"n"`
}

type formatDeep struct {
	Layers map[string][]*formatInner `json:"layers"`
}

type formatHiddenBehindATag struct {
	Skipped formatInner `json:"-"`
	Name    string      `json:"name"`
}

type formatNotExported struct {
	skipped formatInner
	Name    string `json:"name"`
}

type formatReadsItself struct {
	When string `json:"when,format:RFC3339"`
}

func (formatReadsItself) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }
func (*formatReadsItself) UnmarshalJSON([]byte) error  { return nil }

type formatReadsItselfOnly struct {
	When string `json:"when,format:RFC3339"`
}

func (*formatReadsItselfOnly) UnmarshalJSON([]byte) error { return nil }

type formatWritesItselfOnly struct {
	When string `json:"when,format:RFC3339"`
}

func (formatWritesItselfOnly) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

// formatField is a struct with one field of the given type and json tag.
func formatField(fieldType reflect.Type, tag string) reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "Field", Type: fieldType, Tag: reflect.StructTag(`json:` + strconv.Quote(tag))},
	})
}

// formatBreaksEncodingJSON is whether encoding/json fails to read the input into
// the type, or to write what it read.
func formatBreaksEncodingJSON(t *testing.T, target reflect.Type, input string) bool {
	t.Helper()
	value := reflect.New(target)
	if err := json.Unmarshal([]byte(input), value.Interface()); err != nil {
		return true
	}
	_, err := json.Marshal(value.Interface())
	return err != nil
}

func TestClassifyTypeRejectsATypeWhoseJSONTagHasAFormat(t *testing.T) {
	fieldTypes := map[string]reflect.Type{
		"string": reflect.TypeFor[string](), "int": reflect.TypeFor[int](), "int64": reflect.TypeFor[int64](),
		"float64": reflect.TypeFor[float64](), "bool": reflect.TypeFor[bool](), "time": reflect.TypeFor[time.Time](),
		"pointer to time": reflect.TypeFor[*time.Time](), "bytes": reflect.TypeFor[[]byte](),
		"array of bytes": reflect.TypeFor[[2]byte](), "strings": reflect.TypeFor[[]string](),
		"struct": reflect.TypeFor[formatNotExported](), "pointer to int": reflect.TypeFor[*int](),
		"map": reflect.TypeFor[map[string]int](), "duration": reflect.TypeFor[time.Duration](),
		"raw message": reflect.TypeFor[json.RawMessage](), "number": reflect.TypeFor[json.Number](),
	}
	// Each is a tag whose format encoding/json does not read as an option and
	// takes the tag without, and each one it does read.
	tags := []struct {
		option string
		// refused is what the option does to the type.
		refused bool
	}{
		{"format:RFC3339", true}, {"format:unix", true}, {"format:base64", true}, {"format:hex", true},
		{"format:emitnull", true}, {"format:array", true}, {"format:x", true}, {"format:_x1", true},
		{"format:'a,b'", true}, {"format:'15:04'", true}, {"format:'it\\'s'", true},
		{"omitempty,format:x", true}, {"format:x,string", true}, {"format:x,omitempty", true},
		{"format:x,format:y", true},
		{"format:", false}, {"format:$", false}, {"format:2006-01-02", false}, {"format:'unterminated", false},
		{"format:''", false}, {"format", false}, {"format ", false}, {"Format:x", false}, {"formats:x", false},
		{"omitempty", false}, {"case:strict", false}, {"embed", false}, {"string", false},
	}

	for typeName, fieldType := range fieldTypes {
		for _, tc := range tags {
			t.Run(typeName+"/"+tc.option, func(t *testing.T) {
				target := formatField(fieldType, "v,"+tc.option)
				if broken := formatBreaksEncodingJSON(t, target, `{}`); broken != tc.refused {
					t.Fatalf("encoding/json fails on a type with the tag %q = %v, the table says %v", tc.option, broken, tc.refused)
				}

				report := ClassifyType(target)
				if !tc.refused {
					if report.Support == SupportRejected {
						t.Errorf("the type is rejected (%s), and encoding/json reads and writes it", report.Reason)
					}
					if err := PreflightType(target); err != nil {
						t.Errorf("PreflightType refused it: %v", err)
					}
					return
				}
				if report.Support != SupportRejected {
					t.Fatalf("support = %v (%s), and encoding/json fails on the type", report.Support, report.Reason)
				}
				if report.Path != "v" {
					t.Errorf("path = %q, want the name of the field", report.Path)
				}
				if !strings.Contains(report.Reason, "format") {
					t.Errorf("reason %q does not name the option", report.Reason)
				}
				err := PreflightType(target)
				var refusal *types.OperationError
				if !errors.As(err, &refusal) || refusal.Kind != types.KindConfiguration {
					t.Fatalf("PreflightType gave %v, want a configuration refusal", err)
				}
				if !strings.Contains(err.Error(), "No provider call was made") || !strings.Contains(err.Error(), ": v cannot be used as an operation contract") {
					t.Errorf("the refusal does not name the field and say the call was skipped: %v", err)
				}
			})
		}
	}
}

func TestClassifyTypeFindsAFormatWhereverEncodingJSONReachesIt(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		// input is an answer that reaches the field.
		input string
		// path is where the report says the option is, empty when the type is not refused.
		path string
	}{
		{"a field of the type", reflect.TypeFor[formatInner](), `{}`, "when"},
		{"a pointer to the type", reflect.TypeFor[*formatInner](), `{}`, "when"},
		{"a slice of the type", reflect.TypeFor[[]formatInner](), `[{}]`, "[].when"},
		{"an array of the type", reflect.TypeFor[[2]formatInner](), `[{},{}]`, "[].when"},
		{"a map of the type", reflect.TypeFor[map[string]formatInner](), `{"k":{}}`, "{}.when"},
		{"held by a pointer", reflect.TypeFor[struct {
			P *formatInner `json:"p"`
		}](), `{"p":{}}`, "p.when"},
		{"held by a slice", reflect.TypeFor[struct {
			P []formatInner `json:"p"`
		}](), `{"p":[{}]}`, "p.[].when"},
		{"held by a map", reflect.TypeFor[struct {
			P map[string]formatInner `json:"p"`
		}](), `{"p":{"k":{}}}`, "p.{}.when"},
		{"held by an array", reflect.TypeFor[struct {
			P [2]formatInner `json:"p"`
		}](), `{"p":[{},{}]}`, "p.[].when"},
		{"held by a field of a field", reflect.TypeFor[struct {
			Outer struct {
				Inner formatInner `json:"inner"`
			} `json:"outer"`
		}](), `{"outer":{"inner":{}}}`, "outer.inner.when"},
		{"deep", reflect.TypeFor[formatDeep](), `{"layers":{"a":[{}]}}`, "layers.{}.[].when"},
		{"an embedded struct", reflect.TypeFor[formatEmbedsValue](), `{}`, "formatInner.when"},
		{"an embedded struct that is not exported", reflect.TypeFor[formatEmbedsUnexported](), `{}`, "formatUnexportedInner.when"},
		{"an embedded pointer", reflect.TypeFor[formatEmbedsPointer](), `{"when":"x"}`, "FormatInnerExported.when"},
		{"a type that writes itself and does not read itself", reflect.TypeFor[formatWritesItselfOnly](), `{}`, "when"},
		{"a type that reads itself and does not write itself", reflect.TypeFor[formatReadsItselfOnly](), `{}`, "when"},

		{"a field the tag leaves out", reflect.TypeFor[formatHiddenBehindATag](), `{"name":"n"}`, ""},
		{"a field that is not exported", reflect.TypeFor[formatNotExported](), `{"name":"n"}`, ""},
		{"a type that reads and writes itself", reflect.TypeFor[formatReadsItself](), `{}`, ""},
		{"a type that holds one that reads and writes itself", reflect.TypeFor[struct {
			P formatReadsItself `json:"p"`
		}](), `{"p":{}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if broken := formatBreaksEncodingJSON(t, tc.target, tc.input); broken != (tc.path != "") {
				t.Fatalf("encoding/json fails on %s = %v, the table says %v", tc.input, broken, tc.path != "")
			}
			report := ClassifyType(tc.target)
			if tc.path == "" {
				if report.Support == SupportRejected {
					t.Errorf("the type is rejected (%s) and encoding/json reads and writes it", report.Reason)
				}
				return
			}
			if report.Support != SupportRejected {
				t.Fatalf("support = %v (%s), and encoding/json fails on the type", report.Support, report.Reason)
			}
			if report.Path != tc.path {
				t.Errorf("path = %q, want %q", report.Path, tc.path)
			}
			if !strings.Contains(report.Reason, "format") {
				t.Errorf("reason %q does not name the option", report.Reason)
			}
		})
	}
}

type formatRecord struct {
	Name string `json:"name"`
	When string `json:"when,format:RFC3339"`
}

type formatList struct {
	Items []formatRecord `json:"items"`
}

type formatEmptyRecord struct {
	Name string `json:"name"`
	When string `json:"when,format:"`
}

func TestOperationsRefuseATypeWithAFormatBeforeACall(t *testing.T) {
	typePlanQuietLogs(t)
	sent := typePlanRecordCalls(t, `{"name":"n","when":"w","items":[{"name":"n","when":"w"}]}`)

	refused := func(t *testing.T, err error, path string) {
		t.Helper()
		var refusal *types.OperationError
		if !errors.As(err, &refusal) || refusal.Kind != types.KindConfiguration {
			t.Fatalf("got %v, want a configuration refusal", err)
		}
		for _, part := range []string{path, "format", "No provider call was made"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("the refusal %q does not say %q", err, part)
			}
		}
	}
	t.Run("Extract", func(t *testing.T) {
		_, err := Extract[formatRecord]("anything", NewExtractOptions())
		refused(t, err, "when")
	})
	t.Run("Extract in Strict mode", func(t *testing.T) {
		_, err := Extract[formatRecord]("anything", NewExtractOptions().WithMode(types.Strict))
		refused(t, err, "when")
	})
	t.Run("Extract of a list of them", func(t *testing.T) {
		_, err := Extract[formatList]("anything", NewExtractOptions())
		refused(t, err, "items.[].when")
	})
	t.Run("Extract of a pointer to one", func(t *testing.T) {
		_, err := Extract[*formatRecord]("anything", NewExtractOptions())
		refused(t, err, "when")
	})
	t.Run("Generate", func(t *testing.T) {
		_, err := Generate[formatRecord]("anything", NewGenerateOptions())
		refused(t, err, "when")
	})
	t.Run("Transform to it", func(t *testing.T) {
		_, err := Transform[benchFlat, formatList](benchFlat{OrderID: "A-2207"}, NewTransformOptions())
		refused(t, err, "items.[].when")
	})
	if len(*sent) != 0 {
		t.Errorf("%d calls were made for types that are refused", len(*sent))
	}

	// A tag whose format is empty is taken without it, and the type is read.
	t.Run("a format with no value is not refused", func(t *testing.T) {
		before := len(*sent)
		got, err := Extract[formatEmptyRecord]("anything", NewExtractOptions())
		if err != nil || got.Name != "n" || got.When != "w" {
			t.Errorf("Extract = %+v, %v", got, err)
		}
		if len(*sent) != before+1 {
			t.Errorf("%d calls were made, want 1", len(*sent)-before)
		}
	})
}

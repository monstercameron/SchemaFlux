package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// S-003. Requiredness was inferred from `omitempty`, which is a serialization
// directive: it tells encoding/json to leave a zero value out of the output.
// Reading it as "optional" conflates two statements, so a caller who wrote
// `json:"middle_name,omitempty"` to keep their JSON tidy had silently made the
// field optional to Strict mode -- and a caller who wanted an optional field
// and did not know the convention got a required one.
func TestFieldRequiredness(t *testing.T) {
	type sample struct {
		Plain          string  `json:"plain"`
		OmitEmpty      string  `json:"omit_empty,omitempty"`
		TaggedRequired string  `json:"tagged_required,omitempty" schemaflux:"required"`
		TaggedOptional string  `json:"tagged_optional" schemaflux:"optional"`
		Pointer        *string `json:"pointer"`
		PointerTagged  *string `json:"pointer_tagged" schemaflux:"required"`
		Untagged       string
		MixedCase      string `json:"mixed" schemaflux:"OPTIONAL"`
		Unknown        string `json:"unknown" schemaflux:"something-else"`
	}

	cases := []struct {
		field string
		want  Requiredness
		why   string
	}{
		{"Plain", FieldRequired, "no tag, not a pointer"},
		{"OmitEmpty", FieldOptional, "the legacy inference still applies"},
		{"TaggedRequired", FieldRequired, "the explicit tag beats omitempty"},
		{"TaggedOptional", FieldOptional, "the explicit tag beats the absence of omitempty"},
		{"Pointer", FieldOptional, "a pointer can be nil, which is how Go says absent"},
		{"PointerTagged", FieldRequired, "an explicit tag beats the type"},
		{"Untagged", FieldRequired, "no json tag at all"},
		{"MixedCase", FieldOptional, "the tag value folds case"},
		{"Unknown", FieldRequired, "an unrecognised tag value is ignored, not guessed at"},
	}

	sampleType := reflect.TypeOf(sample{})
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			field, ok := sampleType.FieldByName(tc.field)
			if !ok {
				t.Fatalf("no field named %s", tc.field)
			}
			if got := FieldRequiredness(field); got != tc.want {
				t.Errorf("FieldRequiredness = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

// The schema the model sees and the validation Strict applies read the same
// resolver, so they cannot disagree about which fields are required -- which
// they would have, the moment one of them learned about the tag first.
func TestTheSchemaAndTheValidatorAgree(t *testing.T) {
	type record struct {
		Required string `json:"required"`
		Optional string `json:"optional" schemaflux:"optional"`
		Tidy     string `json:"tidy,omitempty" schemaflux:"required"`
	}

	schema := GenerateTypeSchema(reflect.TypeOf(record{}))

	// The schema marks exactly the two required fields.
	for _, line := range strings.Split(schema, "\n") {
		switch {
		case strings.Contains(line, "required:"):
			if !strings.Contains(line, "(required)") {
				t.Errorf("the schema does not mark `required` as required: %s", line)
			}
		case strings.Contains(line, "tidy:"):
			if !strings.Contains(line, "(required)") {
				t.Errorf("the schema does not honour schemaflux:\"required\" over omitempty: %s", line)
			}
		case strings.Contains(line, "optional:"):
			if strings.Contains(line, "(required)") {
				t.Errorf("the schema marks an explicitly optional field required: %s", line)
			}
		}
	}

	// And the validator agrees, field for field.
	if err := ValidateExtractedData(record{Required: "x", Tidy: "y"}); err != nil {
		t.Errorf("validation rejected a record with both required fields set: %v", err)
	}
	if err := ValidateExtractedData(record{Required: "x"}); err == nil {
		t.Error("validation accepted a record missing a schemaflux:\"required\" field")
	}
	if err := ValidateExtractedData(record{Tidy: "y"}); err == nil {
		t.Error("validation accepted a record missing an untagged field")
	}
}

// A pointer field is optional without ceremony, which is the idiom a Go caller
// already reaches for.
func TestPointerFieldsAreOptionalToTheValidator(t *testing.T) {
	type record struct {
		Name   string  `json:"name"`
		Middle *string `json:"middle"`
	}

	if err := ValidateExtractedData(record{Name: "Ada"}); err != nil {
		t.Errorf("a nil pointer field was treated as missing: %v", err)
	}
	if err := ValidateExtractedData(record{}); err == nil {
		t.Error("validation accepted a record with no name")
	}
}

// The JSON Schema sent to the provider has to say the same thing as the prose
// schema in the prompt and the check applied to the answer. Three descriptions
// of one contract that can disagree is three chances to.
func TestTheJSONSchemaAgreesAboutOptionality(t *testing.T) {
	type record struct {
		Required string `json:"required"`
		Optional string `json:"optional" schemaflux:"optional"`
		Tidy     string `json:"tidy,omitempty" schemaflux:"required"`
	}

	schema := GenerateJSONSchema(reflect.TypeOf(record{}))
	if schema == nil {
		t.Fatal("GenerateJSONSchema returned nothing for a plain struct")
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("no properties in %v", schema)
	}

	isNullable := func(name string) bool {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("no property %q", name)
		}
		raw, ok := property["type"]
		if !ok {
			return false
		}
		// The union is []string in this encoder; accept []any too so the test
		// does not pin an internal representation it does not care about.
		switch list := raw.(type) {
		case []string:
			for _, entry := range list {
				if entry == "null" {
					return true
				}
			}
		case []any:
			for _, entry := range list {
				if entry == "null" {
					return true
				}
			}
		}
		return false
	}

	if isNullable("required") {
		t.Error("a required field was made nullable")
	}
	if !isNullable("optional") {
		t.Error("an explicitly optional field was not made nullable")
	}
	if isNullable("tidy") {
		t.Error("schemaflux:\"required\" lost to omitempty in the JSON Schema")
	}
}

// validateRequiredFieldsByTags is the check of Strict's required fields as it
// was written before the fields of a type were read once and kept: it reads the
// tags of every field of every value, and builds the path of every field and
// list element as it goes. It is here to say what the check has to answer, so
// that making it cheaper cannot change an answer.
func validateRequiredFieldsByTags(value reflect.Value, path string, depth int) error {
	if depth > maxValidationDepth {
		return nil
	}
	for value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface {
		if value.IsNil() {
			if path == "" {
				return fmt.Errorf("data cannot be nil pointer")
			}
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Struct:
		structType := value.Type()
		for i := 0; i < structType.NumField(); i++ {
			field := structType.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := field.Tag.Get("json")
			parts := strings.Split(tag, ",")
			if tag != "" && parts[0] == "-" && len(parts) == 1 {
				continue
			}
			name := field.Name
			if tag != "" && parts[0] != "" {
				name = parts[0]
			}
			fieldPath := name
			if path != "" {
				fieldPath = path + "." + name
			}
			fieldValue := value.Field(i)
			if IsRequired(field) && fieldValue.IsZero() {
				return fmt.Errorf("required field %s is empty; %s", fieldPath, optionalFieldRemedy)
			}
			if !fieldValue.IsZero() {
				if err := validateRequiredFieldsByTags(fieldValue, fieldPath, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			element := value.Index(i)
			if element.Kind() == reflect.Struct || element.Kind() == reflect.Ptr || element.Kind() == reflect.Interface {
				if err := validateRequiredFieldsByTags(element, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	case reflect.Map:
		for _, key := range value.MapKeys() {
			element := value.MapIndex(key)
			if element.Kind() == reflect.Struct || element.Kind() == reflect.Ptr || element.Kind() == reflect.Interface {
				if err := validateRequiredFieldsByTags(element, fmt.Sprintf("%s[%v]", path, key.Interface()), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return nil
	}
}

type requiredPlanInner struct {
	Code string `json:"code"`
	Note string `json:"note,omitempty"`
}

type requiredPlanLink struct {
	Label string            `json:"label"`
	Next  *requiredPlanLink `json:"next"`
}

type requiredPlanOuter struct {
	ID      string                       `json:"id"`
	Inner   requiredPlanInner            `json:"inner"`
	Ptr     *requiredPlanInner           `json:"ptr"`
	List    []requiredPlanInner          `json:"list,omitempty"`
	Ptrs    []*requiredPlanInner         `json:"ptrs,omitempty"`
	ByName  map[string]requiredPlanInner `json:"by_name,omitempty"`
	Any     any                          `json:"any,omitempty"`
	Skipped string                       `json:"-"`
	Flag    bool                         `schemaflux:"optional"`
	NoTag   string
	Named   string `json:",omitempty"`
	hidden  string
}

func requiredPlanFilled() requiredPlanOuter {
	return requiredPlanOuter{ID: "a", Inner: requiredPlanInner{Code: "c"}, NoTag: "n"}
}

func TestTheRequiredFieldCheckAnswersAsItDidBeforeTheFieldsWereKept(t *testing.T) {
	filled := func(change func(*requiredPlanOuter)) requiredPlanOuter {
		value := requiredPlanFilled()
		change(&value)
		return value
	}
	chain := func(length int) *requiredPlanLink {
		var head *requiredPlanLink
		for n := length; n > 0; n-- {
			head = &requiredPlanLink{Label: "l", Next: head}
		}
		return head
	}
	emptyCode := requiredPlanInner{Note: "n"}
	var nilPointer *requiredPlanOuter

	cases := []struct {
		name  string
		value any
	}{
		{"everything filled", requiredPlanFilled()},
		{"the first field empty", filled(func(o *requiredPlanOuter) { o.ID = "" })},
		{"a nested field empty", filled(func(o *requiredPlanOuter) { o.Inner.Code = "" })},
		{"a field with no tag empty", filled(func(o *requiredPlanOuter) { o.NoTag = "" })},
		{"an optional field empty", filled(func(o *requiredPlanOuter) { o.Flag = false; o.Named = "" })},
		{"a pointer filled with an empty field", filled(func(o *requiredPlanOuter) { o.Ptr = &emptyCode })},
		{"a nil pointer", filled(func(o *requiredPlanOuter) { o.Ptr = nil })},
		{"a list with an element that fails", filled(func(o *requiredPlanOuter) {
			o.List = []requiredPlanInner{{Code: "a"}, {Code: ""}, {Code: "c"}}
		})},
		{"a list that passes", filled(func(o *requiredPlanOuter) { o.List = []requiredPlanInner{{Code: "a"}, {Code: "b"}} })},
		{"a list of pointers with a nil and a failure", filled(func(o *requiredPlanOuter) {
			o.Ptrs = []*requiredPlanInner{nil, {Code: "ok"}, {}}
		})},
		{"a map whose element fails", filled(func(o *requiredPlanOuter) {
			o.ByName = map[string]requiredPlanInner{"key": {}}
		})},
		{"a map whose elements pass", filled(func(o *requiredPlanOuter) {
			o.ByName = map[string]requiredPlanInner{"a": {Code: "x"}, "b": {Code: "y"}}
		})},
		{"an interface holding a struct that fails", filled(func(o *requiredPlanOuter) { o.Any = requiredPlanInner{} })},
		{"an interface holding a pointer to one that passes", filled(func(o *requiredPlanOuter) { o.Any = &requiredPlanInner{Code: "c"} })},
		{"a field the tag leaves out", filled(func(o *requiredPlanOuter) { o.Skipped = "" })},
		{"an unexported field empty", filled(func(o *requiredPlanOuter) { o.hidden = "" })},
		{"a pointer to the whole value", func() any { v := requiredPlanFilled(); return &v }()},
		{"a nil pointer to the whole value", nilPointer},
		{"a list at the top that fails", []requiredPlanInner{{Code: "a"}, {}}},
		{"a map at the top that fails", map[string]requiredPlanInner{"k": {}}},
		{"a chain within the bound", chain(3)},
		{"a chain past the bound", chain(12)},
		{"a string", "text"},
		{"a number", 7},
		{"an empty struct", struct{}{}},
		{"an anonymous struct that fails", struct {
			A string `json:"a"`
			B []requiredPlanInner
		}{B: []requiredPlanInner{{}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := validateRequiredFieldsByTags(reflect.ValueOf(tc.value), "", 0)
			got := ValidateExtractedData(tc.value)
			switch {
			case want == nil && got == nil:
			case want == nil || got == nil:
				t.Errorf("ValidateExtractedData = %v, the check by tags gives %v", got, want)
			case got.Error() != want.Error():
				t.Errorf("the message differs:\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestTheRequiredFieldCheckNamesTheFailingPath(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"a top-level field", requiredPlanOuter{}, "required field id is empty"},
		{"a nested field", func() any { v := requiredPlanFilled(); v.Inner = requiredPlanInner{Note: "n"}; return v }(), "required field inner.code is empty"},
		{"a list element", func() any {
			v := requiredPlanFilled()
			v.List = []requiredPlanInner{{Code: "a"}, {}}
			return v
		}(), "required field list[1].code is empty"},
		{"a list at the top", []requiredPlanInner{{}}, "required field [0].code is empty"},
		{"a map element", func() any {
			v := requiredPlanFilled()
			v.ByName = map[string]requiredPlanInner{"k": {}}
			return v
		}(), "required field by_name[k].code is empty"},
		{"a pointer in a list", func() any {
			v := requiredPlanFilled()
			v.Ptrs = []*requiredPlanInner{{}}
			return v
		}(), "required field ptrs[0].code is empty"},
		{"a chain", &requiredPlanLink{Label: "a", Next: &requiredPlanLink{Next: nil}}, "required field next.label is empty"},
		{"a nil pointer", (*requiredPlanOuter)(nil), "data cannot be nil pointer"},
		{"nil", nil, "data cannot be nil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExtractedData(tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateExtractedData = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestTheRequiredPlanListsTheFieldsTheCheckLooksAt(t *testing.T) {
	plan := typePlanFor(reflect.TypeFor[requiredPlanOuter]()).requiredFields()
	var names []string
	required := map[string]bool{}
	for _, field := range plan.fields {
		names = append(names, field.name)
		required[field.name] = field.required
	}
	want := []string{"id", "inner", "ptr", "list", "ptrs", "by_name", "any", "Flag", "NoTag", "Named"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("fields = %v, want %v (unexported and `json:\"-\"` fields are left out, in declared order)", names, want)
	}
	for name, wantRequired := range map[string]bool{
		"id": true, "inner": true, "ptr": false, "list": false, "any": false, "Flag": false, "NoTag": true, "Named": false,
	} {
		if required[name] != wantRequired {
			t.Errorf("field %s: required = %v, want %v", name, required[name], wantRequired)
		}
	}
	for n, field := range plan.fields {
		structField := reflect.TypeFor[requiredPlanOuter]().Field(field.index[0])
		if IsRequired(structField) != field.required {
			t.Errorf("field %d (%s) disagrees with IsRequired", n, field.name)
		}
	}
}

func TestTheRequiredFieldCheckAllocationsStayWithinBudget(t *testing.T) {
	typePlanFreshCache(t)
	nested := benchNested{OrderID: "A", Customer: "c", Email: "e", Currency: "u", Status: "s", PlacedOn: "p", Total: 1,
		Gift: true, Priority: 1,
		ShipTo: benchAddress{Street: "s", City: "c", Region: "r", Postcode: "p", Country: "c"},
		Items:  []benchLineItem{{SKU: "a", Description: "d", Quantity: 1, UnitPrice: 2}, {SKU: "b", Description: "d", Quantity: 1, UnitPrice: 2}},
		Tags:   []string{"x"}}
	if err := ValidateExtractedData(&nested); err != nil {
		t.Fatalf("the value fails the check: %v", err)
	}
	// Handed over as a pointer, so that the boxing of the argument is not what
	// is counted. The check made some 190 allocations for a value of this shape.
	if allocs := testing.AllocsPerRun(100, func() { _ = ValidateExtractedData(&nested) }); allocs > 0 {
		t.Errorf("checking a value that passes makes %.0f allocations, over its budget of 0", allocs)
	}

	failing := nested
	failing.Items = []benchLineItem{{SKU: "a", Description: "d", Quantity: 1, UnitPrice: 2}, {}}
	// Seven: the six of the message and its path, and the carrier that gives the
	// refusal its kind. A value that passes still makes none.
	if allocs := testing.AllocsPerRun(100, func() { _ = ValidateExtractedData(&failing) }); allocs > 7 {
		t.Errorf("checking a value that fails makes %.0f allocations, over its budget of 7", allocs)
	}
}

// A map is visited in the order of its keys, so a value with more than one
// entry that fails the check reports the same entry every time. It reported
// whichever the runtime's iteration order gave first, which is not the same one
// from one run to the next.
func TestTheRequiredFieldCheckReportsTheFirstFailingMapEntryByKey(t *testing.T) {
	type entry struct {
		Name string
		Note string `json:"note,omitempty"`
	}
	t.Run("string keys", func(t *testing.T) {
		value := map[string]*entry{"zeta": {}, "alpha": {}, "mid": {}, "omega": {Name: "ok"}}
		for i := 0; i < 200; i++ {
			err := ValidateExtractedData(value)
			if err == nil || !strings.Contains(err.Error(), "[alpha].Name") {
				t.Fatalf("run %d: %v, want the entry with the lowest key", i, err)
			}
		}
	})
	t.Run("integer keys", func(t *testing.T) {
		value := map[int]entry{30: {}, 7: {}, 12: {}}
		first := ""
		for i := 0; i < 200; i++ {
			err := ValidateExtractedData(value)
			if err == nil {
				t.Fatal("an entry with no name passed the check")
			}
			if first == "" {
				first = err.Error()
			} else if err.Error() != first {
				t.Fatalf("run %d: %q, and before that %q", i, err.Error(), first)
			}
		}
	})
}

// requiredFillFor is a value that fills a property of a schema: the text a string
// is given, a number, and an object holding each of its own properties.
func requiredFillFor(t *testing.T, property any) any {
	t.Helper()
	declared, _ := property.(map[string]any)
	switch schemaKind(t, property) {
	case "string":
		return "x"
	case "integer":
		return 1
	case "number":
		return 1.5
	case "boolean":
		return true
	case "object":
		object := map[string]any{}
		properties, _ := declared["properties"].(map[string]any)
		for name, inner := range properties {
			object[name] = requiredFillFor(t, inner)
		}
		return object
	}
	t.Fatalf("no value for the property %v", property)
	return nil
}

// requiredEmptyPath is the path a failure of the check names, and whether the
// error is a failure of the check.
func requiredEmptyPath(err error) (string, bool) {
	text, found := strings.CutPrefix(err.Error(), "required field ")
	if !found {
		return "", false
	}
	path, _, found := strings.Cut(text, " is empty; ")
	return path, found
}

// The check of a Strict answer is held to the members encoding/json reads: a
// value that encoding/json filled, and left one member of empty, fails on that
// member, wherever the member is promoted from, and a value with every member
// filled passes. The values are made by encoding/json itself reading an object.
func TestTheRequiredFieldCheckHoldsEveryMemberEncodingJSONReads(t *testing.T) {
	for _, fact := range embeddingFacts() {
		t.Run(fact.name, func(t *testing.T) {
			if len(fact.members) == 0 {
				if err := ValidateExtractedData(reflect.New(fact.target).Interface()); err != nil {
					t.Errorf("a type with no member failed the check: %v", err)
				}
				return
			}
			schema := GenerateJSONSchema(fact.target)
			if schema == nil {
				t.Fatal("no schema to read the members' types from")
			}
			properties, _ := schema["properties"].(map[string]any)

			read := func(skip string) any {
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
				value := reflect.New(fact.target)
				if err := json.Unmarshal(raw, value.Interface()); err != nil {
					t.Fatalf("encoding/json cannot read %s: %v", raw, err)
				}
				return value.Interface()
			}

			if err := ValidateExtractedData(read("")); err != nil {
				t.Errorf("every member is filled and the check fails: %v", err)
			}
			for _, member := range fact.members {
				err := ValidateExtractedData(read(member))
				if err == nil {
					t.Errorf("%s was left empty and the check passed", member)
					continue
				}
				path, found := requiredEmptyPath(err)
				if !found || (path != member && !strings.HasSuffix(path, "."+member)) {
					t.Errorf("%s was left empty and the check says %q", member, err)
				}
			}
		})
	}
}

func TestTheRequiredFieldCheckNamesTheRouteOfAPromotedMember(t *testing.T) {
	filledExported := PromptBaseExported{ID: "i"}
	cases := []struct {
		name  string
		value any
		// want is the path a failure names, or empty when the value passes.
		want string
	}{
		{"an unexported struct embedded by value, empty", unexportedEmbeddedByValue{Own: "o"}, "unexportedBase.exp"},
		{"an unexported struct embedded by value, filled", unexportedEmbeddedByValue{unexportedBase: unexportedBase{Exp: "e"}, Own: "o"}, ""},
		{"an unexported struct, and the outer field empty", unexportedEmbeddedByValue{unexportedBase: unexportedBase{Exp: "e"}}, "own"},
		{"an exported struct embedded by value", promptEmbedsExported{Name: "n"}, "PromptBaseExported.id"},
		{"an exported struct embedded by value, filled", promptEmbedsExported{PromptBaseExported: filledExported, Name: "n"}, ""},
		{"an exported pointer that is nil", promptEmbedsPointer{Name: "n", Tail: "t"}, "PromptBaseExported.id"},
		{"an exported pointer with an empty member", promptEmbedsPointer{Name: "n", Tail: "t", PromptBaseExported: &PromptBaseExported{}}, "PromptBaseExported.id"},
		{"an exported pointer, filled", promptEmbedsPointer{Name: "n", Tail: "t", PromptBaseExported: &filledExported}, ""},
		{"an exported pointer, the outer member after it empty", promptEmbedsPointer{Name: "n", PromptBaseExported: &filledExported}, "tail"},
		{"an unexported struct that holds an exported pointer", unexportedHoldingExportedPointer{S: "s"}, "pointerInsideUnexported.PlainEmbedded.plain"},
		{"a struct embedded under a tag of options only", optionsOnlyTagEmbedding{Z: 1}, "PlainEmbedded.plain"},
		{"a struct embedded under a tag of options only, filled", optionsOnlyTagEmbedding{PlainEmbedded: PlainEmbedded{Plain: "p"}, Z: 1}, ""},
		{"a diamond: the ambiguous name is not asked for", diamondTop{diamondLeft: diamondLeft{Left: "l"}, diamondRight: diamondRight{Right: "r"}}, ""},
		{"a diamond: a member that is asked for", diamondTop{diamondLeft: diamondLeft{Left: "l"}}, "diamondRight.right"},
		{"two structs declaring a name: the name is not asked for", twinNamesTop{twinNameFirstRoute{twinNameFirst{A: 1}}, twinNameSecondRoute{twinNameSecond{B: 2}}}, ""},
		{"an outer field over an embedded one", outerNameHidesTwin{twinNameFirst: twinNameFirst{A: 1}}, "name"},
		{"an outer field over an embedded one, filled", outerNameHidesTwin{twinNameFirst: twinNameFirst{A: 1}, Name: true}, ""},
		{"a field below an exported struct, in a list", []promptEmbedsExported{{PromptBaseExported: filledExported, Name: "n"}, {Name: "n"}}, "[1].PromptBaseExported.id"},
		{"a field below an unexported struct, in a field", struct {
			Inner promptEmbedsUnexported `json:"inner"`
		}{Inner: promptEmbedsUnexported{Name: "n"}}, "inner.promptBase.id"},
		{"a chain of twelve embedded structs", embeddedChain1{Field1: "1"},
			"embeddedChain2.embeddedChain3.embeddedChain4.embeddedChain5.embeddedChain6.embeddedChain7.embeddedChain8.embeddedChain9.embeddedChain10.embeddedChain11.embeddedChain12.field12"},
		{"a struct embedded under a tag with a name is a field of that name", namedTagEmbedding{Z: 1}, "p"},
		{"an embedded type that is not a struct", labelEmbedding{K: 1}, "Label"},
		{"an unexported embedded type that is not a struct", hiddenLabelEmbedding{K: 1}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExtractedData(tc.value)
			if tc.want == "" {
				if err != nil {
					t.Errorf("the check fails a value it should pass: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("the check passed a value with %s empty", tc.want)
			}
			if path, found := requiredEmptyPath(err); !found || path != tc.want {
				t.Errorf("the check says %q, want it to name %s", err, tc.want)
			}
		})
	}
}

// An embedded struct is not itself a member: encoding/json reads the fields it
// promotes, and the check holds those and no more. A struct all of whose members
// are optional used to fail the check when it was left empty, because the struct
// as a field was held to be required.
func TestTheRequiredFieldCheckDoesNotHoldAnEmbeddedStructAsAField(t *testing.T) {
	type Optionals struct {
		Note string `json:"note,omitempty"`
		Memo string `json:"memo" schemaflux:"optional"`
	}
	type embedsOptionals struct {
		Optionals
		Name string `json:"name"`
	}
	type embedsOptionalsByPointer struct {
		*Optionals
		Name string `json:"name"`
	}
	if err := ValidateExtractedData(embedsOptionals{Name: "n"}); err != nil {
		t.Errorf("an empty struct with only optional members failed the check: %v", err)
	}
	if err := ValidateExtractedData(embedsOptionalsByPointer{Name: "n"}); err != nil {
		t.Errorf("a nil pointer to a struct with only optional members failed the check: %v", err)
	}
	if err := ValidateExtractedData(embedsOptionals{}); err == nil {
		t.Error("the outer member was left empty and the check passed")
	}
}

func TestTheRequiredPlanOfAStructWithEmbeddedStructsListsTheMembers(t *testing.T) {
	cases := []struct {
		name   string
		target reflect.Type
		want   []string
	}{
		{"a diamond", reflect.TypeFor[diamondTop](), []string{"left", "right"}},
		{"two structs declaring a name", reflect.TypeFor[twinNamesTop](), []string{"a", "b"}},
		{"an outer field over an embedded one", reflect.TypeFor[outerNameHidesTwin](), []string{"a", "name"}},
		{"a tagged name over an untagged one", reflect.TypeFor[taggedBeatsUntaggedTop](), []string{"Name"}},
		{"a name at two depths", reflect.TypeFor[shallowBeatsDeepTop](), []string{"name", "a", "q"}},
		{"a tag of options only", reflect.TypeFor[optionsOnlyTagEmbedding](), []string{"plain", "z"}},
		{"an unexported struct", reflect.TypeFor[unexportedEmbeddedByValue](), []string{"exp", "own"}},
		{"an unexported pointer", reflect.TypeFor[unexportedEmbeddedByPointer](), []string{"exp", "own"}},
		{"a tag with a name", reflect.TypeFor[namedTagEmbedding](), []string{"p", "z"}},
		{"a tag of a dash", reflect.TypeFor[omittedEmbedding](), []string{"z"}},
		{"an unexported type that is not a struct", reflect.TypeFor[hiddenLabelEmbedding](), []string{"k"}},
		{"a struct with no member", reflect.TypeFor[soleNamesTop](), []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names := []string{}
			for _, field := range typePlanFor(tc.target).requiredFields().fields {
				names = append(names, field.name)
			}
			if !slices.Equal(names, tc.want) {
				t.Errorf("the plan holds %v, encoding/json reads %v", names, tc.want)
			}
		})
	}
}

// Promoted members are reached through the plan's indexes and not looked up by
// name, so a value that passes costs nothing, as it does for a type with no
// embedding.
func TestTheRequiredFieldCheckOfPromotedMembersAllocatesNothingWhenItPasses(t *testing.T) {
	previousPlans := typePlans
	typePlans = newTypePlanCache(typePlanLimit)
	t.Cleanup(func() { typePlans = previousPlans })
	value := &promptEmbedsPointer{
		Name: "n", Tail: "t", PromptBaseExported: &PromptBaseExported{ID: "i"},
	}
	if err := ValidateExtractedData(value); err != nil {
		t.Fatalf("the value fails the check: %v", err)
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = ValidateExtractedData(value) }); allocs > 0 {
		t.Errorf("checking a value that passes makes %.0f allocations, over its budget of 0", allocs)
	}
	nested := &struct {
		Items []promptEmbedsUnexported `json:"items"`
	}{Items: []promptEmbedsUnexported{{promptBase{ID: "a"}, "n"}, {promptBase{ID: "b"}, "m"}}}
	if err := ValidateExtractedData(nested); err != nil {
		t.Fatalf("the value fails the check: %v", err)
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = ValidateExtractedData(nested) }); allocs > 0 {
		t.Errorf("checking a list that passes makes %.0f allocations, over its budget of 0", allocs)
	}
}

// What the library calls optional is what encoding/json leaves out when it is
// zero. Each tag of the facts table is written by encoding/json itself, and the
// three places that decide whether a member may be missing (FieldRequiredness,
// which the schemas and Strict mode use, and the answer reader's plan) have to say
// "optional" exactly when it did not write the zero value. The tags that are
// options only to the standard library (omitempty:x, omitzero with a space after
// it) and the near misses it does not take (omitempty1, Omitempty, " omitempty")
// are all in the table.
func TestOptionalityIsWhatEncodingJSONLeavesOutWhenZero(t *testing.T) {
	for _, fact := range jsonTagFacts() {
		if fact.omitted {
			continue
		}
		t.Run(fact.tag, func(t *testing.T) {
			structType := jsonTagStruct(fact.tag)
			raw, err := json.Marshal(reflect.New(structType).Interface())
			if err != nil {
				t.Fatal(err)
			}
			_, _, written := jsonTagKeys(t, raw)
			if written == fact.zeroLeftOut {
				t.Fatalf("encoding/json wrote %s for the zero value, and the table says it is left out = %v", raw, fact.zeroLeftOut)
			}

			field := structType.Field(0)
			if got := IsRequired(field); got != written {
				t.Errorf("IsRequired = %v, and encoding/json writes the zero value = %v (%s)", got, written, raw)
			}
			if got := FieldRequiredness(field) == FieldRequired; got != written {
				t.Errorf("FieldRequiredness says required = %v, and encoding/json writes the zero value = %v", got, written)
			}

			members := jsonMembers(structType)
			if len(members) != 2 || members[0].field.Name != "Field" {
				t.Fatalf("the members are %v", jsonMemberNames(members))
			}
			if got := answerMemberIsOptional(members[0]); got == written {
				t.Errorf("the answer reader holds the member optional = %v, and encoding/json writes the zero value = %v", got, written)
			}
			if got := !typePlanFor(structType).answerShape().members[0].required; got == written {
				t.Errorf("the plan holds the member optional = %v, and encoding/json writes the zero value = %v", got, written)
			}
		})
	}
}

// omitzero leaves out the zero value of any type, a struct included, where
// omitempty never leaves a struct out. A member marked omitzero is optional to
// the validator, the prose schema and the JSON schema as it is to the reader.
func TestAnOmitzeroMemberIsOptionalEverywhere(t *testing.T) {
	type span struct {
		From int `json:"from"`
	}
	type record struct {
		Name string `json:"name"`
		Span span   `json:"span,omitzero"`
		When int    `json:"when,omitzero:x"`
	}
	structType := reflect.TypeFor[record]()

	raw, err := json.Marshal(record{Name: "n"})
	if err != nil || string(raw) != `{"name":"n"}` {
		t.Fatalf("encoding/json writes %s, %v for a record with only a name", raw, err)
	}
	for _, name := range []string{"Span", "When"} {
		field, _ := structType.FieldByName(name)
		if IsRequired(field) {
			t.Errorf("%s is marked omitzero and IsRequired says it is required", name)
		}
	}
	if err := ValidateExtractedData(record{Name: "n"}); err != nil {
		t.Errorf("the validator holds an omitzero member to be filled: %v", err)
	}
	// The line of a struct member that is itself a struct ends with the member's
	// marker, after its own members.
	for _, line := range strings.Split(GenerateTypeSchema(structType), "\n") {
		if (strings.HasPrefix(line, "  when:") || strings.HasPrefix(line, "  } ")) && strings.Contains(line, "(required)") {
			t.Errorf("the prose schema marks an omitzero member required: %s", line)
		}
	}
	properties, _ := GenerateJSONSchema(structType)["properties"].(map[string]any)
	if kinds, _ := properties["when"].(map[string]any)["type"].([]string); !slices.Contains(kinds, "null") {
		t.Errorf("the JSON schema does not let an omitzero member be null: %v", properties["when"])
	}
}

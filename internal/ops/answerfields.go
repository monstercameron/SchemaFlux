package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Reading a model's answer one field at a time.
//
// An answer that is only checked for carrying any one of the fields it was asked
// for, and decoded into a plain struct, reads a field that is missing, null or of
// another kind as the Go zero value. An operation that reads its
// answer that way cannot tell "the model said 0" from "the model said nothing",
// and returns the second as if it were the first.
//
// answerObject keeps every member of the answer as raw JSON, so that each one
// is looked up by name and refused when it is not there, is null, or is not of
// the kind the operation asked for. The messages name the member by the key the
// prompt gave it and repeat nothing the model wrote.

// answerObject is the members of a model's JSON answer.
type answerObject struct {
	op      string
	members map[string]json.RawMessage
}

// readAnswerObject reads the response as one JSON object. A response that is
// not one is refused as malformed output.
func readAnswerObject(op, response string) (answerObject, error) {
	var members map[string]json.RawMessage
	if err := ParseJSON(response, &members); err != nil {
		var classified *types.OperationError
		if errors.As(err, &classified) {
			// ParseJSON describes the shape of the response and nothing in it.
			return answerObject{}, types.Newf(classified.Kind, op, "%s", classified.Message)
		}
		return answerObject{}, types.Newf(types.KindMalformedOutput, op, "the response is not the JSON object that was asked for")
	}
	if members == nil {
		return answerObject{}, types.Newf(types.KindMalformedOutput, op, "the response is not the JSON object that was asked for")
	}
	return answerObject{op: op, members: members}, nil
}

// refuse is the error for an answer that does not have the shape that was asked
// for.
func (a answerObject) refuse(format string, args ...any) error {
	return types.Newf(types.KindSchemaViolation, a.op, format, args...)
}

// present reports whether the member is there and is not null.
func (a answerObject) present(name string) bool {
	raw, found := a.members[name]
	return found && !isJSONNull(raw)
}

func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// raw returns the member, refusing it when it is missing or null.
func (a answerObject) raw(name string) (json.RawMessage, error) {
	if !a.present(name) {
		return nil, a.refuse("the answer has no %q", name)
	}
	return a.members[name], nil
}

// decode reads a member that has to be there into target.
func (a answerObject) decode(name string, target any) error {
	raw, err := a.raw(name)
	if err != nil {
		return err
	}
	return a.decodeRaw(name, raw, target)
}

// decodeOptional reads a member into target when it is there. A member that is
// missing or null leaves target alone; one that is there and of the wrong kind
// is refused.
func (a answerObject) decodeOptional(name string, target any) error {
	if !a.present(name) {
		return nil
	}
	return a.decodeRaw(name, a.members[name], target)
}

func (a answerObject) decodeRaw(name string, raw json.RawMessage, target any) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return a.refuse("%q is not of the kind that was asked for", name)
	}
	return nil
}

// number reads a member that has to be a JSON number. A string that holds a
// number is not one, and neither is 1e400: encoding/json refuses a literal that
// float64 cannot hold, and JSON has no spelling for NaN or an infinity, so what
// this returns is always finite.
func (a answerObject) number(name string) (float64, error) {
	var value float64
	if err := a.decode(name, &value); err != nil {
		return 0, err
	}
	return value, nil
}

// fraction reads a member that has to be a number from 0 to 1.
func (a answerObject) fraction(name string) (float64, error) {
	value, err := a.number(name)
	if err != nil {
		return 0, err
	}
	if value < 0 || value > 1 {
		return 0, a.refuse("%q is outside the range 0 to 1", name)
	}
	return value, nil
}

// text reads a member that has to be a JSON string.
func (a answerObject) text(name string) (string, error) {
	var value string
	if err := a.decode(name, &value); err != nil {
		return "", err
	}
	return value, nil
}

// flag reads a member that has to be a JSON true or false.
func (a answerObject) flag(name string) (bool, error) {
	var value bool
	if err := a.decode(name, &value); err != nil {
		return false, err
	}
	return value, nil
}

// elements reads a member that has to be a JSON array and returns its elements
// raw. An empty array is an answer; a missing or null member is not.
func (a answerObject) elements(name string) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if err := a.decode(name, &values); err != nil {
		return nil, err
	}
	// "[]" decodes to an empty slice that is not nil, and a null never gets this
	// far: decode refuses it as a missing member.
	return values, nil
}

// wholeNumberOf reads a raw JSON number that is a whole number: 2 and 2.0 are
// both 2, 2.5 and "2" are not. ok is false for anything else, and for a number
// beyond what an int can count exactly.
func wholeNumberOf(raw json.RawMessage) (int, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, false
	}
	if first := trimmed[0]; first != '-' && (first < '0' || first > '9') {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return 0, false
	}
	const exactlyCountable = 1 << 53
	if value != math.Trunc(value) || math.Abs(value) >= exactlyCountable {
		return 0, false
	}
	return int(value), true
}

// fractionOf reads a raw JSON number from 0 to 1.
func fractionOf(raw json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, false
	}
	if first := trimmed[0]; first != '-' && (first < '0' || first > '9') {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return 0, false
	}
	if math.IsNaN(value) || value < 0 || value > 1 {
		return 0, false
	}
	return value, true
}

// wholeNumbers reads a member that has to be an array of whole numbers.
func (a answerObject) wholeNumbers(name string) ([]int, error) {
	elements, err := a.elements(name)
	if err != nil {
		return nil, err
	}
	numbers := make([]int, len(elements))
	for i, element := range elements {
		number, ok := wholeNumberOf(element)
		if !ok {
			return nil, a.refuse("entry %d of %q is not a whole number", i, name)
		}
		numbers[i] = number
	}
	return numbers, nil
}

// quotedList writes caller text as a JSON array for a prompt, so that a value
// that contains a newline or a heading reads as one entry and not as the
// library's own instructions.
func quotedList(values []string) string {
	encoded, err := promptJSON(values)
	if err != nil {
		// A []string always encodes; this is here so the failure is not read as
		// an empty list.
		return "[]"
	}
	return string(encoded)
}

// quotedText writes one piece of caller text as a JSON string for a prompt.
func quotedText(value string) string {
	encoded, err := promptJSON(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// requiredJSONFields lists the names a JSON object of the given Go type has to
// carry: the members encoding/json reads into the type (jsonMembers) that are not
// marked omitempty or omitzero, in the order the type declares them. A member of
// an embedded struct counts as its parent's. A field whose Go type can hold nil
// is listed in nullable, because a JSON null is an answer for it.
func requiredJSONFields(t reflect.Type) (required []string, nullable map[string]bool) {
	nullable = map[string]bool{}
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return required, nullable
	}
	for _, member := range jsonMembers(t) {
		if !answerMemberIsReadable(member) || answerMemberIsOptional(member) {
			continue
		}
		required = append(required, member.name)
		if nilable(member.field.Type) {
			nullable[member.name] = true
		}
	}
	return required, nullable
}

// answerMemberIsReadable reports whether an answer can fill the member. One
// promoted through an embedded pointer to a struct type that is not exported
// cannot be: encoding/json cannot allocate the pointer, so an answer that
// carries the member is an error at decode. The reader does not require it.
func answerMemberIsReadable(member jsonMember) bool {
	return member.unexportedPointer == nil
}

// answerMemberIsOptional reports whether the type marks the member omitempty or
// omitzero, which is how a type says the model may leave it out. The options are
// read as encoding/json reads them (parseJSONTagOptions), so a tag it takes for
// omitempty is one here and one it does not take is not.
func answerMemberIsOptional(member jsonMember) bool {
	return parseJSONTagOptions(member.field.Tag.Get("json")).leavesOutZero()
}

// Holding an answer to the type it is read into.
//
// decodeAnswer reads the response into the operation's own answer type and
// refuses an answer that leaves out anything the type requires, where the reader
// it replaced accepted an answer that carried any one of the type's field names
// and left every other member the Go zero value, so an operation could not tell
// "the model said 0" from "the model said nothing".
//
// What a type requires, at every depth it describes:
//
//   - A field with a json name that is not marked omitempty or omitzero has to
//     be in the answer. A field of an embedded struct counts as its parent's.
//   - A field whose Go type can hold nil (a pointer, slice, map or interface)
//     may be null. Any other required field may not.
//   - The elements of a list or array, and the values of a map, are held to the
//     same rule: a struct element has to carry its required members, and an
//     element that is null has to be of a type that can hold nil.
//   - A member the type does not declare is ignored, as encoding/json ignores
//     it, and is never named in an error.
//   - An optional member may be left out or be null. When it is there and not
//     null, what is inside it is held to the type as well.
//   - A type that reads itself (json.Unmarshaler, encoding.TextUnmarshaler) is
//     taken as one value: the answer has to carry it, and what it accepts is its
//     own decision.
//
// A key that occurs twice is read as encoding/json reads it, the last one
// wins, and each occurrence is held to the rule, because the decoder merges
// objects under a repeated key and a complete first occurrence would otherwise
// hide an incomplete last one.

// answerKind is what a position in an answer is read as.
type answerKind uint8

const (
	// answerOpaque is a scalar, an interface, or a type that reads itself:
	// there is nothing inside it to look at.
	answerOpaque answerKind = iota
	// answerStruct is a JSON object read into a struct.
	answerStruct
	// answerList is a JSON array read into a slice or an array.
	answerList
	// answerMap is a JSON object read into a map.
	answerMap
)

// expects says what JSON the plan was made for, for a message.
func (p *answerPlan) expects() string {
	if p.kind == answerList {
		return "an array"
	}
	return "an object"
}

// answerMember is one member of a struct that the answer may carry.
type answerMember struct {
	// name is what the member is called in the answer, which is the library's
	// own word for it and so what an error may say.
	name string
	// plan is what is inside the member, or nil when there is nothing to look at.
	plan *answerPlan
	// required is false for a member marked omitempty or omitzero.
	required bool
	// nullable is true when the Go type of the member can hold nil.
	nullable bool
	// slot is the member's place among the required ones, or -1.
	slot int
	// caseRule is how encoding/json matches a key to the member's name.
	caseRule jsonCaseRule
}

// answerPlan is what one type requires of an answer. It is made once for a
// type (typePlan.answerShape) and only read afterwards, so the walk over an
// answer allocates nothing.
type answerPlan struct {
	kind answerKind

	// members are the struct's members in the order the type declares them,
	// looked up by exact name and then ignoring case, the two ways
	// encoding/json finds the member a key belongs to. A member that says
	// case:strict is found by its exact name only.
	members []answerMember
	exact   map[string]int
	// folded lists, for each folded name, the members that have it in the order
	// the type declares them.
	folded map[string][]int
	// required is how many of the members are required.
	required int

	// elem is what is inside a list or a map value, or nil.
	elem *answerPlan
	// elemNullable is true when the Go type of an element can hold nil.
	elemNullable bool

	// scan is false when nothing inside the value can be missing or null, so
	// the walk skips it.
	scan bool
}

// foldAnswerName appends the name as encoding/json folds it, so two names that
// differ only in case, or in the other forms of a letter that Unicode folds
// together, fold to the same bytes.
func foldAnswerName(out []byte, name string) []byte {
	for i := 0; i < len(name); {
		if c := name[i]; c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			out = append(out, c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(name[i:])
		for {
			folded := unicode.SimpleFold(r)
			if folded <= r {
				r = folded
				break
			}
			r = folded
		}
		out = utf8.AppendRune(out, r)
		i += size
	}
	return out
}

// lookup is the member a key belongs to, or -1, as encoding/json finds it: the
// member of that exact name, else the first member whose name is the key
// ignoring case and which does not say case:strict. A key that matches a strict
// member only in another case belongs to none, and the decoder leaves the
// member empty.
func (p *answerPlan) lookup(key string) int {
	if index, found := p.exact[key]; found {
		return index
	}
	var space [48]byte
	for _, index := range p.folded[string(foldAnswerName(space[:0], key))] {
		if p.members[index].caseRule != jsonCaseStrict {
			return index
		}
	}
	return -1
}

// nilable reports whether a value of the type can be nil, which is what lets a
// JSON null be an answer for it.
func nilable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return true
	}
	return false
}

// readsItself reports whether the type, or what a pointer to it reaches, has its
// own way of reading JSON or text.
func readsItself(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return t.Implements(jsonUnmarshalerType) || t.Implements(textUnmarshalerType)
	}
	return decodesItself(t)
}

// answerPlanBuilder makes the plans of a type and of every type inside it. A
// type that contains itself is one plan that points back at itself.
type answerPlanBuilder struct {
	built map[reflect.Type]*answerPlan
	all   []*answerPlan
}

// of is the plan of a type, or nil when there is nothing inside it to look at.
func (b *answerPlanBuilder) of(t reflect.Type) *answerPlan {
	for t.Kind() == reflect.Pointer {
		if readsItself(t) {
			return nil
		}
		t = t.Elem()
	}
	if readsItself(t) {
		return nil
	}
	if known := b.built[t]; known != nil {
		return known
	}

	var plan *answerPlan
	switch t.Kind() {
	case reflect.Struct:
		plan = &answerPlan{kind: answerStruct, exact: map[string]int{}, folded: map[string][]int{}}
		b.register(t, plan)
		for _, field := range jsonMembers(t) {
			if !answerMemberIsReadable(field) {
				continue
			}
			options := parseJSONTagOptions(field.field.Tag.Get("json"))
			member := answerMember{
				name:     field.name,
				plan:     b.of(field.field.Type),
				required: !options.leavesOutZero(),
				nullable: nilable(field.field.Type),
				slot:     -1,
				caseRule: options.caseRule,
			}
			if member.required {
				member.slot = plan.required
				plan.required++
			}
			plan.exact[field.name] = len(plan.members)
			// The first member whose folded name matches, and which does not say
			// case:strict, is the one a key of another case belongs to.
			folded := string(foldAnswerName(nil, field.name))
			plan.folded[folded] = append(plan.folded[folded], len(plan.members))
			plan.members = append(plan.members, member)
		}
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			// Bytes are a base64 string.
			return nil
		}
		plan = &answerPlan{kind: answerList, elemNullable: nilable(t.Elem())}
		b.register(t, plan)
		plan.elem = b.of(t.Elem())
	case reflect.Map:
		plan = &answerPlan{kind: answerMap, elemNullable: nilable(t.Elem())}
		b.register(t, plan)
		plan.elem = b.of(t.Elem())
	}
	return plan
}

func (b *answerPlanBuilder) register(t reflect.Type, plan *answerPlan) {
	b.built[t] = plan
	b.all = append(b.all, plan)
}

// needsScan reports whether a walk over a value of the plan can find anything.
func (p *answerPlan) needsScan() bool {
	if p.kind == answerStruct {
		if p.required > 0 {
			return true
		}
		for i := range p.members {
			if inner := p.members[i].plan; inner != nil && inner.scan {
				return true
			}
		}
		return false
	}
	return !p.elemNullable || (p.elem != nil && p.elem.scan)
}

// computeAnswerShape makes the plan of a type, which typePlan keeps.
func computeAnswerShape(plan *typePlan) *answerPlan {
	if plan.typ == nil {
		return &answerPlan{}
	}
	builder := answerPlanBuilder{built: map[reflect.Type]*answerPlan{}}
	root := builder.of(plan.typ)
	if root == nil {
		return &answerPlan{}
	}
	// A type that contains itself is a cycle of plans, so whether a plan has
	// anything to find is worked out by repeating until nothing changes.
	for changed := true; changed; {
		changed = false
		for _, each := range builder.all {
			if !each.scan && each.needsScan() {
				each.scan = true
				changed = true
			}
		}
	}
	return root
}

// decodeAnswer reads a model's answer into target, which is a pointer to the
// operation's own answer type, and holds the answer to that type (see above).
// It cleans the response as ParseJSON does and decodes it once with
// encoding/json; the check for what is missing is then one walk over the text,
// against a plan that was made once for the type, and allocates nothing.
//
// A refusal is a classified error whose message names the member by its path in
// the library's own words: "clusters[2].label is missing". A position in a list
// is [n], and one in a map is {n}, counted from zero in the order the response
// gives them, because the key is the model's text. Nothing the model wrote is in
// the message: not a key, not a value, not a fragment, and the decoder's own
// error, which can quote one, is not attached. The target is left as its zero
// value beside every refusal.
//
// What ParseJSON gave a caller, this gives as well, because it is the same
// decode of the same text: the tolerance for code fences and surrounding prose,
// the last of a repeated key, a key matched ignoring case (except to a member
// whose tag says case:strict, which only its exact name matches, and which is
// missing when the answer spells it any other way), an int that cannot hold a
// fraction, a number beyond float64 refused, the nesting limit of encoding/json,
// and no limit on size.
func decodeAnswer(op string, response string, target any) error {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return types.Newf(types.KindConfiguration, op, "an answer is read into a non-nil pointer, and was given a %T", target)
	}
	plan := typePlanFor(value.Type()).answerShape()
	body := cleanJSON(response)

	if err := json.Unmarshal([]byte(body), target); err != nil {
		value.Elem().SetZero()
		return answerReadFailure(op, body, plan, err)
	}
	// encoding/json reads a null into a struct, a slice or a map as nothing.
	if plan.kind != answerOpaque && body == "null" {
		value.Elem().SetZero()
		return types.Newf(types.KindMalformedOutput, op, "the response is null, and %s was asked for", plan.expects())
	}
	if plan.scan {
		scan := answerScan{s: body}
		if !scan.value(plan) {
			value.Elem().SetZero()
			return scan.refusal(op)
		}
	}
	return nil
}

// decodeCallerAnswer reads a model's answer into target, which is a pointer to a
// type the CALLER named (the result of Extract, Transform, Generate or Parse, or
// the payload of Compress, Decompose, Merge, Suggest and the like). It cleans the
// response and decodes it once exactly as decodeAnswer does, and refuses what
// decodeAnswer refuses at the top: text that is not JSON, JSON of another kind
// than the type reads, and a null. The target is left as its zero value beside
// every refusal, and no message holds anything the model wrote.
//
// What differs is presence. The caller's type is not the library's to tighten: a
// field of it that the answer leaves out is the Go zero value, and what a field
// has to carry is the business of the operation's own options (CompleteFields,
// ExactFields, strict mode), which are checked after this read. The one rule
// kept from the reader this replaced is that an answer about a struct has to be about it:
// when the target is a struct, or a pointer to one, the answer has to share at
// least one member with the type, matched as encoding/json matches a key. An
// object that shares none, an empty one included, answers nothing and is
// refused. A target with no members to match (a slice, a map, a scalar, an
// interface, a struct that has no JSON members, a type that reads itself) has no
// presence rule.
//
// The check is the same walk over the text that decodeAnswer makes, stopped at
// the first key that belongs to the type, so it allocates nothing and the read
// costs the decode alone.
func decodeCallerAnswer(op string, response string, target any) error {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return types.Newf(types.KindConfiguration, op, "an answer is read into a non-nil pointer, and was given a %T", target)
	}
	plan := typePlanFor(value.Type()).answerShape()
	body := cleanJSON(response)

	if err := json.Unmarshal([]byte(body), target); err != nil {
		value.Elem().SetZero()
		return callerReadFailure(op, body, plan, value.Type().Elem(), err)
	}
	// encoding/json reads a null into a struct, a slice, a map or a pointer as
	// nothing, and into a scalar as its zero value. Neither is an answer.
	if body == "null" {
		value.Elem().SetZero()
		return types.Newf(types.KindMalformedOutput, op, "the response is null, which answers nothing")
	}
	if plan.kind == answerStruct && len(plan.members) > 0 {
		scan := answerScan{s: body}
		if keys, shares := scan.sharesMember(plan); !shares {
			value.Elem().SetZero()
			return callerUnrecognised(op, plan, keys)
		}
	}
	return nil
}

// callerReadFailure is answerReadFailure for a caller's type. A response of
// another kind than the type reads is said to be one that could not be parsed,
// which is what the callers of the reader it replaces were told, and it covers a
// scalar or an interface as well, whose mismatch has no member to name.
func callerReadFailure(op, body string, plan *answerPlan, target reflect.Type, err error) error {
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) && mismatch.Err == nil {
		if plan.kind != answerOpaque && !plan.opens(body) {
			return types.Newf(types.KindMalformedOutput, op, "the response could not be parsed: it is %s, and %s was asked for", answerJSONKind(body), plan.expects())
		}
		if plan.kind == answerOpaque && mismatch.Field == "" {
			return types.Newf(types.KindMalformedOutput, op, "the response could not be parsed: it is %s, and %s was asked for", answerJSONKind(body), answerWanted(target))
		}
	}
	return answerReadFailure(op, body, plan, err)
}

// callerUnrecognised is the refusal for an object that shares no member with the
// caller's struct. The names wanted are the type's own, in the library's words;
// the keys the model sent are its text, so they are counted and not listed.
func callerUnrecognised(op string, plan *answerPlan, keys int) error {
	wanted := make([]string, 0, len(plan.members))
	for i := range plan.members {
		wanted = append(wanted, plan.members[i].name)
	}
	sort.Strings(wanted)
	if keys == 0 {
		return types.Newf(types.KindSchemaViolation, op, "the response was an empty object, which answers nothing (wanted at least one of %v)", wanted)
	}
	return types.Newf(types.KindSchemaViolation, op, "the response has %d member(s), none of them one the type asked for (wanted at least one of %v)", keys, wanted)
}

// sharesMember walks the members of the JSON object at the start of the text and
// reports whether any key belongs to the plan's struct, and how many keys it
// counted before stopping. A key that does ends the walk, so keys is only the
// whole count for an object that shares none.
func (sc *answerScan) sharesMember(p *answerPlan) (keys int, shares bool) {
	sc.skipSpace()
	if sc.i >= len(sc.s) || sc.s[sc.i] != '{' {
		// The decode has accepted the text against a struct, so this is not reached.
		return 0, true
	}
	sc.i++
	for {
		sc.skipSpace()
		if sc.i >= len(sc.s) {
			return keys, false
		}
		if c := sc.s[sc.i]; c == '}' {
			return keys, false
		} else if c == ',' {
			sc.i++
			continue
		}
		keys++
		if sc.memberOf(p) >= 0 {
			return keys, true
		}
		sc.skipSpace()
		sc.i++ // the colon
		sc.skipSpace()
		sc.skipValue()
	}
}

// answerReadFailure turns an error of the decode into a refusal. The decoder's
// error is not attached: a syntax error quotes the character it stopped at, and
// an error from a type's own UnmarshalJSON can quote the whole value.
func answerReadFailure(op, body string, plan *answerPlan, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return types.Newf(types.KindMalformedOutput, op, "the response could not be parsed as JSON (%d bytes, shape: %s)", len(body), describeShape(body))
	}
	var mismatch *json.UnmarshalTypeError
	// An error that carries a reason of its own (a member the decoder cannot
	// allocate, say) is not a value of the wrong kind, and is reported as the
	// decode failing.
	if errors.As(err, &mismatch) && mismatch.Err == nil {
		if plan.kind != answerOpaque && !plan.opens(body) {
			return types.Newf(types.KindMalformedOutput, op, "the response is %s, and %s was asked for", answerJSONKind(body), plan.expects())
		}
		// Field is the path to the value, and it does not say which element of a
		// list the member was in. It is made of the type's own json names, except
		// that the key of a map entry is the model's text, so the path is kept as
		// far as the plan can vouch for it.
		path, whole := plan.knownPath(mismatch.Field)
		switch {
		case path != "" && whole:
			return types.Newf(types.KindSchemaViolation, op, "%s is not %s", path, answerWanted(mismatch.Type))
		case path != "":
			return types.Newf(types.KindSchemaViolation, op, "an entry of %s is not %s", path, answerWanted(mismatch.Type))
		case !whole:
			return types.Newf(types.KindSchemaViolation, op, "an entry of the answer is not %s", answerWanted(mismatch.Type))
		}
		return types.Newf(types.KindSchemaViolation, op, "a member of the answer is not %s", answerWanted(mismatch.Type))
	}
	return types.Newf(types.KindMalformedOutput, op, "the response could not be read into the type that was asked for (%d bytes, shape: %s)", len(body), describeShape(body))
}

// knownPath is the part of a decoder's field path (the json names of the members
// from the root, with no position for a list element) that is made of the type's
// own names. It stops at the first segment that is a map key, which is the
// model's text and is never repeated, and whole says it did not have to stop.
func (p *answerPlan) knownPath(field string) (path string, whole bool) {
	if field == "" {
		return "", true
	}
	var known strings.Builder
	current := p
	for rest := field; rest != ""; {
		var segment string
		segment, rest, _ = strings.Cut(rest, ".")
		// A decoder that counts the positions of a list in the path writes one as
		// a number between the names. A position is not named in a message.
		position := false
		for current != nil && current.kind == answerList {
			current = current.elem
			if isDecimal(segment) {
				position = true
				break
			}
		}
		if position {
			continue
		}
		if current == nil || current.kind != answerStruct {
			return known.String(), false
		}
		index, found := current.exact[segment]
		if !found {
			return known.String(), false
		}
		if known.Len() > 0 {
			known.WriteByte('.')
		}
		known.WriteString(segment)
		current = current.members[index].plan
	}
	return known.String(), true
}

// isDecimal reports whether the text is one or more digits.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// opens reports whether the text begins as the plan's kind of value does.
func (p *answerPlan) opens(body string) bool {
	if body == "" {
		return false
	}
	if p.kind == answerList {
		return body[0] == '['
	}
	return body[0] == '{'
}

// answerJSONKind names what kind of JSON a text is, from its first character,
// for a message. It is only asked of text that parsed.
func answerJSONKind(body string) string {
	if body == "" {
		return "empty"
	}
	switch c := body[0]; {
	case c == '{':
		return "an object"
	case c == '[':
		return "an array"
	case c == '"':
		return "a string"
	case c == 'n':
		return "null"
	case c == 't' || c == 'f':
		return "true or false"
	default:
		return "a number"
	}
}

// answerWanted says what a Go type reads, for a message.
func answerWanted(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "of the kind that was asked for"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return "a whole number the field can hold"
	case reflect.Float32, reflect.Float64:
		return "a number the field can hold"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	}
	return "of the kind that was asked for"
}

// answerProblem is what a walk over an answer found.
type answerProblem uint8

const (
	answerFine answerProblem = iota
	// answerMissing is a required member that is not there.
	answerMissing
	// answerNull is a null where the Go type cannot hold nil.
	answerNull
)

// answerStep is one step of the path to a problem.
type answerStep struct {
	// kind is 'm' for a member of a struct, 'i' for the position of a list
	// element and 'e' for the position of a map entry.
	kind byte
	name string
	n    int
}

// answerScan walks the text of an answer that encoding/json has already
// accepted, against a plan. The text is known to be well formed, so the walk
// does not check syntax. It allocates only when it finds a problem, and for a
// key written with an escape or in another encoding, which it hands to the
// decoder to read.
type answerScan struct {
	s string
	i int

	problem answerProblem
	// more is how many other required members of the same object are missing.
	more int
	// steps is the path to the problem, innermost first.
	steps []answerStep
}

func (sc *answerScan) skipSpace() {
	for sc.i < len(sc.s) {
		switch sc.s[sc.i] {
		case ' ', '\t', '\r', '\n':
			sc.i++
		default:
			return
		}
	}
}

// skipValue moves past the value that begins at the position.
func (sc *answerScan) skipValue() {
	s := sc.s
	i := sc.i
	if i >= len(s) {
		return
	}
	switch s[i] {
	case '"':
		end, _ := endOfScannedString(s, i)
		sc.i = end + 1
	case '{', '[':
		depth := 0
		for ; i < len(s); i++ {
			switch s[i] {
			case '"':
				end, _ := endOfScannedString(s, i)
				i = end
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					sc.i = i + 1
					return
				}
			}
		}
		sc.i = len(s)
	default:
		for i < len(s) && !isScannedDelimiter(s[i]) {
			i++
		}
		sc.i = i
	}
}

// value walks the value at the position, which is not null, against a plan.
func (sc *answerScan) value(p *answerPlan) bool {
	sc.skipSpace()
	if sc.i >= len(sc.s) {
		return true
	}
	switch {
	case p.kind == answerStruct && sc.s[sc.i] == '{':
		return sc.object(p)
	case p.kind == answerMap && sc.s[sc.i] == '{':
		return sc.entries(p)
	case p.kind == answerList && sc.s[sc.i] == '[':
		return sc.list(p)
	}
	sc.skipValue()
	return true
}

// element walks a value that a plan describes, or skips it. A null is fine
// where nullOK says so, and a problem where it does not.
func (sc *answerScan) element(plan *answerPlan, nullOK bool) bool {
	if sc.i >= len(sc.s) {
		return true
	}
	if sc.s[sc.i] == 'n' {
		if !nullOK {
			sc.problem = answerNull
			return false
		}
		sc.i += len("null")
		return true
	}
	if plan == nil || !plan.scan {
		sc.skipValue()
		return true
	}
	return sc.value(plan)
}

// memberOf reads the key at the position and says which member of the plan it
// belongs to, or -1.
func (sc *answerScan) memberOf(p *answerPlan) int {
	start := sc.i + 1
	end, escaped := endOfScannedString(sc.s, sc.i)
	sc.i = end + 1
	if end > len(sc.s) {
		return -1
	}
	key := sc.s[start:end]
	if !escaped && isPlainASCII(key) {
		return p.lookup(key)
	}
	// An escape, or text that is not plain ASCII, is read as the decoder reads
	// it: unescaped, with a byte that is not UTF-8 read as U+FFFD.
	var decoded string
	if err := json.Unmarshal([]byte(sc.s[start-1:end+1]), &decoded); err != nil {
		return -1
	}
	return p.lookup(decoded)
}

func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// object walks a JSON object read into a struct.
func (sc *answerScan) object(p *answerPlan) bool {
	sc.i++
	var seenSpace [4]uint64
	seen := seenSpace[:]
	if p.required > len(seenSpace)*64 {
		seen = make([]uint64, (p.required+63)/64)
	}
	found := 0
	for {
		sc.skipSpace()
		if sc.i >= len(sc.s) {
			break
		}
		if c := sc.s[sc.i]; c == '}' {
			sc.i++
			break
		} else if c == ',' {
			sc.i++
			continue
		}
		index := sc.memberOf(p)
		sc.skipSpace()
		sc.i++ // the colon
		sc.skipSpace()
		if index < 0 {
			sc.skipValue()
			continue
		}
		member := &p.members[index]
		// An optional member that is null is the model leaving it out.
		if !sc.element(member.plan, member.nullable || !member.required) {
			sc.steps = append(sc.steps, answerStep{kind: 'm', name: member.name})
			return false
		}
		if member.slot >= 0 {
			word, bit := member.slot/64, uint64(1)<<(member.slot%64)
			if seen[word]&bit == 0 {
				seen[word] |= bit
				found++
			}
		}
	}
	if found == p.required {
		return true
	}

	first, missing := -1, 0
	for i := range p.members {
		slot := p.members[i].slot
		if slot < 0 || seen[slot/64]&(uint64(1)<<(slot%64)) != 0 {
			continue
		}
		if first < 0 {
			first = i
		}
		missing++
	}
	sc.problem = answerMissing
	sc.more = missing - 1
	sc.steps = append(sc.steps, answerStep{kind: 'm', name: p.members[first].name})
	return false
}

// list walks a JSON array read into a slice or an array.
func (sc *answerScan) list(p *answerPlan) bool {
	sc.i++
	for position := 0; ; {
		sc.skipSpace()
		if sc.i >= len(sc.s) {
			return true
		}
		if c := sc.s[sc.i]; c == ']' {
			sc.i++
			return true
		} else if c == ',' {
			sc.i++
			continue
		}
		if !sc.element(p.elem, p.elemNullable) {
			sc.steps = append(sc.steps, answerStep{kind: 'i', n: position})
			return false
		}
		position++
	}
}

// entries walks a JSON object read into a map.
func (sc *answerScan) entries(p *answerPlan) bool {
	sc.i++
	for position := 0; ; {
		sc.skipSpace()
		if sc.i >= len(sc.s) {
			return true
		}
		switch sc.s[sc.i] {
		case '}':
			sc.i++
			return true
		case ',':
			sc.i++
			continue
		}
		sc.skipValue() // the key, which is the model's text and is not kept
		sc.skipSpace()
		sc.i++ // the colon
		sc.skipSpace()
		if !sc.element(p.elem, p.elemNullable) {
			sc.steps = append(sc.steps, answerStep{kind: 'e', n: position})
			return false
		}
		position++
	}
}

// refusal is the error for what the walk found.
func (sc *answerScan) refusal(op string) error {
	var path strings.Builder
	for i := len(sc.steps) - 1; i >= 0; i-- {
		switch step := sc.steps[i]; step.kind {
		case 'm':
			if path.Len() > 0 {
				path.WriteByte('.')
			}
			path.WriteString(step.name)
		case 'i':
			fmt.Fprintf(&path, "[%d]", step.n)
		case 'e':
			fmt.Fprintf(&path, "{%d}", step.n)
		}
	}
	if sc.problem == answerNull {
		return types.Newf(types.KindSchemaViolation, op, "%s is null, and null is not an answer for it", path.String())
	}
	switch sc.more {
	case 0:
		return types.Newf(types.KindSchemaViolation, op, "%s is missing", path.String())
	case 1:
		return types.Newf(types.KindSchemaViolation, op, "%s is missing, and so is 1 other required member of the same object", path.String())
	}
	return types.Newf(types.KindSchemaViolation, op, "%s is missing, and so are %d other required members of the same object", path.String(), sc.more)
}

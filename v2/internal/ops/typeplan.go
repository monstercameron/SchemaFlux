package ops

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// What an operation needs to know about a Go type, worked out once.
//
// An operation that takes a type parameter -- Extract, Transform, Generate --
// used to work out, on every call, how well the type can serve as a contract,
// the schema the model is shown, the JSON schema the provider enforces (twice:
// once for the request and once more to hash it for the schema identity), the
// identity itself, the cache key and the system prompt. All of that depends on
// the Go type and on nothing the caller sends, and it was most of what an
// Extract allocated, more of it the more fields the type has.
//
// A typePlan holds those results for one type. Each is computed the first time
// somebody asks for it, so a type that is only described in a prompt never pays
// for a hash it does not use, and each is computed once however many goroutines
// ask at the same time.
//
// What a plan is keyed on. The generators take a reflect.Type and nothing else:
// no option changes the JSON schema, the prose schema, the identity or the
// classification, which is why strict mode against transform mode is not a key
// of the plan. The one thing an option does change is the wording of Extract's
// system prompt, which is written in four forms by mode, and those four are
// held side by side in the plan, not as four plans.

// typePlanLimit is how many types the cache holds. A reflect.Type is not
// something a caller's data can mint -- there is one for each type the program
// was written with -- but reflect.StructOf and reflect.ArrayOf make types at run
// time, and a program that did so in a loop would add an entry for each. Types
// past the limit get a plan of their own for the one call, which is the work the
// call did before there was a cache.
const typePlanLimit = 1024

// typePlans is the cache every operation reads.
var typePlans = newTypePlanCache(typePlanLimit)

// typePlanFor is the plan for a type, shared by every call that asks for the
// same type.
func typePlanFor(targetType reflect.Type) *typePlan {
	return typePlans.plan(targetType)
}

// typePlanCache keeps one plan for each type, up to a limit.
//
// A read takes no lock: it is a lookup in a sync.Map. Adding a type takes a
// mutex, and under it looks the type up again before it stores anything, so
// goroutines that meet a new type together are all given the one plan that is
// stored, and the count of plans never passes the limit. Counting a place before
// the plan was stored let a goroutine that arrived while the count stood at the
// limit be given a plan of its own for a type that was being stored by another.
//
// The mutex is taken for a type the cache does not hold yet and not otherwise.
// Once the cache is full no type is added, so a type past the limit is turned
// away by two lookups and the count, without the mutex.
type typePlanCache struct {
	limit   int64
	mu      sync.Mutex
	count   atomic.Int64 // written under mu, read by size and by the full-cache check
	entries sync.Map     // reflect.Type -> *typePlan
}

func newTypePlanCache(limit int) *typePlanCache {
	return &typePlanCache{limit: int64(limit)}
}

// plan is the plan for a type. A nil type, which is what a nil interface has,
// and a type past the limit get a plan that is not kept.
func (cache *typePlanCache) plan(targetType reflect.Type) *typePlan {
	if targetType == nil {
		return &typePlan{}
	}
	if kept, found := cache.entries.Load(targetType); found {
		return kept.(*typePlan)
	}
	if cache.count.Load() >= cache.limit {
		// The count never goes down, so once it stands at the limit nothing is
		// added after this read. A type another goroutine stored between the
		// lookup above and this check is therefore visible to the lookup below,
		// and one that is not there now never will be.
		if kept, found := cache.entries.Load(targetType); found {
			return kept.(*typePlan)
		}
		return &typePlan{typ: targetType}
	}
	return cache.store(targetType)
}

// store adds the plan of a type the lookup in plan did not find, unless another
// goroutine has added it since or the cache has filled.
func (cache *typePlanCache) store(targetType reflect.Type) *typePlan {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if kept, found := cache.entries.Load(targetType); found {
		return kept.(*typePlan)
	}
	held := cache.count.Load()
	if held >= cache.limit {
		return &typePlan{typ: targetType}
	}
	fresh := &typePlan{typ: targetType, retained: true}
	cache.entries.Store(targetType, fresh)
	cache.count.Store(held + 1)
	return fresh
}

// size is how many plans are kept.
func (cache *typePlanCache) size() int {
	return int(cache.count.Load())
}

// lazyValue is a value worked out the first time it is read.
//
// sync.Once on its own would leave a value whose computation did not return, as
// its zero value for every later reader, which is a silent wrong answer where
// the call used to fail every time. There are two ways a computation does not
// return, and a reader after either of them gets a panic and never the zero
// value:
//
//   - It panicked. Every reader panics with a lazyFault that carries the value
//     of the original panic and the stack of the place it was raised, which is
//     lost to a plain re-raise because the stack that is then printed is the
//     reader's.
//   - It ended its goroutine with runtime.Goexit. That is not a panic, so
//     nothing can be recovered from it, and the computation did not finish. Every
//     reader after it panics with a lazyFault that says so.
//
// The goroutine that ran a computation which called runtime.Goexit goes on
// ending, as Goexit requires.
type lazyValue[V any] struct {
	once  sync.Once
	value V
	// fault is set when the computation did not return, and is read only after
	// the once has completed.
	fault *lazyFault
}

// lazyFault is what a reader panics with when the value was never worked out.
type lazyFault struct {
	// value is what the computation panicked with, or nil when it ended its
	// goroutine.
	value any
	// stack is where the computation panicked, or empty when it ended its
	// goroutine.
	stack []byte
}

func (fault *lazyFault) Error() string {
	if fault.stack == nil {
		return "the value of the type's plan was never worked out: the goroutine working it out ended without returning (runtime.Goexit)"
	}
	return fmt.Sprintf("the value of the type's plan could not be worked out: %v\n\nthe panic was raised at:\n%s", fault.value, fault.stack)
}

// Unwrap lets errors.Is and errors.As see an error the computation panicked with.
func (fault *lazyFault) Unwrap() error {
	if err, ok := fault.value.(error); ok {
		return err
	}
	return nil
}

// get returns the value, working it out with compute if nobody has. compute is a
// plain function of the plan, not a closure, so that reading costs no
// allocation.
func (lazy *lazyValue[V]) get(plan *typePlan, compute func(*typePlan) V) V {
	lazy.once.Do(func() {
		returned := false
		defer func() {
			if returned {
				return
			}
			fault := &lazyFault{}
			// recover is nil when the goroutine is ending in Goexit, which is the
			// fault with no value and no stack.
			if value := recover(); value != nil {
				fault.value, fault.stack = value, debug.Stack()
			}
			lazy.fault = fault
		}()
		lazy.value = compute(plan)
		returned = true
	})
	if lazy.fault != nil {
		panic(lazy.fault)
	}
	return lazy.value
}

// extractPromptKinds is how many wordings Extract's system prompt has by mode:
// Strict, Transform and Creative each add their own rules, and every other mode
// is the base prompt.
const extractPromptKinds = 4

// extractPromptKind says which of the wordings a mode gets.
func extractPromptKind(mode types.Mode) int {
	switch mode {
	case types.Strict:
		return 1
	case types.TransformMode:
		return 2
	case types.Creative:
		return 3
	default:
		return 0
	}
}

// extractPromptModes is a mode that gets each wording, in the order of
// extractPromptKind.
var extractPromptModes = [extractPromptKinds]types.Mode{types.ModeUnset, types.Strict, types.TransformMode, types.Creative}

// typePlan is what is known of one type. Nothing in it depends on a call, and
// nothing in it is changed after it is worked out.
type typePlan struct {
	typ reflect.Type

	// retained says the cache holds this plan, so that what is read from it is
	// shared with every other call. A plan that is not kept belongs to the call
	// that asked for it.
	retained bool

	report          lazyValue[TypeReport]
	prose           lazyValue[string]
	name            lazyValue[string]
	schema          lazyValue[map[string]any]
	descriptor      lazyValue[SchemaDescriptor]
	descriptorText  lazyValue[string]
	extractIdentity lazyValue[string]
	extractPrompts  [extractPromptKinds]lazyValue[string]
	required        lazyValue[*requiredPlan]
	answer          lazyValue[*answerPlan]
}

// classification is what the type support matrix says about the type.
func (plan *typePlan) classification() TypeReport {
	return plan.report.get(plan, computeClassification)
}

func computeClassification(plan *typePlan) TypeReport {
	return classifyType(plan.typ, map[reflect.Type]bool{}, nil, 0)
}

// preflight is the error PreflightType returns for the type, or nil. The error
// is made for each call and not kept, because a caller holds the one it is
// given.
func (plan *typePlan) preflight() error {
	return preflightFailure(plan.classification())
}

// proseSchema is the schema as the prompt shows it.
func (plan *typePlan) proseSchema() string {
	return plan.prose.get(plan, computeProseSchema)
}

func computeProseSchema(plan *typePlan) string {
	return describeType(plan.typ, map[reflect.Type]bool{}, 0)
}

// schemaName is the name the provider is given for the schema.
func (plan *typePlan) schemaName() string {
	return plan.name.get(plan, computeSchemaName)
}

func computeSchemaName(plan *typePlan) string {
	return schemaNameFor(plan.typ)
}

// sharedSchema is the JSON schema of the type, or nil when strict mode cannot
// express it. The map is the one every call with this type uses: it goes into
// the request as it is, and nothing may change it. GenerateJSONSchema, which a
// caller can change what it returns, hands out a copy.
func (plan *typePlan) sharedSchema() map[string]any {
	return plan.schema.get(plan, computeSharedSchema)
}

func computeSharedSchema(plan *typePlan) map[string]any {
	return jsonSchemaFor(plan.typ, map[reflect.Type]bool{}, 0)
}

// identity is what DescribeSchema reports for the type.
func (plan *typePlan) identity() SchemaDescriptor {
	return plan.descriptor.get(plan, computeIdentity)
}

func computeIdentity(plan *typePlan) SchemaDescriptor {
	descriptor := SchemaDescriptor{
		Name:    plan.schemaName(),
		Version: SchemaVersionDefault,
		Dialect: SchemaDialect,
	}

	schema := plan.sharedSchema()
	if schema == nil {
		// A type strict mode cannot express still has an identity, because a
		// prompt-only call still has a contract: the prose schema. Hashing that
		// keeps the two paths distinguishable rather than collapsing every
		// unexpressible type onto one hash.
		descriptor.Dialect = "prose"
		descriptor.Hash = digestOf(plan.proseSchema())
		return descriptor
	}

	descriptor.Hash = digestOf(canonicalJSON(schema))
	return descriptor
}

// identityText is the identity as a string, which is what the options carry.
func (plan *typePlan) identityText() string {
	return plan.descriptorText.get(plan, computeIdentityText)
}

func computeIdentityText(plan *typePlan) string {
	return plan.identity().String()
}

// extractCacheIdentity is the operation-and-schema half of the cache key of an
// Extract of this type.
func (plan *typePlan) extractCacheIdentity() string {
	return plan.extractIdentity.get(plan, computeExtractCacheIdentity)
}

func computeExtractCacheIdentity(plan *typePlan) string {
	return SchemaCacheKey("extract", extractPromptVersion, plan.identity())
}

// extractSystemPrompt is Extract's system prompt for the type in a mode.
func (plan *typePlan) extractSystemPrompt(mode types.Mode) string {
	kind := extractPromptKind(mode)
	return plan.extractPrompts[kind].get(plan, extractPromptWriters[kind])
}

// extractPromptWriters write the prompt of each wording. They are made once, so
// that reading a prompt allocates nothing.
var extractPromptWriters = func() [extractPromptKinds]func(*typePlan) string {
	var writers [extractPromptKinds]func(*typePlan) string
	for kind, mode := range extractPromptModes {
		writers[kind] = func(plan *typePlan) string {
			return BuildExtractSystemPrompt(plan.proseSchema(), mode)
		}
	}
	return writers
}()

// requiredFields is which fields of a struct type a Strict answer has to fill.
func (plan *typePlan) requiredFields() *requiredPlan {
	return plan.required.get(plan, computeRequiredFields)
}

func computeRequiredFields(plan *typePlan) *requiredPlan {
	return buildRequiredPlan(plan.typ)
}

// answerShape is what decodeAnswer holds an answer to when it is read into this
// type: which members it has to carry, at every depth, and which may be null.
// The plan is only to be read.
func (plan *typePlan) answerShape() *answerPlan {
	return plan.answer.get(plan, computeAnswerShape)
}

// requiredField is one field a Strict answer is checked for.
type requiredField struct {
	// index is the position of the field in each struct on the way to it from the
	// struct the plan is of: one position for a field of the struct itself, and
	// more for a field promoted from an embedded struct.
	index []int
	// name is what the field is called in the answer: its JSON name, or its Go
	// name when the tag gives none.
	name string
	// path is what the field is called in the message of a failure: the name, with
	// the Go names of the embedded structs it is promoted through before it. It is
	// the name for a field of the struct itself.
	path string
	// required is whether an empty value is a failure.
	required bool
}

// requiredPlan is the fields of one struct type that the check of a Strict
// answer looks at, in the order encoding/json writes them: the members it reads
// into the type (jsonMembers), which are the exported fields without those a
// json tag leaves out, the fields promoted from embedded structs in the place of
// the struct, and not the embedded structs themselves nor a name that two fields
// share at one depth. Resolving them reads the struct tags, which is what the
// check used to do for every field of every value it looked at.
type requiredPlan struct {
	fields []requiredField
}

func buildRequiredPlan(structType reflect.Type) *requiredPlan {
	members := jsonMembers(structType)
	plan := &requiredPlan{fields: make([]requiredField, 0, len(members))}
	for _, member := range members {
		path := member.name
		if len(member.route) > 0 {
			path = strings.Join(member.route, ".") + "." + member.name
		}
		plan.fields = append(plan.fields, requiredField{
			index: member.index, name: member.name, path: path, required: IsRequired(member.field),
		})
	}
	return plan
}

// in is the field's value in a value of the struct the plan is of, and whether
// there is one: a field promoted through an embedded pointer that is nil has
// none, and is as empty as a field can be.
func (field *requiredField) in(structValue reflect.Value) (reflect.Value, bool) {
	last := len(field.index) - 1
	current := structValue
	for _, position := range field.index[:last] {
		current = current.Field(position)
		if current.Kind() == reflect.Ptr {
			if current.IsNil() {
				return reflect.Value{}, false
			}
			current = current.Elem()
		}
	}
	return current.Field(field.index[last]), true
}

// cloneSchema copies a schema the generator wrote, at every level, so that the
// copy can be changed without the original seeing it. It knows the kinds of
// value the generator and the envelopes built from it hold.
func cloneSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	copied := make(map[string]any, len(schema))
	for key, value := range schema {
		copied[key] = cloneSchemaValue(value)
	}
	return copied
}

func cloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSchema(typed)
	case []any:
		if typed == nil {
			return typed
		}
		copied := make([]any, len(typed))
		for i, entry := range typed {
			copied[i] = cloneSchemaValue(entry)
		}
		return copied
	case []string:
		// A list that was nil stays nil, and one that was empty stays empty:
		// they are written differently, as null and as [].
		if typed == nil {
			return typed
		}
		copied := make([]string, len(typed))
		copy(copied, typed)
		return copied
	default:
		return value
	}
}

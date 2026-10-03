package ops

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Target types for the plan tests, one for each way a type can differ in what
// the generators say about it.

type typePlanNode struct {
	Name string        `json:"name"`
	Next *typePlanNode `json:"next"`
}

type typePlanEmbedded struct {
	benchAddress
	Extra string `json:"extra"`
}

type typePlanOpaque struct{ hidden string }

// hidden is never read: the type exists to have no field the schema can see.
var _ = typePlanOpaque{hidden: ""}

type typePlanWithAny struct {
	Name  string `json:"name"`
	Extra any    `json:"extra"`
}

type typePlanMoney struct {
	Price float32 `json:"price"`
}

type typePlanStamp struct {
	At   time.Time `json:"at"`
	Note *string   `json:"note,omitempty"`
	Tidy string    `json:"tidy,omitempty"`
	Skip string    `json:"-"`
	Tag  string    `schemaflux:"optional"`
}

type typePlanMarshaler struct {
	Value string `json:"value"`
}

func (typePlanMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

// typePlanTargets are the types the plan is compared with the generators on.
func typePlanTargets() map[string]reflect.Type {
	return map[string]reflect.Type{
		"flat":           reflect.TypeFor[benchFlat](),
		"nested":         reflect.TypeFor[benchNested](),
		"wide":           reflect.TypeFor[benchWide](),
		"pointer":        reflect.TypeFor[*benchFlat](),
		"slice":          reflect.TypeFor[[]benchLineItem](),
		"bytes":          reflect.TypeFor[[]byte](),
		"time":           reflect.TypeFor[time.Time](),
		"string":         reflect.TypeFor[string](),
		"integer":        reflect.TypeFor[int](),
		"map":            reflect.TypeFor[map[string]string](),
		"map of structs": reflect.TypeFor[map[string]benchFlat](),
		"recursive":      reflect.TypeFor[typePlanNode](),
		"embedded":       reflect.TypeFor[typePlanEmbedded](),
		"no exported":    reflect.TypeFor[typePlanOpaque](),
		"an any field":   reflect.TypeFor[typePlanWithAny](),
		"float32":        reflect.TypeFor[typePlanMoney](),
		"tags":           reflect.TypeFor[typePlanStamp](),
		"marshaler":      reflect.TypeFor[typePlanMarshaler](),
		"channel":        reflect.TypeFor[chan int](),
		"nil":            nil,
	}
}

// typePlanReferenceIdentity is DescribeSchema as it was written before the
// identity was kept, built from the generators and nothing else.
func typePlanReferenceIdentity(target reflect.Type) SchemaDescriptor {
	descriptor := SchemaDescriptor{Name: schemaNameFor(target), Version: SchemaVersionDefault, Dialect: SchemaDialect}
	schema := jsonSchemaFor(target, map[reflect.Type]bool{}, 0)
	if schema == nil {
		descriptor.Dialect = "prose"
		descriptor.Hash = digestOf(describeType(target, map[reflect.Type]bool{}, 0))
		return descriptor
	}
	descriptor.Hash = digestOf(canonicalJSON(schema))
	return descriptor
}

func TestTypePlanHoldsWhatTheGeneratorsGive(t *testing.T) {
	for name, target := range typePlanTargets() {
		t.Run(name, func(t *testing.T) {
			plan := newTypePlanCache(4).plan(target)

			if got, want := plan.classification(), classifyType(target, map[reflect.Type]bool{}, nil, 0); got != want {
				t.Errorf("classification = %+v, the matrix says %+v", got, want)
			}
			prose := describeType(target, map[reflect.Type]bool{}, 0)
			if got := plan.proseSchema(); got != prose {
				t.Errorf("prose schema = %q, want %q", got, prose)
			}
			if got, want := plan.schemaName(), schemaNameFor(target); got != want {
				t.Errorf("schema name = %q, want %q", got, want)
			}

			want := jsonSchemaFor(target, map[reflect.Type]bool{}, 0)
			got := plan.sharedSchema()
			if (got == nil) != (want == nil) || !reflect.DeepEqual(got, want) {
				t.Errorf("JSON schema = %v, want %v", got, want)
			}

			identity := typePlanReferenceIdentity(target)
			if got := plan.identity(); got != identity {
				t.Errorf("identity = %+v, want %+v", got, identity)
			}
			if got := plan.identityText(); got != identity.String() {
				t.Errorf("identity text = %q, want %q", got, identity.String())
			}
			if got, want := plan.extractCacheIdentity(), SchemaCacheKey("extract", extractPromptVersion, identity); got != want {
				t.Errorf("extract cache identity = %q, want %q", got, want)
			}

			if got, want := plan.preflight() == nil, classifyType(target, map[reflect.Type]bool{}, nil, 0).Support != SupportRejected; got != want {
				t.Errorf("preflight passes = %v, want %v", got, want)
			}
		})
	}
}

func TestTypePlanWritesTheExtractPromptOfEachMode(t *testing.T) {
	modes := []types.Mode{types.ModeUnset, types.Strict, types.TransformMode, types.Creative, types.Mode(99), types.Mode(-1)}
	for name, target := range typePlanTargets() {
		plan := newTypePlanCache(4).plan(target)
		prose := describeType(target, map[reflect.Type]bool{}, 0)
		for _, mode := range modes {
			if got, want := plan.extractSystemPrompt(mode), BuildExtractSystemPrompt(prose, mode); got != want {
				t.Errorf("%s, mode %v: the system prompt differs from BuildExtractSystemPrompt:\n%q\nwant\n%q", name, mode, got, want)
			}
		}
	}

	// The four wordings are four different texts, and an unknown mode gets the
	// base one, as BuildExtractSystemPrompt gives it.
	plan := newTypePlanCache(4).plan(reflect.TypeFor[benchFlat]())
	seen := map[string]types.Mode{}
	for _, mode := range []types.Mode{types.ModeUnset, types.Strict, types.TransformMode, types.Creative} {
		prompt := plan.extractSystemPrompt(mode)
		if other, repeated := seen[prompt]; repeated {
			t.Errorf("modes %v and %v share one system prompt", other, mode)
		}
		seen[prompt] = mode
	}
	if plan.extractSystemPrompt(types.Mode(99)) != plan.extractSystemPrompt(types.ModeUnset) {
		t.Error("an unknown mode did not get the base prompt")
	}
}

func TestTypePlanIsSharedBetweenCallsForTheSameType(t *testing.T) {
	cache := newTypePlanCache(8)
	flat, nested := reflect.TypeFor[benchFlat](), reflect.TypeFor[benchNested]()

	first := cache.plan(flat)
	if !first.retained {
		t.Error("the first plan was not kept")
	}
	if cache.size() != 1 {
		t.Errorf("one type is held as %d plans", cache.size())
	}
	if again := cache.plan(flat); again != first {
		t.Error("the same type got two plans")
	}
	if cache.size() != 1 {
		t.Errorf("a second read added a plan: %d held", cache.size())
	}

	other := cache.plan(nested)
	if other == first {
		t.Fatal("two different types share a plan")
	}
	if other.proseSchema() == first.proseSchema() || other.identity().Hash == first.identity().Hash {
		t.Error("two different types share what the plan holds")
	}
	if pointer := cache.plan(reflect.TypeFor[*benchFlat]()); pointer == first {
		t.Error("a type and a pointer to it share a plan, though they have different schemas")
	}
	if cache.size() != 3 {
		t.Errorf("three types are held as %d plans", cache.size())
	}
}

func TestTypePlanForANilTypeIsNotKept(t *testing.T) {
	cache := newTypePlanCache(8)
	plan := cache.plan(nil)
	if plan.retained || cache.size() != 0 {
		t.Errorf("a nil type was kept: retained %v, %d held", plan.retained, cache.size())
	}
	if plan.classification().Support != SupportRejected || plan.proseSchema() != "unknown" || plan.sharedSchema() != nil {
		t.Error("a nil type is not described the way the generators describe it")
	}
	if err := plan.preflight(); err == nil {
		t.Error("a nil type passed preflight")
	}
}

func TestTypePlanCacheStopsAtItsLimit(t *testing.T) {
	const limit = 8
	cache := newTypePlanCache(limit)

	var kept []*typePlan
	var past []reflect.Type
	for n := 1; n <= 3*limit; n++ {
		target := reflect.ArrayOf(n, reflect.TypeFor[string]())
		plan := cache.plan(target)
		if n <= limit {
			kept = append(kept, plan)
			if !plan.retained {
				t.Errorf("type %d, under the limit, was not kept", n)
			}
			continue
		}
		past = append(past, target)
		if plan.retained {
			t.Errorf("type %d, past the limit, was kept", n)
		}
	}
	if cache.size() != limit {
		t.Fatalf("%d plans held, the limit is %d", cache.size(), limit)
	}

	// A type past the limit is still described, and correctly, by a plan made
	// for the call.
	for _, target := range past {
		plan := cache.plan(target)
		if got, want := plan.proseSchema(), describeType(target, map[reflect.Type]bool{}, 0); got != want {
			t.Errorf("%v past the limit: prose %q, want %q", target, got, want)
		}
		if got, want := plan.identity(), typePlanReferenceIdentity(target); got != want {
			t.Errorf("%v past the limit: identity %+v, want %+v", target, got, want)
		}
	}
	if cache.size() != limit {
		t.Errorf("reading types past the limit grew the cache to %d", cache.size())
	}

	// What was kept is still there and is still the one plan.
	for n, plan := range kept {
		if cache.plan(reflect.ArrayOf(n+1, reflect.TypeFor[string]())) != plan {
			t.Errorf("type %d lost its plan once the cache was full", n+1)
		}
	}
}

func TestTypePlanCacheLimitHoldsWhenManyGoroutinesAddTypes(t *testing.T) {
	const limit, workers, perWorker = 16, 50, 20
	cache := newTypePlanCache(limit)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for n := 1; n <= perWorker; n++ {
				plan := cache.plan(reflect.ArrayOf(n, reflect.TypeFor[int]()))
				_ = plan.proseSchema()
			}
		}()
	}
	close(start)
	wg.Wait()

	if cache.size() != limit {
		t.Errorf("%d plans held after %d goroutines added %d types, the limit is %d", cache.size(), workers, perWorker, limit)
	}
	var counted int
	cache.entries.Range(func(_, _ any) bool { counted++; return true })
	if counted != limit {
		t.Errorf("the map holds %d entries and the counter says %d", counted, limit)
	}
}

// The cache every operation reads has the same code as the small ones the tests
// make, with a larger limit: this fills a small one from many goroutines at once,
// each asking for the same types in its own order, and checks the limit is met
// exactly and that a type is either kept, and then has one plan, or is not kept
// at all.
func TestTypePlanCacheHoldsExactlyItsLimitWhenGoroutinesFillItTogether(t *testing.T) {
	const workers, rounds = 24, 40
	for _, limit := range []int{4, 8} {
		kinds := 3 * limit
		targets := make([]reflect.Type, kinds)
		for n := range targets {
			targets[n] = reflect.ArrayOf(n+1, reflect.TypeFor[int]())
		}

		for round := 0; round < rounds; round++ {
			cache := newTypePlanCache(limit)
			handed := make([][]*typePlan, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					handed[w] = make([]*typePlan, kinds)
					for step := 0; step < kinds; step++ {
						// Each goroutine walks the types from a different place, so
						// that they do not meet the same new type in the same order.
						n := (step*7 + w*5) % kinds
						handed[w][n] = cache.plan(targets[n])
					}
				}()
			}
			close(start)
			wg.Wait()

			if cache.size() != limit {
				t.Fatalf("limit %d, round %d: %d plans held", limit, round, cache.size())
			}
			var entries int
			cache.entries.Range(func(_, _ any) bool { entries++; return true })
			if entries != limit {
				t.Fatalf("limit %d, round %d: the map holds %d entries", limit, round, entries)
			}

			kept := 0
			for n, target := range targets {
				stored, isKept := cache.entries.Load(target)
				if isKept {
					kept++
				}
				for w := range handed {
					plan := handed[w][n]
					if plan.typ != target {
						t.Fatalf("limit %d, round %d: goroutine %d was given the plan of another type for %v", limit, round, w, target)
					}
					if isKept && plan != stored.(*typePlan) {
						t.Fatalf("limit %d, round %d: goroutine %d was given a second plan for %v, which the cache keeps", limit, round, w, target)
					}
					if !isKept && plan.retained {
						t.Fatalf("limit %d, round %d: goroutine %d was given a kept plan for %v, which the cache does not hold", limit, round, w, target)
					}
				}
			}
			if kept != limit {
				t.Fatalf("limit %d, round %d: %d types kept", limit, round, kept)
			}
		}
	}
}

func TestTypePlanIsWorkedOutOnceWhenFiftyGoroutinesAskTogether(t *testing.T) {
	const workers = 50
	cache := newTypePlanCache(8)
	target := reflect.TypeFor[benchNested]()

	plans := make([]*typePlan, workers)
	schemas := make([]map[string]any, workers)
	identities := make([]SchemaDescriptor, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			plans[w] = cache.plan(target)
			schemas[w] = plans[w].sharedSchema()
			identities[w] = plans[w].identity()
		}()
	}
	close(start)
	wg.Wait()

	for w := 1; w < workers; w++ {
		if plans[w] != plans[0] {
			t.Fatalf("goroutine %d got a different plan", w)
		}
		if reflect.ValueOf(schemas[w]).Pointer() != reflect.ValueOf(schemas[0]).Pointer() {
			t.Fatalf("goroutine %d got a different schema map: the schema was worked out more than once", w)
		}
		if identities[w] != identities[0] {
			t.Fatalf("goroutine %d got a different identity", w)
		}
	}
	if cache.size() != 1 {
		t.Errorf("%d plans held for one type", cache.size())
	}
}

// Sixteen goroutines meet a type the cache has not seen, in a cache that has room
// for four. Every one of them has to be given the plan the cache keeps, whatever
// the count of places taken is at the moment it arrives.
func TestTypePlanGivesEveryGoroutineMeetingANewTypeTheOnePlanTheCacheKeeps(t *testing.T) {
	const workers, rounds = 16, 200
	for round := 0; round < rounds; round++ {
		cache := newTypePlanCache(4)
		target := reflect.ArrayOf(round+1, reflect.TypeFor[benchFlat]())

		plans := make([]*typePlan, workers)
		results := make([]string, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				plans[w] = cache.plan(target)
				results[w] = plans[w].identityText() + plans[w].extractSystemPrompt(types.ModeUnset) + plans[w].extractCacheIdentity()
			}()
		}
		close(start)
		wg.Wait()

		for w := range plans {
			if plans[w] != plans[0] || results[w] != results[0] {
				t.Fatalf("round %d: goroutine %d was given a different plan (same plan %v, same result %v, kept %v, %d held)",
					round, w, plans[w] == plans[0], results[w] == results[0], plans[w].retained, cache.size())
			}
		}
		if !plans[0].retained || cache.size() != 1 {
			t.Fatalf("round %d: the plan the goroutines share is not the one kept (kept %v, %d held)", round, plans[0].retained, cache.size())
		}
		if cache.plan(target) != plans[0] {
			t.Fatalf("round %d: the cache keeps another plan than the one the goroutines were given", round)
		}
	}
}

func TestLazyValueIsComputedOnceForEveryReader(t *testing.T) {
	const workers = 50
	var lazy lazyValue[string]
	var computed atomic.Int32
	compute := func(*typePlan) string {
		computed.Add(1)
		return "worked out"
	}

	results := make([]string, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[w] = lazy.get(nil, compute)
		}()
	}
	close(start)
	wg.Wait()

	if computed.Load() != 1 {
		t.Errorf("the value was computed %d times", computed.Load())
	}
	for w, got := range results {
		if got != "worked out" {
			t.Errorf("reader %d got %q", w, got)
		}
	}
}

// panicsWith runs read and returns what it panicked with, or nil if it returned.
func panicsWith(read func()) (raised any) {
	defer func() { raised = recover() }()
	read()
	return nil
}

var errTypePlanComputation = errors.New("the computation failed")

type typePlanPanicValue struct{ detail int }

func TestLazyValueThatPanickedPanicsForEveryReaderWithTheOriginalValueAndStack(t *testing.T) {
	var lazy lazyValue[string]
	var computed atomic.Int32
	compute := func(*typePlan) string {
		computed.Add(1)
		panic("the type could not be described")
	}

	var firstStack string
	for read := 1; read <= 3; read++ {
		raised := panicsWith(func() { lazy.get(nil, compute) })
		fault, isFault := raised.(*lazyFault)
		if !isFault {
			t.Fatalf("read %d recovered %#v, want a lazyFault (a value was returned if this is nil)", read, raised)
		}
		if fault.value != "the type could not be described" {
			t.Errorf("read %d: the fault carries %v, want the original panic", read, fault.value)
		}
		if !strings.Contains(fault.Error(), "the type could not be described") {
			t.Errorf("read %d: the message does not carry the value: %s", read, fault.Error())
		}
		// The stack is the one of the place the panic was raised, which is inside
		// compute, and not the one of the reader that re-raised it.
		stack := string(fault.stack)
		if !strings.Contains(stack, "TestLazyValueThatPanickedPanicsForEveryReaderWithTheOriginalValueAndStack.func1") {
			t.Errorf("read %d: the stack does not name the computation that panicked:\n%s", read, stack)
		}
		if read == 1 {
			firstStack = stack
		} else if stack != firstStack {
			t.Errorf("read %d: the stack is not the one of the first panic", read)
		}
	}
	if computed.Load() != 1 {
		t.Errorf("a computation that panicked was run %d times", computed.Load())
	}
}

func TestLazyValueThatPanickedWithAnErrorCanBeUnwrappedToIt(t *testing.T) {
	var lazy lazyValue[int]
	raised := panicsWith(func() {
		lazy.get(nil, func(*typePlan) int { panic(errTypePlanComputation) })
	})
	err, isError := raised.(error)
	if !isError || !errors.Is(err, errTypePlanComputation) {
		t.Errorf("a panic with an error did not reach errors.Is: %#v", raised)
	}
}

func TestLazyValueWhoseComputationEndedItsGoroutineFailsEveryReader(t *testing.T) {
	var lazy lazyValue[string]
	var computed atomic.Int32
	compute := func(*typePlan) string {
		computed.Add(1)
		runtime.Goexit()
		return "never reached"
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		lazy.get(nil, compute)
		t.Error("the goroutine went on after runtime.Goexit")
	}()
	<-done

	for read := 1; read <= 3; read++ {
		raised := panicsWith(func() {
			if got := lazy.get(nil, compute); got != "" {
				t.Errorf("read %d returned %q", read, got)
			}
			t.Errorf("read %d returned a value although the computation never finished", read)
		})
		fault, isFault := raised.(*lazyFault)
		if !isFault || fault.value != nil || !strings.Contains(fault.Error(), "Goexit") {
			t.Errorf("read %d recovered %#v, want a lazyFault that says the computation ended its goroutine", read, raised)
		}
	}
	if computed.Load() != 1 {
		t.Errorf("a computation that ended its goroutine was run %d times", computed.Load())
	}
}

func TestLazyValueEndsInTheOutcomeOfItsComputation(t *testing.T) {
	cases := []struct {
		name    string
		compute func(*typePlan) any
		want    any // the value returned, when panics is false
		panics  bool
		carries any // what a lazyFault holds as its value
	}{
		{"a string", func(*typePlan) any { return "text" }, "text", false, nil},
		{"the zero value of the type", func(*typePlan) any { return nil }, nil, false, nil},
		{"an integer", func(*typePlan) any { return 7 }, 7, false, nil},
		{"an empty string", func(*typePlan) any { return "" }, "", false, nil},
		{"a panic with a string", func(*typePlan) any { panic("text") }, nil, true, "text"},
		{"a panic with an error", func(*typePlan) any { panic(errTypePlanComputation) }, nil, true, errTypePlanComputation},
		{"a panic with a struct", func(*typePlan) any { panic(typePlanPanicValue{3}) }, nil, true, typePlanPanicValue{3}},
		{"a panic with an integer", func(*typePlan) any { panic(42) }, nil, true, 42},
		{"a runtime error", func(*typePlan) any { var m map[string]int; m["x"] = 1; return nil }, nil, true, nil},
		{"a panic with nil", func(*typePlan) any { panic(nil) }, nil, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lazy lazyValue[any]
			for read := 1; read <= 2; read++ {
				var got any
				raised := panicsWith(func() { got = lazy.get(nil, tc.compute) })
				if !tc.panics {
					if raised != nil || got != tc.want {
						t.Fatalf("read %d: got %#v and panic %#v, want %#v", read, got, raised, tc.want)
					}
					continue
				}
				fault, isFault := raised.(*lazyFault)
				if !isFault || len(fault.stack) == 0 {
					t.Fatalf("read %d: recovered %#v, want a lazyFault with a stack", read, raised)
				}
				if tc.carries != nil && fault.value != tc.carries {
					t.Errorf("read %d: the fault carries %#v, want %#v", read, fault.value, tc.carries)
				}
				if fault.value == nil {
					t.Errorf("read %d: the fault carries no value for a panic", read)
				}
			}
		})
	}
}

func TestWhatAPlanHandsOutCannotChangeWhatItHolds(t *testing.T) {
	target := reflect.TypeFor[benchNested]()
	pristine := jsonSchemaFor(target, map[reflect.Type]bool{}, 0)

	// GenerateJSONSchema gives each caller a schema of their own.
	first := GenerateJSONSchema(target)
	if !reflect.DeepEqual(first, pristine) {
		t.Fatal("GenerateJSONSchema differs from the generator")
	}
	first["type"] = "tampered"
	first["required"].([]string)[0] = "tampered"
	properties := first["properties"].(map[string]any)
	delete(properties, "order_id")
	properties["items"].(map[string]any)["type"] = "tampered"
	properties["ship_to"].(map[string]any)["required"].([]string)[0] = "tampered"
	properties["ship_to"].(map[string]any)["properties"].(map[string]any)["city"].(map[string]any)["type"] = "tampered"
	first["additionalProperties"] = true
	if second := GenerateJSONSchema(target); !reflect.DeepEqual(second, pristine) {
		t.Error("changing a schema GenerateJSONSchema returned changed the next one")
	}
	if shared := typePlanFor(target).sharedSchema(); !reflect.DeepEqual(shared, pristine) {
		t.Error("changing a schema GenerateJSONSchema returned changed the plan's schema")
	}
	if reflect.ValueOf(GenerateJSONSchema(target)).Pointer() == reflect.ValueOf(first).Pointer() {
		t.Error("two calls were given the same map")
	}

	// A refusal is made for each call.
	rejected := reflect.TypeFor[typePlanWithAny]()
	err := PreflightType(rejected)
	var refusal *types.OperationError
	if !errors.As(err, &refusal) {
		t.Fatalf("PreflightType gave %v, not an OperationError", err)
	}
	message := refusal.Message
	refusal.Message = "tampered"
	refusal.Kind = types.KindUnknown
	again := PreflightType(rejected)
	var secondRefusal *types.OperationError
	if !errors.As(again, &secondRefusal) || secondRefusal == refusal || secondRefusal.Message != message || secondRefusal.Kind != types.KindConfiguration {
		t.Errorf("a refusal that was changed reached the next caller: %v", again)
	}

	// The report and the identity are values.
	report := ClassifyType(rejected)
	report.Reason = "tampered"
	if ClassifyType(rejected).Reason == "tampered" {
		t.Error("a report that was changed reached the next caller")
	}
	descriptor := DescribeSchema(target)
	descriptor.Hash = "tampered"
	if DescribeSchema(target).Hash == "tampered" {
		t.Error("a descriptor that was changed reached the next caller")
	}
}

func TestCloningASchemaCopiesEveryLevelAndKeepsEmptyListsEmpty(t *testing.T) {
	original := map[string]any{
		"type":     []string{"string", "null"},
		"required": []string(nil),
		"enum":     []string{},
		"anyOf":    []any{map[string]any{"type": "null"}, []any{"nested"}},
		"items":    map[string]any{"type": "integer", "deeper": map[string]any{"flag": true}},
		"closed":   false,
		"count":    3,
	}
	copied := cloneSchema(original)
	if !reflect.DeepEqual(copied, original) {
		t.Fatalf("the copy differs from the original: %v", copied)
	}
	if copied["required"].([]string) != nil {
		t.Error("a nil list became an empty one: it would be written as [] and not null")
	}
	if copied["enum"].([]string) == nil {
		t.Error("an empty list became a nil one: it would be written as null and not []")
	}

	copied["type"].([]string)[0] = "tampered"
	copied["anyOf"].([]any)[0].(map[string]any)["type"] = "tampered"
	copied["items"].(map[string]any)["deeper"].(map[string]any)["flag"] = false
	copied["added"] = 1
	if original["type"].([]string)[0] != "string" ||
		original["anyOf"].([]any)[0].(map[string]any)["type"] != "null" ||
		original["items"].(map[string]any)["deeper"].(map[string]any)["flag"] != true {
		t.Error("changing the copy changed the original")
	}
	if _, added := original["added"]; added {
		t.Error("a key added to the copy appeared in the original")
	}
	if cloneSchema(nil) != nil {
		t.Error("the copy of nothing is something")
	}
}

// typePlanUnclonedKind names the first value in a schema, at any depth, of a kind
// cloneSchemaValue does not copy: it would be shared between the schema a plan
// holds and the copy a caller changes. Strings, booleans and numbers are values
// and are shared safely; maps and lists have to be kinds the clone knows. It
// returns "" when every value is one the clone handles.
func typePlanUnclonedKind(value any, path string) string {
	switch typed := value.(type) {
	case nil, string, bool, int, int64, float64:
		return ""
	case map[string]any:
		for key, entry := range typed {
			if found := typePlanUnclonedKind(entry, path+"/"+key); found != "" {
				return found
			}
		}
		return ""
	case []any:
		for i, entry := range typed {
			if found := typePlanUnclonedKind(entry, fmt.Sprintf("%s[%d]", path, i)); found != "" {
				return found
			}
		}
		return ""
	case []string:
		return ""
	default:
		return fmt.Sprintf("%s holds a %T", path, value)
	}
}

func TestTypePlanUnclonedKindFindsWhatTheCloneWouldShare(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string // empty when nothing is found
	}{
		{"a string", "text", ""},
		{"a boolean", false, ""},
		{"nothing", nil, ""},
		{"an integer", 3, ""},
		{"a list of strings", []string{"a"}, ""},
		{"a list of values", []any{"a", map[string]any{"type": "null"}}, ""},
		{"a map of kinds the clone knows", map[string]any{"a": []string{"x"}, "b": map[string]any{"c": true}}, ""},
		{"a list of maps", []map[string]any{{"type": "null"}}, "/ holds a []map[string]interface {}"},
		{"a typed map", map[string]string{"type": "null"}, "/ holds a map[string]string"},
		{"a list of lists of strings", [][]string{{"a"}}, "/ holds a [][]string"},
		{"a list of maps inside a map", map[string]any{"anyOf": []map[string]any{{}}}, "//anyOf holds a []map[string]interface {}"},
		{"a pointer", new(string), "/ holds a *string"},
		{"a struct", struct{ A int }{1}, "/ holds a struct { A int }"},
		{"a typed map inside a list inside a map", map[string]any{"a": []any{map[string]int{}}}, "//a[0] holds a map[string]int"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := typePlanUnclonedKind(tc.value, "/"); got != tc.want {
				t.Errorf("found %q, want %q", got, tc.want)
			}
		})
	}
}

// Every schema the generators write, and the envelopes built from them, holds
// only kinds cloneSchema copies. A new kind of value in a schema, such as a list
// of maps, would be handed to callers by reference, and a change made to the copy
// would reach the schema every call shares.
func TestEverySchemaTheGeneratorsWriteHoldsOnlyKindsTheCloneCopies(t *testing.T) {
	for name, target := range typePlanTargets() {
		t.Run(name, func(t *testing.T) {
			schema := jsonSchemaFor(target, map[reflect.Type]bool{}, 0)
			if found := typePlanUnclonedKind(schema, name); found != "" {
				t.Errorf("a schema written for %v: %s", target, found)
			}
		})
	}

	properties := map[string]any{
		"plain":    map[string]any{"type": "string"},
		"optional": map[string]any{"type": "integer"},
		"list":     listOf(map[string]any{"type": "string"}),
		"either":   acceptingNull(map[string]any{"anyOf": []any{map[string]any{"type": "string"}}}),
		"nested":   jsonSchemaFor(reflect.TypeFor[benchNested](), map[reflect.Type]bool{}, 0),
	}
	envelope := strictEnvelope(properties, "plain")
	if envelope == nil {
		t.Fatal("no envelope was built")
	}
	if found := typePlanUnclonedKind(envelope, "envelope"); found != "" {
		t.Errorf("an envelope: %s", found)
	}
}

// The copy of a schema shares no map and no list with the original, at any
// depth, for each schema the generators write.
func TestACopyOfASchemaSharesNothingWithTheOriginal(t *testing.T) {
	var shares func(original, copied any, path string) string
	shares = func(original, copied any, path string) string {
		switch typed := original.(type) {
		case map[string]any:
			other := copied.(map[string]any)
			if reflect.ValueOf(typed).Pointer() == reflect.ValueOf(other).Pointer() {
				return path + " is the same map"
			}
			for key, entry := range typed {
				if found := shares(entry, other[key], path+"/"+key); found != "" {
					return found
				}
			}
		case []any:
			other := copied.([]any)
			if len(typed) > 0 && reflect.ValueOf(typed).Pointer() == reflect.ValueOf(other).Pointer() {
				return path + " is the same list"
			}
			for i, entry := range typed {
				if found := shares(entry, other[i], fmt.Sprintf("%s[%d]", path, i)); found != "" {
					return found
				}
			}
		case []string:
			other := copied.([]string)
			if len(typed) > 0 && reflect.ValueOf(typed).Pointer() == reflect.ValueOf(other).Pointer() {
				return path + " is the same list"
			}
		}
		return ""
	}

	for name, target := range typePlanTargets() {
		original := jsonSchemaFor(target, map[reflect.Type]bool{}, 0)
		if original == nil {
			continue
		}
		copied := cloneSchema(original)
		if !reflect.DeepEqual(copied, original) {
			t.Errorf("%s: the copy differs from the original", name)
		}
		if found := shares(original, copied, name); found != "" {
			t.Errorf("%s", found)
		}
	}
}

// typePlanWithoutRoom swaps the cache every operation reads for one that holds
// nothing, so that every type is one past the limit.
func typePlanWithoutRoom(t *testing.T) {
	t.Helper()
	previous := typePlans
	typePlans = newTypePlanCache(0)
	t.Cleanup(func() { typePlans = previous })
}

// A type the cache has no room for gets a plan of its own for each ask. The check
// of a Strict answer asks for the plan of every struct value it visits, so for
// a list of a thousand lines it asked a thousand times, and worked the type's
// fields out each time. The plan is fetched once for each type in a check.
func TestValidatingAListOfATypePastTheCacheLimitWorksTheTypeOutOnce(t *testing.T) {
	typePlanWithoutRoom(t)
	line := benchLineItem{SKU: "a", Description: "d", Quantity: 1, UnitPrice: 2}
	listOf := func(n int) *struct{ Items []benchLineItem } {
		list := &struct{ Items []benchLineItem }{}
		for i := 0; i < n; i++ {
			list.Items = append(list.Items, line)
		}
		return list
	}
	short, long := listOf(2), listOf(200)
	if err := ValidateExtractedData(short); err != nil {
		t.Fatalf("the short list fails the check: %v", err)
	}
	if err := ValidateExtractedData(long); err != nil {
		t.Fatalf("the long list fails the check: %v", err)
	}

	forShort := testing.AllocsPerRun(20, func() { _ = ValidateExtractedData(short) })
	forLong := testing.AllocsPerRun(20, func() { _ = ValidateExtractedData(long) })
	if forLong > forShort {
		t.Errorf("a list of 200 lines makes %.0f allocations and a list of 2 makes %.0f: the type is worked out again for each line", forLong, forShort)
	}
}

// What the check says does not depend on whether the cache has room for the
// type.
func TestValidatingGivesTheSameAnswerWhetherOrNotTheCacheHasRoomForTheType(t *testing.T) {
	type holder struct {
		Lines []benchLineItem
		Ship  benchAddress
		Named map[string]benchAddress
	}
	full := benchLineItem{SKU: "a", Description: "d", Quantity: 1, UnitPrice: 2}
	address := benchAddress{Street: "s", City: "c", Region: "r", Postcode: "p", Country: "c"}
	values := []any{
		&holder{Lines: []benchLineItem{full, full}, Ship: address},
		&holder{Lines: []benchLineItem{full, {}}, Ship: address},
		&holder{Lines: []benchLineItem{full}, Ship: benchAddress{}},
		&holder{Lines: []benchLineItem{{}}, Ship: address},
		&holder{Ship: address, Named: map[string]benchAddress{"home": address}},
		&holder{Ship: address, Named: map[string]benchAddress{"home": address, "work": {}}},
		holder{Lines: []benchLineItem{full}, Ship: address},
		[]holder{{Lines: []benchLineItem{full}, Ship: address}, {Lines: []benchLineItem{{}}, Ship: address}},
		[]*holder{{Lines: []benchLineItem{full}, Ship: address}, nil},
		map[string]*holder{"a": {Lines: []benchLineItem{full}, Ship: address}, "b": {Ship: benchAddress{}}},
		benchFlat{},
		&benchFlat{OrderID: "A"},
	}
	describe := func(err error) string {
		if err == nil {
			return "<nil>"
		}
		return err.Error()
	}
	var kept []string
	for _, value := range values {
		kept = append(kept, describe(ValidateExtractedData(value)))
	}

	typePlanWithoutRoom(t)
	for n, value := range values {
		if got := describe(ValidateExtractedData(value)); got != kept[n] {
			t.Errorf("value %d: %q with no room in the cache, %q with room", n, got, kept[n])
		}
	}
}

// typePlanQuietLogs keeps the library from writing a line for each call, which
// is most of what a call would otherwise cost.
func typePlanQuietLogs(tb testing.TB) {
	tb.Helper()
	previous := logger.GetLogger()
	logger.ConfigureLogger(logger.LoggerConfig{Level: logger.ErrorLevel, DisableStderr: true})
	tb.Cleanup(func() { logger.SetLogger(previous) })
}

// typePlanSentRequest is what an operation hands the model caller.
type typePlanSentRequest struct {
	system, user string
	opts         types.OpOptions
}

// typePlanRecordCalls answers every call with response and records what each
// was sent.
func typePlanRecordCalls(t *testing.T, response string) *[]typePlanSentRequest {
	t.Helper()
	var sent []typePlanSentRequest
	var mu sync.Mutex
	swapLLMCaller(t, func(_ context.Context, system, user string, opts types.OpOptions) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, typePlanSentRequest{system, user, opts})
		return response, nil
	})
	return &sent
}

func TestExtractSendsWhatTheGeneratorsWrite(t *testing.T) {
	typePlanQuietLogs(t)
	target := reflect.TypeFor[benchNested]()
	answer := benchAnswerFor(t, target)

	for _, mode := range []types.Mode{types.TransformMode, types.Strict, types.Creative, types.ModeUnset} {
		t.Run(mode.String(), func(t *testing.T) {
			sent := typePlanRecordCalls(t, answer)
			if _, err := Extract[benchNested]("order A-2207 from Dana", NewExtractOptions().WithMode(mode)); err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if len(*sent) != 1 {
				t.Fatalf("%d calls, want 1", len(*sent))
			}
			call := (*sent)[0]

			effective := call.opts.Mode
			prose := describeType(target, map[reflect.Type]bool{}, 0)
			if want := BuildExtractSystemPrompt(prose, effective); call.system != want {
				t.Errorf("the system prompt is not the one BuildExtractSystemPrompt writes:\n%q\nwant\n%q", call.system, want)
			}
			identity := typePlanReferenceIdentity(target)
			if call.opts.SchemaID != identity.String() {
				t.Errorf("SchemaID = %q, want %q", call.opts.SchemaID, identity.String())
			}
			if want := SchemaCacheKey("extract", extractPromptVersion, identity); call.opts.CacheIdentity != want {
				t.Errorf("CacheIdentity = %q, want %q", call.opts.CacheIdentity, want)
			}
			if call.opts.SchemaName != schemaNameFor(target) || call.opts.ResponseFormat != "json" {
				t.Errorf("SchemaName %q, ResponseFormat %q", call.opts.SchemaName, call.opts.ResponseFormat)
			}
			if want := jsonSchemaFor(target, map[reflect.Type]bool{}, 0); !reflect.DeepEqual(call.opts.JSONSchema, want) {
				t.Errorf("the schema sent is not the one the generator writes: %v", call.opts.JSONSchema)
			}
		})
	}
}

func TestOperationsDoNotChangeTheSchemaTheyShare(t *testing.T) {
	typePlanQuietLogs(t)
	order := reflect.TypeFor[benchNested]()
	before := cloneSchema(typePlanFor(order).sharedSchema())
	answer := benchAnswerFor(t, order)

	sent := typePlanRecordCalls(t, answer)
	for call := 0; call < 3; call++ {
		if _, err := Extract[benchNested]("order A-2207", NewExtractOptions()); err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if _, err := Extract[benchNested]("order A-2207", NewExtractOptions().WithMode(types.Strict)); err != nil {
			t.Fatalf("Extract in strict mode: %v", err)
		}
		if _, err := Transform[benchFlat, benchNested](benchFlat{OrderID: "A-2207"}, NewTransformOptions()); err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if _, err := Generate[benchNested]("an order", NewGenerateOptions()); err != nil {
			t.Fatalf("Generate: %v", err)
		}
	}

	if got := typePlanFor(order).sharedSchema(); !reflect.DeepEqual(got, before) {
		t.Error("a full Extract, Transform and Generate changed the schema the plan holds")
	}
	if len(*sent) != 12 {
		t.Fatalf("%d calls, want 12", len(*sent))
	}
	shared := reflect.ValueOf(typePlanFor(order).sharedSchema()).Pointer()
	for n, call := range *sent {
		if reflect.ValueOf(call.opts.JSONSchema).Pointer() != shared {
			t.Errorf("call %d was sent a schema of its own, not the plan's", n)
		}
		if !reflect.DeepEqual(call.opts.JSONSchema, before) {
			t.Errorf("call %d was sent a schema that differs from the generated one", n)
		}
	}
}

func TestTransformAndGenerateSendWhatTheGeneratorsWrite(t *testing.T) {
	typePlanQuietLogs(t)
	from, to := reflect.TypeFor[benchFlat](), reflect.TypeFor[benchNested]()
	sent := typePlanRecordCalls(t, benchAnswerFor(t, to))

	if _, err := Transform[benchFlat, benchNested](benchFlat{OrderID: "A-2207"}, NewTransformOptions()); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if _, err := Generate[benchNested]("an order", NewGenerateOptions()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	prose := func(target reflect.Type) string { return describeType(target, map[reflect.Type]bool{}, 0) }
	for n, call := range *sent {
		if call.opts.SchemaName != schemaNameFor(to) || !reflect.DeepEqual(call.opts.JSONSchema, jsonSchemaFor(to, map[reflect.Type]bool{}, 0)) {
			t.Errorf("call %d: the schema sent is not the generator's for the target", n)
		}
	}
	if !strings.Contains((*sent)[0].system, prose(from)) || !strings.Contains((*sent)[0].system, prose(to)) {
		t.Error("Transform's prompt does not carry the shapes of both types")
	}
	if !strings.Contains((*sent)[1].system, prose(to)) {
		t.Error("Generate's prompt does not carry the shape of its target")
	}
}

func TestExtractOfATypeNoAnswerCouldSatisfyIsRefusedBeforeACall(t *testing.T) {
	typePlanQuietLogs(t)
	sent := typePlanRecordCalls(t, `{"name":"x"}`)
	for round := 0; round < 3; round++ {
		_, err := Extract[typePlanWithAny]("anything", NewExtractOptions())
		var failure types.ExtractError
		if !errors.As(err, &failure) {
			t.Fatalf("round %d: Extract gave %v, not an ExtractError", round, err)
		}
		var refusal *types.OperationError
		if !errors.As(failure.Err, &refusal) || refusal.Kind != types.KindConfiguration {
			t.Errorf("round %d: the error does not carry the configuration refusal: %v", round, failure.Err)
		}
	}
	if len(*sent) != 0 {
		t.Errorf("%d calls were made for a type that is refused", len(*sent))
	}
}

func TestOperationsRefuseATypeThatEmbedsItselfBeforeACall(t *testing.T) {
	typePlanQuietLogs(t)
	sent := typePlanRecordCalls(t, `{"x":1}`)

	refused := func(t *testing.T, err error) {
		t.Helper()
		var refusal *types.OperationError
		if !errors.As(err, &refusal) || refusal.Kind != types.KindConfiguration {
			t.Errorf("got %v, want a configuration refusal", err)
		}
	}
	t.Run("Extract of a pointer to itself", func(t *testing.T) {
		_, err := Extract[selfEmbedding]("anything", NewExtractOptions())
		refused(t, err)
	})
	t.Run("Extract of an exported one", func(t *testing.T) {
		_, err := Extract[SelfEmbeddingExported]("anything", NewExtractOptions())
		refused(t, err)
	})
	t.Run("Extract of a ring", func(t *testing.T) {
		_, err := Extract[embeddingRingFirst]("anything", NewExtractOptions().WithMode(types.Strict))
		refused(t, err)
	})
	t.Run("Extract of a type that embeds a ring", func(t *testing.T) {
		_, err := Extract[embeddingARing]("anything", NewExtractOptions())
		refused(t, err)
	})
	t.Run("Transform to it", func(t *testing.T) {
		_, err := Transform[benchFlat, selfEmbedding](benchFlat{OrderID: "A-2207"}, NewTransformOptions())
		refused(t, err)
	})
	t.Run("Generate of it", func(t *testing.T) {
		_, err := Generate[embeddingRingOfThreeFirst]("anything", NewGenerateOptions())
		refused(t, err)
	})

	if len(*sent) != 0 {
		t.Errorf("%d calls were made for types that are refused", len(*sent))
	}
}

// Everything a plan can be asked about a type that embeds itself is answered,
// and none of it follows the embedding until the stack runs out. A test that
// overflowed the stack would end the test binary, which is why this is a table
// of reads and not a loop over time.
func TestTypePlanOfATypeThatEmbedsItselfAnswersEveryRead(t *testing.T) {
	for name, target := range embeddingCycles() {
		t.Run(name, func(t *testing.T) {
			plan := newTypePlanCache(4).plan(target)
			if plan.classification().Support != SupportRejected || plan.preflight() == nil {
				t.Errorf("classification %+v, preflight %v", plan.classification(), plan.preflight())
			}
			if plan.sharedSchema() != nil {
				t.Errorf("a schema was written: %v", plan.sharedSchema())
			}
			if plan.proseSchema() == "" || plan.identity().Hash == "" || plan.identity().Dialect != "prose" {
				t.Errorf("prose %q, identity %+v", plan.proseSchema(), plan.identity())
			}
			for _, mode := range []types.Mode{types.ModeUnset, types.Strict, types.TransformMode, types.Creative} {
				if plan.extractSystemPrompt(mode) == "" {
					t.Errorf("no system prompt for mode %v", mode)
				}
			}
			_ = plan.identityText()
			_ = plan.extractCacheIdentity()
			if target.Kind() == reflect.Struct {
				_ = plan.requiredFields()
			}
		})
	}
}

// Reading what a plan holds costs nothing: it is a lookup and a load.
func TestTypePlanReadsAllocateNothing(t *testing.T) {
	for _, target := range benchBuildTypes {
		plan := typePlanFor(target.typ)
		// Worked out once, so that what is measured is a read.
		_ = plan.preflight()
		_ = plan.proseSchema()
		_ = plan.sharedSchema()
		_ = plan.schemaName()
		_ = plan.identityText()
		_ = plan.extractCacheIdentity()
		_ = plan.extractSystemPrompt(types.TransformMode)
		_ = plan.requiredFields()

		reads := map[string]func(){
			"the plan of a type":    func() { _ = typePlanFor(target.typ) },
			"preflight":             func() { _ = plan.preflight() },
			"classification":        func() { _ = ClassifyType(target.typ) },
			"prose schema":          func() { _ = GenerateTypeSchema(target.typ) },
			"JSON schema":           func() { _ = plan.sharedSchema() },
			"schema name":           func() { _ = plan.schemaName() },
			"identity":              func() { _ = DescribeSchema(target.typ) },
			"identity text":         func() { _ = plan.identityText() },
			"extract cache key":     func() { _ = plan.extractCacheIdentity() },
			"extract system prompt": func() { _ = plan.extractSystemPrompt(types.Strict) },
			"required fields":       func() { _ = plan.requiredFields() },
		}
		for name, read := range reads {
			if allocs := testing.AllocsPerRun(100, read); allocs != 0 {
				t.Errorf("%s of %s allocates %.0f times", name, target.name, allocs)
			}
		}
	}
}

// The schema and prompt work of one Extract depends on the type alone, so once
// the plan holds it, it is a handful of loads and allocates nothing.
//
// This runs typePlanBuildAsExtractDoes, a copy in this file of the calls core.go
// makes, and not Extract. It guards the plan: a read from it that starts to
// allocate fails here. It does not guard core.go: if Extract built the schema or
// the prompt itself again, this test would still pass. TestCallAllocationsStayWithinBudget
// drives the real Extract, Transform and Generate and is the test that fails
// when core.go stops using the plan.
func TestExtractBuildAllocationsStayWithinBudget(t *testing.T) {
	budgets := map[string]float64{"flat3": 0, "nested12": 0, "wide60": 0}
	for _, target := range benchBuildTypes {
		budget, ok := budgets[target.name]
		if !ok {
			t.Fatalf("no budget is recorded for %s", target.name)
		}
		build := func() { typePlanBuildAsExtractDoes(target.typ) }
		build()
		if allocs := testing.AllocsPerRun(100, build); allocs > budget {
			t.Errorf("building the request of an Extract of %s makes %.0f allocations, over its budget of %.0f", target.name, allocs, budget)
		}
	}
}

// typePlanBuildAsExtractDoes is the part of Extract that depends on the type
// alone, in the order core.go does it.
func typePlanBuildAsExtractDoes(target reflect.Type) (system string, opt types.OpOptions) {
	plan := typePlanFor(target)
	if err := plan.preflight(); err != nil {
		return "", opt
	}
	opt.ResponseFormat = "json"
	if schema := plan.sharedSchema(); schema != nil {
		opt.JSONSchema = schema
		opt.SchemaName = plan.schemaName()
	}
	opt.SchemaID = plan.identityText()
	opt.CacheIdentity = plan.extractCacheIdentity()
	return plan.extractSystemPrompt(types.TransformMode), opt
}

// A whole call, with the model caller replaced by one that answers at once, so
// that what is held is what the library does around the call: the options, the
// type's plan, the prompts, the decode of the answer and its checks. Each budget
// is what was measured, on go1.27.1, plus two, so that a change which makes a call
// allocate again where the plan removed it fails, and one that adds a few does
// not. The strict decoder is encoding/json/v2 there, which is why the strict row
// is so far below what the decoder of earlier releases needed. The "uncached" runs
// of BenchmarkExtractThroughAStub show what the same calls cost without the plan.
func TestCallAllocationsStayWithinBudget(t *testing.T) {
	typePlanQuietLogs(t)
	flat, nested := reflect.TypeFor[benchFlat](), reflect.TypeFor[benchNested]()
	cases := []struct {
		name   string
		answer reflect.Type
		run    func() error
		budget float64
	}{
		{"Extract of three fields", flat, typePlanExtractOf(flat, types.TransformMode), 16},
		{"Extract of twelve fields", nested, typePlanExtractOf(nested, types.TransformMode), 22},
		{"Extract of twelve fields, strict", nested, typePlanExtractOf(nested, types.Strict), 23},
		{"ExtractResult of three fields", flat, func() error {
			_, err := ExtractResult[benchFlat]("order A-2207, Team plan, $49", NewExtractOptions())
			return err
		}, 25},
		{"Transform to twelve fields", nested, func() error {
			_, err := Transform[benchFlat, benchNested](benchFlat{OrderID: "A-2207"}, NewTransformOptions())
			return err
		}, 31},
		{"Generate of twelve fields", nested, func() error {
			_, err := Generate[benchNested]("an order for a team plan", NewGenerateOptions())
			return err
		}, 21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapLLMResponse(t, benchAnswerFor(t, tc.answer))
			if err := tc.run(); err != nil {
				t.Fatal(err)
			}
			allocs := testing.AllocsPerRun(50, func() { _ = tc.run() })
			t.Logf("%.0f allocations (budget %.0f)", allocs, tc.budget)
			if allocs > tc.budget {
				t.Errorf("%s makes %.0f allocations, over its budget of %.0f", tc.name, allocs, tc.budget)
			}
		})
	}
}

func typePlanExtractOf(target reflect.Type, mode types.Mode) func() error {
	switch target {
	case reflect.TypeFor[benchFlat]():
		return func() error {
			_, err := Extract[benchFlat]("order A-2207, Team plan, $49", NewExtractOptions().WithMode(mode))
			return err
		}
	default:
		return func() error {
			_, err := Extract[benchNested]("order A-2207, Team plan, $49", NewExtractOptions().WithMode(mode))
			return err
		}
	}
}

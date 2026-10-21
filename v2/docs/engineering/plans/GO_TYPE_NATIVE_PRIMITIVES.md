> **Status: motivation, not a plan.** The philosophy here is the library's, and it is
> why `to-production.md` exists; the specific primitive proposals predate the
> operation catalogue in that document's §6.2, which supersedes them (TODOS.md PS-002).

# Go Type-Native LLM Primitives

## Philosophy
**Make LLM operations feel like native Go operations** - seamless integration with Go's type system, where marshalling/unmarshalling happens transparently.

---

## 🎯 Go Type System Primitives

### 1. **Map Operations** - Work with `map[K]V`

#### **MapKeys** - Extract/infer keys from unstructured data
```go
// Parse text and create map with LLM-inferred keys
productPrices, err := ops.MapKeys[string, float64](
    "Apples are $2.50, Bananas cost $1.25, Oranges $3.00",
    ops.NewMapKeysOptions(),
)
// Result: map[string]float64{"Apples": 2.50, "Bananas": 1.25, "Oranges": 3.00}
```

#### **MapValues** - Transform values in place
```go
prices := map[string]float64{"apple": 1.50, "banana": 0.75}
// Apply 10% discount with explanation
discounted, reasons := ops.MapValues(prices, 
    "apply 10% discount",
    ops.NewMapValuesOptions(),
)
// Result: map with updated values + reasoning per key
```

#### **MapFilter** - Semantic filtering of map entries
```go
products := map[string]Product{...}
// "only products under $20"
affordable := ops.MapFilter(products, "under $20", opts)
```

#### **MapMerge** - Intelligent map merging
```go
map1 := map[string]int{"a": 1, "b": 2}
map2 := map[string]int{"b": 3, "c": 4}
// Resolve conflicts intelligently
merged := ops.MapMerge(map1, map2, "prefer larger value", opts)
// Result: {"a": 1, "b": 3, "c": 4}
```

---

### 2. **Slice Operations** - Work with `[]T`

#### **SliceExtract** - Extract slice from unstructured
```go
// Parse text into typed slice
emails, err := ops.SliceExtract[string](
    "Contact john@example.com or jane@company.org",
    ops.NewSliceExtractOptions().WithPattern("email addresses"),
)
// Result: []string{"john@example.com", "jane@company.org"}
```

#### **SliceTransform** - Transform each element
```go
names := []string{"john smith", "jane doe"}
// "convert to title case"
formatted := ops.SliceTransform(names, "title case", opts)
// Result: []string{"John Smith", "Jane Doe"}
```

#### **SliceGroup** - Semantic grouping
```go
type SliceGroup[K comparable, V any] map[K][]V

items := []string{"apple", "car", "banana", "truck", "orange"}
grouped := ops.SliceGroup[string, string](items, "group by category", opts)
// Result: map[string][]string{
//   "fruits": ["apple", "banana", "orange"],
//   "vehicles": ["car", "truck"],
// }
```

#### **SlicePartition** - Split by criteria
```go
numbers := []int{1, 2, 3, 4, 5, 6}
evens, odds := ops.SlicePartition(numbers, "even numbers", opts)
// Result: evens=[2,4,6], odds=[1,3,5]
```

#### **SliceDedupe** - Remove duplicates semantically
```go
comments := []string{
    "This is great!",
    "Love it!",
    "This is really great!",
}
unique := ops.SliceDedupe(comments, 0.8, opts) // 80% similarity threshold
// Result: []string{"This is great!", "Love it!"}
```

#### **SliceReduce** - Aggregate with LLM
```go
reviews := []Review{...}
summary := ops.SliceReduce(reviews, 
    "create overall sentiment summary",
    ops.NewSliceReduceOptions(),
)
// Result: single aggregated summary
```

---

### 3. **Struct Operations** - Work with Go structs

#### **StructFill** - Intelligent field completion
```go
type Person struct {
    Name    string
    Email   string
    Age     int
    Country string // will be inferred
}

partial := Person{Name: "John Smith", Email: "john@example.com"}
// Infer missing fields
complete, confidence := ops.StructFill(partial, opts)
// Result: Country inferred from email domain, Age estimated
```

#### **StructValidate** - Type-aware validation
```go
person := Person{Name: "John", Age: -5, Email: "invalid"}
// Validates based on Go types + semantic rules
result := ops.StructValidate(person, opts)
// Knows Age should be positive, Email should be valid format
```

#### **StructDiff** - Compare struct instances
```go
type StructDiff struct {
    Field    string
    OldValue any
    NewValue any
    Change   string // description of change
}

oldUser := User{Name: "John", Role: "user"}
newUser := User{Name: "John", Role: "admin"}
diffs := ops.StructDiff(oldUser, newUser, opts)
// Result: [{Field: "Role", Old: "user", New: "admin", Change: "promoted to admin"}]
```

#### **StructMerge** - Merge struct instances
```go
source1 := Person{Name: "John", Age: 30}
source2 := Person{Name: "John Smith", Email: "john@example.com"}
// Merge with conflict resolution
merged := ops.StructMerge([]Person{source1, source2}, "prefer more complete", opts)
// Result: {Name: "John Smith", Age: 30, Email: "john@example.com"}
```

#### **StructPartial** - Extract subset of fields
```go
type PersonSummary struct {
    Name string
    Role string
}

fullPerson := Person{Name: "John", Age: 30, Email: "...", Role: "Engineer", ...}
summary := ops.StructPartial[Person, PersonSummary](fullPerson, opts)
// Automatically maps relevant fields
```

---

### 4. **Channel Operations** - Work with `chan T`

#### **ChannelFilter** - Filter streaming data
```go
input := make(chan Message)
// Filter messages in real-time
filtered := ops.ChannelFilter(input, "only urgent messages", opts)
// Returns: chan Message (filtered)
```

#### **ChannelTransform** - Transform stream
```go
input := make(chan string)
output := ops.ChannelTransform[string, ProcessedText](
    input,
    "extract sentiment",
    opts,
)
// Transforms each item as it flows through
```

#### **ChannelBatch** - Batch streaming data
```go
input := make(chan Event)
// Intelligently batch related events
batched := ops.ChannelBatch(input, 
    ops.NewChannelBatchOptions().
        WithBatchBy("related events").
        WithMaxSize(10),
)
// Returns: chan []Event
```

#### **ChannelAggregate** - Running aggregation
```go
input := make(chan Metric)
// Keep running summary
summary := ops.ChannelAggregate(input, 
    "running average and trends",
    time.Second*5, // window
    opts,
)
// Returns: chan AggregateResult
```

---

### 5. **Interface Operations** - Work with `interface{}`

#### **InterfaceConvert** - Smart type conversion
```go
var data interface{} = `{"name": "John", "age": 30}`
// Intelligently convert to target type
person := ops.InterfaceConvert[Person](data, opts)
// Handles JSON string, map, struct, etc.
```

#### **InterfaceInspect** - Analyze unknown types
```go
type InterfaceInfo struct {
    ActualType  string
    Structure   string
    SampleData  string
    Suggestions []string
}

var unknown interface{} = complexData
info := ops.InterfaceInspect(unknown, opts)
// Analyzes and explains the data structure
```

#### **InterfaceMatch** - Type pattern matching
```go
var data interface{} = ...
ops.InterfaceMatch(data,
    ops.When[Person](func(p Person) { ... }),
    ops.When[Company](func(c Company) { ... }),
    ops.WhenJSON(func(m map[string]any) { ... }),
    ops.Otherwise(func() { ... }),
)
```

---

### 6. **Pointer Operations** - Work with `*T`

#### **PointerDeref** - Safe dereferencing with defaults
```go
var ptr *string = nil
// Get value or intelligent default
value := ops.PointerDeref(ptr, "infer appropriate default", opts)
```

#### **PointerFill** - Fill nil pointers
```go
type Config struct {
    Timeout *int
    Host    *string
    Port    *int
}

cfg := Config{Host: stringPtr("localhost")}
// Fill nil fields with intelligent defaults
ops.PointerFill(&cfg, "production settings", opts)
// Timeout and Port now have values
```

---

### 7. **Error Operations** - Work with `error`

#### **ErrorExplain** - Human-friendly error messages
```go
err := someComplexError()
explanation := ops.ErrorExplain(err, 
    ops.NewErrorExplainOptions().
        WithAudience("end-user").
        WithSuggestions(true),
)
// Result: "The file couldn't be saved because... Try: ..."
```

#### **ErrorCategorize** - Classify errors
```go
type ErrorCategory string
const (
    UserError ErrorCategory = "user_error"
    SystemError = "system_error"
    NetworkError = "network_error"
)

err := somethingFailed()
category, confidence := ops.ErrorCategorize(err, opts)
// Intelligently categorizes based on error content
```

#### **ErrorRecover** - Suggest recovery actions
```go
type RecoveryAction struct {
    Action      string
    Probability float64
    Steps       []string
}

err := databaseConnectionFailed()
actions := ops.ErrorRecover(err, opts)
// Returns: suggested recovery actions ranked by likelihood
```

---

### 8. **Function Operations** - Work with `func`

#### **FuncDescribe** - Document functions
```go
myFunc := func(a int, b string) (bool, error) { ... }
doc := ops.FuncDescribe(myFunc, 
    ops.NewFuncDescribeOptions().
        WithExamples(true).
        WithEdgeCases(true),
)
// Generates: signature, purpose, params, returns, examples
```

#### **FuncTest** - Generate test cases
```go
type TestCase[T, R any] struct {
    Input    T
    Expected R
    Name     string
}

myFunc := func(x int) int { return x * 2 }
tests := ops.FuncTest(myFunc, 
    ops.NewFuncTestOptions().WithCoverage("edge cases"),
)
// Generates: []TestCase with various inputs
```

---

### 9. **Type Conversion Primitives**

#### **As** - Smart type casting
```go
// Try to convert to target type intelligently
result, ok := ops.As[TargetType](sourceValue, opts)
if ok {
    // Conversion succeeded
}
```

#### **Cast** - Aggressive conversion
```go
// Force conversion with LLM interpretation
result := ops.Cast[int]("twenty-three", opts)
// Result: 23
```

#### **Parse** - Type-aware parsing
```go
// Parse string to target type
timestamp := ops.Parse[time.Time]("next Friday at 2pm", opts)
amount := ops.Parse[decimal.Decimal]("$1,234.56", opts)
```

---

### 10. **Generic Container Operations**

#### **ContainerQuery** - Query any container
```go
// Works with slices, maps, structs, etc.
result := ops.ContainerQuery[Person](people, 
    "engineers in California",
    opts,
)
```

#### **ContainerTransform** - Transform any container
```go
// Automatically handles different container types
transformed := ops.ContainerTransform[Source, Target](
    source,
    "convert to target format",
    opts,
)
```

#### **ContainerFlat** - Flatten nested structures
```go
nested := map[string]interface{}{
    "user": map[string]interface{}{
        "name": "John",
        "address": map[string]interface{}{
            "city": "NYC",
        },
    },
}
flat := ops.ContainerFlat(nested, ".", opts)
// Result: map[string]interface{}{"user.name": "John", "user.address.city": "NYC"}
```

---

### 11. **Reflection-Based Operations**

#### **TypeInfer** - Infer Go type from data
```go
data := `[{"name": "John", "age": 30}, {"name": "Jane", "age": 25}]`
typeInfo := ops.TypeInfer(data, opts)
// Result: "[]struct{ Name string; Age int }"
// Can generate actual Go code
```

#### **TypeGenerate** - Generate Go types
```go
samples := []map[string]any{...}
code := ops.TypeGenerate("Person", samples, 
    ops.NewTypeGenerateOptions().
        WithJSONTags(true).
        WithValidation(true),
)
// Generates: Go struct definition with appropriate types
```

#### **TypeMorph** - Convert between compatible types
```go
type OldSchema struct { Name string; Age int }
type NewSchema struct { FullName string; Age int; IsAdult bool }

old := OldSchema{Name: "John", Age: 30}
new := ops.TypeMorph[OldSchema, NewSchema](old, opts)
// Result: NewSchema{FullName: "John", Age: 30, IsAdult: true}
```

---

### 12. **Enum & Constant Operations**

#### **EnumParse** - Parse to enum/const
```go
type Status int
const (
    Active Status = iota
    Inactive
    Pending
)

input := "currently active"
status := ops.EnumParse[Status](input, opts)
// Result: Active
```

#### **EnumDescribe** - Explain enum values
```go
descriptions := ops.EnumDescribe[Status](opts)
// Returns: map[Status]string with human descriptions
```

---

### 13. **Time Operations** - Work with `time.Time`

#### **TimeExtract** - Parse natural language dates
```go
dates := ops.TimeExtract("meeting next Tuesday at 2pm and deadline Friday")
// Result: []time.Time with both times
```

#### **TimeFormat** - Intelligent formatting
```go
t := time.Now()
formatted := ops.TimeFormat(t, "friendly", opts)
// Result: "2 hours ago" or "tomorrow at 3pm"
```

#### **TimeRange** - Parse time ranges
```go
type TimeRange struct {
    Start time.Time
    End   time.Time
}

tr := ops.TimeRange("from Monday to Friday", opts)
// Result: TimeRange with start/end
```

---

### 14. **Context Operations** - Work with `context.Context`

#### **ContextExtract** - Extract values from context
```go
ctx := context.WithValue(context.Background(), "user", user)
extracted := ops.ContextExtract[User](ctx, "user information", opts)
// Intelligently finds and extracts from context
```

#### **ContextEnrich** - Add intelligent context
```go
ctx := context.Background()
enriched := ops.ContextEnrich(ctx, situation, opts)
// Adds relevant context values based on situation
```

---

## 🎨 Design Patterns

### Pattern 1: Type-Safe Operations
```go
// All operations preserve type safety
result, err := ops.SliceTransform[string](input, instruction, opts)
// ^^^ Must return []string, enforced at compile time
```

### Pattern 2: Zero-Value Handling
```go
// Smart handling of Go's zero values
var empty Person
filled := ops.StructFill(empty, "create sample person", opts)
// Recognizes zero values and fills appropriately
```

### Pattern 3: Nil-Safe Operations
```go
// All operations handle nil gracefully
var nilSlice []string = nil
result := ops.SliceTransform(nilSlice, "...", opts)
// Returns empty slice, not panic
```

### Pattern 4: Generic Constraints
```go
// Use Go constraints appropriately
func MapKeys[K comparable, V any](input string, opts Options) (map[K]V, error)
// K must be comparable (Go constraint)
```

---

## 🔧 Implementation Strategy

### Phase 1: Basic Type Operations
- Map operations (MapKeys, MapValues, MapFilter, MapMerge)
- Slice operations (SliceExtract, SliceTransform, SliceGroup)
- Struct operations (StructFill, StructValidate, StructDiff)

### Phase 2: Advanced Type Operations
- Channel operations (ChannelFilter, ChannelTransform)
- Interface operations (InterfaceConvert, InterfaceInspect)
- Pointer operations (PointerDeref, PointerFill)

### Phase 3: Reflection & Meta
- Type inference (TypeInfer, TypeGenerate, TypeMorph)
- Function operations (FuncDescribe, FuncTest)
- Error operations (ErrorExplain, ErrorCategorize)

### Phase 4: Specialized
- Time operations (TimeExtract, TimeFormat)
- Enum operations (EnumParse, EnumDescribe)
- Context operations (ContextExtract, ContextEnrich)

---

## 💡 Key Advantages

1. **Native Feel**: Operations work like built-in Go functions
2. **Type Safety**: Full compile-time type checking
3. **Zero Boilerplate**: No manual JSON marshalling needed
4. **Composable**: Can chain operations naturally
5. **Generic**: Work with any Go type
6. **Idiomatic**: Follows Go conventions and patterns

---

## Example: Complex Workflow

```go
// Start with unstructured data
rawData := "John: $1500, Jane: $2000, Bob: $1200"

// Extract to typed map
salaries, _ := ops.MapKeys[string, float64](rawData, opts)

// Filter by criteria
highEarners := ops.MapFilter(salaries, "over $1500", opts)

// Transform values
withBonus := ops.MapValues(highEarners, "add 10% bonus", opts)

// Convert to slice of structs
type Employee struct {
    Name   string
    Salary float64
}
employees := ops.MapToSlice[string, float64, Employee](withBonus, opts)

// Sort by salary
sorted := ops.Sort(employees, "by salary descending", opts)

// Extract top performers
top3 := sorted[:3]

// Generate report
report := ops.StructToText(top3, "professional summary", opts)
```

All type-safe, all LLM-powered, zero manual marshalling! 🚀

---

## Summary

**New Primitive Count**: 50+ type-native operations

**Categories**:
- Map operations: 4
- Slice operations: 6
- Struct operations: 5
- Channel operations: 4
- Interface operations: 3
- Pointer operations: 2
- Error operations: 3
- Function operations: 2
- Type conversion: 3
- Generic containers: 3
- Reflection: 3
- Enum operations: 2
- Time operations: 3
- Context operations: 2

**Total with previous analysis**: ~100 LLM primitives! 🎯

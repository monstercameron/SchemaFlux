> **Status: superseded by PS-002's catalogue**, for the same reason as
> `NEW_PRIMITIVES_ANALYSIS.md`. Kept for the use cases, which are the argument for
> which operations earn a place.

# Practical LLM Operations - Real-World Use Cases

## Philosophy
**Focus on operations that solve actual developer pain points** - not theoretical, but operations people need every day.

---

## 🔥 High-Impact Practical Operations

### 1. **Fix** - Automated Error Correction
```go
type FixResult struct {
    Fixed      interface{}
    Changes    []string
    Confidence float64
    Applied    bool
}

// Fix broken JSON, malformed data, syntax errors, etc.
broken := `{"name": "John", "age": "thirty", missing_quotes: true}`
fixed, err := ops.Fix[map[string]interface{}](broken, 
    ops.NewFixOptions().WithAutoApply(true))
// Result: Valid JSON with corrected syntax and sensible type conversions

// Fix code compilation errors
brokenCode := `func main() { fmt.Println("hello" }`
fixedCode, err := ops.Fix[string](brokenCode, 
    ops.NewFixOptions().WithLanguage("go"))
```
**Use Cases**: Data cleaning, error recovery, API response healing, config file repair

---

### 2. **Migrate** - Data/Code Migration
```go
type MigrationResult struct {
    Migrated   interface{}
    Mapping    map[string]string
    Warnings   []string
    Manual     []string // requires manual intervention
}

// Migrate from old schema to new
oldData := LegacyUser{Username: "john123", FullName: "John"}
newData, err := ops.Migrate[LegacyUser, ModernUser](oldData,
    ops.NewMigrateOptions().
        WithMappingRules(rules).
        WithBreakingChanges(true))

// Migrate between API versions
v1Response := APIv1Response{...}
v2Response := ops.Migrate[APIv1Response, APIv2Response](v1Response, opts)

// Migrate code between languages/frameworks
pythonCode := "def hello(): print('world')"
goCode := ops.Migrate[string, string](pythonCode,
    ops.NewMigrateOptions().
        WithSourceLang("python").
        WithTargetLang("go"))
```
**Use Cases**: API versioning, database migrations, language translations, framework upgrades

---

### 3. **Sanitize** - Intelligent Data Cleaning
```go
type SanitizeResult struct {
    Clean      interface{}
    Removed    []string
    Redacted   []string
    Issues     []string
}

// Remove sensitive data, fix encoding, clean HTML, etc.
dirtyData := `<script>alert('xss')</script>John's email: john@test.com, SSN: 123-45-6789`
clean, err := ops.Sanitize(dirtyData,
    ops.NewSanitizeOptions().
        WithRemove([]string{"scripts", "PII"}).
        WithEncoding("utf-8").
        WithHTMLStrip(true))

// Sanitize user input before storage
userInput := map[string]interface{}{"bio": "<b>hacker</b>", "email": "test@test.com"}
sanitized := ops.Sanitize[map[string]interface{}](userInput, opts)
```
**Use Cases**: Security, data privacy, input validation, log cleaning, HTML sanitization

---

### 4. **Enrich** - Data Augmentation
```go
type EnrichResult[T any] struct {
    Enriched   T
    Added      map[string]interface{}
    Sources    []string
    Confidence float64
}

// Add missing context, metadata, or inferred information
customer := Customer{Email: "john@bigcorp.com"}
enriched, err := ops.Enrich(customer,
    ops.NewEnrichOptions().
        WithInfer([]string{"company", "country", "timezone"}).
        WithSources([]string{"email_domain", "context"}))
// Result: Company="BigCorp", Country="US" (inferred), etc.

// Enrich product data
product := Product{Name: "iPhone 14"}
enriched := ops.Enrich(product,
    ops.NewEnrichOptions().
        WithFields([]string{"category", "price_range", "specs"}))
```
**Use Cases**: CRM data enrichment, product catalogs, lead scoring, analytics

---

### 5. **Mock** - Generate Test Data
```go
// Generate realistic mock data for testing
users := ops.Mock[User](10,
    ops.NewMockOptions().
        WithRealistic(true).
        WithVariety("high").
        WithConstraints(map[string]string{
            "age": "18-65",
            "country": "US,UK,Canada",
        }))

// Generate mock API responses
response := ops.Mock[APIResponse](1,
    ops.NewMockOptions().
        WithScenario("error_case").
        WithSeed(12345)) // reproducible

// Generate test cases
testCases := ops.Mock[TestCase](20,
    ops.NewMockOptions().
        WithCoverage("edge_cases"))
```
**Use Cases**: Testing, development, demos, data seeding, CI/CD

---

### 6. **Diff** - Semantic Differencing (ENHANCED)
```go
type DiffResult struct {
    Added      []Change
    Removed    []Change
    Modified   []Change
    Summary    string
    Impact     string // "breaking", "minor", "patch"
    Conflicts  []Conflict
}

// Compare configurations
oldConfig := Config{Timeout: 30, MaxRetries: 3}
newConfig := Config{Timeout: 60, MaxRetries: 3, NewField: "value"}
diff, err := ops.Diff(oldConfig, newConfig,
    ops.NewDiffOptions().
        WithSemantic(true).
        WithImpactAnalysis(true))
// Result: Explains WHAT changed and WHY it matters

// Diff code changes
diff := ops.Diff(oldCode, newCode,
    ops.NewDiffOptions().
        WithLanguage("go").
        WithExplain(true))
```
**Use Cases**: Version control, change detection, audit logs, config management

---

### 7. **Compress** - Intelligent Compression
```go
// Compress data while preserving meaning
longText := "..." // 5000 words
compressed, err := ops.Compress(longText,
    ops.NewCompressOptions().
        WithTargetRatio(0.3). // 30% of original
        WithPreserve([]string{"key_facts", "conclusions"}))

// Compress API responses (remove redundancy)
response := LargeAPIResponse{...}
compressed := ops.Compress(response,
    ops.NewCompressOptions().
        WithStrategy("remove_redundancy"))
```
**Use Cases**: Token optimization, storage savings, network efficiency, summaries

---

### 8. **Route** - Intelligent Routing/Dispatch
```go
type RouteResult struct {
    Destination string
    Confidence  float64
    Reason      string
    Metadata    map[string]interface{}
}

// Route requests to appropriate handlers
request := IncomingRequest{...}
route, err := ops.Route(request,
    []string{"billing", "support", "sales", "technical"},
    ops.NewRouteOptions().
        WithContext(userHistory).
        WithPriority(true))

// Route messages to queues
message := Message{...}
queue := ops.Route(message,
    []string{"urgent", "normal", "low"},
    opts)
```
**Use Cases**: Message routing, load balancing, support tickets, request dispatch

---

### 9. **Retry** - Smart Retry Logic (ENHANCED)
```go
type RetryResult[T any] struct {
    Result    T
    Attempts  int
    Strategy  string
    Duration  time.Duration
}

// Retry with intelligent backoff
result, err := ops.Retry(
    func() (User, error) { return fetchUser(id) },
    ops.NewRetryOptions().
        WithIntelligentBackoff(true). // LLM analyzes errors
        WithAdaptive(true).           // Changes strategy based on failures
        WithMaxAttempts(5))

// Retry decides: "Network error → exponential backoff"
//                "Rate limit → wait for reset time"
//                "Auth error → no retry"
```
**Use Cases**: API calls, database operations, external services, resilience

---

### 10. **Normalize** - Data Standardization (ENHANCED)
```go
// Normalize addresses, phone numbers, names, dates, etc.
messyAddresses := []string{
    "123 Main St, NYC, NY",
    "123 main street, New York, New York",
    "123 Main Street, New York City",
}
normalized := ops.Normalize[string](messyAddresses,
    ops.NewNormalizeOptions().
        WithStandard("USPS").
        WithDedup(true))
// Result: All converted to same format

// Normalize mixed date formats
dates := []string{"2024-01-15", "Jan 15, 2024", "15/01/2024"}
normalized := ops.Normalize[time.Time](dates, opts)
```
**Use Cases**: Data deduplication, matching, analytics, reporting

---

### 11. **Extract** - Intelligent Extraction (ENHANCED)
```go
// Extract specific entities from unstructured text
text := "Meeting with John at Acme Corp on Monday. Budget: $50k."
entities := ops.ExtractEntities(text,
    []string{"person", "company", "date", "money"},
    opts)
// Result: {person: "John", company: "Acme Corp", date: "Monday", money: "$50k"}

// Extract structured data from documents
invoice := ops.Extract[Invoice](scannedPDF,
    ops.NewExtractOptions().
        WithOCR(true).
        WithValidation(true))

// Extract code patterns
functions := ops.ExtractPattern[FunctionSignature](codebase,
    "public API functions",
    opts)
```
**Use Cases**: Document processing, parsing, entity recognition, data extraction

---

### 12. **Reconcile** - Conflict Resolution
```go
type ReconcileResult[T any] struct {
    Resolved   T
    Conflicts  []Conflict
    Strategy   string
    Confidence float64
}

// Resolve conflicts between multiple sources
sources := []Customer{
    {Name: "John Smith", Email: "john@test.com", Age: 30},
    {Name: "J. Smith", Email: "john@test.com", Age: 32},
    {Name: "John Smith", Phone: "555-1234"},
}
resolved, err := ops.Reconcile(sources,
    ops.NewReconcileOptions().
        WithStrategy("most_recent").
        WithTrustScores(map[int]float64{0: 0.8, 1: 0.9, 2: 0.7}))

// Resolve merge conflicts
merged := ops.Reconcile([]string{branch1, branch2, branch3}, opts)
```
**Use Cases**: Data integration, CRM deduplication, merge conflicts, MDM

---

### 13. **Suggest** - Context-Aware Suggestions
```go
type Suggestion struct {
    Text       string
    Confidence float64
    Category   string
    Action     string
}

// Suggest next actions
currentState := WorkflowState{...}
suggestions := ops.Suggest(currentState,
    ops.NewSuggestOptions().
        WithContext(history).
        WithTopN(5).
        WithRanked(true))
// Result: ["Complete step 3", "Request approval", ...]

// Auto-complete suggestions
partial := "SELECT * FROM use"
completions := ops.Suggest(partial,
    ops.NewSuggestOptions().
        WithContext(schema).
        WithLanguage("sql"))

// Smart replies
email := IncomingEmail{...}
replies := ops.Suggest(email,
    ops.NewSuggestOptions().
        WithTone("professional").
        WithLength("brief"))
```
**Use Cases**: Auto-complete, smart replies, next actions, recommendations

---

### 14. **Annotate** - Add Metadata/Context
```go
type AnnotatedResult[T any] struct {
    Original    T
    Annotations map[string]interface{}
    Tags        []string
    Summary     string
}

// Add annotations to data
code := "func processPayment() { ... }"
annotated := ops.Annotate(code,
    ops.NewAnnotateOptions().
        WithTypes([]string{"intent", "complexity", "risks", "dependencies"}))
// Result: {intent: "payment processing", complexity: "medium", ...}

// Annotate images/files
file := FileData{...}
annotated := ops.Annotate(file,
    ops.NewAnnotateOptions().
        WithExtract([]string{"metadata", "tags", "description"}))
```
**Use Cases**: Documentation, metadata generation, tagging, search indexing

---

### 15. **Recommend** - Recommendation Engine
```go
type Recommendation[T any] struct {
    Item       T
    Score      float64
    Reason     string
    Confidence float64
}

// Recommend items based on context
user := User{History: [...], Preferences: {...}}
recommendations := ops.Recommend[Product](user, catalog,
    ops.NewRecommendOptions().
        WithStrategy("collaborative").
        WithTopN(10).
        WithDiversity(true))

// Recommend actions
situation := SystemState{...}
actions := ops.Recommend[Action](situation, possibleActions, opts)
```
**Use Cases**: Product recommendations, content suggestions, next steps

---

### 16. **Detect** - Pattern/Anomaly Detection
```go
type Detection struct {
    Type       string
    Location   string
    Severity   string
    Description string
    Confidence float64
}

// Detect issues in data
data := []Transaction{...}
anomalies := ops.Detect(data,
    ops.NewDetectOptions().
        WithPatterns([]string{"fraud", "anomaly", "duplicate"}))

// Detect code issues
code := "..."
issues := ops.Detect(code,
    ops.NewDetectOptions().
        WithTypes([]string{"bugs", "security", "performance"}))

// Detect sentiment shifts
comments := []Comment{...}
shifts := ops.Detect(comments,
    ops.NewDetectOptions().
        WithPattern("sentiment_change"))
```
**Use Cases**: Fraud detection, quality assurance, monitoring, security

---

### 17. **Batch** - Batch Processing (ENHANCED)
```go
// Process large collections efficiently
items := []Item{...} // 10,000 items

results := ops.Batch(items,
    func(item Item) (Result, error) {
        return ops.Transform[Item, Result](item, opts)
    },
    ops.NewBatchOptions().
        WithStrategy("adaptive"). // Choose parallel vs merged
        WithMaxConcurrency(10).
        WithErrorHandling("continue").
        WithProgress(true))

// Batch with grouping
results := ops.BatchGrouped(items,
    "group by category",
    processFunc,
    opts)
```
**Use Cases**: Bulk operations, data processing, ETL, migrations

---

### 18. **Cache** - Intelligent Caching
```go
// Cache LLM operations with semantic similarity
cached := ops.Cached(
    func(query string) (Result, error) {
        return ops.Query(data, query, opts)
    },
    ops.NewCacheOptions().
        WithSimilarityMatch(0.95). // Match similar queries
        WithTTL(time.Hour).
        WithInvalidateOn([]string{"data_update"}))

// Semantic cache
result := cached("find all users in California")
// Next call with "show me CA users" → cache hit!
```
**Use Cases**: Cost reduction, performance, rate limiting, efficiency

---

### 19. **Adapt** - Self-Improving Operations
```go
// Operations that learn from feedback
adaptive := ops.Adapt(
    func(input Input) (Output, error) {
        return ops.Classify(input.Text, opts)
    },
    ops.NewAdaptOptions().
        WithFeedbackLoop(true).
        WithMetrics([]string{"accuracy", "latency"}))

// After each call, provide feedback
result, err := adaptive(input)
adaptive.Feedback(result, wasCorrect, actualValue)
// Operation improves over time
```
**Use Cases**: ML pipelines, optimization, A/B testing, personalization

---

### 20. **Stream** - Streaming Operations
```go
// Process streaming data in real-time
stream := ops.Stream[Event, ProcessedEvent](
    inputChannel,
    func(event Event) (ProcessedEvent, error) {
        return ops.Transform[Event, ProcessedEvent](event, opts)
    },
    ops.NewStreamOptions().
        WithBufferSize(100).
        WithWindow(time.Second*5).
        WithAggregation(true))

// Streaming classification
classified := ops.StreamClassify(messagesChannel,
    categories,
    opts)
```
**Use Cases**: Real-time processing, event streams, log processing, monitoring

---

### 21. **Debug** - Debugging Aid
```go
type DebugInfo struct {
    Issue       string
    Explanation string
    Suggestions []string
    StackTrace  string
    Context     map[string]interface{}
}

// Debug complex issues
err := mysteriosError()
debug := ops.Debug(err,
    ops.NewDebugOptions().
        WithContext(map[string]interface{}{
            "request": req,
            "state": currentState,
        }).
        WithDepth("deep"))
// Returns: explanation, root cause, suggestions

// Debug code behavior
result := unexpectedResult()
explanation := ops.Debug(result,
    ops.NewDebugOptions().
        WithExpected(expectedResult).
        WithCode(sourceCode))
```
**Use Cases**: Debugging, troubleshooting, error analysis, DevOps

---

### 22. **Optimize** - Code/Query Optimization
```go
type OptimizationResult struct {
    Optimized   string
    Improvements []string
    Metrics     map[string]float64
}

// Optimize SQL queries
slowQuery := "SELECT * FROM users WHERE ..."
optimized := ops.Optimize(slowQuery,
    ops.NewOptimizeOptions().
        WithType("sql").
        WithGoal("performance").
        WithExplain(true))

// Optimize code
code := "for i := 0; i < len(arr); i++ { ..."
optimized := ops.Optimize(code,
    ops.NewOptimizeOptions().
        WithLanguage("go").
        WithGoals([]string{"performance", "readability"}))
```
**Use Cases**: Performance tuning, code review, query optimization, refactoring

---

### 23. **Lint** - Intelligent Linting
```go
type LintResult struct {
    Issues   []LintIssue
    Warnings []string
    Suggestions []string
    Score    float64
}

// Lint with context awareness
code := "..."
lint := ops.Lint(code,
    ops.NewLintOptions().
        WithRules([]string{"security", "performance", "style"}).
        WithSeverity("error").
        WithAutoFix(true))

// Lint data structures
data := map[string]interface{}{...}
lint := ops.Lint(data,
    ops.NewLintOptions().
        WithSchema(schema).
        WithStrict(true))
```
**Use Cases**: Code quality, validation, CI/CD, standards compliance

---

### 24. **Audit** - Audit Trail Generation
```go
type AuditLog struct {
    Timestamp  time.Time
    Action     string
    User       string
    Changes    []Change
    Impact     string
    Compliance []string
}

// Generate audit logs
change := DataChange{Before: old, After: new}
audit := ops.Audit(change,
    ops.NewAuditOptions().
        WithCompliance([]string{"GDPR", "SOC2"}).
        WithExplain(true))

// Audit API calls
request := APIRequest{...}
audit := ops.Audit(request,
    ops.NewAuditOptions().
        WithSensitiveData(true).
        WithRiskAssessment(true))
```
**Use Cases**: Compliance, security, governance, forensics

---

### 25. **Convert** - Universal Conversion
```go
// Convert between any formats
input := "..." // JSON, XML, YAML, CSV, etc.
output := ops.Convert[TargetType](input,
    ops.NewConvertOptions().
        WithAutoDetect(true).
        WithFormat("yaml"))

// Convert between encodings
data := "..."
converted := ops.Convert(data,
    ops.NewConvertOptions().
        WithFromEncoding("latin1").
        WithToEncoding("utf-8"))

// Convert between data structures
nested := map[string]interface{}{...}
flat := ops.Convert[FlatStruct](nested, opts)
```
**Use Cases**: Data integration, API adaptation, file format conversion

---

## 🎯 Priority Ranking

### Tier 1: Must-Have (Immediate Value)
1. **Fix** - Error correction
2. **Sanitize** - Data cleaning & security
3. **Mock** - Test data generation
4. **Extract** (enhanced) - Entity extraction
5. **Normalize** - Data standardization
6. **Enrich** - Data augmentation

### Tier 2: High Value (Common Use Cases)
7. **Migrate** - Schema/code migration
8. **Diff** - Semantic differencing
9. **Route** - Intelligent routing
10. **Reconcile** - Conflict resolution
11. **Suggest** - Smart suggestions
12. **Detect** - Anomaly detection

### Tier 3: Productivity (Developer Experience)
13. **Debug** - Debugging aid
14. **Optimize** - Code optimization
15. **Lint** - Intelligent linting
16. **Annotate** - Metadata generation
17. **Recommend** - Recommendations
18. **Convert** - Universal conversion

### Tier 4: Advanced (Specialized)
19. **Compress** - Intelligent compression
20. **Batch** (enhanced) - Better bulk ops
21. **Cache** - Semantic caching
22. **Adapt** - Self-improvement
23. **Stream** - Real-time processing
24. **Audit** - Compliance & governance

---

## 💡 Common Patterns Across Operations

### Pattern 1: Progressive Enhancement
```go
// Start simple
result := ops.Fix(data, opts)

// Add intelligence
result := ops.Fix(data, 
    ops.NewFixOptions().
        WithLearn(true).      // Learn from corrections
        WithValidate(true).   // Validate after fixing
        WithExplain(true))    // Explain what was fixed
```

### Pattern 2: Context Awareness
```go
// All operations can use context
result := ops.Suggest(input,
    ops.WithContext(map[string]interface{}{
        "user": currentUser,
        "history": recentActions,
        "environment": "production",
    }))
```

### Pattern 3: Feedback Loops
```go
// Operations that improve
op := ops.NewAdaptiveOperation(baseOp)
result := op.Execute(input)
op.Feedback(wasCorrect, actualValue)
// Gets better over time
```

### Pattern 4: Composability
```go
// Chain operations
result := ops.
    Extract[RawData](input, opts).
    Then(ops.Sanitize).
    Then(ops.Normalize).
    Then(ops.Enrich).
    Execute()
```

---

## 🚀 Real-World Scenarios

### Scenario 1: API Integration
```go
// Receive messy API response
response := thirdPartyAPI.Get()

// Fix malformed JSON
fixed := ops.Fix[APIResponse](response, opts)

// Migrate to our schema
migrated := ops.Migrate[APIResponse, InternalFormat](fixed, opts)

// Normalize data
normalized := ops.Normalize(migrated, opts)

// Enrich with context
enriched := ops.Enrich(normalized, opts)
```

### Scenario 2: Data Processing Pipeline
```go
// Load data
data := loadCSV()

// Sanitize
clean := ops.Sanitize(data, opts)

// Detect anomalies
anomalies := ops.Detect(clean, opts)

// Normalize
normalized := ops.Normalize(clean, opts)

// Deduplicate
unique := ops.Reconcile(normalized, opts)

// Enrich
enriched := ops.Enrich(unique, opts)
```

### Scenario 3: User Input Processing
```go
// Receive user input
input := getUserInput()

// Sanitize for security
safe := ops.Sanitize(input, opts)

// Extract intent
intent := ops.Extract[Intent](safe, opts)

// Route to handler
handler := ops.Route(intent, handlers, opts)

// Suggest responses
suggestions := ops.Suggest(intent, opts)
```

---

## Summary

**25 Practical Operations** focused on:
- ✅ Error handling & recovery
- ✅ Data cleaning & validation
- ✅ Testing & development
- ✅ Integration & migration
- ✅ Security & compliance
- ✅ Performance & optimization
- ✅ Debugging & troubleshooting
- ✅ Real-time processing

Combined with type-native operations (50) and general primitives (32), 
**Total: ~107 comprehensive LLM operations** covering every practical use case! 🎯

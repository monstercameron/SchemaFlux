# SchemaFlux API Reference

## Canonical Import

```bash
go get github.com/monstercameron/schemaflux
```

```go
import schemaflux "github.com/monstercameron/schemaflux"
```

## API Shape

SchemaFlux's primary public API is the fluent request-builder stack.

Every builder follows the same pattern:

```go
result, err := schemaflux.<Builder>(...).
    Steer("...").
    Strict().
    Smart().
    Configure(func(opts ...) ... { return opts }).
    Run()
```

Common builder methods available on most typed LLM builders:
- `WithOptions(...)`
- `Configure(func(opts) opts)`
- `Steer(...)`
- `Mode(...)`
- `Strict()`
- `TransformMode()`
- `Creative()`
- `Intelligence(...)`
- `Smart()`
- `Fast()`
- `Quick()`
- `Context(...)`
- `RequestID(...)` when the underlying option type supports request IDs

Use `Configure(...)` when you need a specialized option that does not have a dedicated fluent shortcut.

## Builder Catalog

### Structured data
- `Extracting[T](input)`
- `Transforming[T, U](input)`
- `Generating[T](prompt)`
- `Inferring[T](input)`
- `Parsing[T](input)`
- `Enriching[T, U](input)`
- `EnrichingInPlace[T](input)`
- `Normalizing[T](input)`
- `NormalizingText(input)`
- `NormalizingBatch[T](items)`
- `Projecting[T, U](input)`
- `Pivoting[T, U](input)`

### Text generation and editing
- `Summarizing(input)`
- `Rewriting(input)`
- `Translating(input)`
- `Expanding(input)`
- `Completing(input)`
- `CompletingField[T](input, fieldName)`
- `Redacting[T](input)`
- `LLMRedacting(input)`

### Evaluation and interpretation
- `Classifying[T, C](input)`
- `Scoring[T](input)`
- `Comparing[T](left, right)`
- `CheckingSimilarity[T](left, right)`
- `Validating[T](input)`
- `Asking[T, A](input, question)`
- `Explaining(input)`
- `Verifying(input)`
- `VerifyingClaim(claim)`

### Collections and ranking
- `Choosing[T](items)`
- `Filtering[T](items)`
- `Sorting[T](items)`
- `Ranking[T](items)`
- `Clustering[T](items)`
- `Matching[S, T](sources, targets)`
- `MatchingOne[S, T](source, targets)`

### Synthesis and decision support
- `Annotating[T](input)`
- `Compressing[T](input)`
- `CompressingText(input)`
- `Decomposing[T](input)`
- `DecomposingInto[T, U](input)`
- `Critiquing[T](input)`
- `Synthesizing[T](sources)`
- `Predicting[T](input)`
- `Negotiating[T](constraints)`
- `NegotiatingAdversarially[T](context)`
- `Resolving[T](sources)`
- `Deriving[T, U](input)`
- `Conforming[T](input, standard)`
- `Interpolating[T](items)`
- `Arbitrating[T](options)`
- `Auditing[T](input)`
- `Assembling[T](parts)`

## Examples

### Extraction

```go
type Person struct {
    Name string `json:"name"`
    Age  int    `json:"age"`
}

person, err := schemaflux.Extracting[Person](raw).
    Strict().
    Smart().
    Steer("Prefer explicit evidence over guesses").
    Run()
```

### Validation

```go
result, err := schemaflux.Validating(person).
    Rules("email must be valid; age must be between 18 and 100").
    Strict().
    Run()
```

### Ranking

```go
ranked, err := schemaflux.Ranking(products).
    By("best battery life for frequent travelers").
    Fast().
    Run()
```

### Matching

```go
matches, err := schemaflux.Matching(leads, accounts).
    By("same company despite name variations").
    Smart().
    Run()
```

### Projection

```go
projected, err := schemaflux.Projecting[InternalUser, PublicProfile](user).
    Exclude("password_hash", "ssn").
    Steer("combine first and last name into display_name").
    Run()
```

## Text Builders With Detailed Results

These builders provide both a simple `Run()` and a richer result method:
- `Summarizing(...).RunDetailed()`
- `Rewriting(...).RunDetailed()`
- `Translating(...).RunDetailed()`
- `Expanding(...).RunDetailed()`

## Compact Helpers

For common collection tasks, compact helpers remain available:
- `ChooseBy(items, criteria...)`
- `FilterBy(items, criteria)`
- `SortBy(items, criteria)`

## Provider Layer

The fluent API works with the same provider stack:
- `openai`
- `anthropic`
- `openrouter`
- `cerebras`
- `deepseek`
- `qwen`
- `zai`
- `local`

Example:

```go
client := schemaflux.NewClient(apiKey).
    WithProvider("deepseek").
    WithRetries(3)
```

## Compatibility Surface

The older direct-call API and `New*Options()` constructors remain exported for backward compatibility.

They are not the recommended starting point for new integrations.

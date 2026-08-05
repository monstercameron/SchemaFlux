# SchemaFlux

SchemaFlux is a Go library for typed LLM operations.

It gives you a single public API built around fluent request builders so application code stays readable while retries, structured output contracts, logging, metrics, and cost tracking stay centralized.

## Install

```bash
go get github.com/monstercameron/schemaflux
```

Set an API key for your provider. OpenAI is the default.

```bash
export SCHEMAFLUX_API_KEY=your-api-key
```

## Quick Start

```go
package main

import (
    "fmt"

    schemaflux "github.com/monstercameron/schemaflux"
)

type Person struct {
    Name string `json:"name"`
    Age  int    `json:"age"`
}

func main() {
    if err := schemaflux.InitWithEnv(); err != nil {
        panic(err)
    }

    person, err := schemaflux.Extracting[Person]("John is 30 years old").
        Strict().
        Run()
    if err != nil {
        panic(err)
    }

    fmt.Printf("%+v\n", person)
}
```

## API Shape

The public API is the fluent builder stack at the root package.

```go
person, err := schemaflux.Extracting[Person](rawText).
    Strict().
    Smart().
    Steer("Prefer explicit evidence over guesses").
    Run()

best, err := schemaflux.Choosing(products).
    By("lowest total cost", "best battery life").
    Fast().
    Run()

summary, err := schemaflux.Summarizing(longText).
    MaxLength(120).
    Run()
```

Builder conventions:
- `Run()` executes the operation
- `WithOptions(...)` replaces the full typed option object
- `Configure(func(...))` lets you mutate the typed option object directly
- common controls such as `Strict()`, `Smart()`, `Fast()`, `Quick()`, `Steer(...)`, `Context(...)`, and `RequestID(...)` are exposed where they make sense

## Core Builders

### Structured extraction and generation
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

### Text operations
- `Summarizing(input)`
- `Rewriting(input)`
- `Translating(input)`
- `Expanding(input)`
- `Completing(input)`
- `CompletingField[T](input, fieldName)`
- `Redacting[T](input)` — see the note below on what it detects
- `LLMRedacting(input)` — see the note below

> **What redaction detects.** Field names are matched as whole names, not
> substrings, so `FirstName` and `APIKey` are redacted while `Filename`,
> `Username`, `Keywords`, `FirstSeen`, and `CardCount` are not. Card numbers are
> validated with Luhn rather than recognised by shape, so an unformatted PAN is
> caught and a 16-digit order number is not. `RedactWithResult` reports the
> fields and values it replaced.
>
> **It is a pattern matcher, not a classifier.** It finds what its categories
> describe and nothing else: a person's name inside a free-text note is not
> detected, and a bare nine-digit number is deliberately not treated as an SSN
> because it is indistinguishable from an order ID. Tag the fields you know with
> `redact:"..."`; the patterns are a safety net under that, not a substitute
> for it.
>
> **Jumbling is obfuscation, not anonymization.** `RedactJumble` and
> `RedactScramble` permute characters, which preserves length, alphabet, and
> frequency. Use them for demo data that has to look realistic, not where
> re-identification matters.

### Analysis and validation
- `Classifying[T, C](input)`
- `Scoring[T](input)`
- `Comparing[T](left, right)`
- `CheckingSimilarity[T](left, right)`
- `Validating[T](input)`
- `Asking[T, A](input, question)`
- `Explaining(input)`
- `Verifying(input)`
- `VerifyingClaim(claim)`

### Collection and reasoning
- `Choosing[T](items)`
- `Filtering[T](items)`
- `Sorting[T](items)`
- `Ranking[T](items)`
- `Clustering[T](items)`
- `Matching[S, T](sources, targets)`
- `MatchingOne[S, T](source, targets)`
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

### Compact helpers
- `ChooseBy(items, criteria...)`
- `FilterBy(items, criteria)`
- `SortBy(items, criteria)`

Full API reference: [docs/reference/API.md](docs/reference/API.md)

## Usage Examples

### Extract typed data

```go
type Invoice struct {
    Number string  `json:"number"`
    Total  float64 `json:"total"`
}

invoice, err := schemaflux.Extracting[Invoice](rawEmail).
    Strict().
    Fast().
    Run()
```

### Transform one type into another

```go
type Lead struct {
    Name  string `json:"name"`
    Email string `json:"email"`
}

type CRMContact struct {
    FullName string `json:"full_name"`
    Email    string `json:"email"`
}

contact, err := schemaflux.Transforming[Lead, CRMContact](lead).
    Strict().
    Steer("Preserve exact identifiers and emails").
    Run()
```

### Generate typed output

```go
type ReleaseNote struct {
    Title   string   `json:"title"`
    Bullets []string `json:"bullets"`
}

note, err := schemaflux.Generating[ReleaseNote]("Write release notes for version 2.3").
    Creative().
    Smart().
    Run()
```

### Filter and sort collections

```go
urgent, err := schemaflux.Filtering(tasks).
    By("high priority tasks due today").
    Run()

ordered, err := schemaflux.Sorting(urgent).
    By("most urgent operational risk first").
    Smart().
    Run()
```

### Summarize and rewrite text

```go
summary, err := schemaflux.Summarizing(article).
    MaxLength(160).
    Run()

rewrite, err := schemaflux.Rewriting(summary).
    Tone("executive").
    Run()
```

### Validate structured data

```go
result, err := schemaflux.Validating(customer).
    Rules("email must be valid, country must be ISO alpha-2, age must be at least 18").
    Run()

if err != nil {
    panic(err)
}
if !result.Valid {
    fmt.Println(result.Issues)
}
```

### Ask typed questions over context

```go
answer, err := schemaflux.Asking[string, string](report, "What changed from last quarter?").
    Strict().
    Run()
```

## Confidence, and what it is not

Result types carry `ModelConfidence`, not `Confidence`. The number is the
model's own claim about its own answer, produced by the same process that
produced the answer being scored. It is not calibrated, it is not comparable
across models or prompts, and it is not a measurement.

Use it as a tie-breaker or a review trigger. Do not use it as a threshold that
decides whether a result is correct — a wrong answer with a stated 0.95 is a
normal event, not an anomaly. The fields renamed by this rule are
`ModelConfidence`, `ModelTrustScore`, `ModelOverallConfidence`, and
`ModelOverallScore`.

## Testing code that uses SchemaFlux

`schemafluxtest` ships a fake provider, so your tests need no API key, no
network, and no money.

```go
import "github.com/monstercameron/schemaflux/schemafluxtest"

func TestRouting(t *testing.T) {
    provider := schemafluxtest.New().
        Reply(`{"queue":"billing","priority":"high"}`)
    defer schemafluxtest.Install(t, provider)()

    result, err := schemaflux.Extract[Triage](ticket, schemaflux.NewExtractOptions())
    // ... assert on result ...

    // And on what your code actually sent:
    if !strings.Contains(provider.LastRequest().UserPrompt, "charged twice") {
        t.Error("the ticket text did not reach the model")
    }
}
```

The cases worth testing are the ones that cost money to reproduce:

- `Fail(err)` — the provider is unreachable
- `FailThen(2, err, body)` — it fails twice, then works
- `Reply(a, b, c)` — a different answer per call; the last repeats
- `Slow(30 * time.Second)` — for timeouts and cancellation; the wait is cancellable
- `WithUsage(...)` — for cost accounting
- `Requests()`, `LastRequest()`, `CallCount()` — what your code sent

`Install` calls `t.Setenv`, which makes Go fail any test that also calls
`t.Parallel`. That is deliberate: SchemaFlux resolves its provider through a
package global, so two parallel tests with different providers would silently
share one. Failing loudly beats a flake nobody can reproduce.

## Reliability

SchemaFlux treats the shared LLM path as infrastructure.

**Repair.** When an answer cannot be used — it is not JSON, it does not fit the
type, a required field is missing under `Strict()` — the failure is fed back to
the model and the request is made again with the problem named. That is
different from a retry, which sends the same request and hopes. One repair is
the default, which catches the common cases without turning one failed call
into unbounded spend; `SCHEMAFLUX_REPAIR_ATTEMPTS` changes it, and `0` disables
it.


Built in:
- automatic request IDs when missing
- retries for transient provider failures and empty completions
- fail-fast behavior for non-retryable auth and request errors
- JSON response enforcement for structured operations
- timeout control through context and client configuration
- structured error and request logging

Retry-related environment variables:
- `SCHEMAFLUX_LLM_MAX_RETRIES`
- `SCHEMAFLUX_LLM_RETRY_BACKOFF`
- `SCHEMAFLUX_TIMEOUT`
- `SCHEMAFLUX_REPAIR_ATTEMPTS`

### Rate limits

A 429 or 503 carries the wait the server wants in `Retry-After`, and that number
is used in place of the local backoff. This matters more than it sounds: the
computed backoff doubles from 500ms and stops at five seconds, so against a
provider that limits *per minute* every retry landed inside the same closed
window and the retry budget bought nothing but latency. The server's wait is
bounded at two minutes, and the caller's own context deadline still cuts it
short. When the server states no wait, the local backoff stays in charge.

Client tuning:

```go
client := schemaflux.NewClient(apiKey).
    WithRetries(3).
    WithRetryBackoff(500 * time.Millisecond).
    WithTimeout(30 * time.Second).
    WithProvider("openai")
```

## Logging

SchemaFlux uses structured logging backed by `slog`.

Environment variables:
- `SCHEMAFLUX_LOG_LEVEL=debug|info|warn|error`
- `SCHEMAFLUX_LOG_FORMAT=text|json`
- `SCHEMAFLUX_LOG_FILE=/path/to/schemaflux.log`
- `SCHEMAFLUX_LOG_BUFFER=1000`
- `SCHEMAFLUX_LOG_SOURCE=true`
- `SCHEMAFLUX_LOG_DISABLE_STDERR=true`
- `SCHEMAFLUX_LOG_DISABLE_CAPTURE=true`

Programmatic configuration:

```go
schemaflux.ConfigureLogging(schemaflux.LoggerConfig{
    Level:      schemaflux.LogDebug,
    Format:     "json",
    FilePath:   "schemaflux.log",
    BufferSize: 2000,
    Capture:    true,
})

entries := schemaflux.GetLogEntries()
fmt.Println("captured logs:", len(entries))
schemaflux.ResetLogEntries()
```

## Metrics And Cost Tracking

SchemaFlux records:
- request counts
- request durations
- prompt, completion, cached, reasoning, and total tokens when available
- prompt, completion, cached, reasoning, and total USD cost when available

Per-request cost history is tracked separately from low-cardinality aggregate metrics.

```go
import (
    "fmt"
    "time"

    "github.com/monstercameron/schemaflux/pricing"
)

summary := pricing.GetCostSummary(time.Now().Add(-1*time.Hour), map[string]string{
    "provider": "openai",
})
fmt.Printf("requests=%d avg_cost=%.6f avg_tokens=%.1f\n",
    summary.RequestCount,
    summary.AverageCostPerRequest,
    summary.AverageTokensPerRequest,
)

record, ok := pricing.GetRequestCost("req-123")
if ok {
    fmt.Printf("request cost: %.6f total tokens: %d\n",
        record.Cost.TotalCost,
        record.TokenUsage.TotalTokens,
    )
}
```

## Providers

Default provider:
- `openai` (`gpt-5.6-luna` / `-sol` / `-terra` by tier, over the Responses API)

Secondary provider:
- `cerebras` (`gemma-4-31b`, over the OpenAI-compatible chat API)

Built-in providers:
- `openai`
- `cerebras`
- `anthropic`
- `openrouter`
- `deepseek`
- `qwen`
- `zai`
- `local`

```go
client := schemaflux.NewClient(apiKey)

client.WithProvider("openai")
client.WithProvider("anthropic")
client.WithProvider("deepseek")
client.WithProvider("qwen")
client.WithProvider("zai")
client.WithProvider("local")
```

Provider notes:
- `anthropic` uses the native Anthropic Messages API
- `deepseek`, `qwen`, `zai`, `openrouter`, and `cerebras` use the shared OpenAI-compatible provider path
- provider-specific env vars are supported, including `CEREBRAS_API_KEY`, `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`, `DASHSCOPE_API_KEY`, and `ZAI_API_KEY`

### Cerebras

```go
client := schemaflux.NewClient("").WithProvider("cerebras") // key from CEREBRAS_API_KEY
schemaflux.SetDefaultClient(client)

claim, err := schemaflux.Extract[ExpenseClaim](text, schemaflux.NewExtractOptions())
```

Nothing else about the call changes. `Extract[T]` sends the same strict JSON
schema it sends to OpenAI, and Cerebras enforces it with constrained decoding —
the guarantee is the provider's, not the prompt's. Runnable examples are
`Example_secondaryProvider` and `Example_secondaryProviderEnforcesTheSchema`.

Two things are worth knowing:

- **All three tiers map to `gemma-4-31b`.** The tiers trade accuracy for
  latency, and there is no cheaper Cerebras sibling whose accuracy loss buys
  anything here. Mapping `Quick` to a smaller model would be inventing a
  trade-off no benchmark has shown.
- **Schema annotations are dropped.** Cerebras rejects `format`, `pattern`,
  `minItems`, `minimum`, and their neighbours outright rather than ignoring
  them, so a `time.Time` field — which the generator annotates with
  `format: date-time` — would fail the whole request. The transport strips them.
  They are annotations on top of a type, never the type itself, so nothing the
  Go type enforces on unmarshal is lost.

Free-tier keys are limited to **5 requests/minute**; paid keys to 500. Either
way a 429 is waited out using the server's own `Retry-After`, not a local
backoff — see the resilience note below.

Custom provider registration:

```go
schemaflux.RegisterProviderFactory("myvendor", func(cfg schemaflux.ProviderConfig) (schemaflux.Provider, error) {
    cfg.BaseURL = "https://vendor.example.com/v1"
    return schemaflux.NewOpenAICompatibleProvider("myvendor", cfg)
})

client := schemaflux.NewClient("").
    WithProviderConfig("myvendor", schemaflux.ProviderConfig{
        APIKey: "vendor-key",
    })
```

Default OpenAI intelligence mapping:
- `Smart -> gpt-5.4`
- `Fast -> gpt-5-mini`
- `Quick -> gpt-5-nano`

Overrides:
- `SCHEMAFLUX_MODEL`
- `SCHEMAFLUX_MODEL_SMART`
- `SCHEMAFLUX_MODEL_FAST`
- `SCHEMAFLUX_MODEL_QUICK`

## Environment

Common environment variables:
- `SCHEMAFLUX_API_KEY`
- `OPENAI_API_KEY`
- `SCHEMAFLUX_PROVIDER`
- `SCHEMAFLUX_TIMEOUT`
- `SCHEMAFLUX_MODEL`
- `SCHEMAFLUX_MODEL_SMART`
- `SCHEMAFLUX_MODEL_FAST`
- `SCHEMAFLUX_MODEL_QUICK`

### Credential resolution

`Init` and `InitWithEnv` take the first credential they find, in this order:

1. the key passed to `Init(key)`, if it is not empty
2. `SCHEMAFLUX_API_KEY`
3. the provider-specific name — `SCHEMAFLUX_OPENAI_API_KEY`, `SCHEMAFLUX_ANTHROPIC_API_KEY`, and so on
4. the provider's own conventional name — `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`
5. for OpenAI only, the bare `OPENAI`, which is a common spelling in `.env` files

If none of them resolves, `Init` returns an error rather than falling through to
the mock provider. The mock answers every operation with `Mock response for: …`,
which parses into zero-valued structs and is indistinguishable from a working
deployment until someone reads the output. To use it deliberately, set
`SCHEMAFLUX_PROVIDER=local`, or call `NewClient("").WithMockProvider()`.

Both functions return an error. Check it:

```go
if err := schemaflux.InitWithEnv(); err != nil {
    log.Fatal(err)
}
```

An operation run before a provider exists reports what to do about it, but that
is a fallback — the error from `Init` is the one that says why.

### `.env` files

`InitWithEnv()` with no arguments loads `./.env` when it exists, and is not an
error when it does not. `InitWithEnv("config/dev.env", "config/local.env")`
loads exactly those paths, in order, and reports a path that does not exist.

**A `.env` file supplies defaults; it does not override the process
environment.** A variable already exported in the shell keeps its value, so
`SCHEMAFLUX_API_KEY=… go run ./cmd/app` behaves as expected regardless of what
the file contains.

## Design Notes

- The root package is the public facade for downstream Go projects.
- Builder implementation lives under `internal/` so the exported API can evolve without exposing internal plumbing.
- Prefer collection-aware builders when they exist instead of scattering raw single-call logic across application code.
- The local provider is useful for tests and smoke runs, not for proving semantic correctness.

## Documentation

- [API reference](docs/reference/API.md)
- [Examples](examples/)
- [Production backlog](docs/engineering/backlog/PRODUCTION_TODO.md)

## Compatibility

The older direct-call function API still exists for existing consumers, but it is compatibility-only. New code should use the fluent builders shown here.

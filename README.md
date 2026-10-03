<p align="center">
  <img src="design/branding/schemaflux-poster.webp" alt="SchemaFlux — structured AI that ships" width="820">
</p>

# SchemaFlux

**Structured AI that ships.**

SchemaFlux is a Go library for typed LLM operations — it makes a language model return a typed Go
value instead of a wall of text you have to parse.

It gives you a single public API built around fluent request builders so application code stays readable while retries, structured output contracts, logging, metrics, and cost tracking stay centralized.

→ **[Read the case study](https://www.earlcameron.com/projects/schemaflux)** — what it does, what it
is built on, and what was hard.

**Status: v1.1.0.** The public API listed in `testdata/api_surface.txt` is
stable under the deprecation policy below — a snapshot test fails the build if
it changes, so an addition or a removal is a reviewed decision rather than a
side effect. Every behaviour claim in this README is backed by a test in the
repository.

Twelve of the project's own 32 acceptance criteria were not met at 1.0. They are
listed with a reason each in `TODOS.md`, and
[ADR 0005](docs/adr/0005-shipping-1-0-with-twelve-unmet-criteria.md) says why
the version ships anyway. The short version is in
[What 1.0 does not include](#what-10-does-not-include) below; read it before
depending on this for anything load-bearing.

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

### Data reshaping and comparison
- `Deduplicate(items, threshold)` — collapse semantically duplicate items,
  keeping your own values. Note the cost: it asks the model about pairs, so it
  is O(n²) calls in the worst case.
- `Merge(sources, strategy)` / `MergeWithMetadata` — combine several records
  into one.
- `Format(data, template)` / `FormatWithMetadata` — render a value through a
  natural-language template.

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

## What the type contract covers

`Extract[Person]` gives you a `Person`. What that guarantees depends on the mode
and on what you have told the library about your type, and the distinctions are
worth knowing because they are the ones people assume away.

**Required fields.** Say so with a tag:

```go
type Applicant struct {
    Email  string  `json:"email"  schemaflux:"required"`
    Middle *string `json:"middle"`                       // pointers are optional
    Notes  string  `json:"notes,omitempty"`              // legacy: omitempty still means optional
}
```

Requiredness used to be inferred from `omitempty` alone, which is a
*serialization* directive — it tells `encoding/json` to leave a zero value out
of the output. Reading it as "optional" cut both ways: adding `omitempty` to
keep your JSON tidy silently made a field optional to `Strict()`, and a field
you meant to be optional was required unless you knew the convention. The tag
is explicit; the type is the next signal, since a pointer can be nil; and
`omitempty` remains the fallback so existing types behave as they did.

The same answer drives all three descriptions of the contract: the schema in the
prompt, the JSON Schema sent to a provider that enforces one, and the check
applied to the answer.

**Modes.** `Strict()` requires every required field and validates recursively.
`Transform()` infers what it reasonably can. `Creative()` reads the source as
generously as possible — and does **not** invent: it used to instruct the model
to "generate plausible values for missing fields", on the one operation whose
purpose is faithfulness to a source. It now says the opposite out loud.

**Confidence floors are enforced where a confidence is reported.** `Classify`,
`Verify`, and `Derive` refuse a result the model itself scored below the floor
you set — the option used to reach the prompt and never be read back, on
operations whose defaults are 0.5, 0.6, and 0.7. `Filter` has no per-item score
in its response, so its `MinConfidence` is documented as an instruction rather
than a threshold.

**Validation decides locally what it can.** `Validate`'s field rules are checked
in Go when they are the kind of thing Go can check exactly:

```go
result, err := schemaflux.Validate(applicant, schemaflux.NewValidateOptions().
    WithFieldRules(map[string]string{
        "email":   "email",
        "country": "iso3166-alpha2",
        "age":     "min:18",
        "tier":    "should sound premium",   // natural language: goes to the model
    }))
```

`email`, `url`, `uuid`, `iso3166-alpha2`, `nonempty`, `min`, `max`, `minlen`,
`maxlen`, `oneof:a|b|c`, and `regex:...` are decided locally. Anything else is
passed through. If every rule is decidable there is **no provider call at all**,
and the local findings survive the model's answer — a model that says "valid"
does not overrule `mail.ParseAddress`.

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

**Prompt reinforcement is on by default and can be turned off.** Every request
carries a short block telling the model to do the task rather than restate the
schema, and to answer with JSON and nothing else. It costs about 53 tokens on a
text request and 117 on a JSON one, on every call. Set
`SCHEMAFLUX_PROMPT_REINFORCEMENT=0` to stop paying for it — worth doing only if
you have measured your own prompts without it, because whether it improves
answers is a question about your model and your task, not one this library can
answer for you.

Retry-related environment variables:
- `SCHEMAFLUX_LLM_MAX_RETRIES`
- `SCHEMAFLUX_LLM_RETRY_BACKOFF`
- `SCHEMAFLUX_TIMEOUT`
- `SCHEMAFLUX_REPAIR_ATTEMPTS`

### Provider failures

A non-200 arrives as a typed `*schemaflux.APIError`, so you branch on the
status rather than on an English sentence:

```go
if status, ok := schemaflux.StatusCodeFrom(err); ok && status == 400 {
    // your schema or your request is wrong; retrying will not help
}

var apiErr *schemaflux.APIError
if errors.As(err, &apiErr) && apiErr.Retryable() {
    // transient
}
```

`Error()` prints the status, the model, how long a throttled answer asked you to
wait, and the vendor's `type`, `code` and `param`, which are fixed words. It does
**not** print the vendor's `message` or the raw response body, and says how long
each was. A provider's message can quote your input back at you (a content filter
naming the passage it objected to, a validation error echoing the field it could
not parse), and a gateway's can quote the key it rejected; this library exists to
run invoices, tickets and notes through models, and that text does not belong in
your logs by default. The same holds however the error is printed: with `%v` or
`%#v`, through `log/slog`, or marshalled as JSON. Both are kept on the value, with
the provider's own key already removed: read `apiErr.Message`, `apiErr.Body` or
`apiErr.Detail()`, or set `SCHEMAFLUX_ERROR_DETAIL=1` while debugging against data
you own.

### Rate limits

A 429 or 503 carries the wait the server wants in `Retry-After`, and that number
is used in place of the local backoff, unjittered — the point of honouring a
number the server gave you is lost if you then randomise it. This matters more
than it sounds: the local backoff is capped at five seconds, so against a
provider that limits *per minute* every retry landed inside the same closed
window and the retry budget bought nothing but latency. The server's wait is
bounded at two minutes, and the caller's own context deadline still cuts it
short. A `Retry-After` that cannot be parsed, or names a time already past,
counts as *the server did not say* rather than as a wait of zero. When the
server states no wait, the local backoff stays in charge.

Client tuning:

```go
client := schemaflux.NewClient(apiKey).
    WithRetries(3).
    WithRetryBackoff(500 * time.Millisecond).
    WithTimeout(30 * time.Second).
    WithProvider("openai")
```

### The retry contract

If you are embedding this library inside an application that does its own
retrying, these are the parts you can code against. They are stated here
because an embedder needs to know where this library's retrying stops and
theirs begins, and because two layers that both retry multiply.

**Attempts are bounded, always.** A call makes at most `retries + 1` requests.
The default is three retries, so four requests. Nothing in the retry path is
unbounded and nothing escalates on its own.

**`WithRetries(0)` means zero.** Not "zero, so use the default" — zero
requests beyond the first. That distinction was a real defect once: the
dispatcher tested `maxRetries <= 0` and substituted the global default of
three, so retries could not be turned off at all and a caller who asked for
none waited out two backoffs anyway. An embedder that does its own retrying
depends on being able to switch this one off, so it is pinned by a test
(`internal/ops/retry_policy_test.go`).

**A custom provider states its own policy, and the three cases are distinct.**
`Provider.RetryPolicy() (int, time.Duration)` is asked once per call:

| Returned | Meaning |
| --- | --- |
| `n > 0` | exactly `n` retries |
| `0` | no retries; the request is made once |
| `< 0` | no opinion — the library's default applies |

Note that `Client.WithRetries` clamps a negative to zero while a provider
returning a negative gets the default. They are different questions: a caller
setting a ceiling of -1 means "none", and a provider that has no policy to
declare means "you decide".

**The backoff is jittered, and the jitter is decorrelated.** Each wait is
uniformly random between the base backoff and three times the *previous* wait,
capped at five seconds. It used to be a plain exponential ladder computed from
the attempt number, which meant every caller retrying the same failure against
the same base backoff retried at the same instant — a provider coming back
from an outage met all of them in one synchronised wave. Correlating each wait
with the previous one rather than with the attempt number is what breaks the
wave up.

**Cancellation and deadlines are never retried, and never slept through.** A
context that is already done ends the call rather than starting an attempt
that cannot succeed, and a backoff in progress is cut short the moment the
context ends — a five-retry budget with an hour of backoff returns
immediately on a cancelled context rather than in five hours. The error
arrives classified as `canceled` or `timeout`, distinct from any provider
failure.

**One place decides what is retryable.** `internal/llm/classify.go` maps a
failure to a `types.ErrorKind`, and the kind decides. There is deliberately no
second opinion anywhere in the stack: an unrecognised error is retried rather
than failed fast, because "nobody has taught the taxonomy this shape yet" is
not evidence that the failure is permanent, and the attempt budget already
bounds what being wrong about it costs.

**Repair is not retry, and has its own budget.** A retry sends the same
request again; a repair sends a new one naming what was wrong with the
answer. They are counted and configured separately (`SCHEMAFLUX_REPAIR_ATTEMPTS`),
and both appear in `Meta` — `Attempts` counts requests, `Repairs` counts the
ones that were corrections.

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

**Unpriced is not free.** A model with no entry in the pricing table reports
`Priced: false` and a zero cost, and those two things together mean "unknown".
They are not the same as a zero cost with `Priced: true`, which would mean free.
Check the flag before you add the numbers up:

```go
if !record.Cost.Priced {
    // This call's cost is unknown, not zero. RegisterPricingModel, or exclude it.
}
```

No model is ever priced at another model's rates. That substitution is what
made an Opus call report Haiku's price — roughly 60x under — while presenting
it as a precise figure. A dated snapshot (`gpt-5-2026-01-15`) still prices from
its base model; a version bump (`gpt-5.6-luna`) does not, because it is a
different model.

**The history is bounded** at ten thousand records, oldest evicted;
`SetCostHistoryLimit` changes it. `GetRequestCost` is a lookup rather than a
scan. Aggregates describe the records still retained.

**Budgets alert on an edge, and can enforce.** `SetBudget` fires its callback
once per threshold crossing per period rather than on every request past 80%.
`SetBudgetEnforcement(true)` makes an exhausted budget refuse calls *before*
they are made, returning `pricing.ErrBudgetExceeded`; it is off by default,
because budgets have been advisory here since the library shipped.

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
- `openai` (`gpt-6-luna` on every tier, over the Responses API)

Which request parameters a model accepts is decided by a table in the library, by
model name. `gpt-5`, `gpt-6` and the o-series reject `temperature`, so it is not sent
to them. A name the table does not know is not sent `temperature` on OpenAI's own
endpoint, and is sent it on any other endpoint, where an unknown name is usually a
local model that accepts it.

The one case the table cannot see is an alias: an Azure deployment or a proxy route
that gives a `gpt-6` model another name. Requests to it fail with "Unsupported
parameter: 'temperature'". Set `OmitTemperature` on the provider configuration for
that endpoint. It is a field of `ProviderConfig` only: there is no environment
variable for it, so an endpoint configured through `SCHEMAFLUX_OPENAI_BASE_URL`
alone has to be given a configuration to set it.

```go
client := schemaflux.NewClient(apiKey).WithProviderConfig("openai", schemaflux.ProviderConfig{
    APIKey:          apiKey,
    BaseURL:         "https://my-resource.openai.azure.com/openai/v1",
    OmitTemperature: true,
})
```

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

### Codex CLI — a subscription login, no API key

`codex` runs operations through the [Codex CLI](https://developers.openai.com/codex/cli)
using whatever login `codex login` already established. There is no API key
anywhere in the path.

```go
import "github.com/monstercameron/schemaflux/codex"

provider, err := codex.New(codex.Options{Model: "gpt-5.6-sol"})
if err != nil {
    return err // the CLI is not installed, or not on PATH
}
client := schemaflux.NewClient("").WithProviderInstance(provider)
```

Or make it selectable from configuration, which is what a host embedding this
library usually wants:

```go
codex.Register()
client := schemaflux.NewClient("").WithProvider("codex")
```

It is a separate package for the same reason `telemetry/otel` is: a consumer
who does not import it gets no process-spawning code path and no new public
API. Registration is an explicit call rather than an `init()`, so a provider
name never appears in the registry as a side effect of an import.

Four things are worth knowing, and the first two are the point:

- **API-key variables are removed from the child process.** `OPENAI_API_KEY`,
  `OPENAI_KEY`, and `CODEX_API_KEY` are stripped before the CLI is started, so
  it cannot quietly fall back to key-based authentication. That fallback is
  worth engineering against precisely because it *succeeds*: the answers come
  back, an account nobody chose gets billed, and nothing in the result says
  which identity paid. `CODEX_HOME` is preserved — it is where the login lives.
- **Every call is unpriced, which is not the same as free.** The token counts
  are real and come from the CLI's own `turn.completed` event, but they are
  drawn against a flat-rate plan rather than invoiced per token, so multiplying
  them by any rate card produces a number that is not the bill. `Meta.Cost`
  reports `Priced: false`. A zero would be indistinguishable from a free call
  and would understate spend silently.
- **The model has to be named.** There is no tier mapping for `codex`: set
  `SCHEMAFLUX_MODEL`, pin one on the call, or pass `Options.Model`. Which models
  a login may run depends on the CLI's version and the caller's subscription,
  and a built-in default here would be this library asserting something it
  cannot know.
- **There is one prompt channel, not two.** `codex exec` takes a single task on
  stdin and has no system role, so the operation's instructions and your data
  arrive in one stream where a chat API would keep them separate. The library's
  own trust boundary still applies upstream — steering is delimited before it
  reaches any provider — but the role separation an API gives for free is not
  available here.

The prompt is written to the child's **stdin**, never to its argument list, so
it cannot be read out of a process listing or echoed back by a CLI that prints
its own invocation. When a turn fails, the CLI's message and its stderr are
both withheld from the error string and reachable through `Detail()` or
`SCHEMAFLUX_ERROR_DETAIL=1`: the one gate that every provider's message and
response body sit behind.

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

## Repository layout

```
schemaflux.go        the only file at the root: the public API
tests/               the black-box suite — package tests
internal/api/client  the Client and catalogue, with their own tests
internal/api/fluent  the builders
internal/            the rest of the implementation, private by compiler rule
internal/testfixtures  doubles shared between the test packages
mw/                  optional middleware (cache, retry, circuit breaker)
pricing/             cost calculation and budgets
telemetry/           logging, metrics, tracing seams
schemafluxtest/      fakes, recorder, and cassette replay for testing YOUR code
cmd/                 a live smoke command
examples/            45 runnable examples, plus smarttodo (its own module)
```

One file at the root, and it has to be there: for a Go module the root package is
what `import "github.com/monstercameron/schemaflux"` resolves to, so the public
API cannot live anywhere else. Moving it under `pkg/` would rename the import to
`.../schemaflux/pkg` and break every consumer.

Everything testable moved to where its tests can reach it. The `Client` and the
catalogue are in `internal/api/client` with their own tests beside them, because
Go requires a package's tests to sit in its directory and those tests reach
unexported state. `schemaflux.Client` is a **type alias** for that package's
type, not a wrapper, so it is the same type to the compiler and every method
comes across without being re-declared.

That alias has one consequence worth knowing: `testdata/api_surface.txt`
snapshots declarations found in the root directory, so it can no longer see the
client's fifteen methods. They are pinned instead by an interface assertion in
`tests/client_methods_test.go`, which is a stronger guarantee — removing or
re-typing one breaks the build rather than a golden-file comparison, and it
cannot be silenced by regenerating a snapshot.

Fixtures shared between the two test packages live in `internal/testfixtures`
rather than being copied into both, which is how two fixtures meant to be
identical drift apart and start proving different things. That is distinct from
`schemafluxtest`, which exists for consumers testing code built on this library:
one may know things about the implementation, the other must not.

## Design Notes

- The root package is the public facade for downstream Go projects.
- Builder implementation lives under `internal/` so the exported API can evolve without exposing internal plumbing.
- Prefer collection-aware builders when they exist instead of scattering raw single-call logic across application code.
- The local provider is useful for tests and smoke runs, not for proving semantic correctness.

## What 1.0 does not include

The version number is a compatibility promise about the public Go API. It is
not a claim that everything this project set out to build is finished. What is
missing, honestly:

**Two things cannot be verified on the maintainer's machine.** `-race` does not
run on windows/arm64, so the race and full-CI-gate criteria are *unverified*
rather than unmet — `-shuffle=on` is used throughout and is genuinely weaker, as
it reorders tests without instrumenting memory access. The CI matrix covers
ubuntu, macos, and windows; a green run there is what closes these.

**Only OpenAI is live-verified.** Its smoke test ran across three models and the
responses are committed as cassettes, so those assertions replay for free. The
other six registered providers — anthropic, openrouter, cerebras, deepseek,
qwen, zai — have never been called against their real endpoints. They are
implemented and unit-tested; they are not proven.

**Five operations have not been lowered onto the `Op` descriptor.** Question,
Predict, Cluster, Compress, and Decompose still call the provider directly, so
they declare no invariants and no batch algebra, and contract negotiation does
not reach them. Everything else does.

**Process-wide state still exists.** A `Client` owns an immutable per-call
snapshot — provider, budget, scheduler, data policy — so two clients no longer
interfere. But `ops.defaultProvider` remains for callers who never build a
client, and the observer and cache policy are still process-wide.

**Evidence does not join to lineage.** A claim resolves to the result that
produced it, and separately to a source span, but nothing connects the two: a
three-stage pipeline does not resolve back to a byte range in the original
input.

**No alert definitions ship.** Runbooks for twelve incident classes do, and they
name the dashboard panels, but alerts are deployment-specific and this library
ships no deployment.

## Documentation

- [API reference](docs/reference/API.md)
- [Architecture decisions](docs/adr/) — why the awkward choices are the way they are
- [Examples](examples/)
- [Task list](TODOS.md) — what is being built, in what order, with the evidence for what is done

## Compatibility

**Both API styles are supported, and neither is deprecated.** The direct
functions (`schemaflux.Extract[T](input, opts)`) are the canonical semantics:
easiest to wrap, to test, and to construct options for dynamically. The fluent
builders (`schemaflux.Extracting[T](input).Strict().Run()`) are the ergonomic
surface. Every fluent call lowers to the same options and the same execution,
and a test asserts they send identical requests.

This corrected an earlier claim here that the direct API was
"compatibility-only". It was not true — most of this README's own examples use
it — and saying so while shipping no `// Deprecated:` marker anywhere meant no
linter, IDE, or `staticcheck` run would have told a user they were on a legacy
path even if they had been.

What *is* deprecated carries the marker, so your tools will say so:
`ValidateLegacy` (use `Validate`, which reports which field failed and how
badly) and `QuestionLegacy` (use `Question`).

### Breaking changes, and what to do about them

These are behaviour changes that a recompile will not always catch. Each says
what broke and what to write instead.

**`Mode` and `Speed` renumbered so zero means unset.** `Strict` was `Mode(0)`
and `Smart` was `Speed(0)`, and every option merge tests `if user.Mode != 0` —
so on about ten operations there was no value you could pass that meant Strict
or Smart. `ModeUnset` and `TierUnset` occupy zero now.

- If you compared a `Mode` or `Speed` to an untyped `0`, compare it to
  `schemaflux.ModeUnset` or `schemaflux.TierUnset` instead.
- If you relied on a zero-valued options struct meaning Strict and Smart, say so
  explicitly. An unset field now takes the operation's own default.

**`Classify`'s category type is constrained to `~string`.** The category came
back as a string and was converted through a JSON round trip, so
`Classify[Ticket, Priority]` with `type Priority int` compiled and failed at run
time. It is a compile error now. Use a string-kinded type:

```go
type Priority string    // was: type Priority int
```

**`ClassifyResult` gained `Categories []C`.** Additive. `MultiLabel` and
`MaxCategories` previously changed the prompt and had nowhere to put an answer;
`MaxCategories` is now enforced, so a model returning more labels than you asked
for is an error rather than a longer list.

**`Complete` and `CompleteField` no longer take a provider.** They were the only
operations that did. Drop the argument; the client's provider is used, as with
every other operation.

**`Redact` takes typed options.** `Redact[T](input, opts ...interface{})` became
`...RedactOptions`. Passing the wrong struct used to substitute the defaults
silently; it is a compile error now.

**Confidence floors are enforced.** `Classify`, `Verify`, and `Derive` return an
error when the model's own reported confidence is below `MinConfidence`, whose
defaults are 0.5, 0.6, and 0.7. If you were relying on low-confidence results
coming back, set `WithMinConfidence(0)`.

**`Summarize` checks `TargetLength`.** A summary more than 20% over the target
is an error rather than a longer summary.

**`ResetCostTracking` no longer clears budget configuration.** It used to null
the limits and the callback, so clearing history silently disabled spend
alerting. Use `ResetBudget` for that.

**The library no longer sets tracing up for you.** `telemetry/otel.InitTracing`,
`ShutdownTracing` and the variables they read (`SCHEMAFLUX_OTLP_ENDPOINT`,
`SCHEMAFLUX_JAEGER_ENDPOINT`, `SCHEMAFLUX_TRACE_SAMPLE_RATE`,
`SCHEMAFLUX_EXPORT_TRACES_STDOUT`, `SCHEMAFLUX_ENVIRONMENT`) are gone, with the
older helpers `StartSpan`, `RecordLLMCall`, `AddSpanTags`, `RecordSpanEvent`,
`ExtractTraceContext` and `InjectTraceContext`. Build your own OpenTelemetry
tracer provider, with your exporter, sampler and resource, configured by
OpenTelemetry's own `OTEL_*` variables, and hand it over:

```go
restore, err := otel.Install(tracerProvider) // github.com/monstercameron/schemaflux/telemetry/otel
defer restore()
```

One call of an operation is then a span named `schemaflux.<operation>`, and each
request it makes to a provider is a child span named by the OpenTelemetry
generative-AI convention (`chat <model>`), with the `gen_ai.*` attributes, a
retry or a hedged copy as an event, and a repair as a second request marked as
one. A failed operation is an error on its own span, with `error.type` holding
the library's error kind. No span carries a prompt, an input, an answer or a
key. The package's documentation lists every attribute and has a runnable
example. A program that only calls `Install` no longer links gRPC and protobuf.

**`Init` returns an error for a provider it cannot resolve a model for.** It
always had the error return; it now uses it. `WithProvider("qwen")` without
`SCHEMAFLUX_MODEL` reports at construction instead of failing on the first call.

**`WithContext` is `WithBackground` on four options types.** `CompleteOptions`,
`InferOptions`, `DiffOptions`, and `ExplainOptions` each carried a prose
`Context` field beside the embedded `context.Context` — two different things
through one selector, one a deadline and the other prompt material. Rename the
call; the meaning is unchanged.

**Cost history is bounded.** Ten thousand records by default, oldest evicted;
`SetCostHistoryLimit` changes it. Aggregates describe the retained history.

# SchemaFlux v2 API reference

SchemaFlux v2.0.1 requires Go 1.27 or later. The module lives in the repository's `v2/` directory and keeps the `/v2` import path:

```bash
go get github.com/monstercameron/schemaflux/v2@v2.0.1
```

```go
import schemaflux "github.com/monstercameron/schemaflux/v2"
```

V1 releases keep the unsuffixed import path. The v1 fluent builders are not part of v2. See the [migration table](../../README.md#migrating-from-v1) for the most common replacements.

## Calling an operation

Every operation accepts a `context.Context` when it runs. Pass a typed options value for operation-specific settings; its embedded `schemaflux.Options` carries shared settings. Omitting options uses the operation's defaults.

```go
type Person struct {
    Name string `json:"name"`
    Age  int    `json:"age"`
}

person, err := schemaflux.Extract[Person](
    ctx,
    "Ada is 36 years old",
    schemaflux.ExtractOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}},
)
```

The root package includes these call shapes:

| Task | Function and result |
| --- | --- |
| Structured extraction | `Extract[T](ctx, input, opts...) (T, error)` |
| Structured transformation | `Transform[U](ctx, input, opts...) (U, error)` |
| Text summary | `Summarize(ctx, text, opts...) (string, error)` |
| Choose from offered values | `Choose[T](ctx, values, opts...) (T, error)` |
| Rank offered values | `Rank[T](ctx, values, query, opts...) (RankResult[T], error)` |
| Validate a value | `Validate[T](ctx, value, opts...) (ValidateResult[T], error)` |
| Classify into categories | `Classify[C](ctx, input, categories, opts...) (ClassifyResult[C], error)` |

Other root operations include `Generate`, `Infer`, `Parse`, `Rewrite`, `Translate`, `Filter`, `Sort`, `Cluster`, `Is`, `Rate`, and `Which`. Consult [the root source](../../) and [runnable examples](../../examples) for exact signatures and options. Experimental operations live in [`exp`](../../exp).

`Options.Tier` chooses a quality and speed class when no exact model is pinned; `Options.Model` pins a model. `Options.Steering` guides the model but is not a checked guarantee. Strict mode and operation invariants can reject an answer that has the wrong shape or violates a checked rule.

## Client and provider

The zero `Client` resolves its provider and model from the environment at call time. For explicit configuration, create a provider and put it on a client:

```go
p, err := provider.New("openai", provider.Config{APIKey: key})
if err != nil {
    return err
}
client := schemaflux.Client{Provider: p, Model: "gpt-6-luna"}
ctx = schemaflux.WithClient(ctx, client)
```

This snippet also imports `github.com/monstercameron/schemaflux/v2/provider`. `WithClient` attaches a client snapshot to one context; `SetDefault` changes the process default. A client can carry a timeout, budget, scheduler, data policy, recorder, and logger. If no usable credential is configured for the default provider, a call fails with `ErrConfiguration` before sending a request.

`provider.New` supports OpenAI, Anthropic, OpenRouter, Cerebras, DeepSeek, Qwen, Z.ai, and typed-decision providers. A custom adapter implements `schemaflux.Provider`. See [provider source](../../provider) for configuration and supported names.

## Results, errors, and records

An operation returns its answer or a classified `*schemaflux.Error`. Use `errors.Is` with sentinels such as `ErrInvalidAnswer`, `ErrPolicy`, `ErrBudgetExceeded`, `ErrRateLimited`, and `ErrTimeout`; do not parse error text.

```go
var recorder schemaflux.Recorder
ctx = schemaflux.WithRecorder(ctx, &recorder)
answer, err := schemaflux.Summarize(ctx, article)
records := recorder.Records()
```

`Record` captures available request counts, timing, token usage, cost, contract level, checks, and provenance separately from the answer. A schema-conforming value does not prove that its claims are true. Inspect the delivered contract level and apply domain checks where the distinction matters. The [README](../../README.md#results-errors-and-records) describes the error and record boundaries in more detail.

## Streaming and packages

`StreamSummarize`, `StreamRewrite`, `StreamTranslate`, and `StreamExpand` return a `TextStream`. Read it with `All` or `Text`, and call `Cancel` when abandoning it. Current middleware wrappers do not forward streams; use an unwrapped streaming provider for these calls.

| Package | Purpose |
| --- | --- |
| [`chat`](../../chat) | Conversation sessions and caller-run tools |
| [`flow`](../../flow) | Decisions, branches, checkpoints, and parallel work |
| [`op`](../../op) | Typed descriptors, plans, stages, and batch runners |
| [`exp`](../../exp) | Experimental operations |
| [`redact`](../../redact) | Local value and model-assisted text redaction |
| [`mw`](../../mw) | Provider middleware |
| [`pricing`](../../pricing) | Prices and spend limits |
| [`telemetry`](../../telemetry) | Observer events, metrics, and OpenTelemetry adapter |
| [`schemafluxtest`](../../schemafluxtest) | Scripted providers and offline test helpers |

For an end-to-end setup and the v1-to-v2 migration steps, start with the [README](../../README.md).

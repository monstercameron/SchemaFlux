<p align="center">
  <img src="design/branding/schemaflux-poster.webp" alt="SchemaFlux — structured AI that ships" width="820">
</p>

# SchemaFlux

**Structured AI that ships.** SchemaFlux turns language-model work into typed Go calls. It builds a request, checks the answer against the requested type and the operation's rules, and returns the value or a classified error. A `Recorder` keeps execution details separate from the answer.

This README describes SchemaFlux v2.0.0. The module path is `github.com/monstercameron/schemaflux/v2`, with its packages at the repository root. V1 releases remain at the unsuffixed module path. The migration notes below describe the new API.

→ [Read the case study](https://www.earlcameron.com/projects/schemaflux)

## Requirements and installation

SchemaFlux v2 requires Go 1.27 or later. Install the v2.0.0 release with:

```bash
go get github.com/monstercameron/schemaflux/v2@v2.0.0
```

For the default OpenAI provider, set `SCHEMAFLUX_OPENAI_API_KEY` or `OPENAI_API_KEY`. `SCHEMAFLUX_API_KEY` is a fallback. If no usable key is set, the first call returns `ErrConfiguration`; it does not send a request or invent an answer. Set `SCHEMAFLUX_PROVIDER` to select another built-in provider. See [`provider`](provider) for names and provider-specific variables.

## First call

```go
package main

import (
    "context"
    "fmt"

    schemaflux "github.com/monstercameron/schemaflux/v2"
)

type Person struct {
    Name string `json:"name"`
    Age  int    `json:"age"`
}

func main() {
    person, err := schemaflux.Extract[Person](
        context.Background(),
        "Ada is 36 years old",
        schemaflux.ExtractOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}},
    )
    if err != nil {
        panic(err)
    }
    fmt.Printf("%s is %d\n", person.Name, person.Age)
}
```

Every operation takes a `context.Context` at the call. An operation with its own settings takes a typed options value that embeds `schemaflux.Options`; operations without extra settings take `schemaflux.Options`. Several options values merge from left to right. The zero value chooses the operation's documented defaults. `Tier` chooses a quality and speed class when no exact model is pinned; `Model` pins one. `Steering` is guidance to the model, not a checked guarantee.

The root package includes `Extract`, `Transform`, `Generate`, `Infer`, `Parse`, text operations such as `Summarize`, `Rewrite` and `Translate`, collection operations such as `Choose`, `Filter`, `Sort` and `Cluster`, and judgment operations such as `Is`, `Rate`, `Which` and `Validate`. Functions that return just a value have `Detail` forms where a structured result matters. Operations still under experimentation live in [`exp`](exp).

## Client and provider

The zero `Client` is usable: it resolves the provider and model from the environment when a call starts. For explicit configuration, build a provider and put it in a client:

```go
p, err := provider.New("openai", provider.Config{APIKey: key})
if err != nil {
    return err
}
client := schemaflux.Client{Provider: p, Model: "gpt-6-luna"}
ctx := schemaflux.WithClient(ctx, client)
result, err := schemaflux.Extract[Invoice](ctx, invoiceText)
```

This example also imports `github.com/monstercameron/schemaflux/v2/provider`. `WithClient` keeps a snapshot of the client on the context; `SetDefault` changes the process default for calls without one. Configure a client before sharing it across goroutines. `Client.Warm(ctx)` checks its configuration and opens a provider connection without sending a prompt. The client can also carry a timeout, retry and repair settings, a `Budget`, a `Scheduler`, a `DataPolicy`, a `Recorder`, and a `slog.Logger`.

`provider.New` builds OpenAI, Anthropic, OpenRouter, Cerebras, DeepSeek, Qwen, Z.ai, and the supported typed-decision providers. `provider.OpenAICompatible` connects to a server using the chat-completions dialect. A custom provider implements `schemaflux.Provider`. The optional [`provider/codex`](provider/codex) adapter uses a local Codex login and must be chosen explicitly. A client cannot use a missing credential as an offline provider.

## Results, errors, and records

An operation's return value is the answer. A failed or unusable answer returns a `*schemaflux.Error` with a `Kind`, such as `ErrInvalidAnswer`, `ErrPolicy`, `ErrBudgetExceeded`, `ErrRateLimited`, or `ErrTimeout`. Use `errors.Is(err, schemaflux.ErrInvalidAnswer)` or `errors.As` rather than matching message text. Error and log messages omit caller and provider payloads. `Error.Detail()` is for a developer inspecting data they are allowed to see.

```go
var recorder schemaflux.Recorder
ctx = schemaflux.WithRecorder(ctx, &recorder)
answer, err := schemaflux.Summarize(ctx, article)
records := recorder.Records()
_ = answer
_ = err
_ = records
```

A `Record` reports the operation, route, request count, timing, usage, cost, contract, checks, and provenance available for that call. A refused call that sent no request also gets a record. A client can keep a standing recorder; set its `Handle` callback or reset it so records do not accumulate indefinitely. A `DataPolicy` can restrict providers, regions, retention, caching, and the minimum delivered contract. A call that cannot meet it fails before sending data.

A schema-conforming value is not proof that its claims are true. SchemaFlux distinguishes prompt-only, schema-constrained, invariant-checked, evidence-checked, and more governed contract levels. The delivered level depends on the operation and the caller's output type. Use the record and domain checks when the distinction matters.

## Other packages

| Package | Purpose |
| --- | --- |
| [`chat`](chat) | A conversation session with caller-run tools; it keeps history and checks tool calls. |
| [`flow`](flow) | Decisions, branches, agreement, review, checkpoints, and bounded parallel work. |
| [`op`](op) | Typed operation descriptors, preflight plans, stages, and batch runners. |
| [`exp`](exp) | Experimental transformation and reasoning operations. |
| [`redact`](redact) | Redact values locally or text with a model. |
| [`mw`](mw) | Compose provider middleware for caching, routing, retries, limits, and more. |
| [`pricing`](pricing) | Price tables and process-level spend limits. |
| [`telemetry`](telemetry) | Observer events, metrics, and optional OpenTelemetry adapter. |
| [`schemafluxtest`](schemafluxtest) | Scripted provider, recordings, and test helpers. |

`StreamSummarize`, `StreamRewrite`, `StreamTranslate`, and `StreamExpand` return a `TextStream`. Range over `All`, call `Text`, or call `Cancel` when abandoning it. A stream reports prompt-only delivery and needs a provider that supports streaming. The current middleware wrappers do not forward streams, so a streaming client must use an unwrapped streaming provider.

## Redaction and its limits

`redact.Value` works locally on a Go value. For a known shape, tag sensitive fields with `redact` tags. Its field-name detector matches whole names, not substrings: `FirstName` and `APIKey` match while `Filename` and `CardCount` do not. Its card detector checks Luhn, so it can distinguish a valid unformatted card number from a same-length order number. A bare nine-digit number is deliberately not treated as an SSN because it is indistinguishable from an order ID. This is a pattern matcher, not a classifier; it does not find a person's name inside an arbitrary note. The `Jumble` and `Scramble` strategies are obfuscation, not anonymization: the value's length and shape survive.

`redact.Text` sends free text to the client's provider for model-assisted detection, with local pattern checks beside it. It cannot guarantee that every sensitive span will be found. Use `redact.Value` and field tags when the data's shape is known. In v1, `RedactWithResult` reports the fields and values it replaced; v2 `redact.Value` returns the cleaned value and an error, without a replacement report. Migrate callers that used the report deliberately.

## Tests without an API key

Inject a scripted provider. It records the requests it receives and makes plain `go test` runs offline:

```go
var model schemafluxtest.Provider
model.Reply(`{"name":"Ada","age":36}`)
ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
got, err := schemaflux.Extract[Person](ctx, "Ada is 36 years old")
```

The example also imports `github.com/monstercameron/schemaflux/v2/schemafluxtest`. More runnable examples are in [`examples`](examples). Tests that reach a paid provider run only with `SCHEMAFLUX_LIVE_TESTS=1`; do not set it for the normal test suite.

## Migrating from v1

V2 is a major API change. Update the module import to `github.com/monstercameron/schemaflux/v2`, then make these changes:

| V1 pattern | V2 pattern |
| --- | --- |
| `InitWithEnv()` | Configure `Client`, or use the zero/default client with environment variables. |
| `Extracting[T](input).Strict().Run()` | `Extract[T](ctx, input, ExtractOptions{Options: Options{Mode: ModeStrict}})` |
| `Summarizing(text).MaxLength(n).Run()` | `Summarize(ctx, text, SummarizeOptions{TargetLength: n, LengthUnit: "words"})` when the old limit meant words; check the option's units. |
| `Choosing(items).By(criteria...).Run()` | `Choose(ctx, items, ChooseOptions{Criteria: criteria})`. |
| Builder `.Context(ctx)` | Pass `ctx` as the first operation argument. |
| Process-wide provider setup | `WithClient(ctx, client)` for an invocation or `SetDefault(client)` for the process. |
| Reading global metrics or relying on a logged error | Attach a `Recorder`, inspect `Record`, and handle the returned `*Error`. |
| Experimental root functions | Import `exp` and call its functions with a context and typed options. |

Review each operation's new options and result type rather than doing a mechanical rename. A few operations now refuse output the old API accepted: malformed structure, violations of an offered collection, unmet checked options, and results below a data-policy floor. The v2 API does not ship the old fluent builders. The [`examples`](examples) directory contains end-to-end ports.

## Development and release checks

Run `./scripts/check.sh` for the repository's CI gate; `./scripts/check.sh --list` shows its steps. The suite clears `SCHEMAFLUX_LIVE_TESTS` before running. See [`AGENTS.md`](AGENTS.md) for the project rules and [`TODOS.md`](TODOS.md) for the upgrade's evidence and remaining follow-ups.

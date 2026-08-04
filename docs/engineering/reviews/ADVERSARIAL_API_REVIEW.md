# Adversarial API Review

Started: 2026-08-04
Reviewer: adversarial pass over the full exported surface (159 root-package functions plus the `pricing`
and `telemetry` packages).

## Purpose

Every public entry point is reviewed against three questions:

1. **API** — is the signature honest, consistent, and hard to misuse?
2. **Usefulness** — does this verb earn its place, or is it a prompt variation wearing a function name?
3. **Implementation** — does the code do what the name and docs claim, under real conditions?

The stance is adversarial on purpose. Findings are written as accusations to be disproved, not as
suggestions. Anything that survives is real work.

## Severity key

| Level | Meaning |
| --- | --- |
| **S1** | Silently produces wrong results, wrong numbers, or fake data. A user cannot tell it happened. |
| **S2** | Design defect that constrains real use or forces the user to work around the library. |
| **S3** | Papercut: misleading signature, dead code, race, avoidable cost. |

## Status legend

`OPEN` — not addressed. `ACK` — accepted, deferred. `FIXED` — resolved, with commit. `WONTFIX` — rejected, with reason.

## Progress

| # | Family | Status |
| --- | --- | --- |
| 1 | Infrastructure (client, init, providers, logging, tracking, pricing) | reviewed — 18 findings |
| 2 | Structured data ops | reviewed — 15 findings |
| 3 | Text ops | reviewed — 13 findings |
| 4 | Analysis + validation ops | reviewed — 10 findings |
| 5 | Collection + reasoning ops | reviewed — 10 findings |
| 6 | Procedural + control flow | reviewed — 10 findings |
| 7 | Fluent builder layer + legacy duality | reviewed — 7 findings |
| 8 | Tools registry | reviewed — 6 findings |
| — | Cross-cutting | 5 findings |
| — | Gaps (missing/weak capabilities) | 14 gaps |
| — | Gaps (control flow) | 10 gaps |
| 9 | Synthesis | complete |
| 10 | Target API shape (9 primitives, precedent-backed) | complete |
| 11 | Remediation plan (7 phases, findings mapped) | complete |

Totals: **94 findings** plus **24 documented gaps** = 118 entries.
Severity across all entries: **38 S1**, **59 S2**, **21 S3**.

## Reading order

- **Reviewing the damage:** Synthesis → S1 index → the family sections for detail.
- **Fixing it:** Target API shape → Remediation plan. Phase 0 is independent of everything else and
  should ship first; Phases 1–2 are where the bulk of the findings close, and they are one body of
  work, not sixty patches.

---

# Family 1 — Infrastructure API

Surface: `NewClient`, `Client.With*`, `Init`, `InitWithEnv`, `GetDefaultClient`, `ConfigureLogging`,
`SetLogLevel`, `GetLogEntries`, `ResetLogEntries`, request-tracking helpers, `pricing.*`, `telemetry.*`,
and the provider registry.

## I-01 — A missing API key silently yields fake data instead of an error `S1` `OPEN`

`client.go:51-54`, `client.go:186-188`

`NewClient("")` falls back to the `local` provider, which is the mock. `Init("")` takes the same path.
A production deployment with an unset `SCHEMAFLOW_API_KEY` therefore does not fail: it returns
`Mock response for: ...` which either parses into a zero-valued struct or produces a JSON parse error
that reads like a model failure. The one honest error in the stack — `"no LLM provider configured"`
(`internal/ops/llm_helper.go:47`) — is unreachable through the documented init path, because a provider
is always assigned.

**Fix:** fall back to the mock only when the provider was explicitly requested as `local`. Otherwise
return an error from `Init`/`InitWithEnv` (which currently cannot fail at all — see I-13).

## I-02 — Cost tracking reports confidently wrong dollar figures `S1` `OPEN`

`pricing/pricing.go:213-256`, `getDefaultPricing`

- Any Anthropic model that is not one of the three hardcoded `claude-3-*` keys falls back to
  **claude-3-haiku pricing**. A Claude Opus call is reported at Haiku rates — roughly a 60x
  understatement, presented as a precise USD number.
- `deepseek`, `qwen`, `zai`, `cerebras`, `openrouter`, and `local` have no entry and no default, so
  `CalculateCost` returns `TotalCost: 0` with only a log warning. Six of eight supported providers
  report a $0.00 spend.
- The price table is a private package var with hardcoded `EffectiveDate: 2024-01-01` values and
  **no public way to register or override a model's price**.

The README markets cost tracking as a headline feature. Reporting zero is bad; reporting a wrong
non-zero number is worse, because it looks like a measurement.

**Fix:** add `RegisterPricingModel`, and put a `PricingSource` / `Estimated` field on `CostInfo` so an
unpriced call is distinguishable from a free one. Never substitute another model's price.

## I-03 — Non-OpenAI providers are sent OpenAI model IDs `S1` `OPEN`

`internal/config/config.go:194-203`

`GetModel` special-cases `openrouter`, `cerebras`, and `anthropic`. Everything else falls through to
`gpt-5.4` / `gpt-5-mini` / `gpt-5-nano` — including `deepseek`, `qwen`, `zai`, and any custom
registered provider. The Anthropic provider defends itself against this (`internal/llm/provider.go:366`
rewrites any `gpt*` model to Sonnet), but the OpenAI-compatible path does not.

So the README's own example, `client.WithProvider("deepseek")`, sends `model: "gpt-5.4"` to
`api.deepseek.com` and 400s, unless the user also sets an undocumented `SCHEMAFLOW_MODEL`.

**Fix:** a per-provider default-model map keyed by intelligence tier, plus validation at provider
construction rather than at first call.

## I-04 — JSON mode is chosen by substring-matching English, including user data `S1` `OPEN`

`internal/ops/llm_helper.go:61`, `299-317`

`inferResponseFormat` decides whether to request structured output by searching the concatenated
system **and user** prompt for phrases such as `"json object"` and `"valid json"`. Two consequences:

- User input containing the phrase "json object" flips a text operation into JSON mode. Response
  format is data-dependent, which is an injection-adjacent control path.
- Rewording an operation's prompt silently removes JSON enforcement, with no test that would catch it.

Each operation knows statically whether it needs JSON. This is the structural keystone of a typed LLM
library and it is currently a string search. The backlog lists this as a nice-to-have; it belongs in
release blockers.

**Fix:** `RequiresJSON bool` on the operation descriptor, threaded into `CompletionRequest`.

## I-05 — The type schema is used for prompting and then thrown away `S1` `OPEN`

`internal/llm/provider.go:130-135`

`GenerateTypeSchema` produces an exact schema for `T`, which is then rendered into the system prompt.
The provider request asks only for `json_object` — free-form JSON — rather than the OpenAI Responses
API's `json_schema` strict mode. The library holds the one artifact that would make
`Extracting[T]` structurally guaranteed and never sends it to the API that can enforce it.

This is the largest missed opportunity in the codebase: the central selling point (types are the
contract) is enforced only by persuasion.

**Fix:** send the generated schema as `text.format = {type: "json_schema", strict: true, schema: ...}`
for providers that support it; fall back to prompt-only for the rest, and record which path was used.

## I-06 — `Client` is write-only configuration over package globals `S2` `OPEN`

`client.go:18-146`, `internal/ops/llm_helper.go:19-35`

`Client` has no method that runs an operation. Every op resolves `ops.defaultProvider`, a package
global. `WithProviderConfig` and `WithProviderInstance` mutate that global as a side effect
(`client.go:108`, `client.go:126`), so constructing a second client silently reconfigures the first.

The practical cost: you cannot use a cheap provider for `Classifying` and a smart one for `Extracting`
in the same process, which is the most obvious reason to want multiple clients.

**Fix:** either give `Client` real operation methods (or carry the provider on `context.Context`), or
delete `Client` and document the global model honestly. The current shape promises isolation it does
not deliver.

## I-07 — Fluent client configuration is order-dependent and silently drops settings `S2` `OPEN`

`client.go:60-112`, `client.go:293-329`

`timeout`, `maxRetries`, and `retryBackoff` are only read when a provider is constructed. The README
ordering happens to work; the reverse ordering does not:

```go
client := schemaflow.NewClient(key).WithProvider("openai").WithTimeout(60 * time.Second) // timeout ignored
```

No error, no warning. A fluent API where option order changes behavior is a trap.

**Fix:** rebuild the provider on any config mutation, or make config a value applied at call time.

## I-08 — No programmatic control of model, tokens, or temperature `S2` `OPEN`

`internal/config/config.go:132-232`, `internal/types` `OpOptions`

Model, timeout, retries, and max tokens are read from `os.Getenv` on every call. `OpOptions` has no
`Model`, `MaxTokens`, or `Temperature` field. To change a model, a library consumer must mutate
process environment variables.

Worse, `SCHEMAFLOW_MODEL` (`config.go:135-137`) overrides everything, so `Smart()` / `Fast()` /
`Quick()` become silent no-ops when it is set — the tier abstraction disappears without a word.

**Fix:** per-operation overrides on the options struct; env vars as the lowest-precedence fallback.

## I-09 — Output token ceilings are hardcoded and truncation is terminal `S2` `OPEN`

`internal/config/config.go:206-218`

4000 / 2000 / 1000 output tokens by tier, with no override. A long `Summarizing`, `Decomposing`, or
`Clustering` result hits the ceiling, returns truncated JSON, fails `ParseJSON`, and is **not** retried
— retries only cover provider-level errors, not parse failures. The user sees a parse error with no
indication that the cause was a token cap they cannot change.

## I-10 — `costHistory` is an unbounded global with O(n) reads `S2` `OPEN`

`pricing/pricing.go:301`, `pricing/analytics.go:27-98`

Every LLM call appends a `CostRecord` (including a `Tags` map) to a package-level slice that is never
evicted or capped. `GetRequestCost` linear-scans it under a read lock; `GetCostSummary` and
`GetTotalCost` do the same. A long-running service leaks memory proportional to request count and
gets slower at reading its own metrics over time.

Additionally, `ResetCostTracking` (`analytics.go:101-109`) nulls `budgetLimits` and `budgetCallback`
as a side effect, so "clear the history" silently disables budget alerting.

**Fix:** ring buffer with a configurable cap, an index by request ID, and separate history reset from
budget config reset.

## I-11 — Budgets do not budget `S2` `OPEN`

`pricing/pricing.go:351-366`, `532-561`

`SetBudget` records limits and fires a callback once spend exceeds **80%** of a limit. It fires on
every subsequent tracked request with no debounce and no state, so an alerting callback becomes a
per-request firehose. Nothing is ever blocked or errored: the "limit" is advisory only, and the
callback signature `(current, limit, period)` gives the receiver no way to tell warning from breach.

**Fix:** edge-triggered notifications with threshold state, and an opt-in enforcement mode that
returns a typed error before the call is made.

## I-12 — Retry classification is substring matching on error text `S2` `OPEN`

`internal/ops/llm_helper.go:205-277`

`isRetryableLLMError` lowercases the error string and searches for `"unauthorized"`, `"status 429"`,
`"rate limit"`, and so on. No typed errors, no status-code plumbing from the providers. A provider
whose message wraps an unrelated word in the non-retryable list will fail fast on a transient error,
and vice versa. Unknown errors are treated as non-retryable, which contradicts the README's claim of
"retries for transient provider failures."

`retryDelay` (`:265`) has exponential backoff with a 5s cap but **no jitter**, so concurrent callers
retry in lockstep.

**Fix:** typed provider errors carrying HTTP status; classify on the type. Add jitter.

## I-13 — `InitWithEnv` has a signature that lies `S3` `OPEN`

`client.go:196-211`

```go
func InitWithEnv(paths ...string) error
```

The doc comment says it "reads configuration from a .env file if path is provided." The body says
`// For now, just use environment variables directly` and never touches `paths`. It also always
returns `nil`, so the README's `if err := InitWithEnv(); err != nil { panic(err) }` is dead code —
which is exactly how a missing key becomes I-01.

It also calls `os.Setenv("SCHEMAFLOW_API_KEY", ...)` (`client.go:205`), mutating process-global state
from a library init, which leaks into child processes.

## I-14 — Unsynchronized globals `S3` `OPEN`

`client.go:149-247`, `internal/ops/llm_helper.go:19-30`

`defaultClient` is written under `mu` in `Init` but read without it in `GetDefaultClient`, `GetLogger`,
`ConfigureLogging`, and `SetLogLevel` (the latter two also *write* `defaultClient.logger` unlocked).
`ops.defaultProvider` and `ops.customLLMCaller` are plain globals with no mutex. The backlog notes
`-race` cannot run on the current Windows/arm64 machine, which is likely why these have not surfaced.

## I-15 — Dead field: `Client.openaiClient` `S3` `OPEN`

`client.go:19`, `client.go:44` — constructed on every keyed `NewClient` and never read anywhere.

## I-16 — `WithDebug` is asymmetric `S3` `OPEN`

`client.go:131-140` — `WithDebug(true)` lowers the global log level to debug; `WithDebug(false)` does
not restore it. Toggling debug off leaves the logger verbose.

## I-17 — Duplicate filter helpers `S3` `OPEN`

`pricing/pricing.go:451` (`MatchesFilters`, exported) and `pricing/pricing.go:499` (`matchesFilters`).
One of them is public surface by accident.

## I-18 — Every request pays a fixed prompt tax `S3` `OPEN`

`internal/ops/llm_helper.go:319-333`

`strengthenSystemPrompt` prepends a fixed instruction block ("Do not merely restate schemas...") to
every single request, JSON or not. It is a workaround for a model failure mode, billed to the user on
every call, with no way to opt out and no measurement of whether it still helps on current models.

---

# Cross-cutting findings

These were found while reviewing one family but apply across the whole operation surface. They are
tracked separately because fixing them is one change, not thirty.

## X-01 — 31 operations discard the caller's `context.Context` `S1` `OPEN`

`internal/ops/collection.go:75,185,307,386`, `text.go:135,206,323,407,517,597,703,777`,
`extended.go:364,443,486,624,691,1059,1122`, `batch.go:128,210`, `complete.go:151`,
`control_flow.go:100`, `diff.go:344`, `explain.go:254`, `infer.go:82`, `parse.go:493`,
`procedural.go:59,163`, `redact_llm.go:140`, `suggest.go:213`

The pattern is:

```go
ctx, cancel := context.WithTimeout(context.Background(), config.GetTimeout())
```

`opts.Context` is accepted by the options struct and by the builder's `Context(...)` method, then
ignored. Consequences for `Choosing`, `Filtering`, `Sorting`, every text operation, `Validating`,
`Asking`, `Inferring`, `Completing`, `Suggesting`, `Diffing`, `Explaining`, `Decide`, and `Guard`:

- Caller cancellation does nothing. An abandoned HTTP request keeps paying for tokens.
- Caller deadlines do nothing; the global 30s `SCHEMAFLOW_TIMEOUT` applies instead.
- Context values are lost, including the request-tracking metadata this library itself puts there
  (`WithCorrelationID` / `WithRequestTrackingMetadata` cannot survive into these ops).

The README advertises "timeout control through context and client configuration." For the majority of
operations that is not true. Even the ops that *do* honor the caller context wrap it in
`context.WithTimeout(ctx, config.GetTimeout())` (57 sites), which silently caps any longer caller
deadline at the global default.

## X-02 — Two divergent JSON-cleanup implementations `S2` `OPEN`

16 files hand-roll markdown-fence stripping inline (`analysis.go`, `arbitrate.go`, `audit.go`,
`collection.go`, `compose.go`, `conform.go`, `derive.go`, `extended.go`, `interpolate.go`,
`negotiate.go`, `pivot.go`, `procedural.go`, `project.go`, `redact_llm.go`, `resolve.go`,
`suggest.go`); 18 others call `ParseJSON`/`cleanJSON` (`internal/ops/json.go`). The two paths behave
differently, and neither handles the common real failure: a sentence before or after the JSON body.
Any hardening done in one place silently does not apply to the other half of the library.

## X-03 — Errors carry raw user payloads into logs `S2` `OPEN`

`internal/ops/json.go:18`, `internal/ops/core.go:116-124`

`ParseJSON` embeds the full cleaned model output in its error string, and `types.ExtractError` stores
the original `Input`. Both flow into structured logs on the error path. For a library that ships a
`Redacting` operation and markets PII handling, error paths are the one place raw payloads should not
appear by default.

---

# Family 2 — Structured data operations

Surface: `Extract`/`Extracting`, `Transform`/`Transforming`, `Generate`/`Generating`,
`Infer`/`Inferring`, `Parse`/`Parsing`, `Enrich`/`Enriching`, `EnrichInPlace`, `Normalize`/
`NormalizeText`/`NormalizeBatch`, `Project`/`Projecting`, `Pivot`/`Pivoting`, plus the shared
schema/JSON helpers in `internal/ops/utils.go` and `internal/ops/json.go`.

## D-01 — The generated schema does not recurse into nested types `S1` `OPEN`

`internal/ops/utils.go:13-91`

`GenerateTypeSchema` expands the top-level struct, but each field is described by
`GetTypeDescription(field.Type)`, which for a struct returns `targetType.String()` and for a slice
falls through to `default:` and returns the Go type name. So:

```go
type Order struct {
    Customer Person      `json:"customer"`
    Items    []OrderItem `json:"items"`
}
```

produces:

```
{
  customer: main.Person (required)
  items: []main.OrderItem (required)
}
```

The model is told a Go package-qualified identifier it has never seen and must guess the nested shape.
Nested structs and slices of structs are the normal case for extraction, so the flagship operation is
weakest exactly where real payloads live. `Project`, `Pivot`, `Enrich`, and `Derive` all inherit this,
since they build both source and target schemas the same way.

**Fix:** recurse in the struct branch (with a depth cap and cycle detection), or generate real JSON
Schema and reuse it for I-05.

## D-02 — `json:"-"` fields are described to the model and expected back `S1` `OPEN`

`internal/ops/utils.go:32-37`

```go
if parts[0] != "-" {
    fieldName = parts[0]
}
```

When the tag *is* `-`, the branch is skipped and the field keeps its Go name — so the field is
included in the schema instead of being excluded. Fields deliberately kept out of serialization
(password hashes, internal handles, cached state) are named to the model, which is asked to produce
values for them. `encoding/json` then discards those values on unmarshal, so the round trip is
guaranteed to mismatch and the tokens are wasted.

**Fix:** skip the field entirely when the tag's first part is `-`.

## D-03 — "Required" is inferred from `omitempty` `S2` `OPEN`

`internal/ops/utils.go:42-47`

`omitempty` is a serialization directive about zero values, not a validation statement. A genuinely
required field tagged `json:"name,omitempty"` is presented to the model as optional, and an optional
field without the tag is presented as required. The library has no way to express requiredness, and
`Strict()` (D-06) has nothing real to enforce.

## D-04 — `NormalizeInput` prefers `fmt.Stringer` over JSON `S2` `OPEN`

`internal/ops/utils.go:99-113`

The type switch checks `fmt.Stringer` before falling back to `json.Marshal`. Any input type with a
`String()` method — extremely common, including `time.Time` — is sent to the model as prose instead of
JSON. A `time.Time` field arrives as `2006-01-02 15:04:05 +0000 UTC` while the generated schema tells
the model the format is `datetime (RFC3339)`. The library contradicts itself in the same request.

**Fix:** marshal structs as JSON; use `Stringer` only for types that are not JSON-marshalable.

## D-05 — `Strict()` is a prompt sentence plus a nil check `S1` `OPEN`

`internal/ops/utils.go:180-198`, `internal/ops/core.go:209-226`

Strict mode adds "All required fields MUST be present and valid / Fail if any required field cannot be
extracted" to the prompt, then calls `ValidateExtractedData(result, opt.Threshold)` — which checks
non-nil and valid reflect kind and **never reads `threshold`**. There is no field-presence check, no
type validation, no threshold comparison. `Strict()` is the most confidence-inspiring word in the API
and it enforces nothing.

## D-06 — Fabricated confidence values `S1` `OPEN`

`internal/ops/utils.go:167-178`, `internal/ops/core.go:216`

`CalculateParsingConfidence` returns `0.3` if the response starts with `{` or `[`, else `0.1`. On the
strict-validation failure path, confidence is set to `opt.Threshold - 0.1` with the comment
`// Just below threshold`. These are not measurements; they are literals shaped like measurements,
surfaced through a documented `Confidence float64 // (0.0-1.0)` field.

Every result struct in this family (`ProjectResult.Confidence`, `EnrichResult.Confidence`,
`NormalizeResult`/`NormalizeChange.Confidence`, `ParseResult`) either carries one of these literals or
carries a number the **model reported about its own work**, with no verification. A caller cannot
distinguish the two.

**Fix:** delete the field, or make it honest — logprob-derived where the provider exposes them, and
explicitly `nil`/absent otherwise. A self-reported score should be named `ModelReportedConfidence`.

## D-07 — `Creative` mode on `Extract` is a documented hallucination switch `S2` `OPEN`

`internal/ops/utils.go:141-146`

```
- Generate plausible values for missing fields
- Prioritize completeness over strict accuracy
```

That is fabrication instructed by the library, on an operation whose entire purpose is faithfulness to
the input. If it must exist, it should be named for what it does (`Fabricate`, `Imagine`) and be
absent from `Extracting`'s builder.

## D-08 — `Project`'s `Exclude` is a suggestion, and the doc sells it for privacy `S1` `OPEN`

`internal/ops/project.go:83-107`, `177-181`

The package documentation's first example is "Create public profile from internal user (privacy
filtering)" with `Exclude: []string{"password_hash", "ssn"}`. `Exclude` is interpolated into the
prompt as `Exclude fields: password_hash, ssn` and nothing else. There is no post-filter on the
returned object, no check that excluded source values are absent from the output, and no error if
they appear. The model can echo an SSN into `display_name` and the library will report success with a
mapping list that does not mention it.

Marketing a prompt hint as privacy filtering is the most dangerous single line of documentation in the
repo.

**Fix:** deterministically drop excluded fields from the marshalled input before the call, and
post-scan the output for excluded values. Also stop describing it as privacy filtering until it is.

## D-09 — `Lost` / `Inferred` / `Mappings` are model self-report presented as an audit trail `S1` `OPEN`

`internal/ops/project.go:258-283` (same shape in `pivot.go`, `enrich.go`, `normalize.go`)

`Lost` — which source fields failed to project — is fully computable in Go by diffing the source field
set against the produced output. Instead it is whatever the model chose to list. The struct field
names (`Lost`, `Inferred`, `Confidence`, `Mappings[].Method`) read like instrumentation and are
narration.

## D-10 — `PreserveNulls` is declared, merged, and never used `S3` `OPEN`

`internal/ops/project.go:27-28`, `303` — present in the options struct and in `mergeProjectOptions`,
absent from the prompt and from the result handling. A documented option that does nothing.

## D-11 — `Parse` silently succeeds with empty data on header mismatch `S1` `OPEN`

`internal/ops/parse.go:283-327`, `374-396`

`parseCSV` maps columns by `capitalizeFirst(header)` against Go **field names**, ignoring `json` tags.
A struct with `FullName string \`json:"full_name"\`` will not receive a `full_name` column
(`"Full_name" != "FullName"`). Unmapped columns are skipped with `continue`, no error. For a
single-struct target with zero matching headers, `Parse` returns a zero-valued struct and `nil` error
— success with no data.

**Fix:** match on the `json` tag first, then the field name; return an error (or a populated
`ParseResult.Unmapped`) when no column maps.

## D-12 — `Parse[T]` panics for interface targets `S3` `OPEN`

`internal/ops/parse.go:301`, `344` — `reflect.TypeOf(result)` is `nil` when `T` is an interface type
(e.g. `Parse[any]`), and the immediate `.Kind()` call dereferences it. A library that leans this hard
on generics should not panic on a legal instantiation.

## D-13 — `detectFormat` heuristics misroute ordinary prose `S2` `OPEN`

`internal/ops/parse.go:188-238`

Any input containing `": "` and no `{` is classified YAML; any input containing `|` and no `{` is
"pipe-delimited"; any input with a tab is TSV. Ordinary English text ("Note: the total was 42") is
routed to the YAML parser, fails, and — with `AllowLLMFallback` off, the default — returns
`parsing failed`. The deterministic-first design here is the right pattern and deserves better
detection than prefix and substring checks.

## D-14 — `NormalizeBatch` is a serial loop of single-item calls `S2` `OPEN`

`internal/ops/normalize.go:347-364`

N items means N sequential LLM round trips: N times the latency and, because each call re-sends the
schema and system prompt, considerably more than N times the necessary prompt tokens. The repo already
contains a batch processor (`internal/ops/batch.go`) that this does not use. `Normalizing` is exactly
the operation where a caller has thousands of rows.

## D-15 — Every op re-implements the same 40-line procedure `S2` `OPEN`

`core.go`, `project.go`, `pivot.go`, `enrich.go`, `normalize.go`, and roughly 60 more files repeat:
validate options, flatten options into an English steering string, build a system prompt by
`fmt.Sprintf`, call the LLM, strip fences (two different ways, X-02), unmarshal into an anonymous
struct, copy fields across, log. Each copy is a place for a divergent bug — X-01 is exactly that bug,
replicated 31 times. There is no shared operation skeleton.

**Fix:** one internal `execute[T](descriptor, input, opts)` that owns context, retries, response
format, parsing, and telemetry. Individual ops become a prompt builder plus a result mapper.

---

# Family 3 — Text operations

Surface: `Summarize`/`Summarizing`, `Rewrite`/`Rewriting`, `Translate`/`Translating`,
`Expand`/`Expanding`, the four `*WithMetadata` variants, `Complete`/`Completing`,
`CompleteField`/`CompletingField`, `Redact`/`Redacting`, `RedactLLM`/`LLMRedacting`,
`Suggest`/`Suggesting`.

## T-01 — `X` and `XWithMetadata` are copy-pasted twins `S2` `OPEN`

`internal/ops/text.go:95/166`, `269/353`, `467/547`, `659/733`

Each pair duplicates the entire option-to-instruction block (roughly 40 lines, identical line for
line) and differs only in whether the prompt asks for JSON and which struct is returned. Eight public
functions carrying four operations' worth of behavior, with four opportunities for the two halves to
drift apart.

**Fix:** one implementation returning the rich result; keep the string-returning form as a
three-line wrapper.

## T-02 — `SummarizeWithMetadata` fails open with an invented confidence `S1` `OPEN`

`internal/ops/text.go:241-251`

The response is passed to `json.Unmarshal` directly — no fence stripping, not even the library's own
`cleanJSON`. On any parse failure the function returns the raw response as the summary with
`Confidence: 0.7 // Default confidence for fallback` and an empty `KeyPoints`, and `error == nil`.

The caller sees a successful result whose documented metadata is silently absent and whose confidence
is a literal. Providers without JSON-mode support (Anthropic, per I-04/I-05) will take this path
routinely, which means the fallback is not an edge case — it is the Anthropic behavior.

## T-03 — Length, tone, and style options are never checked against the output `S2` `OPEN`

`internal/ops/text.go:108-124`, and the builder's `MaxLength(120)` / `Tone("executive")`

`TargetLength`, `LengthUnit`, `Style`, `FocusAreas`, and `PreserveInfo` become one English sentence
appended to `Steering`. Nothing measures the result. `MaxLength` in particular reads like a guarantee
in the README's `Summarizing(article).MaxLength(160)` example and is a request the model may ignore.

`CompressionRatio` (`text.go:253`) is computed from `len()` on strings, so it counts bytes: for any
non-ASCII text the reported ratio is wrong.

## T-04 — `Complete` and `CompleteField` take a provider; nothing else does `S2` `OPEN`

`internal/ops/complete.go:123`, `386`

```go
func Complete(ctx context.Context, provider llm.Provider, partialText string, opts CompleteOptions) (CompleteResult, error)
```

Every other operation in the library resolves the package-global provider and takes neither argument.
These two leak `internal/llm.Provider` into their signature and force the caller to supply a provider
the rest of the API hides. Two callers of the same library cannot use one calling convention.

## T-05 — `estimateCompletionConfidence` is astrology with a float return `S1` `OPEN`

`internal/ops/complete.go:281-311`

```go
confidence := 0.5
if len(completion) > 20 { confidence += 0.2 }   // "Longer completions tend to be more confident"
if strings.Contains(completion, ".") { confidence += 0.1 }
```

Text length and the presence of a full stop are not evidence about correctness. This value is returned
as `CompleteResult.Confidence` alongside genuinely meaningful fields, with nothing marking it as a
guess.

## T-06 — Byte slicing corrupts multi-byte text `S1` `OPEN`

`internal/ops/complete.go:269-276`, `internal/ops/redact_llm.go:305-336`

`completedText[:maxTotalLength]` and the span-slicing in `applyRedactions` index bytes, not runes. Any
cut that lands mid-rune produces invalid UTF-8 in the returned string. `MaxLength` is documented in
characters.

## T-07 — `Redact` over-redacts on field-name substrings `S1` `OPEN`

`internal/ops/redact.go:359-387`

The sensitive-name list includes `"name"`, `"key"`, `"first"`, `"last"`, `"card"`, `"address"`, and
`"full"`, matched as substrings against the lowercased field name. Collateral damage in ordinary
structs: `Filename`, `Username`, `Nickname`, `Keywords`, `KeyMetrics`, `APIKeyLabel`, `FirstSeen`,
`LastUpdated`, `CardCount`, `AddressBookSize`. These fields are silently destroyed, and
`RedactWithResult` (T-09) will not tell you which.

## T-08 — The redaction regexes miss the data they name `S1` `OPEN`

`internal/ops/redact.go:406-438`

False negatives on the exact categories advertised:

- SSN matches only `123-45-6789`; the undashed form is missed.
- Phone matches only `###-###-####`; `(305) 555-1234`, `305.555.1234`, and any international format
  are missed.
- Credit card (PII) requires exactly four space-separated groups; the financial pattern requires a
  separator. An unformatted 16-digit PAN is never caught by either.

False positives that destroy ordinary data:

- `\b[A-Z][a-z]+ [A-Z][a-z]+\b` matches any two capitalized words — "New York", "Total Revenue",
  "Monday Morning".
- `\b\d{9}\b` (financial) matches any nine-digit number — order IDs, sequence numbers.
- `\$\d+(?:\.\d{2})?` masks every currency amount in the text.

A redaction utility that masks "New York" while passing `4111111111111111` through is not merely
imperfect, it is misleading, because the option is called `Categories: []string{"PII"}` and invites
compliance use.

## T-09 — `RedactWithResult` reports nothing, by construction `S1` `OPEN`

`internal/ops/redact.go:186-200`

```go
// For now, return empty result - full implementation would track what was redacted
return redacted, result, nil
```

The one function whose purpose is to tell you what was redacted returns an empty map and a nil error.
A caller auditing a redaction pass receives "nothing was redacted" as a success.

## T-10 — `Redact[T](input T, opts ...interface{})` silently ignores unknown options `S2` `OPEN`

`internal/ops/redact.go:150-167`

Variadic `interface{}` options in a library whose premise is type safety, with a `default:` branch that
falls back to `NewRedactOptions()` when the argument is not a recognized type. Passing the wrong
options struct compiles, runs, and quietly discards your configuration.

## T-11 — Jumble redaction is a reversible anagram with a guessable seed `S1` `OPEN`

`internal/ops/redact.go:505-533`

```go
r := rand.New(rand.NewSource(opts.JumbleSeed))
if opts.JumbleSeed == 0 {
    r = rand.New(rand.NewSource(int64(len(input))))
}
```

`JumbleSeed` defaults to zero (`NewRedactOptions` does not set it), so the RNG is seeded with the
input's length — a value the attacker can read off the output. `jumbleBasic` is a Fisher-Yates shuffle
of the same runes, so the output is a permutation of the input, and the permutation is fully
determined by a seed anyone can reproduce with this library. `RedactJumble` and `RedactScramble` are
therefore **invertible**: the original string can be recovered exactly.

Character-set-preserving scrambling is also not anonymization even when unpredictable — length,
alphabet, and character frequency all survive.

## T-12 — `JumbleSmart` does not do what it documents `S3` `OPEN`

`internal/ops/redact.go:535-539` — documented as "preserves some structure (vowels, consonants)",
implemented as `return jumbleBasic(input, r)`.

## T-13 — `RedactLLM` trusts model-reported character offsets `S1` `OPEN`

`internal/ops/redact_llm.go:232-269`, `305-336`

Spans come back from the model as `{start, end}` integers and are used directly to slice the original
text. Validation is bounds-only (`Start < 0 || End > textLen || Start >= End`), so a span that is off
by a few characters passes and redacts the wrong region — masking neighbouring text while leaving the
sensitive value intact. Nothing cross-checks the sliced text against what the model claimed it found.

The offsets are also compared against `len(originalText)`, which is bytes, while models count
characters. For any non-ASCII input every span is shifted, and slicing at a non-boundary emits invalid
UTF-8 (T-06).

**Fix:** have the model return the matched substrings, then locate them deterministically with
`strings.Index`/regex; treat offsets as hints only, and reject a span whose sliced text does not match
the reported original.

---

# Cross-cutting findings, continued

## X-04 — Twelve declared options are dead code `S2` `OPEN`

Fields that exist in an exported options struct (most with a fluent `With...` setter) and are never
read by any implementation:

| Option struct | Field | Location |
| --- | --- | --- |
| `BatchOptions` | `OnProgress`, `PreProcess`, `PostProcess` | `internal/ops/options.go:1349` and nearby |
| `PipelineOptions` | `SaveProgress` | `internal/ops/pipeline.go:33` |
| `ClassifyOptions` | `CategoryExamples` | `internal/ops/options.go:763` |
| `TransformOptions` | `To` | `internal/ops/options.go:280` |
| `ClusterOptions` | `MaxClusterSize` | `internal/ops/cluster.go:27,95` |
| `RedactOptions` | `PreserveFormat` | `internal/ops/redact.go:52,117` |
| `ProjectOptions` | `PreserveNulls` | `internal/ops/project.go:27` |
| `SynthesizeOptions` | `OutputStructure` | `internal/ops/synthesize.go` |
| `AnnotateOptions` | `Language` | `internal/ops/annotate.go` |
| `CompareOptions` | `IncludeSimilarity` | `internal/ops/options.go` |
| `ScoreOptions` | `IncludeBreakdown` | `internal/ops/options.go` |
| `GenerateOptions` | `EnsureUnique` | `internal/ops/options.go` |

Each has a setter that returns the modified struct, so the call chain reads as if it configured
something. `WithMaxClusterSize(50)` is indistinguishable, at the call site, from an option that works.

**Fix:** implement or delete. A dead option is worse than a missing one because it silently
manufactures false confidence.

## X-05 — Options are compiled into English and never verified against the result `S2` `OPEN`

The dominant pattern across all families:

```go
instructions = append(instructions, fmt.Sprintf("Return top %d suggestions", opts.TopN))
steering := strings.Join(instructions, ". ")
opOptions.Steering = steering
```

`TopN`, `MaxLength`, `Exclude`, `Constraints`, `Categories`, `PreserveInfo`, `FocusAreas`,
`MappingRules`, `PreserveFields`, `StrictSchema` — every one of them becomes a clause in a prompt
sentence, and no operation checks its own output against the constraint it asked for. `Choosing`
does not verify that the returned option was in the input list; `Suggesting` does not trim to `TopN`;
`Filtering` does not check that returned items came from the input.

This is the structural criticism of the library. A typed API whose constraints are advisory prose
offers type safety at the signature and none at the boundary where the data actually arrives. The
minimum viable fix is a post-condition per operation — cheap, deterministic, and mostly a few lines
each — plus a documented policy for what happens when a post-condition fails (error, repair, retry).

---

# Family 4 — Analysis and validation operations

Surface: `Classify`/`Classifying`, `Score`/`Scoring`, `Compare`/`Comparing`,
`Similar`/`CheckingSimilarity`, `Validate`/`Validating`, `ValidateLegacy`, `Question`/`Asking`,
`QuestionLegacy`, `Explain`/`Explaining`, `Verify`/`Verifying`, `VerifyClaim`/`VerifyingClaim`,
`Diff`/`Diffing`, `Audit`/`Auditing`, `Critique`/`Critiquing`, plus the undocumented `Format`,
`FormatWithMetadata`, `Merge`, `MergeWithMetadata`.

## A-01 — `Validate` returns `Valid: true` when the model says "invalid" `S1` `OPEN`

`internal/ops/extended.go:314-325`

```go
if err := json.Unmarshal([]byte(response), &llmResult); err != nil {
    result.Valid = strings.Contains(strings.ToLower(response), "valid")
    result.Confidence = 0.5
    ...
    return result, nil
}
```

`"invalid"` contains `"valid"`. A response of *"The data is invalid because the email is malformed"*
sets `Valid = true`, populates no issues, assigns a fabricated `0.5` confidence, and returns
`error == nil`. The caller's `if !result.Valid` gate never fires.

This is the single most dangerous line in the library: a validation function that fails **open**,
silently, on the exact response text that indicates failure. Any provider that emits fenced JSON —
which this function does not strip, unlike its neighbours — takes this path.

**Fix:** delete the text-inference fallback. A parse failure in a validator is an error.

## A-02 — `MinConfidence` is enforced in one operation out of five `S2` `OPEN`

`internal/ops/annotate.go:287` (enforced) versus `analysis.go:90-92` (Classify),
`collection.go:171-173` (Filter), `verify.go:356` (Verify), `derive.go:211` (Derive) — all prompt-only.

The defaults are non-zero (`ClassifyOptions: 0.5`, `FilterOptions: 0.7`, `VerifyOptions: 0.7`), so a
user who never touches the option still believes a threshold is active. In `Classify` the confidence
value is sitting in a local variable one line above where it is copied to the result, and is not
compared to `opts.MinConfidence`. This is a one-line fix that has not been made in four places.

## A-03 — `MultiLabel` cannot produce a multi-label result `S2` `OPEN`

`internal/ops/analysis.go:83-88`, `ClassifyResult[C]`

`MultiLabel` and `MaxCategories` add "Allow multiple categories" and "Return at most N categories" to
the prompt, but `ClassifyResult[C]` has a single `Category C` field plus `Alternatives` (documented as
"other possible categories"). There is no field that means "this input belongs to these three
categories." The option changes the prompt and cannot change the result.

## A-04 — `Classify[T, C]`'s second type parameter is decorative `S2` `OPEN`

`internal/ops/analysis.go:198-219`

The category is produced as a string and converted to `C` by `json.Marshal` of a string followed by
`json.Unmarshal` into `C`. Only string-kinded types can survive that round trip, so
`Classify[Ticket, Priority]` where `type Priority int` compiles and fails at runtime. The allowed set
is `Categories []string` regardless, so `C` carries no information the API does not already have.

**Credit where due:** `Classify` is the only operation in the library that validates the model's
answer against the allowed set and returns an error when it does not match
(`internal/ops/analysis.go:178-196`). That check is exactly the post-condition X-05 asks for
everywhere else. It should be the template, not the exception.

## A-05 — `Validate` has no deterministic path for deterministic rules `S2` `OPEN`

`internal/ops/extended.go:198-355`

Every rule in the README's own example — "email must be valid, country must be ISO alpha-2, age must
be at least 18" — is checkable in Go, cheaper, faster, and correctly. The API accepts rules only as
prose and sends all of them to a model. `Parse` demonstrates the right pattern in this same package
(deterministic parsers first, LLM only as fallback); `Validate` does not use it.

## A-06 — `AutoCorrect` discards corrections that fail to parse `S3` `OPEN`

`internal/ops/extended.go:336-341` — `if err := json.Unmarshal(...); err == nil` with no `else`. A
correction that does not match `T` vanishes with no error and no log line; `Corrected` stays nil and
looks like "the model had no correction to offer."

## A-07 — `Validate.Valid` has two sources of truth `S3` `OPEN`

`internal/ops/extended.go:328` then `343-351`. With `FailOn` set, validity is recomputed from issue
counts; with `FailOn` empty, it is whatever the model asserted, which can contradict a non-empty
`Errors` list. Same field, two meanings, depending on an option.

## A-08 — Five operations, one shape `S2` `OPEN`

`Validate`, `Verify`, `Audit`, `Critique`, and `Score` all: marshal the input, send criteria as prose,
and return a verdict/score plus a list of issues plus a summary. The only real differences are field
names:

| Operation | Verdict field | Issue type | Score field |
| --- | --- | --- | --- |
| `Validate` | `Valid bool` | `ValidationIssue` | `Confidence` |
| `Verify` | `OverallVerdict string` | `ClaimVerification`, `LogicIssue`, `ConsistencyIssue` | `TrustScore`, `OverallConfidence` |
| `Audit` | `PassesAudit bool` | `AuditFinding` | `Severity` per finding |
| `Critique` | — | `CritiqueIssue` | `OverallScore`, `CriteriaScores` |
| `Score` | — | — | `ScoreResult` |

A user must learn five vocabularies to express one intent, and cannot move between them without
rewriting their result handling. This is the verb-explosion problem in its clearest form: the
distinctions live in the prompt, not in the types.

`Audit` deserves partial credit — `buildAuditSummary` (`audit.go:303`) computes its aggregates in Go
from the findings rather than asking the model for counts.

## A-09 — `Legacy` twins are permanent public surface `S3` `OPEN`

`ValidateLegacy` (`extended.go:358`) and `QuestionLegacy` (`extended.go:1054`) are exported from the
root package alongside their replacements, for a library that has not shipped 1.0. They double the
result types (`ValidationResult` vs `ValidateResult[T]`) that users must distinguish.

## A-10 — Undocumented and unreachable operations `S3` `OPEN`

- `Format`, `FormatWithMetadata`, `Merge`, `MergeWithMetadata` are exported from the root package and
  appear nowhere in the README's operation catalogue.
- `Deduplicate` (`extended.go:1107`) is fully implemented in `internal/ops` and exposed by no public
  wrapper, so no external consumer can call it. Either promote it or delete it.

---

# Family 5 — Collection and reasoning operations

Surface: `Choose`/`Choosing`/`ChooseBy`, `Filter`/`Filtering`/`FilterBy`, `Sort`/`Sorting`/`SortBy`,
`Rank`/`Ranking`, `Cluster`/`Clustering`, `Match`/`Matching`/`MatchOne`, `Annotate`, `Compress`,
`Decompose`, `Synthesize`, `Predict`, `Negotiate`, `Resolve`, `Derive`, `Conform`, `Interpolate`,
`Arbitrate`, `Assemble`, and the `Batch` processor.

## C-01 — `Choose` never checks that the chosen item was one of the options `S1` `OPEN`

`internal/ops/collection.go:112-131`

The model is asked to "Return the COMPLETE selected option as a JSON object" and the response is
unmarshalled straight into `T` and returned. Nothing compares it to the input list. A model that
invents a product, or — far more likely — that echoes an option with a subtly altered price, ID, or
date, produces a `Choose` result that looks authoritative and does not exist in your data.

The function holds `options []T` in a local variable. A membership check is one marshal-and-compare
loop. `Classify` performs the equivalent check (A-04) three files away.

## C-02 — `Filter` returns model-authored objects, and its instructions contradict its own option `S1` `OPEN`

`internal/ops/collection.go:165-169`, `196-247`

Same identity problem as C-01, multiplied: the returned slice is whatever the model emitted. Items can
be silently edited, dropped, duplicated, or invented, and the count can exceed the input length. There
is no check of any kind.

Worse, when `KeepMatching` is false the system prompt still says **"Include items that match the
criteria"** (line 201) while the steering string says **"Remove items that match the criteria"**
(line 168). The model receives two contradictory orders and the library reports whichever it obeys as
success.

## C-03 — `Filter`'s parse fallback collapses a filter to one item `S2` `OPEN`

`internal/ops/collection.go:236-245` — if the array parse fails but a single object parses, `Filter`
returns a one-element slice and `nil` error. A malformed response over a 500-item input becomes a
one-item "successful" filter.

## C-04 — `Sort` checks the count but not the contents `S2` `OPEN`

`internal/ops/collection.go:371-380`

**Credit:** `Sort` is the only collection op that validates anything — it rejects a result whose
length differs from the input and falls back. That check should exist in `Choose` and `Filter`.

It is still not sufficient: equal length does not mean same items. A model that returns the right
number of items with one of them modified passes. A permutation check against the marshalled inputs
(multiset equality) would close it.

## C-05 — `Sort`'s fallback silently multiplies cost by N `S2` `OPEN`

`internal/ops/collection.go:385-453`

`sortByScoringFallback` issues **one LLM call per item, serially**, with no cap, no concurrency, and no
warning to the caller. A failed sort over 500 items quietly becomes 500 additional API calls. Nothing
in the signature, the docs, or the returned value indicates that a fallback ran.

The irony: this fallback is the *better* algorithm. It scores each item independently, keeps the
original items (`scored[i].Item`), and sorts in Go with `sort.SliceStable`, so items cannot be lost,
duplicated, or edited, and `Stable` actually works. The library's stronger approach is reachable only
by accident, on failure.

**Fix:** make per-item scoring the primary strategy above a size threshold, run it with bounded
concurrency, and report in the result which strategy was used.

## C-06 — Collection operations have an undocumented hard size ceiling `S1` `OPEN`

`internal/ops/collection.go:188`, `310`; `internal/config/config.go:206-218`

Every collection op marshals the entire slice into one prompt with no chunking, no token estimate, and
no size guard — `batch.go` is the only chunking code in the repo and only `Extract` uses it. Meanwhile
output tokens are capped at 1000–4000 by tier (I-09), and `Sort`/`Filter` must echo **every item back**.

So a `Sorting` call over a few hundred modest objects cannot physically return a complete result: the
completion truncates, JSON parsing fails, and the caller gets a parse error (or, for `Sort`, N extra
billed calls). The documented examples all use three-item slices, which is the only size range where
this design works.

**Fix:** estimate tokens before the call and fail with a clear error, chunk with a merge step, or use
index-based responses (C-08) so the output size is independent of item size.

## C-07 — `Cluster` reports sizes it did not produce and drops items silently `S2` `OPEN`

`internal/ops/cluster.go:310-335`

Indices are bounds-checked (good), but out-of-range indices are dropped with no error and no count,
while `Size` is set from `len(c.Indices)` — the raw list including the dropped ones. `Size` and
`len(Items)` therefore disagree exactly when the model misbehaved, which is when a caller most needs
to know.

Nothing checks that the clusters plus outliers cover the input exactly once, so items can appear in
two clusters or vanish entirely and the result still reports success. `Quality` is model self-report.
`MaxClusterSize` is dead (X-04).

## C-08 — The library knows the right pattern and uses it in the minority of ops `S2` `OPEN`

Index-based ops that preserve identity by construction: `Match` (`match.go:355`), `Arbitrate`
(`arbitrate.go:324`), `Cluster`, `Interpolate`, `Compose`, `Batch`. They validate the index and index
into the caller's own slice, so the returned values are the caller's objects, byte for byte.

Object-echo ops that do not: `Choose`, `Filter`, `Sort`. `collection.go` documents the switch as a
deliberate choice ("Use object-based selection instead of index-based", lines 77, 196, 318).

That decision traded away the identity guarantee for prompt simplicity, and cost the size ceiling in
C-06 as well: echoing objects makes output length scale with input size, while indices do not.
Reverting the three collection ops to index-based responses fixes C-01, C-02, C-04, and most of C-06
at once. It is the single highest-value change in this family.

## C-09 — `BatchResult.TokensSaved` is an invented constant `S3` `OPEN`

`internal/ops/batch.go:239`

```go
tokensSaved += (len(chunk) - 1) * 100 // Approximate overhead per call
```

100 tokens per item is a guess presented as a measured saving, in a library that receives real token
counts from every provider response. Either compute it from usage or remove the field.

## C-10 — `FilterOptions.BatchSize` is dead `S2` `OPEN`

`internal/ops/options.go:1127,1139` — declared, defaulted to 50, validated in `Validate()`, and never
read by `Filter`. A user reasonably reads a validated, defaulted `BatchSize` as proof that batching
happens. It does not (C-06).

This also means the X-04 dead-option list is a lower bound: the mechanical scan misses fields whose
names are shared with a live field elsewhere in the package.

---

# Family 6 — Procedural and control-flow API

Surface: `Decide` and `Guard` (public), plus `Match`/`When`/`Like`/`Otherwise`, `Pipeline`, `Compose`,
`Then`, `StateMachine`, and `BatchProcessor` (implemented and tested in `internal/ops`, exported
nowhere).

## P-01 — `Decide` silently takes the first branch when anything goes wrong `S1` `OPEN`

`internal/ops/procedural.go:86-92`, `126-131`

Three separate failure paths — LLM error, unparseable response, out-of-range index — each return
`decisions[0].Value` with `err == nil` and a fabricated confidence (`0.5` and `0.3` respectively).
The underlying error is logged at warn level and discarded.

This is a control-flow primitive. When the provider is down, every `Decide` in the program takes
branch zero and reports success. Nothing in the returned `DecisionResult` distinguishes "the model
chose option 0" from "everything failed, so option 0." The caller cannot detect it without string
matching `Explanation` against `"Default selection (LLM unavailable)"`.

**Fix:** return the error. If a default is wanted, make it an explicit option
(`WithFallback(index)`) and set a `Fallback bool` on the result.

## P-02 — `Decide`'s first parameter is named `ctx` and is not a context `S1` `OPEN`

`schemaflow.go:764-770`, `internal/ops/procedural.go:35`, `77-83`

```go
func Decide[T any](ctx any, decisions []Decision[T], opts ...OpOptions) (T, DecisionResult, error)
// doc comment: result, decision, err := schemaflow.Decide(ctx, decisions)
```

`ctx` here is the *decision context* — arbitrary data formatted into the prompt with `%v`. In Go, a
first parameter named `ctx` means `context.Context` to every reader and every linter. The package's
own doc comment shows a call passing a variable named `ctx`. A user following it passes their real
context, which is silently rendered into the prompt as `context.Background.WithDeadline(...)` and
becomes the reasoning input.

Meanwhile the caller's actual context is ignored (X-01, `procedural.go:59`).

**Fix:** rename to `situation`/`state`, and add a real `context.Context` first parameter.

## P-03 — `Guard` makes an unannounced billed API call `S2` `OPEN`

`internal/ops/procedural.go:143-180`

`Guard(state, checks...)` reads as a pure predicate helper over user-supplied functions. When any
check fails it issues an LLM call with a hardcoded 2-second timeout to generate "suggestions", splits
the raw response on `\n`, and returns them. There is no options parameter, so there is no way to turn
it off, choose the model, or supply a context.

A guard clause in a hot path that silently calls an API on the failure branch is a latency and cost
surprise. It is also the only operation in the library with a hardcoded timeout constant.

## P-04 — `Match` costs one LLM call per case and fails to silent non-match `S1` `OPEN`

`internal/ops/control_flow.go:95-122`

Each string case invokes `matchesStringCondition`, which is a full LLM round trip (hardcoded 5s
timeout, `Quick` intelligence, no options). A five-case switch is five sequential API calls in the
worst case.

On any provider error the function `return false` — the case simply does not match. A network blip
therefore causes every case to fall through to `Otherwise`, and the program takes the default branch
with no error anywhere. Same failure mode as P-01, in the construct most likely to be inside a loop.

## P-05 — `Otherwise` fires by position, not last `S2` `OPEN`

`internal/ops/control_flow.go:27-33`

The `_`/`otherwise`/`default` case executes when the loop *reaches* it and nothing has executed yet.
In a Go `switch`, `default` position is irrelevant. Here, putting `Otherwise(...)` first means it runs
immediately and no other case is ever evaluated — a silent, total inversion of intent that looks
correct at the call site.

**Fix:** collect the default case and evaluate it only after all others fail.

## P-06 — `Match` has no error surface at all `S3` `OPEN`

`func Match(input any, cases ...types.Case)` returns nothing: not which case ran, not whether any ran,
not whether the LLM failed. Combined with P-04 there is no way to distinguish "no case matched" from
"the API was unreachable."

## P-07 — `Compose` silently discards every operation after the first `S1` `OPEN`

`internal/ops/pipeline.go:161-183`

```go
// For multiple operations, they need to be type-compatible
// This is a simplified version - in practice you'd need more sophisticated type handling
result, err := operations[0](input)
if err != nil { return zero, err }
return result, nil
```

`Compose(a, b, c)` runs `a` and returns. `b` and `c` never execute, no error, no log. The function
name is a promise the body does not keep. It is currently unreachable from outside the module (P-10),
which is the only reason this has not burned anyone.

## P-08 — `Pipeline` with `RetryFailed` and a zero `MaxRetries` skips every step `S1` `OPEN`

`internal/ops/pipeline.go:120-143`

```go
attempts := 1
if p.opts.RetryFailed && !step.Optional {
    attempts = p.opts.MaxRetries   // not MaxRetries + 1
}
for attempt := 0; attempt < attempts; attempt++ { ... }
```

`NewPipeline(name)` supplies `MaxRetries: 3`, but any caller who passes their own `PipelineOptions`
gets the zero value. `PipelineOptions{FailFast: true, RetryFailed: true}` therefore yields
`attempts = 0`: the loop body never runs, `stepErr` stays nil, `StepsExecuted` and `StepsFailed` are
both unchanged, and the pipeline proceeds to the next step with the previous value. Every step is
skipped and the result reports zero errors.

Separately, `MaxRetries: 1` means one attempt, i.e. no retry — off-by-one against the name.

## P-09 — `Pipeline` sleeps uncancellably and is untyped `S2` `OPEN`

`internal/ops/pipeline.go:141` uses `time.Sleep` between retries, ignoring the context the same
function checks at the top of every step. A cancelled pipeline still waits out its backoff.

Steps are `func(context.Context, any) (any, error)`: the composition primitive of a
generics-first library is `any` in and `any` out, so every step body starts with a type assertion.

## P-10 — Most of the procedural layer is unreachable `S2` `OPEN`

`Pipeline`, `Compose`, `Then`, `StateMachine`, `BatchProcessor`, and the entire fuzzy-switch API
(`Match`, `When`, `Like`, `Otherwise`) are implemented, unit-tested (`procedural_test.go`,
`pipeline_test.go`, `batch_test.go`), and exported from `internal/ops` only. No root-package wrapper
exists, so no external consumer can call any of them.

That is roughly a thousand lines of maintained, tested, dead-to-users code — including `BatchProcessor`,
which is the only chunking implementation in the repo and exactly what the collection ops need (C-06).

**Decide:** promote (with the fixes above) or delete. Keeping it internal and tested is the worst of
both — it pays maintenance cost and returns nothing.

---

# Family 7 — Fluent builder layer and the legacy/fluent duality

Surface: `internal/api/fluent` (`commonRequest`, `opRequest`, `directRequest` bases plus ~45 builders),
re-exported through `fluent.go`.

## F-01 — `Strict()` and `Smart()` cannot be expressed at all `S1` `OPEN`

`internal/types` (`Strict Mode = iota`, `Smart Speed = iota`), `internal/ops/negotiate.go`
`mergeNegotiateOptions` and every other `mergeXOptions`

```go
if user.Mode != 0        { defaults.Mode = user.Mode }
if user.Intelligence != 0 { defaults.Intelligence = user.Intelligence }
```

`Strict` **is** `Mode(0)` and `Smart` **is** `Speed(0)`. In a merge that treats zero as "unset", the
two most prominent modifiers in the README are indistinguishable from not calling them:

```go
schemaflow.Negotiating[Deal](c).Strict().Smart().Run()
// runs in TransformMode on the Fast model — the operation's defaults
```

This affects every operation with a `mergeXOptions` helper (`project`, `pivot`, `negotiate`,
`resolve`, `conform`, `derive`, `arbitrate`, `audit`, `interpolate`, `compose`). `Strict()` silently
becomes "whatever the default is", which also changes temperature (`config.GetTemperature`) and model
tier — the two things a user calls `Strict().Smart()` to control.

The same trap applies to numeric options: `MinSatisfaction > 0`, `MaxAlternatives > 0`, `TopN > 0` —
zero is never settable.

**Fix:** make the sentinel explicit — either pointer fields (`*Mode`), an `UnsetMode = iota` zero
value, or a bitmask of explicitly-set fields. Reordering the constants so `Strict` is non-zero is the
cheapest correct change, and it is a breaking change worth making before 1.0.

## F-02 — Unwired setters are silent no-ops `S1` `OPEN`

`internal/api/fluent/fluent_base.go:195-259`

`directRequest` guards every setter with `if r.setX == nil { return r.lift(r.opts) }` — a nil setter
means the call chains and does nothing. `newAdversarialNegotiationRequest`
(`fluent_advanced.go:75-101`) wires `setSteering`, `setIntelligence`, `setContext`, `setRequestID`,
and `setCorrelationID` but **not `setMode`**. So on `NegotiatingAdversarially(...)`, the methods
`.Strict()`, `.TransformMode()`, and `.Creative()` are dead — they compile, chain, and change nothing.

The pattern makes this class of bug invisible: there is no compile-time signal, no runtime warning,
and no test that would fail. Any future builder that forgets a setter fails the same silent way.

**Fix:** make the setters required (positional struct fields with a constructor that panics on nil at
init time), or return an error/panic in the guard instead of swallowing.

## F-03 — Eleven fluent entrypoints bypass the documented defaults `S2` `OPEN`

`internal/api/fluent/fluent_advanced.go:49,108,165,222,281,338,389,446,503,566,617`

```go
func Negotiating[T any](constraints any) NegotiateRequest[T] {
    return newNegotiateRequest[T](constraints, NegotiateOptions{})
}
```

Every advanced builder starts from a zero-value options struct rather than the `NewXOptions()`
constructor the direct API uses. The op-side merge rescues most fields, but only where a merge exists
and only for fields whose zero value is not meaningful — which, per F-01, excludes `Mode` and
`Intelligence`. The result is that the two public APIs do not agree on defaults, and which one a user
gets depends on which spelling they chose.

## F-04 — `Steer` overwrites and then gets concatenated with library prose `S2` `OPEN`

`fluent_base.go:103-108`, `internal/ops/options.go` `WithSteering`

`Steer` assigns rather than appends, so two `.Steer(...)` calls silently drop the first. The op then
joins the user's steering with its own generated instructions using `". "` (X-05), producing a single
run-on sentence in which the library's clauses can contradict the user's — and, in `Filter`'s case,
contradict the system prompt as well (C-02).

## F-05 — Three parallel builder bases for one concept `S3` `OPEN`

`commonRequest`, `opRequest`, and `directRequest` (`fluent_base.go`) implement the same eleven methods
three times, because the underlying options structs are inconsistent: some embed `CommonOptions`, some
embed `types.OpOptions`, some declare the fields inline. The builder layer is paying for an
inconsistency that belongs to `internal/ops`.

## F-06 — Two complete public APIs, no deprecation markers `S2` `OPEN`

`schemaflow.go` + `fluent.go` export 125 functions, 45 of them gerund-form builders and most of the
rest their direct-call twins. The README says the direct API is "compatibility-only," but there is not
a single `// Deprecated:` comment in the repo, so no linter, IDE, or `staticcheck` run will ever tell a
user they are on the legacy path. Both spellings appear in package doc comments and examples.

For a pre-1.0 library, carrying two full APIs doubles the surface that every other finding in this
document has to be fixed across.

## F-07 — Builders validate nothing until `Run()` `S3` `OPEN`

Options validation happens inside the op implementation, so a mis-built request (empty criteria, zero
items, out-of-range threshold) is only discovered after a call is set up. There is no `Validate()` or
dry-run on the builder, which is otherwise the natural place for it.

---

# Family 8 — Tools registry

Surface: `internal/tools` — 34 files, 86 registered tools across 15 categories, a `Registry`, and
OpenAI/Anthropic tool-spec exporters.

## G-01 — The entire tools package is unreachable by users `S2` `OPEN`

`internal/tools/*` has no root-package re-export. `Registry`, `Tool`, `Register`, `Execute`,
`GetOpenAITools`, and `GetAnthropicTools` cannot be imported by anything outside this module. Only
`examples/tools`, which lives inside the module, can call them.

86 tools, a registry with category sub-registries, and provider-specific spec exporters — all
maintained and tested, all unusable by the audience they were written for. This is the same problem as
P-10 at four times the size.

## G-02 — Half the tools are stubs, auto-registered next to the real ones `S2` `OPEN`

41 of 86 tools are stubs (48%), by file:

| File | Tools | Stubs |
| --- | --- | --- |
| `audio.go` | 6 | 6 |
| `messaging.go` | 6 | 6 |
| `exec.go` | 8 | 7 |
| `image.go` | 8 | 6 |
| `archive.go` | 5 | 4 |
| `http.go` | 8 | 3 |
| `finance.go` | 5 | 3 |
| `time.go` | 7 | 2 |
| `cache_security.go` | 7 | 2 |
| `database.go` | 5 | 1 |
| `file.go` | 10 | 1 |
| `calculate.go`, `compute.go`, `data.go`, `template.go` | 11 | 0 |

**Credit:** stubs set `IsStub: true`, carry "(stub)" in their description, and return a
`StubResult` with `{"stubbed": true}` metadata. That is honest design.

The problem is the default: every tool self-registers into the global `DefaultRegistry` at init, and
`GetOpenAITools()` exports all of them. Wire the registry to a model and it is offered
`text_to_speech`, `send_email`, and `vector_search` as if they worked; the model calls one and gets a
stub payload it must interpret. There is no `WithoutStubs()` filter and no default exclusion.

## G-03 — The `IsStub` flag is not reliable `S2` `OPEN`

`internal/tools/cache_security.go:386-405` — the `token` (JWT) tool returns
`StubResult("JWT token generation requires jwt-go integration...")` but does **not** set
`IsStub: true`, unlike `encrypt`/`decrypt` right below it. A consumer who filters on `IsStub` — the
only mechanism offered — still ships a fake JWT generator.

Wherever `IsStub` is the safety mechanism, it must be enforced mechanically (a test asserting that
every `StubResult` call site belongs to a tool with `IsStub: true`).

## G-04 — An unrestricted `shell` tool is registered by default `S1` `OPEN`

`internal/tools/exec.go:13-93`, `246`

```go
cmd = exec.CommandContext(ctx, "sh", "-c", command)   // or cmd /C on Windows
...
_ = Register(ShellTool)
```

A model-authored string is passed to a shell with no allowlist, no denylist, no sandbox, no dry-run,
no approval callback, and no opt-in. It is auto-registered into the global registry that
`CreateToolHandler()` and `GetOpenAITools()` expose, so the dangerous default is the only default.

The inconsistency is stark: `run_code` (`exec.go:95-106`) is deliberately stubbed with the description
"sandboxed environment (stub - security considerations)", while `shell` — strictly more powerful —
ships fully implemented and unmarked. Whatever reasoning stubbed `run_code` applies more strongly here.

**Fix:** require explicit opt-in (`tools.EnableShell(policy)`), take a command allowlist, and default
the global registry to safe tools only.

## G-05 — The tool layer stubs capabilities the ops layer already implements `S3` `OPEN`

`exec.go` stubs `classify`, `sentiment`, `translate`, `similarity`, and `semantic_search` — three of
which exist as working operations (`Classify`, `Translate`, `Similar`) a package away. Meanwhile
`embed` and `semantic_search` are stubs and embeddings exist nowhere in the library (see Gap-05), so
the two genuinely missing capabilities are represented only by placeholders.

## G-06 — Registration errors are discarded at init `S3` `OPEN`

`_ = Register(ShellTool)` and equivalents throughout. `Registry.Register` returns an error (duplicate
name), thrown away in package init, so a name collision silently keeps whichever tool won the race.

---

# Gaps — missing and weak capabilities

The findings above are defects in what exists. This section is what a typed LLM operations library is
expected to have and SchemaFlow does not. Each was verified absent, not assumed.

## Gap-01 — No streaming, anywhere `S2`

Zero occurrences of streaming in `internal/llm` or `internal/ops`. `Provider.Complete` returns a
whole `CompletionResponse`. Every `Summarizing`, `Generating`, or `Expanding` call blocks for the full
generation, so the library cannot back an interactive UI, and long generations have no progress
signal and no early cancellation (compounded by X-01).

## Gap-02 — No structured outputs, no tool calling, in the model path `S1`

`CompletionRequest` is `{Model, SystemPrompt, UserPrompt, Temperature, MaxTokens, ResponseFormat}`.
There is no `Tools`, no `ToolChoice`, no `ResponseSchema`, and no handling of tool-call responses
anywhere in the provider layer (0 matches for `tools`/`tool_choice`/`ToolCall`).

Two consequences: the generated type schema can never be enforced (I-05), and the 86-tool registry
(Family 8) cannot be attached to any operation. The library has a tools package and no way for a model
to call a tool.

## Gap-03 — No conversation, no multi-modal input `S2`

One system string plus one user string. No message history, no assistant turns, no images, audio, or
files. Any multi-turn behavior must be simulated by the caller concatenating strings, and every op is
therefore single-shot. `Asking`, `Negotiating`, and `NegotiatingAdversarially` in particular are
naturally multi-turn operations implemented as one round trip.

## Gap-04 — No prompt caching, while the cost tracker measures it `S2`

Zero occurrences of `cache_control` or prompt-cache parameters in the provider layer, yet
`TokenUsage.CachedTokens` and `CostInfo.CachedCost` are tracked, reported in `CostSummary`, and
documented in the README. The library reports on a feature it never requests, so those fields are
structurally zero for OpenAI and Anthropic alike.

Since every op re-sends a fixed system prompt plus `strengthenSystemPrompt`'s preamble (I-18), prompt
caching is exactly the optimization this design would benefit from most.

## Gap-05 — No embeddings or vector operations `S2`

Zero occurrences of embeddings outside a stub tool. That rules out the cheap, deterministic
implementations of several existing ops: `Similar` and `CheckingSimilarity` are LLM round trips where
cosine similarity would do; `Clustering` re-implements k-means-shaped work in a prompt;
`Deduplicate` and `Matching` are quadratic prompt problems that embeddings solve directly.

## Gap-06 — No supported way to test code that uses this library `S1`

The only injection point is `setLLMCaller` (`internal/ops/llm_helper.go:28`) — unexported. External
consumers cannot substitute a fake provider through the public API. `WithProviderInstance` exists on
`Client` but the ops read a package global, so it works only as a global side effect (I-06), and the
`local` provider "is not a credible integration substitute" by the backlog's own admission.

A library whose calls cost money and are nondeterministic must ship a first-class test double:
an exported fake provider, a record/replay cassette, or a `Provider` the caller passes per call.
Its absence pushes every downstream user toward either paid tests or hand-rolled interfaces around
this API.

## Gap-07 — No caching, deduplication, or idempotency `S2`

Identical inputs are billed every time. There is no response cache, no content-addressed memoization,
and no idempotency key — even though a `cache` and a `memoize` tool exist in the unreachable tools
package.

## Gap-08 — No concurrency in operations that need it `S2`

Two `go func(` sites exist in `internal/ops` (`batch.go:136`, `pipeline.go:232`). Everything else is
serial, including the N-call paths: `NormalizeBatch` (D-14), `sortByScoringFallback` (C-05), and any
per-item loop. There is no bounded worker pool, no `errgroup`, and no concurrency option.

## Gap-09 — No rate limiting, circuit breaking, or provider failover `S2`

Retry with backoff is the entire resilience story (and it classifies errors by substring, I-12). There
is no client-side rate limiter, no circuit breaker, no queue, and no fallback provider — despite the
library supporting eight providers, which is the natural precondition for failover.

## Gap-10 — No token accounting before the call `S1`

Nothing counts or estimates tokens anywhere. Ops marshal arbitrarily large inputs into a prompt and
discover the limit as a truncated completion or a provider error (C-06, I-09). A library that owns
prompt construction should own the budget: estimate, chunk, or refuse.

## Gap-11 — No middleware, hooks, or interception `S2`

There is no way to observe or modify a request before it goes out: no prompt-rewrite hook, no PII
scrubber on egress (in a library that ships redaction), no per-request budget veto, no audit callback.
The only extension point is registering a whole `Provider` implementation.

## Gap-12 — No error taxonomy for programmatic handling `S2`

Zero sentinel errors (`var ErrX = errors.New(...)`) in `internal/types`. Each op has a bespoke error
struct, but there is no `ErrRateLimited`, `ErrAuth`, `ErrParse`, or `ErrTruncated` to match with
`errors.Is`. Callers who want to branch on failure kind must string-match — the same weakness the
library's own retry logic has (I-12).

## Gap-13 — Prompts are unversioned inline literals `S2`

Every prompt is a string literal inside a function. There is no prompt registry, no version, no way
for a consumer to override one, and no golden tests over prompt text. A prompt edit is invisible in
release notes and silently changes behavior for every downstream user — and because response format is
inferred from prompt wording (I-04), a prompt edit can also silently disable JSON mode.

## Gap-14 — Determinism controls are not exposed `S3`

Temperature is derived from `Mode` and cannot be set directly; there is no seed parameter, no
`top_p`, and no way to request greedy decoding. For a library that markets `Strict`, the strictest
available setting is a hardcoded `0.1`.

---

# Gaps — control flow

Called out separately because this is where the library's ambitions (`docs/engineering/plans/`) are
furthest ahead of the code. Today the control-flow surface is `Decide`, `Guard`, `Pipeline`, and
`Match` — two of which are public, all four of which have S1 defects (Family 6).

## CF-01 — No repair loop on validation or parse failure `S1`

The defining feature of typed LLM libraries is: parse fails → feed the error back to the model → retry
→ succeed. SchemaFlow retries **transport** errors only. A malformed JSON body, a missing required
field, or a category outside the allowed set is terminal (`internal/ops/core.go:188-207`,
`analysis.go:188-196`). The retry machinery already exists in `CallLLM`; it is simply not wired to
validation outcomes.

This is the highest-value missing feature in the library, and it composes with X-05: once
post-conditions exist, a failed post-condition is exactly the retry trigger.

## CF-02 — No escalation between intelligence tiers `S2`

`Smart`/`Fast`/`Quick` is a good axis, but nothing uses it dynamically. The natural pattern —
try `Quick`, escalate to `Smart` when confidence is low, the post-condition fails, or the result is
empty — cannot be expressed. Every call is fixed-tier at build time.

## CF-03 — No sampling, voting, or self-consistency `S2`

No best-of-N, no majority vote across samples, no ensemble across providers. For a library that
returns a `Confidence` field on nearly every result (D-06), the obvious honest source of that number —
agreement across N samples — is unavailable.

## CF-04 — No map-reduce over collections `S1`

There is no chunk → per-chunk op → merge primitive. `BatchProcessor` chunks, but only for `Extract`
and only internally (P-10). This is the direct cause of C-06: the collection ops cannot handle
collections larger than a single context window, and there is no supported way for a user to build
around it.

## CF-05 — No iterate-until-converged `S2`

`Critique` produces issues and `Rewrite` fixes text, but nothing loops one into the other until a
quality bar is met. The same is true of `Validate` + `AutoCorrect`, which produces a correction it
never re-validates.

## CF-06 — No checkpointing or resume `S2`

`PipelineOptions.SaveProgress` is declared and unimplemented (X-04). A ten-step pipeline that fails at
step nine re-runs and re-pays for steps one through eight. There is no run ID, no persisted state, no
resume entry point — while `docs/engineering/plans/workflowengineplan.md` specifies durable timers,
leases, and canonical workflow events at length.

## CF-07 — No human-in-the-loop gate `S2`

No approval step, no pause/resume, no confirmation callback before a destructive action — including
before the `shell` tool executes (G-04). The design docs treat approvals as a first-class node type;
the code has no notion of them.

## CF-08 — No budget- or deadline-aware control flow `S2`

`SetBudget` cannot stop anything (I-11), and there is no per-run cost ceiling, no "stop when spend
exceeds X" hook, and no deadline propagation (X-01). A runaway loop over a large collection is
unbounded in both time and money.

## CF-09 — Composition is linear and untyped `S2`

`Pipeline` is a straight line of `func(context.Context, any) (any, error)` steps. There is no DAG, no
fan-out/fan-in, no conditional edge, no compensation on failure — and `Compose` runs only its first
argument (P-07). In a library built on generics, the composition primitive is the one place with no
type safety at all.

## CF-10 — `GuardResult.RetryAfter` is declared and never set `S3`

`internal/ops/procedural.go:139` — the field that would carry backpressure is always nil. There is no
backoff signal anywhere in the control-flow surface.

---

# Synthesis

## What holds up

An adversarial review that reports only failures is not calibrated. These are load-bearing and correct:

- **The core bet.** Making the Go type the prompt contract is the right idea for this language, and
  the three-layer separation (root facade / fluent builders / `internal/ops`) is a sound structure that
  lets the internals be rewritten without breaking users. Every fix below is reachable without
  changing the public shape.
- **`Classify`** validates the model's answer against the allowed set and errors when it does not
  match (`analysis.go:178-196`). This is the post-condition pattern the rest of the library needs.
- **`Parse`** tries real parsers before the model and treats the LLM as a fallback (`parse.go:137-165`).
  This is the deterministic-first pattern `Validate` should adopt.
- **`Sort`'s scoring fallback** scores items independently and sorts in Go, preserving item identity
  (`collection.go:385-453`). It is a better algorithm than the primary path.
- **`Audit`** computes its summary aggregates in Go rather than asking the model for counts
  (`audit.go:303`).
- **Tool stubs are honestly labelled** with `IsStub`, "(stub)" descriptions, and `stubbed: true`
  metadata — a discipline most projects skip.
- **Request tracking** (`internal/requesttracking`) is a clean, complete subsystem with inject/extract
  and context propagation. It is undermined only by the ops that drop context (X-01).

The good patterns are all present in the codebase already. The problem is that they are exceptions
rather than the shared skeleton.

## Five themes

**1. Fail-open by default.** When something goes wrong, the library returns a plausible success. A
validator returns `valid` on a parse failure (A-01); `Decide` takes branch zero when the API is down
(P-01); `Match` falls to the default branch on a network error (P-04); a missing API key returns mock
text (I-01); `SummarizeWithMetadata` invents a confidence and drops its metadata (T-02); `Parse`
returns an empty struct as success (D-11); `Filter` collapses to one item (C-03);
`RedactWithResult` reports that nothing was redacted (T-09). In each case `err == nil`.

This is the single most damaging pattern in the codebase, because it is invisible in production and
inverts the meaning of the return value.

**2. Numbers that look measured and are not.** `0.3` if the response starts with `{` (D-06); `0.7`
because the fallback needed a value (T-02); `+0.2` because the completion was over twenty characters
(T-05); `(chunk-1) * 100` tokens saved (C-09); Haiku pricing for an Opus call (I-02). Alongside them
sit dozens of `Confidence`, `TrustScore`, `Severity`, and `Quality` floats that are model self-report
presented as instrumentation (D-09). A caller cannot tell which is which.

**3. Constraints are prose; results are unchecked.** Options are compiled into English and appended to
the prompt, and no operation verifies its own output (X-05). `Choose` does not check membership
(C-01); `Filter` does not check provenance (C-02); `MinConfidence` filters in one op out of five
(A-02); `Exclude` is documented as privacy filtering and is a suggestion (D-08); twelve options do
nothing at all (X-04).

**4. Sixty-five copies of one procedure.** No shared execution skeleton (D-15), so a single mistake is
replicated: 31 ops discard the caller's context (X-01), two different JSON cleaners exist (X-02), and
each op re-derives response format, error wrapping, and logging. Fixing anything cross-cutting today
means editing sixty-odd files.

**5. Large, maintained, unreachable surfaces.** The tools registry (86 tools, G-01), most of the
procedural layer (P-10), and `Deduplicate` (A-10) are implemented, tested, and impossible for a user
to call. That is thousands of lines paying maintenance cost and returning nothing — while the
capabilities users would actually want (structured outputs, repair loops, chunking, a test double)
are missing.

## S1 index

| ID | Finding |
| --- | --- |
| I-01 | Missing API key silently yields mock data |
| I-02 | Cost tracking reports wrong dollar figures |
| I-03 | Non-OpenAI providers receive OpenAI model IDs |
| I-04 | JSON mode chosen by substring-matching user data |
| I-05 | Type schema never sent to the provider as a schema |
| X-01 | 31 ops discard the caller's context |
| X-03 | Errors carry raw user payloads into logs |
| D-01 | Schema does not recurse into nested types |
| D-02 | `json:"-"` fields are sent to the model |
| D-05 | `Strict()` enforces nothing |
| D-06 | Fabricated confidence values |
| D-08 | `Project.Exclude` sold as privacy filtering |
| D-09 | Model self-report presented as an audit trail |
| D-11 | `Parse` succeeds with empty data |
| T-02 | `SummarizeWithMetadata` fails open with invented confidence |
| T-05 | Completion confidence is astrology |
| T-06 | Byte slicing corrupts multi-byte text |
| T-07 | `Redact` over-redacts on name substrings |
| T-08 | Redaction regexes miss the data they name |
| T-09 | `RedactWithResult` reports nothing |
| T-11 | Jumble redaction is reversible |
| T-13 | `RedactLLM` trusts model character offsets |
| A-01 | `Validate` returns valid when the model says invalid |
| C-01 | `Choose` does not verify membership |
| C-02 | `Filter` returns model-authored objects; contradictory instructions |
| C-06 | Collection ops have an undocumented size ceiling |
| P-01 | `Decide` silently takes branch zero |
| P-02 | `Decide`'s first parameter is named `ctx` and is not one |
| P-04 | `Match` fails to silent non-match |
| P-07 | `Compose` runs only the first operation |
| P-08 | `Pipeline` skips every step on zero `MaxRetries` |
| F-01 | `Strict()` and `Smart()` are unrepresentable |
| F-02 | Unwired builder setters are silent no-ops |
| G-04 | Unrestricted `shell` tool registered by default |
| Gap-02 | No structured outputs or tool calling |
| Gap-06 | No supported way to test against the library |
| Gap-10 | No token accounting before the call |
| CF-01 | No repair loop on parse or validation failure |
| CF-04 | No map-reduce over collections |

---

# Target API shape

The findings above are mostly not independent. Roughly sixty of them are the same three mistakes
replicated across sixty-five hand-written operations: no shared execution path, no post-conditions,
and options that cannot express their own defaults. Fixing them one at a time is sixty patches that
will drift apart again. This section defines the shape to fix them *into*, so every entry in the
remediation plan is "port to the new core" rather than "patch in place."

## Why this shape

Design decisions here follow established practice rather than invention, because the review found
enough novelty already.

| Decision | Precedent |
| --- | --- |
| Params struct for operation inputs, variadic `RequestOption` for cross-cutting knobs | Google Go style guide (option struct when most callers set something; variadic when most do not); both official LLM SDKs use exactly this pair |
| Per-call options that also work at client construction | `openai-go` / `anthropic-sdk-go` `RequestOption` |
| Explicit unset vs zero | `openai-go` `param.Opt[T]` with `omitzero`, Go 1.24 |
| Validation failure feeds the error back and retries | `instructor-go`: automatic retry on validation failure with descriptive error, usage aggregated across attempts |
| Prompts as versioned, testable artifacts; repair rather than reject near-miss output | BAML: prompts as functions, schema-aligned parsing |
| Resilience as a decorator chain | `http.RoundTripper` middleware, the standard Go idiom for retry / rate limit / cache / metrics |
| Sentinel + typed errors, `errors.Is` / `errors.As` | Go style guide; `*openai.Error` with `StatusCode` |

Go 1.24 is already the module's minimum, so `omitzero`, generics, and `Opt[T]` are all available
today. OpenTelemetry is already a direct dependency, so the middleware chain has a metrics sink to
target without new dependencies.

## Primitive 1 — `Op`: an operation is data

```go
type Op[In, Out any] struct {
    Name       string                                   // "extract", used for metrics + prompt registry
    Prompt     func(In, Options) (system, user string)
    Format     Format                                   // Text | JSONObject | JSONSchema
    Schema     func() *jsonschema.Schema                // nil for text operations
    Decode     func([]byte) (Out, error)
    Invariants []Invariant[In, Out]
}

func Run[In, Out any](ctx context.Context, c *Client, op Op[In, Out], in In,
    opts ...RequestOption) (Result[Out], error)
```

`Run` owns, in exactly one place: context propagation, response-format selection, provider dispatch,
retry, decoding, invariant checking, repair, usage accounting, and telemetry. An operation shrinks to
a prompt builder plus a decoder plus a list of invariants.

Closes by construction: **X-01** (thirty-one context drops become one call site), **X-02** (one JSON
cleaner), **I-04** (`Format` is a declared field, not a substring search over user data), **D-15**
(the sixty-five-fold duplication), **X-03** (one error-construction path that can redact payloads).

## Primitive 2 — `Invariant`: post-conditions the library actually checks

```go
type Invariant[In, Out any] struct {
    Name  string
    Check func(In, Out) error   // nil = pass; the error text is what the model sees on repair
}
```

Written once, attached per operation:

```go
MemberOf[T]()          // Choose: the result must be one of the inputs
SameMultiset[T]()      // Sort: same items, reordered — not "same count"
SubsetOf[T]()          // Filter: every returned item came from the input
CoversExactlyOnce[T]() // Cluster: clusters + outliers partition the input
AtMost(n)              // TopN, MaxCategories, MaxIssues
WithinLength(n, unit)  // MaxLength / TargetLength
AtLeastConfidence(x)   // MinConfidence, in all five operations that declare it
ExcludesValues(fields) // Project.Exclude, Redact categories
CategoryIn(set)        // what Classify already does by hand
```

This is the mechanism that converts "the option is prose" from a per-operation defect into a
declaration. Closes: **X-05**, **C-01**, **C-02**, **C-04**, **C-07**, **A-02**, **A-03**, **T-03**,
**D-08**, and makes **X-04** (dead options) mechanically detectable — an option with no invariant and
no prompt reference is dead by definition.

## Primitive 3 — the repair loop

```go
for attempt := 1; attempt <= budget.Repairs+1; attempt++ {
    out, err := op.Decode(body)
    if err == nil {
        err = checkInvariants(op.Invariants, in, out)
    }
    if err == nil {
        return Result[Out]{Value: out, Meta: meta}, nil
    }
    if !repairable(err) || attempt == budget.Repairs+1 {
        return zero, err
    }
    meta.Repairs = append(meta.Repairs, err.Error())
    req = req.WithRepair(err)   // "Your previous answer failed this check: <error>. Return a corrected answer."
}
```

The retry machinery already exists in `CallLLM`; it is wired to transport errors only. Wiring it to
decode and invariant failures is the single most valuable missing feature (**CF-01**), and it is only
possible once Primitive 2 exists — without invariants there is nothing to repair against.

Usage from every attempt is summed into `Meta`, so repair cost is visible rather than hidden (the
mistake **C-05** makes today, where a fallback silently multiplies spend by N).

## Primitive 4 — `RequestOption`, applied at client or call

```go
type RequestOption func(*Config)

client := schemaflow.New(provider,
    option.Tier(schemaflow.Fast),
    option.Timeout(30*time.Second),
)

res, err := schemaflow.Extract[Person](ctx, client, input,
    option.Mode(schemaflow.Strict),      // overrides client default for this call only
    option.Tier(schemaflow.Smart),
    option.Model("gpt-5.4"),             // programmatic, not an env var
    option.MaxOutputTokens(8000),
    option.Steer("prefer explicit evidence over inference"),
)
```

The same option type at both levels is what makes this pattern pleasant in the official SDKs. It also
removes the order-dependence in `Client.With*` (**I-07**), the env-only configuration
(**I-08**, **I-09**, **Gap-14**), and the global-provider coupling (**I-06**) — the client is passed
explicitly, so two clients with two providers coexist.

## Primitive 5 — explicit unset

```go
const ( ModeUnset Mode = iota; Strict; Transform; Creative )
const ( TierUnset Speed = iota; Smart; Fast; Quick )

type Opt[T any] struct{ v T; set bool }
func Set[T any](v T) Opt[T]
func (o Opt[T]) Get() (T, bool)
```

Renumbering the enums so zero means unset fixes **F-01** for `Mode` and `Speed`; `Opt[T]` fixes it for
every numeric option that currently cannot be set to zero (`TopN`, `MinConfidence`,
`MinSatisfaction`, `MaxAlternatives`). This is a breaking change and must land before 1.0 — today
`.Strict().Smart()` is silently unrepresentable on roughly ten operations.

## Primitive 6 — middleware chain

```go
type Handler    func(context.Context, Request) (Response, error)
type Middleware func(Handler) Handler

client := schemaflow.New(provider, option.Use(
    mw.RateLimit(60),          // Gap-09
    mw.Retry(3, mw.Jitter),    // I-12: typed classification, jittered backoff
    mw.Cache(store),           // Gap-07
    mw.Budget(5.00),           // I-11 / CF-08: returns ErrBudgetExceeded before spending
    mw.RedactEgress(rules),    // Gap-11 / X-03
    mw.Metrics(otelSink),      // telemetry export
    mw.Fallback(providerB),    // Gap-09: provider failover
))
```

One construct replaces the scattered resilience story and gives the extension point the library has
no equivalent of today.

## Primitive 7 — one result envelope

```go
type Result[T any] struct {
    Value T
    Meta  Meta
}

type Meta struct {
    RequestID, CorrelationID string
    Provider, Model          string
    Usage                    TokenUsage
    Cost                     Cost   // Estimated bool + PricingSource — never a silent zero
    Attempts                 int
    Repairs                  []string
    Strategy                 string // "single-shot" | "chunked" | "per-item-scored"
    Elapsed                  time.Duration
}
```

`Confidence` does not appear. Where a number is genuinely measured (sample agreement under
`flow.Vote`, provider logprobs) it is named for its source; where the model asserts it, the field is
`Judgement.ModelReported` and can never be mistaken for instrumentation. Closes **D-06**, **D-09**,
**T-02**, **T-05**, **C-09**, and makes **I-02** expressible (an unpriced call reports
`Estimated: false, PricingSource: ""` rather than `$0.00`).

Thirty bespoke result structs collapse into one shape plus an operation-specific `Value`.

## Primitive 8 — error taxonomy

```go
var (
    ErrNoProvider, ErrAuth, ErrRateLimited, ErrTruncated,
    ErrDecode, ErrInvariant, ErrBudgetExceeded error
)

type APIError struct {
    StatusCode       int
    Provider, Model  string
    Body             []byte
}

type InvariantError struct{ Op, Invariant, Detail string }
```

Retry classification switches from `strings.Contains(msg, "status 429")` to `errors.As` on a typed
status (**I-12**), and callers get real branching (**Gap-12**). `ErrTruncated` in particular makes
**I-09** and **C-06** diagnosable instead of appearing as a JSON parse error.

## Primitive 9 — control flow as combinators

Every combinator takes an `Op` and returns an `Op`, so they compose and each is written once:

```go
flow.Escalate(op, Quick, Smart)                   // CF-02
flow.Vote(op, 3, flow.Majority)                   // CF-03 — and an honest confidence number
flow.MapReduce(op, chunk.BySize(4000), merge)     // CF-04 + C-06
flow.Until(op, critique.Passes, flow.Max(3))      // CF-05
flow.Checkpoint(store, runID)                     // CF-06
flow.Approve(gate)                                // CF-07, including before the shell tool
flow.Fallback(opA, opB)
```

This is the alternative to building the engine described in
`docs/engineering/plans/workflowengineplan.md`. It reaches most of the same capability with library
primitives instead of a control plane, and it replaces four one-off constructs (`Decide`, `Guard`,
`Match`, `Pipeline`) that each carry S1 defects.

## What a call looks like after

```go
// before — global provider, context ignored, options are prose, nothing verified
best, err := schemaflow.Choosing(products).By("lowest total cost").Fast().Run()

// after — explicit client, context honored, membership enforced, cost reported
res, err := schemaflow.Choose(ctx, client, products,
    option.By("lowest total cost"),
    option.Tier(schemaflow.Fast),
)
// res.Value is guaranteed to be one of `products`
// res.Meta.Cost.TotalCost is real or explicitly marked unpriced
// res.Meta.Repairs shows whether the model needed correcting
```

The fluent spelling survives as sugar over the same core, with one signature change:

```go
res, err := schemaflow.Choosing(products).By("lowest total cost").Fast().Run(ctx)
```

`Run()` must take a context. There is no way to honor cancellation otherwise, and the builder is
where the context naturally arrives.

## Breaking changes this shape requires

All of these are cheaper now than after 1.0:

| Change | Findings it makes fixable |
| --- | --- |
| `Run()` → `Run(ctx)` | X-01 |
| `Mode` / `Speed` enums renumbered so zero is unset | F-01 |
| `Confidence float64` removed from result structs | D-06, D-09, T-02, T-05 |
| Per-operation result structs → `Result[T]` + `Meta` | D-09, C-09, I-02 |
| `Decide(ctx any, ...)` → `Decide(ctx context.Context, situation any, ...)` | P-01, P-02 |
| `Redact[T](input, opts ...interface{})` → typed options | T-10 |
| `Complete(ctx, provider, ...)` loses its provider parameter | T-04 |
| Direct-call API marked `// Deprecated:` | F-06 |

---

# Remediation plan

Ordered by risk removed per unit of work. Phases 1 and 2 are the same work as "build the shape above";
everything else is a port onto it.

## Phase 0 — Stop failing open

Days, no architecture change. Every item is a deletion or a few lines, and none of them depend on the
new core. Ship this first regardless of what happens to the rest of the plan.

1. Delete the text-inference fallback in `Validate` (**A-01**) and the JSON fallback in
   `SummarizeWithMetadata` (**T-02**). A parse failure in a validator is an error.
2. Return errors from `Decide` instead of branch zero (**P-01**); give `Match` an error surface
   (**P-04**, **P-06**); make `Otherwise` evaluate last (**P-05**).
3. Fail `Init`/`InitWithEnv` when no key is present and the provider is not explicitly `local`
   (**I-01**), and let `InitWithEnv` return a real error (**I-13**).
4. Fix `json:"-"` handling in the schema generator (**D-02**) — three lines, and it saves tokens on
   every call.
5. Wire `setMode` on the adversarial builder (**F-02**) plus a test asserting every `directRequest`
   has all six setters non-nil.
6. Fix `Pipeline`'s `attempts` off-by-one (**P-08**); make `Compose` correct or delete it (**P-07**).
7. Gate `shell` behind explicit opt-in and remove it from the default registry (**G-04**); add the
   test that every `StubResult` call site belongs to a tool with `IsStub: true` (**G-03**).
8. Mark `Redact`/`RedactLLM` not-production-ready in doc comments and README until
   **T-07**/**T-08**/**T-09**/**T-11**/**T-13** are fixed; delete the "privacy filtering" framing from
   `Project`'s documentation (**D-08**).
9. Delete every fabricated number: `CalculateParsingConfidence`, the `Threshold - 0.1` literal,
   `estimateCompletionConfidence`, the `0.7` fallback, `TokensSaved`
   (**D-06**, **T-05**, **C-09**). Rename model-reported fields `ModelReported*` (**D-09**).
10. Delete or implement the dead options (**X-04**, **C-10**, **D-10**, **CF-10**).

## Phase 1 — Land the core: `Op`, `Run`, `Invariant`, `RequestOption`

One to two weeks. Build Primitives 1–5 and 7–8 alongside the existing code; do not port anything yet
except one operation (`Extract`) as the proof.

Deliverables: `Op[In, Out]`, `Run`, `Invariant`, the option package, `Result[T]`/`Meta`, the error
taxonomy, and the invariant library (`MemberOf`, `SameMultiset`, `SubsetOf`, `CoversExactlyOnce`,
`AtMost`, `WithinLength`, `AtLeastConfidence`, `ExcludesValues`).

Closes on landing: **X-01**, **X-02**, **X-03**, **I-04**, **I-06**, **I-07**, **I-08**, **I-09**,
**I-12**, **F-01**, **F-05**, **F-07**, **D-15**, **Gap-12**, **Gap-14**.

## Phase 2 — Port the operations, attaching invariants

Two to three weeks, family by family, in this order — highest defect density first:

1. **Collections** (`Choose`, `Filter`, `Sort`, `Rank`, `Cluster`): port to index-based responses
   (**C-08**) and attach `MemberOf` / `SubsetOf` / `SameMultiset` / `CoversExactlyOnce`. Closes
   **C-01**, **C-02**, **C-03**, **C-04**, **C-07**, and removes most of **C-06** because output
   length stops scaling with item size.
2. **Analysis** (`Classify`, `Score`, `Validate`, `Verify`, `Audit`, `Critique`): attach
   `AtLeastConfidence` (**A-02**), `CategoryIn` (already hand-written in `Classify` — becomes the
   shared invariant), and give `Validate` a deterministic rule path before the model call
   (**A-05**, following `Parse`'s existing pattern). Fixes **A-06**, **A-07** in passing.
3. **Structured data** (`Extract`, `Transform`, `Project`, `Pivot`, `Enrich`, `Normalize`): attach
   `ExcludesValues` and filter excluded fields out of the marshalled input deterministically
   (**D-08**), compute `Lost`/`Inferred` in Go (**D-09**). Fix `NormalizeInput`'s `Stringer`
   precedence (**D-04**) and batch `NormalizeBatch` (**D-14**).
4. **Text** (`Summarize`, `Rewrite`, `Translate`, `Expand`, `Complete`): collapse the
   `X`/`XWithMetadata` twins into one operation returning `Result[T]` (**T-01**), attach
   `WithinLength` (**T-03**), switch to rune-safe slicing (**T-06**).
5. **Redaction**: rebuild against `ExcludesValues` plus a real pattern library; replace jumble with a
   non-invertible transform or delete the strategy (**T-11**); locate LLM spans by matched substring
   rather than model-reported offsets (**T-13**); implement the redaction report (**T-09**); narrow
   the field-name matcher to word boundaries (**T-07**).

## Phase 3 — Make the types load-bearing

1. Recurse the schema generator into nested structs and slices, with depth cap and cycle detection
   (**D-01**), and emit real JSON Schema. Keep a compact rendering for the prompt path — full JSON
   Schema is token-expensive, which is the reason BAML uses a condensed format.
2. Send that schema as `json_schema` strict mode where the provider supports it (**I-05**,
   **Gap-02**); record which enforcement path was used in `Meta`.
3. Turn on the repair loop (Primitive 3) now that invariants exist (**CF-01**). Report attempts and
   repairs in `Meta`.
4. Make `Strict()` mean something: with real schema enforcement plus `Invariant`-declared
   requiredness, `Strict` becomes a checkable contract rather than a prompt sentence
   (**D-05**, **D-03**).
5. Add token estimation before dispatch, and `chunk.BySize` + `flow.MapReduce` for oversized inputs
   (**Gap-10**, **C-06**, **CF-04**), reusing `BatchProcessor`.

## Phase 4 — Make the operational claims true

1. **Pricing**: per-model tables, `RegisterPricingModel`, and an explicit unpriced signal instead of a
   silent zero or a substituted price (**I-02**). Bound and index `costHistory` (**I-10**). Budgets
   become edge-triggered and optionally enforcing via `mw.Budget` (**I-11**).
2. **Providers**: per-provider default-model map with construction-time validation (**I-03**);
   `mw.Fallback` for failover (**Gap-09**).
3. **Testing**: ship `schemaflowtest` with an exported fake provider and a cassette recorder
   (**Gap-06**). This is the largest adoption blocker in the document — without it nobody can run CI
   against code that uses this library.
4. **Streaming** on the `Provider` interface (**Gap-01**), and prompt caching (**Gap-04**) now that
   `Meta.Cost` already reports cached tokens.
5. **Middleware** (Primitive 6) for rate limiting, caching, egress redaction, and metrics export
   (**Gap-07**, **Gap-09**, **Gap-11**).
6. Clean up the infrastructure papercuts while the file is open: **I-14** (unsynchronized globals),
   **I-15** (dead field), **I-16** (`WithDebug` asymmetry), **I-17** (duplicate filter helpers),
   **I-18** (unconditional prompt tax → make it opt-out and measure it).

## Phase 5 — Control flow

Build Primitive 9 on top of the ported operations, then delete or reimplement the four legacy
constructs. Closes **CF-02**, **CF-03**, **CF-05**, **CF-06**, **CF-07**, **CF-08**, **CF-09**,
**P-03**, **P-09**, **P-10**.

Sequencing note: combinators are worth building only after Phase 2, because `flow.Vote` needs
comparable results, `flow.Escalate` needs a failure signal that is not "the model said something
weird," and `flow.MapReduce` needs invariants to validate the merge.

## Phase 6 — Decide the shape of the product

Decisions, not bugs. Each halves or doubles the maintenance surface, so make them before 1.0:

- **The unreachable code.** Promote or delete the tools registry (**G-01**) and the procedural layer
  (**P-10**). Promoting tools is only worth it alongside tool calling in the provider path
  (**Gap-02**); until then the registry is 86 tools no model can call.
- **The verb catalogue.** Five operations share one shape (**A-08**), and several families are prompt
  variations of each other. With invariants and combinators in place, a core of roughly twelve
  operations plus `Steer` expresses `Critique`, `Audit`, `Verify`, `Arbitrate`, `Negotiate`,
  `Resolve`, `Conform`, `Derive`, and `Pivot`. Ship those as a `recipes` package built from the core
  — named and documented, but not sixty-five hand-written procedures each carrying its own copy of
  every bug in this document.
- **The dual API.** Pick one spelling and mark the other `// Deprecated:` so tooling says so
  (**F-06**). Expose `Deduplicate` or delete it (**A-10**); document or delete `Format`/`Merge`.
- **Prompts as artifacts.** Name, version, and expose them for override, with golden tests
  (**Gap-13**). This is what makes a prompt edit a reviewable change instead of a silent behavior
  change for every downstream user.
- **The design documents.** `workflowengineplan.md` and `SCHEMAFLOWDSLSPEC.md` describe a durable
  workflow engine that does not exist here, and Primitive 9 is a deliberate decision *not* to build
  it. Either scope them into the roadmap explicitly or move them out of `docs/engineering/plans/`, so
  no reader mistakes ambition for behavior.

## Where each S1 gets closed

| Phase | S1 findings closed |
| --- | --- |
| 0 | A-01, P-01, P-04, P-07, P-08, I-01, D-02, F-02, G-04, T-02, T-05 (partial), D-06 (partial) |
| 1 | X-01, X-03, I-04, F-01 |
| 2 | C-01, C-02, D-08, D-09, D-11, T-06, T-07, T-08, T-09, T-11, T-13, A-02 |
| 3 | I-05, D-01, D-05, C-06, CF-01, CF-04, Gap-02, Gap-10 |
| 4 | I-02, I-03, Gap-06 |
| 5 | P-02 |
| 6 | A-08 (design), G-01, P-10 |

## How to use this document

Each finding has a stable ID. When one is addressed, change its status tag from `OPEN` to `FIXED`
with the commit SHA, or to `WONTFIX` with a one-line reason. Do not delete entries: the record of what
was consciously accepted is as useful as the record of what was fixed.

When implementing, cite the finding IDs in the commit message. The phase tables above are the
checklist; the "Target API shape" section is the contract those commits are converging on.

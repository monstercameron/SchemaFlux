# SchemaFlux Production Readiness — Task List

Authority: this file answers **in what order**. `AGENTS.md` answers **how**.
`docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md` answers **why** — every task below
cites the finding or gap ID it closes.

Goal: production-ready, stable, tested. That means three things concretely — no operation
fails open, every result is verified against a declared contract, and every claim in the
README is backed by a test that runs in CI.

## How to read this

- **Milestones (`M00`–`M10`) are ordered by dependency**, not by importance. Do not start
  a milestone whose predecessors are open unless the task says otherwise.
- **Task IDs are stable.** Never renumber. A dropped task becomes `WONTFIX` with a reason.
- **Every task carries a Verify line.** A task is not complete until that verification
  passes and its evidence is recorded beneath the item.
- `[BLOCKED]` marks work that cannot proceed for an external reason, named inline.
- `[LIVE]` marks work requiring a funded provider account (see B-01).

## Status legend

`[ ]` open · `[~]` in progress · `[x]` complete, verified · `[!]` blocked · `[-]` wontfix

---

# M00 — Blockers and preconditions

Nothing in M02 or M07 can be verified end to end until B-01 clears. Everything else in this
file proceeds without it.

- [!] **B-01** — OpenAI account has no credits. `POST /v1/responses` returns
  `insufficient_quota` / `credit_balance_exhausted`; `GET /v1/models` still works, which is
  why model discovery succeeded. Every `[LIVE]` task below is blocked on this.
  *Verify:* a 1-token `/v1/responses` call returns HTTP 200.
- [x] **B-02** — `.env` defines the key as `OPENAI`, which no code path reads. The library
  looks for `SCHEMAFLUX_OPENAI_API_KEY`, `OPENAI_API_KEY`, then `SCHEMAFLUX_API_KEY`.
  Rename the key in `.env`, **or** add `OPENAI` to the resolution chain in
  `providerAPIKeyEnvVars`. Prefer renaming — a two-name alias invites drift.
  *Verify:* `Init` with only `.env` present selects the OpenAI provider, not the mock.
- [x] **B-03** — `InitWithEnv(paths ...string)` accepts paths and ignores them (I-13); it
  never loads a `.env` file at all. `github.com/joho/godotenv` is already an indirect
  dependency. Load the named paths, default to `./.env`, and return a real error.
  *Verify:* unit test — `InitWithEnv("testdata/x.env")` populates config; a malformed file
  returns a non-nil error. Depends on B-02.
- [ ] **B-04** — Establish the live-test gate before any `[LIVE]` task runs: provider-backed
  tests compile always and execute only when `SCHEMAFLUX_LIVE_TESTS=1` is set. Never let a
  default `go test ./...` reach a paid endpoint.
  *Verify:* `go test ./...` with the variable unset makes zero network calls (assert with a
  transport that fails the test on dial).

---

# M01 — Stop failing open

Ships independently of the redesign. Every item is a deletion or a few lines, and each one
removes a way the library currently reports success while being wrong. Do this first
regardless of what happens to the rest of the plan.

## Validators and verdicts

- [x] **F-001** — Delete the text-inference fallback in `Validate`
  (`internal/ops/extended.go:314-325`). `strings.Contains(strings.ToLower(response), "valid")`
  matches the substring inside `"invalid"`, so a model saying the data is invalid returns
  `Valid: true` with `err == nil`. Closes **A-01**.
  *Verify:* table test feeding `"The data is invalid because ..."` asserts an error, not a
  verdict. Add the literal string as a regression fixture.
  **Done:** `internal/ops/extended.go` returns an error on parse failure;
  `internal/ops/validate_failopen_test.go` covers four negative phrasings plus the
  well-formed success and failure paths. `go test ./internal/ops/ -run TestValidate` green.
- [x] **F-002** — Delete the JSON fallback in `SummarizeWithMetadata`
  (`internal/ops/text.go:241-251`) that returns `Confidence: 0.7` and empty `KeyPoints` on a
  parse failure with `err == nil`. Closes **T-02**.
  *Verify:* fenced-JSON input returns an error; assert no `0.7` literal survives in the file.
  **Scope grew:** the same fallback existed in `RewriteWithMetadata`,
  `TranslateWithMetadata`, and `ExpandWithMetadata` — four sites, four `0.7` literals, all
  using `json.Unmarshal` directly rather than the shared fence-stripping `ParseJSON`, so a
  fenced body always took the fallback path.
- [x] **F-003** — `Validate.AutoCorrect` silently drops a correction that fails to unmarshal
  (`extended.go:336-341`, `if err == nil` with no else). Return or log the error. Closes **A-06**.
  *Verify:* test with a `corrected` payload that does not match `T` asserts a non-nil error.
- [x] **F-004** — `Validate.Valid` has two sources of truth: the model's assertion, or a
  recount from issue lists when `FailOn` is set (`extended.go:328`, `343-351`). Always derive
  it from the issues. Closes **A-07**.
  *Verify:* test where the model claims `valid: true` alongside a non-empty `errors` array
  asserts `Valid == false`.

## Control flow

- [x] **F-005** — `Decide` returns `decisions[0]` with `err == nil` on LLM error, unparseable
  response, and out-of-range index (`procedural.go:86-92`, `126-131`), with fabricated
  confidences of `0.5` and `0.3`. Return the error. If a default is wanted, make it explicit
  (`WithFallback(index)`) and set `Fallback bool` on the result. Closes **P-01**.
  *Verify:* injected provider error asserts a non-nil error and no branch taken.
- [x] **F-006** — `Decide`'s first parameter is named `ctx` and is arbitrary prompt data, not
  a `context.Context` (`schemaflux.go:769`) — and the doc comment shows `Decide(ctx, decisions)`,
  inviting callers to pass a real context that gets `%v`-formatted into the prompt. Rename to
  `situation`; add a real `context.Context` first parameter. Closes **P-02**.
  *Verify:* compile-time — the signature's first parameter is `context.Context`.
- [x] **F-007** — `Match` returns nothing and silently treats provider failure as
  "case did not match" (`control_flow.go:95-122`), so an outage routes every input to
  `Otherwise`. Return `(matched bool, err error)`. Closes **P-04**, **P-06**.
  *Verify:* injected error asserts a non-nil error rather than a default-branch execution.
- [x] **F-008** — `Otherwise` fires by position rather than last (`control_flow.go:27-33`);
  placed first, it wins and no other case is evaluated. Collect the default and evaluate it
  only after all others fail. Closes **P-05**.
  *Verify:* test with `Otherwise` as the first argument asserts a later matching case wins.
- [x] **F-009** — `Compose` runs only its first operation and returns
  (`pipeline.go:161-183`). Implement it or delete it. Closes **P-07**.
  *Verify:* `Compose(a, b, c)` observes all three side effects, or the symbol is gone.
- [x] **F-010** — `Pipeline` computes `attempts = MaxRetries` rather than `MaxRetries + 1`
  (`pipeline.go:120-124`), so a caller-supplied `PipelineOptions{RetryFailed: true}` with the
  zero-value `MaxRetries` executes **zero** attempts, skips every step, and reports no error.
  Closes **P-08**.
  *Verify:* test with `PipelineOptions{RetryFailed: true}` asserts each step ran exactly once.
- [x] **F-011** — `Pipeline` retry sleeps with `time.Sleep` (`pipeline.go:141`), ignoring the
  context it checks at the top of every step. Use a cancellable timer. Closes **P-09**.
  *Verify:* cancelling mid-backoff returns promptly with `ctx.Err()`.

## Initialization and configuration

- [x] **F-012** — A missing API key silently selects the mock provider
  (`client.go:51-54`, `186-188`), so a misconfigured deployment returns `Mock response for: ...`
  parsed into zero-valued structs instead of failing. Fall back to `local` only when it was
  explicitly requested; otherwise return an error. Closes **I-01**.
  *Verify:* `Init("")` with a clean environment returns an error naming the missing key.
- [x] **F-013** — `InitWithEnv` always returns `nil`, making the README's
  `if err := InitWithEnv(); err != nil` dead code. Covered by B-03; tracked here because
  F-012 depends on it being able to fail.
- [x] **F-014** — `InitWithEnv` calls `os.Setenv` (`client.go:205`), mutating process-global
  state from a library and leaking into child processes. Remove. Closes **I-13**.
  *Verify:* test asserts the environment is unchanged after `InitWithEnv`.

## Schema generation

- [x] **F-015** — `json:"-"` fields are described to the model and requested back
  (`utils.go:32-37`): when the tag is `-`, the branch is skipped and the Go field name is
  used. Skip the field entirely. Closes **D-02**.
  *Verify:* golden test — a struct with a `json:"-"` field produces a schema omitting it.

## Fabricated numbers

- [x] **F-016** — Delete `CalculateParsingConfidence` (`utils.go:167-178`: `0.3` if the text
  starts with `{`, else `0.1`). Closes half of **D-06**.
- [x] **F-017** — Delete the `Confidence: opt.Threshold - 0.1` literal (`core.go:216`).
  Closes the other half of **D-06**.
- [x] **F-018** — Delete `estimateCompletionConfidence` (`complete.go:281-311`: `+0.2` if the
  completion exceeds 20 characters, `+0.1` if it contains a full stop). Closes **T-05**.
- [x] **F-019** — Delete `BatchResult.TokensSaved`, computed as `(len(chunk)-1) * 100`
  (`batch.go:239`), in a library that receives real token counts from every response.
  Closes **C-09**.
- [ ] **F-020** — Rename every model-self-reported score to make its provenance explicit
  (`ProjectResult.Confidence`, `EnrichResult.Confidence`, `NormalizeChange.Confidence`,
  `VerifyResult.TrustScore` / `OverallConfidence`, `CritiqueResult.OverallScore`,
  `AuditFinding.Severity`, `ClusterResult.Quality`). Closes **D-09**.
  *Verify:* grep asserts no exported field named exactly `Confidence` survives on a result
  struct without a documented measured source.

## Redaction — mark unusable, then fix

- [ ] **F-021** — Mark `Redact`, `RedactWithResult`, and `RedactLLM` not-production-ready in
  their doc comments and in the README until M05's redaction tasks land. Covers **T-07**,
  **T-08**, **T-09**, **T-11**, **T-13**.
- [ ] **F-022** — Delete the "privacy filtering" framing from `Project`'s package
  documentation (`project.go:83-107`), whose first example is `Exclude: []string{"password_hash", "ssn"}`
  against a mechanism that is a prompt hint with no post-filter. Closes the documentation half
  of **D-08**.

## Dead options

- [ ] **F-023** — Implement or delete the dead options. Each has a fluent setter, so the call
  chain reads as if it configured something: `BatchOptions.OnProgress` / `PreProcess` /
  `PostProcess`, `PipelineOptions.SaveProgress`, `ClassifyOptions.CategoryExamples`,
  `TransformOptions.To`, `ClusterOptions.MaxClusterSize`, `RedactOptions.PreserveFormat`,
  `ProjectOptions.PreserveNulls`, `SynthesizeOptions.OutputStructure`,
  `AnnotateOptions.Language`, `CompareOptions.IncludeSimilarity`, `ScoreOptions.IncludeBreakdown`,
  `GenerateOptions.EnsureUnique`, `FilterOptions.BatchSize`, `GuardResult.RetryAfter`.
  Closes **X-04**, **C-10**, **D-10**, **CF-10**.
  *Verify:* **F-024**.
- [ ] **F-024** — Add a static check that fails when an exported options-struct field is
  never read outside its own declaration, setter, and merge function. This is what found the
  list above; make it permanent so the class cannot recur.
  *Verify:* the check fails on a deliberately reintroduced dead field.

## Tools

- [x] **F-025** — Remove `ShellTool` from the default registry (`exec.go:246`). It passes a
  model-authored string to `sh -c` / `cmd /C` with no allowlist, sandbox, or approval, and
  self-registers into the global registry that `GetOpenAITools()` exports. Gate behind
  `tools.EnableShell(policy)` taking a command allowlist. Closes **G-04**.
  *Verify:* default registry contains no `shell`; enabling it requires an explicit policy
  argument; a command outside the allowlist is refused before execution.
- [x] **F-026** — The `token` (JWT) tool returns a `StubResult` without setting `IsStub: true`
  (`cache_security.go:386-405`), so the one filter consumers have still ships a fake JWT
  generator. Set the flag, then add a test asserting every `StubResult` call site belongs to a
  tool with `IsStub: true`. Closes **G-03**.
  *Verify:* the new test fails when the flag is removed from any stub.
- [x] **F-027** — `Register` errors are discarded at init (`_ = Register(...)` throughout
  `internal/tools`), so a duplicate name silently keeps whichever tool won the race.
  Panic at init or collect into a package-level error. Closes **G-06**.

## M01 exit gate

- [x] **F-028** — No operation in the package returns a successful result on a provider
  error, a parse failure, or a validation failure.
  *Verify:* a fault-injection suite that, for every exported operation, runs three scenarios —
  provider error, malformed body, schema-violating body — and asserts each returns a non-nil
  error. This suite is the regression net for all of M01 and must stay green forever after.

---

# M02 — OpenAI provider: complete the Responses API integration

The provider already targets `POST /v1/responses` (`provider.go:100-237`). What is missing is
everything around it. Every item here is code-verifiable; only P-012 and P-013 need credits.

- [x] **P-001** — The response parser concatenates `item.Text` from **every** output item
  regardless of `output[].type` (`provider.go:212-218`). The Responses API returns `reasoning`
  items alongside `message` items, so on a reasoning model the reasoning summary is glued onto
  the JSON payload and corrupts the parse. Filter to `type == "message"` and, within it, to
  `content[].type == "output_text"`.
  *Verify:* golden test decoding a recorded body containing both a `reasoning` item and a
  `message` item asserts only the message text is returned. **This test must exist before the
  fix**, and must fail against the current parser.
- [x] **P-002** — Parse `usage.output_tokens_details.reasoning_tokens` into
  `TokenUsage.ReasoningTokens`. Reasoning tokens are billed and currently invisible, which
  makes every cost figure on a reasoning model wrong.
  *Verify:* golden test asserts the field is populated from a recorded body.
- [x] **P-003** — Parse `usage.input_tokens_details.cached_tokens` into
  `TokenUsage.CachedTokens`. The field, `CostInfo.CachedCost`, and `PriceCachedToken` all
  exist and are reported in `CostSummary`, but nothing ever populates them. Closes the
  measurement half of **Gap-04**.
  *Verify:* golden test asserts a non-zero cached count survives into `CostSummary`.
- [x] **P-004** — `FinishReason` is hardcoded to `"stop"` (`provider.go:230`). Map it from
  `status` and `incomplete_details.reason` so truncation is distinguishable from completion.
  *Verify:* golden test on an `incomplete` body asserts a truncation reason, not `"stop"`.
- [ ] **P-005** — Send `text.format = {type: "json_schema", strict: true, schema: ...}` using
  the schema from `GenerateTypeSchema` rather than the current `json_object`
  (`provider.go:130-135`). Record which enforcement path was used in the result metadata.
  Closes **I-05** and the structured-output half of **Gap-02**. Depends on **S-001**.
  *Verify:* a schema-violating instruction still yields a conforming object; a provider
  without strict support falls back to prompt-only and says so in `Meta`.
- [x] **P-006** — A new `http.Client` is constructed per request (`provider.go:165`),
  defeating connection reuse and HTTP/2 multiplexing. Build it once per provider; keep the
  per-request deadline on the context.
  *Verify:* benchmark showing connection reuse across sequential calls; assert one client
  instance per provider.
- [ ] **P-007** — The non-200 path embeds the raw response body into a `fmt.Errorf`
  (`provider.go:175-178`). Return a typed `*APIError{StatusCode, Provider, Model, Body}` and
  redact the body by default. Feeds **Gap-12** and **X-03**.
  *Verify:* `errors.As` recovers the status code; the default `Error()` string contains no
  request payload.
- [ ] **P-008** — Set `store: false` by default. The Responses API retains responses
  server-side unless told otherwise, which is a surprising default for a library that
  processes arbitrary user records. Expose `option.Store(true)` for callers who want it.
  *Verify:* request body asserts `"store": false` unless explicitly overridden.
- [ ] **P-009** — Send `prompt_cache_key` derived from `(op, T, tier)` so repeat requests
  route to a server holding the prefix. Depends on **CA-002**.
  *Verify:* recorded request contains a stable key across two identical calls and a different
  key across different ops.
- [ ] **P-010** — Update the tier mapping to the 5.6 family confirmed live:
  `gpt-5.6-luna`, `gpt-5.6-sol`, `gpt-5.6-terra` (`internal/config/config.go:194-203`).
  Assign Smart/Fast/Quick by measured capability and price, not by guessing from the names —
  **P-013** produces that evidence. Until then, leave the mapping behind a constant with a
  TODO rather than picking arbitrarily.
  *Verify:* `GetModel` returns 5.6 IDs for the OpenAI provider; pricing table has an entry for
  each (**PR-002**).
- [ ] **P-011** — Revisit `supportsTemperature()` and `supportsReasoningControls()`
  (`provider.go:122`, `136-141`) for the 5.6 family. Both currently pattern-match on model
  name prefixes and will silently misclassify the new IDs.
  *Verify:* unit test per model ID asserts the expected capability flags; **P-013** confirms
  them against the live API.
- [ ] **P-012** `[LIVE]` — Live smoke: one `/v1/responses` call per 5.6 model, asserting
  HTTP 200, non-empty message text, and populated usage including reasoning tokens.
  Blocked on **B-01**.
- [ ] **P-013** `[LIVE]` — Capability matrix across `luna` / `sol` / `terra`: strict
  `json_schema` support, temperature acceptance, reasoning-effort acceptance, cached-token
  reporting, and observed latency and cost per model. This is the evidence **P-010** and
  **P-011** need. Record the results in the task, not from memory. Blocked on **B-01**.
- [ ] **P-014** — Record the responses from P-012 and P-013 as cassettes (**TI-003**) so the
  golden tests above run in CI without credits, forever.

## Other providers

- [ ] **P-015** — `GetModel` falls through to OpenAI model IDs for `deepseek`, `qwen`, `zai`,
  and any custom provider (`config.go:194-203`), so the README's `WithProvider("deepseek")`
  sends `model: "gpt-5.4"` to `api.deepseek.com` and 400s. The Anthropic provider defends
  itself (`provider.go:366`); the OpenAI-compatible path does not. Add a per-provider default
  map and validate at construction. Closes **I-03**.
  *Verify:* table test per provider asserts a plausible model ID; construction with an unknown
  provider/model pair returns an error.
- [ ] **P-016** — The Anthropic provider hardcodes `max_tokens: 1024` before conditionally
  overriding it (`provider.go:383`, `394-396`) and never sends `cache_control`. Wire the real
  ceiling and add prompt-cache breakpoints. Depends on **CA-003**.
  *Verify:* recorded request asserts the configured ceiling and a breakpoint on the last
  stable block.

---

# M03 — Test infrastructure

Built before the redesign, because every later milestone is verified with it. This is also
**Gap-06**, the single largest adoption blocker in the review: today there is no supported way
to test code that uses this library without paying a provider.

- [ ] **TI-001** — Ship `schemafluxtest` with an exported fake provider: scripted responses
  per operation, a canned error mode, a slow mode for timeout tests, and a recorder of the
  exact requests it received. Closes **Gap-06**.
  *Verify:* the triage example from the review doc runs green with no network and no key.
- [ ] **TI-002** — Add a `Provider` seam consumers can inject per call rather than only via a
  package global. Depends on **A-004**.
  *Verify:* two clients with different fake providers run concurrently without interference —
  the test that today's global would fail.
- [ ] **TI-003** — Cassette record/replay: capture live provider bodies once, replay them in
  CI. Redact keys on write.
  *Verify:* a recorded suite replays with `SCHEMAFLUX_LIVE_TESTS` unset; a cassette containing
  a key fails the redaction check.
- [ ] **TI-004** — Golden-prompt tests: for each operation, snapshot the exact rendered system
  and user prompt for a fixed input. Prompt changes then become reviewable diffs instead of
  silent behavior changes for every downstream user. Closes the testing half of **Gap-13**.
  *Verify:* editing any prompt literal fails a golden test until the snapshot is updated.
- [ ] **TI-005** — Determinism test: build the same prompt twice from identical options and
  assert byte equality. This currently **fails** — Go map iteration order randomizes prompt
  bytes (see **CA-001**). Write it now so the fix has a witness.
  *Verify:* the test fails today and passes after CA-001.
- [ ] **TI-006** — Fault-injection harness backing **F-028**: provider error, malformed body,
  schema-violating body, truncated body, empty body — parameterized across every exported
  operation.
- [ ] **TI-007** — Property tests for the collection invariants: for random inputs,
  `Filter` output is a subset of input, `Sort` output is a permutation of input, `Choose`
  output is a member of input, `Cluster` output partitions input exactly once.
  *Verify:* each property fails against today's implementation and passes after **OP-101**–
  **OP-105**.
- [ ] **TI-008** — Concurrency tests under `-race` for the package globals: `defaultClient`
  (`client.go:192-236`), `ops.defaultProvider`, `ops.customLLMCaller`, and `pricing`'s
  package state. Closes **I-14**.
  *Verify:* `go test -race` is green; the review notes `-race` cannot run on the current
  Windows/arm64 machine, so this requires **CI-002**.
- [ ] **TI-009** — UTF-8 corpus fixtures (CJK, emoji, combining marks, RTL) used by every
  truncation, slicing, and redaction test. Closes the test half of **T-06**.
- [ ] **TI-010** — Cost-accounting tests: assert an unpriced model reports "unpriced" rather
  than `$0.00`, and that a priced model's arithmetic matches a hand-computed figure.

---

# M04 — The core: `Op`, `Run`, `Invariant`, options, result, errors

Primitives 1–5 and 7–8 from the review's target shape. Build alongside the existing code;
port nothing yet except one operation as the proof.

- [ ] **A-001** — `Op[In, Out]` descriptor: `Name`, `Prompt`, `Format`, `Schema`, `Decode`,
  `Invariants`. Closes the structural half of **D-15**.
- [ ] **A-002** — `Run[In, Out](ctx, client, op, in, opts...)` owning context propagation,
  response-format selection, provider dispatch, retry, decoding, invariant checking, repair,
  usage accounting, and telemetry. Closes **X-01** at one call site instead of thirty-one.
  *Verify:* cancelling the context aborts the call; assert with the fake provider's slow mode.
- [ ] **A-003** — Single JSON extraction path, hardened once: fenced blocks anywhere, leading
  and trailing prose, and a brace-matching scan. Replaces the 16 hand-rolled fence strippers
  and the parallel `cleanJSON`. Closes **X-02**.
  *Verify:* corpus test of malformed-but-recoverable bodies; each of the 16 old sites is
  deleted, not left behind.
- [ ] **A-004** — `RequestOption` applied at both client construction and per call, with
  per-call precedence. Closes **I-07**, **I-08**, **I-09**, **Gap-14**, and the
  order-dependence trap in `Client.With*`.
  *Verify:* `option.Model(...)` on one call does not leak into the next; setting timeout after
  provider construction takes effect.
- [ ] **A-005** — Renumber `Mode` and `Speed` so zero means unset (`ModeUnset`, `TierUnset`),
  and add `Opt[T]` for numerics that must be settable to zero. Today `Strict == Mode(0)` and
  `Smart == Speed(0)`, so every `mergeXOptions` guard of the form `if user.Mode != 0` makes
  `.Strict()` and `.Smart()` unrepresentable on roughly ten operations. Closes **F-01**.
  *Verify:* test asserts `Negotiating[T](c).Strict().Smart()` produces Strict and Smart, not
  the operation defaults. Breaking change — record in the release notes.
- [ ] **A-006** — `Result[T]` + `Meta`: request and correlation IDs, provider, model, usage,
  cost with `Estimated` and `PricingSource`, attempts, repairs, strategy, elapsed. No
  `Confidence` field. Closes **D-09** structurally and makes **I-02** expressible.
- [ ] **A-007** — Error taxonomy: `ErrNoProvider`, `ErrAuth`, `ErrRateLimited`,
  `ErrTruncated`, `ErrDecode`, `ErrInvariant`, `ErrBudgetExceeded`, plus `APIError` and
  `InvariantError`. Closes **Gap-12**.
  *Verify:* every provider maps its failures onto the taxonomy; `errors.Is` table test.
- [ ] **A-008** — Reclassify retries on typed errors and status codes instead of substring
  matching on message text (`llm_helper.go:205-263`), and add jitter to the backoff
  (`retryDelay:265`). Closes **I-12**. Depends on **A-007**.
  *Verify:* a 429 retries, a 400 does not, an unknown error does not silently fail fast;
  concurrent retries do not align.
- [ ] **A-009** — `Invariant[In, Out]` plus the shared library: `MemberOf`, `SubsetOf`,
  `SameMultiset`, `CoversExactlyOnce`, `AtMost`, `WithinLength`, `AtLeastConfidence`,
  `ExcludesValues`, `CategoryIn`, `OneOf`, `Satisfies`. Closes **X-05** mechanically.
  *Verify:* each invariant has a unit test for pass, fail, and the error message the repair
  loop will feed back.
- [ ] **A-010** — Repair loop: a decode or invariant failure feeds the error back and retries
  within the existing budget, aggregating usage across attempts into `Meta`. Closes **CF-01**.
  Depends on **A-009**.
  *Verify:* fake provider returns a violating result then a valid one; assert one repair, two
  attempts, and summed usage.
- [ ] **A-011** — Move errors and log records off raw payloads: no request body in an error
  string by default, no `Input` field on error types. Closes **X-03**.
  *Verify:* an error from a payload containing a marker string does not contain that marker.
- [ ] **A-012** — Port `Extract` to the core as the proof, keeping `Extracting[T]` working
  unchanged.
  *Verify:* existing extract tests pass untouched against the new path.
- [ ] **A-013** — `Run(ctx)` on the fluent builders. There is no way to honor cancellation
  otherwise, and the builder is where the context naturally arrives. Breaking change.
  *Verify:* `Extracting[T](x).Run(ctx)` cancels.

---

# M05 — Port the operations, attaching invariants

Family by family, highest defect density first. Each family's port is one commit with its
tests.

## Collections — the highest-value family

- [ ] **OP-101** — Switch `Choose`, `Filter`, and `Sort` to index-based responses. The
  codebase already does this in `Match`, `Arbitrate`, `Cluster`, `Interpolate`, `Compose`,
  and `Batch`; `collection.go` documents the switch away from it as deliberate (lines 77, 196,
  318). Reverting fixes identity **and** removes most of the size ceiling, because output
  length stops scaling with item size. Closes **C-08**.
- [ ] **OP-102** — `Choose`: attach `MemberOf`. Today the returned object is whatever the
  model emitted, never compared to the input list (`collection.go:112-131`). Closes **C-01**.
- [ ] **OP-103** — `Filter`: attach `SubsetOf`, and fix the contradiction where the system
  prompt says "Include items that match" (line 201) while `KeepMatching: false` steers "Remove
  items that match" (line 168). Closes **C-02**.
- [ ] **OP-104** — `Filter`: delete the single-object fallback that collapses a failed array
  parse into a one-element result with `err == nil` (`collection.go:236-245`). Closes **C-03**.
- [ ] **OP-105** — `Sort`: upgrade the count check to `SameMultiset` (`collection.go:371-380`).
  Equal length does not mean equal contents. Closes **C-04**.
- [ ] **OP-106** — Promote `sortByScoringFallback` (`collection.go:385-453`) from fallback to
  the primary strategy above a size threshold: it scores items independently, keeps the
  caller's own objects, and sorts in Go, so items cannot be lost, duplicated, or edited — and
  `Stable` actually works. Run it with bounded concurrency and report `Meta.Strategy`.
  Closes **C-05**.
  *Verify:* a 200-item sort makes bounded concurrent calls, returns a permutation, and reports
  its strategy.
- [ ] **OP-107** — `Cluster`: attach `CoversExactlyOnce`; fix `Size` being computed from raw
  indices including out-of-range ones while `Items` holds only valid ones (`cluster.go:310-327`),
  so the two disagree exactly when the model misbehaved. Closes **C-07**.
- [ ] **OP-108** — Token-estimate before dispatch and refuse or chunk oversized collections.
  Today every collection op marshals the whole slice into one prompt with no size guard, while
  output is capped at 1000–4000 tokens by tier, so `Sorting` a few hundred objects cannot
  physically return a complete result. Closes **C-06**, **Gap-10**. Depends on **A-007**
  (`ErrTruncated`) and **CF-004**.

## Analysis and validation

- [ ] **OP-201** — Enforce `MinConfidence` in `Classify`, `Filter`, `Verify`, and `Derive`.
  It is enforced in exactly one operation today (`annotate.go:287`) and is prompt-only in the
  other four, with non-zero defaults (0.5, 0.7, 0.7) that make users believe a threshold is
  active. In `Classify` the value sits in a local variable one line above where it is copied
  to the result. Closes **A-02**.
- [ ] **OP-202** — Generalize `Classify`'s category-membership check (`analysis.go:178-196`)
  into the shared `CategoryIn` invariant. It is the only operation that validates the model's
  answer against the allowed set; it should be the template, not the exception.
- [ ] **OP-203** — `Classify`: either give `ClassifyResult[C]` a real multi-label field or
  delete `MultiLabel` and `MaxCategories`. They change the prompt and cannot change the
  result. Closes **A-03**.
- [ ] **OP-204** — `Classify[T, C]`: the category is produced as a string and converted via a
  JSON round trip, so only string-kinded `C` survives and `Classify[Ticket, Priority]` with
  `type Priority int` compiles and fails at runtime. Constrain `C` or map explicitly.
  Closes **A-04**.
- [ ] **OP-205** — `Validate`: add a deterministic rule path ahead of the model call. Every
  rule in the README's own example ("email must be valid, country must be ISO alpha-2, age at
  least 18") is checkable in Go, and `Parse` already demonstrates the deterministic-first
  pattern in this package. Closes **A-05**.
- [ ] **OP-206** — Collapse `Validate` / `Verify` / `Audit` / `Critique` / `Score` onto one
  result shape. Five vocabularies for one operation shape (verdict + issues + summary) with
  different field names is the verb-explosion problem in its clearest form. Closes **A-08**.
  Coordinate with **PS-002**.

## Structured data

- [ ] **OP-301** — `Project`: filter excluded fields out of the marshalled input
  deterministically and post-scan the output for excluded values, rather than interpolating
  `Exclude` into the prompt as a hint. Closes the mechanism half of **D-08**.
  *Verify:* an SSN present in the input never appears in the output, including when the model
  is instructed to echo it into an unrelated field.
- [ ] **OP-302** — Compute `Lost`, `Inferred`, and `Mappings` in Go by diffing the source
  field set against the produced output, instead of accepting the model's self-report as an
  audit trail (`project.go:258-283`, and the same shape in `pivot.go`, `enrich.go`,
  `normalize.go`). Closes **D-09** behaviorally.
- [ ] **OP-303** — `NormalizeInput`: prefer JSON marshalling over `fmt.Stringer`
  (`utils.go:99-113`). Any type with a `String()` method — including `time.Time` — is sent as
  prose while the generated schema simultaneously tells the model the format is RFC3339.
  Closes **D-04**.
- [ ] **OP-304** — `NormalizeBatch`: replace the serial per-item loop (`normalize.go:347-364`)
  with the batch processor plus bounded concurrency. Closes **D-14**, **Gap-08**.
- [ ] **OP-305** — `Parse`: match CSV headers on the `json` tag before the Go field name, and
  return an error (or a populated `Unmapped`) when no column maps. Today
  `capitalizeFirst(header)` is compared to Go field names, unmapped columns are skipped
  silently, and a single-struct target with zero matching headers returns a zero value with
  `err == nil`. Closes **D-11**.
- [ ] **OP-306** — `Parse`: guard `reflect.TypeOf(result)` against a nil type so
  `Parse[any]` cannot panic (`parse.go:301`, `344`). Closes **D-12**.
- [ ] **OP-307** — `Parse`: strengthen `detectFormat` (`parse.go:188-238`), where any input
  containing `": "` and no `{` is classified YAML and any input containing `|` is
  pipe-delimited, so ordinary prose is routed to the wrong parser and fails. Closes **D-13**.

## Text

- [ ] **OP-401** — Collapse the `X` / `XWithMetadata` twins into one operation returning
  `Result[T]` (`text.go:95/166`, `269/353`, `467/547`, `659/733`), each pair duplicating ~40
  identical lines. Closes **T-01**.
- [ ] **OP-402** — Attach `WithinLength` so `MaxLength` and `TargetLength` are checked rather
  than requested, and compute `CompressionRatio` on runes rather than bytes. Closes **T-03**.
- [ ] **OP-403** — Replace byte slicing with rune-safe truncation in `complete.go:269-276`
  and `redact_llm.go:305-336`. Closes **T-06**. Depends on **TI-009**.
- [ ] **OP-404** — `Complete` and `CompleteField`: drop the `provider llm.Provider` parameter
  that no other operation takes and that leaks `internal/llm` into the signature. Closes
  **T-04**.

## Redaction — rebuild

- [ ] **OP-501** — Replace the field-name substring matcher (`redact.go:359-387`), whose
  sensitive list includes `"name"`, `"key"`, `"first"`, `"last"`, `"card"`, `"address"`, and
  `"full"`, so `Filename`, `Username`, `Keywords`, `LastUpdated`, and `CardCount` are all
  destroyed. Match on word boundaries and an explicit tag. Closes **T-07**.
- [ ] **OP-502** — Replace the pattern set (`redact.go:406-438`). It misses undashed SSNs,
  every phone format except `###-###-####`, and unformatted 16-digit card numbers, while
  masking any two capitalized words, any nine-digit number, and every currency amount.
  Closes **T-08**.
  *Verify:* a labelled corpus of true positives and true negatives with per-category
  precision and recall floors asserted in CI.
- [ ] **OP-503** — Implement `RedactWithResult`, which today returns an empty map and
  `err == nil` under a `// For now` comment (`redact.go:186-200`), so an audit reads as
  "nothing was redacted". Closes **T-09**.
- [ ] **OP-504** — Replace `Redact[T](input T, opts ...interface{})` with typed options; the
  `default:` branch currently discards an unrecognized options struct silently. Closes **T-10**.
- [ ] **OP-505** — Remove or replace jumble redaction. `JumbleSeed` defaults to zero, so the
  RNG is seeded with `len(input)` — a value readable from the output — and `jumbleBasic` is a
  Fisher–Yates shuffle of the same runes, making the transform an **invertible** permutation.
  Closes **T-11**.
  *Verify:* a test asserting the output is not a permutation of the input.
- [ ] **OP-506** — `JumbleSmart` is documented as preserving vowel/consonant structure and
  calls `jumbleBasic` (`redact.go:535-539`). Implement or delete. Closes **T-12**.
- [ ] **OP-507** — `RedactLLM`: locate spans by matching the model's reported substring with
  `strings.Index` rather than trusting model-reported character offsets, which are
  bounds-checked only (`redact_llm.go:232-269`) and compared against `len()` in bytes while
  models count characters. Reject a span whose sliced text does not match the reported
  original. Closes **T-13**.
- [ ] **OP-508** — Lift the redaction not-production-ready markers from **F-021** once
  OP-501–OP-507 are green.

---

# M06 — Make the types load-bearing

- [ ] **S-001** — Recurse `GenerateTypeSchema` into nested structs and slices, with a depth
  cap and cycle detection. Today the struct branch describes each field with
  `GetTypeDescription`, which returns `main.Person` for a struct field and `[]main.OrderItem`
  for a slice field — so the model is told a Go identifier it has never seen for exactly the
  nested payloads extraction is used on. `Project`, `Pivot`, `Enrich`, and `Derive` inherit
  this. Closes **D-01**.
  *Verify:* golden schema test on a three-level nested struct with a slice of structs.
- [ ] **S-002** — Emit real JSON Schema from the same reflection pass, and keep a compact
  rendering for the prompt path (full JSON Schema is token-expensive).
  *Verify:* the emitted schema validates against a JSON Schema meta-schema.
- [ ] **S-003** — Express requiredness explicitly rather than inferring it from the absence of
  `omitempty` (`utils.go:42-47`), which is a serialization directive, not a validation one.
  Closes **D-03**.
- [ ] **S-004** — Make `Strict()` mean something: with strict schema enforcement plus declared
  requiredness, replace `ValidateExtractedData` — which takes a `threshold` and never reads
  it, checking only non-nil (`utils.go:180-198`). Closes **D-05**.
  *Verify:* a response missing a required field fails in Strict and repairs in Transform.
- [ ] **S-005** — Replace `inferResponseFormat` (`llm_helper.go:299-317`), which decides JSON
  mode by searching the concatenated system **and user** prompt for phrases like
  `"json object"` — making response format data-dependent and rewording a prompt enough to
  silently disable enforcement. Use the `Format` field on the descriptor. Closes **I-04**.
  *Verify:* a user input containing the phrase "json object" does not change the request's
  response format.
- [ ] **S-006** — Reconsider `Creative` mode on `Extract`, which instructs "Generate plausible
  values for missing fields / Prioritize completeness over strict accuracy"
  (`utils.go:141-146`) on an operation whose purpose is faithfulness. Rename it for what it
  does or remove it from `Extracting`. Closes **D-07**.
- [ ] **S-007** — Make `strengthenSystemPrompt` opt-out and measure whether it still helps.
  It prepends a fixed instruction block to every request, JSON or not, billed on every call.
  Closes **I-18**.
  *Verify:* an A/B over the golden corpus recording the measured difference; if none, delete.

---

# M07 — Make the operational claims true

## Pricing and budgets

- [x] **PR-001** — Never substitute another model's price. `getDefaultPricing` returns
  claude-3-haiku pricing for **any** Anthropic model, understating an Opus call by roughly
  60x while presenting it as a precise USD figure; six of eight providers have no entry at all
  and report `$0.00`. Add `Estimated` / `PricingSource` to `CostInfo`. Closes **I-02**.
  *Verify:* an unpriced model reports unpriced, never zero; a substituted price is impossible
  by construction.
- [ ] **PR-002** — Add `RegisterPricingModel` and populate the 5.6 family. The price table is
  a private package var with hardcoded 2024 effective dates and no override path.
- [ ] **PR-003** — Bound `costHistory` (a package-level slice appended per call and never
  evicted, `pricing.go:301`) with a ring buffer and a request-ID index; `GetRequestCost`,
  `GetCostSummary`, and `GetTotalCost` all linear-scan it under a lock. Closes **I-10**.
  *Verify:* memory is flat across 100k tracked calls; lookup is O(1).
- [ ] **PR-004** — Separate history reset from budget configuration: `ResetCostTracking`
  currently nulls `budgetLimits` and `budgetCallback` too, so clearing history silently
  disables budget alerting. Closes the side-effect half of **I-10**.
- [ ] **PR-005** — Make budgets edge-triggered and optionally enforcing. `SetBudget` fires its
  callback on every request once spend passes 80% of a limit, with no debounce and no state,
  and nothing is ever blocked. Closes **I-11**.
  *Verify:* one notification per threshold crossing; enforcing mode returns
  `ErrBudgetExceeded` before the call is made.
- [ ] **PR-006** — Delete the duplicated `MatchesFilters` / `matchesFilters` pair
  (`pricing.go:451`, `499`), one of which is exported by accident. Closes **I-17**.

## Middleware

- [ ] **MW-001** — `Handler` / `Middleware` chain applied at client construction. Closes
  **Gap-11**.
- [ ] **MW-002** — `mw.RateLimit`. Closes part of **Gap-09**.
- [ ] **MW-003** — `mw.Retry` wrapping **A-008**.
- [ ] **MW-004** — `mw.Cache`: response cache keyed on a hash of model, tier, mode, prompt
  bytes, and schema, so exact-duplicate calls cost zero. Closes **Gap-07**.
- [ ] **MW-005** — `mw.Budget` enforcing **PR-005**. Closes **CF-08**.
- [ ] **MW-006** — `mw.RedactEgress` so payloads can be scrubbed before they leave.
- [ ] **MW-007** — `mw.Metrics` exporting to OpenTelemetry, already a direct dependency.
  Closes the export half of the metrics gap.
- [ ] **MW-008** — `mw.Fallback` for provider failover. Closes the rest of **Gap-09**.

## Prompt caching

- [x] **CA-001** — Sort map keys before rendering them into prompts. Go randomizes map
  iteration order, so `SchemaHints` (`core.go:73`), `FieldRules` (`core.go:80`,
  `extended.go:232`), `Mappings` (`project.go:171`), and `CategoryDescriptions`
  (`analysis.go:95`) produce different prompt bytes on every call — defeating any prefix cache
  and making runs irreproducible.
  *Verify:* **TI-005** passes.
- [ ] **CA-002** — Split `Prompt` into `Stable` and `Volatile` segments; move steering and all
  option-derived clauses out of the system prompt and into the user message. `applySteering`
  currently appends per-call text to the system block (`llm_helper.go:291`), invalidating the
  prefix on every call.
  *Verify:* two calls differing only in steering share a byte-identical stable prefix.
- [ ] **CA-003** — Provider-specific cache wiring: `cache_control` breakpoints on the last
  stable block for Anthropic (max 4), byte-identical prefixes plus `prompt_cache_key` for
  OpenAI. Depends on **P-009**, **P-016**.
- [ ] **CA-004** — Consolidate per-operation invariant content (schema, exemplars, rules) into
  the stable zone so it crosses the minimum cacheable prefix. Below the floor — 1024 tokens on
  OpenAI, 512–4096 on Anthropic depending on model — caching silently does nothing, and
  today's system prompts are a few hundred tokens.
  *Verify:* measured `cached_tokens` greater than zero on the second identical call.
  `[LIVE]`, blocked on **B-01**.
- [ ] **CA-005** — Fan-out ordering primitive: send one request, await first token, then
  release the rest. A cache entry is only readable after the first response begins streaming,
  so a naive parallel fan-out has every worker pay a full write.
- [ ] **CA-006** — Surface `Meta.CacheHitRatio` and emit a diagnostic when repeated identical
  prefixes report zero cache reads.

## Streaming and long output

- [ ] **ST-001** — Add streaming to the `Provider` interface and implement it for the
  Responses API. Closes **Gap-01**.
- [ ] **ST-002** — Expose streaming on text operations, with the non-streaming path
  implemented in terms of it.
- [ ] **ST-003** — Remove the hardcoded output ceilings (4000/2000/1000 by tier,
  `config.go:206-218`) in favor of a per-call option, and make truncation return
  `ErrTruncated` rather than surfacing as a parse error. Closes **I-09**.

## Infrastructure papercuts

- [ ] **IN-001** — Guard `defaultClient` behind its mutex in `GetDefaultClient`, `GetLogger`,
  `ConfigureLogging`, and `SetLogLevel`, and protect `ops.defaultProvider` and
  `ops.customLLMCaller`. Closes **I-14**. Verified by **TI-008**.
- [ ] **IN-002** — Delete the unused `Client.openaiClient` field. Closes **I-15**.
- [ ] **IN-003** — Make `WithDebug(false)` restore the prior log level. Closes **I-16**.
- [ ] **IN-004** — Decide `Client`'s fate: it has no method that runs an operation, and
  `WithProviderConfig` / `WithProviderInstance` mutate a package global as a side effect, so
  constructing a second client silently reconfigures the first. Either give it real operation
  methods or delete it and document the global model honestly. Closes **I-06**.

---

# M08 — Control flow as combinators

Each takes an `Op` and returns an `Op`. Build after M05, because `Vote` needs comparable
results, `Escalate` needs a failure signal that is not "the model said something odd," and
`MapReduce` needs invariants to validate the merge.

- [ ] **CF-001** — `flux.Escalate(op, from, to)`. Closes **CF-02**.
- [ ] **CF-002** — `flux.Vote(op, n, rule)` — and the first honest confidence number in the
  library, derived from sample agreement. Closes **CF-03**.
- [ ] **CF-003** — `flux.Until(op, pred, max)`. Closes **CF-05**.
- [ ] **CF-004** — `flux.MapReduce(op, chunk, merge)` with bounded concurrency. Closes
  **CF-04**; unblocks **OP-108**.
- [ ] **CF-005** — `flux.Checkpoint(store, runID)`. Closes **CF-06**, and replaces the
  declared-but-unimplemented `PipelineOptions.SaveProgress`.
- [ ] **CF-006** — `flux.Approve(gate)`. Closes **CF-07**; required before **F-025**'s shell
  tool may be enabled.
- [ ] **CF-007** — `flux.Fallback(a, b)`.
- [ ] **CF-008** — Retire or reimplement `Decide`, `Guard`, `Match`, and `Pipeline` on the
  combinators. `Guard` currently issues an unannounced LLM call with a hardcoded 2-second
  timeout and no options (`procedural.go:143-180`). Closes **P-03**, **P-10**.
- [ ] **CF-009** — Bounded-concurrency primitive used by OP-106, OP-304, and CF-004. Today
  only `batch.go:136` and `pipeline.go:232` start goroutines. Closes **Gap-08**.

---

# M09 — Product shape

Decisions, not defects. Each halves or doubles the maintenance surface, so make them before
1.0 — every finding in the review has to be fixed once per operation that survives.

- [ ] **PS-001** — Decide the fate of the tools registry: 86 tools, 41 of them stubs, none
  reachable by an external consumer. Promote (requires **PS-004**) or delete. Closes **G-01**,
  **G-02**, **G-05**.
- [ ] **PS-002** — Decide the verb catalogue. With invariants and combinators in place, a core
  of roughly twelve operations plus `Steer` expresses `Critique`, `Audit`, `Verify`,
  `Arbitrate`, `Negotiate`, `Resolve`, `Conform`, `Derive`, and `Pivot`. Ship those as a
  `recipes` package built on the core. Closes **A-08**, and reduces the cost of every other
  task in this file.
- [ ] **PS-003** — Pick one public spelling. Mark the other `// Deprecated:` so tooling says
  so — there is not a single deprecation marker in the repo today. Expose `Deduplicate`
  (implemented, exported nowhere) or delete it; document or delete `Format` and `Merge`.
  Closes **F-06**, **A-09**, **A-10**.
- [ ] **PS-004** — Add tool calling to the provider path: `Tools`, `ToolChoice`, and tool-call
  response handling, none of which exist in `CompletionRequest`. Closes **Gap-02**; makes
  PS-001 worth doing.
- [ ] **PS-005** — Multi-turn support: `CompletionRequest` carries one system string and one
  user string, with no message history. `Asking`, `Negotiating`, and
  `NegotiatingAdversarially` are naturally multi-turn operations implemented as one round
  trip. Closes **Gap-03**.
- [ ] **PS-006** — Embeddings. Their absence forces LLM round trips for `Similar`,
  `CheckingSimilarity`, `Clustering`, `Deduplicate`, and `Matching`, all of which have cheap
  deterministic vector implementations. Closes **Gap-05**.
- [ ] **PS-007** — Prompts as versioned, overridable artifacts with golden tests, so a prompt
  edit is a reviewable change rather than a silent behavior change for every downstream user.
  Closes **Gap-13**. Depends on **TI-004**.
- [ ] **PS-008** — Resolve `docs/engineering/plans/workflowengineplan.md` and
  `SCHEMAFLUXDSLSPEC.md`, which describe a durable workflow engine this repo does not
  implement and which M08 is a deliberate decision not to build. Scope them into the roadmap
  or move them out of `plans/`.
- [ ] **PS-009** — Reconcile `AGENTS.md` with this repository. It is CodeFlux's file, copied
  verbatim: it declares itself scoped to Codeflux, mandates `docs/plan.md`, `CHANGELOG`,
  `DEVLOG`, `.claude/`, `.artifacts/`, `cmd/codeflux-dev`, atoms, SQLite migrations, a
  frontend, and a `dev`-branch model that does not exist here — while forbidding the
  `git add -A` and direct `main` pushes already used in this repository's history.

---

# M10 — Release gates

- [ ] **CI-001** — Run `go build`, `go vet`, `gofmt -l`, and the full suite on every push.
- [ ] **CI-002** — Add a Linux AMD64 job for `go test -race`, which cannot run on the current
  Windows/arm64 machine. Unblocks **TI-008**.
- [ ] **CI-003** — Normalize line endings (`.gitattributes`) so `gofmt -l` stops reporting
  ~180 files that differ only by CRLF, which currently masks real formatting drift.
- [ ] **CI-004** — Make the numbered examples a release gate. 19 of 45 fail under the local
  provider today because the mock returns `Mock response for: ...`, incompatible with the JSON
  contracts of `Rank`, `Enrich`, `Predict`, `Verify`, and `Question`. Depends on **TI-001**.
- [ ] **CI-005** — Coverage floor, ratcheted from the current measured value rather than set
  aspirationally.
- [ ] **CI-006** — Secret scanning on push; assert no cassette or fixture carries a key.
- [ ] **CI-007** — Public API surface test: snapshot the exported symbols so an unintended
  addition or removal fails review. Depends on **PS-003**.
- [ ] **DOC-001** — Rewrite the README against what the code does. Today it advertises
  timeout control through context (dropped by 31 operations), cost tracking (zero or wrong for
  six of eight providers), retries for transient failures (classified by substring), and
  privacy filtering (a prompt hint).
- [ ] **DOC-002** — Migration guide for the breaking changes: `Run(ctx)`, the `Mode`/`Speed`
  renumbering, `Confidence` removal, per-operation result structs collapsing into
  `Result[T]`, `Decide`'s signature, `Redact`'s options, `Complete` losing its provider
  parameter, and the `SCHEMAFLOW_*` to `SCHEMAFLUX_*` environment rename already shipped.
- [ ] **DOC-003** — Update `docs/engineering/backlog/PRODUCTION_TODO.md` to point here, or
  fold it in and delete it.
- [ ] **REL-001** — Tag v0.2.0 at the end of M02 (provider correctness), v0.3.0 at the end of
  M05 (operations verified), v1.0.0 only after M10.

---

## Dependency summary

```
B-01 ──> P-012, P-013, CA-004        (credits gate every live task)
M01  ──> everything                  (independent; ship first)
TI-001 ─> CI-004, and every later verification
A-001..A-010 ──> M05 ──> M06 ──> M08
A-009 ──> A-010 ──> OP-1xx invariants
S-001 ──> P-005 ──> S-004
CF-004 ──> OP-108
PS-004 ──> PS-001
```

## Completed — evidence

Recorded here rather than inline to keep the task bodies readable. Each entry names the
commit and the test that proves it.

| Task | Commit | Evidence |
| --- | --- | --- |
| **P-001** | `5a676ae` | `provider.go` extracts only `message` items and `output_text` content. `TestOpenAIResponsesIgnoresReasoningOutputItems` failed against the old parser with the observed value `First I identify the name, then the age.{"name":"John","age":30}` and passes now. |
| **P-002** | `5a676ae` | `usage.output_tokens_details.reasoning_tokens` parsed; `TestOpenAIResponsesParsesTokenDetails`. |
| **P-003** | `5a676ae` | `usage.input_tokens_details.cached_tokens` parsed; same test. |
| **P-004** | `5a676ae` | `FinishReason` mapped from `status` / `incomplete_details`; `TestOpenAIResponsesReportsTruncationFinishReason`. A response with no message item now errors — `TestOpenAIResponsesReasoningOnlyIsAnError`. |
| **P-006** | `5a676ae` | One `http.Client` per provider; `TestOpenAIProviderReusesHTTPClient`. |
| **P-010** | `bfebb11` | Tiers resolve to `gpt-5.6-luna` via `ModelDefault*`; `TestGetModelUsesGPT56FamilyForOpenAI`, `TestExplicitModelOverrideBeatsTierDefault`. **Partial** — the luna/sol/terra split still needs P-013. |
| **P-011** | `bfebb11` | Reasoning controls omitted for `gpt-5.6*`; `TestGPT56OmitsUnverifiedReasoningControls`. **Partial** — confirm against the live API in P-013. |
| **PR-001** | `0c867e3` | `CostInfo.Priced` / `PricingSource`; `getDefaultPricing` deleted. `pricing/unpriced_test.go` covers unknown models, the Anthropic substitution, exact matches, and snapshot resolution. |
| **PR-007** | `0c867e3` | See below — found while testing PR-001. |
| **F-001** | `ea1730d` | `Validate` returns an error on parse failure; `internal/ops/validate_failopen_test.go` covers four negative phrasings plus the well-formed success and failure paths. |

| **F-012** | `2c917e6` | `Init` returns an error when no credential resolves and the provider is not `local`; `client_env_test.go`. |
| **F-013** | `2c917e6` | Subsumed by B-03 — `InitWithEnv` can now fail. |
| **F-014** | `2c917e6` | `os.Setenv` removed from `InitWithEnv`. |
| **B-02** | `2c917e6` | `OPENAI` added to the OpenAI key chain after the two canonical names, rather than editing the operator's credential file. |
| **B-03** | `2c917e6` | `godotenv` promoted to a direct dependency; named paths load, `./.env` is the default, a missing named path errors, and the process environment wins over the file. |
| **F-002** | `0915455` | All four `*WithMetadata` fallbacks replaced with `ParseJSON` plus an error return; `internal/ops/text_failopen_test.go` covers 40 cases across the four operations. |
| **F-003** | `234c6db` | An unusable `corrected` payload is now reported instead of dropped; `TestValidateReportsUnusableCorrection`, `TestValidateReturnsUsableCorrection`, `TestValidateNullCorrectionIsNotAnError`, and `TestIntegrationValidateCorrections` at the public boundary. |
| **F-004** | `234c6db` | `Valid` is always derived from the issue lists, `FailOn` defaults to `error`, and an unknown severity is an error; `TestValidateDerivesValidityFromIssues` (7 cases), `TestValidateFailOnSeverities` (3), `TestValidateRejectsUnknownFailOn`, `TestIntegrationValidateDerivesValidityFromIssues`, and `Example_validateAutoCorrect`. |
| **F-005** | `6653590` | `Decide` returns an error on provider failure, unparseable answer, and out-of-range index; a fallback is opt-in via `NewDecideOptions().WithFallback(i)` and sets `DecisionResult.Fallback`. `internal/ops/decide_failopen_test.go` (10 failure bodies plus fallback, config, success, cancellation) and `controlflow_integration_test.go` at the public API. |
| **F-006** | `6653590` | `Decide(ctx context.Context, situation any, ...)` — the real context governs cancellation, the prompt data is named for what it is. `TestDecideHonoursACancelledContext`. |
| **F-007** | `6653590` | `Match` returns `(bool, error)`; a provider failure or an answer that is neither true nor false is reported rather than read as a non-match. `internal/ops/control_flow_failopen_test.go`. |
| **F-008** | `6653590` | The default case is collected and evaluated last wherever it appears. `TestOtherwiseIsEvaluatedLastWhereverItAppears`, `TestOtherwiseRunsWhenNothingMatches`, `TestIntegrationOtherwiseDoesNotShadowLaterCases`. |
| **F-009** | `6653590` | `Compose[T]` chains every operation over one type; the old `Compose[T, U]` signature could never chain, because operation 1's output type did not match operation 2's input. `TestCompose` (5 subtests) replaces the test that had enshrined the bug. |
| **F-010** | `6653590` | `attempts = MaxRetries + 1` when retries are on. `TestPipelineRunsEveryStepWithZeroValueMaxRetries` fails against the old code by running every step zero times; `TestPipelineAttemptCounts` covers seven retry counts. |
| **F-011** | `6653590` | Backoff uses a cancellable timer (`sleepWithContext`) and `PipelineOptions.RetryDelay` makes it configurable. `TestPipelineBackoffIsCancellable`, `TestSleepWithContext`, `TestPipelineTimeoutStopsBetweenSteps`. |
| **F-015** | `6166e38` | `json:"-"` fields are skipped entirely, following the encoding/json grammar (`-` alone excludes; `-,` names the field `-`). `internal/ops/schema_determinism_test.go` covers eight tag shapes plus an all-excluded struct; `schema_integration_test.go` asserts the excluded names never appear in the bytes the provider receives. Found while here: `required` was a substring test on the whole tag, so a field named `omitempty_flag` was reported optional — now `hasJSONOption`. |
| **F-016** | `6166e38` | `CalculateParsingConfidence` deleted along with its only call site. |
| **F-017** | `6166e38` | `Confidence: opt.Threshold - 0.1` deleted, and with it `ExtractError.Confidence`: an error has no confidence, and a zero-valued field invites the next guess. `TestFailedExtractionCarriesNoInventedConfidence`. |
| **F-018** | `6166e38` | `estimateCompletionConfidence` deleted; `CompleteResult.Confidence` stays zero. `TestCompleteReportsNoInventedConfidence`. The test that asserted the heuristic's output ranges is deleted with it. |
| **F-019** | `6166e38` | `BatchResult.TokensSaved` deleted, and `EstimatedCost: float64(apiCalls) * 0.01` with it — the same fabrication one field over, in a library that has a pricing package. |
| **CA-001** | `6166e38` | All six map-into-prompt sites sorted through `sortedKeys`. `TestPromptsAreByteIdenticalAcrossRuns` renders each twenty times and compares bytes; every subtest was verified to FAIL against the unsorted code before the fix landed. `TestIntegrationRepeatedCallsSendIdenticalBytes` checks the same at the provider boundary. |
| **F-029** | `1d9adae` | `NewClient("")` leaves the client with no provider and logs why; the mock is reached with `WithMockProvider()`. `client_concurrency_test.go` covers both paths plus chaining. |
| **F-030** | `1d9adae` | `ops.ErrNoProvider` names `Init`, `InitWithEnv`, and all four credential variables, and `ExtractError` gained `Unwrap` so `errors.Is` reaches it. `TestNoProviderErrorNamesTheWayOut`, `TestUninitialisedOperationErrorNamesTheWayOut`. |
| **F-031** | `1d9adae` | `GetDefaultClient`, `GetLogger`, `ConfigureLogging`, and `SetLogLevel` take the locks `Init` writes under. `TestConcurrentInitAndLoggerAccess` runs eight workers over all five entry points, `TestGetLoggerBeforeInit` covers the nil-client window. **Caveat:** `-race` is unavailable on windows/arm64, so the test exercises the interleaving without the detector; run it under `-race` on amd64 or linux to confirm. |
| **F-034** | `1d9adae` | Every error struct stores a description instead of the payload: `InputShape`, `AShape`/`BShape`, `ItemCount`, `OptionCount`, `PromptShape`, via `types.DescribeValue`. `ClassifyError.Error()` no longer prints the input with `%q`. `internal/ops/error_payload_test.go` drives seven operations with a payload marker and asserts no fragment survives. Closes **X-03** with F-033. |
| **DOC-004** | `1d9adae` | README gained a Credential resolution section (five-step order, why a missing key is an error, how to ask for the mock) and a `.env` section (default `./.env` optional, named paths required, process environment wins). Checked against `resolveProviderAPIKey`. |
| **F-025** | `b4220cc` | `ShellTool` is out of the default registry and out of `ToOpenAIFormat`; `tools.EnableShell(ShellPolicy{AllowedCommands: …})` registers it, `DisableShell` removes it. The policy also confines the working directory and caps the timeout, and shell metacharacters are refused so an allowed base name cannot carry a disallowed command. `internal/tools/shell_policy_test.go` covers 12 refusal cases plus the directory and timeout bounds. |
| **F-026** | `b4220cc` | The `token` tool's JWT path returns a refusal instead of a successful-looking `StubResult` from a tool not marked `IsStub`. `internal/tools/stub_honesty_test.go` parses the package and fails any tool that can reach `StubResult` without the flag — verified to catch a removed flag on `WebSearchTool`. Found while there: `generateRandomToken` hashed the current nanosecond, so tokens from a security-category tool were a deterministic function of their issue time; it uses `crypto/rand` now. Two stub tools whose descriptions did not say so now do. |
| **F-027** | `b4220cc` | All 79 `_ = Register(...)` calls became `mustRegister`, which panics at init on a duplicate. `Registry.Unregister` added, which `DisableShell` needs. `TestRegisterReportsADuplicate`, `TestMustRegisterPanicsOnADuplicate`, `TestUnregister`. |
| **F-028** | `59a0048` | `faultinjection_integration_test.go` drives 57 exported operations through four faults — provider error, malformed body, schema-violating body, empty body — at the public API. It found real defects rather than confirming the fixes: `Explain`, `Question`, and `FormatWithMetadata` still manufactured results from unparseable bodies with invented confidences of 0.5 and 0.7, and roughly 35 operations accepted well-formed JSON of entirely the wrong shape and reported success with every field empty. Both are closed; see **F-035** and **F-036**. `TestFaultInjectionCoversTheExportedSurface` guards the table against falling behind the API. |

### Added during the work

- [x] **F-035** — `encoding/json` ignores unrecognised fields, so a well-formed object of
  entirely the wrong shape unmarshalled into a zero value and returned no error. Roughly 35
  operations therefore reported success with every field empty: a cluster operation returning
  no clusters, a verification returning no verdict, a projection returning an empty record.
  `ParseJSONStrict` requires the body to carry at least one of the target's JSON field names —
  a deliberately weak rule that catches an answer about something else without rejecting a
  model that omits an optional field. All 62 body-parsing sites in `internal/ops` use it.
  *Verify:* `internal/ops/json_strict_test.go` (10 rejections, 8 acceptances, the
  no-field-names passthrough cases, embedded structs) and the fault-injection suite.
- [x] **F-036** — `Explain`, `Question`, and `FormatWithMetadata` had the same fail-open
  fallback F-002 removed from the text operations: an unparseable body became the result, with
  an invented confidence of 0.5 or 0.7 and, for `Explain`, a fabricated key point reading
  "Explanation generated". `Question` also logged the entire response, which is the caller's
  data. All three return the error now.
  *Verify:* the malformed-body arm of the fault-injection suite.
- [x] **F-037** — `Diff` reported a failed summary as the prose "Summary generation failed,
  but changes detected successfully" in the `Summary` field, which reads like a summary. The
  structural comparison is computed locally and is still correct, so the operation still
  succeeds — but `DiffResult.SummaryError` now says the explanation is missing, and an empty
  body is treated as a failed summary rather than an empty one.
  *Verify:* `TestFaultInjectionDiffKeepsTheStructuralResultAndReportsTheMissingSummary`,
  `TestDiffReportsAWorkingSummary`.
- [x] **F-038** — The shared test mock answered every operation with `{"mock": "response"}`,
  which shares no field with any target. Every test using it was asserting that an answer
  about something else parses successfully — the exact behaviour F-035 removed. It answers
  with a body shaped like `Person` now.

- [x] **PR-007** — `lookupPricingModel`'s prefix ladder matched `gpt-5.6-luna` against the
  plain `gpt-5` entry, because it only tested `strings.HasPrefix`. That is the same
  substitution error as PR-001 one layer down: a different model priced at another model's
  rates. Prefix matching now requires the suffix to be empty or to begin with `-`, so a dated
  snapshot still resolves to its base while a minor-version bump does not.
  *Verify:* `TestVersionBumpIsNotTreatedAsSnapshot`, `TestDatedSnapshotsStillPriceFromBaseModel`.
  Found because `TestGPT56FamilyIsUnpricedUntilRatesAreKnown` skipped where it should have
  asserted — the skip was the signal.

- [x] **F-029** — `NewClient("")` still constructs the mock provider directly
  (`client.go:51-54`). F-012 closed the `Init` path, but a caller who builds a client by hand
  with an empty key gets the same silent fake output. Either require a key or take an explicit
  `WithMockProvider()`.
  *Verify:* `NewClient("")` returns an error or a client whose provider is nil, never a mock
  chosen by accident.
- [x] **F-030** — When `Init` fails and the caller discards the error, `defaultClient` stays
  nil and every operation reports `"no LLM provider configured"`
  (`internal/ops/llm_helper.go:47`), which does not say what to do. Make that error name
  `Init`/`InitWithEnv` and the credential variables it looked for.
  *Verify:* the message from an uninitialised call names at least one env var and one
  function.
- [x] **F-031** — `Init` now returns an error while `ConfigureLogging`, `SetLogLevel`, and
  `GetLogger` still read `defaultClient` without the mutex that `Init` writes it under. The
  new early return widens the window in which `defaultClient` is nil. Fold into **IN-001** if
  that lands first; listed separately so it is not lost.
  *Verify:* `go test -race` on a test that calls `Init` and `GetLogger` concurrently.
- [x] **DOC-004** — Document the credential resolution order in the README, including the
  `OPENAI` alias added by B-02 and the fact that `.env` supplies defaults rather than
  overrides. The README's Environment section currently lists neither.

- [x] **PERF-001** — The retry classifier treats an empty completion as retryable
  (`llm_helper.go:248`), so a test feeding an empty body waits out the full backoff: the
  four-case integration table takes 3.5s of wall clock for what should be instant. Real
  behaviour is right; what is missing is a way to run it fast. Add a per-call retry override
  so tests and latency-sensitive callers can set zero attempts without touching global env.
  *Verify:* the same table runs in well under 100ms with retries disabled, and unchanged with
  them enabled.

- [x] **F-032** — **14 options structs embed both `CommonOptions` and `types.OpOptions`, and
  `toOpOptions()` returns only the `CommonOptions` half.** Everything set on the embedded
  `OpOptions` — `RequestID`, `CorrelationID`, `Context`, `Threshold` — is silently discarded.
  Affects `Extract`, `Transform`, `Generate`, `Summarize`, `Rewrite`, `Translate`, `Expand`,
  `Classify`, `Score`, `Compare`, `Choose`, `Filter`, `Sort`, and `Batch`: the most-used
  operations in the library. Found while writing the P-00x integration test, where setting
  `opts.OpOptions.RequestID` produced no cost record because the field never reached the call.
  Merge both halves, with the `CommonOptions` side winning on conflict, and note that `Mode`
  and `Intelligence` cannot be merged correctly until **A-005** fixes the zero-value collision.
  *Verify:* per-struct table test asserting `RequestID`, `CorrelationID`, and `Context` set on
  either embedded struct reach the provider. This is the same class as **X-04**, one level up:
  not a dead field but a dead *struct*.
- [x] **F-033** — `ParseJSON` embedded the entire cleaned model output in its error string, and
  every caller logs that error, so user payloads landed in every log aggregator. Partially
  closes **X-03**; `types.ExtractError.Input` and the other error structs that retain payloads
  are still open.
  *Verify:* `internal/ops/json_redaction_test.go`.
- [x] **F-034** — Finish **X-03**: `types.ExtractError` and its siblings still store the raw
  `Input`. Same reasoning as F-033 — an error that carries the payload copies it wherever the
  error goes.
  *Verify:* a payload marker present in the input never appears in any error's `Error()`.

- [x] **PERF-002** — `Client.WithRetries(0)` could not disable retries. The dispatcher tested
  `maxRetries <= 0` and substituted the global default of three, so a caller who explicitly
  asked for none still waited out two backoffs, and the documented option did nothing. Negative
  now means "not configured, use the default"; zero means zero. Root-package suite went from
  7.1s to 0.135s, and the whole suite now runs in 1.2s.
  *Verify:* `internal/ops/retry_policy_test.go` — disabled, four configured budgets,
  negative-as-default, cancellation, deterministic failures, and four non-retryable messages.

- [x] **PR-008** — `lookupPricingModel` normalised case and whitespace *after* the exact map
  lookup, so `"GPT-4"` or a value with stray spaces missed the table, fell through the prefix
  ladder, and was reported unpriced. An accidental gap in the one signal that is meant to mean
  "we genuinely do not know this rate". Normalisation now happens first.
  *Verify:* `TestModelLookupToleratesCaseAndWhitespace`.

### Standard of done

Raised mid-session, and applied from **F-002** onward. Every closed task carries:

1. **unit tests** exercising the changed function directly, **at least 10 cases**;
2. an **integration test** through the exported API with a provider injected via
   `WithProviderInstance`, so the whole stack is covered, not just the unit;
3. for an **LLM-backed (Smart+) operation**, a runnable **`Example` function with verified
   output** — these execute under `go test` with a scripted provider, so an example that
   rots fails CI, and none of them need a credential or spend.

Tasks closed before this bar was set (F-001, F-012–F-014, P-00x, PR-001) have unit coverage
and, where the boundary mattered, integration coverage; they do not all have examples.

- [x] **TEST-001** — Backfill the raised bar onto the tasks closed before it: examples for
  the LLM-backed operations touched by F-001 (`Validate`) and integration coverage for the
  provider fixes P-001 through P-004, which are currently unit-level only.

### Refined

- **PR-002** — downgraded from blocking to opportunistic. With PR-001 and PR-007 in place an
  unpriced model reports unpriced rather than a wrong figure, so shipping without gpt-5.6
  rates is safe. Needs a published rate card as its source; do not populate the table from a
  guess, which is the exact failure PR-001 removed.
- **P-010 / P-011** — partially closed. The tier mapping and the capability gates are wired
  conservatively; the luna/sol/terra split and the confirmation of accepted reasoning-effort
  values both still depend on **P-013**, which is blocked on **B-01**.
- **F-024** — widen the static check from dead options to dead unexported helpers as well.
  `getDefaultPricing` sat unused-but-harmful for exactly the same reason a dead option does,
  and the same check would have surfaced it.
- **CI-006** — call out `testdata/*.env` explicitly. This repository now ships a fixture env
  file, and a fixture is the most likely place for a real key to be pasted by accident.
- **DOC-002** — add `Init` to the breaking-change list. It gained an `error` return; that is
  source-compatible because Go permits discarding a call's result, but a caller who wants the
  check must now add it.

## Standing rules for this list

Close each item in the same commit as the work it describes, with the evidence beneath it —
the test that proves it, the file that now exists, the measurement that was taken. Never mark
an item complete because a model stopped: the output must exist and its verification must
pass. If you finish something this list does not contain, add the item and check it in the
same commit, so the record still matches the work.

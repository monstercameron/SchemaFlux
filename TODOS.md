# SchemaFlux Production Readiness — Task List

Authority: this file answers **in what order**. `AGENTS.md` answers **how**.
`docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md` answers **why** — every task below
cites the finding or gap ID it closes. `docs/engineering/plans/to-production.md` answers
**what the end state is** — the target architecture, its gap register (`ARC`, `PRD`, `API`,
`EXE`, `TRU`), its delivery gates, and its v1 acceptance criteria.

Goal: production-ready, stable, tested. That means three things concretely — no operation
fails open, every result is verified against a declared contract, and every claim in the
README is backed by a test that runs in CI.

The review and the specification are not the same kind of input. The review is a list of
defects in code that exists; the specification is a description of a library that does not
exist yet. M01–M10 close the review. M11–M17 build the specification. Where the two
disagree, see **Reconciliation with `to-production.md`** near the end of this file — every
disagreement is ruled on there, not left for the next agent to rediscover.

## How to read this

- **Milestones (`M00`–`M17`) are ordered by dependency**, not by importance. `M00`–`M10`
  close the adversarial review; `M11`–`M17` build the target architecture and are scheduled
  but not yet committed — see the reconciliation section. Do not start
  a milestone whose predecessors are open unless the task says otherwise.
- **Task IDs are stable.** Never renumber. A dropped task becomes `WONTFIX` with a reason.
- **Citations.** A task cites review findings (`I-01`, `D-15`, `Gap-06`) and specification
  gaps (`ARC-01`, `PRD-05`, `API-04`, `EXE-13`, `TRU-10`). Cite a specification *decision*
  by its ADR number (`ADR-003`), never by its `D-00x` label: `.audit/traceability.py` reads
  `D-002` as one of the review's `D-xx` schema findings and reports it dangling.
- **A `Revised:` line supersedes the task body above it.** It records what
  `to-production.md` changed about the task's scope, bar, or verification — the original
  wording is kept so the change is visible rather than silently rewritten.
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

- [x] **B-01** — *(Cleared 2026-08-05; the box lagged the evidence row by two days.)*
  OpenAI account has no credits. `POST /v1/responses` returns
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
- [x] **B-04** — Establish the live-test gate before any `[LIVE]` task runs: provider-backed
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
- [x] **F-020** — Rename every model-self-reported score to make its provenance explicit
  (`ProjectResult.Confidence`, `EnrichResult.Confidence`, `NormalizeChange.Confidence`,
  `VerifyResult.TrustScore` / `OverallConfidence`, `CritiqueResult.OverallScore`,
  `AuditFinding.Severity`, `ClusterResult.Quality`). Closes **D-09**.
  *Verify:* grep asserts no exported field named exactly `Confidence` survives on a result
  struct without a documented measured source.

## Redaction — mark unusable, then fix

- [x] **F-021** — Mark `Redact`, `RedactWithResult`, and `RedactLLM` not-production-ready in
  their doc comments and in the README until M05's redaction tasks land. Covers **T-07**,
  **T-08**, **T-09**, **T-11**, **T-13**.
- [x] **F-022** — Delete the "privacy filtering" framing from `Project`'s package
  documentation (`project.go:83-107`), whose first example is `Exclude: []string{"password_hash", "ssn"}`
  against a mechanism that is a prompt hint with no post-filter. Closes the documentation half
  of **D-08**.

## Dead options

- [x] **F-023** — Implement or delete the dead options. Each has a fluent setter, so the call
  chain reads as if it configured something: `BatchOptions.OnProgress` / `PreProcess` /
  `PostProcess`, `PipelineOptions.SaveProgress`, `ClassifyOptions.CategoryExamples`,
  `TransformOptions.To`, `ClusterOptions.MaxClusterSize`, `RedactOptions.PreserveFormat`,
  `ProjectOptions.PreserveNulls`, `SynthesizeOptions.OutputStructure`,
  `AnnotateOptions.Language`, `CompareOptions.IncludeSimilarity`, `ScoreOptions.IncludeBreakdown`,
  `GenerateOptions.EnsureUnique`, `FilterOptions.BatchSize`, `GuardResult.RetryAfter`.
  Closes **X-04**, **C-10**, **D-10**, **CF-10**.
  *Verify:* **F-024**.
- [x] **F-024** — Add a static check that fails when an exported options-struct field is
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
- [x] **P-005** — Send `text.format = {type: "json_schema", strict: true, schema: ...}` using
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
- [x] **P-007** — The non-200 path embeds the raw response body into a `fmt.Errorf`
  (`provider.go:175-178`). Return a typed `*APIError{StatusCode, Provider, Model, Body}` and
  redact the body by default. Feeds **Gap-12** and **X-03**.
  *Verify:* `errors.As` recovers the status code; the default `Error()` string contains no
  request payload.
- [x] **P-008** — Set `store: false` by default. The Responses API retains responses
  server-side unless told otherwise, which is a surprising default for a library that
  processes arbitrary user records. Expose `option.Store(true)` for callers who want it.
  *Verify:* request body asserts `"store": false` unless explicitly overridden. **Done** —
  `TestStoreIsFalseByDefault` / `TestStoreCanBeTurnedOn`; `store: false` accepted live by all
  three 5.6 models.
- [ ] **P-009** — Send `prompt_cache_key` derived from `(op, T, tier)` so repeat requests
  route to a server holding the prefix. Depends on **CA-002**.
  *Verify:* recorded request contains a stable key across two identical calls and a different
  key across different ops.
  **Revised (TRU-10, PRD-14):** `(op, T, tier)` is not enough identity. The key must cover
  operation name *and version*, prompt template version, schema hash, resolved model, and
  the normalized options that change the stable prefix. A key that omits the prompt version
  routes a request to a server holding a prefix a previous release wrote — the cache is
  keyed on something coarser than the thing being cached.
  *Verify (added):* editing a prompt literal changes the key; changing only steering does
  not, because steering is volatile and lives outside the stable prefix (**CA-002**).
- [x] **P-010** — Update the tier mapping to the 5.6 family confirmed live:
  `gpt-5.6-luna`, `gpt-5.6-sol`, `gpt-5.6-terra` (`internal/config/config.go:194-203`).
  Assign Smart/Fast/Quick by measured capability and price, not by guessing from the names —
  **P-013** produces that evidence. Until then, leave the mapping behind a constant with a
  TODO rather than picking arbitrarily.
  *Verify:* `GetModel` returns 5.6 IDs for the OpenAI provider; pricing table has an entry for
  each (**PR-002**).
  **Done** — the mapping is measured rather than guessed, which is what the task asked for:
  `Quick` takes terra (fastest, 4/4 correct on both benchmark tasks), and Smart and Fast both
  stay on luna because nothing in the evidence separates luna from sol. The comment in
  `config.go` carries the latency and accuracy table. `TestGetModelUsesGPT56FamilyForOpenAI`,
  `TestExplicitModelOverrideBeatsTierDefault`, `internal/config/model_defaults_test.go`.
  The verify line's second clause is **withdrawn**, not met: **PR-002** was downgraded to
  opportunistic because populating rates from a guess is the failure **PR-001** removed, and
  with PR-001 and PR-007 in place the 5.6 family reports *unpriced* rather than a wrong
  figure. Splitting Smart from Fast is **P-017**, which needs a discriminating benchmark.
- [x] **P-011** — Revisit `supportsTemperature()` and `supportsReasoningControls()`
  (`provider.go:122`, `136-141`) for the 5.6 family. Both currently pattern-match on model
  name prefixes and will silently misclassify the new IDs.
  *Verify:* unit test per model ID asserts the expected capability flags; **P-013** confirms
  them against the live API.
- [ ] **P-012** `[LIVE]` — Live smoke: one `/v1/responses` call per 5.6 model, asserting
  HTTP 200, non-empty message text, and populated usage including reasoning tokens.
  Blocked on **B-01**.
- [x] **P-013** `[LIVE]` — Capability matrix across `luna` / `sol` / `terra`: strict
  `json_schema` support, temperature acceptance, reasoning-effort acceptance, cached-token
  reporting, and observed latency and cost per model. This is the evidence **P-010** and
  **P-011** need. Record the results in the task, not from memory. Blocked on **B-01**.
- [ ] **P-014** — Record the responses from P-012 and P-013 as cassettes (**TI-003**) so the
  golden tests above run in CI without credits, forever. P-013's *findings* are now pinned as
  unit tests (`internal/llm/capabilities_test.go`), so a regression in the predicates is caught
  without credits; what remains is replaying real response *bodies*.

## Other providers

- [x] **P-015** — `GetModel` falls through to OpenAI model IDs for `deepseek`, `qwen`, `zai`,
  and any custom provider (`config.go:194-203`), so the README's `WithProvider("deepseek")`
  sends `model: "gpt-5.4"` to `api.deepseek.com` and 400s. The Anthropic provider defends
  itself (`provider.go:366`); the OpenAI-compatible path does not. Add a per-provider default
  map and validate at construction. Closes **I-03**.
  *Verify:* table test per provider asserts a plausible model ID; construction with an unknown
  provider/model pair returns an error.
  **Revised (ARC-09):** the model map is the first half. Provider modules own credential
  resolution, endpoint defaults, model mapping, schema adaptation, retry classification, and
  usage normalization — none of which belong in the generic client. FL-001 landed the map;
  what remains is moving the rest of the per-provider knowledge behind the provider
  boundary so `internal/config` stops knowing vendor names. Feeds **CP-001**.
  **Done** — **FL-001** built the per-provider map and made an unmapped provider resolve to
  nothing; this closes the validation half. `config.ValidateModelResolution` checks **all
  three tiers**, because a caller who sets only `SCHEMAFLUX_MODEL_SMART` has one working path
  and two broken ones, and the operation that discovers it is whichever runs first — most
  default to Fast. `WithProviderConfig` and `Init` now report it: `Init` returns the error it
  always had a return value for, and a builder chain puts it on `Err()`. The provider is
  still attached, because the fix is an environment variable and dropping their provider
  would cost the caller the rest of their configuration.
  Unit: `internal/config/model_resolution_test.go` — 8 tests, 30 leaf cases, including
  `TestValidateModelResolutionAgreesWithGetModel`, which fails if the check and `GetModel`
  ever disagree about which pairs resolve. Integration: `client_model_resolution_test.go` —
  9 tests through the exported API covering construction, `Init`, override rescue, provider
  switching, and `WithProviderInstance` (exempt: it carries its own model decisions, and
  asserting that keeps a later change from breaking `schemafluxtest.Install`).
  Found while here: an entirely unknown provider name fails earlier, in
  `llm.CreateProvider`, and the two failures read differently — `TestUnknownProviderNameFailsAtConstruction`
  pins that one too. **Not closed by this:** `ErrNoModelMapping` lives in `internal/config`,
  so an external consumer can match the message but not the sentinel. That is **A-007**'s
  taxonomy, not a second sentinel here.
- [ ] **P-016** — The Anthropic provider hardcodes `max_tokens: 1024` before conditionally
  overriding it (`provider.go:383`, `394-396`) and never sends `cache_control`. Wire the real
  ceiling and add prompt-cache breakpoints. Depends on **CA-003**. Addresses **Gap-10**.
  *Verify:* recorded request asserts the configured ceiling and a breakpoint on the last
  stable block.

---

# M03 — Test infrastructure

Built before the redesign, because every later milestone is verified with it. This is also
**Gap-06**, the single largest adoption blocker in the review: today there is no supported way
to test code that uses this library without paying a provider.

- [x] **TI-001** — Ship `schemafluxtest` with an exported fake provider: scripted responses
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
  **Revised (PRD-13):** a cassette records more than the two bodies. It carries provider and
  model, operation/prompt/schema versions, the request body, the response headers that
  change behaviour (`Retry-After`, request ID), usage, the expected decoded result, and the
  expected failure classification — otherwise a replay proves the parser works and nothing
  about whether the runtime classified the failure the same way. Replay drives the real
  adapter and the real executor path, not a shortcut through the parser.
- [ ] **TI-004** — Golden-prompt tests: for each operation, snapshot the exact rendered system
  and user prompt for a fixed input. Prompt changes then become reviewable diffs instead of
  silent behavior changes for every downstream user. Closes the testing half of **Gap-13**.
  *Verify:* editing any prompt literal fails a golden test until the snapshot is updated.
- [x] **TI-005** — Determinism test: build the same prompt twice from identical options and
  assert byte equality. This currently **fails** — Go map iteration order randomizes prompt
  bytes (see **CA-001**). Write it now so the fix has a witness.
  *Verify:* the test fails today and passes after CA-001.
  **Done, with CA-001** (`6166e38`) — the task asked for the test to be written first so the
  fix had a witness, and that is what happened: `TestPromptsAreByteIdenticalAcrossRuns`
  renders each of six map-into-prompt sites twenty times and compares bytes, and every
  subtest was verified to FAIL against the unsorted code before the sort landed.
  `TestIntegrationRepeatedCallsSendIdenticalBytes` checks the same at the provider boundary.
  The box was left open.
- [ ] **TI-006** — Fault-injection harness backing **F-028**: provider error, malformed body,
  schema-violating body, truncated body, empty body — parameterized across every exported
  operation.
- [ ] **TI-007** — Property tests for the collection invariants: for random inputs,
  `Filter` output is a subset of input, `Sort` output is a permutation of input, `Choose`
  output is a member of input, `Cluster` output partitions input exactly once.
  *Verify:* each property fails against today's implementation and passes after **OP-101**–
  **OP-105**.
  **Revised (EXE-13, ARC-16):** the property set is the operation's declared batch algebra,
  not a hand-picked list. Appendix C of `to-production.md` assigns every family a class —
  independent, subset, permutation, partition, graph, hierarchical, sequential — and the
  property test is generated from the declaration, so an operation cannot acquire a
  batchability class without acquiring its check. Depends on **PL-006**.
- [ ] **TI-008** — Concurrency tests under `-race` for the package globals: `defaultClient`
  (`client.go:192-236`), `ops.defaultProvider`, `ops.customLLMCaller`, and `pricing`'s
  package state. Closes **I-14**.
  *Verify:* `go test -race` is green; the review notes `-race` cannot run on the current
  Windows/arm64 machine, so this requires **CI-002**.
  **Revised (ARC-01, ARC-07):** race-freedom is the weaker half. The specification's exit
  criterion for a safe core is *isolation*: two clients with different providers, budgets,
  and data policies running concurrently must not observe each other's configuration. A
  mutex around a package global passes `-race` and still fails that test. Add the isolation
  case alongside the race case; it is the one that fails today by construction.
  *Verify (added):* two clients with different fake providers and different budgets run
  concurrently and each sees only its own.
- [x] **TI-009** — UTF-8 corpus fixtures (CJK, emoji, combining marks, RTL) used by every
  truncation, slicing, and redaction test. Closes the test half of **T-06**.
  **Done** — `internal/ops/utf8corpus_test.go` holds one corpus of eleven entries, chosen for
  what breaks rather than for coverage of Unicode: accented Latin (two bytes, one rune), CJK
  (three bytes, no spaces to split on), emoji including a ZWJ sequence (several runes that
  display as one), decomposed combining marks (seven visible characters, nine runes), RTL
  Arabic and Hebrew (visual order is not byte order), and a mixed string.
  Four tests consume it: truncation at **every** cut point of every entry; masking, which has
  to reveal characters not bytes; length measurement; and redaction span location. A new
  operation inherits the cases instead of rediscovering them.
  Found while writing it: the combining-marks entry has to be spelled with an explicit
  `́`, because a precomposed é is one rune and tests nothing — the test asserts every
  non-ASCII entry actually differs in byte and rune length, so a corpus entry that exercises
  nothing fails.
- [x] **TI-010** — Cost-accounting tests: assert an unpriced model reports "unpriced" rather
  than `$0.00`, and that a priced model's arithmetic matches a hand-computed figure.

---

# M04 — The core: `Op`, `Run`, `Invariant`, options, result, errors

Primitives 1–5 and 7–8 from the review's target shape. Build alongside the existing code;
port nothing yet except one operation as the proof.

  **Done** — `pricing/accounting_test.go`. The unpriced path across six models, asserting
  zero-with-`Priced:false` is never confusable with free; the arithmetic against a
  hand-computed figure using a model registered by the test, so it does not depend on a rate
  card that will change; cached and reasoning tokens costed separately, with an explicit
  assertion that cached tokens are *not* charged at the prompt rate (nine times too much in
  that case); a missing rate contributing nothing rather than inventing a number; PR-007's
  snapshot-versus-version-bump distinction; nil usage; and a summary of unpriced calls not
  reading as a summary of free ones.
- [ ] **A-001** — `Op[In, Out]` descriptor: `Name`, `Prompt`, `Format`, `Schema`, `Decode`,
  `Invariants`. Closes the structural half of **D-15**.
  **Revised (ARC-04, ARC-16, API-09):** the descriptor is wider than six fields and it is
  *public*. `OperationID{Name, Version}` — a version, because a prompt edit is a behaviour
  change and **PS-007** needs something to hang it on; `Semantics` (category, whether
  inference is permitted, whether evidence is required, identity/order preservation,
  stability tier); `OutputContract` (schema, decoder, invariants, evidence policy,
  normalizers); `BatchAlgebra` (class, encode, merge, global validation); `DefaultPolicy`.
  It holds no context and no provider — it is data plus pure functions, which is what makes
  planning, recipes, loop fusion, and middleware possible without a second execution path.
  *Verify (added):* a caller outside the module can construct an `Op`, inspect it, and run
  it; a stable operation with no declared batch class fails a build-time check.
- [x] **A-002** — `Run[In, Out](ctx, client, op, in, opts...)` owning context propagation,
  response-format selection, provider dispatch, retry, decoding, invariant checking, repair,
  usage accounting, and telemetry. Closes **X-01** at one call site instead of thirty-one.
  *Verify:* cancelling the context aborts the call; assert with the fake provider's slow mode.
- [x] **A-003** — Single JSON extraction path, hardened once: fenced blocks anywhere, leading
  and trailing prose, and a brace-matching scan. Replaces the 16 hand-rolled fence strippers
  and the parallel `cleanJSON`. Closes **X-02**.
  *Verify:* corpus test of malformed-but-recoverable bodies; each of the 16 old sites is
  deleted, not left behind.
  **Done — 22 sites, not 16.** `internal/ops/jsonextract.go` is the one path: a fenced block
  anywhere in the response (with the info string checked, so a ```python block in an
  explanation is not mistaken for the payload), then a brace-matching scan that is string- and
  escape-aware, so a `}` inside a string value cannot end it early. `cleanJSON` is a one-line
  call through to it and every operation reaches it via `ParseJSON`.
  The old strip handled one shape: a response that is *nothing but* a fence, opening at the
  first character and closing at the last. Models do not reliably produce that — they produce
  "Here is the JSON you asked for:" and then a fence, or a fence and then an explanation, or
  an unfenced object after a sentence. Every one of those was an error whose message blamed
  the JSON for being malformed when the JSON was fine.
  *Verify:* `internal/ops/jsonextract_test.go` — a 15-case corpus, plus the parse path over
  it, plus truncation reaching the decoder rather than being disguised, plus prose and error
  pages still failing, plus F-033's no-payload rule still holding.
  **`TestTheOldStripHandledAlmostNoneOfIt` is the witness and states the number:** the old
  implementation is kept in the test file and scored against the corpus — **2 of 8 recovered,
  against 8 of 8**. If a later change makes the new extractor no better, that test fails.
  Integration: `jsonextract_integration_test.go` drives `Extract[invoice]` through all eight
  packagings at the public boundary, plus prose, an HTML error page, an empty body, and a
  truncated one.
- [ ] **A-004** — `RequestOption` applied at both client construction and per call, with
  per-call precedence. Closes **I-07**, **I-08**, **I-09**, **Gap-14**, and the
  order-dependence trap in `Client.With*`.
  *Verify:* `option.Model(...)` on one call does not leak into the next; setting timeout after
  provider construction takes effect.
  **Revised (ARC-08, API-07, API-08):** "per-call precedence" is too blunt a rule. Every
  setting declares exactly one scope — process, client, operation descriptor, invocation,
  request context, provider request — and precedence is deterministic within it: a deadline
  always wins, an invocation may make a limit *stricter* but may never weaken a locked
  client or data-policy constraint. The resolved value and its source must be printable,
  because a caller debugging why their model pin was ignored currently has no way to ask.
  Builders get `With(opts...)` so the fluent surface never needs a method per policy.
  *Verify (added):* a plan explanation prints effective value and source for every material
  setting; an invocation attempting to weaken a locked policy is rejected, not applied.
- [x] **A-005** — Renumber `Mode` and `Speed` so zero means unset (`ModeUnset`, `TierUnset`),
  and add `Opt[T]` for numerics that must be settable to zero. Today `Strict == Mode(0)` and
  `Smart == Speed(0)`, so every `mergeXOptions` guard of the form `if user.Mode != 0` makes
  `.Strict()` and `.Smart()` unrepresentable on roughly ten operations. Closes **F-01**.
  *Verify:* test asserts `Negotiating[T](c).Strict().Smart()` produces Strict and Smart, not
  the operation defaults. Breaking change — record in the release notes.
  **Done.** `ModeUnset` and `TierUnset` take zero; Strict/Transform/Creative and
  Smart/Fast/Quick shift up by one. The twenty `if user.Mode != 0` guards are now correct
  as written — the constants moved, the merges did not — and both new constants are exported
  from the root package and the fluent aliases so a caller can say "no opinion" explicitly.
  Unset resolves to a usable request at the point of use rather than moving the defect into
  the request builder: `GetTemperature`, `GetMaxTokens`, and `GetModel` all answer for it.
  *Verify:* `internal/ops/zero_value_options_test.go` — the merge for eight operations, each
  with deliberately non-Strict, non-Smart defaults so the caller's choice and the default
  disagree; the unset direction still taking the operation default; the zero values reporting
  themselves as unset; and unset resolving to a real temperature, token budget, and model.
  The witness is `TestTheOldZeroValuedChoiceIsReadAsSilence`, which passes a literal `0` —
  what Strict and Smart *were* — through the unchanged merge and asserts it is dropped. A
  full revert could not be compiled as a witness, because the tests name constants that did
  not exist before.
  **`Opt[T]` shipped separately** (`internal/types/opt.go`, 6 tests): the same defect for
  numerics, where there is no spare zero to renumber into. `MinConfidence`, `Threshold`,
  `MinSatisfaction`, and `ConflictThreshold` are still guarded with `> 0`, so a caller cannot
  set them to zero to mean "accept anything". Those fields change type when **OP-201** wires
  enforcement — the same lines, one commit — rather than being churned twice.
  **Breaking change**, for **DOC-002**: any caller comparing a `Mode` or `Speed` to an
  untyped `0`, or relying on `types.OpOptions{}` meaning Strict/Smart, changes behaviour. The
  new behaviour is the documented one: an unset field takes the operation's default.
- [ ] **A-006** — `Result[T]` + `Meta`: request and correlation IDs, provider, model, usage,
  cost with `Estimated` and `PricingSource`, attempts, repairs, strategy, elapsed. No
  `Confidence` field. Closes **D-09** structurally and makes **I-02** expressible.
  **Revised (ARC-13, ARC-24, API-11, TRU-22):** `Meta` is the first slice of the execution
  envelope, so give it the envelope's shape now rather than renaming it later. Four
  compartments that never mix: the value; model claims; deterministic verification results;
  runtime facts. Two fields the current list omits and the specification treats as v1
  gates — **requested versus delivered contract level**, so a degradation cannot be
  invisible (ARC-24), and a **usage/cost tree** rather than a flat total, because a figure
  that cannot attribute spend to a repair or an escalation cannot explain a bill (TRU-22).
  The envelope is constructed for every logical request including failures; `Run` may drop
  it, but must not skip building it, or the two return paths diverge.
- [ ] **A-007** — Error taxonomy: `ErrNoProvider`, `ErrAuth`, `ErrRateLimited`,
  `ErrTruncated`, `ErrDecode`, `ErrInvariant`, `ErrBudgetExceeded`, plus `APIError` and
  `InvariantError`. Closes **Gap-12**.
  *Verify:* every provider maps its failures onto the taxonomy; `errors.Is` table test.
  **Revised (PRD-08, EXE-06, Appendix B):** seven kinds do not separate the failures that
  need different recovery. The taxonomy the recovery machine needs distinguishes at least:
  configuration, authentication, permission, invalid request, rate limited, provider
  unavailable, timeout, canceled, context too large, output truncated, malformed output,
  schema violation, batch protocol violation, invariant violation, evidence violation,
  unsupported capability, policy violation, budget exceeded, circuit open, admission
  rejected, repair exhausted, review required, shutdown. The error carries structured
  context — operation, provider, model, request and attempt IDs, affected item IDs,
  `RetryAfter`, and an `Ambiguous` flag for a timeout that may have been served — and
  `Error()` prints sanitized metadata only. This is what **P-007** started; it is the
  control protocol every later milestone branches on, so widen it here rather than growing
  it a kind at a time.
  *Verify (added):* Appendix B's disposition table is a test — every kind asserts its
  retry, repair, fallback, and terminal behaviour.
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
  **Started** (`internal/ops/invariants.go`): `SameMultiset`, `SubsetOf`, `CoversExactlyOnce`,
  and `MemberOf` ship with 30 unit cases, built because **OP-105** and **OP-107** needed them.
  Values compare by canonical JSON, so an item echoed back with one field changed fails —
  which is the failure that happens, rather than the invented item people imagine.
  Errors report *how many* items went wrong, never which: these are the caller's records
  (**F-034**). Remaining: `AtMost`, `WithinLength`, `AtLeastConfidence`, `ExcludesValues`,
  `CategoryIn`, `OneOf`, `Satisfies`, and the `Invariant[In, Out]` type itself, which needs
  **A-001**'s descriptor to hang on.
  **Revised (TRU-25, ARC-11):** the shipped library is the floor, not the surface. A caller
  must be able to register their own normalizer, invariant, evidence check, and repair
  policy and have it run *inside* the kernel — so a domain rule participates in recovery,
  budgets, telemetry, provenance, and batch isolation instead of being a check the caller
  writes after `Run` returns and pays for twice.
  *Verify (added):* a caller-supplied invariant failing on the first attempt and passing on
  the second is visible in the envelope as a repair, with both attempts billed.
- [x] **A-010** — Repair loop: a decode or invariant failure feeds the error back and retries
  within the existing budget, aggregating usage across attempts into `Meta`. Closes **CF-01**.
  Depends on **A-009**.
  *Verify:* fake provider returns a violating result then a valid one; assert one repair, two
  attempts, and summed usage.
- [ ] **A-011** — Move errors and log records off raw payloads: no request body in an error
  string by default, no `Input` field on error types. Closes **X-03**.
  *Verify:* an error from a payload containing a marker string does not contain that marker.
  **Revised (PRD-05, PRD-09):** removing content from errors leaves debugging with nothing,
  which is why **P-007** kept the vendor's structured message. The specification's answer is
  a second channel: a caller-provided diagnostic sink that is off by default, bounded,
  redacted, retention-controlled, and referenced from the ordinary error by ID and content
  digest. Add the sink interface with this task so "no payload in errors" stops costing
  what it currently costs.
  *Verify (added):* with no sink configured nothing is captured; with one, the error carries
  a reference and the captured body is truncated and scrubbed of credentials.
- [ ] **A-012** — Port `Extract` to the core as the proof, keeping `Extracting[T]` working
  unchanged.
  *Verify:* existing extract tests pass untouched against the new path.
- [ ] **A-013** — `Run(ctx)` on the fluent builders. There is no way to honor cancellation
  otherwise, and the builder is where the context naturally arrives. Breaking change.
  *Verify:* `Extracting[T](x).Run(ctx)` cancels.
  **Revised (API-04, API-05, API-11):** three things ship with it. `RunResult(ctx)` beside
  `Run(ctx)`, executing identically and returning the envelope (API-11). Value receivers and
  copy-on-write option storage, so `base.Strict()` and `base.Fast()` are siblings rather
  than two views of one mutated builder (API-05). And a documented read-at-run contract for
  inputs holding maps, slices, or pointers, enforced by a race test — a deferred builder
  that observes a caller's later mutation is a bug nobody can see (TRU-16).
  *Verify (added):* branching a builder does not affect its sibling; mutating an input
  concurrently with `Run` is caught under `-race`.

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
  **Revised (EXE-03, EXE-12):** use stable invocation-local IDs, not positional indices. An
  index is only meaningful if the model returns every element in order; the failure this
  guards against — an omitted or reordered item — is exactly the case where it does not.
  Duplicate input values also make index reconciliation ambiguous, and equality-based
  matching cannot resolve it. Assign `i-000001`-style IDs at encode time, validate exact
  coverage on return, and reconstruct the caller's order in Go. This is the same protocol
  **PL-003** defines for batching, so define it once here and reuse it.
- [x] **OP-102** — `Choose`: attach `MemberOf`. Today the returned object is whatever the
  model emitted, never compared to the input list (`collection.go:112-131`). Closes **C-01**.
- [x] **OP-103** — `Filter`: attach `SubsetOf`, and fix the contradiction where the system
  prompt says "Include items that match" (line 201) while `KeepMatching: false` steers "Remove
  items that match" (line 168). Closes **C-02**.
- [x] **OP-104** — `Filter`: delete the single-object fallback that collapses a failed array
  parse into a one-element result with `err == nil` (`collection.go:236-245`). Closes **C-03**.
  **Done, with OP-103** (`09f93eb`). A malformed array used to collapse into a one-element
  result reported as success, so a filter over twenty items could return one and no caller
  could tell it from a genuine single match. `collection.go` now says so where the fallback
  used to be, and the parse failure is returned. Verified by `TestIntegrationFilterReturnsASubset`
  and the fault-injection suite's malformed-body arm.
- [x] **OP-105** — `Sort`: upgrade the count check to `SameMultiset` (`collection.go:371-380`).
  Equal length does not mean equal contents. Closes **C-04**.
  **Done** — `SameMultiset` is the first entry in the shared invariant library (**A-009**),
  in `internal/ops/invariants.go`, and `Sort` uses it. The count it replaces is satisfied by
  a model that returns one item twice and drops another, which corrupts a result quietly
  where a short answer at least breaks obviously. Values are compared by canonical JSON, so
  an item echoed back with a changed price fails too — the failure that actually happens.
  Found while here: the parse-failure branch still embedded the whole response in its
  `Reason` — **X-03** in a third place, after **OP-110** removed the two in `Filter`. Removed,
  and the cause is wrapped so `errors.Is` reaches it.
  *Verify:* `internal/ops/invariants_test.go` — 8 multiset cases including the duplicate-plus-drop
  that a count misses, plus a no-payload assertion. Integration:
  `TestIntegrationSortRefusesAResultThatIsNotAPermutation` (4 bodies at the public API) and
  `TestIntegrationSortAcceptsAPermutation`.
- [ ] **OP-106** — Promote `sortByScoringFallback` (`collection.go:385-453`) from fallback to
  the primary strategy above a size threshold: it scores items independently, keeps the
  caller's own objects, and sorts in Go, so items cannot be lost, duplicated, or edited — and
  `Stable` actually works. Run it with bounded concurrency and report `Meta.Strategy`.
  Closes **C-05**.
  *Verify:* a 200-item sort makes bounded concurrent calls, returns a permutation, and reports
  its strategy.
- [x] **OP-107** — `Cluster`: attach `CoversExactlyOnce`; fix `Size` being computed from raw
  indices including out-of-range ones while `Items` holds only valid ones (`cluster.go:310-327`),
  so the two disagree exactly when the model misbehaved. Closes **C-07**.
  **Done** — `CoversExactlyOnce` runs before anything is built, over the cluster indices and
  the outlier indices together (an item the model called an outlier is placed, not missing).
  A response with an item in two clusters, an item in none, or an index out of range is
  refused rather than half-applied: a caller iterating overlapping clusters processes an item
  twice, and one iterating an incomplete partition drops items silently.
  `Size` is now derived from the items actually placed rather than from the raw index count,
  so it cannot disagree with `Items` — which it did *exactly* when the model misbehaved, the
  one case a caller checking `Size` needed them to agree.
  *Verify:* `TestCoversExactlyOnce` (10 cases). Integration:
  `TestIntegrationClusterRequiresAPartition` (4 bodies) and
  `TestIntegrationClusterAcceptsAPartitionAndSizeAgrees`, which asserts `Size == len(Items)`
  and that the outlier comes back as the caller's own record.
- [x] **OP-108** — Token-estimate before dispatch and refuse or chunk oversized collections.
  Today every collection op marshals the whole slice into one prompt with no size guard, while
  output is capped at 1000–4000 tokens by tier, so `Sorting` a few hundred objects cannot
  physically return a complete result. Closes **C-06**, **Gap-10**. Depends on **A-007**
  (`ErrTruncated`) and **CF-004**.

## Analysis and validation

- [x] **OP-201** — Enforce `MinConfidence` in `Classify`, `Filter`, `Verify`, and `Derive`.
  It is enforced in exactly one operation today (`annotate.go:287`) and is prompt-only in the
  other four, with non-zero defaults (0.5, 0.7, 0.7) that make users believe a threshold is
  active. In `Classify` the value sits in a local variable one line above where it is copied
  to the result. Closes **A-02**.
  **Done for the three that can be enforced, and honest about the fourth.**
  `AtLeastConfidence` joins the invariant library and runs in `Classify` (default 0.5),
  `Verify` (0.7, on the overall confidence), and `Derive` (0.6, **per field first** and then
  overall — a derivation whose one weak field is the one the caller cares about should not
  pass because the average carried it). A result the model itself scored below the floor is
  now an error rather than a value.
  `Filter` is **not** enforced and its field says why: the response is the kept items and
  nothing else, so there is no per-item score to check the instruction against. Deleting it
  was the alternative; it stays because asking the model is a real if weaker effect, and what
  was wrong was the name implying a guarantee the protocol cannot support. If `Filter` ever
  returns per-item scores this becomes enforceable and should be enforced.
  The number is still a model claim and enforcing it does not make it a measurement — what it
  buys is that the option means what its name says.
  Found while here: `ClassifyOptions.MinConfidence` had **no setter at all**, so the only way
  to change the 0.5 default was a struct literal. `WithMinConfidence` added. **F-024**'s dead
  options check could not see it: it looks for a field with a setter and no reader, and this
  was the mirror image.
  *Verify:* `TestAtLeastConfidence` (8 cases). Integration: `confidence_integration_test.go`
  — Classify below, at, and above the floor; a zero floor accepting anything; and Verify's
  0.7 default refusing a verification the model scored at 0.3.
- [x] **OP-202** — Generalize `Classify`'s category-membership check (`analysis.go:178-196`)
  into the shared `CategoryIn` invariant. It is the only operation that validates the model's
  answer against the allowed set; it should be the template, not the exception.
  **Done** — `CategoryIn` is in `internal/ops/invariants.go` and `Classify` calls it. It
  returns the *canonical* spelling rather than a boolean, which is what makes it reusable: a
  caller comparing against their own constants needs "Billing" and "billing" to be one
  answer, and every future user of this invariant will want the same.
  Found while here: the parse-failure branch logged the whole response — **X-03**, in
  `Classify` and again in `Derive`. Both removed.
  *Verify:* `TestCategoryIn` (7 cases, including a near-miss and a sentence instead of a
  category). Integration: `TestIntegrationClassifyRefusesACategoryItWasNotOffered` (4 bodies)
  and `TestIntegrationClassifyNormalisesCase`.
- [x] **OP-203** — `Classify`: either give `ClassifyResult[C]` a real multi-label field or
  delete `MultiLabel` and `MaxCategories`. They change the prompt and cannot change the
  result. Closes **A-03**.
  **Done — the field exists now.** `Categories []C` carries every label the model assigned,
  and a single-label answer populates it too, so a caller reads one field regardless of the
  mode. `Category` stays the primary one (the first assigned) for callers who do not care
  about the distinction.
  Both options are enforced rather than requested: every entry is checked against the offered
  set through `CategoryIn`, duplicates collapse case-insensitively, and **MaxCategories** is a
  limit — a model that returns five labels when asked for at most two has not done what was
  asked, and silently keeping all five is the class of failure this list exists to remove.
  Stray *alternatives* outside the set are dropped with a warning rather than failing the
  call: the primary answer is the contract, and a bad suggestion beside a good answer is not
  worth discarding the answer over.
  *Verify:* `internal/ops/classify_labels_test.go` — multi-label reaching the result,
  single-label populating it, an unoffered label failing, MaxCategories at and above the
  limit, and duplicates collapsing.
- [x] **OP-204** — `Classify[T, C]`: the category is produced as a string and converted via a
  JSON round trip, so only string-kinded `C` survives and `Classify[Ticket, Priority]` with
  `type Priority int` compiles and fails at runtime. Constrain `C` or map explicitly.
  Closes **A-04**.
  **Done — constrained rather than mapped.** `C` is `~string`, so
  `Classify[Ticket, Priority]` with `type Priority int` is a compile error instead of code
  that builds cleanly and fails at run time with a JSON error naming a type the caller never
  wrote. The round trip is gone with it: the conversion is `C(canonical)`, which also means
  the *canonical* spelling reaches the caller rather than the model's casing.
  The constraint propagates through `ClassifyResult`, `ClassifyAlternative`,
  `ClassifyRequest`, and `Classifying`. **Breaking change** for **DOC-002** — for callers
  whose code does not work today.
  *Verify:* `TestNamedStringTypesAreSupported` asserts a named string type round-trips and
  keeps its type; the int case is checked by the compiler, which is the point.
- [x] **OP-205** — `Validate`: add a deterministic rule path ahead of the model call. Every
  rule in the README's own example ("email must be valid, country must be ISO alpha-2, age at
  least 18") is checkable in Go, and `Parse` already demonstrates the deterministic-first
  pattern in this package. Closes **A-05**.
  **Done** — `internal/ops/rules.go`. Rules a machine can decide are decided by the machine:
  `email`, `url`, `uuid`, `iso3166-alpha2`, `nonempty`, `min`/`max`, `minlen`/`maxlen`,
  `oneof`, and `regex`. An expression this layer does not understand — which is most of what
  the operation is for — passes through to the model untouched, and the prompt then carries
  only what the model is actually needed for.
  Two consequences worth stating. **When every rule is decidable there is no provider call at
  all**, so the README's own example (valid email, ISO country code, minimum age) costs
  nothing and is exact rather than probable. And **the deterministic findings come first and
  survive the model's answer**: a model that says `valid` does not get to overrule
  `mail.ParseAddress` by omission.
  Issue order is sorted, because Go randomizes map iteration and an issue list a caller
  cannot diff is a worse list. Messages name the field and the complaint, never the value
  (**X-03**).
  *Verify:* `internal/ops/rules_test.go` — 30 rule cases across nine rule kinds, including a
  rune-counted length limit; unrecognised expressions falling through; the no-provider-call
  path asserted by counting calls; a model saying "valid" failing to overrule the email
  check; the no-values-in-messages rule; and a rule naming a field the data lacks being an
  error rather than a pass.
- [ ] **OP-206** — Collapse `Validate` / `Verify` / `Audit` / `Critique` / `Score` onto one
  result shape. Five vocabularies for one operation shape (verdict + issues + summary) with
  different field names is the verb-explosion problem in its clearest form. Closes **A-08**.
  Coordinate with **PS-002**.
  **Revised (ARC-22, TRU-30):** the shared shape is `JudgmentResult` — subject, verdict,
  issues, evidence, and model-reported confidence kept out of the deterministic section —
  and it is one of four result families (judgment, transformation, collection, text) that
  every stable operation draws from. The naming half matters as much as the shape: a
  model-assisted review must not be spelled the same as a deterministic check. `Validate`
  becomes `ValidateDeterministically` and `ValidateHybrid`; `Verify`, `Audit`, and
  `Critique` become model-assisted review and say so in the name.

## Structured data

- [x] **OP-301** — `Project`: filter excluded fields out of the marshalled input
  deterministically and post-scan the output for excluded values, rather than interpolating
  `Exclude` into the prompt as a hint. Closes the mechanism half of **D-08**.
  *Verify:* an SSN present in the input never appears in the output, including when the model
  is instructed to echo it into an unrelated field.
- [x] **OP-302** — Compute `Lost`, `Inferred`, and `Mappings` in Go by diffing the source
  field set against the produced output, instead of accepting the model's self-report as an
  audit trail (`project.go:258-283`, and the same shape in `pivot.go`, `enrich.go`,
  `normalize.go`). Closes **D-09** behaviorally.
  **Revised (ARC-12, ARC-13):** the rule generalizes past these four operations —
  membership, ordering, field presence, input/output diff, cardinality, and reconstruction
  are computed in Go wherever Go can compute them, and a model stage is justified only when
  the judgment is irreducibly probabilistic. Where a model claim survives, it is labelled a
  claim and kept out of the verification section of the envelope (**A-006**).
  *Verify (added):* an operation that asks the model for something Go already knows fails
  review; the deterministic and model-claimed halves of a result are separately addressable.
  **Done for `Project`, the operation the review named.** `Lost` and `Inferred` are a set
  difference over the field names the source and the projection actually carry — an audit
  trail written by the thing being audited is not an audit trail, and a model that drops a
  field and does not mention it produced a projection claiming to have lost nothing, whose
  only evidence of the problem was the missing field itself.
  The model's account is kept beside it as `ModelClaimedLost` and `ModelClaimedInferred`,
  because where the two disagree the disagreement is the interesting part: a field the model
  says it inferred and the diff says came from the source is a different problem from the
  reverse.
  The diff reads what the value *contains* rather than what its type declares, since
  `omitempty` means a declared field can be absent and the question is what the caller
  received. `Mappings` stays the model's, because a mapping is a claim about intent that no
  diff can recover.
  *Verify:* `internal/ops/project_audit_test.go` — a model that silently drops two fields and
  invents one; a faithful projection reporting nothing, so the check is not merely
  pessimistic; and an `omitempty` field absent from the output not being reported as inferred.
  **Still open for `pivot.go`, `enrich.go`, and `normalize.go`**, which have the same shape.
  Filed as **OP-308** below rather than left inside a closed task.

- [x] **OP-303** — `NormalizeInput`: prefer JSON marshalling over `fmt.Stringer`
  (`utils.go:99-113`). Any type with a `String()` method — including `time.Time` — is sent as
  prose while the generated schema simultaneously tells the model the format is RFC3339.
  Closes **D-04**.
  **Done** — JSON first, `Stringer` as the fallback for types JSON cannot render. A
  `json.Marshaler` is a type's own statement about its wire form, and the wire form is what
  the generated schema describes; `String()` is a display format. The old order sent a
  timestamp as `2026-08-07 18:04:05.999 -0400 EDT` while the schema in the *same request*
  said RFC3339, so the model was asked to reconcile two descriptions of one value.
  *Verify:* `internal/ops/parse_detect_test.go` — a bare `time.Time`, a struct carrying one
  (the shape extraction actually sees), the Stringer fallback for an unmarshalable type, and
  strings and byte slices still passing through untouched. Found while writing it:
  `encoding/json` skips unexported fields, so a struct whose only channel is unexported
  marshals cleanly to `{}` and never reaches the fallback — the test says so, because the
  first version of it was testing nothing.
- [x] **OP-304** — `NormalizeBatch`: replace the serial per-item loop (`normalize.go:347-364`)
  with the batch processor plus bounded concurrency. Closes **D-14**, **Gap-08**.
  **Done** — a bounded worker pool, `NormalizeOptions.Concurrency` (default 4, modest because
  the limit that matters is the provider's rate limit rather than the machine's). Results stay
  in input order whichever item finishes first, and the reported failure is the
  lowest-indexed one, so the error does not depend on scheduling. An empty batch makes no
  calls.
  *Verify:* `internal/ops/normalize_batch_test.go` — a peak-concurrency test that parks every
  worker and asserts the peak is above one and at or below the configured bound; an ordering
  test where later items answer first; the failing index named in the error; and the empty
  case.
- [x] **OP-305** — `Parse`: match CSV headers on the `json` tag before the Go field name, and
  return an error (or a populated `Unmapped`) when no column maps. Today
  `capitalizeFirst(header)` is compared to Go field names, unmapped columns are skipped
  silently, and a single-struct target with zero matching headers returns a zero value with
  `err == nil`. Closes **D-11**.
- [x] **OP-306** — `Parse`: guard `reflect.TypeOf(result)` against a nil type so
  `Parse[any]` cannot panic (`parse.go:301`, `344`). Closes **D-12**.
  **Done** — both sites (`parseCSV` and `parseDelimited`). `reflect.TypeOf` on a nil
  interface returns nil and `Kind()` on a nil `Type` panics, so `Parse[any]` over CSV or
  delimited text took the process down. It now returns an error naming the target and saying
  what to write instead.
  *Verify:* `TestParseIntoAnyReportsRatherThanPanicking` — three input formats, each with a
  `recover` that fails the test if the panic comes back — plus
  `TestParseIntoAConcreteTypeStillWorks`.
- [x] **OP-307** — `Parse`: strengthen `detectFormat` (`parse.go:188-238`), where any input
  containing `": "` and no `{` is classified YAML and any input containing `|` is
  pipe-delimited, so ordinary prose is routed to the wrong parser and fails. Closes **D-13**.

## Text

  **Done** — both rules are structural now instead of substring searches.
  YAML requires **at least two** non-empty lines, each a `key: value` with a single-token key
  or a list item. A single line is not a document: "Note: the invoice was paid" has exactly
  the shape a one-pair YAML file has, and misreading prose is the worse error, so a lone pair
  now reports unknown — recorded in the test rather than left implicit.
  Delimited requires either a consistent field count across rows, or — for the single-record
  case the docs use, `Alice|28|Developer` — every field short and at most three words.
  "Use the staging server | the production one is locked" fails that; "John Smith|30" passes.
  *Verify:* `TestDetectFormatDoesNotRouteProseToAParser` (7 sentences, five of which were
  misrouted before) and `TestDetectFormatStillRecognisesRealDocuments` (11 cases), plus
  `TestFormatHintsWinOverDetection` — an explicit hint still overrides every structural rule,
  because the caller knows what they have.
- [ ] **OP-401** — Collapse the `X` / `XWithMetadata` twins into one operation returning
  `Result[T]` (`text.go:95/166`, `269/353`, `467/547`, `659/733`), each pair duplicating ~40
  identical lines. Closes **T-01**.
- [x] **OP-402** — Attach `WithinLength` so `MaxLength` and `TargetLength` are checked rather
  than requested, and compute `CompressionRatio` on runes rather than bytes. Closes **T-03**.
  **Done** — `WithinLength` and `MeasureLength` join the invariant library, with an explicit
  `LengthUnit` because "200" means nothing without one, and `Summarize` checks its
  `TargetLength` against them. The tolerance is 20% and deliberate: the prompt says *target*,
  and a summary asked for three sentences that returns four has done the job — failing the
  call would be a worse answer than the one it gave.
  `CompressionRatio` is computed on runes. In bytes, the same summary of the same text
  reported a number three times smaller for Japanese than for English, and that ratio is what
  a caller tunes `MaxCompression` against.
  *Verify:* `TestWithinLength` (13 cases across four units, including two that would pass or
  fail differently if characters meant bytes) and `TestMeasureLength` (12 cases).
- [x] **OP-403** — Replace byte slicing with rune-safe truncation in `complete.go:269-276`
  and `redact_llm.go:305-336`. Closes **T-06**. Depends on **TI-009**.
- [x] **OP-404** — `Complete` and `CompleteField`: drop the `provider llm.Provider` parameter
  that no other operation takes and that leaks `internal/llm` into the signature. Closes
  **T-04**.

## Redaction — rebuild

  **Done** — both signatures lose it, along with the `internal/llm` import it dragged into a
  public one. They were the only operations in the library taking a provider, so `Complete`
  was the single call a caller had to configure differently from every other; a caller who
  needs a specific provider installs it on the client, which is what the client is for.
  The `if provider != nil` fork inside `completeImpl` is gone with it, so there is one path
  rather than two.
  *Verify:* `TestCompleteUsesTheConfiguredProviderLikeEveryOtherOperation` and
  `TestCompleteFieldUsesTheConfiguredProvider`. **Breaking change** for **DOC-002**.
- [x] **OP-501** — Replace the field-name substring matcher (`redact.go:359-387`), whose
  sensitive list includes `"name"`, `"key"`, `"first"`, `"last"`, `"card"`, `"address"`, and
  `"full"`, so `Filename`, `Username`, `Keywords`, `LastUpdated`, and `CardCount` are all
  destroyed. Match on word boundaries and an explicit tag. Closes **T-07**.
- [x] **OP-502** — Replace the pattern set (`redact.go:406-438`). It misses undashed SSNs,
  every phone format except `###-###-####`, and unformatted 16-digit card numbers, while
  masking any two capitalized words, any nine-digit number, and every currency amount.
  Closes **T-08**.
  *Verify:* a labelled corpus of true positives and true negatives with per-category
  precision and recall floors asserted in CI.
- [x] **OP-503** — Implement `RedactWithResult`, which today returns an empty map and
  `err == nil` under a `// For now` comment (`redact.go:186-200`), so an audit reads as
  "nothing was redacted". Closes **T-09**.
- [x] **OP-504** — Replace `Redact[T](input T, opts ...interface{})` with typed options; the
  `default:` branch currently discards an unrecognized options struct silently. Closes **T-10**.
  **Done** — `Redact` and `RedactWithResult` take `...RedactOptions`. The public wrappers in
  `schemaflux.go` and the fluent aliases already had typed signatures, so this was contained
  to `internal/ops`: what changes is that passing the wrong struct is now a compile error
  instead of a redaction pass configured with none of the caller's settings and no error.
  *Verify:* `TestRedactOptionsResolution` — the compiler covers the removed branch; the test
  covers what remains, that no options means the documented defaults and options given are the
  ones used.
- [x] **OP-505** — Remove or replace jumble redaction. `JumbleSeed` defaults to zero, so the
  RNG is seeded with `len(input)` — a value readable from the output — and `jumbleBasic` is a
  Fisher–Yates shuffle of the same runes, making the transform an **invertible** permutation.
  Closes **T-11**. Addresses **Gap-14**.
  *Verify:* a test asserting the output is not a permutation of the input.
- [x] **OP-506** — `JumbleSmart` is documented as preserving vowel/consonant structure and
  calls `jumbleBasic` (`redact.go:535-539`). Implement or delete. Closes **T-12**.
  **Done, implemented rather than deleted.** Each character is replaced by a random one of
  the same class — vowel for vowel, consonant for consonant, digit for digit, case preserved,
  punctuation and spacing untouched — so the result reads like the same kind of thing.
  Substitution rather than permutation was the deliberate choice: a shuffle keeps the exact
  multiset of characters, so the original is a rearrangement away and the character counts
  are a fingerprint. That is **OP-505**'s lesson about the seed, applied to the transform.
  *Verify:* `TestJumbleSmartPreservesTheStructureItPromises` walks every character of four
  inputs and checks the class survives; `TestJumbleSmartPreservesCase`; and
  `TestJumbleSmartIsNotAPermutation`, which fails if it ever becomes a shuffle again.
  Still documented as obfuscation, not anonymisation — length and layout survive on purpose,
  which is what "preserve some structure" means.
- [x] **OP-507** — `RedactLLM`: locate spans by matching the model's reported substring with
  `strings.Index` rather than trusting model-reported character offsets, which are
  bounds-checked only (`redact_llm.go:232-269`) and compared against `len()` in bytes while
  models count characters. Reject a span whose sliced text does not match the reported
  original. Closes **T-13**.
  **Revised (TRU-02):** a matched substring is a weak evidence reference; make it the real
  one. A span is `{SourceID, StartByte, EndByte, SourceDigest}` validated in bounds against
  the digest of the source it claims to come from, which is the same structure the evidence
  contract needs in **TC-002**. Building it here means redaction is the first consumer of
  evidence rather than a parallel mechanism that has to be replaced later.
  **Done** — the model returns the substring and the library finds it; the offsets are a hint
  used only to choose between repeated occurrences. A bounds check cannot tell a correct
  offset from a plausible wrong one, so a span that was in range but a few characters off
  masked the wrong part of the document and reported the wrong thing as redacted — worse than
  not redacting, because it looks done. Locating by text also removes the character-versus-byte
  mismatch by construction: the model counts runes, Go slices bytes, and they agree only for
  ASCII.
  A span whose text is not in the document is dropped, so a hallucinated finding removes
  nothing. A span with **no** text is dropped too — there is nothing to verify it against, and
  accepting it would keep the old behaviour alive under a new name. The prompt asks for the
  substring accordingly.
  Found while writing the tests: "first occurrence at or after the hint" silently skipped a
  correct occurrence starting one byte *before* the hint, collapsing two spans onto one.
  `locateSpan` picks the nearest occurrence in either direction, and only among those not
  already claimed by an earlier span.
  *Verify:* `internal/ops/redact_spans_test.go` — five offset shapes (correct, off by five,
  past the value, absent, wildly out of range) all landing on the right text; hallucinated
  and textless spans dropped; repeated values mapping to distinct occurrences; a non-ASCII
  document where byte and character offsets diverge; and the redacted output containing none
  of the values while keeping the invoice number it was not asked about.
  **Not closed by this:** whether a span *is* what the model called it. A span labelled "ssn"
  over an order number is still applied — that is classification, and this layer verifies
  location.
- [x] **OP-508** — Lift the redaction not-production-ready markers from **F-021** once
  OP-501–OP-507 are green.

---

# M06 — Make the types load-bearing

  **Done** — the last marker was on `RedactWithResult`, describing an empty-map return that
  **OP-503** had already replaced. The README markers went with **OP-501**–**OP-503**.
  What replaces them is a description of what the mechanism does and does not do, because
  "it works now" is not useful to somebody evaluating this for compliance use: field names
  matched as whole names, cards validated with Luhn, a bare nine-digit run deliberately not
  matched, jumbling documented as obfuscation, and RedactLLM verifying location rather than
  classification.
  `internal/ops/disclosure_test.go` is what holds those descriptions in place, and its
  expected phrases were updated with the mechanism rather than around it — the note in the
  test says which tasks changed what.
- [x] **S-001** — Recurse `GenerateTypeSchema` into nested structs and slices, with a depth
  cap and cycle detection. Today the struct branch describes each field with
  `GetTypeDescription`, which returns `main.Person` for a struct field and `[]main.OrderItem`
  for a slice field — so the model is told a Go identifier it has never seen for exactly the
  nested payloads extraction is used on. `Project`, `Pivot`, `Enrich`, and `Derive` inherit
  this. Closes **D-01**.
  *Verify:* golden schema test on a three-level nested struct with a slice of structs.
- [ ] **S-002** — Emit real JSON Schema from the same reflection pass, and keep a compact
  rendering for the prompt path (full JSON Schema is token-expensive).
  *Verify:* the emitted schema validates against a JSON Schema meta-schema.
  **Revised (TRU-05):** the schema needs an identity, not just a body:
  `{Name, Version, Hash, Dialect, TypePolicy}`. Everything downstream keys on it — prompt
  bytes, cache keys (**P-009**), replay fixtures, stored results, semantic baselines — and
  a schema that changes without changing its hash silently invalidates all four. An
  anonymous type gets a deterministic content-derived name and a warning that persisting
  results against one is a bad idea.
- [x] **S-003** — Express requiredness explicitly rather than inferring it from the absence of
  `omitempty` (`utils.go:42-47`), which is a serialization directive, not a validation one.
  Closes **D-03**.
  **Revised (TRU-06):** requiredness is only half of it — Go's zero value cannot say whether
  a field was absent, explicitly null, or explicitly `0`. Strict mode must evaluate
  requiredness against *presence*, which means shipping at least one first-class presence
  strategy (`Optional[T]{Value, Present, Null}`, pointer fields, or a sidecar
  `FieldPresence` map keyed by JSON pointer) and documenting the others.
  *Verify (added):* an explicit `0`, `false`, or `""` satisfies a required field; an absent
  one does not; the two are distinguishable in the result.
  **Done for the requiredness half; the presence half stays open.**
  `FieldRequiredness` resolves in three steps: an explicit `schemaflux:"required"` or
  `schemaflux:"optional"` tag; then the type, because a pointer can be nil and that is how Go
  spells "may be absent"; then `omitempty`, kept as the legacy inference so no existing type
  changes behaviour.
  The conflation it removes cut both ways: a caller who wrote `json:"middle_name,omitempty"`
  to keep their JSON tidy had silently made the field optional to `Strict`, and a caller who
  wanted an optional field and did not know the convention got a required one.
  **All three descriptions of the contract read the same resolver** — the prose schema in the
  prompt, the JSON Schema sent to the provider, and the validation applied to the answer.
  Three descriptions that can disagree is three chances to.
  *Verify:* `internal/ops/requiredness_test.go` — 9 resolution cases including tag-beats-
  omitempty and tag-beats-pointer; `TestTheSchemaAndTheValidatorAgree`, which checks the
  prompt schema and the validator field for field; and `TestTheJSONSchemaAgreesAboutOptionality`.
  **Not closed by this:** distinguishing *missing* from *explicitly null* from *zero*. The
  validator still reads "populated" as "not the zero value", which cannot tell a model that
  returned 0 because it could not determine a value from one that determined 0. That is the
  Revised note's `Optional[T]` work — the type exists (**A-005**) and threading it through
  the decoder is where it lands.

- [x] **S-004** — Make `Strict()` mean something: with strict schema enforcement plus declared
  requiredness, replace `ValidateExtractedData` — which takes a `threshold` and never reads
  it, checking only non-nil (`utils.go:180-198`). Closes **D-05**.
  *Verify:* a response missing a required field fails in Strict and repairs in Transform.
- [x] **S-005** — Replace `inferResponseFormat` (`llm_helper.go:299-317`), which decides JSON
  mode by searching the concatenated system **and user** prompt for phrases like
  `"json object"` — making response format data-dependent and rewording a prompt enough to
  silently disable enforcement. Use the `Format` field on the descriptor. Closes **I-04**.
  *Verify:* a user input containing the phrase "json object" does not change the request's
  response format.
- [x] **S-006** — Reconsider `Creative` mode on `Extract`, which instructs "Generate plausible
  values for missing fields / Prioritize completeness over strict accuracy"
  (`utils.go:141-146`) on an operation whose purpose is faithfulness. Rename it for what it
  does or remove it from `Extracting`. Closes **D-07**.
  **Renamed in meaning rather than removed.** The prompt said "Generate plausible values for
  missing fields" and "Prioritize completeness over strict accuracy" — an instruction to
  fabricate, on the operation whose entire purpose is faithfulness to a source, reachable by
  a caller who thought they were asking for flexibility.
  Creative now means the most permissive *reading* of the source: interpret loose wording
  generously, use context elsewhere in the input, accept clearly implied values — and leave a
  field null when the input does not support one, stated explicitly, because silence on the
  point is how a model fills it in anyway. Inference stays; invention goes.
  Removing the mode was the alternative and would have been worse: callers reaching for it
  want the permissive reading, and deleting it sends them to `Transform` with no way to ask.
  *Verify:* `internal/ops/prompt_mode_test.go` — the fabrication phrases are gone, the
  refusal is present, the three modes still differ from each other, and `ModeUnset` produces a
  usable prompt without inheriting any of it.
- [ ] **S-007** — Make `strengthenSystemPrompt` opt-out and measure whether it still helps.
  It prepends a fixed instruction block to every request, JSON or not, billed on every call.
  Closes **I-18**.
  *Verify:* an A/B over the golden corpus recording the measured difference; if none, delete.

## Added from `to-production.md`

- [ ] **S-008** — Exact decoding in strict mode: reject unknown properties, duplicate keys,
  and trailing data after the top-level value; enforce maximum nesting depth, string size,
  array length, and total decoded bytes; report the smallest failing JSON pointer.
  `encoding/json` discards unknown fields, which is how a hallucinated field becomes
  invisible — **F-035** caught the "answer about something else" case with a weak
  field-name rule, but overproduction inside a well-shaped answer still passes. Closes
  **TRU-08**; completes what F-035 started.
  *Verify:* a response carrying one extra field fails in strict mode and names it; a
  deliberately deep or oversized body is refused before allocation.
- [ ] **S-009** — Exact numeric handling. `float64` is the wrong default for money,
  identifiers, and large integers: 16-digit account numbers lose precision, leading zeros
  vanish, and a postal code becomes a number. Support `json.Number` for deferred parsing,
  registered decimal types, integer bounds with overflow detection, string schemas for
  identifiers that must keep their shape, and registered encoders for `time.Time`,
  `time.Duration`, and UUIDs. A conversion that would lose information is
  `ErrSchemaViolation`, not a silent zero. Closes **TRU-07**.
  *Verify:* a 19-digit identifier and a two-decimal currency amount survive a round trip
  byte-exact; a value exceeding the declared range is refused.
- [ ] **S-010** — Type support matrix, enforced at preflight rather than discovered at
  runtime. Four levels: full (structs, slices, arrays, pointers, scalars, enums, registered
  time/decimal types), restricted (string-keyed maps, bounded recursion, embedded fields,
  custom marshalers — documented limits or a registered adapter), opaque (blob wrappers,
  no field-level evidence), rejected (unbounded cycles, non-string map keys, unconstrained
  `any`, funcs, channels, unsafe pointers). A rejected shape fails before it costs a call.
  Closes **TRU-09**. Depends on **S-001**, which already caps depth and names cycles.
  *Verify:* one case per level; a rejected type produces an error naming the field and the
  reason, and makes no provider call.
- [ ] **S-011** — Schema evolution rules and migrations. Adding an optional field is
  decode-compatible but changes prompt behaviour and cache identity; adding a required
  field, changing a type, enum, precision, or evidence requirement is a new contract
  version; renaming a JSON field is breaking without an explicit alias. Stored results keep
  the schema version and hash that produced them. Closes **TRU-05** behaviourally;
  **S-002** supplies the identity this operates on.
  *Verify:* a compatibility test per rule; a stored result decoded under a newer schema
  reports the version it was written with rather than silently re-interpreting.

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
- [x] **PR-003** — Bound `costHistory` (a package-level slice appended per call and never
  evicted, `pricing.go:301`) with a ring buffer and a request-ID index; `GetRequestCost`,
  `GetCostSummary`, and `GetTotalCost` all linear-scan it under a lock. Closes **I-10**.
  *Verify:* memory is flat across 100k tracked calls; lookup is O(1).
  **Done** — a ring buffer of `DefaultCostHistoryLimit` (10,000) records with
  `SetCostHistoryLimit` to change it, plus `costIndex` mapping request ID to slot so
  `GetRequestCost` is a lookup rather than a scan under a lock. An evicted record's index
  entry is removed with it — without that, a lookup returns the slot its replacement now
  occupies, which is somebody else's request reported as yours. Every other reader
  (`GetTotalCost`, `GetCostSummary`, `GetCostBreakdown`, `GetRequestCosts`, `ExportCostReport`)
  walks the ring oldest-first through one helper, so order is preserved.
  *Verify:* `pricing/history_budget_test.go` — 5,000 records retained as 100 with the backing
  slice not growing; eviction oldest-first; evicted IDs no longer resolving; a retried request
  ID resolving to its latest record; and the aggregates agreeing with the retained history
  rather than silently reporting a total for records that no longer exist.
- [x] **PR-004** — Separate history reset from budget configuration: `ResetCostTracking`
  currently nulls `budgetLimits` and `budgetCallback` too, so clearing history silently
  disables budget alerting. Closes the side-effect half of **I-10**.
  **Done** — `ResetCostTracking` clears history and totals; the new `ResetBudget` clears
  limits, callback, and notification state. Clearing history is something tests do between
  cases and services do on a schedule, and it was silently switching off spend alerting.
  The notification state goes with the *totals*, not the limits: once spend is back to zero, a
  later crossing is a new edge and has to alert again.
  *Verify:* `TestResetCostTrackingKeepsTheBudget`, `TestResetBudgetKeepsTheHistory`.
- [x] **PR-005** — Make budgets edge-triggered and optionally enforcing. `SetBudget` fires its
  callback on every request once spend passes 80% of a limit, with no debounce and no state,
  and nothing is ever blocked. Closes **I-11**.
  *Verify:* one notification per threshold crossing; enforcing mode returns
  `ErrBudgetExceeded` before the call is made.
  **Revised (PRD-06, EXE-17):** a process-wide spend total is the wrong unit. The budget is
  per logical request and hierarchical — provider calls, input/output/total tokens, cost,
  elapsed time, chunk attempts, item attempts, escalations — and retry, repair, split,
  fallback, escalation, and review all draw from the same ledger, with children unable to
  exceed a parent's ceiling. Without this, every recovery mechanism M08 adds multiplies the
  worst case and nothing bounds the product.
  *Verify (added):* a request whose repairs would exceed its call ceiling stops at the
  ceiling and returns validated partials; the envelope's attempt tree sums to the ledger.
  **Done for the review's I-11**; the specification's hierarchical per-request budget stays
  open as the Revised note above describes, and belongs with **M12**.
  Alerts are edge-triggered at 80% and 100%, keyed by *period instance* so tomorrow's budget
  alerts again rather than staying silent because yesterday's fired. Before this, twenty
  requests past the threshold meant twenty callbacks — which is how an alert becomes a filter
  rule and then becomes nothing.
  Enforcement is opt-in via `SetBudgetEnforcement(true)`, and `CallLLM` asks `CheckBudget()`
  **before** building the request, so an exhausted budget refuses rather than reports. The
  default is off: budgets have been advisory in this library since it shipped, and quietly
  starting to refuse calls mid-run would be a worse surprise than the noise it replaces.
  *Verify:* `pricing/history_budget_test.go` — one callback for twenty crossings, a second for
  the limit itself, none after; enforcement off by default; zero meaning unlimited. Integration:
  `internal/ops/budget_enforcement_test.go` asserts the provider is called **zero** times when
  the budget is exhausted, which is the whole point of checking before rather than after.
- [x] **PR-006** — Delete the duplicated `MatchesFilters` / `matchesFilters` pair
  (`pricing.go:451`, `499`), one of which is exported by accident. Closes **I-17**.

## Middleware

  **Done** — `MatchesFilters` had no caller inside the module or outside it; the comment
  claiming it was "exported for testing" was the only thing keeping it. Deleted.
- [ ] **MW-001** — `Handler` / `Middleware` chain applied at client construction. Closes
  **Gap-11**.
- [ ] **MW-002** — `mw.RateLimit`. Closes part of **Gap-09**.
- [ ] **MW-003** — `mw.Retry` wrapping **A-008**.
- [ ] **MW-004** — `mw.Cache`: response cache keyed on a hash of model, tier, mode, prompt
  bytes, and schema, so exact-duplicate calls cost zero. Closes **Gap-07**.
  **Revised (TRU-10):** the key must carry the complete semantic execution identity —
  operation and prompt versions, schema hash, normalized options, input digest, provider and
  *resolved* model, temperature/seed, required contract level, data-policy partition, and
  decoder version — or a cache hit answers a question nobody asked. Exact result caching is
  opt-in and partitioned by tenant and data policy; caching on *similar* inputs stays off by
  default and is not a v1 feature. A cache hit appears in provenance and in cost accounting
  as a hit, never as a zero-cost call.
- [ ] **MW-005** — `mw.Budget` enforcing **PR-005**. Closes **CF-08**.
- [ ] **MW-006** — `mw.RedactEgress` so payloads can be scrubbed before they leave.
- [ ] **MW-007** — `mw.Metrics` exporting to OpenTelemetry, already a direct dependency.
  Closes the export half of the metrics gap.
  **Revised (ARC-17, ARC-18):** the direction is wrong as stated. Core defines small observer
  interfaces and emits through them; the OpenTelemetry adapter lives in a separate package
  (`telemetry/otel`) and uses the *host's* provider. The library never initializes a global
  SDK, an exporter, an endpoint, or a sampler, and never owns their shutdown — a library
  that configures the host's telemetry stack cannot be embedded twice. That also means OTel
  leaves the core's dependency set. See **OB-001**.
- [ ] **MW-008** — `mw.Fallback` for provider failover. Closes the rest of **Gap-09**.
  **Revised (TRU-12, ARC-24):** failover is not free substitution. A fallback route must
  meet the same minimum capabilities and the same data policy as the route it replaces — a
  private-region failure may not fall back to a public provider — and it may not silently
  downgrade the requested contract. A named degradation (native schema → JSON mode plus
  deterministic validation) is allowed only by explicit policy and is recorded as delivered.
  A fallback's own failure is classified on its own terms, not hidden behind the original.
  Depends on **CP-001** for the capability data this decision needs.

## Prompt caching

- [x] **CA-001** — Sort map keys before rendering them into prompts. Go randomizes map
  iteration order, so `SchemaHints` (`core.go:73`), `FieldRules` (`core.go:80`,
  `extended.go:232`), `Mappings` (`project.go:171`), and `CategoryDescriptions`
  (`analysis.go:95`) produce different prompt bytes on every call — defeating any prefix cache
  and making runs irreproducible. Addresses **Gap-04**.
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
  implemented in terms of it. Addresses **Gap-01**.
  **Revised (TRU-13, TRU-14):** three streaming modes, kept apart because they promise
  different things: raw text, provider events, and *validated items* — a token stream is not
  a partial typed result, and presenting one as the other is the same fail-open the whole
  list is about. The caller-facing iterator owns a bounded buffer, is cancelable, and
  documents whether items arrive in input or completion order; stopping iteration cancels
  the remaining work unless the caller explicitly detaches it.
- [ ] **ST-003** — Remove the hardcoded output ceilings (4000/2000/1000 by tier,
  `config.go:206-218`) in favor of a per-call option, and make truncation return
  `ErrTruncated` rather than surfacing as a parse error. Closes **I-09**.

## Infrastructure papercuts

- [x] **IN-001** — Guard `defaultClient` behind its mutex in `GetDefaultClient`, `GetLogger`,
  `ConfigureLogging`, and `SetLogLevel`, and protect `ops.defaultProvider` and
  `ops.customLLMCaller`. Closes **I-14**. Verified by **TI-008**.
  **Done** — `defaultClient`'s accessors were already locked by **F-031**; this closes the
  other half, `internal/ops`. `defaultProvider` and `customLLMCaller` are written by every
  client construction and every `schemafluxtest.Install` and read by every operation, and both
  were unguarded. They sit behind `providerMu` now, read together through `currentHooks()` so
  an operation cannot see the caller from one installation and the provider from the next —
  `Batch()` had its own unguarded read and uses it too.
  *Verify:* `internal/ops/provider_globals_test.go` — 8 writers swapping providers against 8
  readers dispatching, plus a group swapping the test caller, plus the same for `Batch()`, and
  the no-provider path under concurrency. **Caveat, unchanged from F-031:** `-race` does not
  run on windows/arm64, so locally these exercise the interleaving without the detector.
  **CI-002** now runs them under it on three platforms.
  **This is a stopgap and the file says so.** A lock makes concurrent access safe; it does not
  make two clients possible, because there is still one variable. That is **IN-004**.
- [x] **IN-002** — Delete the unused `Client.openaiClient` field. Closes **I-15**.
  **Done** — the field was written in `NewClient` and never read. Deleted, along with the
  `go-openai` import it was the last user of in this file.
- [x] **IN-003** — Make `WithDebug(false)` restore the prior log level. Closes **I-16**.
  **Done** — `WithDebug(true)` records the level it is raising from and `WithDebug(false)`
  puts it back. The option had exactly one direction before: a caller who turned debug on for
  one operation kept debug logging, and everything debug records, for the rest of the process.
  Two enables remember only the original level, or the restore would be a no-op.
  Needed a `telemetry.Logger.Level()` getter, which did not exist — `SetLevel` had no
  counterpart, so nothing could put a level back.
  *Verify:* `client_debug_test.go` — 6 tests: restore from info, warn, and error; repeated
  enables; disabling something never enabled; the flag itself; a level round trip; and a nil
  logger reporting info rather than panicking.
- [ ] **IN-004** — Decide `Client`'s fate: it has no method that runs an operation, and
  `WithProviderConfig` / `WithProviderInstance` mutate a package global as a side effect, so
  constructing a second client silently reconfigures the first. Either give it real operation
  methods or delete it and document the global model honestly. Closes **I-06**.
  **Revised — the choice is made (ARC-01, ARC-02, ARC-19, ADR-003, ADR-020):** the client
  becomes the real execution boundary and the package globals leave the core. Documenting
  the global model honestly is no longer an option: it is the thing that makes two clients
  impossible, makes the library unusable in a multi-tenant process, and makes **TI-002**
  and **TI-008** unverifiable. Concretely — the client owns an immutable snapshot of
  provider registry, route policy, budgets, scheduler, observer, and cache policy;
  `WithX` after construction is not part of the stable API (a new snapshot is built and
  swapped by the application); `ops.defaultProvider`, `ops.customLLMCaller`, and
  `defaultClient` move behind a compatibility adapter that core does not import; `.env`
  loading and any other process-level mutation move to bootstrap or to a `quick` package
  for scripts and examples. `IN-001`'s mutexes are the stopgap, not the destination.
  *Verify:* two clients with different providers, budgets, and policies run concurrently
  and independently; core compiles with no reference to a package-level provider.

---

# M08 — Control flow as combinators

Each takes an `Op` and returns an `Op`. Build after M05, because `Vote` needs comparable
results, `Escalate` needs a failure signal that is not "the model said something odd," and
`MapReduce` needs invariants to validate the merge.

- [ ] **CF-001** — `flux.Escalate(op, from, to)`. Closes **CF-02**.
- [ ] **CF-002** — `flux.Vote(op, n, rule)` — and the first honest confidence number in the
  library, derived from sample agreement. Closes **CF-03**.
  **Revised (TRU-27):** agreement is a policy, not a proof — correlated models share
  hallucinations, so three samples agreeing on an invented figure is three samples wrong.
  Reconciliation is pluggable (exact agreement, field-level voting, deterministic validation
  then selection, evidence-weighted comparison, adjudicator model) and **must be able to
  abstain**, returning `ErrReviewRequired` rather than the majority answer. Evidence and
  invariants still apply to the winner; a vote does not substitute for them.
- [ ] **CF-003** — `flux.Until(op, pred, max)`. Closes **CF-05**.
- [x] **CF-004** — `flux.MapReduce(op, chunk, merge)` with bounded concurrency. Closes
  **CF-04**; unblocks **OP-108**. **Done `debaf6e`.**
- [ ] **CF-005** — `flux.Checkpoint(store, runID)`. Closes **CF-06**, and replaces the
  declared-but-unimplemented `PipelineOptions.SaveProgress`.
- [ ] **CF-006** — `flux.Approve(gate)`. Closes **CF-07**; required before **F-025**'s shell
  tool may be enabled.
  **Revised (TRU-26):** approval is one use of a more general terminal outcome. Automated
  recovery stops — with `ErrReviewRequired` and a `ReviewPacket{Candidate, InputRefs,
  Evidence, FailedChecks, Attempts, SuggestedAction}` — when evidence is contradictory,
  an invariant survives its budgeted regenerations, eligible providers disagree materially,
  or policy requires a human. That is a successful safety outcome, not a failure, and it is
  the alternative to looping the model until it says something acceptable. The library
  supplies the structure and the callback; it does not build an approval workflow.
- [ ] **CF-007** — `flux.Fallback(a, b)`.
- [ ] **CF-008** — Retire or reimplement `Decide`, `Guard`, `Match`, and `Pipeline` on the
  combinators. `Guard` currently issues an unannounced LLM call with a hardcoded 2-second
  timeout and no options (`procedural.go:143-180`). Closes **P-03**, **P-10**. Addresses **CF-09**.
- [x] **CF-009** — Bounded-concurrency primitive used by OP-106, OP-304, and CF-004. Today
  only `batch.go:136` and `pipeline.go:232` start goroutines. Closes **Gap-08**.
  **Done `debaf6e`** — shipped as `MapReduceOptions.Concurrency`. Kept inside `MapReduce`
  rather than extracted: a bare semaphore helper has no home of its own until OP-106 and
  OP-304 need it, and the ordering guarantee is the part that was actually missing.

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
  **Revised (ARC-03, PRD-16):** the specification supplies the catalogue, so this stops
  being an open question and becomes a migration. Eight stable categories — extraction,
  transformation, generation, classification, selection, ordering, review, validation —
  and everything else (`Negotiate`, `Arbitrate`, `Predict`, `Resolve`, `Interpolate`,
  `Synthesize`, `Assemble`, …) moves to `recipes` or an experimental namespace. Their
  existence was never the problem; giving them the same compatibility and correctness
  promise as `Extract` without a defined semantic contract is. Ship stability tiers
  (stable / beta / experimental) in the package layout, the doc comments, and the
  compatibility policy at the same time, so "experimental" is a marker rather than a claim.
  *Verify:* every operation carries a tier; a stable one without a documented semantic
  contract, batch algebra, and invariants fails a check.
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
  **Revised (ARC-20):** either outcome closes it, and the cheaper one is legitimate. Build a
  session/message abstraction, **or** rename these operations for the single-shot work they
  actually do. What is not allowed is keeping a conversational name on a one-round-trip
  implementation. If a session lands, it is a sequential stateful execution shape with
  transcript invariants — not generic MDSP (Appendix C).
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
  **Revised — decided (API-12, §1.3 non-goals):** `to-production.md` names a general-purpose
  workflow/orchestration language an explicit non-goal for v1 and forbids builders growing
  a branching DSL. So the two documents are out of scope, not unscheduled: move them out of
  `plans/` into an ideas or archive location with a line saying which decision retired them.
  While there: `to-production.md` references nine figures under
  `docs/engineering/plans/figures/` that do not exist, so the document renders with nine
  broken images. Generate them or drop the references. `plans/` should also state its own
  ordering — `to-production.md` is the target architecture; `REFACTOR_PLAN.md`,
  `GO_TYPE_NATIVE_PRIMITIVES.md`, `NEW_PRIMITIVES_ANALYSIS.md`, and `PRACTICAL_LLM_OPS.md`
  predate it and need the same in-scope / superseded ruling.
- [ ] **PS-009** — Reconcile `AGENTS.md` with this repository. It is CodeFlux's file, copied
  verbatim: it declares itself scoped to Codeflux, mandates `docs/plan.md`, `CHANGELOG`,
  `DEVLOG`, `.claude/`, `.artifacts/`, `cmd/codeflux-dev`, atoms, SQLite migrations, a
  frontend, and a `dev`-branch model that does not exist here — while forbidding the
  `git add -A` and direct `main` pushes already used in this repository's history.
  **Revised (PRD-25):** the same task covers the rest of the adoption contract, which is
  missing beside it: `LICENSE`, `SECURITY.md` with a threat model and supported-version
  policy (**SEC-001**), `CONTRIBUTING.md`, issue templates that ask for sanitized envelope
  metadata rather than raw payloads, an ADR directory to hold the twenty decisions
  `to-production.md` records, and a generated provider support matrix (**CP-003**).

---

# M10 — Release gates

- [x] **CI-001** — Run `go build`, `go vet`, `gofmt -l`, and the full suite on every push.
  **Revised (PRD-01, PRD-17):** the required gate is wider, and each addition catches a
  class the others miss: `go test -race`, `go test -shuffle=on -count=10` (order-dependent
  tests and the package globals they hide), `staticcheck`, `govulncheck`, a fuzz smoke
  corpus (**SEC-003**), an examples-compile step, a generated-artifacts-are-fresh check
  (schemas and golden prompts), and a clean-working-tree assertion. The matrix is the
  platform contract, not a convenience: supported Go versions across Linux, macOS, and
  Windows on amd64 and arm64 — published as a support matrix, since this repository's own
  history contains a defect that only reproduces where `-race` cannot run.
  **Done** — `.github/workflows/ci.yml`. Five jobs: `test` (build, test, `-race` off Windows,
  `-shuffle=on -count=10`, vet) across ubuntu / ubuntu-arm / macos / windows on `stable` and
  `oldstable`; `quality` (gofmt, staticcheck, govulncheck, examples build, clean-tree check);
  `secrets`; `traceability`; and the smarttodo module. `SCHEMAFLUX_LIVE_TESTS` is never set,
  so **B-04** still holds and no job can bill the operator.
  Every step was run locally before the file was written, and three of them were red:
  **staticcheck reported 23 findings**, **govulncheck reported 6 reachable vulnerabilities**,
  and **`examples/smarttodo` did not build at all** — its `go.sum` never gained the
  `godotenv` entry that B-03 added to the root module, so the nested module had been broken
  since that commit and nothing noticed. All three are fixed in this commit; see below.
  *Verify:* `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `staticcheck ./...`
  (clean), `go test -shuffle=on -count=10 ./...` (13 packages, no failures),
  `go build ./examples/...`, and the smarttodo module's build and test.
- [x] **CI-002** — Add a Linux AMD64 job for `go test -race`, which cannot run on the current
  Windows/arm64 machine. Unblocks **TI-008**.
  **Done** — the `test` job runs `-race` on every runner except Windows, so the detector now
  sees this code on linux/amd64, linux/arm64, and darwin/arm64. **TI-008** is unblocked but
  not closed: the isolation half of it is a test nobody has written yet, and a green race
  detector says nothing about two clients sharing a package global.
- [x] **CI-003** — Normalize line endings (`.gitattributes`) so `gofmt -l` stops reporting
  ~180 files that differ only by CRLF, which currently masks real formatting drift.
  **Done** — and the masking was real, not theoretical. `gofmt -l` reported **179** files;
  converting a copy of each to LF and re-checking showed **162 of them differed by nothing
  but the line terminator, and 17 carried genuine drift** — eight numbered examples,
  `internal/tools/exec.go`, `internal/types/errors.go`, and six files under
  `examples/smarttodo`. Those 17 are formatted now and `gofmt -l .` returns nothing, so the
  check can become a gate (**CI-001**). `.gitattributes` pins `eol=lf` rather than
  `eol=native`: Go tooling writes LF, so a native checkout re-dirties every file gofmt
  touches. 268 tracked files were converted in the working tree.
  *Verify:* `gofmt -l .` is empty; `go build ./...`, `go vet ./...`, and `go test ./...`
  (13 packages) are green after the conversion.
- [ ] **CI-004** — Make the numbered examples a release gate. 19 of 45 fail under the local
  provider today because the mock returns `Mock response for: ...`, incompatible with the JSON
  contracts of `Rank`, `Enrich`, `Predict`, `Verify`, and `Question`. Depends on **TI-001**.
  **Inventory, inherited from `PRODUCTION_TODO.md` when **DOC-003** folded it in.** As of that
  document's run, under `SCHEMAFLUX_PROVIDER=local`:
  *passing (26):* 02-transform, 07-summarize, 08-classify, 09-score, 10-compare, 11-similar,
  12-validate, 13-merge, 14-decide, 15-guard, 16-infer, 17-diff, 18-explain, 19-parse,
  20-complete, 21-redact, 22-suggest, 36-negotiate, 37-resolve, 38-derive, 39-conform,
  41-arbitrate, 42-project, 43-audit, 44-compose, 45-pivot.
  *failing (19):* 01-extract, 03-generate, 04-choose, 05-filter, 06-sort, 23-annotate,
  24-cluster, 25-rank, 26-compress, 27-decompose, 28-enrich, 29-normalize, 30-match,
  31-critique, 32-synthesize, 33-predict, 34-verify, 35-question, 40-interpolate.
  The cause is one thing: the local provider answers `Mock response for: ...`, which satisfies
  no JSON contract. **TI-001**'s scripted provider is the fix; this list is the acceptance
  criterion. The numbers are from 2026-03-06 and several of these operations have changed
  since — re-measure before relying on them.
- [x] **CI-005** — Coverage floor, ratcheted from the current measured value rather than set
  aspirationally.
  **Done — 62.2%, measured.** `scripts/coverage_floor.py`, run by the `quality` job.
  The measurement covers the **library packages only**. Including the forty-five numbered
  examples — `main` packages with no tests — puts the total at 47.5%, fifteen points of which
  say nothing about whether the library is tested. Making the examples a gate is **CI-004**,
  and it is a different kind of check.
  The tolerance is a whole point rather than zero, because coverage moves by a tenth when an
  error branch is added for a case that cannot be triggered yet, and a check that fails on
  that teaches people to regenerate the floor instead of reading it.
  *Verify:* the check exits 0 at the floor and 1 with the floor raised to 95%; `--update`
  ratchets it.
- [x] **CI-006** — Secret scanning on push; assert no cassette or fixture carries a key.
  **Done** — `scripts/secret_scan.py`, run by the `secrets` job over every tracked text file
  (485 of them). Seven vendor key shapes plus a credential-shaped-assignment heuristic, with
  a placeholder list so fixtures that must *look* like credentials do not keep it red, and a
  `secret-scan: allow` waiver honoured on the line or the line above it.
  **The first version was wrong in the way that matters, and the test caught it.** Planting a
  real-shaped OpenAI key in `testdata/` did not fail the scan: `1234` was on the placeholder
  list, the check was a substring match, and the planted key contained `123456789`. A scanner
  a real key can silence by coincidence is worse than none, because it reports success.
  Entropy now overrides the placeholder list — a run of 16+ alphanumerics carrying upper,
  lower, and digits is treated as issued no matter what words it contains.
  *Verify:* `python scripts/secret_scan.py --self-test` — 14 cases, seven that must be caught
  (including the one that escaped) and seven that must not (the shipped `testdata/sample.env`,
  a `t.Setenv` input, an `os.Getenv` call, a JS expression, a demo password, an explicit
  placeholder, a waived line). Both the self-test and the repository scan run in CI. One real
  waiver was added: `internal/ops/json_redaction_test.go` carries a bearer token as the
  payload it proves does *not* reach an error string.
- [x] **CI-007** — Public API surface test: snapshot the exported symbols so an unintended
  addition or removal fails review. Depends on **PS-003**.
  **Done, and it does not depend on PS-003 after all** — the snapshot records whatever is
  exported today, including the duplicate spellings PS-003 will resolve, and resolving them
  is then a visible diff rather than a silent one. That is the more useful order.
  `testdata/api_surface.txt`: 467 exported declarations of the root package, sorted, read
  from the source rather than by reflection so it sees types and constants a running program
  would not expose. A change prints as added and removed lines with the command to
  regenerate.
  *Verify:* `TestPublicAPISurface` passes against the snapshot and was confirmed to fail on a
  planted line. Regenerating takes an environment variable rather than a flag, because
  `go test` parses its own flags first and an unknown one fails the run before the test sees
  it.
- [x] **DOC-001** — Rewrite the README against what the code does. Today it advertises
  timeout control through context (dropped by 31 operations), cost tracking (zero or wrong for
  six of eight providers), retries for transient failures (classified by substring), and
  privacy filtering (a prompt hint).
  **Done — and three of the four complaints had already been fixed by the code rather than by
  the prose.** Context reaches every operation (**A-002**), cost is honest about being unknown
  (**PR-001**), retries branch on status rather than substrings (**P-007**, **CB-03**), and
  `Exclude` strips fields deterministically (**OP-301**). What the README was missing was any
  account of *this* session's work, which is where a reader's assumptions now go wrong.
  Added: a **What the type contract covers** section — the requiredness tag, what each mode
  means now that Creative does not invent, where confidence floors are enforced and where
  they are only an instruction, and `Validate`'s local rule path with its no-provider-call
  case. And an honest cost section: unpriced is not free, no model is priced at another
  model's rates, the history is bounded, budgets are edge-triggered and optionally enforcing.
  *Verify:* `readme_claims_test.go` — ten claims each tied to the test file that proves the
  behaviour, so removing the behaviour fails the doc test; plus a list of the review's false
  claims (`privacy filtering`, `automatically redacts`, `guaranteed valid`) that must not
  come back.
- [x] **DOC-002** — Migration guide for the breaking changes: `Run(ctx)`, the `Mode`/`Speed`
  renumbering, `Confidence` removal, per-operation result structs collapsing into
  `Result[T]`, `Decide`'s signature, `Redact`'s options, `Complete` losing its provider
  parameter, and the `SCHEMAFLOW_*` to `SCHEMAFLUX_*` environment rename already shipped.
  **Done as a README section rather than a new file**, per the standing rule about
  unrequested Markdown: a migration note nobody finds is not a migration guide, and the
  README is where a caller looks when their build breaks.
  Nine changes, each saying what broke and what to write instead: the `Mode`/`Speed`
  renumbering (**A-005**), `Classify`'s `~string` constraint (**OP-204**), `Categories` and
  enforced `MaxCategories` (**OP-203**), `Complete` losing its provider parameter
  (**OP-404**), `Redact`'s typed options (**OP-504**), enforced confidence floors
  (**OP-201**), `Summarize`'s length check (**OP-402**), `ResetCostTracking` no longer
  clearing budgets (**PR-004**), the removed Jaeger exporter, `Init` reporting an
  unresolvable model (**P-015**), and the bounded cost history (**PR-003**).
  *Verify:* `TestREADMEDocumentsTheBreakingChanges` fails if a note goes missing.
  **Still to add when they land:** `Run(ctx)` (**A-013**) and the per-operation result structs
  collapsing into `Result[T]` (**OP-401**), both still open. The `SCHEMAFLOW_*` to
  `SCHEMAFLUX_*` rename shipped before this session and is in the Environment section.
- [x] **DOC-003** — Update `docs/engineering/backlog/PRODUCTION_TODO.md` to point here, or
  fold it in and delete it.
  **Revised:** fold it in. It is dated 2026-03-06 and most of it is now false or already
  scheduled — the mock-provider complaint is **TI-001** and **CI-004**, the metrics
  complaint is **MW-007** and **OB-001**, the `.env` loaders are **B-03**, the stub tool
  families are **PS-001**, and the `-race` line is **CI-002**. The one item it holds that
  this list does not is the per-example pass/fail inventory; keep that inside **CI-004**
  and delete the file rather than maintaining a second backlog that drifts.
  **Done — folded in and deleted.** Everything it held was either already scheduled here or
  false. The mapping, so nothing is lost by the deletion:
  the mock-provider complaint is **TI-001** (shipped) and **CI-004**; the failing numbered
  examples are **CI-004**, whose inventory now lives in that task; the metrics-export
  complaint is **MW-007** and **OB-001**; the duplicated `.env` loaders are **B-03**
  (shipped); the stubbed tool families are **PS-001**; the `-race` line is **CI-002**
  (shipped); and "add a first-class way for each operation to declare whether it requires
  JSON output" is **S-005** (shipped).
  Its verification snapshot was dated 2026-03-06 and described a build five months and three
  breaking changes ago. A second backlog that drifts is worse than no second backlog.
- [ ] **REL-001** — Tag v0.2.0 at the end of M02 (provider correctness), v0.3.0 at the end of
  M05 (operations verified), v1.0.0 only after M10.
  **Revised:** M10 is no longer the last gate. With M11–M17 scheduled, v1.0.0 means the §19
  acceptance criteria pass (**RC-003**), not that the review is closed. Restated ladder:
  v0.2.0 after M02, v0.3.0 after M05, **v0.4.0 after M07** (operational claims true),
  **v0.5.0 after M12** (safe core, planner, scheduler), **v0.9.0-rc after M16**, and v1.0.0
  only when every §19 box is checked or carries an ADR saying why it is not.

---

# M11 — Execution planning and shapes

Everything above treats one call as the unit of work. `to-production.md` §8–9 says the unit
is a *logical request* that may fan out into chunks, stages, and recovery attempts, and that
the shape of that fan-out must be chosen deliberately, recorded, and inspectable before it is
paid for. Nothing in M01–M10 covers this; **CF-004**/**OP-109** built the one primitive that
exists. Corresponds to delivery Gate 2. Depends on M04.

- [ ] **PL-001** — Separate `Plan` from `Execute`, and expose both. A plan is immutable,
  serializable without sensitive content, and deterministic given the same input, policy
  snapshot, capability snapshot, and estimator version. `Preflight` returns it — eligibility,
  chunking, maximum call count, budget, and estimated cost — **without generating anything**.
  Closes **EXE-20**, **TRU-29**, and the preflight half of **PRD-22**.
  *Verify:* preflighting a 240-item batch makes zero provider calls and reports a call
  ceiling the subsequent run does not exceed; a plan built twice from the same inputs is
  byte-identical.
- [ ] **PL-002** — Explicit execution shapes: atomic, MDSP, SDMP, global. The planner selects
  one, records it in the plan and the envelope, and the caller can force it — `Atomic()`,
  `Batched()`, `Adaptive()` — because forcing atomicity for risk reasons and forcing batching
  for cost reasons are both legitimate. Closes **EXE-01**, **EXE-14**.
  *Verify:* each mode is observable in the envelope; a forced mode that is illegal for the
  operation is refused at plan time, not silently ignored.
- [ ] **PL-003** — MDSP batch protocol: stable invocation-local item IDs on the way out,
  deterministic validation on the way back — exact ID coverage, no duplicates, no unknown
  IDs, per-item schema and invariants — and caller order reconstructed in Go. Output
  position is never trusted. Closes **EXE-03**, **TRU-17**. Shares its implementation with
  **OP-101**.
  *Verify:* responses that omit an item, duplicate one, invent one, and reorder all of them
  each produce the right classification and the right unresolved set.
- [ ] **PL-004** — Token-aware chunk packing. The chunk is bounded by the earliest of item
  count, input tokens, output reserve, context limit, per-call cost, and provider payload
  bytes — accounting for system policy, operation prompt, schema, protocol overhead,
  serialized items, expected output per item, and a safety margin. An item too large for any
  chunk is routed atomically or refused; it is never silently truncated. Closes **EXE-04**
  and the estimation half of **PRD-22**. **OP-108**'s refusal is the degenerate case.
  *Verify:* packing respects each bound in isolation; an oversized single item is refused
  with a message naming which bound it broke.
- [ ] **PL-005** — Adaptive chunk sizing, bounded. Grow on sustained compliance, halve on
  truncation, omissions, duplicate IDs, malformed protocol, or repair above threshold; reduce
  concurrency separately from chunk size under rate pressure; never exceed the operation
  maximum or the budget; record the reason for each change. History is advisory — a
  deterministic per-request limit always wins — and is not shared across tenants.
  Closes **EXE-05**.
  *Verify:* a provider that truncates above 20 items converges to a stable size and stays
  there; the plan explains why.
- [ ] **PL-006** — Every stable operation declares its batch algebra: class (independent,
  subset, permutation, partition, graph, hierarchical, sequential), item encoder, merger,
  and global validation. Appendix C of `to-production.md` is the starting assignment.
  A generic map/reduce may execute a declared algebra; it may not invent one.
  Closes **EXE-13**, **ARC-16**, and the batching half of **TRU-24**. Feeds **TI-007**.
  *Verify:* a stable operation with no declared class fails a build-time check; the declared
  class generates the property test.
- [ ] **PL-007** — Plural APIs: `ExtractMany`, `ClassifyMany`, and their fluent twins, with
  batching, ordering, item identity, partial success, and scheduler policy as *stated*
  semantics. A singular function never reinterprets a slice, and a caller looping a singular
  call is no longer the supported way to process a collection. Closes **EXE-15**, **EXE-02**;
  supersedes the loop pattern the README currently documents.
  *Verify:* 500 inputs through the plural API make bounded batched calls; the singular API
  over a slice is a compile error or a documented single-item call, not a hidden loop.
- [ ] **PL-008** — Partial success and failure policies. `BatchResult[T]` with per-item
  status, attempts, mode, and evidence; `BatchSummary`; and the five policies — `FailFast`,
  `CollectFailures`, `RetryFailedItems`, `RetryThenCollect` (the default for long batches),
  `RequireAll` — each defining scheduling, cancellation, retry, and return behaviour.
  `([]T, error)` cannot express any of this. Closes **EXE-07**, **EXE-08**.
  *Verify:* one table per policy asserting what ran, what was cancelled, and what came back
  when item 3 of 10 fails terminally.
- [ ] **PL-009** — Progressive recovery cascade: preferred MDSP → keep valid items → isolate
  only unresolved IDs → smaller MDSP → atomic → escalate model or provider only if the
  minimum contract and data policy survive → review packet or terminal item failure at
  budget exhaustion. A valid item is never replayed because a sibling failed, unless the
  operation's global algebra requires recomputation. Closes **EXE-18**; it is also a §19.3
  acceptance criterion.
  *Verify:* a batch where two of twenty items fail spends recovery calls on two items, and
  the eighteen valid results are byte-identical to their first-attempt values.
- [ ] **PL-010** — SDMP staged plans over one datum, with reuse. Pass structured
  intermediates instead of resending the source, reuse deterministic preprocessing and
  schema artifacts, run independent checks concurrently under one budget, and **skip the
  model stage entirely when deterministic checks already establish the required contract**.
  Each stage carries its own operation ID and parent lineage. Closes **EXE-10**, **EXE-11**.
  *Verify:* a two-stage extract-then-verify plan sends the source once; a case where the
  deterministic check suffices makes one provider call, not two.
- [ ] **PL-011** — Global and hierarchical operation algebras, written per operation because
  no generic chunker can derive them: ranking (score within chunks, keep a candidate
  frontier, rerank globally, validate IDs and pairwise constraints), clustering (features →
  cluster in Go → optional model labels → exact coverage), deduplication (candidate pairs by
  hash or embedding → model judges likely pairs only → connected components in Go), global
  synthesis (chunk summaries with evidence → synthesize → verify claims against accumulated
  evidence). Closes **ARC-16** for the global classes and finishes what **OP-109** deferred:
  `Sort` refuses today because its merge was unknown, and this is where it becomes known.
  *Verify:* a 500-item `Sort` at the Quick tier returns a permutation of the input; a
  clustering covers every input exactly once; deduplication makes O(candidates) model calls,
  not O(n²).
- [ ] **PL-012** — Optional batch group for loop fusion: a caller keeps a natural Go loop,
  builders defer, and compatible work fuses into MDSP plans. Fusion is legal only when
  operation ID and version, schema hash, route policy, steering, contract level, data policy,
  and budget settings all match; otherwise the group partitions. Fusion is an optimization
  and must not change results relative to running each builder alone. Closes **EXE-16**.
  *Verify:* fused and unfused runs of the same fifty builders produce identical values;
  builders differing only in steering land in separate partitions.
- [ ] **PL-013** — Per-item metrics. HTTP 200 hides omissions and invalid output, so measure
  valid-item ratio, omissions, repairs, atomic fallbacks, and **cost per accepted item** —
  the number that actually says whether a batch strategy is working. Closes **EXE-19**.
  Feeds **OB-002**.
- [ ] **PL-014** — Planner explainability: a human-readable pre-execution plan explanation
  (mode, chunking, parallelism, recovery ladder, call ceiling, cost range, minimum contract,
  data policy) and a post-execution decision ledger recording every adaptive choice and its
  reason. Adaptive routing that cannot explain itself is unauditable. Closes **TRU-28**.
  *Verify:* the §8.6 example renders for a real 240-item classification; every adaptive
  decision in a run appears in the ledger with a reason.

---

# M12 — Bounded scheduler and resilience

M11 decides *what* to run; this decides *how much at once*, and what happens when the
provider pushes back. Also Gate 2. `CF-009` shipped bounded concurrency inside `MapReduce`;
this is the process-wide version with admission control.

- [ ] **SC-001** — Bounded scheduler: max concurrent calls, max queued nodes, in-flight
  tokens, in-flight cost, per-provider and per-tenant limits. Admission weighs estimated
  tokens, cost, quota, and priority; queues are bounded; a full queue or an unmeetable
  deadline returns `ErrAdmissionRejected` rather than allocating goroutines. The scheduler
  owns no semantics — it schedules plan nodes and propagates cancellation.
  Closes **EXE-09**, **PRD-23**, and the buffer half of **TRU-14**.
  *Verify:* 10,000 queued items hold flat goroutine and memory counts; rejection is
  observable, not a hang.
- [ ] **SC-002** — Rate limits, per-provider bulkheads, circuit breakers keyed by endpoint
  and optionally model, decorrelated jitter, and `Retry-After` honoured within the logical
  deadline. **CB-03** proved the failure mode: a retry ladder that expires inside the same
  closed rate-limit window buys latency and nothing else. Closes **PRD-07**.
  *Verify:* a provider returning 429 for 60s is not hammered; a breaker opens, sheds, and
  recovers; concurrent retries do not align.
- [ ] **SC-003** — Fairness: weighted fair queuing with per-tenant concurrency, token, and
  cost buckets; bounded priority classes rather than arbitrary integers; no starvation, and
  no bypassing locked provider or data-policy limits by claiming urgency. Tenant keys are
  bounded workload identifiers, never end-user PII. Closes **TRU-15**.
  *Verify:* a 10,000-item tenant and a 10-item tenant submitted together both progress; the
  small one is not stuck behind the large one.
- [ ] **SC-004** — Idempotency and ambiguity. Stable logical request IDs across every
  attempt, a unique attempt ID per provider call, provider idempotency keys where supported,
  and an `Ambiguous` marker on a timeout that may already have been served — the library
  performs no side effects itself, but its callers do, and they cannot tell today.
  Closes **PRD-10**.
  *Verify:* a timeout followed by a retry is reported as one logical request with two
  attempts, the first marked ambiguous.
- [ ] **SC-005** — Cancellation coverage at every blocking boundary: queues, backoff waits,
  HTTP, streams, workers, stores, exporters. On cancel — stop scheduling, drop queued nodes,
  cancel in flight, finalize verified items, mark the rest canceled, return the typed error
  *with* the partial result. No goroutine outlives its request. Closes **PRD-11**,
  **TRU-18**. **A-002** made the context reach the call; this makes it reach everything else.
  *Verify:* a leak test across all boundaries; cancelling a 200-item batch at item 50 returns
  50 verified items and 150 marked canceled.
- [ ] **SC-006** — `Client.Close()`: stop accepting work, cancel owned workers, honour a
  grace period, flush owned buffers and stores, close owned idle connections, **never** close
  caller-owned transports or exporters, return a joined error, and stay safe under repeated
  and concurrent calls. Closes **PRD-20**.
  *Verify:* double-close is a no-op; an in-flight request either finishes inside the grace
  period or returns a typed shutdown error with its partials; zero goroutines survive.
- [ ] **SC-007** — Caller-owned HTTP. Every provider accepts an `*http.Client` or transport
  with documented ownership, because enterprise deployments need their own proxies, mTLS,
  and instrumentation, and tests need a transport that fails on dial. Closes **PRD-21**.
  **P-006** made the client per-provider; this makes it injectable.
- [ ] **SC-008** — `ValidateConfiguration(ctx)` — non-billable readiness: provider
  registration, credential presence without revealing values, model maps, endpoint scheme
  and host policy, HTTP client presence, capability assumptions, scheduler limits, store
  readiness, and contradictory settings. Contradictions are rejected at construction rather
  than surviving to production. A separate `ProbeProviders` may make real calls and must say
  that it bills. Closes **PRD-18**, **PRD-19**.
  *Verify:* every configuration defect the review found is caught by the report without a
  provider call; a billable probe is labelled as one.

---

# M13 — Trust, evidence, and provenance

Gate 3. This is the milestone that makes the library's central claim honest: typed output
constrains form, and nothing more. M01 stopped operations lying about success; this stops
the *contract* being read as stronger than what was actually enforced.

- [ ] **TC-001** — Prompt segments carry a role and a trust level (trusted policy, trusted
  developer instruction, untrusted application data, untrusted retrieved data, untrusted
  model output). Untrusted content is delimited and never interpolated into system policy
  text; model output is untrusted until it crosses the contract layer. This reduces prompt
  injection; it does not eliminate it, and the documentation must say so. Closes **TRU-01**.
  *Verify:* an adversarial corpus of inputs containing instructions; injection attempts
  reach the model as data, and the operation's invariants still hold.
- [ ] **TC-002** — Evidence contract: `EvidenceRef{SourceID, JSONPointer, StartByte, EndByte,
  SourceDigest}` and `ClaimProvenance{FieldPath, Evidence, Inferred, Method}`, with four
  modes — none, material fields only, all model-derived fields, and `NoInference` (an
  unsupported field stays absent rather than being guessed). The runtime validates that
  references are in bounds and match the source digest; it does not claim the cited text
  entails the claim. Closes **TRU-02**. **OP-507** builds the first spans.
  *Verify:* a fabricated field with no supporting span fails `StrictEvidence`; an
  out-of-bounds or wrong-digest reference is rejected.
- [ ] **TC-003** — Provenance through pipelines: result IDs, parent links, input and schema
  digests, operation and prompt versions, resolved model, normalizers applied, item recovery
  path, cache usage, and library and adapter versions. A summary built from an extraction
  traces back to the original evidence; where lineage breaks, the delivered contract cannot
  be `FullyGoverned`. Closes **TRU-03**.
  *Verify:* a three-stage pipeline's final claim resolves to a span in the original source.
- [ ] **TC-004** — Contract levels, requested and delivered:
  `PromptOnly < JSONWellFormed < SchemaConstrained < SchemaAndInvariantChecked <
  EvidenceChecked < FullyGoverned`. Every detailed result states which level was asked for
  and which was actually delivered, and any degradation requires policy approval rather than
  a log line. Native provider structured output improves the *mechanism*; deterministic
  post-validation is still required. Closes **ARC-11**, **ARC-24**.
  *Verify:* a run that falls back from strict `json_schema` to prompt-only reports a lower
  delivered level; with degradation disallowed, it returns an error instead.
- [ ] **TC-005** — Model drift: record requested tier or pin, resolved provider and model
  identifier, and provider model revision where exposed. `Tier(Smart)` is documented as
  floating; `Model(...)` is a pin request that the provider may not fully honour, and the
  envelope says so. Closes **TRU-04**. This is what makes **P-017**'s benchmark reproducible
  six months from now.
- [ ] **TC-006** — Repair safety and repair regression. Strategy is chosen by failure class:
  syntax damage may include the prior response, bounded and delimited; missing fields may be
  patched; **invariant and evidence failures regenerate from source**, because feeding a
  fabricated answer back reinforces it. After any repair the original schema, invariant,
  evidence, and cardinality contracts are reapplied, and previously valid fields are compared
  for unrelated loss or mutation — "valid JSON" is not repair success. Closes **TRU-19**,
  **TRU-20**; hardens **A-010**, which currently feeds the failure back for every class.
  *Verify:* a repair that fixes the flagged field while dropping a valid unrelated one is
  rejected; an invariant failure produces a regeneration, not an edit.

---

# M14 — Provider capability, routing, and data policy

Gate 4. Today "supported provider" means an adapter compiles. **CB-01** is the cost of that:
the same `Extract[T]` was schema-enforced on one provider and an unconstrained guess on five
others, with nothing at the call site to tell them apart.

- [ ] **CP-001** — Machine-readable provider capabilities — native JSON schema, JSON mode,
  supported schema keywords, streaming, tool calling, multi-turn, prompt caching, idempotency
  keys, seed, usage-reporting quality, model-revision reporting, regions, retention modes,
  context and output ceilings — and planner negotiation that intersects operation
  requirements, invocation requirements, data policy, capability snapshot, budget, and
  breaker state before execution. Keyword stripping is permitted only when the remaining
  mechanism plus deterministic validation still delivers the requested contract, and the plan
  discloses it. **CB-02**'s Cerebras keyword stripping becomes declared behaviour rather than
  a transport-layer workaround. Closes **ARC-10**, **ARC-09** routing half, **ARC-24**.
  *Verify:* a provider that cannot deliver the minimum contract is not selected; a plan
  built against a stale capability snapshot returns `ErrPlanStale` rather than executing.
- [ ] **CP-002** — `DataPolicy`: classification, allowed providers and regions, retention,
  training use, content logging, result caching, and minimum contract — locked at the client
  or tenant boundary, strictenable by an invocation, never weakenable. A failure on a private
  route may not fall back to a public one. Closes **TRU-11**; **P-008**'s `store: false`
  default is the first instance of it.
  *Verify:* a us-only, no-retention policy makes an ineligible provider unselectable, and
  the refusal names the constraint.
- [ ] **CP-003** — Provider conformance suite and generated support matrix, with four
  distinct labels: integrated, conformant, live-verified, production-supported. The suite
  covers auth and permission classification, timeouts, cancellation, ambiguous completion,
  429 with valid, invalid, and missing `Retry-After`, 5xx behaviour, empty, malformed,
  truncated, and refused output, supported and unsupported schema keywords, request-ID and
  usage extraction, reasoning and cached-token normalization, Unicode and large payloads,
  streaming termination, caller-supplied endpoint and HTTP client, credential redaction, and
  capability-declaration consistency. Documentation may not collapse the four labels into
  "supported". Closes **PRD-02**.
  *Verify:* the matrix is generated from suite results, not hand-written; a provider failing
  a declared capability loses the label automatically.

---

# M15 — Security, privacy, and caching

- [ ] **SEC-001** — Publish `SECURITY.md`: threat model, supported versions, disclosure
  process, and response targets. The assets are credentials, prompts, source data, outputs,
  schemas, tenant identity, diagnostics, and routes; the actors include malicious input
  authors, compromised endpoints, other tenants, and the model's own output.
  Closes **PRD-04**. Coordinate with **PS-009**.
- [ ] **SEC-002** — Content logging policy: `LogNoContent`, `LogMetadataOnly` (the production
  default), `LogRedactedContent`, `LogFullContent` (explicit, policy-gated). **Debug changes
  verbosity, not content policy** — that inversion is how prompts end up in log aggregators.
  Authorization headers, keys, cookies, and raw bodies are always scrubbed; captured buffers
  are bounded and retention-limited; diagnostic files get restrictive permissions.
  Closes **PRD-05**. **F-033**/**F-034** removed payloads from errors; this covers logs.
  *Verify:* a log-capture test asserts no prompt, completion, or credential fragment at any
  level with the default policy.
- [ ] **SEC-003** — Fuzz targets with allocation and depth limits: recursive and deeply
  nested types, embedded fields and tags, maps, pointers, nils, custom marshalers, malformed
  UTF-8, huge numbers and exponents, duplicate keys, trailing data, JSON bombs, schema
  keyword adaptation, fence and JSON extraction (**A-003**), batch IDs with duplicates,
  omissions, and adversarial ordering, repair parsing, and redaction scrubbing.
  Closes **PRD-12**.
  *Verify:* the corpus runs in CI as a smoke gate; each target has a seed corpus drawn from
  real defects this list already found.
- [ ] **SEC-004** — Custom endpoint policy: scheme and host allowlists and a private-address
  rule, so a caller-supplied or configuration-supplied base URL cannot be pointed at internal
  infrastructure. The library already accepts custom endpoints for OpenAI-compatible
  providers and validates nothing about them.
  *Verify:* loopback, link-local, and metadata-service URLs are refused unless explicitly
  allowed; the refusal is a typed configuration error.
- [ ] **SEC-005** — Retention and deletion for every store the library owns — result caches,
  diagnostic captures, pricing and usage records, replay fixtures. User content is never
  retained merely because cost accounting is on: cost records keep token counts, model IDs,
  request IDs, and money. Tenant-scoped deletion hooks where the adapter supports them.
  *Verify:* a tenant deletion removes its cache and diagnostic entries; the pricing store
  contains no prompt text by construction.

---

# M16 — Observability and operations

- [ ] **OB-001** — Observer interfaces in core (logger, tracer, metric sink, clock, ID
  generator, diagnostic sink) with no-op defaults, and the OpenTelemetry implementation in
  `telemetry/otel` using the host's provider. Core stops importing OTel, exporters, and the
  SQLite pricing store; optional adapters may. Closes **ARC-17**, **ARC-18**.
  *Verify:* core's dependency list is asserted by a test; the library initializes no global
  SDK and closes nothing it did not create.
- [ ] **OB-002** — Metrics catalog per §15.2: requests, duration, plan nodes, provider
  attempts and duration, queue duration, in-flight gauge, circuit state, item outcomes, batch
  size, batch compliance ratio, repairs, atomic fallbacks, tokens, cost, budget exhaustion,
  and review-required counts. High-cardinality identifiers stay out of metric labels and live
  in the envelope. Depends on **PL-013**.
- [ ] **OB-003** — Cost tree: logical request → stage → chunk → attempt → item attribution,
  with provider-reported usage preferred, estimates marked, and pricing quality recorded as
  `Exact`, `Estimated`, `Unknown`, or `Free`. Historical cost is never recomputed with
  current rates without keeping both versions. Closes **TRU-22** and the drift half of
  **TRU-23**. **PR-001** established that zero never means unknown; this makes the same
  distinction hold across a fan-out.
- [ ] **OB-004** — Operational SLOs as tests, per §15.4: zero panics from valid API use, zero
  goroutine leaks after cancel or completion, zero client-isolation failures, zero unknown or
  duplicate batch IDs accepted, zero secrets in logs, zero calls exceeding a declared budget,
  and validated-item completeness at or above 99.5% on the conformance corpus.
  *Verify:* each SLO has a named test; the leak and isolation ones run in CI on a platform
  where `-race` works (**CI-002**).
- [ ] **OB-005** — Runbooks and dashboards for the incident classes that will actually
  happen: 429 surge, provider latency or outage, malformed or truncated output spike, batch
  omission or repair-rate spike, cost anomaly, model alias or revision change, capability or
  schema enforcement regression, stuck breaker, cache or pricing-store failure, telemetry
  exporter failure, suspected content or credential leak, deprecated endpoint. Each names its
  signal, mitigation, safe fallback, evidence to capture, and recovery criterion.
  Closes **PRD-24**.

---

# M17 — Release engineering and v1 acceptance

- [ ] **RC-001** — Release contents per §17.2: tagged and checksummed artifacts, generated
  changelog, Go API changes **and semantic behaviour changes**, operation, prompt, and schema
  version changes, provider capability and live-verification matrix, platform support matrix,
  known degradations, migration steps, SBOM, vulnerability scan, and the release-candidate
  semantic benchmark comparison. A prompt edit with no Go signature change is a behaviour
  change and belongs in the notes. Closes **PRD-03**, **ARC-23**.
- [ ] **RC-002** — Semantic regression suite for release candidates, on pinned operation
  versions and as-pinned-as-available models: extraction accuracy and hallucination,
  missing-field and evidence-reference validity, classification accuracy and abstention,
  choose/filter precision and recall, ranking agreement and ID coverage, repair success and
  regression rate, valid-item ratio by batch size, latency, tokens, and cost per accepted
  item, and prompt-injection resistance. Results are statistical, with repeated trials and
  intervals — a single exact-output assertion is not a stable live test. Closes **PRD-15**,
  **TRU-21**. This suite spends money: it runs only on a protected release-candidate
  workflow with an explicit spend ceiling, never on `go test ./...` (**B-04**).
- [ ] **RC-003** — Track §19's acceptance criteria as a checklist in this file, and require
  every unchecked box at v1.0.0 to carry an ADR saying why it ships unmet. Twenty-nine boxes
  across core architecture, correctness and trust, execution and resilience, security and
  governance, and verification and operations.
- [ ] **RC-004** — Compatibility and deprecation policy: at least one documented window for a
  deprecated stable API, deprecation notices that name a mechanical replacement, global and
  default-client APIs routed to `quick` or a compatibility adapter, and removal only at a
  major version. There is not one `// Deprecated:` marker in the repository today
  (**PS-003**).
- [ ] **RC-005** — Load and chaos suites: large item streams, provider slowdown and 429
  bursts, mixed tenants and priorities, cancellation storms, large schemas and near-limit
  chunks, partial MDSP failures forcing atomic fallback, and failing telemetry and stores.
  The outcome metrics are cost and latency per valid item, not calls per second.
- [ ] **TR-002** — Extend `.audit/traceability.py` to `to-production.md`'s gap register the
  way **TR-001** covers the review: parse `ARC`, `PRD`, `API`, `EXE`, and `TRU` rows, map
  each to the tasks citing it, and fail on an untraced gap or a dangling citation. The
  coverage table below is the first run, done by hand; it will drift within a week if
  nothing checks it. Note the existing collision: the specification labels its decisions with
  a `D-` prefix and three digits, while the review numbers schema findings `D-01` through
  `D-15` with two — near enough that the script reads one as the other, which is why tasks
  cite decisions as `ADR-nnn` instead.
  *Verify:* the script fails when a gap's only task is deleted, and when a task cites a gap
  ID that does not exist.

---

## Reconciliation with `to-production.md`

The specification was written after this list and does not supersede it wholesale — it is a
target architecture, and this list is a defect ledger with evidence attached. Where they meet,
these are the rulings. Where they conflict, the ruling says which one won and why.

**Scale, first, because it changes what "production-ready" means here.** M01–M10 finish a
library that does not lie about its results. M11–M17 build the one the specification
describes: a planner, a scheduler, a trust model, a capability model, and a governance
envelope. That is roughly three times the remaining work, and much of it is only worth
building for multi-tenant or consequential deployments. **This is a scoping decision, not a
technical one, and it is not made yet.** A defensible smaller v1 is M01–M10 plus M13
(trust), **SC-005**/**SC-006** (cancellation and shutdown), **CP-001** (capability
negotiation), and **SEC-002** (logging), with the planner and scheduler deferred to v2 under
an ADR. Until that decision is recorded, M11–M17 are scheduled but not committed.

**Rulings.**

1. **`Client`'s fate is decided** — see **IN-004**. ARC-01 and ADR-003 remove the option of
   documenting the global model honestly.
2. **`Meta` is the execution envelope** — see **A-006**. Build it with requested-versus-
   delivered contract and a cost tree from the start rather than renaming it at M13.
3. **The verb catalogue is decided** — see **PS-002**. §6.2's eight categories are the stable
   core; everything else becomes a recipe or is marked experimental.
4. **Batch alignment is by ID, not index** — see **OP-101** and **PL-003**. The review
   proposed indices to fix identity; indices fail under exactly the omission and reordering
   they are meant to catch.
5. **`Sort`'s refusal is a stopgap, not the answer.** **OP-109** deliberately kept refusing
   because a sort merge is an interleave the library could not guess. ADR-019 agrees it
   cannot be guessed generically and requires it be *declared* per operation — so **PL-011**
   owns it, and the original "500-item `Sort` returns 500 items" bar comes back.
6. **The workflow-engine documents are out of scope, not unscheduled** — see **PS-008**.
   API-12 and §1.3 make a workflow DSL an explicit non-goal.
7. **The error taxonomy widens now, not incrementally** — see **A-007**. Every recovery
   decision in M11–M14 branches on failure class; adding kinds later means rewriting those
   branches.
8. **Tool calling (`PS-004`), embeddings (`PS-006`), and multi-turn (`PS-005`) are product
   decisions, not v1 gates.** The specification requires only that capabilities be
   *declared* (CP-001) and that conversational names not sit on single-shot implementations
   (ARC-20). None of the three blocks v1.
9. **`P-007`'s deviation stands and needs an ADR.** §10.1 says `Error()` carries sanitized
   metadata only; the shipped implementation keeps the vendor's structured message because
   withholding "exceeds maximum nesting depth" moves the debugging cost onto the caller. The
   compromise — vendor message capped at 400 characters, raw body retained on the value and
   never printed, `SCHEMAFLUX_ERROR_DETAIL=1` for the rest — is a reasonable reading of
   PRD-09, but it is a documented departure and should be recorded as one under **PS-009**'s
   ADR directory rather than left as a note in an evidence row.
10. **Delivery gates map onto milestones**, so the specification's exit criteria can be used
    as-is: Gate 0 ≈ M00–M03, Gate 1 ≈ M04 + M06 + **IN-004**, Gate 2 ≈ M11 + M12, Gate 3 ≈
    M13 + M05's invariant work, Gate 4 ≈ M14 + M02, Gate 5 ≈ M15–M17.
11. **Live cost stays gated.** The specification's semantic suites (§16.6) assume spend that
    **B-04** exists to prevent by accident. `RC-002` runs on a protected workflow with a
    ceiling; a plain `go test ./...` still reaches nothing billable, ever.
12. **Three ID collisions fixed while reconciling.** `OP-104`, `P-014`, and `P-015` were each
    carrying two different tasks — one open in a milestone, one filed later under "Added
    during the work" — so an ID could be simultaneously open and closed and no script could
    tell which. The later of each pair is renumbered `OP-110`, `P-017`, and `P-018` with a
    note in place, and the two cross-references to the old `P-014` now point at `P-017`.
    Nothing else moved: the no-renumbering rule protects identifiers that mean one thing,
    which these did not.

**Gap coverage.** Every gap in the specification's register maps to at least one task. Done
by hand; **TR-002** makes it checkable.

| Gap | Task(s) | Gap | Task(s) |
| --- | --- | --- | --- |
| ARC-01 | IN-004, TI-002, TI-008 | PRD-13 | TI-003 |
| ARC-02 | IN-004, TI-002, A-002 | PRD-14 | A-001, PS-007, P-009 |
| ARC-03 | PS-002 | PRD-15 | RC-002 |
| ARC-04 | A-001 | PRD-16 | PS-002 |
| ARC-05 | FL-003, FL-005 | PRD-17 | CI-001, CI-002 |
| ARC-06 | A-013, A-002 | PRD-18 | SC-008 |
| ARC-07 | A-013, IN-004 | PRD-19 | SC-008 |
| ARC-08 | A-004 | PRD-20 | SC-006 |
| ARC-09 | P-015, CP-001 | PRD-21 | SC-007 |
| ARC-10 | CP-001 | PRD-22 | PL-001, PL-004 |
| ARC-11 | TC-004, A-009 | PRD-23 | SC-001 |
| ARC-12 | OP-205, OP-302, PL-010 | PRD-24 | OB-005 |
| ARC-13 | A-006, OP-302, TC-003 | PRD-25 | PS-009 |
| ARC-14 | A-010, PL-009, PR-005 | API-01 | FL-003, FL-005 |
| ARC-15 | A-007 | API-02 | A-012, PL-007 |
| ARC-16 | PL-006, PL-011 | API-03 | FL-003 |
| ARC-17 | OB-001 | API-04 | A-013 |
| ARC-18 | OB-001, MW-007 | API-05 | A-013 |
| ARC-19 | IN-004, F-014 | API-06 | PS-003, A-005 |
| ARC-20 | PS-005 | API-07 | A-004 |
| ARC-21 | CF-008, PS-002 | API-08 | A-004 |
| ARC-22 | OP-206 | API-09 | A-001 |
| ARC-23 | PS-007, TI-004, RC-001 | API-10 | FL-005 |
| ARC-24 | TC-004, MW-008, CP-001 | API-11 | A-006, A-013 |
| PRD-01 | CI-001–CI-006 | API-12 | PS-008, CF-008 |
| PRD-02 | CP-003 | EXE-01 | PL-002 |
| PRD-03 | RC-001 | EXE-02 | PL-007, OP-304 |
| PRD-04 | SEC-001 | EXE-03 | PL-003, OP-101 |
| PRD-05 | SEC-002, A-011 | EXE-04 | PL-004 |
| PRD-06 | PR-005 | EXE-05 | PL-005 |
| PRD-07 | SC-002 | EXE-06 | A-007, TI-006 |
| PRD-08 | A-007 | EXE-07 | PL-008 |
| PRD-09 | A-011 | EXE-08 | PL-008 |
| PRD-10 | SC-004 | EXE-09 | SC-001, CF-009 |
| PRD-11 | SC-005 | EXE-10 | PL-010 |
| PRD-12 | SEC-003 | EXE-11 | PL-010, OP-205 |
| EXE-12 | OP-101 | TRU-08 | S-008 |
| EXE-13 | PL-006, TI-007 | TRU-09 | S-010 |
| EXE-14 | PL-002 | TRU-10 | MW-004, P-009 |
| EXE-15 | PL-007 | TRU-11 | CP-002 |
| EXE-16 | PL-012 | TRU-12 | MW-008 |
| EXE-17 | PR-005 | TRU-13 | ST-002 |
| EXE-18 | PL-009 | TRU-14 | ST-002, SC-001 |
| EXE-19 | PL-013 | TRU-15 | SC-003 |
| EXE-20 | PL-001 | TRU-16 | A-013 |
| TRU-01 | TC-001 | TRU-17 | PL-003, OP-101 |
| TRU-02 | TC-002, OP-507 | TRU-18 | SC-005 |
| TRU-03 | TC-003 | TRU-19 | TC-006 |
| TRU-04 | TC-005 | TRU-20 | TC-006 |
| TRU-05 | S-002, S-011 | TRU-21 | RC-002, TI-003 |
| TRU-06 | S-003 | TRU-22 | OB-003, A-006 |
| TRU-07 | S-009 | TRU-23 | PR-002, OB-003 |
| TRU-24 | PS-002, PL-006 | TRU-28 | PL-014 |
| TRU-25 | A-009 | TRU-29 | PL-001 |
| TRU-26 | CF-006 | TRU-30 | OP-206, OP-201 |
| TRU-27 | CF-002 | | |

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

Added with M11–M17:
A-001 ──> PL-001 ──> PL-002, PL-004 ──> PL-005      (no plan, no shape, no packing)
A-007 ──> PL-009, SC-002, TC-006                     (recovery branches on failure class)
PL-003 ──> PL-006 ──> PL-011, TI-007                 (IDs, then algebra, then properties)
PL-001 ──> SC-001 ──> SC-003                         (schedule plan nodes, then fairly)
IN-004 ──> TI-002, TI-008, SC-006                    (isolation before lifecycle)
S-002 ──> S-011, P-009, MW-004                       (schema identity keys everything)
CP-001 ──> MW-008, CP-002, CP-003                    (no routing without capabilities)
TC-002 ──> TC-003 ──> TC-004                         (evidence, lineage, delivered level)
PL-013 ──> OB-002 ──> OB-004                         (measure items before asserting SLOs)
CI-002 ──> TI-008, OB-004                            (-race needs a Linux amd64 runner)
B-04 ──> RC-002                                      (the spend gate outlives the credits gate)
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
| **F-020** | `012ea72` | 297 occurrences renamed across 53 files: `Confidence` → `ModelConfidence`, plus `ModelTrustScore`, `ModelOverallConfidence`, `ModelOverallScore`. JSON tags unchanged — that is the wire contract with the model. `internal/ops/provenance_test.go` walks 1070 exported fields and fails any that reads as a measurement without saying so, which is F-020's stated check made permanent. README gained a section on what the number is not. |
| **F-021** | `012ea72` | `Redact`, `RedactWithResult`, and `RedactLLM` say NOT PRODUCTION READY in their doc comments and the README, each naming the concrete failure (substring field match, patterns that miss their formats, an audit that returns an empty map, a reversible jumble, unvalidated character offsets) rather than advising caution. `internal/ops/disclosure_test.go` parses the package and fails if any disclosure goes missing. |
| **F-022** | `012ea72` | The "privacy filtering" framing is gone, replaced by what `Exclude` guarantees and where the guarantee stops. Closed together with **OP-301**, which made the documentation true rather than merely weaker. |
| **OP-301** | `012ea72` | Excluded fields are stripped from the payload before serialisation — matched by JSON field name, case-insensitively, at every level — and the output is scanned for the removed values. `internal/ops/project_exclude_test.go` (12 strip cases, 9 leak-scan cases) and `project_integration_test.go` (6 exclusion cases at the provider boundary plus `Example_projectWithholdsFields`). Both end-to-end tests were verified to FAIL against the prompt-hint version. |
| **F-024** | `9aec323` | `internal/ops/dead_options_test.go` walks the package and fails any exported `*Options` field with a setter and no reader, excluding the setter and merge functions that do not constitute a reader. Written first, so it produced the authoritative list rather than the review's: it found **five fields the review missed** (`ClusterOptions.GenerateDescriptions`, `ConformOptions.PreserveUnknown`, `DecomposeOptions.PreserveHierarchy`, `MatchOptions.Bidirectional`, `ScoreOptions.Normalize`) and corrected two entries (`FilterOptions.BatchSize` is live; `TransformOptions.To` no longer existed). 365 option fields, 0 dead. |
| **F-023** | `9aec323` | All 18 resolved on a stated principle. The six that promised deterministic machinery a prompt cannot deliver — `OnProgress`, `PreProcess`, `PostProcess`, `SaveProgress`, `PreserveNulls`, `PreserveFormat` — were deleted with their setters. The twelve that are legitimately instructions to the model were threaded into their prompts. `internal/ops/options_reach_prompt_test.go` proves each one: it renders the prompt with and without the option and fails if they are identical, which is the difference between a field being read and a field being honoured. |
| **OP-102** | `09f93eb` | **C-01** closed. `Choose` matches the model's answer back to the input and returns the caller's own item; a selection that was not offered is an error. The failure this guards against is not an invented product but an echoed one with a changed price, ID, or date — `internal/ops/collection_identity_test.go` covers seven such alterations, and all seven were verified to PASS silently against the old code. |
| **OP-103** | `09f93eb` | **C-02** and **C-03** closed. `Filter` verifies every returned item was in the input, rejects duplicates and any result longer than the set, and returns the caller's values in the caller's order. The one-item parse fallback is gone — a malformed array used to collapse a filter to a single result. The contradictory instruction is gone too: the system prompt said "Include items that match" while the steering said "Remove items that match", and the library reported whichever the model obeyed as success. |
| **OP-403** | `9408aa0` | **T-06** closed. `MaxLength`, `ShowFirst`, and `ShowLast` are documented in characters and were indexed in bytes, so any cut landing mid-rune produced invalid UTF-8 — `Montréal` truncated to seven "characters" came back with a replacement character. `internal/ops/runes.go` does the same jobs on runes; `Complete`'s truncation and `RedactLLM`'s masking and splicing use it. `internal/ops/runes_test.go` walks every cut point of six ordinary strings (an accented place name, a currency symbol, Japanese, an emoji) and was verified to FAIL against byte indexing. Partially closes **T-13**: `applyRedactions` now refuses a span that is negative, inverted, out of range, or overlapping, rather than applying it — semantic validation of what a span contains remains **OP-507**. |
| **S-005** | `5bf1d1f` | **I-04** closed. The response format was chosen by searching the concatenated system **and user** prompts for phrases like "json object", so a caller summarising a changelog that said "return a json object" got their text operation switched into JSON mode — the format depended on the data. Inference now reads the system prompt only, which the library writes, and `OpOptions.ResponseFormat` lets an operation declare what it needs outright. `internal/ops/response_format_test.go` uses ordinary inputs (a support ticket, a changelog, a bug report) rather than crafted attacks; **six of the seven flipped the format against the old code**. |
| **OP-501** | `56d680b` | **T-07** closed. Field names are matched as whole normalised names, not substrings: `Filename`, `Username`, `Keywords`, `KeyMetrics`, `APIKeyLabel`, `FirstSeen`, `LastUpdated`, `CardCount`, and `AddressBookSize` survive; `FirstName` and `APIKey` do not. Map keys are treated as field names too, which they never were. |
| **OP-502** | `56d680b` | **T-08** closed. Card numbers are validated with Luhn rather than recognised by shape, so an unformatted PAN is caught and a 16-digit order number is not. Phone patterns cover `(305) 555-1234`, `305.555.1234`, and international forms. The three false-positive patterns are deleted: two capitalised words (which matched "New York"), a bare nine-digit run (order IDs), and every currency amount. A bare nine-digit SSN stays deliberately undetected and the docs say so. |
| **OP-503** | `56d680b` | **T-09** closed. `RedactWithResult` reports the values it matched by category and the fields it replaced by name, and a field matched by *content* now has only the matching span replaced — so a notes field containing a phone number keeps the note. |
| **OP-505** | `56d680b` | **T-11** closed. `JumbleSeed` defaults to zero and the RNG was then seeded with the input's LENGTH, a number readable off the output, so the permutation was reproducible and the jumble exactly invertible. The default now draws from `crypto/rand`. An explicit seed still gives determinism, documented as a thing to want for fixtures and not for privacy — and jumbling is documented as obfuscation, not anonymization, because a permutation preserves length, alphabet, and frequency whatever the seed. |
| **A-002** | `a3d1e19` | **X-01** closed. All 29 sites writing `context.WithTimeout(context.Background(), …)` now derive from the caller's context through `operationContext`. Caller cancellation reaches the provider call, and a caller deadline sooner than the library's wins. `Guard` gained a `context.Context` parameter — it had none at all, so its suggestion call could not be cancelled. `internal/ops/context_test.go` drives thirteen operations with a cancelled context and asserts the call sees it; a source-level check fails any reintroduction of the pattern. |
| **S-001** | `0eea104` | **D-01** closed. `GenerateTypeSchema` expanded only the top level and described every other field by its Go type name, so a model asked for an `Order` was told its customer was a `main.Person` and had to invent the shape. It recurses now, through structs, slices, maps, and pointers, flattening embedded structs the way `encoding/json` does. Depth is bounded at six and a self-referential type is named rather than followed. Exclusion applies at every level, not just the top. `internal/ops/schema_recursion_test.go` was verified to FAIL against the flat version, and `TestLiveExtractNestedStructure` extracts a nested order with two line items from the live model. |
| **OP-305** | `0fb5164` | **D-11** closed. CSV headers were matched against Go field names via `capitalizeFirst`, so a struct with `json:"full_name"` never received its own column, and an unmapped column was skipped silently — a single-struct target with zero matching headers returned a zero-valued struct and a nil error. Headers now match the json tag or the field name, folding case, spaces, underscores, and hyphens, and a CSV that maps to nothing is an error naming the columns it accepts. |
| **S-004** | `0fb5164` | **D-05** closed. `Strict()` called a validator that took a threshold it never read and checked only the top level, so a nested address that came back entirely empty reported success. Validation recurses through structs, slices, and maps, names the failing path (`home.country`), and the unused threshold parameter is gone. |
| **OP-108** | `0fb5164` | **C-06** closed for the refusal half. Collection operations marshalled the whole slice with no size guard while output is capped at 1000–4000 tokens by tier, so a `Sort` over a few hundred items truncated and surfaced as a JSON parse error that said nothing about the cause. `Sort` and `Filter` now estimate the echo budget and refuse with an error naming the tier, the estimate, and roughly how many items would fit. Chunking with a merge step remains open as **OP-109**. |
| **P-005** | `0fb5164` | **I-05** closed. The library generated an exact schema for `T`, rendered it into the prompt as prose, and asked the API only for a `json_object` — the one artifact that could make `Extract[T]` structurally guaranteed never reached the API that can enforce it. `GenerateJSONSchema` emits strict-mode JSON Schema (every property required, `additionalProperties: false`, optionals as unions with null) and `Extract` sends it. A type strict mode cannot express — a map, a recursive struct — produces no schema and the call falls back to prompt-only rather than sending something the API would reject. **Verified live:** `TestLiveStructuredOutputIsEnforced` extracts a nested contact under an enforced schema. |
| **TI-001** | `46a1d51` | **Gap-06** closed — the review calls it the single largest adoption blocker. `schemafluxtest` ships an exported fake provider: scripted replies in sequence, a failure mode, a fail-then-recover mode for retries, a cancellable slow mode for timeouts, settable usage for cost accounting, and a recorder of the exact requests it received. `Install(t, provider)` swaps it in and restores the previous client. 15 tests plus two runnable Examples. Found while building it: `NewClient(...).WithProviderInstance(...)` registers the package-level provider but never installs the client, so `GetDefaultClient()` still returned the old one — `SetDefaultClient` is new, and `TokenUsage`, `CostInfo`, and `ResultMetadata` were not exported at the root, so a caller implementing `Provider` could not name the types they were required to return. |
| **A-010** | `b2e39f4` | **CF-01** closed — the review calls it the highest-value missing feature. A parse failure or a failed `Strict()` post-condition used to be terminal, even though the retry machinery already existed in `CallLLM` and was simply not wired to what the answer said. `withRepair` feeds the failure back and asks again with the problem named; the original task stays at the front of the prompt so the cacheable prefix does not move. A transport failure is deliberately *not* repaired — there is no answer to show the model. Default one repair, `SCHEMAFLUX_REPAIR_ATTEMPTS` to change it. 20 unit cases, a consumer-facing test through `schemafluxtest`, and a live extraction. |
| **T-13 (part)** | `56d680b` | The offset half closed with **OP-403**: spans that are negative, inverted, out of range, or overlapping are refused. The semantic half — whether a span contains what the model says — remains **OP-507**. |
| **CF-04** | `debaf6e` | **CF-004 + OP-109 + CF-009** closed. There was no chunk → operate → merge primitive anywhere in the library — `BatchProcessor` chunks, but only for `Extract` and only internally — so a collection larger than one context window had no supported handling and a caller could not build the split either. `MapReduce`/`MapReduceFlat`/`Chunk` are exported, with bounded concurrency, input-order results regardless of finish order, and failures naming the chunk index *and* the input offset so a caller knows which of their items are missing. `Filter` now chunks itself; `Sort` still refuses, because a filter's merge is a concatenation the library knows and a sort's is an interleave it does not. Writing the integration example surfaced a second defect: a failing chunk cancelled the rest, and the reported error was whichever failure had the lowest index — usually a `context canceled` *caused by* the real failure. `rootFailure` reports the first non-cancellation. 12 unit tests, an integration test driving a 200-item `Filter` through the public API, and a runnable `Example_mapReduce`. |
| **CB-01** | `f745d66` | **New, found while wiring Cerebras as the secondary provider.** `OpenAICompatibleProvider.Complete` went through go-openai, whose `ChatCompletionResponseFormat` carries a `Type` and nothing else — so a request arriving with a strict JSON schema had the schema **dropped** and was sent as `{"type":"json_object"}`. The same `Extract[T]` got constrained decoding on OpenAI and an unconstrained guess on Cerebras, DeepSeek, Qwen, ZAI, and OpenRouter, with nothing at the call site to tell them apart — the same fail-open the review is about, in the one place the library's whole contract lives. The chat transport is now hand-rolled for the same reason the Responses path is, and sends `json_schema` with `strict: true`. **Verified live** against `gemma-4-31b`: strict output enforced against a prose instruction, nested types, and a `time.Time` field. |
| **CB-02** | `f745d66` | **New.** Cerebras rejects `format`, `pattern`, `minItems`/`maxItems`, `minLength`/`maxLength`, `minimum`/`maximum` outright rather than ignoring them. `GenerateJSONSchema` annotates `time.Time` with `format: date-time`, so the first extraction with a timestamp field would have failed the *whole* request with a 400 that named a keyword the caller never wrote. The transport strips them into a copy — never in place, since the caller's schema is reused across providers and a Cerebras call must not quietly strip annotations from the next OpenAI one. These are annotations on top of a type, never the type itself. `TestLiveCerebrasHandlesATimestampField` confirms the stripped schema round-trips a timestamp. |
| **CB-03** | `f745d66` | **New, and the one with teeth.** A 429 was reported as a plain formatted error, so the `Retry-After` that came with it was discarded and the retry loop fell back to a backoff that doubles from 500ms and **stops at five seconds**. Against a provider that limits *per minute* — Cerebras' free tier is 5 req/min and answers `Retry-After: 53` — every attempt by construction landed inside the same closed window, so the retry budget bought nothing but latency before failing anyway. `llm.RateLimitError` carries the server's wait (both RFC forms, bounded at two minutes, caller's deadline still wins) and `nextRetryDelay` prefers it. Also fixed a latent bug the typed check exposed: `isRetryableLLMError` matches the *whole message including the vendor's body*, so a 429 whose body mentions `invalid_request_error` was classified permanent. Rate limits are now retryable by type. Applies to the OpenAI Responses path too. |
| **CB-04** | `f745d66` | **New.** `providerModels["cerebras"]` mapped to `llama-3.3-70b`/`llama3.1-8b` — written from a docs page, never called. Now `gemma-4-31b` at all three tiers, and the choice is stated rather than assumed: there is no cheaper Cerebras sibling whose accuracy loss buys anything, so mapping `Quick` to a smaller model would be inventing a trade-off no benchmark has shown. Priced at $0.99/$1.49 per 1M, because an unpriced model reports **zero cost** and a secondary provider whose whole appeal is a cheaper number has to produce a real one. `.env`'s bare `CEREBRAS` spelling is accepted for the same reason bare `OPENAI` is. **Verified live:** all three tiers callable, cost accounting moves. |
| **P-013/P-011/P-008** | `a462106` | **The capability matrix, measured.** `supportsTemperature` and `supportsReasoningControls` pattern-matched on model-name prefixes — guesses about a family that did not exist when the rule was written — and these parameters are not negotiated: an unaccepted one fails the *whole* request with a 400, so a wrong guess is not a degraded call, it is no call at all. `.audit/live/capabilities.py` probes each parameter against each model one at a time. Findings: **temperature is rejected by all three**, including zero (the existing rule was right, now for a reason); **`json_object` is rejected unless the input contains the word "json"** (the library's padding is correct, now verified); **`json_schema` strict works on all three**; **`prompt_cache_key` and `store: false` are accepted**; and **prompt caching reports 2419/2422 cached input tokens on an identical second call**. The landmine: `reasoning.effort: "minimal"` — what `reasoningEffort()` returned for everything that was not gpt-5.4 — is **rejected by the entire 5.6 family** (accepted: none/low/medium/high/xhigh/max). `supportsReasoningControls` returning false was the only thing standing between that function and a 400 on every request. `reasoningEffort` now returns a value each family accepts, or `""` to omit, and the caller honours `""`. |
| **P-011 (evidence)** | `a462106` | **Whether to send the reasoning block at all, measured rather than assumed.** `.audit/live/bench3.py`, four runs per arm on a proration with a distractor: `omitted` 4/4 on all three models; **`effort: none` 0/4 on luna and 0/4 on sol** — answers of 650, 600, 2600 against a correct 500 — while terra survives at 4/4; `effort: low` 4/4 everywhere but faster than omitting nowhere that matters. So the block buys nothing and can cost everything, and omitting it for the 5.6 family is the right default *because it was measured*, not because the accepted values were unknown. The comment in `provider.go` now carries the table. |
| **P-008** | `a462106` | **`store: false` by default.** The Responses API retains responses server-side unless told otherwise. For a library whose job is running arbitrary user records — invoices, tickets, notes — through a model, that is a surprising thing to leave on: the caller opted into an extraction, not into retention. The zero value of `ProviderConfig.Store` is the private one, so a caller who never thinks about it gets retention off. `Store(true)` is opt-in and plumbed through `WithProviderConfig`. Accepted live by all three models. |
| **Gap-12/X-03** | `ca9d236` | **P-007 closed.** A non-200 was a `fmt.Errorf` with the raw body pasted in, which failed twice over. *Recoverability:* a caller wanting to branch on "429 or 400" had to substring-match an English sentence while the status sat right there and was discarded. *Disclosure:* the body is not ours — a content filter names the passage it objected to, a validation error echoes the field it could not parse — and it went straight into the caller's logs. `*APIError{Provider, Model, StatusCode, Message, Type, Code, Param, Body}` fixes both, and `RateLimitError` now embeds it so `errors.As` recovers either. The redaction is split **by shape, not by caution**: what the vendor wrote *about* the request (message, capped at 400 chars; type; code; param) is printed, because withholding "exceeds maximum nesting depth" only moves the debugging cost onto the caller; the raw body is retained on the value and never printed. A body with nothing extractable says it was withheld rather than saying nothing, which would read as "the provider explained nothing". **Note:** the task's verify line said the default `Error()` should contain *no* body; this keeps the vendor's structured message, which is technically part of it. That is a deliberate departure — a library whose errors omit the vendor's reason is one users file bugs against — and `SCHEMAFLUX_ERROR_DETAIL=1` / `Detail()` restore the rest. |
| **X-03 (retry)** | `ca9d236` | **Found while closing P-007, and a real bug.** `isRetryableLLMError` matched substrings against the whole error message *including the vendor's body*, and the non-retryable list contains `invalid_request_error`, `unauthorized`, and `forbidden`. So a transient **500 whose body carried `invalid_request_error`**, or a **503 whose message said `unauthorized upstream`**, was classified permanent and the caller lost every retry they were entitled to. The decision now comes from `APIError.Retryable()` — the status, not the prose — which also survives the body being redacted out of the message. Substring matching remains as the fallback for errors that carry no status. Witnessed: three cases fail against the old classifier. |
| **B-01/B-04** | `9474687` | **Unblocked 2026-08-05: the account has credits.** `GET /v1/models` returns the gpt-5.6 family and `POST /v1/responses` returns 200. `live_provider_test.go` is the gate: six tests behind `SCHEMAFLUX_LIVE_TESTS=1`, skipped by a plain `go test ./...` so no run bills the operator by accident. All six pass against `gpt-5.6-luna`. |
| **P-012** | `9474687` | The provider's request is one the live Responses API accepts and its response is one this library parses, end to end: `Extract` returned `{Number:INV-4417 Total:1284.5 Vendor:Northwind Traders}` and `Validate` returned two correctly-attributed issues. The observed live response body is pinned as a fixture in `TestOpenAIResponsesParsesTheObservedLiveShape`, so the parser is checked against a real response rather than one we wrote to suit ourselves. |
| **P-013** | `9474687` | Measured, not assumed. `.audit/live/bench.py` and `bench2.py`, four runs each: terra 959ms/2050ms, sol 1594ms/3925ms, luna 1680ms/2094ms — **all three 4/4 correct on both tasks**. That supports one assignment and one only: `Quick` takes terra, fastest at no cost in accuracy. Smart and Fast stay on luna because nothing separated luna from sol, and sol was slowest on the harder task without being more accurate. See **P-017**. |

### Added during the work

- [ ] **OP-308** — Apply **OP-302**'s deterministic diff to `Pivot`, `Enrich`, and
  `Normalize`, which report the same model-authored `Lost` / `Inferred` / `Changes` audit
  trail that `Project` did. The helpers (`jsonFieldNamesOfValue`, `missingFrom`) already
  exist; what each one needs is a decision about what its diff *means* — a normalization's
  changed values are not a field-set difference, so it needs a value diff rather than a name
  diff, and that is why this is a separate task rather than a repeat of the same edit.
  *Verify:* per operation, a model that silently drops a field is contradicted by the result.

- [x] **CF-010** — **`MapReduce` dispatched chunks in random order, so `Concurrency: 1` was
  serialized but not sequential — and cancelling on the first failure saved nothing.** Found
  by a flaky test: `TestMapReduceStopsOnTheFirstFailure` failed roughly one run in ten, and
  under **CI-001**'s `-count=10` it would have failed most builds. The flake was real. The old
  implementation started one goroutine per chunk and had them race for a semaphore, so
  dispatch order was whatever the scheduler chose. Two consequences: `Concurrency: 1` — which
  the doc comment offers to callers whose provider is rate-limited — could run chunk 7 before
  chunk 0; and when the failing chunk happened to be scheduled last, every other chunk had
  already been paid for before `cancel()` did anything. The test was asserting the second one
  and only passed when the scheduler was kind.
  Replaced with a worker pool fed in index order. Chunks the feeder never reaches are marked
  cancelled rather than left with a nil error and a zero value, which would have reported them
  as successful empty chunks — the same fail-open this list exists to remove, one layer down.
  *Verify:* `TestMapReduceSequentialRunsChunksInOrder`, `TestMapReduceSequentialFailureStopsTheRest`
  (three failure positions, each asserting exactly `failAt+1` chunks ran),
  `TestMapReduceUnreachedChunksAreNotReportedAsSuccess`. The whole MapReduce set passes
  `-count=10`; the original test now passes deterministically rather than usually.

- [x] **OP-109** — Chunk the collection operations with a merge step, so a batch larger than the
  output budget succeeds rather than being refused. **OP-108** closed the half that matters for
  correctness — the call that cannot succeed now says so — but the useful behaviour is to split it.
  **Done `debaf6e` for `Filter`; `Sort` deliberately still refuses.** The merge is what varies:
  for a filter it is a concatenation, which the library knows; for a sort it is an interleave of
  two sorted runs, which it does not. `MapReduce` is exported so a caller can supply that merge.
  The original verify line — "a 500-item `Sort` at the Quick tier returns 500 items" — is
  therefore **not** met and was the wrong bar: a library-chosen sort merge would be guessing at
  the caller's ordering. Restated: *a 500-item `Filter` at the Quick tier returns a subset in
  input order*, which `TestIntegrationFilterChunksLargeCollections` drives through the public API.
- [ ] **X-07** — A type embedding both `CommonOptions` and `types.OpOptions` has two `Intelligence`
  fields and two `Mode` fields, and `mergeEmbeddedOpOptions` takes both from `CommonOptions`
  unconditionally — so `opts.OpOptions.Intelligence = Quick` is silently ignored, while
  `opts.OpOptions.Context` *is* honoured because Context falls back. A caller has no way to tell
  which fields fall back and which do not. Found while writing the C-06 guard tests, where the
  guard reported the wrong tier. Fold into **M06**'s uniform options work.
  *Verify:* one Intelligence and one Mode reachable per options type, or a documented precedence
  that is the same for every field.

- [ ] **X-06** — `CompleteOptions.Context` is a `[]string` and `InferOptions.Context` is a `string`:
  both mean prose context for the prompt and collide with the embedded
  `types.OpOptions.Context`, which is a `context.Context`. Found while threading X-01, where the
  collision produced a compile error rather than silence — but a caller reading the field list
  has no such warning. Rename the prose fields to `Background` or `Notes`.
  *Verify:* no options struct has two fields named Context reachable from the same selector.

- [x] **TEST-003** — A test used `types.OpOptions` as sample data with a hardcoded field count,
  so adding a field to a production type broke a test about counting fields. It owns its own
  struct now. (Filed as S-006 at first, which collided with the existing S-006 and silently
  un-traced **D-07** — the traceability check caught it on the next run.)

- [x] **OP-110** — *(Filed as OP-104, which was already taken by the open `Filter`
  single-object fallback task in M05. Renumbered here; the work below is unchanged.)*
  Both collection errors embedded the whole model response in their `Reason`
  (`"failed to parse: %v (response: %s)"`), which is **X-03** in two more places: the response is
  the caller's data reshaped, and every caller logs the error. Removed, and the cause is wrapped
  instead so `errors.Is` reaches it.

- [x] **TR-001** — Trace every finding in the adversarial review to the task that addresses it.
  The review is the input to this list, and that relationship is only worth anything if it is
  checkable: a finding with no task is a defect nobody scheduled. `.audit/traceability.py`
  parses both files and fails on an untraced finding. First run: **118 findings, 21 untraced,
  5 of them S1.** Most were scheduled but uncited — the citations are added. Six were genuinely
  unscheduled and are filed below.
  *Verify:* `python .audit/traceability.py` exits non-zero on an untraced finding.
- [x] **FL-001** — **I-03** is live: `GetModel` special-cased three providers and let everything
  else fall through to the OpenAI tier defaults, so `WithProvider("deepseek")` — the README's own
  example — sent `gpt-5.6-luna` to api.deepseek.com. Replaced with a per-provider model map
  (`internal/config/provider_models.go`) covering openrouter, cerebras, anthropic, deepseek, groq,
  mistral, and local. An unmapped provider now resolves to nothing and `CallLLM` errors naming the
  variable to set, rather than sending somebody else's model ID.
  *Verify:* `internal/config/provider_models_test.go` — no provider receives another provider's
  model across all three tiers, unmapped providers resolve to nothing, overrides rescue them.
- [x] **FL-002** — **F-02** is live: `directRequest` guards every setter with
  `if r.setX == nil { return r.lift(r.opts) }`, so a builder that forgets one gets a method that
  compiles, chains, and does nothing. `newAdversarialNegotiationRequest` wired five setters and not
  `setMode` — and `AdversarialOptions` had no `Mode` field at all, so `.Strict()` could never have
  worked. Field added, setter wired, and `internal/api/fluent/wiring_test.go` fails any builder
  that leaves a declared setter unwired. Found while there: the same merge tested
  `opts[0].Intelligence != 0`, which is **F-01** again — `Smart` is zero — and dropped `RequestID`
  and `CorrelationID` entirely.
  *Verify:* the wiring check was confirmed to FAIL with `setMode` removed.
- [ ] **FL-003** — **F-03**: eleven fluent entrypoints start from a zero-value options struct
  rather than the `NewXOptions()` constructor the direct API uses, so the two public APIs disagree
  on defaults and which one a caller gets depends on the spelling they chose. Route every
  entrypoint through the constructor.
  *Verify:* a test that constructs each operation both ways and asserts the resolved options are
  equal.
- [ ] **FL-004** — **F-04**: `Steer` assigns rather than appends, so two `.Steer(...)` calls
  silently drop the first, and the op then joins the caller's steering with its own generated
  clauses using `". "`, producing a run-on in which the library can contradict the caller.
  Accumulate steering, and keep the caller's text in its own block.
  *Verify:* two `.Steer` calls both reach the prompt; the caller's text is separable from the
  library's.
- [ ] **FL-005** — **F-05**: `commonRequest`, `opRequest`, and `directRequest` implement the same
  eleven methods three times, because the options structs behind them are inconsistent. Collapse to
  one base once **M06** has made the options structs uniform. Depends on M06.
- [ ] **FL-006** — **F-07**: builders validate nothing until `Run()`, so a mis-built request is
  discovered after the call is set up. Add `Validate()` to the builder bases and call it from
  `Run()` before any provider work.
  *Verify:* a builder with empty criteria reports the error without a provider being contacted.
- [ ] **FL-007** — **CF-09**: composition is linear and untyped. Fold into the **M08** combinator
  work rather than patching the current shape. Depends on CF-008.

- [ ] **P-017** — *(Filed as P-014, which was already taken by the open cassette task in M02.
  Renumbered here.)* Split `Smart` and `Fast` across the gpt-5.6 family, or record that they should
  not be split. The P-013 benchmark did not discriminate: all three models were 4/4 correct on
  both a typed extraction and a proration with a distractor, so the only measurable difference
  was latency. A discriminating task set is needed — long-context recall, multi-step tool
  reasoning, adversarial instruction-following — before Smart means anything other than Fast.
  *Verify:* a benchmark in `.audit/live/` where the models score differently, and a tier
  assignment justified by it in `config.go`.
- [x] **P-018** — *(Filed as P-015, which was already taken by the open per-provider model-map
  task in M02. Renumbered here.)* The live `usage.input_tokens_details` carries `cache_write_tokens` alongside
  `cached_tokens`, which the provider did not parse. They bill differently, so cost accounting
  that reads only one under-reports the first call of a cached prefix — the call that pays to
  build the cache. Found by inspecting a real response rather than the docs.
  *Verify:* `TestOpenAIResponsesParsesCacheWriteTokens` (5 cases).

- [x] **AUDIT-001** — The standard of done was asserted per commit but never checked across the
  whole list, so twelve closed tasks sat below the ten-case bar without anyone noticing.
  `.audit/audit.py` maps each closed cluster to the test functions covering it, counts the leaf
  cases those functions actually ran, and fails the ones below the bar. All 23 clusters clear it
  now; the smallest is 10 and the largest 254.
  *Verify:* `python .audit/audit.py` after a verbose test run, per `.audit/README.md`.
- [x] **F-039** — `WithProviderConfig` assigned `providerName` before building the provider, so
  a switch that failed left the client reporting the new provider's name while still running
  the old one — or the mock. Name and provider now move together or not at all, and the failure
  is recorded on `Client.Err()`, which is where a builder that returns `*Client` can put one.
  Found by the coverage backfill for F-029.
  *Verify:* `TestClientProviderSelection` (7 cases, each asserting name and provider agree),
  `TestFailedProviderSwitchIsReportedByErr`, `TestSuccessfulProviderSwitchClearsErr`.
- [x] **F-040** — Only `ExtractError` had `Unwrap`, so `errors.Is(err, ops.ErrNoProvider)` was
  false for every other operation even though the message was right there: callers had to
  string-match. All thirteen error types carry the cause and unwrap it now.
  *Verify:* `TestEveryOperationReportsErrNoProvider` drives eleven operations with no provider
  and asserts `errors.Is` reaches the sentinel through each one.

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

- [x] **TEST-002** — Second backfill, driven by `.audit/audit.py`: the provider parsing,
  retry policy, `Compose`, prompt determinism, fabricated numbers, client, `ErrNoProvider`,
  stub honesty, and registry clusters were all below the ten-case bar. Each is now at or above
  it, and the two defects the backfill found are recorded as **F-039** and **F-040**.
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
  **Done, by adopting staticcheck rather than writing a second checker** (`CI-001`). Its
  U1000 does exactly what this refinement asked for, and its first run found five dead
  helpers: `interfaceSlice` (collection.go), `unmappedHeaders` (parse.go — written for
  **OP-305** and never wired), `shouldRedactField` and `stringSliceContains` (redact.go —
  orphaned by **OP-501**'s rewrite), and `runeByteOffsets` (runes.go). All deleted. The
  hand-rolled `dead_options_test.go` stays: it checks a narrower property staticcheck does
  not — an exported field with a setter and no *reader*, which is live code as far as U1000
  is concerned.
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

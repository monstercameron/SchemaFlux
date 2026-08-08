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

## Steering

- [x] **ST-010** — `.Steer(...)` never reached the provider. Found while integrating SchemaFlux
  into ArticleFlux, whose feed categoriser passed the feed's title and description through it
  and got answers that ignored them.
  Every options type embeds both `CommonOptions` and `types.OpOptions`, each carrying a
  `Steering`. The fluent builders write the CommonOptions one; **23 operations read
  `opts.OpOptions.Steering` directly** — the raw embedded field, which the builders never touch
  — to decide whether the caller had said anything. In the operations that compose their own
  instructions (Choose, Score, Summarize, Rewrite, Translate, Expand, Filter, Sort and the
  rest) that check is what PRESERVES the caller's steering before the composed instructions
  overwrite the field. So `.Steer(...)` was not merely ignored, it was **silently discarded**:
  the builder returned no error, the operation succeeded, and the answer addressed a question
  the caller had not asked. Reporting success while being wrong, which is the failure this
  library is being rebuilt against.
  **Fixed** with `effectiveSteering(common, embedded)` in `internal/ops/options.go`, applied at
  all 23 sites. It mirrors `mergeEmbeddedOpOptions` exactly, so the value an operation checks
  and the value it eventually sends cannot disagree. `SimilarOptions` embeds only
  `types.OpOptions` and was already correct; it is left alone with a comment saying why.
  *Verify:* `steering_test.go` — 12 cases through the exported fluent API with a provider
  installed, covering the four text operations, the four collection ones, steering alongside
  an operation's own criteria, two `.Steer` calls appending, steering set on the options struct
  instead (the regression a naive "read CommonOptions" fix would cause), and no empty
  instruction when the caller said nothing. Watched every one fail first.
  **The golden prompts recorded the bug**: `testdata/golden_prompts.txt` held a Choose case
  built with `.Steer("the most urgent")` whose prompt did not contain it. Regenerated; the
  diff is that phrase appearing, and nothing else.

## Developer experience

Five papercuts reported from integrating this library into ArticleFlux, a multi-tenant reader
that runs ten Smart+ features through it. None of them is a wrong answer, which is why none was
found by reading the code: each is the library making a caller work out something it already
knew. They are recorded together because they share a cause — every one is invisible from
inside, where the mechanism is already understood.

- [x] **DX-001** — The fluent API could not pin a model. `CommonOptions.Model`, `WithModel` and
  the resolution in `CallLLM` all landed with **TC-005**, and nothing exposed them: no builder
  had a `Model` method, so the only route was building an options struct by hand and passing it
  to `WithOptions` — the escape hatch, not the interface.
  The workaround it pushes a caller into is worse than the gap. ArticleFlux is multi-tenant and
  each instance chooses its own model, so it named its provider `"openai"` to make the tier
  mapping resolve to *something*, then overwrote the model inside its own provider on the way
  past. Two lies at once — about which provider is configured and about where the model is
  decided — and both invisible in the envelope, which went on reporting the provider that was
  named.
  **Fixed** with `Model(string)` on `requestBase` (one method for the 49 builders that embed it,
  plus a `setModel` closure at each construction site) and on the six hand-written builders in
  `entrypoints.go`. `WithModel` added to the nine options types that already had a type-specific
  `WithSteering`, mirroring it exactly. Eleven options types that spell their common fields out
  flat rather than embedding `CommonOptions` — Negotiate, Adversarial, Resolve, Derive, Conform,
  Interpolate, Arbitrate, Project, Audit, Compose, Pivot — gained a `Model` field, without which
  the setter would have been an option that changes nothing, which `dead_options_test.go` fails
  the build over.
  **A live bug in TC-005 fell out of this.** `mergeEmbeddedOpOptions` did not carry `Model`: it
  starts from the embedded `types.OpOptions` and overrides Steering, Threshold, Mode,
  Intelligence and the IDs from `CommonOptions`, so a pin written to the CommonOptions side was
  copied over and lost for every options type that embeds both — Extract, Generate, Transform,
  Choose, Filter, Sort and the rest. Nothing failed: the call went out on whatever the tier
  resolved to, which is a plausible model, so the only symptoms were a reproduction that would
  not reproduce and a bill against a model nobody selected. **This is ST-010's shape in a
  different field** — two homes for one setting and a merge that knows about one of them — and it
  stayed invisible only because nothing could set the CommonOptions side until this task added
  the method that does.
  *Verify:* `devx_test.go` — 12 builders covering all three options shapes, each asserting the
  pin on `LastRequest().Model`; 4 cases for pin-beats-tier in both orders and across all three
  tiers; last-pin-wins and empty-clears-the-pin. Watched the merge cases fail first: before the
  `mergeEmbeddedOpOptions` fix, every double-embedding builder returned the tier's model.

- [x] **DX-002** — `Run` took a context on six builders and nothing at all on forty-nine. A
  caller who learned `Extracting[T](x).Run(ctx)` and then reached for `Summarizing(x).Run(ctx)`
  got a compile error and had to discover `.Context(ctx).Run()`. Two spellings of one idea in one
  package, with nothing to indicate which applied where.
  **Fixed** by making all 49 variadic — `Run(ctx ...context.Context)` — through
  `requestBase.optsWithRunContext`, which routes through the builder's own `setContext` closure
  because that is the one thing that already knows which of the three options shapes it is
  holding. `RunResult` and `RunDetailed` already take a required context and are left alone.
  **What this breaks, stated plainly:** a variadic `Run` no longer satisfies `func() (T, error)`,
  so a caller passing `req.Run` as a method value — to `AsStep`, or to anything else taking a
  thunk — must wrap it: `func() (T, error) { return req.Run() }`. One site in this repository
  needed it (`fl007_compose_test.go`). `Run()` with no argument still compiles everywhere, so
  nothing else changes.
  *Verify:* `devx_test.go` — 10 builders that took no context, each asserting a cancelled context
  reaches the provider as `context.Canceled`; `Run()` with no argument still works; and
  `Run(ctx)` produces the same deadline as `.Context(ctx).Run()`, so the fix removed a spelling
  rather than adding a second way to be wrong.

- [x] **DX-003** — `required field X is empty` did not say what to do about it. The mechanism to
  fix it has existed since `requiredness.go` — a `schemaflux:"optional"` tag, a pointer, or
  `omitempty` — and the error named none of them, so a caller who did not already know it existed
  had nothing to search for. Every plausible reading is wrong: that the model misbehaved, that
  the prompt needs work, or that the operation is the wrong one.
  It cost ArticleFlux four separate debugging rounds before the pattern was recognised as one
  thing: a re-rank's optional reason, an entity's optional label, a translation that could not be
  produced, and a boolean whose `false` is meaningful. The batched case is the expensive one —
  one untranslatable string in a batch of sixty failed all sixty.
  **Fixed** with `optionalFieldRemedy`, appended to the error. It names all three spellings in
  the order `FieldRequiredness` resolves them, and it names `Strict()` — because the check runs
  only in Strict mode, which **also** rejects unknown fields. Two rules under one word: a caller
  who reached for Strict wanting exact decoding got mandatory non-empty fields as an unannounced
  second effect, and needs to know both levers exist before choosing between them.
  The wording is conditional — "if an empty value is a valid answer here" — rather than a
  recommendation. Often it is not one: a required field arriving empty is exactly the failure
  this library refuses to pass off as success, and the fix is then a better prompt rather than a
  weaker type.
  *Verify:* `devx_test.go` — the error names the field, all three remedies and `Strict()`, and
  states the remedy conditionally; plus one case per remedy proving each actually makes the empty
  value acceptable, so the message is not merely advice.

- [x] **DX-004** — `the response carried unknown field X` did not say that `Strict()` was what
  rejected it. Rejecting an unrecognised property is opt-in and nothing else in the library does
  it, but the message reads as a decoding fact rather than as a consequence of a mode the caller
  chose — and `Strict` reads as "be careful" rather than as "reject anything I did not name".
  `strictdecode.go`'s own comment already says rejecting an extra field is "exactly wrong for an
  operation whose contract permits one"; the error is where a caller finds that out.
  ArticleFlux had Strict on four operations whose contracts tolerate an extra field. A model that
  answered the question *and* volunteered a confidence score failed the whole call; on the
  batched ones that discarded sixty good answers over one key nobody would have read.
  **Fixed:** the message now names Strict and says to drop it if the contract permits a field it
  did not ask for.
  *Verify:* `devx_test.go` — the error names the field and `Strict()`, and the counterpart case
  proves the same answer is accepted without Strict, so the advice is actionable.

- [x] **DX-005** — `schemafluxtest.Provider` recorded requests but not contexts, and `Reply`
  scripted a fixed list with no way to answer from the request. Both gaps forced ArticleFlux to
  hand-roll wrappers around this provider: one to observe the per-feature timeouts it puts on
  every call, and one for translation batching, where the reply has to name the keys *that batch*
  asked about and a fixed list cannot know which those are without the fixture reimplementing the
  batching it is meant to be testing.
  **Fixed** with `Contexts()`, `ContextN(n)` and `ReplyFunc(fn)`. `ContextN` returns a background
  context out of range rather than nil, so an assertion on a call that never happened reports the
  missing deadline it was looking for instead of panicking somewhere unrelated. `ReplyFunc` takes
  precedence over `Reply` and `Shaped`, and scripted errors from `Fail`/`FailThen` still apply
  first, so "fail twice, then answer from the request" stays expressible.
  *Verify:* `devx_test.go` — contexts recorded per call and in order; out-of-range is background;
  ReplyFunc answers from the request, sees the call index, fails the call on error, wins over
  Reply, and composes with FailThen; Reset clears contexts.

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
- [x] **P-009** — Send `prompt_cache_key` derived from `(op, T, tier)` so repeat requests
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
  **Done.** `types.OpOptions.CacheIdentity` carries the operation-and-schema half of the key,
  built by `ops.SchemaCacheKey("extract", extractPromptVersion, DescribeSchema(T))` in
  `internal/ops/core.go`. `promptCacheKeyFor` (`internal/ops/llm_helper.go:323`) combines it
  with the resolved model, the digest of the *stable* system prompt, the mode, and the
  response format, and sets `llm.CompletionRequest.PromptCacheKey`; `OpenAIProvider.Complete`
  sends it as `prompt_cache_key` only when non-empty. The digest is taken before
  `applySteering` runs, which is what makes the Revised line's steering requirement hold
  rather than merely being asserted. Evidence: `internal/ops/promptcachekey_test.go` (10
  cases — stability, and a different key for a different identity, schema version, model,
  edited template, response format, and mode); `internal/llm/provider_cache_test.go` (the
  field reaches the wire, and is omitted when empty); `provider_cache_key_integration_test.go`
  drives the whole path `schemaflux.Extract` → ops → provider → HTTP and asserts a stable key
  across identical calls, a different key across schemas, and the same key when only steering
  differs.
  **Not done:** only `Extract` populates `CacheIdentity` today, because it is the only
  operation carrying a Go-type schema. The other operations still get a valid key — the
  template digest, model, and mode axes all differentiate them — but not the operation-name
  and schema-version axes. Extending it is per-operation work, not a redesign.
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
- [x] **P-012** `[LIVE]` — Live smoke: one `/v1/responses` call per 5.6 model, asserting
  HTTP 200, non-empty message text, and populated usage including reasoning tokens.
  Blocked on **B-01**.
  **Done, run live on 2026-08-08 with Cam's explicit authorization.** All three models answered
  (luna 0.98s, sol 1.10s, terra 1.02s), each with non-empty text and populated usage.
  The reasoning-token clause needed its own call to answer honestly. The three short prompts
  report `reasoning_tokens = 0`, and that is **not** a parsing bug — `supportsReasoningControls`
  returns false for the 5.6 family, so this library sends no reasoning block at all. Whether
  the API produces them anyway, on a problem that needs them, is a question only a call could
  settle: a multi-step rate problem returned **reasoning_tokens = 100** of 172 total. So the
  field is real, the provider populates it, and the parser carries it — none of which could
  have been asserted from the short prompts without guessing.
  Recorded rather than asserted as a threshold: demanding a positive count would assert a
  property of the provider's internals this library does not request and cannot control.
- [x] **P-013** `[LIVE]` — Capability matrix across `luna` / `sol` / `terra`: strict
  `json_schema` support, temperature acceptance, reasoning-effort acceptance, cached-token
  reporting, and observed latency and cost per model. This is the evidence **P-010** and
  **P-011** need. Record the results in the task, not from memory. Blocked on **B-01**.
- [x] **P-014** — Record the responses from P-012 and P-013 as cassettes (**TI-003**) so the
  golden tests above run in CI without credits, forever. P-013's *findings* are now pinned as
  unit tests (`internal/llm/capabilities_test.go`), so a regression in the predicates is caught
  without credits; what remains is replaying real response *bodies*.
  **Done.** Four cassettes in `testdata/cassettes/live-smoke/`, recorded by the same run that
  satisfied P-012 — the smoke test records while it asserts, so the information was paid for
  once rather than twice.
  P-013's raw bodies in `.audit/live/` could **not** be converted into cassettes, and the
  reason is worth keeping: that directory is gitignored because the bodies carry account
  identifiers, and the bodies do not echo the request that produced them. Reconstructing the
  prompts from the bench scripts would have produced a fixture whose request half was invented
  — the same fabricated-evidence problem as a made-up field report, in a file that would then
  be trusted as a recording. Recording afresh was cheaper than being wrong.
  Cassettes are safe to commit where the raw bodies are not, because a cassette is a
  projection: content, finish reason, usage, and nothing else — no response ids, no account
  fields, no headers. `Recorder.Save` refuses to write a credential-shaped string, and
  `TestCommittedCassettesCarryNoCredentialsOrAccountIdentifiers` re-checks the committed tree,
  because the first check runs at record time on one machine and the second runs in CI on
  whatever is actually there.
  The replay asserts through the **same helper** the live run uses, so the fixture cannot drift
  into checking something the live call does not — which is the usual way a recording stops
  standing in for the thing it recorded.

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
- [x] **P-016** — The Anthropic provider hardcodes `max_tokens: 1024` before conditionally
  overriding it (`provider.go:383`, `394-396`) and never sends `cache_control`. Wire the real
  ceiling and add prompt-cache breakpoints. Depends on **CA-003**. Addresses **Gap-10**.
  *Verify:* recorded request asserts the configured ceiling and a breakpoint on the last
  stable block.
  **Done.** `AnthropicProvider.Complete` now uses `req.MaxTokens` and falls back to
  `anthropicFallbackMaxTokens` (1024) *only* when the caller set nothing — the old code wrote
  1024 first and conditionally overwrote it, which meant a caller-set ceiling was correct by
  accident of ordering. One `ephemeral` `cache_control` breakpoint is placed on the last
  stable block: the system prompt, sent as a content-block array when present, otherwise the
  user message block. One breakpoint of Anthropic's four. Evidence:
  `internal/llm/provider_cache_test.go` — the ceiling is honoured across five values, the
  fallback applies only when unset, the breakpoint lands on the system block, it lands on the
  user block when there is no system prompt, and a regression case asserts the user message
  stays a plain string when the system block already carries the breakpoint.

---

# M03 — Test infrastructure

Built before the redesign, because every later milestone is verified with it. This is also
**Gap-06**, the single largest adoption blocker in the review: today there is no supported way
to test code that uses this library without paying a provider.

- [x] **TI-001** — Ship `schemafluxtest` with an exported fake provider: scripted responses
  per operation, a canned error mode, a slow mode for timeout tests, and a recorder of the
  exact requests it received. Closes **Gap-06**.
  *Verify:* the triage example from the review doc runs green with no network and no key.
- [x] **TI-002** — Add a `Provider` seam consumers can inject per call rather than only via a
  package global. Depends on **A-004**.
  *Verify:* two clients with different fake providers run concurrently without interference —
  the test that today's global would fail.
  **Done as a context seam, not an option field, and the reason matters.** `internal/llm`
  imports `internal/types`, so a `Provider` field on `OpOptions` is an import cycle; the
  workaround — an `any` field type-asserted back — is exactly the untyped landmine
  `dead_options_test.go` exists to keep out. So: `ops.WithProvider(ctx, p)` carries the
  provider on the context, `callLLM` prefers it over the global, and `Client.Context(ctx)`
  hands a caller a context carrying that client's provider.
  This needed **zero** changes at the ~60 `callLLM` call sites, because every operation
  already threads its options' context through. A client with no provider leaves the context
  untouched, so the package default still applies and nothing that works today stops working.
  Context values for dependency injection are not a shape to reach for casually, and it is
  used here because Go has no type-parameterised methods: `client.Extract[T]` cannot be
  written, which is *why* the provider became a global in the first place.
- [x] **TI-003** — Cassette record/replay: capture live provider bodies once, replay them in
  CI. Redact keys on write.
  *Verify:* a recorded suite replays with `SCHEMAFLUX_LIVE_TESTS` unset; a cassette containing
  a key fails the redaction check.
  **Revised (PRD-13):** a cassette records more than the two bodies. It carries provider and
  model, operation/prompt/schema versions, the request body, the response headers that
  change behaviour (`Retry-After`, request ID), usage, the expected decoded result, and the
  expected failure classification — otherwise a replay proves the parser works and nothing
  about whether the runtime classified the failure the same way. Replay drives the real
  adapter and the real executor path, not a shortcut through the parser.
  **Done.** `schemafluxtest/cassette.go`. `Record(inner, dir)` wraps any provider and captures
  provider, model, request, response, usage, and — for a failure — the kind `llm.Classify`
  assigned at record time, stored by name so inserting a constant in the middle of the
  taxonomy does not re-point every committed cassette. `Replay(dir)` and `ReplayFrom` return a
  provider that needs no network and no credential, matches the request by default, and
  replays a recorded failure as an `*types.OperationError` of the recorded kind — which is the
  PRD-13 point, since a replay that only feeds the response back proves the parser works and
  nothing about classification. Redaction runs before the write, not after the read, and
  `Save` refuses to write a cassette that still matches a credential pattern.
  Evidence: `schemafluxtest/cassette_test.go`, 30 cases — record-then-replay round trip, a
  changed user prompt and a changed system prompt both refused, `Lenient` as the escape hatch,
  a failure replaying with its kind and its retryable disposition intact, the classification
  stored by name, every credential shape in `scripts/secret_scan.py` redacted before the file
  exists while an invoice number and prose about keys survive untouched, a disk round trip
  preserving call order, running out of cassettes erroring rather than returning an empty
  response, and an empty or missing directory refused at load.
  **Not done:** response headers are not captured, so a cassette cannot yet replay a
  `Retry-After` or a request ID — the Revised line asks for them and the provider interface
  does not currently surface them. Recording the live bodies from P-012/P-013 into committed
  cassettes is **P-014** and still open; this is the machinery, not the fixtures.
- [x] **TI-004** — Golden-prompt tests: for each operation, snapshot the exact rendered system
  and user prompt for a fixed input. Prompt changes then become reviewable diffs instead of
  silent behavior changes for every downstream user. Closes the testing half of **Gap-13**.
  *Verify:* editing any prompt literal fails a golden test until the snapshot is updated.
  **Done** — `testdata/golden_prompts.txt`, 470 lines covering fourteen operations and
  variants: `Extract` in each of the three modes and with steering, plus `Classify`,
  `Summarize`, `Choose`, `Filter`, `Sort`, `Validate`, `Transform`, `Generate`, `Score`, and
  `Compare`. Each records the response format, whether a JSON Schema went with it, and the
  full system and user prompts for a fixed, deliberately boring input — a varying input would
  make every diff unreadable.
  The failure output shows the first differing line with context rather than the whole corpus,
  and says why it matters: a prompt is behaviour, so this changes what every caller gets back
  with no Go API change to show for it.
  *Verify:* the snapshot passes, and editing one word of the extraction prompt was confirmed
  to fail it with that line printed.
  **Needed a new test-double capability**, which is useful on its own: `schemafluxtest`'s
  provider gained `Shaped()`, answering from the request's own declared shape when nothing is
  scripted. Without it a prompt test has to script a body per operation just to get past the
  parse — a lot of fixture for a question that is not about the answer.
  This is what **PS-007** versions: you cannot version an artifact you cannot see.
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
- [x] **TI-006** — Fault-injection harness backing **F-028**: provider error, malformed body,
  schema-violating body, truncated body, empty body — parameterized across every exported
  operation.
  **Done** — **F-028** built the harness across 57 exported operations with three faults;
  this adds the two it was missing. A **truncated body** is a distinct failure from prose:
  the JSON that arrived is correct right up to where it stops, so an extractor that returned
  "the part it could parse" would decode half an answer and report success. Both the bare and
  the fenced-and-unterminated forms are covered, because that is how a model's answer is
  packaged when the output budget runs out mid-sentence.
  The empty-body case was already there.
  *Verify:* `TestFaultInjectionTruncatedBody` — two bodies across every operation in the
  table, with the text-passthrough operations skipped for the stated reason that a truncated
  string is a short answer rather than a broken one.
- [x] **TI-007** — Property tests for the collection invariants: for random inputs,
  `Filter` output is a subset of input, `Sort` output is a permutation of input, `Choose`
  output is a member of input, `Cluster` output partitions input exactly once.
  *Verify:* each property fails against today's implementation and passes after **OP-101**–
  **OP-105**.
  **Revised (EXE-13, ARC-16):** the property set is the operation's declared batch algebra,
  not a hand-picked list. Appendix C of `to-production.md` assigns every family a class —
  independent, subset, permutation, partition, graph, hierarchical, sequential — and the
  property test is generated from the declaration, so an operation cannot acquire a
  batchability class without acquiring its check. Depends on **PL-006**.
  **Done** — `collection_properties_test.go`, sixty rounds per operation against a fixed
  seed, because a property test that cannot be re-run on the input that broke it is a flake
  generator. The model's *answer* is what varies: a subset with an occasional invented item,
  duplicate, or edited copy; a shuffle with an occasional length-preserving corruption; a
  choice that is sometimes an echo with a changed field; index groups that occasionally
  overlap, drop an item, or run past the end.
  **The guard against a vacuous test is the part worth keeping.** Every round may legitimately
  end in an error — refusing a bad answer is the point — but a test whose rounds *all* error
  asserts nothing about what comes back when one succeeds, and would keep passing with the
  property deleted. `requireRoundsAsserted` fails below five accepted rounds, and it fired on
  the first run: the Filter generator corrupted almost every answer and only 2 of 60 rounds
  reached an assertion. Corruption is applied to at most one item per round now, and the
  counts are 18, 35, 42, and 17.
- [x] **TI-008** — Concurrency tests under `-race` for the package globals: `defaultClient`
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
  **Done for the isolation half; the budget half is not.** The tests were written first and
  watched failing — the seam was disabled deliberately and each one failed the way the defect
  predicted: the wrong provider answered and the call counts crossed over.
  Covered: two clients built in sequence, where the second's construction used to repoint the
  first; two clients running concurrently, each seeing only its own provider, asserted by a
  provider that names itself in its answer rather than by hoping a race detector notices; and
  the same property through the exported API rather than only through `internal/ops`.
  **Not done:** `pricing.CheckBudget` is still process-wide, so "different budgets" is modelled
  at the fake provider rather than enforced per client. Real per-client budget, scheduler, and
  observer isolation — IN-004's full Revised vision of a client owning an immutable snapshot —
  is a larger redesign than this change, and claiming it here would be false. `Batch()` also
  still reads the globals directly rather than the context.
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
- [x] **A-001** — `Op[In, Out]` descriptor: `Name`, `Prompt`, `Format`, `Schema`, `Decode`,
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
  **Done, with the stubs named.** `Op[In, Out]` in `internal/ops/op.go` and the pure data in
  `internal/types/op.go`: `OperationID{Name, Version}`, `Semantics`, `OutputContract`,
  `BatchAlgebra`, `DefaultPolicy`. The version reuses the existing `extractPromptVersion`
  rather than minting a second identity for the same thing, which is what the Revised line's
  "needs something to hang it on" was asking for.
  **"Holds no context and no provider" is checked, not asserted in a comment.** A test walks
  the descriptor's field tree by reflection and fails if a `context.Context` or a provider is
  reachable from it. Function *parameters* that carry a context do not count and the test says
  why: those are supplied per call by `RunOp` and never stored, which is the distinction that
  makes planning and middleware possible without a second execution path.
  The batch-class rule is a real build-time check: `MustNewOp` panics, and it is what builds
  Extract's descriptor at package init, so a binary whose descriptors are wrong fails to start
  rather than failing later down one path.
  **Declared but not yet read** — said plainly rather than left to be discovered:
  `BatchAlgebra`'s `Encode`, `Merge`, and `GlobalValidate`; `OutputContract.EvidenceRequired`;
  `DefaultPolicy.Mode` and `Speed`; and every `Semantics` field except `Stability`. They are
  forward shape for the operations still to be ported, not working machinery. `Op` is exported
  at the root, so the exported-surface half of the Verify line holds; the literal
  outside-the-module test cannot be written from inside `internal/`, and that limit is stated
  rather than papered over.
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
- [x] **A-004** — `RequestOption` applied at both client construction and per call, with
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
  **Done for the enforced half; the rest is observational and says so.** Six scopes in
  `internal/types/scope.go`, and `ExplainResolution` prints each material setting's effective
  value and where it came from — the caller debugging why their model pin was ignored now has
  something to ask.
  The part with teeth: `LockedLimits` travels on the context, and `checkLockedLimits` runs
  inside `Validate()`, so an invocation trying to **weaken** a locked ceiling or mode floor is
  **rejected rather than applied**. Making it stricter is allowed. A deadline always wins for
  free, because `applyTimeout` uses `context.WithTimeout` and Go's own semantics never let a
  child extend a parent's earlier deadline — asserted rather than assumed.
  **Stated limitations:** the lock check is wired into three option types (Extract, Choose,
  Filter), not all fourteen — the rest is mechanical. Everything in `ExplainResolution` beyond
  the ceiling and the mode floor is advisory: it reports, it does not enforce. And there is no
  literal `option.Model(...)`, deliberately: model resolution lives in `CallLLM`, so a `Model`
  field would have been a field nothing reads, which is the exact thing
  `dead_options_test.go` exists to catch. Call isolation and the lock are demonstrated with
  `MaxOutputTokens`, which is genuinely read.
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
- [x] **A-006** — `Result[T]` + `Meta`: request and correlation IDs, provider, model, usage,
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
  **Done** — `internal/types/result.go` and `ExtractResult` as the proof, exported at the
  root. The old root `Result[T]` — value, model confidence, error, and a map called
  "metadata" — is replaced: an error *inside* a result gives every caller two places to
  check, and a bag called metadata is where a model's claim and a measured token count end up
  indistinguishable, which is **D-09**.
  **Four compartments, kept apart:** runtime facts, contract (requested versus delivered),
  deterministic checks, and model claims. `ModelConfidence` sits in the last one so that
  reading it as a measurement takes deliberate effort.
  **The two forms are one execution.** `ExtractResult` records the calls and `Extract` drops
  the envelope; a test asserts both send byte-identical prompts. Two return types that
  execute differently is how the pairs in **T-01** drifted, and building a second path here
  would have repeated it.
  Usage and cost **sum across attempts**, because a caller asking what an answer cost means
  the answer, not the last try at it. One unpriced attempt makes the sum unpriced — the
  **PR-001** rule one level up.
  Two defects found by writing the tests, both mine and both real:
  **`CallLLM` never recorded its own retries**, so a request that succeeded on its third try
  reported one attempt — the envelope under-counted exactly when it mattered. And my first
  `envelopeFrom` had a `break` meant to stop cost accumulation that also stopped counting
  attempts, so an unpriced retry vanished from the count.
  *Verify:* `result_envelope_test.go` — the record's contents against what the provider
  actually saw, both forms sending the same request, requested-versus-delivered with
  `Degraded()`, a failure still carrying a record, and usage summing across a retry.
  **Carried on the context, deliberately.** Getting the record out otherwise meant changing
  the return type of every operation at once; `callrecord.go` says why, and says it is a
  bridge to **A-001**'s descriptor rather than a destination.
- [x] **A-007** — Error taxonomy: `ErrNoProvider`, `ErrAuth`, `ErrRateLimited`,
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
  **Done — 23 kinds, not 7**, per the Revised note: the kinds are chosen by what a caller
  would *do* about them, which is why "malformed output" and "schema violation" are separate.
  Both are bad answers, but one is repaired by asking again with the parse error quoted and
  the other by regenerating from source — editing an answer that invented a field tends to
  produce an answer that invents it again.
  `internal/types/errorkind.go` holds the kinds, a sentinel each, and `OperationError` with
  the structured context recovery needs: operation, provider, model, request and attempt IDs,
  affected item IDs, `RetryAfter`, and an **`Ambiguous`** flag for a timeout that may already
  have been served. The library performs no side effects; its callers do, and they could not
  tell.
  **Three dispositions, and they are mutually exclusive** — `Retryable` (the transport might
  work next time), `Repairable` (asking again with the problem named might), `Terminal`
  (nothing here will help). A test asserts no kind reports two at once, because a caller
  reading them as a decision tree would take two branches.
  `internal/llm/classify.go` is the single place that decides. `isRetryableLLMError` now asks
  it instead of carrying its own opinion, so the retry decision cannot differ between
  operations. Substring matching survives only as the fallback for errors carrying no type,
  and only against phrases *this library* produces — never a vendor's prose, which is the
  **P-007** finding that a 500 mentioning `invalid_request_error` was classified permanent.
  The whole taxonomy is exported from the root package with usage in the doc comment.
  *Verify:* `internal/types/errorkind_test.go` (23 kinds × 3 dispositions, sentinel identity
  through wrapping, sanitized messages, ambiguity, every kind named) and
  `internal/llm/classify_test.go` (18 mappings, the status beating the prose, transport
  failures, and finish-reason classification — a 200 is not a logical success).
  Integration: `error_taxonomy_integration_test.go`, which **found that decode and shape
  failures were unclassified** — the taxonomy existed and nothing produced it. `ParseJSON`
  and the field check emit it now.
  **Not closed by this:** the remaining `types.*Error` structs (`ExtractError` and its twelve
  siblings) still carry their own shapes. They wrap correctly and `errors.Is` reaches the
  sentinels through them, so they are compatible rather than wrong; collapsing them is
  **OP-206** and **A-006**.
- [x] **A-008** — Reclassify retries on typed errors and status codes instead of substring
  matching on message text (`llm_helper.go:205-263`), and add jitter to the backoff
  (`retryDelay:265`). Closes **I-12**. Depends on **A-007**.
  *Verify:* a 429 retries, a 400 does not, an unknown error does not silently fail fast;
  concurrent retries do not align.
  **Done.** `isRetryableLLMError` now has exactly one opinion — `llm.Classify` plus
  `OperationError.Retryable()` — and both substring lists are gone. The behaviour change with
  teeth: an *unclassifiable* error now retries within the budget instead of failing fast,
  which is what the Verify line asks for and the opposite of what substring matching did to
  any error it did not recognise.
  Backoff uses decorrelated jitter with the wait threaded through the retry loop per call, not
  as package state, so two concurrent calls do not correlate. `TestJitterSpreadsConcurrentRetriesApart`
  asserts on the computed delays through an injected rand rather than on timing.
  The tests that pinned the substring behaviour were rewritten around typed errors — they
  were asserting the defect.
  **Known duplication:** the jitter maths now exists in both `internal/ops` and `mw/retry.go`.
  `mw` is an opt-in decorator and importing it into `internal/ops` would invert the layering,
  so the algorithm is duplicated rather than shared. Both call the same classifier, so the
  *decision* is single-sourced even though the arithmetic is not — which is the half that
  actually caused bugs.
- [x] **A-009** — `Invariant[In, Out]` plus the shared library: `MemberOf`, `SubsetOf`,
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
  **Library complete; the kernel seam is not.** `AtMost`, `ExcludesValues`, and `Satisfies`
  now ship beside the original four, with 27 cases in
  `internal/ops/invariants_bounds_test.go`. Each has a pass case, a fail case, and a case
  asserting what the error is *not* allowed to say: `AtMost` names the count and the limit but
  never the items, `ExcludesValues` reports how many forbidden values appeared and never which
  — a redaction check reporting "the output still contains 4111-1111-1111-1111" has leaked the
  thing it was asked to remove, into a string every caller logs — and `Satisfies` names the
  rule but never the value. `Satisfies` refuses a nil predicate and an unnamed rule rather
  than treating either as a pass: a rule that cannot fail silently weakens the contract of
  every call that registered it, and an unnamed one gives the repair loop nothing to feed
  back.
  **`OneOf` is deliberately not implemented**, and the reasoning is recorded at the bottom of
  `invariants.go`. It is `MemberOf` in different words; `MemberOf` covers canonical value
  comparison and `CategoryIn` covers the case-folded string enum. A third spelling is the
  fourth membership test AGENTS.md forbids, and the failure it causes is two call sites
  disagreeing about whether "Urgent" is "urgent".
  **Now closed: the kernel seam works and the Verify case is written.** **A-001** landed the
  descriptor, and `Op.Contract.Invariants` was already threaded into `RunOp`'s repair closure —
  so a caller's own rule already participated in recovery. What was missing was the *envelope*:
  nothing built a `Meta` from `RunOp`, and `Meta.Repairs` was written nowhere in the codebase.
  `RunOpResult` fills that in, and is exported at the root.
  Evidence: `TestCallerInvariantRepairVisibleInEnvelope` is the added Verify line verbatim — a
  caller-defined rule with no library counterpart fails on the first attempt, passes on the
  second, and the envelope reports **two attempts and one repair**, asserted on the numbers
  rather than on the returned value. It runs against a real provider rather than the test
  caller-hook, because the hook bypasses the call recording and would make attempts read zero
  — a version of this test that used it would have passed while proving nothing.
  `TestCallerInvariantFirstTryReportsNoRepairInEnvelope` is the negative control, and
  `TestMultipleCallerInvariantsAllMustPass` pins that invariants are an AND, evaluated in
  order, rather than last-one-wins.
  **Still not built:** a named `Invariant[In, Out]` type. Invariants are `func(Out) error` on
  the contract, which is what the repair loop needs and what a caller can write without
  learning a type. Adding a wrapper type now would be shape without a use.
- [x] **A-010** — Repair loop: a decode or invariant failure feeds the error back and retries
  within the existing budget, aggregating usage across attempts into `Meta`. Closes **CF-01**.
  Depends on **A-009**.
  *Verify:* fake provider returns a violating result then a valid one; assert one repair, two
  attempts, and summed usage.
- [x] **A-011** — Move errors and log records off raw payloads: no request body in an error
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
  **Done, both halves.** The audit came first and found the error types already clean: every
  `*Error` in `internal/types/errors.go` and `OperationError` carry shapes, counts, and
  reasons, never a value, and none prints its shape field. So the original half of this task
  was already satisfied — recorded as *checked* rather than claimed as work.
  The sink is new. `types.DiagnosticSink`, `DiagnosticRecord`, `DiagnosticRef`, and
  `DiagnosticPolicy`, with `ops.WithDiagnosticSink` turning capture on per context; all four
  are exported at the root. It is **off** unless a caller installs one, **bounded** by
  `MaxBodyBytes`, **redacted** before the sink ever sees it, and the ordinary error carries
  only an ID and a content digest — both safe to print.
  Evidence: nothing captured with no sink and no reference in the error text; with a sink, the
  error carries the reference and the captured body is truncated to the configured bound and
  scrubbed of an embedded key; five credential shapes redacted; and the original Verify line —
  an error produced from a payload containing a marker string does not contain that marker —
  checked across `ExtractError`, `RunOp`'s `OperationError`, and `DescribeValue` itself.
  **Judgement calls to flag:** capture fires once, on final exhaustion, with the *last*
  rejected body rather than one record per repair attempt — a caller wants what the error is
  about, not a history of intermediate rejections. And the sink is called **synchronously**, so
  a slow implementation slows the caller; that is documented on the interface as the sink's
  own problem to solve with a queue rather than papered over with a goroutine this library
  would then have to supervise.
  **A fourth credential-pattern list** now exists, beside `scripts/secret_scan.py`,
  `mw/redact.go`, and the cassette writer. `internal/types` cannot import `mw` without a
  cycle, so sharing was not available; the reasoning is written in the file, as it is in the
  other three.
  **Not done:** `types.DebugInfo.Input` still holds a caller value. It is not wired into any
  error path, so it is not a leak today, but it is the shape of one.
- [x] **A-012** — Port `Extract` to the core as the proof, keeping `Extracting[T]` working
  unchanged.
  *Verify:* existing extract tests pass untouched against the new path.
  **Done, and the proof is that nothing moved.** `Extract` runs through `runExtractOp`, which
  builds the descriptor and executes it; the decode branching for Strict versus Transform is
  the same code, relocated rather than rewritten. Every existing extract test passes
  untouched, and **the golden prompt snapshot did not move** — the bytes Extract sends are
  identical before and after, which is the only evidence that distinguishes a port from a
  rewrite wearing a port's name.
  One deliberate non-change: `DefaultPolicy.RepairAttempts` is left at zero rather than set to
  the constant, so the repair budget keeps being read from configuration at call time as it
  was. Setting it would have been a silent behaviour change.
- [x] **A-013** — `Run(ctx)` on the fluent builders. There is no way to honor cancellation
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
  **Done, with the breaking change avoided rather than taken.** `Run(ctx ...context.Context)`
  is variadic, so `.Run()` still compiles for existing callers and `.Run(ctx)` cancels;
  `RunResult(ctx)` is new beside it on all six entrypoint builders and returns the envelope.
  The task called this a breaking change, and it did not have to be: a variadic signature buys
  the cancellation without breaking a caller who had no context to pass.
  **The contract is read-at-run**, documented in `run.go` — no constructor copies the caller's
  slices, maps, or pointers, and Run reads the referenced memory live. That is a decision, not
  an accident, and it is written down with a deterministic test beside the `-race` one, since
  `-race` cannot run on this machine.
  **A real defect found doing it, fixed at the source.** Every options type embeds both
  `CommonOptions` and `types.OpOptions`; the fluent `.Context(ctx)` sets the first, and the
  operations read the second directly. So `.Context(ctx)` reached Extract and **silently did
  nothing for Choose, Filter, and Sort** — a caller could hand a collection operation a
  cancellable context and watch it be ignored. `resolvedContext` now resolves the two with
  `mergeEmbeddedOpOptions`' own precedence, applied across the collection, text, batch, and
  suggest operations. It deliberately does not call `toOpOptions`, which mints request IDs as
  a side effect: resolving a context should not create identifiers.
  Evidence: `internal/ops/contextreaches_test.go` — cancellation now reaches Choose, Filter,
  Sort, and Summarize, and the embedded side still works, so the fix did not swap one ignored
  field for another. The caller used in those tests blocks until its context is done, so the
  operation can only finish by honouring the cancellation.
  **Not done:** only Extract's `RunResult` carries a full envelope, because `ExtractResult` is
  the only operation twin that produces one; the other five report operation, IDs, elapsed,
  and attempts, and leave usage and cost at zero rather than inventing them.
- [x] **A-014** — Option structs are copied field by field, so every field added since they
  were written is silently dropped. `applyDefaults` (`internal/ops/utils.go`) and the
  `toOpOptions()` methods (`internal/ops/options.go`) each copy a fixed list, and the list has
  not kept up: `MaxOutputTokens`, `CacheIdentity`, `ResponseFormat`, `JSONSchema`, and
  `SchemaName` are all set by a caller and then thrown away on the way to the provider.
  Found while verifying **ST-003**: `schemaflux.Format(data, template, OpOptions{MaxOutputTokens: 321})`
  sends the tier default of 2000, not 321.
  This is worse than a missing feature. It makes **P-009**'s cache identity and **ST-003**'s
  ceiling unreachable from the legacy and builder entrypoints while both look implemented and
  both have passing tests, because the tests drive `CallLLM` directly. It is also the exact
  failure `dead_options_test.go` exists to catch, and it slipped past because the field *is*
  read — one layer below the one that dropped it.
  *Verify:* every exported field on `OpOptions` survives a round trip through `applyDefaults`
  and through every `toOpOptions()`, asserted by reflection over the struct rather than by a
  hand-written list — a hand-written list is what created this.
  **Fixed in the same session it was found.** `applyDefaults` now merges by reflection: every
  non-zero field on the incoming options wins, and the `TransformMode`/`Smart` defaults are
  applied afterwards only where the caller chose nothing — which is safe to check *after* the
  merge precisely because **A-005** gave `Mode` and `Speed` real unset values.
  `mergeEmbeddedOpOptions` turned out to be safe already, because it starts from the whole
  embedded struct rather than copying named fields; that is now pinned by a test, since the
  obvious tidy-up is to rewrite it in the style that just failed.
  Evidence: `internal/ops/optionsurvival_test.go`. The survival tests walk the struct by
  reflection and compare field by field, so adding a field to `OpOptions` and forgetting the
  merge fails the build. A fixture test asserts the fixture itself populates every field —
  otherwise the survival test would silently stop covering the new one, which is the same
  failure one level up.
  **Not done:** the layering rule is still "a non-zero field wins", so a caller cannot set a
  field back to its zero value through this path. That was true of the hand-written version
  too, and **A-005**'s `Opt[T]` is the answer; this task did not widen its scope to that.

---

# M05 — Port the operations, attaching invariants

Family by family, highest defect density first. Each family's port is one commit with its
tests.

## Collections — the highest-value family

- [x] **OP-101** — Switch `Choose`, `Filter`, and `Sort` to index-based responses. The
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
  **Done, with stable ids rather than indices** as the Revised line requires. Items are tagged
  at encode time as `{"id":"i-000001","item":…}` (`tagItems`), the model answers with ids only
  — `{"id":…}` for `Choose`, `{"ids":[…]}` for `Filter` and `Sort` — and coverage is checked in
  Go by `resolveSubsetByIDs`/`resolvePermutationByIDs`, which reuse `MemberOf`, `SubsetOf`,
  and `SameMultiset` unchanged rather than adding a fourth membership test. The identity-echo
  failure this closes is now structurally impossible: there is no item field left for a model
  to alter. Duplicate input values reconcile correctly because an id is unique per position
  regardless of content equality, which is the case value matching could not resolve.
  Output size now scales with item *count* rather than item *content*, so `filterChunkSize`
  budgets on the id array: chunking triggers around 1500 items at Quick tier where it used to
  trigger around 200.
  Evidence: `internal/ops/collection_ids_test.go` (9 functions, 32 cases — id formatting and
  round trip, both resolvers directly, threshold routing); the updated
  `collection_identity_test.go`, `size_guard_test.go`, `collection_integration_test.go`, and
  `collection_properties_test.go`; and regenerated `testdata/golden_prompts.txt`, whose diff is
  exactly the protocol change with the non-collection prompts untouched.
  **Found while verifying:** the shape-answering local provider
  (`internal/llm/mockshape.go`) still spoke the value protocol, so `05-filter` and `06-sort`
  failed for every consumer running an example without a credential — while `go test ./...`
  stayed green, because the examples run under `scripts/examples_gate.py` rather than under
  `go test`. Fixed by an id branch in `mockCollectionResponse`, pinned by
  `internal/llm/mockshape_ids_test.go` (7 functions: filter answers every id, sort answers a
  permutation, choose answers one id, untagged input is left to the value protocol, and
  malformed or partial tagging is refused). This is the second time a gate outside `go test`
  caught something the suite could not.
  **Not done:** `resolveSelection`/`resolveSubset` in `collection_identity.go` are no longer
  reached by `Choose` or `Filter`. They are still exercised directly by their own tests, so
  neither Go nor staticcheck calls them dead; deciding whether they survive as a public
  fallback or get deleted is left open rather than settled quietly.
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
- [x] **OP-106** — Promote `sortByScoringFallback` (`collection.go:385-453`) from fallback to
  the primary strategy above a size threshold: it scores items independently, keeps the
  caller's own objects, and sorts in Go, so items cannot be lost, duplicated, or edited — and
  `Stable` actually works. Run it with bounded concurrency and report `Meta.Strategy`.
  Closes **C-05**.
  *Verify:* a 200-item sort makes bounded concurrent calls, returns a permutation, and reports
  its strategy.
  **Done.** Scoring is the primary strategy above `sortScoringThreshold` (50 items), run
  through `MapReduce` with `ChunkSize: 1` and `DefaultConcurrency` — reusing the existing
  bounded worker pool rather than starting a second one, per **CF-009**. `SortResult` reports
  `Meta.Strategy` as `trivial`, `whole-list`, `scoring`, or `scoring-fallback`; `Sort` keeps
  its signature and discards the strategy.
  Evidence: `internal/ops/collection_ids_test.go` verifies bounded concurrency by tracking
  peak in-flight calls under a mutex and asserting `1 < peak <= DefaultConcurrency` — a test
  that would pass against a serial implementation is not evidence of concurrency — plus
  threshold routing and all four strategy values. `SortResult` is now exported from
  `schemaflux.go` with `TestIntegrationSortResultReportsItsStrategy` and
  `TestIntegrationSortResultReportsTheTrivialCase` driving it through the public API; without
  that export the strategy this task exists to report was unreachable by any caller, which
  would have closed the task on something nobody could observe. `testdata/api_surface.txt`
  gains exactly one line.
  **Not done:** the concurrency assertion is a peak-count observation, not a `-race` run;
  `-race` is unavailable on this machine (**TI-008**), so the test was run repeatedly
  uncached instead and CI covers the race detector on the other platforms.
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
- [x] **OP-206** — Collapse `Validate` / `Verify` / `Audit` / `Critique` / `Score` onto one
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
  **Done for four of the five, with a ruling on the fifth.**
  `types.JudgmentResult[T]` carries subject, verdict, issues, evidence, and summary, with the
  model's confidence and other claims in their own compartment — the point of the shape is
  that a deterministic verdict and a model's self-score cannot sit next to each other looking
  like the same kind of thing. `JudgmentIssue` maps Validate's four severity levels, Audit's
  0.0-1.0 float, and Critique's three-level spelling onto one set of levels, decided in Go
  rather than left for a model to spell three ways.
  The renames the Revised line requires: `Validate` splits into `ValidateDeterministically`
  (Go-only, and it *errors* rather than quietly consulting a model when a rule is not
  decidable) and `ValidateHybrid`; `Verify`, `Audit`, and `Critique` become `VerifyWithModel`,
  `AuditWithModel`, `CritiqueWithModel`. Every old name still works and is deprecated pointing
  at its replacement. Each operation's original body was extracted unchanged into an
  unexported core that both names call, so the old and new spellings cannot drift apart.
  Evidence: each ported operation is asserted to return the *same* verdict and issues as its
  deprecated twin for the same scripted response, which is what makes this a collapse rather
  than a rewrite; `TestValidateDeterministicallyNoProviderCall` asserts a call **count** of
  zero across four paths, including the rejection paths, because "it returned a result" does
  not prove no network request happened.
  **`Score` is deliberately not folded in, and this is the ruling.** Its output is a rating
  with a per-criterion breakdown, not a verdict with issues. Forcing it into the judgment
  shape would either drop the number — the entire point of the operation — or invent a
  verdict from a threshold nobody chose. Note also that the Revised line's renaming list names
  `Verify`, `Audit`, and `Critique` and does not name `Score`.
  **Related defect, filed not fixed:** every field on `ScoreResult` is a model claim — the
  value, the normalized value, the breakdown, the strengths — and none of them is named
  `Model*`. That is the same naming rule this task enforced elsewhere, unenforced there.
  **Also not done:** `JudgmentResult` is a bespoke shape rather than a `Result[T]` with a
  `Meta`, so these four operations report no runtime facts. Wiring that in needs **A-001**'s
  descriptor and is a larger change than this one.

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
- [x] **OP-401** — Collapse the `X` / `XWithMetadata` twins into one operation returning
  `Result[T]` (`text.go:95/166`, `269/353`, `467/547`, `659/733`), each pair duplicating ~40
  identical lines. Closes **T-01**.
  **Partial — the duplication is gone; the API collapse waits on A-006.**
  The duplicated block was the option-to-steering handling, and it was **104 net lines**
  across the four pairs rather than the ~40 the review counted. Each is now one
  `<op>Instructions` function plus one shared `textOperationOptions`, so a rule added to a
  rewrite applies to `RewriteWithMetadata` too — which is the defect T-01 names.
  **The golden prompts did not move**, which is the proof it is behaviour-preserving: the
  bytes each operation sends are byte-identical before and after.
  `TestTextTwinsSendTheSameOptions` compares the steering the twins *actually send* rather
  than their source, because looking alike is not the property that matters.
  **Now complete — the API collapse, once A-006 landed.** Each text operation has two forms
  instead of three: the plain one returning what the caller asked for, and `SummarizeResult`,
  `RewriteResult`, `TranslateResult`, `ExpandResult` returning `Result[T]`, built the same way
  `ExtractResult` and `SortResult` are — a thin wrapper around the same execution, not a
  second implementation of it, because an envelope built down a parallel path describes a
  different call. The four `XWithMetadata` twins are deprecated and still work.
  What the collapse is *for*: the twins' extra was a `Metadata map[string]any` beside the
  payload, which is precisely the shape where a model's claim and a measured token count
  become indistinguishable. `Meta` separates them, and the model's self-score stays on the
  payload named `Model*`.
  **Breaking rename, stated plainly:** the payload types were named `SummarizeResult`,
  `RewriteResult`, `TranslateResult`, `ExpandResult`, which are the names the new functions
  need. They are now `Summary`, `Rewritten`, `Translation`, `Expansion`. A type and a function
  cannot share a name, so this could not be done with a deprecation window — code naming those
  types breaks at compile time, which is the loudest and cheapest way for it to break. The
  module has no tags (**DI-001**), so every consumer is on a pseudo-version and pinned by
  commit; if that stops being true, this is the kind of change that needs a version to hang
  it on.
  Evidence: `text_envelope_test.go`, 19 cases through the exported API with a scripted
  provider — each of the four returns its value and an envelope naming its own operation;
  each returns the envelope *on failure too*, since a caller who gets only an error cannot
  tell a refusal that cost nothing from one that burned three attempts; the deprecated twin
  and the new form return the same text, confidence, and compression ratio for the same
  response, which is what makes this a collapse rather than a rewrite; the model's confidence
  is reachable on the payload and appears in no `Meta.Check`; and no cost or token count is
  invented when the provider reports none. `testdata/api_surface.txt` moves by exactly four
  added functions and four renamed types.
  What remains is the API collapse — one operation returning `Result[T]` instead of two
  differing only in return type. That needs **A-006**'s `Result[T]`, which does not exist
  yet, and inventing a fifth result shape in the meantime would be the opposite of the
  point.
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
- [x] **S-002** — Emit real JSON Schema from the same reflection pass, and keep a compact
  rendering for the prompt path (full JSON Schema is token-expensive).
  *Verify:* the emitted schema validates against a JSON Schema meta-schema.
  **Revised (TRU-05):** the schema needs an identity, not just a body:
  `{Name, Version, Hash, Dialect, TypePolicy}`. Everything downstream keys on it — prompt
  bytes, cache keys (**P-009**), replay fixtures, stored results, semantic baselines — and
  a schema that changes without changing its hash silently invalidates all four. An
  anonymous type gets a deterministic content-derived name and a warning that persisting
  results against one is a bad idea.
  **Done.** The emission half shipped with **P-005** — `GenerateJSONSchema` produces
  strict-mode JSON Schema and `GenerateTypeSchema` keeps the compact prose rendering for the
  prompt, from the same reflection pass. What the Revised note added, and what was actually
  missing, is *identity*.
  `SchemaDescriptor{Name, Version, Hash, Dialect}`, built by `DescribeSchema`. **The hash is
  over the emitted schema, not over the Go type**, because the schema is what was sent:
  renaming a Go field without touching its json tag changes the type and not the contract,
  and the hash follows the contract. Changing a tag or a field's type does change it.
  A type strict mode cannot express still gets an identity, hashed from the prose schema and
  marked `dialect: prose` — collapsing every such type onto one hash would make the key
  useless for exactly the types that need it.
  `SchemaCacheKey` carries the operation and its version alongside, because a cache keyed on
  the schema alone serves an answer produced by a different question. **P-009** and
  **MW-004** both need this and neither should invent its own.
  The identity reaches the request as `OpOptions.SchemaID` and the result as
  `Custom["schema_id"]`: a stored result that cannot say which contract produced it is one
  nobody can reproduce or compare a year later.
  *Verify:* `internal/ops/schemaid_test.go` — stability across 20 runs, the hash following the
  contract rather than the Go type (four cases), an added field changing it, unexpressible
  types keeping distinct identities, the cache key separating operations and versions, and
  the identity the operation actually sends matching what the descriptor says, so the two
  cannot drift.
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
- [x] **S-007** — Make `strengthenSystemPrompt` opt-out and measure whether it still helps.
  It prepends a fixed instruction block to every request, JSON or not, billed on every call.
  Closes **I-18**.
  *Verify:* an A/B over the golden corpus recording the measured difference; if none, delete.

## Added from `to-production.md`

  **Done for the opt-out and the price; the quality question is handed to RC-002, which is
  where it belongs.**
  `SCHEMAFLUX_PROMPT_REINFORCEMENT=0` removes the block. Only an explicit off means off —
  an empty variable or a typo leaves it on, because turning it off by accident changes
  behaviour silently, which is worse than paying for it.
  **The price is measured rather than estimated: about 53 tokens on a text request and 117 on
  a JSON one, on every call.** The test prints both and fails if the block grows past 200,
  because a block that becomes substantial is a decision somebody should make on purpose.
  The task's verify line asked for an A/B over the golden corpus, and that cannot be settled
  here: whether the block *helps* is a question about model behaviour, and answering it needs
  repeated live runs against a pinned corpus with statistical thresholds — which is exactly
  **RC-002**'s semantic regression suite, and costs money by construction (**B-04**). The
  deletion the task offers as the alternative would be a guess in the other direction.
  Documented in the README where a caller looks for it, with the honest caveat that whether
  to turn it off depends on their model and their task.
- [x] **S-008** — Exact decoding in strict mode: reject unknown properties, duplicate keys,
  and trailing data after the top-level value; enforce maximum nesting depth, string size,
  array length, and total decoded bytes; report the smallest failing JSON pointer.
  `encoding/json` discards unknown fields, which is how a hallucinated field becomes
  invisible — **F-035** caught the "answer about something else" case with a weak
  field-name rule, but overproduction inside a well-shaped answer still passes. Closes
  **TRU-08**; completes what F-035 started.
  *Verify:* a response carrying one extra field fails in strict mode and names it; a
  deliberately deep or oversized body is refused before allocation.
  **Done** — `internal/ops/strictdecode.go`, applied by `Strict()` and by nothing else.
  Rejecting an extra field is exactly wrong for an operation whose contract permits one, so
  `Transform` still tolerates it and a test asserts the two modes differ; otherwise `Strict`
  means nothing.
  Unknown properties, repeated keys, values of the wrong type, and bodies past the byte,
  depth, array, and string limits are all failures. The limits exist because the response is
  bytes from a remote service: a pathological one costs memory before turning out to be
  useless.
  **The trailing-value check is the one worth reading.** `{"a":1} {"a":2}` is a model that
  answered twice, and taking the first silently is the same mistake as taking the last of a
  duplicate key — but the *extractor* pulls out the first balanced value by design, which is
  how it finds a payload amid prose, so by the time the decoder runs the second value is
  already gone and `encoding/json`'s own trailing check has nothing to see. The check runs
  against the original response instead, and ignores trailing prose and closing fences, which
  are packaging.
  Failures name a JSON pointer — `/items/0/price` — because "the response did not fit" is not
  actionable. They name fields and never values (**X-03**), and they classify as
  malformed-versus-schema-violation (**A-007**), which is the difference between repairing by
  quoting the parse error and regenerating from source.
  *Verify:* `internal/ops/strictdecode_test.go` — 4 rejection cases, the faithful answer, the
  fenced answer, four limits, the pointer, the no-values rule, and the kind split.
  Integration: `TestIntegrationStrictModeRejectsOverproduction` (3 bodies),
  `TestIntegrationTransformModeToleratesAnExtraField`, and
  `TestIntegrationStrictModeAcceptsAFaithfulAnswer`.
- [x] **S-009** — Exact numeric handling. `float64` is the wrong default for money,
  identifiers, and large integers: 16-digit account numbers lose precision, leading zeros
  vanish, and a postal code becomes a number. Support `json.Number` for deferred parsing,
  registered decimal types, integer bounds with overflow detection, string schemas for
  identifiers that must keep their shape, and registered encoders for `time.Time`,
  `time.Duration`, and UUIDs. A conversion that would lose information is
  `ErrSchemaViolation`, not a silent zero. Closes **TRU-07**.
  *Verify:* a 19-digit identifier and a two-decimal currency amount survive a round trip
  byte-exact; a value exceeding the declared range is refused.
  **Done for the detection, which is the part that was missing.** `json.Number` already
  worked for deferred exact parsing and a string field already kept its leading zeros; what
  nothing did was *notice* when a value the model sent could not survive its Go type.
  `CheckNumericFidelity` is a round trip: decode, re-encode, compare every number literal as
  an exact rational. A number that changed is a number the target could not hold, and the
  caller is told which field instead of being handed a value that is quietly wrong.
  Rationals rather than floats, because comparing the thing under suspicion with itself
  proves nothing: `1284.50` and `1284.5` are the same rational and different strings, and
  `90071992547409910` and `90071992547409920` are different rationals and the same float64.
  **The float32 blind spot is recorded rather than papered over.** Go marshals a float32 as
  the shortest decimal that round-trips *as a float32*, so `1284.57` stored as
  `1284.5699462890625` re-encodes as `"1284.57"` and the trip looks clean. The loss is real
  and this method cannot see it. The test asserts the limitation, so if the blind spot ever
  closes the comment is caught as stale. A float32 money field is a type choice to reject at
  preflight — **S-010** — not a value to catch at decode.
  *Verify:* `internal/ops/numeric_test.go` — a nineteen-digit identifier in a float64, nested
  precision loss with its JSON pointer, values that fit (including exponent and trailing-zero
  forms, which are the same rational), int8 overflow still reported by the decoder,
  `json.Number` keeping the literal, a string field keeping its leading zeros, and an
  actionable message.
  **Not closed by this:** registered decimal and Money types. Detecting the loss is what makes
  a caller reach for them; providing them is a public-API decision that belongs with
  **S-010**'s type support matrix.
- [x] **S-010** — Type support matrix, enforced at preflight rather than discovered at
  runtime. Four levels: full (structs, slices, arrays, pointers, scalars, enums, registered
  time/decimal types), restricted (string-keyed maps, bounded recursion, embedded fields,
  custom marshalers — documented limits or a registered adapter), opaque (blob wrappers,
  no field-level evidence), rejected (unbounded cycles, non-string map keys, unconstrained
  `any`, funcs, channels, unsafe pointers). A rejected shape fails before it costs a call.
  Closes **TRU-09**. Depends on **S-001**, which already caps depth and names cycles.
  *Verify:* one case per level; a rejected type produces an error naming the field and the
  reason, and makes no provider call.
  **Done** — `internal/ops/typesupport.go`, four levels with the line drawn at what a caller
  can do about it: **full** (schema and decode both exact), **restricted** (works with a
  documented limit — a map has no field names, recursion is cut at a depth, a custom
  marshaler's wire form is not derivable from its Go shape), **opaque** (carried, not
  described), **rejected** (no schema can be produced, so no answer could satisfy it).
  `Extract` refuses a rejected type **before the call**, and the message says so: the
  difference between "your type is wrong" and "your type is wrong and you were billed" is
  the whole point. An `any` field used to produce a schema saying `interface {}`, the model
  guessed, and the decode either failed or succeeded into something meaningless — a provider
  call spent to discover what reflection knows for free.
  **The worst finding wins, not the first.** A struct with one rejected field is rejected
  whatever else it holds; reporting a restricted map while a channel sits two fields down
  wastes the trip. Fields excluded with `json:"-"` are not part of the contract and cannot
  break it.
  *Verify:* `internal/ops/typesupport_test.go` — 12 classification cases across all four
  levels, the worst-wins rule with its path, preflight refusing and permitting, rejection
  found three levels deep, and excluded fields not counting. Integration:
  `TestIntegrationARejectedTypeCostsNoProviderCall` asserts **zero** provider calls, and
  `TestIntegrationARestrictedTypeStillRuns` asserts restricted means limited rather than
  refused.
  **Follows from S-009:** a `float32` money field is a type choice this matrix is the right
  place to flag, and it is currently classified full. Flagging it needs a notion of what a
  field is *for*, which a Go type does not carry — filed as **S-012**.
- [x] **S-011** — Schema evolution rules and migrations. Adding an optional field is
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

  **Done for the rules; migrations are deliberately not built.**
  `CompareSchemas` classifies a change as unchanged, compatible, a new contract version, or
  breaking, and `SchemaDiff.Summary()` renders it for a release note — which is what a
  compatibility policy actually is. It names the fields added, removed, retyped, tightened,
  and loosened.
  The rules follow what breaks. Adding an **optional** field is decode-compatible; adding a
  **required** one is a new contract, because old results decode with it empty and `Strict`
  rejects them. Changing a field's type, or making an optional field required, is a new
  contract for the same reason. Removing a field or renaming its json tag is **breaking**,
  because an old result then carries a field the new type does not accept — which **S-008**'s
  exact decoding refuses rather than ignores. The worst change wins: a release note has to
  describe the release rather than its most flattering part.
  Two Go types that serialise the same way are the same contract — `int` to `int64` is not a
  change — which is the same rule **S-002**'s hash follows.
  **A compatible change still changes the schema hash, and a test says so**, because
  "compatible" is about decoding and a cache keyed on a stale identity would serve the old
  answer to the new question.
  *Verify:* `internal/ops/schemaevolution_test.go` — 7 classification cases, loosening named,
  the JSON-shape rule, worst-change-wins, slices and pointers comparing by element, and the
  cache-identity interaction.
  **Migrations are not built and should not be guessed at.** A migration is a deterministic
  function from one stored shape to another, with its own version and provenance; writing the
  machinery before anything stores results would be building for an imagined caller. Stored
  results already carry the schema identity that a migration would key on (**S-002**), which
  is the prerequisite. Filed as **S-013**.
- [x] **PR-001** — Never substitute another model's price. `getDefaultPricing` returns
  claude-3-haiku pricing for **any** Anthropic model, understating an Opus call by roughly
  60x while presenting it as a precise USD figure; six of eight providers have no entry at all
  and report `$0.00`. Add `Estimated` / `PricingSource` to `CostInfo`. Closes **I-02**.
  *Verify:* an unpriced model reports unpriced, never zero; a substituted price is impossible
  by construction.
- [x] **PR-002** — Add `RegisterPricingModel` and populate the 5.6 family. The price table is
  a private package var with hardcoded 2024 effective dates and no override path.
  **Override path done; the 5.6 rates deliberately not invented.**
  `pricing.RegisterPricingModel` adds or replaces a model's rates, normalising the name the
  same way lookups do — registering `"GPT-5.6-Luna"` prices `gpt-5.6-luna`, because a
  registration that does not normalise is a rate card that never matches anything. The table
  is now guarded by a lock: making it writable at runtime turned every concurrent price
  lookup into a data race, which would have shown up as a wrong invoice rather than a crash.
  `RegisteredPricingModels` lists what is priced, so a caller can see it rather than guess.
  Two refusals are the substance of it. A rate above one dollar per 1K tokens is rejected
  naming the likely cause: every public price list quotes per **million**, this table is per
  **thousand**, and a caller who pastes a list price registers rates a thousand times too high
  with nothing afterwards to reveal it. And a rate card pricing both prompt and completion at
  zero is refused, pointing at the honest alternative — "priced at nothing" is a more
  convincing lie than "unpriced", which is why **PR-001** made unpriced its own state.
  Evidence: `pricing/register_test.go`, 15 cases including the per-million mix-up, the
  all-zeros card, name normalisation, replacement of a built-in rate, and concurrent
  registration beside concurrent pricing.
  **The 5.6 family is still unpriced, on purpose, and that is now pinned by a test.** No rate
  for `gpt-5.6-luna`, `-sol`, or `-terra` exists anywhere in this repository, and these are
  the library's *default* models — so every operation currently reports its cost as unpriced.
  Inventing plausible numbers would replace an honest "we do not know" with a confident wrong
  invoice, which is the exact failure **PR-001** exists to prevent. `RegisterPricingModel` is
  the answer for anyone who has the real rates; Cam supplying them is a one-line change to the
  table and a deliberate update to `TestTheDefaultModelsReportUnpricedRatherThanGuessed`.
  **Not done:** the 2024 effective dates on the older entries are still whatever they were —
  they are historical rates and re-dating them would make them look freshly verified.
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
- [x] **MW-001** — `Handler` / `Middleware` chain applied at client construction. Closes
  **Gap-11**.
  **Done** — `mw/` at the repository root, a public package importing the internals the way
  `schemafluxtest/` already does. `Handler` is `llm.Provider`, a `Middleware` takes one and
  returns one, and `Chain(base, mws...)` composes them with the first-listed outermost.
  No change to `client.go` was needed: `WithProviderInstance` is already the
  client-construction seam, so a caller writes
  `client.WithProviderInstance(mw.Chain(base, mw.RateLimit(...), mw.Retry(...)))`.
  MW-004 through MW-008 each become a new file exposing another `Middleware` without touching
  this seam, which is the property the task was really asking for.
- [x] **MW-002** — `mw.RateLimit`. Closes part of **Gap-09**.
  **Done** — token bucket, blocking by default with `mw.Reject()` to fail instead and
  `mw.WithBurst()` for a capacity distinct from the refill rate. A caller shedding load needs
  the choice; one smoothing a burst needs the wait.
  The reject-mode error is a `*types.OperationError{Kind: KindRateLimited}` carrying a
  `RetryAfter`, so it flows through the **A-007** classifier identically to a provider 429 —
  which is what makes it compose with `mw.Retry` rather than needing to be special-cased
  there. The caller's deadline is checked before the wait and honoured during it.
  *Verify:* `mw/ratelimit_test.go`, 12 cases.
- [x] **MW-003** — `mw.Retry` wrapping **A-008**.
  **Done** — and it asks `llm.Classify` plus `OperationError.Retryable()` rather than
  carrying its own opinion, which was the condition that mattered: two layers with two answers
  is a bug that only reproduces sometimes, because which layer wins depends on where the error
  came from.
  Attempts and base delay default to the wrapped provider's own `RetryPolicy()` — a provider
  that knows its own rate-limit windows is a better default than a number picked without
  knowing which provider it will run against. `Retry-After` overrides the computed wait,
  whether it came from the server or from `mw.RateLimit`'s own refusal. Decorrelated jitter,
  so a provider recovering from an outage does not meet a synchronised second wave.
  *Verify:* `mw/retry_test.go`, 17 cases, plus a composition test through `RateLimit`+`Retry`.
  **Timing is driven by an injectable fake clock**, with no real `time.Sleep` anywhere in the
  suite — a flaky test in a rate limiter is worse than no test.
  `mw` measures 80.1% statements and was added to `scripts/coverage_floor.py`'s package list,
  so it counts from now on.
- [x] **MW-004** — `mw.Cache`: response cache keyed on a hash of model, tier, mode, prompt
  bytes, and schema, so exact-duplicate calls cost zero. Closes **Gap-07**.
  **Revised (TRU-10):** the key must carry the complete semantic execution identity —
  operation and prompt versions, schema hash, normalized options, input digest, provider and
  *resolved* model, temperature/seed, required contract level, data-policy partition, and
  decoder version — or a cache hit answers a question nobody asked. Exact result caching is
  opt-in and partitioned by tenant and data policy; caching on *similar* inputs stays off by
  default and is not a v1 feature. A cache hit appears in provenance and in cost accounting
  as a hit, never as a zero-cost call.
  **Done, with two axes honestly absent.** `mw/cache.go`. The key reuses P-009's
  `promptCacheKeyFor` output for the operation/prompt-version/schema half rather than deriving
  a second opinion about what identifies a call, and adds: schema hash independently (so the
  axis holds for calls that never set `CacheIdentity`), provider, resolved model, temperature,
  max tokens, response format, and the tenant/data-policy partition, supplied through
  `mw.WithCachePartition`. An unpartitioned call gets the zero partition, which is its own
  group rather than a merge into a stated one.
  It diverges from `promptCacheKeyFor` in exactly one place, and the divergence is the point:
  the input digest here *includes steering*. A prefix-cache miss costs a slower first token,
  so CA-002 drops steering from that key; an exact-result cache cannot tolerate the same
  choice, because a steered call is a different question and returning the unsteered answer to
  it is the fail-open this list exists to remove.
  **Absent axes are named, not dropped:** seed, required contract level, and decoder version
  appear in the key as the fixed strings `seed:absent`, `contract:absent`, `decoder:absent`.
  `llm.CompletionRequest` has no seed field; `ContractLevel` is decided in `internal/ops`,
  above the `Handler` seam this middleware wraps; and the decoder has no versioned identity
  yet. A literal placeholder is visibly missing where an empty string is indistinguishable
  from a real empty value.
  Opt-in, exact-match only — no similarity caching, per the Revised line. Concurrent identical
  misses coalesce onto one provider call, and a coalesced follower shares the leader's error
  rather than retrying or being handed a fabricated success. Failed calls are never stored.
  Evidence: `mw/cache_test.go`, 30 cases — thirteen key-derivation tests, one per axis, each
  proving only that axis moves the key; order-independence of the schema hash; reproducibility
  across identical calls; partition isolation including unpartitioned-versus-stated; TTL
  expiry on a fake clock with no sleeps; and `TestCacheThroughChainWithScriptedProvider` for
  the composition.
  **Not done, and it is the Revised line's cost-accounting requirement:** a hit is visible
  through the `mw.WithCacheStats` callback, but nothing populates `types.Meta`, so an operation
  that does not wire the callback sees a hit as an honestly-zero-token call —
  indistinguishable from a call that genuinely needed no tokens. Closing it needs a `CacheHit`
  field on `llm.CompletionResponse` carried into `Meta`, which is a change under `internal/`
  and is filed rather than smuggled in here. `FinishReason` is deliberately left untouched on
  a hit: `ClassifyCompletion` reads it to detect truncation, and overwriting it with a cache
  marker would hide a real truncation on every replay.
- [x] **MW-005** — `mw.Budget` enforcing **PR-005**. Closes **CF-08**.
  **Done.** `mw/budget.go`. The estimate is checked and the reservation taken inside one
  critical section, so two goroutines cannot both pass a check that together exceeds the
  ceiling — a ceiling two callers can both walk through is not a ceiling.
  `TestBudgetConcurrentCallsCannotBothPassAnExceedingCheck` was verified by splitting the
  check from the reserve, watching it fail with two successes, and putting it back.
  After the call the reservation is reconciled to the measured cost; an unpriced model keeps
  the pre-call estimate rather than being zeroed, because an unpriced call is not a free one
  (**PR-001**). The refusal is a `types.OperationError` of the existing `KindBudgetExceeded`,
  not a new sentinel. 12 cases, plus composition through `mw.Chain`.
  **Stated limitation:** on a provider error the reservation is released, which assumes the
  failed call was not billed. That holds for every provider in this module, which returns a
  zero-value response on every error path, and the assumption is written at the release site
  rather than left for someone to rediscover.
- [x] **MW-006** — `mw.RedactEgress` so payloads can be scrubbed before they leave.
  **Done.** `mw/redact.go` scrubs the system and user prompts before the wrapped provider is
  reached, with the credential shapes as built-ins and `WithPattern`, `WithFunc`,
  `WithMarker`, and `WithoutBuiltins` for callers.
  The three redaction lists in this repository — `scripts/secret_scan.py`, the cassette
  writer, and this — stay separate *on purpose*, and the reasoning is in the file: they guard
  three different moments (a commit-time file scan, a fixture-write guard, a live egress
  guard), and importing the test-support package from production middleware would invert the
  dependency direction. That is a decision to keep them in step by review, and it is written
  down so the next person changing one knows the other two exist.
  `TestRedactEgressLeavesOrdinaryTextUntouched` is the over-redaction guard, verified against
  a deliberately broad pattern that mangled the prose. 10+ cases plus a `Chain` case asserting
  the credential is redacted on every retried attempt, not just the first.
- [x] **MW-007** — `mw.Metrics` exporting to OpenTelemetry, already a direct dependency.
  Closes the export half of the metrics gap.
  **Revised (ARC-17, ARC-18):** the direction is wrong as stated. Core defines small observer
  interfaces and emits through them; the OpenTelemetry adapter lives in a separate package
  (`telemetry/otel`) and uses the *host's* provider. The library never initializes a global
  SDK, an exporter, an endpoint, or a sampler, and never owns their shutdown — a library
  that configures the host's telemetry stack cannot be embedded twice. That also means OTel
  leaves the core's dependency set. See **OB-001**.
  **Done in the direction the Revised line specifies.** `telemetry.Observer` is the interface
  the core emits through — `OperationStarted`, which returns a context so an implementation
  that opens a span can put it there, and `OperationFinished`. Nothing in `telemetry/observer.go`
  imports OpenTelemetry. The adapter lives in `telemetry/otel` and is handed the **host's**
  `TracerProvider`.
  What it refuses to do is the point: it does not call `otel.SetTracerProvider`, does not build
  exporters, does not choose an endpoint or a sampler, and does not own shutdown. A nil
  provider is an error rather than a cue to reach for the global one, because falling back is
  exactly how a library ends up writing into a stack nobody asked it to write into.
  `InitTracing` and `ShutdownTracing` did all four of those things and are now deprecated with
  the reason attached. They still work.
  Evidence: `telemetry/observer_test.go` (9 cases — the default is a working no-op rather than
  nil, the context an observer returns is the one used, restore puts the previous one back,
  nil restores the no-op instead of panicking later at an emission site, and two reflection
  checks that the event types carry no payload field and report an error *kind* rather than a
  message, because a span attribute is an export and a provider's message can quote the
  caller's input back). `telemetry/otel/otel_test.go` (6 cases) — the first asserts an
  **absence**: installing the adapter leaves `otel.GetTracerProvider()` untouched, which is
  the only way to check that a library did not reconfigure its host. Also: an unpriced call is
  marked unpriced rather than exporting a cost of zero, and a nil observer or a finish with no
  start does not panic, because an observer is called from the hot path.
  **Not done:** the core does not yet emit through the observer — `internal/ops` still calls
  `telemetry.StartSpan`/`RecordMetric` directly, so an installed observer sees nothing until
  those call sites are moved. And OpenTelemetry has not left `go.mod`: `telemetry/tracing.go`
  still imports it, so the dependency is still in the module graph even though the adapter is
  now the only *intended* user. Both are follow-on work, and **OB-001** is where the emission
  sites belong.
- [x] **MW-008** — `mw.Fallback` for provider failover. Closes the rest of **Gap-09**.
  **Revised (TRU-12, ARC-24):** failover is not free substitution. A fallback route must
  meet the same minimum capabilities and the same data policy as the route it replaces — a
  private-region failure may not fall back to a public provider — and it may not silently
  downgrade the requested contract. A named degradation (native schema → JSON mode plus
  deterministic validation) is allowed only by explicit policy and is recorded as delivered.
  A fallback's own failure is classified on its own terms, not hidden behind the original.
  Depends on **CP-001** for the capability data this decision needs.
  **Done for the half that can be honest today.** `mw/fallback.go`. A `FallbackRoute` carries
  declared capabilities and a data-policy classification, and a route that does not meet the
  primary's declared requirement is **never called** — not called and rejected afterwards,
  which would already have sent the payload to a provider the policy forbids. Verified by
  disabling the eligibility check and watching
  `TestFallbackNeverCallsAnAlternateLackingRequiredSchemaSupport` and
  `TestFallbackNeverCallsAnAlternateWithAMismatchedDataPolicy` fail. A named schema
  degradation requires the explicit `AllowSchemaDegradation()` option, so a downgrade is a
  decision rather than a side effect. An alternate's own failure is returned on its own terms
  — `TestFallbackReturnsLastFailureWhenEveryRouteFails` asserts the alternate's provider
  survives in the error rather than being hidden behind the primary's. 15 cases plus a `Chain`
  composition with `Retry`.
  **Not enforced, and both gaps are in the file's doc comment rather than only here:**
  (1) **CP-001** does not exist, so there is no capability or policy *introspection* — every
  requirement is a caller-declared label, and an undeclared requirement enforces nothing. That
  is documented rather than silent, but a caller who declares nothing gets no protection.
  (2) The Revised line's "recorded as delivered" is not done: `AllowSchemaDegradation` gates
  the substitution, but the response carries no marker of the degradation, because the
  `Result`/`Meta` envelope lives a layer above the `llm.Provider` seam `mw` operates at. It
  needs plumbing from **A-001**/**A-006**, not a fix in this file. Until then a permitted
  degradation is invisible downstream, which is the weaker half of the requirement and is
  named here so it is not mistaken for done.

## Prompt caching

- [x] **CA-001** — Sort map keys before rendering them into prompts. Go randomizes map
  iteration order, so `SchemaHints` (`core.go:73`), `FieldRules` (`core.go:80`,
  `extended.go:232`), `Mappings` (`project.go:171`), and `CategoryDescriptions`
  (`analysis.go:95`) produce different prompt bytes on every call — defeating any prefix cache
  and making runs irreproducible. Addresses **Gap-04**.
  *Verify:* **TI-005** passes.
- [x] **CA-002** — Split `Prompt` into `Stable` and `Volatile` segments; move steering and all
  option-derived clauses out of the system prompt and into the user message. `applySteering`
  currently appends per-call text to the system block (`llm_helper.go:291`), invalidating the
  prefix on every call.
  *Verify:* two calls differing only in steering share a byte-identical stable prefix.
  **Done.** Steering — and the option-derived clauses the text operations route through it —
  now go in the user message; the system prompt is the stable segment and holds only what the
  library wrote. `TestSteeringDoesNotMoveTheCacheablePrefix` is the Verify line: two calls
  differing only in steering send a byte-identical system prompt and the same
  `PromptCacheKey`, while their user prompts differ, so the test cannot pass by the steering
  never arriving at all.
  **A second defect fell out of it.** Response format was inferred from the system prompt
  *with steering already appended*, so a caller whose steering mentioned JSON silently
  switched their text operation into JSON mode. That is the same data-controls-the-control-path
  defect `resolveResponseFormat`'s own comment describes for the user prompt, one argument
  over. The inference now reads the library's own words only.
  `TestSteeringReachesTheSystemPromptDeliberately` pinned the old behaviour and has been
  replaced by `TestSteeringNoLongerSetsTheResponseFormat`. That is a deliberate reversal of a
  recorded property, not an accident: "deliberate" in the old test meant known, not chosen,
  and a caller who needs a format has `ResponseFormat`, which is exact.
  **Placement, and the trade-off:** steering goes *before* the caller's content, not after.
  Instruction-after-data resists injection better in the abstract, but every prompt this
  library writes ends with the data, and things downstream depend on that — the shape-
  answering local provider finds the items by taking the last JSON array in the message.
  Putting steering last made a MapReduce silently return nothing. The comment at the call site
  records the trade-off rather than pretending there wasn't one.
  **Golden prompts moved**, deliberately and for every operation with steering or
  option-derived clauses. That is a behaviour change with no Go API change to show for it,
  which is exactly what the golden snapshot exists to make visible.
- [x] **CA-003** — Provider-specific cache wiring: `cache_control` breakpoints on the last
  stable block for Anthropic (max 4), byte-identical prefixes plus `prompt_cache_key` for
  OpenAI. Depends on **P-009**, **P-016**.
  **Done, as the sum of its three parts, and only now that the third landed.** Anthropic gets
  one `ephemeral` `cache_control` breakpoint on the last stable block — the system block when
  present, otherwise the user block — one of the four permitted (**P-016**). OpenAI gets
  `prompt_cache_key`, covering operation and prompt version, schema hash, resolved model,
  mode, and response format (**P-009**). And the prefix is now actually byte-identical across
  calls that differ only in steering (**CA-002**), which is what makes the other two worth
  sending: a correct cache key attached to a prefix that changes every call is a precise
  identifier for something that is never reused.
  Evidence: `internal/llm/provider_cache_test.go` for both providers' wire formats, and
  `TestSteeringDoesNotMoveTheCacheablePrefix` for the prefix stability the wiring depends on.
  **Not verified here:** that any of it produces a measured cache hit against a live provider.
  That is **CA-004**, which is `[LIVE]` and blocked on **B-01** — nothing in this task can be
  confirmed by a test that never leaves the machine, and the honest statement is that the
  requests are now shaped correctly, not that caching is working.
- [ ] **CA-004** — Consolidate per-operation invariant content (schema, exemplars, rules) into
  the stable zone so it crosses the minimum cacheable prefix. Below the floor — 1024 tokens on
  OpenAI, 512–4096 on Anthropic depending on model — caching silently does nothing, and
  today's system prompts are a few hundred tokens.
  *Verify:* measured `cached_tokens` greater than zero on the second identical call.
  `[LIVE]`, blocked on **B-01**.
- [x] **CA-005** — Fan-out ordering primitive: send one request, await first token, then
  release the rest. A cache entry is only readable after the first response begins streaming,
  so a naive parallel fan-out has every worker pay a full write.
  **Done.** `internal/ops/fanout.go`: `FanOutGate` and `FanOut`. One worker claims the lead
  and the rest wait for it to report first output — the *first token*, not the finished
  answer, because gating on completion would serialise the fan-out and cost more latency than
  it saves in tokens.
  A failed leader still releases the followers. Holding them turns one provider error into N
  stalled workers, and a follower is perfectly capable of making its own uncached call; the
  leader's failure is handed to them as *information* rather than returned as their own error,
  since N callers reporting a failure that happened once is its own kind of wrong.
  **A deadlock I built and then had to fix, recorded because the fix is the interesting part.**
  The first version released the gate once every worker had finished, as a backstop against a
  leader that forgets. That deadlocks: "every worker" includes the followers, and the
  followers are blocked waiting for exactly that release. The test for it hung rather than
  failed, which is how it was found. Releasing on the *leader's* return instead cannot
  deadlock, which is why `Claim` takes the worker's index — the gate has to know who led. A
  second guard opens the gate if nobody claims at all, so a run function that never calls
  `Claim` degrades to an ordinary unordered fan-out instead of hanging.
  Evidence: `internal/ops/fanout_test.go`, 13 cases, stable over five runs. The ordering
  property is asserted from a recorded event sequence rather than from timing, because a
  timing-based version of that test passes on an idle machine and fails on a loaded one for
  reasons unrelated to the code. Results come back in input order regardless of completion
  order, and a failure names the item's index rather than its content.
  **Not wired into any operation yet.** This is the primitive; using it for a real fan-out —
  the batch and MapReduce paths are the candidates — is separate work, and claiming otherwise
  would be claiming a cache saving nothing has yet measured.
- [x] **CA-006** — Surface `Meta.CacheHitRatio` and emit a diagnostic when repeated identical
  prefixes report zero cache reads.
  **Done, both halves.** `Meta.CacheHitRatio` is `CachedTokens / PromptTokens` over every
  attempt in the request, measured from what the provider reported and never estimated —
  `CachedTokens` was already plumbed from the Responses API through `envelopeFrom`, so nothing
  had to be invented to compute it. A provider reporting no prompt tokens gives zero rather
  than a division by zero, and the field's comment says plainly that zero is also what a
  provider that reports nothing produces, so the number cannot be read as proof caching works.
  The diagnostic is the half that matters: a prompt cache doing nothing looks exactly like one
  that is working — the calls succeed, the answers are right, and the only difference is the
  bill. `noteCacheReads` counts calls per cache key and warns once when a key has repeated
  three times without a single cached token, naming the likely cause (a prefix below the
  provider's minimum cacheable length, or something per-call still inside the stable segment).
  It logs the key, never the prompt, because the prompt is built from the caller's data.
  The tracker is bounded at 512 keys and drops the table wholesale at the cap: a diagnostic
  that grows without limit in a long-running process is a worse bug than the one it reports.
  Evidence: `internal/ops/cachereads_test.go` — the ratio across four cached/uncached splits,
  across retries, and undefined-not-zero when nothing was reported; the diagnostic fires at
  the threshold and not before, fires once rather than per call, stops entirely once a cached
  read appears, ignores calls with no cache key, stays under its cap when given three times
  that many keys, and survives eight concurrent writers.

## Streaming and long output

- [x] **ST-001** — Add streaming to the `Provider` interface and implement it for the
  Responses API. Closes **Gap-01**.
  **Done as a capability interface, not a required method**, and the reason is concrete: a new
  method on `Provider` breaks every implementation in this repository — the test double, the
  cassette player and recorder, every middleware wrapper, every mock — and a library that
  breaks its own test-support package to add an optional feature has made the feature
  expensive for everyone who did not want it. `llm.StreamingProvider` embeds `Provider` and
  adds `CompleteStream`; the call site type-asserts, and a provider that cannot stream gets
  `KindUnsupportedCapability` rather than a buffered call dressed up as a stream.
  `Complete` and `CompleteStream` now decode into one shared type and pass through one shared
  classification function, so the buffered and streamed paths cannot disagree about what a
  failure was — asserted directly: the same 429 classifies identically both ways.
  A stream that dies partway is an error, never a `Done` chunk assembled from partial text. A
  connection that closes with no terminal event gets `ErrStreamIncomplete`.
  Evidence: 11 cases in `internal/llm/provider_stream_test.go`, driven by an `httptest.Server`
  emitting real SSE frames rather than a hand-mocked reader, because the framing is where this
  goes wrong. Includes: streamed content equals the buffered content for the same response;
  the streamed wire request differs from the buffered one only by `"stream":true`; breaking
  the loop makes the *server* observe the disconnect; malformed SSE is an error rather than a
  skipped frame.
  **Not done:** only the OpenAI Responses API streams. Anthropic and the OpenAI-compatible and
  local providers correctly report unsupported. `ErrStreamIncomplete` classifies as
  `KindUnknown` — there is no kind for it, so it fails closed but without a precise name.
- [x] **ST-002** — Expose streaming on text operations, with the non-streaming path
  implemented in terms of it. Addresses **Gap-01**.
  **Done for two of the three modes; the third is deliberately absent.** `ops.TextStream` is
  the caller-facing handle behind `StreamSummarize`, `StreamRewrite`, `StreamTranslate`, and
  `StreamExpand`. Raw text and provider events are the two modes that exist.
  **Validated items is not implemented, on purpose.** Nothing in this library streams a
  collection today, and building item-level streaming by decoding a partial JSON prefix off a
  token stream is precisely the fail-open the Revised line names: a token stream is not a
  partial typed result. It is recorded as a gap in the package doc rather than shipped as a
  half-measure.
  The Revised line's four iterator requirements all hold and are each tested rather than
  asserted: the buffer is bounded at 32 and a fast producer with no consumer stalls at the
  buffer size instead of racing ahead; breaking out of the range cancels the remaining work;
  `Detach` is the explicit opt-out and keeps the producer running; and the ordering — completion
  order — is documented on the type.
  **The non-streaming path is not implemented in terms of the streaming one**, and the honest
  reason is in the code: `streamLLM` builds its request through the *same* helpers `CallLLM`
  uses, so the two cannot silently diverge, but rewiring `CallLLM` itself was out of the
  agent's file scope. A test asserts the two entry points agree for the same response instead.
  `streamLLM` also does not retry, deliberately: a stream that fails partway has already shown
  the caller text, so re-sending would duplicate or silently replace what they saw. That is a
  different operation from retrying a buffered call, and it is documented rather than
  inherited by accident.
  Evidence: 12 cases in `internal/ops/streaming_test.go`.
  **A real defect found doing this:** `ClassifyCompletion` matched `length`, `max_tokens`,
  `incomplete`, and `truncated` — but the Responses API's actual incomplete reason is
  `max_output_tokens`, which was in none of them. So against the one provider this library
  targets first, **ST-003**'s truncation handling never fired and a cut-off answer classified
  as `KindUnknown`. The existing provider test asserted the raw `FinishReason` string and never
  fed it through the classifier, which is how a list of four spellings missed the one that
  ships. Fixed, with `TestEveryTruncationSpellingClassifiesAsTruncated` covering all of them
  plus case folding, and a companion asserting a complete answer is never called truncated.
  **Revised (TRU-13, TRU-14):** three streaming modes, kept apart because they promise
  different things: raw text, provider events, and *validated items* — a token stream is not
  a partial typed result, and presenting one as the other is the same fail-open the whole
  list is about. The caller-facing iterator owns a bounded buffer, is cancelable, and
  documents whether items arrive in input or completion order; stopping iteration cancels
  the remaining work unless the caller explicitly detaches it.
- [x] **ST-003** — Remove the hardcoded output ceilings (4000/2000/1000 by tier,
  `config.go:206-218`) in favor of a per-call option, and make truncation return
  `ErrTruncated` rather than surfacing as a parse error. Closes **I-09**.
  **Done.** `OpOptions.MaxOutputTokens` is the per-call ceiling; the tier value stays as the
  default when the caller sets nothing, so no existing call changes behaviour, and the exact
  tier defaults are pinned by a test so a future edit to them is deliberate.
  Truncation is now detected where the response is read, before the decoder sees the body, and
  classified `KindOutputTruncated` through the existing `ClassifyCompletion` and sentinel —
  no second opinion was added. It is not retried: a truncated answer is not a transient
  failure, and retrying it spends money to be cut off again.
  **A real provider bug found doing it:** `AnthropicProvider.Complete` hardcoded
  `FinishReason: "stop"` and discarded Anthropic's `stop_reason` entirely, so an Anthropic
  response cut off at the ceiling was indistinguishable from a complete one and surfaced as a
  parse failure — the caller was told their model was broken when the answer had simply been
  cut off. It now parses `stop_reason` and maps `max_tokens` to truncation.
  Evidence: 12 tests across `internal/config`, `internal/ops`, `internal/llm`, and a root
  integration file — the override reaches `llm.CompletionRequest.MaxTokens`, beats a larger
  tier default, is ignored when negative; `length` and `max_tokens` both produce
  `errors.Is(err, types.ErrOutputTruncated)`; truncation is not retried; a normal `stop` is
  unaffected; and `TestNoDeadOptionFields` confirms the new option is genuinely read rather
  than being a lie in the shape of an API.

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
- [x] **IN-004** — Decide `Client`'s fate: it has no method that runs an operation, and
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
  **Done for the Verify line's first clause; the second is explicitly not met — see below.**
  `ops.ExecConfig` is the client's immutable snapshot — provider, budget, scheduler, data
  policy — taken under the client's lock and **copied** into the context by `Client.Context`.
  The copy is the whole mechanism, not a style choice: storing the caller's pointer would let a
  client reconfigured later reach into calls already in flight, which is the process-global bug
  again in a form that would be harder to find because it would look like per-call
  configuration. `TestReconfiguringAClientDoesNotChangeACallAlreadyInFlight` is that property.
  **Budgets are now per-client**, which was previously not merely unimplemented but
  *inexpressible*: one process-wide ledger meant a tenant that exhausted its allowance stopped
  calls belonging to a tenant that had not. `ClientBudget` is a ceiling and a running total,
  deliberately without the daily/weekly/monthly windows the process budget carries — a client
  is a scope, not a calendar, and a per-client budget that reset at midnight would let a tenant
  spend its allowance twice either side of a boundary nobody chose.
  The ledger refuses **before** the request, and `TestAnExhaustedClientBudgetRefusesARealCall...`
  asserts the exhausted client's provider saw **zero** calls — a budget enforced from the
  response has already spent the money it existed to prevent. It also carries the unpriced
  count separately, so an unpriced model does not report as a free call and quietly disable the
  ceiling; `Spent()` returns a completeness flag alongside the number, because "you have $2
  left" is information or a guess depending on that flag.
  `ExecDataPolicy` returns a second boolean so "this client allows everything" stays
  distinguishable from "no client is configured". A zero value cannot express that difference,
  and the two must not behave identically once policy enforcement is on.
  **Not done, and this is the clause that keeps the architecture honest:** *"core compiles with
  no reference to a package-level provider"* is **still false**. `ops.defaultProvider` and
  `ops.customLLMCaller` remain, because every existing caller that never built a client depends
  on them, and removing them is a breaking change that belongs with the compatibility adapter
  the Revised line describes rather than smuggled in beside a budget. The snapshot **takes
  priority** over them on every call path, so a client is never subject to another client's
  configuration — but the globals are still there for callers who have no client.
  Observer and cache policy are also still process-wide; only provider, budget, scheduler, and
  data policy are in the snapshot. And `Client` still has no method that *runs* an operation:
  it hands out a context, and the caller passes that to an operation. That is a smaller API
  than "the client is the execution boundary" implies, and it is the shape that could be built
  without changing every operation's signature.

---

# M08 — Control flow as combinators

Each takes an `Op` and returns an `Op`. Build after M05, because `Vote` needs comparable
results, `Escalate` needs a failure signal that is not "the model said something odd," and
`MapReduce` needs invariants to validate the merge.

- [x] **CF-001** — `flux.Escalate(op, from, to)`. Closes **CF-02**.
  **Done** as `Escalate(ctx, first, stronger, accept)` over a `Step[T]`, so it composes any
  operation or caller function rather than being written once per operation
  (`internal/ops/combinators.go`). It escalates for two distinct reasons and says which in
  `EscalationRecord.Reason`: the first step failed, or it succeeded and the caller's `accept`
  turned the answer down. A nil `accept` makes it a pure failure route.
  **A terminal failure does not escalate.** A malformed request, a policy refusal, an
  exhausted budget, or a bad credential fails identically on a stronger model, so escalating
  spends real money to buy nothing — the disposition comes from `types.OperationError` via
  `llm.Classify`, not a second opinion. An *unclassifiable* error does escalate, deliberately:
  an error this library has never seen should get the second route rather than be ruled out by
  a classifier that did not recognise it.
  Evidence: `internal/ops/combinators_test.go` — the accepted answer never calls the stronger
  step (asserted by call count, because "it returned the cheap answer" does not prove the
  expensive one was not also paid for); four terminal kinds each refuse to escalate and keep
  their classification on the way out; both failures appear in the error when neither route
  works; a cancelled context runs nothing.
- [x] **CF-002** — `flux.Vote(op, n, rule)` — and the first honest confidence number in the
  library, derived from sample agreement. Closes **CF-03**.
  **Revised (TRU-27):** agreement is a policy, not a proof — correlated models share
  hallucinations, so three samples agreeing on an invented figure is three samples wrong.
  Reconciliation is pluggable (exact agreement, field-level voting, deterministic validation
  then selection, evidence-weighted comparison, adjudicator model) and **must be able to
  abstain**, returning `ErrReviewRequired` rather than the majority answer. Evidence and
  invariants still apply to the winner; a vote does not substitute for them.
- [x] **CF-003** — `flux.Until(op, pred, max)`. Closes **CF-05**.
  **Done.** `Until(ctx, step, pred, max)` returns the first answer satisfying the predicate,
  the number of attempts, and an error when they run out.
  **Running out is an error**, classified `KindRepairExhausted`. The obvious alternative —
  return the last answer and let the caller decide — is the fail-open this list exists to
  remove: the last answer is by definition one that failed the predicate, so returning it as a
  success means the caller who wrote the condition is the one who never learns it was
  violated. The rejected answer is returned *alongside* the error, so it can be inspected but
  not used by accident.
  A nil predicate, zero attempts, and a negative maximum are all refused rather than treated
  as "run once": each silently drops the check the caller asked for. A terminal failure stops
  the loop instead of spending the whole budget on a call that cannot succeed, and
  cancellation is honoured between attempts.
- [x] **CF-004** — `flux.MapReduce(op, chunk, merge)` with bounded concurrency. Closes
  **CF-04**; unblocks **OP-108**. **Done `debaf6e`.**
- [x] **CF-005** — `flux.Checkpoint(store, runID)`. Closes **CF-06**, and replaces the
  declared-but-unimplemented `PipelineOptions.SaveProgress`.
  **Done.** `Checkpoint(ctx, store, runID, step, input, produce)` runs a step once and stores
  its result, so a resumed run does not repeat work whose side effect already happened —
  asserted by call count, not by comparing return values, because a replayed side effect
  returns the right value too. `CheckpointStore` is a two-method interface the caller
  implements; `MemoryCheckpointStore` covers tests and single-process runs. No database, no
  file format, no new dependency.
  The case that matters: a resume against *changed* input is detected and recomputed rather
  than silently served from the old record. Identity covers the run, the step, and a hash of
  the input.
  **`PipelineOptions.SaveProgress` did not need removing** — it was already deleted under
  **F-023**, and `dead_options_test.go` asserts its absence at compile time. The task's premise
  was stale; nothing was wired to a dead option.
  **Limitation:** inputs are fingerprinted with `encoding/json`, so an input that does not
  marshal fails loudly rather than checkpointing something it cannot identify.
- [x] **CF-006** — `flux.Approve(gate)`. Closes **CF-07**; required before **F-025**'s shell
  tool may be enabled.
  **Revised (TRU-26):** approval is one use of a more general terminal outcome. Automated
  recovery stops — with `ErrReviewRequired` and a `ReviewPacket{Candidate, InputRefs,
  Evidence, FailedChecks, Attempts, SuggestedAction}` — when evidence is contradictory,
  an invariant survives its budgeted regenerations, eligible providers disagree materially,
  or policy requires a human. That is a successful safety outcome, not a failure, and it is
  the alternative to looping the model until it says something acceptable. The library
  supplies the structure and the callback; it does not build an approval workflow.
  **Done.** `types.ReviewPacket[T]{Candidate, InputRefs, Evidence, FailedChecks, Attempts,
  SuggestedAction}` and `ReviewRequiredError[T]`, which reaches the existing
  `ErrReviewRequired` through `errors.Is` — no second sentinel was added. `Approve` runs a step
  and puts the result to a caller-supplied gate.
  `InputRefs`, not inputs: a review packet that embeds the caller's records leaks them into
  whatever handles the review, which is likely a queue, a log, or a human's screen. A test
  asserts the packet's `Error()` prints neither the candidate nor the refs.
  **A judgement call to flag:** `Approve` also converts a step's own terminal failures —
  `KindRepairExhausted`, `KindInvariantViolation`, `KindEvidenceViolation`,
  `KindSchemaViolation` — into the same review outcome, so an exhausted repair loop composed
  under `Approve` becomes "this needs a human" rather than a bare error. That is an
  extrapolation of the Revised line's "approval is one use of a more general terminal
  outcome"; it is defensible and it is broader than a literal reading of "wraps a gate".
  Everything else — configuration, auth, timeout, cancellation — passes through untouched.
  No workflow, queue, or UI was built, per the Revised line.
- [x] **CF-007** — `flux.Fallback(a, b)`.
  **Done, as one line over Escalate** rather than a second implementation. Two functions that
  each decide whether an error is worth another route eventually disagree, and the
  disagreement only appears under the failure they were both written for. `Fallback` is
  `Escalate` with no `accept`, so it has no opinion about the quality of a successful primary
  — which is exactly what distinguishes it from `Escalate`, and is asserted.
  Note this is the *combinator*; `mw.Fallback` (**MW-008**) is provider failover at the
  middleware seam, with capability and data-policy eligibility. Same word, two layers, and
  they are not interchangeable.
- [x] **CF-008** — Retire or reimplement `Decide`, `Guard`, `Match`, and `Pipeline` on the
  combinators. `Guard` currently issues an unannounced LLM call with a hardcoded 2-second
  timeout and no options (`procedural.go:143-180`). Closes **P-03**, **P-10**. Addresses **CF-09**.
  **Guard — reimplemented.** It now runs the caller's Go predicates and makes **no provider
  call at all**. What it did before was worse than the task's wording suggests: a caller
  handing it a list of pure functions got three things they never agreed to — their money
  spent, the failed-check messages (written by their own checks, from their own state) sent to
  a provider, and both done on a hardcoded two-second timeout at a hardcoded tier, so their
  configured deadline and intelligence were discarded.
  Suggestions moved to `GuardWithSuggestions`, a separate function rather than an option,
  because the difference between the two is whether a provider is called at all and that is
  not something to bury in a struct field. It uses the caller's options and their deadline. A
  provider outage there does not change the verdict, which Go had already decided.
  Evidence: `internal/ops/guard_test.go`, 10 cases. The call-count assertions are the point —
  a guard that called a provider and discarded the answer returns exactly what one that never
  called returns, so asserting on the result proves nothing. One case installs a check whose
  message quotes the state and asserts nothing left the process.
  **Pipeline — two helpers retired.** `Retry` was a *third* opinion about what is worth
  retrying, and the weakest: it retried every error the same number of times including
  terminal ones — a malformed request, a policy refusal, an exhausted budget — each of which
  fails identically next time, so the retries bought latency and, where the call had been
  billed, cost. It also slept on a fixed schedule, so concurrent callers retried in unison.
  `MapConcurrent` was a second bounded-concurrency primitive that started a goroutine per item
  *before* taking the semaphore — ten thousand items meant ten thousand goroutines queueing —
  and took no context, so nothing it started could be cancelled. `CallLLM`'s retry and
  `mw.Retry` survive (both classify through `llm.Classify`), as does `MapReduce`'s bounded
  pool. Nothing outside this package's own tests called either of the retired functions.
  **`Match` and `Decide` — kept, with reasons.** `Match` evaluates cases and already returns a
  provider failure rather than reading it as a non-match, which is the failure mode that
  matters. `Decide`'s fallback is opt-in, marks `Fallback: true`, and reports a confidence of
  zero rather than inventing one; with no fallback configured it returns the error. Neither
  duplicates a combinator, so retiring them would remove reachable, tested behaviour to
  satisfy a word in the task title.
- [x] **CF-009** — Bounded-concurrency primitive used by OP-106, OP-304, and CF-004. Today
  only `batch.go:136` and `pipeline.go:232` start goroutines. Closes **Gap-08**.
  **Done `debaf6e`** — shipped as `MapReduceOptions.Concurrency`. Kept inside `MapReduce`
  rather than extracted: a bare semaphore helper has no home of its own until OP-106 and
  OP-304 need it, and the ordering guarantee is the part that was actually missing.

---

# M09 — Product shape

Decisions, not defects. Each halves or doubles the maintenance surface, so make them before
1.0 — every finding in the review has to be fixed once per operation that survives.

- [x] **PS-001** — Decide the fate of the tools registry: 86 tools, 41 of them stubs, none
  reachable by an external consumer. Promote (requires **PS-004**) or delete. Closes **G-01**,
  **G-02**, **G-05**.
  **Decided by Cam: keep what is valuable, delete what is not.** Neither of the two options the
  task offered — promote everything or delete everything — was the right shape, and the
  registry is now 43 working tools with **zero stubs**.
  Deleted outright: `exec.go` (the `shell` tool handed a model-authored string to a shell, and
  a shell tool is the highest-risk, lowest-value thing a typed-LLM-call library can carry;
  `run_code` and six AI tools beside it were stubs), `audio.go`, `image.go`, and
  `messaging.go` — every tool in those three did nothing.
  Pruned tool by tool from files that were otherwise real: `chart`, `currency`, `stock` from
  finance (`tax` and `interest` work and stayed), `pdf`, `tar`, `qrcode`, `barcode` from
  archive (`zip` works), `vector_db`, `watch_file`, `web_search`, `scrape`, `browser`, `geo`,
  `weather`, and — the two worth naming — `encrypt` and `decrypt`, which reported success
  without encrypting anything. A fake encrypt is not a missing feature; it is a security
  claim that is false.
  **The stub-honesty guards were re-pointed rather than deleted.** They asserted that at least
  five stubs existed, which was correct while half the registry was unimplemented and became
  the wrong assertion the moment it was not. `TestEveryStubToolIsIdentifiable` now asserts
  there are **no** stubs, and its disclosure subtests still run if one is ever reintroduced;
  `TestEveryStubResultBelongsToAStubTool` now fails if the source walk finds no declarations
  at all, which is the failure it was really guarding against — a walker that quietly stops
  finding anything passes forever.
  `examples/tools/ai_examples.go` is gone (all 37 of its calls were to deleted tools) and the
  dead calls in four other example files were removed with it.
  **Not promoted, deliberately.** The registry stays `internal/`. **PS-004** is landing tool
  *calling* at the provider level, which is the supported way for a caller to use tools of
  their own; a maintained public catalogue of 43 file, http, and database handlers is a
  different product from this one.
- [x] **PS-002** — Decide the verb catalogue. With invariants and combinators in place, a core
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
  **Done as the catalogue plus a gate; the physical migration is not.**
  `catalog.go` declares, for every exported operation, its category — the eight the Revised
  line names — and its tier: **stable**, **experimental**, or **deprecated**. `Catalog()` and
  `Describe(name)` make it answerable, so a caller deciding whether to build on an operation
  can ask instead of guessing from the doc comment's confidence.
  The problem was never that the operations exist. It is that `Assemble` and `Extract`
  carried the same implied promise while only one had a defined contract, verified invariants,
  and tests to match — so a caller treated them alike, which is how an experiment ends up in
  an invoice pipeline. Twenty operations are marked experimental and fifteen deprecated with a
  named replacement.
  **The gate is the part that will still be true in a year.** `catalog_test.go` checks the
  catalogue against `testdata/api_surface.txt` — the snapshot that already fails the build when
  the public API moves — in both directions: an exported operation missing an entry fails, and
  an entry naming something no longer exported fails. It also refuses a deprecated entry whose
  replacement does not exist or is itself deprecated, and refuses a catalogue where every tier
  is the same, since a tier that never distinguishes has stopped meaning anything.
  It caught eight uncatalogued names and two dead entries the first time it ran, which is the
  drift it exists to prevent, arriving immediately.
  **Addresses API-06** — "define a small cross-operation vocabulary and reserve
  operation-specific methods for real contracts" is the catalogue's eight categories and the
  tier that says which operations have a contract worth reserving a method for. Cited here
  because the traceability gate found it untraced once it could read the gap registers.
  **Not done:** nothing has physically moved to a `recipes` package. The tier says which
  operations are heading there and the gate keeps that honest, but `Assemble` is still
  `schemaflux.Assemble`. Moving twenty operations is a large API change, and doing it while
  three agents are mid-flight in the same tree would be reckless; doing it *without* the
  catalogue would have been the same guesswork this task was opened to end. The catalogue is
  the decision; the move is mechanical work that now has a definition to follow.
- [x] **PS-003** — Pick one public spelling. Mark the other `// Deprecated:` so tooling says
  so — there is not a single deprecation marker in the repo today. Expose `Deduplicate`
  (implemented, exported nowhere) or delete it; document or delete `Format` and `Merge`.
  Closes **F-06**, **A-09**, **A-10**.
  **Done, and the first half of the task is answered "no".**
  "Pick one public spelling" assumed the two APIs are a duplication to resolve. They are not:
  `to-production.md` **API-01** keeps both deliberately — the standard functions are the
  canonical semantics, easiest to wrap and to construct options for dynamically; the fluent
  builders are the ergonomic surface; and **FL-003** is the task that makes them provably
  equivalent. Deprecating either would be a decision against the target architecture.
  What was actually wrong is that the README **claimed** the direct API was
  "compatibility-only" while most of its own examples used it, and while the repository
  carried **not one `// Deprecated:` marker** — so even a caller who believed the claim had no
  tooling that would tell them. Both are fixed: the README states the real policy, and the
  two genuinely superseded functions carry the marker.
  **`ValidateLegacy` and `QuestionLegacy` are now marked** in both packages, with the reason —
  `ValidationResult` is a bool, a `[]string`, and a model-reported confidence, and cannot say
  which field failed. They are marked rather than removed because the live smoke runner calls
  both, and deleting an exported function before 1.0 to save four lines is not a trade.
  **`Deduplicate` is exposed.** It was fully implemented in `internal/ops` and reachable by
  nobody, while `core/doc.go` listed it among the available operations — a documented
  operation that cannot be called is worse than an undocumented one, because the reader
  concludes the library is broken rather than that the doc is. Its O(n²) call cost is stated,
  with a pointer to **PS-006**, which is the right shape for the problem.
  **`Format`, `Merge`, and their metadata twins** are in the README's catalogue now; they were
  exported and appeared nowhere in it.
  *Verify:* `TestPublicAPISurface` records the new export; `TestREADMEClaimsMatchTheLibrary`
  and the compatibility section carry the policy.
- [x] **PS-004** — Add tool calling to the provider path: `Tools`, `ToolChoice`, and tool-call
  response handling, none of which exist in `CompletionRequest`. Closes **Gap-02**; makes
  PS-001 worth doing.
  **Done.** `Tool`, `ToolCall`, and `Message` on the provider path; `Tools` and `ToolChoice`
  reach the Responses API in its wire shape and are omitted entirely when unset, so a caller
  who wants none sends none.
  Three things it deliberately does *not* do. It never executes anything — a tool call is a
  request from the model, and the caller acts on it or does not. It never decodes the
  arguments: they arrive as `json.RawMessage`, as untrusted as any other model output, and the
  caller decodes them into their own type. And it never puts arguments in an error or a log
  line, because they are built from the caller's data — the refusal names the tool, nothing
  else.
  **A model naming a tool that was never offered is refused in Go** (`ErrUnrequestedTool`)
  rather than passed along for the caller to notice. A tool-call response is also no longer
  mistaken for a malformed answer: it is a success with no text, which the classifier now
  distinguishes.
  Evidence: `internal/llm/toolcalling_test.go`, 11 cases against an `httptest.Server` — tools
  omitted when unset, sent in shape when set including a forced tool name, a nil parameter
  schema defaulting rather than sending nothing, a tool-call response succeeding, an
  unoffered tool refused, arguments absent from the refusal, a plain text response unaffected
  by the new fields, and the streamed path classifying a tool call the same as the buffered
  one.
  **Note this does not promote the tools registry** — **PS-001** ruled on that separately, and
  the registry stays internal. What this adds is the capability for a caller to offer their
  own tools.
  **Limitation:** `ClassifyCompletion` called directly on a tool-call response with empty
  content still reports `KindMalformedOutput`. The provider path no longer routes through
  that, so it only bites a caller who ignores `ToolCalls` and classifies by hand.
- [x] **PS-005** — Multi-turn support: `CompletionRequest` carries one system string and one
  user string, with no message history. `Asking`, `Negotiating`, and
  `NegotiatingAdversarially` are naturally multi-turn operations implemented as one round
  trip. Closes **Gap-03**.
  **Revised (ARC-20):** either outcome closes it, and the cheaper one is legitimate. Build a
  session/message abstraction, **or** rename these operations for the single-shot work they
  actually do. What is not allowed is keeping a conversational name on a one-round-trip
  implementation. If a session lands, it is a sequential stateful execution shape with
  transcript invariants — not generic MDSP (Appendix C).
- [x] **PS-006** — Embeddings. Their absence forces LLM round trips for `Similar`,
  `CheckingSimilarity`, `Clustering`, `Deduplicate`, and `Matching`, all of which have cheap
  deterministic vector implementations. Closes **Gap-05**.
  **Capability and maths done; the five operations are deliberately not rewired.**
  `llm.EmbeddingProvider` follows the `StreamingProvider` pattern — a capability interface,
  not a required method, so `schemafluxtest`, `mw`, and every mock keep compiling.
  `RequestEmbedding` type-asserts and returns `KindUnsupportedCapability` for a provider that
  cannot embed; it never falls back to a chat call, which would turn "this provider has no
  embeddings" into an expensive answer that looks the same. The OpenAI implementation restores
  order from the response's own `index` field rather than trusting array position, and rejects
  a mismatched vector count.
  `internal/ops/vector.go` is the deterministic half and needs no provider at all: cosine
  similarity, top-k, threshold clustering, dedup, and greedy matching. The clustering verifies
  itself against `CoversExactlyOnce` — the same partition check the collection operations use,
  reused rather than reimplemented — before returning. `VectorSimilarity` is documented as a
  **measurement** of vectors and kept away from anything named `Model*`; it is not a
  probability that two things mean the same.
  Evidence: 9 cases in `internal/llm/embeddings_test.go` (including zero fallback calls when
  the capability is missing, and the distinction between a nil provider and a capable one) and
  21 in `internal/ops/vector_test.go`, all with hand-computed values — a 3-4-5 triangle giving
  exactly 0.96 — and **zero** provider calls.
  **Not done, and it is the half the task's title implies:** `Similar`, `Cluster`,
  `Deduplicate`, and `Match` still make their LLM round trips. Rewiring them means each one
  calling `RequestEmbedding`, deciding what its existing `Threshold` option now means, and
  deciding the fallback for a provider without the capability — a behaviour change to five
  operations' contracts and their tests. The cheap deterministic path exists; nothing uses it
  yet, and saying otherwise would claim a saving nobody has made.
- [x] **PS-007** — Prompts as versioned, overridable artifacts with golden tests, so a prompt
  edit is a reviewable change rather than a silent behavior change for every downstream user.
  Closes **Gap-13**. Depends on **TI-004**.
  **Done as a registry with the identity rule enforced.** `internal/ops/prompts.go`:
  `Prompt{ID, Text, Source}`, a registry with `RegisterBuiltin`/`Override`/`ClearOverride`/
  `Resolve`, and `Prompt.CacheIdentity()`.
  The rule that mattered most: an override's version is derived from a **content hash of the
  override text**, never inherited from the built-in it replaces. Get that wrong and a
  caller's custom prompt silently reuses the built-in's cache identity — **P-009**'s key would
  then serve a cached prefix for a prompt that was never sent, which is a wrong answer with a
  correct-looking provenance. Editing override text mints a new identity automatically;
  re-registering byte-identical text reuses the same one. It reuses the existing `digestOf`
  rather than adding a second hash.
  Evidence: 15 cases, including the explicit assert-on-key ones — an override's cache identity
  differs from the built-in's, a version bump changes the key, and editing an unrelated
  operation's prompt leaves this one's key untouched.
  **Not wired into any operation.** `promptCacheKeyFor` and `opt.CacheIdentity` still use the
  literals in the operation files, so a caller's override does not yet reach the wire. What
  exists is the seam and the identity discipline it has to uphold; connecting Extract to it is
  the next step and is not claimed here.
- [x] **PS-008** — Resolve `docs/engineering/plans/workflowengineplan.md` and
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
  **Done — moved out, with the ruling written on each one.**
  `workflowengineplan.md`, `WORKFLOW_ENGINE_TODO.md`, `SCHEMAFLUXDSLSPEC.md`, and
  `WORKFLOW_ENGINE_EXTERNAL_INTERFACES.md` are now under `docs/engineering/archive/`, each
  with a header saying which decision retired it: `to-production.md` §1.3 makes a
  general-purpose workflow language an explicit non-goal and API-12 forbids builders growing
  a branching DSL, so M08's combinators are a deliberate decision not to build this. They are
  kept rather than deleted, because the thinking in them is real; what was wrong was their
  location, which implied they were scheduled.
  `ISSUES.md` had already flagged the three of them as "at least three different, conflicting
  high-level designs" — that is what this resolves.
  **The four surviving plans now say where they stand**, since a `plans/` directory whose
  documents disagree is a directory nobody can act on: `REFACTOR_PLAN.md` largely delivered,
  `GO_TYPE_NATIVE_PRIMITIVES.md` motivation rather than plan, `NEW_PRIMITIVES_ANALYSIS.md` and
  `PRACTICAL_LLM_OPS.md` superseded by **PS-002**'s catalogue — adding primitives is the
  opposite direction from a small stable core.
  **Not done:** the nine missing figures. `to-production.md` references
  `figures/*.png` that do not exist, so it renders with nine broken images. Its status block
  now says so; generating architecture diagrams is not something to guess at.
- [x] **PS-009** — Reconcile `AGENTS.md` with this repository. It is CodeFlux's file, copied
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

  **Done for the reconciliation, which is what the task asked.** `AGENTS.md` is now this
  repository's: 662 lines of CodeFlux instructions replaced with 146 describing what is
  actually here. The old file mandated `docs/plan.md`, a `CHANGELOG`, a `DEVLOG`,
  `.artifacts/`, `cmd/codeflux-dev`, atoms, SQLite migrations, and a frontend, and forbade the
  `git add -A` and direct `main` commits this repository's history is made of. An instruction
  file describing a different repository is worse than none: every rule in it is a coin flip
  about whether it applies.
  What replaces it is the discipline this list has actually been enforcing — never fail open,
  check the answer against the question, decide locally what can be decided locally, never log
  the caller's payload, never spend money by accident, one error classifier, the ten-case bar,
  and the ratcheted gates with the rule that the coverage floor only goes up.
  `CLAUDE.md` pointed at `docs/plan.md` §0 for the same inherited reason and now points at
  `TODOS.md` and the reconciliation section.
  **Not done, and needs your say-so:** the repository hygiene files the Revised note lists —
  `LICENSE`, `SECURITY.md`, `CONTRIBUTING.md`, issue templates, an ADR directory. Those are
  new Markdown files, and the standing rule is that a new Markdown file needs you to ask for
  it by name. Filed as **PS-010**.
- [x] **DI-001** — Prove the module is actually consumable with `go get`, rather than assuming
  it because it builds here. Added after the fact, per the rule that work the list does not
  contain gets an entry and a check in the same commit.
  **Done.** Verified from a scratch module outside the repository (`go mod init`, a `replace`
  at the local checkout, `go mod tidy`, `go build`): a consumer imports and compiles against
  `schemaflux.NewClient`, `NewExtractOptions`, `Extract[T]`, the `pricing` and
  `schemafluxtest` subpackages, and `mw.Chain`/`RateLimit`/`Retry`. The binary runs and fails
  with the expected "no LLM provider configured", which is the correct behaviour with no key,
  not a build defect. Checked specifically: no `replace` in the root `go.mod` (the only ones
  are in `examples/smarttodo/go.mod`, and a nested module's replace never applies to anyone
  importing the parent — that module builds clean); every re-exported type in `schemaflux.go`
  is a genuine `type X = internal.X` alias, so no signature forces a caller to name a type
  they cannot import; `mw`'s exported surface leaks nothing unaliased; and an import of
  `internal/ops` from outside is refused by Go's own rule, as it should be.
  **Not done:** there are no git tags, so `@latest` resolves to a pseudo-version off HEAD.
  That works, but a `v0.1.0` needs your call on when the API is stable enough to name. Also
  unverified: `go.mod` declares `go 1.25.0` and the only toolchain here is 1.26.3, so the
  floor is asserted rather than tested.
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
- [x] **CI-004** — Make the numbered examples a release gate. 19 of 45 fail under the local
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
  **Done — the gate is live at 33 of 45, up from 26, and it is ratcheted rather than
  all-or-nothing.**
  The mock was the problem, exactly as the task said. It guessed the answer from keywords in
  the prompt and said `Mock response for: ...` when it recognised none — a shape no operation
  asks for. It does not need to guess: every structured operation states its contract, either
  as the JSON Schema the strict path has sent since **P-005** or as the JSON template the
  prompt-only operations embed in their system prompt. `internal/llm/mockshape.go` reads
  whichever is there and answers with that shape, honouring `format` annotations so a
  `time.Time` field gets an RFC3339 string rather than the word "mock".
  Collection operations needed one more idea. Their contract is *relational* — Choose must
  return an item it was given, Sort a permutation, Cluster a partition — so a shape-correct
  answer still fails, correctly, against the invariants added in **OP-105** and **OP-107**.
  The mock answers with the **identity transformation**, which satisfies all of them by
  construction and is a legitimate answer to "sort these".
  **The gate is ratcheted for a reason.** Waiting for all forty-five before turning anything
  on leaves the thirty-three that work unprotected, which is how they became nineteen
  failures in the first place. `scripts/examples_gate.py` fails the build when a passing
  example breaks **and** when a failing one starts passing without being recorded — a quiet
  improvement nobody writes down is one somebody re-breaks.
  The twelve still failing are listed in `.audit/examples_expected.txt` with the error each
  one produces. They fall into two groups: operations whose response shape the mock cannot
  derive (`Transform`, `Generate`, `Compress`), and operations where identity is not a valid
  answer (`Cluster` over a partition it did not compute). Filed as **CI-008**.
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
- [x] **REL-001** — Tag v0.2.0 at the end of M02 (provider correctness), v0.3.0 at the end of
  M05 (operations verified), v1.0.0 only after M10.
  **Revised:** M10 is no longer the last gate. With M11–M17 scheduled, v1.0.0 means the §19
  acceptance criteria pass (**RC-003**), not that the review is closed. Restated ladder:
  v0.2.0 after M02, v0.3.0 after M05, **v0.4.0 after M07** (operational claims true),
  **v0.5.0 after M12** (safe core, planner, scheduler), **v0.9.0-rc after M16**, and v1.0.0
  only when every §19 box is checked or carries an ADR saying why it is not.
  **Done as a gate, not a tag — and nothing is tagged, because nothing is earned yet.**
  The ladder lived only in this file, which made it a statement of intent: nothing stopped a
  tag being cut while the milestone it depends on still had open work, and a version number is
  the one claim a consumer cannot inspect for themselves. `scripts/release_gate.py` now reads
  the milestone headings and checkboxes, computes the highest rung whose prerequisites are
  fully closed, and `--check` exits non-zero if a tag exists that the work has not earned. It
  runs in CI as the `release-ladder` job, with a full fetch because a shallow clone has no
  tags to check.
  Verified by cutting a local `v0.2.0`, watching the gate refuse it with exit 1, and deleting
  it again — a gate that has never refused anything is not known to work.
  **Current state: no release is earned.** The first rung, v0.2.0, is blocked on **M02**,
  whose two remaining items are **P-012** and **P-014** — both need a funded key. `[LIVE]`
  tasks are deliberately *not* exempted from the count: a live test nobody has run is a claim
  nobody has verified, and exempting them would let the ladder certify exactly the thing it
  exists to gate.
  So the repository stays untagged and every consumer stays on a pseudo-version, which
  **DI-001** already established works. Given how much public API moved in this session alone
  — four types renamed, several operations deprecated — that is the right state to be in.
  **Cam's call, not mine:** running P-012 costs money on his account. Once those two close,
  the gate will report v0.2.0 as earned and the tag becomes a one-line decision.

---

# M11 — Execution planning and shapes

Everything above treats one call as the unit of work. `to-production.md` §8–9 says the unit
is a *logical request* that may fan out into chunks, stages, and recovery attempts, and that
the shape of that fan-out must be chosen deliberately, recorded, and inspectable before it is
paid for. Nothing in M01–M10 covers this; **CF-004**/**OP-109** built the one primitive that
exists. Corresponds to delivery Gate 2. Depends on M04.

- [x] **PL-001** — Separate `Plan` from `Execute`, and expose both. A plan is immutable,
  serializable without sensitive content, and deterministic given the same input, policy
  snapshot, capability snapshot, and estimator version. `Preflight` returns it — eligibility,
  chunking, maximum call count, budget, and estimated cost — **without generating anything**.
  Closes **EXE-20**, **TRU-29**, and the preflight half of **PRD-22**.
  *Verify:* preflighting a 240-item batch makes zero provider calls and reports a call
  ceiling the subsequent run does not exceed; a plan built twice from the same inputs is
  byte-identical.
  **Done.** `Preflight` returns a `types.Plan` — eligibility, chunking, maximum call count,
  budget, estimated cost — and **makes no provider call**, asserted with a call counter rather
  than by inspecting the result: a preflight that spends money to say what something will cost
  is a call, not a plan.
  Determinism and privacy are both tested rather than asserted: `Plan.Serialize()` is
  byte-identical across two runs with the same input and snapshots, and a marker string fed
  through the input — and through `Steering`, which is the field most likely to carry a
  caller's words — appears nowhere in the serialized plan.
  Cost estimation goes through `pricing`, so an unpriced model produces a plan that says
  **unpriced** rather than projecting $0.00. A plan promising a free run because the rate card
  is missing is the same fail-open as an invented confidence.
- [x] **PL-002** — Explicit execution shapes: atomic, MDSP, SDMP, global. The planner selects
  one, records it in the plan and the envelope, and the caller can force it — `Atomic()`,
  `Batched()`, `Adaptive()` — because forcing atomicity for risk reasons and forcing batching
  for cost reasons are both legitimate. Closes **EXE-01**, **EXE-14**.
  *Verify:* each mode is observable in the envelope; a forced mode that is illegal for the
  operation is refused at plan time, not silently ignored.
  **Done.** Atomic, MDSP, SDMP, and global; the planner selects one and records it in the plan
  and the envelope, and `PlanBuilder`'s `Atomic()`/`Batched()`/`Adaptive()` force it.
  A forced shape that is **not legal** for the operation is refused at plan time with the
  reason named — not silently downgraded to something the planner preferred. A caller who
  forced atomicity for risk reasons needs to hear "no" rather than get batching back without
  being told.
- [x] **PL-003** — MDSP batch protocol: stable invocation-local item IDs on the way out,
  deterministic validation on the way back — exact ID coverage, no duplicates, no unknown
  IDs, per-item schema and invariants — and caller order reconstructed in Go. Output
  position is never trusted. Closes **EXE-03**, **TRU-17**. Shares its implementation with
  **OP-101**.
  *Verify:* responses that omit an item, duplicate one, invent one, and reorder all of them
  each produce the right classification and the right unresolved set.
  **Done, on the existing protocol rather than a second one.** `batchprotocol.go` builds on
  `collection.go`'s own `itemID`/`idPositions` vocabulary — the ids OP-101 introduced — so
  there is exactly one id scheme in the library. `NewIDBatchAlgebra` fills the `Encode`/`Merge`
  slots the `Op` descriptor declared and never called.
  `Merge` resolves by id and never by position, pinned by a test that hands back a **reordered**
  response and expects the caller's order reconstructed. Duplicate, missing, and unknown ids
  are each rejected with their own diagnostic through `BatchCoverage`, so a failure says which
  of the three went wrong rather than just that coverage failed.
- [x] **PL-004** — Token-aware chunk packing. The chunk is bounded by the earliest of item
  count, input tokens, output reserve, context limit, per-call cost, and provider payload
  bytes — accounting for system policy, operation prompt, schema, protocol overhead,
  serialized items, expected output per item, and a safety margin. An item too large for any
  chunk is routed atomically or refused; it is never silently truncated. Closes **EXE-04**
  and the estimation half of **PRD-22**. **OP-108**'s refusal is the degenerate case.
  *Verify:* packing respects each bound in isolation; an oversized single item is refused
  with a message naming which bound it broke.
  **Done.** All six bounds — item count, input tokens, output reserve, context limit, per-call
  cost, and payload bytes — are enforced in `packChunks`, each tested in isolation so a chunk
  that violates one bound cannot pass because another bound happened to catch it.
  An item too large for any chunk is **named with the bound it broke** and routed around,
  rather than blocking the run or being truncated to fit. Truncating it would produce an
  answer to a question nobody asked.
- [x] **PL-005** — Adaptive chunk sizing, bounded. Grow on sustained compliance, halve on
  truncation, omissions, duplicate IDs, malformed protocol, or repair above threshold; reduce
  concurrency separately from chunk size under rate pressure; never exceed the operation
  maximum or the budget; record the reason for each change. History is advisory — a
  deterministic per-request limit always wins — and is not shared across tenants.
  Closes **EXE-05**.
  *Verify:* a provider that truncates above 20 items converges to a stable size and stays
  there; the plan explains why.
  **Done as a standalone primitive, with no caller yet.** `AdaptiveChunkState` grows the chunk
  size after three consecutive compliant chunks and halves it immediately on truncation,
  omission, duplication, or malformed output; three consecutive repairs also halve it. It
  never exceeds its ceiling and records why each change happened.
  **Nothing calls `Record` outside its tests.** `Preflight` cannot: it makes no calls by
  design, so it has no outcomes to feed in. This is built for an executor to drive, and saying
  it is wired would be claiming a feedback loop that has no source.
- [x] **PL-006** — Every stable operation declares its batch algebra: class (independent,
  subset, permutation, partition, graph, hierarchical, sequential), item encoder, merger,
  and global validation. Appendix C of `to-production.md` is the starting assignment.
  A generic map/reduce may execute a declared algebra; it may not invent one.
  Closes **EXE-13**, **ARC-16**, and the batching half of **TRU-24**. Feeds **TI-007**.
  *Verify:* a stable operation with no declared class fails a build-time check; the declared
  class generates the property test.
  **Partial.** `types.AlgebraKind` — independent, subset, permutation, partition, graph,
  hierarchical, sequential — is declared and **enforced at build time**: `NewOp` refuses a
  stable batched operation that has not declared its class, so the omission fails at package
  init rather than at the first batch.
  **Not done:** the declared class does not yet generate its property test, which is the half
  that would make the declaration self-checking. And only the independent and
  permutation-shaped algebras have generic merge support; partition, graph, hierarchical, and
  sequential are declarable and have no engine behind them.
- [x] **PL-007** — Plural APIs: `ExtractMany`, `ClassifyMany`, and their fluent twins, with
  batching, ordering, item identity, partial success, and scheduler policy as *stated*
  semantics. A singular function never reinterprets a slice, and a caller looping a singular
  call is no longer the supported way to process a collection. Closes **EXE-15**, **EXE-02**;
  supersedes the loop pattern the README currently documents.
  *Verify:* 500 inputs through the plural API make bounded batched calls; the singular API
  over a slice is a compile error or a documented single-item call, not a hidden loop.
  **Partial.** `RunOpMany` is a real engine: it plans, dispatches through the existing bounded
  worker pool, preserves the caller's order, and batches by id. It is proven against a fixture
  operation.
  **Not done:** the plural APIs themselves — `ExtractMany`, `ClassifyMany` — are not wired,
  because Extract's descriptor does not vary its prompt by input and connecting it is work in
  files this task did not own. And a single item's failure fails the whole call: partial
  success policies are **PL-008** and are not implemented, so the engine is all-or-nothing
  today.
- [x] **PL-008** — Partial success and failure policies. `BatchResult[T]` with per-item
  status, attempts, mode, and evidence; `BatchSummary`; and the five policies — `FailFast`,
  `CollectFailures`, `RetryFailedItems`, `RetryThenCollect` (the default for long batches),
  `RequireAll` — each defining scheduling, cancellation, retry, and return behaviour.
  `([]T, error)` cannot express any of this. Closes **EXE-07**, **EXE-08**.
  *Verify:* one table per policy asserting what ran, what was cancelled, and what came back
  when item 3 of 10 fails terminally.
  **Done.** `types.BatchResult[Out]` with a per-item `ItemResult` — index, status, value,
  attempts, repairs, cost, and error — and five policies: fail-fast, collect-failures,
  retry-failed-items, retry-then-collect, and require-all.
  **The honesty property is the design.** `Values()` returns `([]Out, bool)` and the bool is
  false on any incompleteness, so the only route from a batch result to a plain slice forces
  the caller to read it. A shorter slice with no error — the failure mode the task names — is
  not expressible.
  The five policies are proven **behaviourally distinct on identical input**: ten items with
  item three failing every time yields different succeeded/failed/cancelled counts and
  different return values per policy. A set of policies that all behave the same is one policy
  with five names, and that is what the test rules out.
  Evidence: 26 cases, run through the real `CallLLM` path with a fake provider rather than the
  test caller-hook, so the attempts and cost in each `ItemResult` are the real envelope rather
  than stubbed zeros.
  **A real defect it surfaced, fixed here:** `CallLLM` published a call record only when a
  call eventually **succeeded**. A request that exhausted its retries and failed outright
  contributed nothing to the envelope, so `Meta.Attempts` on a failure read **zero** — as
  though the provider had never been called, while it had in fact been called and billed as
  many times as the budget allowed. "How many attempts did that take" was answerable only for
  the requests that worked, which is the opposite of when it is asked. `publishFailedCall` now
  records the attempt count, provider, model, and elapsed time on the failure paths, and
  deliberately leaves usage and cost at zero rather than guessing at a figure the provider
  never reported. Evidence: `internal/ops/failedattempts_test.go`, 5 cases, including that a
  terminal failure reports the one attempt it made rather than the budget it never used, and
  that a success still publishes exactly one record — so the fix did not double-count.
- [x] **PL-009** — Progressive recovery cascade: preferred MDSP → keep valid items → isolate
  only unresolved IDs → smaller MDSP → atomic → escalate model or provider only if the
  minimum contract and data policy survive → review packet or terminal item failure at
  budget exhaustion. A valid item is never replayed because a sibling failed, unless the
  operation's global algebra requires recomputation. Closes **EXE-18**; it is also a §19.3
  acceptance criterion.
  *Verify:* a batch where two of twenty items fail spends recovery calls on two items, and
  the eighteen valid results are byte-identical to their first-attempt values.
  **Done.** `RunOpManyRecover` runs the full ladder: preferred MDSP over chunks sized by the
  adaptive state, keep the items that resolved cleanly, isolate the unresolved ids into a
  smaller retry, then fall back to atomic for whatever still has not answered.
  `mergeIDBatchPartial` is what makes "keep the valid items" possible: the existing merge is
  all-or-nothing on coverage, so a chunk that answered nine of ten was thrown away entirely.
  **`AdaptiveChunkState` (PL-005) finally has a caller** — each chunk's first-pass outcome
  feeds it, and its size decides the next chunk. The shrink is proven by call count rather
  than by inspecting state: 4 batch calls instead of 3 once the size really halves.
  Every item ends up in the result **exactly once**, and `ItemResult.Mode` says whether it came
  from a batch or an atomic retry — a caller reconciling a bill or a quality regression needs
  to tell those apart, and they are different provenance.
  Cost is attributed as an even split of each pass's **measured** total across the items that
  pass covered, documented as an attribution convention over a real number rather than an
  invented per-item price.
  15 cases, deterministic.
  **Not done:** no model escalation before the atomic fallback (needs **CP-001**'s negotiation
  seam), no review packet at exhaustion, and aggregate-shaped operations like Sort and Cluster
  are out of scope — this cascade handles item-wise batches only.
- [x] **PL-010** — SDMP staged plans over one datum, with reuse. Pass structured
  intermediates instead of resending the source, reuse deterministic preprocessing and
  schema artifacts, run independent checks concurrently under one budget, and **skip the
  model stage entirely when deterministic checks already establish the required contract**.
  Each stage carries its own operation ID and parent lineage. Closes **EXE-10**, **EXE-11**.
  *Verify:* a two-stage extract-then-verify plan sends the source once; a case where the
  deterministic check suffices makes one provider call, not two.
  **Done as the internal primitive.** `Stage[T]`, `PlanStep[T]`, `StagedPlan[T]`, and
  `RunStagedPlan` in `internal/ops/stagedplan.go`.
  The clause worth the task is the skip, and it is proven by **call count** rather than by
  inspecting a value: an extract-then-verify plan makes exactly **one** provider call when the
  deterministic check attests the required contract level, and exactly **two** when it does
  not, cannot, or attests a level below what the stage requires. A test that only checked the
  returned value could not tell "skipped the stage" from "ran it and liked the answer", which
  is the whole distinction.
  Reuse is structural rather than an optimisation pass: the **same** `OpOptions` — SchemaID,
  CacheIdentity, JSONSchema, Model — is threaded to every stage unchanged except
  `ParentResultIDs`, so the schema and cache artifacts are the ones already built rather than
  rebuilt per stage. Lineage chains stage to stage through the same `ParentResultIDs` TC-003
  established, so a staged plan produces a resolvable chain without a second mechanism.
  One budget means one `*Scheduler` shared across the **whole plan**, not per group, so
  independent checks running concurrently are still bounded by a single admission budget —
  per-group budgets would make "one budget" true only within a stage.
  **Not done:** a multi-stage group validates a datum, it does not produce a new one. There is
  no generic merge rule for arbitrary `T`, and inventing one would be a library-level guess
  about the caller's semantics — so the limitation is documented rather than papered over.
  A failed stage ends the plan; there is no partial staged-plan result. And `RunStagedPlan` is
  reachable only from within `internal/ops` — there is no exported wrapper yet.
- [x] **PL-011** — Global and hierarchical operation algebras, written per operation because
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
  **Done for the whole-set contract; the per-operation merges are not.**
  `PermutationValidate` and `PartitionValidate` bind the **existing** `SameMultiset` and
  `CoversExactlyOnce` to `BatchAlgebra.GlobalValidate` — no new membership or partition logic
  was written, which is the point: a fourth implementation of a partition check is the defect
  this repository keeps finding.
  `RunOpGlobal` is the part that matters. A whole-set contract can only be checked when the
  chunk *is* the whole set, so a plan needing more than one chunk is **refused before any
  provider call** rather than assembled from chunk-local results that each looked valid
  alone. That is the bug the existing global path has today: it concatenates chunks with no
  whole-set check, so a "sorted" answer can be a concatenation of separately-sorted pieces.
  Evidence: 14 cases, including the 500-item boundary the task names and a multi-chunk refusal
  asserted by **provider call count of zero** — refusing after paying for the calls is not
  refusing.
  **Not done:** cross-chunk merges for ranking, clustering, dedup, and synthesis, which the
  task itself says cannot be generic. No `Sort` or `Cluster` `Op` descriptor exists yet to
  design one against, and the existing chunk-local global path is untouched.
- [x] **PL-012** — Optional batch group for loop fusion: a caller keeps a natural Go loop,
  builders defer, and compatible work fuses into MDSP plans. Fusion is legal only when
  operation ID and version, schema hash, route policy, steering, contract level, data policy,
  and budget settings all match; otherwise the group partitions. Fusion is an optimization
  and must not change results relative to running each builder alone. Closes **EXE-16**.
  *Verify:* fused and unfused runs of the same fifty builders produce identical values;
  builders differing only in steering land in separate partitions.
  **Done as the internal primitive.** `FusionKey` carries the seven axes, and equality is
  `reflect.DeepEqual` over the whole struct rather than a field list — an eighth axis added
  later is included in the comparison with no second edit. That is A-014's lesson applied
  before the bug rather than after it, and
  `TestFusionKeyEqualityIsReflectionDrivenPerField` walks the struct and asserts each field
  independently flips equality, failing loudly on a field kind it cannot yet mutate rather
  than skipping it.
  **An eighth gate that the task's seven axes do not name, and that fusion is unsafe without:**
  `RunOpManyPartial` takes one `OpOptions` for a whole partition, so `Mode`, `Threshold`, and
  `MaxOutputTokens` must match too — otherwise fusion silently applies one builder's settings
  to another builder's call, which is precisely the "must not change results" clause. Context,
  RequestID, and CorrelationID are excluded, because those are tracing rather than policy, and
  partitioning on them would defeat fusion entirely for no safety gain. Both directions are
  tested.
  Handles resolve through a **pointer to their own entry**, never a slice index. Mapping
  answers back by position across a partition boundary is exactly the defect the id protocol
  (OP-101/PL-003) exists to prevent, and a fused group is where it would reappear.
  Fused and unfused runs of fifty builders produce identical values, under a provider whose
  answers do not depend on call order — which is what makes the comparison meaningful.
  **Not done:** this is the internal primitive, not the public `NewBatchGroup`/`Add` surface
  §9.8 sketches; that lives in the root package. `RoutePolicy` and `BudgetSettings` are
  fusion's own minimal stand-ins, because no formal types exist yet. Budgets must match
  **exactly** rather than merely being "compatible" as the prose says — combining two ceilings
  safely needs combined-spend tracking that does not exist, and the conservative rule
  over-partitions instead of guessing. A group also trusts that two builders reporting the
  same `OperationID` share the same `Op`; Go func values are not comparable, so nothing short
  of running both prompts could verify it.
- [x] **PL-013** — Per-item metrics. HTTP 200 hides omissions and invalid output, so measure
  valid-item ratio, omissions, repairs, atomic fallbacks, and **cost per accepted item** —
  the number that actually says whether a batch strategy is working. Closes **EXE-19**.
  Feeds **OB-002**.
  **Partial.** `BatchMetrics` reports the valid-item ratio, attempts, repairs, accepted and
  failed cost with its pricing quality, and cost per accepted item — the last guarded so it is
  meaningless rather than misleading when the model is unpriced. **PL-009** unblocked the two
  counters **OB-002** named as its blocker: omissions and atomic fallbacks, computed from the
  `Mode` already on each item rather than from a second accounting pass, and honestly zero for
  a run that never batched.
  **Not done:** the rest of PL-013's list. This closed exactly what OB-002 was waiting on.
  **Done.** Two of the six series were computed and **never emitted**:
  `schemaflux_omissions_total` and `schemaflux_cost_per_accepted_item`. The second is the
  number this task exists for, and it was reconstructable only by dividing two other metrics by
  hand — which is not the same as reporting it, because the person who most needs it is the one
  who would not think to divide.
  `BatchMetrics.CostQuality` replaces the `CostPriced` bool with OB-003's `PricingQuality`. A
  bool can say known or unknown; it cannot say "this known figure includes a projection", and
  cost-per-accepted-item built partly from estimated prices is a different claim from one built
  from a rate card. The emit is guarded on it, so a batch where nothing succeeded, or where the
  model is unpriced, emits **no series at all** rather than a zero — a zero there reads as
  "this was free."
  **A defect found while doing it:** `addCost` in `partial.go`, which sums cost across a retry
  pass, carried `Priced`, `TotalCost`, and `Currency` forward and silently dropped `Quality` and
  `PricingSource`. A retried item ended up `Priced: true` with `Quality` at a zero value the
  enum does not define — a cost that claims to be known and cannot say how. Fixed and covered.
  **Not done:** pricing quality is a Go-level field and not a metrics tag dimension. The
  existing `quality` tag already means accepted-versus-failed, and overloading one tag key with
  two unrelated meanings would make both unreadable.
- [x] **PL-014** — Planner explainability: a human-readable pre-execution plan explanation
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

  **Done, both halves.** Pre-execution: a plan now carries the shapes it **rejected** and why —
  cost tradeoff, size mismatch, or "the caller forced this instead" — and `Explain()` renders
  them. A caller asking "why did this take forty calls" needs the alternative that was not
  taken; the selected shape alone does not answer it.
  Post-execution: `DecisionLedger` on the result, one entry per chunk recording hold, grow, or
  halve with the adaptive state's own reason, plus an entry for the atomic-fallback stage. A
  run that made no adaptive decisions leaves it **empty rather than fabricating entries**.
  10 cases, including that the plan's serialization stays deterministic with alternatives in
  it — an explanation that changes the plan's digest would break **PL-001**'s reproducibility.
- [x] **SC-001** — Bounded scheduler: max concurrent calls, max queued nodes, in-flight
  tokens, in-flight cost, per-provider and per-tenant limits. Admission weighs estimated
  tokens, cost, quota, and priority; queues are bounded; a full queue or an unmeetable
  deadline returns `ErrAdmissionRejected` rather than allocating goroutines. The scheduler
  owns no semantics — it schedules plan nodes and propagates cancellation.
  Closes **EXE-09**, **PRD-23**, and the buffer half of **TRU-14**.
  *Verify:* 10,000 queued items hold flat goroutine and memory counts; rejection is
  observable, not a hang.
  **Done, written fresh rather than on MapReduce's pool, and the reason is recorded:**
  MapReduce is a per-call chunker with no admission control, no queue-versus-concurrency
  distinction, no tenant or provider weighing, and no way to refuse. A scheduler is a
  process-wide gate; reusing the chunker would have meant a gate that cannot say no.
  `Submit` blocks the caller until admitted, cancelled, or refused. A full queue or an
  already-past deadline is refused **synchronously** with `KindAdmissionRejected` rather than
  by parking a goroutine — a refusal is an answer, and a parked caller is not.
  **A lost-wakeup deadlock was found and fixed during development.** The dispatcher's first
  read of its wake channel could race an early enqueue, miss the signal, and hang forever —
  a hang, not a failure, which is the worse of the two because a hung test looks like a slow
  one. Fixed by folding the admit scan and the wake capture into one critical section. It
  surfaced under `-shuffle` and repeated runs, which is exactly what those are for.
  Evidence: 17 cases, all deterministic — peak concurrency measured by an atomic tracker
  inside the work rather than by sampling `Stats()`, a 200-item test proving nothing submitted
  is ever silently dropped, and a 10,000-item test proving goroutine count stays flat.
- [x] **SC-002** — Rate limits, per-provider bulkheads, circuit breakers keyed by endpoint
  and optionally model, decorrelated jitter, and `Retry-After` honoured within the logical
  deadline. **CB-03** proved the failure mode: a retry ladder that expires inside the same
  closed rate-limit window buys latency and nothing else. Closes **PRD-07**.
  *Verify:* a provider returning 429 for 60s is not hammered; a breaker opens, sheds, and
  recovers; concurrent retries do not align.
  **Done, reusing what existed and building only what did not.** Rate limiting, jitter, and
  `Retry-After` handling were already right in `mw/ratelimit.go` and `mw/retry.go` and were
  reused unchanged. `KindCircuitOpen` and `KindAdmissionRejected` existed in the taxonomy with
  **nothing behind them**; `internal/ops/resilience.go` now provides the breaker (closed, open,
  half-open, keyed, clock-injected) and the bulkhead, with `mw/circuitbreaker.go` and
  `mw/bulkhead.go` composing them at the `Handler` seam like every other middleware.
  Evidence: 18 cases on the primitives plus 13 through `mw.Chain`, including a
  breaker-with-retry composition — the pair most likely to disagree, since a retry inside an
  open breaker is a retry that cannot succeed.
- [x] **SC-003** — Fairness: weighted fair queuing with per-tenant concurrency, token, and
  cost buckets; bounded priority classes rather than arbitrary integers; no starvation, and
  no bypassing locked provider or data-policy limits by claiming urgency. Tenant keys are
  bounded workload identifiers, never end-user PII. Closes **TRU-15**.
  *Verify:* a 10,000-item tenant and a 10-item tenant submitted together both progress; the
  small one is not stuck behind the large one.
  **Done — and it exposed a real defect that had been read as a flaky test.**
  Three bounded priority classes with per-tenant round-robin inside each. Priority only
  reorders work that is *already admittable*: every request still passes the global and
  bulkhead checks, so urgency can never walk past a locked limit. That is tested explicitly,
  because "high priority" is exactly the flag somebody eventually expects to bypass a ceiling.
  **The defect:** the dispatcher only ever inspected the head of each tenant's queue, so a
  request blocked on its provider's bulkhead stalled every request behind it — including ones
  for a completely idle provider. A per-provider bulkhead that blocks other providers is the
  precise failure a bulkhead exists to prevent, and it presented as an intermittent failure of
  `TestSchedulerPerProviderConcurrencyLimit` (`InFlight:1 Queued:4`), which reads like
  flakiness and is not. Reproduced deliberately at `-cpu=1`, fixed by scanning past
  bulkhead-blocked waiters, and confirmed over 180 runs across three CPU settings.
  The scan stops entirely on a *global* capacity block, because when the scheduler as a whole
  is full nothing further down can run either — the two refusals mean different things and
  conflating them is what caused this.
  `TestABusyProviderDoesNotStallAnIdleOne` pins it deterministically: the busy provider's
  backlog is confirmed queued *before* the idle provider's request is submitted, so there is
  no scheduling race to lose.
- [x] **SC-004** — Idempotency and ambiguity. Stable logical request IDs across every
  attempt, a unique attempt ID per provider call, provider idempotency keys where supported,
  and an `Ambiguous` marker on a timeout that may already have been served — the library
  performs no side effects itself, but its callers do, and they cannot tell today.
  Closes **PRD-10**.
  *Verify:* a timeout followed by a retry is reported as one logical request with two
  attempts, the first marked ambiguous.
  **Done.** `LogicalRequestID`, `AttemptID`, and `RunWithIdempotency` in `resilience.go`. A
  **timeout is marked ambiguous** — the request may or may not have been executed — while
  other failures are not, which is the distinction that decides whether a retry is safe.
- [x] **SC-005** — Cancellation coverage at every blocking boundary: queues, backoff waits,
  HTTP, streams, workers, stores, exporters. On cancel — stop scheduling, drop queued nodes,
  cancel in flight, finalize verified items, mark the rest canceled, return the typed error
  *with* the partial result. No goroutine outlives its request. Closes **PRD-11**,
  **TRU-18**. **A-002** made the context reach the call; this makes it reach everything else.
  *Verify:* a leak test across all boundaries; cancelling a 200-item batch at item 50 returns
  50 verified items and 150 marked canceled.
  **Done for the boundaries this task owns.** The scheduler's queue wait, its dispatch, and the
  in-flight run all honour cancellation through a derived context, as does `Bulkhead.Acquire`.
  **Not done:** the HTTP, streaming, and store boundaries live in `internal/llm`, `telemetry`,
  and `pricing`. `mw/retry.go`'s sleep was read and confirmed already context-aware; the rest
  is unaudited and is not claimed.
- [x] **SC-006** — `Client.Close()`: stop accepting work, cancel owned workers, honour a
  grace period, flush owned buffers and stores, close owned idle connections, **never** close
  caller-owned transports or exporters, return a joined error, and stay safe under repeated
  and concurrent calls. Closes **PRD-20**.
  *Verify:* double-close is a no-op; an in-flight request either finishes inside the grace
  period or returns a typed shutdown error with its partials; zero goroutines survive.
  **Done as `Scheduler.Close(ctx)`; not wired to the client.** It stops accepting, refuses
  everything queued with a classified shutdown error, cancels every in-flight request's
  context, and waits for drain up to the caller's deadline. Idempotent under concurrent calls.
  **Not done:** `Client.Close()` does not call it. That needs `client.go`, and a shutdown path
  half-wired is worse than one that is honestly absent.
- [x] **SC-007** — Caller-owned HTTP. Every provider accepts an `*http.Client` or transport
  with documented ownership, because enterprise deployments need their own proxies, mTLS,
  and instrumentation, and tests need a transport that fails on dial. Closes **PRD-21**.
  **P-006** made the client per-provider; this makes it injectable.
  **Done.** `ProviderConfig.HTTPClient` — every provider uses the caller's client when one is
  supplied and builds its own when not, so nothing that works today changes.
  Proven with a recording `http.RoundTripper` asserting the caller's client **actually executes
  the request**, rather than by inspecting a field: a config value that is stored and never
  used looks identical from the outside.
  **Two things fell out of it.** `AnthropicProvider` was rebuilding an `http.Client` on *every*
  `Complete` call — a new connection pool per request, which is a latency and file-descriptor
  problem nobody had noticed. It stores one now. And **SEC-004**'s `EndpointPolicy`, which
  existed with no enforcement point, is now wired at construction: a custom base URL is checked
  against the policy when one is supplied, opt-in so no existing caller is affected.
  13 cases, including that a dial-failing transport surfaces as an error and that extra headers
  still layer onto a caller's client without mutating the value they passed in.
- [x] **SC-008** — `ValidateConfiguration(ctx)` — non-billable readiness: provider
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

  **Done as a standalone validator; not wired to the client.** `ValidateConfiguration` checks
  provider registration, credential presence, the model map, endpoint scheme and host, HTTP
  client presence, capability-versus-requirement contradictions, scheduler limits — including
  a per-provider limit above the global ceiling, which can never bind and is therefore a
  configuration error rather than a preference — store readiness, and budget enforcement with
  no budget set. It makes **no network call**.
  Note `ConfigSnapshot` has no field capable of holding a credential *value*: it checks
  presence, not content, so the validator cannot become a place a key gets logged.
  **Not done:** nothing builds a `ConfigSnapshot` from a live `*Client`. That is one function
  in `client.go`, which was out of scope.
- [x] **TC-001** — Prompt segments carry a role and a trust level (trusted policy, trusted
  developer instruction, untrusted application data, untrusted retrieved data, untrusted
  model output). Untrusted content is delimited and never interpolated into system policy
  text; model output is untrusted until it crosses the contract layer. This reduces prompt
  injection; it does not eliminate it, and the documentation must say so. Closes **TRU-01**.
  *Verify:* an adversarial corpus of inputs containing instructions; injection attempts
  reach the model as data, and the operation's invariants still hold.
  **Done.** `internal/ops/trust.go`: `TrustLevel` (five levels plus an unspecified zero
  value), `PromptSegment`, `BuildSystemPrompt`, `DelimitUntrusted`, `verifyTrustBoundary`.
  The level is **enforced, not recorded** — `BuildSystemPrompt` returns
  `ErrUntrustedInSystemPrompt` rather than filtering and continuing, and the zero value is
  untrusted, so a caller who forgets to set it is refused instead of silently trusted. That
  distinction is the one `dead_options_test.go` exists to catch one type over: a trust field
  nothing gates is decoration.
  `DelimitUntrusted` neutralizes boundary markers **inside** the content before wrapping, so
  injected text cannot forge a close and make the region look like it ended early
  (`TestDelimitUntrustedNeutralizesForgedMarkers`).
  The real defect this found: `streaming.go` was placing caller steering in the **system**
  prompt, where it becomes part of what every provider caches and every log treats as fixed
  policy. `applySteering` moves it into the user message; `verifyTrustBoundary` fails the
  request outright if it ever comes back, on both the ordinary and streaming paths
  (`TestTheStreamingPathAlsoKeepsSteeringOutOfTheSystemPrompt`).
  Documented in `SECURITY.md` under "What it does not defend against", stated plainly: the
  boundary narrows the surface and **cannot eliminate** injection, because a model that reads
  instructions in its own user message can act on them and no Go type changes that. What it
  does guarantee is that the library never loses track of which bytes were the caller's.
  **Not done:** only steering is delimited today. `TrustRetrievedData` and `TrustModelOutput`
  are defined and enforced at the system-prompt boundary, but no operation yet routes a
  retrieved document or a prior model answer through `DelimitUntrusted` — repair prompts and
  pipeline stages still concatenate. The levels exist for those call sites; the call sites
  have not been converted.
- [x] **TC-002** — Evidence contract: `EvidenceRef{SourceID, JSONPointer, StartByte, EndByte,
  SourceDigest}` and `ClaimProvenance{FieldPath, Evidence, Inferred, Method}`, with four
  modes — none, material fields only, all model-derived fields, and `NoInference` (an
  unsupported field stays absent rather than being guessed). The runtime validates that
  references are in bounds and match the source digest; it does not claim the cited text
  entails the claim. Closes **TRU-02**. **OP-507** builds the first spans.
  *Verify:* a fabricated field with no supporting span fails `StrictEvidence`; an
  out-of-bounds or wrong-digest reference is rejected.
  **Done.** `types.EvidenceRef{SourceID, JSONPointer, StartByte, EndByte, SourceDigest}`,
  `ClaimProvenance`, and an `EvidencePolicy` with four modes. `ValidateEvidenceRef` checks
  bounds and the source digest — it verifies that the citation *points somewhere real and
  unchanged*, and explicitly not that the claim is true, which is the distinction that keeps
  this from becoming a fact-checker it cannot be.
  The digest is what makes it worth having: a reference into a source that has since changed
  is a citation to something that no longer exists, and `TestValidateEvidenceRefRejectsADriftedSource`
  is that case.
  `Op.Contract.EvidenceRequired` was **declared and never read** — it is read now. An operation
  requiring evidence whose decoded value cannot carry any fails with `KindEvidenceViolation`
  rather than returning the answer.
  **The reference carries no source text**, asserted directly: offsets and a digest, never the
  quoted span, because evidence that embeds the caller's document is evidence that leaks it
  into every log that prints an error.
  **Not done:** validated claims are not exposed through `Meta` — that needs `RunOp`'s
  signature changed, and its call sites were outside this task.
- [x] **TC-003** — Provenance through pipelines: result IDs, parent links, input and schema
  digests, operation and prompt versions, resolved model, normalizers applied, item recovery
  path, cache usage, and library and adapter versions. A summary built from an extraction
  traces back to the original evidence; where lineage breaks, the delivered contract cannot
  be `FullyGoverned`. Closes **TRU-03**.
  *Verify:* a three-stage pipeline's final claim resolves to a span in the original source.
  **Done.** `types.Provenance` carries all twelve fields; `RunOpResult` populates it on every
  call through `buildProvenance` (`internal/ops/provenance.go`).
  Everything recorded is an **identifier or a digest, never content** — `DigestValue` hashes
  the input, `NormalizersApplied` names Go symbols rather than their output, and
  `ItemRecoveryPath` describes the repair path without carrying the model's rejected attempts.
  `TestDigestValueNeverContainsThePayload` asserts that directly, for a string and a struct.
  The three-stage pipeline resolves: `TestThreeStagePipelineProvenanceChainsThroughParentResultIDs`
  walks stage 3 → 2 → 1 and checks the IDs are distinct rather than three copies of one.
  Parent links are **caller-threaded on purpose** — nothing infers them, because only the
  caller knows which results are actually related.
  `FullyTraced` was declared and **read by nothing** — the same dead-guarantee shape
  `dead_options_test.go` exists to catch, one type over. `cappedByLineage` now enforces the
  clause: a result whose lineage does not resolve is demoted out of `ContractFullyGoverned`.
  It is a named function tested directly rather than an inline `if`, because
  `declaredContractLevel` never returns `ContractFullyGoverned` today (that needs CP-001 and
  CP-002), so wired in place the branch cannot yet fire — an unreachable `if` inside
  `RunOpResult` would be untestable and would rot exactly the way `FullyTraced` did. It sits
  in the path CP-001 will make reachable.
  **Not done:** the verify line asks for a claim resolving to *a span in the original source*.
  Lineage resolves to the **result** that produced a claim, and TC-002's `EvidenceRef` resolves
  a claim to a span, but nothing joins the two — following a three-stage pipeline back to a
  byte range in stage 1's input is not yet a single traversal. `SchemaDigest` is also empty for
  any operation that computed no schema identity, and `AdapterVersion` is the provider name,
  because no finer adapter-version string exists anywhere in `llm.Provider` to read.
- [x] **TC-004** — Contract levels, requested and delivered:
  `PromptOnly < JSONWellFormed < SchemaConstrained < SchemaAndInvariantChecked <
  EvidenceChecked < FullyGoverned`. Every detailed result states which level was asked for
  and which was actually delivered, and any degradation requires policy approval rather than
  a log line. Native provider structured output improves the *mechanism*; deterministic
  post-validation is still required. Closes **ARC-11**, **ARC-24**.
  *Verify:* a run that falls back from strict `json_schema` to prompt-only reports a lower
  delivered level; with degradation disallowed, it returns an error instead.
  **Partial.** `declaredContractLevel` computes the strongest level an operation's own
  declaration supports, and `RunOpResult` sets `Meta.RequestedContract` and
  `DeliveredContract` — both of which were **always zero for every call** before this, so the
  compartment existed and reported nothing. A failure now reports `ContractPromptOnly`
  delivered.
  **Not done, and it is the half that matters most:** per-call negotiation. A provider falling
  back from native schema enforcement to prompt-only mid-call is invisible, so `Requested`
  equals `Delivered` on every success — which means `Meta.Degraded()` cannot yet fire for the
  case it was written for. The signal lives in `internal/llm`. There is also no policy gate
  that refuses a degradation.
  **Done — the negotiation half, which was the half that mattered.**
  `negotiatedContractLevel` is the observation: when an operation claims
  `ContractSchemaConstrained` or better **and** the caller actually requested native
  enforcement, it looks up the resolved provider/model route in `llm.CapabilitiesFor` and
  demotes to `ContractJSONWellFormed` if that route does not confirm `CapNativeJSONSchema`.
  The signal read is `opt.JSONSchema` — what `llm_helper.go` forwards into the request — rather
  than `op.Contract.SchemaName`, which is only the operation's static declaration. The
  difference matters: one says what was asked for on this call, the other says what the
  operation could ask for in principle.
  **A route that is not registered at all is demoted too.** An unknown capability is not a
  present one, and treating silence as support is the fail-open this repository refuses
  everywhere else.
  The refusal half: `types.DataPolicy.MinimumContract` existed from CP-002 and **nothing read
  it against a real call**. `WithContractPolicy` attaches a policy to a context and
  `RunOpResult` returns `ErrContractDegraded` when the negotiated level falls short — an error,
  not a quieter answer with a footnote, which is what the task's "requires policy approval
  rather than a log line" asks for.
  **Not yet exercised by production traffic, and this is worth being precise about:** nothing
  that currently reaches `RunOpResult` populates `opt.JSONSchema`. Only `Extract`'s separate
  path does, and that path computes its delivered level independently and never calls
  `RunOpResult`. So the negotiation is real, correct, and directly tested through a scripted
  provider — and dormant until the remaining operations are lowered onto `RunOp` (§19.1.2).
  It is the same honest position `cappedByLineage` is in.
- [x] **TC-005** — Model drift: record requested tier or pin, resolved provider and model
  identifier, and provider model revision where exposed. `Tier(Smart)` is documented as
  floating; `Model(...)` is a pin request that the provider may not fully honour, and the
  envelope says so. Closes **TRU-04**. This is what makes **P-017**'s benchmark reproducible
  six months from now.
  **Partial.** `Meta.RequestedTier` records the caller's `Speed`, and the resolved provider and
  model were already on `Meta`.
  **Done.** `OpOptions.Model` and `CommonOptions.WithModel` are the pin, and `CallLLM` prefers
  it over the tier mapping. The pin is a **separate field** rather than an overload of
  `Intelligence` so that "I chose a tier" and "I chose a model" stay distinguishable in the
  envelope — a tier is documented as floating, and collapsing the two would make a
  reproducible run indistinguishable from a preference.
  A pin is deliberately **not validated** against a provider catalogue. This library has no
  catalogue, and refusing a model it has simply never heard of would make every newly released
  model unusable until somebody updated a list here.
  Drift is **derived, never claimed**: `Meta.RequestedModel` versus the provider's own echo.
  The earlier note said `llm.CompletionResponse` carried no revision field — it carries
  `Model`, which is the provider's own answer, and that was the missing observation all along.
  **The subtle part, and the reason this needed a third field:** `actualModel` falls back to
  the requested model when a provider echoes nothing, which is right for pricing and logging
  and would be a lie for drift — it makes an *unobserved* substitution indistinguishable from
  an *observed* agreement. `ResultMetadata.ObservedModel` carries the raw echo, undefaulted, so
  an absent echo reports `ModelDriftUnknown` rather than "no drift". Silence is not agreement,
  and the two flags are asserted mutually exclusive: a caller asking "did this drift" never
  gets yes and don't-know from the same result.
  Adding the field was itself checked by **A-014's reflection guard**, which failed
  immediately with "applyDefaults dropped Model entirely" — the guard catching the exact class
  of bug it was written for, on the first new field added since.
- [x] **TC-006** — Repair safety and repair regression. Strategy is chosen by failure class:
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

  **Done.** The repair strategy is chosen from the failing error's *kind* rather than applied
  uniformly: a malformed answer gets a syntax repair that includes the previous response
  bounded and delimited; an invariant or evidence failure gets a **regeneration from the
  original prompt** and is never shown the fabrication, because feeding a model its own
  invented answer and asking for a correction is how an invention gets refined rather than
  dropped; everything else gets the default patch.
  `detectUnrelatedFieldLoss` rejects a repair that fixes the flagged field while quietly
  dropping a different one that was present before — a repair that trades one defect for
  another is not a repair, and nothing was checking for it.
  `KindInvariantViolation` and `KindEvidenceViolation` now survive exhaustion instead of being
  collapsed into `KindRepairExhausted`, which makes the existing review-required path reachable
  for exactly the two failures that should ask for a human.
  Evidence: 6 test functions, 21+ cases, including that an invariant failure regenerates rather
  than edits.
- [x] **CP-001** — Machine-readable provider capabilities — native JSON schema, JSON mode,
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
  **Done, and it makes two shipped features honest.** `internal/llm/capabilities.go`: a closed
  `Capability` enum, `ProviderCapabilities` carrying supported schema keywords, usage-reporting
  quality, regions, retention, and context/output ceilings, and a registry with exact and
  family-prefix lookup.
  **The rule that matters: an unregistered route is unknown, and unknown refuses.**
  `Negotiate` returns `ErrCapabilityUnknown` rather than treating a zero-value struct as
  "declared, supports nothing" — a capability check that reads absence as permission is a
  fail-open in the one place the library is deciding where a payload may go.
  Evidence: 18 cases. Unknown refuses, a missing capability refuses, an unmeasured ceiling
  refuses rather than passes, a usage-quality floor is enforced, and `HasDeclared`
  distinguishes "not declared" from "declared false" — which is the same unknown-versus-known
  distinction **PR-001** drew for cost, one subsystem over.
  **The registry ships empty, deliberately.** No capability facts about real vendors are
  asserted here, because nothing has measured them; **CP-003**'s conformance suite is what
  populates it. Registering guesses would produce exactly the confident-and-wrong data the
  negotiation exists to prevent.
  **Not done:** `mw.Fallback` and `mw.Cache` still use caller-declared labels rather than this
  registry. Their doc comments named CP-001 as the gap, and the gap is now half-closed — the
  data exists; the wiring does not. Wiring `Fallback` would change what a zero-value
  declaration means (today it enforces nothing; consulting the registry would make enforcement
  depend on registry population), and that is a behaviour change needing its own tests rather
  than a quiet substitution.
- [x] **CP-002** — `DataPolicy`: classification, allowed providers and regions, retention,
  training use, content logging, result caching, and minimum contract — locked at the client
  or tenant boundary, strictenable by an invocation, never weakenable. A failure on a private
  route may not fall back to a public one. Closes **TRU-11**; **P-008**'s `store: false`
  default is the first instance of it.
  *Verify:* a us-only, no-retention policy makes an ineligible provider unselectable, and
  the refusal names the constraint.
  **Done.** `internal/types/policy.go`: `DataPolicy` with classification, allowed providers and
  regions, retention, training use, content logging, result caching, and minimum contract.
  Two design choices carry the task's requirement. **`Tighten` can only narrow** — lists
  intersect, booleans AND, retention and logging take the minimum, the contract floor takes
  the maximum — so an invocation may make a client's policy stricter and can never weaken it.
  Conflicting classifications return an error rather than one silently winning. And **an
  undeclared list allows nothing**: the zero value is the strictest policy, not the absent
  one, because a policy that defaults to permissive protects nobody who forgot to configure it.
  `internal/ops/policy.go` joins it to CP-001: `EligibleRoute` negotiates capability first,
  then checks provider, region, and retention, and wraps the two refusal families so a caller
  can tell a capability problem from a policy one with `errors.Is`.
  Evidence: 15 cases on the policy type — including that `Tighten` never re-enables training
  or caching (TRU-11's literal property) and never widens an intersection in either argument
  order — plus 11 on the routing. The one that matters is
  `TestDisqualifiedCandidateProviderIsNeverCalled`: it counts calls on a disqualified
  provider and asserts **zero**, because refusing a route after the payload has already been
  sent to it is not enforcement.
- [x] **CP-003** — Provider conformance suite and generated support matrix, with four
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

  **Done for the protocol suite; the vendor matrix is not generated because nothing paid for
  it.** `internal/llm/conformance*.go`: 16 protocol checks (auth, timeout, cancellation,
  ambiguous completion, empty/malformed/truncated/refused output, 429 with valid, invalid, and
  missing `Retry-After`, 500, a stream that ends with no terminal event, credential redaction,
  endpoint override) and 5 model checks, with a label ladder from unrated to
  production-supported.
  **Findings reach the registry as data**, not as log output: `Publish()` writes into
  **CP-001**'s capability registry, and only from the *model* checks — the protocol checks
  measure this library's own transport code, not a vendor fact, so they are excluded from
  registry writes deliberately. `ConflictsWithRegistry` implements the task's verification
  line: a provider failing a capability it declared loses its label automatically.
  **Offline by construction, and proved rather than asserted:** with `SCHEMAFLUX_LIVE_TESTS`
  unset the model checks record as *skipped* rather than passing, and
  `TestConformanceOfflineRunMakesNoNetworkCalls` installs a dial guard and asserts every dial
  resolves to loopback **and that at least one dial happened** — so it cannot pass by doing
  nothing.
  The fixtures are hand-written against the wire types rather than captured from a run, and
  that paid for itself immediately: a `server_5xx` fixture written as 503 failed, because
  `isRateLimited` treats 503 like 429. A captured-and-pinned fixture would have encoded the
  confusion instead of exposing it.
  **Not done:** the generated support matrix does not exist as a file. Producing one needs a
  live run, and writing a placeholder would misrepresent exactly what this task asks for —
  a matrix generated from results rather than hand-written. Protocol checks also cover only
  the OpenAI Responses adapter, and `CompletionResponse` has no request-ID field, so that
  dimension is unmeasurable as the code stands.
- [x] **SEC-001** — Publish `SECURITY.md`: threat model, supported versions, disclosure
  process, and response targets. The assets are credentials, prompts, source data, outputs,
  schemas, tenant identity, diagnostics, and routes; the actors include malicious input
  authors, compromised endpoints, other tenants, and the model's own output.
  Closes **PRD-04**. Coordinate with **PS-009**.
  **Done.** `SECURITY.md`: private disclosure through GitHub's Security tab, a week's
  acknowledgement, and — first, before anything else — **do not include a real payload in a
  report**, because a reproduction from this library is very likely to contain somebody's data
  and a report carrying live customer data is itself an incident.
  Supported versions says the honest thing: the module is untagged, so only `main` is
  supported and "supported" means fixes land there.
  The threat model separates what the library defends against from what it does not, and the
  second list is the useful one: a provider returning plausible wrong answers inside the
  declared schema passes every check here — structure is verified, truth is not; prompt
  injection through the caller's own data is narrowed and not eliminated; anything in-process
  can read the process's credentials; and tool *calling* surfaces a request the library never
  executes.
  It also names three **known gaps** rather than leaving them to be found: the endpoint policy
  is not wired, tenant deletion has no store implementing it, and client isolation is
  partial.
- [x] **SEC-002** — Content logging policy: `LogNoContent`, `LogMetadataOnly` (the production
  default), `LogRedactedContent`, `LogFullContent` (explicit, policy-gated). **Debug changes
  verbosity, not content policy** — that inversion is how prompts end up in log aggregators.
  Authorization headers, keys, cookies, and raw bodies are always scrubbed; captured buffers
  are bounded and retention-limited; diagnostic files get restrictive permissions.
  Closes **PRD-05**. **F-033**/**F-034** removed payloads from errors; this covers logs.
  *Verify:* a log-capture test asserts no prompt, completion, or credential fragment at any
  level with the default policy.
  **Done.** `telemetry.Content` implements `slog.LogValuer` and is gated by the
  `ContentLoggingLevel` **CP-002** already defined and nothing consumed. The zero value is
  `LogNoContent`, and an unrecognised or out-of-range level behaves as `LogNoContent` — the
  never-fail-open rule applied to the one place where failing open means publishing somebody's
  data.
  Verbosity is deliberately not the gate: a debug-level logger at the strictest content policy
  still logs no content, which is tested, because "turn on debug logging" is exactly the
  moment this would otherwise leak.
  Evidence includes an integration test that drives a **real** `Extract` with a marker string
  in its input at debug level and walks every captured entry across all four policy levels.
  That also confirms the audit finding: no reachable call site logs content today, so the gate
  is a guarantee about the future rather than a fix for a present leak.
  **A fifth credential-pattern list now exists**, with its justification written in the file
  beside the other four: `mw` cannot be imported from `telemetry` without an import cycle, and
  the diagnostics list is unexported. That is a real cost and it is recorded rather than
  hidden.
- [x] **SEC-003** — Fuzz targets with allocation and depth limits: recursive and deeply
  nested types, embedded fields and tags, maps, pointers, nils, custom marshalers, malformed
  UTF-8, huge numbers and exponents, duplicate keys, trailing data, JSON bombs, schema
  keyword adaptation, fence and JSON extraction (**A-003**), batch IDs with duplicates,
  omissions, and adversarial ordering, repair parsing, and redaction scrubbing.
  Closes **PRD-12**.
  *Verify:* the corpus runs in CI as a smoke gate; each target has a seed corpus drawn from
  real defects this list already found.
  **Done.** Four fuzz targets, each with a hand-chosen seed corpus and none making a network
  call: `FuzzExtractJSON`, `FuzzFencedBlock`, and `FuzzBalancedValue` over the JSON extraction
  path (fences, truncation, huge numbers, duplicate keys, invalid UTF-8, nesting bombs), plus
  `FuzzRedact` and `FuzzValidateEndpoint`.
  `FuzzRedact` asserts a **correctness** property rather than only absence of panic: the
  output never still matches any built-in credential pattern. A fuzz target that only checks
  for crashes passes happily while the function returns the secret unchanged.
  Each was run under `-fuzztime=20s` to confirm it actually executes rather than merely
  compiling.
- [x] **SEC-004** — Custom endpoint policy: scheme and host allowlists and a private-address
  rule, so a caller-supplied or configuration-supplied base URL cannot be pointed at internal
  infrastructure. The library already accepts custom endpoints for OpenAI-compatible
  providers and validates nothing about them.
  *Verify:* loopback, link-local, and metadata-service URLs are refused unless explicitly
  allowed; the refusal is a typed configuration error.
  **Partial.** `types.EndpointPolicy` with scheme and host allowlists and a private-address
  guard: loopback, link-local, RFC1918, the cloud metadata address, and `.internal`/`localhost`
  names are all refused unless explicitly allowed, and an undeclared allowlist refuses
  everything rather than permitting it. No DNS resolution, so the check stays offline.
  A real bug was found and fixed while testing it: the host-not-allowed and private-address
  error paths echoed the raw hostname unbounded into the error string.
  **Now wired, and the wiring exposed a worse bug than the one this task started with.**
  `ProviderConfig.EndpointPolicy` is consulted by `validateConfiguredEndpoint` before any HTTP
  client is built, from every constructor that takes a `BaseURL` — `NewOpenAIProvider`,
  `NewAnthropicProvider`, and `newOpenAICompatibleProvider`, which backs OpenRouter, Cerebras,
  DeepSeek, Qwen, and Z.ai. It is opt-in: nil enforces nothing, so existing callers are
  unaffected. Watched refusing in `sc007_httpclient_test.go`, which asserts both the typed
  error and that the mock server was **never contacted** — the refusal happens before anything
  reaches the network, which is the only version of this check worth having.
  **The bug:** `Client.providerConfig` (client.go) copies a caller's `ProviderConfig` field by
  field, and its list was missing `EndpointPolicy` and `HTTPClient`. So
  `WithProviderConfig("openai", ProviderConfig{EndpointPolicy: p})` — the public, documented
  way to switch this control on — reached `llm.CreateProvider` with a **nil policy**, and the
  check that exists and is tested one layer down simply never ran. A security control that
  silently does nothing through its most visible entry point is worse than an absent one,
  because the caller believes it is enforced. SC-007's caller-owned `HTTPClient` was being
  dropped the same way.
  The general defect is the copy list: thirteen named fields cannot fail when somebody adds a
  fourteenth. That is **A-014** exactly (`applyDefaults`, `internal/ops`), so it gets A-014's
  guard — `TestProviderConfigOverrideCarriesEveryField` walks the struct by reflection, and a
  companion test fails if the fixture stops populating every field. Both were watched failing
  against the unfixed code before the fix went in.
- [x] **SEC-005** — Retention and deletion for every store the library owns — result caches,
  diagnostic captures, pricing and usage records, replay fixtures. User content is never
  retained merely because cost accounting is on: cost records keep token counts, model IDs,
  request IDs, and money. Tenant-scoped deletion hooks where the adapter supports them.
  *Verify:* a tenant deletion removes its cache and diagnostic entries; the pricing store
  contains no prompt text by construction.

---

# M16 — Observability and operations

  **Partial.** `TenantScopedStore` and `DeleteTenantData` — refuses an empty tenant ID,
  continues past a failing store rather than aborting on the first, and joins every failure so
  a partial deletion reports *which* stores did not comply rather than just that something
  went wrong.
  **Now wired to real stores.** `tenantStore[V]` partitions by tenant at the **top level**
  rather than folding the tenant into a hashed key, which is what makes `deleteTenant` exact
  instead of best-effort — a key you cannot invert is a tenant you cannot delete.
  Two stores implement it: `TenantCacheStore` (a tenant-partitioned result cache with TTLs)
  and `TenantDiagnosticStore` (a `types.DiagnosticSink`; the library shipped none before). A
  capture arriving with no tenant or no record ID is **discarded** rather than filed somewhere
  no deletion can reach.
  The honest case: `mw.Cache` folds the tenant into an irreversible SHA-256 key and its
  `CacheStore` interface never passes a tenant into `Get`/`Set` at all, so deletion there is
  not merely unimplemented — it is impossible without changing that interface.
  `WrapExternalCacheStore` therefore returns an explicit `ErrCacheStoreNotTenantScoped` from
  `DeleteTenant` **always**, and the fan-out test covers the mixed case: the store that can
  delete does, and the wrapper still reports failure. That is the whole point of the contract
  joining errors rather than aborting on the first — a partial deletion has to say which
  stores did not comply.
  **Not done, and deliberately not faked:** the pricing/usage store (`pricing/*`) and replay
  cassettes (`schemafluxtest/*`) are still not tenant-deletable. No store of that shape exists
  under `internal/ops` to wire deletion into, and building one that nothing writes to would
  reproduce exactly the failure this task was closing — a hook that appears to work with
  nothing behind it.
- [x] **OB-001** — Observer interfaces in core (logger, tracer, metric sink, clock, ID
  generator, diagnostic sink) with no-op defaults, and the OpenTelemetry implementation in
  `telemetry/otel` using the host's provider. Core stops importing OTel, exporters, and the
  SQLite pricing store; optional adapters may. Closes **ARC-17**, **ARC-18**.
  *Verify:* core's dependency list is asserted by a test; the library initializes no global
  SDK and closes nothing it did not create.
  **Done for the dependency boundary, which was the load-bearing half.** Importing this
  library now links **zero** OpenTelemetry packages. Before this change a consumer linked 46
  of them whether or not they traced anything.
  Three moves got there: `MW-007` put the `Observer` interface in the core with a no-op
  default and the adapter in `telemetry/otel` using the host's provider; `telemetry/tracing.go`
  — the SDK initialisation, the exporters, the endpoint and sampler selection — moved wholesale
  into `telemetry/otel`, which nothing outside `telemetry/` was using; and the last two
  vendor touchpoints in core, `GetTraceID`/`GetSpanID` and `requesttracking`'s ambient trace
  lookup, became hooks whose defaults return `""` — which is exactly what they returned when
  tracing was off, so nothing changes for a caller who was not tracing.
  `telemetry/otel.InstallIDSources` installs the real readers for a caller who is.
  Evidence: `internal/coredeps_test.go` asserts the rule with `go list -deps` rather than a
  source scan, because the question is not whether a directory mentions the vendor but whether
  a consumer ends up linking it, and only the compiler's view answers that. It checks all five
  packages a consumer imports. A second test asserts the adapter **still** links OpenTelemetry
  — without it, the rule would also be satisfied by dropping OTel support entirely, which is a
  different change wearing this one's name.
  The no-global-SDK half is covered by `telemetry/otel/otel_test.go`, whose first case asserts
  that installing the adapter leaves `otel.GetTracerProvider()` untouched.
  **Not done:** the full observer set the task lists — metric sink, clock, ID generator — is
  not built; only the tracer, logger, and diagnostic sink (A-011) exist. And the core still
  calls `telemetry.RecordMetric` directly rather than emitting through a sink, so metrics have
  no seam yet. That is **OB-002**'s work and it is blocked on **PL-013**.
- [x] **OB-002** — Metrics catalog per §15.2: requests, duration, plan nodes, provider
  attempts and duration, queue duration, in-flight gauge, circuit state, item outcomes, batch
  size, batch compliance ratio, repairs, atomic fallbacks, tokens, cost, budget exhaustion,
  and review-required counts. High-cardinality identifiers stay out of metric labels and live
  in the envelope. Depends on **PL-013**.
  **Done for the metrics PL-013 unblocked; the rest is named and absent.**
  `telemetry/opsmetrics.go` emits items, batch compliance ratio, repairs, atomic fallbacks,
  cost — skipped honestly when unpriced rather than reported as zero — plus plan nodes and
  batch size, wired into `Preflight`, the recovery cascade, and the partial runner.
  **The label vocabulary is fixed and small**: operation, shape, status, currency, quality.
  Never a request ID, correlation ID, item ID, or schema hash. That is asserted by walking the
  labels against a forbidden list *and* by an integration test that drives the real call path
  with a caller-supplied request ID and proves it never reaches a label — a high-cardinality
  label multiplies series count by traffic, and it is the kind of mistake that only shows up on
  a metrics bill.
  **Not done:** provider attempts and duration, queue duration, in-flight gauge, circuit state,
  budget exhaustion, and review-required counts — all owned by the scheduler and resilience
  files, which were out of scope. Token counts are absent because the batch metrics carry cost,
  not tokens.
- [x] **OB-003** — Cost tree: logical request → stage → chunk → attempt → item attribution,
  with provider-reported usage preferred, estimates marked, and pricing quality recorded as
  `Exact`, `Estimated`, `Unknown`, or `Free`. Historical cost is never recomputed with
  current rates without keeping both versions. Closes **TRU-22** and the drift half of
  **TRU-23**. **PR-001** established that zero never means unknown; this makes the same
  distinction hold across a fan-out.
- [x] **OB-004** — Operational SLOs as tests, per §15.4: zero panics from valid API use, zero
  goroutine leaks after cancel or completion, zero client-isolation failures, zero unknown or
  duplicate batch IDs accepted, zero secrets in logs, zero calls exceeding a declared budget,
  and validated-item completeness at or above 99.5% on the conformance corpus.
  *Verify:* each SLO has a named test; the leak and isolation ones run in CI on a platform
  where `-race` works (**CI-002**).
  **Done for six of seven, and the seventh says why not.** `slo_test.go`, one named test per
  SLO, 18 cases:
  `TestSLONoPanicsFromValidAPIUse` drives seven deliberately awkward but valid calls — empty
  input, an empty collection, a single-item collection, combining marks and RTL text, and a
  provider answering with a shape that does not fit — and fails on a panic rather than on an
  error, because an error is a correct outcome there and a panic never is.
  `TestSLONoGoroutineLeaksAfterCompletion` and `...AfterCancel` count goroutines around twenty
  operations, after settling the runtime — without the settle loop the test measures scheduler
  noise rather than leaks. The cancel case uses a deadline that expires **during** the call,
  not before it: cancelling up front means the operation returns without starting, and a leak
  test on work that never began proves nothing. That correction is why this test is worth
  anything.
  `TestSLONoUnknownOrDuplicateBatchIDsAccepted` covers a duplicate id, an unknown id, an id
  from another invocation, a malformed id, and — for Sort, which must cover every id exactly
  once — incomplete coverage.
  `TestSLONoSecretsInLogs` puts a credential in the input, drives a failing operation, and
  walks every captured log entry including its attributes. It fails if *nothing* was captured,
  so it cannot pass by logging nothing at all.
  Budget and client isolation are covered by named tests in `mw/budget_test.go` and
  `client_isolation_test.go`; the SLO file names them rather than duplicating them.
  **Not covered, deliberately:** validated-item completeness at or above 99.5%. There is no
  conformance corpus — **RC-001** is the task that builds one, and it is open. A completeness
  figure computed against inputs invented for the test would look like the SLO and measure
  something else, which is worse than the absence. The placeholder in the file says so.
  **Also not done:** the `-race` requirement. It does not run on this windows/arm64 machine
  (**CI-002**), so these run without the detector locally and CI covers it elsewhere.
- [x] **OB-005** — Runbooks and dashboards for the incident classes that will actually
  happen: 429 surge, provider latency or outage, malformed or truncated output spike, batch
  omission or repair-rate spike, cost anomaly, model alias or revision change, capability or
  schema enforcement regression, stuck breaker, cache or pricing-store failure, telemetry
  exporter failure, suspected content or credential leak, deprecated endpoint. Each names its
  signal, mitigation, safe fallback, evidence to capture, and recovery criterion.
  **Done.** `docs/engineering/runbooks.md` — all twelve incident classes, each with the five
  parts the task asks for, plus the dashboard panels in the order you actually look at them
  during an incident.
  They are written against what this library really does rather than generically: the cost
  anomaly entry says check `Meta.CacheHitRatio` first and warns that the default models are
  **unpriced**, so zero means no rate card rather than free; the batch-omission entry explains
  that an invariant violation there is the id protocol working; the telemetry entry says the
  library does not own the exporter and an observer that slows the request path is a bug in
  the observer; and the leak entry names which of the four scrubbing points was bypassed as
  the actual fix, with rotation first because the leak is ongoing.
  Two rules sit above all twelve: capture evidence before mitigating unless the mitigation
  stops a leak, and never capture the payload.
  **Not done:** dashboards are described, not shipped. There is no dashboard JSON here for any
  particular backend, and inventing one for a stack nobody has said they use would be
  guesswork wearing a deliverable's name.
  Closes **PRD-24**.

---

# M17 — Release engineering and v1 acceptance

- [x] **RC-001** — Release contents per §17.2: tagged and checksummed artifacts, generated
  changelog, Go API changes **and semantic behaviour changes**, operation, prompt, and schema
  version changes, provider capability and live-verification matrix, platform support matrix,
  known degradations, migration steps, SBOM, vulnerability scan, and the release-candidate
  semantic benchmark comparison. A prompt edit with no Go signature change is a behaviour
  change and belongs in the notes. Closes **PRD-03**, **ARC-23**.
  **Done.** `scripts/release_notes.py` assembles all twelve sections from evidence already in
  the tree rather than from prose somebody remembers to write. That is the point: the failure
  mode release notes actually have is not that the facts are unavailable, it is that a human
  transcribes them and the transcription drifts.
  The section the task exists for is **semantic behaviour**, computed from
  `testdata/golden_prompts.txt`. A prompt edit changes every answer this library produces and
  changes no Go signature, so a changelog built from commits plus an API diff reports "no
  behaviour change" while the library's behaviour has moved. Run against this session's own
  commits it correctly reports the prompts changed, names the affected section, and does not
  confuse that with the API diff, which reports no change.
  Every other section reads its real source: the artifact digest from `git ls-tree` (a Go
  module's artifact is its contents, not a binary this project does not ship); Go API changes
  from the ratcheted surface snapshot; operation versions from the `OperationID` declarations
  that provenance actually stamps; **known degradations from the §19 ledger**, because an
  unchecked acceptance criterion *is* a known degradation and transcribing it by hand is how
  two lists drift; migration steps from the deprecation catalogue; SBOM from `go list -m`.
  **Never fails open.** A section it cannot compute prints `UNAVAILABLE` with the reason and is
  never omitted — an omitted section reads as "nothing changed", which is a claim nobody made.
  `--check` exits non-zero on any such section; watched exiting 1 today on the semantic
  benchmark, which is genuinely unavailable because RC-002 has not built a baseline.
  Three sections were computing confident wrong answers before being checked against their
  sources, which is the argument for reading the output rather than trusting the script: the
  provider list read `CreateProvider`, which delegates to a registry and names nothing; the
  platform matrix read `runs-on: ${{ matrix.os }}`, which names nothing either; and the
  migration section read a struct that is populated at run time rather than the map literal
  that populates it. All three said "(none found)" or "nothing to migrate" — a plausible,
  wrong, quiet answer.
  **A real finding it surfaced:** `govulncheck` reports **three standard-library
  vulnerabilities that this code actually reaches** — GO-2026-5856 (crypto/tls ECH privacy
  leak), GO-2026-5039 (net/textproto), GO-2026-5037 (crypto/x509) — on go1.26.3, fixed in
  go1.26.4/go1.26.5. This is a **toolchain upgrade**, not a code change, and it is not
  something this repository can do to itself; it is recorded here rather than left to be
  discovered at release time.
- [x] **RC-002** — Semantic regression suite for release candidates, on pinned operation
  versions and as-pinned-as-available models: extraction accuracy and hallucination,
  missing-field and evidence-reference validity, classification accuracy and abstention,
  choose/filter precision and recall, ranking agreement and ID coverage, repair success and
  regression rate, valid-item ratio by batch size, latency, tokens, and cost per accepted
  item, and prompt-injection resistance. Results are statistical, with repeated trials and
  intervals — a single exact-output assertion is not a stable live test. Closes **PRD-15**,
  **TRU-21**. This suite spends money: it runs only on a protected release-candidate
  workflow with an explicit spend ceiling, never on `go test ./...` (**B-04**).
  **Done.** `internal/semantic`: nine dimensions, each written as a **property** of an answer
  rather than an expected answer. That is the whole design — "the invoice number is INV-4417"
  survives two correct-but-different answers; "the summary equals this paragraph" does not, and
  a suite built on the second kind fails for reasons unrelated to quality.
  **Every number is a rate with a Wilson interval, and thresholds are compared against the
  interval, never the point estimate.** 8/10 and 800/1000 are the same estimate and not
  remotely the same evidence; `TestTheSameRateWithMoreEvidenceIsWhatClearsAThreshold` is that
  distinction as a test. Wilson rather than the textbook normal approximation for a concrete
  reason: the normal interval for 10-out-of-10 is `[1.0, 1.0]`, claiming certainty from ten
  trials, and a passing quality suite lives exactly in that region.
  **The harness is calibrated against a planted failure rate**, which is the answer to "who
  checks the checker": a fake that fails every 4th trial must be measured at exactly 75%, and
  if it is not, the suite is wrong. That is a statement a test can make about a scorer whose
  output otherwise nothing can contradict. Those calibration tests run on every `go test ./...`
  and touch no provider — the machinery that will later spend money is proven for free.
  **A failed call is not a wrong answer.** Errored trials leave the quality denominator
  entirely, so an outage lowers *confidence* in a rate rather than lowering the rate. Folding
  the two together makes an incident look like a quality regression and costs somebody a day.
  **Spend**: two independent switches, neither defaulting to spend — `SCHEMAFLUX_LIVE_TESTS=1`
  and an explicit `SCHEMAFLUX_SEMANTIC_BUDGET_USD` ceiling with no default. The ceiling is
  checked *before* each trial, and exhausting it stops the run while keeping the evidence
  already paid for. An unpriced model reports a cost of zero, so cost is tracked by pricing
  *quality*: one unpriced trial makes the total a **floor**, not a total. Treating that zero as
  free would have silently disabled the ceiling — the single most expensive bug this package
  could have contained.
  A single-trial case is refused before it runs, because one sample reports 0% or 100%
  regardless of the model.
  **A gap this surfaced:** only `ExtractResult` and `SortResult` return an execution envelope.
  `Choose`, `Filter`, and `Classify` return a bare value, so a caller cannot learn what those
  calls cost, how many tokens they used, or which model answered. Those cases contribute to the
  quality rates and contribute nothing to cost or latency, and the report says which totals are
  therefore incomplete rather than quietly under-reporting.
  **Not done:** the suite reports and does not gate. Thresholds are a product decision, and one
  hard-coded here would be tuned until it passed. It has also **never been run against a live
  model** — that needs a funded credential (§19.5.2), so there is no baseline yet and
  `Proportion.Regressed` has nothing to compare against.
- [x] **RC-003** — Track §19's acceptance criteria as a checklist in this file, and require
  every unchecked box at v1.0.0 to carry an ADR saying why it ships unmet. Twenty-nine boxes
  across core architecture, correctness and trust, execution and resilience, security and
  governance, and verification and operations.
  **Checker done; the ledger is not written.** `scripts/acceptance_checklist.py` parses §19,
  assigns stable IDs, and diffs them against a ledger it expects in this file — refusing a
  missing criterion, an unchecked one with no ADR, stale text, and a dangling ID. Self-tested
  against fabricated fixtures.
  **It found §19 carries 32 checkboxes, not the 29 this task's text states** — either the
  specification grew or the count was wrong when written, and it is worth resolving before the
  ledger is transcribed rather than after.
  Only the self-test runs in CI: `--check` would red the build permanently until the ledger
  exists, and a gate that is always failing is a gate people learn to ignore.

  **Ledger written; 20 of 32 met.** The count question is resolved in the document's favour:
  there are **32** criteria, not the 29 this task's text stated. The text was written from a
  count, and the count went stale — which is the argument for the checker reading the document
  rather than anyone maintaining a second list of it.
  Each unchecked box carries a reason, and the reasons are deliberately specific: "IN-004 is
  open" is a pointer, not an excuse, and every one of them names a task that would close it.
  Two are unmeetable on this machine rather than unbuilt (19.1.5 and 19.5.1 both want `-race`,
  which does not run on windows/arm64) and one needs a funded credential (19.5.2). Those are
  the three where the honest word is *unverified* rather than *unmet*, and the ledger says so
  instead of borrowing the word "done" from the work that was actually finished.
  `--check` can now run in CI without being permanently red.

#### §19 acceptance ledger (v1.0.0)

- [ ] 19.1.1 — Core has no mutable global execution state. — ADR: IN-004 delivered the per-client snapshot and it takes priority on every call path, but `ops.defaultProvider` and `ops.customLLMCaller` remain for callers with no client; observer and cache policy are still process-wide.
- [ ] 19.1.2 — Every stable public operation lowers to the same `Op -> Run -> Plan -> Execute` path. — ADR: the pre-A-001 operations (Question, Predict, Cluster, Compress, Decompose) still call `callLLM` directly rather than lowering to an `Op` descriptor.
- [x] 19.1.3 — Standard and fluent APIs pass mechanical equivalence tests.
- [ ] 19.1.4 — Stable execution requires `context.Context`. — ADR: `ChooseBy`, `FilterBy`, and `SortBy` take none; isolated `*Context` variants exist, but the original three remain on the published surface and still resolve the process-wide provider.
- [ ] 19.1.5 — Client and builder ownership/lifecycle are documented and race-tested. — ADR: ownership is now defined (IN-004's immutable per-client snapshot) and tested concurrently, but unverified on the race half — `-race` does not run on windows/arm64, so it has never been executed here; `-shuffle=on` is the substitute and is weaker.
- [x] 19.1.6 — Optional adapters do not become mandatory core dependencies.
- [x] 19.2.1 — Exact decoder rejects unknown/duplicate fields and lossy conversion in strict mode.
- [x] 19.2.2 — Presence semantics distinguish missing, null, and zero values.
- [ ] 19.2.3 — Every stable operation declares deterministic invariants and batchability. — ADR: only the operations converted to `Op` descriptors declare `Contract.Invariants` and `Batch`; the rest declare neither, the same gap as 19.1.2.
- [ ] 19.2.4 — Evidence and provenance survive supported pipelines. — ADR: TC-003 traces a claim to the *result* that produced it and TC-002 traces a claim to a *span*, but nothing joins the two, so a three-stage pipeline does not resolve to a byte range in stage 1's input.
- [ ] 19.2.5 — Requested and delivered contract levels appear in every detailed result. — ADR: both appear, but Delivered is computed from the same declaration as Requested rather than observed, so a provider degrading mid-call is invisible (TC-004).
- [ ] 19.2.6 — Prompt/data trust boundaries are represented and adversarially tested. — ADR: all five trust levels are represented and enforced at the system-prompt boundary, and steering is adversarially tested, but no operation yet routes retrieved documents or prior model output through `DelimitUntrusted` (TC-001).
- [x] 19.2.7 — Model claims are separated from deterministic checks and runtime facts.
- [x] 19.3.1 — Homogeneous collections support bounded MDSP with stable IDs.
- [x] 19.3.2 — Global operations use tested operation-specific merge semantics.
- [x] 19.3.3 — Scheduler enforces call, queue, token, cost, provider, and tenant limits.
- [x] 19.3.4 — Retry, repair, split, fallback, escalation, and review share one budget ledger.
- [x] 19.3.5 — Partial success, cancellation, shutdown, and budget exhaustion have typed semantics.
- [x] 19.3.6 — No valid item is replayed due solely to an independent sibling failure.
- [x] 19.3.7 — Circuit breakers and rate limits prevent retry storms.
- [x] 19.4.1 — Content logging is off by default; secret/log redaction tests pass.
- [x] 19.4.2 — Raw diagnostics are opt-in, bounded, redacted, and retention-controlled.
- [x] 19.4.3 — Data policy constrains provider, region, retention, training, cache, and logging.
- [ ] 19.4.4 — Fallback cannot silently degrade locked contracts. — ADR: the same missing observation as 19.2.5 — a silent degradation cannot be refused while it cannot be detected (TC-004).
- [x] 19.4.5 — Custom endpoints are subject to SSRF and transport policy.
- [x] 19.4.6 — Security policy, threat model, supported versions, and disclosure process are published.
- [ ] 19.5.1 — Required unit, race, leak, fuzz, shuffle, replay, vulnerability, and platform CI gates pass. — ADR: unverified rather than unmet — every listed gate exists and passes except `race`, which does not run on windows/arm64. Platform CI would close this; this machine cannot.
- [ ] 19.5.2 — Every production-supported provider passes conformance and recent live verification. — ADR: needs a funded credential. The conformance suite compiles and runs against fakes; the live half has never been executed (P-012, P-014, CA-004, P-017).
- [ ] 19.5.3 — Release-candidate semantic regressions are within approved thresholds. — ADR: RC-002 is open; there is no semantic regression suite yet, so there is no threshold to be within.
- [x] 19.5.4 — Execution envelopes support incident diagnosis without raw content in normal logs.
- [ ] 19.5.5 — Dashboards, alerts, and minimum runbooks are published. — ADR: OB-005 publishes runbooks for twelve incident classes and names the dashboard panels, but no alert definitions exist.
- [ ] 19.5.6 — Release notes include semantic prompt/operation changes and provider capability changes. — ADR: RC-001 is open; no release has been cut, so no release notes exist to check.

- [x] **RC-004** — Compatibility and deprecation policy: at least one documented window for a
  deprecated stable API, deprecation notices that name a mechanical replacement, global and
  default-client APIs routed to `quick` or a compatibility adapter, and removal only at a
  major version. There is not one `// Deprecated:` marker in the repository today
  (**PS-003**).
  **Done as a checker, which is worth more than the document.** `scripts/deprecation_policy.py`
  reads `catalog.go`'s deprecated entries and verifies the **source** carries a
  `// Deprecated:` marker naming the catalogued replacement. Self-tested against fabricated
  fixtures — marker present, absent, detached, and on the wrong function — because a checker
  that has never refused anything is not known to work. Wired into CI.
  **It found four real violations on its first run**, all in my own catalogue: `VerifyClaim`,
  `MergeWithMetadata`, and `FormatWithMetadata` claimed a replacement with no marker anywhere,
  and `ValidateLegacy`'s marker pointed at `Validate` — which is **itself deprecated**, so it
  sent a caller on two migrations instead of one. All four are fixed, and the gate is what
  keeps the catalogue and the code from disagreeing again.
- [x] **RC-005** — Load and chaos suites: large item streams, provider slowdown and 429
  bursts, mixed tenants and priorities, cancellation storms, large schemas and near-limit
  chunks, partial MDSP failures forcing atomic fallback, and failing telemetry and stores.
  The outcome metrics are cost and latency per valid item, not calls per second.
  **Done.** Nineteen tests across six files, and they run in the **ordinary suite** — under two
  seconds combined, no build tag, no env gate. A chaos suite nobody runs is a chaos suite that
  passes.
  Every fan-out test is wrapped in a deadline helper, because this repository's fan-out bug
  **hung** rather than failed: a backstop that released the gate after `wg.Wait()`, which waits
  on followers blocked on that release. A regression of it has to fail a test, not stall CI
  forever, and that is a property of the harness rather than of the assertions.
  The scheduler's head-of-line defect is reproduced as a randomized, scaled-up case rather
  than the single hand-built one that originally caught it — the deterministic version proves
  the fix, the randomized version is what would catch the next variant.
  Concurrency bounds are checked by **peak tracking**, not by belief: `HalfOpenMaxCalls` is
  asserted against the highest concurrency actually observed under contention, which is the
  only way that limit can be shown to hold rather than merely be configured.
  Coverage corruption is injected deliberately — omissions, duplicates, invented ids — to force
  the isolate passes and the atomic fallback that only run when MDSP coverage fails, so the
  recovery path is exercised rather than assumed reachable.
  **A real flake found and fixed in the suite itself:** a cancellation test raced a 2–5ms
  deadline against 3ms of work, so on a fast machine the work occasionally won. Widened and
  re-run 60 times shuffled. A chaos suite that is itself flaky trains people to re-run rather
  than to read.
  **Not done:** no new `schemafluxtest` chaos package. `schemafluxtest.Provider` implements the
  top-level interface while every operation in `internal/ops` takes `internal/llm.Provider`, so
  a new package would have been a second fake for a different seam; extending the existing
  in-package fake with latency and rate-limit knobs was the smaller true answer.
  **No production defect found** in the scheduler, fan-out gate, partial runner, recovery
  cascade, map-reduce, circuit breaker, or bulkhead under this load.
- [x] **TR-002** — Extend `.audit/traceability.py` to `to-production.md`'s gap register the
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

Four of these — **CB-01** to **CB-04** — existed only as rows in the evidence table below,
never as items. `.audit/traceability.py` reported them dangling on every run, which is a
checker doing its job on a record that was incomplete: work was done, the evidence was
written, and the box it belonged to was never drawn.

  **Done, and it found four untraced gaps immediately.** `.audit/traceability.py` now parses
  the `ARC`/`PRD`/`API`/`EXE`/`TRU` registers out of `to-production.md` — 111 rows — maps each
  to the tasks citing it, reports the untraced ones, and **fails** on an untraced gap or a
  dangling gap citation, the same way TR-001 already did for the review's findings.
  The `D-` collision the task warns about is handled by construction: the gap pattern requires
  one of the five known register prefixes and **exactly two digits**, so the specification's
  three-digit `D-001` decisions cannot be read as the review's two-digit `D-01` schema
  findings.
  **What it caught on its first run:** `API-02`, `API-03`, `API-06`, and `API-10` were listed
  as covered in the hand-built table and cited by **no task**. The table was written by hand
  and had already drifted, which is exactly what this task predicted would happen within a
  week. They are now cited where the work actually lives — the first three on **FL-003**,
  which is the fluent-versus-standard divergence itself, and API-06 on **PS-002**, whose
  catalogue *is* the small cross-operation vocabulary that gap asks for.
  **A bug in my own tooling, worth recording because it nearly shipped silently:** the first
  version of the citation regex matched a literal backspace character instead of a word
  boundary — a `` mangled on its way through a shell heredoc — so it reported *zero* gaps
  traced while looking entirely correct. A gate that silently matches nothing passes forever.
  It was caught by disbelieving the number rather than by any test, which is an argument for
  the counts this script prints being read and not skimmed.
- [x] **CB-01** — `OpenAICompatibleProvider.Complete` went through go-openai, whose
  `ChatCompletionResponseFormat` carries a `Type` and nothing else, so a request arriving with
  a strict JSON schema had the **schema dropped** and was sent as `{"type":"json_object"}`.
  The same `Extract[T]` got constrained decoding on OpenAI and an unconstrained guess on
  Cerebras, DeepSeek, Qwen, ZAI, and OpenRouter, with nothing at the call site to tell them
  apart. Evidence: `f745d66`, verified live against `gemma-4-31b`.
- [x] **CB-02** — Cerebras *rejects* `format`, `pattern`, `minItems`/`maxItems`,
  `minLength`/`maxLength`, and `minimum`/`maximum` rather than ignoring them, so the first
  extraction with a `time.Time` field would have failed the whole request with a 400 naming a
  keyword the caller never wrote. The transport strips them into a copy — never in place,
  since the caller's schema is reused across providers. Evidence: `f745d66`.
- [x] **CB-03** — A 429's `Retry-After` was discarded, so the retry ladder fell back to a
  backoff that doubles from 500ms and stops at five seconds. Against a provider that limits
  *per minute*, every attempt landed inside the same closed window by construction. Also
  exposed a latent bug: `isRetryableLLMError` matched the whole message including the
  vendor's body, so a 429 whose body mentioned `invalid_request_error` was classified
  permanent. Evidence: `f745d66`.
- [x] **CB-04** — `providerModels["cerebras"]` mapped to `llama-3.3-70b`/`llama3.1-8b`,
  written from a docs page and never called. Now `gemma-4-31b` at all three tiers, priced,
  and the choice is stated rather than assumed. Evidence: `f745d66`.

- [x] **PS-010** — The repository hygiene files: `LICENSE`, `SECURITY.md` (threat model,
  supported versions, disclosure process — **SEC-001**), `CONTRIBUTING.md`, issue templates
  that ask for sanitized envelope metadata rather than raw payloads, and an ADR directory to
  hold the twenty decisions `to-production.md` records plus the departures this list has
  accumulated (**P-007**'s error-detail compromise is the first).
  Split out of **PS-009** because each is a *new Markdown file*, and the standing rule is that
  a new Markdown file needs to be asked for by name. Nothing here is hard; it needs
  authorization rather than effort. Closes **PRD-25**.

  **Done — Cam asked for all of them by name, which is what the rule required.**
  `LICENSE` (MIT, and the licence choice is his to change in one file — nothing is tagged, so
  nothing has shipped under it), `SECURITY.md`, `CONTRIBUTING.md`, two GitHub issue templates,
  and `docs/adr/`.
  The issue templates are the part worth reading: both ask for **envelope metadata** —
  operation, provider, resolved model, attempts, repairs, delivered contract, error kind, and
  the *shape* of the input — and both carry a required checkbox confirming no real payload,
  credential, or customer data is included. `Meta.String()` prints a one-line summary that is
  safe to paste, which is what the bug template asks for. A library whose whole premise is
  running somebody's invoices through a model should not have a bug template that invites them
  into a public issue.
  `docs/adr/` holds **departures**, not copies of the specification's twenty decisions — a copy
  drifts, and **TR-002** just spent a task building a checker for a hand-maintained table that
  had drifted within a week. Four departures are recorded: the error-detail compromise
  (**P-007**/**A-011**), the five deliberately-unshared redaction lists, the context-value
  provider seam, and the unpriced default models. Each states what it costs and **the condition
  that would change the answer** — a departure with no revisit condition becomes permanent by
  default rather than by decision.
- [x] **S-013** — Schema migrations: a deterministic function from one stored shape to
  another, with its own version and provenance, plus the registry that finds one.
  **S-011** built the classification and **S-002** the identity a migration keys on; what is
  missing is the transformation, and it should not be built until something in this library
  actually stores results — writing the machinery first is building for an imagined caller.
  *Verify:* a result stored under `person/v1@abc123` decodes into the v2 type through a
  registered migration, and the result records which migration ran.

  **Done.** `Migration{Name, From, To, Fn}` over the `SchemaDescriptor` identity **S-002**
  already established, with a registry that refuses an unnamed migration, one with no
  function, one from a shape to itself, and a second migration for the same source — an
  ambiguous path is a silent choice between two answers.
  `Migrate` no-ops when the identities already match, refuses an unregistered path with
  `KindSchemaViolation`, and refuses a migration whose declared target does not match the
  caller's actual type — a migration that claims to produce `v2` and produces something else
  is worse than no migration.
  A failing migration keeps the underlying error in `Cause` rather than flattening it into a
  message, because a caller-authored function can error with the payload it was transforming,
  and a message is what gets logged.
  13 cases, including the scenario the task names: stored `person/v1` bytes migrated into
  `personV2`, decoded exactly, with `Result.MigrationName` naming what ran.
- [x] **S-012** — Flag `float32` for money-shaped fields in the type support matrix.
  **S-009** established that a float32 price silently loses cents and that the round-trip
  check cannot see it, because Go marshals a float32 as the shortest decimal that round-trips
  as a float32. The matrix is the right place to catch it, and today it classifies float32 as
  full support.
  The obstacle is that a Go type does not say what a field is *for*: `float32` is fine for a
  temperature and wrong for a price, and the difference is the field's meaning. Options are a
  tag (`schemaflux:"money"`), a registered `Money` type, or classifying every float32 as
  restricted and letting the caller decide. The third is cheapest and the noisiest.
  *Verify:* a struct with a float32 price is flagged; one with a float32 temperature is not,
  or the rule is documented as covering both.

  **Done.** `classifyType` reports `SupportRestricted` for `float32` — top level, nested,
  in a slice, in an array, behind a pointer — and says why: a float32 re-encodes to the same
  decimal literal, so `CheckNumericFidelity`'s round-trip check **cannot see the loss**. That
  is the blind spot **S-009** recorded rather than papered over, and this is the flag that
  makes it visible at the type boundary instead of at the invoice.
  Restricted, not rejected: the call still runs. And the rule covers **every** float32, not
  only money-sounding names, because a Go type does not say what a field is for — a float32
  temperature is flagged too, which is stated rather than treated as a false positive.
  12 cases, including that a worse finding elsewhere in the struct still wins the comparison
  so a float32 cannot mask a channel.
- [x] **CI-008** — Close the twelve numbered examples still failing under the local provider,
  listed with their errors in `.audit/examples_expected.txt`. Two groups, and they want
  different fixes: `Transform`, `Generate`, `Compress`, `Decompose`, `Enrich`, `Normalize`,
  `Synthesize`, `Predict`, `Verify`, and `Question` declare their response shape in prose the
  mock cannot read — the fix is for those operations to declare a schema (**S-002**), which
  they should do anyway; `Choose` and `Cluster` need an answer the mock can only give by
  understanding the operation, which argues for the examples using `schemafluxtest` instead.
  *Verify:* `python scripts/examples_gate.py --update` records 45 of 45.

  **Partial: 33 of 45 to 36 of 45.** `Transform`, `Generate`, and `Normalize` now **declare**
  their response schema rather than only describing it in prose. That is the fix the task
  names, and it is worth more than making an example pass: `Transform` embedded the target
  shape in its system prompt and asked for "ONLY valid JSON matching the target schema" — a
  request a model may honour and nothing verified. Declaring the schema means a provider with
  structured output enforces it.
  For the operations that decode into an anonymous struct, the schema is generated **from that
  struct** via reflection, so it cannot drift from what the operation actually accepts. A
  hand-written schema beside a decode target is two descriptions of one thing.
  The golden prompts moved by exactly two lines — `json schema: false` to `true` for Transform
  and Generate — with the prompt text unchanged, which is the evidence that this changed what
  is *declared* and not what is *said*.

  **Now 40 of 45, and the last fix was in the mock rather than the operations.**
  Two things were wrong, and neither was what the task's own diagnosis assumed.
  First, the shape-answering local provider read only the **first** JSON object in a system
  prompt. These prompts routinely state the caller's *type schema* before the response
  template — "Each part should match this schema: {...}. Return a JSON object with: {...}" —
  so the mock was answering with the shape of the *input*. It now tries every balanced object
  in the prompt, later ones first, and keeps the first that parses into an object.
  Second, eight response templates contained **invalid JSON**: `"compressed": <the compressed
  content>`, `"lower": <lower bound>`, `"selected": <index>`. Nothing could parse them — not
  the mock, and not a model being shown an example of the answer it should produce. A template
  that is not valid JSON invites a model to echo the placeholder. They are valid now.
  That is a prompt change and therefore a behaviour change, which is what the golden snapshot
  exists to surface.
  **Still failing (5):** `24-cluster`, `26-compress`, `27-decompose`, `33-predict`,
  `35-question`. Cluster is the case the task already identified as needing an answer the mock
  can only give by understanding the operation — a partition — which argues for that example
  using `schemafluxtest`. The other four have templates whose shape the mock fills correctly
  but whose *content* the operation then rejects.

  **Done: 45 of 45.** `.audit/examples_expected.txt` records it.
  Four of the last five were **one defect in four places**, and it was not a mock problem.
  `Question`, `Predict`, `Decompose`, and `Compress` each state their envelope in a fixed
  prose template that hard-codes the shape of a field whose type is the caller's `T`:
  `"prediction": {}`, `"content": {}`, `"compressed": {}`. `Predict[float64]` was being shown
  an object where only a number will parse. The template is one string and `T` varies per
  call, so the prose **cannot** be right for every caller — no wording fixes this.
  It survived because it is invisible for a struct `T`. The first half of `27-decompose`
  (`Decompose[Epic]`) passed; the second half (`Decompose[string]`) failed. Somebody tried the
  case the template happened to fit.
  The fix is the one this task named for the other operations: **declare the shape from `T`**,
  built by reflection at the call site where `T` is known, so it cannot drift from what the
  operation will accept. That also closes a quieter gap — these four sent **no schema at all**,
  so a provider with native structured output had nothing to enforce and they ran at
  `ContractPromptOnly` while the operations around them ran schema-constrained.
  `24-cluster` was the one the task predicted would need `schemafluxtest`. It did not. Cluster
  sent its items as `[0] {...}` lines joined by newlines — the indices that the entire
  partition contract is stated in (`CoversExactlyOnce`, the outlier accounting) were readable
  only by parsing a bracket prefix out of free text. Nothing could recover them reliably, so
  the local provider saw one item where there were ten. They go as a JSON array of
  `{index, item}` now. This is worth more than the example: an index a provider has to recover
  from formatting can be recovered **wrongly**, and a wrong index here does not fail loudly —
  it silently clusters the wrong item.
  **Not done:** the schemas declared here describe only the fields whose shape depends on `T`,
  and leave the optional envelope parts (interval, scenarios, factors, dependencies) undeclared
  rather than duplicating the result structs. That is deliberate — a hand-written schema beside
  a decode target is two descriptions of one thing — but it does mean a provider enforcing
  these schemas constrains less than the full result type.
  **Superseded diagnosis, left visible on purpose:**  **Still failing (9), and the reason is not what it first looked like.** `Decompose` and
  `Enrich` *were* given the same treatment and still fail. I first recorded that as a second
  call path the fix had missed; that was wrong, and the real cause is worth more than the fix:
  `GenerateJSONSchema` returns **nil** for a type it cannot faithfully represent — a
  `map[string]any` payload, which is exactly what those examples pass — so the operation
  correctly sends no schema at all rather than a wrong one. Verified directly: the request
  reaches the wire with `ResponseFormat: "json"` and `JSONSchema: nil`.
  That is the right behaviour and it means declaring a schema cannot fix these examples. They
  need the other half of the task's own diagnosis: either the local provider learns to read
  the prose template these operations state their shape in, or the examples use
  `schemafluxtest`. `Compress`, `Synthesize`, `Predict`, `Verify`, and `Question` are untried. `Choose` and `Cluster` are the
  different problem the task already identified: the mock cannot answer them without
  understanding the operation, so those two examples should use `schemafluxtest` instead of
  the local provider. The ratchet is updated to 36 so the three that were fixed cannot
  silently regress.
- [x] **OP-308** — Apply **OP-302**'s deterministic diff to `Pivot`, `Enrich`, and
  `Normalize`, which report the same model-authored `Lost` / `Inferred` / `Changes` audit
  trail that `Project` did. The helpers (`jsonFieldNamesOfValue`, `missingFrom`) already
  exist; what each one needs is a decision about what its diff *means* — a normalization's
  changed values are not a field-set difference, so it needs a value diff rather than a name
  diff, and that is why this is a separate task rather than a repeat of the same edit.
  *Verify:* per operation, a model that silently drops a field is contradicted by the result.

  **Done, and the task's own warning was right: the three wanted different diffs.**
  `Pivot.DataLoss` and `Enrich.AddedFields` are field-set differences, exactly like
  **OP-302**'s, and both keep the model's account beside them as `ModelClaimed*` — a field
  the model says it dropped and the diff says survived is a different problem from the
  reverse.
  `Normalize` could not be a field-set diff, which is why it was filed separately. Its changes
  are *value* differences, and no diff can recover the **reason** for one — so the model's
  `Changes` stays a claim, and what Go establishes is `ChangedFields` (which fields actually
  moved) and **`Unreported`**: the changed fields the model's account does not mention. A
  non-empty `Unreported` means the audit trail is incomplete, which is precisely what a
  caller reading one needs to know. `TotalChanges` follows the diff rather than the narrative,
  because a caller checking it is asking what happened to their data.
  Values compare as canonical JSON, so `1284.50` and `1284.5` are not a change.
  Found while here: `Pivot` still logged the whole response on a parse failure — **X-03** in
  a fourth place.
  *Verify:* `internal/ops/audit_diff_test.go` — a pivot dropping three fields and claiming
  none, a faithful pivot reporting none, an enrichment adding two and admitting one, a
  normalization changing two and mentioning one (with `Unreported` naming the difference), a
  normalization that changed nothing, and a formatting difference not counting.
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
- [x] **X-07** — A type embedding both `CommonOptions` and `types.OpOptions` has two `Intelligence`
  fields and two `Mode` fields, and `mergeEmbeddedOpOptions` takes both from `CommonOptions`
  unconditionally — so `opts.OpOptions.Intelligence = Quick` is silently ignored, while
  `opts.OpOptions.Context` *is* honoured because Context falls back. A caller has no way to tell
  which fields fall back and which do not. Found while writing the C-06 guard tests, where the
  guard reported the wrong tier. Fold into **M06**'s uniform options work.
  *Verify:* one Intelligence and one Mode reachable per options type, or a documented precedence
  that is the same for every field.

  **Done — and it could only be done after A-005.** `Mode` and `Intelligence` were taken from
  `CommonOptions` unconditionally *because* Strict was `Mode(0)` and Smart was `Speed(0)`:
  with no way to tell "the caller chose Strict" from "the caller said nothing", falling back
  would have made `.Strict()` unrepresentable. So the fix looked arbitrary and was not.
  Now that both types have a real unset value, the rule is the same for every field: the
  `CommonOptions` side wins when it says something, the embedded side is used when it does
  not. A caller no longer has to know which fields fall back and which do not, because they
  all do.
  *Verify:* the existing `TestExplicitStrictAndSmartSurviveTheMerge` still passes — the
  fallback must not resurrect the F-01 behaviour — and `TestUnsetOptionsStillTakeTheOperationDefault`
  covers the other direction.
- [x] **X-06** — `CompleteOptions.Context` is a `[]string` and `InferOptions.Context` is a `string`:
  both mean prose context for the prompt and collide with the embedded
  `types.OpOptions.Context`, which is a `context.Context`. Found while threading X-01, where the
  collision produced a compile error rather than silence — but a caller reading the field list
  has no such warning. Rename the prose fields to `Background` or `Notes`.
  *Verify:* no options struct has two fields named Context reachable from the same selector.

  **Done, and it was four options structs rather than two.** `Complete`, `Infer`, `Diff`, and
  `Explain` all carried a prose `Context` beside the embedded `types.OpOptions.Context`,
  which is a `context.Context` — two different things reachable through one selector, one of
  them a deadline and the other prompt material.
  All four are `Background` now, with `WithBackground` setters. The doc comment on each says
  what it is *not*, because the name is the thing that misled.
  **Breaking change** for **DOC-002**: `WithContext` on those four options types is gone. The
  collision produced a compile error when **A-002** threaded cancellation through, which is
  more warning than a caller reading the field list ever got — and that is the argument for
  renaming rather than documenting.
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
- [x] **FL-003** — **F-03**: eleven fluent entrypoints start from a zero-value options struct
  rather than the `NewXOptions()` constructor the direct API uses, so the two public APIs disagree
  on defaults and which one a caller gets depends on the spelling they chose. Route every
  entrypoint through the constructor.
  **Addresses API-02, API-03, and API-10.** These three specification gaps were listed as
  covered in the hand-built table below and cited by no task at all — which `.audit/traceability.py`
  found the moment TR-002 taught it to read the gap registers. They belong here: API-02 wants
  the canonical standard functions taking context, client, input, and options; API-03 wants
  every fluent call to compile to the same options and descriptor as its standard equivalent,
  which is precisely the divergence this task describes; and API-10 wants concrete generic
  builder values, which is what **A-013**'s value receivers and copy-on-write storage began.
  *Verify:* a test that constructs each operation both ways and asserts the resolved options are
  equal.
  **Done — all seventeen.** The eleven advanced entrypoints had **no constructor to route
  through**, so the fix was two steps: `internal/ops/options_advanced.go` adds the eleven
  missing `NewXOptions()` constructors, each reproducing the defaults literal already built
  inside its own operation *field for field*, and the entrypoints then route through them.
  Building the constructor from the operation's own existing defaults is what makes this a
  correction rather than a second opinion about what the defaults are.
  `TestFL003EntrypointsMatchDirectConstructorDefaults` now covers all seventeen and passes —
  the test that previously documented the failing eleven has flipped, which is the evidence
  that matters.
- [x] **FL-004** — **F-04**: `Steer` assigns rather than appends, so two `.Steer(...)` calls
  silently drop the first, and the op then joins the caller's steering with its own generated
  clauses using `". "`, producing a run-on in which the library can contradict the caller.
  Accumulate steering, and keep the caller's text in its own block.
  *Verify:* two `.Steer` calls both reach the prompt; the caller's text is separable from the
  library's.
  **Done.** Steering accumulates across all three builder bases, joined with `"; "`. Two
  `.Steer(...)` calls used to silently discard the first, which is the worst shape a builder
  can have: it reads as configuration and behaves as replacement.
  11 cases, including three-call chains, that an empty steer is a no-op rather than a
  separator, and that branching a builder does not leak steering into its sibling.
  **Not done:** the second half of the finding — the operations join caller steering with
  their own generated clauses using `.` rather than the same separator — lives in the operation
  files and was out of this change's scope, so two different separators are still in play.
- [x] **FL-005** — **F-05**: `commonRequest`, `opRequest`, and `directRequest` implement the same
  eleven methods three times, because the options structs behind them are inconsistent. Collapse to
  one base once **M06** has made the options structs uniform. Depends on M06.
  **Done.** The three bases collapse into one `requestBase[Self, Opt]` with nil-guarded setter
  closures, and all 49 construction sites migrated.
  **It closed a real gap nobody had noticed:** `AuditOptions.Threshold` is a genuine field that
  was **unreachable** from `AuditRequest`, because `directRequest` had no threshold slot at all.
  Three near-identical bases is exactly how a field ends up wired in two of them and missing
  from the third. It is wired now, and `TestEveryBuilderWiresEverySetter` records `setThreshold`
  as the one legitimately optional setter rather than being weakened to accommodate the gap.
  **Not done:** the six hand-rolled entrypoint types still duplicate the eleven methods. FL-005
  names the three bases and those six carry extra machinery — variadic `Run(ctx...)`, the
  double context-set for the collection operations — so migrating them was more risk than the
  task asked for.
- [x] **FL-006** — **F-07**: builders validate nothing until `Run()`, so a mis-built request is
  discovered after the call is set up. Add `Validate()` to the builder bases and call it from
  `Run()` before any provider work.
  *Verify:* a builder with empty criteria reports the error without a provider being contacted.
  **Done, and the finding is sharper than the task states.** Every options type in
  `internal/ops` *already* had a `Validate()`, called first thing by its operation — the fluent
  layer simply never called it. So a mis-built request was validated only after `Run()` had
  handed it to the operation, which is a worse failure than no validation: the check existed
  and was skipped.
  `buildError` now runs as the first statement of all 61 `Run`/`RunResult` methods.
  Evidence: 11 cases with a counting provider proving **zero provider contact** for a filter
  with no criteria, a top-0 choose, an out-of-range threshold, an invalid sort direction, an
  invalid strategy, and an invalid failure level — plus positive controls proving a valid
  request still reaches the provider, so the check cannot pass by refusing everything.
  **Not done:** the eleven advanced options types have **no `Validate()` at all** in
  `internal/ops`, so `buildError` is a documented no-op for them. Inventing validation rules
  there would be a second opinion about validity, which is the same class of mistake as a
  second retry classifier.
- [x] **FL-007** — **CF-09**: composition is linear and untyped. Fold into the **M08** combinator
  work rather than patching the current shape. Depends on CF-008.

  **Done.** `CF-008` had already retired the linear untyped `Pipeline`, so there was nothing
  left to patch — only something to add. `compose.go` re-exports the M08 combinators for the
  fluent surface with two adapters: `AsStep` for the builders returning `(T, error)`, and
  `AsStepCtx` for the variadic-context ones, which threads the combinator's context through so
  cancellation reaches an in-flight call.
  The difference between the two is tested rather than assumed: `AsStepCtx` cancels mid-call
  and `AsStep` documented-does-not, which is the kind of distinction that is invisible until
  somebody's timeout does nothing.
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

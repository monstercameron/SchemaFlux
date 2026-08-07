---
title: "SchemaFlux Production Readiness and Target Architecture Specification"
subtitle: "A normative design for isolated clients, one execution kernel, dual APIs, verifiable contracts, adaptive MDSP/SDMP execution, and governable operations"
author: "SchemaFlux Architecture Working Draft"
date: "5 August 2026"
lang: en-US
---

![SchemaFlux target architecture](figures/target_architecture.png){width=92%}

**Version 1.0 - Proposed target-state specification**

**Audience:** SchemaFlux maintainers, contributors, platform engineers, security reviewers, and enterprise adopters

**Status:** Design proposal; normative for the target architecture, not a claim that the current repository already satisfies the requirements

**Scheduling:** `TODOS.md` is the ordering authority and carries the reconciliation. Every gap in §2 maps to at least one task there — see *Reconciliation with `to-production.md`* — and the delivery gates of §18 map onto its milestones: Gate 0 ≈ M00–M03, Gate 1 ≈ M04 + M06 + IN-004, Gate 2 ≈ M11 + M12, Gate 3 ≈ M13, Gate 4 ≈ M14 + M02, Gate 5 ≈ M15–M17. Where this document and the adversarial review disagree, `TODOS.md` records the ruling rather than either document being silently edited. The milestones implementing §§8–19 (M11–M17) are scheduled but **not yet committed**: adopting them in full is roughly three times the remaining work, and which of them belong in v1 is an open scoping decision recorded there.

**Known defect in this document:** the nine figures referenced below (`figures/*.png`) do not exist in the repository, so every image renders broken. Generate them or remove the references — tracked as **PS-008**.

**Source basis:** The gap inventory is derived from the attached 2,720-line architectural and production-readiness analysis and the immediately following discussion of trust, provenance, schema semantics, caching, streaming, fairness, and governance. Target-state details, acceptance thresholds, and concrete API shapes are design recommendations added by this specification.

# Document control {.unnumbered}

| Field | Value |
| --- | --- |
| Document owner | SchemaFlux maintainers |
| Intended milestone | Production-ready v1 architecture |
| Decision horizon | Core public contract and runtime boundaries |
| Primary language/runtime | Go |
| Normative keywords | MUST, MUST NOT, SHOULD, SHOULD NOT, MAY |
| Compatibility posture | Preserve standard and fluent APIs; converge implementation |
| Primary risk addressed | Probabilistic operations appearing typed and reliable without enforceable execution, trust, and governance contracts |
| Change control | Architecture Decision Records plus semantic release notes |

## Normative language {.unnumbered}

The keywords **MUST**, **MUST NOT**, **REQUIRED**, **SHOULD**, **SHOULD NOT**, and **MAY** are used as design requirements. “MUST” indicates a v1 release gate. “SHOULD” indicates a strong default that may be overridden only by a documented Architecture Decision Record (ADR). “MAY” identifies an optional extension that must not weaken the core contract.

This specification separates three kinds of statements:

- **Source-derived gap:** a concern identified in the supplied analysis.
- **Normative decision:** the target behavior SchemaFlux should implement.
- **Recommended acceptance target:** an initial measurable threshold that maintainers can tune after baselining.

## Executive summary {.unnumbered}

SchemaFlux has a compelling product-level idea: developers express semantic LLM operations as typed Go calls rather than manually constructing provider messages and parsing JSON. The production gap is not primarily missing verbs. It is that the current concept needs a smaller and more governable center: isolated clients, a single execution kernel, explicit contracts, an execution planner, typed failure semantics, verifiable batch protocols, provider capability negotiation, and a trust model.

The target architecture makes the following commitments:

1. **One execution kernel.** Every standard function, fluent builder, recipe, and batch operation lowers to the same `Op[In, Out]`, policy resolution, `Plan`, and `Execute` path.
2. **No mutable global execution state in core.** Credentials, providers, budgets, loggers, caches, and telemetry are instance-scoped or invocation-scoped.
3. **Both API styles remain.** The standard Go API is the canonical interoperability surface; the fluent API is the primary ergonomic surface. They are mechanically equivalent.
4. **Context is mandatory at execution.** `Run(ctx)` is the stable boundary; background context is explicitly named and confined to convenience packages.
5. **Typed output is not treated as proof.** Decode, structural, invariant, evidence, provenance, capability, and data-policy contracts are separate and reported.
6. **Execution shape is explicit.** Atomic, MDSP, SDMP, and global/hierarchical operations are planned rather than hidden in loops or operation-specific helpers.
7. **Batch optimistically, repair selectively.** Homogeneous work uses token-aware MDSP chunks, validates every item independently, and degrades only failed items toward smaller chunks, atomic execution, escalation, or review.
8. **One recovery state machine and one budget tree.** Retry, repair, split, fallback, escalation, cancellation, and human review are governed by typed failures and a parent attempt ledger.
9. **Providers advertise actual guarantees.** Routing and fallback occur only when minimum structured-output, policy, region, retention, and observability contracts can be preserved.
10. **Deterministic work stays deterministic.** Go performs parsing, membership, ordering, schema checks, range checks, reconstruction, and invariants wherever possible.
11. **Every invocation produces an execution envelope.** The envelope explains what ran, under which versions and guarantees, how it recovered, what it cost, and which claims are evidenced.
12. **The host owns infrastructure lifecycle.** SchemaFlux emits logs, metrics, and traces through injected interfaces; applications own exporters, persistence, sampling, and shutdown.
13. **Security and governance are first-class.** Untrusted data is labeled and delimited; content logging is off by default; routing respects data policy; raw diagnostics are bounded and redacted.
14. **Production support is earned by conformance.** Providers and stable operations pass deterministic, fuzz, race, replay, semantic, load, chaos, and policy tests before being labeled production-ready.
15. **v1 is a contract, not a feature count.** Experimental verbs may remain available without inheriting the stability promises of the core operation set.

::: {custom-style="Decision"}
**D-001 — Small center, broad edge.** The durable core is `Client + Provider + Op + Plan + Execute + Contract + Result + Error + Budget`. Providers, exporters, persistence, recipes, convenience globals, and application bootstrap remain adapters around that center.
:::

::: {custom-style="Decision"}
**D-002 — Atomicity is a correctness tool, not the default scheduling strategy.** Homogeneous collections should normally run as bounded MDSP chunks with per-item validation and selective atomic fallback. Global comparative operations require operation-specific split/merge algebra rather than generic batching.
:::

::: {custom-style="Decision"}
**D-003 — Governability is part of the return value.** A caller can request `(T, error)` for convenience, but the runtime always constructs an execution envelope and can return it through detailed APIs or a diagnostic sink.
:::

# Contents {.unnumbered}

1. Scope, goals, and non-goals  
2. Consolidated gap register  
3. Target architecture  
4. Client, configuration, and lifecycle  
5. Public API specification  
6. Operation descriptors and semantic taxonomy  
7. Contract and result model  
8. Execution planning and shapes  
9. MDSP batching, loop handling, and scheduling  
10. Failure model, retries, repair, and escalation  
11. Trust, evidence, and provenance  
12. Schema identity, evolution, and reproducibility  
13. Provider capability, routing, and fallback  
14. Security, privacy, and caching  
15. Observability, cost, and operational lifecycle  
16. Testing and verification strategy  
17. Release engineering and compatibility  
18. Migration architecture and delivery gates  
19. v1 acceptance criteria  
20. Risk register and trade-offs  
21. Architecture Decision Record summary  
Appendices A-G: API, errors, batchability, defaults, CI, traceability, glossary

# 1. Scope, goals, and non-goals

## 1.1 Scope

This specification covers the target production architecture for SchemaFlux as a reusable Go library. It addresses:

- public standard and fluent APIs;
- client ownership and configuration scopes;
- operation descriptors and semantic taxonomy;
- provider interfaces and capability negotiation;
- execution planning for atomic, MDSP, SDMP, and global operations;
- scheduling, batching, backpressure, fairness, and rate control;
- typed errors, retries, repair, fallback, escalation, and partial success;
- structural, invariant, evidence, provenance, capability, and data-policy contracts;
- schema generation, type support, optionality, precision, and evolution;
- prompt injection, logging, caching, data residency, and security controls;
- observability, cost accounting, lifecycle, readiness, and runbooks;
- deterministic, replay, conformance, semantic, load, and chaos testing;
- migration from package-global, operation-by-operation behavior to the target kernel.

It intentionally treats the library as infrastructure that may sit inside multi-tenant and consequential applications. The design therefore assumes that correctness, privacy, cost, and failure isolation are not optional add-ons.

## 1.2 Design goals

The target architecture SHALL:

- preserve the memorable fluent API while supporting idiomatic Go functions;
- make every dependency and policy scope explicit;
- provide safe defaults without preventing advanced provider-specific use;
- retain partial results without silently accepting invalid items;
- make behavior inspectable before and after execution;
- support high-throughput loops without requiring callers to hand-build worker pools;
- permit domain-specific invariants and evidence rules without bypassing telemetry or recovery;
- allow provider substitution only when requested guarantees are preserved;
- minimize core dependencies and avoid owning the host process’s observability stack;
- provide enough execution identity to reproduce, compare, and audit model behavior over time.

## 1.3 Non-goals

The v1 core is not intended to:

- prove that an LLM-generated claim is objectively true merely because it matches a schema;
- replace domain-specific deterministic validators, policy engines, or human review;
- become a general-purpose workflow/orchestration language embedded in fluent builders;
- hide provider capability differences behind a false promise of identical semantics;
- guarantee deterministic model output across provider or model revisions;
- make every experimental semantic verb stable at v1;
- configure global OpenTelemetry exporters, log storage, or database lifecycle for the host application;
- automatically perform side effects based on model output;
- treat semantic caching of “similar” inputs as safe by default.

## 1.4 Recommended v1 success criteria

A v1 release is production-ready when all of the following are true:

- no stable operation can bypass the common execution kernel;
- no core execution policy depends on mutable process-global state;
- every supported provider passes the same conformance suite for its declared capabilities;
- every stable collection operation declares and tests its batch algebra;
- every provider call is attributable to a logical request, stage, chunk, and item set;
- cancellation, budget exhaustion, circuit opening, and partial success have stable typed semantics;
- prompts and completions are not logged by default and secrets are never logged;
- race tests, fuzz targets, replay suites, semantic release gates, and leak checks are green;
- compatibility, security, provider support, and deprecation policies are published;
- the execution envelope reports delivered contract level, not merely requested intent.

# 2. Consolidated gap register

The following register is the requirements baseline. The IDs are used throughout design decisions, implementation milestones, tests, and the final traceability matrix. The “target response” column is normative direction, not a claim about current implementation status.

## 2.1 Architectural gaps

| ID | Gap | Observed consequence | Target response |
| --- | --- | --- | --- |
| ARC-01 | Client isolation | Client configuration mutates package-global provider state. | Every client MUST own an immutable execution snapshot and provider registry; the core runtime MUST contain no mutable process-global execution state. |
| ARC-02 | Dependency injection boundary | Provider selection stops before the actual execution boundary. | `Run` MUST receive or resolve an instance-scoped client and MUST carry all provider, policy, and telemetry dependencies through the invocation. |
| ARC-03 | Verb explosion | Prompt vocabulary is encoded as dozens of top-level architectural surfaces. | Stable operations MUST be a small, versioned core; higher-order semantics SHOULD be recipes over shared descriptors and contracts. |
| ARC-04 | Operation descriptors | Operations are implementations first and declarative descriptors second. | Every public operation MUST compile to an `Op[In, Out]` descriptor executed by the same kernel. |
| ARC-05 | Dual API divergence | Fluent and standard APIs risk different defaults and execution paths. | Both API surfaces MUST lower to the same descriptor, policy resolver, planner, and executor. |
| ARC-06 | Optional context | A parameterless `Run()` makes network lifecycle control optional. | The stable API MUST require `context.Context` at execution; background execution MUST be an explicitly named convenience. |
| ARC-07 | Mutable builders and clients | Locks prevent races but do not prevent semantic reconfiguration races. | Clients and builders SHOULD use immutable value semantics after construction; snapshots MUST be explicit. |
| ARC-08 | Configuration scope ambiguity | Process, client, operation, request, and provider settings overlap. | Every setting MUST declare one scope and precedence rules MUST be deterministic and inspectable. |
| ARC-09 | Provider leakage | The generic client owns concrete provider assumptions and legacy fields. | Provider modules MUST own credential resolution, defaults, schema adaptation, retry classification, and model mapping. |
| ARC-10 | Capability differences | Providers silently omit or rewrite unsupported schema features. | Providers MUST publish machine-readable capabilities; planning MUST negotiate a minimum contract before execution. |
| ARC-11 | Shape safety only | Decoding into `T` appears stronger than the semantic guarantee delivered. | Contracts MUST separately represent decode, structural, invariant, evidence, provenance, capability, and data-policy guarantees. |
| ARC-12 | Probabilistic/deterministic mixing | Models perform checks and reconstruction that Go can perform exactly. | The runtime MUST be deterministic-first and use model judgment only for irreducibly probabilistic decisions. |
| ARC-13 | Metadata provenance | Model claims, deterministic observations, and runtime facts are mixed. | Result types MUST distinguish model-produced content, verified checks, and runtime metadata. |
| ARC-14 | Fragmented recovery loops | Retry, repair, validation, fallback, and escalation are separate mechanisms. | All provider calls MUST be governed by one explicit state machine and one hierarchical attempt budget. |
| ARC-15 | Weak error protocol | Consumers cannot reliably automate recovery without parsing strings. | A stable typed error taxonomy with `errors.Is`/`errors.As` semantics MUST be the control protocol. |
| ARC-16 | Collection semantics | Generic chunking does not preserve ranking, clustering, or global comparison semantics. | Each operation category MUST declare a batch algebra: split, encode, merge, and global invariant validation. |
| ARC-17 | Tightly packaged dependencies | Core execution directly carries exporters, persistence, and provider SDK weight. | Optional integrations MUST live behind interfaces and separate packages; core MUST remain lightweight. |
| ARC-18 | Observability ownership | The library risks configuring the host process logging/tracing stack. | SchemaFlux MUST emit instrumentation; the host MUST own exporters, sampling, storage, and shutdown. |
| ARC-19 | Environment loading | Core initialization mutates process configuration by loading `.env`. | Environment loading MUST remain at application/bootstrap boundaries or an explicit convenience package. |
| ARC-20 | Multi-turn mismatch | Conversational operation names are implemented on a single-turn substrate. | Multi-turn semantics MUST require a session/message abstraction or be renamed as single-shot operations. |
| ARC-21 | Control-flow mixing | Pipelines, voting, fallback, and approval are mixed with domain verbs. | Execution combinators MUST be separate from semantic operations and SHOULD compose `Op -> Op`. |
| ARC-22 | Fragmented result vocabulary | Related review operations expose incompatible verdict and issue shapes. | Stable result families MUST share reusable judgment, transformation, collection, and text contracts. |
| ARC-23 | Prompt compatibility surface | Prompt edits can be behavioral breaking changes without Go API changes. | Operations and prompt templates MUST be versioned, fingerprinted, tested, and included in release notes. |
| ARC-24 | Invisible degradation | Fallback or tier changes can silently reduce guarantees. | Every result MUST state requested and delivered contract levels and any degradation MUST require policy approval. |

## 2.2 Production-readiness gaps

| ID | Gap | Observed consequence | Target response |
| --- | --- | --- | --- |
| PRD-01 | CI quality gate | No comprehensive gate for test, race, lint, vulnerability, shuffle, generation, examples, and platform matrix. | Build a required multi-platform CI pipeline with deterministic and probabilistic test lanes. |
| PRD-02 | Provider conformance | Adapter presence is treated as provider support. | A provider MUST pass a versioned conformance suite and publish a capability matrix before being labeled supported. |
| PRD-03 | Release process | Tags, changelog, compatibility policy, and semantic release notes are incomplete. | Adopt SemVer, release candidates, migration guides, deprecation windows, and behavior-change notes. |
| PRD-04 | Security policy | No complete vulnerability process and SchemaFlux-specific threat model. | Publish `SECURITY.md`, supported-version policy, threat model, and response targets. |
| PRD-05 | Safe logging | Prompts, completions, headers, and diagnostic bodies can leak sensitive content. | Metadata-only logging MUST be the default; content logging MUST be explicit, bounded, redacted, and policy-gated. |
| PRD-06 | Hard budgets | Retries and repair lack one logical request ceiling. | Enforce provider-call, token, cost, elapsed-time, chunk, and escalation budgets across the complete execution tree. |
| PRD-07 | Resilience controls | Retries can amplify outages without breakers, limits, or shedding. | Provide per-provider/model bulkheads, rate limits, queues, breakers, jitter, and observable admission rejection. |
| PRD-08 | Public error contract | Failure kinds and structured context are not sufficiently stable. | Export typed kinds, structured context, recoverability metadata, and deterministic wrapping behavior. |
| PRD-09 | Safe diagnostics | Raw provider evidence is needed for debugging but creates disclosure risk. | Use opt-in, size-limited, redacted diagnostic sinks; never embed raw bodies in normal errors. |
| PRD-10 | Idempotency | Ambiguous network failures can duplicate remote work and downstream side effects. | Carry logical request IDs, attempt IDs, fingerprints, and provider idempotency keys where supported. |
| PRD-11 | Cancellation coverage | Cancellation must propagate through every blocking boundary. | Test queues, backoff, HTTP, streams, workers, storage, and exporters; no goroutine may outlive a canceled request. |
| PRD-12 | Fuzzing | Reflection, schemas, JSON, and repair paths consume hostile input. | Maintain fuzz targets and allocation/depth limits for schema generation, decoding, normalization, and repair. |
| PRD-13 | Replay fixtures | Live provider tests are not reproducible. | Store sanitized request/response fixtures and replay the entire runtime path without network access. |
| PRD-14 | Operation versioning | Prompt and contract changes are not independently versioned. | Attach operation name/version, prompt version, schema hash, and runtime version to every execution. |
| PRD-15 | Semantic benchmarks | Structural tests do not detect quality regressions. | Release candidates MUST run pinned semantic suites measuring validity, accuracy, hallucination, latency, and cost. |
| PRD-16 | Stability tiers | All verbs appear equally production-ready. | Mark stable, beta, and experimental operations in packages, documentation, and compatibility policy. |
| PRD-17 | Platform guarantees | Go, OS, architecture, CGO, and SQLite support are not an explicit contract. | Publish and continuously test a support matrix. |
| PRD-18 | Readiness diagnostics | Configuration failures surface only under paid traffic. | Provide non-billable validation and optional provider probes. |
| PRD-19 | Configuration validation | Invalid combinations can survive initialization. | Reject unsafe or contradictory configuration at client creation or preflight. |
| PRD-20 | Shutdown lifecycle | Workers, exporters, stores, and transports need deterministic cleanup. | `Close` MUST be idempotent, bounded, and ownership-aware. |
| PRD-21 | HTTP ownership | Opaque transports block enterprise networking and testing. | Providers MUST accept caller-owned clients/transports with documented ownership. |
| PRD-22 | Input preflight | Oversized requests fail after cost and latency are incurred. | Estimate context, output reserve, call fan-out, and cost before execution. |
| PRD-23 | Backpressure | Batch/map-reduce can spawn unbounded work. | Use bounded queues, weighted admission, cancellation, and explicit partial-failure semantics. |
| PRD-24 | Runbooks | Operators lack standardized incident actions. | Ship dashboards, alerts, runbooks, and post-incident evidence guidance. |
| PRD-25 | Repository hygiene | Adoption contracts and contributor workflows are incomplete. | Add licensing, security, contributing, templates, architecture records, and tagged examples. |

## 2.3 API and execution-shape gaps

| ID | Gap | Observed consequence | Target response |
| --- | --- | --- | --- |
| API-01 | Preserve dual API surfaces | Keep standard and fluent styles because they serve different consumers. | Treat the standard API as canonical semantics and fluent builders as mechanically equivalent configuration sugar. |
| API-02 | Canonical standard API | Dynamic option construction and dependency injection need conventional functions. | Expose `Extract`, `Transform`, `Run`, and result variants taking `ctx`, `client`, input, and options. |
| API-03 | Fluent equivalence | The fluent surface is a project differentiator. | Every fluent call MUST compile to the same options and operation descriptor as its standard equivalent. |
| API-04 | Context at execution | Context is invocation lifetime, not builder configuration. | Stable builders MUST expose `Run(ctx)`; context MUST NOT be retained across reusable builders. |
| API-05 | Immutable builders | Branching a builder must not mutate sibling configurations. | Builder methods SHOULD return value copies and clone mutable option collections. |
| API-06 | Consistent grammar | Synonymous quality and policy controls damage discoverability. | Define a small cross-operation vocabulary and reserve operation-specific methods for real contracts. |
| API-07 | Option escape hatch | Fluent methods cannot scale to every policy. | All builders MUST support `With(opts...)` using the same options as the standard API. |
| API-08 | Reusable policies | Production callers need centrally assembled policy bundles. | Options MUST be composable, validated, and scope-aware. |
| API-09 | Descriptor layer | Framework integration needs operations as data. | Expose reusable typed operation descriptors for planning, composition, middleware, and testing. |
| API-10 | Concrete builder types | Interface-heavy fluent APIs weaken autocomplete and diagnostics. | Use concrete generic builder values with small shared internals. |
| API-11 | Simple and detailed results | Idiomatic `(T, error)` and governable envelopes serve different needs. | Provide `Run`/`Extract` and explicit `RunResult`/`ExtractResult` variants with identical execution. |
| API-12 | No workflow DSL creep | Operation builders should not become a general control-flow language. | Keep one-operation configuration fluent; use descriptors, recipes, or ordinary Go for composition. |
| EXE-01 | Explicit execution shapes | Atomic, MDSP, SDMP, and global work have different correctness/latency profiles. | The planner MUST select and record an explicit execution mode. |
| EXE-02 | Serial loop stalls | One blocking provider call per iteration is operationally unusable at scale. | Plural APIs and batch groups MUST be the default path for homogeneous collections. |
| EXE-03 | Stable item identity | Position-only batch alignment fails under omission and reordering. | Every item MUST have a stable invocation-local ID and batch protocol validation. |
| EXE-04 | Token-aware chunks | Fixed item counts ignore input and schema size. | Chunk packing MUST honor item, context, output-reserve, cost, and safety-margin limits. |
| EXE-05 | Adaptive chunking | One chunk size cannot optimize every provider/model/schema. | Adaptive policies SHOULD grow on sustained compliance and shrink on omissions, truncation, or repair. |
| EXE-06 | Failure class separation | Transport, syntax, protocol, and semantic failures need different recovery. | The state machine MUST classify failures before selecting retry, repair, split, regenerate, or terminal action. |
| EXE-07 | Partial success | `([]T, error)` cannot represent long-running batch outcomes. | Return item-level success/failure plus batch summary and policy-controlled aggregate errors. |
| EXE-08 | Failure policies | Callers need precise fail-fast, collect, retry, and require-all behavior. | Batch policies MUST define scheduling, cancellation, retry, and return semantics. |
| EXE-09 | Bounded concurrency | Parallel chunks can still overload quotas and memory. | Limit calls, tokens, cost, queue depth, and tenant/provider shares. |
| EXE-10 | SDMP reuse | Repeated stages resend the same source and schema. | The planner SHOULD reuse deterministic intermediates and provider prompt caches where policy permits. |
| EXE-11 | Deterministic-first | Extra prompts are often used for checks Go can perform exactly. | Perform deterministic validation and reconstruction before adding model stages. |
| EXE-12 | Reference/delta outputs | Models should not echo large input objects. | Collection operations SHOULD return stable IDs, scores, classifications, patches, or minimal deltas. |
| EXE-13 | Batchability classes | Independent and global operations cannot share one generic map/reduce. | Every stable operation MUST declare independent, subset, permutation, partition, or global semantics. |
| EXE-14 | Visible mode policy | Users need to force atomicity or batching for risk/cost reasons. | Expose `Atomic`, `Batched`, `Adaptive`, and operation-specific planning policies. |
| EXE-15 | Singular/plural APIs | Silently interpreting a slice changes failure and ordering contracts. | Provide explicit `Extract`/`ExtractMany` and corresponding fluent builders. |
| EXE-16 | Loop fusion | Natural Go loops can describe compatible work without scheduling it efficiently. | An optional batch group MAY fuse compatible deferred builders into MDSP plans. |
| EXE-17 | Hierarchical budgets | Chunk and item recovery can expand call counts unexpectedly. | Track logical request, stage, chunk, and item budgets under one parent ledger. |
| EXE-18 | Progressive degradation | Escalating the whole batch wastes capacity. | Recover large MDSP -> smaller MDSP -> atomic -> stronger model/review only for unresolved items. |
| EXE-19 | Per-item metrics | HTTP success hides item omissions and invalid outputs. | Measure valid-item ratio, omissions, repairs, fallbacks, and cost per accepted item. |
| EXE-20 | Plan/execute separation | Execution strategy is currently implicit in operation code. | Expose inspectable `Plan` and deterministic `Execute` boundaries. |

## 2.4 Trust, correctness, and governance gaps

| ID | Gap | Observed consequence | Target response |
| --- | --- | --- | --- |
| TRU-01 | Prompt injection | Untrusted input can alter instructions despite typed output. | Represent prompt segments by role and trust; isolate untrusted data and never treat model output as trusted before checks. |
| TRU-02 | Evidence grounding | Structurally valid fields can be unsupported inventions. | Support source spans/JSON pointers, evidence modes, inference labels, and strict evidence contracts. |
| TRU-03 | Pipeline provenance | Lineage disappears when model outputs feed later stages. | Carry parent result IDs, input digests, operation/prompt/schema versions, and item-level execution paths. |
| TRU-04 | Model drift | Aliases and provider updates change semantics without code changes. | Record requested/resolved models and revisions; distinguish floating tiers from pinned models. |
| TRU-05 | Schema evolution | Go type changes alter prompts, requiredness, caches, and stored results. | Give schemas stable names, versions, hashes, compatibility rules, and migrations. |
| TRU-06 | Missing vs zero values | Go zero values erase whether a field was absent or explicitly supplied. | Support pointers, `Optional[T]`, and/or presence metadata; strict mode MUST preserve presence semantics. |
| TRU-07 | Numeric precision | `float64` can corrupt money, identifiers, and large integers. | Use `json.Number`, exact decimals, range checks, and type-specific schema guidance. |
| TRU-08 | Unknown fields | Decoders may silently discard hallucinated fields. | Exact-contract mode MUST reject unknown properties and report overproduction. |
| TRU-09 | Complex type support | Recursive, interface-heavy, and custom-marshaled types have unclear behavior. | Publish a type support matrix and fail preflight for unsupported shapes. |
| TRU-10 | Caching semantics | Naive hashes ignore model, prompt, schema, and policy identity. | Cache keys MUST include the complete semantic execution identity; semantic result caching MUST be opt-in. |
| TRU-11 | Data residency | Easy provider switching can violate regional or contractual restrictions. | Data policy MUST constrain eligible providers, regions, retention, and training use before planning. |
| TRU-12 | Fallback semantics | Fallback can reduce structured-output or policy guarantees. | Fallback MUST preserve minimum contracts and disclose any approved degradation. |
| TRU-13 | Streaming typed output | Token streams are not automatically valid typed partial results. | Support explicit text, event, and validated-item streaming modes with partial semantics. |
| TRU-14 | Caller backpressure | Fast producers and slow consumers can grow memory without bound. | Streaming/batch iterators MUST be bounded, cancelable, and ownership-defined. |
| TRU-15 | Noisy neighbors | Large tenants can monopolize global capacity. | Support tenant keys, weighted fair queues, quotas, priorities, and starvation prevention. |
| TRU-16 | Mutation semantics | Deferred builders can observe caller mutations unpredictably. | Document snapshot/read-at-run semantics and reject concurrent mutation through race tests. |
| TRU-17 | Ordering and duplicates | Concurrent recovery can reorder items and duplicate values make equality ambiguous. | Preserve input order by default and use stable invocation IDs for all reconciliation. |
| TRU-18 | Cancellation partials | Cancellation after partial completion needs explicit return semantics. | Stop scheduling, cancel in-flight work, return verified completed items, and mark unresolved items canceled. |
| TRU-19 | Repair amplification | Feeding an invalid answer back can reinforce injection or fabricated content. | Select repair strategy by failure class and regenerate from source for semantic/evidence failures. |
| TRU-20 | Repair regression | A repaired answer can drop valid information while passing a weaker check. | Repair MUST reapply the original contract and detect unrelated field loss or mutation. |
| TRU-21 | Nondeterministic tests | Live assertions can become flaky under probabilistic output. | Use replay for deterministic paths and statistical thresholds for semantic suites. |
| TRU-22 | Nested cost attribution | Batch, repair, and escalation costs cannot be explained by flat records. | Record a parent/child execution and cost tree down to item attempts. |
| TRU-23 | Pricing drift | Historical costs become wrong when current tables are reapplied. | Version pricing sources/effective dates and distinguish exact, estimated, unknown, and free. |
| TRU-24 | Operation semantics | English verb names do not define permitted inference or batching behavior. | Document each stable operation’s semantic contract, invariants, evidence rules, and batch algebra. |
| TRU-25 | Domain extension points | Core cannot know every domain rule. | Allow custom normalization, invariants, evidence checks, and repair policies inside the kernel. |
| TRU-26 | Human review | Some uncertainty should not trigger more model calls. | Provide `ReviewRequired` outcomes and structured review packets. |
| TRU-27 | Ensemble semantics | Majority agreement can amplify correlated hallucination. | Make reconciliation explicit, pluggable, evidence-aware, and able to abstain. |
| TRU-28 | Planner explainability | Adaptive routing and recovery become opaque. | Expose a pre-execution plan explanation and a post-execution decision ledger. |
| TRU-29 | Dry run | Large fan-out should be inspectable before spend. | Preflight MUST report eligibility, chunking, maximum calls, budget, and estimated cost without generation. |
| TRU-30 | Validation terminology | A probabilistic review can be mistaken for authoritative validation. | Distinguish deterministic validation, model-assisted review, and hybrid validation in names and results. |

# 3. Target architecture

![Figure 1 - Target architecture](figures/target_architecture.png){width=94%}

The architecture is divided into a small deterministic core and optional edges. Data flows downward through API lowering, policy resolution, planning, scheduling, provider execution, and contract verification. Runtime facts flow upward into a single execution envelope. Host-owned infrastructure is connected through interfaces and must not be configured implicitly by the library.

## 3.1 Layer responsibilities

**Public API surfaces.** Standard functions, fluent builders, and reusable operation descriptors accept user intent. They perform local validation and lower into the same `Op` plus options. They do not perform retries, provider routing, parsing, or telemetry independently.

**Core runtime.** The core resolves immutable client policy plus invocation options, validates scope and capability requirements, creates a logical request identity, and invokes `Plan` followed by `Execute`.

**Execution planner.** The planner selects execution shape, chunking, model/provider routes, expected contract level, retry/repair policies, maximum call tree, and estimates. Planning is deterministic given the same inputs, policy snapshot, capability snapshot, and estimator versions.

**Bounded scheduler.** The scheduler enforces queue, concurrency, token, cost, tenant, provider, and model limits. It owns no business semantics; it schedules plan nodes and propagates cancellation.

**Provider boundary.** Provider modules translate a provider-neutral request into provider-specific transport, classify failures, normalize usage, expose capabilities, and preserve provider request IDs. They do not decide application-level repair or fallback.

**Trust and correctness layer.** Decoders, schema validators, invariants, evidence checks, provenance assembly, and data-policy checks determine whether output is acceptable. A provider HTTP 200 is not a logical success.

**Execution envelope.** The envelope contains value or partial results, delivered guarantees, attempt history, usage/cost, provenance, sanitized diagnostics, and review requirements.

::: {custom-style="Requirement"}
**ARCH-001.** The core runtime MUST have no dependency on concrete provider SDKs, OpenTelemetry exporters, SQLite, `.env` loaders, or global log buffers. Optional packages MAY depend on those components.
:::

::: {custom-style="Requirement"}
**ARCH-002.** Every public stable operation MUST be executable through `Run[In, Out]`; tests MUST fail if an operation performs a provider call outside the common executor.
:::

::: {custom-style="Requirement"}
**ARCH-003.** Planning and execution MUST be separate observable phases. Callers MUST be able to preflight or explain a plan without issuing a generation request.
:::

## 3.2 Proposed package topology

![Figure 2 - Package topology](figures/package_topology.png){width=96%}

A single Go module may contain these packages initially; separate modules are optional. The key requirement is dependency direction.

| Package | Responsibility | May depend on | Must not own |
| --- | --- | --- | --- |
| `schemaflux/core` | Client snapshot, operations, planner interfaces, executor, contracts, errors, envelopes, budgets | Go standard library and minimal schema/JSON abstractions | Provider SDKs, exporters, databases, environment loading |
| `schemaflux` | Stable public verbs, fluent builders, standard functions, option types | `core` | Independent execution logic |
| `schemaflux/providers/<name>` | Transport, auth, capability map, provider error mapping, usage normalization | `core`, caller-supplied HTTP stack | Application fallback policy or global registration |
| `schemaflux/batch` | Chunkers, schedulers, fairness, loop-fusion groups, batch protocols | `core` | Operation-specific semantic shortcuts not declared by descriptors |
| `schemaflux/recipes` | Higher-order compositions and experimental semantic verbs | `schemaflux`, `core` | New provider execution paths |
| `schemaflux/telemetry/otel` | OpenTelemetry adapter for core observer interfaces | OTel SDK/API | Global SDK initialization or exporter lifecycle |
| `schemaflux/pricing/sqlite` | Optional durable pricing/usage store | database driver, `core` interfaces | Mandatory execution dependency |
| `schemaflux/schemafluxtest` | Fake providers, replay transport, conformance harness, fixtures | all public interfaces | Production globals |
| `schemaflux/quick` | Single-process convenience initialization for scripts/examples | public package and providers | Production guarantees or multi-tenant safety |

## 3.3 Dependency direction and ownership

The dependency graph MUST point toward core abstractions. Core interfaces may describe a logger, tracer, metric sink, cache, pricing resolver, clock, ID generator, and diagnostic sink. Implementations are injected by the host or optional adapters.

Ownership rules:

- Objects created by SchemaFlux are closed by SchemaFlux.
- Caller-owned HTTP clients, transports, loggers, tracers, caches, and exporters are never closed unless the constructor explicitly transfers ownership.
- `Client.Close()` is idempotent and closes only owned resources.
- An operation builder owns no goroutine, open file, provider connection, or request context.
- A `Plan` is immutable after creation.
- An execution envelope is immutable after completion; streaming executions expose an append-only event stream and a final immutable envelope.

## 3.4 Core type model

```go
package core

type OperationID struct {
    Name    string // e.g. "extract"
    Version string // e.g. "v2"
}

type Op[In, Out any] struct {
    ID             OperationID
    Semantics      Semantics
    Prompt         PromptBuilder[In]
    Output         OutputContract[In, Out]
    Batch          BatchAlgebra[In, Out]
    DefaultPolicy  Policy
}

type Client struct {
    // Unexported immutable snapshot. Any runtime-owned mutable components
    // are concurrency-safe and scoped to this instance.
}

func Run[In, Out any](
    ctx context.Context,
    client *Client,
    op Op[In, Out],
    input In,
    opts ...RunOption,
) (Out, error)

func RunResult[In, Out any](
    ctx context.Context,
    client *Client,
    op Op[In, Out],
    input In,
    opts ...RunOption,
) (Result[Out], error)
```

`Op` is primarily data plus pure functions. It does not hold a request context or mutable provider. The executor is the only component authorized to issue provider calls.

# 4. Client, configuration, and lifecycle

## 4.1 Instance isolation

The `Client` is the production boundary for provider registry, routing policy, scheduler, budgets, instrumentation, cache policy, and lifecycle. Creating or changing one client must not affect another client.

Recommended construction:

```go
client, err := schemaflux.NewClient(
    schemaflux.WithProvider("primary", openai.New(openai.Config{
        APIKey:     secret,
        HTTPClient: httpClient,
    })),
    schemaflux.WithDefaultRoute("primary"),
    schemaflux.WithBudget(schemaflux.Budget{
        MaxProviderCalls: 12,
        MaxTotalTokens:   80_000,
        MaxCost:          money.USD("0.75"),
        MaxElapsed:       45 * time.Second,
    }),
    schemaflux.WithObserver(observer),
    schemaflux.WithScheduler(scheduler),
)
if err != nil {
    return err
}
defer client.Close()
```

Construction validates static configuration. Dynamic provider reachability is checked by `ValidateConfiguration` or an explicit live probe, not by a hidden paid generation.

::: {custom-style="Decision"}
**D-004 — Immutable client policy.** `WithX` methods on an already active client are not part of the stable API. Configuration options are applied at construction. If runtime policy must change, the application constructs a new client snapshot and swaps it at its own boundary.
:::

## 4.2 Configuration scopes and precedence

Every setting belongs to exactly one scope:

| Scope | Examples | Lifetime | Precedence |
| --- | --- | --- | --- |
| Process/bootstrap | environment parsing, secret retrieval, exporter initialization | application lifetime | Outside SchemaFlux core |
| Client | provider registry, default route, scheduler, base budgets, logger, cache policy | client lifetime | Lowest runtime policy |
| Operation descriptor | semantic defaults, minimum contract, batch algebra, stable prompt version | operation version lifetime | Overrides generic client defaults where explicitly permitted |
| Invocation option | model pin, strictness, budget reduction, metadata, failure policy | one logical request | Overrides client/operation only within allowed bounds |
| Request context | deadline, cancellation, trace context, caller identity | one execution | Cannot be stored in reusable builders |
| Provider request | resolved model, response format, idempotency key, timeout slice | one provider attempt | Computed by planner/executor; not directly mutable by operation code |

Precedence is deterministic: request context termination rules always win; invocation policy may make limits stricter but may not silently weaken non-overridable client or data-policy constraints. Provider-specific options are namespaced and validated against the selected provider.

The plan explanation MUST display the effective value and source for material settings:

```text
max_provider_calls = 8        source: invocation (client default 12)
minimum_contract   = SchemaAndInvariantChecked  source: operation
content_logging    = MetadataOnly               source: client locked
provider_route     = primary/openai              source: client default
data_region        = us-east                     source: tenant policy locked
```

## 4.3 Lifecycle and shutdown

`Client.Close()` MUST:

- atomically stop accepting new logical requests;
- cancel runtime-owned background workers;
- allow a configurable grace period for active requests;
- flush runtime-owned diagnostic buffers and optional stores;
- close runtime-owned idle HTTP connections;
- not close caller-owned transports or exporters;
- return a joined error for failed owned-resource shutdown;
- remain safe and idempotent under repeated or concurrent calls.

An execution started before shutdown either completes within the grace period or returns a typed shutdown/cancellation error with any validated partial results. No goroutine may survive the client’s final close.

## 4.4 Readiness and preflight

```go
type ReadinessReport struct {
    Ready          bool
    Issues         []ConfigurationIssue
    Providers      []ProviderReadiness
    PolicySnapshot PolicySummary
}

report, err := client.ValidateConfiguration(ctx)
```

The non-billable check validates provider registration, credential presence without revealing values, model maps, endpoint scheme/host policy, HTTP client presence, capability assumptions, schema support, scheduler limits, cache/storage readiness, and contradictory configuration. A separate `ProbeProviders` API MAY perform explicit network health requests and must label any billable probe.

# 5. Public API specification

## 5.1 Dual-surface contract

Keeping the fluent and standard Go APIs is an advantage when their semantics are identical. The standard API is easiest to wrap, test, and construct dynamically. The fluent API communicates intent and improves discoverability. Neither is deprecated merely for stylistic reasons.

The required lowering is:

```text
Fluent builder
  -> immutable operation input + []Option
  -> standard operation function
  -> Op[In, Out]
  -> Run / RunResult
  -> Plan
  -> Execute
```

No fluent builder may own a separate retry loop, prompt renderer, provider resolver, schema decoder, or default set.

## 5.2 Standard Go API

```go
person, err := schemaflux.Extract[Person](
    ctx,
    client,
    text,
    schemaflux.Strict(),
    schemaflux.Smart(),
    schemaflux.Steer("Use only facts explicitly present in the input"),
)

result, err := schemaflux.ExtractResult[Person](
    ctx,
    client,
    text,
    schemaflux.StrictEvidence(),
)
```

The standard functions are thin wrappers:

```go
func Extract[Out any](
    ctx context.Context,
    client *Client,
    input string,
    opts ...Option,
) (Out, error) {
    return core.Run(ctx, client, extractOp[Out](), input, opts...)
}
```

## 5.3 Fluent API

Go does not permit methods with their own type parameters on a non-generic `Client`, so the fluent generic entry point remains a package-level generic function. Client scoping is explicit and Go-valid:

```go
person, err := schemaflux.
    Extracting[Person](text).
    Using(client).
    Strict().
    Smart().
    Steer("Use only facts explicitly present in the input").
    Run(ctx)
```

For migration compatibility, a temporary builder may resolve a configured default client. The v1 production documentation MUST use `.Using(client)` or a constructor that binds a client. An optional `schemaflux/quick` package may retain single-process convenience behavior without contaminating the core contract.

The builder implementation is mechanical:

```go
type ExtractBuilder[T any] struct {
    client *Client
    input  string
    opts   optionSet
}

func (b ExtractBuilder[T]) Run(ctx context.Context) (T, error) {
    if b.client == nil {
        var zero T
        return zero, ErrClientRequired
    }
    return Extract[T](ctx, b.client, b.input, b.opts.slice()...)
}
```

## 5.4 Builder value semantics

Builder methods SHOULD use value receivers and copy-on-write option storage:

```go
func (b ExtractBuilder[T]) Strict() ExtractBuilder[T] {
    b.opts = b.opts.Clone()
    b.opts.Set(Strict())
    return b
}
```

This allows safe branching:

```go
base := schemaflux.Extracting[Person](text).Using(client)
strict := base.Strict()
fast := base.Fast()
```

The runtime contract is read-at-run unless a builder documents snapshotting. Inputs containing mutable maps, slices, or pointers MUST NOT be mutated concurrently with `Run`; race tests enforce this rule. A future `SnapshotInput()` option MAY serialize at builder creation for deferred batch groups.

## 5.5 Common builder grammar

Cross-operation methods MUST have identical meaning:

| Method / option | Meaning | Scope |
| --- | --- | --- |
| `Using(client)` | Bind the execution client | Builder |
| `With(opts...)` | Apply standard reusable options | Builder/invocation |
| `Strict()` | Require exact declared structural contract and operation invariants | Invocation |
| `StrictEvidence()` | Require evidence for material model-derived claims | Invocation |
| `Model(id)` | Request a pinned model identifier | Invocation |
| `Tier(Smart\|Fast\|Quick)` | Request a floating routing policy tier | Invocation |
| `Steer(text)` | Add trusted developer guidance within the operation contract | Invocation |
| `Budget(b)` | Reduce or specify invocation budget within client maxima | Invocation |
| `Metadata(k,v)` | Attach bounded non-sensitive correlation metadata | Invocation |
| `FailurePolicy(p)` | Select aggregate/partial failure semantics | Invocation |
| `Atomic()` / `Adaptive()` | Constrain execution shape where legal | Invocation |
| `Run(ctx)` | Execute and return simple value | Execution |
| `RunResult(ctx)` | Execute and return detailed envelope | Execution |

Operation-specific methods exist only when they change a real semantic contract, for example `Ranking(items).Top(10)`, `Filtering(items).KeepAtMost(20)`, or `Extracting[T](text).AllowMissing("middle_name")`. Synonyms such as `Smart`, `Intelligent`, `Deep`, and `HighQuality` must not coexist unless their routing contracts are materially distinct and documented.

## 5.6 Reusable operation descriptors

Advanced consumers can construct, inspect, compose, and test operations directly:

```go
op := schemaflux.ExtractOp[Person](
    schemaflux.Strict(),
    schemaflux.OperationVersion("v2"),
)

plan, err := schemaflux.Plan(ctx, client, op, text)
if err != nil {
    return err
}
fmt.Println(plan.Explain())

person, err := schemaflux.Execute(ctx, client, plan)
```

Descriptors enable middleware, recipes, loop fusion, semantic registries, and deterministic planning without introducing an alternative execution implementation.

## 5.7 Simple versus detailed return types

The default remains idiomatic:

```go
value, err := schemaflux.Extract[Person](...)
```

The detailed form returns a typed value plus governance metadata:

```go
result, err := schemaflux.ExtractResult[Person](...)
value := result.Value
fmt.Println(result.Envelope.DeliveredContract)
```

Both calls execute identically. `Run` may discard the envelope after emitting it to an optional observer, but must not change planning, checking, retry, or budget behavior.

# 6. Operation descriptors and semantic taxonomy

## 6.1 Operation as declarative data

A stable operation descriptor declares semantics rather than embedding a bespoke request loop:

```go
type Semantics struct {
    Category          OperationCategory
    PermitsInference  bool
    RequiresEvidence  bool
    PreservesIdentity bool
    PreservesOrder    bool
    Stable            Stability
}

type OutputContract[In, Out any] struct {
    Schema       SchemaDescriptor
    Decoder      Decoder[Out]
    Invariants   []Invariant[In, Out]
    Evidence     EvidencePolicy[In, Out]
    Normalizers  []Normalizer[Out]
}

type BatchAlgebra[In, Out any] struct {
    Class          Batchability
    EncodeItems    BatchEncoder[In]
    Merge          BatchMerger[Out]
    GlobalValidate GlobalInvariant[In, Out]
}
```

## 6.2 Stable operation categories

The v1 stable core SHOULD be intentionally small. Suggested categories and operations:

| Category | Candidate stable verbs | Core semantic guarantee |
| --- | --- | --- |
| Extraction | `Extract`, `Parse` where deterministic parsing is insufficient | Produce declared fields from source; inference policy explicit; optional evidence |
| Transformation | `Transform`, `Normalize`, `Conform` | Produce target representation while preserving declared information and invariants |
| Generation | `Generate`, `Summarize` | Create text or structured content under explicit grounding and length constraints |
| Classification | `Classify`, `Score` | Return allowed labels/scores with evidence or rationale policy |
| Selection | `Choose`, `Filter` | Return stable input IDs forming a member/subset contract |
| Ordering | `Rank`, `Sort` | Return scores or IDs representing a permutation/subset; reconstruct in Go |
| Review | `CheckWithModel`, `Critique` | Model-assisted judgment explicitly distinct from deterministic validation |
| Validation | `ValidateDeterministically`, `ValidateHybrid` | Run deterministic rules and optional model-assisted checks with separate provenance |

Operations such as `Negotiate`, `Arbitrate`, `Predict`, `Resolve`, `Interpolate`, `Synthesize`, and `Assemble` MAY remain in `recipes` or an experimental namespace. Their existence is not a problem; granting them the same v1 compatibility and correctness contract without precise semantics is.

## 6.3 Operation contract documentation template

Every stable operation MUST document:

1. intended input and output relationship;
2. whether new information may be inferred or created;
3. required and optional evidence;
4. deterministic invariants;
5. identity/order/cardinality guarantees;
6. batchability class and split/merge behavior;
7. failure and abstention semantics;
8. supported streaming modes;
9. stable prompt/operation version;
10. examples of appropriate and inappropriate use.

## 6.4 Control flow remains separate

Fallback, escalation, voting, sequencing, approval, checkpoints, and map/reduce are execution combinators. They compose operations or plans:

```go
pipeline := schemaflux.Sequence(
    schemaflux.ExtractOp[Invoice](),
    schemaflux.TransformOp[Invoice, LedgerEntry](),
)
```

Fluent operation builders MUST NOT grow an embedded branching language such as `.IfFailed().VoteWith(3).Otherwise()`. Ordinary Go, recipe descriptors, and plan combinators provide clearer control flow and testability.

# 7. Contract and result model

![Figure 3 - Contract stack](figures/contract_stack.png){width=88%}

Typed JSON is the bottom of the contract stack, not the top. An operation may request only the lower layers, but the runtime must report exactly which layers were enforced and delivered.

## 7.1 Contract levels

```go
type ContractLevel uint8

const (
    PromptOnly ContractLevel = iota
    JSONWellFormed
    SchemaConstrained
    SchemaAndInvariantChecked
    EvidenceChecked
    FullyGoverned // evidence + provenance + capability + data policy
)
```

A provider’s native structured output may improve the mechanism used to deliver a structural contract, but deterministic post-validation remains required. The plan records the minimum requested level, expected delivery mechanism, and allowed degradation. The envelope records the actual delivered level.

## 7.2 Result families

```go
type Result[T any] struct {
    Value    T
    Envelope ExecutionEnvelope
}

type JudgmentResult[T any] struct {
    Subject    T
    Verdict    Verdict
    Issues     []Issue
    Evidence   []EvidenceRef
    Confidence ModelConfidence // explicitly model-reported
}

type TransformationResult[T any] struct {
    Value       T
    Presence    FieldPresence
    Changes     []Change
    LostFields  []FieldPath // computed deterministically where possible
    Envelope    ExecutionEnvelope
}
```

Model confidence is never placed in the deterministic `Checks` section. It remains a model claim with model/prompt provenance. Callers must not use it as an uncalibrated correctness threshold unless they have a model- and task-specific calibration study.

## 7.3 Batch result

```go
type BatchResult[T any] struct {
    Items   []ItemResult[T]
    Summary BatchSummary
    Envelope ExecutionEnvelope
}

type ItemResult[T any] struct {
    ID        ItemID
    Index     int
    Value     T
    Err       error
    Status    ItemStatus
    Attempts  []AttemptRef
    Mode      ExecutionMode
    Evidence  []EvidenceRef
}
```

`Items` are returned in input order by default. Completion-order streaming is opt-in and still includes original index. Duplicate input values are safe because reconciliation uses invocation IDs, not equality.

## 7.4 Execution envelope

![Figure 4 - Execution envelope](figures/execution_envelope.png){width=83%}

```go
type ExecutionEnvelope struct {
    Identity          ExecutionIdentity
    Operation         ResolvedOperation
    RequestedPolicy   PolicySummary
    DeliveredContract DeliveredContract
    Attempts          []AttemptReport
    Checks            VerificationReport
    Usage             UsageTree
    Cost              CostTree
    Provenance        Provenance
    Diagnostics       DiagnosticSummary
    Timing            TimingBreakdown
}
```

The envelope is the basis for audit, replay, support, cost analysis, and semantic regression. Sensitive content is represented by digests or diagnostic references unless an explicitly authorized sink stores redacted bodies.

## 7.5 Field presence and optionality

Go zero values cannot distinguish missing from explicitly supplied values. SchemaFlux MUST support at least one first-class presence strategy and document the others:

```go
type Optional[T any] struct {
    Value   T
    Present bool
    Null    bool
}
```

Alternative strategies are pointer fields and a sidecar `FieldPresence` map keyed by JSON pointer. In strict extraction, requiredness is evaluated against presence, not zero value. A valid explicit `0`, `false`, or empty string must not be rejected as missing.

## 7.6 Exact decoding

Strict structural mode applies all of the following unless explicitly relaxed:

- reject unknown JSON properties;
- reject duplicate keys;
- reject trailing data after the top-level value;
- use exact number handling before typed conversion;
- enforce maximum nesting depth, string size, array length, and total decoded bytes;
- validate enum, range, pattern, and cardinality constraints;
- preserve field presence;
- report the smallest failing JSON pointer.

The runtime may perform deterministic, semantics-preserving cleanup such as removing an unambiguous Markdown fence before decoding, but every normalization is recorded. It must not silently invent missing fields or coerce lossy numbers.

## 7.7 Numeric and domain types

`float64` is unsuitable as a blanket recommendation for money, identifiers, and exact quantities. The schema layer SHOULD support:

- `json.Number` for deferred exact parsing;
- registered decimal types;
- `Money{Currency, Amount}` with currency-specific scale rules;
- integer bounds and overflow detection;
- string schemas for account numbers, postal codes, and identifiers that must preserve leading zeros;
- registered encoders for `time.Time`, `time.Duration`, UUIDs, and domain enums.

Strict conversion errors are `ErrSchemaViolation`, not silent zero values.

## 7.8 Type support matrix

Preflight classifies Go shapes:

| Support level | Examples | Behavior |
| --- | --- | --- |
| Full | structs, slices, arrays, pointers, strings, booleans, bounded numbers, enums, registered time/decimal types | Generate schema and exact decoder |
| Restricted | maps with string keys, recursive types with explicit depth, embedded fields, custom marshalers | Require documented limits or registered schema adapters |
| Opaque | raw text/blob wrappers intentionally passed as data | No field-level evidence unless adapter supplied |
| Rejected | cycles without bounds, non-string map keys without adapter, unconstrained `any`, functions, channels, unsafe pointers | Fail preflight before provider cost |

# 8. Execution planning and shapes

![Figure 5 - Execution shapes](figures/execution_shapes.png){width=95%}

The planner chooses a shape based on operation semantics, input cardinality/size, provider capabilities, budgets, failure policy, and caller constraints. Execution shape is part of the plan and envelope.

## 8.1 Atomic execution

Atomic execution sends one logical datum through one provider request. It provides the strongest failure isolation and clearest per-item provenance, but repeats prompt/schema overhead and becomes a serial stall when callers loop synchronously.

Atomic mode is appropriate when:

- the input is uniquely high-risk or very large;
- item-specific steering or data policy differs;
- batch compliance is historically poor;
- the operation is inherently global for that single datum;
- a failed item has been isolated from an MDSP batch;
- deterministic preflight predicts that batching would exceed context or output limits.

Atomic mode is not the default for a homogeneous slice merely because the public singular API is easy to call in a loop.

## 8.2 MDSP: multiple data, single prompt

MDSP applies one prompt/schema contract to multiple independent items. SchemaFlux uses a protocol envelope with stable IDs:

```json
{
  "items": [
    {"id": "i-000001", "data": "..."},
    {"id": "i-000002", "data": "..."}
  ]
}
```

The model returns minimal results:

```json
{
  "results": [
    {"id": "i-000001", "value": {"category": "billing"}},
    {"id": "i-000002", "value": {"category": "technical"}}
  ]
}
```

The deterministic batch protocol checks exact ID coverage, duplicates, unknown IDs, item schema, cardinality, and per-item invariants. It restores caller order and never trusts output position.

## 8.3 SDMP: single data, multiple prompts

SDMP is a staged plan over one datum: extract, critique, verify, adjudicate, or repair. Stages are justified only when they add a distinct contract that cannot be achieved deterministically.

The planner minimizes repeated data transfer by:

- passing structured intermediate outputs rather than the entire source when sufficient;
- reusing deterministic preprocessing and schema artifacts;
- using provider prompt-prefix caching when supported and allowed by data policy;
- executing independent checks concurrently under the same logical budget;
- skipping model review when deterministic checks already establish the required contract.

Each stage has its own operation ID and parent lineage. The final envelope retains the complete stage tree.

## 8.4 Global and hierarchical operations

Ranking, clustering, deduplication, choosing the best item, anomaly detection, and cross-document synthesis depend on relationships across the set. Naive chunking changes semantics.

Each such operation declares an algebra. Examples:

- **Ranking:** score or rank within chunks; retain a candidate frontier; globally rerank candidates; validate resulting IDs and pairwise constraints.
- **Clustering:** derive deterministic or model-produced features; cluster in Go; optionally ask the model to label clusters; validate exact item coverage.
- **Deduplication:** generate candidate pairs using hashes/embeddings; ask the model only about likely pairs; compute connected components deterministically.
- **Global synthesis:** create chunk summaries with evidence; synthesize summaries; verify final claims against accumulated evidence references.

A generic `MapReduce` helper may execute the declared algebra but cannot invent correct merge semantics.

## 8.5 Plan representation

```go
type ExecutionPlan[Out any] struct {
    ID              PlanID
    Operation       ResolvedOperation
    Policy          ResolvedPolicy
    Nodes           []PlanNode
    Expected        PlanEstimate
    MinimumContract ContractLevel
    Explanation     []PlanReason
}

type PlanNode struct {
    ID        NodeID
    Kind      NodeKind // provider, deterministic, merge, review
    DependsOn []NodeID
    Route     Route
    ItemIDs   []ItemID
    Budget    BudgetSlice
}
```

Plans are immutable, serializable without sensitive content, and inspectable. A plan is invalidated if the capability snapshot or locked data policy changes before execution; the executor either replans with approval or returns `ErrPlanStale`.

## 8.6 Preflight explanation

Example:

```text
Operation: classify/v2
Input: 240 items, 58,300 estimated input tokens
Mode: adaptive MDSP
Chunks: 12 initial chunks; 20 item cap; 8,000 token cap
Parallelism: 4 (provider RPM and tenant token budget)
Recovery: smaller MDSP -> atomic -> review; no cross-provider fallback
Maximum provider calls: 28
Estimated cost: USD 0.42-0.68 (pricing table 2026-08-01)
Minimum contract: SchemaAndInvariantChecked
Data policy: confidential/us-only/no-retention
```

The estimate is labeled exact or estimated per field. `Preflight` never performs generation.

# 9. MDSP batching, loop handling, and scheduling

## 9.1 Plural APIs

The public API makes collection semantics explicit:

```go
batch, err := schemaflux.ExtractMany[Person](
    ctx,
    client,
    inputs,
    schemaflux.AdaptiveBatching(),
    schemaflux.Parallelism(8),
    schemaflux.RetryThenCollect(),
)
```

Fluent equivalent:

```go
batch, err := schemaflux.
    ExtractingMany[Person](inputs).
    Using(client).
    Adaptive().
    MaxChunkTokens(24_000).
    Parallelism(8).
    FailurePolicy(schemaflux.RetryThenCollect).
    Run(ctx)
```

Singular functions do not silently reinterpret slices. Plural APIs signal partial success, item identity, ordering, batching, and scheduler policy.

## 9.2 Token-aware packing

The chunker accounts for:

```text
fixed system policy
+ operation prompt
+ output schema
+ batch protocol overhead
+ serialized item inputs
+ expected output per item
+ provider-specific tokenization uncertainty
+ safety margin
```

The effective chunk is bounded by the earliest of item-count limit, input-token limit, output reserve, context limit, per-call cost, and provider payload bytes. Oversized individual items are routed atomically, summarized through an operation-specific hierarchy, or rejected by preflight; they are never silently truncated.

## 9.3 Adaptive batching policy

An adaptive policy learns the largest stable batch for an operation/model/schema/capability key. A recommended additive-increase/multiplicative-decrease strategy is:

1. Start from the minimum of configured initial items and token-aware packing.
2. After a configurable window of fully compliant chunks, increase item target by 25% or a bounded additive step.
3. On output truncation, missing IDs, duplicate IDs, malformed protocol, or repair above threshold, halve the target.
4. On provider rate pressure, reduce concurrent chunks independently of semantic chunk size.
5. Never exceed declared operation maximum, context reserve, or cost budget.
6. Do not share learned statistics across tenants when doing so would disclose workload characteristics.
7. Record the decision reason in the plan/execution ledger.

Adaptive history is advisory. Deterministic per-request limits always win.

## 9.4 Progressive recovery

![Figure 6 - Batch recovery cascade](figures/batch_recovery.png){width=88%}

The default recovery cascade is:

1. execute preferred MDSP chunks;
2. accept and retain individually valid results;
3. isolate only missing or invalid item IDs;
4. retry unresolved items in smaller MDSP chunks;
5. run repeated failures atomically;
6. escalate model or provider only when the minimum contract and data policy remain satisfied;
7. create review packets or terminal item failures when budget is exhausted.

Valid items are never replayed solely because siblings failed unless the operation’s global algebra requires recomputation.

## 9.5 Failure policies

```go
type FailurePolicy uint8

const (
    FailFast FailurePolicy = iota
    CollectFailures
    RetryFailedItems
    RetryThenCollect
    RequireAll
)
```

| Policy | Scheduling behavior | Return behavior |
| --- | --- | --- |
| `FailFast` | Cancel queued/in-flight siblings after first terminal item failure where safe | Return completed verified items in detailed result plus aggregate error |
| `CollectFailures` | Run all planned work; no semantic retries beyond transport defaults | Return item errors; aggregate error optional |
| `RetryFailedItems` | Selectively recover unresolved items; stop after item budget | Return partial result and typed failures |
| `RetryThenCollect` | Default for long batches; selective recovery then partial return | Aggregate error only when caller-configured threshold exceeded |
| `RequireAll` | Attempt all allowed recovery; batch considered failed if any terminal item remains | Detailed result still preserves successful items |

## 9.6 Bounded scheduler

The scheduler enforces multiple dimensions, not only a worker count:

```go
type SchedulerLimits struct {
    MaxConcurrentCalls   int
    MaxQueuedNodes       int
    MaxInFlightTokens    int64
    MaxInFlightCost      Money
    PerProvider          map[ProviderID]ProviderLimits
    PerTenant            TenantLimitPolicy
}
```

Admission computes a weight from estimated input/output tokens, expected cost, provider quotas, and priority. Queues are bounded. When a queue is full or a request cannot meet its deadline, the scheduler returns `ErrAdmissionRejected` rather than allocating unbounded goroutines.

## 9.7 Fairness and noisy-neighbor control

Multi-tenant schedulers SHOULD use weighted fair queuing with per-tenant concurrency, token, and cost buckets. Priorities are bounded classes, not arbitrary integers. A low-priority batch cannot permanently starve; an urgent request cannot bypass locked provider or data-policy limits.

Required metadata is a bounded tenant key or workload class, not end-user PII. Fairness decisions appear in queue timing and plan/execution reasons.

## 9.8 Loop fusion

An optional batch group lets callers retain natural Go loops while deferring execution:

```go
group := schemaflux.NewBatchGroup(client)
handles := make([]schemaflux.Handle[Person], 0, len(inputs))

for _, input := range inputs {
    h := schemaflux.Add(group,
        schemaflux.Extracting[Person](input).
            Strict(),
    )
    handles = append(handles, h)
}

if err := group.Run(ctx); err != nil {
    // Handles still expose item-level outcomes.
}
```

Fusion is legal only when operation ID/version, output schema hash, route policy, steering, contract level, data policy, and compatible budget settings match. Otherwise the group creates separate plan partitions. Loop fusion is optimization; it must not change result semantics relative to executing each builder under the same policy.

## 9.9 Caller-facing streaming and backpressure

For very large inputs, an iterator avoids retaining all results:

```go
iter, err := schemaflux.ExtractStream[Person](ctx, client, source, opts...)
for iter.Next() {
    item := iter.Item() // already schema/protocol checked
    consume(item)
}
if err := iter.Err(); err != nil {
    return err
}
```

The iterator owns a bounded internal buffer. Stopping iteration cancels remaining work unless detached explicitly. The contract defines whether results arrive in input or completion order. Raw token streaming is separate from validated-item streaming.

# 10. Failure model, retries, repair, and escalation

![Figure 7 - Unified execution state machine](figures/execution_state_machine.png){width=98%}

All provider calls are transitions inside one logical request state machine. Individual operation implementations do not create their own unaccounted retry loops.

## 10.1 Error taxonomy

```go
type ErrorKind uint16

const (
    KindConfiguration ErrorKind = iota + 1
    KindAuthentication
    KindPermission
    KindInvalidRequest
    KindRateLimited
    KindProviderUnavailable
    KindTimeout
    KindCanceled
    KindContextTooLarge
    KindOutputTruncated
    KindMalformedOutput
    KindSchemaViolation
    KindBatchProtocolViolation
    KindInvariantViolation
    KindEvidenceViolation
    KindUnsupportedCapability
    KindPolicyViolation
    KindBudgetExceeded
    KindCircuitOpen
    KindAdmissionRejected
    KindRepairExhausted
    KindReviewRequired
    KindShutdown
)

type Error struct {
    Kind       ErrorKind
    Op         OperationID
    Provider   string
    Model      string
    RequestID  string
    AttemptID  string
    ItemIDs    []ItemID
    RetryAfter time.Duration
    Ambiguous  bool
    Cause      error
}
```

Exported sentinel errors support `errors.Is`; `*Error` supports `errors.As`. `Error()` contains sanitized metadata only. Raw provider response content is never included.

## 10.2 Failure classification and disposition

| Failure class | Examples | Default disposition | Replay scope |
| --- | --- | --- | --- |
| Configuration/policy | missing provider, prohibited region, insecure endpoint | Terminal before provider call | None |
| Authentication/permission | 401/403, invalid credential | Terminal; breaker may suppress repeated probes | None |
| Rate limit | 429, quota response | Honor `Retry-After`, jitter, scheduler rate adaptation | Same attempt payload if budget/deadline permit |
| Transient transport | connection reset, 502/503 | Retry through scheduler and breaker | Same node |
| Timeout | provider deadline | Retry only if remaining logical deadline and idempotency policy permit | Same node; mark ambiguity |
| Context too large | provider or preflight context rejection | Replan with smaller chunks or fail oversized item | Affected node/items |
| Truncation | finish reason or incomplete body | Reduce output/chunk size or regenerate | Affected chunk |
| Malformed syntax | invalid JSON, fence noise | Deterministic normalization if unambiguous; otherwise repair/regenerate | Affected chunk/item |
| Batch protocol | missing/duplicate/unknown IDs | Retain valid items; split unresolved IDs | Unresolved items only |
| Schema violation | wrong types, missing required fields | Targeted structural repair or fresh regeneration | Affected items |
| Invariant violation | non-member selection, lost field, invalid permutation | Fresh regeneration from source; avoid editing bad answer | Affected items/global set |
| Evidence violation | claim lacks supporting span/path | Re-extract with evidence requirement or review | Affected claims/items |
| Unsupported capability | provider cannot deliver minimum contract | Choose eligible route or terminal error | Replan |
| Budget/cancel/shutdown | logical ceiling or lifecycle termination | Terminal; return validated partials | None |
| Review required | contradiction, high-risk uncertainty, ensemble disagreement | Produce review packet | None unless human resumes |

## 10.3 Retry versus repair versus regeneration

- **Retry** repeats the same logical provider request because the transport/provider failed before an acceptable response was obtained.
- **Repair** asks for a constrained correction when the output is close enough that the invalid response is useful and safe to include.
- **Regeneration** starts from trusted source data and the original contract; it does not instruct the model to preserve the previous answer.
- **Split** replans a batch protocol failure with fewer items.
- **Fallback** changes provider/model while preserving minimum capabilities and policy.
- **Escalation** selects a stronger or more expensive route because lower tiers exhausted semantic recovery.
- **Review** stops automated recovery and returns evidence and attempt history for a human or external system.

These are separate attempt kinds in the ledger and consume the same logical budget.

## 10.4 Repair safety

Repair strategy is selected by failure class:

- Syntax-only damage may include the prior response after size limits and prompt-injection-safe delimiting.
- Missing fields requests only the missing fields when the rest of the answer is independently valid and field dependencies permit patching.
- Unknown fields are rejected in exact mode; optional deterministic removal is allowed only by explicit policy and is recorded.
- Invariant and evidence failures regenerate from original sources because editing a bad answer can reinforce fabricated content.
- Batch omissions retry only unresolved IDs.

After repair, the original schema, invariant, evidence, cardinality, and preservation contracts are reapplied. The runtime compares previously valid fields and flags unrelated loss or mutation. “Valid JSON” alone never marks repair success.

## 10.5 Hierarchical attempt budget

```go
type Budget struct {
    MaxProviderCalls int
    MaxInputTokens   int64
    MaxOutputTokens  int64
    MaxTotalTokens   int64
    MaxCost          Money
    MaxElapsed       time.Duration
    MaxChunkAttempts int
    MaxItemAttempts  int
    MaxEscalations   int
}
```

Every attempt reserves budget atomically before scheduling. Unused estimates are reconciled with actual usage. Children cannot exceed parent ceilings. The plan reports the theoretical maximum fan-out; the envelope reports actual consumption by stage, chunk, and item.

## 10.6 Backoff, breaker, and idempotency

Transient retries use decorrelated jitter and honor provider `Retry-After` within the logical deadline. Circuit breakers are keyed by provider endpoint and optionally model. Authentication and deterministic invalid requests trip configuration health but are not blindly retried.

Logical request IDs remain stable across all attempts. Each provider attempt receives a unique attempt ID. Provider idempotency keys are used where supported. Ambiguous timeouts are labeled so downstream applications can avoid triggering a side effect twice. SchemaFlux itself performs no application side effects.

## 10.7 Cancellation semantics

On cancellation:

1. stop scheduling new plan nodes;
2. remove queued nodes;
3. cancel in-flight provider calls and backoff waits;
4. stop deterministic workers at context checkpoints;
5. finalize already verified items;
6. mark unresolved items canceled;
7. return `context.Canceled`/`DeadlineExceeded` through the typed error contract plus a detailed partial result when requested.

No internal goroutine, stream reader, queue wait, telemetry flush, or store write may block indefinitely after cancellation.

# 11. Trust, evidence, and provenance

![Figure 8 - Trust boundaries](figures/trust_boundaries.png){width=94%}

Typed structured output constrains form; it does not neutralize instructions embedded in data or prove factual grounding. SchemaFlux needs an explicit trust model.

## 11.1 Prompt segments and trust labels

```go
type TrustLevel uint8
const (
    TrustedPolicy TrustLevel = iota
    TrustedDeveloperInstruction
    UntrustedApplicationData
    UntrustedRetrievedData
    UntrustedModelOutput
)

type PromptSegment struct {
    Role    SegmentRole
    Trust   TrustLevel
    Name    string
    Content any
}
```

The renderer preserves role boundaries and uses structured delimiters. Untrusted content is never interpolated into system policy text. Model output becomes `UntrustedModelOutput` and must cross the contract layer before application consumption. This reduces, but does not claim to eliminate, prompt injection.

## 11.2 Evidence contract

```go
type EvidenceRef struct {
    SourceID    string
    JSONPointer string
    StartByte   int
    EndByte     int
    SourceDigest string
}

type ClaimProvenance struct {
    FieldPath string
    Evidence  []EvidenceRef
    Inferred  bool
    Method    string
}
```

Evidence can reference a text span, JSON pointer, table cell, or registered source location. The runtime validates that references are in bounds and correspond to the correct source digest. It does not automatically prove that the cited text logically entails the claim; domain evidence checkers may add stronger validation.

Modes:

- `NoEvidence`: structural output only.
- `EvidenceForMaterialFields`: operation declares which paths require evidence.
- `EvidenceForAllModelFields`: every model-derived field requires evidence or an explicit inference marker.
- `NoInference`: unsupported fields remain absent rather than guessed.

## 11.3 Provenance through pipelines

Every result receives an immutable result ID and parent links. Provenance includes:

- operation name/version and prompt template version;
- requested and resolved provider/model/revision;
- input and schema digests;
- deterministic normalizers and validators applied;
- parent result IDs and source evidence references;
- item-specific recovery path;
- cache usage and cache key version;
- library and provider adapter versions.

When a later summary consumes an extracted object, the final claim can be traced back through the extraction and to the original evidence. If lineage is lost, the delivered contract cannot be `FullyGoverned`.

## 11.4 Model-reported metadata

Confidence, rationale, inferred mappings, “lost fields,” and trust scores produced by the model are model claims. Deterministic facts such as field presence, input/output diff, membership, ordering, token usage, and cost are computed by Go.

The result model separates:

```text
Value / ModelClaims / VerificationChecks / RuntimeFacts
```

Documentation explicitly warns that model confidence is not calibrated across prompts, providers, or model revisions unless the caller supplies a calibration profile.

## 11.5 Human review

Automated recovery stops when:

- evidence is contradictory or insufficient for a required claim;
- high-risk policy requires human approval;
- invariant failure persists after budgeted regeneration;
- eligible providers disagree materially;
- the requested contract cannot be preserved by fallback;
- source quality prevents reliable extraction.

```go
type ReviewPacket[T any] struct {
    Candidate      T
    InputRefs      []SourceRef
    Evidence       []EvidenceRef
    FailedChecks   []CheckFailure
    Attempts       []AttemptReport
    SuggestedAction string
}
```

`ErrReviewRequired` is a successful safety outcome, not an invitation to loop indefinitely.

## 11.6 Ensemble and adjudication

Consensus is a policy, not a correctness guarantee. Ensemble reconciliation may use exact agreement, field-level voting, deterministic validation then selection, evidence-weighted comparison, an adjudicator model, or abstention. Correlated models can share the same hallucination; therefore evidence and invariants remain mandatory where requested.

# 12. Schema identity, evolution, and reproducibility

## 12.1 Schema descriptors

```go
type SchemaDescriptor struct {
    Name       string
    Version    string
    Hash       string
    Dialect    string
    TypePolicy TypePolicyVersion
}
```

Schema identity is included in prompts, caches, fixtures, envelopes, and semantic baselines. Anonymous types may receive a deterministic content-derived name but are discouraged for persisted workflows.

## 12.2 Evolution rules

Recommended compatibility rules:

- adding an optional field is backward compatible for decoding but may change prompt behavior and cache identity;
- adding a required field is a new schema contract version;
- changing a field type, enum, precision, or evidence requirement is a new contract version;
- renaming a JSON field is breaking unless an explicit migration/alias is supplied;
- changing strictness defaults is a semantic breaking change;
- old stored results retain the schema version/hash that produced them;
- migrations are deterministic functions with their own version and provenance.

## 12.3 Model and prompt reproducibility

Every execution records:

```text
requested model or tier
resolved provider/model identifier
provider model revision if available
operation version
prompt template version and digest
schema version and hash
provider capability snapshot version
library and adapter versions
seed/temperature when supported
```

`Tier(Smart)` is explicitly floating. `Model("provider/model-version")` requests a pin but the provider may still not expose immutable weights; the envelope reports that limitation.

## 12.4 Prompt compatibility policy

Prompt templates are behavioral artifacts. Changes fall into:

- **patch-compatible:** spelling, non-semantic formatting, or provider adaptation proven equivalent by golden/replay tests;
- **minor behavior change:** opt-in improvement or new operation version with migration notes;
- **breaking semantic change:** altered permitted inference, required evidence, defaults, result interpretation, or repair behavior.

Release notes list prompt/operation behavior changes even when Go signatures are unchanged.

# 13. Provider capability, routing, and fallback

## 13.1 Provider interface

```go
type Provider interface {
    ID() ProviderID
    Capabilities(ctx context.Context) (Capabilities, error)
    Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error)
}

type Capabilities struct {
    NativeJSONSchema      bool
    JSONMode              bool
    SupportedKeywords     map[string]bool
    Streaming             StreamingCapabilities
    ToolCalling           bool
    MultiTurn             bool
    PromptCaching         bool
    IdempotencyKeys       bool
    Seed                   bool
    UsageReporting        UsageQuality
    ModelRevisionReporting bool
    Regions               []string
    RetentionModes        []RetentionMode
    MaxContextTokens      int
    MaxOutputTokens       int
}
```

Provider modules own endpoint defaults, authentication, response-format translation, error classification, usage normalization, and capability declarations. Core owns planning and policy.

## 13.2 Capability negotiation

The planner intersects:

1. operation minimum requirements;
2. invocation requirements;
3. tenant/data policy;
4. provider/model capability snapshot;
5. budget and deadline;
6. availability and breaker state.

An eligible route must satisfy all locked constraints. Schema keyword stripping is permitted only when the remaining mechanism plus deterministic validation still delivers the requested contract. The plan must disclose the adaptation.

## 13.3 Data policy

```go
type DataPolicy struct {
    Classification     DataClassification
    AllowedProviders   []ProviderID
    AllowedRegions     []string
    AllowRetention     bool
    AllowTrainingUse   bool
    AllowContentLogging bool
    AllowResultCache   bool
    MinimumContract    ContractLevel
}
```

Data policy is locked at the client or tenant boundary and can be made stricter by an invocation. A fallback to a public provider is prohibited when the original private route fails unless that route is already eligible under the same policy.

## 13.4 Fallback and degradation

Fallback may change latency, price, model behavior, schema enforcement, and data terms. Therefore:

- the fallback route must meet minimum capabilities and data policy;
- the requested contract cannot be silently downgraded;
- explicit policy may allow a named degradation, such as native schema -> JSON mode plus deterministic exact validation;
- the envelope records the requested mechanism, delivered mechanism, and reason;
- cost and model changes consume the same logical budget;
- a fallback failure is classified independently, not hidden behind the original error.

## 13.5 Provider conformance status

Support labels:

- **Integrated:** adapter compiles and basic transport tests pass.
- **Conformant:** mandatory offline/replay conformance suite passes for declared capabilities.
- **Live-verified:** gated live tests pass against named model versions within a recent verification window.
- **Production-supported:** conformance, live verification, security review, support policy, and operational runbook are complete.

Marketing and documentation MUST not collapse these states into a single “supported” claim.

# 14. Security, privacy, and caching

## 14.1 Threat model

Primary assets include API credentials, prompts, source data, model outputs, schemas, tenant identities, execution diagnostics, pricing records, and provider routes. Threat actors include malicious input authors, compromised providers/endpoints, accidental operators, other tenants, dependency attackers, and untrusted model output.

| Threat | Primary controls | Verification |
| --- | --- | --- |
| Credential leakage | typed secret fields, header scrubbing, no `String()` exposure, secret scanners | unit tests and log-capture assertions |
| Prompt/content leakage | metadata-only default, data-policy gate, bounded redacted sinks | privacy integration tests |
| Prompt injection | trust-labeled segments, strict role rendering, evidence/invariants, no model side effects | adversarial semantic corpus |
| SSRF/custom endpoint abuse | scheme/host allowlists, private-address policy, caller transport control | security tests with malicious URLs |
| JSON/schema resource exhaustion | byte/depth/item limits, streaming decoder, preflight caps | fuzz and load tests |
| Cross-tenant contamination | instance/tenant scoping, stable item IDs, cache partitioning, fairness | race, isolation, and chaos tests |
| Malicious model output | treat as untrusted, exact decoding, URL/command policies at application boundary | property tests and review gates |
| Dependency compromise | minimal core dependencies, SBOM, govulncheck, pinned releases | CI and release attestations |

## 14.2 Logging policy

```go
type ContentLoggingPolicy uint8
const (
    LogNoContent ContentLoggingPolicy = iota
    LogMetadataOnly
    LogRedactedContent
    LogFullContent // explicit unsafe opt-in, policy-gated
)
```

Production default is `LogMetadataOnly`. Debug mode changes verbosity, not content policy. Authorization headers, keys, cookies, raw request/response bodies, and schema examples containing sensitive values are always scrubbed. Captured buffers are bounded and have retention limits. Diagnostic files use restrictive permissions.

## 14.3 Diagnostic evidence

When raw provider data is required for support, it is emitted to a caller-provided diagnostic sink that applies authorization, redaction, size limits, retention, and encryption. The ordinary error contains a diagnostic reference and content digest, never the body.

## 14.4 Caching

Cache categories have different risk:

- schema generation and token-estimation caches are safe when keyed by version;
- provider prompt-prefix caching is allowed only when provider retention policy and data policy permit;
- exact result caching is opt-in and partitioned by tenant/data policy;
- semantic near-duplicate caching is off by default and inappropriate for high-stakes differences without domain validation.

An exact result cache key includes operation/prompt/schema versions, normalized options, input digest, provider and resolved model, temperature/seed, delivered contract requirements, data-policy partition, and relevant library/decoder versions. Cache hits appear in provenance and cost accounting.

## 14.5 Retention and deletion

Caches, diagnostics, pricing stores, and replay fixtures each declare retention and deletion behavior. User content is never stored merely because cost accounting is enabled. Pricing records should prefer token counts, model IDs, request IDs, and cost—not prompts. Deletion hooks allow tenant-scoped removal when the backing adapter supports it.

# 15. Observability, cost, and operational lifecycle

## 15.1 Host-owned instrumentation

Core accepts small observer interfaces or no-op implementations. The OpenTelemetry adapter creates spans/metrics using the host’s provider but does not initialize global SDKs, exporters, endpoints, or sampling.

Recommended trace structure:

```text
schemaflux.run
  schemaflux.plan
  schemaflux.queue
  schemaflux.provider.attempt
  schemaflux.decode
  schemaflux.verify
  schemaflux.recovery
  schemaflux.merge
```

Item IDs and request IDs may be attributes only when cardinality and privacy policies permit. High-cardinality details belong in logs or the envelope, not metric labels.

## 15.2 Metrics catalog

| Metric | Type | Primary use |
| --- | --- | --- |
| `schemaflux_requests_total` | counter | logical request outcomes by operation/status |
| `schemaflux_request_duration_seconds` | histogram | end-to-end latency |
| `schemaflux_plan_nodes` | histogram | fan-out and complexity |
| `schemaflux_provider_attempts_total` | counter | attempts by provider/kind/status |
| `schemaflux_provider_duration_seconds` | histogram | provider latency |
| `schemaflux_queue_duration_seconds` | histogram | scheduler pressure |
| `schemaflux_provider_inflight` | gauge | bulkhead utilization |
| `schemaflux_circuit_state` | gauge | breaker health |
| `schemaflux_items_total` | counter | item outcomes |
| `schemaflux_batch_size` | histogram | MDSP behavior |
| `schemaflux_batch_compliance_ratio` | histogram | valid/requested items per chunk |
| `schemaflux_repairs_total` | counter | repair/regeneration by failure class |
| `schemaflux_atomic_fallback_total` | counter | items degraded from MDSP |
| `schemaflux_tokens_total` | counter | reported/estimated token use |
| `schemaflux_cost_total` | counter | cost by currency/pricing quality |
| `schemaflux_budget_exceeded_total` | counter | budget dimension exhausted |
| `schemaflux_review_required_total` | counter | automation abstentions |

## 15.3 Cost tree

Cost is hierarchical:

```text
logical request
  stage
    chunk
      provider attempt
        item attribution
```

Provider-reported usage is preferred. Estimated usage is marked. Pricing records include source, version, effective date, currency, and quality (`Exact`, `Estimated`, `Unknown`, `Free`). Zero cost never means unknown. Historical cost is not recomputed using current price tables without preserving both versions.

## 15.4 Recommended operational SLOs

Initial targets, to be baselined and adjusted:

| SLO | Recommended target | Measurement |
| --- | --- | --- |
| Runtime panics attributable to valid API use | 0 | panic recovery telemetry and tests |
| Goroutine leaks after canceled/completed requests | 0 | leak tests across all blocking boundaries |
| Client isolation failures | 0 | parallel multi-client provider/policy tests |
| Validated item completeness after allowed recovery on conformance corpus | >= 99.5% | per-item semantic/conformance suite |
| Unknown/duplicate batch IDs accepted | 0 | protocol property tests |
| Secrets or authorization headers in logs | 0 | automated log redaction tests |
| Provider calls exceeding declared logical budget | 0 | budget property tests |
| Stable provider conformance pass rate | 100% for declared capabilities | release gate |
| Execution envelope availability for detailed calls | 100% including failures | integration tests |
| Cancellation cleanup | all owned work stopped within bounded grace | timed leak/chaos tests |

## 15.5 Operational runbooks

Every production-supported provider and core incident class has a runbook with signals, immediate mitigation, safe fallback, data to capture, and recovery criteria. Minimum runbooks cover:

- 429/rate-limit surge;
- provider latency or outage;
- malformed/truncated output spike;
- batch omission or repair-rate spike;
- cost anomaly;
- model alias/revision change;
- capability/schema enforcement regression;
- circuit stuck open/closed;
- cache or pricing-store failure;
- telemetry exporter failure;
- suspected content/credential leak;
- deprecated model or provider endpoint.

Telemetry failure must not block core execution unless policy explicitly requires audit durability. Content or credential leak suspicion defaults to fail-closed for affected diagnostics and routes.

# 16. Testing and verification strategy

## 16.1 Test pyramid

SchemaFlux requires more than unit tests because its correctness boundary spans deterministic code and changing probabilistic systems.

| Layer | Purpose | Network |
| --- | --- | --- |
| Unit | option resolution, schemas, decoders, budgets, errors, contracts | No |
| Property | membership, permutation, cardinality, ordering, budget monotonicity | No |
| Race/leak | client isolation, scheduler, cancellation, close | No |
| Fuzz | JSON/schema/repair/normalization hostile inputs | No |
| Replay | full executor with captured provider fixtures | No |
| Provider conformance | common transport/capability/error contract | Usually no; live subset optional |
| Semantic regression | quality, hallucination, evidence, compliance, cost | Yes for release candidates |
| Load/performance | scheduler, queues, memory, large batches | Fake/replay plus selected live |
| Chaos | timeouts, partial streams, malformed bodies, storage/exporter failures | Fake/replay |
| Security | injection, SSRF, redaction, cross-tenant isolation | Fake/local endpoints |

## 16.2 Required deterministic gates

Every pull request runs:

```bash
go test ./...
go test -race ./...
go test -shuffle=on -count=10 ./...
go vet ./...
staticcheck ./...
govulncheck ./...
```

Additional gates compile examples, verify generated schema/prompt artifacts are fresh, run fuzz smoke corpora, check module/license policy, generate an SBOM, and verify a clean working tree. CI covers supported Go versions, Linux, macOS, Windows, amd64, arm64, and any explicitly supported CGO/SQLite combinations.

## 16.3 Fuzz targets

Mandatory fuzz targets include:

- recursive and deeply nested types;
- embedded fields and tags;
- maps, pointers, nils, custom marshalers;
- malformed UTF-8 and Unicode edge cases;
- huge numbers, exponents, duplicate keys, trailing data;
- JSON bombs and allocation/depth limits;
- schema keyword adaptation;
- Markdown fence and JSON extraction normalizers;
- batch IDs, duplicates, omissions, and adversarial ordering;
- repair response parsing;
- redaction and diagnostic scrubbing.

## 16.4 Replay fixtures

A sanitized fixture stores provider/model, operation/prompt/schema versions, request body, relevant response headers/body, usage, expected decoded result, expected failure classification, and invariant outcome. Replay uses the real provider adapter parser and executor path. Fixtures never contain production secrets or uncontrolled PII.

## 16.5 Provider conformance suite

Every provider adapter is tested for:

- auth and permission classification;
- connection, timeout, cancellation, and ambiguous completion;
- 429 with valid/invalid/missing `Retry-After`;
- 500/502/503 behavior;
- empty, malformed, truncated, and refused output;
- supported/unsupported schema keywords;
- exact request ID and usage extraction;
- reasoning/cached token normalization when available;
- Unicode, large payload, and streaming termination;
- caller-supplied endpoint and HTTP client;
- header and credential redaction;
- capability declaration consistency.

## 16.6 Semantic regression suite

Release-candidate semantic suites use pinned operation versions and as-pinned-as-available models. They measure:

- extraction field accuracy and unsupported-field hallucination;
- missing-field and evidence-reference validity;
- classification accuracy and abstention;
- choose/filter membership and precision/recall;
- ranking pairwise agreement and exact ID coverage;
- repair success and regression rate;
- valid-item ratio by batch size;
- latency, token use, and cost per accepted item;
- prompt-injection resistance on adversarial inputs.

Results are statistical, with repeated trials and confidence intervals. A single exact-output assertion is not treated as a stable live-model test unless the provider offers deterministic constrained output and the corpus proves stability.

## 16.7 Performance and load tests

Load tests verify bounded memory and goroutine counts under:

- large item streams;
- provider slowdown and 429 bursts;
- mixed tenants and priorities;
- cancellation storms;
- large schemas and near-context-limit chunks;
- partial MDSP failures causing atomic fallback;
- disabled/failing telemetry and stores.

Key outcome metrics are cost and latency per valid item, not raw calls per second.

# 17. Release engineering and compatibility

## 17.1 Stability tiers

Public operations and packages are labeled:

- **Stable:** SemVer compatibility, operation/prompt version policy, conformance and semantic baselines.
- **Beta:** API largely formed; behavior may change with migration notes.
- **Experimental:** no compatibility promise; isolated package or explicit marker.

Moving an operation to stable requires completed semantic contract documentation, batch algebra, provider matrix, deterministic tests, semantic corpus, and runbook.

## 17.2 Release process

A release includes:

- tagged version and signed/checksummed artifacts;
- generated changelog;
- Go API changes and semantic behavior changes;
- operation/prompt/schema version changes;
- provider capability/live-verification matrix;
- supported Go/OS/architecture matrix;
- known degradations and migration steps;
- SBOM and vulnerability scan result;
- release-candidate semantic benchmark comparison.

## 17.3 Deprecation policy

Deprecated stable APIs remain functional for at least one documented compatibility window. Deprecation warnings point to mechanical replacements. Global/default-client APIs migrate to `schemaflux/quick` or compatibility adapters. Removal requires a major version unless the API was explicitly experimental.

## 17.4 Repository baseline

The repository SHOULD include:

- `LICENSE`, `SECURITY.md`, `CONTRIBUTING.md`, code of conduct where appropriate;
- support and version policy;
- architecture overview and ADR directory;
- issue templates requesting sanitized execution-envelope metadata;
- provider support matrix generated from conformance results;
- examples pinned to released versions;
- separate guides for quick-start convenience and production configuration;
- migration guide from current package-global behavior.

# 18. Migration architecture and delivery gates

![Figure 9 - Delivery gates](figures/delivery_gates.png){width=98%}

Migration should be incremental and behavior-preserving where possible. The new kernel can be introduced under current public operations before removing compatibility state.

## 18.1 Gate 0 - Baseline and compatibility harness

Deliverables:

- freeze and inventory current exported operations and defaults;
- assign gap and operation IDs;
- capture golden prompts and sanitized replay fixtures for representative operations;
- add race tests demonstrating current global-state interference;
- define standard/fluent equivalence tests;
- publish deprecation and stability labels;
- establish CI and release-candidate lanes.

Exit criterion: current behavior can be compared mechanically to the new runtime, and all known deviations are intentional.

## 18.2 Gate 1 - Safe core

Deliverables:

- instance-scoped immutable client;
- provider-neutral `Op`, `Run`, `RunResult`, `Error`, `Result`, and `Budget`;
- context-required execution;
- typed failure taxonomy and attempt ledger;
- host-injected observer and HTTP ownership;
- idempotent shutdown;
- compatibility adapters for existing public APIs.

Exit criterion: two clients with different providers and policies run concurrently without interference; every migrated operation uses one kernel.

## 18.3 Gate 2 - Execution planner and scheduler

Deliverables:

- deterministic preflight and explainable plan;
- singular/plural APIs;
- stable MDSP protocol with item IDs;
- token-aware/adaptive chunking;
- bounded fair scheduler, rate limits, breakers, backpressure;
- partial results and progressive recovery;
- operation batchability declarations.

Exit criterion: large homogeneous loops no longer require serial `Run`, and batch failures isolate unresolved items without replaying valid siblings.

## 18.4 Gate 3 - Trust and correctness

Deliverables:

- contract stack and delivered-level reporting;
- exact decoder, presence semantics, numeric precision, type support matrix;
- deterministic invariants and domain extension points;
- evidence references and pipeline provenance;
- prompt trust segments, data-policy routing, secure diagnostics;
- human-review outcomes.

Exit criterion: typed output cannot be presented as stronger than the checks actually enforced, and high-risk uncertainty can safely abstain.

## 18.5 Gate 4 - Verified provider ecosystem

Deliverables:

- provider capability model and routing negotiation;
- conformance suite and generated support matrix;
- fallback contract enforcement;
- replay/live verification;
- OpenTelemetry, cache, and pricing adapters outside core;
- operational dashboards and runbooks.

Exit criterion: “production-supported provider” has a measurable, current meaning.

## 18.6 Gate 5 - v1 stabilization

Deliverables:

- stable operation set and experimental separation;
- semantic regression baselines;
- complete documentation, security policy, support matrix, and migration guide;
- API/behavior freeze through release candidates;
- load, chaos, privacy, and platform sign-off.

Exit criterion: all v1 acceptance criteria in Section 19 pass and remaining exceptions have explicit ADRs.

## 18.7 Compatibility strategy

During migration:

1. existing fluent and standard calls remain exported;
2. their implementations are redirected one operation at a time to the new kernel;
3. equivalence tests compare option resolution, prompts, errors, and outputs;
4. package-global default provider state becomes a compatibility adapter isolated from core;
5. production docs move to explicit client binding and `Run(ctx)`;
6. global mutation APIs are deprecated and later moved/removed according to SemVer;
7. detailed result APIs are additive and do not force all callers to consume envelopes.

# 19. v1 acceptance criteria

## 19.1 Core architecture

- [ ] Core has no mutable global execution state.
- [ ] Every stable public operation lowers to the same `Op -> Run -> Plan -> Execute` path.
- [ ] Standard and fluent APIs pass mechanical equivalence tests.
- [ ] Stable execution requires `context.Context`.
- [ ] Client and builder ownership/lifecycle are documented and race-tested.
- [ ] Optional adapters do not become mandatory core dependencies.

## 19.2 Correctness and trust

- [ ] Exact decoder rejects unknown/duplicate fields and lossy conversion in strict mode.
- [ ] Presence semantics distinguish missing, null, and zero values.
- [ ] Every stable operation declares deterministic invariants and batchability.
- [ ] Evidence and provenance survive supported pipelines.
- [ ] Requested and delivered contract levels appear in every detailed result.
- [ ] Prompt/data trust boundaries are represented and adversarially tested.
- [ ] Model claims are separated from deterministic checks and runtime facts.

## 19.3 Execution and resilience

- [ ] Homogeneous collections support bounded MDSP with stable IDs.
- [ ] Global operations use tested operation-specific merge semantics.
- [ ] Scheduler enforces call, queue, token, cost, provider, and tenant limits.
- [ ] Retry, repair, split, fallback, escalation, and review share one budget ledger.
- [ ] Partial success, cancellation, shutdown, and budget exhaustion have typed semantics.
- [ ] No valid item is replayed due solely to an independent sibling failure.
- [ ] Circuit breakers and rate limits prevent retry storms.

## 19.4 Security and governance

- [ ] Content logging is off by default; secret/log redaction tests pass.
- [ ] Raw diagnostics are opt-in, bounded, redacted, and retention-controlled.
- [ ] Data policy constrains provider, region, retention, training, cache, and logging.
- [ ] Fallback cannot silently degrade locked contracts.
- [ ] Custom endpoints are subject to SSRF and transport policy.
- [ ] Security policy, threat model, supported versions, and disclosure process are published.

## 19.5 Verification and operations

- [ ] Required unit, race, leak, fuzz, shuffle, replay, vulnerability, and platform CI gates pass.
- [ ] Every production-supported provider passes conformance and recent live verification.
- [ ] Release-candidate semantic regressions are within approved thresholds.
- [ ] Execution envelopes support incident diagnosis without raw content in normal logs.
- [ ] Dashboards, alerts, and minimum runbooks are published.
- [ ] Release notes include semantic prompt/operation changes and provider capability changes.

# 20. Risk register and trade-offs

| Risk | Why it matters | Mitigation / decision |
| --- | --- | --- |
| Overengineering the core | A large framework could erase the simplicity that makes SchemaFlux attractive. | Keep the kernel small; move workflow, adapters, and experimental verbs outward. |
| Planner overhead | Planning and envelopes add CPU/latency for tiny calls. | Keep deterministic planning microsecond/millisecond scale; allow compact envelopes and no-op observers. |
| False confidence from contracts | Users may interpret evidence or invariants as factual proof. | Name each layer precisely; document limitations; preserve review/abstention. |
| API churn | Correcting global state and context may break current users. | Use compatibility wrappers, staged deprecation, equivalence tests, and migration tooling. |
| Adaptive batching instability | Learning can oscillate or hide provider changes. | Bound AIMD, key history by semantic identity, reset on revisions, expose decisions. |
| Provider capability dishonesty | Declared capabilities may drift from real behavior. | Conformance, recent live verification, and runtime contradiction detection. |
| Semantic benchmark expense | Live release gates cost money and can be noisy. | Small high-value corpora, replay for deterministic paths, scheduled RC runs, statistical thresholds. |
| Dependency fragmentation | Too many optional packages can complicate adoption. | One module initially, clear package boundaries, minimal core imports, curated bundles. |
| Evidence token overhead | Field-level evidence increases output size and compliance burden. | Make evidence policy explicit, use compact references, apply only to material fields. |
| Human review integration burden | Review packets need application UX/workflows. | Provide neutral structures and callbacks, not a built-in approval system. |
| Caching privacy risk | Caches can retain sensitive source/output. | Opt-in result cache, tenant partitioning, policy eligibility, TTL/deletion hooks. |
| Global-operation approximation | Hierarchical ranking/clustering may differ from all-at-once results. | Document approximation, validate global invariants, expose plan and quality metrics. |

# 21. Architecture Decision Record summary

| ADR | Decision | Outcome |
| --- | --- | --- |
| ADR-001 | Retain dual public APIs | Standard functions are canonical semantics; fluent builders are equivalent sugar. |
| ADR-002 | One generic execution kernel | All stable operations use `Run/Plan/Execute`. |
| ADR-003 | No mutable global execution state in core | Client and request scopes are explicit. |
| ADR-004 | Require context at execution | `Run(ctx)` is stable; convenience background execution is isolated. |
| ADR-005 | Immutable clients and builders | Avoid semantic reconfiguration races. |
| ADR-006 | Operations are descriptors | Prompt, contracts, batch algebra, and versions are data. |
| ADR-007 | Seven-layer contract stack | Typed output is not conflated with evidence or policy compliance. |
| ADR-008 | Explicit execution shapes | Atomic, MDSP, SDMP, and global plans are distinct. |
| ADR-009 | MDSP plus selective atomic fallback | Optimize homogeneous loops without sacrificing per-item validation. |
| ADR-010 | Deterministic-first runtime | Do exact work in Go; use models for probabilistic judgment. |
| ADR-011 | Unified recovery state machine | Retry, repair, split, fallback, escalation, and review share a budget. |
| ADR-012 | Execution envelope | Governance metadata is constructed for every logical request. |
| ADR-013 | Provider capability negotiation | Portability cannot silently weaken guarantees. |
| ADR-014 | Host-owned observability | SchemaFlux emits; applications configure and own infrastructure. |
| ADR-015 | Stable/beta/experimental tiers | v1 compatibility is limited to operations that earn it. |
| ADR-016 | Version prompts and schemas | Semantic artifacts are part of compatibility identity. |
| ADR-017 | Secure logging by default | Debug does not imply content capture. |
| ADR-018 | Human review is terminal recovery | Abstention is preferable to unbounded model looping. |
| ADR-019 | Operation-specific batch algebra | Global semantics cannot be preserved by generic chunking alone. |
| ADR-020 | Compatibility globals outside core | Quick scripts may use convenience state without weakening production core. |

# Appendix A. Proposed Go API

## A.1 Client construction

```go
client, err := schemaflux.NewClient(
    schemaflux.WithProvider("openai-primary", openai.New(openai.Config{
        APIKey:     secret,
        HTTPClient: httpClient,
    })),
    schemaflux.WithRoutePolicy(schemaflux.RoutePolicy{
        DefaultProvider: "openai-primary",
        MinimumContract: schemaflux.SchemaAndInvariantChecked,
    }),
    schemaflux.WithDataPolicy(tenantPolicy),
    schemaflux.WithBudget(defaultBudget),
    schemaflux.WithObserver(observer),
    schemaflux.WithCache(cache),
)
```

## A.2 Standard and fluent equivalence

```go
// Standard
person, err := schemaflux.Extract[Person](
    ctx, client, text,
    schemaflux.Strict(),
    schemaflux.NoInference(),
)

// Fluent - identical operation/options/runtime
person, err := schemaflux.
    Extracting[Person](text).
    Using(client).
    Strict().
    NoInference().
    Run(ctx)
```

## A.3 Detailed result

```go
result, err := schemaflux.
    Extracting[Person](text).
    Using(client).
    StrictEvidence().
    RunResult(ctx)

switch {
case err == nil:
    use(result.Value)
case errors.Is(err, schemaflux.ErrReviewRequired):
    enqueueReview(result.Envelope.Diagnostics.ReviewPacket)
default:
    handle(err, result.Envelope)
}
```

## A.4 Batch API

```go
result, err := schemaflux.
    ClassifyingMany[TicketCategory](tickets).
    Using(client).
    Adaptive().
    MaxChunkTokens(12_000).
    Parallelism(6).
    FailurePolicy(schemaflux.RetryThenCollect).
    MaxFailureRate(0.02).
    RunResult(ctx)

for _, item := range result.Value.Items {
    if item.Err != nil {
        recordFailure(item.ID, item.Err)
        continue
    }
    persist(item.ID, item.Value)
}
```

## A.5 Custom invariant

```go
op := schemaflux.ExtractOp[Invoice](
    schemaflux.ValidateWith(func(src string, inv Invoice) error {
        if inv.Total.IsNegative() {
            return fmt.Errorf("total must be non-negative")
        }
        return nil
    }),
)

result, err := schemaflux.RunResult(ctx, client, op, document)
```

Custom checks execute inside the kernel and therefore participate in recovery, budgets, telemetry, provenance, and batch isolation.

## A.6 Preflight and plan

```go
builder := schemaflux.
    ExtractingMany[Person](inputs).
    Using(client).
    Adaptive().
    Budget(requestBudget)

report, err := builder.Preflight(ctx)
if err != nil {
    return err
}
fmt.Println(report.Plan.Explain())

result, err := builder.RunResult(ctx)
```

# Appendix B. Error and recovery matrix

| Kind | Retry same request | Repair/regenerate | Fallback | Terminal behavior |
| --- | --- | --- | --- | --- |
| Configuration | No | No | No | Fix client/bootstrap |
| Authentication | No | No | Eligible alternate credential/route only | Operator action |
| Permission | No | No | Only eligible route with same policy | Operator action |
| Rate limited | Yes with `Retry-After` | No | After budgeted retries | Partial/terminal |
| Provider unavailable | Yes | No | Yes if contract preserved | Partial/terminal |
| Timeout | Conditional | No | Conditional | Ambiguity recorded |
| Context too large | No | No | Replan smaller/model with larger context | Oversized item failure |
| Output truncated | Conditional | Regenerate/reduce chunk | Conditional | Item/chunk failure |
| Malformed output | No transport retry | Syntax repair or regenerate | Conditional | Item/chunk failure |
| Schema violation | No transport retry | Targeted repair/regenerate | Conditional | Item failure |
| Batch protocol violation | No | Retry unresolved smaller batch | Conditional | Partial |
| Invariant violation | No | Fresh regenerate | Conditional | Review/terminal |
| Evidence violation | No | Re-extract with evidence | Conditional | Review/terminal |
| Unsupported capability | No | No | Select eligible route | Terminal if none |
| Policy violation | No | No | Only policy-eligible route | Terminal |
| Budget exceeded | No | No | No | Return partial + budget error |
| Circuit open | Queue/fail per policy | No | Eligible route | Admission/partial |
| Canceled/deadline | No | No | No | Return completed partials |
| Review required | No | No | No automatic fallback | Review packet |

# Appendix C. Operation batchability matrix

| Operation family | Batch class | Preferred execution | Merge/global validation |
| --- | --- | --- | --- |
| Extract / classify / per-item transform | Independent | Token-aware MDSP | Exact ID coverage and per-item contracts |
| Validate per item | Independent/hybrid | Deterministic vectorized checks, MDSP only for model rules | Per-item issues and evidence |
| Filter / choose | Subset | MDSP returning IDs; hierarchical candidate selection if global | Every output ID is an input; no duplicates |
| Sort / rank | Permutation/global | Return scores/IDs; hierarchical rerank | Permutation/subset and optional pairwise checks |
| Cluster | Partition/global | Features -> deterministic cluster -> model labels | Every input exactly once; no unknown IDs |
| Deduplicate | Graph/global | Deterministic candidates -> model pair judgments | Connected components; exact input coverage |
| Summarize individually | Independent | MDSP | Per-item evidence/length |
| Synthesize across documents | Hierarchical/global | Evidence-preserving summaries -> synthesis | Final claims map to accumulated evidence |
| Negotiate/session | Sequential stateful | Session API, not generic MDSP | Transcript/state invariants |

# Appendix D. Default policy recommendations

| Policy | Recommended production default | Rationale |
| --- | --- | --- |
| Context | Required | Lifecycle and cancellation are not optional network tuning |
| Content logging | Metadata only | Minimize disclosure |
| Unknown fields | Reject in strict mode | Expose overproduction/hallucination |
| Numbers | Exact intermediate parsing | Prevent lossy coercion |
| Collection mode | Adaptive MDSP for independent plural operations | Balance throughput and per-item correctness |
| Failure policy | Retry then collect | Preserve long-batch progress |
| Atomic fallback | Enabled within budget | Isolate difficult items |
| Cross-provider fallback | Disabled unless explicitly eligible | Prevent semantic/data-policy degradation |
| Semantic result cache | Disabled | Avoid stale or unsafe similarity reuse |
| Raw diagnostics | Disabled | Prevent content leakage |
| Model tier | Explicit route policy | Make floating behavior visible |
| Evidence | Operation-defined; required for material high-risk fields | Balance token cost and grounding |
| Maximum attempts | Small bounded values under parent budget | Prevent retry/repair explosions |
| Human review | Enabled outcome, application handler optional | Safe abstention |

# Appendix E. CI and release gate example

```yaml
name: ci
on:
  pull_request:
  push:
    branches: [main]

jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
        go: ["current", "previous"]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "${{ matrix.go }}" }
      - run: go test ./...
      - run: go test -race ./...
        if: runner.os != 'Windows'
      - run: go test -shuffle=on -count=10 ./...
      - run: go vet ./...

  quality:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: staticcheck ./...
      - run: govulncheck ./...
      - run: go test ./... -run Fuzz -fuzztime=30s
      - run: ./scripts/check-generated-clean.sh
      - run: ./scripts/check-examples.sh

  replay-conformance:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: go test ./conformance/... ./replay/...
```

Live provider and semantic suites should run on protected release-candidate workflows with explicit secrets, spend ceilings, pinned corpus versions, and retained benchmark reports.

# Appendix F. Traceability matrix

| Gap ID | Requirement | Primary specification sections | Acceptance evidence |
| --- | --- | --- | --- |
| ARC-01 | Client isolation | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-02 | Dependency injection boundary | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-03 | Verb explosion | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-04 | Operation descriptors | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-05 | Dual API divergence | 5, 18 | Design review + automated acceptance test |
| ARC-06 | Optional context | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-07 | Mutable builders and clients | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-08 | Configuration scope ambiguity | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-09 | Provider leakage | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-10 | Capability differences | 13, 16-17 | Design review + automated acceptance test |
| ARC-11 | Shape safety only | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-12 | Probabilistic/deterministic mixing | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-13 | Metadata provenance | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-14 | Fragmented recovery loops | 10, Appendix B | Design review + automated acceptance test |
| ARC-15 | Weak error protocol | 10, Appendix B | Design review + automated acceptance test |
| ARC-16 | Collection semantics | 6, 8-9, Appendix C | Design review + automated acceptance test |
| ARC-17 | Tightly packaged dependencies | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-18 | Observability ownership | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-19 | Environment loading | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-20 | Multi-turn mismatch | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-21 | Control-flow mixing | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-22 | Fragmented result vocabulary | 3-7, 10, 13-15 | Design review + automated acceptance test |
| ARC-23 | Prompt compatibility surface | 12, 17-18 | Design review + automated acceptance test |
| ARC-24 | Invisible degradation | 13, 16-17 | Design review + automated acceptance test |
| PRD-01 | CI quality gate | 16-17 | CI/release artifact or operational control |
| PRD-02 | Provider conformance | 13, 16-17 | CI/release artifact or operational control |
| PRD-03 | Release process | 12, 17-18 | CI/release artifact or operational control |
| PRD-04 | Security policy | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-05 | Safe logging | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-06 | Hard budgets | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-07 | Resilience controls | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-08 | Public error contract | 10, Appendix B | CI/release artifact or operational control |
| PRD-09 | Safe diagnostics | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-10 | Idempotency | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-11 | Cancellation coverage | 16-17 | CI/release artifact or operational control |
| PRD-12 | Fuzzing | 16-17 | CI/release artifact or operational control |
| PRD-13 | Replay fixtures | 16-17 | CI/release artifact or operational control |
| PRD-14 | Operation versioning | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-15 | Semantic benchmarks | 16-17 | CI/release artifact or operational control |
| PRD-16 | Stability tiers | 12, 17-18 | CI/release artifact or operational control |
| PRD-17 | Platform guarantees | 12, 17-18 | CI/release artifact or operational control |
| PRD-18 | Readiness diagnostics | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-19 | Configuration validation | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-20 | Shutdown lifecycle | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-21 | HTTP ownership | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-22 | Input preflight | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-23 | Backpressure | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-24 | Runbooks | 4, 10, 13-18 | CI/release artifact or operational control |
| PRD-25 | Repository hygiene | 12, 17-18 | CI/release artifact or operational control |
| API-01 | Preserve dual API surfaces | 5, 18 | Design review + automated acceptance test |
| API-02 | Canonical standard API | 5, 18 | Design review + automated acceptance test |
| API-03 | Fluent equivalence | 5, 18 | Design review + automated acceptance test |
| API-04 | Context at execution | 5-7 | Design review + automated acceptance test |
| API-05 | Immutable builders | 5-7 | Design review + automated acceptance test |
| API-06 | Consistent grammar | 5-7 | Design review + automated acceptance test |
| API-07 | Option escape hatch | 5-7 | Design review + automated acceptance test |
| API-08 | Reusable policies | 5-7 | Design review + automated acceptance test |
| API-09 | Descriptor layer | 5-7 | Design review + automated acceptance test |
| API-10 | Concrete builder types | 5-7 | Design review + automated acceptance test |
| API-11 | Simple and detailed results | 5-7 | Design review + automated acceptance test |
| API-12 | No workflow DSL creep | 5-7 | Design review + automated acceptance test |
| EXE-01 | Explicit execution shapes | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-02 | Serial loop stalls | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-03 | Stable item identity | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-04 | Token-aware chunks | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-05 | Adaptive chunking | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-06 | Failure class separation | 10, Appendix B | Planner/scheduler property and integration tests |
| EXE-07 | Partial success | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-08 | Failure policies | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-09 | Bounded concurrency | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-10 | SDMP reuse | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-11 | Deterministic-first | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-12 | Reference/delta outputs | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-13 | Batchability classes | 6, 8-9, Appendix C | Planner/scheduler property and integration tests |
| EXE-14 | Visible mode policy | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-15 | Singular/plural APIs | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-16 | Loop fusion | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-17 | Hierarchical budgets | 10, Appendix B | Planner/scheduler property and integration tests |
| EXE-18 | Progressive degradation | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-19 | Per-item metrics | 8-10, 15-16 | Planner/scheduler property and integration tests |
| EXE-20 | Plan/execute separation | 8-10, 15-16 | Planner/scheduler property and integration tests |
| TRU-01 | Prompt injection | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-02 | Evidence grounding | 7, 11 | Contract, security, or semantic test evidence |
| TRU-03 | Pipeline provenance | 7, 11 | Contract, security, or semantic test evidence |
| TRU-04 | Model drift | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-05 | Schema evolution | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-06 | Missing vs zero values | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-07 | Numeric precision | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-08 | Unknown fields | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-09 | Complex type support | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-10 | Caching semantics | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-11 | Data residency | 13, 16-17 | Contract, security, or semantic test evidence |
| TRU-12 | Fallback semantics | 13, 16-17 | Contract, security, or semantic test evidence |
| TRU-13 | Streaming typed output | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-14 | Caller backpressure | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-15 | Noisy neighbors | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-16 | Mutation semantics | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-17 | Ordering and duplicates | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-18 | Cancellation partials | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-19 | Repair amplification | 10, Appendix B | Contract, security, or semantic test evidence |
| TRU-20 | Repair regression | 10, Appendix B | Contract, security, or semantic test evidence |
| TRU-21 | Nondeterministic tests | 16-17 | Contract, security, or semantic test evidence |
| TRU-22 | Nested cost attribution | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-23 | Pricing drift | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-24 | Operation semantics | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-25 | Domain extension points | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-26 | Human review | 7, 11 | Contract, security, or semantic test evidence |
| TRU-27 | Ensemble semantics | 7, 11 | Contract, security, or semantic test evidence |
| TRU-28 | Planner explainability | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-29 | Dry run | 7, 11-16 | Contract, security, or semantic test evidence |
| TRU-30 | Validation terminology | 7, 11-16 | Contract, security, or semantic test evidence |

# Appendix G. Glossary

| Term | Definition |
| --- | --- |
| Atomic | One logical datum processed by one provider request. |
| MDSP | Multiple data, single prompt: one homogeneous prompt/schema applied to a bounded item chunk. |
| SDMP | Single data, multiple prompts: staged prompts over one datum, such as extraction then review. |
| Batch algebra | Operation-declared split, encode, merge, and global validation semantics. |
| Contract level | The set of decode, structural, invariant, evidence, provenance, capability, and policy guarantees delivered. |
| Execution envelope | The complete typed operational record for one logical request. |
| Logical request | The caller-visible operation, including every retry, repair, chunk, fallback, and stage. |
| Attempt | One provider call or deterministic recovery action within a logical request. |
| Repair | A constrained correction that may use the invalid answer. |
| Regeneration | A fresh model attempt from trusted source data and the original contract. |
| Fallback | A route change to another provider/model that preserves minimum policy and capability. |
| Escalation | A deliberate move to a stronger/more expensive route after lower-tier semantic failure. |
| Evidence | A bounded reference from a model-derived claim to source content or input structure. |
| Provenance | Lineage describing how output was produced and transformed. |
| Review packet | Structured terminal outcome for human or external approval/reconciliation. |
| Conformance | Evidence that an adapter implements its declared provider/runtime contract. |
| Semantic regression | Measured change in task quality, compliance, hallucination, latency, or cost. |

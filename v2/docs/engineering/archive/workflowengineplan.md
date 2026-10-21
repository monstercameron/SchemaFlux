> **Archived, not scheduled.** `to-production.md` §1.3 makes a general-purpose
> workflow/orchestration language an explicit non-goal for v1, and API-12 forbids
> operation builders growing a branching DSL. M08 builds execution combinators
> (`Escalate`, `Vote`, `Until`, `MapReduce`) instead, which is a deliberate decision
> not to build this. Kept for the thinking in it. See TODOS.md PS-008.

Below is a **fully detailed, UI‑agnostic, world‑class** design document for an enterprise workflow engine. It incorporates all prior decisions, removes UI layer guidance (you’ll use n8n or a custom UI), and deepens implementation details, behaviors, invariants, edge cases, and acceptance criteria. **No code** is included.

---

# 0) Scope, priorities, and positioning

**Primary objective:** A durable, deterministic workflow engine that safely orchestrates long‑running, human‑in‑the‑loop and system workflows across enterprises.

**Priority order:**

1. **Stability:** ACID state, deterministic decisions, durable timers, idempotency at boundaries, strict audit, safe rollbacks.
2. **Scalability:** Horizontal workers, sharding, backpressure, multi‑region readiness.
3. **Flexibility:** JSON DSL with schema, plugin runtime, inter‑workflow interactions, external tool interoperability (e.g., **n8n**).

**UI stance:** Engine exposes APIs, event streams, and exports only; no opinionated UI. n8n (or your UI) consumes these surfaces.

---

# 1) Glossary & invariants

**Workflow Type:** Versioned definition (graph). Once published, immutable. Compatibility flags: backward/forward/none.
**Workflow Run:** Instance of a type; unit of isolation and leasing.
**State:** Named node; contains tasks, timers, awaits, barriers, compensations.
**Task:** Unit of external or human work; idempotent completion by key.
**Timer:** Durable due event; one‑shot or cron; SLA timers for approvals.
**Signal:** Cross‑workflow message with correlation.
**Child Run:** Spawned by a parent (`CallWorkflow` → call/return, or `SpawnWorkflow` → fire‑and‑forget).
**Semaphore:** Named, tenant‑scoped concurrency control for scarce resources.
**CWE (Canonical Workflow Event):** Append‑only event; single source of truth.

**Global invariants:**

* One logical writer per run (lease).
* Per‑run `sequence_number` strictly increases.
* Every state mutation is caused by a persisted CWE.
* Side‑effects are fenced by **outbox** (outgoing) and **inbox** (incoming) with idempotency keys.
* Time is UTC; deterministic decisions do not read wall clock.

---

# 2) Architecture overview

**Control plane:**

* **API Gateway** (gRPC first; REST via gateway), authN/Z, rate limits, quotas.
* **Orchestrator** (deterministic transition function; writes CWEs).
* **Scheduler/Timer Service** (durable timers; SLA escalations).
* **Policy Engine** (OPA/Rego, deny‑by‑default, per‑tenant bundles).
* **Trigger Service** (internal CWEs & external inbox → starts/signals).
* **Child Manager / Signal Router** (call/return, signal correlation).
* **Semaphore Service** (named permits with fairness).
* **Outbox Publisher / Inbox Receiver** (idempotent borders).

**Data plane:**

* **Executors/Workers** (service tasks, compensations, callbacks).
* **Connector Runtime** (gRPC plugins; optional WASM sandbox for untrusted logic).
* **n8n Bridge** (engine ↔ n8n integration, see §16).

**Storage plane:**

* **Event Store** (Postgres; append‑only CWEs; partitions).
* **Snapshots** (JSONB state).
* **Materialized Views** (open tasks, approvals, SLA breaches).
* **Timers** (DB‑backed priority queue; sharded).
* **Outbox/Inbox** (durable intents/messages).
* **Object Store** (large payloads; exports).
* **Search Index** (Elastic/OpenSearch; secondary only).

---

# 3) Canonical Workflow Event (CWE)

**Envelope fields (always present):**
tenant\_id; workflow\_type; type\_version; workflow\_id; run\_id; event\_id; sequence\_number; prev\_hash; hash; event\_time (ingest); effective\_time (business time when relevant); actor; auth\_context\_ref; idempotency\_key; causation\_id; correlation\_id; schema\_version; integrity\_signature (optional).

**Representative event kinds (non‑exhaustive):**

* Lifecycle: Started; Completed; Canceled; Terminated; Migrated.
* Tasks: TaskScheduled; TaskClaimed; TaskStarted; TaskCompleted; TaskFailed; TaskTimedOut.
* Approvals: ApprovalRequested; ApprovalGranted; ApprovalDenied; ApprovalExpired; ApprovalDelegated.
* Timers: TimerSet; TimerCanceled; TimerFired.
* Transitions: Transitioned; GuardFailed; BarrierReached.
* Compensation: CompensationScheduled; CompensationApplied; RollbackPointCreated.
* Inter‑workflow: ChildStarted; ChildCompleted; SignalSent; SignalReceived.
* Borders: OutboundIntentRecorded; InboundMessageAccepted.
* Admin/Policy: Paused; Resumed; Quarantined; PolicyDecisionRecorded; ReplayStarted; ReplayCompleted.

**Tamper evidence & export:**

* Each event `hash` covers envelope+body+`prev_hash` (hash chain).
* Evidence packs include manifest, Merkle proofs, policy decisions, signatures.

**Compatibility:** Binary schema with reserved field numbers; all new fields optional or defaulted; consumers ignore unknowns.

---

# 4) Persistence & data stores

**Primary:** Postgres (or compatible ACID RDBMS) for stability, transactions, and maturity.

**Key tables (conceptual with purpose & indexing):**

* `workflow_events` (append‑only; partitioned by tenant & time). Index: `(run_id, sequence_number)`, `(tenant_id, workflow_type, event_time)`.
* `workflow_runs` (run metadata: status, type, created\_at, last\_state, last\_seq, owner, region).
* `workflow_snapshots` (JSONB state; last\_seq; version hash).
* `timers` (due\_time, timer\_id, run\_id, shard, status, policy). Index: `(shard, due_time)` and `(run_id, timer_id)`.
* `outbox` (dest, payload\_ref, idempotency\_key, attempts, next\_attempt\_at, status).
* `inbox` (source, idempotency\_key, correlation\_key, payload\_ref, status, received\_at).
* `open_tasks` (task\_id, run\_id, type, lease, attempts, SLA info).
* `approvals` (approval\_id, run\_id, role, quorum, SLA timers, status).
* `semaphores` (name, tenant, max\_permits, leased\_permits; queue FIFO index).

**Transactions (hot path):**
Append CWEs → update snapshot → upsert timers → enqueue outbox intents **atomically**. Use `SELECT … FOR UPDATE SKIP LOCKED` or advisory locks for **run exclusivity**. Keep transactions short; avoid chatty loops.

**Partitioning & retention:**

* Monthly (or weekly for hot tenants) partitions for `workflow_events`.
* Hot (90 days), warm (1 year compressed), cold (Parquet exports ≥ N years).
* Snapshot compaction to bound replay time.

**Object storage:**

* Payloads above a threshold stored by content hash; fields record encryption key IDs (BYOK/CMK).
* Exports stored encrypted and signed; residency enforced.

**Search (secondary):**

* Derived, redacted docs for audit/SLA search.
* Execution resilient to search outages.

---

# 5) Execution engine & core functions

## 5.1 Orchestrator (deterministic stepper)

**Function semantics: `AdvanceRun(run_id, cause)`**
**Preconditions:** Holder has a valid lease; `cause` is a due input (TimerFired, TaskCompleted, ApprovalGranted, SignalReceived, Admin, etc.).
**Steps:**

1. Load snapshot at `last_seq`.
2. Evaluate transition function (pure, deterministic; no I/O) using snapshot + cause.
3. Produce proposed CWEs (Transitioned; TaskScheduled; TimerSet; etc.).
4. Validate policies/guards (OPA) and quotas/semaphores.
5. ACID txn: append CWEs → update snapshot → timers/outbox → commit.
6. Renew or release lease depending on remaining ready work.

**Failure modes:**

* Snapshot race (last\_seq mismatch) → retry with fresh snapshot.
* Guard instability → require new `ExternalEventReceived` to re‑evaluate.
* Policy deny → emit `PolicyDecisionRecorded` + `GuardFailed` and follow `on_deny` branch if declared.

## 5.2 Tasks & workers (service or human proxy)

**Scheduling:** Emit `TaskScheduled` and add durable row to `open_tasks`.
**Claiming:** `ClaimTask(task_id)` sets short lease with heartbeat.
**Completion:** `TaskCompleted` with result reference and same idempotency key; orchestrator advances.
**Failure:** `TaskFailed` with error class: retryable, permanent, policy\_denied, external\_limit.
**Edge cases & invariants:**

* Duplicate completion → dedupe on `(task_id, idempotency_key)`; later duplicates are no‑ops.
* Leaked claims → watchdog reclaims on missed heartbeats.
* Backoff → exponential with jitter; max retries enforced by DSL; DLQ after cap.

## 5.3 Approvals & RBAC

**Request:** `ApprovalRequested` with role/quorum/SLA; durable approval row; schedule reminder/escalation/expiry timers.
**Decision:** `ApprovalGranted/Denied/Expired` recording actor, auth\_context, and rationale.
**Quorum logic:** serial/parallel/quorum; OPA decides delegation eligibility; step‑up auth for sensitive steps.
**Edge cases:**

* OOO/Delegation → `ApprovalDelegated`; audit chain preserved.
* Conflicting parallel decisions → ordered by `sequence_number`.
* Business‑time SLAs respect calendars; escalations produce CWEs (reassign, auto‑decision if allowed).

## 5.4 Timers (durable clock)

**Set/Reset:** Idempotent upsert by `(run_id, timer_id)`; store purpose and policy (freeze vs catch\_up on suspend).
**Fire:** Timer workers claim due batches per shard; emit `TimerFired`.
**Edge cases:**

* DST & tz → all schedules are UTC; cron compiled to next UTC instants.
* Drift control → export `now - due_time` metrics; alert on p95 thresholds.

## 5.5 Inter‑workflow interactions

**Call/Return:**

* Parent emits `ChildStarted(call_activity_id)`; child inherits correlation and context (no secrets), version pinned per parent policy.
* Child completion → `ChildCompleted` routed via inbox to parent; parent unparked; result mapping per contract.
* Parent SLA (while child runs): DSL must pick wait/cancel/alternate; linter enforces explicitness.

**Signals:**

* `SendSignal(signal_type, correlation_key, payload, idempotency_key)`; Signal Router dedupes and delivers to matching `AwaitSignal`.
* Broadcast and fan‑out guarded by rate limits and quotas; DLQ if overflow.

**Spawn:** Start child without return path; parent continues (stores child ref for observability).

## 5.6 Backward motion & compensations

**Principle:** Prefer **compensation** to pointer rewind.

* **CompensationScheduled/Applied** emitted for each side‑effecting step when rolling back.
* Pointer rewind permitted only across side‑effect‑free steps (enforced by linter); otherwise require compensator plugin or break‑glass policy with two‑person approval.
* External irreversible effects must record a manual attestation.

---

# 6) Suspend / Resume / Drain / Cancel / Terminate

## 6.1 Suspend

**Effect:** Freeze run/type/tenant/system scope; **no new effects** or transitions.

* Outbox **fenced** (no publish); Inbox **buffers** arrivals (or `reject_actions` per policy).
* Timers: `freeze` (store remaining duration) or `catch_up` (queue on resume).
* Approvals: accept+buffer (default) or reject.
* Child policy: `cascade` (default) | `fence_parent_only` | `cancel_children`.
* Resume: apply buffered inbox in order; restart outbox; cap catch‑up processing to prevent storms.

**Edge cases:**

* Long suspension with huge backlog → phased resume by shard, backlog metrics surfaced, strong rate caps.

## 6.2 Drain

**Effect:** Allow in‑flight tasks/approvals to finish; schedule nothing new.

* Used for blue/green upgrades; transitions permitted only if they do not create new external side‑effects.
* Auto‑suspend on quiesce or after TTL.

## 6.3 Cancel vs Terminate

* **Cancel (graceful):** Schedule compensations in reverse; end in consistent `Canceled` with audit.
* **Terminate (force):** Immediate halt; no compensations. Restricted, fully audited; child propagation policy: `cascade` | `detach` | `complete_then_cancel`.

---

# 7) Progress tracking & visibility (APIs only)

## 7.1 Progress model

* **Milestones:** Named checkpoints declared in DSL; each with a **weight**. Progress % = sum of weights on **taken path**.
* **Parallel branches:** Weighted average within branch scope.
* **Retries/loops:** Do not reduce %; track “effort” and rework counters separately.
* **Children:** Parent progress includes child milestone weight upon completion (or interim coupling if declared).

## 7.2 ETA & risk

* Historical medians per state/task; business calendars; worker/approver backlog contributes.
* Risk flags: predicted SLA breach probability; primary drivers (queue saturation, approver scarcity, external rate‑limits).

## 7.3 Read surfaces

* **GetRun(run\_id):** snapshot, last\_seq, state, tasks, timers (due/remaining), approvals, child refs, progress %, ETA, risk.
* **GetRunTimeline(run\_id, from\_seq, limit):** ordered CWEs with annotations (guard/policy outcomes).
* **StreamRunEvents(filter):** SSE/gRPC stream for runs/types/tags/tenants.
* **ListRuns / ListOpenTasks / ListApprovals / GetAggregates:** filterable via status, SLA, risk, tags.

**Exports:** NDJSON or Parquet evidence packs with chain of custody and policy decisions.

---

# 8) DSL (JSON) semantics & lints (UI‑agnostic)

**Sections:**

* Metadata (name, semver, owners, risk tier, residency).
* Variables (types, defaults, sensitivity, encryption requirements).
* Roles (logical roles → IdP attributes).
* States (on\_enter/on\_exit actions; tasks; transitions with conditions; timeouts; retry policy; barriers; concurrency; semaphore usage).
* Subworkflows (inputs/outputs; version pinning).
* Policies (OPA modules; deny‑by‑default).
* Compensations (reverse actions for every side‑effect step).
* Escalations (SLA steps).
* Triggers (internal/external event rules to start/signals).
* Suspend policies (timer behavior, inbox/outbox, child strategy).
* Progress (milestones & weights).
* Business calendars (references to named calendars).

**Linter rules (non‑exhaustive):**

* Missing compensation for side‑effects; unbounded retries; missing `else` branch; parallel deadlocks; unsafe back‑edges; signals without correlation; child‑parent cycles; human loops without SLA; conflicting suspend policies; triggers that can self‑loop; semaphores acquired without guaranteed release path.

---

# 9) API surface (gRPC first, REST via gateway) — behaviors & errors

> All mutating endpoints require **Idempotency‑Key**; responses include `last_seq` and an `etag` of the snapshot when relevant. Errors are typed: retryable, permanent, policy\_denied, invalid\_state, conflict.

**Types & runs:**

* PublishWorkflowType; DeprecateVersion; StartRun(type, input, run\_key?); GetRun; ListRuns; ReplayRun(from\_seq); MigrateRun(to\_version).

**Tasks & approvals:**

* ClaimTask; CompleteTask; FailTask; ListOpenTasks.
* SubmitApproval; DelegateApproval; ListApprovals.

**Inter‑workflow & signals:**

* CallChild; SpawnWorkflow; SendSignal.

**Timers & triggers:**

* ListTimers; ForceFireTimer.
* CreateTrigger; EnableTrigger; DisableTrigger.

**Control:**

* Suspend(scope, policies); Resume(scope); Drain(scope); Cancel(scope, policy); Terminate(scope).

**Search & export:**

* SearchEvents(filter); ExportRun(format=NDJSON|Parquet).

**AuthZ & policy:**

* RBAC scopes + ABAC via OPA; policy outcomes recorded as `PolicyDecisionRecorded`.

---

# 10) Triggers, eventing & CEP

**Internal CWEs → Actions:**

* Trigger Service tails `workflow_events` (or a mirrored stream) and evaluates declarative rules.
* Deterministic idempotency key = `(source_event_id, rule_id)`.
* Actions: StartRun, SendSignal, or SetTimer.

**External events → Actions:**

* Inbox adapters for HTTP/gRPC/message queues accept CloudEvents‑like messages; validate schema; dedupe via `idempotency_key`; policy decides persistence level (full/redacted/reference).
* Map to SendSignal or StartRun using correlation.

**CEP windows (optional advanced):**

* Windowed rules (“3 declines in 5 minutes → escalate”).
* Watermarking rules for late events; idempotency rooted at `(window, rule_id)`.

**Edge cases:**

* Feedback loops: linter detects self‑trigger cycles; require stabilizer guard.
* Storm control: global/tenant quotas; DLQ + replay with rate caps.

---

# 11) Security, compliance & trust

**BYOK/CMK:** Per‑tenant keys; rotation & revocation; crypto‑erase semantics; HSM/KMS backed. Record key IDs per encrypted field.
**Policy as code:** Tenant‑scoped OPA bundles; signed; compiled & cached by content hash; fail‑closed for high‑risk operations.
**Secrets:** External vault references only; never in DSL or CWEs; short‑lived tokens at runtime.
**Data residency:** Region binding enforced server‑side; exports validated against residency policy.
**Supply chain:** Signed/attested plugins with SBOM and provenance (SLSA); runtime attestation; quarantine on revocation.
**Audit:** Hash‑chained CWEs; evidentiary export includes policy decisions, signatures, lineage, and replay instructions.

---

# 12) Scalability & performance

**Sharding:** Hash(tenant\_id, workflow\_id) → shard; **sticky leases** per run.
**Work classes:** Latency‑sensitive vs bulk; separate worker pools; autoscaling by queue depth, timer lag, and p95 decision latency.
**Work stealing:** Idle shards pull from busy shards within quotas.
**Backpressure:** Per‑tenant quotas; rate limits per connector; shed non‑critical triggers during brownouts.
**Hot partition mitigation:** Detect skew; dynamic shard expansion; per‑tenant isolation pools.
**IDs:** ULIDs for time‑ordered inserts and DB locality.
**SLOs (suggested):** p95 decision latency < 200 ms at 10k tasks/s; p95 timer drift < 2 s at 1k timers/s; resume catch‑up ≥ 2k buffered events/s/shard; dedupe error < 10⁻⁶.

---

# 13) Caching (safely non‑authoritative)

* **Hot snapshot cache:** keyed by `(run_id, last_seq)`; invalidate on mismatch and leases.
* **Definition/policy cache:** by content hash; stale‑while‑revalidate.
* **Directory/IdP cache:** short TTL for role/group membership.
* **Never authoritative:** approvals, due timers, queue cursors—always persisted first.

---

# 14) Observability (no UI)

**Tracing:** OpenTelemetry spans for orchestrator, workers, outbox/inbox, connectors; propagate correlation IDs to external systems (HTTP headers, gRPC metadata).
**Metrics (core):**

* Decision latency; queue depth; worker utilization; retries; DLQ depth; timer drift; approval SLA breaches; outbox backlog; inbox dedupe rate; semaphore wait time; suspend/drain counts; resume catch‑up rate; compensation success rate.
  **Structured logs:** Correlate by run\_id, event\_id; PII‑aware redaction; include policy decision refs.
  **Streams:** SSE/gRPC event streams for consuming systems (e.g., n8n) to react in real time.

---

# 15) Testing & hardening

**Determinism tests:** Randomized interleavings; replays must converge to identical snapshots.
**Property‑based tests:** Guards never leave undefined states; compensations restore invariants; no unbounded retries.
**Chaos:** Kill workers; duplicate deliveries; inject latency; drop connections; simulate DB failover; verify progress and no duplication.
**Migration tests:** Mixed definitions and plugins; sticky in‑flight runs; batch migration dry runs produce diffs and risk reports.
**Scale tests:** Soak & spike; timer storm recovery; hot tenant scenarios; semaphore contention.
**Security tests:** Policy bypass attempts; privilege escalation; secrets leakage; signature revocation response.

---

# 16) n8n (and similar) integration — engine‑centric contracts

**Patterns:**

* **Engine‑of‑Record (recommended):** Engine owns state/timers/approvals/audit; n8n executes tasks. Engine emits outbox intent → n8n webhook/queue; n8n completes job → engine `CompleteTask`.
* **Co‑orchestration:** n8n flows start runs, send signals; engine handles long‑running and human steps.
* **Spawn & Observe:** n8n starts runs and subscribes to event streams to trigger additional automations.

**Connector requirements (engine side):**

* Auth: OAuth2 client credentials or HMAC‑signed webhooks; mTLS optional.
* Idempotency: every job carries `Idempotency‑Key`, `run_id`, `task_id`.
* Delivery: at‑least‑once; n8n nodes must be idempotent.
* Response contract: outcome (`ok|retry|permanent_fail|external_limit`), outputs ref, diagnostics.
* Rate limits & quotas per tenant; DLQ + replay with jitter.

**Security considerations:**

* IP allowlists, replay protection, timestamped signatures; short TTL tokens; least privilege per n8n workflow.

---

# 17) Operational runbooks (UI‑agnostic)

**Timer lag:** Check shard metrics; increase parallelism; verify due\_time indexes; apply storm caps; reindex if necessary.
**Outbox backlog:** Scale publishers; throttle emitters; inspect downstream rate limits; promote DLQ → replay with caps.
**Inbox dedupe anomalies:** Expand dedupe window; validate upstream idempotency; analyze `idempotency_key` collisions.
**Suspend/Resume:** Dry‑run resume (count buffered); phased resume by shard; monitor catch‑up rate and timer drift.
**Cancel/Compensation:** Execute compensation DAG with deadlines; manual attestations for irreversibles; generate evidence pack.
**DR drill:** Region loss simulation; rehydrate snapshots; verify hash continuity; suppress external side‑effects via dry‑run connectors.

---

# 18) Versioning & migration

**Types:** Immutable versions; new runs use latest unless pinned; in‑flight runs stick by default.
**Migrations:** `migrate(plan)` defines per‑run path; simulator previews differences; execute in batches with backpressure awareness.
**Plugins:** Capability negotiation; semver compatibility; contract tests; quarantine on breakage.
**Pinning:** Execution pins definition hash; mismatch at runtime → fail‑closed unless break‑glass override recorded as CWE.

---

# 19) Data lifecycle & governance

**Retention tiers:** Hot (≤ 90 days); Warm (≤ 1 year compressed); Cold (Parquet exports ≥ N years).
**Right to erasure:** Pseudonymized references; tombstones; crypto‑erase via key revocation for field‑level encryption.
**Lineage:** CWEs annotate read/write sets; lineage graph derivable per run and dataset.
**Field‑level retention & purpose:** Policy limits access by purpose; auto‑purge jobs with proofs; exports honor purpose tags.

---

# 20) Performance acceptance criteria (minimum to ship)

* **Stability:** Zero lost CWEs on restarts; idempotent border correctness < 10⁻⁶ error rate.
* **Timers:** p95 drift < 2 s at 1k timers/s; deterministic cron scheduling in UTC across DST.
* **Throughput:** 10k tasks/s sustained with p95 decision latency < 200 ms.
* **Suspend/Resume:** Resume catch‑up ≥ 2k buffered inputs/s/shard; no storm‑induced timeouts.
* **Cancel/Compensation:** ≥ 99% compensations succeed automatically; irreversibles captured with manual attestation CWEs.
* **Evidence:** Export pack verifiable (hash chain, signatures, policy logs) and replayable without external side‑effects.

---

# 21) Risk register & mitigations

* **External non‑idempotent systems:** Enforce idempotency keys; compensations; side‑effect partitions; “read‑your‑writes” verification where feasible.
* **Timer/trigger storms:** Shard caps; paced catch‑up; DLQ spillover; backoff with jitter.
* **Hot tenant skew:** Quotas; reserved shards; per‑tenant worker pools.
* **Definition drift:** Execution pins hash; fail‑closed; linter + review gates.
* **Plugin supply chain:** Signed & attested; SBOM; runtime attestation; immediate quarantine on revocation.
* **Clock skew:** Use DB time for due comparisons; NTP disciplined hosts.
* **Large payloads:** Offload to object store; enforce size limits; redact by policy.

---

# 22) “World‑class” differentiators (beyond baseline)

* **BYOK/CMK with HSM/KMS**, per‑tenant crypto domains, crypto‑erase.
* **Evidentiary audit packs** (hash chain + signatures + policy logs + replay script).
* **BPMN/DMN import/export** (deterministic subset mapping to DSL).
* **CEP windowed triggers** and **predictive SLAs** (business‑time aware).
* **What‑if simulation & Monte Carlo** for throughput and SLA planning.
* **Signed plugin marketplace** with certification for major enterprise systems (SAP, Oracle, Salesforce, ServiceNow, Workday, MS Graph, Snowflake).
* **DR drills as a feature** (push‑button simulation, shadow replays with effect suppression).

---

## Appendix A — Error taxonomy (engine‑wide)

* **retryable** (transient network, rate limited, downstream 5xx)
* **permanent** (validation, invariant violated, unknown task)
* **policy\_denied** (OPA decision)
* **external\_limit** (quota exceeded; tell client when to retry)
* **conflict** (last\_seq mismatch; concurrent modification)
* **suspended** (operation rejected due to suspend policy)

Each error carries: category, human‑readable reason, machine code, retry‑after (if applicable), correlation IDs.

---

## Appendix B — Idempotency & dedupe rules

* **Outbox:** One intent per side‑effecting action per attempt; key = deterministic function of `(run_id, step_ref, attempt)` or business key; at‑least‑once publish; receivers must accept duplicates.
* **Inbox:** Dedupe window bounded (configurable per source); key = upstream idempotency key; store acceptance status and causation.
* **Tasks:** Completion dedupe on `(task_id, idempotency_key)`; late duplicates → no‑ops.
* **Signals:** Keyed by `(signal_type, correlation_key, idempotency_key)`; duplicates suppressed.
* **Timers:** Idempotent upsert on `(run_id, timer_id)`; multiple fires deduped by `sequence_number`.

---

## Appendix C — Business‑time calendars (for SLAs & ETA)

* **Named calendars** per tenant/region/role; versioned.
* **Compilation:** Working windows precomputed into UTC ranges at timer creation; version attached to timers.
* **DST safety:** Always compute in UTC; never store local wall‑clock.
* **Overrides:** Holiday exceptions; shift schedules; “follow‑the‑sun” routing rules.

---

## Appendix D — Data dictionary (condensed; no SQL)

* **workflow\_events:** run identifiers, envelope fields, event kind, body ref, prev\_hash, hash, event\_time.
* **workflow\_runs:** status (active, paused, drained, canceled, completed, terminated), type/version hash, owner, created\_at, last\_state, last\_seq, region.
* **workflow\_snapshots:** state JSON, variables (redacted as per policy), active tasks/timers, outstanding approvals, child refs, last\_seq, definition hash.
* **timers:** run\_id, timer\_id, due\_time, policy, shard, status, retries.
* **outbox/inbox:** endpoint/source, idempotency\_key, payload\_ref (object store), attempts, next\_attempt\_at, status, diagnostics.
* **open\_tasks:** task metadata, lease info, attempts, SLA markers.
* **approvals:** approval metadata, quorum, role, escalation timers, status, actor trail.
* **semaphores:** name, permits, wait queue pointer, lease TTL.

---

## Appendix E — Build checklists

**Before publishing a workflow type:**

* Linter passes (no missing compensations, no deadlocks, no unbounded retries).
* Policies compile and are attached; data residency/PII tags defined.
* Progress milestones/weights defined (or default).
* Suspend/cancel child policies explicit.
* Trigger loops analyzed and constrained.

**Before enabling a connector/plugin:**

* Idempotency contract documented; authentication configured; rate limits & retry taxonomy mapped; observability fields (trace IDs) propagated; signed & attested.

**Before a major upgrade:**

* Blue/green plan; drain thresholds; mixed‑version tests; failover and rollback procedures rehearsed; DR drill scheduled.

---

### Final note

This document gives you a **complete, UI‑agnostic** blueprint for a **world‑class** workflow engine: a stable event‑sourced core with ACID guarantees; deterministic execution; durable timers; human approvals with policy controls; inter‑workflow call/return and signaling; suspend/resume/drain/cancel with strict invariants; robust idempotency at system boundaries; strong security/compliance posture (BYOK/CMK, signed plugins, evidence‑grade exports); scalable runtime (shards, work classes, backpressure); and thorough operability (metrics, traces, runbooks, DR drills). It integrates cleanly with **n8n** or any orchestration UI through webhooks, event streams, and APIs—without coupling the engine to a specific presentation layer.

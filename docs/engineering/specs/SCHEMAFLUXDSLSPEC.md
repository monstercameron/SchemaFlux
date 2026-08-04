Below is a **UI‑agnostic**, **simple‑but‑powerful** **Workflow DSL v1** that lets a SaaS vendor deliver highly customized workflows for many customers **without writing custom code**. It covers node types, edges (routing), branching, integrations, data transforms, and storage read/write—together with crisp execution semantics, idempotency, and policy controls.

> **Design goals:**
>
> 1. **Stable & deterministic**, 2) **Scalable**, 3) **Flexible but comprehensible** (small set of orthogonal concepts), 4) **No customer code required**—only JSON config and expressions.

---

## 0) Big picture (what this DSL gives you)

* **Graph of nodes** (states) connected by **transitions** (edges).
* Nodes do things: call integrations, transform data, wait for timers/signals, route conditionally (including rules/fuzzy/LLM), run in parallel, iterate lists, ask for approvals, read/write storage, or call other workflows.
* **Expressions** provide dynamic behavior and data mapping (no code).
* **Connectors** encapsulate external systems; **route tables** encode reusable routing labels.
* **Policies** (OPA/ABAC) control security, PII, and guardrails; **idempotency** & **retries** are first‑class.

---

## 1) Top‑level document

```json
{
  "dsl_version": "1.0",
  "name": "employee-onboarding",
  "version": "3.2.0",
  "metadata": { "owner": "hr-platform", "tags": ["hr","onboarding"], "description": "..." },

  "input_schema": { /* JSON Schema of inputs */ },
  "output_schema": { /* JSON Schema of outputs */ },

  "variables": { /* declared variables & defaults (optional) */ },

  "policies": { /* policy bundle references and toggles (optional) */ },

  "connectors": { /* optional per-workflow connector aliases (see §6) */ },

  "route_tables": { /* optional embedded route tables (see §5) */ },

  "storage_bindings": { /* logical stores → concrete backends (optional) */ },

  "start": "state_id",
  "end_states": ["succeed", "fail"],

  "nodes": { /* map of node_id → node definition (see §4 for each type) */ }
}
```

### 1.1 Minimal constraints

* `name`, `version`, `start`, `nodes` are required.
* Node IDs must be unique. Every `to`/`next`/transition target must exist unless using a **handoff** (child/spawn/signal).

---

## 2) Expressions & interpolation (one model, everywhere)

To avoid competing syntaxes, the DSL uses **JMESPath** expressions with a **single interpolation rule**:

* **Inside strings:** `${ <JMESPath> }`
  Example: `"Welcome ${employee.firstName}!"`

* **As typed values:** `{"$eval": "<JMESPath>"}`
  Example: `"timeout_sec": {"$eval": "coalesce(taskTimeout, 30)"}`

* **Conditions (booleans):** `"when": "<JMESPath boolean>"`
  Example: `"when": "employee.type == 'contractor'"`

**Available context variables in expressions**

* `$` → full workflow context (inputs, variables, node outputs).
* `context.runId`, `context.tenantId`, `context.nowUtc`, `context.seq`, etc.
* **Built‑ins** (pure functions): `now()`, `uuid()`, `ulid()`, `len(x)`, `contains(x,y)`, `coalesce(a,b,...)`, `concat(a,b,...)`, `match(re, s)`, `lower(s)`, `hash(alg, s)`, `base64(s)`, `json_parse(s)`, `json_stringify(x)`, `to_number(x)`, `to_string(x)`, `if(cond,a,b)`, `secret(name)` (returns a handle; never printed).

> **No customer code**: everything is expressions, mappings, and configuration.
> **Determinism**: expressions must be pure (no network). Use nodes to fetch data first, then route/transform.

---

## 3) Transitions (edges) & handoffs

Every node can declare **transitions** deciding **what comes next**.

```json
"transitions": [
  { "when": "order.total > 1000", "to": "manager_approval" },
  { "when": "order.total <= 1000", "to": "auto_approve" },
  { "default": true, "to": "manual_triage" }
]
```

* **Evaluation order**: top‑down; first true wins; one `default` allowed.
* **Target**: `to: "node_id"` (same workflow) **or** a **handoff**:

  * `handoff.call_child`: call a sub‑workflow and wait for result.
  * `handoff.spawn`: fire‑and‑forget child workflow.
  * `handoff.signal`: send a signal to another workflow (choreography).

**Handoff forms (any transition may use them)**

```json
{ "handoff": {
  "call_child": { "type": "legal-onboarding", "pin_version": "1.*", "input": { "employee": "$.employee" }, "timeout": "P3D", "map_result_to": "vars.legalResult" }
}}
```

```json
{ "handoff": {
  "spawn": { "type": "payroll-setup", "pin_version": ">=2.0 <3.0", "input": { "employeeId": "$.employee.id" } }
}}
```

```json
{ "handoff": {
  "signal": { "signal_type": "REVIEW_READY", "correlation": "employee.id", "payload": { "id": "$.employee.id" } }
}}
```

---

## 4) Node types (simple, orthogonal)

> **Uniform common fields**, then **type‑specific** fields.

### 4.0 Common fields (apply to every node)

```json
{
  "id": "unique_id",
  "type": "<see below>",
  "name": "Human readable",
  "timeout": "PT30S",                   // ISO‑8601 or duration like "30s"
  "retry": { "max_attempts": 3, "strategy": "exponential", "initial": "1s", "max_delay": "30s", "jitter": true },
  "idempotency_key": "${context.runId}-${id}-${context.seq}",   // default if omitted
  "semaphore": { "name": "resource:printer", "permits": 1 },    // optional
  "audit": { "redact": ["employee.ssn"], "extra": { "note": "..." } }, // optional
  "on_error": { "to": "error_handler" },                        // or handoff (same shape as transitions)
  "transitions": [ /* see §3 */ ]
}
```

---

### 4.1 `task.service` — external integration call (HTTP/gRPC/GraphQL/SQL/Queue)

```json
{
  "type": "task.service",
  "connector": "http",                     // "http" | "grpc" | "graphql" | "sql" | "queue"
  "operation": "POST",                     // per connector; e.g., HTTP method, gRPC method
  "endpoint": "https://api.example.com/v1/users",
  "headers": { "Authorization": "Bearer ${secret('EXAMPLE_TOKEN')}", "Idempotency-Key": "${idempotency_key}" },
  "query": { "verbose": true, "tenant": "${context.tenantId}" },
  "body": { "email": "${employee.email}", "name": "${employee.fullName}" },
  "response_map": {                        // where to place outputs (pure mapping)
    "to": "vars.user",
    "select": "$.body"                     // JMESPath over connector's normalized response {status,headers,body}
  },
  "retry": { ... }, "timeout": "PT10S"
}
```

**Connector specifics (summary)**

* **HTTP**: `operation`=GET/POST/…; `endpoint`, `headers`, `query`, `body`.
* **gRPC**: `operation`="pkg.Service/Method", `endpoint`="host\:port", `request` object.
* **GraphQL**: `operation`="query"|"mutation", `endpoint`, `document`(string), `variables`.
* **SQL**: `operation`="query"|"exec", `datasource` alias, `statement` with `${}` interpolation **or** `params` binding; outputs rows to map.
* **Queue**: `operation`="publish"|"consume", topic/queue name, payloads; **consume** typically used via `wait.signal` instead.

**No customer code required**: all mapping done via `response_map.select` and expressions.

---

### 4.2 `task.transform` — object shape transforms (map/merge/extract)

```json
{
  "type": "task.transform",
  "assign": [
    { "path": "vars.employee.fullName", "value": "${concat(employee.firstName, ' ', employee.lastName)}" },
    { "path": "vars.flags.isEU", "value": "${employee.country in ['DE','FR','IT','ES','NL']}" }
  ],
  "remove": ["vars.temp.ssnRaw"],             // optional
  "validate": { /* JSON Schema applied after transform (optional) */ }
}
```

* **Semantics**: apply `assign` in order; each `value` is evaluated; `remove` deletes paths; `validate` enforces shape.
* **Idempotent** by design.

---

### 4.3 `router` — conditional route selection (code‑free)

A general router that supports **strategies**: `"conditions"`, `"rules"`, `"fuzzy"`, `"llm"`, or `"hybrid"`.

```json
{
  "type": "router",
  "strategy": "hybrid",
  "inputs": { "dept": "${department}", "title": "${employee.role}", "region": "${employee.region}" },
  "route_table": "onboarding_routes:v3",
  "thresholds": { "min_confidence": 0.8, "ambiguous_delta": 0.05 },
  "fallback": { "on_low_confidence": { "to": "manual_triage" }, "on_ambiguous": { "to": "manual_triage" } },
  "rules": { "policy_ref": "opa://bundles/hr-routing" },               // only if strategy requires
  "fuzzy": { "catalog": "equipment_bundles:v7", "metric": "jaro", "normalize": true },  // optional
  "llm": { "model": "vendor/model@v1", "constrained_labels_from_route_table": true, "temperature": 0, "top_p": 0 },
  "transitions": [
    { "route": "US_FTE", "to": "us_setup" },
    { "route": "EU_FTE", "to": "eu_setup" },
    { "route": "Contractor", "to": "contractor_setup" },
    { "route": "Legal", "handoff": { "call_child": { "type": "legal-onboarding", "pin_version": "1.*", "input": { "employee": "${employee}" } } } }
  ]
}
```

* **conditions**: simplest—ordered cases with `when` (use `choice` if you don’t need scores).
* **rules**: policy/rule tables (DMN/OPA) return label + reasons.
* **fuzzy**: chooses label from catalog by string/semantic similarity (calibrated to confidence).
* **llm**: returns label within **allowed set** (from route table); constrained JSON output; temperature 0 for determinism.
* **hybrid**: try conditions → rules → fuzzy → llm; first above threshold wins.

> Emits `RouteEvaluated`/`RouteSelected` events; for non‑deterministic strategies, the selection is **pinned** to snapshot.

---

### 4.4 `choice` — simple if/else branching

```json
{
  "type": "choice",
  "cases": [
    { "when": "employee.type == 'contractor'", "to": "contractor_setup" },
    { "when": "employee.region == 'EU'", "to": "eu_setup" }
  ],
  "default": { "to": "us_setup" }
}
```

---

### 4.5 `parallel` — run branches concurrently, then join

```json
{
  "type": "parallel",
  "branches": [
    { "name": "accounts", "sequence": ["create_ad", "create_email"] },
    { "name": "kronos",   "sequence": ["create_kronos"] }
  ],
  "join": { "policy": "all", "timeout": "PT30M" },   // "all" | "any" | "quorum", with optional "count" for quorum
  "next": "post_setup"                               // on successful join
}
```

* Branch steps refer to **existing node IDs** (nodes must be defined separately).
* Errors inside a branch respect each node’s `on_error`. If a branch fails:

  * `join.policy="all"` → the parallel node fails (or you can set `on_error` on the parallel node).
  * `any` → succeed when first branch completes; others are canceled (or compensated) per policy.

---

### 4.6 `foreach` — iterate an array with concurrency control

```json
{
  "type": "foreach",
  "items": "${equipment.items}",
  "item_var": "item",
  "sequence": ["assign_equipment_item"],    // list of node IDs executed per item
  "concurrency": 5,
  "accumulator": {                          // optional: collect outputs
    "path": "vars.assigned",
    "append": "${vars.assign_equipment_item.result}"
  },
  "next": "notify"
}
```

---

### 4.7 `wait.timer` — wait for a duration, timestamp, or cron

```json
{
  "type": "wait.timer",
  "mode": "duration",                 // "duration" | "until" | "cron"
  "duration": "PT2H",
  "timer_id": "reminder-approval",    // idempotent; resetting updates the same durable timer
  "on_timeout": { "to": "escalate" }
}
```

---

### 4.8 `wait.signal` — wait for a signal or inbound message

```json
{
  "type": "wait.signal",
  "signal_type": "APPROVAL_DECISION",
  "correlation": "${employee.id}",     // correlation key
  "timeout": "P2D",
  "on_timeout": { "to": "auto_deny" }
}
```

* Inbound messages go to the **inbox** and are deduped by idempotency key; this node resumes when a matching message arrives.

---

### 4.9 `approval` — human-in-the-loop decision

```json
{
  "type": "approval",
  "role": "hr_manager",                  // logical role (mapped via IdP/policy)
  "quorum": { "mode": "single" },        // "single" | "all" | "n_of_m": { "n": 2, "m": 3 }
  "sla": { "duration": "P2D", "escalate_to": "hr_lead" },
  "form": { "fields": [{ "name": "decision", "type": "select", "options": ["approve","reject","conditional"] }, { "name": "notes", "type": "textarea" }] },
  "output": "vars.approval",             // engine writes decision object here
  "transitions": [
    { "when": "vars.approval.decision == 'approve'", "to": "next_step" },
    { "when": "vars.approval.decision == 'conditional'", "to": "conditional_path" },
    { "when": "vars.approval.decision == 'reject'", "to": "fail_path" }
  ]
}
```

---

### 4.10 `storage` — read/write/update/delete to logical stores (KV/Doc/SQL/Object)

```json
{
  "type": "storage",
  "store": "kv",                                // "kv" | "doc" | "sql" | "object"
  "op": "put",                                  // "get" | "put" | "update" | "delete" | "query" (sql/doc)
  "namespace": "employees",                     // logical namespace/table/bucket
  "key": "${employee.id}",
  "value": { "name": "${employee.fullName}", "manager": "${employee.manager.email}" },
  "result_map": { "to": "vars.dbEmployee", "select": "$" }
}
```

* `doc` (document DB): use `filter` + `update` shape or `query` with params; results mapped.
* `sql`: `statement` + `params` or pre‑registered query name.
* `object`: put/get blobs (use object store for large payloads).

---

### 4.11 `emit.event` — internal event (analytics/audit) and/or outbound webhook

```json
{
  "type": "emit.event",
  "name": "ONBOARDING_COMPLETED",
  "payload": { "employeeId": "${employee.id}", "dept": "${department}" },
  "outbound": {                            // optional webhook publish via outbox
    "connector": "http",
    "endpoint": "https://hooks.example.com/onboarding",
    "headers": { "X-Signature": "${hash('sha256', concat(payload.employeeId, secret('WEBHOOK_SECRET')))}" }
  }
}
```

---

### 4.12 `subflow.call` / `subflow.spawn` — explicit sub‑workflow nodes

Alternative to using **handoff** inside transitions:

```json
{ "type": "subflow.call", "workflow_type": "legal-onboarding", "pin_version": "1.*", "input": { ... }, "map_result_to": "vars.legal" }
```

```json
{ "type": "subflow.spawn", "workflow_type": "payroll-setup", "pin_version": "2.*", "input": { ... } }
```

---

### 4.13 Terminal nodes

```json
{ "type": "succeed", "output": { "employeeId": "${employee.id}", "accounts": "${vars.accounts}" } }
```

```json
{ "type": "fail", "error": { "code": "ONBOARDING_FAILED", "message": "..." } }
```

---

## 5) Route tables (reusable routing labels)

```json
"route_tables": {
  "onboarding_routes:v3": {
    "routes": [
      { "label": "US_FTE", "tags": ["region:US","type:fte"] },
      { "label": "EU_FTE", "tags": ["region:EU","type:fte"] },
      { "label": "Contractor", "tags": ["type:contractor"] },
      { "label": "Legal", "tags": ["escalation","legal"] }
    ]
  },
  "equipment_bundles:v7": {
    "routes": [
      { "label": "DataScience_Pro", "aliases": ["ml eng","data scientist pro"] },
      { "label": "Engineering_Standard", "aliases": ["software engineer","swe"] },
      { "label": "Sales_Lightweight", "aliases": ["ae","account executive"] }
    ]
  }
}
```

* Versioned and immutable once published; referenced by routers.

---

## 6) Connectors (integration catalog)

Connectors can be **declared globally** or **referenced by alias** in a workflow:

```json
"connectors": {
  "http": { "auth": { "type": "oauth2_client_credentials", "token_url": "..." } },
  "salesforce": { "type": "http", "base_url": "https://api.salesforce.com", "auth": { "type": "oauth2", "client_id": "…", "secret": "secret:SF_CLIENT_SECRET" } },
  "kronos": { "type": "http", "base_url": "${secret('KRONOS_BASE')}", "auth": { "type": "api_key", "header": "X-API-Key", "value": "secret:KRONOS_API_KEY" } }
}
```

* Node usage can either use the **generic** connector (`"connector":"http"`) or a **named alias** (`"connector":"kronos"`).
* **Idempotency**: engine injects `Idempotency-Key` header if not set; map to downstream field when needed.
* **Secrets**: always referenced via `secret('NAME')` (resolved by engine).

---

## 7) Policies (security & governance)

```json
"policies": {
  "opa_bundles": ["opa://bundles/hr", "opa://bundles/pii"],
  "enforcement": {
    "deny_on_pii_in_llm": true,
    "approval_required_for_routes": ["Legal"]
  },
  "redaction": ["employee.ssn", "employee.dateOfBirth"]
}
```

* Policies can gate routes, prevent PII egress (e.g., to LLM), or enforce role checks on approvals.

---

## 8) Error handling, retries, timeouts, and idempotency

* **Per‑node retry**: `retry` block; classify errors by connector result (e.g., HTTP 5xx = retryable).
* **Global defaults** can exist (engine config) but per‑node overrides win.
* **Timeouts** abort the node and go to `on_error` transition or propagate error.
* **Idempotency keys**: default `${context.runId}-${node.id}-${context.seq}`; can override. Propagated to connectors where applicable (headers/metadata).
* **Compensation** (configured in engine, not per‑node DSL): each `task.service` can have a named compensator bound out‑of‑band or in a catalog; the engine runs compensations on cancel/rollback where applicable.

---

## 9) Storage bindings (logical → physical)

```json
"storage_bindings": {
  "kv": { "backend": "engine_kv" },                               // engine's KV
  "doc": { "backend": "mongodb", "database": "hrdb" },
  "sql": { "backend": "postgres", "dsn": "secret:PG_DSN" },
  "object": { "backend": "s3", "bucket": "hr-artifacts", "kms_key": "alias/hr-kms" }
}
```

Nodes refer only to logical store names (`kv`, `doc`, `sql`, `object`).

---

## 10) Minimal working example (compact)

```json
{
  "dsl_version": "1.0",
  "name": "onboarding",
  "version": "1.0.0",
  "start": "validate",
  "nodes": {
    "validate": {
      "type": "task.service",
      "connector": "http",
      "operation": "POST",
      "endpoint": "https://id.example/validate",
      "body": { "ssn": "${employee.ssn}", "name": "${employee.fullName}" },
      "response_map": { "to": "vars.validation", "select": "$.body" },
      "transitions": [ { "when": "vars.validation.ok == true", "to": "route_path" }, { "default": true, "to": "manual_review" } ]
    },
    "route_path": {
      "type": "router",
      "strategy": "conditions",
      "transitions": [
        { "when": "employee.type == 'contractor'", "to": "contractor_setup" },
        { "when": "employee.region == 'EU'", "to": "eu_setup" },
        { "default": true, "to": "us_setup" }
      ]
    },
    "us_setup": { "type": "task.transform", "assign": [ { "path": "vars.region", "value": "US" } ], "transitions": [ { "to": "done" } ] },
    "eu_setup": { "type": "task.transform", "assign": [ { "path": "vars.region", "value": "EU" } ], "transitions": [ { "to": "done" } ] },
    "contractor_setup": { "type": "task.transform", "assign": [ { "path": "vars.contract", "value": true } ], "transitions": [ { "to": "done" } ] },
    "manual_review": { "type": "approval", "role": "hr_manager", "output": "vars.approval", "transitions": [ { "when": "vars.approval.decision == 'approve'", "to": "us_setup" }, { "default": true, "to": "fail" } ] },
    "done": { "type": "succeed", "output": { "employeeId": "${employee.id}", "region": "${vars.region}" } },
    "fail": { "type": "fail", "error": { "code": "ONBOARDING_FAILED", "message": "..." } }
  }
}
```

---

## 11) Execution semantics (engine contract)

* **Single writer per run**: the engine advances one node at a time (though `parallel` and `foreach` allow concurrent *sub‑work*).
* **Atomic commit**: each node’s effects (events, snapshot, timers, outbox intents) commit in one ACID transaction.
* **Deterministic decisions**: routers pin choices; replays produce identical outcomes.
* **Durable timers & signals**: survive restarts; at‑least‑once delivery with dedupe.
* **Suspension/Resume/Drain/Cancel/Terminate** available (outside DSL) with well‑defined policies.

---

## 12) JSON Schema (core outlines)

> Below are **concise** schemas to validate DSL shape. (They’re intentionally compact to stay readable; production schemas should expand with full constraints.)

### 12.1 Workflow (root)

```json
{
  "$id": "https://example.com/workflow.schema.json",
  "type": "object",
  "required": ["dsl_version","name","version","start","nodes"],
  "properties": {
    "dsl_version": { "type": "string", "enum": ["1.0"] },
    "name": { "type": "string" },
    "version": { "type": "string" },
    "metadata": { "type": "object" },
    "input_schema": { "type": "object" },
    "output_schema": { "type": "object" },
    "variables": { "type": "object" },
    "policies": { "type": "object" },
    "connectors": { "type": "object" },
    "route_tables": { "type": "object" },
    "storage_bindings": { "type": "object" },
    "start": { "type": "string" },
    "end_states": { "type": "array", "items": { "type": "string" } },
    "nodes": { "$ref": "#/$defs/Nodes" }
  },
  "$defs": {
    "Nodes": {
      "type": "object",
      "additionalProperties": { "$ref": "#/$defs/Node" },
      "minProperties": 1
    },
    "Node": {
      "type": "object",
      "required": ["type"],
      "properties": {
        "id": { "type": "string" },
        "type": { "type": "string" },
        "name": { "type": "string" },
        "timeout": { "type": "string" },
        "retry": { "type": "object" },
        "idempotency_key": { "type": "string" },
        "semaphore": { "type": "object" },
        "audit": { "type": "object" },
        "on_error": { "$ref": "#/$defs/Edge" },
        "transitions": { "type": "array", "items": { "$ref": "#/$defs/Edge" } }
      },
      "allOf": [
        { "if": { "properties": { "type": { "const": "task.service" } } }, "then": { "$ref": "#/$defs/TaskService" } },
        { "if": { "properties": { "type": { "const": "task.transform" } } }, "then": { "$ref": "#/$defs/TaskTransform" } },
        { "if": { "properties": { "type": { "const": "router" } } }, "then": { "$ref": "#/$defs/Router" } },
        { "if": { "properties": { "type": { "const": "choice" } } }, "then": { "$ref": "#/$defs/Choice" } },
        { "if": { "properties": { "type": { "const": "parallel" } } }, "then": { "$ref": "#/$defs/Parallel" } },
        { "if": { "properties": { "type": { "const": "foreach" } } }, "then": { "$ref": "#/$defs/Foreach" } },
        { "if": { "properties": { "type": { "const": "wait.timer" } } }, "then": { "$ref": "#/$defs/WaitTimer" } },
        { "if": { "properties": { "type": { "const": "wait.signal" } } }, "then": { "$ref": "#/$defs/WaitSignal" } },
        { "if": { "properties": { "type": { "const": "approval" } } }, "then": { "$ref": "#/$defs/Approval" } },
        { "if": { "properties": { "type": { "const": "storage" } } }, "then": { "$ref": "#/$defs/Storage" } },
        { "if": { "properties": { "type": { "const": "emit.event" } } }, "then": { "$ref": "#/$defs/EmitEvent" } },
        { "if": { "properties": { "type": { "const": "subflow.call" } } }, "then": { "$ref": "#/$defs/SubflowCall" } },
        { "if": { "properties": { "type": { "const": "subflow.spawn" } } }, "then": { "$ref": "#/$defs/SubflowSpawn" } },
        { "if": { "properties": { "type": { "const": "succeed" } } }, "then": { "$ref": "#/$defs/Succeed" } },
        { "if": { "properties": { "type": { "const": "fail" } } }, "then": { "$ref": "#/$defs/Fail" } }
      ]
    },
    "Edge": {
      "type": "object",
      "properties": {
        "when": { "type": "string" },
        "to": { "type": "string" },
        "default": { "type": "boolean" },
        "handoff": { "type": "object" }
      },
      "oneOf": [
        { "required": ["to"] },
        { "required": ["handoff"] }
      ]
    },

    "TaskService": {
      "type": "object",
      "required": ["connector","operation"],
      "properties": {
        "connector": { "type": "string" },
        "operation": { "type": "string" },
        "endpoint": { "type": "string" },
        "headers": { "type": "object" },
        "query": { "type": "object" },
        "body": { "type": "object" },
        "request": { "type": "object" },
        "variables": { "type": "object" },
        "response_map": { "type": "object" }
      }
    },

    "TaskTransform": {
      "type": "object",
      "properties": {
        "assign": { "type": "array", "items": { "type": "object", "required": ["path","value"] } },
        "remove": { "type": "array", "items": { "type": "string" } },
        "validate": { "type": "object" }
      }
    },

    "Router": {
      "type": "object",
      "required": ["strategy","transitions"],
      "properties": {
        "strategy": { "type": "string", "enum": ["conditions","rules","fuzzy","llm","hybrid"] },
        "inputs": { "type": "object" },
        "route_table": { "type": "string" },
        "thresholds": { "type": "object" },
        "fallback": { "type": "object" },
        "rules": { "type": "object" },
        "fuzzy": { "type": "object" },
        "llm": { "type": "object" }
      }
    },

    "Choice": {
      "type": "object",
      "required": ["cases"],
      "properties": {
        "cases": { "type": "array", "items": { "$ref": "#/$defs/Edge" } },
        "default": { "$ref": "#/$defs/Edge" }
      }
    },

    "Parallel": {
      "type": "object",
      "required": ["branches","join","next"],
      "properties": {
        "branches": { "type": "array", "items": { "type": "object", "required": ["sequence"], "properties": { "name": { "type": "string" }, "sequence": { "type": "array", "items": { "type": "string" } } } } },
        "join": { "type": "object" },
        "next": { "type": "string" }
      }
    },

    "Foreach": {
      "type": "object",
      "required": ["items","sequence"],
      "properties": {
        "items": { "type": "string" },
        "item_var": { "type": "string" },
        "sequence": { "type": "array", "items": { "type": "string" } },
        "concurrency": { "type": "integer" },
        "accumulator": { "type": "object" },
        "next": { "type": "string" }
      }
    },

    "WaitTimer": {
      "type": "object",
      "required": ["mode"],
      "properties": {
        "mode": { "type": "string", "enum": ["duration","until","cron"] },
        "duration": { "type": "string" },
        "until": { "type": "string" },
        "cron": { "type": "string" },
        "timer_id": { "type": "string" },
        "on_timeout": { "$ref": "#/$defs/Edge" }
      }
    },

    "WaitSignal": {
      "type": "object",
      "required": ["signal_type","correlation"],
      "properties": {
        "signal_type": { "type": "string" },
        "correlation": { "type": "string" },
        "timeout": { "type": "string" },
        "on_timeout": { "$ref": "#/$defs/Edge" }
      }
    },

    "Approval": {
      "type": "object",
      "required": ["role"],
      "properties": {
        "role": { "type": "string" },
        "quorum": { "type": "object" },
        "sla": { "type": "object" },
        "form": { "type": "object" },
        "output": { "type": "string" }
      }
    },

    "Storage": {
      "type": "object",
      "required": ["store","op"],
      "properties": {
        "store": { "type": "string", "enum": ["kv","doc","sql","object"] },
        "op": { "type": "string" },
        "namespace": { "type": "string" },
        "key": { "type": "string" },
        "value": { "type": "object" },
        "statement": { "type": "string" },
        "params": { "type": "object" },
        "filter": { "type": "object" },
        "update": { "type": "object" },
        "result_map": { "type": "object" }
      }
    },

    "EmitEvent": {
      "type": "object",
      "required": ["name"],
      "properties": {
        "name": { "type": "string" },
        "payload": { "type": "object" },
        "outbound": { "type": "object" }
      }
    },

    "SubflowCall": {
      "type": "object",
      "required": ["workflow_type"],
      "properties": {
        "workflow_type": { "type": "string" },
        "pin_version": { "type": "string" },
        "input": { "type": "object" },
        "map_result_to": { "type": "string" }
      }
    },

    "SubflowSpawn": {
      "type": "object",
      "required": ["workflow_type"],
      "properties": {
        "workflow_type": { "type": "string" },
        "pin_version": { "type": "string" },
        "input": { "type": "object" }
      }
    },

    "Succeed": { "type": "object", "properties": { "output": { "type": "object" } } },
    "Fail": { "type": "object", "properties": { "error": { "type": "object" } } }
  }
}
```

---

## 13) Linter rules (publish‑time checks)

* All `to` targets exist; at most one `default` per transition set.
* `router` has valid `route_table`; all `route` labels used exist; **fallback** present.
* `parallel.join.policy` consistent with branches; `quorum.count` ≤ branches.
* `foreach.items` evaluates to an array; `concurrency` reasonable.
* `approval` has at least one outcome in `transitions`.
* No cycles without progress (choice/router oscillation).
* No side‑effecting nodes in `parallel` that lack compensations (engine registry).
* No PII in LLM inputs if `policies.enforcement.deny_on_pii_in_llm`.

---

## 14) Operational notes (engine behaviors that make DSL work for all customers)

* **Idempotency**: engine injects keys and dedupes inbound/outbound; connectors must tolerate duplicates.
* **Storage**: logical stores bound to tenant/region per deployment; data residency honored automatically.
* **Observability**: every node emits structured events; routers emit `RouteEvaluated` and `RouteSelected`.
* **Suspend/Resume/Drain/Cancel/Terminate**: outside DSL, but all nodes respect fencing and buffering policies.
* **Versioning**: workflows and route tables are versioned; in‑flight runs remain on their original version unless migrated.

---

### Final remarks

* The **node palette** is lean: service call, transform, router/choice, parallel, foreach, timers, signals, approvals, storage, subflows, events, succeed/fail.
* The **edges** are uniform and powerful (conditions + handoffs).
* **Expressions** unify dynamic behavior—no customer code.
* **Connectors**, **route tables**, and **policies** let you customize deeply per tenant without touching the engine.

If you’d like, I can produce:

* a **reference DSL sample** for your onboarding use case using this spec (fully wired),
* a **linter checklist** generated from the schema, and
* a **migration guide** from your current JSON to DSL v1 (mapping table).


yep—super simple, fits cleanly into DSL 1.0.

# DSL v1 — Minimal Custom Code Node (JS/Go)

Add **one** node type that carries a tiny program as a text blob (UTF-8 or Base64), plus just enough metadata to run it.

## Node: `code.inline`

### Shape (fields)

```json
{
  "id": "any_unique_id",
  "type": "code.inline",

  "lang": "js",                      // "js" or "go"
  "entry": "main",                   // optional; default "main"

  "encoding": "utf8",                // "utf8" (default) or "base64"
  "code": "<program text or base64>",

  "in": "${...}",                    // expression -> JSON value passed to main(input, params)
  "params": { },                     // optional JSON constants (passed as 2nd arg)
  "out": "vars.somewhere",           // JSON path to store return value

  "meta": {                          // optional helpful metadata
    "name": "FilterHasSickDays",
    "version": "1.0.0",
    "author": "hr-platform",
    "description": "Return employees with sickDays >= minDays",
    "tags": ["example","filter"]
  },

  "timeout": "PT200MS",              // optional ISO-8601; defaults apply if omitted
  "on_error": { "to": "some_fallback" },
  "transitions": [ { "to": "next_node" } ]
}
```

### Execution contract (deterministic & sandboxed)

* Engine evaluates `in` → `input`.
* Calls **`entry(input, params)`** in a sandbox (no network/FS/env; deterministic time/rand).
* Must return JSON-serializable value → written to `out`.
* Throw/compile error/timeout ⇒ node fails → `on_error` (if present) or workflow error.

**Expected entry signatures**

* **JavaScript (`lang: "js"`)**
  `function main(input, params) { /* return JSON */ }`
* **Go (`lang: "go"`)**
  `func Main(inputJSON []byte, paramsJSON []byte) ([]byte, error)`
  *(Engine compiles/execs safely under the hood; you just supply the text.)*

> Optional: if your main document defines shared `types`, you may add `"in_type"` / `"out_type"` strings to validate I/O at runtime. If not, omit them.

---

## Examples

### A) UTF-8 JS example (filter employees with sick days)

```json
{
  "id": "filter_sick_days",
  "type": "code.inline",
  "lang": "js",
  "encoding": "utf8",
  "entry": "main",
  "code": "function main(input, params){ const min=(params&&params.minDays)||1; if(!Array.isArray(input)) throw new Error('input must be array'); return input.filter(e=>Number.isInteger(e?.sickDays)&&e.sickDays>=min); }",
  "in": "${vars.employees}",
  "params": { "minDays": 1 },
  "out": "vars.eligibleEmployees",
  "timeout": "PT100MS",
  "transitions": [{ "to": "next_step" }]
}
```

### B) Base64 JS example (same program, base64 payload)

```json
{
  "id": "filter_sick_days_b64",
  "type": "code.inline",
  "lang": "js",
  "encoding": "base64",
  "code": "ZnVuY3Rpb24gbWFpbihpbnB1dCwgcGFyYW1zKXsgY29uc3QgbWluPShwYXJhbXMmJnBhcmFtcy5taW5EYXlzKXx8MTsgaWYoIUFycmF5LmlzQXJyYXkoaW5wdXQpKSB0aHJvdyBuZXcgRXJyb3IoJ2lucHV0IG11c3QgYmUgYXJyYXknKTsgcmV0dXJuIGlucHV0LmZpbHRlcihlPT4gTnVtYmVyLmlzSW50ZWdlcihlPy5zaWNrRGF5cykmJmUuc2lja0RheXM+PW1pbik7IH0=",
  "in": "${vars.employees}",
  "params": { "minDays": 2 },
  "out": "vars.eligibleEmployees",
  "transitions": [{ "to": "next_step" }]
}
```

---

## JSON Schema add-on (merge into your DSL 1.0 schema)

```json
{
  "if": { "properties": { "type": { "const": "code.inline" } }, "required": ["type"] },
  "then": {
    "type": "object",
    "required": ["lang","code","in","out"],
    "properties": {
      "lang": { "type": "string", "enum": ["js","go"] },
      "entry": { "type": "string", "default": "main" },
      "encoding": { "type": "string", "enum": ["utf8","base64"], "default": "utf8" },
      "code": { "type": "string" },
      "in": { "type": "string" },
      "params": { "type": "object" },
      "out": { "type": "string" },
      "in_type": { "type": "string" },
      "out_type": { "type": "string" },
      "meta": {
        "type": "object",
        "properties": {
          "name": { "type": "string" },
          "version": { "type": "string" },
          "author": { "type": "string" },
          "description": { "type": "string" },
          "tags": { "type": "array", "items": { "type": "string" } }
        },
        "additionalProperties": false
      },
      "timeout": { "type": "string" },
      "on_error": { "$ref": "#/$defs/Edge" },
      "transitions": { "type": "array", "items": { "$ref": "#/$defs/Edge" } }
    },
    "additionalProperties": true
  }
}
```

# Codeflux Agent Instructions

## Scope

These instructions apply to the entire Codeflux repository unless a more deeply nested `AGENTS.md` supplies narrower instructions for its subtree.

## Who decides what

| Question | Authority |
| --- | --- |
| **What** gets built — intent, architecture, experiments, gates, scope | `docs/plan.md` |
| **In what order** — dependencies, completion | `TODOS.md` |
| **How** — every rule in this file | `AGENTS.md` |
| **What already exists** | Source, tests, protobuf definitions, migrations |

Where they disagree about implemented behaviour, the source wins. Where they disagree about intent, the plan wins.

Do not invent commands, packages, schemas, or capabilities merely because the plan anticipates them. The repository may still be in an earlier milestone.

## Tracked Agent Configuration

`.claude/` is committed, so every agent working here starts from the same setup rather than whatever its operator has locally. `CLAUDE.md` stays a thin pointer to this file — a repository lint rule enforces that — so the detail lives here.

| Path | What it is |
| --- | --- |
| `.claude/settings.json` | Permissions. The `codeflux-dev` gates are pre-allowed. `run-live`, reading `.env`, and pushing to `main` are denied. |
| `.claude/agents/plan-locator.md` | Finds the governing `docs/plan.md` section and `TODOS.md` ID; reports in scope, deferred, or unplanned. |
| `.claude/agents/gate-runner.md` | Runs the gates and returns a triaged failure list rather than raw log output. |
| `.claude/agents/ledger-scribe.md` | Plans the commit split and drafts the `CHANGELOG` and `DEVLOG` entries a commit requires. |
| `.claude/skills/karpathy-guidelines/` | Vendored, pinned at `2c60614`. MIT by its own frontmatter; the upstream repository has no `LICENSE`, so nothing else from it may be copied. |
| `.claude/skills/frontend-design/` | Vendored verbatim with its `LICENSE.txt`, Apache-2.0. Required reading before building or reshaping a surface. |

The `run-live` denial is the one worth knowing: it is the only command that reaches a provider and spends money, and it is excluded from every suite.

## Branches

Work branches from `dev` and returns to it through a pull request. `dev` is the default branch and its CI is allowed to be red. `main` takes only `dev`, only through a pull request, and only with every check green; it refuses a direct push from everyone, including the repository owner. See `.github/CONTRIBUTING.md`.

## Task Lifecycle

One loop governs every non-trivial change. The rest of this file is the detail behind its steps.

**Before you start**

1. Read this file, and any narrower `AGENTS.md` below the files you will touch.
2. Read `docs/plan.md` §0, Linear Concept and Build Order, then the sections governing your change.
3. Find the governing milestone and task IDs in `TODOS.md`. If neither a plan section nor a task exists, stop and add the task first.
4. Inspect the source, tests, migrations, generated inputs, and Git state actually in scope.

**While you work**

5. Start the `DEVLOG` entry now, while doing the work, not from memory afterwards. State material assumptions as you make them.
6. Run the narrowest relevant test or reproduction before changing anything.
7. Implement the smallest sufficient source, test, migration, or generated change.
8. Rerun the targeted check, then the broader command the task requires.
9. Run `generate-check`, `lint`, and `artifact-check` when their boundaries are affected.

**Before you report it done**

10. Inspect the final diff and `git status`. Confirm no credential, local database, transient asset, or unrelated edit landed outside `.artifacts/`.
11. Finish the `DEVLOG` entry: outcome, verification, limitations, next safe step.
12. When a commit is authorized, follow the commit rules below — explicit paths, one feature, matching `CHANGELOG` entry, both trailers.
13. Report the verification you actually ran and every remaining limitation.

**Close the tasks you finish.** Whenever you are working from a task list — `TODOS.md`, or an ephemeral list you keep during a session — mark each item complete as it lands, not in a batch at the end. Check the box in the same commit as the work it describes, and record the evidence beneath it: the identifier, the test that proves it, the file that now exists. A finished change sitting under an unchecked box is indistinguishable from work nobody started, and the next agent will either redo it or route around it.

**But never mark an item complete until its output exists and its verification passes.** A model stopping is not completion. The two rules are one rule: the list must say what is true, which means neither leaving finished work open nor closing work that is not finished.

If you finish something the list does not contain, add the item and check it in the same commit, so the record still matches the work.

## Project Mission

Codeflux is a local-first coding-agent platform for hobbyists and independent developers. It aims to make accepted past work reduce the cost of future work while optimizing in this order:

1. correctness;
2. speed;
3. monetary and token cost.

The primary interface is a GoWebComponents v5 chat thread with a task-scoped graph for program structure, live execution, and correctness evidence. The local Go coordinator, worker processes, Git worktrees, provider adapters, and SQLite database form the runtime.

## Non-Negotiable Product Boundaries

- Ordinary repository source code remains the default working medium.
- The first useful product is code-first; deep graph verification must pass its experiment gate.
- The prototype graph is optional, task-scoped, derived, and read-only.
- SQLite is the sole authoritative store for Codeflux-managed threads, messages, tasks, events, graphs, atoms, vectors, evidence, budgets, and learned artifacts.
- Do not create JSON, YAML, Markdown, or other sidecar files for Codeflux runtime artifacts.
- Source code, protobuf definitions, SQL migrations, tests, build configuration, and project documentation remain normal Git-tracked files.
- Provider credentials belong in the operating-system credential store, never SQLite.
- Workers must not receive raw provider credentials.
- Author frontend behavior and presentation in Go with GoWebComponents v5. Do not
  add or maintain handwritten JavaScript, TypeScript, HTML, or CSS source.
- The initial routing policy remains fixed until baseline telemetry and shadow evaluation authorize adaptation.
- Vector similarity may discover candidates; it never establishes compatibility, validity, assurance, or permission.
- External-system behavior remains external. Do not describe contract-checked behavior as fully proven.
- User authority, hard budgets, validation requirements, and recovery uncertainty must remain visible.
- No silent provider switching, permission expansion, validation skipping, or unsafe recovery fallback is allowed.

## File Creation and Build Artifact Rules

### Markdown Files Require Explicit User Direction

- Never create a new Markdown file unless the user explicitly requests that specific file.
- A plan, TODO, convention, template, framework default, inferred documentation need, or agent preference is not authorization to create a Markdown file.
- Do not create unsolicited `README.md`, `CHANGELOG.md`, design notes, implementation plans, reports, handoffs, benchmark summaries, nested `AGENTS.md`, nested `CLAUDE.md`, or other `*.md` files.
- Existing Markdown files may be edited only when the user's request or an authorized implementation task requires that edit.
- If useful documentation has no explicitly authorized Markdown destination, report it in the task response or store product runtime knowledge in SQLite as designed; do not create a sidecar document.
- An explicit request to create one Markdown file authorizes only that named file, not additional related Markdown files.
- These were explicitly requested and are authorized; do not delete them as unauthorized clutter: the root `README.md`; `.github/SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `PULL_REQUEST_TEMPLATE.md`; and everything under `.claude/`. Adding a *new* file still requires its own explicit request.
- Vendored third-party files under `.claude/skills/` are copies, not repository prose. Keep them verbatim, keep their licence file beside them, and record any change prominently — `frontend-design` is Apache-2.0 and ships with its `LICENSE.txt`.

### One Repository-Local Artifact Root

- `.artifacts/` is the sole repository-local root for disposable build, test, packaging, benchmark, profiling, and diagnostic output.
- Put compiled binaries, WASM binaries, object files, packaged archives, coverage output, profiles, benchmark output, test-failure captures, temporary development databases, disposable logs, generated comparison trees, and staging files under `.artifacts/`.
- Use purpose-specific children such as `.artifacts/bin/`, `.artifacts/wasm/`, `.artifacts/coverage/`, `.artifacts/package/`, `.artifacts/bench/`, `.artifacts/test-failures/`, `.artifacts/db/`, and `.artifacts/tmp/`; do not create parallel artifact roots.
- `.artifacts/` must remain ignored by Git. Never force-add it or use a tracked placeholder to keep it present.
- Source files and generated source that are intentionally reviewed and committed are not build artifacts; keep them in their declared source locations.
- User-selected production data outside the repository and operating-system or Go toolchain caches are not moved into `.artifacts/`. Any disposable database or cache created inside this repository must be under `.artifacts/`.
- Every development, build, test, benchmark, package, and diagnostic command must either write repository-local disposable output beneath `.artifacts/` or use an external operating-system/toolchain cache.
- A command that would emit a repository-local artifact elsewhere must fail before writing and explain the permitted destination.
- Cleanup operations may remove only a resolved, validated child of `.artifacts/`; they must never broaden cleanup to the repository root.
- CI may upload selected redacted files from `.artifacts/`, but it must not create a second in-repository staging directory.

## Change Ledger, Development Ledger, and Commit Discipline

The tracked extensionless files `CHANGELOG` and `DEVLOG` are the repository's authorized development ledgers. They are governance records, not disposable runtime or build logs, so they remain outside `.artifacts/`.

### CHANGELOG

- Keep `CHANGELOG` synchronized with every authorized commit.
- Give each entry a stable identifier in the form `CL-YYYYMMDD-NNN`.
- Add the entry in the same commit as the change it describes.
- Add a matching `Change-Log: CL-YYYYMMDD-NNN` trailer to the commit message. The trailer binds the entry to the commit without trying to embed a commit hash inside the commit that creates it, which is impossible: writing the hash in would change it.
- A reviewer must be able to resolve the commit later with `git log --grep="Change-Log: CL-YYYYMMDD-NNN"`.
- Carry the real hash in a `Commit:` field, back-filled. Write the entry with `Commit: pending`, commit it, then immediately fill in the short hash. Leave that edit uncommitted; the next feature commit sweeps it up, so it never costs a commit of its own. The newest entry reads `pending` until the next feature lands, and the last entry of a session stays pending until the next session — both are expected, not omissions.
- The trailer is authoritative and the hash is a convenience. A rebase, squash, or cherry-pick changes the hash and leaves the field stale; when the two disagree, believe the trailer.
- Record the date, change type, governing TODO or request, concise outcome, affected behavior, compatibility or migration impact, and verification.
- Describe completed outcomes, not intentions, raw work notes, or marketing claims.
- Record internal, test-only, documentation, and build changes as such; do not omit them merely because they are not user-facing.
- Never rewrite or delete a released entry. Correct it with a later entry that references the superseded Change-ID.

### DEVLOG

- Keep `DEVLOG` as the chronological implementation record.
- Give each implementation entry a stable identifier in the form `DL-YYYYMMDD-NNN`.
- Start or update the entry while doing the work, not from memory after the work is finished.
- Record the goal, governing TODO or user request, assumptions, important decisions, files and schemas changed, validation run, failures encountered, discarded approaches, remaining limitations, and next safe step.
- Link the final implementation entry to its `Change-ID` when the work is committed.
- Add a matching `Dev-Log: DL-YYYYMMDD-NNN` trailer to the commit message.
- Preserve useful failed attempts and reversals when they explain the final implementation or prevent repetition; do not turn the log into a token-by-token transcript.
- Never place credentials, private model reasoning, unredacted tool output, customer data, or other secrets in either ledger.

### Atomic and Focused Commits

- These rules do not authorize creating a commit. Commit only when the user or an authorized repository workflow requests it.
- Every commit must represent one coherent, independently reviewable change with one clear reason to exist.

#### Feature-atomic, not session-atomic

A commit is scoped to **one feature**, not to one work session and not to everything the working tree happens to contain. "Commit my work" is an instruction to commit, not permission to collapse a session into a single commit.

- **The test:** can the subject line name the observable change without using "and"? If the honest subject is "add X and fix Y and reformat Z", that is three commits.
- **A feature is the smallest change that leaves the tree coherent.** Its production code, its narrow tests, its migration, its regenerated source, and its two ledger entries belong together, because separating them leaves a state that is misleading or does not build.
- **Split before staging, not after.** Decide the commit boundaries first, then stage explicit paths for each. Discovering mid-commit that the diff has two purposes means the split was skipped.
- Order the sequence so that **every commit is individually sound**. A dependency lands before the code that imports it. Prefer a compiling intermediate to a tidy final state reached through broken ones.
- Each commit gets **its own** `CL-` and `DL-` pair. Two features never share an entry, and one feature never gets two.
- If a change genuinely cannot be split — a rename that touches forty files, a generated artifact — say so in the entry rather than splitting it artificially into commits that do not build.
- A **checkpoint commit**, collapsing unrelated work into one, requires the user to ask for it explicitly and must be labelled as such in its subject and its `CHANGELOG` entry, including what is failing at that revision.

#### Working-tree hygiene when other lanes are active

- Commit only what your own task changed. Files another lane left modified or untracked are not yours to sweep in, even when they sit in the same directory.
- `git add -A` and `git commit -a` are prohibited. Stage explicit paths, or hunks when one file carries two purposes.
- Before staging, list what changed and account for every path. A path you cannot explain belongs to someone else.
- Stage explicit paths or hunks. Do not use broad staging to absorb unrelated user changes.
- Keep feature code, its narrow tests, required migration, generated source, and directly affected ledger entries together when separating them would leave a misleading or broken state.
- Separate unrelated refactors, formatting sweeps, dependency upgrades, documentation changes, and behavior changes into different commits.
- Do not mix opportunistic cleanup with the requested change.
- A commit must not contain disposable artifacts, secrets, local databases, editor state, or unrelated worktree content.
- Run the narrow required verification before committing. Do not knowingly commit a failing intermediate state unless the user explicitly requests a clearly labeled checkpoint commit.
- Use an imperative, specific subject that describes the observable change. Avoid subjects such as `updates`, `misc`, `cleanup`, `fixes`, or `WIP`.
- Include the governing TODO IDs when available and the matching `Change-Log` and `Dev-Log` trailers.
- Before committing, inspect the staged diff and confirm it has one purpose. After committing, inspect the commit and verify its paths, message, trailers, and test evidence.
- If a change cannot be explained as one focused unit, split the work and its ledger entries before committing.
- Never amend, squash, reorder, or rewrite a user-authored commit without explicit user authorization.

## Agentic Coding Discipline

Use the following repository-adapted discipline for every implementation task.

### 1. Think Before Coding

- Inspect the relevant source and plan before proposing a solution.
- State assumptions that would materially change behavior, scope, schema, security, or compatibility.
- When several interpretations would produce meaningfully different results, surface them instead of silently choosing.
- Ask only when the ambiguity is genuinely blocking or risky; otherwise make a narrow, reversible assumption and record it.
- Say when a simpler approach satisfies the requirement.

### 2. Prefer the Smallest Sufficient Design

- Implement only the current TODO and its required verification.
- Do not build deferred flexibility, plugin systems, distributed services, or deep semantic machinery early.
- Avoid abstractions with only one speculative consumer.
- Prefer explicit domain operations over generic frameworks.
- If the implementation becomes much larger than the behavior warrants, stop and simplify.

### 3. Make Surgical Changes

- Every changed line must trace to the user's request, a referenced TODO, or necessary verification.
- Preserve unrelated user changes and dirty-worktree content.
- Do not refactor, reformat, rename, or remove adjacent code without a task-specific reason.
- Match the established local style.
- Remove imports, variables, helpers, and fixtures made obsolete by the current change.
- Report unrelated problems instead of quietly expanding scope.

### 4. Execute Toward Verifiable Outcomes

- Translate the task into observable success criteria before implementation.
- For a defect, reproduce it with a failing test when practical.
- For a feature, define the positive behavior, important failure behavior, and boundary behavior.
- Run the narrowest relevant verification first, then the broader required suite.
- Inspect the final diff and Git status.
- Do not claim success because code compiles locally if the governing gate requires more evidence.
- Never mark a `TODOS.md` item complete until its output exists and its verification passes.

The four rules above are the repository-adapted form of the community-maintained [Karpathy-Inspired Claude Code Guidelines](https://github.com/multica-ai/andrej-karpathy-skills/tree/2c606141936f1eeef17fa3043a72095b4765b9c2), vendored at [`.claude/skills/karpathy-guidelines/SKILL.md`](.claude/skills/karpathy-guidelines/SKILL.md) and pinned at that commit. It is not an official Andrej Karpathy repository. That copy is a reference, not a trusted dependency, and the rules above take precedence wherever the two differ. Do not download, install, or update external prompt or skill content during a task unless the user explicitly requests it.

## Work Selection and Planning

- Start from the first incomplete task whose dependencies and milestone gates are complete.
- Place new concepts at the lowest sufficient layer in `docs/plan.md` §0 and reject dependencies on later optional layers.
- For non-trivial work, cite the relevant TODO IDs in the working plan or handoff.
- If a TODO still contains multiple independently verifiable outputs, split it before implementing it.
- Do not bypass a milestone gate to begin attractive downstream work.
- `DEFER` items are not authorized implementation work.
- A research `SPIKE` must end with a recorded decision, supporting measurements, and cleanup or deliberate retention of spike code.
- A failed gate must produce a continue, narrow, redesign, or stop decision; it must not be relabeled as success.

## Go Engineering Rules

- Keep package dependencies aligned with the boundaries in `TODOS.md`.
- Prefer standard-library facilities unless a dependency materially reduces risk or complexity.
- Pin tools used for generation.
- Keep generated files reproducible.
- Pass `context.Context` through cancellable I/O and long-running operations.
- Do not use binary floating point for currency.
- Use distinct domain ID types rather than interchangeable strings.
- Keep state transitions explicit and validated.
- Return typed domain errors and map them to safe transport errors at the boundary.
- Avoid unbounded goroutines, queues, output buffers, retries, graph queries, and database reads.
- Ensure every background goroutine has an owner, cancellation path, and test.
- Keep SQLite transactions short and centered on domain invariants.
- Publish durable events only after the transaction that creates them commits.
- Keep transport handlers limited to authentication, validation, conversion, application-service delegation, and safe error mapping.
- For every mutating application function, define authority, idempotency, expected revision, transaction boundary, durable events, external effects, cancellation, and typed failures.
- Persist external-effect intent before execution when replay or ambiguity matters, then persist the attributable outcome.

## Implemented Development Commands

Run repository development operations through the cross-platform Go helper:

```text
go run ./cmd/codeflux-dev help
go run ./cmd/codeflux-dev <command> --help
```

`help` lists the current commands. It is the only accurate list — a copy written into this file goes stale the moment a command is added, and a stale list is worse than none because it reads as authoritative.

What `help` will not tell you:

Use `bootstrap` before lint or generation on a fresh clone. It selects the
patched Go toolchain and installs pinned repository tools beneath `.artifacts`.
`generate` is the sole normal writer for generated source. `migration-check`
validates ordering, embedding, and checksums; it does not apply an application
schema before M03. `benchmark` retains measured output beneath
`.artifacts/bench`. `test-race` is unavailable on Windows ARM64 and runs in the
declared Ubuntu AMD64 CI job. Commands labeled `skeleton` or `gated` by help
return exit 3 until their owning milestone implements the subsystem.

Every command accepts `--root`. A repository-local root must be a child of
`.artifacts`; an explicit external root is never selected or deleted
implicitly. Use `--json` only when command help declares machine-readable
output.

### Everyone Works in `dev`, Nobody Clobbers

**Do not create a Git worktree for your own work.** Every agent works directly in the `dev` checkout, and several will be in it at once. Do not `git worktree add`, and do not clone a second copy to escape the others.

*(This says nothing about the per-task worktrees the product itself creates in `internal/gitwork`. Those are a product feature and are unaffected.)*

Working alongside other agents is therefore a skill the repository requires, not an edge case. The whole discipline reduces to one thing: **touch only what your task changed, and leave everything else exactly as you found it.**

- **Stage explicit paths. Never `git add -A` or `git commit -a`.** Before staging, list what changed and account for every path. A path you cannot explain belongs to someone else.
- **One file can hold two lanes' work.** `CHANGELOG`, `DEVLOG`, and `TODOS.md` are edited by everyone. When a shared file carries someone else's uncommitted work alongside yours, stage only your hunk — build the blob by hand if you must — and leave theirs in the working tree.
- **Re-read before you write.** Sequential identifiers, file tails, and section anchors move under you. Read the current state of `CHANGELOG` and `DEVLOG` immediately before writing an entry, not at the start of the task; identifiers have collided this way already.
- **Never revert, reformat, or "fix" a file you did not change.** If a gate names a file you did not touch, that is another lane mid-edit. Say so and coordinate. The single exception is a mechanical `gofmt` the repository would demand anyway — apply it, leave it unstaged, and it remains theirs to commit.
- **Expect a broken tree that is not your fault.** A file may be syntactically incomplete because someone is writing it right now. Wait, or work elsewhere. Do not repair it to get your own commit through.
- **Never rewrite shared history.** No rebase, amend, squash, or force-push of anything another agent may have. `main` and `dev` both refuse force-pushes regardless.
- **The dev server is shared too.** It binds the fixed port `127.0.0.1:47311`, so only one agent runs it at a time. Check whether one is already up and reuse it; if you genuinely need a second, pass `--address 127.0.0.1:PORT` and stop it afterwards.

The cost of this is real and accepted: a repository-wide gate can be red for reasons that have nothing to do with you, and a push carries whatever else is unpushed. That is the trade for a single shared branch. It is not a reason to route around a gate.

### Stop What You Start

**Every process you start, you stop before the task ends.** This repository has
repeatedly accumulated forty to sixty orphaned processes in a single session,
because each agent starts a server, finishes with it, and leaves it running.
Nothing reaps them, and the cost is real: exhausted memory, a machine that
crawls, and the occupied ports that make the next run fail for reasons unrelated
to the change.

- **The dev server binds a fixed port, `127.0.0.1:47311`.** A second one cannot
  start while the first is alive, and the bind error will say so. That is
  deliberate — an ephemeral port let every stale server coexist invisibly, which
  is precisely how they reached sixty.
- **Before starting a server, check whether yours is already running.** Reuse it.
  Starting a fresh one because it is easier than checking is the whole problem.
- **Do not leave `go run`, `go test`, `gopls`, `staticcheck`, Playwright,
  Chromium, or a worker detached** past the step that needed it. A backgrounded
  process is yours until you stop it.
- **Prefer foreground and short-lived.** Run the thing, read the output, let it
  exit. Background it only when you genuinely need it alive across steps, and
  then stop it explicitly when you no longer do.
- **Stop by port or PID, never broadly.** Kill the process holding `47311` or
  the PID you started; never a blanket kill of every `go`, `node`, or browser
  process, which will take down the user's own work alongside yours.
- **Report what you left running.** If something must outlive the task, say so
  explicitly, say why, and say how to stop it. Silence here is how the count
  climbs.

## Atom Naming Style

Atom names must preserve domain context when displayed without surrounding source. A somewhat long, precise name is preferred over a short generic name that forces a human or retrieval system to reopen the implementation.

Use this conceptual grammar:

```text
<Verb><DomainObject><ImportantQualifier><ObservableOutcome>
```

Not every name needs all four parts, but it must include enough of them to distinguish the atom from realistic alternatives.

Prefer names such as:

```go
DerivePaymentAttemptIdempotencyKey
ReserveAccountFundsUntilAuthorizationExpires
ReconcileAmbiguousGatewayChargeOutcome
LoadRepositorySymbolsAtGitRevision
ValidateTaskBudgetBeforeModelRequest
PersistSessionEventWithMonotonicSequence
```

Avoid names such as:

```go
Process
Handle
Execute
RunTask
DoPayment
CheckData
UpdateState
AtomHelper
PaymentManager
```

Naming rules:

- Begin with a concrete action verb for executable atoms.
- Name the domain object, not only its Go representation.
- Include a qualifier when it distinguishes business intent, lifecycle stage, identity source, effect boundary, or result from neighboring atoms.
- Include the observable outcome when the action verb alone is ambiguous.
- Prefer full domain words over unexplained abbreviations.
- Use established domain abbreviations only when they are clearer to intended users than the expanded phrase.
- Do not add filler words such as `Helper`, `Utility`, `Manager`, `Processor`, `Handler`, `Thing`, or `Impl`.
- Do not encode atom version, evidence version, assurance level, source line, hash, or temporary implementation mechanism in the name.
- Do not encode a provider or dependency name unless the atom's semantics are genuinely provider-specific rather than an adapter binding.
- Do not claim guarantees in the name that the contract and evidence do not support.
- Keep one canonical Go identifier and one human-readable display name derived from the same semantic phrase.
- Store prior names as searchable aliases after a rename; do not allow two active atoms in the same scope to share a normalized canonical name.
- A rename that preserves semantic identity keeps the stable atom ID, creates a new documentation revision, records an alias, and regenerates affected embeddings.
- A change that alters the atom's semantic identity requires a new atom version or atom identity according to the compatibility rules; do not disguise it as a cosmetic rename.
- Include the canonical name and its word-split normalized phrase in embedding input exactly once. Do not repeat it to inflate similarity.

Before accepting a name, test it in isolation:

1. Can a reviewer tell what domain action it performs?
2. Can the reviewer distinguish it from the nearest plausible atom?
3. Does it avoid promising more than the contract proves?
4. Would it still make sense as a graph-node label and retrieval candidate?
5. Are all included qualifiers semantically important rather than implementation trivia?

## Atom Documentation Style

Every executable, modeled, external-adapter, or graph-native atom realized as Go must have a thorough doc comment. The comment has three jobs:

1. explain the atom well enough for a human reviewer to decide whether it fits a requirement;
2. preserve the natural-language semantics needed to discover it during later retrieval;
3. provide structured text that Codeflux can normalize, revision, store in SQLite, and embed.

The comment does not replace the atom's typed signature, executable applicability predicate, contract, tests, evidence, or version bindings. A comment can help find an atom; it cannot make the atom valid for a task.

Mark each source-authored Go atom declaration with the exact `//codeflux:atom`
directive inside the declaration's doc-comment group. The directive makes atom
admission explicit and gives repository lint a deterministic declaration
boundary; omitting it means the declaration is ordinary Go, not an admitted
Codeflux atom. A reviewed naming exception must use a neighboring
`//codeflux:atom-name-exception <kind>: <non-empty reason>` directive, where
`<kind>` is `action-verb`, `abbreviation <TOKEN>`, `provider-specific`, or
`guarantee-claim`. Exceptions are narrow review records, not blanket bypasses.

Use this shape:

```go
// ReserveFunds reserves an amount against an account without capturing it.
//
//codeflux:atom
// Codeflux atom documentation (schema v1):
//   Purpose:
//     Explain the user or domain outcome this atom provides.
//   Use when:
//     Describe the requirements and operating conditions that make it suitable.
//   Do not use when:
//     Name important near-matches, exclusions, and safer alternatives.
//   Semantics:
//     Describe observable behavior, ordering, invariants, and what "success" means.
//   Inputs:
//     - Describe each input's domain meaning, units, valid range, identity role,
//       sensitivity, and distinction from similar values.
//   Outputs:
//     - Describe each result variant and the facts a caller may rely on.
//   Preconditions:
//     - List facts the caller or environment must establish before invocation.
//   Postconditions:
//     - List facts established on each successful or explicitly modeled outcome.
//   Effects:
//     - Name resources, capabilities, effect identity, cardinality, ordering,
//       and externally visible mutations. Write "None: pure atom" when pure.
//   Failure semantics:
//     - Describe error/result categories, partial work, ambiguity, and which
//       failures are safe or unsafe to retry.
//   Determinism:
//     State what is deterministic and identify time, randomness, concurrency,
//     architecture, dependency, or external-response dependencies.
//   Idempotency and retry:
//     Define logical operation identity, deduplication scope, retry conditions,
//     key lifetime, and behavior after an ambiguous result.
//   Reconciliation and compensation:
//     Describe how uncertain outcomes are checked and what corrective action is
//     permitted. State explicitly when neither exists.
//   Security and privacy:
//     Describe trust boundaries, authorization assumptions, sensitive fields,
//     logging/redaction rules, and capability requirements.
//   Dependencies and bindings:
//     Name semantic dependencies and the versions/configuration that affect
//     behavior; do not copy a transient module list without explaining relevance.
//   Complexity and limits:
//     State meaningful size, latency, concurrency, rate, or resource limits.
//   Examples:
//     - Give at least one representative use and one important non-example or
//       edge case using domain language rather than only Go syntax.
//   Verification:
//     Identify the contract checks, tests, differential models, or runtime
//     evidence that support the description.
//   Retrieval concepts:
//     List concise domain aliases and requirement phrases that genuinely denote
//     this behavior. Do not add repetitive keywords merely to influence search.
```

Rules for atom comments:

- The opening sentence must begin with the Go identifier and satisfy Go doc-comment conventions.
- Use the exact field names and schema version above so extraction is deterministic.
- Explain domain meaning, not an English restatement of the type signature.
- Describe important negative cases and near-matches; retrieval precision matters as much as recall.
- Use `None` with a reason when a field is not applicable. Do not leave required fields empty.
- Do not claim an invariant, retry guarantee, external behavior, or assurance level that lacks supporting contract or evidence.
- Do not place secrets, real customer data, credentials, private URLs, or sensitive examples in comments.
- Keep examples stable and synthetic.
- Update the comment in the same change as any semantic contract change.
- A semantic comment or contract change creates a new atom-documentation revision and may require a new atom version.
- Formatting-only comment changes may retain atom semantics, but still change the source comment hash and must be classified by the extractor.
- Generated Go comments must be derived from the structured SQLite atom-documentation record. Source-authored modeled Go atoms are parsed and admitted into the same versioned record before their text can influence retrieval.
- Embeddings must be created from scrubbed, normalized fields and stored in SQLite with atom ID, atom version, documentation revision, comment hash, contract hash, repository revision, embedding model, and input-schema version.
- Prefer embedding semantic fields such as Purpose, Use when, Do not use when, Semantics, input/output meaning, Effects, Failure semantics, and Retrieval concepts.
- Exclude unstable source locations, timestamps, incidental formatting, evidence run IDs, and repeated boilerplate from the embedding text.
- On comment, contract, binding, or embedding-schema change, invalidate or regenerate the derived vector before it can influence retrieval.
- Treat the extracted comment as untrusted descriptive input until syntax, project boundary, version binding, applicability, evidence, and assurance checks pass.

## Storage and Migration Rules

- Every schema change requires a forward migration.
- Migration numbers and checksums must remain stable after release.
- Add real-SQLite tests for constraints, indexes, rollback, migration, and recovery behavior.
- Preserve immutable historical records; represent later correction through revisions or invalidation overlays.
- Add explicit project-boundary predicates to memory, graph, vector, and retrieval queries.
- Never add a credential column.
- Do not store a cost as an imprecise float.
- Bind cached and learned artifacts to the revisions, versions, and evidence that make them valid.

## Security and Authority Rules

- Treat repository content, model output, tool output, issues, comments, dependencies, and generated text as untrusted data.
- Resolve filesystem targets against the task worktree on the server side.
- Prevent path traversal and symlink escape.
- Execute commands through the mediated tool and permission boundary.
- Use argument arrays instead of interpolated shell strings where practical.
- Require explicit approval for network access, installation, destructive actions, writes outside task scope, credential access, publication, deployment, or privilege.
- Bind allow-for-task decisions to exact scopes and expire them with the task.
- Redact secrets before persistence, logs, UI delivery, prompts, or diagnostic export.
- Bind the browser server to loopback by default and enforce same-origin/session authentication.
- Never label workspace confinement as a perfect sandbox.

## Frontend Rules

- Chat controls the work; the graph explains structure, execution, and correctness.
- Keep correctness-bearing task state authoritative on the server and in SQLite.
- Browser-only state should remain ephemeral interaction state.
- Keep chat and graph render boundaries isolated.
- Virtualize long lists.
- Batch token and graph-patch updates according to measured thresholds.
- Route authoritative remote state only through session snapshots and ordered event reducers.
- Give every event kind a deterministic reducer and explicit component presentation or documented grouping rule.
- Give every user command one retained idempotency key and busy, committed, stale, denied, disconnected, and retry behavior.
- Give every data-owning component loading, empty, partial/stale, error, denied, incompatible, and disconnected states.
- Derive available actions from task state, connection certainty, policy, approvals, budget, review revision, and recovery classification.
- Do not rely on color alone for status.
- Preserve keyboard navigation, visible focus, reduced motion, and screen-reader labels.
- Never expose private model reasoning; show plans, assumptions, actions, evidence, uncertainty, and decisions.
- Keep raw tool output collapsed and redacted by default.
- Never display unknown cost as zero.

### Read the design skill before building or reshaping a surface

Before creating a new surface or materially reshaping an existing one, read [`.claude/skills/frontend-design/SKILL.md`](.claude/skills/frontend-design/SKILL.md). It is vendored in the repository under Apache-2.0 precisely so that any agent can read it, not only ones that load skills automatically.

Take from it the design thinking: ground the work in the subject rather than in a template, make the type hierarchy carry meaning, let structural devices encode something true instead of decorating, spend boldness in one place and keep everything around it quiet, and critique your own work with a screenshot before calling it finished.

**Translate it, do not apply it literally.** The skill talks about palettes, typefaces, and CSS selector specificity because it was written for projects that ship CSS. This repository does not: colour, type, spacing, and motion come from the Go design tokens in `web/frontend/design` and the components in `primitives`, and handwritten CSS remains a product-boundary violation. Where the skill's advice and this repository's boundaries collide, the boundaries win — express the intent through tokens, or extend the token system deliberately.

The skill's own quality floor is the repository's too: responsive, visible keyboard focus, reduced motion respected.

### Frontend changes must be driven in a browser

**If a change touches frontend code, a browser must be driven through it with real clicks and real keystrokes before the work is reported as done.** This is not satisfied by a Go test over a pure projection. A projection test proves the projection; it says nothing about whether a person can actually operate the interface, and every frontend defect that has reached review here was invisible to one.

Mounted checks live in `internal/frontendtest` and drive Playwright. Run them with `go run ./cmd/codeflux-dev test-browser`.

**1. Operate the real control.** Use `.Click()`, `.Fill()`, `.Press()`, `.Keyboard().Press()`, `.Type()`, `.Focus()`, `.SelectOption()` on the control a person would use. Do not reach past the interface to set state directly and then assert the state you just set — that asserts your own assignment, not the component.

**2. Assert the event, the state, and the render — all three.** A handler that fires without changing state, state that changes without re-rendering, and a render that happens on the wrong boundary are three different bugs with the same symptom. Check that the event fired, that the resulting state is what it should be, and that what the user sees reflects it. Where render isolation matters, assert it: a boundary re-rendering that does not own the event is a regression, not a cosmetic detail.

**3. Walk the whole keyboard path.** Tab through every stop in order and confirm focus is visible at each. Activate with Enter and Space, dismiss with Escape, and confirm focus is restored to the control that opened an overlay after it closes. Keyboard is not an accessibility afterthought here; it is a supported way to use the product.

**4. Cover the states the surface can actually reach**, not only the one you built it in: loading, empty, error, truncated, and the long-string case that breaks layouts. The acceptance matrix cross-products routes, bootstrap states, and viewports for exactly this reason — a check that only exercises the default view has tested the easy third of the work.

**5. Look at it.** Take a screenshot and open it. Judge it as a person would: is text legible at its rendered scale rather than merely present in the DOM; does anything overlap, clip, or overflow its container; is spacing consistent with neighbouring surfaces; does the focus ring stay inside the viewport; does motion respect reduced-motion. **Excellent UI/UX quality is part of the deliverable, and it cannot be established from a passing assertion.** A screenshot that was captured but never viewed is not evidence.

**6. Keep the accessibility gates.** Every interactive control has an accessible name, every landmark region has a label, and no state is conveyed by colour alone.

**7. Expect a slow headless renderer.** Existing checks use timeouts from 15s to 120s deliberately. A check that fails intermittently at a short timeout is usually the harness being slow, not the product being broken — raise the timeout, and never paper over it with a fixed sleep, which trades a flake for a slow suite that still flakes.

**Do not report a frontend change as working on the strength of a projection test, a screenshot nobody opened, or a build that compiled.** If the browser check could not be run, say so plainly and say why, rather than reporting the change as verified.

## Testing and Verification

For each changed behavior:

- add or update the narrowest useful automated test;
- include at least one important failure or boundary case;
- use a real temporary SQLite database for repository and migration behavior;
- use temporary Git repositories for worktree and edit behavior;
- use deterministic provider fakes for ordinary tests;
- keep live-provider tests opt-in;
- use fault injection for replay, idempotency, crash, and recovery behavior;
- run security-boundary tests for paths, origins, permissions, redaction, and payload limits;
- drive every frontend change through a real browser, per the section below;
- measure rather than assume graph and streaming performance;
- keep statement coverage at or above the floor before pushing, per the section below.

### Coverage floor

**Statement coverage must be at least 80% before Go changes are pushed to `dev`.** `.githooks/pre-push` measures it and refuses the push below the floor.

It is checked on push rather than on commit because coverage is a property of the branch you are publishing, not of every step along the way. A commit that lowers coverage mid-refactor is fine; pushing it is what makes it everyone else's problem.

- A push carrying no Go change skips the measurement. Documentation, ledger entries, and workflow files have no statements to cover, and blocking them would only teach people to bypass the hook.
- **A failing suite is not a coverage failure and is not reported as one.** The hook refuses rather than measuring, because a percentage taken from a partial run describes only the packages that happened to pass, and that number reads as reassuring while meaning nothing.
- `CODEFLUX_MIN_COVERAGE` overrides the floor for a single run. It exists for bisecting and for measuring where the repository actually stands; lowering it to get a push through is not a use.
- Coverage is a floor, not a target. Statements executed is not behaviour verified — the mutation-score gate is what establishes that the tests can detect a defect, and a package can sit at 90% while testing nothing. Do not write assertions whose purpose is to move the number.

## Documentation Rules

- Update `docs/plan.md` when product intent, architecture, phase order, or a gate changes.
- Update `TODOS.md` when implementation dependencies, atomic work, or completion evidence changes.
- Keep `CLAUDE.md` a thin compatibility entry point; do not duplicate this file into it.
- Link to stable primary sources where practical.
- Label community interpretations accurately.
- Do not claim a planned command or component already exists.
- Prefer concrete paths, commands, schemas, and tests once they are real.

## Appendix: ReserveFlow Dogfood Trial (§28 / M24 only)

These rules apply when executing the final §28 ReserveFlow dogfood trial and Milestone 24 tasks:

- Keep the Codeflux repository and runtime database, ReserveFlow repository and application database, and evaluator repository physically and logically separate.
- Reveal one frozen requirement at a time. Do not inspect, infer from hidden artifacts, or preload future requirements.
- Start every requirement from the prior independently accepted ReserveFlow commit with a fresh forecast, plan, budget, worktree, and episode.
- Do not manually edit ReserveFlow source during an evaluated Codeflux run. If an emergency intervention occurs, record it and mark the run contaminated.
- Do not inspect hidden evaluator tests from Codeflux, its workers, its provider context, or the ReserveFlow worktree.
- Do not advance the accepted commit chain until visible and hidden behavioral acceptance pass.
- Freeze a failure before repairing Codeflux: retain the task, base revision, Codeflux version, event sequence, worktree diff, provider/model, policy, budget, environment, and redacted diagnostics.
- Reproduce a Codeflux-owned defect in the smallest deterministic fixture and add a failing test at the lowest responsible layer.
- State the general failure class before implementing the smallest repair. Never weaken validation, authority, evidence, budgets, recovery, or project isolation to satisfy one benchmark case.
- Rerun the failed ReserveFlow requirement from its original clean base. Verify first with project memory disabled, then rerun the chronological memory-enabled path.
- Test the repair against earlier affected ReserveFlow tasks and an unrelated fixture; reject task-specific prompt patches or fixes that depend on future-requirement knowledge.
- Record every clarification, approval, redirect, workaround, evaluator action, and refinement outcome in the append-only intervention and defect ledgers.
- Compare correctness before speed and cost. Include failed cheap attempts, escalation, manual effort, and contaminated runs in the scorecard.
- Before using an adversarial review agent, freeze its prompt, model, input
  allowlist, output schema, no-edit/no-approval authority, timing, replay budget,
  and cost accounting, then verify its isolation from hidden acceptance.
- The adversarial reviewer is an evaluation-only role, not Codeflux multi-agent
  execution topology. It cannot influence the active evaluated run. Give it
  only the currently revealed requirement, visible tests, versioned plan, diff,
  and redacted run evidence; never give it evaluator source, hidden assertions,
  hidden answers, future requirements, or live authority.
- Record reviewer time, tokens, cost, findings, and resulting interventions in
  the scorecard.
- Treat adversarial critique as a hypothesis with `evidence_strength: none`
  until an executable reproduction and independent acceptance support it. The
  reviewer cannot approve its own proposed prompt or process change.
- Test prompt and process candidates one general invariant at a time. Before
  execution, preregister the exact candidate diff, tuning cohort, primary
  endpoint, minimum effect, repetitions, analysis, stop rule, and
  multiple-comparison treatment while holding model, effort, tools, context
  policy, authority, budget, and acceptance criteria fixed.
- Do not increase the replay budget or keep tuning until a positive result
  appears. Retire ambiguous or unsupported candidates and preserve the negative
  result.
- Select at most one candidate on the exposed tuning cohort, then allow one
  confirmation on a lineage-unexposed held-out cohort. Never use ReserveFlow
  hidden-evaluator results to select or revise a prompt.
- Reject a candidate on any correctness, validation, authority, security,
  secrecy, or recovery regression regardless of latency or cost. Describe a
  candidate only as meeting its preregistered gate for the named frozen
  evaluation stratum, never as an optimal prompt.
- Treat the result as evidence about this prototype and task sequence, not proof of general coding-agent superiority.

## Handoff Format

For non-trivial completed work, report:

- TODO IDs completed;
- files and schema versions changed;
- behavior added or corrected;
- tests and commands run;
- benchmark or gate result when applicable;
- assumptions made;
- known limitations or follow-up IDs.
- `DEVLOG` ID and, when committed, `CHANGELOG` ID and commit identity.

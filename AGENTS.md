# SchemaFlux Agent Instructions

## Scope

These instructions apply to the whole repository.

This file was, until now, CodeFlux's `AGENTS.md` copied verbatim. It declared itself scoped to Codeflux, mandated a `docs/plan.md`, a `CHANGELOG`, a `DEVLOG`, `.artifacts/`, `cmd/codeflux-dev`, atoms, SQLite migrations, and a frontend — none of which exist here — and forbade the `git add -A` and direct `main` pushes this repository's own history is made of. An instruction file that describes a different repository is worse than none: every rule in it is a coin flip about whether it applies. This is the reconciliation TODOS.md **PS-009** asked for.

## Who decides what

| Question | Authority |
| --- | --- |
| **In what order**, and what is done | [`TODOS.md`](TODOS.md) |
| **Why** a task exists | [`docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md`](docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md) |
| **What the end state is** | [`docs/engineering/plans/to-production.md`](docs/engineering/plans/to-production.md) |
| **How** to work | this file |
| **What already exists** | the source and its tests |

Where they disagree about implemented behaviour, the source wins. Where they disagree about intent, `TODOS.md`'s reconciliation section carries the ruling.

Do not build something because a plan anticipates it. Several documents under `docs/engineering/plans/` are superseded and say so at the top.

## Before you start

1. Read this file.
2. Find the governing task in `TODOS.md`. Read the whole entry, including any **Revised** line — a Revised line supersedes the task body above it.
3. Read the source and tests actually in scope. The list is often behind the code.
4. If no task covers the work, add one first. If you finish something the list does not contain, add the item and check it in the same commit, so the record matches the work.

## The standard of done

A task is complete when its output exists and its verification passes. A model stopping is not completion.

Every closed task carries:

1. **Unit tests** exercising the changed function directly — **at least ten cases**.
2. An **integration test** through the exported API with a provider injected via `WithProviderInstance` or `schemafluxtest.Install`, so the whole stack is covered rather than one function.
3. For an LLM-backed operation, a runnable **`Example` with verified output**. These execute under `go test` with a scripted provider, so an example that rots fails the build and none of them need a credential.

Write the test that fails first where you can, and say in the evidence that you watched it fail. A test written after the fix proves the fix compiles.

### Recording evidence

Close the item in the same commit as the work, with the evidence beneath it: the test that proves it, the file that now exists, the measurement that was taken. A finished change under an unchecked box is indistinguishable from work nobody started, and the next agent will either redo it or route around it.

Write down what you did **not** do, too. A task closed with a stated limitation is worth more than one closed silently — most of the defects in the review were things somebody knew and did not write down.

## What this library is about

It turns LLM operations into typed Go calls. The whole review it is being rebuilt against is about one failure: **the library reporting success while being wrong**. Every rule below follows from that.

### Never fail open

- An operation that cannot produce a usable answer returns an error. Not a zero value, not a partial result, not a default with an invented confidence.
- A parse failure is a failure. A response of the wrong shape is a failure. A missing required field under `Strict()` is a failure.
- Do not invent numbers. No confidence derived from string length, no cost from a substituted price, no "tokens saved" from arithmetic on a chunk count. If a number is not measured, it does not exist.
- A model's self-reported score is a **claim**, not a measurement. Name such fields `Model*` and keep them away from anything the library verified.

### Check the answer against the question

A collection operation's contract is relational, and the check belongs in Go:

- `Choose` returns one of the items it was offered — compared by value, because the failure is an echoed item with a changed field, not an invented one.
- `Filter` returns a subset. `Sort` returns a permutation. `Cluster` returns a partition.
- The shared checks live in `internal/ops/invariants.go`. Use them; do not write a fourth membership test.

### Decide locally what can be decided locally

A rule Go can evaluate exactly should not be sent to a model. `Validate`'s field rules are the worked example: `email`, `iso3166-alpha2`, `min:18` are exact in Go and a judgement call in a model, and when every rule is decidable there is no provider call at all.

### Never log or embed the caller's payload

This library runs invoices, tickets, and notes through models. Every caller logs the errors it returns.

- No request or response body in an error string or a log line. Ever.
- Field names, shapes, counts, and JSON pointers describe a failure adequately. Values are somebody's data.
- `types.DescribeValue` exists for describing an input without reproducing it.

### Never spend money by accident

- Provider-backed tests compile always and run only when `SCHEMAFLUX_LIVE_TESTS=1` is set. A plain `go test ./...` must make zero network calls.
- Nothing in CI may reach a paid endpoint.
- The semantic regression suite (RC-002) is the one exception, and it runs on a protected workflow with a spend ceiling.

## Go engineering rules

- Errors are classified. `internal/llm/classify.go` is the single place that decides what a failure is; `types.ErrorKind` is the taxonomy and `errors.Is` reaches the sentinels. Do not write a second opinion about what is retryable — a retry decision that differs between layers is a bug nobody can reproduce.
- Substring-matching an error message is the thing the taxonomy exists to remove. It survives only as a fallback for errors carrying no type, and only against phrases this library itself produces.
- Context is a parameter, not a field. `operationContext` derives from the caller's; never write `context.WithTimeout(context.Background(), …)`.
- Map iteration order is random, so anything rendered into a prompt, a hash, or an error list gets sorted. `sortedKeys` exists for this. An unsorted render defeats every prefix cache and makes runs irreproducible.
- Lengths and offsets over text are counted in runes. Byte indexing splits characters, and the corpus at `internal/ops/utf8corpus_test.go` is what catches it.
- An option that changes nothing is a lie in the shape of an API. `dead_options_test.go` fails the build over it.

## Comments

Explain **why**, and especially what was wrong before. A comment saying what the code does is noise beside the code; a comment saying which failure a check prevents is the reason the check survives the next refactor.

Recently changed files are the register to match — `internal/ops/invariants.go`, `internal/llm/classify.go`, `internal/ops/strictdecode.go`. Plain prose. No aphorisms, no epigrams.

## Files

- **Never create a new Markdown file** unless the user asks for that file by name. A plan, a convention, an inferred documentation need, or a framework default is not authorization.
- Existing Markdown may be edited when the task requires it.
- If useful documentation has no authorized destination, put it in the task response or in a doc comment.
- Authorized and not clutter: the root `README.md`, `TODOS.md`, `AGENTS.md`, `CLAUDE.md`, and everything under `docs/`.

## Branches and commits

`main` is the working branch and takes direct commits; that is what this repository's history is, and pretending otherwise was one of the imported file's several fictions.

- Commit only when the user asks, or when an authorized workflow does.
- One coherent change per commit, with explicit paths — not `git add -A` unless you have looked at `git status` and every path belongs.
- The commit message says what was wrong and what changed about it. The diff already says what the code does.
- End with the `Co-Authored-By` trailer.
- There is no `CHANGELOG` or `DEVLOG` in this repository. `TODOS.md`'s evidence entries are the ledger.

## The gate

`.github/workflows/ci.yml` is the required gate. Run its steps locally before reporting anything done:

```
go build ./...
go test ./...
go test -race ./...            # not on windows/arm64; CI covers it
go test -shuffle=on -count=10 ./...
go vet ./...
gofmt -l .                     # must be empty
staticcheck ./...              # must be clean
python scripts/secret_scan.py
python scripts/coverage_floor.py
python scripts/examples_gate.py
python scripts/deprecation_policy.py --check
python scripts/acceptance_checklist.py --check
python scripts/release_gate.py --check
python .audit/traceability.py
go test . -run TestPublicAPISurface
```

This list is the same one in CONTRIBUTING.md and the same set of jobs in
`.github/workflows/ci.yml`. It had drifted into three different lists, so a
contributor could run everything either document asked for, pass, and still be
surprised by CI — which trains people to treat a red build as noise. If you add
a CI job, add it in all three places or the drift starts again.

Three of these are ratcheted rather than absolute — the coverage floor, the example gate, and the API snapshot. They only move deliberately:

- The coverage floor **only goes up**. If a change drops coverage inside the tolerance, that is what the tolerance is for; do not run `--update` to make a number go away.
- The example gate fails on an improvement too, so a fix nobody records cannot be silently re-broken.
- The API snapshot and the golden prompts are regenerated on purpose, and the reason goes in the commit message. A prompt is behaviour: editing one changes what every caller gets back with no Go API change to show for it.

## Before you report

- Inspect the diff and `git status`. No credential, no local database, no unrelated edit.
- Report the verification you actually ran, and every remaining limitation.
- If something is half-done, say which half. This list has closed too many things that were nearly true.

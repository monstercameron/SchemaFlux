# Contributing

## Read these first, in this order

1. [`AGENTS.md`](AGENTS.md) — how work is done here. It is short and it is
   binding.
2. [`TODOS.md`](TODOS.md) — what is being done and in what order. Find the
   governing task before writing anything, and read its whole entry: a
   **Revised** line supersedes the task body above it.
3. The source and its tests. The list is often behind the code.

If no task covers your work, add one first. If you finish something the list
does not contain, add the item and check it in the same commit, so the record
matches the work.

## What this library is about

It turns LLM operations into typed Go calls. The rebuild it is going through is
about one failure: **the library reporting success while being wrong.** Most of
the rules below follow from that, and a change that makes a wrong answer easier
to return will be refused however clean it is.

- **Never fail open.** An operation that cannot produce a usable answer returns
  an error — not a zero value, not a partial result, not a default with an
  invented confidence.
- **Do not invent numbers.** No confidence derived from string length, no cost
  from a substituted price. If a number is not measured, it does not exist. A
  model's self-reported score is a *claim*: name such fields `Model*` and keep
  them away from anything the library verified.
- **Check the answer against the question.** A selection is one of the items
  offered; a filter is a subset; a sort is a permutation. The shared checks live
  in `internal/ops/invariants.go` — use them rather than writing a fourth
  membership test.
- **Never log or embed the caller's payload.** Field names, shapes, counts, and
  JSON pointers describe a failure adequately. Values are somebody's data.
- **Never spend money by accident.** Provider-backed tests compile always and
  run only under `SCHEMAFLUX_LIVE_TESTS=1`. A plain `go test ./...` must make
  zero network calls.

## The standard of done

A task is complete when its output exists and its verification passes. Every
closed task carries:

1. **Unit tests** exercising the changed function directly — **at least ten
   cases**.
2. An **integration test** through the exported API with a provider injected via
   `WithProviderInstance` or `schemafluxtest.Install`.
3. For an LLM-backed operation, a runnable **`Example` with verified output**.

Write the failing test first where you can, and say in the evidence that you
watched it fail. A test written after the fix proves the fix compiles.

**Write down what you did not do.** A task closed with a stated limitation is
worth more than one closed silently — most of the defects in the review were
things somebody knew and did not write down.

### Tests that are worth having

The recurring lesson from this codebase, in three examples:

- Assert on **call counts**, not just return values. A function that called a
  provider and discarded the answer returns exactly what one that never called
  returns.
- Assert on **peak concurrency**, not totals. A test that passes against a
  serial implementation proves nothing about a bound.
- Make a guard **fail when it has nothing to guard**. A checker that silently
  matches nothing passes forever; this repository has shipped one that did.

Timing-based tests are refused. Inject the clock — `mw/ratelimit.go` and
`mw/retry.go` show the pattern. Run `go test -shuffle=on -count=5`; it has
caught two real races and a deadlock here, and a deadlock presents as a *hang*
rather than a failure.

## Before you send anything

`.github/workflows/ci.yml` is the required gate. Run it locally:

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
govulncheck ./...              # CI runs it; findings are stdlib-level today
(cd examples/smarttodo && go test ./...)   # its own module, not covered by ./...
```

Four of these are **ratcheted** rather than absolute — the coverage floor, the
example gate, the API snapshot, and the golden prompts:

- The coverage floor **only goes up**. If a change drops coverage inside the
  tolerance, that is what the tolerance is for; do not run `--update` to make a
  number go away.
- The example gate fails on an *improvement* too, so a fix nobody records cannot
  be silently re-broken.
- The API snapshot and the golden prompts are regenerated on purpose, and the
  reason goes in the commit message. **A prompt is behaviour**: editing one
  changes what every caller gets back with no Go API change to show for it.

## Commits

`main` is the working branch and takes direct commits.

- One coherent change per commit, with **explicit paths** — not `git add -A`
  unless you have looked at `git status` and every path belongs.
- The message says what was wrong and what changed about it. The diff already
  says what the code does.
- Close the task in the same commit as the work, with the evidence beneath it.

## Comments

Explain **why**, and especially what was wrong before. A comment saying what the
code does is noise beside the code; a comment saying which failure a check
prevents is the reason the check survives the next refactor.

Plain prose. No aphorisms.

## Files

**Never create a new Markdown file** unless it was asked for by name. A plan, a
convention, an inferred documentation need, or a framework default is not
authorization. Existing Markdown may be edited when the task requires it, and
anything under `docs/` is fair game.

## Reporting a security issue

Do not open a public issue. See [`SECURITY.md`](SECURITY.md) — and note that a
reproduction containing real payload data is itself an incident.

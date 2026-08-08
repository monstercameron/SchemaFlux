# 0005 — Shipping v1.0.0 with twelve unmet acceptance criteria

Status: accepted
Date: 2026-08-08

## Context

`docs/engineering/plans/to-production.md` §19 lists 32 acceptance criteria for
v1.0.0. TODOS.md carries them as a ledger, and RC-003's rule is explicit: every
unchecked box at v1.0.0 must carry an ADR saying why it ships unmet. This is
that ADR.

At the tag, 20 of 32 are met. Twelve are not. Every task in TODOS.md is closed —
the unmet criteria are not forgotten work, they are the parts of §19 that the
finished work does not reach.

## Decision

Tag v1.0.0 with the twelve unmet, each named below, rather than either (a)
holding the version number until all 32 are met, or (b) quietly checking boxes
that a reader could not verify.

The version number is a compatibility promise about the public Go API, not a
claim that every aspiration in §19 landed. §19 is this project's own bar, set
deliberately higher than "it works" — three of the twelve cannot be met on the
maintainer's hardware at all, and one cannot be met without spending money on
six vendors. Holding 1.0 for those would mean the number never arrives, which
helps nobody and makes the ledger a formality.

What v1.0.0 *does* promise: the public API in `testdata/api_surface.txt` is
stable under the deprecation policy in RC-004, and every behaviour claim in the
README is backed by a test in the repository.

## The twelve, and why each ships unmet

They fall into four groups, and the groups matter more than the count — a
criterion blocked by hardware is a different kind of thing from one blocked by
unfinished design.

### Cannot be verified on the maintainer's machine (2)

- **19.1.5** — client and builder ownership/lifecycle race-tested
- **19.5.1** — the full CI gate list including `race`

`-race` does not run on windows/arm64, which is where this library is developed.
`-shuffle=on` is the substitute used throughout and it is genuinely weaker: it
reorders tests, it does not instrument memory access. Both criteria are
*unverified* rather than *unmet* — the code may well satisfy them and nobody
here can show it. The CI matrix runs ubuntu, macos, and windows, so a green run
there is what would close these; it has not been observed at the time of the
tag because the repository has not been pushed until now.

### Blocked on spending money across six vendors (2)

- **19.5.2** — every production-supported provider live-verified
- **19.5.3** — release-candidate semantic regressions within thresholds

OpenAI is live-verified: P-012's smoke ran across the three gpt-5.6 models on
2026-08-08 and its responses are committed as cassettes, so the assertions
replay for free. The other six registered providers — anthropic, openrouter,
cerebras, deepseek, qwen, zai — have never been called live. The semantic
regression suite (`internal/semantic`) exists and its scoring is calibrated
against a planted failure rate, but running it against a live model is what
produces a baseline, and without a baseline there is no threshold to be within.

Both are one funded run away, per provider. Neither is a design gap.

### Unfinished design, honestly incomplete (7)

- **19.1.1** — core has no mutable global execution state
- **19.1.2** — every stable operation lowers to `Op → Run → Plan → Execute`
- **19.1.4** — stable execution requires `context.Context`
- **19.2.3** — every stable operation declares invariants and batchability
- **19.2.4** — evidence and provenance survive supported pipelines
- **19.2.5** / **19.4.4** — delivered contract level is observed, and a
  degradation below a locked contract is refused

These are the real ones. Three facts underlie most of them:

1. `ops.defaultProvider` and `ops.customLLMCaller` still exist. IN-004 gave the
   client an immutable per-call snapshot that takes priority everywhere, so no
   client is subject to another's configuration — but the globals remain for
   callers who never build a client, and removing them is a breaking change.
2. Five operations (Question, Predict, Cluster, Compress, Decompose) still call
   `callLLM` directly instead of lowering to an `Op` descriptor, so they declare
   no invariants and no batch algebra. That single fact is 19.1.2 and 19.2.3.
3. Contract negotiation is built (`negotiatedContractLevel`) and **dormant**:
   nothing reaching `RunOpResult` populates `opt.JSONSchema` today, so a
   provider silently degrading from native schema enforcement is still
   invisible. That is 19.2.5 and 19.4.4, and it closes when (2) does.

`ChooseBy`, `FilterBy`, and `SortBy` take no context, so a client cannot reach
them. `*Context` variants exist; the original three stay on the published
surface because removing them at 1.0 would break callers on the first day of a
stability promise.

For evidence (19.2.4): lineage resolves a claim to the *result* that produced
it, and `EvidenceRef` resolves a claim to a *span*, but nothing joins the two.
A three-stage pipeline does not resolve to a byte range in stage one's input.

### Published but incomplete (1)

- **19.5.5** — dashboards, alerts, and minimum runbooks

`docs/engineering/runbooks.md` covers twelve incident classes and names the
dashboard panels. No alert definitions exist. Alerts are deployment-specific and
this library ships no deployment, which is a reason and not an excuse: the
criterion says published, and they are not.

### One that ships with a section unavailable (also 19.5.6)

`scripts/release_notes.py` computes eleven of its twelve sections. The
semantic-benchmark comparison reports `UNAVAILABLE` because 19.5.3 has no
baseline. The generator prints the reason rather than omitting the section,
because an omitted section reads as "nothing changed", and `--check` exits
non-zero — so v1.0.0's notes are knowingly incomplete rather than silently so.

## Consequences

- A reader of TODOS.md's §19 ledger sees twelve unchecked boxes with a reason on
  each and this ADR behind them. That is the intended state, not a lapse.
- `scripts/acceptance_checklist.py --check` runs in CI and fails on drift: a
  criterion added to §19 without a ledger line, a ledger line whose wording has
  gone stale against the specification, or an unchecked box with no stated
  reason. It does **not** check whether a ticked box is true — nothing can — so
  ticking one remains a claim somebody stands behind.
- The seven design gaps are the v1.1 agenda. The two hardware ones close on the
  first green CI run. The two spend ones close per provider, whenever somebody
  funds them.
- If any of these is later checked off without the corresponding work, this ADR
  is the record that says what "done" was supposed to mean.

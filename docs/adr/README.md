# Architecture decision records

Two kinds of record live here.

**Decisions the specification made.** `docs/engineering/plans/to-production.md`
records twenty of them (`D-001`…`D-020`). They are not copied here — a copy
drifts from its original, and this repository already spent a task
(**TR-002**) building a checker for a hand-maintained table that had drifted
within a week. Read them there. This directory holds the *departures*.

**Departures.** A departure is a place where the code deliberately does not do
what the specification, the review, or an earlier decision said. Each one is a
choice somebody made with a reason, and the reason is the whole value: a
departure with no record is indistinguishable from an oversight, and the next
person will either "fix" it back or route around it.

A departure gets a record here when it is a *decision*, not when it is unfinished
work. Unfinished work belongs in `TODOS.md` with a task ID. The test is whether
someone could reasonably do the opposite: if yes, it needs a record.

## Format

One file per departure, `NNNN-short-name.md`, with:

- **Status** — accepted, superseded (by which), or withdrawn.
- **Context** — what the specification, review, or prior decision said.
- **Departure** — what the code does instead.
- **Why** — the reasoning, including what it costs.
- **When this should be revisited** — the condition that would change the answer.

The last one matters most. A departure with no revisit condition becomes
permanent by default rather than by decision.

## Records

| ID | Departure | Status |
| --- | --- | --- |
| [0001](0001-error-detail-compromise.md) | Vendor error detail is kept, behind a second channel | Accepted |
| [0002](0002-four-redaction-lists.md) | Five credential-pattern lists, deliberately not shared | Accepted |
| [0003](0003-context-value-provider-seam.md) | The per-call provider seam uses a context value | Accepted |
| [0004](0004-unpriced-default-models.md) | The default models ship unpriced | Accepted |

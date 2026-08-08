# Claude Code Repository Entry Point

Before beginning work in this repository:

1. read and follow [`AGENTS.md`](AGENTS.md);
2. find the governing task in [`TODOS.md`](TODOS.md), and read its whole entry — a **Revised** line supersedes the task body above it;
3. read [`docs/engineering/plans/to-production.md`](docs/engineering/plans/to-production.md) when the work touches the target architecture, and `TODOS.md`'s "Reconciliation with `to-production.md`" section for where the two documents disagree;
4. inspect the current source and tests before assuming planned components exist.

`AGENTS.md` is the authoritative repository-wide agent instruction file. This file stays a thin entry point so the rules are not duplicated and allowed to drift.

It previously pointed at `docs/plan.md` §0, a file this repository does not have — inherited along with the rest of the CodeFlux instructions that `AGENTS.md` now replaces. See TODOS.md PS-009.

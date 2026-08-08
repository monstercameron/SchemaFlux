<!--
Read AGENTS.md and CONTRIBUTING.md before opening this. Find the governing
task in TODOS.md and reference it below -- if none covers this change, add
one in the same PR.
-->

## What was wrong, and what changed

<!-- Not what the diff does -- the code says that. What failure or gap this
closes, and how the change closes it. -->

## Task

<!-- TODOS.md item ID(s). If none exists, the item was added in this PR. -->

## Evidence

<!-- Per AGENTS.md's standard of done: unit tests (>=10 cases) on the changed
function, an integration test through the exported API with an injected
provider, and for an LLM-backed operation a runnable Example with verified
output. Paste the test names, not just "added tests". -->

- [ ] Unit tests exercising the changed function directly (>=10 cases)
- [ ] Integration test through the exported API (`WithProviderInstance` or
      `schemafluxtest.Install`)
- [ ] `Example` with verified output, if this touches an LLM-backed operation
- [ ] I watched the relevant test fail before the fix, not just pass after

## Numbers claimed in this PR

<!-- Any confidence, cost, coverage, or performance figure named here must be
measured, not derived or assumed. State how each was produced. "None" is a
valid answer. -->

## Money

<!-- Provider-backed tests must compile always and run only under
SCHEMAFLUX_LIVE_TESTS=1. Confirm a plain `go test ./...` makes zero network
calls, and that nothing here can run in CI against a paid endpoint. -->

- [ ] This PR does not add anything that calls a paid endpoint without
      `SCHEMAFLUX_LIVE_TESTS=1` gating it

## Local gate

<!-- Ran the checks in CONTRIBUTING.md's "Before you send anything" list?
Paste which ones, and note anything skipped and why. -->

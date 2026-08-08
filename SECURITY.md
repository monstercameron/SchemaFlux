# Security

## Reporting a vulnerability

Report privately through GitHub's **Report a vulnerability** button on the
Security tab of `github.com/monstercameron/SchemaFlux`. Do not open a public
issue for a suspected vulnerability.

**Do not include a real payload in a report.** This library exists to run
invoices, tickets, and notes through language models, so a reproduction is very
likely to contain somebody's data. A shape, a field name, a count, or a JSON
pointer describes the failure adequately — `types.DescribeValue` produces
exactly that. A report containing live customer data is itself an incident.

Expect an acknowledgement within a week. If a fix is warranted, we will agree a
disclosure date with you; if it is not, you will get the reasoning rather than
silence.

## Supported versions

The module is **unreleased**: there are no tags, so every consumer is on a
pseudo-version pinned to a commit. Until `v0.2.0` is cut — the release ladder in
`TODOS.md` (REL-001) and `scripts/release_gate.py` decide when that is earned —
only `main` is supported, and "supported" means fixes land there.

Once tags exist, the intent is: the latest minor line receives security fixes,
and the previous one receives them for one documented deprecation window
(RC-004).

## Threat model

### What this library is trusted with

A caller's data on the way to a model provider, the credentials for that
provider, and the answer on the way back. It runs inside the caller's process
with the caller's privileges. It is not a sandbox and does not attempt to be
one.

### What it defends against

**Sending the caller's data somewhere they did not intend.** Provider selection
is explicit; failover refuses a route that does not meet the primary's declared
capability and data-policy requirements (`mw.Fallback`, CP-001/CP-002); an
undeclared allowlist permits nothing rather than everything; and the endpoint
policy (`types.EndpointPolicy`) refuses loopback, link-local, private, and cloud
metadata addresses unless a caller opts in — the SSRF shape that a
caller-supplied base URL otherwise invites.

**Leaking the caller's data into logs, errors, spans, or fixtures.** No error
carries a request or response body; errors report shapes, counts, and JSON
pointers. Observer events carry no payload field and a test enforces that by
reflection. Captured diagnostics are opt-in, bounded, and redacted before
storage. Recorded test cassettes are redacted before the file exists, and the
writer refuses to write one that still matches a credential pattern.

**Leaking credentials.** Four independent scrubbing points, each guarding a
different moment: `scripts/secret_scan.py` at commit time, `mw.RedactEgress` on
outbound requests, the cassette writer on fixtures, and the diagnostic sink on
captured bodies. A fifth guards log content. They are deliberately separate —
each protects a different boundary — and each says so in its own file.

**Spending money the caller did not authorise.** Budgets are checked *before* a
request, not after the invoice. Provider-backed tests compile always and run
only under `SCHEMAFLUX_LIVE_TESTS=1`; a plain `go test ./...` makes zero network
calls, and CI never reaches a paid endpoint.

**Returning a wrong answer as though it were right.** This is the failure the
whole library is built against. Operations verify their own contracts in Go —
a selection really is one of the items offered, a filter really is a subset, a
sort really is a permutation — and an operation that cannot produce a usable
answer returns an error rather than a zero value, a partial result, or a default
with an invented confidence.

### What it does not defend against

- **A malicious or compromised provider.** A provider that returns plausible
  wrong answers within the declared schema will pass every check this library
  runs. Structure is verified; truth is not.
- **Prompt injection through the caller's own data.** Every prompt segment
  carries a trust level (TC-001). Only the two levels this library authors
  itself — its fixed policy text and an operation's own instructions — may be
  placed in the system prompt; a request whose system segment contains anything
  else is refused rather than filtered. Caller-supplied steering is
  application data, so it goes in the user message wrapped in an explicit
  boundary, with any text inside it that looks like a boundary marker
  neutralized first, and a final check refuses the request outright if steering
  ever appears in the system prompt.

  That narrows the surface. **It does not eliminate prompt injection**, and no
  arrangement of Go types can: a model that reads instructions inside its own
  user message can still act on them. What the boundary guarantees is that the
  *library* never loses track of which bytes were the caller's, and never lets
  those bytes become part of what a provider caches and a log treats as fixed
  policy.
- **A hostile caller.** Anything running in-process can read the credentials the
  process holds.
- **Tool execution.** Tool *calling* surfaces a model's request; the library
  never executes it. What a caller does with that request is theirs. The
  registry's `shell` tool was deleted for this reason (PS-001).
- **Denial of service by a caller's own input.** Bounds exist — chunk sizing,
  output ceilings, bounded concurrency, a bounded scheduler — but a caller who
  submits unbounded work will consume unbounded resources.

## Known gaps

Recorded here rather than left to be discovered, and tracked in `TODOS.md`:

- Endpoint policy is **opt-in and off by default** (SEC-004). A base URL is
  checked only when a caller supplies an `EndpointPolicy`; without one, a
  custom endpoint is accepted as given. The check itself runs before any HTTP
  client is built, in every provider constructor that takes a base URL.
- Tenant deletion reaches the stores this library owns — its result cache and
  its diagnostic sink — but **not** the pricing/usage store or replay cassettes,
  and **not** `mw.Cache`, whose key derivation is irreversible by construction.
  Deletion against that one returns an explicit unsupported error rather than a
  false success (SEC-005).
- Client isolation is partial: a per-call provider seam exists, but budgets and
  other execution state remain process-wide (IN-004).
- The fluent shorthand helpers — `ChooseBy`, `FilterBy`, `SortBy` — take no
  `context.Context`, so `Client.Context(ctx)` cannot reach them and they always
  resolve the process-wide provider. An application running two clients gets
  whichever provider was installed last for any call through those three
  spellings. `ChooseByContext`/`FilterByContext`/`SortByContext` do carry a
  context and are isolated; the three original spellings are kept unchanged
  because they are part of the published surface. They are not yet re-exported
  from the root package (IN-004).

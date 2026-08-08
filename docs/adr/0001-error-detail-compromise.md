# 0001 — Vendor error detail is kept, behind a second channel

**Status:** accepted

## Context

The review's rule, and this library's own rule in `AGENTS.md`, is absolute: no
request or response body in an error string or a log line, ever. Field names,
shapes, counts, and JSON pointers describe a failure adequately; values are
somebody's data.

**A-011**'s Revised line then observed the cost of applying it literally:
"removing content from errors leaves debugging with nothing."

## Departure

`llm.APIError` retains the provider's structured error message (**P-007**), and
`types.DiagnosticSink` can capture a bounded, redacted body (**A-011**).

## Why

A provider's own error text — "unsupported value for `response_format`" — is
almost always about the *request this library constructed*, not about the
caller's data, and it is the single most useful thing when a provider changes
behaviour. Discarding it made those failures undiagnosable.

The compromise has three parts, and it is only defensible with all three:

1. `Error()` withholds the raw response body. `Detail()` includes it, so reading
   it is a deliberate act rather than something that lands in every log line.
2. The diagnostic sink is **off unless a caller installs one**, bounded,
   redacted before it is stored, and referenced from the ordinary error by ID
   and digest rather than by content.
3. A provider error can still quote the caller's input back. That risk is real
   and is why (1) exists.

**What it costs:** a caller who calls `Detail()` and logs the result has
defeated the rule, and nothing stops them. This is a guardrail, not a wall.

## When this should be revisited

If a provider is found to routinely echo request bodies in its structured error
message, (1) is not enough and the message itself needs redacting on the way in.

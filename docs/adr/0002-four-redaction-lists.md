# 0002 — Five credential-pattern lists, deliberately not shared

**Status:** accepted

## Context

Duplication of a security-critical list is normally a defect: five copies drift,
and the one nobody updates is the one that misses the leak.

## Departure

This repository has five independent credential-pattern lists:

| Where | Guards |
| --- | --- |
| `scripts/secret_scan.py` | files at commit time |
| `mw/redact.go` | outbound requests, at egress |
| `schemafluxtest/cassette.go` | recorded fixtures, before the file exists |
| `internal/types/diagnostics.go` | captured bodies, before they reach a sink |
| `internal/telemetry/logger.go` | log content, at the logging boundary |

## Why

Two reasons, and the second is the load-bearing one.

**They guard different moments with different risk profiles.** A commit-time
scanner can afford false positives — a human reviews the result. An egress
redactor cannot: mangling a legitimate request is a broken feature, which is
why `mw/redact.go` has a test asserting ordinary prose survives untouched. A
fixture guard runs once and refuses to write. These want different tuning.

**Sharing them is not available anyway.** `internal/types` cannot import `mw`
without an import cycle; `mw` importing `schemafluxtest` would make production
middleware depend on a test-support package. The Python scanner is not Go. So
the choice was never "share or duplicate" — it was "duplicate, or leave three of
the five boundaries unguarded."

**What it costs:** a newly discovered credential shape has to be added in five
places, and there is nothing that fails when it is added in only one. That is a
real gap and it is the reason each file carries a comment naming the other four.

## When this should be revisited

If a leak is ever traced to a pattern present in one list and missing from
another, the cost has been paid and a shared Go-side list (with the Python
scanner generated from it) becomes worth the import gymnastics.

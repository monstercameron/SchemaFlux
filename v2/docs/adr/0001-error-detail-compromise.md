# 0001 — Vendor error detail is kept, behind a second channel

**Status:** accepted; amended 2026-10-03 (see "Amendment" below, which changes
what the compromise is)

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

## Amendment, 2026-10-03: the message is withheld too

The revisit condition above was met. A reviewer ran the real OpenAI provider
against a loopback server whose 401 message quotes the API key, as some gateways
and proxies do, and the key appeared in the error returned to the caller and in
two of the library's own log lines ("LLM request failed" and "LLM request retry
scheduled"). A message can equally quote the request back: a schema the provider
rejects, a fragment of the prompt, a content-filter explanation. The argument for
printing it, that a structured message is about the request the library
constructed and not about the caller's data, does not hold for text a server
writes and the library cannot inspect.

The rule is now by who wrote the words, and there is one gate with one meaning.

- **What an error prints** is the provider's fixed vocabulary and the library's
  own words: the provider, the status, the model, the vendor's `type`, `code` and
  `param`, the wait a throttled answer asked for, and a note that a message and a
  body were withheld with the length of each. Never the message, never the body.
  `type`, `code` and `param` are chosen by a server, so each is cut to a bounded
  length (64, 64 and 128 runes) and to printable characters. `param` is kept
  because it is the vendor's path to the offending parameter, which can be a
  property name of the caller's own schema: the caller's type, not data.
- **The gate** is `SCHEMAFLUX_ERROR_DETAIL`. Closed, which is the default, it
  prints neither the message nor the body. Open, it prints both, for a developer
  debugging against their own data. `Detail()` returns both whatever the gate
  says, because asking for it is the deliberate act the gate stands for. Before
  this amendment the gate opened the body alone, and only when the vendor had sent
  no structured message; the message was printed always. The codex provider's
  error follows the same gate and now opens both of its fields together too,
  the CLI's `turn.failed` message and its stderr.
- **What is kept on the value, and printed through `Detail()` or the open gate**
  has the provider's own configured key removed, and every credential shape
  `types.RedactCredentials` knows redacted. The key is removed where the provider
  builds the error, which is the one place that holds it, so `Message` and `Body`
  never contain it: a caller who reads the fields, or a debugger that dumps the
  struct, does not get it either. The configured key, and the value of any extra
  header named for a credential, is removed in the forms a server writes it, as it
  is and escaped as JSON, and from the `type`, `code` and `param` as well.
- **Every way an error can be rendered follows the gate.** `APIError`,
  `RateLimitError` and the codex error have `String`, `GoString`, `LogValue` and
  `MarshalJSON`, so that an error held by value, logged through `slog` inside a
  struct or a group, formatted with `%#v` or marshalled prints the same safe text
  and not every exported field.

**What it still costs:** `Detail()`, the open gate, and a caller who reads
`Message` or `Body` and logs it have defeated the rule, as before. The key is the
one thing taken out of what those return. A prompt fragment a gateway quotes is
not, because the library cannot recognise one. A diagnostic sink (part 2 above)
is unchanged.

**Provider text the amendment did not need to touch:** the other errors that name
a provider's answer are written by the library, with fixed sentences and no
quotation (`incompleteAnswerError`, `refusedAnswerError`, the stop-reason and
status errors). A JSON decode failure of a response wraps `encoding/json`'s own
message, which can name one character or one number of the body; that is not
changed here.

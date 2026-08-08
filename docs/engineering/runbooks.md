# Runbooks

**OB-005.** One entry per incident class that will actually happen to a service
running this library. Each names the **signal** you will see first, the
**mitigation**, the **safe fallback**, the **evidence to capture** before you
change anything, and the **recovery criterion** that tells you it is over.

Two rules apply throughout and are not repeated per entry:

- **Evidence before mitigation, unless the mitigation stops a leak.** Most of
  these are cheap to reproduce and expensive to diagnose after the fact.
- **Never capture the payload.** Every diagnostic named below is a shape, a
  count, an ID, or a kind. If you need bodies, turn on a diagnostic sink
  (`WithDiagnosticSink`, A-011) which truncates and redacts before storing, and
  turn it off afterwards. Pasting a failing request into a ticket is the
  incident, not the investigation.

---

## 1. 429 surge

**Signal.** `ErrorKind` `rate_limited` climbing across requests; `Meta.Attempts`
rising while success holds steady, then falling over as retries exhaust.

**Mitigation.** Reduce offered concurrency first — `MapReduceOptions.Concurrency`
at the call site, or the bound on whatever pool is fanning out. Add
`mw.RateLimit` ahead of the provider if nothing is shaping the load yet. Do not
raise the retry budget: retries against a 429 are the surge.

**Safe fallback.** `mw.Fallback` to a route with separate quota, if one is
declared and meets the same capability and data-policy requirements. Failing
that, shed new work by refusing it — a refusal is a classified error the caller
can act on; an accepted request that never completes is not.

**Evidence.** The `Retry-After` values the provider sent (`llm.RetryAfterFrom`
recovers them), the in-flight count when it started, and whether the 429s are
per-key or per-model.

**Recovery.** `rate_limited` back to its baseline for a full window *at the
original concurrency*. Recovering only at reduced concurrency means the incident
is ongoing and being masked.

---

## 2. Provider latency or outage

**Signal.** `Meta.Elapsed` rising with no change in token counts; `timeout` and
`provider_unavailable` kinds appearing together.

**Mitigation.** Confirm the provider's own status before touching anything: a
library-side change during a provider outage adds a variable to an incident that
already has one.

**Safe fallback.** `mw.Fallback` to an alternate route. It refuses to substitute
a route that does not meet the primary's declared capability and data policy —
if failover does nothing, check whether the alternate's declarations actually
satisfy the requirement rather than assuming the fallback is broken.

**Evidence.** Latency distribution before and after, the split between connect
and first-token time if streaming, and whether one model or all of them.

**Recovery.** Latency back inside its normal band for a sustained period, and
`provider_unavailable` at zero.

---

## 3. Malformed or truncated output spike

**Signal.** `malformed_output` or `output_truncated` kinds rising.
`Meta.Repairs` climbing is the same incident one layer up.

**Mitigation.** These are two different problems and the kinds distinguish
them deliberately. **Truncated** means the answer was cut off: raise
`OpOptions.MaxOutputTokens`, or shorten the input. **Malformed** means the shape
was wrong: check whether a prompt changed (the golden snapshot moves when it
does) or whether the model alias moved under you — see entry 6.

**Safe fallback.** Strict mode plus the repair loop already retries a bad shape
within budget. If a specific operation is the only one failing, force it to a
stronger tier rather than loosening its contract. Loosening the contract turns a
detected failure into an undetected one.

**Evidence.** `Meta.Attempts` and `Meta.Repairs` per operation, the finish
reason distribution, and whether the failures cluster on one model.

**Recovery.** Both kinds at baseline and `Meta.Repairs` back to its normal rate.

---

## 4. Batch omission or repair-rate spike

**Signal.** Collection operations failing with invariant violations —
`SubsetOf`, `SameMultiset`, `CoversExactlyOnce` — or `Meta.Repairs` rising on
batched work specifically.

**Mitigation.** This is the failure the id protocol (OP-101) exists to catch,
and catching it is correct behaviour: the model returned an answer that did not
cover its input. Reduce batch size so each request carries fewer items.

**Safe fallback.** Force the atomic execution shape for the affected operation.
It costs more calls and it is exactly the trade the shape selection exists to
let you make deliberately.

**Evidence.** The batch size at failure, which invariant failed, and the counts
in the error — the errors report *how many* items were wrong and never which,
so the counts are what you have and are enough to size the problem.

**Recovery.** Invariant violations at zero at the original batch size.

---

## 5. Cost anomaly

**Signal.** `Meta.Cost.TotalCost` rising without a matching rise in requests, or
`Meta.Usage.PromptTokens` rising with no input change.

**Mitigation.** Check `Meta.CacheHitRatio` first. A prompt cache that stopped
hitting looks exactly like a cost increase, and the library logs a diagnostic
when a prefix repeats several times without a single cached read. The usual
cause is something per-call leaking into the stable prompt segment (CA-002).

**Important:** check `Meta.Cost.Priced` before believing any figure. An unpriced
model reports `Priced: false`, and the default 5.6 models are currently
**unpriced** — a cost of zero from them means "no rate card", not "free".
Register real rates with `pricing.RegisterPricingModel`.

**Safe fallback.** `mw.Budget` with a ceiling refuses calls before they are
made rather than after the invoice.

**Evidence.** Cache hit ratio over time, token counts per operation, and
whether a prompt changed (the golden snapshot is the record).

**Recovery.** Cost per request back to baseline with the cache hit ratio
restored.

---

## 6. Model alias or revision change

**Signal.** Behaviour changes with no deploy: a rise in malformed output, a
change in answer style, or a capability that used to work refusing.

**Mitigation.** Aliases move. Pin the exact model ID rather than the tier, and
compare `Meta.Model` against what you expected — the envelope records the
*resolved* model for exactly this.

**Safe fallback.** Pin to the previous dated snapshot if one is published.

**Evidence.** `Meta.Model` before and after, and the prompt cache key: the key
covers the resolved model, so a moved alias produces a new key and a cold cache,
which is often the first thing anybody notices.

**Recovery.** `Meta.Model` stable at the pinned ID and the affected kinds at
baseline.

---

## 7. Capability or schema enforcement regression

**Signal.** `unsupported_capability` appearing where it did not before, or
strict-mode extractions failing on schemas that used to pass.

**Mitigation.** Check whether the provider changed what it supports before
changing the schema. A schema loosened to work around a provider regression is
permanent in a way the regression is not.

**Safe fallback.** A named degradation — native schema to JSON mode plus
deterministic validation — is allowed **only by explicit policy** and is meant
to be recorded as delivered. If you take it, record that you took it.

**Evidence.** The capability matrix's answer for that model, the schema's
descriptor and hash, and whether `DeliveredContract` dropped below
`RequestedContract`.

**Recovery.** The contract delivered at the level requested, without the
degradation flag.

---

## 8. Stuck breaker

**Signal.** `circuit_open` persisting after the underlying provider has
recovered.

**Mitigation.** Confirm the provider is genuinely healthy with a single manual
call before intervening. A breaker that reopens immediately is doing its job.

**Safe fallback.** Route to an alternate while it is open.

**Evidence.** What opened it — the failure kinds and the window — and whether
the probe that should close it is being attempted at all. A breaker with no
probe is a permanently open one.

**Recovery.** Closed, with success sustained past the probe window.

---

## 9. Cache or pricing-store failure

**Signal.** For the cache: hit ratio at zero with no other change. For pricing:
`Priced: false` appearing on models that used to be priced.

**Mitigation.** Both are optional adapters and neither may take the request path
down with it. A cache that cannot be read is a miss. A rate card that cannot be
loaded is *unpriced*, never free — that distinction is the whole reason the
field exists (PR-001).

**Safe fallback.** Run without them. Cost accounting degrades to unpriced and
should be visibly marked so, rather than reporting zeros.

**Evidence.** Whether `Priced` went false or `TotalCost` went to zero. The
second one, on a model that has a rate card, is a bug worth escalating.

**Recovery.** Hit ratio restored; `Priced` true again for the affected models.

---

## 10. Telemetry exporter failure

**Signal.** Spans or metrics stop arriving; the application is otherwise
healthy.

**Mitigation.** This library does not own the exporter. It emits through
`telemetry.Observer` and the OpenTelemetry adapter uses **your** provider —
so an exporter failure is a failure in your telemetry stack, and the library
will keep serving requests through it. Nothing here needs to be restarted to
fix it.

**Safe fallback.** Remove the observer (`telemetry.SetObserver(nil)`) to rule
telemetry out as a cause. Note that an observer is called inline, so a slow
sink slows the request path — that is the sink's responsibility and this is how
you confirm it.

**Evidence.** Whether request latency moved when telemetry failed. If it did,
the observer is blocking and that is a bug in the observer.

**Recovery.** Telemetry flowing, with request latency unchanged from when it was
not.

---

## 11. Suspected content or credential leak

**Signal.** A credential shape found in a log, a span, a stored diagnostic, or a
committed fixture.

**Mitigation — do this before evidence gathering, because the leak is ongoing.**
Rotate the credential. Then find the channel: the library has four separate
credential-scrubbing points, and knowing which one was bypassed is the fix.
`scripts/secret_scan.py` covers committed files, `mw.RedactEgress` covers
outbound requests, the cassette writer covers recorded fixtures, and the
diagnostic sink covers captured bodies.

**Safe fallback.** Turn off any diagnostic sink until the channel is
identified — it is the only component that stores bodies at all.

**Evidence.** Which channel carried it, and whether the pattern was one the
scrubbers know. A credential shape none of the four lists matches is a gap worth
a task, not just a rotation.

**Recovery.** Rotated, channel closed, and a test that would have caught it.
`scripts/secret_scan.py --self-test` is where a new pattern goes.

---

## 12. Deprecated endpoint

**Signal.** Deprecation warnings in provider responses, or `invalid_request`
appearing on a call shape that used to work.

**Mitigation.** Migrate the affected provider path. This is planned work, not an
incident, unless the endpoint has already been withdrawn.

**Safe fallback.** Pin to the older API version if the provider still offers
one.

**Evidence.** The provider's stated withdrawal date and which operations use the
path.

**Recovery.** Migrated, with the operation's golden prompts and API surface
snapshot reviewed — a provider migration that changes what is sent is a
behaviour change with no Go API change to show for it, which is exactly what
those two snapshots exist to surface.

---

## Dashboards

The panels worth having, in the order you will look at them during an incident.
They are all derived from `types.Meta`, which is why the envelope carries what
it carries:

1. **Request rate and error rate, split by `ErrorKind`.** The split is the
   dashboard. One error-rate line tells you something is wrong; the kinds tell
   you which runbook to open.
2. **Latency distribution**, p50/p95/p99, split by operation.
3. **`Meta.Attempts` and `Meta.Repairs`.** Attempts rising is retry pressure;
   repairs rising is answer quality. They fail differently and are often
   confused for each other.
4. **Cost per request, with unpriced calls counted separately.** Not summed
   with the priced ones — an unpriced call has no cost to add, and adding zero
   understates the bill by exactly the amount you cannot see.
5. **`Meta.CacheHitRatio`.** The leading indicator for entry 5.
6. **In-flight gauge and queue depth**, if a scheduler is in use.
7. **Review-required count.** A rise here is not a failure — it is the library
   declining to guess, which is a successful outcome and should be visible as
   one rather than buried in an error rate.

**Keep high-cardinality identifiers out of metric labels.** Request IDs,
correlation IDs, and schema hashes belong in the envelope on the individual
result, not in a label that multiplies your series count by your traffic.

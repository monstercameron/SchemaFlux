# 0004 — The default models ship unpriced

**Status:** accepted

## Context

`gpt-5.6-luna`, `-sol`, and `-terra` are this library's default models. No rate
card for any of them exists anywhere in this repository, so **every operation
currently reports its cost as unpriced**.

The obvious fix is to add plausible numbers.

## Departure

They stay unpriced, and a test pins that they do
(`TestTheDefaultModelsReportUnpricedRatherThanGuessed`).

## Why

**PR-001** established that zero never means unknown, and this is the same rule
one level up: a rate card nobody verified produces a confident, precisely wrong
invoice, and a caller reconciling it against a provider bill has no way to tell
an estimate from a measurement. An honest "we do not know" is a worse experience
and a better answer.

`pricing.RegisterPricingModel` exists so anyone holding the real rates — the
account owner reading their own dashboard — can supply them in one call. It
refuses a rate above a dollar per 1K tokens, naming the likely cause: every
public price list quotes per **million** and this table is per **thousand**.

**What it costs:** cost accounting is unusable out of the box, and
`Meta.Cost.Priced` is false for every request until someone registers rates.
`PricingQuality` (**OB-003**) exists so that this reads as *unknown* rather than
as *free*.

## When this should be revisited

The moment the real published rates are known. That is a one-line addition to
the table plus a deliberate update to the test above — the test is written to be
changed, not to be deleted.

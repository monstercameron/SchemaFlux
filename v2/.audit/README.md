# Coverage audit

`audit.py` checks the standard of done recorded in `TODOS.md`: every closed task
carries at least ten test cases, and every LLM-backed (Smart+) operation it
touches carries a runnable `Example`.

It is a check, not a report: it maps each closed task cluster to the test
functions that cover it, counts the leaf cases those functions actually ran, and
prints `NEEDS CASES` or `NEEDS EXAMPLE` for any cluster below the bar.

Run it:

    go test ./... -count=1 -v 2>&1 | grep -E "^ *--- PASS" | sed 's/^ *--- PASS: //;s/ (.*//' > .audit/passes.txt
    python .audit/audit.py

Leaf cases, not test functions: a table-driven test contributing twelve
subtests counts as twelve. A function with no subtests counts as one.

When a task is closed, add it to `CLUSTERS` in `audit.py` in the same commit.
An unmapped task is invisible to the check, which is the one failure mode this
file cannot catch by itself.

## Traceability

`traceability.py` checks that every finding in
`docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md` is addressed by a task in
`TODOS.md`. The review is the input to that list, and the relationship is only
worth anything if it is checkable: a finding with no task is a defect nobody
scheduled.

    python .audit/traceability.py

It exits non-zero when a finding has no task. It also reports dangling
references — a task citing a finding ID the review does not contain.

A task claims a finding by naming it in its body: `Closes **I-05**`,
`Addresses **Gap-04**`. That citation is the whole mechanism, so a task that
fixes something without naming what it fixes is invisible here.

### Gated clusters

The live cluster is reported as `GATED` rather than counted. Its tests skip
unless `SCHEMAFLUX_LIVE_TESTS=1`, so they never appear in a default run:
counting them as uncovered would be wrong, and counting them as covered from a
run that skipped them would be a lie. Verify them with

    SCHEMAFLUX_LIVE_TESTS=1 go test . -run TestLive -v

### What these checks have caught

Not hypothetical. `traceability.py` found five dead option fields the review
missed, two live S1 defects with no task at all, and a duplicate task ID that
silently un-traced a finding an hour after it was introduced.

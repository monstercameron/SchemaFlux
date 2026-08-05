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

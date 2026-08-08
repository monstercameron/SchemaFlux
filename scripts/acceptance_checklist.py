#!/usr/bin/env python3
"""Check §19's v1.0.0 acceptance criteria against a ledger this repository does not have yet.

RC-004's checker was "read the catalogue instead of hand-maintaining a
duplicate list." RC-003 asks for the same discipline applied to
`to-production.md` section 19 -- the twenty-nine (see COUNT_HINT below; the
actual count is read from the document, not hard-coded) checkboxes across
core architecture, correctness and trust, execution and resilience, security
and governance, and verification and operations that decide what v1.0.0
means.

RC-003's own instruction is "track §19's acceptance criteria as a checklist
in TODOS.md" -- but TODOS.md is out of scope for this task to edit. So this
script is the checker, built to read a ledger section of TODOS.md in the
format specified below (see --explain), and it currently reports that the
ledger does not exist, because it does not: the ledger is the thing to add,
not something this script invents on your behalf.

    python scripts/acceptance_checklist.py             # report drift
    python scripts/acceptance_checklist.py --check     # exit 1 on drift
    python scripts/acceptance_checklist.py --explain   # print the format to add to TODOS.md
    python scripts/acceptance_checklist.py --self-test

What "drift" means here:

  - a §19 criterion with no matching ledger line in TODOS.md (the ledger has
    not caught up with the spec, or was never added)
  - a ledger line whose ID does not match any current §19 criterion (a
    criterion was removed or renumbered and the ledger is stale)
  - a ledger line whose text no longer matches the criterion's current
    wording at that ID (the spec changed under it)
  - a ledger line marked unchecked with no `ADR:` citation (an unmet box at
    v1.0.0 with no recorded reason is exactly what RC-003 exists to catch)
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = ROOT / "docs" / "engineering" / "plans" / "to-production.md"
TODOS = ROOT / "TODOS.md"

SECTION_HEADING = re.compile(r"^## 19\.(\d+) (.+)$")
TOP_HEADING = re.compile(r"^# 2\d\. ")  # "# 19. ..." start, "# 20. ..." end
CHECKBOX = re.compile(r"^- \[( |x)\] (.+)$")

# The ledger line format this script looks for in TODOS.md. Each spec
# criterion gets one line, ID first so drift is detected by ID rather than by
# hoping the prose never rewraps:
#
#   - [ ] 19.3.4 — Retry, repair, split, fallback, escalation, and review share one budget ledger. — ADR: docs/adr/0003-scheduler-deferred-to-v2.md
#   - [x] 19.1.1 — Core has no mutable global execution state.
#
# Checked boxes need no ADR (the criterion is met). Unchecked boxes need an
# `ADR:` citation of at least a few words -- RC-003 does not require the ADR
# document to exist yet (PS-010, the ADR directory, is its own open task);
# it requires a stated reason, which is what "ADR: <words>" records even
# before the directory exists.
LEDGER_LINE = re.compile(
    r"^- \[( |x)\] (19\.\d+\.\d+) — (.+?)(?: — ADR: (.+))?$"
)


def parse_spec_criteria(text: str) -> list[tuple[str, str]]:
    """Return [(id, title), ...] for every §19 checkbox, in document order."""
    lines = text.splitlines()
    criteria: list[tuple[str, str]] = []

    in_section_19 = False
    subsection = None
    index_in_subsection = 0

    for line in lines:
        if line.startswith("# 19. "):
            in_section_19 = True
            continue
        if in_section_19 and TOP_HEADING.match(line) and not line.startswith("# 19."):
            break
        if not in_section_19:
            continue

        heading = SECTION_HEADING.match(line)
        if heading:
            subsection = heading.group(1)
            index_in_subsection = 0
            continue

        box = CHECKBOX.match(line)
        if box and subsection is not None:
            index_in_subsection += 1
            criterion_id = f"19.{subsection}.{index_in_subsection}"
            criteria.append((criterion_id, box.group(2).strip()))

    return criteria


def parse_ledger(text: str) -> dict[str, tuple[bool, str, str | None]]:
    """Return {id: (checked, title, adr_or_None)} for every ledger line found
    anywhere in TODOS.md. Not anchored to a specific heading, so the ledger
    can live wherever the M17 section puts it."""
    ledger: dict[str, tuple[bool, str, str | None]] = {}
    for line in text.splitlines():
        match = LEDGER_LINE.match(line.strip())
        if not match:
            continue
        checked, criterion_id, title, adr = match.groups()
        ledger[criterion_id] = (checked == "x", title.strip(), adr.strip() if adr else None)
    return ledger


def diff(spec_criteria: list[tuple[str, str]], ledger: dict[str, tuple[bool, str, str | None]]) -> list[str]:
    problems: list[str] = []
    spec_ids = {cid for cid, _ in spec_criteria}

    for criterion_id, title in spec_criteria:
        if criterion_id not in ledger:
            problems.append(f"{criterion_id}: no ledger line in TODOS.md for {title!r}")
            continue
        checked, ledger_title, adr = ledger[criterion_id]
        if ledger_title != title:
            problems.append(
                f"{criterion_id}: ledger text {ledger_title!r} does not match the spec's current {title!r}"
            )
        if not checked and not adr:
            problems.append(f"{criterion_id}: unchecked with no ADR citation ({title!r})")

    for criterion_id in ledger:
        if criterion_id not in spec_ids:
            problems.append(f"{criterion_id}: ledger entry has no matching §19 criterion (stale or renumbered)")

    return problems


LEDGER_FORMAT_EXPLANATION = """\
Add a section to TODOS.md's M17 area, e.g. under RC-003, with one line per
§19 criterion in this exact shape:

    - [ ] 19.1.1 — Core has no mutable global execution state. — ADR: <reason or docs/adr/000N-*.md>
    - [x] 19.1.2 — Every stable public operation lowers to the same `Op -> Run -> Plan -> Execute` path.

Rules this checker enforces:
  - the ID is "19.<subsection>.<index-within-subsection>", assigned in the
    order to-production.md lists the boxes (run this script with no flags
    to print the current IDs and titles);
  - the title after the em dash must match to-production.md's current
    wording for that ID, so a spec edit is visible as drift instead of
    silently going stale;
  - an unchecked box needs " — ADR: <something>" after the title -- the ADR
    document itself does not need to exist yet (that is PS-010's job), but a
    stated reason does;
  - a checked box needs nothing further.

Run `python scripts/acceptance_checklist.py` with no arguments to print every
current §19 ID and title, ready to paste in.
"""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--check", action="store_true", help="exit 1 if the ledger has drifted or is missing")
    parser.add_argument("--explain", action="store_true", help="print the ledger format to add to TODOS.md")
    parser.add_argument("--self-test", action="store_true", help="run the fixture-based self-test")
    args = parser.parse_args()

    if args.self_test:
        return self_test()

    if args.explain:
        print(LEDGER_FORMAT_EXPLANATION)
        return 0

    spec_text = SPEC.read_text(encoding="utf-8")
    todos_text = TODOS.read_text(encoding="utf-8")

    criteria = parse_spec_criteria(spec_text)
    ledger = parse_ledger(todos_text)

    print(f"acceptance checklist: {len(criteria)} criteria in §19, {len(ledger)} ledger lines in TODOS.md")
    for criterion_id, title in criteria:
        print(f"  {criterion_id}: {title}")

    problems = diff(criteria, ledger)

    if not ledger:
        print()
        print("TODOS.md has no ledger yet. Run --explain for the format to add.")

    if problems:
        print()
        print("DRIFT:")
        for p in problems:
            print(f"  {p}")

    if not args.check:
        return 0
    return 1 if problems else 0


# --- self-test -------------------------------------------------------------

SPEC_FIXTURE = """\
# 18. Something before

## 18.1 Not this section

- [ ] Not a §19 item.

# 19. v1 acceptance criteria

## 19.1 Core architecture

- [ ] Core has no mutable global execution state.
- [ ] Every stable public operation lowers to the same path.

## 19.2 Correctness and trust

- [ ] Exact decoder rejects unknown fields.

# 20. Risk register

- [ ] Not a §19 item either.
"""

EXPECTED_CRITERIA = [
    ("19.1.1", "Core has no mutable global execution state."),
    ("19.1.2", "Every stable public operation lowers to the same path."),
    ("19.2.1", "Exact decoder rejects unknown fields."),
]

LEDGER_COMPLETE_AND_CLEAN = """\
Some prose.

- [x] 19.1.1 — Core has no mutable global execution state.
- [ ] 19.1.2 — Every stable public operation lowers to the same path. — ADR: deferred to v2, see docs/adr/0001-scheduler.md
- [x] 19.2.1 — Exact decoder rejects unknown fields.
"""

LEDGER_MISSING_ONE = """\
- [x] 19.1.1 — Core has no mutable global execution state.
- [x] 19.2.1 — Exact decoder rejects unknown fields.
"""

LEDGER_UNCHECKED_NO_ADR = """\
- [x] 19.1.1 — Core has no mutable global execution state.
- [ ] 19.1.2 — Every stable public operation lowers to the same path.
- [x] 19.2.1 — Exact decoder rejects unknown fields.
"""

LEDGER_STALE_TEXT = """\
- [x] 19.1.1 — Core has some mutable global execution state.
- [x] 19.1.2 — Every stable public operation lowers to the same path.
- [x] 19.2.1 — Exact decoder rejects unknown fields.
"""

LEDGER_DANGLING_ID = """\
- [x] 19.1.1 — Core has no mutable global execution state.
- [x] 19.1.2 — Every stable public operation lowers to the same path.
- [x] 19.2.1 — Exact decoder rejects unknown fields.
- [x] 19.9.1 — This criterion does not exist in the spec.
"""


def self_test() -> int:
    failures = 0

    got = parse_spec_criteria(SPEC_FIXTURE)
    if got != EXPECTED_CRITERIA:
        failures += 1
        print(f"FAIL: parse_spec_criteria = {got}, want {EXPECTED_CRITERIA}")

    cases = [
        (LEDGER_COMPLETE_AND_CLEAN, 0, "a complete, matching, ADR-covered ledger has no drift"),
        (LEDGER_MISSING_ONE, 1, "a criterion absent from the ledger is drift"),
        (LEDGER_UNCHECKED_NO_ADR, 1, "an unchecked box with no ADR citation is drift"),
        (LEDGER_STALE_TEXT, 1, "ledger text that no longer matches the spec is drift"),
        (LEDGER_DANGLING_ID, 1, "a ledger entry with no matching spec ID is drift"),
    ]
    for ledger_text, want_problem_count_nonzero, why in cases:
        ledger = parse_ledger(ledger_text)
        problems = diff(EXPECTED_CRITERIA, ledger)
        has_problems = len(problems) > 0
        want = bool(want_problem_count_nonzero)
        if has_problems != want:
            failures += 1
            print(f"FAIL ({why}): problems={problems}")

    # The refusal this gate exists to prove: no ledger at all is reported as
    # drift (missing every criterion), not silently treated as "nothing to
    # check" -- which is exactly today's real state in this repository.
    empty_problems = diff(EXPECTED_CRITERIA, parse_ledger(""))
    if len(empty_problems) != len(EXPECTED_CRITERIA):
        failures += 1
        print(f"FAIL: an empty ledger should report every criterion missing, got {empty_problems}")

    print(f"self-test: {len(cases) + 2} cases, {failures} failures")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

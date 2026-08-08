#!/usr/bin/env python3
"""Report which release the ladder in REL-001 currently earns, and refuse a tag it does not.

The ladder was written down in TODOS.md and nowhere else, which made it a
statement of intent rather than a gate: nothing stopped a tag being cut while
the milestone it depends on still had open work, and a version number is the
one claim a consumer cannot inspect for themselves.

This reads the milestone headings and checkboxes out of TODOS.md, computes the
highest rung whose prerequisite milestones are fully closed, and compares that
against the tags that exist.

    python scripts/release_gate.py            # report
    python scripts/release_gate.py --check    # exit 1 if a tag is not earned

A milestone counts as closed when it has no unchecked boxes. Tasks marked
[LIVE] still count: a live test nobody can run is still a claim nobody has
verified, and skipping them here would let the ladder certify exactly the
things it exists to gate.
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

TODOS = Path(__file__).resolve().parent.parent / "TODOS.md"
ADR_DIR = Path(__file__).resolve().parent.parent / "docs" / "adr"

# REL-001's ladder, including its Revised restatement. Each rung names the
# milestones that must be closed before it may be tagged.
LADDER = [
    ("v0.2.0", ["M00", "M01", "M02"]),
    ("v0.3.0", ["M00", "M01", "M02", "M03", "M04", "M05"]),
    ("v0.4.0", ["M00", "M01", "M02", "M03", "M04", "M05", "M06", "M07"]),
    ("v0.5.0", ["M00", "M01", "M02", "M03", "M04", "M05", "M06", "M07",
                "M08", "M09", "M10", "M11", "M12"]),
    ("v0.9.0-rc", ["M00", "M01", "M02", "M03", "M04", "M05", "M06", "M07",
                   "M08", "M09", "M10", "M11", "M12", "M13", "M14", "M15", "M16"]),
    ("v1.0.0", ["M00", "M01", "M02", "M03", "M04", "M05", "M06", "M07",
                "M08", "M09", "M10", "M11", "M12", "M13", "M14", "M15", "M16", "M17"]),
]

MILESTONE = re.compile(r"^# (M\d+)\b")
OPEN_BOX = re.compile(r"^- \[ \]")
DONE_BOX = re.compile(r"^- \[x\]")

# RC-003's section 19 acceptance ledger lives inside M17 and is written with the
# same checkbox syntax as a task, so this counted its unchecked criteria as open
# milestone work. That is a category error, and it made v1.0.0 unreachable by
# construction: two of those criteria need `-race` on a platform the maintainer
# does not have and one needs six vendors' credentials, so the ledger will never
# be all-checked and the rung would never be earned however much work got done.
#
# Ledger lines are excluded from the milestone count and gated separately, and
# harder -- see acceptance_ok below. The ledger is not unfinished work; it is a
# published statement of what a release does and does not deliver, and the right
# question to ask of it is "does every unchecked box carry a reason and an ADR",
# not "is it empty".
LEDGER_LINE = re.compile(r"^- \[[ x]\] 19\.\d+\.\d+ ")


def milestone_status(text: str) -> dict[str, tuple[int, int]]:
    """Return {milestone: (open, done)}."""
    status: dict[str, list[int]] = {}
    current: str | None = None

    for line in text.splitlines():
        heading = MILESTONE.match(line)
        if heading:
            current = heading.group(1)
            status.setdefault(current, [0, 0])
            continue
        if current is None:
            continue
        if LEDGER_LINE.match(line):
            continue
        if OPEN_BOX.match(line):
            status[current][0] += 1
        elif DONE_BOX.match(line):
            status[current][1] += 1

    return {name: (counts[0], counts[1]) for name, counts in status.items()}


def earned(status: dict[str, tuple[int, int]]) -> tuple[str | None, list[str]]:
    """Return the highest earned rung and the milestones blocking the next one."""
    highest: str | None = None
    blocking: list[str] = []

    for version, required in LADDER:
        unmet = [name for name in required if status.get(name, (1, 0))[0] > 0]
        if unmet:
            return highest, unmet
        highest = version

    return highest, []


def existing_tags() -> list[str]:
    try:
        out = subprocess.run(
            ["git", "tag", "--list"], capture_output=True, text=True, check=True
        ).stdout
    except (subprocess.CalledProcessError, FileNotFoundError):
        return []
    return [line.strip() for line in out.splitlines() if line.strip()]


def rung_index(version: str) -> int:
    for index, (name, _) in enumerate(LADDER):
        if name == version:
            return index
    return -1


def acceptance_ok(text: str) -> tuple[bool, list[str]]:
    """RC-003's rule, enforced: every unchecked section 19 criterion at v1.0.0
    must cite an ADR document that exists.

    This is the compensating check for excluding ledger lines from the milestone
    count. Without it, dropping them would be a straightforward weakening -- the
    gate would stop noticing the ledger entirely. With it the ledger is checked
    on the thing that actually matters, which is not whether a box is ticked but
    whether an untricked one is *accounted for*: a stated reason, and a document
    a reader can go and disagree with.

    A reason alone is not enough at v1.0.0. Inline prose is easy to write and
    easy to leave stale; an ADR has a date, a decision, and consequences, and
    somebody has to look at it again to change it.
    """
    problems: list[str] = []
    for line in text.splitlines():
        if not LEDGER_LINE.match(line) or not line.startswith("- [ ]"):
            continue

        # "- [ ] 19.1.1 - ..." splits to [-, [, ], 19.1.1], so the id is index 3.
        criterion = line.split()[3]
        if "ADR:" not in line:
            problems.append(f"{criterion}: unchecked with no ADR citation")
            continue

        cited = re.findall(r"docs/adr/(\d{4})[-\w]*\.md", line)
        if not cited:
            problems.append(
                f"{criterion}: cites a reason but names no ADR document; "
                "at v1.0.0 the reason has to live somewhere a reader can find it"
            )
            continue

        for number in cited:
            if not list(ADR_DIR.glob(f"{number}-*.md")):
                problems.append(f"{criterion}: cites docs/adr/{number}-*.md, which does not exist")

    return not problems, problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if a tag exists that the ladder has not earned",
    )
    args = parser.parse_args()

    todos = TODOS.read_text(encoding="utf-8")
    status = milestone_status(todos)
    highest, blocking = earned(status)

    # The section 19 gate. It applies from v1.0.0 onward, because that is the
    # rung RC-003 attaches it to: before 1.0 the ledger is a plan, at 1.0 it is
    # a promise, and a promise with an unexplained gap in it is the thing this
    # refuses.
    accepted, acceptance_problems = acceptance_ok(todos)
    if highest is not None and rung_index(highest) >= rung_index("v1.0.0") and not accepted:
        highest = LADDER[rung_index("v1.0.0") - 1][0]
        blocking = ["section 19 acceptance ledger"]

    print("milestones:")
    for name in sorted(status, key=lambda n: int(n[1:])):
        opened, done = status[name]
        mark = "closed" if opened == 0 else f"{opened} open"
        print(f"  {name}: {done} done, {mark}")

    print()
    if highest is None:
        print("earned release: none yet")
    else:
        print(f"earned release: {highest}")
    if blocking:
        nxt = LADDER[0][0] if highest is None else LADDER[rung_index(highest) + 1][0]
        print(f"next rung {nxt} is blocked on: {', '.join(blocking)}")

    tags = existing_tags()
    print(f"tags present: {', '.join(tags) if tags else 'none'}")

    if acceptance_problems:
        print()
        print("section 19 ledger problems (these block v1.0.0 and above):")
        for problem in acceptance_problems:
            print(f"  {problem}")

    if not args.check:
        return 0

    unearned = []
    ceiling = -1 if highest is None else rung_index(highest)
    for tag in tags:
        index = rung_index(tag)
        if index == -1:
            # A tag outside the ladder is somebody else's scheme, not this
            # gate's business to police.
            continue
        if index > ceiling:
            unearned.append(tag)

    if acceptance_problems:
        print()
        print("REFUSED: the section 19 ledger is not accounted for; v1.0.0 cannot be earned.")
        return 1

    if unearned:
        print()
        print("REFUSED: these tags are further along the ladder than the work is:")
        for tag in unearned:
            print(f"  {tag}")
        print("Close the milestone or delete the tag; a version is a claim a consumer cannot check.")
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())

"""Trace every finding and every specification gap to the task that addresses it.

Two documents feed TODOS.md: the adversarial review (findings) and
to-production.md (the ARC/PRD/API/EXE/TRU gap registers). Both relationships
are only useful if they are checkable: an item with no task is a defect nobody
scheduled, and a task citing an item that does not exist is a dangling
reference.

TR-001 covered the review. TR-002 adds the gap registers, which were traced by
hand into TODOS.md's coverage table -- a table that would have drifted within a
week, because nothing checked it and nobody would have noticed.

One collision is handled deliberately. The specification labels its decisions
`D-001` with three digits, while the review numbers schema findings `D-01`
through `D-15` with two. They are near enough that a careless pattern reads one
as the other, so the gap-ID pattern below requires exactly two digits and the
five known register prefixes, rather than matching any letters-dash-digits.
"""

import io, re, sys, collections

REVIEW = "docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md"
SPEC = "docs/engineering/plans/to-production.md"
TODOS = "TODOS.md"

# The five gap registers. Two digits exactly: see the D-001 collision above.
GAP_PREFIXES = ("ARC", "PRD", "API", "EXE", "TRU")
GAP_ID = re.compile(r"^(?:%s)-\d{2}$" % "|".join(GAP_PREFIXES))

review = io.open(REVIEW, encoding="utf-8").read()
spec = io.open(SPEC, encoding="utf-8").read()
todos = io.open(TODOS, encoding="utf-8").read()

# Gaps: table rows "| ARC-01 | Client isolation | ... | ... |"
gaps = {}
for match in re.finditer(r"^\|\s*((?:%s)-\d{2})\s*\|\s*([^|]+?)\s*\|" % "|".join(GAP_PREFIXES),
                         spec, re.M):
    gid, title = match.groups()
    gaps[gid] = {"title": title.strip()}

# Findings: "## I-01 — title `S1` `OPEN`"
findings = {}
# Status is optional: the Gaps and control-flow sections carry a severity but
# no OPEN/CLOSED marker.
for match in re.finditer(r"^## ([A-Za-z]+-\d+) — (.+?)\s*`(S[123])`(?:\s*`(\w+)`)?\s*$", review, re.M):
    fid, title, severity, status = match.groups()
    findings[fid] = {"title": title.strip(), "severity": severity, "status": status or "OPEN"}

# Tasks: "- [x] **F-001** — ..." with their bodies, up to the next task or heading.
task_blocks = {}
for match in re.finditer(r"^- \[([ x])\] \*\*([A-Z]+-\d+)\*\*(.*?)(?=^- \[[ x]\] \*\*|^#|\Z)",
                         todos, re.M | re.S):
    done, tid, body = match.groups()
    task_blocks[tid] = {"done": done == "x", "body": body}

# Which findings does each task claim to address? Tasks cite them as **I-01**
# or plain I-01 inside their body.
covered = collections.defaultdict(list)
for tid, block in task_blocks.items():
    for fid in set(re.findall(r"\b([A-Za-z]+-\d+)\b", block["body"])):
        if fid in findings:
            covered[fid].append(tid)

# Evidence rows also cite findings.
for match in re.finditer(r"^\| \*\*([A-Z][A-Z0-9-]*)\*\* \| `[^`]*` \| (.+?) \|$", todos, re.M):
    tid, evidence = match.groups()
    for fid in set(re.findall(r"\b([A-Za-z]+-\d+)\b", evidence)):
        if fid in findings and tid not in covered[fid]:
            covered[fid].append(tid)

# Which gaps does each task cite? Same rule as findings: anywhere in the body.
gap_covered = collections.defaultdict(list)
for tid, block in task_blocks.items():
    for gid in set(re.findall(r"\b([A-Z]{3}-\d{2})\b", block["body"])):
        if gid in gaps:
            gap_covered[gid].append(tid)

# The coverage table in TODOS.md cites them too, in "| ARC-02 | IN-004 | ..." rows.
for match in re.finditer(r"^\|\s*((?:%s)-\d{2})\s*\|\s*([^|]+?)\s*\|" % "|".join(GAP_PREFIXES),
                         todos, re.M):
    gid, cited = match.groups()
    if gid not in gaps:
        continue
    for tid in re.findall(r"\b([A-Z]{1,3}-\d{3}|[A-Z]{1,2}-\d{2,3})\b", cited):
        if tid in task_blocks and tid not in gap_covered[gid]:
            gap_covered[gid].append(tid)

uncovered = sorted(f for f in findings if not covered[f])
uncovered_gaps = sorted(g for g in gaps if not gap_covered[g])

# A task citing a gap ID that no register defines.
cited_gaps = set()
for block in task_blocks.values():
    cited_gaps |= set(re.findall(r"\b([A-Z]{3}-\d{2})\b", block["body"]))
dangling_gaps = sorted(g for g in cited_gaps if GAP_ID.match(g) and g not in gaps)
closed_by_done = []
for fid, tids in covered.items():
    if tids and all(task_blocks.get(t, {}).get("done") for t in tids):
        closed_by_done.append(fid)

# Dangling: a task citing a finding ID that does not exist in the review.
all_cited = set()
for block in task_blocks.values():
    all_cited |= set(re.findall(r"\b([A-Za-z]+-\d+)\b", block["body"]))
known_task_ids = set(task_blocks)
dangling = sorted(c for c in all_cited
                  if re.match(r"^[A-Z]{1,2}-\d+$", c)
                  and c not in findings and c not in known_task_ids)

by_severity = collections.Counter(f["severity"] for f in findings.values())
uncovered_by_severity = collections.Counter(findings[f]["severity"] for f in uncovered)

print(f"review findings: {len(findings)}  ({dict(sorted(by_severity.items()))})")
print(f"specification gaps: {len(gaps)}")
print(f"tasks in TODOS.md: {len(task_blocks)}")
print(f"findings traced to a task: {len(findings) - len(uncovered)}")
print(f"findings addressed by a CLOSED task: {len(closed_by_done)}")
print(f"gaps traced to a task: {len(gaps) - len(uncovered_gaps)}")
print()

if uncovered:
    print(f"UNTRACED FINDINGS ({len(uncovered)}) — {dict(sorted(uncovered_by_severity.items()))}:")
    for fid in uncovered:
        f = findings[fid]
        print(f"  {fid} [{f['severity']}] {f['title'][:88]}")
else:
    print("every finding traces to at least one task")

if dangling:
    print(f"\nDANGLING REFERENCES ({len(dangling)}): {', '.join(dangling)}")

if uncovered_gaps:
    print("")
    print(f"UNTRACED SPECIFICATION GAPS ({len(uncovered_gaps)}):")
    for gid in uncovered_gaps:
        print(f"  {gid} {gaps[gid]['title'][:88]}")
else:
    print("every specification gap traces to at least one task")

if dangling_gaps:
    print(f"DANGLING GAP REFERENCES ({len(dangling_gaps)}): {', '.join(dangling_gaps)}")

sys.exit(1 if (uncovered or uncovered_gaps or dangling_gaps) else 0)

"""Trace every finding in the adversarial review to the task that addresses it.

The review is the input to TODOS.md. That relationship is only useful if it is
checkable: a finding with no task is a defect nobody scheduled, and a task
citing a finding that does not exist is a dangling reference.
"""

import io, re, sys, collections

REVIEW = "docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md"
TODOS = "TODOS.md"

review = io.open(REVIEW, encoding="utf-8").read()
todos = io.open(TODOS, encoding="utf-8").read()

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

uncovered = sorted(f for f in findings if not covered[f])
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
print(f"tasks in TODOS.md: {len(task_blocks)}")
print(f"findings traced to a task: {len(findings) - len(uncovered)}")
print(f"findings addressed by a CLOSED task: {len(closed_by_done)}")
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

sys.exit(1 if uncovered else 0)

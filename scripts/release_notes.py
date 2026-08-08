#!/usr/bin/env python3
"""Assemble §17.2's release contents from evidence in the repository.

RC-001 lists thirteen things a release has to state. Most of them already
exist somewhere in this tree as a fact -- the API surface snapshot, the golden
prompts, the deprecation catalogue, the §19 ledger -- and the failure mode
release notes actually have is not that these facts are unavailable, it is
that a human transcribes them and the transcription drifts. So this generates
them instead of prompting somebody to remember.

The one that motivated the task is the semantic behaviour section. A prompt
edit changes what this library does and changes no Go signature, so a
changelog built from `git log` and an API diff will report a release with no
behaviour change while every answer the library produces has moved. That
section is computed from `testdata/golden_prompts.txt`, which is exactly the
snapshot that exists to make prompt edits visible.

    python scripts/release_notes.py                 # notes for HEAD vs the last tag
    python scripts/release_notes.py --since v0.1.0  # against an explicit base
    python scripts/release_notes.py --check         # exit 1 if any section is unavailable

Never fail open: a section this cannot compute is printed as UNAVAILABLE with
the reason, never omitted and never filled with a plausible default. An
omitted section reads as "nothing changed"; that is the specific lie this is
built to avoid. `--check` turns any UNAVAILABLE section into a non-zero exit,
so a release process can require completeness without this script having to
guess what completeness means.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

API_SURFACE = "testdata/api_surface.txt"
GOLDEN_PROMPTS = "testdata/golden_prompts.txt"


class Unavailable(Exception):
    """A section that cannot be computed, carrying why."""


def git(*args, check=True):
    result = subprocess.run(
        ["git", *args], cwd=ROOT, capture_output=True, text=True,
    )
    if check and result.returncode != 0:
        raise Unavailable("git %s failed: %s" % (" ".join(args), result.stderr.strip()))
    return result.stdout


def tags():
    out = git("tag", "--list", "--sort=-v:refname", check=False)
    return [line.strip() for line in out.splitlines() if line.strip()]


def resolve_base(explicit):
    if explicit:
        return explicit
    existing = tags()
    if not existing:
        raise Unavailable(
            "no tags exist, so there is no previous release to compare against; "
            "pass --since <ref> to name a base explicitly"
        )
    return existing[0]


def file_at(ref, path):
    """Return a file's contents at a ref, or None if it did not exist there."""
    result = subprocess.run(
        ["git", "show", "%s:%s" % (ref, path)], cwd=ROOT,
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        return None
    return result.stdout


def working_file(path):
    full = ROOT / path
    if not full.exists():
        return None
    return full.read_text(encoding="utf-8")


# --- The sections. Each returns a list of lines, or raises Unavailable.


def section_artifacts(base, head):
    """Tagged and checksummed artifacts.

    A Go library's artifact is its module contents, so the checksum that means
    anything is over the tracked source at the ref -- not over a built binary,
    which this project does not ship. Computed from `git ls-tree`, so it covers
    exactly what a consumer receives from the proxy and nothing from a dirty
    working tree.
    """
    listing = git("ls-tree", "-r", "--name-only", head)
    names = sorted(line.strip() for line in listing.splitlines() if line.strip())
    if not names:
        raise Unavailable("git ls-tree returned nothing for %s" % head)

    digest = hashlib.sha256()
    for name in names:
        blob = subprocess.run(
            ["git", "show", "%s:%s" % (head, name)], cwd=ROOT,
            capture_output=True,
        )
        if blob.returncode != 0:
            continue
        digest.update(name.encode("utf-8"))
        digest.update(b"\0")
        digest.update(blob.stdout)

    return [
        "Ref: %s" % head,
        "Tracked files: %d" % len(names),
        "Source digest (sha256 over tracked contents): %s" % digest.hexdigest(),
        "",
        "This is a Go module: the artifact is the module contents, not a built",
        "binary. The digest covers tracked files at the ref and deliberately",
        "ignores the working tree.",
    ]


def section_changelog(base, head):
    out = git("log", "--no-merges", "--format=%h %s", "%s..%s" % (base, head))
    entries = [line.strip() for line in out.splitlines() if line.strip()]
    if not entries:
        return ["No commits between %s and %s." % (base, head)]
    return entries


def section_go_api(base, head):
    """Go API changes, from the ratcheted public-surface snapshot."""
    before = file_at(base, API_SURFACE)
    after = file_at(head, API_SURFACE) or working_file(API_SURFACE)
    if after is None:
        raise Unavailable("%s does not exist at %s" % (API_SURFACE, head))
    if before is None:
        return ["%s did not exist at %s; every entry is new." % (API_SURFACE, base)]

    old_lines = set(before.splitlines())
    new_lines = set(after.splitlines())

    added = sorted(new_lines - old_lines)
    removed = sorted(old_lines - new_lines)

    if not added and not removed:
        return ["No change to the public Go API."]

    lines = []
    if removed:
        lines.append("REMOVED (%d) -- breaking for any caller using them:" % len(removed))
        lines.extend("  - %s" % entry for entry in removed)
    if added:
        lines.append("ADDED (%d):" % len(added))
        lines.extend("  + %s" % entry for entry in added)
    return lines


def section_semantic_behaviour(base, head):
    """The section RC-001 exists for.

    A prompt edit changes every answer this library produces and changes no Go
    signature. Nothing in a conventional changelog would show it, so it is read
    from the golden-prompt snapshot, which is the artifact that exists to make
    exactly this visible.
    """
    before = file_at(base, GOLDEN_PROMPTS)
    after = file_at(head, GOLDEN_PROMPTS) or working_file(GOLDEN_PROMPTS)
    if after is None:
        raise Unavailable("%s does not exist at %s" % (GOLDEN_PROMPTS, head))
    if before is None:
        return ["%s did not exist at %s; every prompt is new." % (GOLDEN_PROMPTS, base)]

    if before == after:
        return [
            "No prompt changed. Every operation asks its provider the same",
            "question it asked in %s." % base,
        ]

    old_lines = before.splitlines()
    new_lines = after.splitlines()
    added = sum(1 for line in new_lines if line not in old_lines)
    removed = sum(1 for line in old_lines if line not in new_lines)

    # Which operations moved: the snapshot's section headers name them.
    header = re.compile(r"^---\s*(\S+)")
    def sections(lines):
        current = None
        found = {}
        for line in lines:
            match = header.match(line)
            if match:
                current = match.group(1)
                found.setdefault(current, [])
            elif current is not None:
                found[current].append(line)
        return found

    old_sections = sections(old_lines)
    new_sections = sections(new_lines)
    moved = sorted(
        name for name in set(old_sections) | set(new_sections)
        if old_sections.get(name) != new_sections.get(name)
    )

    lines = [
        "PROMPTS CHANGED. This is a behaviour change with no Go signature",
        "change, and it is the one a changelog built from commits would miss.",
        "",
        "Lines added: %d, removed: %d" % (added, removed),
    ]
    if moved:
        lines.append("Sections affected (%d): %s" % (len(moved), ", ".join(moved)))
    else:
        lines.append(
            "The snapshot moved but no section header could be attributed; "
            "review the diff of %s directly." % GOLDEN_PROMPTS
        )
    return lines


def section_operation_versions(base, head):
    """Operation, prompt, and schema version changes.

    Read from the declared OperationID versions in the source, because those
    are what a stored result's provenance records -- a version bumped in a doc
    and not in the declaration would be a version nothing actually stamps.
    """
    pattern = re.compile(r'OperationID\{Name:\s*"([^"]+)",\s*Version:\s*"([^"]+)"')

    def versions(ref):
        out = subprocess.run(
            ["git", "grep", "-h", "-o", "-E", r'OperationID\{[^}]*\}', ref, "--", "*.go"],
            cwd=ROOT, capture_output=True, text=True,
        )
        if out.returncode not in (0, 1):
            return None
        found = {}
        for line in out.stdout.splitlines():
            match = pattern.search(line)
            if match:
                found[match.group(1)] = match.group(2)
        return found

    after = versions(head)
    if after is None:
        raise Unavailable("could not read operation declarations at %s" % head)
    before = versions(base)
    if before is None:
        return ["No comparable declarations at %s." % base]

    changed = sorted(
        name for name in set(before) | set(after)
        if before.get(name) != after.get(name)
    )
    if not changed:
        return ["No operation version changed (%d declared)." % len(after)]
    return [
        "%s: %s -> %s" % (name, before.get(name, "(absent)"), after.get(name, "(absent)"))
        for name in changed
    ]


def section_provider_matrix(base, head):
    """Provider capability and live-verification matrix.

    Capabilities are declared in the source and readable. Live verification is
    not: it requires a funded credential and a run nobody has made here, and
    reporting an unverified provider as verified is the exact failure this
    library's rules forbid. So the capability half is computed and the live
    half is reported as what it is.
    """
    provider_source = ROOT / "internal" / "llm" / "provider.go"
    if not provider_source.exists():
        raise Unavailable("internal/llm/provider.go does not exist")

    # The names come from the built-in factory registry rather than a list kept
    # beside it: a provider somebody registers is a provider this reports,
    # without anyone remembering to update a second place. CreateProvider
    # itself delegates to the registry and names nothing, which is why reading
    # its body finds none -- a mistake worth leaving recorded, because the
    # obvious place to look was the wrong one.
    text = provider_source.read_text(encoding="utf-8")
    marker = text.find("func registerBuiltInProviderFactories")
    providers = []
    if marker >= 0:
        block = text[marker:marker + 3000]
        providers = sorted(set(re.findall(r'"([a-z0-9_-]+)":\s*func\(config ProviderConfig\)', block)))

    capabilities = ROOT / "internal" / "llm" / "capabilities.go"
    declares = capabilities.exists()

    live_dir = ROOT / ".audit" / "live"
    records = sorted(live_dir.glob("*.json")) if live_dir.exists() else []

    lines = [
        "Providers CreateProvider can build: %s" %
        (", ".join(providers) if providers else "(none found -- the switch could not be read)"),
        "Capability declarations: %s" % ("internal/llm/capabilities.go" if declares else "ABSENT"),
        "",
    ]
    if not records:
        lines.append("LIVE VERIFICATION: none recorded. No provider in this release has been")
        lines.append("verified against its real endpoint -- a gap, not a pass. See 19.5.2 in")
        lines.append("TODOS.md's acceptance ledger.")
    else:
        lines.append("Local live-verification records found: %s" %
                     ", ".join(r.name for r in records))
        lines.append("")
        lines.append("These are NOT part of the release: .audit/live/ is gitignored, so they")
        lines.append("exist on one developer's machine and no consumer can see them. A record")
        lines.append("nobody else can read is not verification a release may claim.")
    return lines


def section_platform_matrix(base, head):
    workflow = ROOT / ".github" / "workflows" / "ci.yml"
    if not workflow.exists():
        raise Unavailable(".github/workflows/ci.yml does not exist")
    text = workflow.read_text(encoding="utf-8")

    # `runs-on: ${{ matrix.os }}` names nothing; the matrix does. Literal
    # runs-on values are collected too, for the jobs that do not use a matrix.
    matrix_os = re.findall(r"^\s*os:\s*\[([^\]]+)\]", text, re.M)
    matrix_go = re.findall(r"^\s*go:\s*\[([^\]]+)\]", text, re.M)
    literal_runners = re.findall(r"runs-on:\s*([A-Za-z][\w.-]*)", text)

    runners = sorted({entry.strip() for group in matrix_os for entry in group.split(",")} |
                     set(literal_runners))
    go_versions = sorted({entry.strip() for group in matrix_go for entry in group.split(",")} |
                         set(re.findall(r"go-version:\s*([a-z][a-z]*)\s*$", text, re.M)))

    gomod = ROOT / "go.mod"
    declared = ""
    if gomod.exists():
        match = re.search(r"^go\s+(\S+)", gomod.read_text(encoding="utf-8"), re.M)
        if match:
            declared = match.group(1)

    lines = [
        "go.mod declares: go %s" % (declared or "(unread)"),
        "CI runners: %s" % (", ".join(runners) if runners else "(none found)"),
        "CI Go versions: %s" % (", ".join(go_versions) if go_versions else "(none pinned)"),
        "",
        "Not covered by CI: windows/arm64, which is where this library is",
        "developed and where `-race` does not run. A platform passing here has",
        "not been shown to pass there.",
    ]
    return lines


def section_known_degradations(base, head):
    """Known degradations, read from the §19 acceptance ledger.

    An unchecked acceptance criterion IS a known degradation, stated by
    somebody who had to give a reason for it. Transcribing them by hand into
    release notes is how the two lists drift apart.
    """
    todos = ROOT / "TODOS.md"
    if not todos.exists():
        raise Unavailable("TODOS.md does not exist")

    ledger = re.compile(r"^- \[( |x)\] (19\.\d+\.\d+) — (.+)$")
    unmet = []
    for line in todos.read_text(encoding="utf-8").splitlines():
        match = ledger.match(line.rstrip())
        if match and match.group(1) == " ":
            unmet.append("%s — %s" % (match.group(2), match.group(3)))

    if not ledger:
        raise Unavailable("no §19 ledger found in TODOS.md")
    if not unmet:
        return ["Every §19 acceptance criterion is met."]
    return ["%d of the v1.0.0 acceptance criteria are unmet:" % len(unmet)] + \
           ["  - %s" % entry for entry in unmet]


def section_migration(base, head):
    """Migration steps, from the deprecation catalogue rather than prose."""
    catalog = ROOT / "catalog.go"
    if not catalog.exists():
        raise Unavailable("catalog.go does not exist")

    text = catalog.read_text(encoding="utf-8")
    # The deprecated set is a map literal read by register(), so that literal is
    # the source of truth -- reading the CatalogEntry struct instead would find
    # nothing, because the entries are built at run time.
    block = re.search(r"deprecated\s*:=\s*map\[string\]string\{(.*?)\n\t\}", text, re.S)
    deprecated = []
    if block:
        deprecated = re.findall(r'"([^"]+)":\s*"([^"]*)"', block.group(1))
    if not deprecated:
        return ["No deprecated operations; nothing to migrate."]
    return ["%s -> %s" % (name, replacement or "(no replacement named -- this is a bug)")
            for name, replacement in deprecated]


def section_sbom(base, head):
    if shutil.which("go") is None:
        raise Unavailable("the go toolchain is not on PATH")
    result = subprocess.run(
        ["go", "list", "-m", "-json", "all"], cwd=ROOT,
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise Unavailable("go list -m failed: %s" % result.stderr.strip()[:200])

    modules = []
    decoder = json.JSONDecoder()
    text = result.stdout.strip()
    index = 0
    while index < len(text):
        try:
            obj, offset = decoder.raw_decode(text, index)
        except json.JSONDecodeError:
            break
        modules.append(obj)
        index = offset
        while index < len(text) and text[index] in " \r\n\t":
            index += 1

    if not modules:
        raise Unavailable("go list -m produced no parseable modules")

    lines = ["%d modules in the build list:" % len(modules)]
    for module in modules:
        version = module.get("Version") or "(main)"
        lines.append("  %s %s" % (module.get("Path", "?"), version))
    return lines


def section_vulnerabilities(base, head):
    if shutil.which("govulncheck") is None:
        raise Unavailable(
            "govulncheck is not installed, so this release has NOT been scanned. "
            "Install it (go install golang.org/x/vuln/cmd/govulncheck@latest) and "
            "re-run; do not ship claiming a scan that did not happen"
        )
    result = subprocess.run(
        ["govulncheck", "./..."], cwd=ROOT, capture_output=True, text=True,
    )
    if result.returncode == 0:
        return ["govulncheck reported no vulnerabilities affecting this module."]
    return ["govulncheck reported findings (exit %d):" % result.returncode] + \
           ["  %s" % line for line in result.stdout.splitlines()[:40]]


def section_semantic_benchmark(base, head):
    raise Unavailable(
        "no release-candidate semantic benchmark exists (RC-002 is open). "
        "There is no baseline, so there is nothing to compare a candidate against"
    )


SECTIONS = [
    ("Artifacts and checksums", section_artifacts),
    ("Changelog", section_changelog),
    ("Go API changes", section_go_api),
    ("Semantic behaviour changes", section_semantic_behaviour),
    ("Operation, prompt, and schema versions", section_operation_versions),
    ("Provider capability and live verification", section_provider_matrix),
    ("Platform support", section_platform_matrix),
    ("Known degradations", section_known_degradations),
    ("Migration steps", section_migration),
    ("SBOM", section_sbom),
    ("Vulnerability scan", section_vulnerabilities),
    ("Semantic benchmark comparison", section_semantic_benchmark),
]


def main():
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--since", help="base ref to compare against (default: newest tag)")
    parser.add_argument("--head", default="HEAD", help="ref to describe (default: HEAD)")
    parser.add_argument(
        "--check", action="store_true",
        help="exit 1 if any section could not be computed",
    )
    args = parser.parse_args()

    try:
        base = resolve_base(args.since)
    except Unavailable as reason:
        # Without a base, the diffing sections cannot run at all. Say so once,
        # rather than repeating the same reason under six headings.
        print("Cannot generate release notes: %s" % reason)
        return 1 if args.check else 0

    print("Release notes: %s..%s" % (base, args.head))
    print("=" * 72)

    unavailable = []
    for title, build in SECTIONS:
        print()
        print("## %s" % title)
        try:
            for line in build(base, args.head):
                print(line)
        except Unavailable as reason:
            print("UNAVAILABLE: %s" % reason)
            unavailable.append(title)

    print()
    print("=" * 72)
    if unavailable:
        print("%d of %d sections could not be computed: %s" %
              (len(unavailable), len(SECTIONS), ", ".join(unavailable)))
        print("These are stated rather than omitted on purpose: an omitted section")
        print("reads as 'nothing changed', which is a claim nobody made.")
        return 1 if args.check else 0

    print("All %d sections computed." % len(SECTIONS))
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Check the compatibility and deprecation policy (RC-004) against real source.

RC-004 asks for: a documented window for a deprecated stable API, a
deprecation notice that names a mechanical replacement, and removal only at a
major version. `catalog.go` already records which operations are
`TierDeprecated` and what replaces them (`CatalogEntry.ReplacedBy`) -- PS-002
built that table so a caller could ask "what should I use instead" without a
search. What nothing checked is whether the *source* backs the table up: a
name the catalogue calls deprecated is a broken promise if nothing near its
declaration says so, and a `// Deprecated:` comment that never names the
replacement is not the "mechanical replacement" RC-004 asks for -- it is a
warning with no next step.

This script is the check. It reads catalog.go's `deprecated` map (read only --
catalog.go is out of scope to edit for this task) and, for every entry, looks
for a `// Deprecated:` comment attached to a `func Name(...)` declaration
somewhere in the tracked Go source that names the catalogued replacement.

    python scripts/deprecation_policy.py            # report
    python scripts/deprecation_policy.py --check    # exit 1 on a violation
    python scripts/deprecation_policy.py --self-test

A catalogued name only needs the marker in *one* place it is declared --
this library keeps a public alias, an internal/ops implementation, and
sometimes an internal/api/fluent copy, and RC-004 is satisfied if a caller
reading any of them is told what to use instead.
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CATALOG = ROOT / "catalog.go"

# Files that are not this library's own API surface: vendored/copied
# third-party source under examples/. A `// Deprecated:` marker there belongs
# to whatever project wrote it, and this policy has no opinion about it.
EXCLUDED_PREFIXES = ("examples/smarttodo/wasmdeps/",)

FUNC_DECL = re.compile(r"^func\s+(\w+)[\[(]")
DEPRECATED_MAP = re.compile(r"deprecated\s*:=\s*map\[string\]string\{(.*?)\n\t\}", re.S)
DEPRECATED_ENTRY = re.compile(r'"([A-Za-z0-9_]+)"\s*:\s*"([A-Za-z0-9_]+)"')


def parse_deprecated_map(catalog_text: str) -> dict[str, str]:
    """Extract the {name: replacement} pairs from catalog.go's deprecated map."""
    match = DEPRECATED_MAP.search(catalog_text)
    if not match:
        return {}
    return dict(DEPRECATED_ENTRY.findall(match.group(1)))


def find_marker(name: str, replacement: str, source_text: str) -> bool:
    """Report whether source_text declares `name` with a Deprecated comment
    naming `replacement`.

    Walks backward from the func line over contiguous `//` comment lines --
    a Go doc comment -- and checks the whole block for both the
    "Deprecated:" marker godoc recognises and the replacement's name as a
    whole word, so a comment that merely says "use something else" does not
    count as naming a mechanical replacement.
    """
    lines = source_text.splitlines()
    replacement_pattern = re.compile(r"\b" + re.escape(replacement) + r"\b")

    for i, line in enumerate(lines):
        decl = FUNC_DECL.match(line)
        if not decl or decl.group(1) != name:
            continue

        j = i - 1
        comment_lines: list[str] = []
        while j >= 0 and lines[j].lstrip().startswith("//"):
            comment_lines.insert(0, lines[j])
            j -= 1
        comment_text = "\n".join(comment_lines)

        if "Deprecated:" in comment_text and replacement_pattern.search(comment_text):
            return True

    return False


def tracked_go_files() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "*.go"], capture_output=True, text=True, check=True, cwd=ROOT
    ).stdout
    files = []
    for line in out.splitlines():
        path = line.strip()
        if not path or path.endswith("_test.go"):
            continue
        if any(path.startswith(prefix) for prefix in EXCLUDED_PREFIXES):
            continue
        files.append(path)
    return files


def check_repo() -> tuple[dict[str, str], list[str]]:
    """Return (the deprecated map, the list of violation messages)."""
    catalog_text = CATALOG.read_text(encoding="utf-8")
    deprecated = parse_deprecated_map(catalog_text)

    violations = []
    for name, replacement in sorted(deprecated.items()):
        satisfied = False
        for path in tracked_go_files():
            text = (ROOT / path).read_text(encoding="utf-8", errors="replace")
            if find_marker(name, replacement, text):
                satisfied = True
                break
        if not satisfied:
            violations.append(
                f"{name}: catalog.go marks this TierDeprecated (-> {replacement}), but no "
                f"declaration of `func {name}` anywhere in the tracked source carries a "
                f"'// Deprecated:' comment naming {replacement}"
            )
    return deprecated, violations


# --- self-test -------------------------------------------------------------
#
# find_marker is the whole check, so the self-test proves it on fabricated
# source rather than on whatever this repository currently contains -- a
# self-test that reads real files would stop proving anything the moment the
# repository's own violations (if any) got fixed.

GOOD_SOURCE = """
package example

// SomeOther does something else entirely.
func SomeOther() {}

// Deprecated: use NewName, which returns a typed result instead of a string.
func OldName() (string, error) {
	return "", nil
}
"""

MISSING_MARKER_SOURCE = """
package example

// OldName still works but should not be used.
func OldName() (string, error) {
	return "", nil
}
"""

MARKER_WITHOUT_REPLACEMENT_SOURCE = """
package example

// Deprecated: this is old.
func OldName() (string, error) {
	return "", nil
}
"""

NOT_ATTACHED_SOURCE = """
package example

// Deprecated: use NewName instead.

func OldName() (string, error) {
	return "", nil
}
"""

SELF_TEST_CASES = [
    # (source, name, replacement, want, why)
    (GOOD_SOURCE, "OldName", "NewName", True, "marker present and attached, names the replacement"),
    (MISSING_MARKER_SOURCE, "OldName", "NewName", False, "no Deprecated marker at all"),
    (MARKER_WITHOUT_REPLACEMENT_SOURCE, "OldName", "NewName", False, "marker present but does not name the replacement"),
    (NOT_ATTACHED_SOURCE, "OldName", "NewName", False, "a blank line detaches the comment from the declaration"),
    (GOOD_SOURCE, "SomeOther", "NewName", False, "the marker belongs to a different function"),
]


def self_test() -> int:
    failures = 0
    for source, name, replacement, want, why in SELF_TEST_CASES:
        got = find_marker(name, replacement, source)
        if got != want:
            failures += 1
            print(f"FAIL ({why}): find_marker({name!r}, {replacement!r}) = {got}, want {want}")

    # The map parser, proven against a fixture shaped like catalog.go's own
    # block rather than the real file, so a future reshuffle of the real map
    # cannot make this pass by accident.
    fixture = """
package schemaflux

func init() {
	deprecated := map[string]string{
		"Foo": "Bar",
		"Baz": "Quux",
	}
	_ = deprecated
}
"""
    parsed = parse_deprecated_map(fixture)
    if parsed != {"Foo": "Bar", "Baz": "Quux"}:
        failures += 1
        print(f"FAIL: parse_deprecated_map = {parsed}, want {{'Foo': 'Bar', 'Baz': 'Quux'}}")

    # And the refusal this gate exists to prove: a catalog claiming a
    # deprecation the source does not carry must be reported as a violation,
    # not silently passed.
    fabricated_violations = []
    for name, replacement in {"Ghost": "Anything"}.items():
        if not find_marker(name, replacement, MISSING_MARKER_SOURCE.replace("OldName", "Ghost")):
            fabricated_violations.append(name)
    if fabricated_violations != ["Ghost"]:
        failures += 1
        print(f"FAIL: the gate did not refuse an undocumented deprecation: {fabricated_violations}")

    print(f"self-test: {len(SELF_TEST_CASES) + 2} cases, {failures} failures")
    return 1 if failures else 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="exit 1 on a violation")
    parser.add_argument("--self-test", action="store_true", help="run the fixture-based self-test")
    args = parser.parse_args()

    if args.self_test:
        return self_test()

    deprecated, violations = check_repo()

    print(f"deprecation policy: {len(deprecated)} entries in catalog.go's deprecated map")
    for name in sorted(deprecated):
        mark = "OK" if not any(v.startswith(name + ":") for v in violations) else "MISSING MARKER"
        print(f"  {name} -> {deprecated[name]}: {mark}")

    if violations:
        print()
        print("REFUSED: these catalogued deprecations have no matching source marker:")
        for v in violations:
            print(f"  {v}")

    if not args.check:
        return 0
    return 1 if violations else 0


if __name__ == "__main__":
    sys.exit(main())

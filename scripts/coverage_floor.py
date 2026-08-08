"""Fail if statement coverage drops below the recorded floor.

TODOS.md CI-005. The floor is ratcheted from what was measured, not set
aspirationally: a target nobody meets is one everybody learns to ignore, and a
floor that only ever moves up is the part that actually holds.

The measurement covers the library packages only. The forty-five numbered
examples are `main` packages with no tests, and including them drags the total
down by fifteen points that say nothing about whether the library is tested --
CI-004 is what makes the examples a gate, and it is a different kind of check.

Usage:
    python scripts/coverage_floor.py            # check against the floor
    python scripts/coverage_floor.py --update   # raise the floor to what is measured
"""

import io
import os
import re
import subprocess
import sys
import tempfile

FLOOR_FILE = ".audit/coverage_floor.txt"

# The library. Everything a consumer imports, and nothing that only exists to
# demonstrate it.
PACKAGES = [
    "./",
    "./internal/...",
    "./pricing/...",
    "./telemetry/...",
    "./schemafluxtest/...",
    "./mw/...",
]

# ./core/... and ./debug/... were here and are gone. Both were top-level
# packages that nothing in the module imported, and core/doc.go even declared
# itself "Package schemaflux" while the package clause said `core` -- a
# vestigial copy of the root package's doc comment. They contributed statements
# to this denominator without any consumer being able to reach them, which is
# the wrong direction for a coverage number to be wrong in.

# How far coverage may fall before the check fails.
#
# Not zero: coverage moves by a tenth of a point when an error branch is added
# for a case that cannot be triggered yet, and a check that fails on that
# teaches people to regenerate the floor rather than read it. A whole point is
# large enough to mean something was actually dropped.
TOLERANCE = 1.0


def measure():
    # tempfile rather than TMPDIR: on Windows that variable is often unset or
    # points somewhere `go` cannot write, and a coverage check that fails for a
    # path reason teaches people the check is broken.
    handle, profile = tempfile.mkstemp(prefix="schemaflux_coverage", suffix=".out")
    os.close(handle)

    result = subprocess.run(
        # -coverpkg is what keeps this number meaningful after the black-box
        # tests moved to ./tests/. Without it, `go test` credits coverage only
        # to the package under test, so 29 test files exercising the library
        # from an external package would have counted for nothing and the floor
        # would have collapsed for a reason that had nothing to do with the
        # tests. With it, coverage measures the library's statements regardless
        # of which package's tests reached them, which is the question the floor
        # was always asking.
        ["go", "test", "-coverpkg=" + ",".join(PACKAGES),
         "-coverprofile=" + profile] + PACKAGES + ["./tests/..."],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        print("the test run failed, so coverage was not measured:\n")
        print(result.stdout[-4000:])
        print(result.stderr[-2000:])
        return None

    report = subprocess.run(
        ["go", "tool", "cover", "-func=" + profile],
        capture_output=True, text=True, check=True,
    ).stdout

    try:
        os.unlink(profile)
    except OSError:
        pass

    match = re.search(r"^total:\s+\(statements\)\s+([\d.]+)%", report, re.M)
    if not match:
        print("could not read a total from `go tool cover`")
        return None

    return float(match.group(1))


def read_floor():
    try:
        with io.open(FLOOR_FILE, encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if line and not line.startswith("#"):
                    return float(line)
    except (OSError, ValueError):
        pass
    return None


def write_floor(value):
    os.makedirs(os.path.dirname(FLOOR_FILE), exist_ok=True)
    with io.open(FLOOR_FILE, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(
            "# Statement coverage floor for the library packages, ratcheted from\n"
            "# what was measured. See scripts/coverage_floor.py and TODOS.md CI-005.\n"
            "# Raise it with: python scripts/coverage_floor.py --update\n"
        )
        handle.write("%.1f\n" % value)


def main():
    measured = measure()
    if measured is None:
        return 1

    if "--update" in sys.argv:
        write_floor(measured)
        print("coverage floor set to %.1f%%" % measured)
        return 0

    floor = read_floor()
    if floor is None:
        write_floor(measured)
        print("no floor recorded; set to the measured %.1f%%" % measured)
        return 0

    print("coverage: %.1f%%  floor: %.1f%%  tolerance: %.1f" % (measured, floor, TOLERANCE))

    if measured < floor - TOLERANCE:
        print(
            "\nCoverage fell %.1f points below the floor.\n"
            "Either the new code needs tests, or something tested was deleted.\n"
            "If the drop is intended, say why in TODOS.md and run:\n"
            "    python scripts/coverage_floor.py --update"
            % (floor - measured)
        )
        return 1

    if measured > floor + TOLERANCE:
        print(
            "\nCoverage is %.1f points above the floor. Ratchet it up:\n"
            "    python scripts/coverage_floor.py --update"
            % (measured - floor)
        )

    return 0


if __name__ == "__main__":
    sys.exit(main())

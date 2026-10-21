"""Run each numbered example's offline tests and gate on the result.

TODOS.md CI-004. The examples are the documented feature set, and a documented
feature that does not run is a claim rather than a feature.

The v2 examples configure in-process scripted providers in their tests. Their
main programs intentionally require a real configured provider and are not run
by this gate. The gate is ratcheted:

  * an example that passes and then breaks fails the build;
  * an example that was failing and now passes fails the build too, with an
    instruction to ratchet, because a quiet improvement nobody records is one
    somebody re-breaks;
  * nothing else is tolerated silently.

Usage:
    python scripts/examples_gate.py            # check
    python scripts/examples_gate.py --update   # record the current result
"""

import io
import os
import re
import subprocess
import sys

EXPECTED_FILE = ".audit/examples_expected.txt"
EXAMPLES_DIR = "examples"
TIMEOUT_SECONDS = 180

# No credential. An example test that reaches a paid endpoint during a build is
# the failure B-04 exists to prevent.
ENVIRONMENT = {
    "SCHEMAFLUX_PROVIDER": "local",
    "SCHEMAFLUX_API_KEY": "",
    "SCHEMAFLUX_OPENAI_API_KEY": "",
    "OPENAI_API_KEY": "",
    "OPENAI": "",
    "SCHEMAFLUX_LIVE_TESTS": "",
}

NUMBERED = re.compile(r"^\d\d-")


def examples():
    if not os.path.isdir(EXAMPLES_DIR):
        return []
    names = [name for name in os.listdir(EXAMPLES_DIR) if NUMBERED.match(name)]
    return sorted(names)


def run_all(names):
    env = dict(os.environ)
    env.update(ENVIRONMENT)

    for attempt in range(2):
        try:
            result = subprocess.run(
                ["go", "test", "-p", "1", "-count=1"]
                + ["./" + EXAMPLES_DIR + "/" + name for name in names],
                capture_output=True, text=True, encoding="utf-8", errors="replace",
                timeout=TIMEOUT_SECONDS, env=env,
            )
        except subprocess.TimeoutExpired as expired:
            # A timeout carries whatever was written before the deadline, and it may
            # be None rather than an empty string.
            del expired
            return ({name: False for name in names},
                    {name: "timed out after %ds" % TIMEOUT_SECONDS for name in names})
        output = (result.stdout or "") + (result.stderr or "")
        if result.returncode == 0 or attempt or not (
            "go: unlinkat" in output and "Access is denied" in output
        ):
            break

    results = {name: False for name in names}
    reasons = {name: "no test result was reported" for name in names}
    package_result = re.compile(r"^(ok|FAIL)\s+\S+/examples/(\d\d-[^\s/]+)")
    for line in output.splitlines():
        match = package_result.match(line)
        if match and match.group(2) in results:
            name = match.group(2)
            results[name] = match.group(1) == "ok"
            reasons[name] = "" if results[name] else line.strip()[:300]
    if result.returncode != 0 and all(results.values()):
        diagnostic = next((line.strip() for line in reversed(output.splitlines()) if line.strip()), "go test failed")
        return ({name: False for name in names}, {name: diagnostic[:300] for name in names})
    return results, reasons


def read_expected():
    expected = {}
    try:
        with io.open(EXPECTED_FILE, encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if not line or line.startswith("#"):
                    continue
                status, _, name = line.partition(" ")
                expected[name.strip()] = status.strip()
    except OSError:
        return None
    return expected


def write_expected(results, reasons):
    os.makedirs(os.path.dirname(EXPECTED_FILE), exist_ok=True)
    passing = sum(1 for ok in results.values() if ok)

    with io.open(EXPECTED_FILE, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(
            "# Numbered examples under SCHEMAFLUX_PROVIDER=local.\n"
            "# %d of %d pass. See scripts/examples_gate.py and TODOS.md CI-004.\n"
            "# Update with: python scripts/examples_gate.py --update\n"
            "#\n"
            "# A FAIL line is a known gap, not permission to add another.\n\n"
            % (passing, len(results))
        )
        for name in sorted(results):
            if results[name]:
                handle.write("PASS %s\n" % name)
            else:
                handle.write("FAIL %s\n" % name)
                if reasons.get(name):
                    handle.write("#   %s\n" % reasons[name])


def main():
    names = examples()
    if not names:
        print("no numbered examples found")
        return 1

    results, reasons = run_all(names)
    for name in names:
        print("%s %s" % ("PASS" if results[name] else "FAIL", name))

    passing = sum(1 for ok in results.values() if ok)
    print("\n%d of %d examples pass" % (passing, len(results)))

    if "--update" in sys.argv:
        write_expected(results, reasons)
        print("recorded in %s" % EXPECTED_FILE)
        return 0

    expected = read_expected()
    if expected is None:
        write_expected(results, reasons)
        print("no expectation recorded; wrote the current result to %s" % EXPECTED_FILE)
        return 0

    broke, fixed, unknown = [], [], []
    for name, ok in sorted(results.items()):
        want = expected.get(name)
        if want is None:
            unknown.append(name)
        elif want == "PASS" and not ok:
            broke.append(name)
        elif want == "FAIL" and ok:
            fixed.append(name)

    if broke:
        print("\nThese examples used to pass and no longer do:")
        for name in broke:
            print("  %s: %s" % (name, reasons.get(name, "")))
    if fixed:
        print("\nThese examples now pass. Record it:")
        for name in fixed:
            print("  %s" % name)
        print("  python scripts/examples_gate.py --update")
    if unknown:
        print("\nThese examples are not in the expectation file:")
        for name in unknown:
            print("  %s" % name)
        print("  python scripts/examples_gate.py --update")

    return 1 if (broke or fixed or unknown) else 0


if __name__ == "__main__":
    sys.exit(main())

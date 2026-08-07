"""Fail if a credential is committed to this repository.

TODOS.md CI-006. The specific worry is not a developer pasting their key into
source -- it is `testdata/`. This repository ships a fixture .env because B-03
needed one, and a fixture env file is the most natural place in the world to
paste a real key while debugging and then commit it.

The scan is deliberately narrow. A pattern that fires on anything resembling a
token trains people to add exclusions, and an exclusion list is how a real key
eventually gets through. These patterns match credential shapes that are
vendor-specific enough to be worth stopping the build for.
"""

import io
import os
import re
import subprocess
import sys

# Vendor prefixes, keyed by what to call them in the failure message. The
# lengths are the shortest real ones observed, not the longest, so a truncated
# paste is still caught.
PATTERNS = [
    ("OpenAI key", re.compile(r"\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}")),
    ("Anthropic key", re.compile(r"\bsk-ant-[A-Za-z0-9_-]{20,}")),
    ("Google API key", re.compile(r"\bAIza[A-Za-z0-9_-]{30,}")),
    ("AWS access key ID", re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b")),
    ("GitHub token", re.compile(r"\bgh[pousr]_[A-Za-z0-9]{30,}")),
    ("Slack token", re.compile(r"\bxox[abprs]-[A-Za-z0-9-]{10,}")),
    ("private key block", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----")),
]

# Assignments that look like a live credential rather than a placeholder. The
# value has to be long and unquoted-ish; the placeholder check below rescues
# the obvious fakes.
#
# Twenty characters, not sixteen: a demo password in an example
# ("GoodPassword123!") is sixteen, and firing on it teaches the reader that this
# check cries wolf. Every credential the vendors below actually issue is longer.
ASSIGNMENT = re.compile(
    r"(?i)\b(api[_-]?key|secret|token|password|passwd|credential)\b\s*[:=]\s*[\"']?([^\s\"'#,}]{20,})"
)

# A value containing any of these is a placeholder, not a secret. Fixtures need
# to look like credentials to be useful, so this list is what keeps the scan
# from being permanently red.
PLACEHOLDER_MARKERS = (
    "example",
    "placeholder",
    "your-",
    "your_",
    "xxx",
    "dummy",
    "fake",
    "test-key",
    "test_key",
    "testkey",
    "sample",
    "replace",
    "changeme",
    "redacted",
    "notarealkey",
    "not-a-real",
    "not_a_real",
    "fixture",
    "from-the-",
    "demo",
    "abc123",
    "${",
    "os.getenv",
    "getenv(",
)

# Extensions worth reading. Binaries and images cannot carry a key in a form
# this scan would recognise anyway.
TEXT_EXTENSIONS = (
    ".go", ".md", ".py", ".sh", ".ps1", ".yml", ".yaml", ".json", ".env",
    ".txt", ".toml", ".mjs", ".js", ".html", ".sum", ".mod", "",
)

SKIP_DIRS = (".git/", ".artifacts/", "node_modules/", "dist/")

# This file names every pattern it looks for, so it matches itself.
SELF = os.path.normpath("scripts/secret_scan.py").replace(os.sep, "/")


def tracked_files():
    out = subprocess.run(
        ["git", "ls-files"], capture_output=True, text=True, check=True
    ).stdout
    for line in out.splitlines():
        path = line.strip()
        if not path or path == SELF:
            continue
        if any(path.startswith(skip) for skip in SKIP_DIRS):
            continue
        _, ext = os.path.splitext(path)
        if ext.lower() not in TEXT_EXTENSIONS:
            continue
        yield path


def looks_random(value):
    """Report whether the value looks generated rather than typed.

    This exists because the placeholder list is a substring check, and a real
    key contains substrings. The first version of this scan let
    `sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz123456789` through, because "1234" was on
    the placeholder list and appears inside it -- a scanner that a real key can
    silence by coincidence is worse than none, since it reports success.

    So: entropy wins over the placeholder list. A run of sixteen or more
    alphanumerics carrying upper case, lower case, and digits is what an issued
    credential looks like and what a hand-written fixture almost never is.
    """
    for run in re.findall(r"[A-Za-z0-9]{16,}", value):
        has_upper = any(character.isupper() for character in run)
        has_lower = any(character.islower() for character in run)
        has_digit = any(character.isdigit() for character in run)
        if has_upper and has_lower and has_digit:
            return True
    return False


def is_placeholder(value):
    if looks_random(value):
        return False
    lowered = value.lower()
    return any(marker in lowered for marker in PLACEHOLDER_MARKERS)


def scan(path):
    findings = []
    try:
        with io.open(path, encoding="utf-8", errors="replace") as handle:
            previous = ""
            for number, line in enumerate(handle, start=1):
                # The marker may sit on the line itself or on the line above it.
                # A waiver deserves a sentence explaining why, and a sentence
                # rarely fits after the code it is waiving.
                allowed = "secret-scan: allow" in line or "secret-scan: allow" in previous
                previous = line
                if allowed:
                    continue

                for label, pattern in PATTERNS:
                    match = pattern.search(line)
                    if match and not is_placeholder(match.group(0)):
                        findings.append((number, label, match.group(0)[:12] + "..."))

                match = ASSIGNMENT.search(line)
                if match and not is_placeholder(match.group(2)):
                    # A value that is entirely a Go/Python identifier or an
                    # environment variable name is code, not a credential.
                    value = match.group(2)
                    value = value.rstrip(";),")
                    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_.\[\]()<>-]*", value):
                        findings.append(
                            (number, "credential-shaped assignment", value[:12] + "...")
                        )
    except (OSError, UnicodeDecodeError):
        return []
    return findings


def line_is_flagged(line):
    """Run one line through the same rules scan() applies. Used by --self-test."""
    if "secret-scan: allow" in line:
        return False
    for _, pattern in PATTERNS:
        match = pattern.search(line)
        if match and not is_placeholder(match.group(0)):
            return True
    match = ASSIGNMENT.search(line)
    if match and not is_placeholder(match.group(2)):
        value = match.group(2).rstrip(";),")
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_.\[\]()<>-]*", value):
            return True
    return False


# A scanner nobody tests reports success for the wrong reason. The first
# version of this file passed a real OpenAI key because "1234" was on the
# placeholder list and appeared inside it; that case is the fourth row here.
SELF_TEST_CASES = [
    # (line, should_be_flagged, why)
    ("OPENAI_API_KEY=sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9", True, "issued OpenAI key"),
    ("key = 'sk-ant-api03-Xy7ZqW2mNb8Vc4Lk1Jh6Gf3Ds9Pa5Rt0'", True, "issued Anthropic key"),
    ("AWS_ACCESS_KEY_ID=AKIAIOSFODNN7ABCDEFG", True, "AWS access key ID"),
    ("token: ghp_16CharsAndThenSomeMoreRandom0987654321", True, "GitHub token, digits included"),
    ("xoxb-1234567890-abcdefghijklmnop", True, "Slack token"),
    ("-----BEGIN RSA PRIVATE KEY-----", True, "private key block"),
    ("password = 'Tr0ub4dor&3xKcd9RandomLong'", True, "long high-entropy assignment"),
    ("OPENAI=sk-test-fixture-not-a-real-key", False, "the shipped fixture"),
    ('t.Setenv("OPENAI", "sk-from-the-process-environment")', False, "test input"),
    ('apiKey := os.Getenv("OPENAI_API_KEY")', False, "reading a key is not holding one"),
    ("const apiKey = els.keyInput.value.trim();", False, "an expression, not a literal"),
    ('Password: "GoodPassword123!",', False, "a demo password in an example"),
    ("api_key: your-key-here", False, "explicit placeholder"),
    ("secret = 'sk-live-abcdef' // secret-scan: allow", False, "waived on the line"),
]


def self_test():
    failures = 0
    for line, should_flag, why in SELF_TEST_CASES:
        flagged = line_is_flagged(line)
        if flagged != should_flag:
            failures += 1
            print(
                "FAIL (%s): expected flagged=%s, got %s for: %s"
                % (why, should_flag, flagged, line[:60])
            )
    print("self-test: %d cases, %d failures" % (len(SELF_TEST_CASES), failures))
    return 1 if failures else 0


def main():
    if "--self-test" in sys.argv:
        return self_test()

    failures = []
    scanned = 0

    for path in tracked_files():
        scanned += 1
        for number, label, sample in scan(path):
            failures.append("%s:%d: %s (%s)" % (path, number, label, sample))

    print("secret scan: %d files" % scanned)

    if failures:
        print("\nPOSSIBLE CREDENTIALS COMMITTED:\n")
        for failure in failures:
            print("  " + failure)
        print(
            "\nIf one of these is a fixture, make it obviously fake -- the "
            "placeholder markers this scan accepts are listed in "
            "scripts/secret_scan.py -- or mark the line 'secret-scan: allow'."
        )
        return 1

    print("no credentials found")
    return 0


if __name__ == "__main__":
    sys.exit(main())

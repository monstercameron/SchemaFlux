#!/usr/bin/env bash
#
# Run every check CI runs, in one command.
#
# The problem this replaces: the required checks were enumerated in three places
# — AGENTS.md, CONTRIBUTING.md, and .github/workflows/ci.yml — and the three had
# drifted. A contributor could run everything either document asked for, pass,
# and still be surprised by CI, which teaches people to read a red build as
# noise. Both documents now point here, and this file is the list.
#
#   ./scripts/check.sh              # everything
#   ./scripts/check.sh --fast       # skip the slow ones (shuffle, examples)
#   ./scripts/check.sh --list       # print what would run, run nothing
#
# It never spends money. SCHEMAFLUX_LIVE_TESTS is cleared before anything runs,
# so a shell that has it exported cannot turn a check into a billed provider
# call. That is not paranoia: the credentials are in the maintainer's
# environment, and one accidental `go run ./examples/...` has already billed
# this project once.
#
# It fails closed. A check whose tool is missing is reported as SKIPPED and
# counted, never silently passed — see the note on govulncheck below.

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

# Nothing below may reach a provider.
unset SCHEMAFLUX_LIVE_TESTS
export SCHEMAFLUX_LIVE_TESTS=""

FAST=0
LIST=0
for arg in "$@"; do
  case "$arg" in
    --fast) FAST=1 ;;
    --list) LIST=1 ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $arg (try --help)" >&2; exit 2 ;;
  esac
done

# Checks are ordered fast-to-slow so a typo fails in seconds rather than after
# the full suite. Each entry is "label|command".
CHECKS=(
  "gofmt|test -z \"\$(gofmt -l .)\" || { gofmt -l .; false; }"
  "build|go build ./..."
  "build examples|go build ./examples/..."
  "vet|go vet ./..."
  "secret scan|python scripts/secret_scan.py"
  "secret scan self-test|python scripts/secret_scan.py --self-test"
  "deprecation policy|python scripts/deprecation_policy.py --check"
  "acceptance ledger|python scripts/acceptance_checklist.py --check"
  "release ladder|python scripts/release_gate.py --check"
  "traceability|python .audit/traceability.py"
  "api surface|go test ./tests/ -run TestPublicAPISurface -count=1"
  "tests|go test ./... -count=1"
  "smarttodo module|cd examples/smarttodo && go test ./..."
  "coverage floor|python scripts/coverage_floor.py"
)

SLOW=(
  "tests shuffled|go test ./... -count=3 -shuffle=on"
  "examples gate|python scripts/examples_gate.py"
)

if [ "$FAST" -eq 0 ]; then
  CHECKS+=("${SLOW[@]}")
fi

if [ "$LIST" -eq 1 ]; then
  printf '%s\n' "${CHECKS[@]}" | sed 's/|.*//' | nl
  exit 0
fi

# -race is the one check this cannot run everywhere, and saying so is the point:
# a gate that silently does not run is worse than one that is absent, because
# the build stays green while the guarantee is gone.
PLATFORM="$(go env GOOS)/$(go env GOARCH)"
if [ "$PLATFORM" = "windows/arm64" ]; then
  RACE_NOTE="SKIPPED on $PLATFORM (the race detector does not run here; CI covers it)"
else
  CHECKS+=("race|go test -race ./... -count=1")
  RACE_NOTE=""
fi

PASSED=(); FAILED=(); SKIPPED=()

for entry in "${CHECKS[@]}"; do
  label="${entry%%|*}"
  command="${entry#*|}"

  printf '\n\033[1;34m▸ %s\033[0m\n' "$label"

  # A check whose tool is absent is SKIPPED, not passed. govulncheck is the
  # usual case: it is a separate install, and reporting "no vulnerabilities"
  # because the scanner is missing is the exact shape of lie these gates exist
  # to prevent.
  tool="$(printf '%s' "$command" | awk '{print $1}')"
  # `test` and `cd` are shell builtins, not programs on PATH; probing them with
  # `command -v` would report them missing and skip a check that works fine.
  if [ "$tool" != "test" ] && [ "$tool" != "cd" ] && ! command -v "$tool" >/dev/null 2>&1; then
    printf '\033[1;33m  SKIPPED: %s is not installed\033[0m\n' "$tool"
    SKIPPED+=("$label ($tool not installed)")
    continue
  fi

  # Each check runs in a subshell, so a `cd` inside one cannot leak into the
  # next -- which is what lets the smarttodo entry be a plain `cd && go test`.
  if ( eval "$command" ); then
    printf '\033[0;32m  ok\033[0m\n'
    PASSED+=("$label")
  else
    printf '\033[0;31m  FAILED\033[0m\n'
    FAILED+=("$label")
  fi
done

# govulncheck last, because it is advisory here: the findings today are
# standard-library advisories that need a toolchain upgrade rather than a code
# change, so it reports and does not fail the run.
printf '\n\033[1;34m▸ govulncheck (advisory)\033[0m\n'
if command -v govulncheck >/dev/null 2>&1; then
  if govulncheck ./... >/dev/null 2>&1; then
    printf '\033[0;32m  ok\033[0m\n'
  else
    printf '\033[1;33m  findings present; run `govulncheck ./...` to read them\033[0m\n'
  fi
else
  printf '\033[1;33m  SKIPPED: govulncheck is not installed (go install golang.org/x/vuln/cmd/govulncheck@latest)\033[0m\n'
fi

printf '\n\033[1;34m════ summary ════\033[0m\n'
printf '  passed:  %d\n' "${#PASSED[@]}"
printf '  failed:  %d\n' "${#FAILED[@]}"
printf '  skipped: %d\n' "${#SKIPPED[@]}"
[ -n "$RACE_NOTE" ] && printf '  race:    %s\n' "$RACE_NOTE"

if [ "${#SKIPPED[@]}" -gt 0 ]; then
  printf '\n\033[1;33mskipped, so unverified:\033[0m\n'
  printf '  %s\n' "${SKIPPED[@]}"
fi

if [ "${#FAILED[@]}" -gt 0 ]; then
  printf '\n\033[0;31mfailed:\033[0m\n'
  printf '  %s\n' "${FAILED[@]}"
  exit 1
fi

if [ "$FAST" -eq 1 ]; then
  printf '\n\033[1;33m--fast skipped the shuffled run and the examples gate; CI runs both.\033[0m\n'
fi

printf '\n\033[0;32mall checks passed\033[0m\n'

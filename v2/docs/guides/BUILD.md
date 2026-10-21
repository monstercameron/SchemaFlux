# Build Instructions

This document covers building and testing SchemaFlux and its examples.
Run the commands below from the repository's `v2/` directory.

## Prerequisites

- Go 1.27 or later (see `go.mod`)
- Python 3 for the repository gates; Node.js 22 and Playwright for the SmartTodo browser smoke test

There is no protobuf, no workflow engine, and no code generation step in this
repository. If a future change adds one, this file needs a new section, not a
guess based on what other Go libraries usually have.

## Build the Library

```bash
go build ./...
```

## Build the Examples

The numbered examples under `examples/` (`01-extract`, `02-transform`, ...
`45-pivot`) share the root module and build with the rest of the tree:

```bash
go build ./examples/...
```

Do **not** run an example with `go run` unless you intend to make a real,
billed call to whatever provider it is configured for — each one calls a live
LLM API. `python scripts/examples_gate.py` runs them against a scripted
provider instead, with no network calls and no cost; use that to check an
example still works.

### smarttodo

`examples/smarttodo` is its own Go module (it has its own `go.mod`) and is not
covered by `go build ./examples/...`. Build it directly:

```bash
cd examples/smarttodo
go build ./...
```

It also has a WebAssembly build, packaged by a script rather than a plain
`go build`:

```bash
./scripts/build-smarttodo-demo.sh [output-dir]   # bash
./scripts/build-smarttodo-demo.ps1 [output-dir]  # PowerShell
```

This compiles `examples/smarttodo/cmd/smarttodo-wasm` with `GOOS=js
GOARCH=wasm`, copies the web assets from `examples/smarttodo/web`, and copies
`wasm_exec.js` out of the Go toolchain into the output directory (default
`dist/smarttodo-demo`). A push to `main` runs the SmartTodo browser smoke test
and deploys the bundle to [GitHub Pages](https://monstercameron.github.io/SchemaFlux/).

## Running Tests

```bash
# Run root-module tests
go test ./...

# Run the separate SmartTodo module tests
(cd examples/smarttodo && go test ./...)

# Run tests with coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Run a specific package's tests
go test ./internal/ops/...
```

Provider-backed tests compile unconditionally and only run against a live
provider when `SCHEMAFLUX_LIVE_TESTS=1` is set. Leaving it unset (the default)
makes zero network calls.

Run `./scripts/check.sh` for the complete local release gate. It includes the
root-module tests, a shuffled pass, static analysis, the example gate, API and
coverage checks, and repository policy checks. The script clears
`SCHEMAFLUX_LIVE_TESTS` so this gate never incurs provider charges. The
SmartTodo module has its own tests, and CI also runs its Playwright browser
smoke test.

## Clean Build Artifacts

```bash
rm -f coverage.out
rm -rf dist/
```

## Continuous Integration

`.github/workflows/ci.yml` is the required gate. It runs the root suite on
Windows, Ubuntu, Ubuntu ARM, and macOS, including ten shuffled passes per
platform and race detection where supported. The separate
`.github/workflows/smarttodo-demo.yml` builds, browser-tests, and deploys the
SmartTodo bundle. See those workflows and `./scripts/check.sh --list` for the
current commands.

## Troubleshooting

### An example fails when run directly

Examples call a live LLM provider and need real credentials in the
environment. Use `python scripts/examples_gate.py` to verify examples without
spending money or needing credentials at all.

### `go build ./examples/...` doesn't pick up smarttodo

That's expected — `examples/smarttodo` has its own `go.mod` and its own module
boundary. Build it from inside that directory.

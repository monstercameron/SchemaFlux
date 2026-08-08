# Build Instructions

This document covers building and testing SchemaFlux and its examples.

## Prerequisites

- Go 1.25 or later (see `go.mod`)

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
`dist/smarttodo-demo`).

## Running Tests

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Run a specific package's tests
go test ./internal/ops/...
```

Provider-backed tests compile unconditionally and only run against a live
provider when `SCHEMAFLUX_LIVE_TESTS=1` is set. Leaving it unset (the default)
makes zero network calls.

## Clean Build Artifacts

```bash
rm -f coverage.out
rm -rf dist/
```

## Continuous Integration

`.github/workflows/ci.yml` is the source of truth for what CI actually runs.
As of this writing that is: `go build ./...`, `go test ./...`, `go test -race
./...` (non-Windows runners), `go test -shuffle=on -count=10 ./...`, `go vet
./...`, `gofmt -l .`, `staticcheck ./...`, `govulncheck ./...`, `go build
./examples/...`, and `python3 scripts/examples_gate.py`. Check the workflow
file directly before relying on this list — it is easier for this paragraph to
drift out of date than for the workflow to change silently.

## Troubleshooting

### An example fails when run directly

Examples call a live LLM provider and need real credentials in the
environment. Use `python scripts/examples_gate.py` to verify examples without
spending money or needing credentials at all.

### `go build ./examples/...` doesn't pick up smarttodo

That's expected — `examples/smarttodo` has its own `go.mod` and its own module
boundary. Build it from inside that directory.

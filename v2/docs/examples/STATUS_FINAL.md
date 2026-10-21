# SchemaFlux v2 example status

All 45 numbered examples under [`examples/`](../../examples) are ported to the v2 root module and pass the scripted-provider example gate. Each can be built with the root module; running one directly can call a paid provider.

The earlier 14/15 status belonged to the v1 example plan. `11-similar` now uses the implemented v2 `Similar` operation. The examples after `15-guard` cover more text, collection, decision, validation, and experimental operations through `45-pivot`.

| Example set | Build and test path |
| --- | --- |
| Numbered examples | `go build ./examples/...` and `python scripts/examples_gate.py` from the `v2/` module directory |
| Flowdesk benchmark | `go test ./examples/flowdesk/...`; live runs require the guarded provider setting and credentials |
| SmartTodo app | `go test ./...` from `examples/smarttodo`, a separate Go module |
| SmartTodo browser demo | `bash scripts/build-smarttodo-demo.sh` and the Playwright smoke test in `../.github/workflows/smarttodo-demo.yml` |

The SmartTodo demo is published at [GitHub Pages](https://monstercameron.github.io/SchemaFlux/) after a successful `main` workflow. See the [build guide](../guides/BUILD.md) and the [v2 API reference](../reference/API.md) for setup.

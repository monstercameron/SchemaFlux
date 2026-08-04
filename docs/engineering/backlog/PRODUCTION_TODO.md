# Production TODO

Last updated: 2026-03-06

## Verification snapshot

Completed in this review:
- `go test ./...` in the main module: pass
- `go vet ./...` in the main module: pass
- `go run ./examples/tools all`: pass
- `go build ./...` in `examples/smarttodo`: pass
- Numbered example smoke run with `SCHEMAFLUX_PROVIDER=local`: 26 passed, 19 failed
- Live OpenAI smoke run after provider fixes: `Extract`, `Validate`, and `Rank` passed
- Full live OpenAI smoke run via `cmd/live-llm-ops-smoke/main.go`: 65 / 65 exported LLM ops passed
- Three-model live OpenAI matrix after remapping intelligence:
  - `Smart` / `gpt-5.4`: 65 / 65 passed
  - `Fast` / `gpt-5-mini`: 65 / 65 passed
  - `Quick` / `gpt-5-nano`: 65 / 65 passed

Numbered examples that passed under the local provider:
- `02-transform`, `07-summarize`, `08-classify`, `09-score`, `10-compare`, `11-similar`, `12-validate`, `13-merge`, `14-decide`, `15-guard`, `16-infer`, `17-diff`, `18-explain`, `19-parse`, `20-complete`, `21-redact`, `22-suggest`, `36-negotiate`, `37-resolve`, `38-derive`, `39-conform`, `41-arbitrate`, `42-project`, `43-audit`, `44-compose`, `45-pivot`

Numbered examples that failed under the local provider:
- `01-extract`, `03-generate`, `04-choose`, `05-filter`, `06-sort`, `23-annotate`, `24-cluster`, `25-rank`, `26-compress`, `27-decompose`, `28-enrich`, `29-normalize`, `30-match`, `31-critique`, `32-synthesize`, `33-predict`, `34-verify`, `35-question`, `40-interpolate`

## Release blockers

- [ ] The local/mock provider is not a credible integration substitute for the typed API surface. Its default branch returns plain text such as `Mock response for: ...`, which is incompatible with structured operations like `Rank`, `Enrich`, `Predict`, `Verify`, and `Question`: `internal/llm/provider.go:685`
- [ ] Example programs are not a reliable release gate today. 19 of 45 numbered examples failed under the local provider because the mock layer does not satisfy their JSON contracts. These failures block a claim that the documented feature set is continuously runnable.
- [ ] Metrics export integrations are still minimal. The library now maintains in-process aggregates and supports pluggable sinks, but it still lacks first-class adapters and docs for Prometheus, OTLP metrics, or vendor backends: `telemetry/metrics.go`

## Not production-ready features

The following tool families are explicitly stubbed and should not be marketed as production-ready until they have real integrations, tests, configuration docs, and failure semantics:
- [ ] Search/browser tooling: `internal/tools/http.go:193`, `internal/tools/http.go:213`, `internal/tools/http.go:235`
- [ ] Geo/weather tooling: `internal/tools/time.go:500`, `internal/tools/time.go:519`
- [ ] Vector database tooling: `internal/tools/database.go:402`
- [ ] File watching: `internal/tools/file.go:515`
- [ ] JWT/encryption helpers: `internal/tools/cache_security.go:405`, `internal/tools/cache_security.go:453`, `internal/tools/cache_security.go:472`
- [ ] Additional stub families documented in the tools example README: archive, image processing, audio, messaging, and several AI helpers in `examples/tools/README.md`

## Next work to reach a real release bar

- [ ] Add a first-class way for each operation to declare whether it requires JSON output, and enforce that through `CompletionRequest`
- [ ] Upgrade the local provider so every documented operation has a schema-compatible mock response; then convert the numbered example smoke run into CI
- [ ] Replace the remaining duplicated but non-fatal `.env` loaders with the shared example bootstrap helper
- [ ] Add documented metric sink adapters for at least one standard backend so users do not have to wire exports from scratch
- [ ] Split tool capabilities into `implemented` vs `stubbed` documentation so users do not confuse placeholders with working features
- [ ] Add Linux-based `-race` CI coverage, because the current machine cannot run `go test -race ./...` on Windows/arm64


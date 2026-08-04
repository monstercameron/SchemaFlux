# SchemaFlux Refactoring Plan

## Goal
Transform `SchemaFlux` into a standard, idiomatic, and "go gettable" library with a pristine Developer Experience (DX). The primary goal is to allow users to import a single package (`github.com/monstercameron/schemaflux`) to access all functionality, while hiding implementation details in `internal/`.

## Current State Analysis
- **Root**: Contains `schemaflux.go` (facade), `go.mod`.
- **Core (`/core`)**: Public package containing low-level types, config, and provider logic.
- **Ops (`/ops`)**: Public package containing operation implementations.
- **Issues**:
    - Split between `core` and `ops` exposes internal logic.
    - Users might be confused whether to import `core` or `schemaflux`.
    - Potential for circular dependencies if not careful.
    - "Facade" pattern in `schemaflux.go` is good but implementation details are currently exposed in public subpackages.

## Proposed Architecture

### 1. Root Package (`github.com/monstercameron/schemaflux`)
The single entry point for the library.
- **`client.go`**: Defines `Client` struct and factory methods (`NewClient`, `Init`).
- **`types.go`**: Defines public types (`Mode`, `Speed`, `OpOptions`) via type aliases to internal types where possible, or definitions.
- **`api.go`**: Top-level functions (`Extract`, `Transform`, `Generate`) that wrap internal implementations.
- **`batch.go`**: Public `BatchProcessor` API.
- **`provider.go`**: Defines the `Provider` interface for users who want to implement custom providers.
- **`errors.go`**: Public error types.

### 2. Internal Package (`internal/`)
Hidden implementation details.
- **`internal/types`**: Shared types (`Mode`, `Speed`) to avoid circular dependencies.
- **`internal/llm`**: Low-level LLM communication, request/response structs, and standard provider implementations (OpenAI, Anthropic).
- **`internal/ops`**: Implementation of operations (`Extract`, `Transform`). These functions will accept interfaces (like `llm.Caller`) instead of the concrete `Client`.
- **`internal/schema`**: JSON schema generation and validation logic.
- **`internal/telemetry`**: Tracing and metrics utilities.

## Step-by-Step Execution Plan

### Phase 1: Foundation & Shared Types
1.  Create `internal/types`.
2.  Move `Mode`, `Speed`, and basic constants from `core/types.go` to `internal/types`.
3.  Create `types.go` in root and type-alias `Mode` and `Speed` to `internal/types` (e.g., `type Mode = types.Mode`).

### Phase 2: Internalize Logic
1.  **Move Core**: Move `core/llm.go`, `core/provider.go`, `core/json.go` to `internal/llm` and `internal/schema`.
2.  **Move Ops**: Move `ops/*.go` to `internal/ops`.
3.  **Refactor Ops**: Update `internal/ops` functions to:
    - Accept `context.Context` and a generic `Caller` interface (defined in `internal/llm`) instead of `*Client`.
    - Use `internal/types` for options.

### Phase 3: Construct Root API
1.  **Client**: Create `client.go` in root. The `Client` struct will hold the configuration and the `internal/llm.Provider`.
2.  **API Functions**: In `schemaflux.go` (or `api.go`), implement `Extract`, `Transform`, etc.
    - These functions will convert root `OpOptions` to `internal` options.
    - They will call `internal/ops` functions, passing the `Client`'s internal provider.

### Phase 4: Providers
1.  Move specific provider implementations (`OpenAIProvider`, `AnthropicProvider`) to `internal/providers`.
2.  Expose them via configuration options in root (e.g., `WithProvider("openai")` or `WithOpenAI(apiKey)`).

### Phase 5: Cleanup
1.  Remove `core/` and `ops/` directories from root.
2.  Update `go.mod` and run `go mod tidy`.
3.  Update `examples/` to use the new single-import structure.

## Benefits
- **Clean Import**: `import "github.com/monstercameron/schemaflux"` is all that's needed.
- **Encapsulation**: Implementation details can change without breaking the public API.
- **Maintainability**: Clear separation between public surface and internal logic.
- **Safety**: Prevents users from accidentally depending on internal helper functions.

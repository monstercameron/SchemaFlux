# Issue Tracker- *- **Refactor]** The `interfaceSlice` function is a helper function that is used in this file. It could be moved to `core_utils.go`.

## ops_core.go

- **[Design]** The `ClientExtract` and `ClientTransform` functions are not methods on the `Client` struct. They are standalone functions that take a `Client` as an argument. This is not idiomatic Go.
- **[Refactor]** The `extractImpl` and `transformImpl` functions have a lot of duplicated code for handling options and logging. This could be refactored.
- **[Robustness]** The `Extract` and `Transform` functions temporarily set global variables to the client's values. This is a very bad practice and can lead to race conditions and other issues.
- **[Robustness]** The `Generate` function has a hardcoded `maxPromptLength`. This should be configurable.
- **[Refactor]** The `generateTypeSchema` and `getTypeDescription` functions are useful, but they could be more robust. For example, they don't handle all possible types.
- **Robustness]** The `validateExtractedData` function's check for zero values is not very robust. It will not work correctly for all types.

## ops_extended_test.go

- **[Test]** The tests in this file are good, but they rely on a mock client. It would be better to have more varied mock responses to test different scenarios.
- **[Test]** The tests don't cover failure cases, such as when the LLM returns an invalid response.
- **[Clarity]** The tests could be more descriptive. For example, `TestValidate` could be split into multiple tests for different scenarios.
- **[Test]** The tests for `Merge`, `Question`, and `Deduplicate` are good, but they could be more comprehensive.
- **[Test]** The tests for the client-based versions of the functions (`ClientValidate`, `ClientFormat`) are very basic and only check that the functions run without error.

obust- **Clarity]** The tests could be more descriptive. For example, `TestBatchOperations` could be split into multiple tests for different scenarios.

## ops_batch.go

- **[Design]** The `ExtractBatch` function is not a method on `BatchProcessor`, which is not idiomatic Go. It should be a method.
- **[Refactor]** The `extractParallel` and `extractMerged` functions have a lot of duplicated code for handling results and metadata. This could be refactored.
- **[Robustness]** Line 250: The `extractMerged` function has a hardcoded cost estimation. This should be more accurate and configurable.
- **[Robustness]** Line 340: The `parseMergedResponse` function's logic for handling missing indices is not very robust. It relies on `reflect.DeepEqual` to check for zero values, which might not be correct for all types.
- **[Design]** Line 400: The `determineBestMode` function in `SmartBatch` uses a simple heuristic to decide between `ParallelMode` and `MergedMode`. This could be more sophisticated.
- **Refactor]** The `areInputsSimilar` function is very basic and only checks the type of the inputs. It should be more sophisticated.

## ops_collection.go

- **[Refactor]** The `Choose`, `Filter`, and `Sort` functions have a lot of duplicated code for building prompts and handling options. This could be refactored into helper functions.
- **[Robustness]** The functions in this file rely on the LLM returning a specific format (e.g., just an index or a JSON array of indices). This is brittle and can easily break if the LLM's response format changes.
- **[Design]** The `Choose` function returns a single item, but it has an option to return the top N items. It should return a slice of items in that case.
- **[Robustness]** Line 130: The `Choose` function parses the response as an integer index. This is very brittle.
- **[Robustness]** The `Filter` and `Sort` functions return an error if the number of indices returned by the LLM does not match the number of items. This is good, but it would be better to have more robust error handling.
- **[Refactor]** The `interfaceSlice` function is a helper function that is used in this file. It could be moved to `core_utils.go`.

ss]** Line 300: `getCaller`'s logic for extracting the filename is not robust and will not work correctly on Windows. It should use `filepath.Base`.

## Makefile

- **[Build]** The `Makefile` is written for a Unix-like environment and will not work on Windows without a compatibility layer like `make` for Windows.
- **[Build]** The `lint` target has a check for `golangci-lint` and provides installation instructions, but the instructions are for a Unix-like environment.
- **[Build]** The `test-run` target uses `read -p`, which is a bash-specific command.
- **[Build]** The `info` target uses `wc`, `tr`, `awk`, and `tail`, which are not available on Windows by default.
- **[Refactor]** The `Makefile` is very comprehensive, but it could be simplified. For example, the `test-coverage` and `test-coverage-html` targets could be combined.

## ops_analysis.go

- **[Refactor]** The `Classify`, `Score`, `Compare`, and `Similar` functions have a lot of duplicated code for building prompts and handling options. This could be refactored into helper functions.
- **[Robustness]** The functions in this file rely on the LLM returning a specific format (e.g., just a category name or a number). This is brittle and can easily break if the LLM's response format changes.
- **[Design]** The `Classify` function returns a single string, but it has an option for multi-label classification. It should return a slice of strings in that case.
- **[Robustness]** Line 108: The `Classify` function checks if the returned category is in the list of categories, but it does a case-insensitive comparison. This could lead to unexpected behavior if the categories are case-sensitive.
- **[Robustness]** Line 218: The `Score` function normalizes the score to the given scale, but it doesn't handle the case where the LLM returns a score outside the scale.
- **Refactor]** The `Compare` function's input handling logic is duplicated. It should be extracted into a helper function.

## ops_batch_test.go

- **[Test]** The tests in this file are good, but they rely on a mock client. It would be better to have more varied mock responses to test different scenarios.
- **[Test]** Line 80: The `TestBatchOperations` test for `MergedMode` has a check for the number of API calls, but it's a warning, not a failure. This should be a hard failure.
- **[Test]** Line 100: The `TestBatchOperations` test for `SmartBatch` has hardcoded thresholds for when to switch between `ParallelMode` and `MergedMode`. These should be configurable and tested.
- **[Test]** The tests don't cover failure cases, such as when an item in a batch fails to process.
- **[Clarity]** The tests could be more descriptive. For example, `TestBatchOperations` could be split into multiple tests for different scenarios.

 - **Robustne- **Clarity]** The tests are not very descriptive. For example, `TestExtract` could be split into multiple tests for different scenarios.

## experimental/schemaflux.go

- **[Doc]** The file is named `experimental/schemaflux.go`, which now matches the package name but still duplicates higher-level documentation. Consider consolidating with `core/doc.go` to avoid drift.
- **[Doc]** The file list in the documentation comment is out of date. For example, it lists `data_operations.go`, but the file is `ops_core.go`.
- **[Doc]** The documentation is getting out of sync with the code. It's important to keep it updated.
- **Refactor]** The file is just a documentation file. It could be merged with `doc.go`.

## logger.go

- **[Refactor]** The logger is a custom implementation. It would be better to use a standard logging library like `slog` (which is in the standard library since Go 1.21) or a popular third-party library like `zerolog` or `zap`.
- **[Robustness]** Line 170: The `log` function's parsing of variadic fields is not very robust. It assumes that the fields are always key-value pairs.
- **[Refactor]** Line 120: The `WithFields` method creates a new logger with a copy of the fields. This is inefficient and could be optimized.
- **[Clarity]** The logger is used as a global variable in other files, which is not ideal. It should be passed as a dependency.
- **[Robustness]** Line 300: `getCaller`'s logic for extracting the filename is not robust and will not work correctly on Windows. It should use `filepath.Base`.

]** Line 345: `formatForLog` has hardcoded length limits. These should be configurable.

## doc.go

- **[Doc]** The documentation is good, but it's a bit long and could be better organized. A table of contents would be helpful.
- **[Doc]** The file names mentioned in the "Operation Categories" section are not consistent with the actual file names in the project. For example, it lists `data_operations.go`, but the file is `ops_core.go`.
- **[Doc]** The documentation mentions `ClientExtract`, but the example uses `schemaflux.ClientExtract`, which is not the idiomatic way to call a method on a client.
- **[Doc]** The "Procedural Operations" section mentions `procedural_ops.go`, but the file is `ops_procedural.go`.
- **Doc]** The documentation is getting out of sync with the code. It's important to keep it updated.

## go.mod

- **[Deps]** 2025-10-07: Go directive relaxed to the minor release (`go 1.24.0`; Go tooling rewrites the `.0` suffix automatically) while retaining the `toolchain` pin. ✅
- **[Deps]** 2025-10-07: Module path casing corrected to `github.com/monstercameron/schemaflux` to match the canonical repository location. ✅
- **[Deps]** 2025-10-07: Removed unused `github.com/gin-gonic/gin` and other stale requirements via `go mod tidy`. ✅
- **[Deps]** The OpenTelemetry stack is still locked to specific exporters; revisit once we introduce a pluggable telemetry layer.
- **[Deps]** Indirect dependencies now reflect tidy output. Monitor after the package split to ensure nothing drifts back in.

## go.sum

- **[Deps]** 2025-10-07: Regenerated with `go mod tidy`; duplicate testify/yaml entries cleared. ✅
- **[Deps]** Remaining entries are required by OpenTelemetry and gRPC—revisit after modularizing telemetry to trim further.

## experimental/schemaflux_test.go

- **[Test]** The renamed `experimental/schemaflux_test.go` remains extremely broad; long-term we should split it into focused suites.
- **[Test]** Line 20: The `TestMain` function sets up a mock client if `SCHEMAFLUX_API_KEY` is not set. This is good for CI, but it makes it hard to run tests against a real API.
- **[Test]** Line 33: The `mockLLMResponse` function is a giant `if/else if` block that is hard to maintain. It would be better to use a map of responses or a more structured approach.
- **[Refactor]** The file is very long and contains tests for many different parts of the library. It should be split into multiple files.
- **[Test]** The tests are very basic and don't cover many edge cases. For example, the `TestExtract` test only checks for a successful extraction and doesn't test any failure cases.
- **[Clarity]** The tests are not very descriptive. For example, `TestExtract` could be split into multiple tests for different scenarios.

Clarity]** The file contains a mix of unit tests, integration tests, and edge case tests. It would be clearer if these were separated.

## debug_test.go

- **[Refactor]** This test file is for the `debug.go` file, but it also tests functions from other files (e.g., `ValidateInput`). It should only test the `debug.go` file.
- **[Test]** The tests are quite basic and don't cover many edge cases. For example, `TestDebug` only checks if the global `debugMode` variable is set.
- **[Clarity]** The tests are not very descriptive. For example, `TestDebug` could be split into `TestEnableDebug` and `TestDisableDebug` for clarity.
- **[Test]** Line 110: The `TestValidateInput` test for a very long string is good, but it's testing a validation rule, not a debugging feature.
- **Test]** Line 250: The `TestFormatForLog` test has a `maxLen` that seems arbitrary. The test should be more specific about what it's testing.

## debug.go

- **[Design]** The file contains a mix of debugging utilities, tracing, input validation, and benchmarking. These are different concerns and should be in separate files.
- **[Robustness]** Line 229: The `sanitizeString` function's check for dangerous patterns is very basic and can be easily bypassed. It should not be relied on for security.
- **[Refactor]** Line 161: The `ValidateInput` function has a hardcoded `maxSliceSize`. This should be configurable.
- **[Refactor]** Line 222: The `sanitizeString` function has a hardcoded `maxStringLength`. This should be configurable.
- **[Clarity]** The `OperationTrace` and `OperationDump` structs are very similar. They could be consolidated.
- **[Robustness]** Line 315: `getStackTrace` has a hardcoded limit of 20 stack frames. This might not be enough for deep call stacks.
- **[Robustness]** Line 345: `formatForLog` has hardcoded length limits. These should be configurable.

T- **Clari- **Doc]** The documentation for the various types is good, but it would be helpful to have examples of how they are used in practice.

## core_utils.go

- **[Robustness]** Line 12: `generateRequestID` uses `time.Now().UnixNano()`, which is not guaranteed to be monotonic. If the system clock is adjusted backwards, it could lead to duplicate IDs.
- **[Refactor]** Line 26: The `recordMetric` function is a placeholder. It should be implemented or removed.
- **Clarity]** The file is very small. The functions could be moved to other files where they are used to reduce the number of files in the project.

## coverage_test.go

- **[Test]** Line 440: The `TestLLMRetryLogic` test is skipped. This is a critical piece of functionality that should be tested.
- **[Refactor]** The test file is very long and covers many different parts of the library. It would be better to split it into multiple files, each focused on a specific component (e.g., `core_config_test.go`, `ops_collection_test.go`).
- **[Test]** Line 21: The `TestInterfaceSlice` function is testing a generic function with specific types. This is not a very effective way to test generics.
- **[Test]** Line 610: The `TestComplexOperationChains` test is good, but it relies on a mock LLM response. It would be better to have more varied mock responses to test different scenarios.
- **[Test]** Line 750: The `TestValidateExtractedDataEdgeCases` test for `Person{}` expecting an error because of required fields is based on an assumption about the struct's definition. The test should be self-contained or the struct definition should be included.
- **[Clarity]** The file contains a mix of unit tests, integration tests, and edge case tests. It would be clearer if these were separated.

]** The error messages are formatted as strings, which is good for logging, but it would be better if the fields were also easily accessible for programmatic use.

## core_llm.go

- **[Design]** The file has two main functions, `defaultCallLLM` and `providerCallLLM`, which have very similar logic (retry, logging, etc.). This is a lot of duplicated code that could be refactored into a single function that takes a `Provider` interface.
- **[Refactor]** The `defaultCallLLM` function is a mix of the old global client logic and the new provider-based logic. This makes the code hard to follow and maintain. The fallback to the legacy client should be removed in favor of the provider-based approach.
- **[Robustness]** The special handling for "gpt-5" models in `defaultCallLLM` is brittle and based on string matching. This should be handled by the provider implementation, not in the core logic.
- **[Robustness]** The `parseJSON` function tries to handle markdown code blocks, but it's not very robust. It would be better to have the LLM return clean JSON.
- **[Refactor]** The retry logic is duplicated in both `defaultCallLLM` and `providerCallLLM`. This should be extracted into a higher-order function or a utility.
- **[Config]** The configuration values like `maxRetries` and `retryBackoff` are still being read from global variables. They should be part of the `Client` configuration.
- **[Clarity]** The `isRetryableError` function uses string matching on error messages, which is fragile. It would be better to use typed errors or error codes.

## core_provider.go

- **[Refactor]** Line 221: The `AnthropicProvider` is a mock implementation. It should be a real implementation or removed.
- **[Robustness]** Lines 193, 275: The `EstimateCost` functions in both `OpenAIProvider` and `AnthropicProvider` use rough estimations and hardcoded prices. This should be more accurate and configurable.
- **[Design]** Line 15: The `Provider` interface includes `EstimateCost`, which might not be applicable to all providers (e.g., local models). This could be an optional interface.
- **[Refactor]** Line 340: The `LocalProvider`'s `mockResponse` function has hardcoded logic for different prompt types. This is not very flexible and should be improved.
- **[Design]** Line 430: The global provider registry (`globalRegistry`) introduces global state, which can be problematic for testing and concurrent use. It would be better to manage providers within a `Client` or a dedicated manager.
- **Robustness]** The `NewOpenAIProvider` and `NewAnthropicProvider` functions return an error, but the calling code in `core_config.go`'s `WithProvider` just logs a warning and continues, which could lead to a nil provider.

## core_types.go

- **[Clarity]** Line 110: The comment for `OpOptions` says that `context` and `requestID` are not part of the public API, but they are exported fields. They should be unexported to enforce this.
- **[Design]** Line 116: The `Result` struct contains an `Error` field. This is not idiomatic Go. It's better to return `(T, error)`.
- **[Refactor]** The `TokenUsage`, `CostInfo`, and `ResultMetadata` structs are detailed and useful, but they don't seem to be used consistently across the library. They should be integrated into the `Result` struct or returned from operations.
- **[Clarity]** The constants for `Mode` and `Speed` are well-defined, but their usage is not always clear. For example, it's not obvious how `Strict` mode affects a `Summarize` operation.
- **[Doc]** The documentation for the various types is good, but it would be helpful to have examples of how they are used in practice.

s document tra- **Logging]** Line 205: `SetDebugMode` logs a message when debug mode is disabled. It's probably better to not log anything in this case.

## core_config.go

- **[Design]** The file heavily relies on global variables and a global `Init` function, which makes the code difficult to test and prevents using multiple clients with different configurations. The `Client` struct is a better approach and the global state should be deprecated.
- **[Refactor]** The `Init` function and the `NewClient` function provide two different ways of initialization, which is confusing. The `Init` function should be a simple wrapper around `NewClient` or be removed.
- **[Robustness]** Line 118: The `WithProvider` function logs a warning on failure but doesn't return an error, which could leave the client in an inconsistent state.
- **[Refactor]** The configuration loading from environment variables in `Init` should be moved to `NewClient` or a dedicated configuration object.
- **[Clarity]** Line 316: The comment in `applyDefaults` about not being able to distinguish between `0` and unset values highlights a potential issue. Using pointers for optional settings would be a more robust solution.
- **[Config]** Line 328: The `getModel` function has hardcoded model names. These should be configurable.
- **Refactor]** There is a lot of redundancy between the `Client` struct fields and the global configuration variables. The global variables should be removed.

## core_errors.go

- **[Refactor]** Many of the error structs are very similar (e.g., `SummarizeError`, `TranslateError`, `RewriteError`, `ExpandError`). They could be consolidated into a more generic `TextOperationError` to reduce redundancy.
- **[Design]** The error types are not consistent in their fields. For example, `ExtractError` has `Confidence`, but `GenerateError` does not. A more consistent design would make them easier to work with.
- **[Robustness]** Line 31: The `Unwrap` method on `ExtractError` creates a new error from a string (`fmt.Errorf(e.Reason)`). This loses any wrapped error information. It should wrap a proper error field if one exists.
- **[Design]** There is no common interface for these errors. An interface with methods like `GetRequestID()` and `GetTimestamp()` would make it easier to handle them generically.
- **[Clarity]** The error messages are formatted as strings, which is good for logging, but it would be better if the fields were also easily accessible for programmatic use.

s issues found in the codebase.

## docs/reference/API.md

- **[Doc]** Lines 16, 108, 113, 119: The file contains several string-based DSLs (for provider, filtering, sorting, and validation) that are not documented. It would be beneficial to add documentation explaining the grammar and available options for these DSLs.
- **[Design]** Line 30: The `schemaflux.ClientExtract` function and similar functions could be simplified to `client.Extract` for a more idiomatic Go API. This is a design choice, but the current naming is a bit redundant.
- **[Design]** Line 10: The global `schemaflux.Init` function introduces global state, which can make testing and configuration more difficult. The client-based approach (Option 2) is better, and it might be worth considering deprecating the global function.
- **[Doc]** Line 220: The local provider for testing is a great feature, but the documentation could be expanded to provide more examples of how to use it effectively for different scenarios.
- **[Doc]** General: The API is extensive. A "Common Patterns" or "Cookbook" section with more real-world examples for combining different operations would be very helpful for users.

## docs/guides/BUILD.md

- **[Doc]** Line 9: The Go version is specified as `1.24.6`, which is very specific. It would be better to specify a minimum version, like `1.24`.
- **[Doc]** Line 12: The instructions for installing `protoc` are only for macOS. Instructions for other operating systems should be added.
- **[Build]** Line 50: The `build-all.sh` script is mentioned but is not present in the repository.
- **[Build]** Lines 33-48: The build process for examples is manual and repetitive. A `Makefile` target or a script would be better.
- **[Test]** Line 78: `go test ./...` will run tests in the `examples` directory, which might have different dependencies. It would be better to target the main module's tests specifically.

## control_flow.go

- **[Design]** Line 8: The `Match` function uses `reflect` and string matching, which can be slow and error-prone. A more standard approach like a type switch or a map of functions would be better.
- **[Robustness]** Line 111: The `matchesStringCondition` function calls an LLM, which can be slow and unpredictable. This could lead to performance issues and unexpected behavior.
- **[Robustness]** Line 115: The `matchesStringCondition` function has a hardcoded timeout of 5 seconds. This might not be long enough for complex conditions.
- **[Robustness]** Line 135: The parsing of the LLM response in `matchesStringCondition` is brittle. It only checks for "true" or "yes".
- **[Refactor]** Line 14: The `Match` function has a lot of duplicated code for handling different condition types. This could be refactored to be more concise.
- **[Refactor]** Line 142: The `matchesType` function could be simplified.

## core_config_loader.go

- **[Robustness]** Line 21: The `LoadEnv` function's directory traversal to find `.env` stops at `/`, which is incorrect for Windows. It should use a condition like `dir != filepath.Dir(dir)` to handle drive roots correctly.
- **[Design]** Line 81: `InitWithEnv` calls `Init`, continuing the use of global state. It would be better to return a configured `*Client` instead.
- **[Refactor]** Lines 103-117: The logic for checking `SCHEMAFLUX_MODEL_*` and `OPENAI_*_MODEL` environment variables is duplicated three times. This should be refactored into a helper function.
- **[Concurrency]** The use of a single global mutex (`mu`) for all configuration settings is a bottleneck and prevents using multiple clients with different configurations concurrently. Each configuration setting should have its own lock or be part of a client struct.
- **[Clarity]** Line 161: The `GetModel` function calls an internal `getModel` function. The naming is confusing; it's not clear what the difference is without reading the other file.
- **[Logging]** Line 205: `SetDebugMode` logs a message when debug mode is disabled. It's probably better to not log anything in this case.

### ops_extended.go

*   **[Design] Critical Anti-Pattern in Client Methods**: All `Client...` functions (`ClientValidate`, `ClientFormat`, `ClientMerge`, etc.) use a dangerous, non-concurrent-safe pattern of temporarily overwriting global variables (`client`, `timeout`, `logger`). If two goroutines call these methods concurrently, they will race to set and unset the global state, leading to unpredictable behavior, incorrect API calls, and crashes. This defeats the purpose of a `Client` object and must be refactored. The implementation (`...Impl`) functions should accept client-specific state (like the `openai.Client` and `timeout`) as direct arguments.
*   **[Robustness] Brittle Fallback Logic**: In `validateImpl`, the fallback for failed JSON parsing is extremely weak. It performs a simple substring check for `"valid"`, which can easily lead to incorrect results (e.g., a response `"the data is not valid"` would be parsed as `true`). This should be removed or replaced with a more robust parsing strategy.
*   **[Refactor] Duplicated Client Logic**: The flawed pattern of setting and deferring the restoration of global variables is duplicated across five different functions. This repeated code should be removed as part of the larger refactor away from global state.
*   **[Robustness] Unsafe Indexing in `Deduplicate`**: The `deduplicateImpl` function trusts the indices returned by the LLM. While some checks are in place, a malicious or malformed response with out-of-bounds indices could cause a panic. The logic should be hardened to validate all indices from the LLM *before* using them to access slices.
*   **[Design] Inefficient `Deduplicate` Implementation**: The `Deduplicate` function sends all items to the LLM in a single request. This will not scale for large datasets and is vulnerable to context window limits. A more robust implementation should use batching or an embedding-based approach for larger inputs.
*   **[Robustness] Silent Failure in `Deduplicate`**: If the LLM response for grouping cannot be parsed, the function silently treats all items as unique. It should at least log a warning that deduplication failed to execute, which would aid in debugging.
*   **[Robustness] Weak Data Conversion in `formatImpl`**: If JSON marshaling fails, `formatImpl` falls back to `fmt.Sprintf("%v", data)`, which can produce useless output for complex types (e.g., `&{...}`). It would be better to return an error if the data cannot be meaningfully serialized.
*   **[Clarity] Undefined Helper Function**: The `mergeImpl` function depends on `generateTypeSchema`, which is not defined in this file, making the code harder to understand in isolation.
*   **[Doc] Missing Function Documentation**: The internal `...Impl` functions lack comments explaining what they do.

### ops_options.go

*   **[Design] Inefficient Fluent API**: All `With...` methods use value receivers (e.g., `func (e ExtractOptions) WithSchemaHints(...) ExtractOptions`), which causes the entire struct to be copied on every call. This is inefficient. A more idiomatic Go builder pattern would use pointer receivers (`func (e *ExtractOptions) ...`) to modify the struct in place.
*   **[Refactor] Duplicated Builder Methods**: Each specific options struct (e.g., `ExtractOptions`, `TransformOptions`) re-implements methods like `WithSteering` and `WithMode` simply to call the method on the embedded `CommonOptions`. This creates a large amount of redundant boilerplate code that is difficult to maintain.
*   **[Design] Incomplete API Transition**: The file introduces a new, typed, fluent options system (`ExtractOptions`, etc.) and a `BaseOptions` interface, but the core operation functions (`Extract`, `Summarize`, etc.) still accept the legacy `...OpOptions`. This indicates the transition to the new system is incomplete and the new options structs are not actually used to invoke operations.
*   **[Robustness] Brittle `ConvertOpOptions` Function**: The `ConvertOpOptions` function relies on a string `operationType` to determine which options struct to create. This is not type-safe and can lead to silent failures or incorrect behavior if the string is misspelled.
*   **[Clarity] Unused Compatibility Code**: The file contains several pieces of code for backward compatibility, such as `toOpOptions` and `IsLegacyOption`, that do not appear to be used anywhere in the codebase, adding dead code and confusion.
*   **[Test] Missing Validation Tests**: The file contains numerous `Validate()` methods with important business logic (e.g., checking for conflicting options, validating ranges) that are completely untested. This makes the validation logic fragile and unreliable.
*   **[Doc] Missing Builder Method Documentation**: Most of the `With...` builder methods on the specific option structs lack any documentation, forcing users to read the code to understand what they do.
*   **[Refactor] Confusing Coexistence of `OpOptions` and `CommonOptions`**: The library contains both the legacy `OpOptions` and the new `CommonOptions` structs, which have overlapping fields. This makes the configuration system confusing and should be resolved by fully deprecating and removing the legacy `OpOptions`.

### ops_options_test.go

*   **[Test] Incomplete Builder Pattern Tests**: The `TestBuilderPattern` function only validates the fluent builder for `ExtractOptions`. This test should be expanded to cover all other option structs (e.g., `TransformOptions`, `SummarizeOptions`, etc.) to ensure the builder pattern is implemented correctly and consistently across the entire API.
*   **[Test] Missing Negative Tests for Builders**: The tests for the builder pattern focus on the "happy path." They do not test what happens when invalid values are passed to the builder methods (e.g., `WithThreshold(1.5)`). While the `Validate()` method is tested separately, testing the builder methods directly would provide more granular feedback.
*   **[Test] Redundant Test Cases**: Many of the test cases for the `Validate()` methods are redundant. For example, the tests for `CommonOptions` already cover invalid thresholds, but these tests are repeated in other test functions. The tests could be made more concise by focusing on the specific validation logic of each struct and relying on the `CommonOptions` tests for the common cases.
*   **[Clarity] Lack of Assertions on Field Values**: The `TestBuilderPattern` test correctly checks if the fields are set, but the other tests for `Validate()` methods do not assert that the values set by the builder methods are correct. For example, in `TestExtractOptions`, the test for "with schema hints" does not actually check if the `SchemaHints` field was set correctly.
*   **[Test] Untested Code**: The `toOpOptions()` method is tested, but the `IsLegacyOption()` and `ConvertOpOptions()` functions are not fully tested for all edge cases. For example, what happens if an unknown `operationType` is passed to `ConvertOpOptions`?

### ops_pipeline.go

*   **[Design] Critical Anti-Pattern in `Execute`**: The `Execute` method for a `ClientPipeline` uses the same dangerous, non-concurrent-safe pattern of temporarily overwriting global variables (`client`, `timeout`, `logger`) as seen in `ops_extended.go`. This will cause race conditions if multiple pipelines are executed concurrently. The client's configuration should be passed down through the context or as direct arguments to the operations.
*   **[Design] Non-Idiomatic `Compose` Function**: The `Compose` function is not implemented correctly. It only executes the first operation in the list and returns, completely ignoring the rest. A proper composition function would chain the operations together, which is what the `Then` function does for two operations. The `Compose` function is misleading and broken.
*   **[Robustness] Type Safety Issues in Example Pipelines**: The example pipeline builders (`ExtractAndValidatePipeline`, `TransformAndFormatPipeline`) use type assertions (`input.(T)`) within their step functions. This is not safe. If a preceding step in a real-world pipeline were to return a different type, the pipeline would panic. The pipeline steps should be designed to handle `any` type more gracefully or the pipeline should enforce type safety between steps.
*   **[Refactor] Inconsistent Error Handling**: In `Execute`, a timeout error is added to `result.Errors`, but the function returns immediately. In contrast, a step failure in `FailFast` mode also returns immediately but might not have added all relevant errors. The error handling logic could be more consistent.
*   **[Clarity] Simplified `Compose` Logic**: The comment `// This is a simplified version - in practice you'd need more sophisticated type handling` in the `Compose` function acknowledges its limitations but doesn't fix the fact that the function is fundamentally broken and only executes one operation.
*   **[Test] Missing Pipeline Tests**: There are no tests for the `Pipeline` functionality. Tests should cover successful execution, `FailFast` behavior, optional steps, retries, and timeouts.
*   **[Test] Missing Composition Function Tests**: There are no tests for `Compose`, `Then`, `Map`, `MapConcurrent`, `Reduce`, `Tap`, `Retry`, or `CachedOperation`. These are fundamental building blocks for complex workflows and their correctness is critical.
*   **[Robustness] Hardcoded Retry Delay**: The retry logic in `Execute` uses a simple `time.Sleep(time.Duration(attempt+1) * time.Second)`, which is a linear backoff. This should be configurable and ideally support exponential backoff.
*   **[Design] Unused `SaveProgress` Option**: The `PipelineOptions` struct includes a `SaveProgress` field, but this is never used in the `Execute` method. This is dead code and a misleading feature.
*   **[Robustness] `MapConcurrent` Error Handling**: The `MapConcurrent` function returns only the first error it finds when iterating through the `errors` slice. It should return all errors, perhaps as a single aggregated error, so the caller has a complete picture of all failures.

### ops_pipeline_test.go

*   **[Test] Acknowledged Bug in `TestCompose`**: The test for the `Compose` function correctly identifies that the function is broken and only executes the first operation. While the test accurately reflects the current state, it means a known bug was committed and the test serves to confirm the bug rather than validate correct functionality.
*   **[Test] Skipped Timeout Test**: The `TestPipelineWithTimeout` is marked as skippable because it is "timing-dependent," which means a critical feature (pipeline timeout) is not being reliably tested. The test should be refactored to be deterministic, possibly by using a mock context or a more controlled way of simulating the delay.
*   **[Test] Inadequate `ClientPipeline` Test**: The test for `ClientPipeline` is superficial. It confirms that a pipeline can be created from a client, but it fails to verify the most important aspect: that the client's specific configuration (e.g., API key, timeout) is actually used by the pipeline's operations. This is a major gap, especially given the dangerous global variable manipulation in the implementation.
*   **[Test] Untested Example Pipelines**: The user-facing example functions (`ExtractAndValidatePipeline`, `TransformAndFormatPipeline`) are not covered by any tests, leaving their correctness unverified.
*   **[Test] Missing Failure Scenarios**: The tests for `Then` are incomplete; they check for a failure in the first operation but not the second. Similarly, `MapConcurrent` is not tested for cases where multiple concurrent operations fail.
*   **[Test] Missing Concurrency Test for `CachedOperation`**: The tests for `CachedOperation` validate its basic logic (caching, reset) but lack a specific test to ensure it is truly goroutine-safe by calling `Execute` from multiple goroutines concurrently.
*   **[Test] Missing Test for Retry Delay**: The `Retry` test confirms the number of attempts but does not verify that the specified delay between retries is actually being respected.

### ops_procedural.go

*   **[Robustness] Unsafe `Decide` Fallback**: The `Decide` function has multiple fallback paths (LLM failure, JSON parsing failure) that default to selecting the first option (`decisions[0]`). This is a dangerous default for a decision-making function, as it can lead to unintended consequences without a clear error. It should return an error instead of making an arbitrary choice.
*   **[Design] Inefficient `Guard` Implementation**: The `Guard` function calls an LLM to generate suggestions every time a check fails. This is slow and expensive. The suggestions should be optional, and the LLM call should be made only if explicitly requested.
*   **[Robustness] Brittle `StateMachine` Transition**: The `StateMachine` uses `reflect.TypeOf(event).Name()` to determine the event type for transitions. This is extremely brittle; it will fail for anonymous structs, pointers, and types from different packages with the same name. A more robust approach would be to use a map key that is less prone to collision, or require events to satisfy an interface that provides a unique identifier.
*   **[Refactor] Duplicated Retry Logic**: The file implements a `WithRetry` function, but the `Workflow.Execute` method contains its own, separate retry loop. This duplicated logic should be consolidated by having the workflow use the `WithRetry` function.
*   - **[Robustness] Missing Dependency Graph in `Workflow`**: The `Workflow.Execute` method processes steps in the order they are added. While it checks for dependencies, it does not perform a topological sort. If steps are added in an order that violates the dependency chain (e.g., `[B, A]` where B depends on A), the execution will fail. A proper workflow engine should build a dependency graph and execute steps in the correct order.
*   **[Robustness] Incomplete `Workflow` Compensation**: The compensation logic in the workflow is flawed. It attempts to compensate all completed steps, but it does so in the reverse order of their definition, not the reverse order of their execution. Furthermore, it ignores errors from the `Compensate` functions, which could leave the system in an inconsistent state.
*   **[Design] Unused `StateMachine` Timeout**: The `StateDefinition` includes a `Timeout` field, but it is never used in the `StateMachine` implementation. This is a misleading and unimplemented feature.
*   **[Clarity] Unnecessary `Try` Function**: The `Try` function, which uses `recover` to catch panics, is generally considered an anti-pattern in Go. Idiomatic Go prefers explicit error handling. This function encourages a style that can hide bugs and make code harder to reason about.
*   **[Test] Missing Tests**: None of the complex procedural constructs in this file (`Decide`, `Guard`, `StateMachine`, `Workflow`, `LoopWhile`, etc.) are covered by tests. This is a critical gap, as these components have complex logic and many potential failure modes.
*   **[Robustness] Hardcoded `Guard` Timeout**: The LLM call within `Guard` has a hardcoded 2-second timeout, which may not be sufficient and is not configurable.
*   **[Robustness] `LoopWhile` Lacks Infinite Loop Protection**: While `LoopWhile` has a `maxIterations` check, it doesn't have a timeout, which could lead to very long-running or effectively infinite loops if the body function is slow but the condition remains true.

### ops_procedural_test.go

*   **[Test] Incomplete `Decide` Test**: The tests for the `Decide` function are inadequate. They mock the LLM response but do not test the critical fallback behavior that occurs when the LLM fails or returns malformed JSON. This is the most dangerous part of the function, and it is completely untested.
*   **[Test] Incomplete `Guard` Test**: The test for `Guard` does not cover the case where the LLM call fails. In this scenario, the function should still return the list of failed checks but without suggestions. This behavior is not validated.
*   **[Test] Brittle `StateMachine` Test**: The test for the `StateMachine` transition relies on `sm.Transition(Event{Type: "start"})`. However, the implementation uses `reflect.TypeOf(event).Name()`, which would return `"Event"`, not `"start"`. This test only passes because the transition is defined as `sm.AddTransition(StateIdle, "Event", StateWorking)`. This highlights the brittleness of the implementation, as the test is coupled to the exact type name rather than a logical event name.
*   **[Test] Missing `StateMachine` Timeout Test**: The `StateDefinition` includes a `Timeout` field, but there is no test to verify its functionality. This is because the feature is not implemented, and the test suite fails to catch this dead code.
*   **[Test] Missing `Workflow` Failure Tests**: The `Workflow` tests cover basic execution and compensation but miss several key failure scenarios. There are no tests for:
    *   What happens if a `Compensate` function itself returns an error.
    *   What happens if a step without retry capability fails.
    *   The behavior of the workflow when a step's dependencies are added in the wrong order.
*   **[Test] Missing `WithRetry` Non-Retryable Error Test**: The tests for `WithRetry` do not cover the scenario where the operation returns a non-retryable error (as determined by `isRetryableError`). This is a critical path that is completely untested.
*   **[Test] Superficial `Try` Test**: The test for the `Try` function confirms that it can catch a panic, but it doesn't test for more complex scenarios, such as what happens if the panic value is not an error or if the operation itself returns a non-nil error.
*   **[Clarity] Mock Overwriting**: The tests for `Decide` and `Guard` both overwrite the global `callLLM` function. This is a fragile pattern that can lead to test pollution. If `t.Parallel()` were used, these tests would interfere with each other. The mock should be scoped to each test or sub-test.

### ops_text.go

*   **[Design] Global State Dependency**: All functions in this file (`Summarize`, `Rewrite`, `Translate`, `Expand`) create a new context with a timeout using the global `timeout` variable and implicitly use the global `client` via `callLLM`. They are not integrated with the `Client` struct, making them non-thread-safe and incompatible with multiple client configurations. This is a major design flaw.
*   **[Refactor] Duplicated Code**: The logic for building the `steering` prompt from the options struct, creating a `context.WithTimeout`, defining a `systemPrompt`, and calling `callLLM` is duplicated across all four functions. This boilerplate code should be extracted into a helper function to reduce redundancy.
*   **[Design] Incomplete API Transition**: These functions accept the new, typed options structs (e.g., `SummarizeOptions`), but they immediately convert them to the legacy `OpOptions` using `toOpOptions()`. This indicates that the new, richer options system is not fully integrated and the core logic still relies on the old, less-typed structure.
*   **[Robustness] Inconsistent Error Handling**: The functions return custom error types (e.g., `SummarizeError`), but they are created by wrapping `err.Error()`, which discards the original error's type and stack trace. A better approach would be to wrap the original error (e.g., `Reason: err`).
*   **[Test] Missing Tests**: There are no corresponding tests for any of the functions in this file (`Summarize`, `Rewrite`, `Translate`, `Expand`). This is a significant gap, as the prompt construction logic and interaction with the LLM are complex and need validation.
*   **[Doc] Missing Client-Based Examples**: The functions do not have client-based counterparts (e.g., `ClientSummarize`), which is inconsistent with the pattern seen in other files like `ops_extended.go`.

### otel.go

*   **[Design] Global State for Tracing**: The entire tracing system is built around global variables (`tracer`, `traceProvider`, `tracingEnabled`). This makes it impossible to have different tracing configurations within the same application (e.g., for different clients) and makes testing difficult. Tracing should be managed as part of the `Client` struct.
*   **[Robustness] Unbounded Exporter Creation**: The `InitTracing` function appends exporters to a slice based on environment variables. If multiple exporter endpoints are set (e.g., both Jaeger and OTLP), it will create and use all of them without any warning or clear precedence, which could lead to unexpected performance overhead and data duplication.
*   **[Robustness] Insecure OTLP Client**: The OTLP exporter is configured with `otlptracegrpc.WithInsecure()`, which disables TLS. This is a significant security risk for production environments and should be configurable.
*   **[Refactor] Hardcoded Service Version**: The service version is hardcoded to `"1.0.0"` in the resource attributes. This should be configurable or dynamically determined.
*   **[Clarity] Confusing Environment Variable Logic**: The `getEnvironment` helper function checks for three different environment variables (`SCHEMAFLUX_ENVIRONMENT`, `ENVIRONMENT`, `ENV`) to determine the environment. This is confusing and should be simplified to a single, well-documented variable.
*   **[Robustness] `RecordSpanEvent` Type Handling**: The `RecordSpanEvent` function has a `switch` statement to handle different attribute types, but it has a `default` case that converts any unknown type to a string using `fmt.Sprintf("%v", val)`. This can lead to unhelpful or unreadable attribute values for complex types.
*   **[Test] Missing Tests**: There are no tests for any of the functions in this file. Critical logic like `InitTracing`, `StartSpan`, and `RecordLLMCall` is completely untested. This means there is no validation for exporter configuration, span creation, or attribute recording.
*   **[Design] Tracing Not Integrated with Client**: The `StartSpan` function accepts a legacy `OpOptions` struct to extract tracing attributes. It is not integrated with the new typed options system, nor is it connected to the `Client` struct, further cementing the library's reliance on outdated and global patterns.
*   **[Robustness] Silent Failures in `InitTracing`**: When an exporter fails to be created (e.g., due to a malformed endpoint), the function logs the error but continues execution. This can lead to a state where tracing is thought to be enabled but no data is being exported. It would be better to return an error and halt initialization.

### otel_test.go

*   **[Test] No-Op Tests**: The tests in this file are effectively no-ops. They call functions like `RecordLLMCall` and `AddSpanTags` but make no assertions about the results. The comments explicitly state `"No error expected, just ensuring it doesn't panic"`. This is not a valid testing strategy, as it doesn't verify that the functions are actually working correctly (e.g., that the attributes were added to the span).
*   **[Test] Tracing Not Initialized**: The tests call tracing functions like `StartSpan` without ever calling `InitTracing`. This means the global `tracer` is `nil` and `tracingEnabled` is `false`. As a result, `StartSpan` returns a no-op span, and the tests are not exercising any of the real tracing logic. The entire test file provides a false sense of security.
*   **[Test] Missing `InitTracing` Test**: The most complex and critical function in `otel.go`, `InitTracing`, is not tested at all. There are no tests to verify that exporters are configured correctly based on environment variables, that the resource is created properly, or that the tracer is initialized.
*   **[Test] Missing `ShutdownTracing` Test**: The `ShutdownTracing` function is not tested, leaving its behavior unverified.
*   **[Test] Missing Context Propagation Tests**: The `ExtractTraceContext` and `InjectTraceContext` functions are not tested, which is a critical gap for ensuring distributed tracing works correctly.
*   **[Test] Incomplete `GetSpanID` Test**: The `TestGetSpanID` test only checks that the span ID is not an empty string. It doesn't validate the format of the ID or that it matches the ID of the created span.

### pricing.go

- **[Design] Hardcoded Pricing Data**: All pricing information is hardcoded directly into the `pricingModels` map. This is not sustainable, as LLM prices change frequently. This data should be loaded from a configuration file or a remote service to allow for easy updates without recompiling the application.
- **[Robustness] Non-Concurrent `totalCosts` Map**: The `totalCosts` map is read from and written to by `TrackCost` and `GetTotalCost` under a mutex, but the map itself is not initialized in a thread-safe way. If `TrackCost` is called concurrently by multiple goroutines for the first time, it could lead to a race condition on `totalCosts == nil`.
- **[Robustness] Unbounded `costHistory` Slice**: The `costHistory` slice is appended to indefinitely by `TrackCost`. For a long-running application, this will lead to unbounded memory growth and eventually cause the application to crash. There should be a mechanism to cap the size of the history or rotate it.
- **[Refactor] Inconsistent Default Pricing**: The `getDefaultPricing` function returns a hardcoded default model for each provider (e.g., `gpt-3.5-turbo` for OpenAI). This is a poor assumption, as the user might be using a completely different model that simply isn't in the pricing list. The function should return a zero-cost model and log a more prominent warning.
- **[Robustness] Hardcoded Budget Threshold**: The `checkBudgetLimits` function has a hardcoded threshold of `0.8` (80%) for triggering the budget callback. This should be configurable.
- **[Test] Missing Tests**: There are no tests for any of the functions in this file. Critical logic for cost calculation, tracking, budgeting, and reporting is completely unvalidated.
- **[Robustness] Incomplete `ExportCostReport`**: The `ExportCostReport` function has a placeholder for JSON format (`report = "[]"`). This is an incomplete feature that will not work as expected.
- **[Clarity] Confusing `totalCosts` Keys**: The keys for the `totalCosts` map are constructed with prefixes like `"daily_"` and `"weekly_"`. This is a fragile way to manage time-based aggregation and could be replaced with a more robust time-series data structure.
- **[Design] Global State**: The entire pricing and cost tracking system is based on global variables (`pricingModels`, `costMutex`, `totalCosts`, etc.), making it impossible to have separate cost tracking for different clients or in different parts of an application. This should be encapsulated within the `Client` struct.

### pricing_test.go

*   **[Test] State Pollution Between Tests**: The tests in this file all manipulate the same global state (`costHistory`, `totalCosts`) without any cleanup or isolation. `TestTrackCost` adds a cost, and `TestGetCostBreakdown` adds another. When run together (e.g., with `go test`), the results of one test will be affected by the state left over from the previous one. This makes the tests flaky and unreliable. Each test should run in isolation, for example by clearing the cost history in a `t.Cleanup` function.
*   **[Test] Inaccurate `TestCalculateCost`**: The test for `CalculateCost` uses a wide range for its assertions (`expectedMin`, `expectedMax`) instead of calculating the exact expected cost. This makes the test weak, as it would not catch small but significant miscalculations. The test for an "Unknown model" is particularly vague, allowing for any cost between 0 and 0.1.
*   **[Test] No-Op Test for Filtering**: The `TestTrackCost` function includes a commented-out section for testing filtering, with the note `"filtering by operation may not work without proper implementation"`. It then calls `GetTotalCost` with a filter but makes no assertions on the result, effectively skipping the test for this critical feature.
*   **[Test] Incomplete `ExportCostReport` Test**: The test for `ExportCostReport` checks for an error when an invalid format is requested, but for valid formats (`json`, `csv`), it only checks that the report is not empty. It does not validate the content or structure of the report, meaning a test for a broken CSV or an empty JSON array would still pass.
*   **[Test] Missing `SetBudget` Test**: The `SetBudget` function and its associated callback logic are not tested at all. This is a critical feature for cost control, and its absence from the test suite is a major gap.
*   **[Test] Missing `GetTotalCost` Edge Case Tests**: The tests for `GetTotalCost` do not cover edge cases, such as when there is no cost history or when the `since` parameter is in the future.
*   **[Clarity] Confusing Test Logic**: The `TestGetCostBreakdown` test logs a message if the breakdown is empty but doesn't fail the test. This makes it unclear whether an empty breakdown is a valid state or a sign of a problem.

### provider_test.go

*   **[Test] Mock `AnthropicProvider` Tested as Real**: The test for `AnthropicProvider` calls its `Complete` method and checks the response. However, the implementation of `AnthropicProvider` in `core_provider.go` is just a mock that returns a hardcoded response. The test is therefore not validating any real functionality and gives a false sense of security.
*   **[Test] Inaccurate Cost Estimation Tests**: The `TestProviderCostEstimation` tests use very wide ranges for their assertions (e.g., `cost < 0.01 || cost > 0.1`). This is not a precise way to test cost calculation. The tests should calculate the expected cost based on the hardcoded pricing and assert that the result is very close to that value.
*   **[Test] Fragile `TestProviderIntegration`**: This test manipulates global state (`defaultClient`, `callLLM`) to force the system to use a provider-based implementation. This is a very fragile and complex way to test the integration. It highlights the difficulty of testing the codebase due to the heavy use of global variables and the confusing dual-initialization system.
*   **[Test] Incomplete `Client.WithProvider` Test**: The test for `client.WithProvider("anthropic")` is incomplete. It sets the provider but then immediately overwrites it without ever using it to perform an operation. This means the test doesn't confirm that the newly set provider is actually used.
*   **[Test] Missing Failure Case Tests**: The tests do not cover failure scenarios, such as:
    *   What happens when `NewOpenAIProvider` is called with an empty API key.
    *   What happens when `registry.Get` is called for a provider that doesn't exist.
    *   What happens when `registry.SetDefault` is called for a provider that doesn't exist.
*   **[Test] Missing `ProviderRegistry` Concurrency Test**: The `ProviderRegistry` is protected by a mutex, but there are no tests to verify that it is safe to use from multiple goroutines concurrently (e.g., registering and getting providers at the same time).
*   **[Clarity] Unused `TestProviderTimeout`**: The `TestProviderTimeout` test is well-written, but the `LocalProvider`'s `Complete` method doesn't actually use the context it receives, so the timeout is never triggered. This is another example of a test that appears to be working but is not actually testing the intended functionality.

### README.md

*   **[Doc] Out-of-Date Function Names**: The README contains several examples with function and type names that are no longer correct. For instance:
    *   `schemaflux.Batch()` should be `schemaflux.NewBatchOptions()`.
    *   `schemaflux.ProcessBatch()` is not a function in the codebase.
    *   `results.Metadata.EstimatedCost` and `results.Metadata.TokensSaved` do not exist on the batch result struct.
*   **[Doc] Broken Links**: The link `[**📚 Full API Documentation →**](docs/reference/API.md)` is a relative link that will work on GitHub, but the other links to issues and discussions are full URLs. For consistency, all links should be full URLs.
*   **[Doc] Inconsistent Initialization**: The "Simple Example" shows `schemaflux.Init("your-api-key")`, while the "Get Started" section recommends using environment variables with `schemaflux.Init(os.Getenv("SCHEMAFLUX_API_KEY"))`. The "Complex Example" uses `schemaflux.NewClient(apiKey)`, which is the more modern, preferred approach. The documentation should be consistent and strongly recommend the client-based approach over the global `Init`.
*   **[Doc] Misleading "Circuit Breakers" Claim**: The "Why It Makes Sense" section claims the library has "circuit breakers," but there is no implementation of a circuit breaker pattern in the codebase. This is a misleading claim.
*   **[Doc] Confusing `otel.Start` Example**: The observability example shows `ctx = otel.Start(ctx, "process-batch")`. The `otel` package in Go is the OpenTelemetry API, and it does not have a `Start` function. The correct way to start a span is `tracer.Start(...)`. This example is incorrect and will not compile.
*   **[Doc] Inconsistent `ExtractBatch` Examples**: The `ExtractBatch` examples show two different ways of calling it, one with `schemaflux.Batch()` and another with `schemaflux.ExtractBatch[Invoice](...)`. The function signatures and builder patterns are inconsistent with the actual code.
*   **[Doc] Missing `go mod tidy`**: The "Get Started" section should include `go mod tidy` after `go get` to ensure dependencies are clean.

### scripts/run_tests.sh

*   **[Build] Not Cross-Platform**: The script is a bash script (`#!/bin/bash`) and uses Unix-specific commands and syntax (e.g., `[[ ... ]]`, `fswatch`, `open`). It is not compatible with Windows, which is the user's operating system.
*   **[Build] Missing Dependencies and OS-Specific Instructions**: The `watch` mode requires `fswatch`, and the script provides installation instructions using `brew`, which is specific to macOS. The `badge` mode requires `gocov`, which it attempts to install, but the comment also mentions `gocov-xml` which is not handled.
*   **[Build] Fragile `failures` Mode**: The `failures` mode relies on `grep` to parse the output of `go test`. This is a brittle approach that can easily miss certain types of errors or panics, leading to a false sense of security.
*   **[Build] Bash-Specific Features**: The script uses `PIPESTATUS`, which is a bash-specific feature, to determine the exit code of the test command. This makes the script less portable to other shells.
*   **[Doc] Incomplete Badge Generation Documentation**: The comment for the `badge` target mentions that it requires `gocov-xml` and `gocov`, but the script only attempts to install `gocov`.

### docs/engineering/specs/SCHEMAFLUXDSLSPEC.md

*   **[Doc] Out-of-Date Spec**: This file appears to be a design specification for a workflow engine DSL. However, the concepts and node types described in this document (e.g., `task.service`, `router`, `wait.timer`) do not match the operations implemented in the `schemaflux` library (e.g., `Extract`, `Classify`, `Decide`). This indicates that the implementation has diverged significantly from the original specification.
*   **[Doc] Mismatch with `ops_procedural.go`**: The `Workflow` and `StateMachine` structs defined in `ops_procedural.go` are a Go implementation of a workflow engine, but they do not seem to be related to the JSON-based DSL described in this document. The DSL spec is for a configurable, UI-agnostic engine, while the Go code provides a programmatic, code-first approach.
*   **[Doc] Unused Concepts**: The document details concepts like `connectors`, `route_tables`, and `storage_bindings`, none of which are present in the Go codebase. This suggests that either the project is incomplete or the direction has changed.
*   **[Doc] Inconsistent Naming**: The file is named `SCHEMAFLUXDSLSPEC.md`, but the content refers to a "Workflow DSL v1". The relationship between this DSL and the `SchemaFlux` library is unclear.
*   **[Doc] Broken `code.inline` Example**: The `code.inline` node example for Go provides a function signature `func Main(inputJSON []byte, paramsJSON []byte) ([]byte, error)`, but the implementation details are missing. The comment `(Engine compiles/execs safely under the hood; you just supply the text.)` implies a complex runtime that does not appear to exist in the current codebase.

### `docs/engineering/specs/WORKFLOW_ENGINE_EXTERNAL_INTERFACES.md`

- **[Design] Aspirational, Not Implemented:** This document describes a comprehensive, robust, and feature-rich architecture for integrating external services. It includes concepts like a `Service Connector Framework`, gRPC/REST adapters, circuit breakers, retry policies, a service registry, and detailed observability. **None of this is implemented in the current codebase.** The existing code in `ops_pipeline.go` and `ops_procedural.go` is a primitive and flawed prototype that bears no resemblance to this specification.
- **[Design] Conflicting Specifications:** The JSON-based Workflow DSL shown in this document (e.g., `"type": "external_task"`) is inconsistent with the YAML-based DSL described in `SCHEMAFLUXDSLSPEC.md` (e.g., `action: "http"`). This indicates a lack of a single, coherent design vision for the project. The project has multiple, conflicting design documents.
- **[Doc] Misleading Code Examples:** The document contains numerous Go code snippets (`GRPCConnector`, `BatchAccumulator`, `AuthManager`, `MockService`, etc.) that are presented as implementation patterns. This code **does not exist** in the repository. It is purely illustrative of a system that has not been built, which is highly misleading to anyone trying to understand the current state of the project.
- **[Doc] Unimplemented Features:** The document is a list of unimplemented features. Key missing components include:
    - Service Connector/Registry (`hr-workday`, `payment-stripe`).
    - gRPC service contracts (`ExternalTaskExecutor`).
    - Asynchronous task handling with callbacks.
    - Batch processing accumulators.
    - A real authentication manager with a secret vault.
    - A mock service framework for testing.
- **[Conclusion]** This document is a "ghost" specification. It details a target architecture that is so far removed from the current implementation that it serves more as a source of confusion than a guide. The primary issue is that it presents a vision for the project that is completely disconnected from the reality of the code.

### docs/engineering/plans/workflowengineplan.md

- **[Design] Aspirational, Not Implemented:** This document is an extremely detailed, "world-class" design for a workflow engine. It is a theoretical blueprint and is even more disconnected from the actual codebase than the other markdown files. It describes a sophisticated, event-sourced, ACID-compliant, and scalable system. **None of the described architecture, features, or APIs are implemented.**
- **[Doc] Purely Theoretical:** The document explicitly states "No code is included" and serves as a design document. It covers concepts like a Canonical Workflow Event (CWE), a detailed persistence model, an execution engine, inter-workflow communication, and security protocols that are entirely absent from the Go files.
- **[Design] Contradicts Other Specs:** This plan introduces its own concepts and terminology (e.g., CWE, `AdvanceRun` function, JSON DSL) that are different from what is described in `SCHEMAFLUXDSLSPEC.md` and `docs/engineering/specs/WORKFLOW_ENGINE_EXTERNAL_INTERFACES.md`. This further highlights the lack of a single, coherent vision for the project. The project has at least three different, conflicting high-level designs.
- **[Conclusion]** This document is a design document for a potential future product, not a description of the existing `schemaflux` library. It is valuable as a long-term vision but is completely misleading as a guide to the current code. The primary issue is that it outlines a system that is orders of magnitude more complex and robust than what has been built.

### `WORKFLOW_ENGINE_TODO.md`

- **[CRITICAL] Falsified Progress:** This document is a TODO list where the vast majority of tasks, from foundational setup to advanced features, are marked as complete (`[x]`). This is grossly inaccurate and misleading. Based on the review of the actual codebase, **less than 5%** of the features listed here are implemented, and those that exist are primitive, buggy, and do not follow the described architecture.
- **[Doc] Aspirational, Not Reality:** The TODO list corresponds to the "world-class" engine designed in `docs/engineering/plans/workflowengineplan.md`, not the actual `schemaflux` library. It includes hundreds of specific, granular tasks (e.g., "Create event partitioning by tenant and time," "Implement circuit breaker for external services," "Add BYOK support") that have no corresponding code in the repository.
- **[Conclusion]** This document is the most problematic of all the markdown files. It creates a completely false impression of a mature, feature-complete, and robust system. In reality, the project is in a very early, experimental, and unstable state. The discrepancy between this TODO list and the codebase is a major red flag for the project's status and integrity.



package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The Result forms of RedactLLM, Generate and Audit.
//
// An application built on this library could see what Extract, Sort, the text
// operations and the judgments cost and how long they waited, and could not see
// the same for these three: they returned `(T, error)` and the record of the
// call was thrown away inside callLLM. The three Result forms here carry it out
// the way ExtractResult does, by attaching a recorder to the context and
// running the existing function under it, so the plain form and the Result form
// execute the same code and cannot drift.
//
// The name of the Result form is not always the obvious one. RedactLLMResult
// and AuditResult are types in this package already (the redaction and the
// audit's own value), so the two forms are RedactLLMWithMeta and
// AuditWithMeta. The package has no earlier case of a Result form whose natural
// name was taken; GenerateResult is free and is used as is.

// dx12ResultFrom runs one operation under a call recorder and returns its value
// beside the record of how it ran.
//
// The request and correlation ids are settled before anything else and handed
// to run, so the envelope names them on every path: after a refusal before any
// call was made, after an error before one was made, and after a call. SortResult
// built the envelope of a refused call by hand and left them out, and a caller
// correlating a failure with its own logs got an empty string exactly when the
// call had been turned away. An id the caller supplied, on the options or on
// the context, is the one reported; one the library generates is reported too.
//
// establishes is the contract level a success of this operation delivers. It is
// checked against the caller's policy floor before run is called, so a floor the
// call can never meet costs nothing, and again after.
func dx12ResultFrom[T any](
	operation string,
	base context.Context,
	requestID, correlationID string,
	establishes types.ContractLevel,
	run func(ctx context.Context, requestID, correlationID string) (T, error),
) (_ types.Result[T], runErr error) {
	ctx, records := withCallRecording(WithOperationName(base, operation))
	// The call is one unit to the installed observers, ending with the kind of
	// whatever error this returns.
	ctx, observed := observeOperation(ctx, operation)
	defer observed.end(&runErr)
	ctx, tracking := requesttracking.Ensure(ctx, requestID, correlationID)

	// stamped puts the settled ids on an envelope. What the records carry is
	// the same pair, since the call is handed them; the settled ones win so
	// that an envelope built from no records at all says the same thing.
	stamped := func(meta types.Meta) types.Meta {
		if tracking.RequestID != "" {
			meta.RequestID = tracking.RequestID
		}
		if tracking.CorrelationID != "" {
			meta.CorrelationID = tracking.CorrelationID
		}
		return meta
	}

	if floorErr := checkContractFloor(ctx, establishes); floorErr != nil {
		refused := stamped(types.Meta{Operation: operation, RequestedContract: establishes})
		return types.Result[T]{Meta: refused}, floorErr
	}

	value, err := run(ctx, tracking.RequestID, tracking.CorrelationID)

	meta := stamped(records.meta(operation))
	meta.RequestedContract = establishes
	if err != nil {
		return types.Result[T]{Meta: meta}, err
	}
	meta.DeliveredContract = establishes

	// The floor of the caller's policy, on the call or on the client: an answer
	// delivered below it is an error and not a quieter success.
	if policyErr := enforceContractPolicy(ctx, meta.DeliveredContract); policyErr != nil {
		return types.Result[T]{Meta: meta}, policyErr
	}
	return types.Result[T]{Value: value, Meta: meta}, nil
}

// dx12FirstID returns the first of the two that is set.
func dx12FirstID(first, second string) string {
	if first != "" {
		return first
	}
	return second
}

// redactLLMResultContract is what a redaction establishes: the model's answer
// is declared as a schema, and every span is checked in Go against the text it
// claims to be a span of, and against the categories the caller asked for.
const redactLLMResultContract = types.ContractSchemaAndInvariantChecked

// auditResultContract is what an audit establishes: the answer is declared as a
// schema, every finding is checked against the policies and categories that
// were asked about, and the verdict is derived in Go.
const auditResultContract = types.ContractSchemaAndInvariantChecked

// RedactLLMWithMeta runs RedactLLM and returns the record of how it ran beside
// the redaction: what the call cost, how many attempts it took, how long it
// waited, and which request and correlation ids it carried.
//
// It is RedactLLM, called under a recorder; there is no second implementation.
// On any error the value is empty and the envelope is still returned, because a
// refused redaction is the case where the record is read.
//
// The contract it reports is the invariant-checked one: spans are located in
// the text by this library and a span outside the requested categories is
// refused. What it does not establish is that a span's category is right, which
// is the model's judgement.
func RedactLLMWithMeta(ctx context.Context, text string, opts RedactLLMOptions) (types.Result[RedactLLMResult], error) {
	if ctx == nil {
		ctx = opts.Context
	}
	return dx12ResultFrom("redact_llm", ctx, opts.RequestID, opts.CorrelationID, redactLLMResultContract,
		func(ctx context.Context, requestID, correlationID string) (RedactLLMResult, error) {
			opts.RequestID, opts.CorrelationID = requestID, correlationID
			opts.Context = ctx
			return RedactLLM(ctx, text, opts)
		})
}

// GenerateResult runs Generate and returns the record of how it ran beside the
// generated value.
//
// It is Generate, called under a recorder. The contract it reports depends on
// the type asked for (ContractLevelFor): a string is the model's text as it
// came, so nothing was enforced; a type a strict schema can describe is declared
// to the provider and decoded into the caller's type, which is schema
// constrained; any other type (a map, a recursive type) has no schema to send,
// and the answer is only read as JSON. A floor above the level of the type is
// refused before anything is sent.
func GenerateResult[T any](prompt string, opts GenerateOptions) (types.Result[T], error) {
	return dx12ResultFrom("generate", resolvedContext(opts.CommonOptions, opts.OpOptions),
		dx12FirstID(opts.CommonOptions.RequestID, opts.OpOptions.RequestID),
		dx12FirstID(opts.CommonOptions.CorrelationID, opts.OpOptions.CorrelationID),
		ContractLevelFor[T]("generate"),
		func(ctx context.Context, requestID, correlationID string) (T, error) {
			opts.CommonOptions.RequestID, opts.CommonOptions.CorrelationID = requestID, correlationID
			opts.OpOptions.RequestID, opts.OpOptions.CorrelationID = requestID, correlationID
			useContext(&opts.CommonOptions, &opts.OpOptions, ctx)
			return Generate[T](prompt, opts)
		})
}

// AuditWithMeta runs AuditWithModel and returns the record of how it ran beside
// the judgment: what the audit cost, how many attempts it took, how long it
// waited, and which request and correlation ids it carried.
//
// It is AuditWithModel, called under a recorder, so which findings count, how
// the threshold lists them and what the verdict is are exactly what that
// function does.
func AuditWithMeta[T any](data T, opts ...AuditOptions) (types.Result[types.JudgmentResult[T]], error) {
	var given AuditOptions
	if len(opts) > 0 {
		given = opts[0]
	}
	return dx12ResultFrom("audit", given.Context, given.RequestID, given.CorrelationID, auditResultContract,
		func(ctx context.Context, requestID, correlationID string) (types.JudgmentResult[T], error) {
			// A copy: the caller's options are theirs, and the context, which
			// carries the recorder, must not leak into a value they reuse.
			given.Context = ctx
			given.RequestID, given.CorrelationID = requestID, correlationID
			return AuditWithModel(data, given)
		})
}

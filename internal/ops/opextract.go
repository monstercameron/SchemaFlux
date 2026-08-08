package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A-012: Extract ported to the Op descriptor, as the proof A-001 asked for.
//
// core.go's Extract keeps every step upstream of the model call exactly as
// it was -- option validation, steering assembly, PreflightType, schema
// generation, the cache identity, input normalization, prompt construction
// -- and hands off to runExtractOp only for the part that used to be a
// direct withRepair call. That is the whole port: the bytes sent to the
// model, the schema and cache identities, and the error type returned on
// failure are unchanged, which is what TestGoldenPrompts (golden_prompts_
// test.go, unmoved) and the existing internal/ops extract tests
// (untouched) both check.

// extractOpMeta is the part of Extract's descriptor that does not depend on
// the target type T: identity, semantics, batch class, default policy. It is
// built through MustNewOp at package init, so if this ever loses its batch
// class -- BatchNone, declared explicitly because Extract has no batch form
// today -- every test run panics during init rather than a caller
// discovering it as a silent behaviour change. op_test.go's
// TestStableExtractOpFailsInitWithoutBatchClass reproduces the same check
// against a copy with the batch class stripped, so the mechanism itself has
// a witness independent of extractOpMeta staying correct.
var extractOpMeta = MustNewOp(Op[any, any]{
	ID: types.OperationID{Name: "extract", Version: extractPromptVersion},
	Semantics: types.Semantics{
		Category:           types.CategoryExtraction,
		InferencePermitted: true,
		EvidenceRequired:   false,
		PreservesIdentity:  false,
		PreservesOrder:     false,
		Stability:          types.StabilityStable,
	},
	Batch: BatchAlgebra[any, any]{Class: types.BatchNone},
	// RepairAttempts is left at zero rather than set to
	// DefaultRepairAttempts: zero is the "caller stated no opinion" value
	// repairAttempts() (repair.go) reads, and it resolves through
	// config.GetRepairAttempts() -- which is what the pre-port Extract did
	// by calling withRepair with a bare RepairPolicy{}. Setting this to
	// DefaultRepairAttempts here would fix the repair budget at 1
	// regardless of configuration, which is a behaviour change the task
	// instructions say to stop and report rather than make. It is left
	// unset on purpose, and this note is that report.
	DefaultPolicy: types.DefaultPolicy{},
})

func init() {
	registerStableOp(extractOpMeta.ID, extractOpMeta.Batch.Class)
}

// newExtractOp builds Extract's descriptor for one call.
//
// It is built per call, not kept as a single package-level Op, because the
// decoder depends on T and Go generics have no way to range a package
// variable over a type parameter. Every field that does not depend on T is
// copied from extractOpMeta -- the one place that declares them -- so an Op
// for a concrete type like Op[any, Invoice] could be built once and reused
// by a caller who wants that; Extract itself does not need to, since it is
// called with a fresh input each time regardless.
//
// systemPrompt and userPrompt are computed by core.go's Extract before this
// is called, from the same functions (BuildExtractSystemPrompt,
// fmt.Sprintf) it always used -- BuildPrompt here is a closure returning
// them unchanged, not a second prompt-construction path.
func newExtractOp[T any](opt types.OpOptions, systemPrompt, userPrompt string) Op[any, T] {
	mode := opt.Mode

	return Op[any, T]{
		ID:        extractOpMeta.ID,
		Semantics: extractOpMeta.Semantics,
		Contract: OutputContract[T]{
			// opt.SchemaID already carries the fuller schema identity
			// (schemaid.go); SchemaName here is the same
			// caller-facing name already computed into opt.SchemaName by
			// core.go, not a second reflection over T.
			SchemaName: opt.SchemaName,
			Decode: func(body string) (T, error) {
				var attempt T

				// Strict's two rules, asked separately (DX-007). Strict still
				// means both; either can now be had without the other, because
				// they check unrelated things and bundling them made each one a
				// surprise to whoever wanted the other.
				exact := mode == types.Strict || opt.ExactFields
				complete := mode == types.Strict || opt.CompleteFields

				if exact {
					// Unchanged from the pre-port Extract: an unrecognised
					// property, a repeated key, a second value, or a body
					// past the limits is a failure rather than something
					// encoding/json quietly resolves (F-035).
					if err := DecodeExact(body, &attempt, DecodeLimits{}); err != nil {
						return attempt, err
					}
				} else if err := ParseJSONStrict(body, &attempt); err != nil {
					return attempt, err
				}

				if complete {
					if err := ValidateExtractedData(attempt); err != nil {
						return attempt, err
					}
				}
				return attempt, nil
			},
		},
		Batch:         BatchAlgebra[any, T]{Class: extractOpMeta.Batch.Class},
		DefaultPolicy: extractOpMeta.DefaultPolicy,
		BuildPrompt: func(_ any, _ types.OpOptions) (string, string) {
			return systemPrompt, userPrompt
		},
	}
}

// runExtractOp is what core.go's Extract calls instead of withRepair
// directly. input to RunOp is always nil: Extract's BuildPrompt does not
// read it, because the prompt was already rendered from the normalized input
// before this is reached -- In is `any` here for exactly that reason, not
// because the input is unused by the operation, only by this already-
// rendered call.
func runExtractOp[T any](ctx context.Context, opt types.OpOptions, systemPrompt, userPrompt string) (T, RepairResult, error) {
	op := newExtractOp[T](opt, systemPrompt, userPrompt)
	return RunOp[any, T](ctx, op, nil, opt)
}

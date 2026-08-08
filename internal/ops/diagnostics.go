package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The kernel side of A-011's diagnostic sink: a way for a caller to attach a
// sink to a context so RunOp's repair loop can hand it a failing answer's raw
// body, without RunOp taking a sink as a parameter every operation in this
// package would then have to thread through -- the same reasoning
// callrecord.go already applies to the envelope collector, and the same
// mechanism: attach on the way in, read it back where the failure happens.

type diagnosticSinkKey struct{}

// diagnosticBinding is what travels on the context: the sink plus the policy
// that bounds and redacts what reaches it.
type diagnosticBinding struct {
	sink   types.DiagnosticSink
	policy types.DiagnosticPolicy
}

// WithDiagnosticSink attaches a caller-provided diagnostic sink to ctx, so
// RunOp captures a repair-exhausted answer's raw body to it instead of
// discarding the content entirely. Off by default: a context with no sink
// attached is the ordinary case, and RunOp behaves exactly as it did before
// this existed.
func WithDiagnosticSink(ctx context.Context, sink types.DiagnosticSink, policy types.DiagnosticPolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, diagnosticSinkKey{}, diagnosticBinding{sink: sink, policy: policy})
}

// diagnosticSinkFrom reads back what WithDiagnosticSink attached, or the zero
// binding -- a nil sink -- when nothing was configured.
func diagnosticSinkFrom(ctx context.Context) diagnosticBinding {
	if ctx == nil {
		return diagnosticBinding{}
	}
	if binding, ok := ctx.Value(diagnosticSinkKey{}).(diagnosticBinding); ok {
		return binding
	}
	return diagnosticBinding{}
}

// captureDiagnostic is RunOp's seam into the sink. With no sink configured on
// ctx it is exactly types.CaptureDiagnostic(nil, ...): nothing is captured,
// and the zero DiagnosticRef comes back for the error to carry.
func captureDiagnostic(ctx context.Context, operation string, kind types.ErrorKind, body string) types.DiagnosticRef {
	binding := diagnosticSinkFrom(ctx)
	return types.CaptureDiagnostic(binding.sink, binding.policy, operation, kind, body)
}

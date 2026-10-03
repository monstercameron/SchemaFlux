package ops

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// loggingCase is one call of an operation that writes its first line before it
// has resolved its options, and its last when the model's answer cannot be had.
type loggingCase struct {
	name string
	// starting is the line written first, and failed the line written when the
	// model could not be asked; both are named so that the test reads what an
	// operator reads.
	starting, failed string
	// call makes the call under ctx, which carries the client.
	call func(ctx context.Context)
}

func loggingCases() []loggingCase {
	h2 := func(ctx context.Context) CommonOptions { return CommonOptions{Context: ctx} }
	return []loggingCase{
		{"Arbitrate", "Starting arbitrate operation", "Arbitrate operation LLM call failed", func(ctx context.Context) {
			_, _ = Arbitrate(flatRecords, ArbitrateOptions{Rules: []string{"r"}, Context: ctx})
		}},
		{"Audit", "Starting audit operation", "Audit operation LLM call failed", func(ctx context.Context) {
			_, _ = Audit(flatRecords[0], AuditOptions{Policies: []string{"p"}, Context: ctx})
		}},
		{"Assemble", "Starting compose operation", "Compose operation LLM call failed", func(ctx context.Context) {
			_, _ = Assemble[flatRecord]([]any{flatRecords[0]}, ComposeOptions{Context: ctx})
		}},
		{"Conform", "Starting conform operation", "Conform operation LLM call failed", func(ctx context.Context) {
			_, _ = Conform(flatRecords[0], "ISO8601", ConformOptions{Context: ctx})
		}},
		{"Derive", "Starting derive operation", "Derive operation LLM call failed", func(ctx context.Context) {
			_, _ = Derive[flatRecord, flatRecord](flatRecords[0], DeriveOptions{Context: ctx})
		}},
		{"Interpolate", "Starting interpolate operation", "Interpolate operation LLM call failed", func(ctx context.Context) {
			_, _ = Interpolate(flatRecords, InterpolateOptions{Context: ctx})
		}},
		{"Settle", "Starting negotiate operation", "Negotiate operation LLM call failed", func(ctx context.Context) {
			_, _ = Settle[flatRecord](map[string]any{"a": 1}, NegotiateOptions{Context: ctx})
		}},
		{"SettleAdversarial", "Starting adversarial negotiation", "Adversarial negotiation LLM call failed", func(ctx context.Context) {
			_, _ = SettleAdversarial(AdversarialContext[flatRecord]{}, AdversarialOptions{Context: ctx})
		}},
		{"Pivot", "Starting pivot operation", "Pivot operation LLM call failed", func(ctx context.Context) {
			_, _ = Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{Context: ctx})
		}},
		{"Project", "Starting project operation", "Project operation LLM call failed", func(ctx context.Context) {
			_, _ = Project[flatRecord, flatRecord](flatRecords[0], ProjectOptions{Context: ctx})
		}},
		{"Resolve", "Starting resolve operation", "Resolve operation LLM call failed", func(ctx context.Context) {
			_, _ = Resolve(flatRecords, ResolveOptions{Context: ctx})
		}},
		{"Synthesize", "Starting synthesize operation", "Synthesize operation LLM call failed", func(ctx context.Context) {
			o := NewSynthesizeOptions()
			o.CommonOptions = h2(ctx)
			_, _ = Synthesize[flatRecord]([]any{"a"}, o)
		}},
		{"Predict", "Starting predict operation", "Predict operation LLM call failed", func(ctx context.Context) {
			o := NewPredictOptions()
			o.CommonOptions = h2(ctx)
			_, _ = Predict[flatRecord](flatRecords, o)
		}},
		{"Enrich", "Starting enrich operation", "Enrich operation LLM call failed", func(ctx context.Context) {
			o := NewEnrichOptions()
			o.CommonOptions = h2(ctx)
			_, _ = Enrich[flatRecord, flatRecord](flatRecords[0], o)
		}},
		{"EnrichInPlace", "Starting enrich in-place operation", "EnrichInPlace LLM call failed", func(ctx context.Context) {
			o := NewEnrichOptions()
			o.CommonOptions = h2(ctx)
			_, _ = EnrichInPlace(flatRecords[0], o)
		}},
		{"Decompose", "Starting decompose operation", "Decompose operation LLM call failed", func(ctx context.Context) {
			o := NewDecomposeOptions()
			o.CommonOptions = h2(ctx)
			_, _ = Decompose(flatRecords[0], o)
		}},
		{"DecomposeToSlice", "Starting decompose to slice operation", "DecomposeToSlice LLM call failed", func(ctx context.Context) {
			o := NewDecomposeOptions()
			o.CommonOptions = h2(ctx)
			_, _ = DecomposeToSlice[flatRecord, flatRecord](flatRecords[0], o)
		}},
	}
}

// capturedLogger is a logger that writes every level to a buffer.
func capturedLogger() (*telemetry.Logger, *bytes.Buffer) {
	var written bytes.Buffer
	return telemetry.FromSlog(slog.New(slog.NewTextHandler(&written, &slog.HandlerOptions{Level: slog.LevelDebug}))), &written
}

// asProcessLogger makes l the process logger for one test.
func asProcessLogger(t *testing.T, l *telemetry.Logger) {
	t.Helper()
	previous := logger.GetLogger()
	logger.SetLogger(l)
	t.Cleanup(func() { logger.SetLogger(previous) })
}

// A client's lines go to the client's logger and not to the process's, so two
// clients in one process write to their own. Each operation says what it is
// about to do before it has looked at its options, and says so again when the
// model cannot be asked: both lines have to find the client from the context
// the caller put on its options.
func TestAdvancedOperationsWriteToTheClientsLogger(t *testing.T) {
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", errors.New("provider down")
	})
	for _, tc := range loggingCases() {
		t.Run(tc.name, func(t *testing.T) {
			process, processWritten := capturedLogger()
			asProcessLogger(t, process)
			client, clientWritten := capturedLogger()
			ctx := WithExecConfig(context.Background(), &ExecConfig{Logger: client})

			tc.call(ctx)

			for _, line := range []string{tc.starting, tc.failed} {
				if !strings.Contains(clientWritten.String(), line) {
					t.Errorf("the client's logger did not get %q:\n%s", line, clientWritten)
				}
				if strings.Contains(processWritten.String(), line) {
					t.Errorf("the process logger got %q, which belongs to the client:\n%s", line, processWritten)
				}
			}
		})
	}
}

// A client that discards its lines is not heard from, whatever the process
// logger would have done with them.
func TestAdvancedOperationsHonourAClientThatDiscards(t *testing.T) {
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", errors.New("provider down")
	})
	for _, tc := range loggingCases() {
		t.Run(tc.name, func(t *testing.T) {
			process, processWritten := capturedLogger()
			asProcessLogger(t, process)
			ctx := WithExecConfig(context.Background(), &ExecConfig{Logger: telemetry.Discard()})

			tc.call(ctx)

			for _, line := range []string{tc.starting, tc.failed} {
				if strings.Contains(processWritten.String(), line) {
					t.Errorf("the process logger got %q from a client that discards its lines", line)
				}
			}
		})
	}
}

// With no client on the context every line goes where it always went: to the
// process logger, at the level it had.
func TestAdvancedOperationsWithNoClientWriteToTheProcessLogger(t *testing.T) {
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", errors.New("provider down")
	})
	for _, tc := range loggingCases() {
		t.Run(tc.name, func(t *testing.T) {
			process, processWritten := capturedLogger()
			asProcessLogger(t, process)

			tc.call(context.Background())

			for _, line := range []string{"level=DEBUG msg=\"" + tc.starting, "level=ERROR msg=\"" + tc.failed} {
				if !strings.Contains(processWritten.String(), line) {
					t.Errorf("the process logger did not get %q:\n%s", line, processWritten)
				}
			}
		})
	}
}

// A call refused before it asks a model has still said that it started, and says
// so to the client's logger: the line is written before the options are read,
// from the context they carry.
func TestARefusedCallStillLogsItsStartToTheClient(t *testing.T) {
	process, processWritten := capturedLogger()
	asProcessLogger(t, process)
	client, clientWritten := capturedLogger()
	ctx := WithExecConfig(context.Background(), &ExecConfig{Logger: client})

	_, _ = Arbitrate([]string{}, ArbitrateOptions{Rules: []string{"r"}, Context: ctx})
	_, _ = Conform(flatRecords[0], "", ConformOptions{Context: ctx})
	_, _ = Interpolate([]flatRecord{}, InterpolateOptions{Context: ctx})
	_, _ = Resolve([]flatRecord{}, ResolveOptions{Context: ctx})
	_, _ = Synthesize[flatRecord](nil, func() SynthesizeOptions {
		o := NewSynthesizeOptions()
		o.CommonOptions = CommonOptions{Context: ctx}
		return o
	}())

	for _, line := range []string{
		"Starting arbitrate operation", "Starting conform operation", "Starting interpolate operation",
		"Starting resolve operation", "Starting synthesize operation",
	} {
		if !strings.Contains(clientWritten.String(), line) {
			t.Errorf("the client's logger did not get %q", line)
		}
		if strings.Contains(processWritten.String(), line) {
			t.Errorf("the process logger got %q", line)
		}
	}
}

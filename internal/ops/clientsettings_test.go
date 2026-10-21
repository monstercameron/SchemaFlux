package ops

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// snapshotCtx puts a snapshot on a fresh context.
func snapshotCtx(cfg ExecConfig) context.Context {
	return WithExecConfig(context.Background(), &cfg)
}

func TestExecTimeoutIsTheSnapshotsOrZero(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want time.Duration
	}{
		{"a nil context names none", nil, 0},
		{"a bare context names none", context.Background(), 0},
		{"a snapshot that names no timeout names none", snapshotCtx(ExecConfig{Model: "m"}), 0},
		{"a timeout of one nanosecond", snapshotCtx(ExecConfig{Timeout: 1}), 1},
		{"a timeout of 40 milliseconds", snapshotCtx(ExecConfig{Timeout: 40 * time.Millisecond}), 40 * time.Millisecond},
		{"a timeout of 30 seconds", snapshotCtx(ExecConfig{Timeout: 30 * time.Second}), 30 * time.Second},
		{"a timeout of an hour", snapshotCtx(ExecConfig{Timeout: time.Hour}), time.Hour},
		{"the longest timeout there is", snapshotCtx(ExecConfig{Timeout: time.Duration(1<<63 - 1)}), time.Duration(1<<63 - 1)},
		{"a negative timeout names none", snapshotCtx(ExecConfig{Timeout: -time.Second}), 0},
		{"a timeout of minus one names none", snapshotCtx(ExecConfig{Timeout: -1}), 0},
		{"a derived context reads its parent's snapshot", func() context.Context {
			derived, cancel := context.WithCancel(snapshotCtx(ExecConfig{Timeout: time.Minute}))
			t.Cleanup(cancel)
			return derived
		}(), time.Minute},
		{"the nearer snapshot is the one read", WithExecConfig(snapshotCtx(ExecConfig{Timeout: time.Minute}), &ExecConfig{Timeout: time.Second}), time.Second},
		{"a nearer snapshot with none hides the outer one", WithExecConfig(snapshotCtx(ExecConfig{Timeout: time.Minute}), &ExecConfig{}), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := execTimeout(tc.ctx); got != tc.want {
				t.Errorf("execTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecRetriesIsTheSnapshotsOrZero(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"a nil context names none", nil, 0},
		{"a bare context names none", context.Background(), 0},
		{"a snapshot that names none", snapshotCtx(ExecConfig{Effort: types.EffortHigh}), 0},
		{"one retry", snapshotCtx(ExecConfig{Retries: 1}), 1},
		{"two retries", snapshotCtx(ExecConfig{Retries: 2}), 2},
		{"three retries", snapshotCtx(ExecConfig{Retries: 3}), 3},
		{"a hundred retries", snapshotCtx(ExecConfig{Retries: 100}), 100},
		{"never is minus one", snapshotCtx(ExecConfig{Retries: -1}), -1},
		{"never is also any other negative number", snapshotCtx(ExecConfig{Retries: -7}), -7},
		{"a derived context reads its parent's snapshot", func() context.Context {
			derived, cancel := context.WithCancel(snapshotCtx(ExecConfig{Retries: 4}))
			t.Cleanup(cancel)
			return derived
		}(), 4},
		{"the nearer snapshot is the one read", WithExecConfig(snapshotCtx(ExecConfig{Retries: 4}), &ExecConfig{Retries: 1}), 1},
		{"a nearer snapshot with none hides the outer one", WithExecConfig(snapshotCtx(ExecConfig{Retries: 4}), &ExecConfig{}), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := execRetries(tc.ctx); got != tc.want {
				t.Errorf("execRetries = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExecOmitReinforcementIsTheSnapshotsOrFalse(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"a nil context keeps the block", nil, false},
		{"a bare context keeps the block", context.Background(), false},
		{"a snapshot that says nothing keeps the block", snapshotCtx(ExecConfig{}), false},
		{"a snapshot that sets other fields keeps the block", snapshotCtx(ExecConfig{Timeout: time.Second, Retries: 2, Repairs: 1, Model: "m"}), false},
		{"a snapshot that omits it", snapshotCtx(ExecConfig{OmitReinforcement: true}), true},
		{"a snapshot that omits it among other fields", snapshotCtx(ExecConfig{OmitReinforcement: true, Model: "m", Retries: 1}), true},
		{"a snapshot that says false keeps the block", snapshotCtx(ExecConfig{OmitReinforcement: false}), false},
		{"a derived context reads its parent's snapshot", func() context.Context {
			derived, cancel := context.WithCancel(snapshotCtx(ExecConfig{OmitReinforcement: true}))
			t.Cleanup(cancel)
			return derived
		}(), true},
		{"the nearer snapshot is the one read", WithExecConfig(snapshotCtx(ExecConfig{OmitReinforcement: true}), &ExecConfig{}), false},
		{"a nearer snapshot that omits it overrides one that does not", WithExecConfig(snapshotCtx(ExecConfig{}), &ExecConfig{OmitReinforcement: true}), true},
		{"a context carrying only other values keeps the block", context.WithValue(context.Background(), struct{}{}, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := execOmitReinforcement(tc.ctx); got != tc.want {
				t.Errorf("execOmitReinforcement = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoggerForIsTheSnapshotsOrTheProcessLogger(t *testing.T) {
	process := logger.GetLogger()
	named := telemetry.FromSlog(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	discard := telemetry.Discard()

	cases := []struct {
		name string
		ctx  context.Context
		want *logger.Logger
	}{
		{"a nil context gets the process logger", nil, process},
		{"a bare context gets the process logger", context.Background(), process},
		{"a snapshot with no logger gets the process logger", snapshotCtx(ExecConfig{Model: "m"}), process},
		{"a snapshot with a logger gets it", snapshotCtx(ExecConfig{Logger: named}), named},
		{"a snapshot with the discarding logger gets that and not the process one", snapshotCtx(ExecConfig{Logger: discard}), discard},
		{"a derived context reads its parent's snapshot", func() context.Context {
			derived, cancel := context.WithCancel(snapshotCtx(ExecConfig{Logger: named}))
			t.Cleanup(cancel)
			return derived
		}(), named},
		{"the nearer snapshot is the one read", WithExecConfig(snapshotCtx(ExecConfig{Logger: named}), &ExecConfig{Logger: discard}), discard},
		{"a nearer snapshot with no logger falls to the process one, not the outer client's", WithExecConfig(snapshotCtx(ExecConfig{Logger: named}), &ExecConfig{}), process},
		{"a context carrying only other values gets the process logger", context.WithValue(context.Background(), struct{}{}, 1), process},
		{"a snapshot with every other setting and no logger gets the process logger", snapshotCtx(ExecConfig{Timeout: time.Second, Retries: 1, Repairs: 1, OmitReinforcement: true, Err: errors.New("x")}), process},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loggerFor(tc.ctx); got != tc.want {
				t.Errorf("loggerFor returned %p, want %p", got, tc.want)
			}
		})
	}

	t.Run("a line written through the snapshot's logger reaches only that logger", func(t *testing.T) {
		var written bytes.Buffer
		own := telemetry.FromSlog(slog.New(slog.NewTextHandler(&written, nil)))
		before := len(process.Entries())
		loggerFor(snapshotCtx(ExecConfig{Logger: own})).Warn("a line for the client")
		if written.Len() == 0 {
			t.Errorf("the client's logger received nothing")
		}
		if after := len(process.Entries()); after != before {
			t.Errorf("the process logger received %d line(s)", after-before)
		}
	})
	t.Run("it allocates nothing", func(t *testing.T) {
		with := snapshotCtx(ExecConfig{Logger: named})
		without := context.Background()
		if allocs := testing.AllocsPerRun(100, func() {
			_ = loggerFor(with)
			_ = loggerFor(without)
		}); allocs != 0 {
			t.Errorf("loggerFor allocated %v times per run", allocs)
		}
	})
}

func TestNewExecConfigFieldsAreZeroForAnUnconfiguredClient(t *testing.T) {
	ctx := WithExecConfig(context.Background(), &ExecConfig{})
	cfg := execConfigFrom(ctx)
	if cfg == nil {
		t.Fatal("no snapshot")
	}
	if cfg.Timeout != 0 || cfg.Retries != 0 || cfg.Repairs != 0 || cfg.OmitReinforcement || cfg.Logger != nil || cfg.Sink != nil || cfg.Err != nil {
		t.Errorf("the zero snapshot is not zero in the new fields: %+v", cfg)
	}
}

func TestSnapshotCopyKeepsTheNewFields(t *testing.T) {
	sink := &namedSink{name: "s"}
	boom := errors.New("no client")
	own := telemetry.Discard()
	original := &ExecConfig{
		Timeout: 3 * time.Second, Retries: 2, Repairs: 1, OmitReinforcement: true,
		Logger: own, Sink: sink, Err: boom,
	}
	cfg := execConfigFrom(WithExecConfig(context.Background(), original))
	if cfg == original {
		t.Fatal("WithExecConfig stored the caller's pointer, not a copy")
	}
	if cfg.Timeout != 3*time.Second || cfg.Retries != 2 || cfg.Repairs != 1 || !cfg.OmitReinforcement ||
		cfg.Logger != own || cfg.Sink != Sink(sink) || !errors.Is(cfg.Err, boom) {
		t.Errorf("the copy lost a field: %+v", cfg)
	}

	// Changing the caller's value afterwards does not reach the copy.
	original.Timeout = time.Hour
	original.Retries = 9
	if cfg.Timeout != 3*time.Second || cfg.Retries != 2 {
		t.Errorf("the copy followed the original")
	}
}

func TestZeroNewFieldsLeaveEveryExistingReaderAsItWas(t *testing.T) {
	base := &ExecConfig{Model: "m", Effort: types.EffortHigh, DataPolicyDeclared: true}
	ctx := WithExecConfig(context.Background(), base)
	if ExecModel(ctx) != "m" || ExecEffort(ctx) != types.EffortHigh {
		t.Errorf("the readers changed: model %q effort %v", ExecModel(ctx), ExecEffort(ctx))
	}
	if _, declared := ExecDataPolicy(ctx); !declared {
		t.Errorf("the data policy was lost")
	}
	if ExecProvider(ctx) != nil || ExecBudget(ctx) != nil || ExecScheduler(ctx) != nil || ExecDecisionObserver(ctx) != nil {
		t.Errorf("a reader returned something the snapshot never set")
	}
}

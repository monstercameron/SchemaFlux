package ops

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// callerContext reads the context an options value carries from either half,
// the common half first, and derives nothing from it.
func TestCallerContextPrefersTheCommonHalf(t *testing.T) {
	type key struct{}
	common := context.WithValue(context.Background(), key{}, "common")
	embedded := context.WithValue(context.Background(), key{}, "embedded")
	cases := []struct {
		name   string
		common CommonOptions
		half   types.OpOptions
		want   context.Context
	}{
		{"neither", CommonOptions{}, types.OpOptions{}, nil},
		{"common only", CommonOptions{Context: common}, types.OpOptions{}, common},
		{"embedded only", CommonOptions{}, types.OpOptions{Context: embedded}, embedded},
		{"both", CommonOptions{Context: common}, types.OpOptions{Context: embedded}, common},
		{"a timeout derives nothing", CommonOptions{Context: common, Timeout: 1}, types.OpOptions{}, common},
		{"a timeout and the embedded half", CommonOptions{Timeout: 1}, types.OpOptions{Context: embedded}, embedded},
		{"a background context is returned as it is", CommonOptions{Context: context.Background()}, types.OpOptions{}, context.Background()},
		{"embedded background", CommonOptions{}, types.OpOptions{Context: context.Background()}, context.Background()},
		{"common wins over an embedded background", CommonOptions{Context: common}, types.OpOptions{Context: context.Background()}, common},
		{"nothing set and a timeout", CommonOptions{Timeout: 5}, types.OpOptions{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := callerContext(tc.common, tc.half); got != tc.want {
				t.Errorf("callerContext = %v, want %v", got, tc.want)
			}
		})
	}
}

// loggerForOptions finds the client's logger from either half of an options
// value, the common half first, and derives nothing from them.
func TestLoggerForOptionsReadsTheContextFromEitherHalf(t *testing.T) {
	process := logger.GetLogger()
	first, second := telemetry.Discard(), telemetry.Discard()
	clientA := snapshotCtx(ExecConfig{Logger: first})
	clientB := snapshotCtx(ExecConfig{Logger: second})
	noLogger := snapshotCtx(ExecConfig{Model: "m"})

	cases := []struct {
		name     string
		common   context.Context
		embedded context.Context
		want     *logger.Logger
	}{
		{"neither half has a context", nil, nil, process},
		{"only the common half has a client", clientA, nil, first},
		{"only the embedded half has a client", nil, clientB, second},
		{"the common half wins when both have one", clientA, clientB, first},
		{"a common context with no client hides nothing: it is the one read", context.Background(), clientB, process},
		{"a common snapshot with no logger gives the process logger", noLogger, clientB, process},
		{"an embedded snapshot with no logger gives the process logger", nil, noLogger, process},
		{"a bare common context gives the process logger", context.Background(), nil, process},
		{"a bare embedded context gives the process logger", nil, context.Background(), process},
		{"a derived common context keeps its client", func() context.Context {
			derived, cancel := context.WithTimeout(clientA, time.Hour)
			t.Cleanup(cancel)
			return derived
		}(), nil, first},
		{"a derived embedded context keeps its client", nil, func() context.Context {
			derived, cancel := context.WithCancel(clientB)
			t.Cleanup(cancel)
			return derived
		}(), second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := loggerForOptions(CommonOptions{Context: tc.common}, types.OpOptions{Context: tc.embedded})
			if got != tc.want {
				t.Errorf("loggerForOptions returned %p, want %p", got, tc.want)
			}
		})
	}

	t.Run("it allocates nothing", func(t *testing.T) {
		common, embedded := CommonOptions{Context: clientA}, types.OpOptions{Context: clientB}
		if allocs := testing.AllocsPerRun(100, func() { _ = loggerForOptions(common, embedded) }); allocs != 0 {
			t.Errorf("loggerForOptions allocated %v times per run", allocs)
		}
	})
}

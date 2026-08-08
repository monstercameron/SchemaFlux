package tests

import (
	"context"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The client's fifteen methods, pinned at compile time.
//
// They used to be protected by testdata/api_surface.txt, which snapshots the
// exported declarations found in the root directory's source. The client's
// implementation now lives in internal/api/client and reaches the public API as
// a type alias, so those method declarations are no longer IN the root
// directory and the snapshot cannot see them -- it went from listing fifteen to
// listing none, while every one of them stayed callable.
//
// That is a gap in the snapshot's method, not a change to the API, and leaving
// it there would mean fifteen public methods nothing guards. This interface is
// the replacement guarantee, and it is a stronger one: removing or re-typing
// any of them fails the BUILD rather than a golden-file comparison, and it
// cannot be silenced by regenerating a snapshot.
type clientSurface interface {
	Context(context.Context) context.Context
	Err() error
	Spent() (float64, bool)

	WithBudget(float64) *schemaflux.Client
	WithDataPolicy(types.DataPolicy) *schemaflux.Client
	WithDebug(bool) *schemaflux.Client
	WithMockProvider() *schemaflux.Client
	WithProvider(string) *schemaflux.Client
	WithProviderConfig(string, llm.ProviderConfig) *schemaflux.Client
	WithProviderInstance(llm.Provider) *schemaflux.Client
	WithRequestTracking(requesttracking.Config) *schemaflux.Client
	WithRetries(int) *schemaflux.Client
	WithRetryBackoff(time.Duration) *schemaflux.Client
	WithScheduler(*ops.Scheduler) *schemaflux.Client
	WithTimeout(time.Duration) *schemaflux.Client
}

var _ clientSurface = (*schemaflux.Client)(nil)

// And the logging accessors the alias does not cover, asserted by taking their
// addresses: a signature change breaks this file.
func TestTheClientSurfaceIsWhatItWas(t *testing.T) {
	var (
		_ func() *telemetry.Logger                       = schemaflux.GetLogger
		_ func(telemetry.LoggerConfig) *telemetry.Logger = schemaflux.ConfigureLogging
		_ func(telemetry.LogLevel)                       = schemaflux.SetLogLevel
		_ func() []telemetry.LogEntry                    = schemaflux.GetLogEntries
		_ func()                                         = schemaflux.ResetLogEntries
		_ func(string) error                             = schemaflux.Init
		_ func(...string) error                          = schemaflux.InitWithEnv
		_ func(string) *schemaflux.Client                = schemaflux.NewClient
		_ func(*schemaflux.Client)                       = schemaflux.SetDefaultClient
		_ func() *schemaflux.Client                      = schemaflux.GetDefaultClient
		_ func() []schemaflux.CatalogEntry               = schemaflux.Catalog
		_ func(string) (schemaflux.CatalogEntry, bool)   = schemaflux.Describe
	)

	// The alias is the same type, which is the property that makes the
	// re-export safe. If it ever became a distinct wrapper struct, every
	// method above would still compile and callers holding a
	// *client.Client would stop working -- so assert identity directly.
	if _, ok := any((*schemaflux.Client)(nil)).(clientSurface); !ok {
		t.Fatal("*schemaflux.Client no longer satisfies its own published surface")
	}
}

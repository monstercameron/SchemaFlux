package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
)

// ExecLogger is the logger of the client the call that ctx belongs to was made
// with, or the process logger when the context carries no client or the client
// named none. A package that writes a line from inside a call, such as a
// middleware that recovers a panic in code of the caller's, writes it here so
// that two clients in one process write to their own loggers.
func ExecLogger(ctx context.Context) *logger.Logger { return loggerFor(ctx) }

package ops

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
)

// The readers of the client's settings on its snapshot. Each returns the
// setting's zero value when the context carries no snapshot or the snapshot
// does not name it, and the zero of each is what a call did before the setting
// existed, so a caller that sets none of them sees no change.

// execTimeout is the deadline the context's snapshot names for a call whose
// context has none, or 0. A negative Timeout reads as 0: only a positive one
// is a deadline, and the default applies to anything else.
func execTimeout(ctx context.Context) time.Duration {
	if cfg := execConfigFrom(ctx); cfg != nil && cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return 0
}

// execRetries is the snapshot's Retries: positive is how many times a failed
// request is sent again, negative is never, and 0 leaves the provider's own
// policy in charge.
func execRetries(ctx context.Context) int {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.Retries
	}
	return 0
}

// execOmitReinforcement reports whether the snapshot leaves the reinforcement
// block off the system prompt.
func execOmitReinforcement(ctx context.Context) bool {
	if cfg := execConfigFrom(ctx); cfg != nil {
		return cfg.OmitReinforcement
	}
	return false
}

// loggerFor is the logger of the client ctx carries, or the process logger when
// the context carries no snapshot or its snapshot names none. A line written
// where a call's context is in reach goes through it, so two clients in one
// process write to their own loggers.
func loggerFor(ctx context.Context) *logger.Logger {
	if cfg := execConfigFrom(ctx); cfg != nil && cfg.Logger != nil {
		return cfg.Logger
	}
	return logger.GetLogger()
}

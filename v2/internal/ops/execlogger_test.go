package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
)

func TestExecLoggerIsTheLoggerOfTheClientOfTheCall(t *testing.T) {
	own := logger.GetLogger()
	if got := ExecLogger(context.Background()); got != own {
		t.Errorf("a context with no client got %p, want the process logger %p", got, own)
	}
	if got := ExecLogger(WithExecConfig(context.Background(), &ExecConfig{})); got != own {
		t.Errorf("a client that names no logger got %p, want the process logger %p", got, own)
	}
	clients := logger.ConfigureLogger(logger.DefaultLoggerConfig())
	t.Cleanup(func() { logger.SetLogger(own) })
	logger.SetLogger(own)
	ctx := WithExecConfig(context.Background(), &ExecConfig{Logger: clients})
	if got := ExecLogger(ctx); got != clients || got == own {
		t.Errorf("a client with a logger of its own got %p, want %p", got, clients)
	}
	if got := ExecLogger(WithExecConfig(ctx, &ExecConfig{})); got != own {
		t.Errorf("a context whose latest client names no logger got %p, want the process logger", got)
	}
}

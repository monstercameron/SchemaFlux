//go:build !js && !wasip1

package schemaflux

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
)

func TestAZeroClientWithNoKeyLogsNothingOnAnyPath(t *testing.T) {
	// Not parallel: it replaces the process logger and puts it back.
	var lines bytes.Buffer
	previous := logger.GetLogger()
	logger.SetLogger(telemetry.FromSlog(slog.New(slog.NewTextHandler(&lines, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { logger.SetLogger(previous) })
	server := newLoopback(t)
	useEnvironment(t, map[string]string{"OPENAI_BASE_URL": server.URL})

	// The default is the zero client, put in again so that the logger that the
	// test installed is the one SetDefault just replaced: what is checked is
	// that the lines of a client with no Logger go nowhere.
	useDefault(t, Client{})
	lines.Reset()
	logger.SetLogger(telemetry.FromSlog(slog.New(slog.NewTextHandler(&lines, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	for name, ctx := range map[string]context.Context{
		"on a client of the context": WithClient(context.Background(), Client{}),
		"on the default":             context.Background(),
	} {
		if _, err := callIs2(ctx); err == nil {
			t.Errorf("%s: a call with no key succeeded", name)
		}
	}
	if err := (Client{}).Warm(context.Background()); err == nil {
		t.Error("Warm with no key succeeded")
	}
	if lines.Len() != 0 {
		t.Errorf("the library wrote %q for calls that could not be made", lines.String())
	}
	if server.requests() != 0 {
		t.Errorf("%d requests were sent", server.requests())
	}
}

func TestTheSchedulerOfTheDefaultBoundsAPlainCall(t *testing.T) {
	// The default is the process's, so the test is not parallel. Each call holds
	// its slot for a wait on the bubble's clock, which is long enough that calls
	// that were not bounded would overlap, and costs no time.
	synctest.Test(t, func(t *testing.T) {
		scheduler := &Scheduler{MaxConcurrent: 1}
		var mu sync.Mutex
		inFlight, peak := 0, 0
		provider := &answerer{name: "custom", answer: func(int, Request) (Response, error) {
			mu.Lock()
			inFlight++
			peak = max(peak, inFlight)
			mu.Unlock()
			time.Sleep(time.Second)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return Response{Content: `{"q1":true}`}, nil
		}}
		useDefault(t, Client{Provider: provider, Model: "m", Scheduler: scheduler})
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if _, err := callIs2(context.Background()); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if peak != 1 {
			t.Errorf("%d requests were with the provider at once, want 1: a call with no client ran without the default's scheduler", peak)
		}
		if provider.count() != 8 {
			t.Errorf("the provider received %d requests, want 8", provider.count())
		}
		_ = scheduler.Close(context.Background())
	})
}

func TestTheRootImportsNoSubpackageOfV2AndNoOldPublicPackageButTheTwoItWraps(t *testing.T) {
	if testing.Short() {
		t.Skip("lists the dependencies with the go command")
	}
	out, err := exec.Command("go", "list", "-deps", "./v2/").Output()
	if err != nil {
		// The test runs in the package's directory.
		out, err = exec.Command("go", "list", "-deps", ".").Output()
		if err != nil {
			t.Skipf("the go command is not available here: %v", err)
		}
	}
	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("the list of dependencies looks wrong: %v", deps)
	}
	have := map[string]bool{}
	for _, dep := range deps {
		have[dep] = true
		if strings.HasPrefix(dep, modulePath+"/v2/") {
			t.Errorf("the root depends on %s, a subpackage of itself: the environment's provider must be built through internal/llm and internal/config", dep)
		}
	}
	for _, want := range []string{modulePath + "/internal/config", modulePath + "/internal/llm", modulePath + "/internal/bind", modulePath + "/internal/ops"} {
		if !have[want] {
			t.Errorf("the list has no %s, so the check is not looking at the package", want)
		}
	}
}

package telemetry_test

// SEC-002 integration coverage: drives exported Extract and Complete calls
// through schemafluxtest's in-process provider with a marker in their input.
// The v2 client's debug slog output must contain at least one line and none of
// those lines may carry the marker.
//
// This is a separate (_test) package because schemaflux and schemafluxtest
// import internal/telemetry transitively.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type ticketMarker struct {
	Body string `json:"body"`
}

type extractedMarker struct {
	Summary string `json:"summary"`
}

// TestRealOperationNeverLogsMarker drives real calls with a marker in input.
func TestRealOperationNeverLogsMarker(t *testing.T) {
	const inputMarker = "REAL-OP-MARKER-b91a7d3c-do-not-log"

	var captured bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&captured, &slog.HandlerOptions{Level: slog.LevelDebug}))

	provider := &schemafluxtest.Provider{}
	provider.Reply(`{"summary":"a summary that does not echo the input"}`)
	schemafluxtest.Install(t, provider)

	input := ticketMarker{Body: "please handle this: " + inputMarker}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: provider, Logger: log})
	result, err := schemaflux.Extract[extractedMarker](ctx, input)
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if strings.Contains(result.Summary, inputMarker) {
		t.Fatalf("test setup issue: the scripted reply should not itself contain the marker")
	}
	provider.Reply(" finished")
	if _, err := schemaflux.Complete(ctx, "please finish: "+inputMarker); err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	if captured.Len() == 0 {
		t.Fatal("expected at least one client log entry from the real calls")
	}
	if strings.Contains(captured.String(), inputMarker) {
		t.Fatalf("client log leaked the input marker: %s", captured.String())
	}
}

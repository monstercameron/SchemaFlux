package telemetry

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// MW-007. The library used to configure the host's OpenTelemetry stack --
// exporters, endpoint, sampler, and otel.SetTracerProvider. A library that
// does that cannot be embedded twice. These cover the seam that replaces it:
// the core emits through an interface it owns, and nothing here knows
// OpenTelemetry exists.

type recordingObserver struct {
	mu       sync.Mutex
	started  []OperationEvent
	finished []OperationResult
	stamp    string
}

type stampKey struct{}

func (o *recordingObserver) OperationStarted(ctx context.Context, event OperationEvent) context.Context {
	o.mu.Lock()
	o.started = append(o.started, event)
	o.mu.Unlock()

	if o.stamp != "" {
		return context.WithValue(ctx, stampKey{}, o.stamp)
	}
	return ctx
}

func (o *recordingObserver) OperationFinished(_ context.Context, result OperationResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, result)
}

func (o *recordingObserver) counts() (int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.started), len(o.finished)
}

// The default has to be a working no-op, not nil, so every emission site is
// spared a nil check and a library with no observer costs nothing.
func TestTheDefaultObserverIsANoOpNotNil(t *testing.T) {
	if CurrentObserver() == nil {
		t.Fatal("CurrentObserver returned nil; every emission site would have to check")
	}

	ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})
	if ctx == nil {
		t.Error("the no-op observer returned a nil context")
	}
	ObserveOperationEnd(ctx, OperationResult{Operation: "extract"})
}

func TestAnInstalledObserverSeesBothEnds(t *testing.T) {
	observer := &recordingObserver{}
	defer SetObserver(observer)()

	ctx := ObserveOperationStart(context.Background(), OperationEvent{
		Operation:    "extract",
		Provider:     "openai",
		Model:        "gpt-5.6-luna",
		RequestID:    "req-1",
		Mode:         "strict",
		Intelligence: "smart",
	})
	ObserveOperationEnd(ctx, OperationResult{
		Operation: "extract",
		Duration:  25 * time.Millisecond,
		Attempts:  2,
		Usage:     types.TokenUsage{PromptTokens: 100, TotalTokens: 130},
	})

	started, finished := observer.counts()
	if started != 1 || finished != 1 {
		t.Fatalf("started=%d finished=%d, want 1 and 1", started, finished)
	}
	if observer.started[0].Model != "gpt-5.6-luna" {
		t.Errorf("Model = %q", observer.started[0].Model)
	}
	if observer.finished[0].Attempts != 2 {
		t.Errorf("Attempts = %d", observer.finished[0].Attempts)
	}
}

// An observer that opens a span needs the rest of the operation to run inside
// it, so the context it returns has to be the one that is used.
func TestTheContextAnObserverReturnsIsTheOneUsed(t *testing.T) {
	observer := &recordingObserver{stamp: "from-the-observer"}
	defer SetObserver(observer)()

	ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})

	if got, _ := ctx.Value(stampKey{}).(string); got != "from-the-observer" {
		t.Errorf("the observer's context was discarded (value = %q)", got)
	}
}

func TestSetObserverRestoresThePrevious(t *testing.T) {
	first := &recordingObserver{}
	restoreFirst := SetObserver(first)

	second := &recordingObserver{}
	restoreSecond := SetObserver(second)

	ObserveOperationEnd(context.Background(), OperationResult{Operation: "a"})
	if _, finished := second.counts(); finished != 1 {
		t.Fatal("the second observer did not receive the event")
	}

	restoreSecond()
	ObserveOperationEnd(context.Background(), OperationResult{Operation: "b"})
	if _, finished := first.counts(); finished != 1 {
		t.Error("restoring did not put the first observer back")
	}

	restoreFirst()
	if CurrentObserver() == nil {
		t.Error("restoring to the original left no observer")
	}
}

// nil restores the no-op rather than being stored and panicking later at an
// emission site, which would turn a caller's mistake into a crash somewhere
// else entirely.
func TestANilObserverRestoresTheNoOp(t *testing.T) {
	restore := SetObserver(nil)
	defer restore()

	if CurrentObserver() == nil {
		t.Fatal("CurrentObserver is nil after SetObserver(nil)")
	}
	ObserveOperationEnd(ObserveOperationStart(context.Background(), OperationEvent{}), OperationResult{})
}

func TestObservationIsSafeUnderConcurrency(t *testing.T) {
	observer := &recordingObserver{}
	defer SetObserver(observer)()

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})
				ObserveOperationEnd(ctx, OperationResult{Operation: "extract"})
			}
		}()
	}
	wg.Wait()

	started, finished := observer.counts()
	if started != 200 || finished != 200 {
		t.Errorf("started=%d finished=%d, want 200 each", started, finished)
	}
}

// The events carry names, identifiers, and settings. They must not grow a
// field for the caller's input: an observer's output is a span or a log line,
// which is the least controlled place this library could put somebody's data.
func TestTheEventTypesCarryNoPayloadField(t *testing.T) {
	forbidden := map[string]struct{}{
		"Input": {}, "Payload": {}, "Body": {}, "Data": {},
		"Prompt": {}, "SystemPrompt": {}, "UserPrompt": {}, "Response": {}, "Content": {},
	}

	for _, field := range fieldNamesOf(OperationEvent{}) {
		if _, bad := forbidden[field]; bad {
			t.Errorf("OperationEvent.%s carries the caller's payload into an exported span", field)
		}
	}
	for _, field := range fieldNamesOf(OperationResult{}) {
		if _, bad := forbidden[field]; bad {
			t.Errorf("OperationResult.%s carries the caller's payload into an exported span", field)
		}
	}
}

// The result reports the error *kind*, not the message, because a provider's
// message can quote the caller's input back.
func TestTheResultReportsAKindNotAMessage(t *testing.T) {
	for _, field := range fieldNamesOf(OperationResult{}) {
		if field == "Error" || field == "ErrorMessage" || field == "Err" {
			t.Errorf("OperationResult.%s would export a provider's message; the kind is the safe form", field)
		}
	}
}

// fieldNamesOf lists a struct's field names, so the payload checks above walk
// the type rather than a list somebody has to remember to update.
func fieldNamesOf(value any) []string {
	t := reflect.TypeOf(value)
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names = append(names, t.Field(i).Name)
	}
	return names
}

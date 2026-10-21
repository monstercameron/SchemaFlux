package telemetry_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// Nothing of the caller's, the model's or the provider's reaches an observer.
// Five markers are planted, one in each place such text comes from, a real
// operation of the library is run through a scripted provider, and everything
// the observer was given is searched for them.
const (
	markInput    = "MARK_INPUT_ONE"    // in the text the caller passes in
	markSteering = "MARK_STEER_TWO"    // in the caller's steering
	markAnswer   = "MARK_ANSWER_THREE" // in what the model answers
	markErrBody  = "MARK_ERRBODY_FOUR" // in the body of a provider's error
	markAPIKey   = "MARK_APIKEY_FIVE"  // the API key itself
)

var markers = []string{markInput, markSteering, markAnswer, markErrBody, markAPIKey}

// transcript is an Observer and a NoteObserver that writes down every field of
// everything it is given, so that a search of the text searches every field.
type transcript struct {
	mu    sync.Mutex
	lines []string
	scope map[telemetry.Scope]int
	notes int
}

func (tr *transcript) Started(ctx context.Context, e telemetry.Event) context.Context {
	tr.write("started %+v", e)
	tr.mu.Lock()
	tr.scope[e.Scope]++
	tr.mu.Unlock()
	return ctx
}

func (tr *transcript) Finished(_ context.Context, r telemetry.Result) { tr.write("finished %+v", r) }

func (tr *transcript) Noted(_ context.Context, n telemetry.Note) {
	tr.write("noted %+v", n)
	tr.mu.Lock()
	tr.notes++
	tr.mu.Unlock()
}

func (tr *transcript) write(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.lines = append(tr.lines, line)
}

// leaks lists each marker found in the transcript and the line it is in.
func (tr *transcript) leaks() []string {
	var found []string
	for _, line := range tr.lines {
		for _, marker := range markers {
			if strings.Contains(line, marker) {
				found = append(found, marker+" in "+line)
			}
		}
	}
	return found
}

// The search has to be able to find a marker, or a clean result means nothing.
func TestTheLeakSearchFindsAMarkerInAnyField(t *testing.T) {
	ctx := context.Background()
	plants := map[string]func(tr *transcript, marker string){
		"an event's operation":  func(tr *transcript, m string) { tr.Started(ctx, telemetry.Event{Operation: m}) },
		"an event's provider":   func(tr *transcript, m string) { tr.Started(ctx, telemetry.Event{Provider: m}) },
		"an event's model":      func(tr *transcript, m string) { tr.Started(ctx, telemetry.Event{Model: m}) },
		"an event's schema":     func(tr *transcript, m string) { tr.Started(ctx, telemetry.Event{SchemaName: m}) },
		"an event's request id": func(tr *transcript, m string) { tr.Started(ctx, telemetry.Event{RequestID: m}) },
		"an event's format":     func(tr *transcript, m string) { tr.Started(ctx, telemetry.Event{ResponseFormat: m}) },
		"a result's operation":  func(tr *transcript, m string) { tr.Finished(ctx, telemetry.Result{Operation: m}) },
		"a result's finish":     func(tr *transcript, m string) { tr.Finished(ctx, telemetry.Result{FinishReason: m}) },
		"a result's provider":   func(tr *transcript, m string) { tr.Finished(ctx, telemetry.Result{Provider: m}) },
		"a result's model":      func(tr *transcript, m string) { tr.Finished(ctx, telemetry.Result{Model: m}) },
		"a note's name":         func(tr *transcript, m string) { tr.Noted(ctx, telemetry.Note{Name: m}) },
	}
	for name, plant := range plants {
		for _, marker := range markers {
			tr := &transcript{scope: map[telemetry.Scope]int{}}
			plant(tr, marker)
			if found := tr.leaks(); len(found) != 1 {
				t.Errorf("%s: %s was found %d times, want once", name, marker, len(found))
			}
		}
	}
}

type invoice struct {
	Number string `json:"number"`
	Vendor string `json:"vendor"`
}

// scripted is a provider that answers each call as its script says.
type scripted struct {
	retries int
	answer  func(call int) (schemaflux.Response, error)
	calls   atomic.Int64
}

func (p *scripted) Complete(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	resp, err := p.answer(int(p.calls.Add(1)) - 1)
	if err == nil {
		resp.Model, resp.FinishReason = req.Model, "stop"
		resp.Usage = schemaflux.Usage{Input: 11, Output: 7, Total: 18}
	}
	return resp, err
}

func (p *scripted) Name() string                      { return "local" }
func (p *scripted) RetryPolicy() (int, time.Duration) { return p.retries, time.Millisecond }

func TestNoPayloadReachesAnObserverFromARealOperation(t *testing.T) {
	good := `{"number":"` + markAnswer + `","vendor":"Acme"}`
	keyBody := `{"error":{"message":"Incorrect API key provided: ` + markAPIKey + `. ` + markErrBody + `","type":"invalid_request_error","code":"invalid_api_key"}}`
	cases := []struct {
		name      string
		retries   int
		answer    func(call int) (schemaflux.Response, error)
		wantError bool
		wantNotes bool
	}{
		{"an answer that is used", 0, func(int) (schemaflux.Response, error) { return schemaflux.Response{Content: good}, nil }, false, false},
		{"an answer that is refused every time", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "not JSON at all " + markAnswer}, nil
		}, true, false},
		{"a provider error that quotes the key", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{}, llm.NewAPIError("local", "m", 401, keyBody)
		}, true, false},
		{"a provider error whose body is not JSON", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{}, llm.NewAPIError("local", "m", 500, "upstream broke "+markErrBody+" key="+markAPIKey)
		}, true, false},
		{"a failure that is retried and then answers", 2, func(call int) (schemaflux.Response, error) {
			if call == 0 {
				return schemaflux.Response{}, llm.NewAPIError("local", "m", 500, `{"error":{"message":"`+markErrBody+` `+markAPIKey+`"}}`)
			}
			return schemaflux.Response{Content: good}, nil
		}, false, true},
		{"an error of no kind that quotes the input", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{}, fmt.Errorf("connection reset while sending %s %s", markInput, markErrBody)
		}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			client := schemaflux.Client{Provider: &scripted{retries: tc.retries, answer: tc.answer}, Model: "test-model"}

			tr := &transcript{scope: map[telemetry.Scope]int{}}
			defer telemetry.RegisterObserver(tr)()

			options := ops.NewExtractOptions()
			options.CommonOptions.Context = schemaflux.WithClient(t.Context(), client)
			options.CommonOptions.Steering = "prefer the header " + markSteering
			options.SchemaHints = map[string]string{"number": markSteering}
			_, err := ops.ExtractResult[invoice]("Invoice from Acme "+markInput, options)

			if (err != nil) != tc.wantError {
				t.Fatalf("the call returned %v, want an error: %v", err, tc.wantError)
			}
			if tr.scope[telemetry.ScopeOperation] == 0 || tr.scope[telemetry.ScopeRequest] == 0 {
				t.Fatalf("the observer saw %v units, want an operation and at least one request: the test is not looking at what it is for", tr.scope)
			}
			if tc.wantNotes && tr.notes == 0 {
				t.Fatal("the observer was given no note for a request that was retried")
			}
			for _, leak := range tr.leaks() {
				t.Errorf("leaked: %s", leak)
			}
		})
	}
}

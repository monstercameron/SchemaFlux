// Package parity holds the tests that prove version 2 of the library asks a
// model what version 1 asks it. It imports both roots, which nothing under v2
// may do, so it lives beside the code it compares and not inside it.
//
// The rule it holds (C2) is that for every operation, calling the v2 function
// with no options sends, field for field and byte for byte, the request the old
// root's function sends with its constructor's options, and that each option of
// the v2 type changes the request exactly as the old root's setter for it does.
// Each pair is run in the four states of the call-path table: plain, with a
// recorder, under an observer and under a floor the operation meets. The request
// is the same in all of them: observing a call never changes what the model is
// asked.
//
// This file is the harness: two recording providers (one for each root's
// provider contract), the comparison, and runParity, which runs a table of cases.
// A family lane adds a file of its own with a table and a test that calls
// runParity.
package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
)

const (
	// providerName and modelName are what both recording providers are called and
	// the one model they give every tier.
	providerName = "parity"
	modelName    = "parity-model"

	// pinnedModel is the model the cases that set Options.Model name. The floor
	// state declares a route for it as well, since a policy allows only routes
	// that were declared.
	pinnedModel = "pinned-model"
)

// sentRequest is a request as a provider received it, in one shape for both
// roots: every field the library sets, and nothing that is the provider
// contract's own.
type sentRequest struct {
	Model           string
	System          string
	User            string
	Temperature     float64
	MaxTokens       int
	Format          string
	Schema          string // the JSON schema, written with its keys in order
	SchemaName      string
	Effort          string
	EffortPreferred bool
	CacheKey        string
	WebSearch       bool
	Decision        string // the typed decision, written as JSON
}

// fields lists the request's fields in the order they are compared, as name and
// value, so that a difference is named and the first one found is reported.
func (r sentRequest) fields() []struct{ name, value string } {
	return []struct{ name, value string }{
		{"Model", r.Model},
		{"System", r.System},
		{"User", r.User},
		{"Temperature", fmt.Sprint(r.Temperature)},
		{"MaxTokens", fmt.Sprint(r.MaxTokens)},
		{"Format", r.Format},
		{"Schema", r.Schema},
		{"SchemaName", r.SchemaName},
		{"Effort", r.Effort},
		{"EffortPreferred", fmt.Sprint(r.EffortPreferred)},
		{"CacheKey", r.CacheKey},
		{"WebSearch", fmt.Sprint(r.WebSearch)},
		{"Decision", r.Decision},
	}
}

// canonical writes v as JSON, which orders the keys of a map, so that two maps
// with the same content are the same bytes.
func canonical(v any) string {
	if v == nil {
		return ""
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "unmarshalable: " + err.Error()
	}
	return string(out)
}

// decisionShape is a typed decision as the comparison reads it, whichever root's
// type it came from.
type decisionShape struct {
	State, Guidance string
	Questions       []questionShape
}

type questionShape struct {
	Kind      uint8
	Ask       string
	Options   []string
	Criteria  []string
	Low, High int
}

// script is what both recording providers share: the bodies they answer with and
// the requests they were sent.
type script struct {
	replies []string

	mu   sync.Mutex
	seen []sentRequest
}

// next records req and returns the body to answer it with: the replies in order,
// the last repeating.
func (s *script) next(req sentRequest) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.seen)
	s.seen = append(s.seen, req)
	if len(s.replies) == 0 {
		return "{}"
	}
	return s.replies[min(n, len(s.replies)-1)]
}

// requests returns what was received.
func (s *script) requests() []sentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentRequest(nil), s.seen...)
}

// oldModel is a recording provider of the old root's contract.
type oldModel struct{ *script }

func (m oldModel) Name() string                               { return providerName }
func (m oldModel) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (m oldModel) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (m oldModel) ModelFor(types.Speed) string                { return modelName }

func (m oldModel) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	sent := sentRequest{
		Model: req.Model, System: req.SystemPrompt, User: req.UserPrompt, Temperature: req.Temperature,
		MaxTokens: req.MaxTokens, Format: req.ResponseFormat, Schema: canonical(req.JSONSchema), SchemaName: req.SchemaName,
		Effort: req.ReasoningEffort, EffortPreferred: req.ReasoningEffortPreferred, CacheKey: req.PromptCacheKey, WebSearch: req.WebSearch,
	}
	if d := req.Decision; d != nil {
		shape := decisionShape{State: d.State, Guidance: d.Guidance}
		for _, q := range d.Questions {
			shape.Questions = append(shape.Questions, questionShape{uint8(q.Kind), q.Ask, q.Options, q.Criteria, q.Low, q.High})
		}
		sent.Decision = canonical(shape)
	}
	body := m.next(sent)
	return llm.CompletionResponse{
		Content: body, Provider: providerName, Model: req.Model, FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14},
	}, nil
}

// newModel is a recording provider of version 2's contract.
type newModel struct{ *script }

func (m newModel) Name() string                      { return providerName }
func (m newModel) RetryPolicy() (int, time.Duration) { return -1, 0 }
func (m newModel) ModelFor(schemaflux.Tier) string   { return modelName }

func (m newModel) Complete(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	sent := sentRequest{
		Model: req.Model, System: req.System, User: req.User, Temperature: req.Temperature,
		MaxTokens: req.MaxTokens, Format: req.Format, Schema: canonical(req.Schema), SchemaName: req.SchemaName,
		Effort: string(req.Effort), EffortPreferred: req.EffortPreferred, CacheKey: req.CacheKey, WebSearch: req.WebSearch,
	}
	if d := req.Decision; d != nil {
		shape := decisionShape{State: d.State, Guidance: d.Guidance}
		for _, q := range d.Questions {
			shape.Questions = append(shape.Questions, questionShape{uint8(q.Kind), q.Ask, q.Options, q.Criteria, q.Low, q.High})
		}
		sent.Decision = canonical(shape)
	}
	body := m.next(sent)
	return schemaflux.Response{
		Content: body, Model: req.Model, FinishReason: "stop",
		Usage: schemaflux.Usage{Input: 11, Output: 3, Total: 14},
	}, nil
}

// excerptAround returns at most about a hundred bytes of s around the byte at
// position at, so that a failure shows where two prompts part and never prints a
// whole prompt.
func excerptAround(s string, at int) string {
	const before, after = 30, 70
	start := max(at-before, 0)
	for start > 0 && start < len(s) && s[start]&0xC0 == 0x80 {
		start--
	}
	end := min(at+after, len(s))
	for end < len(s) && s[end]&0xC0 == 0x80 {
		end++
	}
	text := strings.ReplaceAll(s[start:end], "\n", `\n`)
	if start > 0 {
		text = "..." + text
	}
	if end < len(s) {
		text += "..."
	}
	return text
}

// firstDifference returns the byte position at which a and b first differ.
func firstDifference(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// requireSameRequests fails the test, naming the first request and the first
// field that differ between what the old root sent and what v2 sent, with a
// bounded excerpt of the two values where they part.
func requireSameRequests(t *testing.T, label string, old, got []sentRequest) {
	t.Helper()
	if len(old) != len(got) {
		t.Errorf("%s: the old root sent %d requests and v2 sent %d", label, len(old), len(got))
		return
	}
	for i := range old {
		want, have := old[i].fields(), got[i].fields()
		for j := range want {
			if want[j].value == have[j].value {
				continue
			}
			at := firstDifference(want[j].value, have[j].value)
			t.Errorf("%s: request %d differs in %s, at byte %d (old %d bytes, v2 %d bytes)\n  old: %s\n   v2: %s",
				label, i+1, want[j].name, at, len(want[j].value), len(have[j].value),
				excerptAround(want[j].value, at), excerptAround(have[j].value, at))
			return
		}
	}
}

// state is one of the four states of the call-path table.
type state int

const (
	statePlain state = iota
	stateRecorder
	stateObserver
	stateFloor
)

func (s state) String() string {
	return [...]string{"plain", "a recorder", "an observer", "a floor the operation meets"}[s]
}

// parityCase is one pair: the old root's call and v2's, which have to send the
// same requests.
type parityCase struct {
	name string

	// op is the Op of the Record the v2 call adds, as the table of operation
	// names writes it.
	op string

	// replies are the bodies both providers answer with, in order, the last
	// repeating. Both providers answer the same, so that a call that needs
	// several requests sends the same several.
	replies []string

	// floor is the contract level the operation delivers, which the floor state
	// holds the client to. The zero value means the operation meets no floor
	// above prompt only, and the floor state is not run for it.
	floor schemaflux.ContractLevel

	// old makes the call with the old root. The context carries the old root's
	// client; the options of the call have to take it as their Context.
	old func(ctx context.Context) error

	// v2 makes the call with version 2. The context carries its client.
	v2 func(ctx context.Context) error
}

// oldRequests runs the old root's call on a recording provider and returns what
// it sent.
func oldRequests(t *testing.T, c parityCase) []sentRequest {
	t.Helper()
	model := oldModel{&script{replies: c.replies}}
	ctx := ops.WithSharedExecConfig(context.Background(), &ops.ExecConfig{Provider: model, Retries: -1})
	if err := c.old(ctx); err != nil {
		t.Fatalf("the old root's call failed: %v", err)
	}
	return model.requests()
}

// floorRoute declares the route the floor state's policy allows.
var floorRoute sync.Once

// v2Requests runs v2's call on a recording provider in the state given, and
// returns what it sent and the Records its recorder was given.
func v2Requests(t *testing.T, c parityCase, s state) ([]sentRequest, []schemaflux.Record) {
	t.Helper()
	model := newModel{&script{replies: c.replies}}
	client := schemaflux.Client{Provider: model}
	recorder := &schemaflux.Recorder{}
	switch s {
	case stateRecorder:
		client.Recorder = recorder
	case stateFloor:
		floorRoute.Do(func() {
			for _, model := range []string{modelName, pinnedModel} {
				provider.DeclareRoute(providerName, model, []string{"eu"}, 0)
			}
		})
		client.Policy = &schemaflux.DataPolicy{Providers: []string{providerName}, Regions: []string{"eu"}, MinContract: c.floor}
	}
	ctx := schemaflux.WithClient(context.Background(), client)
	if err := c.v2(ctx); err != nil {
		t.Fatalf("the v2 call failed in the state %q: %v", s, err)
	}
	return model.requests(), recorder.Records()
}

// operationCounter is an observer that counts the operations that finish.
type operationCounter struct {
	mu    sync.Mutex
	names []string
}

func (o *operationCounter) OperationStarted(ctx context.Context, _ telemetry.OperationEvent) context.Context {
	return ctx
}

func (o *operationCounter) OperationFinished(_ context.Context, r telemetry.OperationResult) {
	if r.Scope != telemetry.ScopeOperation {
		return
	}
	o.mu.Lock()
	o.names = append(o.names, r.Operation)
	o.mu.Unlock()
}

// runParity runs the cases in the four states. The three states that are not
// process state run in parallel; the observer state runs after them, one case
// at a time, because an observer changes the path of every call that runs while
// it is installed. The test that calls it must not be parallel for the same
// reason.
func runParity(t *testing.T, cases []parityCase) {
	t.Helper()
	t.Run("plain, a recorder and a floor", func(t *testing.T) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				reference := oldRequests(t, c)
				if len(reference) == 0 {
					t.Fatal("the old root sent nothing: the case proves nothing")
				}
				states := []state{statePlain, stateRecorder}
				if c.floor > schemaflux.ContractPromptOnly {
					states = append(states, stateFloor)
				}
				for _, s := range states {
					got, records := v2Requests(t, c, s)
					requireSameRequests(t, s.String(), reference, got)
					if s == stateRecorder {
						if len(records) != 1 || records[0].Op != c.op || records[0].Err != nil || records[0].Requests != len(got) {
							t.Errorf("a recorder was given %d Records, want one for %q with %d requests and no error: %+v", len(records), c.op, len(got), records)
						}
					}
				}
			})
		}
	})
	t.Run("an observer", func(t *testing.T) {
		counter := &operationCounter{}
		t.Cleanup(telemetry.AddObserver(counter))
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				reference := oldRequests(t, c)
				counter.mu.Lock()
				counter.names = nil
				counter.mu.Unlock()
				got, _ := v2Requests(t, c, stateObserver)
				requireSameRequests(t, stateObserver.String(), reference, got)
				counter.mu.Lock()
				defer counter.mu.Unlock()
				if len(counter.names) != 1 {
					t.Errorf("the observer saw %d operations finish (%v), want exactly one", len(counter.names), counter.names)
				}
			})
		}
	})
}

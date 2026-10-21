package guard_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// canary is text that must never appear in a Record, an error, a log line or a
// plan. It is long enough and odd enough to occur nowhere in the library's own
// fixed prompts.
const canary = "ZZ-CANARY-7f3a91"

// floorName is the name of the fake model of a client held to a floor: the
// policy allows that provider alone, and its route is declared once.
const floorName = "guard-floor"

var declareFloorRoute sync.Once

// in is what a call is given. A row's call puts each part where the function
// takes caller text: Text where it takes the data, Key where it takes a map key
// or a field name, Label where it takes a label, a category, a criterion or a
// question, Steer in the options' Steering, and Tokens in MaxOutputTokens. The
// canary run puts the canary in every part; a negative Tokens is the option
// every function refuses.
type in struct {
	Text, Key, Label, Steer string
	Tokens                  int
}

// opts is the common options of the call.
func (x in) opts() schemaflux.Options {
	return schemaflux.Options{Steering: x.Steer, MaxOutputTokens: x.Tokens}
}

// neutral is an input that carries nothing the guard looks for.
func neutral() in {
	return in{Text: "The quarterly report is attached", Key: "name", Label: "alpha", Steer: "be brief"}
}

// canaried is an input that carries the canary in every part.
func canaried() in {
	return in{Text: "text " + canary, Key: "key" + canary, Label: "label " + canary, Steer: "steer " + canary}
}

// ret is the result of a call as the table holds it: the value, then the error.
func ret[T any](v T, err error) (any, error) { return v, err }

// retDrop returns only the error and a value of a type the table does not look
// into: the call has no result worth checking beside an error.
func retDrop(_ any, err error) (any, error) { return nil, err }

// state is how a client is set up for a call: the four states of the call-path
// table. The fake provider is the same in all of them.
type state struct {
	name     string
	recorder bool
	observer bool
	// second adds a recorder to the call's context as well as the client's: each
	// receives the Record, once.
	second bool
	floor  schemaflux.ContractLevel
}

var (
	statePlain    = state{name: "plain"}
	stateRecorder = state{name: "recorder", recorder: true}
	stateObserver = state{name: "observer", recorder: true, observer: true}
	stateFloor    = state{name: "floor", recorder: true, floor: schemaflux.ContractJSONWellFormed}
	stateTwo      = state{name: "two recorders", recorder: true, second: true}
)

// recordedStates are the states in which a Record can be read.
var recordedStates = []state{stateRecorder, stateObserver, stateFloor}

// scopeLog is a telemetry observer that keeps what it is told.
type scopeLog struct {
	mu       sync.Mutex
	started  []telemetry.Event
	finished []telemetry.Result
}

func (l *scopeLog) Started(ctx context.Context, e telemetry.Event) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, e)
	return ctx
}

func (l *scopeLog) Finished(_ context.Context, r telemetry.Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finished = append(l.finished, r)
}

// operations returns the operation-scope events seen: the starts and the
// finishes. The request-scope events inside an operation are its children.
func (l *scopeLog) operations() (starts []telemetry.Event, finishes []telemetry.Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.started {
		if e.Scope == telemetry.ScopeOperation {
			starts = append(starts, e)
		}
	}
	for _, r := range l.finished {
		if r.Scope == telemetry.ScopeOperation {
			finishes = append(finishes, r)
		}
	}
	return starts, finishes
}

// rig is a client on a scripted provider, set up as a state says, and what it
// reports to.
type rig struct {
	model  *schemafluxtest.Provider
	rec    *schemaflux.Recorder
	rec2   *schemaflux.Recorder
	logs   *schemafluxtest.Logs
	scopes *scopeLog
	ctx    context.Context
}

// newRig builds a rig. script scripts the fake; nil leaves it answering "{}". A
// state with an observer registers one for the length of the test, which is why
// no test of this package that uses one runs in parallel with another.
func newRig(t testing.TB, s state, timeout time.Duration, script func(*schemafluxtest.Provider)) *rig {
	t.Helper()
	r := &rig{model: &schemafluxtest.Provider{}, logs: &schemafluxtest.Logs{}, scopes: &scopeLog{}}
	r.model.SetName(floorName)
	if script != nil {
		script(r.model)
	}
	client := schemaflux.Client{Provider: r.model, Logger: r.logs.Logger(), Timeout: timeout}
	if s.recorder {
		r.rec = &schemaflux.Recorder{}
		client.Recorder = r.rec
	}
	if s.floor > schemaflux.ContractPromptOnly {
		declareFloorRoute.Do(func() { provider.DeclareRoute(floorName, "test-model", []string{"eu"}, 0) })
		client.Policy = &schemaflux.DataPolicy{Providers: []string{floorName}, Regions: []string{"eu"}, MinContract: s.floor}
	}
	if s.observer {
		restore := telemetry.RegisterObserver(r.scopes)
		t.Cleanup(restore)
	}
	r.ctx = schemaflux.WithClient(context.Background(), client)
	if s.second {
		r.rec2 = &schemaflux.Recorder{}
		r.ctx = schemaflux.WithRecorder(r.ctx, r.rec2)
	}
	return r
}

// records returns the Records of the rig's recorder.
func (r *rig) records() []schemaflux.Record {
	if r.rec == nil {
		return nil
	}
	return r.rec.Records()
}

// expand fills the canary slot of a scripted answer.
func expand(body string, withCanary bool) string {
	fill := "plain"
	if withCanary {
		fill = canary
	}
	return strings.ReplaceAll(body, "{{c}}", fill)
}

// succeeds scripts the fake with the row's answer, or with an answer shaped from
// the request when the row names none.
func (r *row) succeeds(m *schemafluxtest.Provider, x in, withCanary bool) {
	switch {
	case r.script != nil:
		r.script(m, x, withCanary)
	case r.answer == "":
		m.Shape()
	default:
		m.Reply(expand(r.answer, withCanary))
	}
}

// failureOf returns the *Error an error is, or nil.
func failureOf(err error) *schemaflux.Error {
	e, _ := errors.AsType[*schemaflux.Error](err)
	return e
}

// isZeroResult reports whether a result is the zero value of its type.
func isZeroResult(v any) bool {
	if v == nil {
		return true
	}
	return reflect.ValueOf(v).IsZero()
}

// describeValue says what a non-zero value is, without printing it: a payload
// must not reach a test log either.
func describeValue(v any) string {
	return reflect.TypeOf(v).String()
}

// withDeadline returns ctx with a deadline of d from now.
func withDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, time.Now().Add(d))
}

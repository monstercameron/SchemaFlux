package op_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// The helpers of the tests of this package: a descriptor that reads an invoice, a
// batch descriptor of the id protocol, fake models, and the checks every test
// makes of a call's Record and error.

// invoice is the value the test descriptors read an answer into.
type invoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

// canary is text that must never appear in a Record, an error, a plan or a log.
const canary = "CANARY-7f3a91"

// invoiceDescriptor is a descriptor of one input, a text, that is read as an
// invoice. Its Decode refuses an answer with no number.
func invoiceDescriptor() op.Descriptor[string, invoice] {
	return op.Descriptor[string, invoice]{
		ID: op.ID{Name: "read-invoice", Version: "1"},
		BuildPrompt: func(text string, _ op.Options) (string, string) {
			return "Read the invoice. Return a JSON object with number and total.", text
		},
		Contract: op.Contract[invoice]{
			SchemaName: "invoice",
			Decode: func(body string) (invoice, error) {
				var v invoice
				if err := json.Unmarshal([]byte(body), &v); err != nil {
					return invoice{}, errors.New("the answer is not a JSON object with number and total")
				}
				if v.Number == "" {
					return invoice{}, errors.New("the answer has no number")
				}
				return v, nil
			},
		},
	}
}

// batchDescriptor is invoiceDescriptor with a batch on the id protocol.
func batchDescriptor() op.Descriptor[string, invoice] {
	d := invoiceDescriptor()
	d.Batch = op.Batch[string, invoice]{
		Class:        op.BatchItemwise,
		Kind:         op.AlgebraIndependent,
		Instructions: "Read each invoice. Answer with the id of each and an object with number and total.",
		Item: func(raw json.RawMessage) (invoice, error) {
			var v invoice
			if err := json.Unmarshal(raw, &v); err != nil {
				return invoice{}, errors.New("not an invoice")
			}
			return v, nil
		},
	}
	return d
}

// answeringWith returns a fake model that answers its requests with the bodies
// given, in order, the last repeating.
func answeringWith(bodies ...string) *schemafluxtest.Provider {
	var model schemafluxtest.Provider
	model.Reply(bodies...)
	return &model
}

// clientOn returns a context carrying a client on p, with the settings applied to
// the client before it is put there.
func clientOn(p schemaflux.Provider, settings ...func(*schemaflux.Client)) context.Context {
	c := schemaflux.Client{Provider: p}
	for _, apply := range settings {
		apply(&c)
	}
	return schemaflux.WithClient(context.Background(), c)
}

// recordingTo is a client setting that gives the client a recorder.
func recordingTo(r *schemaflux.Recorder) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) { c.Recorder = r }
}

// floorRoute is the route declared for a client that is held to a floor.
var floorRoute sync.Once

// underFloor is a client setting that holds the client to a floor of level. The
// provider has to be a fake named "op-floor" (namedModel makes one).
func underFloor(level schemaflux.ContractLevel) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) {
		floorRoute.Do(func() { provider.DeclareRoute("op-floor", "test-model", []string{"eu"}, 0) })
		c.Policy = &schemaflux.DataPolicy{Providers: []string{"op-floor"}, Regions: []string{"eu"}, MinContract: level}
	}
}

// namedModel returns a fake model, named "op-floor", that answers as answeringWith
// does.
func namedModel(bodies ...string) *schemafluxtest.Provider {
	model := answeringWith(bodies...)
	model.SetName("op-floor")
	return model
}

// echoing returns a fake model that answers an invoice request with an invoice
// whose number is the user prompt, which is the item itself, and reports a usage
// of 11 tokens in and 3 out for each request.
func echoing() *schemafluxtest.Provider {
	var model schemafluxtest.Provider
	model.SetUsage(schemaflux.Usage{Input: 11, Output: 3, Total: 14})
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{
			Content: fmt.Sprintf(`{"number":%q,"total":1}`, req.User), Model: req.Model, FinishReason: "stop",
			Usage: schemaflux.Usage{Input: 11, Output: 3, Total: 14},
		}, nil
	})
	return &model
}

// batchAnswering returns a fake model that answers a batched request of the id
// protocol with an invoice for every item it was sent, numbered by the item, and
// a request of one item as echoing does. A request it cannot read is answered
// with "{}".
func batchAnswering() *schemafluxtest.Provider {
	var model schemafluxtest.Provider
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		body := fmt.Sprintf(`{"number":%q,"total":1}`, req.User)
		if payload, ok := strings.CutPrefix(req.User, "Process these items:\n"); ok {
			var tagged []struct {
				ID   string `json:"id"`
				Item string `json:"item"`
			}
			if err := json.Unmarshal([]byte(payload), &tagged); err != nil {
				body = "{}"
			} else {
				var parts []string
				for _, t := range tagged {
					parts = append(parts, fmt.Sprintf(`{"id":%q,"output":{"number":%q,"total":1}}`, t.ID, t.Item))
				}
				body = `{"items":[` + strings.Join(parts, ",") + `]}`
			}
		}
		return schemaflux.Response{
			Content: body, Model: req.Model, FinishReason: "stop",
			Usage: schemaflux.Usage{Input: 11, Output: 3, Total: 14},
		}, nil
	})
	return &model
}

// wantError fails the test unless err is an *Error of the kind and for the
// operation given, and returns it.
func wantError(t *testing.T, err error, kind schemaflux.Kind, operation string) *schemaflux.Error {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("the error is %T (%v), want an *Error", err, err)
	}
	if e.Kind != kind || e.Op != operation {
		t.Fatalf("error = kind %v op %q (%v), want kind %v op %q", e.Kind, e.Op, e, kind, operation)
	}
	if !errors.Is(err, kind) {
		t.Errorf("errors.Is(err, %v) is false", kind)
	}
	return e
}

// onlyRecord returns the one Record the recorder holds, or fails the test.
func onlyRecord(t *testing.T, r *schemaflux.Recorder) schemaflux.Record {
	t.Helper()
	records := r.Records()
	if len(records) != 1 {
		t.Fatalf("the recorder holds %d Records, want exactly 1: %+v", len(records), records)
	}
	return records[0]
}

// chainText returns what Error returns for err and for every error beneath it.
func chainText(err error) string {
	var out []string
	for ; err != nil; err = errors.Unwrap(err) {
		out = append(out, err.Error())
	}
	return strings.Join(out, " | ")
}

// operationWatcher is a telemetry observer that tells a function what finished.
type operationWatcher struct{ finished func(telemetry.Result) }

func (operationWatcher) Started(ctx context.Context, _ telemetry.Event) context.Context { return ctx }
func (w operationWatcher) Finished(_ context.Context, r telemetry.Result)               { w.finished(r) }

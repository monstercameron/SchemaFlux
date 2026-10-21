package op_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/op"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The payload of a call is the caller's input, the model's answer and what a
// provider says of its own failure. None of them may reach a Record, the text of
// any error of the chain, the JSON of either, or a line of the client's log: every
// caller logs what the library returns and what it logs.

const (
	payloadInput  = "CANARY_INPUT_a91c"
	payloadAnswer = "CANARY_ANSWER_a91c"
	payloadVendor = "CANARY_VENDOR_a91c"
)

var payloadCanaries = []string{payloadInput, payloadAnswer, payloadVendor}

// leaked returns the canaries that text holds.
func leaked(text string) []string {
	var held []string
	for _, c := range payloadCanaries {
		if strings.Contains(text, c) {
			held = append(held, c)
		}
	}
	return held
}

// logText is everything the logs kept, as text.
func logText(logs *schemafluxtest.Logs) string {
	var out strings.Builder
	for _, entry := range logs.Entries() {
		out.WriteString(entry.Message)
		entry.Attrs(func(a slog.Attr) bool {
			fmt.Fprintf(&out, " %s=%v", a.Key, a.Value)
			return true
		})
		out.WriteByte('\n')
	}
	return out.String()
}

// everythingTheCallLeft is what a caller could log of a call: the error's text
// and, when wholeChain is true, that of every error beneath it, the JSON of the
// error, the JSON of the Records and the lines the client logged. A call whose
// provider returned an error of its own leaves that error in the chain with the
// text the provider gave it, so for such a call only the error the caller
// receives is read.
func everythingTheCallLeft(t *testing.T, err error, rec *schemaflux.Recorder, logs *schemafluxtest.Logs, wholeChain bool) string {
	t.Helper()
	var out strings.Builder
	if wholeChain {
		out.WriteString(chainText(err))
	} else if err != nil {
		out.WriteString(err.Error())
	}
	if err != nil {
		blob, jsonErr := json.Marshal(err)
		if jsonErr != nil {
			t.Fatalf("the error does not marshal: %v", jsonErr)
		}
		out.Write(blob)
		out.WriteString(fmt.Sprintf("%v %+v", err, err))
	}
	for _, r := range rec.Records() {
		blob, jsonErr := json.Marshal(r)
		if jsonErr != nil {
			t.Fatalf("the Record does not marshal: %v", jsonErr)
		}
		out.Write(blob)
		out.WriteString(fmt.Sprintf("%+v", r))
	}
	out.WriteString(logText(logs))
	return out.String()
}

// withLogs is a client setting that gives the client a logger that keeps its lines.
func withLogs(logs *schemafluxtest.Logs) func(*schemaflux.Client) {
	return func(c *schemaflux.Client) { c.Logger = logs.Logger() }
}

// payloadRuns are the calls of the package, each handed the input, on a provider
// that fails with the error given, and each returning the call's error.
func payloadRuns(provider *schemafluxtest.Provider, rec *schemaflux.Recorder, logs *schemafluxtest.Logs) map[string]func() error {
	client := func() func(*schemaflux.Client) {
		return func(c *schemaflux.Client) { c.Recorder = rec; c.Logger = logs.Logger() }
	}
	return map[string]func() error{
		"Run": func() error {
			_, err := op.Run(clientOn(provider, client()), invoiceDescriptor(), payloadInput)
			return err
		},
		"RunBatch": func() error {
			_, err := op.RunBatch(clientOn(provider, client()), batchDescriptor(), []string{payloadInput})
			return err
		},
		"RunMany": func() error {
			_, err := op.RunMany(clientOn(provider, client()), invoiceDescriptor(), []string{payloadInput, payloadInput + "2"})
			return err
		},
	}
}

func TestNoCallLeavesThePayloadInARecordAnErrorOrALogLineWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is unavailable", Status: 503, Err: errors.New(payloadVendor + " " + payloadInput)}
	for _, name := range []string{"Run", "RunBatch", "RunMany"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec schemaflux.Recorder
			var logs schemafluxtest.Logs
			model := answeringWith()
			model.Fail(vendor)
			err := payloadRuns(model, &rec, &logs)[name]()
			if err == nil {
				t.Fatal("want an error")
			}
			if held := leaked(everythingTheCallLeft(t, err, &rec, &logs, false)); len(held) != 0 {
				t.Errorf("the call left %v behind", held)
			}
		})
	}
}

func TestNoCallLeavesThePayloadWhenTheModelsAnswerIsRefused(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Run", "RunBatch", "RunMany"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec schemaflux.Recorder
			var logs schemafluxtest.Logs
			model := answeringWith(`{"number":"` + payloadAnswer + `"`) // cut off: not JSON
			err := payloadRuns(model, &rec, &logs)[name]()
			if err == nil {
				t.Fatal("want an error")
			}
			if held := leaked(everythingTheCallLeft(t, err, &rec, &logs, true)); len(held) != 0 {
				t.Errorf("the call left %v behind", held)
			}
		})
	}
}

func TestNoCallLeavesThePayloadWhenTheProviderReturnsAPlainErrorOfItsOwn(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Run", "RunBatch", "RunMany"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec schemaflux.Recorder
			var logs schemafluxtest.Logs
			model := answeringWith()
			model.Fail(errors.New(payloadVendor + " " + payloadInput))
			err := payloadRuns(model, &rec, &logs)[name]()
			if err == nil {
				t.Fatal("want an error")
			}
			if held := leaked(everythingTheCallLeft(t, err, &rec, &schemafluxtest.Logs{}, false)); len(held) != 0 {
				t.Errorf("the error or the Record holds %v", held)
			}
			if held := leaked(logText(&logs)); len(held) != 0 {
				t.Errorf("the client's log holds %v", held)
			}
		})
	}
}

func TestPreflightsPlanAndErrorsHoldNoPayload(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	d := invoiceDescriptor()
	build := d.BuildPrompt
	d.BuildPrompt = func(text string, o op.Options) (string, string) {
		if strings.Contains(text, "panic") {
			panic(payloadAnswer)
		}
		return build(text, o)
	}
	plan, err := op.Preflight(clientOn(answeringWith(), withLogs(&logs)), d, []string{payloadInput, "panic " + payloadInput})
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := plan.Serialize()
	text := string(blob) + plan.Explain() + plan.IneligibleReason + logText(&logs)
	if held := leaked(text); len(held) != 0 {
		t.Errorf("the plan or the log holds %v: %s", held, text)
	}
}

func TestADescriptorsOwnPanicValueIsNotInTheErrorOrTheLog(t *testing.T) {
	t.Parallel()
	var logs schemafluxtest.Logs
	var rec schemaflux.Recorder
	d := invoiceDescriptor()
	d.Contract.Decode = func(string) (invoice, error) { panic(errors.New(payloadAnswer)) }
	_, err := op.RunMany(clientOn(echoing(), withLogs(&logs), recordingTo(&rec)), d, []string{payloadInput})
	if err == nil {
		t.Fatal("want an error")
	}
	if held := leaked(everythingTheCallLeft(t, err, &rec, &logs, true)); len(held) != 0 {
		t.Errorf("the call left %v behind", held)
	}
}

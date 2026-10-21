package guard_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// payloadScenarios are the calls that carry the canary through the library: one
// that succeeds, with the canary in the input, the instructions, the options and
// the model's answer, and every way of failing, with the canary in the input and
// in what the provider says when it fails.
func payloadScenarios() []scenario {
	return append([]scenario{{name: "success", sends: true}}, failureScenarios...)
}

// seenText is something a call leaves behind, read as text, and where it is.
type seenText struct{ where, text string }

// chainTexts returns what Error returns for err and for every error beneath it.
func chainTexts(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		out = append(out, e.Error())
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		}
	}
	walk(err)
	return out
}

// loggedAs renders v as slog's text and JSON handlers would write it, which
// resolves a LogValuer on the way.
func loggedAs(v any) []string {
	var text, structured bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Error("the call", "value", v)
	slog.New(slog.NewJSONHandler(&structured, nil)).Error("the call", "value", v)
	return []string{text.String(), structured.String()}
}

// recordTexts is every way a Record can be read as text: its fields, its JSON
// and its log value.
func recordTexts(o outcome, wholeChain bool) []seenText {
	var out []seenText
	for _, rec := range o.rig.records() {
		out = append(out, seenText{"the Record's fields", fmt.Sprintf("%+v", rec)})
		if encoded, err := json.Marshal(rec); err == nil {
			out = append(out, seenText{"the Record's JSON", string(encoded)})
		} else {
			out = append(out, seenText{"the Record's JSON", "(not encodable: " + err.Error() + ")"})
		}
		for _, line := range loggedAs(rec) {
			out = append(out, seenText{"the Record's log value", line})
		}
		if wholeChain {
			for _, link := range chainTexts(rec.Err) {
				out = append(out, seenText{"a link of the Record's error chain", link})
			}
		}
	}
	return out
}

// errorTexts is every way an error can be read as text. The whole chain is read
// when the library made the error; when a provider's own error is beneath it,
// that error keeps the text the provider gave it and only what the library says
// is read.
func errorTexts(err error, wholeChain bool) []seenText {
	if err == nil {
		return nil
	}
	var out []seenText
	if wholeChain {
		for _, link := range chainTexts(err) {
			out = append(out, seenText{"Error() of a link of the chain", link})
		}
	} else {
		out = append(out, seenText{"Error()", err.Error()})
	}
	if encoded, jsonErr := json.Marshal(err); jsonErr == nil {
		out = append(out, seenText{"the error's JSON", string(encoded)})
	} else {
		out = append(out, seenText{"the error's JSON", "(not encodable: " + jsonErr.Error() + ")"})
	}
	for _, line := range loggedAs(err) {
		out = append(out, seenText{"the error's log value", line})
	}
	if e := failureOf(err); e != nil {
		out = append(out, seenText{"(*Error).LogValue", e.LogValue().String()})
	}
	return out
}

// logTexts is every line the client's Logger received.
func logTexts(o outcome) []seenText {
	var out []seenText
	for _, entry := range o.rig.logs.Entries() {
		line := entry.Message
		entry.Attrs(func(a slog.Attr) bool {
			line += " " + a.Key + "=" + a.Value.String()
			return true
		})
		out = append(out, seenText{"a line the client's Logger received", line})
	}
	return out
}

// requireNoCanary fails the test for each text that holds the canary.
func requireNoCanary(t *testing.T, texts []seenText) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(s.text, canary) {
			t.Errorf("the canary reached %s: %q", s.where, excerpt(s.text))
		}
	}
}

// excerpt shortens text for a failure message, so that a test never prints a
// whole prompt.
func excerpt(text string) string {
	const most = 200
	runes := []rune(text)
	if len(runes) <= most {
		return text
	}
	return string(runes[:most]) + "..."
}

// The two guards below run every scenario on every row, including those a row is
// exempt from in the guards of kinds and Records (its na map): a call that
// succeeds where another would fail still leaves a Record and log lines, and
// none of them may hold the canary.
func TestRecordCarriesNoPayload(t *testing.T) {
	for _, r := range allRows() {
		for _, st := range recordedStates {
			for _, sc := range payloadScenarios() {
				t.Run(r.id()+"/"+st.name+"/"+sc.name, func(t *testing.T) {
					if sc.floor != 0 && st.name != "floor" {
						t.Skip("a floor is a state of its own")
					}
					if sc.nilCtx {
						t.Skip("a nil context leaves no Record")
					}
					o := run(t, &r, st, sc, canaried())
					texts := recordTexts(o, !sc.fromProvider)
					texts = append(texts, logTexts(o)...)
					requireNoCanary(t, texts)
				})
			}
		}
	}
}

func TestErrorCarriesNoPayload(t *testing.T) {
	for _, r := range allRows() {
		for _, st := range allStates {
			for _, sc := range failureScenarios {
				t.Run(r.id()+"/"+st.name+"/"+sc.name, func(t *testing.T) {
					if sc.floor != 0 && st.name != "floor" {
						t.Skip("a floor is a state of its own")
					}
					o := run(t, &r, st, sc, canaried())
					if o.err == nil {
						t.Skip("the call did not fail: reported by TestEveryFailureIsAnError")
					}
					texts := errorTexts(o.err, !sc.fromProvider)
					texts = append(texts, logTexts(o)...)
					requireNoCanary(t, texts)
				})
			}
		}
	}
}

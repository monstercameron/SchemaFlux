package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// WhichOrNoneResult is WhichOrNone with the model's confidence and the
// envelope: the label, whether one was found, and the model's claim about it.
func TestWhichOrNoneResultReturnsTheAnswerTheConfidenceAndTheEnvelope(t *testing.T) {
	tickets := []string{"Late parcel", "Double charge", "Password reset"}
	cases := []struct {
		name       string
		response   string
		labels     []string
		wantLabel  string
		wantFound  bool
		wantConf   float64
		wantErr    bool
		wantAsked  bool
		wantAnswer string
	}{
		{"the first label", `{"q1":1,"confidence":0.9}`, tickets, "Late parcel", true, 0.9, false, true, "Late parcel"},
		{"the second label", `{"q1":2,"confidence":0.5}`, tickets, "Double charge", true, 0.5, false, true, "Double charge"},
		{"the last label", `{"q1":3,"confidence":0.25}`, tickets, "Password reset", true, 0.25, false, true, "Password reset"},
		{"none of them", `{"q1":4,"confidence":0.75}`, tickets, "", false, 0.75, false, true, whichNone},
		{"none, with a confidence of zero", `{"q1":4,"confidence":0}`, tickets, "", false, 0, false, true, whichNone},
		{"one label, chosen", `{"q1":1,"confidence":1}`, []string{"Only"}, "Only", true, 1, false, true, "Only"},
		{"one label, none", `{"q1":2,"confidence":0.1}`, []string{"Only"}, "", false, 0.1, false, true, whichNone},
		{"a number past none", `{"q1":5,"confidence":0.5}`, tickets, "", false, 0, true, true, ""},
		{"zero", `{"q1":0,"confidence":0.5}`, tickets, "", false, 0, true, true, ""},
		{"no confidence", `{"q1":1}`, tickets, "", false, 0, true, true, ""},
		{"a confidence off the scale", `{"q1":1,"confidence":1.5}`, tickets, "", false, 0, true, true, ""},
		{"no answer", `{"confidence":0.5}`, tickets, "", false, 0, true, true, ""},
		{"the label itself", `{"q1":"Late parcel","confidence":0.5}`, tickets, "", false, 0, true, true, ""},
		{"no labels, so nothing is asked", `{"q1":1,"confidence":0.5}`, nil, "", false, 0, false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder, observed := dec12Observing(tc.response)
			result, err := WhichOrNoneResult(ctx, "Where is my parcel?", "Which open ticket is this about?", tc.labels)

			if sent := len(recorder.taken()) > 0; sent != tc.wantAsked {
				t.Fatalf("a request was sent: %v, want %v", sent, tc.wantAsked)
			}
			if result.Meta.Operation != "which" || result.Meta.RequestedContract != assessResultContract {
				t.Errorf("the envelope names %q at %v", result.Meta.Operation, result.Meta.RequestedContract)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted: %+v", result.Value)
				}
				if result.Value != (Judged[Picked]{}) {
					t.Errorf("a value came back beside the error: %+v", result.Value)
				}
				if result.Meta.DeliveredContract != types.ContractLevel(0) {
					t.Errorf("a contract was delivered beside an error: %v", result.Meta.DeliveredContract)
				}
				return
			}
			if err != nil {
				t.Fatalf("the call failed: %v", err)
			}
			if got := result.Value; got.Answer.Label != tc.wantLabel || got.Answer.Found != tc.wantFound || got.ModelConfidence != tc.wantConf {
				t.Errorf("answer = %+v, want %q, %v, confidence %v", got, tc.wantLabel, tc.wantFound, tc.wantConf)
			}
			if result.Meta.DeliveredContract != assessResultContract {
				t.Errorf("the contract delivered is %v", result.Meta.DeliveredContract)
			}

			records := observed.taken()
			if !tc.wantAsked {
				if len(records) != 0 || result.Meta.Attempts != 0 {
					t.Errorf("nothing was asked, and the observer was told %d times with %d attempts", len(records), result.Meta.Attempts)
				}
				return
			}
			if len(records) != 1 || records[0].Asked[0].Answer != tc.wantAnswer || records[0].ModelConfidence == nil || *records[0].ModelConfidence != tc.wantConf {
				t.Errorf("the observer was told %+v", records)
			}
		})
	}
}

// Questions and labels the decision cannot be made over are refused, with the
// envelope of a refusal, and send nothing.
func TestWhichOrNoneResultRefusesWhatWhichOrNoneRefuses(t *testing.T) {
	many := make([]string, strictEnumBudget)
	for i := range many {
		many[i] = "label " + strings.Repeat("x", 1) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
	}
	cases := []struct {
		name     string
		question string
		labels   []string
		want     string
	}{
		{"a blank question and no labels", "  ", nil, "question 1 is blank"},
		{"a blank question", " ", []string{"a", "b"}, "question 1 is blank"},
		{"a label that is the added option", "Which?", []string{"a", whichNone}, "adds for itself"},
		{"the added option in capitals", "Which?", []string{"a", "NONE OF THESE"}, "adds for itself"},
		{"the added option in another case with space", "Which?", []string{" None Of These "}, "adds for itself"},
		{"a blank label", "Which?", []string{"a", " "}, "is blank"},
		{"a repeated label", "Which?", []string{"a", " a"}, "repeats"},
		{"as many labels as the budget", "Which?", many, "at most 499"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder := dec12WithRecorder(`{"q1":1,"confidence":0.5}`)
			result, err := WhichOrNoneResult(ctx, "text", tc.question, tc.labels)
			if err == nil || types.KindOf(err) != types.KindConfiguration {
				t.Fatalf("err = %v, want a configuration error", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error is %q, want it to say %q", err, tc.want)
			}
			if result.Meta.Operation != "which" || result.Meta.RequestedContract != assessResultContract {
				t.Errorf("a refusal's envelope is %+v", result.Meta)
			}
			if len(recorder.taken()) != 0 {
				t.Error("a request was sent for a refusal")
			}
		})
	}
}

// The request ids a caller names are on the envelope, after an answer and after
// a refusal.
func TestWhichOrNoneResultNamesTheRequestOnEveryPath(t *testing.T) {
	ids := types.OpOptions{RequestID: "req-12", CorrelationID: "corr-34"}
	ctx, _ := dec12WithRecorder(`{"q1":1,"confidence":0.5}`)

	answered, err := WhichOrNoneResult(ctx, "text", "Which?", []string{"a", "b"}, ids)
	if err != nil || answered.Meta.RequestID != "req-12" || answered.Meta.CorrelationID != "corr-34" {
		t.Errorf("after an answer: %+v, %v", answered.Meta, err)
	}
	refused, err := WhichOrNoneResult(ctx, "text", "Which?", []string{"a", whichNone}, ids)
	if err == nil || refused.Meta.RequestID != "req-12" || refused.Meta.CorrelationID != "corr-34" {
		t.Errorf("after a refusal: %+v, %v", refused.Meta, err)
	}
	local, err := WhichOrNoneResult(ctx, "text", "Which?", nil, ids)
	if err != nil || local.Meta.RequestID != "req-12" {
		t.Errorf("with nothing to choose from: %+v, %v", local.Meta, err)
	}
	generated, err := WhichOrNoneResult(context.Background(), "text", "Which?", nil)
	if err != nil || generated.Meta.RequestID == "" {
		t.Errorf("with no id named, the envelope has none: %+v, %v", generated.Meta, err)
	}
}

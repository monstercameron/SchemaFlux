package ops

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type lx025Order struct {
	ID    string  `json:"id"`
	Total float64 `json:"total"`
	Note  string  `json:"note"`
}

var lx025Subject = lx025Order{ID: "A-1", Total: 12.5, Note: "R&D <rush>"}

type validateCall struct {
	system, user string
	opts         types.OpOptions
	calls        int
}

func validateWith(t *testing.T, response string, opts ValidateOptions) (ValidateResult[lx025Order], error, *validateCall) {
	t.Helper()
	sent := &validateCall{}
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, system, user string, opt types.OpOptions) (string, error) {
		sent.system, sent.user, sent.opts = system, user, opt
		sent.calls++
		return response, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := Validate(lx025Subject, opts)
	return result, err, sent
}

// lx025Rules asks for neither a correction nor explanations, both of which
// NewValidateOptions turns on.
func lx025Rules() ValidateOptions {
	return NewValidateOptions().
		WithRules("the total must be positive and the note must be polite").
		WithAutoCorrect(false).
		WithIncludeExplanations(false)
}

const (
	lx025Issue = `{"field":"note","message":"the note is curt","suggestion":"soften it","explanation":"it reads as a demand"}`
	lx025Clean = `{"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"fine"}`
)

func lx025Answer(errors, warnings, info string) string {
	return `{"errors":[` + errors + `],"warnings":[` + warnings + `],"info":[` + info + `],"confidence":0.8,"summary":"s"}`
}

// LX-025. Validate works the verdict out from the issues, which is right, and
// read an answer with no issues in it as an answer that found none: the three
// lists were optional, so `{"valid": false, "summary": "..."}` and `{}` both
// came back valid. A list that is missing was not reported on, and a model that
// says the data is invalid and does not say why has not given a usable answer.
func TestValidateRefusesAnAnswerThatDoesNotReport(t *testing.T) {
	cases := []struct {
		name     string
		response string
	}{
		{"invalid with nothing itemised", `{"valid":false,"summary":"the order is wrong"}`},
		{"an empty object", `{}`},
		{"only a verdict", `{"valid":true}`},
		{"only a confidence", `{"confidence":0.9}`},
		{"no list of errors", `{"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`},
		{"no list of warnings", `{"errors":[],"info":[],"confidence":0.9,"summary":"s"}`},
		{"no list of info", `{"errors":[],"warnings":[],"confidence":0.9,"summary":"s"}`},
		{"errors null", `{"errors":null,"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`},
		{"errors a string", `{"errors":"none","warnings":[],"info":[],"confidence":0.9,"summary":"s"}`},
		{"errors an object", `{"errors":{"note":"curt"},"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`},
		{"no confidence", `{"errors":[],"warnings":[],"info":[],"summary":"s"}`},
		{"a confidence off the scale", `{"errors":[],"warnings":[],"info":[],"confidence":1.4,"summary":"s"}`},
		{"an issue with no message", `{"errors":[{"field":"note"}],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`},
		{"an issue with a blank message", `{"errors":[{"field":"note","message":"  "}],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`},
		{"invalid, with every list empty", `{"valid":false,"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`},
		// Round 12: "invalid" beside a warning, when warnings do not fail, is no
		// longer refused. The report is kept and the data comes back invalid:
		// TestValidateKeepsAReportThatSaysInvalid.
		{"not JSON", `the order looks fine, R&D <rush> aside`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := validateWith(t, tc.response, lx025Rules())
			if err == nil {
				t.Fatalf("the answer was accepted: valid = %v", result.Valid)
			}
			kind := types.KindOf(err)
			if kind != types.KindSchemaViolation && kind != types.KindMalformedOutput && kind != types.KindInvariantViolation {
				t.Errorf("the error is classified %v: %v", kind, err)
			}
			if !reflect.DeepEqual(result, ValidateResult[lx025Order]{}) {
				t.Errorf("a refused validation returned a result: %+v", result)
			}
			if strings.Contains(err.Error(), "R&D") || strings.Contains(err.Error(), "curt") {
				t.Errorf("the error carries the model's or the caller's text: %v", err)
			}
		})
	}
}

// The verdict is the issues', by the caller's FailOn, whatever the model says
// about validity; and each issue's severity is the list it was reported in.
func TestValidateWorksOutTheVerdictFromTheIssues(t *testing.T) {
	cases := []struct {
		name     string
		opts     ValidateOptions
		response string
		valid    bool
		errors   int
		warnings int
		info     int
	}{
		{"nothing found", lx025Rules(), lx025Clean, true, 0, 0, 0},
		{"an error", lx025Rules(), lx025Answer(lx025Issue, "", ""), false, 1, 0, 0},
		{"a warning does not fail by default", lx025Rules(), lx025Answer("", lx025Issue, ""), true, 0, 1, 0},
		{"a warning fails when asked", lx025Rules().WithFailOn("warning"), lx025Answer("", lx025Issue, ""), false, 0, 1, 0},
		{"info does not fail a warning floor", lx025Rules().WithFailOn("warning"), lx025Answer("", "", lx025Issue), true, 0, 0, 1},
		{"info fails when asked", lx025Rules().WithFailOn("info"), lx025Answer("", "", lx025Issue), false, 0, 0, 1},
		{"the model says valid over an error", lx025Rules(), `{"valid":true,"errors":[` + lx025Issue + `],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, false, 1, 0, 0},
		{"the model says invalid and itemises an error", lx025Rules(), `{"valid":false,"errors":[` + lx025Issue + `],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, false, 1, 0, 0},
		{"the model says invalid over a warning that does fail", lx025Rules().WithFailOn("warning"), `{"valid":false,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"confidence":0.9,"summary":"s"}`, false, 0, 1, 0},
		{"two errors and a warning", lx025Rules(), lx025Answer(lx025Issue+","+lx025Issue, lx025Issue, ""), false, 2, 1, 0},
		{"an issue that names no field", lx025Rules(), lx025Answer(`{"field":null,"message":"the order is incomplete","suggestion":null,"explanation":null}`, "", ""), false, 1, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := validateWith(t, tc.response, tc.opts)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if result.Valid != tc.valid {
				t.Errorf("Valid = %v, want %v", result.Valid, tc.valid)
			}
			if len(result.Errors) != tc.errors || len(result.Warnings) != tc.warnings || len(result.Info) != tc.info {
				t.Errorf("%d errors, %d warnings, %d info; want %d, %d, %d",
					len(result.Errors), len(result.Warnings), len(result.Info), tc.errors, tc.warnings, tc.info)
			}
			for _, issue := range result.Errors {
				if issue.Severity != "error" {
					t.Errorf("an error carries severity %q", issue.Severity)
				}
			}
			for _, issue := range result.Warnings {
				if issue.Severity != "warning" {
					t.Errorf("a warning carries severity %q", issue.Severity)
				}
			}
			for _, issue := range result.Info {
				if issue.Severity != "info" {
					t.Errorf("an info item carries severity %q", issue.Severity)
				}
			}
		})
	}
}

// A correction is taken only when the caller asked for one. With AutoCorrect
// off the model was not asked, and a "corrected" value it sent anyway was
// returned all the same.
func TestValidateTakesACorrectionOnlyWhenAsked(t *testing.T) {
	corrected := `{"errors":[` + lx025Issue + `],"warnings":[],"info":[],"corrected":{"id":"A-1","total":12.5,"note":"please rush"},"confidence":0.9,"summary":"s"}`
	cases := []struct {
		name     string
		opts     ValidateOptions
		response string
		want     *lx025Order
		wantErr  bool
	}{
		{"asked for, and given", lx025Rules().WithAutoCorrect(true), corrected, &lx025Order{ID: "A-1", Total: 12.5, Note: "please rush"}, false},
		{"not asked for, and given anyway", lx025Rules(), corrected, nil, false},
		{"asked for, and not given", lx025Rules().WithAutoCorrect(true), lx025Answer(lx025Issue, "", ""), nil, false},
		{"asked for, and null", lx025Rules().WithAutoCorrect(true), `{"errors":[],"warnings":[],"info":[],"corrected":null,"confidence":0.9,"summary":"s"}`, nil, false},
		{"asked for, and not the right shape", lx025Rules().WithAutoCorrect(true), `{"errors":[],"warnings":[],"info":[],"corrected":"a string","confidence":0.9,"summary":"s"}`, nil, true},
		{"not asked for, and not the right shape", lx025Rules(), `{"errors":[],"warnings":[],"info":[],"corrected":"a string","confidence":0.9,"summary":"s"}`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := validateWith(t, tc.response, tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("a correction of the wrong shape was accepted: %+v", result.Corrected)
				}
				// Refused as an answer of the wrong shape, not for some other
				// reason a call can fail.
				if types.KindOf(err) != types.KindSchemaViolation {
					t.Errorf("err = %v (%v), want a schema violation", err, types.KindOf(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !reflect.DeepEqual(result.Corrected, tc.want) {
				t.Errorf("Corrected = %+v, want %+v", result.Corrected, tc.want)
			}
		})
	}
}

// What is sent: the answer is declared, the three lists and the confidence are
// required, the model is not asked for a verdict, and the data reaches it as
// written.
func TestValidateRequest(t *testing.T) {
	cases := []struct {
		name         string
		opts         ValidateOptions
		corrected    bool
		explanations bool
	}{
		{"rules only", lx025Rules(), false, false},
		{"with explanations", lx025Rules().WithIncludeExplanations(true), false, true},
		{"with a correction asked for", lx025Rules().WithAutoCorrect(true), true, false},
		{"with both", lx025Rules().WithAutoCorrect(true).WithIncludeExplanations(true), true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err, sent := validateWith(t, lx025Clean, tc.opts)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !strings.Contains(sent.user, "R&D <rush>") {
				t.Errorf("the data did not reach the model as written:\n%s", sent.user)
			}
			if strings.Contains(sent.system, "R&D") || strings.Contains(sent.system, "polite") {
				t.Error("the instructions carry the caller's data or rules")
			}
			if strings.Contains(sent.system, `"valid"`) {
				t.Error("the model is asked for a verdict")
			}

			schema := sent.opts.JSONSchema
			if schema == nil || sent.opts.SchemaName != "validation" || sent.opts.ResponseFormat != "json" {
				t.Fatalf("schema %v, name %q, format %q", schema != nil, sent.opts.SchemaName, sent.opts.ResponseFormat)
			}
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema breaks a strict-mode rule: %s", problem)
			}
			properties := schema["properties"].(map[string]any)
			for _, list := range []string{"errors", "warnings", "info", "confidence", "summary"} {
				if _, declared := properties[list]; !declared {
					t.Errorf("the schema has no %q", list)
				}
			}
			if _, asked := properties["valid"]; asked {
				t.Error("the schema asks for a verdict")
			}
			if _, asked := properties["corrected"]; asked != tc.corrected {
				t.Errorf("a correction asked for = %v, want %v", asked, tc.corrected)
			}
			issue := properties["errors"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			if _, asked := issue["explanation"]; asked != tc.explanations {
				t.Errorf("an explanation asked for = %v, want %v", asked, tc.explanations)
			}
		})
	}
}

// The deterministic rules still come first, and still need no call when they
// are all there is.
func TestValidateKeepsItsDeterministicFindings(t *testing.T) {
	type contact struct {
		Email string `json:"email"`
	}
	previousCaller, previousProvider := currentHooks()
	calls := 0
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return lx025Clean, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	// Only a rule Go can decide: no call, and the finding stands.
	result, err := Validate(contact{Email: "not-an-email"}, NewValidateOptions().WithFieldRules(map[string]string{"email": "email"}))
	if err != nil || calls != 0 || result.Valid || len(result.Errors) != 1 {
		t.Fatalf("err %v, %d calls, valid %v, %d errors", err, calls, result.Valid, len(result.Errors))
	}

	// With a free-text rule beside it, the model is asked, finds nothing, and
	// the deterministic finding still fails the data.
	result, err = Validate(contact{Email: "not-an-email"},
		NewValidateOptions().WithFieldRules(map[string]string{"email": "email"}).WithRules("the address should be a work address"))
	if err != nil || calls != 1 || result.Valid || len(result.Errors) != 1 {
		t.Fatalf("err %v, %d calls, valid %v, %d errors", err, calls, result.Valid, len(result.Errors))
	}
}

const validateCompleteAnswer = `{"errors":[{"field":"note","message":"m"}],"warnings":[{"field":"note","message":"w"}],"info":[{"field":"note","message":"i"}],"confidence":0.9,"summary":"s"}`

// Validate works the verdict out from the lists of issues, so a list that is not
// in the answer is a list that was not reported, and an answer without the
// confidence or the summary is one that did not say how sure it is or what it
// found. None of them is read as an empty report, which would pass the data.
func TestValidateRefusesAnAnswerMissingAnyMemberItAsksFor(t *testing.T) {
	members := [][]any{
		{"errors"},
		{"warnings"},
		{"info"},
		{"confidence"},
		{"summary"},
		{"errors", 0, "message"},
		{"warnings", 0, "message"},
		{"info", 0, "message"},
	}
	refuseEachMissingMemberInOwnWords(t, validateCompleteAnswer, members, func(response string) (any, error) {
		result, err, _ := validateWith(t, response, lx025Rules())
		return result, err
	})
}

// A verdict, a correction, and the field, suggestion and explanation of an issue
// are not what Validate asks every call for: the model is asked for no verdict,
// a correction only when one is asked for, and an issue's field and suggestion
// may be null.
func TestValidateReadsAnAnswerWithoutWhatItDoesNotAlwaysAskFor(t *testing.T) {
	answer := `{"errors":[{"message":"m"}],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`
	result, err, _ := validateWith(t, answer, lx025Rules())
	if err != nil || len(result.Errors) != 1 || result.Errors[0].Message != "m" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestValidateRefusesANullWhereNothingCanBeNull(t *testing.T) {
	for name, body := range map[string]string{
		"confidence": strings.Replace(validateCompleteAnswer, `"confidence":0.9`, `"confidence":null`, 1),
		"summary":    strings.Replace(validateCompleteAnswer, `"summary":"s"`, `"summary":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			result, err, _ := validateWith(t, body, lx025Rules())
			wantKind(t, err, types.KindSchemaViolation)
			if !strings.Contains(err.Error(), name+" is null") || !reflect.ValueOf(result).IsZero() {
				t.Errorf("result = %+v, err = %v", result, err)
			}
		})
	}
}

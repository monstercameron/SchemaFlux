package ops

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Review round 12, Validate.

// lx12ID writes itself as text, as a UUID does: a byte array in Go and a
// string in JSON.
type lx12ID [4]byte

func (id lx12ID) MarshalText() ([]byte, error) { return []byte(hex.EncodeToString(id[:])), nil }
func (id *lx12ID) UnmarshalText(text []byte) error {
	_, err := hex.Decode(id[:], text)
	return err
}

type lx12User struct {
	ID    lx12ID `json:"id"`
	Email string `json:"email"`
}

type lx12Blob struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// lx12Stamped writes itself as JSON.
type lx12Stamped struct{ at string }

func (s lx12Stamped) MarshalJSON() ([]byte, error) { return json.Marshal(s.at) }
func (s *lx12Stamped) UnmarshalJSON(raw []byte) error {
	return json.Unmarshal(raw, &s.at)
}

type lx12Event struct {
	Name string      `json:"name"`
	When lx12Stamped `json:"when"`
}

// The schema for a correction is the schema of the value being validated, and
// a schema generated from a Go type cannot see how a type writes itself. A
// field that is a byte array in Go and a string in JSON, which is what a UUID
// is, was declared as an array of integers: the data the model was shown had a
// string there, the schema demanded numbers, and whatever the model sent back
// could not be read, so the whole validation was thrown away. This is new with
// the schema: nothing was declared before.
//
// A type whose JSON the generator cannot vouch for gets no declared schema,
// and its answer is held to the same rules by the reader.
func TestValidateDeclaresNoSchemaItCannotVouchFor(t *testing.T) {
	answer := func(corrected string) string {
		return `{"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"s","corrected":` + corrected + `}`
	}
	opts := NewValidateOptions().WithRules("the email has to be on example.com")

	t.Run("a field that writes itself as text", func(t *testing.T) {
		sent := answering(t, answer(`{"id":"0a0b0c0d","email":"sam@example.com"}`))
		result, err := Validate(lx12User{ID: lx12ID{1, 2, 3, 4}, Email: "sam@elsewhere.org"}, opts)
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if sent.opts.JSONSchema != nil {
			t.Errorf("a schema was declared for a type whose JSON it cannot see: %v", sent.opts.JSONSchema)
		}
		want := lx12User{ID: lx12ID{0x0a, 0x0b, 0x0c, 0x0d}, Email: "sam@example.com"}
		if result.Corrected == nil || *result.Corrected != want {
			t.Errorf("Corrected = %+v, want %+v", result.Corrected, want)
		}
		if !strings.Contains(sent.user, `"id":"01020304"`) {
			t.Errorf("the data did not reach the model as the type writes it:\n%s", sent.user)
		}
	})

	t.Run("a field that writes itself as JSON", func(t *testing.T) {
		sent := answering(t, answer(`{"name":"launch","when":"2026-10-03"}`))
		result, err := Validate(lx12Event{Name: "launch", When: lx12Stamped{at: "tomorrow"}}, opts)
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if sent.opts.JSONSchema != nil {
			t.Error("a schema was declared for a type with a custom JSON form")
		}
		if result.Corrected == nil || result.Corrected.When.at != "2026-10-03" {
			t.Errorf("Corrected = %+v", result.Corrected)
		}
	})

	t.Run("raw JSON", func(t *testing.T) {
		sent := answering(t, answer(`{"a":2}`))
		result, err := Validate(json.RawMessage(`{"a":1}`), opts)
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if sent.opts.JSONSchema != nil {
			t.Error("a schema was declared for raw JSON")
		}
		if result.Corrected == nil || string(*result.Corrected) != `{"a":2}` {
			t.Errorf("Corrected = %s", *result.Corrected)
		}
	})

	t.Run("bytes are a string in JSON", func(t *testing.T) {
		sent := answering(t, answer(`{"name":"key","data":"AQID"}`))
		result, err := Validate(lx12Blob{Name: "key", Data: []byte{9}}, opts)
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if schema := sent.opts.JSONSchema; schema != nil {
			corrected := schema["properties"].(map[string]any)["corrected"]
			encoded, _ := json.Marshal(corrected)
			if strings.Contains(string(encoded), `"integer"`) {
				t.Errorf("bytes are declared as a list of numbers, and JSON writes them as a string: %s", encoded)
			}
		}
		if result.Corrected == nil || !reflect.DeepEqual(result.Corrected.Data, []byte{1, 2, 3}) {
			t.Errorf("Corrected = %+v", result.Corrected)
		}
	})

	t.Run("a plain struct still gets its schema", func(t *testing.T) {
		sent := answering(t, lx025Clean)
		if _, err := Validate(lx025Subject, opts); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if sent.opts.JSONSchema == nil {
			t.Error("no schema was declared for a plain struct")
		}
	})

	// When no schema is sent the result says so.
	sent := answering(t, answer(`null`))
	result, err := Validate(lx12User{Email: "x"}, opts)
	if err != nil || sent.opts.JSONSchema != nil {
		t.Fatalf("err %v", err)
	}
	if declared, _ := result.Metadata["schema_declared"].(bool); declared {
		t.Error("the result says a schema was declared, and none was")
	}
}

func TestValidateCorrectedSchemaVouchesOnlyForPlainTypes(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		none   bool
	}{
		{"a plain struct", validateCorrectedSchema[lx025Order](), false},
		{"a string", validateCorrectedSchema[string](), false},
		{"a list of numbers", validateCorrectedSchema[[]int](), false},
		{"a struct with a text-marshalling field", validateCorrectedSchema[lx12User](), true},
		{"a struct with a JSON-marshalling field", validateCorrectedSchema[lx12Event](), true},
		{"raw JSON", validateCorrectedSchema[json.RawMessage](), true},
		{"a type that writes itself as text", validateCorrectedSchema[lx12ID](), true},
		{"a pointer to one", validateCorrectedSchema[*lx12ID](), true},
		{"a list of them", validateCorrectedSchema[[]lx12User](), true},
		{"any", validateCorrectedSchema[any](), true},
		{"an interface", validateCorrectedSchema[error](), true},
		{"a map of anything", validateCorrectedSchema[map[string]any](), true},
	}
	for _, tc := range cases {
		if (tc.schema == nil) != tc.none {
			t.Errorf("%s: no schema = %v, want %v (%v)", tc.name, tc.schema == nil, tc.none, tc.schema)
		}
	}
}

// With nothing to validate against there is nothing to pass. A call with no
// rules, no field rules and no hints came back valid with "checked 0 field
// rules" and no call made, which reads as an approval.
func TestValidateWithNothingToCheckAgainstIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		opts    ValidateOptions
		refused bool
		calls   int
	}{
		{"nothing at all", NewValidateOptions(), true, 0},
		{"blank rules", NewValidateOptions().WithRules("   "), true, 0},
		{"an empty map of field rules", NewValidateOptions().WithFieldRules(map[string]string{}), true, 0},
		{"an empty map of hints", NewValidateOptions().WithSchemaHints(map[string]string{}), true, 0},
		{"rules", NewValidateOptions().WithRules("be polite"), false, 1},
		{"a field rule Go decides", NewValidateOptions().WithFieldRules(map[string]string{"id": "required"}), false, 0},
		{"a field rule the model decides", NewValidateOptions().WithFieldRules(map[string]string{"note": "sounds polite"}), false, 1},
		{"hints only", NewValidateOptions().WithSchemaHints(map[string]string{"total": "in dollars"}), false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, lx025Clean)
			result, err := Validate(lx025Subject, tc.opts)
			if tc.refused {
				if types.KindOf(err) != types.KindConfiguration || result.Valid {
					t.Fatalf("valid %v, err %v; want a configuration error", result.Valid, err)
				}
			} else if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if sent.calls != tc.calls {
				t.Errorf("%d calls, want %d", sent.calls, tc.calls)
			}

			hybrid, err := ValidateHybrid(lx025Subject, tc.opts)
			if tc.refused != (err != nil) {
				t.Errorf("ValidateHybrid: verdict %v, err %v", hybrid.Verdict, err)
			}
		})
	}

	// The deterministic form refuses it too.
	if result, err := ValidateDeterministically(lx025Subject, NewValidateOptions()); err == nil {
		t.Errorf("ValidateDeterministically passed data against no rules: %v", result.Verdict)
	}
}

// A model that says "invalid" is not overruled and its report is not thrown
// away. With something listed, the data is invalid whatever FailOn says, and
// the result says why. With nothing listed there is no report to return, and
// it is refused.
func TestValidateKeepsAReportThatSaysInvalid(t *testing.T) {
	cases := []struct {
		name     string
		response string
		failOn   string
		refused  bool
		valid    bool
		warnings int
		info     int
		flagged  bool
	}{
		{"invalid, with a warning, warnings do not fail", `{"valid":false,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"confidence":0.9,"summary":"s"}`, "error", false, false, 1, 0, true},
		{"invalid, with info only", `{"valid":false,"errors":[],"warnings":[],"info":[` + lx025Issue + `],"confidence":0.9,"summary":"s"}`, "error", false, false, 0, 1, true},
		{"invalid, with a warning, warnings fail", `{"valid":false,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"confidence":0.9,"summary":"s"}`, "warning", false, false, 1, 0, false},
		{"invalid, with nothing listed", `{"valid":false,"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, "error", true, false, 0, 0, false},
		{"valid, with a warning", `{"valid":true,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"confidence":0.9,"summary":"s"}`, "error", false, true, 1, 0, false},
		{"no verdict, with a warning", `{"errors":[],"warnings":[` + lx025Issue + `],"info":[],"confidence":0.9,"summary":"s"}`, "error", false, true, 1, 0, false},
		{"no verdict, with info, info fails", `{"errors":[],"warnings":[],"info":[` + lx025Issue + `],"confidence":0.9,"summary":"s"}`, "info", false, false, 0, 1, false},
		{"invalid, with an error", `{"valid":false,"errors":[` + lx025Issue + `],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, "error", false, false, 0, 0, false},
		{"a null verdict, with a warning", `{"valid":null,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"confidence":0.9,"summary":"s"}`, "error", false, true, 1, 0, false},
		{"invalid, nothing listed, info fails", `{"valid":false,"errors":[],"warnings":[],"info":[],"confidence":0.9,"summary":"s"}`, "info", true, false, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answering(t, tc.response)
			result, err := Validate(lx025Subject, lx025Rules().WithFailOn(tc.failOn))
			if tc.refused {
				if types.KindOf(err) != types.KindSchemaViolation || !reflect.DeepEqual(result, ValidateResult[lx025Order]{}) {
					t.Fatalf("result %+v, err %v; want a refusal and nothing with it", result, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if result.Valid != tc.valid || len(result.Warnings) != tc.warnings || len(result.Info) != tc.info {
				t.Errorf("valid %v with %d warnings and %d info; want %v, %d, %d", result.Valid, len(result.Warnings), len(result.Info), tc.valid, tc.warnings, tc.info)
			}
			if flagged := result.Metadata["model_said_invalid"] == true; flagged != tc.flagged {
				t.Errorf("the result says the model's verdict decided it = %v, want %v", flagged, tc.flagged)
			}
		})
	}
}

// volunteeredRun is what a Validate call came back with, and what it sent.
type volunteeredRun struct {
	valid    bool
	warnings int
	metadata map[string]any
	sent     *assessCall
	err      error
}

// validateAskingForACorrection runs Validate with a correction asked for and the
// provider's answer scripted.
func validateAskingForACorrection[T any](t *testing.T, data T, response, failOn string) volunteeredRun {
	t.Helper()
	sent := answering(t, response)
	result, err := Validate(data, lx025Rules().WithAutoCorrect(true).WithFailOn(failOn))
	return volunteeredRun{valid: result.Valid, warnings: len(result.Warnings), metadata: result.Metadata, sent: sent, err: err}
}

// A verdict the model volunteers is read on the two paths where nothing stops it
// from being sent: a route that does not enforce the schema, which every other
// case above stands for, and a request with no schema at all. A request has none
// when a correction is asked for of a type whose JSON a schema cannot describe,
// and there the answer is held to the same rules and the volunteered verdict is
// still not overruled. Where the provider enforces the schema it cannot be sent,
// because the schema forbids members it does not declare.
func TestValidateReadsAVolunteeredVerdictWhenNoSchemaIsSent(t *testing.T) {
	const noList = `"errors":[],"warnings":[],"info":[],"corrected":null,"confidence":0.9,"summary":"s"`
	subjects := []struct {
		name string
		run  func(t *testing.T, response, failOn string) volunteeredRun
	}{
		{"a text-marshalling field", func(t *testing.T, response, failOn string) volunteeredRun {
			return validateAskingForACorrection(t, lx12User{Email: "x"}, response, failOn)
		}},
		{"a list of them", func(t *testing.T, response, failOn string) volunteeredRun {
			return validateAskingForACorrection(t, []lx12User{{Email: "x"}}, response, failOn)
		}},
	}
	for _, subject := range subjects {
		for _, tc := range []struct {
			name     string
			response string
			failOn   string
			valid    bool
			flagged  bool
		}{
			{"invalid over a warning that does not fail", `{"valid":false,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"corrected":null,"confidence":0.9,"summary":"s"}`, "error", false, true},
			{"invalid over a warning that fails", `{"valid":false,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"corrected":null,"confidence":0.9,"summary":"s"}`, "warning", false, false},
			{"valid over a warning", `{"valid":true,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"corrected":null,"confidence":0.9,"summary":"s"}`, "error", true, false},
			{"a null verdict over a warning", `{"valid":null,"errors":[],"warnings":[` + lx025Issue + `],"info":[],"corrected":null,"confidence":0.9,"summary":"s"}`, "error", true, false},
			{"no verdict over a warning", `{"errors":[],"warnings":[` + lx025Issue + `],"info":[],"corrected":null,"confidence":0.9,"summary":"s"}`, "error", true, false},
		} {
			t.Run(subject.name+", "+tc.name, func(t *testing.T) {
				run := subject.run(t, tc.response, tc.failOn)
				if run.err != nil {
					t.Fatalf("Validate: %v", run.err)
				}
				if len(run.sent.opts.JSONSchema) != 0 {
					t.Fatalf("a schema was sent: %v", run.sent.opts.JSONSchema)
				}
				if run.metadata["schema_declared"] != false {
					t.Errorf("the result says a schema was declared: %v", run.metadata["schema_declared"])
				}
				if run.valid != tc.valid || run.warnings != 1 {
					t.Errorf("valid %v with %d warnings; want %v and 1", run.valid, run.warnings, tc.valid)
				}
				if flagged := run.metadata["model_said_invalid"] == true; flagged != tc.flagged {
					t.Errorf("the result says the model's verdict decided it = %v, want %v", flagged, tc.flagged)
				}
			})
		}
		t.Run(subject.name+", invalid with nothing listed", func(t *testing.T) {
			run := subject.run(t, `{"valid":false,`+noList+`}`, "error")
			if len(run.sent.opts.JSONSchema) != 0 {
				t.Fatalf("a schema was sent: %v", run.sent.opts.JSONSchema)
			}
			if types.KindOf(run.err) != types.KindSchemaViolation || run.valid || run.warnings != 0 {
				t.Errorf("valid %v, %d warnings, err %v; want a refusal and nothing with it", run.valid, run.warnings, run.err)
			}
		})
	}
}

// A correction is laid over a copy of the data, so what it does not mention is
// kept, and a correction that changes nothing is none. An empty object used to
// come back as a zero value, and the documented `x = *result.Corrected` wiped
// the caller's data with it.
func TestValidateLaysACorrectionOverTheData(t *testing.T) {
	data := lx025Order{ID: "A-1", Total: 12.5, Note: "rush"}
	cases := []struct {
		name    string
		raw     string
		want    *lx025Order
		wantErr bool
	}{
		{"none sent", ``, nil, false},
		{"null", `null`, nil, false},
		{"an empty object", `{}`, nil, false},
		{"the data unchanged", `{"id":"A-1","total":12.5,"note":"rush"}`, nil, false},
		{"one field", `{"note":"please rush"}`, &lx025Order{ID: "A-1", Total: 12.5, Note: "please rush"}, false},
		{"every field", `{"id":"B-2","total":3,"note":"ok"}`, &lx025Order{ID: "B-2", Total: 3, Note: "ok"}, false},
		{"a field set to its zero value", `{"note":""}`, &lx025Order{ID: "A-1", Total: 12.5}, false},
		{"a field the data does not have", `{"note":"ok","priority":"high"}`, nil, true},
		{"an unrelated object", `{"answer":42}`, nil, true},
		{"a field of the wrong type", `{"total":"three"}`, nil, true},
		{"a string", `"the corrected order"`, nil, true},
		{"a list", `[{"id":"B-2"}]`, nil, true},
		{"not JSON", `{"id":`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			corrected, err := validateCorrection(json.RawMessage(tc.raw), data)
			if tc.wantErr {
				if types.KindOf(err) != types.KindSchemaViolation || corrected != nil {
					t.Fatalf("corrected %+v, err %v", corrected, err)
				}
				if strings.Contains(err.Error(), "three") || strings.Contains(err.Error(), "priority") {
					t.Errorf("the error repeats what the model wrote: %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(corrected, tc.want) {
				t.Fatalf("corrected = %+v, %v; want %+v", corrected, err, tc.want)
			}
		})
	}
	if data != (lx025Order{ID: "A-1", Total: 12.5, Note: "rush"}) {
		t.Errorf("the caller's data was changed: %+v", data)
	}

	// A value with a map in it: the correction does not write into the
	// caller's own map.
	type tagged struct {
		Tags map[string]string `json:"tags"`
	}
	original := tagged{Tags: map[string]string{"a": "1"}}
	corrected, err := validateCorrection(json.RawMessage(`{"tags":{"b":"2"}}`), original)
	if err != nil || corrected == nil {
		t.Fatalf("corrected %+v, err %v", corrected, err)
	}
	if len(original.Tags) != 1 || original.Tags["a"] != "1" {
		t.Errorf("the correction wrote into the caller's map: %v", original.Tags)
	}
}

// Explanations that were asked for reach the result, and the request says they
// were asked for. Passing "false" for it at the call was caught by nothing.
func TestValidateKeepsExplanationsThatWereAskedFor(t *testing.T) {
	body := lx025Answer(lx025Issue, "", "")
	for _, asked := range []bool{true, false} {
		sent := answering(t, body)
		result, err := Validate(lx025Subject, lx025Rules().WithIncludeExplanations(asked))
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if kept := result.Errors[0].Explanation != ""; kept != asked {
			t.Errorf("explanations asked for = %v, and one was kept = %v", asked, kept)
		}
		if said := strings.Contains(sent.user, "Give an explanation with every item."); said != asked {
			t.Errorf("explanations asked for = %v, and the message says so = %v", asked, said)
		}
	}
}

// The legacy form takes its verdict from the model, and a verdict of valid
// beside a list of issues is not one to pass on. And when the answer cannot be
// read nothing comes back with the error: a half-read `valid: true` did.
func TestValidateLegacyDoesNotPassDataWithIssues(t *testing.T) {
	cases := []struct {
		name     string
		response string
		valid    bool
		issues   int
		wantErr  bool
	}{
		{"valid, no issues", `{"valid":true,"issues":[],"confidence":0.9,"suggestions":[]}`, true, 0, false},
		{"invalid, with issues", `{"valid":false,"issues":["age below 18"],"confidence":0.8,"suggestions":["raise it"]}`, false, 1, false},
		{"valid claimed beside an issue", `{"valid":true,"issues":["email malformed"],"confidence":0.9,"suggestions":[]}`, false, 1, false},
		{"valid claimed beside two issues", `{"valid":true,"issues":["a","b"],"confidence":0.9,"suggestions":[]}`, false, 2, false},
		{"invalid with no issues", `{"valid":false,"issues":[],"confidence":0.9,"suggestions":[]}`, false, 0, false},
		{"issues that are not a list", `{"valid":true,"issues":"none","confidence":0.9}`, false, 0, true},
		{"no verdict", `{"issues":[],"confidence":0.9}`, false, 0, true},
		{"a verdict that is text", `{"valid":"yes","issues":[]}`, false, 0, true},
		{"an empty object", `{}`, false, 0, true},
		{"prose", `The data is valid.`, false, 0, true},
		{"only a verdict", `{"valid":true}`, false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answering(t, tc.response)
			result, err := ValidateLegacy(lx025Subject, "the note must be polite")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted: %+v", result)
				}
				if !reflect.DeepEqual(result, ValidationResult{}) {
					t.Errorf("something came back beside the error: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateLegacy: %v", err)
			}
			if result.Valid != tc.valid || len(result.Issues) != tc.issues {
				t.Errorf("valid %v with %d issues; want %v with %d", result.Valid, len(result.Issues), tc.valid, tc.issues)
			}
		})
	}
}

// Nothing of the answer is written to the log by Validate, Arbitrate or
// Decide, whether the answer is accepted or refused.
func TestTheAnswerIsNotLogged(t *testing.T) {
	const marker = "ZX-SECRET-IN-THE-ANSWER"
	run := map[string]func() error{
		"Validate, accepted": func() error {
			answering(t, `{"errors":[{"field":"note","message":"`+marker+`"}],"warnings":[],"info":[],"confidence":0.9,"summary":"`+marker+`"}`)
			_, err := Validate(lx025Subject, lx025Rules())
			return err
		},
		"Validate, refused": func() error {
			answering(t, `{"valid":false,"summary":"`+marker+`"}`)
			_, err := Validate(lx025Subject, lx025Rules())
			return err
		},
		"Arbitrate, refused": func() error {
			answering(t, `{"winner":"`+marker+`"}`)
			_, err := Arbitrate(lx024Vendors, ArbitrateOptions{Rules: []string{"cheap"}})
			return err
		},
		"Decide, refused": func() error {
			answering(t, `{"explanation":"`+marker+`"}`)
			_, _, err := Decide(context.Background(), "a ticket", routes())
			return err
		},
	}
	for name, call := range run {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			err := call()
			if err != nil && strings.Contains(err.Error(), marker) {
				t.Errorf("the error carries the answer: %v", err)
			}
			if logs() == "" {
				t.Fatal("the operation logged nothing, so this witnesses nothing")
			}
			if strings.Contains(logs(), marker) {
				t.Errorf("the log carries the answer:\n%s", logs())
			}
		})
	}
}

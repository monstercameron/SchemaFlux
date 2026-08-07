package ops

import (
	"encoding/json"
	"strings"
	"testing"
)

// The corpus A-003 asks for: bodies that are malformed as *responses* but carry
// perfectly good JSON. Sixteen copies of a four-line prefix strip handled
// exactly one of these shapes -- a response that is nothing but a fence, with
// the opening fence at the very start and the closing fence at the very end.
// Everything else parsed as a failure whose error blamed the JSON.
func TestExtractJSONRecoversRealModelResponses(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			"bare object",
			`{"name":"Ada"}`,
			`{"name":"Ada"}`,
		},
		{
			"the one shape the old code handled",
			"```json\n{\"name\":\"Ada\"}\n```",
			`{"name":"Ada"}`,
		},
		{
			"fence with no info string",
			"```\n{\"name\":\"Ada\"}\n```",
			`{"name":"Ada"}`,
		},
		{
			"prose before the fence",
			"Here is the JSON you asked for:\n\n```json\n{\"name\":\"Ada\"}\n```",
			`{"name":"Ada"}`,
		},
		{
			"prose after the fence",
			"```json\n{\"name\":\"Ada\"}\n```\n\nLet me know if you need anything else.",
			`{"name":"Ada"}`,
		},
		{
			"prose on both sides",
			"Sure!\n\n```json\n{\"name\":\"Ada\"}\n```\n\nHope that helps.",
			`{"name":"Ada"}`,
		},
		{
			"unfenced with leading prose",
			`The result is {"name":"Ada"} as requested.`,
			`{"name":"Ada"}`,
		},
		{
			"array at the top level",
			"```json\n[{\"id\":1},{\"id\":2}]\n```",
			`[{"id":1},{"id":2}]`,
		},
		{
			"unfenced array after prose",
			"Results: [1, 2, 3]",
			`[1, 2, 3]`,
		},
		{
			"uppercase info string",
			"```JSON\n{\"name\":\"Ada\"}\n```",
			`{"name":"Ada"}`,
		},
		{
			"a brace inside a string does not end the scan",
			`{"note":"a } and a { inside a string","ok":true}`,
			`{"note":"a } and a { inside a string","ok":true}`,
		},
		{
			"an escaped quote does not end the string",
			`{"note":"she said \"yes\" }","ok":true}`,
			`{"note":"she said \"yes\" }","ok":true}`,
		},
		{
			"nested objects close at the right brace",
			`{"a":{"b":{"c":1}}} trailing prose`,
			`{"a":{"b":{"c":1}}}`,
		},
		{
			"a non-JSON fence is skipped for the JSON one",
			"```python\nprint('hi')\n```\n\n```json\n{\"name\":\"Ada\"}\n```",
			`{"name":"Ada"}`,
		},
		{
			"whitespace and blank lines",
			"\n\n   ```json\n\n  {\"name\":\"Ada\"}\n\n```   \n",
			`{"name":"Ada"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSON(tc.input)
			if got != tc.want {
				t.Errorf("extractJSON =\n  %q\nwant\n  %q", got, tc.want)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("the extracted value is not valid JSON: %q", got)
			}
		})
	}
}

// Every case above has to survive the actual parse path, not just the
// extractor, because that is what the operations call.
func TestParseJSONAcceptsTheRecoverableCorpus(t *testing.T) {
	type person struct {
		Name string `json:"name"`
	}

	bodies := []string{
		`{"name":"Ada"}`,
		"```json\n{\"name\":\"Ada\"}\n```",
		"Here is the JSON:\n```json\n{\"name\":\"Ada\"}\n```",
		"```json\n{\"name\":\"Ada\"}\n```\nAnything else?",
		`The answer: {"name":"Ada"}`,
		"```\n{\"name\":\"Ada\"}\n```",
		"```python\nx=1\n```\n```json\n{\"name\":\"Ada\"}\n```",
		"   \n{\"name\":\"Ada\"}\n   ",
	}

	for _, body := range bodies {
		var target person
		if err := ParseJSON(body, &target); err != nil {
			t.Errorf("ParseJSON(%q) = %v", body, err)
			continue
		}
		if target.Name != "Ada" {
			t.Errorf("ParseJSON(%q) produced %+v", body, target)
		}
	}
}

// Truncation is a different failure from malformation, and the extractor must
// not disguise it. A cut-off body comes back as far as it got, so the decoder's
// error describes the real problem.
func TestTruncatedBodiesReachTheDecoder(t *testing.T) {
	cases := []string{
		`{"name":"Ada", "age":`,
		"```json\n{\"name\":\"Ada\", \"age\":",
		`[{"id":1},{"id":`,
	}

	for _, body := range cases {
		var target map[string]any
		err := ParseJSON(body, &target)
		if err == nil {
			t.Errorf("ParseJSON(%q) succeeded; a truncated body is not valid", body)
		}
	}
}

// Nothing recoverable stays nothing recoverable: the extractor does not invent
// a value, and the error still says the body was prose rather than JSON.
func TestUnrecoverableBodiesStayErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"whitespace", "   \n  "},
		{"prose only", "I could not complete that request."},
		{"html error page", "<html><body>502 Bad Gateway</body></html>"},
		{"refusal", "Sorry, I can't help with that."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var target struct {
				Name string `json:"name"`
			}
			if err := ParseJSON(tc.body, &target); err == nil {
				t.Error("ParseJSON accepted a body with no JSON in it")
			}
		})
	}
}

// The error must still carry no payload. A wider extractor that starts
// reporting what it found would undo F-033.
func TestExtractionErrorsCarryNoPayload(t *testing.T) {
	const marker = "SSN-123-45-6789"

	var target struct {
		Name string `json:"name"`
	}
	err := ParseJSON(`Here is the record for `+marker+` but I cannot format it`, &target)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("the error carries the payload: %v", err)
	}
}

// The strict parser sits on top of the same extraction, so a fenced body with
// prose around it reaches the field-name check rather than failing before it.
func TestParseJSONStrictSeesThroughFences(t *testing.T) {
	type verdict struct {
		Valid bool `json:"valid"`
	}

	var ok verdict
	if err := ParseJSONStrict("Here you go:\n```json\n{\"valid\":true}\n```", &ok); err != nil {
		t.Fatalf("ParseJSONStrict = %v, want nil", err)
	}
	if !ok.Valid {
		t.Error("the value did not survive extraction")
	}

	// And an answer about something else is still rejected, fenced or not.
	var wrong verdict
	if err := ParseJSONStrict("```json\n{\"temperature\":21}\n```", &wrong); err == nil {
		t.Error("ParseJSONStrict accepted a body sharing no field with the target")
	}
}

// oldCleanJSON is the implementation this replaced, kept here as the witness.
// It is not called by anything else.
func oldCleanJSON(input string) string {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "```json") {
		input = strings.TrimPrefix(input, "```json")
		input = strings.TrimSuffix(input, "```")
	} else if strings.HasPrefix(input, "```") {
		input = strings.TrimPrefix(input, "```")
		input = strings.TrimSuffix(input, "```")
	}
	return strings.TrimSpace(input)
}

// How much of the corpus the old strip actually handled. This is the argument
// for A-003 stated as a number rather than a claim: if a future change makes
// the new extractor no better than the old one, this fails.
func TestTheOldStripHandledAlmostNoneOfIt(t *testing.T) {
	corpus := []string{
		"```json\n{\"name\":\"Ada\"}\n```",
		"```\n{\"name\":\"Ada\"}\n```",
		"Here is the JSON you asked for:\n\n```json\n{\"name\":\"Ada\"}\n```",
		"```json\n{\"name\":\"Ada\"}\n```\n\nLet me know if you need anything else.",
		"Sure!\n\n```json\n{\"name\":\"Ada\"}\n```\n\nHope that helps.",
		`The result is {"name":"Ada"} as requested.`,
		"```JSON\n{\"name\":\"Ada\"}\n```",
		"```python\nprint('hi')\n```\n\n```json\n{\"name\":\"Ada\"}\n```",
	}

	var oldOK, newOK int
	for _, body := range corpus {
		if json.Valid([]byte(oldCleanJSON(body))) {
			oldOK++
		}
		if json.Valid([]byte(extractJSON(body))) {
			newOK++
		}
	}

	if newOK != len(corpus) {
		t.Errorf("the new extractor recovered %d of %d", newOK, len(corpus))
	}
	if oldOK >= newOK {
		t.Errorf("the old strip recovered %d of %d and the new one %d; A-003 bought nothing",
			oldOK, len(corpus), newOK)
	}
	t.Logf("old strip: %d/%d recovered; extractJSON: %d/%d", oldOK, len(corpus), newOK, len(corpus))
}

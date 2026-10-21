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

// The caller reader sits on top of the same extraction, so a fenced body with
// prose around it reaches the field-name check rather than failing before it.
func TestDecodeCallerAnswerSeesThroughFences(t *testing.T) {
	type verdict struct {
		Valid bool `json:"valid"`
	}

	var ok verdict
	if err := decodeCallerAnswer("probe", "Here you go:\n```json\n{\"valid\":true}\n```", &ok); err != nil {
		t.Fatalf("decodeCallerAnswer = %v, want nil", err)
	}
	if !ok.Valid {
		t.Error("the value did not survive extraction")
	}

	// And an answer about something else is still rejected, fenced or not.
	var wrong verdict
	if err := decodeCallerAnswer("probe", "```json\n{\"temperature\":21}\n```", &wrong); err == nil {
		t.Error("decodeCallerAnswer accepted a body sharing no field with the target")
	}
}

// Shapes that a prefix strip, which was all the extraction there used to be,
// does not recover. extractJSON has to return valid JSON for every one.
func TestExtractJSONRecoversEveryShapeAPrefixStripMisses(t *testing.T) {
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

	for _, body := range corpus {
		if got := extractJSON(body); !json.Valid([]byte(got)) {
			t.Errorf("extractJSON(%q) = %q, which is not valid JSON", body, got)
		}
	}
}

// The scan for the next fence resumed at an offset worked out from the trimmed
// info string, so a non-JSON fence whose info line had trailing space or a
// carriage return left the scan inside the block: its closing fence was read as
// the opening of the next one, and the real JSON block was skipped.
func TestFenceSkipUsesTheLineThatWasActuallyConsumed(t *testing.T) {
	const payload = `{"a":1}`
	cases := []struct {
		name string
		body string
	}{
		{"three trailing spaces", "```text   \n[\"decoy\"]\n```\n```json\n" + payload + "\n```"},
		{"a trailing carriage return", "```text\r\nhello\r\n```\r\n```json\r\n" + payload + "\r\n```"},
		{"leading spaces on the info string", "```   text\nhello\n```\n```json\n" + payload + "\n```"},
		{"many trailing spaces", "```text" + strings.Repeat(" ", 20) + "\n{\"decoy\": 1}\n```\n```json\n" + payload + "\n```"},
		{"a tab and spaces", "```python \t \nd = {1: 2}\n```\n```json\n" + payload + "\n```"},
		{"an upper-case info string", "```TEXT   \nhello\n```\n```json\n" + payload + "\n```"},
		{"a brace inside the skipped block", "```text   \n{\"decoy\": true}\n```\n```json\n" + payload + "\n```"},
		{"prose between the blocks", "```text   \nhello\n```\nAnd the answer:\n```json\n" + payload + "\n```"},
		{"two skipped blocks", "```a   \nx\n```\n```b   \ny\n```\n```json\n" + payload + "\n```"},
		{"one trailing space still works", "```text \nhello\n```\n```json\n" + payload + "\n```"},
		{"a clean info string still works", "```python\nprint(1)\n```\n```json\n" + payload + "\n```"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSON(tc.body); got != payload {
				t.Errorf("extractJSON = %q, want %q", got, payload)
			}
		})
	}
}

// An unterminated fence labelled with another language is not the payload, the
// same as a terminated one; the truncated JSON fence still is.
func TestAnUnterminatedFenceIsHeldToTheSameRuleAsATerminatedOne(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		isJSON bool
	}{
		{"an unterminated json fence", "```json\n{\"a\": 1, \"b\":", true},
		{"an unterminated unlabelled fence with an object", "```\n{\"a\": 1, \"b\":", true},
		{"an unterminated fence of another language that starts with a brace", "```javascript\n{ const x = 1;", false},
		{"an unterminated python fence", "```python\n{'a': 1}", false},
		{"an unterminated fence with an array", "```\n[1, 2,", true},
		{"an unterminated unlabelled fence with prose", "```\nnot json at all", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content, found := fencedBlock(tc.body)
			if found != tc.isJSON {
				t.Errorf("fencedBlock found = %v (%q), want %v", found, content, tc.isJSON)
			}
		})
	}
}

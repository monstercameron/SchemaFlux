package ops

import (
	"strings"
	"testing"
)

// A parse error is logged by every caller, so it must not carry the caller's
// data. The payload is often the record being extracted or redacted, and
// embedding it copies user content into every log aggregator the operator runs.
func TestParseJSONErrorDoesNotLeakThePayload(t *testing.T) {
	secrets := []struct {
		name    string
		payload string
		secret  string
	}{
		{"ssn", `{"ssn": "123-45-6789", broken`, "123-45-6789"},
		{"email", `not json at all, from alice@example.com`, "alice@example.com"},
		{"card", `{"card":"4111111111111111"`, "4111111111111111"},
		// This is the payload the test proves does NOT reach an error string.
		// It has to look like a credential to be worth asserting about.
		// secret-scan: allow
		{"bearer", `Bearer sk-live-abcdefghijklmnop`, "sk-live-abcdefghijklmnop"},
		{"prose_with_name", `I could not process the record for Jane Q. Public.`, "Jane Q. Public"},
		{"html_with_path", `<html>error at /home/mreca/secrets/db.sqlite</html>`, "/home/mreca/secrets/db.sqlite"},
	}

	for _, tc := range secrets {
		t.Run(tc.name, func(t *testing.T) {
			var target struct {
				Field string `json:"field"`
			}
			err := ParseJSON(tc.payload, &target)
			if err == nil {
				t.Fatal("expected a parse error")
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Errorf("the error reproduced the payload: %v", err)
			}
		})
	}
}

// It must still be diagnosable: the size and a short prefix identify the shape
// without reproducing the content.
func TestParseJSONErrorRemainsDiagnosable(t *testing.T) {
	var target struct{}
	err := ParseJSON("<html><body>502 Bad Gateway</body></html>", &target)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	message := err.Error()
	if !strings.Contains(message, "bytes") {
		t.Errorf("the error should report the response size: %v", err)
	}
	if !strings.Contains(message, "markup") {
		t.Errorf("the error should classify the response shape: %v", err)
	}
}

// The prefix is bounded, so a large body cannot be smuggled out through it.
func TestParseJSONErrorPrefixIsBounded(t *testing.T) {
	long := strings.Repeat("SENSITIVE", 500)
	var target struct{}
	err := ParseJSON(long, &target)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if len(err.Error()) > 200 {
		t.Errorf("error message is %d chars; it must stay bounded", len(err.Error()))
	}
	if strings.Contains(err.Error(), "SENSITIVE") {
		t.Errorf("no payload content may survive: %v", err)
	}
}

// preview must not split a multi-byte rune.
func TestDescribeShapeIsRuneSafe(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("日", 100),
		strings.Repeat("🔒", 100),
		strings.Repeat("é", 100),
		"短い",
		"",
	} {
		got := describeShape(input)
		if !isValidUTF8(got) {
			t.Errorf("describeShape(%q) produced invalid UTF-8: %q", input[:min(len(input), 12)], got)
		}
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The classification must carry no caller data whatsoever.
func TestDescribeShapeCarriesNoContent(t *testing.T) {
	cases := []struct{ input, want string }{
		{`{"ssn":"123-45-6789"}`, "json object"},
		{`[1,2,3]`, "json array"},
		{"<html>secret path /etc/passwd</html>", "markup, likely an error page"},
		{"```json\n{\"k\":1}\n```", "fenced block"},
		{"I cannot help with that, Jane Public.", "prose"},
		{"   ", "empty"},
		{"", "empty"},
	}
	for _, tc := range cases {
		got := describeShape(tc.input)
		if got != tc.want {
			t.Errorf("describeShape(%.20q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// lx020Corpus is the shared UTF-8 corpus of internal/ops, repeated here because
// that one is unexported and this package tests through the root API. The
// categories are the ones that break a byte index: two, three and four bytes a
// character, several runes that display as one, and right-to-left text.
var lx020Corpus = []struct{ name, text string }{
	{"ascii", "Hello, world"},
	{"accented latin", "Montréal café"},
	{"currency", "Total: 45€ or £38"},
	{"cjk", "東京都渋谷区"},
	{"japanese sentence", "これはテストです"},
	{"emoji", "ship it 🚀"},
	{"emoji zwj", "family 👨‍👩‍👧"},
	{"combining marks", "e\u0301galite\u0301"},
	{"rtl arabic", "مرحبا بالعالم"},
	{"rtl hebrew", "שלום עולם"},
	{"mixed", "café 東京 🚀 שלום"},
}

func lx020Spans(t *testing.T, values ...string) string {
	t.Helper()
	type span struct {
		Text     string `json:"text"`
		Category string `json:"category"`
	}
	spans := make([]span, 0, len(values))
	for _, value := range values {
		spans = append(spans, span{Text: value, Category: "name"})
	}
	body, err := json.Marshal(map[string]any{"spans": spans})
	if err != nil {
		t.Fatalf("marshal the scripted answer: %v", err)
	}
	return string(body)
}

// LX-020 through the exported API and the real OpenAI provider, over the UTF-8
// corpus. The first version of this check called two internal functions and put
// an ASCII secret after the corpus text, so no corpus entry was ever the secret
// or came after it.
func TestIntegrationRedactLLMOverTheUTF8Corpus(t *testing.T) {
	for _, tc := range lx020Corpus {
		stars := strings.Repeat("*", utf8.RuneCountInString(tc.text))

		layouts := []struct {
			name     string
			document string
			reported []string
			want     string
			spans    int
		}{
			{"the corpus text is the secret", "Before " + tc.text + " after", []string{tc.text}, "Before " + stars + " after", 1},
			{"the secret is at the start", tc.text + " then more", []string{tc.text}, stars + " then more", 1},
			{"the secret is at the end", "At the end: " + tc.text, []string{tc.text}, "At the end: " + stars, 1},
			{"the secret appears twice", tc.text + " | and again | " + tc.text, []string{tc.text}, stars + " | and again | " + stars, 2},
			{"corpus text on both sides of an ASCII secret", tc.text + " <ada@example.com> " + tc.text, []string{"ada@example.com"},
				tc.text + " <***************> " + tc.text, 1},
		}
		for _, layout := range layouts {
			t.Run(tc.name+"/"+layout.name, func(t *testing.T) {
				_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(lx020Spans(t, layout.reported...)))

				result, err := schemaflux.RedactLLM(layout.document, schemaflux.NewRedactLLMOptions())
				if err != nil {
					t.Fatalf("RedactLLM: %v", err)
				}
				if result.Text != layout.want {
					t.Fatalf("Text = %q, want %q", result.Text, layout.want)
				}
				if !utf8.ValidString(result.Text) {
					t.Fatalf("the output is not valid UTF-8: %q", result.Text)
				}
				for _, secret := range layout.reported {
					if strings.Contains(result.Text, secret) {
						t.Fatalf("the secret is still in the output: %q", result.Text)
					}
				}
				if len(result.Spans) != layout.spans {
					t.Fatalf("Spans = %+v, want %d", result.Spans, layout.spans)
				}
				// Offsets are in characters and select exactly what was masked.
				for _, span := range result.Spans {
					if got := string([]rune(layout.document)[span.Start:span.End]); got != span.Original || got != layout.reported[0] {
						t.Fatalf("[]rune(document)[%d:%d] = %q, Original = %q, reported %q", span.Start, span.End, got, span.Original, layout.reported[0])
					}
				}

				if len(*captured) != 1 {
					t.Fatalf("%d requests reached the provider, want 1", len(*captured))
				}
				text, _ := (*captured)[0]["text"].(map[string]any)
				format, _ := text["format"].(map[string]any)
				if format["type"] != "json_schema" || format["strict"] != true {
					t.Fatalf("expected a strict schema on the wire, got %v", format)
				}
				schema, _ := format["schema"].(map[string]any)
				if problem := llm.StrictSchemaProblem(schema); problem != "" {
					t.Fatalf("the schema on the wire breaks a strict-mode rule: %s", problem)
				}
			})
		}
	}
}

// What cannot be redacted returns no text and a classified error, through the
// exported API. The last case is the documented limit: the comparison is not
// Unicode-normalised, so a value quoted in composed form against text written
// with combining marks is not found, and the call refuses.
func TestIntegrationRedactLLMFailsClosed(t *testing.T) {
	const document = "Zoë's mail is zoe@example.com."
	cases := []struct {
		name     string
		document string
		body     string
		want     error
	}{
		{"a value that is not in the text", document, `{"spans":[{"text":"zoe@example.org","category":"email"}]}`, schemaflux.ErrInvariantViolation},
		{"one value found and one not", document, `{"spans":[{"text":"zoe@example.com","category":"email"},{"text":"Zed","category":"name"}]}`, schemaflux.ErrInvariantViolation},
		{"a span with no text", document, `{"spans":[{"text":"","category":"email"}]}`, schemaflux.ErrSchemaViolation},
		{"no spans list", document, `{"found":false}`, schemaflux.ErrSchemaViolation},
		{"a null spans list", document, `{"spans":null}`, schemaflux.ErrSchemaViolation},
		{"prose", document, `I found one email address.`, schemaflux.ErrMalformedOutput},
		{"composed quoted against decomposed", "Signed, e\u0301galite\u0301 team", `{"spans":[{"text":"égalité","category":"name"}]}`, schemaflux.ErrInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			result, err := schemaflux.RedactLLM(tc.document, schemaflux.NewRedactLLMOptions())
			if err == nil {
				t.Fatalf("accepted the response; Text = %q", result.Text)
			}
			if result.Text != "" || result.Original != "" || len(result.Spans) != 0 {
				t.Fatalf("a failed redaction returned something a caller could use: %+v", result)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("kind = %v, want %v (%v)", schemaflux.KindOf(err), tc.want, err)
			}
			for _, secret := range []string{"zoe@example.com", "zoe@example.org", "galit"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error repeats text from the document or the model: %v", err)
				}
			}
		})
	}

	t.Run("a provider failure", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))
		result, err := schemaflux.RedactLLM(document, schemaflux.NewRedactLLMOptions())
		if err == nil || result.Text != "" {
			t.Fatalf("err = %v, Text = %q", err, result.Text)
		}
	})
}

// ExampleRedactLLM shows the call a caller writes and what comes back: the
// text with every copy of each reported value masked, and where the masks are.
// It runs under go test with a scripted provider: no credential, no spend.
func ExampleRedactLLM() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"spans": [{"text": "zoë@example.com", "category": "email"}, {"text": "Zoë Brandt", "category": "name"}]}`))

	const note = "Zoë Brandt asked us to write to zoë@example.com. Signed, ZOË BRANDT"

	result, err := schemaflux.RedactLLM(note, schemaflux.NewRedactLLMOptions())
	if err != nil {
		fmt.Println("nothing was redacted, and no text came back:", err)
		return
	}

	fmt.Println(result.Text)
	for _, span := range result.Spans {
		// Offsets count characters, so they index []rune(note).
		fmt.Printf("%d-%d %s\n", span.Start, span.End, span.Category)
	}

	// Output:
	// ********** asked us to write to ***************. Signed, **********
	// 0-10 name
	// 32-47 email
	// 57-67 name
}

// ExampleRedactLLM_failsClosed is the counterpart: a value the model reports
// that is not in the text cannot be masked, so the call returns an error and
// no text, where it once returned the original text and a nil error.
func ExampleRedactLLM_failsClosed() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"spans": [{"text": "zoe@example.org", "category": "email"}]}`))

	result, err := schemaflux.RedactLLM("Write to zoe@example.com.", schemaflux.NewRedactLLMOptions())

	fmt.Println("error is nil:", err == nil)
	fmt.Printf("text: %q\n", result.Text)

	// Output:
	// error is nil: false
	// text: ""
}

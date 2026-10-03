package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// redactWith runs RedactLLM against a scripted model response.
func redactWith(t *testing.T, text, response string, opts RedactLLMOptions) (RedactLLMResult, error) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return response, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	return RedactLLM(context.Background(), text, opts)
}

// LX-020. RedactLLM located each span with strings.Index, which returns a byte
// offset, and then applied that offset to []rune(text). The two agree only when
// everything before the span is ASCII. With one accented letter in front, the
// mask shifts right by a byte's worth of characters: the start of the secret
// stays readable, a character after it is destroyed, and Spans[].Original still
// reports the right text, so the result looks done.
func TestRedactLLMMasksTheRightCharactersInNonASCIIText(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		secret string
		want   string
	}{
		{"ascii only", "Mail john@email.com now", "john@email.com", "Mail ************** now"},
		{"one accented letter before", "Zoë's mail is zoe@example.com.", "zoe@example.com", "Zoë's mail is ***************."},
		{"several accented letters before", "Écrivez à François: francois@example.fr merci", "francois@example.fr", "Écrivez à François: ******************* merci"},
		{"cjk before", "連絡先は tanaka@example.jp です", "tanaka@example.jp", "連絡先は ***************** です"},
		{"emoji before", "📞 call 555-0142 today", "555-0142", "📞 call ******** today"},
		{"the secret itself is non-ASCII", "Kunde: Jürgen Müller, Berlin", "Jürgen Müller", "Kunde: *************, Berlin"},
		{"non-ASCII after only", "Mail a@b.co — très bien", "a@b.co", "Mail ****** — très bien"},
		{"secret at the very start", "José García signed", "José García", "*********** signed"},
		{"secret at the very end", "Signé: Renée Lefèvre", "Renée Lefèvre", "Signé: *************"},
		{"cyrillic", "Адрес: ivan@example.ru, спасибо", "ivan@example.ru", "Адрес: ***************, спасибо"},
		{"combining mark before", "Café owner: 078-05-1120", "078-05-1120", "Café owner: ***********"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"spans":[{"text":` + quoteJSON(tc.secret) + `,"start":0,"category":"pii"}]}`
			result, err := redactWith(t, tc.text, response, NewRedactLLMOptions())
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != tc.want {
				t.Fatalf("Text = %q, want %q", result.Text, tc.want)
			}
			if strings.Contains(result.Text, tc.secret) {
				t.Fatalf("the secret is still in the output: %q", result.Text)
			}
			// The offsets a caller is handed are in characters, and must
			// select exactly the text that was masked.
			if len(result.Spans) != 1 {
				t.Fatalf("Spans = %+v, want one", result.Spans)
			}
			span := result.Spans[0]
			if got := string([]rune(tc.text)[span.Start:span.End]); got != tc.secret {
				t.Fatalf("[]rune(text)[Start:End] = %q, want %q", got, tc.secret)
			}
		})
	}
}

// A value the model named as sensitive is sensitive everywhere it appears. It
// used to be masked once per mention by the model, so an address repeated in a
// signature stayed readable.
func TestRedactLLMMasksEveryOccurrenceOfAReportedValue(t *testing.T) {
	text := "Write to ana@example.com. Again: ana@example.com. Signature: ana@example.com"
	result, err := redactWith(t, text, `{"spans":[{"text":"ana@example.com","start":9,"category":"email"}]}`, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if strings.Contains(result.Text, "ana@example.com") {
		t.Fatalf("an occurrence was left unmasked: %q", result.Text)
	}
	if len(result.Spans) != 3 || result.Categories["email"] != 3 {
		t.Fatalf("Spans = %d, Categories = %v, want three email spans", len(result.Spans), result.Categories)
	}
}

// A model that quotes the value with different spacing or capitalisation has
// still found something. It used to be treated as a value that is not in the
// document and dropped, leaving the secret in place with no error.
func TestRedactLLMLocatesAValueQuotedWithDifferentSpacingOrCase(t *testing.T) {
	cases := []struct {
		name, text, reported, want string
	}{
		{"collapsed spaces", "Call 555  014   2298 today", "555 014 2298", "Call *************** today"},
		{"line break in the text", "Name: Dana\nKim, age 40", "Dana Kim", "Name: ********, age 40"},
		{"different case", "Email DANA@EXAMPLE.COM now", "dana@example.com", "Email **************** now"},
		{"tab in the text", "SSN:\t078-05-1120", "078-05-1120", "SSN:\t***********"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := redactWith(t, tc.text, `{"spans":[{"text":`+quoteJSON(tc.reported)+`,"start":0,"category":"pii"}]}`, NewRedactLLMOptions())
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != tc.want {
				t.Fatalf("Text = %q, want %q", result.Text, tc.want)
			}
		})
	}
}

// What cannot be redacted must not come back looking redacted. Each of these
// used to return a nil error, and several returned the original text in Text.
func TestRedactLLMFailsClosed(t *testing.T) {
	const text = "Zoë's mail is zoe@example.com."
	cases := []struct {
		name     string
		response string
	}{
		{"a span whose text is not in the document", `{"spans":[{"text":"555-000-1234","start":0,"category":"phone"}]}`},
		{"one locatable span and one that is not", `{"spans":[{"text":"zoe@example.com","start":14,"category":"email"},{"text":"not there","start":0,"category":"name"}]}`},
		{"a span with no text", `{"spans":[{"text":"","start":3,"category":"email"}]}`},
		{"a span with only whitespace", `{"spans":[{"text":"   ","start":3,"category":"email"}]}`},
		{"not JSON", `sorry, I cannot help with that`},
		{"no spans field", `{"note":"nothing found"}`},
		{"spans is null", `{"spans":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := redactWith(t, text, tc.response, NewRedactLLMOptions())
			if err == nil {
				t.Fatalf("accepted the response; Text = %q", result.Text)
			}
			if result.Text != "" {
				t.Fatalf("a failed redaction returned text a caller could use: %q", result.Text)
			}
		})
	}

	// A provider failure is the same: no text back.
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := RedactLLM(context.Background(), text, NewRedactLLMOptions())
	if err == nil || result.Text != "" {
		t.Fatalf("a provider failure returned err=%v and Text=%q", err, result.Text)
	}
}

// An empty span list is an answer: the model looked and found nothing.
// Each span carries the text to redact and a category, and the list itself is
// the answer: a response that leaves any of them out is refused by position
// and not read as a text of nothing or a category of none.
func TestRedactLLMRefusesAnAnswerMissingARequiredMember(t *testing.T) {
	const text = "Zoë's mail is zoe@example.com."
	const complete = `{"spans":[{"text":"zoe@example.com","category":"email"}]}`
	members := [][]any{{"spans"}, {"spans", 0, "text"}, {"spans", 0, "category"}}
	refuseEachMissingMember(t, complete, members, func(response string) (any, error) {
		return redactWith(t, text, response, NewRedactLLMOptions())
	})
}

// A category of none, said out loud, is an answer, and a null for either member
// of a span is not.
func TestRedactLLMAcceptsAnEmptyCategoryAndRefusesANullMember(t *testing.T) {
	const text = "Zoë's mail is zoe@example.com."
	result, err := redactWith(t, text, `{"spans":[{"text":"zoe@example.com","category":""}]}`, NewRedactLLMOptions().WithCategories([]string{"email"}))
	if err != nil || result.Text == text {
		t.Errorf("an empty category: Text = %q, err = %v", result.Text, err)
	}
	for name, response := range map[string]string{
		"spans[0].text":     `{"spans":[{"text":null,"category":"email"}]}`,
		"spans[0].category": `{"spans":[{"text":"zoe@example.com","category":null}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := redactWith(t, text, response, NewRedactLLMOptions())
			wantKind(t, err, types.KindSchemaViolation)
			if !strings.Contains(err.Error(), name+" is null") || result.Text != "" {
				t.Errorf("Text = %q, err = %v", result.Text, err)
			}
		})
	}
}

func TestRedactLLMAcceptsAnEmptySpanList(t *testing.T) {
	const text = "Nothing sensitive here."
	result, err := redactWith(t, text, `{"spans":[]}`, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if result.Text != text || len(result.Spans) != 0 {
		t.Fatalf("Text = %q, Spans = %v", result.Text, result.Spans)
	}
}

// Two overlapping spans used to be merged into one whose category was the two
// names joined with a comma, which the category check then refused as a
// category nobody asked for, even when both had been requested. The merged
// span also lost its Original.
func TestRedactLLMMergesOverlappingSpans(t *testing.T) {
	const text = "Reach Dana Kim <dana@example.com> today"
	response := `{"spans":[{"text":"Dana Kim <dana@example.com>","start":6,"category":"name"},{"text":"dana@example.com","start":16,"category":"email"}]}`

	result, err := redactWith(t, text, response, NewRedactLLMOptions().WithCategories([]string{"name", "email"}))
	if err != nil {
		t.Fatalf("RedactLLM refused two requested categories that overlapped: %v", err)
	}
	if strings.Contains(result.Text, "dana") || strings.Contains(result.Text, "Dana") {
		t.Fatalf("part of the overlapping region is unmasked: %q", result.Text)
	}
	if len(result.Spans) != 1 {
		t.Fatalf("Spans = %+v, want the two merged into one", result.Spans)
	}
	if got := result.Spans[0].Original; got != "Dana Kim <dana@example.com>" {
		t.Fatalf("the merged span's Original = %q, want the text it covers", got)
	}

	// A category that was not requested is still refused, merged or not.
	if _, err := redactWith(t, text, response, NewRedactLLMOptions().WithCategories([]string{"email"})); err == nil {
		t.Fatal("a span in a category that was not requested was applied")
	}
}

func TestRedactLLMShowFirstAndLastCountCharacters(t *testing.T) {
	const text = "Kunde: Jürgen Müller, Berlin"
	response := `{"spans":[{"text":"Jürgen Müller","start":7,"category":"name"}]}`
	result, err := redactWith(t, text, response, NewRedactLLMOptions().WithShowFirst(2).WithShowLast(2))
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if want := "Kunde: Jü*********er, Berlin"; result.Text != want {
		t.Fatalf("Text = %q, want %q", result.Text, want)
	}
}

// The response shape is declared to the provider, so a provider with structured
// output cannot return prose or a span without its text.
func TestRedactLLMDeclaresAStrictSchema(t *testing.T) {
	var seen types.OpOptions
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, _ string, opt types.OpOptions) (string, error) {
		seen = opt
		return `{"spans":[]}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	if _, err := RedactLLM(context.Background(), "some text", NewRedactLLMOptions()); err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if problem := llm.StrictSchemaProblem(seen.JSONSchema); problem != "" {
		t.Fatalf("the schema breaks a strict-mode rule, or is missing: %s", problem)
	}
}

func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

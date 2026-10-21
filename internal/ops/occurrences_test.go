package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LX-020, second pass. The first pass located a reported value exactly and
// looked for a differently cased or spaced copy only when no exact copy existed,
// so a second copy that differed stayed readable with a nil error.
func TestRedactLLMMasksCopiesThatDifferInCaseOrSpacing(t *testing.T) {
	cases := []struct {
		name, text, reported, want string
	}{
		{"second copy in another case", "Dana Kim wrote. Signed, DANA KIM", "Dana Kim", "******** wrote. Signed, ********"},
		{"second copy with two spaces", "Dana Kim and dana  kim", "Dana Kim", "******** and *********"},
		{"second copy broken across a line", "Dana Kim called.\nRegards,\nDana\nKim", "Dana Kim", "******** called.\nRegards,\n********"},
		{"address in another case", "ana@example.com, cc Ana@Example.com", "ana@example.com", "***************, cc ***************"},
		{"a no-break space in the text", "Dana\u00a0Kim signed", "Dana Kim", "******** signed"},
		{"an ideographic space in the text", "Dana\u3000Kim signed", "Dana Kim", "******** signed"},
		{"reported with a no-break space", "Dana Kim signed", "Dana\u00a0Kim", "******** signed"},
		{"reported with spaces around it", "Dana Kim signed", "  Dana Kim ", "******** signed"},
		{"three copies, three layouts", "a b, A  B, a\tb", "a b", "***, ****, ***"},
		{"copies that overlap", "ABABAB", "ABAB", "******"},
		{"copies that overlap, odd length", "aaa", "aa", "***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"spans":[{"text":` + quoteJSON(tc.reported) + `,"category":"pii"}]}`
			result, err := redactWith(t, tc.text, response, NewRedactLLMOptions().WithMinMask(1))
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != tc.want {
				t.Fatalf("Text = %q, want %q", result.Text, tc.want)
			}
		})
	}
}

// Masking every copy of a value is right for an address and wrong for "34": a
// very short value is also a piece of unrelated words and numbers. When a copy
// of one stands on its own, some of the copies inside longer words are left
// alone:
//
//   - a short run of digits, when it is inside a longer number or identifier;
//   - a short word, when it is in the middle or at the end of a longer word. At
//     the start of one it is masked: that is where an inflected form of a name
//     is ("Otts Konto" beside "Ott"), and a reviewer showed the first version
//     of this rule leaving those readable.
//
// The rule covers values of three characters or fewer, and runs of four digits
// or fewer, and nothing longer: see the next test.
func TestRedactLLMLeavesAShortValueInsideALongerWordAlone(t *testing.T) {
	cases := []struct {
		name, text, reported, want string
	}{
		{"an age inside an amount", "Age 34. Total $1345 due.", "34", "Age **. Total $1345 due."},
		{"a name in the middle of a word", "Al called the portal", "Al", "** called the portal"},
		{"a name at the end of a word", "Al and Hal and Cal", "Al", "** and Hal and Cal"},
		{"a name at the start of a word is masked", "Al called. Also, Alabama is warm.", "Al", "** called. **so, **abama is warm."},
		{"an inflected short name is masked", "Herr Ott rief an. Otts Konto ist gesperrt.", "Ott", "Herr *** rief an. ***s Konto ist gesperrt."},
		{"a plural of a short name is masked", "Lee and the Lees", "Lee", "*** and the ***s"},
		{"a longer name that starts with it is masked", "Kim and Kimberly met", "Kim", "*** and ***berly met"},
		{"digits at the start of a longer number", "Age 34, ref 3400", "34", "Age **, ref 3400"},
		{"letters and a digit follow the word rule", "Gate B2 and B2B", "B2", "Gate ** and **B"},
		{"digits inside an identifier", "card 4111, ref X4111Y", "4111", "card ****, ref X4111Y"},
		{"case differs and it is inside a word", "Al left. The portal stayed.", "AL", "** left. The portal stayed."},
		{"three letters is still short", "Ana and Banana", "Ana", "*** and Banana"},
		{"four digits is still short", "PIN 0420, order A04207", "0420", "PIN ****, order A04207"},
		{"punctuation is a boundary", "john wrote from john@example.com", "john", "**** wrote from ****@example.com"},
		{"a possessive is a boundary", "Kim's desk, by Kim", "Kim", "***'s desk, by ***"},
		{"only inside a word: every copy is masked", "ID123456789X", "123456789", "ID*********X"},
		{"only inside words, twice", "tel5550142x and fax5550142y", "5550142", "tel*******x and fax*******y"},
		{"a value with punctuation at its edges", "x(555)y and (555)", "(555)", "x*****y and *****"},
		{"script without word spacing", "田中さんは来た。田中、ありがとう", "田中", "**さんは来た。**、ありがとう"},
		{"hangul with a particle attached", "김철수는 왔다. 김철수 씨", "김철수", "***는 왔다. *** 씨"},
		{"an accent that follows is masked with its letter", "Ana\u0301 and Ana", "Ana", "**** and ***"},
		{"fullwidth digits are digits", "番号 ３４ と １３４５", "３４", "番号 ** と １３４５"},
		{"at the edges of the text", "34", "34", "**"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"spans":[{"text":` + quoteJSON(tc.reported) + `,"category":"pii"}]}`
			// "pii" is not a category the deterministic detectors cover, so what is
			// masked here is only what the model reported.
			result, err := redactWith(t, tc.text, response, NewRedactLLMOptions().WithMinMask(1).WithCategories([]string{"pii"}))
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != tc.want {
				t.Fatalf("Text = %q, want %q", result.Text, tc.want)
			}
		})
	}
}

// Two values that sit side by side are one stretch of sensitive text. Kept
// apart, ShowFirst and ShowLast revealed characters from its middle.
func TestRedactLLMJoinsValuesThatTouch(t *testing.T) {
	const text = "key AB12CD34 end"
	response := `{"spans":[{"text":"AB12","category":"api_key"},{"text":"CD34","category":"api_key"}]}`
	result, err := redactWith(t, text, response, NewRedactLLMOptions().WithShowFirst(1).WithShowLast(1))
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}
	if want := "key A******4 end"; result.Text != want {
		t.Fatalf("Text = %q, want %q", result.Text, want)
	}
	if len(result.Spans) != 1 || result.Spans[0].Original != "AB12CD34" {
		t.Fatalf("Spans = %+v, want one span over AB12CD34", result.Spans)
	}
}

// A span that carries two categories is one finding in each, not one finding in
// a third category named after both.
func TestRedactLLMCountsEachCategoryOfAMergedSpan(t *testing.T) {
	const text = "Reach Dana Kim <dana@example.com> today"

	t.Run("overlapping values", func(t *testing.T) {
		response := `{"spans":[{"text":"Dana Kim <dana@example.com>","category":"name"},{"text":"dana@example.com","category":"email"}]}`
		result, err := redactWith(t, text, response, NewRedactLLMOptions())
		if err != nil {
			t.Fatalf("RedactLLM: %v", err)
		}
		if len(result.Categories) != 2 || result.Categories["name"] != 1 || result.Categories["email"] != 1 {
			t.Fatalf("Categories = %v, want one name and one email", result.Categories)
		}
	})

	t.Run("a label that lists two categories with a space", func(t *testing.T) {
		response := `{"spans":[{"text":"dana@example.com","category":"email, name"}]}`
		result, err := redactWith(t, text, response, NewRedactLLMOptions().WithCategories([]string{"email", "name"}))
		if err != nil {
			t.Fatalf("two requested categories in one label were refused: %v", err)
		}
		if result.Categories["email"] != 1 || result.Categories["name"] != 1 {
			t.Fatalf("Categories = %v, want one email and one name", result.Categories)
		}
		if got := result.Spans[0].Category; got != "email,name" {
			t.Fatalf("Category = %q, want %q", got, "email,name")
		}
	})

	t.Run("a label with padding", func(t *testing.T) {
		response := `{"spans":[{"text":"dana@example.com","category":"  email "}]}`
		result, err := redactWith(t, text, response, NewRedactLLMOptions().WithCategories([]string{"email"}))
		if err != nil {
			t.Fatalf("RedactLLM: %v", err)
		}
		if result.Categories["email"] != 1 {
			t.Fatalf("Categories = %v, want one email", result.Categories)
		}
	})
}

// Text outside the spans comes back byte for byte, including bytes that are not
// valid UTF-8. They used to be replaced, because the whole document went through
// []rune and back.
func TestRedactLLMPreservesBytesOutsideTheSpans(t *testing.T) {
	cases := []struct {
		name, text, reported, want string
	}{
		{"an invalid byte before", "bad \xff byte, mail a@b.co ok", "a@b.co", "bad \xff byte, mail ****** ok"},
		{"an invalid byte after", "mail a@b.co then \xfe\xff", "a@b.co", "mail ****** then \xfe\xff"},
		{"a truncated sequence before", "caf\xc3 mail a@b.co", "a@b.co", "caf\xc3 mail ******"},
		{"a replacement character already there", "� mail a@b.co", "a@b.co", "� mail ******"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spans, err := parseRedactResponse(`{"spans":[{"text":`+quoteJSON(tc.reported)+`,"category":"email"}]}`, tc.text)
			if err != nil {
				t.Fatalf("parseRedactResponse: %v", err)
			}
			got, err := applyRedactions(tc.text, spans, NewRedactLLMOptions())
			if err != nil {
				t.Fatalf("applyRedactions: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Text = %q, want %q", got, tc.want)
			}
		})
	}
}

// A failure of the model's answer is a classified error, so a caller can tell it
// from a provider failure, and it never repeats text the model wrote: a model
// that puts the value in the category field would otherwise put the secret in
// the caller's logs.
func TestRedactLLMFailuresAreClassifiedAndQuoteNothing(t *testing.T) {
	const text = "Zoë's mail is zoe@example.com."
	cases := []struct {
		name     string
		response string
		opts     RedactLLMOptions
		want     types.ErrorKind
	}{
		{"no spans list", `{"note":"nothing"}`, NewRedactLLMOptions(), types.KindSchemaViolation},
		{"prose", `I found zoe@example.com in the text.`, NewRedactLLMOptions(), types.KindMalformedOutput},
		{"an empty object", `{}`, NewRedactLLMOptions(), types.KindSchemaViolation},
		{"spans is null", `{"spans":null}`, NewRedactLLMOptions(), types.KindSchemaViolation},
		{"a span with no text", `{"spans":[{"text":"","category":"zoe@example.com"}]}`, NewRedactLLMOptions(), types.KindSchemaViolation},
		{"a span that is not in the text", `{"spans":[{"text":"zoe@example.org","category":"zoe@example.com"}]}`, NewRedactLLMOptions(), types.KindInvariantViolation},
		{"a category that was not requested", `{"spans":[{"text":"zoe@example.com","category":"zoe@example.com"}]}`,
			NewRedactLLMOptions().WithCategories([]string{"phone"}), types.KindInvariantViolation},
		{"a built-in category that was not requested", `{"spans":[{"text":"zoe@example.com","category":"email"}]}`,
			NewRedactLLMOptions().WithCategories([]string{"phone"}), types.KindInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := redactWith(t, text, tc.response, tc.opts)
			if err == nil {
				t.Fatal("the response was accepted")
			}
			if got := types.KindOf(err); got != tc.want {
				t.Errorf("kind = %v, want %v (%v)", got, tc.want, err)
			}
			for _, secret := range []string{"zoe@example.com", "zoe@example.org"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error repeats text from the model or the document: %v", err)
				}
			}
		})
	}

	// A built-in category name is not the model's own text, and naming it is
	// what makes the error useful.
	_, err := redactWith(t, text, `{"spans":[{"text":"zoe@example.com","category":"email"}]}`,
		NewRedactLLMOptions().WithCategories([]string{"phone"}))
	if err == nil || !strings.Contains(err.Error(), `"email"`) {
		t.Errorf("the error does not name the built-in category: %v", err)
	}
}

// The model is asked for the value and a category. It used to be made to
// produce a position as well, which nothing read.
func TestRedactLLMAsksOnlyForWhatItUses(t *testing.T) {
	span, _ := redactResponseSchema()["properties"].(map[string]any)["spans"].(map[string]any)["items"].(map[string]any)
	properties, _ := span["properties"].(map[string]any)
	if len(properties) != 2 || properties["text"] == nil || properties["category"] == nil {
		t.Errorf("the span schema declares %v, want text and category only", sortedKeys(properties))
	}

	prompt := buildRedactSystemPrompt(NewRedactLLMOptions()) + buildRedactUserPrompt("x", NewRedactLLMOptions())
	for _, stale := range []string{`"start"`, "position", "offset"} {
		if strings.Contains(prompt, stale) {
			t.Errorf("the prompt still mentions %s", stale)
		}
	}

	// A model that sends a position anyway is not refused for it.
	result, err := redactWith(t, "mail a@b.co", `{"spans":[{"text":"a@b.co","start":5,"end":11,"category":"email"}]}`, NewRedactLLMOptions())
	if err != nil || result.Text != "mail ******" {
		t.Errorf("a response with unused fields: Text = %q, err = %v", result.Text, err)
	}
}

// A caller's context with no deadline still gets the default one. The default
// was applied only when the context was nil, so every caller who passed a
// context, including through a Client, ran with no timeout at all.
func TestRedactLLMAppliesTheDefaultDeadline(t *testing.T) {
	seen := func(t *testing.T, ctx context.Context) (time.Time, bool) {
		t.Helper()
		var deadline time.Time
		var bounded bool
		previousCaller, previousProvider := currentHooks()
		setLLMCaller(func(callCtx context.Context, _, _ string, _ types.OpOptions) (string, error) {
			deadline, bounded = callCtx.Deadline()
			return `{"spans":[]}`, nil
		})
		t.Cleanup(func() {
			setLLMCaller(previousCaller)
			SetDefaultProvider(previousProvider)
		})
		if _, err := RedactLLM(ctx, "some text", NewRedactLLMOptions()); err != nil {
			t.Fatalf("RedactLLM: %v", err)
		}
		return deadline, bounded
	}

	t.Run("a context with no deadline", func(t *testing.T) {
		if _, bounded := seen(t, context.Background()); !bounded {
			t.Error("the call ran with no deadline")
		}
	})
	t.Run("no context", func(t *testing.T) {
		// A nil context is what a caller who set none passes.
		if _, bounded := seen(t, nil); !bounded {
			t.Error("the call ran with no deadline")
		}
	})
	t.Run("the caller's own deadline is kept", func(t *testing.T) {
		want := time.Now().Add(5 * time.Minute)
		ctx, cancel := context.WithDeadline(context.Background(), want)
		defer cancel()
		got, bounded := seen(t, ctx)
		if !bounded || !got.Equal(want) {
			t.Errorf("deadline = %v (bounded %v), want the caller's %v", got, bounded, want)
		}
	})
}

// The rule above was first written for values of any length, and a reviewer
// showed what that leaks: "Herr Müller rief an. Müllers Konto ist gesperrt."
// kept "Müllers" readable because "Müller" also stood on its own. An inflected
// or run-together copy of a name or a number is the same secret, so anything
// longer than a very short value is masked wherever it appears.
func TestRedactLLMMasksEveryCopyOfALongerValue(t *testing.T) {
	cases := []struct {
		name, text, reported, want string
	}{
		{"an inflected name", "Herr Müller rief an. Müllers Konto ist gesperrt.", "Müller", "Herr ****** rief an. ******s Konto ist gesperrt."},
		{"a number run into a word", "Call 5550142, or tel5550142x", "5550142", "Call *******, or tel*******x"},
		{"a name inside another word", "Rose left. The primrose stayed.", "Rose", "**** left. The prim**** stayed."},
		{"a plural", "Smith and the Smiths", "Smith", "***** and the *****s"},
		{"a compound", "Dana wrote. Danaplatz 4", "Dana", "**** wrote. ****platz 4"},
		{"five digits", "ID 41111 and X41111Y", "41111", "ID ***** and X*****Y"},
		{"four letters", "Kimi and Kimiko", "Kimi", "**** and ****ko"},
		{"case differs inside a word", "KIMBERLY saw kimberlyite", "Kimberly", "******** saw ********ite"},
		{"an accent that follows is masked with its letter", "cafe\u0301 and cafe", "cafe", "***** and ****"},
		{"letters and digits", "key ab12cd and xab12cdx", "ab12cd", "key ****** and x******x"},
		{"a value with a space", "Dana Kim and Dana Kimberly", "Dana Kim", "******** and ********berly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"spans":[{"text":` + quoteJSON(tc.reported) + `,"category":"pii"}]}`
			result, err := redactWith(t, tc.text, response, NewRedactLLMOptions().WithMinMask(1))
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != tc.want {
				t.Fatalf("Text = %q, want %q", result.Text, tc.want)
			}
		})
	}
}

// A caller can see that copies of a short value were left alone, and how many.
func TestRedactLLMReportsTheCopiesItLeftAlone(t *testing.T) {
	cases := []struct {
		text, reported string
		want           int
	}{
		{"Al called. Also, Alabama is warm.", "Al", 1}, // the one in "called"
		{"Age 34. Total $1345 due, ref 3400.", "34", 2},
		{"Al rang.", "Al", 0},
		{"Hal", "Al", 0}, // nothing stands alone, so the copy is masked, not left
		{"Smith and the Smiths", "Smith", 0},
	}
	for _, tc := range cases {
		response := `{"spans":[{"text":` + quoteJSON(tc.reported) + `,"category":"pii"}]}`
		result, err := redactWith(t, tc.text, response, NewRedactLLMOptions())
		if err != nil {
			t.Fatalf("%q: %v", tc.text, err)
		}
		if got := result.Metadata["left_inside_longer_words"]; got != tc.want {
			t.Errorf("%q: left_inside_longer_words = %v, want %d", tc.text, got, tc.want)
		}
	}
}

// Letters are compared the way a case-insensitive regexp compares them, and
// whitespace the way strings.Fields splits it. What does not match fails the
// call; it never passes as "nothing to redact".
func TestRedactLLMFoldsCaseAndWhitespace(t *testing.T) {
	found := []struct {
		name, text, reported, want string
	}{
		{"upper against lower", "DANA KIM signed", "dana kim", "******** signed"},
		{"lower against upper", "dana kim signed", "DANA KIM", "******** signed"},
		{"greek", "ΑΘΗΝΑ and αθηνα", "Αθηνα", "***** and *****"},
		{"cyrillic", "ИВАН пришёл", "иван", "**** пришёл"},
		{"the kelvin sign is a k", "\u212aim Lee", "kim", "*** Lee"},
		{"the long s is an s", "Stra\u017f\u017fe 4", "strasse", "******* 4"},
		{"a vertical tab", "Dana\vKim signed", "Dana Kim", "******** signed"},
		{"a next-line control", "Dana\u0085Kim signed", "Dana Kim", "******** signed"},
		{"a mix of spaces", "Dana \t\u00a0\n Kim signed", "Dana Kim", "************ signed"},
		{"digits are not folded", "1234 and １２３４", "1234", "**** and １２３４"},
	}
	for _, tc := range found {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"spans":[{"text":` + quoteJSON(tc.reported) + `,"category":"pii"}]}`
			result, err := redactWith(t, tc.text, response, NewRedactLLMOptions().WithMinMask(1))
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != tc.want {
				t.Fatalf("Text = %q, want %q", result.Text, tc.want)
			}
		})
	}

	notFound := []struct {
		name, text, reported string
	}{
		{"a dotted capital I is not an i", "\u0130stanbul office", "istanbul"},
		{"a sharp s is not ss", "Stra\u00dfe 4", "strasse"},
		{"a zero-width space is not a space", "Dana\u200bKim signed", "Dana Kim"},
		{"a missing space", "DanaKim signed", "Dana Kim"},
		{"an extra space inside a word", "Da na Kim signed", "Dana Kim"},
	}
	for _, tc := range notFound {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"spans":[{"text":` + quoteJSON(tc.reported) + `,"category":"pii"}]}`
			result, err := redactWith(t, tc.text, response, NewRedactLLMOptions())
			if err == nil || result.Text != "" {
				t.Fatalf("err = %v, Text = %q; want an error and no text", err, result.Text)
			}
		})
	}
}

// The search is linear in the text. It was a loop that restarted a regexp one
// character after each match, which on a long value inside a repetitive run is
// about the cube of the length: the document is untrusted, so that was a way to
// stall a call. Twenty thousand characters took minutes; it now takes
// milliseconds, and the deadline here is far above that.
func TestRedactLLMSearchIsLinearOnARepetitiveDocument(t *testing.T) {
	text := strings.Repeat("A", 20000)
	value := strings.Repeat("a", 10000)

	done := make(chan [][2]int, 1)
	go func() { done <- occurrencesOf(text, value) }()

	select {
	case found := <-done:
		if len(found) != 10001 {
			t.Fatalf("found %d occurrences, want 10001", len(found))
		}
		merged := mergeByteSpans(func() []byteSpan {
			spans := make([]byteSpan, len(found))
			for i, at := range found {
				spans[i] = byteSpan{start: at[0], end: at[1]}
			}
			return spans
		}())
		if len(merged) != 1 || merged[0].start != 0 || merged[0].end != len(text) {
			t.Fatalf("merged = %d spans, want one over the whole text", len(merged))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the search did not finish in ten seconds")
	}
}

// Categories are counted under the caller's spelling, an unlabelled span is
// masked and not counted, and a requested category with stray spaces still
// matches.
func TestRedactLLMCategoryNamesAreNormalised(t *testing.T) {
	const text = "Mail ana@example.com or call 555-0142."

	t.Run("the model's casing", func(t *testing.T) {
		result, err := redactWith(t, text, `{"spans":[{"text":"ana@example.com","category":"EMAIL"},{"text":"555-0142","category":"Phone"}]}`,
			NewRedactLLMOptions().WithCategories([]string{"email", "phone"}))
		if err != nil {
			t.Fatalf("RedactLLM: %v", err)
		}
		if len(result.Categories) != 2 || result.Categories["email"] != 1 || result.Categories["phone"] != 1 {
			t.Fatalf("Categories = %v, want email and phone as the caller spelled them", result.Categories)
		}
		if result.Spans[0].Category != "email" {
			t.Fatalf("Category = %q, want %q", result.Spans[0].Category, "email")
		}
	})

	t.Run("a built-in name under all", func(t *testing.T) {
		result, err := redactWith(t, text, `{"spans":[{"text":"ana@example.com","category":"Email"}]}`, NewRedactLLMOptions())
		if err != nil {
			t.Fatalf("RedactLLM: %v", err)
		}
		if result.Categories["email"] != 1 || len(result.Categories) != 1 {
			t.Fatalf("Categories = %v, want one email", result.Categories)
		}
	})

	t.Run("a requested category with stray spaces", func(t *testing.T) {
		result, err := redactWith(t, text, `{"spans":[{"text":"ana@example.com","category":"email"}]}`,
			NewRedactLLMOptions().WithCategories([]string{" Email "}))
		if err != nil {
			t.Fatalf("a requested category was refused over its spacing: %v", err)
		}
		if result.Categories["Email"] != 1 {
			t.Fatalf("Categories = %v, want one under the caller's name", result.Categories)
		}
	})

	t.Run("no label", func(t *testing.T) {
		result, err := redactWith(t, text, `{"spans":[{"text":"ana@example.com","category":""}]}`, NewRedactLLMOptions().WithCategories([]string{"name"}))
		if err != nil {
			t.Fatalf("RedactLLM: %v", err)
		}
		if result.Text != "Mail *************** or call 555-0142." {
			t.Fatalf("Text = %q", result.Text)
		}
		if len(result.Categories) != 0 {
			t.Fatalf("Categories = %v, want none for a span with no label", result.Categories)
		}
	})

	t.Run("options that cannot be used are a configuration error", func(t *testing.T) {
		for _, opts := range []RedactLLMOptions{
			NewRedactLLMOptions().WithMinMask(0),
			NewRedactLLMOptions().WithCategories(nil),
			NewRedactLLMOptions().WithCategories([]string{}),
		} {
			result, err := redactWith(t, text, `{"spans":[]}`, opts)
			if types.KindOf(err) != types.KindConfiguration || result.Text != "" {
				t.Errorf("err = %v (kind %v), Text = %q", err, types.KindOf(err), result.Text)
			}
		}
	})
}

// The count is of copies that are still readable. A value the model lists
// twice is counted once, and a copy inside another reported value is masked
// with it and not counted at all.
func TestRedactLLMCountsOnlyCopiesThatStayReadable(t *testing.T) {
	cases := []struct {
		name, text, response string
		want                 int
		wantText             string
	}{
		{"a value listed twice", "Al called.",
			`{"spans":[{"text":"Al","category":"name"},{"text":"Al","category":"name"}]}`, 1, "** called."},
		{"a value listed twice in two cases", "Al called.",
			`{"spans":[{"text":"Al","category":"name"},{"text":"AL","category":"name"}]}`, 1, "** called."},
		{"a copy inside another reported value", "Al called Calvin.",
			`{"spans":[{"text":"Al","category":"name"},{"text":"Calvin","category":"name"}]}`, 1, "** called ******."},
		{"every copy inside another reported value", "Al met Hal.",
			`{"spans":[{"text":"Al","category":"name"},{"text":"Hal","category":"name"}]}`, 0, "** met ***."},
		{"two short values", "Al and Ed called Ned",
			`{"spans":[{"text":"Al","category":"name"},{"text":"Ed","category":"name"}]}`, 3, "** and ** called Ned"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := redactWith(t, tc.text, tc.response, NewRedactLLMOptions().WithMinMask(1))
			if err != nil {
				t.Fatalf("RedactLLM: %v", err)
			}
			if result.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", result.Text, tc.wantText)
			}
			if got := result.Metadata["left_inside_longer_words"]; got != tc.want {
				t.Errorf("left_inside_longer_words = %v, want %d", got, tc.want)
			}
		})
	}
}

// A category name with a comma in it could never match: labels are split on
// commas, because a span can carry several. A blank one names nothing.
func TestRedactLLMOptionsRefuseCategoryNamesThatCannotMatch(t *testing.T) {
	cases := []struct {
		name       string
		categories []string
		wantErr    bool
	}{
		{"all", []string{"all"}, false},
		{"two names", []string{"email", "phone"}, false},
		{"a custom name", []string{"employee id"}, false},
		{"a name with a slash", []string{"city/state"}, false},
		{"a name with padding", []string{" email "}, false},
		{"none", nil, true},
		{"an empty list", []string{}, true},
		{"a name with a comma", []string{"city, state"}, true},
		{"a comma among good names", []string{"email", "last,first"}, true},
		{"a blank name", []string{"email", "  "}, true},
		{"an empty name", []string{""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewRedactLLMOptions().WithCategories(tc.categories).Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, want an error: %v", err, tc.wantErr)
			}
		})
	}
}

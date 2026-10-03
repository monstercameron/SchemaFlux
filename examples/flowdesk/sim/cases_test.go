package sim

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/flowdesk/desk"
)

func TestWithoutReferencesTakesTheReferenceOutOfEveryBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"a reference at the end", "Where is it? [ref M-08]", "Where is it?"},
		{"a reference in the middle", "Before [ref M-08] after", "Before  after"},
		{"a reference at the start", "[ref M-08] Where is it?", "Where is it?"},
		{"a body that is only a reference", "[ref M-08]", ""},
		{"no reference", "Where is it?", "Where is it?"},
		{"two references", "One [ref M-01] two [ref M-02]", "One  two"},
		{"a reference of three digits", "Text [ref M-123]", "Text"},
		{"a tag that is not a reference", "Text [ref X-08]", "Text [ref X-08]"},
		{"a reference with the number missing", "Text [ref M-]", "Text [ref M-]"},
		{"a reference over a line", "Text\n[ref M-08]\n", "Text"},
		{"a reference with other brackets round it", "Text ([ref M-08])", "Text ()"},
		{"an empty body", "", ""},
		{"spaces round a body are trimmed", "  Text  [ref M-08]  ", "Text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			given := []Case{{Message: desk.Message{ID: "M-08", Subject: "Late [ref M-08]", Body: tc.body}}}
			got := WithoutReferences(given)
			if len(got) != 1 || got[0].Message.Body != tc.want {
				t.Errorf("WithoutReferences = %q, want %q", got[0].Message.Body, tc.want)
			}
			if got[0].Message.Subject != "Late [ref M-08]" {
				t.Errorf("the subject was changed: %q", got[0].Message.Subject)
			}
			if given[0].Message.Body != tc.body {
				t.Errorf("the cases that were given were changed: %q", given[0].Message.Body)
			}
		})
	}

	t.Run("every labelled case, and no body is left with a reference", func(t *testing.T) {
		plain := WithoutReferences(Cases())
		if len(plain) != len(Cases()) {
			t.Fatalf("%d cases come back from %d", len(plain), len(Cases()))
		}
		for _, labelled := range plain {
			if strings.Contains(labelled.Message.Body, "[ref") {
				t.Errorf("%s still carries its reference: %q", labelled.Message.ID, labelled.Message.Body)
			}
			if labelled.Message.ID == "" || labelled.Status == "" {
				t.Errorf("a case lost its labels: %+v", labelled.Message)
			}
		}
	})
	t.Run("no cases", func(t *testing.T) {
		if got := WithoutReferences(nil); len(got) != 0 {
			t.Errorf("%d cases from none", len(got))
		}
	})
}

func TestACaseSaysWhatTheDeskDoesWithItsLanguage(t *testing.T) {
	cases := []struct {
		language string
		in, back bool
	}{
		{"English", false, false},
		{"Spanish", true, true},
		{"French", true, true},
		{"German", true, true},
		{"Portuguese", true, true},
		{"another language", true, false},
		{"", true, true},
		{"english", true, true},
		{"Dutch", true, true},
		{"Another language", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.language, func(t *testing.T) {
			labelled := Case{Screen: desk.Screen{Language: tc.language}}
			if labelled.TranslatedIn() != tc.in || labelled.TranslatedBack() != tc.back {
				t.Errorf("translated in %t and back %t, want %t and %t", labelled.TranslatedIn(), labelled.TranslatedBack(), tc.in, tc.back)
			}
		})
	}
}

// The labels have to be the ones the desk can reach, and say what the flows are
// to be held to.
func TestTheLabelledCasesAreWholeAndTheirSendersAreRight(t *testing.T) {
	customers := map[string]string{}
	for _, ticket := range OpenTickets() {
		if ticket.ID == "" || ticket.Customer == "" || ticket.Summary == "" {
			t.Errorf("an open ticket is missing something: %+v", ticket)
		}
		if _, repeated := customers[ticket.Customer]; repeated {
			t.Errorf("two open tickets are for %s", ticket.Customer)
		}
		customers[ticket.Customer] = ticket.ID
	}
	senders := map[string]string{}
	for _, labelled := range Cases() {
		id := labelled.Message.ID
		if previous, repeated := senders[labelled.Message.From]; repeated {
			t.Errorf("%s and %s are from the same sender", id, previous)
		}
		senders[labelled.Message.From] = id

		// A message follows up a ticket exactly when its sender is the ticket's
		// customer, and it is that ticket.
		ticket, isCustomer := customers[labelled.Message.From]
		if isCustomer != (labelled.FollowsUp != "") || (isCustomer && ticket != labelled.FollowsUp) {
			t.Errorf("%s: from %s, the customer of %q; the label follows up %q", id, labelled.Message.From, ticket, labelled.FollowsUp)
		}
		if !strings.Contains(labelled.Message.Body, "[ref "+id+"]") {
			t.Errorf("%s has no reference in its body", id)
		}
		if labelled.Screen.Spam {
			continue
		}
		for what, text := range map[string]string{"draft": labelled.Draft, "English text": labelled.English, "reply in the customer's language": labelled.Sent, "redraft": labelled.Redraft} {
			if text != "" && !strings.Contains(text, "[ref "+id+"]") {
				t.Errorf("%s: the %s does not keep the reference", id, what)
			}
		}
		for _, sensitive := range labelled.Personal {
			english := labelled.English
			if english == "" {
				english = labelled.Message.Body
			}
			if !strings.Contains(english, sensitive.Text) {
				t.Errorf("%s: %q is not in the text the redaction is of", id, sensitive.Text)
			}
		}
	}
	if len(Cases()) < 17 {
		t.Errorf("%d cases; the first fourteen and the three that follow them are the ones the benchmark is recorded on", len(Cases()))
	}
}

// The English text of a translated case is the subject translated and the body
// translated, as Message.Text puts them: a subject line, a blank line, the body.
func TestTheEnglishOfATranslatedCaseHasASubjectLineAndABody(t *testing.T) {
	for _, labelled := range Cases() {
		if labelled.English == "" {
			continue
		}
		t.Run(labelled.Message.ID, func(t *testing.T) {
			subject, body, found := strings.Cut(labelled.English, "\n\n")
			if !found || strings.TrimSpace(subject) == "" || strings.TrimSpace(body) == "" || strings.Contains(subject, "\n") {
				t.Errorf("the English text is %q: it should be a subject line, a blank line and a body", labelled.English)
			}
			if subject == labelled.Message.Subject {
				t.Errorf("the subject was not translated")
			}
		})
	}
}

func TestTheTicketOptionIsTheOneTheDeskShows(t *testing.T) {
	for _, ticket := range OpenTickets() {
		if got := ticketOption(ticket.ID); got != ticket.Option() {
			t.Errorf("ticketOption(%s) = %q, want %q", ticket.ID, got, ticket.Option())
		}
	}
	for _, id := range []string{"", "T-9999", "t-1001", "T-1001 "} {
		if got := ticketOption(id); got != noneOfThese {
			t.Errorf("ticketOption(%q) = %q, want none", id, got)
		}
	}
}

func TestTheFinalReplyIsTheRedraftWhenThereIsOne(t *testing.T) {
	cases := []struct {
		draft, redraft, want string
	}{
		{"draft", "", "draft"},
		{"draft", "redraft", "redraft"},
		{"", "", ""},
		{"", "redraft", "redraft"},
	}
	for _, tc := range cases {
		if got := finalReply(Case{Draft: tc.draft, Redraft: tc.redraft}); got != tc.want {
			t.Errorf("finalReply(%q, %q) = %q, want %q", tc.draft, tc.redraft, got, tc.want)
		}
	}
	for _, labelled := range Cases() {
		want := labelled.Draft
		if labelled.Redraft != "" {
			want = labelled.Redraft
		}
		if got := finalReply(labelled); got != want {
			t.Errorf("%s: finalReply = %q", labelled.Message.ID, got)
		}
	}
}

func TestTheRedactionNamesWhatTheLabelsCallPersonal(t *testing.T) {
	cases := []struct {
		name     string
		personal []Sensitive
		want     string
	}{
		{"nothing", nil, `{"spans":[]}`},
		{"a phone number", []Sensitive{{Text: "555", Category: "phone"}}, `{"spans":[{"text":"555","category":"phone"}]}`},
		{"two values", []Sensitive{{Text: "a", Category: "x"}, {Text: "b", Category: "y"}}, `{"spans":[{"text":"a","category":"x"},{"text":"b","category":"y"}]}`},
		{"a value with a quotation", []Sensitive{{Text: `a "b"`, Category: "x"}}, `{"spans":[{"text":"a \"b\"","category":"x"}]}`},
		{"a value with accents", []Sensitive{{Text: "teléfono", Category: "phone"}}, `{"spans":[{"text":"teléfono","category":"phone"}]}`},
		{"a value with a newline", []Sensitive{{Text: "a\nb", Category: "address"}}, `{"spans":[{"text":"a\nb","category":"address"}]}`},
		{"a category that is blank", []Sensitive{{Text: "a"}}, `{"spans":[{"text":"a","category":""}]}`},
		{"three values", []Sensitive{{Text: "1", Category: "a"}, {Text: "2", Category: "b"}, {Text: "3", Category: "c"}}, `{"spans":[{"text":"1","category":"a"},{"text":"2","category":"b"},{"text":"3","category":"c"}]}`},
		{"the same value twice", []Sensitive{{Text: "1", Category: "a"}, {Text: "1", Category: "a"}}, `{"spans":[{"text":"1","category":"a"},{"text":"1","category":"a"}]}`},
		{"a value with a backslash", []Sensitive{{Text: "a\\b", Category: "x"}}, `{"spans":[{"text":"a\\b","category":"x"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := redact(Case{Personal: tc.personal})
			if err != nil || got != tc.want {
				t.Errorf("redact = %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

// The calls here are timed on a synctest bubble's clock, so a sleep of ten
// milliseconds is exactly that and every figure is exact.
func TestTheCriticalPathIsTheSumOfTheLongestCallOfEachWait(t *testing.T) {
	t.Run("nothing recorded", func(t *testing.T) {
		var log Log
		if log.CriticalPath() != 0 {
			t.Errorf("an empty log has a critical path of %v", log.CriticalPath())
		}
	})
	t.Run("calls one after another are all on it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log Log
			for i := 0; i < 3; i++ {
				end := log.Begin("a")
				time.Sleep(10 * time.Millisecond)
				end()
			}
			if path := log.CriticalPath(); path != 30*time.Millisecond || path != log.Busy() {
				t.Errorf("critical path %v for three calls of 10ms one after another, busy %v", path, log.Busy())
			}
		})
	})
	t.Run("calls that overlap count the longest", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log Log
			short, long := log.Begin("short"), log.Begin("long")
			time.Sleep(10 * time.Millisecond)
			short()
			time.Sleep(20 * time.Millisecond)
			long()
			calls := log.Calls()
			if calls[0].Wait != 1 || calls[1].Wait != 1 {
				t.Fatalf("the calls are in waits %d and %d, want both in the first", calls[0].Wait, calls[1].Wait)
			}
			if calls[0].Duration() != 10*time.Millisecond || calls[1].Duration() != 30*time.Millisecond {
				t.Fatalf("the calls took %v and %v, want 10ms and 30ms", calls[0].Duration(), calls[1].Duration())
			}
			if path := log.CriticalPath(); path != calls[1].Duration() {
				t.Errorf("critical path %v, want the long call's %v and not the short one's %v", path, calls[1].Duration(), calls[0].Duration())
			}
		})
	})
	t.Run("a group and then a call", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log Log
			a, b := log.Begin("a"), log.Begin("b")
			time.Sleep(10 * time.Millisecond)
			a()
			b()
			c := log.Begin("c")
			time.Sleep(10 * time.Millisecond)
			c()
			calls := log.Calls()
			if calls[0].Wait != 1 || calls[1].Wait != 1 || calls[2].Wait != 2 {
				t.Fatalf("waits %d, %d, %d; want 1, 1, 2", calls[0].Wait, calls[1].Wait, calls[2].Wait)
			}
			longest := calls[0].Duration()
			if calls[1].Duration() > longest {
				longest = calls[1].Duration()
			}
			if path, want := log.CriticalPath(), longest+calls[2].Duration(); path != want || path != 20*time.Millisecond {
				t.Errorf("critical path %v, want %v, which is 20ms", path, want)
			}
		})
	})
	t.Run("a call still in flight is counted to now", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log Log
			end := log.Begin("a")
			time.Sleep(10 * time.Millisecond)
			if path := log.CriticalPath(); path != 10*time.Millisecond {
				t.Errorf("critical path %v for a call that has been in flight for 10ms", path)
			}
			end()
		})
	})
	t.Run("the simulator reports the same figure", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider, ctx := through(10 * time.Millisecond)
			if _, err := schemaflux.Is(ctx, caseByID(t, "M-02").Message.Body, askOf[desk.Triage](t, "NeedsHuman")); err != nil {
				t.Fatal(err)
			}
			if path := provider.CriticalPath(); path != 10*time.Millisecond || path != provider.Busy() {
				t.Errorf("critical path %v, busy %v for one call of 10ms", path, provider.Busy())
			}
		})
	})
}

func TestEachCallKnowsItsWait(t *testing.T) {
	var log Log
	a, b := log.Begin("a"), log.Begin("b")
	a()
	b()
	log.Begin("c")()
	d := log.Begin("d")
	e := log.Begin("e")
	d()
	e()
	var got []int
	for _, call := range log.Calls() {
		got = append(got, call.Wait)
	}
	want := []int{1, 1, 2, 3, 3}
	if len(got) != len(want) {
		t.Fatalf("waits %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("waits %v, want %v", got, want)
			break
		}
	}
	if log.Waits() != 3 {
		t.Errorf("%d waits, want 3", log.Waits())
	}
}

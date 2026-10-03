package desk

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// The flows read their questions, their options and the scale of the urgency
// from the struct tags that Assess reads, so these are what the three of them
// ask. Tests may branch freely; it is the package's code that may not.

func TestQuestionIsWhatTheAskTagSays(t *testing.T) {
	type tagged struct {
		Plain  string `ask:"Is it plain?"`
		Spaced string `ask:"  Is it spaced?  "`
		Bare   string
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"the spam question", question[Screen]("Spam"), "Is this message spam, a marketing blast or an automated notification, and not a customer writing to a support desk?"},
		{"the language question", question[Screen]("Language"), "What language is the message written in?"},
		{"the personal data question", question[Screen]("PersonalData"), question[Look]("PersonalData")},
		{"the team question", question[Triage]("Team"), "Which team should handle this message?"},
		{"the intent question", question[Triage]("Intent"), "What is the customer doing?"},
		{"the urgency question", question[Triage]("Urgency"), question[Look]("Urgency")},
		{"the answerable question", question[Triage]("Answerable"), question[Second]("Answerable")},
		{"the needs human question", question[Triage]("NeedsHuman"), question[Look]("NeedsHuman")},
		{"the next question", question[After]("Next"), "What should the desk do after replying?"},
		{"the again question", question[After]("Again"), "Is this customer likely to write to the desk again about the same matter within a week?"},
		{"a field of an embedded struct is the outer struct's", question[Look]("Team"), question[Triage]("Team")},
		{"a field with a tag of its own", question[tagged]("Plain"), "Is it plain?"},
		{"a tag is not trimmed", question[tagged]("Spaced"), "  Is it spaced?  "},
		{"a field with no tag asks nothing", question[tagged]("Bare"), ""},
		{"a field that does not exist asks nothing", question[tagged]("Missing"), ""},
		{"the name is case sensitive", question[Screen]("spam"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("question = %q, want %q", tc.got, tc.want)
			}
		})
	}
	for _, name := range []string{"Spam", "Language", "PersonalData"} {
		if question[Screen](name) == "" {
			t.Errorf("the screen asks nothing under %s", name)
		}
	}
	for _, name := range []string{"Team", "Intent", "Urgency", "Answerable", "NeedsHuman"} {
		if question[Triage](name) == "" {
			t.Errorf("the triage asks nothing under %s", name)
		}
	}
}

func TestOptionsAreTheLabelsOfTheOneofTag(t *testing.T) {
	type tagged struct {
		Spaced   string `oneof:"a, b ,c"`
		Trailing string `oneof:"a,b,"`
		Single   string `oneof:"only"`
		Tabs     string `oneof:"a,\tb\t,c"`
		Inner    string `oneof:"two words, three words here"`
		None     string
	}
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"the languages", options[Screen]("Language"), []string{"English", "Spanish", "French", "German", "Portuguese", "another language"}},
		{"the teams", options[Triage]("Team"), []string{"billing", "technical", "shipping", "account"}},
		{"the intents", options[Triage]("Intent"), []string{"asking a question", "making a complaint", "requesting an action", "giving feedback"}},
		{"the next actions", options[After]("Next"), []string{"close the ticket", "wait for the customer", "follow up tomorrow", "escalate to a manager"}},
		{"through an embedding", options[Look]("Team"), options[Triage]("Team")},
		{"spaces round a label are trimmed", options[tagged]("Spaced"), []string{"a", "b", "c"}},
		{"tabs round a label are trimmed", options[tagged]("Tabs"), []string{"a", "b", "c"}},
		{"spaces inside a label are kept", options[tagged]("Inner"), []string{"two words", "three words here"}},
		{"a trailing comma leaves a blank label, which the library refuses", options[tagged]("Trailing"), []string{"a", "b", ""}},
		{"one label", options[tagged]("Single"), []string{"only"}},
		{"a field with no oneof tag has one blank label", options[tagged]("None"), []string{""}},
		{"a field that does not exist has one blank label", options[tagged]("Missing"), []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Errorf("options = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestScaleIsTheTwoEndsOfTheScaleTag(t *testing.T) {
	type tagged struct {
		Urgency  int `scale:"1-5"`
		Wide     int `scale:"1-100"`
		Zero     int `scale:"0-10"`
		Negative int `scale:"-2-2"`
		Single   int `scale:"3-3"`
		Words    int `scale:"low-high"`
		One      int `scale:"5"`
		Open     int `scale:"1-"`
		Spaced   int `scale:"1 - 5"`
		None     int
	}
	cases := []struct {
		name      string
		field     string
		low, high int
		refused   bool
	}{
		{"the urgency of the triage", "Urgency", 1, 5, false},
		{"a wider scale", "Wide", 1, 100, false},
		{"a scale from zero", "Zero", 0, 10, false},
		{"a scale from below zero", "Negative", -2, 2, false},
		{"a scale of one level", "Single", 3, 3, false},
		{"words", "Words", 0, 0, true},
		{"one number", "One", 0, 0, true},
		{"an open end", "Open", 0, 0, true},
		{"spaces round the dash", "Spaced", 0, 0, true},
		{"no scale tag", "None", 0, 0, true},
		{"a field that does not exist", "Missing", 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			low, high, err := scale[tagged](tc.field)
			if tc.refused {
				if err == nil || low != 0 || high != 0 {
					t.Errorf("scale = %d, %d, %v; want an error and no ends", low, high, err)
				}
				return
			}
			if err != nil || low != tc.low || high != tc.high {
				t.Errorf("scale = %d, %d, %v; want %d, %d", low, high, err, tc.low, tc.high)
			}
		})
	}
	t.Run("the triage's own urgency", func(t *testing.T) {
		low, high, err := scale[Triage]("Urgency")
		if err != nil || low != 1 || high != 5 {
			t.Errorf("scale = %d, %d, %v", low, high, err)
		}
	})
}

func TestAMessageIsReadAsItsSubjectAndItsBody(t *testing.T) {
	cases := []struct {
		name    string
		message Message
		want    string
	}{
		{"a subject and a body", Message{Subject: "Late order", Body: "Where is it?"}, "Late order\n\nWhere is it?"},
		{"no subject", Message{Body: "Where is it?"}, "\n\nWhere is it?"},
		{"no body", Message{Subject: "Late order"}, "Late order\n\n"},
		{"neither", Message{}, "\n\n"},
		{"the ID and the sender are not part of it", Message{ID: "M-01", From: "a@example.com", Subject: "S", Body: "B"}, "S\n\nB"},
		{"a body of several lines", Message{Subject: "S", Body: "one\ntwo"}, "S\n\none\ntwo"},
		{"a subject with a colon", Message{Subject: "Re: order", Body: "B"}, "Re: order\n\nB"},
		{"text that is not English", Message{Subject: "Cobro doble", Body: "Me cobraron dos veces."}, "Cobro doble\n\nMe cobraron dos veces."},
		{"a body that holds the reference", Message{Subject: "S", Body: "B [ref M-02]"}, "S\n\nB [ref M-02]"},
		{"the subject comes first", Message{Subject: "second", Body: "first"}, "second\n\nfirst"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.message.Text(); got != tc.want {
				t.Errorf("Text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestATicketIsShownByItsIDItsCustomerAndItsSummary(t *testing.T) {
	cases := []struct {
		ticket Ticket
		want   string
	}{
		{Ticket{ID: "T-1", Customer: "a@example.com", Summary: "Double charge"}, "T-1 (a@example.com): Double charge"},
		{Ticket{ID: "T-2", Customer: "a@example.com", Summary: "Double charge"}, "T-2 (a@example.com): Double charge"},
		{Ticket{ID: "T-1", Customer: "b@example.com", Summary: "Double charge"}, "T-1 (b@example.com): Double charge"},
		{Ticket{ID: "T-1", Summary: "No customer"}, "T-1 (): No customer"},
		{Ticket{Customer: "a@example.com", Summary: "No ID"}, " (a@example.com): No ID"},
		{Ticket{ID: "T-1", Customer: "a@example.com"}, "T-1 (a@example.com): "},
		{Ticket{}, " (): "},
		{Ticket{ID: "T-9", Customer: "x@example.com", Summary: "A summary: with a colon (and brackets)"}, "T-9 (x@example.com): A summary: with a colon (and brackets)"},
		{Ticket{ID: "T-10", Customer: "x@example.com", Summary: "Línea con acentos"}, "T-10 (x@example.com): Línea con acentos"},
		{Ticket{ID: "T-11", Customer: "x@example.com", Summary: "line\nbreak"}, "T-11 (x@example.com): line\nbreak"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if got := tc.ticket.Option(); got != tc.want {
			t.Errorf("Option of %+v = %q, want %q", tc.ticket, got, tc.want)
		}
		seen[tc.ticket.Option()] = true
	}
	if len(seen) != len(cases) {
		t.Errorf("%d different options from %d tickets that differ", len(seen), len(cases))
	}
}

// labelsOf is the labels of a list of routes, sorted.
func labelsOf[T any](routes []schemaflux.Route[T]) []string {
	labels := make([]string, len(routes))
	for i, route := range routes {
		labels[i] = route.Label
	}
	sort.Strings(labels)
	return labels
}

func sorted(labels []string) []string {
	return append([]string(nil), labels...)
}

// A label with no route is found only when a message that leads to it arrives,
// and it is a failed run when it does. These hold every list of routes to the
// list of labels it is the routes of.
func TestEveryLabelOfEveryOneofTagHasARoute(t *testing.T) {
	d := New(nil)
	languages := sorted(options[Screen]("Language"))
	sort.Strings(languages)
	teams := sorted(options[Triage]("Team"))
	sort.Strings(teams)

	cases := []struct {
		name   string
		labels []string
		routes []string
	}{
		{"the languages the text is translated into English from", languages, labelsOf(d.toEnglish("text"))},
		{"the languages the reply is written in", languages, labelsOf(d.inTheirLanguage("reply"))},
		{"the languages a message is read again for", languages, labelsOf(d.readings(keep(reading{}), keep(reading{})))},
		{"the teams that have a playbook", teams, labelsOf(d.playbooks())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.labels, tc.routes) {
				t.Errorf("the tag offers %q and the routes are %q", tc.labels, tc.routes)
			}
		})
	}

	// Every field with a oneof tag is either one of those, or is listed here as
	// a choice that is returned and not routed on.
	routed := map[string]bool{"Language": true, "Team": true}
	returned := map[string]bool{"Intent": true, "Next": true}
	var walk func(reflect.Type)
	walk = func(kind reflect.Type) {
		for i := range kind.NumField() {
			field := kind.Field(i)
			if field.Anonymous {
				walk(field.Type)
				continue
			}
			if field.Tag.Get("oneof") == "" {
				continue
			}
			if !routed[field.Name] && !returned[field.Name] {
				t.Errorf("%s offers labels and nothing routes on them: add a route list and a test, or say it is only returned", field.Name)
			}
		}
	}
	walk(reflect.TypeFor[Look]())
}

func TestEveryLanguageLeadsWhereItShould(t *testing.T) {
	d := New(nil)
	// What each language does to the text and the reply, as the steps' labels
	// say it: the calls are not made, only the routes read.
	toEnglish, reply := map[string]bool{}, map[string]bool{}
	for _, route := range d.toEnglish("text") {
		toEnglish[route.Label] = route.Run != nil
	}
	for _, route := range d.inTheirLanguage("reply") {
		reply[route.Label] = route.Run != nil
	}
	for _, label := range options[Screen]("Language") {
		if !toEnglish[label] || !reply[label] {
			t.Errorf("%q has no step to English (%t) or for the reply (%t)", label, toEnglish[label], reply[label])
		}
	}

	// English is the one language that is kept as it is, and the one the desk
	// cannot name is kept in the reply: neither makes a call.
	ctx := context.Background()
	for _, route := range d.toEnglish("the text") {
		if route.Label != "English" {
			continue
		}
		if got, err := route.Run(ctx); err != nil || got != "the text" {
			t.Errorf("English is %q, %v; want the text as it is", got, err)
		}
	}
	for _, route := range d.inTheirLanguage("the reply") {
		if route.Label != "English" && route.Label != "another language" {
			continue
		}
		if got, err := route.Run(ctx); err != nil || got != "the reply" {
			t.Errorf("%s is %q, %v; want the reply as it is", route.Label, got, err)
		}
	}
}

// chooser is a provider that answers every assessment with the same number and
// keeps what it was asked.
type chooser struct {
	pick string

	mu      sync.Mutex
	prompts []string
}

func (c *chooser) Name() string                                      { return "local" }
func (c *chooser) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (c *chooser) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func (c *chooser) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, req.UserPrompt)
	c.mu.Unlock()
	return schemaflux.CompletionResponse{Content: c.pick, Provider: "local", Model: req.Model, FinishReason: "stop"}, nil
}

func (c *chooser) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.prompts)
}

func TestTheTicketMatchChoosesByIDAndAsksOfTheSenderAndTheText(t *testing.T) {
	same := "Charged twice for the March invoice"
	three := []Ticket{
		{ID: "T-1", Customer: "a@example.com", Summary: same},
		{ID: "T-2", Customer: "b@example.com", Summary: same},
		{ID: "T-3", Customer: "c@example.com", Summary: "Parcel has not arrived"},
	}
	cases := []struct {
		name      string
		open      []Ticket
		pick      string
		wantID    string
		wantFound bool
		wantCalls int
		refused   bool
	}{
		{"the first of three", three, `{"q1":1}`, "T-1", true, 1, false},
		{"the second, which has the summary of the first", three, `{"q1":2}`, "T-2", true, 1, false},
		{"the third", three, `{"q1":3}`, "T-3", true, 1, false},
		{"none of these, which is the option after the last", three, `{"q1":4}`, "", false, 1, false},
		{"an option that was not offered", three, `{"q1":5}`, "", false, 1, true},
		{"option zero", three, `{"q1":0}`, "", false, 1, true},
		{"an answer that is not a number", three, `{"q1":"T-2"}`, "", false, 1, true},
		{"no answer at all", three, `not json`, "", false, 1, true},
		{"one ticket, chosen", three[:1], `{"q1":1}`, "T-1", true, 1, false},
		{"one ticket, none of these", three[:1], `{"q1":2}`, "", false, 1, false},
		{"no open ticket: nothing is asked", nil, `{"q1":1}`, "", false, 0, false},
		{"an empty list of open tickets: nothing is asked", []Ticket{}, `{"q1":1}`, "", false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &chooser{pick: tc.pick}
			ctx := schemaflux.NewClient("").WithProviderInstance(provider).Context(context.Background())
			support := New(tc.open)

			matched, err := support.match("someone@example.com", "the text of the message")(ctx)
			if tc.refused {
				if err == nil || matched != (Matched{}) {
					t.Errorf("match = %+v, %v; want an error and nothing else", matched, err)
				}
			} else if err != nil || matched.ID != tc.wantID || matched.Found != tc.wantFound {
				t.Errorf("match = %+v, %v; want %q, found %t", matched, err, tc.wantID, tc.wantFound)
			}
			if provider.calls() != tc.wantCalls {
				t.Errorf("%d calls, want %d", provider.calls(), tc.wantCalls)
			}
			if tc.wantCalls == 0 {
				return
			}
			asked := provider.prompts[0]
			for _, needle := range []string{"someone@example.com", "the text of the message", MatchQuestion} {
				if !strings.Contains(asked, needle) {
					t.Errorf("the question does not carry %q:\n%s", needle, asked)
				}
			}
			for _, ticket := range tc.open {
				if !strings.Contains(asked, fmt.Sprintf("%q", ticket.Option())) {
					t.Errorf("the options do not show %q:\n%s", ticket.Option(), asked)
				}
			}
		})
	}
}

func TestAMessageIsFiledUnderTheTicketItWasMatchedTo(t *testing.T) {
	tickets := []Ticket{
		{ID: "T-1", Customer: "a@example.com", Summary: "Same summary"},
		{ID: "T-2", Customer: "b@example.com", Summary: "Same summary"},
		{ID: "T-3", Customer: "c@example.com", Summary: "Another"},
	}
	cases := []struct {
		name    string
		open    []Ticket
		matched Matched
		want    string
		refused bool
	}{
		{"the first", tickets, Matched{ID: "T-1", Found: true}, "T-1", false},
		{"the second, with the summary of the first", tickets, Matched{ID: "T-2", Found: true}, "T-2", false},
		{"the third", tickets, Matched{ID: "T-3", Found: true}, "T-3", false},
		{"an ID that is not open", tickets, Matched{ID: "T-9", Found: true}, "", true},
		{"no ID", tickets, Matched{Found: true}, "", true},
		{"a summary where an ID goes", tickets, Matched{ID: "Same summary", Found: true}, "", true},
		{"no open tickets", nil, Matched{ID: "T-1", Found: true}, "", true},
		{"two tickets with one ID", []Ticket{{ID: "T-1"}, {ID: "T-1"}}, Matched{ID: "T-1", Found: true}, "", true},
		{"one open ticket", tickets[:1], Matched{ID: "T-1", Found: true}, "T-1", false},
		{"the ID is case sensitive", tickets, Matched{ID: "t-1", Found: true}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(tc.open).attach(tc.matched)(context.Background())
			if tc.refused {
				if err == nil || got != (filed{}) {
					t.Errorf("attach = %+v, %v; want an error and nothing else", got, err)
				}
				return
			}
			if err != nil || got.ID != tc.want || got.New {
				t.Errorf("attach = %+v, %v; want %q as an open ticket", got, err, tc.want)
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/flowdesk/sim"
	"github.com/monstercameron/schemaflux/mw"
)

// With -trace a run is followed by its calls in the order they started, each
// with what the model answered. The marks that say where a wait began are
// counted from the calls' own times and agree with the count the row prints,
// which is kept from the order of events.
func TestTheTraceIsTheRunAsItHappened(t *testing.T) {
	out := filepath.Join(t.TempDir(), "runs.jsonl")
	var stdout, stderr bytes.Buffer
	// The run is on a synctest bubble's clock, so a call that starts as another
	// ends does so at the same instant and the marks of the trace, which are
	// worked out from the calls' times, are those of the order of events.
	synctest.Test(t, func(t *testing.T) {
		if code := run([]string{"-flow", "fast", "-n", "4", "-delay", "25ms", "-trace", "-out", out}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d: %s\n%s", code, stderr.String(), stdout.String())
		}
	})
	report := stdout.String()

	for _, want := range []string{
		"spam: yes",           // M-01 is discarded on this answer
		"language: Spanish",   // M-03 is translated on this one
		"personal data: yes",  // M-04 is redacted on this one
		"none of these",       // no open ticket matched
		"translate", "redact", // the steps those answers led to
		"discarded, no ticket",   // how M-01 ended
		"ticket T-M-02 (opened)", // how M-02 ended
		"reply: ",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the trace does not say %q:\n%s", want, report)
		}
	}

	// The ten questions of the first look are all written out, and the
	// last of them are the ones that decide the path: whether a person has to
	// approve, what the desk does next, and whether the customer writes again.
	for _, want := range []string{"needs human: no", "next: close the ticket", "again: no", "next: follow up tomorrow", "again: yes"} {
		if !strings.Contains(report, want) {
			t.Errorf("the trace does not say %q, which is among the last answers of a look:\n%s", want, report)
		}
	}
	lines := strings.Split(report, "\n")
	continued := strings.Repeat(" ", len(continuation))
	look := -1
	for i, line := range lines {
		if look < 0 && strings.Contains(line, "spam: no") {
			look = i
		}
	}
	if look < 0 || look+1 >= len(lines) {
		t.Fatalf("no look at a message that is not spam in the trace:\n%s", report)
	}
	if next := lines[look+1]; !strings.HasPrefix(next, continued) || strings.HasPrefix(next, continued+" ") {
		t.Errorf("the line after the first line of a look is not aligned under its answers (%d columns):\n%s\n%s", len(continued), lines[look], next)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, continued) && utf8.RuneCountInString(line) > len(continued)+answerColumns+2 {
			t.Errorf("a line of answers is wider than the trace wraps at (%d + %d): %q", len(continued), answerColumns, line)
		}
		if strings.Contains(line, "assess") && strings.Contains(line, "…") {
			t.Errorf("a decision's question or answers are cut short: %q", line)
		}
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	records := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(records) != 4 {
		t.Fatalf("%d records for four messages", len(records))
	}
	marks := 0
	for _, line := range records {
		var made record
		if err := json.Unmarshal([]byte(line), &made); err != nil {
			t.Fatal(err)
		}
		marks += made.Waits
		for _, one := range made.Operations {
			if one.Said == "" {
				t.Errorf("%s: a %s call has no answer recorded", made.ID, one.Operation)
			}
			// The calls' times and the run's are counted from the same instant,
			// so no call starts before the run does or ends after it.
			if one.StartMS < 0 || one.StartMS+one.MS > made.WallMS+1e-6 {
				t.Errorf("%s: a %s call from %.3f ms for %.3f ms is outside a run of %.3f ms", made.ID, one.Operation, one.StartMS, one.MS, made.WallMS)
			}
		}
	}
	if got := strings.Count(report, "    wait "); got != marks {
		t.Errorf("the trace marks %d waits and the rows count %d:\n%s", got, marks, report)
	}

	t.Run("and is not printed unasked", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-flow", "fast", "-n", "2", "-delay", "5ms"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
		if strings.Contains(stdout.String(), "wait 1") || strings.Contains(stdout.String(), "spam:") {
			t.Errorf("a trace was printed without -trace:\n%s", stdout.String())
		}
	})
}

func TestWords(t *testing.T) {
	cases := map[string]string{
		"Spam": "spam", "PersonalData": "personal data", "NeedsHuman": "needs human",
		"Team": "team", "Again": "again", "A": "a", "": "", "already lower": "already lower",
		"ABC": "a b c", "WillWriteAgain": "will write again", "Éclair": "éclair", "x9Y": "x9 y",
	}
	for name, want := range cases {
		if got := words(name); got != want {
			t.Errorf("words(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	cases := []struct {
		text  string
		limit int
		want  string
	}{
		{"", 10, ""},
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"one over 10", 10, "one over…"},
		{"a word and more", 7, "a word…"},
		{"trailing space cut", 9, "trailing…"},
		{"ñandú ñandú", 6, "ñandú…"},
		{"日本語のテキスト", 4, "日本語…"},
		{"anything", 0, "anything"},
		{"anything", -3, "anything"},
		{"ab", 1, "…"},
	}
	for _, tc := range cases {
		if got := clip(tc.text, tc.limit); got != tc.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tc.text, tc.limit, got, tc.want)
		}
	}
}

func TestOneLine(t *testing.T) {
	cases := map[string]string{
		"": "", "one": "one", "two words": "two words", "a\nb": "a b", "a\r\n\r\nb": "a b",
		"  padded  ": "padded", "tab\tbed": "tab bed", "a  b   c": "a b c", "\n": "", "x\u00a0y": "x y",
	}
	for text, want := range cases {
		if got := oneLine(text); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", text, got, want)
		}
	}
}

// Every question the desk asks through a struct has a short name, and no two
// questions share one.
func TestEveryQuestionOfTheDeskHasAShortName(t *testing.T) {
	if len(short) != 11 {
		t.Errorf("%d questions named, and the look asks 10 beside the ticket match: %v", len(short), short)
	}
	seen := map[string]string{}
	for question, name := range short {
		if name == "" || strings.ContainsAny(name, "?:") {
			t.Errorf("question %q is named %q", question, name)
		}
		if other, taken := seen[name]; taken {
			t.Errorf("%q names both %q and %q", name, question, other)
		}
		seen[name] = question
	}
	for _, name := range []string{"spam", "language", "personal data", "team", "intent", "urgency", "answerable", "needs human", "next", "again", "ticket"} {
		if _, named := seen[name]; !named {
			t.Errorf("no question is named %q", name)
		}
	}
	for _, name := range []string{"foreign", "angry", "refund"} {
		if _, named := seen[name]; named {
			t.Errorf("a question that the desk no longer asks is named %q", name)
		}
	}
}

func TestFieldNamesAreTheAskTagsOfAStructAndOfWhatItEmbeds(t *testing.T) {
	type none struct{ A int }
	type one struct {
		Spam bool `ask:"Q1?"`
	}
	type two struct {
		PersonalData bool `ask:"Q2?"`
		Count        int
	}
	type embeds struct {
		one
		Other bool `ask:"Q3?"`
	}
	type deep struct {
		embeds
		Last bool `ask:"Q4?"`
	}
	type pointer struct {
		*one
		Mine bool `ask:"Q5?"`
	}
	type duplicate struct {
		A bool `ask:"Same?"`
		B bool `ask:"Same?"`
	}
	type unexported struct {
		hidden bool `ask:"Hidden?"`
	}
	type blank struct {
		A bool `ask:""`
		B bool
	}
	type tagged struct {
		one `ask:"On the embedding?"`
	}
	type named struct {
		WillWriteAgain bool `ask:"Q6?"`
		NeedsHuman     bool `ask:"Q7?"`
	}
	type neighbour struct {
		inner one
	}

	cases := []struct {
		name string
		of   reflect.Type
		want map[string]string
	}{
		{"no tags", reflect.TypeFor[none](), map[string]string{}},
		{"one tag", reflect.TypeFor[one](), map[string]string{"Q1?": "spam"}},
		{"one tag and one field with none", reflect.TypeFor[two](), map[string]string{"Q2?": "personal data"}},
		{"an embedded struct", reflect.TypeFor[embeds](), map[string]string{"Q1?": "spam", "Q3?": "other"}},
		{"embedded twice", reflect.TypeFor[deep](), map[string]string{"Q1?": "spam", "Q3?": "other", "Q4?": "last"}},
		{"an embedded pointer is not walked", reflect.TypeFor[pointer](), map[string]string{"Q5?": "mine"}},
		{"two fields that ask the same", reflect.TypeFor[duplicate](), map[string]string{"Same?": "b"}},
		{"a field that is not exported is named all the same", reflect.TypeFor[unexported](), map[string]string{"Hidden?": "hidden"}},
		{"a blank tag asks nothing", reflect.TypeFor[blank](), map[string]string{}},
		{"a tag on an embedded struct is not a question", reflect.TypeFor[tagged](), map[string]string{"Q1?": "spam"}},
		{"names are written as words", reflect.TypeFor[named](), map[string]string{"Q6?": "will write again", "Q7?": "needs human"}},
		{"a struct that is a field and not embedded is not walked", reflect.TypeFor[neighbour](), map[string]string{}},
		{"the details, which ask nothing", reflect.TypeFor[desk.Details](), map[string]string{}},
		{"the screen", reflect.TypeFor[desk.Screen](), nil},
		{"what follows the reply", reflect.TypeFor[desk.After](), nil},
		{"the triage", reflect.TypeFor[desk.Triage](), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fieldNames(tc.of)
			if tc.want == nil {
				// The desk's own types: every field is there, by the same tag.
				if len(got) != tc.of.NumField() {
					t.Errorf("%d questions named, want %d: %v", len(got), tc.of.NumField(), got)
				}
				for i := range tc.of.NumField() {
					field := tc.of.Field(i)
					if got[field.Tag.Get("ask")] != words(field.Name) {
						t.Errorf("%s is named %q", field.Name, got[field.Tag.Get("ask")])
					}
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("fieldNames = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("the questions of an embedded struct are the outer struct's", func(t *testing.T) {
		if got := fieldNames(reflect.TypeFor[desk.Second]()); len(got) != 7 {
			t.Errorf("%d questions for the second look, want 7: %v", len(got), got)
		}
		if got := fieldNames(reflect.TypeFor[desk.Look]()); len(got) != 10 {
			t.Errorf("%d questions for the look, want 10: %v", len(got), got)
		}
	})
}

func TestSaid(t *testing.T) {
	spam, _ := reflect.TypeFor[desk.Screen]().FieldByName("Spam")
	quoted, _ := json.Marshal(spam.Tag.Get("ask"))
	asking := schemaflux.CompletionRequest{UserPrompt: "Questions:\n" +
		"q1 (yes or no): " + string(quoted) + "\n" +
		"q2 (one of the options below): \"Which colour is it?\"\n  1: \"red\"\n  2: \"blue\"\n" +
		"\nSubject:\n\"x\"\n"}

	cases := []struct {
		name      string
		operation string
		req       schemaflux.CompletionRequest
		reply     string
		want      string
	}{
		{"a decision is its questions and answers", sim.OpAssess, asking, `{"q1":false,"q2":2}`, "spam: no · Which colour is it?: blue"},
		{"a question that went unanswered says so", sim.OpAssess, asking, `{"q1":true}`, "spam: yes · Which colour is it?: no answer"},
		{"a whole number written with a point is an answer", sim.OpAssess, asking, `{"q1":false,"q2":2.0}`, "spam: no · Which colour is it?: blue"},
		{"a decision whose reply cannot be read is shown as it came", sim.OpAssess, asking, `not json`, "not json"},
		{"a decision whose request cannot be read is shown as it came", sim.OpAssess, schemaflux.CompletionRequest{UserPrompt: "?"}, `{"q1":true}`, `{"q1":true}`},
		{"a draft is its text", sim.OpGenerate, schemaflux.CompletionRequest{}, "{\"body\":\"Hello,\\nthanks\"}", `{"body":"Hello,\nthanks"}`},
		{"a text over several lines is put on one", sim.OpTranslate, schemaflux.CompletionRequest{}, "Hola,\n\n  gracias", "Hola, gracias"},
		{"an empty answer is empty", sim.OpSummarize, schemaflux.CompletionRequest{}, "", ""},
		{"an operation the desk does not make", "other", schemaflux.CompletionRequest{}, "text", "text"},
		{"an assessment's JSON under another operation is not read as a decision", sim.OpExtract, asking, `{"q1":false,"q2":2}`, `{"q1":false,"q2":2}`},
		{"a long answer is clipped", sim.OpRewrite, schemaflux.CompletionRequest{}, strings.Repeat("word ", 400), strings.TrimSpace(strings.Repeat("word ", 400)[:saidLimit-1]) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := said(tc.operation, tc.req, tc.reply); got != tc.want {
				t.Errorf("said = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnswersAreCarriedOntoAnotherLineAtTheirSeparators(t *testing.T) {
	cases := []struct {
		name  string
		said  string
		width int
		want  []string
	}{
		{"one answer", "spam: no", 110, []string{"spam: no"}},
		{"nothing", "", 110, []string{""}},
		{"answers that fit on a line", "a: 1 · b: 2 · c: 3", 110, []string{"a: 1 · b: 2 · c: 3"}},
		{"answers that exactly fill a line", "aaaa · bbbb", 11, []string{"aaaa · bbbb"}},
		{"answers one character over", "aaaa · bbbb", 10, []string{"aaaa ·", "bbbb"}},
		{"three lines", "aaaa · bbbb · cccc · dddd · eeee", 11, []string{"aaaa · bbbb ·", "cccc · dddd ·", "eeee"}},
		{"an answer longer than the width stays whole", "a · " + strings.Repeat("x", 30) + " · b", 10, []string{"a ·", strings.Repeat("x", 30) + " ·", "b"}},
		{"a width of nothing puts each answer on a line of its own", "a · b · c", 0, []string{"a ·", "b ·", "c"}},
		{"widths are in characters, and not in bytes", "ñandú · ñandú · ñandú", 14, []string{"ñandú · ñandú ·", "ñandú"}},
		{"a separator without spaces is not one", "a·b · c·d", 110, []string{"a·b · c·d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapAnswers(tc.said, tc.width)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("wrapAnswers = %q, want %q", got, tc.want)
			}
			if tc.said != "" && strings.Contains(tc.said, " · ") && strings.Join(got, " ") != tc.said {
				t.Errorf("the lines do not put the answers back as they were: %q from %q", strings.Join(got, " "), tc.said)
			}
		})
	}
}

func TestPlayByPlayWritesARunAsItHappened(t *testing.T) {
	labelled := sim.Cases()[1]
	run := func(outcome desk.Outcome, calls ...call) string {
		var out bytes.Buffer
		playByPlay(&out, labelled, outcome, record{Operations: calls, WallMS: 123, Status: outcome.Status})
		return out.String()
	}
	plain := desk.Outcome{Status: desk.StatusAnswered, TicketID: "T-M-02", New: true, Reply: "A reply."}
	answers := make([]string, 13)
	for i := range answers {
		answers[i] = fmt.Sprintf("question %d: answer number %d", i+1, i+1)
	}

	cases := []struct {
		name    string
		outcome desk.Outcome
		calls   []call
		present []string
		absent  []string
		check   func(string) string
	}{
		{"sequential calls are a wait each", plain,
			[]call{{Operation: "assess", StartMS: 0, MS: 10, Said: "a"}, {Operation: "audit", StartMS: 10, MS: 10, Said: "b"}},
			[]string{"wait 1", "wait 2"}, nil, nil},
		{"calls that overlap are one wait", plain,
			[]call{{Operation: "assess", StartMS: 0, MS: 10, Said: "a"}, {Operation: "extract", StartMS: 1, MS: 10, Said: "b"}},
			[]string{"wait 1"}, []string{"wait 2"}, nil},
		{"a call that starts the very instant another ends is a new wait", plain,
			[]call{{Operation: "assess", StartMS: 0, MS: 10, Said: "a"}, {Operation: "audit", StartMS: 10, MS: 1, Said: "b"}},
			[]string{"wait 2"}, nil, nil},
		{"a call that starts within the rounding of an end is a new wait", plain,
			[]call{{Operation: "assess", StartMS: 0, MS: 10, Said: "a"}, {Operation: "audit", StartMS: 9.9996, MS: 1, Said: "b"}},
			[]string{"wait 2"}, nil, nil},
		{"a call that starts a little before another ends joins it", plain,
			[]call{{Operation: "assess", StartMS: 0, MS: 10, Said: "a"}, {Operation: "audit", StartMS: 9.99, MS: 1, Said: "b"}},
			[]string{"wait 1"}, []string{"wait 2"}, nil},
		{"calls are listed in the order they started", plain,
			[]call{{Operation: "audit", StartMS: 20, MS: 5, Said: "later"}, {Operation: "assess", StartMS: 0, MS: 5, Said: "earlier"}},
			[]string{"earlier", "later"}, nil, func(out string) string {
				if strings.Index(out, "earlier") > strings.Index(out, "later") {
					return "the call that started first is listed second"
				}
				return ""
			}},
		{"a call that failed is marked", plain,
			[]call{{Operation: "summarize", StartMS: 0, MS: 5, Failed: true}},
			[]string{"FAILED"}, nil, nil},
		{"a decision's answers are written in full and wrapped under the first", plain,
			[]call{{Operation: "assess", StartMS: 0, MS: 5, Said: strings.Join(answers, " · ")}},
			answers, nil, func(out string) string {
				lines := strings.Split(out, "\n")
				first := -1
				for i, line := range lines {
					if strings.Contains(line, "question 1:") {
						first = i
					}
				}
				if first < 0 || strings.Count(out, "answer number") != 13 {
					return "the answers are not all there"
				}
				for _, line := range lines {
					if strings.Contains(line, "answer number") && strings.Contains(line, "…") {
						return "an answer is cut short"
					}
				}
				column := strings.Index(lines[first], "question 1:")
				for _, line := range lines[first+1:] {
					if strings.Contains(line, "question ") && strings.Index(line, "question ") != column {
						return fmt.Sprintf("a continuation starts at column %d and the first line's answers at %d: %q", strings.Index(line, "question "), column, line)
					}
				}
				return ""
			}},
		{"a draft is clipped", plain,
			[]call{{Operation: "generate", StartMS: 0, MS: 5, Said: strings.Repeat("word ", 100)}},
			[]string{"…"}, nil, func(out string) string {
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, "generate") && utf8.RuneCountInString(line) > len(continuation)+170 {
						return "the draft is not clipped to 170 characters"
					}
				}
				return ""
			}},
		{"a ticket that was opened", plain, nil, []string{"ticket T-M-02 (opened)"}, nil, nil},
		{"a ticket that was attached to", desk.Outcome{Status: desk.StatusAnswered, TicketID: "T-1001"}, nil, []string{"ticket T-1001"}, []string{"(opened)"}, nil},
		{"no ticket", desk.Outcome{Status: desk.StatusDiscarded}, nil, []string{"discarded, no ticket"}, nil, nil},
		{"a redraft", desk.Outcome{Status: desk.StatusAnswered, TicketID: "T-1", Redrafted: true}, nil, []string{", redrafted after the audit"}, nil, nil},
		{"a reply", plain, nil, []string{"reply: A reply."}, nil, nil},
		{"no reply", desk.Outcome{Status: desk.StatusDiscarded}, nil, nil, []string{"reply:"}, nil},
		{"a reply is clipped and on one line", desk.Outcome{Status: desk.StatusAnswered, TicketID: "T-1", Reply: "one\ntwo " + strings.Repeat("word ", 100)}, nil,
			[]string{"reply: one two", "…"}, nil, nil},
		{"the message and what it was", plain, nil, []string{"M-02, plain billing question: October invoice | Why is my October invoice"}, nil, nil},
		{"the run's wall time", plain, nil, []string{"done    +   123 ms  answered"}, nil, nil},
		{"no calls at all", plain, nil, nil, []string{"wait 1"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := run(tc.outcome, tc.calls...)
			for _, want := range tc.present {
				if !strings.Contains(out, want) {
					t.Errorf("the play by play does not say %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(out, unwanted) {
					t.Errorf("the play by play says %q:\n%s", unwanted, out)
				}
			}
			if tc.check != nil {
				if problem := tc.check(out); problem != "" {
					t.Errorf("%s:\n%s", problem, out)
				}
			}
		})
	}
}

// stalls is a provider that holds up one operation: the first request of it
// that carries any prompt is slow, and a second request with the same prompt,
// which is what a hedge sends, is not. Choosing the stall by what the call is,
// and not by how many calls came before it, means a normal call that is slow on
// a busy machine cannot move it onto another call.
type stalls struct {
	schemaflux.Provider
	operation string
	slow      time.Duration

	mu      sync.Mutex
	stalled map[string]bool
}

func (s *stalls) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if sim.Operation(req) == s.operation {
		s.mu.Lock()
		first := !s.stalled[req.UserPrompt]
		if s.stalled == nil {
			s.stalled = map[string]bool{}
		}
		s.stalled[req.UserPrompt] = true
		s.mu.Unlock()
		if first {
			select {
			case <-time.After(s.slow):
			case <-ctx.Done():
				return schemaflux.CompletionResponse{}, ctx.Err()
			}
		}
	}
	return s.Provider.Complete(ctx, req)
}

// LAT-005 at the benchmark. A call that stalls is sent again, the run does not
// wait the stall out, and the report says how often that happened.
func TestTheHedgeIsCountedAndReported(t *testing.T) {
	asked, err := parse([]string{"-mode", "live", "-flow", "fast", "-n", "3", "-hedge", "150ms"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	cases := sim.Cases()[:3]

	t.Run("a stalled call is sent again and the run is quick", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// Every hand-off note is stalled, once. The two messages that are not
			// spam each make one, and the first copy of it is the slow one.
			stalling := &stalls{Provider: sim.New(5 * time.Millisecond), operation: sim.OpSummarize, slow: 5 * time.Second}
			var hedges hedging
			var stdout, sink bytes.Buffer
			started := time.Now()
			failed, err := liveThrough(context.Background(), asked, cases, hedges.through(stalling, asked.hedge), &hedges, "", &stdout, &sink)
			took := time.Since(started)
			if err != nil || failed {
				t.Fatalf("failed: %t, err: %v\n%s", failed, err, stdout.String())
			}
			// On the bubble's clock the time is exact. Spam is one wait of 5 ms.
			// The plain message is three waits of 5 ms with the hedge's 150 ms in
			// place of 5 of them, which is 165 ms, and the message that is
			// translated in and back is six waits and the hedge's 150 ms, which
			// is 180 ms. A stall of 5 s waited out would be over 10 s.
			if want := 350 * time.Millisecond; took != want {
				t.Errorf("the runs took %v, want %v: a stall of 5 s is not waited out", took, want)
			}
			// The three messages make 3, 6 and 11 calls in the fast flow, and the
			// hand-off notes of the two that are not spam are the two that are
			// sent twice, each answered first by its second copy.
			sent, won, calls := hedges.sentTwice.Load(), hedges.secondWon.Load(), hedges.calls.Load()
			if sent != 2 || won != 2 || calls != 20 {
				t.Errorf("%d of %d calls were sent twice and the second copy won %d: want 2 of 20 and 2, the two hand-off notes that were stalled", sent, calls, won)
			}
			hedges.report(&stdout, asked.hedge)
			want := fmt.Sprintf("hedge after 150ms: %d of %d calls were sent a second time, and the second copy answered first in %d.", sent, calls, won)
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("the report does not say %q:\n%s", want, stdout.String())
			}

			// A run in which a hedge sent a second copy has a cost that is a floor.
			hedgedRuns := 0
			for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
				var made record
				if err := json.Unmarshal([]byte(line), &made); err != nil {
					t.Fatal(err)
				}
				if made.HedgedCalls > 0 {
					hedgedRuns++
					if made.CostComplete {
						t.Errorf("%s: a hedge sent a second copy and the cost is reported as complete", made.ID)
					}
				}
				if made.HedgeMS != 150 {
					t.Errorf("%s: the record's hedge is %v ms", made.ID, made.HedgeMS)
				}
			}
			if hedgedRuns != 2 {
				t.Errorf("%d runs report a hedged call, want the two whose hand-off note stalled:\n%s", hedgedRuns, sink.String())
			}
		})
	})

	t.Run("no delay leaves the provider as it is and reports nothing", func(t *testing.T) {
		provider := sim.New(time.Millisecond)
		var hedges hedging
		for _, after := range []time.Duration{0, -time.Second} {
			if got := hedges.through(provider, after); got != schemaflux.Provider(provider) {
				t.Errorf("after %v the provider was wrapped", after)
			}
			var stdout bytes.Buffer
			hedges.report(&stdout, after)
			if stdout.Len() != 0 {
				t.Errorf("after %v the report says %q", after, stdout.String())
			}
		}
	})

	t.Run("a negative delay is refused at the flags", func(t *testing.T) {
		if _, err := parse([]string{"-hedge", "-1s"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "-hedge") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("what the observer is told is what is counted", func(t *testing.T) {
		var hedges hedging
		for range 3 {
			hedges.saw(mw.HedgeEvent{})
		}
		hedges.saw(mw.HedgeEvent{Hedged: true})
		hedges.saw(mw.HedgeEvent{Hedged: true, SecondWon: true})
		if hedges.calls.Load() != 5 || hedges.sentTwice.Load() != 2 || hedges.secondWon.Load() != 1 {
			t.Errorf("calls %d, sent twice %d, second won %d", hedges.calls.Load(), hedges.sentTwice.Load(), hedges.secondWon.Load())
		}
	})

	t.Run("a stall is chosen by the operation, and only the first request of a prompt is slow", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			stalling := &stalls{Provider: sim.New(time.Millisecond), operation: sim.OpSummarize, slow: 300 * time.Millisecond}
			summary := schemaflux.CompletionRequest{SystemPrompt: "You are a text summarization expert.", UserPrompt: "Summarize this text:\n[ref M-02]"}
			extract := schemaflux.CompletionRequest{SystemPrompt: "You are a data extraction expert.", UserPrompt: "Extract:\n[ref M-02]"}

			timed := func(req schemaflux.CompletionRequest) time.Duration {
				started := time.Now()
				if _, err := stalling.Complete(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				return time.Since(started)
			}
			if took := timed(extract); took != time.Millisecond {
				t.Errorf("a call of another operation took %v, want the simulator's 1ms", took)
			}
			if took := timed(summary); took != 301*time.Millisecond {
				t.Errorf("the first request of a stalled operation took %v, want the stall of 300ms and the simulator's 1ms", took)
			}
			if took := timed(summary); took != time.Millisecond {
				t.Errorf("a second request with the same prompt took %v, want the simulator's 1ms", took)
			}
		})
	})
}

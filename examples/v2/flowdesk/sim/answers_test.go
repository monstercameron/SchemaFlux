package sim

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/desk"
)

// An assessment's reply is {"q1":true,"q2":3}, which says nothing to a person
// reading a trace. Answers puts each answer back beside its question.
func TestAnswersAreReadBackBesideTheirQuestions(t *testing.T) {
	const message = "Questions:\n" +
		"q1 (yes or no): \"Is it spam?\"\n" +
		"q2 (one of the options below): \"Which team?\"\n" +
		"  1: \"billing\"\n" +
		"  2: \"technical\"\n" +
		"q3 (a rating from 1 to 5): \"How urgent?\"\n" +
		"\nSubject:\n\"hello\"\n"

	cases := []struct {
		name  string
		reply string
		want  []string
	}{
		{"each kind of answer", `{"q1":true,"q2":2,"q3":4}`, []string{"yes", "technical", "4"}},
		{"a no and the first option", `{"q1":false,"q2":1,"q3":1}`, []string{"no", "billing", "1"}},
		{"keys in another order", `{"q3":5,"q1":false,"q2":2}`, []string{"no", "technical", "5"}},
		{"a key that was not asked is passed over", `{"q1":true,"q2":1,"q3":2,"confidence":0.9}`, []string{"yes", "billing", "2"}},
		{"a question left out", `{"q1":true,"q3":2}`, []string{"yes", "no answer", "2"}},
		{"an option that was not offered", `{"q1":true,"q2":3,"q3":2}`, []string{"yes", "no answer", "2"}},
		{"option zero", `{"q1":true,"q2":0,"q3":2}`, []string{"yes", "no answer", "2"}},
		{"a yes or no answered with a number", `{"q1":1,"q2":1,"q3":2}`, []string{"no answer", "billing", "2"}},
		{"a rating answered in words", `{"q1":true,"q2":1,"q3":"high"}`, []string{"yes", "billing", "no answer"}},
		{"a choice answered with its label", `{"q1":true,"q2":"billing","q3":2}`, []string{"yes", "no answer", "2"}},
		{"an empty object", `{}`, []string{"no answer", "no answer", "no answer"}},
		{"a rating written as a whole number with a point", `{"q1":true,"q2":1,"q3":4.0}`, []string{"yes", "billing", "4"}},
		{"a rating written in an exponent", `{"q1":true,"q2":1,"q3":4e0}`, []string{"yes", "billing", "4"}},
		{"a choice written with a point", `{"q1":true,"q2":2.0,"q3":2}`, []string{"yes", "technical", "2"}},
		{"a rating with a fraction", `{"q1":true,"q2":1,"q3":4.5}`, []string{"yes", "billing", "no answer"}},
		{"a choice with a fraction", `{"q1":true,"q2":1.5,"q3":2}`, []string{"yes", "no answer", "2"}},
		{"a rating of null", `{"q1":true,"q2":1,"q3":null}`, []string{"yes", "billing", "no answer"}},
		{"a choice of null", `{"q1":true,"q2":null,"q3":2}`, []string{"yes", "no answer", "2"}},
		{"a rating too large to be a number of this program", `{"q1":true,"q2":1,"q3":1e12}`, []string{"yes", "billing", "no answer"}},
		{"a rating written as a string of digits", `{"q1":true,"q2":1,"q3":"4"}`, []string{"yes", "billing", "no answer"}},
		{"a rating of zero is a number", `{"q1":true,"q2":1,"q3":0}`, []string{"yes", "billing", "0"}},
		{"a negative rating", `{"q1":true,"q2":1,"q3":-2.0}`, []string{"yes", "billing", "-2"}},
		{"a yes or no written as a string", `{"q1":"true","q2":1,"q3":2}`, []string{"no answer", "billing", "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Answers(schemaflux.CompletionRequest{UserPrompt: message}, tc.reply)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("%d answers, want %d", len(got), len(tc.want))
			}
			wantQuestions := []string{"Is it spam?", "Which team?", "How urgent?"}
			for i := range got {
				if got[i].Question != wantQuestions[i] || got[i].Answer != tc.want[i] {
					t.Errorf("answer %d = %+v, want %q to %q", i+1, got[i], tc.want[i], wantQuestions[i])
				}
			}
		})
	}

	t.Run("a reply that is not an object is an error", func(t *testing.T) {
		for _, reply := range []string{"", "yes", `[true]`, `{"q1":`} {
			if _, err := Answers(schemaflux.CompletionRequest{UserPrompt: message}, reply); err == nil {
				t.Errorf("reply %q was read", reply)
			}
		}
	})

	t.Run("a request that is not an assessment is an error", func(t *testing.T) {
		_, err := Answers(schemaflux.CompletionRequest{UserPrompt: "Translate this."}, `{"q1":true}`)
		if err == nil || !strings.Contains(err.Error(), "no list of questions") {
			t.Errorf("err = %v", err)
		}
	})

	// What the simulator answers reads back as the label, for every question
	// the desk asks and every kind of answer there is.
	t.Run("the simulator's own reply reads back as the label", func(t *testing.T) {
		var asking strings.Builder
		asking.WriteString("Questions:\n")
		var fields []reflect.StructField
		var walk func(reflect.Type)
		walk = func(kind reflect.Type) {
			for i := 0; i < kind.NumField(); i++ {
				field := kind.Field(i)
				if field.Anonymous {
					walk(field.Type)
					continue
				}
				fields = append(fields, field)
			}
		}
		walk(reflect.TypeFor[desk.Look]())
		kinds := map[reflect.Kind]int{}
		for i, field := range fields {
			quoted, err := json.Marshal(field.Tag.Get("ask"))
			if err != nil {
				t.Fatal(err)
			}
			key := "q" + strconv.Itoa(i+1)
			kinds[field.Type.Kind()]++
			switch {
			case field.Tag.Get("oneof") != "":
				asking.WriteString(key + " (one of the options below): " + string(quoted) + "\n")
				for n, label := range strings.Split(field.Tag.Get("oneof"), ",") {
					asking.WriteString("  " + strconv.Itoa(n+1) + ": " + strconv.Quote(label) + "\n")
				}
			case field.Tag.Get("scale") != "":
				asking.WriteString(key + " (a rating from " + strings.Replace(field.Tag.Get("scale"), "-", " to ", 1) + "): " + string(quoted) + "\n")
			default:
				asking.WriteString(key + " (yes or no): " + string(quoted) + "\n")
			}
		}
		asking.WriteString("\nSubject:\n\"x\"\n")
		if len(fields) != 10 || kinds[reflect.Bool] != 5 || kinds[reflect.String] != 4 || kinds[reflect.Int] != 1 {
			t.Fatalf("the look asks %d questions of kinds %v; this test is for 10: 5 yes or no, 4 choices and a rating", len(fields), kinds)
		}

		for _, about := range Cases() {
			reply, err := assess(about, asking.String())
			if err != nil {
				t.Fatalf("case %s: %v", about.Message.ID, err)
			}
			got, err := Answers(schemaflux.CompletionRequest{UserPrompt: asking.String()}, reply)
			if err != nil || len(got) != len(fields) {
				t.Fatalf("case %s: %d answers, err %v", about.Message.ID, len(got), err)
			}
			// What each question's label is, written the way Answers writes it.
			yes := func(value bool) string { return map[bool]string{true: "yes", false: "no"}[value] }
			first := func(field string) string { return strings.Split(fieldTag(t, field, "oneof"), ",")[0] }
			choice := func(label, field string) string {
				if label == "" && about.Screen.Spam {
					return first(field)
				}
				return label
			}
			want := map[string]string{
				"Spam":         yes(about.Screen.Spam),
				"Language":     choice(about.Screen.Language, "Language"),
				"PersonalData": yes(about.Screen.PersonalData),
				"Team":         choice(about.Triage.Team, "Team"),
				"Intent":       choice(about.Triage.Intent, "Intent"),
				"Urgency":      strconv.Itoa(max(about.Triage.Urgency, 1)),
				"Answerable":   yes(about.Triage.Answerable),
				"NeedsHuman":   yes(about.Triage.NeedsHuman),
				"Next":         choice(about.NextAction, "Next"),
				"Again":        yes(about.WritesAgain),
			}
			for i, field := range fields {
				expected, known := want[field.Name]
				if !known {
					t.Fatalf("the test has no expectation for %s", field.Name)
				}
				if got[i].Answer != expected {
					t.Errorf("case %s, %s: read back %q, want %q", about.Message.ID, field.Name, got[i].Answer, expected)
				}
			}
		}
	})
}

// fieldTag is a tag of the field of the look that has the name.
func fieldTag(t *testing.T, name, tag string) string {
	t.Helper()
	found, ok := reflect.TypeFor[desk.Look]().FieldByName(name)
	if !ok {
		t.Fatalf("the look has no field %s", name)
	}
	return found.Tag.Get(tag)
}

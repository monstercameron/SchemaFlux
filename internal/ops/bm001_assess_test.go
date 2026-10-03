package ops

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// BM-001. Is, Which, Rate and Assess: decisions a caller branches on.

type assessCall struct {
	system, user string
	opts         types.OpOptions
	calls        int
}

// answering scripts the provider's answer and records what was sent.
func answering(t *testing.T, response string) *assessCall {
	t.Helper()
	sent := &assessCall{}
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, system, user string, opt types.OpOptions) (string, error) {
		sent.system, sent.user, sent.opts = system, user, opt
		sent.calls++
		return response, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	return sent
}

const cfMessage = "My invoice is wrong & I was charged twice <urgent>"

// The answer to a question is the answer the model gave, and an answer that is
// not of the kind that was asked for is an error. It is never read as "no", as
// the first label, or as the bottom of the scale, which is what a plain bool,
// index or number decodes a missing field to.
func TestIsReadsATrueOrFalseAndNothingElse(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     bool
		wantErr  bool
	}{
		{"yes", `{"q1":true}`, true, false},
		{"no", `{"q1":false}`, false, false},
		{"yes, with space around it", "  {\"q1\": true}\n", true, false},
		{"yes, in a code fence", "```json\n{\"q1\":true}\n```", true, false},
		{"yes, with an entry nobody asked for", `{"q1":true,"note":"sure"}`, true, false},
		{"an empty object", `{}`, false, true},
		{"null", `{"q1":null}`, false, true},
		{"the word yes", `{"q1":"yes"}`, false, true},
		{"the word true, quoted", `{"q1":"true"}`, false, true},
		{"one", `{"q1":1}`, false, true},
		{"zero", `{"q1":0}`, false, true},
		{"under another key", `{"answer":true}`, false, true},
		{"under a key in capitals", `{"Q1":true}`, false, true},
		{"a second question nobody asked", `{"q1":true,"q2":false}`, false, true},
		{"a list", `[true]`, false, true},
		{"a bare true", `true`, false, true},
		{"prose", `Yes, it is spam.`, false, true},
		{"nothing", ``, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, tc.response)
			got, err := Is(context.Background(), cfMessage, "Is this about billing?")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted as %v", got)
				}
				if got {
					t.Error("a refused answer came back as yes")
				}
				kind := types.KindOf(err)
				if kind != types.KindSchemaViolation && kind != types.KindMalformedOutput {
					t.Errorf("the error is classified %v: %v", kind, err)
				}
				if strings.Contains(err.Error(), "spam") || strings.Contains(err.Error(), "sure") || strings.Contains(err.Error(), "invoice") {
					t.Errorf("the error carries the model's or the caller's text: %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Is = %v, %v; want %v", got, err, tc.want)
			}
			if sent.calls != 1 {
				t.Errorf("%d calls for one question", sent.calls)
			}
		})
	}
}

func TestWhichReturnsOneOfTheLabelsOrAnError(t *testing.T) {
	labels := []string{"billing", "technical", "shipping"}
	cases := []struct {
		name     string
		response string
		want     string
		wantErr  bool
	}{
		{"the first", `{"q1":1}`, "billing", false},
		{"the second", `{"q1":2}`, "technical", false},
		{"the last", `{"q1":3}`, "shipping", false},
		{"written as a decimal", `{"q1":2.0}`, "technical", false},
		{"zero", `{"q1":0}`, "", true},
		{"one past the end", `{"q1":4}`, "", true},
		{"negative", `{"q1":-1}`, "", true},
		{"a fraction", `{"q1":1.5}`, "", true},
		{"the label itself", `{"q1":"billing"}`, "", true},
		{"the number as text", `{"q1":"1"}`, "", true},
		{"true", `{"q1":true}`, "", true},
		{"null", `{"q1":null}`, "", true},
		{"an empty object", `{}`, "", true},
		{"a list of two", `{"q1":[1,2]}`, "", true},
		{"prose", `billing`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answering(t, tc.response)
			got, err := Which(context.Background(), cfMessage, "Which team should handle this?", labels)
			if tc.wantErr {
				if err == nil || got != "" {
					t.Fatalf("Which = %q, %v; want an error and no label", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Which = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRateReturnsANumberOnTheScaleOrAnError(t *testing.T) {
	cases := []struct {
		name      string
		low, high int
		response  string
		want      int
		wantErr   bool
	}{
		{"the bottom", 1, 5, `{"q1":1}`, 1, false},
		{"the middle", 1, 5, `{"q1":3}`, 3, false},
		{"the top", 1, 5, `{"q1":5}`, 5, false},
		{"zero on a scale that starts at zero", 0, 10, `{"q1":0}`, 0, false},
		{"below zero on a scale that goes there", -2, 2, `{"q1":-2}`, -2, false},
		{"written as a decimal", 1, 5, `{"q1":4.0}`, 4, false},
		{"below the scale", 1, 5, `{"q1":0}`, 0, true},
		{"above the scale", 1, 5, `{"q1":6}`, 0, true},
		{"a fraction", 1, 5, `{"q1":3.5}`, 0, true},
		{"text", 1, 5, `{"q1":"high"}`, 0, true},
		{"the number as text", 1, 5, `{"q1":"3"}`, 0, true},
		{"null", 1, 5, `{"q1":null}`, 0, true},
		{"missing", 0, 10, `{}`, 0, true},
		{"true", 0, 1, `{"q1":true}`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answering(t, tc.response)
			got, err := Rate(context.Background(), cfMessage, "How urgent is this?", tc.low, tc.high)
			if tc.wantErr {
				if err == nil || got != 0 {
					t.Fatalf("Rate = %d, %v; want an error and no rating", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Rate = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

type cfTriage struct {
	Spam    bool   `ask:"Is this message spam or an automated notification?"`
	Team    string `ask:"Which team should handle it?" oneof:"billing, technical, shipping"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
	Angry   bool   `ask:"Is the writer angry?"`
	Note    string `ask:"-"`
	seen    bool
}

// Assess asks every question in one call and fills the struct, or fills
// nothing.
func TestAssessFillsEveryFieldFromOneCall(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     cfTriage
		wantErr  bool
	}{
		{"all answered", `{"q1":false,"q2":1,"q3":4,"q4":true}`, cfTriage{Team: "billing", Urgency: 4, Angry: true}, false},
		{"another set", `{"q1":true,"q2":3,"q3":1,"q4":false}`, cfTriage{Spam: true, Team: "shipping", Urgency: 1}, false},
		{"in another order", `{"q4":true,"q3":5,"q2":2,"q1":false}`, cfTriage{Team: "technical", Urgency: 5, Angry: true}, false},
		{"the first missing", `{"q2":1,"q3":4,"q4":true}`, cfTriage{}, true},
		{"the last missing", `{"q1":false,"q2":1,"q3":4}`, cfTriage{}, true},
		{"the choice missing", `{"q1":false,"q3":4,"q4":true}`, cfTriage{}, true},
		{"the choice out of range", `{"q1":false,"q2":4,"q3":4,"q4":true}`, cfTriage{}, true},
		{"the rating off the scale", `{"q1":false,"q2":1,"q3":9,"q4":true}`, cfTriage{}, true},
		{"a yes or no answered with a number", `{"q1":0,"q2":1,"q3":4,"q4":true}`, cfTriage{}, true},
		{"one more than was asked", `{"q1":false,"q2":1,"q3":4,"q4":true,"q5":true}`, cfTriage{}, true},
		{"an empty object", `{}`, cfTriage{}, true},
		{"the field names for keys", `{"Spam":false,"Team":"billing","Urgency":4,"Angry":true}`, cfTriage{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, tc.response)
			got, err := Assess[cfTriage](context.Background(), cfMessage)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted: %+v", got)
				}
				if got != (cfTriage{}) {
					t.Errorf("a refused assessment filled something in: %+v", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Assess = %+v, %v; want %+v", got, err, tc.want)
			}
			if sent.calls != 1 {
				t.Errorf("%d calls for four questions about one subject", sent.calls)
			}
		})
	}
}

// What is sent: one fixed set of instructions with nothing of the caller's in
// it, the questions and the subject in the message as written, and a declared
// answer that holds only the shape.
func TestAssessRequest(t *testing.T) {
	sent := answering(t, `{"q1":false,"q2":1,"q3":4,"q4":true}`)
	if _, err := Assess[cfTriage](context.Background(), cfMessage, types.OpOptions{Steering: "treat <refund> requests as billing"}); err != nil {
		t.Fatalf("Assess: %v", err)
	}

	if sent.system != assessSystemPrompt {
		t.Error("the instructions are not the fixed prompt")
	}
	for _, caller := range []string{"invoice", "spam", "billing", "urgent", "refund"} {
		if strings.Contains(sent.system, caller) {
			t.Errorf("the instructions carry %q, which is the caller's", caller)
		}
	}
	for _, want := range []string{
		`q1 (yes or no): "Is this message spam or an automated notification?"`,
		`q2 (one of the options below): "Which team should handle it?"`,
		`  1: "billing"`, `  2: "technical"`, `  3: "shipping"`,
		`q3 (a rating from 1 to 5): "How urgent is it?"`,
		`q4 (yes or no): "Is the writer angry?"`,
		`Guidance: "treat <refund> requests as billing"`,
		`"My invoice is wrong & I was charged twice <urgent>"`,
	} {
		if !strings.Contains(sent.user, want) {
			t.Errorf("the message has no %s:\n%s", want, sent.user)
		}
	}
	if strings.Contains(sent.user, "Note") || strings.Contains(sent.user, "q5") {
		t.Errorf("a field with no question was asked about:\n%s", sent.user)
	}
	if !strings.HasSuffix(sent.user, `<urgent>"`) {
		t.Errorf("the subject is not the last thing in the message:\n%s", sent.user)
	}
	if strings.Contains(sent.user, "confidence") {
		t.Error("a confidence was asked for, and nobody asked for one")
	}
	if sent.opts.Steering != "" {
		t.Error("the steering was left on the options, where it is added to the instructions as well")
	}

	schema := sent.opts.JSONSchema
	if schema == nil || sent.opts.SchemaName != "assessment" || sent.opts.ResponseFormat != "json" {
		t.Fatalf("schema %v, name %q, format %q", schema != nil, sent.opts.SchemaName, sent.opts.ResponseFormat)
	}
	if problem := llm.StrictSchemaProblem(schema); problem != "" {
		t.Fatalf("the schema breaks a strict-mode rule: %s", problem)
	}
	properties := schema["properties"].(map[string]any)
	if len(properties) != 4 {
		t.Errorf("the schema declares %d entries for four questions", len(properties))
	}
	if kind := properties["q1"].(map[string]any)["type"]; kind != "boolean" {
		t.Errorf("q1 is declared as %v", kind)
	}
	if options := properties["q2"].(map[string]any)["enum"].([]any); len(options) != 3 || options[0] != 1 || options[2] != 3 {
		t.Errorf("q2 may be %v", options)
	}
	if levels := properties["q3"].(map[string]any)["enum"].([]any); len(levels) != 5 || levels[0] != 1 || levels[4] != 5 {
		t.Errorf("q3 may be %v", levels)
	}
	encoded, _ := promptJSON(schema)
	for _, caller := range []string{"billing", "spam", "urgent", "Which"} {
		if strings.Contains(string(encoded), caller) {
			t.Errorf("the schema holds %q, which is the caller's", caller)
		}
	}

	// Two call sites that ask questions of the same kinds declare the same
	// answer, whatever they ask.
	type otherQuestions struct {
		Refund   bool   `ask:"Does it ask for a refund?"`
		Language string `ask:"What language is it in?" oneof:"en,fr,de"`
		Tone     int    `ask:"How polite is it?" scale:"1-5"`
		Repeat   bool   `ask:"Has the writer written before?"`
	}
	other := answering(t, `{"q1":false,"q2":1,"q3":4,"q4":true}`)
	if _, err := Assess[otherQuestions](context.Background(), "another subject"); err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if !reflect.DeepEqual(other.opts.JSONSchema, schema) {
		t.Error("two sets of questions of the same kinds declare different answers")
	}
}

// A decision defaults to the fast tier, and to the caller's tier when they
// name one.
func TestADecisionRunsOnTheFastTierUnlessToldOtherwise(t *testing.T) {
	cases := []struct {
		name string
		opts []types.OpOptions
		want types.Speed
	}{
		{"no options", nil, types.Fast},
		{"options that name no tier", []types.OpOptions{{Steering: "x"}}, types.Fast},
		{"smart", []types.OpOptions{{Intelligence: types.Smart}}, types.Smart},
		{"quick", []types.OpOptions{{Intelligence: types.Quick}}, types.Quick},
		{"fast, said outright", []types.OpOptions{{Intelligence: types.Fast}}, types.Fast},
		{"a model, and no tier", []types.OpOptions{{Model: "gpt-6-luna"}}, types.Fast},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, `{"q1":true}`)
			if _, err := Is(context.Background(), cfMessage, "Is this about billing?", tc.opts...); err != nil {
				t.Fatalf("Is: %v", err)
			}
			if sent.opts.Intelligence != tc.want {
				t.Errorf("the tier sent is %v, want %v", sent.opts.Intelligence, tc.want)
			}
		})
	}
}

// What cannot be asked is refused before anything is sent.
func TestADecisionThatCannotBeAskedIsRefusedBeforeItIsSent(t *testing.T) {
	type noQuestion struct {
		Spam bool
	}
	type stringWithNoOptions struct {
		Team string `ask:"Which team?"`
	}
	type numberWithNoScale struct {
		Urgency int `ask:"How urgent?"`
	}
	type badScale struct {
		Urgency int `ask:"How urgent?" scale:"low-high"`
	}
	type scaleTooWideForTheField struct {
		Urgency int8 `ask:"How urgent?" scale:"100-190"`
	}
	type unexportedQuestion struct {
		spam bool `ask:"Is it spam?"`
	}
	type floatField struct {
		Score float64 `ask:"How good?"`
	}
	type boolWithOptions struct {
		Spam bool `ask:"Is it spam?" oneof:"yes,no"`
	}
	type oneOption struct {
		Team string `ask:"Which team?" oneof:"billing"`
	}
	type repeatedOption struct {
		Team string `ask:"Which team?" oneof:"billing, technical, billing"`
	}
	type nothingAsked struct {
		Note string `ask:"-"`
	}
	_ = unexportedQuestion{}.spam

	ctx := context.Background()
	cases := []struct {
		name string
		run  func() error
		kind types.ErrorKind
	}{
		{"Is: a blank question", func() error { _, err := Is(ctx, cfMessage, "  "); return err }, types.KindConfiguration},
		{"Which: no labels", func() error { _, err := Which(ctx, cfMessage, "Which?", nil); return err }, types.KindConfiguration},
		{"Which: one label", func() error { _, err := Which(ctx, cfMessage, "Which?", []string{"a"}); return err }, types.KindConfiguration},
		{"Which: a blank label", func() error { _, err := Which(ctx, cfMessage, "Which?", []string{"a", " "}); return err }, types.KindConfiguration},
		{"Which: a label twice", func() error { _, err := Which(ctx, cfMessage, "Which?", []string{"a", "b", "a"}); return err }, types.KindConfiguration},
		{"Which: more labels than can be declared", func() error {
			labels := make([]string, strictEnumBudget+1)
			for i := range labels {
				labels[i] = "label " + strconv.Itoa(i)
			}
			_, err := Which(ctx, cfMessage, "Which?", labels)
			return err
		}, types.KindConfiguration},
		{"Rate: a scale of one level", func() error { _, err := Rate(ctx, cfMessage, "How?", 3, 3); return err }, types.KindConfiguration},
		{"Rate: a scale the wrong way round", func() error { _, err := Rate(ctx, cfMessage, "How?", 5, 1); return err }, types.KindConfiguration},
		{"Rate: a scale of a thousand levels", func() error { _, err := Rate(ctx, cfMessage, "How?", 1, 1000); return err }, types.KindConfiguration},
		{"a subject that says nothing", func() error { _, err := Is(ctx, lx11Opaque{secret: "x"}, "Is it?"); return err }, types.KindInvalidRequest},
		{"a subject that cannot be written", func() error { _, err := Is(ctx, make(chan int), "Is it?"); return err }, types.KindInvalidRequest},
		{"Assess: not a struct", func() error { _, err := Assess[string](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a field with no question", func() error { _, err := Assess[noQuestion](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a string with no options", func() error { _, err := Assess[stringWithNoOptions](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a number with no scale", func() error { _, err := Assess[numberWithNoScale](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a scale that is not numbers", func() error { _, err := Assess[badScale](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a scale too wide for its field", func() error { _, err := Assess[scaleTooWideForTheField](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a question on an unexported field", func() error { _, err := Assess[unexportedQuestion](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a field of a kind no question fills", func() error { _, err := Assess[floatField](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a yes or no with options", func() error { _, err := Assess[boolWithOptions](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: a choice of one", func() error { _, err := Assess[oneOption](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: an option twice", func() error { _, err := Assess[repeatedOption](ctx, cfMessage); return err }, types.KindConfiguration},
		{"Assess: nothing asked", func() error { _, err := Assess[nothingAsked](ctx, cfMessage); return err }, types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, `{"q1":true}`)
			err := tc.run()
			if err == nil || types.KindOf(err) != tc.kind {
				t.Fatalf("err = %v (%v), want %v", err, types.KindOf(err), tc.kind)
			}
			if sent.calls != 0 {
				t.Errorf("%d calls were made for a decision that cannot be asked", sent.calls)
			}
			if strings.Contains(err.Error(), "invoice") {
				t.Errorf("the error carries the subject: %v", err)
			}
		})
	}
}

// The forms that return the envelope ask for a confidence, and refuse an
// answer without one.
func TestTheResultFormsCarryAConfidence(t *testing.T) {
	ctx := context.Background()

	sent := answering(t, `{"q1":true,"confidence":0.8}`)
	is, err := IsResult(ctx, cfMessage, "Is this about billing?")
	if err != nil || !is.Value.Answer || is.Value.ModelConfidence != 0.8 {
		t.Fatalf("IsResult = %+v, %v", is.Value, err)
	}
	if is.Meta.Operation != "is" || is.Meta.DeliveredContract != types.ContractSchemaAndInvariantChecked {
		t.Errorf("the envelope: %+v", is.Meta)
	}
	if !strings.Contains(sent.user, `"confidence"`) {
		t.Error("the message does not ask for a confidence")
	}
	if _, declared := sent.opts.JSONSchema["properties"].(map[string]any)["confidence"]; !declared {
		t.Error("the schema has no confidence")
	}

	answering(t, `{"q1":2,"confidence":0.6}`)
	which, err := WhichResult(ctx, cfMessage, "Which team?", []string{"billing", "technical"})
	if err != nil || which.Value.Answer != "technical" || which.Value.ModelConfidence != 0.6 || which.Meta.Operation != "which" {
		t.Fatalf("WhichResult = %+v, %v", which, err)
	}

	answering(t, `{"q1":4,"confidence":1}`)
	rate, err := RateResult(ctx, cfMessage, "How urgent?", 1, 5)
	if err != nil || rate.Value.Answer != 4 || rate.Value.ModelConfidence != 1 || rate.Meta.Operation != "rate" {
		t.Fatalf("RateResult = %+v, %v", rate, err)
	}

	answering(t, `{"q1":false,"q2":1,"q3":4,"q4":true,"confidence":0.5}`)
	assessed, err := AssessResult[cfTriage](ctx, cfMessage)
	if err != nil || assessed.Value.Answer.Team != "billing" || assessed.Value.ModelConfidence != 0.5 || assessed.Meta.Operation != "assess" {
		t.Fatalf("AssessResult = %+v, %v", assessed, err)
	}

	for _, response := range []string{`{"q1":true}`, `{"q1":true,"confidence":null}`, `{"q1":true,"confidence":"high"}`, `{"q1":true,"confidence":1.5}`, `{"q1":true,"confidence":-0.1}`} {
		answering(t, response)
		refused, err := IsResult(ctx, cfMessage, "Is this about billing?")
		if err == nil || refused.Value.Answer || refused.Value.ModelConfidence != 0 {
			t.Errorf("%s: IsResult = %+v, %v; want an error and no answer", response, refused.Value, err)
		}
	}

	// Under a contract floor a decision cannot meet, nothing is sent.
	sent = answering(t, `{"q1":true,"confidence":0.8}`)
	floor := WithContractPolicy(ctx, types.DataPolicy{MinimumContract: types.ContractEvidenceChecked})
	if _, err := IsResult(floor, cfMessage, "Is this about billing?"); !errors.Is(err, ErrContractDegraded) || sent.calls != 0 {
		t.Errorf("err %v after %d calls; want the floor's refusal before any", err, sent.calls)
	}
}

// WhichOrNone is Which with "none of these" as an answer the model can give.
// With nothing to choose from the answer is none and nothing is asked.
func TestWhichOrNoneReturnsALabelOrNone(t *testing.T) {
	tickets := []string{"Double charge on the March invoice", "Parcel stuck at customs"}
	cases := []struct {
		name     string
		labels   []string
		question string
		response string
		want     string
		found    bool
		calls    int
		kind     types.ErrorKind // KindUnknown for no error
	}{
		{"the first", tickets, "Which ticket?", `{"q1":1}`, tickets[0], true, 1, types.KindUnknown},
		{"the second", tickets, "Which ticket?", `{"q1":2}`, tickets[1], true, 1, types.KindUnknown},
		{"none of them", tickets, "Which ticket?", `{"q1":3}`, "", false, 1, types.KindUnknown},
		{"past the end", tickets, "Which ticket?", `{"q1":4}`, "", false, 1, types.KindSchemaViolation},
		{"zero", tickets, "Which ticket?", `{"q1":0}`, "", false, 1, types.KindSchemaViolation},
		{"no answer", tickets, "Which ticket?", `{}`, "", false, 1, types.KindSchemaViolation},
		{"null", tickets, "Which ticket?", `{"q1":null}`, "", false, 1, types.KindSchemaViolation},
		{"the words none of these", tickets, "Which ticket?", `{"q1":"none of these"}`, "", false, 1, types.KindSchemaViolation},
		{"nothing to choose from", nil, "Which ticket?", `{"q1":1}`, "", false, 0, types.KindUnknown},
		{"an empty list to choose from", []string{}, "Which ticket?", `{"q1":1}`, "", false, 0, types.KindUnknown},
		{"nothing to choose from and no question", nil, "  ", `{"q1":1}`, "", false, 0, types.KindConfiguration},
		{"one label, chosen", tickets[:1], "Which ticket?", `{"q1":1}`, tickets[0], true, 1, types.KindUnknown},
		{"one label, and none", tickets[:1], "Which ticket?", `{"q1":2}`, "", false, 1, types.KindUnknown},
		{"a label that is the none option", []string{"a", "none of these"}, "Which?", `{"q1":1}`, "", false, 0, types.KindConfiguration},
		{"a label twice", []string{"a", "a"}, "Which?", `{"q1":1}`, "", false, 0, types.KindConfiguration},
		{"a blank question", tickets, " ", `{"q1":1}`, "", false, 0, types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, tc.response)
			got, found, err := WhichOrNone(context.Background(), cfMessage, tc.question, tc.labels)
			if types.KindOf(err) != tc.kind {
				t.Fatalf("err = %v (%v), want %v", err, types.KindOf(err), tc.kind)
			}
			if got != tc.want || found != tc.found {
				t.Errorf("WhichOrNone = %q, %v; want %q, %v", got, found, tc.want, tc.found)
			}
			if sent.calls != tc.calls {
				t.Errorf("%d calls, want %d", sent.calls, tc.calls)
			}
			if tc.calls == 1 && err == nil {
				last := `  ` + strconv.Itoa(len(tc.labels)+1) + `: "none of these"`
				if !strings.Contains(sent.user, last) {
					t.Errorf("the message does not offer %s as its last option:\n%s", last, sent.user)
				}
			}
		})
	}
}

type cfScreen struct {
	Spam    bool `ask:"Is this message spam?"`
	Foreign bool `ask:"Is it written in a language other than English?"`
}

type cfRouting struct {
	Team    string `ask:"Which team should handle it?" oneof:"billing,technical"`
	Urgency int    `ask:"How urgent is it?" scale:"1-3"`
}

// cfLook asks the questions of two other structs and one of its own, in one
// call.
type cfLook struct {
	cfScreen
	cfRouting
	Angry bool `ask:"Is the writer angry?"`
}

type cfLookByPointer struct {
	*cfScreen
	Angry bool `ask:"Is the writer angry?"`
}

type cfLookNamed struct {
	Screen cfScreen
	Angry  bool `ask:"Is the writer angry?"`
}

type cfDeep struct {
	cfLook
	Refund bool `ask:"Does it ask for money back?"`
}

// A struct that embeds other question structs asks all of their questions in
// one call, in the order the fields are written, and each answer goes to the
// field that asked. Two sets of questions that are useful apart, a first look
// and a triage, can then be asked together when that saves a wait, without
// being written out twice.
func TestAssessAsksTheQuestionsOfEmbeddedStructs(t *testing.T) {
	sent := answering(t, `{"q1":false,"q2":true,"q3":2,"q4":3,"q5":true}`)
	look, err := Assess[cfLook](context.Background(), cfMessage)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	want := cfLook{cfScreen: cfScreen{Foreign: true}, cfRouting: cfRouting{Team: "technical", Urgency: 3}, Angry: true}
	if look != want {
		t.Errorf("Assess = %+v, want %+v", look, want)
	}
	for _, line := range []string{
		`q1 (yes or no): "Is this message spam?"`,
		`q2 (yes or no): "Is it written in a language other than English?"`,
		`q3 (one of the options below): "Which team should handle it?"`,
		`q4 (a rating from 1 to 3): "How urgent is it?"`,
		`q5 (yes or no): "Is the writer angry?"`,
	} {
		if !strings.Contains(sent.user, line) {
			t.Errorf("the message has no %s:\n%s", line, sent.user)
		}
	}
	if strings.Contains(sent.user, "q6") || sent.calls != 1 {
		t.Errorf("%d calls; the message:\n%s", sent.calls, sent.user)
	}
	if properties := sent.opts.JSONSchema["properties"].(map[string]any); len(properties) != 5 {
		t.Errorf("the schema declares %d entries for five questions", len(properties))
	}

	cases := []struct {
		name     string
		response string
		run      func() (any, error)
		want     any
		kind     types.ErrorKind
	}{
		{"two levels of embedding", `{"q1":true,"q2":false,"q3":1,"q4":1,"q5":false,"q6":true}`,
			func() (any, error) { return Assess[cfDeep](context.Background(), cfMessage) },
			cfDeep{cfLook: cfLook{cfScreen: cfScreen{Spam: true}, cfRouting: cfRouting{Team: "billing", Urgency: 1}}, Refund: true}, types.KindUnknown},
		{"one of the embedded questions unanswered", `{"q1":true,"q3":1,"q4":1,"q5":false}`,
			func() (any, error) { return Assess[cfLook](context.Background(), cfMessage) }, cfLook{}, types.KindSchemaViolation},
		{"an embedded choice out of range", `{"q1":true,"q2":false,"q3":3,"q4":1,"q5":false}`,
			func() (any, error) { return Assess[cfLook](context.Background(), cfMessage) }, cfLook{}, types.KindSchemaViolation},
		{"an embedded rating off its scale", `{"q1":true,"q2":false,"q3":1,"q4":4,"q5":false}`,
			func() (any, error) { return Assess[cfLook](context.Background(), cfMessage) }, cfLook{}, types.KindSchemaViolation},
		{"one answer too many", `{"q1":true,"q2":false,"q3":1,"q4":1,"q5":false,"q6":true}`,
			func() (any, error) { return Assess[cfLook](context.Background(), cfMessage) }, cfLook{}, types.KindSchemaViolation},
		{"the embedded struct on its own still works", `{"q1":true,"q2":true}`,
			func() (any, error) { return Assess[cfScreen](context.Background(), cfMessage) }, cfScreen{Spam: true, Foreign: true}, types.KindUnknown},
		{"embedded by pointer", `{"q1":true,"q2":true,"q3":true}`,
			func() (any, error) { return Assess[cfLookByPointer](context.Background(), cfMessage) }, cfLookByPointer{}, types.KindConfiguration},
		{"a struct field that is not embedded", `{"q1":true,"q2":true,"q3":true}`,
			func() (any, error) { return Assess[cfLookNamed](context.Background(), cfMessage) }, cfLookNamed{}, types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answering(t, tc.response)
			got, err := tc.run()
			if types.KindOf(err) != tc.kind {
				t.Fatalf("err = %v (%v), want %v", err, types.KindOf(err), tc.kind)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

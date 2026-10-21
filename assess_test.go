package schemaflux_test

import (
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const (
	assessMsg       = "Get rich quick! Click here to claim your prize."
	assessAnswer    = `{"q1":true,"q2":2,"q3":4}`
	assessAnswerSur = `{"q1":true,"q2":2,"q3":4,"confidence":0.7}`
)

// assessTriage is the struct of most of the tests: a yes or no, a choice among
// three labels and a rating on a scale of five.
type assessTriage struct {
	Spam    bool   `ask:"Is this message spam?"`
	Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
}

// AssessEnvelope is asked about beside the fields of the struct that embeds it.
type AssessEnvelope struct {
	Language string `ask:"Which language is it written in?" oneof:"english, french"`
}

type assessEmbedding struct {
	AssessEnvelope
	Spam bool `ask:"Is this message spam?"`
}

type assessWithLeftAlone struct {
	Spam   bool   `ask:"Is this message spam?"`
	Note   string `ask:"-"`
	Weight int    `ask:"-"`
}

func TestAssessFillsEachFieldFromTheAnswerToItsQuestion(t *testing.T) {
	t.Parallel()
	model := answeringWith(assessAnswer)
	got, err := schemaflux.Assess[assessTriage](operationClient(model), assessMsg)
	if err != nil {
		t.Fatal(err)
	}
	if got != (assessTriage{Spam: true, Team: "technical", Urgency: 4}) {
		t.Errorf("got %+v", got)
	}
	req := model.Last()
	for _, text := range []string{assessMsg, "Is this message spam?", "Which team should handle it?", "How urgent is it?", "billing", "shipping", "from 1 to 5"} {
		wantText(t, req, text)
	}
	if model.Calls() != 1 || req.Schema == nil || req.SchemaName != "assessment" || req.Format != "json" || req.MaxTokens != 2000 {
		t.Errorf("%d requests, schema %v name %q format %q ceiling %d: want one request declaring the assessment schema", model.Calls(), req.Schema != nil, req.SchemaName, req.Format, req.MaxTokens)
	}
	wantNoText(t, req, "confidence")
}

func TestAssessReadsEveryKindOfAnswerAtItsEnds(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]assessTriage{
		`{"q1":false,"q2":1,"q3":1}`:   {Spam: false, Team: "billing", Urgency: 1},
		`{"q1":true,"q2":3,"q3":5}`:    {Spam: true, Team: "shipping", Urgency: 5},
		`{"q1":false,"q2":3.0,"q3":2}`: {Spam: false, Team: "shipping", Urgency: 2},
	} {
		got, err := schemaflux.Assess[assessTriage](operationClient(answeringWith(body)), assessMsg)
		if err != nil || got != want {
			t.Errorf("Assess on %s = %+v, %v, want %+v", body, got, err, want)
		}
	}
}

func TestAssessRefusesAnAnswerThatLeavesAQuestionOutAndFillsNoField(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no first answer":          `{"q2":2,"q3":4}`,
		"no second answer":         `{"q1":true,"q3":4}`,
		"no third answer":          `{"q1":true,"q2":2}`,
		"a null answer":            `{"q1":true,"q2":null,"q3":4}`,
		"nothing":                  `{}`,
		"answers under wrong keys": `{"spam":true,"team":2,"urgency":4}`,
		"text":                     `Spam, technical, 4.`,
		"empty":                    ``,
		"a list":                   `[true,2,4]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Assess[assessTriage](operationClient(answeringWith(body)), assessMsg)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "assess")
			if got != (assessTriage{}) {
				t.Errorf("a refused answer left %+v beside the error: a missing answer is never a zero value", got)
			}
		})
	}
}

func TestAssessRefusesAnAnswerOfTheWrongKindOrOffItsLabelsAndScale(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a yes written as text":        `{"q1":"true","q2":2,"q3":4}`,
		"a yes written as a number":    `{"q1":1,"q2":2,"q3":4}`,
		"a choice past the last label": `{"q1":true,"q2":4,"q3":4}`,
		"a choice of zero":             `{"q1":true,"q2":0,"q3":4}`,
		"a choice written as a label":  `{"q1":true,"q2":"technical","q3":4}`,
		"a rating above the scale":     `{"q1":true,"q2":2,"q3":6}`,
		"a rating below the scale":     `{"q1":true,"q2":2,"q3":0}`,
		"a rating with a fraction":     `{"q1":true,"q2":2,"q3":2.5}`,
		"a question that was not put":  `{"q1":true,"q2":2,"q3":4,"q4":1}`,
		"an answer given twice":        `{"q1":true,"q1":false,"q2":2,"q3":4}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Assess[assessTriage](operationClient(answeringWith(body)), assessMsg)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "assess")
			if got != (assessTriage{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestAssessIgnoresAnEntryThatAsksNothingAndLeavesAFieldTaggedDashAlone(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"q1":true,"note":"ignored"}`)
	got, err := schemaflux.Assess[assessWithLeftAlone](operationClient(model), assessMsg)
	if err != nil || got != (assessWithLeftAlone{Spam: true}) {
		t.Fatalf("Assess = %+v, %v", got, err)
	}
	if strings.Contains(model.Last().User, "q2") {
		t.Errorf("the message asks a second question: %s", excerpt(model.Last().User))
	}
}

func TestAssessAsksTheQuestionsOfAnEmbeddedStructInTheOrderTheFieldsAreWritten(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"q1":2,"q2":true}`)
	got, err := schemaflux.Assess[assessEmbedding](operationClient(model), assessMsg)
	if err != nil || got.Language != "french" || !got.Spam {
		t.Fatalf("Assess = %+v, %v", got, err)
	}
	wantText(t, model.Last(), "Which language is it written in?")
}

func TestAssessRefusesATypeItCannotAskAboutBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	type noTag struct{ Spam bool }
	type blankTag struct {
		Spam bool `ask:" "`
	}
	type stringNoOneOf struct {
		Team string `ask:"Which team?"`
	}
	type intNoScale struct {
		Urgency int `ask:"How urgent?"`
	}
	type scaleBackwards struct {
		Urgency int `ask:"How urgent?" scale:"5-1"`
	}
	type scaleTooWide struct {
		Urgency int `ask:"How urgent?" scale:"1-101"`
	}
	type scaleNotANumber struct {
		Urgency int `ask:"How urgent?" scale:"low-high"`
	}
	type oneOption struct {
		Team string `ask:"Which team?" oneof:"billing"`
	}
	type repeatedOption struct {
		Team string `ask:"Which team?" oneof:"billing,billing"`
	}
	type boolWithScale struct {
		Spam bool `ask:"Spam?" scale:"1-5"`
	}
	type mapField struct {
		Tags map[string]bool `ask:"Which tags?"`
	}
	type floatField struct {
		Score float64 `ask:"How much?" scale:"1-5"`
	}
	type unexportedAsk struct {
		spam bool `ask:"Is it spam?"`
	}
	type byPointer struct {
		*AssessEnvelope
	}
	type noFields struct{}
	type smallInt struct {
		Urgency int8 `ask:"How urgent?" scale:"1-500"`
	}

	cases := []struct {
		name string
		run  func(model *schemafluxtest.Provider) error
	}{
		{"a string", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[string](operationClient(m), assessMsg)
			return err
		}},
		{"an integer", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[int](operationClient(m), assessMsg)
			return err
		}},
		{"a pointer to a struct", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[*assessTriage](operationClient(m), assessMsg)
			return err
		}},
		{"an interface", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[any](operationClient(m), assessMsg)
			return err
		}},
		{"a slice", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[[]assessTriage](operationClient(m), assessMsg)
			return err
		}},
		{"a field with no question", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[noTag](operationClient(m), assessMsg)
			return err
		}},
		{"a blank question", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[blankTag](operationClient(m), assessMsg)
			return err
		}},
		{"a string with no oneof", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[stringNoOneOf](operationClient(m), assessMsg)
			return err
		}},
		{"an integer with no scale", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[intNoScale](operationClient(m), assessMsg)
			return err
		}},
		{"a scale that runs backwards", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[scaleBackwards](operationClient(m), assessMsg)
			return err
		}},
		{"a scale of more than a hundred levels", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[scaleTooWide](operationClient(m), assessMsg)
			return err
		}},
		{"a scale that is not two numbers", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[scaleNotANumber](operationClient(m), assessMsg)
			return err
		}},
		{"a choice of one option", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[oneOption](operationClient(m), assessMsg)
			return err
		}},
		{"a choice with a repeated option", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[repeatedOption](operationClient(m), assessMsg)
			return err
		}},
		{"a bool with a scale", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[boolWithScale](operationClient(m), assessMsg)
			return err
		}},
		{"a field of a map", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[mapField](operationClient(m), assessMsg)
			return err
		}},
		{"a float with a scale", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[floatField](operationClient(m), assessMsg)
			return err
		}},
		{"a question on a field that is not exported", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[unexportedAsk](operationClient(m), assessMsg)
			return err
		}},
		{"a struct embedded by pointer", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[byPointer](operationClient(m), assessMsg)
			return err
		}},
		{"a struct with no question at all", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[noFields](operationClient(m), assessMsg)
			return err
		}},
		{"a scale that does not fit the field", func(m *schemafluxtest.Provider) error {
			_, err := schemaflux.Assess[smallInt](operationClient(m), assessMsg)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(assessAnswer)
			err := tc.run(model)
			wantError(t, err, schemaflux.ErrConfiguration, "assess")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestAssessRefusesATypeItCannotAskAboutWithARecordOfNoRequestsAndTheDetailTwinsName(t *testing.T) {
	t.Parallel()
	type noTag struct{ Spam bool }
	var recorder schemaflux.Recorder
	model := answeringWith(assessAnswer)
	ctx := operationClient(model, recordingTo(&recorder))
	_, err := schemaflux.Assess[noTag](ctx, assessMsg)
	wantError(t, err, schemaflux.ErrConfiguration, "assess")
	got, err := schemaflux.AssessDetail[noTag](ctx, assessMsg)
	wantError(t, err, schemaflux.ErrConfiguration, "assessdetail")
	if got != (schemaflux.Judged[noTag]{}) {
		t.Errorf("a refused call left %+v beside the error", got)
	}
	records := recorder.Records()
	if len(records) != 2 || records[0].Op != "assess" || records[1].Op != "assessdetail" || records[0].Requests != 0 || records[1].Requests != 0 || model.Calls() != 0 {
		t.Errorf("the recorder holds %+v after %d requests, want one Record each with no request", records, model.Calls())
	}
}

func TestAssessAsksOnceNoMatterHowManyQuestions(t *testing.T) {
	t.Parallel()
	model := answeringWith(assessAnswer)
	if _, err := schemaflux.Assess[assessTriage](operationClient(model), assessMsg); err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 1 {
		t.Errorf("%d requests were sent for three questions", model.Calls())
	}
}

func TestAssessReadsTheSameTypeOnManyGoroutines(t *testing.T) {
	t.Parallel()
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, err := schemaflux.Assess[assessTriage](operationClient(answeringWith(assessAnswer)), assessMsg)
			if err != nil || got != (assessTriage{Spam: true, Team: "technical", Urgency: 4}) {
				t.Errorf("Assess = %+v, %v", got, err)
			}
		}()
	}
	wait.Wait()
}

func TestAssessDetailReturnsTheTypeWithTheModelsConfidenceInTheAnswersTogether(t *testing.T) {
	t.Parallel()
	model := answeringWith(assessAnswerSur)
	got, err := schemaflux.AssessDetail[assessTriage](operationClient(model), assessMsg)
	if err != nil {
		t.Fatal(err)
	}
	if got != (schemaflux.Judged[assessTriage]{Answer: assessTriage{Spam: true, Team: "technical", Urgency: 4}, ModelConfidence: 0.7}) {
		t.Errorf("got %+v", got)
	}
	wantText(t, model.Last(), "confidence")
}

func TestAssessDetailAsksAnotherQuestionThanAssessDoes(t *testing.T) {
	t.Parallel()
	plain, detail := answeringWith(assessAnswer), answeringWith(assessAnswerSur)
	if _, err := schemaflux.Assess[assessTriage](operationClient(plain), assessMsg); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.AssessDetail[assessTriage](operationClient(detail), assessMsg); err != nil {
		t.Fatal(err)
	}
	wantNoText(t, plain.Last(), "confidence")
	wantText(t, detail.Last(), "confidence")
}

func TestAssessDetailRefusesAnAnswerWithoutItsConfidenceOrWithAMissingQuestionAndReturnsNothing(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"no confidence":           assessAnswer,
		"a confidence above one":  `{"q1":true,"q2":2,"q3":4,"confidence":3}`,
		"a confidence below zero": `{"q1":true,"q2":2,"q3":4,"confidence":-0.2}`,
		"a confidence as text":    `{"q1":true,"q2":2,"q3":4,"confidence":"high"}`,
		"a missing answer":        `{"q1":true,"q3":4,"confidence":0.9}`,
		"text":                    `Spam.`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.AssessDetail[assessTriage](operationClient(answeringWith(body)), assessMsg)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "assessdetail")
			if got != (schemaflux.Judged[assessTriage]{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestAssessGoesThroughTheDecisionProviderWithOneTypedQuestionForEachField(t *testing.T) {
	t.Parallel()
	decider := schemafluxtest.NewDecider(schemafluxtest.No(0.9), schemafluxtest.Chose("shipping", 0.8), schemafluxtest.Level(2, 0.95))
	got, err := schemaflux.Assess[assessTriage](decidingClient(decider), assessMsg)
	if err != nil || got != (assessTriage{Spam: false, Team: "shipping", Urgency: 2}) {
		t.Fatalf("Assess = %+v, %v", got, err)
	}
	qs := decider.Last().Decision.Questions
	if len(qs) != 3 || qs[0].Kind != schemaflux.DecisionYesNo || qs[1].Kind != schemaflux.DecisionChoice || qs[2].Kind != schemaflux.DecisionScale {
		t.Fatalf("the decider was sent %+v", qs)
	}
	if qs[1].Ask != "Which team should handle it?" || len(qs[1].Options) != 3 || qs[2].Low != 1 || qs[2].High != 5 {
		t.Errorf("the decider was sent %+v", qs)
	}

	detail := schemafluxtest.NewDecider(schemafluxtest.No(0.9), schemafluxtest.Chose("shipping", 0.6), schemafluxtest.Level(2, 0.95))
	j, err := schemaflux.AssessDetail[assessTriage](decidingClient(detail), assessMsg)
	if err != nil || j.ModelConfidence != 0.6 || j.Answer.Team != "shipping" {
		t.Errorf("AssessDetail = %+v, %v: the confidence is the lowest the decision provider reported", j, err)
	}
}

func TestAssessRefusesATypedAnswerThatLeavesAQuestionUnanswered(t *testing.T) {
	t.Parallel()
	decider := schemafluxtest.NewDecider(schemafluxtest.No(0.9), schemafluxtest.Chose("shipping", 0.8))
	got, err := schemaflux.Assess[assessTriage](decidingClient(decider), assessMsg)
	wantError(t, err, schemaflux.ErrConfiguration, "assess")
	if got != (assessTriage{}) {
		t.Errorf("a failed call left %+v beside the error", got)
	}
}

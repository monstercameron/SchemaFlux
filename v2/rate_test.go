package schemaflux_test

import (
	"math"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const (
	rateAsk = "How urgent is this?"
	rateMsg = "My site is down and customers are leaving."
)

func TestRateReturnsTheLevelOnTheScaleAndSendsTheScale(t *testing.T) {
	t.Parallel()
	for level := 1; level <= 5; level++ {
		model := answeringWith(`{"q1":` + string(rune('0'+level)) + `}`)
		got, err := schemaflux.Rate(operationClient(model), rateMsg, rateAsk, 1, 5)
		if err != nil || got != level {
			t.Fatalf("Rate = %d, %v, want %d", got, err, level)
		}
		req := model.Last()
		wantText(t, req, rateAsk)
		wantText(t, req, rateMsg)
		wantText(t, req, "from 1 to 5")
		if model.Calls() != 1 || req.Schema == nil || req.SchemaName != "assessment" || req.Format != "json" || req.MaxTokens != 2000 {
			t.Errorf("%d requests, schema %v name %q format %q ceiling %d", model.Calls(), req.Schema != nil, req.SchemaName, req.Format, req.MaxTokens)
		}
		wantNoText(t, req, "confidence")
	}
}

func TestRateReadsAScaleThatStartsBelowZero(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]int{`{"q1":-2}`: -2, `{"q1":0}`: 0, `{"q1":2}`: 2, `{"q1":-1.0}`: -1} {
		got, err := schemaflux.Rate(operationClient(answeringWith(body)), rateMsg, rateAsk, -2, 2)
		if err != nil || got != want {
			t.Errorf("Rate on %s = %d, %v, want %d", body, got, err, want)
		}
	}
}

func TestRateRefusesAnAnswerOffTheScaleAndNeverTheNearestEnd(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"below the scale":             `{"q1":0}`,
		"above the scale":             `{"q1":6}`,
		"far above":                   `{"q1":600000}`,
		"negative":                    `{"q1":-3}`,
		"a fraction":                  `{"q1":3.5}`,
		"a number written as text":    `{"q1":"3"}`,
		"a word":                      `{"q1":"urgent"}`,
		"a boolean":                   `{"q1":true}`,
		"null":                        `{"q1":null}`,
		"no answer":                   `{}`,
		"an answer under another key": `{"rating":3}`,
		"a question that was not put": `{"q1":3,"q2":3}`,
		"two answers":                 `{"q1":3,"q1":4}`,
		"text":                        `About a 4.`,
		"empty":                       ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Rate(operationClient(answeringWith(body)), rateMsg, rateAsk, 1, 5)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "rate")
			if got != 0 {
				t.Errorf("a refused answer was read as %d", got)
			}
		})
	}
}

func TestRateRefusesAScaleWithNothingToChooseBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		low, high int
		message   string
	}{
		{"low equal to high", 3, 3, "the scale runs from 3 to 3, which leaves nothing to choose"},
		{"low above high", 5, 1, "the scale runs from 5 to 1, which leaves nothing to choose"},
		{"zero to zero", 0, 0, "the scale runs from 0 to 0, which leaves nothing to choose"},
		{"a scale of a hundred and one levels", 0, 100, "the scale runs from 0 to 100, which is more than the 100 levels a scale may have"},
		{"a scale wider than the integers", math.MinInt, math.MaxInt, ""},
		{"a scale as wide as the integers can say", math.MinInt + 1, math.MaxInt, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(`{"q1":1}`)
			got, err := schemaflux.Rate(operationClient(model), rateMsg, rateAsk, tc.low, tc.high)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "rate")
			if (tc.message != "" && e.Message != tc.message) || model.Calls() != 0 || got != 0 {
				t.Errorf("message %q after %d requests, answer %d: want %q and nothing sent", e.Message, model.Calls(), got, tc.message)
			}
		})
	}
}

func TestRateAcceptsTheWidestScaleAHundredLevels(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Rate(operationClient(answeringWith(`{"q1":100}`)), rateMsg, rateAsk, 1, 100)
	if err != nil || got != 100 {
		t.Errorf("Rate on a scale of 100 levels = %d, %v", got, err)
	}
	got, err = schemaflux.Rate(operationClient(answeringWith(`{"q1":-50}`)), rateMsg, rateAsk, -100, -1)
	if err != nil || got != -50 {
		t.Errorf("Rate on a scale of negative levels = %d, %v", got, err)
	}
	_, err = schemaflux.Rate(operationClient(answeringWith(`{"q1":1}`)), rateMsg, rateAsk, 1, 101)
	wantError(t, err, schemaflux.ErrInvalidRequest, "rate")
}

func TestRateChecksTheQuestionBeforeTheScale(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Rate(operationClient(answeringWith(`{"q1":1}`)), rateMsg, "\t", 5, 1)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "rate"); e.Message != "question is blank" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestRateDetailReturnsTheLevelWithTheModelsConfidence(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"q1":4,"confidence":0.75}`)
	got, err := schemaflux.RateDetail(operationClient(model), rateMsg, rateAsk, 1, 5)
	if err != nil || got != (schemaflux.Judged[int]{Answer: 4, ModelConfidence: 0.75}) {
		t.Fatalf("RateDetail = %+v, %v", got, err)
	}
	wantText(t, model.Last(), "confidence")
	wantText(t, model.Last(), "from 1 to 5")
}

func TestRateDetailAsksAnotherQuestionThanRateDoes(t *testing.T) {
	t.Parallel()
	plain, detail := answeringWith(`{"q1":4}`), answeringWith(`{"q1":4,"confidence":0.75}`)
	if _, err := schemaflux.Rate(operationClient(plain), rateMsg, rateAsk, 1, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.RateDetail(operationClient(detail), rateMsg, rateAsk, 1, 5); err != nil {
		t.Fatal(err)
	}
	wantNoText(t, plain.Last(), "confidence")
	wantText(t, detail.Last(), "confidence")
}

func TestRateDetailRefusesAnAnswerWithoutItsConfidenceOrOffTheScale(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"no confidence":             `{"q1":4}`,
		"a confidence above one":    `{"q1":4,"confidence":1.01}`,
		"a confidence below zero":   `{"q1":4,"confidence":-1}`,
		"a confidence that is text": `{"q1":4,"confidence":"sure"}`,
		"off the scale":             `{"q1":9,"confidence":0.9}`,
		"text":                      `4 (90%)`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.RateDetail(operationClient(answeringWith(body)), rateMsg, rateAsk, 1, 5)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "ratedetail")
			if got != (schemaflux.Judged[int]{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestRateDetailRefusesABlankQuestionAScaleAndABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"q1":4,"confidence":0.75}`)
	ctx := operationClient(model)
	_, err := schemaflux.RateDetail(ctx, rateMsg, " ", 1, 5)
	wantError(t, err, schemaflux.ErrInvalidRequest, "ratedetail")
	_, err = schemaflux.RateDetail(ctx, rateMsg, rateAsk, 5, 5)
	wantError(t, err, schemaflux.ErrInvalidRequest, "ratedetail")
	_, err = schemaflux.RateDetail(ctx, rateMsg, rateAsk, 1, 5, schemaflux.Options{MaxOutputTokens: -2})
	wantError(t, err, schemaflux.ErrConfiguration, "ratedetail")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestRateGoesThroughTheDecisionProviderAsAScaleQuestion(t *testing.T) {
	t.Parallel()
	decider := schemafluxtest.NewDecider(schemafluxtest.Level(4, 0.9))
	got, err := schemaflux.Rate(decidingClient(decider), rateMsg, rateAsk, 1, 5)
	if err != nil || got != 4 {
		t.Fatalf("Rate = %d, %v", got, err)
	}
	q := decider.Last().Decision.Questions[0]
	if q.Kind != schemaflux.DecisionScale || q.Ask != rateAsk || q.Low != 1 || q.High != 5 {
		t.Errorf("the decider was sent %+v", q)
	}
	off := schemafluxtest.NewDecider(schemafluxtest.Level(9, 0.9))
	got, err = schemaflux.Rate(decidingClient(off), rateMsg, rateAsk, 1, 5)
	wantError(t, err, schemaflux.ErrInvalidAnswer, "rate")
	if got != 0 {
		t.Errorf("a level off the scale was read as %d", got)
	}
}

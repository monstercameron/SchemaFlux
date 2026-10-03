// package ops - Predict operation for forecasting and extrapolation
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PredictOptions configures the Predict operation
type PredictOptions struct {
	CommonOptions
	types.OpOptions

	// Prediction horizon (e.g., "next_week", "next_quarter", "1 year")
	Horizon string

	// Include confidence intervals
	IncludeConfidenceInterval bool

	// ConfidenceLevel is the level the interval is stated at (e.g., 0.95 for
	// 95%). An interval the model returns at another level is refused.
	ConfidenceLevel float64

	// Prediction method hint ("trend", "pattern", "regression", "auto")
	Method string

	// Factors to consider in prediction. They guide the model and are not
	// checked against its answer.
	Factors []string

	// Assumptions to use. They guide the model and are not checked against its
	// answer.
	Assumptions []string

	// IncludeScenarios asks for alternative scenarios, NumScenarios of them. An
	// answer with a different number is refused.
	IncludeScenarios bool

	// Number of scenarios to generate
	NumScenarios int

	// IncludeReasoning asks for an explanation of the prediction. An answer
	// without one is refused.
	IncludeReasoning bool

	// Historical data context window
	HistoryWindow string
}

// NewPredictOptions creates PredictOptions with defaults
func NewPredictOptions() PredictOptions {
	return PredictOptions{
		CommonOptions: CommonOptions{
			Mode:         types.Creative,
			Intelligence: types.Smart,
		},
		Horizon:                   "next",
		IncludeConfidenceInterval: true,
		ConfidenceLevel:           0.8,
		Method:                    "auto",
		IncludeScenarios:          false,
		NumScenarios:              3,
		IncludeReasoning:          true,
	}
}

// Validate validates PredictOptions
func (p PredictOptions) Validate() error {
	if err := p.CommonOptions.Validate(); err != nil {
		return err
	}
	if err := checkLockedLimits(p); err != nil {
		return err
	}
	if p.Horizon == "" {
		return errors.New("prediction horizon is required")
	}
	if !(p.ConfidenceLevel >= 0 && p.ConfidenceLevel <= 1) {
		return fmt.Errorf("confidence level must be between 0 and 1, got %f", p.ConfidenceLevel)
	}
	if p.IncludeConfidenceInterval && p.ConfidenceLevel == 0 {
		return errors.New("an interval needs a confidence level above 0")
	}
	validMethods := map[string]bool{"trend": true, "pattern": true, "regression": true, "auto": true}
	if p.Method != "" && !validMethods[p.Method] {
		return fmt.Errorf("invalid method: %s", p.Method)
	}
	if p.NumScenarios < 1 {
		return fmt.Errorf("num scenarios must be at least 1, got %d", p.NumScenarios)
	}
	return nil
}

// WithHorizon sets the prediction horizon
func (p PredictOptions) WithHorizon(horizon string) PredictOptions {
	p.Horizon = horizon
	return p
}

// WithIncludeConfidenceInterval enables confidence intervals
func (p PredictOptions) WithIncludeConfidenceInterval(include bool) PredictOptions {
	p.IncludeConfidenceInterval = include
	return p
}

// WithConfidenceLevel sets the confidence level
func (p PredictOptions) WithConfidenceLevel(level float64) PredictOptions {
	p.ConfidenceLevel = level
	return p
}

// WithMethod sets the prediction method
func (p PredictOptions) WithMethod(method string) PredictOptions {
	p.Method = method
	return p
}

// WithFactors sets factors to consider
func (p PredictOptions) WithFactors(factors []string) PredictOptions {
	p.Factors = factors
	return p
}

// WithAssumptions sets assumptions
func (p PredictOptions) WithAssumptions(assumptions []string) PredictOptions {
	p.Assumptions = assumptions
	return p
}

// WithIncludeScenarios enables scenario generation
func (p PredictOptions) WithIncludeScenarios(include bool) PredictOptions {
	p.IncludeScenarios = include
	return p
}

// WithNumScenarios sets number of scenarios
func (p PredictOptions) WithNumScenarios(num int) PredictOptions {
	p.NumScenarios = num
	return p
}

// WithIncludeReasoning enables reasoning explanation
func (p PredictOptions) WithIncludeReasoning(include bool) PredictOptions {
	p.IncludeReasoning = include
	return p
}

// WithHistoryWindow sets the history window
func (p PredictOptions) WithHistoryWindow(window string) PredictOptions {
	p.HistoryWindow = window
	return p
}

// WithSteering sets the steering prompt
func (p PredictOptions) WithSteering(steering string) PredictOptions {
	p.CommonOptions = p.CommonOptions.WithSteering(steering)
	return p
}

// WithMode sets the mode
func (p PredictOptions) WithMode(mode types.Mode) PredictOptions {
	p.CommonOptions = p.CommonOptions.WithMode(mode)
	return p
}

// WithIntelligence sets the intelligence level
func (p PredictOptions) WithIntelligence(intelligence types.Speed) PredictOptions {
	p.CommonOptions = p.CommonOptions.WithIntelligence(intelligence)
	return p
}

func (p PredictOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(p.CommonOptions, p.OpOptions)
}

func (p PredictOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return p.CommonOptions, p.OpOptions
}

// PredictionInterval represents a confidence interval
type PredictionInterval struct {
	Lower           float64 `json:"lower"`
	Upper           float64 `json:"upper"`
	ConfidenceLevel float64 `json:"confidence_level"`
}

// PredictionScenario represents an alternative scenario
type PredictionScenario struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prediction  any      `json:"prediction"`
	Probability float64  `json:"probability"`
	Conditions  []string `json:"conditions"`
}

// PredictionFactor represents a factor affecting the prediction
type PredictionFactor struct {
	Name      string  `json:"name"`
	Impact    string  `json:"impact"` // "positive", "negative", "neutral"
	Weight    float64 `json:"weight"`
	Reasoning string  `json:"reasoning,omitempty"`
}

// predictResponseSchema is the schema Predict declares for its response. It is
// nil when the prediction type cannot be expressed.
//
// It used to declare only the two fields whose shape depends on T and leave
// additionalProperties open, so the envelope's other parts stayed optional
// without the schema having to track the struct. The live API refuses an open
// object in strict mode, so Predict could not complete a call. A closed schema
// is the only kind strict mode accepts and it forbids anything undeclared, so
// every part of the envelope is declared here.
// TestPredictSchemaCoversTheEnvelope checks this schema, not a copy of it,
// against PredictResult and PredictionScenario.
//
// What the caller asked for must be a real value: reasoning when
// IncludeReasoning is set, scenarios when IncludeScenarios is set. The interval
// stays a union with null even when IncludeConfidenceInterval is set, on
// purpose: it is three numbers, and a prediction that is not a quantity has no
// interval. Requiring one would make the model produce numbers for a forecast
// that has none.
func predictResponseSchema(predictionSchema map[string]any, opts PredictOptions) map[string]any {
	text := map[string]any{"type": "string"}
	number := map[string]any{"type": "number"}
	stringList := listOf(text)

	scenario := strictEnvelope(map[string]any{
		"name":        text,
		"description": text,
		"prediction":  predictionSchema,
		"probability": number,
		"conditions":  stringList,
	}, "name", "description", "prediction", "probability", "conditions")

	required := []string{"prediction", "confidence"}
	if opts.IncludeReasoning {
		required = append(required, "reasoning")
	}
	if opts.IncludeScenarios {
		required = append(required, "scenarios")
	}
	return strictEnvelope(map[string]any{
		"prediction":  predictionSchema,
		"confidence":  number,
		"interval":    typePlanFor(reflect.TypeOf(PredictionInterval{})).sharedSchema(),
		"scenarios":   listOf(scenario),
		"factors":     typePlanFor(reflect.TypeOf([]PredictionFactor{})).sharedSchema(),
		"reasoning":   text,
		"assumptions": stringList,
		"risks":       stringList,
	}, required...)
}

// PredictResult contains the results of prediction
type PredictResult[T any] struct {
	Prediction T `json:"prediction"`
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts. An
	// answer that does not report one from 0 to 1 is refused.
	ModelConfidence float64              `json:"confidence"`
	Interval        *PredictionInterval  `json:"interval,omitempty"`
	Scenarios       []PredictionScenario `json:"scenarios,omitempty"`
	Factors         []PredictionFactor   `json:"factors,omitempty"`
	Reasoning       string               `json:"reasoning,omitempty"`
	Assumptions     []string             `json:"assumptions,omitempty"`
	Risks           []string             `json:"risks,omitempty"`
	// Metadata is not populated.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Predict forecasts or extrapolates based on patterns in historical data.
// Uses LLM to identify trends, patterns, and make informed predictions.
//
// Type parameter T specifies the type of the prediction output.
//
// The answer is checked before it is used, and refused when it breaks what was
// asked: no prediction, a confidence outside 0 to 1, no reasoning when
// IncludeReasoning is set, a number of scenarios other than NumScenarios when
// IncludeScenarios is set, a scenario probability outside 0 to 1, a factor
// whose impact is not positive, negative or neutral, an interval that is
// inverted or stated at another level than ConfidenceLevel. An interval may be
// absent even when one was asked for: a prediction that is not a quantity has
// none.
//
// Examples:
//
//	// Predict next quarter metrics
//	result, err := Predict[Metrics](historicalData, NewPredictOptions().
//	    WithHorizon("next_quarter").
//	    WithIncludeConfidenceInterval(true))
//
//	// Predict with scenarios
//	result, err := Predict[Forecast](salesData, NewPredictOptions().
//	    WithHorizon("1 year").
//	    WithIncludeScenarios(true).
//	    WithNumScenarios(3))
//
//	// Predict with specific factors
//	result, err := Predict[RiskAssessment](data, NewPredictOptions().
//	    WithFactors([]string{"market_trends", "seasonality", "competition"}).
//	    WithAssumptions([]string{"no major policy changes"}))
func Predict[T any](historicalData any, opts PredictOptions) (PredictResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting predict operation")

	// Validate options
	if err := opts.Validate(); err != nil {
		return PredictResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Marshal historical data
	dataJSON, err := json.Marshal(historicalData)
	if err != nil {
		log.Error("Predict operation failed: marshal error", "error", err)
		return PredictResult[T]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal historical data: %w", err))
	}

	// Build method description. Method is one of the four Validate allows.
	methodDesc := ""
	switch opts.Method {
	case "trend":
		methodDesc = "Focus on identifying and extrapolating trends."
	case "pattern":
		methodDesc = "Focus on identifying repeating patterns (cycles, seasonality)."
	case "regression":
		methodDesc = "Use regression-like analysis to predict outcomes."
	case "auto":
		methodDesc = "Automatically determine the best prediction approach."
	}

	// The horizon, factors, assumptions and history window are the caller's
	// text, so each is written as a JSON string.
	factorsDesc := ""
	if len(opts.Factors) > 0 {
		factorsDesc = fmt.Sprintf("\nConsider these factors: %s", parsePromptQuote(opts.Factors))
	}

	assumptionsDesc := ""
	if len(opts.Assumptions) > 0 {
		assumptionsDesc = fmt.Sprintf("\nAssumptions: %s", parsePromptQuote(opts.Assumptions))
	}

	intervalNote := ""
	if opts.IncludeConfidenceInterval {
		intervalNote = fmt.Sprintf("\nInclude %.0f%% confidence interval.", opts.ConfidenceLevel*100)
	}

	scenariosNote := ""
	if opts.IncludeScenarios {
		scenariosNote = fmt.Sprintf("\nGenerate %d alternative scenarios (optimistic, pessimistic, etc.).", opts.NumScenarios)
	}

	reasoningNote := ""
	if opts.IncludeReasoning {
		reasoningNote = "\nExplain the reasoning behind the prediction."
	}

	historyNote := ""
	if opts.HistoryWindow != "" {
		historyNote = fmt.Sprintf("\nFocus on the %s of historical data.", parsePromptQuote(opts.HistoryWindow))
	}

	systemPrompt := fmt.Sprintf(`You are an expert at forecasting and prediction. Analyze the data and make predictions.

Prediction horizon: %s
Method: %s%s%s%s%s%s%s

Return a JSON object with:
{
  "prediction": {},
  "confidence": 0.75,
  "interval": {
    "lower": 0,
    "upper": 0,
    "confidence_level": 0.8
  },
  "scenarios": [
    {
      "name": "Optimistic",
      "description": "If conditions are favorable...",
      "prediction": {},
      "probability": 0.25,
      "conditions": ["condition1", "condition2"]
    }
  ],
  "factors": [
    {
      "name": "Factor name",
      "impact": "positive|negative|neutral",
      "weight": 0.3,
      "reasoning": "Why this factor matters"
    }
  ],
  "reasoning": "Explanation of prediction logic",
  "assumptions": ["assumptions made"],
  "risks": ["potential risks to prediction accuracy"]
}`, parsePromptQuote(opts.Horizon), methodDesc, factorsDesc, assumptionsDesc, intervalNote, scenariosNote, reasoningNote, historyNote)

	userPrompt := fmt.Sprintf("Based on this historical data, predict %s:\n\n%s", parsePromptQuote(opts.Horizon), string(dataJSON))

	// The prose template above hard-codes `"prediction": {}`, but that field's
	// type is the caller's T -- `Predict[float64]` asks for a number and is
	// shown an object. Anything that takes the template at its word produces an
	// answer this operation then rejects as malformed. The prose cannot be fixed
	// in place, because it is one fixed string and T varies per call; the shape
	// has to be declared from T instead.
	var predictionZero T
	// The type's schema is the plan's own: the envelope holds it as it is.
	if schema := predictResponseSchema(typePlanFor(reflect.TypeOf(predictionZero)).sharedSchema(), opts); schema != nil {
		opt.JSONSchema = schema
		opt.SchemaName = "prediction"
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Predict operation LLM call failed", "error", err)
		return PredictResult[T]{}, fmt.Errorf("prediction failed: %w", err)
	}

	// Parse the response. The prediction stays raw until it has been looked at,
	// so that an answer without one is told from one that predicts the zero
	// value, and a confidence of zero is the model saying it is confident of
	// nothing.
	//
	// The prompt asks for the prediction, the confidence, the factors, the
	// assumptions and the risks every time, so an answer that leaves one out is
	// refused by name. The others are asked for by an option or may be left out,
	// and are optional here: the interval, which a prediction that is not a
	// quantity does not have (IncludeConfidenceInterval asks for one and an answer
	// may still have none), the scenarios (IncludeScenarios) and the reasoning
	// (IncludeReasoning). When those two options are on, checkPredictionEnvelope
	// refuses an answer without them.
	var parsed struct {
		Prediction      json.RawMessage      `json:"prediction"`
		ModelConfidence float64              `json:"confidence"`
		Interval        *PredictionInterval  `json:"interval,omitempty"`
		Scenarios       []PredictionScenario `json:"scenarios,omitempty"`
		Factors         []PredictionFactor   `json:"factors"`
		Reasoning       string               `json:"reasoning,omitempty"`
		Assumptions     []string             `json:"assumptions"`
		Risks           []string             `json:"risks"`
	}
	if err := decodeAnswer("predict", response, &parsed); err != nil {
		log.Error("Predict operation failed: parse error", "error", err)
		return PredictResult[T]{}, fmt.Errorf("failed to parse prediction result: %w", err)
	}

	// The reader refuses an answer without a prediction; a RawMessage can hold a
	// null, which is not a prediction.
	if rawValueAbsent(parsed.Prediction) {
		return PredictResult[T]{}, types.Newf(types.KindSchemaViolation, "predict", "prediction is null")
	}
	// Decoded into a local, so that nothing a failed decode filled in reaches
	// the result.
	var prediction T
	if err := decodeCallerAnswer("predict", string(parsed.Prediction), &prediction); err != nil {
		return PredictResult[T]{}, fmt.Errorf("failed to parse prediction result: %w", err)
	}

	if err := checkPredictionEnvelope(opts, parsed.ModelConfidence, parsed.Interval, parsed.Scenarios, parsed.Factors, parsed.Reasoning); err != nil {
		log.Error("Predict operation failed: the answer breaks what was asked", "error", err)
		return PredictResult[T]{}, err
	}

	result := PredictResult[T]{
		Prediction:      prediction,
		ModelConfidence: parsed.ModelConfidence,
		Interval:        parsed.Interval,
		Scenarios:       parsed.Scenarios,
		Factors:         parsed.Factors,
		Reasoning:       parsed.Reasoning,
		Assumptions:     parsed.Assumptions,
		Risks:           parsed.Risks,
	}

	log.Debug("Predict operation succeeded",
		"confidence", result.ModelConfidence,
		"scenarioCount", len(result.Scenarios),
		"factorCount", len(result.Factors))
	return result, nil
}

// checkPredictionEnvelope refuses an answer that breaks what the options asked
// for. The messages carry positions and counts, never the model's text.
func checkPredictionEnvelope(opts PredictOptions, confidence float64, interval *PredictionInterval, scenarios []PredictionScenario, factors []PredictionFactor, reasoning string) error {
	refuse := func(format string, args ...any) error {
		return types.Newf(types.KindSchemaViolation, "predict", format, args...)
	}

	if !(confidence >= 0 && confidence <= 1) {
		return refuse("the answer's confidence is outside 0 to 1")
	}

	if interval != nil {
		if math.IsNaN(interval.Lower) || math.IsNaN(interval.Upper) || math.IsInf(interval.Lower, 0) || math.IsInf(interval.Upper, 0) {
			return refuse("the interval has a bound that is not a finite number")
		}
		if interval.Lower > interval.Upper {
			return refuse("the interval's lower bound is above its upper bound")
		}
		if opts.IncludeConfidenceInterval && math.Abs(interval.ConfidenceLevel-opts.ConfidenceLevel) > 1e-9 {
			return refuse("the interval is stated at a different confidence level than the one asked for")
		}
	}

	if opts.IncludeScenarios && len(scenarios) != opts.NumScenarios {
		return refuse("the answer has %d scenarios, and %d were asked for", len(scenarios), opts.NumScenarios)
	}
	for i, scenario := range scenarios {
		if !(scenario.Probability >= 0 && scenario.Probability <= 1) {
			return refuse("the probability of scenario %d is outside 0 to 1", i+1)
		}
	}

	for i, factor := range factors {
		switch strings.ToLower(strings.TrimSpace(factor.Impact)) {
		case "positive", "negative", "neutral":
		default:
			return refuse("the impact of factor %d is not positive, negative or neutral", i+1)
		}
	}

	if opts.IncludeReasoning && strings.TrimSpace(reasoning) == "" {
		return refuse("the answer has no reasoning, and it was asked for")
	}
	return nil
}

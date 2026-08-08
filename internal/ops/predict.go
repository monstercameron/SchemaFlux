// package ops - Predict operation for forecasting and extrapolation
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
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

	// ModelConfidence level for intervals (e.g., 0.95 for 95%)
	ConfidenceLevel float64

	// Prediction method hint ("trend", "pattern", "regression", "auto")
	Method string

	// Factors to consider in prediction
	Factors []string

	// Assumptions to use
	Assumptions []string

	// Include alternative scenarios
	IncludeScenarios bool

	// Number of scenarios to generate
	NumScenarios int

	// Include explanation of prediction reasoning
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
	if p.Horizon == "" {
		return fmt.Errorf("prediction horizon is required")
	}
	if p.ConfidenceLevel < 0 || p.ConfidenceLevel > 1 {
		return fmt.Errorf("confidence level must be between 0 and 1, got %f", p.ConfidenceLevel)
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
	return p.CommonOptions.toOpOptions()
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

// PredictResult contains the results of prediction
type PredictResult[T any] struct {
	Prediction T `json:"prediction"`
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64              `json:"confidence"`
	Interval        *PredictionInterval  `json:"interval,omitempty"`
	Scenarios       []PredictionScenario `json:"scenarios,omitempty"`
	Factors         []PredictionFactor   `json:"factors,omitempty"`
	Reasoning       string               `json:"reasoning,omitempty"`
	Assumptions     []string             `json:"assumptions,omitempty"`
	Risks           []string             `json:"risks,omitempty"`
	Metadata        map[string]any       `json:"metadata,omitempty"`
}

// Predict forecasts or extrapolates based on patterns in historical data.
// Uses LLM to identify trends, patterns, and make informed predictions.
//
// Type parameter T specifies the type of the prediction output.
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
	log := logger.GetLogger()
	log.Debug("Starting predict operation")

	var result PredictResult[T]
	result.Metadata = make(map[string]any)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Marshal historical data
	dataJSON, err := json.Marshal(historicalData)
	if err != nil {
		log.Error("Predict operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal historical data: %w", err)
	}

	// Build method description
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

	factorsDesc := ""
	if len(opts.Factors) > 0 {
		factorsDesc = fmt.Sprintf("\nConsider these factors: %s", strings.Join(opts.Factors, ", "))
	}

	assumptionsDesc := ""
	if len(opts.Assumptions) > 0 {
		assumptionsDesc = fmt.Sprintf("\nAssumptions: %s", strings.Join(opts.Assumptions, "; "))
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
		historyNote = fmt.Sprintf("\nFocus on the %s of historical data.", opts.HistoryWindow)
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
}`, opts.Horizon, methodDesc, factorsDesc, assumptionsDesc, intervalNote, scenariosNote, reasoningNote, historyNote)

	userPrompt := fmt.Sprintf("Based on this historical data, predict %s:\n\n%s", opts.Horizon, string(dataJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Predict operation LLM call failed", "error", err)
		return result, fmt.Errorf("prediction failed: %w", err)
	}

	// Parse the response
	if err := ParseJSONStrict(response, &result); err != nil {
		log.Error("Predict operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse prediction result: %w", err)
	}

	log.Debug("Predict operation succeeded",
		"confidence", result.ModelConfidence,
		"scenarioCount", len(result.Scenarios),
		"factorCount", len(result.Factors))
	return result, nil
}

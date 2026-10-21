package exp

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// PredictOptions configures Predict. Its zero value forecasts the next period
// with an 80 percent confidence interval, reasoning, Smart tier and Creative mode.
type PredictOptions struct {
	schemaflux.Options
	// Horizon describes the period being forecast; empty means "next".
	Horizon string
	// OmitConfidenceInterval stops asking for an interval; false asks for one.
	// An interval may still be absent for a prediction that is not a quantity.
	OmitConfidenceInterval bool
	// ConfidenceLevel is the requested interval level; zero means 0.8.
	// Values outside 0 to 1 are refused.
	ConfidenceLevel float64
	// Method is trend, pattern, regression or auto; empty means auto.
	Method string
	// Factors are factors to consider; nil names none.
	Factors []string
	// Assumptions are assumptions to use; nil names none.
	Assumptions []string
	// IncludeScenarios asks for NumScenarios alternatives; false asks for none.
	IncludeScenarios bool
	// NumScenarios is the requested number; zero means three. Negative values
	// are refused.
	NumScenarios int
	// OmitReasoning stops asking the model to explain its forecast; false asks
	// for an explanation.
	OmitReasoning bool
	// HistoryWindow describes which part of the history to emphasize; empty
	// leaves the full history available.
	HistoryWindow string
}

// PredictionInterval is the model's claimed confidence range.
type PredictionInterval struct {
	// Lower is the reported lower bound.
	Lower float64 `json:"lower"`
	// Upper is the reported upper bound.
	Upper float64 `json:"upper"`
	// ConfidenceLevel is the level the model says the interval represents.
	ConfidenceLevel float64 `json:"confidence_level"`
}

// PredictionScenario is one alternative forecast reported by the model.
type PredictionScenario struct {
	// Name labels the scenario.
	Name string `json:"name"`
	// Description describes the scenario.
	Description string `json:"description"`
	// Prediction is the alternative outcome.
	Prediction any `json:"prediction"`
	// Probability is the model's uncalibrated claim, from zero to one.
	Probability float64 `json:"probability"`
	// Conditions are the assumptions for this scenario.
	Conditions []string `json:"conditions"`
}

// PredictionFactor is one influence the model reported.
type PredictionFactor struct {
	// Name labels the factor.
	Name string `json:"name"`
	// Impact is positive, negative or neutral.
	Impact string `json:"impact"`
	// Weight is the model's claimed influence.
	Weight float64 `json:"weight"`
	// Reasoning explains the model's assessment, when supplied.
	Reasoning string `json:"reasoning,omitempty"`
}

// PredictResult contains the forecast and the model's reported evidence and
// uncertainty. Its confidence is a model claim, not a calibrated probability.
type PredictResult[T any] struct {
	// Prediction is the forecast value.
	Prediction T `json:"prediction"`
	// ModelConfidence is the model's confidence claim, from zero to one.
	ModelConfidence float64 `json:"confidence"`
	// Interval is the reported interval, if a numerical one was possible.
	Interval *PredictionInterval `json:"interval,omitempty"`
	// Scenarios are reported alternatives.
	Scenarios []PredictionScenario `json:"scenarios,omitempty"`
	// Factors are the reported influences.
	Factors []PredictionFactor `json:"factors,omitempty"`
	// Reasoning is the model's explanation.
	Reasoning string `json:"reasoning,omitempty"`
	// Assumptions are assumptions reported by the model.
	Assumptions []string `json:"assumptions,omitempty"`
	// Risks are risks reported by the model.
	Risks []string `json:"risks,omitempty"`
	// Metadata is reserved and is not populated.
	Metadata map[string]any `json:"metadata,omitempty"`
}

func mergePredictOptions(values []PredictOptions) PredictOptions {
	return mergeExpValues(values, func(out, later PredictOptions) PredictOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Horizon != "" {
			out.Horizon = later.Horizon
		}
		if later.OmitConfidenceInterval {
			out.OmitConfidenceInterval = true
		}
		if later.ConfidenceLevel != 0 {
			out.ConfidenceLevel = later.ConfidenceLevel
		}
		if later.Method != "" {
			out.Method = later.Method
		}
		if later.Factors != nil {
			out.Factors = later.Factors
		}
		if later.Assumptions != nil {
			out.Assumptions = later.Assumptions
		}
		if later.IncludeScenarios {
			out.IncludeScenarios = true
		}
		if later.NumScenarios != 0 {
			out.NumScenarios = later.NumScenarios
		}
		if later.OmitReasoning {
			out.OmitReasoning = true
		}
		if later.HistoryWindow != "" {
			out.HistoryWindow = later.HistoryWindow
		}
		return out
	})
}

func predictInternal(ctx context.Context, o PredictOptions, c bind.Common) ops.PredictOptions {
	in := ops.NewPredictOptions()
	if o.Horizon != "" {
		in.Horizon = o.Horizon
	}
	if o.OmitConfidenceInterval {
		in.IncludeConfidenceInterval = false
	}
	if o.ConfidenceLevel != 0 {
		in.ConfidenceLevel = o.ConfidenceLevel
	}
	if o.Method != "" {
		in.Method = o.Method
	}
	if o.Factors != nil {
		in.Factors = o.Factors
	}
	if o.Assumptions != nil {
		in.Assumptions = o.Assumptions
	}
	in.IncludeScenarios = o.IncludeScenarios
	if o.NumScenarios != 0 {
		in.NumScenarios = o.NumScenarios
	}
	if o.OmitReasoning {
		in.IncludeReasoning = false
	}
	if o.HistoryWindow != "" {
		in.HistoryWindow = o.HistoryWindow
	}
	applyFlatCommon(ctx, c, &in.CommonOptions, &in.OpOptions)
	return in
}

// Predict forecasts T from historical data. The operation records exp.predict
// at ContractSchemaConstrained when T has a schema, and at
// ContractJSONWellFormed otherwise.
func Predict[T any](ctx context.Context, history any, options ...PredictOptions) (PredictResult[T], error) {
	var zero PredictResult[T]
	call, err := bind.Begin(ctx, "exp.predict")
	if err != nil {
		return zero, err
	}
	o := mergePredictOptions(options)
	c, err := commonSettings(o.Options)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if err := c.Check("exp.predict"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Predict[T](history, predictInternal(call.Context(), o, c))
		if endErr := call.End(nil, err); endErr != nil {
			return zero, endErr
		}
		return predictResult(res), nil
	}
	res, err := bind.Recorded(&call, ops.ContractLevelFor[T]("exp.predict"), func(ctx context.Context) (ops.PredictResult[T], error) {
		return ops.Predict[T](history, predictInternal(ctx, o, c))
	})
	if err != nil {
		return zero, err
	}
	return predictResult(res), nil
}

func predictResult[T any](res ops.PredictResult[T]) PredictResult[T] {
	out := PredictResult[T]{Prediction: res.Prediction, ModelConfidence: res.ModelConfidence,
		Reasoning: res.Reasoning, Assumptions: res.Assumptions, Risks: res.Risks}
	if res.Interval != nil {
		out.Interval = &PredictionInterval{Lower: res.Interval.Lower, Upper: res.Interval.Upper, ConfidenceLevel: res.Interval.ConfidenceLevel}
	}
	if len(res.Scenarios) > 0 {
		out.Scenarios = make([]PredictionScenario, len(res.Scenarios))
		for i, v := range res.Scenarios {
			out.Scenarios[i] = PredictionScenario{Name: v.Name, Description: v.Description, Prediction: v.Prediction, Probability: v.Probability, Conditions: v.Conditions}
		}
	}
	if len(res.Factors) > 0 {
		out.Factors = make([]PredictionFactor, len(res.Factors))
		for i, v := range res.Factors {
			out.Factors[i] = PredictionFactor{Name: v.Name, Impact: v.Impact, Weight: v.Weight, Reasoning: v.Reasoning}
		}
	}
	return out
}

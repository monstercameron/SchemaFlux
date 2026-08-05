package ops

import (
	"testing"
)

func TestPredictOptions(t *testing.T) {
	t.Run("NewPredictOptions creates valid defaults", func(t *testing.T) {
		opts := NewPredictOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("default options should be valid: %v", err)
		}
	})

	t.Run("WithHorizon sets horizon", func(t *testing.T) {
		opts := NewPredictOptions().WithHorizon("next_quarter")
		if opts.Horizon != "next_quarter" {
			t.Errorf("expected next_quarter, got %s", opts.Horizon)
		}
	})

	t.Run("WithConfidenceLevel sets confidence level", func(t *testing.T) {
		opts := NewPredictOptions().WithConfidenceLevel(0.95)
		if opts.ConfidenceLevel != 0.95 {
			t.Errorf("expected 0.95, got %f", opts.ConfidenceLevel)
		}
	})

	t.Run("WithMethod sets method", func(t *testing.T) {
		opts := NewPredictOptions().WithMethod("trend")
		if opts.Method != "trend" {
			t.Errorf("expected trend, got %s", opts.Method)
		}
	})

	t.Run("WithFactors sets factors", func(t *testing.T) {
		opts := NewPredictOptions().WithFactors([]string{"seasonality", "market_trends"})
		if len(opts.Factors) != 2 {
			t.Errorf("expected 2 factors, got %d", len(opts.Factors))
		}
	})

	t.Run("WithAssumptions sets assumptions", func(t *testing.T) {
		opts := NewPredictOptions().WithAssumptions([]string{"no major disruptions"})
		if len(opts.Assumptions) != 1 {
			t.Errorf("expected 1 assumption, got %d", len(opts.Assumptions))
		}
	})

	t.Run("WithIncludeScenarios enables scenarios", func(t *testing.T) {
		opts := NewPredictOptions().WithIncludeScenarios(true)
		if !opts.IncludeScenarios {
			t.Error("expected IncludeScenarios to be true")
		}
	})

	t.Run("WithNumScenarios sets scenario count", func(t *testing.T) {
		opts := NewPredictOptions().WithNumScenarios(5)
		if opts.NumScenarios != 5 {
			t.Errorf("expected 5, got %d", opts.NumScenarios)
		}
	})

	t.Run("WithIncludeReasoning sets reasoning flag", func(t *testing.T) {
		opts := NewPredictOptions().WithIncludeReasoning(false)
		if opts.IncludeReasoning {
			t.Error("expected IncludeReasoning to be false")
		}
	})

	t.Run("Validate requires horizon", func(t *testing.T) {
		opts := PredictOptions{}
		if err := opts.Validate(); err == nil {
			t.Error("expected error for empty horizon")
		}
	})

	t.Run("Validate rejects invalid confidence level", func(t *testing.T) {
		opts := NewPredictOptions().WithConfidenceLevel(1.5)
		if err := opts.Validate(); err == nil {
			t.Error("expected error for confidence > 1")
		}
	})

	t.Run("Validate rejects invalid method", func(t *testing.T) {
		opts := NewPredictOptions()
		opts.Method = "invalid"
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid method")
		}
	})
}

func TestPredict(t *testing.T) {
	// Skip integration tests without LLM
	t.Skip("Integration test requires LLM provider")

	t.Run("Predict forecasts values", func(t *testing.T) {
		type Forecast struct {
			Sales  float64 `json:"sales"`
			Growth float64 `json:"growth"`
		}

		historicalData := []map[string]any{
			{"quarter": "Q1", "sales": 100000},
			{"quarter": "Q2", "sales": 120000},
			{"quarter": "Q3", "sales": 115000},
			{"quarter": "Q4", "sales": 140000},
		}

		opts := NewPredictOptions().
			WithHorizon("next_quarter").
			WithIncludeConfidenceInterval(true).
			WithIncludeReasoning(true)

		result, err := Predict[Forecast](historicalData, opts)
		if err != nil {
			t.Fatalf("Predict failed: %v", err)
		}

		if result.ModelConfidence <= 0 {
			t.Error("expected positive confidence")
		}
	})

	t.Run("Predict with scenarios", func(t *testing.T) {
		type Prediction struct {
			Value float64 `json:"value"`
		}

		data := []float64{10, 12, 15, 14, 18, 20}

		opts := NewPredictOptions().
			WithHorizon("next").
			WithIncludeScenarios(true).
			WithNumScenarios(3)

		result, err := Predict[Prediction](data, opts)
		if err != nil {
			t.Fatalf("Predict failed: %v", err)
		}

		if len(result.Scenarios) == 0 {
			t.Error("expected scenarios")
		}
	})
}

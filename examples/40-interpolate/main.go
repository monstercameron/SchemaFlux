package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
)

// loadEnv loads environment variables from .env files
func loadEnv() {
	if err := godotenv.Load(); err == nil {
		return
	}
	dir, _ := os.Getwd()
	for i := 0; i < 3; i++ {
		envPath := filepath.Join(dir, ".env")
		if err := godotenv.Load(envPath); err == nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ============================================================
// USE CASE 1: Sensor Data Gap Filling (IoT)
// ============================================================

// SensorReading from temperature/humidity sensor
type SensorReading struct {
	Timestamp   string  `json:"timestamp"`
	TempCelsius float64 `json:"temp_celsius"`
	Humidity    float64 `json:"humidity_pct"`
	Pressure    float64 `json:"pressure_hpa"`
}

// ============================================================
// USE CASE 2: Stock Price Missing Data
// ============================================================

// DailyStockPrice for a trading day
type DailyStockPrice struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume int     `json:"volume"`
}

// ============================================================
// USE CASE 3: Survey Response Completion
// ============================================================

// SurveyResponse with some missing answers
type SurveyResponse struct {
	QuestionNum int    `json:"question_num"`
	Category    string `json:"category"`
	Response    string `json:"response"`
	Score       int    `json:"score"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Interpolate Example ===")
	fmt.Println("Filling gaps in typed sequences using intelligent inference")

	// ============================================================
	// USE CASE 1: IoT Sensor Data Gap Filling
	// Scenario: Sensor went offline for 2 hours, need to fill gaps
	// ============================================================
	fmt.Println("\n--- Use Case 1: IoT Sensor Data Gap Filling ---")

	sensorData := []SensorReading{
		{Timestamp: "2024-01-15T10:00:00Z", TempCelsius: 22.5, Humidity: 45.0, Pressure: 1013.2},
		{Timestamp: "2024-01-15T11:00:00Z", TempCelsius: 23.1, Humidity: 44.0, Pressure: 1013.0},
		{Timestamp: "2024-01-15T12:00:00Z", TempCelsius: 0, Humidity: 0, Pressure: 0}, // OFFLINE
		{Timestamp: "2024-01-15T13:00:00Z", TempCelsius: 0, Humidity: 0, Pressure: 0}, // OFFLINE
		{Timestamp: "2024-01-15T14:00:00Z", TempCelsius: 25.8, Humidity: 40.0, Pressure: 1012.5},
		{Timestamp: "2024-01-15T15:00:00Z", TempCelsius: 26.2, Humidity: 38.5, Pressure: 1012.2},
	}

	fmt.Println("Original Sensor Data (0 = sensor offline):")
	for _, s := range sensorData {
		status := ""
		if s.TempCelsius == 0 {
			status = " [OFFLINE]"
		}
		fmt.Printf("  %s: %.1f°C, %.0f%% humidity%s\n",
			s.Timestamp[11:16], s.TempCelsius, s.Humidity, status)
	}

	sensorResult, err := schemaflux.Interpolate[SensorReading](sensorData, schemaflux.InterpolateOptions{
		Method:        "trend",
		Intelligence:  types.Smart,
		SequenceField: "timestamp",
		Steering:      "Zero values indicate sensor was offline. Temperature was rising through midday (typical pattern). Interpolate realistic readings that follow the warming trend.",
	})
	if err != nil {
		fmt.Printf("Sensor interpolation failed: %v\n", err)
	} else {
		fmt.Println("\nInterpolated Sensor Data:")
		for i, s := range sensorResult.Complete {
			status := ""
			for _, f := range sensorResult.Filled {
				if f.Index == i {
					status = fmt.Sprintf(" [FILLED: %s]", f.Method)
				}
			}
			fmt.Printf("  %s: %.1f°C, %.0f%% humidity, %.1f hPa%s\n",
				s.Timestamp[11:16], s.TempCelsius, s.Humidity, s.Pressure, status)
		}
		fmt.Printf("\nGaps Filled: %d, Avg ModelConfidence: %.0f%%\n",
			sensorResult.GapCount, sensorResult.AverageConfidence*100)
	}

	// ============================================================
	// USE CASE 2: Stock Price Missing Data
	// Scenario: Market data feed had gaps, need to fill for analysis
	// ============================================================
	fmt.Println("\n--- Use Case 2: Stock Price Missing Data ---")

	stockData := []DailyStockPrice{
		{Date: "2024-01-08", Open: 185.50, High: 187.20, Low: 184.80, Close: 186.40, Volume: 52000000},
		{Date: "2024-01-09", Open: 186.50, High: 188.00, Low: 185.20, Close: 187.80, Volume: 48000000},
		{Date: "2024-01-10", Open: 0, High: 0, Low: 0, Close: 0, Volume: 0}, // MISSING!
		{Date: "2024-01-11", Open: 189.20, High: 191.50, Low: 188.50, Close: 190.80, Volume: 55000000},
		{Date: "2024-01-12", Open: 190.50, High: 192.00, Low: 189.80, Close: 191.50, Volume: 47000000},
	}

	fmt.Println("Original Stock Data (AAPL):")
	for _, s := range stockData {
		if s.Close == 0 {
			fmt.Printf("  %s: [DATA FEED GAP]\n", s.Date)
		} else {
			fmt.Printf("  %s: Open $%.2f, Close $%.2f, Vol %dM\n",
				s.Date, s.Open, s.Close, s.Volume/1000000)
		}
	}

	stockResult, err := schemaflux.Interpolate[DailyStockPrice](stockData, schemaflux.InterpolateOptions{
		Method:        "pattern",
		Intelligence:  types.Smart,
		SequenceField: "date",
		Steering:      "Zero values indicate missing trading day data. The stock shows upward momentum. Previous close should inform next open. High/Low should bracket Open/Close. Volume should be consistent with trend.",
		Constraints: []string{
			"open should be near previous close",
			"high >= max(open, close)",
			"low <= min(open, close)",
		},
	})
	if err != nil {
		fmt.Printf("Stock interpolation failed: %v\n", err)
	} else {
		fmt.Println("\nInterpolated Stock Data:")
		for i, s := range stockResult.Complete {
			status := ""
			for _, f := range stockResult.Filled {
				if f.Index == i {
					status = " [INTERPOLATED]"
				}
			}
			fmt.Printf("  %s: O $%.2f, H $%.2f, L $%.2f, C $%.2f%s\n",
				s.Date, s.Open, s.High, s.Low, s.Close, status)
		}
		for _, f := range stockResult.Filled {
			if f.Reasoning != "" {
				fmt.Printf("\nReasoning: %s\n", f.Reasoning)
			}
		}
		fmt.Printf("ModelConfidence: %.0f%%\n", stockResult.AverageConfidence*100)
	}

	// ============================================================
	// USE CASE 3: Survey Response Completion
	// Scenario: Respondent skipped some questions, infer likely answers
	// ============================================================
	fmt.Println("\n--- Use Case 3: Survey Response Completion ---")

	surveyData := []SurveyResponse{
		{QuestionNum: 1, Category: "satisfaction", Response: "Very satisfied with the product quality", Score: 5},
		{QuestionNum: 2, Category: "satisfaction", Response: "Good customer service experience", Score: 4},
		{QuestionNum: 3, Category: "satisfaction", Response: "", Score: 0}, // SKIPPED
		{QuestionNum: 4, Category: "likelihood", Response: "Would definitely recommend to friends", Score: 5},
		{QuestionNum: 5, Category: "likelihood", Response: "", Score: 0}, // SKIPPED
		{QuestionNum: 6, Category: "feedback", Response: "The mobile app is intuitive", Score: 4},
	}

	fmt.Println("Original Survey Responses:")
	for _, s := range surveyData {
		if s.Response == "" {
			fmt.Printf("  Q%d (%s): [SKIPPED]\n", s.QuestionNum, s.Category)
		} else {
			fmt.Printf("  Q%d (%s): \"%s\" (Score: %d)\n",
				s.QuestionNum, s.Category, s.Response, s.Score)
		}
	}

	surveyResult, err := schemaflux.Interpolate[SurveyResponse](surveyData, schemaflux.InterpolateOptions{
		Method:        "semantic",
		Intelligence:  types.Smart,
		SequenceField: "question_num",
		Steering:      "Empty responses indicate skipped questions. Infer likely responses based on the respondent's overall positive sentiment (avg score ~4.5). Q3 is about shipping/delivery satisfaction. Q5 is about repeat purchase likelihood.",
		Constraints: []string{
			"score must be 1-5",
			"response should match the category theme",
			"maintain consistent positive sentiment",
		},
	})
	if err != nil {
		fmt.Printf("Survey interpolation failed: %v\n", err)
	} else {
		fmt.Println("\nInterpolated Survey Responses:")
		for i, s := range surveyResult.Complete {
			status := ""
			for _, f := range surveyResult.Filled {
				if f.Index == i {
					status = " [INFERRED]"
				}
			}
			fmt.Printf("  Q%d: \"%s\" (Score: %d)%s\n",
				s.QuestionNum, s.Response, s.Score, status)
		}
		fmt.Printf("\nFilled %d skipped questions\n", surveyResult.GapCount)
		for _, f := range surveyResult.Filled {
			fmt.Printf("  Q%d: %s (%.0f%% confidence)\n",
				surveyResult.Complete[f.Index].QuestionNum, f.Method, f.ModelConfidence*100)
		}
	}

	fmt.Println("\n=== Interpolate Example Complete ===")
}

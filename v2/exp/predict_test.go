package exp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	sfprovider "github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type predictOutput struct {
	Value float64 `json:"value"`
}

const predictAnswer = `{"prediction":{"value":42.5},"confidence":0.7,"interval":{"lower":40,"upper":45,"confidence_level":0.8},"scenarios":[],"factors":[{"name":"seasonality","impact":"positive","weight":0.4,"reasoning":"trend"}],"reasoning":"trend extrapolation","assumptions":[],"risks":[]}`

const predictTwoScenarios = `{"prediction":{"value":42.5},"confidence":0.7,"interval":null,"scenarios":[{"name":"up","description":"growth","prediction":{"value":50},"probability":0.3,"conditions":[]},{"name":"down","description":"decline","prediction":{"value":30},"probability":0.2,"conditions":[]}],"factors":[],"reasoning":"trend extrapolation","assumptions":[],"risks":[]}`

func TestPredictCases(t *testing.T) {
	cases := []struct {
		name    string
		opts    []exp.PredictOptions
		answer  string
		wantErr error
	}{
		{"default", nil, predictAnswer, nil},
		{"horizon", []exp.PredictOptions{{Horizon: "next_quarter"}}, predictAnswer, nil},
		{"method", []exp.PredictOptions{{Method: "trend"}}, predictAnswer, nil},
		{"factors", []exp.PredictOptions{{Factors: []string{"seasonality"}}}, predictAnswer, nil},
		{"assumptions", []exp.PredictOptions{{Assumptions: []string{"steady demand"}}}, predictAnswer, nil},
		{"history", []exp.PredictOptions{{HistoryWindow: "last year"}}, predictAnswer, nil},
		{"scenarios", []exp.PredictOptions{{IncludeScenarios: true, NumScenarios: 2}}, predictTwoScenarios, nil},
		{"omit interval", []exp.PredictOptions{{OmitConfidenceInterval: true}}, predictAnswer, nil},
		{"omit reasoning", []exp.PredictOptions{{OmitReasoning: true}}, predictAnswer, nil},
		{"merged", []exp.PredictOptions{{Horizon: "next_quarter"}, {Method: "regression", Factors: []string{"growth"}}}, predictAnswer, nil},
		{"invalid method", []exp.PredictOptions{{Method: "guess"}}, predictAnswer, schemaflux.ErrConfiguration},
		{"invalid confidence level", []exp.PredictOptions{{ConfidenceLevel: 2}}, predictAnswer, schemaflux.ErrConfiguration},
		{"missing prediction", nil, `{"confidence":0.7,"factors":[],"reasoning":"reason","assumptions":[],"risks":[]}`, schemaflux.ErrInvalidAnswer},
		{"missing reasoning", nil, `{"prediction":{"value":42.5},"confidence":0.7,"factors":[],"assumptions":[],"risks":[]}`, schemaflux.ErrInvalidAnswer},
		{"wrong scenario count", []exp.PredictOptions{{IncludeScenarios: true, NumScenarios: 2}}, predictAnswer, schemaflux.ErrInvalidAnswer},
		{"nil context", nil, predictAnswer, schemaflux.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(tc.answer)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
			if tc.name == "nil context" {
				ctx = nil
			}
			got, err := exp.Predict[predictOutput](ctx, []float64{35, 38, 41}, tc.opts...)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v; want %v", err, tc.wantErr)
				}
				if !reflect.DeepEqual(got, exp.PredictResult[predictOutput]{}) {
					t.Fatalf("error returned a value: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Prediction.Value != 42.5 || got.ModelConfidence != 0.7 || got.Reasoning != "trend extrapolation" {
				t.Fatalf("result = %+v", got)
			}
			if tc.name == "default" && (got.Interval == nil || got.Interval.ConfidenceLevel != 0.8 || len(got.Factors) != 1) {
				t.Fatalf("default result = %+v", got)
			}
			if tc.name == "scenarios" && len(got.Scenarios) != 2 {
				t.Fatalf("scenarios = %+v", got.Scenarios)
			}
			if tc.name == "merged" {
				requests := p.Requests()
				if len(requests) != 1 || !strings.Contains(requests[0].System, `Prediction horizon: "next_quarter"`) || !strings.Contains(requests[0].System, `regression-like`) || !strings.Contains(requests[0].System, `growth`) {
					t.Fatalf("merged options not in request: %+v", requests)
				}
			}
		})
	}
}

func TestPredictInvalidMethodDoesNotExposeTheCallerOption(t *testing.T) {
	const canary = "PRIVATE-METHOD-CANARY"
	provider := &schemafluxtest.Provider{}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: provider})
	_, err := exp.Predict[predictOutput](ctx, []int{1}, exp.PredictOptions{Method: canary})
	if !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Fatalf("error = %v; want a configuration error", err)
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(string(encoded), canary) {
		t.Errorf("a caller option appeared in the public error or its JSON")
	}
	if provider.Calls() != 0 {
		t.Errorf("invalid method sent %d provider calls", provider.Calls())
	}
}

func TestPredictContractFollowsTheOutputTypeAndFloor(t *testing.T) {
	structured := &schemafluxtest.Provider{}
	structured.Reply(predictAnswer)
	structured.SetName("predict-floor-allowed")
	sfprovider.DeclareRoute("predict-floor-allowed", "test-model", []string{"eu"}, 0)
	recorder := &schemaflux.Recorder{}
	client := schemaflux.Client{Provider: structured, Policy: &schemaflux.DataPolicy{Providers: []string{"predict-floor-allowed"}, Regions: []string{"eu"}, MinContract: schemaflux.ContractSchemaConstrained}}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	if _, err := exp.Predict[predictOutput](ctx, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if structured.Calls() != 1 {
		t.Errorf("schema-constrained output made %d provider calls, want one", structured.Calls())
	}
	if records := recorder.Records(); len(records) != 1 || records[0].Op != "exp.predict" || records[0].Requests != 1 || records[0].Delivered < schemaflux.ContractSchemaConstrained {
		t.Fatalf("records=%+v", records)
	}

	blocked := &schemafluxtest.Provider{}
	blocked.Reply(predictAnswer)
	blocked.SetName("predict-floor-blocked")
	sfprovider.DeclareRoute("predict-floor-blocked", "test-model", []string{"eu"}, 0)
	floorRecorder := &schemaflux.Recorder{}
	blockedClient := schemaflux.Client{Provider: blocked, Policy: &schemaflux.DataPolicy{Providers: []string{"predict-floor-blocked"}, Regions: []string{"eu"}, MinContract: schemaflux.ContractEvidenceChecked}}
	blockedCtx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), blockedClient), floorRecorder)
	_, err := exp.Predict[predictOutput](blockedCtx, []int{1, 2})
	if !errors.Is(err, schemaflux.ErrPolicy) || blocked.Calls() != 0 {
		t.Fatalf("error=%v provider calls=%d", err, blocked.Calls())
	}
	if records := floorRecorder.Records(); len(records) != 1 || records[0].Op != "exp.predict" || records[0].Requests != 0 {
		t.Fatalf("records=%+v", records)
	}
}

func TestPredictPayloadCanariesStayOutOfErrorAndRecord(t *testing.T) {
	const canary = "predict-private-payload-canary"
	provider := &schemafluxtest.Provider{}
	provider.Reply(canary)
	recorder := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: provider}), recorder)
	_, err := exp.Predict[predictOutput](ctx, []string{canary}, exp.PredictOptions{Options: schemaflux.Options{Steering: canary}})
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("error=%v", err)
	}
	encoded, marshalErr := json.Marshal(recorder.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), canary) {
		t.Error("record exposed the caller or provider canary")
	}
}

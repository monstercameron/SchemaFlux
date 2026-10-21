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
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type pivotInput struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

type pivotOutput struct {
	Name string `json:"name"`
}

const pivotAnswer = `{"pivoted":{"name":"Ada"},"mappings":[{"source_path":"name","target_path":"name","transformation":"copy","aggregation":"first"}],"stats":{"source_fields":2,"target_fields":1,"expansions":0,"compressions":1,"depth_change":0},"data_loss":["model claim"]}`

func TestPivotCases(t *testing.T) {
	cases := []struct {
		name    string
		options []exp.PivotOptions
		answer  string
		wantErr error
	}{
		{"default", nil, pivotAnswer, nil},
		{"pivot on", []exp.PivotOptions{{PivotOn: []string{"name"}}}, pivotAnswer, nil},
		{"group by", []exp.PivotOptions{{GroupBy: []string{"name"}}}, pivotAnswer, nil},
		{"sum", []exp.PivotOptions{{Aggregate: "sum"}}, pivotAnswer, nil},
		{"flatten", []exp.PivotOptions{{Flatten: true}}, pivotAnswer, nil},
		{"merged", []exp.PivotOptions{{PivotOn: []string{"name"}}, {Aggregate: "list", Flatten: true}}, pivotAnswer, nil},
		{"invalid aggregate", []exp.PivotOptions{{Aggregate: "median"}}, pivotAnswer, schemaflux.ErrConfiguration},
		{"blank pivot field", []exp.PivotOptions{{PivotOn: []string{" "}}}, pivotAnswer, schemaflux.ErrConfiguration},
		{"unknown group field", []exp.PivotOptions{{GroupBy: []string{"missing"}}}, pivotAnswer, schemaflux.ErrConfiguration},
		{"missing pivoted", nil, `{"mappings":[],"stats":{},"data_loss":[]}`, schemaflux.ErrInvalidAnswer},
		{"null pivoted", nil, `{"pivoted":null,"mappings":[],"stats":{},"data_loss":[]}`, schemaflux.ErrInvalidAnswer},
		{"bad value", nil, `{"pivoted":{"name":42},"mappings":[],"stats":{},"data_loss":[]}`, schemaflux.ErrInvalidAnswer},
		{"nil context", nil, pivotAnswer, schemaflux.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(tc.answer)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
			if tc.name == "nil context" {
				ctx = nil
			}
			got, err := exp.Pivot[pivotInput, pivotOutput](ctx, pivotInput{Name: "Ada", Score: 5}, tc.options...)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v; want %v", err, tc.wantErr)
				}
				if !reflect.DeepEqual(got, exp.PivotResult[pivotOutput]{}) {
					t.Fatal("error returned a nonzero result")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Pivoted.Name != "Ada" || !got.DataLossChecked || len(got.DataLoss) != 1 || got.DataLoss[0] != "score" {
				t.Fatalf("pivoted value or computed data loss = %+v", got)
			}
			if len(got.ModelClaimedDataLoss) != 1 || got.ModelClaimedDataLoss[0] != "model claim" || len(got.Mappings) != 1 || got.Stats.Compressions != 1 {
				t.Fatalf("reported data = %+v", got)
			}
			if tc.name == "merged" {
				requests := p.Requests()
				if len(requests) != 1 || !strings.Contains(requests[0].System, `Pivot on: ["name"]`) || !strings.Contains(requests[0].System, `Aggregate: list`) || !strings.Contains(requests[0].System, `Flatten: true`) {
					t.Fatalf("merged options not in request: %+v", requests)
				}
			}
		})
	}
}

func TestPivotInvalidAggregationDoesNotExposeTheCallerOption(t *testing.T) {
	const canary = "PRIVATE-AGGREGATION-CANARY"
	provider := &schemafluxtest.Provider{}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: provider})
	_, err := exp.Pivot[pivotInput, pivotOutput](ctx, pivotInput{Name: "Ada"}, exp.PivotOptions{Aggregate: canary})
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
		t.Errorf("invalid aggregation sent %d provider calls", provider.Calls())
	}
}

func TestPivotFloorRefusesBeforeCallingTheProvider(t *testing.T) {
	provider := &schemafluxtest.Provider{}
	provider.Reply(pivotAnswer)
	recorder := &schemaflux.Recorder{}
	client := schemaflux.Client{Provider: provider, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractSchemaConstrained}}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	_, err := exp.Pivot[pivotInput, pivotOutput](ctx, pivotInput{Name: "Ada"})
	if !errors.Is(err, schemaflux.ErrPolicy) || provider.Calls() != 0 {
		t.Fatalf("error=%v provider calls=%d", err, provider.Calls())
	}
	if records := recorder.Records(); len(records) != 1 || records[0].Op != "exp.pivot" || records[0].Requests != 0 {
		t.Fatalf("records=%+v", records)
	}
}

func TestPivotPayloadCanariesStayOutOfErrorAndRecord(t *testing.T) {
	const canary = "pivot-private-payload-canary"
	provider := &schemafluxtest.Provider{}
	provider.Reply(canary)
	recorder := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: provider}), recorder)
	_, err := exp.Pivot[pivotInput, pivotOutput](ctx, pivotInput{Name: canary}, exp.PivotOptions{Options: schemaflux.Options{Steering: canary}})
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

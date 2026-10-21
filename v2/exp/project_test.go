package exp_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type projectInput struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}
type projectOutput struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

const projectAnswer = `{"projected":{"name":"A","label":"x"},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`

func TestProjectCases(t *testing.T) {
	cases := []struct {
		name    string
		opts    []exp.ProjectOptions
		answer  string
		wantErr error
	}{
		{"zero", nil, projectAnswer, nil}, {"one", []exp.ProjectOptions{{Mappings: map[string]string{"name": "name"}}}, projectAnswer, nil},
		{"several", []exp.ProjectOptions{{Mappings: map[string]string{"name": "name"}}, {Exclude: []string{"secret"}}}, projectAnswer, nil},
		{"exclude", []exp.ProjectOptions{{Exclude: []string{"secret"}}}, projectAnswer, nil},
		{"infer allowed", []exp.ProjectOptions{{InferMissing: true}}, projectAnswer, nil},
		{"infer refused", nil, `{"projected":{"name":"A","label":"x"},"mappings":[{"target_field":"label","method":"infer"}],"lost":[],"inferred":["label"],"confidence":0.8}`, schemaflux.ErrInvalidAnswer},
		{"bad JSON", nil, `{}`, schemaflux.ErrInvalidAnswer},
		{"nil context", nil, projectAnswer, schemaflux.ErrInvalidRequest},
		{"excluded leak", []exp.ProjectOptions{{Exclude: []string{"secret"}}}, `{"projected":{"name":"A","label":"private-value"},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`, schemaflux.ErrInvalidAnswer},
		{"mapping target absent", []exp.ProjectOptions{{Mappings: map[string]string{"name": "missing"}}}, projectAnswer, schemaflux.ErrInvalidAnswer},
		{"zero confidence", nil, `{"projected":{"name":"A","label":"x"},"mappings":[],"lost":[],"inferred":[],"confidence":0}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(tc.answer)
			schemafluxtest.Install(t, p)
			ctx := context.Background()
			if tc.name == "nil context" {
				ctx = nil
			}
			_, err := exp.Project[projectInput, projectOutput](ctx, projectInput{Name: "A", Secret: "private-value"}, tc.opts...)
			if tc.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v want %v", err, tc.wantErr)
			}
		})
	}
}

func TestProjectContractFloorRefusesBeforeProviderCall(t *testing.T) {
	t.Parallel()
	model := &schemafluxtest.Provider{}
	model.Reply(projectAnswer)
	recorder := &schemaflux.Recorder{}
	client := schemaflux.Client{Provider: model, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractSchemaConstrained}}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
	got, err := exp.Project[projectInput, projectOutput](ctx, projectInput{Name: "A"})
	if !errors.Is(err, schemaflux.ErrPolicy) || !reflect.DeepEqual(got, exp.ProjectResult[projectOutput]{}) || model.Calls() != 0 {
		t.Fatalf("result=%+v error=%v provider calls=%d", got, err, model.Calls())
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "exp.project" || records[0].Requests != 0 || records[0].Requested != schemaflux.ContractJSONWellFormed {
		t.Fatalf("records=%+v", records)
	}
}

func TestProjectPayloadAndProviderCanariesStayOutOfDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		provider func(*schemafluxtest.Provider, string)
		wantKind schemaflux.Kind
	}{
		{"malformed answer", func(p *schemafluxtest.Provider, canary string) { p.Reply(canary) }, schemaflux.ErrInvalidAnswer},
		{"provider error", func(p *schemafluxtest.Provider, canary string) { p.Fail(errors.New(canary)) }, schemaflux.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const canary = "project-private-payload-canary"
			model := &schemafluxtest.Provider{}
			tc.provider(model, canary)
			var logs strings.Builder
			recorder := &schemaflux.Recorder{Bodies: true}
			client := schemaflux.Client{Provider: model, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
			ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), recorder)
			got, err := exp.Project[projectInput, projectOutput](ctx, projectInput{Name: canary, Secret: canary}, exp.ProjectOptions{Options: schemaflux.Options{Steering: canary}})
			if !errors.Is(err, tc.wantKind) || !reflect.DeepEqual(got, exp.ProjectResult[projectOutput]{}) || strings.Contains(err.Error(), canary) {
				t.Fatalf("result=%+v error=%v", got, err)
			}
			records := recorder.Records()
			if len(records) != 1 || records[0].Op != "exp.project" || records[0].Err == nil {
				t.Fatalf("records=%+v", records)
			}
			encoded, marshalErr := json.Marshal(records)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), canary) || strings.Contains(logs.String(), canary) {
				t.Fatalf("diagnostics exposed payload: record=%s log=%s", encoded, logs.String())
			}
		})
	}
}

package exp_test

import (
	"context"
	"errors"
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
			t.Parallel()
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

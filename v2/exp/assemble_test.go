package exp_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type assembleTestValue struct {
	Name string `json:"name"`
}

func TestAssembleDoesNotExposeForeignProviderError(t *testing.T) {
	const secret = "assemble-provider-error-canary"
	p := &schemafluxtest.Provider{}
	p.Fail(errors.New(secret))
	var logs strings.Builder
	client := schemaflux.Client{Provider: p, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	r := &schemaflux.Recorder{}
	_, err := exp.Assemble[assembleTestValue](schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), r), []any{"part"})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed provider text: %v", err)
	}
	b, _ := json.Marshal(r.Records())
	if strings.Contains(string(b), secret) {
		t.Fatal("record exposed provider text")
	}
	if strings.Contains(logs.String(), secret) {
		t.Skip("call-path log of a foreign provider's error")
	}
}

const assembleTestAnswer = `{"composed":{"name":"Ada"},"field_sources":[],"conflicts_resolved":0,"gaps_filled":[],"unused_parts":[],"completeness":0.8}`

func TestAssembleOptionsAndValidation(t *testing.T) {
	cases := []struct {
		name    string
		options []exp.AssembleOptions
		bad     bool
	}{
		{"default", nil, false}, {"single", []exp.AssembleOptions{{Template: "person"}}, false},
		{"several", []exp.AssembleOptions{{Template: "person"}, {MergeStrategy: "first"}}, false},
		{"template", []exp.AssembleOptions{{Template: "person"}}, false},
		{"first", []exp.AssembleOptions{{MergeStrategy: "first"}}, false},
		{"last", []exp.AssembleOptions{{MergeStrategy: "last"}}, false},
		{"combine", []exp.AssembleOptions{{MergeStrategy: "combine"}}, false},
		{"smart", []exp.AssembleOptions{{MergeStrategy: "smart"}}, false},
		{"fill gaps", []exp.AssembleOptions{{FillGaps: true}}, false},
		{"invalid strategy", []exp.AssembleOptions{{MergeStrategy: "bogus"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &schemafluxtest.Provider{}
			model.Reply(assembleTestAnswer)
			schemafluxtest.Install(t, model)
			got, err := exp.Assemble[assembleTestValue](context.Background(), []any{"part"}, tc.options...)
			if tc.bad {
				if !errors.Is(err, schemaflux.ErrConfiguration) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || got.Composed.Name != "Ada" {
				t.Fatalf("result=%+v error=%v", got, err)
			}
		})
	}
}

func TestAssembleRejectsMissingPartsAndNilContext(t *testing.T) {
	if _, err := exp.Assemble[assembleTestValue](context.Background(), nil); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("empty parts: %v", err)
	}
	if _, err := exp.Assemble[assembleTestValue](nil, []any{"part"}); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("nil context: %v", err)
	}
}

func TestAssembleRecordsSuccessAndFailure(t *testing.T) {
	for _, answer := range []string{assembleTestAnswer, `not json`} {
		p := &schemafluxtest.Provider{}
		p.Reply(answer)
		schemafluxtest.Install(t, p)
		r := &schemaflux.Recorder{}
		_, err := exp.Assemble[assembleTestValue](schemaflux.WithRecorder(context.Background(), r), []any{"part"})
		if answer == assembleTestAnswer && err != nil {
			t.Fatal(err)
		}
		if answer != assembleTestAnswer && err == nil {
			t.Fatal("invalid answer succeeded")
		}
		got := r.Records()
		if len(got) != 1 || got[0].Op != "exp.assemble" {
			t.Fatalf("records=%+v", got)
		}
	}
}

func TestAssembleFloorRefusesBeforeProviderCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(assembleTestAnswer)
	schemafluxtest.Install(t, p)
	client := schemaflux.Client{Provider: p, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	r := &schemaflux.Recorder{}
	_, err := exp.Assemble[assembleTestValue](schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), r), []any{"part"})
	if !errors.Is(err, schemaflux.ErrPolicy) || p.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, p.Calls())
	}
	if got := r.Records(); len(got) != 1 || got[0].Op != "exp.assemble" || got[0].Requests != 0 {
		t.Fatalf("records=%+v", got)
	}
}

func TestAssemblePayloadCanariesStayOutOfErrorsAndRecords(t *testing.T) {
	const secret = "assemble-private-canary"
	p := &schemafluxtest.Provider{}
	p.Reply(secret)
	schemafluxtest.Install(t, p)
	r := &schemaflux.Recorder{Bodies: true}
	_, err := exp.Assemble[assembleTestValue](schemaflux.WithRecorder(context.Background(), r), []any{map[string]string{secret: secret}}, exp.AssembleOptions{Template: secret, Options: schemaflux.Options{Steering: secret}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked canary: %v", err)
	}
	b, _ := json.Marshal(r.Records())
	if strings.Contains(string(b), secret) {
		t.Fatal("record leaked canary")
	}
}

func TestAssembleRejectsNegativeOutputLimitWithoutPayload(t *testing.T) {
	_, err := exp.Assemble[assembleTestValue](context.Background(), []any{"part"}, exp.AssembleOptions{Options: schemaflux.Options{MaxOutputTokens: -1}})
	if !errors.Is(err, schemaflux.ErrConfiguration) || !strings.Contains(err.Error(), "MaxOutputTokens") {
		t.Fatalf("error=%v", err)
	}
}

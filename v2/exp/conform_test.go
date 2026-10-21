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

type conformValue struct {
	Name string `json:"name"`
}

const conformAnswer = `{"conformed":{"name":"ALICE"},"adjustments":[],"violations":[],"compliance":1,"standard":"USPS"}`

func TestConformOptionsAndValidation(t *testing.T) {
	cases := []struct {
		name string
		opts []exp.ConformOptions
	}{
		{"default", nil}, {"single", []exp.ConformOptions{{Strict: true}}}, {"several", []exp.ConformOptions{{Strict: true}, {DropUnknown: true}}},
		{"strict", []exp.ConformOptions{{Strict: true}}}, {"drop unknown", []exp.ConformOptions{{DropUnknown: true}}}, {"custom rules", []exp.ConformOptions{{CustomRules: map[string]string{"n": "rule"}}}},
		{"steering", []exp.ConformOptions{{Options: schemaflux.Options{Steering: "clean"}}}}, {"model", []exp.ConformOptions{{Options: schemaflux.Options{Model: "test"}}}},
		{"max tokens", []exp.ConformOptions{{Options: schemaflux.Options{MaxOutputTokens: 20}}}}, {"web search", []exp.ConformOptions{{Options: schemaflux.Options{WebSearch: true}}}},
		{"tier", []exp.ConformOptions{{Options: schemaflux.Options{Tier: schemaflux.TierFast}}}}, {"effort", []exp.ConformOptions{{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}}}, {"mode", []exp.ConformOptions{{Options: schemaflux.Options{Mode: schemaflux.ModeTransform}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(conformAnswer)
			schemafluxtest.Install(t, p)
			got, err := exp.Conform[conformValue](context.Background(), conformValue{}, "USPS", tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if got.Conformed.Name != "ALICE" {
				t.Fatalf("result: %+v", got)
			}
			if p.Calls() != 1 {
				t.Fatalf("calls=%d", p.Calls())
			}
		})
	}
}

func TestConformRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		standard string
		opts     []exp.ConformOptions
		kind     error
	}{
		{"blank", context.Background(), " ", nil, schemaflux.ErrInvalidRequest}, {"nil context", nil, "USPS", nil, schemaflux.ErrInvalidRequest},
		{"negative tokens", context.Background(), "USPS", []exp.ConformOptions{{Options: schemaflux.Options{MaxOutputTokens: -1}}}, schemaflux.ErrConfiguration},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := exp.Conform[conformValue](tc.ctx, conformValue{}, tc.standard, tc.opts...)
			if !errors.Is(err, tc.kind) {
				t.Errorf("error=%v want %v", err, tc.kind)
			}
		})
	}
}

func TestConformRecordsSuccessAndFailureAndRefusesFloors(t *testing.T) {
	for _, answer := range []string{conformAnswer, `{}`} {
		t.Run(answer, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(answer)
			schemafluxtest.Install(t, p)
			r := &schemaflux.Recorder{}
			_, err := exp.Conform[conformValue](schemaflux.WithRecorder(context.Background(), r), conformValue{}, "USPS")
			records := r.Records()
			if len(records) != 1 || records[0].Op != "exp.conform" {
				t.Fatalf("records=%+v", records)
			}
			if answer == conformAnswer && err != nil {
				t.Fatal(err)
			}
			if answer == `{}` && !errors.Is(err, schemaflux.ErrInvalidAnswer) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	p := &schemafluxtest.Provider{}
	p.Reply(conformAnswer)
	schemafluxtest.Install(t, p)
	client := schemaflux.Client{Provider: p, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	r := &schemaflux.Recorder{}
	_, err := exp.Conform[conformValue](schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), r), conformValue{}, "USPS")
	if !errors.Is(err, schemaflux.ErrPolicy) || p.Calls() != 0 {
		t.Fatalf("err=%v calls=%d", err, p.Calls())
	}
	if got := r.Records(); len(got) != 1 || got[0].Requests != 0 {
		t.Fatalf("records=%+v", got)
	}
}

func TestConformDoesNotExposePayloadInAnswerOrErrors(t *testing.T) {
	const secret = "conform-private-canary"
	p := &schemafluxtest.Provider{}
	p.Reply(secret)
	schemafluxtest.Install(t, p)
	r := &schemaflux.Recorder{Bodies: true}
	_, err := exp.Conform[conformValue](schemaflux.WithRecorder(context.Background(), r), conformValue{Name: secret}, "USPS", exp.ConformOptions{CustomRules: map[string]string{"private": secret}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed canary: %v", err)
	}
	b, _ := json.Marshal(r.Records())
	if strings.Contains(string(b), secret) {
		t.Fatalf("record exposed canary")
	}
}

func TestConformDoesNotExposeForeignProviderError(t *testing.T) {
	const secret = "foreign-conform-error-canary"
	p := &schemafluxtest.Provider{}
	p.Fail(errors.New(secret))
	var logs strings.Builder
	client := schemaflux.Client{Provider: p, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	r := &schemaflux.Recorder{}
	_, err := exp.Conform[conformValue](schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), r), conformValue{}, "USPS")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed canary: %v", err)
	}
	b, _ := json.Marshal(r.Records())
	if strings.Contains(string(b), secret) {
		t.Fatal("record exposed canary")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("log exposed provider text: %s", logs.String())
	}
}

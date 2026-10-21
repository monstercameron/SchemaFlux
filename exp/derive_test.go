package exp_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type deriveTarget struct {
	Label string `json:"label"`
}

const deriveReply = `{"derived":{"label":"known"},"derivations":[],"field_confidence":{"label":0.9},"overall_confidence":0.9}`

func TestDeriveCases(t *testing.T) {
	cases := []struct {
		name string
		opts exp.DeriveOptions
	}{
		{"default", exp.DeriveOptions{}}, {"fields", exp.DeriveOptions{Fields: []string{"label"}}},
		{"rules", exp.DeriveOptions{Rules: map[string]string{"label": "copy"}}},
		{"reasoning", exp.DeriveOptions{OmitReasoning: true}}, {"confidence", exp.DeriveOptions{MinConfidence: .8}},
		{"tier", exp.DeriveOptions{Options: schemaflux.Options{Tier: schemaflux.TierFast}}},
		{"mode", exp.DeriveOptions{Options: schemaflux.Options{Mode: schemaflux.ModeTransform}}},
		{"tokens", exp.DeriveOptions{Options: schemaflux.Options{MaxOutputTokens: 512}}},
		{"search", exp.DeriveOptions{Options: schemaflux.Options{WebSearch: true}}},
		{"steering", exp.DeriveOptions{Options: schemaflux.Options{Steering: "concise"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(deriveReply)
			schemafluxtest.Install(t, p)
			got, err := exp.Derive[deriveTarget](context.Background(), map[string]string{"label": "known"}, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.Derived.Label != "known" {
				t.Fatalf("got %+v", got)
			}
		})
	}
	_, err := exp.Derive[deriveTarget](nil, map[string]string{"label": "x"})
	if !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("nil context: %v", err)
	}
	p := &schemafluxtest.Provider{}
	p.Reply(deriveReply)
	schemafluxtest.Install(t, p)
	_, err = exp.Derive[deriveTarget](context.Background(), map[string]string{"label": "x"}, exp.DeriveOptions{MinConfidence: -1})
	if err != nil {
		t.Fatalf("negative confidence disables the floor: %v", err)
	}
}

func TestDeriveRecordsSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  error
	}{{"success", deriveReply, nil}, {"failure", `{}`, schemaflux.ErrInvalidAnswer}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(tc.reply)
			r := &schemaflux.Recorder{}
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p, Recorder: r})
			_, err := exp.Derive[deriveTarget](ctx, map[string]string{"label": "known"})
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.want == nil && err != nil {
				t.Fatal(err)
			}
			records := r.Records()
			if len(records) != 1 || records[0].Op != "exp.derive" {
				t.Fatalf("records = %#v, want one exp.derive record", records)
			}
		})
	}
}

func TestDeriveMergesSeveralOptionValues(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(deriveReply)
	schemafluxtest.Install(t, p)
	got, err := exp.Derive[deriveTarget](context.Background(), map[string]string{"label": "known"},
		exp.DeriveOptions{Fields: []string{"label"}, Options: schemaflux.Options{Model: "first"}},
		exp.DeriveOptions{Rules: map[string]string{"label": "copy"}, Options: schemaflux.Options{Steering: "concise"}})
	if err != nil || got.Derived.Label != "known" {
		t.Fatalf("Derive() = %+v, %v", got, err)
	}
	request := p.Last()
	requestJSON, _ := json.Marshal(request)
	requestText := string(requestJSON)
	if request.Model != "first" || !strings.Contains(requestText, "copy") || !strings.Contains(requestText, "concise") {
		t.Fatalf("merged options were not applied to request: model=%q, rule present=%t, steering present=%t", request.Model, strings.Contains(requestText, "copy"), strings.Contains(requestText, "concise"))
	}
}

func TestDeriveRejectsInvalidOptionsBeforeSending(t *testing.T) {
	for name, opts := range map[string]exp.DeriveOptions{
		"nan confidence":         {MinConfidence: math.NaN()},
		"infinite confidence":    {MinConfidence: math.Inf(1)},
		"confidence above one":   {MinConfidence: 1.1},
		"negative token ceiling": {Options: schemaflux.Options{MaxOutputTokens: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			p := &schemafluxtest.Provider{}
			p.Reply(deriveReply)
			r := &schemaflux.Recorder{}
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p, Recorder: r})
			_, err := exp.Derive[deriveTarget](ctx, map[string]string{"label": "known"}, opts)
			if !errors.Is(err, schemaflux.ErrConfiguration) {
				t.Fatalf("error = %v, want configuration error", err)
			}
			if p.Calls() != 0 {
				t.Fatalf("provider calls = %d, want zero", p.Calls())
			}
			records := r.Records()
			if len(records) != 1 || records[0].Op != "exp.derive" || records[0].Requests != 0 {
				t.Fatalf("records = %#v, want one refused exp.derive record with zero requests", records)
			}
		})
	}
}

func TestDeriveFloorRefusesBeforeProviderCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.SetName("derive-floor")
	provider.DeclareRoute("derive-floor", "test-model", []string{"eu"}, 0)
	r := &schemaflux.Recorder{}
	client := schemaflux.Client{Provider: p, Recorder: r, Policy: &schemaflux.DataPolicy{Providers: []string{"derive-floor"}, Regions: []string{"eu"}, MinContract: schemaflux.ContractSchemaConstrained}}
	ctx := schemaflux.WithClient(context.Background(), client)
	_, err := exp.Derive[deriveTarget](ctx, map[string]string{"label": "known"})
	if !errors.Is(err, schemaflux.ErrPolicy) {
		t.Fatalf("error = %v, want policy refusal", err)
	}
	if p.Calls() != 0 {
		t.Fatalf("provider calls = %d, want zero", p.Calls())
	}
	records := r.Records()
	if len(records) != 1 || records[0].Op != "exp.derive" || records[0].Requests != 0 {
		t.Fatalf("records = %#v, want one refused exp.derive record with zero requests", records)
	}
}

func TestDeriveErrorsAndRecordsDoNotContainCallerData(t *testing.T) {
	const inputCanary = "input-secret-derive"
	const answerCanary = "answer-secret-derive"
	p := &schemafluxtest.Provider{}
	p.Reply(`{"derived":{"label":"` + answerCanary + `"},"derivations":[],"field_confidence":{"label":0.1},"overall_confidence":0.1}`)
	r := &schemaflux.Recorder{}
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p, Recorder: r})
	_, err := exp.Derive[deriveTarget](ctx, map[string]string{"label": inputCanary}, exp.DeriveOptions{Steering: "option-secret-derive", MinConfidence: .9})
	if err == nil {
		t.Fatal("low model confidence succeeded")
	}
	for _, secret := range []string{inputCanary, answerCanary, "option-secret-derive"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error contains payload %q: %v", secret, err)
		}
	}
	records := r.Records()
	if len(records) != 1 || records[0].Op != "exp.derive" {
		t.Fatalf("records = %#v, want one exp.derive record", records)
	}
	encoded, e := json.Marshal(records[0])
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{inputCanary, answerCanary, "option-secret-derive"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("record contains payload %q", secret)
		}
	}
}

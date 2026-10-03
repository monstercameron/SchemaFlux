package exp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type enrichInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}
type enrichOutput struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Segment string `json:"segment"`
}

const enrichReply = `{"enriched":{"name":"Ada","email":"ada@example.test","segment":"research"},"added_fields":["segment"],"confidence":{"segment":0.8},"derivations":{"segment":"role"}}`

func TestEnrichPublicAPI(t *testing.T) {
	cases := []struct {
		name string
		opts []exp.EnrichOptions
	}{
		{"default", nil}, {"fields", []exp.EnrichOptions{{Fields: []string{"segment"}}}}, {"rules", []exp.EnrichOptions{{Rules: map[string]string{"segment": "role"}}}},
		{"background", []exp.EnrichOptions{{Background: map[string]any{"team": "x"}}}}, {"domain", []exp.EnrichOptions{{Domain: "research"}}}, {"excluded", []exp.EnrichOptions{{ExcludeFields: []string{"email"}}}},
		{"allow changes", []exp.EnrichOptions{{AllowChanges: true}}}, {"confidence", []exp.EnrichOptions{{IncludeConfidence: true}}},
		{"common", []exp.EnrichOptions{{Options: schemaflux.Options{Steering: "carefully", MaxOutputTokens: 200, WebSearch: true}}}},
		{"merge", []exp.EnrichOptions{{Fields: []string{"a"}}, {Domain: "b"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &schemafluxtest.Provider{}
			model.Reply(enrichReply)
			schemafluxtest.Install(t, model)
			got, err := exp.Enrich[enrichOutput](context.Background(), enrichInput{Name: "Ada", Email: "ada@example.test"}, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if got.Enriched.Segment != "research" || len(got.AddedFields) != 1 || got.AddedFields[0] != "segment" {
				t.Fatalf("result = %+v", got)
			}
		})
	}
}

func TestEnrichInPlacePublicAPI(t *testing.T) {
	cases := []struct {
		name string
		opts []exp.EnrichOptions
	}{
		{"default", nil}, {"fields", []exp.EnrichOptions{{Fields: []string{"segment"}}}}, {"rules", []exp.EnrichOptions{{Rules: map[string]string{"segment": "role"}}}},
		{"background", []exp.EnrichOptions{{Background: map[string]any{"team": "x"}}}}, {"domain", []exp.EnrichOptions{{Domain: "research"}}}, {"excluded", []exp.EnrichOptions{{ExcludeFields: []string{"email"}}}},
		{"allow changes", []exp.EnrichOptions{{AllowChanges: true}}}, {"common", []exp.EnrichOptions{{Options: schemaflux.Options{Steering: "carefully", MaxOutputTokens: 200, WebSearch: true}}}},
		{"merge", []exp.EnrichOptions{{Fields: []string{"a"}}, {Domain: "b"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &schemafluxtest.Provider{}
			model.Reply(`{"name":"Ada","email":"ada@example.test"}`)
			schemafluxtest.Install(t, model)
			_, err := exp.EnrichInPlace(context.Background(), enrichInput{Name: "Ada", Email: "ada@example.test"}, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnrichInPlaceRefusesConfidenceAndRecords(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(`{}`)
	schemafluxtest.Install(t, model)
	rec := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(context.Background(), rec)
	_, err := exp.EnrichInPlace(ctx, enrichInput{}, exp.EnrichOptions{IncludeConfidence: true})
	if !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Fatalf("error=%v", err)
	}
	if model.Calls() != 0 {
		t.Fatalf("calls=%d", model.Calls())
	}
	r := rec.Records()
	if len(r) != 1 || r[0].Op != "exp.enrichinplace" {
		t.Fatalf("records=%+v", r)
	}
}

func TestEnrichRecordsSuccessFailureAndRejectsNilContext(t *testing.T) {
	for _, reply := range []string{enrichReply, `{}`} {
		model := &schemafluxtest.Provider{}
		model.Reply(reply)
		schemafluxtest.Install(t, model)
		rec := &schemaflux.Recorder{}
		_, err := exp.Enrich[enrichOutput](schemaflux.WithRecorder(context.Background(), rec), enrichInput{})
		r := rec.Records()
		if len(r) != 1 || r[0].Op != "exp.enrich" {
			t.Fatalf("records=%+v", r)
		}
		if (reply == enrichReply) != (err == nil) {
			t.Fatalf("reply %q error=%v", reply, err)
		}
	}
	if _, err := exp.Enrich[enrichOutput](nil, enrichInput{}); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("nil context error=%v", err)
	}
}

func TestEnrichDoesNotExposePayloadInValidationErrors(t *testing.T) {
	const secret = "enrich-private-canary"
	model := &schemafluxtest.Provider{}
	model.Reply(enrichReply)
	schemafluxtest.Install(t, model)
	_, err := exp.Enrich[enrichOutput](context.Background(), enrichInput{Name: secret}, exp.EnrichOptions{Background: map[string]any{"secret": secret}})
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed payload: %v", err)
	}
}

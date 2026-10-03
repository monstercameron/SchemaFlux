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

type interpolationPoint struct {
	Value int `json:"value"`
}

const interpolateAnswer = `{"complete":[{"value":1},{"value":2},{"value":3}],"filled":[{"index":1,"method":"linear","based_on":[0,2],"reasoning":"between","confidence":0.8}],"gap_count":1,"method":"linear","average_confidence":0.8}`

func TestInterpolateOptionsAndResult(t *testing.T) {
	cases := []struct {
		name string
		opts []exp.InterpolateOptions
	}{
		{"zero", nil}, {"one", []exp.InterpolateOptions{{Method: "linear"}}},
		{"several", []exp.InterpolateOptions{{Method: "trend"}, {SequenceField: "day"}}},
		{"method", []exp.InterpolateOptions{{Method: "semantic"}}},
		{"gaps", []exp.InterpolateOptions{{GapIndices: []int{1}}}},
		{"sequence", []exp.InterpolateOptions{{SequenceField: "day"}}},
		{"window", []exp.InterpolateOptions{{ContextWindow: 2}}},
		{"constraints", []exp.InterpolateOptions{{Constraints: []string{"positive"}}}},
		{"common tokens", []exp.InterpolateOptions{{Options: schemaflux.Options{MaxOutputTokens: 300}}}},
		{"web search", []exp.InterpolateOptions{{Options: schemaflux.Options{WebSearch: true}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &schemafluxtest.Provider{}
			p.Reply(interpolateAnswer)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
			got, err := exp.Interpolate[interpolationPoint](ctx, []interpolationPoint{{1}, {0}, {3}}, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Complete) != 3 || got.Complete[1].Value != 2 || got.GapCount != 1 || got.Filled[0].ModelConfidence != 0.8 {
				t.Fatalf("result = %+v", got)
			}
		})
	}
}

func TestInterpolateRefusals(t *testing.T) {
	cases := []struct {
		name  string
		ctx   context.Context
		items []interpolationPoint
		opts  exp.InterpolateOptions
		want  error
	}{
		{"nil context", nil, []interpolationPoint{{1}, {2}}, exp.InterpolateOptions{}, schemaflux.ErrInvalidRequest},
		{"empty sequence", context.Background(), nil, exp.InterpolateOptions{}, schemaflux.ErrInvalidRequest},
		{"invalid method", context.Background(), []interpolationPoint{{1}, {2}}, exp.InterpolateOptions{Method: "unknown"}, schemaflux.ErrInvalidRequest},
		{"negative window", context.Background(), []interpolationPoint{{1}, {2}}, exp.InterpolateOptions{ContextWindow: -1}, schemaflux.ErrConfiguration},
		{"negative output", context.Background(), []interpolationPoint{{1}, {2}}, exp.InterpolateOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, schemaflux.ErrConfiguration},
		{"invalid gap", context.Background(), []interpolationPoint{{1}, {2}}, exp.InterpolateOptions{GapIndices: []int{2}}, schemaflux.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exp.Interpolate[interpolationPoint](tc.ctx, tc.items, tc.opts)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestInterpolateRecordsCallsAndRefusesFloorsBeforeSending(t *testing.T) {
	for _, reply := range []string{interpolateAnswer, `{}`} {
		p := &schemafluxtest.Provider{}
		p.Reply(reply)
		schemafluxtest.Install(t, p)
		r := &schemaflux.Recorder{}
		ctx := schemaflux.WithRecorder(context.Background(), r)
		_, err := exp.Interpolate[interpolationPoint](ctx, []interpolationPoint{{1}, {0}, {3}})
		records := r.Records()
		if len(records) != 1 || records[0].Op != "exp.interpolate" {
			t.Fatalf("records=%+v", records)
		}
		if reply == interpolateAnswer && err != nil {
			t.Fatal(err)
		}
		if reply != interpolateAnswer && err == nil {
			t.Fatal("malformed answer succeeded")
		}
	}
	p := &schemafluxtest.Provider{}
	p.Reply(interpolateAnswer)
	c := schemaflux.Client{Provider: p, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	r := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), c), r)
	_, err := exp.Interpolate[interpolationPoint](ctx, []interpolationPoint{{1}, {0}, {3}})
	if !errors.Is(err, schemaflux.ErrPolicy) || p.Calls() != 0 {
		t.Fatalf("error=%v calls=%d", err, p.Calls())
	}
	if records := r.Records(); len(records) != 1 || records[0].Op != "exp.interpolate" || records[0].Requests != 0 {
		t.Fatalf("floor records=%+v", records)
	}
}

func TestInterpolatePayloadCanariesStayOutOfErrorsAndRecords(t *testing.T) {
	const secret = "interpolate-private-canary"
	p := &schemafluxtest.Provider{}
	p.Reply(secret)
	schemafluxtest.Install(t, p)
	r := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(context.Background(), r)
	_, err := exp.Interpolate[interpolationPoint](ctx, []interpolationPoint{{1}, {0}, {3}}, exp.InterpolateOptions{Options: schemaflux.Options{Steering: secret}, Constraints: []string{secret}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked payload: %v", err)
	}
	b, _ := json.Marshal(r.Records())
	if strings.Contains(string(b), secret) {
		t.Fatalf("record leaked payload: %s", b)
	}
}

func TestInterpolateForeignProviderErrorCanary(t *testing.T) {
	const secret = "interpolate-provider-private-canary"
	p := &schemafluxtest.Provider{}
	p.Fail(errors.New(secret))
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	recorder := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p, Logger: logger}), recorder)
	_, err := exp.Interpolate[interpolationPoint](ctx, []interpolationPoint{{1}, {0}, {3}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("provider message leaked through error: %v", err)
	}
	data, marshalErr := json.Marshal(recorder.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("provider message leaked through record: %s", data)
	}
	if strings.Contains(logs.String(), secret) {
		t.Skip("call-path log of a foreign provider's error")
	}
}

package exp_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type settleTarget struct {
	Answer string `json:"answer"`
}

const settleAnswer = `{"solution":{"answer":"ok"},"satisfaction":{"time":0.8},"overall_satisfaction":0.8,"tradeoffs":[],"alternatives":[],"reasoning":"balanced","confidence":0.9}`
const settleAdversarialAnswer = `{"deal":{"answer":"ok"},"deal_reached":true,"term_movements":[],"who_conceded_more":"neither","our_satisfaction":0.8,"their_satisfaction":0.7,"reasoning":"balanced","confidence":0.9}`

func TestSettleOptionsAndResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts []exp.SettleOptions
	}{
		{"defaults", nil}, {"one", []exp.SettleOptions{{Strategy: "maximize_primary"}}},
		{"merge", []exp.SettleOptions{{Strategy: "pareto"}, {Constraints: []string{"deadline"}}}},
		{"priorities", []exp.SettleOptions{{Priorities: map[string]float64{"time": 0.8}}}},
		{"constraints", []exp.SettleOptions{{Constraints: []string{"deadline"}}}},
		{"min", []exp.SettleOptions{{MinSatisfaction: 0.7}}}, {"no satisfaction floor", []exp.SettleOptions{{MinSatisfaction: -1}}},
		{"alternatives", []exp.SettleOptions{{MaxAlternatives: 2}}},
		{"strategy", []exp.SettleOptions{{Strategy: "balanced"}}},
		{"common model", []exp.SettleOptions{{Options: schemaflux.Options{Model: "example"}}}},
		{"common tier", []exp.SettleOptions{{Options: schemaflux.Options{Tier: schemaflux.TierFast}}}},
		{"common effort", []exp.SettleOptions{{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}}},
		{"common mode", []exp.SettleOptions{{Options: schemaflux.Options{Mode: schemaflux.ModeTransform}}}},
		{"common steering", []exp.SettleOptions{{Options: schemaflux.Options{Steering: "carefully"}}}},
		{"common output tokens", []exp.SettleOptions{{Options: schemaflux.Options{MaxOutputTokens: 400}}}},
		{"common web search", []exp.SettleOptions{{Options: schemaflux.Options{WebSearch: true}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &schemafluxtest.Provider{}
			p.Reply(settleAnswer)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
			got, err := exp.Settle[settleTarget](ctx, map[string]any{"time": "soon"}, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if got.Solution.Answer != "ok" || got.OverallSatisfaction != 0.8 {
				t.Fatalf("result = %+v", got)
			}
			if len(p.Requests()) != 1 {
				t.Fatalf("requests=%d", len(p.Requests()))
			}
		})
	}
}

func TestSettleRejectsBadOptionsAndNilContext(t *testing.T) {
	for _, o := range []exp.SettleOptions{{MinSatisfaction: 1.1}, {MinSatisfaction: math.NaN()}, {MinSatisfaction: math.Inf(1)}, {Options: schemaflux.Options{MaxOutputTokens: -1}}} {
		_, err := exp.Settle[settleTarget](context.Background(), "constraints", o)
		if !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Fatalf("err=%v", err)
		}
	}
	_, err := exp.Settle[settleTarget](nil, "constraints")
	if !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("nil context err=%v", err)
	}
}

func TestSettleNegativeMaxAlternativesIsConfigurationErrorBeforeRequest(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(settleAnswer)
	schemafluxtest.Install(t, p)
	_, err := exp.Settle[settleTarget](context.Background(), "constraints", exp.SettleOptions{MaxAlternatives: -1})
	if !errors.Is(err, schemaflux.ErrConfiguration) || p.Calls() != 0 {
		t.Fatalf("error=%v calls=%d, want configuration error before request", err, p.Calls())
	}
}

func TestSettleRecordsSuccessFailureAndFloorRefusal(t *testing.T) {
	for _, reply := range []string{settleAnswer, `{}`} {
		p := &schemafluxtest.Provider{}
		p.Reply(reply)
		schemafluxtest.Install(t, p)
		r := &schemaflux.Recorder{}
		ctx := schemaflux.WithRecorder(context.Background(), r)
		_, err := exp.Settle[settleTarget](ctx, "deadline")
		records := r.Records()
		if len(records) != 1 || records[0].Op != "exp.settle" {
			t.Fatalf("records = %+v", records)
		}
		if reply == settleAnswer && err != nil {
			t.Fatal(err)
		}
		if reply != settleAnswer && !errors.Is(err, schemaflux.ErrInvalidAnswer) {
			t.Fatalf("error=%v", err)
		}
	}
	p := &schemafluxtest.Provider{}
	p.Reply(settleAnswer)
	schemafluxtest.Install(t, p)
	r := &schemaflux.Recorder{}
	c := schemaflux.Client{Provider: p, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), c), r)
	_, err := exp.Settle[settleTarget](ctx, "deadline")
	if !errors.Is(err, schemaflux.ErrPolicy) || p.Calls() != 0 {
		t.Fatalf("error=%v provider calls=%d", err, p.Calls())
	}
	if got := r.Records(); len(got) != 1 || got[0].Op != "exp.settle" || got[0].Requests != 0 {
		t.Fatalf("records=%+v", got)
	}
}

func TestSettlePayloadCanariesStayOutOfErrorsAndRecords(t *testing.T) {
	const canary = "settle-private-canary"
	p := &schemafluxtest.Provider{}
	p.Reply(canary)
	schemafluxtest.Install(t, p)
	r := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(context.Background(), r)
	_, err := exp.Settle[settleTarget](ctx, map[string]any{"private": canary}, exp.SettleOptions{Options: schemaflux.Options{Steering: canary}, Strategy: canary})
	if err == nil {
		t.Fatal("malformed response succeeded")
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), canary) {
			t.Fatalf("error chain exposed payload: %v", cause)
		}
	}
	b, marshalErr := json.Marshal(r.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(b), canary) {
		t.Fatalf("record exposed payload: %s", b)
	}
}

func TestSettleForeignProviderErrorCanary(t *testing.T) {
	const canary = "settle-provider-private-canary"
	p := &schemafluxtest.Provider{}
	p.Fail(errors.New(canary))
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p, Logger: logger}), r)
	_, err := exp.Settle[settleTarget](ctx, "deadline")
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("provider error exposed payload: %v", err)
	}
	b, marshalErr := json.Marshal(r.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(b), canary) {
		t.Fatalf("record exposed provider text: %s", b)
	}
	if strings.Contains(logs.String(), canary) {
		t.Fatalf("log exposed provider text: %s", logs.String())
	}
}

func TestSettleAdversarialOptionsAndResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options []exp.SettleAdversarialOptions
	}{
		{"zero", nil}, {"one", []exp.SettleAdversarialOptions{{Strategy: "aggressive"}}}, {"several", []exp.SettleAdversarialOptions{{Strategy: "aggressive"}, {Strategy: "accommodating"}}},
		{"balanced", []exp.SettleAdversarialOptions{{Strategy: "balanced"}}}, {"model", []exp.SettleAdversarialOptions{{Options: schemaflux.Options{Model: "example"}}}}, {"tier", []exp.SettleAdversarialOptions{{Options: schemaflux.Options{Tier: schemaflux.TierFast}}}},
		{"mode", []exp.SettleAdversarialOptions{{Options: schemaflux.Options{Mode: schemaflux.ModeTransform}}}}, {"steering", []exp.SettleAdversarialOptions{{Options: schemaflux.Options{Steering: "carefully"}}}}, {"tokens", []exp.SettleAdversarialOptions{{Options: schemaflux.Options{MaxOutputTokens: 300}}}}, {"web", []exp.SettleAdversarialOptions{{Options: schemaflux.Options{WebSearch: true}}}},
	}
	input := exp.AdversarialContext[settleTarget]{Ours: exp.AdversarialPosition[settleTarget]{Position: settleTarget{"ours"}}, Theirs: exp.AdversarialPosition[settleTarget]{Position: settleTarget{"theirs"}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &schemafluxtest.Provider{}
			p.Reply(settleAdversarialAnswer)
			ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p})
			got, err := exp.SettleAdversarial(ctx, input, tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			if !got.DealReached || got.Deal.Answer != "ok" {
				t.Fatalf("result=%+v", got)
			}
			if len(p.Requests()) != 1 {
				t.Fatalf("requests=%d", len(p.Requests()))
			}
		})
	}
}

func TestSettleAdversarialRecordsCall(t *testing.T) {
	p := &schemafluxtest.Provider{}
	p.Reply(settleAdversarialAnswer)
	schemafluxtest.Install(t, p)
	r := &schemaflux.Recorder{}
	ctx := schemaflux.WithRecorder(context.Background(), r)
	input := exp.AdversarialContext[settleTarget]{Ours: exp.AdversarialPosition[settleTarget]{Position: settleTarget{"ours"}}, Theirs: exp.AdversarialPosition[settleTarget]{Position: settleTarget{"theirs"}}}
	_, err := exp.SettleAdversarial(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Records(); len(got) != 1 || got[0].Op != "exp.settleadversarial" {
		t.Fatalf("records=%+v", got)
	}
	bad := &schemafluxtest.Provider{}
	bad.Reply(`{}`)
	schemafluxtest.Install(t, bad)
	failedRecords := &schemaflux.Recorder{}
	_, err = exp.SettleAdversarial(schemaflux.WithRecorder(context.Background(), failedRecords), input)
	if !errors.Is(err, schemaflux.ErrInvalidAnswer) {
		t.Fatalf("failure error=%v", err)
	}
	if got := failedRecords.Records(); len(got) != 1 || got[0].Op != "exp.settleadversarial" || got[0].Err == nil {
		t.Fatalf("failure records=%+v", got)
	}
	blocked := &schemafluxtest.Provider{}
	blocked.Reply(settleAdversarialAnswer)
	schemafluxtest.Install(t, blocked)
	floorRecorder := &schemaflux.Recorder{}
	client := schemaflux.Client{Provider: blocked, Policy: &schemaflux.DataPolicy{MinContract: schemaflux.ContractEvidenceChecked}}
	_, err = exp.SettleAdversarial(schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), client), floorRecorder), input)
	if !errors.Is(err, schemaflux.ErrPolicy) || blocked.Calls() != 0 {
		t.Fatalf("floor error=%v calls=%d", err, blocked.Calls())
	}
	if got := floorRecorder.Records(); len(got) != 1 || got[0].Op != "exp.settleadversarial" || got[0].Requests != 0 {
		t.Fatalf("floor records=%+v", got)
	}
}

func TestSettleAdversarialRejectsNilContextAndBadCommonOptions(t *testing.T) {
	parties := exp.AdversarialContext[settleTarget]{Ours: exp.AdversarialPosition[settleTarget]{Position: settleTarget{"ours"}}, Theirs: exp.AdversarialPosition[settleTarget]{Position: settleTarget{"theirs"}}}
	if _, err := exp.SettleAdversarial[settleTarget](nil, parties); !errors.Is(err, schemaflux.ErrInvalidRequest) {
		t.Fatalf("nil context error=%v", err)
	}
	_, err := exp.SettleAdversarial(context.Background(), parties, exp.SettleAdversarialOptions{Options: schemaflux.Options{MaxOutputTokens: -1}})
	if !errors.Is(err, schemaflux.ErrConfiguration) {
		t.Fatalf("bad common option error=%v", err)
	}
}

func TestSettleAdversarialPayloadCanariesStayOutOfErrorsAndRecords(t *testing.T) {
	const canary = "adversarial-private-canary"
	p := &schemafluxtest.Provider{}
	p.Reply(canary)
	schemafluxtest.Install(t, p)
	r := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(context.Background(), r)
	parties := exp.AdversarialContext[settleTarget]{
		Ours:   exp.AdversarialPosition[settleTarget]{Position: settleTarget{Answer: canary}, MustHaves: []string{canary}, WalkAway: map[string]any{"private": canary}},
		Theirs: exp.AdversarialPosition[settleTarget]{Position: settleTarget{Answer: canary}},
	}
	_, err := exp.SettleAdversarial(ctx, parties, exp.SettleAdversarialOptions{Strategy: canary, Options: schemaflux.Options{Steering: canary}})
	if err == nil {
		t.Fatal("malformed response succeeded")
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), canary) {
			t.Fatalf("error chain exposed payload: %v", cause)
		}
	}
	b, marshalErr := json.Marshal(r.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(b), canary) {
		t.Fatalf("record exposed payload: %s", b)
	}
}

func TestSettleAdversarialForeignProviderErrorCanary(t *testing.T) {
	const canary = "adversarial-provider-private-canary"
	p := &schemafluxtest.Provider{}
	p.Fail(errors.New(canary))
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := &schemaflux.Recorder{Bodies: true}
	ctx := schemaflux.WithRecorder(schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: p, Logger: logger}), r)
	parties := exp.AdversarialContext[settleTarget]{Ours: exp.AdversarialPosition[settleTarget]{Position: settleTarget{Answer: canary}}, Theirs: exp.AdversarialPosition[settleTarget]{Position: settleTarget{Answer: "theirs"}}}
	_, err := exp.SettleAdversarial(ctx, parties)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("provider error exposed payload: %v", err)
	}
	b, marshalErr := json.Marshal(r.Records())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(b), canary) {
		t.Fatalf("record exposed provider text: %s", b)
	}
	if strings.Contains(logs.String(), canary) {
		t.Fatalf("log exposed provider text: %s", logs.String())
	}
}

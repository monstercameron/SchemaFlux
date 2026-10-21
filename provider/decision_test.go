package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
)

// systemOneAnswers is a System One reply to a request of a yes or no, a choice
// among three options and a scale of 1 to 5.
const systemOneAnswers = `{"answers":{` +
	`"q1":{"type":"noul","noul":0.96},` +
	`"q2":{"type":"choice","choice":"o2","confidence":0.8},` +
	`"q3":{"type":"score","score":2,"confidence":0.7}},` +
	`"model":"jev-1.13.0","usage":{"input_tokens":312,"output_tokens":48}}`

// threeQuestions is a decision that asks one question of each kind.
func threeQuestions() *schemaflux.Decision {
	return &schemaflux.Decision{
		State:    `{"ticket":"My checkout page is blank."}`,
		Guidance: "Treat a chargeback threat as urgent.",
		Questions: []schemaflux.DecisionQuestion{
			{Kind: schemaflux.DecisionYesNo, Ask: "Is the customer asking for a refund?"},
			{Kind: schemaflux.DecisionChoice, Ask: "Which team should handle this?", Options: []string{"billing", "technical", "shipping"}},
			{Kind: schemaflux.DecisionScale, Ask: "How urgent is this?", Low: 1, High: 5},
		},
	}
}

var decisionNames = []struct {
	name, path, model string
}{
	{"jev", "/v1/systemone", "jev-latest"},
	{"jev-openrouter", "/v1/systemone", "~typesafe/jev-latest"},
	{"jev-litellm", "/typesafe/v1/systemone", "jev-latest"},
}

func TestADecisionProviderAnswersATypedDecisionThroughTheRootsRequest(t *testing.T) {
	t.Parallel()
	for _, d := range decisionNames {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, systemOneAnswers))
			p := providerAt(t, d.name, srv.URL, Config{})

			resp, err := p.Complete(t.Context(), schemaflux.Request{
				Model: "gpt-5.6-luna", System: "SYSTEM-PROMPT-NOT-FOR-JEV", User: "USER-PROMPT-NOT-FOR-JEV", Decision: threeQuestions(),
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.Content != "" {
				t.Errorf("Content = %q, want none from a decision provider", resp.Content)
			}
			if len(resp.Answers) != 3 {
				t.Fatalf("got %d answers, want 3: %+v", len(resp.Answers), resp.Answers)
			}
			yes, choice, scale := resp.Answers[0], resp.Answers[1], resp.Answers[2]
			if yes.Kind != schemaflux.DecisionYesNo || !yes.Yes || !yes.ConfidenceReported || yes.ModelConfidence != 0.96 {
				t.Errorf("yes or no answer = %+v, want yes at 0.96", yes)
			}
			if choice.Kind != schemaflux.DecisionChoice || choice.Label != "technical" || choice.ModelConfidence != 0.8 || !choice.ConfidenceReported {
				t.Errorf("choice answer = %+v, want technical at 0.8", choice)
			}
			if scale.Kind != schemaflux.DecisionScale || scale.Level != 3 || scale.ModelConfidence != 0.7 || !scale.ConfidenceReported {
				t.Errorf("scale answer = %+v, want level 3 at 0.7", scale)
			}
			if resp.Model != "jev-1.13.0" || resp.Provider != d.name {
				t.Errorf("answered by %s/%s, want %s/jev-1.13.0", resp.Provider, resp.Model, d.name)
			}
			if resp.Usage.Input != 312 || resp.Usage.Output != 48 || resp.Usage.Total != 360 {
				t.Errorf("Usage = %+v", resp.Usage)
			}
			if resp.Escalation != nil {
				t.Errorf("Escalation = %+v, want none", resp.Escalation)
			}

			got := srv.last(t)
			if got.path != d.path || got.method != http.MethodPost {
				t.Errorf("request went to %s %s, want POST %s", got.method, got.path, d.path)
			}
			if got.header.Get("Authorization") != "Bearer "+testKey {
				t.Errorf("Authorization = %q", got.header.Get("Authorization"))
			}
			if strings.Contains(got.body, "NOT-FOR-JEV") {
				t.Errorf("the prompts were sent to a decision provider: %s", got.body)
			}
			var wire struct {
				Model     string                    `json:"model"`
				Questions map[string]map[string]any `json:"questions"`
				State     json.RawMessage           `json:"state"`
			}
			if err := json.Unmarshal([]byte(got.body), &wire); err != nil {
				t.Fatalf("the body is not JSON: %v", err)
			}
			if wire.Model != d.model {
				t.Errorf("model sent = %q, want %q", wire.Model, d.model)
			}
			if len(wire.Questions) != 3 || wire.Questions["q1"]["type"] != "noul" || wire.Questions["q2"]["type"] != "choice" || wire.Questions["q3"]["type"] != "score" {
				t.Errorf("questions sent = %v", wire.Questions)
			}
			if !strings.Contains(string(wire.State), "checkout page") {
				t.Errorf("state sent = %s", wire.State)
			}
		})
	}
}

func TestADecisionProviderAnswersNoWhenTheProbabilityOfYesIsLow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		noul     string
		wantYes  bool
		wantConf float64
	}{
		{"0.96", true, 0.96}, {"0.5", true, 0.5}, {"0.49", false, 0.51}, {"0.04", false, 0.96}, {"0", false, 1}, {"1", true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.noul, func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, `{"answers":{"q1":{"type":"noul","noul":`+tt.noul+`}},"model":"jev"}`))
			p := providerAt(t, "jev", srv.URL, Config{})
			resp, err := p.Complete(t.Context(), schemaflux.Request{Decision: &schemaflux.Decision{
				State: `"x"`, Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "Is it?"}},
			}})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			a := resp.Answers[0]
			if a.Yes != tt.wantYes || a.ModelConfidence < tt.wantConf-1e-9 || a.ModelConfidence > tt.wantConf+1e-9 || !a.ConfidenceReported {
				t.Errorf("answer = %+v, want yes=%v at %v", a, tt.wantYes, tt.wantConf)
			}
		})
	}
}

func TestADecisionProviderDeclaresItsLimitsAsADecider(t *testing.T) {
	t.Parallel()
	for _, d := range decisionNames {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			p := mustNew(t, d.name, buildable(d.name, Config{}))
			for _, candidate := range []schemaflux.Provider{p, transparent{p}, transparent{transparent{p}}} {
				decider, ok := bind.Optional[Decider](candidate)
				if !ok {
					t.Fatalf("%T is not a Decider", candidate)
				}
				want := schemaflux.DecisionLimits{MaxOptions: 255, MaxLevels: 10, MaxStateTokens: 32000}
				if got := decider.DecisionLimits(); got != want {
					t.Errorf("DecisionLimits() = %+v, want %+v", got, want)
				}
			}
		})
	}
	t.Run("a provider that generates text is not one", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"openai", "anthropic", "openrouter"} {
			if _, ok := bind.Optional[Decider](mustNew(t, name, buildable(name, Config{}))); ok {
				t.Errorf("%s is a Decider", name)
			}
		}
	})
}

func TestADecisionProviderRefusesWhatItCannotAnswerBeforeSendingAnything(t *testing.T) {
	t.Parallel()
	options := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "option " + string(rune('A'+i/26)) + string(rune('a'+i%26))
		}
		return out
	}
	one := func(q schemaflux.DecisionQuestion) *schemaflux.Decision {
		return &schemaflux.Decision{State: `"x"`, Questions: []schemaflux.DecisionQuestion{q}}
	}
	tests := []struct {
		name string
		req  schemaflux.Request
		kind schemaflux.Kind
	}{
		{"a request that asks for text", schemaflux.Request{Model: "m", System: "s", User: "u"}, schemaflux.ErrUnsupported},
		{"a request with a decision of no questions", schemaflux.Request{Decision: &schemaflux.Decision{State: `"x"`}}, schemaflux.ErrUnsupported},
		{"a choice of more options than the limit", schemaflux.Request{Decision: one(schemaflux.DecisionQuestion{Kind: schemaflux.DecisionChoice, Ask: "q", Options: options(256)})}, schemaflux.ErrUnsupported},
		{"a scale of more levels than the limit", schemaflux.Request{Decision: one(schemaflux.DecisionQuestion{Kind: schemaflux.DecisionScale, Ask: "q", Low: 1, High: 11})}, schemaflux.ErrUnsupported},
		{"a state longer than the limit", schemaflux.Request{Decision: &schemaflux.Decision{State: `"` + strings.Repeat("x", 200000) + `"`,
			Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "q"}}}}, schemaflux.ErrUnsupported},
		{"a question with no ask", schemaflux.Request{Decision: one(schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo})}, schemaflux.ErrInvalidRequest},
		{"a question of no kind", schemaflux.Request{Decision: one(schemaflux.DecisionQuestion{Ask: "q"})}, schemaflux.ErrInvalidRequest},
		{"a choice of one option", schemaflux.Request{Decision: one(schemaflux.DecisionQuestion{Kind: schemaflux.DecisionChoice, Ask: "q", Options: []string{"a"}})}, schemaflux.ErrInvalidRequest},
		{"a request that searches the web", schemaflux.Request{WebSearch: true, Decision: one(schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo, Ask: "q"})}, schemaflux.ErrUnsupported},
		{"a request that asks for reasoning", schemaflux.Request{Effort: schemaflux.EffortHigh, Decision: one(schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo, Ask: "q"})}, schemaflux.ErrUnsupported},
	}
	for _, d := range decisionNames {
		for _, tt := range tests {
			t.Run(d.name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				srv := serve(t, replying(200, systemOneAnswers))
				p := providerAt(t, d.name, srv.URL, Config{})
				resp, err := p.Complete(t.Context(), tt.req)
				if err == nil {
					t.Fatalf("Complete succeeded with %+v", resp)
				}
				if e := errorOf(t, err); e.Kind != tt.kind {
					t.Errorf("Kind = %v (%v), want %v", e.Kind, err, tt.kind)
				}
				if srv.count() != 0 {
					t.Errorf("%d requests were sent, want none", srv.count())
				}
				if tt.kind == schemaflux.ErrUnsupported && !errors.Is(err, errors.ErrUnsupported) {
					t.Errorf("errors.Is(err, errors.ErrUnsupported) is false for %v", err)
				}
			})
		}
	}
}

func TestADecisionProviderRefusesAnAnswerThatIsNotOneToItsQuestion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reply string
	}{
		{"no answers at all", `{"model":"jev"}`},
		{"an answer missing", `{"answers":{"q1":{"type":"noul","noul":0.9}},"model":"jev"}`},
		{"a probability past one", `{"answers":{"q1":{"type":"noul","noul":1.5},"q2":{"type":"choice","choice":"o1"},"q3":{"type":"score","score":1}}}`},
		{"a yes or no answered as a choice", `{"answers":{"q1":{"type":"choice","choice":"o1"},"q2":{"type":"choice","choice":"o1"},"q3":{"type":"score","score":1}}}`},
		{"a choice that was not offered", `{"answers":{"q1":{"type":"noul","noul":0.9},"q2":{"type":"choice","choice":"o9"},"q3":{"type":"score","score":1}}}`},
		{"a score past the end of the scale", `{"answers":{"q1":{"type":"noul","noul":0.9},"q2":{"type":"choice","choice":"o1"},"q3":{"type":"score","score":9}}}`},
		{"a confidence that is not a probability", `{"answers":{"q1":{"type":"noul","noul":0.9},"q2":{"type":"choice","choice":"o1","confidence":4},"q3":{"type":"score","score":1}}}`},
		{"a body that is not JSON", `<html>` + testMarker + `</html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := serve(t, replying(200, tt.reply))
			p := providerAt(t, "jev", srv.URL, Config{})
			resp, err := p.Complete(t.Context(), schemaflux.Request{Decision: threeQuestions()})
			if err == nil {
				t.Fatalf("Complete succeeded with %+v", resp)
			}
			if e := errorOf(t, err); e.Kind != schemaflux.ErrInvalidAnswer {
				t.Errorf("Kind = %v (%v), want invalid answer", e.Kind, err)
			}
			if resp.Answers != nil {
				t.Errorf("the failed call returned answers: %+v", resp.Answers)
			}
			for how, text := range theTextOf(t, err) {
				if strings.Contains(text, testMarker) {
					t.Errorf("%s prints the body: %s", how, text)
				}
			}
		})
	}
}

func TestADecisionProviderIsFoundAndCalledThroughTwoLayers(t *testing.T) {
	t.Parallel()
	srv := serve(t, replying(200, `{"answers":{"q1":{"type":"noul","noul":0.9}},"model":"jev"}`))
	p := providerAt(t, "jev", srv.URL, Config{})
	wrapped := transparent{transparent{p}}
	if _, ok := bind.Optional[Decider](wrapped); !ok {
		t.Fatal("not found through two layers")
	}
	resp, err := wrapped.Complete(t.Context(), schemaflux.Request{Decision: &schemaflux.Decision{
		State: `"x"`, Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "Is it?"}},
	}})
	if err != nil || len(resp.Answers) != 1 || !resp.Answers[0].Yes {
		t.Errorf("got %+v, %v", resp, err)
	}
}

func TestAGeneratingProviderIgnoresTheDecisionAndAnswersThePrompts(t *testing.T) {
	t.Parallel()
	srv := serve(t, replying(200, responsesOK))
	p := providerAt(t, "openai", srv.URL, Config{})
	resp, err := p.Complete(t.Context(), schemaflux.Request{Model: "m", System: "SYSTEM-TEXT", User: "USER-TEXT", Decision: threeQuestions()})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "hello" || resp.Answers != nil {
		t.Errorf("got %+v, want text and no answers", resp)
	}
	body := srv.last(t).body
	if !strings.Contains(body, "SYSTEM-TEXT") || !strings.Contains(body, "USER-TEXT") || strings.Contains(body, "questions") {
		t.Errorf("the request carried the decision or lost the prompts: %s", body)
	}
}

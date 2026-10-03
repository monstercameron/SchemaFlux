package wirecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
)

// The decision protocol's own request body (LX-100): what the bounded
// operations send a System One endpoint, checked against what that endpoint
// takes, without a network call. The provider is the real one, built by name,
// with a recording transport in place of the network.

// decisionRecorder keeps the raw body of every request and answers each with a
// System One answer to a yes or no, which most of the cases go on to refuse as
// an answer to another question. What was sent is the subject.
type decisionRecorder struct {
	mu     sync.Mutex
	bodies []string
	urls   []string
}

func (r *decisionRecorder) take() (bodies, urls []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bodies, urls = r.bodies, r.urls
	r.bodies, r.urls = nil, nil
	return bodies, urls
}

func (r *decisionRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.bodies = append(r.bodies, string(raw))
	r.urls = append(r.urls, req.Method+" "+req.URL.String())
	r.mu.Unlock()
	const answer = `{"answers":{"q1":{"type":"noul","noul":0.9}},"model":"jev-1.13.0","usage":{"input_tokens":1,"output_tokens":1}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(answer)),
		Request:    req,
	}, nil
}

// decisionCases are the operations that fill a decision, each through its
// exported function, on a context whose client's provider is the decision
// provider alone.
func decisionCases(ctx context.Context) []struct {
	name      string
	questions int
	run       func() error
} {
	type triage struct {
		Refund  bool   `ask:"Is the customer asking for a refund?"`
		Team    string `ask:"Which team should handle this?" oneof:"billing,technical,shipping"`
		Urgency int    `ask:"How urgent is this?" scale:"1-5"`
		Angry   bool   `ask:"Is the customer angry?"`
	}
	type ticket struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	}
	message := "Please refund my order <7731> & reply \"soon\"."
	step := func(context.Context) (string, error) { return "", nil }

	return []struct {
		name      string
		questions int
		run       func() error
	}{
		{"Is", 1, func() error {
			_, err := schemaflux.Is(ctx, message, "Is the customer asking for a refund?")
			return err
		}},
		{"IsResult", 1, func() error {
			_, err := schemaflux.IsResult(ctx, message, "Is the customer asking for a refund?")
			return err
		}},
		{"Is, steered", 1, func() error {
			_, err := schemaflux.Is(ctx, message, "Is the customer asking for a refund?", schemaflux.OpOptions{Steering: "An exchange is not a refund."})
			return err
		}},
		{"Which", 1, func() error {
			_, err := schemaflux.Which(ctx, message, "Which team should handle this?", []string{"billing", "technical", "shipping"})
			return err
		}},
		{"WhichOrNone", 1, func() error {
			_, _, err := schemaflux.WhichOrNone(ctx, message, "Which open ticket is this about?", []string{"Late parcel", "Double charge"})
			return err
		}},
		{"Rate", 1, func() error {
			_, err := schemaflux.Rate(ctx, message, "How urgent is this?", 1, 5)
			return err
		}},
		{"Rate, ten levels", 1, func() error {
			_, err := schemaflux.Rate(ctx, message, "How urgent is this?", 1, 10)
			return err
		}},
		{"Assess", 4, func() error {
			_, err := schemaflux.Assess[triage](ctx, ticket{ID: "T-1", Subject: message})
			return err
		}},
		{"AssessResult", 4, func() error {
			_, err := schemaflux.AssessResult[triage](ctx, message)
			return err
		}},
		{"Whether", 1, func() error {
			_, err := schemaflux.Whether(ctx, message, "Is this message spam?", step, step)
			return err
		}},
		{"RouteBy", 1, func() error {
			_, err := schemaflux.RouteBy(ctx, message, "Which team should handle this?",
				[]schemaflux.Route[string]{schemaflux.To("billing", step), schemaflux.To("technical", step)})
			return err
		}},
		{"Classify", 1, func() error {
			opts := schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"})
			opts.CommonOptions.Context = ctx
			opts.CategoryDescriptions = map[string]string{"billing": "Payments and refunds"}
			_, err := schemaflux.Classify[string, string](message, opts)
			return err
		}},
		{"Choose", 1, func() error {
			_, err := schemaflux.Choose([]ticket{{ID: "T-1", Subject: "charged twice"}, {ID: "T-2", Subject: "cannot log in"}},
				schemaflux.NewChooseOptions().WithContext(ctx).WithSteering("the most urgent"))
			return err
		}},
	}
}

func TestEveryDecisionSendsABodyTheDecisionEndpointTakes(t *testing.T) {
	presets := []struct {
		provider string
		baseURL  string
		url      string
		model    string
	}{
		{"jev", "", "POST https://api.typesafe.ai/v1/systemone", "jev-latest"},
		{"jev-openrouter", "", "POST https://openrouter.ai/api/v1/systemone", "~typesafe/jev-latest"},
		{"jev-litellm", "http://litellm.internal:4000", "POST http://litellm.internal:4000/typesafe/v1/systemone", "jev-latest"},
	}
	for _, preset := range presets {
		t.Run(preset.provider, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
			rec := &decisionRecorder{}
			decider, err := schemaflux.CreateProvider(preset.provider, schemaflux.ProviderConfig{
				APIKey: "test-key", BaseURL: preset.baseURL, HTTPClient: &http.Client{Transport: rec},
			})
			if err != nil {
				t.Fatalf("CreateProvider(%q): %v", preset.provider, err)
			}
			// The model is the generating provider's default, as it is when the
			// decision provider is one half of a pair: its own is what is sent.
			client := schemaflux.NewClient("test-key").WithProviderInstance(decider).WithModel("gpt-6-luna").WithRetries(0)
			ctx := client.Context(context.Background())

			for _, tc := range decisionCases(ctx) {
				rec.take()
				runErr := tc.run()
				bodies, urls := rec.take()
				if len(bodies) != 1 {
					t.Errorf("%s: %d requests reached the endpoint, want one (the call returned: %v)", tc.name, len(bodies), runErr)
					continue
				}
				if urls[0] != preset.url {
					t.Errorf("%s: sent to %q, want %q", tc.name, urls[0], preset.url)
				}
				for _, problem := range decisionBodyProblems(bodies[0], preset.model, tc.questions) {
					t.Errorf("%s: %s", tc.name, problem)
				}
			}
		})
	}
}

// decisionBodyProblems applies what the System One endpoint takes to one
// request body, written out here and not read from the provider's own limits:
// a rule the provider does not know would pass both it and a test built on it.
//
// From TypeSafe's reference and the owner's prototype client: the body is a
// model, a state and a map of questions and nothing else; a question is a noul
// with no criteria, a choice with a map of 2 to 255 described options, or a
// score with a list of 2 to 10 levels; and no prompt goes with it.
func decisionBodyProblems(raw, wantModel string, wantQuestions int) []string {
	var problems []string
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return []string{"the body is not a JSON object"}
	}

	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "model,questions,state" {
		problems = append(problems, fmt.Sprintf("the body's keys are %v, want model, questions and state and nothing else", keys))
	}
	// The order a map with these keys encodes in, which is what was measured.
	if !strings.HasPrefix(raw, `{"model":`) || strings.Index(raw, `"questions":`) > strings.Index(raw, `"state":`) {
		problems = append(problems, "the body's keys are not in the order model, questions, state")
	}

	var model string
	if json.Unmarshal(body["model"], &model) != nil || model != wantModel {
		problems = append(problems, fmt.Sprintf("model = %s, want %q", body["model"], wantModel))
	}
	if state := strings.TrimSpace(string(body["state"])); state == "" || state == "null" {
		problems = append(problems, "the state is missing")
	}
	for _, prompt := range []string{"You answer questions about a subject", "You are a classification expert", "You are a selection expert", "Questions:\\n"} {
		if strings.Contains(raw, prompt) {
			problems = append(problems, "the body carries a prompt written for a generating model")
		}
	}

	var questions map[string]struct {
		Type         string          `json:"type"`
		Instructions string          `json:"instructions"`
		Criteria     json.RawMessage `json:"criteria"`
	}
	if err := json.Unmarshal(body["questions"], &questions); err != nil {
		return append(problems, "questions is not a map of questions")
	}
	if len(questions) != wantQuestions {
		problems = append(problems, fmt.Sprintf("%d questions in the request, want %d in one request", len(questions), wantQuestions))
	}
	for i := 1; i <= len(questions); i++ {
		key := fmt.Sprintf("q%d", i)
		question, asked := questions[key]
		if !asked {
			problems = append(problems, fmt.Sprintf("the questions are not keyed q1 to q%d: %s is missing", len(questions), key))
			continue
		}
		if strings.TrimSpace(question.Instructions) == "" {
			problems = append(problems, key+" has no instructions")
		}
		switch question.Type {
		case "noul":
			if len(question.Criteria) != 0 {
				problems = append(problems, key+" is a noul and carries criteria")
			}
		case "choice":
			var options map[string]string
			if json.Unmarshal(question.Criteria, &options) != nil {
				problems = append(problems, key+" is a choice whose criteria are not a map of option to description")
				continue
			}
			if len(options) < 2 || len(options) > 255 {
				problems = append(problems, fmt.Sprintf("%s offers %d options, and a choice takes 2 to 255", key, len(options)))
			}
			for option, description := range options {
				if strings.TrimSpace(option) == "" || strings.TrimSpace(description) == "" {
					problems = append(problems, key+" has an option with no key or no description")
				}
			}
		case "score":
			var levels []string
			if json.Unmarshal(question.Criteria, &levels) != nil {
				problems = append(problems, key+" is a score whose criteria are not a list of levels")
				continue
			}
			if len(levels) < 2 || len(levels) > 10 {
				problems = append(problems, fmt.Sprintf("%s has %d levels, and a score takes 2 to 10", key, len(levels)))
			}
		default:
			problems = append(problems, fmt.Sprintf("%s has type %q, and the endpoint takes noul, choice and score", key, question.Type))
		}
	}
	return problems
}

func TestDecisionBodyProblems(t *testing.T) {
	noul := `"q1":{"type":"noul","instructions":"Is it?"}`
	cases := []struct {
		name string
		body string
		want string // a fragment of the problem, or "" for none
	}{
		{"a yes or no", `{"model":"jev-latest","questions":{` + noul + `},"state":"x"}`, ""},
		{"a choice", `{"model":"jev-latest","questions":{"q1":{"type":"choice","instructions":"Which?","criteria":{"o1":"a","o2":"b"}}},"state":"x"}`, ""},
		{"a score", `{"model":"jev-latest","questions":{"q1":{"type":"score","instructions":"How?","criteria":["1","2"]}},"state":{"k":1}}`, ""},
		{"not JSON", `model=jev`, "not a JSON object"},
		{"another model", `{"model":"gpt-6-luna","questions":{` + noul + `},"state":"x"}`, "model ="},
		{"no state", `{"model":"jev-latest","questions":{` + noul + `}}`, "keys"},
		{"a null state", `{"model":"jev-latest","questions":{` + noul + `},"state":null}`, "state is missing"},
		{"an extra key", `{"model":"jev-latest","questions":{` + noul + `},"state":"x","temperature":0}`, "keys"},
		{"keys out of order", `{"model":"jev-latest","state":"x","questions":{` + noul + `}}`, "order"},
		{"a prompt in the body", `{"model":"jev-latest","questions":{"q1":{"type":"noul","instructions":"You answer questions about a subject."}},"state":"x"}`, "prompt"},
		{"a question under another key", `{"model":"jev-latest","questions":{"first":{"type":"noul","instructions":"Is it?"}},"state":"x"}`, "keyed"},
		{"a question with no instructions", `{"model":"jev-latest","questions":{"q1":{"type":"noul","instructions":" "}},"state":"x"}`, "no instructions"},
		{"a noul with criteria", `{"model":"jev-latest","questions":{"q1":{"type":"noul","instructions":"Is it?","criteria":{"true":"y"}}},"state":"x"}`, "carries criteria"},
		{"a choice with one option", `{"model":"jev-latest","questions":{"q1":{"type":"choice","instructions":"Which?","criteria":{"o1":"a"}}},"state":"x"}`, "2 to 255"},
		{"a choice with a list", `{"model":"jev-latest","questions":{"q1":{"type":"choice","instructions":"Which?","criteria":["a","b"]}},"state":"x"}`, "map of option"},
		{"a score with eleven levels", `{"model":"jev-latest","questions":{"q1":{"type":"score","instructions":"How?","criteria":["1","2","3","4","5","6","7","8","9","10","11"]}},"state":"x"}`, "2 to 10"},
		{"a score with a map", `{"model":"jev-latest","questions":{"q1":{"type":"score","instructions":"How?","criteria":{"0":"a"}}},"state":"x"}`, "list of levels"},
		{"a type the endpoint does not take", `{"model":"jev-latest","questions":{"q1":{"type":"text","instructions":"Write."}},"state":"x"}`, "noul, choice and score"},
		{"fewer questions than were asked", `{"model":"jev-latest","questions":{},"state":"x"}`, "0 questions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := decisionBodyProblems(tc.body, "jev-latest", 1)
			if tc.want == "" {
				if len(problems) != 0 {
					t.Errorf("problems found in a body the endpoint takes: %v", problems)
				}
				return
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Errorf("problems %v, want one that says %q", problems, tc.want)
			}
		})
	}
}

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// systemOneKey is the key every test provider is configured with. No error may
// contain it.
const systemOneKey = "sk-test-decider-0123456789abcdef"

// systemOneServer stands in for a System One endpoint on loopback. It records
// what arrived and answers with the status and body it was given.
type systemOneServer struct {
	*httptest.Server

	mu      sync.Mutex
	bodies  []string
	paths   []string
	headers []http.Header
}

func newSystemOneServer(t *testing.T, status int, reply string, header ...string) *systemOneServer {
	t.Helper()
	server := &systemOneServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		server.mu.Lock()
		server.bodies = append(server.bodies, string(raw))
		server.paths = append(server.paths, r.Method+" "+r.URL.Path)
		server.headers = append(server.headers, r.Header.Clone())
		server.mu.Unlock()
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *systemOneServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *systemOneServer) lastBody(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.bodies[len(s.bodies)-1]
}

func systemOneAt(t *testing.T, preset SystemOnePreset, baseURL string) *SystemOneProvider {
	t.Helper()
	provider, err := NewSystemOneProvider(preset, ProviderConfig{APIKey: systemOneKey, BaseURL: baseURL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewSystemOneProvider: %v", err)
	}
	return provider
}

// prototypeQuestion and prototypeBody are how the owner's prototype client
// (schemaflux-jev-prototype, jevops/client.go) wrote a request: the wire format
// that was measured. The provider's body has to be those bytes.
type prototypeQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

func prototypeBody(t *testing.T, model string, state any, questions map[string]prototypeQuestion) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"model": model, "state": state, "questions": questions})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

const systemOneOK = `{"answers":{"q1":{"type":"noul","noul":0.96}},"model":"jev-1.13.0","usage":{"input_tokens":312,"output_tokens":48}}`

func TestSystemOneSendsTheBodyThePrototypeClientSent(t *testing.T) {
	type ticket struct {
		Tier string `json:"customer_tier"`
		Text string `json:"ticket"`
	}
	structState, _ := json.Marshal(ticket{Tier: "enterprise", Text: "My checkout page is blank <after> I click Pay & wait."})

	yesNo := DecisionQuestion{Kind: DecisionYesNo, Ask: "Is the customer asking for a refund?"}
	choice := DecisionQuestion{Kind: DecisionChoice, Ask: "Which team should handle this?", Options: []string{"billing", "technical", "shipping"}}
	scale := DecisionQuestion{Kind: DecisionScale, Ask: "How urgent is this?", Low: 1, High: 5}
	protoYesNo := prototypeQuestion{Type: "noul", Instructions: yesNo.Ask}
	protoChoice := prototypeQuestion{Type: "choice", Instructions: choice.Ask, Criteria: map[string]string{"o1": "billing", "o2": "technical", "o3": "shipping"}}
	protoScale := prototypeQuestion{Type: "score", Instructions: scale.Ask, Criteria: []string{"1 (lowest)", "2", "3", "4", "5 (highest)"}}

	cases := []struct {
		name     string
		decision Decision
		state    any
		want     map[string]prototypeQuestion
	}{
		{"a yes or no", Decision{State: `"Please refund my order."`, Questions: []DecisionQuestion{yesNo}},
			"Please refund my order.", map[string]prototypeQuestion{"q1": protoYesNo}},
		{"a choice", Decision{State: `"Please refund my order."`, Questions: []DecisionQuestion{choice}},
			"Please refund my order.", map[string]prototypeQuestion{"q1": protoChoice}},
		{"a scale", Decision{State: `"Please refund my order."`, Questions: []DecisionQuestion{scale}},
			"Please refund my order.", map[string]prototypeQuestion{"q1": protoScale}},
		{"three questions in one request", Decision{State: `"Please refund my order."`, Questions: []DecisionQuestion{yesNo, choice, scale}},
			"Please refund my order.", map[string]prototypeQuestion{"q1": protoYesNo, "q2": protoChoice, "q3": protoScale}},
		{"eleven questions keep their keys", Decision{State: `"x"`, Questions: []DecisionQuestion{yesNo, yesNo, yesNo, yesNo, yesNo, yesNo, yesNo, yesNo, yesNo, yesNo, scale}},
			"x", map[string]prototypeQuestion{"q1": protoYesNo, "q2": protoYesNo, "q3": protoYesNo, "q4": protoYesNo, "q5": protoYesNo, "q6": protoYesNo,
				"q7": protoYesNo, "q8": protoYesNo, "q9": protoYesNo, "q10": protoYesNo, "q11": protoScale}},
		{"a struct for a state", Decision{State: string(structState), Questions: []DecisionQuestion{yesNo}},
			ticket{Tier: "enterprise", Text: "My checkout page is blank <after> I click Pay & wait."}, map[string]prototypeQuestion{"q1": protoYesNo}},
		{"a state that is not JSON is sent as text", Decision{State: "plain text, not JSON", Questions: []DecisionQuestion{yesNo}},
			"plain text, not JSON", map[string]prototypeQuestion{"q1": protoYesNo}},
		{"options with criteria", Decision{State: `"x"`, Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: "Which?",
			Options: []string{"billing", "technical"}, Criteria: []string{"Payments, invoicing, refunds", ""}}}},
			"x", map[string]prototypeQuestion{"q1": {Type: "choice", Instructions: "Which?",
				Criteria: map[string]string{"o1": "billing: Payments, invoicing, refunds", "o2": "technical"}}}},
		{"guidance goes into every question's instructions", Decision{State: `"x"`, Guidance: "Treat a chargeback threat as urgent.", Questions: []DecisionQuestion{yesNo, scale}},
			"x", map[string]prototypeQuestion{
				"q1": {Type: "noul", Instructions: yesNo.Ask + "\nGuidance: Treat a chargeback threat as urgent."},
				"q2": {Type: "score", Instructions: scale.Ask + "\nGuidance: Treat a chargeback threat as urgent.", Criteria: protoScale.Criteria}}},
		{"a scale that starts below zero", Decision{State: `"x"`, Questions: []DecisionQuestion{{Kind: DecisionScale, Ask: "How?", Low: -1, High: 1}}},
			"x", map[string]prototypeQuestion{"q1": {Type: "score", Instructions: "How?", Criteria: []string{"-1 (lowest)", "0", "1 (highest)"}}}},
		{"a label that is a sentence with quotes", Decision{State: `"x"`, Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: "Which?",
			Options: []string{`The "late parcel" ticket`, "none of these"}}}},
			"x", map[string]prototypeQuestion{"q1": {Type: "choice", Instructions: "Which?",
				Criteria: map[string]string{"o1": `The "late parcel" ticket`, "o2": "none of these"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newSystemOneServer(t, http.StatusOK, systemOneOK)
			provider := systemOneAt(t, SystemOneTypeSafe, server.URL)
			decision := tc.decision
			// The answer does not fit most of these and is refused; the body is
			// the subject.
			_, _ = provider.Complete(context.Background(), CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "ignored", UserPrompt: "ignored", Decision: &decision})

			want := prototypeBody(t, "jev-latest", tc.state, tc.want)
			if got := server.lastBody(t); got != want {
				t.Errorf("the body is not the one the prototype client sent\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestSystemOneReadsTypedAnswers(t *testing.T) {
	yesNo := DecisionQuestion{Kind: DecisionYesNo, Ask: "Is it?"}
	choice := DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: []string{"billing", "technical", "shipping"}}
	scale := DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 1, High: 5}
	reply := func(answers string) string {
		return `{"answers":{` + answers + `},"model":"jev-1.13.0","usage":{"input_tokens":476,"output_tokens":70}}`
	}
	near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

	cases := []struct {
		name      string
		questions []DecisionQuestion
		answers   string
		want      []DecisionAnswer
	}{
		{"a likely yes", []DecisionQuestion{yesNo}, `"q1":{"type":"noul","noul":0.96}`,
			[]DecisionAnswer{{Kind: DecisionYesNo, Yes: true, ModelConfidence: 0.96, ConfidenceReported: true}}},
		{"a likely no reports the probability of no", []DecisionQuestion{yesNo}, `"q1":{"type":"noul","noul":0.2}`,
			[]DecisionAnswer{{Kind: DecisionYesNo, Yes: false, ModelConfidence: 0.8, ConfidenceReported: true}}},
		{"a certain no", []DecisionQuestion{yesNo}, `"q1":{"type":"noul","noul":0}`,
			[]DecisionAnswer{{Kind: DecisionYesNo, Yes: false, ModelConfidence: 1, ConfidenceReported: true}}},
		{"an even chance is a yes", []DecisionQuestion{yesNo}, `"q1":{"type":"noul","noul":0.5}`,
			[]DecisionAnswer{{Kind: DecisionYesNo, Yes: true, ModelConfidence: 0.5, ConfidenceReported: true}}},
		{"a choice with its confidence", []DecisionQuestion{choice}, `"q1":{"type":"choice","choice":"o2","confidence":0.82,"probabilities":{"o1":0.08,"o2":0.85,"o3":0.07}}`,
			[]DecisionAnswer{{Kind: DecisionChoice, Label: "technical", ModelConfidence: 0.82, ConfidenceReported: true}}},
		{"a choice that reports no confidence", []DecisionQuestion{choice}, `"q1":{"type":"choice","choice":"o3"}`,
			[]DecisionAnswer{{Kind: DecisionChoice, Label: "shipping"}}},
		{"a score between two levels is the nearer", []DecisionQuestion{scale}, `"q1":{"type":"score","score":2.99,"confidence":0.99,"probabilities":{"0":0,"1":0,"2":0,"3":1,"4":0}}`,
			[]DecisionAnswer{{Kind: DecisionScale, Level: 4, ModelConfidence: 0.99, ConfidenceReported: true}}},
		{"a score at the bottom", []DecisionQuestion{scale}, `"q1":{"type":"score","score":0,"confidence":0.7}`,
			[]DecisionAnswer{{Kind: DecisionScale, Level: 1, ModelConfidence: 0.7, ConfidenceReported: true}}},
		{"a score at the top", []DecisionQuestion{scale}, `"q1":{"type":"score","score":4,"confidence":0.6}`,
			[]DecisionAnswer{{Kind: DecisionScale, Level: 5, ModelConfidence: 0.6, ConfidenceReported: true}}},
		{"three answers come back in the questions' order", []DecisionQuestion{yesNo, choice, scale},
			`"q3":{"type":"score","score":1.035,"confidence":0.9},"q1":{"type":"noul","noul":0.96},"q2":{"type":"choice","choice":"o1","confidence":0.67}`,
			[]DecisionAnswer{
				{Kind: DecisionYesNo, Yes: true, ModelConfidence: 0.96, ConfidenceReported: true},
				{Kind: DecisionChoice, Label: "billing", ModelConfidence: 0.67, ConfidenceReported: true},
				{Kind: DecisionScale, Level: 2, ModelConfidence: 0.9, ConfidenceReported: true}}},
		{"an answer nobody asked for is not read", []DecisionQuestion{yesNo}, `"q1":{"type":"noul","noul":1},"extra":{"type":"noul","noul":0}`,
			[]DecisionAnswer{{Kind: DecisionYesNo, Yes: true, ModelConfidence: 1, ConfidenceReported: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newSystemOneServer(t, http.StatusOK, reply(tc.answers))
			provider := systemOneAt(t, SystemOneTypeSafe, server.URL)
			resp, err := provider.Complete(context.Background(), CompletionRequest{Decision: &Decision{State: `"x"`, Questions: tc.questions}})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if len(resp.DecisionAnswers) != len(tc.want) {
				t.Fatalf("%d answers, want %d", len(resp.DecisionAnswers), len(tc.want))
			}
			for i, want := range tc.want {
				got := resp.DecisionAnswers[i]
				if got.Kind != want.Kind || got.Yes != want.Yes || got.Label != want.Label || got.Level != want.Level ||
					got.ConfidenceReported != want.ConfidenceReported || !near(got.ModelConfidence, want.ModelConfidence) {
					t.Errorf("answer %d = %+v, want %+v", i+1, got, want)
				}
			}
			if resp.Content != "" {
				t.Errorf("the provider wrote content %q; it answers typed and writes no JSON", resp.Content)
			}
			if resp.Provider != "jev" || resp.Model != "jev-1.13.0" {
				t.Errorf("answered by %s/%s, want jev/jev-1.13.0", resp.Provider, resp.Model)
			}
			if resp.Usage.PromptTokens != 476 || resp.Usage.CompletionTokens != 70 || resp.Usage.TotalTokens != 546 {
				t.Errorf("usage %+v, want 476 in, 70 out, 546 in all", resp.Usage)
			}
		})
	}
}

func TestSystemOneNamesTheUpstreamAndFallsBackToTheModelItSent(t *testing.T) {
	server := newSystemOneServer(t, http.StatusOK, `{"provider":"TypeSafe","answers":{"q1":{"type":"noul","noul":0.9}},"usage":{"input_tokens":3,"output_tokens":0}}`)
	provider := systemOneAt(t, SystemOneOpenRouter, server.URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Decision: &Decision{State: `"x"`, Questions: []DecisionQuestion{{Kind: DecisionYesNo, Ask: "Is it?"}}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.UpstreamProvider != "TypeSafe" {
		t.Errorf("upstream %q, want the vendor the gateway named", resp.UpstreamProvider)
	}
	if resp.Model != "~typesafe/jev-latest" || resp.Provider != "jev-openrouter" {
		t.Errorf("answered by %s/%s; an answer that names no model is the model that was sent", resp.Provider, resp.Model)
	}
	if !resp.Timing.Measured() {
		t.Error("the HTTP call was not timed")
	}
}

func TestSystemOneRefusesAnAnswerThatIsNotOne(t *testing.T) {
	yesNo := DecisionQuestion{Kind: DecisionYesNo, Ask: "Is it?"}
	choice := DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: []string{"billing", "technical"}}
	scale := DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 1, High: 3}
	const leak = "SECRET-RESPONSE-TEXT"

	cases := []struct {
		name      string
		questions []DecisionQuestion
		reply     string
	}{
		{"a page of HTML", []DecisionQuestion{yesNo}, `<html><body>` + leak + `</body></html>`},
		{"an empty object", []DecisionQuestion{yesNo}, `{}`},
		{"a list", []DecisionQuestion{yesNo}, `["` + leak + `"]`},
		{"a chat completion", []DecisionQuestion{yesNo}, `{"choices":[{"message":{"content":"` + leak + `"}}]}`},
		{"a body cut off part way", []DecisionQuestion{yesNo}, `{"answers":{"q1":{"type":"noul","noul":0.9`},
		{"an empty body", []DecisionQuestion{yesNo}, ``},
		{"a question left unanswered", []DecisionQuestion{yesNo, choice}, `{"answers":{"q1":{"type":"noul","noul":0.9}}}`},
		{"an answer of null", []DecisionQuestion{yesNo}, `{"answers":{"q1":null}}`},
		{"a noul with no probability", []DecisionQuestion{yesNo}, `{"answers":{"q1":{"type":"noul"}}}`},
		{"a probability above one", []DecisionQuestion{yesNo}, `{"answers":{"q1":{"type":"noul","noul":1.4}}}`},
		{"a probability below zero", []DecisionQuestion{yesNo}, `{"answers":{"q1":{"type":"noul","noul":-0.1}}}`},
		{"a probability that is a word", []DecisionQuestion{yesNo}, `{"answers":{"q1":{"type":"noul","noul":"` + leak + `"}}}`},
		{"a yes or no answered as a choice", []DecisionQuestion{yesNo}, `{"answers":{"q1":{"type":"choice","choice":"o1","noul":0.9}}}`},
		{"a choice of a key that was not offered", []DecisionQuestion{choice}, `{"answers":{"q1":{"type":"choice","choice":"o3","confidence":0.9}}}`},
		{"a choice that names the label and not its key", []DecisionQuestion{choice}, `{"answers":{"q1":{"type":"choice","choice":"billing","confidence":0.9}}}`},
		{"a choice with no choice", []DecisionQuestion{choice}, `{"answers":{"q1":{"type":"choice","confidence":0.9}}}`},
		{"a confidence above one", []DecisionQuestion{choice}, `{"answers":{"q1":{"type":"choice","choice":"o1","confidence":7}}}`},
		{"a score past the top level", []DecisionQuestion{scale}, `{"answers":{"q1":{"type":"score","score":3,"confidence":0.9}}}`},
		{"a score below the bottom level", []DecisionQuestion{scale}, `{"answers":{"q1":{"type":"score","score":-1,"confidence":0.9}}}`},
		{"a score with no score", []DecisionQuestion{scale}, `{"answers":{"q1":{"type":"score","confidence":0.9}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newSystemOneServer(t, http.StatusOK, tc.reply)
			provider := systemOneAt(t, SystemOneTypeSafe, server.URL)
			resp, err := provider.Complete(context.Background(), CompletionRequest{Decision: &Decision{State: `"x"`, Questions: tc.questions}})
			if err == nil {
				t.Fatalf("answered %+v, want a refusal", resp.DecisionAnswers)
			}
			if kind := Classify(err); kind != types.KindMalformedOutput {
				t.Errorf("kind %v, want malformed output", kind)
			}
			if len(resp.DecisionAnswers) != 0 || resp.Content != "" {
				t.Errorf("a refusal came with an answer beside it: %+v", resp)
			}
			systemOneSaysNothingOfTheirs(t, err, leak)
		})
	}
}

// systemOneSaysNothingOfTheirs fails when an error, printed any of the ways a
// caller's logging prints it, contains the key or any of the given text.
func systemOneSaysNothingOfTheirs(t *testing.T, err error, withheld ...string) {
	t.Helper()
	for _, printed := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		for _, secret := range append([]string{systemOneKey}, withheld...) {
			if secret != "" && strings.Contains(printed, secret) {
				t.Errorf("the error contains %q: %s", secret, printed)
			}
		}
	}
}

func TestSystemOneClassifiesEveryHTTPFailure(t *testing.T) {
	const leak = "SECRET-RESPONSE-TEXT"
	echo := `{"error":{"message":"bad key ` + systemOneKey + ` for state ` + leak + `","type":"invalid_request_error","code":"bad"}}`
	cases := []struct {
		name       string
		status     int
		reply      string
		retryAfter string
		want       types.ErrorKind
		wait       time.Duration
	}{
		{"401", http.StatusUnauthorized, echo, "", types.KindAuthentication, 0},
		{"403", http.StatusForbidden, echo, "", types.KindPermission, 0},
		{"400", http.StatusBadRequest, echo, "", types.KindInvalidRequest, 0},
		{"404", http.StatusNotFound, leak, "", types.KindInvalidRequest, 0},
		{"413", http.StatusRequestEntityTooLarge, echo, "", types.KindContextTooLarge, 0},
		{"429 with Retry-After", http.StatusTooManyRequests, echo, "7", types.KindRateLimited, 7 * time.Second},
		{"429 without", http.StatusTooManyRequests, echo, "", types.KindRateLimited, 0},
		{"500", http.StatusInternalServerError, leak, "", types.KindProviderUnavailable, 0},
		{"502", http.StatusBadGateway, `<html>` + leak + `</html>`, "", types.KindProviderUnavailable, 0},
		{"503 with Retry-After", http.StatusServiceUnavailable, echo, "3", types.KindProviderUnavailable, 3 * time.Second},
		{"529, the endpoint's own overload status", 529, echo, "", types.KindProviderUnavailable, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var header []string
			if tc.retryAfter != "" {
				header = []string{"Retry-After", tc.retryAfter}
			}
			server := newSystemOneServer(t, tc.status, tc.reply, header...)
			provider := systemOneAt(t, SystemOneTypeSafe, server.URL)
			resp, err := provider.Complete(context.Background(), CompletionRequest{Decision: &Decision{State: `"` + leak + `"`, Questions: []DecisionQuestion{{Kind: DecisionYesNo, Ask: "Is it?"}}}})
			if err == nil {
				t.Fatal("no error for a failed status")
			}
			if kind := Classify(err); kind != tc.want {
				t.Errorf("kind %v, want %v", kind, tc.want)
			}
			if status, ok := StatusCodeFrom(err); !ok || status != tc.status {
				t.Errorf("status %d (%v), want %d", status, ok, tc.status)
			}
			if wait, _ := RetryAfterFrom(err); wait != tc.wait {
				t.Errorf("retry after %v, want %v", wait, tc.wait)
			}
			if len(resp.DecisionAnswers) != 0 {
				t.Errorf("a failure came with answers beside it")
			}
			systemOneSaysNothingOfTheirs(t, err, leak)
			// What is kept for a caller who asks has the key taken out.
			var apiErr *APIError
			if errors.As(err, &apiErr) && (strings.Contains(apiErr.Body, systemOneKey) || strings.Contains(apiErr.Message, systemOneKey)) {
				t.Error("the key is kept on the error")
			}
		})
	}
}

func TestSystemOneTimesOutAsATimeoutAndNamesNoURL(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	decision := &Decision{State: `"x"`, Questions: []DecisionQuestion{{Kind: DecisionYesNo, Ask: "Is it?"}}}
	withSecret := server.URL + "/proxy-SECRET-PATH"

	t.Run("the caller's deadline", func(t *testing.T) {
		provider := systemOneAt(t, SystemOneTypeSafe, withSecret)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		_, err := provider.Complete(ctx, CompletionRequest{Decision: decision})
		if kind := Classify(err); kind != types.KindTimeout {
			t.Errorf("kind %v (%v), want a timeout", kind, err)
		}
		systemOneSaysNothingOfTheirs(t, err, "SECRET-PATH", server.URL)
	})
	t.Run("the provider's own timeout", func(t *testing.T) {
		provider, err := NewSystemOneProvider(SystemOneTypeSafe, ProviderConfig{APIKey: systemOneKey, BaseURL: withSecret, Timeout: 40 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Complete(context.Background(), CompletionRequest{Decision: decision})
		if kind := Classify(err); kind != types.KindTimeout {
			t.Errorf("kind %v (%v), want a timeout", kind, err)
		}
		systemOneSaysNothingOfTheirs(t, err, "SECRET-PATH", server.URL)
	})
	t.Run("a cancelled call", func(t *testing.T) {
		provider := systemOneAt(t, SystemOneTypeSafe, withSecret)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := provider.Complete(ctx, CompletionRequest{Decision: decision})
		if kind := Classify(err); kind != types.KindCanceled {
			t.Errorf("kind %v (%v), want a cancellation", kind, err)
		}
		systemOneSaysNothingOfTheirs(t, err, "SECRET-PATH", server.URL)
	})
	t.Run("a server that is not there", func(t *testing.T) {
		gone := httptest.NewServer(http.NotFoundHandler())
		address := gone.URL
		gone.Close()
		provider := systemOneAt(t, SystemOneTypeSafe, address+"/proxy-SECRET-PATH")
		_, err := provider.Complete(context.Background(), CompletionRequest{Decision: decision})
		if kind := Classify(err); kind != types.KindProviderUnavailable {
			t.Errorf("kind %v (%v), want the provider unavailable", kind, err)
		}
		systemOneSaysNothingOfTheirs(t, err, "SECRET-PATH", address)
	})
}

func TestSystemOneRefusesWhatItCannotAnswerBeforeAnythingIsSent(t *testing.T) {
	yesNo := DecisionQuestion{Kind: DecisionYesNo, Ask: "Is it?"}
	labels := make([]string, 256)
	for i := range labels {
		labels[i] = fmt.Sprintf("label-%03d", i)
	}
	decision := func(questions ...DecisionQuestion) *Decision {
		return &Decision{State: `"x"`, Questions: questions}
	}
	cases := []struct {
		name string
		req  CompletionRequest
		want types.ErrorKind
	}{
		{"a request for text", CompletionRequest{SystemPrompt: "Summarize.", UserPrompt: "A long document."}, types.KindUnsupportedCapability},
		{"a request for a structure", CompletionRequest{SystemPrompt: "Return a JSON object.", UserPrompt: "x", ResponseFormat: "json",
			JSONSchema: map[string]any{"type": "object"}}, types.KindUnsupportedCapability},
		{"a decision with no questions", CompletionRequest{Decision: decision()}, types.KindUnsupportedCapability},
		{"256 options", CompletionRequest{Decision: decision(DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: labels})}, types.KindUnsupportedCapability},
		{"a scale of eleven levels", CompletionRequest{Decision: decision(DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 0, High: 10})}, types.KindUnsupportedCapability},
		{"a scale of a hundred levels", CompletionRequest{Decision: decision(DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 1, High: 100})}, types.KindUnsupportedCapability},
		{"a state longer than the context", CompletionRequest{Decision: &Decision{State: `"` + strings.Repeat("word ", 26000) + `"`, Questions: []DecisionQuestion{yesNo}}}, types.KindUnsupportedCapability},
		{"a request that offers tools", CompletionRequest{Decision: decision(yesNo), Tools: []Tool{{Name: "lookup"}}}, types.KindUnsupportedCapability},
		{"a request that asks for a web search", CompletionRequest{Decision: decision(yesNo), WebSearch: true}, types.KindUnsupportedCapability},
		{"a transcript", CompletionRequest{Decision: decision(yesNo), Messages: []Message{{Role: "user", Content: "hi"}}}, types.KindUnsupportedCapability},
		{"a tool turn with no decision", CompletionRequest{Tools: []Tool{{Name: "lookup"}}, Messages: []Message{{Role: "tool", Content: "A long document.", ToolCallID: "call_1"}}}, types.KindUnsupportedCapability},
		{"tools with no decision", CompletionRequest{UserPrompt: "x", Tools: []Tool{{Name: "lookup"}}, ToolChoice: "required"}, types.KindUnsupportedCapability},
		{"a reasoning effort the caller asked for", CompletionRequest{Decision: decision(yesNo), ReasoningEffort: "high"}, types.KindUnsupportedCapability},
		{"a question of no kind", CompletionRequest{Decision: decision(DecisionQuestion{Ask: "?"})}, types.KindInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newSystemOneServer(t, http.StatusOK, systemOneOK)
			provider := systemOneAt(t, SystemOneTypeSafe, server.URL)
			_, err := provider.Complete(context.Background(), tc.req)
			if err == nil {
				t.Fatal("answered, want a refusal")
			}
			if kind := types.KindOf(err); kind != tc.want {
				t.Errorf("kind %v (%v), want %v", kind, err, tc.want)
			}
			if server.calls() != 0 {
				t.Errorf("%d requests were sent for something the provider cannot answer", server.calls())
			}
			systemOneSaysNothingOfTheirs(t, err, "label-", "word word", "A long document")
		})
	}

	// What it does take: the limits themselves, and an effort that is only an
	// operation's preference.
	t.Run("at the limits", func(t *testing.T) {
		server := newSystemOneServer(t, http.StatusOK, systemOneOK)
		provider := systemOneAt(t, SystemOneTypeSafe, server.URL)
		for _, req := range []CompletionRequest{
			{Decision: decision(yesNo), ReasoningEffort: "none", ReasoningEffortPreferred: true},
			{Decision: decision(yesNo), JSONSchema: map[string]any{"type": "object"}, ResponseFormat: "json", SchemaName: "assessment"},
		} {
			if _, err := provider.Complete(context.Background(), req); err != nil {
				t.Errorf("refused: %v", err)
			}
		}
		for _, req := range []CompletionRequest{
			{Decision: decision(DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: labels[:255]})},
			{Decision: decision(DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 1, High: 10})},
		} {
			// The stand-in's answer is to another question and is refused as
			// malformed; what matters is that the request went out.
			before := server.calls()
			_, _ = provider.Complete(context.Background(), req)
			if server.calls() != before+1 {
				t.Error("a decision at the limit was not sent")
			}
		}
	})
}

func TestSystemOnePresetsDifferOnlyInWhereAndAsWhom(t *testing.T) {
	decision := &Decision{State: `"Please refund my order."`, Guidance: "Be strict.", Questions: threeKinds().Questions}
	presets := []struct {
		preset   SystemOnePreset
		name     string
		path     string
		model    string
		variable string
	}{
		{SystemOneTypeSafe, "jev", "POST /v1/systemone", "jev-latest", "TYPESAFE_API_KEY"},
		{SystemOneOpenRouter, "jev-openrouter", "POST /v1/systemone", "~typesafe/jev-latest", "OPENROUTER_API_KEY"},
		{SystemOneLiteLLM, "jev-litellm", "POST /typesafe/v1/systemone", "jev-latest", "LITELLM_API_KEY"},
	}

	var pinnedBodies []string
	for _, tc := range presets {
		t.Run(tc.name, func(t *testing.T) {
			server := newSystemOneServer(t, http.StatusOK, systemOneOK)
			provider := systemOneAt(t, tc.preset, server.URL+"/")
			if provider.Name() != tc.name {
				t.Errorf("named %q, want %q", provider.Name(), tc.name)
			}

			// The generating provider's model is not this endpoint's: the preset's is sent.
			_, _ = provider.Complete(context.Background(), CompletionRequest{Model: "gpt-6-luna", Decision: decision})
			var sent struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal([]byte(server.lastBody(t)), &sent); err != nil || sent.Model != tc.model {
				t.Errorf("sent model %q (%v), want %q", sent.Model, err, tc.model)
			}
			if got := server.paths[0]; got != tc.path {
				t.Errorf("sent to %q, want %q", got, tc.path)
			}
			if got := server.headers[0].Get("Authorization"); got != "Bearer "+systemOneKey {
				t.Errorf("the key was not sent as a bearer token")
			}
			if got := server.headers[0].Get("Content-Type"); got != "application/json" {
				t.Errorf("content type %q", got)
			}
			if limits := provider.DecisionLimits(); limits != (DecisionLimits{MaxOptions: 255, MaxLevels: 10, MaxStateTokens: 32000}) {
				t.Errorf("limits %+v", limits)
			}

			// One of the endpoint's own models, named by the caller, is sent as named.
			_, _ = provider.Complete(context.Background(), CompletionRequest{Model: "jev-1.13.0", Decision: decision})
			pinnedBodies = append(pinnedBodies, server.lastBody(t))

			// The key comes from the preset's own variable when the configuration has none.
			for _, name := range append(append([]string{}, tc.preset.KeyVariables...), "SCHEMAFLUX_API_KEY") {
				t.Setenv(name, "")
			}
			if _, err := NewSystemOneProvider(tc.preset, ProviderConfig{BaseURL: server.URL}); err == nil || types.KindOf(err) != types.KindConfiguration {
				t.Errorf("built with no key anywhere: %v", err)
			}
			t.Setenv(tc.variable, "sk-from-the-environment")
			fromEnv, err := NewSystemOneProvider(tc.preset, ProviderConfig{BaseURL: server.URL})
			if err != nil {
				t.Fatalf("with %s set: %v", tc.variable, err)
			}
			_, _ = fromEnv.Complete(context.Background(), CompletionRequest{Decision: decision})
			if got := server.headers[len(server.headers)-1].Get("Authorization"); got != "Bearer sk-from-the-environment" {
				t.Errorf("the key of %s was not the one sent", tc.variable)
			}
		})
	}
	if len(pinnedBodies) == 3 && (pinnedBodies[0] != pinnedBodies[1] || pinnedBodies[1] != pinnedBodies[2]) {
		t.Errorf("under one model the three presets send different bodies:\n%s\n%s\n%s", pinnedBodies[0], pinnedBodies[1], pinnedBodies[2])
	}
}

func TestSystemOneKeyAndBaseURLComeFromTheConfigurationThenTheEnvironment(t *testing.T) {
	clear := func(t *testing.T) {
		for _, name := range []string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY", "SCHEMAFLUX_API_KEY", "SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL",
			"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY", "SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"} {
			t.Setenv(name, "")
		}
	}
	cases := []struct {
		name    string
		preset  SystemOnePreset
		config  ProviderConfig
		env     map[string]string
		wantKey string
		wantURL string
		wantErr string
	}{
		{"an explicit key wins over every variable", SystemOneTypeSafe, ProviderConfig{APIKey: "explicit"},
			map[string]string{"SCHEMAFLUX_TYPESAFE_API_KEY": "prefixed", "TYPESAFE_API_KEY": "vendor", "SCHEMAFLUX_API_KEY": "shared"}, "explicit", "https://api.typesafe.ai/v1/systemone", ""},
		{"the library's own variable comes first", SystemOneTypeSafe, ProviderConfig{},
			map[string]string{"SCHEMAFLUX_TYPESAFE_API_KEY": "prefixed", "TYPESAFE_API_KEY": "vendor", "SCHEMAFLUX_API_KEY": "shared"}, "prefixed", "https://api.typesafe.ai/v1/systemone", ""},
		{"then the vendor's", SystemOneTypeSafe, ProviderConfig{},
			map[string]string{"TYPESAFE_API_KEY": "vendor", "SCHEMAFLUX_API_KEY": "shared"}, "vendor", "https://api.typesafe.ai/v1/systemone", ""},
		{"then the shared one", SystemOneTypeSafe, ProviderConfig{}, map[string]string{"SCHEMAFLUX_API_KEY": "shared"}, "shared", "https://api.typesafe.ai/v1/systemone", ""},
		{"a variable set to space is not set", SystemOneTypeSafe, ProviderConfig{},
			map[string]string{"SCHEMAFLUX_TYPESAFE_API_KEY": "   ", "TYPESAFE_API_KEY": "vendor"}, "vendor", "https://api.typesafe.ai/v1/systemone", ""},
		{"no key anywhere", SystemOneTypeSafe, ProviderConfig{}, nil, "", "", "TYPESAFE_API_KEY"},
		{"a configured base URL wins", SystemOneTypeSafe, ProviderConfig{APIKey: "k", BaseURL: "https://gateway.example/jev/"},
			map[string]string{"TYPESAFE_BASE_URL": "https://from-env.example"}, "k", "https://gateway.example/jev/v1/systemone", ""},
		{"a base URL from the environment", SystemOneTypeSafe, ProviderConfig{APIKey: "k"},
			map[string]string{"TYPESAFE_BASE_URL": "https://from-env.example"}, "k", "https://from-env.example/v1/systemone", ""},
		{"a proxy has no default base URL", SystemOneLiteLLM, ProviderConfig{APIKey: "k"}, nil, "", "", "LITELLM_BASE_URL"},
		{"a proxy's base URL from the environment", SystemOneLiteLLM, ProviderConfig{APIKey: "k"},
			map[string]string{"LITELLM_BASE_URL": "http://localhost:4000"}, "k", "http://localhost:4000/typesafe/v1/systemone", ""},
		{"a preset with no path", SystemOnePreset{Name: "custom", Model: "m", AuthHeader: "Authorization"}, ProviderConfig{APIKey: "k", BaseURL: "https://x.example"}, nil, "", "", "preset"},
		{"a service tier", SystemOneTypeSafe, ProviderConfig{APIKey: "k", ServiceTier: "priority"}, nil, "", "", "service tier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clear(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			provider, err := NewSystemOneProvider(tc.preset, tc.config)
			if tc.wantErr != "" {
				if err == nil || types.KindOf(err) != types.KindConfiguration || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want a configuration error that says %q", err, tc.wantErr)
				}
				if provider != nil {
					t.Error("a provider came back beside the error")
				}
				for _, value := range tc.env {
					if strings.TrimSpace(value) != "" && strings.Contains(err.Error(), value) {
						t.Errorf("the error contains a variable's value: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSystemOneProvider: %v", err)
			}
			if provider.config.APIKey != tc.wantKey {
				t.Errorf("the key is not the one expected (%d bytes long)", len(provider.config.APIKey))
			}
			if provider.endpoint != tc.wantURL {
				t.Errorf("endpoint %q, want %q", provider.endpoint, tc.wantURL)
			}
		})
	}
}

func TestSystemOneModelSentFor(t *testing.T) {
	direct := &SystemOneProvider{preset: SystemOneTypeSafe}
	gateway := &SystemOneProvider{preset: SystemOneOpenRouter}
	cases := []struct {
		name      string
		provider  *SystemOneProvider
		requested string
		want      string
	}{
		{"no model", direct, "", "jev-latest"},
		{"space", direct, "   ", "jev-latest"},
		{"the generating provider's model", direct, "gpt-6-luna", "jev-latest"},
		{"a Claude model", direct, "claude-3-5-sonnet-20240620", "jev-latest"},
		{"the alias itself", direct, "jev-latest", "jev-latest"},
		{"a pinned version", direct, "jev-1.13.0", "jev-1.13.0"},
		{"a preview", direct, " jev-preview ", "jev-preview"},
		{"in capitals", direct, "JEV-1.13.0", "JEV-1.13.0"},
		{"a name that only contains the family", direct, "not-jev", "jev-latest"},
		{"the gateway's default", gateway, "openai/gpt-4o-mini", "~typesafe/jev-latest"},
		{"the gateway's name for a version", gateway, "typesafe/jev-1.13", "typesafe/jev-1.13"},
		{"the gateway's rolling alias", gateway, "~typesafe/jev-latest", "~typesafe/jev-latest"},
		{"a preset with no family sends its own model", &SystemOneProvider{preset: SystemOnePreset{Model: "decide-1"}}, "decide-2", "decide-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.provider.ModelSentFor(tc.requested); got != tc.want {
				t.Errorf("ModelSentFor(%q) = %q, want %q", tc.requested, got, tc.want)
			}
			if got := ModelSentBy(tc.provider, tc.requested); got != tc.want {
				t.Errorf("ModelSentBy(%q) = %q, want %q", tc.requested, got, tc.want)
			}
		})
	}
}

func TestTheDecisionProvidersAreRegisteredByName(t *testing.T) {
	for _, name := range []string{"jev", "jev-openrouter", "jev-litellm", " JEV "} {
		t.Run(name, func(t *testing.T) {
			provider, err := CreateProvider(name, ProviderConfig{APIKey: systemOneKey, BaseURL: "http://127.0.0.1:1"})
			if err != nil {
				t.Fatalf("CreateProvider(%q): %v", name, err)
			}
			if provider.Name() != NormalizeProviderName(name) {
				t.Errorf("named %q", provider.Name())
			}
			if _, declared := DecisionLimitsOf(provider); !declared {
				t.Error("the provider declares no decision limits")
			}
			if _, warms := provider.(Warmer); !warms {
				t.Error("the provider cannot be warmed")
			}
		})
	}

	// A failure to build is an error beside a nil Provider, not beside a typed
	// nil pointer that passes a nil check.
	for _, name := range []string{"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY", "SCHEMAFLUX_API_KEY", "SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"} {
		t.Setenv(name, "")
	}
	provider, err := CreateProvider("jev-litellm", ProviderConfig{})
	if err == nil || provider != nil {
		t.Errorf("CreateProvider with nothing configured = %v, %v", provider, err)
	}

	listed := strings.Join(ListProviders(), ",")
	for _, name := range []string{"jev", "jev-openrouter", "jev-litellm"} {
		if !strings.Contains(","+listed+",", ","+name+",") {
			t.Errorf("%s is not among the registered providers: %s", name, listed)
		}
	}
}

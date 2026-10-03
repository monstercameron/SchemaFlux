package provider

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The conversions between the root's types and the internal ones are the root's,
// and reach this package through internal/bind. The tests below read them through
// the accessors, which is how the adapters of this package do.

func requestToInternal(r schemaflux.Request) llm.CompletionRequest { return bind.RequestToInternal(r) }

func requestFromInternal(r llm.CompletionRequest) (schemaflux.Request, error) {
	return bind.RequestFromInternal[schemaflux.Request](r)
}

func responseToInternal(r schemaflux.Response) llm.CompletionResponse {
	return bind.ResponseToInternal(r)
}

func responseFromInternal(r llm.CompletionResponse) (schemaflux.Response, error) {
	return bind.ResponseFromInternal[schemaflux.Response](r)
}

func usageToInternal(u schemaflux.Usage) types.TokenUsage { return bind.UsageToInternal(u) }

func timingToInternal(t schemaflux.Timing) types.Timing { return bind.TimingToInternal(t) }

func timingFromInternal(t types.Timing) schemaflux.Timing {
	return bind.TimingFromInternal[schemaflux.Timing](t)
}

// The decision is carried by the request, and its answers and escalation by the
// response.
func decisionToInternal(d *schemaflux.Decision) *llm.Decision {
	return requestToInternal(schemaflux.Request{Decision: d}).Decision
}

func decisionFromInternal(d *llm.Decision) *schemaflux.Decision {
	req, _ := requestFromInternal(llm.CompletionRequest{Decision: d})
	return req.Decision
}

func answersToInternal(a []schemaflux.DecisionAnswer) []llm.DecisionAnswer {
	return responseToInternal(schemaflux.Response{Answers: a}).DecisionAnswers
}

func answersFromInternal(a []llm.DecisionAnswer) []schemaflux.DecisionAnswer {
	resp, _ := responseFromInternal(llm.CompletionResponse{DecisionAnswers: a})
	return resp.Answers
}

func escalationToInternal(e *schemaflux.DecisionEscalation) *llm.DecisionEscalation {
	return responseToInternal(schemaflux.Response{Escalation: e}).DecisionEscalation
}

func escalationFromInternal(e *llm.DecisionEscalation) *schemaflux.DecisionEscalation {
	resp, _ := responseFromInternal(llm.CompletionResponse{DecisionEscalation: e})
	return resp.Escalation
}

// fill sets every field of v, however deep, to a value that is not its zero
// value, so that a conversion that drops one is seen.
func fill(v reflect.Value, n *int) {
	*n++
	switch v.Kind() {
	case reflect.String:
		v.SetString("s" + string(rune('a'+*n%26)))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*n + 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*n%2 + 1))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(*n%7+1) / 8)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), n)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		for i := 0; i < v.Len(); i++ {
			fill(v.Index(i), n)
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(key, n)
		fill(value, n)
		v.SetMapIndex(key, value)
	case reflect.Interface:
		v.Set(reflect.ValueOf("any"))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), n)
			}
		}
	}
}

// zeroFields lists the exported fields of the struct v that hold their zero value.
func zeroFields(v reflect.Value) []string {
	var names []string
	for i := 0; i < v.NumField(); i++ {
		if v.Type().Field(i).IsExported() && v.Field(i).IsZero() {
			names = append(names, v.Type().Field(i).Name)
		}
	}
	return names
}

// The fields of the internal types that the root's types do not have, on purpose:
// no code of the library sets a tool choice, so a Request has none and a request
// that sets one is refused.
var (
	requestNotCarried  = map[string]bool{"ToolChoice": true}
	responseNotCarried = map[string]bool{}
)

func TestEveryFieldOfARequestReachesTheInternalRequest(t *testing.T) {
	t.Parallel()
	var req schemaflux.Request
	fill(reflect.ValueOf(&req).Elem(), new(int))
	internal := requestToInternal(req)
	for _, name := range zeroFields(reflect.ValueOf(internal)) {
		if !requestNotCarried[name] {
			t.Errorf("a Request with every field set gives the internal request an empty %s", name)
		}
	}
	// A request that decides and also carries tools is refused, so the trip back
	// is made twice: with the decision, and with the tools and the messages.
	withoutTools := internal
	withoutTools.Tools, withoutTools.Messages = nil, nil
	reqWithoutTools := req
	reqWithoutTools.Tools, reqWithoutTools.Messages = nil, nil
	if back, err := requestFromInternal(withoutTools); err != nil || !reflect.DeepEqual(back, reqWithoutTools) {
		t.Errorf("the request with its decision did not come back as it went: %v\n%+v\n%+v", err, back, reqWithoutTools)
	}
	withoutDecision := internal
	withoutDecision.Decision = nil
	reqWithoutDecision := req
	reqWithoutDecision.Decision = nil
	if back, err := requestFromInternal(withoutDecision); err != nil || !reflect.DeepEqual(back, reqWithoutDecision) {
		t.Errorf("the request with its tools did not come back as it went: %v\n%+v\n%+v", err, back, reqWithoutDecision)
	}
	if _, err := requestFromInternal(internal); err == nil {
		t.Error("a request that decides and carries tools was passed on")
	}
}

func TestEveryFieldOfAnInternalRequestIsCarriedOrListed(t *testing.T) {
	t.Parallel()
	var internal llm.CompletionRequest
	fill(reflect.ValueOf(&internal).Elem(), new(int))
	internal.ToolChoice = ""
	// The decision and the tools are not carried together (a request that has both
	// is refused), so each is read from its own conversion.
	withoutTools := internal
	withoutTools.Tools, withoutTools.Messages = nil, nil
	req, err := requestFromInternal(withoutTools)
	if err != nil {
		t.Fatalf("requestFromInternal: %v", err)
	}
	withoutDecision := internal
	withoutDecision.Decision = nil
	toolsReq, err := requestFromInternal(withoutDecision)
	if err != nil {
		t.Fatalf("requestFromInternal: %v", err)
	}
	req.Tools, req.Messages = toolsReq.Tools, toolsReq.Messages
	for _, name := range zeroFields(reflect.ValueOf(req)) {
		t.Errorf("an internal request with every field set gives the Request an empty %s", name)
	}
	// A field added to the internal request that neither conversion carries
	// shows up here: it is non-zero in `internal` and has no counterpart.
	count := 0
	typ := reflect.TypeOf(internal)
	for i := 0; i < typ.NumField(); i++ {
		if !requestNotCarried[typ.Field(i).Name] {
			count++
		}
	}
	if want := reflect.TypeOf(schemaflux.Request{}).NumField(); count != want {
		t.Errorf("the internal request has %d carried fields and the Request %d: a field was added to one of them", count, want)
	}
}

func TestEveryFieldOfAResponseSurvivesTheTripThroughTheInternalResponse(t *testing.T) {
	t.Parallel()
	var resp schemaflux.Response
	fill(reflect.ValueOf(&resp).Elem(), new(int))
	resp.Usage.Total = resp.Usage.Input + resp.Usage.Output
	internal := responseToInternal(resp)
	for _, name := range zeroFields(reflect.ValueOf(internal)) {
		if !responseNotCarried[name] {
			t.Errorf("a Response with every field set gives the internal response an empty %s", name)
		}
	}
	back, err := responseFromInternal(internal)
	if err != nil {
		t.Fatalf("responseFromInternal: %v", err)
	}
	if !reflect.DeepEqual(back, resp) {
		t.Errorf("the response did not come back as it went:\n%+v\n%+v", back, resp)
	}
}

func TestEveryFieldOfAnInternalResponseIsCarriedOrListed(t *testing.T) {
	t.Parallel()
	var internal llm.CompletionResponse
	fill(reflect.ValueOf(&internal).Elem(), new(int))
	resp, err := responseFromInternal(internal)
	if err != nil {
		t.Fatalf("responseFromInternal: %v", err)
	}
	for _, name := range zeroFields(reflect.ValueOf(resp)) {
		t.Errorf("an internal response with every field set gives the Response an empty %s", name)
	}
}

func TestUsageTotalIsTheSumWhenTheProviderLeftItZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in    schemaflux.Usage
		total int
	}{
		{schemaflux.Usage{Input: 3, Output: 4}, 7},
		{schemaflux.Usage{Input: 3, Output: 4, Total: 9}, 9},
		{schemaflux.Usage{}, 0},
		{schemaflux.Usage{Input: 5}, 5},
		{schemaflux.Usage{Output: 5}, 5},
		{schemaflux.Usage{Input: 1, Output: 1, Cached: 1, CacheWrite: 1, Reasoning: 1}, 2},
	}
	for _, tt := range tests {
		got := usageToInternal(tt.in)
		if got.TotalTokens != tt.total || got.PromptTokens != tt.in.Input || got.CompletionTokens != tt.in.Output ||
			got.CachedTokens != tt.in.Cached || got.CacheWriteTokens != tt.in.CacheWrite || got.ReasoningTokens != tt.in.Reasoning {
			t.Errorf("usageToInternal(%+v) = %+v, want total %d", tt.in, got, tt.total)
		}
	}
}

func TestAnInternalRequestWithAToolChoiceIsRefusedAndNotPassedOnWithItDropped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  llm.CompletionRequest
	}{
		{"a tool choice", llm.CompletionRequest{ToolChoice: "auto"}},
		{"a tool choice beside tools", llm.CompletionRequest{ToolChoice: "required", Tools: []llm.Tool{{Name: "f"}}}},
		{"a tool choice beside a message list", llm.CompletionRequest{ToolChoice: "none", Messages: []llm.Message{{Role: "user", Content: "x"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := requestFromInternal(tt.req)
			if e := errorOf(t, err); e.Kind != schemaflux.ErrUnsupported {
				t.Errorf("got %v, want an unsupported capability", err)
			}
		})
	}
}

func TestAnInternalRequestWithToolsAndAnAnswerWithToolCallsAreCarried(t *testing.T) {
	t.Parallel()
	req, err := requestFromInternal(llm.CompletionRequest{
		Tools:    []llm.Tool{{Name: "f", Description: "d", Parameters: map[string]any{"type": "object"}}},
		Messages: []llm.Message{{Role: "user", Content: "x"}, {Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}}}},
	})
	if err != nil || len(req.Tools) != 1 || req.Tools[0].Name != "f" || len(req.Messages) != 2 || req.Messages[1].ToolCalls[0].ID != "1" {
		t.Errorf("got %+v, %v", req, err)
	}
	resp, err := responseFromInternal(llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}}})
	if err != nil || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "1" || string(resp.ToolCalls[0].Arguments) != `{"a":1}` || resp.Content != "" {
		t.Errorf("got %+v, %v", resp, err)
	}
	back := requestToInternal(req)
	if len(back.Tools) != 1 || len(back.Messages) != 2 {
		t.Errorf("the request did not go back with its tools: %+v", back)
	}
}

func TestADecisionRequestThatCarriesToolsOrMessagesIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()
	decision := &llm.Decision{State: "s", Questions: []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "q"}}}
	for name, req := range map[string]llm.CompletionRequest{
		"tools":    {Decision: decision, Tools: []llm.Tool{{Name: "f"}}},
		"messages": {Decision: decision, Messages: []llm.Message{{Role: "user", Content: "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := requestFromInternal(req)
			if e := errorOf(t, err); e.Kind != schemaflux.ErrInvalidRequest {
				t.Errorf("got %v, want an invalid request", err)
			}
		})
	}
}

func TestADecisionSurvivesTheTripThroughTheInternalDecision(t *testing.T) {
	t.Parallel()
	var d schemaflux.Decision
	fill(reflect.ValueOf(&d).Elem(), new(int))
	internal := decisionToInternal(&d)
	if internal == nil || len(internal.Questions) != 2 {
		t.Fatalf("internal decision = %+v", internal)
	}
	for _, name := range zeroFields(reflect.ValueOf(internal.Questions[0])) {
		t.Errorf("a question with every field set gives the internal question an empty %s", name)
	}
	if back := decisionFromInternal(internal); !reflect.DeepEqual(back, &d) {
		t.Errorf("the decision did not come back as it went:\n%+v\n%+v", back, &d)
	}
	if decisionToInternal(nil) != nil || decisionFromInternal(nil) != nil {
		t.Error("a nil decision did not stay nil")
	}
	if got := decisionToInternal(&schemaflux.Decision{State: "s"}); got.Questions != nil {
		t.Errorf("a decision with no questions gained %v", got.Questions)
	}
}

func TestDecisionAnswersAndEscalationsSurviveTheTripThroughTheInternalOnes(t *testing.T) {
	t.Parallel()
	var answers []schemaflux.DecisionAnswer
	fill(reflect.ValueOf(&answers).Elem(), new(int))
	if back := answersFromInternal(answersToInternal(answers)); !reflect.DeepEqual(back, answers) {
		t.Errorf("answers came back as\n%+v\nwant\n%+v", back, answers)
	}
	var escalation *schemaflux.DecisionEscalation
	fill(reflect.ValueOf(&escalation).Elem(), new(int))
	internal := escalationToInternal(escalation)
	for _, name := range zeroFields(reflect.ValueOf(*internal)) {
		t.Errorf("an escalation with every field set gives the internal one an empty %s", name)
	}
	if back := escalationFromInternal(internal); !reflect.DeepEqual(back, escalation) {
		t.Errorf("the escalation came back as %+v, want %+v", back, escalation)
	}
	if answersToInternal(nil) != nil || answersFromInternal(nil) != nil || escalationToInternal(nil) != nil || escalationFromInternal(nil) != nil {
		t.Error("a nil did not stay nil")
	}
}

func TestTimingSurvivesTheTripThroughTheInternalTiming(t *testing.T) {
	t.Parallel()
	var timing schemaflux.Timing
	fill(reflect.ValueOf(&timing).Elem(), new(int))
	for _, name := range zeroFields(reflect.ValueOf(timingToInternal(timing))) {
		t.Errorf("a Timing with every field set gives the internal timing an empty %s", name)
	}
	if back := timingFromInternal(timingToInternal(timing)); back != timing {
		t.Errorf("the timing came back as %+v, want %+v", back, timing)
	}
}

func TestAdaptGivesEachProviderExactlyTheOptionalInterfacesItsInternalProviderHas(t *testing.T) {
	t.Parallel()
	inner := func(warm, stream, decide bool) llm.Provider {
		switch {
		case warm && stream && decide:
			return &internalAll{}
		case warm && stream:
			return &internalWarmStream{}
		case warm && decide:
			return &internalWarmDecide{}
		case stream && decide:
			return &internalStreamDecide{}
		case warm:
			return &internalWarm{}
		case stream:
			return &internalStream{}
		case decide:
			return &internalDecide{}
		}
		return &internalBare{}
	}
	for mask := 0; mask < 8; mask++ {
		warm, stream, decide := mask&1 != 0, mask&2 != 0, mask&4 != 0
		p := adapt(inner(warm, stream, decide))
		got := capabilitiesOf(p)
		want := capabilities{warmer: warm, streamer: stream, decider: decide, retrier: true}
		if got != want {
			t.Errorf("warm=%v stream=%v decide=%v: the adapted provider has %+v, want %+v", warm, stream, decide, got, want)
		}
		if p.Name() != "internal" {
			t.Errorf("Name() = %q", p.Name())
		}
		seam, ok := p.(interface{ InternalProvider() llm.Provider })
		if !ok || seam.InternalProvider() == nil {
			t.Errorf("the adapted provider does not offer the internal one")
		}
	}
}

// The internal providers of every combination.
type internalBare struct{}

func (internalBare) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: "internal answer", Provider: "internal"}, nil
}
func (internalBare) Name() string                               { return "internal" }
func (internalBare) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (internalBare) RetryPolicy() (int, time.Duration)          { return -1, 0 }

type (
	internalWarm         struct{ internalBare }
	internalStream       struct{ internalBare }
	internalDecide       struct{ internalBare }
	internalWarmStream   struct{ internalBare }
	internalWarmDecide   struct{ internalBare }
	internalStreamDecide struct{ internalBare }
	internalAll          struct{ internalBare }
)

func (internalWarm) Warm(context.Context) error       { return nil }
func (internalWarmStream) Warm(context.Context) error { return nil }
func (internalWarmDecide) Warm(context.Context) error { return nil }
func (internalAll) Warm(context.Context) error        { return nil }
func (internalStream) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return nil
}
func (internalWarmStream) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return nil
}
func (internalStreamDecide) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return nil
}
func (internalAll) CompleteStream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return nil
}
func (internalDecide) DecisionLimits() llm.DecisionLimits       { return llm.DecisionLimits{} }
func (internalWarmDecide) DecisionLimits() llm.DecisionLimits   { return llm.DecisionLimits{} }
func (internalStreamDecide) DecisionLimits() llm.DecisionLimits { return llm.DecisionLimits{} }
func (internalAll) DecisionLimits() llm.DecisionLimits          { return llm.DecisionLimits{} }

func TestTheRetryPolicyOfAnAdaptedProviderIsInTheConventionOfRetrier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		internalRetries int
		wantRetries     int
	}{
		{-1, 0}, {-5, 0}, {0, -1}, {1, 1}, {3, 3}, {10, 10},
	}
	for _, tt := range tests {
		got, _ := core{retrying{tt.internalRetries}}.RetryPolicy()
		if got != tt.wantRetries {
			t.Errorf("an internal %d is %d, want %d", tt.internalRetries, got, tt.wantRetries)
		}
		// And back: the foreign adapter of a Retrier gives the internal convention.
		back, _ := bind.ToInternal(retrierWith(tt.wantRetries)).RetryPolicy()
		want := tt.internalRetries
		if want < 0 {
			want = -1
		}
		if tt.wantRetries == 0 {
			want = -1
		}
		if back != want {
			t.Errorf("a Retrier answering %d is internal %d, want %d", tt.wantRetries, back, want)
		}
	}
	t.Run("a provider with no settings is not configured", func(t *testing.T) {
		t.Parallel()
		if got, _ := bind.ToInternal(&scripted{}).RetryPolicy(); got != -1 {
			t.Errorf("got %d, want -1", got)
		}
	})
	t.Run("the backoff is passed on", func(t *testing.T) {
		t.Parallel()
		if _, got := bind.ToInternal(&retryingScripted{retries: 2, backoff: 3 * time.Second}).RetryPolicy(); got != 3*time.Second {
			t.Errorf("got %v", got)
		}
	})
}

type retrying struct{ n int }

func (r retrying) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}
func (retrying) Name() string                               { return "r" }
func (retrying) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (r retrying) RetryPolicy() (int, time.Duration)        { return r.n, 0 }

type retryingScripted struct {
	scripted
	retries int
	backoff time.Duration
}

func (r *retryingScripted) RetryPolicy() (int, time.Duration) { return r.retries, r.backoff }

func retrierWith(n int) schemaflux.Provider { return &retryingScripted{retries: n} }

func TestAnAdaptedProviderCompletesThroughTheRootsTypes(t *testing.T) {
	t.Parallel()
	p := adapt(&internalBare{})
	resp, err := p.Complete(t.Context(), schemaflux.Request{Model: "m"})
	if err != nil || resp.Content != "internal answer" || resp.Provider != "internal" {
		t.Errorf("got %+v, %v", resp, err)
	}
}

func TestAProviderThatFailsInTheInternalPackageFailsAsTheRootsError(t *testing.T) {
	t.Parallel()
	failing := &failingInternal{err: &llm.APIError{Provider: "internal", StatusCode: 503, Message: testMarker}}
	_, err := adapt(failing).Complete(t.Context(), schemaflux.Request{})
	e := errorOf(t, err)
	if e.Kind != schemaflux.ErrUnavailable || e.Status != 503 || !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Errorf("got %+v", e)
	}
	if got := e.Detail(); !strings.Contains(got, testMarker) {
		t.Errorf("Detail() = %q, want the provider's words", got)
	}
}

type failingInternal struct {
	internalBare
	err error
}

func (f *failingInternal) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, f.err
}

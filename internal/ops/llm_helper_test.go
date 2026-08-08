package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
	"github.com/monstercameron/schemaflux/telemetry"
)

type captureProvider struct {
	req       llm.CompletionRequest
	resp      llm.CompletionResponse
	responses []llm.CompletionResponse
	errors    []error
	attempts  int
}

func (p *captureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.req = req
	p.attempts++
	if len(p.errors) > 0 {
		err := p.errors[0]
		p.errors = p.errors[1:]
		if err != nil {
			return llm.CompletionResponse{}, err
		}
	}
	if len(p.responses) > 0 {
		resp := p.responses[0]
		p.responses = p.responses[1:]
		if resp.Content == "" {
			resp.Content = `{"ok":true}`
		}
		return resp, nil
	}
	if p.resp.Content == "" {
		p.resp.Content = `{"ok":true}`
	}
	return p.resp, nil
}

func (p *captureProvider) Name() string {
	return "local"
}

func (p *captureProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return 0
}

func (p *captureProvider) RetryPolicy() (int, time.Duration) {
	return 2, time.Millisecond
}

func TestInferResponseFormat(t *testing.T) {
	tests := []struct {
		name       string
		system     string
		wantFormat string
	}{
		{
			name:       "json object contract",
			system:     "Return a JSON object with fields name and age.",
			wantFormat: "json",
		},
		{
			name:       "schema contract",
			system:     "Return ONLY valid JSON matching the schema.",
			wantFormat: "json",
		},
		{
			name:       "plain text summary",
			system:     "Summarize the text in two sentences.",
			wantFormat: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferResponseFormat(tt.system); got != tt.wantFormat {
				t.Fatalf("inferResponseFormat() = %q, want %q", got, tt.wantFormat)
			}
		})
	}
}

func TestCallLLMUsesStructuredContracts(t *testing.T) {
	provider := &captureProvider{}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a ranking expert. Return a JSON object with rankings.`,
		`Rank these items.`,
		types.OpOptions{Intelligence: types.Fast, Mode: types.TransformMode},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}

	if provider.req.ResponseFormat != "json" {
		t.Fatalf("ResponseFormat = %q, want json", provider.req.ResponseFormat)
	}
	if !strings.Contains(provider.req.SystemPrompt, "Perform the semantic task faithfully") {
		t.Fatalf("system prompt missing semantic grounding: %q", provider.req.SystemPrompt)
	}
	if !strings.Contains(provider.req.SystemPrompt, "return only the final JSON answer") {
		t.Fatalf("system prompt missing JSON grounding: %q", provider.req.SystemPrompt)
	}
}

func TestCallLLMLeavesTextOpsAsText(t *testing.T) {
	provider := &captureProvider{}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a text summarization expert. Create concise summaries that preserve key information.`,
		`Summarize this article.`,
		types.OpOptions{Intelligence: types.Fast, Mode: types.TransformMode},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}

	if provider.req.ResponseFormat != "text" {
		t.Fatalf("ResponseFormat = %q, want text", provider.req.ResponseFormat)
	}
	if strings.Contains(provider.req.SystemPrompt, "return only the final JSON answer") {
		t.Fatalf("text prompt unexpectedly forced JSON rules: %q", provider.req.SystemPrompt)
	}
}

func TestCallLLMAppliesSteeringToSystemPrompt(t *testing.T) {
	provider := &captureProvider{}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a filtering expert.`,
		`Filter these items.`,
		types.OpOptions{
			Intelligence: types.Fast,
			Mode:         types.TransformMode,
			Steering:     "Return only a JSON array of matching strings.",
		},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}

	if !strings.Contains(provider.req.SystemPrompt, "Additional instructions:") {
		t.Fatalf("system prompt missing steering section: %q", provider.req.SystemPrompt)
	}
	if !strings.Contains(provider.req.SystemPrompt, "Return only a JSON array of matching strings.") {
		t.Fatalf("system prompt missing steering content: %q", provider.req.SystemPrompt)
	}
}

func TestCallLLMTracksTokensAndCosts(t *testing.T) {
	telemetry.ResetMetrics()
	t.Cleanup(telemetry.ResetMetrics)
	pricing.ResetCostTracking()
	t.Cleanup(pricing.ResetCostTracking)
	requesttracking.Configure(requesttracking.Config{
		Enabled:               true,
		RequestIDStrategy:     requesttracking.IDStrategyUUID,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyInherit,
	})
	t.Cleanup(func() { requesttracking.Configure(requesttracking.DefaultConfig()) })
	t.Setenv("SCHEMAFLUX_METRICS", "")
	originalMetrics := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(originalMetrics) })
	config.SetMetricsEnabled(true)

	provider := &captureProvider{
		resp: llm.CompletionResponse{
			Content:  "ok",
			Model:    "gpt-5-mini-2025-08-07",
			Provider: "openai",
			Usage: types.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		},
	}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{
			Intelligence:  types.Fast,
			Mode:          types.TransformMode,
			RequestID:     "req-123",
			CorrelationID: "corr-789",
		},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}

	tokenSnapshot, ok := telemetry.GetMetricSnapshot("llm_tokens_total", map[string]string{
		"provider":     "openai",
		"model":        "gpt-5-mini-2025-08-07",
		"mode":         "transform",
		"intelligence": "fast",
	})
	if !ok {
		t.Fatal("expected llm_tokens_total metric to exist")
	}
	if tokenSnapshot.Sum != 150 {
		t.Fatalf("expected total tokens 150, got %v", tokenSnapshot.Sum)
	}

	costSnapshot, ok := telemetry.GetMetricSnapshot("llm_cost_total_usd", map[string]string{
		"provider":     "openai",
		"model":        "gpt-5-mini-2025-08-07",
		"mode":         "transform",
		"intelligence": "fast",
	})
	if !ok {
		t.Fatal("expected llm_cost_total_usd metric to exist")
	}
	if costSnapshot.Sum <= 0 {
		t.Fatalf("expected positive cost metric, got %v", costSnapshot.Sum)
	}

	record, ok := pricing.GetRequestCost("req-123")
	if !ok {
		t.Fatal("expected request cost record to exist")
	}
	if record.CorrelationID != "corr-789" {
		t.Fatalf("expected correlation id corr-789, got %q", record.CorrelationID)
	}
	if record.TokenUsage.TotalTokens != 150 {
		t.Fatalf("expected request total tokens 150, got %d", record.TokenUsage.TotalTokens)
	}

	summary := pricing.GetCostSummary(record.Timestamp.Add(-time.Second), map[string]string{"provider": "openai"})
	if summary.RequestCount != 1 {
		t.Fatalf("expected one tracked request, got %d", summary.RequestCount)
	}
	if summary.AverageTokensPerRequest != 150 {
		t.Fatalf("expected average tokens per request 150, got %v", summary.AverageTokensPerRequest)
	}
	if summary.AverageCostPerRequest <= 0 {
		t.Fatalf("expected positive average cost per request, got %v", summary.AverageCostPerRequest)
	}
}

func TestCallLLMInheritsTrackingFromContext(t *testing.T) {
	pricing.ResetCostTracking()
	t.Cleanup(pricing.ResetCostTracking)
	requesttracking.Configure(requesttracking.Config{
		Enabled:               true,
		RequestIDStrategy:     requesttracking.IDStrategyNone,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyInherit,
	})
	t.Cleanup(func() { requesttracking.Configure(requesttracking.DefaultConfig()) })

	provider := &captureProvider{
		resp: llm.CompletionResponse{
			Content:  "ok",
			Model:    "gpt-5-mini",
			Provider: "openai",
		},
	}

	ctx := requesttracking.WithMetadata(context.Background(), requesttracking.Metadata{
		RequestID:     "ctx-req",
		CorrelationID: "ctx-corr",
	})

	_, err := CallLLM(
		ctx,
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast, Mode: types.TransformMode},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}

	record, ok := pricing.GetRequestCost("ctx-req")
	if !ok {
		t.Fatal("expected inherited request cost record to exist")
	}
	if record.CorrelationID != "ctx-corr" {
		t.Fatalf("expected inherited correlation id ctx-corr, got %q", record.CorrelationID)
	}
}

func TestCallLLMRetriesTransientFailures(t *testing.T) {
	provider := &captureProvider{
		errors: []error{
			fmt.Errorf("rate limit exceeded: status 429"),
			nil,
		},
		resp: llm.CompletionResponse{
			Content: "ok",
			Model:   "gpt-5-mini",
		},
	}

	got, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast, Mode: types.TransformMode},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	if got != "ok" {
		t.Fatalf("expected success after retry, got %q", got)
	}
	if provider.attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", provider.attempts)
	}
}

func TestCallLLMDoesNotRetryNonRetryableFailures(t *testing.T) {
	provider := &captureProvider{
		errors: []error{fmt.Errorf("OpenAI API error (status 400): invalid request")},
	}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast, Mode: types.TransformMode},
	)
	if err == nil {
		t.Fatal("expected an error")
	}
	if provider.attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", provider.attempts)
	}
}

// ST-003. The tier ceiling used to be the only ceiling: config.GetMaxTokens
// was the sole input to CallLLM's request, so a caller who wanted a longer or
// shorter answer than their tier's default had no way to ask (I-09). These
// prove the per-call override actually reaches llm.CompletionRequest.MaxTokens
// rather than being a field that compiles and does nothing.
func TestCallLLMMaxOutputTokensOverrideReachesRequest(t *testing.T) {
	provider := &captureProvider{}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast, MaxOutputTokens: 777},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	if provider.req.MaxTokens != 777 {
		t.Fatalf("MaxTokens = %d, want the per-call override of 777", provider.req.MaxTokens)
	}
}

// An override on the Smart tier -- whose default (4000) is the largest -- must
// still win. Proves the option is not merely clamping a smaller number in.
func TestCallLLMMaxOutputTokensOverrideBeatsSmartTierDefault(t *testing.T) {
	provider := &captureProvider{}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Smart, MaxOutputTokens: 50},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	if provider.req.MaxTokens != 50 {
		t.Fatalf("MaxTokens = %d, want the per-call override of 50, not the Smart tier default", provider.req.MaxTokens)
	}
}

// A caller who never touches MaxOutputTokens must keep getting exactly the
// ceiling their tier always sent -- deleting the default outright would change
// every existing call's behaviour, which this task does not ask for.
func TestCallLLMMaxOutputTokensZeroUsesTierDefault(t *testing.T) {
	cases := []struct {
		name   string
		tier   types.Speed
		wantMT int
	}{
		{"smart", types.Smart, config.GetMaxTokens(types.Smart)},
		{"fast", types.Fast, config.GetMaxTokens(types.Fast)},
		{"quick", types.Quick, config.GetMaxTokens(types.Quick)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &captureProvider{}
			_, err := CallLLM(
				context.Background(),
				provider,
				`You are a concise assistant.`,
				`Summarize this text.`,
				types.OpOptions{Intelligence: tc.tier},
			)
			if err != nil {
				t.Fatalf("CallLLM() error = %v", err)
			}
			if provider.req.MaxTokens != tc.wantMT {
				t.Fatalf("MaxTokens = %d, want the %s tier default of %d", provider.req.MaxTokens, tc.name, tc.wantMT)
			}
		})
	}
}

// A negative override is not a caller opinion, it is a mistake -- the tier
// default must still apply rather than being sent to the provider verbatim.
func TestCallLLMMaxOutputTokensNegativeIgnored(t *testing.T) {
	provider := &captureProvider{}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast, MaxOutputTokens: -5},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	if want := config.GetMaxTokens(types.Fast); provider.req.MaxTokens != want {
		t.Fatalf("MaxTokens = %d, want the Fast tier default of %d for a negative override", provider.req.MaxTokens, want)
	}
}

// A truncated finish reason must classify as types.KindOutputTruncated and
// reach the caller via errors.Is(err, types.ErrOutputTruncated), not surface as
// whatever ParseJSON would have said about the cut-off body three layers away.
func TestCallLLMTruncatedFinishReasonProducesTruncationError(t *testing.T) {
	provider := &captureProvider{
		resp: llm.CompletionResponse{Content: `{"partial":`, FinishReason: "length"},
	}

	got, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast},
	)
	if err == nil {
		t.Fatalf("expected a truncation error, got success with content %q", got)
	}
	if !errors.Is(err, types.ErrOutputTruncated) {
		t.Errorf("errors.Is(err, types.ErrOutputTruncated) = false; err = %v", err)
	}
	if kind := types.KindOf(err); kind != types.KindOutputTruncated {
		t.Errorf("KindOf(err) = %v, want KindOutputTruncated", kind)
	}
	if got != "" {
		t.Errorf("expected no content on a truncated response, got %q", got)
	}
}

// max_tokens is OpenAI's other truncation finish reason (length is the other);
// both must classify identically.
func TestCallLLMMaxTokensFinishReasonProducesTruncationError(t *testing.T) {
	provider := &captureProvider{
		resp: llm.CompletionResponse{Content: `{"partial":`, FinishReason: "max_tokens"},
	}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast},
	)
	if !errors.Is(err, types.ErrOutputTruncated) {
		t.Errorf("errors.Is(err, types.ErrOutputTruncated) = false; err = %v", err)
	}
}

// Truncation is a property of the answer, not the transport: sending the
// identical request again asks the same question of the same model and gets
// cut off the same way, so it must not consume the retry budget the way a 500
// or a rate limit does.
func TestCallLLMTruncatedFinishReasonIsNotRetried(t *testing.T) {
	provider := &captureProvider{
		responses: []llm.CompletionResponse{
			{Content: `{"partial":`, FinishReason: "length"},
			{Content: `{"complete":true}`, FinishReason: "stop"},
		},
	}

	_, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast},
	)
	if err == nil {
		t.Fatal("expected the truncation error, not a retried success")
	}
	if !errors.Is(err, types.ErrOutputTruncated) {
		t.Errorf("errors.Is(err, types.ErrOutputTruncated) = false; err = %v", err)
	}
	if provider.attempts != 1 {
		t.Fatalf("attempts = %d, want 1 -- a truncation must not be retried", provider.attempts)
	}
}

// A normal "stop" finish with a complete body must be entirely unaffected by
// the truncation check -- the classifier must not fire on the common case.
func TestCallLLMNormalStopFinishIsUnaffected(t *testing.T) {
	provider := &captureProvider{
		resp: llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop"},
	}

	got, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("got = %q, want the untouched content", got)
	}
}

func TestCallLLMRetriesEmptyContent(t *testing.T) {
	provider := &captureProvider{
		responses: []llm.CompletionResponse{
			{Content: "   "},
			{Content: "usable response"},
		},
	}

	got, err := CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast, Mode: types.TransformMode},
	)
	if err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}
	if got != "usable response" {
		t.Fatalf("expected usable response after retry, got %q", got)
	}
	if provider.attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", provider.attempts)
	}
}

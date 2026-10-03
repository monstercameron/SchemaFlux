package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// An application could see what Extract, Sort and the text operations cost and
// how long they waited, and could not see it for RedactLLM, Generate or Audit.
// The three Result forms close that. These tests go through the exported API
// with a provider injected on a client, so the whole stack is covered, and the
// provider reports usage so the envelope can be held to figures the test chose.

// dx12Provider answers with one body and a fixed usage.
type dx12Provider struct {
	mu   sync.Mutex
	body string
	err  error
	seen []schemaflux.CompletionRequest
}

func (p *dx12Provider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	p.seen = append(p.seen, req)
	p.mu.Unlock()
	if p.err != nil {
		return schemaflux.CompletionResponse{}, p.err
	}
	return schemaflux.CompletionResponse{
		Content:      p.body,
		Model:        req.Model,
		FinishReason: "stop",
		Usage:        schemaflux.TokenUsage{PromptTokens: 30, CompletionTokens: 12, TotalTokens: 42},
	}, nil
}

func (p *dx12Provider) Name() string                                      { return "local" }
func (p *dx12Provider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *dx12Provider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func (p *dx12Provider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

func dx12Install(body string, err error) *dx12Provider {
	provider := &dx12Provider{body: body, err: err}
	schemaflux.NewClient("test-key").WithProviderInstance(provider)
	return provider
}

type dx12Note struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type dx12Customer struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func TestIntegrationGenerateResultReportsWhatTheCallCost(t *testing.T) {
	provider := dx12Install(`{"title":"Standup","body":"Ship it."}`, nil)

	opts := schemaflux.NewGenerateOptions()
	opts.OpOptions.Context = schemaflux.WithCorrelationID(schemaflux.WithRequestID(context.Background(), "req-gen"), "corr-gen")

	result, err := schemaflux.GenerateResult[dx12Note]("a note about standup", opts)
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}

	if result.Value.Title != "Standup" || result.Value.Body != "Ship it." {
		t.Errorf("Value = %+v", result.Value)
	}
	meta := result.Meta
	if meta.Operation != "generate" || meta.Attempts != 1 || provider.calls() != 1 {
		t.Errorf("Operation %q, Attempts %d, provider calls %d", meta.Operation, meta.Attempts, provider.calls())
	}
	if meta.Usage.PromptTokens != 30 || meta.Usage.CompletionTokens != 12 || meta.Usage.TotalTokens != 42 {
		t.Errorf("Usage = %+v, want what the provider reported", meta.Usage)
	}
	if meta.RequestID != "req-gen" || meta.CorrelationID != "corr-gen" {
		t.Errorf("ids = %q, %q, want the caller's", meta.RequestID, meta.CorrelationID)
	}
	if meta.DeliveredContract != schemaflux.ContractSchemaConstrained {
		t.Errorf("DeliveredContract = %v", meta.DeliveredContract)
	}
	if meta.Elapsed < 0 || meta.Provider != "local" {
		t.Errorf("Elapsed %v, Provider %q", meta.Elapsed, meta.Provider)
	}
}

func TestIntegrationGenerateResultNamesItsIdsWhenTheCallIsRefusedBeforeAnyRequest(t *testing.T) {
	provider := dx12Install(`{"title":"x","body":"y"}`, nil)

	opts := schemaflux.NewGenerateOptions()
	opts.OpOptions.Context = schemaflux.WithRequestID(context.Background(), "req-refused")

	result, err := schemaflux.GenerateResult[dx12Note]("   ", opts)
	if err == nil {
		t.Fatal("an empty prompt was accepted")
	}
	if provider.calls() != 0 {
		t.Errorf("the provider was called %d times for a refused prompt", provider.calls())
	}
	if result.Value != (dx12Note{}) {
		t.Errorf("a value came back beside the error: %+v", result.Value)
	}
	if result.Meta.RequestID != "req-refused" || result.Meta.CorrelationID == "" {
		t.Errorf("ids on the refusal = %q, %q", result.Meta.RequestID, result.Meta.CorrelationID)
	}
}

func TestIntegrationGenerateResultSendsWhatGenerateSends(t *testing.T) {
	plain := dx12Install(`{"title":"Standup","body":"Ship it."}`, nil)
	if _, err := schemaflux.Generate[dx12Note]("a note", schemaflux.NewGenerateOptions().WithStyle("terse")); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	detailed := dx12Install(`{"title":"Standup","body":"Ship it."}`, nil)
	if _, err := schemaflux.GenerateResult[dx12Note]("a note", schemaflux.NewGenerateOptions().WithStyle("terse")); err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}

	if plain.calls() != 1 || detailed.calls() != 1 {
		t.Fatalf("call counts differ: %d and %d", plain.calls(), detailed.calls())
	}
	if plain.seen[0].SystemPrompt != detailed.seen[0].SystemPrompt || plain.seen[0].UserPrompt != detailed.seen[0].UserPrompt {
		t.Error("the two forms send different prompts")
	}
}

func TestIntegrationGenerateResultReportsAProviderFailure(t *testing.T) {
	dx12Install("", errors.New("provider unavailable"))

	result, err := schemaflux.GenerateResult[dx12Note]("a note", schemaflux.NewGenerateOptions())
	if err == nil {
		t.Fatal("a failing provider reported success")
	}
	if result.Value != (dx12Note{}) || result.Meta.Attempts != 1 || result.Meta.RequestID == "" {
		t.Errorf("result = %+v", result)
	}
}

func TestIntegrationRedactLLMWithMetaReportsWhatTheCallCost(t *testing.T) {
	provider := dx12Install(`{"spans":[{"text":"zoe@example.com","category":"email"}]}`, nil)

	opts := schemaflux.NewRedactLLMOptions()
	opts.RequestID, opts.CorrelationID = "req-redact", "corr-redact"

	result, err := schemaflux.RedactLLMWithMeta("Write to zoe@example.com today.", opts)
	if err != nil {
		t.Fatalf("RedactLLMWithMeta: %v", err)
	}

	if strings.Contains(result.Value.Text, "zoe@example.com") || len(result.Value.Spans) != 1 {
		t.Errorf("redaction = %+v", result.Value)
	}
	meta := result.Meta
	if meta.Operation != "redact_llm" || meta.Attempts != 1 || provider.calls() != 1 {
		t.Errorf("Operation %q, Attempts %d, provider calls %d", meta.Operation, meta.Attempts, provider.calls())
	}
	if meta.Usage.TotalTokens != 42 {
		t.Errorf("Usage = %+v, want what the provider reported", meta.Usage)
	}
	if meta.RequestID != "req-redact" || meta.CorrelationID != "corr-redact" {
		t.Errorf("ids = %q, %q, want the caller's", meta.RequestID, meta.CorrelationID)
	}
	if meta.DeliveredContract != schemaflux.ContractSchemaAndInvariantChecked {
		t.Errorf("DeliveredContract = %v", meta.DeliveredContract)
	}
}

func TestIntegrationRedactLLMWithMetaRefusalCarriesNoTextAndKeepsItsIds(t *testing.T) {
	dx12Install(`{"spans":[{"text":"someone@else.org","category":"email"}]}`, nil)

	opts := schemaflux.NewRedactLLMOptions()
	opts.RequestID = "req-refused"

	result, err := schemaflux.RedactLLMWithMeta("Write to zoe@example.com today.", opts)
	if err == nil {
		t.Fatal("a span that is not in the text was accepted")
	}
	if result.Value.Text != "" || result.Value.Original != "" {
		t.Errorf("text came back beside an error: %+v", result.Value)
	}
	if result.Meta.RequestID != "req-refused" || result.Meta.Attempts != 1 {
		t.Errorf("Meta = %+v; the call was made and paid for", result.Meta)
	}
}

func TestIntegrationAuditWithMetaReportsWhatTheCallCost(t *testing.T) {
	provider := dx12Install(`{"policies":{"p1":[{"category":"compliance","severity":0.8,"field":"email","issue":"off domain","evidence":null,"recommendation":null}]},"findings":[]}`, nil)

	opts := schemaflux.AuditOptions{
		Policies:      []string{"Email addresses must be on the company domain"},
		RequestID:     "req-audit",
		CorrelationID: "corr-audit",
	}
	result, err := schemaflux.AuditWithMeta(dx12Customer{ID: "1", Email: "sam@elsewhere.org"}, opts)
	if err != nil {
		t.Fatalf("AuditWithMeta: %v", err)
	}

	if result.Value.Verdict != schemaflux.VerdictFail || len(result.Value.Issues) != 1 {
		t.Errorf("judgment = %+v", result.Value)
	}
	meta := result.Meta
	if meta.Operation != "audit" || meta.Attempts != 1 || provider.calls() != 1 {
		t.Errorf("Operation %q, Attempts %d, provider calls %d", meta.Operation, meta.Attempts, provider.calls())
	}
	if meta.Usage.TotalTokens != 42 {
		t.Errorf("Usage = %+v, want what the provider reported", meta.Usage)
	}
	if meta.RequestID != "req-audit" || meta.CorrelationID != "corr-audit" {
		t.Errorf("ids = %q, %q, want the caller's", meta.RequestID, meta.CorrelationID)
	}
}

func TestIntegrationAuditWithMetaRefusalBeforeAnyCallKeepsItsIds(t *testing.T) {
	provider := dx12Install(`{"findings":[]}`, nil)

	result, err := schemaflux.AuditWithMeta(dx12Customer{ID: "1"}, schemaflux.AuditOptions{Threshold: 2, RequestID: "req-bad"})
	if err == nil {
		t.Fatal("a threshold of 2 was accepted")
	}
	if provider.calls() != 0 {
		t.Errorf("the provider was called %d times", provider.calls())
	}
	if result.Meta.RequestID != "req-bad" || result.Meta.CorrelationID == "" {
		t.Errorf("ids on the refusal = %q, %q", result.Meta.RequestID, result.Meta.CorrelationID)
	}
	if result.Value.Verdict != "" || len(result.Value.Issues) != 0 {
		t.Errorf("a judgment came back beside an error: %+v", result.Value)
	}
}

func TestIntegrationAuditWithMetaAgreesWithAuditWithModel(t *testing.T) {
	body := `{"findings":[{"category":"quality","severity":0.6,"field":"id","issue":"odd","evidence":null,"recommendation":null}]}`
	subject := dx12Customer{ID: "1", Email: "sam@example.com"}

	dx12Install(body, nil)
	plain, err := schemaflux.AuditWithModel(subject)
	if err != nil {
		t.Fatalf("AuditWithModel: %v", err)
	}
	dx12Install(body, nil)
	detailed, err := schemaflux.AuditWithMeta(subject)
	if err != nil {
		t.Fatalf("AuditWithMeta: %v", err)
	}

	if plain.Verdict != detailed.Value.Verdict || len(plain.Issues) != len(detailed.Value.Issues) {
		t.Errorf("the two forms judged differently: %+v and %+v", plain, detailed.Value)
	}
}

// Example_generateResult shows what the Result form adds to Generate: the value,
// and the record of what producing it took.
func Example_generateResult() {
	schemaflux.NewClient("example-key").WithProviderInstance(&dx12Provider{body: `{"title":"Standup","body":"Ship it."}`})

	result, err := schemaflux.GenerateResult[dx12Note]("a note about standup", schemaflux.NewGenerateOptions())
	if err != nil {
		fmt.Println("nothing was generated:", err)
		return
	}

	fmt.Println(result.Value.Title)
	fmt.Println("attempts:", result.Meta.Attempts)
	fmt.Println("tokens:", result.Meta.Usage.TotalTokens)
	fmt.Println("contract:", result.Meta.DeliveredContract)

	// Output:
	// Standup
	// attempts: 1
	// tokens: 42
	// contract: schema constrained
}

// Example_redactLLMWithMeta shows the redaction and the record beside it.
func Example_redactLLMWithMeta() {
	schemaflux.NewClient("example-key").WithProviderInstance(&dx12Provider{body: `{"spans":[{"text":"zoe@example.com","category":"email"}]}`})

	result, err := schemaflux.RedactLLMWithMeta("Write to zoe@example.com today.", schemaflux.NewRedactLLMOptions())
	if err != nil {
		fmt.Println("nothing was redacted:", err)
		return
	}

	fmt.Println(result.Value.Text)
	fmt.Println("attempts:", result.Meta.Attempts)
	fmt.Println("tokens:", result.Meta.Usage.TotalTokens)

	// Output:
	// Write to *************** today.
	// attempts: 1
	// tokens: 42
}

// Example_auditWithMeta shows the judgment and the record beside it.
func Example_auditWithMeta() {
	schemaflux.NewClient("example-key").WithProviderInstance(&dx12Provider{body: `{"policies":{"p1":[]},"findings":[]}`})

	result, err := schemaflux.AuditWithMeta(dx12Customer{ID: "1", Email: "sam@example.com"},
		schemaflux.AuditOptions{Policies: []string{"Email addresses must be on the company domain"}})
	if err != nil {
		fmt.Println("no verdict:", err)
		return
	}

	fmt.Println("verdict:", result.Value.Verdict)
	fmt.Println("attempts:", result.Meta.Attempts)
	fmt.Println("tokens:", result.Meta.Usage.TotalTokens)

	// Output:
	// verdict: pass
	// attempts: 1
	// tokens: 42
}

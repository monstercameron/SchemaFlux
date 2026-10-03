package ops

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RedactLLM, Generate and Audit returned `(T, error)` and the record of the
// call, what it cost and how long it waited, was thrown away inside callLLM, so
// an application could see none of it. Their Result forms carry it out the way
// ExtractResult does. The tests here state the figures the provider reported
// and check that the envelope reports them, and check that every path through
// every form, including the ones that never reach a provider, names the
// request and correlation ids the call was made under.

// dx12Provider answers with scripted bodies and reports a fixed usage, so an
// envelope can be compared against figures the test chose.
type dx12Provider struct {
	mu     sync.Mutex
	bodies []string
	err    error
	reqs   []llm.CompletionRequest
}

const (
	dx12PromptTokens     = 11
	dx12CompletionTokens = 7
	dx12TotalTokens      = dx12PromptTokens + dx12CompletionTokens
)

func (p *dx12Provider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return llm.CompletionResponse{}, p.err
	}
	index := len(p.reqs) - 1
	if index >= len(p.bodies) {
		index = len(p.bodies) - 1
	}
	return llm.CompletionResponse{
		Content:      p.bodies[index],
		Model:        req.Model,
		FinishReason: "stop",
		Usage: types.TokenUsage{
			PromptTokens:     dx12PromptTokens,
			CompletionTokens: dx12CompletionTokens,
			TotalTokens:      dx12TotalTokens,
		},
	}, nil
}

func (p *dx12Provider) Name() string                               { return "local" }
func (p *dx12Provider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *dx12Provider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func (p *dx12Provider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// dx12Case is one row of a Result form's table. run makes the call under the
// provider and reports the envelope, whether the value came back empty, and the
// error.
type dx12Case struct {
	name string

	provider *dx12Provider

	// What the caller supplied, on the options or on the context.
	requestID, correlationID       string
	ctxRequestID, ctxCorrelationID string
	policy                         *types.DataPolicy

	run func(ctx context.Context, requestID, correlationID string) (types.Meta, bool, error)

	wantErr   bool
	wantCalls int
	// wantContract is the level a success delivers, and the level an error was
	// asking for.
	wantContract types.ContractLevel
	wantOp       string
}

// dx12Check runs a case and holds the envelope to what the provider did.
func dx12Check(t *testing.T, tc dx12Case) {
	t.Helper()

	ctx := WithProvider(context.Background(), tc.provider)
	if tc.ctxRequestID != "" || tc.ctxCorrelationID != "" {
		ctx = requesttracking.WithMetadata(ctx, requesttracking.Metadata{
			RequestID: tc.ctxRequestID, CorrelationID: tc.ctxCorrelationID,
		})
	}
	if tc.policy != nil {
		ctx = WithContractPolicy(ctx, *tc.policy)
	}

	meta, empty, err := tc.run(ctx, tc.requestID, tc.correlationID)

	if (err != nil) != tc.wantErr {
		t.Fatalf("error = %v, want an error: %v", err, tc.wantErr)
	}
	if got := tc.provider.calls(); got != tc.wantCalls {
		t.Fatalf("the provider was called %d times, want %d", got, tc.wantCalls)
	}
	if tc.wantErr && !empty {
		t.Error("a value came back beside an error")
	}
	if meta.Operation != tc.wantOp {
		t.Errorf("Operation = %q, want %q", meta.Operation, tc.wantOp)
	}

	// The ids, on every path. Supplied ones are reported as supplied, and
	// otherwise the generated ones are reported, so a caller can correlate a
	// refusal that never reached a provider as well as an answer.
	wantRequest, wantCorrelation := tc.requestID, tc.correlationID
	if wantRequest == "" {
		wantRequest = tc.ctxRequestID
	}
	if wantCorrelation == "" {
		wantCorrelation = tc.ctxCorrelationID
	}
	switch {
	case wantRequest != "" && meta.RequestID != wantRequest:
		t.Errorf("RequestID = %q, want the caller's %q", meta.RequestID, wantRequest)
	case wantRequest == "" && meta.RequestID == "":
		t.Error("RequestID is empty, so this call cannot be correlated with anything")
	}
	switch {
	case wantCorrelation != "" && meta.CorrelationID != wantCorrelation:
		t.Errorf("CorrelationID = %q, want the caller's %q", meta.CorrelationID, wantCorrelation)
	case wantCorrelation == "" && meta.CorrelationID == "":
		t.Error("CorrelationID is empty")
	}

	if meta.RequestedContract != tc.wantContract {
		t.Errorf("RequestedContract = %v, want %v", meta.RequestedContract, tc.wantContract)
	}
	wantDelivered := tc.wantContract
	if tc.wantErr {
		wantDelivered = types.ContractPromptOnly
	}
	if meta.DeliveredContract != wantDelivered {
		t.Errorf("DeliveredContract = %v, want %v", meta.DeliveredContract, wantDelivered)
	}

	// What was spent is reported for a failure as well as a success: the calls
	// were made and billed.
	if meta.Attempts != tc.wantCalls {
		t.Errorf("Attempts = %d, want the %d the provider saw", meta.Attempts, tc.wantCalls)
	}
	if want := dx12TotalTokens * tc.wantCalls; tc.provider.err == nil && meta.Usage.TotalTokens != want {
		t.Errorf("Usage.TotalTokens = %d, want the %d the provider reported", meta.Usage.TotalTokens, want)
	}
	if tc.wantCalls > 0 && meta.Provider != "local" {
		t.Errorf("Provider = %q", meta.Provider)
	}
	if meta.Elapsed < 0 {
		t.Errorf("Elapsed = %v", meta.Elapsed)
	}
}

func dx12Policy(level types.ContractLevel) *types.DataPolicy {
	return &types.DataPolicy{MinimumContract: level}
}

// ---------------------------------------------------------------------------
// RedactLLMWithMeta
// ---------------------------------------------------------------------------

const (
	dx12RedactText   = "Write to zoe@example.com today."
	dx12RedactAnswer = `{"spans":[{"text":"zoe@example.com","category":"email"}]}`
)

func dx12RedactCase(name string, provider *dx12Provider, opts RedactLLMOptions, text string) dx12Case {
	return dx12Case{
		name:         name,
		provider:     provider,
		wantOp:       "redact_llm",
		wantContract: redactLLMResultContract,
		run: func(ctx context.Context, requestID, correlationID string) (types.Meta, bool, error) {
			opts.RequestID, opts.CorrelationID = requestID, correlationID
			result, err := RedactLLMWithMeta(ctx, text, opts)
			return result.Meta, reflect.DeepEqual(result.Value, RedactLLMResult{}), err
		},
	}
}

func TestRedactLLMWithMetaCarriesTheRecordOnEveryPath(t *testing.T) {
	good := func() *dx12Provider { return &dx12Provider{bodies: []string{dx12RedactAnswer}} }
	opts := NewRedactLLMOptions()

	cases := []dx12Case{
		func() dx12Case {
			tc := dx12RedactCase("an answer carries the usage the provider reported", good(), opts, dx12RedactText)
			tc.wantCalls = 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("the caller's request and correlation ids are reported", good(), opts, dx12RedactText)
			tc.requestID, tc.correlationID, tc.wantCalls = "req-redact", "corr-redact", 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("ids on the context are reported", good(), opts, dx12RedactText)
			tc.ctxRequestID, tc.ctxCorrelationID, tc.wantCalls = "req-ctx", "corr-ctx", 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("an id on the options wins over one on the context", good(), opts, dx12RedactText)
			tc.requestID, tc.ctxRequestID, tc.ctxCorrelationID, tc.wantCalls = "req-opt", "req-ctx", "corr-ctx", 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("blank text asks nothing and still names its ids", &dx12Provider{bodies: []string{dx12RedactAnswer}}, opts, "  \n")
			tc.requestID, tc.correlationID = "req-blank", "corr-blank"
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("invalid options are refused before any call, with the ids", good(), NewRedactLLMOptions().WithCategories(nil), dx12RedactText)
			tc.requestID, tc.correlationID, tc.wantErr = "req-invalid", "corr-invalid", true
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("a blank category is refused before any call", good(), NewRedactLLMOptions().WithCategories([]string{" "}), dx12RedactText)
			tc.wantErr = true
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("a provider failure is an error with the attempt recorded", &dx12Provider{err: errors.New("provider unavailable")}, opts, dx12RedactText)
			tc.requestID, tc.wantErr, tc.wantCalls = "req-down", true, 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("a span that is not in the text is refused and the call is still reported",
				&dx12Provider{bodies: []string{`{"spans":[{"text":"someone@else.org","category":"email"}]}`}}, opts, dx12RedactText)
			tc.wantErr, tc.wantCalls = true, 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("a span outside the requested categories is refused and the call is still reported",
				&dx12Provider{bodies: []string{`{"spans":[{"text":"zoe@example.com","category":"phone"}]}`}},
				NewRedactLLMOptions().WithCategories([]string{"email"}), dx12RedactText)
			tc.wantErr, tc.wantCalls = true, 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("a floor the call cannot meet is refused before anything is sent, with the ids", good(), opts, dx12RedactText)
			tc.policy = dx12Policy(types.ContractEvidenceChecked)
			tc.requestID, tc.correlationID, tc.wantErr = "req-floor", "corr-floor", true
			return tc
		}(),
		func() dx12Case {
			tc := dx12RedactCase("a floor the call meets is delivered", good(), opts, dx12RedactText)
			tc.policy = dx12Policy(types.ContractSchemaAndInvariantChecked)
			tc.wantCalls = 1
			return tc
		}(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { dx12Check(t, tc) })
	}
}

func TestRedactLLMWithMetaReturnsTheSameRedactionAsRedactLLM(t *testing.T) {
	plainProvider := &dx12Provider{bodies: []string{dx12RedactAnswer}}
	plain, err := RedactLLM(WithProvider(context.Background(), plainProvider), dx12RedactText, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("RedactLLM: %v", err)
	}

	metaProvider := &dx12Provider{bodies: []string{dx12RedactAnswer}}
	result, err := RedactLLMWithMeta(WithProvider(context.Background(), metaProvider), dx12RedactText, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("RedactLLMWithMeta: %v", err)
	}

	if !reflect.DeepEqual(plain, result.Value) {
		t.Errorf("the two forms redacted differently:\n plain %+v\n meta  %+v", plain, result.Value)
	}
	dx12SameRequest(t, plainProvider, metaProvider)
}

func TestRedactLLMWithMetaTakesTheContextFromTheOptionsWhenGivenNone(t *testing.T) {
	provider := &dx12Provider{bodies: []string{dx12RedactAnswer}}
	opts := NewRedactLLMOptions()
	opts.Context = WithProvider(context.Background(), provider)

	result, err := RedactLLMWithMeta(nil, dx12RedactText, opts) //nolint:staticcheck // a nil context is the case under test
	if err != nil {
		t.Fatalf("RedactLLMWithMeta: %v", err)
	}
	if provider.calls() != 1 || result.Meta.Attempts != 1 {
		t.Errorf("provider saw %d calls and the envelope reports %d attempts", provider.calls(), result.Meta.Attempts)
	}
	if result.Value.Text == dx12RedactText {
		t.Errorf("nothing was redacted: %q", result.Value.Text)
	}
}

// dx12SameRequest holds two providers to having been sent the same request by
// the plain form and the Result form of one operation.
func dx12SameRequest(t *testing.T, plain, meta *dx12Provider) {
	t.Helper()
	if plain.calls() != 1 || meta.calls() != 1 {
		t.Fatalf("call counts differ: plain %d, meta %d", plain.calls(), meta.calls())
	}
	a, b := plain.reqs[0], meta.reqs[0]
	if a.SystemPrompt != b.SystemPrompt || a.UserPrompt != b.UserPrompt {
		t.Error("the two forms send different prompts")
	}
	if a.Model != b.Model || a.Temperature != b.Temperature || a.ResponseFormat != b.ResponseFormat || a.SchemaName != b.SchemaName {
		t.Errorf("the two forms send different parameters: %+v and %+v", a, b)
	}
	if !reflect.DeepEqual(a.JSONSchema, b.JSONSchema) {
		t.Error("the two forms declare different schemas")
	}
}

// ---------------------------------------------------------------------------
// GenerateResult
// ---------------------------------------------------------------------------

type dx12Note struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

const dx12NoteAnswer = `{"title":"Standup","body":"Ship it."}`

func dx12GenerateCase[T any](name string, provider *dx12Provider, opts GenerateOptions, prompt string, contract types.ContractLevel) dx12Case {
	return dx12Case{
		name:         name,
		provider:     provider,
		wantOp:       "generate",
		wantContract: contract,
		run: func(ctx context.Context, requestID, correlationID string) (types.Meta, bool, error) {
			opts.CommonOptions.RequestID, opts.CommonOptions.CorrelationID = requestID, correlationID
			opts.OpOptions.Context = ctx
			result, err := GenerateResult[T](prompt, opts)
			return result.Meta, reflect.ValueOf(result.Value).IsZero(), err
		},
	}
}

func TestGenerateResultCarriesTheRecordOnEveryPath(t *testing.T) {
	note := func() *dx12Provider { return &dx12Provider{bodies: []string{dx12NoteAnswer}} }
	opts := NewGenerateOptions()
	structured, text := types.ContractSchemaConstrained, types.ContractPromptOnly

	cases := []dx12Case{
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("a structured answer carries the usage the provider reported", note(), opts, "a note", structured)
			tc.wantCalls = 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("the caller's request and correlation ids are reported", note(), opts, "a note", structured)
			tc.requestID, tc.correlationID, tc.wantCalls = "req-gen", "corr-gen", 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("ids on the context are reported", note(), opts, "a note", structured)
			tc.ctxRequestID, tc.ctxCorrelationID, tc.wantCalls = "req-ctx", "corr-ctx", 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[string]("a string is the model's text, and nothing was enforced", &dx12Provider{bodies: []string{"hello there"}}, opts, "a greeting", text)
			tc.wantCalls = 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("an empty prompt is refused before any call, with the ids", note(), opts, "   ", structured)
			tc.requestID, tc.correlationID, tc.wantErr = "req-empty", "corr-empty", true
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("a count below one is refused before any call", note(), NewGenerateOptions().WithCount(0), "a note", structured)
			tc.wantErr = true
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("a batch is refused before any call", note(), NewGenerateOptions().WithCount(3), "a note", structured)
			tc.wantErr = true
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("a provider failure is an error with the attempt recorded", &dx12Provider{err: errors.New("provider unavailable")}, opts, "a note", structured)
			tc.wantErr, tc.wantCalls = true, 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("an answer that is not the type is an error and the call is still reported",
				&dx12Provider{bodies: []string{`["not","a","note"]`}}, opts, "a note", structured)
			tc.wantErr, tc.wantCalls = true, 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("a floor above a structured answer is refused before anything is sent", note(), opts, "a note", structured)
			tc.policy = dx12Policy(types.ContractSchemaAndInvariantChecked)
			tc.requestID, tc.wantErr = "req-floor", true
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[string]("a floor above a string answer is refused, because a string is not checked at all",
				&dx12Provider{bodies: []string{"hello"}}, opts, "a greeting", text)
			tc.policy = dx12Policy(types.ContractJSONWellFormed)
			tc.wantErr = true
			return tc
		}(),
		func() dx12Case {
			tc := dx12GenerateCase[dx12Note]("a floor the structured answer meets is delivered", note(), opts, "a note", structured)
			tc.policy = dx12Policy(types.ContractSchemaConstrained)
			tc.wantCalls = 1
			return tc
		}(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { dx12Check(t, tc) })
	}
}

func TestGenerateResultReturnsTheSameValueAsGenerate(t *testing.T) {
	plainProvider := &dx12Provider{bodies: []string{dx12NoteAnswer}}
	plainOpts := NewGenerateOptions().WithStyle("terse")
	plainOpts.OpOptions.Context = WithProvider(context.Background(), plainProvider)
	plain, err := Generate[dx12Note]("a note", plainOpts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	metaProvider := &dx12Provider{bodies: []string{dx12NoteAnswer}}
	metaOpts := NewGenerateOptions().WithStyle("terse")
	metaOpts.OpOptions.Context = WithProvider(context.Background(), metaProvider)
	result, err := GenerateResult[dx12Note]("a note", metaOpts)
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}

	if plain != result.Value {
		t.Errorf("the two forms generated differently: %+v and %+v", plain, result.Value)
	}
	dx12SameRequest(t, plainProvider, metaProvider)
}

func TestGenerateResultReadsAContextOnEitherHalfOfTheOptions(t *testing.T) {
	for _, half := range []string{"CommonOptions", "OpOptions"} {
		t.Run(half, func(t *testing.T) {
			provider := &dx12Provider{bodies: []string{dx12NoteAnswer}}
			opts := NewGenerateOptions()
			ctx := WithProvider(context.Background(), provider)
			if half == "CommonOptions" {
				opts.CommonOptions.Context = ctx
			} else {
				opts.OpOptions.Context = ctx
			}

			result, err := GenerateResult[dx12Note]("a note", opts)
			if err != nil {
				t.Fatalf("GenerateResult: %v", err)
			}
			if result.Meta.Attempts != 1 || provider.calls() != 1 {
				t.Errorf("the call on the %s context was not recorded: attempts %d, provider calls %d",
					half, result.Meta.Attempts, provider.calls())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AuditWithMeta
// ---------------------------------------------------------------------------

type dx12Record struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

const (
	dx12AuditPolicy = "Email addresses must be on the company domain"
	dx12AuditClean  = `{"policies":{"p1":[]},"findings":[]}`
	dx12AuditFound  = `{"policies":{"p1":[{"category":"compliance","severity":0.8,"field":"email","issue":"off domain","evidence":null,"recommendation":null}]},"findings":[]}`
)

func dx12AuditCase(name string, provider *dx12Provider, opts AuditOptions) dx12Case {
	return dx12Case{
		name:         name,
		provider:     provider,
		wantOp:       "audit",
		wantContract: auditResultContract,
		run: func(ctx context.Context, requestID, correlationID string) (types.Meta, bool, error) {
			opts.Context = ctx
			opts.RequestID, opts.CorrelationID = requestID, correlationID
			result, err := AuditWithMeta(dx12Record{ID: "1", Email: "sam@elsewhere.org"}, opts)
			return result.Meta, reflect.DeepEqual(result.Value, types.JudgmentResult[dx12Record]{}), err
		},
	}
}

func TestAuditWithMetaCarriesTheRecordOnEveryPath(t *testing.T) {
	policy := AuditOptions{Policies: []string{dx12AuditPolicy}}

	cases := []dx12Case{
		func() dx12Case {
			tc := dx12AuditCase("a passing audit carries the usage the provider reported", &dx12Provider{bodies: []string{dx12AuditClean}}, policy)
			tc.wantCalls = 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("a failing audit is a result, not an error, and carries the record", &dx12Provider{bodies: []string{dx12AuditFound}}, policy)
			tc.wantCalls = 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("the caller's request and correlation ids are reported", &dx12Provider{bodies: []string{dx12AuditClean}}, policy)
			tc.requestID, tc.correlationID, tc.wantCalls = "req-audit", "corr-audit", 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("ids on the context are reported", &dx12Provider{bodies: []string{dx12AuditClean}}, policy)
			tc.ctxRequestID, tc.ctxCorrelationID, tc.wantCalls = "req-ctx", "corr-ctx", 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("a threshold outside zero to one is refused before any call, with the ids", &dx12Provider{bodies: []string{dx12AuditClean}},
				AuditOptions{Policies: []string{dx12AuditPolicy}, Threshold: 1.5})
			tc.requestID, tc.correlationID, tc.wantErr = "req-threshold", "corr-threshold", true
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("a negative threshold is refused before any call", &dx12Provider{bodies: []string{dx12AuditClean}},
				AuditOptions{Threshold: -0.1})
			tc.wantErr = true
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("Deep and Shallow together are refused before any call", &dx12Provider{bodies: []string{dx12AuditClean}},
				AuditOptions{Deep: true, Shallow: true})
			tc.wantErr = true
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("a provider failure is an error with the attempt recorded", &dx12Provider{err: errors.New("provider unavailable")}, policy)
			tc.wantErr, tc.wantCalls = true, 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("an answer that does not report on the policy is refused and the call is still reported",
				&dx12Provider{bodies: []string{`{"findings":[]}`}}, policy)
			tc.wantErr, tc.wantCalls = true, 1
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("a floor the call cannot meet is refused before anything is sent, with the ids", &dx12Provider{bodies: []string{dx12AuditClean}}, policy)
			tc.policy = dx12Policy(types.ContractEvidenceChecked)
			tc.requestID, tc.correlationID, tc.wantErr = "req-floor", "corr-floor", true
			return tc
		}(),
		func() dx12Case {
			tc := dx12AuditCase("a floor the call meets is delivered", &dx12Provider{bodies: []string{dx12AuditClean}}, policy)
			tc.policy = dx12Policy(types.ContractSchemaAndInvariantChecked)
			tc.wantCalls = 1
			return tc
		}(),
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { dx12Check(t, tc) })
	}
}

func TestAuditWithMetaWithNoOptionsAuditsLikeAuditWithModel(t *testing.T) {
	answer := `{"findings":[{"category":"quality","severity":0.6,"field":"id","issue":"odd","evidence":null,"recommendation":null}]}`
	subject := dx12Record{ID: "1", Email: "sam@example.com"}

	plainProvider := &dx12Provider{bodies: []string{answer}}
	plain, err := AuditWithModel(subject, AuditOptions{Context: WithProvider(context.Background(), plainProvider)})
	if err != nil {
		t.Fatalf("AuditWithModel: %v", err)
	}

	// No options at all: the Result form must not turn "none" into something
	// else on the way to the shared core.
	metaProvider := &dx12Provider{bodies: []string{answer}}
	ctx := WithProvider(context.Background(), metaProvider)
	result, err := AuditWithMeta(subject, AuditOptions{Context: ctx})
	if err != nil {
		t.Fatalf("AuditWithMeta: %v", err)
	}
	if !reflect.DeepEqual(plain, result.Value) {
		t.Errorf("the two forms judged differently:\n plain %+v\n meta  %+v", plain, result.Value)
	}
	dx12SameRequest(t, plainProvider, metaProvider)
}

func TestAuditWithMetaDoesNotChangeTheCallersOptions(t *testing.T) {
	provider := &dx12Provider{bodies: []string{dx12AuditClean}}
	opts := AuditOptions{Policies: []string{dx12AuditPolicy}, Context: WithProvider(context.Background(), provider)}
	original := opts.Context

	if _, err := AuditWithMeta(dx12Record{ID: "1"}, opts); err != nil {
		t.Fatalf("AuditWithMeta: %v", err)
	}
	if opts.Context != original || opts.RequestID != "" || opts.CorrelationID != "" {
		t.Errorf("the caller's options were written to: %+v", opts)
	}
}

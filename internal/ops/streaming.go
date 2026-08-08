package ops

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
	"github.com/monstercameron/schemaflux/telemetry"
)

// Three streaming modes, kept apart.
//
// TODOS.md ST-002's Revised line names three things a streaming text
// operation could hand back, and requires they never be conflated:
//
//  1. Raw text -- incremental deltas of the answer as the provider produces
//     them. TextStream, below, is this and only this: it never yields a
//     provider event, and never yields a struct claiming to be a partial
//     parsed result.
//  2. Provider events -- llm.StreamChunk, one level down in internal/llm.
//     It carries the same deltas plus the provider's own framing (Done,
//     the accumulated Response). A caller who type-asserts a Provider to
//     llm.StreamingProvider and calls CompleteStream directly gets this
//     mode; ops does not re-wrap it, because re-wrapping it as "raw text"
//     would be exactly the flattening this rule forbids.
//  3. Validated items -- a stream of items each checked against a schema
//     as they complete, for a collection operation (Choose, Filter,
//     Cluster, and friends in internal/ops/collection.go and
//     combinators.go). No such mode exists here. Building one by decoding
//     a partial JSON prefix off a token stream is precisely the "a token
//     stream is not a partial typed result" failure the Revised line
//     names: an item is either fully received and validated, or it does
//     not exist yet, and there is no honest halfway representation of a
//     struct that has been JSON-parsed by however many bytes happened to
//     have arrived. Nothing in this repository's collection operations
//     streams today, so there is no honest place to attach mode 3 yet --
//     rather than invent one, this file only ships modes 1 and 2, and mode
//     3 stays a documented gap, not a token stream wearing a struct's name.

// textStreamBufferSize bounds the channel between the goroutine reading the
// provider stream and the goroutine ranging over a TextStream.
//
// It is not sized to "never block" -- the opposite. Once it is full, the
// producer blocks on the channel send instead of buffering an unbounded
// number of deltas while a slow or stalled consumer never asks for them.
// That block propagates all the way to the socket: llm.CompleteStream is
// itself pull-based (see its doc comment), so a producer blocked on send
// here stops reading the wire, rather than reading ahead into memory.
const textStreamBufferSize = 32

// textStreamItem is either an incremental delta or the one terminal item a
// TextStream ever sends. Exactly one terminal item is sent per stream,
// always last, and it is never both a delta and a result.
type textStreamItem struct {
	delta string

	final bool
	err   error
	resp  llm.CompletionResponse
}

// TextStream is the caller-facing iterator for a streaming text operation.
// See the "Three streaming modes" note above for what this is and is not.
//
// Ordering: deltas are delivered in COMPLETION order -- the order the
// provider generated them in. For every provider wired up today that is
// also wire order; nothing here reorders or reassembles, so a caller
// appending each delta it receives directly to a buffer reconstructs the
// answer correctly.
//
// Cancellation: ranging over All and breaking out of the loop before the
// stream finishes cancels the underlying request, unless Detach was called
// first. A TextStream that nobody ever ranges over, and is simply dropped,
// leaks its goroutine and the request it is reading -- call Cancel (or
// range to completion) even when the caller never wanted the text.
type TextStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	items  chan textStreamItem

	detached bool // set only before ranging begins; see Detach.

	resultMu  sync.Mutex
	consumed  bool
	finalText string
	finalResp llm.CompletionResponse
	err       error
}

// Detach stops All from canceling the underlying request when the caller
// breaks out of the range loop early.
//
// Without calling this, stopping iteration stops the work -- the default,
// because a caller who walked away from a stream almost never wants the
// provider still spending their tokens on an answer nobody will read.
// Detach is for the opposite case: keep generating after the caller stops
// displaying tokens (to warm a cache, or to log the full answer).
//
// Detach only has an effect if it is called before the range loop breaks;
// calling it after All has already returned does nothing.
func (s *TextStream) Detach() { s.detached = true }

// Cancel stops the stream immediately, regardless of Detach.
func (s *TextStream) Cancel() { s.cancel() }

// All returns the iterator. Ranging over it yields each text delta as it
// arrives, in completion order; the loop ends when the stream completes,
// fails, or is canceled. After the loop ends, Err (and Text) report the
// outcome.
func (s *TextStream) All() iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for item := range s.items {
			if item.final {
				s.resultMu.Lock()
				s.consumed = true
				s.err = item.err
				if item.err == nil {
					s.finalResp = item.resp
					s.finalText = item.resp.Content
				}
				s.resultMu.Unlock()
				if item.err != nil {
					yield("", item.err)
				}
				return
			}
			if !yield(item.delta, nil) {
				if s.detached {
					// The caller wants the work to keep running after it
					// stops reading. Nobody will call All again to drain
					// s.items, so the producer would block forever on its
					// next send without this: a goroutine and the HTTP
					// response it holds open, leaked for the lifetime of
					// the process.
					go func() {
						for range s.items {
						}
					}()
				} else {
					s.cancel()
				}
				return
			}
		}
	}
}

// Text drains the stream and returns the complete text -- the same content
// a buffered call would return for an identical response, built by reading
// the same deltas a caller ranging manually would see, not by making a
// second request.
func (s *TextStream) Text() (string, error) {
	for _, err := range s.All() {
		if err != nil {
			return "", err
		}
	}
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.finalText, s.err
}

// Err reports the outcome after the stream has been fully consumed (via All
// or Text). It is the zero value, nil, until then.
func (s *TextStream) Err() error {
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	return s.err
}

// classifyFinalChunk decides whether a stream's terminal Response is usable,
// using the exact same classifier the buffered path uses in CallLLM
// (internal/ops/llm_helper.go) -- llm.ClassifyCompletion for a truncated
// body, then validateLLMCompletion for anything else. Reusing those
// functions rather than re-deriving the check here is what keeps a streamed
// failure classified identically to a buffered one for the same cause; a
// second implementation of "is this response usable" is exactly the second
// opinion AGENTS.md forbids.
func classifyFinalChunk(resp llm.CompletionResponse) error {
	if kind := llm.ClassifyCompletion(resp); kind == types.KindOutputTruncated {
		provider := resp.Provider
		model := resp.Model
		return &types.OperationError{
			Kind:     kind,
			Provider: provider,
			Model:    model,
			Message:  fmt.Sprintf("response truncated before it could be decoded (finish_reason=%q)", resp.FinishReason),
		}
	}
	return validateLLMCompletion(resp)
}

// streamLLM is the streaming counterpart of CallLLM (llm_helper.go).
//
// It builds the exact same request CallLLM would for identical inputs --
// literally the same unexported helpers (config.GetModel, applySteering,
// resolveResponseFormat, strengthenSystemPrompt, promptCacheKeyFor) compute
// every field, so the two paths cannot describe "the same call" two
// different ways. What it does not share with CallLLM is retries: a stream
// that fails partway has already shown the caller text, and re-sending the
// request would either duplicate what they saw or silently replace it --
// neither is "retrying a request", so streamLLM does not retry. A caller
// that wants retry-on-failure semantics around a stream gets one attempt and
// an error describing why, and decides for themselves whether to start a
// new TextStream.
func streamLLM(ctx context.Context, systemPrompt, userPrompt string, opts types.OpOptions) (*TextStream, error) {
	log := logger.GetLogger()

	_, provider := currentHooks()
	if provider == nil {
		// Streaming has no equivalent of the customLLMCaller test hook: that
		// hook is a plain (ctx, prompts) -> (string, error) function, which
		// has no way to emit incremental chunks. A caller (or test) that set
		// only a customLLMCaller and no provider gets ErrNoProvider here,
		// same as the buffered path gets when neither is set.
		return nil, ErrNoProvider
	}

	streamer, ok := provider.(llm.StreamingProvider)
	if !ok {
		// The provider is real but cannot stream. This must never fall back
		// to a buffered Complete dressed up as a one-chunk stream -- that
		// would hide the difference between "streams" and "doesn't" behind
		// an identical-looking success, which is the opposite of what a
		// capability error is for.
		return nil, &types.OperationError{
			Kind:     types.KindUnsupportedCapability,
			Provider: provider.Name(),
			Message:  fmt.Sprintf("provider %q does not support streaming", provider.Name()),
		}
	}

	model := config.GetModel(opts.Intelligence, provider.Name())
	if model == "" {
		return nil, fmt.Errorf(
			"no default model for provider %q; set SCHEMAFLUX_MODEL, or SCHEMAFLUX_MODEL_SMART / _FAST / _QUICK. Providers with a built-in mapping: %s",
			provider.Name(), strings.Join(config.KnownProviders(), ", "))
	}

	if err := pricing.CheckBudget(); err != nil {
		log.Error("Refusing the streaming request: budget exhausted",
			"provider", provider.Name(), "model", model, "error", err)
		return nil, err
	}

	maxTokens := config.GetMaxTokens(opts.Intelligence)
	if opts.MaxOutputTokens > 0 {
		maxTokens = opts.MaxOutputTokens
	}
	temperature := config.GetTemperature(opts.Mode)
	effectiveSystemPrompt := applySteering(systemPrompt, opts.Steering)
	responseFormat := resolveResponseFormat(opts.ResponseFormat, effectiveSystemPrompt)
	stableSystemPrompt := strengthenSystemPrompt(systemPrompt, responseFormat)

	req := llm.CompletionRequest{
		Model:          model,
		SystemPrompt:   strengthenSystemPrompt(effectiveSystemPrompt, responseFormat),
		UserPrompt:     userPrompt,
		Temperature:    float64(temperature),
		MaxTokens:      maxTokens,
		ResponseFormat: responseFormat,
		JSONSchema:     opts.JSONSchema,
		SchemaName:     opts.SchemaName,
		PromptCacheKey: promptCacheKeyFor(opts, model, responseFormat, stableSystemPrompt),
	}

	streamCtx, cancel := operationContext(ctx, config.GetTimeout())
	streamCtx, tracking := requesttracking.Ensure(streamCtx, opts.RequestID, opts.CorrelationID)
	start := time.Now()

	log.Debug("LLM stream started",
		"requestID", tracking.RequestID,
		"correlationID", tracking.CorrelationID,
		"provider", provider.Name(),
		"model", model,
		"responseFormat", responseFormat,
		"maxTokens", maxTokens,
	)

	stream := &TextStream{
		ctx:    streamCtx,
		cancel: cancel,
		items:  make(chan textStreamItem, textStreamBufferSize),
	}

	go runStream(streamCtx, stream, streamer, req, provider, model, responseFormat, opts, tracking, start)

	return stream, nil
}

// trySend delivers an item to the consumer, or gives up once the stream's
// context is canceled -- which is what unblocks a producer that is stuck
// sending into a full, abandoned channel after Cancel (directly, or via a
// non-detached break out of All).
func (s *TextStream) trySend(item textStreamItem) bool {
	select {
	case s.items <- item:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// runStream drains the provider's event stream into s.items, translating
// llm.StreamChunk (mode 2, provider events) into plain text deltas (mode 1,
// what TextStream promises) and applying the same completion classifier the
// buffered path uses before ever calling a Done chunk a success.
func runStream(
	ctx context.Context,
	s *TextStream,
	streamer llm.StreamingProvider,
	req llm.CompletionRequest,
	provider llm.Provider,
	model, responseFormat string,
	opts types.OpOptions,
	tracking requesttracking.Metadata,
	start time.Time,
) {
	defer close(s.items)
	log := logger.GetLogger()

	for chunk, err := range streamer.CompleteStream(ctx, req) {
		if err != nil {
			log.Error("LLM stream failed",
				"requestID", tracking.RequestID,
				"correlationID", tracking.CorrelationID,
				"provider", provider.Name(),
				"model", model,
				"duration_ms", time.Since(start).Milliseconds(),
				"error", err,
			)
			s.trySend(textStreamItem{final: true, err: err})
			return
		}

		if chunk.Delta != "" {
			if !s.trySend(textStreamItem{delta: chunk.Delta}) {
				return
			}
		}

		if chunk.Done {
			resp := chunk.Response
			if fErr := classifyFinalChunk(resp); fErr != nil {
				log.Error("LLM stream completed with an unusable response",
					"requestID", tracking.RequestID,
					"correlationID", tracking.CorrelationID,
					"provider", provider.Name(),
					"model", model,
					"finishReason", resp.FinishReason,
					"error", fErr,
				)
				s.trySend(textStreamItem{final: true, err: fErr})
				return
			}

			recordStreamCompletion(ctx, tracking, provider, model, responseFormat, opts, resp, start)
			s.trySend(textStreamItem{final: true, resp: resp})
			return
		}
	}

	// llm.StreamingProvider promises the sequence always ends in either an
	// error or a Done chunk (see its doc comment). This is the defensive
	// floor if that promise is ever broken by a future provider: a caller
	// must never see the range loop just end with no terminal item at all,
	// which would read as "the stream finished successfully" even though
	// nothing said so.
	s.trySend(textStreamItem{final: true, err: fmt.Errorf(
		"ops: provider %q ended its stream with no result", provider.Name())})
}

// recordStreamCompletion publishes the same accounting a buffered call
// records in CallLLM -- usage, cost, the call-record envelope, telemetry --
// for a streamed call's single (non-retried) attempt.
func recordStreamCompletion(
	ctx context.Context,
	tracking requesttracking.Metadata,
	provider llm.Provider,
	model, responseFormat string,
	opts types.OpOptions,
	resp llm.CompletionResponse,
	start time.Time,
) {
	log := logger.GetLogger()

	actualModel := resp.Model
	if actualModel == "" {
		actualModel = model
	}
	actualProvider := resp.Provider
	if actualProvider == "" {
		actualProvider = provider.Name()
	}

	usage := resp.Usage
	cost := pricing.CalculateCost(&usage, actualModel, actualProvider)
	metadata := &types.ResultMetadata{
		RequestID:     tracking.RequestID,
		CorrelationID: tracking.CorrelationID,
		RetryCount:    0, // streamLLM never retries; see its doc comment.
		TraceID:       telemetry.GetTraceID(ctx),
		SpanID:        telemetry.GetSpanID(ctx),
		StartTime:     start,
		EndTime:       time.Now(),
		Duration:      time.Since(start),
		Model:         actualModel,
		Provider:      actualProvider,
		Mode:          opts.Mode,
		Intelligence:  opts.Intelligence,
		TokenUsage:    &usage,
		CostInfo:      cost,
		Custom: map[string]any{
			"response_format": responseFormat,
			"schema_id":       opts.SchemaID,
			"streamed":        true,
		},
	}

	publishCallRecord(ctx, metadata)
	pricing.TrackCost(cost, metadata)
	telemetry.RecordLLMMetrics(metadata)

	log.Info("LLM stream completed",
		"requestID", tracking.RequestID,
		"correlationID", tracking.CorrelationID,
		"provider", actualProvider,
		"model", actualModel,
		"responseFormat", responseFormat,
		"duration_ms", metadata.Duration.Milliseconds(),
		"tokens_total", usage.TotalTokens,
		"cost_usd", cost.TotalCost,
		"finishReason", resp.FinishReason,
	)
}

// StreamSummarize streams Summarize's answer. See Summarize
// (internal/ops/text.go) for the operation; this builds the identical
// system and user prompt via the same unexported instruction builders, so
// the two cannot silently diverge, and calls streamLLM instead of callLLM.
func StreamSummarize(input string, opts SummarizeOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	opt := textOperationOptions(opts.toOpOptions(), opts.OpOptions.Steering, summarizeInstructions(opts))

	systemPrompt := `You are a text summarization expert. Create concise summaries that preserve key information.

Rules:
- Maintain the most important points
- Use clear, concise language
- Preserve critical details and context
- Keep the original tone when appropriate`

	userPrompt := fmt.Sprintf("Summarize this text:\n%s", input)

	return streamLLM(opts.OpOptions.Context, systemPrompt, userPrompt, opt)
}

// StreamRewrite streams Rewrite's answer. See Rewrite (internal/ops/text.go).
func StreamRewrite(input string, opts RewriteOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	opt := textOperationOptions(opts.toOpOptions(), opts.OpOptions.Steering, rewriteInstructions(opts))

	systemPrompt := `You are a text rewriting expert. Modify text while preserving its core meaning.

Rules:
- Maintain the original message and intent
- Improve clarity and readability
- Adapt style as requested
- Fix grammar and spelling errors`

	userPrompt := fmt.Sprintf("Rewrite this text:\n%s", input)

	return streamLLM(opts.OpOptions.Context, systemPrompt, userPrompt, opt)
}

// StreamTranslate streams Translate's answer. See Translate
// (internal/ops/text.go).
func StreamTranslate(input string, opts TranslateOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	opt := textOperationOptions(opts.toOpOptions(), opts.OpOptions.Steering, translateInstructions(opts))

	systemPrompt := `You are a translation expert. Translate text accurately between languages.

Rules:
- Preserve meaning and nuance
- Maintain appropriate formality level
- Handle idioms and cultural references appropriately
- Keep technical terms accurate`

	userPrompt := fmt.Sprintf("Translate this text:\n%s", input)

	return streamLLM(opts.OpOptions.Context, systemPrompt, userPrompt, opt)
}

// StreamExpand streams Expand's answer. See Expand (internal/ops/text.go).
func StreamExpand(input string, opts ExpandOptions) (*TextStream, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	opt := textOperationOptions(opts.toOpOptions(), opts.OpOptions.Steering, expandInstructions(opts))

	systemPrompt := `You are a content expansion expert. Elaborate on text with additional detail and context.

Rules:
- Add relevant details and examples
- Maintain consistency with the original
- Provide useful elaboration
- Keep the expanded content coherent`

	userPrompt := fmt.Sprintf("Expand on this text:\n%s", input)

	return streamLLM(opts.OpOptions.Context, systemPrompt, userPrompt, opt)
}

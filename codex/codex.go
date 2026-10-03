// Package codex runs SchemaFlux operations against the Codex CLI's existing
// subscription login, so a host can use this library with no API key at all.
//
// It is a separate package for the reason telemetry/otel is (OB-001): the
// library a consumer imports should not carry an adapter's concerns. Nothing
// in the core knows this package exists, and a host that does not import it
// gets no process-spawning code path, no CLI flags, and no new public API.
//
// # What it authenticates as
//
// Whoever ran `codex login`. This package never reads, copies, or forwards an
// API key -- and it goes further than not using one: it removes the key
// variables from the child process's environment (see environmentWithoutAPIKeys),
// so the CLI cannot quietly fall back to key-based authentication and bill an
// account the caller did not choose. That fallback is the failure worth
// engineering against, because it succeeds: the answers come back, and nothing
// in the result says which identity paid for them.
//
// # What it costs
//
// Nothing this library can state. The tokens are real and the CLI reports
// them, but they are drawn against a flat-rate plan rather than invoiced per
// token, so every cost this transport produces is *unpriced* -- not zero.
// pricing.CalculateCost refuses to multiply these counts by any rate card
// (PS-013). A zero would be indistinguishable from a free call and would
// understate a caller's spend silently, which is the PR-001 mistake.
//
// # Using it
//
//	provider, err := codex.New(codex.Options{Model: "gpt-5.6-sol"})
//	if err != nil {
//	    return err
//	}
//	client := schemaflux.NewClient("").WithProviderInstance(provider)
//
// Or, to make the CLI selectable by name from configuration:
//
//	codex.Register()
//	client := schemaflux.NewClient("").WithProvider("codex")
//
// # The model has to be named
//
// This package registers no tier-to-model mapping, so `WithProvider("codex")`
// requires a model from SCHEMAFLUX_MODEL, a pin on the call, or Options.Model.
// That is deliberate. Which models a codex login may run is a property of the
// CLI's version and the caller's subscription, not of this library, and a
// built-in default here would be this library asserting something it cannot
// know -- the same defect that once sent `gpt-5.4` to api.deepseek.com because
// a provider with no mapping fell through to somebody else's.
//
// # One prompt channel
//
// `codex exec` takes a single task on stdin; it has no system-role channel.
// So the operation's instructions and the caller's data arrive in one stream,
// where a chat API would keep them in separate roles. The library's own trust
// boundary still applies upstream -- steering is delimited before it reaches
// any provider (TC-001) -- but the role separation a provider API would give
// for free is not available here, and a caller weighing prompt-injection risk
// should know that this transport has one channel rather than two.
//
// # No tools, no conversation, no search
//
// For the same reason the CLI cannot be handed tool declarations, a message
// list or a web search. A request that sets Tools, ToolChoice, Messages or
// WebSearch is refused with the unsupported-capability kind before the CLI is
// run, and is never answered from its prompts alone as though it had not set
// them. Behind mw.Fallback the refusal sends the request to the next route.
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// providerName is what this transport calls itself everywhere: the registry
// key, CompletionResponse.Provider, the observer's events, and pricing's
// unmetered set. One constant, because those four agreeing is what lets a host
// filter its telemetry by provider and get the right rows.
const providerName = "codex"

// defaultExecutable is the CLI's name on PATH.
const defaultExecutable = "codex"

// executableEnvVar names an override, for a host that installs the CLI
// somewhere PATH does not reach.
const executableEnvVar = "SCHEMAFLUX_CODEX_BIN"

// Options configures the transport.
type Options struct {
	// Executable is the codex binary. Empty means SCHEMAFLUX_CODEX_BIN, then
	// "codex" on PATH.
	Executable string

	// Model is the model to run when a request pins none. Empty is allowed --
	// the request usually carries one, because CallLLM refuses to call a
	// provider without resolving a model first.
	Model string

	// ReasoningEffort is passed to the CLI as model_reasoning_effort when set.
	// Empty leaves the CLI's own default alone.
	ReasoningEffort string

	// MaxRetries and RetryBackoff are reported through RetryPolicy, and follow
	// the contract README's "The retry contract" states: a positive value is
	// that many retries, zero is none, and a negative value means this
	// provider has no opinion and the library's default applies.
	//
	// The zero value of this struct therefore means "no retries", which is the
	// same thing every other provider's zero-valued ProviderConfig means.
	MaxRetries   int
	RetryBackoff time.Duration
}

// Provider is the codex transport. It satisfies the library's provider
// interface, so everything reaches it through the same path every other
// provider is reached through -- which is what makes an operation run over the
// CLI observable, budgeted, retried, and classified exactly as an API-backed
// one is.
type Provider struct {
	options Options

	// turn is the shell boundary. It is nil in production, where runTurn does
	// the work; a test replaces it to script the CLI without one existing.
	turn turn
}

// New returns a codex provider, resolving the executable so a missing CLI is
// reported now rather than on the first operation.
//
// Reporting it now matters more than it looks: the first operation is usually
// inside somebody's request handler, and "executable file not found" arriving
// there reads as a transient provider failure and gets retried three times
// before surfacing. Here it is a configuration error, which is what it is.
func New(options Options) (*Provider, error) {
	if strings.TrimSpace(options.Executable) == "" {
		options.Executable = strings.TrimSpace(os.Getenv(executableEnvVar))
	}
	if options.Executable == "" {
		options.Executable = defaultExecutable
	}

	resolved, err := exec.LookPath(options.Executable)
	if err != nil {
		return nil, &types.OperationError{
			Kind:     types.KindConfiguration,
			Provider: providerName,
			Message: fmt.Sprintf(
				"the codex CLI (%q) is not on PATH; install it and run `codex login`, or set %s to its full path",
				options.Executable, executableEnvVar),
			Cause: err,
		}
	}
	options.Executable = resolved

	return &Provider{options: options}, nil
}

// Register makes the transport selectable as "codex" through the library's
// provider registry, so a host can choose it from configuration rather than by
// constructing it.
//
// It is a function rather than an init(): registering as a side effect of
// import would mean a host that imported this package for its types silently
// gained a new provider name, and a registry that changes because of an import
// is one nobody can audit by reading configuration.
func Register() error {
	return llm.RegisterProviderFactory(providerName, func(config llm.ProviderConfig) (llm.Provider, error) {
		return New(Options{
			MaxRetries:   config.MaxRetries,
			RetryBackoff: config.RetryBackoff,
		})
	})
}

// Name identifies the transport.
func (p *Provider) Name() string { return providerName }

// EstimateCost reports zero, and the zero means "this transport is not metered
// per token", not "this call is free".
//
// Nothing downstream treats it as money: CallLLM's cost comes from
// pricing.CalculateCost, which refuses to price this provider at all and
// reports Priced=false. This method exists because the interface has it.
func (p *Provider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// RetryPolicy reports the configured budget, following the same three-way
// contract every other provider follows: positive is a count, zero is none,
// and negative means no opinion.
func (p *Provider) RetryPolicy() (int, time.Duration) {
	return p.options.MaxRetries, p.options.RetryBackoff
}

// Complete runs one codex turn.
func (p *Provider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	// The CLI takes one task on stdin. Tool declarations, a conversation and a
	// search have no place in it, and a request that sets one was answered from
	// its system and user prompts alone: a conversation held in Messages was
	// answered as the empty user prompt. It is refused before the CLI runs.
	if err := llm.RefuseUnsupportedFields(req, providerName, "Tools", "ToolChoice", "WebSearch", "Messages"); err != nil {
		return llm.CompletionResponse{}, err
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(p.options.Model)
	}

	schema, err := encodeSchema(req)
	if err != nil {
		return llm.CompletionResponse{}, err
	}

	request := turnRequest{
		model:  model,
		prompt: []byte(assemblePrompt(req)),
		schema: schema,
	}
	// LX-105. A caller's effort for this call replaces the one the provider
	// was configured with. An operation's preference does not: it was measured
	// on another model, and this provider's own setting is the operator's.
	// The request's effort used to be ignored altogether.
	if !req.ReasoningEffortPreferred {
		request.effort = strings.TrimSpace(req.ReasoningEffort)
		// The value becomes a line of the CLI's configuration, so it is held
		// to the words an effort can be. Which of them a model takes is the
		// CLI's to say; that it is an effort at all is checked here, where a
		// misspelling can be named instead of failing a turn later.
		if request.effort != "" && !isEffortWord(request.effort) {
			return llm.CompletionResponse{}, &types.OperationError{
				Kind:     types.KindInvalidRequest,
				Provider: providerName,
				Model:    model,
				Message:  "the reasoning effort asked for is not one of none, minimal, low, medium, high, xhigh, max",
			}
		}
	}

	transcript, runErr := p.transport()(ctx, request)

	// The caller's context is checked before anything else is interpreted. A
	// killed child produces a torn event stream that would otherwise be
	// classified on its contents, reporting a provider failure for what was
	// actually the caller hanging up -- and the two are treated differently
	// everywhere downstream, starting with whether they are retried.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return llm.CompletionResponse{}, ctxErr
	}

	result := scanEvents(transcript.events)

	if runErr != nil && transcript.exitCode < 0 {
		kind, message := classifyTurn(result, -1)
		return llm.CompletionResponse{}, classified(kind, model, message, &Error{
			ExitCode: -1,
			Failure:  result.failure,
			Stderr:   string(transcript.stderr),
		})
	}

	// A completed event is the only thing that makes a turn a success. A zero
	// exit with no completed event is a CLI that stopped early, and reporting
	// its empty output as an empty answer would be failing open.
	if !result.completed {
		kind, message := classifyTurn(result, transcript.exitCode)
		return llm.CompletionResponse{}, classified(kind, model, message, &Error{
			ExitCode: transcript.exitCode,
			Failure:  result.failure,
			Stderr:   string(transcript.stderr),
		})
	}

	return llm.CompletionResponse{
		Content:  strings.TrimSpace(string(transcript.reply)),
		Usage:    result.usage,
		Provider: providerName,

		// Model is deliberately left empty. The CLI does not report which
		// model actually served the turn in any event this package reads, and
		// filling in the model that was *asked for* would claim an observation
		// nobody made -- types.ResultMetadata.ObservedModel exists precisely
		// to distinguish "the provider said" from "we assumed", and drift
		// detection reads it. Empty means unknown, which is true.
		Model: "",

		// A completed turn ended normally; that is what the event means. This
		// is a translation of the CLI's own signal, not a guess about one it
		// did not send -- and it matters upstream, where an unrecognised
		// finish reason is checked for truncation before the body is decoded.
		FinishReason: "stop",
	}, nil
}

// transport returns the shell boundary in use.
func (p *Provider) transport() turn {
	if p.turn != nil {
		return p.turn
	}
	return p.runTurn
}

// assemblePrompt flattens the request's two prompt segments into the single
// task the CLI accepts.
//
// The order is the same one every provider in this library sends -- system
// policy first, then the user message -- so a prompt that behaves one way
// against an API behaves the same way here. See the package comment for what
// is lost: the CLI has no system role, so this is one stream rather than two.
func assemblePrompt(req llm.CompletionRequest) string {
	system := strings.TrimSpace(req.SystemPrompt)
	user := strings.TrimSpace(req.UserPrompt)

	switch {
	case system == "":
		return user
	case user == "":
		return system
	default:
		return system + "\n\n" + user
	}
}

// encodeSchema renders the request's JSON Schema for --output-schema, or
// returns nil when the request declared none.
//
// A request that asks for JSON without supplying a schema gets no schema file
// rather than an invented one: the CLI would enforce whatever was written
// here, and a guessed schema is a constraint the caller never asked for.
func encodeSchema(req llm.CompletionRequest) ([]byte, error) {
	if len(req.JSONSchema) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(req.JSONSchema)
	if err != nil {
		// The schema is generated by this library from the caller's Go type,
		// so a failure here is this library's bug -- but the error must still
		// not carry the schema, which describes the caller's data shape in
		// detail. The field count is enough to locate it.
		return nil, &types.OperationError{
			Kind:     types.KindConfiguration,
			Provider: providerName,
			Message: fmt.Sprintf("the generated output schema (%d top-level keys) could not be encoded",
				len(req.JSONSchema)),
		}
	}
	return encoded, nil
}

// isEffortWord reports whether word is one of the reasoning efforts this
// library names (types.Effort).
func isEffortWord(word string) bool {
	switch types.Effort(word) {
	case types.EffortNone, types.EffortMinimal, types.EffortLow, types.EffortMedium,
		types.EffortHigh, types.EffortXHigh, types.EffortMax:
		return true
	}
	return false
}

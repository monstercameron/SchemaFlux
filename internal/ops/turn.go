package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Turn is what one request of a conversation says: the instructions the model is
// given, the tools it may ask for, and everything said so far, oldest first.
type Turn struct {
	// Instructions is the system prompt, sent exactly as written. It is the
	// program's own text; the reinforcement block an operation adds to its prompt
	// is for a declared shape of answer and would contradict a conversation's own
	// instructions, so none is added and a client's OmitReinforcement does not
	// apply.
	Instructions string

	// Tools are the functions the model may ask for. Nil means the model can only
	// answer in words.
	Tools []llm.Tool

	// Messages is the conversation. It is the whole of the request's input: a turn
	// has no separate user prompt.
	Messages []llm.Message
}

// turnOperation names a turn in an error that was not made under an operation of
// its own, which is a turn called without a record around it.
const turnOperation = "llm.turn"

// CallTurn sends one turn of a conversation to the model and returns the whole
// answer: its text, its tool calls, or both. It is CallLLM for a request that is
// a conversation, and it sends it through the same function (callRequest), so
// that what a call to the model is held to is held here too: the client's data
// policy and budget before the request, its scheduler, retries and deadline, the
// observers and the call record. A reply that is only tool calls is a complete
// answer and is not asked for again.
//
// The request carries Tools and Messages, the instructions as its system prompt,
// text as its format and nothing in its user prompt, schema, tool choice or
// decision. It refuses before anything is sent a route that is declared to take
// no tools or no conversation, and a conversation that cannot fit the model's
// context window when that window is declared (turnFitsTheWindow). A route that
// declares nothing is asked, and a provider that cannot carry Tools or Messages
// refuses with the unsupported kind itself (llm.RefuseUnsupportedFields).
//
// The checks of the reply are not made here: they need the history the reply
// is to be added to (RunTurn).
func CallTurn(ctx context.Context, turn Turn, opts types.OpOptions) (llm.CompletionResponse, error) {
	if ctx == nil {
		return llm.CompletionResponse{}, types.Newf(types.KindInvalidRequest, turnOperation, "the context is nil")
	}
	if err := contextOver(ctx); err != nil {
		return llm.CompletionResponse{}, err
	}
	if err := checkCallOptions(opts); err != nil {
		return llm.CompletionResponse{}, err
	}
	if len(turn.Messages) == 0 {
		return llm.CompletionResponse{}, types.Newf(types.KindInvalidRequest, turnOperation, "a turn needs at least one message")
	}

	ctx, _ = PinExec(ctx)

	provider, _, err := routeFor(ctx)
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	if provider == nil {
		// Only the test hook that answers a (system, user) pair is set. It cannot
		// carry a conversation, and answering the pair of an empty prompt from it
		// would be the failure this library does not do.
		return llm.CompletionResponse{}, noProvider
	}

	// The client's Timeout binds a turn as it binds an operation, whatever the
	// call site passed.
	ctx, cancel := clientDeadline(ctx)
	defer cancel()

	model := resolveModel(ctx, opts, provider)
	if model == "" {
		return llm.CompletionResponse{}, config.NoDefaultModelError(provider.Name())
	}

	op := operationNameFor(ctx, opts)
	if op == "" {
		op = turnOperation
	}
	if err := routeTakesTheTurn(op, provider.Name(), model, turn); err != nil {
		return llm.CompletionResponse{}, err
	}

	maxTokens := config.GetMaxTokens(opts.Intelligence)
	if opts.MaxOutputTokens > 0 {
		maxTokens = opts.MaxOutputTokens
	}
	if err := turnFitsTheWindow(op, provider.Name(), model, turn, maxTokens); err != nil {
		return llm.CompletionResponse{}, err
	}

	effort, effortPreferred := requestEffort(ctx, opts)
	req := llm.CompletionRequest{
		Model:                    model,
		SystemPrompt:             turn.Instructions,
		Temperature:              float64(config.GetTemperature(opts.Mode)),
		MaxTokens:                maxTokens,
		ResponseFormat:           "text",
		PromptCacheKey:           turnCacheKey(model, turn.Instructions, turn.Tools),
		Tools:                    turn.Tools,
		Messages:                 turn.Messages,
		WebSearch:                opts.WebSearch,
		ReasoningEffort:          effort,
		ReasoningEffortPreferred: effortPreferred,
	}
	return callRequest(ctx, provider, req, opts)
}

// routeTakesTheTurn refuses a route that has declared it cannot do what the turn
// needs. A route that has declared nothing about it is not refused here: there
// is no fact to act on, and a provider that cannot carry tools or a message list
// says so itself before it sends anything. A capability that was declared and is
// false is a fact, and the request is refused as unsupported without being sent.
func routeTakesTheTurn(op, provider, model string, turn Turn) error {
	caps, known := llm.CapabilitiesFor(provider, model)
	if !known {
		return nil
	}
	missing := ""
	switch {
	case len(turn.Tools) > 0 && caps.HasDeclared(llm.CapToolCalling) && !caps.Has(llm.CapToolCalling):
		missing = "tool calls"
	case len(turn.Messages) > 1 && caps.HasDeclared(llm.CapMultiTurn) && !caps.Has(llm.CapMultiTurn):
		missing = "a conversation of more than one message"
	}
	if missing == "" {
		return nil
	}
	return &types.OperationError{
		Kind:     types.KindUnsupportedCapability,
		Op:       op,
		Provider: provider,
		Model:    model,
		Message:  "the route is declared to take no " + missing + ", so nothing was sent",
	}
}

// turnFitsTheWindow refuses a conversation that cannot fit the model's context
// window before it is sent, which is better than letting the vendor reject it
// with an error that names none of this library's concepts. The window is the
// one the capability registry declares for the route, the figure the planner's
// capability snapshot reads. A route that declares none is not refused: the
// planner's DefaultContextTokens is an assumption made to size chunks, and
// refusing a conversation on an assumed window would refuse every long one on a
// model whose window is larger and was never written down.
//
// The size is an estimate of the input, the bytes of everything the request
// carries over charactersPerToken, plus the ceiling on the reply, since the
// window holds both. It is low for dense JSON and for scripts that take several
// bytes a character, so a conversation it admits can still be too large for the
// vendor, and one it refuses is certainly too large.
func turnFitsTheWindow(op, provider, model string, turn Turn, maxTokens int) error {
	caps, known := llm.CapabilitiesFor(provider, model)
	if !known || caps.ContextWindow <= 0 {
		return nil
	}
	estimated := (turnBytes(turn) + charactersPerToken - 1) / charactersPerToken
	if estimated+maxTokens <= caps.ContextWindow {
		return nil
	}
	return types.Newf(types.KindContextTooLarge, op,
		"the conversation is about %d tokens and the reply may take up to %d more, which is over the %d-token context window of the model; shorten the history before sending it",
		estimated, maxTokens, caps.ContextWindow)
}

// turnBytes is the size of what a turn's request carries, in bytes.
func turnBytes(turn Turn) int {
	total := len(turn.Instructions)
	for _, tool := range turn.Tools {
		total += len(tool.Name) + len(tool.Description)
		if len(tool.Parameters) > 0 {
			if encoded, err := json.Marshal(tool.Parameters); err == nil {
				total += len(encoded)
			}
		}
	}
	for _, message := range turn.Messages {
		total += len(message.Content)
		for _, call := range message.ToolCalls {
			total += len(call.Name) + len(call.Arguments)
		}
	}
	return total
}

// turnCacheKey is the identity a conversation's requests share, which is the
// part of them that does not change from turn to turn: the model, the
// instructions and the tool declarations. It never holds the history, which grows
// with every turn and would give every turn a key no other turn has, and a key
// that is never sent twice is a prefix cache that never hits. A tool's schema is
// written through encoding/json, which orders a map's keys, so the same
// declaration is the same bytes.
func turnCacheKey(model, instructions string, tools []llm.Tool) string {
	hash := sha256.New()
	field := func(text string) {
		// The length first, so that "ab"+"c" and "a"+"bc" are not one key.
		hash.Write([]byte(strconv.Itoa(len(text))))
		hash.Write([]byte{0})
		hash.Write([]byte(text))
	}
	field(instructions)
	for _, tool := range tools {
		field(tool.Name)
		field(tool.Description)
		encoded, err := json.Marshal(tool.Parameters)
		if err != nil {
			// A schema that cannot be written cannot be sent either, and the provider
			// refuses the request. The key only has to differ from a declaration
			// that can be written.
			encoded = []byte("?")
		}
		field(string(encoded))
	}
	return "chat:" + model + ":" + hex.EncodeToString(hash.Sum(nil))[:12]
}

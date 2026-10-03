package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// LAT-006. This library's own share of a decision.
//
// A program that makes every control-flow decision with Is, Which and Assess
// builds this request on every message. Most of it depends on the questions and
// on nothing else: how the questions read in the message, whether any of them
// mentions a number, and the declared answer. A set of questions that is asked
// again is worked out once, and a call pays only for what is new in it, which
// is the subject.

// dec12Ask is a set of questions together with what is worked out from them
// alone.
type dec12Ask struct {
	questions []assessQuestion

	// ready says the rest has been worked out. A set of questions asked once is
	// worked out by the call that asks it; a type's questions are worked out
	// when its plan is read and every call after reuses them.
	ready bool
	// block is the questions as the message shows them.
	block string
	// numbers is whether any question or option mentions a number, which is
	// what decides the effort a decision prefers.
	numbers bool
	// schema and schemaConfidence are the declared answer without and with the
	// model's confidence, when they were worked out ahead. Otherwise they are
	// taken from dec12Schemas.
	schema, schemaConfidence map[string]any
	// typed is the questions as a decision provider reads them, when they were
	// worked out ahead. Otherwise the call that asks them works them out.
	typed []llm.DecisionQuestion
}

// dec12NewAsk refuses questions that cannot be asked and works out how the
// message shows them.
func dec12NewAsk(questions []assessQuestion) (dec12Ask, error) {
	if err := checkQuestions(questions); err != nil {
		return dec12Ask{}, err
	}
	block, err := dec12RenderQuestions(questions)
	if err != nil {
		return dec12Ask{}, err
	}
	return dec12Ask{questions: questions, ready: true, block: block, numbers: dec12QuestionsMentionANumber(questions)}, nil
}

// dec12PlanAsk is dec12NewAsk for a type's questions, with the declared answer
// kept as well: a type is asked about again and again, so nothing is looked up
// for it after this.
func dec12PlanAsk(questions []assessQuestion) (dec12Ask, error) {
	ask, err := dec12NewAsk(questions)
	if err != nil {
		return dec12Ask{}, err
	}
	ask.schema = dec12Schemas.declare(questions, false)
	ask.schemaConfidence = dec12Schemas.declare(questions, true)
	ask.typed = decisionQuestions(questions)
	return ask, nil
}

// declared is the declared answer for these questions. It is shared by every
// call that asks questions of the same kinds, and nothing may change it.
func (ask dec12Ask) declared(confidence bool) map[string]any {
	if confidence && ask.schemaConfidence != nil {
		return ask.schemaConfidence
	}
	if !confidence && ask.schema != nil {
		return ask.schema
	}
	return dec12Schemas.declare(ask.questions, confidence)
}

// dec12QuestionsMentionANumber reports whether anything the caller wrote in
// the questions mentions a number.
func dec12QuestionsMentionANumber(questions []assessQuestion) bool {
	for _, q := range questions {
		if mentionsANumber(q.ask) {
			return true
		}
		for _, label := range q.labels {
			if mentionsANumber(label) {
				return true
			}
		}
	}
	return false
}

// dec12Request is a decision as it goes to the provider: the message, the
// options that carry the declared answer, and the reasoning effort the decision
// prefers.
type dec12Request struct {
	user   string
	opt    types.OpOptions
	effort types.Effort
	// state and guidance are the subject, written as JSON, and the caller's
	// steering, each by itself: the two things of the message that a decision
	// provider is handed typed (decisionCall).
	state, guidance string
}

// dec12BuildRequest does everything a decision does before the provider is
// called: it refuses questions that cannot be asked, writes the subject and the
// message, and declares the answer. It is apart from assessCore so that what
// this library spends on a decision besides the wait for the model can be
// measured by itself. ctx is the call's context, which says where a line it
// writes goes; it is not used for anything else, and may be nil.
func dec12BuildRequest(ctx context.Context, subject any, ask dec12Ask, confidence bool, opts []types.OpOptions) (dec12Request, error) {
	if !ask.ready {
		var err error
		if ask, err = dec12NewAsk(ask.questions); err != nil {
			return dec12Request{}, err
		}
	}
	shown, err := writtenSubject("assess", subject)
	if err != nil {
		return dec12Request{}, err
	}
	// A decision is the kind of call the fast tier is for. A caller who names
	// a tier gets the one they named.
	tierChosen := false
	for _, given := range opts {
		tierChosen = tierChosen || given.Intelligence != types.TierUnset
	}
	opt := applyDefaults(opts...)
	if !tierChosen {
		opt.Intelligence = types.Fast
	}

	written, err := dec12WriteSubject(shown)
	if err != nil {
		return dec12Request{}, types.Newf(types.KindInvalidRequest, "assess", "the subject could not be written as JSON")
	}
	userPrompt, err := assessUserPrompt(ask.block, written, opt.Steering, confidence)
	if err != nil {
		loggerFor(ctx).Error("Assess operation failed: the request could not be written", "error", err)
		return dec12Request{}, err
	}

	// Reasoning buys nothing on a judgment that holds no number, and costs a
	// third of the wait (LX-105). The preference is only acted on for a model
	// it was measured on, and never over an effort the caller set. It looks at
	// what the caller supplied and not at the message, whose own wording
	// ("q1", "a rating from 1 to 5") is full of numbers.
	effort := judgmentEffort(opt.Steering, written)
	if ask.numbers {
		effort = types.EffortDefault
	}

	// The steering is in the message, as a quoted value. Left on the options
	// it would be added to the instructions as well.
	guidance := strings.TrimSpace(opt.Steering)
	opt.Steering = ""
	opt.ResponseFormat = "json"
	opt.JSONSchema = ask.declared(confidence)
	opt.SchemaName = "assessment"
	return dec12Request{user: userPrompt, opt: opt, effort: effort, state: written, guidance: guidance}, nil
}

// LX-100. The decision, typed, beside the message.
//
// The message above puts the questions to a model that writes JSON. A decision
// provider cannot read a prompt and does not need to: it is handed the same
// questions typed, on the request (llm.CompletionRequest.Decision). The request
// is built in CallLLM, which every operation reaches through a string and its
// options, so the decision travels to it on the context of the one provider
// call it belongs to.
//
// It is not a field of types.OpOptions. Those are the caller's own settings,
// and a decision is not something a caller sets: the operation derives it from
// its arguments.

// decisionQuestion is a question as a decision provider reads it. The labels of
// a choice are the caller's own slice and are not copied.
func decisionQuestion(q assessQuestion) llm.DecisionQuestion {
	switch q.kind {
	case questionChoice:
		return llm.DecisionQuestion{Kind: llm.DecisionChoice, Ask: q.ask, Options: q.labels}
	case questionRating:
		return llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: q.ask, Low: q.low, High: q.high}
	}
	return llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: q.ask}
}

// decisionQuestions is a set of questions as a decision provider reads them.
func decisionQuestions(questions []assessQuestion) []llm.DecisionQuestion {
	typed := make([]llm.DecisionQuestion, len(questions))
	for i, q := range questions {
		typed[i] = decisionQuestion(q)
	}
	return typed
}

// decisionCall is the context of one provider call that makes a decision. It
// carries the decision to CallLLM, which puts it on the request, and carries
// back how the answer came: typed or as JSON, from whom, and whether a decision
// provider was asked first.
//
// It is a context of its own and not a value in one so that a decision costs a
// generating provider's caller nothing: Is, Which, Rate and Assess already
// wrapped their context once to decline the reinforcement block, and this is
// that wrapper, grown to hold the decision as well. The questions of a type are
// the ones its plan keeps, and one question is held here, so nothing else is
// allocated for it.
type decisionCall struct {
	context.Context

	decision llm.Decision
	// one is where a decision of one question keeps it.
	one [1]llm.DecisionQuestion
	// withoutReinforcement declines the reinforcement block for the call, as
	// dec12WithoutReinforcement does.
	withoutReinforcement bool

	// What came back, set by CallLLM when the call is answered.

	// typed is whether the answer came as typed answers, which CallLLM wrote in
	// the decision reader's form, and not as JSON a model wrote.
	typed bool
	// answeredBy is the provider the answer names.
	answeredBy string
	// escalation is set when a decision provider was asked first and the answer
	// is not its own.
	escalation *llm.DecisionEscalation
}

type decisionCallKey struct{}

// Value answers for the call itself and for the reinforcement block, and hands
// every other key on.
func (call *decisionCall) Value(key any) any {
	switch key.(type) {
	case decisionCallKey:
		return call
	case dec12WithoutReinforcementKey:
		if call.withoutReinforcement {
			return true
		}
	}
	return call.Context.Value(key)
}

// newDecisionCall returns the context a decision's provider call runs under.
// typed is the questions when they were worked out ahead; otherwise they are
// worked out from questions here.
func newDecisionCall(ctx context.Context, state, guidance string, questions []assessQuestion, typed []llm.DecisionQuestion) *decisionCall {
	if ctx == nil {
		ctx = context.Background()
	}
	call := &decisionCall{Context: ctx}
	switch {
	case typed != nil:
	case len(questions) == 1:
		call.one[0] = decisionQuestion(questions[0])
		typed = call.one[:]
	default:
		typed = decisionQuestions(questions)
	}
	call.decision = llm.Decision{State: state, Guidance: guidance, Questions: typed}
	return call
}

// decisionCallFrom is the decision call a context belongs to, or nil.
func decisionCallFrom(ctx context.Context) *decisionCall {
	if ctx == nil {
		return nil
	}
	call, _ := ctx.Value(decisionCallKey{}).(*decisionCall)
	return call
}

// requestDecision is the decision a request made on ctx carries, or nil for a
// request that decides nothing.
func requestDecision(ctx context.Context) *llm.Decision {
	if call := decisionCallFrom(ctx); call != nil && len(call.decision.Questions) > 0 {
		return &call.decision
	}
	return nil
}

// typedAnswerContent writes a decision provider's typed answers in the form
// the decision reader takes, as the content of the response. A response with no
// typed answers is a generating provider's, and is left as it is. Typed answers
// that are not answers to the questions asked are refused, in this library's
// words, and the content is left empty.
func typedAnswerContent(req llm.CompletionRequest, resp *llm.CompletionResponse) error {
	if len(resp.DecisionAnswers) == 0 {
		return nil
	}
	rendered, err := llm.RenderDecisionAnswers(req.Decision, resp.DecisionAnswers)
	if err != nil {
		resp.Content = ""
		return err
	}
	resp.Content = rendered
	return nil
}

// Keys of ResultMetadata.Custom that say how a decision was answered.
const (
	// customDecisionTyped is true when the answer came typed from a decision
	// provider.
	customDecisionTyped = "decision_typed"
	// customDecisionAskedFirst names the decision provider that was asked first
	// when the answer is not its own, and customDecisionEscalated says why.
	customDecisionAskedFirst = "decision_asked_first"
	customDecisionEscalated  = "decision_escalated"
	// customDecisionAskedFirstConfidence is the lowest probability that
	// provider reported, when it reported one: its claim about the answer that
	// was not used.
	customDecisionAskedFirstConfidence = "decision_asked_first_confidence"
)

// noteDecisionAnswer records how a decision was answered: on the call's record,
// and for the operation that asked, on its decision call.
func noteDecisionAnswer(ctx context.Context, resp llm.CompletionResponse, metadata *types.ResultMetadata) {
	typed := len(resp.DecisionAnswers) > 0
	if !typed && resp.DecisionEscalation == nil {
		// A generating provider's answer to a request nobody else was asked.
		if call := decisionCallFrom(ctx); call != nil {
			call.typed, call.answeredBy, call.escalation = false, metadata.Provider, nil
		}
		return
	}
	if typed {
		metadata.Custom[customDecisionTyped] = true
	}
	if escalation := resp.DecisionEscalation; escalation != nil {
		metadata.Custom[customDecisionAskedFirst] = escalation.Provider
		metadata.Custom[customDecisionEscalated] = escalation.Reason
		if escalation.ConfidenceReported {
			metadata.Custom[customDecisionAskedFirstConfidence] = escalation.ModelConfidence
		}
	}
	if call := decisionCallFrom(ctx); call != nil {
		call.typed, call.answeredBy, call.escalation = typed, metadata.Provider, resp.DecisionEscalation
	}
}

// dec12RenderQuestions is the questions as the message shows them, each under
// its key, with its text and its options quoted as JSON.
func dec12RenderQuestions(questions []assessQuestion) (string, error) {
	size := len("Questions:\n")
	for _, q := range questions {
		size += len(q.ask) + 60
		for _, label := range q.labels {
			size += len(label) + 12
		}
	}
	var message strings.Builder
	message.Grow(size)
	message.WriteString("Questions:\n")
	for i, q := range questions {
		message.WriteString(assessKey(i))
		switch q.kind {
		case questionChoice:
			message.WriteString(" (one of the options below): ")
		case questionRating:
			message.WriteString(" (a rating from " + strconv.Itoa(q.low) + " to " + strconv.Itoa(q.high) + "): ")
		default:
			message.WriteString(" (yes or no): ")
		}
		if err := dec12Quote(&message, q.ask); err != nil {
			return "", types.Classified(types.KindInvalidRequest, err)
		}
		message.WriteByte('\n')
		if q.kind != questionChoice {
			continue
		}
		for n, label := range q.labels {
			message.WriteString("  " + strconv.Itoa(n+1) + ": ")
			if err := dec12Quote(&message, label); err != nil {
				return "", types.Classified(types.KindInvalidRequest, err)
			}
			message.WriteByte('\n')
		}
	}
	return message.String(), nil
}

// dec12WriteSubject writes the subject as JSON for the message. Text is the
// usual subject, and is written without the encoder.
func dec12WriteSubject(shown any) (string, error) {
	if text, isText := shown.(string); isText {
		var out strings.Builder
		out.Grow(len(text) + 2)
		if err := dec12Quote(&out, text); err != nil {
			return "", err
		}
		return out.String(), nil
	}
	return dec12Marshal(shown)
}

// dec12Encoder is an encoder that writes as promptJSON does, with the buffer it
// writes to.
type dec12Encoder struct {
	buf bytes.Buffer
	enc *json.Encoder
}

// dec12Encoders keeps encoders for reuse: promptJSON makes a buffer and an
// encoder for every value it writes, and a decision writes its subject, and
// every piece of text that needs an escape, on every call.
var dec12Encoders = sync.Pool{New: func() any {
	encoder := &dec12Encoder{}
	encoder.enc = json.NewEncoder(&encoder.buf)
	encoder.enc.SetEscapeHTML(false)
	return encoder
}}

// dec12MostKept is the largest buffer that goes back into the pool, so that one
// large subject does not leave its memory held.
const dec12MostKept = 64 << 10

// dec12Marshal is promptJSON's output as a string: "<", ">" and "&" left as
// they are, and no newline at the end.
func dec12Marshal(value any) (string, error) {
	encoder := dec12Encoders.Get().(*dec12Encoder)
	encoder.buf.Reset()
	err := encoder.enc.Encode(value)
	written := ""
	if err == nil {
		written = strings.TrimSuffix(encoder.buf.String(), "\n")
	}
	if encoder.buf.Cap() <= dec12MostKept {
		dec12Encoders.Put(encoder)
	}
	return written, err
}

// dec12PlainInteger reads a short run of digits, with a minus sign in front of
// it or not, as the whole number it is. It reports false for anything it does
// not read the way the decoder does: a fraction, an exponent, a leading zero,
// more digits than it keeps, or no digits.
func dec12PlainInteger(raw []byte) (int, bool) {
	digits := bytes.TrimSpace(raw)
	negative := len(digits) > 0 && digits[0] == '-'
	if negative {
		digits = digits[1:]
	}
	if len(digits) == 0 || len(digits) > 9 {
		return 0, false
	}
	if digits[0] == '0' {
		return 0, len(digits) == 1 && !negative
	}
	number := 0
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		number = number*10 + int(c-'0')
	}
	if negative {
		number = -number
	}
	return number, true
}

// dec12Quote writes text as a JSON string, byte for byte as promptJSON does.
//
// Text made of plain printable ASCII needs no escape, and is the case that
// matters: every question, every option and nearly every subject. Anything
// else, a quote, a backslash, a control character or a character outside ASCII,
// goes through the encoder, so that the escapes are the encoder's and not a
// second opinion about them.
func dec12Quote(out *strings.Builder, text string) error {
	for i := 0; i < len(text); i++ {
		if c := text[i]; c < 0x20 || c == '"' || c == '\\' || c >= utf8.RuneSelf {
			written, err := dec12Marshal(text)
			if err != nil {
				return err
			}
			out.WriteString(written)
			return nil
		}
	}
	out.WriteByte('"')
	out.WriteString(text)
	out.WriteByte('"')
	return nil
}

// dec12SchemaLimit is how many declared answers are kept. Questions of the same
// kinds share one, so a program has a handful; a program that asks questions of
// a new shape every time would otherwise add one for each, and those past the
// limit are built for the call, as they were before there was a cache.
const dec12SchemaLimit = 256

// dec12SchemaCache keeps the declared answer for each set of question kinds.
//
// What is declared depends on the kinds alone, the number of options of a
// choice and the ends of a rating's scale, and holds none of the caller's text,
// which is what makes it safe to keep and to share. Reads take no lock: the map
// is replaced whole when an entry is added, and added to only until the limit.
type dec12SchemaCache struct {
	mu      sync.Mutex
	entries atomic.Pointer[map[string]map[string]any]
}

var dec12Schemas dec12SchemaCache

// dec12SchemaKey writes what the declared answer depends on: for each question
// its kind and the number of options or the ends of the scale, and whether the
// model's confidence is asked for.
func dec12SchemaKey(dst []byte, questions []assessQuestion, confidence bool) []byte {
	for _, q := range questions {
		switch q.kind {
		case questionChoice:
			dst = append(dst, 'c')
			dst = strconv.AppendInt(dst, int64(len(q.labels)), 10)
		case questionRating:
			dst = append(dst, 'r')
			dst = strconv.AppendInt(dst, int64(q.low), 10)
			dst = append(dst, ':')
			dst = strconv.AppendInt(dst, int64(q.high), 10)
		default:
			dst = append(dst, 'y')
		}
		dst = append(dst, ';')
	}
	if confidence {
		dst = append(dst, 'C')
	}
	return dst
}

// declare is the declared answer for questions of these kinds.
func (cache *dec12SchemaCache) declare(questions []assessQuestion, confidence bool) map[string]any {
	var scratch [96]byte
	key := dec12SchemaKey(scratch[:0], questions, confidence)
	if entries := cache.entries.Load(); entries != nil {
		if schema, found := (*entries)[string(key)]; found {
			return schema
		}
	}
	schema := assessSchema(questions, confidence)
	cache.store(string(key), schema)
	return schema
}

// store keeps a declared answer, unless the cache is full. When two calls add
// the same kind at once the first stays and the second is dropped, and both
// calls hold a schema that declares the same answer.
func (cache *dec12SchemaCache) store(key string, schema map[string]any) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var current map[string]map[string]any
	if entries := cache.entries.Load(); entries != nil {
		current = *entries
	}
	if _, kept := current[key]; kept || len(current) >= dec12SchemaLimit {
		return
	}
	next := make(map[string]map[string]any, len(current)+1)
	for k, v := range current {
		next[k] = v
	}
	next[key] = schema
	cache.entries.Store(&next)
}

// size is how many declared answers are kept.
func (cache *dec12SchemaCache) size() int {
	if entries := cache.entries.Load(); entries != nil {
		return len(*entries)
	}
	return 0
}

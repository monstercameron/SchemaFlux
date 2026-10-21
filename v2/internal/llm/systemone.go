package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The System One protocol: TypeSafe AI's decision endpoint, which its Jev model
// answers and which OpenRouter and a LiteLLM proxy pass through unchanged.
//
// A request is a state and a set of typed questions, and the answer is one
// typed answer for each question:
//
//	POST {base}{path}      Authorization: Bearer {key}
//	{"model":M,"questions":{"q1":Q,...},"state":S}
//
//	yes or no   {"type":"noul","instructions":ASK}
//	choice      {"type":"choice","instructions":ASK,"criteria":{"o1":TEXT,...}}
//	scale       {"type":"score","instructions":ASK,"criteria":[LEVEL,...]}
//
//	{"answers":{"q1":A,...},"model":"jev-1.13.0","usage":{"input_tokens":n,"output_tokens":n}}
//
//	noul     {"type":"noul","noul":P}                     the probability of yes
//	choice   {"type":"choice","choice":"o2","confidence":C}
//	score    {"type":"score","score":F,"confidence":C}    a position on the levels, counted from 0
//
// It writes no text and has no streaming. The three places it is served differ
// in the base URL, the path, the credential and the name the model goes by, and
// in nothing else, so there is one implementation and a preset for each.
//
// The body is the one the owner's prototype client sent when the model was
// measured (2026-10-01), key for key and in the same order: it is built to
// encode as that client's map did.

// SystemOnePreset is where one System One endpoint is and how it is addressed.
type SystemOnePreset struct {
	// Name is the provider's name: what a client is configured with, what a
	// data policy allows, and what a call's record says answered.
	Name string

	// BaseURL is the endpoint's base when the configuration names none. Empty
	// means there is no default and the configuration has to name one, as for a
	// proxy the caller runs.
	BaseURL string

	// Path is appended to the base URL.
	Path string

	// AuthHeader and AuthPrefix are the header the key is sent in and what goes
	// in front of it: "Authorization" and "Bearer " for all three presets.
	AuthHeader string
	AuthPrefix string

	// Model is the model sent when the request names none of this endpoint's.
	Model string

	// ModelFamily is how this endpoint's models are told from another
	// provider's: a requested model is sent as asked when its name, without a
	// gateway's "vendor/" or "~vendor/" in front, starts with this.
	ModelFamily string

	// KeyVariables and BaseURLVariables are the environment variables the key
	// and the base URL are read from when the configuration has none, in the
	// order they are tried. SCHEMAFLUX_API_KEY is tried after the key's own.
	KeyVariables     []string
	BaseURLVariables []string

	// Limits is what the endpoint takes.
	Limits DecisionLimits
}

// jevLimits is what Jev takes, from TypeSafe's reference: 255 options to a
// choice, 2 to 10 levels to a score, and a state of 32 thousand tokens. The
// number of questions in one request has no stated limit; the owner's probe
// asked 400 in one request and all were answered.
var jevLimits = DecisionLimits{MaxOptions: 255, MaxLevels: 10, MaxStateTokens: 32000}

// The presets that ship. The key variables are the vendor's own name for the
// key, with this library's prefixed form in front of it, as for every other
// provider.
var (
	// SystemOneTypeSafe is TypeSafe's own endpoint.
	SystemOneTypeSafe = SystemOnePreset{
		Name:             "jev",
		BaseURL:          "https://api.typesafe.ai",
		Path:             "/v1/systemone",
		AuthHeader:       "Authorization",
		AuthPrefix:       "Bearer ",
		Model:            "jev-latest",
		ModelFamily:      "jev",
		KeyVariables:     []string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"},
		BaseURLVariables: []string{"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL"},
		Limits:           jevLimits,
	}

	// SystemOneOpenRouter is the same protocol at OpenRouter, under OpenRouter's
	// key and its name for the model.
	SystemOneOpenRouter = SystemOnePreset{
		Name:             "jev-openrouter",
		BaseURL:          "https://openrouter.ai/api",
		Path:             "/v1/systemone",
		AuthHeader:       "Authorization",
		AuthPrefix:       "Bearer ",
		Model:            "~typesafe/jev-latest",
		ModelFamily:      "jev",
		KeyVariables:     []string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"},
		BaseURLVariables: []string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"},
		Limits:           jevLimits,
	}

	// SystemOneLiteLLM is the same protocol behind a LiteLLM proxy's TypeSafe
	// pass-through. The proxy is the caller's own, so there is no default base
	// URL; the key is the proxy's virtual key, and the proxy adds TypeSafe's.
	SystemOneLiteLLM = SystemOnePreset{
		Name:             "jev-litellm",
		Path:             "/typesafe/v1/systemone",
		AuthHeader:       "Authorization",
		AuthPrefix:       "Bearer ",
		Model:            "jev-latest",
		ModelFamily:      "jev",
		KeyVariables:     []string{"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY"},
		BaseURLVariables: []string{"SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"},
		Limits:           jevLimits,
	}
)

// sharedKeyVariable is the key of any provider whose own variables are not set.
const sharedKeyVariable = "SCHEMAFLUX_API_KEY"

// SystemOneProvider is a decision provider that speaks the System One protocol.
// It answers a request that carries a Decision and refuses any other.
type SystemOneProvider struct {
	preset     SystemOnePreset
	config     ProviderConfig
	endpoint   string
	httpClient *http.Client
}

// NewSystemOneProvider builds a decision provider for one preset.
//
// The key is the configuration's, and with none there the first of the
// preset's own variables that is set, and then SCHEMAFLUX_API_KEY. The base URL
// is the configuration's, then the first of the preset's variables that is set,
// then the preset's default. A provider with no key or no base URL is a
// configuration error here, where the mistake was made, and names the variables
// and never a value.
func NewSystemOneProvider(preset SystemOnePreset, config ProviderConfig) (*SystemOneProvider, error) {
	name := NormalizeProviderName(preset.Name)
	if name == "" || preset.Path == "" || preset.Model == "" || preset.AuthHeader == "" {
		return nil, types.Newf(types.KindConfiguration, "llm.configure", "a System One preset needs a name, a path, a model and the header its key is sent in")
	}
	preset.Name = name

	if strings.TrimSpace(config.APIKey) == "" {
		config.APIKey = firstSetVariable(append(append([]string{}, preset.KeyVariables...), sharedKeyVariable)) // secret-scan: allow
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, types.Newf(types.KindConfiguration, "llm.configure", "%s API key is required: set it on the provider's configuration, or in %s",
			name, strings.Join(append(append([]string{}, preset.KeyVariables...), sharedKeyVariable), ", "))
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		config.BaseURL = firstSetVariable(preset.BaseURLVariables)
	}
	config = settleBaseURL(config, preset.BaseURL)
	if config.BaseURL == "" {
		return nil, types.Newf(types.KindConfiguration, "llm.configure", "%s has no base URL: set it on the provider's configuration, or in %s",
			name, strings.Join(preset.BaseURLVariables, ", "))
	}

	if err := validateConfiguredEndpoint(name, config); err != nil {
		return nil, err
	}
	if err := validateCostEnvironment(); err != nil {
		return nil, err
	}
	if err := refuseServiceTier(name, config); err != nil {
		return nil, err
	}

	return &SystemOneProvider{
		preset:     preset,
		config:     config,
		endpoint:   config.BaseURL + preset.Path,
		httpClient: resolveHTTPClient(config),
	}, nil
}

// firstSetVariable is the value of the first of names that is set to something,
// without the space around it.
func firstSetVariable(names []string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// systemOneFactory is the registry's factory for a preset. An error comes back
// beside a nil Provider, and not beside a typed nil pointer.
func systemOneFactory(preset SystemOnePreset) ProviderFactory {
	return func(config ProviderConfig) (Provider, error) {
		provider, err := NewSystemOneProvider(preset, config)
		if err != nil {
			return nil, err
		}
		return provider, nil
	}
}

// Name is the preset's name.
func (provider *SystemOneProvider) Name() string { return provider.preset.Name }

// RetryPolicy returns provider retry settings.
func (provider *SystemOneProvider) RetryPolicy() (int, time.Duration) {
	return provider.config.MaxRetries, provider.config.RetryBackoff
}

// EstimateCost is the estimate every provider makes. The endpoint bills the
// input and nothing for the answer.
func (provider *SystemOneProvider) EstimateCost(req CompletionRequest) float64 {
	return estimateCost(provider.preset.Name, req)
}

// DecisionLimits is what the endpoint takes.
func (provider *SystemOneProvider) DecisionLimits() DecisionLimits { return provider.preset.Limits }

// Warm opens the connection the provider's calls will use.
func (provider *SystemOneProvider) Warm(ctx context.Context) error {
	return warmConnection(ctx, provider.httpClient, provider.Name(), provider.config.BaseURL)
}

// ModelSentFor returns the model this provider sends for a requested one: the
// requested name when it is one of the endpoint's own models, and the preset's
// model for any other name and for none.
//
// A decision provider is most often one half of a pair with a generating
// provider (mw.Decisions), and the request it is handed names the generating
// provider's model, which this endpoint cannot serve. A caller who wants a
// particular version of the decision model names it, and that name is sent.
//
// It is exported so that the route can be known before the call: a data policy
// is checked against the model that is sent (see ModelSentBy).
func (provider *SystemOneProvider) ModelSentFor(requested string) string {
	name := strings.TrimSpace(requested)
	family := strings.ToLower(strings.TrimSpace(provider.preset.ModelFamily))
	if name == "" || family == "" {
		return provider.preset.Model
	}
	bare := strings.ToLower(strings.TrimPrefix(name, "~"))
	if slash := strings.LastIndex(bare, "/"); slash >= 0 {
		bare = bare[slash+1:]
	}
	if strings.HasPrefix(bare, family) {
		return name
	}
	return provider.preset.Model
}

// The wire shapes. The request's fields are in the order a map with these keys
// encodes in, which is the order the prototype client's body had.
type systemOneRequest struct {
	Model     string                       `json:"model"`
	Questions map[string]systemOneQuestion `json:"questions"`
	State     json.RawMessage              `json:"state"`
}

type systemOneQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	// Criteria is a map from option key to description for a choice, a list of
	// levels, lowest first, for a score, and absent for a noul.
	Criteria any `json:"criteria,omitempty"`
}

type systemOneResponse struct {
	Answers map[string]systemOneAnswer `json:"answers"`
	Model   string                     `json:"model"`
	// Provider is the vendor a gateway says served the request. It is decoded
	// as any, as the chat dialect's is: only a string is read.
	Provider any `json:"provider"`
	Usage    struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// systemOneAnswer is one answer. Every value is a pointer, so that one that is
// missing is told from one that is zero: a noul of 0 is "certainly no", and a
// noul that is not there is no answer.
type systemOneAnswer struct {
	Type   string   `json:"type"`
	Choice *string  `json:"choice"`
	Noul   *float64 `json:"noul"`
	Score  *float64 `json:"score"`
	// ModelConfidence is the wire's "confidence": the model's own claim about a
	// choice or a score.
	ModelConfidence *float64 `json:"confidence"`
}

// systemOneQuestionKey and systemOneOptionKey are the names a question and an
// option go by on the wire: "q1", "q2" and "o1", "o2", counted from one as the
// generating prompt numbers them. A caller's label is never a key: it can be
// any text, of any length, and the answer is matched back by position.
func systemOneQuestionKey(i int) string { return "q" + strconv.Itoa(i+1) }
func systemOneOptionKey(i int) string   { return "o" + strconv.Itoa(i+1) }

// systemOneBody writes a decision as the protocol's request body.
//
// The guidance goes into the instructions of every question and not into the
// state. The state is the caller's data, and this model has been seen to be
// talked out of a judgment by text in its state; guidance placed there would be
// one more piece of data, with the same standing as whatever the data says of
// itself. Criteria are per option, and a yes or no has none, so the
// instructions are the one place every kind of question reads.
func systemOneBody(model string, decision *Decision) ([]byte, error) {
	body := systemOneRequest{
		Model:     model,
		Questions: make(map[string]systemOneQuestion, len(decision.Questions)),
		State:     json.RawMessage(decision.State),
	}
	// The state is JSON as the operations write it. One that is not, from a
	// request built by hand, is sent as the text it is.
	if !json.Valid(body.State) {
		quoted, err := json.Marshal(decision.State)
		if err != nil {
			return nil, err
		}
		body.State = quoted
	}

	guidance := strings.TrimSpace(decision.Guidance)
	for i, question := range decision.Questions {
		wire := systemOneQuestion{Instructions: question.Ask}
		if guidance != "" {
			wire.Instructions += "\nGuidance: " + guidance
		}
		switch question.Kind {
		case DecisionYesNo:
			wire.Type = "noul"
		case DecisionChoice:
			wire.Type = "choice"
			criteria := make(map[string]string, len(question.Options))
			for n, label := range question.Options {
				described := label
				if n < len(question.Criteria) && strings.TrimSpace(question.Criteria[n]) != "" {
					described = label + ": " + question.Criteria[n]
				}
				criteria[systemOneOptionKey(n)] = described
			}
			wire.Criteria = criteria
		case DecisionScale:
			wire.Type = "score"
			var levels []string
			for level := question.Low; ; level++ {
				text := strconv.Itoa(level)
				switch level {
				case question.Low:
					text += " (lowest)"
				case question.High:
					text += " (highest)"
				}
				levels = append(levels, text)
				// Compared before the step, so that a scale that ends at the
				// largest int does not wrap past its end.
				if level == question.High {
					break
				}
			}
			wire.Criteria = levels
		}
		body.Questions[systemOneQuestionKey(i)] = wire
	}
	return json.Marshal(body)
}

// Complete asks the endpoint the questions of the request's decision.
//
// A request with no decision, a decision past the endpoint's limits, and a
// request that sets something the protocol cannot carry are refused before
// anything is sent. The prompts, the response format and the schema of the
// request are the same decision put to a generating model, and are not sent.
func (provider *SystemOneProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	name := provider.preset.Name
	model := provider.ModelSentFor(req.Model)

	if err := provider.preset.Limits.Check(name, model, req.Decision); err != nil {
		return CompletionResponse{}, err
	}
	if err := unsupportedRequestFields(req, name, "Tools", "ToolChoice", "WebSearch", "Messages"); err != nil {
		return CompletionResponse{}, err
	}
	if err := effortNotSupported(req, name); err != nil {
		return CompletionResponse{}, err
	}

	payload, err := systemOneBody(model, req.Decision)
	if err != nil {
		// The encoder's error can quote what it could not encode.
		return CompletionResponse{}, &types.OperationError{Kind: types.KindInvalidRequest, Op: "llm.decide", Provider: name, Model: model,
			Message: "the decision could not be written as JSON"}
	}

	ctx, cancel := callContext(ctx, provider.config)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint, bytes.NewReader(payload))
	if err != nil {
		// The error quotes the URL, and a URL can hold a credential.
		return CompletionResponse{}, &types.OperationError{Kind: types.KindConfiguration, Op: "llm.configure", Provider: name, Model: model,
			Message: "the base URL is not one a request can be sent to"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(provider.preset.AuthHeader, provider.preset.AuthPrefix+provider.config.APIKey)

	resp, clock, err := doTimed(provider.httpClient, httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%s request failed: %w", name, withoutURL(err))
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		// Typed, with the wait a throttled answer asks for, the key removed and
		// the body withheld: the body of a failure can echo the request.
		return CompletionResponse{}, statusError(name, model, resp, provider.config)
	}

	raw, err := readBounded(resp.Body, maxResponseBodyBytes)
	if err != nil {
		if types.KindOf(err) != types.KindUnknown {
			return CompletionResponse{}, err
		}
		return CompletionResponse{}, fmt.Errorf("%s: failed to read response: %w", name, withoutURL(err))
	}

	malformed := func(message string) (CompletionResponse, error) {
		return CompletionResponse{}, &types.OperationError{Kind: types.KindMalformedOutput, Op: "llm.decide", Provider: name, Model: model, Message: message}
	}
	var decoded systemOneResponse
	// The decoder's error quotes the bytes it stopped at, which are the body's.
	if json.Unmarshal(raw, &decoded) != nil {
		return malformed("the response is not a System One answer")
	}
	if decoded.Answers == nil {
		return malformed("the response is not a System One answer: it has no answers")
	}

	answers, err := systemOneAnswers(req.Decision, decoded.Answers)
	if err != nil {
		return malformed(err.Error())
	}

	answeredModel := strings.TrimSpace(decoded.Model)
	if answeredModel == "" {
		answeredModel = model
	}
	upstream, _ := decoded.Provider.(string)

	return CompletionResponse{
		Provider:         name,
		UpstreamProvider: strings.TrimSpace(upstream),
		Model:            answeredModel,
		Usage: types.TokenUsage{
			PromptTokens:     decoded.Usage.InputTokens,
			CompletionTokens: decoded.Usage.OutputTokens,
			TotalTokens:      decoded.Usage.InputTokens + decoded.Usage.OutputTokens,
			InputTokens:      decoded.Usage.InputTokens,
			OutputTokens:     decoded.Usage.OutputTokens,
		},
		Timing:          clock.timing(),
		DecisionAnswers: answers,
	}, nil
}

// withoutURL takes the URL out of a transport error. net/http reports a failed
// request as "Post <url>: <cause>", and a base URL can hold a credential, in its
// user part or in a query. The cause is kept, so a timeout is still a timeout.
func withoutURL(err error) error {
	var transport *url.Error
	if errors.As(err, &transport) && transport.Err != nil {
		return transport.Err
	}
	return err
}

// systemOneAnswers reads the protocol's answers into typed ones, one for each
// question and in the questions' order. Every question has to be answered, as
// the type it was asked as, with a value that is one: a probability from 0 to
// 1, the key of an option that was offered, a position on the levels. The
// errors name the question by position and repeat nothing of the answer.
func systemOneAnswers(decision *Decision, wire map[string]systemOneAnswer) ([]DecisionAnswer, error) {
	probability := func(value *float64) (float64, bool) {
		if value == nil || !(*value >= 0 && *value <= 1) {
			return 0, false
		}
		return *value, true
	}

	answers := make([]DecisionAnswer, len(decision.Questions))
	for i, question := range decision.Questions {
		position := "question " + strconv.Itoa(i+1)
		got, answered := wire[systemOneQuestionKey(i)]
		if !answered {
			return nil, errors.New("the response does not answer " + position)
		}
		answer := DecisionAnswer{Kind: question.Kind}

		// A confidence that is there has to be a probability. One that is not
		// there was not reported, and nothing stands in for it.
		if got.ModelConfidence != nil {
			confidence, ok := probability(got.ModelConfidence)
			if !ok {
				return nil, errors.New("the confidence reported for " + position + " is not a number from 0 to 1")
			}
			answer.ModelConfidence, answer.ConfidenceReported = confidence, true
		}

		switch question.Kind {
		case DecisionYesNo:
			yes, ok := probability(got.Noul)
			if got.Type != "noul" || !ok {
				return nil, errors.New("the answer to " + position + " is not the probability of yes that was asked for")
			}
			// The probability reported is that of yes. The answer is the likelier
			// side, and the probability of the answer is that side's.
			answer.Yes = yes >= 0.5
			answer.ModelConfidence, answer.ConfidenceReported = yes, true
			if !answer.Yes {
				answer.ModelConfidence = 1 - yes
			}
		case DecisionChoice:
			if got.Type != "choice" || got.Choice == nil {
				return nil, errors.New("the answer to " + position + " is not the choice that was asked for")
			}
			chosen := -1
			for n := range question.Options {
				if systemOneOptionKey(n) == *got.Choice {
					chosen = n
					break
				}
			}
			if chosen < 0 {
				return nil, errors.New("the answer to " + position + " is not one of its " + strconv.Itoa(len(question.Options)) + " options")
			}
			answer.Label = question.Options[chosen]
		case DecisionScale:
			if got.Type != "score" || got.Score == nil || math.IsNaN(*got.Score) || math.IsInf(*got.Score, 0) {
				return nil, errors.New("the answer to " + position + " is not the score that was asked for")
			}
			// The score is a position on the levels, counted from zero, and can
			// sit between two of them: the nearest level is the answer, as the
			// prototype client read it. A position past either end is refused
			// and not taken for the end.
			index := math.Round(*got.Score)
			if index < 0 || index > float64(question.High)-float64(question.Low) {
				return nil, errors.New("the answer to " + position + " is not a level on its scale")
			}
			answer.Level = question.Low + int(index)
		}
		answers[i] = answer
	}
	return answers, nil
}

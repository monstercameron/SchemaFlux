package schemafluxtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// cassetteVersion is the version a cassette file declares. A file of another
// version is refused: a reader that guessed at the meaning of a newer file would
// replay a different exchange from the one recorded.
const cassetteVersion = 1

// The wire types are what a cassette is in a file. They have a tag for every
// member, in the order the file shows them, so that the bytes written for an
// exchange are the same each time and the files can be read and diffed.
type (
	cassetteWire struct {
		Version  int           `json:"version"`
		Provider string        `json:"provider"`
		Request  requestWire   `json:"request"`
		Response *responseWire `json:"response,omitempty"`
		Failure  *failureWire  `json:"failure,omitempty"`
	}

	requestWire struct {
		Model           string        `json:"model,omitempty"`
		System          string        `json:"system,omitempty"`
		User            string        `json:"user,omitempty"`
		Temperature     float64       `json:"temperature,omitempty"`
		MaxTokens       int           `json:"max_tokens,omitempty"`
		Format          string        `json:"format,omitempty"`
		SchemaName      string        `json:"schema_name,omitempty"`
		Schema          string        `json:"schema,omitempty"`
		Effort          string        `json:"effort,omitempty"`
		EffortPreferred bool          `json:"effort_preferred,omitempty"`
		WebSearch       bool          `json:"web_search,omitempty"`
		Decision        *decisionWire `json:"decision,omitempty"`
		Tools           []toolWire    `json:"tools,omitempty"`
		Messages        []messageWire `json:"messages,omitempty"`
	}

	toolWire struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	}

	messageWire struct {
		Role       string         `json:"role"`
		Content    string         `json:"content,omitempty"`
		ToolCalls  []toolCallWire `json:"tool_calls,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
	}

	decisionWire struct {
		State     string         `json:"state,omitempty"`
		Guidance  string         `json:"guidance,omitempty"`
		Questions []questionWire `json:"questions,omitempty"`
	}

	questionWire struct {
		Kind     string   `json:"kind"`
		Ask      string   `json:"ask,omitempty"`
		Options  []string `json:"options,omitempty"`
		Criteria []string `json:"criteria,omitempty"`
		Low      int      `json:"low,omitempty"`
		High     int      `json:"high,omitempty"`
	}

	responseWire struct {
		Content      string          `json:"content"`
		Usage        usageWire       `json:"usage"`
		Model        string          `json:"model,omitempty"`
		Provider     string          `json:"provider,omitempty"`
		Upstream     string          `json:"upstream,omitempty"`
		FinishReason string          `json:"finish_reason,omitempty"`
		ServiceTier  string          `json:"service_tier,omitempty"`
		Cached       bool            `json:"cached,omitempty"`
		Hedged       bool            `json:"hedged,omitempty"`
		Answers      []answerWire    `json:"answers,omitempty"`
		Escalation   *escalationWire `json:"escalation,omitempty"`
		ToolCalls    []toolCallWire  `json:"tool_calls,omitempty"`
	}

	usageWire struct {
		Input      int `json:"input,omitempty"`
		Output     int `json:"output,omitempty"`
		Total      int `json:"total,omitempty"`
		Cached     int `json:"cached,omitempty"`
		CacheWrite int `json:"cache_write,omitempty"`
		Reasoning  int `json:"reasoning,omitempty"`
	}

	answerWire struct {
		Kind               string  `json:"kind"`
		Yes                bool    `json:"yes,omitempty"`
		Label              string  `json:"label,omitempty"`
		Level              int     `json:"level,omitempty"`
		ModelConfidence    float64 `json:"model_confidence,omitempty"`
		ConfidenceReported bool    `json:"confidence_reported,omitempty"`
	}

	escalationWire struct {
		Provider           string  `json:"provider,omitempty"`
		Model              string  `json:"model,omitempty"`
		Reason             string  `json:"reason,omitempty"`
		ModelConfidence    float64 `json:"model_confidence,omitempty"`
		ConfidenceReported bool    `json:"confidence_reported,omitempty"`
	}

	toolCallWire struct {
		ID        string `json:"id,omitempty"`
		Name      string `json:"name"`
		Arguments string `json:"arguments,omitempty"`
	}

	failureWire struct {
		Kind       string `json:"kind"`
		Status     int    `json:"status,omitempty"`
		RetryAfter string `json:"retry_after,omitempty"`
	}
)

// decisionKindNames are the names a decision's kinds have in a file. A kind that
// has none is written as its number, so that a decision that is not well formed
// and a provider that was sent one still replay as they were.
var decisionKindNames = map[schemaflux.DecisionKind]string{
	schemaflux.DecisionYesNo:  "yes_no",
	schemaflux.DecisionChoice: "choice",
	schemaflux.DecisionScale:  "scale",
}

func decisionKindName(k schemaflux.DecisionKind) string {
	if name, ok := decisionKindNames[k]; ok {
		return name
	}
	return strconv.Itoa(int(k))
}

func decisionKindOf(name string) (schemaflux.DecisionKind, bool) {
	for kind, known := range decisionKindNames {
		if known == name {
			return kind, true
		}
	}
	n, err := strconv.ParseUint(name, 10, 8)
	return schemaflux.DecisionKind(n), err == nil
}

// kindOfName is the kind a file names, or false for a name no kind has.
func kindOfName(name string) (schemaflux.Kind, bool) {
	if name == "" || name == schemaflux.Kind(0).Error() {
		return 0, false
	}
	for k := schemaflux.Kind(1); k != 0; k++ {
		if k.Error() == name {
			return k, true
		}
	}
	return 0, false
}

// wire is the file form of the cassette.
func (c Cassette) wire() cassetteWire {
	w := cassetteWire{Version: cassetteVersion, Provider: c.Provider, Request: requestWireOf(c.Request)}
	if c.Response != nil {
		r := responseWireOf(*c.Response)
		w.Response = &r
	}
	if c.Failure != nil {
		f := failureWireOf(*c.Failure)
		w.Failure = &f
	}
	return w
}

// failureWireOf is the file form of a failure. A failure of no kind is what the
// library takes it for, an unavailable provider, and is written as that, since a
// file cannot name a kind that no kind has.
func failureWireOf(f CassetteFailure) failureWire {
	kind := f.Kind
	if kind == 0 {
		kind = schemaflux.ErrUnavailable
	}
	w := failureWire{Kind: kind.Error(), Status: f.Status}
	if f.RetryAfter != 0 {
		w.RetryAfter = f.RetryAfter.String()
	}
	return w
}

func requestWireOf(r CassetteRequest) requestWire {
	w := requestWire{
		Model: r.Model, System: r.System, User: r.User, Temperature: r.Temperature, MaxTokens: r.MaxTokens,
		Format: r.Format, SchemaName: r.SchemaName, Schema: r.Schema, Effort: string(r.Effort),
		EffortPreferred: r.EffortPreferred, WebSearch: r.WebSearch, Tools: toolsWireOf(r.Tools), Messages: messagesWireOf(r.Messages),
	}
	if d := r.Decision; d != nil {
		w.Decision = &decisionWire{State: d.State, Guidance: d.Guidance}
		for _, q := range d.Questions {
			w.Decision.Questions = append(w.Decision.Questions, questionWire{
				Kind: decisionKindName(q.Kind), Ask: q.Ask, Options: q.Options, Criteria: q.Criteria, Low: q.Low, High: q.High,
			})
		}
	}
	return w
}

// toolsWireOf is the file form of the declared tools.
func toolsWireOf(tools []schemaflux.Tool) []toolWire {
	var out []toolWire
	for _, t := range tools {
		out = append(out, toolWire{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}

// messagesWireOf is the file form of a conversation. A call's arguments are
// written as the text they are.
func messagesWireOf(messages []schemaflux.Message) []messageWire {
	var out []messageWire
	for _, m := range messages {
		w := messageWire{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			w.ToolCalls = append(w.ToolCalls, toolCallWire{ID: c.ID, Name: c.Name, Arguments: string(c.Arguments)})
		}
		out = append(out, w)
	}
	return out
}

// toolsOfWire is the tools a file declares.
func toolsOfWire(tools []toolWire) []schemaflux.Tool {
	var out []schemaflux.Tool
	for _, t := range tools {
		out = append(out, schemaflux.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}

// messagesOfWire is the conversation a file holds.
func messagesOfWire(messages []messageWire) []schemaflux.Message {
	var out []schemaflux.Message
	for _, w := range messages {
		m := schemaflux.Message{Role: w.Role, Content: w.Content, ToolCallID: w.ToolCallID}
		for _, c := range w.ToolCalls {
			call := schemaflux.ToolCall{ID: c.ID, Name: c.Name}
			if c.Arguments != "" {
				call.Arguments = json.RawMessage(c.Arguments)
			}
			m.ToolCalls = append(m.ToolCalls, call)
		}
		out = append(out, m)
	}
	return out
}

func responseWireOf(r CassetteResponse) responseWire {
	w := responseWire{
		Content: r.Content,
		Usage: usageWire{
			Input: r.Usage.Input, Output: r.Usage.Output, Total: r.Usage.Total,
			Cached: r.Usage.Cached, CacheWrite: r.Usage.CacheWrite, Reasoning: r.Usage.Reasoning,
		},
		Model: r.Model, Provider: r.Provider, Upstream: r.Upstream, FinishReason: r.FinishReason,
		ServiceTier: r.ServiceTier, Cached: r.Cached, Hedged: r.Hedged,
	}
	for _, a := range r.Answers {
		w.Answers = append(w.Answers, answerWire{
			Kind: decisionKindName(a.Kind), Yes: a.Yes, Label: a.Label, Level: a.Level,
			ModelConfidence: a.ModelConfidence, ConfidenceReported: a.ConfidenceReported,
		})
	}
	if e := r.Escalation; e != nil {
		w.Escalation = &escalationWire{
			Provider: e.Provider, Model: e.Model, Reason: e.Reason,
			ModelConfidence: e.ModelConfidence, ConfidenceReported: e.ConfidenceReported,
		}
	}
	for _, call := range r.ToolCalls {
		w.ToolCalls = append(w.ToolCalls, toolCallWire(call))
	}
	return w
}

// encodeJSON is v as JSON without the escaping of <, > and & that Marshal does,
// which would make every prompt that holds markup unreadable in a file, and
// without the newline an Encoder adds.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// MarshalJSON writes the cassette as the member list of a cassette file, in a
// fixed order and with its kinds by name. A number that JSON cannot hold (not a
// number, or infinite) is an error.
func (c Cassette) MarshalJSON() ([]byte, error) { return encodeJSON(c.wire()) }

// UnmarshalJSON reads what MarshalJSON writes. It refuses a document of another
// version, a member it does not know, a kind it has no name for and anything
// after the document, so that nothing is replayed as something it was not.
func (c *Cassette) UnmarshalJSON(data []byte) error {
	parsed, err := parseCassette(data)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// MarshalJSON writes the request as it is written inside a cassette file.
func (r CassetteRequest) MarshalJSON() ([]byte, error) { return encodeJSON(requestWireOf(r)) }

// MarshalJSON writes the response as it is written inside a cassette file.
func (r CassetteResponse) MarshalJSON() ([]byte, error) { return encodeJSON(responseWireOf(r)) }

// MarshalJSON writes the failure as it is written inside a cassette file, its
// kind by name and its wait as a duration string such as "1.5s".
func (f CassetteFailure) MarshalJSON() ([]byte, error) { return encodeJSON(failureWireOf(f)) }

// MarshalJSON writes the tool call as it is written inside a cassette file.
func (c CassetteToolCall) MarshalJSON() ([]byte, error) {
	return encodeJSON(toolCallWire(c))
}

// malformed is a cassette document that cannot be read, said without quoting the
// document: a file holds the caller's prompts, and an error that repeated a bit
// of one would be a way for it to reach a log.
type malformed struct{ reason string }

func (m *malformed) Error() string { return m.reason }

// parseCassette reads one cassette document. It never panics, whatever it is
// given, and its errors say what is wrong with the document and not what the
// document says.
func parseCassette(data []byte) (Cassette, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w cassetteWire
	if err := dec.Decode(&w); err != nil {
		return Cassette{}, readFailure(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Cassette{}, &malformed{"it has something after the cassette"}
	}
	if w.Version != cassetteVersion {
		if w.Version == 0 {
			return Cassette{}, &malformed{"it has no version"}
		}
		return Cassette{}, &malformed{"it is of version " + strconv.Itoa(w.Version) + ", and this one reads version " + strconv.Itoa(cassetteVersion)}
	}
	c := Cassette{Provider: w.Provider}
	var err error
	if c.Request, err = requestOfWire(w.Request); err != nil {
		return Cassette{}, err
	}
	if w.Response != nil {
		if c.Response, err = responseOfWire(*w.Response); err != nil {
			return Cassette{}, err
		}
	}
	if w.Failure != nil {
		kind, ok := kindOfName(w.Failure.Kind)
		if !ok {
			return Cassette{}, &malformed{"its failure names a kind this version does not know"}
		}
		f := &CassetteFailure{Kind: kind, Status: w.Failure.Status}
		if w.Failure.RetryAfter != "" {
			if f.RetryAfter, err = time.ParseDuration(w.Failure.RetryAfter); err != nil || f.RetryAfter < 0 {
				return Cassette{}, &malformed{"its failure has a retry_after that is not a duration"}
			}
		}
		c.Failure = f
	}
	return c, nil
}

// readFailure says what is wrong with a document the decoder could not read.
func readFailure(err error) error {
	var syntax *json.SyntaxError
	var kind *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &malformed{"it is empty or cut short"}
	case errors.As(err, &syntax):
		return &malformed{"it is not valid JSON, at byte " + strconv.FormatInt(syntax.Offset, 10)}
	case errors.As(err, &kind):
		return &malformed{"its member " + strconv.Quote(kind.Field) + " has a value of the wrong type"}
	}
	return &malformed{"it has a member this version does not know"}
}

func requestOfWire(w requestWire) (CassetteRequest, error) {
	r := CassetteRequest{
		Model: w.Model, System: w.System, User: w.User, Temperature: w.Temperature, MaxTokens: w.MaxTokens,
		Format: w.Format, SchemaName: w.SchemaName, Schema: w.Schema, Effort: schemaflux.Effort(w.Effort),
		EffortPreferred: w.EffortPreferred, WebSearch: w.WebSearch, Tools: toolsOfWire(w.Tools), Messages: messagesOfWire(w.Messages),
	}
	if d := w.Decision; d != nil {
		r.Decision = &schemaflux.Decision{State: d.State, Guidance: d.Guidance}
		for _, q := range d.Questions {
			kind, ok := decisionKindOf(q.Kind)
			if !ok {
				return CassetteRequest{}, &malformed{"a question of its decision has a kind that is neither a name nor a number"}
			}
			r.Decision.Questions = append(r.Decision.Questions, schemaflux.DecisionQuestion{
				Kind: kind, Ask: q.Ask, Options: cloneStrings(q.Options), Criteria: cloneStrings(q.Criteria), Low: q.Low, High: q.High,
			})
		}
	}
	return r, nil
}

func responseOfWire(w responseWire) (*CassetteResponse, error) {
	r := &CassetteResponse{
		Content: w.Content,
		Usage: schemaflux.Usage{
			Input: w.Usage.Input, Output: w.Usage.Output, Total: w.Usage.Total,
			Cached: w.Usage.Cached, CacheWrite: w.Usage.CacheWrite, Reasoning: w.Usage.Reasoning,
		},
		Model: w.Model, Provider: w.Provider, Upstream: w.Upstream, FinishReason: w.FinishReason,
		ServiceTier: w.ServiceTier, Cached: w.Cached, Hedged: w.Hedged,
	}
	for _, a := range w.Answers {
		kind, ok := decisionKindOf(a.Kind)
		if !ok {
			return nil, &malformed{"an answer of its response has a kind that is neither a name nor a number"}
		}
		r.Answers = append(r.Answers, schemaflux.DecisionAnswer{
			Kind: kind, Yes: a.Yes, Label: a.Label, Level: a.Level,
			ModelConfidence: a.ModelConfidence, ConfidenceReported: a.ConfidenceReported,
		})
	}
	if e := w.Escalation; e != nil {
		r.Escalation = &schemaflux.DecisionEscalation{
			Provider: e.Provider, Model: e.Model, Reason: e.Reason,
			ModelConfidence: e.ModelConfidence, ConfidenceReported: e.ConfidenceReported,
		}
	}
	for _, call := range w.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, CassetteToolCall(call))
	}
	return r, nil
}

// Package schemafluxtest — cassettes.
//
// A cassette is a recorded provider exchange, replayed without a network or a
// credential. It exists because live provider tests are not reproducible: the
// model changes under you, the same prompt gives a different answer tomorrow,
// and a suite that needs a funded account is a suite most contributors cannot
// run at all.
//
// What is recorded is more than the two bodies. TODOS.md PRD-13 is specific
// about this, and the reason is worth stating: a replay that only feeds the
// response back proves the parser works and nothing about whether the runtime
// classified a failure the same way. So a cassette carries the provider and
// model, the request, the response, the usage, and — for a failure — the kind
// the classifier assigned at record time. A replay that produces a different
// classification is a regression the fixture can catch.
package schemafluxtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Cassette is one recorded exchange.
type Cassette struct {
	// RecordedAt says when, because a fixture's age is the first thing to
	// check when a replay stops matching reality.
	RecordedAt time.Time `json:"recorded_at"`

	Provider string `json:"provider"`
	Model    string `json:"model"`

	// Request is what was sent, minus anything that cannot be replayed.
	Request CassetteRequest `json:"request"`

	// Response is what came back, when the call succeeded.
	Response *CassetteResponse `json:"response,omitempty"`

	// Failure is what came back when it did not, with the classification the
	// runtime assigned at record time. Replaying a failure and getting a
	// different kind is the regression this field exists to catch.
	Failure *CassetteFailure `json:"failure,omitempty"`
}

// CassetteRequest is the part of a request that identifies it.
type CassetteRequest struct {
	SystemPrompt   string `json:"system_prompt"`
	UserPrompt     string `json:"user_prompt"`
	ResponseFormat string `json:"response_format,omitempty"`
	SchemaName     string `json:"schema_name,omitempty"`

	// HasJSONSchema rather than the schema itself: the schema is derived from
	// the caller's Go type and would make every cassette enormous, while what
	// a replay needs to know is whether strict enforcement was requested.
	HasJSONSchema bool `json:"has_json_schema"`
}

// CassetteResponse is a recorded success.
type CassetteResponse struct {
	Content      string                `json:"content"`
	FinishReason string                `json:"finish_reason,omitempty"`
	Usage        schemaflux.TokenUsage `json:"usage"`

	// UpstreamProvider and ServiceTier are what a gateway said served the
	// request. A data policy reads the first, and the second can differ from
	// the tier that was asked for, so a replay that dropped them would feed
	// the runtime an answer with less provenance than the recorded one.
	UpstreamProvider string `json:"upstream_provider,omitempty"`
	ServiceTier      string `json:"service_tier,omitempty"`

	// ToolCalls is what the model asked for instead of, or beside, an answer.
	// A tool-call exchange recorded without it replays as an empty answer
	// that still reports a finish reason of "tool_calls".
	ToolCalls []CassetteToolCall `json:"tool_calls,omitempty"`
}

// CassetteToolCall is one recorded tool call. Arguments are kept as text so
// that credential redaction can rewrite them like any other recorded string.
type CassetteToolCall struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// CassetteFailure is a recorded failure and how it was classified.
type CassetteFailure struct {
	Message string `json:"message"`

	// Kind is the classifier's answer, stored by name so a cassette stays
	// readable and does not break when a constant is renumbered.
	Kind string `json:"kind"`
}

// Recorder wraps a provider and writes what passes through it.
//
// Wrap a real provider to record, then commit the directory and replay from it
// forever after. Recording is the only part that costs money, and it happens
// once.
type Recorder struct {
	mu sync.Mutex

	inner     schemaflux.Provider
	directory string
	recorded  []Cassette
}

// Record returns a provider that delegates to inner and writes each exchange
// into directory.
func Record(inner schemaflux.Provider, directory string) *Recorder {
	return &Recorder{inner: inner, directory: directory}
}

// Complete implements schemaflux.Provider.
func (r *Recorder) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	resp, err := r.inner.Complete(ctx, req)

	cassette := Cassette{
		RecordedAt: time.Now().UTC(),
		Provider:   r.inner.Name(),
		Model:      req.Model,
		Request: CassetteRequest{
			SystemPrompt:   req.SystemPrompt,
			UserPrompt:     req.UserPrompt,
			ResponseFormat: req.ResponseFormat,
			SchemaName:     req.SchemaName,
			HasJSONSchema:  len(req.JSONSchema) > 0,
		},
	}

	if err != nil {
		cassette.Failure = &CassetteFailure{
			Message: err.Error(),
			Kind:    llm.Classify(err).String(),
		}
	} else {
		cassette.Response = &CassetteResponse{
			Content:          resp.Content,
			FinishReason:     resp.FinishReason,
			Usage:            resp.Usage,
			UpstreamProvider: resp.UpstreamProvider,
			ServiceTier:      resp.ServiceTier,
		}
		for _, call := range resp.ToolCalls {
			cassette.Response.ToolCalls = append(cassette.Response.ToolCalls, CassetteToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: string(call.Arguments),
			})
		}
	}

	r.mu.Lock()
	r.recorded = append(r.recorded, cassette)
	r.mu.Unlock()

	return resp, err
}

func (r *Recorder) Name() string { return r.inner.Name() }
func (r *Recorder) EstimateCost(req schemaflux.CompletionRequest) float64 {
	return r.inner.EstimateCost(req)
}
func (r *Recorder) RetryPolicy() (int, time.Duration) { return r.inner.RetryPolicy() }

// Save writes the recorded cassettes, redacting credentials on the way out.
//
// Redaction happens at write time rather than at read time on purpose: a
// secret that reaches the disk has leaked, and a reader that scrubs it is
// scrubbing a file somebody has already committed.
//
// The directory ends up holding exactly the cassettes recorded here: numbered
// .json files left by an earlier run are removed first, because Replay plays
// every one it finds and a leftover would be played as part of this run. Files
// that are not numbered cassettes are left alone. Nothing is removed or
// written until every cassette has been encoded and checked, so a refusal
// leaves the earlier recording as it was.
func (r *Recorder) Save() error {
	r.mu.Lock()
	recorded := append([]Cassette(nil), r.recorded...)
	r.mu.Unlock()

	if len(recorded) == 0 {
		return nil
	}

	files := make([][]byte, len(recorded))
	for i, cassette := range recorded {
		redacted := redactCassette(cassette)

		encoded, err := json.MarshalIndent(redacted, "", "  ")
		if err != nil {
			return fmt.Errorf("schemafluxtest: encoding cassette %d: %w", i, err)
		}
		if leak := findCredential(string(encoded)); leak != "" {
			return fmt.Errorf(
				"schemafluxtest: refusing to write cassette %d: it still contains something shaped like a %s. "+
					"Redaction runs before the write for exactly this reason; report the pattern that got through",
				i, leak)
		}
		files[i] = encoded
	}

	if err := os.MkdirAll(r.directory, 0o755); err != nil {
		return fmt.Errorf("schemafluxtest: creating %s: %w", r.directory, err)
	}
	if err := removeNumberedCassettes(r.directory); err != nil {
		return err
	}

	for i, encoded := range files {
		name := filepath.Join(r.directory, fmt.Sprintf("%03d.json", i))
		if err := os.WriteFile(name, encoded, 0o644); err != nil {
			return fmt.Errorf("schemafluxtest: writing %s: %w", name, err)
		}
	}

	return nil
}

// removeNumberedCassettes deletes the files in directory that Save would name:
// a run of digits followed by .json. Directories and every other name stay.
func removeNumberedCassettes(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("schemafluxtest: reading %s: %w", directory, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, numbered := cassetteNumber(entry.Name()); !numbered {
			continue
		}
		name := filepath.Join(directory, entry.Name())
		if err := os.Remove(name); err != nil {
			return fmt.Errorf("schemafluxtest: removing the earlier cassette %s: %w", name, err)
		}
	}
	return nil
}

// cassetteNumber returns the digits of a numbered cassette file name such as
// "007.json", and false for any other name.
func cassetteNumber(name string) (string, bool) {
	stem, ok := strings.CutSuffix(name, ".json")
	if !ok || stem == "" {
		return "", false
	}
	for _, r := range stem {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return stem, true
}

// cassetteFileLess orders cassette files for replay. Numbered files go by the
// value of their number, so "1000.json" follows "999.json" where a string sort
// would put it before "101.json"; the comparison is on the digits without
// leading zeros, longer meaning larger, so no width of number overflows.
// Names that are not numbers come after the numbered ones, in string order.
func cassetteFileLess(a, b string) bool {
	numberA, numberedA := cassetteNumber(a)
	numberB, numberedB := cassetteNumber(b)
	switch {
	case numberedA && numberedB:
		valueA, valueB := strings.TrimLeft(numberA, "0"), strings.TrimLeft(numberB, "0")
		if len(valueA) != len(valueB) {
			return len(valueA) < len(valueB)
		}
		if valueA != valueB {
			return valueA < valueB
		}
		return a < b
	case numberedA:
		return true
	case numberedB:
		return false
	}
	return a < b
}

// Recorded returns what has been captured so far.
func (r *Recorder) Recorded() []Cassette {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Cassette(nil), r.recorded...)
}

// Player replays cassettes in order, with no network and no credential.
type Player struct {
	mu        sync.Mutex
	cassettes []Cassette
	position  int

	// strict reports an error when the replayed request does not match what
	// was recorded. On by default: a replay that silently answers a different
	// question is a test that passes for the wrong reason.
	strict bool
}

// Replay loads every .json cassette in a directory, in the order Save numbered
// them: by the value of the number in the file name, then by name for files
// that are not numbered.
func Replay(directory string) (*Player, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("schemafluxtest: reading %s: %w", directory, err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return cassetteFileLess(names[i], names[j]) })

	if len(names) == 0 {
		return nil, fmt.Errorf("schemafluxtest: no cassettes in %s", directory)
	}

	player := &Player{strict: true}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("schemafluxtest: reading %s: %w", name, err)
		}
		var cassette Cassette
		if err := json.Unmarshal(raw, &cassette); err != nil {
			return nil, fmt.Errorf("schemafluxtest: parsing %s: %w", name, err)
		}
		player.cassettes = append(player.cassettes, cassette)
	}

	return player, nil
}

// ReplayFrom builds a player from cassettes already in memory, which is what a
// test that just recorded them wants.
func ReplayFrom(cassettes ...Cassette) *Player {
	return &Player{cassettes: append([]Cassette(nil), cassettes...), strict: true}
}

// Lenient turns off request matching, for a replay that only cares about the
// answers. Prefer the default: a replay answering a question nobody asked is a
// test passing for the wrong reason.
func (p *Player) Lenient() *Player {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strict = false
	return p
}

// Complete implements schemaflux.Provider.
//
// A context that is already done is reported before a cassette is consumed,
// as schemafluxtest.Provider does, so a test of cancellation or a deadline
// sees what a real provider would do and the recording is still there for the
// call that follows.
func (p *Player) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if err := ctx.Err(); err != nil {
		return schemaflux.CompletionResponse{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.position >= len(p.cassettes) {
		return schemaflux.CompletionResponse{}, fmt.Errorf(
			"schemafluxtest: the code under test made %d calls and only %d were recorded",
			p.position+1, len(p.cassettes))
	}

	cassette := p.cassettes[p.position]
	p.position++

	if p.strict {
		// Everything recorded about the request that changes what the model
		// answers: the two prompts, the model, and the shape asked for. The
		// schema itself is not stored, only whether one was sent.
		checks := []struct {
			what  string
			match bool
		}{
			{"system prompt", cassette.Request.SystemPrompt == req.SystemPrompt},
			{"user prompt", cassette.Request.UserPrompt == req.UserPrompt},
			{"model", cassette.Model == req.Model},
			{"response format", cassette.Request.ResponseFormat == req.ResponseFormat},
			{"schema name", cassette.Request.SchemaName == req.SchemaName},
			{"JSON schema", cassette.Request.HasJSONSchema == (len(req.JSONSchema) > 0)},
		}
		for _, check := range checks {
			if !check.match {
				return schemaflux.CompletionResponse{}, fmt.Errorf(
					"schemafluxtest: call %d sent a different %s than the cassette recorded; "+
						"the request changed, so the recording no longer answers the question being asked",
					p.position, check.what)
			}
		}
	}

	if cassette.Failure != nil {
		return schemaflux.CompletionResponse{}, &types.OperationError{
			Kind:    kindByName(cassette.Failure.Kind),
			Message: cassette.Failure.Message,
		}
	}

	if cassette.Response == nil {
		return schemaflux.CompletionResponse{}, fmt.Errorf(
			"schemafluxtest: cassette %d records neither a response nor a failure", p.position-1)
	}

	response := schemaflux.CompletionResponse{
		Content:          cassette.Response.Content,
		Provider:         cassette.Provider,
		Model:            cassette.Model,
		FinishReason:     cassette.Response.FinishReason,
		Usage:            cassette.Response.Usage,
		UpstreamProvider: cassette.Response.UpstreamProvider,
		ServiceTier:      cassette.Response.ServiceTier,
	}
	for _, call := range cassette.Response.ToolCalls {
		replayed := llm.ToolCall{ID: call.ID, Name: call.Name}
		if call.Arguments != "" {
			replayed.Arguments = json.RawMessage(call.Arguments)
		}
		response.ToolCalls = append(response.ToolCalls, replayed)
	}
	return response, nil
}

// Name reports "local", which is the name reserved for an in-process double.
// A provider name with no model mapping resolves to no model at all and every
// call errors, which is FL-001 working as intended.
func (p *Player) Name() string                                      { return "local" }
func (p *Player) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *Player) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

// Remaining reports how many cassettes have not been played, so a test can
// assert it consumed the ones it expected to.
func (p *Player) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cassettes) - p.position
}

// credentialPatterns are the shapes that must never reach a committed file.
// They are the shapes scripts/secret_scan.py looks for over the whole
// repository, applied one step earlier, before the file exists. The order
// matters twice: an Anthropic key also fits the OpenAI pattern, so it comes
// first to be named correctly, and a private key is matched as a whole block
// before the header-only form, which is the fallback for a block that was cut
// off and has no end line.
var credentialPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"OpenAI key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`)},
	{"AWS access key ID", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"bearer header", regexp.MustCompile(`(?i)authorization"?\s*[:=]\s*"?bearer\s+\S+`)},
	{"private key block", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----.*?-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----`)},
	{"private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----(?:\r?\n[A-Za-z0-9+/=]{8,})*`)},
}

const redactedMarker = "[redacted by schemafluxtest]"

func redactCassette(cassette Cassette) Cassette {
	cassette.Request.SystemPrompt = redactCredentials(cassette.Request.SystemPrompt)
	cassette.Request.UserPrompt = redactCredentials(cassette.Request.UserPrompt)
	if cassette.Response != nil {
		response := *cassette.Response
		response.Content = redactCredentials(response.Content)
		if len(response.ToolCalls) > 0 {
			response.ToolCalls = make([]CassetteToolCall, len(cassette.Response.ToolCalls))
			for i, call := range cassette.Response.ToolCalls {
				response.ToolCalls[i] = CassetteToolCall{
					ID:        redactCredentials(call.ID),
					Name:      redactCredentials(call.Name),
					Arguments: redactCredentials(call.Arguments),
				}
			}
		}
		cassette.Response = &response
	}
	if cassette.Failure != nil {
		failure := *cassette.Failure
		failure.Message = redactCredentials(failure.Message)
		cassette.Failure = &failure
	}
	return cassette
}

func redactCredentials(text string) string {
	for _, entry := range credentialPatterns {
		text = entry.pattern.ReplaceAllString(text, redactedMarker)
	}
	return text
}

// findCredential names the first credential shape still present, or "".
func findCredential(text string) string {
	for _, entry := range credentialPatterns {
		if entry.pattern.MatchString(text) {
			return entry.name
		}
	}
	return ""
}

// kindByName resolves a recorded classification back to its kind.
//
// By name rather than by number, so a cassette recorded today still means the
// same thing after a constant is inserted in the middle of the taxonomy.
//
// The search is bounded by the size of the kind's own range, not by whichever
// kind was last when this was written: a kind added later is found without
// this function being told. A value with no name reads "unknown", the same as
// KindUnknown, so that name is answered up front rather than matched by the
// first unnamed value the loop reaches.
func kindByName(name string) types.ErrorKind {
	if name == types.KindUnknown.String() {
		return types.KindUnknown
	}
	for kind := types.KindUnknown + 1; kind <= maxKindProbed; kind++ {
		if kind.String() == name {
			return kind
		}
	}
	return types.KindUnknown
}

// maxKindProbed is how far above KindUnknown kindByName looks. The taxonomy
// has a couple of dozen kinds; this leaves room for it to grow by an order of
// magnitude before a named kind could fall outside the search.
const maxKindProbed types.ErrorKind = 255

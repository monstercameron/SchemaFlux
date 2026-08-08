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
			Content:      resp.Content,
			FinishReason: resp.FinishReason,
			Usage:        resp.Usage,
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
func (r *Recorder) Save() error {
	r.mu.Lock()
	recorded := append([]Cassette(nil), r.recorded...)
	r.mu.Unlock()

	if len(recorded) == 0 {
		return nil
	}
	if err := os.MkdirAll(r.directory, 0o755); err != nil {
		return fmt.Errorf("schemafluxtest: creating %s: %w", r.directory, err)
	}

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

		name := filepath.Join(r.directory, fmt.Sprintf("%03d.json", i))
		if err := os.WriteFile(name, encoded, 0o644); err != nil {
			return fmt.Errorf("schemafluxtest: writing %s: %w", name, err)
		}
	}

	return nil
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

// Replay loads every cassette in a directory, in filename order.
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
	sort.Strings(names)

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
func (p *Player) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
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
		if cassette.Request.SystemPrompt != req.SystemPrompt {
			return schemaflux.CompletionResponse{}, fmt.Errorf(
				"schemafluxtest: call %d sent a different system prompt than the cassette recorded; "+
					"the prompt changed, so the recording no longer answers the question being asked",
				p.position)
		}
		if cassette.Request.UserPrompt != req.UserPrompt {
			return schemaflux.CompletionResponse{}, fmt.Errorf(
				"schemafluxtest: call %d sent a different user prompt than the cassette recorded",
				p.position)
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

	return schemaflux.CompletionResponse{
		Content:      cassette.Response.Content,
		Provider:     cassette.Provider,
		Model:        cassette.Model,
		FinishReason: cassette.Response.FinishReason,
		Usage:        cassette.Response.Usage,
	}, nil
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
// They mirror scripts/secret_scan.py, which is the check that runs over the
// whole repository; this is the same rule applied one step earlier, before the
// file exists.
var credentialPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"OpenAI key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}`)},
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`)},
	{"AWS access key ID", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`)},
	{"bearer header", regexp.MustCompile(`(?i)authorization"?\s*[:=]\s*"?bearer\s+\S+`)},
}

const redactedMarker = "[redacted by schemafluxtest]"

func redactCassette(cassette Cassette) Cassette {
	cassette.Request.SystemPrompt = redactCredentials(cassette.Request.SystemPrompt)
	cassette.Request.UserPrompt = redactCredentials(cassette.Request.UserPrompt)
	if cassette.Response != nil {
		response := *cassette.Response
		response.Content = redactCredentials(response.Content)
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
func kindByName(name string) types.ErrorKind {
	for kind := types.KindUnknown; kind <= types.KindReviewRequired; kind++ {
		if kind.String() == name {
			return kind
		}
	}
	return types.KindUnknown
}

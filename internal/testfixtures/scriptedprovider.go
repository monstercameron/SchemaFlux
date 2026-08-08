// Package testfixtures holds the test doubles shared by more than one of this
// module's test packages.
//
// It exists because the root package's tests and the black-box suite in
// `tests/` are two different Go packages and cannot see each other's helpers.
// The alternative was a copy in each, which is how two fixtures that are
// supposed to be the same drift apart and start proving different things.
//
// It is under `internal/` deliberately: this is a fixture for testing THIS
// library, not a fixture for consumers testing code built on it. That is what
// `schemafluxtest` is for, and the two should not be confused — one is allowed
// to know things about the implementation, the other must not.
package testfixtures

import (
	"context"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// ScriptedProvider is a Provider that returns canned bodies. It is the seam a
// test needs in order to drive the whole stack — public API, ops, llm — without
// reaching a paid endpoint.
type ScriptedProvider struct {
	body string
	err  error

	// bodies answers with a different body per call, for tests that need the
	// repair loop to see a second answer.
	bodies []string

	// requests records what the stack actually sent.
	requests []schemaflux.CompletionRequest
}

func (p *ScriptedProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	if p.err != nil {
		return schemaflux.CompletionResponse{}, p.err
	}
	body := p.body
	if len(p.bodies) > 0 {
		index := len(p.requests) - 1
		if index >= len(p.bodies) {
			index = len(p.bodies) - 1
		}
		body = p.bodies[index]
	}

	return schemaflux.CompletionResponse{
		Content:      body,
		Provider:     p.Name(),
		Model:        req.Model,
		FinishReason: "stop",
	}, nil
}

// Name reports "local", the name the library reserves for an in-process test
// double. It is not cosmetic: a provider whose name has no model mapping
// resolves to no model at all and every call errors, which is FL-001 working as
// intended. A test double that invented a provider name would be asserting
// against a configuration no caller should ship.
func (p *ScriptedProvider) Name() string                                      { return "local" }
func (p *ScriptedProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *ScriptedProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

// Requests returns what the stack actually sent, in order. Tests assert against
// this rather than against a prompt they constructed themselves, which would
// only prove the test can build a string.
func (p *ScriptedProvider) Requests() []schemaflux.CompletionRequest { return p.requests }

// WithScriptedProviderReplies installs a provider that answers with a different
// body per call, so a test can exercise the repair loop — one unusable answer,
// then a good one.
func WithScriptedProviderReplies(t *testing.T, bodies ...string) *ScriptedProvider {
	t.Helper()
	provider := &ScriptedProvider{bodies: bodies}
	schemaflux.NewClient("test-key").WithProviderInstance(provider)
	return provider
}

// WithScriptedProvider installs a provider that answers with one body, or fails
// with err.
func WithScriptedProvider(t *testing.T, body string, err error) *ScriptedProvider {
	t.Helper()
	provider := &ScriptedProvider{body: body, err: err}
	schemaflux.NewClient("test-key").WithProviderInstance(provider)
	return provider
}

// NewScripted returns a provider that answers every call with body. It does not
// install itself: use it when the test wants to hand the provider to a specific
// client rather than to the process default.
func NewScripted(body string) *ScriptedProvider {
	return &ScriptedProvider{body: body}
}

// NewScriptedReplies returns a provider that answers with a different body per
// call, without installing itself.
func NewScriptedReplies(bodies ...string) *ScriptedProvider {
	return &ScriptedProvider{bodies: bodies}
}

// NewFailing returns a provider whose every call fails with err.
func NewFailing(err error) *ScriptedProvider {
	return &ScriptedProvider{err: err}
}

// NamedProvider answers with its own name inside the body, so a test can tell
// WHICH provider served a call rather than only that some provider did. That
// distinction is the whole point of an isolation test: two clients both
// succeeding proves nothing if you cannot tell them apart.
type NamedProvider struct {
	name string
}

func NewNamed(name string) *NamedProvider { return &NamedProvider{name: name} }

func (p *NamedProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	return schemaflux.CompletionResponse{
		Content:      p.name,
		Provider:     "local",
		Model:        req.Model,
		FinishReason: "stop",
	}, nil
}

func (p *NamedProvider) Name() string                                      { return "local" }
func (p *NamedProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *NamedProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

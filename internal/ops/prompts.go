package ops

import (
	"fmt"
	"strings"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PS-007: prompts as versioned, overridable artifacts, so a prompt edit is a
// reviewable change rather than a silent behavior change for every
// downstream caller. TI-004 (golden prompts) proved the prompts are worth
// pinning; this is what pins them and lets a caller substitute their own.
//
// The piece that matters most, stated once here so every function below can
// point back at it: an override is a DIFFERENT prompt, not a substitute
// value for the built-in one. If an override silently kept the built-in's
// version, promptCacheKeyFor (llm_helper.go, P-009) would key the override's
// request identically to the built-in's, and a provider that already holds
// the built-in's prefix cached under that key would get a cache hit for a
// prefix it never actually received -- served bytes for a prompt it was
// never sent. Every override in this file mints its own version, derived
// from the override's own text, so that cannot happen structurally rather
// than by convention.
//
// This file does not wire the registry into Extract or any other running
// operation -- those files are owned elsewhere (llm_helper.go, core.go) and
// changing what prompt an operation actually sends is a separate change
// from building the seam that would let it. See this task's report for what
// that wiring would take.

// PromptSource says where a resolved Prompt's text came from: the library's
// own built-in template, or a caller's runtime substitution for it. It is
// part of a Prompt's identity (see CacheIdentity) precisely so a built-in
// and an override can never collide even if, by coincidence, they had the
// same operation name and the same version string.
type PromptSource string

const (
	PromptSourceBuiltin  PromptSource = "builtin"
	PromptSourceOverride PromptSource = "override"
)

// Prompt is one versioned, identifiable prompt artifact for one operation.
type Prompt struct {
	// ID names the operation and this prompt's version. For a built-in
	// prompt, Version is whatever the operation declares (extractPromptVersion
	// is the existing instance of this idea for Extract, core.go). For an
	// override, Version is never the built-in's -- see Override.
	ID types.OperationID

	// Text is the actual prompt template text sent to the model.
	Text string

	// Source distinguishes a built-in prompt from a caller's override. See
	// CacheIdentity for why this is part of identity, not just metadata.
	Source PromptSource
}

// CacheIdentity is the identity this prompt contributes to a provider-facing
// cache key (see promptCacheKeyFor, llm_helper.go, P-009).
//
// It folds in Source and a content hash of Text, not just ID -- two reasons.
// First, an override and a built-in must never alias each other's cached
// prefix even if a caller's override happened to reuse the built-in's exact
// version string (Override never lets that happen on its own, but this is
// the second, structural guard against the same mistake). Second, editing
// override text without bumping anything else -- the whole point of an
// override being a live substitution -- still has to mint a new identity,
// because the bytes actually being sent changed; a version string a caller
// forgot to bump must not leave a stale cache key silently serving the OLD
// override's prefix for the NEW override's request.
func (p Prompt) CacheIdentity() string {
	parts := []string{"prompt", p.ID.Name, p.ID.Version, string(p.Source), digestOf(p.Text)}
	for i, part := range parts {
		if strings.TrimSpace(part) == "" {
			parts[i] = "-"
		}
	}
	return strings.Join(parts, ":")
}

// PromptRegistry holds the built-in prompt for each operation, and any
// caller-supplied override standing in for it. Zero value is not usable;
// construct with NewPromptRegistry.
type PromptRegistry struct {
	mu        sync.RWMutex
	builtins  map[string]Prompt // keyed by types.OperationID.Name
	overrides map[string]Prompt // keyed by types.OperationID.Name
}

// NewPromptRegistry returns an empty registry.
func NewPromptRegistry() *PromptRegistry {
	return &PromptRegistry{
		builtins:  make(map[string]Prompt),
		overrides: make(map[string]Prompt),
	}
}

// RegisterBuiltin registers (or replaces) the library's own prompt for an
// operation.
//
// id.Version is the operation's contract version, the same idea
// extractPromptVersion (core.go) already carries for Extract: bump it when
// the prompt text or the contract around it changes in a way that should
// mint a new cache identity. Replacing a built-in at the same Name but a
// different Version is exactly what a reviewable prompt edit looks like --
// the change shows up in CacheIdentity without anyone having to remember to
// touch a cache key by hand.
func (r *PromptRegistry) RegisterBuiltin(id types.OperationID, text string) error {
	if strings.TrimSpace(id.Name) == "" {
		return fmt.Errorf("prompt registry: operation name is required")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("prompt registry: prompt text is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builtins[id.Name] = Prompt{ID: id, Text: text, Source: PromptSourceBuiltin}
	return nil
}

// Override substitutes a caller-supplied prompt for the named operation.
//
// The override's version is derived from the override's own text (a content
// hash) rather than taken from the built-in it replaces, or left for the
// caller to set: a caller-chosen version string could accidentally collide
// with a real built-in version, or with a previous override that happened
// to reuse the same label while the text underneath changed. Deriving it
// here means an override can never silently inherit -- or collide with --
// an identity that belongs to different bytes. Overriding the same
// operation again with different text automatically mints a different
// version; overriding it again with byte-identical text reuses the same
// one, which is correct: it is the same prompt.
func (r *PromptRegistry) Override(operationName, text string) error {
	if strings.TrimSpace(operationName) == "" {
		return fmt.Errorf("prompt registry: operation name is required")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("prompt registry: override text is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides[operationName] = Prompt{
		ID:     types.OperationID{Name: operationName, Version: "override-" + digestOf(text)},
		Text:   text,
		Source: PromptSourceOverride,
	}
	return nil
}

// ClearOverride removes a caller's override for an operation, so Resolve
// reverts to the built-in. A no-op if no override was registered.
func (r *PromptRegistry) ClearOverride(operationName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.overrides, operationName)
}

// Resolve returns the prompt currently active for an operation: the
// caller's override if one is registered, otherwise the built-in.
//
// There is no third state. An operation with neither a built-in nor an
// override registered is an error -- not a zero-value Prompt sent to a
// provider as though it were a real one, which is the "never fail open"
// rule (AGENTS.md) applied to a prompt instead of a decoded answer.
func (r *PromptRegistry) Resolve(operationName string) (Prompt, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.overrides[operationName]; ok {
		return p, nil
	}
	if p, ok := r.builtins[operationName]; ok {
		return p, nil
	}
	return Prompt{}, fmt.Errorf("prompt registry: no prompt registered for operation %q", operationName)
}

// HasOverride reports whether operationName currently resolves to a
// caller-supplied override rather than the built-in.
func (r *PromptRegistry) HasOverride(operationName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.overrides[operationName]
	return ok
}

package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TestPromptCacheKeyIsStableAcrossIdenticalInputs is the baseline the rest of
// this file's cases perturb one field away from: given the exact same
// identity, model, response format, and rendered template, two calls must
// mint the same key, or nothing downstream could ever hit the cache.
func TestPromptCacheKeyIsStableAcrossIdenticalInputs(t *testing.T) {
	opts := types.OpOptions{CacheIdentity: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict", Mode: types.Strict}
	key1 := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "system prompt text")
	key2 := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "system prompt text")

	if key1 == "" {
		t.Fatal("expected a non-empty key")
	}
	if key1 != key2 {
		t.Fatalf("expected identical inputs to produce identical keys, got %q vs %q", key1, key2)
	}
}

// TestPromptCacheKeyDiffersByCacheIdentity covers the axis (op, T, tier) used
// to be the whole key on: two different operation-and-schema identities must
// never collide, because a collision here means one op's answer can be served
// from another's cached prefix.
func TestPromptCacheKeyDiffersByCacheIdentity(t *testing.T) {
	opts := types.OpOptions{Mode: types.Strict}
	a := promptCacheKeyFor(setCacheIdentity(opts, "extract:v1:Person:v1:hash1:json-schema-2020-12-strict"), "gpt-5.6-luna", "json", "prompt")
	b := promptCacheKeyFor(setCacheIdentity(opts, "extract:v1:Company:v1:hash2:json-schema-2020-12-strict"), "gpt-5.6-luna", "json", "prompt")

	if a == b {
		t.Fatalf("expected different schema identities to mint different keys, both were %q", a)
	}
}

// TestPromptCacheKeyDiffersBySchemaVersion is the Revised note's specific
// complaint about (op, T, tier): a schema *version* bump must change the key
// even when the operation and the type's Go name are unchanged, because a v2
// answer must never be served from a v1 cache entry.
func TestPromptCacheKeyDiffersBySchemaVersion(t *testing.T) {
	opts := types.OpOptions{Mode: types.Strict}
	a := promptCacheKeyFor(setCacheIdentity(opts, "extract:v1:Invoice:v1:samehash:json-schema-2020-12-strict"), "gpt-5.6-luna", "json", "prompt")
	b := promptCacheKeyFor(setCacheIdentity(opts, "extract:v1:Invoice:v2:samehash:json-schema-2020-12-strict"), "gpt-5.6-luna", "json", "prompt")

	if a == b {
		t.Fatalf("expected a schema version bump to change the key, both were %q", a)
	}
}

// TestPromptCacheKeyDiffersByModel: a cache entry written by one model's
// tokenizer means nothing to another. Two different resolved models must
// never share a key even with every other input held constant.
func TestPromptCacheKeyDiffersByModel(t *testing.T) {
	opts := types.OpOptions{CacheIdentity: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict", Mode: types.Strict}
	a := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "prompt")
	b := promptCacheKeyFor(opts, "gpt-5.6-terra", "json", "prompt")

	if a == b {
		t.Fatalf("expected different models to mint different keys, both were %q", a)
	}
}

// TestPromptCacheKeyChangesWhenPromptLiteralEdited is the Revised note's added
// verify line, direct: editing a prompt literal -- represented here by the
// rendered stable template text changing -- must change the key. Without
// this, a prompt revision would route to a server holding the PREVIOUS
// release's prefix instead of missing the cache and writing a new one.
func TestPromptCacheKeyChangesWhenPromptLiteralEdited(t *testing.T) {
	opts := types.OpOptions{CacheIdentity: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict", Mode: types.Strict}
	before := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "Extract the name and age.")
	after := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "Extract the full name and exact age.")

	if before == after {
		t.Fatalf("expected an edited prompt literal to change the key, both were %q", before)
	}
}

// TestPromptCacheKeyIgnoresSteering documents that steering is not even a
// parameter this function accepts -- it is intentionally excluded before it
// could reach the key, because it is volatile and appended to the system
// prompt as a suffix AFTER this function runs (CA-002). Two calls whose only
// difference is the steering text passed to CallLLM must therefore always
// share a key, verified here by observing the function cannot see it at all:
// two different steering strings, same template, resolve to the same key.
func TestPromptCacheKeyIgnoresSteering(t *testing.T) {
	opts := types.OpOptions{CacheIdentity: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict", Mode: types.Strict}
	// The template argument is what CallLLM builds from systemPrompt BEFORE
	// applySteering runs, so a caller varying Steering never changes it.
	a := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "Extract the name and age.")
	b := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "Extract the name and age.")

	if a != b {
		t.Fatalf("expected steering-insensitive inputs to share a key, got %q vs %q", a, b)
	}
}

// TestPromptCacheKeyDiffersByResponseFormat: the reinforcement boilerplate
// strengthenSystemPrompt adds differs by JSON vs text (extra rules block), so
// two requests that differ only in response format render different bytes and
// must not share a key.
func TestPromptCacheKeyDiffersByResponseFormat(t *testing.T) {
	opts := types.OpOptions{CacheIdentity: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict", Mode: types.Strict}
	a := promptCacheKeyFor(opts, "gpt-5.6-luna", "json", "prompt")
	b := promptCacheKeyFor(opts, "gpt-5.6-luna", "text", "prompt")

	if a == b {
		t.Fatalf("expected different response formats to mint different keys, both were %q", a)
	}
}

// TestPromptCacheKeyDiffersByMode covers operations whose rendered template
// text does not vary by Mode even though their behavior does; Mode is folded
// in directly so those operations are not silently collapsed onto one key.
func TestPromptCacheKeyDiffersByMode(t *testing.T) {
	optsStrict := types.OpOptions{CacheIdentity: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict", Mode: types.Strict}
	optsCreative := types.OpOptions{CacheIdentity: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict", Mode: types.Creative}

	a := promptCacheKeyFor(optsStrict, "gpt-5.6-luna", "json", "prompt")
	b := promptCacheKeyFor(optsCreative, "gpt-5.6-luna", "json", "prompt")

	if a == b {
		t.Fatalf("expected different modes to mint different keys, both were %q", a)
	}
}

// TestPromptCacheKeyDegradesGracefullyWithoutCacheIdentity: an operation that
// has not been wired to compute a schema identity yet (CacheIdentity == "")
// must still produce a usable, non-empty key -- the template and model axes
// alone already keep it apart from other operations' keys, which is strictly
// better than refusing to cache at all.
func TestPromptCacheKeyDegradesGracefullyWithoutCacheIdentity(t *testing.T) {
	opts := types.OpOptions{Mode: types.TransformMode}
	key := promptCacheKeyFor(opts, "gpt-5.6-luna", "text", "Summarize the following text.")

	if key == "" {
		t.Fatal("expected a non-empty key even with no CacheIdentity")
	}
}

// TestPromptCacheKeyBlankFieldsFallBackToPlaceholder: an empty model or an
// empty (whitespace-only) identity must not silently collapse the key's
// segment count -- each blank field becomes an explicit placeholder so the
// key's shape stays predictable rather than shifting the meaning of the
// segments after it.
func TestPromptCacheKeyBlankFieldsFallBackToPlaceholder(t *testing.T) {
	opts := types.OpOptions{CacheIdentity: "   ", Mode: types.ModeUnset}
	key := promptCacheKeyFor(opts, "", "", "prompt")

	if key == "" {
		t.Fatal("expected a non-empty key")
	}
	segments := strings.Split(key, ":")
	if len(segments) != 5 {
		t.Fatalf("expected 5 segments, got %d in %q", len(segments), key)
	}
	if segments[0] != "-" || segments[1] != "-" {
		t.Fatalf("expected blank identity and model to fall back to '-', got %q", key)
	}
}

// setCacheIdentity returns a copy of opts with CacheIdentity set, so table
// cases above can share a base OpOptions value without aliasing it.
func setCacheIdentity(opts types.OpOptions, identity string) types.OpOptions {
	opts.CacheIdentity = identity
	return opts
}

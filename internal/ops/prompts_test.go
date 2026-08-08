package ops

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestPromptRegistryResolvesBuiltin(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.RegisterBuiltin(types.OperationID{Name: "extract", Version: "v1"}, "extract this: {input}"); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	got, err := r.Resolve("extract")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Text != "extract this: {input}" {
		t.Errorf("Text = %q, want the built-in text", got.Text)
	}
	if got.Source != PromptSourceBuiltin {
		t.Errorf("Source = %v, want PromptSourceBuiltin", got.Source)
	}
	if got.ID.Version != "v1" {
		t.Errorf("Version = %q, want %q", got.ID.Version, "v1")
	}
}

func TestPromptRegistryUnknownOperationErrors(t *testing.T) {
	r := NewPromptRegistry()
	_, err := r.Resolve("never-registered")
	if err == nil {
		t.Fatal("expected an error for an unregistered operation, got nil")
	}
}

func TestPromptRegistryRegisterBuiltinRejectsEmptyNameOrText(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.RegisterBuiltin(types.OperationID{Name: "", Version: "v1"}, "text"); err == nil {
		t.Error("expected an error for an empty operation name, got nil")
	}
	if err := r.RegisterBuiltin(types.OperationID{Name: "extract", Version: "v1"}, ""); err == nil {
		t.Error("expected an error for empty prompt text, got nil")
	}
}

// TestPromptOverrideChangesTheSentPrompt proves the override seam actually
// changes what Resolve hands back -- the whole point of an override.
func TestPromptOverrideChangesTheSentPrompt(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.RegisterBuiltin(types.OperationID{Name: "classify", Version: "v1"}, "built-in template"); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := r.Override("classify", "caller's own template"); err != nil {
		t.Fatalf("Override: %v", err)
	}

	got, err := r.Resolve("classify")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Text != "caller's own template" {
		t.Errorf("Text = %q, want the override's text, not the built-in's", got.Text)
	}
	if got.Source != PromptSourceOverride {
		t.Errorf("Source = %v, want PromptSourceOverride", got.Source)
	}
	if !r.HasOverride("classify") {
		t.Error("HasOverride = false, want true after Override")
	}
}

// TestPromptOverrideDoesNotInheritBuiltinVersion is the rule that matters
// most: a caller-supplied prompt is a different prompt and must not reuse
// the built-in's version identity.
func TestPromptOverrideDoesNotInheritBuiltinVersion(t *testing.T) {
	r := NewPromptRegistry()
	const builtinVersion = "v3"
	if err := r.RegisterBuiltin(types.OperationID{Name: "summarize", Version: builtinVersion}, "built-in text"); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := r.Override("summarize", "override text"); err != nil {
		t.Fatalf("Override: %v", err)
	}

	got, err := r.Resolve("summarize")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID.Version == builtinVersion {
		t.Errorf("override's Version = %q, which is the built-in's version -- an override must mint its own", got.ID.Version)
	}
}

// TestPromptOverrideDoesNotShareCacheIdentityWithBuiltin is the assertion
// the task calls out by name: an override must not reuse the built-in's
// cache identity, or the prompt cache key (P-009) would serve a cached
// prefix for a prompt that was never sent.
func TestPromptOverrideDoesNotShareCacheIdentityWithBuiltin(t *testing.T) {
	r := NewPromptRegistry()
	id := types.OperationID{Name: "rewrite", Version: "v1"}
	if err := r.RegisterBuiltin(id, "built-in rewrite template"); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	builtin, err := r.Resolve("rewrite")
	if err != nil {
		t.Fatalf("Resolve (builtin): %v", err)
	}
	builtinKey := builtin.CacheIdentity()

	if err := r.Override("rewrite", "override rewrite template"); err != nil {
		t.Fatalf("Override: %v", err)
	}
	override, err := r.Resolve("rewrite")
	if err != nil {
		t.Fatalf("Resolve (override): %v", err)
	}
	overrideKey := override.CacheIdentity()

	if builtinKey == overrideKey {
		t.Fatalf("override's CacheIdentity (%q) equals the built-in's (%q); a cache keyed on this would serve the built-in's cached prefix for a prompt that was never sent", overrideKey, builtinKey)
	}
}

// TestPromptVersionBumpChangesCacheIdentity proves a deliberate version bump
// on a built-in changes its key -- the case a prompt edit is supposed to be
// visible through.
func TestPromptVersionBumpChangesCacheIdentity(t *testing.T) {
	r := NewPromptRegistry()
	name := "translate"

	if err := r.RegisterBuiltin(types.OperationID{Name: name, Version: "v1"}, "translate {text} to {lang}"); err != nil {
		t.Fatalf("RegisterBuiltin v1: %v", err)
	}
	v1, err := r.Resolve(name)
	if err != nil {
		t.Fatalf("Resolve v1: %v", err)
	}
	key1 := v1.CacheIdentity()

	if err := r.RegisterBuiltin(types.OperationID{Name: name, Version: "v2"}, "translate {text} into {lang}, preserving tone"); err != nil {
		t.Fatalf("RegisterBuiltin v2: %v", err)
	}
	v2, err := r.Resolve(name)
	if err != nil {
		t.Fatalf("Resolve v2: %v", err)
	}
	key2 := v2.CacheIdentity()

	if key1 == key2 {
		t.Errorf("CacheIdentity did not change across a version bump: both %q", key1)
	}
}

// TestPromptUnrelatedEditElsewhereDoesNotChangeKey proves registering or
// editing a DIFFERENT operation's prompt leaves this operation's cache
// identity untouched -- the registry's per-operation keying must not leak
// across operations.
func TestPromptUnrelatedEditElsewhereDoesNotChangeKey(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.RegisterBuiltin(types.OperationID{Name: "expand", Version: "v1"}, "expand on {topic}"); err != nil {
		t.Fatalf("RegisterBuiltin expand: %v", err)
	}
	before, err := r.Resolve("expand")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	beforeKey := before.CacheIdentity()

	// Register a wholly unrelated operation, then override it too.
	if err := r.RegisterBuiltin(types.OperationID{Name: "score", Version: "v1"}, "score {item}"); err != nil {
		t.Fatalf("RegisterBuiltin score: %v", err)
	}
	if err := r.Override("score", "score {item} out of ten"); err != nil {
		t.Fatalf("Override score: %v", err)
	}

	after, err := r.Resolve("expand")
	if err != nil {
		t.Fatalf("Resolve after unrelated edit: %v", err)
	}
	afterKey := after.CacheIdentity()

	if beforeKey != afterKey {
		t.Errorf("expand's CacheIdentity changed after an unrelated operation was edited: %q -> %q", beforeKey, afterKey)
	}
}

// TestPromptOverrideSameTextReusesSameVersion proves two overrides with
// byte-identical text mint the same version -- it really is the same
// prompt, not merely the same operation.
func TestPromptOverrideSameTextReusesSameVersion(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.RegisterBuiltin(types.OperationID{Name: "validate", Version: "v1"}, "validate {input}"); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := r.Override("validate", "custom validation text"); err != nil {
		t.Fatalf("Override 1: %v", err)
	}
	first, _ := r.Resolve("validate")

	if err := r.Override("validate", "custom validation text"); err != nil {
		t.Fatalf("Override 2: %v", err)
	}
	second, _ := r.Resolve("validate")

	if first.ID.Version != second.ID.Version {
		t.Errorf("two overrides with identical text got different versions: %q vs %q", first.ID.Version, second.ID.Version)
	}
	if first.CacheIdentity() != second.CacheIdentity() {
		t.Errorf("two overrides with identical text got different cache identities")
	}
}

// TestPromptOverrideDifferentTextMintsDifferentVersion proves editing an
// override's text (without touching anything else) changes both its
// version and its cache identity -- the case that most needs to work, since
// a caller iterating on their own prompt text is exactly the scenario this
// task exists for.
func TestPromptOverrideDifferentTextMintsDifferentVersion(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.RegisterBuiltin(types.OperationID{Name: "negotiate", Version: "v1"}, "negotiate {terms}"); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	if err := r.Override("negotiate", "override draft one"); err != nil {
		t.Fatalf("Override 1: %v", err)
	}
	first, _ := r.Resolve("negotiate")

	if err := r.Override("negotiate", "override draft two, reworded"); err != nil {
		t.Fatalf("Override 2: %v", err)
	}
	second, _ := r.Resolve("negotiate")

	if first.ID.Version == second.ID.Version {
		t.Errorf("editing override text did not change its version: both %q", first.ID.Version)
	}
	if first.CacheIdentity() == second.CacheIdentity() {
		t.Error("editing override text did not change its cache identity")
	}
}

// TestPromptClearOverrideRevertsToBuiltin proves ClearOverride is a real
// revert, not just a "prefer override" toggle that leaves stale state
// around.
func TestPromptClearOverrideRevertsToBuiltin(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.RegisterBuiltin(types.OperationID{Name: "match", Version: "v1"}, "built-in match text"); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := r.Override("match", "override match text"); err != nil {
		t.Fatalf("Override: %v", err)
	}
	r.ClearOverride("match")

	got, err := r.Resolve("match")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != PromptSourceBuiltin || got.Text != "built-in match text" {
		t.Errorf("Resolve after ClearOverride = %+v, want the built-in", got)
	}
	if r.HasOverride("match") {
		t.Error("HasOverride = true after ClearOverride")
	}
}

func TestPromptOverrideRejectsEmptyNameOrText(t *testing.T) {
	r := NewPromptRegistry()
	if err := r.Override("", "text"); err == nil {
		t.Error("expected an error for an empty operation name, got nil")
	}
	if err := r.Override("op", ""); err == nil {
		t.Error("expected an error for empty override text, got nil")
	}
}

// TestPromptCacheIdentityDeterministic proves CacheIdentity is a pure
// function of the Prompt's own fields -- calling it twice on the same
// value never produces two different keys, which is what makes it usable
// as a cache key at all.
func TestPromptCacheIdentityDeterministic(t *testing.T) {
	p := Prompt{ID: types.OperationID{Name: "extract", Version: "v1"}, Text: "hello {name}", Source: PromptSourceBuiltin}
	// A second, independently-constructed value with identical fields --
	// not the same variable read twice -- so this proves CacheIdentity is a
	// pure function of the fields rather than merely returning a cached
	// result on repeat calls to the same receiver.
	same := Prompt{ID: types.OperationID{Name: "extract", Version: "v1"}, Text: "hello {name}", Source: PromptSourceBuiltin}
	if p.CacheIdentity() != same.CacheIdentity() {
		t.Error("CacheIdentity is not deterministic for two Prompt values with identical fields")
	}
}

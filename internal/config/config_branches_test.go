package config

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// SetTraceEnabled's explicit-true branch, mirroring the env-var coverage
// TestGetTraceEnabledHonorsBothEnvNames already has for the fallback.
func TestSetTraceEnabledExplicit(t *testing.T) {
	t.Setenv("SCHEMAFLUX_TRACE", "")
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "")
	SetTraceEnabled(false)
	if GetTraceEnabled() {
		t.Fatal("GetTraceEnabled() = true before SetTraceEnabled(true)")
	}

	SetTraceEnabled(true)
	if !GetTraceEnabled() {
		t.Error("GetTraceEnabled() = false despite SetTraceEnabled(true)")
	}
	SetTraceEnabled(false) // restore
}

// GetTemperature's default branch: an unrecognised or unset Mode gets the
// same balanced temperature as TransformMode, never a zero-value surprise.
func TestGetTemperatureDefaultBranch(t *testing.T) {
	if got := GetTemperature(types.ModeUnset); got != 0.3 {
		t.Errorf("GetTemperature(ModeUnset) = %v, want 0.3 (the balanced default)", got)
	}
	if got := GetTemperature(types.Mode(99)); got != 0.3 {
		t.Errorf("GetTemperature(unrecognised) = %v, want 0.3", got)
	}
}

// tierName's default branch: an unrecognised Speed value must still produce
// a readable label for an error message, not an empty string.
func TestTierNameDefaultBranch(t *testing.T) {
	if got := tierName(types.TierUnset); got != "unknown tier" {
		t.Errorf("tierName(TierUnset) = %q, want \"unknown tier\"", got)
	}
	if got := tierName(types.Speed(99)); got != "unknown tier" {
		t.Errorf("tierName(unrecognised) = %q, want \"unknown tier\"", got)
	}
}

// GetModel's TierUnset path for OpenAI: no per-tier env override applies
// (there is no SCHEMAFLUX_MODEL_ for "unset"), and the intelligence switch
// falls through to its own default, which is the Fast model.
func TestGetModelUnsetTierFallsBackToFastDefault(t *testing.T) {
	clearModelEnv(t)
	if got := GetModel(types.TierUnset, "openai"); got != ModelDefaultFast {
		t.Errorf("GetModel(TierUnset, openai) = %q, want %q", got, ModelDefaultFast)
	}
}

// A mapped provider whose entry is missing exactly one tier's model falls
// back to that provider's own Fast model, not an empty string and not
// another provider's model -- GetModel's `models[intelligence]` /
// `models[types.Fast]` fallback line.
func TestGetModelFallsBackToProvidersOwnFastModelWhenTierMissing(t *testing.T) {
	clearModelEnv(t)
	// Every entry in providerModels defines all three tiers (see
	// TestEveryMappingCoversEveryTier), so this exercises the fallback via
	// TierUnset, which is not a key any entry defines either.
	got := GetModel(types.TierUnset, "anthropic")
	want := providerModels["anthropic"][types.Fast]
	if got != want {
		t.Errorf("GetModel(TierUnset, anthropic) = %q, want the provider's own Fast model %q", got, want)
	}
}

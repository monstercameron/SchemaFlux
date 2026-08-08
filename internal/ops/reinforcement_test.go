package ops

import (
	"strings"
	"testing"
)

// S-007. The reinforcement block is prepended to every request, JSON or not,
// and billed on every call. Making it opt-out is the half that can be settled
// without spending money.
func TestPromptReinforcementCanBeTurnedOff(t *testing.T) {
	const operationPrompt = "You are a data extraction expert."

	on := strengthenSystemPrompt(operationPrompt, "json")
	if !strings.Contains(on, "Perform the semantic task faithfully") {
		t.Fatal("the reinforcement block is missing by default")
	}
	if !strings.Contains(on, operationPrompt) {
		t.Fatal("the operation's own prompt was lost")
	}

	t.Setenv(promptReinforcementEnvVar, "0")
	off := strengthenSystemPrompt(operationPrompt, "json")

	if strings.Contains(off, "Perform the semantic task faithfully") {
		t.Error("the block survived being turned off")
	}
	if off != operationPrompt {
		t.Errorf("with reinforcement off the prompt should be the operation's own, got:\n%s", off)
	}
}

// Only an explicit off means off. Turning it off by accident -- an empty
// variable, a typo -- changes behaviour silently, which is worse than paying
// for the block.
func TestOnlyAnExplicitValueTurnsItOff(t *testing.T) {
	for _, value := range []string{"0", "false", "off", "no", "FALSE", " Off ", "0 "} {
		t.Setenv(promptReinforcementEnvVar, value)
		if promptReinforcementEnabled() {
			t.Errorf("%q did not turn it off", value)
		}
	}

	// Whitespace is trimmed, so " Off " means off; a value that means nothing
	// leaves it on, because turning it off by accident is the worse error.
	for _, value := range []string{"", "1", "true", "on", "yes", "maybe"} {
		t.Setenv(promptReinforcementEnvVar, value)
		if !promptReinforcementEnabled() {
			t.Errorf("%q turned it off", value)
		}
	}
}

// What it costs, stated as a number rather than an impression. This is not a
// quality claim -- whether the block helps needs a live A/B, which is RC-002 --
// but the price is measurable here and a caller deciding is entitled to it.
func TestTheReinforcementBlockHasAMeasuredCost(t *testing.T) {
	const operationPrompt = "You are a data extraction expert."

	for _, format := range []string{"", "json"} {
		t.Setenv(promptReinforcementEnvVar, "1")
		on := strengthenSystemPrompt(operationPrompt, format)

		t.Setenv(promptReinforcementEnvVar, "0")
		off := strengthenSystemPrompt(operationPrompt, format)

		overhead := estimateTokens(on) - estimateTokens(off)
		if overhead <= 0 {
			t.Fatalf("format %q: the block adds no tokens, which cannot be right", format)
		}
		t.Logf("format %q: the reinforcement block costs about %d tokens per call", format, overhead)

		// A guard rather than a golden number: if the block grows into
		// something substantial, that is a decision somebody should make on
		// purpose.
		if overhead > 200 {
			t.Errorf("format %q: the block now costs %d tokens per call, which is worth a decision", format, overhead)
		}
	}
}

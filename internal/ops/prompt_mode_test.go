package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// S-006. Creative on an extraction told the model to "generate plausible values
// for missing fields" and to "prioritize completeness over strict accuracy" --
// an instruction to fabricate, on the one operation whose purpose is
// faithfulness to a source.
func TestCreativeExtractionDoesNotAskForInvention(t *testing.T) {
	prompt := BuildExtractSystemPrompt("{name: string}", types.Creative)

	forbidden := []string{
		"Generate plausible values",
		"plausible values for missing",
		"completeness over strict accuracy",
	}
	for _, phrase := range forbidden {
		if strings.Contains(prompt, phrase) {
			t.Errorf("the Creative extraction prompt still says %q", phrase)
		}
	}

	// And it says the opposite explicitly, because silence on the point is how
	// the model fills in a field anyway.
	if !strings.Contains(prompt, "do NOT invent one") {
		t.Error("the Creative prompt does not tell the model to leave unsupported fields null")
	}
}

// The other two modes keep their meanings: Strict refuses partial data,
// Transform infers. Only invention is gone.
func TestExtractionModesKeepTheirDistinctions(t *testing.T) {
	strict := BuildExtractSystemPrompt("{}", types.Strict)
	transform := BuildExtractSystemPrompt("{}", types.TransformMode)
	creative := BuildExtractSystemPrompt("{}", types.Creative)

	if !strings.Contains(strict, "MUST be present") {
		t.Error("Strict no longer requires required fields")
	}
	if !strings.Contains(transform, "Infer missing fields") {
		t.Error("Transform no longer infers")
	}

	// Three modes have to actually differ, or the option is decoration.
	if strict == transform || transform == creative || strict == creative {
		t.Error("two extraction modes produce the same prompt")
	}
}

// An unset mode still produces a usable prompt rather than an empty ruleset,
// which matters now that ModeUnset is the zero value (A-005).
func TestUnsetModeStillProducesAPrompt(t *testing.T) {
	prompt := BuildExtractSystemPrompt("{name: string}", types.ModeUnset)
	if !strings.Contains(prompt, "Target schema") {
		t.Error("an unset mode produced a prompt without the schema")
	}
	if strings.Contains(prompt, "plausible") {
		t.Error("an unset mode inherited the invention instruction")
	}
}

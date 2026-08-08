package ops

import (
	"errors"
	"fmt"
	"strings"
)

// TC-001. Every prompt segment this library sends has a role -- system or
// user -- and now a trust level, and the trust level is enforced, not just
// recorded: a field nothing enforces is exactly what dead_options_test.go
// exists to catch, one type over. This narrows prompt injection; it does not
// remove it. A model that reads its own user message can still be talked
// into ignoring instructions inside it -- that is a property of the model,
// no Go type changes it. What this DOES guarantee: untrusted text never
// becomes part of the bytes the library calls "system policy," and when it
// does appear in the user message it is wrapped in a boundary the content
// itself cannot forge.

// TrustLevel classifies where a piece of prompt text came from.
type TrustLevel uint8

const (
	// TrustUnspecified is the zero value: nothing has said where this text
	// came from. BuildSystemPrompt treats it as untrusted, the same as
	// every other unsourced segment -- a caller who forgets to set this
	// deliberately gets refused, not silently trusted by default.
	TrustUnspecified TrustLevel = iota

	// TrustPolicy: this library's own fixed rules, written once in this
	// codebase and identical for every caller and every call -- the
	// reinforcement block strengthenSystemPrompt prepends.
	TrustPolicy

	// TrustDeveloperInstruction: an operation's own fixed prompt text --
	// BuildExtractSystemPrompt and its siblings -- authored by this
	// library for a specific operation. Not caller-authored and not
	// model-authored, but not identical across every call the way
	// TrustPolicy is (it varies with the operation and its options).
	TrustDeveloperInstruction

	// TrustApplicationData: the caller's own data -- steering, criteria,
	// the value being operated on. Supplied per call, by code this
	// library does not control and cannot see the contents of in advance.
	TrustApplicationData

	// TrustRetrievedData: text pulled in from outside the direct call -- a
	// tool result, a retrieved document, an evidence source's text.
	TrustRetrievedData

	// TrustModelOutput: a prior model response fed back into a later
	// prompt -- a repair, a pipeline stage reading an earlier stage's
	// answer. Untrusted until the contract layer (Decode/Invariants) has
	// accepted it; TC-002's EvidenceCarrier is the mechanism that decides
	// whether a claim drawn from one gets to stand.
	TrustModelOutput
)

// String names the level, for the boundary marker and for error messages --
// never the content, only the label.
func (t TrustLevel) String() string {
	switch t {
	case TrustPolicy:
		return "trusted policy"
	case TrustDeveloperInstruction:
		return "trusted developer instruction"
	case TrustApplicationData:
		return "untrusted application data"
	case TrustRetrievedData:
		return "untrusted retrieved data"
	case TrustModelOutput:
		return "untrusted model output"
	default:
		return "unspecified"
	}
}

// Trusted reports whether content at this level may be placed in the
// system/policy segment. Only the two levels this library itself authors
// qualify -- everything else, including the zero value, is untrusted.
func (t TrustLevel) Trusted() bool {
	return t == TrustPolicy || t == TrustDeveloperInstruction
}

// PromptSegment is one labelled piece of a prompt: the text, and what may
// be done with it.
type PromptSegment struct {
	Trust   TrustLevel
	Content string
}

// ErrUntrustedInSystemPrompt is returned by BuildSystemPrompt when a
// segment is not trusted. Named so a caller can errors.Is against it rather
// than matching a message -- A-008's lesson about substring-matching a
// classification, applied one layer up from provider errors.
var ErrUntrustedInSystemPrompt = errors.New("ops: untrusted prompt segment may not be placed in the system prompt")

// BuildSystemPrompt concatenates segments into the system/policy prompt,
// refusing outright -- an error, not a filtered-and-continued result -- if
// any segment is not TrustPolicy or TrustDeveloperInstruction.
//
// This is the enforcement half TC-001 asks for: a trust level that only
// labels a segment and never gates anything placed by it is the field
// dead_options_test.go's whole pattern exists to catch. strengthenSystemPrompt
// (llm_helper.go) is this function's caller in the request path every
// operation's system prompt goes through, so this gates every call this
// library makes, not a code path exercised only by a test.
func BuildSystemPrompt(segments ...PromptSegment) (string, error) {
	var kept []string
	for i, seg := range segments {
		if !seg.Trust.Trusted() {
			return "", fmt.Errorf("%w: segment %d has trust level %q", ErrUntrustedInSystemPrompt, i, seg.Trust)
		}
		text := strings.TrimSpace(seg.Content)
		if text != "" {
			kept = append(kept, text)
		}
	}
	return strings.Join(kept, "\n\n"), nil
}

// Boundary markers for untrusted content placed in the user segment. Fixed,
// unambiguous strings rather than anything derived from the content, so a
// boundary looks the same on every call. What has to be defended is the
// reverse direction: content that contains text shaped like one of these
// markers, forging a fake boundary -- sanitizeMarkers neutralizes that
// before the real ones go on.
const (
	untrustedOpen  = "<<<UNTRUSTED CONTENT"
	untrustedClose = "END UNTRUSTED CONTENT>>>"
)

// DelimitUntrusted wraps a segment's content in an explicit boundary naming
// its trust level, with any text inside that could itself look like one of
// the boundary markers neutralized first. Trusted content passes through
// unchanged -- there is nothing to isolate it from.
//
// This is what CA-002's steering seam needed and did not have: steering is
// caller-supplied, per-call text (TrustApplicationData) that lands in the
// user message, and before this it was concatenated in with no marker at
// all -- "Additional instructions:\n" + steering + "\n\n" + userPrompt,
// indistinguishable, once assembled, from an instruction the library wrote
// itself. Wrapping it does not stop a model from acting on injected text
// inside the boundary -- no Go type can do that -- it stops the *library*
// from ever losing track of which bytes were the caller's, which is the
// half of the problem a type system actually reaches.
func DelimitUntrusted(seg PromptSegment) string {
	if seg.Trust.Trusted() {
		return seg.Content
	}
	sanitized := sanitizeMarkers(seg.Content)
	return fmt.Sprintf("%s: %s\n%s\n%s", untrustedOpen, seg.Trust, sanitized, untrustedClose)
}

// sanitizeMarkers neutralizes any occurrence of the boundary markers
// themselves inside untrusted content, so injected text cannot forge a fake
// close (or open) and make a downstream reader -- model or code -- believe
// untrusted text ended earlier, or trusted text began, than it actually did.
func sanitizeMarkers(content string) string {
	replacer := strings.NewReplacer(
		untrustedOpen, "[boundary marker removed]",
		untrustedClose, "[boundary marker removed]",
	)
	return replacer.Replace(content)
}

// ErrTrustBoundaryViolated is returned when untrusted content is found
// inside what is about to be sent as the system/policy prompt.
var ErrTrustBoundaryViolated = errors.New("ops: untrusted content detected inside the system prompt")

// verifyTrustBoundary is CallLLM's last check before a request is built: the
// caller's steering -- TrustApplicationData, always placed in the user
// message by applySteering -- must not appear anywhere in the bytes about to
// be sent as the system prompt. This is not a defence against a
// sufficiently motivated model reading its own user message; nothing here
// is. It is a defence against this library's OWN prompt assembly
// regressing: a future edit that accidentally folds steering into the
// system segment fails the request outright instead of silently shipping a
// caller's per-call text into the block every provider's cache, and every
// log line that prints a system prompt's length or hash, treats as fixed
// policy.
func verifyTrustBoundary(systemPrompt, steering string) error {
	steering = strings.TrimSpace(steering)
	if steering == "" {
		return nil
	}
	if strings.Contains(systemPrompt, steering) {
		return fmt.Errorf("%w: caller-supplied steering appears in the system prompt", ErrTrustBoundaryViolated)
	}
	return nil
}

// package ops - LLM-powered Redact operation for intelligent data masking
package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RedactSpan represents a span of characters to redact
type RedactSpan struct {
	Start    int    `json:"start"`    // Start index (0-based)
	End      int    `json:"end"`      // End index (exclusive)
	Category string `json:"category"` // Type of sensitive data (email, phone, ssn, name, etc.)
	Original string `json:"original"` // The original text that was redacted
}

// RedactLLMResult contains the redaction result with detailed span information
type RedactLLMResult struct {
	Text       string         `json:"text"`       // The redacted text
	Original   string         `json:"original"`   // The original text
	Spans      []RedactSpan   `json:"spans"`      // List of redacted spans
	Categories map[string]int `json:"categories"` // Count per category
	Metadata   map[string]any `json:"metadata"`   // Additional metadata
}

// RedactLLMOptions configures the LLM-powered Redact operation
type RedactLLMOptions struct {
	types.OpOptions
	Categories []string // Categories to detect: email, phone, ssn, name, address, credit_card, password, api_key, custom
	MaskChar   rune     // Character to use for masking (default: '*')
	ShowFirst  int      // Number of characters to show at start (default: 0)
	ShowLast   int      // Number of characters to show at end (default: 0)
	MinMask    int      // Minimum mask length (default: 3)
}

// NewRedactLLMOptions creates RedactLLMOptions with defaults
func NewRedactLLMOptions() RedactLLMOptions {
	return RedactLLMOptions{
		OpOptions: types.OpOptions{
			Mode:         types.Strict,
			Intelligence: types.Fast,
		},
		Categories: []string{"all"}, // Detect all sensitive data by default
		MaskChar:   '*',
		ShowFirst:  0,
		ShowLast:   0,
		MinMask:    3,
	}
}

// WithCategories sets which categories to detect
func (opts RedactLLMOptions) WithCategories(categories []string) RedactLLMOptions {
	opts.Categories = categories
	return opts
}

// WithMaskChar sets the masking character
func (opts RedactLLMOptions) WithMaskChar(char rune) RedactLLMOptions {
	opts.MaskChar = char
	return opts
}

// WithShowFirst shows N characters at the start
func (opts RedactLLMOptions) WithShowFirst(n int) RedactLLMOptions {
	opts.ShowFirst = n
	return opts
}

// WithShowLast shows N characters at the end
func (opts RedactLLMOptions) WithShowLast(n int) RedactLLMOptions {
	opts.ShowLast = n
	return opts
}

// WithMinMask sets minimum mask length
func (opts RedactLLMOptions) WithMinMask(n int) RedactLLMOptions {
	opts.MinMask = n
	return opts
}

// WithIntelligence sets the intelligence level
func (opts RedactLLMOptions) WithIntelligence(intelligence types.Speed) RedactLLMOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// Validate checks if options are valid
func (opts RedactLLMOptions) Validate() error {
	if len(opts.Categories) == 0 {
		return fmt.Errorf("at least one category must be specified")
	}
	if opts.MinMask < 1 {
		return fmt.Errorf("MinMask must be at least 1")
	}
	return nil
}

// llmSpanResponse is the expected JSON response from LLM
type llmSpanResponse struct {
	Spans []struct {
		// Text is the sensitive substring itself, and it is what the library
		// works from. Start and End are accepted as a hint for which
		// occurrence is meant, never as the authority: models count characters
		// and Go slices bytes, so an offset that is correct in the model's
		// terms lands mid-rune in ours, and a bounds check cannot tell the
		// difference between a valid offset and a plausible wrong one.
		Text     string `json:"text"`
		Start    int    `json:"start"`
		End      int    `json:"end"`
		Category string `json:"category"`
	} `json:"spans"`
}

// RedactLLM uses an LLM to identify and redact sensitive data.
//
// # How a span is located
//
// The model returns the sensitive substring, and the library finds it in the
// document. The offsets it also reports are a hint used only to choose between
// repeated occurrences.
//
// Offsets used to be the authority, bounds-checked and otherwise trusted. A
// bounds check cannot tell a correct offset from a plausible wrong one, so a
// span that was in range but off by a few characters masked the wrong part of
// the document and reported the wrong thing as redacted -- which is worse than
// not redacting, because it looks done. Offsets were also the one place where
// the model and Go disagree by construction: the model counts characters, Go
// slices bytes, and the two agree only for ASCII.
//
// A span whose text is not in the document is dropped rather than applied, so a
// hallucinated finding removes nothing. A span with no text is dropped too:
// there is nothing to verify it against, and accepting it would keep the old
// behaviour alive under a new name.
//
// What is still not checked is whether the substring *is* what the model called
// it: a span labelled "ssn" over an order number is applied. Detection is the
// model's judgement, and this layer verifies location, not classification.
//
// # It is a model call
//
// Detection is only as good as the model's, and it is not deterministic. Where
// you know the shape of your data, tag the fields and use Redact.
func RedactLLM(ctx context.Context, text string, opts RedactLLMOptions) (RedactLLMResult, error) {
	logger := telemetry.GetLogger()
	logger.Debug("Starting LLM redact operation", "requestID", opts.RequestID, "textLength", len(text))

	result := RedactLLMResult{
		Original:   text,
		Text:       text,
		Spans:      []RedactSpan{},
		Categories: make(map[string]int),
		Metadata:   make(map[string]any),
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		logger.Error("RedactLLM validation failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("invalid options: %w", err)
	}

	if strings.TrimSpace(text) == "" {
		return result, nil // Nothing to redact
	}

	// Create context with timeout
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = operationContext(opts.Context, 30*time.Second)
		defer cancel()
	}

	// Build prompts
	systemPrompt := buildRedactSystemPrompt(opts)
	userPrompt := buildRedactUserPrompt(text, opts)

	// Call LLM
	opOpts := opts.OpOptions
	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		logger.Error("RedactLLM LLM call failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("LLM call failed: %w", err)
	}

	// Parse LLM response
	spans, err := parseRedactResponse(response, text)
	if err != nil {
		logger.Error("RedactLLM parse failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// The category list is what the caller asked to have detected, and it was
	// stated in the prompt and checked nowhere -- so a caller who narrowed
	// detection to "email" could get a name redacted anyway. That is not a
	// harmless over-redaction: the caller narrowed the list precisely because
	// the other fields have to survive intact downstream.
	//
	// "all" is the default and means what it says, so it opts out of the check.
	// An empty Category on a span is not matched against anything: the span
	// still redacts, and refusing it would turn a model that merely omitted a
	// label into a failed redaction, which fails in the unsafe direction.
	if !containsCategory(opts.Categories, "all") {
		for _, span := range spans {
			if span.Category == "" {
				continue
			}
			if !containsCategory(opts.Categories, span.Category) {
				logger.Error("RedactLLM returned a span outside the requested categories",
					"requestID", opts.RequestID, "category", span.Category)
				return RedactLLMResult{}, fmt.Errorf(
					"redact: a span was categorized %q, which is not among the requested categories", span.Category)
			}
		}
	}

	// Apply redactions. A span the text cannot support is an error: applying it
	// anyway redacts the wrong characters and leaves the sensitive ones in
	// place, which is worse than not redacting at all because it looks done.
	result.Spans = spans
	redacted, err := applyRedactions(text, spans, opts)
	if err != nil {
		return RedactLLMResult{}, fmt.Errorf("redaction failed: %w", err)
	}
	result.Text = redacted

	// Count categories
	for _, span := range spans {
		result.Categories[span.Category]++
	}

	result.Metadata["mask_char"] = string(opts.MaskChar)
	result.Metadata["categories_requested"] = opts.Categories
	result.Metadata["spans_found"] = len(spans)

	logger.Debug("RedactLLM succeeded", "requestID", opts.RequestID, "spansFound", len(spans))

	return result, nil
}

// buildRedactSystemPrompt creates the system prompt for redaction
func buildRedactSystemPrompt(opts RedactLLMOptions) string {
	prompt := `You are a sensitive data detection expert. Your task is to identify ALL sensitive information in the given text and quote it back exactly.

Return a JSON object with a "spans" array. Each span must have:
- "text": the sensitive substring, copied exactly as it appears in the input
- "start": a 0-based position hint, used only to disambiguate repeated values
- "category": the type of sensitive data

Categories to detect:
`

	if containsCategory(opts.Categories, "all") {
		prompt += `- email: Email addresses
- phone: Phone numbers (any format)
- ssn: Social Security Numbers
- name: Person names (first, last, full)
- address: Physical addresses
- credit_card: Credit card numbers
- password: Passwords or secrets in text
- api_key: API keys, tokens, secrets
- ip: IP addresses
- date_of_birth: Birth dates
- financial: Bank accounts, amounts with context
`
	} else {
		for _, cat := range opts.Categories {
			prompt += fmt.Sprintf("- %s\n", cat)
		}
	}

	prompt += `
IMPORTANT:
1. Be thorough - find ALL instances of sensitive data
2. "text" must be the sensitive substring copied EXACTLY as it appears, character for character
3. "start" is a 0-based position hint; it is used only to pick between repeated occurrences
4. Return ONLY valid JSON, no explanations
5. If no sensitive data found, return {"spans": []}

Example for "Contact john@email.com or call 555-1234":
{"spans": [{"text": "john@email.com", "start": 8, "category": "email"}, {"text": "555-1234", "start": 31, "category": "phone"}]}`

	return prompt
}

// buildRedactUserPrompt creates the user prompt
func buildRedactUserPrompt(text string, opts RedactLLMOptions) string {
	return fmt.Sprintf("Find all sensitive data in this text and return their positions as JSON:\n\n%s", text)
}

// parseRedactResponse parses the LLM response into spans
func parseRedactResponse(response, originalText string) ([]RedactSpan, error) {
	// Clean up response - remove markdown code blocks
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var llmResponse llmSpanResponse
	if err := ParseJSONStrict(response, &llmResponse); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}

	// Locate each span by its text rather than by the offsets the model
	// reported.
	//
	// The offsets were bounds-checked and otherwise trusted, and a bounds check
	// cannot tell a correct offset from a plausible wrong one -- so a span that
	// was in range but off by a few characters masked the wrong part of the
	// document and reported the wrong thing as redacted. Searching for the
	// substring also removes the character-versus-byte mismatch entirely: the
	// model counts runes, Go slices bytes, and the two agree only for ASCII.
	spans := make([]RedactSpan, 0, len(llmResponse.Spans))
	consumed := make(map[string]int, len(llmResponse.Spans))

	for _, s := range llmResponse.Spans {
		if s.Text == "" {
			// Nothing to verify against. A span with only offsets is exactly
			// the case this task removed, and accepting it would keep the old
			// behaviour alive under a new name.
			continue
		}

		start := locateSpan(originalText, s.Text, s.Start, consumed[s.Text])
		if start < 0 {
			// The model reported text that is not in the document. That is a
			// hallucinated span; masking anything for it would remove content
			// the model never actually found.
			continue
		}
		consumed[s.Text] = start + len(s.Text)

		spans = append(spans, RedactSpan{
			Start:    start,
			End:      start + len(s.Text),
			Category: s.Category,
			Original: s.Text,
		})
	}

	// Sort spans by start position and merge overlapping
	spans = mergeOverlappingSpans(spans)

	return spans, nil
}

// mergeOverlappingSpans sorts and merges overlapping redaction spans
func mergeOverlappingSpans(spans []RedactSpan) []RedactSpan {
	if len(spans) <= 1 {
		return spans
	}

	// Simple bubble sort by start (spans are usually small)
	for i := 0; i < len(spans)-1; i++ {
		for j := i + 1; j < len(spans); j++ {
			if spans[j].Start < spans[i].Start {
				spans[i], spans[j] = spans[j], spans[i]
			}
		}
	}

	// Merge overlapping
	merged := []RedactSpan{spans[0]}
	for i := 1; i < len(spans); i++ {
		last := &merged[len(merged)-1]
		if spans[i].Start <= last.End {
			// Overlapping - extend the last span
			if spans[i].End > last.End {
				last.End = spans[i].End
				last.Original = "" // Can't preserve original for merged spans
				last.Category = last.Category + "," + spans[i].Category
			}
		} else {
			merged = append(merged, spans[i])
		}
	}

	return merged
}

// applyRedactions applies the redaction spans to the text
// applyRedactions splices the masked spans into the text. The spans carry
// character offsets reported by the model, so the slicing is rune-indexed --
// byte indexing cut multi-byte runes in half -- and out-of-range or overlapping
// spans are refused rather than applied, because a bad offset here silently
// redacts the wrong characters and leaves the right ones in place.
func applyRedactions(text string, spans []RedactSpan, opts RedactLLMOptions) (string, error) {
	if len(spans) == 0 {
		return text, nil
	}

	runes := []rune(text)
	total := len(runes)

	ordered := make([]RedactSpan, len(spans))
	copy(ordered, spans)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })

	var result strings.Builder
	lastEnd := 0

	for i, span := range ordered {
		switch {
		case span.Start < 0 || span.End < 0:
			return "", fmt.Errorf("redaction span %d has a negative offset [%d:%d]", i, span.Start, span.End)
		case span.Start > span.End:
			return "", fmt.Errorf("redaction span %d is inverted [%d:%d]", i, span.Start, span.End)
		case span.End > total:
			return "", fmt.Errorf("redaction span %d [%d:%d] exceeds the text, which is %d characters",
				i, span.Start, span.End, total)
		case span.Start < lastEnd:
			return "", fmt.Errorf("redaction span %d [%d:%d] overlaps the previous one, which ended at %d",
				i, span.Start, span.End, lastEnd)
		}

		if span.Start > lastEnd {
			result.WriteString(string(runes[lastEnd:span.Start]))
		}
		result.WriteString(maskText(string(runes[span.Start:span.End]), opts))
		lastEnd = span.End
	}

	if lastEnd < total {
		result.WriteString(string(runes[lastEnd:]))
	}

	return result.String(), nil
}

// maskText applies the masking strategy to sensitive text
func maskText(text string, opts RedactLLMOptions) string {
	// ShowFirst and ShowLast are documented in characters. Indexing bytes meant
	// that revealing "the first four characters" of a name beginning with a
	// multi-byte rune cut it in half, producing invalid UTF-8 in a function
	// whose entire purpose is to be careful with the text it is handed.
	runes := []rune(text)
	textLen := len(runes)

	showFirst := opts.ShowFirst
	showLast := opts.ShowLast

	// Ensure we don't show more than the text length
	if showFirst+showLast >= textLen {
		// If showing first+last would reveal everything, just mask all
		showFirst = 0
		showLast = 0
	}
	if showFirst < 0 {
		showFirst = 0
	}
	if showLast < 0 {
		showLast = 0
	}

	maskLen := textLen - showFirst - showLast
	if maskLen < opts.MinMask {
		maskLen = opts.MinMask
	}

	var result strings.Builder

	if showFirst > 0 && showFirst < textLen {
		result.WriteString(string(runes[:showFirst]))
	}

	for i := 0; i < maskLen; i++ {
		result.WriteRune(opts.MaskChar)
	}

	if showLast > 0 && showLast < textLen {
		result.WriteString(string(runes[textLen-showLast:]))
	}

	return result.String()
}

// containsCategory checks if a category list contains a specific category
func containsCategory(categories []string, target string) bool {
	for _, c := range categories {
		if strings.EqualFold(c, target) {
			return true
		}
	}
	return false
}

// locateSpan finds the occurrence of value that the model most likely meant.
//
// The reported start is a hint: the nearest occurrence at or after it wins,
// because a model that miscounts usually miscounts low. Failing that, the first
// occurrence not already claimed by an earlier span is used, so a value
// appearing three times produces three distinct spans rather than three copies
// of the first.
func locateSpan(text, value string, hint, from int) int {
	if value == "" {
		return -1
	}

	// Every occurrence not already claimed by an earlier span, so a value
	// appearing three times produces three distinct spans rather than three
	// copies of the first.
	var candidates []int
	for at := from; at <= len(text); {
		if at < 0 {
			at = 0
		}
		found := strings.Index(text[at:], value)
		if found < 0 {
			break
		}
		candidates = append(candidates, at+found)
		at = at + found + 1
	}
	if len(candidates) == 0 {
		// Everything at or after `from` is taken, or the value is not there at
		// all. Fall back to the whole document before giving up: a model that
		// reports spans out of order should not lose one.
		if at := strings.Index(text, value); at >= 0 {
			return at
		}
		return -1
	}

	// The nearest candidate to the hint, in either direction. A model that
	// miscounts usually miscounts low -- it counts runes where Go counts bytes
	// -- but it can miscount high too, and "first occurrence at or after the
	// hint" silently skips a correct occurrence that starts one byte earlier.
	best := candidates[0]
	bestDistance := abs(best - hint)
	for _, candidate := range candidates[1:] {
		if distance := abs(candidate - hint); distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// package ops - LLM-powered Redact operation for intelligent data masking
package ops

import (
	"context"
	"encoding/json"
	"fmt"
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
		Start    int    `json:"start"`
		End      int    `json:"end"`
		Category string `json:"category"`
	} `json:"spans"`
}

// RedactLLM uses LLM to intelligently identify and redact sensitive data
// It returns the redacted text with character-level precision
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
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
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

	// Apply redactions
	result.Spans = spans
	result.Text = applyRedactions(text, spans, opts)

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
	prompt := `You are a sensitive data detection expert. Your task is to identify ALL sensitive information in the given text and return their exact character positions.

Return a JSON object with a "spans" array. Each span must have:
- "start": the 0-based index where the sensitive data starts
- "end": the 0-based index where it ends (exclusive, like Python slicing)
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
2. Indices must be exact character positions (0-based)
3. "end" is exclusive (text[start:end] gives the sensitive part)
4. Return ONLY valid JSON, no explanations
5. If no sensitive data found, return {"spans": []}

Example for "Contact john@email.com or call 555-1234":
{"spans": [{"start": 8, "end": 22, "category": "email"}, {"start": 31, "end": 39, "category": "phone"}]}`

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
	if err := json.Unmarshal([]byte(response), &llmResponse); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}

	// Convert to RedactSpan and validate indices
	spans := make([]RedactSpan, 0, len(llmResponse.Spans))
	textLen := len(originalText)

	for _, s := range llmResponse.Spans {
		// Validate indices
		if s.Start < 0 || s.End > textLen || s.Start >= s.End {
			continue // Skip invalid spans
		}

		span := RedactSpan{
			Start:    s.Start,
			End:      s.End,
			Category: s.Category,
			Original: originalText[s.Start:s.End],
		}
		spans = append(spans, span)
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
func applyRedactions(text string, spans []RedactSpan, opts RedactLLMOptions) string {
	if len(spans) == 0 {
		return text
	}

	// Build result string
	var result strings.Builder
	lastEnd := 0

	for _, span := range spans {
		// Add text before this span
		if span.Start > lastEnd {
			result.WriteString(text[lastEnd:span.Start])
		}

		// Apply mask to this span
		sensitiveText := text[span.Start:span.End]
		maskedText := maskText(sensitiveText, opts)
		result.WriteString(maskedText)

		lastEnd = span.End
	}

	// Add remaining text after last span
	if lastEnd < len(text) {
		result.WriteString(text[lastEnd:])
	}

	return result.String()
}

// maskText applies the masking strategy to sensitive text
func maskText(text string, opts RedactLLMOptions) string {
	textLen := len(text)

	// Calculate how many characters to mask
	showFirst := opts.ShowFirst
	showLast := opts.ShowLast

	// Ensure we don't show more than the text length
	if showFirst+showLast >= textLen {
		// If showing first+last would reveal everything, just mask all
		showFirst = 0
		showLast = 0
	}

	maskLen := textLen - showFirst - showLast
	if maskLen < opts.MinMask {
		maskLen = opts.MinMask
	}

	// Build masked text
	var result strings.Builder

	// Show first N characters
	if showFirst > 0 && showFirst < textLen {
		result.WriteString(text[:showFirst])
	}

	// Add mask characters
	for i := 0; i < maskLen; i++ {
		result.WriteRune(opts.MaskChar)
	}

	// Show last N characters
	if showLast > 0 && showLast < textLen {
		result.WriteString(text[textLen-showLast:])
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
